package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
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
	// StageStoppingOthers covers the stops of other providers' models the
	// caller approved (Options.Others), made before anything else.
	StageStoppingOthers   Stage = "stopping-others"
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
	// is complete only once WaitPendingRoutes has returned (SettleStart, for
	// a start that succeeded).
	Out io.Writer
	// RestartOwed says config.yaml was already changed for this launch and
	// the proxy has not been restarted for it. Start's one restart then
	// publishes that change too: on success the route hook restarts even when
	// its own write changed nothing, and a start that returns any error other
	// than a refusal restarts on its way out, whether or not it had touched
	// anything by then. Only the two refusals RestartStillOwed names, an
	// *OccupiedError and an *OccupancyUnknownError, leave the restart with
	// the caller.
	RestartOwed bool
	// Others is the caller's answer about the models of other provider
	// families loaded beside the target (residents, #412). Its zero value
	// means "not asked yet": a start that finds any such model, or a family
	// it cannot read, refuses with an *OccupiedError that lists them, and
	// touches nothing. See OthersAnswer.
	Others OthersAnswer
}

// OthersAnswer is what the user decided about the models of other provider
// families loaded beside the target. Decided false is the question not yet
// asked. Decided with an empty Stop keeps them all and starts. Stop lists the
// model ids (Resident.Entry.ModelID) the user saw and approved stopping; the
// start stops each of them that is still loaded and stoppable before it does
// anything else, and keeps every other. Cancel is the caller not retrying.
type OthersAnswer struct {
	Decided bool
	Stop    []string
}

// Resident is a model of another provider family that is loaded while a
// start runs: it holds memory the target will share. Stoppable is false when
// wt has no backend that can stop it (mlx_lm_server), or when it is an omlx
// model still loading, which omlx refuses to unload. PID is set for a process
// server (mtplx) still loading, known only by wt's pidfile: that one is
// stopped only through the verified pidfile stop (stopLoadingServer), never by
// a pid alone.
type Resident struct {
	Entry     localmodels.Entry
	Stoppable bool
	PID       int
}

// StoppableIDs are the model ids of the residents wt can stop, in the
// engine's order: what a stop answer approves.
func StoppableIDs(others []Resident) []string {
	var ids []string
	for _, r := range others {
		if r.Stoppable {
			ids = append(ids, r.Entry.ModelID)
		}
	}
	return ids
}

// Label is the resident's id as a line names it: the model id, with
// " (loading)" for a model that is still loading.
func (r Resident) Label() string {
	if r.Entry.Loading || r.PID > 0 {
		return r.Entry.ModelID + " (loading)"
	}
	return r.Entry.ModelID
}

// Figure is the memory the resident holds as wt knows it: what its server
// says (Entry.LoadedSize, printed with localmodels.FormatSize), else "memory
// use unknown", never 0 and never a guess; with "; wt cannot stop it" when
// it is not Stoppable (a clause, not a parenthesis: the warning line puts the
// whole figure in parentheses).
func (r Resident) Figure() string {
	fig := "memory use unknown"
	if r.Entry.LoadedSize > 0 {
		switch localmodels.Family(r.Entry.ProviderID) {
		case "omlx":
			fig = localmodels.FormatSize(r.Entry.LoadedSize) + " resident (omlx estimate)"
		default:
			fig = localmodels.FormatSize(r.Entry.LoadedSize) + " loaded"
		}
	}
	if !r.Stoppable {
		fig += "; wt cannot stop it"
	}
	return fig
}

// RestartStillOwed reports whether a Start that was given RestartOwed and
// returned err left that restart unsettled: err is, or wraps, an
// *OccupiedError (same-family occupants, other providers' residents, or both)
// or an *OccupancyUnknownError, the two refusals that touch nothing. The
// caller then retries with RestartOwed set again, or calls SettleRoutes when
// it gives up. Every other outcome of such a Start, nil included, has started
// the one restart.
func RestartStillOwed(err error) bool {
	var occupied *OccupiedError
	var unknown *OccupancyUnknownError
	return errors.As(err, &occupied) || errors.As(err, &unknown)
}

