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

// Options tunes Apply/ApplyChange/Sync.
//   - Path    — config.yaml; "" means DefaultPath().
//   - Restart — proxy restart hook; nil means Restart. Tests inject.
//   - Ctx     — bounds the config.yaml lock wait; nil means no bound.
//
// There is no ready gate (#179 Phase B): a local model is routed because the
// live inventory found it, never because modelman flagged it downloaded.
type Options struct {
	Path    string
	Restart func() []string
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
	// PlanSync (the dry run) forces it off — it holds no lock and must report
	// exactly the plan built from the original probe.
	Recheck func() []string
	// NoRestart writes config.yaml but leaves the proxy alone: the caller owes
	// (and performs) the restart itself, so several route changes in one
	// operation cost one bounce.
	NoRestart bool
	// ForceRestart restarts the proxy even when this call changed nothing —
	// the settling restart for a run of earlier NoRestart writes.
	ForceRestart bool
}

// Outcome is one requested id's result. Action is "routed" or "unrouted"
// (Apply) — Sync further splits "routed" into "adopted" and "rewritten" where
// its plan says so; Err is set when the id was rejected.
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

// lookup finds id's registry model.
func lookup(cfg *config.Config, id string) (config.Model, error) {
	i := config.IndexModelByID(cfg.Models, id)
	if i < 0 {
		return config.Model{}, fmt.Errorf("model %q not found in registry", id)
	}
	return cfg.Models[i], nil
}

// prepareModel validates m and builds its row. m is a registry model or a
// DiscoveredModel; either way it needs a routable id, and its provider must be
// in the registry (the row dials that provider's base_url) and have a LiteLLM
// mapping.
func prepareModel(cfg *config.Config, m config.Model) (*yaml.Node, error) {
	// The id is the row's model_name and the only handle a later stop, family
	// clear or sync has on it. DiscoveredModel yields "/<artifact>" for a
	// provider with a LiteLLM policy but no probe family (retired llamacpp):
	// RowFamily could never classify that row, so it is refused, not written.
	if m.ID == "" || strings.HasPrefix(m.ID, "/") {
		return nil, fmt.Errorf("model %q (provider %q): no routable id — the provider has no local family", m.ID, m.ProviderID)
	}
	p := cfg.ProviderByID(m.ProviderID)
	if p == nil {
		return nil, fmt.Errorf("model %q references unknown provider %q", m.ID, m.ProviderID)
	}
	if m.Native || p.Auth.Type == "native" {
		return nil, fmt.Errorf("provider %q is native and is not routed through LiteLLM", m.ProviderID)
	}
	pol, ok := PolicyFor(p.ID)
	if !ok {
		return nil, fmt.Errorf("provider %q has no LiteLLM mapping", p.ID)
	}
	// Config.validate's per-model data rule; wt commands no longer refuse on
	// validation errors, so enforce it here. FixedModel providers (llamacpp)
	// use a fixed LiteLLM model string and ignore model_name.
	if strings.TrimSpace(m.ModelName) == "" && !pol.FixedModel {
		return nil, fmt.Errorf("model %q: empty model_name", m.ID)
	}
	return BuildEntry(m, *p)
}

// DiscoveredModel is the minimal model wt routes for a discovered local
// artifact that no registry overlay matches (#179 Phase B): its id is the
// catalog's config.DiscoveredModelID — the same id -M, usage and the picker
// use — and PolicyFor supplies api_base, the model prefix and the key. It has
// no cost, so BuildEntry writes the explicit $0 pricing every local row gets.
func DiscoveredModel(providerID, artifact string) config.Model {
	return config.Model{
		ID:         config.DiscoveredModelID(localmodels.Family(providerID), artifact),
		ProviderID: providerID,
		ModelName:  artifact,
		Location:   config.LocationLocal,
		Source:     config.SourceDiscovered,
	}
}

