# Code Review Fixes (Round 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the verified findings from the second `/code-review high` pass over the `downloads-on-exit` branch (the branch already went through one round of fixes in `docs/superpowers/plans/2026-09-14-code-review-fixes.md`).

**Architecture:** Five independent, narrowly-scoped bug fixes across `main.py` (the post-exit queued-ops runner), `queue.py` (`PendingChanges.apply()`), the HuggingFace-backed providers (`omlx.py`/`llamacpp.py`/`_progress.py`), and `screens/models.py` (dead-code removal). No design changes — every fix closes a gap against behavior the surrounding code/docs/tests already document as intended.

**Tech Stack:** Python 3.13, `uv run pytest`, Typer, MagicMock-based provider stubs.

**Spec:** No new spec — these are bug fixes against the existing `docs/superpowers/specs/2026-09-13-downloads-on-exit-design.md` design and `modelman/CLAUDE.md`'s documented invariants (e.g. "already-completed steps are not undone/lost" for apply()'s exception safety net).

## Global Constraints

- Run `cd modelman && uv run pytest <file> -q` for focused runs; `make check` (lint+typecheck) before the final commit of each task.
- Every new test gets a comment describing the scenario and why it matters (this repo's convention — see any test in `tests/commands/test_run_tui.py`).
- Commits reference the plan item they complete, per this repo's CLAUDE.md workflow rules.
- One review finding (an `except TypeError` fallback in `queue.py::_download` using `exc.__traceback__.tb_next` to detect a signature mismatch) was investigated and is **not** actioned — verified empirically (see the note at the end of this doc) to already behave correctly; the review's proposed `inspect.signature`-based alternative would not change behavior for the counterexample it cited.

---

## Task 1: Harden `run_queued_ops` — operation counting, provider-instantiation failures, provider dedup, and a general `apply()` exception safety net

**Files:**
- Modify: `modelman/src/modelman/main.py:173-263` (the `run_queued_ops` function)
- Test: `modelman/tests/commands/test_run_tui.py`

**Interfaces:**
- Consumes: `PendingChanges`, `QueuedOps` (`queue.py`), `ProviderRegistry.get`/`.get_class` (`providers/registry.py`), `provider_config` (`registry.py`) — all unchanged.
- Produces: no new public signatures; `run_queued_ops(queued: QueuedOps) -> bool` keeps its existing contract.

This task folds three related review findings into one commit because they all touch the same ~90-line function and are naturally reviewed together:

1. `total` (used for the "N of M operations failed" summary and the Ctrl+C "remaining skipped" count) is computed once from the pre-apply queue snapshot, but `queue.py`'s `apply()` can append a cascaded unexpose to `PendingChanges.exposes` mid-run — deleting, or clearing the ready flag of, a model that's exposed but has no explicit expose/unexpose queued. That cascade's own `expose:start`/`unexpose:start`/`done`/`fail` events fire but were never counted, so `total` undercounts real work.
2. A provider that fails to *instantiate* with a real error (bad config, a broken constructor — not a `KeyError`, which correctly means "flag-only provider") currently propagates straight out of `run_queued_ops` uncaught, crashing the CLI with a raw traceback instead of being reported like any other per-item failure. This exact scenario was covered by a test before the old `StatusScreen` (which wrapped its equivalent call in a broad `except Exception`) was deleted; the replacement runner dropped that safety net.
3. The `provider_instances` construction loop builds (and discards) one `ProviderRegistry.get(...)` instance per *queued item*, instead of once per distinct provider id — wasteful, and inconsistent with `sync.py::_modeldir_providers`'s established dedup-by-id pattern.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/commands/test_run_tui.py` (after `test_run_queued_ops_missing_ready_model_id_prints_live_failure`, before `test_print_event_formats_download_lifecycle`):

```python
def test_run_queued_ops_counts_cascaded_unexpose_in_total(tmp_path, monkeypatch, capsys):
    """Deleting a model that is currently exposed (with no explicit
    expose/unexpose queued through the TUI) makes queue.py's apply()
    append a cascaded unexpose to PendingChanges.exposes mid-run. The
    pre-apply `total` count must grow to include it, or the "N of M
    operations failed" summary undercounts real work — regression for a
    review finding where a failing cascade silently disappeared from the
    denominator."""
    from dataclasses import replace

    from modelman.state import locked_state

    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])
    with locked_state(state_path) as state:
        state.set("ollama/x", replace(state.get("ollama/x"), exposed=True))
    # queued.exposes is intentionally empty: the cascade must come only
    # from queue.py's delete-of-an-exposed-model logic, not a
    # user-queued unexpose.
    variant = model_entry_to_variant(entry)
    fake_provider = MagicMock()
    monkeypatch.setattr(
        "modelman.queue.apply_expose_queue",
        MagicMock(side_effect=RuntimeError("config unwritable")),
    )
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(QueuedOps(deletes={"ollama/x": variant}))

    assert failed is True
    out = capsys.readouterr().out
    # 1 delete (succeeded) + 1 cascaded unexpose (failed) = 2, not 1.
    assert "1 of 2 operations failed." in out


def test_run_queued_ops_reports_provider_instantiation_error_without_crashing(
    tmp_path, monkeypatch, capsys
):
    """A provider whose constructor raises a real error (bad config, a
    broken __init__) must not crash the whole run with a raw traceback,
    and must not be silently treated as a flag-only provider either —
    that would flip ready=True without ever downloading anything.
    Regression for a review finding: the old StatusScreen caught this
    with a broad except Exception; the post-exit runner dropped it."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])
    with patch("modelman.main.ProviderRegistry.get", side_effect=ValueError("bad config")):
        failed = run_queued_ops(QueuedOps(ready={"ollama/x": True}))

    assert failed is True
    out = capsys.readouterr().out
    assert "provider unavailable: bad config" in out
    state = load_state(state_path)
    # Must NOT have been silently marked ready — the provider never ran.
    assert state.get("ollama/x").ready is False


def test_run_queued_ops_reports_unexpected_apply_exception_without_crashing(
    tmp_path, monkeypatch, capsys
):
    """A genuine bug reaching all the way out of PendingChanges.apply()
    (not a per-step failure apply() already captures itself) must not
    crash the CLI with a raw traceback — apply()'s own exception safety
    net has already persisted whatever completed before the crash, and
    run_queued_ops must report the rest cleanly instead of losing it."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])
    fake_provider = MagicMock()
    monkeypatch.setattr(
        "modelman.queue.find_shared_artifact_owner",
        MagicMock(side_effect=RuntimeError("registry corrupted")),
    )
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(
            QueuedOps(deletes={"ollama/x": model_entry_to_variant(entry)})
        )

    assert failed is True
    out = capsys.readouterr().out
    assert "unexpected error: registry corrupted" in out


def test_run_queued_ops_builds_one_provider_instance_per_provider_id(
    tmp_path, monkeypatch, capsys
):
    """Two queued items on the same provider must construct that
    provider once, not once per item — matches the dedup-by-id pattern
    sync.py's _modeldir_providers already uses."""
    entry_a = ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a:7b")
    entry_b = ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry_a, entry_b])
    fake_provider = MagicMock()
    fake_provider.download.return_value = "ollama:x"
    fake_provider.size_of.return_value = None
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider) as get_mock:
        failed = run_queued_ops(QueuedOps(ready={"ollama/a": True, "ollama/b": True}))

    assert failed is False
    assert get_mock.call_count == 1
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/commands/test_run_tui.py -k "cascaded_unexpose or instantiation_error or unexpected_apply_exception or one_provider_instance" -v`
Expected: all four FAIL — `1 of 2` shows as `1 of 1`; the `ValueError`/`RuntimeError` propagate uncaught (test errors, not assertion failures); `get_mock.call_count` is `2`, not `1`.

- [ ] **Step 3: Replace `run_queued_ops` in `main.py`**

Replace the entire function body (lines 173-263) with:

```python
def run_queued_ops(queued: QueuedOps) -> bool:
    """Apply a QueuedOps returned by the TUI against fresh on-disk state.

    Builds a PendingChanges the same way ModelScreen._run_apply used to,
    but against a Registry/StateStore just loaded from disk: adds/edits
    already persisted immediately while the TUI was open, while queued
    deletes/moves/ready/exposes have not. Returns True iff the run
    should exit non-zero (failures, or a Ctrl+C cancellation).
    """
    registry = load_registry()
    state = load_state()
    models_by_id = {m.id: m for m in registry.models}
    ready_specs = {}
    missing_ready = []
    for mid in queued.ready:
        model_entry = models_by_id.get(mid)
        if model_entry is None:
            missing_ready.append(mid)
        else:
            ready_specs[mid] = model_entry_to_variant(model_entry)

    provider_instances: dict[str, object] = {}
    unavailable_providers: dict[str, str] = {}
    provider_ids = {
        spec["provider"] for spec in list(ready_specs.values()) + list(queued.deletes.values())
    }
    for provider_id in provider_ids:
        try:
            entry = registry.provider(provider_id)
        except KeyError:
            # Not in the registry or not mapped to a Provider class:
            # PendingChanges treats this as flag-only (native/unmapped).
            continue
        try:
            provider_instances[provider_id] = ProviderRegistry.get(
                provider_id, provider_config(entry)
            )
        except Exception as exc:  # noqa: BLE001
            # A provider that fails to instantiate with a real error (bad
            # config, a broken constructor) must not crash the whole run.
            # A ready-on against it is recorded as its own failure below
            # (never silently treated as flag-only — that would flip
            # ready=True without ever downloading anything); a delete
            # against it still cleans up the registry/state rows, the same
            # degraded-but-safe behavior a flag-only provider already gets.
            unavailable_providers[provider_id] = str(exc)

    provider_ready_failures: list[tuple[str, str]] = []
    ready_items: list[tuple[str, dict, bool]] = []
    for mid, target in queued.ready.items():
        spec = ready_specs.get(mid)
        if spec is None:
            continue  # already recorded in missing_ready
        reason = unavailable_providers.get(spec["provider"])
        if reason is not None:
            provider_ready_failures.append((mid, reason))
        else:
            ready_items.append((mid, spec, target))

    pending = PendingChanges(
        registry=registry,
        state=state,
        registry_path=_default_registry_path(),
        state_path=_default_state_path(),
        providers=provider_instances,
        ready=ready_items,
        deletes=list(queued.deletes.items()),
        moves=list(queued.moves.items()),
        exposes=list(queued.exposes.items()),
        litellm_path=default_litellm_config_path(),
    )
    total = (
        len(pending.ready)
        + len(missing_ready)
        + len(provider_ready_failures)
        + len(pending.deletes)
        + len(pending.moves)
        + len(pending.exposes)
    )
    completed = 0
    # Ids already counted in the `exposes` term above. apply() can append
    # cascaded unexposes to PendingChanges.exposes mid-run (deleting, or
    # clearing the ready flag of, a model that's exposed but has no
    # explicit expose/unexpose queued — see queue.py's deletes loop and
    # ready loop) — each cascade must grow `total` too, the first time its
    # start tag is seen, or the "N of M" summary and the Ctrl+C remaining
    # count both undercount real work.
    counted_expose_ids = set(queued.exposes)
    done_verbs = {
        "delete:done",
        "download:done",
        "ready:done",
        "move:done",
        "expose:done",
        "unexpose:done",
    }

    def on_event(tag: str) -> None:
        nonlocal completed, total
        parts = tag.split("|")
        verb = parts[0]
        if verb in ("expose:start", "unexpose:start") and parts[1] not in counted_expose_ids:
            total += 1
            counted_expose_ids.add(parts[1])
        print_event(tag)
        if verb in done_verbs:
            completed += 1

    # A model id queued while the TUI was open but missing from a
    # freshly-loaded registry (deleted out-of-band, or a hand-edited
    # registry.toml) must not take down the whole run — every other op
    # (deletes/moves/exposes) already degrades to a per-item failure on
    # a missing id with a live event, via queue.py's own emit() calls;
    # ready needs the same treatment, done here since its lookup happens
    # before PendingChanges even exists.
    for mid in missing_ready:
        pending.failures.append(f"ready {mid}: Unknown model: {mid}")
        on_event(f"ready:fail|{mid}|{mid}|Unknown model")
    for mid, reason in provider_ready_failures:
        pending.failures.append(f"ready {mid}: provider unavailable: {reason}")
        on_event(f"ready:fail|{mid}|{mid}|provider unavailable: {reason}")

    try:
        pending.apply(on_event=on_event, on_progress=typer.echo)
    except KeyboardInterrupt:
        pending.cancel()
        typer.echo(
            f"\nCancelled: {completed} steps completed, {total - completed} remaining skipped."
        )
        return True
    except Exception as exc:  # noqa: BLE001
        # apply() normally captures per-step failures itself; an exception
        # reaching all the way out here is a genuine bug, not a per-item
        # failure — its own exception safety net (_persist) has already
        # saved whatever completed before this point. Report it like any
        # other failure instead of crashing with a raw traceback (mirrors
        # the deleted StatusScreen's equivalent except Exception).
        pending.failures.append(f"unexpected error: {exc}")
        return print_error_summary(pending.failures, total)

    return print_error_summary(pending.failures, total)
```

- [ ] **Step 4: Run the new tests, then the full file, to verify they pass**

Run: `cd modelman && uv run pytest tests/commands/test_run_tui.py -v`
Expected: all tests PASS, including the pre-existing ones (the dedup/failure-routing changes must not change behavior for any case they didn't target).

- [ ] **Step 5: Lint and typecheck**

Run: `cd modelman && make check`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd modelman
git add src/modelman/main.py tests/commands/test_run_tui.py
git commit -m "$(cat <<'EOF'
fix(modelman): harden run_queued_ops's counting, provider-instantiation errors, and exception safety net

completes plan item #1 (review findings: total undercounts expose
cascades, a broad except-Exception safety net around apply() was lost
when StatusScreen was deleted, provider_instances rebuilds redundant
instances)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 2: Persist completed work before returning on a cancelled download in `queue.py`

**Files:**
- Modify: `modelman/src/modelman/queue.py:527-539` (the `DownloadCancelled` branch inside `apply()`'s ready loop)
- Test: `modelman/tests/test_queue.py`

**Interfaces:**
- Consumes: `PendingChanges._persist(emit)` (already defined, called elsewhere in the same method).
- Produces: no new signatures.

The `except DownloadCancelled:` branch does a plain `return` without calling `self._persist(emit)`. Every other exit path from `apply()` either persists (the normal end-of-function path, and the `except BaseException: self._persist(emit); raise` safety net around the whole loop body) or is a documented, tested "nothing saved" path (`aborted()`'s early `return` on `self.cancelled`, per the comment right above that safety net). `DownloadCancelled`'s `return` matches neither category — a plain `return` bypasses the `except BaseException` safety net, so an earlier delete/move that fully completed in the same `apply()` call is silently dropped from disk if a later item's download is cancelled.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/test_queue.py` (near the other cancellation tests, e.g. after `test_apply_cancelled_before_moves_skips_them`):

```python
def test_apply_download_cancelled_persists_earlier_completed_delete(tmp_path):
    """A DownloadCancelled raised partway through the ready loop must not
    discard an already-completed delete from earlier in the same apply()
    call — the same "already-completed steps survive" invariant the
    BaseException safety net gives every other unwind path. Regression:
    the DownloadCancelled branch did a plain `return` without persisting,
    silently dropping the delete's already-applied registry change."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="a", family="f", provider="ollama", name="a"),
        _entry(id="b", family="f", provider="ollama", name="b"),
    )
    state = _make_state()
    ollama = MagicMock()
    ollama.name = "ollama"
    ollama.delete.return_value = None
    ollama.is_downloaded.return_value = True
    # Distinct artifact paths per model so find_shared_artifact_owner
    # (called by the delete step) doesn't see a-and-b as colliding —
    # a bare MagicMock's artifact_paths would otherwise return the same
    # mocked value for both.
    ollama.artifact_paths.side_effect = lambda v: frozenset([v["id"]])
    ollama.download.side_effect = DownloadCancelled("b")

    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={"ollama": ollama},
        deletes=[("a", _variant(id="a", provider="ollama", name="a"))],
        ready=[("b", _variant(id="b", provider="ollama", name="b"), True)],
    )
    events: list[str] = []
    pending.apply(on_event=events.append)

    assert "download:cancelled|b|b" in events
    assert "apply:cancelled" in events
    reloaded = load_registry(reg_path)
    assert not any(m.id == "a" for m in reloaded.models)
```

Add the import at the top of `modelman/tests/test_queue.py` (alongside the existing `from modelman.queue import PendingChanges` line):

```python
from modelman.providers._progress import DownloadCancelled
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd modelman && uv run pytest tests/test_queue.py -k test_apply_download_cancelled_persists_earlier_completed_delete -v`
Expected: FAIL — `reloaded.models` still contains `"a"` (registry.toml was never re-saved).

- [ ] **Step 3: Add the persist call**

In `modelman/src/modelman/queue.py`, in the ready loop's download branch:

```python
                    try:
                        local_path = self._download(variant, on_progress)
                    except DownloadCancelled:
                        self._cleanup_partial_download(provider, variant)
                        emit(f"download:cancelled|{model_id}|{label}")
                        emit("apply:cancelled")
                        return
```

becomes:

```python
                    try:
                        local_path = self._download(variant, on_progress)
                    except DownloadCancelled:
                        self._cleanup_partial_download(provider, variant)
                        emit(f"download:cancelled|{model_id}|{label}")
                        emit("apply:cancelled")
                        # A plain `return`, like aborted()'s early-return
                        # paths above, does NOT reach the `except
                        # BaseException: self._persist(emit); raise` safety
                        # net around this whole loop — persist explicitly so
                        # an already-completed delete/move earlier in this
                        # same apply() call is not silently dropped.
                        self._persist(emit)
                        return
```

- [ ] **Step 4: Run the test, then the full file, to verify they pass**

Run: `cd modelman && uv run pytest tests/test_queue.py -v`
Expected: all PASS.

- [ ] **Step 5: Lint and typecheck**

Run: `cd modelman && make check`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd modelman
git add src/modelman/queue.py tests/test_queue.py
git commit -m "$(cat <<'EOF'
fix(modelman): persist completed work before returning on DownloadCancelled

completes plan item #2 (review finding: the DownloadCancelled branch's
plain `return` bypassed apply()'s exception safety net, dropping an
earlier delete/move from the same apply() call)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 3: Harden `omlx`/`llamacpp` Ctrl+C cancellation signaling and fix `_progress.py`'s stale docstring

**Files:**
- Modify: `modelman/src/modelman/providers/omlx.py:90-98`
- Modify: `modelman/src/modelman/providers/llamacpp.py:137-151`
- Modify: `modelman/src/modelman/providers/_progress.py:10-19` (module docstring)
- Test: `modelman/tests/test_providers/test_omlx.py`, `modelman/tests/test_providers/test_llamacpp.py`

**Interfaces:**
- Consumes: `ProgressTqdm._active_should_cancel` (unchanged), `self._cancel_requested` (already exists on both providers).
- Produces: no new signatures.

`ollama.py`'s `_tracked_popen_runner` terminates its subprocess *immediately* when a real Ctrl+C's `KeyboardInterrupt` unwinds through `proc.wait()` (`except BaseException: _terminate_with_escalation(proc); raise`), rather than waiting for a separate `cancel_current()` call that — on the real interrupt path — always arrives too late (after `apply()` has already fully unwound). `omlx.py`/`llamacpp.py` never got the equivalent hardening: their `should_cancel` callback (`lambda: self._cancel_requested`) is wired into `ProgressTqdm`, but nothing sets `_cancel_requested` until `PendingChanges.cancel()` is called — which, like `ollama.py`'s old bug, only happens from `main.py`'s `except KeyboardInterrupt` handler, after `apply()`'s call into `snapshot_download()` has already been interrupted by a raw `KeyboardInterrupt` and unwound.

Python cannot forcibly kill `huggingface_hub`'s internal download worker threads the way `ollama.py` can kill a subprocess — there is no equivalent "immediate termination" available here. The fix below is a *narrower* one: flip `_cancel_requested` the instant the interrupt reaches the `snapshot_download()` call site, so any other worker thread still mid-download sees `should_cancel` return `True` on its very next progress update and raises `DownloadCancelled` itself — shortening the race window (between the caller's `_cleanup_partial_download` and a straggling thread's write) instead of eliminating it outright. The module docstring in `_progress.py` currently claims "nothing currently wires a `should_cancel` callback into `provider.download()`" — false; both providers wire it via `cancel_current()`. Fixing the docstring alongside the behavior change avoids leaving the file's own documentation still wrong about how (and now when) the callback is used.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/test_providers/test_omlx.py` (after `test_cancel_current_resets_on_next_download`):

```python
def test_download_flips_cancel_flag_when_interrupted(provider):
    """A real Ctrl+C (or any other exception) unwinding through
    snapshot_download must flip _cancel_requested immediately, so any HF
    worker thread still mid-download picks up the cancellation on its
    next progress update instead of only learning about it after
    apply() has already fully unwound (see queue.py's DownloadCancelled
    handling). Regression for a review finding: this provider never
    mirrored ollama.py's immediate-interrupt hardening."""
    variant: VariantSpec = {
        "id": "q4",
        "provider": "omlx",
        "name": "x-mlx",
        "repo": "foo/bar",
    }
    with patch("modelman.providers.omlx.snapshot_download", side_effect=KeyboardInterrupt):
        with pytest.raises(KeyboardInterrupt):
            provider.download(variant)
    assert provider._cancel_requested is True
```

Add to `modelman/tests/test_providers/test_llamacpp.py` (after `test_cancel_current_resets_on_next_download`):

```python
def test_download_flips_cancel_flag_when_interrupted(provider):
    """See OMLXProvider's identical test — llamacpp shares the same
    snapshot_download-based download path and the same gap."""
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "x-gguf",
        "repo": "foo/bar",
        "files": ["model.gguf"],
    }
    with patch("modelman.providers.llamacpp.snapshot_download", side_effect=KeyboardInterrupt):
        with pytest.raises(KeyboardInterrupt):
            provider.download(variant)
    assert provider._cancel_requested is True
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/test_providers/test_omlx.py::test_download_flips_cancel_flag_when_interrupted tests/test_providers/test_llamacpp.py::test_download_flips_cancel_flag_when_interrupted -v`
Expected: both FAIL — `KeyboardInterrupt` propagates correctly, but `provider._cancel_requested` is still `False` (nothing sets it).

- [ ] **Step 3: Harden `omlx.py`**

In `modelman/src/modelman/providers/omlx.py`, change:

```python
        with HF_DOWNLOAD_LOCK:
            ProgressTqdm.set_active_context(on_progress, lambda: self._cancel_requested)
            try:
                if on_progress is not None:
                    kwargs["tqdm_class"] = ProgressTqdm
                snapshot_download(**kwargs)
                return str(target)
            finally:
                ProgressTqdm.clear_active_context()
```

to:

```python
        with HF_DOWNLOAD_LOCK:
            ProgressTqdm.set_active_context(on_progress, lambda: self._cancel_requested)
            try:
                if on_progress is not None:
                    kwargs["tqdm_class"] = ProgressTqdm
                snapshot_download(**kwargs)
                return str(target)
            except BaseException:
                # A real Ctrl+C (or any other interrupt) unwinding through
                # this call flips the cancellation flag immediately, so any
                # HF worker thread still mid-download sees should_cancel
                # return True on its next progress update and raises
                # DownloadCancelled itself (see ProgressTqdm.display())
                # instead of only being told to cancel via
                # PendingChanges.cancel() after apply() has already fully
                # unwound. Narrows, but does not eliminate, the window
                # where a straggling worker thread writes into `target`
                # after the caller's cleanup has already run.
                self._cancel_requested = True
                raise
            finally:
                ProgressTqdm.clear_active_context()
```

- [ ] **Step 4: Harden `llamacpp.py`**

In `modelman/src/modelman/providers/llamacpp.py`, change:

```python
            ProgressTqdm.set_active_context(on_progress, lambda: self._cancel_requested)
            try:
                if on_progress is not None:
                    kwargs["tqdm_class"] = ProgressTqdm
                path = snapshot_download(**kwargs)
                return str(Path(path) / primary)
            finally:
                ProgressTqdm.clear_active_context()
```

to:

```python
            ProgressTqdm.set_active_context(on_progress, lambda: self._cancel_requested)
            try:
                if on_progress is not None:
                    kwargs["tqdm_class"] = ProgressTqdm
                path = snapshot_download(**kwargs)
                return str(Path(path) / primary)
            except BaseException:
                # See OMLXProvider.download's identical hardening.
                self._cancel_requested = True
                raise
            finally:
                ProgressTqdm.clear_active_context()
```

- [ ] **Step 5: Fix `_progress.py`'s stale docstring**

In `modelman/src/modelman/providers/_progress.py`, change the module docstring's second paragraph from:

```
For HuggingFace downloads, `snapshot_download` runs synchronously and
does not natively support cancellation. To make it interruptible, the
`ProgressTqdm` bar accepts an optional `should_cancel` callable; if it
returns True on any `display()` update, the bar raises
`DownloadCancelled`, which bubbles out of `snapshot_download` and out
of the apply loop. Nothing currently wires a `should_cancel` callback
into `provider.download()` — a Ctrl+C during `run_queued_ops()` now
interrupts an in-flight HuggingFace download via a raw
`KeyboardInterrupt` instead (see modelman/CLAUDE.md's "Downloads
(queued, applied on exit)" section).
```

to:

```
For HuggingFace downloads, `snapshot_download` runs synchronously and
does not natively support cancellation. To make it interruptible, the
`ProgressTqdm` bar accepts an optional `should_cancel` callable; if it
returns True on any `display()` update, the bar raises
`DownloadCancelled`, which bubbles out of `snapshot_download` and out
of the apply loop. `OMLXProvider.download()`/`LlamaCppProvider.download()`
wire this to their own `_cancel_requested` flag, flipped by
`cancel_current()` (called from `PendingChanges.cancel()`) and also by
their own `download()` when a `BaseException` — a real Ctrl+C included —
unwinds through the `snapshot_download` call, so any HF worker thread
still mid-download picks up the cancellation on its next progress
update rather than only learning about it after `apply()` has already
fully unwound. This narrows, but does not eliminate, the window where a
straggling worker thread writes into the target directory after the
caller's cleanup has already run (see
`PendingChanges._cleanup_partial_download` in queue.py).
```

- [ ] **Step 6: Run the new tests, then the full provider test files, to verify they pass**

Run: `cd modelman && uv run pytest tests/test_providers/test_omlx.py tests/test_providers/test_llamacpp.py -v`
Expected: all PASS.

- [ ] **Step 7: Lint and typecheck**

Run: `cd modelman && make check`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
cd modelman
git add src/modelman/providers/omlx.py src/modelman/providers/llamacpp.py \
  src/modelman/providers/_progress.py \
  tests/test_providers/test_omlx.py tests/test_providers/test_llamacpp.py
git commit -m "$(cat <<'EOF'
fix(modelman): flip omlx/llamacpp cancel flag immediately on interrupt

completes plan item #3 (review findings: omlx/llamacpp never mirrored
ollama.py's immediate-interrupt Ctrl+C hardening; _progress.py's
docstring wrongly claimed nothing wires should_cancel)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 4: Remove dead `_added_ids` disk-state merge and fix a stale comment in `screens/models.py`

**Files:**
- Modify: `modelman/src/modelman/screens/models.py:257` (field declaration), `:360-368` (stale comment), `:647` (population), `:812-821` (dead merge block)
- Test: `modelman/tests/screens/test_models.py:1932`, `modelman/tests/screens/test_app_navigation.py:1607`

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing new — this task only removes dead state and updates the two tests that asserted on it.

`_added_ids`'s only remaining use — a defensive `locked_state` merge in the discard path — is dead by the comment's own admission ("nothing writes state in the background anymore ... this merge is defensive insurance, not a load-bearing path"). Verified: `_on_add_model` never writes to `self.state`, so the on-disk `modelman.toml` never contains an id from `_added_ids` in the first place; the merge block always finds nothing to remove. `_restore_snapshot`'s own unconditional cleanup (dropping any `self.state.models` entry not in the snapshot) already covers the in-memory side for every added-this-session id, with no need for the separate disk-side check. Separately, `_load_models`'s `NoMatches` guard has a comment naming `_on_download_finished` and `_poll_downloads` — both deleted along with `DownloadManager`/`DownloadScreen`/`StatusScreen` — describing machinery that no longer exists.

- [ ] **Step 1: Update the two tests that assert on `_added_ids`**

In `modelman/tests/screens/test_models.py`, in `test_discard_persists_state_cleanup_for_session_added_model`, remove the line:

```python
        assert added_id in app.screen._added_ids
```

(Keep the rest of the test as-is — `assert app.screen.queued_ready == {added_id: True}` and the on-disk assertions after discard already cover the behavior that matters.)

In `modelman/tests/screens/test_app_navigation.py`, remove the line:

```python
    assert ms._added_ids == set()
```

from the test containing it (the one asserting `assert ms.queued_ready == {}` right before it — keep that and the following `assert dict(ms.state.models) == {}`).

- [ ] **Step 2: Run the two test files to verify they still pass with the field removed**

(This step runs *after* Step 3's code change — reordered here only because the plan format lists tests first; in practice run Step 1's edits, do Step 3, then run this.)

Run: `cd modelman && uv run pytest tests/screens/test_models.py tests/screens/test_app_navigation.py -q`
Expected: PASS (no test references `_added_ids` anymore).

- [ ] **Step 3: Remove `_added_ids` and fix the stale comment**

In `modelman/src/modelman/screens/models.py`:

Remove the field declaration and its comment (around line 253-257):

```python
        # Ids of models created this session via the add dialog. Used by
        # the discard flow to cancel/clear any background download for a
        # model that won't survive the snapshot restore (see
        # _on_exit_confirm's discard branch).
        self._added_ids: set[str] = set()
```

Remove the population line (around line 647), inside `_on_add_model`:

```python
        self._added_ids.add(variant["id"])
```

Remove the dead merge block (around lines 808-821), replacing:

```python
            # A session-added model never independently persists to
            # modelman.toml before apply() runs (nothing writes state in
            # the background anymore) — this merge is defensive
            # insurance, not a load-bearing path.
            with contextlib.suppress(Exception), locked_state(self.state_path) as disk_state:
                for mid in self._added_ids:
                    if mid not in self._snapshot_state_entries:
                        disk_state.models.pop(mid, None)
            self.queued_ready.clear()
            self.queued_deletes.clear()
            self.queued_moves.clear()
            self.queued_exposes.clear()
            self._ready_cascade_for_expose.clear()
            self._added_ids.clear()
            self.app.exit(None)
```

with:

```python
            self.queued_ready.clear()
            self.queued_deletes.clear()
            self.queued_moves.clear()
            self.queued_exposes.clear()
            self._ready_cascade_for_expose.clear()
            self.app.exit(None)
```

Check whether `locked_state` is still used elsewhere in this file (it is — `action_toggle_litellm` uses it) so the import stays; only remove the import if this was its last use (it is not).

Fix the stale comment in `_load_models` (around lines 360-368), replacing:

```python
    def _load_models(self) -> None:
        try:
            mt = self.query_one("#model-table", DataTable)
        except NoMatches:
            # Screen already popped — e.g. a background download's
            # on_complete callback (_on_download_finished) or the 1s
            # _poll_downloads timer fired after Escape closed this screen
            # while an untracked download was still in flight.
            return
```

with:

```python
    def _load_models(self) -> None:
        try:
            mt = self.query_one("#model-table", DataTable)
        except NoMatches:
            # _run_reconcile's background worker defers back to this via
            # self.app.call_from_thread(self.reload) — if the app has
            # already begun exiting (Apply/Discard) by the time that
            # deferred call runs, the table may be torn down already.
            return
```

- [ ] **Step 4: Run the affected test files to verify everything passes**

Run: `cd modelman && uv run pytest tests/screens/test_models.py tests/screens/test_app_navigation.py -q`
Expected: PASS.

- [ ] **Step 5: Lint and typecheck**

Run: `cd modelman && make check`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd modelman
git add src/modelman/screens/models.py tests/screens/test_models.py tests/screens/test_app_navigation.py
git commit -m "$(cat <<'EOF'
refactor(modelman): remove dead _added_ids disk-state merge, fix stale comment

completes plan item #4 (review findings: _added_ids' only remaining use
was admitted-dead defensive code; _load_models' NoMatches comment named
deleted background-download machinery)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 5: Add a regression test guarding `print_event`/`done_verbs` tag-vocabulary coverage

**Files:**
- Test: `modelman/tests/commands/test_run_tui.py`

**Interfaces:**
- Consumes: `print_event`, `main.py`'s module-level knowledge of `queue.py`'s emitted tags (read from this test, not refactored).

`print_event`'s ~20-branch dispatch and `run_queued_ops`'s local `done_verbs` set both hard-code the same tag vocabulary queue.py's `emit()` calls produce, independently, with nothing enforcing agreement — a missed branch for a new tag silently falls into `print_event`'s `else: typer.echo(f"  (unhandled event: {tag})", err=True)` catch-all instead of failing a test. Rather than merging the two into a single generic dispatch table (a larger, riskier refactor of a small but currently-correct piece of code — out of scope for a bug-fix pass), this task adds a test that enumerates every tag `queue.py` actually emits and asserts `print_event` recognizes all of them, so a future added-but-unhandled tag fails CI instead of only showing up as a runtime warning.

- [ ] **Step 1: Write the test**

Add to `modelman/tests/commands/test_run_tui.py` (after `test_print_error_summary_prints_nothing_on_clean_run`):

```python
def test_print_event_recognizes_every_tag_queue_emits(capsys):
    """queue.py's module docstring and its emit() call sites are the
    source of truth for the lifecycle-tag vocabulary; print_event's
    dispatch and run_queued_ops's done_verbs set both hard-code that same
    vocabulary independently, with nothing enforcing agreement. This
    enumerates every verb queue.py actually emits (grepped from its
    `emit(f"...")`/`emit("...")` call sites) and asserts print_event
    handles each without falling into the "(unhandled event: ...)"
    catch-all — a future verb added to queue.py but missed here, or in
    print_event, now fails a test instead of only a runtime stderr line."""
    import re
    from pathlib import Path

    import modelman.queue as queue_module

    source = Path(queue_module.__file__).read_text()
    verbs = set(re.findall(r'emit\(f?"([a-z]+:[a-z]+)', source))
    assert verbs, "expected to find at least one emit() call in queue.py"

    for verb in sorted(verbs):
        print_event(f"{verb}|a|b|c")
    err = capsys.readouterr().err
    assert "unhandled event" not in err, err
```

- [ ] **Step 2: Run the test to verify it passes against today's code**

Run: `cd modelman && uv run pytest tests/commands/test_run_tui.py::test_print_event_recognizes_every_tag_queue_emits -v`
Expected: PASS (this is a regression guard, not a bug fix — it should already pass; if it fails, that itself is a real gap worth investigating before continuing).

- [ ] **Step 3: Lint and typecheck**

Run: `cd modelman && make check`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd modelman
git add tests/commands/test_run_tui.py
git commit -m "$(cat <<'EOF'
test(modelman): guard print_event against an unhandled queue.py tag

completes plan item #5 (review finding: print_event's dispatch and
done_verbs both hard-code queue.py's tag vocabulary independently, with
a missed branch only surfacing as a runtime stderr line, not a test
failure)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Final verification

