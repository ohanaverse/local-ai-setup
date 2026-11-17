package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"

	"os/exec"
)

// flipRegistry is a registry the pre-flip path could launch claude from: a
// cloud model claude may use, and the agy provider every config with agents
// needs (wt/CLAUDE.md "Fixture gotcha").
const flipRegistry = minimalRegistryTOML + `
[[providers]]
id = "agy"
type = "agy"
name = "agy"
location = "cloud"
auth = { type = "native" }
`

// noServerHome is a scratch home with config.toml naming no model server (and
// a claude agent the registry could serve), the registry above present at the
// default path, a scratch config.yaml, and a fake claude on PATH that touches
// the returned marker if it ever runs. Every registry and local-provider seam
// fails the test if it is reached, and so does the gateway build.
func noServerHome(t *testing.T) (home, marker string) {
	t.Helper()
	home = t.TempDir()
	withCleanConfigEnv(t, home)
	cfgDir := filepath.Join(home, ".config", "agent-wt")
	regDir := filepath.Join(home, ".config", "local-ai")
	for _, d := range []string{cfgDir, regDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfgBody := "default_tag = \"code\"\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"openrouter\"]\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"), []byte(flipRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_LITELLM_CONFIG", filepath.Join(home, "config.yaml"))
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
	marker = filepath.Join(t.TempDir(), "agent-ran")
	bin := t.TempDir()
	script := "#!/bin/sh\ntouch '" + marker + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stubStdinTTY(t, false)

	oldProbe, oldBuild, oldTUI := probeInventory, buildGatewayCmd, tuiRun
	probeInventory = func(*config.Config) localmodels.Snapshot {
		t.Error("a local provider was probed")
		return localmodels.Snapshot{}
	}
	buildGatewayCmd = func(string, config.Model, string, bool, agents.Gateway, []config.Model, []string) (*exec.Cmd, agents.LaunchInfo, error) {
		t.Error("an agent was built against a gateway")
		return nil, agents.LaunchInfo{}, os.ErrInvalid
	}
	tuiRun = func(bool, bool, string, string, string, string, string, []string, themes.Theme, string, tui.ModelClient, *config.Config, tui.ProfileApplier, tui.SlotProfiles, tui.RemoveFuncsBuilder) error {
		t.Error("the TUI opened")
		return nil
	}
	t.Cleanup(func() {
		probeInventory, buildGatewayCmd, tuiRun = oldProbe, oldBuild, oldTUI
	})
	t.Chdir(t.TempDir())
	return home, marker
}

// notConfiguredText is the refusal of a model-needing path when neither the
// url nor the token is set: NotConfiguredError's text, then what is still
// true.
func notConfiguredText(still string) string {
	return modelclient.Still(&modelclient.NotConfiguredError{MissingURL: true, MissingToken: true}, still).Error()
}

// wtLines are the lines of out that begin "wt: ".
func wtLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "wt: ") {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestLaunchWithoutEndpointRefusesWithBothSettings pins the flip's refusal
// (Task 18) for a launch that asks for a model: with no [model-server] and no
// WT_MODEL_* a `-M` launch of a model-driven agent (a plain one runs natively
// since #615) stops before anything runs, with exactly one line naming
// both settings and saying nothing was launched, exit 1. A registry that the
// pre-flip path would have launched from is present and is not used: no
// provider probe, no route check, no refcount sweep, no agent process, and
// config.toml is byte-for-byte unchanged (config.Load, the registry's
// loader, would have migrated it: it lacks the agy agent Load inserts).
// Without the refusal a session machine would silently fall back to a local
// registry and route the user's agent past the model server.
func TestLaunchWithoutEndpointRefusesWithBothSettings(t *testing.T) {
	home, marker := noServerHome(t)
	cfgPath := filepath.Join(home, ".config", "agent-wt", "config.toml")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	out, code := runRoot(t, "--cwd", "-A", "claude", "-M", "openrouter/x/y")
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Errorf("config.toml changed: the registry's loader ran\n%s", after)
	}
	want := "wt: " + notConfiguredText(launchStill)
	if code != 1 {
		t.Errorf("exit = %d, want 1\n%s", code, out)
	}
	if lines := wtLines(out); len(lines) != 1 || lines[0] != want {
		t.Errorf("wt lines = %q, want exactly %q", lines, want)
	}
	for _, s := range []string{"url and token_file under [model-server]", "WT_MODEL_URL and WT_MODEL_TOKEN_FILE", "nothing was launched"} {
		if !strings.Contains(out, s) {
			t.Errorf("output does not name %q:\n%s", s, out)
		}
	}
	if fileExists(marker) {
		t.Error("the agent process ran")
	}
}

