package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
)

// nativeBuild is one agent build the recording seam saw.
type nativeBuild struct {
	agent string
	model config.Model
	gw    agents.Gateway
	yolo  bool
	dir   string
	args  []string
}

// nativeRig is a machine with no model server whose agents are fake scripts
// that touch <ran>/<name>, with the real gateway build recorded and the TUI
// recorded instead of opened. Its config.toml has no [model-server], so the
// mode is unset (a launch refuses) until a test chooses native: chooseNative
// (WT_MODEL_MODE) or storedNativeHome (mode = "native" in config.toml).
type nativeRig struct {
	t      *testing.T
	home   string
	ran    string // directory holding one marker per agent that ran
	builds []nativeBuild
	tui    []nativeTUICall
	hooks  hooks // the app's hooks: the recording gateway build
}

// nativeTUICall is one tuiRun the picker path made.
type nativeTUICall struct {
	agent  string
	pinned string
	models tui.ModelClient
}

// ranAgent reports whether the fake binary of name ran.
func (r *nativeRig) ranAgent(name string) bool { return fileExists(filepath.Join(r.ran, name)) }

// newNativeRig builds the rig on noServerHome: the same scratch home, config
// and cleanup, with the agents' fake binaries added and the failing seams
// (gateway build, TUI) swapped for recorders that still build and run the real
// command.
func newNativeRig(t *testing.T) *nativeRig {
	t.Helper()
	home, _, h := noServerHome(t)
	r := &nativeRig{t: t, home: home, ran: t.TempDir(), hooks: h}
	bin := t.TempDir()
	for _, name := range []string{"claude", "codex", "copilot", "pi", "opencode"} {
		script := "#!/bin/sh\nenv > '" + filepath.Join(r.ran, name+".env") + "'\ntouch '" + filepath.Join(r.ran, name) + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldTUI := tuiRun
	t.Cleanup(func() { tuiRun = oldTUI })
	r.hooks.buildGateway = func(agent string, m config.Model, dir string, yolo bool, gw agents.Gateway, models []config.Model, extra []string) (*exec.Cmd, agents.LaunchInfo, error) {
		cmd, info, err := agents.BuildLaunchCmdGateway(agent, m, dir, yolo, gw, models, extra)
		r.builds = append(r.builds, nativeBuild{agent: agent, model: m, gw: gw, yolo: yolo, dir: dir, args: extra})
		return cmd, info, err
	}
	tuiRun = func(_, _ bool, _ string, agent, pinned, _, _ string, _ []string, _ themes.Theme, _ string, models tui.ModelClient, _ *config.Config, _ tui.ProfileLayer, _ tui.SlotProfiles, _ tui.RemoveFuncsBuilder, _ []string) error {
		r.tui = append(r.tui, nativeTUICall{agent: agent, pinned: pinned, models: models})
		return nil
	}
	return r
}

// chooseNative makes native the effective mode through the environment
// (WT_MODEL_MODE=native), the explicit choice a native launch now needs.
func (r *nativeRig) chooseNative() { r.t.Setenv("WT_MODEL_MODE", "native") }

// storedNativeHome is newNativeRig with native chosen in config.toml:
// [model-server] mode = "native" appended to the rig's config.
func storedNativeHome(t *testing.T) *nativeRig {
	t.Helper()
	r := newNativeRig(t)
	r.appendConfig("\n[model-server]\nmode = \"native\"\n")
	return r
}

// appendConfig appends body to the rig's config.toml.
func (r *nativeRig) appendConfig(body string) {
	r.t.Helper()
	cfg := filepath.Join(r.home, ".config", "agent-wt", "config.toml")
	old, err := os.ReadFile(cfg)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(cfg, append(old, body...), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// run runs the root command with stderr and stdout captured (a launch prints
// its note to osStderr and its summary to stdout), returning everything.
func (r *nativeRig) run(t *testing.T, args ...string) (out string, code int) {
	t.Helper()
	notes := captureNotes(t)
	var fileOut, fileErr string
	fileErr = captureFile(t, &os.Stderr, func() {
		fileOut = captureFile(t, &os.Stdout, func() { out, code = runRootWith(t, r.hooks, args...) })
	})
	return out + fileOut + fileErr + notes.String(), code
}

// TestLaunchWithoutAServerRunsEveryAgentNatively pins #615: with native mode
// chosen in config.toml, `wt -A <agent>` launches the agent itself, exit 0,
// with the native model and a zero Gateway (so no gateway token or origin can
// reach it), passes --yolo and the arguments after `--` on, prints exactly one
// "launching <agent> natively" note, and leaves no launch history or usage record.
// Before this a machine whose agent reaches its model by the agent's own
// configuration could not launch through wt at all.
func TestLaunchWithoutAServerRunsEveryAgentNatively(t *testing.T) {
	for _, name := range []string{"claude", "codex", "copilot", "pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			// Stored, not WT_MODEL_MODE: the agent's environment is
			// checked below for WT_MODEL_* and must carry none.
			r := storedNativeHome(t)
			out, code := r.run(t, "--cwd", "-A", name, "--yolo", "--", "--some-flag")
			if code != 0 || !r.ranAgent(name) {
				t.Fatalf("exit %d, agent ran %v; want 0 and the agent run\n%s", code, r.ranAgent(name), out)
			}
			if n := strings.Count(out, "launching "+name+" natively"); n != 1 {
				t.Errorf("the native note appears %d times, want 1:\n%s", n, out)
			}
			if len(r.builds) != 1 {
				t.Fatalf("builds = %d, want 1", len(r.builds))
			}
			b := r.builds[0]
			if !b.model.Native || b.model.ID != "" || b.gw != (agents.Gateway{}) || !b.yolo || len(b.args) != 1 || b.args[0] != "--some-flag" {
				t.Errorf("build = %+v, want the native model, a zero Gateway, --yolo and the passthrough args", b)
			}
			env, err := os.ReadFile(filepath.Join(r.ran, name+".env"))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(env), "\n") {
				if strings.HasPrefix(line, "WT_MODEL_") || strings.Contains(line, "gateway") {
					t.Errorf("the agent's environment carries %q, want no model-server or gateway setting", line)
				}
			}
			// rotation.state stays in the list: nothing writes it any more,
			// so its absence proves a launch path does not bring it back.
			for _, f := range []string{"rotation.state", "last-picked.state", "usage.jsonl"} {
				if fileExists(filepath.Join(r.home, ".config", "agent-wt", f)) {
					t.Errorf("a native launch wrote %s", f)
				}
			}
		})
	}
}

