package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadModelExposureAcrossNativeLocalCloud asserts the end-to-end wiring
// of Load() and deriveNative across the three location classes (#179
// configured-is-exposed): native, local and cloud models whose provider
// resolves are all in the catalog. No stored flag gates catalog membership:
// a regression that brought one back would hide registered models from every
// picker. What a local row can do is decided downstream by the live
// inventory (internal/localmodels) through internal/catalog's row rules.
func TestLoadModelExposureAcrossNativeLocalCloud(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")

	writeRegistry(t, dir, `
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
auth = { type = "none", base_url = "http://localhost:11434" }

[[providers]]
id = "agy"
name = "Antigravity"
location = "cloud"
auth = { type = "native" }

[[models]]
id = "ollama/exposed"
family = "exposed"
provider_id = "ollama"
model_name = "exposed"
location = "local"
tags = ["code"]

[[models]]
id = "ollama/unexposed"
family = "unexposed"
provider_id = "ollama"
model_name = "unexposed"
location = "local"
tags = ["code"]

[[models]]
id = "agy/native"
family = "agy"
provider_id = "agy"
model_name = "native"
location = "cloud"
tags = ["code"]
`)

	// modelman's leftover [model_state] flags, on disk: they must change
	// nothing here (see the test comment above).
	must(t, filepath.Join(dir, "local-ai/modelman.toml"), `
[model_state]

[model_state."ollama/exposed"]
exposed = true
ready = true

[model_state."ollama/unexposed"]
exposed = false
ready = false

[model_state."agy/native"]
exposed = false
ready = false
`)

	cfgDir := Dir()
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	byID := map[string]Model{}
	for _, m := range cfg.Models {
		byID[m.ID] = m
	}

	if !cfg.InCatalog(byID["agy/native"]) {
		t.Errorf("agy/native (native provider) must always be in the catalog")
	}
	if !cfg.InCatalog(byID["ollama/exposed"]) {
		t.Errorf("ollama/exposed (local model) must be in the catalog (configured means exposed, #179)")
	}
	if !cfg.InCatalog(byID["ollama/unexposed"]) {
		t.Errorf("ollama/unexposed (local model, exposed=false) must still be in the catalog (configured means exposed, #179 — visibility is governed by the live model inventory (internal/localmodels), not this predicate)")
	}
}

// TestInCatalogPredicate pins the catalog-membership rule (#179
// configured-is-exposed): a model whose provider resolves and whose location
// resolves (on the model or inherited from the provider) is in the catalog —
// native, local and cloud alike. No stored flag gates catalog membership; a
// model dropped here is one no picker and no `-M` pin can reach.
// internal/catalog is the policy owner of the per-row launch rules; this
// predicate only decides catalog membership.
func TestInCatalogPredicate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")

	writeRegistry(t, dir, `
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
auth = { type = "none", base_url = "http://localhost:11434" }

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
auth = { type = "api_key", secret_ref = "OPENROUTER_API_KEY" }

[[providers]]
id = "native-provider"
name = "Native Provider"
location = "local"
auth = { type = "native" }

[[models]]
id = "native-provider/native-model"
family = "native"
provider_id = "native-provider"
model_name = "native-model"
location = "local"
tags = ["code"]

[[models]]
id = "ollama/local-flag-ready"
family = "local-flag-ready"
provider_id = "ollama"
model_name = "local-flag-ready"
location = "local"
tags = ["code"]

[[models]]
id = "ollama/local-flag-not-ready"
family = "local-flag-not-ready"
provider_id = "ollama"
model_name = "local-flag-not-ready"
location = "local"
tags = ["code"]

[[models]]
id = "ollama/local-flag-unexposed"
family = "local-flag-unexposed"
provider_id = "ollama"
model_name = "local-flag-unexposed"
location = "local"
tags = ["code"]

[[models]]
id = "openrouter/cloud-flag"
family = "cloud-flag"
provider_id = "openrouter"
model_name = "cloud-flag"
location = "cloud"
tags = ["code"]

[[models]]
id = "openrouter/cloud-inherited"
family = "cloud-inherited"
provider_id = "openrouter"
model_name = "cloud-inherited"
tags = ["code"]
`)

	// modelman's leftover [model_state] flags, on disk: the table says
	// exposed/ready for each of these ids, and none of it may reach the
	// predicate.
	must(t, filepath.Join(dir, "local-ai/modelman.toml"), `
[model_state]

[model_state."native-provider/native-model"]
exposed = false
ready = false

[model_state."ollama/local-flag-ready"]
exposed = true
ready = true

[model_state."ollama/local-flag-not-ready"]
exposed = true
ready = false

[model_state."ollama/local-flag-unexposed"]
exposed = false
ready = true

[model_state."openrouter/cloud-flag"]
exposed = true

[model_state."openrouter/cloud-inherited"]
exposed = true
`)

	cfgDir := Dir()
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	byID := map[string]Model{}
	for _, m := range cfg.Models {
		byID[m.ID] = m
	}

	tests := []struct {
		id       string
		expected bool
		reason   string
	}{
		{"native-provider/native-model", true, "native models are always in the catalog"},
		{"ollama/local-flag-ready", true, "a local model is in the catalog"},
		{"ollama/local-flag-not-ready", true, "every local model is in the catalog — row visibility is governed by the live model inventory (internal/localmodels), not this predicate"},
		{"ollama/local-flag-unexposed", true, "no stored flag gates catalog membership (configured means exposed, #179)"},
		{"openrouter/cloud-flag", true, "a cloud model is in the catalog"},
		{"openrouter/cloud-inherited", true, "a cloud location inherited from the provider also puts the model in the catalog"},
	}

	for _, tt := range tests {
		m := byID[tt.id]
		if got := cfg.InCatalog(m); got != tt.expected {
			t.Errorf("InCatalog(%q) = %v, want %v (%s)", tt.id, got, tt.expected, tt.reason)
		}
	}
}
