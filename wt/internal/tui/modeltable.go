package tui

import (
	"fmt"
	"strconv"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
	"github.com/ohanaverse/local-ai-setup/wt/internal/windows"
)

// modelTable is the rendered selector: a header line (shown as the list's
// title) and one item per sorted row. header is the full table's; the items
// share one tuilayout.Columns, which decides how much of the table is drawn
// at the width the list is given (tuilayout.FitTo).
type modelTable struct {
	header string
	items  []*modelItem
}

// The table's columns, left to right.
const (
	colFamily = iota
	colModel
	colTier
	colLoc
	colStatus
	colRunning
	colCost
	colCtx
	col1D
	col7D
	col30D
)

// colDropOrder is the order in which columns are given up when the table is
// wider than its list (tuilayout.Columns.DropOrder): usage from the longest
// window to the shortest first, then cost, the tier, location and family. The
// tier outlasts usage and cost because it is the guidance for choosing a
// model and costs four or five columns; the mode line still names a
// highlighted row's price and tier when their columns are gone.
// MODEL, STATUS and RUNNING are not in it: they say what a row is and whether
// Enter launches or starts it, so they are never dropped.
//
// CTX, the context window (#689), is given up just before COST: it is one
// short cell, and the mode line still names the highlighted row's window.
var colDropOrder = []int{col30D, col7D, col1D, colCtx, colCost, colTier, colLoc, colFamily}

// unratedDropOrder is colDropOrder for a table in which no row has a tier or
// a score (no tiers.toml, or none of its models in it): TIER is a column of
// dashes there, so it goes first and never costs a column that says
// something.
var unratedDropOrder = []int{colTier, col30D, col7D, col1D, colCtx, colCost, colLoc, colFamily}

const rowPrefixWidth = 4 // ref column (2) + rotation marker (2), composed by modelItem.Title()

// timePricedMark follows the COST cell of a model that has cost.time_prices
// rows: the prices shown are the ones in force when the table was built, and
// they change with the time. costLegend is the COST heading of a table that
// has such a row; it says what the mark means. It is used only when the
// column is already that wide (a marked cell with three prices is one column
// wider), so it never widens a row. The table is still not as narrow as one
// with no marked row: the mark's one column can cost a usage column (1D, 7D,
// 30D) at the one list width where that column only just fitted,
// and where COST is the last column drawn the long heading must also clear
// the list's title bar (tuilayout.TitleRoom), so COST itself is given up at
// three widths where a plain table shows it. The mode line names the
// highlighted row's price whether or not the column is drawn (modeLine).
const (
	timePricedMark = "~"
	costLegend     = "COST (~ varies by time)"
)

// costNote is a row's price in force as the launcher's mode line names it
// when the row is highlighted: `cost 0.66/0.022/1.98`, input, cached input
// and output per million tokens, a missing one as a dash. When the model's
// price depends on the time the word is `cost~`: the mark comes before the
// numbers, so nothing that shortens the line can leave the numbers without
// it. It is "" for a row that shows no price (a discovered row) or has none
// in force. The numbers are written short (six significant digits, no
// padding) so that the note fits a 40-column terminal (modeLine); the COST
// column keeps its fixed width.
func costNote(r tableRow) string {
	p := r.price()
	if r.Discovered || p.Input == nil && p.Cache == nil && p.Output == nil {
		return ""
	}
	short := func(v *float64) string {
		if v == nil {
			return "-"
		}
		return strconv.FormatFloat(*v, 'g', 6, 64)
	}
	word := "cost"
	if r.timePriced() {
		word += timePricedMark
	}
	return word + " " + short(p.Input) + "/" + short(p.Cache) + "/" + short(p.Output)
}

// costCell is a row's COST cell: the three prices in force for it
// (tableRow.price), marked when the model's price depends on the time.
func costCell(r tableRow) string {
	p := r.price()
	text := formatPerToken(config.ModelCost{InputPricePerMillion: p.Input, CachePricePerMillion: p.Cache, OutputPricePerMillion: p.Output})
	if r.timePriced() {
		text += timePricedMark
	}
	return text
}

// windowOf is a row's context window in tokens, 0 when unknown: a model's
// HostContext, and for a slot-profile row the smallest over its slot models
// (ProfileRow.Window, "-" when any slot's is unknown).
func windowOf(r tableRow) int {
	if r.Profile != nil {
		return r.Profile.Window()
	}
	return r.Model.HostContext
}

// windowNote is the mode line's name of a row's window ("ctx 262k"), "" when
// it is unknown.
func windowNote(r tableRow) string {
	if w := windowOf(r); w > 0 {
		return "ctx " + windows.Short(w)
	}
	return ""
}

func padRunes(s string, w int) string { return tuilayout.PadRunes(s, w) }

func maxRunes(min int, ss ...string) int { return tuilayout.MaxRunes(min, ss...) }

// runningText is a row's RUNNING cell: "run" for a model that is serving,
// "load" for one omlx is still loading (#259; Enter starts it, which waits for
// the load), "-" otherwise. Both words fit the column's fixed width of 7.
func runningText(r catalog.Row) string {
	switch {
	case r.Running && r.Loading:
		return "load"
	case r.Running:
		return "run"
	}
	return "-"
}

