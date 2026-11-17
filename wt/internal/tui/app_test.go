package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// testConfig returns a Config with one agent (claude, supporting the
// ollama provider) and a two-model code tag group plus one design model.
// Tests across the picker, rotation, and launch need a real catalog so
// the agent filter, tag filter, and rotation all have something to
// operate on.
func testConfig() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
		},
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/gemma4:14b", ModelName: "gemma4:14b", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/gemma4:design", ModelName: "gemma4:design", ProviderID: "ollama", Tags: []string{"design"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	return cfg
}

// TestInitLoadsEntries asserts Init starts background worktree enumeration.
// Without this command, the TUI would sit forever at the loading screen.
func TestInitLoadsEntries(t *testing.T) {
	m := model{status: "loading worktrees..."}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd; expected loadPickerCmd")
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

// TestEscFromAgentPickerClearsStatus verifies that pressing Esc from
// phaseAgent (after e.g. an invalid pinned-model error set m.status) clears
// the status before transitioning back to phaseList. Without this, the
// stale error text follows the user back to the worktree list and there is
// no way to dismiss it — a stranded user has only Ctrl+C.
func TestEscFromAgentPickerClearsStatus(t *testing.T) {
	m := model{phase: phaseAgent, status: "model \"ollama/missing\" is not in the eligible list for agent \"claude\""}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseList {
		t.Errorf("phase = %d, want phaseList (%d)", gotModel.phase, phaseList)
	}
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty (Esc must clear stale errors before transitioning)", gotModel.status)
	}
}

