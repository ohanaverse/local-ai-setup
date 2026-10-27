package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// cwdLaunch is what one real `wt --cwd -A claude -M claude/opus` left behind:
// where the agent ran, whether the guard was asked for, and the bookkeeping.
type cwdLaunch struct {
	err        error
	agentDir   string   // the agent's own working directory, links resolved
	claimSeen  bool     // the agent found this wt's root session claim while it ran
	guarded    bool     // maybeInstallGuard was called
	guardRoot  string   // the checkout root git reported when it was called, links resolved ("" if none)
	released   int      // times releaseSession was called
	stateDir   string   // the scratch config home's agent-wt directory
	stateFiles []string // the files a launch wrote there, sorted
}

// runCwdLaunch runs the root command with --cwd from dir against a scratch
// config home that holds one native claude model, with a claude on PATH that
// only records the directory it was started in. Nothing is stubbed between
// the flag and the agent process, so the directory is the one the agent
// really got, and the state files are the ones a real launch writes.
func runCwdLaunch(t *testing.T, dir string, extra ...string) cwdLaunch {
	t.Helper()
	return runPinnedLaunch(t, dir, []string{"--cwd"}, extra...)
}

// runPinnedLaunch is runCwdLaunch with where (--cwd, or -W <name>) choosing
// the launch directory.
func runPinnedLaunch(t *testing.T, dir string, where []string, extra ...string) cwdLaunch {
	t.Helper()
	t.Chdir(dir)
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	cfgDir := filepath.Join(home, ".config", "agent-wt")
	regDir := filepath.Join(home, ".config", "local-ai")
	for _, d := range []string{cfgDir, regDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("default_tag = \"code\"\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"claude\"]\ndefault_provider = \"claude\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"),
		[]byte("[[providers]]\nid = \"claude\"\nname = \"Claude\"\nlocation = \"cloud\"\nauth = { type = \"native\" }\n"+
			"[[providers]]\nid = \"agy\"\nname = \"Antigravity\"\nlocation = \"cloud\"\nauth = { type = \"native\" }\n"+
			"[[models]]\nid = \"claude/opus\"\nprovider_id = \"claude\"\nmodel_name = \"opus\"\nlocation = \"cloud\"\ntags = [\"code\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A claude that writes where it was started, and whether this wt's claim
	// is in its repository's wt-sessions while it runs, and exits.
	binDir := t.TempDir()
	pwdFile := filepath.Join(t.TempDir(), "pwd")
	claimFile := filepath.Join(t.TempDir(), "claim")
	script := "#!/bin/sh\npwd -P > '" + pwdFile + "'\n" +
		"d=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) && test -f \"$d/wt-sessions/" + strconv.Itoa(os.Getpid()) + "\" && touch '" + claimFile + "'\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := cwdLaunch{stateDir: cfgDir}
	oldTUI, oldGuard := tuiRun, maybeInstallGuard
	tuiRun = func(bool, bool, string, string, string, string, string, []string, themes.Theme, string, *config.Config, tui.ProfileApplier, tui.SlotProfiles) error {
		t.Error("tuiRun was called: a launch with -A and -M pinned went to the TUI")
		return nil
	}
	// The guard takes no root argument: it acts on the repository git finds
	// from the working directory. So the root it was "given" is read the
	// same way at the moment it is called.
	maybeInstallGuard = func() {
		res.guarded = true
		if out, gerr := exec.Command("git", "rev-parse", "--show-toplevel").Output(); gerr == nil {
			res.guardRoot, _ = filepath.EvalSymlinks(strings.TrimSpace(string(out)))
		}
	}
	oldRelease := releaseSession
	releaseSession = func() { res.released++ }
	t.Cleanup(func() { tuiRun, maybeInstallGuard, releaseSession = oldTUI, oldGuard, oldRelease })

	before, err := os.ReadDir(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	args := append(append(append([]string{}, where...), "-A", "claude", "-M", "claude/opus"), extra...)
	captureFile(t, &os.Stdout, func() { _, res.err = runWT(t, args...) })

	if b, rerr := os.ReadFile(pwdFile); rerr == nil {
		res.agentDir = strings.TrimSpace(string(b))
	}
	res.claimSeen = fileExists(claimFile)
	after, err := os.ReadDir(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after {
		if !slices.ContainsFunc(before, func(b os.DirEntry) bool { return b.Name() == e.Name() }) {
			res.stateFiles = append(res.stateFiles, e.Name())
		}
	}
	slices.Sort(res.stateFiles)
	return res
}

// physical resolves dir's symbolic links (macOS's temp directory sits behind
// one), so a path can be compared with what `pwd -P` printed.
func physical(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// wantCwdBookkeeping asserts the state one single-model --cwd launch records:
// the rotation pointer, the picker's marker, one usage event for the pair and
// one live-session line, all in the scratch config home, and that the launch
// released its session exactly once. The live-session line is still there
// after the agent exited only because releaseSession is stubbed here (and in
// TestMain), so that count pins the record made at launch; the release is
// pinned by the res.released count.
func wantCwdBookkeeping(t *testing.T, res cwdLaunch) {
	t.Helper()
	for _, name := range []string{"rotation.state", "last-picked.state"} {
		data, err := os.ReadFile(filepath.Join(res.stateDir, name))
		if err != nil {
			t.Errorf("%s not written: %v", name, err)
			continue
		}
		if got := strings.TrimSpace(string(data)); got != "claude/opus" {
			t.Errorf("%s = %q, want claude/opus", name, got)
		}
	}
	if got := usage.NewStoreAt(res.stateDir).CountsForAgent("claude", []string{"claude/opus"})["claude/opus"]; got.ThirtyDay != 1 {
		t.Errorf("usage events for claude × claude/opus = %d, want 1", got.ThirtyDay)
	}
	if got := refcount.NewStoreAt(res.stateDir).Counts([]string{"claude/opus"})["claude/opus"]; got != 1 {
		t.Errorf("record at launch: live-session count for claude/opus = %d, want 1 (releaseSession is stubbed, so this does not show the release)", got)
	}
	if res.released != 1 {
		t.Errorf("releaseSession called %d times after the agent exited, want 1", res.released)
	}
}

// cwdStateFiles is every entry a single-model launch adds under wt's config
// directory: the four state files, their locks, the lock of the config.toml
// migration every first load runs, and the profile self-heal's backup
// directory. The inside-a-repository and outside-a-repository tests compare
// against the same list, so the two launches cannot drift apart.
var cwdStateFiles = []string{
	"config.toml.lock", "last-picked.state", "profile-backups",
	"refcount.jsonl", "refcount.jsonl.lock", "rotation.state",
	"usage.jsonl", "usage.jsonl.lock",
}

// TestCwdOutsideARepoLaunchesInTheCurrentDirectory verifies that `wt --cwd`
// with the agent and model pinned launches in a directory that is not inside
// a git repository. It used to stop with "not in a git repo: exit status 128",
// although nothing a --cwd launch does needs a repository: a scratch
// directory, a notes folder or a home directory could not be launched in
// with the flag that says "here". The agent must start in that directory, no
// commit hook may be installed (there is no repository to install it in), the
// launch must be recorded exactly like one inside a repository, and nothing
// may be written into the directory itself.
func TestCwdOutsideARepoLaunchesInTheCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	res := runCwdLaunch(t, dir)
	if res.err != nil {
		t.Fatalf("wt --cwd outside a repository: %v", res.err)
	}
	if want := physical(t, dir); res.agentDir != want {
		t.Errorf("the agent started in %q, want the current directory %q", res.agentDir, want)
	}
	if res.guarded {
		t.Error("the main guard was installed outside a git repository")
	}
	wantCwdBookkeeping(t, res)
	if !slices.Equal(res.stateFiles, cwdStateFiles) {
		t.Errorf("files written under the config home = %v, want %v", res.stateFiles, cwdStateFiles)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("the launch left %d entries in the directory it ran in (first: %s); its bookkeeping belongs under the config home only", len(left), left[0].Name())
	}
}

// TestCwdInsideARepoIsUnchanged pins the launch --cwd already made inside a
// repository, end to end: the agent starts in the subdirectory the command
// was typed in (not the checkout root), the guard is installed, and the same
// state files are written with the same contents as outside one. It is the
// other half of TestCwdOutsideARepoLaunchesInTheCurrentDirectory: making the
// flag work without a repository must not move anything for a launch that
// has one.
func TestCwdInsideARepoIsUnchanged(t *testing.T) {
	repo := initTestRepo(t)
	sub := filepath.Join(repo, "pkg", "inner")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	res := runCwdLaunch(t, sub)
	if res.err != nil {
		t.Fatalf("wt --cwd inside a repository: %v", res.err)
	}
	if want := physical(t, sub); res.agentDir != want {
		t.Errorf("the agent started in %q, want the current directory %q", res.agentDir, want)
	}
	if !res.guarded {
		t.Error("the main guard was not installed: --cwd in a subdirectory is still inside a repository")
	}
	// The checkout root, not the subdirectory the command was typed in.
	if want := physical(t, repo); res.guardRoot != want {
		t.Errorf("the guard was installed for root %q, want the checkout root %q", res.guardRoot, want)
	}
	wantCwdBookkeeping(t, res)
	if !slices.Equal(res.stateFiles, cwdStateFiles) {
		t.Errorf("files written under the config home = %v, want %v", res.stateFiles, cwdStateFiles)
	}
}

// TestCwdOutsideARepoReachesThePickers verifies the TUI half of --cwd outside
// a repository: when a picker is still owed (no agent, or an agent with no
// model resolved) wt opens the TUI with the current directory as the
// pre-resolved path, so the worktree picker — which needs a repository — is
// skipped and the agent and model pickers work as they do inside one. A
// regression here is the same "not in a git repo" refusal, for the
// interactive form of the command (`claude-wt --cwd`).
func TestCwdOutsideARepoReachesThePickers(t *testing.T) {
	for _, c := range []struct {
		name      string
		args      []string
		wantAgent string
	}{
		{"no agent", []string{"--cwd"}, ""},
		{"agent without a model", []string{"--cwd", "-A", "pi"}, "pi"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			// pi is configured with no eligible model, so nothing resolves
			// without the picker.
			writeConfiguredAgent(t, t.TempDir(), "pi", "ollama")
			stubStdinTTY(t, true)

			var called bool
			var gotAgent, gotPath string
			oldTUI, oldGuard := tuiRun, maybeInstallGuard
			tuiRun = func(yolo, allowReplace bool, _ string, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, _ tui.ProfileApplier, _ tui.SlotProfiles) error {
				called, gotAgent, gotPath = true, agent, prePath
				return nil
			}
			maybeInstallGuard = func() { t.Error("the main guard was installed outside a git repository") }
			t.Cleanup(func() { tuiRun, maybeInstallGuard = oldTUI, oldGuard })

			if _, err := runWT(t, c.args...); err != nil {
				t.Fatalf("wt %s outside a repository: %v", strings.Join(c.args, " "), err)
			}
			if !called {
				t.Fatal("tuiRun was not called")
			}
			if gotAgent != c.wantAgent {
				t.Errorf("tuiRun agent = %q, want %q", gotAgent, c.wantAgent)
			}
			// A non-empty path is what skips the worktree picker; it must be
			// the directory itself, absolute like every other --cwd launch.
			if !filepath.IsAbs(gotPath) || physical(t, gotPath) != physical(t, dir) {
				t.Errorf("tuiRun path = %q, want the current directory %q", gotPath, dir)
			}
		})
	}
}

// TestWorktreeFlagOutsideARepoStillRefuses pins the launches that do need a
// repository: -W creates or opens a worktree, so outside one it must refuse
// with a message that says why, launch nothing and open no picker — with or
// without --cwd beside it. --cwd learning to run without a repository must
// not turn `-W name` into a silent launch in the wrong place.
func TestWorktreeFlagOutsideARepoStillRefuses(t *testing.T) {
	for _, args := range [][]string{
		{"-W", "my-branch", "-A", "shell"},
		{"-W", "my-branch", "--cwd", "-A", "shell"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			expectDirectLaunch(t)
			t.Chdir(t.TempDir())
			writeEmptyRegistry(t, t.TempDir())
			oldLaunch := launchFiltered
			launchFiltered = func(string, string, *config.Config, bool, string, string, string, bool, []string, []config.Model, *precomputedProfiles) error {
				t.Error("launchFiltered was called: -W launched outside a git repository")
				return nil
			}
			t.Cleanup(func() { launchFiltered = oldLaunch })

			_, err := runWT(t, args...)
			if err == nil {
				t.Fatal("expected -W outside a git repository to be refused")
			}
			if !strings.HasPrefix(err.Error(), "not in a git repo: ") {
				t.Errorf("error = %q, want it to begin \"not in a git repo: \"", err.Error())
			}
		})
	}
}

// TestOutsideARepoWithoutCwdIsUnchanged pins the launch wt already made
// outside a repository with no --cwd: the worktree picker is skipped, the
// launch path is "." and no guard is installed. --cwd outside a repository
// lands in the same directory by its own branch; this path must stay as it
// was, since every `claude-wt` typed in a plain directory goes through it.
func TestOutsideARepoWithoutCwdIsUnchanged(t *testing.T) {
	expectDirectLaunch(t)
	t.Chdir(t.TempDir())
	writeEmptyRegistry(t, t.TempDir())

	var gotPath string
	var called bool
	oldLaunch := launchFiltered
	launchFiltered = func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model, pp *precomputedProfiles) error {
		called, gotPath = true, worktreePath
		return nil
	}
	t.Cleanup(func() { launchFiltered = oldLaunch })

	if _, err := runWT(t, "-A", "shell", "true"); err != nil {
		t.Fatalf("wt -A shell outside a repository: %v", err)
	}
	if !called {
		t.Fatal("launchFiltered was not called")
	}
	if gotPath != "." {
		t.Errorf("launch path = %q, want \".\"", gotPath)
	}
}

// TestCwdInAGitDirectoryWithNoCheckoutLaunches covers the directory git knows
// but has no checkout root for: a bare repository (inside .git is the same
// case). worktree.IsRepo says yes there and worktree.RepoRoot fails, so --cwd
// used to be refused with "not in a git repo" in a place that is one. It
// launches like any other directory without a checkout root: in place, and
// with no guard installed by this launch.
func TestCwdInAGitDirectoryWithNoCheckoutLaunches(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	res := runCwdLaunch(t, dir)
	if res.err != nil {
		t.Fatalf("wt --cwd in a bare repository: %v", res.err)
	}
	if want := physical(t, dir); res.agentDir != want {
		t.Errorf("the agent started in %q, want the current directory %q", res.agentDir, want)
	}
	if res.guarded {
		t.Error("the main guard was installed for a directory with no checkout root")
	}
	wantCwdBookkeeping(t, res)
}

// runCwdPlain runs `wt --cwd <args>` from dir against an empty scratch config
// home (no agents configured, no registry) with a claude on PATH that records
// the directory it was started in. It returns the error, the directory the
// agent saw, and whether the guard was installed.
func runCwdPlain(t *testing.T, dir string, args ...string) (err error, agentDir string, guarded bool) {
	t.Helper()
	t.Chdir(dir)
	withCleanConfigEnv(t, t.TempDir())
	binDir := t.TempDir()
	pwdFile := filepath.Join(t.TempDir(), "pwd")
	script := "#!/bin/sh\npwd -P > '" + pwdFile + "'\nexit 0\n"
	if werr := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); werr != nil {
		t.Fatal(werr)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PWD_FILE", pwdFile)

	expectDirectLaunch(t)
	oldGuard := maybeInstallGuard
	maybeInstallGuard = func() { guarded = true }
	t.Cleanup(func() { maybeInstallGuard = oldGuard })

	captureFile(t, &os.Stdout, func() { _, err = runWT(t, args...) })
	if b, rerr := os.ReadFile(pwdFile); rerr == nil {
		agentDir = strings.TrimSpace(string(b))
	}
	return err, agentDir, guarded
}

// TestCwdOutsideARepoLaunchesShellAndAnUnconfiguredAgentInPlace covers the
// two --cwd launches that skip the model layer: a command agent (shell, through
// launchFiltered) and a model-driven agent with no config.toml entry
// (launchPassthrough). Outside a repository both must run in the current
// directory, install no guard and return no error.
func TestCwdOutsideARepoLaunchesShellAndAnUnconfiguredAgentInPlace(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"shell", []string{"--cwd", "-A", "shell", "--", "sh", "-c", `pwd -P > "$PWD_FILE"`}},
		{"unconfigured agent", []string{"--cwd", "-A", "claude"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			err, agentDir, guarded := runCwdPlain(t, dir, c.args...)
			if err != nil {
				t.Fatalf("wt %s outside a repository: %v", strings.Join(c.args, " "), err)
			}
			if want := physical(t, dir); agentDir != want {
				t.Errorf("the command ran in %q, want the current directory %q", agentDir, want)
			}
			if guarded {
				t.Error("the main guard was installed outside a git repository")
			}
		})
	}
}
