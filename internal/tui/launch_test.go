package tui

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
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
	cmd, err := launchAgent("claude", config.Model{ID: "claude-sonnet"}, "/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()}, nil, nil)
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
	cmd, err := launchAgent("opencode", config.Model{ID: "ollama/gemma4:9b"}, "/tmp/repo", false,
		&session.Session{ID: "proj-123.json", MTime: time.Now()}, nil, nil)
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
	cmd, err := launchAgent("claude", config.Model{ID: "claude-sonnet"}, "/tmp/repo", false, nil, nil, nil)
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
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
	}}
	m := config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}
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
		if !strings.Contains(got, "--model deepseek-v4-pro:cloud") {
			t.Errorf("args = %q, want --model deepseek-v4-pro:cloud (sync + verify)", got)
		}
	}
}
