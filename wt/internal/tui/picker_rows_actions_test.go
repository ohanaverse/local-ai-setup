package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// The launch selector offers the same row actions the manage list has
// (cleanup_rowaction_test.go pins what each action does once reached; these
// pin that the picker reaches them at all): the RemoveFuncs the picker is
// started with are the instance its keys use, enter still launches and never
// starts a removal, the filter box wins every printable rune while it is
// open, and a fill's instance swap waits for the list's return to rest.

// actionRecorder records what reaches one RemoveFuncs stub. Every instance
// the builder returns gets its own, so a test can tell which instance the
// keys reached (the plumbing identity these tests pin). snap is the snapshot
// the instance was built from — which generation's engine stands behind it.
type actionRecorder struct {
	snap      cleanup.Snapshot
	previewed []string
	rescued   []string
	restored  int
}

// funcs is the recorder's RemoveFuncs: the previews are recorded and refused
// (nil previews are not runnable), the hand-offs recorded and done.
func (r *actionRecorder) funcs() RemoveFuncs {
	return RemoveFuncs{
		Preview:     previewRecorder(&r.previewed),
		Rescue:      func(row cleanup.Row) error { r.rescued = append(r.rescued, cleanup.DisplayName(row)); return nil },
		RestoreRoot: func() error { r.restored++; return nil },
	}
}

// pickerActionsReady is the picker Run builds (newRunModel takes the builder
// as one of Run's arguments), loaded from rep as the picker first paints.
func pickerActionsReady(t *testing.T, rep cleanup.Report, build RemoveFuncsBuilder) model {
	t.Helper()
	m := newRunModel(false, false, "", "", "", "", "", nil, themes.Default, "", &config.Config{}, build)
	m.width, m.height = 100, 40
	m, _ = updateMsg(m, rowsLoadedMsg{report: rep, repoRoot: rep.Repo})
	if !m.ready {
		t.Fatalf("not ready after the rows load; status %q", m.status)
	}
	return m
}

// pickerKey sends one key through the picker and runs the command it returns
// once — the removal preview's stub call, or the quit `r` and `R` end on —
// without feeding its message back (as runPreview does for the list alone).
func pickerKey(t *testing.T, m model, k tea.KeyMsg) model {
	t.Helper()
	next, cmd := updateMsg(m, k)
	if cmd != nil {
		cmd()
	}
	return next
}

// pickerActionsFixture is pickerFixture plus the rows `r` and `R` act on: a
// worktree holding uncommitted work (a rescue is offered for it) and the root
// on a branch of its own (restore root's row). loose reads proven merged —
// the fill-swap tests' row, whose stale-engine story is "was proven merged".
func pickerActionsFixture() cleanup.Report {
	r := pickerFixture()
	r.Rows[0].Name, r.Rows[0].Branch = "drift", "drift"
	r.Rows[0].Head = strings.Repeat("1", 40)
	r.Rows[3].Status = cleanup.StatusProven
	r.Rows = append(r.Rows, cleanup.Row{
		Kind: cleanup.KindWorktree, Name: "wip", Path: "/repo/.worktrees/wip",
		Branch: "wip", Ref: "refs/heads/wip", Status: cleanup.StatusUniqueWork,
		Losses: cleanup.Losses{Dirty: true},
	})
	return r
}

