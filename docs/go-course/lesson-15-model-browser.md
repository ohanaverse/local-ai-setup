# Lesson 15: Model browser

## Concept Intro

The model browser is the "choose the backend LLM from a list" feature. It shows
all models (curated + discovered, from lesson 4's registry) with their metadata
columns — provider, location (local/cloud), source (curated/discovered), tags —
and lets you filter and pick one. It reuses the `list` widget from lesson 13,
but with richer items that render the metadata.

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
| registry filtering | `registry.FilterByTag(models, tag)` / `FilterBySource(models, source)` helpers. |
| browser phase | A new `phaseBrowser` constant on the existing `phase` enum — toggles between `phaseModel` and the browser. |
| back key | `esc` returns from the browser to the agent+model screen (and still quits from `phaseModel`). |
| discovery cache | `registry.Discover` runs once per browser-open; filter toggles re-use the cache. |

> **Why not a separate `browserOpen bool`?** Lesson 14 already introduced a
> `phase` enum (`phaseList` / `phaseModel`). A third constant `phaseBrowser`
> keeps every keybinding gate uniform (`m.phase == phaseBrowser`) and avoids
> the `phaseModel && browserOpen` ambiguity a parallel bool would create.

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

// FilterBySource keeps models whose Source field matches (used by the
// 'c' key in the optional challenge below).
func FilterBySource(models []config.Model, source config.Source) []config.Model {
	if source == "" {
		return models
	}
	var out []config.Model
	for _, m := range models {
		if m.Source == source {
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

// Description renders provider / location / source / tags columns.
// Location and Source are typed (config.Location / config.Source) so we
// cast via string(). Provider is the ProviderID foreign key into the
// Providers list — not a separate Provider struct field.
func (m modelItem) Description() string {
	tags := strings.Join(m.model.Tags, ",")
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%-10s %-6s %-10s %s",
		m.model.ProviderID,
		string(m.model.Location),
		string(m.model.Source),
		tags,
	)
}

func buildModelItems(models []config.Model) []list.Item {
	items := make([]list.Item, 0, len(models))
	for _, m := range models {
		items = append(items, modelItem{model: m})
	}
	return items
}

// refreshBrowser rebuilds the browser list from the discovery cache plus
// the current tag filter. We snapshot registry.Discover(cfg) once per
// browser-open into m.browserCache; subsequent filter toggles only re-run
// the cheap in-memory filter, not the shell/HTTP discovery calls.
func (m *model) refreshBrowser() {
	if m.browserCache == nil {
		m.browserCache = registry.Discover(m.cfg)
	}
	filtered := registry.FilterByTag(m.browserCache, m.browserTag)
	m.browser = list.New(buildModelItems(filtered), list.NewDefaultDelegate(),
		m.width-2, m.height-2)
	m.browser.Title = "Model browser"
}
```

> **Why cache discovery?** `registry.Discover` shells out to `ollama list`
> and HTTP-fetches `openrouter.ai/api/v1/models`. Calling it on every
> keystroke in the filter input would hammer those endpoints. The cache is
> scoped to the browser's lifetime — opening `m` again after closing it
> re-discovers, which is what you want: models may have changed since the
> last browser session.

### Wiring into the app

Add a third phase constant in `app.go`:

```go
type phase int

const (
	phaseList    phase = iota // worktree list (lesson 13)
	phaseModel                // agent+model screen (lesson 14)
	phaseBrowser              // model browser (this lesson)
)
```

Extend `model` with the browser fields:

```go
type model struct {
	// ... existing lesson-14 fields ...

	browser       list.Model // model browser list widget
	browserCache  []config.Model // snapshot of registry.Discover, per browser-open
	browserTag    string          // "" = all models; otherwise a tag like "code"
}
```

In `Update`, change the `m` key from placeholder to opening the browser,
and make `esc` phase-aware so it pops back from the browser instead of
quitting:

```go
case "m":
	if m.phase == phaseModel {
		m.phase = phaseBrowser
		m.browserCache = nil // force refresh on open
		m.refreshBrowser()
	}
case "esc":
	// Nested screen: pop back. Top-level: quit.
	if m.phase == phaseBrowser {
		m.phase = phaseModel
		return m, nil
	}
	return m, tea.Quit
```

When the browser is open, Enter picks the selected model back into the
model phase (no rotation state write):

```go
case "enter":
	if m.phase == phaseBrowser {
		if item, ok := m.browser.SelectedItem().(modelItem); ok {
			m.current = item.model
			m.phase = phaseModel
		}
	}
	// (phaseModel Enter remains a no-op until lesson 16 wires it to launch.)
```

And update `View` to render the browser when open:

```go
func (m model) View() string {
	if m.phase == phaseBrowser {
		view := m.browser.View()
		if m.browserTag != "" {
			view += "\nfilter: tag=" + m.browserTag
		}
		return view + "\n" + m.browser.Help.View(m.browser.KeyMap)
	}
	if m.phase == phaseModel {
		// ... as in lesson 14 ...
	}
	// ...
}
```

> **Empty-list hint.** When the active tag filter excludes everything,
> the title still says `"Model browser"` and the list is blank. The
> `filter: tag=…` line makes the empty state self-explanatory rather
> than looking like a hang.

### Defer the build when the window size is unknown

`m.width`/`m.height` start at zero until the first `tea.WindowSizeMsg`
arrives. If the user opens the browser before that message, `list.New`
at width-2/height-2 = -2/-2 panics or renders nothing. The existing
worktree list has the same issue but solves it by gating the list build
in `entriesLoadedMsg`. Do the same here:

```go
func (m *model) refreshBrowser() {
	if m.browserCache == nil {
		m.browserCache = registry.Discover(m.cfg)
	}
	if m.width <= 0 || m.height <= 0 {
		// Defer until WindowSizeMsg arrives; View() will see zero size
		// and skip rendering rather than building a broken list.
		return
	}
	filtered := registry.FilterByTag(m.browserCache, m.browserTag)
	m.browser = list.New(buildModelItems(filtered), list.NewDefaultDelegate(),
		m.width-2, m.height-2)
	m.browser.Title = "Model browser"
}
```

And in the `tea.WindowSizeMsg` case, rebuild if the browser is open:

```go
case tea.WindowSizeMsg:
	m.width, m.height = msg.Width, msg.Height
	if m.ready {
		m.list.SetSize(msg.Width-2, msg.Height-2)
	}
	if m.phase == phaseBrowser {
		m.refreshBrowser()
	}
```

### Tag filter

Add an `f` key that toggles `browserTag` between the active rotation tag
and `""` (all):

```go
case "f":
	if m.phase == phaseBrowser {
		if m.browserTag == "" {
			m.browserTag = m.tag
		} else {
			m.browserTag = ""
		}
		m.refreshBrowser()
	}
```

## Tests

The repo convention is one `Test*` function per behavior with a top-of-
function `// what/why` block explaining the user-facing consequence.
Lesson 15 follows that.

### `internal/registry/filter_test.go` (new)

- `TestFilterByTagKeepsMatching` — design-tagged models survive; code-only ones drop. A regression would silently empty the browser when a tag filter is applied.
- `TestFilterByTagEmptyReturnsAll` — empty tag returns the input slice (not a new copy). Keeps the no-op path allocation-free.
- `TestFilterBySourceKeepsMatching` — only `SourceCurated` survive; discovered ones drop. Powers the optional `c` key.
- `TestFilterBySourceEmptyInput` — nil in → empty out, no panic.

### `internal/tui/model_browser_test.go` (new)

- `TestModelItemFilterValueIsID` — `FilterValue` returns the model ID (drives the list's built-in fuzzy filter).
- `TestModelItemDescriptionHasProviderLocationSourceTags` — Description contains all four metadata columns. Without this the browser would just show IDs.
- `TestRefreshBrowserFiltersByTag` — given a cfg + cached models, `refreshBrowser` with `browserTag="design"` keeps only design-tagged entries.
- `TestRefreshBrowserEmptyTagShowsAll` — `browserTag=""` keeps everything in the cache.
- `TestRefreshBrowserUsesCachedDiscovery` — call refresh twice without changing inputs; `Discover` must not be hit twice. Inject a fake `Discoverer` via a `registry` test seam (the existing `Discoverer` interface in `discover.go` is the seam; or factor `registry.Discover` to accept a `[]Discoverer` for testability — see `registry_test.go` for the precedent).
- `TestBrowserKeyOpensBrowser` — `m` from `phaseModel` lands in `phaseBrowser`. Replaces lesson-14's `TestModelKeyShowsPlaceholder` (the placeholder string is gone).
- `TestBrowserEscReturnsToModelPhase` — `esc` from `phaseBrowser` returns to `phaseModel` and does NOT issue `tea.Quit`. From `phaseModel`, `esc` still issues `tea.Quit`.
- `TestBrowserEnterPicksModel` — Enter on a browser item sets `m.current` to the picked model, returns to `phaseModel`, and does not advance the rotation state file (assert the file is untouched after the press).
- `TestBrowserEnterIgnoresNonModelItem` — defensive: if `SelectedItem()` is somehow not a `modelItem`, Enter is a no-op.
- `TestBrowserTagFilterToggle` — `f` cycles `browserTag` between `""` and `m.tag` and the list shrinks/grows accordingly.
- `TestBrowserViewRendersList` — `View()` in `phaseBrowser` includes `"Model browser"` in the output.
- `TestBrowserIgnoresKeysWhenNotOpen` — `f`, `enter`, `c` are no-ops in `phaseModel` / `phaseList`.

### `internal/tui/app_test.go` (edit)

- Replace `TestModelKeyShowsPlaceholder` with `TestModelKeyOpensBrowser` — pressing `m` in `phaseModel` now sets `m.phase = phaseBrowser` instead of writing a status string.

## Run It

```bash
go run ./cmd/wt
```

Pick a worktree, press `m` to open the browser. You'll see curated + discovered
models with provider/location/source/tags columns. Filter by typing; press `f`
to toggle tag filtering; press `enter` to select a model; `esc` to go back.

## Try It Yourself

Wire the `c` key (browser-only) to cycle a `sourceCycle int` through `0 = all
/ 1 = curated / 2 = discovered`, applying `registry.FilterBySource` on each
press. Reuse the discovery cache; only the filter helper runs.

<details>
<summary>Solution sketch</summary>

```go
case "c":
	if m.phase == phaseBrowser {
		m.sourceCycle = (m.sourceCycle + 1) % 3
		// 0 = all (no filter), 1 = curated, 2 = discovered
		switch m.sourceCycle {
		case 1:
			m.browserSource = config.SourceCurated
		case 2:
			m.browserSource = config.SourceDiscovered
		default:
			m.browserSource = ""
		}
		m.refreshBrowser()
	}
```

`refreshBrowser` applies `FilterByTag` then `FilterBySource` over the cache.
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 15: model browser" && git tag lesson-15
```
