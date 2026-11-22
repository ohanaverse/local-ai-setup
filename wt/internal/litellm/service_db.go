package litellm

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// dbTimeout bounds the whole database half of one route write (the listing,
// the writes and the confirming listing together): it runs under the
// config.yaml lock, which lifecycle writers wait for only briefly.
const dbTimeout = 15 * time.Second

// dbRoutes is the database half of one route write (Options.Admin set). The
// plan callback lists the database before planning, hands the local rows of
// its plan to route, and returns to applyPlanned only what config.yaml must
// still do. The caller folds the result into the Result after the write.
//
// Every call runs inside applyPlanned's config.yaml lock: the plan is decided
// against the document as read there, and a fallback must join that same
// write (one save, one restart). The API calls take no lock of their own and
// share one deadline (dbTimeout, from Options.Ctx when set), so a hung proxy
// costs the lock holder that long at most and then falls back to the file.
type dbRoutes struct {
	admin   *AdminClient
	base    context.Context
	timeout time.Duration
	ctx     context.Context // set by the first call; bounded by timeout
	cancel  context.CancelFunc
	// current is the database as listed before planning; valid only when
	// listed.
	current []DBModel
	listed  bool
	// fellBack is set when the local adds were handed back to config.yaml.
	fellBack bool
	// reason says why the database path was given up ("" when it held or was
	// never tried, e.g. a change with no local row).
	reason string
	// addsToFile / unchecked say what a lost database path cost: local adds
	// written to config.yaml instead, and route names whose database rows
	// could not be looked at (a failed listing).
	addsToFile bool
	unchecked  []string
	// routed holds the local adds the API carried, in plan order.
	routed []dbRouted
	// unrouted holds the route names whose database rows were deleted.
	unrouted []string
	// failed holds the removals whose database delete failed: the route may
	// still be live (reported as an Outcome with Err and a warning).
	failed []Outcome
	// warnings hold for the adds only while the API path held (a fallback
	// writes skipped rows the file way, so they owe no warning).
	warnings []string
	// delWarnings are about deletes and hold whatever the adds did.
	delWarnings []string
}

type dbRouted struct {
	id      string
	written bool // the row was added or replaced
	updated bool // it was replaced (a changed wt row)
}

func newDBRoutes(o Options) *dbRoutes {
	if o.Admin == nil {
		return nil
	}
	base := o.Ctx
	if base == nil {
		base = context.Background()
	}
	t := o.dbTimeout
	if t <= 0 {
		t = dbTimeout
	}
	return &dbRoutes{admin: o.Admin, base: base, timeout: t}
}

// context is the one deadline every API call of this write shares, started
// by the first call (inside the lock, so the lock wait does not spend it).
func (d *dbRoutes) context() context.Context {
	if d.ctx == nil {
		d.ctx, d.cancel = context.WithTimeout(d.base, d.timeout)
	}
	return d.ctx
}

// close releases the deadline; nil-safe.
func (d *dbRoutes) close() {
	if d != nil && d.cancel != nil {
		d.cancel()
	}
}

// list reads the database; a failure gives up the database path (the caller
// then reports what it cost through unlisted).
func (d *dbRoutes) list() bool {
	cur, err := d.admin.List(d.context())
	if err != nil {
		d.reason = oneLine(err)
		return false
	}
	d.current, d.listed = cur, true
	return true
}

// unlisted records what a failed listing cost this write: whether local adds
// go to config.yaml, and which local removals' database rows went unchecked.
func (d *dbRoutes) unlisted(cfg *config.Config, add []plannedAdd, removeNames []string) {
	d.addsToFile = slices.ContainsFunc(add, func(a plannedAdd) bool { return a.err == nil && RowFamily(cfg, a.id) != "" })
	for _, n := range removeNames {
		if RowFamily(cfg, n) != "" && !slices.Contains(d.unchecked, n) {
			d.unchecked = append(d.unchecked, n)
		}
	}
}

func oneLine(err error) string {
	// errors.Join (ApplyDB) separates its parts with newlines.
	return strings.Join(strings.Fields(err.Error()), " ")
}

// owned is the database rows wt may plan against: marked and under a wt: id.
func (d *dbRoutes) owned() []DBModel {
	var out []DBModel
	for _, m := range d.current {
		if isManagedDB(m) && strings.HasPrefix(m.ID, DBIDPrefix) {
			out = append(out, m)
		}
	}
	return out
}

// routeRows is owned as config.yaml-shaped rows (one per route name), for
// planSync's ownership and removal loops.
func (d *dbRoutes) routeRows() []RowInfo {
	var out []RowInfo
	seen := map[string]bool{}
	for _, m := range d.owned() {
		if !seen[m.ModelName] {
			seen[m.ModelName] = true
			out = append(out, RowInfo{ID: m.ModelName, Managed: true})
		}
	}
	return out
}