// TestNativeLaunchKeepsTheWorktreeSteps pins that a native launch still does
// everything that is not model selection: -W creates the worktree and runs the
// agent in it, --root launches in the primary checkout, with no early refusal.
// The refusal used to come before any git work; the native path must not skip
// the worktree creation along with it.
func TestNativeLaunchKeepsTheWorktreeSteps(t *testing.T) {
	r := newNativeRig(t)
	r.chooseNative()
	repo := initTestRepo(t)
	t.Chdir(repo)
	out, code := r.run(t, "-W", "feat-x", "-A", "claude")
	if code != 0 || !r.ranAgent("claude") {
		t.Fatalf("-W: exit %d, ran %v\n%s", code, r.ranAgent("claude"), out)
	}
	want := filepath.Join(repo, ".worktrees", "feat-x")
	if len(r.builds) != 1 || !fileExists(want) || evalPath(t, r.builds[0].dir) != evalPath(t, want) {
		t.Errorf("-W: builds %+v, want one in the new worktree %s", r.builds, want)
	}
	if err := os.Remove(filepath.Join(r.ran, "claude")); err != nil {
		t.Fatal(err)
	}
	out, code = r.run(t, "--root", "-A", "claude")
	if code != 0 || !r.ranAgent("claude") {
		t.Errorf("--root: exit %d, ran %v\n%s", code, r.ranAgent("claude"), out)
	}
}

