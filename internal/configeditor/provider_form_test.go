package configeditor

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestProviderForm_Add_Success verifies that a new provider is appended
// to cfg.Providers when the form is saved. This is the core create
// operation for the providers tab.
func TestProviderForm_Add_Success(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "existing"}},
	}
	enterProviderForm(&m, config.Provider{}, true)
	m.provID.SetValue("foo")
	m.provName.SetValue("Foo Provider")
	m.provAuth.SetValue("none")
	m.provBaseURL.SetValue("")
	m.provLoc = config.LocationLocal

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList after save, got %d", m2.phase)
	}
	if len(m2.cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(m2.cfg.Providers))
	}
	found := false
	for _, p := range m2.cfg.Providers {
		if p.ID == "foo" {
			found = true
			if p.Location != config.LocationLocal {
				t.Errorf("location = %q, want local", p.Location)
			}
			if p.Auth.Type != "none" {
				t.Errorf("auth.type = %q, want none", p.Auth.Type)
			}
		}
	}
	if !found {
		t.Error("new provider 'foo' not found in cfg.Providers")
	}
	if !m2.dirty {
		t.Error("expected dirty=true after form save")
	}
}

// TestProviderForm_EditImmutableID verifies that editing an existing
// provider does not change its ID. The ID is the primary key and must
// remain stable during edits.
func TestProviderForm_EditImmutableID(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "alpha", Name: "Alpha"}},
	}
	enterProviderForm(&m, config.Provider{ID: "alpha", Name: "Alpha"}, false)
	m.provID.SetValue("beta") // attempt to change ID

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	for _, p := range m2.cfg.Providers {
		if p.ID == "alpha" {
			if p.Name != "Alpha" {
				// Name wasn't changed because we only set provID
			}
			return
		}
	}
	// If we get here, the ID was changed — that's a bug.
	t.Error("provider ID was mutated during edit; ID must be immutable")
}

// TestProviderForm_EmptyID_BlocksSave verifies that saving a provider
// with an empty ID is rejected with an error and the cursor jumps to the
// ID field so the user can fix it immediately.
func TestProviderForm_EmptyID_BlocksSave(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{}
	enterProviderForm(&m, config.Provider{}, true)
	m.provID.SetValue("") // empty

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	if m2.phase != phaseForm {
		t.Fatalf("expected to stay in phaseForm, got %d", m2.phase)
	}
	if m2.formError == "" {
		t.Fatal("expected error for empty ID")
	}
	if m2.formCursor != 0 {
		t.Errorf("cursor = %d, want 0 (ID field)", m2.formCursor)
	}
	if len(m2.cfg.Providers) != 0 {
		t.Error("expected no providers added when save is blocked")
	}
	if m2.dirty {
		t.Error("expected dirty=false when save is blocked")
	}
}
