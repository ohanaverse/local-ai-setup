package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

func routesCfg() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
			{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8003/v1"}},
		},
		Models: []config.Model{
			{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal},
			{ID: "ollama/b:1", ProviderID: "ollama", ModelName: "b:1", Location: config.LocationLocal},
			{ID: "mtplx/Y--Q35", ProviderID: "mtplx", ModelName: "Y/Q35", Location: config.LocationLocal},
			{ID: "mtplx/Y--Q27", ProviderID: "mtplx", ModelName: "Y/Q27", Location: config.LocationLocal},
		},
	}
}

type routeCall struct{ add, remove []string }

// stubRoutes replaces the litellm seams for one test and returns the recorded
// calls and the warning buffer.
func stubRoutes(t *testing.T, res litellm.Result, err error) (*[]routeCall, *bytes.Buffer) {
	t.Helper()
	var calls []routeCall
	var warn bytes.Buffer
	oa, ow, op, ww := applyRoutes, waitProxy, probeProxy, routesWarn
	applyRoutes = func(_ *config.Config, add, remove []string, o litellm.Options) (litellm.Result, error) {
		if !o.SkipReadyGate {
			t.Error("lifecycle hook must skip the ready gate: the model is verifiably running")
		}
		if o.Restart == nil {
			t.Error("lifecycle hook must pass a ctx-aware restart, not leave the package default")
		}
		calls = append(calls, routeCall{add, remove})
		return res, err
	}
	waitProxy = func(context.Context, string, time.Duration) error { return nil }
	// Default to "the proxy was already up" so the readiness-wait tests below
	// exercise the wait; the proxy-down case overrides it.
	probeProxy = func(context.Context, string, time.Duration) bool { return true }
	routesWarn = &warn
	t.Cleanup(func() { applyRoutes, waitProxy, probeProxy, routesWarn = oa, ow, op, ww })
	return &calls, &warn
}

// TestRouteAfterStartSingleModelReplacesSiblings pins the bug fix: starting a
// single-model provider's model (mtplx) adds ITS route and removes routes of
// the provider's other models, since starting it stopped whatever ran before.
// Without the removal a replaced model keeps a dead route.
func TestRouteAfterStartSingleModelReplacesSiblings(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35"})
	want := routeCall{add: []string{"mtplx/Y--Q35"}, remove: []string{"mtplx/Y--Q27"}}
	if len(*calls) != 1 || !slices.Equal((*calls)[0].add, want.add) || !slices.Equal((*calls)[0].remove, want.remove) {
		t.Fatalf("calls = %+v, want %+v", *calls, want)
	}
}

// TestRouteAfterStartMultiTenantOnlyAdds pins that ollama (many models at
// once) only adds the started model's route and never removes siblings that
// may still be loaded.
func TestRouteAfterStartMultiTenantOnlyAdds(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"})
	if len(*calls) != 1 || !slices.Equal((*calls)[0].add, []string{"ollama/a:1"}) || len((*calls)[0].remove) != 0 {
		t.Fatalf("calls = %+v", *calls)
	}
}

// TestRouteAfterStop pins stop symmetry: stopping an ollama model removes only
// its route; stopping a single-model provider's model removes routes for every
// model of that provider (the whole provider went down).
func TestRouteAfterStop(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStop(context.Background(), routesCfg(), "ollama", "a:1")
	routeAfterStop(context.Background(), routesCfg(), "mtplx", "Y/Q35")
	if len(*calls) != 2 {
		t.Fatalf("calls = %+v", *calls)
	}
	if !slices.Equal((*calls)[0].remove, []string{"ollama/a:1"}) {
		t.Errorf("ollama stop removed %v", (*calls)[0].remove)
	}
	if got := slices.Clone((*calls)[1].remove); !slices.Equal(got, []string{"mtplx/Y--Q35", "mtplx/Y--Q27"}) {
		t.Errorf("mtplx stop removed %v", got)
	}
}

// TestRouteNeverFailsTheCaller pins the failure contract: a LiteLLM error is a
// stderr warning, a missing config.yaml is silent (LiteLLM not set up), and
// unregistered models warn once. Failing a successful start over a missing
// route would strand a running model.
func TestRouteNeverFailsTheCaller(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, errors.New("boom"))
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"})
	if !bytes.Contains(warn.Bytes(), []byte("boom")) {
		t.Fatalf("no warning for a failed apply: %q", warn.String())
	}

	_, warn = stubRoutes(t, litellm.Result{}, litellm.ErrMissing)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"})
	if warn.Len() != 0 {
		t.Fatalf("missing config.yaml must be silent, got %q", warn.String())
	}

	calls, warn := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "unregistered:9"})
	if len(*calls) != 0 || !bytes.Contains(warn.Bytes(), []byte("not in the registry")) {
		t.Fatalf("unregistered: calls=%v warn=%q", *calls, warn.String())
	}
}

