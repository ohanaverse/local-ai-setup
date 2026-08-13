# Lesson 16: Launching the agent

## Concept Intro

The final act of the TUI: launch the chosen agent with the chosen model in the
chosen worktree. This uses the driver abstraction from lesson 6 to build the
`exec.Cmd`, then hands control over to the subprocess.

The critical detail is the **TUI → subprocess handoff**: Bubble Tea runs in the
alternate screen buffer, so we must stop the TUI (`p.ReleaseTerminal()` /
`tea.Program` quit) *before* starting the agent, or the agent's output will be
mangled. The clean pattern is to emit a `launchMsg` carrying the built command,
and in response: return `tea.Quit`, then run the command and stream its output
to the terminal. In practice we build the command, `ReleaseTerminal()`, run it,
and restore the terminal afterward.

We also handle the `--yolo` flag by passing it through the driver's
`YoloFlag()`.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `tea.Program.ReleaseTerminal()` | Leaves the alternate screen so a subprocess can take over. |
| `tea.Program.RestoreTerminal()` | Returns to the TUI afterward. |
| `cmd.Stdout = os.Stdout` / `cmd.Stderr` | Wire the subprocess to the real terminal. |
| `cmd.Run()` | Runs and waits for the subprocess to exit. |
| launch message | A `tea.Msg` carrying `(*exec.Cmd, error)`. |
| `tea.Batch(quit, runCmd)` | Quits the TUI and runs the agent. |

## Worked Walkthrough

Add a launch command builder in the agents package (lesson 6 already has
`Command`). We just need the TUI side to use it. Create
`internal/tui/launch.go`:

```go
package tui

import (
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// launchMsg tells the app to quit the TUI and run the agent.
type launchMsg struct {
	cmd  *exec.Cmd
	path string
}

// launchAgent builds the command for agent/model in worktreePath.
func launchAgent(agent string, m config.Model, worktreePath string, yolo bool) (*exec.Cmd, error) {
	d := agents.ByName(agent)
	if d == nil {
		return nil, agentUnknownError{agent: agent}
	}
	return agents.Command(d, m, yolo, worktreePath)
}

type agentUnknownError struct{ agent string }

func (e agentUnknownError) Error() string { return "unknown agent: " + e.agent }
```

Now handle Enter on the model phase to build and send the launch. In `Update`,
replace the Enter handling for the model phase:

```go
case "enter":
	if m.browserOpen {
		// pick model (lesson 15)
		...
	} else if m.phase == phaseModel {
		cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo)
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		return m, tea.Batch(tea.Quit, runAndWaitCmd(cmd))
	}
```

We need `m.selectedPath` (the worktree path chosen in lesson 13) and `m.yolo`.
Add them to `model` and set them when the worktree is selected. The command
that releases the terminal and runs the agent:

```go
// runAndWaitCmd releases the TUI, runs cmd, restores the TUI, and quits.
func runAndWaitCmd(cmd *exec.Cmd) tea.Cmd {
	return func() tea.Msg {
		p := currentProgram()
		p.ReleaseTerminal()

		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()

		p.RestoreTerminal()
		return launchDoneMsg{err: err}
	}
}

// currentProgram returns the running tea.Program (set at Run() time).
var currentProgram = func() *tea.Program {
	// In practice, store the program in a package var when Run() starts.
	return nil
}
```

To keep the program handle available, store it in a package var in `Run`:

```go
// in app.go
var prog *tea.Program

func Run() error {
	prog = tea.NewProgram(model{status: "ready"}, tea.WithAltScreen())
	_, err := prog.Run()
	return err
}
```

and make `currentProgram` return it. After the agent exits, the program is
already quitting, so `launchDoneMsg` can just return `tea.Quit` in `Update`:

```go
case launchDoneMsg:
	if msg.err != nil {
		m.status = "agent exited: " + msg.err.Error()
	}
	return m, tea.Quit
```

## Run It

```bash
go run ./cmd/wt --yolo
```

Pick a worktree, a model, press Enter. The TUI releases, and the agent runs
interactively in the terminal. When the agent exits, the TUI restores briefly
and quits.

## Try It Yourself

Support **session resume** on launch: before launching a claude/opencode agent,
check `session.LatestForAgent` (lesson 11) and, if a session exists, append the
agent's resume flag (e.g. `--resume <id>` for claude) to the command args.

<details>
<summary>Solution</summary>

```go
if s := sessionLatest(m.agent, m.selectedPath); s != nil {
	switch m.agent {
	case "claude":
		cmd.Args = append(cmd.Args, "--resume", s.ID)
	case "opencode":
		cmd.Args = append(cmd.Args, "--session", s.ID)
	}
}
```

(Add a `sessionLatest` helper wrapping `session.LatestForAgent`, ignoring errors.)
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 16: launch the agent" && git tag lesson-16
```
