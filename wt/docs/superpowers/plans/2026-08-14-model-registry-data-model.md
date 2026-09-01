# Model Registry Data Model — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat `Model`/`AgentDefault` types with the three-entity data model (Provider, Model, Agent) from the spec, add lookup methods, cross-entity validation, and a `SecretStore` interface.

**Architecture:** All changes are in `internal/config/config.go` (types, methods, validation) and `cmd/wt/main.go` (wiring `wt models` to print the new structure). The file stays as a single `config` package — lesson 19 covers splitting into `internal/` sub-packages.

**Tech Stack:** Go 1.26, `BurntSushi/toml`, `github.com/spf13/cobra`

---

### Task 1: Replace all type definitions

**Files:**
- Modify: `internal/config/config.go` (entire type block)

- [ ] **Step 1: Replace the type block**

Replace everything from the `Location` const block through the `Config` struct with the new types. Keep `Path()`, `Load()`, `Validate()`, and `ModelsWithTag()` — they'll be updated in later tasks.

```go
package config

import (
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
	Type      string `toml:"type"`                 // "none", "api_key", "oauth", "native"
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
	Tags       []string `toml:"tags"` // e.g. ["code", "design"]
}

// ── Agent ─────────────────────────────────────────────────

// Agent is a supported AI coding tool.
type Agent struct {
	Name               string   `toml:"name"`
	SupportedProviders []string `toml:"supported_providers"`           // hard constraint
	DefaultProvider    string   `toml:"default_provider,omitempty"`    // optional
}

// ── Config ────────────────────────────────────────────────

// Config is the on-disk configuration for wt.
type Config struct {
	DefaultTag string     `toml:"default_tag"`
	Providers  []Provider `toml:"providers"`
	Models     []Model    `toml:"models"`
	Agents     []Agent    `toml:"agents"`
}
```

- [ ] **Step 2: Verify it compiles (tests will fail, that's expected)**

```bash
go build ./...
```

Expected: compile errors in `Validate()` and `ModelsWithTag()` referencing old field names. That's fine — we fix those next.

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "refactor: replace types with Provider/Model/Agent data model"
```

---

### Task 2: Add lookup methods

**Files:**
- Modify: `internal/config/config.go` (append new methods after `ModelsWithTag`)

- [ ] **Step 1: Add ProviderByID**

```go
// ProviderByID returns the provider with the given id, or nil if not found.
func (c *Config) ProviderByID(id string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].ID == id {
			return &c.Providers[i]
		}
	}
	return nil
}
```

- [ ] **Step 2: Add ModelsByFamily**

```go
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
```

- [ ] **Step 3: Add ModelsByProvider**

```go
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
```

- [ ] **Step 4: Add AgentByName**

```go
// AgentByName returns the agent with the given name, or an error if not found.
func (c *Config) AgentByName(name string) (*Agent, error) {
	for i := range c.Agents {
		if c.Agents[i].Name == name {
			return &c.Agents[i], nil
		}
	}
	return nil, fmt.Errorf("agent %q not found", name)
}
```

- [ ] **Step 5: Add ProvidersForAgent**

```go
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
```

- [ ] **Step 6: Add ResolveLocation**

```go
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
```

- [ ] **Step 7: Verify it compiles**

```bash
go build ./...
```

Expected: still errors in `Validate()` and `ModelsWithTag()` from old field references. The new methods compile cleanly.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add lookup methods (ProviderByID, ModelsByFamily, ModelsByProvider, AgentByName, ProvidersForAgent, ResolveLocation)"
```

---

### Task 3: Update ModelsWithTag for new Model struct

**Files:**
- Modify: `internal/config/config.go` (the `ModelsWithTag` function)

- [ ] **Step 1: Add HasTag method on Model**

Insert before `ModelsWithTag`:

```go
// HasTag returns true if the model has the given tag.
func (m Model) HasTag(tag string) bool {
	for _, t := range m.Tags {
		if t == tag {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Refactor ModelsWithTag to use HasTag**

Replace the existing `ModelsWithTag` body:

```go
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
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./...
```

Expected: only `Validate()` still has errors (old field references). Everything else compiles.

- [ ] **Step 4: Commit**

```bash
git add internal/config/config.go
git commit -m "refactor: add Model.HasTag, simplify ModelsWithTag"
```

---

### Task 4: Rewrite Validate() with cross-entity checks

**Files:**
- Modify: `internal/config/config.go` (the `Validate` method)

- [ ] **Step 1: Replace the Validate method**

```go
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
```

- [ ] **Step 2: Verify it compiles and build succeeds**

```bash
go build ./...
```

Expected: clean build, no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: rewrite Validate() with cross-entity reference checks"
```

---

### Task 5: Add SecretStore interface and FileSecretStore

**Files:**
- Create: `internal/config/secrets.go`

