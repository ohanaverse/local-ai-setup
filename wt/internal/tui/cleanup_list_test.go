package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// cleanupFixture is a report with every kind of row the list draws, in an
// order that is NOT the group order, so the list has to group it.
func cleanupFixture() cleanup.Report {
	return cleanup.Report{
		Repo:  "/repo",
		Base:  cleanup.Base{Default: "main", Ref: "origin/main"},
		Fetch: cleanup.FetchResult{OK: true, At: time.Now()},
		GH:    cleanup.GHOK,
		Rows: []cleanup.Row{
			{Kind: cleanup.KindWorktree, Name: "main", Path: "/repo", Branch: "main", Status: cleanup.StatusInUse,
				Blockers: []cleanup.Reason{{Code: cleanup.BlockPrimary}, {Code: cleanup.BlockCurrent}}},
			{Kind: cleanup.KindBranch, Name: "spike-picker", Branch: "spike-picker", Status: cleanup.StatusLessSure,
				Evidence: cleanup.EvPatchID, Reasons: []cleanup.Reason{{Code: cleanup.ReasonWeakSignal}}},
			{Kind: cleanup.KindWorktree, Name: "full-branch-name-paths", Path: "/repo/.worktrees/full-branch-name-paths",
				Branch: "full-branch-name-paths", Status: cleanup.StatusProven, Evidence: cleanup.EvPRTip,
				PR: &cleanup.PRInfo{Number: 517, State: "MERGED"}, AgeDays: 2, AgeKnown: true},
			{Kind: cleanup.KindWorktree, Name: "mtplx-profiles", Path: "/repo/.worktrees/mtplx-profiles", Branch: "mtplx-profiles",
				Status: cleanup.StatusUniqueWork, Reasons: []cleanup.Reason{{Code: cleanup.ReasonUnpushed, Detail: "4 unpushed commits"}}},
			{Kind: cleanup.KindBranch, Name: "fix-default-guard", Branch: "fix-default-guard", Status: cleanup.StatusProven,
				Evidence: cleanup.EvAncestor},
			{Kind: cleanup.KindRemote, Name: "old-remote", Branch: "old-remote", Remote: "origin", Status: cleanup.StatusLessSure,
				Evidence: cleanup.EvMergeAddsNothing, Reasons: []cleanup.Reason{{Code: cleanup.ReasonNoMergedPR, Detail: "no merged PR backs this remote branch"}}},
			{Kind: cleanup.KindBranch, Name: "who-knows", Branch: "who-knows", Status: cleanup.StatusLessSure, Unknown: true,
				Reasons: []cleanup.Reason{{Code: cleanup.ReasonPRUnknown, Detail: "gh exited 1"}}},
			{Kind: cleanup.KindRemote, Name: "merged-remote", Branch: "merged-remote", Remote: "origin", Status: cleanup.StatusProven,
				Evidence: cleanup.EvPRTip, PR: &cleanup.PRInfo{Number: 500, State: "MERGED"}},
			{Kind: cleanup.KindWorktree, Name: "deep", Path: "/repo/.worktrees/deep", Branch: "deep", Status: cleanup.StatusInUse,
				Blockers: []cleanup.Reason{{Code: cleanup.BlockCurrent}}},
		},
	}
}

// newSizedCleanupList builds the list model and sends it a window size, the
// way the program does before the first frame.
func newSizedCleanupList(t *testing.T, r cleanup.Report, w, h int) cleanupListModel {
	t.Helper()
	m := newCleanupList(r, themes.Default, time.Now())
	return cleanupSend(t, m, tea.WindowSizeMsg{Width: w, Height: h})
}

// cleanupSend runs one message through Update and returns the new model.
func cleanupSend(t *testing.T, m cleanupListModel, msg tea.Msg) cleanupListModel {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(cleanupListModel)
}

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

var spaceKey = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}

// moveTo moves the cursor to the top, then down until the highlighted row
// has the given display name, failing when it is never reached.
func moveTo(t *testing.T, m cleanupListModel, name string) cleanupListModel {
	t.Helper()
	m = cleanupSend(t, m, tea.KeyMsg{Type: tea.KeyHome})
	for i := 0; i < 50; i++ {
		if row, ok := m.selectedRow(); ok && cleanup.DisplayName(row) == name {
			return m
		}
		m = cleanupSend(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	t.Fatalf("never reached row %q", name)
	return m
}

// TestCleanupListGroupsByKind pins that the list is grouped by kind
// (WORKTREES, LOCAL BRANCHES, REMOTE BRANCHES, cleanup.KindGroups' order,
// the root first) instead of by status, whatever order the report's rows came
// in, and that each row still says its verdict (proven / less sure / unique
// work / in use): the status headings are gone, so the verdict must travel
// with the row or the user loses "is this safe" at a glance.
func TestCleanupListGroupsByKind(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	view := m.View()
	last := -1
	for _, h := range []string{cleanup.HeadingWorktrees, cleanup.HeadingLocal, cleanup.HeadingRemote} {
		i := strings.Index(view, h)
		if i < 0 {
			t.Fatalf("heading %q missing from view:\n%s", h, view)
		}
		if i <= last {
			t.Errorf("heading %q out of order in view:\n%s", h, view)
		}
		last = i
	}
	for _, st := range cleanup.GroupOrder {
		if strings.Contains(view, cleanup.Heading(st)) {
			t.Errorf("status heading %q still drawn:\n%s", cleanup.Heading(st), view)
		}
	}
	// Rows sit under their own kind heading, the root first among worktrees.
	wtAt, localAt, remoteAt := strings.Index(view, cleanup.HeadingWorktrees), strings.Index(view, cleanup.HeadingLocal), strings.Index(view, cleanup.HeadingRemote)
	for name, want := range map[string][2]int{
		"worktree  main": {wtAt, localAt}, "worktree  deep": {wtAt, localAt}, "fix-default-guard": {localAt, remoteAt},
		"spike-picker": {localAt, remoteAt}, "origin/merged-remote": {remoteAt, len(view)},
	} {
		if i := strings.Index(view, name); i < want[0] || i > want[1] {
			t.Errorf("%s drawn outside its group:\n%s", name, view)
		}
	}
	if strings.Index(view, "worktree  main") > strings.Index(view, "worktree  full-branch") {
		t.Errorf("root is not the first worktree row:\n%s", view)
	}
	for name, verdict := range map[string]string{
		"full-branch-name-paths": "proven", "spike-picker": "less sure", "mtplx-profiles": "unique work", "deep": "in use",
	} {
		if block := rowBlock(t, view, name); !strings.Contains(block, verdict) {
			t.Errorf("%s does not say its verdict %q: %q", name, verdict, block)
		}
	}
}

// TestRowListTagsWarnStyled pins that a row carries its cleanup.RowTags and
// that a TagWarn tag (unpushed, dirty, held, ...) is drawn in the warning
// style while an info tag (the PR, +remote) is not: the warning colour is
// what makes a risky row stand out in a long list.
func TestRowListTagsWarnStyled(t *testing.T) {
	r := cleanupFixture()
	r.Rows[3].Losses = cleanup.Losses{Unpushed: 4}           // mtplx-profiles
	r.Rows[2].Upstream = &cleanup.Upstream{Remote: "origin"} // full-branch-name-paths
	r.Rows[2].PRTag = &cleanup.PRInfo{Number: 517, State: "MERGED"}
	m := newSizedCleanupList(t, r, 120, 40)
	warn := map[string]bool{}
	for _, it := range m.items {
		if it.heading != "" || it.newWorktree {
			continue
		}
		for _, u := range rowUnits(it) {
			if u.warn {
				warn[cleanup.DisplayName(it.row)+":"+u.text] = true
			}
		}
	}
	if !warn["mtplx-profiles:unpushed"] {
		t.Errorf("unpushed is not a warn unit: %v", warn)
	}
	for k := range warn {
		if strings.HasPrefix(k, "full-branch-name-paths:") {
			t.Errorf("info tag drawn as a warning: %s", k)
		}
	}
	view := m.View()
	for name, tag := range map[string]string{"mtplx-profiles": "unpushed", "full-branch-name-paths": "#517 merged"} {
		if !strings.Contains(rowBlock(t, view, name), tag) {
			t.Errorf("%s does not show tag %q:\n%s", name, tag, view)
		}
	}
	if !strings.Contains(rowBlock(t, view, "full-branch-name-paths"), "+remote") {
		t.Errorf("+remote tag missing:\n%s", view)
	}
}

// newLaunchList builds the list in launch mode and sizes it.
func newLaunchList(t *testing.T, r cleanup.Report, w, h int) cleanupListModel {
	t.Helper()
	m := newRowList(r, rowListOpts{Mode: modeLaunch, Theme: themes.Default, Now: time.Now()})
	return cleanupSend(t, m, tea.WindowSizeMsg{Width: w, Height: h})
}

// TestLaunchModeEnterChoosesTheRow pins that in launch mode Enter on any row
// emits rowChosenMsg with that row — the primary checkout and an in-use
// (current) worktree too: launching warns, it never blocks — and removes
// nothing (no preview, no phase change).
func TestLaunchModeEnterChoosesTheRow(t *testing.T) {
	for _, name := range []string{"main", "deep", "spike-picker", "origin/old-remote"} {
		m := newLaunchList(t, cleanupFixture(), 120, 40)
		m.rm.Preview = func([]cleanup.Row) []removal.ItemPreview {
			t.Errorf("%s: enter previewed a removal", name)
			return nil
		}
		m = moveTo(t, m, name)
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatalf("%s: enter returned no command", name)
		}
		got, ok := cmd().(rowChosenMsg)
		if !ok || cleanup.DisplayName(got.Row) != name {
			t.Errorf("%s: enter emitted %#v, want rowChosenMsg for it", name, cmd())
		}
		if fm := next.(cleanupListModel); fm.phase != cleanupPhaseList {
			t.Errorf("%s: enter changed the phase to %v", name, fm.phase)
		}
	}
}

// TestLaunchModeNewWorktreeRow pins launch mode's first item, "+ New
// worktree…": it is drawn above the first group, the cursor starts on it,
// Enter on it (and `n` anywhere) emits newWorktreeChosenMsg, and launch mode
// ticks nothing (no default ticks, no boxes, space and `a` do nothing).
func TestLaunchModeNewWorktreeRow(t *testing.T) {
	m := newLaunchList(t, cleanupFixture(), 120, 40)
	view := m.View()
	ni, wi := strings.Index(view, "+ New worktree…"), strings.Index(view, cleanup.HeadingWorktrees)
	if ni < 0 || ni > wi {
		t.Fatalf("new-worktree item missing or not first:\n%s", view)
	}
	if !strings.Contains(view, "> + New worktree…") {
		t.Errorf("cursor does not start on the new-worktree item:\n%s", view)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the new-worktree item returned no command")
	}
	if _, ok := cmd().(newWorktreeChosenMsg); !ok {
		t.Errorf("enter on new worktree emitted %#v", cmd())
	}
	m = moveTo(t, m, "spike-picker")
	_, cmd = m.Update(keyRunes("n"))
	if cmd == nil {
		t.Fatal("n returned no command")
	}
	if _, ok := cmd().(newWorktreeChosenMsg); !ok {
		t.Errorf("n emitted %#v", cmd())
	}
	m = cleanupSend(t, m, spaceKey)
	m = cleanupSend(t, m, keyRunes("a"))
	if names := m.tickedNames(); len(names) != 0 {
		t.Errorf("launch mode ticked %v", names)
	}
	if v := m.View(); strings.Contains(v, "[x]") || strings.Contains(v, "[ ]") || strings.Contains(v, "[-]") {
		t.Errorf("launch mode draws tick boxes:\n%s", v)
	}

	// Manage mode has no new-worktree item and `n` does nothing.
	mm := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	if strings.Contains(mm.View(), "New worktree") {
		t.Errorf("manage mode shows the new-worktree item:\n%s", mm.View())
	}
	if _, cmd := mm.Update(keyRunes("n")); cmd != nil {
		t.Errorf("n in manage mode returned %#v", cmd())
	}

	// A launch list with no rows still offers the new worktree.
	empty := cleanupFixture()
	empty.Rows = nil
	e := newLaunchList(t, empty, 80, 24)
	if !strings.Contains(e.View(), "+ New worktree…") {
		t.Errorf("empty launch list:\n%s", e.View())
	}
	_, cmd = e.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on an empty launch list returned no command")
	}
	if _, ok := cmd().(newWorktreeChosenMsg); !ok {
		t.Errorf("enter on an empty launch list emitted %#v", cmd())
	}
}

