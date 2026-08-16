package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/registry"
)

// modelItem adapts a config.Model to a list.Item for the model browser.
type modelItem struct {
	model config.Model
}

// FilterValue returns the model ID so the list's built-in fuzzy filter
// narrows by ID.
func (m modelItem) FilterValue() string { return m.model.ID }

// Title renders the model ID — the primary identifier users search for.
func (m modelItem) Title() string { return m.model.ID }

// Description renders the metadata columns (provider, location, source,
// tags). Location and Source are typed config enums; we cast via
// string(). ProviderID is a foreign key into the Providers list —
// there is no separate Provider field on the model.
func (m modelItem) Description() string {
	tags := strings.Join(m.model.Tags, ",")
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%-10s %-6s %-10s %s",
		m.model.ProviderID,
		string(m.model.Location),
		string(m.model.Source),
		tags,
	)
}

// buildModelItems wraps a slice of models into list.Items.
func buildModelItems(models []config.Model) []list.Item {
	items := make([]list.Item, 0, len(models))
	for _, m := range models {
		items = append(items, modelItem{model: m})
	}
	return items
}

// refreshBrowser rebuilds the browser list from the discovery cache plus
// the current tag and source filters. We snapshot registry.Discover(cfg)
// once per browser-open into m.browserCache; subsequent filter toggles
// only re-run the cheap in-memory filter, not the shell/HTTP discovery
// calls.
//
// If the terminal window size is unknown (width/height still zero
// before any WindowSizeMsg arrived), refreshBrowser skips the list
// build. The next WindowSizeMsg will trigger a rebuild.
func (m *model) refreshBrowser() {
	if m.browserCache == nil {
		m.browserCache = registry.Discover(m.cfg)
	}
	if m.width <= 0 || m.height <= 0 {
		return
	}
	filtered := registry.FilterByTag(m.browserCache, m.browserTag)
	filtered = registry.FilterBySource(filtered, m.browserSourceForCycle())
	m.browser = list.New(buildModelItems(filtered), list.NewDefaultDelegate(),
		m.width-2, m.height-2)
	m.browser.Title = "Model browser"
}

// browserSourceForCycle maps the sourceCycle int to a config.Source
// (empty = no source filter).
func (m *model) browserSourceForCycle() config.Source {
	switch m.sourceCycle % 3 {
	case 1:
		return config.SourceCurated
	case 2:
		return config.SourceDiscovered
	default:
		return ""
	}
}

// browserView renders the model browser screen when the user has opened
// it. It includes the list, the optional filter hint, and our own
// keybind hints (bubbles/list.KeyMap is a struct and does not implement
// help.KeyMap in the version we use, so we render our own footer).
func (m *model) browserView() string {
	if m.width <= 0 || m.height <= 0 {
		return "model browser (waiting for window size)"
	}
	view := m.browser.View()
	if m.browserTag != "" || m.browserSourceForCycle() != "" {
		parts := []string{}
		if m.browserTag != "" {
			parts = append(parts, "tag="+m.browserTag)
		}
		if src := m.browserSourceForCycle(); src != "" {
			parts = append(parts, "source="+string(src))
		}
		view += "\nfilter: " + strings.Join(parts, " ")
	}
	view += "\n[f] tag filter   [c] source filter   [enter] pick   [esc] back"
	return view
}