// TestLaunchSelectorPassesRemoveFuncs pins the plumbing the launch
// selector's actions need: the RemoveFuncs the picker is started with (the
// builder Run takes, cmd/wt's over cleanupRemoveFuncs) are the instance its
// key handling uses. `x` reaches that instance's Preview on the highlighted
// row. `r` and `R` reach its Rescue and RestoreRoot through
// finishRowSession, where the shared flow lands once the screen is gone (the
// picker has no session loop to re-enter after a hand-off). The PR fill's
// landing swaps in the instance built for that load — the one whose engine
// re-checks the rows now on screen — and the keys use it from then on.
func TestLaunchSelectorPassesRemoveFuncs(t *testing.T) {
	var built []*actionRecorder
	builder := RemoveFuncsBuilder(func(cleanup.Snapshot, cleanup.GatherInputs) RemoveFuncs {
		rec := &actionRecorder{}
		built = append(built, rec)
		return rec.funcs()
	})
	rep := pickerActionsFixture()

	// `x` reaches the instance the picker was loaded with.
	built = nil
	m := pickerCursorTo(t, pickerActionsReady(t, rep, builder), "loose")
	m = pickerKey(t, m, keyRunes("x"))
	if len(built) != 1 {
		t.Fatalf("the builder ran %d times for one load, want 1", len(built))
	}
	if got := strings.Join(built[0].previewed, ","); got != "loose" {
		t.Errorf("x previewed %q (phase %v), want the highlighted row through the instance the picker received", got, m.rows.phase)
	}

	// The PR fill landing swaps in the instance built for the filled load.
	built = nil
	m = pickerActionsReady(t, rep, builder)
	m, _ = updateMsg(m, rowPRsLoadedMsg{Report: rep, Snap: cleanup.Snapshot{PRsAsked: true}, gen: m.rowsGen})
	m = pickerCursorTo(t, m, "loose")
	m = pickerKey(t, m, keyRunes("x"))
	if len(built) != 2 {
		t.Fatalf("the builder ran %d times for a load and its PR fill, want 2", len(built))
	}
	if len(built[0].previewed) != 0 {
		t.Errorf("x previewed through the pre-fill instance %v", built[0].previewed)
	}
	if got := strings.Join(built[1].previewed, ","); got != "loose" {
		t.Errorf("x previewed %q, want the highlighted row through the filled load's instance", got)
	}

	// `r` hands its row to the same instance's Rescue at the session's end.
	built = nil
	m = pickerCursorTo(t, pickerActionsReady(t, rep, builder), "wip")
	m = pickerKey(t, m, keyRunes("r"))
	if m.rows.rescueRow == nil || cleanup.DisplayName(*m.rows.rescueRow) != "wip" {
		t.Fatalf("r left rescueRow %v, want the wip row held for the hand-off", m.rows.rescueRow)
	}
	if len(built) != 1 || len(built[0].rescued) != 0 {
		t.Fatalf("the rescue ran inside the screen (%v), want it held for finishRowSession", built)
	}
	if err := finishRowSession(m, nil); err != nil {
		t.Fatalf("finishRowSession: %v", err)
	}
	if got := strings.Join(built[0].rescued, ","); got != "wip" {
		t.Errorf("the hand-off rescued %q, want wip through the instance the picker received", got)
	}

	// `R` hands off to the same instance's RestoreRoot the same way.
	built = nil
	m = pickerCursorTo(t, pickerActionsReady(t, rep, builder), "drift")
	m = pickerKey(t, m, keyRunes("R"))
	if !m.rows.restoreRoot {
		t.Fatal("R did not flag the restore-root hand-off")
	}
	if len(built) != 1 || built[0].restored != 0 {
		t.Fatalf("the restore ran inside the screen (%v), want it held for finishRowSession", built)
	}
	if err := finishRowSession(m, nil); err != nil {
		t.Fatalf("finishRowSession: %v", err)
	}
	if built[0].restored != 1 {
		t.Errorf("the hand-off restored %d times, want 1 through the instance the picker received", built[0].restored)
	}
}

// TestLaunchSelectorEnterStillLaunchesRow pins that giving the launch
// selector the row actions changed none of enter's behavior: enter on a row
// still launches it (the selectedEntryMsg the rest of the launch flow takes)
// and starts no removal, rescue or restore — whatever RemoveFuncs the picker
// holds — and enter on "+ New worktree…" still asks for one.
func TestLaunchSelectorEnterStillLaunchesRow(t *testing.T) {
	rec := &actionRecorder{}
	builder := RemoveFuncsBuilder(func(cleanup.Snapshot, cleanup.GatherInputs) RemoveFuncs { return rec.funcs() })
	m := pickerCursorTo(t, pickerActionsReady(t, pickerActionsFixture(), builder), "loose")
	next, sel := pickerEnter(t, m)
	if sel.entry != (worktree.Entry{Type: worktree.TypeBranch, Branch: "loose"}) {
		t.Errorf("enter chose %v, want the loose branch to launch in", sel.entry)
	}
	if len(rec.previewed) != 0 || len(rec.rescued) != 0 || rec.restored != 0 {
		t.Errorf("enter started an action: previewed %v rescued %v restored %d", rec.previewed, rec.rescued, rec.restored)
	}
	if next.rows.phase != cleanupPhaseList || next.rows.conf != nil || next.rows.rescueRow != nil || next.rows.restoreRoot {
		t.Errorf("enter left phase %v (conf %v, rescue %v, restore %v), want the plain list",
			next.rows.phase, next.rows.conf, next.rows.rescueRow, next.rows.restoreRoot)
	}
	// The new-worktree item is its own message, never a removal either.
	m.rows.cursor = 0
	next, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the new-worktree item returned no command")
	}
	if _, ok := cmd().(newWorktreeChosenMsg); !ok {
		t.Error("enter on the new-worktree item did not ask for one")
	}
	if len(rec.previewed) != 0 || next.rows.phase != cleanupPhaseList {
		t.Errorf("enter on the new-worktree item started a removal: %v (phase %v)", rec.previewed, next.rows.phase)
	}
}

