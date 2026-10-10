package lifecycle

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Target names the model to start by its provider-side name — exactly
// config.Model.ModelName. For a discovered model that is the artifact name, so
// no registry entry is required.
//
// ModelID is the catalog row's id (a registry id or a discovered id). The
// route hook writes the route under it, so the id in config.yaml is the one
// the picker showed. It may be empty: the hook then derives the model from
// ProviderID and ModelName (see StartRouteChange).
type Target struct{ ProviderID, ModelName, ModelID string }

// Stage is a progress milestone reported during Start.
type Stage string

const (
	StageStoppingOccupant Stage = "stopping-occupant"
	StageStarting         Stage = "starting"
	StageWaiting          Stage = "waiting-for-model"
	StageWarming          Stage = "warming"
	// StageRouting covers the LiteLLM route update that follows a successful
	// start: rewriting config.yaml, bouncing the proxy and waiting for it
	// back. Without it callers keep rendering the engine's last stage
	// ("warming the model") for the whole route window.
	StageRouting Stage = "routing"
)

// Options tunes Start. AllowReplace permits displacing running models (an
// Exclusive server's occupant, or a Pool's evictions); Progress (optional)
// receives stages in order.
type Options struct {
	AllowReplace bool
	Progress     func(Stage)
	// Out (optional) receives every line the start prints, in place of
	// stderr: what a Pool start unloaded, a route that could not be written,
	// and the warnings of the proxy restart. nil leaves them on stderr. It
	// is for a caller that owns the screen (the model picker's alt screen
	// hides stderr). Writes are serialised (routesOutMu), but the restart's
	// warnings are written by a goroutine that outlives Start, so the text
	// is complete only once WaitPendingRoutes has returned.
	Out io.Writer
}

// OccupiedError means starting the target would displace running models and
// AllowReplace was false. Nothing was touched. An Exclusive server has exactly
// one occupant; a Pool can have several.
type OccupiedError struct{ Occupants []localmodels.Entry }

// IDs lists the occupants' model ids, in the order they would go.
func (e *OccupiedError) IDs() []string {
	ids := make([]string, len(e.Occupants))
	for i, o := range e.Occupants {
		ids[i] = o.ModelID
	}
	return ids
}

func (e *OccupiedError) Error() string {
	return fmt.Sprintf("starting this model would stop %s — confirm to replace", strings.Join(e.IDs(), ", "))
}

// DaemonDownError means the provider's daemon is not answering.
type DaemonDownError struct{ Provider, Origin string }

func (e *DaemonDownError) Error() string {
	return fmt.Sprintf("%s is not answering at %s — start the %s app/service first", e.Provider, e.Origin, e.Provider)
}

// BinaryMissingError means a required provider binary is not on PATH.
type BinaryMissingError struct{ Binary string }

func (e *BinaryMissingError) Error() string {
	return fmt.Sprintf("%s binary not found on PATH", e.Binary)
}

// PortBusyError means the provider's port still answers when a fresh server
// needs it, and no known model explains it — wt never kills unknown processes.
type PortBusyError struct{ Port int }

func (e *PortBusyError) Error() string {
	return fmt.Sprintf("port %d is in use by another process — stop it before starting this model", e.Port)
}

// OccupancyUnknownError means the provider's live state could not be
// determined: its server accepted a connection but did not give a usable
// answer, so starting could replace a model that is still running. Nothing was
// touched.
type OccupancyUnknownError struct{ ProviderID, Origin string }

func (e *OccupancyUnknownError) Error() string {
	return fmt.Sprintf("cannot tell whether %s at %s is already serving a model — confirm before replacing it", e.ProviderID, e.Origin)
}

// KeyRefusedError means a provider's server refused a request for want of a
// valid API key (HTTP 401 or 403). Waiting does not change that answer, so a
// warmup stops on it instead of polling to its timeout.
type KeyRefusedError struct {
	URL     string
	KeySent bool   // the registry named a key and it was sent
	Detail  string // the server's own answer
}

func (e *KeyRefusedError) Error() string {
	if e.KeySent {
		return fmt.Sprintf("%s refused the API key the registry provider's auth.secret_ref names (%s)", e.URL, e.Detail)
	}
	return fmt.Sprintf("%s wants an API key (%s): set auth.secret_ref on the registry's provider", e.URL, e.Detail)
}

// UnsupportedError means wt has no lifecycle backend for the provider.
type UnsupportedError struct{ ProviderID string }

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("wt cannot start models for provider %q", e.ProviderID)
}

