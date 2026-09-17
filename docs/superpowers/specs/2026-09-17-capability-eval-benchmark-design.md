# Cross-category capability benchmark (`modelman benchmark eval`) — Design

**Status:** Approved (2026-09-17)
**Date:** 2026-09-17 (spec written)
**Owner:** Keith Hartmann

## Problem

Two benchmark tools exist today and both answer narrow questions. `modelman
benchmark run` (and the legacy `benchmarks/*-benchmark` scripts) measure raw
throughput/TTFT on one or two workloads (`chat`/`code`/`long`/`short`) — speed
only, no notion of correctness. `modelman benchmark agent run` measures one
thing very deeply: can a model, driven by `pi` as a real coding agent, fix one
specific bug in one invented codebase (`day31-drift`) — a repo-level, agentic,
gates+judge-scored signal, expensive per row (a full agent session) and scoped
to a single coding task.

Neither tool answers the actual question motivating both: **which local model
format, quantization, and engine combination is the right choice for a given
kind of work** — reasoning, planning, single-turn coding, code review, doc
summary — when the choice is made per use case rather than once for
"benchmarking work in general." A model that's a fine choice for summarizing
docs may be a poor choice for spotting a race condition in a diff, and nothing
in the current setup lets that comparison happen without hand-running each
model in a chat window.

External evaluation harnesses solve pieces of this — see
`docs/benchmarking-tools-for-apple-silicon/benchmark-tools-research.md`.
EvalPlus in particular ships a real, community-maintained grader for coding
(HumanEval+/MBPP+) that needs no bespoke rubric. Nothing off-the-shelf covers
reasoning/planning/code-review/doc-summary the way this project wants them
scored, and the project already has a working, trusted pattern for that kind
of grading — the agent benchmark's LLM judge (strict-JSON rubric contract,
multi-sample median, retry-on-malformed-response) — that is currently
hardcoded to one rubric shape (diff-fixing dimensions) and only reachable from
the agentic pi-driven path.

## Goals

1. **A third benchmark module**, `modelman/src/modelman/benchmark/eval/`,
   sibling to `benchmark/agent/`, reusing in-process what already exists:
   provider isolation (`providers/lifecycle/orchestrate.py`), registry model-id
   resolution and the `direct`/`litellm`/`openrouter` route pattern, and a
   generalized version of the judge's scoring mechanics.
2. **Single-turn only.** Every eval item is one prompt in, one completion out,
   scored — no tools, no workspace, no git diff. This is a capability probe,
   not a second agentic harness; the existing `agent` subsystem already owns
   the agentic tier.
3. **Five categories, each a fixed hand-authored item set + rubric**:
   reasoning, planning, coding, code_review, doc_summary. Adding a sixth later
   is a new directory, not a runner code change.
4. **`coding` is graded by EvalPlus**, not by the in-house judge — a real
   pass@1 signal against HumanEval+/MBPP+, invoked as a subprocess against the
   row's resolved OpenAI-compatible endpoint.
5. **The other four categories are graded by a generalized judge** — the
   agent benchmark's judge mechanics, lifted out of their hardcoded rubric so
   each category supplies its own dimensions.
6. **A suite (TOML) drives a matrix of rows (model × route) against a set of
   categories**, same shape as the existing agent-benchmark suites, including
   optional cloud/OpenRouter rows as quality-ceiling references.
7. **One report** that groups by registry `family` (so quant/engine variants
   of the same base model sit side by side) and answers "which row wins at
   which category" directly, not five separate report formats to cross-read.

## Non-goals

- Not a replacement for `modelman benchmark run` (pure speed) or
  `modelman benchmark agent run` (repo-level agentic coding) — both keep their
  current scope; this is a third, complementary tool.
- Not multi-turn or tool-using evals. If a category later needs that, it's a
  new kind of row, not a retrofit onto this single-turn runner.
- Not adopting Inspect AI, lm-eval-harness, or any other external harness as
  the primary framework for anything except `coding` (EvalPlus). The other
  four categories intentionally reuse the project's own judge pattern rather
  than a new dependency's task/scorer API.
- Not sandboxing model-generated code beyond what the `agent` benchmark
  already accepts. EvalPlus's code-execution step runs locally, same trust
  model `gates.py` already applies to agent-produced diffs.
