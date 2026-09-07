# wt Session Survey — Design

**Date:** 2026-09-07
**Status:** Approved (pending implementation)
**Scope:** `wt/` (Go). No modelman changes.

## Problem

When a model underperforms in an agent session, it is often unclear whether
the model itself is bad or the *agent × model combination* is at fault.
Launch counts (`usage.jsonl`) record what was launched, never whether it
worked. This design adds a lightweight post-session survey so users can
(a) capture a verdict per session, (b) see accumulated stats before
launching, and (c) dump everything from a single `wt stats` command.

## Requirements

1. After an agent session ends, prompt up to three questions on the parent
   terminal:
   - **Q1 — did it work?** Three answers: `y`es (continue), `n`o (stop — no
     further questions), `s`kip / Enter (record a skip, stop).
   - **Q2 — speed**, `1(slow)-5(fast)`, Enter = leave unrated.
   - **Q3 — quality**, `1(bad)-5(great)`, Enter = leave unrated.
2. Record every answer set (including skips) as append-only JSONL events,
   keyed by agent and model.
3. Stats windows: 1d / 7d / 30d, per **model** (all agents) and per
   **agent × model** combo.
4. After the survey completes, print accumulated stats for the current
   model and the current agent × model combo.
5. The model picker shows agent-scoped 30-day survey stats so bad combos
   are visible *before* launching.
6. `wt stats` reports all collected stats (wt collects, wt reports).

**Worked-percentage semantics:** `worked% = worked / (worked + failed)`
over *answered* surveys only. Skips are recorded (participation is
visible) but excluded from the denominator.

## Data Model & Store — `internal/survey`

New package mirroring `internal/usage`:

- **File:** `~/.config/agent-wt/survey.jsonl` (`config.Dir()`;
  `NewStoreAt(dir)` for tests). One JSON object per line:
  `{"agent", "model_id", "timestamp"}` plus exactly one of:
  - `"skipped": true` — skip pressed; no verdict.
  - `"worked": false` — did not work; no ratings.
  - `"worked": true, "speed"?: 1-5, "quality"?: 1-5` — worked; each
    rating independently skippable (`omitempty`).
- **Store interface:** `Record(Event) error` + `Events() []Event`.
  `StoreImpl` uses the `usage.go` shape: flock sidecar `survey.jsonl.lock`,
  prune-older-than-30d + append under the lock, atomic write
  (`config.WriteFileAtomic`), `now` var seam. Malformed lines are skipped
  on read (best-effort, like `Counts`); scan errors surface only in
  `Record`'s prune path (whose output overwrites the file).
- **Retention:** 30 days — the longest stats window — pruned on every write.
- **Stats** per key per window:
  `{Answered, Worked, Failed, Skipped, RatedSpeed, SpeedSum, RatedQuality, QualitySum}`
  with `WorkedPct() (float64, bool)`, `SpeedAvg() (float64, bool)`,
  `QualityAvg() (float64, bool)` (false / zero when the denominator is 0).
- **Pure aggregation functions** over `[]Event` — windows are durations
  measured from an explicit `asOf time.Time` parameter (production callers
  pass the package `now` seam's value; tests pass fixed times, keeping the
  functions pure and trivially unit-testable):
  - `ModelStats(events, window) map[string]Stats` — all-agents, keyed by
    model id.
  - `AgentModelStats(events, agent, window) map[string]Stats` — one agent,
    keyed by model id (picker + after-survey scope).
  - `AllAgentModelStats(events, window) []ComboRow` — every observed
    (agent, model) pair, for the report command.
- `survey.jsonl` is wt-owned; nothing else reads it. modelman's
  `usage.jsonl` reader is unaffected (a deliberate rejection of storing
  survey events inside `usage.jsonl`, where every `model_id`-bearing line
  would count as a launch).

## Survey Prompt — `survey.PromptRun`

`PromptRun(r io.Reader, w io.Writer, store Store, agent string, m config.Model)`
— one implementation used by both launch paths:

- **Guards (no-op, silently):** stdin not a TTY (own injectable seam,
  mirroring `cmd/wt`'s `stdinTTY`), or `m.ID == ""` (shell/command
  sessions). Pre-launch failures (e.g. the ollama check) are not
  surveyed by construction — they print a summary and return before
  `runAgentCmd` is ever reached, so no guard inside `PromptRun` is
  needed for them.
- **Flow:**
  1. `survey · did it work? [y]es / [n]o / [s]kip (Enter=skip) `
     — `y`: continue; `n`: record `{worked:false}`, stop; `s`/Enter:
     record `{skipped:true}`, stop.
  2. `survey · speed 1(slow)-5(fast)? (Enter=skip) ` — digits 1-5;
     Enter leaves unrated.
  3. `survey · quality 1(bad)-5(great)? (Enter=skip) ` — same rules.
  4. Record the event, print `survey saved`, then the after-survey stats
     (below).
- Invalid input re-prompts. Store write failures print a stderr note and
  never change the exit code. The prompt loop reads via `bufio.Scanner`
  on the injected reader, so tests drive it with strings.
