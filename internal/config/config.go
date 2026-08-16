package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
}

// ── Agent ─────────────────────────────────────────────────

// Agent is a supported AI coding tool.
type Agent struct {
	Name               string   `toml:"name"`
	SupportedProviders []string `toml:"supported_providers"`        // hard constraint
	DefaultProvider    string   `toml:"default_provider,omitempty"` // optional
}

// ── Config ────────────────────────────────────────────────

// Config is the on-disk configuration for wt.
type Config struct {
	DefaultTag string     `toml:"default_tag"`
	Providers  []Provider `toml:"providers"`
	Models     []Model    `toml:"models"`
	Agents     []Agent    `toml:"agents"`
}

// Path returns the config file location, honoring XDG_CONFIG_HOME like wt-core.sh
func Path() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agent-wt", "config.toml")
}

// Load reads the config file at Path(). Runs legacy migration first if needed.
// Returns an empty Config if the file does not exist yet.
func Load() (*Config, error) {
	if _, err := Migrate(); err != nil {
		return nil, fmt.Errorf("migration: %w", err)
	}

	cfg := &Config{DefaultTag: "code"}
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Validate returns an error describing the first invalid entry.
func (c *Config) Validate() error {
	if c.DefaultTag == "" {
		return fmt.Errorf("default_tag must not be empty")
	}

	// Providers
	provIDs := map[string]bool{}
	for _, p := range c.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider entry with empty id")
		}
		if provIDs[p.ID] {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		provIDs[p.ID] = true
	}

	// Models
	modelIDs := map[string]bool{}
	for _, m := range c.Models {
		if m.ID == "" {
			return fmt.Errorf("model entry with empty id")
		}
		if modelIDs[m.ID] {
			return fmt.Errorf("duplicate model id %q", m.ID)
		}
		modelIDs[m.ID] = true
		if !provIDs[m.ProviderID] {
			return fmt.Errorf("model %q: unknown provider %q", m.ID, m.ProviderID)
		}
		// Location must be resolvable
		if _, err := c.ResolveLocation(m); err != nil {
			return err
		}
	}

	// Agents
	agentNames := map[string]bool{}
	for _, a := range c.Agents {
		if a.Name == "" {
			return fmt.Errorf("agent entry with empty name")
		}
		if agentNames[a.Name] {
			return fmt.Errorf("duplicate agent name %q", a.Name)
		}
		agentNames[a.Name] = true
		for _, pid := range a.SupportedProviders {
			if !provIDs[pid] {
				return fmt.Errorf("agent %q: unknown provider %q", a.Name, pid)
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
	}

	return nil
}

// ValidateAll reports every validation problem at once using errors.Join.
func (c *Config) ValidateAll() error {
	var errs []error
	if c.DefaultTag == "" {
		errs = append(errs, fmt.Errorf("default_tag must not be empty"))
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
		} else {
			if _, err := c.ResolveLocation(m); err != nil {
				errs = append(errs, err)
			}
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

	return errors.Join(errs...)
}

// IsNative reports whether this model is an agent's native model
// (e.g. "claude/native"), as opposed to a provider-hosted model.
func (m Model) IsNative() bool {
	return m.ModelName == "native"
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

// ProviderByID returns the provider with the given id, or nil if not found.
func (c *Config) ProviderByID(id string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].ID == id {
			return &c.Providers[i]
		}
	}
	return nil
}

// ModelsByFamily returns models whose Family matches the given family.
// An empty family matches models that also have an empty family.
func (c *Config) ModelsByFamily(family string) []Model {
	var out []Model
	for _, m := range c.Models {
		if m.Family == family {
			out = append(out, m)
		}
	}
	return out
}

// ModelsByProvider returns models from the given provider.
func (c *Config) ModelsByProvider(providerID string) []Model {
	var out []Model
	for _, m := range c.Models {
		if m.ProviderID == providerID {
			out = append(out, m)
		}
	}
	return out
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

// ProvidersForAgent returns the providers supported by the named agent.
func (c *Config) ProvidersForAgent(agentName string) ([]Provider, error) {
	a, err := c.AgentByName(agentName)
	if err != nil {
		return nil, err
	}
	var out []Provider
	for _, pid := range a.SupportedProviders {
		p := c.ProviderByID(pid)
		if p == nil {
			return nil, fmt.Errorf("agent %q: provider %q not found in config", agentName, pid)
		}
		out = append(out, *p)
	}
	return out, nil
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

// Save writes cfg to the config path using an atomic temp-file + rename.
func Save(cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}
