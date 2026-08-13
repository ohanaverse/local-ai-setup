# Lesson 13: Worktree list screen

## Concept Intro

Now we render the worktree/branch picker as a real list. Bubble Tea provides
`bubbles/list` — a fully-featured list widget with cursor movement, filtering,
and custom rendering of items. This is the port of the fzf picker from
`wt-core.sh`, but richer.

We feed it the `Entry` values from the `worktree` package (lesson 7). Each item
implements `list.Item` (`Title()`, `Description()`, `FilterValue()`) which
formats the entry as a fixed-width row (analog of `format_entries`'s
`tag/branch/path` columns). When the user presses Enter, we send a message back
to the parent model saying "this entry was selected", and the parent decides
what to do (in later lessons: create a worktree and launch).

Key concepts: wrapping our data in a `list.Item`, giving `list.New` a custom
delegate (for styling the selected item), and handling Enter to emit a
selection message.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `list.Model` | The list widget: `list.New(items, delegate, width, height)`. |
| `list.DefaultDelegate` | Renders title + description lines with default styling. |
| `list.Item` interface | `FilterValue() string` + `Title()` + `Description()`. |
| `tea.KeyEnter` | The Enter key message. |
| selection message | A custom `tea.Msg` type carrying the chosen `worktree.Entry`. |
| `m.list.SetItems(...)` | Replaces the list's items. |

## Worked Walkthrough

Create `internal/tui/worktree_list.go`:

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// entryItem adapts a worktree.Entry to a list.Item.
type entryItem struct {
	entry worktree.Entry
}

// FilterValue is used by the list's built-in filter.
func (e entryItem) FilterValue() string { return e.entry.Branch }

// Title renders the branch name (remote prefix stripped for display).
func (e entryItem) Title() string {
	b := e.entry.Branch
	if i := strings.IndexByte(b, '/'); i >= 0 {
		b = b[i+1:] // strip remote prefix
	}
	return b
}

// Description renders the metadata columns.
func (e entryItem) Description() string {
	path := e.entry.Path
	if path == "" {
		path = "(no worktree)"
	}
	return strings.TrimSpace(strings.Join([]string{
		pad("["+string(e.entry.Type)+"]", 9),
		path,
	}, " "))
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// selectedEntryMsg is emitted when the user picks a worktree or branch.
type selectedEntryMsg struct{ entry worktree.Entry }
```

Now integrate it into the app model. Extend `model` in `app.go` to hold the
list and a loading state, and fetch entries on startup. Update `app.go`:

```go
type model struct {
	status string
	width  int
	height int

	entries  []worktree.Entry
	list     list.Model
	loading  bool
	ready    bool
}
```

We need to load entries without blocking the UI. Bubble Tea's pattern: `Init`
returns a `Cmd` that does the work in a goroutine and returns a `Msg`. Add a
command:

```go
// loadEntriesCmd returns a command that enumerates worktrees/branches.
func loadEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		root, err := worktree.RepoRoot()
		if err != nil {
			return entriesLoadedMsg{err: err}
		}
		entries, err := worktree.Enumerate(root)
		return entriesLoadedMsg{entries: entries, err: err}
	}
}

// entriesLoadedMsg carries the enumeration result to Update.
type entriesLoadedMsg struct {
	entries []worktree.Entry
	err     error
}
```

In `Init`, kick off the load and show a spinner-ish status:

```go
func (m model) Init() tea.Cmd {
	m.loading = true
	return loadEntriesCmd()
}
```

In `Update`, handle the load result and Enter:

```go
case entriesLoadedMsg:
	m.loading = false
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		return m, nil
	}
	m.entries = msg.entries
	m.list = buildList(msg.entries, m.width-2, m.height-2)
	m.ready = true
case tea.KeyMsg:
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "enter":
		item, ok := m.list.SelectedItem().(entryItem)
		if ok {
			return m, func() tea.Msg { return selectedEntryMsg{entry: item.entry} }
		}
	}
}
```

`buildList` constructs the widget:

```go
func buildList(entries []worktree.Entry, width, height int) list.Model {
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		items = append(items, entryItem{entry: e})
	}
	l := list.New(items, list.NewDefaultDelegate(), width, height)
	l.Title = "Pick a worktree or branch"
	l.SetShowStatusBar(true)
	return l
}
```

Update `View` to render the list instead of the placeholder (when loaded):

```go
func (m model) View() string {
	if m.loading {
		return "loading worktrees..."
	}
	if !m.ready {
		return m.status
	}
	return m.list.View()
}
```

## Run It

```bash
go run ./cmd/wt
```

A list of worktrees and branches appears, navigable with arrows and filterable
by typing. Enter currently sends a `selectedEntryMsg` that is ignored (we'll
handle it in lesson 14).

## Try It Yourself

Use `m.list.InputField()`/the built-in filter: type a substring to filter the
list down (the `FilterValue` we implemented makes this work automatically). Add
a help footer showing the keybinds via `m.list.Help.View(m.list.KeyMap)`.

<details>
<summary>Solution</summary>

In `View`, append the help:

```go
return m.list.View() + "\n" + m.list.Help.View(m.list.KeyMap)
```

The `DefaultKeyMap` already includes `/` for filter and arrows for movement.
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 13: worktree list screen" && git tag lesson-13
```
