package tuilayout

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// testRow is a table row: it draws itself through the table's shared Columns.
type testRow struct {
	cells []string
	cols  *Columns
}

func (r testRow) FilterValue() string    { return strings.Join(r.cells, " ") }
func (r testRow) Title() string          { return strings.Repeat(" ", r.cols.Prefix) + r.cols.Line(r.cells) }
func (r testRow) Description() string    { return "" }
func (r testRow) TableColumns() *Columns { return r.cols }

// The test table: NAME and STATE are never dropped; SIZE goes first, then
// NOTE, then KIND.
const (
	tName = iota
	tKind
	tState
	tSize
	tNote
)

func testColumns(prefix int) *Columns {
	heads := []string{PadRunes("NAME", 12), PadRunes("KIND", 5), PadRunes("STATE", 7), PadRunes("SIZE", 6), "NOTE"}
	return NewColumns(heads, []int{12, 5, 7, 6, 9}, prefix, []int{tSize, tNote, tKind})
}

// TestColumnsDropInTheTablesOwnOrder verifies a table gives up its columns
// in the order it named, whole, and never the ones it did not name. The
// launcher's picker and the Models tab have different columns to protect;
// one fixed order would drop the wrong ones in one of them.
func TestColumnsDropInTheTablesOwnOrder(t *testing.T) {
	cases := []struct {
		width  int
		header string
	}{
		{200, "NAME          KIND   STATE    SIZE    NOTE"},
		{50, "NAME          KIND   STATE    SIZE    NOTE"},
		// 12+5+7+6+9 and four gaps is 47; the header needs TitleRoom more
		// than its own 42, so 47 still fits and 46 drops SIZE.
		{47, "NAME          KIND   STATE    SIZE    NOTE"},
		{46, "NAME          KIND   STATE    NOTE"},
		{38, "NAME          KIND   STATE"},
		// The rows fit 28 columns exactly, but the header (26) needs
		// TitleRoom more, so KIND goes.
		{29, "NAME          KIND   STATE"},
		{28, "NAME          STATE"},
		// Narrower than the columns that are never dropped: they stay.
		{10, "NAME          STATE"},
	}
	for _, c := range cases {
		cols := testColumns(0)
		cols.Fit(c.width)
		if got := cols.Header(); got != c.header {
			t.Errorf("width %d: header = %q, want %q", c.width, got, c.header)
		}
		if !cols.Shown(tName) || !cols.Shown(tState) {
			t.Errorf("width %d: a column that is never dropped was dropped", c.width)
		}
	}
	// A wider list gets the columns back: nothing is forgotten.
	cols := testColumns(0)
	cols.Fit(10)
	cols.Fit(200)
	if !cols.Shown(tSize) || !cols.Shown(tNote) || !cols.Shown(tKind) {
		t.Error("columns dropped at a narrow width did not come back at a wide one")
	}
}

// TestColumnsBuiltAsALiteralShowEveryColumn verifies a Columns written as a
// struct literal, without NewColumns, draws every column until it is fitted
// and then drops them in its own order like any other. Its fields are
// exported, so the literal compiles; before, it showed no column at all — an
// empty header and empty rows, with no error to say why.
func TestColumnsBuiltAsALiteralShowEveryColumn(t *testing.T) {
	built := testColumns(2)
	literal := &Columns{Heads: built.Heads, Widths: built.Widths, Prefix: built.Prefix, DropOrder: built.DropOrder}
	row := []string{PadRunes("alpha", 12), PadRunes("local", 5), PadRunes("ok", 7), PadRunes("5.2 GB", 6), "a note"}
	if got, want := literal.Header(), built.Header(); got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
	if got, want := literal.Line(row), built.Line(row); got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	if got, want := literal.Width(), built.Width(); got != want {
		t.Errorf("Width = %d, want %d", got, want)
	}
	if !(&Columns{Heads: built.Heads, Widths: built.Widths}).Shown(tNote) {
		t.Error("Shown reports a column of an unfitted literal as dropped")
	}
	literal.Fit(30)
	built.Fit(30)
	if got, want := literal.Header(), built.Header(); got != want {
		t.Errorf("narrow header = %q, want %q", got, want)
	}
	if literal.Shown(tSize) || !literal.Shown(tName) {
		t.Error("a fitted literal did not drop SIZE and keep NAME")
	}
}

// TestColumnsLineMatchesTheHeader verifies a row shows exactly the header's
// columns at every width, that a row's trailing padding is trimmed (an empty
// last cell included), and that the header is indented by the rows' prefix.
// A row that kept a column its header dropped would put values under the
// wrong heading.
func TestColumnsLineMatchesTheHeader(t *testing.T) {
	cols := testColumns(2)
	full := []string{PadRunes("alpha", 12), PadRunes("local", 5), PadRunes("ok", 7), PadRunes("5.2 GB", 6), "a note"}
	bare := []string{PadRunes("beta", 12), PadRunes("cloud", 5), PadRunes("ok", 7), PadRunes("-", 6), ""}
	if got, want := cols.Header(), "  NAME          KIND   STATE    SIZE    NOTE"; got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
	if got, want := cols.Line(full), "alpha         local  ok       5.2 GB  a note"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	if got, want := cols.Line(bare), "beta          cloud  ok       -"; got != want {
		t.Errorf("a row with an empty last cell = %q, want %q", got, want)
	}
	cols.Fit(30)
	if got, want := cols.Line(full), "alpha         ok"; got != want {
		t.Errorf("narrow line = %q, want %q", got, want)
	}
	// Width counts the prefix and the widest note rows append.
	cols.Fit(200)
	if got := cols.Width(); got != 2+47 {
		t.Errorf("Width = %d, want 49", got)
	}
	cols.Tail = 12
	if got := cols.Width(); got != 2+47+12 {
		t.Errorf("Width with a 12-column note = %d, want 61", got)
	}
}

