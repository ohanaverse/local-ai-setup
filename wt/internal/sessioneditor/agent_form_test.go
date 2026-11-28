package sessioneditor

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestAgentForm_Add_Success verifies that a new agent is appended to
// cfg.Agents when the form is saved with valid data.
func TestAgentForm_Add_Success(t *testing.T) {
	m := newModel(testTheme(), &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{},
	}, nil)
	enterAgentForm(m, config.Agent{}, true)
	m.agName.SetValue("foo")
	m.agProvidersInput.SetValue("agy")
	m.agDefaultProviderInput.SetValue("agy")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(*model)
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

// TestAgentForm_DefaultProvider_Constrained verifies that a default provider
// outside the supported providers no longer blocks the save. Why: no reader
// uses either field now (the eligibility rule derives a provider's agents), so
// UpsertAgent checks only the name; the form's provider fields are removed in
// a later change.
func TestAgentForm_DefaultProvider_Constrained(t *testing.T) {
	m := newModel(testTheme(), &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{},
	}, nil)
	enterAgentForm(m, config.Agent{}, true)
	m.agName.SetValue("foo")
	m.agProvidersInput.SetValue("agy, claude")
	m.agDefaultProviderInput.SetValue("codex") // not in supported

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(*model)
	if m2.phase != phaseList || m2.formError != "" {
		t.Fatalf("expected the save to go through, got phase %d, error %q", m2.phase, m2.formError)
	}
}

// TestAgentForm_NoProviders_BlocksSave verifies that an agent with no
// supported providers now saves (the name is the only required field). Why:
// the eligibility rule reads no list, so an empty one is a valid entry; the
// test keeps its old name until the form drops the field.
func TestAgentForm_NoProviders_BlocksSave(t *testing.T) {
	m := newModel(testTheme(), &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{},
	}, nil)
	enterAgentForm(m, config.Agent{}, true)
	m.agName.SetValue("foo")
	m.agProvidersInput.SetValue("")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(*model)
	if m2.phase != phaseList || m2.formError != "" {
		t.Fatalf("expected the save to go through, got phase %d, error %q", m2.phase, m2.formError)
	}
	if len(m2.cfg.Agents) != 1 || m2.cfg.Agents[0].Name != "foo" || len(m2.cfg.Agents[0].SupportedProviders) != 0 {
		t.Errorf("agents = %+v, want foo with no list", m2.cfg.Agents)
	}
}

// TestAgentForm_RenameExisting verifies that saving an existing agent
// with a new name updates it in place. Previously the name was only set on
// new agents, so renames were silently ignored.
func TestAgentForm_RenameExisting(t *testing.T) {
	m := newModel(testTheme(), &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{{Name: "old", SupportedProviders: []string{"agy"}}},
	}, nil)
	enterAgentForm(m, m.cfg.Agents[0], false)
	m.agName.SetValue("new")
	m.agProvidersInput.SetValue("agy")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(*model)
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
	enterAgentForm(m, config.Agent{Name: "claude"}, false)
	view := m.agentFormView()
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
