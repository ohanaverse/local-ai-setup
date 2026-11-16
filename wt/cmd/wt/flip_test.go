package main

import (
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

	oldProbe, oldRoute, oldSweep, oldBuild, oldTUI := probeInventory, ensureModelRoute, sweepRefcounts, buildGatewayCmd, tuiRun
	probeInventory = func(*config.Config) localmodels.Snapshot {
		t.Error("a local provider was probed")
		return localmodels.Snapshot{}
	}
	ensureModelRoute = func(*config.Config, config.Model) bool {
		t.Error("the launch-time route check ran")
		return false
	}
	sweepRefcounts = func() { t.Error("the refcount file was swept") }
	buildGatewayCmd = func(string, config.Model, string, bool, agents.Gateway, []config.Model, []string) (*exec.Cmd, agents.LaunchInfo, error) {
		t.Error("an agent was built against a gateway")
		return nil, agents.LaunchInfo{}, os.ErrInvalid
	}
	tuiRun = func(bool, bool, string, string, string, string, string, []string, themes.Theme, string, tui.ModelClient, *config.Config, tui.ProfileApplier, tui.SlotProfiles, tui.RemoveFuncsBuilder) error {
		t.Error("the TUI opened")
		return nil
	}
	t.Cleanup(func() {
		probeInventory, ensureModelRoute, sweepRefcounts, buildGatewayCmd, tuiRun = oldProbe, oldRoute, oldSweep, oldBuild, oldTUI
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
// (Task 18): with no [model-server] and no WT_MODEL_* a launch of a
// model-driven agent stops before anything runs, with exactly one line naming
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
	out, code := runRoot(t, "--cwd", "-A", "claude")
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
// the server (Task 18's list) with no endpoint configured: each refuses with
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
		{"launch", []string{"--cwd", "-A", "claude"}, false, launchStill},
		{"-M", []string{"--cwd", "-A", "claude", "-M", "openrouter/x/y"}, false, launchStill},
		{"picker", []string{"--cwd"}, true, launchStill},
		{"start", []string{"start", "openrouter/x/y"}, false, "nothing was started"},
		{"stop MODEL", []string{"stop", "omlx/q"}, false, "nothing was stopped"},
		{"job", []string{"job", "job-1"}, false, "the job was not read"},
		{"smoke", []string{"smoke", "openrouter/x/y"}, false, smokeStill},
		{"rotate TAG", []string{"rotate", "code"}, false, "nothing was printed"},
		{"model list --server", []string{"model", "list", "--server"}, false, "nothing was listed"},
		{"profile slots add", []string{"profile", "slots", "add", "mine", "--slot", "sonnet=openrouter/x/y"}, false, slotEditStill},
		{"profile slots candidates", []string{"profile", "slots", "candidates"}, false, candidatesStill},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, marker := noServerHome(t)
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
			if _, err := os.Stat(filepath.Join(home, ".config", "agent-wt", "slot-profiles.toml")); err == nil {
				t.Error("slot-profiles.toml was written")
			}
		})
	}
}

// TestPathsThatNeedNoServerStillRun walks the paths that need no model server
// with none configured: a command agent's launch, `wt worktrees`, `profile
// slots rm|usage`, `stats` (its spend says not configured) and `config path`
// all still run and exit 0. Refusing them would lock a machine with no server
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
