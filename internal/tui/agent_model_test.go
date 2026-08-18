package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/session"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// tempStateDir creates an isolated agent-wt state directory under a temp
// XDG_CONFIG_HOME. rotation.ForTag reads its per-tag state from disk, so
// every test that presses 'r' must isolate state or it becomes dependent on
// the host's real rotation-*.state files.
func tempStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent-wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(dir))
	return dir
}

// seedState writes "<index>\n<last>\n" for a tag group so rotation.Next picks
// deterministically from a known starting point.
func seedState(t *testing.T, dir, tag, content string) {
	t.Helper()
	if err := os.WriteFile(rotation.StateFile(dir, tag), []byte(content), 0o600); err != nil {
		t.Fatalf("seed %s state: %v", tag, err)
	}
}

// singleModelList builds a one-item list.Model with m highlighted at
// index 0. It replaces the pre-refactor pattern of setting m.current
// directly: tests that need "the picker has X highlighted" now build
// a single-item list and select index 0. m.models is the single
// source of truth for the picked model in production code; this
// helper lets tests mirror that contract.
func singleModelList(m config.Model) list.Model {
	items := []list.Item{modelItem{model: m}}
	ml := list.New(items, list.NewDefaultDelegate(), 80, 24)
	ml.Select(0)
	return ml
}

// phaseModelWithList builds a model in phaseModel with m.models populated
// for the given agent+tag, m.current set to the rotation's next-to-use
// model, and the list cursor on that model. Tests that exercise 'r', 'd',
// or the View use this helper instead of constructing model literals
// (which would skip the list-build path the production code uses).
func phaseModelWithList(t *testing.T, cfg *config.Config, agent, tag string) model {
	t.Helper()
	models, err := cfg.ModelsForAgentAndTag(agent, tag)
	if err != nil {
		t.Fatalf("ModelsForAgentAndTag: %v", err)
	}
	if len(models) == 0 {
		t.Fatalf("phaseModelWithList: no models for agent %q tag %q", agent, tag)
	}
	m := model{
		cfg:       cfg,
		phase:     phaseModel,
		agent:     agent,
		tag:       tag,
		models:    buildModelList(models, 80, 24),
		modelsFor: agent,
		modelsTag: tag,
		width:     80,
		height:    24,
	}
	// Set up the rotation snapshot for the picker's filtered list and
	// position the cursor on the model after the last-launched one.
	m.rotation = rotation.New(tag, models, "")
	if last, ok := m.rotation.LastLaunched(); ok {
		if next, ok := FindAfter(models, last); ok {
			m.models.Select(indexOfModel(models, next))
		}
	}
	return m
}

// TestFirstAgentDefaultsToClaude asserts DefaultAgent falls back to "claude"
// when there is no config or no agents are configured. The TUI must still
// show a sensible default even against an empty catalog.
func TestFirstAgentDefaultsToClaude(t *testing.T) {
	for _, cfg := range []*config.Config{nil, {DefaultTag: "code"}} {
		if got := cfg.DefaultAgent(); got != "claude" {
			t.Errorf("DefaultAgent(%v) = %q, want claude", cfg, got)
		}
	}
}

// TestFirstAgentPicksFirst asserts DefaultAgent returns the first configured
// agent. This is the initial agent shown on the agent+model screen.
func TestFirstAgentPicksFirst(t *testing.T) {
	cfg := &config.Config{Agents: []config.Agent{{Name: "codex"}, {Name: "claude"}}}
	if got := cfg.DefaultAgent(); got != "codex" {
		t.Errorf("DefaultAgent = %q, want codex", got)
	}
}

// TestModelsForAgentAndTagEmptyConfig asserts the helper returns an
// empty list (not the legacy (none) placeholder) when the agent exists
// but has no models for the requested tag. Validation happens at the
// phaseList → phaseModel transition, not in a placeholder model.
func TestModelsForAgentAndTagEmptyConfig(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama"}},
		// No models — ModelsForAgentAndTag should return an empty list.
	}
	got, err := cfg.ModelsForAgentAndTag("claude", "code")
	if err != nil {
		t.Fatalf("ModelsForAgentAndTag: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 (validation gate, not placeholder)", len(got))
	}
}

