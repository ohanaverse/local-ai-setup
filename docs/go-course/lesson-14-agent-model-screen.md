# Lesson 14: Agent+model screen

## Concept Intro

This is the heart of the hybrid flow you chose. After the user picks a
worktree (lesson 13), they land on an **agent+model screen** that shows:

- the current agent (e.g. `claude`), and
- the current model (e.g. `kimi-k2.6:cloud`),

with two key actions:
- **`r`** — rotate to the next model in the active tag group (using
  `rotation.Next` from lesson 5), and
- **`m`** — open the full model browser (lesson 15).

Enter launches the agent with the currently shown model (lesson 16). This
screen replaces the silent auto-rotation of the bash tool with an explicit
one-keystroke action, exactly as we designed.

The screen keeps a `state` value: which agent is selected, the active tag
group, the current model, and whether we're rotating or launching. It renders
two lines: a status line and a keybind hint.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `tea.KeyRune` | Matches a printable key by its rune. |
| screen state | A sub-struct holding `agent`, `tag`, `model`, `phase`. |
| `tea.Batch(cmds...)` | Runs several commands and delivers all their messages. |
| launch message | A custom `tea.Msg` carrying the resolved model + agent. |
| deferred model resolution | Resolve rotation *on the keypress*, not at construction. |

## Worked Walkthrough

Add a new phase to the app. Extend `model` in `app.go`:

```go
type phase int

const (
	phaseList phase = iota // worktree list (lesson 13)
	phaseModel             // agent+model screen (this lesson)
)

type model struct {
	status string
	width  int
	height int

	entries  []worktree.Entry
	list     list.Model
	loading  bool
	ready    bool

	phase  phase
	agent  string          // current agent name
	tag    string          // active rotation tag group
	current config.Model   // currently shown model
	cfg    *config.Config
}
```

Add `selectedEntryMsg` handling: when a worktree is chosen, move to the model
phase and pick an initial agent + model (first agent default; first model in
the default tag group). In `Update`:

```go
case selectedEntryMsg:
	// (worktree selection is stored for lesson 16's launch)
	m.phase = phaseModel
	m.agent = firstAgent(m.cfg)             // e.g. "claude"
	m.tag = m.cfg.DefaultTag                // e.g. "code"
	m.current = firstModel(m.cfg, m.tag)    // first model in tag group
	return m, nil
```

Helpers:

```go
func firstAgent(cfg *config.Config) string {
	// first agent_default entry, or "claude"
	if len(cfg.AgentDefaults) > 0 {
		return cfg.AgentDefaults[0].Agent
	}
	return "claude"
}

func firstModel(cfg *config.Config, tag string) config.Model {
	ms := cfg.ModelsWithTag(tag)
	if len(ms) == 0 {
		return config.Model{ID: "(none)", Provider: "", Location: config.LocationCloud}
	}
	return ms[0]
}
```

Now the model-phase keybinds in `Update`. Add cases for the `r` (rotate) and
`m` (open browser placeholder) keys:

```go
case tea.KeyMsg:
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "r":
		if m.phase == phaseModel {
			// Rotate to next model in the active tag group.
			rot := rotation.ForTag(m.cfg, m.tag)
			next, ok := rot.Next("")
			if ok {
				m.current = next
			}
		}
	case "m":
		if m.phase == phaseModel {
			m.status = "model browser coming in lesson 15"
		}
	}
```

And render the model phase distinctly in `View`:

```go
func (m model) View() string {
	if m.phase == phaseModel {
		style := lipgloss.NewStyle().Padding(2, 2)
		return style.Render(
			fmt.Sprintf("agent : %s\nmodel : %s\n\ntag : %s\n\n"+
				"[r] rotate   [m] browse models   [enter] launch   [q] quit",
				m.agent, m.current.ID, m.tag))
	}
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

Select a worktree, then on the agent+model screen press `r` repeatedly to cycle
the model and watch the state file advance (`~/.config/agent-wt/rotation-code.state`).
Press `m` to see the placeholder; `q` to quit.

## Try It Yourself

Make the rotation respect the *cross-tag* skip: pass the other tag group's
name to `Next`. Since we only track the active tag for now, add a second tag
(e.g. `design`) as a toggleable key (`d`) and rotate `code` against `design`.

<details>
<summary>Solution</summary>

Track `m.otherTag` and toggle it with `d`:

```go
case "d":
	if m.tag == "code" {
		m.tag, m.otherTag = "design", "code"
	} else {
		m.tag, m.otherTag = "code", "design"
	}
// and in rotate:
next, ok := rot.Next(m.otherTag)
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 14: agent+model screen" && git tag lesson-14
```
