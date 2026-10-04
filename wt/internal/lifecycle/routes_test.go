package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
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

// routeCall is one recorded route write: the added ids, the explicit
// removals and the families whose routes all go.
type routeCall struct{ add, remove, families []string }

// stubRoutes replaces the litellm seams for one test and returns the recorded
// calls and the warning buffer.
//
// applyAndReport restarts the proxy asynchronously, so a test that triggers a
// restart must call WaitPendingRoutes before asserting on anything the async
// phase sets (a counter, the warning buffer, a captured ctx). The cleanup
// below is only a backstop against a leaked goroutine outliving the seam
// restore and corrupting the NEXT test; it is not a substitute for that call.
func stubRoutes(t *testing.T, res litellm.Result, err error) (*[]routeCall, *bytes.Buffer) {
	t.Helper()
	var calls []routeCall
	var warn bytes.Buffer
	oa, ow, op, ww, or := applyRoutes, waitProxy, probeProxy, routesWarn, restartProxy
	applyRoutes = func(_ *config.Config, ch litellm.Change, o litellm.Options) (litellm.Result, error) {
		if !o.NoRestart {
			t.Error("applyAndReport must always defer ApplyChange's restart and run it asynchronously itself")
		}
		if o.ForceRestart {
			t.Error("ForceRestart would make ApplyChange restart despite NoRestart, putting the bounce back on the caller's path")
		}
		var add []string
		for _, m := range ch.Add {
			add = append(add, m.ID)
		}
		calls = append(calls, routeCall{add, ch.Remove, ch.RemoveFamilies})
		return res, err
	}
	restartProxy = func(context.Context) []string { return nil }
	waitProxy = func(context.Context, string, time.Duration) error { return nil }
	// Default to "the proxy was already up" so the readiness-wait tests below
	// exercise the wait; the proxy-down case overrides it.
	probeProxy = func(context.Context, string, time.Duration) bool { return true }
	routesWarn = &warn
	t.Cleanup(func() { applyRoutes, waitProxy, probeProxy, routesWarn, restartProxy = oa, ow, op, ww, or })
	// Registered last, so it runs FIRST: settle any in-flight restart before
	// the seams above are put back underneath it.
	t.Cleanup(WaitPendingRoutes)
	return &calls, &warn
}

// TestRouteAfterStartSingleModelReplacesSiblings pins the bug fix: starting a
// single-model provider's model (mtplx) adds ITS route and removes every
// other route of the provider's family, since starting it stopped whatever
// ran before. Since #179 Phase B the removal is the whole family (marked rows
// of discovered siblings included), not a registry-derived id list that
// missed them. Without it a replaced model keeps a dead route.
func TestRouteAfterStartSingleModelReplacesSiblings(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35"}, false)
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want one", *calls)
	}
	c := (*calls)[0]
	if !slices.Equal(c.add, []string{"mtplx/Y--Q35"}) || len(c.remove) != 0 || !slices.Equal(c.families, []string{"mtplx"}) {
		t.Fatalf("call = %+v, want add [mtplx/Y--Q35] and the mtplx family removed", c)
	}
}

// TestRouteAfterStartMultiTenantOnlyAdds pins that ollama (many models at
// once) only adds the started model's route and never removes siblings that
// may still be loaded.
func TestRouteAfterStartMultiTenantOnlyAdds(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	if len(*calls) != 1 || !slices.Equal((*calls)[0].add, []string{"ollama/a:1"}) || len((*calls)[0].remove) != 0 || len((*calls)[0].families) != 0 {
		t.Fatalf("calls = %+v", *calls)
	}
}

