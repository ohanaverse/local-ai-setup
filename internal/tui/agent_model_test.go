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

// TestFirstModelPlaceholder asserts firstModel returns the "(none)" sentinel
// for a nil config or an empty tag group. The agent+model screen must render
// something rather than a zero-value Model with a blank ID.
func TestFirstModelPlaceholder(t *testing.T) {
	cases := []*config.Config{
		nil,
		{DefaultTag: "code", Models: []config.Model{
			{ID: "m", Tags: []string{"design"}}, // only design, not code
		}},
	}
	for _, cfg := range cases {
		m := firstModel(cfg, "code")
		if m.ID != "(none)" {
			t.Errorf("firstModel = %q, want (none)", m.ID)
		}
		if m.Location != config.LocationCloud {
			t.Errorf("placeholder location = %q, want cloud", m.Location)
		}
	}
}

// TestFirstModelPicksFirstInTag asserts firstModel returns the first model
// whose tags include the given tag, ignoring models tagged otherwise.
func TestFirstModelPicksFirstInTag(t *testing.T) {
	cfg := &config.Config{Models: []config.Model{
		{ID: "design-first", Tags: []string{"design"}},
		{ID: "code-first", Tags: []string{"code"}},
	}}
	if got := firstModel(cfg, "code"); got.ID != "code-first" {
		t.Errorf("firstModel(code) = %q, want code-first", got.ID)
	}
	if got := firstModel(cfg, "design"); got.ID != "design-first" {
		t.Errorf("firstModel(design) = %q, want design-first", got.ID)
	}
}

// TestSelectedEntryMsgNoModelsShowsPlaceholder asserts that picking a
// worktree when the active tag group is empty still lands on the model phase
// with a "(none)" model rather than panicking or hanging. The user sees a
// fallback screen even with a sparse catalog.
func TestSelectedEntryMsgNoModelsShowsPlaceholder(t *testing.T) {
	m := model{cfg: &config.Config{DefaultTag: "code", Agents: []config.Agent{{Name: "claude"}}}}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	if gotModel.current.ID != "(none)" {
		t.Errorf("current = %q, want (none)", gotModel.current.ID)
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

	m := model{cfg: cfg, phase: phaseModel, tag: "code", otherTag: "design",
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
func TestToggleBackToCode(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel, tag: "code", otherTag: "",
		current: config.Model{ID: "ollama/gemma4:9b"}}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	got, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.tag != "code" || gotModel.otherTag != "design" {
		t.Errorf("tag/otherTag = (%q, %q), want (code, design)", gotModel.tag, gotModel.otherTag)
	}
	if gotModel.current.ID != "ollama/gemma4:9b" {
		t.Errorf("current = %q, want first code model", gotModel.current.ID)
	}
}

// TestModelAndTagKeysIgnoredInListPhase asserts 'm' and 'd' do nothing while
// on the worktree list, matching how 'r' is gated. The agent+model keybinds
// must not fire before a worktree is chosen.
func TestModelAndTagKeysIgnoredInListPhase(t *testing.T) {
	for _, key := range []rune{'m', 'd'} {
		m := model{cfg: testConfig(), phase: phaseList, tag: "code",
			current: config.Model{ID: "ollama/gemma4:9b"}}
		got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		gotModel := got.(model)
		if gotModel.status != "" {
			t.Errorf("key %q: status mutated in list phase: %q", string(key), gotModel.status)
		}
		if gotModel.tag != "code" {
			t.Errorf("key %q: tag mutated in list phase: %q", string(key), gotModel.tag)
		}
		if gotModel.current.ID != "ollama/gemma4:9b" {
			t.Errorf("key %q: current mutated in list phase: %q", string(key), gotModel.current.ID)
		}
	}
}

// TestViewModelPlaceholder asserts the model-phase View still renders a
// coherent screen when the current model is the "(none)" placeholder. This
// keeps the fallback catalog path from producing a blank screen.
func TestViewModelPlaceholder(t *testing.T) {
	m := model{phase: phaseModel, agent: "claude", tag: "code",
		current: config.Model{ID: "(none)", ProviderID: "", Location: config.LocationCloud}}
	view := m.View()
	for _, want := range []string{"agent", "claude", "(none)", "tag"} {
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
