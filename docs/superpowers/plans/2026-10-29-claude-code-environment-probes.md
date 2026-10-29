# Claude Code Environment Probes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `probe_runner.py` that runs fixed `claude -p` cases on any machine, writes an aggregate-only JSON, and compares two such files.

**Architecture:** One new stdlib script beside `transcript_speed.py`. Each run is one `claude -p --output-format json` process in a fixed empty directory; its transcript is found by `session_id` and read with `transcript_speed`'s call rows, so probe and organic numbers mean the same thing. Aggregation and comparison follow the shape of `transcript_speed --json` / `--compare`.

**Tech Stack:** Python >= 3.11 stdlib, pytest, ruff, uv (all under `agent-utilities/claude-utilities/`).

**Spec:** `docs/superpowers/specs/2026-10-29-claude-code-environment-probes-design.md`

**Prerequisite:** PR #499 (extends `transcript_speed.calls_from_transcript` / `derive`). Merge it first, or branch from `worktree-transcript-speed-context-report`. Work in a new worktree from that base.

## Global Constraints

- Stdlib only at runtime; Python >= 3.11; `make check test` in `agent-utilities/claude-utilities` must pass (ruff lint + format + pytest).
- Tests never call the real model, `claude`, or touch the real `~/.claude`. The only real calls are the two named in Task 1 and Task 8.
- Output JSON is aggregate-only: no session ids, paths, hostnames, usernames or prompt text. Transcripts are never copied or committed.
- Default model `claude-sonnet-5-5` (always passed as `--model`); default `--runs` 10; cases `tiny`, `count`, `fill20k`, `fill80k` (`tool` deferred); variants `default` and `lean`.
- `lean` flags exactly: `--setting-sources project --strict-mcp-config --disable-slash-commands`. Never `--bare`. Never `--no-session-persistence`.
- Prompts go on stdin, with a per-run nonce `(ref N)` as the last thing in the prompt; every run uses the same fixed probe directory `~/.cache/claude-utilities/probe-cwd` (empty, not a git repo).
- A warm run (index >= 1) with `cache_hit` < 0.5 is excluded from warm percentiles and counted. Cache lifetime assumed 300 s; exceeding it warns on stderr and continues.
- Estimates are labelled as estimates in README text, as elsewhere in `agent-utilities/`.

## Review Focus

- A repeated prompt answered by the gateway's response cache: Task 3 pins nonce-last and identical prefix across nonces.
- `claude -p` exits non-zero, prints non-JSON, hangs, or reports `is_error`: the run is discarded and counted, never raises (Task 4).
- Transcript missing or slow to appear: bounded wait, then discard with reason (Task 4).
- A (case, variant) with zero usable warm runs: aggregate gives `None` percentiles, compare prints `n/a`, nothing divides by zero (Tasks 6, 7).
- Files with different variant or case sets, or different run counts or Claude Code versions: compare uses the intersection and warns; different pinned models refuse (Task 7).
- `claude` not on `PATH`, or the probe directory missing: clear one-line error; directory is created (Task 8).

---

### Task 1: Confirm the `claude -p` JSON shape (gating)

**Files:**
- Create: `agent-utilities/claude-utilities/tests/fixtures/claude_p_tiny.json`
- Test: `agent-utilities/claude-utilities/tests/test_probe_runner.py`

**Interfaces:**
- Produces: the fixture other tasks' fake `claude` echoes; a verified answer on whether `duration_ms - duration_api_ms` can serve as `client_overhead_s`.

- [ ] **Step 1: One real run, with the user's agreement (a few cents).** `mkdir -p ~/.cache/claude-utilities/probe-cwd`, then from that directory: `echo "Reply with the single word OK. (ref 1)" | claude -p --output-format json --model claude-sonnet-5-5 > <scratch>/tiny.json`.
- [ ] **Step 2: Save it as the fixture** with `session_id` replaced by `00000000-0000-0000-0000-000000000000` and any path, user or host value removed.
- [ ] **Step 3: Write `test_tiny_fixture_has_expected_fields`** asserting the keys `session_id`, `is_error`, `duration_ms`, `duration_api_ms`, `result` exist and `duration_ms >= duration_api_ms >= 0`.
- [ ] **Step 4: Run** `uv run pytest tests/test_probe_runner.py -q`. Expected: PASS. If a key is missing or the difference is negative, **stop and report to the user**: drop `client_overhead_s` from Tasks 4–7 and the spec's reading table (spec, Open items 1), then continue.
- [ ] **Step 5: Commit** `test: record claude -p json output shape`.

