package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
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

// TestNewPickModelUsesTableHeaderAndRunningState verifies wt smoke's picker
// shows the same column header as the agent flow and reads RUNNING from the live
// inventory (a running local model shows "run"), with no per-agent counts since
// the list is not scoped to one agent. Without it the two pickers would drift
// and smoke would show stale or missing running state.
func TestNewPickModelUsesTableHeaderAndRunningState(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true},
	}})
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}}}
	models := []config.Model{{ID: "omlx/a", ProviderID: "omlx", ModelName: "a"}}

	pm := newPickModel(cfg, models, themes.Default)

	if !strings.Contains(pm.list.Title, "RUNNING") {
		t.Errorf("title = %q, want the table header (with RUNNING)", pm.list.Title)
	}
	items := pm.list.Items()
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if line := items[0].(*modelItem).line; !strings.Contains(line, "run") {
		t.Errorf("line = %q, want it to show the running state (run)", line)
	}
}

// TestPickModelViewHeaderAlignsWithRows renders the standalone picker's real
// list view and checks the header's FAMILY and MODEL columns start at the same
// rune offset as the first row's cells. Both the title style and the list's
// TitleBar padding must be cleared (shared styleTableTitle helper), otherwise
// the header shifts relative to the rows in wt smoke's picker.
func TestPickModelViewHeaderAlignsWithRows(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b", ProviderID: "ollama", Family: "gemma4"}}
	pm := newPickModel(nil, models, themes.Default)

	var header, row string
	for _, ln := range strings.Split(ansiRE.ReplaceAllString(pm.View(), ""), "\n") {
		if strings.Contains(ln, "FAMILY") && header == "" {
			header = ln
		}
		if strings.Contains(ln, "ollama/gemma4:9b") && row == "" {
			row = ln
		}
	}
	if header == "" || row == "" {
		t.Fatalf("header/row not found in view:\n%s", pm.View())
	}
	off := func(s, sub string) int { return len([]rune(s[:strings.Index(s, sub)])) }
	if off(header, "MODEL") != off(row, "ollama/gemma4:9b") {
		t.Errorf("MODEL offset %d != row id offset %d\n%q\n%q", off(header, "MODEL"), off(row, "ollama/gemma4:9b"), header, row)
	}
	if off(header, "FAMILY") != off(row, "gemma4") {
		t.Errorf("FAMILY offset %d != row family offset %d\n%q\n%q", off(header, "FAMILY"), off(row, "gemma4"), header, row)
	}
}
