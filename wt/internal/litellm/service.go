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

// Options tunes ApplyChange/Sync.
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
	// UntouchedFamilies lists local provider families (localmodels.Family
	// values) whose state nothing can vouch for: the probe ran and was
	// neither OK nor refused, or it could not run because the registry
	// references the family unresolvably (a data gap). Sync neither adds,
	// removes nor rewrites a row whose RowFamily is one of them — discovered
	// routes included, which Untouched (registry ids only) cannot name. A
	// family never probed because the registry has no local provider row or
	// local model for it, and does not reference it unresolvably, does not
	// belong here: its leftover marked rows are stale and sync removes them.
	// An empty entry names no family and is ignored.
	UntouchedFamilies []string
	// Recheck, when set, is called by Sync under the config.yaml lock when
	// its plan touches a local route, and returns the local ids desired NOW —
	// running or pulled, registered or discovered. It prunes both sides of
	// the plan: a Remove id that is desired after all is kept (a start
	// finishing between the caller's probe and the lock would otherwise have
	// its fresh route removed), and an Add id — with its Adopt/Rewrite entry —
	// that is no longer desired is not written.
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
	// DeferMissingAdd asks the write not to build a missing AddMissingOnly
	// row, but to name it in Result.Missing instead. Building a cloud row
	// resolves the provider's secret_ref, and an exec: ref runs its helper
	// synchronously, bounded only by execSecretTimeout — a wait the TUI
	// picker's update goroutine cannot pay (it is also the goroutine that
	// repaints the screen and answers keys, #253). The caller owes a second
	// attempt through the same change where waiting is affordable, or the
	// route stays missing. A row that is already there is skipped before the
	// deferral (Change.AddMissingOnly) and is never reported as missing.
	DeferMissingAdd bool
	// reportOllamaServe asks the write to also collect the rows LiteLLM starts
	// its own `ollama serve` for (Result.ollamaServe; Sync publishes them in
	// Result.Warnings). Unexported, and set by Sync alone: the scan is per-write
	// work, and no other caller reports what it finds — a start, stop or launch
	// is not the place to hear about a row it never named.
	reportOllamaServe bool
}

// Outcome is one id's result. Action is "routed" or "unrouted" (ApplyChange)
// — Sync further splits "routed" into "adopted" and "rewritten" where its
// plan says so — or ActionAPIBaseSet for an ollama row whose empty api_base
// the write filled (any write: the row need not be one the change named); Err
// is set when the id was rejected. A removal that removed no row has no
// outcome.
//
// ID is the row's model_name; a repaired row that has none is named by
// rowLabel instead, never by "". Written is set on a "routed" (or "adopted",
// "rewritten") outcome whose row was added or differs from the one read:
// every row a change sets is reported as routed, identical or not, and
// Result.Changed is about the whole file, so Written is the only thing that
// says the id's own route changed (#206).
type Outcome struct {
	ID      string
	Action  string
	Err     error
	Written bool
}

// ActionAPIBaseSet is the Outcome action for a row File.EnsureOllamaAPIBase
// repaired (#202). It is part of `wt litellm sync --json`'s contract.
const ActionAPIBaseSet = "api_base set"

// Result reports a batch: per-id outcomes, whether config.yaml was written,
// and non-fatal warnings (restart failures; from Sync, also the rows LiteLLM
// starts its own `ollama serve` for).
type Result struct {
	Outcomes []Outcome
	Changed  bool
	Warnings []string
	// Missing names the AddMissingOnly adds the write did not build because
	// Options.DeferMissingAdd asked it not to. Empty for every other caller:
	// a write without that option builds the row, or reports why it could not
	// in Outcomes.
	Missing []string
	// ollamaServe is File.ollamaServeWarnings for the document as written, and
	// is filled only when Options.reportOllamaServe is set. Only Sync sets it,
	// and only Sync publishes it, in Warnings: a start, stop or launch is not
	// the place to hear about a row it never named.
	ollamaServe []string
}

