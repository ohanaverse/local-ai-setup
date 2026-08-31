# Agents Smoke Script — Design (`make test-agents`)

**Status:** Proposed (2026-08-31). Approved design for implementation; not yet implemented.

**Scope:** a standalone, live-services test script (`scripts/agents-smoke.sh`) plus
Makefile wiring. It verifies that **every supported wt agent** can complete a
real one-shot round trip with a configurable set of models.

Companion to `docs/wt-agents/litellm-troubleshooting.md`, which documents the
by-hand launch matrix run of 2026-08-31. This script mechanizes that procedure
so it can be re-run whenever agents, drivers, or gateway config change.

## Problem

wt has 7 agents (claude, codex, copilot, opencode, pi, agy, shell), each with
its own driver contract (native dispatch, gateway env wiring, custom provider
config, models.json sync…). The only verification today is either (a) Go unit
tests with stubbed seams — which never touch a real launch — or (b) a manual
one-shot matrix run by hand on each agent, performed once during debugging.
Nothing catches "a driver or gateway regression broke agent X with model Y"
without the operator remembering to hand-test every agent.

The 2026-08-31 litellm work showed how easy it is to break a single agent's
routing invisibly (pi's split-on-first-slash resolution, codex's `env_key`,
opencode's catalog, the proxy-side `drop_params`/`additional_drop_params`
settings). A repeatable per-agent/per-model smoke is the missing check.

## Goal

`make test-agents` (from a repo checkout, inside the repo):

1. Runs **at least one test for every supported agent**, each as a real
   one-shot launch through the installed `wt` binary.
2. Agents with a native model test with the `<agent>/native` variant (e.g.
   `claude/native`) — proving the agent's own subscription path.
3. Agents that support the `ollama` provider test with an ollama cloud model —
   proving the gateway round-trip in whatever mode the live config is in.
4. Reports PASS / FAIL / SKIP per row and exits non-zero iff any FAIL.

## Non-Goals (Out of Scope)

- **CI wiring.** The script needs live gateways, agent binaries, and
  authorization; it is a human-run / on-demand tool. (The existing `make
  test` target stays the CI-safe smoke; this is deliberately separate.)
- **Config mutation.** The script never changes `~/.config/agent-wt/config.toml`,
  `~/.pi/agent/models.json`, the litellm proxy, or repo state. It always
  reflects the live configuration — if `[gateway].mode` is `litellm`, ollama
  rows test litellm; if `direct`, they test the local ollama gateway.
- **Auto-discovery.** New agents/providers (openrouter, omlx, llama.cpp, …)
  appear by **adding table rows**, not by inference from the registry.
- **Latency/perf assertions.** Pass/fail only (chosen over the latency-canary
  option during design).

## Matrix (default table)

| Agent | Tests | Rationale |
|---|---|---|
| claude | `claude/native` + `ollama/glm-5.3-flash:cloud` | native subscription **and** gateway (the archetypal two-path agent) |
| codex | `ollama/…:cloud` | no `codex/native` registry entry |
| copilot | `copilot/native` + `ollama/…:cloud` | as claude |
| opencode | `ollama/…:cloud` | not eligible for `*/native` via registry row |
| pi | `ollama/…:cloud` | same |
| agy | `agy/native` one-shot | agy takes its model inside its own TUI/config; driver never passes `--model`, but the launch path still resolves a model, so the row pins `agy/native` to avoid the multi-model picker error |
| shell | `echo <sentinel>` (argv passthrough) | proves `--` → argv (`ArgSetter`) contract |

One-shot flags (verified 2026-08-31 against installed CLIs):
`claude -p` · `codex exec` · `copilot -p` · `opencode run` · `pi -p` ·
`agy -p`. All passthrough args ride wt's existing `--` mechanism — the script
adds no new wt surface.

## Script Interface

**Location:** `scripts/agents-smoke.sh` in this repo.

**Usage:**

```
scripts/agents-smoke.sh [--list|--dry-run] [--only <agent>] [--timeout <sec>] [-h]
make test-agents ARGS="--only claude"     # same entry point
```

- `--list` — print the matrix and exit (no launches; CI-safe).
- `--dry-run` — additionally print the exact `wt` command per row; no launches.
- `--only <agent>` — run rows for the given agents (comma-delimited, e.g.
  `--only claude,codex`), for debugging.
- `--timeout <sec>` — per-row wall-clock limit (default 180).

**Config table** (embedded at the top of the script; the extension point):

