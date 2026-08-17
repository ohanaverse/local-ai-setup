package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// testConfig returns a Config with one agent (claude, supporting the
// ollama provider) and a two-model code tag group plus one design model.
// Tests across the picker, rotation, and launch need a real catalog so
// the agent filter, tag filter, and rotation all have something to
// operate on.
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
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
}

// TestInitLoadsEntries asserts Init starts background worktree enumeration.
// Without this command, the TUI would sit forever at the loading screen.
func TestInitLoadsEntries(t *testing.T) {
	m := model{status: "loading worktrees..."}
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
	m := model{status: "loading worktrees..."}
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

// TestEntriesLoadedMsg asserts receiving worktree data builds the list and
// marks the model ready. Without this the TUI would never transition from the
// loading screen to the picker.
func TestEntriesLoadedMsg(t *testing.T) {
	m := model{width: 80, height: 24}
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !gotModel.ready {
		t.Errorf("ready = false, want true")
	}
	if len(gotModel.entries) != 1 {
		t.Errorf("entries = %d, want 1", len(gotModel.entries))
	}
}

// TestEntriesLoadedSetsDefaultBranchWarning asserts that when the only
// pickable target is the current worktree on the repo's default branch, the
// list title is updated to warn the user. Without this, a user on main could
// launch an agent without realizing they are working directly on the
// protected branch.
func TestEntriesLoadedSetsDefaultBranchWarning(t *testing.T) {
	cfg := &config.Config{DefaultTag: "code"}
	m := model{cfg: cfg, width: 80, height: 24}

	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"},
	}
	newM, _ := m.Update(entriesLoadedMsg{entries: entries, defaultBranch: "main"})
	mm := newM.(model)

	if mm.defaultBranch != "main" {
		t.Fatalf("defaultBranch = %q, want main", mm.defaultBranch)
	}
	if !strings.Contains(mm.list.Title, "main") {
		t.Fatalf("expected title to contain default branch warning, got %q", mm.list.Title)
	}
}

// TestEntriesLoadedNoWarningForMultipleEntries asserts that when there are
// multiple choices, no default-branch warning is shown.
func TestEntriesLoadedNoWarningForMultipleEntries(t *testing.T) {
	cfg := &config.Config{DefaultTag: "code"}
	m := model{cfg: cfg, width: 80, height: 24}

	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"},
		{Type: worktree.TypeBranch, Branch: "feature"},
	}
	newM, _ := m.Update(entriesLoadedMsg{entries: entries, defaultBranch: "main"})
	mm := newM.(model)

	if strings.Contains(mm.list.Title, "WARNING") {
		t.Fatalf("expected no warning title, got %q", mm.list.Title)
	}
}

// TestViewReady asserts the rendered list contains the title. This confirms
// the list widget was built and is visible once worktrees are loaded.
func TestViewReady(t *testing.T) {
	m := model{width: 80, height: 24}
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
// selection affordance for the worktree picker. (The list's first item is
// the "+ New worktree…" sentinel; we step down to a real entry before
// pressing Enter, since Enter on the sentinel opens the new-worktree
// prompt instead.)
func TestEnterSelectsEntry(t *testing.T) {
	m := model{width: 80, height: 24}
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		{Type: worktree.TypeBranch, Branch: "feature", Path: ""},
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries})
	m = got.(model)
	// Move down past the sentinel (index 0) to the first real entry.
	var downCmd tea.Cmd
	m.list, downCmd = m.list.Update(tea.KeyMsg{Type: tea.KeyDown})
	_ = downCmd
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
// worktree moves the TUI into the model phase with a resolved agent,
// tag, and a populated picker list. The cursor lands on the rotation's
// next-to-use model (index 0 with no state file).
func TestSelectedEntryMsgTransitionsToModelPhase(t *testing.T) {
	tempStateDir(t) // isolate XDG_CONFIG_HOME so rotation state is fresh
	m := model{cfg: testConfig(), width: 80, height: 24}
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
		t.Errorf("current = %q, want first code model (rotation index 0)", gotModel.current.ID)
	}
	if len(gotModel.models.Items()) == 0 {
		t.Error("models list is empty; expected populated picker")
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

// TestToggleTagSwitchesGroup asserts pressing 'd' in the model phase flips
// the active tag group (code <-> design) and re-resolves the shown model to
// the new group's first entry. This powers cross-tag rotation from a single
// keystroke.
// TestToggleTagSwitchesGroup asserts pressing 'd' in the model phase flips
// the active tag group (code <-> design) and rebuilds the picker from
// the new group's models. (otherTag is now computed via oppositeTag;
// only m.tag is asserted.)
func TestToggleTagSwitchesGroup(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "design", "0\nollama/gemma4:design\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.tag != "design" {
		t.Errorf("tag = %q, want design", gotModel.tag)
	}
	if gotModel.current.ID != "ollama/gemma4:design" {
		t.Errorf("current = %q, want ollama/gemma4:design", gotModel.current.ID)
	}
}

