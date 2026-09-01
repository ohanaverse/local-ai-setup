package configeditor

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

func testTheme() themes.Theme {
	t, _ := themes.Get("default")
	return t
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
// populates the agents list from the config. Without this, the list
// would be empty.
func TestLoadedMsg_BuildsLists(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.width, m.height = 80, 24

	cfg := &config.Config{
		Agents: []config.Agent{{Name: "claude"}},
	}
	got, _ := m.Update(loadedMsg{cfg: cfg})
	m2 := got.(model)
	if !m2.ready {
		t.Fatal("expected ready=true after loadedMsg")
	}
	if len(m2.list.Items()) == 0 {
		t.Errorf("agents list: expected at least 1 item, got %d", len(m2.list.Items()))
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

// TestNewKey_OpensAddForm verifies that pressing 'n' opens the add form
// for an agent. This wires the documented add functionality to a keybinding.
func TestNewKey_OpensAddForm(t *testing.T) {
	m := newModel(testTheme(), &config.Config{DefaultTag: "code"}, nil)
	m.ready = true
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m2 := got.(model)
	if m2.phase != phaseForm {
		t.Fatalf("expected phaseForm after 'n', got %d", m2.phase)
	}
	if !m2.formIsNew {
		t.Fatal("expected formIsNew=true for add form")
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
	// The list should still be built so the UI is usable.
	if len(m2.list.Items()) == 0 {
		t.Fatal("expected a built agents list")
	}
}

// TestEnterKey_OpensEditFormForSelectedItem verifies that pressing Enter on a
// sorted list opens the form for the selected item, not the item matching the
// slice index in cfg.
func TestEnterKey_OpensEditFormForSelectedItem(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	// Config has zeta first, alpha second.
	m.cfg = &config.Config{
		Agents: []config.Agent{
			{Name: "zeta", SupportedProviders: []string{"ollama"}},
			{Name: "alpha", SupportedProviders: []string{"ollama"}},
		},
	}
	m.ready = true
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)

	// List is sorted by name (commands first, then alphabetical), so among
	// the configured agents "alpha" sorts before "zeta". Select "alpha" and
	// press Enter to open the edit form for it, not "zeta" (cfg.Agents[0]).
	selectAgentItem(&m, "alpha")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := got.(model)
	if m2.phase != phaseForm {
		t.Fatalf("expected phaseForm after Enter, got %d", m2.phase)
	}
	if m2.agEdit.Name != "alpha" {
		t.Errorf("agEdit.Name = %q, want alpha", m2.agEdit.Name)
	}
}

// TestEnterKey_UnconfiguredAgent_OpensAddForm verifies that pressing Enter
// on a registered-but-unconfigured agent driver opens the add form pre-populated
// with that agent's name.
func TestEnterKey_UnconfiguredAgent_OpensAddForm(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{Agents: []config.Agent{}}
	m.ready = true
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)

	// Find an unconfigured agent in the list items (skip commands).
	items := m.list.Items()
	for i, it := range items {
		ai := it.(agentItem)
		if !ai.command && !ai.configured {
			m.list.Select(i)
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

// selectAgentItem selects the list item whose agent name matches name.
func selectAgentItem(m *model, name string) {
	for i, it := range m.list.Items() {
		if ai, ok := it.(agentItem); ok && ai.agent.Name == name {
			m.list.Select(i)
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