// TestRouteAfterStartRoutesDiscoveredModel pins #179 Phase B: starting a model
// with no registry overlay routes it under its discovered id — the id the
// picker, -M and usage use — instead of warning "not in the registry" and
// leaving every LiteLLM-forced agent (codex) unable to reach it. A two-slash
// mtplx artifact keeps both slashes in its id.
func TestRouteAfterStartRoutesDiscoveredModel(t *testing.T) {
	calls, warn := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "llama3.2:3b"}, false)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "mtplx", ModelName: "mlx-community/Qwen3.8-27B-4bit"}, false)
	if len(*calls) != 2 || warn.Len() != 0 {
		t.Fatalf("calls = %+v warn = %q, want two writes and no warning", *calls, warn.String())
	}
	if got := (*calls)[0]; !slices.Equal(got.add, []string{"ollama/llama3.2:3b"}) || len(got.families) != 0 {
		t.Errorf("ollama discovered start = %+v, want add [ollama/llama3.2:3b] only", got)
	}
	if got := (*calls)[1]; !slices.Equal(got.add, []string{"mtplx/mlx-community/Qwen3.8-27B-4bit"}) || !slices.Equal(got.families, []string{"mtplx"}) {
		t.Errorf("mtplx discovered start = %+v, want the two-slash id added and the mtplx family removed", got)
	}
}

// TestRouteAfterStartOmlx6bitClearsOmlxFamily pins the omlx/omlx-6bit
// spelling split: a registry omlx-6bit model and a discovered artifact (whose
// id is always spelled "omlx/<artifact>") share one physical server, so
// starting the 6-bit model must remove the whole "omlx" family — the family
// name, never the provider id "omlx-6bit", which no discovered row carries.
func TestRouteAfterStartOmlx6bitClearsOmlxFamily(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal}},
	}
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "omlx-6bit", ModelName: "Six"}, false)
	if len(*calls) != 1 || !slices.Equal((*calls)[0].add, []string{"omlx-6bit/Six"}) || !slices.Equal((*calls)[0].families, []string{"omlx"}) {
		t.Fatalf("calls = %+v, want add [omlx-6bit/Six] and the omlx family removed", *calls)
	}
}

// TestRouteHooksRequestOnlyRealFamilies pins what the hooks may put in
// Change.RemoveFamilies: only a real, non-empty localmodels.Family value.
// ApplyChange matches families by name, so a raw provider id ("omlx-6bit")
// would be a silent no-op that leaves the replaced model's dead route behind,
// and "" is the family of every provider wt has no probe for — a hook that
// asked to clear it would be asking to clear routes it cannot name. So an
// omlx-6bit start or stop clears "omlx", and a family-less provider
// (retired llamacpp) requests no family removal at all, registered or not.
func TestRouteHooksRequestOnlyRealFamilies(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
			{ID: "llamacpp", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8080"}},
		},
		Models: []config.Model{
			{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal},
			{ID: "llamacpp/G", ProviderID: "llamacpp", ModelName: "G", Location: config.LocationLocal},
		},
	}
	for _, tc := range []struct {
		provider, model string
		want            []string // RemoveFamilies of both the start and the stop
	}{
		{"omlx-6bit", "Six", []string{"omlx"}},
		{"omlx-6bit", "unregistered", []string{"omlx"}},
		{"omlx", "unregistered", []string{"omlx"}},
		{"mtplx", "org/unregistered", []string{"mtplx"}},
		{"ollama", "unregistered:1", nil},
		{"llamacpp", "G", nil},
		{"llamacpp", "unregistered", nil},
		{"no-such-provider", "x", nil},
	} {
		calls, _ := stubRoutes(t, litellm.Result{}, nil)
		routeAfterStart(context.Background(), cfg, Target{ProviderID: tc.provider, ModelName: tc.model}, false)
		routeAfterStop(context.Background(), cfg, tc.provider, tc.model)
		routeAfterStop(context.Background(), cfg, tc.provider, "") // a provider-wide stop
		if len(*calls) == 0 {
			t.Errorf("%s/%s: no route write recorded for the start", tc.provider, tc.model)
			continue
		}
		for i, c := range *calls {
			if !slices.Equal(c.families, tc.want) {
				t.Errorf("%s/%s call %d: families = %q, want %q", tc.provider, tc.model, i, c.families, tc.want)
			}
			for _, f := range c.families {
				if f == "" || f != localmodels.Family(tc.provider) {
					t.Errorf("%s/%s call %d: family %q is not the provider's non-empty localmodels.Family (%q)", tc.provider, tc.model, i, f, localmodels.Family(tc.provider))
				}
			}
		}
	}
}

