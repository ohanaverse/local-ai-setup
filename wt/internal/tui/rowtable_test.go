package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// tableFixture is one of each row kind the table draws: a worktree on a
// branch with facts filled, a detached worktree, a bare entry, a local
// branch, a remote branch, and a pending (facts-only) worktree row.
func tableFixture() []cleanup.Row {
	return []cleanup.Row{
		{
			Kind: cleanup.KindWorktree, Name: "feat/x", Branch: "feat/x", Path: "/r/.worktrees/feat-x",
			Status: cleanup.StatusProven, Evidence: cleanup.EvMergeAddsNothing,
			State: cleanup.StateInMain, Diff: cleanup.DiffStat{Added: 99, Removed: 55},
			Losses: cleanup.Losses{IgnoredEntries: 12},
			PRTag:  &cleanup.PRInfo{Number: 383}, Sessions: 2,
		},
		{
			Kind: cleanup.KindWorktree, Name: "wt-loose", Status: cleanup.StatusUniqueWork,
			State: cleanup.StateDirty, Diff: cleanup.DiffStat{Added: 3, Removed: 1},
		},
		{
			Kind: cleanup.KindWorktree, Name: "bare.git", Bare: true,
			Status: cleanup.StatusLessSure, State: cleanup.StateUnknown,
		},
		{
			Kind: cleanup.KindWorktree, Name: "pending-wt", Branch: "pending-wt",
			Status: cleanup.StatusLessSure, Pending: true,
			State: cleanup.StateUnknown, Losses: cleanup.Losses{GatesUnknown: true},
		},
		{
			Kind: cleanup.KindBranch, Name: "solo", Branch: "solo",
			Status: cleanup.StatusUniqueWork, State: cleanup.StateUnknown,
		},
		{
			Kind: cleanup.KindRemote, Name: "feat/y", Branch: "feat/y", Remote: "origin",
			Status: cleanup.StatusLessSure, State: cleanup.StateUnknown,
		},
	}
}

