package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

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

// profileCfg is cloudRoutesCfg with a second openrouter model and the
// [litellm] url set, the registry a mixed slot profile needs.
func profileCfg() *config.Config {
	cfg := cloudRoutesCfg()
	cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/y", ProviderID: "openrouter", ModelName: "y", Location: config.LocationCloud})
	cfg.SetLitellmForTest(config.LitellmState{URL: proxyURL, APIKey: "sk-test-1234"})
	return cfg
}

// modelsByID returns cfg's models with the given ids, in the order asked.
func modelsByID(t *testing.T, cfg *config.Config, ids ...string) []config.Model {
	t.Helper()
	var out []config.Model
	for _, id := range ids {
		i := config.IndexModelByID(cfg.Models, id)
		if i < 0 {
			t.Fatalf("fixture has no model %q", id)
		}
		out = append(out, cfg.Models[i])
	}
	return out
}

// sortedRoutedIDs is routedIDs in a stable order: the order of rows in
// config.yaml is the writer's business, not these tests'.
func sortedRoutedIDs(t *testing.T, path string) []string {
	t.Helper()
	ids := routedIDs(t, path)
	slices.Sort(ids)
	return ids
}

// TestEnsureModelRoutesWritesMissingCloudRoutesAndRestartsNothing pins the
// first half of "at most one proxy restart per profile launch": two missing
// cloud routes are written by two locked writes, the call reports a restart
// as owed, and it restarts nothing itself. If it restarted per write, a
// profile with four cloud slots would bounce the proxy four times, each
// bounce killing the one before.
func TestEnsureModelRoutesWritesMissingCloudRoutesAndRestartsNothing(t *testing.T) {
	path, _, warn := realRoutes(t, "model_list: []\n")
	restarts := fakeRestarter(t)
	cfg := profileCfg()

	owed, err := EnsureModelRoutes(context.Background(), nil, cfg, modelsByID(t, cfg, "openrouter/x", "openrouter/y"), false)
	WaitPendingRoutes()
	if err != nil || !owed {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (true, nil); output %q", owed, err, warn.String())
	}
	if got, want := sortedRoutedIDs(t, path), []string{"openrouter/x", "openrouter/y"}; !slices.Equal(got, want) {
		t.Fatalf("routed = %v, want %v", got, want)
	}
	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarts = %d, want 0: the caller owes the one restart", n)
	}
	want := "wt: LiteLLM route for openrouter/x updated\nwt: LiteLLM route for openrouter/y updated\n"
	if got := warn.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestEnsureModelRoutesErrors pins every result the single-model check
