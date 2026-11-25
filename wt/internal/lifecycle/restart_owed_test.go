package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// proxyURL is the [litellm] url the tests in this file configure. Nothing
// dials it: every test installs stubRoutes or realRoutes, which replace the
// readiness wait.
const proxyURL = "http://localhost:4000"

// fakeRestarter replaces the proxy restart for one test and counts its runs.
// Install it after stubRoutes or realRoutes: its cleanup then runs first,
// waits for any restart still in flight, and only then puts the seam back.
func fakeRestarter(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := restartProxy
	restartProxy = func(context.Context) []string {
		n.Add(1)
		return nil
	}
	t.Cleanup(func() {
		WaitPendingRoutes()
		restartProxy = old
	})
	return &n
}

// sortedRoutedIDs is routedIDs in a stable order: the order of rows in
// config.yaml is the writer's business, not these tests'.
func sortedRoutedIDs(t *testing.T, path string) []string {
	t.Helper()
	ids := routedIDs(t, path)
	slices.Sort(ids)
	return ids
}

// writeRouteDeferred writes the route of the registry model id the way an
// earlier deferred write of the same launch would (restartDeferred: written,
// proxy not restarted) and reports whether config.yaml changed, which is what
// Options.RestartOwed carries into the start that follows.
func writeRouteDeferred(t *testing.T, cfg *config.Config, id string) bool {
	t.Helper()
	i := config.IndexModelByID(cfg.Models, id)
	if i < 0 {
		t.Fatalf("fixture has no model %q", id)
	}
	return applyAndReport(context.Background(), cfg, litellm.Change{Add: []config.Model{cfg.Models[i]}}, restartDeferred)
}

// withCloudModel adds an openrouter model x to cfg and sets the [litellm]
// url, the registry a start that owes a restart needs.
func withCloudModel(cfg *config.Config) {
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key", SecretRef: "sk-test", BaseURL: "https://openrouter.ai/api/v1"}})
	cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud})
	cfg.SetLitellmForTest(config.LitellmState{URL: proxyURL, APIKey: "sk-test-1234"})
}

