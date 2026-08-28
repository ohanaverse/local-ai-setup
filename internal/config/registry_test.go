package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRegistry writes a registry.toml under dir/local-ai/ and points
// XDG_CONFIG_HOME at dir.
func writeRegistry(t *testing.T, dir, content string) {
	t.Helper()
	regDir := filepath.Join(dir, "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
}

const minimalRegistry = `
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
auth = { type = "none", base_url = "http://localhost:11434" }

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
location = "local"
tags = ["code"]
`

func TestRegistryPathHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	if got := RegistryPath(); !strings.HasSuffix(got, "/local-ai/registry.toml") && !strings.HasSuffix(got, "\\local-ai\\registry.toml") {
		t.Errorf("RegistryPath() = %q, want suffix local-ai/registry.toml", got)
	}
}

func TestRegistryPathHonorsModelmanRegistryOverride(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "/custom/registry.toml")
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	if got := RegistryPath(); got != "/custom/registry.toml" {
		t.Errorf("RegistryPath() = %q, want MODELMAN_REGISTRY override", got)
	}
}

// A MODELMAN_REGISTRY value starting with "~/" must expand to the real home
// directory, matching modelman's Python resolver (Path.expanduser()) — a
// literal "~" segment is never expanded by the OS or os.ReadFile, so without
// this wt would fail to find a registry that modelman itself can read.
func TestRegistryPathExpandsTildeInModelmanRegistryOverride(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	t.Setenv("MODELMAN_REGISTRY", "~/custom-registry.toml")
	want := filepath.Join(home, "custom-registry.toml")
	if got := RegistryPath(); got != want {
		t.Errorf("RegistryPath() = %q, want %q", got, want)
	}
}

func TestLoad_JoinsRegistry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeRegistry(t, dir, minimalRegistry)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"ollama\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].ID != "ollama" {
		t.Errorf("providers = %+v, want one ollama provider", cfg.Providers)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "ollama/gemma4:9b" {
		t.Errorf("models = %+v, want one gemma4 model", cfg.Models)
	}
}

func TestLoad_FailsClosedWithoutRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when registry.toml is missing")
	}
	if !strings.Contains(err.Error(), "modelman migrate") {
		t.Errorf("error should point at `modelman migrate`, got: %v", err)
	}
}

func TestLoad_RegistryExtraFieldsIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeRegistry(t, dir, `
[[providers]]
id = "ollama"
name = "Ollama"
model_dir = "/extra/ignored"

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
location = "local"
tags = ["code"]

[models.model_info]
supports_function_calling = true

[models.cost]
kind = "free"
`)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ModelName != "gemma4:9b" {
		t.Errorf("registry model not loaded: %+v", cfg.Models)
	}
}

func TestSave_OmitsProvidersAndModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama"}},
		Models:     []Model{{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}},
		Agents:     []Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "[[providers]]") || strings.Contains(s, "[[models]]") {
		t.Errorf("Save must not persist providers/models (modelman owns registry.toml):\n%s", s)
	}
	if !strings.Contains(s, "[[agents]]") {
		t.Errorf("Save must persist agents:\n%s", s)
	}
}

func TestLoad_LegacyConfigSectionsIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeRegistry(t, dir, minimalRegistry)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-Phase-4 config.toml still carries providers/models; they must be
	// ignored (registry.toml is the source of truth) and never resurrected.
	legacy := "default_tag = \"code\"\n[[providers]]\nid = \"stale\"\nname = \"Stale\"\n[[models]]\nid = \"stale/x\"\nprovider_id = \"stale\"\nmodel_name = \"x\"\n"
	if err := os.WriteFile(Path(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, p := range cfg.Providers {
		if p.ID == "stale" {
			t.Error("legacy config.toml provider leaked into the joined catalog")
		}
	}
}
