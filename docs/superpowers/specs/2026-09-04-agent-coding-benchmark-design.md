# Speed + quality agentic coding benchmark — Design

**Status:** Approved (2026-09-04)
**Date:** 2026-09-04 (spec written)
**Branch:** `docs/agent-coding-benchmark-spec`
**Owner:** Keith Hartmann

## Problem

Every benchmark in this repo measures speed and nothing else. `modelman benchmark`
and the `benchmarks/*-benchmark` scripts report TTFT, throughput, and total time
for single-turn completions; `benchmarks/qwen3.8-benchmark.md` and
`ornith-1.5-benchmark.md` rank backends purely on tokens/sec. The one existing
agentic test, `wt/scripts/agents-smoke.sh`, checks that an agent echoes back a
marker string — pass/fail plumbing, no output quality, no performance stats.

That leaves the actual question unanswerable: **a configuration that generates
tokens fast is not necessarily a configuration that writes working code.** A
4-bit-quantized 9B model may finish a task in 40 seconds while a 27B takes six
minutes, and the fast one may have patched the symptom rather than the cause.
Nothing in the current setup distinguishes "fast and correct" from "fast and
wrong," so model and quantization choices get made on throughput numbers that say
nothing about whether the output holds up.

What is missing is a harness that runs a real engineering task through a coding
agent across a matrix of model / thinking-level / route configurations, measures
the speed of each configuration, grades the quality of what it produced, and
reports both axes without letting either one contaminate the other.

## Goals

1. **One-shot agentic runs across a configuration matrix.** Each row specifies a
   model, a thinking level, and a route (`direct` vs `litellm`); a real coding
   agent (pi) performs a real task in a scratch repo with tools enabled.
2. **Speed stats that mean something for multi-request runs.** TTFT, busy
   throughput, end-to-end throughput, tool time, token splits, and cost — defined
   precisely enough to be comparable with the existing single-turn benchmark
   numbers.
3. **Quality measured two independent ways:** deterministic hard gates (do the
   hidden tests pass, did it break anything, did it tamper, was its own regression
   test vacuous) and a blind LLM rubric score from a separately chosen robust
   judge model.
4. **Speed and quality stay separable in the report,** with a gate-derived cap
   applied to the composite so a fast-but-broken config cannot score well.
5. **Isolation rules respected** — one local model loaded at a time, reusing
   `bin/llm-isolate-provider`, with the cloud-model judge deliberately sequenced
   after `restore_providers()`.
6. **Adding a task costs no code.** Tasks are declarative bundles.

## Non-goals

- **Multi-agent grading.** v1 drives pi only. An `AgentRunner` protocol is the
  seam for later `claude`/`codex` adapters; per-agent flag differences (the
  five-field matrix in `agents-smoke.sh`) are out of scope here.
- **Agent rows on pi-native subscription providers** (claude/codex/copilot
  native). v1 rows must be reachable via LiteLLM (`route = "litellm"`) or via a
  local backend's own endpoint (`route = "direct"`).
- **Replacing `modelman benchmark`'s single-turn workloads.** Those remain the
  fast triage tool and the source of comparable throughput baselines.
- **Public-leaderboard-grade validity.** Hidden tests live in this repo; anyone
  with repo access can read them. This is a personal decision tool, not an
  unattended eval.
- **Plotting/PNG dashboards.** `metrics.jsonl` exists so plotting can be added
  later without changing the harness.
- **Iterative repair turns** (handing test output back for a second attempt).
  See *Deferred: the repair round* — the seam is designed for, disabled in v1.

## Resolved decisions

| Fork | Chosen | Why |
|---|---|---|
| Graded artifact | **Agentic repo task** in a scratch git repo, graded as `git diff` | Single-shot code generation is already covered by the existing `code` workload; the agentic task is what measures engineering |
| Agent visibility | **Self-verifying one-shot** — visible tests runnable, hidden tests copied in only after the run | Realistic, bounded cost (one agent run per row), and it enables the vacuous-test and overclaim checks |
| First task archetype | **Latent bug hunt** | Strongest signal per authoring hour; every tempting fix can be made to fail a hidden test |
| Judge information | **Fully blind to gates**, hard gates applied as a multiplicative cap afterward | Avoids failure-anchoring smear across rubric dimensions; keeps gate-vs-judge disagreement informative |
| Architecture | **Extend modelman** (`modelman benchmark agent`) | CLAUDE.md names modelman the canonical benchmark owner; reuses isolation, results, state, tests, CI; avoids a second benchmark CLI |
| Judge transport | **Direct HTTP via LiteLLM at temperature 0**, not a pi subprocess | pi exposes no temperature control; `workloads/base.py` already has the plumbing; fresh request ≡ fresh context |