// (TestViewModelPhase was rewritten in agent_model_test.go using the
// new picker-based View.)

// TestOllamaWarnShownWhenUnavailable asserts that when the current model is an
// unavailable ollama model, the TUI transitions to phaseOllamaWarn.
func TestOllamaWarnShownWhenUnavailable(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama"}},
		Models: []config.Model{
			{ID: "ollama/test-model-xyz-not-real", ProviderID: "ollama", ModelName: "test-model-xyz-not-real", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	m := phaseModelWithList(t, cfg, "claude", "code")
	m.selectedPath = "/repo"

	// Simulate pressing enter — this should trigger the ollama check.
	// The model name is guaranteed not to exist, so it will be unavailable.
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := newM.(model)

	if mm.phase != phaseOllamaWarn {
		t.Fatalf("expected phaseOllamaWarn, got %d", mm.phase)
	}
	if !strings.Contains(mm.ollamaWarnModel.Title, "test-model-xyz-not-real") {
		t.Fatalf("expected title to contain model name, got %q", mm.ollamaWarnModel.Title)
	}
}

// TestNoOllamaWarnForNonOllamaModel asserts that non-ollama models skip the
// availability check and proceed directly to launch/resume.
func TestNoOllamaWarnForNonOllamaModel(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Models: []config.Model{
			{ID: "openrouter/gpt-4", ProviderID: "openrouter", Tags: []string{"code"}},
		},
	}
	m := model{cfg: cfg, phase: phaseModel, width: 80, height: 24, agent: "claude", tag: "code", current: cfg.Models[0], selectedPath: "/repo"}

	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("enter")})
	mm := newM.(model)

	if mm.phase == phaseOllamaWarn {
		t.Fatal("expected no ollama warn for non-ollama model")
	}
	// Should have produced a command (either launch or resume prompt).
	if cmd == nil {
		t.Fatal("expected a command from enter on non-ollama model")
	}
}

// TestOllamaWarnCancel returns to phaseModel.
func TestOllamaWarnCancel(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Models: []config.Model{
			{ID: "ollama/test-model-xyz-not-real", ProviderID: "ollama", ModelName: "test-model-xyz-not-real", Tags: []string{"code"}},
		},
	}
	m := model{cfg: cfg, phase: phaseOllamaWarn, width: 80, height: 24, agent: "claude", tag: "code", current: cfg.Models[0], selectedPath: "/repo"}
	m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), 78, 22)

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("esc")})
	mm := newM.(model)

	if mm.phase != phaseModel {
		t.Fatalf("expected phaseModel after cancel, got %d", mm.phase)
	}
}

// TestNKeyOpensNewWorktreePhase asserts pressing `n` on the picker
// list transitions to phaseNewWorktree. Without this wiring, the
// keyboard shortcut is dead and only the sentinel is reachable.
func TestNKeyOpensNewWorktreePhase(t *testing.T) {
	m := model{phase: phaseList, ready: true, width: 80, height: 24}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", gotModel.phase)
	}
}

// TestNKeyIgnoredWhileLoading asserts `n` does not transition
// before the list is ready. A user mashing `n` while the picker is
// still loading would otherwise jump into an empty prompt and
// have to esc out.
func TestNKeyIgnoredWhileLoading(t *testing.T) {
	m := model{phase: phaseList, ready: false, width: 80, height: 24}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase == phaseNewWorktree {
		t.Errorf("phase = phaseNewWorktree, want phaseList (loading not complete)")
	}
}

