package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
	"github.com/spf13/cobra"
)

// startCall is what one engine call of a start carried.
type startCall struct{ replace, owed bool }

// stubLifecycleStartCalls replaces the engine with scripted outcomes (the nth
// call returns outcomes[n], nil past the end) and records AllowReplace and
// RestartOwed of each call.
func stubLifecycleStartCalls(t *testing.T, outcomes []error) *[]startCall {
	t.Helper()
	calls := &[]startCall{}
	old := lifecycleStart
	lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
		n := len(*calls)
		*calls = append(*calls, startCall{replace: opts.AllowReplace, owed: opts.RestartOwed})
		if n >= len(outcomes) {
			return nil
		}
		return outcomes[n]
	}
	t.Cleanup(func() { lifecycleStart = old })
	return calls
}

// TestStartForLaunchOwed verifies the start a profile launch uses. It passes
// the owed proxy restart to both engine attempts, and reports the restart as
// still owed only when it returns after a refusal it did not retry: then the
// engine touched nothing, and the caller must settle the restart itself. If
// it reported false there, the cloud routes written before the start would
// stay unpublished; if it reported true after the engine had restarted, the
// proxy would restart twice.
func TestStartForLaunchOwed(t *testing.T) {
	occupied := func() error {
		return &lifecycle.OccupiedError{Occupants: []localmodels.Entry{localmodelsEntry("omlx/other", "other")}}
	}
	unknown := func() error {
		return &lifecycle.OccupancyUnknownError{ProviderID: "mtplx", Origin: "http://localhost:8003"}
	}
	boom := errors.New("server exited")
	cases := []struct {
		name      string
		owed      bool
		replace   bool // --replace
		tty       bool
		answer    bool // the y/N answer when tty
		outcomes  []error
		wantCalls []startCall
		wantStill bool
		wantErr   string // "" means nil
	}{
		{"started at once", true, false, false, false, []error{nil}, []startCall{{false, true}}, false, ""},
		{"nothing owed is passed on as false", false, false, false, false, []error{nil}, []startCall{{false, false}}, false, ""},
		{"refused, replaced under --replace", true, true, false, false, []error{occupied(), nil}, []startCall{{false, true}, {true, true}}, false, ""},
		{"refused, confirmed at the prompt", true, false, true, true, []error{occupied(), nil}, []startCall{{false, true}, {true, true}}, false, ""},
		{"refused, declined at the prompt", true, false, true, false, []error{occupied()}, []startCall{{false, true}}, true, "cancelled — omlx/other still running"},
		{"refused, no terminal to ask on", true, false, false, false, []error{occupied()}, []startCall{{false, true}}, true, "--replace"},
		{"occupancy unknown, declined", true, false, true, false, []error{unknown()}, []startCall{{false, true}}, true, "cancelled — mtplx may still be serving a model"},
		{"refused and declined with nothing owed", false, false, true, false, []error{occupied()}, []startCall{{false, false}}, false, "cancelled"},
		{"any other failure", true, false, false, false, []error{boom}, []startCall{{false, true}}, false, "server exited"},
		{"replace granted, second attempt fails", true, true, false, false, []error{occupied(), boom}, []startCall{{false, true}, {true, true}}, false, "server exited"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubSignals(t)
			stubStdinTTY(t, tc.tty)
			oldConfirm, oldOut := confirmReplace, osStderr
			confirmReplace = func(string) (bool, error) { return tc.answer, nil }
			osStderr = io.Discard
			t.Cleanup(func() { confirmReplace, osStderr = oldConfirm, oldOut })
			calls := stubLifecycleStartCalls(t, tc.outcomes)

			still, err := startForLaunchOwed(&config.Config{}, startTestRow(), tc.replace, tc.owed)

			if still != tc.wantStill {
				t.Errorf("stillOwed = %v, want %v", still, tc.wantStill)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if len(*calls) != len(tc.wantCalls) {
				t.Fatalf("engine calls = %+v, want %+v", *calls, tc.wantCalls)
			}
			for i, want := range tc.wantCalls {
				if (*calls)[i] != want {
					t.Errorf("engine call %d = %+v, want %+v", i, (*calls)[i], want)
				}
			}
		})
	}
}

// slotFixtureConfig is a config with agent "claude" (providers "openrouter",
// "omlx"), cloud models "openrouter/a" and "openrouter/b", local model
// "omlx/q" (model_name "q"), and [litellm] url "http://localhost:4000",
// key "sk-test-1234", enabled false. Under TestMain's inventory stub
// (localmodels.OnDiskSnapshotForTest) "omlx/q" is on disk and not running:
// a start row.
func slotFixtureConfig() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Family: "a", Tags: []string{"code"}},
			{ID: "openrouter/b", ProviderID: "openrouter", ModelName: "b", Family: "b", Tags: []string{"code"}},
			{ID: "omlx/q", ProviderID: "omlx", ModelName: "q", Family: "q", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"openrouter", "omlx"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000", APIKey: "sk-test-1234"})
	return cfg
}

// slotFixtureCheck is a Usable check "profile/<name>" for agent "claude",
// with claude's four slots declared and no static error.
func slotFixtureCheck(index int, name, def string, slots map[string]string) profiles.SlotCheck {
	return profiles.SlotCheck{
		Index:    index,
		Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: def, Slots: slots},
		Usable:   true,
		Declared: []string{"fable", "opus", "sonnet", "haiku"},
	}
}

// slotCloud and slotMixed are the two slot maps most tests use. The default
// slot is "opus" in both, so the distinct models come out in the order
// written here: openrouter/a first.
func slotCloud() map[string]string {
	return map[string]string{"opus": "openrouter/a", "haiku": "openrouter/b"}
}

func slotMixed() map[string]string {
	return map[string]string{"opus": "openrouter/a", "haiku": "omlx/q"}
}

// stubSlotChecks sets launchSlotChecks for one test.
func stubSlotChecks(t *testing.T, checks ...profiles.SlotCheck) {
	t.Helper()
	old := launchSlotChecks
	launchSlotChecks = checks
	t.Cleanup(func() { launchSlotChecks = old })
}

// slotSeams records, in order, what a profile launch asked of the engine:
// "ensure:<id>,<id>", "start:<id>:owed=<bool>", "restart", "wait" (for a
// proxy restart in flight), "probe", "settle", "build". Its fields script each seam's result.
type slotSeams struct {
	events     []string
	ensureOwed bool // what ensureSlotRoutes reports as owed when it adds nothing to owedIn
	ensureErr  error
	// ensureErrOn, when set, scripts the error of one ensure call by the ids
	// it was given, so a test can fail the second call and not the first.
	ensureErrOn func(ids []string) error
	// startReplace is the replace argument of each startSlotModel call.
	startReplace []bool
	// startedOwed and startedErr script the ensure made after the start stub
	// ran: the check of the started local model's route.
	startedOwed bool
	startedErr  error
	started     bool
	startStill  bool
	startErr    error
	restartErr  error
	probeErr    error
	buildErr    error
	cmd         *exec.Cmd // what buildSlotCmd returns; default exec.Command("true")
	// ensured is every id handed to ensureSlotRoutes, in call order.
	ensured []string
	// launch is the profile buildSlotCmd was given, nil until it runs.
	launch *profiles.SlotLaunch
}

