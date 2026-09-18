# Code-Review Fixes: Capability Eval Benchmark Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all 10 verified `/code-review` findings on the eval-benchmark branch: judge-contract robustness, result preservation on rejudge, scoped preflight, lazy judge transport, row/category validation, label-collision safety, gate subprocess import isolation, dry-run row numbering, the reference suite's unusable coding `limit`, and a dead constant.

**Architecture:** Fixes stay inside existing module boundaries — `judge_core.py` (contract validation), `eval/suite.py` + `eval/cli.py` (validation + dry-run indexing), `eval/runner.py` + `eval/report.py` (scoped preflight, lazy transport, rejudge reconstruction), `agent/gates.py` + `agent/workspace.py` (gate isolation via a seeded regular package instead of `-S`), suite/category TOML config. Each task is independently testable and commits alone.

**Tech Stack:** Python 3.13 (uv), pytest, tomllib, unittest (gate subprocesses), Typer CLI.

**Spec:** Findings from the 2026-09-17 background `/code-review` run (10 findings, each verified against the code before planning). User-facing docs: `docs/guides/11-capability-eval-benchmark.md`, `docs/guides/09-agent-benchmarks.md`.

## Global Constraints

- Python `==3.13.*`; run everything under `modelman/` with `uv run` (pyenv `VIRTUAL_ENV` warning is expected noise).
- Patch *where used* in tests (`modelman.benchmark.eval.runner.isolation`, etc.), per modelman/CLAUDE.md.
- Ruff: explicit `strict=` on any `zip()`; `ruff format <specific-file>` only, never the whole tree; run `make check` (lint+typecheck) per task, `make test-all` at the end.
- Each commit references its plan item (`- completes plan item #N`) and ends with `Co-Authored-By: Claude Code <noreply@anthropic.com>`.
- Guide edits must pass `make check-links`.

---

### Task 1: Validate judge `total` (finding 1)

**Files:**
- Modify: `modelman/src/modelman/benchmark/judge_core.py:134-141` (`parse_response`)
- Test: `modelman/tests/benchmark/test_judge_core.py`

**Interfaces:**
- Produces: `parse_response` raises `JudgeContractError` for a non-int `total` (no `int()` coercion, no raw `ValueError`/`TypeError` escaping the retry loop) and for `total != sum(scores)`. `judge_row`'s retry loop already catches `JudgeContractError`, so malformed totals now consume a retry instead of discarding the category.

- [ ] **Step 1: Write the failing tests** — add to `test_judge_core.py`:

```python
def test_parse_response_rejects_non_int_total():
    # `total` must be a true int: a numeric string ("90") must not be
    # silently coerced, and a non-numeric value must raise JudgeContractError
    # (which judge_row retries) instead of a raw ValueError/TypeError that
    # escaped the retry loop and failed the whole category.
    for bad in ("90", 90.5, None, {"v": 90}, [90]):
        raw = json.dumps({"scores": {"a": 90}, "total": bad})
        with pytest.raises(JudgeContractError, match="total"):
            parse_response(raw, NO_VERDICT_RUBRIC)


def test_parse_response_rejects_total_not_matching_score_sum():
    # The declared total must equal the sum of the declared scores — a
    # fabricated total (999 when scores sum to 90) would otherwise inflate
    # the category score.
    raw = json.dumps({"scores": {"a": 60, "b": 30}, "total": 999, "verdict": "good"})
    with pytest.raises(JudgeContractError, match="total"):
        parse_response(raw, VERDICT_RUBRIC)
    raw2 = json.dumps({"scores": {"a": 60, "b": 30}, "total": 80, "verdict": "good"})
    with pytest.raises(JudgeContractError, match="total"):
        parse_response(raw2, VERDICT_RUBRIC)
```

- [ ] **Step 2: Run to verify fail:** `cd modelman && uv run pytest tests/benchmark/test_judge_core.py -q` → 2 failures.
- [ ] **Step 3: Implement** in `parse_response`, replacing `total=int(data["total"])`:

```python
    total = data["total"]
    if isinstance(total, bool) or not isinstance(total, int):
        raise JudgeContractError(f"total must be an int, got {total!r}")
    if total != sum(scores[d] for d in rubric.dimensions):
        raise JudgeContractError(
            f"total {total} does not equal the sum of scores {sum(scores[d] for d in rubric.dimensions)}"
        )
```

