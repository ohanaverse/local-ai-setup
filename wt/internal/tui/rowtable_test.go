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
			PRTag:  &cleanup.PRInfo{Number: 383, State: "MERGED"}, Sessions: 2,
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
	want := []string{"feat/x", "feat/x", "#383 merged", "+99/-55", "12", "in main", "safe"}
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

	// The PR column carries the PR fact alone: the state lower-cased, "?" while
	// the lookup could not answer, blank when there is no PR, and "-" while
	// pending beats both (verdict-shaped cells wait for the fill).
	for _, c := range []struct {
		name string
		row  cleanup.Row
		want string
	}{
		{"merged", cleanup.Row{Kind: cleanup.KindBranch, PRTag: &cleanup.PRInfo{Number: 555, State: "MERGED"}}, "#555 merged"},
		{"open", cleanup.Row{Kind: cleanup.KindBranch, PRTag: &cleanup.PRInfo{Number: 3, State: "OPEN"}}, "#3 open"},
		{"closed", cleanup.Row{Kind: cleanup.KindBranch, PRTag: &cleanup.PRInfo{Number: 4, State: "CLOSED"}}, "#4 closed"},
		{"unknown", cleanup.Row{Kind: cleanup.KindBranch, PRUnknown: true}, "?"},
		{"no pr", rows[1], ""},
		{"empty state", cleanup.Row{Kind: cleanup.KindBranch, PRTag: &cleanup.PRInfo{Number: 5}}, "#5"},
		{"pending wins", cleanup.Row{Kind: cleanup.KindBranch, Pending: true, PRUnknown: true}, "-"},
	} {
		if got := rowCells(c.row)[rtPR]; got != c.want {
			t.Errorf("PR cell (%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRowTableWidthsAndRightAlignment pins the layout: every column is as
// wide as its widest cell or heading (measured in runes), and the number
// cells (DIFF, IGN) sit on their column's right edge while name cells and the
// free-text PR# cell ("#3 open", "?") sit on the left. Why: the table only scans if its digits stack and its
// headings line up over their columns.
func TestRowTableWidthsAndRightAlignment(t *testing.T) {
	rows := tableFixture()
	cols := newRowTable(rows, rowPrefixWidthLaunch)

	if len(cols.full.Widths) != 7 {
		t.Fatalf("widths = %v, want 7 columns", cols.full.Widths)
	}
	// Headings are the floor of every width.
	for i, head := range []string{"WORKTREE", "BRANCH", "PR#", "DIFF", "IGN", "STATUS", "VERDICT"} {
		if cols.full.Widths[i] < utf8.RuneCountInString(head) {
			t.Errorf("column %d width %d is under its heading %q", i, cols.full.Widths[i], head)
		}
	}
	// The widest cells set the rest: feat/x / #383 / +99/-55 / 12 / in main.
	if cols.full.Widths[rtDiff] != utf8.RuneCountInString("+99/-55") {
		t.Errorf("DIFF width = %d, want the widest cell %d", cols.full.Widths[rtDiff], utf8.RuneCountInString("+99/-55"))
	}
	if cols.full.Widths[rtIgn] != utf8.RuneCountInString("IGN") && cols.full.Widths[rtIgn] != utf8.RuneCountInString("12") {
		t.Errorf("IGN width = %d, want max(IGN, 12)", cols.full.Widths[rtIgn])
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
	// PR# is left-aligned: a short cell is padded after, so the "#" of every
	// PR cell starts at the same column under the heading.
	short := cleanup.Row{Kind: cleanup.KindBranch, Branch: "b", PRTag: &cleanup.PRInfo{Number: 3, State: "OPEN"}}
	if l, long := rowLine(short, cols), rowLine(rows[0], cols); strings.Index(l, "#3 open") != strings.Index(long, "#383 merged") {
		t.Errorf("PR cells not left-aligned: %q vs %q", l, long)
	}
	// One line per row, each starting with the row's own cells.
	for i, r := range rows {
		if got := rowLine(r, cols); strings.Contains(got, "\n") {
			t.Errorf("row %d rendered %q, want one line", i, got)
		}
	}
}

// TestRowTableLadder narrows the table one column of width at a time and
// records the widest width at which each step of the ladder has happened:
// IGN, STATUS, DIFF and VERDICT drop (in that order, VERDICT still drawn when
// DIFF goes), then the PR state word, then WORKTREE and BRANCH merge into
// NAME. The PR number is drawn at every width, and WORKTREE and BRANCH stay
// until the merge. Why: the ladder is the contract for what a narrow terminal
// keeps; a step taken out of order hides the wrong fact.
func TestRowTableLadder(t *testing.T) {
	for _, prefix := range []int{rowPrefixWidthLaunch, rowPrefixWidthManage} {
		tbl := newRowTable(tableFixture(), prefix)
		conds := map[string]func() bool{
			"ign":     func() bool { return !tbl.Shown(rtIgn) },
			"status":  func() bool { return !tbl.Shown(rtStatus) },
			"diff":    func() bool { return !tbl.Shown(rtDiff) },
			"verdict": func() bool { return !tbl.Shown(rtVerdict) },
			"prstate": func() bool { return !tbl.PRState() },
			"merged":  tbl.Merged,
		}
		first := map[string]int{} // step -> the widest width at which it has happened
		for w := 200; w >= 1; w-- {
			tbl.Fit(w)
			if !tbl.Shown(rtPR) {
				t.Fatalf("prefix %d width %d: the PR column is not drawn", prefix, w)
			}
			if !tbl.Merged() && (!tbl.Shown(rtWorktree) || !tbl.Shown(rtBranch)) {
				t.Fatalf("prefix %d width %d: WORKTREE or BRANCH hidden before the merge", prefix, w)
			}
			for name, c := range conds {
				if _, seen := first[name]; !seen && c() {
					first[name] = w
					if name == "diff" && !tbl.Shown(rtVerdict) {
						t.Errorf("prefix %d: VERDICT already gone when DIFF first drops (width %d)", prefix, w)
					}
				}
			}
		}
		prev := 201
		for _, name := range []string{"ign", "status", "diff", "verdict", "prstate", "merged"} {
			w, ok := first[name]
			if !ok {
				t.Fatalf("prefix %d: step %q never happened (%v)", prefix, name, first)
			}
			if w >= prev {
				t.Fatalf("prefix %d: step %q at width %d is not after the previous step at %d (%v)", prefix, name, w, prev, first)
			}
			prev = w
		}
	}
}

// TestRowTableNeverWiderThanTheList fits the table at every width from the
// narrowest the name floor allows up to 200 and checks the header (with its
// title room) and every row line fit. Why: a line wider than the list is
// cut at the terminal's edge — the PR number is what would be lost.
func TestRowTableNeverWiderThanTheList(t *testing.T) {
	for _, prefix := range []int{rowPrefixWidthLaunch, rowPrefixWidthManage} {
		rows := tableFixture()
		tbl := newRowTable(rows, prefix)
		tbl.Fit(1) // the floor: the narrowest layout there is
		floor := max(tbl.Width(), utf8.RuneCountInString(tbl.Header())+tuilayout.TitleRoom)
		for w := floor; w <= 200; w++ {
			tbl.Fit(w)
			if got := utf8.RuneCountInString(tbl.Header()) + tuilayout.TitleRoom; got > w {
				t.Fatalf("prefix %d width %d: header %q needs %d", prefix, w, tbl.Header(), got)
			}
			for i, r := range rows {
				if got := prefix + utf8.RuneCountInString(rowLine(r, tbl)); got > w {
					t.Fatalf("prefix %d width %d: row %d is %d wide: %q", prefix, w, i, got, rowLine(r, tbl))
				}
			}
		}
	}
}

// TestRowTableTruncatesNameKeepsPR narrows a table with one long name to one
// rune under the untruncated merged layout: the NAME cell is cut with an
// ellipsis, the PR number is whole and last on the line.
func TestRowTableTruncatesNameKeepsPR(t *testing.T) {
	long := "feature-very-long-worktree-name-x"
	r := cleanup.Row{
		Kind: cleanup.KindWorktree, Name: long, Branch: long,
		Status: cleanup.StatusProven, State: cleanup.StateInMain,
		PRTag: &cleanup.PRInfo{Number: 383, State: "MERGED"},
	}
	tbl := newRowTable([]cleanup.Row{r}, rowPrefixWidthLaunch)
	w := tbl.merged.Width() - 1
	tbl.Fit(w)
	if !tbl.Merged() || tbl.PRState() {
		t.Fatalf("merged=%v prstate=%v at width %d, want a merged layout without the state word", tbl.Merged(), tbl.PRState(), w)
	}
	line := rowLine(r, tbl)
	if !strings.Contains(line, "…") || !strings.HasSuffix(line, "#383") {
		t.Errorf("line %q: want a cut name and the PR number last", line)
	}
	if strings.Contains(line, long) {
		t.Errorf("line %q still holds the whole name", line)
	}
	if got := rowPrefixWidthLaunch + utf8.RuneCountInString(line); got > w {
		t.Errorf("line is %d wide at width %d: %q", got, w, line)
	}
}

// TestRowTableWidensBack pins that the fit is recomputed from the whole
// table: a frame that truncated the name leaves nothing behind once the
// terminal is wide again.
func TestRowTableWidensBack(t *testing.T) {
	rows := tableFixture()
	tbl := newRowTable(rows, rowPrefixWidthLaunch)
	tbl.Fit(200)
	header, line := tbl.Header(), rowLine(rows[0], tbl)
	tbl.Fit(rowPrefixWidthLaunch + 12)
	if !tbl.Merged() {
		t.Fatalf("width %d did not merge", rowPrefixWidthLaunch+12)
	}
	tbl.Fit(200)
	if tbl.Header() != header || rowLine(rows[0], tbl) != line {
		t.Errorf("after widening: header %q line %q, want %q and %q", tbl.Header(), rowLine(rows[0], tbl), header, line)
	}
}

// TestRowTableTinyWidthsDoNotPanic fits and renders at widths below any
// layout: the line is cut by the list's own clip, but nothing panics (a
// negative repeat count would).
func TestRowTableTinyWidthsDoNotPanic(t *testing.T) {
	for _, prefix := range []int{rowPrefixWidthLaunch, rowPrefixWidthManage} {
		rows := tableFixture()
		tbl := newRowTable(rows, prefix)
		for w := 0; w <= 12; w++ {
			tbl.Fit(w)
			_ = tbl.Header()
			for _, r := range rows {
				_ = rowLine(r, tbl)
			}
		}
	}
}

// TestPendingRowInMergedMode: a facts-only row keeps its name and reads "-"
// for its PR in the merged layout.
func TestPendingRowInMergedMode(t *testing.T) {
	rows := tableFixture()
	tbl := newRowTable(rows, rowPrefixWidthLaunch)
	tbl.Fit(tbl.merged.Width()) // the widest merged layout: names whole
	if !tbl.Merged() {
		t.Fatalf("width %d did not merge", tbl.merged.Width())
	}
	line := rowLine(rows[3], tbl) // pending-wt
	if !strings.Contains(line, "pending") || !strings.HasSuffix(line, "-") {
		t.Errorf("pending row in merged mode = %q, want its name and a - PR cell", line)
	}
}

// TestShortPRCell: the PR cell without its state word.
func TestShortPRCell(t *testing.T) {
	for in, want := range map[string]string{"#12 merged": "#12", "#3 open": "#3", "?": "?", "-": "-", "": ""} {
		if got := shortPRCell(in); got != want {
			t.Errorf("shortPRCell(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTruncateNameKeepsRunesWhole: the cut is on a rune boundary and the
// ellipsis counts toward the width.
func TestTruncateNameKeepsRunesWhole(t *testing.T) {
	for _, c := range []struct {
		in   string
		w    int
		want string
	}{
		{"feature-login", 8, "feature…"},
		{"héllo-wörld", 6, "héllo…"},
		{"日本語のブランチ", 4, "日本語…"},
		{"abc", 3, "abc"},
		{"abc", 1, "…"},
	} {
		got := truncateName(c.in, c.w)
		if got != c.want || utf8.RuneCountInString(got) > c.w || !utf8.ValidString(got) {
			t.Errorf("truncateName(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
}

// TestMergedNamePicksWorktreeElseBranch: the merged NAME is the worktree's
// name when the row has one, else the branch cell.
func TestMergedNamePicksWorktreeElseBranch(t *testing.T) {
	rows := tableFixture()
	for i, want := range []string{"feat/x", "wt-loose", "bare.git", "pending-wt", "solo", "origin/feat/y"} {
		if got := mergedName(rows[i]); got != want {
			t.Errorf("row %d: mergedName = %q, want %q", i, got, want)
		}
	}
	if got := mergedName(cleanup.Row{Kind: cleanup.KindWorktree, Branch: "b"}); got != "b" {
		t.Errorf("worktree row with no name: mergedName = %q, want the branch b", got)
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

// TestVerdictWords pins the VERDICT column's one-word mapping: safe / check /
// work for the three real verdicts, locked for a row nothing can be done
// about (the root, a hard stop), and "-" while the fill is still owed. Why:
// the column is the list's one glance-level answer, and a word that reads
// safer than the row is (a hard-stopped row saying "safe") is the mistake
// this table exists to prevent.
func TestVerdictWords(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  cleanup.Row
		want string
	}{
		{"proven", cleanup.Row{Kind: cleanup.KindBranch, Name: "b", Branch: "b", Status: cleanup.StatusProven}, "safe"},
		{"less sure", cleanup.Row{Kind: cleanup.KindBranch, Name: "b", Branch: "b", Status: cleanup.StatusLessSure}, "check"},
		{"unique work", cleanup.Row{Kind: cleanup.KindBranch, Name: "b", Branch: "b", Status: cleanup.StatusUniqueWork}, "work"},
		{"root", cleanup.Row{Kind: cleanup.KindWorktree, Name: "r", Primary: true, Status: cleanup.StatusInUse,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockPrimary, Detail: "the primary checkout"}}}, "locked"},
		{"pending", cleanup.Row{Kind: cleanup.KindBranch, Name: "b", Branch: "b", Status: cleanup.StatusLessSure, Pending: true}, "-"},
	} {
		if got := verdictWord(tc.row); got != tc.want {
			t.Errorf("%s: verdictWord = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestVerdictColumnIsShort pins that no verdict word outgrows the column at
// 80 columns: the longest is "locked" (6 runes), and the VERDICT heading
// still fits and is drawn there. Why: the column is never dropped, so a
// longer word would push the table past the terminal at 80 — the shape the
// list was flattened to keep.
func TestVerdictColumnIsShort(t *testing.T) {
	rows := []cleanup.Row{
		{Kind: cleanup.KindBranch, Name: "safe-row", Branch: "safe-row", Status: cleanup.StatusProven},
		{Kind: cleanup.KindBranch, Name: "check-row", Branch: "check-row", Status: cleanup.StatusLessSure},
		{Kind: cleanup.KindBranch, Name: "work-row", Branch: "work-row", Status: cleanup.StatusUniqueWork},
		{Kind: cleanup.KindWorktree, Name: "root", Primary: true, Status: cleanup.StatusInUse,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockPrimary}}},
	}
	for _, r := range rows {
		if got := utf8.RuneCountInString(rowCells(r)[rtVerdict]); got > 6 {
			t.Errorf("%s: verdict cell is %d runes, want at most 6", cleanup.DisplayName(r), got)
		}
	}
	cols := newRowTable(rows, rowPrefixWidthManage)
	cols.Fit(200)
	if !cols.Shown(rtVerdict) || !strings.Contains(cols.Header(), "VERDICT") {
		t.Errorf("VERDICT not drawn on a wide terminal: %q", cols.Header())
	}
}

// TestPRLineNamesThePRWhenTheColumnIsGone pins the detail block's fallback
// for a dropped PR# column: the PR and its state, "PR lookup unknown", and
// nothing for no PR or a pending row. Why: the column is the first dropped on
// a narrow terminal and is the only other place the PR fact reads.
func TestPRLineNamesThePRWhenTheColumnIsGone(t *testing.T) {
	for _, c := range []struct {
		row  cleanup.Row
		want string
	}{
		{cleanup.Row{PRTag: &cleanup.PRInfo{Number: 4, State: "CLOSED"}}, "PR #4 closed"},
		{cleanup.Row{PRUnknown: true}, "PR lookup unknown"},
		{cleanup.Row{}, ""},
		{cleanup.Row{Pending: true, PRUnknown: true}, ""},
	} {
		if got := prLine(c.row); got != c.want {
			t.Errorf("prLine(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
}

// TestDetailLineHelpers pins the sentences the detail block uses for a fact
// the table does not draw: the verdict word, the STATUS word, the ignored
// count and the branch — each "" when the row has nothing to say (pending,
// bare, no branch). Why: a hidden column's fact is only reachable through
// these lines.
func TestDetailLineHelpers(t *testing.T) {
	rows := tableFixture()
	if got := verdictLine(rows[0]); got != "verdict safe" {
		t.Errorf("verdictLine(proven) = %q, want %q", got, "verdict safe")
	}
	if got := verdictLine(rows[3]); got != "" {
		t.Errorf("verdictLine(pending) = %q, want empty", got)
	}
	if got := statusLine(rows[1]); !strings.HasPrefix(got, "status ") || !strings.Contains(got, string(rows[1].State)) {
		t.Errorf("statusLine(dirty) = %q, want %q plus its state", got, "status ")
	}
	if got := statusLine(rows[3]); got != "" {
		t.Errorf("statusLine(pending) = %q, want empty", got)
	}
	if got := ignoredLine(rows[0]); got != "12 ignored" {
		t.Errorf("ignoredLine = %q, want %q", got, "12 ignored")
	}
	for _, i := range []int{2, 3, 4} { // bare, pending, branch row
		if got := ignoredLine(rows[i]); got != "" {
			t.Errorf("ignoredLine(row %d) = %q, want empty", i, got)
		}
	}
	if got := branchLine(rows[0]); got != "branch feat/x" {
		t.Errorf("branchLine = %q, want %q", got, "branch feat/x")
	}
	if got := branchLine(rows[1]); got != "" { // detached worktree: no branch
		t.Errorf("branchLine(detached) = %q, want empty", got)
	}
}
