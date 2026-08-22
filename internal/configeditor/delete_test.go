package configeditor

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestDelete_Agent_SucceedsAfterConfirm verifies that pressing 'd' then
// 'y' removes the selected agent row and marks the config dirty.
func TestDelete_Agent_SucceedsAfterConfirm(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Agents: []config.Agent{
			{Name: "claude"},
			{Name: "codex"},
		},
	}
	m.ready = true
	m.lists[sectionAgents] = buildAgentsList(testTheme(), 80, 24, m.cfg)

	enterDelete(&m, sectionAgents, "claude")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m2 := got.(model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList after confirm, got %d", m2.phase)
	}
	if len(m2.cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent after delete, got %d", len(m2.cfg.Agents))
	}
	if m2.cfg.Agents[0].Name != "codex" {
		t.Errorf("remaining agent = %q, want codex", m2.cfg.Agents[0].Name)
	}
	if !m2.dirty {
		t.Error("expected dirty=true after delete")
	}
}

// TestDelete_Provider_BlockedByModelRef verifies that deleting a provider
// referenced by a model is blocked with a clear error and the row is
// preserved.
func TestDelete_Provider_BlockedByModelRef(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Models:    []config.Model{{ID: "agy/native", ProviderID: "agy"}},
	}
	m.ready = true
	m.lists[sectionProviders] = buildProvidersList(testTheme(), 80, 24, m.cfg)

	enterDelete(&m, sectionProviders, "agy")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m2 := got.(model)
	if m2.deleteError == "" {
		t.Fatal("expected FK error for provider referenced by model")
	}
	if len(m2.cfg.Providers) != 1 {
		t.Error("expected provider row preserved after blocked delete")
	}
}

// TestDelete_Provider_BlockedByAgentRef verifies that deleting a provider
// referenced by an agent is blocked with a clear error.
func TestDelete_Provider_BlockedByAgentRef(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "claude"}},
		Agents:    []config.Agent{{Name: "claude", SupportedProviders: []string{"claude"}}},
	}
	m.ready = true
	m.lists[sectionProviders] = buildProvidersList(testTheme(), 80, 24, m.cfg)

	enterDelete(&m, sectionProviders, "claude")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m2 := got.(model)
	if m2.deleteError == "" {
		t.Fatal("expected FK error for provider referenced by agent")
	}
	if len(m2.cfg.Providers) != 1 {
		t.Error("expected provider row preserved after blocked delete")
	}
}

// TestDelete_Model_Succeeds verifies that deleting a model is never blocked
// by FK checks and the row is removed immediately.
func TestDelete_Model_Succeeds(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Models: []config.Model{
			{ID: "ollama/a"},
			{ID: "ollama/b"},
		},
	}
	m.ready = true
	m.lists[sectionModels] = buildModelsList(testTheme(), 80, 24, m.cfg)

	enterDelete(&m, sectionModels, "ollama/a")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m2 := got.(model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList after confirm, got %d", m2.phase)
	}
	if len(m2.cfg.Models) != 1 {
		t.Fatalf("expected 1 model after delete, got %d", len(m2.cfg.Models))
	}
}

// TestDelete_Cancel_PreservesRow verifies that pressing 'n' on the delete
// prompt returns to the list without removing anything.
func TestDelete_Cancel_PreservesRow(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Agents: []config.Agent{{Name: "claude"}},
	}
	m.ready = true
	m.lists[sectionAgents] = buildAgentsList(testTheme(), 80, 24, m.cfg)

	enterDelete(&m, sectionAgents, "claude")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m2 := got.(model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList after cancel, got %d", m2.phase)
	}
	if len(m2.cfg.Agents) != 1 {
		t.Fatalf("expected row preserved, got %d agents", len(m2.cfg.Agents))
	}
}

// TestDelete_Key_TargetsSelectedItemInSortedList verifies that pressing 'd'
// on a sorted list targets the highlighted item, not the item matching the
// slice index in cfg.
func TestDelete_Key_TargetsSelectedItemInSortedList(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	// Config has zeta first, alpha second.
	m.cfg = &config.Config{
		Providers: []config.Provider{
			{ID: "zeta"},
			{ID: "alpha"},
		},
	}
	m.ready = true
	m.section = sectionProviders
	m.lists[sectionProviders] = buildProvidersList(testTheme(), 80, 24, m.cfg)

	// List is sorted by ID, so index 0 is "alpha".
	// Pressing 'd' should target "alpha", NOT "zeta" (which is cfg.Providers[0]).
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m2 := got.(model)
	if m2.phase != phaseDelete {
		t.Fatalf("expected phaseDelete, got %d", m2.phase)
	}
	if m2.deleteTarget.id != "alpha" {
		t.Errorf("delete target id = %q, want alpha", m2.deleteTarget.id)
	}

	// Confirm delete.
	got2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m3 := got2.(model)
	if len(m3.cfg.Providers) != 1 || m3.cfg.Providers[0].ID != "zeta" {
		t.Fatalf("expected only zeta remaining, got %v", m3.cfg.Providers)
	}
}
