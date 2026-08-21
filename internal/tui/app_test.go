package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
	"github.com/ohanaverse/agent-worktree/internal/themes"
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
//
// After Ruling 17 (Task 3) removed the m.entries field, this test asserts
// directly against the built list instead: the picker still contains the
// sentinel + the single entry, in that order, which is what the user sees.
func TestEntriesLoadedMsg(t *testing.T) {
	m := model{width: 80, height: 24}
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
	}
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: entries}}
	got, _ := m.Update(entriesLoadedMsg{groups: groups, defaultBranch: "main", repoRoot: "/tmp/repo"})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !gotModel.ready {
		t.Errorf("ready = false, want true")
	}
	items := gotModel.list.Items()
	// Sentinel + one real entry = 2 items.
	if len(items) != 2 {
		t.Fatalf("list items = %d, want 2 (sentinel + entry)", len(items))
	}
	first, ok := items[0].(entryItem)
	if !ok || first.kind != kindNewWorktree {
		t.Errorf("items[0] = %+v, want sentinel (kindNewWorktree)", items[0])
	}
	second, ok := items[1].(entryItem)
	if !ok {
		t.Fatalf("items[1] is %T, want entryItem", items[1])
	}
	if second.entry.Branch != "main" {
		t.Errorf("items[1] branch = %q, want main", second.entry.Branch)
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
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: entries}}
	newM, _ := m.Update(entriesLoadedMsg{groups: groups, defaultBranch: "main"})
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
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: entries}}
	newM, _ := m.Update(entriesLoadedMsg{groups: groups, defaultBranch: "main"})
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
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: entries}}
	got, _ := m.Update(entriesLoadedMsg{groups: groups})
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
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: entries}}
	got, _ := m.Update(entriesLoadedMsg{groups: groups})
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

// TestEnterOnDefaultBranchTriggersGuard asserts that selecting the current
// worktree on the default branch trips the phaseGuardWarn prompt before
// launching. Bare default-branch rows are filtered out of the picker (the
// default branch must never be a linked worktree), so the guard now only
// fires for a worktree entry on the default branch. A non-default branch must
// NOT trip the guard — it selects straight through (dispatching a worktree
// create for a bare branch).
func TestEnterOnDefaultBranchTriggersGuard(t *testing.T) {
	m := model{width: 80, height: 24}
	groups := []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
			{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		}},
		{Kind: worktree.GroupLocalBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "other"},
		}},
	}
	got, _ := m.Update(entriesLoadedMsg{groups: groups, defaultBranch: "main", repoRoot: "/tmp/repo"})
	m = got.(model)

	// Select the current worktree on the default branch.
	idx := pickEntryIndex(t, m, func(ei entryItem) bool {
		return ei.kind == kindEntry && ei.entry.Type == worktree.TypeCurrent && ei.entry.Branch == "main"
	})
	m.list.Select(idx)
	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := newM.(model)
	if mm.phase != phaseGuardWarn {
		t.Fatalf("current default row: phase = %v, want phaseGuardWarn (must trip guard, not launch)", mm.phase)
	}
	if cmd != nil {
		t.Errorf("current default row: expected nil cmd (guard prompt, no launch), got %T", cmd())
	}

	// A non-default branch must select straight through (no guard).
	mm.phase = phaseList // reset for the contrast case
	otherIdx := pickEntryIndex(t, mm, func(ei entryItem) bool {
		return ei.kind == kindEntry && ei.entry.Branch == "other"
	})
	mm.list.Select(otherIdx)
	newM2, cmd2 := mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm2 := newM2.(model)
	if mm2.phase == phaseGuardWarn {
		t.Fatalf("other row: phase = phaseGuardWarn, want no guard for non-default branch")
	}
	if cmd2 == nil {
		t.Fatal("other row: expected a cmd (bare branch dispatches ensureBranchWorktreeCmd), got nil")
	}
}

