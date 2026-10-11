package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// newUsageStore is a seam for tests: production uses realNewUsageStore (the
// default config dir); tests swap it to isolate from the real usage.jsonl.
var newUsageStore = realNewUsageStore

// realNewUsageStore is the production implementation of the newUsageStore
// seam: a Store rooted at the default config dir.
func realNewUsageStore() usage.Store { return usage.NewStore() }

// newRefcountStore is a seam for tests: production uses
// realNewRefcountStore (the default config dir); tests swap it to isolate
// from the real refcount.jsonl.
var newRefcountStore = realNewRefcountStore

// realNewRefcountStore is the production implementation of the
// newRefcountStore seam: a Store rooted at the default config dir.
func realNewRefcountStore() refcount.Store { return refcount.NewStore() }

// modelItem adapts a config.Model to a list.Item for the model picker.
// The compact representation is baked onto .line; marked records the
// rotation's last-launched row and ref is the live "in use" session count
// (issue #73) — both are composed into the rendered prefix by Title()
// rather than baked into .line, so FilterValue (fuzzy matching) and every
// .line consumer see the unprefixed format. exception, when non-empty,
// appends a per-row deviation note in Title() (e.g. "(via proxy)" for a
// row protocol negotiation forces through LiteLLM, "(litellm required)" for
// a row that needs litellm but wt's [litellm] url/api_key aren't
// configured, "(not in LiteLLM)" for an Unmapped cloud row whose route
// would go through LiteLLM, which also sets blocked, "(unavailable)" for any other route resolution failure).
// blocked, when non-empty, is the hint the agent flow's model phase shows on
// Enter instead of launching or starting: a local provider wt has no lifecycle
// backend for, or an Unmapped cloud row whose route would go through LiteLLM (see
// catalog.Row Action/BlockReason). A non-running local model is deliberately
// NOT one of these — it is a start row — and a local model that is not on disk
// is not one either: it has no row at all. Every
// picker honors it: the agent flow shows it on Enter, and the standalone
// PickStartModel (wt smoke, wt start) refuses to select a blocked
// row and shows it as a notice instead.
//
// line is always the FULL table's row, whatever the terminal's width: it is
// what the filter matches, so a query for a family or a cost still finds its
// row when that column is not drawn. What is drawn comes from cells through
// cols, the layout the whole table shares (nil for an item built by hand, in
// which case the full line is drawn).
type modelItem struct {
	model     config.Model
	line      string
	cells     []string
	cols      *tuilayout.Columns
	marked    bool
	ref       int
	exception string
	blocked   string // non-empty: the agent flow's Enter shows this instead of launching or starting
	start     bool   // Enter starts the model through the lifecycle engine
	// cost is the row's price in force as the launcher's mode line names it
	// for the highlighted row (costNote); "" for a row with no per-token
	// price.
	cost string
}

// markerMarked is the last-launched row's 2-rune prefix; markerBlank keeps
// every other row aligned. Plain ASCII is deliberate: Unicode geometric
// shapes (e.g. U+25B6 ▶) are East Asian Ambiguous width and render 2 cells
// wide on CJK-configured terminals, which would shift the marked row's
// columns relative to every other row.
const (
	markerMarked = "> "
	markerBlank  = "  "
)

// refClamp is the highest digit the ref column ever renders — a single
// glyph keeps the column width fixed regardless of how many concurrent
// sessions are actually using a model.
const refClamp = 9

// refColumn renders the ref count's 2-rune prefix: "<digit> " when ref > 0
// (clamped at refClamp), or two blank spaces when the model is unused.
// Kept the same width as markerMarked/markerBlank so every row's columns
// line up regardless of ref/marked state.
func refColumn(ref int) string {
	if ref <= 0 {
		return "  "
	}
	if ref > refClamp {
		ref = refClamp
	}
	return fmt.Sprintf("%d ", ref)
}

// FilterValue returns the full line so the list's built-in fuzzy filter
// narrows by both family and ID — the full table's line, not the columns
// drawn at the current width, so hiding a column on a narrow terminal does
// not change what a query can find. Deliberately excludes the marker prefix:
// the user never typed it, so it would only pollute match ranking.
func (m modelItem) FilterValue() string { return m.line }

// Title renders the compact one-line model representation with the ref
// column (issue #73's "in use" count) prepended before the last-launched
// marker prefix, and any exception note appended after the line.
func (m modelItem) Title() string {
	prefix := refColumn(m.ref)
	line := m.line
	if m.cols != nil {
		line = m.cols.Line(m.cells)
	}
	if m.exception != "" {
		line += " " + m.exception
	}
	if m.marked {
		return prefix + markerMarked + line
	}
	return prefix + markerBlank + line
}

