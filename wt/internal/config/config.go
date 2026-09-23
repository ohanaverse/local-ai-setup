package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

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

// ── LiteLLM routing state (wt-owned) ─────────────────────

// UpdateLitellm applies mutate to the LiteLLM routing state and persists it to
// wt's config.toml. wt owns this state (moved from modelman.toml 2026-09-21).
func (c *Config) UpdateLitellm(mutate func(*LitellmState)) error {
	prev, prevTable := c.litellm, c.LitellmTable
	s := c.litellm
	mutate(&s)
	c.litellm = s
	c.LitellmTable = &s
	if err := Save(c); err != nil {
		// Nothing persisted: keep memory in step with disk so later routing
		// in this process does not act on a state that was never saved.
		c.litellm, c.LitellmTable = prev, prevTable
		return err
	}
	return nil
}

// LitellmConfigured reports whether both the proxy URL and API key are set.
func (c *Config) LitellmConfigured() bool { return c.litellm.URL != "" && c.litellm.APIKey != "" }

// IsLitellm reports whether non-native models route through the LiteLLM
// proxy, per the wt-owned [litellm].enabled. wt
// never starts, stops, or restarts the proxy — this is routing policy only.
func (c *Config) IsLitellm() bool { return c.litellm.Enabled }

// IsDirect reports whether agents dial providers directly where the
// agent×provider protocol overlap allows it.
func (c *Config) IsDirect() bool { return !c.litellm.Enabled }

// LitellmBaseURL returns the proxy URL with any trailing
// slashes removed. Drivers append their own protocol suffix (/v1, /v1/),
// so a user URL like "http://localhost:4000/" must not produce a double
// slash. TrimRight (not TrimSuffix) so a double-slash typo is also
// normalized.
func (c *Config) LitellmBaseURL() string { return strings.TrimRight(c.litellm.URL, "/") }

// LitellmAPIKey returns the proxy API key.
func (c *Config) LitellmAPIKey() string { return c.litellm.APIKey }

// SetLitellmForTest overrides the [litellm] routing state.
// Production wiring goes through finalizeCfg (config.toml / legacy fallback); tests in
// other packages cannot set the unexported field directly.
func (c *Config) SetLitellmForTest(s LitellmState) { c.litellm = s }

// ── Provider ──────────────────────────────────────────────

// ErrLitellmUnconfigured wraps a ResolveRoute failure caused specifically by
// litellm routing being required (forced by a protocol mismatch, or chosen
// via the on/off toggle) while wt's [litellm] url/api_key are
// unset. Callers (the model picker) use errors.Is against this sentinel to
// show a more specific "litellm required" label instead of a generic
// "unavailable" one, which would also cover unrelated failures like an
// unknown provider or a direct-mode provider missing auth.base_url.
var ErrLitellmUnconfigured = errors.New("litellm routing required but not configured")

