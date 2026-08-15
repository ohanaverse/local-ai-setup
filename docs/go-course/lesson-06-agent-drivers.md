# Lesson 6: Agent driver abstraction

## Concept Intro

The whole point of `wt` is to launch a coding agent in a chosen worktree with
a chosen model. But each agent passes a model to its CLI *differently*:

- **claude** — cloud models via env vars (`ANTHROPIC_BASE_URL`,
  `ANTHROPIC_AUTH_TOKEN=ollama`) plus `--model`; native model via no args.
- **codex** — `--model <name>`.
- **pi** — `--model <name>`.
- **copilot** — env vars (`COPILOT_PROVIDER_BASE_URL`,
  `COPILOT_PROVIDER_API_KEY`, `COPILOT_MODEL`).
- **opencode** — a JSON config env var (`OPENCODE_CONFIG_CONTENT`).
- **agy** — no model passthrough at all (you pick inside its TUI via `/model`).

The bash wrappers duplicated this logic per launcher. We factor it into a
single **driver interface** so the TUI and CLI can say "launch agent X with
model Y in directory Z" without knowing the agent's quirks.

Each driver builds a `LaunchCmd`: the binary to run, the args to pass (which
may include `--model` or the yolo flag), and the environment variables to set
(which may include model-provider vars). A `--yolo` skip-permissions flag is
added per driver too, since each agent uses a different flag.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `type Driver interface` | `Build(m config.Model, yolo bool) LaunchCmd` — the abstraction seam. |
| `LaunchCmd` | Plain struct: `Bin string`, `Args []string`, `Env []string`. |
| `exec.CommandContext(ctx, ...)` | Builds a command that can be cancelled (used by the TUI later). |
| `cmd.Env = append(os.Environ(), extra...)` | Merges custom env vars with the inherited environment. |
| registry of drivers | `map[string]Driver` keyed by agent name. |
| `LookPath` | Resolves an executable before building (to detect "not installed"). |

> **Note on native detection.** The lesson text below uses `m.Native`, but the
> `config.Model` struct has no `Native` field. Native models are identified by
> `ModelName == "native"` (e.g. `claude/native`), so the implementation adds a
> `Model.IsNative()` helper in `internal/config/config.go` and the drivers use
> `m.IsNative()` in place of `m.Native`.

## Worked Walkthrough

Create `internal/agents/agents.go`:

```go
package agents

import (
	"os"
	"os/exec"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// LaunchCmd describes a fully-built process to exec.
type LaunchCmd struct {
	Bin  string
	Args []string
	Env  []string // extra env vars, merged over os.Environ() by the caller
}

// Driver knows how to build a launch command for one agent.
type Driver interface {
	// Build returns the command to run agent for the given model.
	// yolo adds the agent's skip-permissions flag.
	Build(m config.Model, yolo bool) LaunchCmd
	// YoloFlag is the agent's permission-skip flag.
	YoloFlag() string
}

// Installed checks whether the agent's binary is on PATH.
func (d *claudeDriver) Installed() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// registry maps agent name -> driver constructor.
var registry = map[string]func() Driver{}

func register(name string, f func() Driver) { registry[name] = f }

// ByName returns the driver for an agent, or nil if unknown.
func ByName(name string) Driver {
	f, ok := registry[name]
	if !ok {
		return nil
	}
	return f()
}

// Names lists registered agents.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	return out
}
```

### Claude driver

Claude is the trickiest: cloud models require the Anthropic-compatible env
vars pointing at the ollama gateway, plus `--model`; native models use no
special args.

Create `internal/agents/claude.go`:

