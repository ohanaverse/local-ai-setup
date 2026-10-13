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
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
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
// "ensure:<id>,<id>", "start:<id>:owed=<bool>", "restart", "probe",
// "settle", "build". Its fields script each seam's result.
type slotSeams struct {
	events     []string
	ensureOwed bool // what ensureSlotRoutes reports as owed when it adds nothing to owedIn
	ensureErr  error
	startStill bool
	startErr   error
	restartErr error
	probeErr   error
	buildErr   error
	cmd        *exec.Cmd // what buildSlotCmd returns; default exec.Command("true")
}

// log is the recorded events joined with ";".
func (s *slotSeams) log() string { return strings.Join(s.events, ";") }

// stubSlotSeams swaps the six seams of slot_launch.go for recorders and
// silences osStderr. The command buildSlotCmd hands back runs in the launch's
// worktree, as the real one does.
func stubSlotSeams(t *testing.T) *slotSeams {
	t.Helper()
	s := &slotSeams{}
	oldEnsure, oldRestart, oldProbe, oldSettle := ensureSlotRoutes, restartProxyNow, probeProxyNow, settleRoutesNow
	oldStart, oldBuild, oldOut := startSlotModel, buildSlotCmd, osStderr
	ensureSlotRoutes = func(_ context.Context, _ io.Writer, _ *config.Config, models []config.Model, owedIn bool) (bool, error) {
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		s.events = append(s.events, "ensure:"+strings.Join(ids, ","))
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
	startSlotModel = func(_ *config.Config, row catalog.Row, _ bool, owed bool) (bool, error) {
		s.events = append(s.events, fmt.Sprintf("start:%s:owed=%t", row.Model.ID, owed))
		return s.startStill, s.startErr
	}
	buildSlotCmd = func(_ profiles.SlotLaunch, worktreePath string, _ bool, _ *config.Config, _ []string) (*exec.Cmd, error) {
		s.events = append(s.events, "build")
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