- **Wiring:**
  - Non-TUI — `cmd/wt/launch.go runAgentCmd`: immediately after the
    summary `Println`, *before* the `os.Exit(ExitCode)` branch, so
    non-zero agent exits are still surveyed (a crashed session is exactly
    a "did it work? no" data point).
  - TUI — `runAndWaitCmd` stashes `pendingSurvey{agent, m}` next to
    `pendingSummary`; `Run()` prints the summary, then invokes the survey
    through a `runSurvey` var seam (matching `tuiRun` / `newUsageStore`).
    Order everywhere: **summary → prompts → stats**.
- `agy` is surveyed on its wt-resolved model like every other agent
  (consistent with usage/rotation semantics); skipping is one keypress.

## Post-Survey Stats Output

Printed by `FormatAfterSurvey` (pure, in the survey package) from the
events loaded for the just-recorded answer — two rows, fixed-width
segments so the 1d/7d/30d columns align; `—` for windows with no
answered surveys:

```
survey saved
model           1d ✓100% q4.0 s4.0 (1)    7d ✓100% q4.0 s4.0 (1)    30d ✓96%  q4.2 s3.9 (12)
claude × model  1d —                     7d —                     30d ✓95%  q4.3 s4.0 (8)
```

Format per window segment: `✓<pct> q<quality> s<speed> (<n>)` where pct
is the integer worked percentage, quality/speed are one-decimal averages,
and `(n)` is the answered count. `—` when `Answered == 0`.

## Model Picker Segment

`internal/tui/model_list.go`:

- `enterModelPhase` loads events once via the store and builds
  `AgentModelStats(events, agent, 30d)` over the full catalog — the same
  one-pass pattern as the existing `Store.Counts` call. The store comes
  from a `newSurveyStore` seam (package var + real constructor), matching
  the `newUsageStore` pattern.
- `buildModelItems` takes the stats map as a new parameter and appends
  the segment **last**, after `[tags]`:
  - Format: `[⚠]✓<pct> q<quality> s<speed> n<answered>`
    (e.g. `✓96% q4.2 s3.9 n12`).
  - `⚠` replaces `✓` when `WorkedPct < 70%` and `Answered ≥ 3` — the
    "this combo does not work with this agent" signal.
  - Segment **omitted** when `Answered == 0`; untouched lines stay
    byte-identical, and appending last means existing column alignment
    never shifts.
- `FormatPickerSegment(Stats) string` lives in the survey package so the
  threshold and rounding rules (pct integer, quality/speed one decimal)
  are unit-tested in one place.

## `wt stats` Command — `cmd/wt/stats.go`

```
wt stats [--window 1d|7d|30d] [--model <id>] [--agent <name>]
```

- Window defaults to `30d`; filters narrow rows.
- One `Events()` pass → `AllAgentModelStats` → rows: per (agent, model)
  combo `{worked pct, quality avg, speed avg, n answered, skipped}` plus
  a per-model **(all)** aggregate row. Sorted by model id, all-agents row
  first, then agent name. Combos with zero answered and zero skipped in
  the window are omitted; an empty store prints `no survey data`.
- Rendered with the existing `renderTable` helper. Report only — a
  missing file is not an error, and the command never affects the exit
  code.

## Testing

- **`internal/survey`**: record/prune/flock/atomic write against
  `NewStoreAt` + the `now` seam (mirroring `usage_test.go`); aggregation
  windows; skip exclusion from the worked% denominator; rated-only
  averages; `FormatPickerSegment` threshold and empty cases;
  `FormatAfterSurvey` alignment; `PromptRun` flows — `y` → both ratings,
  `n` → stops after Q1, `s`/Enter → skip event, invalid → re-prompt,
  Enter on ratings → unrated — via injected reader/writer.
- **`internal/tui`**: `buildModelItems` with a stats map — segment
  presence, `⚠` rule, byte-identical line when no data; the survey seam
  (TTY-off → no prompt, prompt after summary print).
- **`cmd/wt`**: `stats` command output against a temp-dir store — window
  selection, `--model`/`--agent` filters, empty store.
- Every new `Test*` carries the top-level `//` what/why comment per repo
  convention.

## Docs

- `wt/CLAUDE.md` — new `internal/survey` package row; post-run flow
  (summary → survey → stats); picker line format.
- `docs/wt-agents/README.md#post-run-summary-line` — survey follows the
  summary line on both paths.
- `docs/wt-stats.md` — command reference with example output.
- `docs/wt-config.md` — untouched (no config surface in v1).

## Non-Goals (v1)

- No config toggle for the survey (every-exit with one-keypress skip is
  the chosen annoyance trade-off).
- No survey on `shell`/command sessions, pre-launch failures, or
  non-TTY launches.
- No cross-machine sync, no per-worktree or per-task tagging.
- modelman reads nothing; a future `modelman` surface could consume
  `survey.jsonl`, but that is out of scope here.