## Verified against the live setup

These were confirmed empirically before designing, not assumed:

- `pi --mode json` streams `session`, `agent_start`, `turn_start`,
  `message_start`, `message_update` (with `thinking_delta` / `text_delta` /
  `toolcall_*`), `message_end`, `turn_end`, `agent_end`, `agent_settled`.
  Client-side wall-clock timestamps on those lines yield TTFT and per-message
  duration. `usage` is zero on early deltas and populated on the final
  `message_end`/`turn_end` (observed: `input=382, output=38, reasoning=33`),
  which is the authoritative token source. `message_end` carries the final
  content.
- Pi has **no built-in Ollama provider** — but `~/.pi/agent/models.json` already
  contains one, created by wt. It currently holds two providers, both
  `api = "openai-completions"`: `litellm` → `http://localhost:4000/v1` with 42
  models keyed by **registry id** (e.g. `ollama/qwen3.8:27b-mlx`), and `ollama` →
  `http://localhost:11434/v1` with 37 models keyed by **bare `model_name`**
  (`qwen3.8:27b-mlx`). Both are written by `wt/internal/agents/pi_models.go`.
  So `route = "direct"` for Ollama already exists as a working pattern, while
  `omlx` (`http://localhost:8000/v1`) and `llamacpp` (`http://localhost:8080/v1`,
  both per `~/.config/litellm/config.yaml` `api_base` entries) must be
  synthesized by the harness.
- `PI_CODING_AGENT_DIR` overrides pi's config directory (default `~/.pi/agent`),
  and `--session-dir` overrides session storage. Together these let a run use a
  generated config without mutating the user's live `models.json`.
- Thinking levels are `off|minimal|low|medium|high|xhigh|max` and are
  model-declared in pi via `thinkingLevelMap`; a backend that ignores
  `reasoning_effort` makes the level a silent no-op. `usage.reasoning` is the
  observable proof, hence its own column.
- **pi has no permission-bypass flag.** `piDriver.YoloFlag()` returns `""` and
  `wt/internal/agents/pi.go` documents it as "no documented permission-bypass
  flag." Tool execution in `-p`/`--mode json` therefore needs no approval flag;
  the harness passes `--no-approve` to pin project-trust behavior (see below),
  not to enable tools.
- wt addresses models as `--model <pi-provider>/<launch-id>` and **does not pass
  `--provider`**, because pi splits `--model` on the first slash. A registry id
  sent under the `ollama` provider "could never be addressed and its bare form
  would be sent upstream (400 at the gateway)" per the comment in `pi.go`. The
  harness uses the same single-flag form.
- LiteLLM currently serves 37 deployments per `/v1/models`, so `route =
  "litellm"` model ids resolve against `~/.config/litellm/config.yaml` and the
  registry. Pi's locally-generated `litellm` provider block lists 42 entries — a
  superset written by `pi_models.go` from the registry, not a disagreement with the
  proxy's own count. Preflight resolves rows against the live proxy, not the local
  block, so a stale local entry is caught as an unresolvable row.

## Architecture

```
modelman benchmark agent run --suite benchmarks/suites/q4-agent-sweep.toml
```

`modelman/src/modelman/benchmark/agent/`, mounted on the existing `benchmark_app`
as a sub-Typer:

| module | does | depends on |
|---|---|---|
| `suite.py` | load + validate a suite TOML; expand cartesian rows into `RowConfig` | `registry.py` for model→provider resolution |
| `task.py` | load + validate a task bundle (`task.md`, `visible/`, `hidden/`, `gates.toml`, `rubric.md`, `meta.toml`) | filesystem only |
| `workspace.py` | copy `visible/` → temp dir, `git init` + baseline commit, copy in `hidden/` post-run, expose diff and baseline-commit checkout | `workspace` only |
| `pidriver.py` | build the route's pi config, spawn `pi --mode json`, timestamp the JSONL, parse usage, enforce timeout | `suite.py`, `task.py` |
| `gates.py` | ordered gate evaluation, pass ratios, failure taxonomy codes, vacuous-test check | `workspace.py` |
| `judge.py` | anonymize diff, build judge prompt, call transport, validate score contract, retry, apply cap | `task.py` (rubric), injected transport |
| `report.py` | per-row artifact writes, `summary.md` four tables, `metrics.jsonl` | results of the above |
| `cli.py` | `agent run` / `agent list-tasks` / `agent list-suites` / `agent show --latest` | all |

