package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadModelmanStateMatchesSharedFixture guards wt's modelman.toml
// decoding against the shape modelman actually writes. The fixture at
// docs/contracts/modelman.sample.toml is also read by modelman's
// tests/contracts/test_modelman_fixture.py — a schema change not
// reflected in both tests fails both CI jobs in the same PR instead of
// wt's picker silently losing exposure state. wt only consumes the
// litellm_exposed flags; every other field (ready/downloaded, disk_path,
// size_bytes, families) is modelman-only and must stay ignorable here.
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

	exposed, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error: %v", err)
	}

	// Only the two litellm_exposed=true ids may appear — the unexposed
	// local model, the never-downloaded cloud model, and the legacy
	// downloaded-key entry must not leak into the set.
	if len(exposed) != 2 {
		t.Fatalf("got %d exposed ids, want 2: %v", len(exposed), exposed)
	}
	for _, id := range []string{"ollama/contract-fixture:subscription", "llamacpp/legacy-contract-fixture"} {
		if !exposed[id] {
			t.Errorf("expected %q in exposed set, got %v", id, exposed)
		}
	}
	if exposed["ollama/contract-fixture:local"] {
		t.Errorf("unexposed model must not be in the exposed set")
	}
	if exposed["openrouter/contract-fixture:cloud"] {
		t.Errorf("entry with no litellm_exposed key must default to unexposed")
	}
}