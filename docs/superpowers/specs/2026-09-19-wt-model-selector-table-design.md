# wt: modelman-style model selector table

Date: 2026-09-19
Status: approved, pending implementation plan

## Context

Sub-project 3 of 5 in the "make wt's model selector look like modelman's"
effort. Depends on #110 (per-agent usage counts, `config.DiscoveredModelID`)
and #111 (`localmodels.Inventory`: registered + discovered local models with
live `Running`).

| # | Sub-project | Depends on |
|---|---|---|
| 1 | Stats and surveys for any agent-model pair (done, #110) | none |
| 2 | Go discovery and running-state of local models (done, #111) | 1 |
| 3 | New selector table (this spec) | 1, 2 |
| 4 | Go start + single-model-provider replacement warning; run discovered models without writing config | 2 |
| 5 | Launch-time smoke gate (exit non-zero on failure) | 4 |

## Problem

wt's model picker is a Bubbles `list` of one preformatted string per row: no
column headings, family-grouped usage sort, and only models in `registry.toml`
(local ones only when running). Modelman's table shows FAMILY / PROVIDER /
MODEL / LOC / STATUS / EXPOSED / RUNNING / COST. wt should present the same
information, include discovered local models, and sort so cheap and
already-running models come first.

## Design

### Approach

Keep the Bubbles `list` (fuzzy filter, themed delegate, rotation marker,
in-use ref column, `wt smoke`'s `PickModel` reuse), render a header line above
it, and put a pure "rows" layer underneath: a builder produces `Row` values, a
sort orders them, and rendering formats them. One shared column-width
calculation drives both header and rows so they cannot drift. Rejected:
`bubbles/table` (loses filter/delegate, rewrites ~1000 lines of picker tests);
header-string-only (leaves inclusion and sort tangled with rendering).

### Rows and inclusion

`Row` carries: family, provider, id (`provider/model`), location, status,
exposed, running, cost, per-agent 1d/7d/30d counts, per-agent survey stats, and
the `config.Model` to launch (synthesized for discovered entries: id
`config.DiscoveredModelID`, `ModelName` = artifact, location local, no
tags/family).

Rows come from three sources, all limited to providers the chosen agent
supports (`supported_providers` stays a hard constraint):

1. Exposed cloud models (existing exposure predicate).
2. Every configured local model, running or not.
3. Every discovered local model from `localmodels.Inventory`.

If a discovered entry's id equals an existing row id, the registry row wins (no
duplicate; covers an ollama cloud tag lacking `remote_host`). Discovered rows
have no tags/family, so they are hidden when `-T` or `-F` is set.

### Sort

Two groups, no divider row (the RUNNING column makes the split visible):

- **Group 1:** exposed cloud models plus every running local model, by cost
  ascending (output price per million, then input price per million), then
  7-day usage ascending, then id. Local models and subscription-only models
  (no per-token prices) count as $0; a row with no cost data at all sorts last
  within the group.
- **Group 2:** non-running configured and discovered local models,
  alphabetical by id.

### Columns and rendering

Headings: `FAMILY  MODEL  LOC  STATUS  EXPOSED  RUNNING  COST  1D  7D  30D
SURVEY`. MODEL shows `provider/model`. The rotation marker and in-use ref count
remain a left prefix. Mid-row cells are plain ASCII (wt's rule: ambiguous-width
glyphs misalign CJK terminals):

| Column | Values |
|---|---|
| LOC | `cloud`, `local` |
| STATUS | `ok` (on disk, or cloud), `absent` (configured local, not on disk), `new` (discovered, unregistered) |
| EXPOSED | `Y` when modelman.toml's `exposed` flag (legacy `litellm_exposed` ORed) is set, else `-`; discovered rows `-` |
| RUNNING | `run` or `-` (live, from `Inventory`) |
| COST | existing three-price format; `-` for discovered/no data |
| 1D 7D 30D | `usage.CountsForAgent` for the chosen agent-model pair; model-level `Counts` when no agent (`wt smoke`) |
| SURVEY | existing trailing `FormatPickerSegment` segment (already uses `✓`/`⚠`), per agent-model pair |

Discovered rows show family `-`. The old trailing `[tags]` cell is dropped
(tags are not in the column list; `-T` still filters). Requires a new read-only config accessor for
the raw exposed flag (local models are always "exposed" to `IsExposed`, so the
predicate cannot supply the column).

### Selecting a row, and data flow

- A running row with a resolvable route launches through the existing launch
  path. That includes running discovered models; stats record under
  `DiscoveredModelID` (#110).
- A non-running local row does not launch: status line, e.g. `not running —
  start it with modelman start <id>`, until sub-project 4 adds auto-start.
  Exception: a pulled ollama model stays launchable even when not loaded
  (ollama's daemon loads on demand, and sub-project 3 must not regress launching
  models started flag-only by `modelman start`). RUNNING still shows `-` for
  it; only launch gating differs.
- A discovered model is not in LiteLLM's `model_list`. With LiteLLM routing on
  or a forced route, a discovered row gets an exception note (`(not in
  LiteLLM)`) and cannot be selected; it works in direct mode.
- The `Inventory` probe runs synchronously inside `enterModelPhase`, as the
  existing `localgate.Apply` probes already do (probes run concurrently with a
  2s cap; a refused localhost connection is instant). A `tea.Cmd` + probing
  phase was rejected: it adds a phase and rewrites every `enterModelPhase`
  test for no regression fixed. Revisit if the picker ever feels slow.
- The non-TUI path and `-M` pin validation keep using `localgate`, unchanged.
- `wt smoke`'s `PickModel` uses the same row builder with no agent: model-level
  counts, blank per-agent columns.

### Carry-forward notes

- omlx/mtplx provider `Status` in the snapshot reflects the model-dir scan, not
  server liveness; the table's RUNNING comes from `Entry.Running`, never from
  `Status`.
- Stats key on `Row` id, which for a discovered model is
  `config.DiscoveredModelID` and for a registered one the registry id.

### Out of scope

Starting models and the replace-warning (#4), the smoke gate (#5), non-TUI
launch changes, removing `localgate`, a divider row between groups.

## Testing

Per the repo pattern: assert on unexported functions directly and stub the
inventory through a `var x = realX` seam. Every `Test*` carries a what/why
comment. Cases:

- Row builder: three sources included; agent `supported_providers` filter;
  exposed-vs-unexposed cloud; `-T`/`-F` hide discovered rows; duplicate-id
  collision keeps the registry row.
- Sort: group order; cost tie-breaks (output then input); local and
  subscription-only = $0; no-cost-data last; 7d ascending tie-break; group 2
  alphabetical.
- Cells: STATUS/EXPOSED/RUNNING per row kind; per-agent counts and survey vs
  model-level in `PickModel`.
- Alignment: header and every row share widths; multi-byte content does not
  shift columns.
- TUI: probing state then populated table; non-running row shows the start
  hint and does not launch; discovered row with LiteLLM on is unselectable;
  running discovered row launches with the synthesized model and records
  stats under its `DiscoveredModelID`.
- Regression: `wt smoke`'s picker still works; existing picker behavior (marker
  on last-launched, ref column, fuzzy filter) unchanged.

Update `wt/CLAUDE.md` (TUI/model-picker sections, package table) when
implemented.
