package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestStopModelOllamaRunsOllamaStop verifies stopModel unloads exactly the
// named ollama model via `ollama stop <name>`. Ollama is multi-tenant, so the
// post-exit picker must stop only the model the user ticked, never the daemon
// or the other loaded models.
func TestStopModelOllamaRunsOllamaStop(t *testing.T) {
	var ran []string
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	if err := stopModel(context.Background(), e, &config.Config{}, "ollama", "qwen3.8:27b-mlx"); err != nil {
		t.Fatalf("stopModel: %v", err)
	}
	if len(ran) != 1 || ran[0] != "/bin/ollama stop qwen3.8:27b-mlx" {
		t.Fatalf("ran = %v, want [/bin/ollama stop qwen3.8:27b-mlx]", ran)
	}
}

// psServing is a stub ollama daemon whose /api/ps lists the given loaded
// models; used so the post-failure re-probe never touches a real daemon.
func psServing(t *testing.T, loaded ...string) *httptest.Server {
	t.Helper()
	body := `{"models":[`
	for i, n := range loaded {
		if i > 0 {
			body += ","
		}
		body += `{"name":"` + n + `"}`
	}
	body += `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// failingOllamaStop is an env whose `ollama stop` exits 1 with "model not found".
func failingOllamaStop() *env {
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.run = func(context.Context, string, ...string) ([]byte, error) {
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

// TestStopModelOllamaReprobeFailureKeepsOriginalError verifies that when the
// re-probe itself cannot get an answer (daemon gone), the original CLI message
// is returned rather than guessing success: with no evidence the model is
// unloaded, the stop is reported as it failed.
func TestStopModelOllamaReprobeFailureKeepsOriginalError(t *testing.T) {
	daemon := psServing(t)
	url := daemon.URL
	daemon.Close()
	err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", url), "ollama", "x")
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

// TestStopModelSingleModelDelegatesToStop verifies single-model providers
// (omlx, mtplx) stop through their existing whole-provider stop. The picker
// only lists a running model, which on these providers is the sole occupant,
// so stopping the provider is stopping that model.
func TestStopModelSingleModelDelegatesToStop(t *testing.T) {
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