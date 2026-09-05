# Speed + Quality Agentic Coding Benchmark Implementation Plan

> **For agentic workers:** Execute this plan **inline in one session**, task by task, using superpowers:executing-plans. Subagent-driven development was considered and rejected for this plan: the 24 tasks are tightly sequential rather than independent — Tasks 9–10 edit Task 8's file, 18 and 21 edit Task 14's, 20–21 edit Task 19's — and the code is already written out per step, so per-task dispatch buys review overhead without buying isolation. Steps use checkbox (`- [ ]`) syntax for tracking. **Global Constraints and the cross-phase contracts below apply to every phase file; read this index before any phase file.**

**Goal:** Add `modelman benchmark agent` — a harness that runs a real coding task through the `pi` agent across a matrix of model/thinking/route configurations, grades the result on deterministic gates plus a blind LLM rubric, and reports speed and quality as separable, comparable columns.

**Architecture:** A new `modelman/src/modelman/benchmark/agent/` sub-package, mounted as a Typer sub-app on the existing `benchmark_app` (`modelman benchmark agent ...`). It reuses the existing benchmark subsystem's isolation helpers (`modelman.benchmark.isolation`), results directory convention, and state-pointer pattern, but owns its own suite/task/row model rather than extending the single-turn `workloads/` machinery, since an agent row is a multi-request agentic run, not a single completion.

**Tech Stack:** Python 3.13, `uv`, Typer, `requests`, stdlib `tomllib`/`subprocess`/`unittest`/`gzip`. No new third-party dependencies — the design's judge transport reuses the `requests` plumbing already in `benchmark/workloads/base.py`.

**Spec:** `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md` — this plan implements it section by section; executors should read both. Anything this plan doesn't spell out in full is settled by the spec.

**Status:** items #1–#24 complete, including the live smoke run (2026-09-05): one
`ollama/glm-5.3-flash:cloud` row on `day31-drift`, judged by `anthropic/claude-opus-5`
through OpenRouter — 5/6 hidden, rubric 87, `VACUOUS_TEST`, cap ×0.5, composite 44, and
`agent judge --latest` re-scored the same artifacts to 95/48 without re-running anything.
That run is what found corrections 32–38; every one of them was invisible to the 134
hermetic tests that passed beforehand. Phase 2's listings were rebuilt
from the shipped code; Phase 3's original listings stand, with a corrections section and
the shipped files at the end of that file. From Phase 4 onward the numbered corrections
below are the record: where a defect was *in* a listing the phase file was rebuilt, and
where it was in the design the shipped code is authoritative over the plan's draft —
reading a phase file's listing for Phase 4–6 is reading a draft, not the API.

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
- **Annotate every empty collection literal.** `make check` also runs `mypy src/`, which rejects `bundles = []` with `var-annotated` even when the next line appends a value — write `bundles: list[TaskBundle] = []`. Any bare `x = []` / `x = {}` in a `src/` module needs the annotation; the plan's listings were not all written with that in mind.
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

**Found while executing Phase 1 (fixed in the phase files above):**

9. **pytest collected the fixture task bundles as tests.** Task 1's `mini-drift` ships `visible/tests/test_pkg.py` and `hidden/test_hidden.py`, which import `pkg` — a module that exists only inside a seeded temp workspace — so every directory-wide run (`pytest tests/benchmark/agent/`, `make test`, CI) aborted at *collection*, not on an assertion. Task 2 now has a Step 0 creating `tests/benchmark/agent/conftest.py` with `collect_ignore = ["fixtures"]`. The plan missed this because every one of its own verification commands named a single test file; the first command wide enough to trip it was Task 7 Step 5's `pytest tests/benchmark/agent/`.
10. **`mypy` rejected `bundles = []`** in Task 1's `list_task_bundles` (`var-annotated`), so `make check` failed on the plan's own code — now annotated, and a Global Constraint.
11. **Task 1's `mini-drift/meta.toml` claimed `[tiers] frontier = "6/6"`**, carried over from the 6-hidden-test `day31-drift` bundle; the fixture has 2 hidden tests, so it is `2/2`.

**Found while executing Phase 2 (full write-up and rebuilt listings in [Phase 2](2026-09-04-agent-coding-benchmark-p2-pi-driver-metrics.md)):**