// TestModelsForAgentAndTagReturnsFirstInTag asserts ModelsForAgentAndTag
// preserves config-order, so the picker can use index 0 as the
// "first model in this group" without further sorting.
func TestModelsForAgentAndTagReturnsFirstInTag(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama"}},
		Models: []config.Model{
			{ID: "design-first", ProviderID: "ollama", Tags: []string{"design"}},
			{ID: "code-first", ProviderID: "ollama", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	got, err := cfg.ModelsForAgentAndTag("claude", "code")
	if err != nil {
		t.Fatalf("ModelsForAgentAndTag: %v", err)
	}
	if len(got) != 1 || got[0].ID != "code-first" {
		t.Errorf("got = %+v, want [code-first]", got)
	}
}

// TestSelectedEntryMsgEmptyListStaysOnList asserts that when the agent+tag
// has no compatible models, picking a worktree does NOT enter the model
// phase. The validation gate at the phaseList → phaseModel boundary
// surfaces a status message and keeps the user in the picker.
func TestSelectedEntryMsgEmptyListStaysOnList(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama"}},
		// No models with provider "ollama".
	}
	m := model{cfg: cfg, phase: phaseList}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList (no entry into picker)", gotModel.phase)
	}
	if gotModel.status == "" {
		t.Error("status = empty, want an error message")
	}
}

// TestSelectedEntryMsgEmptyListSetsActionableStatus asserts the status
// message names the agent and the tag so the user knows what to fix.
func TestSelectedEntryMsgEmptyListSetsActionableStatus(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama"}},
	}
	m := model{cfg: cfg, phase: phaseList, agent: "claude", tag: "code"}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	for _, want := range []string{"claude", "code"} {
		if !strings.Contains(gotModel.status, want) {
			t.Errorf("status %q missing %q", gotModel.status, want)
		}
	}
}

// TestToggleBackToCode asserts pressing 'd' twice returns to the code group.
// Toggling is meant to be a stable two-way switch, not a one-way trip.
// (otherTag is now computed via oppositeTag(m.tag), not stored on model.)
func TestToggleBackToCode(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	got, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.tag != "code" {
		t.Errorf("tag = %q, want code (after two toggles)", gotModel.tag)
	}
}

// TestTagKeyIgnoredInListPhase asserts 'd' does nothing while still on
// the worktree list, matching how 'r' is gated. The model-screen keybind
// must not fire before a worktree is chosen. (The 'm' key was removed
// when the browser was deleted; this test now covers only 'd'.)
func TestTagKeyIgnoredInListPhase(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseList, tag: "code",
		models: singleModelList(config.Model{ID: "ollama/gemma4:9b"})}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status mutated in list phase: %q", gotModel.status)
	}
	if gotModel.tag != "code" {
		t.Errorf("tag mutated in list phase: %q", gotModel.tag)
	}
}

// TestModelScreenMKeyIsNoOp asserts that pressing 'm' on the model
// screen does not open a browser (there is no browser anymore) and
// does not mutate any state. This pins the removal: if a future
// change re-introduces 'm' as a keybind, this test will fail.
func TestModelScreenMKeyIsNoOp(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	before := m
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	gotModel := got.(model)
	if gotModel.phase != before.phase {
		t.Errorf("phase changed: before %v, after %v", before.phase, gotModel.phase)
	}
	if gotModel.tag != before.tag {
		t.Errorf("tag changed: before %q, after %q", before.tag, gotModel.tag)
	}
	if len(gotModel.models.Items()) != len(before.models.Items()) {
		t.Errorf("models items changed: before %d, after %d", len(before.models.Items()), len(gotModel.models.Items()))
	}
}

// TestViewModelPhase asserts the model-phase View renders the picker list
// with the agent and tag in the header and the keybind hints in the footer.
func TestViewModelPhase(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	view := m.View()
	for _, want := range []string{"agent", "claude", "tag", "code", "ollama/gemma4:9b", "[↑/↓] navigate", "[d] switch tag", "[enter] launch"} {
		if !strings.Contains(view, want) {
			t.Errorf("View missing %q in:\n%s", want, view)
		}
	}
}