`agent/runner.py` orchestrates phases 1–3 and owns the isolation loop; it is the
only module that imports `benchmark.isolation`.

### CLI surface

| command | flags |
|---|---|
| `modelman benchmark agent run` | `--suite <path>` (required), `--row <label-or-index>` (repeatable, filters both execution and `agent judge`), `--passes <n>`, `--cooldown <s>`, `--agent-timeout <s>`, `--skip-judge`, `--results-dir <path>`, `--keep-workspace <dir>`, `--dry-run` (resolve + expand rows, print the matrix, run nothing) |
| `modelman benchmark agent judge` | `--run-id <id>` \| `--latest`, `--row …`, `--samples <n>` — re-judge existing artifacts without re-running any agent, which is how a rubric edit gets evaluated cheaply |
| `modelman benchmark agent list-tasks` | — bundles under `benchmarks/tasks/` with gate/test counts |
| `modelman benchmark agent list-suites` | — suites under `benchmarks/suites/` with expanded row counts |
| `modelman benchmark agent show` | `--latest` \| `--run-id <id>` — prints the persisted `summary.md`, mirroring `benchmark show-results` |

`--dry-run` earns its place: the matrix is expanded data, and printing 16 resolved
rows before loading a 27B six times is a cheap defence against a mistyped suite.

### Run pipeline

```
phase 0  preflight     validate suite + task bundle
                       fail early: isolation helpers absent from PATH, unresolvable
                       model row, missing [routes.direct.<provider>] block,
                       hidden/ leaked into visible/, judge key unavailable
phase 1  execute       group rows by provider → isolate_provider(provider) once per group
                         (stop others, start, warmup exact variant)
                         for each row:
                           fresh temp workspace (never reused, never a worktree)
                           generated PI_CODING_AGENT_DIR + models.json for the route
                           pi --mode json … under hard timeout, timestamps captured
                           collect diff + final message + session JSONL
                           copy in hidden/ → run gates 1..9
                         restore_providers() once at end of local phase
phase 2  judge         runs AFTER restore on purpose: the judge is a cloud model and
                       must not contend with a loaded local model, and its latency
                       must stay off the measured clock
                         per row: anonymize diff → fresh judge request → scores
phase 3  combine       gates × rubric → capped composite → summary.md + metrics.jsonl
```

Row order within a provider group is fixed for reproducibility. The consequence —
pass 1 of a local 27B runs on cooler hardware than pass 4 — is surfaced in the
report as run order and `passes`, not engineered around; multi-pass medians remain
the answer as they are today.

### Task bundle layout

```
benchmarks/tasks/<task-id>/
  task.md        # the bug report handed to the agent; never names the culprit file
  visible/       # copied into the workspace, becomes the seeded git repo
  hidden/        # copied in ONLY after the run finishes, from outside the workspace
  gates.toml     # ordered gate definitions + parameters
  rubric.md      # dimensions, scale, closed verdict enum, output contract
  meta.toml      # intended root cause, tier expectations, timeouts, notes
```

`meta.toml` is never shown to the agent and never shown to the judge. It exists
for the human reading the report and for the harness to know which baseline commit
the vacuous-test check runs against.

## The first task: `day31-drift`

Domain `kettlecomb`, an invented metered-seat credit ledger. Invented names are
load-bearing: a public or textbook repo would be in the models' training data, and
the run would then measure memorization. ~350 lines, stdlib only, `unittest` so
gates never need network access or a package install.

```
visible/kettlecomb/
  calendarlib.py   # month_length(), add_months()  ← the defect lives here
  billing.py       # prorate()  ← the symptom surfaces here, one file away
  ledger.py        # accrues cycles: anchor = add_months(anchor, 1)
  __init__.py
visible/tests/test_ledger.py   # passes as shipped; every fixture uses a day-15 anchor
visible/README.md
hidden/test_day31.py           # 6 boundary tests, copied in post-run
```

**The defect.** `add_months` does `d.replace(month=d.month + n)` inside
`try/except ValueError`, and on exception **snaps the day to 30**. Day-31 accounts
therefore drift backward through every short month, and day-30 accounts drift
through February. Because `ledger.py` applies `add_months` iteratively, the error
compounds across cycles, which is what makes the reported symptom look like a
proration bug rather than a calendar bug.

**The trap.** The bug report describes the symptom in `billing.prorate`, so the
tempting edit site is the wrong file, and every cheap fix fails at least one hidden
test:

