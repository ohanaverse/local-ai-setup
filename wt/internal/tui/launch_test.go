package tui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestLaunchAgentUnknownAgent asserts that asking for an unregistered agent
// returns a clear error. Without this guard the TUI could try to exec a
// nil driver.
func TestLaunchAgentUnknownAgent(t *testing.T) {
	_, err := launchAgent("not-an-agent", config.Model{}, "/tmp", false, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("error = %q, want 'unknown agent'", err.Error())
	}
}

// TestLaunchAgentNeverAddsAResumeFlag pins that the picker's launch starts
// the agent fresh: wt adds no resume or session flag of its own. Continuing a
// conversation is done with the agent's own flags after `--`.
func TestLaunchAgentNeverAddsAResumeFlag(t *testing.T) {
	stubFakeBinary(t, "claude")
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "claude", Auth: config.AuthConfig{Type: "native"}}},
	}
	cmd, err := launchAgent("claude", config.Model{ID: "claude-sonnet", ProviderID: "claude"}, "/tmp/repo", false, cfg, nil)
	if err != nil {
		t.Fatalf("launchAgent: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "--resume") || strings.Contains(got, "--session") {
		t.Errorf("args = %q, should not contain resume/session flags", got)
	}
}

// TestRunAndWaitCmdWiresStdio uses a no-op command (true) to verify
// that runAndWaitCmd wires Stdin/Stdout/Stderr and returns a
// launchDoneMsg. Without stdio wiring, the agent would see no input
// and write to /dev/null — a regression here would silently break
// every interactive agent invocation while passing all the picker
// tests above.
func TestRunAndWaitCmdWiresStdio(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true not available")
	}
	cmd := exec.Command(truePath)
	msg := runAndWaitCmd(cmd, "shell", config.Model{})()
	done, ok := msg.(launchDoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	if done.err != nil {
		t.Errorf("true exited with error: %v", done.err)
	}
}

// TestRunAndWaitCmdCapturesSummaryOnSuccess asserts the TUI launch path
// captures the summary line via pendingSummary rather than printing it
// directly. The previous design called fmt.Println inside the closure,
// which landed inside the alt-screen buffer that bubbletea discards at
// tea.Quit shutdown — so the user never saw the summary. Without this
// test, a regression that re-introduced a direct fmt.Println here
// would silently break the user-visible "wt: agent · model · 1s"
// line on every TUI launch.
func TestRunAndWaitCmdCapturesSummaryOnSuccess(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	prev := pendingSummary
	pendingSummary = ""
	t.Cleanup(func() { pendingSummary = prev })

	cmd := exec.Command(truePath)
	msg := runAndWaitCmd(cmd, "claude", config.Model{ID: "claude/sonnet"})()
	if _, ok := msg.(launchDoneMsg); !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	w.Close()
	out, _ := io.ReadAll(r)

	// runAndWaitCmd must NOT print the summary itself — that goes into
	// the alt-screen buffer, which is discarded. It must populate the
	// package variable instead.
	if strings.Contains(string(out), "wt: claude · claude/sonnet ·") {
		t.Errorf("runAndWaitCmd printed to stdout directly; summary must be captured into pendingSummary, not printed. stdout = %q", string(out))
	}
	if !strings.Contains(pendingSummary, "wt: claude · claude/sonnet ·") {
		t.Errorf("pendingSummary = %q, want it to contain the formatted line", pendingSummary)
	}
}

