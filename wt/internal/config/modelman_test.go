package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeModelmanState writes a modelman.toml under dir/local-ai/ and points
// XDG_CONFIG_HOME at dir.
func writeModelmanState(t *testing.T, dir, content string) {
	t.Helper()
	stateDir := filepath.Join(dir, "local-ai")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "modelman.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
}

// writeLitellmState is writeModelmanState plus a `[litellm]` table entry.
func writeLitellmState(t *testing.T, dir string, enabled bool, url, apiKey string) LitellmState {
	t.Helper()
	content := fmt.Sprintf("[litellm]\nenabled = %v\nurl = %q\napi_key = %q\n", enabled, url, apiKey)
	writeModelmanState(t, dir, content)
	return LitellmState{Enabled: enabled, URL: url, APIKey: apiKey}
}

// TestLoadModelmanStateMissingFile asserts that a missing modelman.toml is
// not an error: there is simply no legacy [litellm] table to fall back to.
// This is the first-run state before modelman has written anything.
func TestLoadModelmanStateMissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	litellm, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error = %v, want nil", err)
	}
	if litellm != nil {
		t.Errorf("litellm = %+v, want nil", litellm)
	}
}

// TestLoadModelmanStateHonorsXDG asserts that loadModelmanState reads from
// $XDG_CONFIG_HOME/local-ai/modelman.toml, not a hardcoded ~/.config path.
// Without this guard, a custom XDG location populated by modelman would be
// ignored and the legacy [litellm] fallback would never be found.
func TestLoadModelmanStateHonorsXDG(t *testing.T) {
	dir := t.TempDir()
	want := writeLitellmState(t, dir, true, "http://localhost:4000", "sk-xdg")

	litellm, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error = %v", err)
	}
	if litellm == nil || *litellm != want {
		t.Errorf("litellm = %+v, want %+v", litellm, want)
	}
}

// TestLoadModelmanStateMalformedTOMLError asserts that a malformed
// modelman.toml surfaces a clear parse error. Hand-edited TOML can contain
// syntax mistakes, and silent failure would hide the legacy routing state.
func TestLoadModelmanStateMalformedTOMLError(t *testing.T) {
	dir := t.TempDir()
	writeModelmanState(t, dir, `this is not toml {{{`)

	_, err := loadModelmanState()
	if err == nil {
		t.Fatal("expected error for malformed modelman.toml, got nil")
	}
	if !strings.Contains(err.Error(), "parse modelman.toml") {
		t.Errorf("error = %q, want it to mention 'parse modelman.toml'", err)
	}
}

// TestModelmanStateReadsNoPerModelState pins #179 Phase B's boundary: wt
// reads only modelman.toml's legacy [litellm] table — no [model_state] field
// at all, so neither `ready`/`downloaded`, `running` nor the retired
// `exposed` flags can creep back into a routing or picker decision, and not
// price_refresh_last_run either (the stale-price notice reads the registry).
// Local presence and running state come from wt's live inventory. A file
// carrying every per-model key still loads.
func TestModelmanStateReadsNoPerModelState(t *testing.T) {
	typ := reflect.TypeOf(modelmanState{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if want := []string{"Litellm"}; !reflect.DeepEqual(fields, want) {
		t.Fatalf("modelmanState fields = %v, want exactly %v", fields, want)
	}
	dir := t.TempDir()
	writeModelmanState(t, dir, `
price_refresh_last_run = "2026-09-14"

[model_state."omlx/qwen3.8"]
ready = true
downloaded = true
running = true
exposed = true
litellm_exposed = true
`)
	if _, err := loadModelmanState(); err != nil {
		t.Fatalf("loadModelmanState() error = %v, want per-model keys ignored", err)
	}
}

// TestLoadModelExposureAcrossNativeLocalCloud asserts the end-to-end wiring
// of Load(), deriveNative, and modelman exposure across the three location
// classes (#179 configured-is-exposed): native, local and cloud models whose
// provider resolves are all in the catalog regardless of their modelman.toml
// `exposed`/`ready` flags — none of those flags gates catalog membership any
// more. What a local row can do is decided downstream by the live inventory
// (internal/localmodels) through internal/catalog's row rules.
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

	writeModelmanState(t, dir, `
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

// TestModelmanPathHonorsXDG asserts that ModelmanPath() uses the same
// XDG base-directory resolution as RegistryPath(): when XDG_CONFIG_HOME is
// set, the returned path is exactly $XDG/local-ai/modelman.toml. A suffix-only
// assertion would still pass if a regression dropped XDG honoring and fell
// back to ~/.config (which also ends in /local-ai/modelman.toml), so the
// assertion must be tight. Unlike RegistryPath(), ModelmanPath() must never
// honor MODELMAN_REGISTRY.
func TestModelmanPathHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	t.Setenv("MODELMAN_REGISTRY", "/env/should/be/ignored.toml")
	want := filepath.Join("/custom/xdg", "local-ai", "modelman.toml")
	if got := ModelmanPath(); got != want {
		t.Errorf("ModelmanPath() = %q, want %q", got, want)
	}
}

// A leading "~" or "~/" in XDG_CONFIG_HOME must expand, matching the
// RegistryPath() contract and modelman's Path.expanduser(). Without this,
// a literal "~/" segment is left in the path and wt fails to find the
// modelman state file that modelan itself can read.
func TestModelmanPathExpandsTildeInXDG(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	t.Setenv("XDG_CONFIG_HOME", "~/custom-xdg")
	t.Setenv("MODELMAN_REGISTRY", "")
	want := filepath.Join(home, "custom-xdg", "local-ai", "modelman.toml")
	if got := ModelmanPath(); got != want {
		t.Errorf("ModelmanPath() = %q, want %q", got, want)
	}
}

// TestInCatalogPredicate implements the catalog-membership rule (#179
// configured-is-exposed): a model whose provider resolves and whose location
// resolves (on the model or inherited from the provider) is in the catalog —
// native, local and cloud alike. The exposed/ready flags in modelman.toml no
// longer gate it. internal/catalog is the policy owner of the per-row launch
// rules; this predicate only decides catalog membership.
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

	cfgDir := Dir()
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Native model: always exposed regardless of flag
	// Local model with flag+ready: exposed (unchanged)
	// Local model with flag+not-ready: exposed too (wt reads no per-model
	//   modelman state — visibility is governed by the live model inventory
	//   (internal/localmodels), not these flags)
	// Local model with exposed=false: exposed too (bypasses the exposed
	//   flag itself, not just ready)
	// Cloud model with flag (no ready key): exposed
	// Cloud-inherited model with flag (no ready key, no model location): exposed
	writeModelmanState(t, dir, `
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
		{"ollama/local-flag-ready", true, "local models are in the catalog regardless of the exposed/ready flags"},
		{"ollama/local-flag-not-ready", true, "the ready flag no longer gates catalog membership — row visibility is governed by the live model inventory (internal/localmodels), not this predicate"},
		{"ollama/local-flag-unexposed", true, "the exposed flag no longer gates catalog membership (configured means exposed, #179)"},
		{"openrouter/cloud-flag", true, "a cloud model is in the catalog regardless of the exposed/ready flags"},
		{"openrouter/cloud-inherited", true, "a cloud location inherited from the provider also puts the model in the catalog"},
	}

	for _, tt := range tests {
		m := byID[tt.id]
		if got := cfg.InCatalog(m); got != tt.expected {
			t.Errorf("InCatalog(%q) = %v, want %v (%s)", tt.id, got, tt.expected, tt.reason)
		}
	}
}