| tempting fix | defeated by |
|---|---|
| `except ValueError: day = 28` | 31-day months now return 28 |
| clamp inside `prorate` (symptom patch) | anchor accumulation test still drifts |
| correct clamping, but no year rollover | December + 1 → month 13 |
| correct clamping, hardcoded 28 for February | leap-year anchors |
| correct clamping + leap rules + rollover | — this is the intended fix |

**Hidden tests** (the pass-ratio denominator, m = 6):

1. day-31 anchor → February, non-leap year → 28
2. day-31 anchor → February, leap year → 29
3. day-31 anchor → a 30-day month → 30, not 28 and not 31
4. December anchor + 1 month → January, year increments
5. February-29 anchor + 12 months → February 28 of a non-leap year
6. 8-cycle accumulation from a day-31 anchor → no drift, proration credits exact

**Expected tiers**, recorded in `meta.toml` so the report can be read against them:
frontier cloud models should clear 6/6 with a real regression test; mid-tier cloud
and healthy local 27B should clear 3–5/6, typically failing the leap-year or the
accumulation case; quantized/smaller local models are expected to produce a
plausible symptom patch in `prorate` — non-empty diff, self-consistent explanation,
0 composite after cap. A row that "adds a test" which passes on the baseline is
expected at this tier too, which is exactly why gate 8 exists.

**Task authoring rules** (apply to every future task):

- The codebase must be the obstacle — unreadable from the prompt alone.
- A deterministic gate must exist.
- A plausible wrong answer must exist; failure means "confidently produced
  something that doesn't hold up," not "said no."
- Bespoke domain, no framework dependencies, ≤500 lines total.
- `task.md` names neither the culprit file nor the intended fix.

## Suite format

```toml
name = "q4 agent sweep"
task = "benchmarks/tasks/day31-drift"
passes = 1
cooldown_s = 20
agent_timeout_s = 420

[judge]
model = "openrouter/anthropic/claude-opus-4"   # separately chosen, robust, cloud
thinking = "low"
temperature = 0.0
samples = 1          # >1 → median per dimension, then sum
max_attempts = 2     # JSON-contract retries on one sample
route = "litellm"

[routes.direct.omlx]
base_url = "http://localhost:8000/v1"
api = "openai-completions"

[routes.direct.ollama]
base_url = "http://localhost:11434/v1"
api = "openai-completions"

[routes.direct.llamacpp]
base_url = "http://localhost:8080/v1"
api = "openai-completions"

[[rows]]
models   = ["ollama/qwen3.8:27b-mlx", "omlx/Ornith-1.5-35B-A3B-MLX-6bit"]
thinking = ["off", "high"]
routes   = ["direct", "litellm"]
```

- A `[[rows]]` entry whose fields are lists expands as a cartesian product —
  2 models × 2 thinking × 2 routes = 8 rows. Hand-writing a 24-row matrix is how
  suites stop getting maintained.
- A row may instead pin one combination (`model = `, `thinking = `, `route = `) and
  carry `label = "…"` used in the report and as the row directory name component.
- `provider` (the isolation target) derives from the registry model id unless
  overridden. The override is how omlx 4-bit and 6-bit stay distinguishable, since
  both variants are served by one provider and warmup must name the exact variant.
- `[routes.direct.<provider>]` is required for any row using `route = "direct"`;
  preflight fails with the provider name and the missing block if absent.
- `benchmarks/suites/smoke.toml` is a one-row, fast-cloud-model, `--skip-judge`
  suite for plumbing checks.

### Route resolution for the agent process

`pidriver` maps `(model, route)` → `(pi provider, pi launch id)`:

- `route = "litellm"`: provider `litellm`, launch id = registry model id (the
  LiteLLM deployment name, e.g. `ollama/qwen3.8:27b-mlx`).
- `route = "direct"`: provider = the backend id (`ollama`, `omlx`, `llamacpp`),
  launch id = registry `model_name` (the backend's own artifact name, e.g.
  `qwen3.8:27b-mlx`). This mirrors the existing single-turn runner's convention of
  `target.model_name if route == "direct" else target.model_id`, and mirrors wt's
  `ollama` provider convention exactly.

The generated `models.json` under a per-run `PI_CODING_AGENT_DIR` holds exactly one
provider block for the row's route. **Provider ids and entry shape are deliberately
identical to the ones `wt/internal/agents/pi_models.go` writes today** (`litellm`,
`ollama`, fields `id`/`contextWindow`/`input`/`reasoning`/`_launch`), with
`contextWindow` and `reasoning` copied from the matching live entry where one
exists. Parity is the point: an agent row should behave like a wt-launched session,
and a future divergence between the two shapes becomes a visible diff rather than a
silently different benchmark. Preflight probes the resolved `base_url` and fails
with the provider name if it does not answer.

