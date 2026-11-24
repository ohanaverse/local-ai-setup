package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "user.email", "test@test").CombinedOutput(); err != nil {
		t.Fatalf("git config email: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "user.name", "Test").CombinedOutput(); err != nil {
		t.Fatalf("git config name: %v\n%s", err, out)
	}
}

// inRepo makes a throwaway git repository, changes into it, and returns its
// path. When the test ends the working directory is restored to a neutral
// temp dir, never to the package directory inside the real checkout.
func inRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir)
	neutral := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(neutral) })
	return dir
}

// CommonDir must return the absolute hooks directory inside a normal git repo
// so callers know where to read and write pre-commit hooks.
func TestCommonDir(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Change into the repo so git rev-parse works.
	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	got, err := CommonDir()
	if err != nil {
		t.Fatalf("CommonDir() error = %v", err)
	}
	// git rev-parse --git-common-dir returns a relative path when inside
	// the repo root; resolve it so the comparison works regardless.
	if !filepath.IsAbs(got) {
		got = filepath.Join(dir, got)
	}
	want := filepath.Join(dir, ".git")
	resolvedGot, _ := filepath.EvalSymlinks(got)
	resolvedWant, _ := filepath.EvalSymlinks(want)
	if resolvedGot != resolvedWant {
		t.Fatalf("CommonDir() = %q, want %q", got, want)
	}
}

// Check must report NotInstalled when no pre-commit hook exists at all.
// This is the baseline state for a fresh repo before the guard is ever
// installed.
func TestCheckNotInstalled(t *testing.T) {
	inRepo(t)

	status := Check()
	if status != NotInstalled {
		t.Fatalf("Check() = %v, want NotInstalled", status)
	}
}

// Check must report Installed when the pre-commit hook contains our marker.
// This is the signal the launcher uses to skip redundant installs.
func TestCheckInstalled(t *testing.T) {
	inRepo(t)

	if _, err := Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	status := Check()
	if status != Installed {
		t.Fatalf("Check() = %v, want Installed", status)
	}
}

// Check must report NotInstalled when a pre-commit hook exists but was not
// created by wt. We must never touch foreign hooks.
func TestCheckForeignHook(t *testing.T) {
	dir := inRepo(t)

	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho foreign\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	status := Check()
	if status != NotInstalled {
		t.Fatalf("Check() = %v, want NotInstalled for foreign hook", status)
	}
}

// Install must create a pre-commit hook when none exists and return changed
// true. Without this, the guard is never written to disk.
func TestInstallCreatesHook(t *testing.T) {
	dir := inRepo(t)

	changed, err := Install()
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if !changed {
		t.Fatal("Install() changed = false, want true")
	}

	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("hook not found: %v", err)
	}
	if !strings.Contains(string(data), marker) {
		t.Fatal("hook missing marker")
	}
}

// Install must be idempotent: a second call returns changed false so the
// launcher avoids redundant disk writes and misleading user output.
func TestInstallIdempotent(t *testing.T) {
	inRepo(t)

	first, err := Install()
	if err != nil {
		t.Fatalf("first Install() error = %v", err)
	}
	if !first {
		t.Fatal("first Install() changed = false, want true")
	}

	second, err := Install()
	if err != nil {
		t.Fatalf("second Install() error = %v", err)
	}
	if second {
		t.Fatal("second Install() changed = true, want false")
	}
}

// Install must preserve an existing foreign hook by appending it after our
// guard block. Losing the user's original hook would break their existing
// pre-commit workflows.
func TestInstallPreservesForeignHook(t *testing.T) {
	dir := inRepo(t)

	foreign := "#!/bin/sh\necho 'original hook'\n"
	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hookPath, []byte(foreign), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Install()
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), marker) {
		t.Fatal("hook missing our marker")
	}
	if !strings.Contains(string(data), "original hook") {
		t.Fatal("foreign hook was not preserved")
	}
}

