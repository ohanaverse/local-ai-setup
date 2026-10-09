package config

import (
	"errors"
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

// TestRegistryPathHonorsXDG asserts the full RegistryPath() when
// XDG_CONFIG_HOME is set: it must be exactly $XDG/local-ai/registry.toml
// (not the fallback to ~/.config). A suffix-only assertion would still
// pass if a regression dropped XDG honoring and returned the user's
// real ~/.config — which also ends in /local-ai/registry.toml. This
// test is the regression guard for the precedence rule, so the
// assertion must be tight enough to fail when the precedence is
// broken.
func TestRegistryPathHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	want := filepath.Join("/custom/xdg", "local-ai", "registry.toml")
	if got := RegistryPath(); got != want {
		t.Errorf("RegistryPath() = %q, want %q", got, want)
	}
}

// TestRegistryPathHonorsTheRegistryOverride pins that WT_REGISTRY names the
// registry outright: it wins even when XDG_CONFIG_HOME is also set. A scratch
// run, an .envrc or a CI job names one registry this way without moving the
// rest of the config home; if XDG could shadow it, wt would read and write a
// registry the user did not name. The whole chain, with the older
// MODELMAN_REGISTRY name that is read after this one, is pinned by
// TestRegistryPathPrecedence.
func TestRegistryPathHonorsTheRegistryOverride(t *testing.T) {
	t.Setenv("WT_REGISTRY", "/custom/registry.toml")
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	if got := RegistryPath(); got != "/custom/registry.toml" {
		t.Errorf("RegistryPath() = %q, want the WT_REGISTRY override", got)
	}
}

// TestRegistryPathExpandsTildeInTheRegistryOverride pins that a WT_REGISTRY
// value starting with "~/" expands to the home directory, as llmbench's
// registry_path does (Path.expanduser()). Neither the OS nor os.ReadFile
// expands a literal "~", so without this wt would report no registry at a
// path llmbench reads, for a value an .envrc that does not shell-expand
// leaves as written.
func TestRegistryPathExpandsTildeInTheRegistryOverride(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	t.Setenv("WT_REGISTRY", "~/custom-registry.toml")
	want := filepath.Join(home, "custom-registry.toml")
	if got := RegistryPath(); got != want {
		t.Errorf("RegistryPath() = %q, want %q", got, want)
	}
}

// TestLoad_JoinsRegistry asserts the core Load() contract: config.toml's
// own sections (default_tag, agents) are joined with registry.toml's
// providers/models into one Config, so callers see a single view instead of
// two files.
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

// TestLoad_FailsClosedWithoutRegistry asserts Load() errors when
// registry.toml is absent — wrapping ErrRegistryMissing and pointing at
// `wt model init`, the command that creates one — so wt never silently runs
// with zero providers/models, and never sends the user to a tool that is
// being retired.
func TestLoad_FailsClosedWithoutRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when registry.toml is missing")
	}
	if !errors.Is(err, ErrRegistryMissing) {
		t.Errorf("error should wrap ErrRegistryMissing, got: %v", err)
	}
	if !strings.Contains(err.Error(), "seed it with `wt model init`") {
		t.Errorf("error should point at `wt model init`, got: %v", err)
	}
	if strings.Contains(err.Error(), "modelman") {
		t.Errorf("error still names modelman: %v", err)
	}
}

// loadRegistry must return the ErrRegistryMissing sentinel (not just any
// error) when registry.toml is absent, so cmd/wt can distinguish "no
// registry yet" (tolerable for command agents) from a genuine config
// problem (malformed file, etc.).
func TestLoadRegistryMissingReturnsSentinel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, err := loadRegistry()
	if !errors.Is(err, ErrRegistryMissing) {
		t.Fatalf("loadRegistry() error = %v, want ErrRegistryMissing", err)
	}
}

// TestLoad_RegistryExtraFieldsIgnored asserts forward compatibility: a
// registry may hold keys wt's typed reader does not model (a hand edit, a
// newer wt, another reader's field) without breaking the decode — unknown
// keys are ignored, so one unmodelled key does not stop every wt command.
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

// TestSave_OmitsProvidersAndModels pins the ownership boundary on the
// write side: Save persists only config.toml's own content (agents,
// default_tag). Providers and models live in registry.toml, which wt writes
// through UpdateRegistry alone; a copy of them in config.toml would be a
// second, stale catalog nothing reads.
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
		t.Errorf("Save must not persist providers/models (they belong to registry.toml):\n%s", s)
	}
	if !strings.Contains(s, "[[agents]]") {
		t.Errorf("Save must persist agents:\n%s", s)
	}
}

// TestLoad_LegacyConfigSectionsIgnored asserts a pre-Phase-4 config.toml
// still carrying its own providers/models loads cleanly: the legacy
// sections are ignored (registry.toml is the source of truth), matching
// finalizeCfg's registry-join-last ordering.
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