// foreignWarning is the one line naming marked rows under a foreign id, which
// wt leaves alone (PlanDB skips them; ApplyDB would refuse them), or "".
func (d *dbRoutes) foreignWarning() string {
	var ids []string
	for _, m := range d.current {
		if isManagedDB(m) && !strings.HasPrefix(m.ID, DBIDPrefix) {
			ids = append(ids, m.ID)
		}
	}
	switch len(ids) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("database route %s is marked wt_managed but is not a %s id; left alone", ids[0], DBIDPrefix)
	}
	slices.Sort(ids)
	return fmt.Sprintf("database routes %s are marked wt_managed but are not %s ids; left alone", strings.Join(ids, ", "), DBIDPrefix)
}

// familyRoutes is the route names of the owned database rows of local family
// fam, minus the ids being added: ApplyChange's RemoveFamilies on the database.
func (d *dbRoutes) familyRoutes(cfg *config.Config, fam string, adding map[string]bool) []string {
	var out []string
	for _, r := range d.routeRows() {
		if !adding[r.ID] && RowFamily(cfg, r.ID) == fam {
			out = append(out, r.ID)
		}
	}
	return out
}

// route carries the local half of a plan out through the API and returns the
// adds config.yaml must still make. Removals always stay with config.yaml as
// well (a route still in the file goes from there; one that is not costs
// nothing), so only the adds are split.
//
// An add stays with config.yaml when its row is not local (RowFamily ""), was
// not built, or replaces a wt row config.yaml still holds (the move of such
// rows is the migration's job). A local add whose id has a hand-written
// config.yaml row is skipped with a warning: a database row beside it would
// make LiteLLM load-balance the two (Review Focus 2).
//
// config.yaml wins over the database: an owned database row whose route name
// config.yaml serves after this write (a row already in the file, or an add
// the file makes) is deleted, so a fallback or a pre-migration row never
// leaves a stale twin LiteLLM would load-balance with the current row.
//
// Deletes run first, one id at a time through ApplyDB's guarded path, so each
// failure is pinned to its route: a removal whose delete failed is an
// "unrouted" Outcome with Err and a warning that the route may still be live
// (config.yaml cannot remove a database row, so it is never "used
// config.yaml"). A transport error stops the deletes; the rest are reported
// failed with it.
//
// The adds and replacements then go in one ApplyDB call and are confirmed by
// a fresh listing. Any failure there — database models off, a transport error
// (from the deletes too), an add LiteLLM accepted but does not list (Review
// Focus 5), any other error — hands the whole add batch back to config.yaml
// (add is returned unchanged) and d.reason says why.
func (d *dbRoutes) route(cfg *config.Config, f *File, add []plannedAdd, removeNames []string) []plannedAdd {
	var fileAdd []plannedAdd
	var desired []DBModel
	var warnings []string
	for _, a := range add {
		if a.err != nil || a.row == nil || RowFamily(cfg, a.id) == "" {
			fileAdd = append(fileAdd, a)
			continue
		}
		if f.hasUnmarkedRow(a.id) {
			warnings = append(warnings, fmt.Sprintf("%s: config.yaml has a hand-written row for this id; no database route was added beside it", a.id))
			continue
		}
		if f.row(a.id) != nil {
			fileAdd = append(fileAdd, a)
			continue
		}
		m, err := DBModelFromRow(a.row)
		if err != nil {
			fileAdd = append(fileAdd, a)
			continue
		}
		desired = append(desired, m)
	}
	byName := map[string][]string{}
	var names []string
	for _, m := range d.owned() {
		if byName[m.ModelName] == nil {
			names = append(names, m.ModelName)
		}
		byName[m.ModelName] = append(byName[m.ModelName], m.ID)
	}

	// Removals, then twins of what config.yaml serves.
	removing := map[string]bool{}
	var transport error
	for _, n := range removeNames {
		if len(byName[n]) == 0 || removing[n] {
			continue
		}
		removing[n] = true
		if err := d.deleteAll(byName[n], &transport); err != nil {
			d.failed = append(d.failed, Outcome{ID: n, Action: "unrouted", Err: err})
			d.delWarnings = append(d.delWarnings, fmt.Sprintf("%s: its database route was NOT removed and may still be live (%s)", n, oneLine(err)))
			continue
		}
		d.unrouted = append(d.unrouted, n)
	}
	served := map[string]bool{}
	for _, r := range f.Rows() {
		served[r.ID] = true
	}
	for _, a := range fileAdd {
		if a.err == nil && a.row != nil {
			served[a.id] = true
		}
	}
	for _, n := range names {
		if !served[n] || removing[n] {
			continue
		}
		if err := d.deleteAll(byName[n], &transport); err != nil {
			d.delWarnings = append(d.delWarnings, fmt.Sprintf("%s: config.yaml routes it, but its database route could not be removed (%s); both may be live", n, oneLine(err)))
		}
	}

	p := PlanDB(desired, d.current)
	p.Delete = nil
	if len(p.Add)+len(p.Update) > 0 {
		if transport != nil {
			return d.fallBack(transport, add)
		}
		if _, _, _, err := d.admin.ApplyDB(d.context(), p); err != nil {
			return d.fallBack(err, add)
		}
		if err := d.confirm(append(slices.Clone(p.Add), p.Update...)); err != nil {
			return d.fallBack(err, add)
		}
	}
	added, updated := map[string]bool{}, map[string]bool{}
	for _, m := range p.Add {
		added[m.ModelName] = true
	}
	for _, m := range p.Update {
		updated[m.ModelName] = true
	}
	for _, m := range desired {
		d.routed = append(d.routed, dbRouted{id: m.ModelName, written: added[m.ModelName] || updated[m.ModelName], updated: updated[m.ModelName]})
	}
	d.warnings = warnings
	return fileAdd
}

