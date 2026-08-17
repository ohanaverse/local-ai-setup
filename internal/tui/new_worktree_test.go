package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestValidateNewWorktreeNameRejectsEmpty asserts the validator
// rejects the empty string. An empty worktree name would create a
// malformed .worktrees/ directory; the TUI must surface this as a
// user-facing error rather than calling git.
func TestValidateNewWorktreeNameRejectsEmpty(t *testing.T) {
	if got := validateNewWorktreeName(""); got == "" {
		t.Error("validateNewWorktreeName(\"\") returned empty error, want non-empty")
	}
}

// TestValidateNewWorktreeNameRejectsWhitespace asserts pure
// whitespace is also rejected. Trim-then-check matches the same
// contract — git would reject this, but the TUI should pre-empt.
func TestValidateNewWorktreeNameRejectsWhitespace(t *testing.T) {
	if got := validateNewWorktreeName("   "); got == "" {
		t.Error("validateNewWorktreeName(\"   \") returned empty error, want non-empty")
	}
}

// TestValidateNewWorktreeNameRejectsDot asserts the validator rejects
// "." and "..". Via filepath.Join these would resolve to the repo root
// (".worktrees/.." cleans to dir), so EnsureForName would silently
// return the repo root and create nothing — the prompt must surface an
// error instead.
func TestValidateNewWorktreeNameRejectsDot(t *testing.T) {
	for _, name := range []string{".", ".."} {
		if got := validateNewWorktreeName(name); got == "" {
			t.Errorf("validateNewWorktreeName(%q) returned empty error, want non-empty", name)
		}
	}
}

// TestValidateNewWorktreeNameAcceptsValid asserts the validator
// does not over-restrict. Slashed names like "feature/x" are valid
// branch names and EnsureForName handles them; the TUI's validator
// must defer to that without pre-rejecting.
func TestValidateNewWorktreeNameAcceptsValid(t *testing.T) {
	cases := []string{"my-feature", "feature/x", "a", "release-1.0"}
	for _, name := range cases {
		if got := validateNewWorktreeName(name); got != "" {
			t.Errorf("validateNewWorktreeName(%q) = %q, want empty", name, got)
		}
	}
}

// TestNewInputModelIsFocused asserts the input is focused on
// creation. An unfocused input would require an extra tab/click,
// breaking the muscle-memory contract that Enter submits.
func TestNewInputModelIsFocused(t *testing.T) {
	ti := newInputModel(80)
	if !ti.Focused() {
		t.Error("newInputModel(80).Focused() = false, want true")
	}
}

// TestNewInputModelHasPlaceholder asserts the placeholder matches
// the contract constant. Without a placeholder the prompt is a
// blank rectangle and users have no hint what to type.
func TestNewInputModelHasPlaceholder(t *testing.T) {
	ti := newInputModel(80)
	if ti.Placeholder != newWorktreePlaceholder {
		t.Errorf("Placeholder = %q, want %q", ti.Placeholder, newWorktreePlaceholder)
	}
}

// TestNewInputModelWidth asserts the input width fits the terminal.
// An over-wide input would wrap awkwardly; a too-narrow one would
// hide long branch names. The contract is width - 4 (room for
// padding/borders).
func TestNewInputModelWidth(t *testing.T) {
	ti := newInputModel(80)
	if ti.Width != 76 {
		t.Errorf("Width = %d, want 76", ti.Width)
	}
}

// TestEnsureNewWorktreeCmdSuccess is the happy-path integration
// test. In a fresh temp git repo, the command must create a
// worktree and emit a newWorktreeCreatedMsg with err == nil and
// a path under .worktrees/. A regression here means the feature
// silently does nothing.
func TestEnsureNewWorktreeCmdSuccess(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := ensureNewWorktreeCmd(dir, "fresh-feature")
	msg := cmd()
	got, ok := msg.(newWorktreeCreatedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want newWorktreeCreatedMsg", msg)
	}
	if got.err != nil {
		t.Fatalf("err = %v, want nil", got.err)
	}
	if got.name != "fresh-feature" {
		t.Errorf("name = %q, want fresh-feature", got.name)
	}
	wantPath := filepath.Join(dir, ".worktrees", "fresh-feature")
	if got.path != wantPath {
		t.Errorf("path = %q, want %q", got.path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("worktree not created at %q: %v", wantPath, err)
	}
}

// TestEnsureNewWorktreeCmdGitError asserts that git errors are
// surfaced to the caller rather than swallowed. The traversal
// name is rejected (by EnsureForName's path-separator guard or by
// git itself, depending on filepath.Base). Either path must
// surface an error to the caller.
func TestEnsureNewWorktreeCmdGitError(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := ensureNewWorktreeCmd(dir, "../evil")
	msg := cmd()
	got, ok := msg.(newWorktreeCreatedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want newWorktreeCreatedMsg", msg)
	}
	if got.err == nil {
		t.Fatal("err = nil, want non-nil for traversal name")
	}
}

// gitInit is a test helper that creates a fresh git repo in dir.
// Duplicated from internal/worktree/create_test.go to keep this
// package's tests self-contained.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// Set identity so commits/worktree-add don't fail.
	for _, kv := range [][]string{
		{"user.email", "t@e"},
		{"user.name", "t"},
		{"commit.gpgsign", "false"},
		{"tag.gpgsign", "false"},
	} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v\n%s", kv[0], err, out)
		}
	}
	// Make an initial commit so the branch is created with a HEAD.
	c := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "init")
	if out, err := c.CombinedOutput(); err != nil {
		// Some git versions need user config before any commit; the
		// git config above should cover that. If it still fails
		// the test setup is broken, not the code under test.
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}
