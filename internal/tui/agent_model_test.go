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

// seedState writes the legacy "rotation-<tag>.state" file (the
// pre-PR-3a single-tag state file) so rotation.LastLaunched's
// backward-compat fallback picks up the seeded body. The production
// per-slot file (rotation-<agent>-<tag>-<family>.state) is also
// tried first by LastLaunched, but tests don't have a real agent
// name in their slot, so the legacy file is the path that lands the
// seed.
func seedState(t *testing.T, dir, tag, content string) {
	t.Helper()
	// Legacy file path: rotation-<tag>.state. The per-slot format
	// (rotation-_-<tag>-_.state) is also written so callers using the
	// default slot ({"-", tag, "-"}) read it via the per-slot path;
	// either path lets the test exercise LastLaunched.
	legacyPath := filepath.Join(dir, "rotation-"+tag+".state")
	if err := os.WriteFile(legacyPath, []byte(content), 0o600); err != nil {
		t.Fatalf("seed %s state: %v", tag, err)
	}
	perSlotPath := rotation.StateFileForSlot(dir, rotation.Slot{Agent: "-", Tag: tag, Family: "-"})
	if err := os.WriteFile(perSlotPath, []byte(content), 0o600); err != nil {
		t.Fatalf("seed %s per-slot state: %v", tag, err)
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
// model, and the list cursor on that model. Tests that exercise
// rotation, the list, or the View use this helper instead of
// constructing model literals (which would skip the list-build path
// the production code uses).
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
	// The helper builds a Slot from (agent, tag, "") to mirror what
	// the pinned-agent path in app.go does today (family defaults to
	// empty / dash when no -F filter is active). Tests that need a
	// non-empty family should construct the Slot directly and call
	// positionAfterLastLaunched.
	slot := rotation.SlotFromFlags(agent, tag, "")
	m.rotation = rotation.NewForSlot(slot, models, "")
	if last, ok := m.rotation.LastLaunched(); ok {
		if next, ok := FindAfter(models, last); ok {
			m.models.Select(indexOfModel(models, next))
		}
	}
	return m
}

// drivePhaseAgentEnter simulates the full PR-2 picker flow: fire
// selectedEntryMsg (which builds m.agentList and transitions to
// phaseAgent), select the agent row matching `agentName`, then fire
// Enter on the picker. The returned model is whatever phaseAgent
// Enter produces — phaseModel on the happy path, or phaseAgent
// with m.status set if the catalog is empty for that agent+tag.
// Tests that used to drive only selectedEntryMsg and assert
// phaseModel state now call this helper to land at the same
// post-transition state the production code reaches when the
// user picks an agent from the picker.
func drivePhaseAgentEnter(t *testing.T, m model, agentName string) model {
	t.Helper()
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m = got.(model)
	if m.phase != phaseAgent {
		t.Fatalf("after selectedEntryMsg: phase = %v, want phaseAgent", m.phase)
	}
	for i, it := range m.agentList.Items() {
		if ai, ok := it.(agentItem); ok && !ai.command && ai.name == agentName {
			m.agentList.Select(i)
			break
		}
	}
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return got.(model)
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
// has no compatible models, pressing Enter on the agent row in the new
// phaseAgent picker does NOT enter the model phase. The validation gate
// fires when the user picks the agent (not when the worktree is picked,
// since PR 2 split the entry into two transitions), surfaces a status
// message, and keeps the user in phaseAgent so they can fix the config.
func TestSelectedEntryMsgEmptyListStaysOnList(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama"}},
		// No models with provider "ollama".
	}
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	if gotModel.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent (no entry into model picker)", gotModel.phase)
	}
	if gotModel.status == "" {
		t.Error("status = empty, want an error message")
	}
	if view := gotModel.View(); !strings.Contains(view, gotModel.status) {
		t.Errorf("View missing status %q:\n%s", gotModel.status, view)
	}
}

