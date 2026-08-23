# DeepSeek Harness Support

## Overview

Add support for [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness)
(`dsh`) to wt. `dsh` is an open-source coding agent whose single binary ships
three run modes:

- **Browser** — the terminal-on-the-web TUI that `ollama launch dsh` opens by
  default; serves on `http://127.0.0.1:3080` in the user's browser.
- **Terminal TUI** — an interactive terminal app, installed via the optional
  turtle-ui `pnpm` profile plugin.
- **Headless** — a non-interactive one-shot CLI: it takes a task string as its
  positional argument, prints the final answer to stdout, and exits.

This feature only supports the **ollama** provider (dsh is currently
ollama-only — no native provider, just as the local `pi` agent is).

wt launches `dsh` through Ollama:

```shell
ollama launch dsh --model <name>
ollama launch dsh --model <name> -- --port 3081
ollama launch dsh --config         # configure without launching
```

The `dsh` binary resolves its run mode from the `--profile` flag (default
browser). Ollama stores its managed settings in
`~/.ollama/launch/dsh/settings.yaml`.

## Why

`dsh` is a third-party coding agent that fits wt's purpose (launch an AI coding
agent in a worktree). wt's design is one agent per driver, and the three modes
have materially different behavior (interactive browser vs. non-interactive
one-shot), so they are three separate agents. Each mode shares one config
concern — web search — which requires Ollama cloud access
(`ollama signin`) and is off by default unless the user opts in ("user
feedback and dsh documentation").

## Approach Rationale

**Register three agents**, one per run mode, each a driver in
`internal/agents/` that runs `ollama launch dsh`. This matches wt's existing
one-agent-per-file pattern and keeps the modes isolated: each driver composes
over a shared ollama-launch command builder, with only per-mode arguments (the
profile selector and, for the terminal/browser modes, the yolo flag). The
web-search toggle is an agent-level config option surfaced by the launcher.

Each dsh driver runs `ollama launch dsh --model <bare model name>`. The bare
provider name is `m.ModelName`, per wt's model-id contract elsewhere (never
`m.ID`, which would carry the `provider/` prefix). The `--` passthrough args
that wt forwards after `--` are appended after the model flag, so
`dsh-webui -- --port 3081` becomes `ollama launch dsh --model <m> -- --port
3081`.

The design for isolation: each agent wraps `ollama launch dsh` with minimal
shared plumbing that varies only in the profile argument and the yolo/web-search
args it prepends. That's small enough for a single implementation plan; no
decomposition into sub-projects is needed.

## Design Details

### Agent identifiers

Three agents, one per run mode, each with a `dsh-` prefix so the group is
obvious and distinct from the shared `dsh` binary:

| wt agent | dsh run mode | profile flag | notes |
|---|---|---|---|
| `dsh-webui` | browser TUI | (default; `ollama launch dsh`) | default mode |
| `dsh-tui` | terminal TUI | `--profile tui` | requires the turtle-ui plugin |
| `dsh-headless` | CLI one-shot | `--profile headless` | non-interactive; task string as positional arg |

### Driver design

Each driver is a tiny struct in `internal/agents/deepseek`:

```go
package deepseek

func init(d *Register) {
    d.Register("dsh-webui", webuiDriver{})
    d.Register("dsh-tui", tuiDriver{})
    d.Register("dsh-headless", headlessDriver{})
}
```

Each driver's `Run(cmd *exec.Cmd, extraArgs []string, profile string)` builds the
`ollama launch dsh --model <name> -- <extraArgs>` command with the profile flag,
and `IsHeadless() bool` (true only for `dsh-headless`).

- `dsh-webui` / `dsh-tui` prepend the yolo flag (`WebSearch`) when the user opts
  in (see Web search below). `dsh-headless` takes no yolo flag; process permission
  mode is controlled by an env var the launcher does not manage.
- All drivers pass the bare model name (`m.ModelName`), not `m.ID`.
- All drivers append the user's passthrough args (`extraArgs`) after the model
  flag, wrapped by wt's passthrough handling (`dsh-webui -- --port 3081`).

The headless driver's headless mode can be tested with a mock `dsh` output; the
non-headless tests assert only that the correct profile flag/args are present
and that the model name is bare (`dsh-webui-headless-bare-model`).

### Model selection

**No model picker** for these agents (they have no model layer). wt
authoritatively pins the model in the launch command via `--model <name>`, so
there's no eligible-models list or rotation for dsh agents. Add these three
agents to the "no model layer" check (`IsCommand` / similar), matching how wt
handles `shell` and the local `pi` agent. Because they have no model layer, wt does not surface the usual "multiple models match" behavior for them.

### Launch & resume

wt launches each dsh agent via `ollama launch dsh`. **No session resume** for
dsh agents — wt starts a fresh `dsh` session each launch. This is a conservative
default; resume support is out of scope.

### Web search

Web search requires Ollama cloud access (`ollama signin`) and a model that
supports tools. It is **off by default** (per user feedback). When the user opts
in through an agent config option, wt passes the flag per mode (exact flag
pending investigation / dsh docs). This is the one config concern shared by the
two interactive modes.

### Configuration

Config is open-ended, so all configuration changes live in the user's
`~/.config/agent-wt/config.toml` under `[agents]`. No Go changes required — this
is a config-only change: configure the new agents via the `wt config` TUI so the
user manages them directly.

Each dsh agent's `[agents]` block pins it to the ollama provider only:

```toml
[[agents]]
name = "dsh-webui"
supported_providers = ["ollama"]
default_provider = "ollama"

[[agents]]
name = "dsh-tui"
supported_providers = ["ollama"]
default_provider = "ollama"

[[agents]]
name = "dsh-headless"
supported_providers = ["ollama"]
default_provider = "ollama"
```

Note: `dsh-tui` requires the turtle-ui terminal profile, which is not shipped by
default; the dsh README documents it. Each agent must be added to
`config.toml`.

The config schema for agents (`config.Agent`: name + supported/default providers
via `config.UpsertAgent`) supports this with no schema change. The `ollama`
provider (local gateway `config.OllamaBaseURL`) satisfies `dsh` out of the box.

### Docs

Update `docs/wt-agents/README.md` to list the three dsh agents with a short
description and add per-mode pages consistent with the existing per-agent
reference style. `dsh-headless` documents the headless mode's non-interactive
nature; others reference the browser/terminal TUI behavior, `:3080`, and where
the model picker lives.

## Testing

- **Web search toggle**: a per-env flag on the launch command when opted in
  (`dsh-tui-web-search`) — no arg otherwise.
- **Per-mode args**: assert the correct `--profile <mode>` selector in each
  driver's build.
- **Bare model**: assert the model name is bare (bare-model).
- **Passthrough**: assert user args land after the model flag.
- **Headless mode round-trip**: headless writes the final answer to stdout and
  exits 0 (success), exits 1 or non-zero for other outcomes (failure).
- **No resume**: assert no `--resume`/`--session` is present for any dsh agent.

## Scope

Three agents (one per mode), one config option (web search), docs. No model
layer, no rotation, no session resume. This is a small, self-contained change
covered by a single implementation plan.