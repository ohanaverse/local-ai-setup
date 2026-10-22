package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// errorSrv is a provider server that is listening and answers every request
// with a 500: an answer that settles nothing.
func errorSrv(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// TestStartedGoneDecidesPerFamily verifies what "gone" means after a start,
// family by family (#343): an mtplx whose port refuses or that no longer lists
// the model; an omlx that refuses or whose status shows the model unloaded; an
// ollama daemon that refuses — and nothing else. In particular an ollama model
// that was only unloaded is not gone (ollama loads a pulled model on request),
// a pool read through the fallback, which lists only what is loaded, settles nothing, and a server that answers
// with an error is not "gone": a start that worked must not be failed by a
// probe that could not tell.
func TestStartedGoneDecidesPerFamily(t *testing.T) {
	closed := "http://" + freeAddr(t)
	pool := func(f *fakePool) string { return f.serve(t) }
	for name, tc := range map[string]struct {
		provider, url, model string
		want                 string // a substring of why; "" = not gone
	}{
		"mtplx serving the model":        {"mtplx", openaiSrv(t, []string{"Y/Q35"}), "Y/Q35", ""},
		"mtplx port refuses":             {"mtplx", closed, "Y/Q35", "mtplx no longer answers at " + closed},
		"mtplx serving nothing":          {"mtplx", openaiSrv(t, nil), "Y/Q35", "is no longer serving it"},
		"mtplx serving another model":    {"mtplx", openaiSrv(t, []string{"Y/Q27"}), "Y/Q35", "is no longer serving it"},
		"mtplx answers with an error":    {"mtplx", errorSrv(t), "Y/Q35", ""},
		"omlx has it loaded":             {"omlx", pool(&fakePool{loaded: map[string]bool{"A": true}}), "A", ""},
		"omlx is loading it":             {"omlx", pool(&fakePool{loaded: map[string]bool{"A": false}, loading: map[string]bool{"A": true}}), "A", ""},
		"omlx unloaded it":               {"omlx", pool(&fakePool{loaded: map[string]bool{"A": false, "B": true}}), "A", "omlx no longer has it loaded"},
		"omlx-6bit unloaded it":          {"omlx-6bit", pool(&fakePool{loaded: map[string]bool{"A": false}}), "org/A", "omlx no longer has it loaded"},
		"omlx refuses":                   {"omlx", closed, "A", "omlx no longer answers at " + closed},
		"omlx will not say which":        {"omlx", pool(&fakePool{loaded: map[string]bool{"A": false, "B": true}, mgmtKey: "k"}), "A", ""},
		"omlx read through the fallback": {"omlx", pool(&fakePool{loaded: map[string]bool{"B": true}, alias: map[string]string{"B": "b-alias"}, mgmtKey: "k"}), "A", ""},
		"omlx answers with an error":     {"omlx", errorSrv(t), "A", ""},
		"ollama has it loaded":           {"ollama", ollamaSrv(t, []string{"a:1"}, []string{"a:1"}), "a:1", ""},
		"ollama unloaded it":             {"ollama", ollamaSrv(t, []string{"a:1"}, nil), "a:1", ""},
		"ollama refuses":                 {"ollama", closed, "a:1", "ollama no longer answers at " + closed},
		"ollama answers with an error":   {"ollama", errorSrv(t), "a:1", ""},
		"a provider wt cannot start":     {"mlx_lm_server", closed, "m", ""},
	} {
		t.Run(name, func(t *testing.T) {
			url := tc.url
			if tc.provider != "ollama" {
				url += "/v1"
			}
			got := testEnv().startedGone(context.Background(), provCfg(tc.provider, url), Target{ProviderID: tc.provider, ModelName: tc.model})
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Errorf("startedGone = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStartedGoneIsNotSettledByACancelledProbe verifies a probe that was cut
// short says nothing: Ctrl+C while the proxy wait ends cancels the context the
// check runs under, and a request that failed for that reason must not be read
// as a refused connection — it would report a serving model as stopped and
// remove its route.
func TestStartedGoneIsNotSettledByACancelledProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	url := ollamaSrv(t, []string{"a:1"}, nil)
	if got := testEnv().startedGone(ctx, provCfg("ollama", url), Target{ProviderID: "ollama", ModelName: "a:1"}); got != "" {
		t.Errorf("startedGone under a cancelled context = %q, want nothing settled", got)
	}
}

// twoMtplxYAML routes both mtplx siblings and an ollama model, each a row wt
// owns.
const twoMtplxYAML = `model_list:
  - model_name: mtplx/Y--Q35
    litellm_params:
      model: openai/Y/Q35
      api_base: http://localhost:8003/v1
      api_key: not-needed
    model_info:
      wt_managed: true
  - model_name: mtplx/Y--Q27
    litellm_params:
      model: openai/Y/Q27
      api_base: http://localhost:8003/v1
      api_key: not-needed
    model_info:
      wt_managed: true
  - model_name: ollama/a:1
    litellm_params:
      model: ollama_chat/a:1
      api_base: http://localhost:11434
    model_info:
      wt_managed: true
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`

// TestSettleStartRemovesOnlyTheRouteOfTheModelThatIsGone drives the public
// SettleStart against the real route writer (#343). A started model whose
// server is gone is a *StoppedError and loses its own route — the case where
// the stop removed the family's routes first and the start hook then wrote
// this one, which would otherwise leave a route to a dead port — with one
// proxy restart. Nothing else goes: an mtplx that answers with another model
// is another start's server, and that model's route is its own. A model that
// is still served is nil, with config.yaml and the proxy untouched. The
// restart is counted with no wait of the test's own: SettleStart has waited
// for it (#349).
func TestSettleStartRemovesOnlyTheRouteOfTheModelThatIsGone(t *testing.T) {
	q35 := Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"}
	ollama := ollamaSrv(t, []string{"a:1"}, nil)
	for name, tc := range map[string]struct {
		mtplxURL     string
		gone         bool
		wantRouted   []string
		wantRestarts int
	}{
		"port refuses":          {"http://" + freeAddr(t), true, []string{"mtplx/Y--Q27", "ollama/a:1"}, 1},
		"serving another model": {openaiSrv(t, []string{"Y/Q27"}), true, []string{"mtplx/Y--Q27", "ollama/a:1"}, 1},
		"still serving":         {openaiSrv(t, []string{"Y/Q35"}), false, []string{"mtplx/Y--Q35", "mtplx/Y--Q27", "ollama/a:1"}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			path, restarts, warn := realRoutes(t, twoMtplxYAML)
			err := SettleStart(context.Background(), nil, wrapCfg(t, ollama, tc.mtplxURL), q35)
			var stopped *StoppedError
			if errors.As(err, &stopped) != tc.gone {
				t.Fatalf("SettleStart = %v, want a *StoppedError: %v", err, tc.gone)
			}
			if got := routedIDs(t, path); !slices.Equal(got, tc.wantRouted) {
				t.Errorf("routed = %v, want %v (warn %q)", got, tc.wantRouted, warn.String())
			}
			if restarts() != tc.wantRestarts {
				t.Errorf("restarts = %d, want %d", restarts(), tc.wantRestarts)
			}
		})
	}
}

// TestSettleStartRemovesEveryRouteOfAPoolModel verifies the removal for a
// pool model that omlx no longer has loaded covers every id it is routed
// under — omlx and omlx-6bit are one server, and a row under each can name the
// same directory — and leaves a sibling that is still loaded routed.
func TestSettleStartRemovesEveryRouteOfAPoolModel(t *testing.T) {
	url := (&fakePool{loaded: map[string]bool{"A": false, "B": true}}).serve(t)
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: url + "/v1"}},
			{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: url + "/v1"}},
		},
		Models: []config.Model{
			{ID: "omlx/A", ProviderID: "omlx", ModelName: "A", Location: config.LocationLocal},
			{ID: "omlx-6bit/A", ProviderID: "omlx-6bit", ModelName: "A", Location: config.LocationLocal},
			{ID: "omlx/B", ProviderID: "omlx", ModelName: "B", Location: config.LocationLocal},
		},
	}
	row := func(id string) string {
		return "  - model_name: " + id + "\n    litellm_params:\n      model: openai/x\n      api_base: " + url + "/v1\n      api_key: not-needed\n    model_info:\n      wt_managed: true\n"
	}
	path, _, warn := realRoutes(t, "model_list:\n"+row("omlx/A")+row("omlx-6bit/A")+row("omlx/B"))
	err := SettleStart(context.Background(), nil, cfg, Target{ProviderID: "omlx", ModelName: "A", ModelID: "omlx/A"})
	var stopped *StoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("SettleStart = %v, want a *StoppedError", err)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"omlx/B"}) {
		t.Errorf("routed = %v, want [omlx/B] (warn %q)", got, warn.String())
	}
}

