package lifecycle

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestStopModelOllamaRunsOllamaStop verifies stopModel unloads exactly the
// named ollama model via `ollama stop <name>`, pinned to the registry origin
// with OLLAMA_HOST so the CLI stops the same daemon the re-probe queries (an
// inherited OLLAMA_HOST pointing elsewhere would stop a daemon wt never
// probes). Ollama is multi-tenant, so the post-exit picker must stop only the
// model the user ticked, never the daemon or the other loaded models.
func TestStopModelOllamaRunsOllamaStop(t *testing.T) {
	var ran, envs []string
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.runEnv = func(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		envs = append(envs, env...)
		return nil, nil
	}
	if err := stopModel(context.Background(), e, &config.Config{}, "ollama", "qwen3.8:27b-mlx"); err != nil {
		t.Fatalf("stopModel: %v", err)
	}
	if len(ran) != 1 || ran[0] != "/bin/ollama stop qwen3.8:27b-mlx" {
		t.Fatalf("ran = %v, want [/bin/ollama stop qwen3.8:27b-mlx]", ran)
	}
	if len(envs) != 1 || envs[0] != "OLLAMA_HOST="+config.OllamaBaseURL {
		t.Fatalf("env = %v, want [OLLAMA_HOST=%s]", envs, config.OllamaBaseURL)
	}
}

// psServing is a stub ollama daemon whose /api/ps lists the given loaded
// models; used so the post-failure re-probe never touches a real daemon.
// Built on the serveFree listener handoff like every server in this package.
func psServing(t *testing.T, loaded ...string) *httptest.Server {
	t.Helper()
	srv, _ := serveFree(t, psBodyHandler(loaded...))
	return srv
}

// psBodyHandler answers every request with an /api/ps body listing loaded.
func psBodyHandler(loaded ...string) http.Handler {
	body := `{"models":[`
	for i, n := range loaded {
		if i > 0 {
			body += ","
		}
		body += `{"name":"` + n + `"}`
	}
	body += `]}`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
}

// psFailing answers /api/ps with a server error: the deterministic stand-in
// for a re-probe that cannot get an answer (daemon gone). Closing a real
// server instead would leave a window in which another process takes the
// freed ephemeral port — the flake vector #131's listener handoff removed,
// since a stranger's JSON without a "models" key decodes to an empty loaded
// list and would fake a successful stop.
func psFailing(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := serveFree(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	return srv
}

// failingOllamaStop is an env whose `ollama stop` exits 1 with "model not found".
func failingOllamaStop() *env {
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.runEnv = func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte("model not found\n"), errors.New("exit 1")
	}
	return e
}

// TestStopModelOllamaAlreadyUnloadedIsSuccess verifies that when `ollama stop`
// exits non-zero but /api/ps shows the model is no longer loaded (daemon
// restart or eviction between the picker's probe and the stop), the stop is a
// success. The goal — the model is not loaded — is met, and a spurious
// "failed: model not found" reads as a broken provider CLI.
func TestStopModelOllamaAlreadyUnloadedIsSuccess(t *testing.T) {
	daemon := psServing(t) // nothing loaded
	err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x")
	if err != nil {
		t.Fatalf("err = %v, want nil: the model is already unloaded", err)
	}
}

// TestStopModelOllamaStillLoadedSurfacesFailure verifies a failed `ollama stop`
// is still an error when /api/ps shows the model loaded — including under
// ollama's implicit ":latest" tag — so a real failure is never masked.
func TestStopModelOllamaStillLoadedSurfacesFailure(t *testing.T) {
	for _, loaded := range []string{"x", "x:latest"} {
		daemon := psServing(t, loaded)
		err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x")
		if err == nil || err.Error() != "model not found" {
			t.Errorf("loaded=%q: err = %v, want ollama's own message", loaded, err)
		}
	}
}

// TestStopModelOllamaExitZeroButStillLoadedIsError verifies a clean `ollama
// stop` exit is never trusted by itself: when /api/ps still lists the model
// (a stop racing a re-load, or a CLI quirk reporting success wrongly), the
// stop is an error — the same state-not-exit-code standard omlx and mtplx
// hold even on exit 0, so the picker never prints "done" for a loaded model.
func TestStopModelOllamaExitZeroButStillLoadedIsError(t *testing.T) {
	daemon := psServing(t, "x")
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.runEnv = func(context.Context, []string, string, ...string) ([]byte, error) { return nil, nil }
	if err := stopModel(context.Background(), e, provCfg("ollama", daemon.URL), "ollama", "x"); err == nil {
		t.Fatal("stopModel returned nil while /api/ps still lists the model")
	}
}

