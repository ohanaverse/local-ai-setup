package config

import (
	"reflect"
	"slices"
	"testing"
)

// These tests pin the provider tables config derives its behavior from, so
// they can move into internal/provider without changing what wt does.

// TestDiscoveredModelIDPinned pins the registry id wt gives a discovered model:
// omlx-6bit shares omlx's family and ids outside every family pass through.
func TestDiscoveredModelIDPinned(t *testing.T) {
	cases := map[string]string{
		"omlx":      "omlx/x",
		"omlx-6bit": "omlx/x",
		"mtplx":     "mtplx/x",
		"llamacpp":  "llamacpp/x", // ids outside every family pass through
	}
	for id, want := range cases {
		if got := DiscoveredModelID(id, "x"); got != want {
			t.Errorf("DiscoveredModelID(%q, x) = %q, want %q", id, got, want)
		}
	}
}

// TestSeedableProviderIDsPinned pins which providers `wt model init` can seed,
// and their order (local first, then cloud).
func TestSeedableProviderIDsPinned(t *testing.T) {
	want := []string{"ollama", "omlx", "mlx_lm_server", "mtplx", "openrouter"}
	if got := SeedableProviderIDs(); !slices.Equal(got, want) {
		t.Errorf("SeedableProviderIDs() = %v, want %v", got, want)
	}
}

// TestSeedRowsPinned pins the exact default registry rows wt seeds, since they
// are written into users' registry.toml and a contract fixture reads them.
func TestSeedRowsPinned(t *testing.T) {
	local := map[string]map[string]any{
		"ollama": {
			"id": "ollama", "name": "Ollama", "location": "local",
			"protocols": []string{"anthropic", "openai-chat"},
			"auth":      map[string]any{"type": "none", "base_url": "http://localhost:11434"},
		},
		"omlx": {
			"id": "omlx", "name": "oMLX", "location": "local",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8000"},
		},
		"mlx_lm_server": {
			"id": "mlx_lm_server", "name": "mlx-lm server (target+draft)", "location": "local",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8001/v1"},
		},
		"mtplx": {
			"id": "mtplx", "name": "MTPLX", "location": "local", "model_dir": "~/.mtplx/models",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8003/v1"},
		},
	}
	for id, want := range local {
		if got := defaultProviderRow(id); !reflect.DeepEqual(got, want) {
			t.Errorf("defaultProviderRow(%q) = %#v, want %#v", id, got, want)
		}
	}
	for _, id := range []string{"omlx-6bit", "llamacpp", "bogus"} {
		if got := defaultProviderRow(id); got != nil {
			t.Errorf("defaultProviderRow(%q) = %#v, want nil", id, got)
		}
	}
	wantCloud := map[string]any{
		"id": "openrouter", "name": "OpenRouter", "location": "cloud",
		"auth": map[string]any{"type": "api_key", "secret_ref": "OPENROUTER_API_KEY", "base_url": "https://openrouter.ai/api/v1"},
	}
	if got := cloudProviderRow("openrouter"); !reflect.DeepEqual(got, wantCloud) {
		t.Errorf("cloudProviderRow(openrouter) = %#v, want %#v", got, wantCloud)
	}
}