// RowFamily is the local provider family a config.yaml row belongs to, or ""
// for a cloud or unrecognised row. A registry model answers by its provider
// when its location resolves local (a registry cloud model — even one on the
// local ollama provider — is never a local family's row). Any other id is
// classified by its prefix up to the first "/" when that prefix is a local
// provider: discovered ids are exactly "<family>/<artifact>", and a row of a
// deleted local overlay keeps its provider prefix.
func RowFamily(cfg *config.Config, id string) string {
	if i := config.IndexModelByID(cfg.Models, id); i >= 0 {
		m := cfg.Models[i]
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			return ""
		}
		return localmodels.Family(m.ProviderID)
	}
	prefix, _, ok := strings.Cut(id, "/")
	if !ok {
		return ""
	}
	return localmodels.Family(prefix)
}

// isRegistryID reports whether id names a registry model. A discovered route
// (no registry entry) never replaces a hand-written row of the same name;
// registry ids keep Phase A adoption.
func isRegistryID(cfg *config.Config, id string) bool {
	return config.IndexModelByID(cfg.Models, id) >= 0
}

// plannedAdd is one add from a plan: the id and either its built row or the
// reason it could not be built. Every plan builds its rows while planning, so
// the write path never prepares a row a second time.
type plannedAdd struct {
	id  string
	row *yaml.Node
	err error
}

// plannedRemove is one removal from a plan. markedOnly limits it to rows that
// carry the marker: a row removed for its marker alone may share its name
// with a hand-written row, and that one is not wt's to delete.
type plannedRemove struct {
	id         string
	markedOnly bool
}

// Apply adds routes for `add` and removes routes for `remove` in one locked
// read-modify-write and one restart. Per-id validation failures are reported
// in Outcomes and do not block the rest. A missing/invalid config.yaml
// returns (Result{}, err) with nothing changed. The file is written, and the
// proxy restarted, only when the document actually changed.
func Apply(cfg *config.Config, add, remove []string, o Options) (Result, error) {
	return applyPlanned(cfg, func(*File) ([]plannedAdd, []plannedRemove) {
		out := make([]plannedAdd, len(add))
		for i, id := range add {
			out[i] = plannedAdd{id: id}
			m, err := lookup(cfg, id)
			if err == nil {
				out[i].row, err = prepareModel(cfg, m)
			}
			out[i].err = err
		}
		rm := make([]plannedRemove, len(remove))
		for i, id := range remove {
			rm[i] = plannedRemove{id: id}
		}
		return out, rm
	}, o)
}

// Change is one targeted route write — the lifecycle hooks' unit (#179
// Phase B).
//   - Add            — models to route: registry overlays or DiscoveredModels.
//     A DISCOVERED id that already has a hand-written (unmarked) row is
//     skipped: the user's row serves that name and is never replaced.
//   - Remove         — ids whose rows go: a registry id's rows whether marked
//     or not (Phase A ownership), any other id's marked rows only.
//   - RemoveFamilies — local families (localmodels.Family values) whose
//     routes all go: every marked row whose RowFamily is the family —
//     discovered siblings included — plus the family's registry local ids,
//     marked or legacy-unmarked. Never an unmarked row that is not a registry
//     id, and never an id in Add.
type Change struct {
	Add            []config.Model
	Remove         []string
	RemoveFamilies []string
}

