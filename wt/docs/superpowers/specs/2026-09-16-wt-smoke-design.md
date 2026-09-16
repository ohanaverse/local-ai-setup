# `wt smoke` — model×agent smoke test

**Status:** Approved (design), pending implementation plan
**Date:** 2026-09-16

## Motivation

`wt` supports seven agent drivers (claude, codex, copilot, opencode, pi, agy,
shell), each with its own routing/env/flag quirks (see wt/CLAUDE.md's Agents
table). When a model is newly exposed, a local backend is started, or a
driver's launch logic changes, there is no fast way to answer "does this
model actually work through every agent that's supposed to support it right
now?" without launching each agent by hand.

`wt/scripts/agents-smoke.sh` already answers a related but different
question — "do our hand-curated regression rows (fixed agent×model×route
combinations) still pass?" — via a static bash MATRIX, both routing modes,
wired into `make test-agents`. It is a regression tool over a fixed list, not
a debugging tool over a live selection.

This spec adds `wt smoke`: given one model (picked interactively or passed
directly), find every agent currently eligible for it and run a one-shot
prompt through each, in-process, using wt's real launch machinery. It answers
"is this model healthy across wt right now" — the tool an agent (human or
LLM) reaches for when debugging a specific model/agent pairing, distinct from
agents-smoke.sh's regression role. The two are not merged in this change;
see Non-Goals.

## Goals

- Given a model, determine every non-command agent currently eligible to
  launch it (same eligibility rules `wt`'s own launch path already applies —
  no new capability matrix).
- Run a one-shot, non-interactive prompt through each eligible agent, reusing
  wt's real in-process launch construction (`agents.BuildLaunchCmd`) rather
  than shelling out to the `wt` binary — this is a test of wt's own launch
  code, not a black-box CLI test.
- Classify each pairing PASS / FAIL / SKIP, with performance data (duration)
  and enough debugging detail (command line, exit code, captured output) to
  diagnose a failure without re-running anything by hand.
- Work both interactively (list eligible models, prompt for a pick) and
  non-interactively (`--model` flag, no TTY needed) — the tool is meant to be
  invoked by an agent debugging an issue, not only by a human at a terminal.
- Stay strictly read-only against modelman-owned state (`registry.toml`,
  `modelman.toml`) — no writes, no routing-mode flips, no provider
  start/stop.

## Non-Goals

- Starting, stopping, or isolating local model providers. That stays
  modelman's job (`modelman start`/`stop`, or the TUI's `s` keybinding); if
  the model under test isn't actually running, its agents will simply fail
  or the model won't appear in the interactive list.
- Flipping `[litellm].enabled` to cover both direct and LiteLLM-forced
  routing. `wt smoke` tests whatever routing mode is live right now.
  Covering both modes for a given model is a caller's responsibility (e.g. a
  future agents-smoke.sh refactor could flip the mode and invoke `wt smoke`
  twice — see below).
- Refactoring `agents-smoke.sh` to call `wt smoke` per row instead of
  shelling out to `wt -A <agent> -M <model>` directly. Raised during design
  as a plausible future direction (agents-smoke.sh would own routing-mode
  flips and local-model start/stop around calls to `wt smoke`, deferring
  per-agent one-shot-arg and pass/fail logic to it), but explicitly deferred
  to a separate follow-up task so this plan's scope and risk stay bounded to
  the new command.
- Semantic/quality scoring of agent responses. This is a smoke test
  (did it respond at all, correctly wired), not `modelman benchmark agent`'s
  judged-rubric benchmark. No LLM judge, no gates, no task workspace.
- A capability matrix UI or config file listing which agent supports which
  model — that already exists as `cfg.ModelsForAgent`/provider membership;
  `wt smoke` queries it, it does not duplicate it.

## CLI surface

```
wt smoke [model-id] [flags]
```

