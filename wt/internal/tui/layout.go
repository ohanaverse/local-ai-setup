package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// This file is the one place a list-backed screen's height is worked out.
//
// Bubble Tea draws a view that is taller than the terminal by dropping lines
// from its TOP. Every screen here puts its header and its status line at the
// top and sizes a bubbles list underneath, so a list sized without counting
// the lines around it does not produce a scrolled or clipped list: it silently
// removes the header and the status. The model picker did exactly that — its
// list was the window minus two, under six to eight lines of its own — so its
// agent/tag header and every status it ever set were never on screen.
//
// The rule that prevents it: a screen describes the lines around its list as
// a listFrame, the same function its View renders with, and fitList measures
// that frame to get the list's height. The count cannot drift from the view,
// because it IS the view, rendered around an empty list.

// The same holds sideways: a line wider than the terminal is cut at the right
// edge, so a list sized without counting the columns its frame puts beside it
// loses its last columns past the edge instead of truncating them with an
// ellipsis inside it. frameSides measures those columns from the frame, too.
//
// View and fitLists each ask frameFor which frame to use, and agree only
// because both read the same model state. So after changing state by hand (a
// test setting m.status, say), go through Update before measuring View():
// until then the list is still sized for the old state.

// listFrame renders a screen around its list's view: everything the screen
// prints above, below and beside the list.
type listFrame func(listView string) string

// clip cuts every line of s to at most width columns, which is what the
// terminal does to a line that is too long — the text simply ends at the edge;
// nothing wraps, so the line count the height fit relies on does not change.
// Frames clip their own free text (a status line, a path, the key hints on a
// narrow terminal) so that it cannot make the frame wider than the terminal
// and push the list's columns off the edge with it. A width that is not
// positive (no size reported yet, or no room at all) clips nothing.
func clip(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// listExtent measures what l really draws when it is given the columns avail
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
//     pagination line and its help: seven lines for the model table, nine for
//     a prompt of title+description choices, about twelve with the full help
//     (`?`) open.
//
// Both are measured, by rendering a copy of the list squeezed to one line,
// rather than kept as constants, because they follow the list's own state and
// styles: a constant floor is wrong the moment the help is expanded. The copy
// is sized twice for the reason sizeList gives (two passes reach its fixed
// point; the third there only confirms it).
//
// The search gives up after listWidthSlack columns: if the list is still too
// wide by then (a terminal narrower than a single key binding), it is given
// avail and the terminal cuts its lines at the edge, as it always did.
func listExtent(l list.Model, avail int) (width, floor int) {
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

// listWidthSlack bounds listExtent's search. The widest step it has to get
// past is one help-line key binding ("↓/j down • ", eleven columns).
const listWidthSlack = 16

// fitList chooses how a screen is laid out in a terminal of the given height
// and how tall its list may be. frames are the screen's layouts in order of
// preference, fullest first; the first one that leaves the list at least
// minList lines (its floor, from listExtent) is used, and the list gets every line that
// frame does not.
//
// When even the sparest frame leaves less than minList, the list is given
// minList anyway — bubbles would draw that many lines whatever it was told —
// and the view is taller than the terminal. Bubble Tea then drops lines from
// the top of the view: first whatever the sparest frame prints above the list
// (a status line), and once that is gone the list's own top lines, starting
// with its title — for the model table, the column header. The rows nearest
// the bottom and the key hints under the list are what remain.
//
// Before the terminal has reported a size (height <= 0) the fullest frame is
// used; nothing is drawn from it until a size arrives.
func fitList(height, minList int, frames ...listFrame) (listFrame, int) {
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

// frameSides is the number of columns frame puts beside its list: padding, a
// margin, any prefix. It is measured by rendering the frame around a probe
// line wider than anything else the frame prints, so that the probe's line is
// the widest; what the frame's width exceeds the probe's by is beside the list.
func frameSides(frame listFrame) int {
	probe := strings.Repeat("x", lipgloss.Width(frame(""))+1)
	return lipgloss.Width(frame(probe)) - len(probe)
}

// modelFrames are the model picker's layouts: the agent/tag header above the
// table, the mode line and key hints below it, a status line on top when one
// is set, and a one-line margin above and below.
//
// A terminal too short for all of it loses, in this order: the top and bottom
// margin (blank lines, so nothing is lost); then the agent/tag header; then
// the mode line and key hints under the table. The header goes before the
// status line because it repeats a choice the user just made, while a status
// is news — a failed start, a route warning — that exists nowhere else on
// screen. The footer goes last of the three, and in practice only when the
// list's full help (`?`) is open on a short terminal — the expanded help
// needs about twelve lines by itself and shows the keys anyway — or on a
// terminal of fewer than nine lines. The table and the status line are never
// given up by choice; see fitList for what happens when even that is too much.
func (m *model) modelFrames() []listFrame {
	// The columns left between the picker's side padding: its own text is
	// clipped to them, and the table is sized to them (frameSides).
	inner := m.width - 4
	build := func(margin, withHeader, withFooter bool) listFrame {
		return func(listView string) string {
			pad := lipgloss.NewStyle().Padding(0, 2)
			if margin {
				pad = lipgloss.NewStyle().Padding(1, 2)
			}
			headerStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenHeader))
			dimStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
			header := ""
			if withHeader {
				header = clip(headerStyle.Render(fmt.Sprintf("agent : %s\ntag   : %s", m.agent, m.tag)), inner) + "\n"
			}
			// Mode footer: the picker is where an off/on surprise surfaces (a row
			// marked "(via proxy)" under direct mode), so name the current mode on
			// the same screen. Falls back to direct when cfg is nil (view-only
			// tests).
			mode := "LiteLLM: off (direct)"
			if m.cfg != nil && m.cfg.IsLitellm() {
				mode = "LiteLLM: on"
			}
			// Enter's effect depends on the highlighted row — launch for a cloud or
			// already-running row, start through the lifecycle engine for a
			// non-running local one — so the hint names both rather than claiming
			// Enter always launches.
			footer := ""
			if withFooter {
				footer = "\n" + clip(dimStyle.Render(fmt.Sprintf("%s\n[↑/↓] navigate   [enter] launch or start   [q] quit", mode)), inner)
			}
			body := header + listView + footer
			// A launch/config/session/ollama error set on the model phase must be
			// visible; phaseModelView previously dropped m.status, making a failed
			// launch look like "nothing happens" when Enter was pressed.
			if m.status != "" {
				body = clip(ErrorStyle(m.theme).Render(m.status), inner) + "\n\n" + body
			}
			return pad.Render(body)
		}
	}
	return []listFrame{build(true, true, true), build(false, true, true), build(false, false, true), build(false, false, false)}
}

// agentFrames are the agent+command picker's layouts: the worktree path and
// any status above the list, the key hints below. A terminal too short for
// both loses the path line, for the reason modelFrames gives: it repeats the
// user's last choice, the status does not.
func (m *model) agentFrames() []listFrame {
	build := func(withPath bool) listFrame {
		return func(listView string) string {
			header := ""
			if withPath {
				header = clip("directory: "+m.selectedPath, m.width) + "\n\n"
			}
			if m.status != "" {
				header += clip("status: "+m.status, m.width) + "\n\n"
			}
			footer := "\n" + clip("[↑/↓] navigate   [enter] continue   [esc] back", m.width)
			return header + listView + footer
		}
	}
	return []listFrame{build(true), build(false)}
}

// resumeFrame is the resume prompt's layout: the status line above its
// choices, in the picker's style and position — it is the route check's note
// and any resume warning, which the user would otherwise only see after
// backing out to the picker — and the key hints below.
func (m *model) resumeFrame() listFrame {
	return func(listView string) string {
		body := listView + "\n" + clip("[enter] choose   [esc] back", m.width)
		if m.status != "" {
			body = clip(ErrorStyle(m.theme).Render(m.status), m.width) + "\n\n" + body
		}
		return body
	}
}

// worktreeFrame is the worktree list's layout: a reload error or a status
// above the list, when there is one.
func (m *model) worktreeFrame() listFrame {
	return func(listView string) string {
		if m.listError != "" {
			return clip(ErrorStyle(m.theme).Render("error: "+m.listError), m.width) + "\n" + listView
		}
		// A pinned --agent that errors (config error, empty model catalog) sets
		// m.status while staying on the worktree list; render it so the failure
		// is visible instead of silently swallowed by the list view.
		if m.status != "" {
			return clip(ErrorStyle(m.theme).Render(m.status), m.width) + "\n" + listView
		}
		return listView
	}
}

// choiceFrame is the layout of a plain choice prompt (the ollama availability
// warning, the replace confirmation): its choices and the key hints below.
func (m *model) choiceFrame() listFrame {
	return func(listView string) string {
		return listView + "\n" + clip("[enter] choose   [esc] back", m.width)
	}
}

// fitLists sizes the list of the screen that is showing to the room its frame
// leaves. Update calls it after every message rather than at each place a
// size could change, because those places are many and easy to miss: a window
// resize, a status line appearing or being cleared (two lines each way), a
// rebuilt table, the help being expanded, a phase entered from a phase that
// was sized differently. A list sized one message late is a header pushed off
// the screen.
func (m *model) fitLists() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	switch m.phase {
	case phaseModel:
		m.fitTo(&m.models, m.modelFrames()...)
	case phaseList:
		if m.ready {
			m.fitTo(&m.list, m.worktreeFrame())
		}
	case phaseReplaceConfirm:
		m.fitTo(&m.replace.choices, m.choiceFrame())
	case phaseAgent:
		m.fitTo(&m.agentList, m.agentFrames()...)
	case phaseResume:
		m.fitTo(&m.resume.choices, m.resumeFrame())
	case phaseOllamaWarn:
		m.fitTo(&m.ollamaWarnModel, m.choiceFrame())
	}
}