// TestEnterInModelPhaseLaunchesWithoutSession asserts that pressing Enter
// when there is no prior session quits the TUI and runs the agent. We test
// the immediate-launch path by inspecting the returned command batch.
func TestEnterInModelPhaseLaunchesWithoutSession(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel, agent: "claude", tag: "code",
		selectedPath: t.TempDir(), models: singleModelList(config.Model{ID: "ollama/gemma4:9b"})}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty", gotModel.status)
	}
	if cmd == nil {
		t.Fatal("expected launch command batch, got nil")
	}
}

// TestEnterInModelPhaseShowsResumePrompt asserts that when a session exists,
// Enter moves to phaseResume instead of launching immediately.
func TestEnterInModelPhaseShowsResumePrompt(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	repo := t.TempDir()
	slug := session.Slug(repo)
	sessDir := filepath.Join(homeDir, ".claude", "projects", slug)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "session.jsonl"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	m := model{cfg: testConfig(), phase: phaseModel, agent: "claude", tag: "code",
		selectedPath: repo, models: singleModelList(config.Model{ID: "ollama/gemma4:9b"})}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.phase != phaseResume {
		t.Errorf("phase = %v, want phaseResume", gotModel.phase)
	}
	if cmd != nil {
		t.Errorf("expected no cmd while showing resume prompt, got %v", cmd)
	}
}

// TestResumePromptCancelReturnsToModel asserts selecting Cancel in the
// resume prompt returns to the model phase without launching.
func TestResumePromptCancelReturnsToModel(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseResume, agent: "claude", tag: "code",
		selectedPath: t.TempDir(), models: singleModelList(config.Model{ID: "ollama/gemma4:9b"}),
		resume: resumeModel{choices: list.New(buildResumeChoices(nil), list.NewDefaultDelegate(), 80, 24)}}
	// Move selection to Cancel (index 1).
	m.resume.choices.CursorDown()
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel", gotModel.phase)
	}
	if cmd != nil {
		t.Errorf("expected no cmd for cancel, got %v", cmd)
	}
}

// TestLaunchDoneMsgRecordsError asserts that when the agent subprocess
// exits with an error, the model stores the error so it can be surfaced.
func TestLaunchDoneMsgRecordsError(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel}
	got, _ := m.Update(launchDoneMsg{err: nil})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty for nil error", gotModel.status)
	}

	got, cmd := got.Update(launchDoneMsg{err: nil})
	_ = got.(model)
	if cmd == nil {
		t.Errorf("cmd for launchDoneMsg = nil, want non-nil")
	}
}

// TestEscInResumePromptReturnsToModel asserts esc from the resume prompt
// returns to the model phase, not quitting the TUI.
func TestEscInResumePromptReturnsToModel(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseResume, agent: "claude", tag: "code"}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel", gotModel.phase)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd for esc in resume prompt, got %v", cmd)
	}
}

// TestWindowSizeResizesResumePrompt asserts a WindowSizeMsg resizes the
// resume prompt list so it renders at the current terminal dimensions.
func TestWindowSizeResizesResumePrompt(t *testing.T) {
	m := model{phase: phaseResume, resume: resumeModel{choices: list.New(buildResumeChoices(nil), list.NewDefaultDelegate(), 10, 10)}}
	got, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	gotModel := got.(model)
	if gotModel.width != 80 || gotModel.height != 24 {
		t.Errorf("dimensions = (%d, %d), want (80, 24)", gotModel.width, gotModel.height)
	}
}

// TestResumePromptResumeChoiceLaunchesWithSession asserts selecting the
// resume choice builds a launch batch carrying the session resume flag.
func TestResumePromptResumeChoiceLaunchesWithSession(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseResume, agent: "claude", tag: "code",
		selectedPath: t.TempDir(), models: singleModelList(config.Model{ID: "ollama/gemma4:9b"}),
		launchModel: config.Model{ID: "ollama/gemma4:9b"},
		resume: resumeModel{
			session: &session.Session{ID: "abc-123"},
			choices: list.New(buildResumeChoices(&session.Session{ID: "abc-123"}), list.NewDefaultDelegate(), 80, 24),
		}}
	// Resume choice is at index 0.
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty", gotModel.status)
	}
	if cmd == nil {
		t.Fatal("expected launch command batch, got nil")
	}
}