// TestColumnsSurviveAShortRowAndNoColumns verifies a row with fewer cells
// than the table has columns is drawn with the missing ones empty, and that a
// table left with no column reports the width of what is still drawn. Columns
// holds slices where the launcher's table held fixed arrays, so the compiler
// rules out neither any more: a short row was a panic inside View, which
// takes the whole screen down, and no column at all made Width two columns
// less than the prefix and the note a row still prints.
func TestColumnsSurviveAShortRowAndNoColumns(t *testing.T) {
	cols := testColumns(0)
	short := []string{PadRunes("alpha", 12), PadRunes("local", 5)}
	if got, want := cols.Line(short), "alpha         local"; got != want {
		t.Errorf("a short row = %q, want %q", got, want)
	}
	if got := cols.Line(nil); got != "" {
		t.Errorf("a row with no cells = %q, want it empty", got)
	}

	none := NewColumns([]string{"ONLY"}, []int{4}, 2, []int{0})
	none.Tail = 5
	none.Fit(1)
	if none.Shown(0) {
		t.Fatal("the one column, which is in DropOrder, was not dropped at width 1")
	}
	if got := none.Width(); got != 2+5 {
		t.Errorf("Width with no column = %d, want 7 (the prefix and the note)", got)
	}
	if got := (&Columns{}).Width(); got != 0 {
		t.Errorf("Width of an empty table = %d, want 0", got)
	}
}

// TestFitToFitsATableInsideItsFrame verifies FitTo, given a list of table
// rows inside a frame, sets the list's title to the header of the columns
// that fit and leaves the whole view inside the terminal at every size wt
// supports — the header still on screen and a row drawn whole, no cell cut
// at the list's edge. This is the path both programs' tables take; the
// launcher's own fit tests cover its screens, and this covers the shared rule
// without them.
func TestFitToFitsATableInsideItsFrame(t *testing.T) {
	// 48 is one column more than the whole table: the width at which a row
	// that Columns.Width undercounts is the first thing cut.
	for _, width := range []int{40, 48, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			cols := testColumns(0)
			var items []list.Item
			for i := 0; i < 30; i++ {
				// Every cell fills its column, the last one included, so a
				// row is as wide as Columns.Width says a row can be.
				items = append(items, testRow{cols: cols, cells: []string{
					PadRunes("model-"+strings.Repeat("x", i%6), 12), PadRunes("local", 5), PadRunes("ok", 7), PadRunes("1.0 GB", 6), "full note",
				}})
			}
			// A table's rows are drawn whole only by a delegate whose title
			// styles have no padding (TitleRoom), as tui.ThemedListDelegate's
			// have none. Bubbles' default pads a row by two columns, which
			// Columns does not count, and would cut the last cell of a table
			// that fits exactly.
			delegate := list.NewDefaultDelegate()
			delegate.ShowDescription = false
			delegate.SetSpacing(0)
			delegate.Styles.NormalTitle = lipgloss.NewStyle()
			delegate.Styles.SelectedTitle = lipgloss.NewStyle()
			delegate.Styles.DimmedTitle = lipgloss.NewStyle()
			l := list.New(items, delegate, width, height)
			l.SetShowStatusBar(false)
			l.Styles.Title = lipgloss.NewStyle()
			l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
			frame := ListFrame(func(listView string) string {
				return Clip("a status line that is rather long and must be cut at the edge of a narrow terminal", width) + "\n\n" + listView + "\n" + Clip("keys", width)
			})
			FitTo(&l, width, height, frame)
			view := DrawnFrame(&l, height, frame)(l.View())
			if h := lipgloss.Height(view); h > height {
				t.Errorf("%dx%d: the view is %d lines tall", width, height, h)
			}
			if w := lipgloss.Width(view); w > width {
				t.Errorf("%dx%d: the view is %d columns wide", width, height, w)
			}
			if !strings.Contains(view, "NAME") || !strings.Contains(view, "STATE") || !strings.Contains(view, "model-") {
				t.Errorf("%dx%d: the header or the rows are not on screen:\n%s", width, height, view)
			}
			if first := cols.Line(items[0].(testRow).cells); !strings.Contains(view, first) {
				t.Errorf("%dx%d: the first row is not drawn whole (want %q):\n%s", width, height, first, view)
			}
			if width == 40 && strings.Contains(view, "SIZE") {
				t.Errorf("%dx%d: SIZE should have been dropped at this width:\n%s", width, height, view)
			}
			if width == 120 && !strings.Contains(view, "NOTE") {
				t.Errorf("%dx%d: every column fits at this width:\n%s", width, height, view)
			}
		}
	}
}
