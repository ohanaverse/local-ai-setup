package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// testConfig returns a Config with one agent and a two-model code tag group.
// Several lesson-14 tests need a real catalog so firstAgent/firstModel and
// the rotation have something to operate on.
func testConfig() *config.Config {
	return &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "ollama"},
		},
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/gemma4:14b", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/gemma4:design", ProviderID: "ollama", Tags: []string{"design"}},
		},
		Agents: []config.Agent{
			{Name: "claude"},
		},
	}
}

// TestInitLoadsEntries asserts Init starts background worktree enumeration.
// Without this command, the TUI would sit forever at the loading screen.
func TestInitLoadsEntries(t *testing.T) {
	m := model{loading: true, status: "loading worktrees..."}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd; expected loadEntriesCmd")
	}
}

// TestUpdateWindowSizeMsg asserts the model records the terminal's reported
// dimensions on a WindowSizeMsg. Without this, the list would lay out at zero
// size and render nothing usable.
func TestUpdateWindowSizeMsg(t *testing.T) {
	m := model{status: "ready"}
	got, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.width != 80 || gotModel.height != 24 {
		t.Errorf("dimensions = (%d, %d), want (80, 24)", gotModel.width, gotModel.height)
	}
}

// TestUpdateQuitKeys asserts that q, esc, and ctrl+c all return tea.Quit.
// These three are the universal TUI exit affordances; if any one is missing
// the user can get stuck in the alternate screen with no way back to the
// shell.
func TestUpdateQuitKeys(t *testing.T) {
	cases := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC},
	}
	for _, c := range cases {
		m := model{status: "ready"}
		_, cmd := m.Update(c)
		if cmd == nil {
			t.Errorf("key %q: got nil cmd, want tea.Quit", c.String())
		}
	}
}

// TestUpdateOtherKeyIgnored asserts that pressing a non-quit key returns nil
// (no quit) and leaves state untouched while the list is not yet ready. If
// unknown keys accidentally quit or mutate state, behavior is
// non-deterministic across terminals.
func TestUpdateOtherKeyIgnored(t *testing.T) {
	m := model{status: "ready", width: 80, height: 24}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Errorf("key 'x': got non-nil cmd, want nil")
	}
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.status != "ready" || gotModel.width != 80 || gotModel.height != 24 {
		t.Errorf("state mutated by non-quit key: %+v", gotModel)
	}
}

// TestViewLoading asserts View shows a loading message while worktrees are
// being enumerated. This is the first feedback the user sees after launching.
func TestViewLoading(t *testing.T) {
	m := model{loading: true, status: "loading worktrees..."}
	view := m.View()
	if !strings.Contains(view, "loading worktrees") {
		t.Errorf("View missing loading message: %q", view)
	}
}

// TestViewNotReady asserts View renders the status string when the list is
// not ready. This covers error states and any pre-load messages.
func TestViewNotReady(t *testing.T) {
	m := model{status: "ready"}
	view := m.View()
	if !strings.Contains(view, "ready") {
		t.Errorf("View missing status %q: %q", "ready", view)
	}
}

// TestViewBeforeWindowSizeDoesNotPanic asserts View is safe to call when no
// WindowSizeMsg has been received yet. The model's width/height start at
// zero; the simple string views must not panic.
func TestViewBeforeWindowSizeDoesNotPanic(t *testing.T) {
	m := model{status: "ready"}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked with no WindowSizeMsg: %v", r)
		}
	}()
	_ = m.View()
}

// TestEntriesLoadedMsg asserts receiving worktree data builds the list,
// marks the model ready, and clears the loading flag. Without this the TUI
// would never transition from the loading screen to the picker.
func TestEntriesLoadedMsg(t *testing.T) {
	m := model{loading: true, width: 80, height: 24}
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.loading {
		t.Errorf("loading = true, want false")
	}
	if !gotModel.ready {
		t.Errorf("ready = false, want true")
	}
	if len(gotModel.entries) != 1 {
		t.Errorf("entries = %d, want 1", len(gotModel.entries))
	}
}

// TestViewReady asserts the rendered list contains the title. This confirms
// the list widget was built and is visible once worktrees are loaded.
func TestViewReady(t *testing.T) {
	m := model{loading: true, width: 80, height: 24}
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries})
	gotModel := got.(model)
	view := gotModel.View()
	if !strings.Contains(view, "Pick a worktree or branch") {
		t.Errorf("View missing list title: %q", view)
	}
}