// buildTable builds rows, sorts them and renders them, for the standalone
// picker (PickStartModel), which has no in-use counts.
func buildTable(in tableInput, lastID string) modelTable {
	rows := buildRows(in)
	sortRows(rows)
	return renderTable(rows, in.agent, nil, lastID)
}

// renderTable formats already-sorted rows. Columns are separated by two
// spaces and padded to the widest cell (headings included) so the header and
// every row line up; measured in runes so multi-byte names don't shift them.
// refs is each row's count of the model server's client sessions (the
// in-use column).
func renderTable(rows []tableRow, agent string, refs map[string]int, lastID string) modelTable {
	cost := make([]string, len(rows))
	ctx := make([]string, len(rows))
	fam := make([]string, len(rows))
	c1, c7, c30 := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
	ctxW := len("CTX")
	famW, idW, costW, w1, w7, w30 := len("FAMILY"), len("MODEL"), len("COST"), len("1D"), len("7D"), len("30D")
	wS, wT := len("STATUS"), len("TIER")
	tier := make([]string, len(rows))
	timePriced, rated := false, false
	// Every TIER cell pads its score to the widest score in the table, so
	// the digits line up on the right.
	digits := 0
	for _, r := range rows {
		digits = max(digits, r.tier.ScoreDigits())
	}
	for i, r := range rows {
		fam[i] = r.Model.Family
		if r.Profile != nil {
			fam[i] = "profile"
		}
		if fam[i] == "" {
			fam[i] = "-"
		}
		cost[i] = "-"
		if !r.Discovered {
			cost[i] = costCell(r)
			timePriced = timePriced || r.timePriced()
		}
		// A slot-profile row's tier is "-": its slots' tiers are on its
		// slot line (slotLine).
		tier[i] = r.tier.Cell(digits)
		rated = rated || tier[i] != "-"
		wT = maxRunes(wT, tier[i])
		ctx[i] = windows.Short(windowOf(r))
		ctxW = maxRunes(ctxW, ctx[i])
		c1[i], c7[i], c30[i] = fmt.Sprint(r.counts.OneDay), fmt.Sprint(r.counts.SevenDay), fmt.Sprint(r.counts.ThirtyDay)
		famW = maxRunes(famW, fam[i])
		idW = maxRunes(idW, r.Model.ID)
		costW = maxRunes(costW, cost[i])
		w1, w7, w30 = maxRunes(w1, c1[i]), maxRunes(w7, c7[i]), maxRunes(w30, c30[i])
		wS = maxRunes(wS, string(r.Status))
	}
	costHead := "COST"
	if timePriced && len(costLegend) <= costW {
		costHead = costLegend
	}
	dropOrder := colDropOrder
	if !rated {
		dropOrder = unratedDropOrder
	}
	// One layout for the header and every row. It starts with every column
	// shown; whoever sizes the list narrows it (tuilayout.FitTo).
	cols := tuilayout.NewColumns(
		[]string{
			padRunes("FAMILY", famW), padRunes("MODEL", idW), padRunes("TIER", wT), padRunes("LOC", 5), padRunes("STATUS", wS),
			padRunes("RUNNING", 7), padRunes(costHead, costW), padRunes("CTX", ctxW),
			padRunes("1D", w1), padRunes("7D", w7), padRunes("30D", w30),
		},
		[]int{famW, idW, wT, 5, wS, 7, costW, ctxW, w1, w7, w30},
		rowPrefixWidth, dropOrder,
	)
	header := cols.Header()

	items := make([]*modelItem, 0, len(rows))
	for i, r := range rows {
		loc := string(r.Location)
		if r.Profile != nil {
			loc = r.Profile.Loc
		}
		if loc == "" {
			loc = "-"
		}
		// The last padded column would leave trailing spaces; Columns.Line
		// trims them.
		cells := []string{
			padRunes(fam[i], famW), padRunes(r.Model.ID, idW), padRunes(tier[i], wT), padRunes(loc, 5), padRunes(string(r.Status), wS),
			padRunes(runningText(r.Row), 7), padRunes(cost[i], costW), padRunes(ctx[i], ctxW),
			padRunes(c1[i], w1), padRunes(c7[i], w7), padRunes(c30[i], w30),
		}
		it := &modelItem{model: r.Model, line: cols.Line(cells), cells: cells, cols: cols, marked: lastID != "" && r.Model.ID == lastID, ref: refs[r.Model.ID], cost: costNote(r), window: windowNote(r), tier: r.tier.Notes()}
		switch r.Action() {
		case catalog.ActionBlock:
			it.blocked, it.blockedShort = r.BlockReason(), r.shortBlock
		case catalog.ActionStart:
			it.start = true
		}
		if r.Profile != nil {
			// The slot line takes the mode line's place for this row
			// (slotLine), so the item carries the profile and its slots'
			// tiers, and no price or tier note.
			it.profile, it.cost, it.window, it.tier, it.slotTiers = r.Profile, "", "", nil, r.slotTiers
		}
		items = append(items, it)
	}
	return modelTable{header: header, items: items}
}
