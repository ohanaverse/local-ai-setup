package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The worktree/branch table's columns, left to right. The four after PR# go in
// rtDropOrder's order; WORKTREE, BRANCH and PR# are never dropped by Columns
// (rowTable then shortens the PR cell and merges WORKTREE and BRANCH).
const (
	rtWorktree = iota
	rtBranch
	rtPR
	rtDiff
	rtIgn
	rtStatus
	rtVerdict
)

// rtDropOrder is the order the columns are given up when the list is
// narrower than the table: the ignored count, then STATUS, then DIFF (the
// work itself), then VERDICT. The PR is kept longest: it is what the user
// picks a row by, with the worktree and the branch. Whatever a step hides the
// highlighted row's detail block names (detail in cleanup_list.go).
var rtDropOrder = []int{rtIgn, rtStatus, rtDiff, rtVerdict}

// rowNameFloor is the narrowest the merged NAME column is cut to.
const rowNameFloor = 6

// The prefix widths: the session digit and cursor in launch mode, plus the
// checkbox and its space in manage mode (spec §1).
const (
	rowPrefixWidthLaunch = 4
	rowPrefixWidthManage = 8
)

// rowHeads are the table's headings, in column order.
var rowHeads = []string{"WORKTREE", "BRANCH", "PR#", "DIFF", "IGN", "STATUS", "VERDICT"}

// rowCells is one row's seven cells, unsanitised-then-padded: names where
// they apply, blank where the row kind cannot have them (a branch row has
// no worktree, a detached one no branch, a bare entry no working tree), and
// "-" where a fact is unknown or its fill has not landed yet (a pending
// row's detail cells read "-", per the facts-first amendment — but only
// cells that apply at all: inapplicability is permanent, so a pending branch
// row's DIFF/IGN stay blank, as they are once filled). Cell text is
// sanitised through cleanup.OneLine, as DisplayName is. The PR cell is the
// PR fact alone (prCell): "#N state", "?" while unknown — overridden by
// "-" while the row is pending, like every verdict-shaped cell.
func rowCells(r cleanup.Row) []string {
	wt, branch := "", branchCell(r)
	if r.Kind == cleanup.KindWorktree {
		wt = cleanup.DisplayName(r)
	}
	state := r.State
	if state == "" {
		state = cleanup.StateUnknown
	}
	verdict := verdictWord(r)

	// DIFF and IGN exist only where a working tree can hold changes. The
	// ignored count is what removal would lose: the effective view, as every
	// other display site reads it (losses guard).
	diff, ign := "", ""
	if r.Kind == cleanup.KindWorktree && !r.Bare {
		losses := r.EffectiveLosses()
		switch {
		case r.Pending, r.Diff.Unknown:
			diff = "-"
		default:
			diff = fmt.Sprintf("+%d/-%d", r.Diff.Added, r.Diff.Removed)
		}
		switch {
		case r.Pending, losses.GatesUnknown:
			ign = "-"
		default:
			ign = strconv.Itoa(losses.IgnoredEntries)
		}
	}

	pr, status, verdictCell := prCell(r), string(state), verdict
	if r.Pending {
		// The names are facts (stage 1); every verdict-shaped cell waits.
		pr, status, verdictCell = "-", "-", "-"
	}

	return []string{wt, branch, pr, diff, ign, status, verdictCell}
}

// prCell is the PR column's fact: "#383 merged" from the row's PR (GitHub's
// state, lower-cased), "?" while the lookup gave no usable answer, "" when
// there is no PR (the lookup answered none — never read as unknown). The
// filter matches this cell too, so the column is the one place the PR reads.
func prCell(r cleanup.Row) string {
	switch {
	case r.PRUnknown:
		return "?"
	case r.PRTag != nil:
		return strings.TrimSpace(fmt.Sprintf("#%d %s", r.PRTag.Number, strings.ToLower(r.PRTag.State)))
	}
	return ""
}

// rowMode is which of the table's three layouts a width fits.
type rowMode int

