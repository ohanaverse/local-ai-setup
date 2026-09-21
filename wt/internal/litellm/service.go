package litellm

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"gopkg.in/yaml.v3"
)

// Options tunes Apply/Sync.
//   - Path          — config.yaml; "" means DefaultPath().
//   - SkipReadyGate — skip the "model must be ready" check (the lifecycle
//     hook passes it: the model is verifiably running; modelman passes it
//     because it applied the gate against its own in-memory state).
//   - Restart       — proxy restart hook; nil means Restart. Tests inject.
type Options struct {
	Path          string
	SkipReadyGate bool
	Restart       func() []string
	// Untouched lists model ids Sync must neither add nor remove (their
	// running state is unknown, e.g. the provider probe failed).
	Untouched []string
	// Recheck, when set, is called by Sync under the config.yaml lock just
	// before it removes routes, and returns the ids running NOW. Ids that
	// turn out to be running are kept: a start finishing between the caller's
	// probe and the lock would otherwise have its fresh route removed.
	Recheck func() []string
	// NoRestart writes config.yaml but leaves the proxy alone: the caller owes
	// (and performs) the restart itself, so several route changes in one
	// operation cost one bounce.
	NoRestart bool
	// ForceRestart restarts the proxy even when this call changed nothing —
	// the settling restart for a run of earlier NoRestart writes.
	ForceRestart bool
}

// Outcome is one requested id's result. Action is "exposed" or "unexposed"
// (what was asked for and applied); Err is set when the id was rejected.
type Outcome struct {
	ID     string
	Action string
	Err    error
}

// Result reports a batch: per-id outcomes, whether config.yaml was written,
// and non-fatal warnings (restart failures).
type Result struct {
	Outcomes []Outcome
	Changed  bool
	Warnings []string
}

func (o Options) path() string {
	if o.Path != "" {
		return o.Path
	}
	return DefaultPath()
}

// isCloud reports whether a model is exempt from the ready gate: its provider
// policy says cloud, or the model or its provider is located in the cloud.
func isCloud(m config.Model, p config.Provider, pol Policy) bool {
	return pol.Cloud || m.Location == config.LocationCloud || p.Location == config.LocationCloud
}

// prepare validates id against the registry and builds its row.
func prepare(cfg *config.Config, id string, skipReady bool) (*yaml.Node, error) {
	i := config.IndexModelByID(cfg.Models, id)
	if i < 0 {
		return nil, fmt.Errorf("model %q not found in registry", id)
	}
	m := cfg.Models[i]
	p := cfg.ProviderByID(m.ProviderID)
	if p == nil {
		return nil, fmt.Errorf("model %q references unknown provider %q", id, m.ProviderID)
	}
	if m.Native || p.Auth.Type == "native" {
		return nil, fmt.Errorf("provider %q is native and cannot be exposed through LiteLLM", m.ProviderID)
	}
	pol, ok := PolicyFor(p.ID)
	if !ok {
		return nil, fmt.Errorf("provider %q has no LiteLLM mapping", p.ID)
	}
	// Config.validate's per-model data rule; wt commands no longer refuse on
	// validation errors, so enforce it here. FixedModel providers (llamacpp)
	// use a fixed LiteLLM model string and ignore model_name.
	if strings.TrimSpace(m.ModelName) == "" && !pol.FixedModel {
		return nil, fmt.Errorf("model %q: empty model_name", id)
	}
	if !skipReady && !cfg.ReadyFlag(id) && !isCloud(m, *p, pol) {
		return nil, fmt.Errorf("model %q is not ready", id)
	}
	return BuildEntry(m, *p)
}

// Check validates ids without touching any file (`--dry-run`).
func Check(cfg *config.Config, ids []string, skipReady bool) []Outcome {
	out := make([]Outcome, 0, len(ids))
	for _, id := range ids {
		_, err := prepare(cfg, id, skipReady)
		out = append(out, Outcome{ID: id, Action: "exposed", Err: err})
	}
	return out
}

// Apply adds routes for `add` and removes routes for `remove` in one locked
// read-modify-write and one restart. Per-id validation failures are reported
// in Outcomes and do not block the rest. A missing/invalid config.yaml
// returns (Result{}, err) with nothing changed. The file is written, and the
// proxy restarted, only when the document actually changed.
func Apply(cfg *config.Config, add, remove []string, o Options) (Result, error) {
	return applyPlanned(cfg, func(*File) ([]string, []string) { return add, remove }, o)
}