// TestLaunchesOnDefaultBranchCoversRemoteForm asserts the guard helper
// matches both the bare local default branch and the remote-tracking form of
// the default under ANY remote (origin/main, upstream/main, ...). The remote
// form is what Enumerate emits when the default branch exists only as a
// remote ref; EnsureForBranch turns it into a local main, so it must trip the
// guard too. It also asserts the helper is precise: a branch whose name ends
// in "/main" but is not the default — a local feature/main, or a worktree
// checked out on feature/main — must NOT trip the guard, since launching
// there runs the feature branch, not the default.
func TestLaunchesOnDefaultBranchCoversRemoteForm(t *testing.T) {
	cases := []struct {
		name      string
		e         worktree.Entry
		groupKind worktree.GroupKind
		want      bool
	}{
		{"current on main", worktree.Entry{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"}, worktree.GroupWorktrees, true},
		{"worktree on main", worktree.Entry{Type: worktree.TypeWorktree, Branch: "main", Path: "/repo/.worktrees/main"}, worktree.GroupWorktrees, true},
		{"bare local main", worktree.Entry{Type: worktree.TypeBranch, Branch: "main"}, worktree.GroupLocalBranches, true},
		{"remote origin/main", worktree.Entry{Type: worktree.TypeBranch, Branch: "origin/main"}, worktree.GroupRemoteBranches, true},
		{"remote upstream/main", worktree.Entry{Type: worktree.TypeBranch, Branch: "upstream/main"}, worktree.GroupRemoteBranches, true},
		{"feature", worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"}, worktree.GroupLocalBranches, false},
		{"remote origin/feature", worktree.Entry{Type: worktree.TypeBranch, Branch: "origin/feature"}, worktree.GroupRemoteBranches, false},
		{"bare local feature/main", worktree.Entry{Type: worktree.TypeBranch, Branch: "feature/main"}, worktree.GroupLocalBranches, false},
		{"worktree on feature/main", worktree.Entry{Type: worktree.TypeWorktree, Branch: "feature/main", Path: "/repo/.worktrees/main"}, worktree.GroupWorktrees, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := launchesOnDefaultBranch(tc.e, tc.groupKind, "main"); got != tc.want {
				t.Errorf("launchesOnDefaultBranch(%+v, %v, main) = %v, want %v", tc.e, tc.groupKind, got, tc.want)
			}
		})
	}
}

// TestEnterOnSeparatorIsIgnored asserts that pressing Enter on the
// locals→remotes separator row does nothing rather than forwarding a
// zero-value worktree.Entry to selectedEntryMsg — which would launch the
// agent in wt's CWD with cmd.Dir="". The separator is a visual divider
// only; it carries no pickable target.
func TestEnterOnSeparatorIsIgnored(t *testing.T) {
	m := model{width: 80, height: 24}
	groups := []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
			{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		}},
		{Kind: worktree.GroupLocalBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "feature"},
		}},
		{Kind: worktree.GroupRemoteBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "origin/dev"},
		}},
	}
	got, _ := m.Update(entriesLoadedMsg{groups: groups, defaultBranch: "main"})
	m = got.(model)

	idx := -1
	for i, it := range m.list.Items() {
		if ei, ok := it.(entryItem); ok && ei.kind == kindSeparator {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("separator row not found in list")
	}
	m.list.Select(idx)
	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := newM.(model)
	if mm.phase != phaseList {
		t.Fatalf("separator Enter: phase = %v, want phaseList (must not transition)", mm.phase)
	}
	if cmd != nil {
		t.Errorf("separator Enter: expected nil cmd, got %T (would forward a zero entry to launch)", cmd())
	}
}

// TestDefaultBranchWarningFiresWhenOnlyDefaultBranch asserts that the
// default-branch warning still fires when the only entry is the current
// worktree on main with no other branches or worktrees. This is the
// minimal-repo scenario where the user has nowhere else to switch.
func TestDefaultBranchWarningFiresWhenOnlyDefaultBranch(t *testing.T) {
	cfg := &config.Config{DefaultTag: "code"}
	m := model{cfg: cfg, width: 80, height: 24}
	groups := []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
			{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"},
		}},
	}
	newM, _ := m.Update(entriesLoadedMsg{groups: groups, defaultBranch: "main"})
	mm := newM.(model)
	if !strings.Contains(mm.list.Title, "WARNING") {
		t.Fatalf("expected default-branch warning title, got %q", mm.list.Title)
	}
}

// pickEntryIndex returns the index of the first list item matching match, or
// fails the test. Used by Enter-handler tests to position the cursor on a
// specific entry without depending on list ordering.
func pickEntryIndex(t *testing.T, m model, match func(entryItem) bool) int {
	t.Helper()
	for i, it := range m.list.Items() {
		ei, ok := it.(entryItem)
		if !ok {
			continue
		}
		if match(ei) {
			return i
		}
	}
	t.Fatal("no list item matched the requested entry")
	return -1
}

