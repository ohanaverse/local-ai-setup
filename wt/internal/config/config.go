package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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

// LitellmState is the `[litellm]` table of wt's config.toml. It controls
// whether agents dial providers directly or route through the LiteLLM proxy.
// wt reads it from config.toml and nowhere else.
type LitellmState struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`
	APIKey  string `toml:"api_key"`
}

// UpdateLitellm applies mutate to the LiteLLM routing state and persists it to
// wt's config.toml. wt owns this state.
//
// It persists through PatchSave rather than a whole-file Save of c: c may be
// a long-lived process's stale in-memory snapshot (issue #143 — a `wt
// litellm` run against a Config loaded before a concurrent `wt config`
// editor session saved its own Agents/DefaultTag change). PatchSave re-reads
// config.toml fresh under the lock and touches only LitellmTable, so that
// concurrent change survives.
func (c *Config) UpdateLitellm(mutate func(*LitellmState)) error {
	prev, prevTable := c.litellm, c.LitellmTable
	var result LitellmState
	err := c.PatchSave(func(fresh *Config) {
		s := LitellmState{}
		if fresh.LitellmTable != nil {
			s = *fresh.LitellmTable
		}
		mutate(&s)
		fresh.LitellmTable = &s
		result = s
	})
	if err != nil {
		// Nothing persisted: keep memory in step with disk so later routing
		// in this process does not act on a state that was never saved.
		c.litellm, c.LitellmTable = prev, prevTable
		return err
	}
	c.litellm, c.LitellmTable = result, &result
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
// Production wiring goes through finalizeCfg (config.toml); tests in
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
// Provider.Origin, and auth.secret_ref resolved through ResolveSecret), using
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
		BaseOrigin: provider.Origin(),
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

// MtplxPort is the port wt serves mtplx on when the registry's mtplx
// base_url names none. mtplx is the one local server wt hands a port to
// (`mtplx serve --port`, `mtplx stop --port`); omlx and ollama listen where
// their own configuration says, and wt cannot start an mlx_lm_server.
const MtplxPort = 8003

// Origin is the address wt reaches provider p at: BaseOrigin of its
// auth.base_url, with the port filled in where the url names none and wt is
// what decides the port. That is one case only: the mtplx provider, an http
// url, a loopback host. `wt start` spawns `mtplx serve --port MtplxPort
// --host 127.0.0.1` for such a url, so that is where the server is, and a
// url read as written would mean port 80: the start succeeded and every
// probe after it found nothing there (#348).
//
// Every reader of the provider's address goes through here — the probes
// (localmodels.FamilyOrigin), a direct route (ResolveRoute, pi's models.json)
// and a LiteLLM route's api_base — so they name one server.
//
// Everything else is left as written, and a url with no port keeps its
// scheme's own (80, 443): a url that names a port; an https url or a host
// that is not this machine, which wt could not have started and where the
// implicit port is what the user wrote; and every other provider — wt never
// tells omlx or ollama a port (they listen where their own settings say), so
// there the address wt probes, the one a start waits on and the route's
// api_base already agree, and a default port would be a guess.
func (p Provider) Origin() string {
	origin := BaseOrigin(p.Auth.BaseURL)
	if p.ID != "mtplx" {
		return origin
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Port() != "" || !isLoopbackHost(u.Hostname()) {
		return origin
	}
	u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(MtplxPort))
	return u.String()
}

// isLoopbackHost reports whether host names this machine: "localhost", or an
// address in 127.0.0.0/8 or ::1.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// CostWindow is one [start, end) span in its TimePrice's timezone, starting
// on each of the listed weekdays. Days use "mon".."sun"; times are "HH:MM"
// and end may be "24:00". An end before the start is a window that runs past
// midnight: from start on a listed day to end on the next calendar day. A
// start equal to the end is refused (validateWindow).
type CostWindow struct {
	Days  []string `toml:"days"`
	Start string   `toml:"start"`
	End   string   `toml:"end"`
}

// TimePrice is a time-windowed override of a ModelCost's flat (default)
// per-token prices. The rows are written by `wt cloud-sync`: the ollama
// flow's row labelled off-peak (ollama's off-peak pricing, on ollama cloud
// entries) and the openrouter flow's rows labelled openrouter (the other
// levels of a model OpenRouter prices by time of day, whose flat price is
// the dearest level); a user may write more by hand. ModelCost.PriceAt
// (price_at.go) applies them, and the model picker shows what it returns.
// A LiteLLM route carries the flat prices. What a row means: the first row
// whose window contains an instant wins, per field, falling back to the
// flat prices. A row the registry's validator refuses is not applied
// (TimePrice.Problem).
type TimePrice struct {
	Label                 string       `toml:"label,omitempty"`
	Timezone              string       `toml:"timezone"`
	InputPricePerMillion  *float64     `toml:"input_price_per_million,omitempty"`
	CachePricePerMillion  *float64     `toml:"cache_price_per_million,omitempty"`
	OutputPricePerMillion *float64     `toml:"output_price_per_million,omitempty"`
	Windows               []CostWindow `toml:"windows"`
}

// ModelCost holds optional per-token and subscription pricing for a model.
type ModelCost struct {
	InputPricePerMillion  *float64    `toml:"input_price_per_million,omitempty"`
	CachePricePerMillion  *float64    `toml:"cache_price_per_million,omitempty"`
	OutputPricePerMillion *float64    `toml:"output_price_per_million,omitempty"`
	SubscriptionPrice     *float64    `toml:"subscription_price,omitempty"`
	SubscriptionPeriod    string      `toml:"subscription_period,omitempty"`
	TimePrices            []TimePrice `toml:"time_prices,omitempty"`
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
	// Fetch and Draft say where a local model's weights come from: the
	// registry's [models.fetch] table, and [models.draft] for the draft half
	// of an mlx_lm_server pairing. wt reads them here — to show a local_path
	// and to name the pairing in a hint; llmbench acts on them — and writes
	// them only when `wt model add` registers a pairing (modeladmin.Add). Nothing
	// encodes a Model back into the registry (RegistryDoc is patch-shaped),
	// so decoding two more keys cannot change what a write touches.
	// Neither can fail a load: a malformed one reads as absent
	// (ModelArtifact.UnmarshalTOML).
	Fetch ModelArtifact `toml:"fetch,omitempty"`
	Draft ModelArtifact `toml:"draft,omitempty"`
	// PricingUpdatedAt is the registry's pricing_updated_at, as written:
	// the string `wt cloud-sync` stamps, or a TOML date-time a
	// hand edit left. It is `any` so that neither spelling can fail the
	// load; read it through PricingUpdated.
	PricingUpdatedAt any  `toml:"pricing_updated_at,omitempty"`
	Native           bool `toml:"-"` // derived: provider auth.type == "native"; not persisted
}

// pricingStampLayouts are the spellings of pricing_updated_at wt reads: the
// stamp both tools write, then what a hand edit plausibly leaves (a
// date-time with no offset, a bare date; both taken as UTC).
var pricingStampLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"}

// PricingUpdated is when the model's token prices were last refreshed, and
// whether the registry says. A value that is not a time (a typo, a number)
// counts as not said.
func (m Model) PricingUpdated() (time.Time, bool) {
	switch v := m.PricingUpdatedAt.(type) {
	case time.Time:
		return v, true
	case string:
		for _, layout := range pricingStampLayouts {
			if t, err := time.Parse(layout, v); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// ModelArtifact is one side of a model's weights: a Hugging Face repo, or a
// directory the user produced (bin/mlx-quantize). The other keys a fetch
// table can hold (files, quantizations) are not decoded.
type ModelArtifact struct {
	Repo      string `toml:"repo,omitempty"`
	LocalPath string `toml:"local_path,omitempty"`
	// Malformed records what UnmarshalTOML tolerated while reading the
	// value. It is about how the file was written, not part of the value:
	// never encoded, and zero for a table that was well formed or absent.
	Malformed ArtifactFaults `toml:"-" json:"-"`
}

// ArtifactFaults is what was wrong with a fetch or draft value that the
// loader read as absent rather than fail on. It is comparable, so a
// ModelArtifact still is.
type ArtifactFaults struct {
	// NotTable: the value is not a table at all (a string, a number, an
	// array), so neither key was read.
	NotTable bool
	// Repo and LocalPath: that key is there and is not a string.
	Repo, LocalPath bool
}

// artifactFields is the shape of a fetch or draft table as wt reads it: the
// keys, each a string. The reader (UnmarshalTOML), the record of what it
// tolerated (ArtifactFaults) and the writer's check (validateArtifact) all go
// by this list, so they cannot disagree about what a well-formed table is.
var artifactFields = []struct {
	key   string
	field func(*ModelArtifact) *string
	fault func(*ArtifactFaults) *bool
}{
	{"repo", func(a *ModelArtifact) *string { return &a.Repo }, func(f *ArtifactFaults) *bool { return &f.Repo }},
	{"local_path", func(a *ModelArtifact) *string { return &a.LocalPath }, func(f *ArtifactFaults) *bool { return &f.LocalPath }},
}

// UnmarshalTOML reads a fetch or draft value and never fails. What wt reads
// of these two tables it only shows (a path, the two sides of a pairing), and
// registries are edited by hand too, so a slip in one must not stop every wt command, launches
// included (as a typed decode would: one bad key fails the whole registry).
// A value that is not a table reads as an empty artifact, and a repo or
// local_path that is not a string reads as absent while the other key is
// still read. An empty table is an empty artifact, and any other key (files,
// quantizations, one wt does not model) is ignored. What was tolerated is
// recorded in Malformed, which `wt model list` prints (Model.Malformed); every
// other command stays quiet. The registry writer names a malformed value too:
// validateArtifact refuses a row it touches.
func (a *ModelArtifact) UnmarshalTOML(v any) error {
	*a = ModelArtifact{}
	table, isTable := v.(map[string]any)
	a.Malformed.NotTable = !isTable
	for _, f := range artifactFields {
		fv, present := table[f.key]
		// A failed assertion leaves "", which is what absent reads as.
		s, isString := fv.(string)
		*f.field(a), *f.fault(&a.Malformed) = s, present && !isString
	}
	return nil
}

// problems names what was malformed in the value read from key ("fetch" or
// "draft"), in artifactFields' order; nil when nothing was.
func (a ModelArtifact) problems(key string) []string {
	if a.Malformed.NotTable {
		return []string{key + " is not a table"}
	}
	var out []string
	for _, f := range artifactFields {
		if *f.fault(&a.Malformed) {
			out = append(out, key+"."+f.key+" is not a string")
		}
	}
	return out
}

// Malformed names what the loader tolerated in this row instead of failing on
// it, as phrases for a person. In its fetch and draft: "fetch is not a
// table", "draft.local_path is not a string" (fetch before draft, repo
// before local_path); each reads as absent. Then each cost.time_prices row
// the registry's validator refuses (TimePrice.Problem), by its place in the
// file: "cost.time_prices[0]: windows[0]: start must be HH:MM, got 9:00";
// the model picker does not apply such a row. nil for a row with nothing
// malformed, and for a Model that was not read from a registry file.
func (m Model) Malformed() []string {
	out := append(m.Fetch.problems("fetch"), m.Draft.problems("draft")...)
	for i, row := range m.Cost.TimePrices {
		if problem := row.Problem(); problem != "" {
			out = append(out, fmt.Sprintf("cost.time_prices[%d]: %s", i, problem))
		}
	}
	return out
}

// Target is the artifact as a user names it on a command line: the local
// path when there is one, else the repo. "" when the table is empty.
func (a ModelArtifact) Target() string {
	if a.LocalPath != "" {
		return a.LocalPath
	}
	return a.Repo
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
// wt-owned config.toml; Providers + Models come from the shared
// registry.toml and are never persisted from a Config (see Save).
type Config struct {
	DefaultTag string     `toml:"default_tag"`
	Providers  []Provider `toml:"providers"`
	Models     []Model    `toml:"models"`
	Agents     []Agent    `toml:"agents"`
	// LitellmTable is the wt-owned persisted [litellm] routing state.
	LitellmTable *LitellmState `toml:"litellm,omitempty"`
	litellm      LitellmState  `toml:"-"` // runtime copy of LitellmTable
	// wt decides which local models are running from the live inventory
	// (internal/localmodels), never from a stored flag. See
	// docs/superpowers/specs/2026-09-20-wt-live-resolution-design.md.
}

// Dir returns the base config directory (~/.config/agent-wt, or
// $XDG_CONFIG_HOME/agent-wt), honoring XDG_CONFIG_HOME like wt-core.sh.
func Dir() string {
	return filepath.Join(baseConfigHome(), "agent-wt")
}

// baseConfigHome returns the XDG base config directory honoring
// XDG_CONFIG_HOME (with a leading "~" or "~/" expanded via expandHome,
// matching Python's Path.expanduser(), which llmbench uses). Falls back to
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
// the shared registry.toml (Providers + Models) into one in-memory
// Config. The registry is checked before any schema-migration save so a
// missing registry fails closed before wt rewrites config.toml: legacy
// provider/model sections survive on disk; nothing reads them, and a failed
// load is not what removes them. Returns an empty Config if config.toml does
// not exist yet. A missing registry (ErrRegistryMissing) still returns an
// error, but the returned Config is not nil: it carries whatever config.toml
// already parsed (Agents/DefaultTag), just with an empty model catalog, so a
// genuinely configured agent isn't misread as unconfigured by callers like
// agents.IsConfigured. A malformed config.toml/registry.toml returns nil.
func Load() (*Config, error) {
	if _, err := Migrate(); err != nil {
		return nil, fmt.Errorf("migration: %w", err)
	}

	cfg, exists, err := readConfigFile()
	if err != nil {
		return nil, err
	}
	if !exists {
		providers, models, err := loadRegistry()
		if err != nil {
			return cfgOrNilOnMissingRegistry(cfg, err), err
		}
		return finalizeCfg(cfg, providers, models), nil
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

	// migrateConfigSchema runs against a fresh, lock-protected read (via
	// lockedApply) rather than the unlocked cfg read above: loadRegistry and
	// the schema-migration probe both take real time, and a concurrent
	// PatchSave-based writer landing in that window must not be clobbered by
	// a stale whole-file save (issue #143 follow-up).
	fresh, changed, err := lockedApply(func(fresh *Config) (bool, error) {
		return migrateConfigSchema(fresh)
	})
	if err != nil {
		return nil, fmt.Errorf("schema migration: %w", err)
	}
	if changed {
		fmt.Fprintln(os.Stderr, "wt: migrated config to native-provider alignment (renamed google→agy, rewired opencode to ollama-only)")
	}

	// Join the registry LAST so wt never mutates registry data
	// in memory (schema fixups above only ever see wt-owned config.toml
	// content). Providers/Models from a pre-Phase-4 config.toml are
	// overwritten here — registry.toml is the source of truth.
	return finalizeCfg(fresh, providers, models), nil
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
// from provider auth types, and copies config.toml's [litellm] table, when it
// has one, into the runtime routing state. With no table, routing is off.
func finalizeCfg(cfg *Config, providers []Provider, models []Model) *Config {
	cfg.Providers, cfg.Models = providers, models
	deriveNative(cfg)
	if cfg.LitellmTable != nil {
		cfg.litellm = *cfg.LitellmTable
	}
	return cfg
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

	// Providers and models are registry.toml's rows, so every error in these
	// two loops is marked ErrRegistryEntry (or is an ErrLocation): `wt config`
	// cannot repair one, and RegistryFixHint names the file instead (#291).
	// An agent error below is config.toml's and stays unmarked.
	provIDs := map[string]bool{}
	for _, p := range c.Providers {
		if p.ID == "" {
			errs = append(errs, registryEntryError(fmt.Errorf("provider entry with empty id")))
		}
		if provIDs[p.ID] {
			errs = append(errs, registryEntryError(fmt.Errorf("duplicate provider id %q", p.ID)))
		}
		provIDs[p.ID] = true
		// A provider entry may leave its location out (its models then need
		// their own), but a value that is set must be a location — checked
		// here as well as per model, so an entry with no models, or one whose
		// models all override it, is still reported.
		if p.Location != "" && !p.Location.Valid() {
			errs = append(errs, providerLocationError(p))
		}
	}

	// Models
	modelIDs := map[string]bool{}
	for _, m := range c.Models {
		if m.ID == "" {
			errs = append(errs, registryEntryError(fmt.Errorf("model entry with empty id")))
		}
		if modelIDs[m.ID] {
			errs = append(errs, registryEntryError(fmt.Errorf("duplicate model id %q", m.ID)))
		}
		modelIDs[m.ID] = true
		if m.ModelName == "" {
			// The lifecycle engine matches a start target against a provider's
			// reported names by this field; an empty one matches nothing, so the
			// model would look permanently stopped and warm the empty name.
			// The text does not say where the entry is: RegistryFixHint does.
			errs = append(errs, registryEntryError(fmt.Errorf("model %q: model_name is required", m.ID)))
		}
		if !provIDs[m.ProviderID] {
			errs = append(errs, registryEntryError(fmt.Errorf("model %q: unknown provider %q", m.ID, m.ProviderID)))
		} else if _, err := c.ResolveLocation(m); err != nil {
			// Location must be resolvable. A mistyped one is ErrLocation
			// already; no location at all is marked here.
			errs = append(errs, registryEntryError(err))
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
// with no registry entry: "<family>/<artifact name as the provider lists
// it>". Registry-matched models keep their registry id instead; callers do
// that lookup before falling back to this.
func DiscoveredModelID(providerID, artifactName string) string {
	return discoveredFamily(providerID) + "/" + artifactName
}

// discoveredFamily maps a registry provider id to its probe family.
// Mirrors internal/localmodels.familyOf: omlx and omlx-6bit share "omlx";
// every other local provider is its own family.
func discoveredFamily(providerID string) string {
	if providerID == "omlx" || providerID == "omlx-6bit" {
		return "omlx"
	}
	return providerID
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

// InCatalog reports whether m appears in wt's model catalog (#179):
// configured means exposed. Native, local and cloud registry models are all
// in; the only exclusion is a registry data gap — a provider_id naming no
// provider, or no location on model or provider — which stays out,
// fail-closed, even when the model sets its own location.
func (c *Config) InCatalog(m Model) bool {
	if m.Native {
		return true
	}
	if c.ProviderByID(m.ProviderID) == nil {
		return false
	}
	_, err := c.ResolveLocation(m)
	return err == nil
}

// OpenRouterProvider is the provider_id whose models take their price from
// OpenRouter — the whole OpenRouterPriced rule. cloudsync aliases it, so the
// rule's two readers (over the typed config and over registry rows) cannot
// name different ids.
const OpenRouterProvider = "openrouter"

// OpenRouterPriced reports whether m's price comes from OpenRouter, which is
// what `wt cloud-sync`'s openrouter flow refreshes and what the stale-pricing
// notice watches: a model whose provider_id is "openrouter", and no other.
// cloudsync.OpenRouterPriced is the same rule over registry rows; both are
// pinned by docs/contracts/catalog-predicates.sample.toml.
//
// Until #322 a model of any non-native cloud provider counted too, and a
// provider's openrouter_priced key overrode the result. The key dates from
// when ollama's cloud models had no published prices; it is no longer read.
// A registry that still has it loads (the decoder ignores a key Provider
// does not model) and a write keeps it.
func (c *Config) OpenRouterPriced(m Model) bool {
	return m.ProviderID == OpenRouterProvider
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

// ErrLocation marks an error about a registry entry's location: none where
// one is needed is reported elsewhere, this is a value that is set and is not
// a location. Callers use it to point the user at registry.toml, which wt
// reads but `wt config` does not edit.
var ErrLocation = errors.New("invalid location")

// ErrRegistryEntry marks a validation error about a provider or model row of
// registry.toml that is not about its location: an empty or repeated id, a
// model with no model_name, a model whose provider has no row, a model with
// no location on itself or its provider. Like ErrLocation it is in a file
// `wt config` does not edit, and RegistryFixHint names that file. The marked
// error's text is unchanged.
var ErrRegistryEntry = errors.New("invalid registry entry")

type registryEntryErr struct{ err error }

func (e registryEntryErr) Error() string        { return e.err.Error() }
func (e registryEntryErr) Unwrap() error        { return e.err }
func (e registryEntryErr) Is(target error) bool { return target == ErrRegistryEntry }

// registryEntryError marks err as ErrRegistryEntry, keeping its text and
// whatever it wraps (a location error stays an ErrLocation).
func registryEntryError(err error) error { return registryEntryErr{err: err} }

// RegistryFixHint is the hint for a config error whose repair is in
// registry.toml — a file `wt config` cannot edit — or "" for
// any other error (and nil). That is a provider or model row that fails
// validation (ErrLocation, ErrRegistryEntry), a registry path that is a
// broken symlink (ErrRegistryLink), and a registry file wt cannot stat, read,
// parse or accept the top level of (ErrRegistryFile, ErrRegistryTopLevel):
// those last errors name the file, so the hint does not repeat it. It is the one source of the wording, so the commands that refuse
// to run on such an error and the editor that opens on it name the same file
// the same way (#209).
//
// A missing registry (ErrRegistryMissing) gets "" as well: its error already
// names the command that creates one. LoadFixHint is the function to ask for
// the whole hint; this one answers only "is the repair in the registry".
func RegistryFixHint(err error) string {
	switch {
	case errors.Is(err, ErrLocation), errors.Is(err, ErrRegistryEntry):
		return "fix the entry in " + RegistryPath()
	case errors.Is(err, ErrRegistryLink):
		return "fix the link or move it aside"
	case errors.Is(err, ErrRegistryFile), errors.Is(err, ErrRegistryTopLevel):
		return "fix that file by hand"
	}
	return ""
}

// RegistryFixHintFromAny checks every error in a joined error (from
// ValidateAll) for a registry problem and returns the first hint found.
// This ensures the correct repair location is shown even when a config.toml
// error appears first in the join (e.g., empty default_tag + missing
// model_name both present).
func RegistryFixHintFromAny(err error) string {
	if err == nil {
		return ""
	}
	// errors.Join returns an error that implements Unwrap() []error
	type unwrapper interface{ Unwrap() []error }
	if u, ok := err.(unwrapper); ok {
		for _, e := range u.Unwrap() {
			if hint := RegistryFixHint(e); hint != "" {
				return hint
			}
		}
	}
	// Also check the error itself in case it's not a joined error
	if hint := RegistryFixHint(err); hint != "" {
		return hint
	}
	return ""
}

// LoadFixHint is the repair to name after a config load or validation error,
// or "" when there is nothing to add (nil, and a missing registry, whose
// error already says to run `wt model init` — a hint would name a second
// repair beside it). A registry problem — the file, the link to it, or a
// provider or model row in it — gets RegistryFixHint's wording. Everything
// else (a config.toml that does not parse, default_tag, an agent entry) is
// taken to be in wt's own config.toml, the one file `wt config` edits, and
// only then is `wt config` the repair.
//
// Every command that refuses to run on such an error, and every note that
// quotes one, asks here, so the same error is never given two repairs (#291).
func LoadFixHint(err error) string {
	switch {
	case err == nil, errors.Is(err, ErrRegistryMissing):
		return ""
	}
	if hint := RegistryFixHint(err); hint != "" {
		return hint
	}
	return "run `wt config` to repair"
}

// Valid reports whether l is one of the two locations the registry defines.
// The registry is read by llmbench too, so any other value —
// a typo such as "Local", a word from some other scheme — is not interpreted:
// reading "Local" as local would paper over a file that llmbench reads
// differently (a location that is set and is not exactly "local" is not local
// to it).
func (l Location) Valid() bool { return l == LocationLocal || l == LocationCloud }

// ResolveLocation returns the effective location for a model.
// Model location takes precedence; falls back to provider location.
// Returns an error if neither is set, the provider is unknown, or the value
// that applies is not a location (#200).
//
// That last case is why callers can rely on the result: a non-nil error means
// "wt cannot say where this model runs", and every consumer treats it the same
// way — out of the catalog, out of the inventory, a gap for sync, a validation
// error. A mistyped value used to resolve without error, as itself, and each
// consumer then drew its own conclusion from a location that was neither local
// nor cloud: the model was offered, never routed, and failed at launch.
func (c *Config) ResolveLocation(m Model) (Location, error) {
	if m.Location != "" {
		if !m.Location.Valid() {
			return "", locationError(fmt.Sprintf(`model %q has location %q; expected "local" or "cloud"`, m.ID, string(m.Location)))
		}
		return m.Location, nil
	}
	p := c.ProviderByID(m.ProviderID)
	if p == nil {
		return "", fmt.Errorf("model %q: unknown provider %q", m.ID, m.ProviderID)
	}
	if p.Location != "" {
		if !p.Location.Valid() {
			return "", fmt.Errorf("model %q: %w", m.ID, providerLocationError(*p))
		}
		return p.Location, nil
	}
	return "", fmt.Errorf("model %q: no location on model or provider %q", m.ID, p.ID)
}

// providerLocationError is the error for a provider entry whose location is
// set but is not a location. One wording, for validation and for the models
// that inherit it.
func providerLocationError(p Provider) error {
	return locationError(fmt.Sprintf(`provider %q has location %q; expected "local" or "cloud"`, p.ID, string(p.Location)))
}

// locationErr carries a location error's own wording while matching
// ErrLocation, so the message stays exactly what the user should read.
type locationErr struct{ msg string }

func (e locationErr) Error() string        { return e.msg }
func (e locationErr) Is(target error) bool { return target == ErrLocation }

func locationError(msg string) error { return locationErr{msg: msg} }

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

// Save writes cfg to the config path using an atomic temp-file + rename,
// under WithLock so it can never interleave with a PatchSave's
// read-modify-write. Only wt-owned fields are persisted: Providers/Models
// live in registry.toml, which Save never writes (UpdateRegistry does).
//
// Prefer PatchSave (or, for Load's own self-persisting migrations,
// lockedApply) over Save whenever cfg may be a stale snapshot (loaded
// earlier, possibly in a different process) — Save writes cfg wholesale and
// so reverts any field a concurrent writer changed since cfg was loaded.
// Every production writer (the `wt config` editor, `wt litellm on|off|set`,
// Load's own migrations) now goes through one of those locked-fresh-read
// paths instead; Save remains as the simple whole-file primitive they build
// on and for tests/tools that construct and persist a Config in one step
// with no concurrent-writer risk.
func Save(cfg *Config) error {
	return WithLock(func() error { return writeConfigFile(cfg) })
}

// writeConfigFile is Save's encode-and-write step, factored out so
// PatchSave can reuse it after merging fresh's caller-owned fields.
func writeConfigFile(cfg *Config) error {
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

// readConfigFile reads and decodes config.toml's wt-owned fields only — no
// registry join, no schema migration side effects. A missing file is not an
// error: it returns a fresh zero-value Config (default_tag "code") and
// exists=false, mirroring the starting point Load uses on a first run. Load
// and PatchSave both call this now, so a schema/decode change can't drift
// between what Load sees on its initial read and what PatchSave's merge
// base decodes for the same file.
func readConfigFile() (cfg *Config, exists bool, err error) {
	cfg = &Config{DefaultTag: "code"}
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return cfg, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, false, fmt.Errorf("parse config: %w", err)
	}
	return cfg, true, nil
}

// PatchSave serializes a locked read-modify-write cycle on config.toml: it
// re-reads the file fresh from disk under WithLock, lets apply mutate only
// the fields the caller owns, and writes the merged result. A caller whose
// in-memory Config may be stale (loaded earlier, possibly by another
// process) can therefore persist its own change without reverting anything
// a concurrent writer changed in a field apply does not touch — see issue
// #143 (config.toml writes were whole-file last-writer-wins between the
// `wt config` editor and `wt litellm on|off|set`).
func (c *Config) PatchSave(apply func(fresh *Config)) error {
	return WithLock(func() error {
		fresh, _, err := readConfigFile()
		if err != nil {
			return err
		}
		apply(fresh)
		return writeConfigFile(fresh)
	})
}

// lockedApply is Load's version of PatchSave's re-read-fresh pattern for its
// self-persisting schema fixups: it takes the config.toml lock, reads the
// file fresh, lets apply mutate that fresh copy and report whether anything
// actually changed, and writes back only when it did. Without this, Load's migration writes read config.toml
// once — before loadRegistry and the migration probe run — and then saved
// that stale snapshot wholesale, silently reverting a concurrent
// PatchSave-based writer (`wt config` editor, `wt litellm set`) that landed
// in that window. That reopened the exact lost-update race PatchSave closed
// for those two writers (issue #143 follow-up).
func lockedApply(apply func(fresh *Config) (bool, error)) (fresh *Config, changed bool, err error) {
	err = WithLock(func() error {
		var err error
		fresh, _, err = readConfigFile()
		if err != nil {
			return err
		}
		changed, err = apply(fresh)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return writeConfigFile(fresh)
	})
	return fresh, changed, err
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
		if !c.InCatalog(m) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
