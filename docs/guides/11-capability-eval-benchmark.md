# Cross-category capability benchmark — `modelman benchmark eval`

> Use this to: score local models on reasoning/planning/coding/code_review/doc_summary,
> so a model/quant/engine choice can be made per use case instead of on
> throughput alone.

Design rationale: `docs/superpowers/specs/2026-09-17-capability-eval-benchmark-design.md`.

## Prerequisites

- Everything in [05-benchmarks](05-benchmarks.md)'s Prerequisites (no other local model loaded, backends healthy, isolation helpers on PATH).
- A working LiteLLM apiKey seeded into `~/.pi/agent/models.json` for any `route = "litellm"` row or judge — same requirement as [09-agent-benchmarks](09-agent-benchmarks.md).
- `OPENROUTER_API_KEY` available if the suite's `[judge]` or any row uses `route = "openrouter"`.
- `uv sync --extra eval` from `modelman/` — the `coding` category needs EvalPlus, which is not installed by plain `make install`.

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark eval list-categories --root ../benchmarks/tasks/eval
uv run modelman benchmark eval list-items --category doc_summary --root ../benchmarks/tasks/eval
uv run modelman benchmark eval run --suite ../benchmarks/suites/eval-sweep.toml \
    --root ../benchmarks/tasks/eval --dry-run
```

## Steps

### 1. Pick or author categories

A category lives under `benchmarks/tasks/eval/<name>/` — `items.toml` (the fixed prompt set) plus `rubric.toml`/`rubric.md` (except `coding`, which is EvalPlus-graded and has no rubric). Five categories ship today: `reasoning`, `planning`, `coding`, `code_review`, `doc_summary`. A rubric's `[dimensions]` must sum to 100.

### 2. Write or reuse a suite

A suite (`benchmarks/suites/eval-*.toml`) picks a `[judge]`, an optional `[coding]` override (dataset/limit), and a `[[rows]]` list (model + route, optionally a per-row `categories =` narrowing). `eval-sweep.toml` is the reference suite. Always `--dry-run` a new/edited suite first.

### 3. Run it

```bash
uv run modelman benchmark eval run --suite <path> --root ../benchmarks/tasks/eval \
    [--category <name>]... [--row <label-or-index>]...
```

There is no `--skip-judge` flag here (unlike `modelman benchmark agent run`): `coding` has no judge to skip, and the four judged categories always judge. Rows are grouped by provider and isolated once per group, same as `modelman benchmark`/`modelman benchmark agent` — see [05-benchmarks](05-benchmarks.md) Step 1.

### 4. Read the report

```bash
uv run modelman benchmark eval show --latest
```

`summary.md` has three tables: **Capability matrix** (rows grouped by registry family so quant/engine variants of the same base model sit adjacent, one column per category, composite /100 or EvalPlus pass@1%), **Per-category leaderboard** (top rows per category — the direct "best model for X" answer), and **Anomalies** (`JUDGE_FAIL`, isolation errors, EvalPlus execution errors).

### 5. Re-judge cheaply after a rubric edit

```bash
uv run modelman benchmark eval judge --latest --root ../benchmarks/tasks/eval [--row <row-dir>]...
```

Re-scores every judged category's items from their persisted `response.txt` — no regeneration, no isolation. Coding has nothing to re-judge (EvalPlus's pass@1 is deterministic).

## Gotchas

- **`coding` needs `uv sync --extra eval`** — a plain `make install`/`uv sync` does not pull EvalPlus, since it executes model-generated code and only this one category needs it.
- **`omlx` 4-bit/6-bit disambiguation is the same as every other suite here** — use a row's `provider =` override, and `direct_model =` for the server-side basename. See [05-benchmarks](05-benchmarks.md) Gotchas.
- **A row's `categories =` narrows which categories that row runs**, independent of `--category`'s CLI-level narrowing — both apply, intersected.
- **Judging (and coding's EvalPlus grading) both cost real time/spend per item** — a 5-category sweep across many rows adds up; use `--category`/`--row` to scope a suite down while iterating on content or rubrics.

## Going deeper

- Design spec: `docs/superpowers/specs/2026-09-17-capability-eval-benchmark-design.md`
- Module map: `modelman/CLAUDE.md`
- Source: `modelman/src/modelman/benchmark/eval/`, `modelman/src/modelman/benchmark/judge_core.py`
- Speed-only local benchmarks: [05-benchmarks](05-benchmarks.md)
- Agentic repo-level coding benchmark: [09-agent-benchmarks](09-agent-benchmarks.md)
