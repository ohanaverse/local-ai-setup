package tui

import (
	"fmt"
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

	// Each clause of the lock on its own: an in_use row with no blockers,
	// and a blocker on a row that is not in_use. Both are [-] and stay
	// unticked through space and `a`.
	r := cleanupFixture()
	r.Rows = append(r.Rows,
		cleanup.Row{Kind: cleanup.KindWorktree, Name: "inuse-noblock", Path: "/repo/.worktrees/inuse-noblock", Status: cleanup.StatusInUse},
		cleanup.Row{Kind: cleanup.KindBranch, Name: "blocked-lesssure", Branch: "blocked-lesssure", Status: cleanup.StatusLessSure,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockHeld}}},
		cleanup.Row{Kind: cleanup.KindBranch, Name: "blocked-proven", Branch: "blocked-proven", Status: cleanup.StatusProven,
			Evidence: cleanup.EvAncestor, Blockers: []cleanup.Reason{{Code: cleanup.BlockBaseOfOpenPR}}},
	)
	for _, name := range []string{"inuse-noblock", "blocked-lesssure", "blocked-proven"} {
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

// TestCleanupListRemoteRowsAreLocked pins that every remote row, proven or
// not, is [-] and cannot be ticked by space or `a`: enter now removes what is
// ticked, and wt does not delete origin branches (shared state; plan call 9).
func TestCleanupListRemoteRowsAreLocked(t *testing.T) {
	for _, name := range []string{"origin/old-remote", "origin/merged-remote"} {
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
		if !strings.Contains(m.View(), "wt does not delete origin branches") {
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
	if !strings.Contains(view, cleanup.Heading(cleanup.StatusProven)) || !strings.Contains(view, "> [x] worktree") {
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
	for _, want := range []string{"wt cleanup", "q quit", "base:  origin/main", "── main ─", "contains 30 other worktrees"} {
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
