package litellm

import (
	"fmt"
	"slices"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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
	path := o.path()
	var res Result
	err := WithLock(path, func() error {
		f, err := Open(path)
		if err != nil {
			return err
		}
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
	if res.Changed {
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
func ModelFor(cfg *config.Config, providerID, modelName string) (config.Model, bool) {
	for _, m := range cfg.Models {
		if m.ProviderID == providerID && m.ModelName == modelName {
			return m, true
		}
	}
	return config.Model{}, false
}

// Sync makes the local-model routes match reality: every id in `running`
// (that is a registry local model) gets a route; every other local model that
// currently has a route loses it. Cloud and unrelated rows are untouched.
func Sync(cfg *config.Config, running []string, o Options) (Result, error) {
	f, err := Open(o.path())
	if err != nil {
		return Result{}, err
	}
	routed := f.RoutedIDs()
	var add, remove []string
	for _, m := range LocalModels(cfg) {
		switch {
		case slices.Contains(o.Untouched, m.ID):
		case slices.Contains(running, m.ID):
			add = append(add, m.ID)
		case slices.Contains(routed, m.ID):
			remove = append(remove, m.ID)
		}
	}
	o.SkipReadyGate = true
	return Apply(cfg, add, remove, o)
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