// TestEachModelNeedingPathRefuses walks every path that needs a model from
// the server (Task 18's list) with no endpoint configured: a launch that asks
// for a model (-M, -T, -F, a slot profile pin; a plain launch or picker runs
// the agent natively since #615, see native_test.go) and every server command
// each refuses with
// NotConfiguredError's text and its own clause saying what did not happen
// (a launch's "nothing was launched"), exit 1, and spawns no agent. One path that fell back to the registry instead would be a
// session reading host state the split exists to keep away from it.
func TestEachModelNeedingPathRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		tty   bool
		still string
	}{
		{"-M", []string{"--cwd", "-A", "claude", "-M", "openrouter/x/y"}, false, launchStill},
		{"-M profile/NAME", []string{"--cwd", "-A", "claude", "-M", "profile/base"}, false, launchStill},
		{"-T", []string{"--cwd", "-A", "claude", "-T", "code"}, false, launchStill},
		{"-F", []string{"--cwd", "-A", "claude", "-F", "qwen"}, false, launchStill},
		{"picker with -T", []string{"--cwd", "-T", "code"}, true, launchStill},
		{"start", []string{"start", "openrouter/x/y"}, false, "nothing was started"},
		{"stop MODEL", []string{"stop", "omlx/q"}, false, "nothing was stopped"},
		{"job", []string{"job", "job-1"}, false, "the job was not read"},
		{"smoke", []string{"smoke", "openrouter/x/y"}, false, smokeStill},
		{"rotate TAG", []string{"rotate", "code"}, false, "nothing was printed"},
		{"model list --server", []string{"model", "list", "--server"}, false, "nothing was listed"},
		{"profile slots add", []string{"profile", "slots", "add", "mine", "--slot", "sonnet=openrouter/x/y"}, false, slotEditStill},
		{"profile slots candidates", []string{"profile", "slots", "candidates"}, false, candidatesStill},
		{"profile slots set", []string{"profile", "slots", "set", "base", "--slot", "haiku=openrouter/x/y"}, false, slotEditStill},
		{"profile show -M profile/NAME", []string{"profile", "show", "-A", "claude", "-M", "profile/base"}, false, showStill},
		{"profile show -M MODEL", []string{"profile", "show", "-A", "claude", "-M", "openrouter/x/y"}, false, showStill},
		{"stop with no argument", []string{"stop"}, false, "nothing was stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, marker := noServerHome(t)
			// A slot profile the slot commands and show can name.
			slotFile := filepath.Join(home, ".config", "agent-wt", "slot-profiles.toml")
			if err := os.WriteFile(slotFile, []byte(slotsBase), 0o644); err != nil {
				t.Fatal(err)
			}
			stubStdinTTY(t, tc.tty)
			out, code := runRoot(t, tc.args...)
			if code != 1 {
				t.Errorf("exit = %d, want 1\n%s", code, out)
			}
			if lines, want := wtLines(out), "wt: "+notConfiguredText(tc.still); len(lines) != 1 || lines[0] != want {
				t.Errorf("wt lines = %q, want exactly %q", lines, want)
			}
			if fileExists(marker) {
				t.Error("an agent process ran")
			}
			if b, _ := os.ReadFile(slotFile); string(b) != slotsBase {
				t.Errorf("slot-profiles.toml was written:\n%s", b)
			}
		})
	}
}

