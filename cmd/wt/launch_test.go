package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

// TestDefaultAgentFromConfig asserts that when the config lists agents, the
// first one is the default. This is the non-TUI equivalent of the TUI's
// firstAgent: the launch path must pick a sensible agent without --agent.
func TestDefaultAgentFromConfig(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "codex"},
			{Name: "claude"},
		},
	}
	if got := defaultAgent(cfg); got != "codex" {
		t.Errorf("defaultAgent = %q, want %q", got, "codex")
	}
}

// TestDefaultAgentFallback asserts that an empty or nil config falls back to
// "claude". Without this, a first run with no config would launch nothing.
func TestDefaultAgentFallback(t *testing.T) {
	if got := defaultAgent(&config.Config{}); got != "claude" {
		t.Errorf("defaultAgent(empty) = %q, want %q", got, "claude")
	}
	if got := defaultAgent(nil); got != "claude" {
		t.Errorf("defaultAgent(nil) = %q, want %q", got, "claude")
	}
}

// TestDefaultModelPrefersNative asserts that an agent's native model
// (e.g. claude/native) is chosen over the first code-tagged model. This
// mirrors the bash wrappers' WT_DEFAULT_CODE="native:claude".
func TestDefaultModelPrefersNative(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Tags: []string{"code"}},
			{ID: "claude/native", ModelName: "native", Tags: []string{"code", "design"}},
		},
	}
	if got := defaultModel(cfg, "claude"); got.ID != "claude/native" {
		t.Errorf("defaultModel = %q, want %q", got.ID, "claude/native")
	}
}

// TestDefaultModelFallsBackToTag asserts that when an agent has no native
// model, the first model in the default tag group is used. This is the
// fallback for agents like pi that route through a provider.
func TestDefaultModelFallsBackToTag(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Tags: []string{"code"}},
		},
	}
	if got := defaultModel(cfg, "pi"); got.ID != "ollama/gemma4:9b" {
		t.Errorf("defaultModel = %q, want %q", got.ID, "ollama/gemma4:9b")
	}
}

// TestDefaultModelEmptyConfig asserts that an empty config yields a "(none)"
// placeholder rather than a zero-value model. This keeps the launch path from
// exec'ing an agent with an empty model id.
func TestDefaultModelEmptyConfig(t *testing.T) {
	cfg := &config.Config{DefaultTag: "code"}
	if got := defaultModel(cfg, "claude"); got.ID != "(none)" {
		t.Errorf("defaultModel = %q, want %q", got.ID, "(none)")
	}
}

// TestBuildLaunchUnknownAgent asserts that an unregistered agent returns a
// clear error rather than a nil command. Without this the launch path could
// dereference a nil driver.
func TestBuildLaunchUnknownAgent(t *testing.T) {
	_, err := buildLaunch("not-an-agent", config.Model{}, "/tmp", false, nil)
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("error = %q, want 'unknown agent'", err.Error())
	}
}

// TestBuildLaunchClaudeResume asserts that a claude launch with a session
// appends --resume <id>. This is the non-TUI resume wiring that the bash
// claude-wt wrapper used to do.
func TestBuildLaunchClaudeResume(t *testing.T) {
	cmd, err := buildLaunch("claude", config.Model{ID: "claude/native", ModelName: "native"}, "/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()})
	if err != nil {
		t.Fatalf("buildLaunch: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--resume abc-123") {
		t.Errorf("args = %q, want --resume abc-123", got)
	}
}

// TestBuildLaunchOpenCodeResume asserts that an opencode launch with a session
// appends --session <id>.
func TestBuildLaunchOpenCodeResume(t *testing.T) {
	cmd, err := buildLaunch("opencode", config.Model{ID: "ollama/gemma4:9b"}, "/tmp/repo", false,
		&session.Session{ID: "proj-123.json", MTime: time.Now()})
	if err != nil {
		t.Fatalf("buildLaunch: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--session proj-123.json") {
		t.Errorf("args = %q, want --session proj-123.json", got)
	}
}

// TestBuildLaunchNoSessionOmitsResume asserts that a nil session injects no
// resume/session flag. This is the "start fresh" path.
func TestBuildLaunchNoSessionOmitsResume(t *testing.T) {
	cmd, err := buildLaunch("claude", config.Model{ID: "claude/native", ModelName: "native"}, "/tmp/repo", false, nil)
	if err != nil {
		t.Fatalf("buildLaunch: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "--resume") || strings.Contains(got, "--session") {
		t.Errorf("args = %q, should not contain resume/session flags", got)
	}
}

// TestInGitRepoAt asserts that inGitRepoAt reports true inside a git repo and
// false outside one. This gates the passthrough path: outside a repo the
// agent launches directly with no picker, so a wrong answer here would either
// drop the user into the TUI or skip the picker unexpectedly.
func TestInGitRepoAt(t *testing.T) {
	dir := t.TempDir()
	if inGitRepoAt(dir) {
		t.Fatal("inGitRepoAt = true for a non-repo directory")
	}
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if !inGitRepoAt(dir) {
		t.Fatal("inGitRepoAt = false for a git repo")
	}
}
