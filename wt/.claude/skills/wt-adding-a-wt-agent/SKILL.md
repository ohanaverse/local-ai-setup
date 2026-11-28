---
name: wt-adding-a-wt-agent
description: Steps to add a new AI agent driver to the wt launcher (internal/agents driver, bin shim, docs entry). Use when asked to add support for a new agent/CLI to wt.
---

## Adding a new agent

1. Add a driver in `internal/agents/<name>.go` implementing `Build`/`YoloFlag`, registered via `register("<name>", ...)`.
2. Add a shim `bin/<name>-wt`: `exec wt --agent <name> "$@"`.
3. Add a doc to `docs/wt-agents/`.

`make install` copies `bin/*` by glob and `make uninstall` removes
`$(BINDIR)/*-wt`, so the new shim is picked up (and removed) automatically.
The smoke-test loop in `make test` iterates a hard-coded launcher list: add
`<name>-wt` to it in `wt/Makefile`. The config-validation tests in
`internal/config` enumerate agents by name — check `config_test.go` if the
agent needs a `[[agents]]` entry.

## Driver implementation details

1. Create `internal/agents/<name>.go` implementing the `Driver` interface:
   - `Build(m config.Model, yolo bool, r Route) LaunchCmd` — dial from the `Route` (`agents.GatewayRoute`: the model server's gateway origin, its token, the model's gateway id); never hardcode a provider endpoint
   - `YoloFlag() string`
2. Implement other optional capabilities as needed: `Seeder`, `GatewaySyncer` (an agent that keeps its own model catalog file, like pi's `models.json`), `ArgSetter`, `StateDirer` (if the agent keeps per-working-directory state, so `wt smoke` can remove it for its temporary directories)
3. Register from the driver file's own `init()` with `register("<name>", func() Driver { return <name>Driver{} })` (`register` is defined in `internal/agents/agents.go`)
4. Add a model-id regression test (`Test<Name>OllamaPrefix`) using a model with distinct `ID`/`ModelName` to catch wrong id passthrough
5. Add the agent to the capability table in `wt/docs/internals/agents.md`
6. Decide whether the agent has a native provider (a row named for it, run on the agent's own login) and update `config.NativeCapableAgents` (and `config.NativeOnlyAgents` for an agent that can run nothing else) in `internal/config/config.go`; `TestNativeTablesMatchTheDrivers` (`internal/agents/native_tables_test.go`) fails for a new driver until they match

**Key gotchas:**
- The model ref comes from `Route.ModelRef` — `GatewayRoute` already picked the gateway id (`m.GatewayID`, else `m.ID`); don't re-derive it. There are no direct routes: every non-native launch goes through the model server's gateway, which speaks every wire protocol LiteLLM does
- Don't add session handling: wt never resumes a session (there is no `Resumer` capability and no `internal/session` package). The user continues a conversation with the agent's own flags after `--`, which `BuildLaunchCmdGateway` appends unchanged
- Test the gateway route (`litellmRoute`/`GatewayRoute` in the driver tests, and a `TestGatewayMatrix<Name>` cell in `gateway_matrix_test.go`)
- **`Build` must start with `if m.Native { return <bare LaunchCmd> }`.** Every existing driver (claude, codex, copilot, pi, opencode) does this so a native model (`config.Model{Native: true, ...}`) launches the bare binary with no `--model`/gateway env, and the gateway token never reaches it. Forgetting it emits provider env vars pointing at an empty base URL instead of a plain command — write a `Test<Name>NativeBypass` test (see `opencode_test.go`'s `TestOpenCodeNativeBypass`) to pin it.
