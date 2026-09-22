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
	applyRoutes = func(_ *config.Config, add, remove []string, o litellm.Options) (litellm.Result, error) {
		if !o.SkipReadyGate {
			t.Error("lifecycle hook must skip the ready gate: the model is verifiably running")
		}
		if !o.NoRestart {
			t.Error("applyAndReport must always defer Apply's restart and run it asynchronously itself")
		}
		if o.ForceRestart {
			t.Error("ForceRestart would make Apply restart despite NoRestart, putting the bounce back on the caller's path")
		}
		calls = append(calls, routeCall{add, remove})
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
// single-model provider's model (mtplx) adds ITS route and removes routes of
// the provider's other models, since starting it stopped whatever ran before.
// Without the removal a replaced model keeps a dead route.
func TestRouteAfterStartSingleModelReplacesSiblings(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35"}, false)
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
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"}, false)
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
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	if !bytes.Contains(warn.Bytes(), []byte("boom")) {
		t.Fatalf("no warning for a failed apply: %q", warn.String())
	}

	_, warn = stubRoutes(t, litellm.Result{}, litellm.ErrMissing)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	if warn.Len() != 0 {
		t.Fatalf("missing config.yaml must be silent, got %q", warn.String())
	}

	calls, warn := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "unregistered:9"}, false)
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
	routeAfterStop(context.Background(), cfg, "ollama", "a:1")
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

// TestRouteAfterStartUnregisteredSettlesOwedRestart pins that a start of a
// model missing from the registry still bounces the proxy when an earlier
// deferred occupant-route removal is owed. Returning early would leave the
// proxy serving the replaced model's dead route until a manual restart.
func TestRouteAfterStartUnregisteredSettlesOwedRestart(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "unregistered:9"}, true)
	// The settling bounce restarts asynchronously: let it finish before the
	// second stubRoutes below re-points the seams underneath it.
	WaitPendingRoutes()
	if len(*calls) != 1 || len((*calls)[0].add) != 0 || len((*calls)[0].remove) != 0 {
		t.Fatalf("owed restart not settled: calls=%+v", *calls)
	}
	calls, _ = stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "unregistered:9"}, false)
	if len(*calls) != 0 {
		t.Fatalf("nothing owed, yet the proxy was touched: %+v", *calls)
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
	routeRemove(context.Background(), cfg, "ollama", "a:1", restartDeferred)
	WaitPendingRoutes() // nothing should be pending — prove it before re-stubbing
	stubRoutes(t, litellm.Result{Changed: false}, nil)
	probeProxy = func(context.Context, string, time.Duration) bool { probed++; return true }
	routeAfterStop(context.Background(), cfg, "ollama", "a:1")
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
		done <- applyAndReport(context.Background(), routesCfg(), []string{"ollama/a:1"}, nil, restartIfChanged)
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

	applyAndReport(context.Background(), routesCfg(), []string{"ollama/a:1"}, nil, restartIfChanged)
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

// TestBounceRoutesBoundsTheConfigWriteLockWait pins that the settling bounce
// hands litellm a context with a deadline. That context is what WithLock waits
// on for <config>.lock (see TestWithLockHonorsContext, which pins that a
// bounded context actually makes a contended lock give up): without a deadline
// here, a lock held by another wt or by modelman would hang this cleanup path
// — reached after a failed or Ctrl+C'd start — forever. The deadline must not
// leak into the async restart, which runs on the goroutine's own
// context.WithoutCancel; TestBounceRoutesSurvivesCancelledContext covers that
// side.
func TestBounceRoutesBoundsTheConfigWriteLockWait(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	var applyCtx context.Context
	applyRoutes = func(_ *config.Config, _, _ []string, o litellm.Options) (litellm.Result, error) {
		applyCtx = o.Ctx
		return litellm.Result{}, nil
	}
	began := time.Now()
	bounceRoutes(context.Background(), routesCfg())
	WaitPendingRoutes()

	if applyCtx == nil {
		t.Fatal("bounceRoutes passed no context to the config.yaml write")
	}
	dl, ok := applyCtx.Deadline()
	if !ok {
		t.Fatal("the config.yaml write's lock wait is unbounded — a contended flock would hang the settling bounce forever")
	}
	if budget := dl.Sub(began); budget <= 0 || budget > settleTimeout+time.Second {
		t.Errorf("lock-wait budget = %v, want (0, %v]", budget, settleTimeout)
	}
}
