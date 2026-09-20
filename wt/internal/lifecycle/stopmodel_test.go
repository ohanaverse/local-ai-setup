package lifecycle

import (
	"context"
	"errors"
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

// TestStopModelOllamaSurfacesFailure verifies a failing `ollama stop` returns
// its output as the error, and a missing binary returns *BinaryMissingError,
// so the picker can print an honest "failed: ..." line instead of "done".
func TestStopModelOllamaSurfacesFailure(t *testing.T) {
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("model not found\n"), errors.New("exit 1")
	}
	err := stopModel(context.Background(), e, &config.Config{}, "ollama", "x")
	if err == nil || err.Error() != "model not found" {
		t.Fatalf("err = %v, want \"model not found\"", err)
	}

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