### Task 2: Keep probe sessions out of organic stats

**Files:**
- Modify: `agent-utilities/claude-utilities/transcript_speed.py` (file collection in `main()`; new constant and functions)
- Test: `agent-utilities/claude-utilities/tests/test_transcript_speed.py`

**Interfaces:**
- Produces: `PROBE_DIR_MARKER = "claude-utilities-probe-cwd"`; `is_probe_path(path: Path) -> bool` (marker in any part of the path); `collect_files(paths: list[Path], include_probes: bool = False) -> list[Path]` (directories are scanned recursively and probe sessions skipped unless `include_probes`; a `.jsonl` file named directly is always kept).

- [ ] **Step 1: Write failing tests** `test_is_probe_path_matches_marker_in_project_folder`, `test_directory_scan_skips_probe_sessions_by_default`, `test_include_probes_keeps_them`, `test_file_named_directly_is_kept` (fixtures: a tmp tree with one normal and one `-Users-x--cache-claude-utilities-probe-cwd/` project folder).
- [ ] **Step 2: Run** `uv run pytest tests/test_transcript_speed.py -k probe -q`. Expected: FAIL (names undefined).
- [ ] **Step 3: Implement** the three names above; replace the file-collection loop in `main()` with `collect_files`; add `--include-probes` (store_true).
- [ ] **Step 4: Run** the full suite. Expected: PASS.
- [ ] **Step 5: Commit** `feat(agent-utilities): skip probe sessions in organic stats`.

### Task 3: Cases and prompt building

**Files:**
- Create: `agent-utilities/claude-utilities/probe_runner.py` (sections: cases, run, orchestration, aggregate, compare, CLI)
- Test: `agent-utilities/claude-utilities/tests/test_probe_runner.py`

**Interfaces:**
- Produces: `@dataclass(frozen=True) class Case: name: str; body: str; filler_tokens: int`; `CASES: dict[str, Case]` (`tiny`, `count`, `fill20k`, `fill80k`; bodies per the spec: reply with one word / count 1 to 300 one per line / reply `DONE` after the filler); `VARIANT_FLAGS: dict[str, list[str]]`; `make_filler(tokens: int) -> str` (deterministic synthetic lines, about 4 characters per token; sizes are checked against reported `ctx_tokens` in Task 8); `build_prompt(case: Case, nonce: str) -> str` = filler, body, then `\n(ref {nonce})`.

