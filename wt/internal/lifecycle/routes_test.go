package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// wrote is the Result of a write that added or rewrote id's own row — what
// the launch-time check's "route for <id> updated" line is printed for.
func wrote(id string) litellm.Result {
	return litellm.Result{Changed: true, Outcomes: []litellm.Outcome{{ID: id, Action: "routed", Written: true}}}
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

// TestRouteAfterStartOmlx6bitKeepsOmlxFamily pins the pool rule for the
// omlx/omlx-6bit pair (#213): both share one physical server that holds
// several models loaded, so starting the 6-bit model adds its route and clears
// nothing. Clearing the "omlx" family here removed the routes of models still
// loaded, which the next sync put back — the two undid each other.
func TestRouteAfterStartOmlx6bitKeepsOmlxFamily(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal}},
	}
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "omlx-6bit", ModelName: "Six"}, false)
	if len(*calls) != 1 || !slices.Equal((*calls)[0].add, []string{"omlx-6bit/Six"}) || len((*calls)[0].families) != 0 || len((*calls)[0].remove) != 0 {
		t.Fatalf("calls = %+v, want add [omlx-6bit/Six] and nothing removed", *calls)
	}
}

// TestRouteHooksRequestOnlyRealFamilies pins what the hooks may put in
// Change.RemoveFamilies: only a real, non-empty localmodels.Family value.
// ApplyChange matches families by name, so a raw provider id ("omlx-6bit")
// would be a silent no-op that leaves the replaced model's dead route behind,
// and "" is the family of every provider wt has no probe for — a hook that
// asked to clear it would be asking to clear routes it cannot name. So an
// omlx-6bit provider stop clears "omlx" (its start clears nothing: omlx is a
// pool, #213), an mtplx start and stop both clear "mtplx", and a family-less
// provider (retired llamacpp) requests no family removal at all, registered
// or not.
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
		start, stop     []string // RemoveFamilies of the start, and of the provider stop
	}{
		{"omlx-6bit", "Six", nil, []string{"omlx"}},
		{"omlx-6bit", "unregistered", nil, []string{"omlx"}},
		{"omlx", "unregistered", nil, []string{"omlx"}},
		{"mtplx", "org/unregistered", []string{"mtplx"}, []string{"mtplx"}},
		{"ollama", "unregistered:1", nil, nil},
		{"llamacpp", "G", nil, nil},
		{"llamacpp", "unregistered", nil, nil},
		{"no-such-provider", "x", nil, nil},
	} {
		calls, _ := stubRoutes(t, litellm.Result{}, nil)
		routeAfterStart(context.Background(), cfg, Target{ProviderID: tc.provider, ModelName: tc.model}, false)
		routeAfterStop(context.Background(), cfg, tc.provider)
		if len(*calls) == 0 {
			t.Errorf("%s/%s: no route write recorded for the start", tc.provider, tc.model)
			continue
		}
		if wantCalls := 1 + min(len(tc.stop), 1); len(*calls) != wantCalls {
			t.Errorf("%s/%s: %d route writes, want %d (the start, and a stop only when it clears a family)", tc.provider, tc.model, len(*calls), wantCalls)
			continue
		}
		for i, c := range *calls {
			want := tc.start
			if i > 0 {
				want = tc.stop
			}
			if !slices.Equal(c.families, want) {
				t.Errorf("%s/%s call %d: families = %q, want %q", tc.provider, tc.model, i, c.families, want)
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
	routeAfterStop(context.Background(), routesCfg(), "ollama")
	if len(*calls) != 0 {
		t.Fatalf("ollama stop wrote routes: %+v", *calls)
	}
	routeAfterStop(context.Background(), routesCfg(), "mtplx")
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
	routeAfterStop(context.Background(), cfg, "mtplx") // a stop that still writes (an ollama stop writes nothing, #179)
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
	routeRemoveFamily(context.Background(), cfg, "mtplx", restartDeferred)
	WaitPendingRoutes() // nothing should be pending — prove it before re-stubbing
	stubRoutes(t, litellm.Result{Changed: false}, nil)
	probeProxy = func(context.Context, string, time.Duration) bool { probed++; return true }
	routeAfterStop(context.Background(), cfg, "mtplx")
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

// TestEnsureRouteAddsOnlyTheModelsRoute pins that the launch-time ensure
// routes the model the start hook would, and removes nothing — not even a
// single-model provider's family. Nothing was stopped on this path, and an
// omlx/mtplx server can list sibling variants as running together: a family
// clear here would delete a running sibling's route, so alternating launches
// on two variants would each rewrite config.yaml, bounce the proxy and leave a
// live session on the other with "Invalid model name". It also pins the one
// line a changed write prints.
func TestEnsureRouteAddsOnlyTheModelsRoute(t *testing.T) {
	calls, warn := stubRoutes(t, wrote("mtplx/Y--Q35"), nil)
	changed := EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"})
	WaitPendingRoutes()
	if !changed {
		t.Fatal("changed = false, want true when the write changed config.yaml")
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want one", *calls)
	}
	c := (*calls)[0]
	if !slices.Equal(c.add, []string{"mtplx/Y--Q35"}) || len(c.remove) != 0 || len(c.families) != 0 {
		t.Fatalf("call = %+v, want add [mtplx/Y--Q35] and no removal: the ensure must not clear a running sibling's route", c)
	}
	if got, want := warn.String(), "wt: LiteLLM route for mtplx/Y--Q35 updated\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestRouteWriteReportsTheAPIBaseRepair pins that a start, stop or launch
// says so when its write fills an ollama row's empty api_base (#202). Any
// write of config.yaml makes that repair, and the row is usually one the user
// wrote by hand that the operation never named: only `wt litellm sync` listed
// it, so the first write after an upgrade — most often a launch — changed a
// hand-written row with nothing on screen but the launched model's own
// "updated" line. That line stays last: it is what the picker's status shows.
func TestRouteWriteReportsTheAPIBaseRepair(t *testing.T) {
	res := litellm.Result{Changed: true, Outcomes: []litellm.Outcome{
		{ID: "ollama/a:1", Action: "routed", Written: true},
		{ID: "ollama/q8", Action: litellm.ActionAPIBaseSet},
	}}
	_, warn := stubRoutes(t, res, nil)
	EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"})
	WaitPendingRoutes()
	want := "wt: LiteLLM route for ollama/q8: api_base set\nwt: LiteLLM route for ollama/a:1 updated\n"
	if got := warn.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestEnsureRouteSaysUpdatedOnlyForItsOwnRoute pins #206: a launch prints
// "route for <id> updated" only when that model's own row was added or
// rewritten. Any write also fills other rows' api_base and enforces settings,
// and the line used to be printed for every write that changed the file — so a
// launch announced a route change for a model whose route was byte-identical,
// and printed "updated" right beside that model's own "not updated". What the
// write did is said by the line already printed for it; when there is none
// (an enforced setting), one line names the file, since the launch is about to
// wait for a proxy restart either way. Both forms of the check print the same.
func TestEnsureRouteSaysUpdatedOnlyForItsOwnRoute(t *testing.T) {
	const id = "ollama/a:1"
	mdl := config.Model{ID: id, ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal}
	for _, tc := range []struct {
		name     string
		outcomes []litellm.Outcome
		want     string
	}{
		{"its own row written", []litellm.Outcome{{ID: id, Action: "routed", Written: true}}, "wt: LiteLLM route for ollama/a:1 updated\n"},
		{"only another row repaired", []litellm.Outcome{{ID: id, Action: "routed"}, {ID: "ollama/q8", Action: litellm.ActionAPIBaseSet}}, "wt: LiteLLM route for ollama/q8: api_base set\n"},
		{"only a setting enforced", []litellm.Outcome{{ID: id, Action: "routed"}}, "wt: LiteLLM config.yaml updated\n"},
		{"left to a hand-written row", nil, "wt: LiteLLM config.yaml updated\n"},
		{"its row could not be built", []litellm.Outcome{{ID: id, Err: errors.New("no key")}}, "wt: LiteLLM route for ollama/a:1 not updated: no key\n"},
		{"another model's row written", []litellm.Outcome{{ID: id, Action: "routed"}, {ID: "ollama/b:1", Action: "routed", Written: true}}, "wt: LiteLLM config.yaml updated\n"},
	} {
		res := litellm.Result{Changed: true, Outcomes: tc.outcomes}
		_, warn := stubRoutes(t, res, nil)
		if !EnsureModelRoute(routesCfg(), mdl) {
			t.Errorf("%s: changed = false, want true: the file changed and the proxy restarts", tc.name)
		}
		WaitPendingRoutes()
		if got := warn.String(); got != tc.want {
			t.Errorf("%s: blocking check printed %q, want %q", tc.name, got, tc.want)
		}
		var out bytes.Buffer
		if changed, done := TryEnsureModelRouteTo(&out, routesCfg(), mdl); !changed || !done {
			t.Errorf("%s: changed=%v done=%v, want both true", tc.name, changed, done)
		}
		WaitPendingRoutes()
		if got := out.String(); got != tc.want {
			t.Errorf("%s: non-blocking check printed %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestEnsureRouteUpdatedLineAgainstTheRealWriter runs the same rule through
// litellm.ApplyChange and a real config.yaml, so it rests on what the writer
// actually reports rather than on a stubbed Result: the first launch writes
// the route, a launch that only repairs another row or only restores a setting
// rewrites the file and restarts the proxy without claiming the route changed,
// and a launch with nothing to do is silent.
func TestEnsureRouteUpdatedLineAgainstTheRealWriter(t *testing.T) {
	seed := "model_list:\n  - model_name: hand/other\n    litellm_params:\n      model: ollama/other\n      api_base: http://h:1\n"
	path, restarts, warn := realRoutes(t, seed)
	tg := Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"}
	launch := func(label string, edit func(string) string, wantRestart bool, want string) {
		t.Helper()
		if edit != nil {
			b, _ := os.ReadFile(path)
			after := edit(string(b))
			if after == string(b) {
				t.Fatalf("%s: the fixture edit matched nothing", label)
			}
			if err := os.WriteFile(path, []byte(after), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		warn.Reset()
		n := restarts()
		changed := EnsureRoute(context.Background(), routesCfg(), tg)
		WaitPendingRoutes()
		if changed != wantRestart || (restarts()-n == 1) != wantRestart {
			t.Errorf("%s: changed=%v restarts=+%d, want changed=%v", label, changed, restarts()-n, wantRestart)
		}
		if got := warn.String(); got != want {
			t.Errorf("%s: printed %q, want %q", label, got, want)
		}
	}
	launch("first launch", nil, true, "wt: LiteLLM route for ollama/a:1 updated\n")
	launch("nothing to do", nil, false, "")
	launch("another row repaired", func(s string) string { return strings.Replace(s, "      api_base: http://h:1\n", "", 1) },
		true, "wt: LiteLLM route for hand/other: api_base set\n")
	launch("a setting enforced", func(s string) string {
		return strings.Replace(s, "  use_chat_completions_url_for_anthropic_messages: true\n", "", 1)
	}, true, "wt: LiteLLM config.yaml updated\n")
}

// TestEnsureRouteQuietWhenNothingChanged pins that a write reporting no
// change prints nothing and restarts nothing, whatever the reason it was
// unchanged. The write is stubbed, so why ApplyChange leaves a file alone
// (the route is already there, a hand-written row holds the name) is not
// exercised here.
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

// cloudRoutesCfg is routesCfg plus what a cloud launch needs: an openrouter
// provider with one model, a native provider with one model, and a cloud
// model on the local ollama provider.
func cloudRoutesCfg() *config.Config {
	cfg := routesCfg()
	cfg.Providers = append(cfg.Providers,
		config.Provider{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key", SecretRef: "sk-test", BaseURL: "https://openrouter.ai/api/v1"}},
		config.Provider{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
	)
	cfg.Models = append(cfg.Models,
		config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud},
		config.Model{ID: "ollama/g:cloud", ProviderID: "ollama", ModelName: "g:cloud", Location: config.LocationCloud},
		config.Model{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Native: true},
	)
	return cfg
}

// TestEnsureModelRouteRoutesARegistryCloudModel pins the launch-time repair
// for cloud routes. Sync is what routes cloud models, so the check used to
// skip them — and a config.yaml that had lost its cloud rows (a sync against
// the wrong registry removed all of them) stayed broken: every launch on a
// cloud model got "Invalid model name" from the proxy until someone ran
// `wt litellm sync` by hand, while local launches repaired themselves. The
// check now adds the launched cloud model's route too, and still removes
// nothing.
func TestEnsureModelRouteRoutesARegistryCloudModel(t *testing.T) {
	for _, id := range []string{"openrouter/x", "ollama/g:cloud"} {
		t.Run(id, func(t *testing.T) {
			calls, warn := stubRoutes(t, wrote(id), nil)
			cfg := cloudRoutesCfg()
			m := cfg.Models[config.IndexModelByID(cfg.Models, id)]
			changed := EnsureModelRoute(cfg, m)
			WaitPendingRoutes()
			if !changed {
				t.Fatal("changed = false, want the missing cloud route written")
			}
			if len(*calls) != 1 {
				t.Fatalf("calls = %+v, want one", *calls)
			}
			if c := (*calls)[0]; !slices.Equal(c.add, []string{id}) || len(c.remove) != 0 || len(c.families) != 0 {
				t.Fatalf("call = %+v, want add [%s] and no removal", c, id)
			}
			if got, want := warn.String(), "wt: LiteLLM route for "+id+" updated\n"; got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
		})
	}
}

// TestEnsureModelRouteCloudAgainstTheRealWriter runs the cloud repair through
// litellm.ApplyChange and a real config.yaml: the first launch writes the row
// sync would write (marked, so sync owns it afterwards) and restarts the proxy
// once, and the next launch finds it in place and does nothing.
func TestEnsureModelRouteCloudAgainstTheRealWriter(t *testing.T) {
	path, restarts, warn := realRoutes(t, "model_list: []\n")
	cfg := cloudRoutesCfg()
	m := cfg.Models[config.IndexModelByID(cfg.Models, "openrouter/x")]
	if !EnsureModelRoute(cfg, m) {
		t.Fatalf("first launch: changed = false, output %q", warn.String())
	}
	WaitPendingRoutes()
	if got := routedIDs(t, path); !slices.Equal(got, []string{"openrouter/x"}) {
		t.Fatalf("routed = %v, want [openrouter/x]", got)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "wt_managed: true") {
		t.Fatalf("the row is not marked, so sync would not own it:\n%s", b)
	}
	warn.Reset()
	if EnsureModelRoute(cfg, m) {
		t.Fatal("second launch: changed = true, want a no-op")
	}
	WaitPendingRoutes()
	if restarts() != 1 || warn.Len() != 0 {
		t.Fatalf("restarts = %d output = %q, want one restart in all and a silent second launch", restarts(), warn.String())
	}
}

// TestEnsureModelRouteLeavesAnExistingCloudRowAlone pins that the cloud repair
// is for a missing row only. Building a cloud row resolves its provider's
// secret_ref, and the launching shell often does not hold that key — the
// proxy's plist does — so a check that rebuilt the row on every launch printed
// "route … not updated: secret_ref … resolved empty" before every cloud
// launch although the route was in place and working; in a shell holding a
// different key it replaced the api_key sync wrote and restarted the proxy. A
// row that is there is sync's to keep current.
func TestEnsureModelRouteLeavesAnExistingCloudRowAlone(t *testing.T) {
	path, restarts, warn := realRoutes(t, "model_list: []\n")
	cfg := cloudRoutesCfg()
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "openrouter" {
			cfg.Providers[i].Auth.SecretRef = "WT_TEST_OPENROUTER_KEY"
		}
	}
	m := cfg.Models[config.IndexModelByID(cfg.Models, "openrouter/x")]
	t.Setenv("WT_TEST_OPENROUTER_KEY", "sk-proxy")
	if !EnsureModelRoute(cfg, m) {
		t.Fatalf("first launch: changed = false, output %q", warn.String())
	}
	WaitPendingRoutes()
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "sk-another-shell"} {
		t.Setenv("WT_TEST_OPENROUTER_KEY", key)
		warn.Reset()
		if EnsureModelRoute(cfg, m) {
			t.Errorf("key %q: changed = true, want the existing row left alone", key)
		}
		WaitPendingRoutes()
		if warn.Len() != 0 {
			t.Errorf("key %q: output = %q, want none: the route is in place", key, warn.String())
		}
		if now, _ := os.ReadFile(path); string(now) != string(written) {
			t.Errorf("key %q: config.yaml was rewritten:\n%s", key, now)
		}
	}
	if restarts() != 1 {
		t.Errorf("restarts = %d, want only the first launch's", restarts())
	}
}

// execSecretRef points a provider's secret_ref at a helper that records that
// it ran, and returns the marker path and the ref. The ref is unique per test
// (config.ResolveSecret memoizes successful resolutions process-wide by ref),
// so one test's helper can never stand in for another's.
func execSecretRef(t *testing.T) (marker, ref string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "ran")
	script := filepath.Join(dir, "key.sh")
	body := "#!/bin/sh\necho ran > " + marker + "\necho sk-from-helper\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return marker, "exec:" + script
}

// cloudRoutesCfgWithExecSecret is cloudRoutesCfg with openrouter's secret_ref
// replaced by an exec: helper, returning the marker that helper writes.
func cloudRoutesCfgWithExecSecret(t *testing.T) (*config.Config, string) {
	t.Helper()
	marker, ref := execSecretRef(t)
	cfg := cloudRoutesCfg()
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "openrouter" {
			cfg.Providers[i].Auth.SecretRef = ref
		}
	}
	return cfg, marker
}

// TestTryEnsureModelRouteDefersAMissingCloudRow pins #253: the launch-time
// check's non-blocking form must not run a cloud provider's exec: secret_ref
// helper on the update goroutine. Building a cloud row resolves that secret,
// and an exec: helper runs synchronously, bounded only by execSecretTimeout
// (15s) — so a launch of a cloud model whose route was missing froze the
// picker, ctrl+c included, before its routing screen had even been entered.
// The row build is deferred instead: the check reports that it got nowhere, and
// the command behind the routing screen rebuilds where waiting is affordable.
// Deferring must not lose the route, so the retry has to write it.
func TestTryEnsureModelRouteDefersAMissingCloudRow(t *testing.T) {
	path, restarts, _ := realRoutes(t, "model_list: []\n")
	cfg, marker := cloudRoutesCfgWithExecSecret(t)
	m := cfg.Models[config.IndexModelByID(cfg.Models, "openrouter/x")]
	// Converge the file with a write that names no model, so the retry at the
	// end is deciding on the row alone. (A deferred check writes nothing either
	// way — TestTryEnsureModelRouteDeferredCheckWritesNothing.)
	if _, err := litellm.ApplyChange(cfg, litellm.Change{}, litellm.Options{Path: path}); err != nil {
		t.Fatal(err)
	}
	seededRestarts := restarts()

	var out bytes.Buffer
	changed, done := TryEnsureModelRouteTo(&out, cfg, m)
	WaitPendingRoutes()

	if changed || done {
		t.Fatalf("changed = %v done = %v, want false and false: the row build is the caller's retry to make", changed, done)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the exec: secret_ref helper ran on the non-blocking check: that wait is what freezes the picker")
	}
	if got := routedIDs(t, path); len(got) != 0 {
		t.Fatalf("routed = %v, want the row still missing", got)
	}
	if got := restarts(); got != seededRestarts {
		t.Fatalf("restarts = +%d, want 0: nothing was written", got-seededRestarts)
	}

	// The retry, where waiting is affordable, is what builds the row.
	if !EnsureModelRouteTo(&out, cfg, m) {
		t.Fatalf("the retry changed = false, output %q", out.String())
	}
	WaitPendingRoutes()
	if got := routedIDs(t, path); !slices.Equal(got, []string{"openrouter/x"}) {
		t.Fatalf("routed = %v, want [openrouter/x]: the deferred build must still happen", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the retry never resolved the exec: secret_ref: %v", err)
	}
}

// TestTryEnsureModelRouteDeferredCheckWritesNothing pins that a check which
// defers the row is as empty-handed as one that found the lock held: nothing
// saved, nothing restarted, nothing printed. The repairs every write makes
// (litellm_settings, ollama api_base) used to be saved on the deferred attempt,
// so a launch against a config.yaml that lacked them restarted the proxy from
// the update goroutine and then a second time when the retry wrote the row.
func TestTryEnsureModelRouteDeferredCheckWritesNothing(t *testing.T) {
	path, restarts, _ := realRoutes(t, "model_list: []\n")
	cfg, _ := cloudRoutesCfgWithExecSecret(t)
	m := cfg.Models[config.IndexModelByID(cfg.Models, "openrouter/x")]
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	changed, done := TryEnsureModelRouteTo(&out, cfg, m)
	WaitPendingRoutes()

	if changed || done {
		t.Fatalf("changed = %v done = %v, want false and false", changed, done)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, before) {
		t.Fatalf("config.yaml = %q (err %v), want it exactly as it was", after, err)
	}
	if got := restarts(); got != 0 || out.Len() != 0 {
		t.Fatalf("restarts = %d output = %q, want none: the retry owns the whole write", got, out.String())
	}

	// The retry writes the row and the repairs together, for one restart.
	if !EnsureModelRouteTo(&out, cfg, m) {
		t.Fatalf("the retry changed = false, output %q", out.String())
	}
	WaitPendingRoutes()
	if got := restarts(); got != 1 {
		t.Fatalf("restarts = %d, want 1 for the whole launch", got)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"openrouter/x"}) {
		t.Fatalf("routed = %v, want [openrouter/x]", got)
	}
}

// TestTryEnsureModelRouteFinishesForACloudRowThatIsThere pins the other side of
// the deferral: a route already in config.yaml is the case this repair exists
// to leave alone, so nothing is deferred and the check finishes on the update
// goroutine — the launch continues without a routing screen, exactly as before.
// Reporting a present row as deferred would send every cloud launch to that
// screen and re-run the whole check for a route that is already in place.
func TestTryEnsureModelRouteFinishesForACloudRowThatIsThere(t *testing.T) {
	path, restarts, _ := realRoutes(t, "model_list: []\n")
	cfg, marker := cloudRoutesCfgWithExecSecret(t)
	m := cfg.Models[config.IndexModelByID(cfg.Models, "openrouter/x")]
	// Put the row there the way a launch does, with a secret this shell resolves
	// without a helper, so the file already holds everything a write enforces and
	// the check below is deciding on the row alone. The providers are cloned: a
	// plain struct copy shares cfg's slice, and the seed's literal secret would
	// replace the exec: ref in cfg too — leaving no helper for the marker
	// assertion below to catch.
	seed := *cfg
	seed.Providers = slices.Clone(cfg.Providers)
	for i := range seed.Providers {
		if seed.Providers[i].ID == "openrouter" {
			seed.Providers[i].Auth.SecretRef = "sk-test"
		}
	}
	if !EnsureModelRouteTo(&bytes.Buffer{}, &seed, m) {
		t.Fatal("fixture: the seeded launch wrote no row")
	}
	WaitPendingRoutes()
	seededRestarts := restarts()

	var out bytes.Buffer
	changed, done := TryEnsureModelRouteTo(&out, cfg, m)
	WaitPendingRoutes()

	if changed || !done {
		t.Fatalf("changed = %v done = %v, want false and true: the row is there and the check is finished", changed, done)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the exec: secret_ref helper ran for a row that is already routed (#252)")
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"openrouter/x"}) {
		t.Fatalf("routed = %v, want the seeded row untouched", got)
	}
	if got := restarts(); got != seededRestarts || out.Len() != 0 {
		t.Fatalf("restarts = +%d output = %q, want a silent no-op", got-seededRestarts, out.String())
	}
}

// TestTryEnsureModelRouteReportsARefusedPairingAtOnce pins that the picker's
// non-blocking check treats litellm.ErrRegistryRedirected as an answer, not as
// a held lock. done == false sends the picker to its routing screen to retry
// with a lock wait — and no wait changes a refusal, so every launch with a
// redirected registry showed that screen and ran the check twice to print one
// line.
func TestTryEnsureModelRouteReportsARefusedPairingAtOnce(t *testing.T) {
	_, restarts, _ := realRoutes(t, "model_list: []\n")
	home := t.TempDir()
	def := filepath.Join(home, ".config", "litellm", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(def), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(def, []byte("model_list: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MODELMAN_REGISTRY", filepath.Join(home, "scratch", "registry.toml"))
	t.Setenv("WT_LITELLM_CONFIG", "")
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")

	var out bytes.Buffer
	changed, done := TryEnsureModelRouteTo(&out, routesCfg(), config.Model{
		ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal,
	})
	WaitPendingRoutes()
	if changed || !done {
		t.Fatalf("changed = %v done = %v, want false and true: refused, with nothing to retry", changed, done)
	}
	if got := out.String(); !strings.HasPrefix(got, "wt: LiteLLM route not updated: ") || !strings.Contains(got, "WT_LITELLM_CONFIG") {
		t.Errorf("output = %q, want the refusal and the variable that lifts it", got)
	}
	if got := routedIDs(t, def); len(got) != 0 {
		t.Errorf("routed = %v, want the default config.yaml untouched", got)
	}
	if restarts() != 0 {
		t.Errorf("restarts = %d, want 0", restarts())
	}
}

// TestEnsureModelRouteSkipsWhatSyncWouldNotRoute pins the guard: the check
// writes only a route sync itself would write. A cloud model that is not in
// the registry, a native model, and a model whose location cannot be resolved
// (its provider row is missing) are never written, and the last does not
// panic.
func TestEnsureModelRouteSkipsWhatSyncWouldNotRoute(t *testing.T) {
	calls, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	cfg := cloudRoutesCfg()
	unregistered := config.Model{ID: "openrouter/nope", ProviderID: "openrouter", ModelName: "nope", Location: config.LocationCloud}
	native := cfg.Models[config.IndexModelByID(cfg.Models, "claude/sonnet")]
	orphan := config.Model{ID: "gone/y", ProviderID: "gone", ModelName: "y"}
	if EnsureModelRoute(cfg, unregistered) || EnsureModelRoute(cfg, native) || EnsureModelRoute(cfg, orphan) {
		t.Fatal("changed = true for a model sync would not route")
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

// TestTryEnsureModelRouteDoesNotWaitForTheLock pins the launch-time check's
// non-blocking form against a lock another wt is genuinely holding: it reports
// that it got nowhere, promptly, and writes nothing. This is what keeps the TUI
// picker's update goroutine — the one that repaints the screen and answers keys
// — from freezing for ensureRouteLockTimeout before its routing screen has been
// entered (#192 review). A regression here does not fail an assertion, it hangs
// the picker with ctrl+c dead, so the guard below is the test.
func TestTryEnsureModelRouteDoesNotWaitForTheLock(t *testing.T) {
	cfgPath, restarts, warn := realRoutes(t, "model_list: []\n")
	cfg := routesCfg()
	mdl := config.Model{ID: "mtplx/Y--Q35", ProviderID: "mtplx", ModelName: "Y/Q35", Location: config.LocationLocal}

	// Hold the lock the way another wt mid-write would. flock conflicts between
	// two open file descriptions, this process's included, so a plain goroutine
	// with its own WithLock is enough to contend.
	release := make(chan struct{})
	holding, held := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(held)
		_ = litellm.WithLock(context.Background(), cfgPath, func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	var changed, done bool
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		changed, done = TryEnsureModelRouteTo(nil, cfg, mdl)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		// Unblock the holder so the check can finish writing before the test
		// ends; the assertions are moot on a failure path that already fataled.
		close(release)
		<-held
		<-returned
		t.Fatal("TryEnsureModelRouteTo waited for a held config.yaml lock: the picker would freeze")
	}
	close(release)
	<-held
	WaitPendingRoutes()

	if done {
		t.Error("done = true, want false: nothing was established while the lock was held")
	}
	if changed {
		t.Error("changed = true, want false: nothing was written")
	}
	if warn.String() != "" {
		t.Errorf("output = %q, want none: a contended lock is not a warning, the retry reports", warn.String())
	}
	if got := routedIDs(t, cfgPath); len(got) != 0 {
		t.Errorf("routed = %v, want none: the check wrote config.yaml while another wt held it", got)
	}
	if got := restarts(); got != 0 {
		t.Errorf("restarts = %d, want 0: nothing changed, so no proxy bounce", got)
	}
}

// TestTryEnsureModelRouteRoutesWhenTheLockIsFree pins the other side of the same
// form: a free lock is still taken, so the common launch — the route already
// present — is decided here, on the update goroutine, in one non-blocking call.
// WithLock only runs its first flock before it consults a deadline, and that is
// what an already-cancelled context relies on; a form that turned contention
// into "never write" would push every launch of a running model onto the retry
// path and its routing screen.
func TestTryEnsureModelRouteRoutesWhenTheLockIsFree(t *testing.T) {
	cfgPath, restarts, warn := realRoutes(t, "model_list: []\n")

	changed, done := TryEnsureModelRouteTo(nil, routesCfg(), config.Model{
		ID: "mtplx/Y--Q35", ProviderID: "mtplx", ModelName: "Y/Q35", Location: config.LocationLocal,
	})
	WaitPendingRoutes()

	if !done || !changed {
		t.Fatalf("changed = %v done = %v, want both true with the lock free", changed, done)
	}
	if got := routedIDs(t, cfgPath); !slices.Contains(got, "mtplx/Y--Q35") {
		t.Errorf("routed = %v, want mtplx/Y--Q35", got)
	}
	if want := "wt: LiteLLM route for mtplx/Y--Q35 updated\n"; warn.String() != want {
		t.Errorf("output = %q, want %q", warn.String(), want)
	}
	// The write itself changed the file, so the proxy must have bounced: this
	// form bounces exactly as the blocking one does, it just does not wait.
	if got := restarts(); got != 1 {
		t.Errorf("restarts = %d, want 1", got)
	}
}

// TestTryEnsureModelRouteGivesTheWriteNoTimeToWait pins the mechanism at the
// seam it lives at: the non-blocking form hands ApplyChange a context that is
// already done, so WithLock cannot poll (lockPollInterval) its way up to a
// deadline. It is deliberately not "a short wait" — a 50ms budget would pass
// every behavioural test here and still put a stall on the update goroutine.
func TestTryEnsureModelRouteGivesTheWriteNoTimeToWait(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	inner := applyRoutes
	var ctxErr error
	var hadDeadline bool
	applyRoutes = func(cfg *config.Config, ch litellm.Change, o litellm.Options) (litellm.Result, error) {
		if o.Ctx != nil {
			ctxErr = o.Ctx.Err()
			_, hadDeadline = o.Ctx.Deadline()
		}
		return inner(cfg, ch, o)
	}
	TryEnsureModelRouteTo(nil, routesCfg(), config.Model{
		ID: "mtplx/Y--Q35", ProviderID: "mtplx", ModelName: "Y/Q35", Location: config.LocationLocal,
	})
	WaitPendingRoutes()

	if ctxErr == nil {
		t.Error("the route write got a live context: WithLock would poll for the lock up to its caller's deadline")
	}
	if hadDeadline {
		t.Error("the route write got a deadline, want none: a deadline is a wait, however short")
	}
}

// TestEnsureModelRouteToSendsItsOutputToTheCaller pins what the TUI picker
// relies on (#192): a check handed a writer prints there and not on stderr —
// the "updated" line and, as long as the caller reads only after
// WaitPendingRoutes, what the asynchronous proxy restart prints too. A failed
// restart is the warning that explains an "Invalid model name" launch, and it
// is printed by a goroutine that outlives the check; under Bubble Tea's alt
// screen stderr is where nobody would see either line. The next check, handed
// no writer, must print on stderr as before: the writer belongs to one check,
// not to the process. Run under -race this also pins that the restart
// goroutine's write and the check's own do not race.
func TestEnsureModelRouteToSendsItsOutputToTheCaller(t *testing.T) {
	_, stderr := stubRoutes(t, wrote("ollama/a:1"), nil)
	restartProxy = func(context.Context) []string { return []string{"LiteLLM proxy restart failed: boom"} }
	mdl := config.Model{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal}
	const want = "wt: LiteLLM route for ollama/a:1 updated\nwt: LiteLLM proxy restart failed: boom\n"

	var captured bytes.Buffer
	if !EnsureModelRouteTo(&captured, routesCfg(), mdl) {
		t.Fatal("changed = false, want true when the write changed config.yaml")
	}
	WaitPendingRoutes()
	if got := captured.String(); got != want {
		t.Fatalf("the check's writer got %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr got %q, want nothing: the check was handed its own writer", stderr.String())
	}

	EnsureModelRoute(routesCfg(), mdl)
	WaitPendingRoutes()
	if got := stderr.String(); got != want {
		t.Fatalf("stderr after a check with no writer = %q, want %q", got, want)
	}
	if got := captured.String(); got != want {
		t.Fatalf("the first check's writer = %q after a later check, want it untouched (%q)", got, want)
	}
}

// TestRouteOutputIsNotSharedWithAnotherOperation pins the #192 review finding
// that replaced SetRouteOutput. That redirect swapped one process-wide writer
// for the length of a check, so it caught whatever any route operation printed
// in that window: a start whose replace failed leaves a proxy restart in
// flight, the user launches another, running model while it is, and the first
// model's restart warning was collected as the second launch's note — shown in
// its status line, in place of its own "updated" line. A check's writer now
// travels with the check, so the unrelated restart still prints on stderr.
func TestRouteOutputIsNotSharedWithAnotherOperation(t *testing.T) {
	_, stderr := stubRoutes(t, wrote("ollama/a:1"), nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var restarts atomic.Int32
	restartProxy = func(context.Context) []string {
		if restarts.Add(1) > 1 {
			return nil // the check's own restart: nothing to report
		}
		// The other operation's restart: held open across the whole check,
		// then failing.
		close(entered)
		<-release
		return []string{"LiteLLM proxy restart failed: the other model's"}
	}

	// The other operation: a failed replace settling the occupant's removal.
	bounceRoutes(context.Background(), routesCfg())
	<-entered

	var captured bytes.Buffer
	changed, done := TryEnsureModelRouteTo(&captured, routesCfg(), config.Model{
		ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal,
	})
	if !changed || !done {
		t.Fatalf("changed = %v done = %v, want the check to write its route", changed, done)
	}
	close(release)
	WaitPendingRoutes()

	if got, want := captured.String(), "wt: LiteLLM route for ollama/a:1 updated\n"; got != want {
		t.Errorf("the check's writer got %q, want only its own line %q", got, want)
	}
	if got, want := stderr.String(), "wt: LiteLLM proxy restart failed: the other model's\n"; got != want {
		t.Errorf("stderr got %q, want the other operation's warning %q", got, want)
	}
}