// backend is one provider family's lifecycle.
type backend interface {
	// tenancy reports how the provider's server holds models.
	tenancy() Tenancy
	// stop stops the provider's current occupant and waits for it to release its port.
	stop(ctx context.Context, e *env, cfg *config.Config) error
	// stopModel stops the one named model (provider-side name). Shared and
	// Pool providers unload just that model; an Exclusive provider stops its
	// only occupant, so modelName is informational there.
	stopModel(ctx context.Context, e *env, cfg *config.Config, modelName string) error
	// start runs everything from spawn through warmup for t, reporting stages.
	start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error
}

// SameModel reports whether two provider-side names denote the same model under
// the family's matching rule (exported so internal/survey counts usage the way
// the engine matches occupants).
func SameModel(family, a, b string) bool {
	if family == "ollama" {
		return localmodels.OllamaNameMatches(a, b) || localmodels.OllamaNameMatches(b, a)
	}
	return localmodels.NameMatches(a, b)
}

// ProbeTrusted reports whether snap's Running flags for family were produced by
// a probe that could actually determine them. An absent status counts as
// trusted: inventory registers a family's key whenever this config has a local
// provider or model for it, so an absent key means nothing is being started
// into that family — and hand-built snapshots in tests carry no status map.
//
// Exported because the post-exit stop picker (internal/survey) filters its
// candidates by this same rule: it must not offer a model whose running state
// no probe confirmed, or on a single-model provider it would stop whatever is
// actually loaded rather than the row the user ticked. Keeping one
// implementation is the point — a tightened rule here must reach both paths.
func ProbeTrusted(snap localmodels.Snapshot, family string) bool {
	st, ok := snap.Providers[family]
	if !ok {
		return true
	}
	return st == localmodels.StatusOK
}

// isRunning reports whether live Inventory already shows the target serving:
// running, and not still loading. A target omlx is mid-load on is not serving
// yet (#259); isLoading answers for it.
func isRunning(snap localmodels.Snapshot, family string, t Target) bool {
	return targetIs(snap, family, t, func(en localmodels.Entry) bool { return en.Running && !en.Loading })
}

// isLoading reports whether live Inventory shows the target mid-load. A start
// on such a target is not a no-op: it joins the load and returns when the
// model is loaded.
func isLoading(snap localmodels.Snapshot, family string, t Target) bool {
	return targetIs(snap, family, t, func(en localmodels.Entry) bool { return en.Running && en.Loading })
}

// targetIs reports whether a trusted snapshot has an entry for the target that
// satisfies is.
func targetIs(snap localmodels.Snapshot, family string, t Target, is func(localmodels.Entry) bool) bool {
	if !ProbeTrusted(snap, family) {
		return false
	}
	for _, en := range snap.Entries {
		if is(en) && localmodels.Family(en.ProviderID) == family && SameModel(family, en.ModelName, t.ModelName) {
			return true
		}
	}
	return false
}

// backendsByFamily is the single source of truth for which providers wt can
// start and how each one's server holds models (tenancy). defaultEnv copies
// it, and TenancyOf reads it (it has no *env). Keeping these in
// agreement used to be manual — the occupancy check held its own hardcoded
// family list — so a new single-model backend could be startable while the
// check still reported no occupant, replacing a running model without a
// confirmation.
var backendsByFamily = map[string]backend{
	"ollama": ollamaBackend{},
	"omlx":   omlxBackend{},
	"mtplx":  mtplxBackend{},
}

// resolveEvictions decides which running models starting t would displace. It
// prefers the snapshot, which is free, and re-probes the server only when the
// snapshot's probe could not be trusted — the case that used to read as "no
// occupant" and let a start evict the running model. The re-probe has no
// sizes, so on a Pool every other loaded model is a victim. A family the
// snapshot saw refuse the connection is re-probed too, though the plan calls
// it known-empty (evictions): the engine is about to act, and a server that
// came up since the snapshot must not have its model replaced unasked. unknown
// reports that even the re-probe could not tell; the caller must then require
// AllowReplace.
func (e *env) resolveEvictions(ctx context.Context, cfg *config.Config, family string, t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, unknown bool) {
	b := e.backends[family]
	if b == nil {
		return nil, false
	}
	ten := b.tenancy()
	// A Shared server displaces nobody whatever its state, so it is never
	// re-probed.
	reprobeDown := snap.Down[family] && (ten == Exclusive || ten == Pool)
	if v, known := evictions(ten, family, t, snap); known && !reprobeDown {
		return v, false
	}
	served, known := e.liveServed(ctx, cfg, family)
	if !known {
		return nil, true
	}
	for _, id := range served {
		if SameModel(family, id, t.ModelName) {
			continue // the target itself is already served: not an occupant
		}
		victims = append(victims, localmodels.Entry{ProviderID: t.ProviderID, ModelID: id, ModelName: id, Running: true})
		if ten == Exclusive {
			break
		}
	}
	return victims, false
}

