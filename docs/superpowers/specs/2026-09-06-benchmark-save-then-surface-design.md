# Design — Save-then-surface for the single-turn benchmark runner

Date: 2026-09-06
Issue: #32 — Never discard a completed benchmark run
Status: Approved (Solution 1)

## Problem

In `modelman/src/modelman/benchmark/runner.py`, the single-turn runner calls
`restore_providers()` inside a `finally` block and calls `write_results()` only
*after* it:

```python
try:
    for target in targets:
        ...  # run all passes/routes, append results
finally:
    restore_providers()          # step 1

write_results(run, results_dir)  # step 2 (AFTER restore)
return run
```

If restoring the backends fails (e.g. a LaunchAgent will not start), the
exception propagates out of `finally`, so `write_results` never runs. The CLI's
generic `except BenchmarkError` then exits 1, and a **fully completed, measured
run is discarded**. The `--latest` pointer in state is never updated, so
`show --latest` has no idea the run ever happened.

A completed measurement is more valuable than the housekeeping step that
follows it. The agent-benchmark runner (`agent/runner.py`) already solves this
with a `RunSavedButRestoreFailed` exception that carries `run_dir` + `results`
so the CLI can still record the run and report the row count before exiting
non-zero. The single-turn runner never received the same treatment.

## Goal

If a benchmark workload finishes but provider restore fails, the run file is
still written and a clear error names the surviving directory.

## Design

### 1. New exception in `errors.py`

Add `RunSavedButRestoreFailed(BenchmarkError)` carrying `run_dir: Path` and
the completed `run: BenchmarkRun` (which holds `run_id`, `started_at`, and
`results`), mirroring the agent runner's exception. This is the contract
between `runner.py` and `cli.py`.

### 2. `runner.py` — reorder save before restore

Change the tail of `run_benchmark` from the current `finally`-then-write shape
to:

```python
restore_error: str | None = None
try:
    restore_providers()
except BenchmarkError as exc:
    restore_error = str(exc)

write_results(run, results_dir)          # always persist first

if restore_error is not None:
    raise RunSavedButRestoreFailed(
        f"providers failed to restore after the run (all results were saved "
        f"to {results_dir / run.run_id}): {restore_error}",
        run_dir=results_dir / run.run_id,
        run=run,
    )
return run
```

Key points:

- `write_results` runs **unconditionally** — the run is never discarded.
- Restore failure is captured, not raised immediately.
- The exception carries the run dir + the completed run so the CLI can still
  record `--latest`.

### 3. `cli.py` — catch it, record, then fail loudly

Add a handler before the generic `except BenchmarkError`:

```python
except RunSavedButRestoreFailed as exc:
    _record_latest(exc.run_dir, exc.run)   # update --latest + row count
    typer.echo(f"error: {exc}", err=True)
    raise typer.Exit(1) from None
```

`_record_latest` is a small helper extracted from the success path (which
currently records `last_run` = `run.started_at.isoformat()` and
`last_run_dir` = `str(output_dir)` in `state.extra["benchmarks"]`). Both the
success path and the `RunSavedButRestoreFailed` handler call it, so the
`--latest` pointer is updated identically in both cases. This mirrors the
agent CLI: record the run like any other, then exit non-zero with a message
naming the surviving directory.

### 4. Tests

- **Unit:** a test that forces `restore_providers()` to raise and asserts
  `write_results` still wrote the file and `RunSavedButRestoreFailed` carries
  the right `run_dir`/`results`.
- **CLI:** a test that a `RunSavedButRestoreFailed` results in a non-zero exit
  **and** an updated `--latest` pointer.
- Run `cd modelman && make test` and `make test-all` at root.

## Out of scope

- No change to `llm-restore-providers` failure behavior.
- No new benchmark features.

## Verification

```bash
# Repro: intentionally break a provider restore
launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist
uv run modelman benchmark run --suite <some-short-suite>
# Expect: results file exists, exit non-zero with message naming the run directory.

# Then restore real state and confirm the normal path still works
launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist
PATH=$PWD/bin:$PATH llm-restore-providers
uv run modelman benchmark run --suite <suite>   # exit 0, latest pointer updated
```
