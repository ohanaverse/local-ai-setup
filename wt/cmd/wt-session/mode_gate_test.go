package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/configeditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// stubEditorSideEffect replaces the config editor with one that counts its
// runs, records the tab it opened on and runs effect (the user's change in the
// editor, written to the fake config home only). It returns the run count and
// the last tab.
func stubEditorSideEffect(t *testing.T, effect func(run int)) (runs *int, tab *configeditor.Tab) {
	t.Helper()
	runs, tab = new(int), new(configeditor.Tab)
	old := configeditorRun
	configeditorRun = func(_ themes.Theme, _ *config.Config, _ error, o configeditor.Options) (configeditor.Result, error) {
		*runs++
		*tab = o.StartTab
		if effect != nil {
			effect(*runs)
		}
		return configeditor.Result{}, nil
	}
	t.Cleanup(func() { configeditorRun = old })
	return runs, tab
}

// appendModelServer appends a [model-server] table to the rig's config.toml.
func appendModelServer(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, ".config", "agent-wt", "config.toml")
	old, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(old, []byte("\n[model-server]\n"+body)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGateOpensTheModelServerTabOnATerminal pins the first-run gate: an unset
// mode on a terminal opens wt config on the Model server tab, and once the
// editor stored native the launch proceeds natively (one agent run). Why: a
// first launch should ask for the mode instead of just refusing.
func TestGateOpensTheModelServerTabOnATerminal(t *testing.T) {
	r := newNativeRig(t)
	stubStdinTTY(t, true)
	runs, tab := stubEditorSideEffect(t, func(int) { r.appendConfig("\n[model-server]\nmode = \"native\"\n") })
	out, code := r.run(t, "--cwd", "-A", "claude")
	if code != 0 || *runs != 1 || *tab != configeditor.TabServer || !r.ranAgent("claude") {
		t.Fatalf("exit %d, editor runs %d on tab %v, agent ran %v; want 0, 1, the Model server tab, true\n%s", code, *runs, *tab, r.ranAgent("claude"), out)
	}
	if n := strings.Count(out, "launching claude natively"); n != 1 {
		t.Errorf("native note %d times, want 1:\n%s", n, out)
	}
}

// TestGateDoesNothingWhenAModeIsStored: a stored mode of any kind skips the
// gate, terminal or not. Why: the gate is a first-run question only.
func TestGateDoesNothingWhenAModeIsStored(t *testing.T) {
	r := storedNativeHome(t)
	stubStdinTTY(t, true)
	runs, _ := stubEditorSideEffect(t, nil)
	out, code := r.run(t, "--cwd", "-A", "claude")
	if code != 0 || *runs != 0 || !r.ranAgent("claude") {
		t.Fatalf("exit %d, editor runs %d; want 0 and none\n%s", code, *runs, out)
	}
}

// TestGateWithoutATerminalRefusesWithTheFix: no terminal means the script's
// refusal (exit 1, wt config named, nothing launched), no editor, and -W
// creates nothing. Why: a script must never block or be asked.
func TestGateWithoutATerminalRefusesWithTheFix(t *testing.T) {
	r := newNativeRig(t)
	root := sharedRootRepo(t)
	t.Chdir(root)
	runs, _ := stubEditorSideEffect(t, nil)
	out, code := r.run(t, "-W", "gatebranch", "-A", "claude")
	if code != 1 || *runs != 0 || !strings.Contains(out, "wt config") || !strings.Contains(out, launchStill) || r.ranAgent("claude") {
		t.Fatalf("exit %d, editor runs %d\n%s", code, *runs, out)
	}
	wantNothingCreated(t, root, "gatebranch")
}

// TestGateLeavingTheTabUnsetEndsTheLaunch: the editor is opened exactly once,
// and a mode still unset after it ends the launch with exit 1 and its own
// line, creating nothing. Why: no loop, no guessing a mode.
func TestGateLeavingTheTabUnsetEndsTheLaunch(t *testing.T) {
	r := newNativeRig(t)
	root := sharedRootRepo(t)
	t.Chdir(root)
	stubStdinTTY(t, true)
	runs, _ := stubEditorSideEffect(t, nil)
	out, code := r.run(t, "-W", "gatebranch", "-A", "claude")
	if code != 1 || *runs != 1 || !slices.Contains(wtLines(out), "wt: no model-server mode chosen — nothing was launched") || r.ranAgent("claude") {
		t.Fatalf("exit %d, editor runs %d\n%s", code, *runs, out)
	}
	wantNothingCreated(t, root, "gatebranch")
}

// TestGateRunsBeforeAnyWorktreeIsCreated: with the editor leaving the mode
// unset, `-W newbranch` has made no branch or worktree, and when the editor
// chooses native the worktree is created only after it ran. Why: the gate
// precedes every side effect.
func TestGateRunsBeforeAnyWorktreeIsCreated(t *testing.T) {
	r := newNativeRig(t)
	root := sharedRootRepo(t)
	t.Chdir(root)
	stubStdinTTY(t, true)
	seen := false
	stubEditorSideEffect(t, func(int) {
		seen = fileExists(filepath.Join(root, ".worktrees", "gatebranch"))
		r.appendConfig("\n[model-server]\nmode = \"native\"\n")
	})
	out, code := r.run(t, "-W", "gatebranch", "-A", "claude")
	if seen {
		t.Error("the worktree existed when the editor opened")
	}
	if code != 0 || !fileExists(filepath.Join(root, ".worktrees", "gatebranch")) {
		t.Fatalf("exit %d, worktree missing after the mode was chosen\n%s", code, out)
	}
}

// TestGateSkipsCommandAgents: a command agent needs no model, so an unset
// mode neither opens the editor nor refuses. Why: `-A shell` works on a
// machine with no mode.
func TestGateSkipsCommandAgents(t *testing.T) {
	r := newNativeRig(t)
	stubStdinTTY(t, true)
	runs, _ := stubEditorSideEffect(t, nil)
	out, code := r.run(t, "--cwd", "-A", "shell")
	if *runs != 0 || strings.Contains(out, "no model") {
		t.Fatalf("exit %d, editor runs %d\n%s", code, *runs, out)
	}
}

// TestGateOpensTheEditorAtMostOnce: the first re-resolution still unset ends
// the launch; the editor never reopens. Why: no prompt loop.
func TestGateOpensTheEditorAtMostOnce(t *testing.T) {
	r := newNativeRig(t)
	stubStdinTTY(t, true)
	runs, _ := stubEditorSideEffect(t, nil)
	r.run(t, "--cwd", "-A", "claude", "-M", "x/y")
	if *runs != 1 {
		t.Errorf("editor opened %d times, want 1", *runs)
	}
}

// TestGateEditorChoosingRemoteProceedsToARemoteLaunch: a remote mode written
// by the editor, with the fake server up, launches through it. Why: the stale
// cached refusal must not survive the editor (resetModelCaches).
func TestGateEditorChoosingRemoteProceedsToARemoteLaunch(t *testing.T) {
	r := newNativeRig(t)
	stubStdinTTY(t, true)
	f := newFakeModelServer(t, modelapi.Row{ID: "openrouter/x/y", GatewayID: "openrouter/x/y", ProviderID: "openrouter", ModelName: "x/y",
		Location: "cloud", Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 1})
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte(f.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.hooks.newClient = fastModelClient
	var built []string
	prev := r.hooks.buildGateway
	r.hooks.buildGateway = func(agent string, m config.Model, dir string, yolo bool, gw agents.Gateway, models []config.Model, extra []string) (*exec.Cmd, agents.LaunchInfo, error) {
		built = append(built, m.ID)
		return prev(agent, m, dir, yolo, gw, models, extra)
	}
	runs, _ := stubEditorSideEffect(t, func(int) {
		r.appendConfig(fmt.Sprintf("\n[model-server]\nmode = \"remote\"\nurl = %q\ntoken_file = %q\n", f.URL, tok))
	})
	out, code := r.run(t, "--cwd", "-A", "claude", "-M", "openrouter/x/y")
	if code != 0 || *runs != 1 || len(built) != 1 || !r.ranAgent("claude") {
		t.Fatalf("exit %d, editor runs %d, builds %v\n%s", code, *runs, built, out)
	}
}

// TestOneShotCommandsRefuseNamingWtConfigAndNeverOpenTheEditor runs every
// server-needing command with no mode, on and off a terminal: exit 1, "wt
// config" named, its own still clause, and the editor never opens. Why: only a
// launch asks; a one-shot command refuses with the fix.
func TestOneShotCommandsRefuseNamingWtConfigAndNeverOpenTheEditor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		still string
	}{
		{"rotate", []string{"rotate", "code"}, "nothing was printed"},
		{"job", []string{"job", "job-1"}, "the job was not read"},
		{"start", []string{"start", "openrouter/x/y"}, "nothing was started"},
		{"stop", []string{"stop", "omlx/q"}, "nothing was stopped"},
		{"model list --server", []string{"model", "list", "--server"}, "nothing was listed"},
		{"smoke", []string{"smoke", "openrouter/x/y"}, smokeStill},
		{"profile -M profile/", []string{"profile", "show", "-A", "claude", "-M", "profile/base"}, showStill},
		{"profile slots add", []string{"profile", "slots", "add", "mine", "--slot", "sonnet=openrouter/x/y"}, slotEditStill},
		{"profile slots candidates", []string{"profile", "slots", "candidates"}, candidatesStill},
	} {
		for _, tty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tty=%v", tc.name, tty), func(t *testing.T) {
				home, _, h := noServerHome(t)
				if err := os.WriteFile(filepath.Join(home, ".config", "agent-wt", "slot-profiles.toml"), []byte(slotsBase), 0o644); err != nil {
					t.Fatal(err)
				}
				stubStdinTTY(t, tty)
				runs, _ := stubEditorSideEffect(t, nil)
				out, code := runRootWith(t, h, tc.args...)
				if code != 1 || *runs != 0 || !strings.Contains(out, "wt config") || !strings.Contains(out, tc.still) {
					t.Errorf("exit %d, editor runs %d, want 1, 0, wt config and %q\n%s", code, *runs, tc.still, out)
				}
			})
		}
	}
}

