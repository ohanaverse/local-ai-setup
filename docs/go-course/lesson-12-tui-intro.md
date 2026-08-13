# Lesson 12: TUI intro

## Concept Intro

Now the payoff: a real TUI instead of an fzf prompt. We use **Bubble Tea**,
charmbracelet's framework. The core mental model is three pieces:

1. **Model** — a Go value that holds the entire UI state.
2. **Update** — a function `(msg Msg) (Model, Cmd)`. It reacts to messages
   (keypresses, timers, results of async work) and returns a new model plus
   optionally a *command* to run (e.g. "run this git command in the
   background and tell me when it's done").
3. **View** — a function returning a `string` rendered to the terminal.

This is a message-passing architecture: the `tea.Cmd` type is
`func() tea.Msg`, and you use `tea.Println`/`exec.Command` wrapped in
commands to do I/O without blocking the UI. This is the Go analog of the
state machine `wt-core.sh` encodes with `case` statements and fzf
sub-processes.

For this lesson we build a minimal app shell: a screen that shows a message,
handles `q`/`ctrl-c` to quit, and demonstrates the Update/View cycle. We also
set up the renderer for an alternate-screen (full-screen) TUI.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `tea.Model` interface | `Init() tea.Cmd`, `Update(msg tea.Msg) (tea.Model, tea.Cmd)`, `View() string`. |
| `tea.KeyMsg` | A message containing the pressed key. |
| `tea.KeyCtrlC` / `tea.KeyEsc` / `rune('q')` | Key identifiers in a switch. |
| `tea.Quit` | A `tea.Cmd` that exits the program. |
| `tea.NewProgram(model, opts...)` | Starts the Bubble Tea runtime. |
| `tea.WithAltScreen()` | Option for full-screen (alternate buffer). |
| `lipgloss.NewStyle()` | Styling helper (colors, padding). |
| `tea.Cmd` | `func() tea.Msg` — a unit of async work. |

## Worked Walkthrough

Add the TUI dependencies:

```bash
go get github.com/charmbracelet/bubbletea@latest \
       github.com/charmbracelet/bubbles@latest \
       github.com/charmbracelet/lipgloss@latest
```

Create `internal/tui/app.go`:

```go
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// model holds the entire UI state. For now, a single screen with a status.
type model struct {
	status string
	width  int
	height int
}

// Init returns any initial commands (none yet).
func (m model) Init() tea.Cmd { return nil }

// Update handles messages and returns the new state plus optional commands.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

// View renders the screen as a string.
func (m model) View() string {
	style := lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Align(lipgloss.Center, lipgloss.Center)
	return style.Render(fmt.Sprintf("wt\n%s\n\nPress q to quit", m.status))
}

// Run starts the TUI and returns when it quits.
func Run() error {
	p := tea.NewProgram(model{status: "ready"}, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
```

### Wiring

Replace the placeholder in `rootCmd`'s `RunE` (but keep `--rotate-tag` and the
guard/init handling from earlier lessons above it):

```go
if interactive {
	// not in a git repo -> just pass through? handle later.
	return tui.Run()
}
```

For now, call `tui.Run()` unconditionally as the fallback:

```go
return tui.Run()
```

## Run It

```bash
go run ./cmd/wt
```

A full-screen view appears with `wt`, the status, and "Press q to quit". Press
`q` to exit.

## Try It Yourself

Add a counter: pressing `space` increments an `int` in the model, and the view
displays it.

<details>
<summary>Solution</summary>

Add a field and a case:

```go
count int
// in Update:
case " ":
	m.count++
// in View, change the inner render:
fmt.Sprintf("wt  count=%d\n%s\n\nPress q to quit", m.count, m.status)
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 12: TUI intro" && git tag lesson-12
```