// evalPath resolves symlinks (a macOS temp directory is one).
func evalPath(t *testing.T, p string) string {
	t.Helper()
	q, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// TestNativePickerOpensWithoutAModelClient pins the interactive half: with no
// server a terminal launch opens the picker with no model client and the
// no client (the picker then launches a chosen agent natively and never
// shows a model screen), both for a bare `wt` and for `-A claude` outside
// -W/--root/--cwd. The refusal used to stop both before the screen opened.
func TestNativePickerOpensWithoutAModelClient(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		agent string
	}{
		{"no agent", nil, ""},
		{"agent pinned", []string{"-A", "claude"}, "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newNativeRig(t)
			r.chooseNative()
			t.Chdir(initTestRepo(t))
			stubStdinTTY(t, true)
			maybeInstallGuard = func() {}
			out, code := r.run(t, tc.args...)
			if code != 0 || len(r.tui) != 1 {
				t.Fatalf("exit %d, tuiRun calls %d; want 0 and one\n%s", code, len(r.tui), out)
			}
			if c := r.tui[0]; c.models != nil || c.agent != tc.agent {
				t.Errorf("tuiRun got %+v, want no client, native, agent %q", c, tc.agent)
			}
		})
	}
}

// TestHalfConfiguredServerStillRefuses pins the fallback's edge: exactly one
// of the url and the token set is a half-configured server, which refuses and
// names the missing setting rather than launching natively, so a typo in
// config.toml or a lost token file never silently drops the server.
func TestHalfConfiguredServerStillRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, env, val, want string
	}{
		{"url only", "WT_MODEL_URL", "http://127.0.0.1:9", "the model server token is not configured"},
		{"token only", "WT_MODEL_TOKEN", "tok", "the model server url is not configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newNativeRig(t)
			t.Setenv(tc.env, tc.val)
			out, code := r.run(t, "--cwd", "-A", "claude")
			if code != 1 || !strings.Contains(out, tc.want) || !strings.Contains(out, launchStill) {
				t.Errorf("exit %d, output %q; want 1 naming %q and %q", code, out, tc.want, launchStill)
			}
			if r.ranAgent("claude") || len(r.builds) != 0 || strings.Contains(out, "natively") {
				t.Errorf("the agent launched natively beside a half-configured server (builds %d)\n%s", len(r.builds), out)
			}
		})
	}
}

