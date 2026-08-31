package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Location says whether a model is hosted locally or in the cloud.
type Location string

const (
	LocationLocal Location = "local"
	LocationCloud Location = "cloud"
)

// Source tracks how a model entered the registry.
type Source string

const (
	SourceCurated    Source = "curated"
	SourceDiscovered Source = "discovered"
)

// OllamaBaseURL is the default address of the local Ollama gateway.
// It is kept in the config package for legacy migration only; agent
// drivers now declare their own full gateway URLs via the OllamaURLer
// capability in internal/agents.
const OllamaBaseURL = "http://localhost:11434"

// ── GatewayConfig ─────────────────────────────────────────

// GatewayConfig is wt-owned gateway routing config.
type GatewayConfig struct {
	Mode   string `toml:"mode"` // "direct" | "litellm"
	URL    string `toml:"url"`
	APIKey string `toml:"api_key"`
}

// IsDirect reports whether the gateway is disabled/absent.
func (g GatewayConfig) IsDirect() bool {
	return g.Mode == "" || g.Mode == "direct"
}

// IsLitellm reports whether the gateway routes through the LiteLLM proxy.
func (g GatewayConfig) IsLitellm() bool {
	return g.Mode == "litellm"
}

// BaseURL returns g.URL with any trailing slashes removed. Drivers append
// their own protocol suffix (/v1, /v1/), so a user URL like
// "http://localhost:4000/" must not produce a double slash. TrimRight (not
// TrimSuffix) so a double-slash typo is also normalized.
func (g GatewayConfig) BaseURL() string { return strings.TrimRight(g.URL, "/") }

// ── Provider ──────────────────────────────────────────────

// Provider is a source of models with connection info.
type Provider struct {
	ID       string     `toml:"id"`
	Name     string     `toml:"name"`
	Location Location   `toml:"location,omitempty"`
	Auth     AuthConfig `toml:"auth"`
}

// AuthConfig describes how to authenticate with a provider.
type AuthConfig struct {
	Type      string `toml:"type"` // "none", "api_key", "oauth", "native"
	SecretRef string `toml:"secret_ref,omitempty"`
	BaseURL   string `toml:"base_url,omitempty"`
}

// ── Model ─────────────────────────────────────────────────

// Model is a specific variant of a base model available from a provider.
type Model struct {
	ID         string   `toml:"id"`          // unique key, e.g. "ollama/gemma4:9b"
	Family     string   `toml:"family"`      // base model grouping, e.g. "gemma4"
	ProviderID string   `toml:"provider_id"` // → Provider.ID
	ModelName  string   `toml:"model_name"`  // provider-specific name, e.g. "gemma4:9b"
	Location   Location `toml:"location,omitempty"`
	Tags       []string `toml:"tags"`             // e.g. ["code", "design"]
	Source     Source   `toml:"source,omitempty"` // curated or discovered
	Native     bool     `toml:"-"`                // derived: provider auth.type == "native"; not persisted
}

// ── Agent ─────────────────────────────────────────────────

// Agent is a supported AI coding tool.
type Agent struct {
	Name               string   `toml:"name"`
	SupportedProviders []string `toml:"supported_providers"`        // hard constraint
	DefaultProvider    string   `toml:"default_provider,omitempty"` // optional
}

// ── Config ────────────────────────────────────────────────

// Config is wt's in-memory configuration: Agents + DefaultTag come from
// wt-owned config.toml; Providers + Models come from modelman-owned
// registry.toml and are never persisted by wt (see Save).
type Config struct {
	DefaultTag string          `toml:"default_tag"`
	Gateway    GatewayConfig   `toml:"gateway"`
	Providers  []Provider      `toml:"providers"`
	Models     []Model         `toml:"models"`
	Agents     []Agent         `toml:"agents"`
	exposed    map[string]bool `toml:"-"` // from modelman.toml
}

// Dir returns the base config directory (~/.config/agent-wt, or
// $XDG_CONFIG_HOME/agent-wt), honoring XDG_CONFIG_HOME like wt-core.sh.
func Dir() string {
	return filepath.Join(baseConfigHome(), "agent-wt")
}

// baseConfigHome returns the XDG base config directory honoring
// XDG_CONFIG_HOME (with a leading "~" or "~/" expanded via expandHome,
// matching Python's Path.expanduser() used by modelman). Falls back to
// ~/.config when XDG_CONFIG_HOME is unset. Shared by Dir() and
// RegistryPath() so the two agree on the XDG precedence rule.
func baseConfigHome() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config")
	}
	expanded, err := expandHome(base)
	if err != nil {
		return base
	}
	return expanded
}

