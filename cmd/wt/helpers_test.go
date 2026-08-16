package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/guard"
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

// TestMaybeInstallGuardInRepo installs the guard in a temp repo and verifies
// that a subsequent Check reports Installed. Without this, the launcher would
// silently skip guard protection on normal launches.
func TestMaybeInstallGuardInRepo(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	maybeInstallGuard()

	if guard.Check() != guard.Installed {
		t.Fatal("expected guard installed after maybeInstallGuard")
	}
}

// TestMaybeInstallGuardOutsideRepo does nothing and does not error when not
// inside a git repo. The passthrough path must remain safe outside version
// control.
func TestMaybeInstallGuardOutsideRepo(t *testing.T) {
	oldWd, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(oldWd)

	maybeInstallGuard() // should not panic or print fatal error
}

// TestMaybeInstallGuardIsIdempotent calls the helper twice in the same repo;
// the second call must not error or leave a broken hook.
func TestMaybeInstallGuardIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	maybeInstallGuard()
	maybeInstallGuard()

	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("hook missing after idempotent install: %v", err)
	}
}

// TestCheckGuardStatusInstalled reports Installed when the guard has been
// installed in the current repo.
func TestCheckGuardStatusInstalled(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	if _, err := guard.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}

	status, err := checkGuardStatus()
	if err != nil {
		t.Fatalf("checkGuardStatus: %v", err)
	}
	if status != guard.Installed {
		t.Fatalf("status = %v, want Installed", status)
	}
}

// TestCheckGuardStatusNotInstalled reports NotInstalled for a fresh repo.
func TestCheckGuardStatusNotInstalled(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	status, err := checkGuardStatus()
	if err != nil {
		t.Fatalf("checkGuardStatus: %v", err)
	}
	if status != guard.NotInstalled {
		t.Fatalf("status = %v, want NotInstalled", status)
	}
}

// TestRemoveGuardUninstalls the guard so --no-guard can restore the original
// hook.
func TestRemoveGuardUninstalls(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	if _, err := guard.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := removeGuard(); err != nil {
		t.Fatalf("removeGuard: %v", err)
	}
	if guard.Check() != guard.NotInstalled {
		t.Fatal("expected guard removed")
	}
}

// TestGuardHelpersOutsideRepoError returns an error when not in a git repo so
// the flags cannot be misused outside version control.
func TestGuardHelpersOutsideRepoError(t *testing.T) {
	oldWd, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(oldWd)

	if _, err := checkGuardStatus(); err == nil {
		t.Fatal("expected error outside repo for checkGuardStatus")
	}
	if err := removeGuard(); err == nil {
		t.Fatal("expected error outside repo for removeGuard")
	}
}