// TestRouteAfterStop pins what a stop does to routes: stopping a single-model
// provider's model removes every route of that provider's family (the whole
// provider went down), while stopping an ollama model writes nothing — ollama
// only unloads it, and a pulled model is still served on request, so removing
// its route would break the next request through LiteLLM (#179).
func TestRouteAfterStop(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStop(context.Background(), routesCfg(), "ollama", "a:1")
	if len(*calls) != 0 {
		t.Fatalf("ollama stop wrote routes: %+v", *calls)
	}
	routeAfterStop(context.Background(), routesCfg(), "mtplx", "Y/Q35")
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v", *calls)
	}
	if c := (*calls)[0]; len(c.add) != 0 || len(c.remove) != 0 || !slices.Equal(c.families, []string{"mtplx"}) {
		t.Errorf("mtplx stop = %+v, want only the mtplx family removed", c)
	}
}

// TestRouteNeverFailsTheCaller pins the failure contract: a LiteLLM error is a
// stderr warning and a missing config.yaml is silent (LiteLLM not set up).
// Failing a successful start over a missing route would strand a running
// model.
func TestRouteNeverFailsTheCaller(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, errors.New("boom"))
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	if !bytes.Contains(warn.Bytes(), []byte("boom")) {
		t.Fatalf("no warning for a failed apply: %q", warn.String())
	}

	_, warn = stubRoutes(t, litellm.Result{}, litellm.ErrMissing)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	if warn.Len() != 0 {
		t.Fatalf("missing config.yaml must be silent, got %q", warn.String())
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
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	WaitPendingRoutes() // the wait now runs asynchronously; observe it finish
	if waited != 1 {
		t.Fatalf("waited = %d, want 1", waited)
	}
	stubRoutes(t, litellm.Result{Changed: false}, nil)
	waitProxy = func(context.Context, string, time.Duration) error { waited++; return nil }
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	WaitPendingRoutes()
	if waited != 1 {
		t.Fatal("waited despite no change")
	}

	noURL := routesCfg() // no [litellm] url at all
	stubRoutes(t, litellm.Result{Changed: true}, nil)
	probed := 0
	probeProxy = func(context.Context, string, time.Duration) bool { probed++; return true }
	waitProxy = func(context.Context, string, time.Duration) error { waited++; return nil }
	routeAfterStart(context.Background(), noURL, Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	WaitPendingRoutes()
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
	// WaitPendingRoutes between the two calls as well as after: each one's
	// probe now runs on its own goroutine, and two overlapping goroutines
	// would race on the counters this stub increments.
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	WaitPendingRoutes()
	routeAfterStop(context.Background(), cfg, "mtplx", "Y/Q35") // a stop that still writes (an ollama stop writes nothing, #179)
	WaitPendingRoutes()
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

// TestRouteRestartSurvivesCallerCancelAfterReturn pins the fix for a
// regression the async redesign introduced. Every real caller of the route
// hook cancels its own ctx through a scoped `defer cancel()` the instant its
// call returns — wt start's startSignalCtx, the TUI start flow's st.cancel,
// the stop picker's and StopEntries' stopSignalCtx. That was harmless while
// the restart ran synchronously (it finished before the caller returned), but
// now it fires BEFORE the async restart starts. If the async phase inherited
// that cancellation, wt would write config.yaml and the proxy would silently
// never pick the change up — and the old "cancelled by the user, stay quiet"
// warning guard would hide it, since a post-return cancel is indistinguishable
// from a Ctrl+C.
//
// probeProxy blocks until the cancel has definitely landed, so this test
// cannot pass by winning a race with the goroutine.
func TestRouteRestartSurvivesCallerCancelAfterReturn(t *testing.T) {
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000"})
	_, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	cancelled := make(chan struct{})
	var restartCtx, waitCtx context.Context
	probeProxy = func(context.Context, string, time.Duration) bool {
		<-cancelled // the caller's scoped defer cancel() has already fired
		return true
	}
	restartProxy = func(ctx context.Context) []string { restartCtx = ctx; return nil }
	waitProxy = func(ctx context.Context, _ string, _ time.Duration) error { waitCtx = ctx; return nil }

	base, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "sentinel"))
	routeAfterStart(base, cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	cancel() // exactly what every caller's own `defer cancel()` does here
	close(cancelled)
	WaitPendingRoutes()

	if restartCtx == nil {
		t.Fatal("restart never ran — the caller's post-return cancel killed it")
	}
	if waitCtx == nil {
		t.Fatal("readiness wait never ran — the caller's post-return cancel killed it")
	}
	for name, c := range map[string]context.Context{"restart": restartCtx, "readiness wait": waitCtx} {
		// Detached from cancellation, but still a descendant carrying the
		// caller's values — not a bare context.Background().
		if c.Value(ctxKey{}) != "sentinel" {
			t.Errorf("%s ctx lost the caller's values", name)
		}
		select {
		case <-c.Done():
			t.Errorf("%s ctx was cancelled by the caller's post-return cancel", name)
		default:
		}
	}
	if warn.Len() != 0 {
		t.Fatalf("unexpected warning: %q", warn.String())
	}
}

// TestRouteWarnsWhenProxyWaitFailsUncancelled pins that a genuine readiness
// failure (ctx still live) is still surfaced as a warning.
func TestRouteWarnsWhenProxyWaitFailsUncancelled(t *testing.T) {
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000"})
	_, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	waitProxy = func(context.Context, string, time.Duration) error { return errors.New("proxy down") }
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	WaitPendingRoutes() // the warning is written by the async restart goroutine
	if !bytes.Contains(warn.Bytes(), []byte("proxy down")) {
		t.Fatalf("expected warning, got %q", warn.String())
	}
}

// TestRouteAfterStartDiscoveredSettlesOwedRestart pins that a start of a
// model with no registry overlay still bounces the proxy when an earlier
// deferred occupant-route removal is owed, even when its own route write
// changed nothing (e.g. a hand-written row already serves its name) — and
// that with nothing owed an unchanged write does not bounce. Skipping the
// owed bounce would leave the proxy serving the replaced model's dead route
// until a manual restart.
func TestRouteAfterStartDiscoveredSettlesOwedRestart(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	restarted := false
	restartProxy = func(context.Context) []string { restarted = true; return nil }
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "unregistered:9"}, true)
	// The settling bounce restarts asynchronously: let it finish before the
	// second stubRoutes below re-points the seams underneath it.
	WaitPendingRoutes()
	if len(*calls) != 1 || !restarted {
		t.Fatalf("owed restart: calls=%+v restarted=%v, want one write and a bounce", *calls, restarted)
	}
	calls, _ = stubRoutes(t, litellm.Result{}, nil)
	restarted = false
	restartProxy = func(context.Context) []string { restarted = true; return nil }
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "unregistered:9"}, false)
	WaitPendingRoutes()
	if len(*calls) != 1 || restarted {
		t.Fatalf("nothing owed: calls=%+v restarted=%v, want one write and no bounce", *calls, restarted)
	}
}

