package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// TestPickModelEnterSelectsHighlightedModel asserts that Enter on the
// standalone model picker (used by callers like wt smoke that don't run the
// full worktree->agent->model TUI state machine) selects the currently
// highlighted row and signals completion via tea.Quit. wt smoke needs this
// exact selection semantics so the model a user sees highlighted is the one
// that gets smoke-tested.
func TestPickModelEnterSelectsHighlightedModel(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b", ProviderID: "ollama", Family: "gemma4"},
		{ID: "ollama/qwen3.8:27b", ModelName: "qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
	}
	m := newPickModel(nil, models, themes.Default)

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gm := got.(pickModel)

	if cmd == nil {
		t.Fatalf("cmd = nil, want tea.Quit")
	}
	if gm.canceled {
		t.Errorf("canceled = true, want false")
	}
	want := gm.list.Items()[0].(*modelItem).model.ID
	if gm.selected.ID != want {
		t.Errorf("selected = %q, want %q (the highlighted row)", gm.selected.ID, want)
	}
}

// TestPickModelEscCancels asserts that Esc cancels the standalone picker
// without a selection, so a caller can distinguish "user picked nothing"
// from "user picked the first model" and must not silently launch a default.
func TestPickModelEscCancels(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{{ID: "ollama/gemma4:9b", Family: "gemma4"}}
	m := newPickModel(nil, models, themes.Default)

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	gm := got.(pickModel)

	if cmd == nil {
		t.Fatalf("cmd = nil, want tea.Quit")
	}
	if !gm.canceled {
		t.Errorf("canceled = false, want true")
	}
}

// TestPickModelCtrlCCancels mirrors the whole-TUI convention (app_test.go's
// TestUpdateQuitKeys) that Ctrl+C always quits immediately, even though this
// picker is a separate, standalone Bubble Tea program from the main app.
func TestPickModelCtrlCCancels(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{{ID: "ollama/gemma4:9b", Family: "gemma4"}}
	m := newPickModel(nil, models, themes.Default)

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	gm := got.(pickModel)

	if cmd == nil {
		t.Fatalf("cmd = nil, want tea.Quit")
	}
	if !gm.canceled {
		t.Errorf("canceled = false, want true")
	}
}

// TestPickModelQCancelsWhenIdle asserts 'q' cancels the picker outside
// filter mode, matching the main TUI's universal quit-key convention.
func TestPickModelQCancelsWhenIdle(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{{ID: "ollama/gemma4:9b", Family: "gemma4"}}
	m := newPickModel(nil, models, themes.Default)

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gm := got.(pickModel)

	if cmd == nil {
		t.Fatalf("cmd = nil, want tea.Quit")
	}
	if !gm.canceled {
		t.Errorf("canceled = false, want true")
	}
}

// TestPickModelQTypesIntoFilterInsteadOfQuitting asserts that 'q' while the
// list's incremental filter is open types into the query instead of
// canceling the picker — the same filter-key mishandling class of bug fixed
// for the main TUI's model phase (TestQDoesNotQuitWhileFilteringModelList)
// must not reappear in this standalone picker.
func TestPickModelQTypesIntoFilterInsteadOfQuitting(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{
		{ID: "ollama/qwen3.8:27b", ModelName: "qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
		{ID: "ollama/other", ModelName: "other", ProviderID: "ollama"},
	}
	m := newPickModel(nil, models, themes.Default)
	m.list, _ = m.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.list.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.list.FilterState())
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gm := got.(pickModel)

	if gm.canceled {
		t.Errorf("canceled = true, want false (quit hijacked the filter keystroke)")
	}
	if !strings.Contains(gm.list.FilterInput.Value(), "q") {
		t.Errorf("filter input = %q, want to contain q", gm.list.FilterInput.Value())
	}
}

// TestNewPickModelFamilyTotalsCoverFullCatalog asserts that a family's
// 30-day usage total (embedded in each row's line) reflects every model in
// cfg's full catalog, not just the narrower eligible slice this picker
// renders — matching buildModelItems' contract and the agent flow's picker.
// Without this, wt smoke's picker would show a lower family total (and could
// sort differently) than the main wt picker for the exact same family,
// whenever a family has models that aren't currently eligible.
func TestNewPickModelFamilyTotalsCoverFullCatalog(t *testing.T) {
	store := stubUsageStore(t)
	stubRefcountStore(t)
	eligible := config.Model{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b", ProviderID: "ollama", Family: "gemma4"}
	ineligible := config.Model{ID: "ollama/gemma4:14b", ModelName: "gemma4:14b", ProviderID: "ollama", Family: "gemma4"}
	// Record usage against the model that is NOT in the eligible slice this
	// picker renders — only the full catalog (cfg.Models) knows about it.
	if err := store.Record(ineligible.ID); err != nil {
		t.Fatalf("Record: %v", err)
	}
	cfg := &config.Config{Models: []config.Model{eligible, ineligible}}

	m := newPickModel(cfg, []config.Model{eligible}, themes.Default)

	item := m.list.Items()[0].(*modelItem)
	if !strings.Contains(item.line, "  1  ") {
		t.Errorf("line = %q, want it to include the family's 30-day total (1) from the ineligible sibling model", item.line)
	}
}