// TestSelectedEntryMsgTransitionsToAgentPhase asserts that choosing a
// worktree moves the TUI into the agent+command picker phase, with the
// agent list populated and selectedPath recorded. The model-tag resolution
// and list build used to happen here, but moved to phaseAgent's Enter
// handler in PR 2 — full transition tests for that handler are added in
// the follow-up task.
func TestSelectedEntryMsgTransitionsToAgentPhase(t *testing.T) {
	tempStateDir(t) // isolate XDG_CONFIG_HOME so rotation state is fresh
	m := model{cfg: testConfig(), width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: "/tmp/feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent", gotModel.phase)
	}
	if gotModel.selectedPath != "/tmp/feature" {
		t.Errorf("selectedPath = %q, want /tmp/feature", gotModel.selectedPath)
	}
	if len(gotModel.agentList.Items()) == 0 {
		t.Error("agentList is empty; expected at least one configured agent")
	}
}

// TestEnterOnBareBranchDispatchesCreate asserts that selecting a bare
// branch (TypeBranch, Path="") dispatches ensureBranchWorktreeCmd rather
// than proceeding straight to the agent picker. Without this, the agent
// would launch in wt's CWD (cmd.Dir="") instead of a worktree for the
// branch.
func TestEnterOnBareBranchDispatchesCreate(t *testing.T) {
	m := model{width: 80, height: 24}
	groups := []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
			{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		}},
		{Kind: worktree.GroupLocalBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "feature"},
		}},
	}
	got, _ := m.Update(entriesLoadedMsg{groups: groups, defaultBranch: "main", repoRoot: "/tmp/repo"})
	m = got.(model)

	idx := pickEntryIndex(t, m, func(ei entryItem) bool {
		return ei.kind == kindEntry && ei.entry.Branch == "feature"
	})
	m.list.Select(idx)

	// Enter emits selectedEntryMsg (the guard check passes for a non-default
	// branch), which the runtime would feed back into Update.
	_, selCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if selCmd == nil {
		t.Fatal("Enter returned nil cmd, want selectedEntryMsg")
	}
	selMsg := selCmd()
	selected, ok := selMsg.(selectedEntryMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want selectedEntryMsg", selMsg)
	}

	// Feeding selectedEntryMsg back in must dispatch the worktree create.
	newM, createCmd := m.Update(selected)
	mm := newM.(model)
	if !mm.creating {
		t.Error("creating = false, want true while the bare-branch create is in flight")
	}
	if createCmd == nil {
		t.Fatal("cmd = nil, want ensureBranchWorktreeCmd")
	}
	msg := createCmd()
	if _, ok := msg.(branchWorktreeCreatedMsg); !ok {
		t.Errorf("cmd returned %T, want branchWorktreeCreatedMsg", msg)
	}
}

// TestBranchWorktreeCreatedSuccessProceeds asserts that a successful
// bare-branch create sets selectedPath to the created worktree and proceeds
// to the agent picker. Without this, the created worktree would be ignored
// and the agent would still launch in wt's CWD.
func TestBranchWorktreeCreatedSuccessProceeds(t *testing.T) {
	tempStateDir(t) // isolate XDG_CONFIG_HOME so rotation state is fresh
	m := model{cfg: testConfig(), width: 80, height: 24, creating: true}
	got, _ := m.Update(branchWorktreeCreatedMsg{path: "/tmp/repo/.worktrees/feature", branch: "feature"})
	gotModel := got.(model)
	if gotModel.creating {
		t.Error("creating = true, want false after create completes")
	}
	if gotModel.selectedPath != "/tmp/repo/.worktrees/feature" {
		t.Errorf("selectedPath = %q, want /tmp/repo/.worktrees/feature", gotModel.selectedPath)
	}
	if gotModel.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent", gotModel.phase)
	}
}

// TestBranchWorktreeCreatedErrorReturnsToList asserts that a failed
// bare-branch create surfaces the error and returns to the list. A silent
// failure would leave the user on a stale screen with no feedback.
func TestBranchWorktreeCreatedErrorReturnsToList(t *testing.T) {
	m := model{phase: phaseList, width: 80, height: 24, creating: true}
	got, _ := m.Update(branchWorktreeCreatedMsg{err: errMock("git worktree add: boom")})
	gotModel := got.(model)
	if gotModel.creating {
		t.Error("creating = true, want false after create completes")
	}
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList", gotModel.phase)
	}
	if gotModel.listError == "" {
		t.Error("listError = empty, want error from msg")
	}
}

