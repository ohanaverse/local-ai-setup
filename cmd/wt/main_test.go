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