// ApplyChange writes ch in one locked read-modify-write and at most one
// restart, deciding the family removals against the document as read under
// the lock (so a route another process just wrote is seen). Per-model build
// failures are reported in Outcomes and do not block the rest; file-level
// failures are Apply's.
func ApplyChange(cfg *config.Config, ch Change, o Options) (Result, error) {
	return applyPlanned(cfg, func(f *File) ([]plannedAdd, []plannedRemove) {
		adding := map[string]bool{}
		var add []plannedAdd
		for _, m := range ch.Add {
			adding[m.ID] = true
			if !isRegistryID(cfg, m.ID) && f.hasUnmarkedRow(m.ID) {
				continue
			}
			row, err := prepareModel(cfg, m)
			add = append(add, plannedAdd{id: m.ID, row: row, err: err})
		}
		var rm []plannedRemove
		seen := map[string]bool{}
		drop := func(id string, markedOnly bool) {
			if adding[id] || seen[id] {
				return
			}
			seen[id] = true
			rm = append(rm, plannedRemove{id: id, markedOnly: markedOnly})
		}
		for _, id := range ch.Remove {
			drop(id, !isRegistryID(cfg, id))
		}
		for _, fam := range ch.RemoveFamilies {
			for _, m := range LocalModels(cfg) {
				if localmodels.Family(m.ProviderID) == fam {
					drop(m.ID, false)
				}
			}
			for _, r := range f.Rows() {
				if r.Managed && RowFamily(cfg, r.ID) == fam {
					drop(r.ID, true)
				}
			}
		}
		return add, rm
	}, o)
}