// Description returns empty because the compact view is one line per item.
// list.DefaultDelegate.Render still calls this method; we keep it to satisfy
// the interface while rendering nothing.
func (m modelItem) Description() string { return "" }

// formatPerToken returns per-token costs as three space-delimited values,
// e.g. " 0.1234  0.2345  0.3456": the COST column's format. Each price
// is right-aligned to a 2-digit (leading-space-padded) integer part plus 4
// decimal places, so prices over $10/million don't break column alignment.
// A missing individual price renders as a 7-dash placeholder ("-------"),
// matching that width. When all three prices are absent it returns a
// hyphen.
func formatPerToken(cost config.ModelCost) string {
	const missing = "-------"
	format7dec := func(p *float64) string {
		if p == nil {
			return missing
		}
		return fmt.Sprintf("%7.4f", *p)
	}
	in := format7dec(cost.InputPricePerMillion)
	cache := format7dec(cost.CachePricePerMillion)
	out := format7dec(cost.OutputPricePerMillion)
	if in == missing && cache == missing && out == missing {
		return "-"
	}
	return fmt.Sprintf("%s %s %s", in, cache, out)
}

// styleTableTitle styles a list whose Title is the selector table's header so
// it sits flush with the row text, in the theme's dim colour. The reset is
// tuilayout.StyleTableTitle, which `wt config`'s Models tab calls too; this
// is the launcher's name for it, used by the agent-flow picker and wt smoke's
// PickStartModel.
func styleTableTitle(l *list.Model, theme themes.Theme) {
	tuilayout.StyleTableTitle(l, theme.Token(themes.TokenDim))
}

// TableColumns is the layout this row's table shares, which is how
// tuilayout.FitTo finds the table in a list and shows the columns that fit
// its width: nothing is rebuilt on a resize, so the inventory is not probed
// again and the cursor, the marked row and any filter are untouched. nil for
// an item built by hand.
func (m modelItem) TableColumns() *tuilayout.Columns { return m.cols }

// clampModelSelection guards against bubbles v1.0.0 leaving
// m.Index() outside [0, len(VisibleItems())) after a filter
// narrows the list.
func clampModelSelection(m *model) {
	visible := m.models.VisibleItems()
	if len(visible) == 0 {
		return
	}
	if i := m.models.Index(); i < 0 || i >= len(visible) {
		m.models.Select(0)
	}
}

// phaseModelView renders the model picker screen: the list of
// agent+tag-compatible models, an agent/tag header, and a footer
// describing the keybinds. The picker IS the agent+model screen —
// there is no separate browser. The lines around the list are modelFrames'
// (layout.go), the same frames the list's height is measured from.
//
// A status too tall to share the terminal with the table has the screen to
// itself (statusAlone).
func (m *model) phaseModelView() string {
	view := drawnFrame(&m.models, m.height, m.modelFrames()...)(m.models.View())
	if !m.statusMayBeAlone() || lipgloss.Height(view) <= m.height {
		return view
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	return tuilayout.MessageAlone("", tuilayout.WrapText(m.status, m.width), statusAloneHint, m.width, m.height, dim)
}

// statusAloneHint is the last line of a status that has the screen.
const statusAloneHint = "press a key to go back to the models"

// statusAlone reports whether the picker's status has the screen to itself:
// it is so tall that even the sparest frame, with the table at its least
// height, is taller than the terminal. Bubble Tea drops such a view's lines
// from the top, which is where the status is, so the report of a start that
// unloaded several models (two lines a model, #275) would be the part that
// goes. Alone it is wrapped to the width, cut in its middle behind a marker
// if it is still too tall (tuilayout.MessageAlone), and the next key takes it
// down (Update). It is what `wt config`'s Models tab does with a refusal.
//
// It is a measurement of the frame as drawn, not a count of lines, so it
// stays right when the frame's other lines change. Not while the list's full
// help is open: that view is allowed to run over a short terminal, and the
// key that would be taken for "go back" is the one that closes the help. Nor
// while a filter is being typed, where every key is a character of it.
//
// Update asks this for every key the picker gets, so the frame is drawn only
// when there is a status that could have the screen (statusMayBeAlone).
func (m *model) statusAlone() bool {
	return m.statusMayBeAlone() &&
		lipgloss.Height(drawnFrame(&m.models, m.height, m.modelFrames()...)(m.models.View())) > m.height
}

// statusMayBeAlone is the part of statusAlone that costs nothing: whether
// there is a status, a known height and a view the rule applies to. What is
// left is to measure the frame.
func (m *model) statusMayBeAlone() bool {
	return m.status != "" && m.height > 0 && !m.models.Help.ShowAll && !m.models.SettingFilter()
}