// TestEnterOnSentinelOpensNewWorktreePhase asserts picking the
// sentinel and pressing Enter transitions to phaseNewWorktree. The
// sentinel must be Enter-able, not just `n`-able.
func TestEnterOnSentinelOpensNewWorktreePhase(t *testing.T) {
	// Build a list with just the sentinel as the selected item.
	l := buildList(nil, 80, 24)
	m := model{
		phase: phaseList, ready: true, width: 80, height: 24,
		list: l,
	}
	// Sentinel is the first (and only) item, so the cursor is on it.
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", gotModel.phase)
	}
}

// TestEnterOnSentinelIgnoredWhileLoading asserts the sentinel
// cannot be picked before the list is ready. Consistent with the
// existing list-unready guard and the `n` keypress guard.
func TestEnterOnSentinelIgnoredWhileLoading(t *testing.T) {
	// Even with a sentinel-bearing list, ready=false short-circuits.
	l := buildList(nil, 80, 24)
	m := model{
		phase: phaseList, ready: false, width: 80, height: 24,
		list: l,
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase == phaseNewWorktree {
		t.Errorf("phase = phaseNewWorktree, want phaseList (loading not complete)")
	}
}

// TestEscOnNewWorktreeReturnsToList asserts pressing esc on the
// new-worktree prompt returns to the picker. Without this, the
// user is stuck in the prompt with no way out.
func TestEscOnNewWorktreeReturnsToList(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		newInput: newInputModel(80),
		newError: "previous error",
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList", gotModel.phase)
	}
	if gotModel.newError != "" {
		t.Errorf("newError = %q, want empty (esc clears it)", gotModel.newError)
	}
}

// TestEnterOnNewWorktreeEmptyNameKeepsPhase asserts an empty name
// does not dispatch a create command. The user must see the inline
// error and try again; an empty-name dispatch would race a
// `worktree add` for a malformed path.
func TestEnterOnNewWorktreeEmptyNameKeepsPhase(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		repoRoot: "/tmp/repo",
		newInput: newInputModel(80),
	}
	// newInput is empty by default.
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", gotModel.phase)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil (empty name should not dispatch)", cmd)
	}
	if gotModel.newError == "" {
		t.Error("newError = empty, want validation error message")
	}
}

// TestEnterOnNewWorktreeDispatchesCmd asserts a non-empty name
// triggers the ensureNewWorktreeCmd with the repo root and the
// input's current value. The cmd is what kicks off the async git
// work; this is the contract that wires the input to the worker.
func TestEnterOnNewWorktreeDispatchesCmd(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		repoRoot: "/tmp/repo",
		newInput: newInputModel(80),
	}
	m.newInput.SetValue("my-feature")

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree (cmd runs async; phase stays)", gotModel.phase)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want ensureNewWorktreeCmd")
	}
	// Run the cmd and verify the message type.
	msg := cmd()
	if _, ok := msg.(newWorktreeCreatedMsg); !ok {
		t.Errorf("cmd returned %T, want newWorktreeCreatedMsg", msg)
	}
}

// TestNewWorktreeCreatedErrorStaysOnPrompt asserts a git failure
// surfaces inline and keeps the user on the prompt. A silent
// failure would leave the user with no feedback.
func TestNewWorktreeCreatedErrorStaysOnPrompt(t *testing.T) {
	m := model{phase: phaseNewWorktree, width: 80, height: 24}
	got, _ := m.Update(newWorktreeCreatedMsg{err: errMock("git worktree add: boom")})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree (stays on error)", gotModel.phase)
	}
	if gotModel.newError == "" {
		t.Error("newError = empty, want error from msg")
	}
	if gotModel.pendingHighlight != "" {
		t.Errorf("pendingHighlight = %q, want empty (no success)", gotModel.pendingHighlight)
	}
}

