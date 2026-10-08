// Package tuilayout sizes a Bubble Tea screen built around a bubbles list so
// that it is never taller or wider than the terminal, and lays out a table
// whose columns are dropped whole when it is too wide. internal/tui (the
// launcher's pickers) and internal/configeditor (`wt config`) both import it,
// so the two programs fit a terminal by one rule.
package tuilayout

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// This file is the one place a list-backed screen's height is worked out.
//
// Bubble Tea draws a view that is taller than the terminal by dropping lines
// from its TOP. Every screen sized here puts its header and its status line at the
// top and sizes a bubbles list underneath, so a list sized without counting
// the lines around it does not produce a scrolled or clipped list: it silently
// removes the header and the status. The launcher's model picker did exactly
// that — its list was the window minus two, under six to eight lines of its
// own — so its agent/tag header and every status it ever set were never on
// screen.
//
// The rule that prevents it: a screen describes the lines around its list as
// a ListFrame, the same function its View renders with, and FitList measures
// that frame to get the list's height. The count cannot drift from the view,
// because it IS the view, rendered around an empty list.

// The same holds sideways: a line wider than the terminal is cut at the right
// edge, so a list sized without counting the columns its frame puts beside it
// loses its last columns past the edge instead of truncating them with an
// ellipsis inside it. FrameSides measures those columns from the frame, too.
//
// The measuring is done once, by FitTo, in the Update that changed something;
// View does none of it. View asks DrawnFrame for the fullest frame that
// leaves the list the height it already has, which is the frame FitTo sized
// it for as long as both read the same model state. So after changing state
// by hand (a test setting a status line, say), go through Update before
// measuring View(): until then the list is still sized for the old state.

// ListFrame renders a screen around its list's view: everything the screen
// prints above, below and beside the list.
type ListFrame func(listView string) string