- Not authoring the full final item sets in this document — the shapes and
  rubric dimensions below are fixed; the concrete prompts/diffs/docs are
  authored during implementation.

## Architecture

```
modelman/src/modelman/benchmark/
  judge_core.py         # NEW — generalized: Rubric, JudgeScore, JudgeOutcome,
                         #   strict-JSON parsing, retry loop, multi-sample median.
                         #   Extracted from agent/judge.py; no behavior change there.
  agent/
    judge.py            # unchanged behavior — imports shared mechanics from
                         #   judge_core.py, keeps anonymize_diff/detect_overclaim/
                         #   its own fixed Rubric instance (diff-fixing dimensions)
  eval/                  # NEW
    cli.py               # `modelman benchmark eval` subcommands
    category.py          # Category, Item, Rubric loading from benchmarks/tasks/eval/<name>/
    suite.py             # Suite TOML parsing, row x category expansion (mirrors agent/suite.py)
    judged_runner.py     # single-turn completion + judge_core scoring, for reasoning/
                         #   planning/code_review/doc_summary
    evalplus_runner.py   # subprocess wrapper around EvalPlus for `coding`
    report.py            # summary.md: capability matrix, per-category leaderboard, anomalies
    isolation.py         # thin reuse of providers/lifecycle/orchestrate.py, same
                         #   pattern as benchmark/isolation.py (group rows by provider)
```

`judge_core.py` is the one refactor to existing code. Today
`agent/judge.py` hardcodes:

```python
DIMENSIONS = ("root_cause", "approach", "test_quality", "scope", "coherence")
MAX_POINTS = {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10}
VERDICTS = {"symptom_patch", "partial", "principled_fix", "no_useful_change"}
```

These become a `Rubric` dataclass (`dimensions: dict[str, int]`, `verdicts:
set[str] | None`) passed into `judge_row`/`parse_judge_response` instead of
read as module constants. `agent/suite.py` builds the agent's fixed `Rubric`
once at import time and passes it through; every existing call site keeps its
current signature otherwise. `anonymize_diff` and `detect_overclaim` stay in
`agent/judge.py` — they're diff-specific and don't apply to non-diff
categories.

## Category data model

Each category is a directory under `benchmarks/tasks/eval/<category>/`:

```
benchmarks/tasks/eval/doc_summary/
  items.toml    # the fixed prompt set: [[items]] with id + prompt + any
                #   per-item judge-only reference notes
  rubric.md     # human/judge-facing scoring prose (day31-drift's rubric.md pattern)
  rubric.toml   # machine-readable: [dimensions] name -> max points (sum to 100),
                #   optional verdicts list
  meta.toml     # never shown to model or judge — human notes on intent/expected tiers
```

`coding`'s directory drops `rubric.md`/`rubric.toml` (EvalPlus grades it) and
`items.toml` instead selects the EvalPlus dataset/params:

```toml
# benchmarks/tasks/eval/coding/items.toml
dataset = "humaneval"   # or "mbpp"
limit = 40                # subset size — full HumanEval+ per row is too slow
                           # across a multi-model x multi-engine sweep
```

Item shape example (`code_review`):

```toml
[[items]]
id = "off-by-one-loop"
prompt = """
Review this diff for correctness issues. Point out anything you'd block on.

<diff>
...
</diff>
"""
# never shown to model or judge — human record of what was seeded
[items.meta]
seeded_issue = "loop uses range(n+1), off-by-one past the last element"
```

## Suite format

```toml
# benchmarks/suites/eval-sweep.toml
name = "eval-sweep"
cooldown_s = 15.0

[judge]
model = "anthropic/claude-opus-5"
route = "openrouter"
temperature = 0.0
samples = 3
max_attempts = 2

[coding]
dataset = "humaneval"
limit = 40

[[rows]]
model = "ollama/qwen3.8:27b-mlx"
route = "litellm"
# categories omitted = run every configured category

[[rows]]
model = "omlx/mlx-community--Qwen3.8-27B-4bit"
provider = "omlx"       # disambiguates 4-bit vs 6-bit, same override agent suites use
route = "direct"

[[rows]]
model = "openrouter/anthropic/claude-opus-5"
route = "openrouter"
categories = ["coding", "reasoning"]   # cloud baseline scoped down to control spend/time
```

