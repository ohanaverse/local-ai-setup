package configeditor

import (
	"fmt"
	"sort"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
	"github.com/ohanaverse/agent-worktree/internal/tui"
)

// agentItem adapts config.Agent to list.Item. command marks command agents
// like shell, which never show an installed check. configured indicates the
// agent has an entry in config.toml. issue is a human-readable problem.
type agentItem struct {
	agent      config.Agent
	command    bool
	configured bool
	installed  bool
	issue      string
}

func (a agentItem) FilterValue() string { return a.agent.Name }
func (a agentItem) Title() string {
	var marker string
	switch {
	case a.command:
		marker = "command"
	case !a.configured:
		marker = "✗ not configured"
	case a.installed:
		marker = "✓ installed"
	default:
		marker = "✗ not installed"
	}
	providers := config.TagsToString(a.agent.SupportedProviders)
	if providers == "" {
		providers = "-"
	}
	return fmt.Sprintf("%s  (%s)  %s", a.agent.Name, providers, marker)
}
func (a agentItem) Description() string {
	if a.issue != "" {
		return a.issue
	}
	if a.agent.DefaultProvider != "" {
		return "default: " + a.agent.DefaultProvider
	}
	return ""
}

// buildAgentsList constructs the agents list from the shared
// agents.ListEntries helper. It merges configured agents with registered
// drivers, preserves command classification and issue state, and applies the
// configeditor-specific commands-first display order.
func buildAgentsList(theme themes.Theme, width, height int, cfg *config.Config) list.Model {
	entries := agents.ListEntries(cfg, agents.Installed)
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		ag, err := cfg.AgentByName(e.Name)
		if err != nil {
			ag = &config.Agent{Name: e.Name}
		}
		it := agentItem{
			agent:      *ag,
			command:    e.Command,
			configured: e.Configured,
			installed:  e.Installed,
			issue:      e.Issue,
		}
		items = append(items, it)
	}

	// Sort: commands first, then alphabetical. agents.ListEntries returns
	// a purely alphabetical list; the configeditor applies its own display
	// order here.
	sort.SliceStable(items, func(i, j int) bool {
		ai := items[i].(agentItem)
		aj := items[j].(agentItem)
		if ai.command != aj.command {
			return ai.command
		}
		return ai.agent.Name < aj.agent.Name
	})

	l := list.New(items, tui.ThemedListDelegate(theme), width, height)
	l.Title = "Agents"
	l.SetShowStatusBar(false)
	return l
}
