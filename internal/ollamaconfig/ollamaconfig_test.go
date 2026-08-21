package ollamaconfig

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

// testTheme returns the default theme for tests.
func testTheme() themes.Theme {
	t, _ := themes.Get("default")
	return t
}

// TestInitReturnsLoadCmd verifies that Init starts the data loading
// command.
func TestInitReturnsLoadCmd(t *testing.T) {
	m := newModel(testTheme())
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd; expected load command")
	}
}

// TestUpdateWindowSizeMsg verifies that the model records terminal
// dimensions.
func TestUpdateWindowSizeMsg(t *testing.T) {
	m := newModel(testTheme())
	got, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.width != 80 || m2.height != 24 {
		t.Errorf("dimensions = (%d, %d), want (80, 24)", m2.width, m2.height)
	}
}

// TestLoadedMsgBuildsList verifies that a loadedMsg with entries
// transitions the model to the list phase with a populated list.
func TestLoadedMsgBuildsList(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}}, config: true, ollama: true},
		{model: config.Model{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud}, config: true, ollama: false},
	}
	got, _ := m.Update(loadedMsg{entries: entries, ollamaFound: true})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList", m2.phase)
	}
	if m2.list.Items() == nil || len(m2.list.Items()) != 2 {
		t.Fatalf("expected 2 list items, got %v", m2.list.Items())
	}
}

// TestLoadedMsgOllamaNotFound verifies that when ollama is not found,
// a status message is shown.
func TestLoadedMsgOllamaNotFound(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal}, config: true, ollama: false},
	}
	got, _ := m.Update(loadedMsg{entries: entries, ollamaFound: false})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.status == "" {
		t.Error("expected status message when ollama is not found")
	}
}

// TestEnterOnSyncedGoesToEdit verifies that pressing Enter on a synced
// entry transitions to the edit phase.
func TestEnterOnSyncedGoesToEdit(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}}, config: true, ollama: true},
	}
	m.list = buildSyncList(entries, testTheme(), 78, 22)
	m.phase = phaseList
	m.ready = true
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseEdit {
		t.Fatalf("phase = %v, want phaseEdit", m2.phase)
	}
}

// TestEnterOnMissingGoesToResolve verifies that pressing Enter on a
// missing entry transitions to the resolve phase.
func TestEnterOnMissingGoesToResolve(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud}, config: true, ollama: false},
	}
	m.list = buildSyncList(entries, testTheme(), 78, 22)
	m.phase = phaseList
	m.ready = true
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseResolve {
		t.Fatalf("phase = %v, want phaseResolve", m2.phase)
	}
}

// TestEnterOnUntrackedGoesToEdit verifies that pressing Enter on an
// untracked entry transitions to the edit phase with pre-filled values.
func TestEnterOnUntrackedGoesToEdit(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/llama3.2:3b", Family: "llama3.2:3b", ProviderID: "ollama", ModelName: "llama3.2:3b", Location: config.LocationLocal}, config: false, ollama: true},
	}
	m.list = buildSyncList(entries, testTheme(), 78, 22)
	m.phase = phaseList
	m.ready = true
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseEdit {
		t.Fatalf("phase = %v, want phaseEdit", m2.phase)
	}
	if !m2.editIsNew {
		t.Error("editIsNew should be true for untracked model")
	}
}

// TestEscFromEditReturnsToList verifies that Esc in the edit phase
// returns to the list phase.
func TestEscFromEditReturnsToList(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.phase = phaseEdit
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList", m2.phase)
	}
}

// TestEscFromResolveReturnsToList verifies that Esc in the resolve
// phase returns to the list phase.
func TestEscFromResolveReturnsToList(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.phase = phaseResolve
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList", m2.phase)
	}
}

// TestRKeyTriggersRefresh verifies that pressing 'r' in the list phase
// returns a command (the load command).
func TestRKeyTriggersRefresh(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.phase = phaseList
	m.ready = true
	// Need a list to avoid nil panics.
	m.list = list.New(nil, list.NewDefaultDelegate(), 78, 22)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("expected load command from 'r' key, got nil")
	}
}

// TestQuitKeys verifies that q and ctrl+c quit the program.
func TestQuitKeys(t *testing.T) {
	cases := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyCtrlC},
	}
	for _, c := range cases {
		m := newModel(testTheme())
		m.phase = phaseList
		m.ready = true
		m.list = list.New(nil, list.NewDefaultDelegate(), 78, 22)
		_, cmd := m.Update(c)
		if cmd == nil {
			t.Errorf("key %q: got nil cmd, want tea.Quit", c.String())
		}
	}
}

