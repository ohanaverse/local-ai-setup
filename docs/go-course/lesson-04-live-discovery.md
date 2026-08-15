# Lesson 4: Live model discovery

## Concept Intro

A purely hand-curated registry (lesson 2) is stable but stale — when you pull
a new model into Ollama or a provider adds a model, it won't appear until you
edit config. The hybrid design adds **live discovery**: query connected
providers for their current model lists and merge the results with the
curated entries.

Two providers cover the common cases:

- **Ollama:** list models via the CLI, `ollama list`, which prints a
table of `NAME  ID  SIZE  MODIFIED`. The output includes **both** local models
(with a size like `21 GB`) and cloud models (size is `-`, name usually ends
in `:cloud`). We shell out, parse the first column, and decide `local` vs
`cloud` from the size field.
- **OpenRouter (cloud):** a REST API `https://openrouter.ai/api/v1/models`
returns a JSON array. We fetch it and pull each model's `id`.

Discovery results are tagged `SourceDiscovered` and merged into the registry,
deduped by model id against curated entries (curated wins — it carries the
user's tags and metadata).

This lesson also introduces the **discoverer interface**: a small abstraction
so future providers slot in without touching the merge logic. We call it
`Discoverer` rather than `Provider` because `config.Provider` already exists
as a struct.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
|`exec.Command(name, args...).Output()`|Runs a command and captures stdout (returns `[]byte`).|
|`encoding/json.Unmarshal`|Decodes JSON bytes into a struct/slice.|
|`http.Client{Timeout: ...}`|An HTTP client with a request timeout so discovery can't hang forever.|
|`defer resp.Body.Close()`|Ensures the response body is closed even on early return.|
|`io.ReadAll(resp.Body)`|Reads the whole response body.|
|`Discoverer` interface|`Discover() ([]config.Model, error)` — the seam for adding providers.|
|merge / dedup|Curated entries override discovered ones with the same id.|

## Worked Walkthrough

### Step 1: Add `Source` to `config.Model`

The config needs to track whether a model came from the config file or from
live discovery. Add the type and field to `internal/config/config.go`:

```go
// Source tracks how a model entered the registry.
type Source string

const (
	SourceCurated    Source = "curated"
	SourceDiscovered Source = "discovered"
)
```

And add the field to `Model`:

```go
type Model struct {
	ID         string   `toml:"id"`          // unique key, e.g. "ollama/gemma4:9b"
	Family     string   `toml:"family"`      // base model grouping, e.g. "gemma4"
	ProviderID string   `toml:"provider_id"` // → Provider.ID
	ModelName  string   `toml:"model_name"`  // provider-specific name, e.g. "gemma4:9b"
	Location   Location `toml:"location,omitempty"`
	Tags       []string `toml:"tags"` // e.g. ["code", "design"]
	Source     Source   `toml:"source,omitempty"` // curated or discovered
}
```

### Step 2: Create `internal/registry/discover.go`

Add the discoverer interface and an ollama implementation:

```go
package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Discoverer discovers models from a single source.
type Discoverer interface {
	Discover() ([]config.Model, error)
}

// Ollama lists models via `ollama list`. The CLI prints both local and cloud
// models; cloud entries have "-" in the SIZE column.
type Ollama struct{}

func (Ollama) Discover() ([]config.Model, error) {
	if _, err := exec.LookPath("ollama"); err != nil {
		return nil, nil // ollama not installed — no models to discover
	}
	out, err := exec.Command("ollama", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("ollama list: %w", err)
	}
	var models []config.Model
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i == 0 {
			continue // header row: NAME  ID  SIZE  MODIFIED
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name := fields[0]
		if name == "" {
			continue
		}
		// Cloud models have "-" in the SIZE column; local models have a size.
		loc := config.LocationLocal
		if fields[2] == "-" {
			loc = config.LocationCloud
		}
		models = append(models, config.Model{
			ID:         "ollama/" + name,
			Family:     name,
			ProviderID: "ollama",
			ModelName:  name,
			Location:   loc,
			Source:     config.SourceDiscovered,
		})
	}
	return models, nil
}
```

Add the OpenRouter discoverer — it hits a JSON API:

```go
// OpenRouter lists cloud models via the OpenRouter REST API.
type OpenRouter struct {
	Client *http.Client
}

func (or OpenRouter) Discover() ([]config.Model, error) {
	if or.Client == nil {
		or.Client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := or.Client.Get("https://openrouter.ai/api/v1/models")
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("openrouter decode: %w", err)
	}
	models := make([]config.Model, 0, len(payload.Data))
	for _, m := range payload.Data {
		id := m.ID
		family := id
		if idx := strings.LastIndex(id, "/"); idx >= 0 {
			family = id[idx+1:]
		}
		models = append(models, config.Model{
			ID:         "openrouter/" + id,
			Family:     family,
			ProviderID: "openrouter",
			ModelName:  id,
			Location:   config.LocationCloud,
			Source:     config.SourceDiscovered,
		})
	}
	return models, nil
}
```

### Step 3: Add merge logic in `internal/registry/registry.go`

```go
package registry

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Merge combines curated models with discovered ones. Curated entries win on
// id collisions; discovered entries are appended otherwise.
func Merge(curated []config.Model, discovered []config.Model) []config.Model {
	byID := make(map[string]config.Model, len(curated))
	for _, m := range curated {
		byID[m.ID] = m
	}
	for _, d := range discovered {
		if _, exists := byID[d.ID]; !exists {
			byID[d.ID] = d
		}
	}
	out := make([]config.Model, 0, len(byID))
	for _, m := range byID {
		out = append(out, m)
	}
	return out
}

// Discover runs each connected discoverer and returns the merged registry.
func Discover(cfg *config.Config) []config.Model {
	var discovered []config.Model
	discoverers := []Discoverer{Ollama{}, OpenRouter{}}
	for _, d := range discoverers {
		models, err := d.Discover()
		if err != nil {
			// Discovery failures are non-fatal — fall back to curated only.
			continue
		}
		discovered = append(discovered, models...)
	}
	return Merge(cfg.Models, discovered)
}
```

### Step 4: Wire `modelsCmd` to show curated models as a table

Update `modelsCmd()` in `cmd/wt/main.go` to display only configured models,
sorted by provider and model name, in a clean table format. Remove the
`registry` import — discovery is implemented and ready for the TUI (lesson 12)
and launch flow, but `wt models` shows the user's hand-curated registry only.

Add `"sort"` to the imports:

```go
import (
	"fmt"
	"os"
	"sort"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/spf13/cobra"
)
```

Then change the `models` subcommand:

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

			// Sort curated models by provider, then model name.
			models := make([]config.Model, len(cfg.Models))
			copy(models, cfg.Models)
			sort.Slice(models, func(i, j int) bool {
				if models[i].ProviderID != models[j].ProviderID {
					return models[i].ProviderID < models[j].ProviderID
				}
				return models[i].ID < models[j].ID
			})

			fmt.Println("\nModels:")
			fmt.Printf("%-32s %-16s %-12s %-8s %s\n",
				"ID", "FAMILY", "PROVIDER", "LOCATION", "TAGS")
			fmt.Println("--------------------------------------------------------------------------------")
			for _, m := range models {
				loc, _ := cfg.ResolveLocation(m)
				fmt.Printf("%-32s %-16s %-12s %-8s %v\n",
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

## Run It

```bash
go run ./cmd/wt models
```

Sample output (depends on what's in your config):

```
Providers:
  ollama          local      auth=none    base_url=http://localhost:11434
  claude          cloud      auth=native  base_url=

Models:
ID                               FAMILY           PROVIDER     LOCATION TAGS
--------------------------------------------------------------------------------
claude/native                    claude           claude       cloud    [code design]
ollama/gemma4:9b                 gemma4           ollama       local    [code design]
ollama/gemma4:27b                gemma4           ollama       local    [code]

Agents:
  claude     providers=[claude ollama] default=claude
```

## Try It Yourself

Refactor `Merge` to preserve a stable order (curated first in config order,
then discovered), instead of map iteration order.

<details>
<summary>Solution</summary>

Track an explicit order list instead of ranging over the map:

```go
func Merge(curated, discovered []config.Model) []config.Model {
	byID := make(map[string]config.Model, len(curated)+len(discovered))
	var order []string
	add := func(m config.Model) {
		if _, exists := byID[m.ID]; exists {
			return
		}
		byID[m.ID] = m
		order = append(order, m.ID)
	}
	for _, m := range curated {
		add(m)
	}
	for _, m := range discovered {
		add(m)
	}
	out := make([]config.Model, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 04: live model discovery" && git tag lesson-04
```
