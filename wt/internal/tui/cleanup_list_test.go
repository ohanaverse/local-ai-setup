package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
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

// moveTo moves the cursor down until the highlighted row has the given
// display name, failing when it is never reached.
func moveTo(t *testing.T, m cleanupListModel, name string) cleanupListModel {
	t.Helper()
	for i := 0; i < 50; i++ {
		if row, ok := m.selectedRow(); ok && cleanup.DisplayName(row) == name {
			return m
		}
		m = cleanupSend(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	t.Fatalf("never reached row %q", name)
	return m
}

// TestCleanupListGroupOrder pins that the list shows the four group headings
// in the spec's order (PROVEN, LESS SURE, UNIQUE WORK, IN USE) whatever order
// the report's rows came in: the order is how the user reads "safe first".
func TestCleanupListGroupOrder(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	view := m.View()
	last := -1
	for _, st := range cleanup.GroupOrder {
		h := cleanup.Heading(st)
		i := strings.Index(view, h)
		if i < 0 {
			t.Fatalf("heading %q missing from view:\n%s", h, view)
		}
		if i <= last {
			t.Errorf("heading %q out of order in view:\n%s", h, view)
		}
		last = i
	}
	// Rows sit under their own heading: a proven row before LESS SURE.
	if strings.Index(view, "fix-default-guard") > strings.Index(view, cleanup.Heading(cleanup.StatusLessSure)) {
		t.Errorf("proven row drawn below the LESS SURE heading:\n%s", view)
	}
}

// TestCleanupListDefaultTicksOnlyProven pins the default ticks: exactly the
// Tickable (proven, unblocked) rows start ticked, local and remote alike, and
// nothing else. A less-sure or unique-work row ticked by default is a
// one-keystroke loss of work once enter removes things.
func TestCleanupListDefaultTicksOnlyProven(t *testing.T) {
	r := cleanupFixture()
	m := newSizedCleanupList(t, r, 120, 40)
	got := strings.Join(m.tickedNames(), ",")
	want := "full-branch-name-paths,fix-default-guard,origin/merged-remote"
	if got != want {
		t.Errorf("default ticks = %q, want %q", got, want)
	}
	for _, row := range r.Rows {
		ticked := false
		for _, n := range m.tickedNames() {
			ticked = ticked || n == cleanup.DisplayName(row)
		}
		if ticked != row.Tickable() {
			t.Errorf("%s: ticked=%v, Tickable=%v", cleanup.DisplayName(row), ticked, row.Tickable())
		}
	}
	if !strings.Contains(m.View(), "[x] worktree  full-branch-name") {
		t.Errorf("proven row not drawn ticked:\n%s", m.View())
	}
}

// TestCleanupListInUseRowsNotToggleable pins that an in-use row is drawn
// [-] and that neither space nor `a` can tick it: it is the directory the
// user is in, the primary checkout, a locked worktree.
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
}

// TestCleanupListRemoteNotProvenIsLocked pins that a remote row that is not
// proven is [-] and cannot be ticked: deleting an origin branch is shared
// state, and only a merged PR backing it may put it up for removal.
func TestCleanupListRemoteNotProvenIsLocked(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	m = moveTo(t, m, "origin/old-remote")
	m = cleanupSend(t, m, spaceKey)
	for _, n := range m.tickedNames() {
		if n == "origin/old-remote" {
			t.Errorf("remote row that is not proven was ticked")
		}
	}
	if !strings.Contains(m.View(), "[-] remote    origin/old-remote") {
		t.Errorf("remote not-proven row not drawn [-]:\n%s", m.View())
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
	if want := "full-branch-name-paths,fix-default-guard,origin/merged-remote"; got != want {
		t.Errorf("after a: ticked = %q, want %q", got, want)
	}
}

// TestCleanupListEnterIsViewOnly pins that enter in PR A removes nothing: it
// quits with tea.Quit (no other command, so nothing that could run git) and
// leaves the "view only" sentence naming what would be removed.
func TestCleanupListEnterIsViewOnly(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 120, 40)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	fm := next.(cleanupListModel)
	if cmd == nil {
		t.Fatal("enter returned no command; want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("enter's command produced %T, want tea.QuitMsg only", cmd())
	}
	want := "view only — nothing is removed; would remove 3 items: worktree full-branch-name-paths, branch fix-default-guard, remote origin/merged-remote"
	if fm.exitMessage() != want {
		t.Errorf("exit message = %q\nwant          %q", fm.exitMessage(), want)
	}
	if fm.View() != "" {
		t.Errorf("view after enter should be empty (the message prints after the alt screen), got %q", fm.View())
	}

	// Nothing ticked says so.
	m = newSizedCleanupList(t, cleanupFixture(), 120, 40)
	for _, n := range []string{"full-branch-name-paths", "fix-default-guard", "origin/merged-remote"} {
		m = moveTo(t, m, n)
		m = cleanupSend(t, m, spaceKey)
	}
	fm = cleanupSend(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if fm.exitMessage() != "view only — nothing is removed; nothing ticked" {
		t.Errorf("exit message with nothing ticked = %q", fm.exitMessage())
	}

	// q quits with no message.
	next, cmd = newSizedCleanupList(t, cleanupFixture(), 120, 40).Update(keyRunes("q"))
	if cmd == nil || next.(cleanupListModel).exitMessage() != "" {
		t.Errorf("q: cmd=%v message=%q; want tea.Quit and no message", cmd, next.(cleanupListModel).exitMessage())
	}
}

// TestCleanupListNarrowWidthKeepsEvidence pins the width rule at 80
// columns with long branch names: no line is wider than the terminal, the
// name column is what gets cut, every row's evidence stays whole, and the
// detail line under the list carries the highlighted row's full sentence.
func TestCleanupListNarrowWidthKeepsEvidence(t *testing.T) {
	r := cleanupFixture()
	long := "feature/an-extremely-long-branch-name-that-goes-on-and-on-forever"
	r.Rows = append(r.Rows, cleanup.Row{Kind: cleanup.KindBranch, Name: long, Branch: long,
		Status: cleanup.StatusProven, Evidence: cleanup.EvMergeAddsNothing})
	r.Rows[2].Name = "worktree-" + long
	m := newSizedCleanupList(t, r, 80, 24)
	view := m.View()
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line %d is %d columns wide: %q", i, w, line)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "[x]") && strings.Contains(line, long) {
			t.Errorf("long name was not truncated in its row at 80 columns: %q", line)
		}
	}
	for _, ev := range []string{"squash merged, content is in origin/main", "squash merged, PR #517", "ancestor of origin/main"} {
		if !strings.Contains(view, ev) {
			t.Errorf("evidence %q cut at 80 columns:\n%s", ev, view)
		}
	}
	// The Unknown row's detail says why, whole, in the detail line.
	m = moveTo(t, m, "who-knows")
	view = m.View()
	if !strings.Contains(strings.Join(strings.Fields(view), " "), "PR lookup failed (gh exited 1)") {
		t.Errorf("unknown row's reason missing from the view:\n%s", view)
	}
	if h := lipgloss.Height(view); h > 24 {
		t.Errorf("view is %d lines tall at 24", h)
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