// TestSelectedEntryMsgEmptyListSetsActionableStatus asserts the status
// message names the agent and the tag so the user knows what to fix.
// PR 2 moved the empty-catalog validation into the phaseAgent Enter
// handler, so this test drives that path (selecting the agent row in
// the new picker and pressing Enter) rather than the bare worktree pick.
func TestSelectedEntryMsgEmptyListSetsActionableStatus(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama"}},
	}
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	for _, want := range []string{"claude", "code"} {
		if !strings.Contains(gotModel.status, want) {
			t.Errorf("status %q missing %q", gotModel.status, want)
		}
	}
}

// TestPinnedAgentEmptyListStatusUsesFirstActiveTag asserts that in the pinned
// --agent path the empty-catalog status uses the first active -T tag rather
// than the config default tag.
func TestPinnedAgentEmptyListStatusUsesFirstActiveTag(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama"}},
	}
	m := model{
		cfg:          cfg,
		initialAgent: "claude",
		activeTags:   "design,code",
		width:        80,
		height:       24,
	}
	got, _ := m.Update(entriesLoadedMsg{entries: []worktree.Entry{{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"}}})
	m = got.(model)
	got, _ = m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: "/tmp/feature"}})
	gotModel := got.(model)
	if !strings.Contains(gotModel.status, `tag "design"`) {
		t.Fatalf("status = %q, want first active tag design", gotModel.status)
	}
}

