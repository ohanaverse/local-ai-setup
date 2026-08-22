package configeditor

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestModelForm_Add_Success verifies that a new model is appended with
// parsed tags when the form is saved.
func TestModelForm_Add_Success(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "agy"}},
		Models:    []config.Model{},
	}
	enterModelForm(&m, config.Model{}, true)
	m.modID.SetValue("agy/test")
	m.modFamily.SetValue("test")
	m.modProv.SetValue("agy")
	m.modName.SetValue("test-model")
	m.modTags.SetValue("code, design")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList after save, got %d", m2.phase)
	}
	if len(m2.cfg.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(m2.cfg.Models))
	}
	mod := m2.cfg.Models[0]
	if mod.ID != "agy/test" {
		t.Errorf("id = %q, want agy/test", mod.ID)
	}
	if len(mod.Tags) != 2 || mod.Tags[0] != "code" || mod.Tags[1] != "design" {
		t.Errorf("tags = %v, want [code design]", mod.Tags)
	}
}

// TestModelForm_ProviderPicker_OnlyExisting verifies that saving with a
// non-existent provider ID is rejected. This ensures models always point
// to valid providers.
func TestModelForm_ProviderPicker_OnlyExisting(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "ollama"}},
	}
	enterModelForm(&m, config.Model{}, true)
	m.modID.SetValue("foo/bar")
	m.modProv.SetValue("nonexistent")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	if m2.phase != phaseForm {
		t.Fatalf("expected to stay in form, got phase %d", m2.phase)
	}
	if m2.formError == "" {
		t.Fatal("expected error for nonexistent provider")
	}
	if len(m2.cfg.Models) != 0 {
		t.Error("expected no models added when save is blocked")
	}
}

// TestModelForm_DuplicateID_BlocksSave verifies that adding a model with
// an ID that already exists is rejected. Duplicate IDs would break
// model lookups and rotation state.
func TestModelForm_DuplicateID_BlocksSave(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "ollama"}},
		Models:    []config.Model{{ID: "ollama/gemma4:9b"}},
	}
	enterModelForm(&m, config.Model{}, true)
	m.modID.SetValue("ollama/gemma4:9b")
	m.modProv.SetValue("ollama")

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m2 := got.(model)
	if m2.phase != phaseForm {
		t.Fatalf("expected to stay in form, got phase %d", m2.phase)
	}
	if m2.formError == "" {
		t.Fatal("expected error for duplicate ID")
	}
}

// TestModelForm_LocationDerived verifies that the location field reflects
// the provider's location when the model itself has no explicit location.
// This communicates to the user that location is inherited.
func TestModelForm_LocationDerived(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}},
	}
	enterModelForm(&m, config.Model{ProviderID: "ollama"}, true)
	view := m.modelFormView()
	if !strings.Contains(view, "local") {
		t.Errorf("expected location 'local' in form view, got:\n%s", view)
	}
}
