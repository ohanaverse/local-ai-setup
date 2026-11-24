package litellm

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// The alias half of a route write (#596). After the model rows (route) and
// under the same config.yaml lock and 15 s deadline, a write plans each alias
// in its scope from the registry re-read there (Options.LoadRegistry) and
// carries the plan out with ApplyAliasDB: rows only in LiteLLM's database,
// never in config.yaml, never a restart, never a fallback. What fails is a
// warning. Sync covers every alias and deletes the rows of aliases the
// registry no longer has; ApplyChange covers only the aliases that target a
// model it changed and reports them in warnings alone.

// AliasLive is one target an alias plan gives a row: the registry model id
// and its LiteLLM deployment order.
type AliasLive struct {
	Target string
	Order  int
}

// AliasPlan is what one write does with one alias (SyncPlan.Aliases, the dry
// run's plan.aliases). Action is one of the AliasAction* values; Live holds
// the targets that get (or keep) a row built from the registry, in target
// order; Skipped the targets that get none (AliasSkip: Frozen ones keep the
// rows they have). Skipped reasons are warning text only: the JSON rendering
// carries the target and Frozen, never the reason.
type AliasPlan struct {
	ID, Action string
	Live       []AliasLive
	Skipped    []AliasSkip
}

// The actions an AliasPlan names.
const (
	AliasActionRoute   = "route"   // no row before, rows after
	AliasActionRewrite = "rewrite" // rows before and after, some changed
	AliasActionUnroute = "unroute" // rows before, none after
	AliasActionKeep    = "keep"    // rows, unchanged
	AliasActionNone    = "none"    // no row before or after (no live target)
	AliasActionSkip    = "skip"    // frozen whole: malformed, a fault, a twin
)

// aliasUnavailablePrefix starts every aliasUnavailable line.
const aliasUnavailablePrefix = "alias routes unavailable ("

// aliasUnavailable is decision 4's one line for a write whose alias half
// could not run: the registry write stands, nothing went to config.yaml, and
// any full sync repairs it.
func aliasUnavailable(reason string) string {
	return fmt.Sprintf(aliasUnavailablePrefix+"%s); registry written, LiteLLM not updated; run 'wt-model litellm sync' when the proxy answers", reason)
}

// Reasons aliasUnavailable names when the database half was never tried.
const (
	aliasNoAdmin = "no LiteLLM proxy pair is set"
	aliasDBOff   = "general_settings.store_model_in_db is not on in config.yaml"
)

// aliasWork is one write's alias half decided (planAliases): the plan per
// alias for the report, the row plan per alias that changes, and the
// warnings the decisions owe.
type aliasWork struct {
	plans    []AliasPlan
	rows     map[string]AliasRowPlan
	warnings []string
}

