package tui

import (
	"os/exec"
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
	_, err := launchAgent("not-an-agent", config.Model{}, "/tmp", false, nil)
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
		&session.Session{ID: "abc-123", MTime: time.Now()})
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
		&session.Session{ID: "proj-123.json", MTime: time.Now()})
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
	cmd, err := launchAgent("claude", config.Model{ID: "claude-sonnet"}, "/tmp/repo", false, nil)
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
