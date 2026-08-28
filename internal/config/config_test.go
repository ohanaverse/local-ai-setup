package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Path must honor XDG_CONFIG_HOME when set, and fall back to ~/.config when
// not. Getting this wrong means the tool reads/writes the wrong config file
// and users lose their settings or see defaults unexpectedly.
func TestPath(t *testing.T) {
	// With XDG_CONFIG_HOME set
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	if got := Path(); got != "/custom/xdg/agent-wt/config.toml" {
		t.Errorf("Path() = %q, want %q", got, "/custom/xdg/agent-wt/config.toml")
	}

	// Without XDG_CONFIG_HOME — falls back to ~/.config
	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "agent-wt", "config.toml")
	if got := Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

// First-run experience: when no config file exists, Load must return a usable
// default Config (not an error). The modelman-owned registry.toml must exist
// (seeded by `modelman migrate`); with an empty registry, Load returns an
// empty catalog.
func TestLoad_FileNotExist(t *testing.T) {
	tmp := t.TempDir()
	writeRegistry(t, tmp, "providers = []\nmodels = []\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DefaultTag != "code" {
		t.Errorf("DefaultTag = %q, want %q", cfg.DefaultTag, "code")
	}
	if len(cfg.Providers) != 0 || len(cfg.Models) != 0 || len(cfg.Agents) != 0 {
		t.Error("expected empty Providers, Models, Agents on first run")
	}
}

