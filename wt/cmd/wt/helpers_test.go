package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/guard"
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

// writeEmptyRegistry writes a minimal modelman-owned registry.toml (no
// providers/models) under $home/.config/local-ai/ so config.Load succeeds.
// wt fail-closes without this file; tests that exercise the launch path need
// it even when they don't care about specific models.
func writeEmptyRegistry(t *testing.T, home string) {
	t.Helper()
	withCleanConfigEnv(t, home)
	regDir := filepath.Join(home, ".config", "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"),
		[]byte("providers = []\nmodels = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withCleanConfigEnv sets XDG_CONFIG_HOME to a fixture path and clears any
// inherited MODELMAN_REGISTRY so RegistryPath() cannot short-circuit on the
// developer's shell environment. All tests that exercise the launch path must
// call this before touching config.Load or RegistryPath() — otherwise a stray
// `export MODELMAN_REGISTRY=...` in the dev's env makes the test read their
// real registry instead of the temp fixture.
func withCleanConfigEnv(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")
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

// TestPickerSkippedOutsideTTY confirms the root command returns the
// picker-needs-TTY error when stdin is not a terminal. Without this guard,
// `wt < /dev/null` (no -A) and other non-interactive invocations fail with
// Bubble Tea's opaque "could not open a new TTY" message — users see the
// failure but not the fix (add -A).
//
// The test swaps stdin to /dev/null, simulating the piped case, then asserts
// the resulting error mentions both TTY and -A so users know what to fix.
// The underlying term.IsTerminal check is exercised by the ioctl on the
// swapped fd; this test does not duplicate that micro-assertion.
func TestPickerSkippedOutsideTTY(t *testing.T) {
	// Run from a non-git directory so the RunE path that would open the
	// TUI is the outside-repo branch (the simplest one to trigger).
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeEmptyRegistry(t, home)

	// Pipe stdin from /dev/null so isStdinTTY returns false.
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open /dev/null: %v", err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	oldStdin := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() { os.Stdin = oldStdin })

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{}) // no -A, so picker path is taken
	err = root.Execute()
	if err == nil {
		t.Fatal("expected TTY-needs error, got nil")
	}
	if !strings.Contains(err.Error(), "TTY") {
		t.Errorf("error %q doesn't mention TTY", err.Error())
	}
	if !strings.Contains(err.Error(), "-A") {
		t.Errorf("error %q doesn't mention -A flag", err.Error())
	}
}