// Start starts t. It is a no-op when live Inventory shows t already running.
// A t that omlx is still loading is not running yet: Start joins that load
// and returns once the model is loaded, asking nothing (see evictions). It
// returns *OccupiedError (touching nothing) when it would replace a running
// model and opts.AllowReplace is false, returns *OccupancyUnknownError
// (touching nothing) when its server accepted a connection but could not say
// what it is serving, and otherwise stops the occupant of an Exclusive
// provider (mtplx), if any, and runs the provider's start sequence. On a Pool
// (omlx) a start loads beside the models already loaded: nothing is stopped,
// omlx evicts what it must, and the routes of whatever it unloaded are removed
// afterwards, whether or not the load succeeded (reconcilePool). After a
// successful start the model's LiteLLM route is updated (routes.go), announced
// as StageRouting so callers stop rendering the engine's last stage while the
// proxy is bounced.
func Start(ctx context.Context, cfg *config.Config, t Target, opts Options) error {
	return startWith(ctx, defaultEnv(), cfg, t, opts)
}

// startWith is Start on a given env. Everything the start prints goes through
// routePrintf with this context, the asynchronous restart included, so the
// one line below is what sends all of it to opts.Out.
func startWith(ctx context.Context, e *env, cfg *config.Config, t Target, opts Options) error {
	ctx = withRouteOutput(ctx, opts.Out)
	if err := start(ctx, e, cfg, t, opts); err != nil {
		if e.restartOwed {
			// A stopped occupant's or an evicted model's route was already
			// dropped from config.yaml; the proxy still serves the old list
			// until it is bounced.
			bounceRoutes(ctx, cfg)
		}
		return err
	}
	if opts.Progress != nil {
		opts.Progress(StageRouting)
	}
	routeAfterStart(ctx, cfg, t, e.restartOwed)
	return nil
}

func start(ctx context.Context, e *env, cfg *config.Config, t Target, opts Options) error {
	family := localmodels.Family(t.ProviderID)
	b := e.backends[family]
	if b == nil {
		return &UnsupportedError{ProviderID: t.ProviderID}
	}
	report := func(s Stage) {
		if opts.Progress != nil {
			opts.Progress(s)
		}
	}
	snap := e.inventory(cfg)
	if isRunning(snap, family, t) {
		return nil
	}
	victims, unknown := e.resolveEvictions(ctx, cfg, family, t, snap)
	if unknown && !opts.AllowReplace {
		origin, _ := localmodels.FamilyOrigin(cfg, family)
		return &OccupancyUnknownError{ProviderID: t.ProviderID, Origin: origin}
	}
	if len(victims) > 0 && !opts.AllowReplace {
		return &OccupiedError{Occupants: victims}
	}
	if b.tenancy() == Exclusive && len(victims) > 0 {
		occ := victims[0]
		report(StageStoppingOccupant)
		if err := b.stop(ctx, e, cfg); err != nil {
			return fmt.Errorf("stopping %s before starting %s: %w", occ.ModelID, t.ModelName, err)
		}
		// The occupant is down now. Drop its route here rather than after the
		// start, because a start that fails from here on returns without any
		// route hook and would strand the dead occupant's model_list row.
		if e.onOccupantStopped != nil && e.onOccupantStopped(ctx, cfg, occ) {
			e.restartOwed = true
		}
	}
	if b.tenancy() != Pool {
		return b.start(ctx, e, cfg, t, report)
	}
	// What was loaded before the load, to compare with what is loaded after:
	// omlx decides what to evict, and can evict more or less than the plan
	// named — or evict and then fail the load.
	before := victims
	if ProbeTrusted(snap, family) {
		before = runningOthers(snap, family, t)
	}
	// A start that joins a load planned nothing itself (evictions): what that
	// load evicts was put to whoever began it, so none of it is this start's
	// surprise and none is marked "not predicted".
	planned := victims
	if ProbeTrusted(snap, family) && isLoading(snap, family, t) {
		planned = before
	}
	err := b.start(ctx, e, cfg, t, report)
	e.reconcilePool(ctx, cfg, before, planned)
	return err
}

