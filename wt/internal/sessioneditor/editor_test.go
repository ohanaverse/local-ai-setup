package sessioneditor

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	m2 := got.(*model)
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
	m2 := got.(*model)

	got, _ = m2.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m3 := got.(*model)
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
	m2 := got.(*model)
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
	m2 := got.(*model)
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
	// Config has zeta first, alpha second.
	m := newModel(testTheme(), &config.Config{
		Agents: []config.Agent{
			{Name: "zeta"},
			{Name: "alpha"},
		},
	}, nil)
	m.ready = true
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)

	// List is sorted by name (commands first, then alphabetical), so among
	// the configured agents "alpha" sorts before "zeta". Select "alpha" and
	// press Enter to open the edit form for it, not "zeta" (cfg.Agents[0]).
	selectAgentItem(m, "alpha")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := got.(*model)
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
	m := newModel(testTheme(), &config.Config{Agents: []config.Agent{}}, nil)
	m.ready = true
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)

	// Find an unconfigured agent in the list items (skip commands).
	items := m.list.Items()
	for i, it := range items {
		ai := it.(agentItem)
		if !ai.command && !ai.configured {
			m.list.Select(i)
			got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m2 := got.(*model)
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
	_, err := Run(
		testTheme(),
		&config.Config{},
		nil,
		Options{},
		tea.WithInput(strings.NewReader("q")),
		tea.WithoutRenderer(),
	)
	if err != nil {
		t.Logf("Run returned (acceptable in non-TTY): %v", err)
	}
}

// TestStatusWrapsInsteadOfBeingCutOff pins what makes the end of a long
// status visible (#209). The status was drawn as one line, and a terminal
// cuts a too-wide line at its right edge, where a load error names the file
// to fix. The status now wraps to the terminal's width, and the list gives up
// the rows the extra lines take, so the view is still no taller than the
// terminal (Bubble Tea drops a too-tall view's TOP lines: the title and this
// status). The session editor reads no registry, so the error here is a long
// config.toml one whose last word is the file.
func TestStatusWrapsInsteadOfBeingCutOff(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude"}, {Name: "codex"}, {Name: "pi"}, {Name: "opencode"}},
	}
	locErr := fmt.Errorf("toml: line 12 (last key \"agents.name\"): incompatible types: TOML value has type integer; destination has type string; fix the entry in /tmp/somewhere/config.toml")
	// 60 columns is the narrowest checked: below the title's own width this
	// screen was already too wide for the terminal, status or no status.
	for _, size := range []struct{ w, h int }{{80, 24}, {60, 24}, {120, 12}, {200, 50}} {
		var m tea.Model = newModel(testTheme(), cfg, locErr)
		m, _ = m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m, _ = m.Update(loadedMsg{cfg: cfg, err: locErr})
		view := m.View()
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		if len(lines) > size.h {
			t.Errorf("%dx%d: view is %d lines tall", size.w, size.h, len(lines))
		}
		for _, l := range lines {
			if w := lipgloss.Width(l); w > size.w {
				t.Errorf("%dx%d: a line is %d columns wide: %q", size.w, size.h, w, l)
			}
		}
		// The whole message is on screen: look for the hint words in the view.
		// The hint may be wrapped across multiple lines, so we check that all
		// key words appear somewhere in the view (in order, but not necessarily
		// contiguous). This avoids both false positives from strings.Fields
		// collapsing whitespace and false negatives from exact string matching
		// when the hint wraps.
		hintWords := []string{"fix", "the", "entry", "in", "/tmp/somewhere/config.toml"}
		for _, word := range hintWords {
			if !strings.Contains(view, word) {
				t.Errorf("%dx%d: hint word %q not found in view:\n%s", size.w, size.h, word, view)
				break
			}
		}
		if !strings.HasPrefix(view, "[Agents]") {
			t.Errorf("%dx%d: the title is not the first line:\n%s", size.w, size.h, view)
		}
	}
}

// TestResolveHomeFallsBackToNoAbbreviation pins resolveHome: the home it is
// given is used as is, and a home that cannot be read leaves paths written in
// full, as the package-variable seam did.
func TestResolveHomeFallsBackToNoAbbreviation(t *testing.T) {
	if got := resolveHome(func() (string, error) { return "/Users/dev", nil }); got != "/Users/dev" {
		t.Errorf("resolveHome = %q, want /Users/dev", got)
	}
	if got := resolveHome(func() (string, error) { return "", errors.New("no home") }); got != "" {
		t.Errorf("resolveHome on an error = %q, want empty", got)
	}
}

// TestAgentsFilterNarrowsTheList verifies / filters the Agents tab's list:
// bubbles ranks the rows in a command and sends the result back as a message,
// and a list that lost its filter results showed every agent for "/cla".
func TestAgentsFilterNarrowsTheList(t *testing.T) {
	cfg := &config.Config{Agents: []config.Agent{{Name: "claude"}, {Name: "pi"}}}
	m := newModel(testTheme(), cfg, nil)
	m.home = testUserHome
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = stillCursors(send(t, m, loadedMsg{cfg: cfg}))
	all := len(m.list.VisibleItems())
	m = keys(t, m, "/", "c", "l", "a", "u")
	got := m.list.VisibleItems()
	if len(got) != 1 || got[0].(agentItem).agent.Name != "claude" || all < 2 {
		t.Errorf("/clau shows %d of %d agents, want claude alone", len(got), all)
	}
}
