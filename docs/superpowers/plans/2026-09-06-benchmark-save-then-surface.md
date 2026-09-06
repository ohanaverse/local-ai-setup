# Benchmark Save-then-Surface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure a completed single-turn benchmark run is always written to disk, even when provider restore fails, and surface the restore failure loudly with a non-zero exit while still recording the `--latest` pointer.

**Architecture:** Port the agent runner's `RunSavedButRestoreFailed` pattern to the single-turn runner. Reorder `run_benchmark` so `write_results` runs unconditionally before restore; capture the restore error instead of raising immediately; raise a `RunSavedButRestoreFailed` exception carrying the run dir + completed run. The CLI catches it, records `--latest` like a normal run, then exits non-zero.

**Tech Stack:** Python 3.13, Typer CLI, pytest, `modelman` package (`modelman/src/modelman/benchmark/`).

**Spec:** `docs/superpowers/specs/2026-09-06-benchmark-save-then-surface-design.md`

---

## File Structure

- `modelman/src/modelman/benchmark/runner.py` — define `RunSavedButRestoreFailed`; reorder `run_benchmark` tail to save-then-surface.
- `modelman/src/modelman/benchmark/cli.py` — import the exception; add a handler that records `--latest` then exits non-zero; extract `_record_latest` helper.
- `modelman/tests/benchmark/test_runner.py` — add tests for the exception and the save-then-surface behavior.
- `modelman/tests/benchmark/test_cli.py` — add a test that a restore-failed run still records `--latest` and exits non-zero.

> **Note on placement:** The spec said to add the exception to `errors.py`, but the existing agent pattern defines `RunSavedButRestoreFailed` in `agent/runner.py` (not `errors.py`). To stay consistent with the codebase and avoid coupling `errors.py` to `results.py` (for the `BenchmarkRun` type), we define it in `runner.py` instead. `cli.py` already imports from `runner.py`, so this is the natural home.

---

## Task 1: Define `RunSavedButRestoreFailed` in `runner.py`

**Files:**
- Modify: `modelman/src/modelman/benchmark/runner.py` (add exception class after the imports, before `LOCAL_PROVIDERS`)
- Test: `modelman/tests/benchmark/test_runner.py`

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/test_runner.py`:

```python
from pathlib import Path

import pytest

from modelman.benchmark.runner import RunSavedButRestoreFailed


def test_run_saved_but_restore_failed_carries_run_dir_and_run():
    """The exception must carry the surviving run dir and the completed run so
    the CLI can still record the --latest pointer after a restore failure."""
    run = object()
    exc = RunSavedButRestoreFailed("boom", run_dir=Path("/tmp/x"), run=run)
    assert exc.run_dir == Path("/tmp/x")
    assert exc.run is run
    assert isinstance(exc, Exception)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/benchmark/test_runner.py::test_run_saved_but_restore_failed_carries_run_dir_and_run -v`
Expected: FAIL with `ImportError: cannot import name 'RunSavedButRestoreFailed'`

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/runner.py`, after the `from modelman.state import StateStore` import and before `LOCAL_PROVIDERS`, add:

```python
class RunSavedButRestoreFailed(BenchmarkError):
    """Every row completed and is on disk; only putting the backends back failed.

    Carries `run_dir` and the completed `run` so the CLI can still record the
    `--latest` pointer and report the row count. Without it, a host whose
    provider restore fails turns a finished, fully persisted sweep into an exit
    code with nothing to show for it, and `show-results --latest` has no idea
    the run ever happened."""

    def __init__(self, message: str, *, run_dir: Path, run: BenchmarkRun) -> None:
        super().__init__(message)
        self.run_dir = run_dir
        self.run = run
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/benchmark/test_runner.py::test_run_saved_but_restore_failed_carries_run_dir_and_run -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/runner.py modelman/tests/benchmark/test_runner.py
git commit -m "feat(benchmark): add RunSavedButRestoreFailed exception"
```

---

## Task 2: Reorder `run_benchmark` to save-then-surface

**Files:**
- Modify: `modelman/src/modelman/benchmark/runner.py` (tail of `run_benchmark`)
- Test: `modelman/tests/benchmark/test_runner.py`

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/test_runner.py`:

```python
from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.results import BenchmarkMetrics, TargetResult
from modelman.benchmark.runner import RunSavedButRestoreFailed, run_benchmark
from modelman.benchmark.workloads.base import WorkloadSpec
from modelman.registry import ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore


class _FakeWorkload:
    spec = WorkloadSpec(
        name="chat", display_name="Chat", prompt="hi",
        max_tokens=1, temperature=0.0, stream=True,
    )

    def build_payload(self, model_id):
        return {"model": model_id}

    def run(self, session, url, payload):
        raise AssertionError("workload.run must not be called (route is faked)")

    def metrics(self, raw):
        return BenchmarkMetrics(ttft_ms=1, total_ms=2, completion_tokens=3, prompt_tokens=4)