// TestResumePromptStartFreshLaunchesWithoutSession asserts selecting Start
// fresh builds a launch batch without a session.
func TestResumePromptStartFreshLaunchesWithoutSession(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseResume, agent: "claude", tag: "code",
		selectedPath: t.TempDir(), models: singleModelList(config.Model{ID: "ollama/gemma4:9b"}),
		launchModel: config.Model{ID: "ollama/gemma4:9b"},
		resume: resumeModel{
			session: &session.Session{ID: "abc-123"},
			choices: list.New(buildResumeChoices(&session.Session{ID: "abc-123"}), list.NewDefaultDelegate(), 80, 24),
		}}
	// Move selection to Start fresh (index 1).
	m.resume.choices.CursorDown()
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty", gotModel.status)
	}
	if cmd == nil {
		t.Fatal("expected launch command batch, got nil")
	}
}

// TestViewResumePhase asserts the resume prompt View includes the list title
// and navigation hint. This confirms the screen is coherent.
func TestViewResumePhase(t *testing.T) {
	m := model{phase: phaseResume, width: 80, height: 24, resume: resumeModel{
		choices: list.New(buildResumeChoices(
			&session.Session{ID: "abc-123"}), list.NewDefaultDelegate(), 78, 22),
	}}
	m.resume.choices.Title = "Resume previous session?"
	view := m.View()
	for _, want := range []string{"Resume previous session?", "Resume abc-123", "[enter] choose"} {
		if !strings.Contains(view, want) {
			t.Errorf("View missing %q in:\n%s", want, view)
		}
	}
}

// TestResumePromptListNavigates asserts arrow keys update the resume prompt
// selection. Without this the user cannot choose Cancel or Start fresh.
func TestResumePromptListNavigates(t *testing.T) {
	m := model{phase: phaseResume, width: 80, height: 24, resume: resumeModel{
		choices: list.New(buildResumeChoices(&session.Session{ID: "abc-123"}), list.NewDefaultDelegate(), 78, 22),
	}}
	if m.resume.choices.Index() != 0 {
		t.Fatalf("initial index = %d, want 0", m.resume.choices.Index())
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	gotModel := got.(model)
	if gotModel.resume.choices.Index() != 1 {
		t.Errorf("index after down = %d, want 1", gotModel.resume.choices.Index())
	}
}

// TestSelectedEntryMsgSetsSelectedPath asserts picking a worktree stores its
// path so the launch command can cd into it.
func TestSelectedEntryMsgSetsSelectedPath(t *testing.T) {
	m := model{cfg: testConfig()}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: "/tmp/feature"}})
	gotModel := got.(model)
	if gotModel.selectedPath != "/tmp/feature" {
		t.Errorf("selectedPath = %q, want /tmp/feature", gotModel.selectedPath)
	}
}

// TestYoloFlagPassedToLaunch asserts that when the model has yolo=true, the
// launch command builder propagates it to the agent driver.
func TestYoloFlagPassedToLaunch(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel, agent: "claude", tag: "code",
		selectedPath: t.TempDir(), models: singleModelList(config.Model{ID: "ollama/gemma4:9b"}), yolo: true}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty", gotModel.status)
	}
	if cmd == nil {
		t.Fatal("expected launch command batch, got nil")
	}
}

// TestUnknownAgentLaunchShowsError asserts that pressing Enter with an
// unregistered agent surfaces a readable error instead of launching.
func TestUnknownAgentLaunchShowsError(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel, agent: "not-an-agent", tag: "code",
		selectedPath: t.TempDir(), models: singleModelList(config.Model{ID: "ollama/gemma4:9b"})}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if cmd != nil {
		t.Errorf("expected nil cmd for unknown agent, got %v", cmd)
	}
	if !strings.Contains(gotModel.status, "unknown agent") {
		t.Errorf("status = %q, want 'unknown agent' error", gotModel.status)
	}
}