// log is the recorded events joined with ";".
func (s *slotSeams) log() string { return strings.Join(s.events, ";") }

// stubSlotSeams swaps the six seams of slot_launch.go and waitPendingRoutes
// for recorders and silences osStderr. The command buildSlotCmd hands back runs in the launch's
// worktree, as the real one does.
func stubSlotSeams(t *testing.T) *slotSeams {
	t.Helper()
	s := &slotSeams{}
	oldEnsure, oldRestart, oldProbe, oldSettle := ensureSlotRoutes, restartProxyNow, probeProxyNow, settleRoutesNow
	oldStart, oldBuild, oldOut := startSlotModel, buildSlotCmd, osStderr
	oldWait := waitPendingRoutes
	waitPendingRoutes = func() { s.events = append(s.events, "wait") }
	ensureSlotRoutes = func(_ context.Context, _ io.Writer, _ *config.Config, models []config.Model, owedIn bool) (bool, error) {
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		s.events = append(s.events, "ensure:"+strings.Join(ids, ","))
		s.ensured = append(s.ensured, ids...)
		if s.ensureErrOn != nil {
			if err := s.ensureErrOn(ids); err != nil {
				return false, err
			}
		}
		if s.started {
			if s.startedErr != nil {
				return false, s.startedErr
			}
			return owedIn || s.startedOwed, nil
		}
		if s.ensureErr != nil {
			return false, s.ensureErr
		}
		return owedIn || s.ensureOwed, nil
	}
	restartProxyNow = func(context.Context, *config.Config) error {
		s.events = append(s.events, "restart")
		return s.restartErr
	}
	probeProxyNow = func(context.Context, *config.Config) error {
		s.events = append(s.events, "probe")
		return s.probeErr
	}
	settleRoutesNow = func(context.Context, *config.Config) { s.events = append(s.events, "settle") }
	startSlotModel = func(_ *config.Config, row catalog.Row, replace bool, owed bool) (bool, error) {
		s.events = append(s.events, fmt.Sprintf("start:%s:owed=%t", row.Model.ID, owed))
		s.startReplace = append(s.startReplace, replace)
		s.started = true
		return s.startStill, s.startErr
	}
	buildSlotCmd = func(sl profiles.SlotLaunch, worktreePath string, _ bool, _ *config.Config, _ []string) (*exec.Cmd, error) {
		s.events = append(s.events, "build")
		s.launch = &sl
		if s.buildErr != nil {
			return nil, s.buildErr
		}
		cmd := s.cmd
		if cmd == nil {
			cmd = exec.Command("true")
		}
		cmd.Dir = worktreePath
		return cmd, nil
	}
	osStderr = io.Discard
	t.Cleanup(func() {
		ensureSlotRoutes, restartProxyNow, probeProxyNow, settleRoutesNow = oldEnsure, oldRestart, oldProbe, oldSettle
		startSlotModel, buildSlotCmd, osStderr = oldStart, oldBuild, oldOut
		waitPendingRoutes = oldWait
	})
	return s
}

// slotLaunchEnv gives one test its own config home and worktree. It returns
// wt's state directory inside that home (where usage.jsonl, rotation.state,
// last-picked.state and refcount.jsonl would be written) and the worktree.
func slotLaunchEnv(t *testing.T) (stateDir, worktree string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	return filepath.Join(home, "agent-wt"), t.TempDir()
}

// slotRow resolves check against cfg with no inventory probe, which is enough
// for an all-cloud profile.
func slotRow(cfg *config.Config, check profiles.SlotCheck) catalog.Row {
	return catalog.ResolveSlotProfile(cfg, nil, check)
}

// TestApplySlotProfileAppliesArgsAndWrapper verifies the applier applies the
// two mechanisms the slot build does not deliver: the profile's args are
// appended to the command, and its wrapper then wraps the whole command line.
// Claude allows neither today; an agent that does must not lose them.
func TestApplySlotProfileAppliesArgsAndWrapper(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	fakeAgentOnPath(t, "wt-test-wrapper")
	check := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	check.Profile.Args = []string{"--effort", "high"}
	check.Profile.Wrapper = &profiles.WrapperSpec{Binary: "wt-test-wrapper", ArgsTemplate: []string{"run", "{{args}}"}}
	check.Profile.Env = map[string]string{"FROM_PROFILE": "1"}
	check.Profile.ConfigContent = map[string]any{"x": "1"}
	row := slotRow(slotFixtureConfig(), check)
	cmd := exec.Command("true", "--model", "opus")
	cmd.Dir = worktree

	cleanup, err := applySlotProfile(cmd, "claude", row)
	if err != nil {
		t.Fatalf("applySlotProfile() error = %v", err)
	}
	if cleanup == nil || cleanup() != nil {
		t.Error("cleanup must be a non-nil no-op")
	}
	if got, want := strings.Join(cmd.Args[1:], " "), "run --model opus --effort high"; got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	if filepath.Base(cmd.Path) != "wt-test-wrapper" {
		t.Errorf("cmd.Path = %q, want the wrapper binary", cmd.Path)
	}
	// env and config_content are the slot build's to deliver, not the
	// applier's: applied here too they would be set twice, once as a file.
	if slices.Contains(cmd.Env, "FROM_PROFILE=1") {
		t.Errorf("cmd.Env = %v: the applier must not apply the profile's env", cmd.Env)
	}
	if _, statErr := os.Stat(filepath.Join(worktree, ".claude")); !os.IsNotExist(statErr) {
		t.Error("the applier wrote under .claude in the worktree")
	}
}

// TestApplySlotProfileFailsClosed verifies an apply error is returned, not
// swallowed. The automatic-profile applier degrades to an unprofiled launch
// on a config error; for a slot profile that would start Claude Code without
// what the profile asked for, so the launch must stop.
func TestApplySlotProfileFailsClosed(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	check := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	check.Profile.Wrapper = &profiles.WrapperSpec{Binary: "wt-test-definitely-missing-binary-xyz", ArgsTemplate: []string{"{{args}}"}}
	cmd := exec.Command("true")
	cmd.Dir = worktree

	cleanup, err := applySlotProfile(cmd, "claude", slotRow(slotFixtureConfig(), check))
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v, want the wrapper-not-installed error", err)
	}
	if cleanup == nil {
		t.Error("cleanup must not be nil, even on an error")
	}

	if _, err := applySlotProfile(exec.Command("true"), "claude", catalog.Row{Model: config.Model{ID: "openrouter/a"}}); err == nil {
		t.Error("a model row was accepted: the applier must refuse a row that is not a slot profile")
	}
}

// TestApplySlotProfileSelfHealsALeftoverSettingsFile verifies the self-heal
// of a settings.local.json left behind by an earlier automatic-profile
// session still runs before a profile launch. Without it a profile session
// would start with that stale file in the worktree, and its env block could
// redirect the session.
func TestApplySlotProfileSelfHealsALeftoverSettingsFile(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rp := profiles.ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := profiles.ApplyConfigContent(exec.Command("claude"), "claude", worktree, rp); err != nil {
		t.Fatalf("seed an orphaned backup: %v", err)
	}
	cmd := exec.Command("true")
	cmd.Dir = worktree
	row := slotRow(slotFixtureConfig(), slotFixtureCheck(0, "cloud", "opus", slotCloud()))

	var err error
	stderr := captureFile(t, &os.Stderr, func() {
		_, err = applySlotProfile(cmd, "claude", row)
	})
	if err != nil {
		t.Fatalf("applySlotProfile() error = %v", err)
	}
	if !strings.Contains(stderr, "wt: restored a leftover profile-managed file from a previous session: "+target) {
		t.Errorf("stderr = %q, want the self-heal notice", stderr)
	}
	// The restore rewrites the file's JSON, so compare what it holds, not
	// its bytes: the user's own key is back and the old profile's is gone.
	data, rerr := os.ReadFile(target)
	if rerr != nil || !strings.Contains(string(data), `"hooks"`) || strings.Contains(string(data), `"env"`) {
		t.Errorf("target = %q (%v), want the user's hooks kept and the old profile's env block removed", data, rerr)
	}
}