Rows reuse `agent/suite.py`'s model/route/provider-override resolution
directly (same `direct_model` override rule for oMLX's org-prefixed registry
ids vs. bare server-side names — see `docs/guides/05-benchmarks.md` Gotchas).
A suite expands to (row × category × item) executions; `--category` and a
row's own `categories =` both narrow that set. `[judge]` and `[coding]`
mirror the existing agent-suite `[judge]` block and stay independent of each
row's own route — the judge call always goes through its own configured
route, exactly as today.

## Execution flow

1. Parse the suite; group rows by provider; for each group, isolate once
   (`providers/lifecycle/orchestrate.py`, unchanged), same pattern as both
   existing benchmark modules.
2. For each row, for each category in scope:
   - **`coding`**: resolve the row's endpoint (base_url + server-side model
     name), invoke EvalPlus's CLI as a subprocess against it, parse its
     pass@1 output. Exact CLI flags get pinned against the installed EvalPlus
     version during implementation — the research doc's cited `--backend
     openai --base-url` form is the starting point, not a final contract.
   - **everything else**: for each item, send one direct chat completion to
     the row's endpoint, then score the response via `judge_core.judge_row`
     using that category's `Rubric` and `rubric.md` prose (embedded in the
     judge prompt, same as today). A row's category score is the mean of its
     item totals, normalized to /100 (each rubric's dimensions sum to 100,
     matching the existing `day31-drift` convention).
3. Restore providers in a `finally` block, same as both existing modules —
   a failed restore never costs already-collected results.
4. Judging happens after generation completes for that row/category (not
   interleaved with a still-loaded local model), same ordering rule the agent
   benchmark already enforces between local generation and cloud judging.

## Report

`~/.config/local-ai/benchmarks/eval-<run-id>/`:

```
run.toml                          # suite snapshot
summary.md                        # see below
metrics.jsonl
<row-dir>/<category>/response.txt          # judged categories
<row-dir>/<category>/judge.json            # judged categories
<row-dir>/<category>/evalplus_result.json  # coding only
<row-dir>/<category>/score.json            # normalized /100 + wall_s, all categories
```

`summary.md` has three tables:

1. **Capability matrix** — rows grouped by registry `family` (so e.g.
   `ollama/qwen3.8:27b-mlx` and `omlx/...Qwen3.8-27B-4bit` sit adjacent),
   columns = each category run, cells = composite /100 or EvalPlus pass@1 %,
   with `JUDGE_FAIL`/`TIMEOUT`/`N/A` where applicable. This is the primary
   "which format/quant/engine wins at which use case" view.
2. **Per-category leaderboard** — top rows per category, the direct answer
   to "best model for doc summary" etc.
3. **Anomalies** — `JUDGE_FAIL`, timeouts, empty/truncated responses,
   EvalPlus execution errors. Not a speed report — pure throughput stays
   `modelman benchmark run`'s job; this table exists so a bad row is visible
   as an anomaly rather than a silently low score.

## CLI

Mirrors the existing `agent` subcommands:

```
modelman benchmark eval list-categories [--root <path>]
modelman benchmark eval list-items --category <name> [--root <path>]
modelman benchmark eval run --suite <path> [--category <name>]... \
    [--row <label-or-index>]... [--dry-run] [--skip-judge]