(and return `total=total` in the `JudgeScore` constructor.)
- [ ] **Step 4: Run to verify pass** (same command) + `uv run pytest tests/benchmark/eval tests/benchmark/agent -q` for regressions.
- [ ] **Step 5: Commit** `fix(modelman): validate judge total against scores and reject non-int totals — completes plan item #1`.

### Task 2: Suite/CLI validation — unknown category names, duplicate labels, dead constant (findings 5, 6a, 10)

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/suite.py:81-118` (`_expand_rows`), delete line 29 (`DEFAULT_CODING_DATASET`)
- Modify: `modelman/src/modelman/benchmark/eval/cli.py:62-81` (`run_cmd` validation)
- Test: `modelman/tests/benchmark/eval/test_suite.py`, `modelman/tests/benchmark/eval/test_cli.py`

**Interfaces:**
- Produces: `load_suite` raises `BenchmarkError` on a duplicated row `label`; `run_cmd` raises `BenchmarkError` when any row's `categories` names a category unknown to `--root`. `runner.py` keeps its own `DEFAULT_CODING_DATASET` (the only live copy).

- [ ] **Step 1: Write failing tests** — in `test_suite.py`:

```python
def test_load_suite_rejects_duplicate_labels(tmp_path):
    # Two rows sharing an explicit label both write metrics.jsonl lines
    # keyed by that label; rejudge reconstruction can only keep one, which
    # misattributes model_id/route to the colliding rows. Reject the suite
    # at load time instead.
    body = SUITE_BODY + '\n\n[[rows]]\nmodel = "ollama/a"\nroute = "litellm"\nlabel = "row1"\n'
    with pytest.raises(BenchmarkError, match="duplicate row label"):
        load_suite(_write(tmp_path, body), _registry())
```

(In `SUITE_BODY`, rename row 1 to `label = "row1"` so the added row collides; keep other tests passing.)
In `test_cli.py`, add a test that `eval run --dry-run` (and by extension the real run path, since validation runs before dry-run) exits 1 with `unknown categor` in stderr when a row sets `categories = ["reasning"]`.

- [ ] **Step 2: Run to verify fail.**
- [ ] **Step 3: Implement** — in `_expand_rows`, after building `label`, track seen labels in a set and raise `BenchmarkError(f"suite rows have duplicate label: {label!r}")`. In `run_cmd` (cli.py), right after `categories = list_categories(root)` and before the `--category` filtering, validate every row in `loaded_suite.rows`:

```python
    known_names = {c.name for c in categories}
    for r in loaded_suite.rows:
        if r.categories is not None:
            unknown = [c for c in r.categories if c not in known_names]
            if unknown:
                typer.echo(
                    f"error: row {r.label!r} names unknown category(ies): {', '.join(unknown)} "
                    f"(known: {', '.join(sorted(known_names))})",
                    err=True,
                )
                raise typer.Exit(1)
```

In `suite.py`, delete the unused `DEFAULT_CODING_DATASET` line.
- [ ] **Step 4: Run to verify pass:** `uv run pytest tests/benchmark/eval/test_suite.py tests/benchmark/eval/test_cli.py -q`.
- [ ] **Step 5: Commit** `fix(modelman): validate eval suite labels and row category names — completes plan item #2`.

### Task 3: Scoped preflight + lazy judge transport (findings 3, 4)

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/suite.py:227-253` (`preflight`), `modelman/src/modelman/benchmark/eval/runner.py:182-199` (`run_suite`)
- Test: `modelman/tests/benchmark/eval/test_suite.py`, `modelman/tests/benchmark/eval/test_runner.py`

**Interfaces:**
- Produces: `eval/suite.py::preflight(suite, registry, rows=None)` — `rows` defaults to `suite.rows`; all three checks (provider availability, direct-route blocks, openrouter key for rows) run against `rows` (judge-route openrouter check stays suite-wide). `run_suite` selects rows FIRST, preflights the selection, and builds the judge transport only when at least one selected row will run a non-coding category. `judge_transport_factory` signature unchanged; `run_suite`'s row-loop behavior unchanged.

- [ ] **Step 1: Write failing tests** — in `test_suite.py`:

```python
def test_preflight_scoped_rows_ignores_unselected_providers(tmp_path):
    # --row selecting only row 2 must not fail preflight because row 1's
    # provider is unavailable — preflight's job is to catch what the
    # SELECTED rows would actually hit.
    suite = load_suite(_write(tmp_path, SUITE_BODY), _registry())
    selected = [suite.rows[1]]
    with patch("modelman.benchmark.eval.suite.lifecycle") as mock_lc:
        mock_lc.BACKENDS = {"ollama": mock_backend_unavailable(), "omlx": None}
        preflight(suite, _registry(), rows=selected)  # must not raise
