package localgate

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func TestProviderOf(t *testing.T) {
	provider, name, ok := providerOf("ollama/qwen3.8:27b-mlx")
	if !ok || provider != "ollama" || name != "qwen3.8:27b-mlx" {
		t.Errorf("providerOf = (%q, %q, %v), want (ollama, qwen3.8:27b-mlx, true)", provider, name, ok)
	}
}

// TestProviderOfKeepsFirstSlashOnly asserts the marker schema's "split on
// the FIRST / only" rule — the model name itself may contain a "/" (e.g.
// an openrouter org/model spelling).
func TestProviderOfKeepsFirstSlashOnly(t *testing.T) {
	provider, name, ok := providerOf("openrouter/z-ai/glm-5.3-flash")
	if !ok || provider != "openrouter" || name != "z-ai/glm-5.3-flash" {
		t.Errorf("providerOf = (%q, %q, %v), want (openrouter, z-ai/glm-5.3-flash, true)", provider, name, ok)
	}
}

func TestProviderOfNoSlash(t *testing.T) {
	if _, _, ok := providerOf("no-slash-here"); ok {
		t.Error("expected ok=false for an id with no provider prefix")
	}
}

func TestAvailableOmlxProbesConfiguredURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	if !Available("omlx/some-model") {
		t.Error("expected omlx model to be available when the probe URL responds 200")
	}
}

func TestAvailableOmlxDownReportsUnavailable(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")() // nothing listens here
	if Available("omlx/some-model") {
		t.Error("expected omlx model to be unavailable when the probe URL is unreachable")
	}
}

func TestAvailableMlxLmServerProbesConfiguredURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer SetMlxLMServerProbeURLForTest(srv.URL)()

	if !Available("mlx_lm_server/target-repo") {
		t.Error("expected mlx_lm_server model to be available when the probe URL responds 200")
	}
}

// TestAvailableUnknownProviderIsUnavailable asserts that a marker naming a
// not-yet-wired-in provider (e.g. the future mtplx, issue #66) fails
// closed as "not running" rather than panicking or reporting healthy.
func TestAvailableUnknownProviderIsUnavailable(t *testing.T) {
	if Available("mtplx/some-model") {
		t.Error("expected an unknown provider to report unavailable")
	}
}

// TestAvailableOllamaDelegatesToOllamacheck mirrors
// internal/ollamacheck's own TestAvailable fixture: a fake `ollama` binary
// on PATH stands in for the real daemon.
func TestAvailableOllamaDelegatesToOllamacheck(t *testing.T) {
	tmpDir := t.TempDir()
	fakeOllama := filepath.Join(tmpDir, "ollama")
	script := "#!/bin/sh\necho \"NAME    ID    SIZE    MODIFIED\"\necho \"qwen3.8:27b-mlx   abc   5.0 GB   2 days ago\"\n"
	if err := exec.Command("sh", "-c", "cat > "+fakeOllama+" <<'EOF'\n"+script+"EOF\nchmod +x "+fakeOllama).Run(); err != nil {
		t.Fatalf("creating fake ollama: %v", err)
	}
	t.Setenv("PATH", tmpDir)

	if !Available("ollama/qwen3.8:27b-mlx") {
		t.Error("expected ollama/qwen3.8:27b-mlx to be available")
	}
	if Available("ollama/missing-model") {
		t.Error("expected ollama/missing-model to be unavailable")
	}
}

func TestNotRunningErrorMessage(t *testing.T) {
	err := &NotRunningError{ModelID: "ollama/qwen3.8:27b-mlx"}
	want := "local model \"ollama/qwen3.8:27b-mlx\" is not running — start it with `modelman start ollama/qwen3.8:27b-mlx`"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestResolveNoMarker(t *testing.T) {
	cfg := &config.Config{}
	id, err := Resolve(cfg)
	if err != nil || id != "" {
		t.Errorf("Resolve() = (%q, %v), want (\"\", nil)", id, err)
	}
}

func TestResolveMarkerVerified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	id, err := Resolve(cfg)
	if err != nil || id != "omlx/qwen3.8" {
		t.Errorf("Resolve() = (%q, %v), want (\"omlx/qwen3.8\", nil)", id, err)
	}
}

func TestResolveMarkerStale(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")()

	cfg := &config.Config{}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	id, err := Resolve(cfg)
	if id != "" {
		t.Errorf("Resolve() id = %q, want \"\"", id)
	}
	nre, ok := err.(*NotRunningError)
	if !ok {
		t.Fatalf("err = %v (%T), want *NotRunningError", err, err)
	}
	if nre.ModelID != "omlx/qwen3.8" {
		t.Errorf("NotRunningError.ModelID = %q, want omlx/qwen3.8", nre.ModelID)
	}
}