// TestInvalidPinFromProceedFromSelectedPathRoutesToAgentPicker verifies
// that when --agent is pinned via CLI (-A) and -M is also pinned but the
// model is not in the agent's eligible list, proceedFromSelectedPath routes
// to phaseAgent rather than leaving the user stranded on phaseList. The
// pre-fix behavior was: m.phase stayed at whatever the caller had (phaseList
// after a worktree pick), m.status showed the error, and the user had no
// way to recover except Ctrl+C.
func TestInvalidPinFromProceedFromSelectedPathRoutesToAgentPicker(t *testing.T) {
	requireBinary(t, "claude")
	tempStateDir(t)
	cfg := testConfig()
	m := withServer(t, model{cfg: cfg, initialAgent: "claude", pinnedModel: "ollama/missing", phase: phaseList, width: 80, height: 24})
	got, cmd := m.proceedFromSelectedPath()
	got, _ = settleCatalog(t, got, cmd)
	if got.phase != phaseAgent {
		t.Errorf("phase = %d, want phaseAgent (%d) so the user can pick a different agent", got.phase, phaseAgent)
	}
	if !strings.Contains(got.status, `unknown model "ollama/missing"`) {
		t.Errorf("status = %q, want the model server's unknown-model refusal", got.status)
	}
	if got.pinnedModel != "" {
		t.Errorf("pinnedModel = %q, want empty (clear the bad pin so re-entry validates fresh)", got.pinnedModel)
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

// TestRowsLoadedMsgBuildsTheList asserts a picker load builds the row list in
// launch mode and marks the model ready: "+ New worktree…" first, then the
// report's rows. Without this the TUI would never leave the loading screen.
func TestRowsLoadedMsgBuildsTheList(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	if !m.rows.launch() {
		t.Error("the picker's list is not in launch mode")
	}
	if !m.rows.items[0].newWorktree {
		t.Errorf("items[0] = %+v, want the new-worktree item", m.rows.items[0])
	}
	if m.rows.rows != len(pickerFixture().Rows) {
		t.Errorf("rows = %d, want %d", m.rows.rows, len(pickerFixture().Rows))
	}
}

// TestViewReady asserts the rendered picker shows the list's title line and
// its rows once loaded.
func TestViewReady(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	view := m.View()
	for _, want := range []string{"enter launch", "WORKTREE", "loose"} {
		if !strings.Contains(view, want) {
			t.Errorf("View missing %q:\n%s", want, view)
		}
	}
}

// TestRowsLoadedCursorOnRootWithNoDefaultBranch is the regression guard for
// a repository whose default branch cannot be determined: the cursor still
// lands on the checkout the user stands in (the root here), not on
// "+ New worktree…", so a bare Enter relaunches there.
func TestRowsLoadedCursorOnRootWithNoDefaultBranch(t *testing.T) {
	rep := pickerFixture()
	rep.Base = cleanup.Base{}
	rep.Rows = rep.Rows[:1]
	rep.Rows[0].Branch, rep.Rows[0].Name = "work", "work"
	m := pickerReady(t, rep)
	if row, ok := m.rows.selectedRow(); !ok || row.Name != "work" {
		t.Errorf("cursor on %+v (ok %v), want the root on work", row, ok)
	}
}

// TestEnterSelectsEntry asserts that pressing Enter on a picker row emits a
// selectedEntryMsg carrying that row's worktree. This is the primary
// selection affordance for the worktree picker.
func TestEnterSelectsEntry(t *testing.T) {
	m := pickerCursorTo(t, pickerReady(t, pickerFixture()), "other")
	_, sel := pickerEnter(t, m)
	if sel.entry.Branch != "other" || sel.entry.Path != "/repo/.worktrees/other" {
		t.Errorf("selected %+v, want the other worktree", sel.entry)
	}
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

// TestSelectedEntryMsgBareBranchPlansTheCreation asserts that a bare-branch
// pick binds the creation it owes — a read-only plan whose path the agent
// will launch in, rather than wt's CWD (cmd.Dir="") — and proceeds to the
// agent picker, creating nothing yet: the commit point's launch gate makes
// the worktree only once the person continues, so a declined gate creates
// nothing.
func TestSelectedEntryMsgBareBranchPlansTheCreation(t *testing.T) {
	tempStateDir(t) // isolate XDG_CONFIG_HOME so rotation state is fresh
	root := t.TempDir()
	gitInit(t, root)
	if out, err := exec.Command("git", "-C", root, "branch", "feature").CombinedOutput(); err != nil {
		t.Fatalf("git branch feature: %v\n%s", err, out)
	}
	m := model{cfg: testConfig(), width: 80, height: 24, repoRoot: root}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.creation == nil {
		t.Fatal("creation = nil; the pick owes a worktree and must carry its plan")
	}
	if gotModel.creation.name != "feature" {
		t.Errorf("creation.name = %q, want feature", gotModel.creation.name)
	}
	if gotModel.selectedPath != gotModel.creation.plan.Path() {
		t.Errorf("selectedPath = %q, want the plan path %q", gotModel.selectedPath, gotModel.creation.plan.Path())
	}
	if gotModel.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent", gotModel.phase)
	}
	if _, err := os.Stat(gotModel.selectedPath); err == nil {
		t.Errorf("%s exists at pick time; the creation belongs to the commit point", gotModel.selectedPath)
	}
}

// TestSelectedEntryMsgBareBranchResolveErrorReturnsToList asserts that a
// bare-branch pick whose plan cannot be resolved surfaces the error and
// returns to the list. A silent failure would leave the user on a stale
// screen with no feedback.
func TestSelectedEntryMsgBareBranchResolveErrorReturnsToList(t *testing.T) {
	m := model{phase: phaseList, width: 80, height: 24, repoRoot: t.TempDir()} // not a git repo
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList", gotModel.phase)
	}
	if gotModel.listError == "" {
		t.Error("listError = empty, want the resolve error")
	}
}

// TestToggleTagSwitchesGroup was removed in PR 3b Task 3 along with
// the `d` key handler. The cross-tag switch is no longer a TUI
// keybinding; tag filtering happens at launch via the -T flag
// (threaded through tui.Run in Task 4, exercised by
// TestLaunchFilteredUsesEligibleAndSlot in cmd/wt-session/launch_test.go) and
// the resulting picker list is covered by TestPhaseModelHonorsFilters
// in agent_picker_test.go.

// (TestViewModelPhase was rewritten in agent_model_test.go using the
// new picker-based View.)

// TestNKeyOpensNewWorktreePhase asserts pressing `n` on the picker
// transitions to phaseNewWorktree. Without this wiring, the keyboard
// shortcut is dead and only the "+ New worktree…" item is reachable.
func TestNKeyOpensNewWorktreePhase(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = drainCmds(t, m, cmd)
	if m.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", m.phase)
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

// TestEnterOnSentinelOpensNewWorktreePhase asserts Enter on
// "+ New worktree…" transitions to phaseNewWorktree. The item must be
// Enter-able, not just `n`-able.
func TestEnterOnSentinelOpensNewWorktreePhase(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m.rows.cursor = 0
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", m.phase)
	}
}

// TestEnterOnSentinelIgnoredWhileLoading asserts Enter does nothing before
// the picker is ready. Consistent with the `n` keypress guard.
func TestEnterOnSentinelIgnoredWhileLoading(t *testing.T) {
	m := model{phase: phaseList, ready: false, width: 80, height: 24}
	got, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.phase == phaseNewWorktree {
		t.Errorf("phase = phaseNewWorktree, want phaseList (loading not complete)")
	}
	if cmd != nil {
		t.Errorf("Enter while loading returned a command")
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

// (The typed-name Enter's contract lives in new_worktree_test.go:
// TestTypedNameDefersCreationToTheCommitPoint and its neighbours. The old
// ensureNewWorktreeCmd/newWorktreeCreatedMsg machinery died with the
// deferral: the commit point creates now.)

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

// TestQDoesNotQuitWhileFiltering asserts that 'q' types into the picker's
// filter rather than quitting. The filter is opened with '/'; a query like
// "quality" would otherwise quit the TUI on the first 'q'.
func TestQDoesNotQuitWhileFiltering(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.rows.filtering {
		t.Fatal("'/' did not open the filter")
	}
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		t.Errorf("'q' while filtering returned a command (quit?)")
	}
	if !strings.Contains(m.rows.filter, "q") {
		t.Errorf("filter = %q, want to contain q (quit hijacked the key)", m.rows.filter)
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

// TestQDoesNotQuitWhileFilteringModelList asserts that 'q' types into the
// model picker's filter query instead of quitting the TUI. isTyping() had no
// phaseModel case (only phaseList and phaseAgent), so a query like "qwen"
// quit the app on the first 'q' — even though the model picker's '/' filter
// is enabled by default and advertised in the wrap-around/filter-key tests
// above. This is the isTyping() half of the PR-review finding covering
// mishandled filter keystrokes in the model picker.
func TestQDoesNotQuitWhileFilteringModelList(t *testing.T) {
	// Isolate the usage store: buildTable scans it for the usage
	// counts.
	stubUsageStore(t)
	models := []config.Model{
		{ID: "ollama/qwen3.8:27b", ModelName: "qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
		{ID: "ollama/other", ModelName: "other", ProviderID: "ollama"},
	}
	m := model{phase: phaseModel, width: 80, height: 24}
	m.models = compactModelList(t, models)
	// Open the filter exactly like a user pressing '/'.
	m.models, _ = m.models.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.models.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.models.FilterState())
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(gotModel.models.FilterInput.Value(), "q") {
		t.Errorf("filter input = %q, want to contain q (quit hijacked the key)", gotModel.models.FilterInput.Value())
	}
}

// TestEnterWhileFilteringAppliesFilterNotLaunch asserts that Enter, while
// the model picker's filter input is focused (FilterState == Filtering),
// is handled by bubbles/list's own AcceptWhileFiltering binding rather than
// being intercepted by the phaseModel Enter case, which runs the ollama
// check and launches the highlighted model. Before the fix, the top-level
// "enter" case switched on m.phase and ran the launch path unconditionally,
// so a keystroke meant to commit the filter query instead launched (and,
// via launchAndRecord, could commit rotation/usage state for) whatever
// model happened to be highlighted underneath the filter overlay.
func TestEnterWhileFilteringAppliesFilterNotLaunch(t *testing.T) {
	stubUsageStore(t) // buildTable scans the usage store
	models := []config.Model{
		{ID: "ollama/qwen3.8:27b", ModelName: "qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
		{ID: "ollama/other", ModelName: "other", ProviderID: "ollama"},
	}
	m := model{cfg: testConfig(), phase: phaseModel, width: 80, height: 24}
	m.models = compactModelList(t, models)
	m.models, _ = m.models.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.models.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.models.FilterState())
	}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel (Enter while filtering must not advance past the picker)", gotModel.phase)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd (no launch) for Enter while filtering, got %v", cmd)
	}
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty (Enter while filtering must not run the ollama check)", gotModel.status)
	}
}

// TestModelPickerFilterReceivesJKKeys verifies that "j"/"k" keystrokes typed
// into the model picker's filter query reach the filter text input instead
// of being intercepted as wrap-around navigation. bubbles/list's Filter key
// calls GoToStart(), which resets the cursor to index 0 every time
// filtering begins, and the wrap-around handler treated index 0 as
// "top of list, wrap on up" for any "up"/"k" keypress — matching "k"/"j"
// before bubbles/list's own filter input ever saw them. A query like
// "kimi" would silently drop its leading "k". Flagged in PR #82 review.
func TestModelPickerFilterReceivesJKKeys(t *testing.T) {
	stubUsageStore(t) // buildTable scans the usage store
	models := []config.Model{
		{ID: "ollama/kimi", ModelName: "kimi", ProviderID: "ollama"},
		{ID: "ollama/other", ModelName: "other", ProviderID: "ollama"},
	}
	m := model{phase: phaseModel, width: 80, height: 24}
	m.models = compactModelList(t, models)

	// Open the filter exactly like a user pressing '/'.
	m.models, _ = m.models.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.models.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.models.FilterState())
	}
	if idx := m.models.Index(); idx != 0 {
		t.Fatalf("cursor index = %d, want 0 (bubbles/list resets to start on filter open)", idx)
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(gotModel.models.FilterInput.Value(), "k") {
		t.Errorf("filter input = %q, want to contain k (wrap-around hijacked the key)", gotModel.models.FilterInput.Value())
	}
}