// Path returns the config file location.
func Path() string {
	return filepath.Join(Dir(), "config.toml")
}

// Load reads config.toml (Agents + DefaultTag — wt-owned) and joins it with
// modelman-owned registry.toml (Providers + Models) into one in-memory
// Config. The registry is checked before any schema-migration save so a
// missing registry fails closed before wt rewrites config.toml: legacy
// provider/model sections must survive on disk for `modelman migrate` to
// import them. Returns an empty Config if config.toml does not exist yet.
func Load() (*Config, error) {
	if _, err := Migrate(); err != nil {
		return nil, fmt.Errorf("migration: %w", err)
	}

	cfg := &Config{DefaultTag: "code"}
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		providers, models, err := loadRegistry()
		if err != nil {
			return nil, err
		}
		return finalizeCfg(cfg, providers, models)
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	providers, models, err := loadRegistry()
	if err != nil {
		return nil, err
	}

	changed, err := migrateConfigSchema(cfg)
	if err != nil {
		return nil, fmt.Errorf("schema migration: %w", err)
	}
	if changed {
		if err := Save(cfg); err != nil {
			return nil, fmt.Errorf("save migrated config: %w", err)
		}
		fmt.Fprintln(os.Stderr, "wt: migrated config to native-provider alignment (renamed google→agy, rewired opencode to ollama-only)")
	}

	// Join modelman-owned registry LAST so wt never mutates registry data
	// in memory (schema fixups above only ever see wt-owned config.toml
	// content). Providers/Models from a pre-Phase-4 config.toml are
	// overwritten here — registry.toml is the source of truth.
	return finalizeCfg(cfg, providers, models)
}

// finalizeCfg joins registry providers/models into cfg, derives native-ness
// from provider auth types, and loads modelman exposure state. Callers must
// already have loaded config.toml and registry.toml.
func finalizeCfg(cfg *Config, providers []Provider, models []Model) (*Config, error) {
	cfg.Providers, cfg.Models = providers, models
	deriveNative(cfg)
	exposed, err := loadModelmanState()
	if err != nil {
		return nil, err
	}
	cfg.exposed = exposed
	return cfg, nil
}

