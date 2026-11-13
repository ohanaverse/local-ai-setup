package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// quits reports whether cmd, run once, is tea.Quit's message.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestPickerCtrlCFollowsTheRowList pins the final review's C1: in the launch
// picker ctrl+c goes to the row list whenever the list is not at rest, so a
// removal in flight is never abandoned (the manage list's Ruling T), a
// confirm screen is cancelled rather than quitting wt, and only a list at
// rest quits the program.
func TestPickerCtrlCFollowsTheRowList(t *testing.T) {
	rep := pickerActionsFixture()
	build := RemoveFuncsBuilder(func(cleanup.Snapshot, cleanup.GatherInputs) RemoveFuncs { return (&actionRecorder{}).funcs() })

	m := pickerActionsReady(t, rep, build)
	m.rows.phase, m.rows.run = cleanupPhaseRunning, &cleanupRun{}
	next, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if quits(cmd) || next.rows.done || next.rows.phase != cleanupPhaseRunning {
		t.Errorf("ctrl+c during a run: quit %v, done %v, phase %v; want the run to carry on", quits(cmd), next.rows.done, next.rows.phase)
	}

	// The confirm screen: ctrl+c cancels it and the list stays.
	m = pickerKey(t, pickerCursorTo(t, pickerActionsReady(t, rep, build), "loose"), keyRunes("x"))
	row, _ := m.rows.selectedRow()
	m, _ = updateMsg(m, cleanupPreviewMsg{previews: []removal.ItemPreview{{Item: removal.ItemFromRow(row), Approval: removal.ApprovalProven}}})
	if m.rows.phase != cleanupPhaseConfirm {
		t.Fatalf("phase %v, want the confirm screen", m.rows.phase)
	}
	next, cmd = updateMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if quits(cmd) || next.rows.done || next.rows.phase != cleanupPhaseList || !strings.Contains(next.rows.status, "cancelled") {
		t.Errorf("ctrl+c on the confirm screen: quit %v, phase %v, status %q; want it cancelled", quits(cmd), next.rows.phase, next.rows.status)
	}

	// At rest it still quits wt.
	_, cmd = updateMsg(pickerActionsReady(t, rep, build), tea.KeyMsg{Type: tea.KeyCtrlC})
	if !quits(cmd) {
		t.Error("ctrl+c on a list at rest did not quit")
	}
}

// TestRunningFooterIsTruthfulInThePicker pins the progress screen's last
// line: the manage list forwards signals, so its wording stays; the picker's
// Bubble Tea program owns them, so there the screen promises keys and ctrl+c
// only.
func TestRunningFooterIsTruthfulInThePicker(t *testing.T) {
	const manage = "removing, please wait: keys, ctrl+c and signals wait until the run ends"
	const launch = "removing, please wait: keys and ctrl+c wait until the run ends"
	run := func(m cleanupListModel) cleanupListModel {
		m.phase, m.run = cleanupPhaseRunning, &cleanupRun{}
		return m
	}
	ml := run(newSizedCleanupList(t, cleanupFixture(), 120, 40))
	if v := ml.View(); !strings.Contains(v, manage) {
		t.Errorf("manage footer changed:\n%s", v)
	}
	rep := pickerActionsFixture()
	pl := run(newRowList(rep, rowListOpts{Mode: modeLaunch, Theme: themes.Default, Now: time.Now()}))
	pl.width, pl.height = 120, 40
	v := pl.View()
	if !strings.Contains(v, launch) || strings.Contains(v, "signals wait") {
		t.Errorf("launch footer wrong:\n%s", v)
	}
}

// TestPickerRemovalDiscardsEarlierFills pins the final review's I3: a PR
// fill dispatched before a removal run must not land after the run's
// post-run rebuild and put the removed row back. The run's start and its
// rebuild each bump the load generation.
func TestPickerRemovalDiscardsEarlierFills(t *testing.T) {
	rep := pickerActionsFixture()
	build := RemoveFuncsBuilder(func(cleanup.Snapshot, cleanup.GatherInputs) RemoveFuncs { return (&actionRecorder{}).funcs() })

	// The rebuild: gotRefresh bumps.
	m := pickerActionsReady(t, rep, build)
	gen := m.rowsGen
	after := rep
	after.Rows = nil
	for _, r := range rep.Rows {
		if r.Name != "loose" {
			after.Rows = append(after.Rows, r)
		}
	}
	m.rows.phase, m.rows.run = cleanupPhaseRefreshing, &cleanupRun{}
	m, _ = updateMsg(m, cleanupRefreshMsg{report: after})
	if m.rowsGen == gen {
		t.Fatal("the post-run rebuild left rowsGen alone: an earlier fill still matches")
	}
	m, _ = updateMsg(m, rowPRsLoadedMsg{Report: rep, gen: gen, Snap: cleanup.Snapshot{PRsAsked: true}})
	for _, it := range m.rows.items {
		if it.heading == "" && !it.newWorktree && cleanup.DisplayName(it.row) == "loose" {
			t.Error("a fill dispatched before the run brought the removed row back")
		}
	}
}