// TestModelPickerWrapsFromTopToBottom verifies pressing up/k at the first
// model wraps the cursor to the last model, since bubbles/list does not
// wrap by default. Locks down the wrap-around behavior added in PR #82,
// which previously had no direct test (flagged in review).
func TestModelPickerWrapsFromTopToBottom(t *testing.T) {
	stubUsageStore(t) // buildTable scans the usage store
	models := []config.Model{
		{ID: "ollama/a", ModelName: "a", ProviderID: "ollama"},
		{ID: "ollama/b", ModelName: "b", ProviderID: "ollama"},
		{ID: "ollama/c", ModelName: "c", ProviderID: "ollama"},
	}
	m := model{phase: phaseModel, width: 80, height: 24}
	// Compact production layout: a(0), b(1), c(2) — no dividers.
	m.models = compactModelList(t, models)
	m.models.Select(0) // first model row

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if got := gotModel.models.Index(); got != 2 {
		t.Errorf("index after up-wrap = %d, want 2 (last model row)", got)
	}
}

// TestModelPickerWrapsFromBottomToTop verifies pressing down/j at the last
// model wraps the cursor back to the first model. Locks down the
// wrap-around behavior added in PR #82, which previously had no direct
// test (flagged in review).
func TestModelPickerWrapsFromBottomToTop(t *testing.T) {
	stubUsageStore(t) // buildTable scans the usage store
	models := []config.Model{
		{ID: "ollama/a", ModelName: "a", ProviderID: "ollama"},
		{ID: "ollama/b", ModelName: "b", ProviderID: "ollama"},
		{ID: "ollama/c", ModelName: "c", ProviderID: "ollama"},
	}
	m := model{phase: phaseModel, width: 80, height: 24}
	// Compact production layout: a(0), b(1), c(2) — no dividers.
	m.models = compactModelList(t, models)
	m.models.Select(2) // last model row

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if got := gotModel.models.Index(); got != 0 {
		t.Errorf("index after down-wrap = %d, want 0 (first model row)", got)
	}
}

// TestNNotHijackedWhileFiltering asserts that 'n' types into the picker's
// filter instead of opening the new-worktree prompt, so branches with 'n' in
// their name stay filterable by typing.
func TestNNotHijackedWhileFiltering(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = drainCmds(t, m, cmd)
	if m.phase == phaseNewWorktree {
		t.Error("phase = phaseNewWorktree, want phaseList (n hijacked the filter)")
	}
	if !strings.Contains(m.rows.filter, "n") {
		t.Errorf("filter = %q, want to contain n", m.rows.filter)
	}
}

// TestRePickClearsAnAbandonedCreation checks every selection rebinds the
// launch: a later pick of an existing worktree must clear the creation plan
// an earlier pick left behind. Without this, esc back to the list and a
// re-pick still drove the launch — and at the commit point created — the
// abandoned pick's worktree. The typed name's plan goes with it: it is owed
// the same commit-point Ensure, and an abandoned one must never run it for a
// later pick.
func TestRePickClearsAnAbandonedCreation(t *testing.T) {
	hooks, _ := stubShellLaunch()
	m := model{
		hooks:        hooks,
		cfg:          &config.Config{},
		phase:        phaseList,
		width:        80,
		height:       24,
		initialAgent: "opencode",
		creation:     &launchCreation{repoRoot: "/repo", name: "abandoned"},
		typedPlan:    &worktree.Plan{},
	}
	m, _ = updateMsg(m, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeWorktree, Branch: "kept", Path: "/repo/.worktrees/kept"}})
	if m.creation != nil {
		t.Errorf("creation = %+v, want nil after picking an existing worktree", m.creation)
	}
	if m.typedPlan != nil {
		t.Errorf("typedPlan = %+v, want nil after picking an existing worktree", m.typedPlan)
	}
	if m.selectedPath != "/repo/.worktrees/kept" {
		t.Errorf("selectedPath = %q, want the re-pick's path", m.selectedPath)
	}
}

