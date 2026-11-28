package sessioneditor

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestAgentForm_Add_NeedsOnlyAName verifies that a new agent saves with just a
// name. Why: the eligibility rule is derived and an agent entry holds nothing
// else, so the form has no other input and a bare name must save.
func TestAgentForm_Add_NeedsOnlyAName(t *testing.T) {
	m := newModel(testTheme(), &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{},
	}, nil)
	enterAgentForm(m, config.Agent{}, true)
	m.agName.SetValue("foo")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(*model)
	if m2.phase != phaseList || m2.formError != "" {
		t.Fatalf("expected the save to go through, got phase %d, error %q", m2.phase, m2.formError)
	}
	if len(m2.cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(m2.cfg.Agents))
	}
	ag := m2.cfg.Agents[0]
	if ag.Name != "foo" {
		t.Errorf("name = %q, want foo", ag.Name)
	}
}

// TestAgentForm_EmptyNameErrors verifies that saving with an empty name stays
// in the form, shows the error and puts the cursor on the Name field. Why: the
// name is the only field that can be wrong now, so the error cursor always
// goes to 0.
func TestAgentForm_EmptyNameErrors(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	enterAgentForm(m, config.Agent{}, true)
	m.formCursor = 1
	m.focusAgentField()

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(*model)
	if m2.phase != phaseForm || m2.formError == "" {
		t.Fatalf("expected the form to stay open with an error, got phase %d, error %q", m2.phase, m2.formError)
	}
	if m2.formCursor != 0 {
		t.Errorf("formCursor = %d, want 0 (the name)", m2.formCursor)
	}
	if len(m2.cfg.Agents) != 0 {
		t.Errorf("agents = %+v, want none", m2.cfg.Agents)
	}
}

// TestAgentForm_FieldsAreNameAndInstalled verifies that the form shows only
// Name and the read-only Installed line, and that Tab and Up cycle through
// exactly those two. Why: the Supported Providers and Default Provider inputs
// were removed with the derived eligibility rule (#763); a leftover label or a
// wrap at the old size of 4 would leave dead cursor positions.
func TestAgentForm_FieldsAreNameAndInstalled(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	enterAgentForm(m, config.Agent{Name: "claude"}, false)
	view := m.agentFormView()
	for _, gone := range []string{"Supported Providers", "Default Provider"} {
		if strings.Contains(view, gone) {
			t.Errorf("form view still shows %q:\n%s", gone, view)
		}
	}
	for _, want := range []string{"Name", "Installed"} {
		if !strings.Contains(view, want) {
			t.Errorf("form view lacks %q:\n%s", want, view)
		}
	}

	press := func(k tea.KeyType) {
		got, _ := m.Update(tea.KeyMsg{Type: k})
		m = got.(*model)
	}
	for i, want := range []int{1, 0, 1, 0} {
		press(tea.KeyTab)
		if m.formCursor != want {
			t.Fatalf("after Tab %d: formCursor = %d, want %d", i+1, m.formCursor, want)
		}
	}
	press(tea.KeyUp)
	if m.formCursor != 1 {
		t.Errorf("Up from 0: formCursor = %d, want 1", m.formCursor)
	}
}

// TestAgentForm_RenameExisting verifies that saving an existing agent
// with a new name updates it in place. Previously the name was only set on
// new agents, so renames were silently ignored.
func TestAgentForm_RenameExisting(t *testing.T) {
	m := newModel(testTheme(), &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{{Name: "old"}},
	}, nil)
	enterAgentForm(m, m.cfg.Agents[0], false)
	m.agName.SetValue("new")

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

// TestAgentForm_RenameKeepsSynthesized verifies that renaming an agent whose
// Synthesized flag is true through the form leaves the flag true. Why:
// saveAgentForm copies the stored entry and changes only the name, and the
// flag is what keeps a synthesized agent out of the next save (the editor
// writes only listed agents); a rename that cleared it would write an agent
// the user never listed.
func TestAgentForm_RenameKeepsSynthesized(t *testing.T) {
	m := newModel(testTheme(), &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Agents:    []config.Agent{{Name: "old", Synthesized: true}},
	}, nil)
	enterAgentForm(m, m.cfg.Agents[0], false)
	m.agName.SetValue("new")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(*model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList after rename, got %d (error %q)", m2.phase, m2.formError)
	}
	if len(m2.cfg.Agents) != 1 || m2.cfg.Agents[0].Name != "new" {
		t.Fatalf("agents = %+v, want one agent named new", m2.cfg.Agents)
	}
	if !m2.cfg.Agents[0].Synthesized {
		t.Errorf("Synthesized = false after the rename, want true: %+v", m2.cfg.Agents[0])
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
