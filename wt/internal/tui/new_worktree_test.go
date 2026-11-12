package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitprompt"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
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

// TestTypedNameDefersCreationToTheCommitPoint checks the "+ New worktree…"
// prompt's Enter is a pick, not a create: it resolves the plan read-only and
// binds the creation to the launch flow exactly like a bare-branch row, so
// nothing exists until the commit point says go. It matters because the old
// prompt-time `git worktree add` ran before any confirm — a decline after the
// prompt could not take the creation back.
func TestTypedNameDefersCreationToTheCommitPoint(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	m := model{cfg: &config.Config{}, phase: phaseNewWorktree, width: 80, height: 24, repoRoot: dir, newInput: newInputModel(80)}
	m.newInput.SetValue("my-feature")
	got, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.creation == nil || got.creation.name != "my-feature" {
		t.Fatalf("creation = %+v, want the typed name's plan", got.creation)
	}
	want := filepath.Join(dir, ".worktrees", "my-feature")
	if got.selectedPath != want {
		t.Errorf("selectedPath = %q, want %q", got.selectedPath, want)
	}
	if got.phase != phaseAgent {
		t.Errorf("phase = %v, want the launch flow's agent picker", got.phase)
	}
	if _, err := os.Stat(want); err == nil {
		t.Error("the prompt created the worktree; the commit point creates it")
	}
	if cmd != nil {
		t.Error("the prompt dispatched a create command; creation is the commit point's")
	}
}

// TestTypedNameCreationNeverAsksTheWFlagQuestion pins the L2 promise that
// the picker's "+ New worktree…" prompt creates a new name without the
// create-it? question `wt -W <new name>` asks: the person already typed the
// name on purpose. Stdin is at end-of-file, where any y/N question would
// answer no, so a worktree that still gets made — at the commit point, after
// the gate states its creation line — proves none was asked.
func TestTypedNameCreationNeverAsksTheWFlagQuestion(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	m := model{cfg: &config.Config{}, phase: phaseNewWorktree, width: 80, height: 24, repoRoot: dir, agent: "claude", newInput: newInputModel(80)}
	m.newInput.SetValue("typed-on-purpose")
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.creation == nil {
		t.Fatal("no creation bound for the typed name")
	}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	stderr := answerOnStdio(t, "") // EOF: a question here would read it as "no"
	if ok, gerr := m.launchGate(&tg, true); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); a typed name must create with no question", ok, gerr)
	}
	got := stderr()
	if strings.Contains(got, "continue?") {
		t.Errorf("gate asked the create question a typed name never needs: %q", got)
	}
	if !strings.Contains(got, "creating branch typed-on-purpose and worktree ") {
		t.Errorf("gate printed %q; want the frozen creation line for a new branch", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".worktrees", "typed-on-purpose")); err != nil {
		t.Errorf("worktree not created at the commit point: %v", err)
	}
}

// TestDeclinedConfirmAfterATypedNameLeavesNothing checks the deferral holds
// through a decline: a typed name the one question declines leaves no branch,
// no directory and no worktree registration — the confirm is the only thing
// that may make them. It matters because the prompt used to create first and
// never ask, so any later "no" had already spent the repository's state.
func TestDeclinedConfirmAfterATypedNameLeavesNothing(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	dir := t.TempDir()
	gitInit(t, dir)
	m := model{cfg: &config.Config{}, phase: phaseNewWorktree, width: 80, height: 24, repoRoot: dir, agent: "claude", newInput: newInputModel(80)}
	m.newInput.SetValue("fresh")
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.creation == nil {
		t.Fatal("no creation bound for the typed name")
	}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	// A holder forces the one question; "n" declines it.
	tg.Holders = []sessionclaim.Claim{{PID: 4242, Started: time.Now().Unix(), Path: dir, Branch: "main", Agent: "codex"}}
	answerOnStdio(t, "n")
	if ok, gerr := m.launchGate(&tg, true); ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); want a quiet no", ok, gerr)
	}
	if _, err := os.Stat(filepath.Join(dir, ".worktrees", "fresh")); err == nil {
		t.Error("worktree directory created after a declined confirm")
	}
	if out, err := exec.Command("git", "-C", dir, "show-ref", "--verify", "refs/heads/fresh").Output(); err == nil {
		t.Errorf("branch ref created after a declined confirm: %s", out)
	}
	out, _ := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if strings.Contains(string(out), "fresh") {
		t.Errorf("worktree registered after a declined confirm:\n%s", out)
	}
}

// TestTypedNameGitErrorStaysOnThePrompt asserts a name git would reject is
// refused inline at the prompt, before any launch or creation is bound:
// worktree.Resolve pre-rejects what Ensure would reject, so the person sees
// the error where they can fix it. The traversal name flattens to a harmless
// directory name and git rejects the branch name itself.
func TestTypedNameGitErrorStaysOnThePrompt(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	m := model{cfg: &config.Config{}, phase: phaseNewWorktree, width: 80, height: 24, repoRoot: dir, newInput: newInputModel(80)}
	m.newInput.SetValue("../evil")
	got, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.newError == "" {
		t.Error("newError = empty, want the git error surfaced at the prompt")
	}
	if got.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree (stays on error)", got.phase)
	}
	if got.creation != nil {
		t.Errorf("creation = %+v, want nil for a refused name", got.creation)
	}
	if cmd != nil {
		t.Error("a command was dispatched despite the error")
	}
}

// typedCreation resolves name the way the prompt's Enter does and wraps the
// plan in the creation the commit point ensures, so the record tests drive
// exactly the production object.
func typedCreation(t *testing.T, dir, name string) *launchCreation {
	t.Helper()
	plan, err := worktree.Resolve(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	return &launchCreation{repoRoot: dir, name: name, plan: plan}
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
// source of truth at the commit point (launchCreation.ensure): only a branch
// this launch creates is recorded. A typed new name records the branch it
// made; a typed existing name (a worktree for an existing branch), a reused
// worktree and a bare-branch row's owed creation are history this launch did
// not make, and record nothing.
func TestPickerCreationRoutesRecordOnlyACreatedBranch(t *testing.T) {
	t.Run("typed new name", func(t *testing.T) {
		exitprompt.Reset()
		t.Cleanup(exitprompt.Reset)
		dir := t.TempDir()
		gitInit(t, dir)
		c := typedCreation(t, dir, "fresh")
		path, err := c.ensure()
		if err != nil {
			t.Fatal(err)
		}
		rec, ok := exitprompt.Recorded()
		if !ok || rec.Path != path || rec.Branch != "fresh" || rec.Tip == "" {
			t.Errorf("Recorded() = %+v, %v; want fresh at %s", rec, ok, path)
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
		if _, err := typedCreation(t, dir, "topic").ensure(); err != nil {
			t.Fatal(err)
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
		if _, err := typedCreation(t, dir, "again").ensure(); err != nil {
			t.Fatal(err)
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
		if _, err := typedCreation(t, dir, "feature").ensure(); err != nil {
			t.Fatal(err)
		}
		if rec, ok := exitprompt.Recorded(); ok {
			t.Errorf("Recorded() = %+v; want nothing for a bare-branch row", rec)
		}
	})
}
