package tui

import (
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
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
	colLoc
	colStatus
	colRunning
	colCost
	col1D
	col7D
	col30D
	colSurvey
	numCols
)

// colDropOrder is the order in which columns are given up when the table is
// wider than its list (tuilayout.Columns.DropOrder): the survey segment first, then usage from the longest
// window to the shortest, cost, location and family. MODEL, STATUS and RUNNING
// are not in it: they say what a row is and whether Enter launches or starts
// it, so they are never dropped.
var colDropOrder = []int{colSurvey, col30D, col7D, col1D, colCost, colLoc, colFamily}

const rowPrefixWidth = 4 // ref column (2) + rotation marker (2), composed by modelItem.Title()

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

// buildTable is the one entry point the pickers use: build rows, sort them,
// look up in-use counts, render.
func buildTable(in tableInput, refs refcount.Store, lastID string) modelTable {
	rows := buildRows(in)
	sortRows(rows)
	var refCounts map[string]int
	if refs != nil {
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.Model.ID
		}
		refCounts = refs.Counts(ids)
	}
	cfg := in.cfg
	if in.skipRoute {
		// No route resolution: renderTable only uses cfg to decorate/block rows
		// by launch route.
		cfg = nil
	}
	return renderTable(rows, cfg, in.agent, refCounts, lastID)
}

// resolvedRoute pairs one row's ResolveRoute result, computed ahead of
// renderTable's per-row rendering loop.
type resolvedRoute struct {
	route config.Route
	err   error
}

// renderTable formats already-sorted rows. Columns are separated by two
// spaces and padded to the widest cell (headings included) so the header and
// every row line up; measured in runes so multi-byte names don't shift them.
func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[string]int, lastID string) modelTable {
	// Resolve every row's route up front, in parallel, instead of one at a
	// time in the rendering loop below: cfg.ResolveRoute may dial an exec:
	// secret_ref subprocess (up to execSecretTimeout) per distinct provider,
	// and resolving rows sequentially would let one slow/hung provider's
	// helper add its full cost on top of every other row's otherwise-fast
	// resolution. config.ResolveSecret memoizes per ref with its own
	// per-ref locking (internal/config), so concurrent rows for the SAME
	// provider still only pay for one subprocess run; this only
	// parallelizes across DISTINCT providers. All goroutines are joined
	// (wg.Wait) before this function returns, so nothing is left running in
	// the background afterward.
	var routes []resolvedRoute
	if cfg != nil {
		routes = make([]resolvedRoute, len(rows))
		var wg sync.WaitGroup
		for i, r := range rows {
			wg.Add(1)
			go func(i int, m config.Model) {
				defer wg.Done()
				route, err := cfg.ResolveRoute(m, agents.ProtocolsFor(agent))
				routes[i] = resolvedRoute{route: route, err: err}
			}(i, r.Model)
		}
		wg.Wait()
	}

	cost := make([]string, len(rows))
	fam := make([]string, len(rows))
	c1, c7, c30 := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
	famW, idW, costW, w1, w7, w30 := len("FAMILY"), len("MODEL"), len("COST"), len("1D"), len("7D"), len("30D")
	wS := len("STATUS")
	for i, r := range rows {
		fam[i] = r.Model.Family
		if fam[i] == "" {
			fam[i] = "-"
		}
		cost[i] = "-"
		if !r.Discovered {
			cost[i] = formatPerToken(r.Model.Cost)
		}
		c1[i], c7[i], c30[i] = fmt.Sprint(r.counts.OneDay), fmt.Sprint(r.counts.SevenDay), fmt.Sprint(r.counts.ThirtyDay)
		famW = maxRunes(famW, fam[i])
		idW = maxRunes(idW, r.Model.ID)
		costW = maxRunes(costW, cost[i])
		w1, w7, w30 = maxRunes(w1, c1[i]), maxRunes(w7, c7[i]), maxRunes(w30, c30[i])
		wS = maxRunes(wS, string(r.Status))
	}
	// One layout for the header and every row. It starts with every column
	// shown; whoever sizes the list narrows it (tuilayout.FitTo).
	cols := tuilayout.NewColumns(
		[]string{
			padRunes("FAMILY", famW), padRunes("MODEL", idW), padRunes("LOC", 5), padRunes("STATUS", wS),
			padRunes("RUNNING", 7), padRunes("COST", costW),
			padRunes("1D", w1), padRunes("7D", w7), padRunes("30D", w30), "SURVEY",
		},
		[]int{famW, idW, 5, wS, 7, costW, w1, w7, w30, len("SURVEY")},
		rowPrefixWidth, colDropOrder,
	)
	header := cols.Header()

	items := make([]*modelItem, 0, len(rows))
	for i, r := range rows {
		loc := string(r.Location)
		if loc == "" {
			loc = "-"
		}
		// The last padded column would leave trailing spaces; Columns.Line
		// trims them, so they stay only when the survey segment follows (and
		// keeps it column-aligned).
		cells := []string{
			padRunes(fam[i], famW), padRunes(r.Model.ID, idW), padRunes(loc, 5), padRunes(string(r.Status), wS),
			padRunes(runningText(r.Row), 7), padRunes(cost[i], costW),
			padRunes(c1[i], w1), padRunes(c7[i], w7), padRunes(c30[i], w30),
			survey.FormatPickerSegment(r.stats),
		}
		cols.Widths[colSurvey] = maxRunes(cols.Widths[colSurvey], cells[colSurvey])
		it := &modelItem{model: r.Model, line: cols.Line(cells), cells: cells, cols: cols, marked: lastID != "" && r.Model.ID == lastID, ref: refs[r.Model.ID]}
		switch r.Action() {
		case catalog.ActionBlock:
			it.blocked = r.BlockReason()
		case catalog.ActionStart:
			it.start = true
		}
		if cfg != nil {
			route, err := routes[i].route, routes[i].err
			switch {
			case r.RefusedByRoute(route, err):
				// An Unmapped cloud model is not in LiteLLM's model_list:
				// routing it through the proxy cannot work, so it is
				// unselectable. The rule itself lives in
				// catalog.Row.RefusedByRoute, shared with the non-TUI pin and
				// `wt smoke`.
				it.exception = "(not in LiteLLM)"
				it.blocked = r.RouteRefusal(route, err)
				it.start = false
			case err != nil:
				it.exception = "(unavailable)"
				if errors.Is(err, config.ErrLitellmUnconfigured) {
					it.exception = "(litellm required)"
				}
				// A start row whose launch cannot resolve must not start: the
				// server would spawn (possibly replacing a running model) for a
				// launch that then fails at ResolveRoute. Launch rows stay as
				// they were and report the error on Enter.
				if it.start {
					it.start = false
					it.blocked = it.model.ID + " cannot be launched: " + err.Error()
				}
			case route.Forced:
				it.exception = "(via proxy)"
			}
		}
		if it.exception != "" {
			cols.Tail = max(cols.Tail, 1+utf8.RuneCountInString(it.exception))
		}
		items = append(items, it)
	}
	return modelTable{header: header, items: items}
}
