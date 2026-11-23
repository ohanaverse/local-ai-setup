package litellm

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
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
	// gated is set when this write skipped the database half because
	// config.yaml does not turn database models on (gate). enabling says this
	// write adds store_model_in_db itself; off is the warning when it will not.
	gated    bool
	enabling bool
	off      string
	// cancelled is set when Sync held its config.yaml write because its
	// context was cancelled (finish says so instead of the fallback line).
	cancelled bool
	// migrated holds the local ids whose config.yaml rows this sync moved to
	// the database: added (or found) there and confirmed, so the caller
	// removes them from the file in the same write.
	migrated []string
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
	base := o.DBCtx
	if base == nil {
		base = o.Ctx
	}
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

// gate reports whether this write may use the database at all: only when
// config.yaml's general_settings.store_model_in_db is true. Otherwise the
// running proxy refuses /model/new, so the write takes the file path without
// calling the API — the path Options.Admin nil takes — and finish says why:
// either this very write adds the setting (enabling; the restart it costs
// loads it, and the next sync moves the local rows), or the setting is off
// by hand or cannot be written (off).
func (d *dbRoutes) gate(f *File) bool {
	if storeModelInDB(f.root()) == storeOn {
		return true
	}
	d.gated = true
	d.off = f.dbRoutesOffWarning()
	d.enabling = d.off == ""
	return false
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

// ownedAliases is the database rows the alias path plans against (IsAliasDB);
// owned never holds one, so the model-row path never deletes an alias row.
func (d *dbRoutes) ownedAliases() []DBModel {
	var out []DBModel
	for _, m := range d.current {
		if IsAliasDB(m) {
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
// Focus 5), any other error — hands the add batch back to config.yaml, except
// the routes the database already serves, current (fallBack), and d.reason
// says why.
func (d *dbRoutes) route(cfg *config.Config, f *File, add []plannedAdd, removeNames []string, fresh map[string]*yaml.Node, migrate bool) []plannedAdd {
	var fileAdd []plannedAdd
	var desired []DBModel
	var warnings []string
	// migrating holds the ids whose wt rows leave config.yaml in this write
	// (migrate: Sync only). For them the database copy wins: a twin already
	// there (an interrupted migration) is kept when its hash matches, replaced
	// when it does not, and never deleted as a twin.
	migrating := map[string]bool{}
	pending := map[string]plannedAdd{} // adds whose id has a wt row in config.yaml
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
			if !migrate {
				fileAdd = append(fileAdd, a)
				continue
			}
			pending[a.id] = a // decided below, with the unchanged rows
			continue
		}
		ensureRowParams(a.row)
		m, err := DBModelFromRow(a.row)
		if err != nil {
			fileAdd = append(fileAdd, a)
			continue
		}
		desired = append(desired, m)
	}
	if migrate {
		ids := make([]string, 0, len(fresh))
		for id := range fresh {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		stay := func(id string) {
			if a, ok := pending[id]; ok {
				fileAdd = append(fileAdd, a)
			}
		}
		var orphans []string
		for id := range pending {
			if fresh[id] == nil {
				orphans = append(orphans, id)
			}
		}
		slices.Sort(orphans)
		for _, id := range orphans {
			stay(id) // no fresh row to move: the planner owes one, keep the file path
		}
		for _, id := range ids {
			// Only a local id whose config.yaml rows are all wt's moves: a
			// hand-written row keeps the id in the file (warned above for an
			// add; an unchanged hand-written row is left as it is).
			if RowFamily(cfg, id) == "" || f.row(id) == nil || f.hasUnmarkedRow(id) {
				stay(id)
				continue
			}
			row := cloneNode(fresh[id])
			ensureRowParams(row)
			// A param the user added to the file row has no place in the
			// database row (a later sync rebuilds it from the registry), so
			// such a row stays in config.yaml rather than lose it.
			if extra := userParams(f.row(id), row); len(extra) > 0 {
				warnings = append(warnings, fmt.Sprintf("%s: its config.yaml row has params wt does not set (%s), so it stays there; remove them to move the route to the database", id, strings.Join(extra, ", ")))
				stay(id)
				continue
			}
			m, err := DBModelFromRow(row)
			if err != nil {
				stay(id)
				continue
			}
			migrating[id] = true
			desired = append(desired, m)
		}
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
		if !migrating[r.ID] {
			served[r.ID] = true
		}
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

	p := PlanDB(desired, d.current, d.admin.Key)
	p.Delete = nil
	if len(p.Add)+len(p.Update) > 0 {
		failed := transport
		if failed == nil {
			if _, _, _, err := d.admin.ApplyDB(d.context(), p); err != nil {
				failed = err
				if stopApply(d.context(), err) {
					transport = err
				}
			}
		}
		if failed == nil {
			failed = d.confirm(append(slices.Clone(p.Add), p.Update...))
		}
		if failed != nil {
			d.dropMigratingTwins(migrating, byName, p, &transport)
			return d.fallBack(failed, add, currentIDs(desired, migrating, p))
		}
	}
	// Every row being moved is now in the database, confirmed: written ones by
	// the listing confirm just made, unchanged ones by the listing this write
	// planned against (PlanDB found them managed, with the current hash).
	for _, m := range desired {
		if migrating[m.ModelName] {
			d.migrated = append(d.migrated, m.ModelName)
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
		if migrating[m.ModelName] {
			continue // reported by finish as moved
		}
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

// dropMigratingTwins runs when a migration's database half failed: the rows
// being moved stay in config.yaml, which then wins again, so any database row
// of theirs — one an interrupted migration left, or one this write added or
// replaced before the failure — is deleted, lest LiteLLM load-balance it with
// the file's row. Best effort: a delete that fails is one warning per id, and
// after a transport error none is attempted (deleteAll reports it per id).
// The next sync migrates again.
func (d *dbRoutes) dropMigratingTwins(migrating map[string]bool, byName map[string][]string, p DBPlan, transport *error) {
	if len(migrating) == 0 {
		return
	}
	touched := map[string]bool{}
	for _, m := range append(slices.Clone(p.Add), p.Update...) {
		touched[m.ModelName] = true
	}
	names := make([]string, 0, len(migrating))
	for n := range migrating {
		if touched[n] || len(byName[n]) > 0 {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for _, n := range names {
		ids := slices.Clone(byName[n])
		if !slices.Contains(ids, DBID(n)) {
			ids = append(ids, DBID(n))
		}
		if err := d.deleteAll(ids, transport); err != nil {
			d.delWarnings = append(d.delWarnings, fmt.Sprintf("%s: config.yaml routes it, but its database route could not be removed (%s); both may be live", n, oneLine(err)))
		}
	}
}

// fallBack hands the add batch back to config.yaml, minus the routes the
// database already serves, current (current): PlanDB wrote nothing for them,
// so they are not part of what failed, and a file row beside one would make
// LiteLLM load-balance the two (and cost a restart on every such sync).
func (d *dbRoutes) fallBack(err error, add []plannedAdd, current map[string]bool) []plannedAdd {
	d.fellBack = true
	d.reason = oneLine(err)
	d.addsToFile = true
	return slices.DeleteFunc(slices.Clone(add), func(a plannedAdd) bool { return current[a.id] })
}

// currentIDs is the route names of the desired database rows PlanDB left
// alone (neither added nor replaced) that are not being moved from
// config.yaml: the database holds each, wt-managed and with its hash.
func currentIDs(desired []DBModel, migrating map[string]bool, p DBPlan) map[string]bool {
	written := map[string]bool{}
	for _, m := range append(slices.Clone(p.Add), p.Update...) {
		written[m.ModelName] = true
	}
	out := map[string]bool{}
	for _, m := range desired {
		if !migrating[m.ModelName] && !written[m.ModelName] {
			out[m.ModelName] = true
		}
	}
	return out
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
		if !slices.ContainsFunc(rows, func(r DBModel) bool { return r.ID == w.ID && isManagedDB(r) && hashMatches(r, w, d.admin.Key) }) {
			return fmt.Errorf("database model %s was accepted but is not listed", w.ID)
		}
	}
	return nil
}

// readOnlyDB lists the database for a plan that writes nothing (PlanSync,
// DesiredIDs): nil and no rows without Options.Admin; no API call at all
// when config.yaml does not turn database models on (gate). The caller
// closes it (nil-safe). A failed listing leaves no rows and d.reason set.
func readOnlyDB(o Options, f *File) (*dbRoutes, []RowInfo) {
	d := newDBRoutes(o)
	if d == nil || !d.gate(f) || !d.list() {
		return d, nil
	}
	return d, d.routeRows()
}

// preview turns a plan made with the database's rows (planSyncWithDB) into
// what the real Sync would do with them, writing nothing: route's decisions
// replayed without its API calls. It returns the ids of the adds the
// database would carry (they never reach config.yaml), and on the plan:
//   - a local add the database already holds, current, leaves Add (the real
//     sync writes nothing for it); one whose stored hash differs is a
//     Rewrite;
//   - a local add with a hand-written config.yaml row leaves Add with
//     route's warning (no database route is added beside it);
//   - the migration: each local id whose config.yaml rows are all wt's and
//     carry no param wt does not set goes to Move (and leaves Add); one
//     with such params gets route's warning and stays;
//   - EnableDB when this sync would write store_model_in_db itself (gate),
//     or one warning when the listing failed.
//
// nil-safe: without Options.Admin it changes nothing.
func (d *dbRoutes) preview(cfg *config.Config, f *File, plan *SyncPlan, built map[string]*yaml.Node) map[string]bool {
	if d == nil {
		return nil
	}
	if d.gated {
		plan.EnableDB = d.enabling
		return nil
	}
	if !d.listed {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("database routes unavailable (%s); this plan covers config.yaml only", d.reason))
		return nil
	}
	viaDB := map[string]bool{}
	drop := map[string]bool{}
	for _, id := range plan.Add {
		row := built[id]
		if row == nil || RowFamily(cfg, id) == "" || f.row(id) != nil && !f.hasUnmarkedRow(id) {
			continue // the file's: a cloud row, or a wt row the migration decides
		}
		if f.hasUnmarkedRow(id) {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: config.yaml has a hand-written row for this id; no database route was added beside it", id))
			drop[id] = true
			continue
		}
		r := cloneNode(row)
		ensureRowParams(r)
		m, err := DBModelFromRow(r)
		if err != nil {
			continue
		}
		viaDB[id] = true
		p := PlanDB([]DBModel{m}, d.current, d.admin.Key)
		switch {
		case len(p.Add) > 0:
		case len(p.Update) > 0:
			if !slices.Contains(plan.Rewrite, id) {
				plan.Rewrite = append(plan.Rewrite, id)
			}
		default:
			drop[id] = true
		}
	}
	ids := make([]string, 0, len(plan.fresh))
	for id := range plan.fresh {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if RowFamily(cfg, id) == "" || f.row(id) == nil || f.hasUnmarkedRow(id) {
			continue
		}
		row := cloneNode(plan.fresh[id])
		ensureRowParams(row)
		if extra := userParams(f.row(id), row); len(extra) > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: its config.yaml row has params wt does not set (%s), so it stays there; remove them to move the route to the database", id, strings.Join(extra, ", ")))
			continue
		}
		if _, err := DBModelFromRow(row); err != nil {
			continue
		}
		plan.Move = append(plan.Move, id)
		drop[id] = true
	}
	gone := func(id string) bool { return drop[id] }
	plan.Add = slices.DeleteFunc(plan.Add, gone)
	plan.Adopt = slices.DeleteFunc(plan.Adopt, gone)
	plan.Rewrite = slices.DeleteFunc(plan.Rewrite, gone)
	return viaDB
}

// finish folds the database half into res after the file write: Via, the
// warnings, and the outcomes. sync selects Sync's reporting: a failed
// listing is always one warning line; only rows
// actually written are outcomes, a replaced one is "rewritten"; ApplyChange
// reports every add as "routed" with Written saying whether it changed. A
// database removal whose id the file removal already reported is not
// reported twice; a failed one is an "unrouted" Outcome with Err.
func (d *dbRoutes) finish(res *Result, sync bool) {
	res.Via = "file"
	if d == nil {
		return
	}
	if d.gated {
		// The database half was not tried. A write that saved the setting has
		// enabled it (its restart loads it); the rows move on the next sync.
		if d.enabling && res.Changed {
			res.Warnings = append(res.Warnings, "database routes enabled; local routes move to the database on the next sync")
		}
		// The opt-out is said where routes are reviewed (Sync here, PlanSync
		// itself), never on a start or stop job: it would repeat on every one.
		if d.off != "" && sync {
			res.Warnings = append(res.Warnings, d.off)
		}
		return
	}
	if len(d.migrated) > 0 {
		// A moved route is not unrouted: its file row went because the
		// database now serves it. Report it once, as a rewritten route.
		res.Outcomes = slices.DeleteFunc(res.Outcomes, func(o Outcome) bool {
			return o.Action == "unrouted" && slices.Contains(d.migrated, o.ID)
		})
	}
	switch {
	case d.cancelled:
		res.Warnings = append(res.Warnings, "sync cancelled; config.yaml was not written")
	case d.reason != "":
		if d.addsToFile {
			res.Warnings = append(res.Warnings, fmt.Sprintf("database routes unavailable (%s); used config.yaml", d.reason))
		}
		if len(d.unchecked) > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("database routes unavailable (%s); not checked for %s, whose database route may still be live", d.reason, strings.Join(d.unchecked, ", ")))
		}
		// Sync always looks, so a listing it could not make is said even when
		// nothing local was pending — once, and only when no line above says it.
		if sync && !d.listed && !d.addsToFile && len(d.unchecked) == 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("database routes unavailable (%s); database routes not checked", d.reason))
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
	for _, id := range d.migrated {
		res.Outcomes = append(res.Outcomes, Outcome{ID: id, Action: "rewritten", Written: true})
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
