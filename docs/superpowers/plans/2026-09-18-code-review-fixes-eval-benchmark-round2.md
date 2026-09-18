# Eval Benchmark Code-Review Fixes (Round 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all 9 findings from the 2026-09-18 `/code-review high` run on the capability-eval-benchmark branch.

**Architecture:** Nine independent fixes to the `modelman benchmark eval` subsystem plus two shared-convention fixes (stale test comment, missing test-doc comments). One new shared module `modelman/src/modelman/benchmark/_routes.py` consolidates the credential/endpoint helpers duplicated between `agent/` and `eval/` (user decision). `cooldown_s` is honored between rows rather than dropped (user decision).

**Tech Stack:** Python 3.13, uv, pytest, typer.

**Spec:** The findings themselves (listed per task below) from the `/code-review high` run; no separate design doc.

## Global Constraints

- All commands run from the worktree root: `/Users/keith/github/ohanaverse/local-ai-setup/.worktrees/capability-eval-benchmark`. Python commands use `uv run --directory modelman ...` (repo convention; never bare pytest).
- Every new test carries a comment describing what it tests and why (user's global CLAUDE.md Test Documentation rule).
- Commits during plan execution reference the plan item they complete ("completes plan item #N of this plan").
- Commit trailer: `Co-Authored-By: Claude Code <noreply@anthropic.com>`.
- Test patching follows the "patch where used, not where defined" convention when module-attribute seams exist; helpers moved to `_routes.py` are imported into consuming modules' namespaces, so tests that patch the consumer module's name keep working unchanged.

---

### Task 1: Validate row `categories` against the full category set (not the CLI-filtered one)

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/cli.py:69-87`
- Test: `modelman/tests/benchmark/eval/test_cli.py`

**Interfaces:**
- Consumes: `list_categories(root)` (existing), `run_suite`'s existing `categories` narrowing.
- Produces: no signature changes.

**Finding:** `run_cmd` builds `known_names` from the post-`--category`-filter list, so row 3 of eval-sweep.toml (`categories = ["coding", "reasoning"]`) + `--category coding` falsely errors "unknown category(ies): reasoning".

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/eval/test_cli.py`:

```python
@patch("modelman.benchmark.eval.cli.load_registry")
def test_run_cmd_category_filter_allows_row_naming_other_known_categories(
    mock_load_registry, tmp_path
):
    # --category narrows what RUNS, but a row's `categories =` must be
    # validated against every category that EXISTS under --root, not the
    # already-narrowed set: eval-sweep row 3 declares ["coding",
    # "reasoning"] and `--category coding` should legitimately run just its
    # coding cell, not be rejected as naming an "unknown" category.
    from modelman.registry import ModelEntry, ProviderEntry, Registry

    mock_load_registry.return_value = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "t"
[judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 1
route = "litellm"

[[rows]]
model = "ollama/a"
route = "litellm"
categories = ["coding", "mini_review"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(
        eval_app,
        [
            "run",
            "--suite",
            str(suite_path),
            "--root",
            str(FIXTURE_CATEGORIES),
            "--category",
            "coding",
            "--dry-run",
        ],
    )
    assert result.exit_code == 0
    assert "coding" in result.stdout
```

Note: fixture categories are `coding` + `mini_review`; the row names both, `--category coding` selects only coding. (The shipped eval-sweep equivalent is `reasoning`, which isn't in fixtures.)

- [ ] **Step 2: Run to verify it fails**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_cli.py::test_run_cmd_category_filter_allows_row_naming_other_known_categories -q`
Expected: FAIL with "unknown categor" in output, exit_code == 1.

- [ ] **Step 3: Fix — validate against all categories, then filter**

In `cli.py`, replace the block at lines 69-87 so `known_names` comes from the unfiltered `list_categories(root)` result. Implementation:

```python
    all_categories = list_categories(root)
    categories = all_categories
    if category:
        wanted = set(category)
        categories = [c for c in categories if c.name in wanted]

    # A row's `categories =` must name real categories (validated against
    # the FULL set under --root, not the --category-filtered set: scoping
    # with --category coding must not reject a row that also legitimately
    # names reasoning): a typo would otherwise filter every category out
    # silently and the row would "run" with zero results.
    known_names = {c.name for c in all_categories}
    for r in loaded_suite.rows:
        if r.categories is not None:
            unknown = [c for c in r.categories if c not in known_names]
            if unknown:
                typer.echo(
                    f"error: row {r.label!r} names unknown category(ies): "
                    f"{', '.join(unknown)} (known: {', '.join(sorted(known_names))})",
                    err=True,
                )
                raise typer.Exit(1)
```

Also update the `categories` reference in the dry-run print (`row_categories = r.categories or [c.name for c in categories]` — unchanged semantics since `categories` is already filtered at that point).

- [ ] **Step 4: Run the whole file**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_cli.py -q`
Expected: all pass, including the pre-existing `test_run_cmd_rejects_row_naming_unknown_category` (which names a category absent from fixtures entirely — still rejected).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/eval/cli.py modelman/tests/benchmark/eval/test_cli.py
git commit -m "fix(modelman): validate row categories against full category set

--category-narrowed validation falsely rejected rows naming other
existing categories; a row-level categories override is now checked
against every category under --root. completes plan item #1 of
docs/superpowers/plans/2026-09-18-code-review-fixes-eval-benchmark-round2.md"
```

---

### Task 2: preflight demands OPENROUTER_API_KEY only when a judged category will run

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/suite.py:261-268`
- Test: `modelman/tests/benchmark/eval/test_suite.py`

**Interfaces:**
- Consumes: `preflight(suite, registry, rows=...)` (existing signature).
- Produces: no signature change; behavior change only.

**Finding:** preflight's judge-route clause fires whenever `suite.judge.route == "openrouter"`, even for a coding-only scoped run that never invokes the judge — contradicting `run_suite`'s `needs_judge` lazy-transport design and the degrade-to-N/A philosophy. preflight has no `categories` in hand (it runs before the runner narrows them); only `run_suite` calls it (runner.py:205).

**Decision:** `preflight` gains a `judge_route_active: bool | None = None` keyword. `run_suite` computes `needs_judge` (already exists, lines 215-219) BEFORE calling preflight (move the computation above the line-205 call) and passes `judge_route_active=needs_judge`. When `judge_route_active` is None (default, e.g. direct test calls that don't pass the flag), keep the old behavior (`suite.judge.route == "openrouter"` implies key needed) so existing tests still get the strict check.

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/eval/test_suite.py`:

```python
def test_preflight_judge_route_openrouter_ignored_for_coding_only_selection(tmp_path, monkeypatch):
    # preflight must mirror run_suite's needs_judge design: the judge's
    # OpenRouter key is only required when a selected row actually runs a
    # judged category. A coding-only selection (EvalPlus, purely local,
    # no judge call) must not be blocked on a missing OPENROUTER_API_KEY —
    # the repo's philosophy is degrade-to-N/A, not block a local run.
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.setattr("modelman.benchmark.eval.suite.LITELLM_PLIST", tmp_path / "missing.plist")
    suite = load_suite(_write(tmp_path, SUITE_BODY), _registry())
    coding_only = RowConfig(
        label="coding-only",
        model_id="ollama/a",
        route="litellm",
        provider_id="ollama",
        categories=["coding"],
    )
    preflight(suite, _registry(), rows=[coding_only], judge_route_active=False)
```

- [ ] **Step 2: Run to verify it fails**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_suite.py::test_preflight_judge_route_openrouter_ignored_for_coding_only_selection -q`
Expected: FAIL — `preflight()` got an unexpected keyword 'judge_route_active'.

- [ ] **Step: Fix**

In `suite.py`, change the judge clause and signature:

```python
def preflight(
    suite: Suite,
    registry: Registry,
    rows: list[RowConfig] | None = None,
    *,
    judge_route_active: bool | None = None,
) -> None:
    """Fail fast on what the SELECTED rows would actually hit mid-run.

    `rows` defaults to the full suite; run_suite passes its post-_select_rows
    selection so a scoped run (--row/--category) is never blocked by an
    unselected row's provider being down, a missing direct-route block, or a
    missing openrouter key.

    `judge_route_active`: run_suite computes whether any selected row
    actually runs a judged category (its needs_judge) and passes that here,
    so the judge key is demanded only when the judge will really be called
    (a coding-only EvalPlus run never invokes it). None keeps the
    conservative default: a judge on route=openrouter implies the key."""
```

and the clause becomes:

```python
    if judge_route_active is None:
        judge_needs_key = suite.judge.route == "openrouter"
    else:
        judge_needs_key = judge_route_active and suite.judge.route == "openrouter"
    needs_openrouter = any(row.route == "openrouter" for row in rows) or judge_needs_key
    # Call openrouter_key(LITELLM_PLIST) EXPLICITLY with the module-global
    # name (not the bare openrouter_key()) so a monkeypatch of
    # modelman.benchmark.eval.suite.LITELLM_PLIST takes effect — a
    # default-parameter plist path is bound at def time and patching the
    # module attribute would not steer it.
    if needs_openrouter and openrouter_key(LITELLM_PLIST) is None:
        raise BenchmarkError(
            f"OPENROUTER_API_KEY not found (environment or {LITELLM_PLIST}); "
            "needed for the judge and/or an openrouter row"
        )
```

Note on scope: the judge route "litellm" needs the LiteLLM apiKey (not OPENROUTER_API_KEY) — that check lives in `_default_judge_transport_factory` and stays there. `judge_needs_key = judge_route_active and suite.judge.route == "openrouter"` is correct: demanding the OpenRouter key applies only to a judge on route=openrouter that will actually be called.

In `runner.py` `run_suite`, move the `needs_judge` computation before `preflight(...)` and pass `judge_route_active=needs_judge`:

```python
    rows = _select_rows(suite.rows, row_filter)
    # The judge transport hard-requires an API key (LiteLLM or OpenRouter);
    # build it only when at least one selected row will run a judged
    # category, so a coding-only EvalPlus run stays purely local.
    needs_judge = any(
        category.name != CODING_CATEGORY
        for row in rows
        for category in _row_categories(row, categories)
    )
    # Preflight the SELECTION, not the whole suite: a scoped run must not
    # be blocked by an unselected row's provider being down or its key
    # missing.
    preflight(suite, registry, rows=rows, judge_route_active=needs_judge)
```

and delete the old needs_judge block at lines 212-219, keeping the `judge_transport` construction where it was using the already-computed `needs_judge`.

- [ ] **Step 3: Run the full test files**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_suite.py tests/benchmark/eval/test_runner.py -q`
Expected: all pass. In particular `test_run_suite_skips_judge_transport_for_coding_only_rows` still passes (its factory raises if called — needs_judge is False so it never is).

- [ ] **Step 4: Commit**

```bash
git add modelman/src/modelman/benchmark/eval/suite.py modelman/src/modelman/benchmark/eval/runner.py modelman/tests/benchmark/eval/test_suite.py
git commit -m "fix(modelman): preflight demands judge OpenRouter key only when judged categories run

Mirrors run_suite's needs_judge lazy-transport design in preflight: a
coding-only scoped run (purely local EvalPlus, judge never invoked) no
longer fails preflight on a missing OPENROUTER_API_KEY. completes plan
item #2 of docs/superpowers/plans/2026-09-18-code-review-fixes-eval-benchmark-round2.md"
```

---

### Task 3: rejudge --row accepts label-or-index and errors on no match

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/runner.py:466-497` (`rejudge_run`)
- Modify: `modelman/src/modelman/benchmark/eval/cli.py:167-205` (`judge_cmd`)
- Test: `modelman/tests/benchmark/eval/test_rejudge.py`

**Interfaces:**
- Produces: `rejudge_run(run_dir, categories, *, row_filter, ...)` now interprets `row_filter` entries as row-dir basename OR label OR 1-based full-run index (mirroring `_select_rows`), and raises `BenchmarkError` when a filter value matches nothing.

**Finding:** rejudge's filter matches only `row_dir.name` (`"02--label"`), while `run --row` accepts label-or-index; a reused `--row 2` silently re-judges nothing, exits 0, and still rewrites summary.md/metrics.jsonl.

Design: the row-dir name is `f"{index:02d}--{label}"`. Matching rule per filter value `v`: a row-dir matches if `row_dir.name == v` OR the extracted label (`_row_label_from_dir_name`) equals `v` OR the 1-based position (over the sorted row dirs) equals `int(v)` when `v.isdigit()` (current basename behavior preserved). Any filter value that matches nothing raises `BenchmarkError` naming the value and the known row dirs — never a silent empty rejudge.

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/benchmark/eval/test_rejudge.py`:

```python
def test_rejudge_row_filter_accepts_label_and_index(tmp_path):
    # `run --row` accepts label-or-index, so `judge --row` must too: a user
    # who ran `eval run --row 2` reuses the same value for rejudge. The
    # filter used to match only the row-dir BASENAME ("02--row2"), so an
    # index or label silently matched nothing — zero re-judged items, exit
    # 0, with summary.md/metrics.jsonl rewritten as if a rejudge happened.
    run_dir = _seed_run(tmp_path)
    row2_dir = run_dir / "02--row2"
    item_dir = row2_dir / "mini_review" / "i1"
    item_dir.mkdir(parents=True)
    (item_dir / "response.txt").write_text("another response", encoding="utf-8")

    category = _mini_category()
    outcomes = rejudge_run(
        run_dir,
        [category],
        row_filter=["row2"],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )
    assert len(outcomes) == 1
    assert outcomes[0]["row"] == "02--row2"

    outcomes_idx = rejudge_run(
        run_dir,
        [category],
        row_filter=["2"],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )
    assert len(outcomes_idx) == 1
    assert outcomes_idx[0]["row"] == "02--row2"


def test_rejudge_row_filter_unknown_value_raises(tmp_path):
    # An unknown --row value must fail loudly (exit 1 in the CLI) rather
    # than silently producing an empty no-op rejudge that still rewrites
    # summary.md/metrics.jsonl and exits 0 — the user would believe a
    # rejudge happened when nothing was re-judged.
    from modelman.benchmark.errors import BenchmarkError

    run_dir = _seed_run(tmp_path)
    category = _mini_category()
    with pytest.raises(BenchmarkError, match="matched no row"):
        rejudge_run(
            run_dir,
            [category],
            row_filter=["no-such-row"],
            judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
        )


def _mini_category() -> Category:
    return Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
```

(`_mini_category` is a shared helper deduplicating the Category construction repeated in every existing test in this file; place it above the new tests.)

- [ ] **Step 2: Run to verify they fail**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_rejudge.py::test_rejudge_row_filter_accepts_label_and_index tests/benchmark/eval/test_rejudge.py::test_rejudge_row_filter_unknown_value_raises -q`
Expected: first FAIL (0 outcomes), second FAIL (no raise — silently returns []).

- [ ] **Step 3: Implement**

In `runner.py`, add a selector helper next to `_select_rows`:

```python
def _select_row_dirs(row_dirs: list[Path], row_filter: list[str]) -> list[Path]:
    """Match rejudge's --row against row directories the way `run --row`
    matches suite rows: basename, label (the basename minus its "NN--"
    index prefix), or 1-based position among the sorted row dirs. A value
    that matches nothing raises — a silent empty rejudge would rewrite
    summary.md/metrics.jsonl and exit 0 while re-judging nothing."""
    if not row_filter:
        return list(row_dirs)
    selected: list[Path] = []
    matched_values: set[str] = set()
    known = [d.name for d in row_dirs]
    for value in row_filter:
        value_matched = False
        for pos, row_dir in enumerate(row_dirs, start=1):
            if (
                row_dir.name == value
                or _row_label_from_dir_name(row_dir.name) == value
                or (value.isdigit() and pos == int(value))
            ):
                if row_dir not in selected:
                    selected.append(row_dir)
                value_matched = True
        if value_matched:
            matched_values.add(value)
        else:
            raise BenchmarkError(
                f"--row {value!r} matched no row directories in this run "
                f"(known: {', '.join(known)})"
            )
    return selected
```

Then in `rejudge_run`, replace `if row_filter and row_dir.name not in row_filter: continue` with pre-computation:

```python
    outcomes: list[dict] = []
    row_dirs = sorted(p for p in run_dir.iterdir() if p.is_dir())
    selected_row_dirs = _select_row_dirs(row_dirs, list(row_filter or []))
    for row_dir in selected_row_dirs:
```

(the inner loop body otherwise unchanged).

- [ ] **Step 4: Run the file**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_rejudge.py -q`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/eval/runner.py modelman/tests/benchmark/eval/test_rejudge.py
git commit -m "fix(modelman): rejudge --row accepts label-or-index and errors on no match

The filter used to match only row-dir basenames while run --row accepts
label-or-index; a reused --row 2 silently re-judged nothing, exited 0,
and still rewrote summary.md/metrics.jsonl. completes plan item #3 of
docs/superpowers/plans/2026-09-18-code-review-fixes-eval-benchmark-round2.md"
```

---

### Task 4: Validate explicit `provider =` overrides against known providers

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/suite.py:80-125` (`_expand_rows`)
- Test: `modelman/tests/benchmark/eval/test_suite.py`

**Interfaces:**
- Consumes: `registry.providers` (list of `ProviderEntry`).
- Produces: no signature changes.

**Finding:** a typo'd `provider = "omlqx"` on a litellm-routed row passes load, passes preflight (`BACKENDS.get` misses → skipped), and in run_suite's groupby is not in `ISOLATABLE_PROVIDERS` → no isolation, no error, silently contaminated results against the "benchmark isolation is still mandatory" invariant.

Fix at suite load (in `_expand_rows`): when `raw.get("provider")` is explicitly set, require it to be a known provider id — either in `registry.providers` or in `lifecycle.BACKENDS` (BACKENDS covers isolatable ids; registry covers cloud ids like `openrouter`). The model's registry lookup already happened, so `model_entry.provider_id` is available as a consistency reference, but a user may legitimately re-point a model to a different backend provider? Keep it permissive: validate existence, not consistency with the model's own provider.

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/eval/test_suite.py`:

```python
def test_load_suite_rejects_unknown_explicit_provider(tmp_path):
    # An explicit `provider =` must name a known provider id (registry
    # providers or lifecycle BACKENDS): a typo'd id passes load, passes
    # preflight (BACKENDS.get misses → skipped), and in run_suite's
    # grouping never matches ISOLATABLE_PROVIDERS — so no isolation, no
    # error, and a litellm-routed row benchmarks against whatever is
    # currently loaded, silently violating the mandatory-isolation
    # invariant.
    body = SUITE_BODY.replace(
        'model = "ollama/a"\nroute = "litellm"',
        'model = "ollama/a"\nroute = "litellm"\nprovider = "omlqx"',
    )
    with pytest.raises(BenchmarkError, match="unknown provider"):
        load_suite(_write(tmp_path, body), _registry())
```

- [ ] **Step 2: Run to verify it fails**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_suite.py::test_load_suite_rejects_unknown_explicit_provider -q`
Expected: FAIL — no raise.

- [ ] **Step 3: Implement**

In `suite.py` `_expand_rows`, after `provider_id = raw.get("provider") or model_entry.provider_id`:

```python
        if raw.get("provider"):
            # An explicit provider must name a real provider id (registry
            # providers — e.g. cloud openrouter — or a lifecycle backend).
            # A typo'd id would silently skip provider isolation for a
            # litellm-routed row (not in ISOLATABLE_PROVIDERS, not in
            # BACKENDS), benchmarking against whatever is currently loaded
            # and violating the mandatory-isolation invariant.
            known = {p.id for p in registry.providers} | set(lifecycle.BACKENDS)
            if raw["provider"] not in known:
                raise BenchmarkError(
                    f"suite row {index} names unknown provider: {raw['provider']!r} "
                    f"(model {model_id!r} belongs to {model_entry.provider_id!r})"
                )
```

Note: this runs even when the explicit provider equals the model's own — fine, it exists. A user overriding to a different *valid* provider is allowed (permissive existence check as decided).

- [ ] **Step 4: Run the file**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_suite.py -q`
Expected: all pass. `test_load_suite_rejects_unknown_model_even_with_explicit_provider` unaffected (unknown model raises first).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/eval/suite.py modelman/tests/benchmark/eval/test_suite.py
git commit -m "fix(modelman): validate explicit provider overrides at suite load

A typo'd provider id on a litellm-routed row silently skipped provider
isolation (no error at load, preflight, or run time), violating the
mandatory-isolation invariant; explicit providers are now checked
against registry.providers + lifecycle BACKENDS. completes plan item #4
of docs/superpowers/plans/2026-09-18-code-review-fixes-eval-benchmark-round2.md"
```

---

### Task 5: Update the stale -S comment in test_day31_drift_bundle.py

**Files:**
- Modify: `modelman/tests/benchmark/agent/test_day31_drift_bundle.py:73-84`

**Interfaces:** none.

**Finding:** the comment claims "-S mirrors gates.py's own hidden-test subprocess invocations", but commit 3abac0c reverted -S out of gates.py — gates now run subprocesses WITHOUT -S and rely on `create_workspace` seeding `tests/__init__.py` (regular packages win over site-packages strays by sys.path order). The comment misdirects the next maintainer.

- [ ] **Step 1: Fix the comment**

Replace the comment block above `_run_test_module`'s `subprocess.run` with (verified: the helper runs against `ws.root` for the first two tests — which went through create_workspace's seeding — and against bare `dest` worktrees for the last two; with -S site-packages is dropped entirely so no shadowing is possible in either case, and the seeded regular-package chain also works under -S — the -S is the helper's own belt-and-suspenders for all four tests):

```python
def _run_test_module(root: Path, module_name: str) -> tuple[int, int, int]:
    # "-S" is THIS HELPER's own protection, not a mirror of gates.py: gates
    # runs its subprocesses WITHOUT -S (site-packages stays on sys.path so
    # task bundles may import third-party deps) and relies on
    # create_workspace's tests/__init__.py seeding (3abac0c) to keep a
    # stray top-level tests/__init__.py in site-packages (e.g. evalplus ->
    # stop-sequencer) from shadowing the workspace's tests. This test
    # helper doesn't go through create_workspace's seeded chain for the
    # baseline-worktree cases below (dest dirs hold bare tests/), so it
    # keeps -S for the same shadowing defense.
```

- [ ] **Step 2: Run the test file to confirm no behavior change**

Run: `uv run --directory modelman pytest tests/benchmark/agent/test_day31_drift_bundle.py -q`
Expected: all 4 pass.

- [ ] **Step 3: Commit**

```bash
git add modelman/tests/benchmark/agent/test_day31_drift_bundle.py
git commit -m "docs(modelman): correct stale -S comment in day31 drift bundle test

gates.py runs its subprocesses without -S since 3abac0c (the shadowing
defense is now create_workspace's tests/__init__.py seeding); the -S in
this helper is its own protection for the unseeded baseline-worktree
cases, which the old comment misattributed to gates. completes plan
item #5 of docs/superpowers/plans/2026-09-18-code-review-fixes-eval-benchmark-round2.md"
```

---

### Task 6: Honor cooldown_s between rows within a provider group

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/runner.py` (run_suite row loop)
- Test: `modelman/tests/benchmark/eval/test_runner.py`

**Interfaces:**
- Consumes: `Suite.cooldown_s` (existing field, currently dead).
- Produces: no signature changes.

**Finding:** `cooldown_s` parsed (default 15.0; eval-sweep.toml sets 15.0) but `run_suite` never reads it — a dead config knob. **User decision: honor it.**

Semantics: sleep `suite.cooldown_s` BETWEEN rows within the same provider group, after the first (no sleep before the first row of a group — it just got isolated/warmup, nothing to settle from). This mirrors agent/runner.py's between-passes cooldown and keeps the shipped TOML's 15.0 honest. Cooldown applies regardless of row success (a failed row still loads/heats the provider via isolation). Test with `cooldown_s=0` existing tests are unaffected.

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/eval/test_runner.py`:

```python
@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_sleeps_cooldown_between_rows_in_a_group(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
):
    # Suite.cooldown_s is documented (and shipped in eval-sweep.toml as
    # 15.0) as thermal settling between rows; it must actually be honored
    # after each row except the first in a provider group — the first row
    # runs right after isolation warmup, with nothing to settle from. It
    # was previously parsed but never read: a dead knob that silently
    # ignored the user's config.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_run_judged.return_value = _fake_category_result(50.0)

    suite = _suite()
    suite.cooldown_s = 0.25
    suite.rows = [
        RowConfig(label="r1", model_id="ollama/a", route="litellm", provider_id="ollama"),
        RowConfig(label="r2", model_id="ollama/a", route="litellm", provider_id="ollama"),
        RowConfig(label="r3", model_id="ollama/a", route="litellm", provider_id="ollama"),
    ]
    with patch("modelman.benchmark.eval.runner.time.sleep") as mock_sleep:
        run_suite(
            suite,
            _registry(),
            [_mini_review_category()],
            results_dir=tmp_path,
            judge_transport_factory=lambda suite: object(),
        )
    # 3 rows, one group: cooldown after rows 1 and 2, none after row 3.
    assert mock_sleep.call_args_list == [call(0.25), call(0.25)]
```

(`from unittest.mock import call` needs adding to the file's imports if absent.)

- [ ] **Step 2: Run to verify it fails**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_runner.py::test_run_suite_sleeps_cooldown_between_rows_in_a_group -q`
Expected: FAIL — `mock_sleep.call_args_list == []` (never called).

- [ ] **Step 3: Implement**

In `runner.py`: add `import time` to the imports. Cooldown semantics: sleep between rows within a provider group, unconditionally (a failed row still left the provider warm from isolation; tracking "rows that actually ran" buys nothing). Implementation — change `for row in group:` to:

```python
            for row_position, row in enumerate(group):
                # Thermal settling between rows within a provider group
                # (agent/runner.py honors cooldown_s between passes the
                # same way); none before the first row — it runs right
                # after the group's isolation warmup, with nothing to
                # settle from.
                if row_position > 0:
                    time.sleep(suite.cooldown_s)
```

at the top of the loop body (before the `_isolation_extra_args` try).

- [ ] **Step 4: Run the file**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_runner.py -q`
Expected: all pass (existing suites use `cooldown_s=0`; `_suite()` in this file sets 0, so `time.sleep(0)` is a no-op call that doesn't disturb the other tests).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/eval/runner.py modelman/tests/benchmark/eval/test_runner.py
git commit -m "fix(modelman): honor eval Suite.cooldown_s between rows within a group

Parsed since the beginning (eval-sweep.toml ships 15.0) but never read
by run_suite — a dead knob. Now sleeps between rows in a provider
group, mirroring agent/runner.py's between-passes cooldown. completes
plan item #6 of docs/superpowers/plans/2026-09-18-code-review-fixes-eval-benchmark-round2.md"
```

---

### Task 7: Promote credential/endpoint helpers to benchmark/_routes.py

**Files:**
- Create: `modelman/src/modelman/benchmark/_routes.py`
- Modify: `modelman/src/modelman/benchmark/agent/suite.py` (delete openrouter_key, LITELLM_PLIST, OPENROUTER_BASE_URL; import from _routes)
- Modify: `modelman/src/modelman/benchmark/agent/pidriver.py` (delete LIVE_PI_MODELS_PATH, _load_live_models; import from _routes)
- Modify: `modelman/src/modelman/benchmark/agent/runner.py` (_build_judge_transport uses shared helpers)
- Modify: `modelman/src/modelman/benchmark/eval/suite.py` (delete LITELLM_PLIST, LIVE_PI_MODELS_PATH, OPENROUTER_BASE_URL, openrouter_key, load_live_models; import from _routes)
- Modify: `modelman/src/modelman/benchmark/eval/runner.py` (import from _routes)
- Create: `modelman/tests/benchmark/test_routes.py`
- Modify packaging if needed: check whether `[tool.setuptools] packages` or Dockerfile COPY covers `modelman/src/modelman/benchmark/` already (it does — benchmark is part of the modelman package; a new submodule under an existing package needs no packaging change). The `_routes.py` name starts with underscore = private module, same pattern as `_toml_io.py`.

**Interfaces:**
- Produces: `modelman.benchmark._routes` with:
  - `LITELLM_PLIST: Path`
  - `LIVE_PI_MODELS_PATH: Path`
  - `OPENROUTER_BASE_URL: str`
  - `openrouter_key(plist_path: Path = LITELLM_PLIST) -> str | None`
  - `load_live_models(path: Path = LIVE_PI_MODELS_PATH) -> dict`
  - `litellm_credentials(live_models_path: Path = LIVE_PI_MODELS_PATH) -> tuple[str, str]` — returns `(base_url, api_key)` from `~/.pi/agent/models.json`'s litellm provider entry; raises `BenchmarkError` when the apiKey is missing (message identical to the current callers').
- Back-compat: `agent.suite.openrouter_key`, `agent.suite.LITELLM_PLIST`, `agent.suite.OPENROUTER_BASE_URL`, `pidriver.LIVE_PI_MODELS_PATH`, `eval.suite.openrouter_key`, `eval.suite.load_live_models`, `eval.suite.LITELLM_PLIST`, `eval.suite.LIVE_PI_MODELS_PATH`, `eval.suite.OPENROUTER_BASE_URL` remain importable names (re-exported via `from ... import X` at each module top — a from-import re-binds the name in the consuming module's namespace, so "patch where used" tests and importers keep working unchanged).

**Finding:** three copies of the models.json litellm-credential reading (agent/runner.py:133, eval/suite.py:resolve_row_endpoint, eval/runner.py:_default_judge_transport_factory) and two of the plist OPENROUTER_API_KEY extraction (agent/suite.py:195, eval/suite.py:170). The next plist/models.json format change gets fixed in one copy and silently missed in the others.

- [ ] **Step 1: Create `modelman/src/modelman/benchmark/_routes.py`**

```python
"""Shared credential/endpoint helpers for the benchmark subsystem's
route resolution: the OpenRouter key (environment or the LiteLLM
LaunchAgent plist) and the LiteLLM apiKey/baseUrl from pi's
~/.pi/agent/models.json.

agent/ and eval/ each parse their own suites (deliberately parallel —
their row shapes differ), but the CREDENTIALS those routes need are the
same machine-level facts in both benchmarks; this module is the single
source so a plist/models.json format change is fixed exactly once."""

from __future__ import annotations

import json
import os
import plistlib
from pathlib import Path

from modelman.benchmark.errors import BenchmarkError

LITELLM_PLIST = Path.home() / "Library" / "LaunchAgents" / "local.litellm.proxy.plist"
LIVE_PI_MODELS_PATH = Path.home() / ".pi" / "agent" / "models.json"
OPENROUTER_BASE_URL = "https://openrouter.ai/api/v1"


def openrouter_key(plist_path: Path = LITELLM_PLIST) -> str | None:
    """The OpenRouter key, from the environment or the LiteLLM LaunchAgent.

    Same two places preflight looks; a judge on route=openrouter needs the
    value, not just the knowledge that one exists."""
    env_key = os.environ.get("OPENROUTER_API_KEY")
    if env_key:
        return env_key
    if not plist_path.exists():
        return None
    try:
        with plist_path.open("rb") as f:
            data = plistlib.load(f)
    except Exception:
        return None
    key = data.get("EnvironmentVariables", {}).get("OPENROUTER_API_KEY")
    return str(key) if key else None


def load_live_models(path: Path = LIVE_PI_MODELS_PATH) -> dict:
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return {}


def litellm_credentials(
    live_models_path: Path = LIVE_PI_MODELS_PATH,
) -> tuple[str, str]:
    """(base_url, api_key) for the local LiteLLM gateway, from pi's
    models.json litellm provider entry. Raises when the apiKey is absent —
    every caller (judge transports on both benchmarks, eval's litellm
    route) hard-requires it."""
    live = load_live_models(live_models_path)
    litellm_entry = live.get("providers", {}).get("litellm", {})
    api_key = litellm_entry.get("apiKey")
    if not api_key:
        raise BenchmarkError(
            "no LiteLLM apiKey found in ~/.pi/agent/models.json; launch a wt "
            "pi session in litellm mode at least once to seed it"
        )
    base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
    return base_url, api_key


__all__ = [
    "LITELLM_PLIST",
    "LIVE_PI_MODELS_PATH",
    "OPENROUTER_BASE_URL",
    "litellm_credentials",
    "load_live_models",
    "openrouter_key",
]
```

- [ ] **Step 2: Write tests for _routes.py**

Create `modelman/tests/benchmark/test_routes.py`:

```python
"""Tests for modelman.benchmark._routes — the shared credential helpers
both benchmark subsystems (agent/ and eval/) resolve their routes
through."""

import json
from pathlib import Path

import pytest

from modelman.benchmark._routes import litellm_credentials, load_live_models
from modelman.benchmark.errors import BenchmarkError


def test_load_live_models_missing_path_returns_empty(tmp_path):
    # A missing models.json must read as "no providers" rather than raising
    # — the first wt/pi session hasn't necessarily happened yet, and every
    # caller treats "no litellm entry" as the actionable error, not
    # "file absent".
    assert load_live_models(tmp_path / "nope.json") == {}


def test_litellm_credentials_returns_base_url_and_key(tmp_path):
    # Both benchmarks' judge transports (and eval's litellm route) key off
    # the same (base_url, apiKey) pair from pi's models.json — one shared
    # extraction, so a format change is fixed once.
    live_path = tmp_path / "models.json"
    live_path.write_text(
        json.dumps(
            {"providers": {"litellm": {"baseUrl": "http://localhost:4000/v1", "apiKey": "sk-x"}}}
        ),
        encoding="utf-8",
    )
    assert litellm_credentials(live_path) == ("http://localhost:4000/v1", "sk-x")


def test_litellm_credentials_defaults_base_url_when_unspecified(tmp_path):
    # models.json may omit baseUrl (pi writes only what it needs); the
    # canonical local gateway default must apply, not a None/blank base_url.
    live_path = tmp_path / "models.json"
    live_path.write_text(json.dumps({"providers": {"litellm": {"apiKey": "sk-x"}}}), encoding="utf-8")
    assert litellm_credentials(live_path) == ("http://localhost:4000/v1", "sk-x")


def test_litellm_credentials_missing_key_raises(tmp_path):
    # No apiKey means no way to authenticate to the gateway — the shared
    # error must name the seeding remedy (run a wt pi session in litellm
    # mode) so users know how to fix it, matching the pre-refactor callers.
    live_path = tmp_path / "models.json"
    live_path.write_text(json.dumps({"providers": {}}), encoding="utf-8")
    with pytest.raises(BenchmarkError, match="apiKey"):
        litellm_credentials(live_path)
```

- [ ] **Step 3: Run the new tests**

Run: `uv run --directory modelman pytest tests/benchmark/test_routes.py -q`
Expected: all pass.

- [ ] **Step 4: Rewire agent/suite.py**

In `modelman/src/modelman/benchmark/agent/suite.py`:
- Delete the `openrouter_key` function, `LITELLM_PLIST` constant, and `OPENROUTER_BASE_URL` constant (lines ~18, ~25, ~195-211).
- Add `from modelman.benchmark._routes import LITELLM_PLIST, OPENROUTER_BASE_URL, openrouter_key` (top imports). Since agent/suite.py's `preflight` still calls `openrouter_key`, the re-export keeps the module-level name.
- `_openrouter_key_available` stays as-is (calls the re-exported name).
- `modelman` import group: `_routes` import alphabetizes before `modelman.benchmark.errors` — `from modelman.benchmark import _routes` or a direct name import; follow existing file style (direct names).

Check the module docstring of agent/suite.py for any "duplicated deliberately" language about these helpers and adjust if present.

- [ ] **Step 5: Rewire agent/pidriver.py**

In `modelman/src/modelman/benchmark/agent/pidriver.py`:
- Delete `LIVE_PI_MODELS_PATH` and `_load_live_models`.
- Add `from modelman.benchmark._routes import LIVE_PI_MODELS_PATH, load_live_models`.
- `resolve_pi_target`'s `live = _load_live_models(live_models_path)` becomes `live = load_live_models(live_models_path)`.
- Check tests for `_load_live_models` references: earlier grep showed none.

- [ ] **Step 6: Rewire agent/runner.py `_build_judge_transport`**

Replace the litellm branch of `_build_judge_transport` (lines 128-139) with the shared helper:

```python
    base_url, api_key = litellm_credentials(live_models_path)
    return judge.LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=judge_cfg.model)
```

adding `from modelman.benchmark._routes import litellm_credentials` to runner.py's imports. Note the error message changes from the agent/runner copy ("no LiteLLM apiKey found in ~/.pi/agent/models.json for the judge transport") to the shared one ("...; launch a wt pi session in litellm mode at least once to seed it") — acceptable unification; check `test_runner.py` for a test matching the old message (`test_judge_transport_without_openrouter_key_names_the_missing_key` tests the openrouter branch, fine).

- [ ] **Step 7: Rewire eval/suite.py**

In `modelman/src/modelman/benchmark/eval/suite.py`:
- Delete `LITELLM_PLIST`, `LIVE_PI_MODELS_PATH`, `OPENROUTER_BASE_URL`, `openrouter_key`, `load_live_models` definitions.
- Add `from modelman.benchmark._routes import (
    LITELLM_PLIST,
    LIVE_PI_MODELS_PATH,
    OPENROUTER_BASE_URL,
    load_live_models,
    openrouter_key,
  )`.
- `resolve_row_endpoint`'s litellm branch becomes:

```python
    if row.route == "litellm":
        base_url, api_key = litellm_credentials(live_models_path)
        return base_url, row.model_id, api_key
```

(adding `litellm_credentials` to the import). Note: this changes the missing-key error message from eval/suite's own ("no LiteLLM apiKey found in ~/.pi/agent/models.json; launch a wt pi session...") to the shared one — same wording by construction (the shared message was copied from eval/suite's, which includes the seeding remedy). Check `tests/benchmark/eval/test_suite.py::test_resolve_row_endpoint_litellm_route_reads_live_models_json` still passes — it seeds a live-path with both baseUrl and apiKey, matching.
- Update the module docstring: the "Deliberately parallel to (not shared with)" note stays true for suite PARSING but should acknowledge the credential helpers are shared via `modelman.benchmark._routes`.

- [ ] **Step 8: Rewire eval/runner.py `_default_judge_transport_factory`**

Replace its litellm branch (lines 85-93) with:

```python
    base_url, api_key = litellm_credentials(LIVE_PI_MODELS_PATH)
    return LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=judge_cfg.model)
```

Update the `from modelman.benchmark.eval.suite import (...)` list: drop names now imported from _routes (keep whatever eval/suite still re-exports or move the import directly — import from `modelman.benchmark._routes` directly, matching the agent side).

- [ ] **Step 9: Run all affected test files**

Run: `uv run --directory modelman pytest tests/benchmark/test_routes.py tests/benchmark/agent/ tests/benchmark/eval/ -q`
Expected: all pass. Patching notes:
- Agent tests monkeypatch the `OPENROUTER_API_KEY` env var — the shared `openrouter_key` still reads it first, so those are unaffected.
- Both `agent/suite.preflight` and `agent/runner._build_judge_transport` pass `plist_path` explicitly through their signatures, so the default-parameter seam never mattered there.
- The re-exported names (`eval.suite.LITELLM_PLIST` etc.) remain re-bindable module attributes; where a caller must honor a monkeypatched path, it passes the module-global explicitly at call time (the pattern Task 2 already established for eval's preflight: `openrouter_key(LITELLM_PLIST)`).

Then run lint + typecheck before committing:
Run: `cd modelman && make check`

- [ ] **Step 10: Commit**

```bash
git add modelman/src/modelman/benchmark/_routes.py modelman/src/modelman/benchmark/agent/suite.py modelman/src/modelman/benchmark/agent/pidriver.py modelman/src/modelman/benchmark/agent/runner.py modelman/src/modelman/benchmark/eval/suite.py modelman/src/modelman/benchmark/eval/runner.py modelman/tests/benchmark/test_routes.py
git commit -m "refactor(modelman): share benchmark credential helpers via _routes

Three copies of the models.json litellm-credential reading and two of the
plist OPENROUTER_API_KEY extraction existed across agent/ and eval/; the
next plist/models.json format change would be fixed in one copy and
silently missed in the others. Completes plan item #7 of
docs/superpowers/plans/2026-09-18-code-review-fixes-eval-benchmark-round2.md"
```

---

### Task 8: _read_metrics_row_meta skips malformed metrics.jsonl lines

**Files:**
- Modify: `modelman/src/modelman/benchmark/eval/runner.py:333-355` (`_read_metrics_row_meta`)
- Test: `modelman/tests/benchmark/eval/test_rejudge.py`

**Interfaces:** no signature changes.

**Finding:** a bare `json.loads` on every metrics.jsonl line — one truncated/hand-edited line crashes rejudge with a raw JSONDecodeError (judge_cmd catches only BenchmarkError/FileNotFoundError). House convention (usage/wt_state.py): malformed lines skipped, not fatal.

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/eval/test_rejudge.py`:

```python
def test_reconstruct_skips_malformed_metrics_lines(tmp_path):
    # metrics.jsonl is append-style and can be left with a truncated final
    # line (run interrupted mid-write) or a hand-edited line; the
    # reconstruction must skip malformed lines rather than crash rejudge
    # with a raw JSONDecodeError — a skipped line only degrades to the
    # label-fallback keying (the same house convention as usage/wt_state's
    # "malformed lines are skipped, not fatal").
    from modelman.benchmark.eval.runner import _reconstruct_run_results

    run_dir = _seed_run(tmp_path)
    (run_dir / "metrics.jsonl").write_text(
        json.dumps(
            {
                "label": "row1",
                "model_id": "ollama/a",
                "route": "litellm",
                "error": None,
                "categories": {"mini_review": 10},
            }
        )
        + "\n"
        + '{"label": "trunc", "model_i'  # truncated mid-write
        + "\n",
        encoding="utf-8",
    )
    results = _reconstruct_run_results(run_dir)
    assert len(results) == 1
    assert results[0].row.model_id == "ollama/a"  # from the healthy line, not a crash
```

- [ ] **Step 2: Run to verify it fails**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_rejudge.py::test_reconstruct_skips_malformed_metrics_lines -q`
Expected: FAIL with json.JSONDecodeError.

- [ ] **Step 3: Implement**

In `_read_metrics_row_meta`:

```python
    for line in metrics_path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            # A run interrupted mid-write (or a hand-edited line) leaves a
            # partial line; skip it — the affected row just degrades to the
            # label-fallback keying (house convention: malformed lines are
            # skipped, not fatal, same as usage/wt_state).
            continue
        key = row.get("row_dir") or row.get("label")
        meta[key] = row
```

- [ ] **Step 4: Run the file**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_rejudge.py -q`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/eval/runner.py modelman/tests/benchmark/eval/test_rejudge.py
git commit -m "fix(modelman): skip malformed metrics.jsonl lines in rejudge reconstruction

A truncated final line (interrupted run) or hand-edited line crashed
rejudge with a raw JSONDecodeError; now skipped per the house
malformed-lines-are-skipped convention. completes plan item #8 of
docs/superpowers/plans/2026-09-18-code-review-fixes-eval-benchmark-round2.md"
```

---

### Task 9: Add test-doc comments to test_category.py

**Files:**
- Modify: `modelman/tests/benchmark/eval/test_category.py`

**Interfaces:** none.

**Finding:** all four tests lack the descriptive comment required by the user's global CLAUDE.md Test Documentation rule (every other new test file in this PR carries them).

- [ ] **Step 1: Add comments**

```python
def test_load_category_reads_items_and_rubric():
    # The happy path: a judged category loads its items (with per-item
    # meta), its rubric dimensions, and the rubric markdown — the three
    # inputs judged_runner's prompt builder and judge_core's scorer both
    # consume, so any load-shape regression breaks the whole judged path.
    ...


def test_load_category_coding_has_no_rubric():
    # coding is the one rubric-less category (EvalPlus grades it, not a
    # judge): load_category must return rubric=None with its dataset/limit
    # config, and every dispatch site branches on exactly this None.
    ...


def test_load_category_rejects_rubric_not_summing_to_100():
    # Rubric dimensions must sum to 100: scores are reported on a /100
    # scale, so a mis-weighted rubric would silently make category scores
    # incomparable — the check is the guard that keeps every category's
    # score_100 directly comparable.
    ...


def test_list_categories_finds_every_subdirectory():
    # list_categories is the --root inventory behind both `list-categories`
    # and run's category validation; it must surface every category
    # directory (judged and coding alike), or rows silently run nothing.
    ...
```

(the `...` are the existing bodies — unchanged; only comments added).

- [ ] **Step 2: Run the file**

Run: `uv run --directory modelman pytest tests/benchmark/eval/test_category.py -q`
Expected: all 4 pass.

- [ ] **Step 3: Commit**

```bash
git add modelman/tests/benchmark/eval/test_category.py
git commit -m "docs(modelman): add test-doc comments to test_category.py

Per the Test Documentation convention every other test file in this PR
follows: what scenario each test covers and why it matters. completes
plan item #9 of docs/superpowers/plans/2026-09-18-code-review-fixes-eval-benchmark-round2.md"
```

---

### Task 10: Full verification

- [ ] **Step 1: Run the full modelman suite + checks**

Run: `cd modelman && make all` (format + test + check)
Expected: all pass. If `ruff format` reflows unrelated files, `git status --short` and only commit our own files (per modelman/CLAUDE.md's ruff-format gotcha).

- [ ] **Step 2: Live smoke (optional but recommended)**

Run: `uv run --directory modelman modelman benchmark eval run --suite benchmarks/suites/eval-sweep.toml --root benchmarks/tasks/eval --category coding --dry-run`
Expected: exit 0, rows print with coding category — the exact command from finding 1 that previously errored.

- [ ] **Step 3: Final commit if anything needed adjusting**

Only if Task 10's checks forced changes.

## Self-Review

1. **Spec coverage:** all 9 findings map to Tasks 1-9. Finding for cooldown (6) and _routes (7) follow the user's explicit choices. ✔
2. **Placeholder scan:** no TBDs; every step has concrete code. ✔
3. **Type consistency:** `_select_row_dirs(row_dirs, row_filter)` used only in Task 3; `litellm_credentials(live_models_path) -> tuple[str, str]` consistent across Tasks 7's rewire steps; Task 2's `judge_route_active` consistent. One correction integrated inline: Task 2's test monkeypatches `modelman.benchmark.eval.suite.LITELLM_PLIST` — the implementation must call `openrouter_key(LITELLM_PLIST)` explicitly (module-global name lookup at call time) for the patch to take effect. ✔
```