// deleteAll deletes ids through ApplyDB's guarded path, one call per id. Once
// a transport error has been seen (*transport), nothing more is attempted and
// that error is returned for every later id.
func (d *dbRoutes) deleteAll(ids []string, transport *error) error {
	for _, id := range ids {
		if *transport != nil {
			return *transport
		}
		if _, _, _, err := d.admin.ApplyDB(d.context(), DBPlan{Delete: []string{id}}); err != nil {
			if stopApply(d.context(), err) {
				*transport = err
			}
			return err
		}
	}
	return nil
}

// fallBack hands the whole add batch back to config.yaml.
func (d *dbRoutes) fallBack(err error, add []plannedAdd) []plannedAdd {
	d.fellBack = true
	d.reason = oneLine(err)
	d.addsToFile = true
	return add
}

// confirm re-lists the database and requires every row just written to be
// there, wt-managed and carrying its hash: a 200 from /model/new is not proof
// the row was stored.
func (d *dbRoutes) confirm(written []DBModel) error {
	rows, err := d.admin.List(d.context())
	if err != nil {
		return fmt.Errorf("re-list after the write: %w", err)
	}
	for _, w := range written {
		if !slices.ContainsFunc(rows, func(r DBModel) bool { return r.ID == w.ID && isManagedDB(r) && hashMatches(r, w) }) {
			return fmt.Errorf("database model %s was accepted but is not listed", w.ID)
		}
	}
	return nil
}

// finish folds the database half into res after the file write: Via, the
// warnings, and the outcomes. sync selects Sync's reporting: only rows
// actually written are outcomes, a replaced one is "rewritten"; ApplyChange
// reports every add as "routed" with Written saying whether it changed. A
// database removal whose id the file removal already reported is not
// reported twice; a failed one is an "unrouted" Outcome with Err.
func (d *dbRoutes) finish(res *Result, sync bool) {
	res.Via = "file"
	if d == nil {
		return
	}
	if d.reason != "" {
		if d.addsToFile {
			res.Warnings = append(res.Warnings, fmt.Sprintf("database routes unavailable (%s); used config.yaml", d.reason))
		}
		if len(d.unchecked) > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("database routes unavailable (%s); not checked for %s, whose database route may still be live", d.reason, strings.Join(d.unchecked, ", ")))
		}
	}
	res.Warnings = append(res.Warnings, d.delWarnings...)
	for _, id := range d.unrouted {
		if !slices.ContainsFunc(res.Outcomes, func(o Outcome) bool { return o.ID == id && o.Action == "unrouted" }) {
			res.Outcomes = append(res.Outcomes, Outcome{ID: id, Action: "unrouted"})
		}
	}
	res.Outcomes = append(res.Outcomes, d.failed...)
	if !d.listed || d.fellBack {
		return
	}
	res.Via = "db"
	res.Warnings = append(res.Warnings, d.warnings...)
	for _, r := range d.routed {
		switch {
		case !sync:
			res.Outcomes = append(res.Outcomes, Outcome{ID: r.id, Action: "routed", Written: r.written})
		case r.updated:
			res.Outcomes = append(res.Outcomes, Outcome{ID: r.id, Action: "rewritten", Written: true})
		case r.written:
			res.Outcomes = append(res.Outcomes, Outcome{ID: r.id, Action: "routed", Written: true})
		}
	}
}