// applyPlanned is Apply with the add/remove decision made by plan against the
// document as read under the lock, so callers that derive the change from the
// current routes (Sync) never act on a snapshot another process has since
// changed.
func applyPlanned(cfg *config.Config, plan func(*File) (add, remove []string), o Options) (Result, error) {
	path := o.path()
	var res Result
	err := WithLock(path, func() error {
		f, err := Open(path)
		if err != nil {
			return err
		}
		add, remove := plan(f)
		for _, id := range remove {
			f.RemoveRow(id)
			res.Outcomes = append(res.Outcomes, Outcome{ID: id, Action: "unexposed"})
		}
		for _, id := range add {
			row, perr := prepare(cfg, id, o.SkipReadyGate)
			if perr != nil {
				res.Outcomes = append(res.Outcomes, Outcome{ID: id, Err: perr})
				continue
			}
			if err := f.SetRow(id, row); err != nil {
				return err
			}
			res.Outcomes = append(res.Outcomes, Outcome{ID: id, Action: "exposed"})
		}
		f.EnsureSettings()
		if !f.Changed() {
			return nil
		}
		if err := f.Save(); err != nil {
			return err
		}
		res.Changed = true
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if (res.Changed && !o.NoRestart) || o.ForceRestart {
		restart := o.Restart
		if restart == nil {
			restart = Restart
		}
		res.Warnings = restart()
	}
	return res, nil
}

// LocalModels lists the registry's non-native local models that have a
// LiteLLM mapping — the set Sync manages. Cloud and native models are never
// touched by Sync.
func LocalModels(cfg *config.Config) []config.Model {
	var out []config.Model
	for _, m := range cfg.Models {
		if m.Native {
			continue
		}
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		if _, ok := PolicyFor(m.ProviderID); !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

// ModelFor finds the registry model for a provider and provider-side name.
// An exact match wins. Otherwise it falls back to the same per-family name
// matching the live inventory uses (ollama's implicit ":latest" tag, a path-
// or org-prefixed omlx/mtplx spelling), in either direction, so a target
// spelled slightly differently from the registry still gets its route. The
// fallback applies only when exactly one model matches: an ambiguous name
// routes nothing rather than the wrong model.
func ModelFor(cfg *config.Config, providerID, modelName string) (config.Model, bool) {
	for _, m := range cfg.Models {
		if m.ProviderID == providerID && m.ModelName == modelName {
			return m, true
		}
	}
	match := localmodels.NameMatches
	if providerID == "ollama" {
		match = localmodels.OllamaNameMatches
	}
	var found config.Model
	n := 0
	for _, m := range cfg.Models {
		if m.ProviderID == providerID && (match(modelName, m.ModelName) || match(m.ModelName, modelName)) {
			found = m
			n++
		}
	}
	if n != 1 {
		return config.Model{}, false
	}
	return found, true
}

// Sync makes the local-model routes match reality: every id in `running`
// (that is a registry local model) gets a route; every other local model that
// currently has a route loses it. Cloud and unrelated rows are untouched.
func Sync(cfg *config.Config, running []string, o Options) (Result, error) {
	o.SkipReadyGate = true
	return applyPlanned(cfg, func(f *File) (add, remove []string) {
		routed := f.RoutedIDs()
		for _, m := range LocalModels(cfg) {
			switch {
			case slices.Contains(o.Untouched, m.ID):
			case slices.Contains(running, m.ID):
				add = append(add, m.ID)
			case slices.Contains(routed, m.ID):
				remove = append(remove, m.ID)
			}
		}
		if len(remove) > 0 && o.Recheck != nil {
			fresh := o.Recheck()
			remove = slices.DeleteFunc(remove, func(id string) bool { return slices.Contains(fresh, id) })
		}
		return add, remove
	}, o)
}

// Providers maps each LiteLLM-mapped provider id to whether it is a cloud
// provider. modelman reads this through `wt litellm providers` instead of
// keeping its own copy of the policy table.
func Providers() map[string]bool {
	out := make(map[string]bool, len(policies))
	for id, p := range policies {
		out[id] = p.Cloud
	}
	return out
}
