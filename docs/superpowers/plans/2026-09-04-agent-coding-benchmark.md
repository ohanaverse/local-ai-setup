# Speed + Quality Agentic Coding Benchmark Implementation Plan

> **For agentic workers:** Execute this plan **inline in one session**, task by task, using superpowers:executing-plans. Subagent-driven development was considered and rejected for this plan: the 24 tasks are tightly sequential rather than independent — Tasks 9–10 edit Task 8's file, 18 and 21 edit Task 14's, 20–21 edit Task 19's — and the code is already written out per step, so per-task dispatch buys review overhead without buying isolation. Steps use checkbox (`- [ ]`) syntax for tracking. **Global Constraints and the cross-phase contracts below apply to every phase file; read this index before any phase file.**

**Goal:** Add `modelman benchmark agent` — a harness that runs a real coding task through the `pi` agent across a matrix of model/thinking/route configurations, grades the result on deterministic gates plus a blind LLM rubric, and reports speed and quality as separable, comparable columns.

**Architecture:** A new `modelman/src/modelman/benchmark/agent/` sub-package, mounted as a Typer sub-app on the existing `benchmark_app` (`modelman benchmark agent ...`). It reuses the existing benchmark subsystem's isolation helpers (`modelman.benchmark.isolation`), results directory convention, and state-pointer pattern, but owns its own suite/task/row model rather than extending the single-turn `workloads/` machinery, since an agent row is a multi-request agentic run, not a single completion.

**Tech Stack:** Python 3.13, `uv`, Typer, `requests`, stdlib `tomllib`/`subprocess`/`unittest`/`gzip`. No new third-party dependencies — the design's judge transport reuses the `requests` plumbing already in `benchmark/workloads/base.py`.

**Spec:** `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md` — this plan implements it section by section; executors should read both. Anything this plan doesn't spell out in full is settled by the spec.

## Global Constraints