12. Task 5's tests 6–8 resolved a `route=litellm` row against a deliberately missing `models.json`, which raises `BenchmarkError` — 3 of 7 tests could never pass. Fixed with a `_live_litellm_path()` fixture.
13. Task 6's fake agent wrote no session file and its helper passed `--session-dir` only sometimes, so gate 1 had nothing to find. The fake writes one now and the helper always passes the flag.
14. **`_read_stdout` used `proc` as a free variable** at module level — a `NameError` on the first line of the first real run, which empties `events` and makes every row report zero tokens. `proc` is a parameter now.
15. **The poll loop exited on "assistant `message_end` seen AND process gone"**, so a run whose model never replied burned its whole timeout (a 40-minute row that died at second three cost 40 minutes) and risked losing the `agent_end` tail. It exits when the child exits and lets the reader drain to EOF.
16. **`compute_metrics` timed off `event["ts_ms"]`, which pi never emits** — `turn_start` has no timestamp and an assistant `message_end` repeats its `message_start`'s `message.timestamp`. The plan's index-proportional fallback produced a constant TTFT and, for a turn whose start and end share an index bucket, `gen_seconds = 0.0`: a silent zero on the metric the sweep exists to measure. `run_pi_process` now stamps `_ts` (seconds since run start) as the reader reads each line, and the metrics are documented as arrival-based rather than provider-side.

**Found while executing Phase 3 (full write-up plus the shipped `gates.py` in [Phase 3](2026-09-04-agent-coding-benchmark-p3-gates.md)):**

17. Tasks 8–11 were written against the pre-Phase-2 `PiRunResult`; `evaluate()` now takes pi's event list as an `events=` keyword.
18. Gate 1's condition also failed a timed-out run, so `TIMEOUT` was unreachable and the task's own test asserted a code the code could not emit.
19. `mini-drift/gates.toml` had no `[build]` section, so gates 4–6 `KeyError`ed on a bundle `load_task` accepted.
20. `_run_discover` used `top_level_dir="."`, which cannot import a `tests/` dir with no `__init__.py` — every healthy run would have read `BROKEN_BUILD` and capped at 0.25.
21. Gate 3 used `git diff --quiet HEAD`, ignoring untracked files, so an agent whose only change was a new test file scored `NO_DIFF` and zero.
22. `ALL_HIDDEN_FAILING` (×0.25) was missing from `CAP_TABLE` despite being the spec's own row.
23. `PARTIAL_HIDDEN_PASS` (×0.50) was missing too — and Task 11 asserts the spec's worked example that needs it.
24. Task 10's minimum-across-conditions test asserted a hidden ratio the fixture cannot produce; rewritten to fire two caps together.
25. `add()` stamped a failure code onto gates that passed, so a `Pass` row could read `AGENT_ERROR`.

**Found while executing Phase 4:**

26. Task 14's runner listing was written against the pre-Phase-2 driver API (`RowMetrics`, `run_pi_process(cmd, cwd, env, timeout_s)`, `compute_metrics(run_result)`) and could not import. It was rebuilt against what shipped: events come back alongside the result, metrics need start/end walls plus a log writer, and `evaluate` takes `events=` as a keyword.
27. **`run_pi_process` had no `env` parameter at all**, so `PI_CODING_AGENT_DIR` could never reach the child. Every row would have run against the user's own `~/.pi/agent` provider list, `write_pi_config` was dead code, and the per-run `models.json` the plan built in Task 5 was never read. It takes `env` now, with a test that reads the value back out of the session file the fake agent writes.

**Found while executing Phase 5:**

28. Task 16's `build_prompt` test asserted the phrase "do not speculate" while passing `rubric_md="score this"` — that sentence lives in the task rubric, not the prompt template, so the assertion could never hold. The stub now carries the real rubric's anti-speculation line, which is also what the assertion was meant to prove: the rubric reaches the judge verbatim.
29. Task 16's test file imported `JudgeScore`/`JudgeOutcome`, which only Task 17's tests use — `F401`, so `make check` failed at Task 16.
30. Task 17's appended listings put `import requests`, the judge-module alias and `LiteLLMJudgeTransport` mid-file, which is `E402` (constraint 6's rule, still biting).
31. Task 18's `_closing_message` was still written against the pre-Phase-2 `{"ts","event"}` wrapper and a `PiRunResult.events` field that no longer exists; and because judging is on by default, Task 14's three phase-1-only runner tests had to opt out with `skip_judge=True` — a standing consequence for any future runner test.

**Found while executing Phase 6 — all four of these were invisible to 128 hermetic tests and appeared only in the live smoke run:**