// TestToggleTagSwitchesGroup was removed in PR 3b Task 3 along with
// the `d` key handler. The cross-tag switch is no longer a TUI
// keybinding; tag filtering happens at launch via the -T flag
// (threaded through tui.Run in Task 4, exercised by
// TestLaunchFilteredUsesEligibleAndSlot in cmd/wt/launch_test.go) and
// the resulting picker list is covered by TestPhaseModelHonorsFilters
// in agent_picker_test.go.

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
	m := model{cfg: cfg, phase: phaseModel, width: 80, height: 24, agent: "claude", tag: "code", selectedPath: "/repo",
		models: singleModelList(cfg.Models[0])}

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
	m := model{cfg: cfg, phase: phaseOllamaWarn, width: 80, height: 24, agent: "claude", tag: "code", selectedPath: "/repo"}
	// Build a stub picker with the unavailable model as the highlighted
	// item, so ollamaWarn has a model to reference if proceedToLaunch
	// is ever called from this test.
	items := []list.Item{modelItem{model: cfg.Models[0]}}
	m.models = list.New(items, list.NewDefaultDelegate(), 78, 22)
	m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), 78, 22)

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("esc")})
	mm := newM.(model)

	if mm.phase != phaseModel {
		t.Fatalf("expected phaseModel after cancel, got %d", mm.phase)
	}
}

// TestOllamaWarnHasProceedAndCancelOnly asserts the ollama
// availability warning has two choices: proceed and cancel.
// The "skip" choice (which rotated to the next model) is gone
// because rotation is now implicit via RecordLaunch on Enter.
func TestOllamaWarnHasProceedAndCancelOnly(t *testing.T) {
	choices := buildOllamaChoices()
	if len(choices) != 2 {
		t.Fatalf("ollama choices = %d, want 2 (proceed + cancel)", len(choices))
	}
	// Verify the two surviving choices are the ones we expect, and
	// no entry references the removed "skip" wording.
	for _, c := range choices {
		oi, ok := c.(choiceItem)
		if !ok {
			t.Fatalf("choice is %T, want choiceItem", c)
		}
		if oi.choice == ollamaProceedChoice {
			if oi.title != "Proceed anyway" {
				t.Errorf("proceed title = %q, want %q", oi.title, "Proceed anyway")
			}
		} else if oi.choice == ollamaCancelChoice {
			if oi.title != "Cancel" {
				t.Errorf("cancel title = %q, want %q", oi.title, "Cancel")
			}
		} else {
			t.Errorf("unexpected choice %v (%q)", oi.choice, oi.title)
		}
		if strings.Contains(strings.ToLower(oi.title), "skip") {
			t.Errorf("choice title %q still mentions 'skip'", oi.title)
		}
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
	l := buildList(nil, "", "/tmp/repo", themes.Default, 80, 24)
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
	l := buildList(nil, "", "/tmp/repo", themes.Default, 80, 24)
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
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: entries}}
	m := model{
		width:            80,
		height:           24,
		pendingHighlight: "x",
	}
	got, _ := m.Update(entriesLoadedMsg{groups: groups, defaultBranch: "main"})
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
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: entries}}
	m := model{width: 80, height: 24}
	got, _ := m.Update(entriesLoadedMsg{groups: groups, defaultBranch: "main"})
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
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: entries}}
	m := model{phase: phaseList, ready: true, width: 80, height: 24}
	m.list = buildList(groups, "", "/repo", themes.Default, 78, 22)
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

// TestQDoesNotQuitWhileFilteringAgentList asserts that 'q' types into the
// agent+command picker's list filter rather than quitting the TUI. PR 2's
// phaseAgent screen reuses bubbles/list (filter opened with '/'), but
// isTyping() had no phaseAgent case, so a query like "quality" killed the
// app on the first 'q' while the worktree-list screen guarded it fine.
func TestQDoesNotQuitWhileFilteringAgentList(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	m := model{phase: phaseAgent, cfg: cfg, width: 80, height: 24}
	m.agentList = list.New(buildAgentList(cfg), list.NewDefaultDelegate(), 78, 22)
	// Open the filter exactly like a user pressing '/'.
	m.agentList, _ = m.agentList.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.agentList.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.agentList.FilterState())
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(gotModel.agentList.FilterInput.Value(), "q") {
		t.Errorf("filter input = %q, want to contain q (quit hijacked the key)", gotModel.agentList.FilterInput.Value())
	}
}

// TestNNotHijackedWhileFiltering asserts that 'n' types into the list
// filter instead of opening the new-worktree prompt. Before the fix,
// the 'n' keypress was intercepted before bubbles/list saw it, so no
// filter query could ever contain 'n' — branches with 'n' in their
// name became unfilterable by typing.
func TestNNotHijackedWhileFiltering(t *testing.T) {
	entries := []worktree.Entry{{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"}}
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: entries}}
	m := model{phase: phaseList, ready: true, width: 80, height: 24}
	m.list = buildList(groups, "", "/repo", themes.Default, 78, 22)
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
		groups: []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"}}}},
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
