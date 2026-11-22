package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// storeOnYAML is a config.yaml with database models on (#598) and no route.
const storeOnYAML = `model_list: []
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
general_settings:
  cancel_on_disconnect: true
  store_model_in_db: true
`

// TestStartPrintsTheDatabaseFallbackLine pins that a start's route hook
// passes the proxy pair's model API to the write (applyChangeWithAdmin, the
// production applyRoutes) and prints what the write warns about. Here the
// proxy lists its database but refuses /model/new with a 503, so the route
// goes to config.yaml and the write says so in one line: before #598's Task 6
// the hook dropped Result.Warnings, and a start whose route silently left the
// database path (costing a restart per start) said nothing.
func TestStartPrintsTheDatabaseFallbackLine(t *testing.T) {
	path, restarts, warn := realRoutes(t, storeOnYAML)
	applyRoutes = applyChangeWithAdmin // realRoutes' cleanup restores the seam
	var posts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/model/info":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case "/model/new":
			posts.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(proxy.Close)
	var calls []string
	swapBackend(t, "ollama", &fakeBackend{calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), openaiSrv(t, nil))
	cfg.SetLitellmForTest(config.LitellmState{URL: proxy.URL, APIKey: "sk-test-secret"})

	if err := startFull(context.Background(), fullFamilies, cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, Options{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	WaitPendingRoutes()
	if posts.Load() == 0 {
		t.Fatal("no /model/new: the route hook did not pass the model API client")
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"ollama/a:1"}) {
		t.Fatalf("routed = %v, want the config.yaml fallback row", got)
	}
	if restarts() != 1 {
		t.Fatalf("restarts = %d, want 1 (the fallback wrote config.yaml)", restarts())
	}
	var fallback []string
	for _, l := range strings.Split(warn.String(), "\n") {
		if strings.Contains(l, "database routes unavailable") {
			fallback = append(fallback, l)
		}
	}
	if len(fallback) != 1 || !strings.HasPrefix(fallback[0], "wt: ") || !strings.HasSuffix(fallback[0], "; used config.yaml") {
		t.Fatalf("output %q, want one wt: line ending in '; used config.yaml'", warn.String())
	}
	if strings.Contains(warn.String(), "sk-test-secret") {
		t.Fatalf("output shows the key: %q", warn.String())
	}
}

// TestApplyChangeWithAdminWithoutAPairIsTheFilePath pins that a host with no
// proxy pair (no url or no key) keeps every route in config.yaml exactly as
// before #598: applyChangeWithAdmin passes no model API client then.
func TestApplyChangeWithAdminWithoutAPairIsTheFilePath(t *testing.T) {
	path, _, _ := realRoutes(t, storeOnYAML)
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, nil))
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://127.0.0.1:1"}) // no key: no client
	i := config.IndexModelByID(cfg.Models, "ollama/a:1")
	res, err := applyChangeWithAdmin(cfg, litellm.Change{Add: []config.Model{cfg.Models[i]}}, litellm.Options{NoRestart: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Via != "file" || !slices.Equal(routedIDs(t, path), []string{"ollama/a:1"}) {
		t.Fatalf("via %q routed %v, want the file path", res.Via, routedIDs(t, path))
	}
}

// TestProxyRestartsRunOneAtATime pins that this process never restarts the
// proxy twice at once (#598 review): the model server's start-up route sync
// hands its restart to BounceProxyTo while a route job may be restarting
// (bounceProxyAsync) or a slot-profile launch running RestartProxyTo. Two
// overlapping restarts kill each other's drain and leave the proxy's state
// to whichever launchctl call ran last.
func TestProxyRestartsRunOneAtATime(t *testing.T) {
	var running, most atomic.Int32
	oldRestart, oldProbe, oldWait := restartProxy, probeProxy, waitProxy
	restartProxy = func(context.Context) []string {
		n := running.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return nil
	}
	probeProxy = func(context.Context, string, time.Duration) bool { return false }
	waitProxy = func(context.Context, string, time.Duration) error { return nil }
	t.Cleanup(func() {
		WaitPendingRoutes()
		restartProxy, probeProxy, waitProxy = oldRestart, oldProbe, oldWait
		setLastRestartErr(nil)
	})
	cfg := profileCfg()
	var out strings.Builder
	BounceProxyTo(context.Background(), &out, cfg)
	bounceProxyAsync(context.Background(), cfg)
	BounceProxyTo(context.Background(), &out, cfg)
	if err := RestartProxyTo(context.Background(), &out, cfg); err != nil {
		t.Fatal(err)
	}
	WaitPendingRoutes()
	if most.Load() != 1 {
		t.Fatalf("at most %d restarts ran at once, want 1", most.Load())
	}
}
