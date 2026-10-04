package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

func price(v float64) *float64 { return &v }

// realisticTable is a table with the shapes real rows take: a long cloud model
// id with prices, usage and a survey segment (and the last-launched marker); a
// running local model in use by two sessions; and a discovered local model
// with no family, cost or survey.
func realisticTable() modelTable {
	rows := []tableRow{
		{
			Row: catalog.Row{
				Model: config.Model{ID: "openrouter/anthropic/claude-sonnet-4.5-thinking", ProviderID: "openrouter", ModelName: "anthropic/claude-sonnet-4.5-thinking", Family: "sonnet",
					Cost: config.ModelCost{InputPricePerMillion: price(3), CachePricePerMillion: price(0.3), OutputPricePerMillion: price(15)}},
				Location: config.LocationCloud, Status: catalog.StatusOK,
			},
			counts: usage.UsageCounts{OneDay: 3, SevenDay: 12, ThirtyDay: 104},
			stats:  survey.Stats{Answered: 12, Worked: 11, Failed: 1, RatedSpeed: 12, SpeedSum: 44, RatedQuality: 12, QualitySum: 51},
		},
		{
			Row: catalog.Row{
				Model:    config.Model{ID: "omlx/Qwen3.8-27B-Instruct-MLX-6bit", ProviderID: "omlx", ModelName: "Qwen3.8-27B-Instruct-MLX-6bit", Family: "qwen3.8"},
				Location: config.LocationLocal, Status: catalog.StatusOK, Running: true,
			},
			counts: usage.UsageCounts{OneDay: 1, SevenDay: 4, ThirtyDay: 9},
		},
		{
			Row: catalog.Row{
				Model:    config.Model{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b"},
				Location: config.LocationLocal, Status: catalog.StatusNew, Discovered: true,
			},
		},
	}
	return renderTable(rows, nil, "claude", map[string]int{"omlx/Qwen3.8-27B-Instruct-MLX-6bit": 2}, "openrouter/anthropic/claude-sonnet-4.5-thinking")
}

// The realistic table as it was rendered before columns could be dropped,
// captured from that code: the header and the three rows' titles.
const (
	goldenHeader = "    FAMILY   MODEL                                            LOC    STATUS  RUNNING  COST                     1D  7D  30D  SURVEY"
	goldenRow0   = "  > sonnet   openrouter/anthropic/claude-sonnet-4.5-thinking  cloud  ok      -         3.0000  0.3000 15.0000  3   12  104  ✓92% q4.2 s3.7 n12"
	goldenRow1   = "2   qwen3.8  omlx/Qwen3.8-27B-Instruct-MLX-6bit               local  ok      run      -                        1   4   9"
	goldenRow2   = "    -        ollama/gemma4:9b                                 local  new     -        -                        0   0   0"
)

// tablePicker is the agent flow's model picker over realisticTable in a
// terminal of the given size, with the cursor on the running local row.
func tablePicker(t *testing.T, width, height int) model {
	t.Helper()
	tbl := realisticTable()
	delegate := ThemedListDelegate(themes.Default)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	items := make([]list.Item, len(tbl.items))
	for i, it := range tbl.items {
		items[i] = it
	}
	ml := list.New(items, delegate, width-2, height-2)
	ml.Title = tbl.header
	styleTableTitle(&ml, themes.Default)
	ml.SetShowStatusBar(false)
	ml.Select(1)
	m := model{phase: phaseModel, theme: themes.Default, agent: "claude", tag: "code", models: ml}
	return resized(t, m, width, height)
}

// shownColumns lists the headings in a header line.
func shownColumns(header string) string {
	return strings.Join(strings.Fields(header), " ")
}

// TestModelTableDropsWholeColumnsInOrder pins what a model table shows when it
// is wider than its list: whole columns go, from the right-hand end of the
// drop order (survey, 30D, 7D, 1D, cost, location, family), and MODEL, STATUS
// and RUNNING always stay. Before this the terminal cut the row wherever its
// edge fell, so on an 80-column terminal a long model id pushed STATUS and
// RUNNING — whether Enter launches or starts — off the screen. Each width also
// checks that every line fits the terminal, that the three kept columns are
// whole in the header and in every row, and that the header and the rows start
// each shown column at the same place.
func TestModelTableDropsWholeColumnsInOrder(t *testing.T) {
	const all = "FAMILY MODEL LOC STATUS RUNNING COST 1D 7D 30D SURVEY"
	for _, tc := range []struct {
		width int
		want  string
	}{
		{200, all},
		{120, "FAMILY MODEL LOC STATUS RUNNING COST 1D"},
		{100, "FAMILY MODEL LOC STATUS RUNNING"},
		{80, "MODEL STATUS RUNNING"},
		{60, "MODEL STATUS RUNNING"},
		{40, "MODEL STATUS RUNNING"},
	} {
		m := tablePicker(t, tc.width, 24)
		header := m.models.Title
		if got := shownColumns(header); got != tc.want {
			t.Errorf("width %d: columns = %q, want %q", tc.width, got, tc.want)
			continue
		}
		view := m.View()
		assertFits(t, "model table", view, tc.width, 24)

		// MODEL, STATUS and RUNNING fit beside the row prefix from 76 columns
		// up for this table (47 + 6 + 7, two separators, the 4-column prefix,
		// the list's 2 and the picker's 4). Below that the list cuts the row.
		fits := tc.width >= 76
		ids := []string{"openrouter/anthropic/claude-sonnet-4.5-thinking", "omlx/Qwen3.8-27B-Instruct-MLX-6bit", "ollama/gemma4:9b"}
		cells := [][]string{{"ok", "-"}, {"ok", "run"}, {"new", "-"}}
		if fits {
			for _, h := range []string{"MODEL", "STATUS", "RUNNING"} {
				if !strings.Contains(view, h) {
					t.Errorf("width %d: view lacks the %s heading", tc.width, h)
				}
			}
		}
		for i, it := range m.models.Items() {
			title := it.(*modelItem).Title()
			if fits && !strings.Contains(view, strings.TrimRight(title, " ")) {
				t.Errorf("width %d: row %d is not whole in the view: %q", tc.width, i, title)
			}
			// Column starts: each shown heading sits over its cell.
			for _, col := range []struct{ head, cell string }{
				{"MODEL", ids[i] + " "}, {"STATUS", cells[i][0] + " "}, {"RUNNING", cells[i][1]},
			} {
				// The header and a row's title carry the same 4-column prefix,
				// and everything before these columns is ASCII, so byte
				// offsets are column offsets.
				at := strings.Index(header, col.head)
				if at < 0 || at > len(title) || !strings.HasPrefix(title[at:], col.cell) {
					t.Errorf("width %d: row %d has no %q under the %s heading at byte %d: %q / %q", tc.width, i, col.cell, col.head, at, header, title)
				}
			}
		}
	}
}

// TestModelTableUnchangedWhenItFits pins that at a width with room for
// everything the table is byte for byte what it was before columns could be
// dropped: the same columns, order and padding, the marker and ref prefixes,
// and trailing spaces trimmed on a row with no survey segment. The expected
// strings were captured from the code as it was.
func TestModelTableUnchangedWhenItFits(t *testing.T) {
	m := tablePicker(t, 200, 24)
	if m.models.Title != goldenHeader {
		t.Errorf("header = %q, want %q", m.models.Title, goldenHeader)
	}
	for i, want := range []string{goldenRow0, goldenRow1, goldenRow2} {
		it := m.models.Items()[i].(*modelItem)
		if got := it.Title(); got != want {
			t.Errorf("row %d = %q, want %q", i, got, want)
		}
		if it.FilterValue() != want[rowPrefixWidth:] {
			t.Errorf("row %d filter value = %q, want the row without its prefix", i, it.FilterValue())
		}
	}
	// A freshly built table, before any list has sized it, is the full one too.
	tbl := realisticTable()
	if tbl.header != goldenHeader || tbl.items[0].Title() != goldenRow0 {
		t.Errorf("unsized table = %q / %q, want the full table", tbl.header, tbl.items[0].Title())
	}
}

// TestModelTableResizeKeepsCursorMarkAndFilter pins that narrowing the
// terminal and widening it again only changes which columns are drawn: the
// full table comes back, and the selected row, the last-launched marker, the
// in-use digit and an applied filter are all as they were. The table is not
// rebuilt on a resize — rebuilding would mean probing the inventory again and
// risk moving the cursor to another model.
func TestModelTableResizeKeepsCursorMarkAndFilter(t *testing.T) {
	m := tablePicker(t, 200, 24)
	m.models.SetFilterText("o") // matches all three rows; stays applied
	m = resized(t, m, 200, 24)
	visible := len(m.models.VisibleItems())
	if m.models.FilterState() != list.FilterApplied || visible == 0 {
		t.Fatalf("filter state = %v with %d rows, want an applied filter with rows", m.models.FilterState(), visible)
	}
	m.models.Select(1)
	selected := selectedModelID(m)

	narrow := resized(t, m, 60, 24)
	if got := shownColumns(narrow.models.Title); got != "MODEL STATUS RUNNING" {
		t.Fatalf("narrow columns = %q, want only the three that always stay", got)
	}
	wide := resized(t, narrow, 200, 24)

	for name, got := range map[string]model{"narrow": narrow, "wide again": wide} {
		if selectedModelID(got) != selected {
			t.Errorf("%s: cursor on %q, want %q", name, selectedModelID(got), selected)
		}
		if got.models.FilterState() != list.FilterApplied || got.models.FilterValue() != "o" || len(got.models.VisibleItems()) != visible {
			t.Errorf("%s: filter = %v %q with %d rows, want the applied \"o\" with %d", name, got.models.FilterState(), got.models.FilterValue(), len(got.models.VisibleItems()), visible)
		}
		first := got.models.Items()[0].(*modelItem).Title()
		second := got.models.Items()[1].(*modelItem).Title()
		if !strings.HasPrefix(first, "  > ") || !strings.HasPrefix(second, "2   ") {
			t.Errorf("%s: prefixes = %q / %q, want the marker and the in-use digit kept", name, first[:4], second[:4])
		}
	}
	if wide.models.Title != goldenHeader || wide.models.Items()[0].(*modelItem).Title() != goldenRow0 {
		t.Errorf("after widening: header = %q, want the full table back", wide.models.Title)
	}
}

// TestModelTableFilterMatchesHiddenColumns pins that the filter still matches
// on the whole row when columns are not drawn: on a narrow terminal the FAMILY
// and LOC columns are hidden, and a user who types a family name or "local"
// must still find the row. The filter value is the full table's line, not the
// line on screen.
func TestModelTableFilterMatchesHiddenColumns(t *testing.T) {
	m := tablePicker(t, 60, 24)
	if strings.Contains(m.View(), "sonnet ") || strings.Contains(m.models.Title, "FAMILY") {
		t.Fatalf("fixture: the FAMILY column is still drawn at 60 columns: %q", m.models.Title)
	}
	for query, want := range map[string]string{
		"sonnet":  "openrouter/anthropic/claude-sonnet-4.5-thinking",
		"qwen3.8": "omlx/Qwen3.8-27B-Instruct-MLX-6bit",
		"15.0000": "openrouter/anthropic/claude-sonnet-4.5-thinking",
	} {
		m.models.SetFilterText(query)
		var got []string
		for _, it := range m.models.VisibleItems() {
			got = append(got, it.(*modelItem).model.ID)
		}
		if len(got) == 0 || got[0] != want {
			t.Errorf("filter %q matched %v, want %s first", query, got, want)
		}
	}
}

// TestStandalonePickerDropsColumnsToo pins that the picker `wt start` and
// `wt smoke` use applies the same rule: it renders the same table, so on a
// narrow terminal it must keep MODEL, STATUS and RUNNING whole as well, and
// give the full table back when widened.
func TestStandalonePickerDropsColumnsToo(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, runningOmlxSnapshot())
	cfg := modelTestConfig()
	pm := newPickModel(cfg, cfg.Models, themes.Default, false)
	full := pm.list.Title

	next, _ := pm.Update(tea.WindowSizeMsg{Width: 44, Height: 24})
	narrow := next.(pickModel)
	if got := shownColumns(narrow.list.Title); got != "FAMILY MODEL STATUS RUNNING" {
		t.Errorf("columns at 44 = %q, want survey, usage, cost and location dropped", got)
	}
	assertFits(t, "standalone picker", narrow.View(), 44, 24)
	if view := narrow.View(); !strings.Contains(view, "omlx/qwen3.8") || !strings.Contains(view, "RUNNING") || !strings.Contains(view, "run") {
		t.Errorf("view at 44 = %q, want the model id and the running column", view)
	}

	next, _ = narrow.Update(tea.WindowSizeMsg{Width: 160, Height: 24})
	if got := next.(pickModel).list.Title; got != full {
		t.Errorf("header after widening = %q, want %q", got, full)
	}
}
