package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// OllamaBaseURL is the base address of the local Ollama gateway. Agent
// drivers (internal/agents) derive their full wire URLs from it via the
// OllamaURLer capability, adding the per-agent path suffix ("/", "/v1",
// "/v1/") their wire protocol expects; legacy migration writes it as the
// ollama provider's auth base URL.
const OllamaBaseURL = "http://localhost:11434"

// ── LiteLLM routing state (modelman-sourced) ─────────────

// IsLitellm reports whether non-native models route through the LiteLLM
// proxy, sourced read-only from modelman.toml's [litellm].enabled. wt
// never starts, stops, or restarts the proxy — this is routing policy only.
func (c *Config) IsLitellm() bool { return c.litellm.Enabled }

// IsDirect reports whether agents dial providers directly where the
// agent×provider protocol overlap allows it.
func (c *Config) IsDirect() bool { return !c.litellm.Enabled }

// LitellmBaseURL returns the modelman-sourced proxy URL with any trailing
// slashes removed. Drivers append their own protocol suffix (/v1, /v1/),
// so a user URL like "http://localhost:4000/" must not produce a double
// slash. TrimRight (not TrimSuffix) so a double-slash typo is also
// normalized.
func (c *Config) LitellmBaseURL() string { return strings.TrimRight(c.litellm.URL, "/") }

// LitellmAPIKey returns the modelman-sourced proxy API key.
func (c *Config) LitellmAPIKey() string { return c.litellm.APIKey }

// SetLitellmForTest overrides the modelman-sourced [litellm] routing state.
// Production wiring goes through finalizeCfg (loadModelmanState); tests in
// other packages cannot set the unexported field directly.
func (c *Config) SetLitellmForTest(s LitellmState) { c.litellm = s }

// ── Provider ──────────────────────────────────────────────

// Route is everything a driver needs to dial one model for one launch,
// resolved once by ResolveRoute so drivers stop knowing about specific
// providers (e.g. ollama) or transports.
type Route struct {
	BaseOrigin string   // scheme://host:port, no wire-path suffix
	APIKey     string
	ModelRef   string   // m.ID via the proxy, m.ModelName direct — a property of the endpoint's own catalog
	Display    string   // m.ModelName, for catalog "name" fields
	ProviderID string   // registry provider id; meaningful only when !Litellm
	Protocol   Protocol // chosen common protocol, meaningful only when !Litellm
	Litellm    bool
	Forced     bool     // true if litellm was required regardless of the on/off setting (Task 5)
}