// TestNewWorktreeCreatedSuccessTriggersReload asserts a successful
// create sets pendingHighlight, transitions back to phaseList, and
// dispatches loadEntriesCmd. The reload + highlight flow is what
// brings the user back to the picker on the new entry.
func TestNewWorktreeCreatedSuccessTriggersReload(t *testing.T) {
	m := model{phase: phaseNewWorktree, width: 80, height: 24}
	got, cmd := m.Update(newWorktreeCreatedMsg{path: "/repo/.worktrees/x", name: "x"})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList (return to picker)", gotModel.phase)
	}
	if gotModel.pendingHighlight != "x" {
		t.Errorf("pendingHighlight = %q, want x", gotModel.pendingHighlight)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want loadEntriesCmd")
	}
}

// TestEntriesLoadedAppliesPendingHighlight asserts that after a
// reload, the list cursor moves to the entry matching the
// pendingHighlight branch. This is the "you just created this,
// here it is" feedback.
func TestEntriesLoadedAppliesPendingHighlight(t *testing.T) {
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"},
		{Type: worktree.TypeWorktree, Branch: "x", Path: "/repo/.worktrees/x"},
	}
	m := model{
		width:            80,
		height:           24,
		pendingHighlight: "x",
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries, defaultBranch: "main"})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	// Find the index of "x" — should be selected.
	items := gotModel.list.Items()
	var xIdx int = -1
	for i, it := range items {
		if ei, ok := it.(entryItem); ok && ei.kind == kindEntry && ei.entry.Branch == "x" {
			xIdx = i
			break
		}
	}
	if xIdx < 0 {
		t.Fatal("entry x not found in reloaded list")
	}
	if gotModel.list.Index() != xIdx {
		t.Errorf("list.Index() = %d, want %d (x)", gotModel.list.Index(), xIdx)
	}
	if gotModel.pendingHighlight != "" {
		t.Errorf("pendingHighlight = %q, want cleared after applying", gotModel.pendingHighlight)
	}
}

// TestEntriesLoadedNoPendingHighlightLeavesCursorAtZero asserts
// that without a pendingHighlight, the list cursor stays at the
// default (top). This is the normal reload path — cursor reset is
// fine.
func TestEntriesLoadedNoPendingHighlightLeavesCursorAtZero(t *testing.T) {
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"},
	}
	m := model{width: 80, height: 24}
	got, _ := m.Update(entriesLoadedMsg{entries: entries, defaultBranch: "main"})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	// After a fresh build, the cursor is at 0 (the sentinel). That's
	// acceptable behavior — this test just guards against
	// pendingHighlight being unexpectedly set.
	if gotModel.pendingHighlight != "" {
		t.Errorf("pendingHighlight = %q, want empty", gotModel.pendingHighlight)
	}
}

// TestQTypesInNewWorktreePrompt asserts that pressing 'q' in the
// new-worktree prompt types the character instead of quitting the TUI.
// Before the phase-gated quit fix, any branch name containing 'q'
// (e.g. quick-fix) killed the app mid-typing and lost the input.
func TestQTypesInNewWorktreePrompt(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		newInput: newInputModel(80),
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(gotModel.newInput.Value(), "q") {
		t.Errorf("input value = %q, want to contain q (quit hijacked the key)", gotModel.newInput.Value())
	}
}

