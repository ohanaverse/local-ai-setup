package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// installed is a test seam wrapping agents.Installed. Production code uses
// the real PATH lookup; tests override it to control the installed state
// deterministically without depending on the host's installed binaries.
var installed = agents.Installed

// agentItem is one row in the phaseAgent picker. command distinguishes
// agents (require model layer) from commands (launch directly). issue is a
// short, human-readable problem that prevents launch ("" = launchable).
type agentItem struct {
	name    string
	command bool
	issue   string
}

func (a agentItem) FilterValue() string { return a.name }

func (a agentItem) Title() string {
	if a.command {
		return a.name + "  (command)"
	}
	return a.name + "  (agent)"
}

func (a agentItem) Description() string {
	if a.issue != "" {
		return a.issue
	}
	if a.command {
		return "no model; runs passthrough commands or interactive shell"
	}
	return "agent: launches with a model"
}

// buildAgentList constructs the agent+command picker rows from the shared
// agents.ListEntries helper. Each configured agent and registered command
// appears once, sorted alphabetically; command classification and issue text
// are preserved on each row. The installed check is threaded through the
// tui.installed seam so tests can stub it deterministically.
func buildAgentList(cfg *config.Config) []list.Item {
	entries := agents.ListEntries(cfg, installed)
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		it := agentItem{name: e.Name, command: e.Command}
		if !e.Command {
			it.issue = e.Issue
		}
		items = append(items, it)
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