// OccupiedError means the start needs an answer before it touches anything,
// and nothing was touched. Occupants are the target's own family's models it
// would displace while AllowReplace was false: an Exclusive server has
// exactly one occupant, a Pool can have several. Others are the other
// provider families' models loaded beside it, and OthersUnknown the families
// whose probe could not say what they hold, while Options.Others was not
// Decided. Either half may be empty.
type OccupiedError struct {
	Occupants     []localmodels.Entry
	Others        []Resident
	OthersUnknown []string
}

// IDs lists the occupants' model ids, in the order they would go.
func (e *OccupiedError) IDs() []string {
	ids := make([]string, len(e.Occupants))
	for i, o := range e.Occupants {
		ids[i] = o.ModelID
	}
	return ids
}

func (e *OccupiedError) Error() string {
	var parts []string
	if len(e.Occupants) > 0 {
		parts = append(parts, fmt.Sprintf("starting this model would stop %s — confirm to replace", strings.Join(e.IDs(), ", ")))
	}
	if len(e.Others) > 0 || len(e.OthersUnknown) > 0 {
		msg := "starting this model loads it beside models of other providers"
		if len(e.Others) > 0 {
			labels := make([]string, len(e.Others))
			for i, r := range e.Others {
				labels[i] = r.Label()
			}
			msg += " (" + strings.Join(labels, ", ") + ")"
		}
		if len(e.OthersUnknown) > 0 {
			msg += "; cannot tell whether " + strings.Join(e.OthersUnknown, " or ") + " is serving a model"
		}
		parts = append(parts, msg+" — confirm to keep or stop them")
	}
	return strings.Join(parts, "; ")
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

// PortlessAddressError means the registry's base_url for a process family
// (Provider, e.g. mtplx) gives a start no address: it names no port and is
// not a url wt puts its own on, or it does not parse. wt serves the family on
// 127.0.0.1 and gives a url with no port its port only where that server
// answers (config.Provider.Origin); a server spawned for any other url would
// not be the one at that address, so nothing is spawned. Origin is the
// address as wt reads it.
type PortlessAddressError struct{ Provider, Origin string }

func (e *PortlessAddressError) Error() string {
	return fmt.Sprintf("the registry's %s base_url (%s) names no port, and wt serves %s on 127.0.0.1 — name the port %s listens on in the base_url, or use http://127.0.0.1", e.Provider, e.Origin, e.Provider, e.Provider)
}

// OccupancyUnknownError means the provider's live state could not be
// determined: its server accepted a connection but did not give a usable
// answer, so starting could replace a model that is still running. Nothing was
// touched.
type OccupancyUnknownError struct{ ProviderID, Origin string }

func (e *OccupancyUnknownError) Error() string {
	return fmt.Sprintf("cannot tell whether %s at %s is already serving a model — confirm before replacing it", e.ProviderID, e.Origin)
}

// LoadingInstanceError means a ScopeMachine family's one instance (a process wt
// spawned, pid PID) is still loading, so a start cannot tell what it will
// serve and must not spawn a second one on the machine.
type LoadingInstanceError struct {
	ProviderID string
	PID        int
}

func (e *LoadingInstanceError) Error() string {
	return fmt.Sprintf("%s already has an instance (pid %d) still loading; wait for it to finish, or run `wt stop %s`", e.ProviderID, e.PID, e.ProviderID)
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
// confirmation. Every process family in the provider table
// (provider.ProcessFamilies) gets the one spec-driven processServer backend.
var backendsByFamily = buildBackends()

func buildBackends() map[string]backend {
	m := map[string]backend{
		"ollama": ollamaBackend{},
		"omlx":   omlxBackend{},
	}
	for _, family := range provider.ProcessFamilies() {
		m[family] = processServer{family}
	}
	return m
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

// Start starts t. It is a no-op when live Inventory shows t already running,
// or when a machine-scope family's readiness command finds its one instance
// already serving t, on whatever port (machineOccupant: the instance is
// adopted; one serving another model is the occupant).
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
// proxy is bounced. It returns once config.yaml is written, with the proxy
// restart still running and the model free to be stopped during it: a caller
// about to say the model is running, or to hand it to an agent, calls
// SettleStart after a nil return, which is that wait and the check after it.
func Start(ctx context.Context, cfg *config.Config, t Target, opts Options) error {
	return startWith(ctx, defaultEnv(), cfg, t, opts)
}

// startWith is Start on a given env. Everything the start prints goes through
// routePrintf with this context, the asynchronous restart included, so the
// one line below is what sends all of it to opts.Out.
func startWith(ctx context.Context, e *env, cfg *config.Config, t Target, opts Options) error {
	ctx = withRouteOutput(ctx, opts.Out)
	if err := start(ctx, e, cfg, t, opts); err != nil {
		if e.restartOwed || (opts.RestartOwed && !RestartStillOwed(err)) {
			// A stopped occupant's or an evicted model's route was already
			// dropped from config.yaml, or the caller wrote a route before
			// this start (Options.RestartOwed); the proxy still serves the
			// old list until it is bounced. A refusal that touched nothing
			// does not spend the caller's restart: the retry after a
			// confirmed replace would then restart a second time.
			bounceRoutes(ctx, cfg)
		}
		return err
	}
	if opts.Progress != nil {
		opts.Progress(StageRouting)
	}
	routeAfterStart(ctx, cfg, t, e.restartOwed || opts.RestartOwed)
	return nil
}

// familyOf is the family a start's provider id belongs to
// (localmodels.Family); a test swaps it so a synthetic family's provider id
// names that family.
var familyOf = localmodels.Family

func start(ctx context.Context, e *env, cfg *config.Config, t Target, opts Options) error {
	family := familyOf(t.ProviderID)
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
	// A ScopeMachine family (rec) runs one instance on the whole machine, on
	// whatever port it was started with, so the snapshot's probe of the
	// registry's port cannot see it: its readiness command answers instead.
	// One already serving t is adopted, the same no-op as a target the
	// snapshot shows running (the route hook still runs); one serving another
	// model is the occupant, asked about and stopped like any other.
	foreign, foreignFound, adopt, merr := e.machineOccupant(ctx, cfg, family, t)
	if merr != nil {
		return merr
	}
	if adopt {
		return nil
	}
	// Residents are read only when there is a question to ask or an approved
	// stop to make: a Decided answer with nothing to stop (every caller
	// today) would throw them away, and reading them can cost a pidfile read
	// and a process-table lookup.
	var others []Resident
	var othersUnknown []string
	if !opts.Others.Decided || len(opts.Others.Stop) > 0 {
		others, othersUnknown = e.residents(cfg, snap, family, t)
	}
	var victims []localmodels.Entry
	var unknown bool
	if foreignFound {
		victims = []localmodels.Entry{foreign}
	} else {
		victims, unknown = e.resolveEvictions(ctx, cfg, family, t, snap)
	}
	if unknown && !opts.AllowReplace {
		origin := localmodels.FamilyOrigin(cfg, family)
		return &OccupancyUnknownError{ProviderID: t.ProviderID, Origin: origin}
	}
	askReplace := len(victims) > 0 && !opts.AllowReplace
	askOthers := !opts.Others.Decided && (len(others) > 0 || len(othersUnknown) > 0)
	if askReplace || askOthers {
		occ := &OccupiedError{}
		if askReplace {
			occ.Occupants = victims
		}
		if askOthers {
			occ.Others, occ.OthersUnknown = others, othersUnknown
		}
		return occ
	}
	// Other providers' models go first, so their memory is free before
	// anything of the target's own family moves.
	if err := e.stopOthers(ctx, cfg, t, others, opts.Others.Stop, report); err != nil {
		return err
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

// stopOthers stops the residents of other provider families whose ids the
// user approved (OthersAnswer.Stop), in the snapshot's order, each through
// the stop the stop loop uses: its backend's stopModel, or for an mtplx still
// loading the verified pidfile stop. Each stop's route removal is written at
// once through onOccupantStopped and its restart deferred to Start's one
// bounce, as a replaced occupant's is. An approved id that is no longer
// loaded has nothing to stop; a resident wt cannot stop is never stopped, and
// one the user approved (stoppable when asked, an omlx model loading again
// by now) is kept with the kept-model line; a stoppable resident the user
// did not approve while approving others appeared after the question, and is
// kept with a line saying so. The first stop that fails ends the start: the
// target is not started.
func (e *env) stopOthers(ctx context.Context, cfg *config.Config, t Target, others []Resident, approved []string, report func(Stage)) error {
	if len(approved) == 0 {
		return nil
	}
	reported := false
	for _, r := range others {
		if !r.Stoppable {
			// An approved model that wt can no longer stop (an omlx model
			// loading again since the question) is kept and said so; one
			// nobody approved stays as silent as a keep.
			if slices.Contains(approved, r.Entry.ModelID) {
				routePrintf(ctx, "wt: %s (%s) is also loaded; not stopped\n", r.Label(), r.Figure())
			}
			continue
		}
		if !slices.Contains(approved, r.Entry.ModelID) {
			routePrintf(ctx, "wt: %s (%s) is also loaded; not stopped\n", r.Label(), r.Figure())
			continue
		}
		if !reported {
			report(StageStoppingOthers)
			reported = true
		}
		var err error
		if r.PID > 0 {
			err = e.stopLoadingServer(ctx, cfg, localmodels.Family(r.Entry.ProviderID), r.PID)
		} else if b := e.backends[localmodels.Family(r.Entry.ProviderID)]; b == nil {
			err = &UnsupportedError{ProviderID: r.Entry.ProviderID}
		} else {
			err = b.stopModel(ctx, e, cfg, r.Entry.ModelName)
		}
		if err != nil {
			return fmt.Errorf("stopping %s before starting %s: %w", r.Entry.ModelID, t.ModelName, err)
		}
		if e.onOccupantStopped != nil && e.onOccupantStopped(ctx, cfg, r.Entry) {
			e.restartOwed = true
		}
	}
	return nil
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

// SettleRoutes restarts the LiteLLM proxy (and waits for it, asynchronously —
// see WaitPendingRoutes) to apply the route removals a StopBatch wrote. It
// rewrites nothing: the removals are already in config.yaml, and every caller
// reaches it because some StopBatch.Stop reported restartOwed.
// It runs detached from ctx, so a batch cut short by Ctrl+C still settles what
// it wrote.
//
// What the restart prints goes to stderr (SettleRoutesTo names another writer).
func SettleRoutes(ctx context.Context, cfg *config.Config) { SettleRoutesTo(ctx, nil, cfg) }

// SettleRoutesTo is SettleRoutes printing to out (nil: stderr), for a caller
// whose stderr is not visible while the restart runs: the picker, back on its
// alt screen. The restart is asynchronous and can print minutes later (the
// notice that the proxy is finishing a request in flight, a failed restart's
// warning), from its own goroutine: out must be safe to write while the
// caller reads it, and is complete only once WaitPendingRoutes has returned.
func SettleRoutesTo(ctx context.Context, out io.Writer, cfg *config.Config) {
	bounceRoutes(withRouteOutput(ctx, out), cfg)
}

// stopModel stops one model outside a batch, the same shape as stop/start:
// each backend's own stop, an ollama one waiting under a bound of its own.
// StopBatch.Stop is the production path; this is the per-backend core tests
// drive directly.
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
// StopBatch.Stop would refuse with *UnsupportedError. Every backend
// implements both start and stop, so this is Startable under its stop-side
// name.
func CanStop(providerID string) bool { return Startable(providerID) }
