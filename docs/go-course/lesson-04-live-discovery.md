# Lesson 4: Live model discovery

## Concept Intro

A purely hand-curated registry (lesson 2) is stable but stale — when you pull
a new model into Ollama or a provider adds a model, it won't appear until you
edit config. The hybrid design adds **live discovery**: query connected
providers for their current model lists and merge the results with the
curated entries.

Two providers cover the common cases:

- **Ollama (local):** list models via the CLI, `ollama list`, which prints a
  table of `NAME  ID  SIZE  MODIFIED`. We shell out and parse the first column.
- **OpenRouter (cloud):** a REST API `https://openrouter.ai/api/v1/models`
  returns a JSON array. We fetch it and pull each model's `id`.

Discovery results are tagged `SourceDiscovered` and merged into the registry,
deduped by model id against curated entries (curated wins — it carries the
user's tags and metadata).

This lesson also introduces the **provider interface**: a small abstraction so
future providers slot in without touching the merge logic.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `exec.Command(name, args...).Output()` | Runs a command and captures stdout (returns `[]byte`). |
| `encoding/json.Unmarshal` | Decodes JSON bytes into a struct/slice. |
| `http.Client{Timeout: ...}` | An HTTP client with a request timeout so discovery can't hang forever. |
| `defer resp.Body.Close()` | Ensures the response body is closed even on early return. |
| `io.ReadAll(resp.Body)` | Reads the whole response body. |
| `Provider` interface | `Discover() ([]Model, error)` — the seam for adding providers. |
| merge / dedup | Curated entries override discovered ones with the same id. |

## Worked Walkthrough

Add the provider interface and an ollama implementation in
`internal/registry/discover.go`:

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
)

// Provider discovers models from a single source.
type Provider interface {
	Discover() ([]config.Model, error)
}

// Ollama lists local models via `ollama list`.
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
		name := strings.Fields(line)[0]
		if name == "" {
			continue
		}
		models = append(models, config.Model{
			ID:       name,
			Provider: "ollama",
			Location: config.LocationLocal,
			Source:   config.SourceDiscovered,
		})
	}
	return models, nil
}
```

Add the OpenRouter provider — it hits a JSON API. We need the `Source` field
on `config.Model` (added in lesson 2) and a `SetSource` helper:

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
		models = append(models, config.Model{
			ID:       m.ID,
			Provider: "openrouter",
			Location: config.LocationCloud,
			Source:   config.SourceDiscovered,
		})
	}
	return models, nil
}
```

Now the merge in `internal/registry/registry.go`:

```go
package registry

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Merge combines curated models with discovered ones. Curated entries win on
// id collisions; discovered entries are appended (tag-free) otherwise.
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

// Discover runs each connected provider and returns the merged registry.
// baseURL is passed so future providers that need it (like ollama over HTTP)
// can use it; today it is reserved.
func Discover(cfg *config.Config) []config.Model {
	var discovered []config.Model
	providers := []Provider{Ollama{}, OpenRouter{}}
	for _, p := range providers {
		models, err := p.Discover()
		if err != nil {
			// Discovery failures are non-fatal — fall back to curated only.
			continue
		}
		discovered = append(discovered, models...)
	}
	return Merge(cfg.Models, discovered)
}
```

Note `config.Model` needs a `Source` field. Add it:

```go
	Source Source `toml:"source,omitempty"`
```

## Run It

Wire a quick test invocation:

```go
// in main or a scratch func
cfg, _ := config.Load()
all := registry.Discover(cfg)
for _, m := range all {
	fmt.Printf("%-24s %-10s %-6s %s\n", m.ID, m.Provider, m.Location, m.Source)
}
```

```bash
go run ./cmd/wt
```

```
native:claude         claude     cloud  curated
kimi-k2.6:cloud       ollama     cloud  curated
llama3.1              ollama     local  discovered   <- from `ollama list`
qwen3                 ollama     local  discovered   <- from `ollama list`
```

(The exact local models depend on what's installed.)

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
