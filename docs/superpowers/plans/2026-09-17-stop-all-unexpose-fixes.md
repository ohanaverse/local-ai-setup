# stop --all Un-expose Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix four `/code-review high` findings on the `modelman-local-model-lifecycle-confirm` branch's un-expose-on-stop work: `stop --all` silently drops unexpose failures, a stale-snapshot race can clobber a concurrent `exposed` write, the TUI's self-heal loop leaves the EXPOSED column stale, and `stop --all` bounces the LiteLLM proxy once per exposed model instead of once total.

**Architecture:** Add one new batched-unexpose primitive to `litellm.py` (`apply_unexpose_queue`, the un-expose counterpart to the existing `apply_expose_queue`) that does a single config load/save/restart for a whole id list. Point `local_control.py`'s `stop_all_local_models()` at it, changing its return type from a bare `list[str]` to a small `StopAllResult` dataclass (mirroring `StopResult`) so `main.py` can surface warnings the same way the single-model stop path already does. Fix the stale-snapshot race in both `stop_all_local_models()` and `_clear_stale_running_flag()` by falling back to the freshly-locked value (not the pre-attempt snapshot) when an unexpose fails — matching the pattern `stop_local_model()` already uses. Fix the TUI's on-mount reconcile self-heal loop (`screens/models.py::_run_reconcile`) to pick up the `exposed=False` that `_clear_stale_running_flag()` already persisted to disk, instead of leaving the in-memory copy stale.

**Tech Stack:** Python 3.13, Typer CLI, Textual TUI, pytest/pytest-asyncio.

**Spec:** No separate spec doc — this plan implements the four findings from the `/code-review high` report on commits `9d8962d` (un-expose on stop) and `ea8a26a` (always-confirm + busy-guard), inline in this plan.

## Global Constraints