```

(Write `mock_backend_unavailable()` as a simple object whose `check_available()` returns "down".) In `test_runner.py`:

```python
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_skips_judge_transport_for_coding_only_rows(
    mock_isolation, mock_resolve, tmp_path
):
    # A coding-only run never calls the judge, so building the judge
    # transport (which hard-requires a LiteLLM/OpenRouter API key) must be
    # deferred — a purely local EvalPlus run must not need an API key.
    # coding dispatch is patched out; judged_runner.run_judged_category is
    # NOT patched, and the factory below would raise if called.
    ...
    run_suite(
        _suite(row_categories=["coding"]),
        _registry(),
        [coding_category()],
        results_dir=tmp_path,
        judge_transport_factory=_boom_factory,  # raises AssertionError if built
    )
```

with `evalplus_runner.run_coding_category` patched to return a `CodingResult` and a `coding_category()` helper (Category with `name="coding"`, `rubric=None`).

- [ ] **Step 2: Run to verify fail.**
- [ ] **Step 3: Implement** — `preflight(suite, registry, rows=None)`; replace both `suite.rows` iteration bodies with `for row in (rows if rows is not None else suite.rows)` (the `needs_openrouter` row check included). In `run_suite`, reorder to:

```python
    rows = _select_rows(suite.rows, row_filter)
    preflight(suite, registry, rows=rows)
    ...
    selected_names: set[str] = set()
    for row in rows:
        selected_names.update(c.name for c in _row_categories(row, categories))
    needs_judge = any(name != CODING_CATEGORY for name in selected_names)
    judge_transport = (judge_transport_factory or _default_judge_transport_factory)(suite.judge) if needs_judge else None
```

`_run_row` receives `judge_transport | None`; judged dispatch is unreachable when None (guarded by `needs_judge`), assert for safety.
- [ ] **Step 4: Run to verify pass:** `uv run pytest tests/benchmark/eval -q`.
- [ ] **Step 5: Commit** `fix(modelman): scope eval preflight to selected rows and build judge transport lazily — completes plan item #3`.

### Task 4: Rejudge reconstruction keeps partial results + row-dir-keyed metrics (findings 2, 6b)

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/report.py:184-206` (`write_metrics_jsonl`), `modelman/src/modelman/benchmark/eval/runner.py:310-421` (`_read_metrics_row_meta`, `_reconstruct_run_results`)
- Test: `modelman/tests/benchmark/eval/test_rejudge.py`, `modelman/tests/benchmark/eval/test_report.py`

**Interfaces:**
- Produces: `metrics.jsonl` lines gain a `"row_dir"` key (the row's directory basename, unique per run) written by `write_metrics_jsonl`; `_read_metrics_row_meta` keys by `row_dir` (falling back to label for lines that lack it); `_reconstruct_run_results` no longer discards a row's category dirs when `error.txt` exists — it sets `error` AND reconstructs whatever categories are on disk.

- [ ] **Step 1: Write failing tests** — in `test_rejudge.py`:

```python
def test_reconstruct_keeps_scored_categories_alongside_error_txt(tmp_path):
    # A row that scored one category then failed a later one has BOTH
    # scored artifacts and error.txt on disk; reconstruction (which
    # regenerates summary.md/metrics.jsonl after a rejudge) must keep the
    # scored categories, not blank them to N/A — a failure never costs
    # already-computed results.
    run_dir = _seed_run(tmp_path)
    item_dir = run_dir / "01--row1" / "mini_review" / "i1"
    (item_dir / "judge.json").write_text(
        json.dumps({"status": "scored", "combined": {"scores": {"a": 90}, "total": 90,
                     "verdict": "", "flags": [], "rationale": "", "raw_text": ""},
                    "attempts_used": 1}),
        encoding="utf-8")
    (run_dir / "01--row1" / "error.txt").write_text("doc_summary timed out", encoding="utf-8")
    results = _reconstruct_run_results(run_dir)
    assert len(results) == 1
    assert results[0].error == "doc_summary timed out"
    assert "mini_review" in results[0].category_results