// providerByID returns the provider with the given id and whether it was found.
func (c *Config) providerByID(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// ResolveRoute resolves the Route for launching model m. In direct mode it
// dials the model's own provider (auth.base_url, resolved through
// BaseOrigin, and auth.secret_ref resolved through ResolveSecret), using
// the provider-side model name (m.ModelName). In litellm mode it routes
// through the configured LiteLLM gateway using the full registry id.
// ResolveRoute resolves the Route for launching model m for an agent that
// speaks agentProtocols. It first checks protocol overlap: an empty
// intersection forces LiteLLM regardless of the on/off toggle. Otherwise,
// if LiteLLM is on the route goes through the gateway, and if it is off
// the route dials the provider directly using auth.base_url and
// auth.secret_ref. Returns an error when direct mode is required (forced or
// chosen) but the provider has no base_url or LiteLLM is forced but
// unconfigured.
func (c *Config) ResolveRoute(m Model, agentProtocols []Protocol) (Route, error) {
	if m.Native {
		return Route{}, nil // drivers never call this for native models
	}
	// Command agents (e.g. shell) and some test seams pass an empty model.
	// There is no provider endpoint to resolve in that case.
	if m.ID == "" && m.ProviderID == "" {
		return Route{}, nil
	}

	providerID := m.ProviderID
	if providerID == "" && strings.Contains(m.ID, "/") {
		providerID = strings.SplitN(m.ID, "/", 2)[0]
	}
	provider, ok := c.providerByID(providerID)
	if !ok {
		return Route{}, fmt.Errorf("unknown provider %q for model %q", providerID, m.ID)
	}
	// Native providers never route through a gateway; this mirrors deriveNative
	// and keeps manually-constructed test configs from needing the Native bit
	// set on every native model.
	if provider.Auth.Type == "native" {
		return Route{}, nil
	}

	common := intersectProtocols(agentProtocols, provider.EffectiveProtocols())
	forced := len(agentProtocols) > 0 && len(common) == 0
	useLitellm := forced || c.IsLitellm()

	if useLitellm {
		if c.LitellmBaseURL() == "" {
			return Route{}, fmt.Errorf(
				"litellm routing is required for this model but no URL is configured — "+
					"run 'modelman litellm set --url ... --api-key ...' or 'modelman litellm on'")
		}
		if c.LitellmAPIKey() == "" {
			return Route{}, fmt.Errorf(
				"litellm routing is required for this model but no API key is configured — "+
					"run 'modelman litellm set --url ... --api-key ...'")
		}
		return Route{
			BaseOrigin: c.LitellmBaseURL(),
			APIKey:     c.LitellmAPIKey(),
			ModelRef:   m.ID,
			Display:    m.ModelName,
			ProviderID: providerID,
			Litellm:    true,
			Forced:     forced,
		}, nil
	}

	if provider.Auth.BaseURL == "" {
		return Route{}, fmt.Errorf(
			"direct routing: provider %q has no auth.base_url in registry.toml — "+
				"set one, or enable the proxy with 'modelman litellm on'", providerID)
	}
	apiKey := ""
	if provider.Auth.SecretRef != "" {
		apiKey = ResolveSecret(provider.Auth.SecretRef)
	}
	protocol := ProtocolOpenAIChat
	if len(common) > 0 {
		protocol = common[0]
	}
	return Route{
		BaseOrigin: BaseOrigin(provider.Auth.BaseURL),
		APIKey:     apiKey,
		ModelRef:   m.ModelName,
		Display:    m.ModelName,
		ProviderID: providerID,
		Protocol:   protocol,
		Litellm:    false,
	}, nil
}

// intersectProtocols returns the protocol values common to a and b, ordered
// by a's preference.
func intersectProtocols(a, b []Protocol) []Protocol {
	set := make(map[Protocol]bool, len(b))
	for _, p := range b {
		set[p] = true
	}
	var out []Protocol
	for _, p := range a {
		if set[p] {
			out = append(out, p)
		}
	}
	return out
}

// Provider is a source of models with connection info.
type Provider struct {
	ID       string     `toml:"id"`
	Name     string     `toml:"name"`
	Location Location   `toml:"location,omitempty"`
	Protocols []Protocol `toml:"protocols,omitempty"`
	Auth     AuthConfig `toml:"auth"`
}

// EffectiveProtocols returns the provider's declared protocols, defaulting
// to openai-chat when the registry entry predates this field.
func (p Provider) EffectiveProtocols() []Protocol {
	if len(p.Protocols) == 0 {
		return []Protocol{ProtocolOpenAIChat}
	}
	return p.Protocols
}

// BaseOrigin normalizes a stored base_url to a bare origin (no /v1
// suffix). Mirrors Python's registry.base_origin — the two must agree.
func BaseOrigin(url string) string {
	trimmed := strings.TrimRight(url, "/")
	trimmed = strings.TrimSuffix(trimmed, "/v1")
	return strings.TrimRight(trimmed, "/")
}

// ResolveSecret resolves a secret_ref-style value: "os.environ/NAME" or a
// bare "^[A-Z][A-Z0-9_]*$" name reads that env var; anything else is used
// verbatim. Shared by direct-mode provider auth and [litellm].api_key.
var envRefName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func ResolveSecret(ref string) string {
	if name, ok := strings.CutPrefix(ref, "os.environ/"); ok {
		return os.Getenv(name)
	}
	if envRefName.MatchString(ref) {
		return os.Getenv(ref)
	}
	return ref
}

// AuthConfig describes how to authenticate with a provider.
type AuthConfig struct {
	Type      string `toml:"type"` // "none", "api_key", "oauth", "native"
	SecretRef string `toml:"secret_ref,omitempty"`
	BaseURL   string `toml:"base_url,omitempty"`
}

// ── Model ─────────────────────────────────────────────────

// ModelCost holds optional per-token and subscription pricing for a model.
type ModelCost struct {
	InputPricePerMillion  *float64 `toml:"input_price_per_million,omitempty"`
	CachePricePerMillion  *float64 `toml:"cache_price_per_million,omitempty"`
	OutputPricePerMillion *float64 `toml:"output_price_per_million,omitempty"`
	SubscriptionPrice     *float64 `toml:"subscription_price,omitempty"`
	SubscriptionPeriod    string   `toml:"subscription_period,omitempty"`
}

// Model is a specific variant of a base model available from a provider.
type Model struct {
	ID         string    `toml:"id"`          // unique key, e.g. "ollama/gemma4:9b"
	Family     string    `toml:"family"`      // base model grouping, e.g. "gemma4"
	ProviderID string    `toml:"provider_id"` // → Provider.ID
	ModelName  string    `toml:"model_name"`  // provider-specific name, e.g. "gemma4:9b"
	Location   Location  `toml:"location,omitempty"`
	Tags       []string  `toml:"tags"`             // e.g. ["code", "design"]
	Source     Source    `toml:"source,omitempty"` // curated or discovered
	Cost       ModelCost `toml:"cost,omitempty"`
	Native     bool      `toml:"-"`                // derived: provider auth.type == "native"; not persisted
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
	Providers  []Provider      `toml:"providers"`
	Models     []Model         `toml:"models"`
	Agents     []Agent         `toml:"agents"`
	litellm    LitellmState   `toml:"-"` // from modelman.toml
	exposed    map[string]ExposureEntry `toml:"-"` // from modelman.toml
	// localRunning is the raw [local].running_model marker from
	// modelman.toml; localGateActive is true only for a Config built by
	// Load() (see finalizeCfg) — a hand-built Config{} literal (nearly
	// every pre-issue-#65 test) leaves it false, so the gate this field
	// pair drives is a no-op for those tests unless they explicitly opt
	// in via SetLocalRunningForTest.
	localRunning    string `toml:"-"`
	localGateActive bool   `toml:"-"`
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
	exposed, litellm, localRunning, err := loadModelmanState()
	if err != nil {
		return nil, err
	}
	cfg.exposed = exposed
	cfg.litellm = litellm
	cfg.localRunning = localRunning
	cfg.localGateActive = true
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

	// Note: no validation of the [litellm] routing state here — it is sourced
	// from modelman-owned modelman.toml, which wt cannot repair (Global
	// Constraints: wt never fails closed on modelman.toml). A bad value only
	// surfaces when ResolveRoute actually needs it at launch time.

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
// Non-native models require exposed AND (ready OR cloud location).
//
// The cloud-location check uses ResolveLocation: a model may omit its own
// `location` and inherit it from the provider. validate() already guarantees
// the location is resolvable, so an error here is treated as non-cloud.
func (c *Config) IsExposed(m Model) bool {
	if m.Native {
		return true
	}
	st, ok := c.exposed[m.ID]
	if !ok || !st.Exposed {
		return false
	}
	if loc, err := c.ResolveLocation(m); err == nil && loc == "cloud" {
		return true
	}
	return st.Ready
}

// FilterToRunningLocal narrows models to those launchable under the
// one-local-model-at-a-time policy (issue #65): cloud and native models
// pass through unchanged; a local model is kept only when its id equals
// runningLocalID. A no-op (models returned unchanged) when the gate is
// not active (LocalGateActive) — every pre-issue-#65 test that builds a
// Config{} literal directly is unaffected.
func (c *Config) FilterToRunningLocal(models []Model, runningLocalID string) []Model {
	if !c.localGateActive {
		return models
	}
	out := make([]Model, 0, len(models))
	for _, m := range models {
		if loc, err := c.ResolveLocation(m); err == nil && loc == LocationLocal && m.ID != runningLocalID {
			continue
		}
		out = append(out, m)
	}
	return out
}

// SetExposedForTest replaces the in-memory exposed set. Tests only.
func (c *Config) SetExposedForTest(exposed map[string]ExposureEntry) {
	c.exposed = exposed
}

// ExposeAllForTest marks every non-native model in cfg as exposed and ready.
// Tests only.
func (c *Config) ExposeAllForTest() {
	if c.exposed == nil {
		c.exposed = make(map[string]ExposureEntry)
	}
	for _, m := range c.Models {
		if !m.Native {
			c.exposed[m.ID] = ExposureEntry{Exposed: true, Ready: true}
		}
	}
}

// LocalRunningModel returns the raw `[local].running_model` marker from
// modelman.toml — the registry id of the local model `modelman start` last
// started, or "" if none (see docs/superpowers/specs/2026-09-10-one-local-
// model-at-a-time-design.md). This is the unverified marker; a caller that
// needs to know whether the marked model is actually serving right now
// should probe it (internal/localgate.Resolve).
func (c *Config) LocalRunningModel() string { return c.localRunning }

// LocalGateActive reports whether the one-local-model-at-a-time gate
// (FilterToRunningLocal, and the -M pin checks in cmd/wt/resolve.go and
// internal/tui) is live for this Config. True only for a Config built by
// Load() (production). A hand-built Config{} literal — the shape nearly
// every pre-issue-#65 test uses — defaults to false, so the gate is a
// no-op for those tests unless they call SetLocalRunningForTest.
func (c *Config) LocalGateActive() bool { return c.localGateActive }

// SetLocalRunningForTest activates the one-local-model-at-a-time gate (as
// Load() would) and sets the running-model marker, as if finalizeCfg had
// read it from modelman.toml. runningModelID == "" simulates "gate active,
// no local model marked running" (every local model gets filtered out by
// FilterToRunningLocal). Tests only.
func (c *Config) SetLocalRunningForTest(runningModelID string) {
	c.localGateActive = true
	c.localRunning = runningModelID
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
	return c.EligibleModelsIn(agentName, nil, tags, family)
}

// EligibleModelsIn is the single full-catalog traversal shared by
// EligibleModels and the TUI model picker. It applies the tag/family
// filters to a caller-supplied full catalog (agentName's models) and returns
// the eligible slice — so a caller that needs both the full catalog (for
// cross-filter family count aggregation) and the eligible subset only walks
// the registry once. Order matches the provided catalog's order.
//
// See EligibleModels for the filter semantics.
func (c *Config) EligibleModelsIn(agentName string, ms []Model, tags, family string) ([]Model, error) {
	if ms == nil {
		var err error
		ms, err = c.ModelsForAgent(agentName)
		if err != nil {
			return nil, err
		}
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
