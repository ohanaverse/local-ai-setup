package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

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