// treats as a non-failure and a profile launch must stop on. A profile that
// launched anyway would start Claude Code with a slot the proxy answers
// "Invalid model name" for. Each case also pins that nothing is restarted
// when nothing is owed.
func TestEnsureModelRoutesErrors(t *testing.T) {
	rowErr := errors.New(`secret_ref "OPENROUTER_API_KEY" resolved empty`)
	lockErr := fmt.Errorf("waiting for /x/config.yaml.lock: %w", context.DeadlineExceeded)
	tests := []struct {
		name      string
		id        string
		res       litellm.Result
		err       error
		wantIs    error
		wantCalls int
	}{
		{name: "config.yaml missing", id: "openrouter/x", err: fmt.Errorf("%w: /x/config.yaml", litellm.ErrMissing), wantIs: litellm.ErrMissing, wantCalls: 1},
		{name: "registry redirected", id: "openrouter/x", err: fmt.Errorf("%w: set WT_LITELLM_CONFIG", litellm.ErrRegistryRedirected), wantIs: litellm.ErrRegistryRedirected, wantCalls: 1},
		{name: "config.yaml invalid", id: "openrouter/x", err: fmt.Errorf("%w: model_list is not a list", litellm.ErrInvalid), wantIs: litellm.ErrInvalid, wantCalls: 1},
		{name: "lock timeout", id: "openrouter/x", err: lockErr, wantIs: context.DeadlineExceeded, wantCalls: 1},
		{name: "the row's own write error", id: "openrouter/x", res: litellm.Result{Outcomes: []litellm.Outcome{{ID: "openrouter/x", Err: rowErr}}}, wantIs: rowErr, wantCalls: 1},
		{name: "the row's own write error, local model", id: "ollama/a:1", res: litellm.Result{Outcomes: []litellm.Outcome{{ID: "ollama/a:1", Err: rowErr}}}, wantIs: rowErr, wantCalls: 1},
		{name: "native model", id: "claude/sonnet", wantIs: ErrNotRoutable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls, warn := stubRoutes(t, tc.res, tc.err)
			restarts := fakeRestarter(t)
			cfg := profileCfg()

			owed, err := EnsureModelRoutes(context.Background(), nil, cfg, modelsByID(t, cfg, tc.id), false)
			WaitPendingRoutes()

			var re *RouteEnsureError
			if !errors.As(err, &re) {
				t.Fatalf("err = %v (%T), want a *RouteEnsureError", err, err)
			}
			if re.ModelID != tc.id {
				t.Errorf("ModelID = %q, want %q", re.ModelID, tc.id)
			}
			if !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.wantIs)
			}
			cause := tc.err
			if cause == nil {
				cause = tc.wantIs
			}
			if want := "LiteLLM route for " + tc.id + " not written: " + cause.Error(); err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
			if owed {
				t.Error("owed = true beside an error, want false")
			}
			if len(*calls) != tc.wantCalls {
				t.Errorf("writes = %d, want %d", len(*calls), tc.wantCalls)
			}
			if n := restarts.Load(); n != 0 {
				t.Errorf("restarts = %d, want 0: nothing was owed", n)
			}
			if warn.Len() != 0 {
				t.Errorf("output = %q, want none: the error is returned, not printed", warn.String())
			}
		})
	}
}

// TestEnsureModelRoutesCloudModelNotInTheRegistry pins the second way a
// model is not wt's to route: a cloud model the registry does not hold. It
// is refused before config.yaml is opened.
func TestEnsureModelRoutesCloudModelNotInTheRegistry(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	cfg := profileCfg()
	gone := config.Model{ID: "openrouter/gone", ProviderID: "openrouter", ModelName: "gone", Location: config.LocationCloud}

	owed, err := EnsureModelRoutes(context.Background(), nil, cfg, []config.Model{gone}, false)
	if owed || !errors.Is(err, ErrNotRoutable) {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (false, ErrNotRoutable)", owed, err)
	}
	if want := "LiteLLM route for openrouter/gone not written: wt has no LiteLLM route for this model"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
	if len(*calls) != 0 {
		t.Fatalf("writes = %+v, want none", *calls)
	}
}

// TestEnsureModelRoutesMissingConfigAgainstTheRealWriter runs the
// missing-config.yaml case through litellm.ApplyChange: the single-model
// check stays silent there and launches, and a profile launch must not.
func TestEnsureModelRoutesMissingConfigAgainstTheRealWriter(t *testing.T) {
	path, _, _ := realRoutes(t, "model_list: []\n")
	restarts := fakeRestarter(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cfg := profileCfg()

	owed, err := EnsureModelRoutes(context.Background(), nil, cfg, modelsByID(t, cfg, "openrouter/x"), false)
	WaitPendingRoutes()
	if owed || !errors.Is(err, litellm.ErrMissing) {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (false, an error wrapping litellm.ErrMissing)", owed, err)
	}
	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarts = %d, want 0", n)
	}
}