- `model-id` (positional, optional): a registry model id (`provider/name`),
  same format as `-M`. Omitted → list every currently eligible model
  (cloud-exposed, or local and live-verified-running — the same predicates
  wt's own launch/model-picker paths already use) and prompt for a choice.
  Requires a TTY when omitted; errors clearly (same pattern as other
  TUI-requiring paths) when stdin is not a TTY.
- `--prompt <text>` (optional): override the default prompt. See "Prompt and
  pass/fail classification" for how this changes verification.
- `--timeout <duration>` (default `180s`): per-agent timeout, same default as
  agents-smoke.sh.
- `--only <agents>` (optional): comma-separated agent names to restrict the
  run to (must be a subset of the eligible set; unknown names error).
- `--json` (optional): emit machine-readable output instead of the human
  table (see "Output").

Exit code: `0` if every non-skipped row PASSed, `1` if any row FAILed. SKIPped
rows (agent binary not installed) do not affect the exit code, matching
agents-smoke.sh's convention.

## Architecture

### New driver capability: `OneShotRunner`

`internal/agents/agents.go` gains one more optional capability, alongside
`ProtocolDeclarer`/`Syncer`/`ArgSetter`/`Resumer`/`Seeder`/`Commanded`:

```go
// OneShotRunner is an optional Driver capability for agents that can run a
// single prompt non-interactively and exit. wt smoke appends OneShotArgs'
// return value to the command Build already constructed.
type OneShotRunner interface {
    OneShotArgs(prompt string) []string
}
```

Implemented by claude, codex, copilot, opencode, pi, and agy — the same six
agents agents-smoke.sh's MATRIX already drives one-shot, using the same
flags that MATRIX currently hand-encodes in bash:

| Agent | `OneShotArgs(prompt)` |
|---|---|
| claude | `["-p", prompt]` |
| codex | `["exec", prompt]` |
| copilot | `["-p", prompt]` |
| opencode | `["run", prompt]` |
| pi | `["-p", prompt]` |
| agy | `["-p", prompt]` |

`shell` does not implement it (excluded via `IsCommand`, see below).

### Agent eligibility

For the chosen model `m`, iterate `cfg.Agents`; for each agent name where
`agents.IsCommand(name)` is false and `m` is present in
`cfg.ModelsForAgent(name)`, the agent is eligible. This reuses the exact
membership check `wt`'s launch path already applies — no new matrix, no
duplicated provider-support logic. `IsCommand` naturally excludes `shell`
(no model layer); `agy`'s own narrow `SupportedProviders` naturally narrows
it to effectively just `agy/native`, consistent with agents-smoke.sh's
comment that agy's driver ignores whatever model is passed.

### Row execution

For each eligible agent, in-process:

1. Resolve the launch command the same way a real launch would:
   `agents.BuildLaunchCmd(agent, m, cwd, yolo=false, sess=nil, cfg, extraArgs=nil)`.
   This runs the same route resolution, pre-launch sync (e.g. pi's
   `models.json` write), and env wiring a real `wt -A <agent> -M <m.ID>`
   launch would — `wt smoke` is testing wt's own launch machinery, not a
   parallel implementation of it.
2. If the driver implements `OneShotRunner`, append `OneShotArgs(prompt)` to
   the resolved `*exec.Cmd`'s `Args`. (Drivers reachable here always
   implement it, since `shell` — the only non-implementer — is filtered out
   during eligibility; this is a defensive check, not an expected branch.)
3. Run the command with a `--timeout`-bounded context, capturing combined
   stdout+stderr (not connected to the real terminal — this is a batch
   report, not an interactive launch).
4. Classify the result (below).

An agent binary missing on `PATH` surfaces as `agents.Command`'s existing
`"agent %s not installed"` error — classified SKIP, matching
agents-smoke.sh's classifier.

### Prompt and pass/fail classification

Default prompt (no `--prompt` given): a sentinel-echo request, mirroring
agents-smoke.sh —

> Reply with exactly this text and nothing else: `WT-SMOKE-<agent>-<runid>`

`<runid>` is generated once per `wt smoke` invocation (not per row), so all
rows in one run share a run id for easy correlation. PASS requires: process
exit code 0 AND the exact sentinel string present in captured output.
Anything else is FAIL (except the installed-check SKIP case above).

When `--prompt` is given, the sentinel is not part of the prompt (a custom
prompt was not asked to produce it), so verification degrades to "process
exited 0 within the timeout." This is a known limitation of overriding the
prompt: a custom-prompt run only confirms the agent ran and exited cleanly,
not that its response was meaningful. This is documented in `wt smoke`'s
`--help` text, not silently different behavior.

### Output

Human (default): one line per row —