// End-to-end parse check: a valid config.toml (agents + default tag) joined
// with a valid registry.toml (providers + models) must deserialize correctly
// into the typed structs. This is the happy path that every real config
// follows.
func TestLoad_ValidConfig(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "agent-wt")
	os.MkdirAll(cfgDir, 0755)
	os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`
default_tag = "design"

[[agents]]
name = "claude"
supported_providers = ["ollama"]
default_provider = "ollama"
`), 0644)
	writeRegistry(t, tmp, `
[[providers]]
id = "ollama"
name = "Ollama"
auth = { type = "none", base_url = "http://localhost:11434" }

[[providers]]
id = "agy"
name = "Antigravity"
location = "cloud"
auth = { type = "native" }

[[models]]
id = "ollama/test:1"
family = "test"
provider_id = "ollama"
model_name = "test:1"
location = "local"
tags = ["code"]

[[models]]
id = "agy/native"
family = "agy"
provider_id = "agy"
model_name = "native"
location = "cloud"
tags = ["code", "design"]
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DefaultTag != "design" {
		t.Errorf("DefaultTag = %q, want %q", cfg.DefaultTag, "design")
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers (ollama + agy from registry), got %d", len(cfg.Providers))
	}
	if cfg.Providers[0].ID != "ollama" {
		t.Errorf("provider ID = %q, want %q", cfg.Providers[0].ID, "ollama")
	}
	if cfg.Providers[0].Auth.Type != "none" {
		t.Errorf("auth type = %q, want %q", cfg.Providers[0].Auth.Type, "none")
	}
	if cfg.Providers[0].Auth.BaseURL != "http://localhost:11434" {
		t.Errorf("auth base_url = %q, want %q", cfg.Providers[0].Auth.BaseURL, "http://localhost:11434")
	}
	if len(cfg.Models) != 2 {
		t.Fatalf("expected 2 models (test + agy/native from registry), got %d", len(cfg.Models))
	}
	if cfg.Models[0].Family != "test" {
		t.Errorf("model family = %q, want %q", cfg.Models[0].Family, "test")
	}
	if len(cfg.Agents) != 2 {
		t.Fatalf("expected 2 agents (claude + agy from schema migration), got %d", len(cfg.Agents))
	}
	if cfg.Agents[0].DefaultProvider != "ollama" {
		t.Errorf("agent default = %q, want %q", cfg.Agents[0].DefaultProvider, "ollama")
	}
}

// Corrupt config files must produce a clear parse error, not a nil config or
// a panic. Users hand-edit TOML and will make syntax mistakes; the tool must
// surface the problem rather than silently using defaults.
func TestLoad_BadTOML(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "agent-wt")
	os.MkdirAll(cfgDir, 0755)
	os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`this is not toml {{{`), 0644)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for bad TOML, got nil")
	}
}

// DefaultTag drives which rotation group is used when the user doesn't
// specify --code or --design. An empty tag means the tool has no way to
// select models, so it must be rejected at validation time.
func TestValidate_EmptyDefaultTag(t *testing.T) {
	cfg := &Config{DefaultTag: ""}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty default_tag")
	}
}

// Provider IDs are the foreign keys that models and agents reference.
// Duplicates would make those references ambiguous — which "ollama" does a
// model mean? Validation must catch this.
func TestValidate_DuplicateProviderID(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers: []Provider{
			{ID: "ollama", Name: "Ollama"},
			{ID: "ollama", Name: "Ollama Dup"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate provider id")
	}
}

// An empty provider ID is a configuration error — models can't reference a
// provider with no identity. Catch it early so the user gets a clear message.
func TestValidate_EmptyProviderID(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "", Name: "NoID"}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty provider id")
	}
}

// Model IDs are the unique keys for the registry. Duplicates would cause
// silent overwrites or ambiguous lookups when the TUI or rotation tries to
// select a model.
func TestValidate_DuplicateModelID(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama"}},
		Models: []Model{
			{ID: "ollama/x", Family: "x", ProviderID: "ollama", ModelName: "x", Location: "local"},
			{ID: "ollama/x", Family: "x", ProviderID: "ollama", ModelName: "x", Location: "local"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate model id")
	}
}

// Every model's ProviderID must reference a provider that exists in the
// config. An orphan model (pointing to a missing provider) can never be
// launched and indicates a config bug.
func TestValidate_UnknownProvider(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Models: []Model{
			{ID: "ghost/x", Family: "x", ProviderID: "ghost", ModelName: "x", Location: "local"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// Location is required for launch (local vs cloud affects how the agent
// connects). If neither the model nor its provider sets a location, the
// tool can't determine how to invoke the model — this must be caught.
func TestValidate_NoLocation(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama"}},
		Models: []Model{
			{ID: "ollama/x", Family: "x", ProviderID: "ollama", ModelName: "x"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing location")
	}
}

// When a model omits location but its provider declares one, the config is
// valid — the provider's location is inherited. This is the common case for
// cloud-only providers like OpenRouter where every model is cloud.
func TestValidate_LocationFromProvider(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama", Location: "local"}},
		Models: []Model{
			{ID: "ollama/x", Family: "x", ProviderID: "ollama", ModelName: "x"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

// Agent names are used as keys for lookups and launcher dispatch. Duplicates
// would make AgentByName ambiguous — which "claude" agent config should be
// used?
func TestValidate_DuplicateAgentName(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama"}},
		Agents: []Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate agent name")
	}
}

// An agent's SupportedProviders must only reference providers that exist.
// Otherwise the cascading filter (agent → providers → models) breaks: the
// TUI would show provider options that can't be resolved.
func TestValidate_AgentUnknownProvider(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Agents: []Agent{
			{Name: "claude", SupportedProviders: []string{"ghost"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for agent referencing unknown provider")
	}
}

// DefaultProvider must be in the agent's SupportedProviders list. If it
// isn't, --default would select a provider the agent can't use, producing a
// confusing runtime error instead of a clear config validation error.
func TestValidate_DefaultProviderNotInSupported(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama"}, {ID: "claude", Name: "Claude"}},
		Agents: []Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}, DefaultProvider: "claude"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for default provider not in supported_providers")
	}
}

// An agent with no supported_providers is unusable: the launcher cannot pick
// a model for it. Validate must reject this so the user sees a clear error
// instead of a runtime "no eligible models" message during launch.
func TestValidate_AgentRequiresProvider(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama", Auth: AuthConfig{Type: "none"}}},
		Agents:     []Agent{{Name: "lonely"}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty supported_providers")
	}
	if !strings.Contains(err.Error(), "must have at least one supported provider") {
		t.Errorf("error = %q, want it to mention 'supported provider'", err)
	}
}

// ValidateAll must collect every validation error at once, not stop at the
// first one. This gives users a complete list of config problems.
func TestValidateAll_CollectsMultipleErrors(t *testing.T) {
	cfg := &Config{
		DefaultTag: "",
		Providers:  []Provider{{ID: "ollama"}, {ID: "ollama"}},
		Models:     []Model{{ID: "a", ProviderID: "ghost"}},
	}
	err := cfg.ValidateAll()
	if err == nil {
		t.Fatal("expected errors")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "default_tag") {
		t.Error("expected default_tag error")
	}
	if !strings.Contains(errStr, "duplicate provider") {
		t.Error("expected duplicate provider error")
	}
	if !strings.Contains(errStr, "ghost") {
		t.Error("expected unknown provider error")
	}
}

// Smoke test: a realistic config with multiple providers, cross-provider
// model families, and an agent with a default must pass validation. This
// catches regressions where new validation rules break valid configs.
func TestValidate_ValidFullConfig(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers: []Provider{
			{ID: "ollama", Name: "Ollama"},
			{ID: "openrouter", Name: "OpenRouter", Location: "cloud"},
		},
		Models: []Model{
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: "local", Tags: []string{"code"}},
			{ID: "openrouter/gemma-4-9b", Family: "gemma4", ProviderID: "openrouter", ModelName: "gemma-4-9b", Tags: []string{"code"}},
		},
		Agents: []Agent{
			{Name: "claude", SupportedProviders: []string{"ollama", "openrouter"}, DefaultProvider: "ollama"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

// ModelsWithTag is the primary filter for rotation (--code / --design).
// It must return only models with the given tag, include models that have
// the tag alongside others, and return empty for unknown tags.
func TestModelsWithTag(t *testing.T) {
	cfg := &Config{
		Models: []Model{
			{ID: "a", Tags: []string{"code", "design"}},
			{ID: "b", Tags: []string{"design"}},
			{ID: "c", Tags: []string{"code"}},
			{ID: "d", Tags: []string{}},
		},
	}
	code := cfg.ModelsWithTag("code")
	if len(code) != 2 {
		t.Fatalf("expected 2 code models, got %d", len(code))
	}
	if code[0].ID != "a" || code[1].ID != "c" {
		t.Errorf("unexpected code models: %v", code)
	}

	design := cfg.ModelsWithTag("design")
	if len(design) != 2 {
		t.Fatalf("expected 2 design models, got %d", len(design))
	}

	empty := cfg.ModelsWithTag("nonexistent")
	if len(empty) != 0 {
		t.Errorf("expected 0 models for unknown tag, got %d", len(empty))
	}
}

// ProviderByID is the primary foreign-key lookup. It must return a pointer
// for found providers (so callers can check nil) and nil for missing ones.
// Used by ResolveLocation and validation.
func TestProviderByID(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{
			{ID: "ollama", Name: "Ollama"},
			{ID: "openrouter", Name: "OpenRouter"},
		},
	}
	p := cfg.ProviderByID("ollama")
	if p == nil {
		t.Fatal("expected to find ollama provider")
	}
	if p.Name != "Ollama" {
		t.Errorf("name = %q, want %q", p.Name, "Ollama")
	}

	p = cfg.ProviderByID("nonexistent")
	if p != nil {
		t.Error("expected nil for unknown provider")
	}
}

// AgentByName looks up an agent by its name. Used by the CLI to resolve
// which agent config to use. Must return a clear error for unknown agents so
// the caller can surface it to the user.
func TestAgentByName(t *testing.T) {
	cfg := &Config{
		Agents: []Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "pi", SupportedProviders: []string{"openrouter"}},
		},
	}
	a, err := cfg.AgentByName("claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Name != "claude" {
		t.Errorf("name = %q, want %q", a.Name, "claude")
	}

	_, err = cfg.AgentByName("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

// ResolveLocation implements the inheritance rule: model location overrides
// provider location. This is used at launch time to decide whether to connect
// locally or via cloud API. Must error when neither is set (can't launch
// without knowing where the model lives) and when the provider is unknown.
func TestResolveLocation(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{
			{ID: "ollama", Name: "Ollama"},
			{ID: "openrouter", Name: "OpenRouter", Location: "cloud"},
		},
	}

	// Model has location — takes precedence
	m := Model{ID: "x", ProviderID: "ollama", Location: "local"}
	loc, err := cfg.ResolveLocation(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loc != "local" {
		t.Errorf("location = %q, want %q", loc, "local")
	}

	// Model has no location, provider does
	m2 := Model{ID: "y", ProviderID: "openrouter"}
	loc, err = cfg.ResolveLocation(m2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loc != "cloud" {
		t.Errorf("location = %q, want %q", loc, "cloud")
	}

	// Neither has location
	m3 := Model{ID: "z", ProviderID: "ollama"}
	_, err = cfg.ResolveLocation(m3)
	if err == nil {
		t.Fatal("expected error for missing location")
	}

	// Unknown provider
	m4 := Model{ID: "w", ProviderID: "ghost"}
	_, err = cfg.ResolveLocation(m4)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// HasTag is used by ModelsWithTag and throughout the TUI for filtering.
// It must handle nil tags safely (no panic) and return true only for exact
// matches.
func TestHasTag(t *testing.T) {
	m := Model{Tags: []string{"code", "design"}}
	if !m.HasTag("code") {
		t.Error("expected HasTag(\"code\") = true")
	}
	if !m.HasTag("design") {
		t.Error("expected HasTag(\"design\") = true")
	}
	if m.HasTag("nonexistent") {
		t.Error("expected HasTag(\"nonexistent\") = false")
	}

	m2 := Model{Tags: nil}
	if m2.HasTag("anything") {
		t.Error("expected HasTag on nil tags = false")
	}
}

// Save writes config.toml atomically via temp file + rename. Providers and
// models are modelman-owned (registry.toml) and are never persisted by wt, so
// a Save of a config carrying providers must not write them to disk.
func TestSave(t *testing.T) {
	tmp := t.TempDir()
	writeRegistry(t, tmp, "providers = []\nmodels = []\n")

	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama"}},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// File must exist and be loadable.
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save() error = %v", err)
	}
	if loaded.DefaultTag != "code" {
		t.Errorf("DefaultTag = %q, want %q", loaded.DefaultTag, "code")
	}
	// Save trims providers/models (modelman owns registry.toml), so the
	// saved config.toml has no providers and Load returns an empty catalog.
	if len(loaded.Providers) != 0 {
		t.Errorf("Providers = %v, want 0 (providers are modelman-owned, not saved by wt)", loaded.Providers)
	}
}

// TestModelsForAgentFiltersByProvider asserts ModelsForAgent returns only
// models whose ProviderID is in the agent's supported_providers list.
// Without this filter the TUI list would show models the active agent
// cannot drive (e.g. claude/native listed for the codex agent).
func TestModelsForAgentFiltersByProvider(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{
			{ID: "ollama"},
			{ID: "claude"},
		},
		Models: []Model{
			{ID: "claude/native", ProviderID: "claude", ModelName: "native"},
			{ID: "ollama/a:9b", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/b:9b", ProviderID: "ollama", Tags: []string{"code"}},
		},
		Agents: []Agent{
			{Name: "codex", SupportedProviders: []string{"ollama"}},
		},
	}
	got, err := cfg.ModelsForAgent("codex")
	if err != nil {
		t.Fatalf("ModelsForAgent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (claude/native filtered out)", len(got))
	}
	for _, m := range got {
		if m.ProviderID != "ollama" {
			t.Errorf("got model %q with provider %q, want only ollama", m.ID, m.ProviderID)
		}
	}
}

// TestModelsForAgentUnknownAgent asserts that asking for a non-existent
// agent returns an error rather than panicking. The TUI uses this to
// surface a clear error if --agent points at a typo.
func TestModelsForAgentUnknownAgent(t *testing.T) {
	cfg := &Config{
		Agents: []Agent{{Name: "claude"}},
	}
	_, err := cfg.ModelsForAgent("nope")
	if err == nil {
		t.Fatal("expected error for unknown agent, got nil")
	}
}

// TestModelsForAgentAndTagIntersectsBoth asserts the helper composes the
// agent filter and the tag filter. A model that passes the agent filter
// but is tagged with a different group must be excluded.
func TestModelsForAgentAndTagIntersectsBoth(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{{ID: "ollama"}},
		Models: []Model{
			{ID: "ollama/code-1", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/design-1", ProviderID: "ollama", Tags: []string{"design"}},
			{ID: "ollama/both", ProviderID: "ollama", Tags: []string{"code", "design"}},
		},
		Agents: []Agent{
			{Name: "codex", SupportedProviders: []string{"ollama"}},
		},
	}
	got, err := cfg.ModelsForAgentAndTag("codex", "code")
	if err != nil {
		t.Fatalf("ModelsForAgentAndTag: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (ollama/code-1, ollama/both)", len(got))
	}
	for _, m := range got {
		if !m.HasTag("code") {
			t.Errorf("got %q without code tag", m.ID)
		}
	}
}
