package config

import (
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
	want := []string{"ollama", "omlx", "mlx_lm_server", "mtplx", "rec", "openrouter"}
	if got := SeedableProviderIDs(); !slices.Equal(got, want) {
		t.Errorf("SeedableProviderIDs() = %v, want %v", got, want)
	}
}

// TestProviderOriginPortFillIn pins when wt fills in the port it hands a
// server: only for the provider wt starts (mtplx), an http url, and a host
// that server answers on. Everything else, including a provider id the table
// does not know, is left as the registry wrote it.
func TestProviderOriginPortFillIn(t *testing.T) {
	cases := []struct {
		id, baseURL, want string
	}{
		{"mtplx", "http://localhost/v1", "http://localhost:8003"},
		{"mtplx", "http://127.0.0.1:9000/v1", "http://127.0.0.1:9000"},
		{"mtplx", "https://localhost/v1", "https://localhost"},
		{"mtplx", "http://example.com/v1", "http://example.com"},
		{"omlx", "http://localhost/v1", "http://localhost"},
		{"omlx-6bit", "http://localhost/v1", "http://localhost"},
		{"acme", "http://localhost/v1", "http://localhost"},
		{"", "http://localhost", "http://localhost"},
	}
	for _, c := range cases {
		p := Provider{ID: c.id, Auth: AuthConfig{BaseURL: c.baseURL}}
		if got := p.Origin(); got != c.want {
			t.Errorf("Provider{%q, %q}.Origin() = %q, want %q", c.id, c.baseURL, got, c.want)
		}
	}
}