Agent invocation, one process per row:

```
pi --mode json --session-dir <row artifacts dir> \
   --model <pi provider>/<pi launch id> --thinking <level> \
   --no-extensions --no-skills --no-prompt-templates --no-context-files \
   --no-approve \
   -p "<task.md body + harness instructions>"
```

Tools stay enabled (that is the point of the task) and pi needs no bypass flag to
use them. `--no-context-files` and `--no-skills` keep this repo's `CLAUDE.md` and
skills out of the run, so the agent works the task and not the monorepo.
`--no-approve` is passed explicitly rather than relying on the user's
`defaultProjectTrust`: non-interactive modes never prompt, but a global setting of
`"always"` would otherwise let project resources load, and `"ask"`/`"never"` would
not — an invisible difference between two runs of the same row.

**Secrets.** The generated config dir contains the LiteLLM API key. It is written
to a temp dir removed in `finally`, and no part of it is copied into run artifacts;
`run.toml` and all logs mask key values. Keys are read from the existing
`~/.pi/agent/models.json` / `~/.config/litellm/config.yaml` / LaunchAgent plist,
never hardcoded into the suite file.

## Metrics

An agent row is N LLM requests plus tool execution, so a single tokens/sec is
meaningless unless defined. Per row:

| metric | definition |
|---|---|
| `wall_ms` | process spawn → exit |
| `requests`, `turns` | count of assistant messages / tool rounds |
| `ttft_first_ms` | client timestamp of first `text_delta` minus first request start |
| `ttft_mean_ms`, `ttft_max_ms` | across all requests; later requests expose cache effects |
| `gen_tok_s` | Σ output tokens ÷ Σ(`message_start`→`message_end`) — **busy** throughput, excludes tool exec and prompt assembly |
| `e2e_tok_s` | Σ output tokens ÷ `wall_ms` |
| `in_tok`, `out_tok`, `cache_read_tok`, `reasoning_tok` | summed from `usage` on each `message_end` |
| `tool_ms` | Σ `tool_execution_start`→`tool_execution_end` |
| `cost_usd` | Σ `usage.cost.total` (0 for local, nonzero for OpenRouter) |

`gen_tok_s` is the figure directly comparable to the existing
`qwen3.8-benchmark`/`ornith-1.5-benchmark` tables. The `gen_tok_s` vs `e2e_tok_s`
gap is diagnostic on its own: a slow row with healthy busy throughput is spending
its time in tool rounds, which is a reasoning cost, not an inference cost, and the
two require different remedies.

Two derived anomaly flags:
- **thinking no-op** — `thinking` is non-`off` but `reasoning_tok == 0`: the backend
  silently ignored the level, so the row is not comparable to its partner row.
- **cold first token** — `ttft_first_ms` grossly out of family with `ttft_mean_ms`
  (≥3× median): the model was still loading despite warmup. Flagged, not averaged
  away.

## Gates and failure taxonomy

Evaluated in this order; each has its own code so "fast but invalid" is
classifiable instead of one FAIL bucket.

| # | gate | code on failure |
|---|---|---|
| 1 | pi exited, `message_end` present, session file captured | `AGENT_ERROR` |
| 2 | completed within `agent_timeout_s` | `TIMEOUT` |
| 3 | non-empty diff — tracked modifications **or** new files | `NO_DIFF` |
| 4 | `kettlecomb` still imports; visible suite collects | `BROKEN_BUILD` |
| 5 | shipped visible tests still pass | `VISIBLE_REGRESSION` |
| 6 | no **pre-existing** test file was modified or deleted | `TAMPERED_TESTS` |
| 7 | agent added ≥1 new test file under `tests/` | `NO_REGRESSION_TEST` |
| 8 | the agent's added test **fails against the unfixed baseline commit** | `VACUOUS_TEST` |
| 9 | hidden tests, recorded as pass ratio *n/m* | `HIDDEN_TESTS_FAILED n/m` |

- Gate 6 and gate 7 are complementary, not contradictory, and the wording matters:
  gate 6 forbids edits to files that **existed in the baseline commit**, which is
  what a test-rewriting model does; gate 7 requires a **new** file under `tests/`,
  which is what an honest regression test is. Hidden tests cannot be edited at all,
  since they are copied in from outside the workspace only after the run.