// TestSecondEnterAtTheCommitPointIsIgnored checks the commit point serializes
// the launch in flight: a second Enter pressed before the terminal is released
// must not start a second run command — with it a second creation binding, a
// second gate and a second agent. A session can still commit again after the
// slot applier's failure returns it to the picker (TestRetryAfterASlotAbort…);
// the quit path holds the guard to process exit (TestLaunchDoneKeeps…). The
// old m.creating guard that serialized selections went with the create flow it
// guarded.
func TestSecondEnterAtTheCommitPointIsIgnored(t *testing.T) {
	m, first, _ := pickRow(t, "/repo/.worktrees/feat")
	if first == nil {
		t.Fatal("the first Enter did not start a run command")
	}
	_, second := updateMsg(m, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo/.worktrees/feat"}})
	if second != nil {
		t.Error("a second Enter started a second run command; the commit point takes one launch")
	}
}

// TestRetryAfterASlotAbortDispatchesARunCommand checks the commit-point guard
// is an in-flight guard, not a latch: the slot applier's failure (slotAbortMsg;
// the flow returns to the model picker) releases it, so the deliberate retry
// can commit again. The guard releases on exactly that one outcome — the quit
// path holds it to process exit (TestLaunchDoneKeepsTheCommitGuardLatched). A
// guard that never released left the session silently dead — every later Enter
// dispatched nothing.
func TestRetryAfterASlotAbortDispatchesARunCommand(t *testing.T) {
	m, first, _ := pickRow(t, "/repo/.worktrees/feat")
	if first == nil {
		t.Fatal("the first Enter did not start a run command")
	}
	// The abort returns to the model picker, whose list the fixture must
	// carry for Update's final fit (the real picker always has one).
	m.models = compactModelList(t, nil)
	// The run command's outcome came back as a slotAbortMsg and returned to
	// the picker; the retry's Enter must start a run command of its own.
	m, _ = updateMsg(m, slotAbortMsg{err: errMock("no slot-profile applier")})
	if _, retry := updateMsg(m, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo/.worktrees/feat"}}); retry == nil {
		t.Error("the retry's Enter dispatched nothing; the guard latched instead of guarding the launch in flight")
	}
}

// TestLaunchDoneKeepsTheCommitGuardLatched checks the two run-command outcomes
// treat the commit-point guard differently on purpose: the slot applier's
// abort is the one retry path and releases it
// (TestRetryAfterASlotAbortDispatchesARunCommand), while the agent's own exit
// quits and keeps the guard latched through teardown. It matters because the
// run command's closure hands the terminal back (input restarts at
// RestoreTerminal) just before launchDoneMsg is processed, so a KeyMsg already
// queued is handled before the QuitMsg and would commit a duplicate run
// command — a second agent, a second rotation/usage record — against a
// released guard. What this pin observes: the guard's state after each
// outcome, and that a selection fed during teardown dispatches nothing. What
// it cannot observe: the queued-key race itself — that ordering is
// bubbletea's, around tea.Quit, and is not reachable from an Update-level
// test; the pin holds the precondition that makes the race harmless.
func TestLaunchDoneKeepsTheCommitGuardLatched(t *testing.T) {
	m, first, _ := pickRow(t, "/repo/.worktrees/feat")
	if first == nil {
		t.Fatal("the first Enter did not start a run command")
	}
	// The agent exited and the program is quitting: the guard must still be
	// held when the quit is processed.
	done, _ := updateMsg(m, launchDoneMsg{err: errMock("exit status 1")})
	if !done.committed {
		t.Error("launchDoneMsg released the guard; a key queued at RestoreTerminal can commit a duplicate run command before the QuitMsg")
	}
	// The observable consequence: a selection already queued when the quit is
	// processed must dispatch no run command.
	if _, queued := updateMsg(done, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo/.worktrees/feat"}}); queued != nil {
		t.Error("a selection fed during teardown dispatched a run command; the quit path holds the guard")
	}
	// Contrast: the slot applier's abort returns to the model picker and
	// releases the guard so the retry's Enter can commit
	// (TestRetryAfterASlotAbortDispatchesARunCommand drives the dispatch).
	m.models = compactModelList(t, nil)
	abort, _ := updateMsg(m, slotAbortMsg{err: errMock("no slot-profile applier")})
	if abort.committed {
		t.Error("slotAbortMsg kept the guard; the return to the model picker must be able to commit a retry")
	}
}

// TestReloadErrorVisibleAboveList asserts that a reload failure after
// a successful create renders above the list. Before the fix the error
// was stored in m.status, which View only shows while !m.ready, so the
// user saw a stale list with no explanation after the reload failed.
func TestReloadErrorVisibleAboveList(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, _ = updateMsg(m, rowsLoadedMsg{err: errMock("git worktree list: boom")})
	if m.listError == "" {
		t.Fatal("listError = empty, want reload error")
	}
	if view := m.View(); !strings.Contains(view, "boom") {
		t.Errorf("View missing reload error, got:\n%s", view)
	}
}

// pickerOpenOn opens the picker on r's repository through a rows load that
// carries a counting fetch hook (the picker's `F` refresh runs exactly that
// hook, reporting fetchErr — nil: the fetch succeeds), and returns the ready
// model with the hook's counter.
func pickerOpenOn(t *testing.T, r *rmRepo, fetchErr error) (model, func() int) {
	t.Helper()
	in := pickerInputs(r.dir)
	in.OwnRepo = "o/r"
	in.PRList = func(string, string) ([]byte, error) { return []byte("[]"), nil }
	in.PRBaseList = func(string, string) ([]byte, error) { return []byte("[]"), nil }
	fetches := 0
	in.FetchRemotes = func(string) error { fetches++; return fetchErr }
	m := model{width: 120, height: 50, theme: themes.Default, cfg: &config.Config{}}
	m, _ = updateMsg(m, loadRowsCmd(r.dir, nil, in, false)())
	if !m.ready {
		t.Fatalf("not ready after the rows load; status %q", m.status)
	}
	return m, func() int { return fetches }
}

// pickerRowListed reports whether the picker's list carries a row named name.
func pickerRowListed(m model, name string) bool {
	for _, it := range m.rows.items {
		if !it.newWorktree && it.row.Name == name {
			return true
		}
	}
	return false
}

// TestPickerOpenDoesNotFetch: opening the picker runs no fetch and leaves
// the repository's fetch stamp alone — fetching is the user's `F` now (no
// automatic behaviors). The hook counts any fetch a rows load would run;
// the stamp check then walks the whole open path (loadPickerCmd) with the
// real fetch function, the one that used to run on every open and write
// FETCH_HEAD. A fresh clone has no fetch stamp at all, so the check is
// about the open and not about the setup.
func TestPickerOpenDoesNotFetch(t *testing.T) {
	r := newRmRepo(t)
	_, fetches := pickerOpenOn(t, r, nil)
	if fetches() != 0 {
		t.Errorf("the rows load fetched %d times; opening the list must fetch nothing", fetches())
	}
	old := findRepoRoot
	findRepoRoot = func() (string, error) { return r.dir, nil }
	t.Cleanup(func() { findRepoRoot = old })
	if _, ok := loadPickerCmd()().(rowsLoadedMsg); !ok {
		t.Fatal("the open load did not answer rowsLoadedMsg")
	}
	if _, ok := worktree.LastFetched(r.dir); ok {
		t.Error("opening the picker wrote a fetch stamp; it fetches nothing")
	}
}

// TestPickerFKeyRefreshes: `F` in the picker fetches (exactly once, through
// the rows load's fetch hook) and re-gathers the rows, so a branch born
// after the open shows up. It is the list's only refresh, since opening
// fetches nothing.
func TestPickerFKeyRefreshes(t *testing.T) {
	r := newRmRepo(t)
	m, fetches := pickerOpenOn(t, r, nil)
	r.loose("afterwards") // born after the load: only a re-gather can show it
	m, cmd := updateMsg(m, keyRunes("F"))
	if cmd == nil {
		t.Fatal("F dispatched no refresh")
	}
	m = drainCmds(t, m, cmd)
	if fetches() != 1 {
		t.Errorf("F fetched %d times, want 1", fetches())
	}
	if !m.ready || m.listError != "" {
		t.Fatalf("ready=%v listError=%q after F; want the refreshed list", m.ready, m.listError)
	}
	if !pickerRowListed(m, "afterwards") {
		t.Errorf("the refreshed list lacks afterwards:\n%s", m.View())
	}
}

// TestPickerFKeyRefreshKeepsFilterCursorScroll: a `F` refresh's reload is a
// report swap like the PR fill's — it keeps what the user was doing: the
// typed filter, the highlighted row and the scroll position survive
// (withReport, the preservation every other swap already had). The
// highlighted-row claim pins the BY-KEY cursor restore specifically: the
// fixture has two rows the filter keeps visible, and the cursor is moved to
// the one a re-seat would not pick (settleCursor falls back to the filter's
// first visible row), so a reload that lost the cursor lands there and
// fails. Both of the refresh's outcomes count: the fetch succeeding, and the
// fetch failing while the re-gather still succeeds — the failure is the
// header's line, and neither may throw a review back to the top with its
// filter dropped.
func TestPickerFKeyRefreshKeepsFilterCursorScroll(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fetchErr error
	}{
		{"fetch ok", nil},
		{"fetch failed", errMock("no route to origin")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRmRepo(t)
			// Two rows match "after" below: the cursor claim needs two
			// visible rows, or a re-seat passes it.
			r.loose("aftermath")
			r.loose("afterwards")
			m, _ := pickerOpenOn(t, r, tc.fetchErr)
			// The user filters to the two rows and keeps the query (enter).
			m, _ = updateMsg(m, keyRunes("/"))
			m, _ = updateMsg(m, keyRunes("after"))
			m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
			// Move the cursor off the row settleCursor would re-seat (the
			// filter's first visible row) and onto the other match.
			seated, ok := m.rows.selectedRow()
			if !ok {
				t.Fatal("no highlighted row under the filter")
			}
			m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyDown})
			want, ok := m.rows.selectedRow()
			if !ok || want.Name == seated.Name {
				t.Fatalf("the filter left one visible row (cursor on %v); the cursor claim needs two", seated)
			}
			m.rows.offset = 1
			m, cmd := updateMsg(m, keyRunes("F"))
			if cmd == nil {
				t.Fatal("F dispatched no refresh")
			}
			m = drainCmds(t, m, cmd)
			if m.rows.filter != "after" || m.rows.filtering {
				t.Errorf("filter = %q filtering = %v after F; the reload keeps the query", m.rows.filter, m.rows.filtering)
			}
			if row, ok := m.rows.selectedRow(); !ok || row.Name != want.Name {
				t.Errorf("cursor = %+v ok=%v after F; the reload keeps the highlighted row (%q, restored by key)", row, ok, want.Name)
			}
			if m.rows.offset != 1 {
				t.Errorf("offset = %d after F; the reload keeps the scroll", m.rows.offset)
			}
		})
	}
}

