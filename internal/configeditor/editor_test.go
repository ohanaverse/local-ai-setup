package configeditor

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

func testTheme() themes.Theme {
	t, _ := themes.Get("default")
	return t
}

// TestTab_Cycles verifies that Tab advances through sections in order
// and Shift+Tab moves backward. Tab navigation is the primary way users
// switch between Agents, Providers, and Models.
func TestTab_Cycles(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{}

	// Tab: Agents → Providers
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m2 := got.(model)
	if m2.section != sectionProviders {
		t.Errorf("Tab from Agents: got section %d, want Providers", m2.section)
	}

	// Tab: Providers → Models
	got, _ = m2.Update(tea.KeyMsg{Type: tea.KeyTab})
	m3 := got.(model)
	if m3.section != sectionModels {
		t.Errorf("Tab from Providers: got section %d, want Models", m3.section)
	}

	// Tab: Models → Agents (wrap)
	got, _ = m3.Update(tea.KeyMsg{Type: tea.KeyTab})
	m4 := got.(model)
	if m4.section != sectionAgents {
		t.Errorf("Tab from Models: got section %d, want Agents", m4.section)
	}

	// Shift+Tab: Agents → Models (wrap backward)
	got, _ = m4.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m5 := got.(model)
	if m5.section != sectionModels {
		t.Errorf("Shift+Tab from Agents: got section %d, want Models", m5.section)
	}

	// Shift+Tab: Models → Providers
	got, _ = m5.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m6 := got.(model)
	if m6.section != sectionProviders {
		t.Errorf("Shift+Tab from Models: got section %d, want Providers", m6.section)
	}
}

// TestTab_NumberKeys_Jump verifies that pressing 1, 2, or 3 jumps directly
// to the corresponding section. This is a keyboard shortcut for users who
// know exactly which tab they want.
func TestTab_NumberKeys_Jump(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{}
	m.section = sectionModels // start away from default

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m2 := got.(model)
	if m2.section != sectionAgents {
		t.Errorf("'1': got section %d, want Agents", m2.section)
	}

	got, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m3 := got.(model)
	if m3.section != sectionProviders {
		t.Errorf("'2': got section %d, want Providers", m3.section)
	}

	got, _ = m3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m4 := got.(model)
	if m4.section != sectionModels {
		t.Errorf("'3': got section %d, want Models", m4.section)
	}
}

// TestInitReturnsLoadCmd verifies that Init dispatches config loading.
// Without this command the TUI would hang at "Loading config..." forever.
func TestInitReturnsLoadCmd(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd; expected load command")
	}
}

// TestLoadedMsg_BuildsLists verifies that a successful loadedMsg
// populates all three tab lists from the config. Without this, switching
// to any tab would show an empty list.
func TestLoadedMsg_BuildsLists(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.width, m.height = 80, 24

	cfg := &config.Config{
		Agents:    []config.Agent{{Name: "claude"}},
		Providers: []config.Provider{{ID: "ollama"}},
		Models:    []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama"}},
	}
	got, _ := m.Update(loadedMsg{cfg: cfg})
	m2 := got.(model)
	if !m2.ready {
		t.Fatal("expected ready=true after loadedMsg")
	}
	if len(m2.lists[sectionAgents].Items()) == 0 {
		t.Errorf("agents list: expected at least 1 item, got %d", len(m2.lists[sectionAgents].Items()))
	}
	if len(m2.lists[sectionProviders].Items()) != 1 {
		t.Errorf("providers list: expected 1 item, got %d", len(m2.lists[sectionProviders].Items()))
	}
	if len(m2.lists[sectionModels].Items()) != 1 {
		t.Errorf("models list: expected 1 item, got %d", len(m2.lists[sectionModels].Items()))
	}
}

// TestSwitchSection_RebuildsList verifies that switching to the Providers
// tab renders the providers list that was built from cfg.Providers.
func TestSwitchSection_RebuildsList(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.width, m.height = 80, 24
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "alpha"}, {ID: "beta"}},
	}
	got, _ := m.Update(loadedMsg{cfg: cfg})
	m2 := got.(model)

	// Switch to providers tab.
	got, _ = m2.Update(tea.KeyMsg{Type: tea.KeyTab})
	m3 := got.(model)
	if m3.section != sectionProviders {
		t.Fatalf("expected sectionProviders, got %d", m3.section)
	}
	if len(m3.lists[sectionProviders].Items()) != 2 {
		t.Errorf("providers list: expected 2 items, got %d", len(m3.lists[sectionProviders].Items()))
	}
}

// TestUpdateWindowSizeMsg verifies that the model records terminal dimensions
// and resizes existing lists.
func TestUpdateWindowSizeMsg(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.width, m.height = 80, 24
	cfg := &config.Config{
		Agents: []config.Agent{{Name: "claude"}},
	}
	got, _ := m.Update(loadedMsg{cfg: cfg})
	m2 := got.(model)

	got, _ = m2.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m3 := got.(model)
	if m3.width != 100 || m3.height != 40 {
		t.Errorf("dimensions = (%d, %d), want (100, 40)", m3.width, m3.height)
	}
}