// TestHalfConfiguredServerFromConfigFileStillRefuses covers the config-file
// forms of a half-configured server: a token_file with no url, and a url with
// no token, each in config.toml, refuse with nothing launched, also with -W,
// where nothing may be created either.
func TestHalfConfiguredServerFromConfigFileStillRefuses(t *testing.T) {
	for _, tc := range []struct{ name, table, want string }{
		{"token_file only", "token_file = \"TOK\"\n", "the model server url is not configured"},
		{"url only", "url = \"http://127.0.0.1:9\"\n", "the model server token is not configured"},
	} {
		for _, w := range []bool{false, true} {
			name := tc.name
			args := []string{"--cwd", "-A", "claude"}
			if w {
				name += " with -W"
				args = []string{"-W", "feat-half", "-A", "claude"}
			}
			t.Run(name, func(t *testing.T) {
				r := newNativeRig(t)
				tok := filepath.Join(t.TempDir(), "tok")
				if err := os.WriteFile(tok, []byte("secret\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg := filepath.Join(r.home, ".config", "agent-wt", "config.toml")
				body, err := os.ReadFile(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cfg, append(body, []byte("\n[model-server]\n"+strings.ReplaceAll(tc.table, "TOK", tok))...), 0o644); err != nil {
					t.Fatal(err)
				}
				repo := initTestRepo(t)
				t.Chdir(repo)
				out, code := r.run(t, args...)
				if code != 1 || !strings.Contains(out, tc.want) || !strings.Contains(out, launchStill) {
					t.Errorf("exit %d, output %q; want 1 naming %q and %q", code, out, tc.want, launchStill)
				}
				if r.ranAgent("claude") || len(r.builds) != 0 || strings.Contains(out, "natively") {
					t.Errorf("launched natively beside a half-configured server\n%s", out)
				}
				wantNothingCreated(t, repo, "feat-half")
			})
		}
	}
}

// TestNativeLaunchSaysNothingAboutSlotProfiles pins that a native launch
// (which can never use a slot profile) prints no slot-profiles.toml warning,
// though the file has a stray key that warns on a server launch.
func TestNativeLaunchSaysNothingAboutSlotProfiles(t *testing.T) {
	r := newNativeRig(t)
	r.chooseNative()
	dir := filepath.Join(r.home, ".config", "agent-wt")
	if err := os.WriteFile(filepath.Join(dir, "slot-profiles.toml"), []byte("[[profiles]]\nname = \"mixed\"\nagent = \"claude\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := r.run(t, "--cwd", "-A", "claude")
	if code != 0 || !r.ranAgent("claude") {
		t.Fatalf("exit %d, ran %v\n%s", code, r.ranAgent("claude"), out)
	}
	if strings.Contains(out, "slot-profiles.toml") {
		t.Errorf("a native launch warned about slot-profiles.toml:\n%s", out)
	}
}

// TestEmptyModelFlagStillRefusesWithoutAServer pins that an explicit empty -M
// (a script's unset variable) asks for a model like any other -M: with no
// server it refuses instead of launching natively, where only a launch with no
// -M at all falls back.
func TestEmptyModelFlagStillRefusesWithoutAServer(t *testing.T) {
	r := newNativeRig(t)
	r.chooseNative()
	out, code := r.run(t, "--cwd", "-A", "claude", "-M", "")
	if code != 1 || r.ranAgent("claude") || len(r.builds) != 0 || strings.Contains(out, "natively") {
		t.Errorf("exit %d, ran %v, builds %d; want a refusal and no launch\n%s", code, r.ranAgent("claude"), len(r.builds), out)
	}
}

// strayURL is a url line for the [model-server] table storedNativeHome ends
// with: a value native mode must ignore.
const strayURL = "url = \"http://127.0.0.1:9\"\n"

// unsetText is the refusal of a model-needing path when no mode is chosen
// anywhere: ModeUnsetError's text, then what is still true.
func unsetText(still string) string {
	return modelclient.Still(&modelclient.ModeUnsetError{}, still).Error()
}

// nativeRefusalText is the refusal of a path that asks the model server for
// something while native mode is chosen.
func nativeRefusalText(still string) string {
	return modelclient.Still(&modelclient.NativeModeError{}, still).Error()
}

// TestUnsetModeRefusesALaunchWithoutAnything pins the stage 3 reversal of
// #615: with no [model-server] and no WT_MODEL_* a plain launch no longer
// falls back to native. It refuses, exit 1, with one line that begins with
// the old lead "no model server is configured" (llmbench's hint and scripts
// key on it), names wt config and WT_MODEL_MODE, and says nothing was
// launched; no agent runs, nothing is built, and with -W no worktree or
// branch is created, and a picker never opens. Guessing native on an
// unconfigured machine is what the explicit mode replaces.
func TestUnsetModeRefusesALaunchWithoutAnything(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		tty  bool
	}{
		{"--cwd", []string{"--cwd", "-A", "claude"}, false},
		{"-W", []string{"-W", "feat-unset", "-A", "claude"}, false},
		{"picker, agent pinned", []string{"-A", "claude"}, true},
		{"picker, no agent", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newNativeRig(t)
			repo := initTestRepo(t)
			t.Chdir(repo)
			stubStdinTTY(t, tc.tty)
			maybeInstallGuard = func() {}
			want := "wt: " + unsetText(launchStill)
			if tc.tty { // the first-run gate asked, and the mode stayed unset
				stubEditorSideEffect(t, nil)
				want = "wt: " + errNoModeChosen.Error()
			}
			out, code := r.run(t, tc.args...)
			// A terminal row runs the editor, whose host-config load of this
			// rig's legacy config.toml genuinely prints the migration note
			// after the refusal (run it: it is the second wt line), so only
			// those rows may carry that one extra line; a non-terminal row
			// is exactly one line.
			lines := wtLines(out)
			okLines := len(lines) == 1 || (tc.tty && len(lines) == 2 && strings.HasPrefix(lines[1], "wt: migrated config"))
			if code != 1 || !okLines || lines[0] != want {
				t.Errorf("exit %d, wt lines %q; want 1 and %q\n%s", code, wtLines(out), want, out)
			}
			for _, s := range []string{"wt: no model server is configured", "wt config", "WT_MODEL_MODE"} {
				if !tc.tty && !strings.Contains(out, s) {
					t.Errorf("output does not name %q:\n%s", s, out)
				}
			}
			if r.ranAgent("claude") || len(r.builds) != 0 || len(r.tui) != 0 || strings.Contains(out, "natively") {
				t.Errorf("ran %v, builds %d, tui %d; want nothing launched, built or opened\n%s", r.ranAgent("claude"), len(r.builds), len(r.tui), out)
			}
			wantNothingCreated(t, repo, "feat-unset")
		})
	}
}

// TestStoredNativeLaunchesNativelyOnEveryPath pins native as a stored
// choice: with [model-server] mode = "native" in config.toml every launch
// path that #615 made native still is — the pre-flight lets it through, a
// non-TUI launch (launchFilteredRemote) runs the agent natively with one
// note naming native mode, -W still creates its worktree, and the picker
// opens with no model client (bare and with an agent pinned). The -W and
// picker cases carry a stray url under [model-server], which an explicit
// native ignores (it was a half-configured refusal before the mode existed).
func TestStoredNativeLaunchesNativelyOnEveryPath(t *testing.T) {
	t.Run("launchFilteredRemote", func(t *testing.T) {
		r := storedNativeHome(t)
		out, code := r.run(t, "--cwd", "-A", "codex")
		if code != 0 || !r.ranAgent("codex") || len(r.builds) != 1 || !r.builds[0].model.Native {
			t.Fatalf("exit %d, ran %v, builds %+v; want one native launch\n%s", code, r.ranAgent("codex"), r.builds, out)
		}
		if n := strings.Count(out, agents.NativeNote("codex")); n != 1 || !strings.Contains(out, "wt: native mode") {
			t.Errorf("the native-mode note appears %d times, want 1:\n%s", n, out)
		}
	})
	t.Run("-W", func(t *testing.T) {
		r := storedNativeHome(t)
		r.appendConfig(strayURL)
		repo := initTestRepo(t)
		t.Chdir(repo)
		out, code := r.run(t, "-W", "feat-n", "-A", "claude")
		if code != 0 || !r.ranAgent("claude") || !fileExists(filepath.Join(repo, ".worktrees", "feat-n")) {
			t.Errorf("exit %d, ran %v; want the native launch in the new worktree\n%s", code, r.ranAgent("claude"), out)
		}
	})
	for _, tc := range []struct {
		name  string
		args  []string
		agent string
	}{
		{"picker, no agent", nil, ""},
		{"picker, agent pinned", []string{"-A", "claude"}, "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := storedNativeHome(t)
			r.appendConfig(strayURL)
			t.Chdir(initTestRepo(t))
			stubStdinTTY(t, true)
			maybeInstallGuard = func() {}
			out, code := r.run(t, tc.args...)
			if code != 0 || len(r.tui) != 1 || r.tui[0].models != nil || r.tui[0].agent != tc.agent {
				t.Errorf("exit %d, tui %+v; want one picker with no client for agent %q\n%s", code, r.tui, tc.agent, out)
			}
		})
	}
}

// TestStoredNativeStillRefusesAPinnedModel pins that native mode keeps #615's
// refusals: -M (an empty one included), -T and -F ask for a model from a
// catalog native mode does not have, so each refuses, exit 1, with the one
// native-mode line pointing at wt config, and nothing runs.
func TestStoredNativeStillRefusesAPinnedModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"-M", []string{"-M", "openrouter/x/y"}},
		{"-M empty", []string{"-M", ""}},
		{"-T", []string{"-T", "code"}},
		{"-F", []string{"-F", "qwen"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := storedNativeHome(t)
			out, code := r.run(t, append([]string{"--cwd", "-A", "claude"}, tc.args...)...)
			want := "wt: " + nativeRefusalText(launchStill)
			if lines := wtLines(out); code != 1 || len(lines) != 1 || lines[0] != want {
				t.Errorf("exit %d, wt lines %q; want 1 and exactly %q", code, lines, want)
			}
			if !strings.Contains(out, "wt config") || r.ranAgent("claude") || len(r.builds) != 0 {
				t.Errorf("ran %v, builds %d; want a refusal naming wt config and nothing run\n%s", r.ranAgent("claude"), len(r.builds), out)
			}
		})
	}
}