// TestPickerFKeyIgnoredWhileTyping: while the filter query is being typed,
// `F` is a letter of that query, never a refresh (the same rule as q, n and
// x there). Without it a filter for anything with a capital F would fetch
// behind the user's back.
func TestPickerFKeyIgnoredWhileTyping(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, _ = updateMsg(m, keyRunes("/"))
	m, cmd := updateMsg(m, keyRunes("F"))
	if cmd != nil {
		t.Error("F while typing dispatched a command; it is filter text")
	}
	if !m.rows.filtering || m.rows.filter != "F" {
		t.Errorf("filtering=%v filter=%q, want the query to read F", m.rows.filtering, m.rows.filter)
	}
}

// TestPickerFKeyIgnoredWhileRefreshing: a second `F` while the first
// refresh's load is still in flight starts nothing — one fetch per request,
// never a stampede on key repeat.
func TestPickerFKeyIgnoredWhileRefreshing(t *testing.T) {
	r := newRmRepo(t)
	m, fetches := pickerOpenOn(t, r, nil)
	// The first F starts the refresh; its load command is kept unrun (the
	// fetch is in flight).
	m, cmd := updateMsg(m, keyRunes("F"))
	if cmd == nil {
		t.Fatal("F started no refresh")
	}
	m, load := updateMsg(m, cmd())
	if load == nil {
		t.Fatal("the refresh message started no rows load")
	}
	if fetches() != 0 {
		t.Fatal("the rows load fetched before its command ran")
	}
	// The second F, mid-refresh: its message is dropped, no second load.
	m, cmd2 := updateMsg(m, keyRunes("F"))
	if cmd2 != nil {
		if _, extra := updateMsg(m, cmd2()); extra != nil {
			t.Fatal("F while refreshing started a second rows load")
		}
	}
	m = drainCmds(t, m, load)
	if fetches() != 1 {
		t.Errorf("fetches = %d, want 1 (the second F is ignored while refreshing)", fetches())
	}
}

// TestPickerRefreshFailureShowsNote: a refresh that cannot re-read the
// repository leaves the list as it was and says why in one line above it —
// a failed refresh must never empty the picker or bury the rows it had.
func TestPickerRefreshFailureShowsNote(t *testing.T) {
	m := model{width: 100, height: 40, theme: themes.Default, cfg: &config.Config{}}
	m, _ = updateMsg(m, rowsLoadedMsg{report: pickerFixture(), repoRoot: t.TempDir(),
		in: cleanup.GatherInputs{FetchRemotes: func(string) error { return nil }}})
	if !m.ready {
		t.Fatalf("not ready after the rows load; status %q", m.status)
	}
	m, cmd := updateMsg(m, keyRunes("F"))
	m = drainCmds(t, m, cmd)
	if m.listError == "" {
		t.Fatal("no note after a refresh that could not re-read the repository")
	}
	if strings.Contains(m.listError, "\n") {
		t.Errorf("the note is not one line: %q", m.listError)
	}
	if !m.ready {
		t.Fatal("the list went away on a failed refresh")
	}
	if row, ok := m.rows.selectedRow(); !ok || row.Name != "deep" {
		t.Errorf("cursor = %+v ok=%v; the old list keeps its rows and its cursor", row, ok)
	}
}

// TestPickerHeaderShowsLastRefreshed: the header carries the list's refresh
// note — "not refreshed here" while the repository has no fetch stamp (a
// fresh clone has none), and "refreshed 3m ago" once a fetch ran there. The
// stamp is FETCH_HEAD's mtime (LastFetched), so the test ages it three
// minutes to make the rendering deterministic without sleeping.
func TestPickerHeaderShowsLastRefreshed(t *testing.T) {
	empty := func(string, string) ([]byte, error) { return []byte("[]"), nil }
	r := newRmRepo(t)
	m, _ := pickerLoad(t, r.dir, empty)
	if note := m.rows.headerBlock(); !strings.Contains(note, "not refreshed here") {
		t.Errorf("a fresh clone's header lacks the note:\n%s", note)
	}
	if err := worktree.FetchRemotes(r.dir); err != nil {
		t.Fatal(err)
	}
	stamp := r.git("rev-parse", "--git-path", "FETCH_HEAD")
	if !filepath.IsAbs(stamp) {
		stamp = filepath.Join(r.dir, stamp)
	}
	ago := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(stamp, ago, ago); err != nil {
		t.Fatal(err)
	}
	m, _ = pickerLoad(t, r.dir, empty)
	if note := m.rows.headerBlock(); !strings.Contains(note, "refreshed 3m ago") {
		t.Errorf("header note is not \"refreshed 3m ago\" in:\n%s", note)
	}
}

// errMock is a small helper to make error literals readable in tests.
type errMock string

func (e errMock) Error() string { return string(e) }