// reconcilePool finds the models a pool load unloaded — those of before that
// omlx no longer has loaded — and, for each, removes its route (the deferred
// write Start's one settling bounce applies), says so, and tells the caller.
// planned is what the eviction plan named; anything else is marked, because
// omlx evicts from a soft watermark wt cannot see. A start that joined a load
// passes before as planned: the plan was the first start's, not its own.
//
// Only a status reading (SizesKnown) can show an eviction. The fallback
// reading names what is loaded by the ids of omlx's list, which gives an
// aliased model its alias: a sibling that is still loaded is then missing from
// it by directory name, and reading that as "unloaded" would remove the route
// of a model that is serving. So a pool that cannot be read now, or that
// answers only through the fallback, changes nothing: `wt litellm sync`
// reconciles the routes.
func (e *env) reconcilePool(ctx context.Context, cfg *config.Config, before, planned []localmodels.Entry) {
	if len(before) == 0 {
		return
	}
	pool, err := localmodels.OmlxPool(cfg, e.probeClient)
	if err != nil || !pool.SizesKnown {
		return
	}
	for _, en := range before {
		name := en.Artifact
		if name == "" {
			name = en.ModelName
		}
		if m, ok := pool.Find(name); ok && (m.Loaded || m.Loading) {
			continue
		}
		note := " (not predicted)"
		for _, p := range planned {
			if p.ModelID == en.ModelID {
				note = ""
			}
		}
		routePrintf(ctx, "wt: omlx unloaded %s to make room%s\n", en.ModelID, note)
		if e.onOccupantStopped != nil && e.onOccupantStopped(ctx, cfg, en) {
			e.restartOwed = true
		}
	}
}

// Stop stops the provider as a whole: mtplx's process, or the omlx service
// with every model it has loaded. A no-op for multi-tenant ollama. After a
// successful stop the family's LiteLLM routes are removed (routes.go).
func Stop(ctx context.Context, cfg *config.Config, providerID string) error {
	if err := stop(ctx, defaultEnv(), cfg, providerID); err != nil {
		return err
	}
	routeAfterStop(ctx, cfg, providerID)
	return nil
}

// stop is Stop's injectable core, the same shape Start/start uses so tests can
// drive the real dispatch without a live provider.
func stop(ctx context.Context, e *env, cfg *config.Config, providerID string) error {
	b := e.backends[localmodels.Family(providerID)]
	if b == nil {
		return &UnsupportedError{ProviderID: providerID}
	}
	return b.stop(ctx, e, cfg)
}

// StopModelDeferred stops one running model and writes its route removal,
// leaving the LiteLLM proxy restart to the caller (issue #142). en is the
// inventory entry: ProviderID and ModelName name what to stop, ModelID the
// route id its row was built under, which the removal needs to tell the model
// from a sibling whose name merely resembles it (poolRouteIDs, #195). On ollama
// it unloads just that model; on an Exclusive provider (mtplx) it stops the
// provider's sole occupant; on a Pool (omlx) it unloads that one model, leaving
// the service and its loaded siblings up and routed. The caller must only pass
// a model live Inventory reported running.
//
// restartOwed reports that a removal was written and the proxy therefore still
// needs its restart: the caller must call SettleRoutes once after the last stop
// of a batch when any call owed one — including when the batch ends early on
// Ctrl+C or a failure, since every removal already written still needs that
// restart. One settling bounce replaces N overlapping restarts, each of which
// killed the proxy the previous one had just brought up. A failed stop writes
// nothing and owes nothing.
func StopModelDeferred(ctx context.Context, cfg *config.Config, en localmodels.Entry) (restartOwed bool, err error) {
	if err := stopModel(ctx, defaultEnv(), cfg, en.ProviderID, en.ModelName); err != nil {
		return false, err
	}
	return routeRemoveModel(ctx, cfg, en, restartDeferred), nil
}

// SettleRoutes restarts the LiteLLM proxy (and waits for it, asynchronously —
// see WaitPendingRoutes) to apply the route removals a StopModelDeferred batch
// wrote. It rewrites nothing: the removals are already in config.yaml, and
// every caller reaches it because some StopModelDeferred reported restartOwed.
// It runs detached from ctx, so a batch cut short by Ctrl+C still settles what
// it wrote.
func SettleRoutes(ctx context.Context, cfg *config.Config) { bounceRoutes(ctx, cfg) }

// stopModel is StopModelDeferred's injectable core, the same shape as
// stop/start.
func stopModel(ctx context.Context, e *env, cfg *config.Config, providerID, modelName string) error {
	b := e.backends[localmodels.Family(providerID)]
	if b == nil {
		return &UnsupportedError{ProviderID: providerID}
	}
	return b.stopModel(ctx, e, cfg, modelName)
}

// Startable reports whether wt has a lifecycle backend for providerID's family
// (ollama, omlx/omlx-6bit, mtplx). backendsByFamily is the single source of
// truth; internal/catalog delegates here so the picker's "can wt start this?"
// answer cannot drift from the engine's.
func Startable(providerID string) bool {
	return backendsByFamily[localmodels.Family(providerID)] != nil
}

// CanStop reports whether wt has a stop backend for providerID's family, so a
// picker never offers a model (e.g. one on mlx_lm_server) that
// StopModelDeferred would refuse with *UnsupportedError. Every backend
// implements both start and stop, so this is Startable under its stop-side
// name.
func CanStop(providerID string) bool { return Startable(providerID) }