// TestStopModelOllamaCancelledReturnsCancelWithoutProbe verifies the picker's
// Ctrl-C path surfaces as a cancellation, not a success: the re-probe never
// runs on a cancelled stop, where the model unloading during the probe's
// timeout window would turn "cancelled" into "done".
func TestStopModelOllamaCancelledReturnsCancelWithoutProbe(t *testing.T) {
	daemon := psServing(t) // nothing loaded: a probe would report success
	e := failingOllamaStop()
	ctx, cancel := context.WithCancel(context.Background())
	e.runEnv = func(_ context.Context, _ []string, _ string, _ ...string) ([]byte, error) {
		cancel() // the picker cancels the stop in flight
		return nil, errors.New("exit 1")
	}
	if err := stopModel(ctx, e, provCfg("ollama", daemon.URL), "ollama", "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestStopModelOllamaReprobeFailureKeepsOriginalError verifies that when the
// re-probe itself cannot get an answer (daemon gone), the original CLI message
// is returned rather than guessing success: with no evidence the model is
// unloaded, the stop is reported as it failed.
func TestStopModelOllamaReprobeFailureKeepsOriginalError(t *testing.T) {
	daemon := psFailing(t)
	err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x")
	if err == nil || err.Error() != "model not found" {
		t.Fatalf("err = %v, want ollama's own message", err)
	}
}

// TestStopModelOllamaMissingBinary verifies a missing binary returns
// *BinaryMissingError (no probe happens, so &config.Config{} is fine there).
func TestStopModelOllamaMissingBinary(t *testing.T) {
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "", errors.New("nope") }
	var bm *BinaryMissingError
	if err := stopModel(context.Background(), e, &config.Config{}, "ollama", "x"); !errors.As(err, &bm) {
		t.Fatalf("err = %v, want *BinaryMissingError", err)
	}
}

// TestStopModelRoutesToProviderBackend verifies dispatch: the picker's
// `stopModel` reaches the backend registered for each provider id (including
// the `omlx-6bit` alias). Body unchanged from the original delegation test.
func TestStopModelRoutesToProviderBackend(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{}, true, &calls)
	for _, provider := range []string{"omlx", "omlx-6bit", "mtplx"} {
		calls = nil
		if err := stopModel(context.Background(), e, &config.Config{}, provider, "m"); err != nil {
			t.Fatalf("%s: stopModel: %v", provider, err)
		}
		if len(calls) != 1 || calls[0] != "stopModel:m" {
			t.Errorf("%s: calls = %v, want [stopModel:m]", provider, calls)
		}
	}
}

// TestStopModelUnsupportedProvider verifies providers without a backend
// return *UnsupportedError rather than silently reporting success, so the
// picker never claims to have stopped a model it cannot control.
func TestStopModelUnsupportedProvider(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{}, true, &calls)
	var ue *UnsupportedError
	if err := stopModel(context.Background(), e, &config.Config{}, "mlx_lm_server", "m"); !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UnsupportedError", err)
	}
}

// TestCanStop verifies CanStop is true exactly for the families wt has a stop
// backend for (including the omlx-6bit alias) and false for mlx_lm_server and
// unknown ids, keeping the post-exit picker from offering unstoppable models.
func TestCanStop(t *testing.T) {
	for id, want := range map[string]bool{
		"ollama": true, "omlx": true, "omlx-6bit": true, "mtplx": true,
		"mlx_lm_server": false, "anthropic": false,
	} {
		if got := CanStop(id); got != want {
			t.Errorf("CanStop(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestStopModelOmlxRunsOmlxStopAndWaitsForPort verifies the omlx stopModel
// thunk actually reaches the daemon stop: it invokes `omlx stop` and returns
// only after the port closes. A thunk that returned nil without stopping would
// leave the post-exit picker printing "done" for a model still loaded.
func TestStopModelOmlxRunsOmlxStopAndWaitsForPort(t *testing.T) {
	srv, addr := serveFree(t, chatHandler())
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var ran []string
	e.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		srv.Close()
		return nil, nil
	}
	if err := stopModel(context.Background(), e, provCfg("omlx", "http://"+addr), "omlx", "m"); err != nil {
		t.Fatalf("stopModel: %v", err)
	}
	if len(ran) != 1 || ran[0] != "/bin/omlx stop" {
		t.Fatalf("ran = %v, want [/bin/omlx stop]", ran)
	}

	// The port never closes: the thunk must report it, not "done".
	_, addr2 := serveFree(t, chatHandler())
	e.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	e.stopTimeout = 60 * time.Millisecond
	if err := stopModel(context.Background(), e, provCfg("omlx", "http://"+addr2), "omlx", "m"); err == nil {
		t.Fatal("stopModel returned nil while the daemon still holds its port")
	}
}

// TestStopModelMtplxRunsMtplxStopAndWaitsForPort is the mtplx counterpart: the
// thunk must run `mtplx stop --port N ...` and confirm the port closed.
func TestStopModelMtplxRunsMtplxStopAndWaitsForPort(t *testing.T) {
	srv, addr := serveFree(t, chatHandler())
	_, portStr, _ := net.SplitHostPort(addr)
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/mtplx", nil }
	var ran []string
	e.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append([]string{name}, args...)
		srv.Close()
		return nil, nil
	}
	if err := stopModel(context.Background(), e, provCfg("mtplx", "http://"+addr+"/v1"), "mtplx", "m"); err != nil {
		t.Fatalf("stopModel: %v", err)
	}
	want := []string{"/bin/mtplx", "stop", "--port", portStr, "--grace-seconds", "10"}
	if !reflect.DeepEqual(ran, want) {
		t.Fatalf("ran = %v, want %v", ran, want)
	}
}