// TestSlotProfileWritesNoSettingsFile verifies a Claude profile with
// config_content, run through the real slot build and the applier, writes
// nothing: no .claude directory in the worktree, and nothing in the config
// home except the self-heal check's lock file. config_content travels in the
// inline --settings argument, so an interrupted session leaves no proxy-only
// ids behind for a later bare `claude` run. The proxy key stays out of argv.
func TestSlotProfileWritesNoSettingsFile(t *testing.T) {
	stateDir, worktree := slotLaunchEnv(t)
	fakeAgentOnPath(t, "claude")
	cfg := slotFixtureConfig()
	check := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	check.Profile.Env = map[string]string{"MAX_THINKING_TOKENS": "4096"}
	check.Profile.ConfigContent = map[string]any{"modelOverrides": map[string]any{"claude-sonnet-4-6": "openrouter/b"}}
	row := slotRow(cfg, check)
	if row.Action() != catalog.ActionLaunch {
		t.Fatalf("fixture: the row is not a launch row: %s", row.BlockReason())
	}

	cmd, err := agents.BuildSlotLaunchCmd(row.Profile.Launch(), worktree, false, cfg, nil)
	if err != nil {
		t.Fatalf("BuildSlotLaunchCmd() error = %v", err)
	}
	cleanup, err := applySlotProfile(cmd, "claude", row)
	if err != nil {
		t.Fatalf("applySlotProfile() error = %v", err)
	}
	if cerr := cleanup(); cerr != nil {
		t.Fatalf("cleanup() error = %v", cerr)
	}

	i := slices.Index(cmd.Args, "--settings")
	if i < 0 || i+1 >= len(cmd.Args) || !strings.Contains(cmd.Args[i+1], "modelOverrides") {
		t.Errorf("args = %q, want config_content inside the --settings value", cmd.Args)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, "sk-test-1234") {
			t.Errorf("the proxy key is in argv: %q", a)
		}
	}
	if _, statErr := os.Stat(filepath.Join(worktree, ".claude")); !os.IsNotExist(statErr) {
		t.Error("a .claude directory exists in the worktree: a settings file was written")
	}
	entries, _ := os.ReadDir(worktree)
	if len(entries) != 0 {
		t.Errorf("the worktree holds %d entries, want none", len(entries))
	}
	// The one thing the applier may leave in the config home is the lock file
	// of the self-heal check (profile-backups/<key>.lock). A backup marker
	// there would mean a file was rewritten.
	_ = filepath.WalkDir(filepath.Dir(stateDir), func(path string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "profile-backups" && strings.HasSuffix(path, ".lock") {
			return nil
		}
		t.Errorf("the config home gained a file: %s", path)
		return nil
	})
}

// TestRunSlotAgentCmdPostExitFlow verifies what follows a profile session:
// the refcount lines are released, then the stop picker runs, then the
// summary line names the profile and the duration. There is no per-model
// price notice and no survey, since neither belongs to one model here. A
// stop picker that ran before the release would never offer the profile's
// own local model.
func TestRunSlotAgentCmdPostExitFlow(t *testing.T) {
	slotLaunchEnv(t)
	prevNotice, prevRelease, prevPicker := emitPriceNotice, releaseSession, runStopPicker
	t.Cleanup(func() { emitPriceNotice, releaseSession, runStopPicker = prevNotice, prevRelease, prevPicker })
	var order []string
	releaseSession = func() { order = append(order, "release") }
	runStopPicker = func(*config.Config) {
		order = append(order, "picker")
		fmt.Fprint(os.Stdout, "PICKER-MARKER\n")
	}
	emitPriceNotice = func(*config.Config) { order = append(order, "notice") }
	cfg := slotFixtureConfig()
	row := slotRow(cfg, slotFixtureCheck(0, "cloud", "opus", slotCloud()))
	cleaned := false

	var err error
	stdout := captureFile(t, &os.Stdout, func() {
		err = runSlotAgentCmd(exec.Command("true"), "claude", row, cfg, func() error { cleaned = true; return nil })
	})

	if err != nil {
		t.Fatalf("runSlotAgentCmd() error = %v", err)
	}
	if !cleaned {
		t.Error("the applier's cleanup was not called after the run")
	}
	if got := strings.Join(order, ","); got != "release,picker" {
		t.Errorf("order = %s, want release,picker (and no price notice)", got)
	}
	i, j := strings.Index(stdout, "PICKER-MARKER"), strings.Index(stdout, "\nwt: claude · profile/cloud · ")
	if i < 0 || j < 0 || i > j {
		t.Errorf("stdout = %q, want the stop picker before a summary line naming the profile", stdout)
	}
	if strings.Contains(stdout, "survey ·") {
		t.Errorf("stdout = %q, want no survey after a profile session", stdout)
	}
}

// TestRunSlotAgentCmdReturnsARunError verifies an agent that could not be
// run at all comes back as an error after the post-exit flow has run, so the
// refcount lines recorded for the launch are not left behind.
func TestRunSlotAgentCmdReturnsARunError(t *testing.T) {
	slotLaunchEnv(t)
	prevRelease := releaseSession
	t.Cleanup(func() { releaseSession = prevRelease })
	released := false
	releaseSession = func() { released = true }
	cfg := slotFixtureConfig()
	row := slotRow(cfg, slotFixtureCheck(0, "cloud", "opus", slotCloud()))

	var err error
	captureFile(t, &os.Stdout, func() {
		err = runSlotAgentCmd(exec.Command(filepath.Join(t.TempDir(), "no-such-agent")), "claude", row, cfg, func() error { return nil })
	})
	if err == nil {
		t.Fatal("err = nil, want the run error")
	}
	if !released {
		t.Error("the session's refcount lines were not released after a failed run")
	}
}

// runningQ is an inventory in which the fixture's local model is running.
func runningQ() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", ModelName: "q", Registered: true, Running: true, ArtifactKnown: true},
		},
	}
}

// pinSlotProfile runs the non-TUI launch of `-A claude -M <pin>` with extra
// passthrough args, as launchFiltered runs it.
func pinSlotProfile(worktree string, cfg *config.Config, pin string, extraArgs ...string) error {
	return launchFiltered("claude", worktree, cfg, false, "", "", pin, true, extraArgs, nil, nil)
}

// slotStateFiles are the files a recorded launch writes under wt's state
// directory.
var slotStateFiles = []string{"usage.jsonl", "rotation.state", "last-picked.state", "refcount.jsonl"}