// Uninstall must remove the hook entirely when we installed it and there was
// no previous foreign hook to restore. Leaving a stale guard behind would
// continue blocking commits after the user asked to remove it.
func TestUninstallRemovesHook(t *testing.T) {
	dir := inRepo(t)

	if _, err := Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}

	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatal("hook still exists after Uninstall")
	}

	status := Check()
	if status != NotInstalled {
		t.Fatalf("Check() = %v, want NotInstalled after Uninstall", status)
	}
}

// Uninstall must restore a preserved foreign hook to its original content.
// This ensures the user’s existing pre-commit checks survive a temporary
// wt guard installation.
func TestUninstallRestoresForeignHook(t *testing.T) {
	dir := inRepo(t)

	foreign := "#!/bin/sh\necho 'original hook'\n"
	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hookPath, []byte(foreign), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}

	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("hook missing after Uninstall: %v", err)
	}
	if string(data) != foreign {
		t.Fatalf("restored hook = %q, want %q", data, foreign)
	}
	if strings.Contains(string(data), marker) {
		t.Fatal("restored hook still contains marker")
	}
}

// Uninstall must be a no-op when the hook does not contain our marker. We
// must never delete a pre-commit hook we did not create.
func TestUninstallLeavesForeignHookAlone(t *testing.T) {
	dir := inRepo(t)

	foreign := "#!/bin/sh\necho 'foreign'\n"
	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hookPath, []byte(foreign), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}

	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("foreign hook deleted: %v", err)
	}
	if string(data) != foreign {
		t.Fatalf("foreign hook modified: %q", data)
	}
}

// Uninstall must succeed silently when no pre-commit hook exists at all.
// Running --no-guard on a repo that never had the guard should not error.
func TestUninstallNoHook(t *testing.T) {
	inRepo(t)

	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
}