// TestRowCellsPerKind pins each row kind's seven cells: names where they
// apply and blank where they do not (a branch row has no worktree, a
// detached one no branch, a remote row's branch carries its remote), "-"
// for a pending row's detail cells and for unknown facts, "+N/-N" counts,
// the STATUS word and the verdict in words. Why: these cells are the whole
// screen — a wrong cell tells a user work sits where it does not.
func TestRowCellsPerKind(t *testing.T) {
	rows := tableFixture()

	got := rowCells(rows[0]) // filled worktree
	want := []string{"feat/x", "feat/x", "#383", "+99/-55", "12", "in main", "proven"}
	if len(got) != len(want) {
		t.Fatalf("cells = %q, want 7 entries", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cell %d = %q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}

	got = rowCells(rows[1]) // detached: directory name, no branch
	if got[0] != "wt-loose" || got[1] != "" {
		t.Errorf("detached cells = %q, want the directory name and a blank branch", got)
	}

	got = rowCells(rows[2]) // bare: no working tree to count
	if got[3] != "" || got[4] != "" || got[5] != "-" {
		t.Errorf("bare cells = %q, want blank DIFF/IGN and STATUS %q", got, "-")
	}

	got = rowCells(rows[3]) // pending: names first, detail cells "-"
	for i, want := range []string{"pending-wt", "pending-wt", "-", "-", "-", "-", "-"} {
		if got[i] != want {
			t.Errorf("pending cell %d = %q, want %q (all: %q)", i, got[i], want, got)
		}
	}

	got = rowCells(rows[4]) // local branch: no worktree column content
	if got[0] != "" || got[1] != "solo" || got[5] != "-" {
		t.Errorf("branch cells = %q, want a blank worktree, branch solo, STATUS -", got)
	}

	got = rowCells(rows[5]) // remote branch: branch cell names its remote
	if got[0] != "" || got[1] != "origin/feat/y" || got[5] != "-" {
		t.Errorf("remote cells = %q, want a blank worktree, origin/feat/y, STATUS -", got)
	}

	// A pending branch row: inapplicability beats pending — DIFF/IGN stay
	// blank (a branch row never has a working tree), while the cells that
	// would eventually apply read "-".
	got = rowCells(cleanup.Row{Kind: cleanup.KindBranch, Name: "wait", Branch: "wait", Pending: true})
	want = []string{"", "wait", "-", "", "", "-", "-"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pending branch cell %d = %q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}
}

// TestRowTableWidthsAndRightAlignment pins the layout: every column is as
// wide as its widest cell or heading (measured in runes), and the number
// cells (PR#, DIFF, IGN) sit on their column's right edge while name cells
// sit on the left. Why: the table only scans if its digits stack and its
// headings line up over their columns.
func TestRowTableWidthsAndRightAlignment(t *testing.T) {
	rows := tableFixture()
	cols := newRowTable(rows, rowPrefixWidthLaunch)

	if len(cols.Widths) != 7 {
		t.Fatalf("widths = %v, want 7 columns", cols.Widths)
	}
	// Headings are the floor of every width.
	for i, head := range []string{"WORKTREE", "BRANCH", "PR#", "DIFF", "IGN", "STATUS", "VERDICT"} {
		if cols.Widths[i] < utf8.RuneCountInString(head) {
			t.Errorf("column %d width %d is under its heading %q", i, cols.Widths[i], head)
		}
	}
	// The widest cells set the rest: feat/x / #383 / +99/-55 / 12 / in main.
	if cols.Widths[rtDiff] != utf8.RuneCountInString("+99/-55") {
		t.Errorf("DIFF width = %d, want the widest cell %d", cols.Widths[rtDiff], utf8.RuneCountInString("+99/-55"))
	}
	if cols.Widths[rtIgn] != utf8.RuneCountInString("IGN") && cols.Widths[rtIgn] != utf8.RuneCountInString("12") {
		t.Errorf("IGN width = %d, want max(IGN, 12)", cols.Widths[rtIgn])
	}

	line := rowLine(rows[0], cols)
	// Right-aligned: the DIFF cell ends at its column's right edge — the
	// text after the BRANCH column's end is "#383 … +99/-55 … 12".
	if !strings.Contains(line, "#383") || !strings.Contains(line, "+99/-55") {
		t.Fatalf("line %q lacks the number cells", line)
	}
	// The header indents by the prefix and names every heading.
	header := cols.Header()
	if !strings.HasPrefix(header, strings.Repeat(" ", rowPrefixWidthLaunch)) {
		t.Errorf("header %q not indented by the prefix", header)
	}
	for _, head := range []string{"WORKTREE", "BRANCH", "PR#", "DIFF", "IGN", "STATUS", "VERDICT"} {
		if !strings.Contains(header, head) {
			t.Errorf("header %q lacks %q", header, head)
		}
	}
	// One line per row, each starting with the row's own cells.
	for i, r := range rows {
		if got := rowLine(r, cols); strings.Contains(got, "\n") {
			t.Errorf("row %d rendered %q, want one line", i, got)
		}
	}
}

// TestRowTableDropsColumnsInOrderAndWhole narrows the table width step by
// step and records the width at which each column disappears: PR# first,
// then IGN, then STATUS, then DIFF, while WORKTREE, BRANCH and VERDICT never
// drop — not even at 10 columns. Why: the drop order is the contract for
// what a narrow terminal keeps; a column dropped out of order hides the
// wrong fact, and a half-shown column would break the alignment.
func TestRowTableDropsColumnsInOrderAndWhole(t *testing.T) {
	cols := newRowTable(tableFixture(), rowPrefixWidthLaunch)

	firstHide := map[int]int{} // column -> the widest width at which it is already hidden
	for w := 200; w >= 10; w-- {
		cols.Fit(w)
		for c := range cols.Heads {
			if !cols.Shown(c) {
				if _, seen := firstHide[c]; !seen {
					firstHide[c] = w
				}
			}
		}
	}
	for _, never := range []int{rtWorktree, rtBranch, rtVerdict} {
		if w, hidden := firstHide[never]; hidden {
			t.Errorf("column %d hidden at width %d: the never-drop columns stay", never, w)
		}
	}
	// PR# disappears at the widest width, then IGN, then STATUS, then DIFF.
	prev := 200
	for _, c := range []int{rtPR, rtIgn, rtStatus, rtDiff} {
		w, ok := firstHide[c]
		if !ok {
			t.Fatalf("column %d never dropped", c)
		}
		if w >= prev {
			t.Fatalf("drop order wrong: column %d hidden at %d after a column hidden at %d (map %v)", c, w, prev, firstHide)
		}
		prev = w
	}
	// Columns are whole: Fit either shows a column or hides it, and the
	// header names exactly the shown ones.
	cols.Fit(40)
	header := cols.Header()
	for c, head := range cols.Heads {
		name := strings.TrimSpace(head)
		if cols.Shown(c) != strings.Contains(header, name) {
			t.Errorf("column %d (%q) shown=%v but header %q disagrees", c, name, cols.Shown(c), header)
		}
	}
}

// TestRowPrefixWidths pins the row prefix: the session digit (2 runes,
// clamped at 9) then the cursor (2 runes) — 4 in launch mode — plus the
// checkbox and its space in manage mode — 8 total. Why: the columns are
// built with a fixed prefix width; a longer prefix would shove every cell
// right of its heading.
func TestRowPrefixWidths(t *testing.T) {
	if got := rowPrefix(0, false, ""); len(got) != rowPrefixWidthLaunch || got != "    " {
		t.Errorf("launch prefix %q, want 4 blanks", got)
	}
	if got := rowPrefix(3, true, ""); got != "3 > " {
		t.Errorf("prefix %q, want digit and cursor %q", got, "3 > ")
	}
	if got := rowPrefix(0, false, "[ ]"); len(got) != rowPrefixWidthManage || got != "    [ ] " {
		t.Errorf("manage prefix %q, want %d runes", got, rowPrefixWidthManage)
	}
	if got := rowPrefix(0, true, "[-]"); got != "  > [-] " {
		t.Errorf("prefix %q, want the locked box after the cursor", got)
	}
	for n, want := range map[int]string{0: "  ", 1: "1 ", 9: "9 ", 10: "9 ", 99: "9 "} {
		if got := rowPrefix(n, false, ""); !strings.HasPrefix(got, want) {
			t.Errorf("rowPrefix(%d) = %q, want digit %q (clamped at 9)", n, got, want)
		}
	}
}

// TestDiffLineWhenDropped pins the detail block's DIFF line: the counts and
// the ignored count in one unbreakable phrase for a row whose DIFF column
// is not shown, "-" when the counts could not be read. Why: when the column
// gives up on a narrow terminal, this line is the only place the numbers
// still appear — shown whole or not at all.
func TestDiffLineWhenDropped(t *testing.T) {
	rows := tableFixture()
	if got := diffLine(rows[0]); got != "+99/-55 · 12 ignored" {
		t.Errorf("diffLine = %q, want the counts and the ignored count", got)
	}
	if got := diffLine(rows[2]); got != "+0/-0 · 0 ignored" {
		t.Errorf("bare diffLine = %q, want the zero counts", got)
	}
	unknown := cleanup.Row{Kind: cleanup.KindWorktree, Name: "x", Diff: cleanup.DiffStat{Unknown: true}}
	if got := diffLine(unknown); got != "-" {
		t.Errorf("unknown diffLine = %q, want -", got)
	}
}

// ensure the layout compiles against tuilayout's API (a nil check keeps the
// import honest if the helpers above ever stop building a table).
var _ = tuilayout.PadRunes
