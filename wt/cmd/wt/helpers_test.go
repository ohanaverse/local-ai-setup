package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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

// writeEmptyRegistry writes a minimal registry.toml (no
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

// withCleanConfigEnv sets XDG_CONFIG_HOME to a fixture path. All tests that
// exercise the launch path must call this before touching config.Load or
// RegistryPath(), so a test that does not name a registry of its own reads
// the fixture's file, not something the shell exported — this package's
// TestMain (config.IsolateConfigHomeForTest) clears WT_REGISTRY and
// MODELMAN_REGISTRY for the whole process, so no inherited name reaches it.
func withCleanConfigEnv(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

// writeConfiguredAgent writes config.toml with one agent entry (name,
// providerID) and a registry.toml with a matching provider and no models, so
// the agent is "configured" (has a config.toml entry) with zero eligible
// models — used by tests that need a configured model-driven agent whose
// model resolution still falls through to the picker, distinct from an
// unconfigured agent that falls through to passthrough.
func writeConfiguredAgent(t *testing.T, home, agentName, providerID string) {
	t.Helper()
	withCleanConfigEnv(t, home)
	cfgDir := filepath.Join(home, ".config", "agent-wt")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgToml := "default_tag = \"code\"\n[[agents]]\nname = \"" + agentName + "\"\nsupported_providers = [\"" + providerID + "\"]\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfgToml), 0o644); err != nil {
		t.Fatal(err)
	}
	regDir := filepath.Join(home, ".config", "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// migrateConfigSchema (runs on every Load once config.toml exists)
	// unconditionally ensures an "agy" agent referencing an "agy" provider
	// (see wt/CLAUDE.md's "Fixture gotcha" note) — any config.toml with
	// agents needs a matching agy provider in the registry or Validate
	// fails with "unknown provider \"agy\"".
	regToml := "[[providers]]\nid = \"" + providerID + "\"\nname = \"" + providerID + "\"\nlocation = \"local\"\nauth = { type = \"none\", base_url = \"http://localhost:11434\" }\n" +
		"[[providers]]\nid = \"agy\"\nname = \"agy\"\nlocation = \"cloud\"\nauth = { type = \"native\" }\n"
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"), []byte(regToml), 0o644); err != nil {
		t.Fatal(err)
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

// claudeStateDir is the directory claude keeps a project's transcripts in for
// a working directory, under home. It asks the driver rather than rebuilding
// the path, so a test cannot disagree with wt about where claude's state is.
// HOME must not be changed between this call and the code under test.
func claudeStateDir(t *testing.T, home, workdir string) string {
	t.Helper()
	sd, ok := agents.ByName("claude").(agents.StateDirer)
	if !ok {
		t.Fatal("the claude driver no longer reports a state directory")
	}
	t.Setenv("HOME", home)
	return sd.StateDir(workdir)
}

// TestConfigErrorHintNamesTheFileToFix pins the hint on a config error. A
// mistyped location is a registry problem, and registry.toml is not the
// file `wt config` saves: it writes wt's own config.toml and cannot repair a
// registry row, so telling the user to run it sent them to the wrong place
// (#200). Every other config error keeps the existing hint.
func TestConfigErrorHintNamesTheFileToFix(t *testing.T) {
	t.Setenv("WT_REGISTRY", "/tmp/somewhere/registry.toml")
	cfg := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "omlx", Location: "Local"}},
		Models:     []config.Model{{ID: "omlx/m", ProviderID: "omlx", ModelName: "m"}},
	}
	err := configError(cfg.Validate())
	if err == nil {
		t.Fatal("configError(nil-free validation error) = nil")
	}
	got := err.Error()
	for _, want := range []string{`config error: provider "omlx" has location "Local"; expected "local" or "cloud"`, "/tmp/somewhere/registry.toml"} {
		if !strings.Contains(got, want) {
			t.Errorf("error %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "wt config") {
		t.Errorf("error %q still sends the user to `wt config`, which cannot edit the registry", got)
	}
	if !errors.Is(err, config.ErrLocation) {
		t.Errorf("the wrapped error is lost: errors.Is(err, config.ErrLocation) = false")
	}

	other := configError(errors.New("default_tag must not be empty"))
	if got := other.Error(); got != "config error: default_tag must not be empty (run `wt config` to repair)" {
		t.Errorf("another config error = %q, want the existing hint unchanged", got)
	}
}

// TestConfigErrorForABrokenRegistryLink pins what the commands that refuse
// to run print when registry.toml is a symlink to a file that is not there:
// the link, its target, and a hint about the link — not "seed the registry"
// (there is one, behind the link) and not "run `wt config`" (which cannot
// repair a symlink).
func TestConfigErrorForABrokenRegistryLink(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	link := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "unmounted", "registry.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, loadErr := config.Load()
	if !errors.Is(loadErr, config.ErrRegistryLink) {
		t.Fatalf("Load error = %v, want config.ErrRegistryLink", loadErr)
	}
	got := configError(loadErr).Error()
	want := "config error: registry link is broken: " + link + " is a symlink to " + target +
		", which does not exist (fix the link or move it aside)"
	if got != want {
		t.Errorf("configError =\n  %q\nwant\n  %q", got, want)
	}
}

// TestConfigErrorForAMissingRegistryNamesModelInit pins the whole line a
// user reads when there is no registry: where it was looked for and the one
// command that creates it, named once. It used to name a command that no
// longer exists, and to say it twice.
func TestConfigErrorForAMissingRegistryNamesModelInit(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	_, loadErr := config.Load()
	if !errors.Is(loadErr, config.ErrRegistryMissing) {
		t.Fatalf("Load error = %v, want config.ErrRegistryMissing", loadErr)
	}
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	want := "config error: model registry not found at " + registry +
		" — seed it with `wt model init`"
	if got := configError(loadErr).Error(); got != want {
		t.Errorf("configError =\n  %q\nwant\n  %q", got, want)
	}
}