- [ ] Run the full modelman suite once, after all five tasks: `cd modelman && uv run pytest -q`
- [ ] Run `make check` (lint + typecheck) one more time at the repo root's `modelman/` package.
- [ ] Re-run the `git grep -n "litellm_exposed = " docs/guides/` check from the root `CLAUDE.md` if any task touched exposure state (none of these do — skip unless a step surprised you).

## Note: rejected finding (`_download`'s `TypeError` fallback)

The review flagged `queue.py::PendingChanges._download`'s `except TypeError` fallback (distinguishing "provider has no `on_progress` param" from "a real bug inside `download()`" via `exc.__traceback__.tb_next`) as fragile, proposing `inspect.signature(provider.download).parameters` instead, with this counterexample: "any `provider.download()` implementation that itself is a thin wrapper... adds a stack frame, so a genuine signature-mismatch TypeError raised from inside that wrapper would be misclassified as 'a real bug'."

Verified empirically (see the interactive check performed while drafting this plan): if `provider.download()` itself declares `on_progress` as a parameter and only fails when *internally* delegating to a helper that doesn't, Python raises the `TypeError` from *inside* `download()`'s own frame — `tb_next` is correctly non-`None`, and the current code correctly treats this as a real bug (the provider's own signature does support `on_progress`; the failure is unrelated). If `provider.download()` itself doesn't declare `on_progress` at all, the `TypeError` is always raised at the call site with zero extra frames, regardless of what the callee's body would have done — there is no wrapper shape that produces the counterexample described. `inspect.signature` would classify both cases identically to the current `tb_next` check. No code change made for this finding.