const (
	// rowModeFull is every column, less the ones rtDropOrder gave up.
	rowModeFull rowMode = iota
	// rowModeShortPR is WORKTREE, BRANCH and PR# with the PR cell cut to its
	// number ("#12": the state word is gone).
	rowModeShortPR
	// rowModeMerged is NAME (the worktree if the row has one, else the
	// branch, cut with an ellipsis to what is left) and the short PR#.
	rowModeMerged
)

// rowTable is the worktree list's table: three layouts built from the same
// rows, of which Fit picks the first that fits the list. Columns stays a
// drop-whole-columns engine shared with the model tables; the ladder past its
// drops (short PR cell, merge, cut) lives here. The zero mode is full.
type rowTable struct {
	full, short, merged *tuilayout.Columns
	mode                rowMode
	nameFull            int // the widest merged name, before any cut
}

// newRowTable builds the layouts for the header and every row: the widest
// cell or heading sets each column's width (terminal cells), and rtDropOrder decides
// what a narrow list gives up first. prefix is the row prefix's width
// (rowPrefixWidthLaunch or rowPrefixWidthManage).
func newRowTable(rows []cleanup.Row, prefix int) *rowTable {
	raw := make([][]string, len(rows))
	widths := make([]int, len(rowHeads))
	for i, head := range rowHeads {
		widths[i] = cellWidth(head)
	}
	nameFull := cellWidth("NAME")
	prShort := cellWidth(rowHeads[rtPR])
	for i, r := range rows {
		raw[i] = rowCells(r)
		for c, cell := range raw[i] {
			if n := cellWidth(cell); n > widths[c] {
				widths[c] = n
			}
		}
		nameFull = max(nameFull, cellWidth(mergedName(r)))
		prShort = max(prShort, cellWidth(shortPRCell(raw[i][rtPR])))
	}
	heads := make([]string, len(rowHeads))
	for i, head := range rowHeads {
		heads[i] = padCells(head, widths[i])
	}
	return &rowTable{
		full: tuilayout.NewColumns(heads, widths, prefix, rtDropOrder),
		short: tuilayout.NewColumns(
			[]string{heads[rtWorktree], heads[rtBranch], padCells(rowHeads[rtPR], prShort)},
			[]int{widths[rtWorktree], widths[rtBranch], prShort}, prefix, nil),
		merged: tuilayout.NewColumns(
			[]string{padCells("NAME", nameFull), padCells(rowHeads[rtPR], prShort)},
			[]int{nameFull, prShort}, prefix, nil),
		nameFull: nameFull,
	}
}

// fitsWidth reports whether c's rows fit width and its header does with the
// title room the list's title bar needs (Columns.Fit's own test).
func fitsWidth(c *tuilayout.Columns, width int) bool {
	return c.Width() <= width && cellWidth(c.Header())+tuilayout.TitleRoom <= width
}

// Fit chooses the layout for a list of the given width, trying each step only
// when the one before does not fit: the full table with rtDropOrder's columns
// given up, then the short PR cell, then the merged NAME column, whose name is
// cut (never below rowNameFloor, and never wider than the widest name) to
// what is left. Below the floor the list's own clip applies, as it does for
// any table too narrow for its never-dropped columns. It recomputes from the
// whole table every call, so a resize widens the layout back.
func (t *rowTable) Fit(width int) {
	t.full.Fit(width)
	switch {
	case fitsWidth(t.full, width):
		t.mode = rowModeFull
		return
	case fitsWidth(t.short, width):
		t.mode = rowModeShortPR
		return
	}
	t.mode = rowModeMerged
	t.setNameWidth(t.nameFull)
	deficit := max(0, t.merged.Width()-width,
		cellWidth(t.merged.Header())+tuilayout.TitleRoom-width)
	t.setNameWidth(min(t.nameFull, max(rowNameFloor, t.nameFull-deficit)))
}

// setNameWidth sets the merged NAME column's width and its heading's padding.
func (t *rowTable) setNameWidth(w int) {
	t.merged.Widths[0] = w
	t.merged.Heads[0] = padCells("NAME", w)
}

