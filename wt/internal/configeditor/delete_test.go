package configeditor

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)

	enterDelete(&m, "claude")
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

// TestDelete_Cancel_PreservesRow verifies that pressing 'n' on the delete
// prompt returns to the list without removing anything.
func TestDelete_Cancel_PreservesRow(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Agents: []config.Agent{{Name: "claude"}},
	}
	m.ready = true
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)

	enterDelete(&m, "claude")
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
		Agents: []config.Agent{
			{Name: "zeta"},
			{Name: "alpha"},
		},
	}
	m.ready = true
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)

	// List is sorted by name (commands first, then alphabetical), so among
	// the configured agents "alpha" sorts before "zeta". Select "alpha" and
	// press 'd' to target it, NOT "zeta" (which is cfg.Agents[0]).
	selectAgentItem(&m, "alpha")
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
	if len(m3.cfg.Agents) != 1 || m3.cfg.Agents[0].Name != "zeta" {
		t.Fatalf("expected only zeta remaining, got %v", m3.cfg.Agents)
	}
}