// TestLaunchSelectorActionKeysNotCapturedByFilter pins the filter box's
// precedence, both directions: while it is open every printable rune — the
// action keys `x`, `d`, `o`, `R`, `r` included — is query text (it filters
// the rows and starts no action); with the box closed those keys act on the
// highlighted row as the manage list's do, a kept query notwithstanding.
func TestLaunchSelectorActionKeysNotCapturedByFilter(t *testing.T) {
	rec := &actionRecorder{}
	builder := RemoveFuncsBuilder(func(cleanup.Snapshot, cleanup.GatherInputs) RemoveFuncs { return rec.funcs() })
	rep := pickerActionsFixture()

	// The filter open: the action runes are query text. They filter (the
	// query hides every row it matches nothing of) and fire no action.
	m := pickerActionsReady(t, rep, builder)
	m = pickerKey(t, m, keyRunes("/"))
	for _, k := range []string{"x", "d", "o", "R", "r"} {
		m = pickerKey(t, m, keyRunes(k))
	}
	if m.rows.filter != "xdoRr" || !m.rows.filtering {
		t.Errorf("filter %q (typing %v), want the action runes typed into it", m.rows.filter, m.rows.filtering)
	}
	rows, hidden := 0, 0
	for i, it := range m.rows.items {
		if it.heading == "" && !it.newWorktree {
			rows++
			if m.rows.hidden(i) {
				hidden++
			}
		}
	}
	if rows == 0 || hidden != rows {
		t.Errorf("the query hid %d of %d rows, want it to filter every row it matches nothing of", hidden, rows)
	}
	if len(rec.previewed) != 0 || len(rec.rescued) != 0 || rec.restored != 0 || m.rows.rescueRow != nil || m.rows.restoreRoot {
		t.Errorf("an action fired while the filter was open: previewed %v rescued %v restored %d",
			rec.previewed, rec.rescued, rec.restored)
	}
	if m.rows.phase != cleanupPhaseList {
		t.Errorf("phase %v with the filter open, want the list untouched", m.rows.phase)
	}

	// The box closed: `x` acts on the highlighted row.
	m = pickerCursorTo(t, pickerActionsReady(t, rep, builder), "loose")
	m = pickerKey(t, m, keyRunes("x"))
	if got := strings.Join(rec.previewed, ","); got != "loose" {
		t.Errorf("x with the filter closed previewed %q, want the highlighted row", got)
	}
	// `r` and `R` act the same way, each on its own list (each ends it).
	m = pickerCursorTo(t, pickerActionsReady(t, rep, builder), "wip")
	m = pickerKey(t, m, keyRunes("r"))
	if m.rows.rescueRow == nil {
		t.Error("r with the filter closed held no row for rescue")
	}
	m = pickerCursorTo(t, pickerActionsReady(t, rep, builder), "drift")
	m = pickerKey(t, m, keyRunes("R"))
	if !m.rows.restoreRoot {
		t.Error("R with the filter closed flagged no restore-root")
	}

	// A kept query is a closed box: `x` acts on the row the query left
	// visible (typing the query is not an action either, of course).
	rec.previewed = nil
	m = pickerActionsReady(t, rep, builder)
	m = pickerKey(t, m, keyRunes("/"))
	for _, r := range "loose" {
		m = pickerKey(t, m, keyRunes(string(r)))
	}
	m = pickerKey(t, m, enterKey) // keeps the query and closes the box
	m = pickerKey(t, m, keyRunes("x"))
	if got := strings.Join(rec.previewed, ","); got != "loose" {
		t.Errorf("x with a kept query previewed %q, want the one row it left visible", got)
	}
}

