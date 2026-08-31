package configeditor

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestAgentForm_Add_Success verifies that a new agent is appended to
// cfg.Agents when the form is saved with valid data.
func TestAgentForm_Add_Success(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{},
	}
	enterAgentForm(&m, config.Agent{}, true)
	m.agName.SetValue("foo")
	m.agProvidersInput.SetValue("agy")
	m.agDefaultProviderInput.SetValue("agy")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList after save, got %d", m2.phase)
	}
	if len(m2.cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(m2.cfg.Agents))
	}
	ag := m2.cfg.Agents[0]
	if ag.Name != "foo" {
		t.Errorf("name = %q, want foo", ag.Name)
	}
	if len(ag.SupportedProviders) != 1 || ag.SupportedProviders[0] != "agy" {
		t.Errorf("supported_providers = %v, want [agy]", ag.SupportedProviders)
	}
	if ag.DefaultProvider != "agy" {
		t.Errorf("default_provider = %q, want agy", ag.DefaultProvider)
	}
}

// TestAgentForm_DefaultProvider_Constrained verifies that the default
// provider must be one of the supported providers. This prevents
// inconsistent agent configurations.
func TestAgentForm_DefaultProvider_Constrained(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{},
	}
	enterAgentForm(&m, config.Agent{}, true)
	m.agName.SetValue("foo")
	m.agProvidersInput.SetValue("agy, claude")
	m.agDefaultProviderInput.SetValue("codex") // not in supported

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	if m2.phase != phaseForm {
		t.Fatalf("expected to stay in form, got phase %d", m2.phase)
	}
	if m2.formError == "" {
		t.Fatal("expected error for invalid default provider")
	}
}

// TestAgentForm_NoProviders_BlocksSave verifies that an agent with no
// supported providers cannot be saved. Every agent needs at least one
// provider to be launchable.
func TestAgentForm_NoProviders_BlocksSave(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{},
	}
	enterAgentForm(&m, config.Agent{}, true)
	m.agName.SetValue("foo")
	m.agProvidersInput.SetValue("")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	if m2.phase != phaseForm {
		t.Fatalf("expected to stay in form, got phase %d", m2.phase)
	}
	if m2.formError == "" {
		t.Fatal("expected error for empty supported providers")
	}
}

// TestAgentForm_RenameExisting verifies that saving an existing agent
// with a new name updates it in place. Previously the name was only set on
// new agents, so renames were silently ignored.
func TestAgentForm_RenameExisting(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{{Name: "old", SupportedProviders: []string{"agy"}}},
	}
	enterAgentForm(&m, m.cfg.Agents[0], false)
	m.agName.SetValue("new")
	m.agProvidersInput.SetValue("agy")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList after rename, got %d", m2.phase)
	}
	if len(m2.cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(m2.cfg.Agents))
	}
	if m2.cfg.Agents[0].Name != "new" {
		t.Errorf("name = %q, want new", m2.cfg.Agents[0].Name)
	}
}

// TestAgentForm_InstalledReadOnly verifies that the installed field
// has no editable textinput cursor marker, confirming it is display-only.
func TestAgentForm_InstalledReadOnly(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{}
	enterAgentForm(&m, config.Agent{Name: "claude"}, false)
	view := m.agentFormView()
	// The installed field should show the checkmark or x, but no cursor.
	if strings.Contains(view, "[") && strings.Contains(view, "]") {
		// Simple heuristic: if there's a bracket pair near the installed
		// line, it might be a list marker, not a textinput cursor.
		// A textinput cursor is typically a blinking block rendered by
		// the textinput itself. Since installed is just a string value,
		// there should be no textinput.View() output for it.
	}
	// More direct: the "Installed" line should not contain a textinput
	// placeholder or prompt character.
	lines := strings.Split(view, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Installed") {
			if strings.Contains(line, "_") || strings.Contains(line, "|") {
				t.Errorf("installed field appears editable: %q", line)
			}
		}
	}
}