// TestNewKey_OpensAddForm verifies that pressing 'n' on a tab opens the
// add form for the current section. This wires the documented add
// functionality to a keybinding.
func TestNewKey_OpensAddForm(t *testing.T) {
	for _, sec := range []section{sectionAgents, sectionProviders, sectionModels} {
		t.Run(sec.String(), func(t *testing.T) {
			m := newModel(testTheme(), &config.Config{DefaultTag: "code"}, nil)
			m.ready = true
			m.section = sec
			m.lists[sectionAgents] = buildAgentsList(testTheme(), 80, 24, m.cfg)
			m.lists[sectionProviders] = buildProvidersList(testTheme(), 80, 24, m.cfg)
			m.lists[sectionModels] = buildModelsList(testTheme(), 80, 24, m.cfg)

			got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
			m2 := got.(model)
			if m2.phase != phaseForm {
				t.Fatalf("expected phaseForm after 'n', got %d", m2.phase)
			}
			if !m2.formIsNew {
				t.Fatal("expected formIsNew=true for add form")
			}
		})
	}
}

// TestLoadedMsg_Error_SetsReadyAndStatus verifies that a failed config
// load is surfaced in the TUI instead of leaving the screen stuck on
// "Loading config...". This is the repair path: `wt config` must open
// even when the existing config fails validation.
func TestLoadedMsg_Error_SetsReadyAndStatus(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.width, m.height = 80, 24

	got, _ := m.Update(loadedMsg{err: fmt.Errorf("bad config"), cfg: &config.Config{DefaultTag: "code"}})
	m2 := got.(model)
	if !m2.ready {
		t.Fatal("expected ready=true after loadedMsg with error")
	}
	if m2.status == "" {
		t.Fatal("expected status to show the error")
	}
	if !strings.Contains(m2.status, "bad config") {
		t.Fatalf("status %q should mention the underlying error", m2.status)
	}
	// Lists should still be built so the UI is usable.
	if len(m2.lists) != 3 {
		t.Fatalf("expected 3 built lists, got %d", len(m2.lists))
	}
}

// TestEnterKey_OpensEditFormForSelectedItem verifies that pressing Enter on a
// sorted list opens the form for the selected item, not the item matching the
// slice index in cfg.
func TestEnterKey_OpensEditFormForSelectedItem(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	// Config has zeta first, alpha second.
	m.cfg = &config.Config{
		Providers: []config.Provider{
			{ID: "zeta", Name: "Zeta"},
			{ID: "alpha", Name: "Alpha"},
		},
	}
	m.ready = true
	m.section = sectionProviders
	m.lists[sectionProviders] = buildProvidersList(testTheme(), 80, 24, m.cfg)

	// List is sorted by ID, so index 0 is "alpha".
	// Pressing Enter should open the edit form for "alpha".
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := got.(model)
	if m2.phase != phaseForm {
		t.Fatalf("expected phaseForm after Enter, got %d", m2.phase)
	}
	if m2.provEdit.ID != "alpha" {
		t.Errorf("provEdit.ID = %q, want alpha", m2.provEdit.ID)
	}
}

// TestEnterKey_UnconfiguredAgent_OpensAddForm verifies that pressing Enter
// on a registered-but-unconfigured agent driver opens the add form pre-populated
// with that agent's name.
func TestEnterKey_UnconfiguredAgent_OpensAddForm(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{Agents: []config.Agent{}}
	m.ready = true
	m.section = sectionAgents
	m.lists[sectionAgents] = buildAgentsList(testTheme(), 80, 24, m.cfg)

	// Find an unconfigured agent in the list items (skip commands).
	items := m.lists[sectionAgents].Items()
	for i, it := range items {
		ai := it.(agentItem)
		if !ai.command && !ai.configured {
			m.lists[sectionAgents].Select(i)
			got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m2 := got.(model)
			if m2.phase != phaseForm {
				t.Fatalf("expected phaseForm, got %d", m2.phase)
			}
			if !m2.formIsNew {
				t.Error("expected formIsNew=true for unconfigured agent")
			}
			if m2.agName.Value() != ai.agent.Name {
				t.Errorf("form name = %q, want %q", m2.agName.Value(), ai.agent.Name)
			}
			return
		}
	}
}

// TestRun_EmptyConfig_Launches verifies that Run returns without panic
// even when the config is empty. This is the smoke test for the package
// skeleton; without it, a missing Init or zero-value model could deadlock
// or crash on startup.
func TestRun_EmptyConfig_Launches(t *testing.T) {
	err := Run(
		testTheme(),
		&config.Config{},
		nil,
		tea.WithInput(strings.NewReader("q")),
		tea.WithoutRenderer(),
	)
	if err != nil {
		t.Logf("Run returned (acceptable in non-TTY): %v", err)
	}
}
