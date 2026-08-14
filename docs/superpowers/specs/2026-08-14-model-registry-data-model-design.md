# Model Registry Data Model

## Overview

Replace the flat bash `models.conf` arrays with a typed, relational config that
captures three entities — **providers**, **models**, and **agents** — and the
cascading constraints between them. The config is stored as TOML at
`~/.config/agent-wt/config.toml`.

## Entities

### Provider

A source of models. Providers carry connection info and may require secrets.

```go
type Location string

const (
    LocationLocal Location = "local"
    LocationCloud Location = "cloud"
)

type Provider struct {
    ID       string     `toml:"id"`
    Name     string     `toml:"name"`
    Location Location   `toml:"location,omitempty"`
    Auth     AuthConfig `toml:"auth"`
}

type AuthConfig struct {
    Type      string `toml:"type"`       // "none", "api_key", "oauth", "native"
    SecretRef string `toml:"secret_ref,omitempty"`
    BaseURL   string `toml:"base_url,omitempty"`
}
```

**Auth types:**

| Type | Meaning | SecretRef |
|---|---|---|
| `none` | No auth needed (local Ollama) | omitted |
| `api_key` | API key in a secret file | filename in secrets dir |
| `oauth` | OAuth flow (future) | omitted or token ref |
| `native` | Uses the agent's own built-in subscription (e.g. Claude Code) | omitted |

**Location:** optional. Providers that are always cloud (OpenRouter) or always
local can set it here. Providers that span both (Ollama) omit it and let each
model specify.

### Model

A specific variant of a base model available from a provider.

```go
type Model struct {
    ID         string   `toml:"id"`          // unique key, e.g. "ollama/gemma4:9b"
    Family     string   `toml:"family"`      // base model grouping, e.g. "gemma4"
    ProviderID string   `toml:"provider_id"` // → Provider.ID
    ModelName  string   `toml:"model_name"`  // provider-specific name, e.g. "gemma4:9b"
    Location   Location `toml:"location,omitempty"`
    Tags       []string `toml:"tags"`        // e.g. ["code", "design"]
}
```

**ID format:** `provider_id/model_name` — the combination of provider and
provider-specific model name is the unique key.

**Family:** groups variants of the same base model across providers. All gemma4
variants share `family = "gemma4"` regardless of provider. If empty, the model
is treated as its own unique family — no cross-provider grouping or rotation
skip applies. Used for:

- **Display grouping** — TUI groups variants under a family header.
- **Rotation skip** — if a gemma4 variant was just used, skip all other gemma4
  variants regardless of provider.

**Location resolution:** model location takes precedence. If omitted, the
provider's location is used. At least one must be set.

```go
func (c *Config) ResolveLocation(m Model) (Location, error)
```

### Agent

An AI coding tool that the `wt` launcher can invoke.

```go
type Agent struct {
    Name               string   `toml:"name"`
    SupportedProviders []string `toml:"supported_providers"` // hard constraint
    DefaultProvider    string   `toml:"default_provider,omitempty"`
}
```

**SupportedProviders** is a hard constraint: an agent cannot use a provider
not in this list. The TUI and CLI must filter provider options based on the
selected agent.

**DefaultProvider** is optional. When the user passes `--default`, the agent's
default provider is used. If the agent has no default configured, the CLI
raises an error: `"agent %q has no default provider configured"`.

An agent may support a provider whose auth type is `native` for a *different*
agent. For example, `pi` can list `"claude"` in its supported providers — it
uses Claude's subscription auth, but it is not pi's native/default provider.

### Config

```go
type Config struct {
    DefaultTag string     `toml:"default_tag"`
    Providers  []Provider `toml:"providers"`
    Models     []Model    `toml:"models"`
    Agents     []Agent    `toml:"agents"`
}
```

## Cascading constraints

```
Agent selected
  → Providers = agent.SupportedProviders (hard filter)
    → Models = models whose provider_id is in Providers
      → Tag filter = models whose tags include the selected tag
        → Family grouping for display
```

## Example config

