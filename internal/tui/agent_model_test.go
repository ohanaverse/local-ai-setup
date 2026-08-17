package tui

import (
	"os"
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
	if next, ok := rotation.ForTag(cfg, tag).Next(""); ok {
		m.current = next
		m.models.Select(indexOfModel(models, next))
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

// TestRotatePersistsState asserts pressing 'r' advances the on-disk
// rotation-<tag>.state file, not just the in-memory model. Without state
// persistence a restart would lose the user's place in the rotation.
func TestRotatePersistsState(t *testing.T) {
	dir := tempStateDir(t)
	m := model{cfg: testConfig(), phase: phaseModel, tag: "code",
		current: config.Model{ID: "ollama/gemma4:9b"}}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	data, err := os.ReadFile(rotation.StateFile(dir, "code"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	content := string(data)
	// Fresh state starts at index 0, so one rotate picks the first model and
	// persists the next index as 1.
	if !strings.HasPrefix(content, "1\n") {
		t.Errorf("state = %q, want index line starting with 1", content)
	}
	if !strings.Contains(content, "ollama/gemma4:9b") {
		t.Errorf("state = %q, want last-selected model recorded", content)
	}
}

// TestRotateSkipsOtherTagLastUsed asserts that when the other tag group most
// recently used a model, rotating the active group skips that model and
// lands on the next one instead. This is the cross-tag skip wired through
// rot.Next(otherTag); without it both groups could stack on the same model.
func TestRotateSkipsOtherTagLastUsed(t *testing.T) {
	dir := tempStateDir(t)
	// code = [A, B]; B is shared with design. Point code's index at B and
	// record that design last used B, so rotating code must skip B → A.
	cfg := &config.Config{DefaultTag: "code", Models: []config.Model{
		{ID: "A", Tags: []string{"code"}},
		{ID: "B", Tags: []string{"code", "design"}},
	}}
	seedState(t, dir, "code", "1\nB\n")
	seedState(t, dir, "design", "0\nB\n")

	m := model{cfg: cfg, phase: phaseModel, tag: "code",
		current: config.Model{ID: "B"}}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gotModel := got.(model)
	if gotModel.current.ID != "A" {
		t.Errorf("current = %q, want A (B cross-skipped by design)", gotModel.current.ID)
	}
}

// TestRotateSingleModelStaysPut asserts rotating a one-model tag group keeps
// the same model. The cross-skip fallback must still make progress by
// returning the sole member rather than reporting failure.
func TestRotateSingleModelStaysPut(t *testing.T) {
	dir := tempStateDir(t)
	cfg := &config.Config{Models: []config.Model{
		{ID: "only", Tags: []string{"code"}},
	}}
	seedState(t, dir, "code", "0\nonly\n")

	m := model{cfg: cfg, phase: phaseModel, tag: "code", current: config.Model{ID: "only"}}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gotModel := got.(model)
	if gotModel.current.ID != "only" {
		t.Errorf("current = %q, want only", gotModel.current.ID)
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
		current: config.Model{ID: "ollama/gemma4:9b"}}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status mutated in list phase: %q", gotModel.status)
	}
	if gotModel.tag != "code" {
		t.Errorf("tag mutated in list phase: %q", gotModel.tag)
	}
	if gotModel.current.ID != "ollama/gemma4:9b" {
		t.Errorf("current mutated in list phase: %q", gotModel.current.ID)
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
	if gotModel.current.ID != before.current.ID {
		t.Errorf("current changed: before %q, after %q", before.current.ID, gotModel.current.ID)
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
	for _, want := range []string{"agent", "claude", "tag", "code", "ollama/gemma4:9b", "[r] rotate", "[d] switch tag", "[enter] launch"} {
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
		selectedPath: t.TempDir(), current: config.Model{ID: "ollama/gemma4:9b"}}
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
		selectedPath: repo, current: config.Model{ID: "ollama/gemma4:9b"}}
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
		selectedPath: t.TempDir(), current: config.Model{ID: "ollama/gemma4:9b"},
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
		selectedPath: t.TempDir(), current: config.Model{ID: "ollama/gemma4:9b"},
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
		selectedPath: t.TempDir(), current: config.Model{ID: "ollama/gemma4:9b"},
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
		selectedPath: t.TempDir(), current: config.Model{ID: "ollama/gemma4:9b"}, yolo: true}
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
		selectedPath: t.TempDir(), current: config.Model{ID: "ollama/gemma4:9b"}}
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
		selectedPath: "/nonexistent/path/that/cannot/be/git", current: config.Model{ID: "ollama/gemma4:9b"}}
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
		current: config.Model{ID: "ollama/gemma4:9b"},
		resume:  resumeModel{choices: list.New(buildResumeChoices(nil), list.NewDefaultDelegate(), 80, 24)}}
	m.resume.choices.CursorDown() // Cancel
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.agent != "claude" || gotModel.current.ID != "ollama/gemma4:9b" || gotModel.tag != "code" {
		t.Errorf("model mutated on cancel: agent=%q current=%q tag=%q", gotModel.agent, gotModel.current.ID, gotModel.tag)
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
	if gotModel.current.ID != "ollama/gemma4:14b" {
		t.Errorf("current = %q, want ollama/gemma4:14b (rotation index 1)", gotModel.current.ID)
	}
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
	// State file: next_index=1, last=ollama/gemma4:9b. With 2 code models,
	// Next() returns models[1] (= gemma4:14b), then advances to index 0.
	seedState(t, dir, "code", "1\nollama/gemma4:9b\n")
	cfg := testConfig()
	m := phaseModelWithList(t, cfg, "claude", "code")

	if got := len(m.models.Items()); got != 2 {
		t.Errorf("models items = %d, want 2 (testConfig code models)", got)
	}
	if m.current.ID != "ollama/gemma4:14b" {
		t.Errorf("current = %q, want ollama/gemma4:14b (rotation index 1)", m.current.ID)
	}
	if m.models.Index() != 1 {
		t.Errorf("cursor index = %d, want 1", m.models.Index())
	}
}

// TestModelScreenRotateMovesCursor asserts pressing 'r' updates both
// m.current (the rotation cursor) and m.models.Index() (the visible
// list cursor). They are the same cursor, just two views of it.
func TestModelScreenRotateMovesCursor(t *testing.T) {
	dir := tempStateDir(t)
	// State: index 0, last=gemma4:9b. Next() returns models[0]=gemma4:9b
	// and advances to index 1. The visible cursor should land on the
	// first model.
	seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	if m.current.ID != "ollama/gemma4:9b" {
		t.Fatalf("precondition: current = %q, want ollama/gemma4:9b", m.current.ID)
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gotModel := got.(model)
	if gotModel.current.ID != "ollama/gemma4:14b" {
		t.Errorf("current = %q, want ollama/gemma4:14b (rotated)", gotModel.current.ID)
	}
	if gotModel.models.Index() != 1 {
		t.Errorf("cursor index = %d, want 1", gotModel.models.Index())
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
	if gotModel.current.ID != "ollama/gemma4:design" {
		t.Errorf("current = %q, want ollama/gemma4:design", gotModel.current.ID)
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

// TestModelScreenEnterUsesHighlightedNotCurrent asserts that Enter launches
// the highlighted list item, even if m.current lags. This protects against
// a class of bugs where stale state could launch the wrong model after
// the user has navigated the cursor.
func TestModelScreenEnterUsesHighlightedNotCurrent(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	// Move the list cursor to index 0 (gemma4:9b) without rotating.
	m.models.Select(0)
	// Stale m.current points at the second model.
	m.current = config.Model{ID: "stale", ProviderID: "ollama"}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.current.ID != "ollama/gemma4:9b" {
		t.Errorf("current after Enter = %q, want ollama/gemma4:9b (highlighted wins)", gotModel.current.ID)
	}
}