// TestApplyAndReportSettlesOwedRestartOnFailedWrite pins that a file-level
// write failure (the config.yaml lock wait cancelled, a save error) does not
// swallow a restart an earlier deferred write owes: under restartForced the
// replaced occupant's route removal is already in config.yaml, so the proxy
// is still bounced exactly once — otherwise it keeps serving the dead
// occupant's route until a manual restart. The other modes owe nothing, and a
// failed write changed nothing, so they must not bounce.
func TestApplyAndReportSettlesOwedRestartOnFailedWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode restartMode
		want int32
	}{
		{"forced", restartForced, 1},
		{"if-changed", restartIfChanged, 0},
		{"deferred", restartDeferred, 0},
	} {
		_, warn := stubRoutes(t, litellm.Result{}, errors.New("lock wait cancelled"))
		var restarts atomic.Int32
		restartProxy = func(context.Context) []string { restarts.Add(1); return nil }
		ch := litellm.Change{Add: []config.Model{litellm.DiscoveredModel("mtplx", "org/new")}}
		if changed := applyAndReport(context.Background(), routesCfg(), ch, tc.mode); changed {
			t.Errorf("%s: a failed write reported a change", tc.name)
		}
		WaitPendingRoutes()
		if got := restarts.Load(); got != tc.want {
			t.Errorf("%s: restarts = %d, want %d", tc.name, got, tc.want)
		}
		if !bytes.Contains(warn.Bytes(), []byte("lock wait cancelled")) {
			t.Errorf("%s: the write failure was not reported: %q", tc.name, warn.String())
		}
	}
}