func (t *rowTable) active() *tuilayout.Columns {
	switch t.mode {
	case rowModeShortPR:
		return t.short
	case rowModeMerged:
		return t.merged
	}
	return t.full
}

// Header is the header line for the layout now chosen.
func (t *rowTable) Header() string { return t.active().Header() }

// Width is the widest a row of the layout now chosen can be.
func (t *rowTable) Width() int { return t.active().Width() }

// Shown reports whether the rt* column col is drawn on its own: in the merged
// layout only PR# is (WORKTREE and BRANCH share NAME).
func (t *rowTable) Shown(col int) bool {
	switch t.mode {
	case rowModeShortPR:
		return col == rtWorktree || col == rtBranch || col == rtPR
	case rowModeMerged:
		return col == rtPR
	}
	return t.full.Shown(col)
}

// PRState reports whether the PR cell still carries its state word.
func (t *rowTable) PRState() bool { return t.mode == rowModeFull }

// Merged reports whether WORKTREE and BRANCH are one NAME column.
func (t *rowTable) Merged() bool { return t.mode == rowModeMerged }

// branchCell is the BRANCH cell: the branch, a remote row's remote/branch,
// blank for a kind with none.
func branchCell(r cleanup.Row) string {
	switch r.Kind {
	case cleanup.KindWorktree, cleanup.KindBranch:
		return cleanup.OneLine(r.Branch)
	case cleanup.KindRemote:
		return cleanup.OneLine(r.Remote + "/" + r.Branch)
	}
	return ""
}

// mergedName is the NAME cell: the worktree's name if the row has one, else
// the branch cell.
func mergedName(r cleanup.Row) string {
	if r.Kind == cleanup.KindWorktree {
		if n := cleanup.DisplayName(r); n != "" {
			return n
		}
	}
	return branchCell(r)
}

// shortPRCell is the PR cell without its state word: "#12 merged" is "#12";
// "?", "-" and "" are as they are.
func shortPRCell(cell string) string {
	if i := strings.IndexByte(cell, ' '); i >= 0 {
		return cell[:i]
	}
	return cell
}

// cellWidth is s's width in terminal cells (a CJK character takes two), the
// measure every width in this table uses: a rune count lets a wide name push
// the PR number off the edge (style guide L5).
func cellWidth(s string) int { return lipgloss.Width(s) }