// previewRecorder is a RemoveFuncs.Preview that records the rows it was
// asked about.
func previewRecorder(got *[]string) func([]cleanup.Row) []removal.ItemPreview {
	return func(rows []cleanup.Row) []removal.ItemPreview {
		for _, r := range rows {
			*got = append(*got, cleanup.DisplayName(r))
		}
		return nil
	}
}

// runPreview sends key and runs the preview command it returns, returning
// the names the preview was asked about and the new model.
func runPreview(t *testing.T, m cleanupListModel, key tea.KeyMsg) ([]string, cleanupListModel) {
	t.Helper()
	var got []string
	m.rm.Preview = previewRecorder(&got)
	next, cmd := m.Update(key)
	if cmd != nil {
		cmd()
	}
	return got, next.(cleanupListModel)
}

// TestManageModeEnterStillRemovesTicked pins that manage mode built through
// newRowList keeps the tick-and-Enter semantics: Enter previews exactly the
// ticked rows (not the highlighted one) and emits no rowChosenMsg.
func TestManageModeEnterStillRemovesTicked(t *testing.T) {
	m := newRowList(cleanupFixture(), rowListOpts{Mode: modeManage, Theme: themes.Default, Now: time.Now()})
	m = cleanupSend(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = moveTo(t, m, "spike-picker")
	got, fm := runPreview(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Join(got, ",") != "full-branch-name-paths,fix-default-guard" {
		t.Errorf("enter previewed %v, want the two ticked rows", got)
	}
	if fm.phase != cleanupPhasePreviewing {
		t.Errorf("phase = %v, want previewing", fm.phase)
	}
}

// TestXRemoveKeyRefusesWhatCannotBeRemoved pins the row keys' refusals: a
// locked row (a hard stop) and the new-worktree item are never handed to the
// engine — nothing is previewed and the status line says why — in manage and
// launch mode alike. What `x` does act on is pinned in
// TestCleanupListXPreviewsOnlyTheHighlightedRow.
func TestXRemoveKeyRefusesWhatCannotBeRemoved(t *testing.T) {
	// Manage, a locked row: nothing previewed, the status says why.
	m := moveTo(t, newSizedCleanupList(t, cleanupFixture(), 120, 40), "deep")
	got, fm := runPreview(t, m, keyRunes("x"))
	if len(got) != 0 || fm.phase != cleanupPhaseList || !strings.Contains(fm.status, "cannot be removed") {
		t.Errorf("manage x on a locked row: previewed %v, phase %v, status %q", got, fm.phase, fm.status)
	}
	// Launch, a locked row: nothing previewed, the status says why.
	l := moveTo(t, newLaunchList(t, cleanupFixture(), 120, 40), "deep")
	got, fl := runPreview(t, l, keyRunes("x"))
	if len(got) != 0 || fl.phase != cleanupPhaseList || !strings.Contains(fl.status, "cannot be removed") {
		t.Errorf("launch x on a locked row: previewed %v, phase %v, status %q", got, fl.phase, fl.status)
	}
	// Launch, the new-worktree item: nothing previewed.
	got, fl = runPreview(t, newLaunchList(t, cleanupFixture(), 120, 40), keyRunes("x"))
	if len(got) != 0 || fl.phase != cleanupPhaseList || fl.status == "" {
		t.Errorf("launch x on new worktree: previewed %v, phase %v, status %q", got, fl.phase, fl.status)
	}
}

// TestRowListRefreshKeepsTheMode pins that the list rebuilt after a removal
// run stays in the mode it was opened in: a launch list must not come back
// as a manage list with boxes and default ticks.
func TestRowListRefreshKeepsTheMode(t *testing.T) {
	m := newLaunchList(t, cleanupFixture(), 120, 40)
	m.phase = cleanupPhaseRefreshing
	m.run = &cleanupRun{}
	m = cleanupSend(t, m, cleanupRefreshMsg{report: cleanupFixture()})
	if m.opts.Mode != modeLaunch || len(m.tickedNames()) != 0 || !strings.Contains(m.View(), "+ New worktree…") {
		t.Errorf("after refresh: mode %v ticked %v\n%s", m.opts.Mode, m.tickedNames(), m.View())
	}
}

// TestRowListFits80x24 pins the 80x24 layout in both modes with the long
// rows: walking the cursor over every item, the view is never taller than 24
// lines or wider than 80 columns, and the highlighted row is on screen.
func TestRowListFits80x24(t *testing.T) {
	for _, mode := range []listMode{modeManage, modeLaunch} {
		m := newRowList(longRowsFixture(), rowListOpts{Mode: mode, Theme: themes.Default, Now: time.Now()})
		m = cleanupSend(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		for i := 0; i < 30; i++ {
			view := m.View()
			if h := lipgloss.Height(view); h > 24 {
				t.Fatalf("mode %v step %d: view is %d lines:\n%s", mode, i, h, view)
			}
			for _, line := range strings.Split(view, "\n") {
				if w := lipgloss.Width(line); w > 80 {
					t.Fatalf("mode %v step %d: line %d wide: %q", mode, i, w, line)
				}
			}
			if !strings.Contains(view, "> ") {
				t.Fatalf("mode %v step %d: no highlighted item on screen:\n%s", mode, i, view)
			}
			m = cleanupSend(t, m, keyRunes("j"))
		}
	}
}

// TestCleanupListDefaultTicksOnlyProven pins the default ticks: exactly the
// Tickable (proven, unblocked) local rows start ticked, and nothing else — no
// remote row, proven or not (wt does not delete origin branches). A
// less-sure or unique-work row ticked by default is a one-keystroke loss of
// work now that enter removes things.
func TestCleanupListDefaultTicksOnlyProven(t *testing.T) {
	r := cleanupFixture()
	m := newSizedCleanupList(t, r, 120, 40)
	got := strings.Join(m.tickedNames(), ",")
	want := "full-branch-name-paths,fix-default-guard"
	if got != want {
		t.Errorf("default ticks = %q, want %q", got, want)
	}
	for _, row := range r.Rows {
		ticked := false
		for _, n := range m.tickedNames() {
			ticked = ticked || n == cleanup.DisplayName(row)
		}
		if ticked != (row.Tickable() && row.Kind != cleanup.KindRemote) {
			t.Errorf("%s: ticked=%v, Tickable=%v", cleanup.DisplayName(row), ticked, row.Tickable())
		}
	}
	if !strings.Contains(m.View(), "[x] worktree  full-branch-name") {
		t.Errorf("proven row not drawn ticked:\n%s", m.View())
	}
}

// TestCleanupListInUseRowsNotToggleable pins that a hard-stop row is drawn
// [-] and that neither space nor `a` can tick it — hard-stop rows only: the
// directory the user is in or an ancestor of it, the primary checkout, a
// locked, nested or mid-operation worktree, and a worktree whose state could
// not be read (its directory is gone). The floors never lock a row and stay
// tickable (held in TestCleanupListHeldRowIsToggleable; the whole floor set
// in TestCleanupLockedOnlyForHardStops): warn-but-never-prevent means a
// softer signal must never block the user's keys.
func TestCleanupListInUseRowsNotToggleable(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	m = moveTo(t, m, "deep")
	m = cleanupSend(t, m, spaceKey)
	m = cleanupSend(t, m, keyRunes("a"))
	for _, n := range m.tickedNames() {
		if n == "deep" || n == "main" {
			t.Errorf("in-use row %q was ticked", n)
		}
	}
	if !strings.Contains(m.View(), "[-] worktree  deep") {
		t.Errorf("in-use row not drawn [-]:\n%s", m.View())
	}

	// Each clause of the lock on its own: an in_use row with no blockers,
	// and a blocker on a row that is not in_use. Both are [-] and stay
	// unticked through space and `a`.
	r := cleanupFixture()
	r.Rows = append(r.Rows,
		cleanup.Row{Kind: cleanup.KindWorktree, Name: "inuse-noblock", Path: "/repo/.worktrees/inuse-noblock", Status: cleanup.StatusInUse},
		cleanup.Row{Kind: cleanup.KindBranch, Name: "blocked-lesssure", Branch: "blocked-lesssure", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockOpenPR}}},
		cleanup.Row{Kind: cleanup.KindBranch, Name: "blocked-proven", Branch: "blocked-proven", Status: cleanup.StatusProven,
			Evidence: cleanup.EvAncestor, Blockers: []cleanup.Reason{{Code: cleanup.BlockBaseOfOpenPR}}},
		// The remaining hard stops, one code per row: locked,
		// mid-operation and nested worktrees, then the missing-dir case —
		// a worktree whose state could not be read (its directory is gone),
		// the gates_unavailable reason with the Losses.GatesUnknown flag
		// the engine refuses on ("could not read the worktree's state").
		cleanup.Row{Kind: cleanup.KindWorktree, Name: "locked-wt", Path: "/repo/.worktrees/locked-wt", Branch: "locked-wt", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockLocked}}},
		cleanup.Row{Kind: cleanup.KindWorktree, Name: "mid-rebase", Path: "/repo/.worktrees/mid-rebase", Branch: "mid-rebase", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockOperation, Detail: "rebase-merge"}}},
		cleanup.Row{Kind: cleanup.KindWorktree, Name: "holds-one", Path: "/repo/.worktrees/holds-one", Branch: "holds-one", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockNested, Detail: "/repo/.worktrees/holds-one/.worktrees/inner"}}},
		cleanup.Row{Kind: cleanup.KindWorktree, Name: "gone-dir", Path: "/repo/.worktrees/gone-dir", Branch: "gone-dir", Status: cleanup.StatusLessSure, Unknown: true,
			Reasons: []cleanup.Reason{{Code: cleanup.ReasonGatesUnavailable, Detail: "cleanup: worktree /repo/.worktrees/gone-dir is prunable: its git link is broken, so its state cannot be read"}},
			Losses:  cleanup.Losses{GatesUnknown: true}},
	)
	for _, name := range []string{"inuse-noblock", "blocked-lesssure", "blocked-proven", "locked-wt", "mid-rebase", "holds-one", "gone-dir"} {
		m = newSizedCleanupList(t, r, 120, 50)
		m = moveTo(t, m, name)
		m = cleanupSend(t, m, spaceKey)
		m = cleanupSend(t, m, keyRunes("a"))
		for _, n := range m.tickedNames() {
			if n == name {
				t.Errorf("%s was ticked", name)
			}
		}
		if block := rowBlock(t, m.View(), name); !strings.HasPrefix(strings.TrimLeft(block, "> "), "[-]") {
			t.Errorf("%s not drawn [-]: %q", name, block)
		}
	}
}