// TestRouteWaitsForProxyOnlyWhenChanged pins the readiness wait: it runs after
// a route change when a LiteLLM URL is configured, and is skipped when nothing
// changed (no restart happened) or no URL is set. The no-URL case also pins
// that the liveness pre-probe is not even attempted without a URL — there is
// nothing to dial.
func TestRouteWaitsForProxyOnlyWhenChanged(t *testing.T) {
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000"})
	stubRoutes(t, litellm.Result{Changed: true}, nil)
	waited := 0
	waitProxy = func(context.Context, string, time.Duration) error { waited++; return nil }
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"})
	if waited != 1 {
		t.Fatalf("waited = %d, want 1", waited)
	}
	stubRoutes(t, litellm.Result{Changed: false}, nil)
	waitProxy = func(context.Context, string, time.Duration) error { waited++; return nil }
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"})
	if waited != 1 {
		t.Fatal("waited despite no change")
	}

	noURL := routesCfg() // no [litellm] url at all
	stubRoutes(t, litellm.Result{Changed: true}, nil)
	probed := 0
	probeProxy = func(context.Context, string, time.Duration) bool { probed++; return true }
	waitProxy = func(context.Context, string, time.Duration) error { waited++; return nil }
	routeAfterStart(context.Background(), noURL, Target{ProviderID: "ollama", ModelName: "a:1"})
	if waited != 1 || probed != 0 {
		t.Fatalf("no LiteLLM URL: waited=%d (want still 1) probed=%d (want 0)", waited, probed)
	}
}

// TestRouteSkipsWaitWhenProxyWasNotRunning pins the pre-probe gate: when a
// LiteLLM URL is configured but the proxy is not answering, wt must not spend
// the 30s readiness budget waiting for a proxy that was never up — the restart
// is best-effort and there is nothing to come back. Before the probe, every
// start and stop on a machine with a configured-but-stopped proxy stalled for
// the full timeout, even with routing switched off.
func TestRouteSkipsWaitWhenProxyWasNotRunning(t *testing.T) {
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://127.0.0.1:1"})
	_, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	probed := 0
	probeProxy = func(context.Context, string, time.Duration) bool { probed++; return false }
	waited := 0
	waitProxy = func(context.Context, string, time.Duration) error {
		waited++
		return errors.New("LiteLLM proxy not ready")
	}
	began := time.Now()
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"})
	routeAfterStop(context.Background(), cfg, "ollama", "a:1")
	if probed != 2 || waited != 0 {
		t.Fatalf("probed=%d (want 2) waited=%d (want 0)", probed, waited)
	}
	if warn.Len() != 0 {
		t.Errorf("a proxy that was already down must not warn, got %q", warn.String())
	}
	if el := time.Since(began); el > 2*time.Second {
		t.Errorf("hook took %v with a down proxy, want prompt", el)
	}
}

type ctxKey struct{}

// TestRouteHonorsCallerContext pins that the CALLER's ctx (not a fresh
// Background) reaches the proxy wait, for both start and stop: the stub
// captures the ctx it receives and the test checks it carries the caller's
// sentinel value and fires Done when the caller cancels. Also pins that a
// cancelled ctx (Ctrl+C) produces no "proxy not ready" warning. Ignoring ctx
// would hang the process up to the readiness timeout.
func TestRouteHonorsCallerContext(t *testing.T) {
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000"})
	_, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	var captured []context.Context
	waitProxy = func(ctx context.Context, _ string, _ time.Duration) error {
		captured = append(captured, ctx)
		return ctx.Err()
	}
	base, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "sentinel"))
	routeAfterStart(base, cfg, Target{ProviderID: "ollama", ModelName: "a:1"})
	routeAfterStop(base, cfg, "ollama", "a:1")
	if len(captured) != 2 {
		t.Fatalf("waitProxy calls = %d, want 2", len(captured))
	}
	for i, c := range captured {
		if c.Value(ctxKey{}) != "sentinel" {
			t.Errorf("call %d: waitProxy did not receive the caller's ctx", i)
		}
		select {
		case <-c.Done():
			t.Errorf("call %d: ctx done before the caller cancelled", i)
		default:
		}
	}
	cancel()
	for i, c := range captured {
		select {
		case <-c.Done():
		default:
			t.Errorf("call %d: caller cancel did not reach waitProxy's ctx", i)
		}
	}
	routeAfterStart(base, cfg, Target{ProviderID: "ollama", ModelName: "a:1"})
	routeAfterStop(base, cfg, "ollama", "a:1")
	if warn.Len() != 0 {
		t.Fatalf("cancelled ctx must not warn, got %q", warn.String())
	}
}

// TestRouteWarnsWhenProxyWaitFailsUncancelled pins that a genuine readiness
// failure (ctx still live) is still surfaced as a warning.
func TestRouteWarnsWhenProxyWaitFailsUncancelled(t *testing.T) {
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000"})
	_, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	waitProxy = func(context.Context, string, time.Duration) error { return errors.New("proxy down") }
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"})
	if !bytes.Contains(warn.Bytes(), []byte("proxy down")) {
		t.Fatalf("expected warning, got %q", warn.String())
	}
}