// TestLaunchSlotProfileSteps verifies the order of the launch flow and its
// restart count for each shape of profile. The cloud slots' routes are
// ensured first. An idle local model is then started and told whether a
// restart is owed, so its own restart publishes the cloud routes; a running
// one only has its route ensured. The proxy is restarted once when a write
// is still owed and never otherwise, and it is probed on every launch before
// the command is built. Two restarts would cost the user 20 to 40 seconds; a
// missing one would start Claude Code against a proxy that does not know the
// slot's id. Each case also checks that the ids handed to the route writes
// are exactly the distinct models the built payload's four slot variables
// name: a slot model left out would be refused by the proxy mid-session.
func TestLaunchSlotProfileSteps(t *testing.T) {
	cases := []struct {
		name       string
		slots      map[string]string
		running    bool // the local model is already running
		ensureOwed bool // a route write changed config.yaml
		want       string
		// startedOwed: the route check after the start had to write the row.
		startedOwed bool
	}{
		{"all cloud, every route exists", slotCloud(), false, false,
			"ensure:openrouter/a,openrouter/b;wait;probe;build", false},
		{"all cloud, a route was missing", slotCloud(), false, true,
			"ensure:openrouter/a,openrouter/b;restart;wait;probe;build", false},
		{"idle local model, a cloud route was missing", slotMixed(), false, true,
			"ensure:openrouter/a;start:omlx/q:owed=true;ensure:omlx/q;wait;probe;build", false},
		{"idle local model, routes in place", slotMixed(), false, false,
			"ensure:openrouter/a;start:omlx/q:owed=false;ensure:omlx/q;wait;probe;build", false},
		{"idle local model, the start left its route unwritten", slotMixed(), false, false,
			"ensure:openrouter/a;start:omlx/q:owed=false;ensure:omlx/q;restart;wait;probe;build", true},
		{"running local model, routes in place", slotMixed(), true, false,
			"ensure:openrouter/a;ensure:omlx/q;wait;probe;build", false},
		{"running local model, a route was missing", slotMixed(), true, true,
			"ensure:openrouter/a;ensure:omlx/q;restart;wait;probe;build", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, worktree := slotLaunchEnv(t)
			if tc.running {
				stubProbeInventory(t, runningQ())
			}
			seams := stubSlotSeams(t)
			seams.ensureOwed, seams.startedOwed = tc.ensureOwed, tc.startedOwed
			sentinel := filepath.Join(t.TempDir(), "agent-ran")
			seams.cmd = exec.Command("touch", sentinel)
			stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", tc.slots))

			var err error
			captureFile(t, &os.Stdout, func() {
				err = pinSlotProfile(worktree, slotFixtureConfig(), "profile/p")
			})

			if err != nil {
				t.Fatalf("launch error = %v", err)
			}
			if got := seams.log(); got != tc.want {
				t.Errorf("steps = %s\n want  %s", got, tc.want)
			}
			if n := strings.Count(seams.log(), "restart"); n > 1 {
				t.Errorf("%d proxy restarts, want at most one", n)
			}
			if _, statErr := os.Stat(sentinel); statErr != nil {
				t.Error("the agent command did not run")
			}
			// What was routed is what the agent is told to use: the ids
			// handed to the route writes are the distinct values of the
			// built payload's four slot variables, no more and no fewer.
			if seams.launch == nil {
				t.Fatal("the slot command was never built")
			}
			sc, berr := agents.BuildSlotCommand(*seams.launch, false, "http://localhost:4000", "sk-test-1234")
			if berr != nil {
				t.Fatalf("BuildSlotCommand() error = %v", berr)
			}
			var payload []string
			for _, v := range slotVarsOf(t, slotSettingsOf(t, sc.Args)) {
				payload = append(payload, v)
			}
			slices.Sort(payload)
			payload = slices.Compact(payload)
			ensured := slices.Clone(seams.ensured)
			slices.Sort(ensured)
			ensured = slices.Compact(ensured)
			if !slices.Equal(ensured, payload) {
				t.Errorf("ids routed = %v, payload slot ids = %v: want the same set", ensured, payload)
			}
		})
	}
}

// TestLaunchSlotProfilePassesReplaceToTheStart verifies the non-TUI launch
// hands the process-wide --replace to the start of the profile's local model,
// and hands false without it. If it always passed false,
// `claude-wt -W x -M profile/mixed --replace` against an occupied server
// would be refused for want of the flag it was given; if it always passed
// true, a launch without the flag would stop a running model unasked.
func TestLaunchSlotProfilePassesReplaceToTheStart(t *testing.T) {
	for _, replace := range []bool{true, false} {
		t.Run(fmt.Sprintf("replace=%t", replace), func(t *testing.T) {
			_, worktree := slotLaunchEnv(t)
			old := allowReplace
			allowReplace = replace
			t.Cleanup(func() { allowReplace = old })
			seams := stubSlotSeams(t)
			stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", slotMixed()))

			var err error
			captureFile(t, &os.Stdout, func() {
				err = pinSlotProfile(worktree, slotFixtureConfig(), "profile/p")
			})

			if err != nil {
				t.Fatalf("launch error = %v", err)
			}
			if !slices.Equal(seams.startReplace, []bool{replace}) {
				t.Errorf("start replace arguments = %v, want one start given %t", seams.startReplace, replace)
			}
		})
	}
}