// TestLaunchSelectorSessionLogPrintsWhenItQuits pins the results screen's
// promise ("the results print again when you quit") in the picker: the
// session's removal log prints at the session's end (finishRowSession), the
// same lines RunCleanupList prints at its own — whatever the session ended
// on.
func TestLaunchSelectorSessionLogPrintsWhenItQuits(t *testing.T) {
	m := model{rows: cleanupListModel{log: []removal.Outcome{
		removal.NewOutcome(removal.ItemFromRow(cleanup.Row{Kind: cleanup.KindWorktree, Name: "done", Path: "/repo/.worktrees/done"}),
			removal.WtRemoved, removal.BrDeleted, "removed"),
	}}}
	got := captureStdout(t, func() {
		if err := finishRowSession(m, nil); err != nil {
			t.Errorf("finishRowSession: %v", err)
		}
	})
	if want := logText(m.rows.log); !strings.Contains(got, want) {
		t.Errorf("the session printed %q, want it to carry the log %q", got, want)
	}
}

// TestLaunchSelectorFillSwapWaitsForTheList pins the PR fill's instance swap
// under a removal flow: a landing mid-flow is deferred to the list's next
// return to rest — never dropped, never built only to be discarded. The flow
// keeps the funcs its sealed previews came from (the engine must not change
// under what the user is confirming), and the moment the flow is cancelled
// back to the list the filled generation's instance takes over, so the next
// `x` previews through the engine that stands on the rows the screen shows.
// The instance it replaces stands on the load's never-filled snapshot and
// would read the proven row as "status changed since it was listed (looks
// merged (less sure), was proven merged)".
func TestLaunchSelectorFillSwapWaitsForTheList(t *testing.T) {
	var built []*actionRecorder
	builder := RemoveFuncsBuilder(func(snap cleanup.Snapshot, _ cleanup.GatherInputs) RemoveFuncs {
		rec := &actionRecorder{snap: snap}
		built = append(built, rec)
		return rec.funcs()
	})
	rep := pickerActionsFixture()
	// openConfirm is `x` plus its preview answer: the confirm screen a fill
	// lands under (the stub previews record and return nil, so the answer is
	// fed by hand — only Confirmed* is sealed behind genuine previews).
	openConfirm := func(m model) model {
		t.Helper()
		m = pickerKey(t, m, keyRunes("x"))
		row, ok := m.rows.selectedRow()
		if !ok {
			t.Fatal("the highlighted row vanished before its preview")
		}
		m, _ = updateMsg(m, cleanupPreviewMsg{previews: []removal.ItemPreview{{
			Item: removal.ItemFromRow(row), Approval: removal.ApprovalProven}}})
		if m.rows.phase != cleanupPhaseConfirm {
			t.Fatalf("phase %v, want the confirm screen a fill can land under", m.rows.phase)
		}
		return m
	}

	// One fill under the flow: nothing is built while the flow runs (the
	// landing's build waits for rest — no invoke-and-discard), and esc back
	// to the list builds and applies the filled load's instance right there.
	built = nil
	m := openConfirm(pickerCursorTo(t, pickerActionsReady(t, rep, builder), "loose"))
	m, _ = updateMsg(m, rowPRsLoadedMsg{Report: rep, Snap: cleanup.Snapshot{PRsAsked: true}, gen: m.rowsGen})
	if len(built) != 1 {
		t.Fatalf("the builder ran %d times for a load and a mid-flow fill, want 1: a landing under a flow builds nothing it would discard", len(built))
	}
	m = pickerKey(t, m, escKey)
	if m.rows.phase != cleanupPhaseList {
		t.Fatalf("esc left phase %v, want the list", m.rows.phase)
	}
	if len(built) != 2 {
		t.Fatalf("the builder ran %d times by the return to rest, want 2: the deferred fill's instance builds there", len(built))
	}
	m = pickerCursorTo(t, m, "loose")
	m = pickerKey(t, m, keyRunes("x"))
	if got := strings.Join(built[1].previewed, ","); got != "loose" {
		t.Errorf("x previewed %q through %d instances, want the row through the filled load's instance", got, len(built))
	}
	if len(built[0].previewed) != 1 {
		t.Errorf("the pre-fill instance answered the post-cancel x too: %v", built[0].previewed)
	}
	if !built[1].snap.PRsAsked {
		t.Error("the instance in use was not built from the filled snapshot: its engine re-checks the load's never-filled PR facts and reads the proven row as status changed since it was listed")
	}

	// Several fills under one flow: only the newest is ever built — the
	// pending swap is replaced, not queued, and an older landing's builder
	// never runs.
	built = nil
	m = openConfirm(pickerCursorTo(t, pickerActionsReady(t, rep, builder), "loose"))
	for _, drift := range []string{"first fill", "second fill"} {
		m, _ = updateMsg(m, rowPRsLoadedMsg{Report: rep, gen: m.rowsGen,
			Snap: cleanup.Snapshot{PRsAsked: true, RootDrift: drift}})
	}
	if len(built) != 1 {
		t.Fatalf("two mid-flow fills built %d instances, want 1 (the load's): only the newest pending is ever built", len(built))
	}
	m = pickerKey(t, m, escKey)
	if len(built) != 2 {
		t.Fatalf("the builder ran %d times by the return to rest, want 2", len(built))
	}
	// RootDrift marks which fill's snapshot each instance stands on (its
	// drift meaning is scenery here).
	if built[1].snap.RootDrift != "second fill" {
		t.Errorf("the instance in use was built from %q, want the newest fill's snapshot", built[1].snap.RootDrift)
	}
}