- Gate 8 is the highest-value check in the harness. Implementation: `git worktree
  add <tmp> <baseline-sha>` inside the row workspace, copy only the agent's new test
  files into it, run them. The check fires only when they **pass** on unfixed code,
  which means the test asserts nothing about the bug. A collection error or
  assertion failure both count as "not vacuous." Cost is one extra `unittest`
  invocation per row.
- A row short-circuits at the first hard gate that makes later gates
  un-evaluable (`TIMEOUT`, `NO_DIFF`, `BROKEN_BUILD`, `TAMPERED_TESTS`), and the
  skipped gates are recorded as `skipped`, not `pass`.

## Scoring

`composite = round(rubric_total × cap)` where the cap comes from the worst gate
outcome:

| gate outcome | cap |
|---|---|
| all pass | ×1.00 |
| `NO_REGRESSION_TEST` | ×0.85 |
| `VACUOUS_TEST` | ×0.70 |
| partial hidden-test pass (≥1 of m, not all) | ×0.50 |
| all hidden tests fail, or `BROKEN_BUILD` | ×0.25 |
| `TAMPERED_TESTS`, `TIMEOUT`, `NO_DIFF`, `AGENT_ERROR` | ×0.00 |

The cap is where the expectation "some configurations run quickly but produce
inferior or invalid output" gets enforced structurally: a 40-second row with an
elegant-looking symptom patch keeps its full rubric score in its own column and
gets a composite of 0.25× it. Both facts appear in the same table row.

`JUDGE_FAIL` leaves gates and speed intact and sets quality/composite to `N/A`; it
never voids a row.

## Judge contract

**Inputs (blind).** `task.md` + the seed contents of files the diff touches + the
anonymized diff + the agent's closing message. Explicitly **not** provided: gate
results, hidden tests, `meta.toml`, the config label, timing, and token stats. The
rubric states: *"you do not know whether this code passes tests; do not speculate."*
That line is what keeps the rubric score independent of the gates, which in turn is
what makes gate-vs-judge disagreement informative — judge says the fix looks
principled while hidden tests fail is a different diagnosis (plausible reasoning
about the wrong thing) from bad code.

**Dimensions** (100 points, each answerable from the diff alone):

| dimension | pts | question |
|---|---|---|
| root_cause | 30 | did the change land in month arithmetic or one layer removed; a diff that only special-cases a day number or month number is a symptom patch |
| approach | 25 | general last-day clamping + leap-aware lengths + year rollover, rather than coincidence |
| test_quality | 20 | does the added test name a real boundary and assert the specific behavior |
| scope | 15 | drive-by rewrites, public API changes, new dependencies |
| coherence | 10 | does the closing message match what the diff actually does |

**Output**, strict JSON, every attempt's raw text stored:

```json
{"scores": {"root_cause": 18, "approach": 12, "test_quality": 15,
            "scope": 10, "coherence": 7},
 "total": 62,
 "verdict": "symptom_patch",
 "flags": ["overclaims_test_pass", "unrelated_rewrite"],
 "rationale": "…"}
```

`verdict` is a closed enum — `symptom_patch | partial | principled_fix |
no_useful_change` — the discrete column that makes cross-run trends readable.
`flags` are machine-greppable. Malformed or key-incomplete output retries to
`judge.max_attempts`, then `JUDGE_FAIL`.

**Anonymization.** Normalize `diff --git` headers (they embed temp workspace paths
containing the run id), strip model/provider/session/token strings and timestamps
from the closing message, and truncate oversized diffs with an explicit
`[TRUNCATED]` marker the rubric tells the judge to penalize under `scope` rather
than guess past.

**Transport.** A direct chat-completions request to the configured route at
`temperature = 0.0`, reusing the requests plumbing in `benchmark/workloads/base.py`
patterns. Not a pi subprocess: pi has no temperature flag, and a fresh request gives
the same context isolation a fresh process would, cheaper. `judge.samples > 1` issues
N independent requests and takes the per-dimension median before summing.

**Overclaim metric (free).** The agent's closing message is grepped for
test-passing/success claims and compared against gate 9 — a computed column, not a
judge dimension. On quantized local rows this is frequently the most informative cell
in the table.

## Artifacts and report

```
~/.config/local-ai/benchmarks/<run_id>/
  run.toml                          # resolved suite post-expansion, env, pi version, git sha
  rows/01--qwen3.8-27b--off--litellm--p1/   # --pN suffix always present; N = pass number
    agent.jsonl.gz                  # raw stream with harness timestamps added
    agent.session.jsonl
    diff.raw.patch                  # as produced
    diff.patch                      # anonymized — exactly what the judge saw
    gates.json   metrics.json   judge.json
  summary.md
  metrics.jsonl                     # one line per row, for plotting later
```

