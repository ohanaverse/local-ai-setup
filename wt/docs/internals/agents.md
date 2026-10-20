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

**claude maps every model slot on a single-model launch.** Wherever `claudeDriver.Build` sets `ANTHROPIC_BASE_URL` (every non-native launch, direct or LiteLLM), it also sets `ANTHROPIC_DEFAULT_<SLOT>_MODEL` to `Route.ModelRef` (the `--model` value) and `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME` to `m.ModelName` for each of the four slots, and names the same eight keys in `ClearEnv`, so `command` drops an inherited value and the built command holds one entry per key. The names and the label rule come from `claudeSlotVars` (`claude_slots.go`), which the slot-profile build uses too: change it there, never in one build. Precedence rests on order: `profiles.ApplyEnvAndArgs` appends an applied automatic profile's `env` after the driver's, and the last entry for a key wins in `exec.Cmd.Env`, so a profile value overrides wt's on both launch paths (`TestAutomaticProfileSlotVarWinsOverSingleModelLaunch`, `TestRunAndWaitCmdProfileSlotVarWinsOverSingleModelLaunch`). The native launch sets and clears none of them.

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

**Slot support** is one more optional interface, `profiles.SlotCapable`, declared in `internal/profiles` beside `ProfileCapable` so that package never imports `internal/agents`: `SlotNames()` (the slots, in display order), `ReservedEnvKeys()` (the env keys wt owns for the launch), `CheckConfigContent(map[string]any) error` (the driver's own static rule for a profile's `config_content`) and `BuildSlots(profiles.SlotBuildInput) (profiles.SlotCommand, error)`. Only claude implements it (`claude_slots.go`: slots `fable`, `opus`, `sonnet`, `haiku`). `agents.SlotsFor(agent)` is the type assertion; `agents.BuildSlotLaunchCmd` gives the driver's `SlotCommand` the treatment `BuildLaunchCmdInfo` gives a single-model launch — binary lookup, `ClearEnv` plus `ClearEnvPrefixes` filtering of the inherited environment, the worktree directory and `PWD`, passthrough args appended unchanged — after `CheckSlotPassthrough` refused the flags the driver names (`RefusedSlotPassthrough`, the optional `SlotPassthroughRefuser`; `--settings` and `--model` for claude, in both the two-argument and the `=` form). It reads the `[litellm]` url and key directly and never `cfg.IsLitellm()` or `ResolveRoute`. `agents.BuildSlotCommand` is the driver call alone, for `wt profile show`. Adding slot profiles to another agent is implementing this interface; the catalog, the picker and the launch flow do not change.

**A driver's `config_content` rule is checked twice by one piece of code.** Static validation reaches `CheckConfigContent` through `profiles.SlotDrivers.ConfigContent` (`agents.SlotDrivers()`), so a profile the slot build would refuse is a blocked row with the build's own message, listed by `wt profile list`, and never a launch row. Claude's rule is `claudeConfigEnv`, `claudeModelOverrides` and `claudeEncodable` (`claude_slots.go`): `config_content.env` and `config_content.modelOverrides`, when present, are tables (#412), and the content can be encoded as the `--settings` JSON (a TOML nan or inf would fail the build's `json.Marshal` at the launch, after the routes were written). `CheckConfigContent` and `claudeSlotSettings` both call them; the build's calls are the second line of defence, for a caller that did not validate. A `SlotDrivers` whose `ConfigContent` is nil fails closed: `ValidateSlots` refuses every profile that sets `config_content` (`config_content cannot be checked for agent "<agent>"`). The callback is asked only about a profile that sets `config_content`, after the mechanism and reserved-`env` rules and before the reserved-key rule for `config_content.env`.

**What a driver warns about never blocks.** The optional `agents.SlotConfigWarner` (`ConfigContentWarnings(cc, slotModels) []string`) is asked through `agents.SlotConfigWarnings(check)` for a Usable profile that passed validation, with the distinct ids its slots name; it is not on `profiles.SlotDrivers`, so a warning cannot reach a verdict. Claude's says one line per `modelOverrides` target that is not a string or not one of those ids, in key order (#412). `cmd/wt`'s `slotContentNotes` prefixes each with the profile, for `slotWarnings` and `wt profile list`.

**`config_defaults` is an automatic-profile mechanism only.** claude's `ProfileMechanisms` declares `env`, `config_file` and `config_defaults` (`profiles.MechanismConfigDefaults`); `config_defaults` is written into the same `.claude/settings.local.json` as `config_content`, but only where that file has no value (`profiles.ApplyConfigContent`; see [launch-flow.md](launch-flow.md#profiles-internalprofiles)). No other driver declares it, so `Validate` refuses it for every other agent. A slot profile has no such field (the key is unknown in `slot-profiles.toml`) and `CheckConfigContent` is never asked about it.

**Pre-launch `ollamacheck.Check`** (both paths: `cmd/wt/launch.go`, `internal/tui/app.go`) runs only when the model's *resolved route* is direct and the model is ollama (`!route.Litellm && ollamacheck.IsOllamaModel(m)`) — not the raw toggle, so a protocol-forced route (codex+ollama) isn't spuriously blocked. It shells out to `ollama list`, pinned with `OLLAMA_HOST` to the daemon the registry's ollama provider row names (`localmodels.FamilyOrigin(cfg, "ollama")`), not the one the shell's `OLLAMA_HOST` names, and stopped after `ollamacheck`'s 30-second limit (the one `wt cloud-sync` gives the same command) so a daemon that never answers cannot freeze the picker. It answers "not available" when no `ollama` command is installed, and an error — the TUI shows it as the picker's status, the non-TUI launch stops with it — when `ollama list` fails, the registry has no ollama provider row, or the row's `base_url` gives no `http(s)://host` (no command is run then: ollama reads an unusable `OLLAMA_HOST` as its default daemon). The lookup and the list are one seam, `ollamacheck.StubListForTest`. The TUI start flow skips it for a model it just loaded.

New driver: see the `adding-a-wt-agent` skill.
