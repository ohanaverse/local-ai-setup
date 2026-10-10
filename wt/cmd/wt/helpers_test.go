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
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
)

// gitLocationEnv names the variables that tell git which repository, work
// tree, index, object store or command-line config to use: the ones git
// itself drops before it runs a command in another repository
// (`git rev-parse --local-env-vars`), and GIT_NAMESPACE. A git hook exports
// several of them to whatever it runs, `go test` included.
var gitLocationEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_PREFIX",
	"GIT_NAMESPACE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE",
	"GIT_SHALLOW_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE",
	"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
}

// isolateGit makes git, for the rest of the test, find a repository only
// where the test put one. Every test that runs git in a temp directory, or
// that depends on a temp directory not being a repository, calls it (gitInit
// does, for the tests that build a repository).
//
// Two things on the developer's machine otherwise decide what git finds:
//
//   - an exported GIT_DIR (tests run from a git hook) names a repository
//     whatever the working directory is. Every temp directory is then "inside
//     a repo", `git init <dir>` and `git -C <dir> commit` act on that
//     repository, and the guard is appended to its hooks. The variables are
//     unset, not emptied: an empty GIT_DIR is a fatal error to git.
//   - a TMPDIR or GOTMPDIR inside a checkout puts every t.TempDir() inside
//     that checkout, which git discovers by walking up with no variable set.
//     GIT_CEILING_DIRECTORIES stops that walk at the temp roots (tempRoots),
//     so a directory below one is in a repository only when the test ran
//     `git init` there or in a parent it made itself.
//
// It uses t.Setenv, so it cannot be called from a parallel test.
func isolateGit(t *testing.T) {
	t.Helper()
	for _, name := range gitLocationEnv {
		if _, set := os.LookupEnv(name); set {
			t.Setenv(name, "") // registers the restore of the inherited value
			if err := os.Unsetenv(name); err != nil {
				t.Fatalf("unset %s: %v", name, err)
			}
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", strings.Join(tempRoots(t), string(os.PathListSeparator)))
}

// tempRoots returns, as absolute paths, the directories a test's temp
// directories are made in: os.TempDir() (TMPDIR), where os.MkdirTemp("", ...)
// puts them, and GOTMPDIR when it is set, where t.TempDir() puts them instead.
func tempRoots(t *testing.T) []string {
	t.Helper()
	roots := []string{os.TempDir()}
	if dir := os.Getenv("GOTMPDIR"); dir != "" {
		roots = append(roots, dir)
	}
	for i, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			t.Fatalf("temp root %s: %v", root, err)
		}
		roots[i] = abs
	}
	return roots
}

// expectDirectLaunch is for a test that runs the root command and expects it
// to reach a launch the test has stubbed (launchFiltered, launchPassthrough)
// without the TUI. It isolates git (isolateGit) and replaces the two seams
// such a test would otherwise leave real with stubs that fail it:
//
//   - tuiRun, which opens /dev/tty: a hang in a developer's terminal, where
//     there is one to open;
//   - maybeInstallGuard, which appends the commit hook to whatever repository
//     git finds from the working directory.
//
// Both are reached when the launch decision goes another way than the test
// expects, most easily because the directory it ran in turned out to be
// inside a repository. Both are restored on cleanup. A test that launches
// inside a repository it built expects the guard: it calls this first and
// then installs its own maybeInstallGuard stub. A test that means to reach
// the TUI does not call this; it stubs tuiRun itself.
func expectDirectLaunch(t *testing.T) {
	t.Helper()
	isolateGit(t)
	oldTUI, oldGuard := tuiRun, maybeInstallGuard
	tuiRun = func(bool, bool, string, string, string, string, []string, themes.Theme, string, *config.Config, tui.ProfileApplier) error {
		t.Error("tuiRun was called: the command went to the TUI instead of the launch this test stubs")
		return errors.New("tuiRun reached in a test that expects a direct launch")
	}
	maybeInstallGuard = func() {
		t.Error("maybeInstallGuard was called: the command took the working directory for a git repository")
	}
	t.Cleanup(func() { tuiRun, maybeInstallGuard = oldTUI, oldGuard })
}

// gitInit makes dir a git repository with a committer identity of its own.
// It isolates git first (isolateGit), so the repository is created in dir and
// nowhere else.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	isolateGit(t)
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
	isolateGit(t)
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

// TestIsolateGitStopsAtEveryTempRoot checks that after isolateGit a fresh temp
// directory is outside every git repository even when the temp roots are
// inside a checkout: TMPDIR, where os.MkdirTemp("", ...) puts a directory,
// and GOTMPDIR, where t.TempDir() puts one when it is set. A root left out of
// the ceiling list means a developer who runs the suite with that variable
// pointing into a checkout gets the wt commit hook, a branch and a worktree
// written into that checkout by tests that believe they are outside a
// repository.
func TestIsolateGitStopsAtEveryTempRoot(t *testing.T) {
	checkout := t.TempDir()
	gitInit(t, checkout)
	inCheckout := func(t *testing.T, name string) string {
		t.Helper()
		dir := filepath.Join(checkout, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	for _, tc := range []struct {
		name             string
		tmpdir, gotmpdir bool // which variables point into the checkout
	}{
		{name: "TMPDIR", tmpdir: true},
		{name: "GOTMPDIR", gotmpdir: true},
		{name: "both", tmpdir: true, gotmpdir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOTMPDIR", "") // empty is unset, to os.MkdirTemp as to isolateGit
			if tc.tmpdir {
				t.Setenv("TMPDIR", inCheckout(t, tc.name+"-tmpdir"))
			}
			if tc.gotmpdir {
				t.Setenv("GOTMPDIR", inCheckout(t, tc.name+"-gotmpdir"))
			}
			isolateGit(t)

			fromTesting := t.TempDir()
			fromOS, err := os.MkdirTemp("", "wt-isolate-git-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(fromOS) })

			// The case is only a test of the ceiling if the directories
			// really are below the checkout.
			if !strings.HasPrefix(fromTesting, checkout+string(os.PathSeparator)) {
				t.Fatalf("t.TempDir() = %s, want it below the checkout %s", fromTesting, checkout)
			}
			if tc.tmpdir && !strings.HasPrefix(fromOS, checkout+string(os.PathSeparator)) {
				t.Fatalf("os.MkdirTemp = %s, want it below the checkout %s", fromOS, checkout)
			}

			for _, dir := range []string{fromTesting, fromOS} {
				out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").CombinedOutput()
				if err == nil {
					t.Errorf("git in %s found the repository %s; want none", dir, strings.TrimSpace(string(out)))
				}
			}
		})
	}
}

// TestGuardHelpersOutsideRepoError returns an error when not in a git repo so
// the flags cannot be misused outside version control.
func TestGuardHelpersOutsideRepoError(t *testing.T) {
	isolateGit(t)
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
	isolateGit(t)
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