```

```python
def test_reconstruct_prefers_row_dir_key_over_colliding_labels(tmp_path):
    # metrics.jsonl lines are now keyed by row_dir (unique); two rows with
    # colliding labels must each get their OWN model_id/route back, not the
    # last line's.
    (tmp_path / "01--dup").mkdir(); (tmp_path / "02--dup").mkdir()
    (tmp_path / "metrics.jsonl").write_text(
        json.dumps({"label": "dup", "row_dir": "01--dup", "model_id": "m/one", "route": "litellm"})
        + "\n"
        + json.dumps({"label": "dup", "row_dir": "02--dup", "model_id": "m/two", "route": "direct"})
        + "\n", encoding="utf-8")
    results = _reconstruct_run_results(tmp_path)
    by_dir = {r.row_dir.name: r for r in results}
    assert by_dir["01--dup"].row.model_id == "m/one-placeholder"  # write exact per implementation
```

(write the assertion to match the real model_ids "m/one"/"m/two".) Also add a `test_report.py` test asserting `write_metrics_jsonl` emits `row_dir`.

- [ ] **Step 2: Run to verify fail.**
- [ ] **Step 3: Implement** — in `write_metrics_jsonl`, add `"row_dir": r.row_dir.name` to the record dict. In `_read_metrics_row_meta`, build two dicts: `by_row_dir` and `by_label`; return both (or a small lookup function `lookup(row) -> dict` that prefers `row_dir`). In `_reconstruct_run_results`, replace the error-path `continue` with: read error text (or None), then fall through to the category reconstruction loop, and append one `RowRunResult(row=row, row_dir=row_dir, category_results=category_results, error=error_text)`.
- [ ] **Step 4: Run to verify pass:** `uv run pytest tests/benchmark/eval -q`.
- [ ] **Step 5: Commit** `fix(modelman): preserve scored categories through rejudge reconstruction and key metrics by row dir — completes plan item #4`.

### Task 5: Dry-run shows full-suite row indexes (finding 8)

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/cli.py:74-93` (`run_cmd` dry-run branch)
- Test: `modelman/tests/benchmark/eval/test_cli.py`

**Interfaces:**
- Produces: dry-run prints each selected row with its FULL-suite 1-based index (matching what `--row <index>` selects at run time), then the filtered count.

- [ ] **Step 1: Write failing test** — a CliRunner test with a 3-row suite and `--row 3` asserting stdout contains `03` (the full-suite index), not `01`.
- [ ] **Step 2: Run to verify fail.**
- [ ] **Step 3: Implement** — replace the dry-run enumeration:

```python
    if dry_run:
        selected = set()
        for i, r in enumerate(loaded_suite.rows, start=1):
            if not row or r.label in set(row) or str(i) in set(row):
                row_categories = r.categories or [c.name for c in categories]
                typer.echo(
                    f"{i:02d}  {r.label}  model={r.model_id}  route={r.route}  "
                    f"categories={row_categories}"
                )
        typer.echo(
            f"{len(rows)} of {len(loaded_suite.rows)} row(s), {len(categories)} categorie(s) "
            "resolved, dry run — nothing executed"
        )
        return
```

- [ ] **Step 4: Run to verify pass:** `uv run pytest tests/benchmark/eval/test_cli.py -q`.
- [ ] **Step 5: Commit** `fix(modelman): dry-run prints full-suite row indexes matching --row selection — completes plan item #5`.