// TestCleanupListHeldRowIsToggleable pins that a floor blocker (held by
// another wt session) does not lock a row: only hard stops do, so the user
// may tick it and the removal asks for the typed confirmation.
func TestCleanupListHeldRowIsToggleable(t *testing.T) {
	r := cleanupFixture()
	r.Rows = append(r.Rows, cleanup.Row{Kind: cleanup.KindBranch, Name: "held-lesssure", Branch: "held-lesssure", Status: cleanup.StatusLessSure,
		Blockers: []cleanup.Reason{{Code: cleanup.BlockHeld}}})
	m := newSizedCleanupList(t, r, 120, 50)
	m = moveTo(t, m, "held-lesssure")
	m = cleanupSend(t, m, spaceKey)
	if !slices.Contains(m.tickedNames(), "held-lesssure") {
		t.Errorf("held row was not ticked by space")
	}
}

// TestCleanupListUnprovenRemoteRowsAreLocked pins that a remote row which is
// not a proven origin branch is [-] and cannot be ticked by space or `a`:
// enter removes what is ticked, and the engine deletes on origin only a
// proven origin row (the proven one is tickable, see
// TestOriginRowsAreTickableButNeverTickedForYou).
func TestCleanupListUnprovenRemoteRowsAreLocked(t *testing.T) {
	for _, name := range []string{"origin/old-remote"} {
		m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
		m = moveTo(t, m, name)
		m = cleanupSend(t, m, spaceKey)
		m = cleanupSend(t, m, keyRunes("a"))
		for _, n := range m.tickedNames() {
			if n == name {
				t.Errorf("remote row %s was ticked", name)
			}
		}
		if !strings.Contains(m.View(), "[-] remote    "+name) {
			t.Errorf("remote row %s not drawn [-]:\n%s", name, m.View())
		}
		if !strings.Contains(m.View(), "only a proven origin branch can be removed") {
			t.Errorf("the detail line does not say why %s cannot be ticked:\n%s", name, m.View())
		}
	}
}

// TestCleanupListSpaceTogglesLocalRows pins that space toggles a row that
// is not locked: off and on again for a proven row, and on for a less-sure
// local branch (the user may decide; it is never ticked for them).
func TestCleanupListSpaceTogglesLocalRows(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	m = moveTo(t, m, "fix-default-guard")
	m = cleanupSend(t, m, spaceKey)
	if strings.Contains(strings.Join(m.tickedNames(), ","), "fix-default-guard") {
		t.Fatal("space did not untick a ticked row")
	}
	m = cleanupSend(t, m, spaceKey)
	if !strings.Contains(strings.Join(m.tickedNames(), ","), "fix-default-guard") {
		t.Fatal("space did not tick it again")
	}
	m = moveTo(t, m, "spike-picker")
	m = cleanupSend(t, m, spaceKey)
	if !strings.Contains(strings.Join(m.tickedNames(), ","), "spike-picker") {
		t.Errorf("space did not tick a less-sure local row; ticked: %v", m.tickedNames())
	}
}

// TestCleanupListAllTicksOnlyProven pins that `a` ticks every tickable
// proven row (bringing back one the user unticked) and nothing else, even
// with the cursor on a less-sure row.
func TestCleanupListAllTicksOnlyProven(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	m = moveTo(t, m, "fix-default-guard")
	m = cleanupSend(t, m, spaceKey) // untick
	m = moveTo(t, m, "spike-picker")
	m = cleanupSend(t, m, keyRunes("a"))
	got := strings.Join(m.tickedNames(), ",")
	if want := "full-branch-name-paths,fix-default-guard"; got != want {
		t.Errorf("after a: ticked = %q, want %q", got, want)
	}
}

// TestCleanupListTitleNamesRemoval pins the list's key line: enter removes
// the ticked rows now, and the PR A "view only" wording is gone from it.
func TestCleanupListTitleNamesRemoval(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	if v := m.View(); !strings.Contains(v, "enter remove ticked") || strings.Contains(v, "view only") {
		t.Errorf("title line:\n%s", v)
	}
	// q quits with nothing to print.
	next, cmd := m.Update(keyRunes("q"))
	if cmd == nil || next.(cleanupListModel).exitMessage() != "" {
		t.Errorf("q: cmd=%v message=%q; want tea.Quit and no message", cmd, next.(cleanupListModel).exitMessage())
	}
}

// TestCleanupListTitleLineNamesAllActions pins the manage title's key legend
// at two widths: at 100x30 it names every action in its full form — `x
// remove`, `R restore root`, `F refresh`, `r rescue` beside `q quit` — and at
// 80x24 it still fits with no clipping and still spells out every action name
// (only key qualifiers and separators tighten, never an action; `q quit` stays
// visible). Why: the title is the only
// place `x`, `R` and `F` are named, and a legend wider than the terminal
// silently loses its tail — `q quit` — on the 80-column terminals people
// actually run.
func TestCleanupListTitleLineNamesAllActions(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 100, 30)
	for _, want := range []string{"x remove", "R restore root", "F refresh", "r rescue", "q quit"} {
		if !strings.Contains(m.titleLine(), want) {
			t.Errorf("title at 100x30 misses %q:\n%s", want, m.titleLine())
		}
	}
	if v := m.View(); !strings.Contains(v, "x remove") || !strings.Contains(v, "F refresh") {
		t.Errorf("view at 100x30 does not carry the title's actions:\n%s", v)
	}
	if w := lipgloss.Width(m.titleLine()); w > 100 {
		t.Errorf("title at 100x30 is %d columns and clips:\n%s", w, m.titleLine())
	}
	narrow := newSizedCleanupList(t, cleanupFixture(), 80, 24)
	if w := lipgloss.Width(narrow.titleLine()); w > 80 {
		t.Errorf("title at 80x24 is %d columns and clips:\n%s", w, narrow.titleLine())
	}
	if !strings.Contains(narrow.titleLine(), "q quit") {
		t.Errorf("title at 80x24 hides q quit:\n%s", narrow.titleLine())
	}
	for _, want := range []string{"x remove", "R restore root", "F refresh", "r rescue"} {
		if !strings.Contains(narrow.titleLine(), want) {
			t.Errorf("title at 80x24 shortens %q:\n%s", want, narrow.titleLine())
		}
	}
}

// longRowsFixture is cleanupFixture plus the rows whose sentences are too
// long for one line at 80 columns: a long branch name, the primary
// checkout with its path (the only Current worktree, so "you are here"),
// and an Unknown branch with several long reasons.
func longRowsFixture() cleanup.Report {
	r := cleanupFixture()
	long := "feature/an-extremely-long-branch-name-that-goes-on-and-on-forever"
	r.Rows = append(r.Rows, cleanup.Row{Kind: cleanup.KindBranch, Name: long, Branch: long,
		Status: cleanup.StatusProven, Evidence: cleanup.EvMergeAddsNothing})
	r.Rows[2].Name = "worktree-" + long
	// Only the primary checkout is Current now: drop "deep"'s marker.
	r.Rows[8].Blockers = []cleanup.Reason{{Code: cleanup.BlockLocked}}
	r.Rows[0].Path = "/private/tmp/claude-501/a-rather-deep/scratch/cleanup-fixture/repo"
	r.Rows[0].Blockers = []cleanup.Reason{{Code: cleanup.BlockPrimary, Detail: r.Rows[0].Path}, {Code: cleanup.BlockCurrent}}
	r.Rows = append(r.Rows, cleanup.Row{Kind: cleanup.KindBranch, Name: "many-reasons", Branch: "many-reasons",
		Status: cleanup.StatusLessSure, Unknown: true, Evidence: cleanup.EvSameCommit, Reasons: []cleanup.Reason{
			{Code: cleanup.ReasonPRUnknown, Detail: "gh exited 1: could not resolve to a Repository with the name o/r"},
			{Code: cleanup.ReasonUnpushedUnknown, Detail: "rev-list failed: fatal: bad revision"},
			{Code: cleanup.ReasonIgnoredFiles, Detail: ".env, .venv, node_modules"},
		}})
	return r
}

// squash is s with every whitespace character removed, so a sentence can be
// compared with its wrapped rendering whatever the line breaks.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// rowBlock is the rendered text of the row whose name column starts with
// namePrefix: its first line and the continuation lines under it, joined.
func rowBlock(t *testing.T, view, namePrefix string) string {
	t.Helper()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		trim := strings.TrimLeft(line, "> ")
		if !(strings.HasPrefix(trim, "[x]") || strings.HasPrefix(trim, "[ ]") || strings.HasPrefix(trim, "[-]")) {
			continue
		}
		// After the 3-column box ("[ ]" holds a space): kind, then name.
		if fields := strings.Fields(trim[3:]); len(fields) < 2 || !strings.HasPrefix(fields[1], namePrefix) {
			continue
		}
		block := []string{line}
		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "    ") || strings.HasPrefix(strings.TrimSpace(next), "[") {
				break
			}
			block = append(block, next)
		}
		return strings.Join(block, "\n")
	}
	t.Fatalf("no row named %q in view:\n%s", namePrefix, view)
	return ""
}

// TestCleanupListNarrowWidthKeepsEvidence pins the width rule at 80
// columns with long names and long sentences: no line is wider than the
// terminal, the name column is what gets cut, and EVERY row's whole
// sentence (cleanup.RowText, the age after it) is in that row's own lines —
// wrapped onto continuation lines, never cut with an ellipsis.
func TestCleanupListNarrowWidthKeepsEvidence(t *testing.T) {
	r := longRowsFixture()
	m := newSizedCleanupList(t, r, 80, 60)
	view := m.View()
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line %d is %d columns wide: %q", i, w, line)
		}
	}
	long := r.Rows[len(r.Rows)-2].Name
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "[x]") && strings.Contains(line, long) {
			t.Errorf("long name was not truncated in its row at 80 columns: %q", line)
		}
	}
	wrapped := 0
	for _, row := range r.Rows {
		name := cleanup.DisplayName(row)
		block := rowBlock(t, view, name[:min(len(name), 12)])
		if strings.Count(block, "\n") > 0 {
			wrapped++
		}
		if !strings.Contains(squash(block), squash(cleanup.RowText(r, row))) {
			t.Errorf("row %q does not carry its whole sentence %q:\n%s", name, cleanup.RowText(r, row), block)
		}
		// A sentence cut with an ellipsis cannot contain its own end, so the
		// check above is also the no-"…" check for the evidence.
	}
	if wrapped < 2 {
		t.Errorf("only %d rows wrapped at 80 columns; the fixture's long sentences should wrap:\n%s", wrapped, view)
	}
}

