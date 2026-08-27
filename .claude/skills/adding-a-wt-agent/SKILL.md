---
name: adding-a-wt-agent
description: Steps to add a new AI agent driver to the wt launcher (internal/agents driver, bin shim, docs entry). Use when asked to add support for a new agent/CLI to wt.
---

## Adding a new agent

1. Add a driver in `internal/agents/<name>.go` implementing `Build`/`YoloFlag`, registered via `register("<name>", ...)`.
2. Add a shim `bin/<name>-wt`: `exec wt --agent <name> "$@"`.
3. Add a doc to `docs/wt-agents/`.