// TestEnsureModelRoutesSettlesWhatIsOwedOnError pins the choice that a
// failed ensure restarts once, asynchronously, for what is already in
// config.yaml, and hands back owed == false. Otherwise a route written
// before the failure stays unpublished: the next single-model launch of
// that model finds its row in place, restarts nothing, and the proxy
// answers "Invalid model name".
func TestEnsureModelRoutesSettlesWhatIsOwedOnError(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name   string
		owedIn bool
		// results holds one scripted write result per model, in order.
		results []litellm.Result
		errs    []error
		ids     []string
	}{
		{name: "owed by an earlier call", owedIn: true, ids: []string{"openrouter/x"}, results: []litellm.Result{{}}, errs: []error{boom}},
		{name: "owed by an earlier write of this call", ids: []string{"openrouter/x", "openrouter/y"}, results: []litellm.Result{wrote("openrouter/x"), {}}, errs: []error{nil, boom}},
		{name: "the failing write itself changed the file", ids: []string{"openrouter/x"}, results: []litellm.Result{{Changed: true, Outcomes: []litellm.Outcome{{ID: "ollama/b:1", Action: litellm.ActionAPIBaseSet}, {ID: "openrouter/x", Err: boom}}}}, errs: []error{nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRoutes(t, litellm.Result{}, nil)
			restarts := fakeRestarter(t)
			n := 0
			applyRoutes = func(*config.Config, litellm.Change, litellm.Options) (litellm.Result, error) {
				i := n
				n++
				return tc.results[i], tc.errs[i]
			}
			cfg := profileCfg()

			var out bytes.Buffer
			owed, err := EnsureModelRoutes(context.Background(), &out, cfg, modelsByID(t, cfg, tc.ids...), tc.owedIn)
			WaitPendingRoutes()

			var re *RouteEnsureError
			if !errors.As(err, &re) || !errors.Is(err, boom) {
				t.Fatalf("err = %v, want a *RouteEnsureError wrapping boom", err)
			}
			if want := tc.ids[len(tc.ids)-1]; re.ModelID != want {
				t.Errorf("ModelID = %q, want %q", re.ModelID, want)
			}
			if owed {
				t.Error("owed = true, want false: the call settled the restart itself")
			}
			if got := restarts.Load(); got != 1 {
				t.Errorf("restarts = %d, want exactly 1", got)
			}
		})
	}
}

// TestEnsureModelRoutesWriteOptions pins how each write is made: restart
// deferred, the config.yaml lock wait bounded, the row built here (no
// DeferMissingAdd, which is for the picker's non-blocking check), one write
// per distinct id, a cloud change that leaves an existing row alone and a
// local change that does not. An unbounded lock wait would hang a launch
// behind another wt process for as long as that process holds the file.
func TestEnsureModelRoutesWriteOptions(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	restarts := fakeRestarter(t)
	type write struct {
		id          string
		missingOnly bool
		opts        litellm.Options
		left        time.Duration
	}
	var writes []write
	applyRoutes = func(_ *config.Config, ch litellm.Change, o litellm.Options) (litellm.Result, error) {
		w := write{id: ch.Add[0].ID, missingOnly: ch.AddMissingOnly, opts: o}
		if o.Ctx != nil {
			if dl, ok := o.Ctx.Deadline(); ok {
				w.left = time.Until(dl)
			}
		}
		if len(ch.Add) != 1 || len(ch.Remove) != 0 || len(ch.RemoveFamilies) != 0 {
			t.Errorf("change = %+v, want one add and no removal", ch)
		}
		writes = append(writes, w)
		return litellm.Result{}, nil
	}
	cfg := profileCfg()

	owed, err := EnsureModelRoutes(context.Background(), nil, cfg, modelsByID(t, cfg, "openrouter/x", "mtplx/Y--Q35", "openrouter/x", "ollama/g:cloud"), true)
	WaitPendingRoutes()
	if err != nil || !owed {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (true, nil): owedIn is carried through writes that change nothing", owed, err)
	}
	if len(writes) != 3 {
		t.Fatalf("writes = %d, want 3: the repeated id is written once", len(writes))
	}
	wantIDs := []string{"openrouter/x", "mtplx/Y--Q35", "ollama/g:cloud"}
	wantMissingOnly := []bool{true, false, true}
	for i, w := range writes {
		if w.id != wantIDs[i] || w.missingOnly != wantMissingOnly[i] {
			t.Errorf("write %d = %s (AddMissingOnly %v), want %s (%v)", i, w.id, w.missingOnly, wantIDs[i], wantMissingOnly[i])
		}
		if !w.opts.NoRestart || w.opts.ForceRestart || w.opts.DeferMissingAdd {
			t.Errorf("write %d options = %+v, want NoRestart only", i, w.opts)
		}
		if w.left <= 0 || w.left > ensureRouteLockTimeout {
			t.Errorf("write %d lock wait = %v, want a deadline within %v", i, w.left, ensureRouteLockTimeout)
		}
	}
	if n := restarts.Load(); n != 0 {
		t.Errorf("restarts = %d, want 0 on success", n)
	}
}

