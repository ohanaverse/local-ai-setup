package tuilayout

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
)

// ColSep separates two columns of a table.
const ColSep = "  "

// TitleRoom is how much wider than a table's header its list must be for
// bubbles to draw that header whole. The list's title bar appends two spaces
// to the title (the gap before a status message, there even when the message
// is empty) and cuts the result, with an ellipsis, to the list's width less
// the one column it reserves for its spinner. A header closer to the edge
// than this loses the end of its last heading, or keeps it and gains a stray
// "…" where the two spaces were cut.
//
// The rows need no such room: a delegate whose title styles have no padding
// (tui.ThemedListDelegate) draws a row whole up to the list's full width.
const TitleRoom = 3

// Columns is one table's column layout, shared by its header and every one
// of its rows so that they always show the same columns. A column is shown
// whole or not at all — in the header and in every row alike — which keeps
// the columns aligned and means the terminal's edge never cuts one in half.
//
// A table is a bubbles list whose title is the header and whose items each
// render one row through the shared Columns; FitTo narrows it to the list's
// width (TableItem).
type Columns struct {
	// Heads are the column headings, each padded to its column's width. The
	// last may be left unpadded.
	Heads []string
	// Widths are the columns' widths in runes.
	Widths []int
	// Tail is the widest text any row appends after its columns (a note such
	// as "(via proxy)" and the space before it).
	Tail int
	// Prefix is how many columns every row prints before its first cell (a
	// marker, a count). The header is indented by as much.
	Prefix int
	// DropOrder lists the columns a narrow list gives up, by index, first to
	// go first. A column that is not in it is never dropped.
	DropOrder []int

	// shown is which columns are drawn, one entry per heading. Read it
	// through visible, never directly: a Columns built as a literal has none.
	shown []bool
}

// NewColumns returns a layout with every column shown.
func NewColumns(heads []string, widths []int, prefix int, dropOrder []int) *Columns {
	c := &Columns{Heads: heads, Widths: widths, Prefix: prefix, DropOrder: dropOrder}
	c.showAll()
	return c
}

// showAll shows every column again, one entry per heading.
func (c *Columns) showAll() {
	if len(c.shown) != len(c.Heads) {
		c.shown = make([]bool, len(c.Heads))
	}
	for i := range c.shown {
		c.shown[i] = true
	}
}

// visible is shown, with every column shown when nothing has fitted the
// layout yet. The fields are exported, so a Columns can be written as a
// literal instead of through NewColumns; without this it would draw an empty
// header and empty rows, and say nothing.
func (c *Columns) visible() []bool {
	if len(c.shown) != len(c.Heads) {
		c.showAll()
	}
	return c.shown
}

// Shown reports whether column i is drawn at the width last fitted.
func (c *Columns) Shown(i int) bool { return c.visible()[i] }

// Fit chooses the columns to show in a list of the given width. It starts
// from the whole table and gives up columns in DropOrder until what is left
// fits: every row (Width) within the list's width, and the header with
// TitleRoom to spare, which the list's title bar needs to draw it whole. If
// the columns that are never dropped are still too wide — a very narrow
// terminal, or a very long id — they stay and the list cuts the line at its
// right edge, the header up to three columns before the rows: that is the
// one case in which content is cut, and no cell is abbreviated to avoid it.
func (c *Columns) Fit(width int) {
	c.showAll()
	for _, drop := range c.DropOrder {
		if c.Width() <= width && utf8.RuneCountInString(c.Header())+TitleRoom <= width {
			return
		}
		c.shown[drop] = false
	}
}

// Width is the widest a row can be with the columns now shown: the prefix,
// the columns at their full width, and the longest note after them.
func (c *Columns) Width() int {
	w, n := c.Prefix+c.Tail, 0
	for i, shown := range c.visible() {
		if shown {
			w += c.Widths[i]
			n++
		}
	}
	return w + len(ColSep)*(n-1)
}

// Header is the header line for the columns now shown, indented by Prefix,
// with the last heading's padding trimmed.
func (c *Columns) Header() string {
	var cells []string
	for i, shown := range c.visible() {
		if shown {
			cells = append(cells, c.Heads[i])
		}
	}
	return strings.Repeat(" ", c.Prefix) + strings.TrimRight(strings.Join(cells, ColSep), " ")
}

// Line is one row's cells for the columns now shown, without the prefix. The
// row's trailing spaces are trimmed: the padding of its last cell, and the
// separator before a last cell that is empty.
func (c *Columns) Line(cells []string) string {
	var out []string
	for i, shown := range c.visible() {
		if shown {
			out = append(out, cells[i])
		}
	}
	return strings.TrimRight(strings.Join(out, ColSep), " ")
}

// TableItem is a list item that is a row of a table: it draws itself through
// the Columns its whole table shares.
type TableItem interface {
	// TableColumns returns the table's shared layout, or nil for an item
	// that is not part of one.
	TableColumns() *Columns
}

// fitColumns narrows or widens the table in l to a list of the given width:
// it picks the columns that fit (Columns.Fit) and sets the list's title, the
// header, to match. The rows redraw from the shared layout by themselves, so
// nothing is rebuilt: the cursor and any filter are untouched. A list with
// no table rows is left alone.
func fitColumns(l *list.Model, width int) {
	for _, it := range l.Items() {
		if ti, ok := it.(TableItem); ok {
			if cols := ti.TableColumns(); cols != nil {
				cols.Fit(width)
				l.Title = cols.Header()
				return
			}
		}
	}
}

// PadRunes pads s with spaces to w runes; a longer s is returned as it is.
func PadRunes(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// MaxRunes is the rune count of the longest of ss, and at least min.
func MaxRunes(min int, ss ...string) int {
	w := min
	for _, s := range ss {
		if n := utf8.RuneCountInString(s); n > w {
			w = n
		}
	}
	return w
}
