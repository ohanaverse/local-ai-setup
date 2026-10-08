package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadModelmanStateMatchesSharedFixture pins wt's read of the shared
// docs/contracts/modelman.sample.toml fixture. wt reads far less of it than
// modelman's Python test does: only the legacy [litellm] table is pinned
// here, by the same field names and values. The [model_state] rows and the
// top-level price_refresh_last_run the Python side asserts on are merely
// tolerated (the file must load with them present). A drift in the shared
// table would otherwise ship silently since each side's CI only runs its own
// language's tests.
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

	litellm, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error: %v", err)
	}

	// The fixture's [model_state] rows (ready/downloaded, running, the
	// legacy exposed keys) are deliberately NOT read: since #179 Phase B wt
	// takes local presence and running state from its live inventory, and
	// reads only the legacy [litellm] table (pinned by
	// TestModelmanStateReadsNoPerModelState). The file must still load with
	// them, and price_refresh_last_run, present.

	if litellm == nil {
		t.Fatal("fixture [litellm] table not decoded")
	}
	if !litellm.Enabled || litellm.URL != "http://localhost:4000" {
		t.Errorf("got litellm=%+v", litellm)
	}
	if litellm.APIKey != "sk-litellm-CONTRACT-FIXTURE-NOT-A-REAL-KEY" {
		t.Errorf("got api key %q", litellm.APIKey)
	}
}