// Route is everything a driver needs to dial one model for one launch,
// resolved once by ResolveRoute so drivers stop knowing about specific
// providers (e.g. ollama) or transports.
type Route struct {
	BaseOrigin string // scheme://host:port, no wire-path suffix
	APIKey     string
	ModelRef   string   // m.ID via the proxy, m.ModelName direct — a property of the endpoint's own catalog
	Display    string   // m.ModelName, for catalog "name" fields
	ProviderID string   // registry provider id; meaningful only when !Litellm
	Protocol   Protocol // chosen common protocol, meaningful only when !Litellm
	Litellm    bool
	Forced     bool // true if litellm was required regardless of the on/off setting (Task 5)
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
					"run 'wt litellm set --url ... --api-key ...' or 'wt litellm on': %w",
				ErrLitellmUnconfigured)
		}
		if c.LitellmAPIKey() == "" {
			return Route{}, fmt.Errorf(
				"litellm routing is required for this model but no API key is configured — "+
					"run 'wt litellm set --url ... --api-key ...': %w",
				ErrLitellmUnconfigured)
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
				"set one, or enable the proxy with 'wt litellm on'", providerID)
	}
	apiKey := ""
	if provider.Auth.SecretRef != "" {
		var err error
		apiKey, err = ResolveSecret(provider.Auth.SecretRef)
		if err != nil {
			return Route{}, fmt.Errorf("resolving credentials for provider %q: %w", providerID, err)
		}
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
	ID        string     `toml:"id"`
	Name      string     `toml:"name"`
	Location  Location   `toml:"location,omitempty"`
	Protocols []Protocol `toml:"protocols,omitempty"`
	Auth      AuthConfig `toml:"auth"`
	// ModelDir is the registry's model_dir (e.g. "~/.omlx/models"): where a
	// filesystem-backed provider keeps its models. Read-only; not expanded.
	ModelDir string `toml:"model_dir,omitempty"`
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

// ResolveSecret resolves a secret_ref-style value. Three forms:
//   - "exec:<path> [args...]" runs the command (no shell — split on
//     whitespace, so a path containing spaces is not supported) with a
//     execSecretTimeout deadline and returns its trimmed stdout; a
//     non-zero exit, a timeout, or a zero exit with empty stdout are all
//     errors (the latter because an exec: command claims success is not
//     the same contract as an os.environ/NAME ref, whose absence
//     legitimately resolves to ""). Memoized per raw ref for the lifetime
//     of the process — including across concurrent callers — so a ref
//     resolved from multiple call sites or goroutines in one launch
//     (ResolveRoute and pi's models.json sync both resolve the same
//     provider's secret_ref; the TUI can resolve routes for several table
//     rows concurrently) only runs the command once. Only a successful
//     resolution is memoized this way — a failure is not cached, so a
//     transient problem (e.g. not yet logged into Vault) can succeed on a
//     later call without restarting wt.
//   - "os.environ/NAME" or a bare "^[A-Z][A-Z0-9_]*$" name reads that env
//     var — unchanged: a missing var resolves to "", never an error.
//   - anything else is used verbatim, unchanged.
//
// Shared by direct-mode provider auth and LiteLLM's SecretRef:true policy
// api_key (internal/litellm.BuildEntry).
var envRefName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// execSecretTimeout bounds how long an exec: secret_ref command may run.
// This is the only unbounded-by-default subprocess wt would otherwise
// spawn; every other exec site (proxy restart, git fetch, lifecycle
// backends) already carries a deadline. A var (not const), following this
// package's seam convention (wt/CLAUDE.md's Go tests section — "a var x =
// realX plus a realX function"), so a test can shrink it instead of paying
// the real deadline in wall-clock time.
var execSecretTimeout = 15 * time.Second

// execSecretMu guards execSecretCache and execSecretInflight only long
// enough to read or write those two maps — never for the duration of a
// subprocess run. Holding one global lock across a whole runExecSecret call
// (the prior implementation) meant two DIFFERENT exec: refs could not
// resolve concurrently: a slow or hung helper for provider A would block an
// unrelated, already-fast provider B behind it. Deduping concurrent callers
// of the SAME ref onto one subprocess run (the memoization
// TestResolveSecretExecMemoizedConcurrent pins) is handled per-ref instead,
// via execSecretInflight.
var execSecretMu sync.Mutex

// execSecretCache holds only successful resolutions, for the process
// lifetime. A failed resolution is deliberately never cached: an exec:
// helper backed by e.g. Vault or 1Password can fail once for a transient
// reason (not yet logged in, a stale token) and succeed moments later, and
// wt's TUI is long-lived enough that caching the failure would force a full
// restart to recover.
var execSecretCache = map[string]string{}

// execSecretInflight tracks exec: resolutions currently running, one entry
// per ref, so concurrent callers for the same ref share a single subprocess
// run instead of each starting their own.
var execSecretInflight = map[string]*execSecretCall{}

// execSecretCall is one in-flight (or just-finished) exec: resolution.
// value/err are only written once, by the goroutine that created the
// entry, before done is closed — every other reader only touches them
// after receiving from done, so no further synchronization is needed.
type execSecretCall struct {
	done  chan struct{}
	value string
	err   error
}

func ResolveSecret(ref string) (string, error) {
	if cmdline, ok := strings.CutPrefix(ref, "exec:"); ok {
		return resolveExecSecret(ref, cmdline)
	}
	if name, ok := strings.CutPrefix(ref, "os.environ/"); ok {
		return os.Getenv(name), nil
	}
	if envRefName.MatchString(ref) {
		return os.Getenv(ref), nil
	}
	return ref, nil
}

// resolveExecSecret resolves one exec: ref. See execSecretMu/execSecretCache/
// execSecretInflight above for the concurrency and caching contract.
func resolveExecSecret(ref, cmdline string) (string, error) {
	execSecretMu.Lock()
	if v, ok := execSecretCache[ref]; ok {
		execSecretMu.Unlock()
		return v, nil
	}
	if call, ok := execSecretInflight[ref]; ok {
		execSecretMu.Unlock()
		<-call.done
		return call.value, call.err
	}
	call := &execSecretCall{done: make(chan struct{})}
	execSecretInflight[ref] = call
	execSecretMu.Unlock()

	value, err := runExecSecret(ref, cmdline)
	call.value, call.err = value, err
	close(call.done)

	execSecretMu.Lock()
	delete(execSecretInflight, ref)
	if err == nil {
		execSecretCache[ref] = value
	}
	execSecretMu.Unlock()

	return value, err
}

// runExecSecret runs the command line for a single exec: ref (already
// stripped of its "exec:" prefix) and returns its resolved value or error.
// Held behind execSecretMu by its only caller, ResolveSecret, so the whole
// run-and-cache sequence for one ref is atomic across concurrent callers.
func runExecSecret(ref, cmdline string) (string, error) {
	parts := strings.Fields(cmdline)
	if len(parts) == 0 {
		return "", fmt.Errorf("secret_ref %q: exec: form has no command", ref)
	}
	ctx, cancel := context.WithTimeout(context.Background(), execSecretTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	// WaitDelay bounds how long Wait (inside Output) waits for the child's
	// own I/O pipes to close after the context is done. Without it, a
	// script whose last command is a separate child process (e.g. a shell
	// script ending in `sleep 30`) leaves that grandchild holding the
	// stdout pipe open after the immediate child is killed, and Output
	// hangs until the grandchild itself exits — defeating the timeout.
	cmd.WaitDelay = 2 * time.Second
	out, runErr := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("secret_ref %q: timed out after %s", ref, execSecretTimeout)
	}
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("secret_ref %q: %s", ref, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("secret_ref %q: %w", ref, runErr)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return "", fmt.Errorf("secret_ref %q: command succeeded but produced no output", ref)
	}
	return value, nil
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
	// ModelInfo is the registry's free-form `[models.model_info]` table.
	// LiteLLM rows merge it over the derived pricing keys, so hand-written
	// keys (context windows, capability flags) reach the proxy.
	ModelInfo map[string]any `toml:"model_info,omitempty"`
	Native    bool           `toml:"-"` // derived: provider auth.type == "native"; not persisted
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
	DefaultTag string     `toml:"default_tag"`
	Providers  []Provider `toml:"providers"`
	Models     []Model    `toml:"models"`
	Agents     []Agent    `toml:"agents"`
	// LitellmTable is the wt-owned persisted [litellm] routing state.
	LitellmTable    *LitellmState            `toml:"litellm,omitempty"`
	migratedLitellm bool                     `toml:"-"`
	litellm         LitellmState             `toml:"-"` // runtime copy of LitellmTable (or the legacy fallback)
	exposed         map[string]ExposureEntry `toml:"-"` // from modelman.toml
	// wt decides which local models are running from the live inventory
	// (internal/localmodels), never from modelman's per-model `running` flag.
	// The flag-parsing fields that used to live here were removed when the
	// last caller (internal/localgate) was deleted; modelman still owns the
	// key, wt simply does not read it. See
	// docs/superpowers/specs/2026-09-20-wt-live-resolution-design.md.
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
// import them. Returns an empty Config if config.toml does not exist yet. A
// missing registry (ErrRegistryMissing) still returns an error, but the
// returned Config is not nil: it carries whatever config.toml already
// parsed (Agents/DefaultTag), just with an empty model catalog, so a
// genuinely configured agent isn't misread as unconfigured by callers like
// agents.IsConfigured. A malformed config.toml/registry.toml returns nil.
func Load() (*Config, error) {
	if _, err := Migrate(); err != nil {
		return nil, fmt.Errorf("migration: %w", err)
	}

	cfg := &Config{DefaultTag: "code"}
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		providers, models, err := loadRegistry()
		if err != nil {
			return cfgOrNilOnMissingRegistry(cfg, err), err
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
		// cfg already carries the Agents/DefaultTag decoded from config.toml
		// above — a missing registry must not throw that away. A genuinely
		// configured model-driven agent needs cfg.Agents intact so
		// agents.IsConfigured keeps reporting it as configured (and its
		// launch fails loud on the empty model catalog below) instead of
		// silently reading as unconfigured and falling back to the
		// unconfigured-agent passthrough (issue #147's fail-open edge case).
		return cfgOrNilOnMissingRegistry(cfg, err), err
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
	c, err := finalizeCfg(cfg, providers, models)
	if err != nil {
		return nil, err
	}
	if c.migratedLitellm {
		if err := Save(c); err != nil {
			fmt.Fprintf(os.Stderr, "wt: could not persist [litellm] to config.toml: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "wt: moved [litellm] routing state from modelman.toml into wt's config.toml")
		}
		c.migratedLitellm = false
	}
	return c, nil
}

// cfgOrNilOnMissingRegistry returns cfg (whatever Load has decoded from
// config.toml so far — Agents/DefaultTag, no Providers/Models) when err
// wraps ErrRegistryMissing, so a caller like agents.IsConfigured can still
// see real agent entries. Any other registry error (a genuine parse
// failure) returns nil, matching Load's existing fail-closed behavior for
// malformed config/registry data.
func cfgOrNilOnMissingRegistry(cfg *Config, err error) *Config {
	if errors.Is(err, ErrRegistryMissing) {
		return cfg
	}
	return nil
}

// finalizeCfg joins registry providers/models into cfg, derives native-ness
// from provider auth types, and loads modelman exposure state. Callers must
// already have loaded config.toml and registry.toml.
func finalizeCfg(cfg *Config, providers []Provider, models []Model) (*Config, error) {
	cfg.Providers, cfg.Models = providers, models
	deriveNative(cfg)
	exposed, legacy, err := loadModelmanState()
	if err != nil {
		return nil, err
	}
	cfg.exposed = exposed
	switch {
	case cfg.LitellmTable != nil:
		cfg.litellm = *cfg.LitellmTable
	case legacy != nil:
		cfg.litellm = *legacy
		cfg.LitellmTable = legacy
		cfg.migratedLitellm = true
	}
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

	// Note: no semantic validation of the [litellm] routing state here. A
	// malformed [litellm] table in wt's own config.toml already fails the
	// config load like any malformed config.toml; a missing or empty URL/key
	// only surfaces when ResolveRoute actually needs it at launch time.

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
		if m.ModelName == "" {
			// The lifecycle engine matches a start target against a provider's
			// reported names by this field; an empty one matches nothing, so the
			// model would look permanently stopped and warm the empty name.
			errs = append(errs, fmt.Errorf("model %q: model_name is required (add model_name to this registry.toml entry)", m.ID))
		}
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

// DiscoveredModelID is the usage/survey id for a local model found on disk
// with no registry entry: "<provider>/<artifact name as the provider lists
// it>". Registry-matched models keep their registry id instead; callers do
// that lookup before falling back to this.
func DiscoveredModelID(providerID, artifactName string) string {
	return providerID + "/" + artifactName
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

// IndexModelByID returns the index of the model with the given registry id
// in models, or -1 when absent. One lookup helper for every consumer of
// model ids (cmd/wt, internal/tui) instead of a per-package copy.
func IndexModelByID(models []Model, id string) int {
	for i := range models {
		if models[i].ID == id {
			return i
		}
	}
	return -1
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
//
// Native models are always exposed (they cannot route through LiteLLM).
//
// Local models are always exposed at this Stage-1 check (2026-09-15
// local-model visibility design); their real visibility is decided
// downstream by the live model inventory (internal/localmodels) through
// internal/catalog's row rules — not by the exposed/ready flags in
// modelman.toml. modelman's start_local_model
// keeps `exposed` in sync on start so LiteLLM-forced routes still work —
// see docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md.
//
// Cloud (and any model whose location cannot be resolved — a registry
// data gap, treated conservatively as non-local) requires exposed AND
// (ready OR cloud location), unchanged from before. The cloud-location
// check uses ResolveLocation: a model may omit its own `location` and
// inherit it from the provider.
func (c *Config) IsExposed(m Model) bool {
	if m.Native {
		return true
	}
	loc, locErr := c.ResolveLocation(m)
	if locErr == nil && loc == LocationLocal {
		return true
	}
	st, ok := c.exposed[m.ID]
	if !ok || !st.Exposed {
		return false
	}
	if locErr == nil && loc == LocationCloud {
		return true
	}
	return st.Ready
}

// ExposedFlag reports modelman's raw `exposed` flag for the model id (legacy
// litellm_exposed ORed in at load). Unlike IsExposed it never treats local
// models as always exposed, so it is what a table mirroring modelman's EXPOSED
// column should read.
func (c *Config) ExposedFlag(id string) bool {
	st, ok := c.exposed[id]
	return ok && st.Exposed
}

// ReadyFlag reports modelman's `ready` flag for the model id (legacy
// `downloaded` ORed in at load). The LiteLLM expose gate uses it: non-cloud
// models must be ready before they may be routed.
func (c *Config) ReadyFlag(id string) bool {
	st, ok := c.exposed[id]
	return ok && st.Ready
}

// SetExposureForTest overrides one model's modelman exposure/ready state.
// Production wiring goes through finalizeCfg (loadModelmanState); tests in
// other packages cannot set the unexported map directly.
func (c *Config) SetExposureForTest(id string, e ExposureEntry) {
	if c.exposed == nil {
		c.exposed = map[string]ExposureEntry{}
	}
	c.exposed[id] = e
}

// AgentSupportsProvider reports whether the named agent lists providerID in
// supported_providers. An unknown agent supports nothing.
func (c *Config) AgentSupportsProvider(agentName, providerID string) bool {
	a, err := c.AgentByName(agentName)
	if err != nil {
		return false
	}
	for _, pid := range a.SupportedProviders {
		if pid == providerID {
			return true
		}
	}
	return false
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

// WriteFileAtomic writes data to path atomically via a unique temp file in the
// same directory + rename, creating the parent directory if needed. The temp
// name is unique per call so concurrent writers never share (and truncate)
// one another's temp file; the loser of the rename race simply loses.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Best-effort cleanup so a failed write/chmod/rename never leaves a
	// (possibly secret-bearing) temp file behind; after a successful rename
	// the name no longer exists and the error is ignored.
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// CreateTemp makes the file 0600; set the requested mode explicitly.
	if err := os.Chmod(tmp, perm); err != nil {
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
	mode := os.FileMode(0o644)
	if cfg.LitellmTable != nil && cfg.LitellmTable.APIKey != "" {
		mode = 0o600
	}
	return WriteFileAtomic(Path(), buf.Bytes(), mode)
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