// TestCleanupListSaysWhenRowsAreOffScreen pins the scroll hints: a list
// taller than its room says there is more below (the real 80x24 screen
// ended on a group heading with its rows out of sight and no sign of them),
// and once scrolled, that there is more above. The view stays within 24.
func TestCleanupListSaysWhenRowsAreOffScreen(t *testing.T) {
	m := newSizedCleanupList(t, longRowsFixture(), 80, 24)
	view := m.View()
	if !strings.Contains(view, "↓ more below") || strings.Contains(view, "↑ more above") {
		t.Errorf("top of a long list: want only the below hint:\n%s", view)
	}
	m = cleanupSend(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	view = m.View()
	if !strings.Contains(view, "↑ more above") || strings.Contains(view, "↓ more below") {
		t.Errorf("end of a long list: want only the above hint:\n%s", view)
	}
	// Scrolled, the list opens on a heading or a row's first line, never on
	// the tail of a wrapped row.
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "gh:") && i+2 < len(lines) && strings.HasPrefix(lines[i+2], "    ") {
			t.Errorf("list opens on a continuation line %q:\n%s", lines[i+2], view)
		}
	}
	if h := lipgloss.Height(view); h > 24 {
		t.Errorf("view is %d lines at 24", h)
	}
	if strings.Contains(newSizedCleanupList(t, cleanupFixture(), 120, 60).View(), "more below") {
		t.Error("a list that fits shows a scroll hint")
	}
}

// TestCleanupListPrimaryCheckoutSaysYouAreHere pins that at 80 columns the
// primary checkout's row, with its long path, still shows "you are here":
// the marker was the part an ellipsis cut off first.
func TestCleanupListPrimaryCheckoutSaysYouAreHere(t *testing.T) {
	m := newSizedCleanupList(t, longRowsFixture(), 80, 60)
	block := rowBlock(t, m.View(), "main")
	if !strings.Contains(squash(block), squash("you are here")) {
		t.Errorf("primary checkout row lacks the marker:\n%s", block)
	}
}

// TestCleanupListWrappedRowStaysOnScreen pins the 80x24 layout with the
// cursor on a row whose sentence wraps to three or more lines: the header,
// the whole highlighted row and the detail line are all on screen, and the
// view is no taller than the terminal.
func TestCleanupListWrappedRowStaysOnScreen(t *testing.T) {
	r := longRowsFixture()
	m := newSizedCleanupList(t, r, 80, 24)
	m = moveTo(t, m, "many-reasons")
	view := m.View()
	if h := lipgloss.Height(view); h > 24 {
		t.Errorf("view is %d lines at 24:\n%s", h, view)
	}
	block := rowBlock(t, view, "many-reasons")
	if strings.Count(block, "\n") < 2 {
		t.Fatalf("highlighted row is not 3+ lines at 80 columns:\n%s", block)
	}
	if !strings.HasPrefix(block, "> ") {
		t.Errorf("highlighted row has no cursor:\n%s", block)
	}
	row := r.Rows[len(r.Rows)-1]
	if !strings.Contains(squash(block), squash(cleanup.RowText(r, row))) {
		t.Errorf("highlighted row cut:\n%s", block)
	}
	if !strings.Contains(view, "base:  origin/main") {
		t.Errorf("header missing:\n%s", view)
	}
	if !strings.Contains(view, "── many-reasons ─") {
		t.Errorf("detail line missing:\n%s", view)
	}
	// Moving back up to the top brings the first row (and its heading) back.
	for i := 0; i < 20; i++ {
		m = cleanupSend(t, m, tea.KeyMsg{Type: tea.KeyUp})
	}
	view = m.View()
	if !strings.Contains(view, cleanup.HeadingWorktrees) || !strings.Contains(view, "> [-] worktree  main") {
		t.Errorf("top of the list not back on screen:\n%s", view)
	}
}

// TestCleanupListCurrentMarkerOnLongestPath pins that "you are here" goes on
// the innermost worktree holding the caller's directory (the longest Current
// path), and the outer one says it only contains it.
func TestCleanupListCurrentMarkerOnLongestPath(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	view := m.View()
	for _, line := range strings.Split(view, "\n") {
		switch {
		case strings.Contains(line, "worktree  deep"):
			if !strings.Contains(line, "you are here") {
				t.Errorf("deepest current row lacks the marker: %q", line)
			}
		case strings.Contains(line, "worktree  main"):
			if strings.Contains(line, "you are here") || !strings.Contains(line, "contains the directory you are in") {
				t.Errorf("outer current row: %q", line)
			}
		}
	}
}

