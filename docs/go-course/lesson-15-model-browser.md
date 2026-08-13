# Lesson 15: Model browser

## Concept Intro

The model browser is the "choose the backend LLM from a list" feature. It shows
all models (curated + discovered, from lesson 4's registry) with their metadata
columns — provider, location (local/cloud), tags — and lets you filter and pick
one. It reuses the `list` widget from lesson 13, but with richer items that
render the metadata.

The browser is a *view* into the registry, not the rotation itself: picking a
model here sets the current model directly (no state-file advance), while
rotation (`r`) advances the state file. This distinguishes the two model
actions you asked for: deliberate selection vs quick rotation.

We also add a simple **tag filter** key (`f`) that narrows the list to models
having a chosen tag, so "show me design models" is one keystroke away.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `modelItem` | A `list.Item` for a `config.Model` with metadata in `Description()`. |
| registry filtering | `registry.FilterByTag(models, tag)` helper. |
| `tea.Batch` | Run multiple commands at once (e.g. refresh + focus). |
| focus state | A `browserOpen bool` flag toggling between model phase and browser. |
| back key | `esc` returns from the browser to the agent+model screen. |

## Worked Walkthrough

Create `internal/registry/filter.go`:

```go
package registry

import "github.com/ohanaverse/agent-worktree/internal/config"

// FilterByTag returns models whose tags include tag (empty tag = all).
func FilterByTag(models []config.Model, tag string) []config.Model {
	if tag == "" {
		return models
	}
	var out []config.Model
	for _, m := range models {
		if m.HasTag(tag) {
			out = append(out, m)
		}
	}
	return out
}
```

Now the browser items in `internal/tui/model_browser.go`:

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/registry"
)

// modelItem adapts a config.Model to a list.Item.
type modelItem struct {
	model config.Model
}

func (m modelItem) FilterValue() string { return m.model.ID }

func (m modelItem) Title() string { return m.model.ID }

func (m modelItem) Description() string {
	loc := string(m.model.Location)
	src := string(m.model.Source)
	tags := strings.Join(m.model.Tags, ",")
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%-10s %-6s %-10s %s", m.model.Provider, loc, src, tags)
}

func buildModelItems(models []config.Model) []list.Item {
	items := make([]list.Item, 0, len(models))
	for _, m := range models {
		items = append(items, modelItem{model: m})
	}
	return items
}

// refreshBrowser rebuilds the browser list from cfg + discovery + tag filter.
func (m *model) refreshBrowser() {
	all := registry.Discover(m.cfg)          // curated + discovered
	filtered := registry.FilterByTag(all, m.browserTag)
	m.browser = list.New(buildModelItems(filtered), list.NewDefaultDelegate(),
		m.width-2, m.height-2)
	m.browser.Title = "Model browser"
}
```

### Wiring into the app

Extend `model` in `app.go`:

```go
	browser     list.Model // model browser list widget
	browserOpen bool
	browserTag  string
```

In `Update`, change the `m` key from placeholder to opening the browser, and
add `esc` to close it:

```go
case "m":
	if m.phase == phaseModel {
		m.browserOpen = true
		m.refreshBrowser()
	}
case "esc":
	if m.browserOpen {
		m.browserOpen = false
	}
```

When the browser is open, Enter picks the selected model back into the model
phase (no rotation state write):

```go
case "enter":
	if m.browserOpen {
		item, ok := m.browser.SelectedItem().(modelItem)
		if ok {
			m.current = item.model
			m.browserOpen = false
		}
	}
```

And update `View` to render the browser when open:

```go
func (m model) View() string {
	if m.browserOpen {
		return m.browser.View() + "\n" + m.browser.Help.View(m.browser.KeyMap)
	}
	if m.phase == phaseModel {
		// ... as before ...
	}
	// ...
}
```

### Tag filter

Add an `f` key that cycles the browser tag filter through the available tags.
Keep it simple for now — toggle between the current tag and empty (all):

```go
case "f":
	if m.browserOpen {
		if m.browserTag == "" {
			m.browserTag = m.tag
		} else {
			m.browserTag = ""
		}
		m.refreshBrowser()
	}
```

## Run It

```bash
go run ./cmd/wt
```

Pick a worktree, press `m` to open the browser. You'll see curated + discovered
models with provider/location/source/tags columns. Filter by typing; press `f`
to toggle tag filtering; press `enter` to select a model; `esc` to go back.

## Try It Yourself

Add a `source` filter too: cycle through `all → curated → discovered` with the
`c` key, using a new `registry.FilterBySource` helper.

<details>
<summary>Solution</summary>

```go
// in filter.go
func FilterBySource(models []config.Model, source config.Source) []config.Model {
	var out []config.Model
	for _, m := range models {
		if m.Source == source {
			out = append(out, m)
		}
	}
	return out
}
```

Wire an `m.sourceCycle` int and a `c` key that increments it and filters:
0=all, 1=curated, 2=discovered.
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 15: model browser" && git tag lesson-15
```