// TestBounceRoutesSurvivesCancelledContext pins that the settling restart after
// a failed start runs on a live context even when the caller's is already
// cancelled (Ctrl+C mid-start). A restart on the cancelled ctx would be killed
// at once, leaving the proxy on its stale model list.
func TestBounceRoutesSurvivesCancelledContext(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	var restartErr error
	restarted := false
	restartProxy = func(ctx context.Context) []string { restarted, restartErr = true, ctx.Err(); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bounceRoutes(ctx, routesCfg())
	WaitPendingRoutes() // bounceRoutes returns before its restart has run
	if !restarted || restartErr != nil {
		t.Fatalf("restarted=%v ctx.Err=%v, want a restart on a live ctx", restarted, restartErr)
	}
}

// TestRouteProbesProxyOnlyWhenRestarting pins the lazy probe: a deferred write
// and an unchanged write never restart, so they must not pay the proxy probe
// (up to proxyAliveTimeout each) on every start and stop.
func TestRouteProbesProxyOnlyWhenRestarting(t *testing.T) {
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000"})
	stubRoutes(t, litellm.Result{Changed: true}, nil)
	probed := 0
	probeProxy = func(context.Context, string, time.Duration) bool { probed++; return true }
	// mtplx: an ollama stop never writes at all (#179).
	routeRemove(context.Background(), cfg, "mtplx", "Y/Q35", restartDeferred)
	WaitPendingRoutes() // nothing should be pending — prove it before re-stubbing
	stubRoutes(t, litellm.Result{Changed: false}, nil)
	probeProxy = func(context.Context, string, time.Duration) bool { probed++; return true }
	routeAfterStop(context.Background(), cfg, "mtplx", "Y/Q35")
	WaitPendingRoutes()
	if probed != 0 {
		t.Fatalf("probed %d times without a restart", probed)
	}
}

// TestApplyAndReportRestartsAsynchronously pins that the route write returns
// as soon as config.yaml is saved, without waiting for the proxy restart or
// readiness poll: that machinery is off the model-serving critical path, so
// control goes back to the caller — which then decides for itself where it
// needs the proxy ready — instead of every start and stop blocking in the
// hook. (It is not a wall-clock saving for a plain wt start/stop: main() waits
// via WaitPendingRoutes before the process exits either way.) WaitPendingRoutes
// must still observe the restart finish before the caller exits, or the restart
// would be killed mid-flight and the proxy never pick up the route change.
func TestApplyAndReportRestartsAsynchronously(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{Changed: true}, nil)
	restartStarted := make(chan struct{})
	restartMayFinish := make(chan struct{})
	restartProxy = func(context.Context) []string {
		close(restartStarted)
		<-restartMayFinish
		return nil
	}

	done := make(chan bool, 1)
	go func() {
		done <- applyAndReport(context.Background(), routesCfg(), litellm.Change{Add: routesCfg().Models[:1]}, restartIfChanged)
	}()

	select {
	case changed := <-done:
		if !changed {
			t.Fatal("applyAndReport returned false, want true (config.yaml changed)")
		}
	case <-time.After(time.Second):
		t.Fatal("applyAndReport blocked on the async restart instead of returning first")
	}
	<-restartStarted // the restart did start...
	close(restartMayFinish)
	WaitPendingRoutes() // ...and WaitPendingRoutes must observe it finish
	if len(*calls) != 1 {
		t.Fatalf("route calls = %d, want 1", len(*calls))
	}
}