// TestEnterSelectsEntry asserts that pressing Enter when the list is ready
// emits a selectedEntryMsg carrying the current entry. This is the primary
// selection affordance for the worktree picker.
func TestEnterSelectsEntry(t *testing.T) {
	m := model{loading: true, width: 80, height: 24}
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		{Type: worktree.TypeBranch, Branch: "feature", Path: ""},
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries})
	m = got.(model)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter returned nil cmd, want selectedEntryMsg")
	}
	msg := cmd()
	selected, ok := msg.(selectedEntryMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want selectedEntryMsg", msg)
	}
	if selected.entry.Branch != "main" {
		t.Errorf("selected branch = %q, want main", selected.entry.Branch)
	}
}

// TestSelectedEntryMsgTransitionsToModelPhase asserts that choosing a
// worktree moves the TUI into the model phase with a resolved agent, tag,
// and current model. Without this, the picker would have nowhere to go and
// the agent+model screen could never be shown.
func TestSelectedEntryMsgTransitionsToModelPhase(t *testing.T) {
	m := model{cfg: testConfig()}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel", gotModel.phase)
	}
	if gotModel.agent != "claude" {
		t.Errorf("agent = %q, want claude", gotModel.agent)
	}
	if gotModel.tag != "code" {
		t.Errorf("tag = %q, want code", gotModel.tag)
	}
	if gotModel.current.ID != "ollama/gemma4:9b" {
		t.Errorf("current = %q, want first code model", gotModel.current.ID)
	}
}

// TestRotateKeyAdvancesModel asserts pressing 'r' in the model phase advances
// the current model through the active tag group via rotation.Next. This is
// the explicit replacement for the bash tool's silent auto-rotation.
func TestRotateKeyAdvancesModel(t *testing.T) {
	// rotation.ForTag reads its next index from a per-tag state file on disk,
	// so isolate the state in a temp XDG_CONFIG_HOME and seed it to point at
	// the second model. Without this the test depends on the host's real
	// rotation-code.state and is non-deterministic.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stateDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agent-wt")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(rotation.StateFile(stateDir, "code"),
		[]byte("1\nollama/gemma4:9b\n"), 0o600); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	m := model{cfg: testConfig(), phase: phaseModel, tag: "code",
		current: config.Model{ID: "ollama/gemma4:9b"}}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gotModel := got.(model)
	if gotModel.current.ID != "ollama/gemma4:14b" {
		t.Errorf("current = %q, want second code model", gotModel.current.ID)
	}
}

// TestRotateKeyIgnoredInListPhase asserts 'r' does nothing while still on the
// worktree list. Rotation is only meaningful once an agent+model is chosen.
func TestRotateKeyIgnoredInListPhase(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseList, tag: "code",
		current: config.Model{ID: "ollama/gemma4:9b"}}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gotModel := got.(model)
	if gotModel.current.ID != "ollama/gemma4:9b" {
		t.Errorf("current mutated in list phase: %q", gotModel.current.ID)
	}
}

// TestModelKeyShowsPlaceholder asserts pressing 'm' in the model phase sets a
// status placeholder for the lesson 15 model browser. This confirms the key
// is wired before the browser exists.
func TestModelKeyShowsPlaceholder(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel, tag: "code"}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	gotModel := got.(model)
	if !strings.Contains(gotModel.status, "model browser") {
		t.Errorf("status missing browser placeholder: %q", gotModel.status)
	}
}

// TestToggleTagSwitchesGroup asserts pressing 'd' in the model phase flips
// the active tag group (code <-> design) and re-resolves the shown model to
// the new group's first entry. This powers cross-tag rotation from a single
// keystroke.
func TestToggleTagSwitchesGroup(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel, tag: "code",
		current: config.Model{ID: "ollama/gemma4:9b"}}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.tag != "design" || gotModel.otherTag != "code" {
		t.Errorf("tag/otherTag = (%q, %q), want (design, code)", gotModel.tag, gotModel.otherTag)
	}
	if gotModel.current.ID != "ollama/gemma4:design" {
		t.Errorf("current = %q, want first design model", gotModel.current.ID)
	}
}

// TestViewModelPhase asserts the model-phase View renders agent, model, and
// tag lines plus the keybind hints. This is the primary feedback surface for
// the agent+model screen.
func TestViewModelPhase(t *testing.T) {
	m := model{phase: phaseModel, agent: "claude", tag: "code",
		current: config.Model{ID: "ollama/gemma4:9b"}}
	view := m.View()
	for _, want := range []string{"agent", "claude", "model", "ollama/gemma4:9b", "[r] rotate"} {
		if !strings.Contains(view, want) {
			t.Errorf("View missing %q in:\n%s", want, view)
		}
	}
}