### Task 6: Gate subprocesses without `-S` — seed tests as a regular package (finding 7)

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/workspace.py:129-140` (`create_workspace`), `modelman/src/modelman/benchmark/agent/gates.py:74-92,113-115,183-185`
- Test: existing `modelman/tests/benchmark/agent/test_gates.py`, `test_gates_day31_drift.py`, plus a new test in `test_workspace.py` (create if absent)

**Interfaces:**
- Produces: `create_workspace` seeds an empty `__init__.py` into `root/<tests_dir>/` BEFORE the baseline commit (so it is baseline content, never a "new file" for gate 7 and never a tamper target unless the agent edits it). All three gate subprocesses drop `-S`, restoring site-packages so bundle tests can import third-party deps from the harness venv. Shadowing defense: the workspace's `tests` is now a REGULAR package, which beats the stray site-packages `tests/__init__.py` by sys.path order (cwd is first for `-c` scripts) — regular-vs-regular resolves by order where regular-vs-namespace ignores it. Verified empirically in /tmp simulation during planning.

- [ ] **Step 1: Write the failing test** — in `modelman/tests/benchmark/agent/test_workspace.py` (or create it):

```python
def test_create_workspace_seeds_tests_init(tmp_path):
    # The workspace's tests dir must be a REGULAR package: a regular
    # package on sys.path (cwd first) beats the stray top-level tests/
    # __init__.py that some harness deps (evalplus -> stop-sequencer) ship
    # into site-packages, which otherwise shadows the namespace-package
    # portion regardless of sys.path order. Seeding at create time (before
    # the baseline commit) keeps gates 6/7 semantics intact: it is never a
    # "new file" and can only be flagged tampered if the agent edits it.
    task = load_task(FIXTURE_ROOT / "mini-drift")
    ws = create_workspace(task, base_dir=tmp_path)
    try:
        assert (ws.root / "tests" / "__init__.py").is_file()
        # and it must be part of the baseline commit, not pending content
        assert ws.root / "tests" / "__init__.py" in _baseline_files(ws)
    finally:
        destroy_workspace(ws)
```

(write `_baseline_files` as `git ls-tree -r --name-only HEAD` in the workspace.)
- [ ] **Step 2: Run to verify fail.**
- [ ] **Step 3: Implement** — in `create_workspace`, after `copytree` and before the baseline commit:

```python
    # Seed tests_dir as a REGULAR package (see gates.py's import-isolation
    # note): a regular package in cwd beats any same-named package shipped
    # in harness site-packages, restoring third-party imports for bundle
    # tests without the -S flag.
    tests_dir = task.gates_config.get("build", {}).get("tests_dir")
    if tests_dir:
        (root / tests_dir).mkdir(parents=True, exist_ok=True)
        (root / tests_dir / "__init__.py").touch()
```

Remove `"-S"` from the three subprocess arg lists in `gates.py` and rewrite the block comment (lines 74-84) to describe the regular-package seeding mechanism, naming this plan.
- [ ] **Step 4: Run to verify pass:** `uv run pytest tests/benchmark/agent -q` (includes the full nine-gate runs against mini-drift and day31-drift, which exercise discovery + dotted-name loading through the real subprocesses).
- [ ] **Step 5: Commit** `fix(modelman): seed workspace tests as regular package, restore site-packages to gate subprocesses — completes plan item #6`.

### Task 7: Ship a coding config that can actually score (finding 9) + guide updates

**Files:**
- Modify: `benchmarks/suites/eval-sweep.toml:12-14`, `benchmarks/tasks/eval/coding/items.toml`
- Modify: `docs/guides/11-capability-eval-benchmark.md` (Gotchas bullet), `modelman/src/modelman/benchmark/eval/evalplus_runner.py` docstring (only if wording now contradicts)

**Interfaces:**
- Produces: the reference suite and the coding category default to the FULL dataset (no `limit`), so a shipped-config run produces a real pass@1. `[coding] limit =` remains supported for suites that deliberately want subset generation (documented as still unable to grade).

- [ ] **Step 1:** Remove `limit = 40` from `benchmarks/suites/eval-sweep.toml`'s `[coding]` block and from `benchmarks/tasks/eval/coding/items.toml` (keep `dataset = "humaneval"`). Update both files' comments: `limit` is supported but any limit < the dataset size (164 for humaneval) fails EvalPlus 0.3.1's grading assert — leave it unset for a real score.
- [ ] **Step 2:** Update guide 11's gotcha bullet: the shipped config now runs the full dataset; setting `limit` < dataset size still produces EVALPLUS_ERROR (known EvalPlus 0.3.1 gap).
- [ ] **Step 3:** `grep -rn "limit = 40\|limit=40" docs/ benchmarks/` to catch stale references; run `make check-links`.
- [ ] **Step 4: Commit** `fix(benchmarks): ship eval-sweep coding config that can actually score (full dataset) — completes plan item #7`.

### Task 8: Full verification

- [ ] **Step 1:** `cd modelman && make all` (format check + full test suite + lint/typecheck).
- [ ] **Step 2:** From repo root: `make test-all` (mirrors CI: root lint + modelman + wt).
- [ ] **Step 3:** Fix anything surfaced; amend or add fixup commits referencing plan items.
- [ ] **Step 4: Commit** any final fixes as `fix(modelman): verification pass for code-review fixes — completes plan item #8` (only if needed).
