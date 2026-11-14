package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The worktree/branch table's columns, left to right. WORKTREE, BRANCH and
// VERDICT are never dropped; the others go in rtDropOrder's order.
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
// narrower than the table: PR first, then the ignored count, then STATUS,
// then DIFF. The PR tag is the most redundant (the detail block names it),
// the counts least — DIFF is given up last because it is the work itself,
// and then the detail block names the highlighted row's counts (diffLine).
var rtDropOrder = []int{rtPR, rtIgn, rtStatus, rtDiff}

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
// sanitised through cleanup.OneLine, as DisplayName is.
func rowCells(r cleanup.Row) []string {
	wt, branch := "", ""
	switch r.Kind {
	case cleanup.KindWorktree:
		wt = cleanup.DisplayName(r)
		branch = cleanup.OneLine(r.Branch)
	case cleanup.KindBranch:
		branch = cleanup.OneLine(r.Branch)
	case cleanup.KindRemote:
		branch = cleanup.OneLine(r.Remote + "/" + r.Branch)
	}
	state := r.State
	if state == "" {
		state = cleanup.StateUnknown
	}
	verdict := verdictText(r.Status)

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

	pr, status, verdictCell := "", string(state), verdict
	if r.Pending {
		// The names are facts (stage 1); every verdict-shaped cell waits.
		pr, status, verdictCell = "-", "-", "-"
	} else if r.PRTag != nil {
		pr = fmt.Sprintf("#%d", r.PRTag.Number)
	}

	return []string{wt, branch, pr, diff, ign, status, verdictCell}
}

// newRowTable builds the shared layout for the header and every row: the
// widest cell or heading sets each column's width (runes), and rtDropOrder
// decides what a narrow list gives up first. prefix is the row prefix's
// width (rowPrefixWidthLaunch or rowPrefixWidthManage).
func newRowTable(rows []cleanup.Row, prefix int) *tuilayout.Columns {
	raw := make([][]string, len(rows))
	widths := make([]int, len(rowHeads))
	for i, head := range rowHeads {
		widths[i] = utf8.RuneCountInString(head)
	}
	for i, r := range rows {
		raw[i] = rowCells(r)
		for c, cell := range raw[i] {
			if n := utf8.RuneCountInString(cell); n > widths[c] {
				widths[c] = n
			}
		}
	}
	heads := make([]string, len(rowHeads))
	for i, head := range rowHeads {
		heads[i] = tuilayout.PadRunes(head, widths[i])
	}
	return tuilayout.NewColumns(heads, widths, prefix, rtDropOrder)
}

// rowLine renders one row through the table's shared layout: number cells
// (PR#, DIFF, IGN) sit on their column's right edge, name cells on the left,
// and Columns keeps every shown column whole.
func rowLine(r cleanup.Row, cols *tuilayout.Columns) string {
	cells := rowCells(r)
	padded := make([]string, len(cells))
	for i, cell := range cells {
		w := cols.Widths[i]
		switch i {
		case rtPR, rtDiff, rtIgn:
			if n := utf8.RuneCountInString(cell); n < w {
				cell = strings.Repeat(" ", w-n) + cell
			}
			padded[i] = cell
		default:
			padded[i] = tuilayout.PadRunes(cell, w)
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