```go
package agents

import (
	"fmt"
	"os/exec"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() {
	register("claude", func() Driver { return claudeDriver{} })
}

type claudeDriver struct{}

func (claudeDriver) YoloFlag() string { return "--dangerously-skip-permissions" }

func (claudeDriver) Build(m config.Model, yolo bool) LaunchCmd {
	args := []string{}
	if yolo {
		args = append(args, claudeDriver{}.YoloFlag())
	}
	lc := LaunchCmd{Bin: "claude", Args: args}

	switch {
	case m.IsNative():
		// native model — no extra args/env
		return lc
	case m.Location == config.LocationCloud:
		// cloud model via the ollama Anthropic-compatible gateway
		lc.Env = append(lc.Env,
			"ANTHROPIC_AUTH_TOKEN=ollama",
			"ANTHROPIC_API_KEY=",
			"ANTHROPIC_BASE_URL=http://localhost:11434",
		)
		lc.Args = append(lc.Args, "--model", m.ID)
	default:
		// local model — same env gateway, still pass the model name
		lc.Env = append(lc.Env,
			"ANTHROPIC_AUTH_TOKEN=ollama",
			"ANTHROPIC_API_KEY=",
			"ANTHROPIC_BASE_URL=http://localhost:11434",
		)
		lc.Args = append(lc.Args, "--model", m.ID)
	}
	return lc
}
```

The `Installed` method we referenced earlier lives here too — fix it to be a
package function instead of a method on the unexported struct. Put it at
package level:

```go
// Installed reports whether the claude binary is on PATH.
func claudeInstalled() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}
```

Remove the erroneous `Installed` method from `agents.go` and add a generic
helper instead:

```go
// Installed reports whether bin resolves on PATH.
func Installed(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
```

### Codex driver (the simplest)

Create `internal/agents/codex.go`:

```go
package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("codex", func() Driver { return codexDriver{} }) }

type codexDriver struct{}

func (codexDriver) YoloFlag() string { return "--skip-permission" }

func (codexDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "codex"}
	if yolo {
		lc.Args = append(lc.Args, "--skip-permission")
	}
	if !m.IsNative() {
		lc.Args = append(lc.Args, "--model", m.ID)
	}
	return lc
}
```

The other drivers (copilot, pi, opencode, agy) follow the same shape — see
the challenge.

### Using the driver

A convenience builder that resolves the binary and returns an `exec.Cmd`
ready to run in a directory:

```go
// In agents.go
func Command(d Driver, m config.Model, yolo bool, workdir string) (*exec.Cmd, error) {
	lc := d.Build(m, yolo)
	bin, err := exec.LookPath(lc.Bin)
	if err != nil {
		return nil, fmt.Errorf("agent %s not installed", lc.Bin)
	}
	cmd := exec.Command(bin, lc.Args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), lc.Env...)
	return cmd, nil
}
```

## Run It

```go
// quick check in main
d := agents.ByName("codex")
if d == nil {
	fmt.Println("codex driver registered")
} else {
	lc := d.Build(config.Model{ID: "deepseek-v4-pro:cloud", Location: config.LocationCloud}, false)
	fmt.Println(lc.Bin, lc.Args)
}
```

```bash
go run ./cmd/wt
```

```
codex ["--model" "deepseek-v4-pro:cloud"]
```

## Try It Yourself

Implement the **copilot** driver. It never passes `--model`; instead it sets
env vars for cloud models:

- `COPILOT_PROVIDER_BASE_URL` — the ollama gateway (`http://localhost:11434`)
- `COPILOT_PROVIDER_API_KEY` — empty
- `COPILOT_MODEL` — the model id

Skip all env for native models. Yolo flag: `--dangerously-skip-permissions`.

<details>
<summary>Solution</summary>

```go
package agents

import "github.com/ohanaverse/agent-worktree/internal/config"

func init() { register("copilot", func() Driver { return copilotDriver{} }) }

type copilotDriver struct{}

func (copilotDriver) YoloFlag() string { return "--dangerously-skip-permissions" }

func (copilotDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "copilot"}
	if yolo {
		lc.Args = append(lc.Args, copilotDriver{}.YoloFlag())
	}
	if !m.IsNative() {
		lc.Env = append(lc.Env,
			"COPILOT_PROVIDER_BASE_URL=http://localhost:11434",
			"COPILOT_PROVIDER_API_KEY=",
			"COPILOT_MODEL="+m.ID,
		)
	}
	return lc
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 06: agent driver abstraction" && git tag lesson-06
```
