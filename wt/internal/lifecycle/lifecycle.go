package lifecycle

import (
	"context"
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Target names the model to start by its provider-side name — exactly
// config.Model.ModelName. For a discovered model that is the artifact name, so
// no registry entry is required.
type Target struct{ ProviderID, ModelName string }

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

// Options tunes Start. AllowReplace permits stopping a running occupant of a
// single-model provider; Progress (optional) receives stages in order.
type Options struct {
	AllowReplace bool
	Progress     func(Stage)
}

// OccupiedError means starting the target would replace a running model and
// AllowReplace was false. Nothing was touched.
type OccupiedError struct{ Occupant localmodels.Entry }

func (e *OccupiedError) Error() string {
	return fmt.Sprintf("starting this model would stop %s, which is running — confirm to replace it", e.Occupant.ModelID)
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

// UnsupportedError means wt has no lifecycle backend for the provider.
type UnsupportedError struct{ ProviderID string }

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("wt cannot start models for provider %q", e.ProviderID)
}

// backend is one provider family's lifecycle.
type backend interface {
	// singleModel reports whether the provider serves one model at a time
	// (starting another replaces it).
	singleModel() bool
	// stop stops the provider's current occupant and waits for it to release its port.
	stop(ctx context.Context, e *env, cfg *config.Config) error
	// stopModel stops the one named model (provider-side name). Multi-tenant
	// providers unload just that model; single-model providers stop their
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

// isRunning reports whether live Inventory already shows the target serving.
func isRunning(snap localmodels.Snapshot, family string, t Target) bool {
	if !ProbeTrusted(snap, family) {
		return false
	}
	for _, en := range snap.Entries {
		if en.Running && localmodels.Family(en.ProviderID) == family && SameModel(family, en.ModelName, t.ModelName) {
			return true
		}
	}
	return false
}

// backendsByFamily is the single source of truth for which providers wt can
// start and which of them serve one model at a time. defaultEnv copies it, and
// Occupant reads it (Occupant has no *env). Keeping these two in agreement used
// to be manual — Occupant held its own hardcoded family list — so a new
// single-model backend could be startable while Occupant still reported no
// occupant, replacing a running model without a confirmation.
var backendsByFamily = map[string]backend{
	"ollama": ollamaBackend{},
	"omlx":   omlxBackend{},
	"mtplx":  mtplxBackend{},
}

// Occupant reports the running model that starting t would replace: none for
// ollama (multi-tenant) or unsupported providers; on omlx/omlx-6bit (one
// domain) and mtplx, any running model of the family other than t itself. It
// also reports none when the snapshot's probe for the family failed, because a
// caller acting on that false would replace a model it never saw.
func Occupant(t Target, snap localmodels.Snapshot) (localmodels.Entry, bool) {
	family := localmodels.Family(t.ProviderID)
	if b := backendsByFamily[family]; b == nil || !b.singleModel() {
		return localmodels.Entry{}, false
	}
	if !ProbeTrusted(snap, family) {
		return localmodels.Entry{}, false
	}
	for _, en := range snap.Entries {
		if !en.Running || localmodels.Family(en.ProviderID) != family {
			continue
		}
		if SameModel(family, en.ModelName, t.ModelName) {
			continue
		}
		return en, true
	}
	return localmodels.Entry{}, false
}

// resolveOccupant decides whether starting t would replace a running model of a
// single-model family. It prefers the snapshot, which is free, and re-probes
// the server only when the snapshot's probe could not be trusted — the case
// that used to read as "no occupant" and let a start evict the running model.
// unknown reports that even the re-probe could not tell; the caller must then
// require AllowReplace.
func (e *env) resolveOccupant(ctx context.Context, cfg *config.Config, family string, t Target, snap localmodels.Snapshot) (occ localmodels.Entry, has, unknown bool) {
	if b := e.backends[family]; b == nil || !b.singleModel() {
		return localmodels.Entry{}, false, false
	}
	if occ, ok := Occupant(t, snap); ok {
		return occ, true, false
	}
	if ProbeTrusted(snap, family) {
		return localmodels.Entry{}, false, false
	}
	ids, known := e.liveServed(ctx, cfg, family)
	if !known {
		return localmodels.Entry{}, false, true
	}
	for _, id := range ids {
		if SameModel(family, id, t.ModelName) {
			continue // the target itself is already served: not an occupant
		}
		return localmodels.Entry{ProviderID: t.ProviderID, ModelID: id, ModelName: id, Running: true}, true, false
	}
	return localmodels.Entry{}, false, false
}

// Start starts t. It is a no-op when live Inventory shows t already running,
// returns *OccupiedError (touching nothing) when it would replace a running
// model and opts.AllowReplace is false, returns *OccupancyUnknownError
// (touching nothing) when its server accepted a connection but could not say
// what it is serving, and otherwise stops the occupant (if any) and runs the
// provider's start sequence. After a successful start the model's LiteLLM
// route is updated (routes.go), announced as StageRouting so callers stop
// rendering the engine's last stage while the proxy is bounced.
func Start(ctx context.Context, cfg *config.Config, t Target, opts Options) error {
	if err := start(ctx, defaultEnv(), cfg, t, opts); err != nil {
		return err
	}
	if opts.Progress != nil {
		opts.Progress(StageRouting)
	}
	routeAfterStart(ctx, cfg, t)
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
	if occ, has, unknown := e.resolveOccupant(ctx, cfg, family, t, snap); unknown {
		if !opts.AllowReplace {
			origin, _ := localmodels.FamilyOrigin(cfg, family)
			return &OccupancyUnknownError{ProviderID: t.ProviderID, Origin: origin}
		}
	} else if has {
		if !opts.AllowReplace {
			return &OccupiedError{Occupant: occ}
		}
		report(StageStoppingOccupant)
		if err := b.stop(ctx, e, cfg); err != nil {
			return fmt.Errorf("stopping %s before starting %s: %w", occ.ModelID, t.ModelName, err)
		}
		// The occupant is down now. Drop its route here rather than after the
		// start, because a start that fails from here on returns without any
		// route hook and would strand the dead occupant's model_list row.
		if e.onOccupantStopped != nil {
			e.onOccupantStopped(ctx, cfg, occ)
		}
	}
	return b.start(ctx, e, cfg, t, report)
}

// Stop stops the provider's running model. A no-op for multi-tenant ollama.
// After a successful stop the model's LiteLLM route is updated (routes.go).
func Stop(ctx context.Context, cfg *config.Config, providerID string) error {
	if err := stop(ctx, defaultEnv(), cfg, providerID); err != nil {
		return err
	}
	routeAfterStop(ctx, cfg, providerID, "")
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

// StopModel stops one running model (modelName is the provider-side name) for
// the post-exit stop picker. On ollama it unloads just that model; on
// single-model providers (omlx, mtplx) it stops the provider's sole occupant.
// The caller must only pass a model live Inventory reported running. After a
// successful stop the model's LiteLLM route is updated (routes.go).
func StopModel(ctx context.Context, cfg *config.Config, providerID, modelName string) error {
	if err := stopModel(ctx, defaultEnv(), cfg, providerID, modelName); err != nil {
		return err
	}
	routeAfterStop(ctx, cfg, providerID, modelName)
	return nil
}

// stopModel is StopModel's injectable core, the same shape as stop/start.
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
// picker never offers a model (e.g. one on mlx_lm_server) that StopModel would
// refuse with *UnsupportedError. Every backend implements both start and
// stop, so this is Startable under its stop-side name.
func CanStop(providerID string) bool { return Startable(providerID) }

// SingleModel reports whether providerID's family serves one model per
// process, so stopping any of its models stops the whole provider (and every
// sibling variant it lists as running).
func SingleModel(providerID string) bool {
	b := backendsByFamily[localmodels.Family(providerID)]
	return b != nil && b.singleModel()
}
