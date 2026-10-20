package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Blocked-profile gaps of the non-TUI path (#412 A4): the remembered failed
// proxy restart through the real lifecycle.ProbeProxy, the local-model block
// reasons on the pin path, and a step-1 re-resolve that turns a launch row
// into a blocked one.

// fakeProxy is a stand-in LiteLLM proxy that answers 200 on every path, the
// liveness endpoint included. A test points [litellm] url at it so that a
// probe or readiness wait that does run reaches this server, never whatever
// answers at the developer's proxy address. internal/tui's blocked-profile
// test keeps a copy of this helper and of failLastProxyRestart below it; the
// launch path each package tests, not the helpers, differ.
func fakeProxy(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

// failLastProxyRestart makes internal/lifecycle remember a failed proxy
// restart the way a real one is remembered: lifecycle.RestartProxy runs the
// restart command WT_LITELLM_RESTART_CMD names ("false", which exits 1) and
// records the failure. It returns that restart's error. cfg's [litellm] url
// must be a fakeProxy registered before this call: on cleanup, which runs
// before the server closes, a restart that succeeds (the command "true", then
// the readiness wait against that server) clears the remembered failure, so
// it does not leak into later tests of this process.
func failLastProxyRestart(t *testing.T, cfg *config.Config) error {
	t.Helper()
	t.Setenv("WT_LITELLM_RESTART_CMD", "false")
	t.Cleanup(func() {
		// Runs before t.Setenv's own restore, so the variable is set here.
		_ = os.Setenv("WT_LITELLM_RESTART_CMD", "true")
		if err := lifecycle.RestartProxy(context.Background(), cfg); err != nil {
			t.Errorf("clearing the remembered restart failure: %v", err)
		}
		if err := lifecycle.ProbeProxy(context.Background(), cfg); err != nil {
			t.Errorf("the remembered restart failure was not cleared: %v", err)
		}
	})
	return lifecycle.RestartProxy(context.Background(), cfg)
}

// assertSlotLaunchAborted fails unless the profile launch ran no agent (the
// sentinel the agent command would create does not exist) and recorded
// nothing under stateDir.
func assertSlotLaunchAborted(t *testing.T, stateDir, sentinel string) {
	t.Helper()
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Error("the agent command ran after the launch was refused")
	}
	for _, name := range slotStateFiles {
		if _, statErr := os.Stat(filepath.Join(stateDir, name)); !os.IsNotExist(statErr) {
			t.Errorf("%s was written although the launch was refused", name)
		}
	}
}

// TestSlotProfileRefusedAfterARememberedFailedRestart verifies, with the real
// lifecycle.ProbeProxy in place of the probe seam, that a proxy restart which
// failed earlier in the same wt process refuses a later profile launch whose
// own routes were all in place. The proxy answers its liveness probe here, as
// the old proxy does after a failed restart; only the remembered failure can
// say that the routes written since are not live. Without it Claude Code
// would start against a proxy that answers "Invalid model name" for its
// slots. The launch must also run no agent and record nothing.
func TestSlotProfileRefusedAfterARememberedFailedRestart(t *testing.T) {
	stateDir, worktree := slotLaunchEnv(t)
	cfg := slotFixtureConfig()
	cfg.SetLitellmForTest(config.LitellmState{URL: fakeProxy(t), APIKey: "sk-test-1234"})
	if err := failLastProxyRestart(t, cfg); err == nil {
		t.Fatal("fixture: the restart did not fail, so the test proves nothing")
	}
	seams := stubSlotSeams(t)
	probeProxyNow = lifecycle.ProbeProxy // stubSlotSeams' cleanup restores the stub
	sentinel := filepath.Join(t.TempDir(), "agent-ran")
	seams.cmd = exec.Command("touch", sentinel)
	stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", slotCloud()))

	var err error
	stdout := captureFile(t, &os.Stdout, func() {
		err = pinSlotProfile(worktree, cfg, "profile/p")
	})

	const prefix = "the last LiteLLM proxy restart failed, so routes written since may not be live: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), "restart it manually: false") {
		t.Fatalf("err = %v, want it to begin %q and name the failed restart command", err, prefix)
	}
	if got, want := seams.log(), "ensure:openrouter/a,openrouter/b;wait"; got != want {
		t.Errorf("steps = %q, want %q (no restart owed, the wait, then the refusing probe)", got, want)
	}
	if strings.Contains(stdout, "wt: claude ·") {
		t.Errorf("stdout = %q, want no summary line: no session ran", stdout)
	}
	assertSlotLaunchAborted(t, stateDir, sentinel)
}