// TestWaitPendingRoutesBlocksUntilTheRestartFinishes pins the guarantee the
// launch paths depend on: WaitPendingRoutes does not return while an async
// proxy restart is still running. wt start/stop only need it at process exit,
// but an agent launch (cmd/wt's startForLaunch, the TUI start flow, wt smoke's
// one-shot prompt) hands the model to a client that dials it THROUGH LiteLLM
// the instant the start returns — if this call could return early, that client
// would race a mid-restart proxy (connection refused) or one still serving the
// route table from before the model existed. Those callers stub their own seam
// over this function; this is the test that the real thing actually blocks.
func TestWaitPendingRoutesBlocksUntilTheRestartFinishes(t *testing.T) {
	stubRoutes(t, litellm.Result{Changed: true}, nil)
	var restartFinished atomic.Bool
	release := make(chan struct{})
	restartProxy = func(context.Context) []string {
		<-release
		restartFinished.Store(true)
		return nil
	}

	applyAndReport(context.Background(), routesCfg(), litellm.Change{Add: routesCfg().Models[:1]}, restartIfChanged)
	if restartFinished.Load() {
		t.Fatal("precondition: the restart finished before it was released")
	}

	waited := make(chan struct{})
	go func() { WaitPendingRoutes(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("WaitPendingRoutes returned while the restart was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("WaitPendingRoutes never returned after the restart finished")
	}
	if !restartFinished.Load() {
		t.Error("WaitPendingRoutes returned before the restart goroutine completed")
	}
}

// TestBounceRoutesLeavesConfigAlone pins that the settling bounce restarts the
// proxy without re-reading config.yaml. The removal it settles is already on
// disk — that is what restartOwed means — so re-entering applyAndReport for an
// empty change bought a second locked read and parse of the file the caller had
// just written, once per single-model stop (#142). Writing again would also put
// a lock wait back on this cleanup path, reached after a failed or Ctrl+C'd
// start, which no longer needs one: the restart is asynchronous and detached.
func TestBounceRoutesLeavesConfigAlone(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{Changed: true}, nil)
	restarted := false
	restartProxy = func(context.Context) []string { restarted = true; return nil }
	bounceRoutes(context.Background(), routesCfg())
	WaitPendingRoutes()

	if len(*calls) != 0 {
		t.Fatalf("bounceRoutes wrote config.yaml %d times, want 0: the removal is already written", len(*calls))
	}
	if !restarted {
		t.Fatal("bounceRoutes did not restart the proxy")
	}
}

// TestStartRouteChangeUsesTheRowsModelID pins #195: an artifact whose name
// fuzzy-matches a registry model ("org/Y/Q35" ends in "/Y/Q35") is a distinct
// model with its own discovered id. With the row's id carried in Target the
// route is written under that id; the lenient ModelFor fallback would route
// the registry model mtplx/Y--Q35, so the picker and the hook would disagree.
func TestStartRouteChangeUsesTheRowsModelID(t *testing.T) {
	cfg := routesCfg()
	ch := StartRouteChange(cfg, Target{ProviderID: "mtplx", ModelName: "org/Y/Q35", ModelID: "mtplx/org/Y/Q35"})
	if len(ch.Add) != 1 || ch.Add[0].ID != "mtplx/org/Y/Q35" {
		t.Fatalf("add = %+v, want the discovered id mtplx/org/Y/Q35", ch.Add)
	}
	if !slices.Equal(ch.RemoveFamilies, []string{"mtplx"}) {
		t.Errorf("families = %v, want the single-model family cleared", ch.RemoveFamilies)
	}
}

// TestStartRouteChangeExactRegistryID pins the other half: a ModelID that IS
// a registry id routes that registry model, whatever its ModelName spelling.
func TestStartRouteChangeExactRegistryID(t *testing.T) {
	ch := StartRouteChange(routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"})
	if len(ch.Add) != 1 || ch.Add[0].ID != "mtplx/Y--Q35" {
		t.Fatalf("add = %+v, want the registry model mtplx/Y--Q35", ch.Add)
	}
}

// TestStartRouteChangeWithoutModelIDKeepsTheFallback pins that a Target built
// without an id resolves as before: ModelFor, then the discovered model.
func TestStartRouteChangeWithoutModelIDKeepsTheFallback(t *testing.T) {
	ch := StartRouteChange(routesCfg(), Target{ProviderID: "mtplx", ModelName: "org/Y/Q35"})
	if len(ch.Add) != 1 || ch.Add[0].ID != "mtplx/Y--Q35" {
		t.Fatalf("add = %+v, want the fuzzy-matched registry model", ch.Add)
	}
}

// TestEnsureRouteWritesTheStartHooksChange pins that the launch-time ensure
// and the start hook write the same change: the model's route and, for a
// single-model provider, its family cleared. It also pins the one line a
// changed write prints.
func TestEnsureRouteWritesTheStartHooksChange(t *testing.T) {
	calls, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	changed := EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"})
	WaitPendingRoutes()
	if !changed {
		t.Fatal("changed = false, want true when the write changed config.yaml")
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want one", *calls)
	}
	c := (*calls)[0]
	if !slices.Equal(c.add, []string{"mtplx/Y--Q35"}) || len(c.remove) != 0 || !slices.Equal(c.families, []string{"mtplx"}) {
		t.Fatalf("call = %+v, want add [mtplx/Y--Q35] and the mtplx family cleared", c)
	}
	if got, want := warn.String(), "wt: LiteLLM route for mtplx/Y--Q35 updated\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestEnsureRouteQuietWhenNothingChanged covers a route that is already
// there and a discovered id served by a hand-written row (ApplyChange skips
// it): both come back unchanged, so nothing prints and the proxy is left
// alone.
func TestEnsureRouteQuietWhenNothingChanged(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{Changed: false}, nil)
	restarts := 0
	restartProxy = func(context.Context) []string { restarts++; return nil }
	changed := EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"})
	WaitPendingRoutes()
	if changed || warn.Len() != 0 || restarts != 0 {
		t.Fatalf("changed = %v output = %q restarts = %d, want an unchanged, silent no-op", changed, warn.String(), restarts)
	}
}

// TestEnsureRouteSilentWhenConfigMissing pins that a machine with no
// config.yaml — LiteLLM never set up — sees nothing at launch.
func TestEnsureRouteSilentWhenConfigMissing(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, litellm.ErrMissing)
	if EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"}) {
		t.Fatal("changed = true on a missing config.yaml")
	}
	WaitPendingRoutes()
	if warn.Len() != 0 {
		t.Fatalf("output = %q, want nothing", warn.String())
	}
}

