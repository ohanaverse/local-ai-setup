# wt: usage and survey stats for any agent-model pair

Date: 2026-09-19
Status: approved, pending implementation plan

## Context

Sub-project 1 of 5 in the "make wt's model selector look like modelman's"
effort (long-term goal: migrate away from modelman, so discovery and local
model lifecycle are ported to Go rather than shelled out to modelman).

| # | Sub-project | Depends on |
|---|---|---|
| 1 | Stats and surveys for any agent-model pair (this spec) | none |
| 2 | Go discovery and running-state of local models | 1 |
| 3 | New selector table (headings, columns, union, sort) | 1, 2 |
| 4 | Go start + single-model-provider replacement warning; run discovered models without writing config | 2 |
| 5 | Launch-time smoke gate (exit non-zero on failure) | 4 |

## Problem

`internal/usage` and `internal/survey` key on a free-form `model_id` string
and never validate against the registry, but launch paths only build a
`config.Model` from registry entries. A discovered (on-disk, unregistered)
local model therefore has no id and no `Model` to record against. Also, usage
events carry no `agent`, so launch counts cannot be scoped to an agent-model
pair (survey events already can). Retention is already 30 days in both stores
with prune-on-write; it does not change.

Local ids in the registry are inconsistent with on-disk names (omlx disk
`Qwen3.8-27B-4bit` vs registry `omlx/mlx-community--Qwen3.8-27B-4bit`; mtplx
registry mixes `Youssofal/...` and `Youssofal--...`), so a registry-shaped id
cannot be derived from a discovered artifact.

## Design

**Identity.** Every launch resolves to a stat key `(agent, model_id)`:

- Model matches a registry entry -> `model_id` is the registry id. Matching
  uses modelman's `_name_matches` rule (equal, or one is a `/`-suffix of the
  other; suffix strict so 4-bit and 6-bit variants never collide), ported to
  Go in sub-project 2.
- Otherwise -> `model_id` is `<provider>/<on-disk artifact name>`, e.g.
  `omlx/Qwen3.8-27B-4bit`.
- A discovered model registered later starts a fresh key. Old history is not
  merged; it ages out under the 30-day retention. No alias table (YAGNI).

**Usage store (`internal/usage`).**

- `event` gains `Agent string \`json:"agent,omitempty"\``.
- `Record(modelID)` becomes `Record(agent, modelID)`.
- Add `CountsForAgent(agent string, modelIDs []string)` alongside the existing
  model-level `Counts`. Legacy lines without `agent` count toward model-level
  totals only, never toward a pair.
- Retention 30d, prune on write: unchanged.

**Survey store (`internal/survey`).** No schema change (already has `agent` and
free-form `model_id`). `PromptRun` and the launch paths must accept a
`config.Model` synthesized from a discovered artifact so recording does not
require a registry entry.

**Out of scope.** Discovery itself (#2), selector rendering (#3), start/warn
(#4), smoke gate (#5).

## Testing

Table-driven tests (each with the required what/why comment):

- Unregistered model: record then read back usage and survey stats.
- Pair scoping: `CountsForAgent` excludes other agents and legacy lines;
  `Counts` still includes legacy lines.
- Old-format usage JSONL lines parse without error.
- Pruning at 30 days for events with an agent field.
- Launch path (TUI and non-TUI) records against a synthesized model.

Update `wt/CLAUDE.md` usage/survey descriptions when implemented.