- Every new/changed function keeps the existing "best-effort, never raise past a stop" contract described in `modelman/CLAUDE.md`'s "Local-model lifecycle" section — a failed unexpose degrades to a warning, it never blocks `stop`/`stop --all`/the TUI's `s` stop action.
- Run only the test files that exercise touched code per task (`modelman/CLAUDE.md`'s testing guidance); run `make check` and the full `make test` once, in the final task.
- `git grep -n "exposed = " docs/guides/` before and after — this work does not change any model's actual exposure state or the guides' embedded snapshots, but the CLAUDE.md gotcha says to check on *any* modelman state change, so confirm no drift as a final check.

---

## File Structure

- Modify `modelman/src/modelman/litellm.py` — add `apply_unexpose_queue()` next to `apply_expose_queue()`.
- Modify `modelman/src/modelman/local_control.py` — `stop_all_local_models()` (use the new batched helper, return `StopAllResult`), `_clear_stale_running_flag()` (fix the stale-snapshot race).
- Modify `modelman/src/modelman/main.py` — `stop --all` branch reads `StopAllResult.stopped`/`.warnings`.
- Modify `modelman/src/modelman/screens/models.py` — `_run_reconcile()`'s self-heal loop re-reads `exposed` from disk for models it just cleared.
- Modify `modelman/tests/test_litellm.py` — new tests for `apply_unexpose_queue`.
- Modify `modelman/tests/test_local_control.py` — update existing `stop_all_local_models` tests for the new return type, add new tests for warnings-on-failure, the batched-restart count, and the race fix (both `stop_all_local_models` and `_clear_stale_running_flag`).
- Modify `modelman/tests/commands/test_local_control.py` — new CLI wiring test: `modelman stop --all` prints a warning when unexpose fails.
- Modify `modelman/tests/screens/test_app_navigation.py` — new pilot test: on-mount reconcile self-heal clears EXPOSED, not just RUNNING.

---

### Task 1: `apply_unexpose_queue()` in litellm.py

**Files:**
- Modify: `modelman/src/modelman/litellm.py` (add function after `apply_expose_queue`, currently ending at line 747)
- Test: `modelman/tests/test_litellm.py`

**Interfaces:**
- Consumes: `load_litellm_config`, `save_litellm_config`, `remove_exposed`, `ensure_litellm_settings`, `_set_exposed_flag`, `restart_litellm_proxy` (all already defined earlier in `litellm.py`); `StateStore` from `modelman.state`.
- Produces: `apply_unexpose_queue(state: StateStore, model_ids: list[str], litellm_path: Path) -> list[str]` — used by Task 2's `stop_all_local_models()`. Returns proxy-restart warnings (empty list on success/no-op). Raises `LiteLLMConfigError`/`OSError` on a config-level failure (missing/unwritable file), same as `apply_expose_queue`.

- [x] **Step 1: Write the failing tests**

Add to `modelman/tests/test_litellm.py` (near the existing `apply_expose_queue`-adjacent tests — search the file for `def test_unexpose_model` to place these nearby):

```python
def test_apply_unexpose_queue_restarts_once_for_multiple_models(tmp_path, monkeypatch):
    # `stop --all` must bounce the LiteLLM proxy once for the whole batch,
    # not once per exposed model being stopped — this is the un-expose
    # counterpart to test_apply_expose_queue_restarts_once_when_applied.
    from modelman.litellm import apply_unexpose_queue, save_litellm_config
    from modelman.state import ModelState, StateStore

    state = StateStore()
    state.set("ollama/a", ModelState(exposed=True))
    state.set("ollama/b", ModelState(exposed=True))
    path = tmp_path / "config.yaml"
    save_litellm_config(
        {
            "model_list": [
                {"model_name": "ollama/a", "litellm_params": {"model": "ollama/a"}},
                {"model_name": "ollama/b", "litellm_params": {"model": "ollama/b"}},
            ],
            "general_settings": {},
        },
        path,
    )

    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    warnings = apply_unexpose_queue(state, ["ollama/a", "ollama/b"], path)

    assert calls == ["restart"]
    assert warnings == []
    assert state.get("ollama/a").exposed is False
    assert state.get("ollama/b").exposed is False
    from modelman.litellm import load_litellm_config

    assert load_litellm_config(path)["model_list"] == []


def test_apply_unexpose_queue_no_restart_when_empty(tmp_path, monkeypatch):
    # An empty batch (e.g. stop --all with nothing exposed) must not touch
    # the config file or bounce the proxy at all.
    from modelman.litellm import apply_unexpose_queue
    from modelman.state import StateStore

    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    warnings = apply_unexpose_queue(StateStore(), [], tmp_path / "config.yaml")
    assert warnings == []
    assert calls == []


def test_apply_unexpose_queue_propagates_config_failure(tmp_path):
    # A missing/unwritable config file is a config-level failure, not a
    # per-model one — it must propagate so the caller (stop_all_local_models)
    # can turn it into a single warning covering the whole batch.
    from modelman.litellm import LiteLLMConfigError, apply_unexpose_queue
    from modelman.state import ModelState, StateStore

    state = StateStore()
    state.set("ollama/a", ModelState(exposed=True))
    with pytest.raises(LiteLLMConfigError):
        apply_unexpose_queue(state, ["ollama/a"], tmp_path / "does-not-exist.yaml")
    # A failed batch must not flip the flag — the config was never touched.
    assert state.get("ollama/a").exposed is True
```

Check the top of `modelman/tests/test_litellm.py` for an existing `import pytest` — add it if missing (needed for `pytest.raises` in the third test).

- [x] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/test_litellm.py -k apply_unexpose_queue -v`
Expected: FAIL with `ImportError: cannot import name 'apply_unexpose_queue'`

- [x] **Step 3: Implement `apply_unexpose_queue`**

In `modelman/src/modelman/litellm.py`, add immediately after `apply_expose_queue` (after the line `return outcomes, []` that currently ends that function, before the `# Canonical restart command...` comment block):

```python
def apply_unexpose_queue(
    state: StateStore,
    model_ids: list[str],
    litellm_path: Path,
) -> list[str]:
    """Un-expose every id in `model_ids` with a single config load and a
    single atomic save — the un-expose counterpart to apply_expose_queue,
    used by stop_all_local_models() so stopping N exposed models bounces
    the LiteLLM proxy once instead of N times.

    Unlike apply_expose_queue, remove_exposed() never validates against
    the registry and cannot fail per-model, so this takes no Registry and
    has no per-item outcome to report: it either applies the whole batch
    or raises. Config-level failures (missing/unwritable file) propagate
    to the caller, same as apply_expose_queue and unexpose_model — the
    caller decides how to turn that into a warning. Flags flip only after
    the save succeeds, so state never claims an un-exposure the config
    file lost.

    Returns proxy-restart warnings (empty on success or when nothing
    changed), matching apply_expose_queue's warnings contract.
    """
    if not model_ids:
        return []
    config = load_litellm_config(litellm_path)
    before = copy.deepcopy(config)
    for model_id in model_ids:
        remove_exposed(config, model_id)
    ensure_litellm_settings(config)
    changed = config != before
    if changed:
        save_litellm_config(config, litellm_path)
    flags_changed = False
    for model_id in model_ids:
        if _set_exposed_flag(state, model_id, False):
            flags_changed = True
    if changed or flags_changed:
        return restart_litellm_proxy()
    return []
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd modelman && uv run pytest tests/test_litellm.py -k apply_unexpose_queue -v`
Expected: PASS (3 passed)

- [x] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/src/modelman/litellm.py modelman/tests/test_litellm.py
git commit -m "$(cat <<'EOF'
feat(modelman): add apply_unexpose_queue for batched un-expose

Un-expose counterpart to apply_expose_queue — one config load/save/
restart for a whole id list instead of one per model.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Batch `stop_all_local_models()` through `apply_unexpose_queue`, add `StopAllResult`, fix the stale-snapshot race

**Files:**
- Modify: `modelman/src/modelman/local_control.py:1076-1101` (`stop_all_local_models`), and the `StopResult` dataclass area (`modelman/src/modelman/local_control.py:129-134`) to add `StopAllResult`
- Modify: `modelman/src/modelman/local_control.py:49-55` (import `apply_unexpose_queue`)
- Test: `modelman/tests/test_local_control.py`

**Interfaces:**
- Consumes: `apply_unexpose_queue` (Task 1), `LiteLLMConfigError` (already imported), `load_state`/`locked_state` (already imported).
- Produces: `StopAllResult(stopped: list[str], warnings: list[str] = field(default_factory=list))`; `stop_all_local_models(...) -> StopAllResult` (was `-> list[str]`). Task 3 (`main.py`) reads `.stopped` and `.warnings`.

- [x] **Step 1: Update existing tests for the new return type, and write the new failing tests**

`modelman/tests/test_local_control.py` currently treats `stop_all_local_models(...)` as a plain list in two places. Update both:

```python
def test_stop_all_local_models_stops_every_running_one(tmp_path):
    # `modelman stop --all` (or equivalent) must clear every running
    # model's flag in one pass, via the single stop-all isolation call —
    # not one stop_provider() call per model.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop_all:
        result = stop_all_local_models(state_path)
    mock_stop_all.assert_called_once()
    assert sorted(result.stopped) == ["ollama/qwen3.8:27b-mlx", "omlx/model-a"]
    assert result.warnings == []
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is False
    assert state.get("omlx/model-a").running is False
```

```python
def test_stop_all_local_models_unexposes_each_stopped_model(tmp_path):
    # `--all` must not leave a stopped model's LiteLLM row behind either —
    # same expose/running symmetry as a single stop_local_model() call,
    # just applied to every model the batch stops, in a single batched
    # config write (see test_stop_all_local_models_unexposes_in_one_batch
    # for the call-count assertion).
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/qwen3.8:27b-mlx", ModelState(ready=True, exposed=True, running=True))
    store.set("omlx/model-a", ModelState(ready=True, exposed=False, running=True))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text(
        "model_list:\n"
        "  - model_name: ollama/qwen3.8:27b-mlx\n"
        "    litellm_params:\n"
        "      model: ollama/qwen3.8:27b-mlx\n"
    )

    with patch("modelman.local_control.stop_all_local_providers"):
        result = stop_all_local_models(state_path, litellm_path=litellm_path)

    assert sorted(result.stopped) == ["ollama/qwen3.8:27b-mlx", "omlx/model-a"]
    assert result.warnings == []
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").exposed is False
    assert state.get("omlx/model-a").exposed is False  # was never exposed; stays False
```

Now add three new tests directly after `test_stop_all_local_models_unexposes_each_stopped_model`:

```python
def test_stop_all_local_models_unexposes_in_one_batched_call(tmp_path):
    # Two exposed models being stopped together must go through ONE
    # apply_unexpose_queue call (one config load/save/restart), not one
    # unexpose_model() call per model — the efficiency half of the
    # code-review finding this task fixes.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=True, running=True))
    store.set("ollama/b", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.apply_unexpose_queue", return_value=[]) as mock_batch,
    ):
        result = stop_all_local_models(state_path)

    mock_batch.assert_called_once()
    (_, called_ids, _), _ = mock_batch.call_args
    assert sorted(called_ids) == ["ollama/a", "ollama/b"]
    assert sorted(result.stopped) == ["ollama/a", "ollama/b"]


def test_stop_all_local_models_surfaces_unexpose_failure_as_warning(tmp_path):
    # A batch unexpose failure (disk full, unwritable config) must not be
    # silently swallowed: every process still stops and every running
    # flag still clears, but the caller (main.py's --all branch) needs a
    # warning to print, mirroring stop_local_model's StopResult.warnings.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch(
            "modelman.local_control.apply_unexpose_queue",
            side_effect=OSError(28, "No space left on device"),
        ),
    ):
        result = stop_all_local_models(state_path)

    assert result.stopped == ["ollama/a"]
    assert any("could not be un-exposed" in w for w in result.warnings)
    state = load_state(state_path)
    # The process is stopped either way — a failed unexpose must not block it.
    assert state.get("ollama/a").running is False


def test_stop_all_local_models_failed_unexpose_does_not_clobber_concurrent_exposed_write(
    tmp_path,
):
    # Race guard: stop_all_local_models() loads `state` once up front, then
    # (in this test) a concurrent process flips `exposed` on disk while the
    # batch unexpose is failing. The flag write at the end must fall back
    # to the freshly-locked value, not the stale pre-attempt snapshot —
    # the same guard stop_local_model() already has.
    from modelman.state import locked_state

    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)

    def _concurrent_write_then_fail(*args, **kwargs):
        # Simulate another process (e.g. `modelman expose ollama/a`)
        # racing this stop_all_local_models() call.
        with locked_state(state_path) as fresh:
            fresh.models["ollama/a"] = replace(fresh.models["ollama/a"], exposed=False)
        raise OSError(28, "No space left on device")

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch(
            "modelman.local_control.apply_unexpose_queue",
            side_effect=_concurrent_write_then_fail,
        ),
    ):
        stop_all_local_models(state_path)

    # Must reflect the concurrent write (False), not the stale pre-attempt
    # snapshot (True) that stop_all_local_models loaded before the race.
    assert load_state(state_path).get("ollama/a").exposed is False
```

`test_stop_all_local_models_failed_unexpose_does_not_clobber_concurrent_exposed_write` needs `replace` imported at the top of `tests/test_local_control.py` — check for `from dataclasses import replace` and add it if missing.

- [x] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/test_local_control.py -k stop_all_local_models -v`
Expected: the two updated tests FAIL with `AttributeError: 'list' object has no attribute 'stopped'`; the three new tests FAIL (two with the same AttributeError, one with `AttributeError: module has no attribute 'apply_unexpose_queue'` when patching).

- [x] **Step 3: Implement the fix**

In `modelman/src/modelman/local_control.py`, update the litellm import block (currently lines 49-55):

```python
from .litellm import (
    ExposeError,
    LiteLLMConfigError,
    apply_unexpose_queue,
    default_litellm_config_path,
    expose_model,
    unexpose_model,
)
```

Add `StopAllResult` next to `StopResult` (currently lines 129-134):

```python
@dataclass
class StopResult:
    # The marker that was cleared, or None if nothing was running.
    stopped_model_id: str | None
    warnings: list[str] = field(default_factory=list)


@dataclass
class StopAllResult:
    # Every model id stopped by this call, sorted.
    stopped: list[str]
    warnings: list[str] = field(default_factory=list)
```

Replace `stop_all_local_models()` (currently lines 1076-1101) with:

```python
def stop_all_local_models(
    state_path: Path | None = None, *, litellm_path: Path | None = None
) -> StopAllResult:
    """Stop every currently-running local model, best-effort un-expose
    every one that was exposed in a single batched LiteLLM write, and
    clear all their flags. Returns the sorted list of model ids that were
    stopped plus any non-fatal warnings (e.g. a failed batch unexpose)."""
    state = load_state(state_path)
    running_ids = sorted(mid for mid, s in state.models.items() if s.running)
    if not running_ids:
        return StopAllResult(stopped=[])
    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to stop local models: {exc}") from exc

    exposed_ids = [mid for mid in running_ids if state.models[mid].exposed]
    unexpose_ok = True
    warnings: list[str] = []
    if exposed_ids:
        try:
            warnings = apply_unexpose_queue(
                state, exposed_ids, litellm_path or default_litellm_config_path()
            )
        except (LiteLLMConfigError, OSError) as exc:
            unexpose_ok = False
            warnings = [
                f"{len(exposed_ids)} stopped model(s) could not be un-exposed from "
                f"LiteLLM ({exc}); they may still be routable through a dead backend "
                f"until you run `modelman unexpose <id>` for each: "
                f"{', '.join(exposed_ids)}"
            ]

    with locked_state(state_path) as fresh:
        for mid in running_ids:
            existing = fresh.models.get(mid)
            if existing is not None and existing.running:
                fresh.models[mid] = replace(
                    existing,
                    running=False,
                    exposed=state.models[mid].exposed if unexpose_ok else existing.exposed,
                )
    return StopAllResult(stopped=running_ids, warnings=warnings)
```

This fixes both remaining findings for this function: the batch failure now produces a real warning instead of being suppressed, and the exposed-flag fallback on failure reads `existing.exposed` (the value `fresh` just loaded fresh inside `locked_state`, i.e. current-as-of-the-lock) instead of the stale pre-attempt `state.models[mid].exposed` snapshot — matching `stop_local_model()`'s own guard.

- [x] **Step 4: Run tests to verify they pass**

Run: `cd modelman && uv run pytest tests/test_local_control.py -k stop_all_local_models -v`
Expected: PASS (5 passed)

- [x] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/src/modelman/local_control.py modelman/tests/test_local_control.py
git commit -m "$(cat <<'EOF'
fix(modelman): surface stop --all unexpose failures, batch the restart

stop_all_local_models() now routes its unexpose step through the new
apply_unexpose_queue (one config write/restart for the whole batch
instead of one per model) and returns a StopAllResult carrying
warnings, so a failed batch unexpose is no longer silently dropped.
Also fixes a stale-snapshot race: on failure, the exposed flag now
falls back to the freshly-locked value instead of the pre-attempt
snapshot, matching stop_local_model()'s existing guard.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Fix `_clear_stale_running_flag()`'s stale-snapshot race

**Files:**
- Modify: `modelman/src/modelman/local_control.py:235-269` (`_clear_stale_running_flag`)
- Test: `modelman/tests/test_local_control.py`

**Interfaces:**
- Consumes: nothing new — same signature, same callers (`running_model_ids()`, `start_local_model()`'s occupant-replacement/idempotency paths).
- Produces: no interface change; internal behavior fix only.

- [x] **Step 1: Write the failing test**

Add to `modelman/tests/test_local_control.py` (near `test_running_model_ids_unexposes_a_stale_flagged_model`):

```python
def test_clear_stale_running_flag_failed_unexpose_does_not_clobber_concurrent_write(tmp_path):
    # Same race guard as stop_all_local_models: if unexpose_model() fails
    # while this function is clearing a stale flag, the exposed field it
    # writes back must reflect whatever a concurrent process wrote in the
    # meantime, not the stale pre-attempt snapshot this function loaded
    # for itself at the top.
    from modelman.local_control import _clear_stale_running_flag
    from modelman.state import locked_state

    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"  # missing -> unexpose_model raises

    def _concurrent_write_then_fail(*args, **kwargs):
        with locked_state(state_path) as fresh:
            fresh.models["ollama/a"] = replace(fresh.models["ollama/a"], exposed=False)
        raise OSError(28, "No space left on device")

    with patch(
        "modelman.local_control.unexpose_model", side_effect=_concurrent_write_then_fail
    ):
        _clear_stale_running_flag("ollama/a", state_path, litellm_path)

    state = load_state(state_path)
    assert state.get("ollama/a").running is False
    # Must reflect the concurrent write, not the stale True snapshot this
    # function loaded before the race.
    assert state.get("ollama/a").exposed is False
```

Check that `replace` is imported at the top of the test file (Task 2 already adds it if missing).

- [x] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/test_local_control.py -k test_clear_stale_running_flag_failed_unexpose_does_not_clobber_concurrent_write -v`
Expected: FAIL — `exposed` comes back `True` (the stale snapshot), not `False`.

- [x] **Step 3: Implement the fix**

Replace `_clear_stale_running_flag()` (currently `modelman/src/modelman/local_control.py:235-269`) with:

```python
def _clear_stale_running_flag(
    model_id: str, state_path: Path | None, litellm_path: Path | None = None
) -> None:
    """Best-effort: clear ONE model's running flag when it's still True —
    used when a probe finds it not actually serving (stale flag), or after
    a failed start/stop for that specific model. Never touches any other
    model's flag.

    Also best-effort un-exposes the model when it was exposed: a model
    modelman no longer believes is running must not stay routable through
    LiteLLM (the same reasoning as stop_local_model's unexpose). Done
    OUTSIDE the locked_state transaction below, mirroring
    _expose_for_start/_unexpose_for_stop's own pattern, so the LiteLLM
    config write and proxy restart never happen while holding the
    process-wide state lock.
    """
    try:
        state = load_state(state_path)
    except OSError:
        return
    existing = state.models.get(model_id)
    if existing is None or not existing.running:
        return
    unexpose_ok = True
    if existing.exposed:
        try:
            unexpose_model(state, model_id, litellm_path or default_litellm_config_path())
        except (LiteLLMConfigError, OSError):
            unexpose_ok = False
    try:
        with locked_state(state_path) as fresh:
            fresh_existing = fresh.models.get(model_id)
            if fresh_existing is not None and fresh_existing.running:
                fresh.models[model_id] = replace(
                    fresh_existing,
                    running=False,
                    exposed=state.models[model_id].exposed if unexpose_ok else fresh_existing.exposed,
                )
    except OSError:
        pass  # the LocalControlError about the failed start is the user's answer
```

The change: `unexpose_model(...)` is no longer wrapped in `contextlib.suppress` directly — it's wrapped in an explicit `try/except` that records success via `unexpose_ok`, and the flag write at the bottom uses `fresh_existing.exposed` (the value just read fresh inside the lock) instead of the stale `state.models[model_id].exposed` when the unexpose failed.

- [x] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/test_local_control.py -k test_clear_stale_running_flag_failed_unexpose_does_not_clobber_concurrent_write -v`
Expected: PASS

- [x] **Step 5: Run the full local_control test file to check for regressions**

Run: `cd modelman && uv run pytest tests/test_local_control.py -q`
Expected: all pass

- [x] **Step 6: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/src/modelman/local_control.py modelman/tests/test_local_control.py
git commit -m "$(cat <<'EOF'
fix(modelman): stop stale-flag self-heal from clobbering concurrent exposed writes

_clear_stale_running_flag() now falls back to the freshly-locked
exposed value (not its own stale pre-attempt snapshot) when its
best-effort unexpose fails, matching stop_local_model()'s existing
race guard.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `main.py`'s `stop --all` branch reads `StopAllResult`

**Files:**
- Modify: `modelman/src/modelman/main.py:666-676`
- Test: `modelman/tests/commands/test_local_control.py`

**Interfaces:**
- Consumes: `StopAllResult` (Task 2) from `stop_all_local_models()`.
- Produces: no new interface — CLI output only.

- [x] **Step 1: Write the failing test**

Add to `modelman/tests/commands/test_local_control.py`, right after `test_stop_command_single_model_prints_unexpose_warning`:

```python
def test_stop_command_all_prints_unexpose_warning(tmp_path, monkeypatch):
    # `modelman stop --all` must surface a failed batch un-expose the same
    # way the single-model path already does (see
    # test_stop_command_single_model_prints_unexpose_warning) — this was
    # previously silently dropped.
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[model_state."ollama/x"]\nready = true\nexposed = true\nrunning = true\n'
    )
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch(
            "modelman.local_control.apply_unexpose_queue",
            side_effect=OSError(28, "No space left"),
        ),
    ):
        result = runner.invoke(app, ["stop", "--all"])
    assert result.exit_code == 0, result.stdout
    assert "warning:" in result.output
    assert "could not be un-exposed" in result.output
    # The process still stops even though the unexpose failed.
    assert load_state(path=state_path).get("ollama/x").running is False
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/commands/test_local_control.py -k test_stop_command_all_prints_unexpose_warning -v`
Expected: FAIL — no `warning:` in output (current code drops it), and/or an `AttributeError` if `main.py` hasn't been updated to read `.stopped` yet (it hasn't — this test also proves the wiring needs the Task 2 return type, in case Task 2 is run out of order in a subagent-driven flow).

- [x] **Step 3: Implement the fix**

In `modelman/src/modelman/main.py`, replace the `if all_:` branch (currently lines 666-676):

```python
    if all_:
        try:
            result = stop_all_local_models()
        except LocalControlError as exc:
            typer.echo(f"error: {exc}", err=True)
            raise typer.Exit(1) from exc
        if not result.stopped:
            typer.echo("No local model is running.")
        else:
            typer.echo(f"Stopped {len(result.stopped)} model(s): {', '.join(result.stopped)}.")
        for warning in result.warnings:
            typer.echo(f"warning: {warning}", err=True)
        return
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/commands/test_local_control.py -k "stop_command_all" -v`
Expected: PASS (both `test_stop_command_all_stops_everything` and the new `test_stop_command_all_prints_unexpose_warning`)

- [x] **Step 5: Run the full commands test file to check for regressions**

Run: `cd modelman && uv run pytest tests/commands/test_local_control.py -q`
Expected: all pass

- [x] **Step 6: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/src/modelman/main.py modelman/tests/commands/test_local_control.py
git commit -m "$(cat <<'EOF'
fix(modelman): print stop --all unexpose warnings to the CLI

main.py's --all branch now reads StopAllResult.warnings, matching
the single-model stop path's existing warning output.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Fix the TUI's on-mount reconcile self-heal to clear `exposed`, not just `running`

**Files:**
- Modify: `modelman/src/modelman/screens/models.py:351-354` (inside `_run_reconcile`)
- Test: `modelman/tests/screens/test_app_navigation.py`

**Interfaces:**
- Consumes: `load_state` (already imported at `modelman/src/modelman/screens/models.py:52`).
- Produces: no interface change — in-memory `self.state` now matches what `_clear_stale_running_flag()` already persisted to disk.

- [x] **Step 1: Write the failing test**

Add to `modelman/tests/screens/test_app_navigation.py`, after `test_reconcile_shows_reality_when_manifest_out_of_date`:

```python
@pytest.mark.asyncio
async def test_reconcile_self_heal_clears_exposed_column_for_dead_model(tmp_path, monkeypatch):
    """A model left flagged running+exposed from a prior session, whose
    process died externally before the TUI reopens, must show as NOT
    exposed after the on-mount reconcile self-heals it — not just NOT
    running. _clear_stale_running_flag() already un-exposes it on disk;
    the in-memory self.state used to render the table was left stale."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])
    litellm_path = tmp_path / "litellm-config.yaml"
    litellm_path.write_text(
        "model_list:\n"
        "  - model_name: ollama/o35\n"
        "    litellm_params:\n"
        "      model: ollama/o35\n"
    )
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))

    store = StateStore()
    store.set("ollama/o35", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 1024
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))
    # The probe finds nothing actually serving — the process died outside
    # modelman's knowledge (crash, manual kill).
    monkeypatch.setattr("modelman.local_control._probe_running", lambda *a, **k: False)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()

        mt = app.screen.query_one("#model-table", DataTable)
        row = mt.get_row_at(0)
        assert row[5] == "–"  # EXPOSED column: "–", not "Y"
        assert app.screen.state.get("ollama/o35").running is False
        assert app.screen.state.get("ollama/o35").exposed is False

    from modelman.state import load_state as _load_state

    # And the disk-side flag (already written by _clear_stale_running_flag)
    # agrees.
    assert _load_state(state_path).get("ollama/o35").exposed is False
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/screens/test_app_navigation.py -k test_reconcile_self_heal_clears_exposed_column_for_dead_model -v`
Expected: FAIL — `app.screen.state.get("ollama/o35").exposed` is `True` (stale in-memory value) and/or the EXPOSED column still renders `Y`.

- [x] **Step 3: Implement the fix**

In `modelman/src/modelman/screens/models.py`, replace the self-heal loop inside `_run_reconcile()` (currently lines 351-354):

```python
        verified = set(running_model_ids(self.registry, self.state, self.state_path))
        for model_id, model_state in list(self.state.models.items()):
            if model_state.running and model_id not in verified:
                self.state.models[model_id] = replace(model_state, running=False)
```

with:

```python
        verified = set(running_model_ids(self.registry, self.state, self.state_path))
        stale_ids = [
            model_id
            for model_id, model_state in self.state.models.items()
            if model_state.running and model_id not in verified
        ]
        if stale_ids:
            # running_model_ids() already best-effort un-exposed each of
            # these on disk (_clear_stale_running_flag) — pull that fresh
            # value in rather than leaving self.state's in-memory copy at
            # its stale pre-reconcile exposed value, which would render a
            # wrong "Y" in the EXPOSED column for the rest of the session.
            fresh_state = load_state(self.state_path)
            for model_id in stale_ids:
                model_state = self.state.models[model_id]
                fresh_exposed = fresh_state.models.get(model_id, model_state).exposed
                self.state.models[model_id] = replace(
                    model_state, running=False, exposed=fresh_exposed
                )
```

Update the comment immediately above this block (currently lines 345-350) to mention the exposed sync — replace:

```python
        # Self-heal the running flag the same way ready/disk_path already
        # are: a model flagged running whose process actually died (crash,
        # manual kill outside modelman) must not keep showing RUNNING=●
        # forever. running_model_ids() re-probes every flagged model and
        # clears any stale flag as a side effect; anything it doesn't
        # return is not verified running, so it gets cleared here too.
```

with:

```python
        # Self-heal the running flag the same way ready/disk_path already
        # are: a model flagged running whose process actually died (crash,
        # manual kill outside modelman) must not keep showing RUNNING=●
        # forever. running_model_ids() re-probes every flagged model and
        # clears any stale flag (and best-effort un-exposes it) as a side
        # effect; anything it doesn't return is not verified running, so
        # it gets cleared here too — exposed is re-read from disk since
        # running_model_ids() already wrote the post-unexpose value there.
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/screens/test_app_navigation.py -k test_reconcile_self_heal_clears_exposed_column_for_dead_model -v`
Expected: PASS

- [x] **Step 5: Run the full screens test suite to check for regressions**

Run: `cd modelman && uv run pytest tests/screens/ -q`
Expected: all pass

- [x] **Step 6: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/src/modelman/screens/models.py modelman/tests/screens/test_app_navigation.py
git commit -m "$(cat <<'EOF'
fix(modelman): sync EXPOSED column after TUI reconcile self-heals a dead model

_run_reconcile()'s self-heal loop now re-reads the exposed flag from
disk for every model it clears running on, instead of leaving
self.state's in-memory copy at its stale pre-reconcile value —
running_model_ids() already best-effort un-exposed these on disk via
_clear_stale_running_flag(), so the EXPOSED column was showing "Y"
for models that were neither running nor exposed.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Full verification pass

**Files:** none (verification only)

**Interfaces:** none

- [x] **Step 1: Run lint/typecheck**

Run: `cd modelman && make check`
Expected: no errors

- [x] **Step 2: Run the full modelman test suite**

Run: `cd modelman && make test`
Expected: all pass

- [x] **Step 3: Run the root shell lint (unaffected by this change, but part of `make test-all`)**

Run: `cd /Users/keith/github/ohanaverse/local-ai-setup && make lint`
Expected: no errors

- [x] **Step 4: Confirm no guide drift**

Run: `cd /Users/keith/github/ohanaverse/local-ai-setup && git grep -n "exposed = " docs/guides/`
Expected: same output as before this branch's work started (this plan never changes an actual model's exposure state, only code paths) — spot-check that the printed lines still match the live `~/.config/local-ai/modelman.toml` if in doubt.

- [x] **Step 5: Update modelman/CLAUDE.md's Local-model lifecycle paragraph**

The paragraph currently says (in the "Stop flow" sentence): "`stop_all_local_models()` is the only stop-everything path (`stop_all_local_providers()` + the same per-model best-effort unexpose + clear every flag), reached from `modelman stop --all`." Update "the same per-model best-effort unexpose" to reflect the batching:

Find this exact sentence in `modelman/CLAUDE.md` (under "### Local-model lifecycle (multi-model design, 2026-09-14)"):

```
`stop_all_local_models()` is the only stop-everything path (`stop_all_local_providers()` + the same per-model best-effort unexpose + clear every flag), reached from `modelman stop --all`.
```

Replace with:

```
`stop_all_local_models()` is the only stop-everything path (`stop_all_local_providers()` + a single batched best-effort unexpose via `apply_unexpose_queue` covering every exposed model being stopped + clear every flag), reached from `modelman stop --all`; a failed batch unexpose surfaces as one warning on `StopAllResult.warnings` rather than blocking the stop.
```

- [x] **Step 6: Commit the CLAUDE.md update**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/CLAUDE.md
git commit -m "$(cat <<'EOF'
docs(modelman): document batched stop --all unexpose in CLAUDE.md

fix(scan): address code-review findings on stop --all unexpose handling
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** All four code-review findings are covered — Task 2 covers findings #1 (dropped warnings) and #4 (N sequential restarts) together since they share the same loop; Task 3 covers finding #2's `_clear_stale_running_flag()` half (Task 2 covers its `stop_all_local_models()` half); Task 5 covers finding #3 (TUI EXPOSED staleness).
- **Placeholder scan:** every step has real code, real test bodies, real commands — no "add appropriate handling" placeholders.
- **Type consistency:** `StopAllResult.stopped`/`.warnings` names are used consistently across Task 2 (definition), Task 4 (`main.py` consumer), and all new tests. `apply_unexpose_queue(state, model_ids, litellm_path) -> list[str]` signature is consistent between Task 1 (definition + tests) and Task 2 (call site + tests).
