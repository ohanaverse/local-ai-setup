# Design: Remove agent default; always show the agent/command picker

Date: 2026-08-21

## Problem

Running `wt` with no options is broken and confusing:

1. **The agent picker is skipped.** `cmd/wt/main.go` resolves the agent to the
   config default (`a.cfg.DefaultAgent()` — the first configured agent,
   e.g. `copilot`) *before* handing control to the TUI. The TUI treats any
   non-empty `initialAgent` as "pinned" and goes straight to that agent's
   model screen, so the user never gets to choose an agent.
2. **Model selection silently does nothing.** Because the user is dumped onto
   the default agent's model screen (and that agent may not even be
   installed), pressing Enter can fail to launch. The error is written to
   `m.status`, but `phaseModelView()` never renders `m.status`, so the
   failure is invisible — the UI appears frozen.
3. **The agent+command picker is unsorted.** `buildAgentList` emits configured
   agents in config order, then registered drivers in map (nondeterministic)
   order. It should be deterministic: agents alphabetically, then commands
   alphabetically.

## Goals

- Remove the implicit agent/command default entirely. The agent is resolved
  only from `-A` or the user's choice in the agent/command picker.
- **Always show the agent/command picker when `-A` is not provided** — for
  plain `wt` *and* for `-W`, `--cwd`, and non-git launches (with the worktree
  already resolved so the worktree picker stays skipped).
- Surface launch/config errors on the model screen so a failed launch is never
  silent.
- Sort the agent/command picker deterministically: agents alphabetically, then
  commands alphabetically (only `shell` today).

## Design

### 1. `cmd/wt/main.go` — remove the default, route no-agent launches through the picker

Replace the agent-defaulting block:

```go
// Resolve the agent: --agent flag wins, else the config default.
agent := agentFlag
if agent == "" {
    agent = a.cfg.DefaultAgent()
}
```

with no defaulting:

```go
// The agent comes only from --agent or the picker. No implicit default.
agent := agentFlag
```

`launchFiltered` keeps the automated (rotation-based) non-interactive path, but
is only called when `-A` was given. When `agent == ""`, each launch branch
routes through `tui.Run` with the worktree pre-selected via a new `prePath`
argument:

- `-W <name>`: create/reuse the worktree as today, then
  `tui.Run(yolo, "", tags, family, args, theme, path)` when no agent; else
  `launchFiltered(agent, path, ...)`.
- `--cwd`: `tui.Run(..., root)` when no agent; else `launchFiltered(agent, root, ...)`.
- Not a git repo: `tui.Run(..., ".")` when no agent; else `launchFiltered(agent, ".", ...)`.
- Plain interactive `wt`: `tui.Run(yolo, agent, ..., "")` — worktree picker
  shown first (unchanged), `prePath=""`.

Edge case (kept simple per YAGNI): `-M` (pin a model) without `-A` now routes
to the picker, where the pin is unused — consistent with "the agent must be
user-provided." `*-wt` shims always pass `-A` and are unaffected.

### 2. `internal/tui`: `prePath` to skip the worktree picker

Add a `prePath string` field to the `model` struct and a matching trailing
argument to `Run`. When set, `Init()` emits a `selectedEntryMsg{Path: prePath}`
instead of `loadEntriesCmd()`, so control flows through the existing
`proceedFromSelectedPath`:

- unpinned (`initialAgent == ""`) → builds the agent+command picker
  (`phaseAgent`);
- pinned (`initialAgent != ""`) → goes straight to the model phase.

This reuses the existing flow and works outside a git repo (no
`git rev-parse` runs because worktree enumeration is skipped).

### 3. `internal/tui/model_list.go`: render `m.status` on the model screen

`phaseModelView()` currently drops `m.status`, which is the "hitting Enter does
nothing" symptom. Prepend the rendered status (via `ErrorStyle`) above the
list/header/footer so launch, config, session, and ollama-check failures are
always visible. (`phaseAgentView` already renders status.)

### 4. `internal/tui/agent_picker.go`: deterministic ordering

Rewrite `buildAgentList` to partition into agents (non-command) and commands,
`sort.Slice` each by name, and concatenate agents-then-commands. Deterministic
regardless of config order or `agents.Names()` map iteration order.

## Testing

- `internal/tui`: `buildAgentList` ordering (agents alpha then commands alpha);
  `phaseModelView` renders `m.status`; TUI with `prePath` skips the worktree
  picker and lands in the agent picker (unpinned) or the model phase (pinned),
  and works with a non-git `prePath`.
- `cmd/wt`: existing `launchFiltered` tests remain green (agent is always
  non-empty there).
- Manual: `wt` shows agent picker after a worktree; `wt -W feat` shows the agent
  picker with the worktree pre-selected; `wt -A claude` skips the agent picker.

## Non-goals

- No changes to model rotation, the worktree picker, the ollama check, session
  resume, or the `*-wt` shims.
