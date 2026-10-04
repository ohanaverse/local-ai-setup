package tui

import (
	"fmt"
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

// refusedTable is a table whose widest row ends in an exception note: a cloud
// model whose provider has no LiteLLM mapping, with routing through the proxy
// on, reads "(not in LiteLLM)" after its columns. The second row has no note.
// The agent is "" so no row is forced through the proxy (that would add
// "(via proxy)" to both and print the forced-route notice).
func refusedTable(t *testing.T) modelTable {
	t.Helper()
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "zcloud", Location: config.LocationCloud, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "https://zcloud.example"}},
			{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	rows := []tableRow{
		{
			Row: catalog.Row{
				Model: config.Model{ID: "zcloud/glm-5.5-turbo-preview-2026-09", ProviderID: "zcloud", ModelName: "glm-5.5-turbo-preview-2026-09", Family: "glm",
					Cost: config.ModelCost{InputPricePerMillion: price(1), CachePricePerMillion: price(0.1), OutputPricePerMillion: price(4)}},
				Location: config.LocationCloud, Status: catalog.StatusOK, Unmapped: true,
			},
			counts: usage.UsageCounts{OneDay: 2, SevenDay: 30, ThirtyDay: 211},
		},
		{
			Row: catalog.Row{
				Model:    config.Model{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8"},
				Location: config.LocationLocal, Status: catalog.StatusOK, Running: true,
			},
		},
	}
	tbl := renderTable(rows, cfg, "", nil, "")
	if got := tbl.items[0].exception; got != "(not in LiteLLM)" || tbl.items[1].exception != "" {
		t.Fatalf("fixture: exceptions = %q / %q, want \"(not in LiteLLM)\" on the first row only", got, tbl.items[1].exception)
	}
	return tbl
}

// tablePicker is the agent flow's model picker over realisticTable in a
// terminal of the given size, with the cursor on the running local row.
func tablePicker(t *testing.T, width, height int) model {
	t.Helper()
	return pickerOver(t, realisticTable(), width, height)
}

// pickerOver is the agent flow's model picker over tbl in a terminal of the
// given size, with the cursor on the second row.
func pickerOver(t *testing.T, tbl modelTable, width, height int) model {
	t.Helper()
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

		// MODEL, STATUS and RUNNING are whole in the header and in every row
		// from 75 columns up for this table: 47 + 6 + 7, two separators and
		// the 4-column prefix make a 68-column header, the list's title bar
		// needs 3 more to draw it whole, and the picker pads 4. (The rows need
		// less — no title-bar room, and their last cell is trimmed — so they
		// are whole a few columns before the header is.) Below that the list
		// cuts the line. TestModelTableColumnBoundaries pins the exact widths.
		fits := tc.width >= 75
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

// tableCut describes what the list cut off the table in m's view, or "" when
// the header and every row are drawn whole: the header line holds the whole
// header with no ellipsis after it, and each row's line holds the whole row
// (its exception note included) with none either. bubbles marks whatever it
// cuts with "…", and nothing in a header or in these fixtures' rows contains
// one.
func tableCut(m model) string {
	lines := strings.Split(m.View(), "\n")
	whole := func(text string) bool {
		for _, line := range lines {
			if strings.Contains(line, text) {
				return !strings.Contains(line, "…")
			}
		}
		return false
	}
	if !whole(m.models.Title) {
		return "the header"
	}
	for i, it := range m.models.Items() {
		if !whole(it.(*modelItem).Title()) {
			return fmt.Sprintf("row %d", i)
		}
	}
	return ""
}

// columnBoundary is one terminal width at which a table gains a column, and
// the columns it shows from there up to the next one.
type columnBoundary struct {
	width int
	want  string
}

// assertColumnBoundaries checks each boundary at its exact width and one
// column either side: the columns shown (one column narrower still shows the
// previous boundary's), and that at all three widths the header and every row
// are drawn whole. The first boundary is the narrowest terminal that holds
// the three columns that always stay.
func assertColumnBoundaries(t *testing.T, tbl func() modelTable, boundaries []columnBoundary) {
	t.Helper()
	for i, b := range boundaries {
		for _, width := range []int{b.width - 1, b.width, b.width + 1} {
			want := b.want
			if width < b.width {
				if i == 0 {
					continue // narrower than the kept columns: something is cut
				}
				want = boundaries[i-1].want
			}
			m := pickerOver(t, tbl(), width, 24)
			if got := shownColumns(m.models.Title); got != want {
				t.Errorf("width %d: columns = %q, want %q", width, got, want)
			}
			if cut := tableCut(m); cut != "" {
				t.Errorf("width %d: %s is cut or followed by an ellipsis:\n%s", width, cut, m.View())
			}
		}
	}
}

// TestModelTableColumnBoundaries pins the exact terminal width at which each
// column of the realistic table appears, and that one column narrower it is
// still hidden — with the header and every row whole on both sides. The fit
// has to leave the list's title bar three columns beyond the header (two
// spaces it appends, one it reserves for a spinner); a fit that counted less
// kept a column one to three columns too long, and the header then read
// "RUNNIN…" or grew a stray "…" after its last heading while the rows under
// it were fine. Below the first width the three kept columns do not fit and
// the header is the first thing cut.
func TestModelTableColumnBoundaries(t *testing.T) {
	assertColumnBoundaries(t, realisticTable, []columnBoundary{
		// 68 columns of header (prefix 4, 47 + 6 + 7, two separators), the
		// title bar's 3, the picker's 4.
		{75, "MODEL STATUS RUNNING"},
		{84, "FAMILY MODEL STATUS RUNNING"},
		{91, "FAMILY MODEL LOC STATUS RUNNING"},
		{113, "FAMILY MODEL LOC STATUS RUNNING COST"},
		{120, "FAMILY MODEL LOC STATUS RUNNING COST 1D"},
		{124, "FAMILY MODEL LOC STATUS RUNNING COST 1D 7D"},
		{129, "FAMILY MODEL LOC STATUS RUNNING COST 1D 7D 30D"},
		{146, "FAMILY MODEL LOC STATUS RUNNING COST 1D 7D 30D SURVEY"},
	})
	// One column short of the first boundary the rows still fit — they need no
	// title-bar room — and the header is what is cut.
	if cut := tableCut(tablePicker(t, 74, 24)); cut != "the header" {
		t.Errorf("width 74: cut = %q, want the header cut and the rows whole", cut)
	}
}

// TestModelTableCountsTheExceptionNote pins that the note a row appends after
// its columns — "(not in LiteLLM)", "(via proxy)" — is part of the width the
// column fit counts: wherever the three kept columns and the note fit, the
// note is whole, and each further column appears only where the row has room
// for it AND the note. The note is why a blocked row cannot be chosen; a fit
// that left it out kept columns the row had no room for, and the list cut the
// row inside the note.
func TestModelTableCountsTheExceptionNote(t *testing.T) {
	tbl := func() modelTable { return refusedTable(t) }
	// The refused row with only the kept columns is 68 columns wide (prefix 4,
	// a 36-column id, STATUS, "-" and the 17 columns of the note with its
	// space), plus the picker's 4.
	const keptFit = 72
	for width := keptFit; width <= 160; width++ {
		m := pickerOver(t, tbl(), width, 24)
		if cut := tableCut(m); cut != "" {
			t.Errorf("width %d (columns %q): %s is cut", width, shownColumns(m.models.Title), cut)
		}
		if !strings.Contains(m.View(), " (not in LiteLLM)") {
			t.Errorf("width %d: view lacks the whole exception note", width)
		}
	}
	// With the note counted, each column arrives later than the header alone
	// would allow (FAMILY at 87, where the header fits from 73).
	assertColumnBoundaries(t, tbl, []columnBoundary{
		{keptFit, "MODEL STATUS RUNNING"},
		{87, "FAMILY MODEL STATUS RUNNING"},
		{94, "FAMILY MODEL LOC STATUS RUNNING"},
		{119, "FAMILY MODEL LOC STATUS RUNNING COST"},
		{123, "FAMILY MODEL LOC STATUS RUNNING COST 1D"},
		{127, "FAMILY MODEL LOC STATUS RUNNING COST 1D 7D"},
		{132, "FAMILY MODEL LOC STATUS RUNNING COST 1D 7D 30D"},
		{140, "FAMILY MODEL LOC STATUS RUNNING COST 1D 7D 30D SURVEY"},
	})
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

	// 45 is the narrowest terminal that holds the header with FAMILY: 42
	// columns, and the 3 the list's title bar needs beyond it (this picker has
	// no side padding).
	next, _ := pm.Update(tea.WindowSizeMsg{Width: 45, Height: 24})
	narrow := next.(pickModel)
	if got := shownColumns(narrow.list.Title); got != "FAMILY MODEL STATUS RUNNING" {
		t.Errorf("columns at 45 = %q, want survey, usage, cost and location dropped", got)
	}
	assertFits(t, "standalone picker", narrow.View(), 45, 24)
	if view := narrow.View(); !strings.Contains(view, "omlx/qwen3.8") || !strings.Contains(view, "RUNNING") || !strings.Contains(view, "run") || strings.Contains(view, "RUNNING…") {
		t.Errorf("view at 45 = %q, want the model id and the running column, whole", view)
	}

	next, _ = narrow.Update(tea.WindowSizeMsg{Width: 160, Height: 24})
	if got := next.(pickModel).list.Title; got != full {
		t.Errorf("header after widening = %q, want %q", got, full)
	}
}