// TestRunAndWaitCmdCapturesSummaryOnFailure asserts the summary is
// captured even when the subprocess exits non-zero. The post-run line
// is supposed to fire on every exit, success or failure — a regression
// that only emitted on success would hide what just failed.
func TestRunAndWaitCmdCapturesSummaryOnFailure(t *testing.T) {
	falsePath, err := exec.LookPath("false")
	if err != nil {
		t.Skip("`false` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	prev := pendingSummary
	pendingSummary = ""
	t.Cleanup(func() { pendingSummary = prev })

	cmd := exec.Command(falsePath)
	msg := runAndWaitCmd(cmd, "shell", config.Model{})()
	done, ok := msg.(launchDoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	if done.err == nil {
		t.Fatal("expected error from `false`")
	}
	w.Close()
	out, _ := io.ReadAll(r)

	if strings.Contains(string(out), "wt: shell ·") {
		t.Errorf("runAndWaitCmd printed to stdout directly on failure; summary must be captured. stdout = %q", string(out))
	}
	if !strings.Contains(pendingSummary, "wt: shell ·") {
		t.Errorf("pendingSummary = %q, want it to contain the formatted line on failure", pendingSummary)
	}
}

// TestRunAndWaitCmdCapturesPendingSurvey verifies runAndWaitCmd stashes the
// agent/model into pendingSurveyState next to pendingSummary, so Run() can
// invoke the post-session survey after the alt-screen tears down — the
// same capture-then-emit pattern the summary line uses.
func TestRunAndWaitCmdCapturesPendingSurvey(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	prev := pendingSurveyState
	pendingSurveyState = pendingSurvey{}
	t.Cleanup(func() { pendingSurveyState = prev })

	cmd := exec.Command(truePath)
	msg := runAndWaitCmd(cmd, "claude", config.Model{ID: "claude/sonnet"})()
	if _, ok := msg.(launchDoneMsg); !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	if pendingSurveyState.agent != "claude" || pendingSurveyState.m.ID != "claude/sonnet" {
		t.Fatalf("pendingSurveyState = %+v, want agent=claude model=claude/sonnet", pendingSurveyState)
	}
}

// launchAgent must invoke the pi driver's SyncModels before building the
// command, mirroring the non-TUI path. Without it, the TUI launch would fall
// back to pi's default model for a rotation-selected model. The sync runs
// before LookPath, so it is observable even when pi is not installed.
func TestLaunchAgentSyncsPi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(dir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"ollama":{"models":[]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
		Models: []config.Model{
			{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud", ProviderID: "ollama"},
		},
	}
	m := config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud", ProviderID: "ollama"}
	cmd, err := launchAgent("pi", m, "/tmp", false, cfg, nil)
	if err != nil && !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("launchAgent: %v", err)
	}
	data, _ := os.ReadFile(modelsPath)
	if !strings.Contains(string(data), "deepseek-v4-pro:cloud") {
		t.Errorf("models.json = %s, want it to contain deepseek-v4-pro:cloud (sync ran)", string(data))
	}
	if err == nil {
		got := strings.Join(cmd.Args, " ")
		if !strings.Contains(got, "--model ollama/deepseek-v4-pro:cloud") {
			t.Errorf("args = %q, want --model ollama/deepseek-v4-pro:cloud (sync + verify)", got)
		}
	}
}

// TestRunAndWaitCmdAppliesProfileApplier is the regression lock for the
// code-review finding that runAndWaitCmd (the interactive picker's launch
// path) never applied local-model launch profiles at all. It stubs
// profileApplier to record the (cmd, agent, m) it was called with and
// returns a cleanup that records whether it ran; both must fire around
// cmd.Run(), exactly mirroring the non-TUI path's runAgentCmd.
func TestRunAndWaitCmdAppliesProfileApplier(t *testing.T) {
	oldApplier := profileApplier
	t.Cleanup(func() { profileApplier = oldApplier })

	var gotAgent string
	var gotModel config.Model
	cleanupCalled := false
	profileApplier = func(cmd *exec.Cmd, agent string, m config.Model) (func() error, error) {
		gotAgent, gotModel = agent, m
		cmd.Env = append(cmd.Env, "WT_TEST_PROFILE_APPLIED=1")
		return func() error { cleanupCalled = true; return nil }, nil
	}

	cmd := exec.Command("true")
	m := config.Model{ID: "ollama/x", ModelName: "x"}
	msg := runAndWaitCmd(cmd, "claude", m)()

	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a successful launchDoneMsg", msg)
	}
	if gotAgent != "claude" || gotModel.ID != "ollama/x" {
		t.Errorf("profileApplier called with (%q, %+v), want (claude, {ID: ollama/x})", gotAgent, gotModel)
	}
	found := false
	for _, e := range cmd.Env {
		if e == "WT_TEST_PROFILE_APPLIED=1" {
			found = true
		}
	}
	if !found {
		t.Error("profileApplier's cmd.Env mutation did not survive into cmd.Run()")
	}
	if !cleanupCalled {
		t.Error("profileApplier's cleanup was not called after cmd.Run()")
	}
}

// TestRunAndWaitCmdProfileApplierErrorSkipsRun is the regression lock for
// the TUI-side equivalent of the non-TUI runAgentCmd fix (plan item #5): a
// pre-launch profileApplier error must skip cmd.Run() entirely, still
// release the refcount entry already recorded by launchAndRecord before
// runAndWaitCmd was returned, and still populate pendingSummary (duration
// 0) and print the error to stderr (the TUI never surfaces
// launchDoneMsg's error, so without it the user sees no diagnostic at all)
// — but must NOT populate pendingSurveyState, since nothing ran and there
// is no session to survey or offer a stop picker for.
func TestRunAndWaitCmdProfileApplierErrorSkipsRun(t *testing.T) {
	oldApplier := profileApplier
	t.Cleanup(func() { profileApplier = oldApplier })
	oldRelease := releaseSession
	t.Cleanup(func() { releaseSession = oldRelease })
	pendingSummary, pendingSurveyState = "", pendingSurvey{}
	t.Cleanup(func() { pendingSummary, pendingSurveyState = "", pendingSurvey{} })

	released := false
	releaseSession = func() { released = true }
	profileApplier = func(cmd *exec.Cmd, agent string, m config.Model) (func() error, error) {
		return nil, errors.New("wrapper not installed")
	}

	sentinelDir := t.TempDir()
	sentinel := filepath.Join(sentinelDir, "should-not-exist")
	cmd := exec.Command("touch", sentinel)
	m := config.Model{ID: "ollama/x", ModelName: "x"}

	// Capture stderr: the Update handler never renders launchDoneMsg's
	// error and Run() does not return it, so this stderr line is the
	// user's only diagnostic for a launch that never ran.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	msg := runAndWaitCmd(cmd, "pi", m)()
	os.Stderr = oldStderr
	_ = w.Close()
	stderrOut, _ := io.ReadAll(r)

	done, ok := msg.(launchDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a launchDoneMsg carrying the profileApplier error", msg)
	}
	if !strings.Contains(string(stderrOut), "wt: wrapper not installed") {
		t.Errorf("stderr = %q, want it to contain %q — a profileApplier error must be visible to the user", stderrOut, "wt: wrapper not installed")
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Error("sentinel file exists — cmd.Run() executed despite the profileApplier error")
	}
	if !released {
		t.Error("releaseSession() was not called on a profileApplier error")
	}
	if pendingSummary == "" {
		t.Error("pendingSummary was not set on a profileApplier error")
	}
	if pendingSurveyState.agent != "" {
		t.Error("pendingSurveyState was populated despite the profileApplier error — nothing ran, there is no session to survey")
	}
}

// TestRunAndWaitCmdNilProfileApplierIsNoop verifies a nil profileApplier
// (its zero value — the state every other test in this file runs under,
// since only Run() ever sets it) leaves runAndWaitCmd's behavior exactly
// as it was before this task, so the profile layer is opt-in via Run's new
// parameter and never a behavior change for a caller that doesn't wire it.
func TestRunAndWaitCmdNilProfileApplierIsNoop(t *testing.T) {
	oldApplier := profileApplier
	profileApplier = nil
	t.Cleanup(func() { profileApplier = oldApplier })

	cmd := exec.Command("true")
	msg := runAndWaitCmd(cmd, "shell", config.Model{})()
	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a successful launchDoneMsg", msg)
	}
}

// relativeArgTUIFixture mirrors the report behind the relative-argument note:
// the command is typed in <repo>/wt, the agent starts in <repo>, and
// "../../other" names a sibling project only from the first. It installs a
// fake `claude`, points the shell-directory seam at <repo>/wt, and clears the
// pending notes.
func relativeArgTUIFixture(t *testing.T) (launch, other string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other = filepath.Join(root, "other")
	launch = filepath.Join(root, "repo")
	shell := filepath.Join(launch, "wt")
	for _, d := range []string{other, shell} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldDir, oldNotes := shellDir, pendingRouteNotes
	shellDir = func() (string, error) { return shell, nil }
	pendingRouteNotes = ""
	t.Cleanup(func() { shellDir, pendingRouteNotes = oldDir, oldNotes })
	return launch, other
}

// TestLaunchAgentQueuesTheRelativePathNote pins the picker's half of the
// relative-argument note. The picker owns the terminal while it builds the
// launch, so a note printed then would be lost under the alt screen; it is
// queued with the other pending notes and printed when the terminal is
// released, above the agent's own output. The argument is passed on
// unchanged.
func TestLaunchAgentQueuesTheRelativePathNote(t *testing.T) {
	launch, other := relativeArgTUIFixture(t)
	native := config.Model{ID: "claude/native", ProviderID: "claude", ModelName: "native", Native: true}
	cmd, err := launchAgent("claude", native, launch, false, nil, []string{"../../other", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Args, "../../other") {
		t.Errorf("args = %v, want the relative argument passed through unchanged", cmd.Args)
	}
	for _, want := range []string{`wt: note: "../../other" is a relative path`, "claude starts in " + launch, other} {
		if !strings.Contains(pendingRouteNotes, want) {
			t.Errorf("pending notes %q lack %q", pendingRouteNotes, want)
		}
	}
	if n := strings.Count(pendingRouteNotes, "wt: note:"); n != 1 {
		t.Errorf("pending notes = %q, want exactly one", pendingRouteNotes)
	}
	// A launch that failed and is retried builds the command again; the note
	// must not pile up.
	if _, err := launchAgent("claude", native, launch, false, nil, []string{"../../other", "--verbose"}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(pendingRouteNotes, "wt: note:"); n != 1 {
		t.Errorf("after a second build pending notes = %q, want still one", pendingRouteNotes)
	}
	var printed bytes.Buffer
	flushRouteNotes(&printed)
	if !strings.Contains(printed.String(), other) || pendingRouteNotes != "" {
		t.Errorf("flushed %q, left %q; want the note printed once and the queue empty", printed.String(), pendingRouteNotes)
	}
}

// TestPassthroughLaunchQueuesTheRelativePathNote pins the same for an agent
// with no config entry, which the picker launches through launchPassthrough.
func TestPassthroughLaunchQueuesTheRelativePathNote(t *testing.T) {
	launch, other := relativeArgTUIFixture(t)
	m := model{selectedPath: launch, extraArgs: []string{"../../other"}}
	if got, _ := m.launchPassthrough("claude"); strings.Contains(got.status, "launch failed") {
		t.Fatalf("status = %q, want the launch built", got.status)
	}
	if !strings.Contains(pendingRouteNotes, `"../../other" is a relative path`) || !strings.Contains(pendingRouteNotes, other) {
		t.Fatalf("pending notes = %q, want the relative-path note naming %s", pendingRouteNotes, other)
	}
}

// TestLaunchAgentQueuesNothingWithoutAMistakenPath pins that an ordinary
// picker launch leaves the queue empty.
func TestLaunchAgentQueuesNothingWithoutAMistakenPath(t *testing.T) {
	launch, _ := relativeArgTUIFixture(t)
	native := config.Model{ID: "claude/native", ProviderID: "claude", ModelName: "native", Native: true}
	for _, args := range [][]string{nil, {"run", "say hi"}, {"--verbose"}} {
		if _, err := launchAgent("claude", native, launch, false, nil, args); err != nil {
			t.Fatal(err)
		}
	}
	if pendingRouteNotes != "" {
		t.Fatalf("pending notes = %q, want none", pendingRouteNotes)
	}
}

// TestPickerLaunchesStraightThroughAPriorSession pins that the picker no
// longer stops to ask about an earlier session (#198, #204). It used to look
// one up for the worktree and show a "Resume previous session?" prompt; wt
// now leaves sessions to the agent, so Enter on a model launches it — here
// with a prior claude session on disk for the directory, which is what used
// to raise the prompt — and the launch carries no resume flag.
func TestPickerLaunchesStraightThroughAPriorSession(t *testing.T) {
	stubFakeBinary(t, "claude")
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	launch, home := filepath.Join(root, "repo"), filepath.Join(root, "home")
	t.Setenv("HOME", home)
	sd, ok := agents.ByName("claude").(agents.StateDirer)
	if !ok {
		t.Fatal("the claude driver no longer reports a state directory")
	}
	projects := sd.StateDir(launch)
	for _, d := range []string{launch, projects} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projects, "0000aaaa-prior-session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	native := config.Model{ID: "claude/native", ProviderID: "claude", ModelName: "native", Native: true}
	m := model{cfg: &config.Config{}, phase: phaseModel, agent: "claude", tag: "code", selectedPath: launch, width: 80, height: 24, models: singleModelList(native)}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next := got.(model)

	if cmd == nil {
		t.Fatalf("no launch command: phase = %v status = %q", next.phase, next.status)
	}
	if next.phase != phaseModel || strings.Contains(next.status, "launch failed") {
		t.Fatalf("phase = %v status = %q, want the launch to go straight ahead", next.phase, next.status)
	}
	if view := next.View(); strings.Contains(view, "Resume") {
		t.Fatalf("the picker still shows a resume prompt:\n%s", view)
	}
	built, err := launchAgent("claude", native, launch, false, &config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range built.Args {
		if a == "--resume" || a == "--continue" || strings.Contains(a, "prior-session") {
			t.Fatalf("launch args %v carry a resume flag wt added", built.Args)
		}
	}
}
