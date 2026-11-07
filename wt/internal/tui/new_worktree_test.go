package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitprompt"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
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

// TestEnsureNewWorktreeCmdNeverAsksTheWFlagQuestion pins the L2 promise that
// the picker's "+ New worktree…" prompt creates a new name without the
// create-it? question `wt -W <new name>` asks: the person already typed the
// name on purpose. Stdin is at end-of-file, where any y/N question would
// answer no, so a worktree that still gets created proves none was asked.
func TestEnsureNewWorktreeCmdNeverAsksTheWFlagQuestion(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	saved := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() { os.Stdin = saved })

	dir := t.TempDir()
	gitInit(t, dir)

	got, ok := ensureNewWorktreeCmd(dir, "typed-on-purpose")().(newWorktreeCreatedMsg)
	if !ok || got.err != nil {
		t.Fatalf("ensureNewWorktreeCmd = %+v, ok=%v; want a created worktree with no error", got, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, ".worktrees", "typed-on-purpose")); err != nil {
		t.Errorf("worktree not created: %v", err)
	}
}

// TestEnsureNewWorktreeCmdGitError asserts that git errors are
// surfaced to the caller rather than swallowed. The traversal
// name flattens to a harmless directory name and git rejects the
// branch name itself; either way the error must surface to the
// caller.
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

// TestEnsureBranchWorktreeCmdSuccess is the happy-path integration test for
// the bare-branch path. In a fresh temp git repo, the command must create a
// worktree for an existing local branch and emit a branchWorktreeCreatedMsg
// with err == nil and a path under .worktrees/. A regression here means
// selecting a bare branch silently does nothing.
func TestEnsureBranchWorktreeCmdSuccess(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Create a feature branch without checking it out, so EnsureForBranch
	// takes the local-branch path and git worktree add can check it out.
	if out, err := exec.Command("git", "-C", dir, "branch", "feature").CombinedOutput(); err != nil {
		t.Fatalf("git branch feature: %v\n%s", err, out)
	}

	cmd := ensureBranchWorktreeCmd(dir, "feature")
	msg := cmd()
	got, ok := msg.(branchWorktreeCreatedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want branchWorktreeCreatedMsg", msg)
	}
	if got.err != nil {
		t.Fatalf("err = %v, want nil", got.err)
	}
	if got.branch != "feature" {
		t.Errorf("branch = %q, want feature", got.branch)
	}
	wantPath := filepath.Join(dir, ".worktrees", "feature")
	if got.path != wantPath {
		t.Errorf("path = %q, want %q", got.path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("worktree not created at %q: %v", wantPath, err)
	}
}

// gitInit is a test helper that creates a fresh git repo in dir.
// Duplicated from internal/worktree/create_test.go to keep this
// package's tests self-contained.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	// Pin the initial branch to "main" so the repo does not depend on the
	// runner's git init.defaultBranch (CI's may be "master"). A test that
	// then queries refs/heads/main (TestPRTagDroppedWhenTheBranchMoved)
	// must find it. Matches internal/worktree/enumerate_test.go.
	if err := exec.Command("git", "init", "-b", "main", dir).Run(); err != nil {
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

// TestPickerCreationRoutesRecordOnlyACreatedBranch checks the exit prompt's
// source of truth on the picker: a typed new name records the branch it made,
// while a typed existing name, an existing worktree and a bare-branch row
// (EnsureForBranch, history this launch did not make) record nothing.
func TestPickerCreationRoutesRecordOnlyACreatedBranch(t *testing.T) {
	t.Run("typed new name", func(t *testing.T) {
		exitprompt.Reset()
		t.Cleanup(exitprompt.Reset)
		dir := t.TempDir()
		gitInit(t, dir)
		got, ok := ensureNewWorktreeCmd(dir, "fresh")().(newWorktreeCreatedMsg)
		if !ok || got.err != nil {
			t.Fatalf("msg = %+v, ok=%v", got, ok)
		}
		rec, ok := exitprompt.Recorded()
		if !ok || rec.Path != got.path || rec.Branch != "fresh" || rec.Tip == "" {
			t.Errorf("Recorded() = %+v, %v; want fresh at %s", rec, ok, got.path)
		}
	})
	t.Run("typed existing branch", func(t *testing.T) {
		exitprompt.Reset()
		t.Cleanup(exitprompt.Reset)
		dir := t.TempDir()
		gitInit(t, dir)
		if out, err := exec.Command("git", "-C", dir, "branch", "topic").CombinedOutput(); err != nil {
			t.Fatalf("git branch: %v\n%s", err, out)
		}
		if got, ok := ensureNewWorktreeCmd(dir, "topic")().(newWorktreeCreatedMsg); !ok || got.err != nil {
			t.Fatalf("msg = %+v, ok=%v", got, ok)
		}
		if rec, ok := exitprompt.Recorded(); ok {
			t.Errorf("Recorded() = %+v; want nothing for an existing branch", rec)
		}
	})
	t.Run("typed existing worktree", func(t *testing.T) {
		exitprompt.Reset()
		t.Cleanup(exitprompt.Reset)
		dir := t.TempDir()
		gitInit(t, dir)
		if _, err := worktree.EnsureForName(dir, "again"); err != nil {
			t.Fatal(err)
		}
		if got, ok := ensureNewWorktreeCmd(dir, "again")().(newWorktreeCreatedMsg); !ok || got.err != nil {
			t.Fatalf("msg = %+v, ok=%v", got, ok)
		}
		if rec, ok := exitprompt.Recorded(); ok {
			t.Errorf("Recorded() = %+v; want nothing for a reused worktree", rec)
		}
	})
	t.Run("bare branch row", func(t *testing.T) {
		exitprompt.Reset()
		t.Cleanup(exitprompt.Reset)
		dir := t.TempDir()
		gitInit(t, dir)
		if out, err := exec.Command("git", "-C", dir, "branch", "feature").CombinedOutput(); err != nil {
			t.Fatalf("git branch: %v\n%s", err, out)
		}
		if got, ok := ensureBranchWorktreeCmd(dir, "feature")().(branchWorktreeCreatedMsg); !ok || got.err != nil {
			t.Fatalf("msg = %+v, ok=%v", got, ok)
		}
		if rec, ok := exitprompt.Recorded(); ok {
			t.Errorf("Recorded() = %+v; want nothing for a bare-branch row", rec)
		}
	})
}