// TestQDoesNotQuitWhileFiltering asserts that 'q' types into the list
// filter (bubbles/list incremental filter) rather than quitting. The
// filter is opened with '/'; a query like "quality" would otherwise
// quit the TUI on the first 'q'.
func TestQDoesNotQuitWhileFiltering(t *testing.T) {
	entries := []worktree.Entry{{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"}}
	m := model{phase: phaseList, ready: true, width: 80, height: 24}
	m.list = buildList(entries, 78, 22)
	// Open the filter exactly like a user pressing '/'.
	m.list, _ = m.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.list.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.list.FilterState())
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(gotModel.list.FilterInput.Value(), "q") {
		t.Errorf("filter input = %q, want to contain q (quit hijacked the key)", gotModel.list.FilterInput.Value())
	}
}

// TestNNotHijackedWhileFiltering asserts that 'n' types into the list
// filter instead of opening the new-worktree prompt. Before the fix,
// the 'n' keypress was intercepted before bubbles/list saw it, so no
// filter query could ever contain 'n' — branches with 'n' in their
// name became unfilterable by typing.
func TestNNotHijackedWhileFiltering(t *testing.T) {
	entries := []worktree.Entry{{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"}}
	m := model{phase: phaseList, ready: true, width: 80, height: 24}
	m.list = buildList(entries, 78, 22)
	// Open the filter exactly like a user pressing '/'.
	m.list, _ = m.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.list.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.list.FilterState())
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase == phaseNewWorktree {
		t.Error("phase = phaseNewWorktree, want phaseList (n hijacked the filter)")
	}
	if !strings.Contains(gotModel.list.FilterInput.Value(), "n") {
		t.Errorf("filter input = %q, want to contain n", gotModel.list.FilterInput.Value())
	}
}

// TestEnterOnNewWorktreeSetsCreating asserts the first Enter in the
// prompt marks the create as in-flight. The flag is what later Enters
// check to avoid dispatching a second concurrent `git worktree add`.
func TestEnterOnNewWorktreeSetsCreating(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		repoRoot: "/tmp/repo",
		newInput: newInputModel(80),
	}
	m.newInput.SetValue("my-feature")
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !gotModel.creating {
		t.Error("creating = false, want true after first Enter")
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want ensureNewWorktreeCmd")
	}
}

// TestEnterIgnoredWhileCreating asserts a second Enter while a create
// is in flight is ignored (no command dispatched). Without the
// in-flight guard, two concurrent git worktree add calls raced and one
// failed with a spurious "already exists" error.
func TestEnterIgnoredWhileCreating(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		repoRoot: "/tmp/repo",
		newInput: newInputModel(80),
		creating: true,
	}
	m.newInput.SetValue("my-feature")
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if cmd != nil {
		t.Error("cmd = non-nil, want nil (second Enter must not dispatch)")
	}
	if !gotModel.creating {
		t.Error("creating = false, want true (flag must persist while in flight)")
	}
}

// TestNewWorktreeCreatedClearsCreating asserts the in-flight flag is
// cleared when the async create reports back (success or error). A
// stale true would lock the prompt forever.
func TestNewWorktreeCreatedClearsCreating(t *testing.T) {
	m := model{phase: phaseNewWorktree, creating: true}
	got, _ := m.Update(newWorktreeCreatedMsg{path: "/repo/.worktrees/x", name: "x"})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.creating {
		t.Error("creating = true, want false after result msg")
	}
}

// TestReloadErrorVisibleAboveList asserts that a reload failure after
// a successful create renders above the list. Before the fix the error
// was stored in m.status, which View only shows while !m.ready, so the
// user saw a stale list with no explanation after the reload failed.
func TestReloadErrorVisibleAboveList(t *testing.T) {
	m := model{width: 80, height: 24}
	got, _ := m.Update(entriesLoadedMsg{
		entries: []worktree.Entry{{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"}},
	})
	m = got.(model) // ready == true
	if !m.ready {
		t.Fatal("setup failed: model not ready")
	}

	got, _ = m.Update(entriesLoadedMsg{err: errMock("git worktree list: boom")})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.listError == "" {
		t.Fatal("listError = empty, want reload error")
	}
	if view := gotModel.View(); !strings.Contains(view, "boom") {
		t.Errorf("View missing reload error, got:\n%s", view)
	}
}

// TestViewCreatingIndicator asserts the prompt shows a "creating…"
// status while a create is in flight and hides the enter hint. This is
// the visible feedback that explains why a second Enter does nothing.
func TestViewCreatingIndicator(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		newInput: newInputModel(80),
		creating: true,
	}
	m.newInput.SetValue("my-feature")
	view := m.View()
	if !strings.Contains(view, "creating my-feature") {
		t.Errorf("View missing creating indicator, got:\n%s", view)
	}
	if strings.Contains(view, "[enter] create") {
		t.Errorf("View shows enter hint while creating, got:\n%s", view)
	}
}

// errMock is a small helper to make error literals readable in tests.
type errMock string

func (e errMock) Error() string { return string(e) }