// TestBareWtWithoutATerminalKeepsThePickersOwnRefusal: bare `wt` with no
// terminal says the picker needs one and never opens the editor. Why: the
// gate must not replace the picker's refusal.
func TestBareWtWithoutATerminalKeepsThePickersOwnRefusal(t *testing.T) {
	_, _, h := noServerHome(t)
	t.Chdir(sharedRootRepo(t))
	runs, _ := stubEditorSideEffect(t, nil)
	out, code := runRootWith(t, h)
	if code != 1 || *runs != 0 || !strings.Contains(out, errPickerNeedsTTY.Error()) {
		t.Fatalf("exit %d, editor runs %d, want 1, 0 and %q\n%s", code, *runs, errPickerNeedsTTY, out)
	}
}

// TestResetModelCachesForgetsAFailure: a cached unset refusal is gone after
// resetModelCaches once config.toml names a mode. Why: the gate re-resolves
// after the editor and must not see its first answer again.
func TestResetModelCachesForgetsAFailure(t *testing.T) {
	r := newNativeRig(t)
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.buildModels(launchStill); !modelclient.IsModeUnset(err) {
		t.Fatalf("first read = %v, want the unset refusal", err)
	}
	r.appendConfig("\n[model-server]\nmode = \"native\"\n")
	if _, err := a.buildModels(launchStill); !modelclient.IsModeUnset(err) {
		t.Fatalf("cached read = %v, want the cached unset refusal", err)
	}
	a.resetModelCaches()
	if _, err := a.buildModels(launchStill); !modelclient.IsNativeMode(err) {
		t.Fatalf("read after reset = %v, want the native refusal", err)
	}
}
