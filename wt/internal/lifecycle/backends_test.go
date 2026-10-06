package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func provCfg(id, baseURL string) *config.Config {
	return &config.Config{Providers: []config.Provider{{ID: id, Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: baseURL}}}}
}

// chatServer answers every path: GET -> {}, POST chat -> a chat.completion. It
// records POST bodies.
type chatServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newChatServer(t *testing.T) *chatServer {
	t.Helper()
	cs := &chatServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			cs.mu.Lock()
			cs.bodies = append(cs.bodies, m)
			cs.mu.Unlock()
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"models":[]}`))
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (cs *chatServer) lastModel() string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.bodies) == 0 {
		return ""
	}
	s, _ := cs.bodies[len(cs.bodies)-1]["model"].(string)
	return s
}

// freeAddr reserves then releases a local port so a test can start a server on
// it later (modelling "daemon down, then `omlx start` brings it up").
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func serveAt(t *testing.T, addr string, h http.Handler) *httptest.Server {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return serveOn(t, l, h)
}

func chatHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
}

// TestOllamaStartWarmsModel verifies an ollama start checks the daemon, then
// sends a 1-token chat naming the model (which loads it) and reports only the
// warming stage — there is no process to start.
func TestOllamaStartWarmsModel(t *testing.T) {
	srv := newChatServer(t)
	e := testEnv()
	var stages []Stage
	err := ollamaBackend{}.start(context.Background(), e, provCfg("ollama", srv.URL), Target{ProviderID: "ollama", ModelName: "gemma4:9b"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if srv.lastModel() != "gemma4:9b" || !reflect.DeepEqual(stages, []Stage{StageWarming}) {
		t.Errorf("model=%q stages=%v", srv.lastModel(), stages)
	}
}

// TestOllamaDaemonDown verifies a dead daemon yields *DaemonDownError (with the
// origin) and no chat attempt — wt reports instead of kickstarting launchd.
func TestOllamaDaemonDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	err := ollamaBackend{}.start(context.Background(), testEnv(), provCfg("ollama", url), Target{ProviderID: "ollama", ModelName: "m"}, func(Stage) {})
	var dd *DaemonDownError
	if !errors.As(err, &dd) || dd.Origin != url {
		t.Errorf("err = %v, want *DaemonDownError with origin %s", err, url)
	}
}

// TestOllamaIsMultiTenant verifies ollama declares itself not single-model and
// its stop is a no-op, so replacement logic never tries to stop the daemon.
func TestOllamaIsMultiTenant(t *testing.T) {
	if (ollamaBackend{}).tenancy() != Shared {
		t.Error("ollama must not be single-model")
	}
	if err := (ollamaBackend{}).stop(context.Background(), testEnv(), &config.Config{}); err != nil {
		t.Errorf("stop = %v, want nil", err)
	}
}

// TestOmlxAlreadyUpSkipsStartAndLoadsBasename verifies that with the daemon
// answering, no `omlx start` is run, and the load names the model's last path
// segment (omlx serves directory basenames, the registry stores HF repo ids):
// a load sent under the repo id is a 404 and the model never starts.
func TestOmlxAlreadyUpSkipsStartAndLoadsBasename(t *testing.T) {
	fp := &fakePool{loaded: map[string]bool{"Qwen3.8-27B-4bit": false}}
	url := fp.serve(t)
	e := testEnv()
	var ran [][]string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, append([]string{name}, args...))
		return nil, nil
	}
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var stages []Stage
	err := omlxBackend{}.start(context.Background(), e, provCfg("omlx", url), Target{ProviderID: "omlx", ModelName: "mlx-community/Qwen3.8-27B-4bit"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Errorf("omlx start must not run when the daemon is up, ran %v", ran)
	}
	if !reflect.DeepEqual(fp.loads, []string{"Qwen3.8-27B-4bit"}) || !reflect.DeepEqual(stages, []Stage{StageWarming}) {
		t.Errorf("loads=%v stages=%v", fp.loads, stages)
	}
}

// TestOmlxStartsDaemonWhenDown verifies a down daemon triggers `omlx start`
// (stage starting), waits for the port, then loads the model — the port comes
// up when the fake `omlx start` starts a pool on the configured address.
func TestOmlxStartsDaemonWhenDown(t *testing.T) {
	addr := freeAddr(t)
	fp := &fakePool{loaded: map[string]bool{"Qwen-4bit": false}}
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var ran []string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name)
		ran = append(ran, args...)
		serveAt(t, addr, fp.handler())
		return nil, nil
	}
	var stages []Stage
	err := omlxBackend{}.start(context.Background(), e, provCfg("omlx", "http://"+addr), Target{ProviderID: "omlx", ModelName: "Qwen-4bit"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ran, []string{"/bin/omlx", "start"}) || !reflect.DeepEqual(stages, []Stage{StageStarting, StageWarming}) {
		t.Errorf("ran=%v stages=%v", ran, stages)
	}
	if !fp.isLoaded("Qwen-4bit") {
		t.Errorf("loads = %v: the model was not loaded into the daemon that came up", fp.loads)
	}
}

// TestOmlxMissingBinaryAndNeverComesUp verifies the two failure shapes: no omlx
// binary -> *BinaryMissingError; `omlx start` that never brings the port up ->
// an error that includes the command's output so the user can see why.
func TestOmlxMissingBinaryAndNeverComesUp(t *testing.T) {
	url := "http://" + freeAddr(t)
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	var bm *BinaryMissingError
	if err := (omlxBackend{}).start(context.Background(), e, provCfg("omlx", url), Target{ProviderID: "omlx", ModelName: "m"}, func(Stage) {}); !errors.As(err, &bm) {
		t.Errorf("err = %v, want *BinaryMissingError", err)
	}

	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("license expired"), errors.New("exit 1")
	}
	err := (omlxBackend{}).start(context.Background(), e, provCfg("omlx", url), Target{ProviderID: "omlx", ModelName: "m"}, func(Stage) {})
	if err == nil || !containsFold(err.Error(), "license expired") {
		t.Errorf("err = %v, want it to include the omlx start output", err)
	}
}

// TestOmlxStopWaitsForPortClose verifies stop runs `omlx stop` and returns nil
// once the port closes, and returns an error naming the origin when it does
// not — a replacement must not start while the old daemon still holds the port.
func TestOmlxStopWaitsForPortClose(t *testing.T) {
	srv, addr := serveFree(t, chatHandler())
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var ran []string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, args...)
		srv.Close()
		return nil, nil
	}
	if err := (omlxBackend{}).stop(context.Background(), e, provCfg("omlx", "http://"+addr)); err != nil {
		logPortHolder(t, addr)
		t.Fatalf("stop: %v", err)
	}
	if !reflect.DeepEqual(ran, []string{"stop"}) {
		t.Errorf("ran = %v", ran)
	}

	_, addr2 := serveFree(t, chatHandler())
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	e.stopTimeout = 60 * time.Millisecond
	if err := (omlxBackend{}).stop(context.Background(), e, provCfg("omlx", "http://"+addr2)); err == nil || !containsFold(err.Error(), addr2) {
		t.Errorf("err = %v, want still-listening error naming %s", err, addr2)
	}
}

// TestDefaultEnvRegistersBackends verifies the production env wires the ollama
// and omlx backends with their tenancy (ollama shared, omlx a pool): a start
// on either must load beside what is running, never stop it first.
func TestDefaultEnvRegistersBackends(t *testing.T) {
	e := defaultEnv()
	if e.backends["ollama"] == nil || e.backends["omlx"] == nil {
		t.Fatalf("backends = %v", e.backends)
	}
	if e.backends["ollama"].tenancy() != Shared || e.backends["omlx"].tenancy() != Pool {
		t.Error("ollama must be multi-tenant and omlx a pool")
	}
}

// keyedOmlx is an omlx started with --api-key: every /v1 request without the
// key is refused 401, as omlx 0.7.0 does (#256) — the chat completion `wt warm`
// sends and the POST /v1/models/{id}/load a start sends alike. It counts POSTs.
type keyedOmlx struct {
	*httptest.Server
	posts atomic.Int32
}

func newKeyedOmlx(t *testing.T, key string) *keyedOmlx {
	t.Helper()
	ko := &keyedOmlx{}
	ko.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			ko.posts.Add(1)
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, `{"error":{"message":"API key required","type":"authentication_error"}}`, http.StatusUnauthorized)
			return
		}
		chatHandler().ServeHTTP(w, r)
	}))
	t.Cleanup(ko.Close)
	return ko
}

func keyedProvCfg(baseURL, secretRef string) *config.Config {
	return &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "api_key", BaseURL: baseURL, SecretRef: secretRef}}}}
}

// TestOmlxStartSendsRegistryKey pins #256: an omlx with an API key refuses a
// keyless load, so the start must carry the key the registry's omlx provider
// names (auth.secret_ref). The 401 on the liveness probe still
// counts as "up", so no `omlx start` runs.
func TestOmlxStartSendsRegistryKey(t *testing.T) {
	srv := newKeyedOmlx(t, "sk-omlx")
	t.Setenv("WT_TEST_OMLX_KEY", "sk-omlx")
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("ran %s %v: the daemon is up", name, args)
		return nil, nil
	}
	err := omlxBackend{}.start(context.Background(), e, keyedProvCfg(srv.URL, "WT_TEST_OMLX_KEY"), Target{ProviderID: "omlx", ModelName: "org/Qwen-4bit"}, func(Stage) {})
	if err != nil {
		t.Fatalf("start on a keyed omlx with the registry's key: %v", err)
	}
}

// TestOmlxStartWithoutKeyFailsAtOnce: a refused key does not heal by waiting,
// so the start must fail on the first 401 to its load with an error that says
// what to set, not poll the whole warmup budget (ten minutes in production)
// and report a timeout.
func TestOmlxStartWithoutKeyFailsAtOnce(t *testing.T) {
	srv := newKeyedOmlx(t, "sk-omlx")
	e := testEnv()
	e.warmupTimeout = 30 * time.Second
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	began := time.Now()
	err := omlxBackend{}.start(context.Background(), e, provCfg("omlx", srv.URL), Target{ProviderID: "omlx", ModelName: "Qwen-4bit"}, func(Stage) {})
	var kr *KeyRefusedError
	if !errors.As(err, &kr) {
		t.Fatalf("err = %v, want *KeyRefusedError", err)
	}
	if kr.KeySent {
		t.Error("KeySent = true for a provider with no secret_ref")
	}
	if !strings.Contains(err.Error(), "auth.secret_ref") || !strings.Contains(err.Error(), "API key required") {
		t.Errorf("err = %q, want the server's reason and the setting to change", err)
	}
	if n := srv.posts.Load(); n != 1 {
		t.Errorf("load requests = %d, want 1: a 401 is final", n)
	}
	if d := time.Since(began); d > 5*time.Second {
		t.Errorf("took %v: the start waited instead of failing on the refusal", d)
	}
}

// TestOmlxStartWithWrongKeySaysTheKeyWasRefused: the registry names a key and
// the server refuses it — a different fix (the key is wrong) from the one
// above (no key is set), so the error says which.
func TestOmlxStartWithWrongKeySaysTheKeyWasRefused(t *testing.T) {
	srv := newKeyedOmlx(t, "sk-omlx")
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	err := omlxBackend{}.start(context.Background(), e, keyedProvCfg(srv.URL, "sk-stale"), Target{ProviderID: "omlx", ModelName: "Qwen-4bit"}, func(Stage) {})
	var kr *KeyRefusedError
	if !errors.As(err, &kr) || !kr.KeySent {
		t.Fatalf("err = %v, want *KeyRefusedError with KeySent", err)
	}
}

// TestWarmLoadsAModelIntoARunningOmlx covers the engine behind `wt warm`,
// which modelman calls when its own keyless warmup is refused: the request
// names the directory basename and carries the registry's key; nothing is
// started or stopped. Any provider but omlx is refused — the others take no
// key, so their callers have nothing to ask wt for.
func TestWarmLoadsAModelIntoARunningOmlx(t *testing.T) {
	srv := newKeyedOmlx(t, "sk-omlx")
	e := testEnv()
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("ran %s %v: warm starts nothing", name, args)
		return nil, nil
	}
	if err := warm(context.Background(), e, keyedProvCfg(srv.URL, "sk-omlx"), "omlx-6bit", "org/Qwen-6bit"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if n := srv.posts.Load(); n != 1 {
		t.Errorf("chat requests = %d, want 1", n)
	}
	var ue *UnsupportedError
	if err := warm(context.Background(), e, provCfg("mtplx", srv.URL), "mtplx", "m"); !errors.As(err, &ue) {
		t.Errorf("warm mtplx: err = %v, want *UnsupportedError", err)
	}
}