// TestLaunchSelectorQuitWindowKeepsTheHandOffInstance pins the `r`/`R` quit
// window: once the key set its hand-off and the list is done (the screen is
// gone, but a message queued before QuitMsg can still land), a fill landing
// in that window changes nothing — no instance is built or swapped, the
// hand-off flag survives — so finishRowSession hands off through the same
// instance whose Rescue/RestoreRoot availability the key checked.
func TestLaunchSelectorQuitWindowKeepsTheHandOffInstance(t *testing.T) {
	var built []*actionRecorder
	builder := RemoveFuncsBuilder(func(snap cleanup.Snapshot, _ cleanup.GatherInputs) RemoveFuncs {
		rec := &actionRecorder{snap: snap}
		built = append(built, rec)
		return rec.funcs()
	})
	rep := pickerActionsFixture()
	m := pickerCursorTo(t, pickerActionsReady(t, rep, builder), "drift")
	m = pickerKey(t, m, keyRunes("R"))
	if !m.rows.restoreRoot || !m.rows.done {
		t.Fatalf("R left restoreRoot %v and done %v, want the hand-off held and the list done", m.rows.restoreRoot, m.rows.done)
	}
	m, _ = updateMsg(m, rowPRsLoadedMsg{Report: rep, Snap: cleanup.Snapshot{PRsAsked: true}, gen: m.rowsGen})
	if len(built) != 1 {
		t.Errorf("the quit window built %d extra instances, want none: nothing swaps after done", len(built)-1)
	}
	if !m.rows.restoreRoot {
		t.Fatal("the quit-window fill dropped the restore-root hand-off")
	}
	if err := finishRowSession(m, nil); err != nil {
		t.Fatalf("finishRowSession: %v", err)
	}
	if built[0].restored != 1 {
		t.Errorf("the hand-off restored %d times through the load's instance, want 1", built[0].restored)
	}
}