// TestEnvPairOverridesAStoredNative pins rule 2 of the effective mode: a
// WT_MODEL_URL plus a token in the environment is remote over a stored
// native, so a script that points one invocation at a server keeps working
// on a machine whose stored choice is native.
func TestEnvPairOverridesAStoredNative(t *testing.T) {
	storedNativeHome(t)
	f := newFakeModelServer(t)
	t.Setenv("WT_MODEL_URL", f.URL)
	t.Setenv("WT_MODEL_TOKEN", f.Token)
	ms, err := (&app{hooks: testHooks()}).openModels(context.Background(), "x")
	if err != nil {
		t.Fatalf("openModels: %v", err)
	}
	if ms.mode != modelclient.ModeRemote || ms.ep.URL != f.URL {
		t.Errorf("mode %q url %q, want remote at %q", ms.mode, ms.ep.URL, f.URL)
	}
}

// TestWtModelModeEnvBeatsEverything pins rule 1: WT_MODEL_MODE is the
// explicit choice and wins over a stored mode and over an env endpoint pair.
// WT_MODEL_MODE=native beside a configured server and a full env pair
// launches natively; WT_MODEL_MODE=local over a stored native resolves the
// stored endpoint as local.
func TestWtModelModeEnvBeatsEverything(t *testing.T) {
	t.Run("native over a server and an env pair", func(t *testing.T) {
		r := newNativeRig(t)
		f := newFakeModelServer(t)
		tok := filepath.Join(t.TempDir(), "tok")
		if err := os.WriteFile(tok, []byte(f.Token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.appendConfig("\n[model-server]\nmode = \"remote\"\nurl = \"" + f.URL + "\"\ntoken_file = \"" + tok + "\"\n")
		t.Setenv("WT_MODEL_URL", f.URL)
		t.Setenv("WT_MODEL_TOKEN", f.Token)
		r.chooseNative()
		out, code := r.run(t, "--cwd", "-A", "claude")
		if code != 0 || !r.ranAgent("claude") || len(r.builds) != 1 || !r.builds[0].model.Native {
			t.Errorf("exit %d, ran %v, builds %+v; want a native launch\n%s", code, r.ranAgent("claude"), r.builds, out)
		}
		if n := len(f.reqs()); n != 0 {
			t.Errorf("the server was asked %d times, want none", n)
		}
	})
	t.Run("local over a stored native", func(t *testing.T) {
		r := newNativeRig(t)
		f := newFakeModelServer(t)
		tok := filepath.Join(t.TempDir(), "tok")
		if err := os.WriteFile(tok, []byte(f.Token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.appendConfig("\n[model-server]\nmode = \"native\"\nurl = \"" + f.URL + "\"\ntoken_file = \"" + tok + "\"\n")
		t.Setenv("WT_MODEL_MODE", "local")
		ms, err := (&app{hooks: testHooks()}).openModels(context.Background(), "x")
		if err != nil || ms.mode != modelclient.ModeLocal || ms.ep.URL != f.URL {
			t.Errorf("openModels = %+v, %v; want local at %q", ms, err, f.URL)
		}
	})
}

// TestStoredRemoteWithTheTokenFromTheEnvironmentIsValid pins validation on
// the merged values: mode = "remote" and a url in config.toml with no
// token_file, WT_MODEL_TOKEN set in the environment, resolves against the
// server rather than refusing for a missing token.
func TestStoredRemoteWithTheTokenFromTheEnvironmentIsValid(t *testing.T) {
	r := newNativeRig(t)
	f := newFakeModelServer(t)
	r.appendConfig("\n[model-server]\nmode = \"remote\"\nurl = \"" + f.URL + "\"\n")
	t.Setenv("WT_MODEL_TOKEN", f.Token)
	ms, err := (&app{hooks: testHooks()}).openModels(context.Background(), "x")
	if err != nil || ms.mode != modelclient.ModeRemote || ms.ep.URL != f.URL || ms.ep.Token != f.Token {
		t.Errorf("openModels = %+v, %v; want remote at %q with the env token", ms, err, f.URL)
	}
}

// TestStoredRemoteMissingEverythingRefusesNamingWtConfig pins that a stored
// remote or local mode with neither url nor token anywhere is a broken
// server, not a native machine: the launch refuses, exit 1, with
// NotConfiguredError's text (which names wt config), and nothing runs.
func TestStoredRemoteMissingEverythingRefusesNamingWtConfig(t *testing.T) {
	for _, mode := range []string{"remote", "local"} {
		t.Run(mode, func(t *testing.T) {
			r := newNativeRig(t)
			r.appendConfig("\n[model-server]\nmode = \"" + mode + "\"\n")
			out, code := r.run(t, "--cwd", "-A", "claude")
			want := "wt: " + notConfiguredText(launchStill)
			if lines := wtLines(out); code != 1 || len(lines) != 1 || lines[0] != want {
				t.Errorf("exit %d, wt lines %q; want 1 and exactly %q", code, lines, want)
			}
			if !strings.Contains(out, "wt config") || r.ranAgent("claude") || len(r.builds) != 0 || strings.Contains(out, "natively") {
				t.Errorf("ran %v, builds %d; want a refusal naming wt config\n%s", r.ranAgent("claude"), len(r.builds), out)
			}
		})
	}
}

// TestHalfConfiguredEnvOnlyStillRefuses pins Review Focus 2's last case for
// the one env-only half TestHalfConfiguredServerStillRefuses does not cover:
// WT_MODEL_TOKEN_FILE alone, nothing stored, infers remote and refuses naming
// the missing url — never an unset-mode refusal and never a native launch.
func TestHalfConfiguredEnvOnlyStillRefuses(t *testing.T) {
	r := newNativeRig(t)
	tok := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(tok, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_MODEL_TOKEN_FILE", tok)
	out, code := r.run(t, "--cwd", "-A", "claude")
	if code != 1 || !strings.Contains(out, "the model server url is not configured") || strings.Contains(out, "WT_MODEL_MODE") {
		t.Errorf("exit %d; want 1 naming the missing url, not the unset mode\n%s", code, out)
	}
	if r.ranAgent("claude") || len(r.builds) != 0 || strings.Contains(out, "secret") {
		t.Errorf("launched, or printed the token\n%s", out)
	}
}

// inlineTokenTable is a [model-server] table carrying a plaintext token and a
// url beside mode (no mode line when mode is ""): stray values for native,
// a problem for every other mode.
func inlineTokenTable(mode string) string {
	body := "\n[model-server]\n"
	if mode != "" {
		body += "mode = \"" + mode + "\"\n"
	}
	return body + "url = \"http://127.0.0.1:9\"\ntoken = \"inline-secret\"\n"
}

// TestStoredNativeIgnoresAStrayInlineToken pins the spec's "an explicit
// native ignores stray url/token values" for the inline token too: with
// mode = "native" beside a url and a plaintext token, a launch runs
// natively (nothing refused) and `wt stats` reads not_configured, never
// unavailable. One note says the token is ignored and should be removed,
// without its value. Refusing there would block every native launch over a
// value native never uses.
func TestStoredNativeIgnoresAStrayInlineToken(t *testing.T) {
	r := newNativeRig(t)
	r.appendConfig(inlineTokenTable("native"))
	out, code := r.run(t, "--cwd", "-A", "claude")
	if code != 0 || !r.ranAgent("claude") || len(r.builds) != 1 || !r.builds[0].model.Native {
		t.Fatalf("exit %d, ran %v, builds %+v; want a native launch\n%s", code, r.ranAgent("claude"), r.builds, out)
	}
	if n := strings.Count(out, "wt: note: config.toml: [model-server] token is ignored in native mode"); n != 1 {
		t.Errorf("the ignored-token note appears %d times, want 1:\n%s", n, out)
	}
	if strings.Contains(out, "inline-secret") {
		t.Errorf("the output carries the token:\n%s", out)
	}

	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := runStats(t, a, "--json")
	if !strings.Contains(stdout, `"`+spendNotConfigured+`"`) || strings.Contains(stdout, `"`+spendUnavailable+`"`) || strings.Contains(stderr+stdout, "inline-secret") {
		t.Errorf("stats: stdout %s stderr %q; want spend %q, never %q, and no token", stdout, stderr, spendNotConfigured, spendUnavailable)
	}
}

// TestInlineTokenStillRefusesOutsideNative pins the other side of that
// ruling so it cannot widen: the same plaintext token with no mode (a url
// makes it inferred remote), remote or local refuses the launch, exit 1,
// naming token_file and never the value, and nothing runs.
func TestInlineTokenStillRefusesOutsideNative(t *testing.T) {
	for _, mode := range []string{"", "remote", "local"} {
		t.Run("mode="+mode, func(t *testing.T) {
			r := newNativeRig(t)
			r.appendConfig(inlineTokenTable(mode))
			out, code := r.run(t, "--cwd", "-A", "claude")
			if code != 1 || !strings.Contains(out, "token_file") || strings.Contains(out, "inline-secret") {
				t.Errorf("exit %d; want 1 naming token_file without the value\n%s", code, out)
			}
			if r.ranAgent("claude") || len(r.builds) != 0 {
				t.Errorf("launched beside an inline token in mode %q\n%s", mode, out)
			}
		})
	}
	// With only the token (no url, no mode) the mode is unset: the token
	// problem is still what is said, as before the mode existed.
	r := newNativeRig(t)
	r.appendConfig("\n[model-server]\ntoken = \"inline-secret\"\n")
	if out, code := r.run(t, "--cwd", "-A", "claude"); code != 1 || !strings.Contains(out, "token_file") || r.ranAgent("claude") {
		t.Errorf("token only: exit %d; want 1 naming token_file\n%s", code, out)
	}
}

// TestEnvOnlyEndpointLaunchesAPinnedModel is the CI and llmbench shape end
// to end through the root command: no [model-server] table, no mode and no
// WT_MODEL_MODE, only WT_MODEL_URL and WT_MODEL_TOKEN. The env pair resolves
// remote against the fake server and `-M` launches that model through its
// gateway, exit 0, with no refusal. Scripts set exactly these two variables;
// the mode must not turn them into an unset-mode refusal.
func TestEnvOnlyEndpointLaunchesAPinnedModel(t *testing.T) {
	r := newNativeRig(t)
	f := newFakeModelServer(t, wireRow("openrouter/x/y", modelapi.ActionLaunch, 1))
	t.Setenv("WT_MODEL_URL", f.URL)
	t.Setenv("WT_MODEL_TOKEN", f.Token)
	out, code := r.run(t, "--cwd", "-A", "claude", "-M", "openrouter/x/y")
	if code != 0 || !r.ranAgent("claude") || len(r.builds) != 1 {
		t.Fatalf("exit %d, ran %v, builds %d; want one launch\n%s", code, r.ranAgent("claude"), len(r.builds), out)
	}
	if b := r.builds[0]; b.model.ID != "openrouter/x/y" || b.model.Native || b.gw == (agents.Gateway{}) {
		t.Errorf("build = %+v, want openrouter/x/y through the gateway", b)
	}
	if strings.Contains(out, "no model server is configured") || strings.Contains(out, "natively") {
		t.Errorf("a refusal or native note beside an env endpoint:\n%s", out)
	}
}