// TestPrePathInitSkipsWorktreePicker asserts that when a worktree path is
// pre-resolved (Run's prePath, from -W/--cwd/outside-repo), Init skips
// worktree enumeration and proceeds straight to the agent/command picker
// when no agent is pinned. Without this, `wt -W feat` (no -A) could not
// prompt for an agent.
func TestPrePathInitSkipsWorktreePicker(t *testing.T) {
	m := model{prePath: "/repo/.worktrees/feat", cfg: testConfig()}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd")
	}
	msg := cmd()
	sel, ok := msg.(selectedEntryMsg)
	if !ok {
		t.Fatalf("Init cmd returned %T, want selectedEntryMsg", msg)
	}
	if sel.entry.Path != "/repo/.worktrees/feat" {
		t.Errorf("path = %q, want prePath", sel.entry.Path)
	}
	// Feeding it proceeds to the agent picker (unpinned).
	got, _ := m.Update(sel)
	nm := got.(model)
	if nm.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent (skip worktree picker, show agent picker)", nm.phase)
	}
	if len(nm.agentList.Items()) == 0 {
		t.Error("agentList empty; expected agent+command picker rows")
	}
}

// TestPrePathInitPinnedGoesToModel asserts the pre-path shortcut still honors
// a pinned --agent: the worktree picker is skipped and the model phase opens
// directly.
func TestPrePathInitPinnedGoesToModel(t *testing.T) {
	// Isolate usage (seam) and rotation (XDG) state: the pinned-agent path
	// loads the catalog, which scans usage.jsonl for the list build and
	// reads rotation.state for cursor positioning.
	tempStateDir(t)
	stubUsageStore(t)
	requireBinary(t, "claude")
	m := withServer(t, model{prePath: "/repo/.worktrees/feat", cfg: testConfig(), initialAgent: "claude"})
	msg := m.Init()()
	sel, ok := msg.(selectedEntryMsg)
	if !ok {
		t.Fatalf("Init cmd returned %T, want selectedEntryMsg", msg)
	}
	got, cmd := m.Update(sel)
	nm, _ := settleCatalog(t, got.(model), cmd)
	if nm.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel (pinned agent with prePath)", nm.phase)
	}
}

// TestPrePathInitPinnedCommandLaunches asserts that `wt -W feat -A shell`
// (prePath + command agent) routes through launchCommand instead of the model
// phase. Without this test, a regression in proceedFromSelectedPath's command
// path could silently route shell through enterModelPhase — failing because
// shell has no models — or skip launchCommand entirely. The assertion is
// phase- and agent-based (not on the launch outcome) so the test is robust to
// bash being absent from PATH: when bash is missing, m.status holds an error
// and runAndWaitCmd is never returned, but the phase/agent transitions still
// prove the right code path was taken.
func TestPrePathInitPinnedCommandLaunches(t *testing.T) {
	m := model{prePath: "/repo/.worktrees/feat", cfg: testConfig(), initialAgent: "shell"}
	msg := m.Init()()
	sel, ok := msg.(selectedEntryMsg)
	if !ok {
		t.Fatalf("Init cmd returned %T, want selectedEntryMsg", msg)
	}
	got, _ := m.Update(sel)
	nm := got.(model)
	if nm.agent != "shell" {
		t.Errorf("agent = %q, want %q (pinned command agent must be honored)", nm.agent, "shell")
	}
	if nm.phase == phaseModel {
		t.Errorf("phase = phaseModel, want command path (shell has no models)")
	}
	// selectedPath must be threaded to launchCommand even when the path is
	// pre-resolved by -W. Verify it's set; the launch itself may or may not
	// succeed depending on bash being on PATH, but the path argument is
	// part of the launchCommand contract and must be populated.
	if nm.selectedPath != "/repo/.worktrees/feat" {
		t.Errorf("selectedPath = %q, want prePath (%q)", nm.selectedPath, "/repo/.worktrees/feat")
	}
}

// TestPrintPendingSummaryAndSurveyOrder verifies Run()'s post-p.Run() logic
// invokes the survey and prints the summary and pricing notice, and clears
// both package vars so a later launch in the same process doesn't replay
// stale state. Full ordering is pinned by TestPostExitOrder.
func TestPrintPendingSummaryAndSurveyOrder(t *testing.T) {
	prevSummary, prevSurvey, prevRunSurvey := pendingSummary, pendingSurveyState, runSurvey
	t.Cleanup(func() {
		pendingSummary = prevSummary
		pendingSurveyState = prevSurvey
		runSurvey = prevRunSurvey
	})

	pendingSummary = "wt: claude · claude/sonnet · 1s"
	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "claude/sonnet"}}

	var calledWith pendingSurvey
	called := false
	runSurvey = func(agent string, m config.Model) string {
		called = true
		calledWith = pendingSurvey{agent: agent, m: m}
		return ""
	}
	noticeCalled := false
	tail := exitTail{priceNote: func() { noticeCalled = true }}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurveyTail(tail)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "wt: claude · claude/sonnet · 1s") {
		t.Errorf("stdout = %q, want the summary line", string(out))
	}
	if !called {
		t.Fatal("runSurvey was not invoked")
	}
	if calledWith.agent != "claude" || calledWith.m.ID != "claude/sonnet" {
		t.Fatalf("runSurvey called with %+v, want agent=claude model=claude/sonnet", calledWith)
	}
	if pendingSummary != "" {
		t.Errorf("pendingSummary = %q, want cleared", pendingSummary)
	}
	if pendingSurveyState.agent != "" {
		t.Errorf("pendingSurveyState = %+v, want cleared", pendingSurveyState)
	}
	if !noticeCalled {
		t.Error("the price notice was not invoked after the summary")
	}
}

// TestPrintPendingSummaryAndSurveySkipsWhenNoLaunch verifies runSurvey is
// never invoked when no launch happened (pendingSurveyState.agent == ""),
// e.g. the user quit from the worktree picker without launching anything.
func TestPrintPendingSummaryAndSurveySkipsWhenNoLaunch(t *testing.T) {
	prevSurvey, prevRunSurvey := pendingSurveyState, runSurvey
	t.Cleanup(func() {
		pendingSurveyState = prevSurvey
		runSurvey = prevRunSurvey
	})
	pendingSurveyState = pendingSurvey{}
	called := false
	runSurvey = func(agent string, m config.Model) string { called = true; return "" }
	noticeCalled := false
	tail := exitTail{priceNote: func() { noticeCalled = true }}

	printPendingSummaryAndSurveyTail(tail)
	if called {
		t.Fatal("runSurvey was invoked with no pending launch")
	}
	if noticeCalled {
		t.Error("the price notice was invoked with no launch")
	}
}

