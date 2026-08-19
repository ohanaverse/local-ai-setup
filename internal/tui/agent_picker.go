package tui

import (
	"fmt"
	"sort"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// agentItem is one row in the phaseAgent picker. command distinguishes
// agents (require model layer) from commands (launch directly).
type agentItem struct {
	name    string
	command bool
}

func (a agentItem) FilterValue() string { return a.name }

func (a agentItem) Title() string {
	if a.command {
		return a.name + "  (command)"
	}
	return a.name + "  (agent)"
}

func (a agentItem) Description() string {
	if a.command {
		return "no model; runs passthrough commands or interactive shell"
	}
	return "agent: launches with a model"
}

// buildAgentList constructs the agent+command picker rows. Each configured
// agent and each registered command appears once, in a stable order:
// configured agents in config order, then commands alphabetically. Callers
// wrap the result in a bubbles/list picker; tests range over the items
// directly.
func buildAgentList(cfg *config.Config) []list.Item {
	items := make([]list.Item, 0)
	seen := map[string]bool{}

	// Configured agents first (config order is deterministic).
	for _, a := range cfg.Agents {
		if seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		items = append(items, agentItem{name: a.Name, command: agents.IsCommand(a.Name)})
	}

	// Any registered command driver not already listed (e.g. a future
	// command driver that wasn't pre-declared in config). Plain agents
	// must appear in cfg.Agents to be launchable, so unconfigured agent
	// drivers are deliberately omitted — listing them created dead-end
	// rows that errored "agent not found" on Enter.
	commands := []string{}
	for _, n := range agents.Names() {
		if !agents.IsCommand(n) || seen[n] {
			continue
		}
		seen[n] = true
		commands = append(commands, n)
	}
	// Sort so the tail rows are stable across runs (agents.Names() ranges
	// a map, so its order is nondeterministic).
	sort.Strings(commands)
	for _, n := range commands {
		items = append(items, agentItem{name: n, command: true})
	}
	return items
}

// phaseAgentView renders the agent+command picker: the worktree path
// header, the picker list itself, and a footer describing the keybinds.
// The picker list is built by selectedEntryMsg via buildAgentList and
// stored on m.agentList; phaseAgentView only formats the surrounding chrome.
// m.status is rendered above the list when set, so errors from the Enter
// handler (config error, empty model catalog, launch failure) are visible
// instead of silently swallowed.
func (m *model) phaseAgentView() string {
	header := fmt.Sprintf("directory: %s\n\n", m.selectedPath)
	body := m.agentList.View()
	if m.status != "" {
		body = errorStyle.Render(m.status) + "\n" + body
	}
	footer := "\n[↑/↓] navigate   [enter] continue   [esc] back"
	return header + body + footer
}