func (o Options) path() string {
	if o.Path != "" {
		return o.Path
	}
	return DefaultPath()
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

// OllamaAPIBase is the address wt gives an ollama row: the registry ollama
// provider's base_url, the same value BuildEntry writes for the rows wt builds
// itself. When the registry has no ollama provider, or it names no address,
// that is config.OllamaBaseURL — the address wt itself dials for ollama then
// (localmodels.FamilyOrigin) — and never "": a row left without an api_base
// is what makes LiteLLM start a second `ollama serve` (#202, #206).
func OllamaAPIBase(cfg *config.Config) string {
	if p := cfg.ProviderByID("ollama"); p != nil && p.Auth.BaseURL != "" {
		return p.Auth.BaseURL
	}
	return config.OllamaBaseURL
}

// RegistryGap reports whether registry model m is dropped from every list
// sync manages by a data gap rather than by choice: a non-native model whose
// provider_id names no provider, or whose location cannot be resolved — none
// set, or a value that is neither "local" nor "cloud" (a typo such as
// "Local"). config.ResolveLocation is the one judge of that (#200). Sync keeps
// such a model's row until the registry is repaired (planSync), and the sync
// command keeps the model's whole family frozen on the same predicate.
func RegistryGap(cfg *config.Config, m config.Model) bool {
	if m.Native {
		return false
	}
	_, err := cfg.ResolveLocation(m)
	return cfg.ProviderByID(m.ProviderID) == nil || err != nil
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
//   - AddMissingOnly — Add writes a model only when config.yaml has no row of
//     that name at all; a row that is there, marked or not, is left exactly as
//     it is and its model is not even built. For a repair that must not stand
//     in for sync: building a cloud row resolves its provider's secret_ref,
//     which fails in a shell that does not hold the key (the proxy's plist
//     does) and can run an exec: helper — neither is owed for a route that is
//     already in place, and a key that differs in this shell must not replace
//     the one sync wrote.
type Change struct {
	Add            []config.Model
	AddMissingOnly bool
	Remove         []string
	RemoveFamilies []string
}

// ApplyChange writes ch in one locked read-modify-write and at most one
// restart, deciding the family removals against the document as read under
// the lock (so a route another process just wrote is seen). Per-model build
// failures are reported in Outcomes and do not block the rest. A missing or
// invalid config.yaml returns (Result{}, err) with nothing changed. The file
// is written, and the proxy restarted, only when the document actually
// changed.
func ApplyChange(cfg *config.Config, ch Change, o Options) (Result, error) {
	// Collected by the plan, under the lock, and published on the Result below.
	// A caller that asked for the deferral is told what it owes a second
	// attempt for; dropping an id here would lose the route.
	var missing []string
	res, err := applyPlanned(func(f *File) ([]plannedAdd, []plannedRemove) {
		adding := map[string]bool{}
		var add []plannedAdd
		for _, m := range ch.Add {
			adding[m.ID] = true
			if !isRegistryID(cfg, m.ID) && f.hasUnmarkedRow(m.ID) {
				continue
			}
			if ch.AddMissingOnly && f.row(m.ID) != nil {
				continue
			}
			// The row is missing and building it can run the provider's exec:
			// secret_ref helper synchronously. A caller that cannot afford that
			// wait (the picker's update goroutine, #253) defers the build and
			// makes it again through this same call where waiting is possible.
			if ch.AddMissingOnly && o.DeferMissingAdd {
				missing = append(missing, m.ID)
				continue
			}
			row, err := prepareModel(cfg, m)
			add = append(add, plannedAdd{id: m.ID, row: row, err: err})
		}
		var rm []plannedRemove
		seen := map[string]bool{}
		drop := func(id string, markedOnly bool) {
			// "" names no row. It must not reach removeRows, which would
			// match it against every marked row that has no model_name.
			if id == "" || adding[id] || seen[id] {
				return
			}
			seen[id] = true
			rm = append(rm, plannedRemove{id: id, markedOnly: markedOnly})
		}
		for _, id := range ch.Remove {
			drop(id, !isRegistryID(cfg, id))
		}
		for _, fam := range ch.RemoveFamilies {
			// "" is not a family: localmodels.Family and RowFamily both answer
			// "" for anything outside the local families (cloud providers and
			// rows, retired llamacpp), so matching it would remove every marked
			// cloud route and every family-less registry local row.
			if fam == "" {
				continue
			}
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
	}, o, OllamaAPIBase(cfg))
	if err != nil {
		return Result{}, err
	}
	res.Missing = missing
	return res, nil
}

// applyPlanned is the one locked read-modify-write every route change goes
// through (ApplyChange, Sync). The add/remove decision is made by plan against
// the document as read under the lock, so a caller that derives the change
// from the current routes never acts on a snapshot another process has since
// changed.
func applyPlanned(plan func(*File) ([]plannedAdd, []plannedRemove), o Options, ollamaBase string) (Result, error) {
	path := o.path()
	var res Result
	err := WithLock(o.Ctx, path, func() error {
		f, err := Open(path)
		if err != nil {
			return err
		}
		// After Open, so a machine with no config.yaml keeps answering
		// ErrMissing — which the route hooks treat as silence — whatever its
		// registry path is.
		if err := checkRegistryPairing(o); err != nil {
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
		// Written is only ever set on a "routed" outcome — an id the plan adds —
		// so a plan that adds nothing owes no digests. Marshaling every row of
		// model_list twice is the write path's most expensive step, and the
		// removal-only writes (every stop, every single-model family clear) have
		// no routed outcome to describe.
		var before map[string]string
		if len(add) > 0 {
			before = f.rowDigests()
		}
		if res.Outcomes, err = applyTo(f, add, remove, ollamaBase); err != nil {
			return err
		}
		f.EnsureSettings()
		if len(add) > 0 {
			after := f.rowDigests()
			for i, oc := range res.Outcomes {
				res.Outcomes[i].Written = oc.Action == "routed" && before[oc.ID] != after[oc.ID]
			}
		}
		// The scan runs after the repair (applyTo), so what it finds is the rows
		// the repair does not reach. Only a caller that reports it pays for it.
		if o.reportOllamaServe {
			res.ollamaServe = f.ollamaServeWarnings(loadProxyEnv())
		}
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

// applyTo carries a plan out on f in memory and returns what it did: the
// removals, the adds, then the api_base repair (File.EnsureOllamaAPIBase) on
// the rows that are left. It writes nothing to disk. applyPlanned saves the
// result; PlanSync runs it on a File it then discards, so the repairs a dry
// run lists are read off the same document the real sync would write — a row
// the plan replaces or removes is not also reported as repaired.
func applyTo(f *File, add []plannedAdd, remove []plannedRemove, ollamaBase string) ([]Outcome, error) {
	var out []Outcome
	for _, r := range remove {
		removed := false
		if r.markedOnly {
			removed = f.RemoveMarkedRows(r.id)
		} else {
			removed = f.RemoveRow(r.id)
		}
		// A removal that removed nothing is not an outcome. A family clear
		// names every registry local model of the family, routed or not;
		// recording each as "unrouted" made the Result claim routes were
		// removed that never existed (#195). Sync's removals come from the
		// rows themselves, so its report — what `wt litellm sync` prints
		// and modelman reads — never held one.
		if removed {
			out = append(out, Outcome{ID: r.id, Action: "unrouted"})
		}
	}
	for _, a := range add {
		if a.err != nil {
			out = append(out, Outcome{ID: a.id, Err: a.err})
			continue
		}
		// A plan owes every add a row or an error. One with neither is a
		// planner bug; report it against its id rather than hand SetRow a
		// nil node, which would break the whole document.
		if a.row == nil {
			out = append(out, Outcome{ID: a.id, Err: fmt.Errorf("model %q: no row was built", a.id)})
			continue
		}
		if err := f.SetRow(a.id, a.row); err != nil {
			return nil, err
		}
		out = append(out, Outcome{ID: a.id, Action: "routed"})
	}
	for _, id := range f.EnsureOllamaAPIBase(ollamaBase) {
		out = append(out, Outcome{ID: id, Action: ActionAPIBaseSet})
	}
	return out, nil
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
	// Repair names the ollama rows, hand-written ones included, whose empty
	// api_base the write will fill (File.EnsureOllamaAPIBase). Only PlanSync
	// fills it: the real sync reports each repair as an Outcome.
	Repair []string
	// Warnings names the rows the write leaves in a state LiteLLM starts its
	// own `ollama serve` for (File.ollamaServeWarnings). Only PlanSync fills
	// it: the real sync puts them in Result.Warnings.
	Warnings []string
	Errors   []Outcome
	// markedOnly holds the Remove ids owned by marker alone (not named like a
	// managed registry id): Sync drops only their marked rows.
	markedOnly map[string]bool
}

// planSync decides a sync against f. Desired = every CloudModels id plus
// the caller's local models (#179 Phase B: running or pulled, registered or
// discovered). A row is wt's to remove when it carries the marker or is named
// like a managed registry id (cloud or local) — the latter covers rows
// written before the marker existed. Unmarked rows with any other name are
// hand-written and never touched, and a desired DISCOVERED id that already
// has a hand-written row is left to it (no add, no adoption). Untouched ids,
// and every row whose RowFamily is in UntouchedFamilies, are neither added,
// rewritten nor removed; Recheck (see Options) re-verifies local ids. Built
// rows for plan.Add are returned alongside the plan: Sync's write path reuses
// them instead of preparing every changed row a second time.
//
// Accepted cost of UntouchedFamilies: a marked row of a DELETED cloud model
// whose id starts with a local family prefix (e.g. "ollama/x:cloud") reads as
// that family's row, so it stays while the family is untrusted and goes on
// the next sync whose probe is healthy.
func planSync(cfg *config.Config, f *File, localIn []config.Model, o Options) (SyncPlan, map[string]*yaml.Node) {
	localModels := LocalModels(cfg)
	untouchedFam := map[string]bool{}
	for _, fam := range o.UntouchedFamilies {
		// RowFamily is "" for every cloud and hand-aliased row: an empty
		// entry would freeze all of them, so it names no family.
		if fam != "" {
			untouchedFam[fam] = true
		}
	}
	frozen := func(id string) bool {
		return slices.Contains(o.Untouched, id) || untouchedFam[RowFamily(cfg, id)]
	}
	managed := map[string]bool{}
	var desired []config.Model
	for _, m := range CloudModels(cfg) {
		managed[m.ID] = true
		desired = append(desired, m)
	}
	// local is every id Recheck may prune: registry local ids, the desired
	// local ids (discovered ones included) and every row of a local family.
	local := map[string]bool{}
	for _, m := range localModels {
		managed[m.ID] = true
		local[m.ID] = true
	}
	// gap holds registry models dropped from both lists by a data gap (a
	// provider_id naming no provider, or no location to resolve): their rows
	// stay until the registry is repaired. Removing them would delete every
	// route — and its hand-added params — the moment `providers = []` lands.
	gap := map[string]bool{}
	for _, m := range cfg.Models {
		if RegistryGap(cfg, m) {
			gap[m.ID] = true
		}
	}
	// A registry local id is wanted only when the caller hands in the registry
	// model itself. A discovered model whose id merely spells a registry id
	// (an unregistered artifact "foo" beside the registry model "ollama/foo",
	// which serves another model_name) is dropped: the registry model owns
	// the id, and routing it on the artifact's word would point the route at
	// a model that may not be on disk.
	wantLocal := map[string]bool{}
	for _, m := range localIn {
		if i := config.IndexModelByID(cfg.Models, m.ID); i >= 0 &&
			(cfg.Models[i].ProviderID != m.ProviderID || cfg.Models[i].ModelName != m.ModelName) {
			continue
		}
		wantLocal[m.ID] = true
	}
	// Registry local models keep registry order; discovered ones follow in
	// the caller's order.
	for _, m := range localModels {
		if wantLocal[m.ID] && !frozen(m.ID) {
			desired = append(desired, m)
			local[m.ID] = true
		}
	}
	for _, m := range localIn {
		if isRegistryID(cfg, m.ID) || frozen(m.ID) {
			continue
		}
		local[m.ID] = true
		if f.hasUnmarkedRow(m.ID) {
			continue // a hand-written row already serves this name
		}
		desired = append(desired, m)
	}
	for _, r := range f.Rows() {
		if RowFamily(cfg, r.ID) != "" {
			local[r.ID] = true
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
	for _, m := range desired {
		id := m.ID
		want[id] = true
		if err := providerCredErr(cfg, id, credErr); err != nil {
			plan.Errors = append(plan.Errors, Outcome{ID: id, Err: fmt.Errorf("model %q: %w", id, err)})
			continue
		}
		row, err := prepareModel(cfg, m)
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
		if want[r.ID] || gap[r.ID] || frozen(r.ID) || seen[r.ID] {
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

// planned turns the plan into the adds and removals applyTo carries out,
// handing each add the row planSync already built for it.
func (plan SyncPlan) planned(built map[string]*yaml.Node) ([]plannedAdd, []plannedRemove) {
	add := make([]plannedAdd, 0, len(plan.Add))
	for _, id := range plan.Add {
		add = append(add, plannedAdd{id: id, row: built[id]})
	}
	rm := make([]plannedRemove, 0, len(plan.Remove))
	for _, id := range plan.Remove {
		rm = append(rm, plannedRemove{id: id, markedOnly: plan.markedOnly[id]})
	}
	return add, rm
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
func PlanSync(cfg *config.Config, local []config.Model, o Options) (SyncPlan, error) {
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
	// The dry run refuses what the real sync refuses (applyPlanned).
	if err := checkRegistryPairing(o); err != nil {
		return SyncPlan{}, err
	}
	if err := f.checkModelList(); err != nil {
		return SyncPlan{}, err
	}
	plan, built := planSync(cfg, f, local, o)
	// The write also fills an empty api_base on ollama rows, and those can be
	// rows the user wrote by hand, so a dry run says which. They are found by
	// carrying the plan out on f, which is never saved: a row the plan adopts,
	// rewrites or removes is gone or rebuilt by the time the repair looks, and
	// listing it would promise an "api_base set" line the real sync never
	// prints.
	add, rm := plan.planned(built)
	done, err := applyTo(f, add, rm, OllamaAPIBase(cfg))
	if err != nil {
		return SyncPlan{}, err
	}
	for _, oc := range done {
		if oc.Action == ActionAPIBaseSet {
			plan.Repair = append(plan.Repair, oc.ID)
		}
	}
	plan.Warnings = f.ollamaServeWarnings(loadProxyEnv())
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

// Sync reconciles config.yaml with the registry and the local models the
// caller found (#179): every CloudModels id and every model in local —
// registry overlay or DiscoveredModel — gets a marked route; rows wt owns
// that are no longer desired are removed; hand-written rows are never touched
// (see planSync). The plan is made under the config.yaml lock. A config.yaml
// whose model_list is not a list is refused (ErrInvalid) before anything is
// planned or written, whatever the plan turns out to be — PlanSync checks the
// same shape, so a dry run and a real sync always agree. Outcome actions:
// "routed", "adopted", "rewritten", "unrouted", and ActionAPIBaseSet for an
// ollama row whose empty api_base the write filled; per-id build failures are
// reported with Err.
func Sync(cfg *config.Config, local []config.Model, o Options) (Result, error) {
	var plan SyncPlan
	// The one caller that reports the rows LiteLLM starts its own `ollama serve`
	// for, so the one caller that pays for the scan (Options.reportOllamaServe).
	o.reportOllamaServe = true
	res, err := applyPlanned(func(f *File) ([]plannedAdd, []plannedRemove) {
		p, built := planSync(cfg, f, local, o)
		plan = p
		// The plan already built every changed row while comparing it with the
		// row on disk; hand the rows through so the write path does not
		// prepare each one a second time.
		return plan.planned(built)
	}, o, OllamaAPIBase(cfg))
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
	res.Warnings = append(res.Warnings, res.ollamaServe...)
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
