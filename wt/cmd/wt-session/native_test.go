package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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
// recorded instead of opened.
type nativeRig struct {
	home   string
	ran    string // directory holding one marker per agent that ran
	builds []nativeBuild
	tui    []nativeTUICall
}

// nativeTUICall is one tuiRun the picker path made.
type nativeTUICall struct {
	agent  string
	pinned string
	models tui.ModelClient
	native bool
}

// ranAgent reports whether the fake binary of name ran.
func (r *nativeRig) ranAgent(name string) bool { return fileExists(filepath.Join(r.ran, name)) }

// newNativeRig builds the rig on noServerHome: the same scratch home, config
// and cleanup, with the agents' fake binaries added and the failing seams
// (gateway build, TUI) swapped for recorders that still build and run the real
// command.
func newNativeRig(t *testing.T) *nativeRig {
	t.Helper()
	home, _ := noServerHome(t)
	r := &nativeRig{home: home, ran: t.TempDir()}
	bin := t.TempDir()
	for _, name := range []string{"claude", "codex", "copilot", "pi", "opencode"} {
		script := "#!/bin/sh\ntouch '" + filepath.Join(r.ran, name) + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	buildGatewayCmd = func(agent string, m config.Model, dir string, yolo bool, gw agents.Gateway, models []config.Model, extra []string) (*exec.Cmd, agents.LaunchInfo, error) {
		cmd, info, err := agents.BuildLaunchCmdGateway(agent, m, dir, yolo, gw, models, extra)
		r.builds = append(r.builds, nativeBuild{agent: agent, model: m, gw: gw, yolo: yolo, dir: dir, args: extra})
		return cmd, info, err
	}
	tuiRun = func(_, _ bool, _ string, agent, pinned, _, _ string, _ []string, _ themes.Theme, _ string, models tui.ModelClient, native bool, _ *config.Config, _ tui.ProfileApplier, _ tui.SlotProfiles, _ tui.RemoveFuncsBuilder) error {
		r.tui = append(r.tui, nativeTUICall{agent: agent, pinned: pinned, models: models, native: native})
		return nil
	}
	return r
}

// run runs the root command with stderr and stdout captured (a launch prints
// its note to osStderr and its summary to stdout), returning everything.
func (r *nativeRig) run(t *testing.T, args ...string) (out string, code int) {
	t.Helper()
	notes := captureNotes(t)
	fileOut := captureFile(t, &os.Stdout, func() { out, code = runRoot(t, args...) })
	return out + fileOut + notes.String(), code
}

// TestLaunchWithoutAServerRunsEveryAgentNatively pins #615: with no model
// server configured at all, `wt -A <agent>` launches the agent itself, exit 0,
// with the native model and a zero Gateway (so no gateway token or origin can
// reach it), passes --yolo and the arguments after `--` on, prints exactly one
// "launching <agent> natively" note, and leaves no rotation or usage record.
// Before this a machine whose agent reaches its model by the agent's own
// configuration could not launch through wt at all.
func TestLaunchWithoutAServerRunsEveryAgentNatively(t *testing.T) {
	for _, name := range []string{"claude", "codex", "copilot", "pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			r := newNativeRig(t)
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
// native flag set (the picker then launches a chosen agent natively and never
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
			t.Chdir(initTestRepo(t))
			stubStdinTTY(t, true)
			maybeInstallGuard = func() {}
			out, code := r.run(t, tc.args...)
			if code != 0 || len(r.tui) != 1 {
				t.Fatalf("exit %d, tuiRun calls %d; want 0 and one\n%s", code, len(r.tui), out)
			}
			if c := r.tui[0]; c.models != nil || !c.native || c.agent != tc.agent {
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