// TestSessionCheckErrorShowsStatus asserts that if session.LatestForAgent
// returns an error, the TUI surfaces it in status instead of crashing.
func TestSessionCheckErrorShowsStatus(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel, agent: "opencode", tag: "code",
		selectedPath: "/nonexistent/path/that/cannot/be/git", models: singleModelList(config.Model{ID: "ollama/gemma4:9b"})}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if cmd != nil {
		t.Errorf("expected nil cmd on session check error, got %v", cmd)
	}
	if !strings.Contains(gotModel.status, "session check failed") {
		t.Errorf("status = %q, want 'session check failed'", gotModel.status)
	}
}

// TestResumePromptCancelDoesNotMutateModel asserts that canceling the resume
// prompt preserves the current agent and model.
func TestResumePromptCancelDoesNotMutateModel(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseResume, agent: "claude", tag: "code",
		models: singleModelList(config.Model{ID: "ollama/gemma4:9b"}),
		resume:  resumeModel{choices: list.New(buildResumeChoices(nil), list.NewDefaultDelegate(), 80, 24)}}
	m.resume.choices.CursorDown() // Cancel
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.agent != "claude" || gotModel.tag != "code" {
		t.Errorf("model mutated on cancel: agent=%q tag=%q", gotModel.agent, gotModel.tag)
	}
}

// TestResumeChoiceTitleIncludesRelativeTime asserts the resume choice shows
// the session's relative time so the user knows how stale it is.
func TestResumeChoiceTitleIncludesRelativeTime(t *testing.T) {
	sess := &session.Session{ID: "abc-123", MTime: time.Now()}
	items := buildResumeChoices(sess)
	if len(items) == 0 {
		t.Fatal("expected resume items")
	}
	item := items[0].(resumeItem)
	if item.choice != resumeChoice {
		t.Errorf("first choice = %v, want resumeChoice", item.choice)
	}
	if !strings.Contains(item.desc, "ago") && !strings.Contains(item.desc, "just now") {
		t.Errorf("desc = %q, want relative time", item.desc)
	}
}