// TestQDoesNotQuitWhileFiltering asserts that 'q' types into the union
// list's incremental filter (opened with '/') instead of quitting the
// TUI. Before the isTyping guard, a filter query containing 'q' (e.g.
// "qwen") killed the app on the first 'q' instead of narrowing the list.
func TestQDoesNotQuitWhileFiltering(t *testing.T) {
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b"}, config: true, ollama: true},
	}
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.phase = phaseList
	m.ready = true
	m.list = buildSyncList(entries, testTheme(), 78, 22)
	// Open the filter exactly like a user pressing '/'.
	m.list, _ = m.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.list.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.list.FilterState())
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(m2.list.FilterInput.Value(), "q") {
		t.Errorf("filter input = %q, want to contain q (quit hijacked the key)", m2.list.FilterInput.Value())
	}
}

// TestEscDoesNotQuitWhileFiltering asserts that Esc cancels the filter
// (bubbles/list's own behavior) instead of the global esc handler
// quitting the TUI. Before the isTyping guard, Esc while filtering fell
// through to the phaseList branch and called tea.Quit.
func TestEscDoesNotQuitWhileFiltering(t *testing.T) {
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b"}, config: true, ollama: true},
	}
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.phase = phaseList
	m.ready = true
	m.list = buildSyncList(entries, testTheme(), 78, 22)
	m.list, _ = m.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.list.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.list.FilterState())
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("expected no quit command while canceling the filter with esc")
	}
}

// TestLoadedMsgErrorRecoversToUsableList verifies that a loadedMsg
// carrying an error (e.g. `ollama list` failing because the daemon
// isn't running) still leaves the TUI in a usable state: ready so 'r'
// refresh and 'q' quit are reachable, with the error shown above an
// empty list rather than a permanently stuck bare error screen.
func TestLoadedMsgErrorRecoversToUsableList(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	got, _ := m.Update(loadedMsg{err: fmt.Errorf("ollama list: exit status 1")})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !m2.ready {
		t.Fatal("expected ready=true after a failed load so refresh/quit are reachable")
	}
	if m2.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList", m2.phase)
	}
	if m2.listError == "" {
		t.Error("expected listError to be set")
	}
	// 'r' must now trigger a refresh instead of being unreachable.
	_, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Error("expected 'r' to trigger a refresh command after a failed load")
	}
}

// TestSecondEnterWhileSavingIsNoOp verifies that pressing Enter again on
// the edit screen before the first save's savedMsg has landed does not
// re-run handleEditSave. Without the m.saving guard, a second Enter
// before the async round-trip completes would append a duplicate model
// (editIsNew stays true) or race a second config.Load/Save.
func TestSecondEnterWhileSavingIsNoOp(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.phase = phaseEdit
	m.saving = true
	m.editModel = config.Model{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b"}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected no save command while a save is already in flight")
	}
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseEdit {
		t.Fatalf("phase = %v, want phaseEdit (unchanged)", m2.phase)
	}
}

// TestSavedMsgClearsSavingFlag verifies that savedMsg resets m.saving
// regardless of success or failure, so a later Enter press is not
// permanently blocked by a stale in-flight guard.
func TestSavedMsgClearsSavingFlag(t *testing.T) {
	m := newModel(testTheme())
	m.saving = true
	got, _ := m.Update(savedMsg{err: fmt.Errorf("disk full")})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.saving {
		t.Error("expected saving=false after savedMsg, even on error")
	}
}

// TestPullChoiceRunsPullCmd verifies that choosing "Pull with ollama" on
// the resolve screen dispatches pullCmd for the model being resolved and
// returns to the list phase, using the pullCmd override so no real
// `ollama pull` subprocess runs.
func TestPullChoiceRunsPullCmd(t *testing.T) {
	orig := pullCmd
	defer func() { pullCmd = orig }()
	var gotModelName string
	pullCmd = func(modelName string) tea.Cmd {
		gotModelName = modelName
		return func() tea.Msg { return pulledMsg{} }
	}

	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.enterResolve(config.Model{ModelName: "kimi:cloud"})
	m.resolve.Select(0) // "Pull with ollama" is the first choice

	got, cmd := m.handleEnter()
	if cmd == nil {
		t.Fatal("expected a command from the pull choice, got nil")
	}
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("handleEnter returned %T, want model", got)
	}
	if m2.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList", m2.phase)
	}
	msg := cmd()
	if _, ok := msg.(pulledMsg); !ok {
		t.Fatalf("cmd() returned %T, want pulledMsg", msg)
	}
	if gotModelName != "kimi:cloud" {
		t.Errorf("pullCmd called with %q, want kimi:cloud", gotModelName)
	}
}