// applyPlanned is Apply with the add/remove decision made by plan against the
// document as read under the lock, so callers that derive the change from the
// current routes (Sync) never act on a snapshot another process has since
// changed.
func applyPlanned(cfg *config.Config, plan func(*File) ([]plannedAdd, []plannedRemove), o Options) (Result, error) {
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
		for _, r := range remove {
			if r.markedOnly {
				f.RemoveMarkedRows(r.id)
			} else {
				f.RemoveRow(r.id)
			}
			res.Outcomes = append(res.Outcomes, Outcome{ID: r.id, Action: "unrouted"})
		}
		for _, a := range add {
			if a.err != nil {
				res.Outcomes = append(res.Outcomes, Outcome{ID: a.id, Err: a.err})
				continue
			}
			// A plan owes every add a row or an error. One with neither is a
			// planner bug; report it against its id rather than hand SetRow a
			// nil node, which would break the whole document.
			if a.row == nil {
				res.Outcomes = append(res.Outcomes, Outcome{ID: a.id, Err: fmt.Errorf("model %q: no row was built", a.id)})
				continue
			}
			if err := f.SetRow(a.id, a.row); err != nil {
				return err
			}
			res.Outcomes = append(res.Outcomes, Outcome{ID: a.id, Action: "routed"})
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

// routeableModels lists the registry models whose location resolves to loc —
// one filter for both halves of the set Sync manages. Skips native models and
// native providers (prepareModel rejects those the same way), fails closed on a
// dangling provider_id (ResolveLocation errors), and requires a LiteLLM
// mapping.
func routeableModels(cfg *config.Config, loc config.Location) []config.Model {
	var out []config.Model
	for _, m := range cfg.Models {
		if m.Native {
			continue
		}
		p := cfg.ProviderByID(m.ProviderID)
		if p == nil || p.Auth.Type == "native" {
			continue
		}
		if l, err := cfg.ResolveLocation(m); err != nil || l != loc {
			continue
		}
		if _, ok := PolicyFor(m.ProviderID); !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

// LocalModels lists the registry's non-native local models that have a
// LiteLLM mapping — the local half of the set Sync manages (CloudModels is
// the other).
func LocalModels(cfg *config.Config) []config.Model {
	return routeableModels(cfg, config.LocationLocal)
}

// CloudModels lists the registry cloud models sync routes (#179: configured
// means exposed) — the cloud half. Pinned by docs/contracts/catalog-predicates.
func CloudModels(cfg *config.Config) []config.Model {
	return routeableModels(cfg, config.LocationCloud)
}

// SyncPlan is what one sync changes. Add holds desired ids whose row is
// missing or differs (each written with the marker); Adopt is the subset of
// Add that replaces an unmarked row, Rewrite the subset that replaces a
// changed wt row (a price or credential change, not a new route); Remove holds owned rows no longer
// desired; Errors holds desired ids whose row could not be built.
type SyncPlan struct {
	Add     []string
	Adopt   []string
	Rewrite []string
	Remove  []string
	Errors  []Outcome
	// markedOnly holds the Remove ids owned by marker alone (not named like a
	// managed registry id): Sync drops only their marked rows.
	markedOnly map[string]bool
}

// planSync decides a sync against f. Desired = every CloudModels id plus the
// running registry local models. A row is wt's to remove when it carries the
// marker or is named like a managed registry id (cloud or local) — the
// latter covers rows written before the marker existed. Unmarked rows with
// any other name are hand-written and never touched. Untouched ids are
// neither added nor removed; Recheck (see Options) re-verifies local ids.
// Built rows for plan.Add are returned alongside the plan: Sync's write path
// reuses them instead of preparing every changed row a second time.
func planSync(cfg *config.Config, f *File, running []string, o Options) (SyncPlan, map[string]*yaml.Node) {
	localModels := LocalModels(cfg)
	local := map[string]bool{}
	for _, m := range localModels {
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
	// gap holds registry models dropped from both lists by a data gap (a
	// provider_id naming no provider, or no location to resolve): their rows
	// stay until the registry is repaired. Removing them would delete every
	// route — and its hand-added params — the moment `providers = []` lands.
	gap := map[string]bool{}
	for _, m := range cfg.Models {
		if _, err := cfg.ResolveLocation(m); !m.Native && (cfg.ProviderByID(m.ProviderID) == nil || err != nil) {
			gap[m.ID] = true
		}
	}
	for _, m := range localModels {
		if slices.Contains(running, m.ID) && !slices.Contains(o.Untouched, m.ID) {
			desired = append(desired, m.ID)
		}
	}
	var plan SyncPlan
	built := map[string]*yaml.Node{}
	want := map[string]bool{}
	// rowsPerID counts the mapping rows sharing each model_name: LiteLLM
	// load-balances across duplicates, so a value-equal FIRST row still needs
	// SetRow when a second row shares the id (SetRow replaces the first row
	// and drops the rest). Only a single value-equal row is a clean no-op.
	rows := f.Rows()
	rowsPerID := map[string]int{}
	for _, r := range rows {
		rowsPerID[r.ID]++
	}
	// credErr resolves each SecretRef provider's credentials once per plan.
	// A failure is never cached by config.ResolveSecret and an exec: helper
	// may take its full timeout, so resolving per model multiplied one broken
	// helper by the provider's model count.
	credErr := map[string]error{}
	for _, id := range desired {
		want[id] = true
		if err := providerCredErr(cfg, id, credErr); err != nil {
			plan.Errors = append(plan.Errors, Outcome{ID: id, Err: fmt.Errorf("model %q: %w", id, err)})
			continue
		}
		m, err := lookup(cfg, id)
		var row *yaml.Node
		if err == nil {
			row, err = prepareModel(cfg, m)
		}
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
		built[id] = row
		plan.Add = append(plan.Add, id)
		switch {
		case old == nil:
		case IsManaged(old):
			plan.Rewrite = append(plan.Rewrite, id)
		default:
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
	//
	// want is filled before prepareModel runs — deliberately, since `managed` holds
	// every cloud id: a desired cloud row whose build fails still short-circuits
	// this loop by name and keeps its route, where moving that assignment below
	// the error branch would let one transient build failure delete a live route.
	seen := map[string]bool{}
	for _, r := range rows {
		if want[r.ID] || gap[r.ID] || slices.Contains(o.Untouched, r.ID) || seen[r.ID] {
			continue
		}
		if r.Managed || managed[r.ID] {
			seen[r.ID] = true
			plan.Remove = append(plan.Remove, r.ID)
			if !managed[r.ID] {
				if plan.markedOnly == nil {
					plan.markedOnly = map[string]bool{}
				}
				plan.markedOnly[r.ID] = true
			}
		}
	}
	if o.Recheck != nil {
		touchesLocal := slices.ContainsFunc(plan.Add, func(id string) bool { return local[id] }) ||
			slices.ContainsFunc(plan.Remove, func(id string) bool { return local[id] })
		if touchesLocal {
			if fresh, ok := runRecheck(o); ok {
				plan.Remove = slices.DeleteFunc(plan.Remove, func(id string) bool { return local[id] && slices.Contains(fresh, id) })
				// The same predicate the Add side gets, so Adopt and Rewrite stay
				// the subsets of Add that SyncPlan documents: an id pruned from
				// Add must not stay in either, or the plan describes a change it
				// will not perform.
				stoppedAgain := func(id string) bool { return local[id] && !slices.Contains(fresh, id) }
				plan.Add = slices.DeleteFunc(plan.Add, stoppedAgain)
				plan.Adopt = slices.DeleteFunc(plan.Adopt, stoppedAgain)
				plan.Rewrite = slices.DeleteFunc(plan.Rewrite, stoppedAgain)
			}
		}
	}
	return plan, built
}

// providerCredErr is id's provider's credential error, resolved at most once
// per memo; nil for providers whose policy takes no secret_ref (prepare
// reports any other problem).
func providerCredErr(cfg *config.Config, id string, memo map[string]error) error {
	i := config.IndexModelByID(cfg.Models, id)
	if i < 0 {
		return nil
	}
	p := cfg.ProviderByID(cfg.Models[i].ProviderID)
	if p == nil {
		return nil
	}
	if pol, ok := PolicyFor(p.ID); !ok || !pol.SecretRef {
		return nil
	}
	err, seen := memo[p.ID]
	if !seen {
		_, err = providerAPIKey(*p)
		memo[p.ID] = err
	}
	return err
}

// PlanSync reports what Sync would change without writing (`sync --dry-run`).
// Like Sync it refuses a config.yaml whose model_list is not a list, so the two
// can never disagree: a dry run neither promises a plan the real sync would not
// perform nor refuses a file the real sync would rewrite. The refusal lives in
// checkModelList, which only inspects an existing value — it must not create
// model_list, or a no-op sync would flip File.Changed and write the file and
// restart the proxy.
func PlanSync(cfg *config.Config, running []string, o Options) (SyncPlan, error) {
	// PlanSync holds no config.yaml lock, and Recheck is defined as Sync's
	// under-the-lock re-verification; a dry run reports the plan built from
	// the caller's original probe, or it could not agree with the real sync.
	// Forced off so the exported dry run cannot run lock-less recheck
	// semantics however it is called.
	o.Recheck = nil
	f, err := Open(o.path())
	if err != nil {
		return SyncPlan{}, err
	}
	if err := f.checkModelList(); err != nil {
		return SyncPlan{}, err
	}
	plan, _ := planSync(cfg, f, running, o)
	return plan, nil
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
// real sync always agree. Outcome actions: "routed", "adopted", "rewritten",
// "unrouted"; per-id build failures are reported with Err.
func Sync(cfg *config.Config, running []string, o Options) (Result, error) {
	var plan SyncPlan
	res, err := applyPlanned(cfg, func(f *File) ([]plannedAdd, []plannedRemove) {
		p, built := planSync(cfg, f, running, o)
		plan = p
		// The plan already built every changed row while comparing it with the
		// row on disk; hand the rows through so the write path does not
		// prepare each one a second time.
		add := make([]plannedAdd, 0, len(plan.Add))
		for _, id := range plan.Add {
			add = append(add, plannedAdd{id: id, row: built[id]})
		}
		rm := make([]plannedRemove, 0, len(plan.Remove))
		for _, id := range plan.Remove {
			rm = append(rm, plannedRemove{id: id, markedOnly: plan.markedOnly[id]})
		}
		return add, rm
	}, o)
	if err != nil {
		return res, err
	}
	for i := range res.Outcomes {
		if res.Outcomes[i].Action != "routed" {
			continue
		}
		switch id := res.Outcomes[i].ID; {
		case slices.Contains(plan.Adopt, id):
			res.Outcomes[i].Action = "adopted"
		case slices.Contains(plan.Rewrite, id):
			res.Outcomes[i].Action = "rewritten"
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