// TestCleanupListZeroSizeAndEmptyReport pins that the screen survives a
// zero-size window and a report with no rows, and that an empty report
// says so and quits on any key.
func TestCleanupListZeroSizeAndEmptyReport(t *testing.T) {
	m := newCleanupList(cleanupFixture(), themes.Default, time.Now())
	_ = m.View()
	m = cleanupSend(t, m, tea.WindowSizeMsg{Width: 0, Height: 0})
	_ = m.View()
	m = cleanupSend(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = cleanupSend(t, m, spaceKey)
	_ = m.View()
	m = cleanupSend(t, m, tea.WindowSizeMsg{Width: 1, Height: 1})
	_ = m.View()

	empty := cleanupFixture()
	empty.Rows = nil
	e := newCleanupList(empty, themes.Default, time.Now())
	_ = e.View()
	e = cleanupSend(t, e, tea.WindowSizeMsg{Width: 80, Height: 24})
	if !strings.Contains(e.View(), "nothing to clean up") {
		t.Errorf("empty report view:\n%s", e.View())
	}
	_, cmd := e.Update(keyRunes("x"))
	if cmd == nil {
		t.Fatal("any key on the empty report should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("empty report key produced %T, want tea.QuitMsg", cmd())
	}
}

// TestCleanupListCursorSkipsHeadings pins that the cursor lands only on
// rows: the first row at start, and moving up from it stays on a row.
func TestCleanupListCursorSkipsHeadings(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	if _, ok := m.selectedRow(); !ok {
		t.Fatal("cursor starts on a heading")
	}
	m = cleanupSend(t, m, tea.KeyMsg{Type: tea.KeyUp})
	for i := 0; i < 20; i++ {
		if _, ok := m.selectedRow(); !ok {
			t.Fatalf("cursor on a heading after %d downs", i)
		}
		m = cleanupSend(t, m, keyRunes("j"))
	}
	for i := 0; i < 20; i++ {
		m = cleanupSend(t, m, keyRunes("k"))
		if _, ok := m.selectedRow(); !ok {
			t.Fatalf("cursor on a heading after %d ups", i)
		}
	}
}

// nestedReport is cleanupFixture with its primary checkout holding n other
// worktrees (one nested blocker each, as CheckGates makes them) and being
// the only Current worktree.
func nestedReport(n int) cleanup.Report {
	r := cleanupFixture()
	r.Rows[8].Blockers = []cleanup.Reason{{Code: cleanup.BlockLocked}}
	bl := []cleanup.Reason{{Code: cleanup.BlockPrimary, Detail: "/repo"}, {Code: cleanup.BlockCurrent}}
	for i := 0; i < n; i++ {
		bl = append(bl, cleanup.Reason{Code: cleanup.BlockNested, Detail: fmt.Sprintf("/repo/.claude/worktrees/agent-%02d", i)})
	}
	r.Rows[0].Blockers = append(bl, cleanup.Reason{Code: cleanup.BlockDefaultBranch, Detail: "main"})
	return r
}

// TestCleanupListNestedPrimaryRowIsShort pins Ruling 16: the primary
// checkout holding 30 worktrees is a short row ("contains 30 other
// worktrees", no paths) at 80 columns, and highlighting it at 80x24 keeps
// the title, the header and the detail on screen. One path per worktree
// made the row taller than the terminal.
func TestCleanupListNestedPrimaryRowIsShort(t *testing.T) {
	m := newSizedCleanupList(t, nestedReport(30), 80, 60)
	block := rowBlock(t, m.View(), "main")
	if n := strings.Count(block, "\n") + 1; n > 4 {
		t.Errorf("primary row is %d lines at 80 columns, want <= 4:\n%s", n, block)
	}
	if !strings.Contains(squash(block), squash("contains 30 other worktrees")) || strings.Contains(block, "agent-") {
		t.Errorf("primary row does not collapse its nested worktrees:\n%s", block)
	}

	m = moveTo(t, newSizedCleanupList(t, nestedReport(30), 80, 24), "main")
	view := m.View()
	if h := lipgloss.Height(view); h > 24 {
		t.Errorf("view is %d lines at 24", h)
	}
	for _, want := range []string{"wt worktrees", "q quit", "base:  origin/main", "── main ─", "contains 30 other worktrees"} {
		if !strings.Contains(strings.Join(strings.Fields(view), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("highlighted primary at 80x24 lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "agent-") {
		t.Errorf("30 nested paths do not fit one line, yet the detail lists them:\n%s", view)
	}
}

// TestCleanupListDetailNamesNestedPathsThatFit pins the other half of
// Ruling 16: when the nested worktrees' paths fit on one line, the
// highlighted row's detail names them; the row itself never does.
func TestCleanupListDetailNamesNestedPathsThatFit(t *testing.T) {
	m := moveTo(t, newSizedCleanupList(t, nestedReport(1), 120, 40), "main")
	view := m.View()
	if !strings.Contains(view, "/repo/.claude/worktrees/agent-00") {
		t.Errorf("detail lacks the one nested path:\n%s", view)
	}
	if block := rowBlock(t, view, "main"); strings.Contains(block, "agent-00") || !strings.Contains(block, "contains 1 other worktree") {
		t.Errorf("row should carry the count, not the path:\n%s", block)
	}
}

// TestCleanupListFortyRowsAllReachable pins scrolling with many rows: at
// 80x24, `j` reaches each of 40 worktree rows in turn, and each, the last
// included, is on screen whole with the cursor on it.
func TestCleanupListFortyRowsAllReachable(t *testing.T) {
	r := cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"}, GH: cleanup.GHOK}
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("wt-%02d", i)
		r.Rows = append(r.Rows, cleanup.Row{Kind: cleanup.KindWorktree, Name: name, Path: "/repo/.worktrees/" + name,
			Branch: name, Status: cleanup.StatusProven, Evidence: cleanup.EvAncestor})
	}
	m := newSizedCleanupList(t, r, 80, 24)
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("wt-%02d", i)
		row, ok := m.selectedRow()
		if !ok || row.Name != name {
			t.Fatalf("after %d j: highlighted %q, want %q", i, row.Name, name)
		}
		view := m.View()
		if h := lipgloss.Height(view); h > 24 {
			t.Fatalf("%s: view is %d lines", name, h)
		}
		block := rowBlock(t, view, name)
		if !strings.HasPrefix(block, "> ") || !strings.Contains(block, "ancestor of origin/main") {
			t.Fatalf("%s not whole and highlighted on screen:\n%s", name, view)
		}
		m = cleanupSend(t, m, keyRunes("j"))
	}
}

// rescueListFixture is a report with one worktree row that has local work
// (unpushed commits and dirt), one clean worktree and one branch row.
func rescueListFixture() cleanup.Report {
	return cleanup.Report{
		Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"}, Fetch: cleanup.FetchResult{OK: true, At: time.Now()}, GH: cleanup.GHOK,
		Rows: []cleanup.Row{
			{Kind: cleanup.KindWorktree, Name: "spike", Path: "/repo/.worktrees/spike", Branch: "spike", Ref: "refs/heads/spike",
				Status: cleanup.StatusUniqueWork, Losses: cleanup.Losses{Dirty: true, Unpushed: 2}},
			{Kind: cleanup.KindWorktree, Name: "tidy", Path: "/repo/.worktrees/tidy", Branch: "tidy", Ref: "refs/heads/tidy", Status: cleanup.StatusProven},
			{Kind: cleanup.KindBranch, Name: "lone", Branch: "lone", Status: cleanup.StatusLessSure},
		},
	}
}

// TestCleanupListRKeyHandsTheRowToRescue checks `r` on a worktree row with
// local work quits the list and records that row for the rescue hand-off, and
// that the title line names the key. Why: the rescue prompts on the real
// terminal, so the list must leave the alt screen first.
func TestCleanupListRKeyHandsTheRowToRescue(t *testing.T) {
	m := newSizedCleanupList(t, rescueListFixture(), 100, 30)
	m.rm.Rescue = func(cleanup.Row) error { return nil }
	m = moveTo(t, m, "spike")
	if v := m.View(); !strings.Contains(v, "r rescue") {
		t.Errorf("title line does not offer r rescue:\n%s", v)
	}

	next, cmd := m.Update(keyRunes("r"))

	fm := next.(cleanupListModel)
	if cmd == nil || fm.rescueRow == nil || fm.rescueRow.Name != "spike" || !fm.done {
		t.Fatalf("cmd=%v rescueRow=%+v done=%v; want quit with the spike row", cmd, fm.rescueRow, fm.done)
	}
}

// TestCleanupListRKeyExplainsWhenNotOffered checks `r` on a clean worktree, on
// a branch row, and with no rescue wired in stays in the list and says why on
// the status line. Why: a key that does nothing, silently, reads as broken.
func TestCleanupListRKeyExplainsWhenNotOffered(t *testing.T) {
	for _, c := range []struct{ row, want string }{
		{"tidy", "nothing to keep"},
		{"lone", "wt -W lone"},
	} {
		m := newSizedCleanupList(t, rescueListFixture(), 100, 30)
		m.rm.Rescue = func(cleanup.Row) error { return nil }
		m = moveTo(t, m, c.row)
		next, cmd := m.Update(keyRunes("r"))
		fm := next.(cleanupListModel)
		if fm.rescueRow != nil || fm.done || (cmd != nil && cmd() != nil) || !strings.Contains(fm.status, c.want) {
			t.Errorf("%s: rescueRow=%v done=%v status=%q; want to stay with %q", c.row, fm.rescueRow, fm.done, fm.status, c.want)
		}
	}
	m := moveTo(t, newSizedCleanupList(t, rescueListFixture(), 100, 30), "spike")
	next, _ := m.Update(keyRunes("r"))
	if fm := next.(cleanupListModel); fm.rescueRow != nil || !strings.Contains(fm.status, "not available") {
		t.Errorf("no Rescue wired: rescueRow=%v status=%q", fm.rescueRow, fm.status)
	}
}

// TestCleanupListFKeyRefetches: `F` in the manage list re-reads the
// repository through RemoveFuncs.Refetch and rebuilds the list from the
// fresh report; when the re-read fails the list keeps its rows and the
// status says why in one line (nothing was removed here, so a failure never
// ends the program the way the post-run refresh's failure does).
func TestCleanupListFKeyRefetches(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	calls := 0
	m.rm.Refetch = func() (cleanup.Report, error) {
		calls++
		rep := cleanupFixture()
		rep.Rows = append(rep.Rows, cleanup.Row{Kind: cleanup.KindBranch, Name: "fresh-branch", Branch: "fresh-branch", Status: cleanup.StatusLessSure})
		return rep, nil
	}
	m = press(t, m, keyRunes("F"))
	if calls != 1 {
		t.Fatalf("Refetch called %d times, want 1", calls)
	}
	if v := m.View(); !strings.Contains(v, "fresh-branch") {
		t.Errorf("the list was not rebuilt from the fresh report:\n%s", v)
	}
	if m.phase != cleanupPhaseList {
		t.Errorf("phase = %v, want the list again after the refetch", m.phase)
	}

	m.rm.Refetch = func() (cleanup.Report, error) { calls++; return cleanup.Report{}, errMock("re-read failed") }
	m = press(t, m, keyRunes("F"))
	if calls != 2 {
		t.Fatalf("Refetch called %d times, want 2", calls)
	}
	if m.status == "" || strings.Contains(m.status, "\n") {
		t.Errorf("status = %q, want a one-line failure note", m.status)
	}
	if m.phase != cleanupPhaseList {
		t.Errorf("phase = %v; a failed refetch stays in the list", m.phase)
	}
	if v := m.View(); !strings.Contains(v, "fresh-branch") {
		t.Errorf("a failed refetch dropped the old list:\n%s", v)
	}
}

// TestCleanupListFKeyKeepsTheUsersUntickedRows: a row the user unticked stays
// unticked across the `F` re-read, and the rows still ticked stay ticked.
// Why: the rebuilt list would otherwise start from newRowList's default
// (every tickable local row ticked), and enter would then remove a row the
// user had deliberately kept.
func TestCleanupListFKeyKeepsTheUsersUntickedRows(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	var kept cleanup.Row
	var keptSet bool
	for i, it := range m.items {
		if m.ticked[i] && !keptSet {
			kept, keptSet = it.row, true
			delete(m.ticked, i)
		}
	}
	if !keptSet {
		t.Fatal("the fixture has no default-ticked row to untick")
	}
	wantTicked := len(m.ticked)
	m.rm.Refetch = func() (cleanup.Report, error) { return cleanupFixture(), nil }
	m = press(t, m, keyRunes("F"))
	got := 0
	for i, it := range m.items {
		if it.heading != "" {
			continue
		}
		if keyOf(it.row) == keyOf(kept) && m.ticked[i] {
			t.Errorf("row %q was unticked by the user and is ticked again after F", kept.Name)
		}
		if m.ticked[i] {
			got++
		}
	}
	if got != wantTicked {
		t.Errorf("%d rows ticked after F, want %d (the others' ticks kept)", got, wantTicked)
	}
}

// TestCleanupListFetchKeyRefreshes: `F` refreshes in place — RemoveFuncs.Refetch
// runs exactly once and the list rebuilds from its fresh report without
// leaving the screen. Why: this is the key path end to end (the `F` binding,
// the refetch command and the re-entry into the list); a break anywhere along
// it turns `F` into a dead key.
func TestCleanupListFetchKeyRefreshes(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	calls := 0
	m.rm.Refetch = func() (cleanup.Report, error) {
		calls++
		rep := cleanupFixture()
		rep.Rows = append(rep.Rows, cleanup.Row{Kind: cleanup.KindBranch, Name: "fresh-branch", Branch: "fresh-branch", Status: cleanup.StatusLessSure})
		return rep, nil
	}
	m = press(t, m, keyRunes("F"))
	if calls != 1 {
		t.Fatalf("Refetch called %d times, want 1", calls)
	}
	if v := m.View(); !strings.Contains(v, "fresh-branch") {
		t.Errorf("the list did not rebuild from the fresh report:\n%s", v)
	}
	if m.done || m.phase != cleanupPhaseList {
		t.Errorf("done=%v phase=%v; F refreshes in place and stays in the list", m.done, m.phase)
	}
}

// TestCleanupListFetchFailureKeepsOldListAndSaysWhy: when the `F` re-read
// fails — Refetch's own error, the re-read's failure (a fetch failure is not
// this error: it travels in the report's Fetch field and the header renders
// it) — the old rows stay and the status says why in one line. Why: nothing
// was removed here, so the rows the user was reading are still what to show;
// a blank or silent failure reads as a broken key.
func TestCleanupListFetchFailureKeepsOldListAndSaysWhy(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	m.rm.Refetch = func() (cleanup.Report, error) {
		return cleanup.Report{}, errMock("re-read failed: git broke")
	}
	m = press(t, m, keyRunes("F"))
	if v := m.View(); !strings.Contains(v, "mtplx-profiles") || !strings.Contains(v, "old-remote") {
		t.Errorf("a failed refresh dropped the old list:\n%s", v)
	}
	if !strings.Contains(m.status, "git broke") || strings.Contains(m.status, "\n") {
		t.Errorf("status = %q; want the failure's reason in one line", m.status)
	}
	if m.done || m.phase != cleanupPhaseList {
		t.Errorf("done=%v phase=%v; a failed refresh stays in the list", m.done, m.phase)
	}
}

// TestCleanupListFetchNotWired: `F` while RemoveFuncs.Refetch is still nil
// (the pre-wiring gap cmd/wt closes) says one line on the status and does
// nothing — no panic, no silent no-op, and nothing run at all (no prune: no
// refresh ran). Why: the gap must read as a told gap, not a broken key.
func TestCleanupListFetchNotWired(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	next, cmd := m.Update(keyRunes("F"))
	fm := next.(cleanupListModel)
	if fm.done || fm.phase != cleanupPhaseList || (cmd != nil && cmd() != nil) {
		t.Fatalf("done=%v phase=%v cmd=%v; want nothing to happen", fm.done, fm.phase, cmd)
	}
	if fm.status != "refresh is not available here" {
		t.Errorf("status = %q, want %q", fm.status, "refresh is not available here")
	}
	if v := fm.View(); !strings.Contains(v, "mtplx-profiles") {
		t.Errorf("the list changed under an unwired F:\n%s", v)
	}
}

// TestCleanupListFetchKeepsCursorOnSameRow: the `F` re-read keeps the
// highlighted row — the same row by kind, ref, path and name, not the same
// index (the fresh report gained a row above it) — and a failed re-read keeps
// the cursor there too. Why: a refresh that jumps the cursor loses the user's
// place mid-review.
func TestCleanupListFetchKeepsCursorOnSameRow(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	m = moveTo(t, m, "who-knows")
	m.rm.Refetch = func() (cleanup.Report, error) {
		rep := cleanupFixture()
		rep.Rows = append([]cleanup.Row{{Kind: cleanup.KindWorktree, Name: "aaa-new", Path: "/repo/.worktrees/aaa-new",
			Branch: "aaa-new", Status: cleanup.StatusLessSure}}, rep.Rows...)
		return rep, nil
	}
	m = press(t, m, keyRunes("F"))
	if v := m.View(); !strings.Contains(v, "aaa-new") {
		t.Fatalf("the fresh report did not rebuild the list:\n%s", v)
	}
	if row, ok := m.selectedRow(); !ok || cleanup.DisplayName(row) != "who-knows" {
		t.Fatalf("after F the cursor is on %q (ok=%v), want who-knows", cleanup.DisplayName(row), ok)
	}
	m.rm.Refetch = func() (cleanup.Report, error) { return cleanup.Report{}, errMock("re-read failed") }
	m = press(t, m, keyRunes("F"))
	if row, ok := m.selectedRow(); !ok || cleanup.DisplayName(row) != "who-knows" {
		t.Fatalf("after a failed F the cursor is on %q (ok=%v), want who-knows", cleanup.DisplayName(row), ok)
	}
}

// TestCleanupListRKeyStillRescues: adding `F` must not displace `r` — the
// manage list still hands the highlighted row to rescue and quits, and no
// refetch runs on the way (`F` is the only refetch key).
func TestCleanupListRKeyStillRescues(t *testing.T) {
	m := newSizedCleanupList(t, rescueListFixture(), 100, 30)
	refetches := 0
	m.rm.Refetch = func() (cleanup.Report, error) { refetches++; return cleanupFixture(), nil }
	m.rm.Rescue = func(cleanup.Row) error { return nil }
	m = moveTo(t, m, "spike")
	next, cmd := m.Update(keyRunes("r"))
	fm := next.(cleanupListModel)
	if cmd == nil || fm.rescueRow == nil || fm.rescueRow.Name != "spike" || !fm.done {
		t.Fatalf("cmd=%v rescueRow=%+v done=%v; want quit with the spike row", cmd, fm.rescueRow, fm.done)
	}
	if refetches != 0 {
		t.Errorf("r ran %d refetches; only F refetches", refetches)
	}
}

// TestRunRescueStopsSignalForwardingFirst checks the r key's hand-off stops
// the signal forwarding before rm.Rescue runs, and does nothing when no row
// was chosen. Why: the Bubble Tea program has finished by then, so forwarded
// signals would go to a dead p.Send and be dropped, and Ctrl-C at the
// rescue's y/N prompt (or SIGHUP during the copy) would do nothing.
func TestRunRescueStopsSignalForwardingFirst(t *testing.T) {
	var calls []string
	row := cleanup.Row{Name: "spike"}
	fm := cleanupListModel{rescueRow: &row}
	rm := RemoveFuncs{Rescue: func(r cleanup.Row) error {
		calls = append(calls, "rescue:"+r.Name)
		return nil
	}}
	if err := runRescue(fm, rm, func() { calls = append(calls, "stop") }); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "stop,rescue:spike" {
		t.Errorf("calls = %s, want stop before rescue", got)
	}

	calls = nil
	if err := runRescue(cleanupListModel{}, rm, func() { calls = append(calls, "stop") }); err != nil || len(calls) != 0 {
		t.Errorf("no row chosen: err=%v calls=%v, want nothing", err, calls)
	}
}

// TestRunCleanupListStopsForwardingThenRescues drives RunCleanupList through
// the program seam with a final model that holds a rescue row. What: the
// signal forwarding is stopped and then rm.Rescue runs with that row, in that
// order, and a final model without a row rescues nothing. Why:
// TestRunRescueStopsSignalForwardingFirst covers runRescue alone; this fails
// if RunCleanupList stops calling runRescue (the r key would do nothing) or
// calls the rescue before the stop (Ctrl-C at its prompt would be dropped).
func TestRunCleanupListStopsForwardingThenRescues(t *testing.T) {
	old := runCleanupProgram
	t.Cleanup(func() { runCleanupProgram = old })
	var calls []string
	row := cleanup.Row{Name: "spike"}
	runCleanupProgram = func(m cleanupListModel) (tea.Model, func(), error) {
		m.rescueRow = &row
		return m, func() { calls = append(calls, "stop") }, nil
	}
	rm := RemoveFuncs{Rescue: func(r cleanup.Row) error {
		calls = append(calls, "rescue:"+r.Name)
		return nil
	}}
	if err := RunCleanupList(cleanup.Report{}, themes.Theme{}, rm); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 2 || calls[0] != "stop" || calls[1] != "rescue:spike" {
		t.Errorf("calls = %v, want stop then rescue:spike", calls)
	}

	calls = nil
	runCleanupProgram = func(m cleanupListModel) (tea.Model, func(), error) {
		return m, func() { calls = append(calls, "stop") }, nil
	}
	if err := RunCleanupList(cleanup.Report{}, themes.Theme{}, rm); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "rescue:") {
			t.Errorf("no row chosen but rescue ran: %v", calls)
		}
	}
}

// TestGuardedRunReleasesSignalForwardingWhenTheProgramPanics: the program run
// panics. What: the signal forwarding is released before the panic leaves
// guardedRun (once), while a run that returns normally keeps it until the
// caller calls the returned stop. Why: the release used to be deferred only
// after runCleanupProgram returned, so a panic in the program left the
// process's SIGINT/SIGTERM forwarded to a dead program.
func TestGuardedRunReleasesSignalForwardingWhenTheProgramPanics(t *testing.T) {
	stops := 0
	stop := func() { stops++ }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		guardedRun(stop, func() (tea.Model, error) { panic("boom") })
	}()
	if stops != 1 {
		t.Errorf("stop called %d times after a panic, want 1", stops)
	}

	stops = 0
	_, got, err := guardedRun(stop, func() (tea.Model, error) { return nil, nil })
	if err != nil || stops != 0 {
		t.Fatalf("normal run: err %v, stops %d, want the forwarding kept", err, stops)
	}
	got()
	if stops != 1 {
		t.Errorf("returned stop did not release (stops %d)", stops)
	}
}

