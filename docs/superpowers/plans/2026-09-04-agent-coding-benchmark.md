# Speed + Quality Agentic Coding Benchmark Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

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

**Placeholder scan:** none — the one draft that briefly introduced sloppy copy-paste code (Task 6) and a stub-attribute placeholder (Task 8) were both caught and rewritten during drafting; the final task bodies contain complete, real code throughout, with `<!-- UNVERIFIED -->` markers used only for genuinely-not-yet-run live command output, matching this repo's own guide-writing convention (guide 05).

**Type/name consistency:** verified `RowConfig`, `DirectRouteConfig`, `PiTarget`, `PiRunResult`, `RowMetrics`, `TaskBundle`, `Workspace`, `GateResult`, `GatesReport`, `JudgeScore`, `JudgeOutcome`, `RowRunResult`, and `RowReport` are constructed with the same field names everywhere they cross a task boundary; `Workspace.seed_hidden` copying into `tests/` (not the workspace root) was corrected early (Task 2) once Task 3's real hidden-test module-name resolution (`tests.test_day31`) made the mismatch visible, and Task 1's `mini-drift` hidden fixture was given two independent test methods specifically so Task 10 could exercise a genuine partial hidden-pass ratio.
