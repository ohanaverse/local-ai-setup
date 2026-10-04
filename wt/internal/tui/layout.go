package tui

import (
	"fmt"

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

// listFrame renders a screen around its list's view: everything the screen
// prints above and below the list.
type listFrame func(listView string) string

const (
	// minTableListHeight is the least height at which bubbles renders a table
	// list (one-line rows: the model picker) within the height it was given:
	// the title bar (2), one row, the pagination dots (2) and the help line
	// (2). Asked for less, it still draws those seven lines.
	minTableListHeight = 7
	// minChoiceListHeight is the same floor for a list of title+description
	// items (three lines each with their spacing): the agent picker and the
	// choice prompts.
	minChoiceListHeight = 9
)

// fitList chooses how a screen is laid out in a terminal of the given height
// and how tall its list may be. frames are the screen's layouts in order of
// preference, fullest first; the first one that leaves the list at least
// minList lines is used, and the list gets every line that frame does not.
//
// When even the sparest frame leaves less than minList, the list is kept at
// minList anyway — a list bubbles cannot draw is no picker at all — and the
// view is taller than the terminal. Bubble Tea then drops its top lines, so
// what is lost is whatever that frame still prints above the list, while the
// list and the key hints under it stay.
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

// modelFrames are the model picker's layouts: the agent/tag header above the
// table, the mode line and key hints below it, a status line on top when one
// is set, and a one-line margin above and below.
//
// A terminal too short for all of it loses, in this order: the top and bottom
// margin (blank lines, so nothing is lost), then the agent/tag header. The
// header goes before the status line because it repeats a choice the user
// just made, while a status is news — a failed start, a route warning — that
// exists nowhere else on screen. The table, the mode line and the key hints
// are never dropped.
func (m *model) modelFrames() []listFrame {
	build := func(margin, withHeader bool) listFrame {
		return func(listView string) string {
			pad := lipgloss.NewStyle().Padding(0, 2)
			if margin {
				pad = lipgloss.NewStyle().Padding(1, 2)
			}
			headerStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenHeader))
			dimStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
			header := ""
			if withHeader {
				header = headerStyle.Render(fmt.Sprintf("agent : %s\ntag   : %s\n", m.agent, m.tag))
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
			footer := dimStyle.Render(fmt.Sprintf("\n%s\n[↑/↓] navigate   [enter] launch or start   [q] quit", mode))
			body := header + listView + footer
			// A launch/config/session/ollama error set on the model phase must be
			// visible; phaseModelView previously dropped m.status, making a failed
			// launch look like "nothing happens" when Enter was pressed.
			if m.status != "" {
				body = ErrorStyle(m.theme).Render(m.status) + "\n\n" + body
			}
			return pad.Render(body)
		}
	}
	return []listFrame{build(true, true), build(false, true), build(false, false)}
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
				header = fmt.Sprintf("directory: %s\n\n", m.selectedPath)
			}
			if m.status != "" {
				header += "status: " + m.status + "\n\n"
			}
			footer := "\n[↑/↓] navigate   [enter] continue   [esc] back"
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
		body := listView + "\n[enter] choose   [esc] back"
		if m.status != "" {
			body = ErrorStyle(m.theme).Render(m.status) + "\n\n" + body
		}
		return body
	}
}

// ollamaWarnFrame is the ollama availability prompt's layout: its choices and
// the key hints below.
func ollamaWarnFrame(listView string) string {
	return listView + "\n[enter] choose   [esc] back"
}

// fitLists sizes the list of the screen that is showing to the room its frame
// leaves. Update calls it after every message rather than at each place a
// size could change, because those places are many and easy to miss: a window
// resize, a status line appearing or being cleared (two lines each way), a
// rebuilt table, a phase entered from a phase that was sized differently. A
// list sized one message late is a header pushed off the screen.
//
// The worktree list and the replace-confirm prompt are not here: they print at
// most one line besides their list, which the window-minus-two size they are
// given already leaves room for.
func (m *model) fitLists() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	switch m.phase {
	case phaseModel:
		_, h := fitList(m.height, minTableListHeight, m.modelFrames()...)
		m.models.SetSize(m.width-2, h)
	case phaseAgent:
		_, h := fitList(m.height, minChoiceListHeight, m.agentFrames()...)
		m.agentList.SetSize(m.width-2, h)
	case phaseResume:
		_, h := fitList(m.height, minChoiceListHeight, m.resumeFrame())
		m.resume.choices.SetSize(m.width-2, h)
	case phaseOllamaWarn:
		_, h := fitList(m.height, minChoiceListHeight, ollamaWarnFrame)
		m.ollamaWarnModel.SetSize(m.width-2, h)
	}
}