// TestRootRowShowsDriftBadge pins the drift badge on the root row: the
// root_drift soft label's detail — rootclaim.Drift's text, carried live by the
// classifier and never re-shaped here — is drawn on the root row as a TagWarn
// badge like the other risk tags, and a root row without the label (the root
// on its default branch, or no default known) carries no badge: no drift is
// invented without one (Review Focus 1).
func TestRootRowShowsDriftBadge(t *testing.T) {
	const drift = "root is on topic, not main"
	with := cleanupFixture()
	with.Rows[0].Reasons = append(with.Rows[0].Reasons,
		cleanup.Reason{Code: cleanup.ReasonRootDrift, Detail: drift})
	findRoot := func(m cleanupListModel) cleanupItem {
		t.Helper()
		for _, it := range m.items {
			if it.heading == "" && !it.newWorktree && it.row.Path == "/repo" {
				return it
			}
		}
		t.Fatal("no root row in the list")
		return cleanupItem{}
	}
	it := findRoot(newSizedCleanupList(t, with, 120, 40))
	if !slices.Contains(it.tags, cleanup.Tag{Text: drift, Severity: cleanup.TagWarn}) {
		t.Errorf("root row tags = %+v; want the drift badge %q as a warn tag", it.tags, drift)
	}
	warned := false
	for _, u := range rowUnits(it) {
		if u.warn && u.text == drift {
			warned = true
		}
	}
	if !warned {
		t.Errorf("row units = %+v; want %q drawn as a warn unit (the badge)", rowUnits(it), drift)
	}
	plain := findRoot(newSizedCleanupList(t, cleanupFixture(), 120, 40))
	for _, tg := range plain.tags {
		if tg.Text == drift || strings.HasPrefix(tg.Text, "root is ") {
			t.Errorf("root row tags = %+v; a row with no root_drift label got a drift badge", plain.tags)
		}
	}
}

// TestPickerHeaderShowsRootDrift pins the picker header's root-drift note:
// the `wt` launch header carries the drift line (the root row's root_drift
// label, rootclaim.Drift's text) whatever row the cursor is on — the design's
// "the same badge is shown in the `wt` launch header even when the user
// selected something else". The note is the picker header's alone: the manage
// list surfaces the drift as its root row's badge, and a header whose root
// row carries no label (root on its default, or no default known) shows no
// drift note at all.
func TestPickerHeaderShowsRootDrift(t *testing.T) {
	const drift = "root is on topic, not main"
	with := cleanupFixture()
	with.Rows[0].Reasons = append(with.Rows[0].Reasons,
		cleanup.Reason{Code: cleanup.ReasonRootDrift, Detail: drift})
	picker := newLaunchList(t, with, 120, 40)
	if h := picker.headerBlock(); !strings.Contains(h, drift) {
		t.Errorf("picker header = %q; want the root drift note %q", h, drift)
	}
	manage := newSizedCleanupList(t, with, 120, 40)
	if h := manage.headerBlock(); strings.Contains(h, drift) {
		t.Errorf("manage header = %q; the drift note is the picker header's (the manage list's root row carries the badge)", h)
	}
	for _, it := range picker.items {
		if it.heading != "" || it.newWorktree {
			continue
		}
		for _, tg := range it.tags {
			if tg.Text == drift {
				t.Errorf("picker row %q repeats the drift as a badge (tags %+v); the header says it once", it.row.Path, it.tags)
			}
		}
	}
	clean := newLaunchList(t, cleanupFixture(), 120, 40)
	if h := clean.headerBlock(); strings.Contains(h, drift) {
		t.Errorf("picker header = %q; want no drift note without the root_drift label", h)
	}
}

// procStart returns pid's start time in epoch seconds — what a claim records
// (docs/contracts/session-claims.md: equal to the real start within 5 s). A
// fixture "other session" must read live under that rule, so the claim names
// a real child's real start time, read from ps's lstart in the local zone,
// never a manufactured timestamp. (Shape: cmd/wt's launch_test.go and
// internal/cleanup's snapshot_test.go.)
func procStart(t *testing.T, pid int) int64 {
	t.Helper()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C") // English month names for the parse
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps lstart for %d: %v", pid, err)
	}
	start, err := time.ParseInLocation(time.ANSIC, strings.TrimSpace(string(out)), time.Local)
	if err != nil {
		t.Fatalf("parse lstart %q: %v", out, err)
	}
	return start.Unix()
}

// staleClaim claims path under repo's wt-sessions for a real child process
// through sessionclaim.Hold, then kills AND reaps the child: the claim file
// left behind is exactly what the claim contract's liveness rule calls stale
// — a real session that died without releasing its claim, carrying the
// child's real start time, never a manufactured one. The reap matters as
// much as the kill: a killed but unreaped child is still a live pid to
// kill(pid, 0), and its claim would read live. Returns the claim and its
// file's path. (Shape: cmd/wt's holderClaim in restore_root_test.go.)
func staleClaim(t *testing.T, repo, path, branch, agent string) (sessionclaim.Claim, string) {
	t.Helper()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	c := sessionclaim.Claim{PID: child.Process.Pid, Started: procStart(t, child.Process.Pid), Path: path, Branch: branch, Agent: agent}
	common := rmGit(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	release, err := sessionclaim.Hold(common, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := child.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	return c, filepath.Join(common, "wt-sessions", strconv.Itoa(c.PID))
}

// TestRefreshPrunesStaleClaims: the `F` refresh drops the stale session
// claims — on the manage list's re-read and on the picker's rows reload, the
// one key path both share. Why: a claim outlives its session whenever a
// process dies without releasing it, and the user-driven cleanup design
// prunes those exactly on refresh ("stale ones are pruned on refresh"), so
// the held labels a refresh shows name live sessions and leftovers stop
// piling up. The stale claim is a real dead process — spawned, claimed with
// its real start time through sessionclaim.Hold, then killed and reaped.
func TestRefreshPrunesStaleClaims(t *testing.T) {
	r := newRmRepo(t)
	h := r.harness()
	_, file := staleClaim(t, r.dir, r.dir, "main", "claude")
	m := h.list(120, 40)
	m.rm.Refetch = func() (cleanup.Report, error) { h.regather(); return h.report, nil }
	m = press(t, m, keyRunes("F"))
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the manage list's F left the stale claim %s behind (stat error %v)", file, err)
	}

	_, file = staleClaim(t, r.dir, r.dir, "main", "claude")
	p := newLaunchList(t, h.report, 120, 40)
	_, cmd := p.Update(keyRunes("F"))
	if cmd == nil {
		t.Fatal("the picker's F dispatched no refresh")
	}
	if _, ok := cmd().(refreshRowsChosenMsg); !ok {
		t.Fatal("the picker's F did not answer with refreshRowsChosenMsg")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the picker's F left the stale claim %s behind (stat error %v)", file, err)
	}
}

// TestOpeningTheListPrunesNothing: opening the list — building it, sizing it,
// rendering it, moving in it — drops no claim, not even a stale one. Why
// (the anti-goal): the prune belongs to the user's refresh and the
// fetch-first `--json` run alone ("opening the list does not fetch or
// prune", the user-driven cleanup design) — a list someone opens to look
// must never delete anything under them.
func TestOpeningTheListPrunesNothing(t *testing.T) {
	r := newRmRepo(t)
	h := r.harness()
	_, file := staleClaim(t, r.dir, r.dir, "main", "claude")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the fixture claim %s: %v", file, err)
	}
	m := h.list(120, 40)
	_ = m.View()
	m = cleanupSend(t, m, keyRunes("down"))
	_ = m.View()
	p := newLaunchList(t, h.report, 120, 40)
	_ = p.View()
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("opening the list dropped the stale claim %s: %v", file, err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("opening the list rewrote the stale claim %s", file)
	}
}

