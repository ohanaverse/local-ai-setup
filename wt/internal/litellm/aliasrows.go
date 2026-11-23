package litellm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Alias database rows (#596). A registry alias becomes one LiteLLM database
// row per live target: the target's own row with model_name set to the alias
// id and litellm_params.order set to the target's order. They live in an id
// space and under a marker of their own so the model-row path (PlanDB,
// ApplyDB, dbRoutes.owned and routeRows, AdminClient.Routes) of this binary
// and of every shipped phase 1 binary never sees them: no wt_managed key,
// and an id that does not start with DBIDPrefix.
const (
	// DBAliasIDPrefix starts every alias row id:
	// "wta:alias/<name>/<h8>/<target id>", h8 the first 8 hex of the row's
	// routeHash. ApplyAliasDB touches no id without it.
	DBAliasIDPrefix = "wta:alias/"
	// AliasManagedKey is the model_info marker of an alias row (true).
	AliasManagedKey = "wt_alias_managed"
	// AliasKey and AliasTargetKey name, in model_info, the alias id and the
	// registry model id of the target the row routes to (informational: the
	// plan reads both from the id).
	AliasKey       = "wt_alias"
	AliasTargetKey = "wt_alias_target"
)

// IsAliasDB reports whether m is an alias row wt wrote: model_info carries
// wt_alias_managed: true and the id starts with DBAliasIDPrefix.
func IsAliasDB(m DBModel) bool {
	v, _ := m.Info[AliasManagedKey].(bool)
	return v && strings.HasPrefix(m.ID, DBAliasIDPrefix)
}

// AliasSkip is a target that got no row. Frozen means its row could not be
// built (a cloud key unset in serve's environment, a target the registry
// cannot resolve): its existing rows are kept and nothing is added
// (decision 3). Not frozen means it is not live (a local target that is not
// running): it has no row, and an existing one goes.
type AliasSkip struct {
	Target, Reason string
	Frozen         bool
}

// DBModelsFromAlias builds the database rows of alias a: one per live
// target, built as that target's model row is (prepareModel, then
// ensureRowParams and DBModelFromRow), with model_name the alias id,
// litellm_params.order the target's order (targets with equal order share
// the requests, measured on LiteLLM 1.109.1), model_info without wt_managed and with AliasManagedKey,
// AliasKey and AliasTargetKey, and the id
// DBAliasIDPrefix + "<name>/<h8>/<target id>", h8 taken from the row's
// routeHash under hashKey before any hash is stamped (ApplyAliasDB stamps
// it). Every content change is therefore a new id.
//
// A local target for which live(id) is false is skipped "not running"; a
// target whose row fails to build is skipped Frozen with the build error
// (which names a secret_ref, never a resolved key). A malformed alias, or one
// whose id is not "alias/<name>", has every target skipped Frozen. Rows come
// in target order.
func DBModelsFromAlias(cfg *config.Config, a config.Alias, live func(id string) bool, hashKey string) ([]DBModel, []AliasSkip) {
	name, isAlias := strings.CutPrefix(a.ID, config.AliasIDPrefix)
	if a.Malformed != "" || !isAlias || name == "" || strings.Contains(name, "/") {
		reason := "malformed alias: " + a.Malformed
		if a.Malformed == "" {
			reason = fmt.Sprintf("alias id %q is not %s<name>", a.ID, config.AliasIDPrefix)
		}
		skips := make([]AliasSkip, 0, len(a.Targets))
		for _, t := range a.Targets {
			skips = append(skips, AliasSkip{Target: t.Model, Reason: reason, Frozen: true})
		}
		return nil, skips
	}
	var rows []DBModel
	var skips []AliasSkip
	frozen := func(t config.AliasTarget, format string, args ...any) {
		skips = append(skips, AliasSkip{Target: t.Model, Reason: fmt.Sprintf(format, args...), Frozen: true})
	}
	for i, t := range a.Targets {
		m, ok := onlyModel(cfg, t.Model)
		if !ok {
			frozen(t, "target %q does not name exactly one registry model", t.Model)
			continue
		}
		loc, err := cfg.ResolveLocation(m)
		if err != nil {
			frozen(t, "%s", oneLine(err))
			continue
		}
		if loc == config.LocationLocal && (live == nil || !live(m.ID)) {
			skips = append(skips, AliasSkip{Target: t.Model, Reason: "not running"})
			continue
		}
		row, err := prepareModel(cfg, m)
		if err != nil {
			frozen(t, "%s", oneLine(err))
			continue
		}
		ensureRowParams(row)
		dm, err := DBModelFromRow(row)
		if err != nil {
			frozen(t, "%s", oneLine(err))
			continue
		}
		order := t.Order
		if order < 1 {
			order = i + 1 // the reader's default: the target's 1-based position
		}
		dm.Params["order"] = order
		dm.ModelName = a.ID
		delete(dm.Info, ManagedKey)
		delete(dm.Info, ParamsHashKey)
		dm.Info[AliasManagedKey] = true
		dm.Info[AliasKey] = a.ID
		dm.Info[AliasTargetKey] = t.Model
		h := routeHash(dm, hashKey)
		if len(h) < 8 {
			frozen(t, "its row cannot be encoded")
			continue
		}
		dm.ID = DBAliasIDPrefix + name + "/" + h[:8] + "/" + t.Model
		rows = append(rows, dm)
	}
	return rows, skips
}

// onlyModel is the model row id names when exactly one row has it.
func onlyModel(cfg *config.Config, id string) (config.Model, bool) {
	var found config.Model
	n := 0
	for _, m := range cfg.Models {
		if m.ID == id {
			found = m
			n++
		}
	}
	return found, n == 1
}

// parseAliasID splits an alias row id into its alias id and target id; ok is
// false for an id that does not have the DBAliasIDPrefix shape.
func parseAliasID(id string) (alias, target string, ok bool) {
	rest, has := strings.CutPrefix(id, DBAliasIDPrefix)
	if !has {
		return "", "", false
	}
	name, rest, has := strings.Cut(rest, "/")
	if !has || name == "" {
		return "", "", false
	}
	h8, target, has := strings.Cut(rest, "/")
	if !has || len(h8) != 8 || target == "" {
		return "", "", false
	}
	return config.AliasIDPrefix + name, target, true
}

// AliasRowPlan is what one alias's re-point changes: rows to add, rows to
// replace (an id match whose stored hash differs: Delete then Add) and ids of
// rows to delete.
type AliasRowPlan struct {
	Alias   string
	Add     []DBModel
	Replace []DBModel
	Delete  []string
}

func (p AliasRowPlan) empty() bool { return len(p.Add)+len(p.Replace)+len(p.Delete) == 0 }

// AliasDBPlan is the alias half of a database sync, one entry per alias that
// changes, sorted by alias id.
type AliasDBPlan struct {
	Aliases []AliasRowPlan
}

// Empty reports whether the plan changes nothing.
func (p AliasDBPlan) Empty() bool {
	return !slices.ContainsFunc(p.Aliases, func(a AliasRowPlan) bool { return !a.empty() })
}

// PlanAliasDB compares desired (rows keyed by in-scope alias id, as
// DBModelsFromAlias builds them) with the database's alias rows. Only current
// rows that are IsAliasDB and whose id has the alias-row shape are looked at,
// grouped by the alias their id names: a model row, a LiteLLM UI row, or a
// row under the prefix without the marker is never planned.
//
// A desired row missing from the database is an Add; one whose id is there
// but whose stored hash does not match (hashMatches under hashKey: edited
// outside wt) is a Replace. A current row of an alias in desired that is not
// desired is deleted unless keep(alias, target) — the caller answers true
// only for a frozen alias, or for a still-configured pair whose target is
// frozen, so a row whose alias is gone or whose target was dropped is deleted
// whatever the freeze. Rows of an alias absent from desired are planned
// (with the same keep) only when deleteOrphans. Lists sorted by id. Pure.
func PlanAliasDB(desired map[string][]DBModel, current []DBModel, hashKey string, keep func(alias, target string) bool, deleteOrphans bool) AliasDBPlan {
	byAlias := map[string][]DBModel{}
	for _, m := range current {
		if !IsAliasDB(m) {
			continue
		}
		alias, _, ok := parseAliasID(m.ID)
		if !ok {
			continue
		}
		byAlias[alias] = append(byAlias[alias], m)
	}
	var names []string
	for a := range desired {
		names = append(names, a)
	}
	if deleteOrphans {
		for a := range byAlias {
			if _, in := desired[a]; !in {
				names = append(names, a)
			}
		}
	}
	slices.Sort(names)
	var p AliasDBPlan
	byID := func(a, b DBModel) int { return strings.Compare(a.ID, b.ID) }
	for _, alias := range names {
		rp := AliasRowPlan{Alias: alias}
		cur := make(map[string]DBModel, len(byAlias[alias]))
		for _, m := range byAlias[alias] {
			cur[m.ID] = m
		}
		want := map[string]bool{}
		for _, d := range desired[alias] {
			if want[d.ID] {
				continue
			}
			want[d.ID] = true
			r, ok := cur[d.ID]
			switch {
			case !ok:
				rp.Add = append(rp.Add, d)
			case !hashMatches(r, d, hashKey):
				rp.Replace = append(rp.Replace, d)
			}
		}
		for _, m := range byAlias[alias] {
			if want[m.ID] {
				continue
			}
			_, target, _ := parseAliasID(m.ID)
			if keep != nil && keep(alias, target) {
				continue
			}
			rp.Delete = append(rp.Delete, m.ID)
		}
		if rp.empty() {
			continue
		}
		slices.SortFunc(rp.Add, byID)
		slices.SortFunc(rp.Replace, byID)
		slices.Sort(rp.Delete)
		p.Aliases = append(p.Aliases, rp)
	}
	return p
}

// aliasPlanFault refuses an alias plan that names anything outside its own
// rows: an id without DBAliasIDPrefix or naming another alias, or a row to
// write that lacks the alias marker or another model_name. nil when sound.
func aliasPlanFault(a AliasRowPlan) error {
	var errs []error
	mine := func(id string) bool {
		alias, _, ok := parseAliasID(id)
		return ok && alias == a.Alias
	}
	for _, m := range append(slices.Clone(a.Add), a.Replace...) {
		if !mine(m.ID) || !IsAliasDB(m) || m.ModelName != a.Alias {
			errs = append(errs, fmt.Errorf("litellm: refusing to write database model %q for %s: not one of its alias rows (%s<name>/<h8>/<target>, %s)", m.ID, a.Alias, DBAliasIDPrefix, AliasManagedKey))
		}
	}
	for _, id := range a.Delete {
		if !mine(id) {
			errs = append(errs, fmt.Errorf("litellm: refusing to delete database model %q for %s: not one of its alias rows", id, a.Alias))
		}
	}
	return errors.Join(errs...)
}

// ApplyAliasDB carries out p one alias at a time, add-first so a re-point
// never leaves the alias name without a row (Review Focus 2):
//
//  1. the Adds, each through addConverged with IsAliasDB (stamping
//     wt_params_sha; a duplicate id is LiteLLM's HTTP 500 and converges only
//     when a re-list shows the same hash);
//  2. one re-list, confirming every Add listed, IsAliasDB and with its hash;
//  3. the Replaces, each a Delete then an Add (its row is already wrong, so
//     its gap is accepted);
//  4. the Deletes.
//
// Steps 3 and 4 run only when every Add of the alias is confirmed, and the
// Deletes only when every Replace succeeded: otherwise the alias's current
// rows stay (two generations briefly coexisting beats none) and one warning
// says the next sync converges. The Replaces and Deletes are checked against
// the re-list: an id listed under a row that is not IsAliasDB is never
// deleted (one warning), an id not listed is already gone.
//
// A plan entry naming anything outside its alias's rows (aliasPlanFault) is
// refused with no HTTP call and is part of err; so is a transport error or an
// ended context, which stops the run at once like ApplyDB (stopApply), and
// ErrDBModelsOff (every alias would fail the same). added counts confirmed
// Adds and re-added Replaces, deleted the rows deleted (a Replace's
// included). Error and warning texts carry no key: the admin client redacts
// the master key and each row's resolved api_key.
func (c *AdminClient) ApplyAliasDB(ctx context.Context, p AliasDBPlan) (added, deleted int, warnings []string, err error) {
	var errs []error
	stop := func(e error) (int, int, []string, error) {
		return added, deleted, warnings, errors.Join(append(errs, e)...)
	}
	kept := func(alias string, failed []error) {
		warnings = append(warnings, fmt.Sprintf("%s: its new database routes were not confirmed (%s); its current routes stay, the next sync converges", alias, oneLine(errors.Join(failed...))))
	}
	fatal := func(e error) bool { return stopApply(ctx, e) || errors.Is(e, ErrDBModelsOff) }
	for _, a := range p.Aliases {
		if a.empty() {
			continue
		}
		if e := aliasPlanFault(a); e != nil {
			errs = append(errs, e)
			continue
		}
		var failed []error
		for _, m := range a.Add {
			if e := c.addConverged(ctx, m, IsAliasDB); e != nil {
				e = fmt.Errorf("add %s: %w", m.ID, e)
				if fatal(e) {
					return stop(fmt.Errorf("litellm: %s: %w", a.Alias, e))
				}
				failed = append(failed, e)
			}
		}
		if len(failed) > 0 {
			kept(a.Alias, failed)
			continue
		}
		rows, e := c.List(ctx)
		if e != nil {
			if fatal(e) {
				return stop(fmt.Errorf("litellm: %s: re-list: %w", a.Alias, e))
			}
			kept(a.Alias, []error{fmt.Errorf("re-list: %w", e)})
			continue
		}
		listed := make(map[string]DBModel, len(rows))
		for _, r := range rows {
			listed[r.ID] = r
		}
		for _, m := range a.Add {
			if r, ok := listed[m.ID]; !ok || !IsAliasDB(r) || !hashMatches(r, m, c.Key) {
				failed = append(failed, fmt.Errorf("%s was accepted but is not listed", m.ID))
			}
		}
		if len(failed) > 0 {
			kept(a.Alias, failed)
			continue
		}
		added += len(a.Add)
		for _, m := range a.Replace {
			if r, ok := listed[m.ID]; ok {
				if !IsAliasDB(r) {
					failed = append(failed, fmt.Errorf("%s is listed without %s; left alone", m.ID, AliasManagedKey))
					continue
				}
				if e := c.Delete(ctx, m.ID); e != nil {
					e = fmt.Errorf("replace %s: delete: %w", m.ID, e)
					if fatal(e) {
						return stop(fmt.Errorf("litellm: %s: %w", a.Alias, e))
					}
					failed = append(failed, e)
					continue
				}
				deleted++
			}
			if e := c.addConverged(ctx, m, IsAliasDB); e != nil {
				e = fmt.Errorf("replace %s: deleted but not re-added: %w", m.ID, e)
				if fatal(e) {
					return stop(fmt.Errorf("litellm: %s: %w", a.Alias, e))
				}
				failed = append(failed, e)
				continue
			}
			added++
		}
		if len(failed) > 0 {
			kept(a.Alias, failed)
			continue
		}
		var delFailed []error
		for _, id := range a.Delete {
			r, ok := listed[id]
			if !ok {
				continue // already gone
			}
			if !IsAliasDB(r) {
				delFailed = append(delFailed, fmt.Errorf("%s is listed without %s; left alone", id, AliasManagedKey))
				continue
			}
			if e := c.Delete(ctx, id); e != nil {
				e = fmt.Errorf("delete %s: %w", id, e)
				if fatal(e) {
					return stop(fmt.Errorf("litellm: %s: %w", a.Alias, e))
				}
				delFailed = append(delFailed, e)
				continue
			}
			deleted++
		}
		if len(delFailed) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: an old database route was not removed (%s); it may still be live until the next sync", a.Alias, oneLine(errors.Join(delFailed...))))
		}
	}
	return added, deleted, warnings, errors.Join(errs...)
}
