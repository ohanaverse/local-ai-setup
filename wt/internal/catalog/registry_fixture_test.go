package catalog

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestRegistryFixtureLocalOverlays pins the shared contract fixture's #179
// Phase B rows (docs/contracts/registry.sample.toml, read by this test and by
// llmbench/tests/test_registry.py) against a real
// inventory round — a fake ollama daemon, a temp mtplx model directory, and
// failing /v1/models probes, never a real server. The "--"-style overlay matches its
// on-disk artifact through model_name, the provider/model_name-style one
// matches as before, and the overlay that is not on disk gets no row though
// the registry still parses. A change to how overlays match fails here: a
// registered model would lose its row in the picker, or gain a second one.
func TestRegistryFixtureLocalOverlays(t *testing.T) {
	var reg struct {
		Providers []config.Provider `toml:"providers"`
		Models    []config.Model    `toml:"models"`
	}
	if _, err := toml.DecodeFile("../../../docs/contracts/registry.sample.toml", &reg); err != nil {
		t.Fatal(err)
	}
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"contract-fixture:local"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer ollama.Close()
	// A server that answers every probe with an error, rather than a closed
	// server's freed port: another process could rebind that port before the
	// probe and answer it.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	mtplxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mtplxDir, "org--contract-fixture-dashed"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Providers: reg.Providers, Models: reg.Models}
	for i := range cfg.Providers {
		switch p := &cfg.Providers[i]; p.ID {
		case "ollama":
			p.Auth.BaseURL = ollama.URL
		case "mtplx":
			p.Auth.BaseURL, p.ModelDir = failing.URL, mtplxDir
		case "mlx_lm_server":
			p.Auth.BaseURL = failing.URL
		}
	}
	var local []config.Model
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err == nil && loc == config.LocationLocal {
			local = append(local, m)
		}
	}
	snap := localmodels.Inventory(cfg)
	rows := Build(Input{Config: cfg, Models: local, Inventory: &snap})
	want := []string{"ollama/contract-fixture:local", "mtplx/org--contract-fixture-dashed"}
	if got := rowIDs(rows); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v (the absent overlay and the stopped pairing get none)", got, want)
	}
	for _, r := range rows {
		if r.Discovered || r.Model.Family != "contract-fixture" {
			t.Errorf("%s = %+v, want the overlay's family, not a discovered row", r.Model.ID, r)
		}
	}
}
