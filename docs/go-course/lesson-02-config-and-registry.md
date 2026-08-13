# Lesson 2: Config & model registry data model

## Concept Intro

The heart of this tool's "robust model options" is a **model registry**: a
list of known models, each described by metadata (provider, location
local/cloud, tags). Today this lives in a bash-sourced `models.conf` with two
flat arrays (`CODE_MODELS`, `DESIGN_MODELS`) and a few vars. We're upgrading
it to a structured config where every model carries its own metadata and can
be tagged (e.g. tagged `code`, `design`) so rotation and browsing work by tag
instead of by hard-coded array names.

In Go, we model this as typed structs and parse a TOML file with the
`BurntSushi/toml` library. TOML is a good fit: it's human-editable (unlike
JSON's braces), typed (unlike bash sourcing), and supports arrays of tables —
perfect for a list of model entries.

Key design choice: a `Model` is identified by a stable `id` string. Tags are
a `[]string`. The `source` field records whether an entry is *curated*
(hand-written by the user) or *discovered* (from a live provider — lesson 4).

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `type Model struct` | A struct literal field type for one registry entry. |
| TOML array of tables | `[[models]]` — each block is one entry. |
| `toml.DecodeFile(path, &cfg)` | Decodes a TOML file into a struct, returning a Meta with Undecoded keys. |
| `toml.Decode(...)` | Decodes from a `[]byte`. |
| `meta.Undecoded()` | Keys in the file not represented in the struct — useful for validation warnings. |
| `Validate() error` | A method on the config that returns an error describing problems. |
| `//go:embed` | Embeds a default config into the binary (used later for seeding). |

## Worked Walkthrough

Add TOML:

```bash
go get github.com/BurntSushi/toml@latest
```

Create `internal/config/config.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Location says whether a model is hosted locally or in the cloud.
type Location string

const (
	LocationLocal Location = "local"
	LocationCloud Location = "cloud"
)

// Source says whether an entry was hand-curated or discovered from a provider.
type Source string

const (
	SourceCurated    Source = "curated"
	SourceDiscovered Source = "discovered"
)

// Model is one entry in the registry.
type Model struct {
	ID       string   `toml:"id"`                 // stable id, e.g. "kimi-k2.6:cloud"
	Provider string   `toml:"provider"`           // claude, ollama, openrouter, copilot, ...
	Location Location `toml:"location"`           // local | cloud
	Tags     []string `toml:"tags"`               // e.g. ["code", "design"]
	Native   bool     `toml:"native,omitempty"`   // true if this is an agent's native model
}

// AgentDefault maps an agent name (claude, codex, ...) to its default model id.
type AgentDefault struct {
	Agent string `toml:"agent"`
	Model string `toml:"model"`
}

// Config is the on-disk configuration for wt.
type Config struct {
	DefaultTag     string         `toml:"default_tag"`      // rotation group used when none specified
	Models         []Model        `toml:"models"`
	AgentDefaults  []AgentDefault `toml:"agent_defaults"`
	OllamaBaseURL  string         `toml:"ollama_base_url"`  // e.g. http://localhost:11434
}

// Path returns the config file location, honoring XDG_CONFIG_HOME like wt-core.sh.
func Path() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agent-wt", "config.toml")
}

// Load reads the config file at Path(). Returns an empty Config if the file
// does not exist yet (so first-run works before any config is written).
func Load() (*Config, error) {
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
	seen := map[string]bool{}
	for _, m := range c.Models {
		if m.ID == "" {
			return fmt.Errorf("model entry with empty id")
		}
		if seen[m.ID] {
			return fmt.Errorf("duplicate model id %q", m.ID)
		}
		seen[m.ID] = true
		if m.Location != LocationLocal && m.Location != LocationCloud {
			return fmt.Errorf("model %q: invalid location %q", m.ID, m.Location)
		}
	}
	return nil
}

// ModelsWithTag returns models whose tags include tag.
func (c *Config) ModelsWithTag(tag string) []Model {
	var out []Model
	for _, m := range c.Models {
		for _, t := range m.Tags {
			if t == tag {
				out = append(out, m)
				break
			}
		}
	}
	return out
}
```

The `toml` import is missing — add it:

```go
import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)
```

A matching `config.toml`:

```toml
default_tag = "code"
ollama_base_url = "http://localhost:11434"

[[models]]
id = "kimi-k2.6:cloud"
provider = "ollama"
location = "cloud"
tags = ["code", "design"]

[[models]]
id = "native:claude"
provider = "claude"
location = "cloud"
tags = ["code", "design"]
native = true

[[agent_defaults]]
agent = "claude"
model = "native:claude"
```

## Run It

Wire `wt models` to print the parsed registry:

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
			for _, m := range cfg.ModelsWithTag("code") {
				fmt.Printf("%-20s %-10s %-6s %v\n", m.ID, m.Provider, m.Location, m.Tags)
			}
			return nil
		},
	}
}
```

```bash
go run ./cmd/wt models
```

```
kimi-k2.6:cloud      ollama     cloud  [code design]
native:claude        claude     cloud  [code design]
```

## Try It Yourself

Add a `HasTag(tag string) bool` method on `Model` and refactor
`ModelsWithTag` to use it.

<details>
<summary>Solution</summary>

```go
func (m Model) HasTag(tag string) bool {
	for _, t := range m.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

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
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 02: config & model registry data model" && git tag lesson-02
```
