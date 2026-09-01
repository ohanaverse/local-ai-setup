package config

import (
	"os"
	"path/filepath"
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

// TestLoadModelmanStateMissingFileReturnsEmptySet asserts that a missing
// modelman.toml is not an error: every non-native model is simply unexposed
// until modelman marks it. This is the first-run state before any model has
// been routed through LiteLLM.
func TestLoadModelmanStateMissingFileReturnsEmptySet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	exposed, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error = %v, want nil", err)
	}
	if len(exposed) != 0 {
		t.Errorf("exposed = %v, want empty map", exposed)
	}
}

// TestLoadModelmanStateHonorsXDG asserts that loadModelmanState reads from
// $XDG_CONFIG_HOME/local-ai/modelman.toml, not a hardcoded ~/.config path.
// Without this guard, a custom XDG location populated by modelman would be
// ignored and wt would see no exposed models.
func TestLoadModelmanStateHonorsXDG(t *testing.T) {
	dir := t.TempDir()
	writeModelmanState(t, dir, `
[model_state]

[model_state.exposed-model]
litellm_exposed = true
`)

	exposed, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error = %v", err)
	}
	if !exposed["exposed-model"] {
		t.Errorf("exposed[exposed-model] = false, want true")
	}
	if len(exposed) != 1 {
		t.Errorf("len(exposed) = %d, want 1", len(exposed))
	}
}

// TestLoadModelmanStateMalformedTOMLError asserts that a malformed
// modelman.toml surfaces a clear parse error. Hand-edited TOML can contain
// syntax mistakes, and silent failure would leave every model unexposed.
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

// TestLoadExposesOnlyLitellmExposedModels asserts the end-to-end wiring of
// Load(), deriveNative, and modelman exposure: native models are always
// exposed; non-native models are exposed only when modelman.toml marks them
// litellm_exposed. This prevents wt from advertising models that the LiteLLM
// proxy is not configured to serve.
func TestLoadExposesOnlyLitellmExposedModels(t *testing.T) {
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
litellm_exposed = true

[model_state."ollama/unexposed"]
litellm_exposed = false

[model_state."agy/native"]
litellm_exposed = false
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

	if !cfg.IsExposed(byID["agy/native"]) {
		t.Errorf("agy/native (native provider) must always be exposed")
	}
	if !cfg.IsExposed(byID["ollama/exposed"]) {
		t.Errorf("ollama/exposed (litellm_exposed=true) must be exposed")
	}
	if cfg.IsExposed(byID["ollama/unexposed"]) {
		t.Errorf("ollama/unexposed (litellm_exposed=false) must not be exposed")
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