// frameFor is the frame l is rendered in at the current window size, and the
// size l must have inside it. View and fitTo both come through here, so the
// frame a list was sized for is the frame it is drawn in.
func (m *model) frameFor(l *list.Model, frames ...listFrame) (frame listFrame, width, height int) {
	// The side columns are the same for every layout of a screen — the
	// layouts differ in the lines they print, not in their padding — so the
	// fullest one stands for all.
	// Never a width below one: bubbles is not given zero or a negative size.
	width, floor := listExtent(*l, max(1, m.width-frameSides(frames[0])))
	frame, height = fitList(m.height, floor, frames...)
	return frame, width, height
}

// fitTo sizes l to the room its frame leaves.
func (m *model) fitTo(l *list.Model, frames ...listFrame) {
	_, width, height := m.frameFor(l, frames...)
	// A model table shows the columns that fit this width; other lists have
	// no table and are left alone.
	fitTableColumns(l, width)
	sizeList(l, width, height)
}

// sizeList tells l its size, as many times as it takes for l to draw itself
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
func sizeList(l *list.Model, width, height int) {
	for pass := 0; pass < 3; pass++ {
		pages, perPage, pageKeys := l.Paginator.TotalPages, l.Paginator.PerPage, l.KeyMap.NextPage.Enabled()
		l.SetSize(width, height)
		if l.Paginator.TotalPages == pages && l.Paginator.PerPage == perPage && l.KeyMap.NextPage.Enabled() == pageKeys {
			break
		}
	}
}
