package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
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
// a row that needs litellm but modelman.toml's [litellm] url/api_key aren't
// configured, "(not in LiteLLM)" for a discovered (registry-less) row whose route
// would go through LiteLLM, which also sets blocked, "(unavailable)" for any other route resolution failure).
// blocked, when non-empty, is the hint the agent flow's model phase shows on
// Enter instead of launching or starting: a local model that is not on disk,
// a local provider wt has no lifecycle backend for, or a discovered row whose
// route would go through LiteLLM (see rowAction/blockReason). A non-running
// local model is deliberately NOT one of these — it is a start row. It is the
// agent flow only that honors it: PickModel (wt smoke's standalone picker)
// selects the highlighted row unconditionally, since its rows come from a
// cross-agent eligible union where choosing a row this flow would refuse is
// a legitimate diagnostic choice.
type modelItem struct {
	model     config.Model
	line      string
	marked    bool
	ref       int
	exception string
	blocked   string // non-empty: the agent flow's Enter shows this instead of launching or starting
	start     bool   // Enter starts the model through the lifecycle engine
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
// narrows by both family and ID. Deliberately excludes the marker prefix:
// the user never typed it, so it would only pollute match ranking.
func (m modelItem) FilterValue() string { return m.line }

// Title renders the compact one-line model representation with the ref
// column (issue #73's "in use" count) prepended before the last-launched
// marker prefix, and any exception note appended after the line.
func (m modelItem) Title() string {
	prefix := refColumn(m.ref)
	line := m.line
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
// e.g. " 0.1234  0.2345  0.3456", matching modelman's COST column
// (_format_per_token in modelman/src/modelman/screens/models.py). Each price
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
// it sits flush with the row text. Bubbles pads the title and its title bar by
// default; both must be cleared (keeping the bottom spacing) or the header
// columns shift relative to the rows. Shared by every table-backed list so the
// agent-flow picker and wt smoke's PickModel cannot drift.
func styleTableTitle(l *list.Model, theme themes.Theme) {
	l.Styles.Title = lipgloss.NewStyle().Foreground(theme.Token(themes.TokenDim))
	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
}

// clampModelSelection guards against bubbles v1.0.0 leaving
// m.Index() outside [0, len(VisibleItems())) after a filter
// narrows the list. With dividers gone there is no
// direction-of-travel walk or divider-skipping — just the clamp.
func clampModelSelection(m *model) tea.Cmd {
	visible := m.models.VisibleItems()
	if len(visible) == 0 {
		return nil
	}
	if i := m.models.Index(); i < 0 || i >= len(visible) {
		m.models.Select(0)
	}
	return nil
}

// phaseModelView renders the model picker screen: the list of
// agent+tag-compatible models, an agent/tag header, and a footer
// describing the keybinds. The picker IS the agent+model screen —
// there is no separate browser.
func (m *model) phaseModelView() string {
	pad := lipgloss.NewStyle().Padding(1, 2)
	headerStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenHeader))
	dimStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	header := headerStyle.Render(fmt.Sprintf("agent : %s\ntag   : %s\n", m.agent, m.tag))
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
	body := header + m.models.View() + footer
	// A launch/config/session/ollama error set on the model phase must be
	// visible; phaseModelView previously dropped m.status, making a failed
	// launch look like "nothing happens" when Enter was pressed.
	if m.status != "" {
		body = ErrorStyle(m.theme).Render(m.status) + "\n\n" + body
	}
	return pad.Render(body)
}