// TestPathsThatNeedNoServerStillRun walks the paths that need no model server
// with none configured: a command agent's launch, `wt worktrees`, `wt remove`,
// `wt rescue` and `wt restore-root` (dry runs), `profile list|status|on|off`,
// `profile slots rm|usage`, `stats` (its spend says not configured), `config
// path`, `--init` and `--no-guard` all still run and exit 0. Refusing them would lock a machine with no server
// out of wt's own files and of a plain shell launch.
func TestPathsThatNeedNoServerStillRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		setup func(t *testing.T, home string) []string // returns extra args
		check func(t *testing.T, out string)
		// says: the output may name the missing server (stats says why it
		// shows no spend).
		says bool
	}{
		{name: "shell launch", setup: func(t *testing.T, _ string) []string {
			mark := filepath.Join(t.TempDir(), "shell-ran")
			t.Cleanup(func() {
				if !fileExists(mark) {
					t.Error("the shell command did not run")
				}
			})
			return []string{"--cwd", "-A", "shell", "--", "touch", mark}
		}},
		{name: "worktrees", setup: func(t *testing.T, _ string) []string {
			stubCleanupGH(t)
			t.Chdir(cleanupRepo(t))
			return []string{"worktrees", "--json", "--no-fetch"}
		}},
		{name: "profile slots rm", setup: func(t *testing.T, home string) []string {
			p := filepath.Join(home, ".config", "agent-wt", "slot-profiles.toml")
			if err := os.WriteFile(p, []byte(slotsBase), 0o644); err != nil {
				t.Fatal(err)
			}
			return []string{"profile", "slots", "rm", "base"}
		}},
		{name: "profile slots usage", args: []string{"profile", "slots", "usage"}},
		{name: "stats", args: []string{"stats", "--json"}, says: true, check: func(t *testing.T, out string) {
			if !strings.Contains(out, `"`+spendNotConfigured+`"`) {
				t.Errorf("stats spend status is not %q:\n%s", spendNotConfigured, out)
			}
		}},
		{name: "config path", args: []string{"config", "path"}},
		{name: "profile list", args: []string{"profile", "list"}},
		{name: "profile status", args: []string{"profile", "status"}},
		{name: "profile on", args: []string{"profile", "on"}},
		{name: "profile off", args: []string{"profile", "off"}},
		{name: "--init", setup: inTestRepo, args: []string{"--init"}},
		{name: "--no-guard", setup: inTestRepo, args: []string{"--no-guard"}},
		{name: "remove --dry-run", setup: func(t *testing.T, _ string) []string {
			stubCleanupGH(t)
			dir := cleanupRepo(t)
			t.Chdir(dir)
			cgit(t, dir, "branch", "topic")
			return []string{"remove", "--dry-run", "--no-fetch", "-D", "--accept-loss", "topic"}
		}},
		{name: "rescue --dry-run", setup: func(t *testing.T, _ string) []string {
			stubCleanupGH(t)
			dir := cleanupRepo(t)
			t.Chdir(dir)
			p := filepath.Join(filepath.Dir(dir), "wt-topic")
			cgit(t, dir, "worktree", "add", "-b", "topic", p)
			cleanupCommit(t, p, "x.txt", "work")
			return []string{"rescue", "--dry-run", "--no-fetch", p}
		}},
		{name: "restore-root --dry-run", setup: func(t *testing.T, _ string) []string {
			stubCleanupGH(t)
			t.Chdir(cleanupRepo(t))
			return []string{"restore-root", "--dry-run", "--no-fetch"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := noServerHome(t)
			args := tc.args
			if tc.setup != nil {
				args = append(args, tc.setup(t, home)...)
			}
			var out string
			var code int
			captureFile(t, &os.Stdout, func() { out, code = runRoot(t, args...) })
			if code != 0 {
				t.Errorf("exit = %d, want 0\n%s", code, out)
			}
			if !tc.says && strings.Contains(out, "no model server is configured") {
				t.Errorf("a path that needs no server mentioned it:\n%s", out)
			}
			if tc.check != nil {
				tc.check(t, out)
			}
		})
	}
}

// gitState is what a launch could create in repo: its worktrees, its local
// branches and whether the main guard's pre-commit hook exists.
func gitState(t *testing.T, repo string) string {
	t.Helper()
	wts, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, wts)
	}
	brs, err := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname)", "refs/heads").CombinedOutput()
	if err != nil {
		t.Fatalf("git for-each-ref: %v\n%s", err, brs)
	}
	_, herr := os.Stat(filepath.Join(repo, ".git", "hooks", "pre-commit"))
	return string(wts) + "\n" + string(brs) + fmt.Sprintf("\nhook: %v", herr == nil)
}

// inTestRepo is a TestPathsThatNeedNoServerStillRun setup: the command runs
// in a fresh repository.
func inTestRepo(t *testing.T, _ string) []string {
	t.Helper()
	t.Chdir(initTestRepo(t))
	return nil
}

