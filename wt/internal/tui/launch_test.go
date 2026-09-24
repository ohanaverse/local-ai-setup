package tui

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
)

// TestLaunchAgentUnknownAgent asserts that asking for an unregistered agent
// returns a clear error. Without this guard the TUI could try to exec a
// nil driver.
func TestLaunchAgentUnknownAgent(t *testing.T) {
	_, err := launchAgent("not-an-agent", config.Model{}, "/tmp", false, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("error = %q, want 'unknown agent'", err.Error())
	}
}

// TestLaunchAgentClaudeResumeAppendsFlag asserts that a claude launch with
// a session appends --resume <id> to the command args. This is the resume
// wiring that the bash wrappers do for claude.
func TestLaunchAgentClaudeResumeAppendsFlag(t *testing.T) {
	requireBinary(t, "claude")
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "claude", Auth: config.AuthConfig{Type: "native"}}},
	}
	cmd, err := launchAgent("claude", config.Model{ID: "claude-sonnet", ProviderID: "claude"}, "/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()}, cfg, nil)
	if err != nil {
		t.Fatalf("launchAgent: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--resume abc-123") {
		t.Errorf("args = %q, want --resume abc-123", got)
	}
}

// TestLaunchAgentOpenCodeResumeAppendsFlag asserts that an opencode launch
// with a session appends --session <id> to the command args.
func TestLaunchAgentOpenCodeResumeAppendsFlag(t *testing.T) {
	requireBinary(t, "opencode")
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
	}
	cmd, err := launchAgent("opencode", config.Model{ID: "ollama/gemma4:9b"}, "/tmp/repo", false,
		&session.Session{ID: "proj-123.json", MTime: time.Now()}, cfg, nil)
	if err != nil {
		t.Fatalf("launchAgent: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--session proj-123.json") {
		t.Errorf("args = %q, want --session proj-123.json", got)
	}
}

// TestLaunchAgentWithoutSessionOmitsResumeFlag asserts that when no session
// is passed, no resume/session flag is injected. This is the "start fresh"
// path.
func TestLaunchAgentWithoutSessionOmitsResumeFlag(t *testing.T) {
	requireBinary(t, "claude")
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "claude", Auth: config.AuthConfig{Type: "native"}}},
	}
	cmd, err := launchAgent("claude", config.Model{ID: "claude-sonnet", ProviderID: "claude"}, "/tmp/repo", false, nil, cfg, nil)
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
	cmd, err := launchAgent("pi", m, "/tmp", false, nil, cfg, nil)
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