`summary.md` is four tables:

1. **Quality** — label · model · thinking · route · outcome code · hidden n/m ·
   rubric total · cap · composite · verdict
2. **Speed** — label · ttft_first · ttft_mean · gen_tok_s · e2e_tok_s · in/out/
   reasoning tok · tool_ms · requests · cost
3. **Two-axis** — sorted by composite, with rows that are Pareto-nondominated
   (no other row is both faster and higher-quality) starred: the "fastest config
   at ≥ this quality" reading
4. **Anomalies** — every cap applied, every `VACUOUS_TEST`, every thinking no-op,
   every cold-first-token flag, every overclaim mismatch, and every row where
   `rubric_total ≥ 70` while all hidden tests failed. That last class is a finding
   about the task or the judge, and is listed rather than silently averaged.

`state.extra["benchmarks"]` gains an `agent_last_run` pointer alongside the existing
`last_run`/`last_run_dir` keys (same `extra` dict pattern, no schema migration), so
`modelman benchmark agent show --latest` works like `benchmark show-results --latest`.
Per-run `summary.md` copies into `benchmarks/results/` under the existing dated
archive convention.

## Testing

All of it hermetic — no GPU, no network, no live provider. This is a hard
requirement given the repo's known problem of tests reaching live services.

| module under test | technique |
|---|---|
| `pidriver` | fake agent script emitting a captured JSONL fixture (from the verified probe above); asserts TTFT, `gen_tok_s` vs `e2e_tok_s`, `tool_ms`, usage sums, unknown-event tolerance, timeout kill |
| `gates` | miniature fixture task bundle + pre-authored patches, one per outcome: clean fix, partial pass, vacuous test, tampered tests, broken build, no diff. Asserts exact taxonomy code and skip-after-short-circuit |
| `judge` | injected fake transport: valid, malformed-then-valid, permanently malformed, `samples=3` median. Asserts retry count, cap application, `JUDGE_FAIL`, and that gate data is absent from the prompt |
| `suite` | cartesian expansion, unknown provider, missing `[routes.direct.*]`, `--row` filter, judge-key-missing preflight message |
| `workspace` | baseline commit, diff capture incl. new files, hidden-test copy-in timing, worktree add for the vacuous check |
| `report` | golden `summary.md` snapshot incl. anomaly table entries and `N/A` quality rendering |
| `cli` | argument surface + exit codes, orchestration mocked |

Tests land in `modelman/tests/benchmark/agent/`, one file per module, matching the
existing `tests/benchmark/` convention, and run under modelman's `make check` /
`make test` and therefore `make test-all` and modelman-ci with no CI changes.

Live verification (manual, documented, ~2 min):
`modelman benchmark agent run --suite benchmarks/suites/smoke.toml` — one fast
`:cloud` row, `--skip-judge` optional, no isolation needed since no local model is
loaded.

## Error handling

- **Timeouts kill the process group** (`start_new_session=True` + `killpg`). pi
  spawns child shell processes for its tools; an orphaned child survives into the
  next row and contaminates both its timing and the GPU. This is a correctness
  requirement, not tidiness.
- **Isolation failure for a provider group** marks that group's rows
  `AGENT_ERROR` with the isolation stderr and continues to the next group, matching
  how `benchmark/runner.py` records per-target errors rather than aborting the run.
- `restore_providers()` runs in `finally`, including on `SIGINT`, as the existing
  runner does.
- Workspace cleanup is `finally`-based; `--keep-workspace <dir>` retains it for
  forensics.
- **Unknown JSONL event types are ignored, never fatal,** so a pi version that adds
  events degrades gracefully. A line that fails JSON parsing is counted in
  `unparsed_lines` and skipped.
- Missing `OPENROUTER_API_KEY` for the judge route fails at **preflight** with a
  pointer to `~/Library/LaunchAgents/local.litellm.proxy.plist`, the same source the
  existing benchmark reads, rather than dying mid-run after paying for the agent rows.
- Judge transport HTTP errors retry once with backoff, then `JUDGE_FAIL`; the row's
  gates and speed remain fully reported.
- Row artifacts are written incrementally (metrics as soon as the agent exits, gates
  after gate 9, judge after judging) so a crash in a later phase does not lose an
  earlier row.

## Deferred: the repair round

