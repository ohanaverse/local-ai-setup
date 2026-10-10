package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// How a list-backed screen is fitted to the terminal is internal/tuilayout's
// to say (it is shared with `wt config`): a screen describes the lines around
// its list as a listFrame, the same function its View renders with, and fitTo
// measures that frame for the list's size. This file holds the launcher's own
// frames, and the names its code and tests have always used for the shared
// helpers.

// listFrame renders a screen around its list's view (tuilayout.ListFrame).
type listFrame = tuilayout.ListFrame

func clip(s string, width int) string { return tuilayout.Clip(s, width) }

// modeLine is the line under the launcher's model table: the LiteLLM mode
// and, after it, the highlighted row's price (costNote; "" for a row with
// none). A price is shown whole or not at all, because the footer is clipped
// at the terminal's edge with no sign of the cut, and a price cut there
// reads as another price (0.33 as 0). So when the two do not fit the width
// together the price takes the line and the mode gives way (at 40 columns:
// under "LiteLLM: off (direct)" for every price longer than 12 characters,
// which is all but the shortest, and under "LiteLLM: on" for a price with
// many digits); when the price does not fit even alone, the line is the
// mode, as for a row with no price.
func modeLine(mode, cost string, width int) string {
	switch {
	case cost == "":
		return mode
	case lipgloss.Width(mode)+3+lipgloss.Width(cost) <= width:
		return mode + "   " + cost
	case lipgloss.Width(cost) <= width:
		return cost
	}
	return mode
}

func listExtent(l list.Model, avail int) (width, floor int) { return tuilayout.ListExtent(l, avail) }

func fitList(height, minList int, frames ...listFrame) (listFrame, int) {
	return tuilayout.FitList(height, minList, frames...)
}

func frameSides(frame listFrame) int { return tuilayout.FrameSides(frame) }

func fitTo(l *list.Model, termWidth, termHeight int, frames ...listFrame) {
	tuilayout.FitTo(l, termWidth, termHeight, frames...)
}

func drawnFrame(l *list.Model, termHeight int, frames ...listFrame) listFrame {
	return tuilayout.DrawnFrame(l, termHeight, frames...)
}

// pickerPadX is the model picker's horizontal padding on each side. It is
// what frameSides measures beside the table and what modelFrames clips its own
// text to, so the two must be the same number: a frame wider than the columns
// its text was clipped to pushes the list's last columns off the edge.
const pickerPadX = 2

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
// given up by choice; see tuilayout.FitList for what happens when even that is
// too much.
func (m *model) modelFrames() []listFrame {
	// The columns left between the picker's side padding: its own text is
	// clipped to them, and the table is sized to them (frameSides measures
	// this padding back off the frame). Derived from pickerPadX, never a
	// literal, so the two cannot drift apart.
	inner := m.width - 2*pickerPadX
	build := func(margin, withHeader, withFooter bool) listFrame {
		return func(listView string) string {
			pad := lipgloss.NewStyle().Padding(0, pickerPadX)
			if margin {
				pad = lipgloss.NewStyle().Padding(1, pickerPadX)
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
			// And the highlighted row's price in force: the COST column is
			// given up on a narrow terminal (a table with an OpenRouter id
			// gives it up at 80 columns), and this line is not. It is on
			// the mode line, not a line of its own, so the footer is as tall
			// as it was and nothing about the fit changes.
			if it, ok := m.models.SelectedItem().(*modelItem); ok {
				mode = modeLine(mode, it.cost, inner)
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
			// A launch/config/ollama error set on the model phase must be
			// visible; phaseModelView previously dropped m.status, making a failed
			// launch look like "nothing happens" when Enter was pressed.
			//
			// It is wrapped to the columns the picker has, never cut at the
			// edge: a status is a sentence that ends in its reason — why a
			// route was not written, whether wt saw an eviction coming — and
			// the engine's lines after a start are wider than 80 columns
			// (#275; #209 was the same cut, on the agent picker). The lines
			// it takes are taken from the margin, the header and the footer
			// in that order, like any status; one too tall even for the
			// sparest layout has the screen to itself (statusAlone).
			if m.status != "" {
				body = clip(ErrorStyle(m.theme).Render(tuilayout.WrapText(m.status, inner)), inner) + "\n\n" + body
			}
			return pad.Render(body)
		}
	}
	return []listFrame{build(true, true, true), build(false, true, true), build(false, false, true), build(false, false, false)}
}

// agentFrames are the agent+command picker's layouts: the worktree path and
// any status above the list, the key hints below.
//
// The status is wrapped to the terminal's width, not clipped. This is the
// screen a refused -M pin comes back to, and its reason ends in what to do —
// for an mlx_lm_server pairing, the llmbench command with the pairing's own
// target and draft, which no terminal width reaches on one line — so a
// status cut at the right edge shows the complaint and not the fix (#209).
//
// A terminal too short for all of it loses the path line first, for the
// reason modelFrames gives: it repeats the user's last choice, the status
// does not. Only one too short for the wrapped status and the list together
// gets the status unwrapped, each line cut at the edge with an ellipsis to
// say so.
func (m *model) agentFrames() []listFrame {
	build := func(withPath, wrapped bool) listFrame {
		return func(listView string) string {
			header := ""
			if withPath {
				header = clip("directory: "+m.selectedPath, m.width) + "\n\n"
			}
			if m.status != "" {
				status := "status: " + m.status
				if wrapped {
					header += clip(tuilayout.WrapText(status, m.width), m.width) + "\n\n"
				} else {
					header += cutLines(status, m.width) + "\n\n"
				}
			}
			footer := "\n" + clip("[↑/↓] navigate   [enter] continue   [esc] back", m.width)
			return header + listView + footer
		}
	}
	return []listFrame{build(true, true), build(false, true), build(true, false), build(false, false)}
}

// cutLines is clip that says when it cut: a line of s wider than width loses
// its end to an ellipsis, where clip ends it at the edge with no sign that
// anything is missing. A width that is not positive cuts nothing.
func cutLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		switch {
		case lipgloss.Width(line) <= width:
		case width == 1:
			lines[i] = "…"
		default:
			lines[i] = clip(line, width-1) + "…"
		}
	}
	return strings.Join(lines, "\n")
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
	case phaseOllamaWarn:
		m.fitTo(&m.ollamaWarnModel, m.choiceFrame())
	}
}

// fitTo sizes l to the room its frames leave in this model's window.
func (m *model) fitTo(l *list.Model, frames ...listFrame) {
	fitTo(l, m.width, m.height, frames...)
}