// padCells pads s with spaces to w cells; a wider s is returned as it is.
func padCells(s string, w int) string {
	if n := cellWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// truncateName cuts s to at most w cells, the last of them an ellipsis, on a
// rune boundary; a name that fits is returned whole. The cut is not on a
// grapheme-cluster boundary, so a combining mark or ZWJ emoji sequence can be
// split before the ellipsis. The width bound still holds; only the look is off.
func truncateName(s string, w int) string {
	if cellWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	var out []rune
	used := 0
	for _, r := range s {
		rw := cellWidth(string(r))
		if used+rw > w-1 {
			break
		}
		out, used = append(out, r), used+rw
	}
	return string(out) + "…"
}

// NameCut reports whether the merged NAME cell shows less than r's whole name.
func (t *rowTable) NameCut(r cleanup.Row) bool {
	return t.Merged() && cellWidth(mergedName(r)) > t.merged.Widths[0]
}

// nameLine is the whole name for the detail block, used when NAME is cut: the
// rule line above the detail is cut at the same width, so this wrapped text is
// where a long name reads whole.
func nameLine(r cleanup.Row) string { return "name " + mergedName(r) }

// rowLine renders one row through the layout Fit chose: number cells (DIFF,
// IGN) sit on their column's right edge, name cells (PR# is free text, "#3
// open" or "?") on the left, and Columns keeps every shown column whole.
func rowLine(r cleanup.Row, t *rowTable) string {
	switch t.mode {
	case rowModeShortPR:
		cells := rowCells(r)
		w := t.short.Widths
		return t.short.Line([]string{
			padCells(cells[rtWorktree], w[0]),
			padCells(cells[rtBranch], w[1]),
			padCells(shortPRCell(cells[rtPR]), w[2]),
		})
	case rowModeMerged:
		w := t.merged.Widths
		return t.merged.Line([]string{
			padCells(truncateName(mergedName(r), w[0]), w[0]),
			padCells(shortPRCell(rowCells(r)[rtPR]), w[1]),
		})
	}
	cols := t.full
	cells := rowCells(r)
	padded := make([]string, len(cells))
	for i, cell := range cells {
		w := cols.Widths[i]
		switch i {
		case rtDiff, rtIgn:
			if n := cellWidth(cell); n < w {
				cell = strings.Repeat(" ", w-n) + cell
			}
			padded[i] = cell
		default:
			padded[i] = padCells(cell, w)
		}
	}
	return cols.Line(padded)
}

// rowPrefix is a row's fixed-width prefix: the session digit (clamped at 9,
// like the model list's ref column — plain ASCII so the columns never
// shift), then the cursor, then, in manage mode only, the checkbox and its
// space. box is "[x]" / "[ ]" / "[-]" / "" (launch mode, no box).
func rowPrefix(sessions int, cursor bool, box string) string {
	out := refColumn(sessions)
	if cursor {
		out += markerMarked
	} else {
		out += markerBlank
	}
	if box != "" {
		out += box + " "
	}
	return out
}

// prLine is the highlighted row's PR fact for the detail block, used when the
// PR# column is not drawn: "PR #3 open", "PR lookup unknown", or "" when the
// row has no PR or is still pending (the table draws "-" for those too).
func prLine(r cleanup.Row) string {
	if r.Pending {
		return ""
	}
	if c := prCell(r); c == "?" {
		return "PR lookup unknown"
	} else if c != "" {
		return "PR " + c
	}
	return ""
}

// diffLine is the highlighted row's counts for the detail block, used when
// the DIFF column is not drawn (shown whole or not at all): "+N/-N · N
// ignored", or "-" when the counts could not be read.
func diffLine(r cleanup.Row) string {
	if r.Diff.Unknown {
		return "-"
	}
	losses := r.EffectiveLosses()
	ign := 0
	if !losses.GatesUnknown {
		ign = losses.IgnoredEntries
	}
	return fmt.Sprintf("+%d/-%d · %d ignored", r.Diff.Added, r.Diff.Removed, ign)
}

// verdictLine is the highlighted row's verdict for the detail block, used when
// the VERDICT column is not drawn: "verdict safe", or "" for a pending row
// (the table draws "-" for it too).
func verdictLine(r cleanup.Row) string {
	if r.Pending {
		return ""
	}
	return "verdict " + verdictWord(r)
}

// statusLine is the highlighted row's STATUS word for the detail block, used
// when the STATUS column is not drawn: "status dirty", or "" while pending.
func statusLine(r cleanup.Row) string {
	if r.Pending {
		return ""
	}
	state := r.State
	if state == "" {
		state = cleanup.StateUnknown
	}
	return "status " + string(state)
}

// ignoredLine is the highlighted row's ignored count for the detail block,
// used when IGN is not drawn but DIFF is (diffLine carries it with the
// counts): "12 ignored", or "" where the table has no IGN cell either.
func ignoredLine(r cleanup.Row) string {
	if r.Kind != cleanup.KindWorktree || r.Bare || r.Pending {
		return ""
	}
	losses := r.EffectiveLosses()
	if losses.GatesUnknown {
		return ""
	}
	return fmt.Sprintf("%d ignored", losses.IgnoredEntries)
}

// branchLine is the worktree row's branch for the detail block, used when
// WORKTREE and BRANCH are merged into NAME and the branch is not drawn.
func branchLine(r cleanup.Row) string {
	if r.Kind != cleanup.KindWorktree || r.Branch == "" {
		return ""
	}
	return "branch " + cleanup.OneLine(r.Branch)
}