def test_run_benchmark_saves_results_when_restore_fails(tmp_path, monkeypatch):
    """A completed run must be written to disk even when restore_providers
    raises, and the failure must surface as RunSavedButRestoreFailed carrying
    the run dir and the completed run."""
    import modelman.benchmark.runner as runner_module

    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(litellm_exposed=True))

    def _fake_isolate(pid):
        return type("I", (), {"ok": True, "direct_url": "http://localhost:8080"})()

    def _fail_restore():
        raise BenchmarkError("llamacpp down")

    def _fake_route(session, target, route, url, workload, pass_number):
        return TargetResult(
            model_id=target.model_id,
            provider_id=target.provider_id,
            route=route,
            pass_number=pass_number,
            metrics=BenchmarkMetrics(ttft_ms=1, total_ms=2, completion_tokens=3, prompt_tokens=4),
            error=None,
        )

    monkeypatch.setattr(runner_module, "isolate_provider", _fake_isolate)
    monkeypatch.setattr(runner_module, "restore_providers", _fail_restore)
    monkeypatch.setattr(runner_module, "_run_route", _fake_route)

    with pytest.raises(RunSavedButRestoreFailed) as excinfo:
        run_benchmark(registry, state, _FakeWorkload(), results_dir=tmp_path)

    exc = excinfo.value
    assert exc.run_dir == tmp_path / exc.run.run_id
    assert (exc.run_dir / "results.json").exists()
    assert len(exc.run.results) == 2  # direct + litellm routes
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/benchmark/test_runner.py::test_run_benchmark_saves_results_when_restore_fails -v`
Expected: FAIL — currently `restore_providers()` raises inside `finally` before `write_results`, so `RunSavedButRestoreFailed` is never raised and `results.json` is never written.

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/runner.py`, replace the tail of `run_benchmark`:

```python
    finally:
        restore_providers()

    write_results(run, results_dir)
    return run
```

with:

```python
    restore_error: str | None = None
    try:
        restore_providers()
    except BenchmarkError as exc:
        restore_error = str(exc)

    write_results(run, results_dir)

    if restore_error is not None:
        raise RunSavedButRestoreFailed(
            f"providers failed to restore after the run (all results were saved "
            f"to {results_dir / run.run_id}): {restore_error}",
            run_dir=results_dir / run.run_id,
            run=run,
        )
    return run
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/benchmark/test_runner.py::test_run_benchmark_saves_results_when_restore_fails -v`
Expected: PASS

- [ ] **Step 5: Run the full benchmark test suite**

Run: `cd modelman && uv run pytest tests/benchmark/ -q`
Expected: all pass (including the existing `test_discover_targets_*` tests).

- [ ] **Step 6: Commit**

```bash
git add modelman/src/modelman/benchmark/runner.py modelman/tests/benchmark/test_runner.py
git commit -m "feat(benchmark): save results before surfacing restore failure"
```

---

## Task 3: CLI — record `--latest`, then fail loudly

**Files:**
- Modify: `modelman/src/modelman/benchmark/cli.py`
- Test: `modelman/tests/benchmark/test_cli.py`

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/test_cli.py`:

```python
from pathlib import Path
from unittest.mock import patch

from modelman.benchmark.results import BenchmarkRun
from modelman.benchmark.runner import RunSavedButRestoreFailed


def test_run_records_latest_even_when_restore_failed(tmp_path):
    """A restore-failed run still records the --latest pointer and exits
    non-zero, so show-results --latest can find the surviving run."""
    run_dir = tmp_path / "20260905-143200"
    run = BenchmarkRun(
        run_id="20260905-143200",
        workload_name="chat",
        started_at=datetime(2026, 8, 28, 14, 32, 0, tzinfo=UTC),
        results=[],
    )

    def _raise(*args, **kwargs):
        raise RunSavedButRestoreFailed(
            f"providers failed to restore (saved to {run_dir}): llamacpp down",
            run_dir=run_dir,
            run=run,
        )

    with (
        patch("modelman.benchmark.cli.load_registry"),
        patch("modelman.benchmark.cli.load_state") as mock_state,
        patch("modelman.benchmark.cli.save_state") as mock_save,
        patch("modelman.benchmark.cli.run_benchmark", _raise),
    ):
        state = mock_state.return_value
        state.extra = {}

        runner = CliRunner()
        result = runner.invoke(
            app,
            ["benchmark", "run", "--results-dir", str(tmp_path)],
        )
        assert result.exit_code == 1
        assert "llamacpp down" in result.output
        assert mock_save.called
        assert state.extra["benchmarks"]["last_run_dir"] == str(run_dir)