// TestSettleStartSendsItsOutputToTheCaller verifies the writer the model
// picker hands over: what the check prints goes to it, as Options.Out does
// for the start it follows, the warning of the proxy restart its route
// removal starts included. That one is written by a goroutine, and it is
// there when SettleStart returns, with no wait of the caller's: the picker
// reads the writer for the start's result straight away. Nothing is left for
// stderr, which the picker's alt screen hides.
func TestSettleStartSendsItsOutputToTheCaller(t *testing.T) {
	q35 := Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"}
	path, _, warn := realRoutes(t, twoMtplxYAML)
	t.Setenv("WT_LITELLM_RESTART_CMD", "exit 3")
	var out strings.Builder
	err := SettleStart(context.Background(), &out, wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), "http://"+freeAddr(t)), q35)
	if RoutesPending() {
		t.Error("SettleStart returned with the route removal's restart still running")
	}
	var stopped *StoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("SettleStart = %v, want a *StoppedError", err)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"mtplx/Y--Q27", "ollama/a:1"}) {
		t.Errorf("routed = %v, want the gone model's route removed", got)
	}
	if !strings.HasPrefix(out.String(), "wt: ") || !strings.Contains(out.String(), "restart") {
		t.Errorf("the caller's writer got %q, want the failed restart's warning", out.String())
	}
	if warn.Len() != 0 {
		t.Errorf("stderr got %q, want nothing: the caller owns the screen", warn.String())
	}
}