```toml
default_tag = "code"

# ── Providers ──────────────────────────────────────────────

[[providers]]
id = "ollama"
name = "Ollama"
auth = { type = "none", base_url = "http://localhost:11434" }

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
auth = { type = "api_key", secret_ref = "openrouter.key" }

[[providers]]
id = "claude"
name = "Claude Code"
location = "cloud"
auth = { type = "native" }

[[providers]]
id = "copilot"
name = "GitHub Copilot"
location = "cloud"
auth = { type = "native" }

# ── Models ─────────────────────────────────────────────────

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
location = "local"
tags = ["code", "design"]

[[models]]
id = "ollama/gemma4:27b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:27b"
location = "local"
tags = ["code"]

[[models]]
id = "openrouter/google/gemma-4-9b"
family = "gemma4"
provider_id = "openrouter"
model_name = "google/gemma-4-9b"
tags = ["code", "design"]

[[models]]
id = "openrouter/google/gemma-4-27b"
family = "gemma4"
provider_id = "openrouter"
model_name = "google/gemma-4-27b"
tags = ["code"]

[[models]]
id = "ollama/kimi-k2.6:cloud"
family = "kimi-k2.6"
provider_id = "ollama"
model_name = "kimi-k2.6:cloud"
location = "cloud"
tags = ["code", "design"]

# ── Agents ─────────────────────────────────────────────────

[[agents]]
name = "claude"
supported_providers = ["claude", "ollama", "openrouter"]
default_provider = "claude"

[[agents]]
name = "pi"
supported_providers = ["claude", "ollama", "openrouter"]
default_provider = "openrouter"

[[agents]]
name = "copilot"
supported_providers = ["copilot", "ollama"]
default_provider = "copilot"
```

## Lookup methods

```go
func (c *Config) ProviderByID(id string) *Provider
func (c *Config) ModelsWithTag(tag string) []Model
func (c *Config) ModelsByFamily(family string) []Model
func (c *Config) ModelsByProvider(providerID string) []Model
func (c *Config) ProvidersForAgent(agentName string) ([]Provider, error)
func (c *Config) AgentByName(name string) (*Agent, error)
func (c *Config) ResolveLocation(m Model) (Location, error)
```

## Validation

| Rule | Error |
|---|---|
| `DefaultTag` must not be empty | `"default_tag must not be empty"` |
| `Model.ID` must be unique | `"duplicate model id %q"` |
| `Model.ID` must not be empty | `"model entry with empty id"` |
| `Model.ProviderID` must reference an existing `Provider.ID` | `"model %q: unknown provider %q"` |
| `Model.Location` or its provider's location must be set | `"model %q: no location on model or provider %q"` |
| `Provider.ID` must be unique | `"duplicate provider id %q"` |
| `Provider.ID` must not be empty | `"provider entry with empty id"` |
| `Agent.Name` must be unique | `"duplicate agent name %q"` |
| `Agent.SupportedProviders` entries must reference existing `Provider.ID` | `"agent %q: unknown provider %q"` |
| `Agent.DefaultProvider` must be in `SupportedProviders` (if set) | `"agent %q: default provider %q not in supported_providers"` |
| `--default` with no `DefaultProvider` set | `"agent %q has no default provider configured"` |

## Secrets

Secrets are stored separately from the config file. Today they are files in
`~/.config/agent-wt/secrets/`. The config only stores an opaque reference.

```go
type SecretStore interface {
    Get(ref string) (string, error)
}

type FileSecretStore struct {
    Dir string
}

func (s *FileSecretStore) Get(ref string) (string, error) {
    data, err := os.ReadFile(filepath.Join(s.Dir, ref))
    if err != nil {
        return "", err
    }
    return strings.TrimSpace(string(data)), nil
}
```

The `SecretStore` interface allows swapping in a secret manager (vault,
1Password, environment variables) later without changing the config format.

## What changes from current lesson 2 code

| Change | Detail |
|---|---|
| Remove | `Model.Native bool`, `Model.Location Location`, `Model.Provider string` |
| Add | `Model.Family string`, `Model.ProviderID string`, `Model.ModelName string` |
| Add | `Model.Location Location` (optional, overrides provider) |
| Add | `Provider` struct with `ID`, `Name`, `Location`, `Auth` |
| Add | `AuthConfig` struct with `Type`, `SecretRef`, `BaseURL` |
| Replace | `AgentDefault` → `Agent` with `SupportedProviders`, `DefaultProvider` |
| Add | `Config.Providers []Provider` |
| Replace | `Config.AgentDefaults` → `Config.Agents []Agent` |
| Add | `SecretStore` interface + `FileSecretStore` |
| Add | `ModelsByFamily`, `ModelsByProvider`, `ProvidersForAgent`, `AgentByName`, `ProviderByID`, `ResolveLocation` |
| Update | `Validate()` with cross-entity reference checks |
| Remove | `OllamaBaseURL` from `Config` (moved to `Provider.Auth.BaseURL`) |
