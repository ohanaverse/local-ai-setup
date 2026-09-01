---
name: adding-a-wt-agent
description: Steps to add a new AI agent driver to the wt launcher (internal/agents driver, bin shim, docs entry). Use when asked to add support for a new agent/CLI to wt.
---

## Adding a new agent

1. Add a driver in `internal/agents/<name>.go` implementing `Build`/`YoloFlag`, registered via `register("<name>", ...)`.
2. Add a shim `bin/<name>-wt`: `exec wt --agent <name> "$@"`.
3. Add a doc to `docs/wt-agents/`.

No Makefile changes needed: `make install` copies `bin/*` by glob and
`make uninstall` removes `$(BINDIR)/*-wt`, so the new shim is picked up (and
removed) automatically. The smoke-test loop in `make test` also globs, but
note the config-validation tests in `internal/config` enumerate agents by
name — check `config_test.go` if the agent needs a `[[agents]]` entry.
