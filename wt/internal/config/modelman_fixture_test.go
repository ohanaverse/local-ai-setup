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
// litellm_exposed and ready flags; every other field (disk_path,
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

	state, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error: %v", err)
	}

	// Check the new predicate cases:
	// - ollama/contract-fixture:subscription (flag+ready) → should be in state
	// - llamacpp/legacy-contract-fixture (flag+downloaded) → should be in state
	// - ollama/contract-fixture:local (flag=false) → NOT litellm_exposed
	// - ollama/contract-fixture:local-not-ready (flag=true, ready=false) → in state
	// - openrouter/contract-fixture:cloud (no flag) → NOT litellm_exposed
	// - openrouter/contract-fixture:cloud-exposed (flag=true, no ready) → in state

	expectedLitellmExposed := map[string]bool{
		"ollama/contract-fixture:subscription": true,
		"llamacpp/legacy-contract-fixture":   true,
	}

	for id, shouldBeExposed := range expectedLitellmExposed {
		st, ok := state[id]
		if !ok {
			t.Errorf("expected %q in modelman state, got missing", id)
			continue
		}
		if shouldBeExposed && !st.LitellmExposed {
			t.Errorf("expected %q to have litellm_exposed=true", id)
		}
	}

	// Verify local model with flag off
	st, ok := state["ollama/contract-fixture:local"]
	if !ok {
		t.Errorf("expected ollama/contract-fixture:local in state")
	} else if st.LitellmExposed {
		t.Errorf("expected ollama/contract-fixture:local to have litellm_exposed=false")
	}

	// Verify cloud model with no flag (defaults)
	st, ok = state["openrouter/contract-fixture:cloud"]
	if !ok {
		t.Errorf("expected openrouter/contract-fixture:cloud in state")
	} else if st.LitellmExposed {
		t.Errorf("expected openrouter/contract-fixture:cloud to have litellm_exposed=false")
	}

	// Verify the local-not-ready case: flag on, ready off
	st, ok = state["ollama/contract-fixture:local-not-ready"]
	if !ok {
		t.Errorf("expected ollama/contract-fixture:local-not-ready in state")
	} else if !st.LitellmExposed {
		t.Errorf("expected ollama/contract-fixture:local-not-ready to have litellm_exposed=true")
	} else if st.Ready {
		t.Errorf("expected ollama/contract-fixture:local-not-ready to have ready=false")
	}

	// Verify cloud-exposed case: flag on, no ready key (defaults to false)
	st, ok = state["openrouter/contract-fixture:cloud-exposed"]
	if !ok {
		t.Errorf("expected openrouter/contract-fixture:cloud-exposed in state")
	} else if !st.LitellmExposed {
		t.Errorf("expected openrouter/contract-fixture:cloud-exposed to have litellm_exposed=true")
	}
	// ready defaults to false for missing key
}