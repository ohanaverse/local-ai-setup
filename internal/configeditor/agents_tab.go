package configeditor

import (
	"fmt"
	"sort"
	"strings"

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
	case a.issue == "not configured":
		marker = "✗ not configured"
	case a.installed:
		marker = "✓ installed"
	default:
		marker = "✗ not installed"
	}
	providers := strings.Join(a.agent.SupportedProviders, ", ")
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

// buildAgentsList constructs the agents list. It merges configured agents
// with every registered driver (e.g. opencode may be registered but not
// configured), skips installed checks for commands, and sorts commands
// first then alphabetically.
func buildAgentsList(theme themes.Theme, width, height int, cfg *config.Config) list.Model {
	seen := map[string]bool{}
	items := make([]list.Item, 0)

	// Collect every configured agent and registered driver once each.
	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		ag, err := cfg.AgentByName(name)
		configured := err == nil
		if !configured {
			// AgentByName returns an error when missing; create a placeholder.
			ag = &config.Agent{Name: name}
		}
		it := agentItem{agent: *ag, command: agents.IsCommand(name), configured: configured}
		if it.command {
			// No installed check for commands.
		} else if !configured {
			it.issue = "not configured"
		} else {
			it.installed = agents.Installed(name)
		}
		items = append(items, it)
	}
	for _, a := range cfg.Agents {
		add(a.Name)
	}
	for _, n := range agents.Names() {
		add(n)
	}

	// Sort: commands first, then alphabetical.
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