// slotMlxConfig is slotFixtureConfig plus a local mlx_lm_server provider and
// one target+draft pairing, "mlx_lm_server/p", which wt cannot start.
func slotMlxConfig() *config.Config {
	cfg := slotFixtureConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal,
		Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8001"}})
	cfg.Models = append(cfg.Models, config.Model{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p", Family: "p"})
	return cfg
}

// TestPinnedSlotProfileLocalModelBlockReasons verifies that the catalog's
// block reasons for a profile's one local model reach the user on the -M pin
// path word for word, with nothing routed, started or run and nothing
// recorded: a local model the probe has no entry for, a model the probe
// confirmed is not on disk, an mlx_lm_server pairing that is not running, and
// a pairing whose family probe failed. A generic message there would leave
// the user with no way to tell a missing download from a stopped pairing, and
// a launch that went on would start Claude Code with an unserved slot.
func TestPinnedSlotProfileLocalModelBlockReasons(t *testing.T) {
	notRunning := "local model \"mlx_lm_server/p\" is not running, and wt cannot start an mlx_lm_server pairing — " +
		"start it with `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`"
	cases := []struct {
		name  string
		local string
		snap  localmodels.Snapshot
		want  string
	}{
		{"absent from the inventory", "omlx/q",
			localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK}},
			"slot haiku: omlx/q is not on disk or running"},
		{"confirmed not on disk", "omlx/q",
			localmodels.Snapshot{
				Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
				Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/q", ModelName: "q", Registered: true, ArtifactKnown: true}},
			},
			"omlx/q is not on disk — pull or download it first"},
		{"an mlx_lm_server pairing that is not running", "mlx_lm_server/p",
			localmodels.Snapshot{
				Providers: map[string]localmodels.Status{"mlx_lm_server": localmodels.StatusOK},
				Entries:   []localmodels.Entry{{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", ModelName: "p", Registered: true}},
			},
			notRunning},
		{"the pairing's family probe failed", "mlx_lm_server/p",
			localmodels.Snapshot{
				Providers:     map[string]localmodels.Status{"mlx_lm_server": localmodels.StatusPartial},
				ProbeFailures: map[string]error{"mlx_lm_server": errors.New("Get \"http://localhost:8001/v1/models\": connection refused")},
				Entries:       []localmodels.Entry{{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", ModelName: "p", Registered: true}},
			},
			notRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, worktree := slotLaunchEnv(t)
			stubProbeInventory(t, tc.snap)
			started := stubStartDriver(t, nil)
			seams := stubSlotSeams(t)
			sentinel := filepath.Join(t.TempDir(), "agent-ran")
			seams.cmd = exec.Command("touch", sentinel)
			stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", map[string]string{"opus": "openrouter/a", "haiku": tc.local}))

			var err error
			captureFile(t, &os.Stdout, func() {
				err = pinSlotProfile(worktree, slotMlxConfig(), "profile/p")
			})

			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if got := seams.log(); got != "" {
				t.Errorf("steps = %q, want none: a blocked profile routes, starts and probes nothing", got)
			}
			if started.called {
				t.Error("a model was started for a blocked profile")
			}
			assertSlotLaunchAborted(t, stateDir, sentinel)
		})
	}
}

// TestSlotProfileStepOneReResolvesAgainstAFreshProbe verifies step 1 of the
// non-TUI launch: the row resolveModel found (a launch row, its local model
// running) is judged again against a second, fresh probe, and when that probe
// no longer has the local model the launch is refused with the new reason,
// before any route is written, anything is started or the agent runs, and
// with nothing recorded. Judged on the first probe alone, a model stopped in
// the moment between the two would leave Claude Code with a dead slot.
func TestSlotProfileStepOneReResolvesAgainstAFreshProbe(t *testing.T) {
	stateDir, worktree := slotLaunchEnv(t)
	probes := 0
	old := probeInventory
	probeInventory = func(*config.Config) localmodels.Snapshot {
		probes++
		if probes == 1 {
			return runningQ()
		}
		return localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK}}
	}
	t.Cleanup(func() { probeInventory = old })
	started := stubStartDriver(t, nil)
	seams := stubSlotSeams(t)
	sentinel := filepath.Join(t.TempDir(), "agent-ran")
	seams.cmd = exec.Command("touch", sentinel)
	stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", slotMixed()))

	var err error
	captureFile(t, &os.Stdout, func() {
		err = pinSlotProfile(worktree, slotFixtureConfig(), "profile/p")
	})

	if want := "slot haiku: omlx/q is not on disk or running"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if probes != 2 {
		t.Errorf("inventory probes = %d, want 2: one to find the row, one fresh for step 1", probes)
	}
	if got := seams.log(); got != "" {
		t.Errorf("steps = %q, want none", got)
	}
	if started.called {
		t.Error("a model was started after the fresh probe blocked the profile")
	}
	assertSlotLaunchAborted(t, stateDir, sentinel)
}