// TestEnsureModelRoutesPrintsToTheCallersWriter pins where the lines go.
// The TUI runs the profile launch under the alt screen, where stderr is not
// visible, so it passes its own writer; a line that leaked to stderr there
// would be lost, and one that leaked from another operation would be
// reported as this launch's.
func TestEnsureModelRoutesPrintsToTheCallersWriter(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, nil)
	results := []litellm.Result{
		wrote("openrouter/x"),
		{Changed: true, Outcomes: []litellm.Outcome{{ID: "openrouter/y", Action: "routed"}, {ID: "ollama/b:1", Action: litellm.ActionAPIBaseSet}}},
		{Changed: true, Outcomes: []litellm.Outcome{{ID: "ollama/g:cloud", Action: "routed"}}},
		{Outcomes: []litellm.Outcome{{ID: "ollama/a:1", Action: "routed"}}},
	}
	n := 0
	applyRoutes = func(*config.Config, litellm.Change, litellm.Options) (litellm.Result, error) {
		n++
		return results[n-1], nil
	}
	cfg := profileCfg()

	var out bytes.Buffer
	owed, err := EnsureModelRoutes(context.Background(), &out, cfg, modelsByID(t, cfg, "openrouter/x", "openrouter/y", "ollama/g:cloud", "ollama/a:1"), false)
	if err != nil || !owed {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (true, nil)", owed, err)
	}
	want := "wt: LiteLLM route for openrouter/x updated\n" + // its own row was written
		"wt: LiteLLM route for ollama/b:1: api_base set\n" + // another row repaired by the second write
		"wt: LiteLLM config.yaml updated\n" // the third write changed the file, but no row: an enforced setting
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if warn.Len() != 0 {
		t.Fatalf("stderr got %q, want nothing: the caller passed a writer", warn.String())
	}
}

// TestRestartProxy pins the restart a profile launch waits for and can fail
// on. The asynchronous restart prints a failed command or a readiness
// timeout as a warning and the launch goes on; here both are returned, so
// the agent never starts against a proxy that does not know its routes.
func TestRestartProxy(t *testing.T) {
	notReady := errors.New("LiteLLM proxy not ready at http://localhost:4000/health/liveliness: context deadline exceeded")
	tests := []struct {
		name      string
		warnings  []string
		waitErr   error
		wantErr   string
		wantWaits int
	}{
		{name: "restarted and ready", wantWaits: 1},
		{name: "restart command failed", warnings: []string{"failed to restart LiteLLM proxy (exit status 1); restart it manually: launchctl kickstart"}, wantErr: "failed to restart LiteLLM proxy (exit status 1); restart it manually: launchctl kickstart"},
		{name: "readiness timeout", waitErr: notReady, wantErr: notReady.Error(), wantWaits: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, warn := stubRoutes(t, litellm.Result{}, nil)
			var restartCtx context.Context
			var waits []string
			pendingDuringWait := false
			restartProxy = func(ctx context.Context) []string {
				restartCtx = ctx
				return tc.warnings
			}
			waitProxy = func(_ context.Context, url string, d time.Duration) error {
				waits = append(waits, fmt.Sprintf("%s %s", url, d))
				pendingDuringWait = RoutesPending()
				return tc.waitErr
			}
			// Nothing was listening before: the asynchronous restart skips
			// the readiness wait then. This call must not.
			probeProxy = func(context.Context, string, time.Duration) bool { return false }
			cfg := profileCfg()

			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "sentinel"))
			cancel() // a caller that was cancelled cannot call the restart off
			err := RestartProxy(ctx, cfg)

			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("RestartProxy = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Fatalf("RestartProxy = %v, want %q", err, tc.wantErr)
			}
			if restartCtx == nil {
				t.Fatal("the restart never ran")
			}
			if restartCtx.Err() != nil || restartCtx.Value(ctxKey{}) != "sentinel" {
				t.Errorf("restart ctx: err %v value %v, want the caller's values without its cancellation", restartCtx.Err(), restartCtx.Value(ctxKey{}))
			}
			if len(waits) != tc.wantWaits {
				t.Fatalf("readiness waits = %v, want %d", waits, tc.wantWaits)
			}
			if tc.wantWaits == 1 {
				if want := proxyURL + " " + proxyReadyTimeout.String(); waits[0] != want {
					t.Errorf("readiness wait = %q, want %q", waits[0], want)
				}
				if !pendingDuringWait {
					t.Error("RoutesPending() = false during the restart: a process exit would not wait for it")
				}
			}
			if RoutesPending() {
				t.Error("RoutesPending() = true after RestartProxy returned")
			}
			if warn.Len() != 0 {
				t.Errorf("output = %q, want none: the failure is returned, not printed", warn.String())
			}
		})
	}
}

