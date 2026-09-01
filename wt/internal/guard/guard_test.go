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
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir()) // restore to a neutral dir

	status := Check()
	if status != NotInstalled {
		t.Fatalf("Check() = %v, want NotInstalled", status)
	}
}

// Check must report Installed when the pre-commit hook contains our marker.
// This is the signal the launcher uses to skip redundant installs.
func TestCheckInstalled(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir())

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
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir())

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
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir())

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
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir())

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
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir())

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
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir())

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
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir())

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
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir())

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
	dir := t.TempDir()
	gitInit(t, dir)
	os.Chdir(dir)
	defer os.Chdir(t.TempDir())

	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
}