- [ ] **Step 1: Create secrets.go**

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// SecretStore resolves opaque secret references to values.
type SecretStore interface {
	Get(ref string) (string, error)
}

// FileSecretStore reads secrets from files in a directory.
type FileSecretStore struct {
	Dir string
}

// Get reads the file named by ref and returns its trimmed contents.
func (s *FileSecretStore) Get(ref string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, ref))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// DefaultSecretsDir returns the default secrets directory.
func DefaultSecretsDir() string {
	return filepath.Join(configDir(), "secrets")
}

// configDir is the base config directory (shared with Path).
func configDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agent-wt")
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/config/secrets.go
git commit -m "feat: add SecretStore interface and FileSecretStore"
```

---

### Task 6: Wire modelsCmd to print the new registry

**Files:**
- Modify: `cmd/wt/main.go` (the `modelsCmd` function)

- [ ] **Step 1: Update modelsCmd to use the new config**

Replace the `modelsCmd` function:

```go
func modelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "Browse and manage the model registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}

			fmt.Println("Providers:")
			for _, p := range cfg.Providers {
				fmt.Printf("  %-15s %-10s auth=%-8s base_url=%s\n",
					p.ID, p.Location, p.Auth.Type, p.Auth.BaseURL)
			}

			fmt.Println("\nModels:")
			for _, m := range cfg.ModelsWithTag("code") {
				loc, _ := cfg.ResolveLocation(m)
				fmt.Printf("  %-35s family=%-12s provider=%-12s location=%-6s tags=%v\n",
					m.ID, m.Family, m.ProviderID, loc, m.Tags)
			}

			fmt.Println("\nAgents:")
			for _, a := range cfg.Agents {
				fmt.Printf("  %-10s providers=%v default=%s\n",
					a.Name, a.SupportedProviders, a.DefaultProvider)
			}
			return nil
		},
	}
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add cmd/wt/main.go
git commit -m "feat: wire modelsCmd to print new Provider/Model/Agent registry"
```

---

### Task 7: Create sample config and end-to-end test

**Files:**
- Create: `testdata/config.toml` (sample config for manual testing)

- [ ] **Step 1: Create testdata/config.toml**

```bash
mkdir -p testdata
```

```toml
default_tag = "code"

[[providers]]
id = "ollama"
name = "Ollama"
[auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
[auth]
type = "api_key"
secret_ref = "openrouter.key"

[[providers]]
id = "claude"
name = "Claude Code"
location = "cloud"
[auth]
type = "native"

[[providers]]
id = "copilot"
name = "GitHub Copilot"
location = "cloud"
[auth]
type = "native"

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

- [ ] **Step 2: Run with the sample config**

```bash
XDG_CONFIG_HOME=./testdata go run ./cmd/wt models
```

Expected output:

```
Providers:
  ollama                      auth=none    base_url=http://localhost:11434
  openrouter     cloud        auth=api_key base_url=
  claude         cloud        auth=native  base_url=
  copilot        cloud        auth=native  base_url=

Models:
  ollama/gemma4:9b                      family=gemma4       provider=ollama       location=local  tags=[code design]
  ollama/gemma4:27b                     family=gemma4       provider=ollama       location=local  tags=[code]
  openrouter/google/gemma-4-9b          family=gemma4       provider=openrouter   location=cloud  tags=[code design]
  openrouter/google/gemma-4-27b         family=gemma4       provider=openrouter   location=cloud  tags=[code]
  ollama/kimi-k2.6:cloud                family=kimi-k2.6    provider=ollama       location=cloud  tags=[code design]

Agents:
  claude     providers=[claude ollama openrouter] default=claude
  pi         providers=[claude ollama openrouter] default=openrouter
  copilot    providers=[copilot ollama] default=copilot
```

- [ ] **Step 3: Test validation with a bad config**

Create `testdata/bad-config/config.toml`:

```bash
mkdir -p testdata/bad-config
```

```toml
default_tag = "code"

[[models]]
id = "orphan/model"
family = "test"
provider_id = "nonexistent"
model_name = "model"
location = "cloud"
tags = ["code"]
```

Run:

```bash
XDG_CONFIG_HOME=./testdata/bad-config go run ./cmd/wt models
```

Expected: error `model "orphan/model": unknown provider "nonexistent"`

- [ ] **Step 4: Commit**

```bash
git add testdata/
git commit -m "test: add sample config and validation test case"
```

---

### Task 8: Final verification and tag

- [ ] **Step 1: Run full build and fmt**

```bash
go build ./...
go fmt ./...
go vet ./...
```

Expected: all pass.

- [ ] **Step 2: Review the final diff**

```bash
git diff main --stat
```

- [ ] **Step 3: Commit any remaining changes and tag**

```bash
git add -A
git commit -m "lesson 02: config & model registry data model (revised)"
git tag -f lesson-02
```
