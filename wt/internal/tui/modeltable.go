package tui

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// modelTable is the rendered selector: a header line (shown as the list's
// title) and one item per sorted row.
type modelTable struct {
	header string
	items  []*modelItem
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
			ids[i] = r.model.ID
		}
		refCounts = refs.Counts(ids)
	}
	return renderTable(rows, in.cfg, in.agent, refCounts, lastID)
}

// renderTable formats already-sorted rows. Columns are separated by two
// spaces and padded to the widest cell (headings included) so the header and
// every row line up; measured in runes so multi-byte names don't shift them.
func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[string]int, lastID string) modelTable {
	cost := make([]string, len(rows))
	fam := make([]string, len(rows))
	c1, c7, c30 := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
	famW, idW, costW, w1, w7, w30 := len("FAMILY"), len("MODEL"), len("COST"), len("1D"), len("7D"), len("30D")
	wS := len("STATUS")
	for i, r := range rows {
		fam[i] = r.model.Family
		if fam[i] == "" {
			fam[i] = "-"
		}
		cost[i] = "-"
		if !r.discovered {
			cost[i] = formatPerToken(r.model.Cost)
		}
		c1[i], c7[i], c30[i] = fmt.Sprint(r.counts.OneDay), fmt.Sprint(r.counts.SevenDay), fmt.Sprint(r.counts.ThirtyDay)
		famW = maxRunes(famW, fam[i])
		idW = maxRunes(idW, r.model.ID)
		costW = maxRunes(costW, cost[i])
		w1, w7, w30 = maxRunes(w1, c1[i]), maxRunes(w7, c7[i]), maxRunes(w30, c30[i])
		wS = maxRunes(wS, string(r.status))
	}
	const sep = "  "
	header := strings.Repeat(" ", rowPrefixWidth) + strings.Join([]string{
		padRunes("FAMILY", famW), padRunes("MODEL", idW), padRunes("LOC", 5), padRunes("STATUS", wS),
		padRunes("EXPOSED", 7), padRunes("RUNNING", 7), padRunes("COST", costW),
		padRunes("1D", w1), padRunes("7D", w7), padRunes("30D", w30), "SURVEY",
	}, sep)

	items := make([]*modelItem, 0, len(rows))
	for i, r := range rows {
		loc := string(r.location)
		if loc == "" {
			loc = "-"
		}
		line := strings.Join([]string{
			padRunes(fam[i], famW), padRunes(r.model.ID, idW), padRunes(loc, 5), padRunes(string(r.status), wS),
			padRunes(flag(r.exposed, "Y"), 7), padRunes(flag(r.running, "run"), 7), padRunes(cost[i], costW),
			padRunes(c1[i], w1), padRunes(c7[i], w7), padRunes(c30[i], w30),
		}, sep)
		// The last padded column would leave trailing spaces; keep them only
		// when the survey segment follows (so it stays column-aligned).
		if seg := survey.FormatPickerSegment(r.stats); seg != "" {
			line += sep + seg
		} else {
			line = strings.TrimRight(line, " ")
		}
		it := &modelItem{model: r.model, line: line, marked: lastID != "" && r.model.ID == lastID, ref: refs[r.model.ID], row: r}
		if !r.launchable() {
			it.blocked = r.notLaunchableHint()
		}
		if cfg != nil {
			route, err := cfg.ResolveRoute(r.model, agents.ProtocolsFor(agent))
			switch {
			case r.discovered && err == nil && (route.Litellm || route.Forced):
				// A discovered model is not in LiteLLM's model_list: routing
				// it through the proxy cannot work, so it is unselectable.
				it.exception = "(not in LiteLLM)"
				it.blocked = "discovered model " + r.model.ID + " is not in LiteLLM — turn LiteLLM routing off (modelman litellm off) to use it"
			case errors.Is(err, config.ErrLitellmUnconfigured):
				it.exception = "(litellm required)"
			case err != nil:
				it.exception = "(unavailable)"
			case route.Forced:
				it.exception = "(via proxy)"
			}
		}
		items = append(items, it)
	}
	return modelTable{header: header, items: items}
}