modelman benchmark eval show --latest | --run-id <id>
modelman benchmark eval judge --latest [--row <row-dir>]...   # re-score, no re-run
```

## Content plan for the five categories

Each category ships ~6-10 hand-authored items, a `rubric.toml`/`rubric.md`
pair (dimensions sum to 100), and a `meta.toml` with ground-truth notes never
shown to the model or judge — the `day31-drift` convention. Item authoring
happens during implementation.

| Category | Item shape | Rubric dimensions (sum to 100) |
|---|---|---|
| **reasoning** | Multi-step logic/math/deduction puzzles with a known correct answer, freeform response (no code) | `correct_answer` 40, `valid_reasoning_chain` 30, `no_unjustified_leaps` 15, `clarity` 15 |
| **planning** | Short feature/migration descriptions; model produces an implementation plan, no code | `decomposition` 25, `risk_identification` 25, `completeness` 25, `scope_discipline` 25 |
| **coding** | EvalPlus HumanEval+/MBPP+ subset (`[coding].limit` in suite) | *(none — deterministic pass@1 from EvalPlus)* |
| **code_review** | A pasted diff/snippet with one seeded issue (bug/security/race/leak/style, varied per item); model reviews, doesn't fix | `bug_detection` 40, `false_positive_control` 20, `severity_judgment` 20, `actionability` 20 |
| **doc_summary** | A ~300-1500 word doc excerpt (README/changelog/policy/spec, varied domain); model summarizes | `factual_accuracy` 35, `coverage` 30, `conciseness` 20, `structure` 15 |

`reasoning` is judge-scored rather than answer-matched because free-text
local-model output formats final answers inconsistently, and these backends
expose no logprobs to fall back on for a stricter check — the same
chat-only-endpoint caveat the research doc raises for `lm-eval-harness`
loglikelihood tasks applies here.

## Dependencies

EvalPlus becomes an optional extra (`modelman[eval]` in `pyproject.toml`), not
a default `make install` dependency — it's the one piece of this subsystem
that executes model-generated code via a separate package, and normal
modelman usage (TUI, expose, `benchmark run`, `benchmark agent run`) has no
reason to require it.

## Testing

- `judge_core.py`'s extracted mechanics: existing `agent/judge.py` tests must
  keep passing unmodified (behavior-preserving refactor) — add new tests
  instantiating a second, differently-shaped `Rubric` to prove the module is
  actually generalized, not just relocated.
- `eval/suite.py`: TOML parsing, row × category expansion, row-level
  `categories =` narrowing, provider-override resolution (reuse of
  `agent/suite.py`'s tested logic, so mostly new-surface tests only).
- `eval/judged_runner.py`: mock transport, verify score normalization to
  /100 and mean-of-items aggregation.
- `eval/evalplus_runner.py`: mock subprocess, verify endpoint/model
  resolution and pass@1 parsing — no real EvalPlus invocation in unit tests.
- `eval/report.py`: golden-file test on a synthetic multi-row, multi-category
  run producing the three summary.md tables, family-grouping included.

## File map

- `modelman/src/modelman/benchmark/judge_core.py` — new, extracted from `agent/judge.py`
- `modelman/src/modelman/benchmark/agent/judge.py` — modified to import from `judge_core.py`
- `modelman/src/modelman/benchmark/eval/` — new module tree (cli.py, category.py, suite.py, judged_runner.py, evalplus_runner.py, report.py, isolation.py)
- `modelman/pyproject.toml` — new `eval` optional-dependency extra (EvalPlus)
- `benchmarks/tasks/eval/<category>/` — new, one directory per category (items.toml, rubric.md, rubric.toml except `coding`, meta.toml)
- `benchmarks/suites/eval-*.toml` — new suite files
- `docs/guides/` — new guide (numbered after 09), documenting this tool day-to-day, following the 09-agent-benchmarks.md pattern

## Phasing

1. `judge_core.py` extraction + `agent/judge.py` regression tests green.
2. `eval/` runner + suite + isolation reuse, wired to a stub category (no
   real content yet) to prove the plumbing end-to-end.
3. EvalPlus integration for `coding` (optional extra, subprocess wrapper).
4. Author content for the four judged categories (reasoning, planning,
   code_review, doc_summary) — items, rubrics, meta notes.
5. Report generation (capability matrix, leaderboard, anomalies) against a
   real multi-row run.
6. Guide doc.

## Risks

- **EvalPlus CLI surface may not match the research doc's cited flags
  exactly for the installed version** — mitigated by pinning a version and
  verifying the exact invocation live during Phase 3, same "verified live"
  discipline the existing guides already follow.
- **A 5-category x N-row sweep is a lot of judge calls** (4 judged categories
  x ~8 items x `[judge].samples` per row) — cloud API spend scales with row
  count. `--category`/`--skip-judge` and per-row `categories =` exist
  specifically to let a suite be scoped down for cheap iteration.
- **Content quality is the actual bottleneck.** A framework with weak items
  (ambiguous rubric, an item any model trivially solves or fails) produces
  numbers that look precise but aren't discriminating — the same risk
  `day31-drift`'s authoring rules (bespoke domain, deterministic gate,
  plausible wrong answer) were written to manage; the five categories here
  need the same care per item.
