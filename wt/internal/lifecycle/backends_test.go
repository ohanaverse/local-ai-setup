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
	"sync"
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
	err := ollamaBackend{}.start(context.Background(), e, provCfg("ollama", srv.URL), Target{"ollama", "gemma4:9b"}, func(s Stage) { stages = append(stages, s) })
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
	err := ollamaBackend{}.start(context.Background(), testEnv(), provCfg("ollama", url), Target{"ollama", "m"}, func(Stage) {})
	var dd *DaemonDownError
	if !errors.As(err, &dd) || dd.Origin != url {
		t.Errorf("err = %v, want *DaemonDownError with origin %s", err, url)
	}
}

// TestOllamaIsMultiTenant verifies ollama declares itself not single-model and
// its stop is a no-op, so replacement logic never tries to stop the daemon.
func TestOllamaIsMultiTenant(t *testing.T) {
	if (ollamaBackend{}).singleModel() {
		t.Error("ollama must not be single-model")
	}
	if err := (ollamaBackend{}).stop(context.Background(), testEnv(), &config.Config{}); err != nil {
		t.Errorf("stop = %v, want nil", err)
	}
}

// TestOmlxAlreadyUpSkipsStartAndWarmsBasename verifies that with the daemon
// answering, no `omlx start` is run, and the warmup names the model's last path
// segment (omlx serves directory basenames, the registry stores HF repo ids).
func TestOmlxAlreadyUpSkipsStartAndWarmsBasename(t *testing.T) {
	srv := newChatServer(t)
	e := testEnv()
	var ran [][]string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, append([]string{name}, args...))
		return nil, nil
	}
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var stages []Stage
	err := omlxBackend{}.start(context.Background(), e, provCfg("omlx", srv.URL), Target{"omlx", "mlx-community/Qwen3.8-27B-4bit"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Errorf("omlx start must not run when the daemon is up, ran %v", ran)
	}
	if srv.lastModel() != "Qwen3.8-27B-4bit" || !reflect.DeepEqual(stages, []Stage{StageWarming}) {
		t.Errorf("model=%q stages=%v", srv.lastModel(), stages)
	}
}

// TestOmlxStartsDaemonWhenDown verifies a down daemon triggers `omlx start`
// (stage starting), waits for the port, then warms — the port comes up when the
// fake `omlx start` starts a server on the configured address.
func TestOmlxStartsDaemonWhenDown(t *testing.T) {
	addr := freeAddr(t)
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var ran []string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name)
		ran = append(ran, args...)
		serveAt(t, addr, chatHandler())
		return nil, nil
	}
	var stages []Stage
	err := omlxBackend{}.start(context.Background(), e, provCfg("omlx", "http://"+addr), Target{"omlx", "Qwen-4bit"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ran, []string{"/bin/omlx", "start"}) || !reflect.DeepEqual(stages, []Stage{StageStarting, StageWarming}) {
		t.Errorf("ran=%v stages=%v", ran, stages)
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
	if err := (omlxBackend{}).start(context.Background(), e, provCfg("omlx", url), Target{"omlx", "m"}, func(Stage) {}); !errors.As(err, &bm) {
		t.Errorf("err = %v, want *BinaryMissingError", err)
	}

	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("license expired"), errors.New("exit 1")
	}
	err := (omlxBackend{}).start(context.Background(), e, provCfg("omlx", url), Target{"omlx", "m"}, func(Stage) {})
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

	addr2 := freeAddr(t)
	serveAt(t, addr2, chatHandler())
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	e.stopTimeout = 60 * time.Millisecond
	if err := (omlxBackend{}).stop(context.Background(), e, provCfg("omlx", "http://"+addr2)); err == nil || !containsFold(err.Error(), addr2) {
		t.Errorf("err = %v, want still-listening error naming %s", err, addr2)
	}
}

// TestDefaultEnvRegistersBackends verifies the production env wires the ollama
// and omlx backends (single-model: omlx yes, ollama no).
func TestDefaultEnvRegistersBackends(t *testing.T) {
	e := defaultEnv()
	if e.backends["ollama"] == nil || e.backends["omlx"] == nil {
		t.Fatalf("backends = %v", e.backends)
	}
	if e.backends["ollama"].singleModel() || !e.backends["omlx"].singleModel() {
		t.Error("ollama must be multi-tenant and omlx single-model")
	}
}