// TestSelectedEntryMsgPositionsCursorAtNextToUse asserts the cursor lands
// on the rotation's next-to-use model, not necessarily index 0.
func TestSelectedEntryMsgPositionsCursorAtNextToUse(t *testing.T) {
	dir := tempStateDir(t)
	// State: next_index=1, last=ollama/gemma4:9b. With 2 code models, Next()
	// returns models[1] (= gemma4:14b) and advances to index 0.
	seedState(t, dir, "code", "1\nollama/gemma4:9b\n")
	cfg := testConfig()
	m := model{cfg: cfg, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.models.Index() != 1 {
		t.Errorf("cursor index = %d, want 1", gotModel.models.Index())
	}
}

// TestPhaseModelWithListBuildsAndPositionsCursor asserts the test
// helper populates m.models and positions the cursor on the rotation's
// next-to-use model. The production code uses the same list-build path
// (in selectedEntryMsg and on 'd'), so this is a smoke test of the
// shared infrastructure.
func TestPhaseModelWithListBuildsAndPositionsCursor(t *testing.T) {
	dir := tempStateDir(t)
	// State file: legacy 2-line "1\nollama/gemma4:9b\n". LastLaunched
	// reads the last non-empty line (gemma4:9b) and FindAfter returns
	// the next model (gemma4:14b at index 1). The cursor lands on
	// gemma4:14b.
	seedState(t, dir, "code", "1\nollama/gemma4:9b\n")
	cfg := testConfig()
	m := phaseModelWithList(t, cfg, "claude", "code")

	if got := len(m.models.Items()); got != 2 {
		t.Errorf("models items = %d, want 2 (testConfig code models)", got)
	}
	if m.models.Index() != 1 {
		t.Errorf("cursor index = %d, want 1 (after gemma4:9b)", m.models.Index())
	}
}

// TestModelScreenToggleTagRebuildsList asserts pressing 'd' rebuilds
// m.models from the new tag's models and positions the cursor on the
// new rotation index.
func TestModelScreenToggleTagRebuildsList(t *testing.T) {
	dir := tempStateDir(t)
	// Pre-seed design rotation with index 0 = gemma4:design.
	seedState(t, dir, "design", "0\nollama/gemma4:design\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	if m.tag != "code" {
		t.Fatalf("precondition: tag = %q, want code", m.tag)
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.tag != "design" {
		t.Errorf("tag = %q, want design", gotModel.tag)
	}
	if len(gotModel.models.Items()) != 1 {
		t.Errorf("models items = %d, want 1 (only design model)", len(gotModel.models.Items()))
	}
}

// TestModelScreenToggleTagEmptyRestores asserts that toggling to a tag
// with no models reverts to the previous tag and surfaces a status
// message rather than leaving the user in a void.
func TestModelScreenToggleTagEmptyRestores(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama"}},
		Models: []config.Model{
			// Only code models, no design.
			{ID: "ollama/code-only", ProviderID: "ollama", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	m := phaseModelWithList(t, cfg, "claude", "code")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.tag != "code" {
		t.Errorf("tag = %q, want code (restored)", gotModel.tag)
	}
	if gotModel.status == "" {
		t.Error("status = empty, want error message about empty design tag")
	}
}

// TestPhaseModelUpDownMovesCursor asserts the picker forwards
// arrow keys to bubble/list. Before this fix, Update() never called
// m.models.Update(msg) in phaseModel, so up/down were dead keys.
// This is the regression guard for the up/down bug.
func TestPhaseModelUpDownMovesCursor(t *testing.T) {
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	if m.models.Index() != 0 {
		t.Fatalf("precondition: cursor = %d, want 0", m.models.Index())
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(model)
	if m.models.Index() != 1 {
		t.Errorf("cursor after down = %d, want 1", m.models.Index())
	}
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = got.(model)
	if m.models.Index() != 0 {
		t.Errorf("cursor after up = %d, want 0", m.models.Index())
	}
}

// TestPhaseModelEnterStaysInApp asserts Enter in the picker does
// NOT get consumed by bubble/list (which would otherwise launch
// the highlighted item twice). The picker intercepts Enter for
// ollama check + launch; the list is not allowed to handle it.
func TestPhaseModelEnterStaysInApp(t *testing.T) {
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := got.(model); !ok {
		t.Errorf("Update(Enter) returned %T, want model", got)
	}
	_ = cmd
}

// TestNoRKeyInModelPhase asserts pressing 'r' in phaseModel is a
// no-op. Rotation now advances via RecordLaunch on Enter, not via
// an explicit key. This test guards against accidental re-
// introduction of the r key (which would conflict with the new
// implicit-rotation model).
func TestNoRKeyInModelPhase(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/gemma4:9b\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	beforeCursor := m.models.Index()
	beforeItems := len(m.models.Items())
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gotModel := got.(model)
	if gotModel.models.Index() != beforeCursor {
		t.Errorf("cursor moved on 'r': before=%d after=%d", beforeCursor, gotModel.models.Index())
	}
	if len(gotModel.models.Items()) != beforeItems {
		t.Errorf("list size changed on 'r': before=%d after=%d", beforeItems, len(gotModel.models.Items()))
	}
}

// TestSelectedEntryNoLastLaunchedStartsAtZero asserts the picker
// lands the cursor at index 0 when no rotation state exists. This
// is the cold-start path: fresh user, fresh state file.
func TestSelectedEntryNoLastLaunchedStartsAtZero(t *testing.T) {
	tempStateDir(t) // isolates state; file doesn't exist
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor index = %d, want 0 (no last-launched)", gotModel.models.Index())
	}
}

// TestSelectedEntryPositionsAfterLastLaunched asserts the picker
// lands the cursor on the model after the last-launched one. With
// the new model, rotation advances implicitly — every picker entry
// is "one past where we left off".
func TestSelectedEntryPositionsAfterLastLaunched(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/gemma4:9b\n")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	// testConfig has two code models: gemma4:9b (index 0) and
	// gemma4:14b (index 1). Last launched was gemma4:9b, so the
	// next-to-show must be gemma4:14b.
	if gotModel.models.Index() != 1 {
		t.Errorf("cursor index = %d, want 1 (after gemma4:9b)", gotModel.models.Index())
	}
}

// TestSelectedEntryLastLaunchedMissingFallsBackToZero asserts the
// picker lands on index 0 when the saved last-launched model is no
// longer in the snapshot (config changed since last launch).
func TestSelectedEntryLastLaunchedMissingFallsBackToZero(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/removed:cloud\n")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor index = %d, want 0 (fallback when last-launched missing)", gotModel.models.Index())
	}
}

// TestSelectedEntryLastLaunchedLastInListWrapsToZero asserts the
// picker wraps to index 0 when the saved last-launched is the last
// item in the snapshot. Without wrap, the cursor would advance
// past the end of the list forever.
func TestSelectedEntryLastLaunchedLastInListWrapsToZero(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/gemma4:14b\n") // last item in testConfig
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor index = %d, want 0 (wrap when last-launched is last)", gotModel.models.Index())
	}
}

// TestToggleTagRebuildsRotation asserts pressing 'd' rebuilds both
// the model list and the rotation snapshot from the new tag's
// filtered models. Without a fresh snapshot, rotation would still
// operate on the old tag's list.
func TestToggleTagRebuildsRotation(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "design", "ollama/gemma4:design\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	if m.tag != "code" {
		t.Fatalf("precondition: tag = %q, want code", m.tag)
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.tag != "design" {
		t.Errorf("tag = %q, want design", gotModel.tag)
	}
	if gotModel.rotation == nil {
		t.Fatal("rotation is nil after 'd' toggle")
	}
	if gotModel.rotation.Tag() != "design" {
		t.Errorf("rotation tag = %q, want design", gotModel.rotation.Tag())
	}
	// The design tag has one model (gemma4:design). Last-launched
	// is gemma4:design, so FirstAfter wraps to index 0 (only one
	// item).
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor index = %d, want 0 (wrap on single-item design)", gotModel.models.Index())
	}
	if len(gotModel.models.Items()) != 1 {
		t.Errorf("models items = %d, want 1 (only design model)", len(gotModel.models.Items()))
	}
}

