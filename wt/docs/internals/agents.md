# wt agent drivers and routes

Internals reference for `wt/`, reached from [`wt/CLAUDE.md`](../../CLAUDE.md). Backticked paths are relative to `wt/` unless they start with `../`.

## Agents (Go)

Each agent registers a `Driver` (`Build(m config.Model, yolo bool, r config.Route) LaunchCmd`, `YoloFlag() string`). `BuildLaunchCmd(agent, m, worktreePath, yolo, cfg, extraArgs)` is the shared constructor for both launch paths — it resolves one `config.Route` via `cfg.ResolveRoute(m, agents.ProtocolsFor(agent))` and hands it to `Build`; drivers must not bypass it.

`Route` carries `BaseOrigin` (scheme://host:port, no wire-path suffix), `APIKey`, `ModelRef`, `Display`, `ProviderID`, `Protocol`, `Litellm`, `Forced`. Direct routes dial the provider's own `auth.base_url`, read through `Provider.Origin` as wt's probes read it (an mtplx url on this machine with no port is port 8003, #348; key from `auth.secret_ref` via `ResolveSecret`); litellm/forced routes dial the `[litellm]` url/key.

**Agent protocols.** Agents declare wire protocols via `ProtocolDeclarer`; providers declare what they serve (`Provider.protocols`, default `openai-chat`). An empty intersection sets `Forced = true` — LiteLLM regardless of the toggle.

| Agent | Declared protocols |
|---|---|
| claude | `anthropic` |
| codex | `openai-responses` |
| copilot, opencode, pi | `openai-chat` |
| agy, shell | none — agy is a native passthrough, shell a command runner |

Consequence: **codex always routes through LiteLLM** (no local provider serves `openai-responses`), and `BuildLaunchCmd` prints the forced-LiteLLM notice on every launch of a protocol-forced pair, whether the LiteLLM toggle is on or off; claude × openrouter is forced the same way. litellm ≤ 1.98.0 cannot serve codex without a workaround — see [docs/wt-agents/codex-wt.md](../wt-agents/codex-wt.md).

**Model id contract** (resolved centrally in `ResolveRoute`): litellm/forced routes set `Route.ModelRef` to the **registry id** (`m.ID`, e.g. `ollama/qwen3.8:27b-mlx` — LiteLLM's `model_list` key); direct routes use the **provider-side name** (`m.ModelName`). `Route.Display` is always `m.ModelName`. Regression tests (`TestClaudeOllamaPrefix`, `TestOpenCodeOllamaPrefix`, …) use distinct `ID`/`ModelName` so a wrong id can't slip through.

**copilot must use the chat-completions wire (`WIRE_API=completions`)** — its `responses` wire drops leading characters through the OpenAI-compatible bridge (verified direct and via LiteLLM). This deliberately diverges from `ollama launch copilot`, which prescribes `responses`.

Per-agent env/args/config shapes (direct and LiteLLM): [docs/wt-agents/](../wt-agents/) `{claude,codex,copilot,opencode,pi,agy,shell}-wt.md`. Notable: **agy is not a command agent** (`IsCommand("agy")` is false), so `-A agy` without `-M` errors "multiple models match" when >1 agy model is eligible.

**Optional capabilities** (type assertions: `Syncer` and `ArgSetter` in `BuildLaunchCmd` (via `BuildLaunchCmdInfo`), `ProtocolDeclarer` in `ProtocolsFor`, `Seeder` in `initseed.Seed`, `OneShotRunner` and `StateDirer` in `internal/smoke`):

| Capability | Purpose | Implemented by |
|---|---|---|
| `ProtocolDeclarer` | wire protocols → `ResolveRoute` | claude, codex, copilot, opencode, pi |
| `Seeder` | `InstructionPointers()` — the pointer files `wt --init` creates beside AGENTS.md (never on a launch) | claude, copilot |
| `Syncer` | pre-launch sync, given the launch target and its resolved route (pi → `~/.pi/agent/models.json`: the registry models plus the launch target, so a discovered model gets its entry too, written where the route makes `Build` look — `litellm` for a protocol-forced route even with the toggle off) | pi |
| `ArgSetter` | passthrough args become argv | shell |
| `OneShotRunner` | `OneShotArgs(prompt)` for `wt smoke` | claude, codex, copilot, opencode, pi, agy |
| `StateDirer` | `StateDir(path)` — the agent's own per-working-directory state, which `wt smoke` removes for a temporary row directory | claude, pi |

**Pre-launch `ollamacheck.Check`** (both paths: `cmd/wt/launch.go`, `internal/tui/app.go`) runs only when the model's *resolved route* is direct and the model is ollama (`!route.Litellm && ollamacheck.IsOllamaModel(m)`) — not the raw toggle, so a protocol-forced route (codex+ollama) isn't spuriously blocked. It shells out to `ollama list`, pinned with `OLLAMA_HOST` to the daemon the registry's ollama provider row names (`localmodels.FamilyOrigin(cfg, "ollama")`), not the one the shell's `OLLAMA_HOST` names, and stopped after `ollamacheck`'s 30-second limit (the one `wt cloud-sync` gives the same command) so a daemon that never answers cannot freeze the picker. It answers "not available" when no `ollama` command is installed, and an error — the TUI shows it as the picker's status, the non-TUI launch stops with it — when `ollama list` fails, the registry has no ollama provider row, or the row's `base_url` gives no `http(s)://host` (no command is run then: ollama reads an unusable `OLLAMA_HOST` as its default daemon). The lookup and the list are one seam, `ollamacheck.StubListForTest`. The TUI start flow skips it for a model it just loaded.

New driver: see the `adding-a-wt-agent` skill.