// TestLaunchSelectorFillSwapAppliesWhenAPreviewFails pins the deferred fill
// swap's return to rest through gotPreviews' error path: `x` starts a
// preview, the PR fill lands while it is in flight (deferred, nothing built),
// the re-check fails — the list is back at rest with "nothing was removed" —
// and the filled generation's instance is built and takes over right there.
func TestLaunchSelectorFillSwapAppliesWhenAPreviewFails(t *testing.T) {
	var built []*actionRecorder
	builder := RemoveFuncsBuilder(func(snap cleanup.Snapshot, _ cleanup.GatherInputs) RemoveFuncs {
		rec := &actionRecorder{snap: snap}
		built = append(built, rec)
		return rec.funcs()
	})
	rep := pickerActionsFixture()
	m := pickerKey(t, pickerCursorTo(t, pickerActionsReady(t, rep, builder), "loose"), keyRunes("x"))
	if m.rows.phase != cleanupPhasePreviewing {
		t.Fatalf("x left phase %v, want the preview in flight", m.rows.phase)
	}
	m, _ = updateMsg(m, rowPRsLoadedMsg{Report: rep, Snap: cleanup.Snapshot{PRsAsked: true}, gen: m.rowsGen})
	if len(built) != 1 {
		t.Fatalf("the builder ran %d times with a fill under a preview, want 1: the swap waits for rest", len(built))
	}
	m, _ = updateMsg(m, cleanupPreviewMsg{err: errors.New("the re-check failed")})
	if m.rows.phase != cleanupPhaseList {
		t.Fatalf("a failed preview left phase %v, want the list", m.rows.phase)
	}
	if len(built) != 2 || !built[1].snap.PRsAsked {
		t.Fatalf("builder ran %d times; a failed preview must apply the deferred fill's instance (built from the filled snapshot)", len(built))
	}
	m.rows.rm.Preview([]cleanup.Row{{Name: "probe"}})
	if strings.Join(built[1].previewed, ",") != "probe" || len(built[0].previewed) != 1 {
		t.Errorf("the model's funcs are not the filled instance (the load's saw only the original x): filled %v, load's %v", built[1].previewed, built[0].previewed)
	}
}

// deferredFillInstance defers one fill swap on m (mid-flow, as the picker's
// landing does through swapRemoveFuncs) and returns the recorder whose
// instance the swap would install and the count of times its build ran.
func deferredFillInstance(m *cleanupListModel) (*actionRecorder, *int) {
	rec, builds := &actionRecorder{}, new(int)
	swapRemoveFuncs(m, func() RemoveFuncs { *builds++; return rec.funcs() })
	return rec, builds
}

// funcsAreThe reports whether m's Preview is rec's instance, by asking it
// about a probe row and reading what the recorder saw.
func funcsAreThe(m cleanupListModel, rec *actionRecorder) bool {
	before := len(rec.previewed)
	m.rm.Preview([]cleanup.Row{{Name: "probe"}})
	return len(rec.previewed) == before+1
}

// TestFillSwapAppliesWhenARefetchLands pins the same return to rest through
// the manage list's `F` re-read: a fill swap deferred while the list
// refreshes is built and applied when the re-read lands, so the list that
// comes out of it carries the deferred build's instance.
func TestFillSwapAppliesWhenARefetchLands(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	m.rm.Refetch = func() (cleanup.Report, error) { return cleanupFixture(), nil }
	m, _ = m.startRefetch()
	if m.phase != cleanupPhaseRefreshing {
		t.Fatalf("F left phase %v, want refreshing", m.phase)
	}
	rec, builds := deferredFillInstance(&m)
	if *builds != 0 {
		t.Fatalf("the swap built %d times under a refresh, want it deferred", *builds)
	}
	m = cleanupSend(t, m, cleanupRefetchMsg{report: cleanupFixture()})
	if m.phase != cleanupPhaseList || *builds != 1 {
		t.Fatalf("after the re-read: phase %v, builds %d; want the list and the deferred build applied once", m.phase, *builds)
	}
	if !funcsAreThe(m, rec) {
		t.Error("the list's funcs are not the deferred fill's instance after the re-read landed")
	}
}

// TestFillSwapAppliesToTheListAPostRunRefreshBuilds pins it through the
// post-run rebuild: a swap deferred while the run's refresh is in flight
// travels to the fresh list (pendingRM) and is applied there.
func TestFillSwapAppliesToTheListAPostRunRefreshBuilds(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	m.phase, m.run = cleanupPhaseRefreshing, &cleanupRun{}
	rec, builds := deferredFillInstance(&m)
	if *builds != 0 {
		t.Fatalf("the swap built %d times under a refresh, want it deferred", *builds)
	}
	m = cleanupSend(t, m, cleanupRefreshMsg{report: cleanupFixture()})
	if m.phase != cleanupPhaseList || *builds != 1 {
		t.Fatalf("after the rebuild: phase %v, builds %d; want the list and the deferred build applied once", m.phase, *builds)
	}
	if !funcsAreThe(m, rec) {
		t.Error("the fresh list's funcs are not the deferred fill's instance")
	}
}