// TestPrintPendingSummaryAndSurveySkipsPriceNoticeForCommandAgent verifies
// the pricing reminder does NOT fire for a command-agent launch (e.g.
// shell), which records pendingSurveyState with m.ID == "" — the reminder
// is meaningless there and was previously firing unconditionally alongside
// the summary line.
func TestPrintPendingSummaryAndSurveySkipsPriceNoticeForCommandAgent(t *testing.T) {
	prevSummary, prevSurvey, prevRunSurvey := pendingSummary, pendingSurveyState, runSurvey
	t.Cleanup(func() {
		pendingSummary = prevSummary
		pendingSurveyState = prevSurvey
		runSurvey = prevRunSurvey
	})

	pendingSummary = "wt: shell · 1s"
	pendingSurveyState = pendingSurvey{agent: "shell", m: config.Model{}}
	runSurvey = func(agent string, m config.Model) string { return "" }
	noticeCalled := false
	tail := exitTail{priceNote: func() { noticeCalled = true }}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurveyTail(tail)
	w.Close()
	os.Stdout = old
	_, _ = io.ReadAll(r)

	if noticeCalled {
		t.Error("the price notice was invoked for a command agent (m.ID == \"\")")
	}
}

// TestModelSelectorWrapsInAppAndStandalone pins spec §6: the cursor wraps
// in the model selector on both of its build paths — the launcher's model
// phase (the catalog load) and the standalone start picker (newPickModel) —
// through bubbles' InfiniteScrolling. Why: the same screen two flows reach
// must not dead-end at either list edge; a flag set on one build site only
// would leave the other flow's cursor stuck.
func TestModelSelectorWrapsInAppAndStandalone(t *testing.T) {
	stubUsageStore(t)
	stubRunning(t, "omlx/qwen3.8")
	tempStateDir(t)
	cfg := modelTestConfig()

	// Standalone start picker.
	pm := newPickModel(pickModels(), themes.Default)
	if !pm.list.InfiniteScrolling {
		t.Fatal("standalone picker does not wrap (InfiniteScrolling unset)")
	}
	pm.list.Select(len(pm.list.Items()) - 1)
	next, _ := pm.list.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := next.Index(); got != 0 {
		t.Errorf("standalone: down from the last row = %d, want wrap to 0", got)
	}
	// Up from the wrapped-top row wraps back down to the end. (list.Update
	// returns an updated copy without mutating the field, so the chain runs
	// on the returned model.)
	prev, _ := next.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := prev.Index(); got != len(pm.list.Items())-1 {
		t.Errorf("standalone: up from the top row = %d, want wrap to the last row %d", got, len(pm.list.Items())-1)
	}

	// The launcher's model phase.
	nm := flowEnter(t, model{cfg: cfg, width: 80, height: 24}, "claude")
	if len(nm.models.Items()) == 0 {
		t.Fatal("model phase list is empty")
	}
	if !nm.models.InfiniteScrolling {
		t.Fatal("model phase list does not wrap (InfiniteScrolling unset)")
	}
	nm.models.Select(len(nm.models.Items()) - 1)
	updated, _ := nm.models.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := updated.Index(); got != 0 {
		t.Errorf("model phase: down from the last row = %d, want wrap to 0", got)
	}
}

// TestSessionRegisteredBeforeStartAndEndedOnExit pins the client session's
// life on the model server: it is registered when a model is chosen, before
// the start is posted (the server's idle scan must see it), never from
// Update; a start that fails re-registers it without the model; a launch row
// registers from its run command; a failed register is a status note, not a
// refusal; and the post-exit flow ends it, once and last.
func TestSessionRegisteredBeforeStartAndEndedOnExit(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog()}
	f.startFn = func(context.Context, int, modelapi.StartRequest, modelclient.Progress) (modelapi.JobStatus, error) {
		return modelapi.JobStatus{}, &modelclient.JobError{Problem: modelapi.Problem{Code: modelapi.CodeFailed, Message: "omlx refused to load"}}
	}
	m := remoteTable(t, f)
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	m, _ = updateMsg(m, recvStart(t, m))
	if want := []string{"register omlx/qwen3.8", "start omlx/qwen3.8", "remove omlx/qwen3.8"}; !reflect.DeepEqual(f.history(), want) {
		t.Fatalf("calls = %v, want %v", f.history(), want)
	}
	if !strings.Contains(m.status, "omlx refused to load") {
		t.Errorf("status = %q, want the start's failure", m.status)
	}

	m.hooks.gateway = func(string, config.Model, string, bool, agents.Gateway, []config.Model, []string) (*exec.Cmd, agents.LaunchInfo, error) {
		return nil, agents.LaunchInfo{}, errors.New("no agent here")
	}
	m, cmd := enterRemote(t, m, "openrouter/glm-5.3")
	if len(f.history()) != 3 || cmd == nil {
		t.Fatalf("Enter on a launch row: calls %v, cmd nil %v: want no call from Update and a run command", f.history(), cmd == nil)
	}
	m, _ = updateMsg(m, cmd())
	if !strings.Contains(m.status, "launch failed: no agent here") || m.committed {
		t.Errorf("status %q, committed %v: want the failure and the picker free to retry", m.status, m.committed)
	}
	if want := []string{"add openrouter/glm-5.3", "remove openrouter/glm-5.3"}; !reflect.DeepEqual(f.history()[3:], want) {
		t.Errorf("launch row calls = %v, want %v", f.history()[3:], want)
	}

	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "openrouter/glm-5.3"}}
	t.Cleanup(func() { pendingSurveyState = pendingSurvey{} })
	oldSurvey := runSurvey
	runSurvey = func(string, config.Model) string { return "" }
	t.Cleanup(func() { runSurvey = oldSurvey })
	printPendingSummaryAndSurveyTail(exitTailOf(m))
	h := f.history()
	if h[len(h)-1] != "end" || slices.Index(h, "end") != len(h)-1 {
		t.Errorf("calls = %v, want the client session ended once, last", h)
	}

	// A register the server refuses is a note on the status line; the start
	// still goes ahead.
	g := &fakeModels{cat: startRowCatalog(), registerErr: errors.New("model server at http://x:1 is not reachable")}
	g.startFn = f.startFn
	n := remoteTable(t, g)
	n, _ = enterRemote(t, n, "omlx/qwen3.8")
	n, _ = updateMsg(n, recvStart(t, n))
	if !strings.Contains(n.status, "did not register this client session") || !slices.Contains(g.history(), "start omlx/qwen3.8") {
		t.Errorf("status %q, calls %v: want the register note and the start still posted", n.status, g.history())
	}
}

