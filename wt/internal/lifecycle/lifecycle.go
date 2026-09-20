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
	// start runs everything from spawn through warmup for t, reporting stages.
	start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error
}

// sameModel reports whether two provider-side names denote the same model under
// the family's matching rule.
func sameModel(family, a, b string) bool {
	if family == "ollama" {
		return localmodels.OllamaNameMatches(a, b) || localmodels.OllamaNameMatches(b, a)
	}
	return localmodels.NameMatches(a, b)
}

// isRunning reports whether live Inventory already shows the target serving.
func isRunning(snap localmodels.Snapshot, family string, t Target) bool {
	for _, en := range snap.Entries {
		if en.Running && localmodels.Family(en.ProviderID) == family && sameModel(family, en.ModelName, t.ModelName) {
			return true
		}
	}
	return false
}

// Occupant reports the running model that starting t would replace: none for
// ollama (multi-tenant) or unsupported providers; on omlx/omlx-6bit (one
// domain) and mtplx, any running model of the family other than t itself.
func Occupant(t Target, snap localmodels.Snapshot) (localmodels.Entry, bool) {
	family := localmodels.Family(t.ProviderID)
	if family != "omlx" && family != "mtplx" {
		return localmodels.Entry{}, false
	}
	for _, en := range snap.Entries {
		if !en.Running || localmodels.Family(en.ProviderID) != family {
			continue
		}
		if sameModel(family, en.ModelName, t.ModelName) {
			continue
		}
		return en, true
	}
	return localmodels.Entry{}, false
}

// Start starts t. It is a no-op when live Inventory shows t already running,
// returns *OccupiedError (touching nothing) when it would replace a running
// model and opts.AllowReplace is false, and otherwise stops the occupant (if
// any) and runs the provider's start sequence.
func Start(ctx context.Context, cfg *config.Config, t Target, opts Options) error {
	return start(ctx, defaultEnv(), cfg, t, opts)
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
	if b.singleModel() {
		if occ, ok := Occupant(t, snap); ok {
			if !opts.AllowReplace {
				return &OccupiedError{Occupant: occ}
			}
			report(StageStoppingOccupant)
			if err := b.stop(ctx, e, cfg); err != nil {
				return fmt.Errorf("stopping %s before starting %s: %w", occ.ModelID, t.ModelName, err)
			}
		}
	}
	return b.start(ctx, e, cfg, t, report)
}

// Stop stops the provider's running model (used for replacement; wt has no
// user-facing stop command). A no-op for multi-tenant ollama.
func Stop(ctx context.Context, cfg *config.Config, providerID string) error {
	e := defaultEnv()
	b := e.backends[localmodels.Family(providerID)]
	if b == nil {
		return &UnsupportedError{ProviderID: providerID}
	}
	return b.stop(ctx, e, cfg)
}
