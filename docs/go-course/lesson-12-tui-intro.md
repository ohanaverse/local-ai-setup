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
go get github.com/charmbracelet/bubbletea \
       github.com/charmbracelet/lipgloss
```

(`bubbles` is added in lesson 13, when we wire up `list.Model`.)

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

In `cmd/wt/main.go`, replace the final placeholder (`fmt.Println("(TUI not yet
implemented - coming in lesson 12)")`) with a call to `tui.Run()`. It must
come *after* all the existing flag handlers (`--init`, `--version`,
`--rotate-tag`, `-w`, `--debug-session`, `--debug-worktrees`) so those flags
still take their early-return paths and never reach the TUI:

```go
import "github.com/ohanaverse/agent-worktree/internal/tui"

// ...inside rootCmd().RunE, after the --debug-worktrees branch:

return tui.Run()
```

The full RunE currently looks like:

```go
cmd.RunE = func(cmd *cobra.Command, args []string) error {
    // --init         → seed agent files, install guard, exit
    // --version      → print version, exit
    // --rotate-tag   → print next model in tag group, exit
    // -w <name>      → ensure worktree, print path, exit
    // --debug-session <agent> → print newest session, exit
    // --debug-worktrees       → print entries, exit
    return tui.Run()  // ← this lesson's change
}
```

(We'll add an "outside a git repo" passthrough in lesson 17; for now `tui.Run`
is the unconditional fallback for an interactive launch.)

## Run It

```bash
go run ./cmd/wt
```

With no flags set, the TUI is reached (all five flag handlers skip themselves)
and the alternate-screen full-screen view appears with `wt`, the status
(`ready`), and "Press q to quit". Press `q` to exit.

> **TTY required.** Bubble Tea's `tea.WithAltScreen()` opens `/dev/tty` to
> manage the alternate screen buffer. Running from a non-TTY context — a
> pipe (`go run ./cmd/wt | tee out.log`), a CI runner, an editor's output
> panel — fails with:
>
> ```
> Error: could not open a new TTY: open /dev/tty: device not configured
> ```
>
> Run from a real terminal session (Terminal.app, iTerm, gnome-terminal,
> Windows Terminal, etc.). The other flag paths (`--version`, `-w`,
> `--rotate-tag`, etc.) work fine without a TTY — they print and exit before
> `tui.Run()` is reached.

To confirm the other flag paths still work and bypass the TUI:

```bash
go run ./cmd/wt --version           # prints "wt 0.1.0", no TUI
go run ./cmd/wt -w my-feature       # prints worktree path, no TUI
go run ./cmd/wt --rotate-tag code   # prints next code model (or an error if the tag group is empty), no TUI
```

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
