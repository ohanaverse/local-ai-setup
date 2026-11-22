package litellm

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// dbRoutes is the database half of one route write (Options.Admin set). The
// plan callback lists the database before planning, hands the local rows of
// its plan to route, and returns to applyPlanned only what config.yaml must
// still do. The caller folds the result into the Result after the write.
//
// Every call runs inside applyPlanned's config.yaml lock: the plan is decided
// against the document as read there, and a fallback must join that same
// write (one save, one restart). The API calls take no lock of their own.
type dbRoutes struct {
	admin *AdminClient
	ctx   context.Context
	// current is the database as listed before planning; valid only when ok.
	current []DBModel
	// ok is true once the listing succeeded and stays true unless the local
	// batch fell back to config.yaml.
	ok bool
	// reason says why the database path was given up ("" when it was never
	// tried, e.g. a change with no local row).
	reason string
	// routed holds the local adds the API carried, in plan order.
	routed []dbRouted
	// unrouted holds the route names whose database rows were deleted.
	unrouted []string
	// warnings are kept only when the API path held (a fallback writes the
	// skipped rows the file way, so they owe no warning).
	warnings []string
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
	ctx := o.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return &dbRoutes{admin: o.Admin, ctx: ctx}
}

// list reads the database; a failure gives up the database path.
func (d *dbRoutes) list() bool {
	cur, err := d.admin.List(d.ctx)
	if err != nil {
		d.fail(err)
		return false
	}
	d.current, d.ok = cur, true
	return true
}

func (d *dbRoutes) fail(err error) {
	d.ok = false
	// One line: errors.Join (ApplyDB) separates its parts with newlines.
	d.reason = strings.Join(strings.Fields(err.Error()), " ")
	d.routed, d.unrouted, d.warnings = nil, nil, nil
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

// route carries the local adds of a plan out through the API and deletes the
// database rows of removeNames, and returns the adds config.yaml must still
// make. Removals always stay with config.yaml as well (a route still in the
// file goes from there; one that is not costs nothing), so only the adds are
// split.
//
// An add stays with config.yaml when its row is not local (RowFamily ""), was
// not built, or replaces a wt row config.yaml still holds (the move of such
// rows is the migration's job). A local add whose id has a hand-written
// config.yaml row is skipped with a warning: a database row beside it would
// make LiteLLM load-balance the two (Review Focus 2).
//
// Any API failure — database models off, a connection error, an add LiteLLM
// accepted but does not list afterwards (Review Focus 5), any other error —
// gives up the database path for the whole batch: add is returned unchanged
// for config.yaml and d.reason says why.
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
	for _, m := range d.owned() {
		byName[m.ModelName] = append(byName[m.ModelName], m.ID)
	}
	var del, unrouted []string
	gone := map[string]bool{}
	for _, n := range removeNames {
		if ids := byName[n]; len(ids) > 0 && !gone[n] {
			gone[n] = true
			del = append(del, ids...)
			unrouted = append(unrouted, n)
		}
	}
	p := PlanDB(desired, d.current)
	slices.Sort(del)
	p.Delete = del
	if !p.Empty() {
		if _, _, _, err := d.admin.ApplyDB(d.ctx, p); err != nil {
			d.fail(err)
			return add
		}
		if err := d.confirm(append(slices.Clone(p.Add), p.Update...)); err != nil {
			d.fail(err)
			return add
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
	d.unrouted = unrouted
	d.warnings = warnings
	return fileAdd
}

// confirm re-lists the database and requires every row just written to be
// there, wt-managed and carrying its hash: a 200 from /model/new is not proof
// the row was stored.
func (d *dbRoutes) confirm(written []DBModel) error {
	if len(written) == 0 {
		return nil
	}
	rows, err := d.admin.List(d.ctx)
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
// fallback warning, the other warnings, and the outcomes. sync selects Sync's
// reporting: only rows actually written are outcomes, a replaced one is
// "rewritten"; ApplyChange reports every add as "routed" with Written saying
// whether it changed. A database removal whose id the file removal already
// reported is not reported twice.
func (d *dbRoutes) finish(res *Result, sync bool) {
	res.Via = "file"
	if d == nil {
		return
	}
	if !d.ok {
		if d.reason != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("database routes unavailable (%s); used config.yaml", d.reason))
		}
		return
	}
	res.Via = "db"
	res.Warnings = append(res.Warnings, d.warnings...)
	for _, id := range d.unrouted {
		if !slices.ContainsFunc(res.Outcomes, func(o Outcome) bool { return o.ID == id && o.Action == "unrouted" }) {
			res.Outcomes = append(res.Outcomes, Outcome{ID: id, Action: "unrouted"})
		}
	}
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