// hookGit runs git in dir with extra environment and returns the combined
// output and the error.
func hookGit(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := hookGit(t, dir, nil, args...); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// primaryWithCommit makes a repository with one commit (made before the hook
// exists) and leaves the primary checkout on a branch named "work".
func primaryWithCommit(t *testing.T) string {
	t.Helper()
	dir := inRepo(t)
	mustGit(t, dir, "commit", "--allow-empty", "-m", "base")
	mustGit(t, dir, "checkout", "-b", "work")
	return dir
}

// v1 of the hook, as wt shipped it before the primary-checkout rule.
const v1Hook = `#!/usr/bin/env bash
# block-main-commit v1
# Installed by wt. Blocks commits to main/master unless explicitly bypassed.
set -euo pipefail
branch="$(git symbolic-ref --short HEAD 2>/dev/null || true)"
case "$branch" in
    main|master)
        if [ "${WT_SKIP_MAIN_BLOCK:-0}" = "1" ]; then
            exit 0
        fi
        echo "block-main-commit: commits to '$branch' are blocked."
        exit 1
        ;;
esac
exit 0
`

// A commit on a named branch in the primary checkout is blocked, and the
// message names the two commands that fix it (#560).
func TestHookBlocksNamedBranchInPrimary(t *testing.T) {
	dir := primaryWithCommit(t)
	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	out, err := hookGit(t, dir, nil, "commit", "--allow-empty", "-m", "x")
	if err == nil {
		t.Fatalf("commit on a named branch in the primary checkout succeeded:\n%s", out)
	}
	for _, want := range []string{"wt restore-root", "wt -W <name>"} {
		if !strings.Contains(out, want) {
			t.Errorf("message lacks %q:\n%s", want, out)
		}
	}
}

// The bypasses still work for the primary-checkout rule.
func TestHookPrimaryRuleBypass(t *testing.T) {
	dir := primaryWithCommit(t)
	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	if out, err := hookGit(t, dir, []string{"WT_SKIP_MAIN_BLOCK=1"}, "commit", "--allow-empty", "-m", "x"); err != nil {
		t.Fatalf("WT_SKIP_MAIN_BLOCK=1 did not bypass: %v\n%s", err, out)
	}
	if out, err := hookGit(t, dir, nil, "commit", "--no-verify", "--allow-empty", "-m", "y"); err != nil {
		t.Fatalf("--no-verify did not bypass: %v\n%s", err, out)
	}
}

// A detached HEAD in the primary checkout is allowed.
func TestHookAllowsDetachedInPrimary(t *testing.T) {
	dir := primaryWithCommit(t)
	mustGit(t, dir, "checkout", "--detach")
	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	if out, err := hookGit(t, dir, nil, "commit", "--allow-empty", "-m", "x"); err != nil {
		t.Fatalf("detached commit blocked: %v\n%s", err, out)
	}
}

// A linked worktree on a named branch is where work belongs: allowed. The
// same hook (in the common dir) still blocks the primary checkout.
func TestHookAllowsLinkedWorktree(t *testing.T) {
	dir := primaryWithCommit(t)
	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(t.TempDir(), "linked")
	mustGit(t, dir, "worktree", "add", "-b", "feature", wtDir)
	if out, err := hookGit(t, wtDir, nil, "commit", "--allow-empty", "-m", "x"); err != nil {
		t.Fatalf("commit in a linked worktree blocked: %v\n%s", err, out)
	}
	if _, err := hookGit(t, dir, nil, "commit", "--allow-empty", "-m", "y"); err == nil {
		t.Fatal("primary checkout commit allowed beside a linked worktree")
	}
}

// The main/master rule is unchanged, in a linked worktree too.
func TestHookStillBlocksMainInLinkedWorktree(t *testing.T) {
	dir := primaryWithCommit(t)
	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(t.TempDir(), "linked")
	mustGit(t, dir, "worktree", "add", "-B", "main", wtDir) // -B: "main" may exist
	out, err := hookGit(t, wtDir, nil, "commit", "--allow-empty", "-m", "x")
	if err == nil || !strings.Contains(out, "commits to 'main' are blocked") {
		t.Fatalf("main in a linked worktree not blocked: %v\n%s", err, out)
	}
}

// A v1 hook still blocks main, so Check calls it Installed; Install upgrades
// it in place (changed true), keeping a preserved original hook.
func TestInstallUpgradesV1(t *testing.T) {
	dir := inRepo(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	v1 := v1Hook + "\n# (preserved original pre-commit hook)\n#!/bin/sh\necho 'original hook'\n"
	if err := os.WriteFile(hookPath, []byte(v1), 0o755); err != nil {
		t.Fatal(err)
	}
	if Check() != Installed {
		t.Fatal("Check() on a v1 hook is not Installed")
	}
	changed, err := Install()
	if err != nil || !changed {
		t.Fatalf("Install() = %v, %v; want true, nil", changed, err)
	}
	data, _ := os.ReadFile(hookPath)
	s := string(data)
	if !strings.Contains(s, marker) || strings.Contains(s, "block-main-commit v1") {
		t.Fatalf("hook not upgraded to v2:\n%s", s)
	}
	if !strings.Contains(s, "echo 'original hook'") || strings.Count(s, "preserved original pre-commit hook") != 1 {
		t.Fatalf("preserved tail lost or duplicated:\n%s", s)
	}
	if again, _ := Install(); again {
		t.Fatal("second Install() changed the v2 hook")
	}
}

// A v1 hook with no preserved tail upgrades to exactly the v2 hook.
func TestInstallUpgradesBareV1(t *testing.T) {
	dir := inRepo(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hookPath, []byte(v1Hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if changed, err := Install(); err != nil || !changed {
		t.Fatalf("Install() = %v, %v", changed, err)
	}
	data, _ := os.ReadFile(hookPath)
	if string(data) != hookSrc {
		t.Fatalf("bare v1 not replaced by the v2 hook:\n%s", data)
	}
}

// Uninstall finds a v1 marker and restores its preserved tail.
func TestUninstallV1(t *testing.T) {
	dir := inRepo(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	foreign := "#!/bin/sh\necho 'original hook'\n"
	if err := os.WriteFile(hookPath, []byte(v1Hook+"\n# (preserved original pre-commit hook)\n"+foreign), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(hookPath)
	if string(data) != foreign {
		t.Fatalf("restored = %q", data)
	}
}