// planAliases decides the alias half against current (the database as
// listed). Pure. Every alias of reg in scope (only nil: all) is planned:
//
//   - one AliasFault rejects (a malformed alias, an id that is a model's, two
//     local targets, a target naming no model, ...), one whose id another
//     alias also has, one whose id is a route name fileNames holds
//     (config.yaml after this write) or a database row wt does not own, and
//     one an unmarked row under its DBAliasIDPrefix space squats, is frozen
//     whole: rows neither added nor deleted, one warning (Review Focus 6);
//   - otherwise its rows are DBModelsFromAlias(live): a local target live
//     only when routed after this write, a cloud target whose row cannot be
//     built frozen (decision 3), and a target frozen(id) says nothing may
//     touch kept as it is too; each frozen target one warning.
//
// keep (PlanAliasDB) answers from the row id's alias and target: true for a
// frozen alias, or for a frozen target still configured on its alias.
//
// sub is the write's subscription check, for Claude subscription targets
// (dbModelsFromAlias).
func planAliases(reg *config.Config, current []DBModel, hashKey string, only map[string]bool, live, frozen func(string) bool, fileNames map[string]bool, deleteOrphans bool, sub aliasState) aliasWork {
	var w aliasWork
	had := map[string]int{}
	var foreign []DBModel // database rows wt's alias path does not own
	for _, m := range current {
		if IsAliasDB(m) {
			if a, _, ok := parseAliasID(m.ID); ok {
				had[a]++
			}
			continue
		}
		foreign = append(foreign, m)
	}
	perID := map[string]int{}
	for _, a := range reg.Aliases {
		perID[a.ID]++
	}
	desired := map[string][]DBModel{}
	frozenAlias := map[string]bool{}
	frozenPair := map[string]map[string]bool{}
	configured := map[string]map[string]bool{}
	planned := map[string]bool{}
	add := func(p AliasPlan) {
		w.plans = append(w.plans, p)
		planned[p.ID] = true
	}
	seen := map[string]bool{}
	for _, a := range reg.Aliases {
		if only != nil && !only[a.ID] || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		skip := func(warning, reason string) {
			frozenAlias[a.ID] = true
			w.warnings = append(w.warnings, warning+"; its database routes are left as they are")
			p := AliasPlan{ID: a.ID, Action: AliasActionSkip}
			for _, t := range reg.AliasTargets(a) {
				p.Skipped = append(p.Skipped, AliasSkip{Target: t.Model, Reason: reason, Frozen: true})
			}
			add(p)
		}
		if n := perID[a.ID]; n > 1 {
			reason := fmt.Sprintf("the registry has %d aliases with this id", n)
			skip(a.ID+": "+reason, reason)
			continue
		}
		if err := config.AliasFault(reg, a); err != nil {
			skip(oneLine(err), oneLine(err))
			continue
		}
		if fileNames[a.ID] {
			reason := "config.yaml has a row with this name"
			skip(a.ID+": "+reason+"; no database route is added beside it", reason)
			continue
		}
		if i := slices.IndexFunc(foreign, func(m DBModel) bool {
			alias, _, ok := parseAliasID(m.ID)
			return strings.HasPrefix(m.ID, DBAliasIDPrefix) && (ok && alias == a.ID || m.ModelName == a.ID)
		}); i >= 0 {
			reason := fmt.Sprintf("database row %s is under %s without %s", foreign[i].ID, DBAliasIDPrefix, AliasManagedKey)
			skip(fmt.Sprintf("%s: %s, so wt cannot write the alias's rows; remove that row by hand (the LiteLLM UI, or POST /model/delete), then run 'wt-model litellm sync'", a.ID, reason), reason)
			continue
		}
		if i := slices.IndexFunc(foreign, func(m DBModel) bool { return m.ModelName == a.ID }); i >= 0 {
			reason := fmt.Sprintf("LiteLLM's database has a row named %s that wt does not own (%s)", a.ID, foreign[i].ID)
			skip(fmt.Sprintf("%s: %s; no alias route is added beside it", a.ID, reason), reason)
			continue
		}
		rows, skips := dbModelsFromAlias(reg, a, live, hashKey, sub)
		pairs := map[string]bool{}
		configured[a.ID] = pairs
		for _, t := range reg.AliasTargets(a) {
			pairs[t.Model] = true
		}
		fz := map[string]bool{}
		frozenPair[a.ID] = fz
		rows = slices.DeleteFunc(rows, func(m DBModel) bool {
			target, _ := m.Info[AliasTargetKey].(string)
			if !frozen(target) {
				return false
			}
			skips = append(skips, AliasSkip{Target: target, Reason: "its state is unknown to this sync (its provider's probe was not trusted)", Frozen: true})
			return true
		})
		for _, s := range skips {
			if s.Frozen {
				fz[s.Target] = true
				w.warnings = append(w.warnings, fmt.Sprintf("%s: target %s is kept as it is (%s)", a.ID, s.Target, s.Reason))
			}
		}
		if rows == nil {
			rows = []DBModel{}
		}
		desired[a.ID] = rows
		p := AliasPlan{ID: a.ID, Skipped: skips}
		for _, m := range rows {
			target, _ := m.Info[AliasTargetKey].(string)
			p.Live = append(p.Live, AliasLive{Target: target, Order: paramOrder(m.Params["order"])})
		}
		if len(rows) == 0 && len(fz) == 0 {
			w.warnings = append(w.warnings, fmt.Sprintf("%s has no live target", a.ID))
		}
		add(p)
	}
	keep := func(alias, target string) bool {
		return frozenAlias[alias] || configured[alias][target] && frozenPair[alias][target]
	}
	plan := PlanAliasDB(desired, current, hashKey, keep, deleteOrphans)
	w.rows = map[string]AliasRowPlan{}
	for _, rp := range plan.Aliases {
		w.rows[rp.Alias] = rp
		if !planned[rp.Alias] { // an orphan: its alias is gone from the registry
			add(AliasPlan{ID: rp.Alias})
		}
	}
	for i := range w.plans {
		p := &w.plans[i]
		if p.Action == AliasActionSkip {
			continue
		}
		rp := w.rows[p.ID]
		after := had[p.ID] - len(rp.Delete) + len(rp.Add)
		switch {
		case rp.empty() && had[p.ID] > 0:
			p.Action = AliasActionKeep
		case rp.empty():
			p.Action = AliasActionNone
		case had[p.ID] == 0:
			p.Action = AliasActionRoute
		case after <= 0:
			p.Action = AliasActionUnroute
		default:
			p.Action = AliasActionRewrite
		}
	}
	slices.SortStableFunc(w.plans, func(a, b AliasPlan) int { return strings.Compare(a.ID, b.ID) })
	return w
}