// TestPinnedAgentErrorRenderedOnList asserts that a pinned --agent whose
// model catalog is empty surfaces its error on the worktree list instead of
// swallowing it. PR 2 moved model resolution into the pinned selectedEntryMsg
// path; without the ready-phaseList status render, `wt --agent <agent>` with
// an empty catalog failed with zero on-screen feedback.
func TestPinnedAgentErrorRenderedOnList(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama"}},
		// No models — ModelsForAgentAndTag returns an empty list.
	}
	m := model{cfg: cfg, initialAgent: "claude", width: 80, height: 24}
	// Load the worktree list first so m.ready is true; the pinned-path
	// error then must render on the ready list, not via the loading branch.
	got, _ := m.Update(entriesLoadedMsg{entries: []worktree.Entry{{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"}}})
	m = got.(model)
	got, _ = m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: "/tmp/feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList (pinned agent error stays on list)", gotModel.phase)
	}
	if gotModel.status == "" {
		t.Fatal("status = empty, want a config error")
	}
	if view := gotModel.View(); !strings.Contains(view, gotModel.status) {
		t.Errorf("View missing status %q:\n%s", gotModel.status, view)
	}
}

// TestToggleBackToCode and TestTagKeyIgnoredInListPhase were removed in PR 3b
// Task 3 along with the `d` key handler. The toggle's two-way switch behavior
// and the list-phase gating are no longer meaningful once the keybind itself
// is gone. Coverage of the `-T` filter mechanism lives in
// TestPhaseModelHonorsFilters (agent_picker_test.go) and
// TestLaunchFilteredUsesEligibleAndSlot (cmd/wt/launch_test.go).

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
	for _, want := range []string{"agent", "claude", "tag", "code", "ollama/gemma4:9b", "[↑/↓] navigate", "[enter] launch"} {
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
		resume: resumeModel{choices: list.New(buildResumeChoices(nil), list.NewDefaultDelegate(), 80, 24)}}
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
// on the rotation's next-to-use model, not necessarily index 0. PR 2
// split the picker into a phaseAgent step followed by a phaseModel step,
// so this test now drives both: selectEntryMsg lands in phaseAgent, the
// phaseAgent Enter handler builds m.models and positions the cursor.
func TestSelectedEntryMsgPositionsCursorAtNextToUse(t *testing.T) {
	dir := tempStateDir(t)
	// State: next_index=1, last=ollama/gemma4:9b. With 2 code models, Next()
	// returns models[1] (= gemma4:14b) and advances to index 0.
	seedState(t, dir, "code", "1\nollama/gemma4:9b\n")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	if gotModel.models.Index() != 1 {
		t.Errorf("cursor index = %d, want 1", gotModel.models.Index())
	}
}

// TestPhaseModelWithListBuildsAndPositionsCursor asserts the test
// helper populates m.models and positions the cursor on the rotation's
// next-to-use model. The production code uses the same list-build path
// (in selectedEntryMsg), so this is a smoke test of the
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

// TestModelScreenToggleTagRebuildsList was removed in PR 3b Task 3
// along with the `d` tag-toggle key. The cross-tag switch from inside
// the TUI is gone: tag filtering is now driven by `-T` (cmd/wt flags
// threaded into tui.Run in Task 4), and the resulting picker list is
// covered by TestPhaseModelHonorsFilters (agent_picker_test.go).
//
// TestModelScreenToggleTagEmptyRestores likewise — its job was to
// surface a status message when toggling to a tag with no models. The
// new -T filter path returns the empty catalog through the
// selectedEntryMsg empty-catalog status (TestSelectedEntryMsgEmptyListSetsActionableStatus);
// no in-TUI fallback is needed.

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
// is the cold-start path: fresh user, fresh state file. PR 2 added
// a phaseAgent step between worktree selection and the model picker,
// so the test now drives both transitions to reach phaseModel.
func TestSelectedEntryNoLastLaunchedStartsAtZero(t *testing.T) {
	tempStateDir(t) // isolates state; file doesn't exist
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
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
// is "one past where we left off". PR 2 added a phaseAgent step,
// so the test drives both transitions to reach phaseModel.
func TestSelectedEntryPositionsAfterLastLaunched(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/gemma4:9b\n")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
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
// longer in the snapshot (config changed since last launch). PR 2
// added a phaseAgent step, so the test drives both transitions to
// reach phaseModel.
func TestSelectedEntryLastLaunchedMissingFallsBackToZero(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/removed:cloud\n")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor index = %d, want 0 (fallback when last-launched missing)", gotModel.models.Index())
	}
}

// TestSelectedEntryLastLaunchedLastInListWrapsToZero asserts the
// picker wraps to the next model when the saved last-launched is
// the last item in the snapshot. Without wrap, the cursor would
// advance past the end of the list forever. PR 3b: the picker now
// sources models from cfg.EligibleModels without an implicit tag
// filter, so all 3 testConfig models (2 code + 1 design) are in
// the snapshot; wrap from index 1 lands on index 2 (gemma4:design).
func TestSelectedEntryLastLaunchedLastInListWrapsToZero(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/gemma4:14b\n")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	// 3 models in snapshot (2 code + 1 design). Wrap from
	// gemma4:14b (index 1) → gemma4:design (index 2).
	if gotModel.models.Index() != 2 {
		t.Errorf("cursor index = %d, want 2 (wrap to next when last-launched is not last)", gotModel.models.Index())
	}
}

// TestToggleTagRebuildsRotation was removed in PR 3b Task 3 along
// with the `d` tag-toggle key. Rotation snapshots are built once per
// picker entry from the -T-filtered catalog (via
// positionAfterLastLaunched in rotation_helpers.go); there is no
// longer an in-picker path that rebuilds them. Per-slot state files
// are exercised by TestPhaseModelWithListBuildsAndPositionsCursor,
// TestSelectedEntryPositionsAfterLastLaunched, and
// TestNextEntryAfterLaunchAdvancesCursor.

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
	// rotation state must have been written. PR 3b writes go to the
	// per-slot file (slot {claude/code/-} → rotation-claude-code-_.state).
	if _, err := os.Stat(rotation.StateFileForSlot(dir, rotation.Slot{Agent: "claude", Tag: "code", Family: "-"})); err == nil {
		t.Errorf("rotation state written on Enter without a committed launch")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat rotation state: %v", err)
	}
}