- Python `==3.13.*`, everything runs via `uv run` (`modelman/CLAUDE.md`) — never invoke `python`/`pytest` directly.
- No new runtime dependencies. Judge HTTP calls use `requests` (already a dependency); TOML parsing uses stdlib `tomllib`/`tomli_w` (already used by `registry.py`/`state.py`).
- `pyproject.toml`'s `[tool.hatch.build.targets.wheel] packages = ["src/modelman"]` already covers nested sub-packages (the existing `benchmark/workloads/` sub-package needed no separate entry) — **do not add a `benchmark.agent` entry**, it would be redundant.
- All new tests are hermetic: no GPU, no network, no live provider, no live `ollama`/LiteLLM process. Every subprocess call (`git`, `pi`, `python -m unittest`) in tests either runs against a fixture the test controls or is mocked.
- Every test file gets a module or per-test comment stating what behavior is covered and why it matters (this repo's convention — see `modelman/tests/benchmark/test_runner.py` for the exact style to match).
- Follow `modelman/tests/benchmark/` conventions: one test file per module, `tmp_path`-based fixtures, env-var path redirection (`MODELMAN_REGISTRY`, `MODELMAN_STATE`) rather than mocking the registry/state modules themselves.
- Run focused tests per task (`uv run pytest tests/benchmark/agent/test_X.py -v`) — run `make check`/`make test` in full only on the final task of each phase.
- Commit after every task, referencing this plan (e.g. `feat(agent-bench): add task bundle loader - completes plan item #1`).
- **Keep every import at the top of its file.** Each task below says "append to `test_X.py`", and appending an `import` after module code is an `E402` error — `make check` runs `ruff check src/ tests/` (with `E` selected) in CI, so a mid-file import fails the build. Merge each task's new imports into the existing top-of-file block, and delete any import a later task makes unused (`F401`).
- **Task numbers in cross-references are the ones in this index's phase list**, not the ones the draft used: `pidriver`/`suite`/`gates` prose written before the renumbering said "Task 16" for the runner and "Task 22" for the report. The phase files have been corrected; if you find a stale one, fix it in place.

## Cross-phase contracts

Two things every phase depends on and no phase should restate from memory.

### 1. The pi `--mode json` event shape

Re-captured from a live `pi --mode json` run during plan review (the spec's "Verified against the live setup" section carries the full capture). The four facts that are **not** guessable and that every parser keys on:

| what | real shape | the shape a guess produces |
|---|---|---|
| streaming delta | `message_update.assistantMessageEvent` = `{type:"text_delta", contentIndex, delta:"<str>"}` | `message_update.delta = {type, text}` |
| authoritative usage | `message_end.message.usage` = `{input, output, cacheRead, cacheWrite, reasoning, totalTokens, cost:{total}}` | `message_end.usage`, snake_case `cache_read` |
| tool events | `tool_execution_start` = `{toolCallId, toolName, args}` | `{name}` |
| who is speaking | `message_start`/`message_end` also fire for the **user** message | assistant-only |

The last row is the expensive one: count every `message_start` and `requests` is off by one per turn; accept every `message_end` and gate 1 (AGENT_ERROR) passes for an agent that died before it ever replied. A fixture that guesses the nesting makes every `pidriver` test green while real rows report `in_tok=0, out_tok=0, ttft=None` — which is exactly how this plan's first draft was wrong, and why Task 6's fake agent is written from the capture.

### 2. The type/name table

Every name below is produced once and consumed by name elsewhere. Renaming one in a later task means editing the producing task's `**Interfaces:**` bullet first.

| type | produced by | consumed by |
|---|---|---|
| `TaskBundle` (`task_id, path, task_md, rubric_md, gates_config, meta, visible_dir, hidden_dir`) | Task 1 `task.py` | Tasks 2, 8–10, 12–14, 16, 21 |
| `Workspace` (`root, baseline_sha`) + `create_workspace`/`destroy_workspace` | Task 2 `workspace.py` | Tasks 8–11, 14 |
| `RowConfig`, `DirectRouteConfig`, `PiTarget`, `PiRunResult`, `RowMetrics` | Tasks 5–7 `pidriver.py` | Tasks 8–10, 12, 14, 18, 20 |
| `GateResult`, `GatesReport` (`hidden_pass, hidden_total, hidden_evaluated, cap`) | Tasks 8–10 `gates.py` | Tasks 14, 19, 20, 21 |
| `JudgeConfig`, `Suite`, `load_suite`, `preflight` | Tasks 12–13 `suite.py` | Tasks 14, 15, 18, 21 |
| `JudgeScore`, `JudgeOutcome`, `JudgeTransport` | Tasks 16–17 `judge.py` | Tasks 18–21 |
| `RowRunResult` (`row, pass_number, row_dir, gates, metrics, diff_raw, events, seed_contents, closing_message, judge, composite, error`) | Tasks 14, 18, 21 `runner.py` | Tasks 15, 21, 22 |
| `RowReport`, `write_row_artifacts`, `write_judge_json`, `render_summary`, `write_metrics_jsonl`, `write_run_toml` | Tasks 19–21 `report.py` | Task 21 `runner.py` |

Two rules those names encode, both corrected during review: `RowMetrics.requests` counts **assistant** messages only, and `cold_first_token` compares the first TTFT to the median of **subsequent** requests (against a mean that includes the first sample, a two-request row would need first ≥ 5× second before the flag could ever fire).

## Phase files (execute in this order)

Each phase file holds its tasks verbatim from the original single-file plan. Global Constraints above apply to all of them. A phase ends on a committed, green state — that is where `make check` / `make test` run.

- [Phase 1: Task bundle + workspace](2026-09-04-agent-coding-benchmark-p1-task-bundle.md)

  - Task 1: Task bundle loader + mini-drift test fixture
  - Task 2: Workspace — seed, diff, hidden copy-in, baseline worktree
  - Task 3: The `day31-drift` task bundle
  - Task 4: Hand-verify the bundle — correct fix, symptom patch, vacuous check

- [Phase 2: pi driver + metrics](2026-09-04-agent-coding-benchmark-p2-pi-driver-metrics.md)

  - Task 5: Route resolution + `models.json` generation
  - Task 6: Fake agent fixture + process streaming with hard timeout
  - Task 7: Metrics — TTFT, gen/e2e throughput, tool time, anomaly flags

- [Phase 3: Gates](2026-09-04-agent-coding-benchmark-p3-gates.md)

  - Task 8: Gate skeleton + gates 1–4 (AGENT_ERROR, TIMEOUT, NO_DIFF, BROKEN_BUILD)
  - Task 9: Gates 5–7 (VISIBLE_REGRESSION, TAMPERED_TESTS, NO_REGRESSION_TEST)
  - Task 10: Gate 8 (VACUOUS_TEST) + gate 9 (hidden pass ratio) + cap wiring
  - Task 11: Full taxonomy against the real `day31-drift` bundle

- [Phase 4: Suite + runner + isolation loop](2026-09-04-agent-coding-benchmark-p4-suite-runner.md)

  - Task 12: Suite parsing + cartesian row expansion
  - Task 13: Preflight validation
  - Task 14: Runner — phase 0 (preflight) + phase 1 (execute) + isolation loop
  - Task 15: CLI (`run`/`list-tasks`/`list-suites`/`--dry-run`) + smoke suite

- [Phase 5: Judge](2026-09-04-agent-coding-benchmark-p5-judge.md)

  - Task 16: Judge core — anonymization, prompt, contract validation, retries
  - Task 17: LiteLLM HTTP transport with one retry on transport failure
  - Task 18: Wire judging into the runner (phase 2) + `--skip-judge`

- [Phase 6: Report + artifacts + docs](2026-09-04-agent-coding-benchmark-p6-report-docs-verification.md)

  - Task 19: Per-row artifact writes + `run.toml` with key masking
  - Task 20: Summary report — four tables + Pareto stars + `metrics.jsonl`
  - Task 21: Wire reporting into the runner + `agent show` + `agent judge`
  - Task 22: `q4-agent-sweep.toml` + `.gitignore`
  - Task 23: Guide 09 + README/CLAUDE.md cross-links
  - Task 24: Full verification — `make test-all`, lint, and a live smoke run

Plan self-review (below) is global; the phase files carry no review section.

## Plan self-review

**Spec coverage:** every spec section maps to a task — Problem/Goals/Non-goals informed scope throughout; Resolved decisions and Verified-against-live-setup facts are embedded as code comments and design choices (Tasks 5–7); Architecture's module table is Tasks 1–21 (with the noted `pidriver`↔`suite` dependency-direction flip for phase-testability); the CLI surface is Task 15/18/21; the run pipeline's three phases are Tasks 14 (0–1), 18 (2), and the combine step is folded into Task 21's `run_suite` tail rather than a separate "phase 3" function, since it's a few lines of glue, not independent logic; the `day31-drift` task is Tasks 3–4 and 11; the suite format is Tasks 12–13; metrics are Task 7; gates and failure taxonomy are Tasks 8–11; scoring is Task 10's cap logic plus the review-added minimum-across-triggered-conditions rule; the judge contract is Tasks 16–17; artifacts and report are Tasks 19–21; testing requirements are met per-module as each was built; error handling (timeouts/killpg, isolation-failure containment, `finally`-based cleanup, unknown-event tolerance, missing-key preflight, judge retry, incremental artifact writes) is threaded through Tasks 6, 14, 17, 19–21; the deferred repair round is rejected-at-load in Task 12; the file map is fully covered across Tasks 1–23; phasing matches this plan's own six phases.

**Known, deliberate scope reduction:** `agent judge` does not regenerate `summary.md`/`metrics.jsonl` (Task 21's scope note) — flagged in guide 09 as a follow-up rather than silently omitted.

**Placeholder scan:** no placeholders remain — but note that this section's first edition claimed Task 6's fixture had already been "caught and rewritten during drafting", and it had not: the fixture still encoded a guessed event nesting, which is defect 1 in the "Correction pass" at the end of this section. A self-review claim is not evidence; the live probe is.

**Type/name consistency:** verified `RowConfig`, `DirectRouteConfig`, `PiTarget`, `PiRunResult`, `RowMetrics`, `TaskBundle`, `Workspace`, `GateResult`, `GatesReport`, `JudgeScore`, `JudgeOutcome`, `RowRunResult`, and `RowReport` are constructed with the same field names everywhere they cross a task boundary; `Workspace.seed_hidden` copying into `tests/` (not the workspace root) was corrected early (Task 2) once Task 3's real hidden-test module-name resolution (`tests.test_day31`) made the mismatch visible, and Task 1's `mini-drift` hidden fixture was given two independent test methods specifically so Task 10 could exercise a genuine partial hidden-pass ratio.

**Correction pass (2026-09-05, after the split, before execution):** eight defects found by re-verifying the plan's assumptions against the repo, `pi`'s own docs/source, and one live `pi --mode json` probe. They are fixed in the phase files, not just recorded here, and the index's contract section above exists so they cannot silently reappear:

1. **The pi event shape in Tasks 6–7 was wrong** (nested `assistantMessageEvent.delta`, `message_end.message.usage`, camelCase `cacheRead`, `toolName`, user-message pair). As drafted, every hermetic test passed while real rows scored zero tokens — fixed in Tasks 6, 7, 18.
2. **Task 7's `cold_first_token` test could not pass** (3000 ≥ 3×1525 is false) — the comparator now excludes the first sample, per the spec.
3. **Task 10's vacuous-test cap was wrong** — fixing `pkg` in that fixture isolates the ×0.70 cap; a second test now pins the minimum rule where ×0.70 and ×0.25 both fire.
4. **Task 14's mocked `run_pi_process` never wrote a session file**, so gate 1 failed every row and the `NO_DIFF` assertion was unreachable — the test double now recreates that side effect, plus a test that pins gate 1 requiring it.
5. **Tasks 4 and 11 derived the repo root with `parents[3]`**, which is `modelman/` — now `parents[4]`.
6. **Appended imports would have failed CI** (`E402`, and one unused `ModelState` import as `F401`) — import placement is now a Global Constraint.
7. **Task 22's sweep named `omlx/Ornith-1.5-35B-A3B-MLX-6bit`, which is not a registry model id**, so `load_suite` rejected the file — the sweep now lists only ids that exist, with the 6-bit and `direct_model` cases as documented commented rows. Reviewing that row also surfaced a live-only hazard the plan had no answer for: a `route = "direct"` omlx row would send the registry's org-prefixed `model_name` to a server that knows the basename, so `RowConfig` gained an optional `direct_model` override (Tasks 5, 12).
8. **Task 21's `rejudge_run` truncated each row's `agent.jsonl.gz`** by re-writing artifacts with `events=[]` — it now uses a `write_judge_json` that touches only `judge.json`, with a test asserting the stream survives.
