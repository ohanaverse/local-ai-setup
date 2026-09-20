# wt: live-truth resolution for the non-TUI path, `-M` pin and `wt smoke` (sub-project 4c)

Date: 2026-09-20
Status: approved, pending implementation plan

## Context

Sub-project 4c of the "make wt's model selector look like modelman's" effort.
Merged so far: #110 per-agent stats, #111 `localmodels.Inventory`, #112 selector
table, #113 `internal/lifecycle` (4a), #114 TUI start-on-select flow (4b).

| # | Sub-project | Depends on |
|---|---|---|
| 1-3 | Stats, inventory, selector table (done) | |
| 4a | Lifecycle engine (done, #113) | 2 |
| 4b | TUI start-on-select flow (done, #114) | 4a |
| 4c | Non-TUI launch, `-M` pin and `wt smoke` on live truth; start policy without a TUI; retire `localgate` (this spec) | 4a, 4b |
| 5 | Launch-time smoke gate (exit non-zero on failure) | 4c |

State scope (decided earlier): live truth everywhere; wt writes nothing to
modelman-owned state and never restarts the LiteLLM proxy.

## Problem

Three paths still decide "is this local model running?" from modelman's
per-model `running` flag plus a probe (`internal/localgate`): non-TUI
`resolveModel`, the TUI's `-M` pin check (`enterModelPhase`), and `wt smoke`'s
`Eligibility`. The TUI table already uses the live `localmodels.Inventory`, so
the two truths disagree: a model started by wt's TUI is invisible to
`wt -A claude -M omlx/x`, and a discovered model cannot be pinned at all.

## Design

### Approach

Extract a shared `internal/catalog` package that owns "what rows exist, is each
running or present, and what does Enter do": row inclusion (registry models,
discovered synthesis, agent/`-T`/`-F` filters), the running/status rules, and
`action()` (launch / start / block with reasons). The TUI wraps it with
counts, survey stats, sort and rendering; the non-TUI path and `wt smoke`
consume the same rows, so there is one source of truth. Rejected: a separate,
smaller CLI resolver (duplicates the row rules; the two would drift, which is
how the flag-versus-live split arose); having the CLI call into `internal/tui`
(inverts the dependency).

### Non-TUI resolution

`resolveModel` works from live rows:

- **Rotation (no `-M`)** picks only among `launch` rows (cloud and
  already-running local): an unattended launch never auto-starts anything.
- **A `-M` pin** is looked up among ALL rows, including discovered ones:
  `wt -A claude -M omlx/Qwen3.8-27B-8bit` works with no registry entry (the row
  is synthesized exactly as in the TUI). Then: launch -> proceed; start -> the
  start driver; block -> an error carrying the row's block reason.
- The old "all of agent's eligible models are local and none running" error is
  replaced by "no cloud or running local model for agent X; `wt -M <id>` starts
  one".

### Starting without a TUI (`startForLaunch`)

Calls `lifecycle.Start` with these rules:

- Progress goes to stderr as timestamped lines (`wt: starting omlx/x — warming
  the model (12s)`).
- Ctrl+C cancels the context; the engine tears down anything it spawned; a
  second Ctrl+C exits.
- A plain start with nothing to replace never asks.
- On `*OccupiedError` / `*OccupancyUnknownError`: with a TTY, name the model that
  will be stopped and ask `y/N` (default N); without a TTY, refuse with an error
  naming the occupant and the flag; the new **`--replace`** flag opts in
  non-interactively and still prints a one-line "replacing X" notice.
- Error wording (daemon down, missing binary, port busy, generic) is shared with
  the TUI: the mapping moves from `internal/tui` into `internal/lifecycle` as one
  exported function, the only engine change in this sub-project.

### TUI `-M` pin, smoke, and retiring `localgate`

- **TUI pin:** the verdict comes from the same rows. A running pin launches. A
  pin needing a start selects its row and enters the existing 4b start flow (so
  the replace dialog applies; `--replace` skips it). A blocked pin routes back
  to the agent picker with the block reason; a pin absent from the list keeps
  today's "not in the eligible list" message.
- **`wt smoke`:** eligibility is "cloud plus running local, from live rows". It
  still never starts or stops anything.
- **Retire the flag machinery:** delete `internal/localgate`; delete
  `Config.RunningLocalModelIDs`, `LocalGateActive`, `FilterToRunningLocal`,
  `SetLocalRunningForTest` and modelman's per-model `running` flag parsing;
  delete or migrate the ~30 tests that use them. Afterwards wt reads `exposed`
  from `modelman.toml` (EXPOSED column) but never `running`.

### Out of scope

The post-selection smoke gate (sub-project 5); stopping models; mlx_lm_server;
engine behavior changes (only the error-message mapping moves); a user-facing
stop command. The work may land as two PRs cut at a task boundary: (1) catalog
extraction + non-TUI resolution + start driver + TUI pin, then (2) smoke +
retiring `localgate`.

## Testing

Per the repo pattern (assert on unexported functions, `var x = realX` seams,
every `Test*` with a what/why comment):

- Catalog: the rows and `action()` tests move with the code; the TUI's tests keep
  passing against the wrapper.
- Resolution: rotation excludes non-running local models; a pin starts a
  non-running model; a discovered id can be pinned; a block reason surfaces; the
  "no cloud or running local model" error; unchanged behavior for cloud-only
  configs and command agents.
- Start driver: prompt yes / no, default N, non-TTY refusal naming the occupant
  and the flag, `--replace` proceeds with the notice, plain start never prompts,
  Ctrl+C cancels (context cancelled, engine torn down), typed errors map to the
  shared messages.
- TUI pin: running pin launches, start-needing pin enters the start flow, blocked
  pin routes back with the reason.
- Smoke: eligibility on live rows excludes non-running local models; it never
  calls `lifecycle.Start`.
- Retirement: nothing references the deleted APIs (`go build ./...`, and a grep
  in the plan's final task); `modelman.toml` fixtures with `running` keys still
  load (the key is ignored).

Update `wt/CLAUDE.md` and the guides that describe the running gate
(`docs/guides/00-config-map.md` and any other mention of `localgate` /
`modelman start` as wt's start mechanism) when implemented.
