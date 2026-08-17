package tui

import (
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
	_, err := launchAgent("not-an-agent", config.Model{}, "/tmp", false, nil, nil)
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
		&session.Session{ID: "abc-123", MTime: time.Now()}, nil)
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
		&session.Session{ID: "proj-123.json", MTime: time.Now()}, nil)
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
	cmd, err := launchAgent("claude", config.Model{ID: "claude-sonnet"}, "/tmp/repo", false, nil, nil)
	if err != nil {
		t.Fatalf("launchAgent: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "--resume") || strings.Contains(got, "--session") {
		t.Errorf("args = %q, should not contain resume/session flags", got)
	}
}

// TestRunAndWaitCmdWiresStdio uses a no-op command (true) to verify that
// runAndWaitCmd wires Stdin/Stdout/Stderr and returns a launchDoneMsg.
func TestRunAndWaitCmdWiresStdio(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true not available")
	}
	cmd := exec.Command(truePath)
	msg := runAndWaitCmd(cmd)()
	done, ok := msg.(launchDoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	if done.err != nil {
		t.Errorf("true exited with error: %v", done.err)
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
	cmd, err := launchAgent("pi", m, "/tmp", false, nil, cfg)
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