// TestPickerRunStartBumpsTheGeneration pins the other half: starting a run
// bumps it, so a fill dispatched before the run is dropped even while the
// run is still in flight.
func TestPickerRunStartBumpsTheGeneration(t *testing.T) {
	r := newRmRepo(t)
	r.merged("one")
	h := r.harness()
	h.stubRun = func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
		return nil
	}
	m := newRunModel(false, false, "", "", "", "", "", nil, themes.Default, "", nil, nil)
	m.width, m.height = 120, 40
	m.rows = newRowList(h.report, rowListOpts{Mode: modeLaunch, Theme: themes.Default, Now: rmLater(), RemoveFuncs: h.funcs()})
	m.rows.width, m.rows.height = 120, 40
	m.ready = true
	m = pickerCursorTo(t, m, "one")
	next, cmd := updateMsg(m, keyRunes("x"))
	m = next
	for cmd != nil && m.rows.phase != cleanupPhaseConfirm {
		next, cmd = updateMsg(m, cmd())
		m = next
	}
	if m.rows.phase != cleanupPhaseConfirm {
		t.Fatalf("phase %v, want confirm", m.rows.phase)
	}
	gen := m.rowsGen
	m, _ = updateMsg(m, keyRunes("y"))
	if m.rows.phase != cleanupPhaseRunning {
		t.Fatalf("phase %v, want running", m.rows.phase)
	}
	if m.rowsGen == gen {
		t.Error("starting a run left rowsGen alone: a fill dispatched before it still matches")
	}
}

// TestPickerReloadFromBeforeARunIsDropped pins the `F` half of I3: a reload
// dispatched before a removal run must not land over the run's rows.
func TestPickerReloadFromBeforeARunIsDropped(t *testing.T) {
	rep := pickerActionsFixture()
	build := RemoveFuncsBuilder(func(cleanup.Snapshot, cleanup.GatherInputs) RemoveFuncs { return (&actionRecorder{}).funcs() })
	m := pickerActionsReady(t, rep, build)
	m, _ = updateMsg(m, refreshRowsChosenMsg{})
	if !m.rowsRefreshing {
		t.Fatal("F did not start a reload")
	}
	m.rowsGen++ // a removal run began, and removed "loose"
	after := rep
	after.Rows = nil
	for _, r := range rep.Rows {
		if r.Name != "loose" {
			after.Rows = append(after.Rows, r)
		}
	}
	m.rows = m.rows.withReport(after)
	m, _ = updateMsg(m, rowsLoadedMsg{report: rep, repoRoot: rep.Repo})
	if m.rowsRefreshing || m.rows.fetching {
		t.Error("a dropped reload left the refresh guard set")
	}
	for _, it := range m.rows.items {
		if it.heading == "" && !it.newWorktree && cleanup.DisplayName(it.row) == "loose" {
			t.Error("a reload from before the run brought the removed row back")
		}
	}
}

// TestFinishRowSessionLogIsOutBeforeTheHandOff is the picker's "died at the
// prompt" pin (final review I1): the removal log is printed before an `r` or
// `R` hand-off asks on the terminal, exactly once overall, and a note the
// hand-off returns (declined, refused, done) ends the picker session with
// the error the hand-off would have returned.
func TestFinishRowSessionLogIsOutBeforeTheHandOff(t *testing.T) {
	out1 := removal.NewOutcome(removal.ItemFromRow(cleanup.Row{Kind: cleanup.KindWorktree, Name: "done", Path: "/repo/.worktrees/done"}),
		removal.WtRemoved, removal.BrDeleted, "outcome of the removal")
	for _, which := range []string{"rescue", "restore-root"} {
		seen := ""
		declined := errMock("not confirmed; nothing was changed")
		hand := func() error {
			seen = stdoutSoFar(t)
			return &HandOffNote{Text: "declined", Err: declined}
		}
		var rows cleanupListModel
		rows.log = []removal.Outcome{out1}
		rows.rm = RemoveFuncs{Rescue: func(cleanup.Row) error { return hand() }, RestoreRoot: hand}
		if which == "rescue" {
			rows.rescueRow = &cleanup.Row{Name: "spike"}
		} else {
			rows.restoreRoot = true
		}
		var err error
		out := captureStdout(t, func() { err = finishRowSession(model{rows: rows}, nil) })
		if !errors.Is(err, declined) {
			t.Errorf("%s: err = %v, want the declined hand-off's own error", which, err)
		}
		if !strings.Contains(seen, "outcome of the removal") {
			t.Errorf("%s: at the hand-off stdout held %q; the log must be out first", which, seen)
		}
		if n := strings.Count(out, "outcome of the removal"); n != 1 {
			t.Errorf("%s: the outcome printed %d times, want once:\n%s", which, n, out)
		}
	}
}

// TestFinishRowSessionSuccessNoteIsNotAnError pins that a SUCCESSFUL `r` or
// `R` hand-off in the picker ends the session with a nil error. The hand-off
// returns a *HandOffNote whose Err is nil — an error VALUE that means success
// — so finishRowSession must strip it through noteOf; without that, wt would
// print "wt: rescued to branch ..." and exit 1 after a hand-off that worked.
// (The decline leg above cannot catch it: a decline note still matches
// errors.Is through Unwrap.)
func TestFinishRowSessionSuccessNoteIsNotAnError(t *testing.T) {
	for _, which := range []string{"rescue", "restore-root"} {
		done := func() error { return &HandOffNote{Text: "done"} }
		var rows cleanupListModel
		rows.rm = RemoveFuncs{Rescue: func(cleanup.Row) error { return done() }, RestoreRoot: done}
		if which == "rescue" {
			rows.rescueRow = &cleanup.Row{Name: "spike"}
		} else {
			rows.restoreRoot = true
		}
		var err error
		captureStdout(t, func() { err = finishRowSession(model{rows: rows}, nil) })
		if err != nil {
			t.Errorf("%s: a successful hand-off ended the picker session with %v, want nil", which, err)
		}
	}
}