// paramOrder reads litellm_params.order: an int as built, a float64 as
// listed back from the API.
func paramOrder(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// routeAliases carries the alias half of one write out, after route() and
// under d.context(): planAliases over d.current (the listing this write made;
// the model half never touches an alias row, so it still holds them), then
// one ApplyAliasDB per alias that changes, so a failure is pinned to its
// alias. Every failure is a warning and never a fallBack: a transport error,
// an ended deadline or database models off stops the half with decision 4's
// line; any other failure is the apply's own warning and that alias gets no
// outcome. outcomes is one per alias whose change was applied whole:
// "routed", "rewritten" or "unrouted" (Sync reports them; ApplyChange drops
// them). Nothing here restarts the proxy or touches config.yaml.
func (d *dbRoutes) routeAliases(reg *config.Config, only map[string]bool, live func(string) bool, frozen func(string) bool, deleteOrphans bool, sub aliasState) (warnings []string, outcomes []Outcome) {
	// A scratch registry paired with the real proxy's database (the
	// default wt-model home) did not write the rows there: add, never
	// delete (no orphan, stale or replaced row).
	noDeletes := scratchRegistryRealProxy()
	w := planAliases(reg, d.current, d.admin.Key, only, live, frozen, d.fileNames, deleteOrphans && !noDeletes, sub)
	warnings = w.warnings
	held := false
	for _, p := range w.plans {
		rp, ok := w.rows[p.ID]
		if !ok {
			continue
		}
		if noDeletes && (len(rp.Delete) > 0 || len(rp.Replace) > 0) {
			held = true
			rp.Delete, rp.Replace = nil, nil
			if rp.empty() {
				continue
			}
		}
		ctx := d.context()
		if err := ctx.Err(); err != nil {
			return append(warnings, aliasUnavailable(oneLine(err))), outcomes
		}
		_, _, warns, err := d.admin.ApplyAliasDB(ctx, AliasDBPlan{Aliases: []AliasRowPlan{rp}})
		warnings = append(warnings, warns...)
		if err != nil {
			if stopApply(ctx, err) || errors.Is(err, ErrDBModelsOff) {
				return append(warnings, aliasUnavailable(oneLine(err))), outcomes
			}
			warnings = append(warnings, oneLine(err))
			continue
		}
		if len(warns) > 0 {
			continue
		}
		switch p.Action {
		case AliasActionRoute:
			outcomes = append(outcomes, Outcome{ID: p.ID, Action: "routed", Written: true})
		case AliasActionRewrite:
			outcomes = append(outcomes, Outcome{ID: p.ID, Action: "rewritten", Written: true})
		case AliasActionUnroute:
			outcomes = append(outcomes, Outcome{ID: p.ID, Action: "unrouted"})
		}
	}
	if held || noDeletes && deleteOrphans && hasOrphans(d.current, reg) {
		warnings = append(warnings, fmt.Sprintf("the registry is redirected (%s) but the proxy pair is the default wt-model home's (%s): no alias row was deleted; redirect HOME or XDG_CONFIG_HOME too for a scratch run",
			config.RegistryPath(), config.ModelHome()))
	}
	return warnings, outcomes
}

// hasOrphans reports whether current holds an alias row (IsAliasDB) of an
// alias reg does not have.
func hasOrphans(current []DBModel, reg *config.Config) bool {
	return slices.ContainsFunc(current, func(m DBModel) bool {
		alias, _, ok := parseAliasID(m.ID)
		return ok && IsAliasDB(m) && !slices.ContainsFunc(reg.Aliases, func(a config.Alias) bool { return a.ID == alias })
	})
}

// leftAfter is the route names this write leaves, for the alias half's
// liveness (decision 3): config.yaml's rows after the plan's removals plus
// the adds it makes (file), and with them wt's database model rows after
// route() (all). It also records file as d.fileNames, the twin guard's view.
func (d *dbRoutes) leftAfter(f *File, fileAdd []plannedAdd, rm []plannedRemove) map[string]bool {
	file := map[string]bool{}
	for _, r := range f.Rows() {
		if slices.ContainsFunc(rm, func(p plannedRemove) bool { return p.id == r.ID && (!p.markedOnly || r.Managed) }) {
			continue
		}
		file[r.ID] = true
	}
	for _, a := range fileAdd {
		if a.err == nil && a.row != nil {
			file[a.id] = true
		}
	}
	all := map[string]bool{}
	for id := range file {
		all[id] = true
	}
	for _, r := range d.routeRows() {
		if !slices.Contains(d.unrouted, r.ID) {
			all[r.ID] = true
		}
	}
	if !d.fellBack {
		for _, r := range d.routed {
			all[r.id] = true
		}
	}
	for _, id := range d.migrated {
		all[id] = true
	}
	d.fileNames = file
	return all
}

// aliasFrozen is the targets the alias half must leave as they are: the
// ones Options.Untouched names and those of an untrusted family (the same
// predicate planSync freezes model rows with).
func aliasFrozen(o Options, reg *config.Config) func(string) bool {
	fams := map[string]bool{}
	for _, f := range o.UntouchedFamilies {
		if f != "" {
			fams[f] = true
		}
	}
	return func(id string) bool {
		return slices.Contains(o.Untouched, id) || fams[RowFamily(reg, id)]
	}
}

// scratchRegistryRealProxy reports whether the registry is redirected
// (config.RegistryRedirected: WT_REGISTRY and the like) while the wt-model
// home, where the proxy pair comes from, is still the default one under the
// home directory. Then the database a write reaches is the real proxy's,
// and the registry's aliases are not the ones that wrote its alias rows:
// the alias half may add rows but never deletes one (no orphan or stray
// delete). With no home directory the answer is false.
func scratchRegistryRealProxy() bool {
	if !config.RegistryRedirected() {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return filepath.Clean(config.ModelHome()) == filepath.Join(home, ".config", "wt-model")
}

// loadRegistry is Options.LoadRegistry, config.Load when unset.
func (o Options) loadRegistry() (*config.Config, error) {
	if o.LoadRegistry != nil {
		return o.LoadRegistry()
	}
	return config.Load()
}

// changedIDs reports whether a model id is one ch changes: added, removed, or
// of a family it clears (judged against cfg).
func changedIDs(cfg *config.Config, ch Change) func(string) bool {
	ids := map[string]bool{}
	for _, m := range ch.Add {
		ids[m.ID] = true
	}
	for _, id := range ch.Remove {
		ids[id] = true
	}
	return func(id string) bool {
		if ids[id] {
			return true
		}
		fam := RowFamily(cfg, id)
		return fam != "" && slices.Contains(ch.RemoveFamilies, fam)
	}
}

// aliasesTouching is the ids of reg's aliases with a target ch changes:
// ApplyChange's alias scope. nil when there is none (touchesAlias false).
func aliasesTouching(reg *config.Config, ch Change) map[string]bool {
	changed := changedIDs(reg, ch)
	var out map[string]bool
	for _, a := range reg.Aliases {
		if slices.ContainsFunc(reg.AliasTargets(a), func(t config.AliasTarget) bool { return changed(t.Model) }) {
			if out == nil {
				out = map[string]bool{}
			}
			out[a.ID] = true
		}
	}
	return out
}

// touchesAlias reports whether ch changes some alias's target in reg.
func touchesAlias(reg *config.Config, ch Change) bool {
	return len(aliasesTouching(reg, ch)) > 0
}

// listedAliasTargets reports whether the listing holds an alias row (any
// alias) whose target, read from its id, is one changed says: a row a
// caller's stale cfg cannot know about still sends the change to the alias
// half, which then re-reads the registry.
func (d *dbRoutes) listedAliasTargets(changed func(string) bool) bool {
	return slices.ContainsFunc(d.current, func(m DBModel) bool {
		_, target, ok := parseAliasID(m.ID)
		return ok && IsAliasDB(m) && (changed == nil || changed(target))
	})
}

// aliasHalf runs the alias half of a listed write: it re-reads the registry
// (a failure is one warning and nothing more), narrows the scope (scope nil:
// every alias, else the aliases scope picks from the re-read registry),
// computes liveness from what this write leaves and calls routeAliases. With
// changed set (ApplyChange) it then deletes the leftover rows of the changed
// targets that no alias in scope accounts for (deleteStrays). Its warnings
// and outcomes are kept for finish.
func (d *dbRoutes) aliasHalf(o Options, f *File, fileAdd []plannedAdd, rm []plannedRemove, scope func(*config.Config) map[string]bool, changed func(*config.Config) func(string) bool, deleteOrphans bool) {
	reg, err := o.loadRegistry()
	if err != nil {
		d.aliasWarnings = append(d.aliasWarnings, aliasUnavailable("the registry could not be re-read: "+oneLine(err)))
		return
	}
	var only map[string]bool
	if scope != nil {
		only = scope(reg)
	}
	unavailable := false
	if scope == nil || len(only) > 0 {
		all := d.leftAfter(f, fileAdd, rm)
		w, oc := d.routeAliases(reg, only, func(id string) bool { return all[id] }, aliasFrozen(o, reg), deleteOrphans, aliasStateOf(o.subscription))
		d.aliasWarnings = append(d.aliasWarnings, w...)
		d.aliasOutcomes = append(d.aliasOutcomes, oc...)
		// Decision 4 is one line per write: when the route pass already said
		// the database half is unavailable, the stray pass stays quiet.
		unavailable = slices.ContainsFunc(w, func(l string) bool { return strings.HasPrefix(l, aliasUnavailablePrefix) })
	}
	if changed != nil && !unavailable {
		d.aliasWarnings = append(d.aliasWarnings, d.deleteStrays(only, changed(reg), aliasFrozen(o, reg))...)
	}
}

// deleteStrays deletes, per alias, the listed alias rows (IsAliasDB) whose
// target (read from the row id) is a changed model and whose alias is not in
// scope: the registry no longer pairs that alias with that target (an alias
// re-pointed or removed while the database half was down), so the row is a
// leftover routing to a model this write starts or stops. It touches no
// other row, no alias without such a row, and nothing at all while a
// redirected registry is paired with the default wt-model home's proxy
// (scratchRegistryRealProxy: a scratch registry's aliases say nothing about
// the real proxy's rows) or for a frozen target. Every failure is a warning.
func (d *dbRoutes) deleteStrays(scope map[string]bool, changed, frozen func(string) bool) []string {
	if scratchRegistryRealProxy() {
		return nil
	}
	byAlias := map[string][]string{}
	for _, m := range d.current {
		alias, target, ok := parseAliasID(m.ID)
		if !ok || !IsAliasDB(m) || scope[alias] || !changed(target) || frozen(target) {
			continue
		}
		byAlias[alias] = append(byAlias[alias], m.ID)
	}
	names := slices.Sorted(maps.Keys(byAlias))
	var warnings []string
	for _, alias := range names {
		ids := byAlias[alias]
		slices.Sort(ids)
		ctx := d.context()
		if err := ctx.Err(); err != nil {
			return append(warnings, aliasUnavailable(oneLine(err)))
		}
		_, _, warns, err := d.admin.ApplyAliasDB(ctx, AliasDBPlan{Aliases: []AliasRowPlan{{Alias: alias, Delete: ids}}})
		warnings = append(warnings, warns...)
		if err != nil {
			if stopApply(ctx, err) || errors.Is(err, ErrDBModelsOff) {
				return append(warnings, aliasUnavailable(oneLine(err)))
			}
			warnings = append(warnings, oneLine(err))
		}
	}
	return warnings
}

// previewAliases is the dry run's alias half (PlanSync, DesiredIDs): the
// plan for every alias of cfg against the listing, writing nothing. live is
// the route names the sync would leave; fileNames config.yaml's.
func (d *dbRoutes) previewAliases(cfg *config.Config, o Options, live, fileNames map[string]bool) aliasWork {
	return planAliases(cfg, d.current, d.admin.Key, nil, func(id string) bool { return live[id] }, aliasFrozen(o, cfg), fileNames, true, aliasStateOf(o.subscription))
}

// hasAliasRow reports whether the listing holds a row of alias id
// (IsAliasDB, the alias read from the row id).
func (d *dbRoutes) hasAliasRow(id string) bool {
	return slices.ContainsFunc(d.current, func(m DBModel) bool {
		a, _, ok := parseAliasID(m.ID)
		return ok && a == id && IsAliasDB(m)
	})
}

// PreviewAlias is the alias half of `wt-model alias set --dry-run`: the rows
// alias a would have after the write, and the targets that get none with the
// write's own reason. The liveness and the Claude subscription check come from
// PlanSync, the planner the write itself uses, so a preview cannot disagree
// with it (#703: a local-only liveness said "not routed" for a subscription
// target the write routed). cfg is the registry as the write would leave it
// and o the options the write builds (SyncInputs, with Admin set); a nil
// Admin has no row plan, and the caller says so before it asks. A plan that
// covers no aliases (the database half cannot run: the proxy does not
// answer, store_model_in_db is off) is the error, quoted from the plan's own
// warnings, which is what the write itself reports.
func PreviewAlias(cfg *config.Config, local []config.Model, o Options, a config.Alias) ([]DBModel, []AliasSkip, error) {
	if o.Admin == nil {
		return nil, nil, errors.New("no LiteLLM proxy pair: no rows would be written")
	}
	plan, err := PlanSync(cfg, local, o)
	if err != nil {
		return nil, nil, err
	}
	i := slices.IndexFunc(plan.Aliases, func(p AliasPlan) bool { return p.ID == a.ID })
	if i < 0 {
		// The plan has no alias entries exactly when the database half cannot
		// run (the proxy does not answer, store_model_in_db is off): PlanSync
		// then says so in its warnings and the write reports the same line.
		// Say that, not "no plan entry".
		if w := planAliasUnavailableWarning(plan.Warnings); w != "" {
			return nil, nil, errors.New(w)
		}
		return nil, nil, fmt.Errorf("the sync plan has no entry for %s", a.ID)
	}
	routed := map[string]bool{}
	for _, l := range plan.Aliases[i].Live {
		routed[l.Target] = true
	}
	rows, _ := dbModelsFromAlias(cfg, a, func(id string) bool { return routed[id] }, o.Admin.Key, aliasSubClean)
	rows = slices.DeleteFunc(rows, func(m DBModel) bool {
		target, _ := m.Info[AliasTargetKey].(string)
		return !routed[target]
	})
	return rows, plan.Aliases[i].Skipped, nil
}

// planAliasUnavailableWarning is the plan's own line for a sync whose alias
// half cannot run ("alias routes unavailable (...); this plan does not cover
// aliases", PlanSync), or "" when its warnings name none.
func planAliasUnavailableWarning(warnings []string) string {
	for _, w := range warnings {
		if strings.HasPrefix(w, aliasUnavailablePrefix) {
			return w
		}
	}
	return ""
}
