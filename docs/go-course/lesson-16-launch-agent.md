# Lesson 16: Launching the agent

## Concept Intro

The final act of the TUI: launch the chosen agent with the chosen model in the
chosen worktree. This uses the driver abstraction from lesson 6 to build the
`exec.Cmd`, then hands control over to the subprocess.

The critical detail is the **TUI → subprocess handoff**: Bubble Tea runs in the
alternate screen buffer, so we must stop the TUI (`p.ReleaseTerminal()` /
`tea.Program` quit) *before* starting the agent, or the agent's output will be
mangled. The clean pattern is to build the command, `ReleaseTerminal()`, run it,
and restore the terminal afterward.

We also handle the `--yolo` flag by passing it through the driver's
`YoloFlag()`, and we surface a **session resume prompt** for claude and
opencode when a prior session exists.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `tea.Program.ReleaseTerminal()` | Leaves the alternate screen so a subprocess can take over. |
| `tea.Program.RestoreTerminal()` | Returns to the TUI afterward. |
| `cmd.Stdout = os.Stdout` / `cmd.Stderr` | Wire the subprocess to the real terminal. |
| `cmd.Run()` | Runs and waits for the subprocess to exit. |
| `launchMsg` / `launchDoneMsg` | Messages that carry the built command and the subprocess exit result. |
| `phaseResume` | A new TUI phase that asks whether to resume a prior claude/opencode session. |
| `tea.Batch(tea.Quit, runAndWaitCmd)` | Quits the TUI and runs the agent. |

## Worked Walkthrough

Add a launch command builder in `internal/tui/launch.go`:

```go
package tui

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

// launchMsg tells the app to quit the TUI and run the agent.
type launchMsg struct{ cmd *exec.Cmd }

// launchDoneMsg is emitted after the agent subprocess exits.
type launchDoneMsg struct{ err error }

// currentProgram holds the running tea.Program so runAndWaitCmd can
// release/restore the terminal. It is set in Run().
var currentProgram *tea.Program

// launchAgent builds the command for agent/model in worktreePath, optionally
// appending a resume flag for claude or opencode.
func launchAgent(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session) (*exec.Cmd, error) {
	d := agents.ByName(agent)
	if d == nil {
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
	cmd, err := agents.Command(d, m, yolo, worktreePath)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		switch agent {
		case "claude":
			cmd.Args = append(cmd.Args, "--resume", sess.ID)
		case "opencode":
			cmd.Args = append(cmd.Args, "--session", sess.ID)
		}
	}
	return cmd, nil
}

// runAndWaitCmd releases the TUI, runs the agent with stdio wired to the
// terminal, restores the TUI, and returns a launchDoneMsg.
func runAndWaitCmd(cmd *exec.Cmd) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if currentProgram != nil {
			currentProgram.RestoreTerminal()
		}
		return launchDoneMsg{err: err}
	}
}
```

The resume prompt uses the same `bubbles/list` widget with three choices:

```go
type resumeOption int

const (
	resumeChoice resumeOption = iota
	freshChoice
	cancelChoice
)

type resumeItem struct {
	choice resumeOption
	title  string
	desc   string
}

func buildResumeChoices(sess *session.Session) []list.Item {
	items := []list.Item{
		resumeItem{choice: freshChoice, title: "Start fresh", desc: "Launch without resuming a session"},
		resumeItem{choice: cancelChoice, title: "Cancel", desc: "Return to agent+model screen"},
	}
	if sess != nil {
		items = append([]list.Item{resumeItem{
			choice: resumeChoice,
			title:  fmt.Sprintf("Resume %s", sess.ID),
			desc:   session.RelativeTime(sess.MTime),
		}}, items...)
	}
	return items
}
```

In `internal/tui/app.go`, store the selected worktree path and yolo flag, then
handle Enter in the model phase:

```go
case phaseModel:
	sess, err := session.LatestForAgent(m.agent, m.selectedPath)
	if err != nil {
		m.status = "session check failed: " + err.Error()
		return m, nil
	}
	if sess == nil {
		cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo, nil)
		if err != nil {
			m.status = "launch failed: " + err.Error()
			return m, nil
		}
		return m, tea.Batch(tea.Quit, runAndWaitCmd(cmd))
	}
	m.phase = phaseResume
	m.resume.session = sess
	m.resume.choices = list.New(buildResumeChoices(sess), list.NewDefaultDelegate(), m.width-2, m.height-2)
	m.resume.choices.Title = "Resume previous session?"
	return m, nil
```

The resume prompt has its own Enter handling:

```go
case phaseResume:
	if item, ok := m.resume.choices.SelectedItem().(resumeItem); ok {
		switch item.choice {
		case cancelChoice:
			m.phase = phaseModel
			return m, nil
		case freshChoice:
			cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo, nil)
			...
		case resumeChoice:
			cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo, m.resume.session)
			...
		}
	}
```

Both launch paths return `runAndWaitCmd(cmd)` (without batching it with
`tea.Quit`). `tea.Quit` is intentionally not batched: Bubble Tea fires
`QuitMsg` in a concurrent goroutine and `Program.Run()` would return on
`QuitMsg`, killing the agent via process exit before `cmd.Run()` finished.
By returning only `runAndWaitCmd(cmd)`, the program stays alive while the
agent runs, and `Update(launchDoneMsg)` issues `tea.Quit` after the agent
exits — so the TUI quits cleanly and the agent gets to run to completion.
`runAndWaitCmd` releases and restores the terminal around the agent.

## Run It

```bash
go run ./cmd/wt            # interactive TUI (needs a TTY)
go run ./cmd/wt --yolo     # pass the agent's skip-permissions flag
```

Pick a worktree, a model, press Enter. If a prior claude/opencode session
exists, the TUI shows a resume prompt. Choose **Resume** to append `--resume`
/ `--session`, **Start fresh** to launch without resuming, or **Cancel** to go
back.

> **Update (post-lesson):** `buildResumeChoices` was reordered so
> `freshChoice` is at index 0 — the bubbles/list cursor default. Enter
> therefore launches a fresh session unless Resume is highlighted. Cancel
> still backs out, and Resume is now the last entry when a session exists.

## Try It Yourself

- Add a key (e.g. `l`) as an alternate launch keybind alongside Enter.
- Print a short "launching <agent> with <model>" status just before
  `tea.Quit`.
- Skip the resume prompt entirely when `--cwd` is used (hint: add a `noResume`
  field to `model`).

## Checkpoint

```bash
git add -A && git commit -m "lesson 16: launch the agent with resume prompt" && git tag lesson-16
```