32. Suites name their task repo-relative (`task = "benchmarks/tasks/day31-drift"`), but the CLI is run from `modelman/` as the guide, CI and `uv run` require — so *every* real suite died at "task bundle not found" while every test passed, because all test suites pass absolute `tmp_path` task paths. `load_suite` now falls back to the suite file's own ancestors (an absolute or cwd-resolvable path still wins; an unresolvable path is returned untouched so `load_task` raises its own error).
33. `run_suite` let `isolation.restore_providers()` raise between the rows and the persistence phase, so a completed sweep — raw streams, gates, metrics, judge verdicts — vanished with an exception after minutes of real work. This host triggered it immediately: the `local.llamacpp.server` LaunchAgent points at a GGUF that no longer exists (`~/.cache/huggingface/hub` holds no models; 90k failed-load retries in `~/.llamacpp.err.log`; its log has no "server listening" line ever), so `llm-restore-providers` can never succeed here. Artifacts, judging and the summary are now written before the failure surfaces, and the error names the directory that survived. (The single-turn `modelman benchmark` runner has the same shape — `finally: restore_providers()` before `write_results` — and was left alone as out of scope.)
34. A row's error text lived only in `RowRunResult.error`: the live run's summary.md read `ISOLATION_ERROR` in every quality cell with no reason on disk or console, which is this harness's own "the one misreading it cannot allow" failure mode in another guise — an operator cannot distinguish a broken backend from a model that failed the task. `summary.md` gained an Errors table and `agent run` echoes each errored row's reason to stderr.
35. `run_suite` called the isolation helper for *every* provider group, so a row on a non-local provider failed `bin/llm-isolate-provider` (it knows only the four local backends) and was recorded `ISOLATION_ERROR` before a single request went out — which blocked the cloud baseline the two-axis table exists to compare local rows against. Guarded by `ISOLATABLE_PROVIDERS` = `DEFAULT_PROVIDER_IDS` ∪ `{omlx-6bit}` (the constant omits the 6-bit override, which is the variant that most needs isolating), and `restore_providers()` now runs only if something was isolated.
36. `bin/llm-isolate-provider`'s stdout is its contract — `isolation.py` parses it as a single JSON object — but `omlx stop` shells out to brew, which narrates ("Stopping `omlx`…") on **stdout**. Isolating any provider while oMLX happened to be running produced prose-then-JSON and "isolation helper returned invalid JSON"; because the narration only occurs when the service is up, and every isolate stops the others, it failed *every other* run, including a re-run that had just succeeded. It is a pre-existing bug in a monorepo-wide helper: `modelman benchmark`'s single-turn runs hit it identically. Service narration now goes through `narrate_on_stderr`.
37. `[judge].route` was parsed, validated as `litellm`-only, and then never read — `_build_judge_transport` always built a gateway transport. The local gateway serves 37 models and none of them are claude, so both suites named `openrouter/anthropic/claude-opus-4` (no such deployment) and every row could only `JUDGE_FAIL`, with `judge.json` recording the status and no reason because `judge_row` discarded the exception. Fixed as one defect with three parts: route selects the endpoint (`openrouter` goes direct and drops the LiteLLM-style prefix OpenRouter does not know), HTTP failures carry the response body that names the missing deployment, `JudgeOutcome.error` persists to `judge.json`, and the sweep's judge is `anthropic/claude-opus-5`, verified against `openrouter.ai/api/v1/models` on 2026-09-05.

**Recorded as a limitation rather than fixed (found while executing Phase 6):**

38. **`[judge].thinking` is parsed, validated and never applied.** `LiteLLMJudgeTransport` sends `model`, `messages` and `temperature` only, so the judge runs at whatever its model defaults to. Applying it means a reasoning-effort field whose shape differs between the gateway and OpenRouter direct — inventing that unverified was worse than recording it. A suite that needs a specific judge reasoning level has to say so in the prompt until this lands.

**Found by CI after the plan's own verification steps all passed locally:**

39. `workspace._status_since_baseline` stages everything with `git add -A`, so the `__pycache__/test_day31.cpython-313.pyc` that the visible-test run and the agent's own `python3` invocations leave in the workspace counted as an added file — and gate 7 selects new regression tests by a `test_` prefix, so gate 8 tried to `read_text` bytecode and died on `UnicodeDecodeError`. Ten CI failures, none local: the dev machine's `core.excludesFile` is `~/.gitignore`, the runner's is nothing, which is the entire difference between a suite that looks hermetic and one that is. Bytecode is now excluded at the source in `.git/info/exclude` (repo-local, so the agent sees exactly the tree the bundle describes) and filtered from the entries as defense in depth. Same run exposed that `test_run_records_the_pointer_even_when_restore_failed` never patched `load_registry` and so depended on this machine owning `~/.config/local-ai/registry.toml`. The lesson for anyone executing a later plan here: hermeticity has to be tested with `GIT_CONFIG_GLOBAL=/dev/null XDG_CONFIG_HOME=/tmp/empty`, not just with the network mocked.
