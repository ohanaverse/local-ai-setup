package tui

import (
	"fmt"

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
// agent and each registered command appears once. Callers wrap the result
// in a bubbles/list picker; tests range over the items directly.
//
// PR 3 reverts the PR 2 sort.Strings determinism: the picker lists every
// registered driver (in agents.Names() map range order, so nondeterministic)
// after the configured agents. The deterministic-ordering test was removed
// in PR 3b. width/height are kept in the signature for test compatibility
// (app_test.go calls buildAgentList(cfg, w, h)) but unused — the picker is
// sized by the bubbles/list constructor in selectedEntryMsg, not here.
func buildAgentList(cfg *config.Config, width, height int) []list.Item {
	items := make([]list.Item, 0)
	seen := map[string]bool{}

	// Configured agents first (so they sort before any unknown commands).
	for _, a := range cfg.Agents {
		if seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		items = append(items, agentItem{name: a.Name, command: agents.IsCommand(a.Name)})
	}

	// Any registered driver not already listed (e.g. a future command
	// driver that wasn't pre-declared in config).
	for _, n := range agents.Names() {
		if seen[n] {
			continue
		}
		seen[n] = true
		items = append(items, agentItem{name: n, command: agents.IsCommand(n)})
	}

	_ = width
	_ = height
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
