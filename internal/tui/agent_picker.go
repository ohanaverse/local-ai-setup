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
// agent and each registered command appears once. The list is ordered
// deterministically — agents alphabetically, then commands alphabetically —
// regardless of config order or the nondeterministic agents.Names() map
// iteration. Callers wrap the result in a bubbles/list picker; tests range
// over the items directly.
func buildAgentList(cfg *config.Config) []list.Item {
	seen := map[string]bool{}
	var agentRows, commandRows []agentItem

	// Collect every configured agent and registered driver once each.
	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		it := agentItem{name: name, command: agents.IsCommand(name)}
		if it.command {
			commandRows = append(commandRows, it)
		} else {
			agentRows = append(agentRows, it)
		}
	}
	for _, a := range cfg.Agents {
		add(a.Name)
	}
	for _, n := range agents.Names() {
		add(n)
	}

	// Agents first, then commands; each group sorted by name.
	sort.Slice(agentRows, func(i, j int) bool { return agentRows[i].name < agentRows[j].name })
	sort.Slice(commandRows, func(i, j int) bool { return commandRows[i].name < commandRows[j].name })

	items := make([]list.Item, 0, len(agentRows)+len(commandRows))
	for _, a := range agentRows {
		items = append(items, a)
	}
	for _, c := range commandRows {
		items = append(items, c)
	}
	return items
}

// phaseAgentView renders the agent+command picker: the worktree path
// header, the picker list itself, and a footer describing the keybinds.
// The picker list is built by selectedEntryMsg via buildAgentList and
// stored on m.agentList; phaseAgentView only formats the surrounding chrome.
func (m *model) phaseAgentView() string {
	header := fmt.Sprintf("directory: %s\n\n", m.selectedPath)
	if m.status != "" {
		header += "status: " + m.status + "\n\n"
	}
	footer := "\n[↑/↓] navigate   [enter] continue   [esc] back"
	return header + m.agentList.View() + footer
}