```

Add the needed imports at the top of `test_cli.py` (the file currently only imports `CliRunner` and `app`):

```python
from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from modelman.benchmark.results import BenchmarkRun
from modelman.benchmark.runner import RunSavedButRestoreFailed
from modelman.main import app
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/benchmark/test_cli.py::test_run_records_latest_even_when_restore_failed -v`
Expected: FAIL — the CLI's generic `except BenchmarkError` catches the exception and exits 1 without recording `last_run_dir`.

- [ ] **Step 3: Write minimal implementation**

In `modelman/src/modelman/benchmark/cli.py`:

1. Update the import from `runner`:

```python
from modelman.benchmark.runner import (
    DEFAULT_RESULTS_DIR,
    RunSavedButRestoreFailed,
    run_benchmark,
)
```

2. Extract the `--latest` recording into a helper. Replace the success-path recording block:

```python
    # Record latest run pointer in state.
    output_dir = (results_dir or DEFAULT_RESULTS_DIR) / run.run_id
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["last_run"] = run.started_at.isoformat()
    benchmarks["last_run_dir"] = str(output_dir)
    save_state(state)

    typer.echo(f"Benchmark complete: {run.run_id}")
    typer.echo(f"Results: {output_dir}")
```

with:

```python
    _record_latest(run, results_dir or DEFAULT_RESULTS_DIR)

    typer.echo(f"Benchmark complete: {run.run_id}")
    typer.echo(f"Results: {(results_dir or DEFAULT_RESULTS_DIR) / run.run_id}")
```

3. Add the `RunSavedButRestoreFailed` handler before the generic `except BenchmarkError`:

```python
    except RunSavedButRestoreFailed as exc:
        _record_latest(exc.run, exc.run_dir.parent)
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from None
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
```

4. Add the helper function at module level (after the imports):

```python
def _record_latest(run: BenchmarkRun, results_dir: Path) -> None:
    """Record the --latest pointer for a completed run in state."""
    state = load_state()
    output_dir = results_dir / run.run_id
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["last_run"] = run.started_at.isoformat()
    benchmarks["last_run_dir"] = str(output_dir)
    save_state(state)
```

> **Note:** `_record_latest` calls `load_state()`/`save_state()` itself, so the existing `state = load_state()` call in `run_cmd` is no longer needed for the recording path. Leave the earlier `state = load_state()` line in place only if it is used elsewhere in the function; if it is only used for recording, remove it. Verify by reading the full function before editing.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/benchmark/test_cli.py::test_run_records_latest_even_when_restore_failed -v`
Expected: PASS

- [ ] **Step 5: Run the full benchmark test suite**

Run: `cd modelman && uv run pytest tests/benchmark/ -q`
Expected: all pass.

- [ ] **Step 6: Run the full modelman suite**

Run: `cd modelman && make test`
Expected: all pass (597 existing tests + new ones).

- [ ] **Step 7: Commit**

```bash
git add modelman/src/modelman/benchmark/cli.py modelman/tests/benchmark/test_cli.py
git commit -m "feat(benchmark): record latest pointer and fail loudly on restore failure"
```

---

## Task 4: Full verification

**Files:** none (verification only)

- [ ] **Step 1: Run the repo-wide gate**

Run: `make test-all` at repo root
Expected: lint + modelman tests + wt build/vet/test all pass.

- [ ] **Step 2: Manual smoke test (optional, requires live providers)**

The llama.cpp provider was retired 2026-09-07; use an active local provider such as Ollama for these steps. Verify the target provider is currently running and can be started/stopped cleanly on your machine.

```bash
# Break a provider restore, then confirm the run is still saved and exits non-zero.
ollama stop <model>
PATH=$PWD/bin:$PATH uv run modelman benchmark run --model <model>
# Expect: results file exists, exit non-zero with message naming the run directory.

# Restore real state and confirm the normal path still works.
PATH=$PWD/bin:$PATH llm-restore-providers
PATH=$PWD/bin:$PATH uv run modelman benchmark run --model <model>   # exit 0, latest pointer updated
```

- [ ] **Step 3: Commit any remaining changes**

```bash
git status
git add -A
git commit -m "chore: final verification"
```

---

## Self-Review Notes

- **Spec coverage:** The spec's four sections map to Tasks 1 (exception), 2 (reorder), 3 (CLI handler + `_record_latest`), and 4 (tests/verification). The spec's `_record_latest(exc.run_dir, exc.run)` signature was refined to `_record_latest(run, results_dir)` so the helper can derive `run_dir` from `run.run_id` and the results dir — consistent with the success path.
- **Type consistency:** `RunSavedButRestoreFailed` carries `run_dir: Path` and `run: BenchmarkRun` everywhere. `_record_latest` takes `(run: BenchmarkRun, results_dir: Path)` and is called identically from both the success path and the handler.
- **Placeholder scan:** No TBD/TODO. The `<some-short-suite>`/`<suite>` in Task 4 are illustrative command arguments, not requirements.