// TestRestartProxyWaitsForARestartInFlight pins that two restarts never
// overlap: a restart that started while another was running would kill the
// proxy the first had just brought up.
func TestRestartProxyWaitsForARestartInFlight(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	release := make(chan struct{})
	var running, overlapped atomic.Int32
	var count atomic.Int32
	restartProxy = func(context.Context) []string {
		if running.Add(1) > 1 {
			overlapped.Add(1)
		}
		if count.Add(1) == 1 {
			<-release // the first restart is still running
		}
		running.Add(-1)
		return nil
	}
	cfg := profileCfg()

	bounceRoutes(context.Background(), cfg) // an earlier operation's restart, in flight
	done := make(chan error, 1)
	go func() { done <- RestartProxy(context.Background(), cfg) }()

	select {
	case err := <-done:
		t.Fatalf("RestartProxy returned (%v) while another restart was in flight", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RestartProxy = %v", err)
	}
	if count.Load() != 2 || overlapped.Load() != 0 {
		t.Fatalf("restarts = %d, overlapping = %d, want 2 in sequence", count.Load(), overlapped.Load())
	}
}

// TestRestartProxyWithoutAURL pins the unconfigured case: with no [litellm]
// url there is no address to wait for, so nothing is restarted and the
// error says why.
func TestRestartProxyWithoutAURL(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	restarts := fakeRestarter(t)
	err := RestartProxy(context.Background(), routesCfg())
	if !errors.Is(err, config.ErrLitellmUnconfigured) {
		t.Fatalf("RestartProxy = %v, want an error wrapping config.ErrLitellmUnconfigured", err)
	}
	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarts = %d, want 0", n)
	}
}

// TestProbeProxy pins the probe every profile launch makes before it starts
// the agent, including a launch that restarted nothing. It needs a 200:
// litellm.Listening counts a timeout as an answer, and a launch into a
// proxy that does not answer fails on the agent's first request instead of
// here, with the reason.
func TestProbeProxy(t *testing.T) {
	down := errors.New("LiteLLM proxy not ready at http://localhost:4000/health/liveliness: context deadline exceeded")
	tests := []struct {
		name    string
		url     string
		waitErr error
		wantErr string
		wantIs  error
		waits   int
	}{
		{name: "answers", url: proxyURL, waits: 1},
		{name: "a trailing slash is trimmed", url: proxyURL + "/", waits: 1},
		{name: "does not answer", url: proxyURL, waitErr: down, wantErr: "the LiteLLM proxy does not answer at http://localhost:4000: " + down.Error(), wantIs: down, waits: 1},
		{name: "no url", url: "", wantErr: "the LiteLLM proxy cannot be probed: " + config.ErrLitellmUnconfigured.Error(), wantIs: config.ErrLitellmUnconfigured},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRoutes(t, litellm.Result{}, nil)
			restarts := fakeRestarter(t)
			var waits []string
			waitProxy = func(_ context.Context, url string, d time.Duration) error {
				waits = append(waits, fmt.Sprintf("%s %s", url, d))
				return tc.waitErr
			}
			cfg := routesCfg()
			cfg.SetLitellmForTest(config.LitellmState{URL: tc.url, APIKey: "sk-test-1234"})

			err := ProbeProxy(context.Background(), cfg)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("ProbeProxy = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Fatalf("ProbeProxy = %v, want %q", err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.wantIs)
			}
			if len(waits) != tc.waits {
				t.Fatalf("waits = %v, want %d", waits, tc.waits)
			}
			if tc.waits == 1 {
				if want := proxyURL + " " + proxyProbeTimeout.String(); waits[0] != want {
					t.Errorf("wait = %q, want %q", waits[0], want)
				}
			}
			if n := restarts.Load(); n != 0 {
				t.Errorf("restarts = %d, want 0: a probe restarts nothing", n)
			}
		})
	}
}