One feedback turn — run the visible suite after the agent finishes, and on failure
feed the output back for a second attempt — is a designed-for but disabled
capability. It separates "got it right immediately" from "got it right when handed
the error," which are genuinely different capabilities worth a column each, but it
roughly doubles cost for failing rows and couples the runner to a conversation loop.
The seam is that `pidriver.run_row()` takes an optional list of follow-up turns and
row artifacts are numbered by turn, so enabling it later adds a suite field
(`repair_rounds = 1`) and no restructure. v1 ships `repair_rounds = 0` with the
parameter present and rejected-if-nonzero at preflight, so the interface exists
without a dead code path.

## File map

**New**
- `modelman/src/modelman/benchmark/agent/{__init__,suite,task,workspace,pidriver,gates,judge,report,runner,cli}.py`
- `benchmarks/tasks/day31-drift/` — `task.md`, `visible/…`, `hidden/…`,
  `gates.toml`, `rubric.md`, `meta.toml`
- `benchmarks/suites/q4-agent-sweep.toml`, `benchmarks/suites/smoke.toml`
- `modelman/tests/benchmark/agent/` — one test file per module, plus
  `fixtures/fake_agent.py` and `fixtures/tasks/mini-drift/`
- `docs/guides/09-agent-benchmarks.md`

**Edited**
- `modelman/src/modelman/benchmark/cli.py` — mount `agent_app` on `benchmark_app`
- `modelman/src/modelman/state.py` usage sites — `extra["benchmarks"]["agent_last_run"]`
- `benchmarks/README.md` — new section pointing at agent benchmarks and guide 09
- `docs/guides/05-benchmarks.md` — cross-link
- `CLAUDE.md` (root) — Commands (`modelman benchmark agent run`), Architecture
  (agent bench module), and the guide-staleness note updated to include 09's
  *absence* of exposure snapshots
- `modelman/CLAUDE.md` — module map entries for `benchmark/agent/`
- `.gitignore` — bench workspace temp dirs

Guide 09 is numbered fresh on purpose: guides 00/02/04/05/08 embed live
`litellm_exposed` snapshots and adding a sixth snapshot-bearing guide would multiply
the drift surface documented in CLAUDE.md. Guide 09 contains no exposure snapshots.

## Phasing

Ordered so each phase ends runnable and testable, and the risky deliverable (the
task bundle) is validated before the expensive matrix exists.

1. **Task bundle + workspace**: `task.py`, `workspace.py`, the `day31-drift`
   bundle, and a miniature fixture bundle for tests. Verified by hand: `git diff`
   and the vacuous-check mechanics against a manually authored correct patch and a
   manually authored symptom patch.
2. **pi driver + metrics**: `pidriver.py` against the captured JSONL fixture, then
   one live cloud row. This is where `gen_tok_s`/`e2e_tok_s` semantics get frozen.
3. **Gates**: `gates.py` with the pre-authored patch fixtures, all nine codes
   reachable in tests.
4. **Suite + runner + isolation loop**: `suite.py`, `runner.py`, `cli.py run`,
   `--dry-run`. First end-to-end multi-row local run happens here.
5. **Judge**: `judge.py` with injected transport, then one live judge call, then
   caps wired into composite.
6. **Report + artifacts + docs**: `report.py`, `agent show`, guide 09,
   `benchmarks/README.md`, both CLAUDE.md files, `check-links`.

Phase 1–2 are the only phases with genuine unknown risk; the rest is mechanical
once the metrics semantics are settled.

## Risks

1. **Task authoring is the hard part.** If the day-31 defect turns out to be too
   easy, every strong config scores 6/6 and the task discriminates nothing; too hard
   and every local row is a timeout. Mitigation: `meta.toml` records the tier
   expectations, the smoke suite exercises the pipeline cheaply, and the first live
   run is explicitly a calibration run whose purpose is to adjust the hidden test set.
2. **Judge variance.** A single sample on a stochastic model can move a row's
   composite. Mitigation: `temperature = 0`, closed `verdict` enum as the stable
   signal, `judge.samples` available, and all raw judge text retained.
3. **Contamination.** If `kettlecomb` resembles a known repo, strong models recall a
   fix. Mitigation: invented domain and names, no public release of the bundle,
   hidden tests excluded from the agent workspace, and a task-swap expectation
   documented in guide 09.
4. **Local-model run time.** A 27B in a six-round agentic task can exceed
   `agent_timeout_s`, producing `TIMEOUT` rows that look like quality failures.
   Mitigation: `TIMEOUT` caps at 0 but is reported as a distinct outcome class, and
   the guide states that timeout is a configuration input to tune per backend.
5. **Judge cost.** Judging is cloud API spend on every row. Mitigation: judging
   happens after all local work, `--skip-judge` exists, and `--row` allows judging a
   subset of an existing run's artifacts without re-running agents.
