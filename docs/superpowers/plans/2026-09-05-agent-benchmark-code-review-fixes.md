# Agent Benchmark Code-Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 10 findings from the `/code-review` run on `modelman/src/modelman/benchmark/agent/` (the agentic coding benchmark) and `bin/llm-isolate-provider`, each with a regression test that fails before the fix and passes after.

**Architecture:** Each task is an independent bug fix in one module (occasionally a module + its one caller). No shared scaffolding is needed across tasks — they touch disjoint files except Task 5, which touches both `isolation.py` and `runner.py`. Tasks are TDD: write the failing test, confirm it fails for the right reason, make the minimal fix, confirm it passes.

**Tech Stack:** Python 3.13, pytest, `uv run` (per repo's `python-rules.md`), bash (`bin/llm-isolate-provider`).

**Spec:** No separate spec doc — this plan implements the findings from the `/code-review --level high` run in this session (10 findings, ranked most-severe first). Each task quotes its finding's failure scenario verbatim from that review.

## Global Constraints

- Run all Python commands with `uv run` from the `modelman/` directory (per `~/.claude/rules/python-rules.md` — a pyenv-managed Python may not find dependencies).
- Every test gets a one-to-two-sentence comment describing the behavior covered and why it matters (per the user's global CLAUDE.md "Test Documentation" rule) — the tests drafted below already include this; preserve it.
- Run only the touched test file per task (`uv run pytest tests/benchmark/agent/test_X.py -v`), not the full suite, until the final task, which runs `make check` (lint+typecheck) and `make test` (full suite) per `modelman/CLAUDE.md`'s testing guidance.
- No new abstractions beyond what each finding requires — several of these are one-line or few-line fixes; do not refactor surrounding code.
- Commits: `git add <touched files>` + commit message `fix(agent-bench): <summary> - completes plan item #<N>`, then `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq` trailers per this session's attribution instructions.

---

### Task 1: judge.py — reject boolean scores

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/judge.py:148`
- Test: `modelman/tests/benchmark/agent/test_judge.py`

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing new — `parse_response` keeps its existing signature and now raises `JudgeContractError` for a boolean score instead of silently accepting it.

**Finding:** Score validation accepts JSON booleans as valid dimension scores because Python's `bool` is an `int` subclass. If the judge model emits `{"scores": {"root_cause": true, ...}}`, `isinstance(value, int)` is `True` and `0 <= True <= 30` is `True`, so the row is silently scored 1 instead of raising `JudgeContractError` and retrying — corrupting `rubric_total`/`composite` with no error surfaced anywhere.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/agent/test_judge.py`, near `test_parse_response_rejects_unknown_verdict`:

```python
def test_parse_response_rejects_a_boolean_score():
    """Python's bool is an int subclass, so `isinstance(value, int)` alone
    lets a JSON `true`/`false` through as a valid 0-30 score — a plausible
    malformed reply from a less-compliant judge model. Without this check the
    row is silently scored 1 (True == 1) instead of raising JudgeContractError
    and retrying, corrupting rubric_total/composite with no error anywhere."""
    bad = json.loads(VALID_RESPONSE)
    bad["scores"]["root_cause"] = True
    with pytest.raises(JudgeContractError, match="root_cause"):
        parse_response(json.dumps(bad))
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_judge.py::test_parse_response_rejects_a_boolean_score -v`
Expected: FAIL (no exception raised — `True` passes the current `isinstance(value, int)` check).

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/agent/judge.py`, change line 148:

```python
    for dim in DIMENSIONS:
        value = scores[dim]
        if isinstance(value, bool) or not isinstance(value, int) or not (0 <= value <= MAX_POINTS[dim]):
            raise JudgeContractError(f"invalid score for {dim}: {value!r}")
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: PASS (all tests in the file, including the new one and the existing `test_parse_response_accepts_valid_contract`).

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/benchmark/agent/judge.py tests/benchmark/agent/test_judge.py
git commit -m "$(cat <<'EOF'
fix(agent-bench): reject boolean judge scores - completes plan item #1

Python's bool is an int subclass, so a judge reply with a JSON boolean
score (e.g. {"root_cause": true}) passed isinstance(value, int) and was
silently scored 1 instead of raising JudgeContractError and retrying.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 2: workspace.py — handle git rename entries in diff-status parsing

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/workspace.py:47-58`
- Test: `modelman/tests/benchmark/agent/test_workspace.py`

**Interfaces:**
- Consumes: nothing new.
- Produces: `Workspace._status_since_baseline()` keeps its return type `list[tuple[str, str]]`; `new_files_since_baseline()` / `modified_or_deleted_since_baseline()` keep their existing signatures and now correctly surface files git reports as renamed/copied.

**Finding:** `_status_since_baseline()` parses `git diff --name-status` with a single-tab `.partition("\t")`, which mishandles git rename entries (`R100\told\tnew` — three tab-separated fields, not two). With `diff.renames` enabled (a common global git config override), an agent diff git reads as a rename produces status `"R100"`, matching neither `"A"` nor `("M", "D")` in either caller — the file silently drops out of both `new_files_since_baseline()` and `modified_or_deleted_since_baseline()`, so gate 3 (`NON_EMPTY_DIFF`), gate 7 (`HAS_REGRESSION_TEST`), and judge `seed_contents` can all miss real agent changes.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/agent/test_workspace.py`:

```python
import subprocess


def test_rename_detected_when_diff_renames_is_enabled(tmp_path):
    """A repo with `diff.renames` enabled (a common global git config
    override, independent of any -M flag this code passes) reports a
    moved-and-edited file as a single R100 status line with three
    tab-separated fields (status, old-name, new-name), not the two fields
    A/M/D lines have. The old single-tab .partition() folded old+new into
    one mangled `name` field and the status matched neither "A" nor
    ("M", "D") in either caller — the file silently vanished from both
    new_files_since_baseline() and modified_or_deleted_since_baseline(),
    which gates 3 and 7 rely on to see real agent changes."""
    ws = create_workspace(_task(), base_dir=tmp_path)
    try:
        subprocess.run(["git", "config", "diff.renames", "true"], cwd=ws.root, check=True)
        original = (ws.root / "tests" / "test_pkg.py").read_text(encoding="utf-8")
        (ws.root / "tests" / "test_pkg.py").unlink()
        (ws.root / "tests" / "test_pkg_moved.py").write_text(
            original + "\n# renamed with a small edit\n", encoding="utf-8"
        )
        new_names = [p.name for p in ws.new_files_since_baseline()]
        changed_names = [p.name for p in ws.modified_or_deleted_since_baseline()]
        assert "test_pkg_moved.py" in new_names
        assert "test_pkg.py" in changed_names
    finally:
        destroy_workspace(ws)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_workspace.py::test_rename_detected_when_diff_renames_is_enabled -v`
Expected: FAIL — both assertions fail because the `R100` line is dropped entirely (neither list contains either filename).

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/agent/workspace.py`, replace `_status_since_baseline`:

```python
    def _status_since_baseline(self) -> list[tuple[str, str]]:
        _git(["add", "-A"], cwd=self.root)
        result = _git(
            ["diff", self.baseline_sha, "--cached", "--name-status", "--"], cwd=self.root
        )
        entries = []
        for line in result.stdout.splitlines():
            parts = line.split("\t")
            status = parts[0]
            if status[0] in ("R", "C"):
                # A rename/copy line is "R100\told\tnew" (three fields, and
                # the letter carries a similarity percentage) rather than the
                # two-field "A"/"M"/"D" lines the callers below match on.
                # Treat it as the old path disappearing and the new path
                # appearing, which is what gates 3/7 and the judge's
                # seed_contents actually need to see.
                old_name, new_name = parts[1], parts[2]
                if not _is_build_artifact(old_name):
                    entries.append(("D", old_name))
                if not _is_build_artifact(new_name):
                    entries.append(("A", new_name))
                continue
            name = parts[1]
            if _is_build_artifact(name):
                continue
            entries.append((status, name))
        return entries
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_workspace.py -v`
Expected: PASS (all tests, including the new one and the existing bytecode-exclusion tests).

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/benchmark/agent/workspace.py tests/benchmark/agent/test_workspace.py
git commit -m "$(cat <<'EOF'
fix(agent-bench): handle git rename/copy status lines in workspace diffing - completes plan item #2

git diff --name-status emits three tab-separated fields for a detected
rename/copy (R100\told\tnew), not the two fields A/M/D lines have. The
old single-tab partition() mangled that into one field and matched
neither status check, silently dropping the file from both
new_files_since_baseline() and modified_or_deleted_since_baseline()
whenever diff.renames is enabled.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 3: workspace.py — seed_hidden() reads the configured tests_dir

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/workspace.py:31-38`
- Test: `modelman/tests/benchmark/agent/test_workspace.py`

**Interfaces:**
- Consumes: `TaskBundle.gates_config["build"]["tests_dir"]` (already read the same way by `gates.py`'s `_run_hidden_tests`/`_detect_and_evaluate_gate8`/`evaluate`).
- Produces: `Workspace.seed_hidden(task)` keeps its existing signature.

**Finding:** `Workspace.seed_hidden()` hardcodes the destination as `self.root / "tests"` instead of using the task's configured `gates_config["build"]["tests_dir"]`, which every other consumer (`gates.py`) reads dynamically. Both shipped task bundles happen to use `"tests"` today, so it's currently silent, but a future bundle setting a different `tests_dir` seeds hidden tests into the wrong directory while gate 9 builds its dotted module name from the configured `tests_dir` — the import fails and the row is misreported as `ALL_HIDDEN_FAILING` instead of the harness config bug it actually is.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/agent/test_workspace.py`:

```python
import dataclasses


def test_seed_hidden_respects_a_configured_tests_dir_other_than_tests():
    """seed_hidden() must read gates_config's tests_dir the same way gates.py
    does, not hardcode "tests". Both shipped bundles happen to use "tests"
    today, so a hardcoded destination is silent right up until a bundle
    configures a different regression directory — then hidden tests land in
    the wrong place while gate 9 looks for them, by dotted module name, under
    the configured tests_dir, and the row is misreported as
    ALL_HIDDEN_FAILING instead of the harness config bug it actually is."""
    task = _task()
    custom = dataclasses.replace(
        task,
        gates_config={
            **task.gates_config,
            "build": {**task.gates_config["build"], "tests_dir": "regression_tests"},
        },
    )
    ws = create_workspace(task)
    try:
        ws.seed_hidden(custom)
        assert (ws.root / "regression_tests" / "test_hidden.py").exists()
        assert not (ws.root / "tests" / "test_hidden.py").exists()
    finally:
        destroy_workspace(ws)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_workspace.py::test_seed_hidden_respects_a_configured_tests_dir_other_than_tests -v`
Expected: FAIL — `regression_tests/test_hidden.py` does not exist because `seed_hidden` copied into the hardcoded `tests/` instead.

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/agent/workspace.py`, change `seed_hidden`:

```python
    def seed_hidden(self, task: TaskBundle) -> None:
        """Copy hidden/'s contents into the bundle's configured tests_dir,
        joining the visible test package so unittest's dotted module names
        resolve. Called only after the agent run has already finished —
        hidden tests must never be visible during the run itself."""
        if not task.hidden_dir.is_dir():
            return
        tests_dir = task.gates_config["build"]["tests_dir"]
        shutil.copytree(task.hidden_dir, self.root / tests_dir, dirs_exist_ok=True)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_workspace.py -v`
Expected: PASS (all tests, including the existing `test_seed_hidden_copies_hidden_files_into_tests_package`, which still uses the default `"tests"` tests_dir from the fixture).

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/benchmark/agent/workspace.py tests/benchmark/agent/test_workspace.py
git commit -m "$(cat <<'EOF'
fix(agent-bench): seed_hidden() reads the configured tests_dir - completes plan item #3

seed_hidden() hardcoded "tests" as the seeding destination instead of
reading gates_config["build"]["tests_dir"] the way gates.py's own gate
9/gate 8 code already does. A bundle configuring a different tests_dir
would silently seed hidden tests into the wrong directory.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 4: report.py — write run.toml atomically

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/report.py:1-12,130-136`
- Test: `modelman/tests/benchmark/agent/test_report.py`

**Interfaces:**
- Consumes: `modelman._toml_io.atomic_write_toml(payload: dict[str, Any], path: Path) -> None` (already used by `registry.py`/`state.py` for the same crash-safety reason).
- Produces: `write_run_toml(path, suite_dict, *, git_sha, pi_version) -> None` keeps its existing signature.

**Finding:** `write_run_toml()` writes `run.toml` directly (`path.open("wb")` + `tomli_w.dump`) instead of reusing the repo's existing `atomic_write_toml()` helper (temp file + `os.replace`) that `registry.py`/`state.py` use for exactly this crash-safety reason. A crash or kill mid-write leaves a truncated `run.toml`; `rejudge_run()` later does `tomllib.load()` on it and raises, making a previously-completed run's data un-rejudgeable even though every row's own artifacts are intact.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/agent/test_report.py` (also add `import pytest` to the file's existing imports at the top):

```python
def test_write_run_toml_leaves_an_existing_file_untouched_on_failure(tmp_path, monkeypatch):
    """A crash mid-write must never leave a truncated run.toml — rejudge_run()
    later does tomllib.load() on this file, and a partial write there makes a
    previously-completed run's data un-rejudgeable even though every row's own
    artifacts are intact. Simulates the crash by making the TOML encoder
    raise partway through, after the file would already be open for writing
    under the old direct-write implementation."""
    import modelman._toml_io as toml_io_module

    path = tmp_path / "run.toml"
    path.write_text("previous contents\n", encoding="utf-8")

    def _boom(payload, f):
        raise OSError("disk full")

    monkeypatch.setattr(toml_io_module.tomli_w, "dump", _boom)
    with pytest.raises(OSError):
        write_run_toml(path, {"judge": {"model": "x"}}, git_sha="abc123", pi_version="1.2.3")
    assert path.read_text(encoding="utf-8") == "previous contents\n"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_report.py::test_write_run_toml_leaves_an_existing_file_untouched_on_failure -v`
Expected: FAIL — the direct `path.open("wb")` truncates `run.toml` to empty before `tomli_w.dump` raises, so the file no longer contains `"previous contents\n"`.

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/agent/report.py`, remove the `import tomli_w` line (no longer used directly in this file) and add:

```python
from modelman._toml_io import atomic_write_toml
```

Then replace `write_run_toml`:

```python
def write_run_toml(path: Path, suite_dict: dict[str, Any], *, git_sha: str, pi_version: str) -> None:
    payload = {
        "run": {"git_sha": git_sha, "pi_version": pi_version},
        "suite": _mask_keys(suite_dict),
    }
    atomic_write_toml(payload, path)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_report.py -v`
Expected: PASS (all tests, including the existing `test_write_run_toml_masks_api_keys`, since `atomic_write_toml` still calls `tomli_w.dump` with the same payload shape).

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/benchmark/agent/report.py tests/benchmark/agent/test_report.py
git commit -m "$(cat <<'EOF'
fix(agent-bench): write run.toml atomically - completes plan item #4

write_run_toml() wrote directly via path.open("wb") + tomli_w.dump
instead of reusing the repo's existing atomic_write_toml() helper
(temp file + os.replace) that registry.py/state.py already use for
this exact crash-safety reason. A crash mid-write left a truncated
run.toml that rejudge_run() could no longer parse.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 5: isolation.py + runner.py — single source of truth for isolatable providers

**Files:**
- Modify: `modelman/src/modelman/benchmark/isolation.py`
- Modify: `modelman/src/modelman/benchmark/agent/runner.py:17,418-423`
- Modify: `CLAUDE.md:49` (root, "Adding a New Benchmark Backend" playbook)
- Test: `modelman/tests/benchmark/test_isolation.py`

**Interfaces:**
- Consumes: nothing new.
- Produces: `modelman.benchmark.isolation.SUPPORTED_PROVIDER_IDS: frozenset[str]` — the new single source of truth; `runner.py`'s `ISOLATABLE_PROVIDERS` becomes an alias for it instead of a hand-rebuilt set.

**Finding:** `ISOLATABLE_PROVIDERS` hardcodes `{"omlx-6bit"}` as a one-off addition to the registry-derived `DEFAULT_PROVIDER_IDS`, duplicating (without deriving from) `bin/llm-isolate-provider`'s own supported-provider list, which is the actual source of truth. A 5th isolatable backend added per this repo's own root-CLAUDE.md "Adding a New Benchmark Backend" playbook (which never mentions this set) is silently skipped for isolation in the agent benchmark — rows for it run without isolation and produce distorted results with no error raised.

This task centralizes the known-isolatable-provider list in `isolation.py` (the module that already owns the subprocess contract with `bin/llm-isolate-provider`) as an explicit constant, so `runner.py` no longer reconstructs it from an unrelated registry constant plus a one-off addition, and adds it to the playbook as the thing to update alongside the shell script's own case statement.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/test_isolation.py`:

```python
from modelman.benchmark import isolation


def test_supported_provider_ids_matches_llm_isolate_providers_documented_list():
    """bin/llm-isolate-provider's own header comment ("Supported: ollama,
    llamacpp, omlx, omlx-6bit") is the real source of truth for what this
    helper can isolate. This constant is the one place modelman code checks
    isolability, so a 5th backend added to the shell script's case statement
    without updating this set is caught here rather than silently running
    unisolated in modelman benchmark agent."""
    assert isolation.SUPPORTED_PROVIDER_IDS == frozenset({"ollama", "llamacpp", "omlx", "omlx-6bit"})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/test_isolation.py::test_supported_provider_ids_matches_llm_isolate_providers_documented_list -v`
Expected: FAIL with `AttributeError: module 'modelman.benchmark.isolation' has no attribute 'SUPPORTED_PROVIDER_IDS'`.

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/isolation.py`, add near the top (after the imports, before `IsolateResult`):

```python
# What bin/llm-isolate-provider can actually isolate, mirroring that script's
# own "Supported:" header comment — the shell script is the real source of
# truth (it owns the case statement), and this constant is the one place
# modelman code checks isolability, kept here next to the rest of the
# subprocess contract with that script rather than rebuilt ad hoc elsewhere.
SUPPORTED_PROVIDER_IDS: frozenset[str] = frozenset({"ollama", "llamacpp", "omlx", "omlx-6bit"})
```

In `modelman/src/modelman/benchmark/agent/runner.py`, change the import on line 17:

```python
from modelman.registry import Registry
```

(removing `DEFAULT_PROVIDER_IDS`, which is no longer used anywhere in this file), and replace lines 418-423:

```python
# What bin/llm-isolate-provider can actually isolate — see
# modelman.benchmark.isolation.SUPPORTED_PROVIDER_IDS, the single source of
# truth this set now aliases instead of rebuilding from an unrelated
# registry constant plus a one-off addition.
ISOLATABLE_PROVIDERS = isolation.SUPPORTED_PROVIDER_IDS
```

`isolation` is already imported at the top of `runner.py` (`from modelman.benchmark import isolation`), so no new import is needed there.

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/test_isolation.py tests/benchmark/agent/test_runner.py -v`
Expected: PASS (all tests, including the existing `test_omlx_6bit_override_still_isolates`, `test_run_suite_never_isolates_a_cloud_provider`, and `test_run_suite_still_isolates_local_rows_once`, since `ISOLATABLE_PROVIDERS`'s contents are unchanged — only where it's defined moved).

- [ ] **Step 5: Update the root playbook and commit**

Edit `CLAUDE.md` (repo root), changing line 49 from:

```
4. modelman side: add a provider entry to `~/.config/local-ai/registry.toml` (via `modelman sync` or the TUI) and confirm the provider id is in `LOCAL_PROVIDERS` in `modelman/src/modelman/benchmark/runner.py` — a backend missing from that set is silently skipped by `modelman benchmark`
```

to:

```
4. modelman side: add a provider entry to `~/.config/local-ai/registry.toml` (via `modelman sync` or the TUI) and confirm the provider id is in `LOCAL_PROVIDERS` in `modelman/src/modelman/benchmark/runner.py` — a backend missing from that set is silently skipped by `modelman benchmark`
5. If the new backend should also be isolatable for `modelman benchmark agent`, add it to `SUPPORTED_PROVIDER_IDS` in `modelman/src/modelman/benchmark/isolation.py` — a backend missing from that set runs unisolated with no error
```

(renumber the existing step 5, "Smoke test...", to step 6).

```bash
cd modelman
git add src/modelman/benchmark/isolation.py src/modelman/benchmark/agent/runner.py tests/benchmark/test_isolation.py
git add ../CLAUDE.md
git commit -m "$(cat <<'EOF'
fix(agent-bench): centralize the isolatable-provider list in isolation.py - completes plan item #5

ISOLATABLE_PROVIDERS in runner.py hardcoded {"omlx-6bit"} as a one-off
addition to the registry-derived DEFAULT_PROVIDER_IDS, duplicating
without deriving from bin/llm-isolate-provider's own supported-provider
list. Moved the constant to isolation.py (which already owns the
subprocess contract with that script) and added a playbook step so a
future backend addition doesn't silently skip isolation.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 6: gates.py — remove dead compute_composite()/score_row()

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/gates.py:235-249`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing — these functions have no caller anywhere in `modelman/` (confirmed by repo-wide grep) and no test references them.

**Finding:** `compute_composite()`/`score_row()` are dead code implementing a stale composite formula (`hidden_ratio*0.60 + judge/100*0.40*cap`) that contradicts the real pipeline's formula (`round(rubric_total * cap)`, used via `judge.apply_cap` in `runner.py`). A future maintainer editing scoring logic finds and edits these functions believing they're live, while the real composite calculation elsewhere in `runner.py` silently diverges — two formulas for the same concept is a correctness trap.

This is a deletion with no behavior change and no new test (there's nothing to regression-test once the misleading code is gone); the existing test suite is the verification.

- [ ] **Step 1: Delete the dead code**

In `modelman/src/modelman/benchmark/agent/gates.py`, delete lines 235-250 (the `compute_composite` and `score_row` functions and the blank lines immediately surrounding them), so the file goes directly from `_detect_and_evaluate_gate8`'s closing `return outcomes` to the `def evaluate(` that follows.

- [ ] **Step 2: Run the full gates test file to verify nothing broke**

Run: `uv run pytest tests/benchmark/agent/test_gates.py tests/benchmark/agent/test_gates_day31_drift.py -v`
Expected: PASS (no test in either file imports `compute_composite` or `score_row` — confirmed by grep before writing this task).

- [ ] **Step 3: Commit**

```bash
cd modelman
git add src/modelman/benchmark/agent/gates.py
git commit -m "$(cat <<'EOF'
fix(agent-bench): remove dead compute_composite()/score_row() - completes plan item #6

These implemented a stale composite formula (hidden_ratio*0.60 +
judge/40*cap) that contradicts the actual pipeline's formula
(round(rubric_total * cap), via judge.apply_cap in runner.py) and had
no caller anywhere in modelman/ — a correctness trap for a future
maintainer who finds and edits them believing they're live.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 7: runner.py — persist judge.json via the cheap path, not a full re-write

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/runner.py:329-348,502-511`
- Test: `modelman/tests/benchmark/agent/test_runner.py`

**Interfaces:**
- Consumes: `report.write_judge_json(row_dir: Path, judge_outcome: JudgeOutcome) -> None` (already exists, already used by `rejudge_run`).
- Produces: a new `_persist_judge_artifact(result: RowRunResult) -> None` in `runner.py`, called only in the post-judge loop.

**Finding:** The post-judge call to `_persist_row_artifacts()` in `run_suite()` re-runs the full `write_row_artifacts()` (re-gzipping the entire event stream and rewriting diff/gates/metrics files unchanged from the first call) just to add `judge.json`, when the existing cheaper `write_judge_json()` (already used by `rejudge_run` for this exact purpose) would do. Every row in a sweep pays for a second ~0.5MB gzip write and four redundant file rewrites purely to persist one new field.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/agent/test_runner.py`:

```python
def test_persist_judge_artifact_does_not_rewrite_the_full_row(tmp_path, monkeypatch):
    """The post-judge persist step must add judge.json through the cheap
    write_judge_json() path (already used by rejudge_run for this exact
    purpose), not by re-running the full write_row_artifacts() — which
    re-gzips the entire event stream and rewrites diff/gates/metrics files
    that did not change, a second time, per row, on every sweep with any
    judge phase at all."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    import modelman.benchmark.agent.report as report_module

    calls = {"write_row_artifacts": 0}
    original = report_module.write_row_artifacts

    def _counting(*args, **kwargs):
        calls["write_row_artifacts"] += 1
        return original(*args, **kwargs)

    monkeypatch.setattr(report_module, "write_row_artifacts", _counting)

    class _FakeJudgeTransport:
        def complete(self, prompt, *, temperature):
            return json.dumps(
                {
                    "scores": {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10},
                    "total": 100, "verdict": "principled_fix", "flags": [], "rationale": "ok",
                }
            )

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        judge_transport_factory=lambda cfg, path: _FakeJudgeTransport(),
    )

    assert calls["write_row_artifacts"] == 1, "write_row_artifacts ran a second time just to add judge.json"
    judge_json = json.loads((results[0].row_dir / "judge.json").read_text(encoding="utf-8"))
    assert judge_json["combined"]["total"] == 100
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_runner.py::test_persist_judge_artifact_does_not_rewrite_the_full_row -v`
Expected: FAIL — `calls["write_row_artifacts"] == 2` (once before judging, once after), not 1.

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/agent/runner.py`, update `_persist_row_artifacts`'s docstring (it is now called only once) and add a new function immediately after it:

```python
def _persist_row_artifacts(result: RowRunResult) -> None:
    """Write a row's artifacts, once, before judging — so a row's raw stream
    survives a judge crash. The post-judge step uses _persist_judge_artifact
    instead, which writes only judge.json."""
    if result.error is not None or result.gates is None or result.metrics is None:
        return
    report.write_row_artifacts(
        result.row_dir,
        events=result.events,
        diff_raw=result.diff_raw,
        gates=result.gates,
        metrics=result.metrics,
        judge_outcome=result.judge,
        seed_contents=result.seed_contents,
        closing_message=result.closing_message,
        label=result.row.label,
        model_id=result.row.model_id,
        thinking=result.row.thinking,
        route=result.row.route,
    )


def _persist_judge_artifact(result: RowRunResult) -> None:
    """Write only judge.json for a row that has just been judged, via the
    same cheap path rejudge_run uses — write_row_artifacts would otherwise
    re-gzip the whole event stream and rewrite diff/gates/metrics files that
    did not change, just to add this one field."""
    if result.error is not None or result.gates is None or result.metrics is None or result.judge is None:
        return
    report.write_judge_json(result.row_dir, result.judge)
```

Then in `run_suite`, change the post-judge loop (around line 510):

```python
    if not skip_judge:
        _judge_all(
            suite, task, results, live_models_path, judge_transport_factory or _build_judge_transport
        )
        for result in results:
            _persist_judge_artifact(result)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_runner.py -v`
Expected: PASS (all tests, including the new one and the existing `test_run_suite_judges_rows_after_restore_and_sets_composite` and `test_rejudge_run_rewrites_judge_json_from_persisted_artifacts`).

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/benchmark/agent/runner.py tests/benchmark/agent/test_runner.py
git commit -m "$(cat <<'EOF'
perf(agent-bench): persist judge.json without rewriting the full row - completes plan item #7

The post-judge persist step called the full write_row_artifacts() a
second time per row purely to add judge.json, re-gzipping the entire
event stream and rewriting unchanged diff/gates/metrics files. Added
_persist_judge_artifact(), which uses the existing write_judge_json()
helper (already used by rejudge_run for this exact purpose) instead.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 8: gates.py — raise on an unmapped gate failure code instead of silently dropping it

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/gates.py:1-14,280-290`
- Test: `modelman/tests/benchmark/agent/test_gates.py`

**Interfaces:**
- Consumes: `modelman.benchmark.errors.BenchmarkError` (already used the same way by `workspace.py`, `suite.py`).
- Produces: `evaluate(...)` keeps its existing signature; `finish()` (a nested closure, unchanged interface) now raises `BenchmarkError` instead of silently filtering out an unrecognized code.

**Finding:** Failure codes are free-floating string literals duplicated across `GATE_NAMES`, `CAP_TABLE`, and each `add()` call site with no single source of truth; `finish()` silently ignores any triggered code not found in `CAP_TABLE` instead of raising. A typo'd code string at a future call site (e.g. `"Broken_Build"` vs `"BROKEN_BUILD"`) silently drops out of the cap computation — the row is capped as if nothing failed instead of erroring loudly, and no existing test asserts code/`CAP_TABLE` consistency.

Note: `VISIBLE_REGRESSION` (gate 5's failure code) is *deliberately* absent from `CAP_TABLE` — gate 5 is diagnostic-only per the spec and never affects the cap — so the fix must not treat that omission as an error; only a code that's in neither `CAP_TABLE` nor this explicit "diagnostic-only, no cap" allowlist should raise.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/agent/test_gates.py`:

```python
from modelman.benchmark.errors import BenchmarkError


def test_finish_raises_on_a_triggered_code_missing_from_cap_table(workspace, monkeypatch):
    """A typo'd failure code at a future call site (e.g. "Broken_Build"
    instead of "BROKEN_BUILD") used to silently drop out of the cap
    computation — the row would be capped as if nothing had failed. Simulate
    the typo by deleting BROKEN_BUILD's real cap-table entry and confirm a
    row that trips gate 4 now raises instead of scoring cap=1.0."""
    import modelman.benchmark.agent.gates as gates_module

    monkeypatch.delitem(gates_module.CAP_TABLE, "BROKEN_BUILD")
    (workspace.root / "pkg" / "__init__.py").write_text("this is not valid python(((", encoding="utf-8")
    with pytest.raises(BenchmarkError, match="BROKEN_BUILD"):
        evaluate(workspace, _task(), _ok_run(), events=_reply_events(), session_file_present=True)


def test_visible_regression_still_does_not_raise_despite_no_cap_table_entry(workspace):
    """VISIBLE_REGRESSION is deliberately absent from CAP_TABLE (gate 5 is
    diagnostic-only per the spec and never affects the cap) — the
    consistency check above must not mistake that intentional omission for a
    typo and start raising on every broken-visible-test row."""
    (workspace.root / "pkg" / "__init__.py").write_text(
        "def add_one(n: int) -> int:\n    return n\n", encoding="utf-8"
    )
    report = _evaluate(workspace)
    assert report.results[4].code == "VISIBLE_REGRESSION"
    assert report.cap == 1.0
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_gates.py::test_finish_raises_on_a_triggered_code_missing_from_cap_table -v`
Expected: FAIL — `evaluate()` returns normally with `report.cap == 1.0` instead of raising, because `finish()`'s `if c in CAP_TABLE` filter silently drops the now-unmapped `BROKEN_BUILD` code.

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/agent/gates.py`, add the import at the top (alongside the existing stdlib imports):

```python
from modelman.benchmark.errors import BenchmarkError
```

Add a module-level constant near `CAP_TABLE`/`GATE_NAMES`:

```python
# Codes that are deliberately absent from CAP_TABLE: gate 5 is diagnostic-only
# per the spec (it never short-circuits and never affects the cap), so
# VISIBLE_REGRESSION triggering must not be mistaken for a call-site typo by
# the consistency check in finish() below.
NO_CAP_CODES = {"VISIBLE_REGRESSION"}
```

Replace `finish()`'s cap computation:

```python
    def finish(short_circuit_code: str | None = None, last_gate_number: int = 9) -> GatesReport:
        """Mark everything after `last_gate_number` unevaluated and apply the
        cap table to whatever fired.

        The cap is recomputed from triggered_codes rather than taken from the
        short-circuit argument alone: NO_REGRESSION_TEST and VACUOUS_TEST cap
        without short-circuiting, so a code-only cap would miss them."""
        if short_circuit_code:
            skipped_from(last_gate_number + 1)
        unmapped = [c for c in report.triggered_codes if c not in CAP_TABLE and c not in NO_CAP_CODES]
        if unmapped:
            raise BenchmarkError(f"gate failure code(s) have no CAP_TABLE entry: {unmapped}")
        report.cap = min([1.0, *[CAP_TABLE[c] for c in report.triggered_codes if c in CAP_TABLE]])
        return report
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_gates.py tests/benchmark/agent/test_gates_day31_drift.py -v`
Expected: PASS (all tests, including both new ones and every existing short-circuit/cap test, since every real call site's code is already in `CAP_TABLE` or `NO_CAP_CODES`).

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/benchmark/agent/gates.py tests/benchmark/agent/test_gates.py
git commit -m "$(cat <<'EOF'
fix(agent-bench): raise on a gate failure code missing from CAP_TABLE - completes plan item #8

finish() silently filtered out any triggered failure code not present
in CAP_TABLE, so a call-site typo (e.g. "Broken_Build" instead of
"BROKEN_BUILD") would cap the row as if nothing had failed instead of
erroring loudly. VISIBLE_REGRESSION is exempted via an explicit
NO_CAP_CODES set, since gate 5 is deliberately diagnostic-only per the
spec and was never meant to have a cap-table entry.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 9: bin/llm-isolate-provider — stop swallowing real stderr diagnostics

**Files:**
- Modify: `bin/llm-isolate-provider:20-30,48-49,55-56`

**Interfaces:**
- Consumes: nothing.
- Produces: a renamed helper, `silence_stdout` (replacing `narrate_on_stderr`), used at the same two call sites.

**Finding:** `narrate_on_stderr()`'s redirection order (`"$@" 1>&2` inside a call already wrapped in `2>/dev/null`) discards the wrapped command's original stderr entirely rather than routing it to the script's real stderr as the preceding comment claims, and the fix is patched at two call sites rather than establishing the JSON-only-stdout invariant script-wide. It happens to still fix the stated bug (stdout narration no longer leaks into the JSON-parsed stdout), but any real stderr diagnostic from `ollama stop`/`omlx stop` (e.g. a permission error) is now silently swallowed instead of surfaced, and a third subprocess call added later reintroduces the same class of bug since the invariant isn't enforced centrally.

There is no pytest harness for `bin/*` scripts in this repo (they're validated by `make lint-shell`, per `CLAUDE.md`); this task is verified via `bash -n`/shellcheck (already run by `make lint-shell`) plus a manual functional check, matching the existing convention for this script (`CLAUDE.md`'s "Smoke test" step).

- [ ] **Step 1: Confirm the current (buggy) behavior manually**

Run this to demonstrate the bug — a fake command's real stderr is currently discarded even though the outer `2>/dev/null` is meant only to suppress its stdout-narration double:

```bash
bash -c '
narrate_on_stderr() { "$@" 1>&2; }
fake_cmd() { echo "stdout narration"; echo "REAL STDERR DIAGNOSTIC" >&2; }
narrate_on_stderr fake_cmd 2>/dev/null
echo "--- nothing above this line should be REAL STDERR DIAGNOSTIC, and it is not printed at all ---"
'
```
Expected: only the `---` line prints — `REAL STDERR DIAGNOSTIC` is swallowed, confirming the bug.

- [ ] **Step 2: Write the minimal implementation**

In `bin/llm-isolate-provider`, replace lines 20-30:

```bash
# This script's stdout is its contract: isolation.py parses it as a single JSON
# object. `omlx stop` shells out to brew, which narrates ("Stopping `omlx`...")
# on *stdout*, so an isolate whose target needed oMLX stopped produced
# prose-then-JSON and the caller died with "isolation helper returned invalid
# JSON" — which, because the stop only happens when the service is up, made
# every other run fail. Discard a command's stdout narration outright rather
# than routing it through this script's own stderr and then redirecting that
# to /dev/null at the call site — that combination discarded the command's
# real stderr too, since both narration-turned-stderr and the command's own
# stderr land on the same fd once redirected. Any future subprocess call that
# narrates on stdout must route through this helper rather than repeating the
# pattern ad hoc.
silence_stdout() {
    "$@" 1>/dev/null
}
```

Then change line 49 from:

```bash
        narrate_on_stderr ollama stop "$OLLAMA_MODEL" 2>/dev/null || true
```

to:

```bash
        silence_stdout ollama stop "$OLLAMA_MODEL" || true
```

And line 56 from:

```bash
        narrate_on_stderr omlx stop 2>/dev/null || true
```

to:

```bash
        silence_stdout omlx stop || true
```

- [ ] **Step 3: Verify the fix removes the swallowing bug**

Run the same repro as Step 1, but with the new function:

```bash
bash -c '
silence_stdout() { "$@" 1>/dev/null; }
fake_cmd() { echo "stdout narration"; echo "REAL STDERR DIAGNOSTIC" >&2; }
silence_stdout fake_cmd
echo "--- REAL STDERR DIAGNOSTIC should have printed above this line ---"
'
```
Expected: `REAL STDERR DIAGNOSTIC` prints (to the terminal's stderr), `stdout narration` does not, confirming both halves of the fix.

- [ ] **Step 4: Lint the script**

Run: `make lint-shell` (from the repo root, `/Users/keith/github/ohanaverse/local-ai-setup`)
Expected: PASS — `bash -n` and `shellcheck --severity=error` both clean on `bin/llm-isolate-provider`.

- [ ] **Step 5: Manual functional smoke test (only if the target hardware/services are available)**

Run: `bin/llm-isolate-provider ollama` (or whichever local backend is currently set up on this machine)
Expected: stdout is exactly one JSON object (`{"provider": "ollama", ...}`); no narration text appears mixed into it. This matches the existing convention in `CLAUDE.md`'s "Smoke test" step for this script — skip if the local backends aren't running on this machine, since `make lint-shell` already covers syntax/lint correctness.

- [ ] **Step 6: Commit**

```bash
git add bin/llm-isolate-provider
git commit -m "$(cat <<'EOF'
fix(agent-bench): stop swallowing real stderr in llm-isolate-provider - completes plan item #9

narrate_on_stderr()'s redirection order ("$@" 1>&2 inside a call
already wrapped in 2>/dev/null) discarded the wrapped command's real
stderr along with its stdout narration, since both ended up on the
same fd once redirected. Renamed to silence_stdout(), which discards
only stdout, so a real diagnostic from `ollama stop`/`omlx stop` now
reaches this script's own stderr instead of being silently dropped.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 10: suite.py — remove the unreachable second [judge].route validation

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/suite.py:155-160`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing — `load_suite`'s signature and behavior are unchanged; only dead code is removed.

**Finding:** `load_suite()` validates `[judge].route` twice — once directly via `judge_raw["route"]` (which already raises for any invalid/missing value), then again via an unreachable `raw.get("judge", {}).get("route", "litellm")` check a few lines later. The second block's error message and default value can never execute — dead code that misleads a reader into thinking a missing `[judge]` table is tolerated with a `"litellm"` default, when in fact it already raised a bare `KeyError` earlier.

This is a deletion with no behavior change; the existing test `test_load_suite_rejects_an_unknown_judge_route` (in `modelman/tests/benchmark/agent/test_suite.py`) already covers the (now sole) first validation block, whose error message still matches that test's `match="judge.*route|route"` regex.

- [ ] **Step 1: Delete the dead code**

In `modelman/src/modelman/benchmark/agent/suite.py`, delete lines 155-160:

```python
    if raw.get("judge", {}).get("route", "litellm") not in JUDGE_ROUTES:
        raise BenchmarkError(
            f"[judge].route = {raw['judge'].get('route')!r} is not one of {JUDGE_ROUTES}. "
            "The field used to be accepted and ignored, which is worse than rejecting it: "
            "a suite asking for a judge the gateway cannot reach scores JUDGE_FAIL on every row."
        )
```

leaving the blank line before `return Suite(` as the only thing between the `judge = JudgeConfig(...)` block and `routes_direct = {...}`.

- [ ] **Step 2: Run the suite test file to verify nothing broke**

Run: `uv run pytest tests/benchmark/agent/test_suite.py -v`
Expected: PASS (all tests, including `test_load_suite_rejects_an_unknown_judge_route`, which is satisfied by the first, still-present validation block).

- [ ] **Step 3: Commit**

```bash
cd modelman
git add src/modelman/benchmark/agent/suite.py
git commit -m "$(cat <<'EOF'
fix(agent-bench): remove unreachable second [judge].route validation - completes plan item #10

load_suite() validated [judge].route twice: once via judge_raw["route"]
(which already raises for any invalid/missing value), then again via
an unreachable raw.get("judge", {}).get("route", "litellm") check that
could never execute — misleading a reader into thinking a missing
[judge] table was tolerated with a "litellm" default.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Bc1M5mtG9iskPXVJyB8Xhq
EOF
)"
```

---

### Task 11: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Run the full modelman check + test suite**

Run (from `modelman/`): `make check && make test`
Expected: PASS — lint, typecheck, and the entire test suite (including every file touched above) all clean.

- [ ] **Step 2: Run the root shell lint**

Run (from the repo root): `make lint-shell`
Expected: PASS — `bin/llm-isolate-provider` (and every other `bin/`/`benchmarks/` script) passes `bash -n` + `shellcheck --severity=error`.

- [ ] **Step 3: Run the root link checker (CLAUDE.md was edited in Task 5)**

Run (from the repo root): `make check-links`
Expected: PASS — no broken repo-relative markdown links introduced by the `CLAUDE.md` edit.

No commit for this task — it is verification of the 10 preceding commits, not a code change.

## Self-Review

**Spec coverage:** All 10 findings from the `/code-review` run map to Tasks 1-10 above, one task each, in the same order the review ranked them. Task 11 adds full-suite verification not present in the original findings list but required by this repo's testing conventions.

**Placeholder scan:** Every step contains real code (exact diffs, not descriptions), real test bodies, and real commands. No "TBD"/"similar to Task N"/"add appropriate handling" placeholders.

**Type consistency:** `Workspace.seed_hidden(self, task: TaskBundle) -> None` (Task 3) keeps its existing signature; `_persist_row_artifacts`/`_persist_judge_artifact` (Task 7) both take `RowRunResult` and return `None`, matching the existing `RowRunResult` dataclass fields (`error`, `gates`, `metrics`, `judge`, `row_dir`) used throughout `runner.py`; `SUPPORTED_PROVIDER_IDS: frozenset[str]` (Task 5) is consumed by `runner.py`'s `ISOLATABLE_PROVIDERS` with no type change (both are set-like collections tested via `in`).