// Validate returns an error describing the first invalid entry.
func (c *Config) Validate() error {
	if errs := c.validate(); len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// ValidateAll reports every validation problem at once using errors.Join.
func (c *Config) ValidateAll() error {
	return errors.Join(c.validate()...)
}

// validate collects every validation problem in c, in a stable order.
func (c *Config) validate() []error {
	var errs []error
	if c.DefaultTag == "" {
		errs = append(errs, fmt.Errorf("default_tag must not be empty"))
	}

	if c.Gateway.Mode != "" && c.Gateway.Mode != "direct" && c.Gateway.Mode != "litellm" {
		errs = append(errs, fmt.Errorf("gateway.mode must be empty, direct, or litellm, got %q", c.Gateway.Mode))
	}

	// litellm mode is unusable without a base URL and bearer token: drivers
	// would emit empty ANTHROPIC_BASE_URL / base_url values and silently
	// misroute (claude falls back to api.anthropic.com with the gateway key).
	// Fail fast at validation time instead.
	if c.Gateway.IsLitellm() {
		if c.Gateway.URL == "" {
			errs = append(errs, fmt.Errorf("gateway.url must be set when gateway.mode is litellm"))
		}
		if c.Gateway.APIKey == "" {
			errs = append(errs, fmt.Errorf("gateway.api_key must be set when gateway.mode is litellm"))
		}
	}

	// Providers
	provIDs := map[string]bool{}
	for _, p := range c.Providers {
		if p.ID == "" {
			errs = append(errs, fmt.Errorf("provider entry with empty id"))
		}
		if provIDs[p.ID] {
			errs = append(errs, fmt.Errorf("duplicate provider id %q", p.ID))
		}
		provIDs[p.ID] = true
	}

	// Models
	modelIDs := map[string]bool{}
	for _, m := range c.Models {
		if m.ID == "" {
			errs = append(errs, fmt.Errorf("model entry with empty id"))
		}
		if modelIDs[m.ID] {
			errs = append(errs, fmt.Errorf("duplicate model id %q", m.ID))
		}
		modelIDs[m.ID] = true
		if !provIDs[m.ProviderID] {
			errs = append(errs, fmt.Errorf("model %q: unknown provider %q", m.ID, m.ProviderID))
		} else if _, err := c.ResolveLocation(m); err != nil {
			// Location must be resolvable
			errs = append(errs, err)
		}
	}

	// Agents
	agentNames := map[string]bool{}
	for _, a := range c.Agents {
		if a.Name == "" {
			errs = append(errs, fmt.Errorf("agent entry with empty name"))
		}
		if agentNames[a.Name] {
			errs = append(errs, fmt.Errorf("duplicate agent name %q", a.Name))
		}
		agentNames[a.Name] = true
		if len(a.SupportedProviders) == 0 {
			errs = append(errs, fmt.Errorf("agent %q: must have at least one supported provider", a.Name))
		}
		for _, pid := range a.SupportedProviders {
			if !provIDs[pid] {
				errs = append(errs, fmt.Errorf("agent %q: unknown provider %q", a.Name, pid))
			}
		}
		if a.DefaultProvider != "" {
			found := false
			for _, pid := range a.SupportedProviders {
				if pid == a.DefaultProvider {
					found = true
					break
				}
			}
			if !found {
				errs = append(errs, fmt.Errorf("agent %q: default provider %q not in supported_providers", a.Name, a.DefaultProvider))
			}
		}
	}

	return errs
}

// HasTag returns true if the model has the given tag.
func (m Model) HasTag(tag string) bool {
	for _, t := range m.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// ModelsWithTag returns models whose tags include tag.
func (c *Config) ModelsWithTag(tag string) []Model {
	var out []Model
	for _, m := range c.Models {
		if m.HasTag(tag) {
			out = append(out, m)
		}
	}
	return out
}

// deriveNative marks each model whose provider authenticates natively
// (auth.type == "native") as Native. It runs after the registry join so the
// in-memory Native field reflects the registry's auth data — the single
// source of truth for native-ness. A model whose provider is missing (or has
// a non-native auth type) is left non-native.
func deriveNative(cfg *Config) {
	for i := range cfg.Models {
		p := cfg.ProviderByID(cfg.Models[i].ProviderID)
		cfg.Models[i].Native = p != nil && p.Auth.Type == "native"
	}
}

// IsExposed reports whether m should appear in wt's model catalog.
// Native models are always exposed (they cannot route through LiteLLM).
func (c *Config) IsExposed(m Model) bool {
	if m.Native {
		return true
	}
	return c.exposed[m.ID]
}

// SetExposedForTest replaces the in-memory exposed set. Tests only.
func (c *Config) SetExposedForTest(exposed map[string]bool) {
	c.exposed = exposed
}

// ExposeAllForTest marks every non-native model in cfg as exposed. Tests only.
func (c *Config) ExposeAllForTest() {
	if c.exposed == nil {
		c.exposed = make(map[string]bool)
	}
	for _, m := range c.Models {
		if !m.Native {
			c.exposed[m.ID] = true
		}
	}
}

// ProviderByID returns the provider with the given id, or nil if not found.
func (c *Config) ProviderByID(id string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].ID == id {
			return &c.Providers[i]
		}
	}
	return nil
}

// AgentByName returns the agent with the given name, or an error if not found.
func (c *Config) AgentByName(name string) (*Agent, error) {
	for i := range c.Agents {
		if c.Agents[i].Name == name {
			return &c.Agents[i], nil
		}
	}
	return nil, fmt.Errorf("agent %q not found", name)
}

// ResolveLocation returns the effective location for a model.
// Model location takes precedence; falls back to provider location.
// Returns an error if neither is set or the provider is unknown.
func (c *Config) ResolveLocation(m Model) (Location, error) {
	if m.Location != "" {
		return m.Location, nil
	}
	p := c.ProviderByID(m.ProviderID)
	if p == nil {
		return "", fmt.Errorf("model %q: unknown provider %q", m.ID, m.ProviderID)
	}
	if p.Location != "" {
		return p.Location, nil
	}
	return "", fmt.Errorf("model %q: no location on model or provider %q", m.ID, p.ID)
}