// TestLaunchSlotProfileRecordsTheProfile verifies the bookkeeping of a profile
// launch: one usage event under the profile's id, last-picked.state naming
// the profile, rotation.state byte-identical, and one refcount line per
// distinct slot model (a model filling two slots is counted once). The
// profile then shows its launches in the picker and in `wt stats`, the
// rotation of single-model launches is not disturbed, and another session's
// stop picker does not offer a model this session uses.
func TestLaunchSlotProfileRecordsTheProfile(t *testing.T) {
	stateDir, worktree := slotLaunchEnv(t)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rotationState := filepath.Join(stateDir, "rotation.state")
	if err := os.WriteFile(rotationState, []byte("openrouter/b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubSlotSeams(t)
	stubSlotChecks(t, slotFixtureCheck(0, "cloud", "opus",
		map[string]string{"opus": "openrouter/a", "sonnet": "openrouter/a", "haiku": "openrouter/b"}))

	var err error
	captureFile(t, &os.Stdout, func() {
		err = pinSlotProfile(worktree, slotFixtureConfig(), "profile/cloud")
	})
	if err != nil {
		t.Fatalf("launch error = %v", err)
	}

	counts := usage.NewStoreAt(stateDir).CountsForAgent("claude", []string{"profile/cloud", "openrouter/a"})
	if counts["profile/cloud"].ThirtyDay != 1 || counts["openrouter/a"].ThirtyDay != 0 {
		t.Errorf("usage = %+v, want one launch under profile/cloud and none under a slot model", counts)
	}
	if data, _ := os.ReadFile(filepath.Join(stateDir, "last-picked.state")); string(data) != "profile/cloud\n" {
		t.Errorf("last-picked.state = %q, want %q", data, "profile/cloud\n")
	}
	if data, _ := os.ReadFile(rotationState); string(data) != "openrouter/b\n" {
		t.Errorf("rotation.state = %q, want it byte-identical (%q)", data, "openrouter/b\n")
	}
	refs := refcount.NewStoreAt(stateDir).Counts([]string{"openrouter/a", "openrouter/b", "profile/cloud"})
	if refs["openrouter/a"] != 1 || refs["openrouter/b"] != 1 || refs["profile/cloud"] != 0 {
		t.Errorf("refcounts = %v, want one line per distinct slot model and none under the profile id", refs)
	}
}

// TestSlotProfileFailuresRunNoAgent is the fail-closed table: each row of the
// spec's failure table ends with an error, no agent command run, and nothing
// recorded. A failure that degraded to a launch would start Claude Code with
// some slots still sending Anthropic ids, which the proxy rejects.
func TestSlotProfileFailuresRunNoAgent(t *testing.T) {
	one := func(c profiles.SlotCheck) func() []profiles.SlotCheck {
		return func() []profiles.SlotCheck { return []profiles.SlotCheck{c} }
	}
	cloud := func() profiles.SlotCheck { return slotFixtureCheck(0, "p", "opus", slotCloud()) }
	mixed := func() profiles.SlotCheck { return slotFixtureCheck(0, "p", "opus", slotMixed()) }
	notEligible := `model "profile/p" is not in the eligible list for agent "claude"`
	cases := []struct {
		name   string
		checks func() []profiles.SlotCheck // nil: no profile loaded
		cfg    func(*config.Config)
		seams  func(*slotSeams)
		// running: the profile's local model is already running, so the
		// launch takes step 5 and no start.
		running bool
		args    []string
		want    string // a substring of the error
		steps   string // the engine steps that ran before the abort
	}{
		{name: "slot-profiles.toml does not parse", want: notEligible},
		{name: "a profile has no usable name",
			checks: func() []profiles.SlotCheck {
				return []profiles.SlotCheck{{Profile: profiles.SlotProfile{Agent: "claude"}, Err: errors.New("name is missing")}}
			},
			want: notEligible},
		{name: "two profiles share a name",
			checks: func() []profiles.SlotCheck {
				a, b := cloud(), cloud()
				b.Index = 1
				a.Duplicate, b.Duplicate = true, true
				a.Err, b.Err = profiles.ErrSlotDuplicate, profiles.ErrSlotDuplicate
				return []profiles.SlotCheck{a, b}
			},
			want: "duplicate profile name"},
		{name: "a profile fails static validation",
			checks: func() []profiles.SlotCheck {
				c := cloud()
				c.Err = errors.New(`default "x" is not a slot this profile fills`)
				return []profiles.SlotCheck{c}
			},
			want: `default "x" is not a slot this profile fills`},
		{name: "a profile fails resolved validation",
			checks: one(slotFixtureCheck(0, "p", "opus", map[string]string{"opus": "openrouter/gone"})),
			want:   "slot opus: openrouter/gone is not in the registry"},
		{name: "the registry has a provider with id profile",
			checks: one(cloud()),
			cfg: func(c *config.Config) {
				c.Providers = append(c.Providers, config.Provider{ID: "profile", Location: config.LocationCloud})
			},
			want: notEligible},
		{name: "a registry model id begins with profile/",
			checks: one(cloud()),
			cfg: func(c *config.Config) {
				c.Models = append(c.Models, config.Model{ID: "profile/other", ProviderID: "openrouter", ModelName: "other"})
			},
			want: notEligible},
		{name: "LiteLLM has no url",
			checks: one(cloud()),
			cfg:    func(c *config.Config) { c.SetLitellmForTest(config.LitellmState{APIKey: "sk-test-1234"}) },
			want:   "no URL is configured"},
		{name: "LiteLLM has no api key",
			checks: one(cloud()),
			cfg:    func(c *config.Config) { c.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000"}) },
			want:   "no API key is configured"},
		{name: "passthrough args contain --settings",
			checks: one(cloud()), args: []string{"--settings", "x.json"},
			want: "a slot profile sets --settings itself"},
		{name: "passthrough args contain --model=",
			checks: one(cloud()), args: []string{"--model=opus"},
			want: "a slot profile sets --model itself"},
		{name: "a slot's route cannot be written",
			checks: one(cloud()),
			seams: func(s *slotSeams) {
				s.ensureErr = &lifecycle.RouteEnsureError{ModelID: "openrouter/a", Err: litellm.ErrMissing}
			},
			want:  "slot opus: LiteLLM route for openrouter/a not written: LiteLLM config not found",
			steps: "ensure:openrouter/a,openrouter/b"},
		{name: "the local model's start is refused with a restart owed",
			checks: one(mixed()),
			seams: func(s *slotSeams) {
				s.ensureOwed, s.startStill = true, true
				s.startErr = errors.New("cancelled — omlx/other still running")
			},
			want:  "cancelled — omlx/other still running",
			steps: "ensure:openrouter/a;start:omlx/q:owed=true;settle"},
		{name: "the local model's start fails",
			checks: one(mixed()),
			seams:  func(s *slotSeams) { s.startErr = errors.New("failed to start omlx/q: server exited") },
			want:   "failed to start omlx/q",
			steps:  "ensure:openrouter/a;start:omlx/q:owed=false"},
		{name: "Ctrl+C lost to the local model's start",
			checks: one(mixed()),
			seams:  func(s *slotSeams) { s.startErr = errors.New("started omlx/q; launch cancelled") },
			want:   "started omlx/q; launch cancelled",
			steps:  "ensure:openrouter/a;start:omlx/q:owed=false"},
		{name: "the started local model's route cannot be written",
			checks: one(mixed()),
			seams: func(s *slotSeams) {
				s.startedErr = &lifecycle.RouteEnsureError{ModelID: "omlx/q", Err: litellm.ErrMissing}
			},
			want:  "slot haiku: LiteLLM route for omlx/q not written: LiteLLM config not found",
			steps: "ensure:openrouter/a;start:omlx/q:owed=false;ensure:omlx/q"},
		{name: "a running local model's route cannot be written (step 5)",
			checks: one(mixed()), running: true,
			seams: func(s *slotSeams) {
				s.ensureOwed = true
				s.ensureErrOn = func(ids []string) error {
					if slices.Contains(ids, "omlx/q") {
						return &lifecycle.RouteEnsureError{ModelID: "omlx/q", Err: litellm.ErrMissing}
					}
					return nil
				}
			},
			want:  "slot haiku: LiteLLM route for omlx/q not written: LiteLLM config not found",
			steps: "ensure:openrouter/a;ensure:omlx/q"},
		{name: "the proxy restart fails",
			checks: one(cloud()),
			seams: func(s *slotSeams) {
				s.ensureOwed = true
				s.restartErr = errors.New("LiteLLM proxy not ready at http://localhost:4000: timed out")
			},
			want:  "LiteLLM proxy not ready",
			steps: "ensure:openrouter/a,openrouter/b;restart"},
		{name: "the proxy does not answer the probe",
			checks: one(cloud()),
			seams: func(s *slotSeams) {
				s.probeErr = errors.New("the LiteLLM proxy does not answer at http://localhost:4000: connection refused")
			},
			want:  "does not answer at http://localhost:4000",
			steps: "ensure:openrouter/a,openrouter/b;wait;probe"},
		{name: "the command cannot be built",
			checks: one(cloud()),
			seams:  func(s *slotSeams) { s.buildErr = errors.New("agent claude not installed") },
			want:   "agent claude not installed",
			steps:  "ensure:openrouter/a,openrouter/b;wait;probe;build"},
		{name: "the wrapper cannot be applied",
			checks: func() []profiles.SlotCheck {
				c := cloud()
				c.Profile.Wrapper = &profiles.WrapperSpec{Binary: "wt-test-definitely-missing-binary-xyz", ArgsTemplate: []string{"{{args}}"}}
				return []profiles.SlotCheck{c}
			},
			want:  "not installed",
			steps: "ensure:openrouter/a,openrouter/b;wait;probe;build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, worktree := slotLaunchEnv(t)
			if tc.running {
				stubProbeInventory(t, runningQ())
			}
			seams := stubSlotSeams(t)
			sentinel := filepath.Join(t.TempDir(), "agent-ran")
			seams.cmd = exec.Command("touch", sentinel)
			if tc.seams != nil {
				tc.seams(seams)
			}
			if tc.checks != nil {
				stubSlotChecks(t, tc.checks()...)
			} else {
				stubSlotChecks(t)
			}
			cfg := slotFixtureConfig()
			if tc.cfg != nil {
				tc.cfg(cfg)
			}

			var err error
			stdout := captureFile(t, &os.Stdout, func() {
				err = pinSlotProfile(worktree, cfg, "profile/p", tc.args...)
			})

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
				t.Error("the agent command ran after a failure")
			}
			if got := seams.log(); got != tc.steps {
				t.Errorf("steps = %q, want %q", got, tc.steps)
			}
			if strings.Contains(stdout, "wt: claude ·") {
				t.Errorf("stdout = %q, want no summary line: no session ran", stdout)
			}
			for _, name := range slotStateFiles {
				if _, statErr := os.Stat(filepath.Join(stateDir, name)); !os.IsNotExist(statErr) {
					t.Errorf("%s was written although the launch aborted", name)
				}
			}
		})
	}
}

// TestSlotProfileRouteErrorKeepsItsCause verifies the abort for a route that
// could not be written names the slot and still carries the cause on its
// chain, so a caller can tell a missing config.yaml from a lock timeout.
func TestSlotProfileRouteErrorKeepsItsCause(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	seams := stubSlotSeams(t)
	seams.ensureErr = &lifecycle.RouteEnsureError{ModelID: "openrouter/b", Err: litellm.ErrMissing}
	stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", slotCloud()))

	err := pinSlotProfile(worktree, slotFixtureConfig(), "profile/p")
	if err == nil || !strings.HasPrefix(err.Error(), "slot haiku: ") {
		t.Fatalf("err = %v, want it to start with the slot that names openrouter/b", err)
	}
	if !errors.Is(err, litellm.ErrMissing) {
		t.Errorf("err = %v, want litellm.ErrMissing still on the chain", err)
	}
}

// TestSlotProfileStartErrorIsAStartError verifies a failed or refused start of
// the profile's local model is marked as a start's outcome. The root command
// reads that mark to print the message once; without it the message and the
// server's log tail would be printed twice.
func TestSlotProfileStartErrorIsAStartError(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	seams := stubSlotSeams(t)
	seams.startErr = errors.New("failed to start omlx/q: server exited")
	stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", slotMixed()))

	err := pinSlotProfile(worktree, slotFixtureConfig(), "profile/p")
	if !isStartError(err) {
		t.Errorf("err = %v, want it marked as a start error", err)
	}
}

// TestSlotProfileLaunchSkipsAutomaticProfiles verifies a profile launch never
// resolves, prompts for or applies an automatic profile, even when
// profiles.toml has an entry that matches one of the slot models. The two
// kinds of profile do not stack: an automatic env or settings file applied on
// top could redirect a slot.
func TestSlotProfileLaunchSkipsAutomaticProfiles(t *testing.T) {
	stateDir, worktree := slotLaunchEnv(t)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[[profiles]]\nagent = \"claude\"\nmatch = \"provider\"\nprovider = \"openrouter\"\nenv = { WT_TEST_AUTO_PROFILE = \"1\" }\n"
	if err := os.WriteFile(filepath.Join(stateDir, "profiles.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := slotFixtureConfig()
	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load() error = %v", err)
	}
	if profiles.Resolve(store, "claude", cfg, cfg.Models[0]).Empty() {
		t.Fatal("fixture: the automatic profile does not match the slot model, so the test proves nothing")
	}
	loads, prompts := 0, 0
	oldLoad, oldConfirm := loadProfileStore, confirmProfile
	loadProfileStore = func() (profiles.Store, error) { loads++; return profiles.Load() }
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { prompts++; return true, nil }
	t.Cleanup(func() { loadProfileStore, confirmProfile = oldLoad, oldConfirm })
	seams := stubSlotSeams(t)
	cmd := exec.Command("true")
	seams.cmd = cmd
	stubSlotChecks(t, slotFixtureCheck(0, "cloud", "opus", slotCloud()))

	// The already-loaded store is handed in too, as runLaunchPath hands it to
	// every launch: the profile path must ignore it.
	pp := &precomputedProfiles{store: store}
	captureFile(t, &os.Stdout, func() {
		err = launchFiltered("claude", worktree, cfg, false, "", "", "profile/cloud", true, nil, nil, pp)
	})

	if err != nil {
		t.Fatalf("launch error = %v", err)
	}
	if loads != 0 || prompts != 0 {
		t.Errorf("profiles.toml loaded %d times and prompted %d times, want 0 and 0", loads, prompts)
	}
	if slices.Contains(cmd.Env, "WT_TEST_AUTO_PROFILE=1") {
		t.Errorf("cmd.Env = %v: the automatic profile was applied to a slot-profile launch", cmd.Env)
	}
}

// launchCobraCmd is a command with the two flags runLaunchPath reads.
func launchCobraCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().StringP("model", "M", "", "")
	cmd.Flags().Bool("yolo", false, "")
	return cmd
}

// restoreLaunchSlotChecks puts launchSlotChecks back when the test ends:
// runLaunchPath sets it for the whole process.
func restoreLaunchSlotChecks(t *testing.T) {
	t.Helper()
	old := launchSlotChecks
	t.Cleanup(func() { launchSlotChecks = old })
}

// TestSlotWarnings verifies the warning lines of a launch: the load error;
// one line per profile that has no usable name or agent, naming its position
// in the file; one line when the registry has a provider "profile" while
// a usable profile exists; and one line per modelOverrides target of a
// passing profile that is not one of its slot models (#412), the launch-time
// channel that warning reaches both launch paths through. A profile that
// merely fails validation gets no line here: it has a blocked row that
// carries its reason.
func TestSlotWarnings(t *testing.T) {
	usable := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	blocked := slotFixtureCheck(1, "broken", "opus", slotCloud())
	blocked.Err = errors.New("default is missing")
	unusable := profiles.SlotCheck{Index: 2, Profile: profiles.SlotProfile{Agent: "claude"}, Err: errors.New("name is missing")}
	reserved := slotFixtureConfig()
	reserved.Providers = append(reserved.Providers, config.Provider{ID: "profile", Location: config.LocationCloud})
	prefixed := slotFixtureConfig()
	prefixed.Models = append(prefixed.Models, config.Model{ID: "profile/mixed", ProviderID: "openrouter", ModelName: "mixed"})
	overriding := slotFixtureCheck(3, "overriding", "opus", slotCloud())
	overriding.Profile.ConfigContent = map[string]any{"modelOverrides": map[string]any{
		"claude-opus-4-1": "openrouter/a", "claude-sonnet-4-6": "openrouter/z"}}
	routedOnly := slotFixtureCheck(4, "routed-only", "opus", slotCloud())
	routedOnly.Profile.ConfigContent = map[string]any{"modelOverrides": map[string]any{"claude-opus-4-1": "openrouter/b"}}
	blockedOverriding := overriding
	blockedOverriding.Err = errors.New("default is missing")
	cases := []struct {
		name string
		a    *app
		want []string
	}{
		{"nothing wrong", &app{cfg: slotFixtureConfig(), slotChecks: []profiles.SlotCheck{usable, blocked}}, nil},
		{"a modelOverrides target that is not a slot model, once",
			&app{cfg: slotFixtureConfig(), slotChecks: []profiles.SlotCheck{usable, overriding, routedOnly}},
			[]string{`wt: profile/overriding (claude): config_content.modelOverrides["claude-sonnet-4-6"] targets "openrouter/z", which is not one of the profile's slot models, so wt does not make sure it is routed`}},
		{"a blocked profile's modelOverrides says nothing more",
			&app{cfg: slotFixtureConfig(), slotChecks: []profiles.SlotCheck{blockedOverriding}}, nil},
		{"no file", &app{cfg: slotFixtureConfig()}, nil},
		{"the file does not parse",
			&app{cfg: slotFixtureConfig(), slotLoadErr: errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")},
			[]string{"wt: parse slot-profiles.toml: toml: line 2: unexpected EOF — slot profiles are unavailable"}},
		{"a profile with no usable name",
			&app{cfg: slotFixtureConfig(), slotChecks: []profiles.SlotCheck{usable, unusable}},
			[]string{"wt: slot-profiles.toml[2]: name is missing — skipped (see 'wt profile list')"}},
		{"a file whose only content is under an unknown key",
			&app{cfg: slotFixtureConfig(), slotUnknown: []string{"profiles"}},
			[]string{`wt: slot-profiles.toml: unknown key "profiles" — ignored`}},
		{"unknown keys beside a usable profile, one line each",
			&app{cfg: slotFixtureConfig(), slotChecks: []profiles.SlotCheck{usable}, slotUnknown: []string{"top", "profile.envs"}},
			[]string{
				`wt: slot-profiles.toml: unknown key "top" — ignored`,
				`wt: slot-profiles.toml: unknown key "profile.envs" — ignored`,
			}},
		{"the reserved provider id",
			&app{cfg: reserved, slotChecks: []profiles.SlotCheck{usable}},
			[]string{`wt: the registry has a provider with id "profile" — slot profiles are unavailable`}},
		{"a registry model id with the reserved prefix",
			&app{cfg: prefixed, slotChecks: []profiles.SlotCheck{usable}},
			[]string{`wt: the registry has a model whose id begins with "profile/" (profile/mixed) — slot profiles are unavailable`}},
		{"the reserved provider id with no usable profile",
			&app{cfg: reserved, slotChecks: []profiles.SlotCheck{unusable}},
			[]string{"wt: slot-profiles.toml[2]: name is missing — skipped (see 'wt profile list')"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slotWarnings(tc.a); !slices.Equal(got, tc.want) {
				t.Errorf("slotWarnings() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunLaunchPathWarnsAboutSlotProfilesOncePerRun verifies who sees the
// warnings: a launch with no agent chosen yet, or with an agent that has
// slots, prints each line once; a shell launch prints none, since a slot
// profile could never apply to it.
func TestRunLaunchPathWarnsAboutSlotProfilesOncePerRun(t *testing.T) {
	cases := []struct {
		agent string
		want  int
	}{{"claude", 1}, {"", 1}, {"shell", 0}}
	for _, tc := range cases {
		t.Run("agent="+tc.agent, func(t *testing.T) {
			restoreLaunchSlotChecks(t)
			stubStdinTTY(t, false)
			oldLaunch, oldSweep := launchFiltered, sweepRefcounts
			launchFiltered = func(string, string, *config.Config, bool, string, string, string, bool, []string, []config.Model, *precomputedProfiles) error {
				return nil
			}
			sweepRefcounts = func() {}
			t.Cleanup(func() { launchFiltered, sweepRefcounts = oldLaunch, oldSweep })
			cfg := slotFixtureConfig()
			cfg.Agents = append(cfg.Agents, config.Agent{Name: "shell"})
			a := &app{cfg: cfg, slotLoadErr: errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")}

			stderr := captureFile(t, &os.Stderr, func() {
				// Without a pin an agent launch ends at "the picker needs a
				// TTY"; the warnings are printed before that.
				_ = runLaunchPath(launchCobraCmd(), a, tc.agent, "", "", "", nil, ".", "")
			})

			if got := strings.Count(stderr, "slot profiles are unavailable"); got != tc.want {
				t.Errorf("warning printed %d times, want %d; stderr = %q", got, tc.want, stderr)
			}
		})
	}
}

// TestRunLaunchPathRepeatsSlotWarningsAfterTheTUI verifies the warnings on
// the TUI path: they are printed before the TUI opens, where the alternate
// screen then covers them, and the same lines are printed again once the TUI
// has returned and the terminal is the user's again. Without the second
// print a user with a typo in slot-profiles.toml sees a picker with no
// profile rows and nothing on screen that says why. Both ways into the TUI
// are covered: the worktree picker (no launch path) and the model picker.
func TestRunLaunchPathRepeatsSlotWarningsAfterTheTUI(t *testing.T) {
	const warning = "wt: parse slot-profiles.toml: toml: line 2: unexpected EOF — slot profiles are unavailable\n" +
		"wt: slot-profiles.toml: unknown key \"top\" — ignored\n"
	cases := []struct{ name, launchPath string }{
		{"the worktree picker", ""},
		{"the model picker", "."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreLaunchSlotChecks(t)
			stubStdinTTY(t, true)
			oldTUI, oldSweep, oldLaunch := tuiRun, sweepRefcounts, launchFiltered
			launchFiltered = func(string, string, *config.Config, bool, string, string, string, bool, []string, []config.Model, *precomputedProfiles) error {
				t.Error("the launch went past the picker")
				return nil
			}
			tuiRun = func(bool, bool, string, string, string, string, string, []string, themes.Theme, string, *config.Config, tui.ProfileApplier, tui.SlotProfiles) error {
				fmt.Fprintln(os.Stderr, "<the TUI ran>")
				return errors.New("the TUI's own error")
			}
			sweepRefcounts = func() {}
			t.Cleanup(func() { tuiRun, sweepRefcounts, launchFiltered = oldTUI, oldSweep, oldLaunch })
			a := &app{cfg: slotFixtureConfig(), slotUnknown: []string{"top"},
				slotLoadErr: errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")}

			var err error
			stderr := captureFile(t, &os.Stderr, func() {
				err = runLaunchPath(launchCobraCmd(), a, "claude", "", "", "", nil, tc.launchPath, "")
			})

			if want := warning + "<the TUI ran>\n" + warning; stderr != want {
				t.Errorf("stderr =\n%s\nwant\n%s", stderr, want)
			}
			if err == nil || err.Error() != "the TUI's own error" {
				t.Errorf("err = %v, want the TUI's own error passed through", err)
			}
		})
	}
}

// TestBrokenSlotFileLeavesModelLaunchesAlone is Review Focus 1. With a TOML
// typo in slot-profiles.toml, or one profile with a malformed name, a pinned
// single-model launch and a rotated one behave exactly as before, wt prints
// exactly one warning line for the problem, and `-M profile/<name>` exits
// non-zero with the "not in the eligible list" message. A hand-written file
// must never be able to break the launches that do not use it.
func TestBrokenSlotFileLeavesModelLaunchesAlone(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		pin     string
		warning string
	}{
		{"a TOML typo", "[[profile]]\nname = \"mixed\n", "profile/mixed", "slot profiles are unavailable"},
		{"a malformed name",
			"[[profile]]\nname = \"Bad_Name\"\nagent = \"claude\"\ndefault = \"opus\"\n\n[profile.slots]\nopus = \"openrouter/a\"\n",
			"profile/Bad_Name", "skipped (see 'wt profile list')"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreLaunchSlotChecks(t)
			path := writeSlotProfilesTOML(t, tc.body)
			stateDir := filepath.Dir(path)
			worktree := t.TempDir()
			fakeAgentOnPath(t, "claude")
			stubEnsureRoute(t)
			stubSlotSeams(t)
			oldSweep := sweepRefcounts
			sweepRefcounts = func() {}
			t.Cleanup(func() { sweepRefcounts = oldSweep })

			// The app as wt builds it: newApp reads the slot file from the
			// test's config home. Only the registry is the fixture's.
			a, aerr := newApp()
			if aerr != nil {
				t.Fatalf("newApp() error = %v: a broken slot file must not stop wt", aerr)
			}
			a.cfg = slotFixtureConfig()

			rotationState := func() string {
				data, _ := os.ReadFile(filepath.Join(stateDir, "rotation.state"))
				return strings.TrimSpace(string(data))
			}

			// A pinned single-model launch: it runs, and the warning is
			// printed once.
			var err error
			var stderr string
			captureFile(t, &os.Stdout, func() {
				stderr = captureFile(t, &os.Stderr, func() {
					err = runLaunchPath(launchCobraCmd(), a, "claude", "openrouter/a", "", "", nil, worktree, "")
				})
			})
			if err != nil {
				t.Fatalf("pinned launch error = %v", err)
			}
			if got := rotationState(); got != "openrouter/a" {
				t.Errorf("rotation.state = %q after the pinned launch, want openrouter/a", got)
			}
			if got := strings.Count(stderr, tc.warning); got != 1 {
				t.Errorf("warning printed %d times, want exactly once; stderr = %q", got, stderr)
			}
			slotLines := 0
			for _, line := range strings.Split(stderr, "\n") {
				if strings.Contains(line, "slot") {
					slotLines++
				}
			}
			if slotLines != 1 {
				t.Errorf("%d stderr lines mention slot profiles, want one; stderr = %q", slotLines, stderr)
			}

			// A rotated launch: rotation advances to the next launchable
			// model, as it does with no slot file at all.
			captureFile(t, &os.Stdout, func() {
				err = launchFiltered("claude", worktree, a.cfg, false, "", "", "", false, nil, nil, nil)
			})
			if err != nil {
				t.Fatalf("rotated launch error = %v", err)
			}
			if got := rotationState(); got != "openrouter/b" {
				t.Errorf("rotation.state = %q after the rotated launch, want openrouter/b", got)
			}

			// The profile itself cannot be pinned.
			captureFile(t, &os.Stderr, func() {
				err = runLaunchPath(launchCobraCmd(), a, "claude", tc.pin, "", "", nil, worktree, "")
			})
			want := fmt.Sprintf("model %q is not in the eligible list for agent %q", tc.pin, "claude")
			if err == nil || err.Error() != want {
				t.Errorf("profile pin error = %v, want %q", err, want)
			}
		})
	}
}

// TestRunLaunchPathLaunchesAPinnedSlotProfile verifies the whole non-TUI path
// from runLaunchPath: `-A claude -M profile/<name>` with a worktree reaches
// the profile launch and runs the agent, and a pin of a blocked profile
// returns its reason, which main turns into a non-zero exit.
func TestRunLaunchPathLaunchesAPinnedSlotProfile(t *testing.T) {
	restoreLaunchSlotChecks(t)
	_, worktree := slotLaunchEnv(t)
	oldSweep := sweepRefcounts
	sweepRefcounts = func() {}
	t.Cleanup(func() { sweepRefcounts = oldSweep })
	seams := stubSlotSeams(t)
	sentinel := filepath.Join(t.TempDir(), "agent-ran")
	seams.cmd = exec.Command("touch", sentinel)
	a := &app{cfg: slotFixtureConfig(), slotChecks: []profiles.SlotCheck{
		slotFixtureCheck(0, "cloud", "opus", slotCloud()),
		slotFixtureCheck(1, "broken", "opus", map[string]string{"opus": "openrouter/gone"}),
	}}

	var err error
	captureFile(t, &os.Stdout, func() {
		err = runLaunchPath(launchCobraCmd(), a, "claude", "profile/cloud", "", "", nil, worktree, "")
	})
	if err != nil {
		t.Fatalf("runLaunchPath() error = %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Error("the agent command did not run for a pinned launch profile")
	}

	err = runLaunchPath(launchCobraCmd(), a, "claude", "profile/broken", "", "", nil, worktree, "")
	if err == nil || err.Error() != "slot opus: openrouter/gone is not in the registry" {
		t.Errorf("blocked pin error = %v, want the row's reason", err)
	}
}

// TestRunLaunchPathRefusesANonStringOverrideTarget follows a claude profile
// whose config_content.modelOverrides has a number target, read from the file
// by the real drivers, to a `-A claude -M profile/<name>` launch (#418): the
// pin returns the block reason, the agent never runs, and no route is written
// and no command built, so the target never reaches the --settings payload.
// Before, the target only warned and the session started with
// {"modelOverrides":{"claude-sonnet-4-6":5}}, which Claude Code cannot use.
func TestRunLaunchPathRefusesANonStringOverrideTarget(t *testing.T) {
	restoreLaunchSlotChecks(t)
	_, worktree := slotLaunchEnv(t)
	oldSweep := sweepRefcounts
	sweepRefcounts = func() {}
	t.Cleanup(func() { sweepRefcounts = oldSweep })
	seams := stubSlotSeams(t)
	sentinel := filepath.Join(t.TempDir(), "agent-ran")
	seams.cmd = exec.Command("touch", sentinel)
	store, err := profiles.ParseSlots([]byte("[[profile]]\nname = \"numbered\"\nagent = \"claude\"\ndefault = \"opus\"\n" +
		"[profile.slots]\nopus = \"openrouter/a\"\n[profile.config_content.modelOverrides]\nclaude-sonnet-4-6 = 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{cfg: slotFixtureConfig(), slotChecks: profiles.ValidateSlots(store, agents.SlotDrivers())}

	err = runLaunchPath(launchCobraCmd(), a, "claude", "profile/numbered", "", "", nil, worktree, "")
	const want = `config_content.modelOverrides["claude-sonnet-4-6"] must be a model id string`
	if err == nil || err.Error() != want {
		t.Errorf("runLaunchPath() error = %v, want %q", err, want)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Error("the agent ran for a profile with a non-string modelOverrides target")
	}
	if got := seams.log(); got != "" {
		t.Errorf("the refused launch drove the engine: steps %q, want none", got)
	}
}