// A leading "~" or "~/" in XDG_CONFIG_HOME must be expanded, as it is for
// WT_REGISTRY and as llmbench's registry_path does (Path.expanduser()).
// Without this, a user (or an .envrc that doesn't shell-expand) sets
// XDG_CONFIG_HOME=~/custom-xdg, llmbench reads the expanded path while wt
// reads the literal tilde-string and fails to find the registry — two tools
// disagreeing on the very precedence rule RegistryPath's doc comment says
// they share.
func TestRegistryPathExpandsTildeInXDG(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	t.Setenv("XDG_CONFIG_HOME", "~/custom-xdg")
	want := filepath.Join(home, "custom-xdg", "local-ai", "registry.toml")
	if got := RegistryPath(); got != want {
		t.Errorf("RegistryPath() = %q, want %q", got, want)
	}
}

// TestExpandHomeHappyPath verifies the cross-platform happy path: with HOME
// set, a "~/" prefix is expanded to the user's home directory. The
// HOME-unset error path cannot be reliably simulated on platforms where
// os.UserHomeDir() falls back to the user database (most Unix); the
// contract is exercised instead by RegistryPath() and baseConfigHome(),
// which handle the error from expandHome.
func TestExpandHomeHappyPath(t *testing.T) {
	t.Setenv("HOME", "")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("cannot simulate HOME-unset on this platform; error path tested at the call sites")
	}
	if got, err := expandHome("~/x"); err != nil || got != filepath.Join(home, "x") {
		t.Errorf("expandHome(~/x) = (%q, %v), want (%q, nil)", got, err, filepath.Join(home, "x"))
	}
}

// A registry override (WT_REGISTRY or its alias) of the form "~username/..."
// is left literal: Go has no portable getpwnam. expandHome's doc comment
// records the limitation; the alternative (silently expanding to the current
// user's home) would read the wrong user's registry without a word.
func TestExpandHomeLeavesTildeUsernameLiteral(t *testing.T) {
	if got, err := expandHome("~ops/shared/registry.toml"); err != nil || got != "~ops/shared/registry.toml" {
		t.Errorf("expandHome(~ops/...) = (%q, %v), want (literal, nil)", got, err)
	}
}

// TestLoad_RegistryReadsProviderModelDirAndBaseURL verifies wt now decodes a
// provider's model_dir and auth.base_url from registry.toml. The local model
// inventory scans model_dir for on-disk models and probes base_url, so
// dropping either would silently point discovery at the wrong place.
func TestLoad_RegistryReadsProviderModelDirAndBaseURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeRegistry(t, dir, `
[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"
model_dir = "~/.mtplx/models"

[providers.auth]
type = "none"
base_url = "http://localhost:8003/v1"
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
	p := cfg.ProviderByID("mtplx")
	if p == nil {
		t.Fatal("mtplx provider not loaded")
	}
	if p.ModelDir != "~/.mtplx/models" {
		t.Errorf("ModelDir = %q, want %q", p.ModelDir, "~/.mtplx/models")
	}
	if p.Auth.BaseURL != "http://localhost:8003/v1" {
		t.Errorf("Auth.BaseURL = %q", p.Auth.BaseURL)
	}
}

// TestExpandHomeExported verifies the exported ExpandHome expands a leading
// ~/ against $HOME and leaves other paths alone — the inventory relies on it
// to turn a registry model_dir like "~/.omlx/models" into a real directory.
func TestExpandHomeExported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := ExpandHome("~/a/b")
	if err != nil || got != filepath.Join(home, "a/b") {
		t.Errorf("ExpandHome(~/a/b) = %q, %v", got, err)
	}
	if got, _ := ExpandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("ExpandHome(/abs/path) = %q", got)
	}
}

// TestRegistryRedirected pins what counts as a registry wt was sent to by the
// environment: WT_REGISTRY or XDG_CONFIG_HOME naming anything but the
// default ~/.config/local-ai/registry.toml. LiteLLM's config.yaml follows
// neither variable, so the route writers use this to refuse pairing a
// redirected registry with the default config.yaml. Spelling the default path
// out is not a redirect. The answer compares RegistryPath with the default,
// so the older MODELMAN_REGISTRY name counts through the same call
// (TestRegistryPathPrecedence pins that RegistryPath reads it).
func TestRegistryRedirected(t *testing.T) {
	home := t.TempDir()
	def := filepath.Join(home, ".config", "local-ai", "registry.toml")
	for _, tc := range []struct {
		name, registry, xdg string
		want                bool
	}{
		{"nothing set", "", "", false},
		{"WT_REGISTRY elsewhere", filepath.Join(home, "scratch", "registry.toml"), "", true},
		{"XDG_CONFIG_HOME elsewhere", "", filepath.Join(home, "xdg"), true},
		{"WT_REGISTRY spells the default", def, "", false},
		{"WT_REGISTRY spells the default with a tilde", "~/.config/local-ai/registry.toml", "", false},
		{"XDG_CONFIG_HOME spells the default", "", filepath.Join(home, ".config"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("WT_REGISTRY", tc.registry)
			t.Setenv("XDG_CONFIG_HOME", tc.xdg)
			if got := RegistryRedirected(); got != tc.want {
				t.Fatalf("RegistryRedirected() = %v, want %v (registry path %s)", got, tc.want, RegistryPath())
			}
		})
	}
}