// restoreRootListFixture is a report whose root sits on a feature branch — the
// one row `R` restores — beside a linked worktree and a branch row (rows `R`
// explains instead of offering). The rows carry exactly the shape
// rootmove.Offered reads (Primary, Bare, Head, Branch, the default_branch
// blocker): Offered is the gate, and it reads the row alone, never git.
func restoreRootListFixture() cleanup.Report {
	return restoreRootReport(cleanup.Row{
		Kind: cleanup.KindWorktree, Name: "topic", Path: "/repo", Branch: "topic", Ref: "refs/heads/topic",
		Primary: true, Head: "0123456789abcdef0123456789abcdef01234567", Status: cleanup.StatusLessSure,
		Reasons: []cleanup.Reason{{Code: cleanup.ReasonRootDrift, Detail: "root is on topic, not main"}},
	})
}

// restoreRootReport is a report around one root row, with a linked worktree
// and a branch row beside it so a non-root refusal is always one move away.
func restoreRootReport(root cleanup.Row) cleanup.Report {
	return cleanup.Report{
		Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"}, Fetch: cleanup.FetchResult{OK: true, At: time.Now()}, GH: cleanup.GHOK,
		Rows: []cleanup.Row{
			root,
			{Kind: cleanup.KindWorktree, Name: "tidy", Path: "/repo/.worktrees/tidy", Branch: "tidy", Ref: "refs/heads/tidy", Status: cleanup.StatusProven},
			{Kind: cleanup.KindBranch, Name: "lone", Branch: "lone", Status: cleanup.StatusLessSure},
		},
	}
}

// TestCleanupListRestoreRootKeyHandsOff checks `R` on the root row a restore
// is offered for quits the list and flags the restore-root hand-off, touching
// nothing else (the rescue row stays unset). Why: restore-root asks on the
// real terminal, so the list must leave the alt screen first — the flag is
// what runRestoreRoot hands to RemoveFuncs.RestoreRoot after the screen.
func TestCleanupListRestoreRootKeyHandsOff(t *testing.T) {
	m := newSizedCleanupList(t, restoreRootListFixture(), 100, 30)
	m.rm.RestoreRoot = func() error { return nil }
	m = moveTo(t, m, "topic")
	next, cmd := m.Update(keyRunes("R"))
	fm := next.(cleanupListModel)
	if cmd == nil || !fm.restoreRoot || !fm.done {
		t.Fatalf("cmd=%v restoreRoot=%v done=%v; want quit with the restore flagged", cmd, fm.restoreRoot, fm.done)
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Errorf("R's command = %T, want tea.QuitMsg (the alt screen must close first)", cmd())
	}
	if fm.rescueRow != nil {
		t.Errorf("R set rescueRow %+v; the restore-root hand-off must not touch rescue", fm.rescueRow)
	}
}

// TestCleanupListRestoreRootExplainsWhenNotOffered checks `R` on a row
// rootmove.Offered refuses stays in the list and shows that gate's own line
// byte-for-byte on the status line — one case per refusal shape (a linked
// worktree and a branch row are not the root; the root on the default is
// already there; a detached root has no branch to move; an unborn root has no
// commit to move). Why: a key that does nothing, silently, reads as broken,
// and re-wording Offered's line here would let the two drift apart.
func TestCleanupListRestoreRootExplainsWhenNotOffered(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		name   string
		report cleanup.Report
		row    string
		want   string
	}{
		{"a linked worktree is not the root", restoreRootListFixture(), "tidy", "only the root can be restored"},
		{"a branch row is not the root", restoreRootListFixture(), "lone", "only the root can be restored"},
		{"the root on the default is already there", restoreRootReport(cleanup.Row{
			Kind: cleanup.KindWorktree, Name: "main", Path: "/repo", Branch: "main", Ref: "refs/heads/main",
			Primary: true, Head: sha, Status: cleanup.StatusInUse,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockPrimary}, {Code: cleanup.BlockDefaultBranch, Detail: "main"}}}),
			"main", "root is already on main"},
		{"a detached root has no branch to move", restoreRootReport(cleanup.Row{
			Kind: cleanup.KindWorktree, Name: "repo", Path: "/repo", Primary: true, Head: sha,
			Status:  cleanup.StatusLessSure,
			Reasons: []cleanup.Reason{{Code: cleanup.ReasonRootDrift, Detail: "root is detached"}}}),
			"repo", "the root is detached — there is no branch to move"},
		{"an unborn root has no commit to move", restoreRootReport(cleanup.Row{
			Kind: cleanup.KindWorktree, Name: "fresh", Path: "/repo", Branch: "fresh", Ref: "refs/heads/fresh",
			Primary: true, Head: "0000000000000000000000000000000000000000", Status: cleanup.StatusLessSure,
			Reasons: []cleanup.Reason{{Code: cleanup.ReasonRootDrift, Detail: "root is on fresh, not main"}}}),
			"fresh", "the root's branch has no commits yet"},
	} {
		restored := false
		m := newSizedCleanupList(t, c.report, 100, 30)
		m.rm.RestoreRoot = func() error { restored = true; return nil }
		m = moveTo(t, m, c.row)
		next, cmd := m.Update(keyRunes("R"))
		fm := next.(cleanupListModel)
		if fm.restoreRoot || fm.done || cmd != nil || restored {
			t.Errorf("%s: restoreRoot=%v done=%v cmd=%v restored=%v; want to stay in the list", c.name, fm.restoreRoot, fm.done, cmd, restored)
		}
		if fm.status != c.want {
			t.Errorf("%s: status = %q, want Offered's line byte-for-byte %q", c.name, fm.status, c.want)
		}
	}
}

// TestCleanupListRestoreRootNotWired checks `R` while RemoveFuncs.RestoreRoot
// is still nil (the pre-wiring gap cmd/wt closes): on the row a restore is
// offered for it says one line and does nothing. Why: the gap must read as a
// told gap, not a broken key — no panic, no silent no-op.
func TestCleanupListRestoreRootNotWired(t *testing.T) {
	m := newSizedCleanupList(t, restoreRootListFixture(), 100, 30)
	m = moveTo(t, m, "topic")
	next, cmd := m.Update(keyRunes("R"))
	fm := next.(cleanupListModel)
	if fm.restoreRoot || fm.done || cmd != nil {
		t.Fatalf("restoreRoot=%v done=%v cmd=%v; want nothing to happen", fm.restoreRoot, fm.done, cmd)
	}
	if fm.status != "restore root is not wired here" {
		t.Errorf("status = %q, want %q", fm.status, "restore root is not wired here")
	}
}

// TestRunCleanupListRestoresRootAfterTheScreen drives RunCleanupList through
// the program seam with a final model whose `R` flag is set. What: the signal
// forwarding is stopped, then RemoveFuncs.RestoreRoot runs — and it runs only
// once the alt screen is gone (the seam's return). Why: the restore asks on
// the real terminal; TestRunRestoreRootStopsSignalForwardingFirst covers
// runRestoreRoot alone, this fails if RunCleanupList stops calling it or
// calls it while the screen is still up.
func TestRunCleanupListRestoresRootAfterTheScreen(t *testing.T) {
	old := runCleanupProgram
	t.Cleanup(func() { runCleanupProgram = old })
	var calls []string
	screenGone, sawScreenGone := false, false
	runCleanupProgram = func(m cleanupListModel) (tea.Model, func(), error) {
		m.restoreRoot = true
		defer func() { screenGone = true }() // the alt screen closes as the program returns
		return m, func() { calls = append(calls, "stop") }, nil
	}
	rm := RemoveFuncs{RestoreRoot: func() error {
		calls = append(calls, "restore")
		sawScreenGone = screenGone
		return nil
	}}
	if err := RunCleanupList(cleanup.Report{}, themes.Theme{}, rm); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 2 || calls[0] != "stop" || calls[1] != "restore" {
		t.Errorf("calls = %v, want stop then restore", calls)
	}
	if !sawScreenGone {
		t.Error("RestoreRoot ran while the alt screen was still up")
	}

	calls = nil
	runCleanupProgram = func(m cleanupListModel) (tea.Model, func(), error) {
		return m, func() { calls = append(calls, "stop") }, nil
	}
	if err := RunCleanupList(cleanup.Report{}, themes.Theme{}, rm); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c == "restore" {
			t.Errorf("no R chosen but restore ran: %v", calls)
		}
	}
}

// TestRunRestoreRootStopsSignalForwardingFirst checks the R hand-off stops
// the signal forwarding before RemoveFuncs.RestoreRoot runs, and does nothing
// when no restore was flagged or no func is wired. Why: the Bubble Tea program
// has finished by then, so a forwarded signal would go to a dead p.Send and be
// dropped, and Ctrl-C at the restore's prompt (or SIGHUP during the move)
// would do nothing.
func TestRunRestoreRootStopsSignalForwardingFirst(t *testing.T) {
	var calls []string
	fm := cleanupListModel{restoreRoot: true}
	rm := RemoveFuncs{RestoreRoot: func() error {
		calls = append(calls, "restore")
		return nil
	}}
	if err := runRestoreRoot(fm, rm, func() { calls = append(calls, "stop") }); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "stop,restore" {
		t.Errorf("calls = %s, want stop before restore", got)
	}

	calls = nil
	if err := runRestoreRoot(cleanupListModel{}, rm, func() { calls = append(calls, "stop") }); err != nil || len(calls) != 0 {
		t.Errorf("no restore flagged: err=%v calls=%v, want nothing", err, calls)
	}
	if err := runRestoreRoot(fm, RemoveFuncs{}, func() { calls = append(calls, "stop") }); err != nil || len(calls) != 0 {
		t.Errorf("no RestoreRoot wired: err=%v calls=%v, want nothing", err, calls)
	}
}

