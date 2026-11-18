package configeditor

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestTabBarShowsThreeTabs pins the tab bar's three names in order, so the
// Model server tab is drawn after Models and the old two-tab prefixes stay.
func TestTabBarShowsThreeTabs(t *testing.T) {
	got := tabBar(testTheme(), TabServer)
	if !strings.Contains(got, "Agents") || !strings.Contains(got, "Models") || !strings.Contains(got, "[Model server]") {
		t.Errorf("tab bar = %q, want Agents, Models and [Model server]", got)
	}
	if strings.Index(got, "Models") > strings.Index(got, "Model server") {
		t.Errorf("tab bar = %q, want Models before Model server", got)
	}
}

// TestNextTabCycles pins the one tab order that every tab key uses:
// Agents, Models, Model server, back to Agents.
func TestNextTabCycles(t *testing.T) {
	for from, want := range map[Tab]Tab{TabAgents: TabModels, TabModels: TabServer, TabServer: TabAgents} {
		if got := nextTab(from); got != want {
			t.Errorf("nextTab(%d) = %d, want %d", from, got, want)
		}
	}
}

// TestStartTabOpensTheModelServerTab checks that Options.StartTab can open the
// new tab, that its placeholder view is the tab bar, and that tab leaves it.
func TestStartTabOpensTheModelServerTab(t *testing.T) {
	m := newModel(testTheme(), nil, nil)
	m.opts, m.tab = Options{StartTab: TabServer}, TabServer
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = send(t, m, loadedMsg{cfg: nil})
	if !strings.Contains(m.View(), "[Model server]") {
		t.Fatalf("want the Model server tab open, view:\n%s", m.View())
	}
	m = keys(t, m, "tab")
	if m.tab != TabAgents {
		t.Errorf("tab on Model server = %d, want Agents", m.tab)
	}
}