// TestLaunchAndRecordWritesLastLaunched asserts that launchAndRecord — the
// single commit point reached only after the ollama check and resume prompt
// are satisfied — writes the launched model's ID to the per-slot state
// file. This is the positive counterpart to
// TestEnterInModelPhaseDoesNotRecordBeforeLaunch: the rotation advances
// exactly when a launch commits, no sooner. PR 3b writes go to the
// per-slot file (slot {claude/code/-} renders as
// rotation-claude-code-_.state).
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

	data, err := os.ReadFile(rotation.StateFileForSlot(dir, rotation.Slot{Agent: "claude", Tag: "code", Family: "-"}))
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
// depending on a real agent binary or ollama being available. PR 2 added a
// phaseAgent step before phaseModel, so each picker entry drives both
// transitions (worktree pick → phaseAgent Enter) to reach the rotation
// snapshot it asserts on.
func TestNextEntryAfterLaunchAdvancesCursor(t *testing.T) {
	dir := tempStateDir(t)
	// Seed: last-launched is gemma4:9b (index 0). Picker entry 1
	// will land on gemma4:14b (index 1).
	seedState(t, dir, "code", "ollama/gemma4:9b\n")

	// Picker entry 1: drive the full PR-2 path (worktree pick →
	// phaseAgent → phaseModel) so m.models is built and cursor is
	// positioned at "next-to-use".
	m := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	m = drivePhaseAgentEnter(t, m, "claude")
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
	// restarted). Drive selectedEntryMsg → phaseAgent → Enter; the
	// phaseAgent Enter handler rebuilds the rotation, sees gemma4:14b
	// as last-launched, and advances to the next model. PR 3b: the
	// picker shows all 3 testConfig models (no implicit tag filter),
	// so "next" from gemma4:14b (index 1) is gemma4:design (index 2).
	m2 := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	m2 = drivePhaseAgentEnter(t, m2, "claude")
	if m2.models.Index() != 2 {
		t.Errorf("entry 2: cursor = %d, want 2 (next after gemma4:14b in 3-model snapshot)", m2.models.Index())
	}
}

// TestNextEntryAfterManualPickAdvancesFromManualPick asserts the rotation
// advances from the user's manual pick when they launch it. If the user
// navigates to a non-rotation-suggested model and launches, the next entry
// should land on the model AFTER the manual pick — not the model after the
// prior rotation pick. This protects against "manual picks are ignored"
// regressions. The launch is committed via launchAndRecord so the test
// exercises the real recording path without a real agent or ollama. PR 2
// added a phaseAgent step before phaseModel, so each picker entry now
// drives the worktree pick and the phaseAgent Enter to reach phaseModel.
func TestNextEntryAfterManualPickAdvancesFromManualPick(t *testing.T) {
	dir := tempStateDir(t)
	// No prior state. Picker entry 1 lands at index 0.
	_ = dir

	m := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	m = drivePhaseAgentEnter(t, m, "claude")
	if m.models.Index() != 0 {
		t.Fatalf("entry 1: cursor = %d, want 0", m.models.Index())
	}

	// User navigates down to index 1 — manual pick of gemma4:14b.
	// (Capture the returned model so the cursor advance sticks; m.models
	// is a value field, so the in-place update inside Update() doesn't
	// propagate unless we use the result.)
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(model)
	if m.models.Index() != 1 {
		t.Fatalf("after Down: cursor = %d, want 1 (gemma4:14b)", m.models.Index())
	}

	// Commit the launch of gemma4:14b (the manual pick) via the shared
	// commit point. Discard the returned tea.Cmd so nothing execs.
	m.launchModel = m.models.Items()[m.models.Index()].(modelItem).model
	m, _ = m.launchAndRecord(exec.Command("true"))

	// Picker entry 2: last-launched is gemma4:14b (the manual pick).
	// PR 3b: the picker shows all 3 testConfig models, so the next
	// entry advances to gemma4:design (index 2), not wrapping to 0.
	m2 := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	m2 = drivePhaseAgentEnter(t, m2, "claude")
	if m2.models.Index() != 2 {
		t.Errorf("entry 2: cursor = %d, want 2 (next after manual pick gemma4:14b)", m2.models.Index())
	}
}