// TestModelLaunchRefusesBeforeAnyGitWork pins the refusal's place in a
// repository: with no model server configured, a launch that asks for a model
// (-M or -T; a plain one runs natively since #615) with
// -W (a new name), --root, or from the worktree picker refuses with the one
// not-configured line, exit 1, before RunE does anything — no worktree or
// branch is created, the main guard is not installed. The plan says a
// model-needing path refuses before anything starts; a launch that first
// made `.worktrees/<name>` and its branch would leave behind work the user
// has to clean up for a launch that could never run.
func TestModelLaunchRefusesBeforeAnyGitWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		tty  bool
	}{
		{"-W a new name", []string{"-W", "feat-x", "-A", "claude", "-M", "openrouter/x/y"}, false},
		{"--root", []string{"--root", "-A", "claude", "-M", "openrouter/x/y"}, false},
		{"the worktree picker, agent pinned", []string{"-A", "claude", "-M", "openrouter/x/y"}, true},
		{"the worktree picker, no agent", []string{"-T", "code"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, marker := noServerHome(t)
			repo := initTestRepo(t)
			t.Chdir(repo)
			stubStdinTTY(t, tc.tty)
			before := gitState(t, repo)
			out, code := runRoot(t, tc.args...)
			if want := "wt: " + notConfiguredText(launchStill); code != 1 || len(wtLines(out)) != 1 || wtLines(out)[0] != want {
				t.Errorf("exit %d, output %q; want 1 and exactly %q", code, out, want)
			}
			if after := gitState(t, repo); after != before {
				t.Errorf("the refused launch changed the repository:\nbefore %s\nafter  %s", before, after)
			}
			if fileExists(marker) {
				t.Error("the agent process ran")
			}
		})
	}
}

// TestLaunchRunsBesideABrokenRegistry pins R3's promise from the other side:
// with a model server configured, a registry.toml that does not parse, or a
// registry link that leads nowhere, is no launch's error. A model-driven
// launch (claude's native row on the server) and a shell launch both run,
// exit 0, and neither stream carries a registry error or the "configuration
// did not load" note. A launch that read the host registry would refuse
// every model on a session machine whose registry is broken — or that has a
// stale one left behind.
func TestLaunchRunsBesideABrokenRegistry(t *testing.T) {
	for _, reg := range []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{"unparsable", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("models = [\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling link", func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(path), "gone.toml"), path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		for _, launch := range []struct {
			name string
			args func(mark string) []string
		}{
			{"claude", func(string) []string { return []string{"--cwd", "-A", "claude", "-M", "claude/opus"} }},
			{"shell", func(mark string) []string { return []string{"--cwd", "-A", "shell", "--", "touch", mark} }},
		} {
			t.Run(reg.name+"/"+launch.name, func(t *testing.T) {
				home := t.TempDir()
				withCleanConfigEnv(t, home)
				cfgDir := filepath.Join(home, ".config", "agent-wt")
				nativeOpusServer(t, cfgDir)
				regPath := filepath.Join(home, ".config", "local-ai", "registry.toml")
				if err := os.MkdirAll(filepath.Dir(regPath), 0o755); err != nil {
					t.Fatal(err)
				}
				reg.write(t, regPath)
				mark := filepath.Join(t.TempDir(), "ran")
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ntouch '"+mark+"'\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				stubStdinTTY(t, false)
				t.Chdir(t.TempDir())

				var out string
				var code int
				notes := captureNotes(t)
				fileOut := captureFile(t, &os.Stdout, func() { out, code = runRoot(t, launch.args(mark)...) })
				out += fileOut + notes.String()
				if code != 0 || !fileExists(mark) {
					t.Errorf("exit %d, ran %v; want 0 and the launch run beside a broken registry\n%s", code, fileExists(mark), out)
				}
				for _, bad := range []string{"registry", "did not load"} {
					if strings.Contains(out, bad) {
						t.Errorf("the launch surfaced %q beside a broken registry:\n%s", bad, out)
					}
				}
			})
		}
	}
}

// TestStatsFamilyNoNoteWithoutARegistry pins `wt stats --family` on a
// session-only machine: no model server answers and there is no registry at
// the default path, so the families come from each id's prefix and no note
// says the configuration "did not load" (it would send the user to `wt model
// init`, building host state the split keeps off a session machine). A
// registry that exists and does not load still gets its note
// (TestStatsFamilyNoteNamesTheRepairThatWorks).
func TestStatsFamilyNoNoteWithoutARegistry(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-wt", "config.toml"), []byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	_, stderr := runStats(t, a, "--family", "qwen")
	if strings.Contains(stderr, "did not load") || strings.Contains(stderr, "wt model init") {
		t.Errorf("stderr = %q, want no registry note on a machine with no registry", stderr)
	}
}