```bash
# agent|model|oneshot-args|what-this-proves
claude|claude/native|-p @PROMPT@|own subscription, no model args
claude|ollama/glm-5.3-flash:cloud|-p @PROMPT@|gateway round-trip
codex|ollama/glm-5.3-flash:cloud|exec @PROMPT@|
copilot|copilot/native|-p @PROMPT@|own subscription
copilot|ollama/glm-5.3-flash:cloud|-p @PROMPT@|
opencode|ollama/glm-5.3-flash:cloud|run @PROMPT@|
pi|ollama/glm-5.3-flash:cloud|-p @PROMPT@|
agy|agy/native|-p @PROMPT@|native model (driver ignores it)
shell|-|echo @PROMPT@|-- passthrough becomes argv
```

- Fields are `|`-delimited so model ids (`ollama/glm-5.3-flash:cloud`) may
  contain `/` and `:` freely.
- `model` `-` = launch without `-M` (native default / no model layer).
- `@PROMPT@` is replaced with the row's full prompt line — for LLM agents the
  "Reply with exactly…" instruction; for `shell` the same string is exec'd as
  argv (`echo` prints it verbatim, sentinel included, which also pins the
  quoting of `--` passthrough). Later providers
  (openrouter, omlx, llama.cpp) are new rows only; no script logic change.

## Pass Rule & Sentinel

**Prompt** (per row): `Reply with exactly this text and nothing else: <sentinel>`.
Sentinel: `WT-SMOKE-<agent>-<runid>` with `runid = $(date +%s)`-style per-run
value — unique per run so a stale session transcript or cached output can never
false-pass a later run.

**Row PASS ⇔ agent exit code 0 AND sentinel appears in the agent's stdout.**
Chosen over exit-code-only because the matrix proved some agents can end
politely (or emit banners) around a failed round-trip; the sentinel is the
only artifact that proves a model actually answered.

## Execution Model

- Each row runs: `wt --cwd -A <agent> [-M <model>] -- <oneshot-args>` from the
  **current directory** — no worktree creation (`--cwd`), no `-W` churn, no
  `--yolo`, no resume handling beyond wt's defaults.
- Per-row timeout via `gtimeout` when available, else a `perl alarm` fallback
  (macOS ships neither GNU `timeout` nor guarantees coreutils). On timeout the
  row is FAIL, the child is killed, and the run continues.
- Rows run **sequentially** (parallel agents would collide on session files
  and gateway capacity; sequential also keeps output readable).
- Never aborts early: every row gets a verdict; final summary prints
  `[PASS|FAIL|SKIP] agent × model (duration)` lines and a compact table,
  then exits 0 iff zero FAILs.

## Result Classification

- **PASS** — exit 0 + sentinel in stdout.
- **FAIL** — anything else, including wt refusing the row (unknown model,
  not eligible, validation error). A refused row is a config/registry gap —
  a real finding, per the litellm lessons — and must show wt's stderr.
- **SKIP** — wt reports the agent binary is not installed. Matched by wt's
  exact installation-error message (`agent <bin> not installed`); SKIP never
  affects the exit code. Generic substrings ("command not found", "No such
  file") are deliberately *not* matched — they also appear in real agent
  failures and must surface as FAIL, not be masked as SKIP.
- **Prereq check, fail fast:** `wt` on `$PATH` (else error before any row);
  `wt --version` must succeed.

## Makefile Integration

- **`test-agents` target** → runs `scripts/agents-smoke.sh`, passing `ARGS`
  through. Listed in `make help`. **Not** part of `make test` (which stays
  credential-free).
- **`lint` / `format` / `format-check`** — extended to include
  `scripts/agents-smoke.sh` alongside `bin/*-wt` (same shellcheck/shfmt
  discipline, same flags).

## Testing the Script Itself

- `shellcheck`/`shfmt` clean via the extended `make check`.
- `--list` and `--dry-run` are deterministic self-checks (no live services).
- Live runs are manual: `make test-agents` on a machine with agent binaries
  and reachable gateways.

## Risks / Notes

- One-shot flags are CLI-contract facts of the external agents; they are
  recorded in the table and re-checked on agent upgrades (any drift fails
  the row loudly rather than silently).
- Native rows exercise wt's native dispatch (no env/model args) — e.g.
  `claude/native` launches bare. This is exactly the path most likely to
  regress silently in driver refactors.
- The script reads the live config only; users whose config is mid-change
  see the real (possibly failing) state — intended for a smoke tool.