```
[PASS ] claude    · ollama/qwen3.8:27b-mlx    (4.2s)
[FAIL ] codex     · ollama/qwen3.8:27b-mlx    (180.0s)
[SKIP ] copilot   · ollama/qwen3.8:27b-mlx    (not installed)
```

For every FAIL row, an indented detail block follows immediately (command
line, exit code, captured output — truncated to a bounded tail), so a
failure is fully diagnosable from one invocation's output without re-running
anything. PASS/SKIP rows stay single-line. A trailing summary line
(`PASS: 4 FAIL: 1 SKIP: 1 (model=..., runid=...)`) matches
agents-smoke.sh's summary convention.

`--json`: an array of objects, one per row —

```json
{
  "agent": "codex",
  "model": "ollama/qwen3.8:27b-mlx",
  "status": "FAIL",
  "exit_code": 1,
  "duration_ms": 180000,
  "command": "codex exec ...",
  "output": "...",
  "error": "context deadline exceeded"
}
```

plus a top-level `run_id` and `model` alongside the `rows` array. This is the
contract a future caller (agents-smoke.sh, or an LLM agent parsing results
programmatically) would consume.

## Error handling

- No TTY and no `model-id` given: clear error, same pattern as other
  TUI-requiring wt paths ("model picker needs a TTY; pass a model id
  directly").
- `model-id` given but not currently eligible for any agent: clear error
  listing why (not exposed / not running / unknown id), not a silent empty
  report.
- `--only` names an agent that isn't in the eligible set for this model:
  error before running anything (fail fast, mirrors agents-smoke.sh's
  `validate_only_agents`).
- Per-row timeout: classified FAIL with `error: "timed out after Ns"`, not a
  crash of the whole run — one slow/hung agent must not block the rest of
  the report.

## Package layout

- `cmd/wt/smoke.go` — `smokeCmd(a *app) *cobra.Command`, flag parsing, the
  interactive model-list prompt (non-TUI, plain numbered list — this is a
  read-only report command like `wt stats`, not a launch-flow picker), and
  output rendering (human table / JSON). Registered in `main.go`'s
  `rootCmd()` alongside `rotateCmd`/`configCmd`/`statsCmd`.
- `internal/smoke/` — the testable core, free of TTY/output concerns:
  - `EligibleAgents(cfg *config.Config, m config.Model) []string`
  - `RowResult` struct (`Agent, Status, ExitCode, Duration, Command, Output, Err`)
  - `RunRow(cfg *config.Config, agent string, m config.Model, prompt, sentinel string, timeout time.Duration) RowResult`
  - Process execution goes through a package-level var seam (`var runCmd = realRunCmd`), following the existing pattern documented in wt/CLAUDE.md's Go Tests section (`tuiRun`, `installed`, `stdinTTY`, etc.) — tests stub `runCmd` to verify classification logic (sentinel matching, timeout handling, installed-check SKIP) without executing real agent binaries.
- `internal/agents/{claude,codex,copilot,opencode,pi,agy}.go` — each gains a
  small `OneShotArgs` method.

## Testing

- `internal/agents`: one test per driver's `OneShotArgs` (table-driven,
  asserting the exact arg slice).
- `internal/smoke`: `EligibleAgents` against a fixture config (asserts
  `shell` excluded, `agy` narrowed to its native model, provider-mismatched
  agents excluded); `RunRow`'s classification logic (PASS/FAIL/SKIP/timeout)
  driven entirely through the `runCmd` seam — no real subprocess exec in
  `go test ./...`.
- `cmd/wt`: flag parsing and output rendering (human + JSON) against
  synthetic `RowResult` slices — same "assert on unexported functions
  directly" style `wt stats`' `buildStatsRows` tests use.
- No live-binary test is added to `make test`. A live, real-agent run of
  `wt smoke` remains a manual/CI-excluded verification step, matching
  `make test-agents`'s existing precedent (needs installed binaries and live
  credentials).

## Docs touchpoints (implementation plan should include)

- `wt/CLAUDE.md`: add `OneShotRunner` to the Optional Capabilities table, add
  `wt smoke` to the Smoke test command list, add `internal/smoke` to the
  package table.
- `wt/docs/wt-agents/` (or a new short doc): `wt smoke` usage/flags.