// UpsertAgent adds a when oldName is empty, or updates the agent named
// oldName. It validates name, supported providers, and default provider.
func (c *Config) UpsertAgent(a Agent, oldName string) error {
	if a.Name == "" {
		return fmt.Errorf("agent name is empty")
	}
	if len(a.SupportedProviders) == 0 {
		return fmt.Errorf("agent %q: must have at least one supported provider", a.Name)
	}
	for _, pid := range a.SupportedProviders {
		if c.ProviderByID(pid) == nil {
			return fmt.Errorf("agent %q: provider %q does not exist", a.Name, pid)
		}
	}
	if a.DefaultProvider != "" {
		found := false
		for _, pid := range a.SupportedProviders {
			if pid == a.DefaultProvider {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("agent %q: default provider %q not in supported_providers", a.Name, a.DefaultProvider)
		}
	}
	// Rename check: if oldName differs, ensure new name is not taken.
	if oldName != "" && oldName != a.Name {
		if _, err := c.AgentByName(a.Name); err == nil {
			return fmt.Errorf("agent %q already exists", a.Name)
		}
	}
	for i := range c.Agents {
		if c.Agents[i].Name == oldName {
			c.Agents[i] = a
			return nil
		}
	}
	c.Agents = append(c.Agents, a)
	return nil
}

// DeleteAgent removes the agent named name.
func (c *Config) DeleteAgent(name string) {
	for i := range c.Agents {
		if c.Agents[i].Name == name {
			c.Agents = append(c.Agents[:i], c.Agents[i+1:]...)
			return
		}
	}
}

// WriteFileAtomic writes data to path atomically via a temp file + rename,
// creating the parent directory if needed.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Save writes cfg to the config path using an atomic temp-file + rename.
// Only wt-owned fields are persisted: Providers/Models live in
// modelman-owned registry.toml and are never written by wt.
func Save(cfg *Config) error {
	trimmed := *cfg
	trimmed.Providers = nil
	trimmed.Models = nil
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(&trimmed); err != nil {
		return err
	}
	return WriteFileAtomic(Path(), buf.Bytes(), 0o644)
}

// ModelsForAgent returns the models whose ProviderID is in the named
// agent's supported_providers list. Order matches cfg.Models.
//
// Errors:
//   - agent not found in cfg.Agents
//   - agent references a provider not in cfg.Providers (only reachable
//     if Validate was bypassed)
func (c *Config) ModelsForAgent(agentName string) ([]Model, error) {
	a, err := c.AgentByName(agentName)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, pid := range a.SupportedProviders {
		allowed[pid] = true
	}
	var out []Model
	for _, m := range c.Models {
		if allowed[m.ProviderID] {
			out = append(out, m)
		}
	}
	return out, nil
}

// parseFilterList splits a comma-delimited string, trimming whitespace and
// dropping empty entries. Empty or whitespace-only input returns nil.
// Used by the -T/--tags and -F/--family CLI flags.
func parseFilterList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseFilterList is the exported form of parseFilterList, used by callers
// outside the config package (e.g. cmd/wt/launch.go). It trims whitespace,
// drops empty entries, and returns nil for empty/whitespace-only input.
func ParseFilterList(s string) []string { return parseFilterList(s) }

// TagsToString joins a tag slice into a comma-delimited display string.
// Returns "" for nil or empty slices.
func TagsToString(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ", ")
}

// FirstTag returns the first comma-delimited tag from s, or fallback if s is
// empty. It is the shared form of the rotation slot's tag component: both the
// non-TUI launch path and the TUI picker derive the slot tag from -T the same
// way.
func FirstTag(s, fallback string) string {
	parts := ParseFilterList(s)
	if len(parts) == 0 {
		return fallback
	}
	return parts[0]
}

// EligibleModels returns the models usable by agent after applying tag
// and family filters. Order matches cfg.Models.
//
// Semantics:
//   - Provider filter is hard: only models whose ProviderID is in
//     agent.SupportedProviders are considered.
//   - tags == "" → no tag filter.
//   - tags != "" → model must have at least one matching tag.
//   - family == "" → no family filter.
//   - family != "" → model.Family must equal one of the listed families.
//   - When both are non-empty: tags and family are AND-combined.
//
// Errors:
//   - agent not found
func (c *Config) EligibleModels(agentName, tags, family string) ([]Model, error) {
	ms, err := c.ModelsForAgent(agentName)
	if err != nil {
		return nil, err
	}
	tagSet := map[string]bool{}
	for _, t := range parseFilterList(tags) {
		tagSet[t] = true
	}
	familySet := map[string]bool{}
	for _, f := range parseFilterList(family) {
		familySet[f] = true
	}
	var out []Model
	for _, m := range ms {
		if len(tagSet) > 0 {
			hit := false
			for _, t := range m.Tags {
				if tagSet[t] {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if len(familySet) > 0 && !familySet[m.Family] {
			continue
		}
		if !c.IsExposed(m) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