// Clip cuts every line of s to at most width columns, which is what the
// terminal does to a line that is too long — the text simply ends at the edge;
// nothing wraps, so the line count the height fit relies on does not change.
// Frames Clip their own free text (a status line, a path, the key hints on a
// narrow terminal) so that it cannot make the frame wider than the terminal
// and push the list's columns off the edge with it. A width that is not
// positive (no size reported yet, or no room at all) clips nothing.
func Clip(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// ListExtent measures what l really draws when it is given the columns avail
// and as little height as possible, and returns the two sizes the fit needs:
//
//   - width, the widest width to give l at which no line it draws is wider
//     than avail. A bubbles list is not exactly as wide as it is told: its
//     title bar is padded one column past the width, and its help line is cut
//     at whole key bindings, so a list told 37 to 39 draws 48 columns where
//     one told 36 draws 33. Neither rule is worth restating here, so the
//     width is found by trying: avail first, then one column less at a time.
//   - floor, the least height at which l draws itself within the height it is
//     given. Asked for less, it still draws its title bar, one item, its
//     pagination line and its help: in the launcher, seven lines for the
//     model table, nine for a prompt of title+description choices, about
//     twelve with the full help (`?`) open.
//
// Both are measured, by rendering a copy of the list squeezed to one line,
// rather than kept as constants, because they follow the list's own state and
// styles: a constant floor is wrong the moment the help is expanded. The copy
// is sized twice for the reason SizeList gives (two passes reach its fixed
// point; the third there only confirms it).
//
// The search gives up after listWidthSlack columns: if the list is still too
// wide by then (a terminal narrower than a single key binding), it is given
// avail and the terminal cuts its lines at the edge, as it always did.
func ListExtent(l list.Model, avail int) (width, floor int) {
	measure := func(w int) (rendered, height int) {
		l.SetSize(w, 1)
		l.SetSize(w, 1)
		view := l.View()
		return lipgloss.Width(view), lipgloss.Height(view)
	}
	for w := avail; w >= 1 && w > avail-listWidthSlack; w-- {
		if rendered, height := measure(w); rendered <= avail {
			return w, height
		}
	}
	_, floor = measure(avail)
	return avail, floor
}

// listWidthSlack bounds ListExtent's search. The widest step it has to get
// past is one help-line key binding ("↓/j down • ", eleven columns).
const listWidthSlack = 16

// FitList chooses how a screen is laid out in a terminal of the given height
// and how tall its list may be. frames are the screen's layouts in order of
// preference, fullest first; the first one that leaves the list at least
// minList lines (its floor, from ListExtent) is used, and the list gets every
// line that frame does not.
//
// When even the sparest frame leaves less than minList, the list is given
// minList anyway — bubbles would draw that many lines whatever it was told —
// and the view is taller than the terminal. Bubble Tea then drops lines from
// the top of the view: first whatever the sparest frame prints above the list
// (a status line), and once that is gone the list's own top lines, starting
// with its title — for a table, the column header. The rows nearest
// the bottom and the key hints under the list are what remain.
//
// Before the terminal has reported a size (height <= 0) the fullest frame is
// used; nothing is drawn from it until a size arrives.
func FitList(height, minList int, frames ...ListFrame) (ListFrame, int) {
	if height <= 0 {
		return frames[0], minList
	}
	for _, frame := range frames {
		// An empty list view still occupies one line, hence the -1.
		if h := height - (lipgloss.Height(frame("")) - 1); h >= minList {
			return frame, h
		}
	}
	return frames[len(frames)-1], minList
}

// FrameSides is the number of columns frame puts beside its list: padding, a
// margin, any prefix. It is measured by rendering the frame around a probe
// line wider than anything else the frame prints, so that the probe's line is
// the widest; what the frame's width exceeds the probe's by is beside the list.
func FrameSides(frame ListFrame) int {
	probe := strings.Repeat("x", lipgloss.Width(frame(""))+1)
	return lipgloss.Width(frame(probe)) - len(probe)
}

// FitTo sizes l to the room its frames leave in a terminal of termWidth by
// termHeight: the widest width at which l draws within the columns the frame
// leaves beside it, and the height the fullest frame that fits leaves over.
// It is the one place a list is measured (ListExtent renders up to
// listWidthSlack probes), which is why it runs in Update and never in View.
func FitTo(l *list.Model, termWidth, termHeight int, frames ...ListFrame) {
	// The side columns are the same for every layout of a screen — the
	// layouts differ in the lines they print, not in their padding — so the
	// fullest one stands for all.
	// Never a width below one: bubbles is not given zero or a negative size.
	width, floor := ListExtent(*l, max(1, termWidth-FrameSides(frames[0])))
	_, height := FitList(termHeight, floor, frames...)
	// A table shows the columns that fit this width; other lists have no
	// table and are left alone.
	fitColumns(l, width)
	SizeList(l, width, height)
}

// DrawnFrame is the frame to draw l in: the fullest one that leaves l the
// height it has. FitTo gave l the room its chosen frame left over, and every
// fuller frame leaves less than that, so this is the frame FitTo chose —
// found from the list's own height instead of by measuring the list again on
// every View (every keystroke of a filter is a View). When FitTo could fit no
// frame it gave l its floor and FitList's last resort, the sparest frame,
// which is what this returns too.
func DrawnFrame(l *list.Model, termHeight int, frames ...ListFrame) ListFrame {
	frame, _ := FitList(termHeight, l.Height(), frames...)
	return frame
}

// SizeList tells l its size, as many times as it takes for l to draw itself
// within it. bubbles' SetSize works out how many rows fit on a page from the
// lines the list prints around them, and two of those are read as they were
// BEFORE the call:
//
//   - the pagination line, one row with a single page and two with several,
//     which follows the page count the previous sizing left;
//   - the help, whose expanded form (`?`) is two rows taller once the
//     next/previous-page keys are enabled. SetSize enables them only after it
//     has counted the rows, and bubbles' own `?` handler re-paginates without
//     touching them at all.
//
// So a size that tips the list from one page to several is computed with too
// many rows per page, and the list draws one line taller than it was told —
// two more when `?` is what tipped it, since the pass that corrects the
// pagination line is the one that enables the page keys. In a view that
// otherwise fits exactly, those lines push the top of the screen off until
// the next message.
//
// Sizing again corrects it, and the loop stops at a fixed point: a pass that
// leaves the page count, the rows per page and the page keys as it found them
// read the same pagination line and help a further pass would, so a further
// pass would change nothing. The page count alone is not enough to stop on —
// `?` leaves it at two before the first pass and at two after it, with the
// rows per page still wrong.
//
// Three passes always get there. The first leaves the page keys in step with
// the page count, whatever state it started from. If it ended on several
// pages, the second reads the two-row line and the taller help — the most
// those ever take — so it fits no more rows than the first and stays on
// several pages; if it ended on one page, the second reads the least they
// take, fits no fewer rows and stays on one. Either way the second pass
// counts its rows from the state it ends in, which is the fixed point, and
// the third only observes that nothing moved.
func SizeList(l *list.Model, width, height int) {
	for pass := 0; pass < 3; pass++ {
		pages, perPage, pageKeys := l.Paginator.TotalPages, l.Paginator.PerPage, l.KeyMap.NextPage.Enabled()
		l.SetSize(width, height)
		if l.Paginator.TotalPages == pages && l.Paginator.PerPage == perPage && l.KeyMap.NextPage.Enabled() == pageKeys {
			break
		}
	}
}