// TestEnsureRouteWarnsAndNeverFails pins that a failed write is a warning,
// never an error the launch could trip on, and prints no "updated" line.
func TestEnsureRouteWarnsAndNeverFails(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, errors.New("boom"))
	if EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"}) {
		t.Fatal("changed = true on a failed write")
	}
	WaitPendingRoutes()
	if got, want := warn.String(), "wt: LiteLLM route not updated: boom\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestEnsureModelRouteSkipsNonLocal pins the guard: a cloud model and a model
// whose location cannot be resolved (its provider row is missing) are never
// written, and the second does not panic.
func TestEnsureModelRouteSkipsNonLocal(t *testing.T) {
	calls, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	cfg := routesCfg()
	cloud := config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud}
	orphan := config.Model{ID: "gone/y", ProviderID: "gone", ModelName: "y"}
	if EnsureModelRoute(cfg, cloud) || EnsureModelRoute(cfg, orphan) {
		t.Fatal("changed = true for a model that is not local")
	}
	if len(*calls) != 0 || warn.Len() != 0 {
		t.Fatalf("calls = %+v output = %q, want no write and no output", *calls, warn.String())
	}
}

// TestEnsureModelRouteBoundsTheLockWait pins that a launch cannot hang behind
// another wt holding the config.yaml lock: the write is handed a context with
// a deadline. It also pins the Target EnsureModelRoute builds — the row's id
// travels with it.
func TestEnsureModelRouteBoundsTheLockWait(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	inner := applyRoutes
	var hadDeadline bool
	var added string
	applyRoutes = func(cfg *config.Config, ch litellm.Change, o litellm.Options) (litellm.Result, error) {
		if o.Ctx != nil {
			_, hadDeadline = o.Ctx.Deadline()
		}
		if len(ch.Add) == 1 {
			added = ch.Add[0].ID
		}
		return inner(cfg, ch, o)
	}
	EnsureModelRoute(routesCfg(), config.Model{ID: "mtplx/org/Y/Q35", ProviderID: "mtplx", ModelName: "org/Y/Q35", Location: config.LocationLocal})
	WaitPendingRoutes()
	if !hadDeadline {
		t.Error("the route write got no deadline: a contended config.yaml lock would hang the launch")
	}
	if added != "mtplx/org/Y/Q35" {
		t.Errorf("added = %q, want the row's own id mtplx/org/Y/Q35", added)
	}
}