// TestSettleStartRemovesTheRouteUnderACancelledContext verifies the route
// removal does not inherit the caller's cancellation. Ctrl+C during the proxy
// wait cancels the non-TUI start's context before the check runs; the check
// still finds the server gone, and the write that follows must then wait for
// config.yaml's lock like any other. Under the done context it gave up at once when another wt held the
// lock — the stop that took the server down writes its own removal about
// then — and left a route to a port nothing listens on.
func TestSettleStartRemovesTheRouteUnderACancelledContext(t *testing.T) {
	q35 := Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"}
	path, _, warn := realRoutes(t, twoMtplxYAML)
	cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), "http://"+freeAddr(t))

	held, release, unlocked := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		unlocked <- litellm.WithLock(context.Background(), path, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	// Long enough for the write to have met the held lock at least once.
	time.AfterFunc(150*time.Millisecond, func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := SettleStart(ctx, nil, cfg, q35)
	if lerr := <-unlocked; lerr != nil {
		t.Fatalf("holding the lock: %v", lerr)
	}
	var stopped *StoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("SettleStart = %v, want a *StoppedError", err)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"mtplx/Y--Q27", "ollama/a:1"}) {
		t.Errorf("routed = %v, want the gone model's route removed (warn %q)", got, warn.String())
	}
	if warn.Len() != 0 {
		t.Errorf("stderr got %q, want no \"not updated\" warning", warn.String())
	}
}

// TestSettleStartChecksUnderACancelledContext verifies the check itself does
// not inherit the caller's cancellation, for the one probe that reads its
// context (ollama's). Both callers reach SettleStart with a done context — the
// non-TUI start after a Ctrl+C in the proxy wait, the model picker after a
// cancel that lost to a start that finished anyway — and each used to strip
// the cancellation itself (#349). A probe that was cancelled settles nothing,
// so with the cancellation left on, a daemon that is gone would be reported as
// running and its route kept.
func TestSettleStartChecksUnderACancelledContext(t *testing.T) {
	path, restarts, warn := realRoutes(t, twoMtplxYAML)
	cfg := wrapCfg(t, "http://"+freeAddr(t), openaiSrv(t, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := SettleStart(ctx, nil, cfg, Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"})
	var stopped *StoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("SettleStart under a cancelled context = %v, want a *StoppedError: the daemon refuses", err)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"mtplx/Y--Q35", "mtplx/Y--Q27"}) {
		t.Errorf("routed = %v, want the gone model's route removed (warn %q)", got, warn.String())
	}
	if restarts() != 1 {
		t.Errorf("restarts = %d, want 1, finished by the time SettleStart returned", restarts())
	}
}

// TestSettleStartWaitsForTheProxyBeforeItChecks verifies the order of the one
// call both callers make (#349): the wait for the restart the start left
// running comes first, and the check after it. The wait is where another
// terminal's `wt stop` lands (#343), so a check made before it would pass a
// server that is stopped a moment later, and the model would be handed to an
// agent with its server gone. Here the server goes while the restart is held:
// only a check made after the wait can see it.
func TestSettleStartWaitsForTheProxyBeforeItChecks(t *testing.T) {
	q35 := Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"}
	calls, _ := stubRoutes(t, litellm.Result{Changed: true}, nil)
	served := []string{"Y/Q35"}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		data := []map[string]any{}
		for _, id := range served {
			data = append(data, map[string]any{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), srv.URL)

	entered, held := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(held) })
	// Before stubRoutes' own wait for the restart, whatever this test did.
	t.Cleanup(release)
	var restarts atomic.Int32
	restartProxy = func(context.Context) []string {
		if restarts.Add(1) == 1 {
			close(entered)
			<-held
		}
		return nil
	}
	// The start's own route write, whose restart SettleStart has to wait for.
	routeAfterStart(context.Background(), cfg, q35, false)
	<-entered

	done := make(chan error, 1)
	go func() { done <- SettleStart(context.Background(), nil, cfg, q35) }()
	select {
	case err := <-done:
		t.Fatalf("SettleStart returned %v while the start's restart was still running", err)
	case <-time.After(100 * time.Millisecond):
	}
	mu.Lock()
	served = nil // another terminal's `wt stop`, during the wait
	mu.Unlock()
	release()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SettleStart never returned after the restart finished")
	}
	var stopped *StoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("SettleStart = %v, want a *StoppedError: the server stopped serving during the wait", err)
	}
	if n := len(*calls); n != 2 || !slices.Equal((*calls)[1].remove, []string{"mtplx/Y--Q35"}) {
		t.Fatalf("route writes = %+v, want the start's and then the removal of mtplx/Y--Q35", *calls)
	}
	if RoutesPending() || restarts.Load() != 2 {
		t.Errorf("pending = %v, restarts = %d; want the removal's restart finished when SettleStart returns", RoutesPending(), restarts.Load())
	}
}
