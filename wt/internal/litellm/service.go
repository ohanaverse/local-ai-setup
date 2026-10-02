package litellm

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"gopkg.in/yaml.v3"
)

// recheckTimeout bounds Sync's live Recheck probe while the config.yaml
// lock is held: a slow/unresponsive provider must not hold the lock
// indefinitely and starve a concurrent bounded-context caller (e.g. the
// lifecycle route hook's settling bounce). A timeout is treated the same
// as "no Recheck": the pre-recheck plan proceeds unchanged. A var, not a
// const, so tests can shrink it.
var recheckTimeout = 5 * time.Second

// runRecheck calls o.Recheck with recheckTimeout, reporting ok=false (fresh
// left nil) when it does not return in time.
func runRecheck(o Options) (fresh []string, ok bool) {
	done := make(chan []string, 1)
	go func() { done <- o.Recheck() }()
	select {
	case fresh = <-done:
		return fresh, true
	case <-time.After(recheckTimeout):
		return nil, false
	}
}

// Options tunes Apply/Sync.
//   - Path          — config.yaml; "" means DefaultPath().
//   - SkipReadyGate — skip the "model must be ready" check (the lifecycle
//     hook passes it: the model is verifiably running; modelman passes it
//     because it applied the gate against its own in-memory state).
//   - Restart       — proxy restart hook; nil means Restart. Tests inject.
//   - Ctx           — bounds the config.yaml lock wait; nil means no bound.
type Options struct {
	Path          string
	SkipReadyGate bool
	Restart       func() []string
	// Ctx bounds the wait for the config.yaml lock (nil = unbounded). The
	// lifecycle route hook passes its caller's context, so a contended lock
	// cannot outlive a caller that set a deadline; the CLI leaves it nil, since
	// an interactive command should wait rather than fail.
	Ctx context.Context
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
// (expose/unexpose) — or, from Sync, "routed", "adopted" or "unrouted"; Err is
// set when the id was rejected.
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
	err := WithLock(o.Ctx, path, func() error {
		f, err := Open(path)
		if err != nil {
			return err
		}
		// Refuse a config we cannot understand before planning or mutating
		// anything. EnsureSettings would otherwise rewrite such a file whenever
		// the plan happened to be empty, and a caller must not edit a file it
		// cannot parse. PlanSync checks this too, so a dry run and a real sync
		// can never disagree about whether a malformed file is workable.
		if err := f.checkModelList(); err != nil {
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
// LiteLLM mapping — the local half of the set Sync manages (CloudModels is
// the other).
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

// CloudModels lists the registry cloud models sync routes (#179: configured
// means exposed): non-native, with a provider entry LiteLLM can map, whose
// location resolves to cloud. Pinned by docs/contracts/catalog-predicates.
func CloudModels(cfg *config.Config) []config.Model {
	var out []config.Model
	for _, m := range cfg.Models {
		if m.Native {
			continue
		}
		p := cfg.ProviderByID(m.ProviderID)
		if p == nil || p.Auth.Type == "native" {
			continue
		}
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationCloud {
			continue
		}
		if _, ok := PolicyFor(m.ProviderID); !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

// SyncPlan is what one sync changes. Add holds desired ids whose row is
// missing or differs (each written with the marker); Adopt is the subset of
// Add that replaces an unmarked row; Remove holds owned rows no longer
// desired; Errors holds desired ids whose row could not be built.
type SyncPlan struct {
	Add    []string
	Adopt  []string
	Remove []string
	Errors []Outcome
}

// planSync decides a sync against f. Desired = every CloudModels id plus the
// running registry local models. A row is wt's to remove when it carries the
// marker or is named like a managed registry id (cloud or local) — the
// latter covers rows written before the marker existed. Unmarked rows with
// any other name are hand-written and never touched. Untouched ids are
// neither added nor removed; Recheck (see Options) re-verifies local ids.
func planSync(cfg *config.Config, f *File, running []string, o Options) SyncPlan {
	local := map[string]bool{}
	for _, m := range LocalModels(cfg) {
		local[m.ID] = true
	}
	managed := map[string]bool{}
	var desired []string
	for _, m := range CloudModels(cfg) {
		managed[m.ID] = true
		desired = append(desired, m.ID)
	}
	for id := range local {
		managed[id] = true
	}
	for _, m := range LocalModels(cfg) {
		if slices.Contains(running, m.ID) && !slices.Contains(o.Untouched, m.ID) {
			desired = append(desired, m.ID)
		}
	}
	var plan SyncPlan
	want := map[string]bool{}
	// rowsPerID counts the mapping rows sharing each model_name: LiteLLM
	// load-balances across duplicates, so a value-equal FIRST row still needs
	// SetRow when a second row shares the id (SetRow replaces the first row
	// and drops the rest). Only a single value-equal row is a clean no-op.
	rowsPerID := map[string]int{}
	for _, r := range f.Rows() {
		rowsPerID[r.ID]++
	}
	for _, id := range desired {
		want[id] = true
		row, err := prepare(cfg, id, true)
		if err != nil {
			plan.Errors = append(plan.Errors, Outcome{ID: id, Err: err})
			continue
		}
		old := f.row(id)
		if old != nil {
			carryUserParams(old, row)
			// Compare by value, not bytes: a row read back from disk keeps
			// quoting styles ('ollama/gemma:9b') a freshly built one lacks,
			// so byte equality would report every unchanged row as changed.
			var oldv, newv any
			if old.Decode(&oldv) == nil && row.Decode(&newv) == nil && reflect.DeepEqual(oldv, newv) && rowsPerID[id] == 1 {
				continue
			}
		}
		plan.Add = append(plan.Add, id)
		if old != nil && !IsManaged(old) {
			plan.Adopt = append(plan.Adopt, id)
		}
	}
	// Iterate rows, not models: a row outlives a registry entry that was deleted
	// or renamed, so a loop over the model lists would never visit it and would
	// strand the stale route (TestSyncRemovesMarkedRowOfDeletedModel). Ownership
	// is read per row — its marker, or `managed` for the unmarked pre-#179 rows
	// TestSyncRemovesLegacyUnmarkedLocalRoute pins — so a model-shaped rewrite of
	// this loop loses both clauses.
	//
	// seen deduplicates: two rows can share one id and one RemoveRow drops both,
	// so the id belongs in the plan once. Without it the same removal was
	// announced twice in the report and handed twice to callers of PlanSync.
	seen := map[string]bool{}
	for _, r := range f.Rows() {
		if want[r.ID] || slices.Contains(o.Untouched, r.ID) || seen[r.ID] {
			continue
		}
		if r.Managed || managed[r.ID] {
			seen[r.ID] = true
			plan.Remove = append(plan.Remove, r.ID)
		}
	}
	if o.Recheck != nil {
		touchesLocal := slices.ContainsFunc(plan.Add, func(id string) bool { return local[id] }) ||
			slices.ContainsFunc(plan.Remove, func(id string) bool { return local[id] })
		if touchesLocal {
			if fresh, ok := runRecheck(o); ok {
				plan.Remove = slices.DeleteFunc(plan.Remove, func(id string) bool { return local[id] && slices.Contains(fresh, id) })
				// The same predicate the Add side gets, so Adopt stays the subset
				// of Add that SyncPlan documents: an id pruned from Add must not
				// stay in Adopt, or the plan describes an adoption it will not
				// perform.
				stoppedAgain := func(id string) bool { return local[id] && !slices.Contains(fresh, id) }
				plan.Add = slices.DeleteFunc(plan.Add, stoppedAgain)
				plan.Adopt = slices.DeleteFunc(plan.Adopt, stoppedAgain)
			}
		}
	}
	return plan
}

// PlanSync reports what Sync would change without writing (`sync --dry-run`).
// Like Sync it refuses a config.yaml whose model_list is not a list, so the two
// can never disagree: a dry run neither promises a plan the real sync would not
// perform nor refuses a file the real sync would rewrite. The refusal lives in
// checkModelList, which only inspects an existing value — it must not create
// model_list, or a no-op sync would flip File.Changed and write the file and
// restart the proxy.
func PlanSync(cfg *config.Config, running []string, o Options) (SyncPlan, error) {
	f, err := Open(o.path())
	if err != nil {
		return SyncPlan{}, err
	}
	if err := f.checkModelList(); err != nil {
		return SyncPlan{}, err
	}
	return planSync(cfg, f, running, o), nil
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

// Sync reconciles config.yaml with the registry and the running local
// models (#179): every CloudModels id and every running registry local
// model gets a marked route; rows wt owns that are no longer desired are
// removed; hand-written rows are never touched (see planSync). The plan is
// made under the config.yaml lock. A config.yaml whose model_list is not a
// list is refused (ErrInvalid) before anything is planned or written, whatever
// the plan turns out to be — PlanSync checks the same shape, so a dry run and a
// real sync always agree. Outcome actions: "routed", "adopted", "unrouted";
// per-id build failures are reported with Err.
func Sync(cfg *config.Config, running []string, o Options) (Result, error) {
	o.SkipReadyGate = true
	var plan SyncPlan
	res, err := applyPlanned(cfg, func(f *File) (add, remove []string) {
		plan = planSync(cfg, f, running, o)
		return plan.Add, plan.Remove
	}, o)
	if err != nil {
		return res, err
	}
	for i := range res.Outcomes {
		switch res.Outcomes[i].Action {
		case "exposed":
			res.Outcomes[i].Action = "routed"
			if slices.Contains(plan.Adopt, res.Outcomes[i].ID) {
				res.Outcomes[i].Action = "adopted"
			}
		case "unexposed":
			res.Outcomes[i].Action = "unrouted"
		}
	}
	res.Outcomes = append(res.Outcomes, plan.Errors...)
	return res, nil
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