// TestRemoteLaunchRunsTheAgentOnTheGateway pins the launch half of the API
// path, the non-TUI remote launch's steps in the run command: the agent is
// built against the server's gateway with the model's gateway id, the client
// session holds the model before the agent runs, the rotation is recorded,
// and nothing on the registry path (the refcount, the stop picker) is touched.
func TestRemoteLaunchRunsTheAgentOnTheGateway(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog()}
	m := remoteTable(t, f)
	var built config.Model
	m.hooks.gateway = func(agent string, mdl config.Model, path string, _ bool, _ agents.Gateway, _ []config.Model, _ []string) (*exec.Cmd, agents.LaunchInfo, error) {
		built = mdl
		cmd := exec.Command("true")
		cmd.Dir = path
		return cmd, agents.LaunchInfo{}, nil
	}
	m, cmd := enterRemote(t, m, "openrouter/glm-5.3")
	if m.phase != phaseLaunching || cmd == nil {
		t.Fatalf("phase %v, cmd nil %v: want the launching screen and its prep", m.phase, cmd == nil)
	}
	m, cmd = updateMsg(m, cmd())
	if !m.committed || cmd == nil {
		t.Fatalf("committed %v, cmd nil %v: want the launch committed once prepared", m.committed, cmd == nil)
	}
	msg, ok := cmd().(launchDoneMsg)
	if !ok || msg.err != nil {
		t.Fatalf("run command = %#v, want the agent's clean exit", msg)
	}
	if built.ID != "openrouter/glm-5.3" || built.GatewayID != "openrouter/glm-5.3" {
		t.Errorf("built %+v, want the chosen row's model with its gateway id", built)
	}
	if h := f.history(); !reflect.DeepEqual(h, []string{"register openrouter/glm-5.3"}) {
		t.Errorf("calls = %v, want the client session registered by the run command", h)
	}
	if last, _ := rotation.New().Last(); last != "openrouter/glm-5.3" {
		t.Errorf("rotation last = %q, want the launched model recorded", last)
	}
}

// stubGatewayBuild returns m with an agent builder that builds a no-op
// command, for the tests that drive a launch prep by hand.
func stubGatewayBuild(m model) model {
	m.hooks.gateway = func(_ string, _ config.Model, path string, _ bool, _ agents.Gateway, _ []config.Model, _ []string) (*exec.Cmd, agents.LaunchInfo, error) {
		cmd := exec.Command("true")
		cmd.Dir = path
		return cmd, agents.LaunchInfo{}, nil
	}
	return m
}

// TestLateEscDropsThePreparedModel pins that a prep finished before the esc
// reached Update (its result arrives stale) still takes its model out of the
// client session: nothing will launch it, so the server's idle scan must not
// be kept from stopping it.
func TestLateEscDropsThePreparedModel(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog()}
	m := remoteTable(t, f)
	m = stubGatewayBuild(m)
	m, cmd := enterRemote(t, m, "openrouter/glm-5.3")
	msg := cmd() // the prep ran to its end, holding the model
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.phase != phaseModel {
		t.Fatalf("phase = %v, want the table after esc", m.phase)
	}
	m, drop := updateMsg(m, msg)
	if m.committed || drop == nil {
		t.Fatalf("committed %v, cmd nil %v: want nothing launched and a drop command", m.committed, drop == nil)
	}
	if got := drop(); got != nil {
		t.Errorf("drop command returned %#v, want nothing", got)
	}
	if h := f.history(); !slices.Contains(h, "remove openrouter/glm-5.3") {
		t.Errorf("calls = %v, want the model dropped from the client session", h)
	}
}

// TestEscThenReEnterKeepsTheModelForTheNewLaunch pins the ordering guard on
// the stale drop: the first prep's late result arrives while a second launch
// of the same model is preparing, and must not take the model out from under
// it (the new prep's hold was a no-op, the model being held already).
func TestEscThenReEnterKeepsTheModelForTheNewLaunch(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog()}
	m := remoteTable(t, f)
	m = stubGatewayBuild(m)
	m, cmd := enterRemote(t, m, "openrouter/glm-5.3")
	stale := cmd()
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, cmd2 := enterRemote(t, m, "openrouter/glm-5.3")
	if m.phase != phaseLaunching || cmd2 == nil {
		t.Fatalf("phase %v, cmd nil %v: want the second launching screen", m.phase, cmd2 == nil)
	}
	m, drop := updateMsg(m, stale)
	if drop != nil {
		t.Errorf("the stale prep's result returned a command; it must not drop the model under the new launch")
	}
	if m.phase != phaseLaunching {
		t.Fatalf("phase = %v, the stale result must not leave the launching screen", m.phase)
	}
	m, run := updateMsg(m, cmd2())
	if !m.committed || run == nil {
		t.Errorf("committed %v, cmd nil %v: want the new launch committed", m.committed, run == nil)
	}
	if h := f.history(); slices.Contains(h, "remove openrouter/glm-5.3") {
		t.Errorf("calls = %v, the model was dropped from the new launch", h)
	}
}

// TestCtrlCDuringLaunchPrepLeavesWithoutAnAgent pins that the launch's server
// work (the client session's register, the gateway's model list) runs behind
// the launching screen, not under a live picker: ctrl+c there quits wt, the
// prep's result is dropped, the agent is never built or spawned, and the
// client session is ended. Before this, the run command did that work and
// went on to release the terminal and run the agent after the TUI had gone.
func TestCtrlCDuringLaunchPrepLeavesWithoutAnAgent(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog(), registerBlock: make(chan struct{})}
	m := remoteTable(t, f)
	m.hooks.gateway = func(string, config.Model, string, bool, agents.Gateway, []config.Model, []string) (*exec.Cmd, agents.LaunchInfo, error) {
		t.Error("the agent was built after ctrl+c")
		return exec.Command("true"), agents.LaunchInfo{}, nil
	}
	m, cmd := enterRemote(t, m, "openrouter/glm-5.3")
	if m.phase != phaseLaunching || cmd == nil || m.committed {
		t.Fatalf("phase %v, cmd nil %v, committed %v: want the launching screen, a prep command and nothing committed", m.phase, cmd == nil, m.committed)
	}
	if v := plain(m); !strings.Contains(v, "openrouter/glm-5.3") || !strings.Contains(v, "ctrl+c quit") {
		t.Errorf("launching view = %q, want the model and ctrl+c quit", v)
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	m, quit := updateMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit == nil || quit() != tea.Quit() {
		t.Fatal("ctrl+c on the launching screen did not quit")
	}
	settleRemoteStart(m) // what Run does once the program is over
	var msg tea.Msg
	select {
	case msg = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("the prep command did not end after ctrl+c")
	}
	m, run := updateMsg(m, msg)
	if run != nil || m.committed {
		t.Errorf("the prep's result after ctrl+c committed a launch (cmd nil %v, committed %v)", run == nil, m.committed)
	}
	exitTailOf(m).release()
	if h := f.history(); len(h) == 0 || h[len(h)-1] != "end" {
		t.Errorf("calls = %v, want the client session ended", h)
	}
}