// TestCleanupLockedOnlyForHardStops pins the lock's exact boundary: only a
// hard stop locks a row — primary, current, locked, mid-operation, nested and
// missing dir, each row carrying the one real code its name maps to (the
// classifier's in_use/HardStop derivation is pinned in the cleanup package) —
// while the floors (default branch, held, root drift) never lock: they read
// less sure and the user may tick past them, whereupon the engine asks for
// --accept-loss's typed confirmation. Why: warn-but-never-prevent — a softer
// signal must never block the user's keys. no_default_branch is this
// builder's decision for a code the brief leaves out: a "no known default"
// uncertainty is a floor, not a hard stop, and it is pinned on that side.
func TestCleanupLockedOnlyForHardStops(t *testing.T) {
	hardStops := []struct {
		name string
		row  cleanup.Row
	}{
		{"primary", cleanup.Row{Kind: cleanup.KindWorktree, Name: "primary", Path: "/repo", Branch: "main", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockPrimary, Detail: "/repo"}}}},
		{"current", cleanup.Row{Kind: cleanup.KindWorktree, Name: "current", Path: "/repo/.worktrees/current", Branch: "current", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockCurrent, Detail: "/repo/.worktrees/current"}}}},
		{"locked", cleanup.Row{Kind: cleanup.KindWorktree, Name: "locked", Path: "/repo/.worktrees/locked", Branch: "locked", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockLocked, Detail: "/repo/.worktrees/locked"}}}},
		{"mid-operation", cleanup.Row{Kind: cleanup.KindWorktree, Name: "mid-operation", Path: "/repo/.worktrees/mid-operation", Branch: "mid-operation", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockOperation, Detail: "rebase-merge"}}}},
		{"nested", cleanup.Row{Kind: cleanup.KindWorktree, Name: "nested", Path: "/repo/.worktrees/nested", Branch: "nested", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockNested, Detail: "/repo/.worktrees/nested/.worktrees/inner"}}}},
		// missing dir: the classifier's shape when a worktree's state could
		// not be read — its directory is gone (the engine refuses it
		// whatever is approved: "could not read the worktree's state").
		{"missing dir", cleanup.Row{Kind: cleanup.KindWorktree, Name: "missing-dir", Path: "/repo/.worktrees/missing-dir", Branch: "missing-dir",
			Status: cleanup.StatusLessSure, Unknown: true,
			Reasons: []cleanup.Reason{{Code: cleanup.ReasonGatesUnavailable, Detail: "cleanup: worktree /repo/.worktrees/missing-dir is prunable: its git link is broken, so its state cannot be read"}},
			Losses:  cleanup.Losses{GatesUnknown: true}}},
	}
	for _, tc := range hardStops {
		if !cleanupLocked(tc.row) {
			t.Errorf("%s is a hard stop but does not lock the row", tc.name)
		}
	}
	floors := []struct {
		name string
		row  cleanup.Row
	}{
		{"default branch", cleanup.Row{Kind: cleanup.KindWorktree, Name: "default-branch", Path: "/repo/.worktrees/default-branch", Branch: "main", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockDefaultBranch, Detail: "main"}}}},
		// BlockHeld is ReasonHeld's alias (worktrees.go): the same held
		// code in the legacy blocker shape the held test seam fakes.
		{"held", cleanup.Row{Kind: cleanup.KindWorktree, Name: "held", Path: "/repo/.worktrees/held", Branch: "held", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockHeld, Detail: "/repo/.worktrees/held"}}}},
		{"root drift", cleanup.Row{Kind: cleanup.KindWorktree, Name: "root-drift", Path: "/repo", Branch: "topic", Status: cleanup.StatusLessSure,
			Reasons: []cleanup.Reason{{Code: cleanup.ReasonRootDrift, Detail: "root is on topic, not main"}}}},
		{"no default branch", cleanup.Row{Kind: cleanup.KindWorktree, Name: "no-default", Path: "/repo/.worktrees/no-default", Branch: "no-default",
			Status: cleanup.StatusLessSure, Unknown: true,
			Reasons: []cleanup.Reason{{Code: cleanup.ReasonNoDefaultBranch, Detail: "no default branch, so no base to compare with"}}}},
	}
	for _, tc := range floors {
		if cleanupLocked(tc.row) {
			t.Errorf("%s is a floor but locks the row", tc.name)
		}
	}
}

// TestCleanupListRemoveOnCwdOrAncestorIsRefused pins Review Focus 5's list
// half: a remove targeting the cwd's own row or an ancestor of the cwd is
// refused with one pinned explanation on every removal path the list has —
// `x`, `d`, `o` and enter's ticked batch — and previews nothing. Refusal,
// never a silent skip: a ticked row that cannot be removed refuses the whole
// selection with its reason rather than quietly dropping the row and
// previewing the rest (the engine refuses such a target too; here the list
// refuses before the engine is called).
func TestCleanupListRemoveOnCwdOrAncestorIsRefused(t *testing.T) {
	r := cleanupFixture()
	// The caller's directory sits inside "inner": "inner" is the cwd's own
	// row (cleanup marks every worktree containing the cwd Current, and the
	// "you are here" sentence rides the innermost one), while "deep" — which
	// contains inner's path — and "main" (the root, the primary checkout)
	// are ancestors of the cwd: removing either deletes the caller's
	// directory with it.
	byName := map[string]cleanup.Row{}
	for _, row := range r.Rows {
		byName[cleanup.DisplayName(row)] = row
	}
	inner := cleanup.Row{Kind: cleanup.KindWorktree, Name: "inner", Path: "/repo/.worktrees/deep/inner", Branch: "inner",
		Status: cleanup.StatusInUse, Blockers: []cleanup.Reason{{Code: cleanup.BlockCurrent, Detail: "/repo/.worktrees/deep/inner"}}}
	r.Rows = append(r.Rows, inner)
	byName["inner"] = inner

	for _, name := range []string{"inner", "deep", "main"} {
		row := byName[name]
		want := "cannot be removed: " + cleanup.OneLine(cleanup.RowText(r, row))
		for _, key := range []tea.KeyMsg{keyRunes("x"), keyRunes("d"), keyRunes("o")} {
			m := moveTo(t, newSizedCleanupList(t, r, 120, 50), name)
			got, fm := runPreview(t, m, key)
			if len(got) != 0 || fm.phase != cleanupPhaseList || fm.status != want {
				t.Errorf("%s on %s: previewed %v, phase %v, status %q, want no preview and %q", key.String(), name, got, fm.phase, fm.status, want)
			}
		}
		// enter's ticked batch: the keys can never tick a locked row, so the
		// tick is set straight here (a stale tick after a re-read, or any
		// path that leaves one). With it ticked — alone and beside a proven
		// row — enter refuses the whole selection with the same explanation
		// and previews nothing.
		for _, withProven := range []bool{false, true} {
			m := setTicks(t, newSizedCleanupList(t, r, 120, 50))
			for i, it := range m.items {
				if it.heading == "" && cleanup.DisplayName(it.row) == name {
					m.ticked[i] = true
				}
			}
			if withProven {
				m = cleanupSend(t, moveTo(t, m, "full-branch-name-paths"), spaceKey)
			}
			got, fm := runPreview(t, m, enterKey)
			if len(got) != 0 || fm.phase != cleanupPhaseList || fm.status != want {
				t.Errorf("enter with %s ticked (proven row alongside %v): previewed %v, phase %v, status %q, want no preview and %q",
					name, withProven, got, fm.phase, fm.status, want)
			}
		}
	}
}

// TestCleanupListTickedRowsReturnsTheRawTickSet pins Ruling R506b's seam
// contract: tickedRows is the RAW tick set — every ticked row in list
// order, locked rows included. The filter it once had dropped a ticked row
// from every later count and batch without a word: a silent skip through
// the back door, the exact anti-pattern this task exists to kill. A locked
// row in the set is refused by name in startRemoval, whole-selection,
// before anything is previewed — refusal, never a row quietly decided away
// while the batch proceeds partial.
func TestCleanupListTickedRowsReturnsTheRawTickSet(t *testing.T) {
	r := cleanupFixture()
	m := newSizedCleanupList(t, r, 120, 50)
	// The mixed set, built through the tick seam: "deep" is locked in the
	// current report and still carries the tick it gathered while clean
	// (set straight here — space never ticks a locked row), beside the
	// ordinary proven row the user ticked with the keys.
	m = setTicks(t, m, "full-branch-name-paths")
	for i, it := range m.items {
		if it.heading == "" && cleanup.DisplayName(it.row) == "deep" {
			m.ticked[i] = true
		}
	}
	var names []string
	for _, row := range m.tickedRows() {
		names = append(names, cleanup.DisplayName(row))
	}
	if strings.Join(names, ",") != "full-branch-name-paths,deep" {
		t.Errorf("tickedRows() = %v, want the raw tick set [full-branch-name-paths deep]: every ticked row, locked included, in list order", names)
	}
}

// TestCleanupListEnterRefusesARowLockedInTheCurrentReport pins the race
// Ruling R506b names (Refusal, never silent skip): a row can gain a hard
// stop between the tick and the act — an F-refresh re-read can harden the
// row it re-classifies — and the tick it gathered while clean is still on
// it in the current report. enter then refuses the WHOLE selection with
// the row keys' explanation, naming that row, and removes nothing: the
// batch must not proceed partial, previewing only the rows beside it. The
// post-refresh state is built here directly through the fixture and the
// tick seam (space never ticks a locked row, so the tick is set straight).
func TestCleanupListEnterRefusesARowLockedInTheCurrentReport(t *testing.T) {
	r := cleanupFixture()
	// "hardened" as the re-read left it: mid-operation (a rebase-merge in
	// progress) — a hard stop gained since the user's tick landed.
	hardened := cleanup.Row{Kind: cleanup.KindWorktree, Name: "hardened", Path: "/repo/.worktrees/hardened", Branch: "hardened",
		Status: cleanup.StatusLessSure, Blockers: []cleanup.Reason{{Code: cleanup.BlockOperation, Detail: "rebase-merge"}}}
	r.Rows = append(r.Rows, hardened)
	m := setTicks(t, newSizedCleanupList(t, r, 120, 50), "full-branch-name-paths")
	for i, it := range m.items {
		if it.heading == "" && cleanup.DisplayName(it.row) == "hardened" {
			m.ticked[i] = true
		}
	}
	removed := false
	m.rm.Run = func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
		removed = true
		return nil
	}
	got, fm := runPreview(t, m, enterKey)
	want := "cannot be removed: " + cleanup.OneLine(cleanup.RowText(r, hardened))
	if len(got) != 0 || removed || fm.phase != cleanupPhaseList || fm.status != want {
		t.Errorf("enter with a hardened tick: previewed %v (removed %v), phase %v, status %q; want nothing previewed or run, the list and %q",
			got, removed, fm.phase, fm.status, want)
	}
	// The batch source is the raw set too: both ticks are still in it,
	// the hardened row included — nothing was silently decided away.
	if rows := fm.tickedRows(); len(rows) != 2 {
		t.Errorf("tickedRows() holds %d rows after the refusal, want the raw 2 (the hardened row included)", len(rows))
	}
}