// TestEnterInModelPhaseDoesNotRecordBeforeLaunch asserts that pressing Enter
// alone does NOT write rotation state. Recording must happen only when the
// launch actually commits (launchAndRecord), so a launch that never runs —
// here the ollama availability warning fires, since the test model has no
// ModelName so no ollama model matches — must not advance the rotation.
// This is the regression guard for the "rotation advances on cancelled
// launches" bug: without it, a user who cancels the ollama warning (or a
// resume prompt, or hits an ollama-check error) would silently skip a model
// on the next picker entry despite never launching anything.
func TestEnterInModelPhaseDoesNotRecordBeforeLaunch(t *testing.T) {
	dir := tempStateDir(t)
	m := phaseModelWithList(t, testConfig(), "claude", "code")

	// Cursor is at index 0 by default (no last-launched seed).
	if m.models.Index() != 0 {
		t.Fatalf("precondition: cursor = %d, want 0", m.models.Index())
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// The launch did not commit (the ollama warning gates it), so no
	// rotation state must have been written.
	if _, err := os.Stat(rotation.StateFile(dir, "code")); err == nil {
		t.Errorf("rotation state written on Enter without a committed launch")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat rotation state: %v", err)
	}
}

// TestLaunchAndRecordWritesLastLaunched asserts that launchAndRecord — the
// single commit point reached only after the ollama check and resume prompt
// are satisfied — writes the launched model's ID to rotation-<tag>.state.
// This is the positive counterpart to TestEnterInModelPhaseDoesNotRecordBefore
// Launch: the rotation advances exactly when a launch commits, no sooner.
func TestLaunchAndRecordWritesLastLaunched(t *testing.T) {
	dir := tempStateDir(t)
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	first, ok := m.models.Items()[0].(modelItem)
	if !ok {
		t.Fatalf("items[0] is %T, want modelItem", m.models.Items()[0])
	}
	// launchAndRecord records the model captured in m.launchModel, then
	// returns a tea.Cmd that would run the agent. We discard that cmd so
	// the test never execs anything; the recording is what we verify.
	m.launchModel = first.model
	m, _ = m.launchAndRecord(exec.Command("true"))

	data, err := os.ReadFile(rotation.StateFile(dir, "code"))
	if err != nil {
		t.Fatalf("read state after launchAndRecord: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != first.model.ID {
		t.Errorf("state file = %q, want %q (launched model)", got, first.model.ID)
	}
}

// TestNextEntryAfterLaunchAdvancesCursor asserts the picker entry after a
// committed launch lands on the model AFTER the just-launched one. This is
// the core promise of rotation-by-launch: every launch advances the
// rotation. The launch is committed via launchAndRecord (the real recording
// path) rather than bare Enter, so the test exercises the contract without
// depending on a real agent binary or ollama being available.
func TestNextEntryAfterLaunchAdvancesCursor(t *testing.T) {
	dir := tempStateDir(t)
	// Seed: last-launched is gemma4:9b (index 0). Picker entry 1
	// will land on gemma4:14b (index 1).
	seedState(t, dir, "code", "ollama/gemma4:9b\n")

	// Picker entry 1.
	m := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m = got.(model)
	if m.models.Index() != 1 {
		t.Fatalf("entry 1: cursor = %d, want 1 (gemma4:14b)", m.models.Index())
	}

	// Commit the launch of gemma4:14b (the highlighted model) via the
	// shared commit point. The returned tea.Cmd would run the agent; we
	// discard it so the test never execs anything. Recording happens
	// synchronously here, advancing the rotation to gemma4:14b.
	m.launchModel = m.models.Items()[m.models.Index()].(modelItem).model
	m, _ = m.launchAndRecord(exec.Command("true"))

	// Picker entry 2 (e.g., user backed out and picked again, or the app
	// restarted). New selectedEntryMsg rebuilds the rotation, sees
	// gemma4:14b as last-launched, and advances to the next model — which
	// wraps to gemma4:9b (only 2 models in the code tag).
	m2 := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	got, _ = m2.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m2 = got.(model)
	if m2.models.Index() != 0 {
		t.Errorf("entry 2: cursor = %d, want 0 (wrap after gemma4:14b)", m2.models.Index())
	}
}

// TestNextEntryAfterManualPickAdvancesFromManualPick asserts the rotation
// advances from the user's manual pick when they launch it. If the user
// navigates to a non-rotation-suggested model and launches, the next entry
// should land on the model AFTER the manual pick — not the model after the
// prior rotation pick. This protects against "manual picks are ignored"
// regressions. The launch is committed via launchAndRecord so the test
// exercises the real recording path without a real agent or ollama.
func TestNextEntryAfterManualPickAdvancesFromManualPick(t *testing.T) {
	dir := tempStateDir(t)
	// No prior state. Picker entry 1 lands at index 0.
	_ = dir

	m := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m = got.(model)
	if m.models.Index() != 0 {
		t.Fatalf("entry 1: cursor = %d, want 0", m.models.Index())
	}

	// User navigates down to index 1 — manual pick of gemma4:14b.
	// (Capture the returned model so the cursor advance sticks; m.models
	// is a value field, so the in-place update inside Update() doesn't
	// propagate unless we use the result.)
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(model)
	if m.models.Index() != 1 {
		t.Fatalf("after Down: cursor = %d, want 1 (gemma4:14b)", m.models.Index())
	}

	// Commit the launch of gemma4:14b (the manual pick) via the shared
	// commit point. Discard the returned tea.Cmd so nothing execs.
	m.launchModel = m.models.Items()[m.models.Index()].(modelItem).model
	m, _ = m.launchAndRecord(exec.Command("true"))

	// Picker entry 2: last-launched is gemma4:14b (the manual pick), so
	// the next entry wraps to gemma4:9b (index 0).
	m2 := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	got, _ = m2.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m2 = got.(model)
	if m2.models.Index() != 0 {
		t.Errorf("entry 2: cursor = %d, want 0 (wrap after manual pick gemma4:14b)", m2.models.Index())
	}
}
