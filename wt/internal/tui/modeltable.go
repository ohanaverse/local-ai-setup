package tui

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// modelTable is the rendered selector: a header line (shown as the list's
// title) and one item per sorted row. header is the full table's; the items
// share one tableColumns, which decides how much of the table is drawn at the
// width the list is given (fitTableColumns).
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

// colSep separates two columns.
const colSep = "  "

// colDropOrder is the order in which columns are given up when the table is
// wider than its list: the survey segment first, then usage from the longest
// window to the shortest, cost, location and family. MODEL, STATUS and RUNNING
// are not in it: they say what a row is and whether Enter launches or starts
// it, so they are never dropped.
var colDropOrder = []int{colSurvey, col30D, col7D, col1D, colCost, colLoc, colFamily}

// tableColumns is one table's column layout, shared by its header and every
// one of its rows so that they always show the same columns. A column is shown
// whole or not at all — in the header and in every row alike — which keeps the
// columns aligned and means the terminal's edge never cuts one in half.
type tableColumns struct {
	heads  [numCols]string // column headings, padded to the column's width (SURVEY is not padded: it is last)
	widths [numCols]int    // each column's width in runes
	// tail is the widest text any row appends after its columns: the
	// exception note ("(via proxy)") and the space before it.
	tail  int
	shown [numCols]bool
}

// fit chooses the columns to show in a list of the given width. It starts
// from the whole table and gives up columns in colDropOrder until what is
// left fits: every row (width) within the list's width, and the header with
// tableTitleRoom to spare, which the list's title bar needs to draw it whole.
// If MODEL, STATUS and RUNNING alone are still too wide — a very narrow
// terminal, or a very long model id — those three stay and the list cuts the
// line at its right edge, the header up to three columns before the rows:
// that is the one case in which content is cut, and the model id is never
// abbreviated to avoid it.
func (c *tableColumns) fit(width int) {
	for i := range c.shown {
		c.shown[i] = true
	}
	for _, drop := range colDropOrder {
		if c.width() <= width && utf8.RuneCountInString(c.header())+tableTitleRoom <= width {
			return
		}
		c.shown[drop] = false
	}
}

// width is the widest a row can be with the columns now shown: the prefix,
// the columns at their full width, and the longest exception note.
func (c *tableColumns) width() int {
	w, n := rowPrefixWidth+c.tail, 0
	for i, shown := range c.shown {
		if shown {
			w += c.widths[i]
			n++
		}
	}
	return w + len(colSep)*(n-1)
}

// header is the header line for the columns now shown.
func (c *tableColumns) header() string {
	var cells []string
	for i, shown := range c.shown {
		if shown {
			cells = append(cells, c.heads[i])
		}
	}
	// With every column shown the last heading is SURVEY, which is not
	// padded; with it dropped the last one is, and the padding is trimmed.
	return strings.Repeat(" ", rowPrefixWidth) + strings.TrimRight(strings.Join(cells, colSep), " ")
}

// line is one row's columns for the layout now shown: the padded cells, then
// the survey segment when that column is shown and the row has one. A row
// that ends in a padded cell has its trailing spaces trimmed, exactly as the
// full table's rows do.
func (c *tableColumns) line(cells [numCols]string) string {
	var out []string
	for i, shown := range c.shown {
		if shown && i != colSurvey {
			out = append(out, cells[i])
		}
	}
	line := strings.Join(out, colSep)
	if c.shown[colSurvey] && cells[colSurvey] != "" {
		return line + colSep + cells[colSurvey]
	}
	return strings.TrimRight(line, " ")
}

const rowPrefixWidth = 4 // ref column (2) + rotation marker (2), composed by modelItem.Title()

func padRunes(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func maxRunes(min int, ss ...string) int {
	w := min
	for _, s := range ss {
		if n := utf8.RuneCountInString(s); n > w {
			w = n
		}
	}
	return w
}

func flag(b bool, yes string) string {
	if b {
		return yes
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
	// shown; whoever sizes the list narrows it (fitTableColumns).
	cols := &tableColumns{
		heads: [numCols]string{
			padRunes("FAMILY", famW), padRunes("MODEL", idW), padRunes("LOC", 5), padRunes("STATUS", wS),
			padRunes("RUNNING", 7), padRunes("COST", costW),
			padRunes("1D", w1), padRunes("7D", w7), padRunes("30D", w30), "SURVEY",
		},
		widths: [numCols]int{famW, idW, 5, wS, 7, costW, w1, w7, w30, len("SURVEY")},
	}
	cols.fit(int(^uint(0) >> 1))
	header := cols.header()

	items := make([]*modelItem, 0, len(rows))
	for i, r := range rows {
		loc := string(r.Location)
		if loc == "" {
			loc = "-"
		}
		// The last padded column would leave trailing spaces; tableColumns.line
		// keeps them only when the survey segment follows (so it stays
		// column-aligned).
		cells := [numCols]string{
			padRunes(fam[i], famW), padRunes(r.Model.ID, idW), padRunes(loc, 5), padRunes(string(r.Status), wS),
			padRunes(flag(r.Running, "run"), 7), padRunes(cost[i], costW),
			padRunes(c1[i], w1), padRunes(c7[i], w7), padRunes(c30[i], w30),
			survey.FormatPickerSegment(r.stats),
		}
		cols.widths[colSurvey] = maxRunes(cols.widths[colSurvey], cells[colSurvey])
		it := &modelItem{model: r.Model, line: cols.line(cells), cells: cells, cols: cols, marked: lastID != "" && r.Model.ID == lastID, ref: refs[r.Model.ID]}
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
			cols.tail = max(cols.tail, 1+utf8.RuneCountInString(it.exception))
		}
		items = append(items, it)
	}
	return modelTable{header: header, items: items}
}
