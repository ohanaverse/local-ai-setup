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

## Driver implementation details

Migrated verbatim from `wt/CLAUDE.md` (2026-09-11 doctor cleanup). NOTE: this
describes registering via `internal/agents/catalog.go`'s `AddEntry()`/
`MustAdd()`, which doesn't match the `register("<name>", ...)` call above —
reconcile which is current before relying on either.

1. Create `internal/agents/<name>.go` implementing the `Driver` interface:
   - `Build(m config.Model, yolo bool, r Route) LaunchCmd` — dial from the resolved `Route` (base origin, API key, model ref); never hardcode a provider endpoint
   - `YoloFlag() string`
   - `Protocols() []Protocol` (the `ProtocolDeclarer` capability) — the wire protocols the agent speaks; this drives route resolution
2. Implement other optional capabilities as needed: `Seeder`, `Syncer`, `ArgSetter`, `Resumer`
3. Register in `internal/agents/catalog.go` via `AddEntry()` or `MustAdd()`
4. Add a model-id regression test (`Test<Name>OllamaPrefix`) using a model with distinct `ID`/`ModelName` to catch wrong id passthrough
5. Update the driver table in `wt/CLAUDE.md`

**Key gotchas:**
- The model ref comes from `Route.ModelRef` — `ResolveRoute` already picked `m.ID` (litellm/forced) or `m.ModelName` (direct); don't re-derive it
- If the agent's protocol is served by no local provider (empty intersection in `ResolveRoute`), it always routes through LiteLLM and wt prints the forced-LiteLLM stderr notice — codex is the current example
- If implementing `Resumer`, add session path logic to `internal/session`
- Test both routing modes (direct/litellm) if the agent will route through LiteLLM