// TestRestartStillOwed pins which Start errors leave the caller's owed
// restart unsettled: only the two refusals that touch nothing. A caller
// that read a failed load as "still owed" would restart a second time; one
// that read a refusal as "settled" would never publish the route it wrote.
func TestRestartStillOwed(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"occupied", &OccupiedError{}, true},
		{"occupied, wrapped", fmt.Errorf("start: %w", &OccupiedError{}), true},
		{"occupancy unknown", &OccupancyUnknownError{ProviderID: "mtplx"}, true},
		{"occupancy unknown, wrapped", fmt.Errorf("start: %w", &OccupancyUnknownError{}), true},
		{"a failed load", errors.New("out of memory"), false},
		{"daemon down", &DaemonDownError{Provider: "ollama"}, false},
		{"cancelled", context.Canceled, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RestartStillOwed(tc.err); got != tc.want {
				t.Errorf("RestartStillOwed(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIdleLocalModelPlusMissingCloudRouteRestartsOnce pins the mixed
// profile: a cloud route is written first (restart owed), then the idle
// local model is started with Options.RestartOwed. The start's own restart
// publishes the cloud route too, so the launch costs exactly one restart.
// The second case is the one RestartOwed exists for: the local model's

// without the option the proxy would never learn the cloud route — the
// session's cloud slot would answer "Invalid model name".
func TestIdleLocalModelPlusMissingCloudRouteRestartsOnce(t *testing.T) {
	for _, localRouted := range []bool{false, true} {
		t.Run(fmt.Sprintf("local route already written=%v", localRouted), func(t *testing.T) {
			path, _, warn := realRoutes(t, "model_list: []\n")
			restarts := fakeRestarter(t)
			var calls []string
			swapBackend(t, "ollama", &fakeBackend{calls: &calls})
			// a:1 is pulled and not loaded: an idle local model.
			cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), openaiSrv(t, nil))
			withCloudModel(cfg)
			ctx := context.Background()
			local := Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"}

			if localRouted {
				writeRouteDeferred(t, cfg, "ollama/a:1")
				warn.Reset()
			}

			owed := writeRouteDeferred(t, cfg, "openrouter/x")
			if !owed {
				t.Fatal("the cloud route write changed nothing, want a restart owed")
			}
			if err := startFull(ctx, fullFamilies, cfg, local, Options{RestartOwed: owed}); err != nil {
				t.Fatalf("Start: %v", err)
			}
			WaitPendingRoutes()

			if !slices.Equal(calls, []string{"start:a:1"}) {
				t.Fatalf("backend calls = %v, want one start", calls)
			}
			if got, want := sortedRoutedIDs(t, path), []string{"ollama/a:1", "openrouter/x"}; !slices.Equal(got, want) {
				t.Fatalf("routed = %v, want %v (output %q)", got, want, warn.String())
			}
			if n := restarts.Load(); n != 1 {
				t.Fatalf("restarts = %d, want exactly 1", n)
			}
		})
	}
}

// TestStartRefusedWhileRestartOwed pins Review Focus 4. A profile wrote a
// cloud route (restart owed) and its local model's server is occupied, so
// the start is refused with *OccupiedError. The refusal must not restart
// the proxy: the user is about to be asked whether to replace, and the
// confirmed retry restarts once for everything. Restarting at the refusal
// too would bounce the proxy twice in one launch. A user who declines must
// still get the one restart (SettleRoutes), or the route written for them
// stays invisible to the proxy and the next launch of that cloud model,

func TestStartRefusedWhileRestartOwed(t *testing.T) {
	// refused sets the scene up to the refusal and returns what the two
	// endings need.
	refused := func(t *testing.T) (path string, restarts *atomic.Int32, cfg *config.Config, target Target, calls *[]string) {
		t.Helper()
		path, _, _ = realRoutes(t, staleSiblingYAML) // routes mtplx/Y--Q27, the occupant
		restarts = fakeRestarter(t)
		calls = new([]string)
		swapBackend(t, "mtplx", &fakeBackend{single: true, calls: calls})
		cfg = wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, []string{"Y/Q27"}))
		withCloudModel(cfg)
		target = Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"}

		if !writeRouteDeferred(t, cfg, "openrouter/x") {
			t.Fatal("the cloud route write changed nothing, want a restart owed")
		}
		err := Start(context.Background(), cfg, target, Options{RestartOwed: true})
		WaitPendingRoutes()
		var occupied *OccupiedError
		if !errors.As(err, &occupied) {
			t.Fatalf("Start = %v, want *OccupiedError", err)
		}
		if !RestartStillOwed(err) {
			t.Fatal("RestartStillOwed = false after a refusal that touched nothing")
		}
		if len(*calls) != 0 {
			t.Fatalf("backend calls = %v, want none at the refusal", *calls)
		}
		if n := restarts.Load(); n != 0 {
			t.Fatalf("restarts = %d at the refusal, want 0", n)
		}
		return path, restarts, cfg, target, calls
	}

	t.Run("confirmed retry restarts once in total", func(t *testing.T) {
		path, restarts, cfg, target, calls := refused(t)
		if err := Start(context.Background(), cfg, target, Options{AllowReplace: true, RestartOwed: true, Others: OthersAnswer{Decided: true}}); err != nil {
			t.Fatalf("retry: %v", err)
		}
		WaitPendingRoutes()
		if !slices.Equal(*calls, []string{"stop", "start:Y/Q35"}) {
			t.Fatalf("backend calls = %v, want the occupant stopped, then the start", *calls)
		}
		if got, want := sortedRoutedIDs(t, path), []string{"mtplx/Y--Q35", "openrouter/x"}; !slices.Equal(got, want) {
			t.Fatalf("routed = %v, want %v", got, want)
		}
		if n := restarts.Load(); n != 1 {
			t.Fatalf("restarts = %d, want exactly 1 for the whole launch", n)
		}
	})

	t.Run("giving up settles with one restart", func(t *testing.T) {
		path, restarts, cfg, _, calls := refused(t)
		SettleRoutes(context.Background(), cfg)
		WaitPendingRoutes()
		if len(*calls) != 0 {
			t.Fatalf("backend calls = %v, want none: the user declined", *calls)
		}
		if got, want := sortedRoutedIDs(t, path), []string{"mtplx/Y--Q27", "openrouter/x"}; !slices.Equal(got, want) {
			t.Fatalf("routed = %v, want %v: the written cloud route beside the occupant's", got, want)
		}
		if n := restarts.Load(); n != 1 {
			t.Fatalf("restarts = %d, want exactly 1, so the written route is live", n)
		}
	})
}

// TestStartFailureSettlesAnOwedRestart pins the other error branch: a start
// that got past the refusals and then failed has, per the caller's contract,
// settled the owed restart. The cloud route the profile wrote is published

func TestStartFailureSettlesAnOwedRestart(t *testing.T) {
	path, _, _ := realRoutes(t, "model_list: []\n")
	restarts := fakeRestarter(t)
	var calls []string
	swapBackend(t, "ollama", &fakeBackend{calls: &calls, startErr: errors.New("out of memory")})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), openaiSrv(t, nil))

	for _, owed := range []bool{false, true} {
		err := startFull(context.Background(), fullFamilies, cfg, Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"}, Options{RestartOwed: owed})
		WaitPendingRoutes()
		if err == nil || RestartStillOwed(err) {
			t.Fatalf("RestartOwed=%v: Start = %v, want a failure that is not a refusal", owed, err)
		}
		want := int32(0)
		if owed {
			want = 1
		}
		if n := restarts.Load(); n != want {
			t.Fatalf("RestartOwed=%v: restarts = %d, want %d", owed, n, want)
		}
	}
	if got := routedIDs(t, path); len(got) != 0 {
		t.Fatalf("routed = %v, want none: the model never started", got)
	}
}