// TestProbeProxyAgainstARealListener runs the probe through the real
// litellm.WaitReady and a local test server, so the seam's stub is not the
// only thing that ever answers it: a 200 on /health/liveliness is "there".
func TestProbeProxyAgainstARealListener(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	waitProxy = litellm.WaitReady
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: srv.URL, APIKey: "sk-test-1234"})

	if err := ProbeProxy(context.Background(), cfg); err != nil {
		t.Fatalf("ProbeProxy = %v, want nil", err)
	}
	if !slices.Equal(paths, []string{"/health/liveliness"}) {
		t.Fatalf("requests = %v, want one to /health/liveliness", paths)
	}
}

// TestProfileRoutesRestartCounts pins the restart budget of a profile
// launch against the real config.yaml writer, with the route calls made in
// the order the launch makes them: the cloud slots' routes, then the local
// model's when it is already running, then one restart if one is owed.
// Zero restarts when every route exists, so launching the same profile twice
// does not drop the first session's in-flight requests; exactly one when
// several are missing, so the routes are live before the agent starts.
func TestProfileRoutesRestartCounts(t *testing.T) {
	tests := []struct {
		name  string
		cloud []string
		local []string
	}{
		{name: "several missing cloud routes", cloud: []string{"openrouter/x", "openrouter/y", "ollama/g:cloud"}},
		{name: "missing cloud routes and a running local model", cloud: []string{"openrouter/x", "openrouter/y"}, local: []string{"ollama/a:1"}},
		{name: "one slot model named twice", cloud: []string{"openrouter/x", "openrouter/x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, _, warn := realRoutes(t, "model_list: []\n")
			restarts := fakeRestarter(t)
			cfg := profileCfg()
			ctx := context.Background()

			launch := func() {
				t.Helper()
				owed, err := EnsureModelRoutes(ctx, nil, cfg, modelsByID(t, cfg, tc.cloud...), false)
				if err != nil {
					t.Fatalf("cloud routes: %v", err)
				}
				if len(tc.local) > 0 {
					if owed, err = EnsureModelRoutes(ctx, nil, cfg, modelsByID(t, cfg, tc.local...), owed); err != nil {
						t.Fatalf("local route: %v", err)
					}
				}
				if owed {
					if err := RestartProxy(ctx, cfg); err != nil {
						t.Fatalf("RestartProxy: %v", err)
					}
				}
			}

			launch()
			if n := restarts.Load(); n != 1 {
				t.Fatalf("first launch: restarts = %d, want exactly 1 (output %q)", n, warn.String())
			}
			want := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(tc.cloud), tc.local...))))
			if got := sortedRoutedIDs(t, path); !slices.Equal(got, want) {
				t.Fatalf("routed = %v, want %v", got, want)
			}

			warn.Reset()
			launch()
			if n := restarts.Load(); n != 1 {
				t.Fatalf("second launch: restarts = %d in all, want still 1: every route exists", n)
			}
			if warn.Len() != 0 {
				t.Fatalf("second launch printed %q, want nothing", warn.String())
			}
		})
	}
}
