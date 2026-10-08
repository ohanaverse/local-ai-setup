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

// TestFitToFitsATableInsideItsFrame verifies FitTo, given a list of table
// rows inside a frame, sets the list's title to the header of the columns
// that fit and leaves the whole view inside the terminal at every size wt
// supports — the header and a row still on screen. This is the path both
// programs' tables take; the launcher's own fit tests cover its screens, and
// this covers the shared rule without them.
func TestFitToFitsATableInsideItsFrame(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			cols := testColumns(0)
			var items []list.Item
			for i := 0; i < 30; i++ {
				items = append(items, testRow{cols: cols, cells: []string{
					PadRunes("model-"+strings.Repeat("x", i%6), 12), PadRunes("local", 5), PadRunes("ok", 7), PadRunes("1.0 GB", 6), "note",
				}})
			}
			delegate := list.NewDefaultDelegate()
			delegate.ShowDescription = false
			delegate.SetSpacing(0)
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
			if width == 40 && strings.Contains(view, "SIZE") {
				t.Errorf("%dx%d: SIZE should have been dropped at this width:\n%s", width, height, view)
			}
			if width == 120 && !strings.Contains(view, "NOTE") {
				t.Errorf("%dx%d: every column fits at this width:\n%s", width, height, view)
			}
		}
	}
}
