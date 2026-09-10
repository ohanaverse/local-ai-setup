package config

import (
	"os"
	"path/filepath"
	"testing"
)

// fixturePriceRefreshDate is the value of price_refresh_last_run in the
// shared modelman.toml contract fixture. Centralizing it makes the
// relationship between the fixture and the accessor tests explicit and
// avoids updating multiple literals when the fixture date changes.
const fixturePriceRefreshDate = "2026-09-14"

// TestLoadModelmanStateMatchesSharedFixture pins wt's read of the shared
// docs/contracts/modelman.sample.toml fixture to the same field names and
// values modelman's Python test asserts — a schema drift between the two
// readers would otherwise ship silently since each side's CI only runs its
// own language's tests.
func TestLoadModelmanStateMatchesSharedFixture(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")

	// ModelmanPath() resolves to $XDG_CONFIG_HOME/local-ai/modelman.toml,
	// so the fixture must be copied there — wt has no MODELMAN_STATE
	// override (a deliberate asymmetry: wt is a read-only consumer and
	// never needs to redirect the state file the way tests redirect the
	// registry).
	stateDir := filepath.Join(dir, "local-ai")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../docs/contracts/modelman.sample.toml")
	if err != nil {
		t.Fatalf("read shared fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "modelman.toml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	models, litellm, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error: %v", err)
	}

	if !models["ollama/contract-fixture:subscription"].Exposed {
		t.Error("expected ollama/contract-fixture:subscription to be exposed")
	}
	if !models["llamacpp/legacy-contract-fixture"].Exposed {
		t.Error("legacy litellm_exposed-only entry must still read as exposed")
	}
	if models["ollama/contract-fixture:local"].Exposed {
		t.Error("expected ollama/contract-fixture:local to be not exposed")
	}
	if models["openrouter/contract-fixture:cloud"].Exposed {
		t.Error("expected openrouter/contract-fixture:cloud to be not exposed")
	}

	if models["ollama/contract-fixture:local-not-ready"].Ready {
		t.Error("expected ollama/contract-fixture:local-not-ready to have ready=false")
	}

	if got := models["llamacpp/legacy-contract-fixture"].Ready; !got {
		t.Error("legacy `downloaded` entry must still read as ready")
	}

	if !litellm.Enabled || litellm.URL != "http://localhost:4000" {
		t.Errorf("got litellm=%+v", litellm)
	}
	if litellm.APIKey != "sk-litellm-CONTRACT-FIXTURE-NOT-A-REAL-KEY" {
		t.Errorf("got api key %q", litellm.APIKey)
	}

	// Global price-refresh date (issue #69): wt's stale-pricing notice
	// reads this top-level key; a decode regression fails both CI jobs.
	if v, ok := PriceRefreshLastRun(); !ok || v != fixturePriceRefreshDate {
		t.Errorf("PriceRefreshLastRun() = (%q, %v), want (%q, true)", v, ok, fixturePriceRefreshDate)
	}
}