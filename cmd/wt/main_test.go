package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

// TestConfig_NoSubcommand_LaunchesEditor verifies that `wt config` with no
// args invokes the config editor TUI. We stub configeditorRun so the test
// does not require a TTY.
func TestConfig_NoSubcommand_LaunchesEditor(t *testing.T) {
	called := false
	old := configeditorRun
	configeditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error) error {
		called = true
		return nil
	}
	defer func() { configeditorRun = old }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"config"})
	err := root.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected configeditorRun to be called")
	}
}

// TestLegacyShortFlagRejected verifies that the old `-w` arity is captured
// and rejected with the migration message. Users coming from the bash
// engine expect `-w name`, `-wname`, and `-w=name` to all parse; if pflag
// treats them as unknown flags instead, the migration message never fires.
func TestLegacyShortFlagRejected(t *testing.T) {
	for _, args := range []struct {
		name string
		args []string
	}{
		{"space separated", []string{"-w", "my-branch"}},
		{"no space", []string{"-wmy-branch"}},
		{"equals", []string{"-w=my-branch"}},
	} {
		t.Run(args.name, func(t *testing.T) {
			var buf bytes.Buffer
			root := rootCmd()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(args.args)
			err := root.Execute()
			if err == nil {
				t.Fatal("expected error for legacy -w flag, got nil")
			}
			if !strings.Contains(err.Error(), "-w is removed") {
				t.Errorf("error %q does not contain migration message", err.Error())
			}
		})
	}
}

// TestPickerSkippedOnWorktreeFlag verifies that `wt -W foo` doesn't
// open the TUI. We can't easily test the TUI itself from a unit test;
// instead we verify the short-circuit path runs by checking that
// `wt -W foo -A claude` errors with "not in a git repo" or similar
// before any TUI is launched.
func TestPickerSkippedOnWorktreeFlag(t *testing.T) {
	// Run from a non-git directory.
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-W", "my-branch"})
	err := root.Execute()
	// Should error with "not in a git repo", not launch a TUI.
	if err == nil {
		t.Fatal("expected error for -W outside git repo")
	}
	if !strings.Contains(err.Error(), "git") {
		t.Errorf("error %q doesn't mention git", err.Error())
	}
}

// TestRemovedSubcommand_Rejected verifies that `wt models` and `wt agents`
// exit non-zero with a migration message pointing at `wt config`. Without
// this guard, cobra's ArbitraryArgs (required for shell-wt passthrough)
// silently swallows the unknown first positional and the root RunE
// creates a worktree literally named "models" or "agents" — a silent
// footgun for users with muscle-memory invocations or stale doc snippets.
func TestRemovedSubcommand_Rejected(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantInError string
	}{
		{"wt models", []string{"models"}, "wt models is removed"},
		{"wt models with flags", []string{"models", "-A", "claude"}, "wt models is removed"},
		{"wt agents", []string{"agents"}, "wt agents is removed"},
		{"wt agents with flags", []string{"agents", "-A", "codex"}, "wt agents is removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			root := rootCmd()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(tc.args)
			err := root.Execute()
			if err == nil {
				t.Fatalf("expected error for %v, got nil (output: %q)", tc.args, buf.String())
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantInError)
			}
			// Must point users at the replacement.
			if !strings.Contains(err.Error(), "wt config") {
				t.Errorf("error %q does not mention `wt config` as the replacement", err.Error())
			}
		})
	}
}

// TestShellPassthrough_StillWorks guards against an over-tightened Args
// validator regressing shell-wt. `wt --agent shell ls -la` (the form the
// shell-wt shim produces) must NOT be rejected by the new guard.
func TestShellPassthrough_StillWorks(t *testing.T) {
	// We only need to assert the new guard does not fire for "ls" — the
	// rest of the launch path will fail in this unit test environment
	// (no TTY, no config) but that's after the guard.
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--agent", "shell", "ls", "-la"})
	err := root.Execute()
	if err == nil {
		// Tolerated: would mean a full launch path ran (won't in this env).
		return
	}
	if strings.Contains(err.Error(), "is removed") {
		t.Fatalf("shell passthrough was incorrectly rejected as removed subcommand: %v", err)
	}
}