- [ ] **Step 1: Write failing tests** `test_make_filler_is_deterministic_and_scales`, `test_prompt_ends_with_nonce`, `test_prompts_share_prefix_across_nonces` (`build_prompt(c, "1")` and `build_prompt(c, "2")` share everything before the nonce and differ after), `test_lean_flags_are_exact_and_never_bare`.
- [ ] **Step 2: Run** `uv run pytest tests/test_probe_runner.py -q`. Expected: FAIL (module missing).
- [ ] **Step 3: Implement** the names above; call `transcript_speed.require_python()` from the future `main()`, not at import.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(agent-utilities): probe cases and prompt builder`.

### Task 4: One probe run

**Files:**
- Modify: `agent-utilities/claude-utilities/probe_runner.py`
- Test: `agent-utilities/claude-utilities/tests/test_probe_runner.py`

**Interfaces:**
- Consumes: Task 3 names; `transcript_speed.calls_from_transcript(path)` and `derive(c)` (rows with `model`, `ttfb_s`, `total_s`, `tps_total`, `ctx_tokens`, `cache_hit`).
- Produces: `@dataclass class Run: case: str; variant: str; index: int; wall_s: float | None; api_s: float | None; client_overhead_s: float | None; ttfb_s: float | None; total_s: float | None; tps_total: float | None; ctx_tokens: int | None; cache_hit: float | None; discard_reason: str | None`; `DISCARD_REASONS = ("error", "bad-json", "timeout", "no-transcript", "no-call", "extra-calls")`; `run_once(case: Case, variant: str, index: int, *, model: str, claude_bin: str, probe_dir: Path, projects_dir: Path, timeout_s: float = 600.0) -> Run`.

- [ ] **Step 1: Write failing tests** using a fake `claude` (a tiny Python script written to `tmp_path`, run via `claude_bin`; it reads stdin, prints JSON shaped like the Task 1 fixture, and writes an `api-request` plus `assistant` transcript under `projects_dir/<folder>/<session_id>.jsonl`): `test_run_once_measures_pinned_model_call`, `test_side_call_to_other_model_is_ignored`, `test_two_pinned_calls_are_discarded_as_extra_calls`, `test_is_error_is_discarded`, `test_non_json_stdout_is_discarded`, `test_missing_transcript_is_discarded_after_bounded_wait`, `test_hang_is_discarded_as_timeout` (`timeout_s=0.5`), `test_command_uses_variant_flags_and_stdin_prompt`.
- [ ] **Step 2: Run** the new tests. Expected: FAIL.
- [ ] **Step 3: Implement `run_once`.** Command `[claude_bin, "-p", "--output-format", "json", "--model", model, *VARIANT_FLAGS[variant]]`, `cwd=probe_dir`, prompt on stdin with a random numeric nonce. `wall_s` is the process time; `api_s` is `duration_api_ms / 1000`; `client_overhead_s` is `(duration_ms - duration_api_ms) / 1000`. Find the transcript by globbing `projects_dir/*/<session_id>.jsonl`, polling up to 10 s. Measured call = the single row whose `model` equals the pinned `model`; zero rows is `no-call`, more than one is `extra-calls`. Never raise: every failure returns a `Run` with `discard_reason`.
- [ ] **Step 4: Run** the suite. Expected: PASS.
- [ ] **Step 5: Commit** `feat(agent-utilities): run one claude -p probe`.

### Task 5: Orchestrate rounds

**Files:**
- Modify: `agent-utilities/claude-utilities/probe_runner.py`
- Test: `agent-utilities/claude-utilities/tests/test_probe_runner.py`

**Interfaces:**
- Consumes: `Run`, `run_once`, `Case`.
- Produces: `CACHE_LIFETIME_S = 300.0`; `run_probe(cases: list[Case], variants: list[str], runs: int, *, run_fn=run_once, clock=time.monotonic, **run_kwargs) -> tuple[list[Run], float]` returning all runs and the largest start-to-start gap in seconds between consecutive runs of the same (case, variant).

- [ ] **Step 1: Write failing tests** `test_order_is_round_then_case_then_variant`, `test_max_gap_is_largest_same_case_variant_gap` (fake clock), `test_gap_over_cache_lifetime_warns_on_stderr_and_continues` (`capsys`).
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement `run_probe`** with `run_fn` and `clock` injectable so no test sleeps or calls `claude`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(agent-utilities): interleave probe rounds`.

### Task 6: Aggregate to the output file

**Files:**
- Modify: `agent-utilities/claude-utilities/probe_runner.py`
- Test: `agent-utilities/claude-utilities/tests/test_probe_runner.py`

**Interfaces:**
- Consumes: `Run`, `transcript_speed.percentile`.
- Produces: `SCHEMA = 1`; `COLD_HIT_THRESHOLD = 0.5`; `FIELDS = ("wall_s", "api_s", "client_overhead_s", "ttfb_s", "total_s", "tps_total", "ctx_tokens", "cache_hit")`; `aggregate_runs(runs: list[Run], meta: dict) -> dict`. Output: `{"schema", "meta", "startup": {variant: int | None}, "cases": [{"case", "variant", "runs_ok", "discarded": {reason: n}, "cache_cold_warm": n, "cold": {field: value} | None, "warm": {field: {"p50", "p90"} | None}}]}`. `meta` holds only `label`, `claude_version`, `model`, `runs`, `variants`, `date`, `max_gap_s`. `startup[variant]` is the `tiny` case's warm `ctx_tokens` p50.

- [ ] **Step 1: Write failing tests** `test_cold_is_run_zero_and_warm_percentiles_use_the_rest`, `test_cache_cold_warm_runs_are_excluded_and_counted`, `test_discarded_runs_are_counted_by_reason`, `test_group_with_no_usable_warm_runs_has_none_percentiles`, `test_startup_is_tiny_warm_context_per_variant`, `test_output_has_no_session_ids_or_paths` (dump the JSON and assert absent).
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement `aggregate_runs`**; p50 is the median, p90 nearest-rank, as in `transcript_speed.aggregate`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(agent-utilities): aggregate probe runs`.

### Task 7: Compare two probe files

**Files:**
- Modify: `agent-utilities/claude-utilities/probe_runner.py`
- Test: `agent-utilities/claude-utilities/tests/test_probe_runner.py`

**Interfaces:**
- Consumes: `aggregate_runs` output shape.
- Produces: `compare(path_a: str, path_b: str) -> str`; raises `ValueError` for an unknown `schema` or different `meta.model`. Per (case, variant) in both files: warm p50 and p90 for `wall_s`, `ttfb_s`, `total_s`, `tps_total`, `client_overhead_s`, `ctx_tokens`, `cache_hit`, with B vs A percent difference, plus the cold run and the `cache_cold_warm` count. The route residue from the spec is that B vs A difference for the same case and variant, so it needs no separate line. Then derived lines labelled as estimates: `config cost` (default minus lean `wall_s` p50 for `tiny`, per file) and `prompt-size slope` (`ttfb_s` growth per 10k `ctx_tokens` between `tiny` and `fill80k`, per file and variant). Prints a warning, and still compares, when `claude_version` or `runs` differ; lists (case, variant) pairs present on one side only.

- [ ] **Step 1: Write failing tests** `test_compare_refuses_different_models`, `test_compare_refuses_unknown_schema`, `test_compare_warns_on_version_and_run_count_differences`, `test_compare_uses_intersection_and_lists_one_sided_pairs`, `test_compare_prints_config_cost_and_slope`, `test_compare_prints_na_for_none_percentiles`.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement `compare`**; format widths like `transcript_speed.compare`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(agent-utilities): compare probe files`.

### Task 8: CLI, docs, live check

**Files:**
- Modify: `agent-utilities/claude-utilities/probe_runner.py`, `agent-utilities/claude-utilities/README.md`, `agent-utilities/CLAUDE.md`
- Test: `agent-utilities/claude-utilities/tests/test_probe_runner.py`

**Interfaces:**
- Produces: `main(argv: list[str] | None = None) -> int`. `run --label NAME [--runs 10] [--model claude-sonnet-5-5] [--variants default,lean] [--cases tiny,count,fill20k,fill80k] [--out FILE] [--claude-bin claude] [--probe-dir DIR] [--projects-dir DIR]` writes the JSON (default `probe-<label>.json`). `--compare A.json B.json` prints `compare`. Both call `transcript_speed.require_python()`.

- [ ] **Step 1: Write failing tests** `test_run_writes_aggregate_json_with_fake_claude`, `test_missing_claude_binary_gives_one_line_error`, `test_probe_dir_is_created_if_missing`, `test_unknown_case_or_variant_is_rejected`.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement `main`.** Record `claude --version` output into `meta.claude_version`; if `claude` cannot be run, exit with a one-line error.
- [ ] **Step 4: Docs.** README: a "Probes" section with usage, the cases and variants, the cold/warm and cache-cold rules, the reading table from the spec (as readings to test), the copy-two-files instruction for another machine, the visibility note, and the checks to make on a gateway machine the first time (the gateway accepts the pinned model name as passed to `--model`; the chosen cases raise no permission prompts; the lean flags really shrink the reported `startup`). `agent-utilities/CLAUDE.md`: a Commands line, a Layout mention, and a gotcha that probe sessions are skipped by `transcript_speed.py` unless `--include-probes`.
- [ ] **Step 5: Run** `make check test` in `agent-utilities/claude-utilities` and `make check-links` at the repo root. Expected: all pass.
- [ ] **Step 6: Live check, with the user's agreement (about 8 real calls).** `uv run python probe_runner.py run --label smoke --runs 2 --cases tiny,count`. Expected: a `schema: 1` file; `startup` for `lean` is lower than for `default`; no discards. Also run `fill20k` once and compare the reported `ctx_tokens` against the target (spec: within about 15%); adjust `make_filler` if it is off.
- [ ] **Step 7: Commit** `feat(agent-utilities): probe_runner CLI and docs`, then open a PR (body uses `Refs`, never a closing keyword).
