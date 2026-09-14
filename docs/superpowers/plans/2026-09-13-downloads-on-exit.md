# modelman downloads-on-exit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace modelman's background-download subsystem (`DownloadManager`, `DownloadScreen`, `StatusScreen`, the quit guard, the deferred-expose indirection) with a queue-everything-and-apply-on-exit model: every TUI action (including a real download) just populates an in-memory queue, and Escape/ctrl+q's Apply choice exits the app carrying that queue so `main.py` can run it in the plain terminal afterward.

**Architecture:** `queue.py`'s `apply()` gets its old download step back (it ran downloads directly before `DownloadManager` was introduced) plus a new `QueuedOps` plain-data carrier. `ModelScreen` never talks to a download manager again — ready-on/off, delete, move, and expose all just populate `queued_ready`/`queued_deletes`/`queued_moves`/`queued_exposes` dicts, and its exit-confirm dialog's Apply/Discard choices both call `self.app.exit(...)` (Apply carries a `QueuedOps`, Discard/empty carries `None`) instead of staying in the TUI. `main.py`'s `run_tui()` gets a post-exit runner that rebuilds a `PendingChanges` from fresh on-disk state and the returned `QueuedOps`, then calls `apply()` with a plain-text event printer and forwards provider progress straight to stdout.

**Tech Stack:** Python 3.13, Textual (TUI), Typer (CLI), pytest + pytest-asyncio (tests), uv (packaging).

**Spec:** `docs/superpowers/specs/2026-09-13-downloads-on-exit-design.md`

## Global Constraints

- No registry/state objects cross the TUI/terminal boundary on exit — only the plain-data `QueuedOps` dataclass (per the spec's Data Flow section).
- `PendingChanges`'s structure, its `apply()` step order (deletes → moves → ready/downloads → exposes → save), the `manages_own_cache` carve-out, and providers' `download(variant, on_progress=...)` signature are all **kept** — this task restores behavior that existed before `DownloadManager` (commit `34992cf`'s parent), it doesn't invent new provider APIs.
- Apply and Discard on the exit-confirm dialog now both **exit the app** (a behavior change from today, where they stay in the TUI). Escape/ctrl+q with an empty queue exits immediately, no dialog.
- Cancellation is now OS-signal-based (Ctrl+C raises `KeyboardInterrupt` in the foreground CLI process), not a UI Cancel button — `PendingChanges.cancel()`/`cancelled`/`aborted()` machinery is reused, not reinvented.
- The `family` scroll-to-startup argument chain (`modelman download <family>`'s only reason to exist) is deleted as dead code throughout: `ModelmanApp(family=...)`, `ModelScreen(scroll_to_family=...)`, `_scroll_cursor_to_family()`, `run_tui(family)`.
- Sequencing follows the spec's own risk note: `queue.py` first, then `app.py`/`main.py`, then screens (`forms.py`/`models.py`), then the old subsystem's files/tests are deleted, then the two large screen test files are rewritten, then docs, then full verification. Tasks 2–5 intentionally leave the full test suite red until Task 5 lands — that's expected, not a regression to chase mid-sequence.

---

## Task 1: `queue.py` — restore real downloads in `apply()`, add `QueuedOps`

**Files:**
- Modify: `modelman/src/modelman/queue.py`
- Test: `modelman/tests/test_queue.py`

**Interfaces:**
- Produces: `QueuedOps` dataclass — `ready: dict[str, bool]`, `deletes: dict[str, VariantSpec]`, `moves: dict[str, str]`, `exposes: dict[str, bool]` (all `default_factory=dict`). Consumed by `ModelScreen._on_exit_confirm` (Task 4) and `main.py::run_queued_ops` (Task 3).
- Produces: `PendingChanges.apply()` now calls `provider.download(variant, on_progress=...)` for a ready-on against a mapped, non-`manages_own_cache` provider, instead of asserting. Consumed by Task 3's `run_queued_ops`.

- [ ] **Step 1: Add the `QueuedOps` dataclass and the two new imports**

In `modelman/src/modelman/queue.py`, add to the imports (the module already has `from __future__ import annotations`, so `VariantSpec` stays a `TYPE_CHECKING`-only import — no runtime cost):

```python
from .providers._progress import DownloadCancelled, human_bytes
```

Add this new dataclass directly above `class PendingChanges:`:

```python
@dataclass
class QueuedOps:
    """Everything the TUI queued, carried out on Apply as the app's exit
    value (see ModelmanApp/ModelScreen). No registry/state objects cross
    the TUI/terminal boundary — just this plain data; main.py's
    run_queued_ops() rebuilds a PendingChanges from fresh on-disk state.
    """

    ready: dict[str, bool] = field(default_factory=dict)
    deletes: dict[str, VariantSpec] = field(default_factory=dict)
    moves: dict[str, str] = field(default_factory=dict)
    exposes: dict[str, bool] = field(default_factory=dict)
```

- [ ] **Step 2: Restore the download branch in the ready loop**

Replace the current ready-loop body (the `assert not (...)` and the `if provider is None or (manages_own_cache and target): ... else: ...` two-way branch) with a three-way branch that restores the real download. Find this block:

```python
            provider_cls = ProviderRegistry.get_class(provider_id)
            manages_own_cache = bool(
                provider_cls is not None and provider_cls.manages_own_cache
            )
            # Real downloads no longer run inside apply() — the caller
            # (ModelScreen) must route a ready-on against a real provider
            # through DownloadManager.start() instead of queuing it here.
            # A target=True entry reaching this point with a downloadable
            # provider present is a caller bug, not a runtime condition.
            assert not (provider is not None and not manages_own_cache and target), (
                f"ready-on for {model_id!r} must go through DownloadManager, "
                "not PendingChanges.apply()"
            )
            if provider is None or (manages_own_cache and target):
```

and replace through the matching `else:` clear-branch's end (the line right before `# Cascade: turning ready off (either branch) drops the model's`) with:

```python
            provider_cls = ProviderRegistry.get_class(provider_id)
            manages_own_cache = bool(
                provider_cls is not None and provider_cls.manages_own_cache
            )
            if provider is None or (manages_own_cache and target):
```

(unchanged flag-only-flip branch body stays exactly as-is — only the removed `assert` above it changes), then change the following `else:` to `elif target:` and insert the new download branch before the existing clear-branch's `else:`. Concretely, the full new ready-loop dispatch (replacing from `if provider is None or (manages_own_cache and target):` through the end of the existing clear `else:` branch, i.e. current lines ~415–481) is:

```python
            if provider is None or (manages_own_cache and target):
                # Flag-only flip (native/unmapped provider, or a
                # manages_own_cache ready-on): no provider call exists for
                # the ready-on itself — but a provider-is-None ready-off
                # still means "remove the artifact",
                # so drop the file recorded in state.disk_path, mirroring
                # what a mapped provider's delete() would do.
                emit(f"ready:start|{model_id}|{label}")
                if not target:
                    try:
                        _remove_local_artifact(self.state, variant)
                    except Exception as exc:  # noqa: BLE001
                        reason = _reason(exc)
                        self.failures.append(f"clear {model_id}: {reason}")
                        emit(f"delete:fail|{model_id}|{label}|{reason}")
                    self.state.set(
                        model_id,
                        replace(
                            self.state.get(model_id),
                            ready=target,
                            disk_path=None,
                            size_bytes=None,
                        ),
                    )
                else:
                    self.state.set(model_id, replace(self.state.get(model_id), ready=target))
                self._touched_model_ids.add(model_id)
                emit(f"ready:done|{model_id}|{label}")
            elif target:
                # provider present, not manages_own_cache, target=True:
                # a real download/pull. Restored from before DownloadManager
                # existed — the TUI now queues every ready-on instead of
                # backgrounding real ones, so apply() owns them again.
                emit(f"download:start|{model_id}|{label}")
                try:
                    local_path = self._download(variant, on_progress)
                except DownloadCancelled:
                    emit(f"download:cancelled|{model_id}|{label}")
                    emit("apply:cancelled")
                    return
                except Exception as exc:  # noqa: BLE001
                    reason = _reason(exc)
                    self.failures.append(f"download {model_id}: {exc}")
                    emit(f"download:fail|{model_id}|{label}|{reason}")
                    continue
                size_bytes = self._size_of(local_path)
                if size_bytes is None:
                    try:
                        size_bytes = provider.size_of(variant)
                    except Exception:  # noqa: BLE001
                        size_bytes = None
                self.state.set(
                    model_id,
                    replace(
                        self.state.get(model_id),
                        ready=True,
                        disk_path=local_path,
                        size_bytes=size_bytes,
                    ),
                )
                self._touched_model_ids.add(model_id)
                if size_bytes:
                    emit(f"download:done|{model_id}|{label}|{human_bytes(size_bytes)}")
                else:
                    emit(f"download:done|{model_id}|{label}")
            else:
                # provider present, target False: clear.
                emit(f"delete:start|{model_id}|{label}")
                # Mirror the deletes loop's guard: a stale-ready model whose
                # artifact is already gone must clear cleanly, not fail and
                # strand state.ready=True with the unexpose cascade skipped.
                if self._remove_artifact_if_present(
                    model_id, variant, label, provider, "clear", emit
                ):
                    continue
                self.state.set(
                    model_id,
                    replace(
                        self.state.get(model_id),
                        ready=False,
                        disk_path=None,
                        size_bytes=None,
                    ),
                )
                self._touched_model_ids.add(model_id)
                emit(f"delete:done|{model_id}|{label}")
```

- [ ] **Step 3: Add the `_download` and `_size_of` helpers**

Add these next to the existing `_delete` method at the bottom of `PendingChanges` (after `_delete`):

```python
    def _download(self, variant: VariantSpec, on_progress: EventFn | None = None) -> str:
        provider = self.providers[variant["provider"]]
        try:
            return provider.download(variant, on_progress=on_progress)  # type: ignore[attr-defined]
        except TypeError:
            return provider.download(variant)  # type: ignore[attr-defined]

    @staticmethod
    def _size_of(local_path: str) -> int | None:
        # TypeError/ValueError alongside OSError: a non-path object (e.g. a
        # test stub's return value) must not crash apply().
        try:
            p = Path(local_path)
            return p.stat().st_size if p.is_file() else None
        except (OSError, TypeError, ValueError):
            return None
```

- [ ] **Step 4: Run the existing queue tests to see the expected failures**

Run: `cd modelman && uv run pytest tests/test_queue.py -q`
Expected: `test_apply_no_longer_downloads_ready_on_entries` fails (it asserts the now-removed guard) — this is expected; it's deleted in the next step. Everything else should still pass.

- [ ] **Step 5: Delete the now-obsolete assert test and fix two stale docstrings**

Delete `test_apply_no_longer_downloads_ready_on_entries` (currently the block starting `def test_apply_no_longer_downloads_ready_on_entries(tmp_path):` through its final `provider.download.assert_not_called()` line) — its entire premise (a mapped ready-on must not call `download()`) is now false.

In `test_apply_deletes_before_ready_loop`, replace the docstring:

```python
    """On apply, delete steps must run before the ready loop (free disk
    first). Downloads no longer run inside apply() — a real ready-on goes
    through DownloadManager instead (see queue.py's ready-loop assert) —
    so the ordering guarantee that survives is deletes-before-ready-loop,
    pinned here with a flag-only ready-on."""
```

with:

```python
    """On apply, delete steps must run before the ready loop (free disk
    first), pinned here with a flag-only ready-on; see
    test_apply_ready_on_mapped_provider_downloads below for the
    real-download case."""
```

In `test_apply_cascade_ready_before_exposing`, replace the docstring:

```python
    """The toggle-expose cascade relies on apply() running the ready loop
    before the expose loop so a model's ready flip lands before its
    LiteLLM model_list entry is written. Real downloads no longer run
    inside apply() (they go through DownloadManager — see queue.py's
    ready-loop assert), so this pins the surviving ordering with a
    flag-only ready-on: a future reorder in apply() cannot silently
    break the cascade."""
```

with:

```python
    """The toggle-expose cascade relies on apply() running the ready loop
    before the expose loop so a model's ready flip lands before its
    LiteLLM model_list entry is written. Pinned here with a flag-only
    ready-on so the assertion doesn't depend on a real provider call."""
```

- [ ] **Step 6: Add the new download tests**

Append to `tests/test_queue.py` (these use the same `_registry_with`/`_entry`/`_make_state` helpers already defined near the top of the file):

```python
def test_apply_ready_on_mapped_provider_downloads(tmp_path):
    """A ready-on against a mapped, non-manages_own_cache provider must
    call provider.download() directly — apply() owns real downloads
    again now that the TUI queues every ready-on instead of routing a
    mapped provider's through a background DownloadManager."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="ollama/x", family="f", provider="ollama", name="x:7b"),
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()
    provider = MagicMock()
    provider.download.return_value = "ollama:x:7b"
    provider.size_of.return_value = None

    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, True)],
    )
    progress_lines: list[str] = []
    pending.apply(on_progress=progress_lines.append)

    provider.download.assert_called_once()
    call_args = provider.download.call_args
    assert call_args.args[0]["id"] == "ollama/x"
    assert call_args.kwargs["on_progress"] is progress_lines.append
    assert state.get("ollama/x").ready is True
    assert state.get("ollama/x").disk_path == "ollama:x:7b"
    assert pending.failures == []


def test_apply_ready_on_downloads_sequentially(tmp_path):
    """Two queued ready-on downloads run one after another: download A
    completes before download B starts, matching apply()'s pre-
    DownloadManager synchronous behavior."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="ollama/a", family="f", provider="ollama", name="a:7b"),
        _entry(id="ollama/b", family="f", provider="ollama", name="b:7b"),
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()
    order: list[str] = []

    def _tracking_download(variant, on_progress=None):
        order.append(f"start:{variant['id']}")
        order.append(f"done:{variant['id']}")
        return f"ollama:{variant['id']}"

    provider = MagicMock()
    provider.download.side_effect = _tracking_download
    provider.size_of.return_value = None

    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[
            ("ollama/a", {"id": "ollama/a", "provider": "ollama", "name": "a:7b"}, True),
            ("ollama/b", {"id": "ollama/b", "provider": "ollama", "name": "b:7b"}, True),
        ],
    )
    pending.apply()

    assert order == ["start:ollama/a", "done:ollama/a", "start:ollama/b", "done:ollama/b"]


def test_apply_ready_on_forwards_progress_lines(tmp_path):
    """Per-line progress from provider.download() must reach the
    on_progress callback the caller (main.py's post-exit runner)
    supplies."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="ollama/x", family="f", provider="ollama", name="x:7b"),
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()

    def _download(variant, on_progress=None):
        on_progress("pulling manifest")
        on_progress("verifying sha256")
        return "ollama:x:7b"

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.size_of.return_value = None

    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, True)],
    )
    lines: list[str] = []
    pending.apply(on_progress=lines.append)

    assert lines == ["pulling manifest", "verifying sha256"]


def test_apply_ready_on_download_failure_does_not_stop_other_steps(tmp_path):
    """A download failure records into self.failures and the run
    continues with the remaining deletes/moves/exposes."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="ollama/x", family="f", provider="ollama", name="x:7b"),
        _entry(id="ollama/y", family="f", provider="ollama", name="y:7b"),
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()
    provider = MagicMock()
    provider.is_downloaded.return_value = True
    provider.delete.return_value = None
    provider.download.side_effect = RuntimeError("connection refused")

    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, True)],
        deletes=[("ollama/y", {"id": "ollama/y", "provider": "ollama", "name": "y:7b"})],
    )
    pending.apply()

    assert "download ollama/x: connection refused" in pending.failures
    provider.delete.assert_called_once()
    reloaded = load_registry(reg_path)
    assert all(m.id != "ollama/y" for m in reloaded.models)


def test_apply_cancelled_mid_ready_loop_skips_remaining_and_does_not_save(tmp_path):
    """A cancel request landing during one ready-loop item's download
    skips every remaining item; already-completed steps stay applied in
    memory, but (per existing cancel semantics) nothing is saved."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="ollama/a", family="f", provider="ollama", name="a:7b"),
        _entry(id="ollama/b", family="f", provider="ollama", name="b:7b"),
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()
    provider = MagicMock()
    provider.size_of.return_value = None

    pending: PendingChanges

    def _download(variant, on_progress=None):
        pending.cancelled = True  # simulate a Ctrl+C landing mid-download
        return f"ollama:{variant['id']}"

    provider.download.side_effect = _download

    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[
            ("ollama/a", {"id": "ollama/a", "provider": "ollama", "name": "a:7b"}, True),
            ("ollama/b", {"id": "ollama/b", "provider": "ollama", "name": "b:7b"}, True),
        ],
    )
    events: list[str] = []
    pending.apply(on_event=events.append)

    provider.download.assert_called_once()  # b's download never started
    assert events[-1] == "apply:cancelled"
    assert not state_path.exists()  # cancel semantics: nothing persisted
```

- [ ] **Step 7: Run the full queue test file**

Run: `cd modelman && uv run pytest tests/test_queue.py -q`
Expected: PASS, all tests green.

- [ ] **Step 8: Commit**

```bash
git add modelman/src/modelman/queue.py modelman/tests/test_queue.py
git commit -m "$(cat <<'EOF'
feat(modelman): restore real downloads in PendingChanges.apply()

Adds QueuedOps, the plain-data carrier the TUI's exit-confirm dialog
will hand to the post-exit runner. Part of the downloads-on-exit
redesign - completes plan item #1.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 2: `app.py` — drop the download-quit-guard and the family-scroll chain

**Files:**
- Modify: `modelman/src/modelman/app.py`

**Interfaces:**
- Consumes: `QueuedOps` from Task 1 (`modelman.queue`).
- Produces: `ModelmanApp(App[QueuedOps | None])` with no `family` constructor argument. `request_quit()` unconditionally delegates to the top `ModelScreen`'s `action_back()`.

> This task leaves `tests/screens/test_app_navigation.py` red (it still imports `DownloadState` and constructs `ModelmanApp(family=...)`) and `ModelScreen` still references `self.app.downloads`, which no longer exists — expected until Tasks 4–5 land. Verify with `make check` (lint + typecheck), not the test suite, for this task.

- [ ] **Step 1: Update imports and the class declaration**

In `modelman/src/modelman/app.py`, remove:

```python
from .downloads import DownloadManager
```

and:

```python
from .screens.forms import QuitBlockedModal
```

Add:

```python
from .queue import QueuedOps
```

Change:

```python
class ModelmanApp(App[None]):
```

to:

```python
class ModelmanApp(App[QueuedOps | None]):
```

- [ ] **Step 2: Drop the `family` constructor argument and `self.downloads`**

Replace:

```python
    def __init__(self, family: str | None = None) -> None:
        super().__init__()
        self._initial_family = family
        # One DownloadManager for the app's lifetime, created before any
        # screen mounts so every screen can reference self.app.downloads
        # the instant it might need it (quit guard, glyph, routing).
        self.downloads = DownloadManager(self)
        # Set by the startup price-refresh worker when it skips or fails; read
```

with:

```python
    def __init__(self) -> None:
        super().__init__()
        # Set by the startup price-refresh worker when it skips or fails; read
```

- [ ] **Step 3: Drop the scroll-to-family push_screen argument**

Replace:

```python
        self.push_screen(
            ModelScreen(
                registry=registry,
                state=load_state(),
                registry_path=_default_registry_path(),
                state_path=_default_state_path(),
                scroll_to_family=self._initial_family,
            )
        )
```

with:

```python
        self.push_screen(
            ModelScreen(
                registry=registry,
                state=load_state(),
                registry_path=_default_registry_path(),
                state_path=_default_state_path(),
            )
        )
```

- [ ] **Step 4: Simplify `request_quit()`**

Replace the entire `request_quit` method:

```python
    def request_quit(self) -> None:
        """The single quit entry point every binding routes through
        (ctrl+q's default action_quit, and ModelScreen's Escape when it
        has no pending changes — it's the app's root screen now, so
        Escape-with-nothing-queued means quit). Blocks quitting while a
        download is active instead of exiting
        out from under it, and — if the top screen is a ModelScreen with
        an unapplied queue (delete/ready/move/expose) — routes through
        its own action_back() so ctrl+q gets the same apply/discard/
        cancel confirmation Escape would give, instead of silently
        dropping the queue."""
        if self.downloads.has_active():

            def _on_choice(review: bool | None) -> None:
                if review:
                    from .screens.downloads import DownloadScreen

                    self.push_screen(DownloadScreen())

            self.push_screen(QuitBlockedModal(), _on_choice)
            return

        top = self.screen
        if isinstance(top, ModelScreen) and top.has_pending_changes():
            top.action_back()
            return

        self.exit()
```

with:

```python
    def request_quit(self) -> None:
        """ctrl+q's entry point: delegate to the top ModelScreen's Escape
        handling (the apply/discard/cancel confirmation dialog when a
        queue is pending, or an immediate exit when it's empty) instead
        of quitting out from under an unapplied queue. Falls through to
        a direct exit when the top screen isn't a ModelScreen (e.g. a
        modal is open)."""
        top = self.screen
        if isinstance(top, ModelScreen):
            top.action_back()
            return
        self.exit()
```

- [ ] **Step 5: Verify**

Run: `cd modelman && make check`
Expected: lint and typecheck pass for `app.py` (a typecheck error may surface from `ModelScreen` still expecting `scroll_to_family`/still touching `self.app.downloads` — that's `screens/models.py`, fixed in Task 4; if `make check` fails only on `screens/models.py` or `screens/downloads.py`/`screens/status.py`/`downloads.py`, that's expected at this point).

- [ ] **Step 6: Commit**

```bash
git add modelman/src/modelman/app.py
git commit -m "$(cat <<'EOF'
feat(modelman): drop the download-quit-guard and family-scroll chain from ModelmanApp

Part of the downloads-on-exit redesign - completes plan item #2.
ModelScreen and the rest of the DownloadManager subsystem still
reference the old shape; fixed in the following tasks.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 3: `main.py` — remove the `download` command, add the post-exit runner

**Files:**
- Modify: `modelman/src/modelman/main.py`
- Delete: `modelman/tests/commands/test_download.py`
- Create: `modelman/tests/commands/test_run_tui.py`

**Interfaces:**
- Consumes: `QueuedOps`, `PendingChanges` (`modelman.queue`, Task 1).
- Produces: `run_tui() -> None` (no arguments), `run_queued_ops(queued: QueuedOps) -> bool` (True iff the run should exit non-zero), `print_event(tag: str) -> None`, `print_error_summary(failures: list[str], total: int) -> bool`. Consumed by `ModelmanApp` (Task 2, already updated) and by this task's own tests.

- [ ] **Step 1: Update imports**

In `modelman/src/modelman/main.py`, change:

```python
from .registry import load_registry, save_registry
```

to:

```python
from .providers.registry import ProviderRegistry
from .queue import PendingChanges, QueuedOps
from .registry import (
    _default_registry_path,
    load_registry,
    model_entry_to_variant,
    provider_config,
    save_registry,
)
```

and change:

```python
from .state import load_state, locked_state
```

to:

```python
from .state import _default_state_path, load_state, locked_state
```

- [ ] **Step 2: Replace `run_tui`, `_main`, and delete the `download` command**

Replace:

```python
def run_tui(family: str | None) -> None:
    """Launch the Textual TUI, optionally scrolling the model list's cursor
    to the given family's first row on open."""
    # Imported lazily so non-TUI subcommands (expose, sync, benchmark,
    # usage, migrate) don't pay the Textual import cost at CLI startup.
    from .app import ModelmanApp

    ModelmanApp(family=family).run()


@app.callback(invoke_without_command=True)
def _main(ctx: typer.Context) -> None:
    """Run `modelman` with no args to open the TUI."""
    if ctx.invoked_subcommand is None:
        run_tui(None)


@app.command()
def download(
    family: str = typer.Argument(..., help="Family name (filename under families dir)"),
):
    """Open the TUI's model list scrolled to a family (queued downloads on exit)."""
    run_tui(family)
```

with:

```python
def print_event(tag: str) -> None:
    """Print one thin, human-readable line for a queue.py lifecycle tag
    (see queue.py's module docstring for the tag format). Replaces
    StatusScreen's RichLog rendering now that apply() runs after the TUI
    has exited, in a plain terminal."""
    parts = tag.split("|", 3)
    verb = parts[0]
    if verb == "delete:start":
        typer.echo(f"Deleting {parts[2]}...")
    elif verb == "delete:done":
        typer.echo(f"  done: deleted {parts[2]}")
    elif verb == "delete:fail":
        typer.echo(f"  FAILED: delete {parts[2]}: {parts[3]}")
    elif verb == "download:start":
        typer.echo(f"Downloading {parts[2]}...")
    elif verb == "download:done":
        suffix = f" ({parts[3]})" if len(parts) >= 4 and parts[3] else ""
        typer.echo(f"  done: downloaded {parts[2]}{suffix}")
    elif verb == "download:fail":
        typer.echo(f"  FAILED: download {parts[2]}: {parts[3]}")
    elif verb == "download:cancelled":
        typer.echo(f"  cancelled: {parts[2]}")
    elif verb == "ready:start":
        typer.echo(f"Marking {parts[2]} ready...")
    elif verb == "ready:done":
        typer.echo(f"  done: {parts[2]} ready")
    elif verb == "move:start":
        typer.echo(f"Moving {parts[2]} -> {parts[3]}...")
    elif verb == "move:done":
        typer.echo(f"  done: moved {parts[2]} -> {parts[3]}")
    elif verb == "move:fail":
        typer.echo(f"  FAILED: move {parts[2]}: {parts[3]}")
    elif verb in ("expose:start", "unexpose:start"):
        action = "Exposing" if verb == "expose:start" else "Unexposing"
        typer.echo(f"{action} {parts[2]}...")
    elif verb in ("expose:done", "unexpose:done"):
        action = "exposed" if verb == "expose:done" else "unexposed"
        typer.echo(f"  done: {action} {parts[2]}")
    elif verb in ("expose:fail", "unexpose:fail"):
        action = verb.split(":")[0]
        typer.echo(f"  FAILED: {action} {parts[2]}: {parts[3]}")
    elif verb == "expose:warning":
        typer.echo(f"  warning: {parts[1]}")
    elif verb == "save:done":
        typer.echo("Saved.")
    elif verb == "save:fail":
        typer.echo(f"  FAILED: save: {parts[1]}")
    # apply:done / apply:cancelled / save:start: no line — run_queued_ops
    # prints its own summary once apply() returns.


def print_error_summary(failures: list[str], total: int) -> bool:
    """Print the "Completed with errors" block when `failures` is
    non-empty. Returns True iff there were failures, so the caller can
    decide the process exit code. Clean runs print nothing."""
    if not failures:
        return False
    typer.echo("Completed with errors:")
    for failure in failures:
        typer.echo(f"  - {failure}")
    typer.echo(f"{len(failures)} of {total} operations failed.")
    return True


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
    ready_specs = {mid: model_entry_to_variant(registry.model(mid)) for mid in queued.ready}

    providers: dict[str, object] = {}
    for spec in list(ready_specs.values()) + list(queued.deletes.values()):
        try:
            entry = registry.provider(spec["provider"])
        except KeyError:
            # Not in the registry or not mapped to a Provider class:
            # PendingChanges treats this as flag-only (native/unmapped).
            continue
        providers[spec["provider"]] = ProviderRegistry.get(spec["provider"], provider_config(entry))

    pending = PendingChanges(
        registry=registry,
        state=state,
        registry_path=_default_registry_path(),
        state_path=_default_state_path(),
        providers=providers,
        ready=[(mid, ready_specs[mid], target) for mid, target in queued.ready.items()],
        deletes=list(queued.deletes.items()),
        moves=list(queued.moves.items()),
        exposes=list(queued.exposes.items()),
        litellm_path=default_litellm_config_path(),
    )
    total = len(pending.ready) + len(pending.deletes) + len(pending.moves) + len(pending.exposes)
    completed = 0
    done_verbs = {
        "delete:done",
        "download:done",
        "ready:done",
        "move:done",
        "expose:done",
        "unexpose:done",
    }

    def on_event(tag: str) -> None:
        nonlocal completed
        print_event(tag)
        if tag.split("|", 1)[0] in done_verbs:
            completed += 1

    try:
        pending.apply(on_event=on_event, on_progress=typer.echo)
    except KeyboardInterrupt:
        pending.cancel()
        typer.echo(
            f"\nCancelled: {completed} steps completed, {total - completed} remaining skipped."
        )
        return True

    return print_error_summary(pending.failures, total)


def run_tui() -> None:
    """Launch the Textual TUI. If the user queues changes and exits via
    Apply, run them against fresh on-disk state now that the TUI has
    closed — provider progress and lifecycle events print straight to
    stdout instead of a StatusScreen."""
    # Imported lazily so non-TUI subcommands (expose, sync, benchmark,
    # usage, migrate) don't pay the Textual import cost at CLI startup.
    from .app import ModelmanApp

    queued = ModelmanApp().run()
    if queued is None:
        return
    if run_queued_ops(queued):
        raise typer.Exit(1)


@app.callback(invoke_without_command=True)
def _main(ctx: typer.Context) -> None:
    """Run `modelman` with no args to open the TUI."""
    if ctx.invoked_subcommand is None:
        run_tui()
```

- [ ] **Step 2: Run typecheck/lint on main.py**

Run: `cd modelman && uv run ruff check src/modelman/main.py && uv run mypy src/modelman/main.py`
Expected: PASS (fix any import-order/unused-import issues ruff reports).

- [ ] **Step 3: Delete the old command test and write the new runner test file**

Delete `modelman/tests/commands/test_download.py` entirely.

Create `modelman/tests/commands/test_run_tui.py`:

```python
"""Tests for the post-exit queued-ops runner: main.py applies a
QueuedOps returned by the TUI against fresh on-disk state, after the
TUI has already exited, printing provider progress and lifecycle
events straight to stdout. See
docs/superpowers/specs/2026-09-13-downloads-on-exit-design.md.
"""

from __future__ import annotations

from unittest.mock import MagicMock, patch

from typer.testing import CliRunner

from modelman.main import app, print_error_summary, print_event, run_queued_ops
from modelman.queue import QueuedOps
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry, save_registry
from modelman.state import StateStore, load_state, save_state


def _seed(tmp_path, monkeypatch, *, models=(), providers=("ollama",)):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[ProviderEntry(id=p, name=p, auth=AuthConfig(type="none")) for p in providers],
            models=list(models),
        ),
        reg_path,
    )
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return reg_path, state_path


def test_no_args_invokes_run_tui():
    # `modelman` with no subcommand is the TUI entry point; run_tui takes
    # no arguments now that the `download <family>` scroll-to-startup
    # shortcut is gone.
    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, [])
        assert result.exit_code == 0
        run_tui.assert_called_once_with()


def test_download_command_is_gone():
    runner = CliRunner()
    result = runner.invoke(app, ["download", "ornith"])
    assert result.exit_code != 0


def test_run_tui_discard_or_empty_does_not_apply(tmp_path, monkeypatch):
    # A None QueuedOps (Discard, or Escape with an empty queue) must not
    # touch the registry/state at all.
    reg_path, state_path = _seed(tmp_path, monkeypatch)
    before = reg_path.read_text()
    with patch("modelman.app.ModelmanApp") as app_cls:
        app_cls.return_value.run.return_value = None
        from modelman.main import run_tui

        run_tui()
    assert reg_path.read_text() == before


def test_run_queued_ops_downloads_a_queued_ready_on(tmp_path, monkeypatch, capsys):
    """A queued ready-on against a mapped provider downloads for real —
    the behavior apply() lost when DownloadManager took it over, and
    gets back now that the TUI queues everything instead."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])

    fake_provider = MagicMock()
    fake_provider.download.return_value = "ollama:x:7b"
    fake_provider.size_of.return_value = None
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(QueuedOps(ready={"ollama/x": True}))

    assert failed is False
    fake_provider.download.assert_called_once()
    state = load_state(state_path)
    assert state.get("ollama/x").ready is True
    assert state.get("ollama/x").disk_path == "ollama:x:7b"
    out = capsys.readouterr().out
    assert "Downloading x:7b..." in out
    assert "done: downloaded x:7b" in out


def test_run_queued_ops_reports_failures_and_returns_true(tmp_path, monkeypatch, capsys):
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])
    fake_provider = MagicMock()
    fake_provider.download.side_effect = RuntimeError("connection refused")
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(QueuedOps(ready={"ollama/x": True}))

    assert failed is True
    out = capsys.readouterr().out
    assert "Completed with errors:" in out
    assert "download ollama/x: connection refused" in out
    assert "1 of 1 operations failed." in out


def test_run_queued_ops_keyboard_interrupt_prints_cancelled_summary(tmp_path, monkeypatch, capsys):
    entry_a = ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a:7b")
    entry_b = ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry_a, entry_b])
    fake_provider = MagicMock()
    fake_provider.download.side_effect = ["ollama:a:7b", KeyboardInterrupt()]
    fake_provider.size_of.return_value = None
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(QueuedOps(ready={"ollama/a": True, "ollama/b": True}))

    assert failed is True
    out = capsys.readouterr().out
    assert "Cancelled: 1 steps completed, 1 remaining skipped." in out
    # Cancel semantics: nothing saved for this run.
    state = load_state(state_path)
    assert state.get("ollama/a").ready is False


def test_print_event_formats_download_lifecycle(capsys):
    print_event("download:start|ollama/x|x:7b")
    print_event("download:done|ollama/x|x:7b|21.7 GB")
    out = capsys.readouterr().out
    assert "Downloading x:7b..." in out
    assert "done: downloaded x:7b (21.7 GB)" in out


def test_print_error_summary_prints_nothing_on_clean_run(capsys):
    assert print_error_summary([], total=3) is False
    assert capsys.readouterr().out == ""
```

- [ ] **Step 4: Run the new test file**

Run: `cd modelman && uv run pytest tests/commands/test_run_tui.py -q`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/main.py modelman/tests/commands/test_run_tui.py
git rm modelman/tests/commands/test_download.py
git commit -m "$(cat <<'EOF'
feat(modelman): remove `download` command, add post-exit queued-ops runner

main.py now applies a QueuedOps returned by the TUI against fresh
on-disk state after the TUI exits, printing lifecycle events and
provider progress straight to stdout. Part of the downloads-on-exit
redesign - completes plan item #3.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 4: `forms.py` + `screens/models.py` — queue everything, exit on Apply/Discard

**Files:**
- Modify: `modelman/src/modelman/screens/forms.py`
- Modify: `modelman/src/modelman/screens/models.py`

**Interfaces:**
- Consumes: `QueuedOps` (`modelman.queue`, Task 1).
- Produces: `ModelScreen` with no DownloadManager references anywhere, no family-scroll machinery, `action_back()`/`_on_exit_confirm()` that exit the app instead of staying in the TUI.

> This task leaves `tests/screens/test_models.py` and `tests/screens/test_app_navigation.py` red — expected, fixed in Tasks 6–7. Verify with `make check` for this task; a quick manual TUI smoke test (Step 9) is the real gate.

- [ ] **Step 1: Delete `QuitBlockedModal` and `CancelApplyDialog` from `forms.py`**

In `modelman/src/modelman/screens/forms.py`, delete the entire `QuitBlockedModal` class block (from `class QuitBlockedModal(ModelmanModal[bool]):` through the line before `class CancelApplyDialog`) and the entire `CancelApplyDialog` class block (from `class CancelApplyDialog(ModelmanModal[Literal["cancel", "wait"]]):` through its final `self.dismiss(value)  # type: ignore[arg-type]` line). `ConfirmExitDialog` (right above `QuitBlockedModal`) and `ModelmanModal` (its base, defined earlier in the file) are unaffected.

- [ ] **Step 2: Fix `models.py`'s imports**

In `modelman/src/modelman/screens/models.py`, remove:

```python
from collections.abc import Callable
```

(it's only used by `_run_apply`, deleted in Step 12 below).

Change:

```python
from ..litellm import (
    default_litellm_config_path,
    is_effectively_exposed,
    passes_ready_gate,
    provider_policy,
)
```

to:

```python
from ..litellm import (
    is_effectively_exposed,
    passes_ready_gate,
    provider_policy,
)
```

Change:

```python
from ..queue import PendingChanges
```

to:

```python
from ..queue import QueuedOps
```

- [ ] **Step 3: Drop the `g` binding and the family-scroll constructor param**

Remove `("g", "open_downloads", "Downloads"),` from `BINDINGS`.

Replace the `__init__` signature and body:

```python
    def __init__(
        self,
        registry: Registry,
        state: StateStore,
        registry_path: Path,
        state_path: Path,
        scroll_to_family: str | None = None,
    ) -> None:
        super().__init__()
        self.registry = registry
        self.state = state
        self.registry_path = registry_path
        self.state_path = state_path
        # One-time cursor placement after the first reload (e.g. `modelman
        # download <family>`): scroll to that family's first row instead of
        # filtering the list to it. Consumed once in on_mount().
        self._scroll_to_family = scroll_to_family
        # Provider of the last model added or edited this session; used to
```

with:

```python
    def __init__(
        self,
        registry: Registry,
        state: StateStore,
        registry_path: Path,
        state_path: Path,
    ) -> None:
        super().__init__()
        self.registry = registry
        self.state = state
        self.registry_path = registry_path
        self.state_path = state_path
        # Provider of the last model added or edited this session; used to
```

- [ ] **Step 4: Simplify `on_mount`**

Replace:

```python
        self.reload()
        self._refresh_pending_bar()
        self._update_litellm_status()
        mt.focus()
        if self._scroll_to_family is not None:
            self._scroll_cursor_to_family(self._scroll_to_family)
            self._scroll_to_family = None
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
        self._last_poll_had_active = False
        self.set_interval(1.0, self._poll_downloads)
```

with:

```python
        self.reload()
        self._refresh_pending_bar()
        self._update_litellm_status()
        mt.focus()
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
```

Delete the entire `_scroll_cursor_to_family` method:

```python
    def _scroll_cursor_to_family(self, family: str) -> None:
        """One-time cursor placement for `modelman download <family>`:
        move to that family's first row instead of filtering the list.
        Walks the table's existing row order (already produced by
        reload()'s call to _sorted_models()) instead of sorting the whole
        registry a second time just to find one row."""
        mt = self.query_one("#model-table", DataTable)
        models_by_id = {m.id: m for m in self.registry.models}
        for i, row_key in enumerate(mt.rows.keys()):
            entry = models_by_id.get(str(row_key.value))
            if entry is not None and entry.family == family:
                mt.move_cursor(row=i)
                return
```

- [ ] **Step 5: Delete `_poll_downloads`**

Delete the entire method:

```python
    def _poll_downloads(self) -> None:
        """Reload state from disk while any download is active (or just
        finished, to catch the final transition), so DownloadManager's
        writes — which land on disk, not on this screen's in-memory
        StateStore — become visible without the user navigating away and
        back. Cheap: modelman.toml is small and this only runs while
        downloads exist."""
        active = self.app.downloads.has_active()  # type: ignore[attr-defined]
        if not active and not self._last_poll_had_active:
            return
        self._last_poll_had_active = active
        try:
            self.state = load_state(self.state_path)
        except Exception:  # noqa: BLE001
            return
        self.reload()
```

- [ ] **Step 6: Drop the downloading branch from the STATUS glyph**

In `_load_models`'s `_repopulate`, remove:

```python
                elif self.app.downloads.is_downloading(m.id):  # type: ignore[attr-defined]
                    status = "[cyan]⏳[/cyan]"
```

from the `if/elif` chain (the chain becomes `queued_deletes` → `queued_ready` → `queued_moves` → `ready` → default).

- [ ] **Step 7: Simplify `_projected_ready` and `_cancel_ready_cascade`**

Replace:

```python
    def _projected_ready(self, model_id: str) -> bool:
        """The ready value this model will have after apply() (or, for a
        model routed through DownloadManager instead of the queue, after
        its download finishes): the queued target if one exists,
        otherwise True while actively downloading, otherwise the
        persisted flag."""
        if model_id in self.queued_ready:
            return self.queued_ready[model_id]
        if self.app.downloads.is_downloading(model_id):  # type: ignore[attr-defined]
            return True
        return self.state.get(model_id).ready
```

with:

```python
    def _projected_ready(self, model_id: str) -> bool:
        """The ready value this model will have after apply(): the queued
        target if one exists, otherwise the persisted flag."""
        if model_id in self.queued_ready:
            return self.queued_ready[model_id]
        return self.state.get(model_id).ready
```

Replace:

```python
    def _cancel_ready_cascade(self, mid: str) -> None:
        """Undo an expose-triggered ready cascade: cancel a running
        download if that's what the cascade used (mapped provider), or
        drop the queued flag-only ready-on otherwise. Shared by the
        repeated-'x'-keypress cancel path and discard (Discard-cancels-
        cascade)."""
        if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
            self.app.downloads.cancel(mid)  # type: ignore[attr-defined]
        self.queued_ready.pop(mid, None)
        self._ready_cascade_for_expose.discard(mid)
```

with:

```python
    def _cancel_ready_cascade(self, mid: str) -> None:
        """Undo an expose-triggered ready cascade: drop the queued
        ready-on. Shared by the repeated-'x'-keypress cancel path and
        discard."""
        self.queued_ready.pop(mid, None)
        self._ready_cascade_for_expose.discard(mid)
```

- [ ] **Step 8: Simplify `action_toggle_ready` and `action_toggle_expose`**

Replace the entire `action_toggle_ready` method body:

```python
    def action_toggle_ready(self) -> None:
        entry = self._current_entry()
        if entry is None:
            return
        mid = entry.id

        if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
            # Three-way 'r': downloading -> cancel. The model's readiness
            # was transient (it only becomes ready when the download
            # finishes), so a queued expose — whether cascade-marked or
            # queued while the model merely looked ready-by-projection —
            # would be doomed once the download stops. Drop both halves.
            self.app.downloads.cancel(mid)  # type: ignore[attr-defined]
            self.queued_exposes.pop(mid, None)
            self._ready_cascade_for_expose.discard(mid)
            self.app.notify(f"Cancelling download: {mid}")
            self.reload()
            return

        persisted_ready = self.state.get(mid).ready
        displayed_ready = self.queued_ready.get(mid, persisted_ready)
        target = not displayed_ready
        if target == persisted_ready:
            # Repeated keypress: this target is exactly what's already on
            # disk once any queued flip is dropped. Cancel it instead of
            # re-queuing a no-op.
            self.queued_ready.pop(mid, None)
            if mid in self._ready_cascade_for_expose:
                # This readiness was only queued because a prior 'x' press
                # cascaded it in for an expose; cancel that expose with it.
                self.queued_exposes.pop(mid, None)
                self._ready_cascade_for_expose.discard(mid)
            # The invariant covers every other case: a user-queued expose
            # stranded by this cancel is dropped (with a notification).
            self._enforce_expose_ready_rule(mid, entry)
            self.app.notify(f"Model already {'ready' if target else 'not ready'}")
            self._refresh_pending_bar()
            self.reload()
            return

        if target and self._provider_can_download(entry.provider_id):
            # A real download (ollama/omlx/llamacpp, local or cloud):
            # start it immediately in the background instead of queuing
            # it — it is never applied by PendingChanges.apply() (Task 4).
            self._start_download(entry)
            return

        # Flag-only provider (native/unmapped) ready-on, or any ready-off:
        # unchanged apply-on-exit queue behavior. apply() owns the
        # consequences — it removes the artifact (provider delete, or the
        # recorded disk_path for flag-only providers) on ready-off and
        # re-derives the unexpose cascade from the persisted exposure flag
        # — and the invariant below drops any queued expose the new ready
        # value makes impossible, so the screen queue can never strand a
        # doomed expose.
        self.queued_ready[mid] = target
        self._enforce_expose_ready_rule(mid, entry)
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()
```

with:

```python
    def action_toggle_ready(self) -> None:
        entry = self._current_entry()
        if entry is None:
            return
        mid = entry.id

        persisted_ready = self.state.get(mid).ready
        displayed_ready = self.queued_ready.get(mid, persisted_ready)
        target = not displayed_ready
        if target == persisted_ready:
            # Repeated keypress: this target is exactly what's already on
            # disk once any queued flip is dropped. Cancel it instead of
            # re-queuing a no-op.
            self.queued_ready.pop(mid, None)
            if mid in self._ready_cascade_for_expose:
                # This readiness was only queued because a prior 'x' press
                # cascaded it in for an expose; cancel that expose with it.
                self.queued_exposes.pop(mid, None)
                self._ready_cascade_for_expose.discard(mid)
            # The invariant covers every other case: a user-queued expose
            # stranded by this cancel is dropped (with a notification).
            self._enforce_expose_ready_rule(mid, entry)
            self.app.notify(f"Model already {'ready' if target else 'not ready'}")
            self._refresh_pending_bar()
            self.reload()
            return

        # Every ready-on/off is queued now — apply() (post-exit) owns the
        # consequences, including a mapped provider's real download. The
        # invariant below drops any queued expose the new ready value
        # makes impossible, so the screen queue can never strand one.
        self.queued_ready[mid] = target
        self._enforce_expose_ready_rule(mid, entry)
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()
```

Replace, inside `action_toggle_expose`, the cascade branch:

```python
            if mid in self.queued_ready:
                self.app.notify(
                    "Model is queued to be made not ready — cancel that before exposing"
                )
                return
            if self._provider_can_download(entry.provider_id):
                self._start_download(entry)
            else:
                self.queued_ready[mid] = True
            self._ready_cascade_for_expose.add(mid)
```

with:

```python
            if mid in self.queued_ready:
                self.app.notify(
                    "Model is queued to be made not ready — cancel that before exposing"
                )
                return
            self.queued_ready[mid] = True
            self._ready_cascade_for_expose.add(mid)
```

- [ ] **Step 9: Delete the DownloadManager-routing helpers**

Delete these four entire methods (contiguous in the file, from `def _provider_entry_or_none(self, provider_id: str):` through `_on_download_finished`'s final `self._refresh_pending_bar()` line):

```python
    def _provider_entry_or_none(self, provider_id: str):
        try:
            return self.registry.provider(provider_id)
        except KeyError:
            return None

    def _provider_can_download(self, provider_id: str) -> bool:
        """True when a ready-on against this provider is a real
        download/pull and must go through DownloadManager: the provider
        has a registered Provider class (ollama/omlx/llamacpp) that does NOT
        declare manages_own_cache. This is deliberately NOT
        model_has_local_artifact — an ollama *cloud* model has no local
        artifact but its ready-on still runs a real `ollama pull` (that's
        what registers the tag), so it must route through DownloadManager
        too; native/unmapped providers have no Provider class and keep the
        queued flag flip. A provider that manages its own cache outside
        modelman's control (MTPLX via the `mtplx` CLI) declares
        manages_own_cache = True and is also treated as flag-only. Mirrors
        _run_apply's try/except-KeyError flag-only rule."""
        if self._provider_entry_or_none(provider_id) is None:
            return False
        from ..providers.registry import ProviderRegistry

        provider_cls = ProviderRegistry.get_class(provider_id)
        if provider_cls is None:
            return False
        return not provider_cls.manages_own_cache

    def _start_download(self, entry: ModelEntry) -> None:
        """Route a real ready-on through DownloadManager instead of the
        apply-on-exit queue — the download starts immediately in the
        background and PendingChanges.apply() never touches it (Task 4's
        assertion enforces this)."""
        variant = model_entry_to_variant(entry)
        config = provider_config(self.registry.provider(entry.provider_id))

        def _on_complete(local_path: str, mid: str = entry.id) -> None:
            self.app.call_from_thread(self._on_download_finished, mid)

        self.app.downloads.start(  # type: ignore[attr-defined]
            entry.id, variant, config, on_complete=_on_complete, registry=self.registry
        )
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()

    def _on_download_finished(self, model_id: str) -> None:
        """Best-effort immediate refresh when this screen started the
        download that just finished. The 1s poll (below) is the
        authoritative fallback for every other case — a download that
        finishes after the initiating ModelScreen was replaced by
        another instance still gets picked up there."""
        try:
            self.state = load_state(self.state_path)
        except Exception:  # noqa: BLE001
            return
        self.reload()
        self._refresh_pending_bar()
```

- [ ] **Step 10: Simplify `_on_add_model`**

Replace:

```python
        save_registry(self.registry, self.registry_path)
        if self._provider_can_download(entry.provider_id):
            # Mapped provider: the ready-on is a real download/pull —
            # start it in the background now instead of queueing it.
            self._start_download(entry)
        else:
            # Flag-only provider or cloud model with no download
            # mechanism: unchanged apply-on-exit queue behavior.
            self.queued_ready[variant["id"]] = True
        self._last_provider_used = variant["provider"]
```

with:

```python
        save_registry(self.registry, self.registry_path)
        self.queued_ready[variant["id"]] = True
        self._last_provider_used = variant["provider"]
```

- [ ] **Step 11: Drop the `is_downloading` guards from delete/edit**

In `action_delete_model`, remove:

```python
        if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
            # The weights are being written right now; deleting mid-flight
            # would race the download thread. Cancel first instead.
            self.app.notify(f"{mid} is downloading — cancel first")
            return
```

In `action_edit_model`, remove:

```python
        if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
            # An edit that changes the variant mid-download would race the
            # in-flight write; move/edit must wait for the download.
            self.app.notify(f"{mid} is downloading — cancel first")
            return
```

- [ ] **Step 12: Make `action_back` exit directly on an empty queue, and delete `action_open_downloads`**

Replace:

```python
    def action_back(self) -> None:
        if not self.has_pending_changes():
            # This screen is the app's root now — nothing to pop back to,
            # so Escape with no pending changes quits (same guard ctrl+q
            # already goes through: downloads-active check, etc.).
            self.app.request_quit()  # type: ignore[attr-defined]
            return
```

with:

```python
    def action_back(self) -> None:
        if not self.has_pending_changes():
            # This screen is the app's root now — nothing to pop back to,
            # so Escape with no pending changes exits immediately.
            self.app.exit()
            return
```

Delete the entire `action_open_downloads` method:

```python
    def action_open_downloads(self) -> None:
        from .downloads import DownloadScreen

        self.app.push_screen(DownloadScreen())
```

- [ ] **Step 13: Rewrite `_on_exit_confirm` to exit the app on Apply and Discard**

Replace the entire method:

```python
    def _on_exit_confirm(self, choice: str | None) -> None:
        if choice == "apply":
            self._push_status_screen()
            return
        if choice == "discard":
            # Snapshot the clear-set BEFORE the cancel loops: they discard
            # ids from _ready_cascade_for_expose as they cancel them, so a
            # union taken afterwards would only see the leftovers — and
            # skip clear_state for every cascade-cancelled download,
            # leaving its stale row in the DownloadScreen.
            to_clear = set(self._ready_cascade_for_expose) | set(self._added_ids)
            # Cancel any download that was only running because an expose
            # cascaded it in (Discard-cancels-cascade) BEFORE restoring the
            # snapshot, so a discarded session leaves no background
            # download running for a change the user walked away from.
            for mid in list(self._ready_cascade_for_expose):
                self._cancel_ready_cascade(mid)
            # Also cancel any download still running for a model added
            # this session: _restore_snapshot() below removes its registry
            # entry, and a download that finishes afterward would persist
            # a dangling modelman.toml row (ready=True) for a model_id no
            # longer in the registry.
            for mid in list(self._added_ids):
                if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
                    self.app.downloads.cancel(mid)  # type: ignore[attr-defined]
            # Clear the download states for all cascade-cancelled/added
            # models so they don't persist in the DownloadScreen after
            # discard. clear_state() is safe to call even if the state
            # doesn't exist.
            for mid in to_clear:
                self.app.downloads.clear_state(mid)  # type: ignore[attr-defined]
            self._restore_snapshot()
            # In-place edits save the registry to disk immediately
            # (_on_edit_model) — restoring the in-memory snapshot alone
            # would leave a discarded edit persisted on disk, so write the
            # restored registry back out too.
            save_registry(self.registry, self.registry_path)
            # A download that finished mid-session persisted ready=True to
            # disk (DownloadManager._run's locked_state write) for a model
            # this discard just removed from the registry; the in-memory
            # restore alone leaves that dangling row in modelman.toml.
            # Locked read-modify-write merges only this session's added
            # ids onto fresh disk state, so a concurrent DownloadManager
            # completion for an unrelated model survives.
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
            # This screen is the app's root now — nothing to pop back to.
            # Re-render in place instead.
            self.reload()
            self._refresh_pending_bar()
            return
        # "cancel" or None: stay on the model screen, queue preserved.
        return
```

with:

```python
    def _on_exit_confirm(self, choice: str | None) -> None:
        if choice == "apply":
            self.app.exit(
                QueuedOps(
                    ready=dict(self.queued_ready),
                    deletes=dict(self.queued_deletes),
                    moves=dict(self.queued_moves),
                    exposes=dict(self.queued_exposes),
                )
            )
            return
        if choice == "discard":
            self._restore_snapshot()
            # In-place edits save the registry to disk immediately
            # (_on_edit_model) — restoring the in-memory snapshot alone
            # would leave a discarded edit persisted on disk, so write the
            # restored registry back out too.
            save_registry(self.registry, self.registry_path)
            # A session-added model never independently persists to
            # modelman.toml before apply() runs (nothing writes state in
            # the background anymore) — this merge is defensive
            # insurance, not a load-bearing path.
            with contextlib.suppress(Exception), locked_state(self.state_path) as disk_state:
                for mid in self._added_ids:
                    if mid not in self._snapshot_state_entries:
                        disk_state.models.pop(mid, None)
            self.app.exit(None)
            return
        # "cancel" or None: stay on the model screen, queue preserved.
        return
```

- [ ] **Step 14: Delete `_push_status_screen`, `_on_status_screen_dismissed`, `_register_deferred_expose`, `_run_apply`**

Delete all four entire methods, from `def _push_status_screen(self) -> None:` through `_run_apply`'s final `self._added_ids.clear()` line (everything between `_on_exit_confirm` — now ending at Step 13's `return` — and `_retake_snapshot`).

- [ ] **Step 15: Run lint/typecheck**

Run: `cd modelman && make check`
Expected: PASS (fix any residual unused-import or type errors ruff/mypy report).

- [ ] **Step 16: Manual smoke test**

Run: `cd modelman && uv run modelman` against a scratch registry (e.g. `MODELMAN_REGISTRY=/tmp/mm-smoke/registry.toml MODELMAN_STATE=/tmp/mm-smoke/modelman.toml uv run modelman`, with `/tmp/mm-smoke` empty so it starts from a fresh install). Add one ollama model with `a`, press `r` to queue it ready, press `escape`, confirm the ConfirmExitDialog shows "ready 1" and lists it as a `↓` row, press `y` (Apply), and confirm the TUI exits back to the shell (rather than hanging or crashing) and prints `Downloading ...` progress lines. This is a manual check — no automated test exists yet for this end-to-end path until Task 6.

- [ ] **Step 17: Commit**

```bash
git add modelman/src/modelman/screens/forms.py modelman/src/modelman/screens/models.py
git commit -m "$(cat <<'EOF'
feat(modelman): ModelScreen queues every ready-on/off, exits on Apply/Discard

Removes all DownloadManager routing from ModelScreen (every action now
just populates the queue dicts) and the family-scroll machinery
(download <family>'s only reason to exist). Escape/ctrl+q's Apply and
Discard choices now exit the app instead of staying in the TUI. Part
of the downloads-on-exit redesign - completes plan item #4.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 5: Delete the old download-subsystem files and their tests

**Files:**
- Delete: `modelman/src/modelman/downloads.py`
- Delete: `modelman/src/modelman/screens/downloads.py`
- Delete: `modelman/src/modelman/screens/status.py`
- Delete: `modelman/tests/test_downloads.py`
- Delete: `modelman/tests/screens/test_downloads_screen.py`
- Delete: `modelman/tests/screens/test_status.py`

**Interfaces:** N/A — pure deletion; no signatures produced or consumed. Depends on Task 4 having removed every reference to these files first.

- [ ] **Step 1: Confirm nothing still imports the files being deleted**

Run: `cd modelman && grep -rn "from .downloads import\|from \.\.downloads import\|from modelman.downloads import\|screens.downloads\|screens\.status\|from .status import\|DownloadManager\|DownloadScreen\|StatusScreen" src/ tests/ --include=*.py | grep -v "test_downloads.py\|test_downloads_screen.py\|test_status.py\|src/modelman/downloads.py\|src/modelman/screens/downloads.py\|src/modelman/screens/status.py"`
Expected: no output (or only comments in `tests/screens/test_models.py`/`tests/screens/test_app_navigation.py`/`tests/test_state.py`/`tests/test_providers/test_mtplx.py`/`tests/test_providers/test_base.py` — those are fixed in Tasks 6–7 and are prose, not imports; if any *code* reference remains outside the six files above, stop and go back to Task 4).

- [ ] **Step 2: Delete the files**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/modelman
git rm src/modelman/downloads.py src/modelman/screens/downloads.py src/modelman/screens/status.py
git rm tests/test_downloads.py tests/screens/test_downloads_screen.py tests/screens/test_status.py
```

- [ ] **Step 3: Verify the suite collects (it will still show failures — that's Tasks 6–7)**

Run: `cd modelman && uv run pytest --collect-only -q 2>&1 | tail -30`
Expected: no `ImportError`/`ModuleNotFoundError` for `modelman.downloads`, `modelman.screens.downloads`, or `modelman.screens.status`. Collection errors in `tests/screens/test_models.py` or `tests/screens/test_app_navigation.py` (e.g. `from modelman.downloads import DownloadState`) are expected — fixed in Tasks 6–7.

- [ ] **Step 4: Commit**

```bash
git commit -m "$(cat <<'EOF'
chore(modelman): delete DownloadManager, DownloadScreen, StatusScreen

Fully unreferenced after Task 4 moved ModelScreen off background
downloads. Part of the downloads-on-exit redesign - completes plan
item #5.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 6: Rewrite `tests/screens/test_models.py`

**Files:**
- Modify: `modelman/tests/screens/test_models.py`

**Interfaces:** Consumes `QueuedOps` (Task 1) and the `ModelScreen` behavior from Task 4 — no new signatures produced.

This file has ~2730 lines. The table below (from a full read of the file against the new design) lists every test that needs a change. Anything not listed is unaffected — leave it alone.

**Mechanical rule used throughout:** wherever a test currently does `started = []` / `monkeypatch.setattr(app.downloads, "start", lambda *a, **k: started.append(...))` (or `.cancel`/`.clear_state`/`DownloadState`) to stub out `DownloadManager`, delete those lines entirely — `app.downloads` no longer exists. Wherever a test asserts `app.screen.queued_ready == {}` specifically *because* a mapped provider's ready-on "routed to DownloadManager instead", flip the assertion: it now queues, so assert `queued_ready == {"<id>": True}`.

- [ ] **Step 1: Delete tests with no equivalent in the new design**

Delete these test functions entirely (each is documented at its current line number; re-locate by function name since earlier deletions shift line numbers — delete from `def test_...` through the blank line before the next `def`):

- `test_app_with_initial_family_launches_into_model_screen` — wait, this one lives in `test_app_navigation.py`, not here; skip it in this file.
- `test_r_on_not_ready_local_artifact_model_routes_to_download_manager` (~1110-1157)
- `test_download_finishing_after_screen_popped_does_not_crash` (~1161-1216)
- `test_r_on_not_ready_cloud_model_routes_to_download_manager` (~1265-1290)
- `test_r_twice_cancels_started_download_with_notification` (~1294-1329)
- `test_status_shows_downloading_glyph` (~1943-1964)
- `test_delete_blocked_while_downloading` (~1968-1986)
- `test_edit_blocked_while_downloading` (~1990-2010)
- `test_poll_refreshes_state_after_download_completes` (~2013-2042)
- `test_r_on_downloading_model_cancels` (~2045-2066)
- `test_r_on_real_provider_ready_on_starts_download_not_queue` (~2069-2091)
- `test_r_on_cloud_model_of_mapped_provider_starts_download` (~2177-2199)
- `test_discard_clears_state_of_cascaded_download_for_preexisting_model` (~2347-2380)
- `test_x_on_already_downloading_unrelated_model_just_queues` (~2384-2407)
- `test_expose_against_downloading_model_is_deferred_not_applied_now` (~2450-2481)
- `test_run_apply_refreshes_stale_state_for_a_just_finished_download` (~2485-2536)
- `test_deferred_expose_failure_notifies_the_user` (~2600-2640)
- `test_provider_can_download_treats_mtplx_as_flag_only` (~2727-2751)

- [ ] **Step 2: Trivial fix — drop the `family=` kwarg**

In `test_model_screen_cursor_restored_after_reload`, change `ModelmanApp(family="ornith")` to `ModelmanApp()`.

- [ ] **Step 3: Rewrite the ready-on/expose-cascade tests that assumed DownloadManager routing**

For `test_x_on_not_ready_model_cascades_ready_and_expose`: remove the `app.downloads.start` monkeypatch; change the final assertion from `app.screen.queued_ready == {}` to `app.screen.queued_ready == {"ollama/glm-5.2:cloud": True}` (keep the `queued_exposes` assertion as-is).

For `test_r_cancel_after_x_cascade_also_cancels_expose`: remove all `app.downloads`/`DownloadState`/cancel-list mocking; after the cascade (`x` then `r`), assert `queued_ready == {}` and `queued_exposes == {}` (no `cancelled` list to check — nothing was ever backgrounded).

For `test_x_cancel_after_x_cascade_also_cancels_ready`: same shape — remove `app.downloads` mocking; assert `queued_exposes == {}` and `queued_ready == {}` after two `x` presses.

For `test_r_x_r_on_not_ready_model_drops_stranded_expose`: remove the `app.downloads`/`DownloadState`/`.cancel` mocking that simulated a live download between the three keypresses. Trace the new `action_toggle_ready`/`action_toggle_expose` logic (Task 4, Step 8) directly to derive the expected `queued_ready`/`queued_exposes` after each of the three presses on a not-ready mapped-provider model:
  1. `r` (not-ready → target True): `queued_ready == {"ollama/a": True}`, `_ready_cascade_for_expose` does **not** contain `"ollama/a"` (the user pressed `r` directly, not via an `x` cascade).
  2. `x` (ready gate: `_projected_ready` is now `True` via the queued entry, so no cascade needed): `queued_exposes == {"ollama/a": True}`, `queued_ready` unchanged, `_ready_cascade_for_expose` still empty.
  3. `r` again: `displayed_ready` is `True` (queued), target becomes `False`; `target == persisted_ready` (`False == False`) is True, so this is the "repeated keypress" branch — `queued_ready.pop("ollama/a")`, and since `"ollama/a"` is **not** in `_ready_cascade_for_expose`, the queued expose is *not* auto-cancelled here directly, but `_enforce_expose_ready_rule` runs next and finds the projected ready (now the persisted `False`, since `queued_ready` no longer has an entry) fails the gate for the still-queued `queued_exposes["ollama/a"] = True` — so it drops it with a notification. Final state: `queued_ready == {}`, `queued_exposes == {}`.
  Update the test body to drive exactly those three key presses and assert that final state, with a notification captured for the third press's stranded-expose drop (matching `_enforce_expose_ready_rule`'s `self.app.notify(...)` call).

For `test_x_cascade_on_real_provider_starts_download`: remove the `app.downloads.start` mock; change `queued_ready == {}` to `queued_ready == {"ollama/x": True}`; keep the `queued_exposes == {"ollama/x": True}` and `"ollama/x" in app.screen._ready_cascade_for_expose` assertions.

For `test_x_twice_on_cascaded_download_cancels_it`: remove `DownloadState`/`app.downloads.cancel` mocking and the `cancelled == ["ollama/x"]` assertion; assert `queued_exposes == {}`, `queued_ready == {}`, `"ollama/x" not in app.screen._ready_cascade_for_expose` after the second `x` press.

For `test_discard_cancels_cascaded_download`: remove `app.downloads.start`/`DownloadState`/`.cancel` mocking and the `cancelled == ["ollama/x"]` assertion. Rename to `test_discard_drops_cascaded_ready_on_and_expose` and assert that after pressing Escape then `d` (Discard), the app exits with `app.return_value is None` and `load_registry(reg_path)` / `load_state(state_path)` show no trace of the cascaded ready/expose (they were only ever in-memory queue entries, never persisted).

- [ ] **Step 4: Rewrite the add-model and discard tests**

For `test_add_then_delete_model_queues_changes` (in `test_app_navigation.py`, not here — skip) — N/A for this file.

For `test_model_screen_add_appends_model_entry_to_registry`: remove the `app.downloads.start` patch.

For `test_model_screen_discard_restores_fetch_dataclass`: remove the `app.downloads.start` patch used for the `x` cascade; add an assertion that `queued_ready` also got cascaded before the discard.

For `test_discard_combined_move_add_and_download`: remove the defensive `app.downloads.start` patch (nothing to stub); drop the now-redundant manual `queued_ready[...] = True` line if `_on_add_model` already queues it. Rename if the "download" in its name is misleading after the fix (optional).

For `test_discard_removes_out_of_family_added_model`: same fix as above.

For `test_add_model_saves_registry_immediately_and_starts_download`: remove the `app.downloads.start` mock/`started` list; change the final assertion from `queued_ready == {}` to `queued_ready == {"ollama/newmodel:7b": True}`; keep the "registry saved immediately" assertion. Rename to `test_add_model_saves_registry_immediately_and_queues_ready_on` (the "starts download" framing is now wrong).

For `test_discard_persists_state_cleanup_for_session_added_model`: remove the `monkeypatch.setattr(app.downloads, "start", lambda *a, **k: None)` line and the "simulate the download having completed mid-session" `locked_state` block that fakes a `DownloadManager` background write — nothing writes `modelman.toml` in the background anymore, so that scenario can't occur. Repurpose the test to assert the simpler, still-real invariant: after adding a model this session (which queues `ready=True` and saves the registry immediately) and then discarding, both `load_registry(reg_path)` and `load_state(state_path)` show no trace of the added model (registry entry gone, no stray state row).

- [ ] **Step 5: Rewrite `test_r_flag_only_provider_still_queues_not_downloads`**

Remove the `started = []` / `monkeypatch.setattr(app.downloads, "start", ...)` / `assert started == []` lines. Keep `assert app.screen.queued_ready == {"claude-agent/x": True}`. Rename to `test_r_flag_only_provider_queues_ready_flip` (the flag-only-vs-mapped distinction it originally drew for *whether it queues* no longer applies — every provider queues now — but it's still worth keeping as the flag-only representative case, paired with a mapped-provider case covered by the rewritten `test_add_model_saves_registry_immediately_and_queues_ready_on` and `test_x_cascade_on_real_provider_starts_download` above).

- [ ] **Step 6: Fix stale comments**

In `test_d_queues_only_delete_not_ready_or_expose` and `test_discard_reverts_immediately_saved_registry_edit`, remove/rewrite any comment claiming a mapped provider's ready-on "routes through DownloadManager instead of the queue" — it no longer does. No assertion changes needed in either test.

- [ ] **Step 7: Run the file**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -q`
Expected: PASS, all green.

- [ ] **Step 8: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
test(modelman): rewrite test_models.py for the downloads-on-exit redesign

Deletes DownloadManager-routing tests with no equivalent in the new
design; flips every "mapped provider ready-on bypasses the queue"
assertion to "it queues now". Part of the downloads-on-exit redesign -
completes plan item #6.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 7: Rewrite `tests/screens/test_app_navigation.py`

**Files:**
- Modify: `modelman/tests/screens/test_app_navigation.py`

**Interfaces:** Consumes `QueuedOps` and `main.run_queued_ops` (Tasks 1 and 3) and the `ModelmanApp`/`ModelScreen` behavior from Tasks 2 and 4 — no new signatures produced.

This file is 2140 lines / 43 tests. Empirically verified pattern for every rewritten Apply/Discard test in this task (confirmed against Textual 8.2.8's `App.exit()`/`run_test()`): calling `self.app.exit(result)` from inside a dismiss-callback (exactly `ConfirmExitDialog`'s `_on_exit_confirm` shape) sets `app.return_value`/`app.return_code` synchronously and is readable immediately after `await pilot.pause()`, both inside and after the `async with app.run_test()` block closes. Do **not** press further keys after the exit-triggering press — the app's message pump has stopped.

- [ ] **Step 1: Fix the module-level import**

Remove `from modelman.downloads import DownloadState` (line 5).

- [ ] **Step 2: Delete tests with no equivalent in the new design**

Delete these entire test functions:

- `test_app_with_initial_family_launches_into_model_screen` (~107-134)
- `test_model_table_repaints_after_returning_from_apply` (~553-614) — no "return to ModelScreen after apply" exists; the app exits.
- `test_discard_after_apply_does_not_resurrect_earlier_applied_delete` (~617-705) — the app can't apply twice in one running session anymore (it exits after the first Apply).
- `test_discard_cancels_download_for_a_model_added_this_session` (~1071-1141) — folds into the rewritten add+discard coverage in Task 6.
- `test_run_apply_does_not_swallow_provider_instantiation_errors` (~1373-1411) — `ModelScreen._run_apply` no longer exists.
- `test_ctrl_q_blocked_while_a_download_is_active` (~2104-2120)
- `test_quit_blocked_modal_review_pushes_download_screen` (~2123-2140)

- [ ] **Step 3: Trivial fixes — drop `family=` kwargs**

In `test_direct_download_syncs_agent_providers`, `test_reconcile_shows_reality_when_manifest_out_of_date`, and `test_reconcile_does_not_persist_to_disk_on_cancel`, change `ModelmanApp(family="ornith")` to `ModelmanApp()`. These tests' actual assertions (sync_agent_providers, reconcile behavior) are independent of family-scroll and need no other change.

- [ ] **Step 4: Rewrite `test_toggle_ready_queues_variant`**

Remove the `app.downloads.start` patch. Replace the assertion that inspected a `started` list with `assert app.screen.queued_ready.get("ollama/o35") is True`.

- [ ] **Step 5: Rewrite `test_status_shows_four_states`**

Remove the injected `DownloadState(status="downloading")` row and its `⏳`-glyph assertion entirely — nothing is ever mid-download while the TUI is open. Keep the other three states (ready `✓`, not-ready `○`, queued-delete `✗`) and add a fourth via a queued ready-on (`↓`) instead of the deleted "downloading" one, so the test still demonstrates four distinct STATUS glyphs.

- [ ] **Step 6: Remove the DownloadManager patch from `test_add_then_delete_model_queues_changes` and `test_model_screen_add_form_offers_all_providers_for_empty_family`'s sibling adds**

Remove the `app.downloads.start` patch from `test_add_then_delete_model_queues_changes` (~274-326) — the add now always queues, nothing to stub.

- [ ] **Step 7: Rewrite the Apply-flow tests to assert on the app's exit value**

For each of `test_expose_after_reconcile_survives_stale_state`, `test_apply_preserves_other_models_state_rows`, `test_escape_with_pending_shows_dialog_and_apply`, `test_exit_dialog_lists_move_and_apply_persists_it`: replace the StatusScreen wait-loop pattern

```python
    from modelman.screens.status import StatusScreen

    async with app.run_test() as pilot:
        ...
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("y")
        await pilot.pause()
        for _ in range(50):
            cur = app.screen
            if isinstance(cur, StatusScreen) and cur.done:
                break
            await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
        # ...assertions on ms.registry / ms.state / a RichLog...
```

with the new shape: press Escape then `y` (Apply), then assert directly on `app.return_value` (a `QueuedOps`) and, where the test needs to see the *applied* result (not just what got queued), call `main.run_queued_ops(app.return_value)` against the same `MODELMAN_REGISTRY`/`MODELMAN_STATE` env vars the test already set, then re-load registry/state from disk:

```python
    from modelman.main import run_queued_ops
    from modelman.queue import QueuedOps

    async with app.run_test() as pilot:
        ...
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("y")
        await pilot.pause()
        assert isinstance(app.return_value, QueuedOps)
        queued = app.return_value

    # App has exited; apply the queue the same way main.py's run_tui() does.
    failed = run_queued_ops(queued)
    assert failed is False
    # ...assertions on load_registry(reg_path) / load_state(state_path)...
```

Apply this shape to each of the four tests, replacing their post-apply assertions (which today read `ms.registry`/`ms.state` — now stale, since the post-exit runner mutates a freshly-loaded Registry/StateStore, not the screen's own objects) with `load_registry(reg_path)`/`load_state(state_path)` reads after calling `run_queued_ops`.

For `test_apply_move_emptying_family_keeps_it_visible` (~1842-1891): same restructuring, and specifically fix the assertion that currently reads `ms.registry.models` after apply — change it to `load_registry(reg_path).models`. Drop the second Escape press that used to return from `StatusScreen` — there's nothing to return to.

- [ ] **Step 8: Rewrite `test_discard_pending_exits_without_applying`**

Remove the `app.downloads.start`/`.cancel` patches used to test the cascade-cancel-on-discard path (that cascade is gone — nothing live to cancel). Replace the final `assert isinstance(app.screen, ModelScreen)` with `assert app.return_value is None` (Discard now exits the app too).

- [ ] **Step 9: Fix `test_discard_after_move_reverts_without_duplicates`**

This test doesn't reference download machinery, but Discard now exits the app instead of leaving you on `ModelScreen`. Update its post-discard assertions accordingly: replace any `isinstance(app.screen, ModelScreen)` check with `app.return_value is None`, and move any registry/state assertions to run after the `async with app.run_test()` block closes (reading from disk via `load_registry`/`load_state`), matching the pattern in Step 7.

- [ ] **Step 10: Fix the ctrl+q tests**

Update `test_ctrl_q_exits_immediately_with_no_active_downloads`'s docstring (drop the "no active downloads" framing — there's no such concept anymore; it's just "ctrl+q with an empty queue exits immediately"). No assertion change needed.

`test_ctrl_q_on_model_screen_confirms_pending_queue_instead_of_dropping_it` needs no behavior change (a pending queue still opens `ConfirmExitDialog` on ctrl+q) — just double-check its setup doesn't reference `app.downloads` (per the file-wide import fix in Step 1, it shouldn't).

Confirm there is a plain-`Escape` (not just ctrl+q) counterpart for both the empty-queue and pending-queue cases somewhere in this file or `test_models.py`; if not, add one here:

```python
@pytest.mark.asyncio
async def test_escape_with_empty_queue_exits_app(tmp_path, monkeypatch):
    """Escape is ModelScreen's own binding for the same action_back()
    ctrl+q delegates to — an empty queue must exit immediately, no
    dialog, mirroring the ctrl+q case above."""
    _seed_registry_and_state(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.press("escape")
        await pilot.pause()
        assert app.return_value is None
```

- [ ] **Step 11: Run the file**

Run: `cd modelman && uv run pytest tests/screens/test_app_navigation.py -q`
Expected: PASS, all green.

- [ ] **Step 12: Run the full modelman suite**

Run: `cd modelman && make test`
Expected: PASS, all green (this is the first point since Task 1 where the entire suite should be green again).

- [ ] **Step 13: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/tests/screens/test_app_navigation.py
git commit -m "$(cat <<'EOF'
test(modelman): rewrite test_app_navigation.py for the downloads-on-exit redesign

Every Apply-flow test now asserts on the app's exit value (a
QueuedOps) instead of waiting on a StatusScreen; Discard also exits
the app now. Deletes the quit-guard-while-downloading tests. Part of
the downloads-on-exit redesign - completes plan item #7.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 8: Update docs

**Files:**
- Modify: `modelman/CLAUDE.md`
- Modify: `modelman/README.md`
- Modify: `docs/guides/02-providers-and-models.md`

**Interfaces:** N/A — documentation only.

- [ ] **Step 1: `modelman/README.md` — CLI command list**

Remove the line:

```
modelman download <family>      # open the TUI, cursor scrolled to that family
```

from the CLI usage block (leaving `modelman`, `modelman sync`, `modelman expose`, `modelman unexpose`, `modelman migrate`).

- [ ] **Step 2: `modelman/README.md` — TUI section**

Replace:

```
The TUI has two screens:

- **Model screen** — the app's only/root screen: ...
  ...
- **Status screen** — when you apply on exit, the model screen hands off to
  a status screen that streams per-item progress (`Deleting …`,
  `Downloaded …`, `Saving …`) into a scrollable log. Provider progress is
  forwarded live: Ollama's pull output (stripped of ANSI escapes) and
  huggingface_hub tqdm bars (per-file bytes/rate) appear as each line is
  emitted. `Escape` mid-run pops a Cancel-or-Wait dialog: `Cancel` kills any
  running subprocess (Ollama) and stops the queue; `Wait` keeps waiting.
  Once the run completes (or is cancelled), `Escape` returns to the model
  screen underneath, already showing the post-apply state.

All dialogs share a layout convention: the cancel/default button is
rightmost, the primary action is to its left, and pressing `Escape`
cancels (this works even when an Input is focused). Destructive prompts
(`ConfirmModal`, `ConfirmExitDialog`, `CancelApplyDialog`) focus the
safe button on open so a reflexive `Enter` is never destructive.

All model changes (adds, edits, deletes, ready toggles, exposure toggles,
moves) are queued in memory. On exit, a confirmation dialog shows the
pending set; confirming runs **deletes, then moves, then ready changes
(downloads/clears/flag flips), then exposure changes**, and writes
`registry.toml` + `modelman.toml` once. A delete for a not-on-disk model
is legal: the on-disk removal is skipped, but the registry/state
cleanup, lifecycle events, and any cascade-unexpose still run.
```

with:

```
The TUI has a single screen:

- **Model screen** — the app's only/root screen: ...
  ...

All model changes (adds, edits, deletes, ready toggles, exposure toggles,
moves) are queued in memory — nothing downloads or writes to disk while
the TUI is open (add/edit are the one exception: registry.toml is
persisted immediately, so a discarded session doesn't lose a
concurrently-typed edit). `Escape`/`Ctrl+Q` with a pending queue shows a
confirmation dialog listing the pending set; `Apply` or `Discard` both
exit the app. On Apply, `main.py` runs the queue in the plain terminal
after the TUI closes: **deletes, then moves, then ready changes
(downloads/clears/flag flips), then exposure changes**, printing
provider progress and a thin lifecycle line per operation to stdout,
then writes `registry.toml` + `modelman.toml` once. A failed operation
is reported in an error summary at the end and the process exits
non-zero; `Ctrl+C` mid-run cancels the remaining queue (already-applied
steps are not undone, nothing is saved for the interrupted run). A
delete for a not-on-disk model is legal: the on-disk removal is
skipped, but the registry/state cleanup, lifecycle events, and any
cascade-unexpose still run.

All dialogs share a layout convention: the cancel/default button is
rightmost, the primary action is to its left, and pressing `Escape`
cancels (this works even when an Input is focused). Destructive prompts
(`ConfirmModal`, `ConfirmExitDialog`) focus the safe button on open so a
reflexive `Enter` is never destructive.
```

(Keep the `...` sections of the Model screen bullet — its column/key list — unchanged; only replace the two-screen framing and the Status-screen bullet as shown.)

- [ ] **Step 3: `modelman/README.md` — Expose section**

Replace:

```
In the TUI, press `x` on a model row to queue an exposure toggle; it applies
on exit alongside downloads/deletes (a not-ready model is downloaded/pulled
first). The EXPOSED column shows `Y` when
exposed (or queued to expose) and `–` otherwise.
```

with:

```
In the TUI, press `x` on a model row to queue an exposure toggle; it
applies after you exit via Apply, alongside every other queued change (a
not-ready model is downloaded/pulled first, in the terminal, once the
TUI has closed). The EXPOSED column shows `Y` when exposed (or queued
to expose) and `–` otherwise.
```

- [ ] **Step 4: `modelman/CLAUDE.md` — remove the `download` command and DownloadManager/StatusScreen architecture bullets**

In the "Entry point" bullet, remove `` `download <family>` (opens the TUI's model list scrolled to that family's first row), `` from the subcommand list.

In the "Textual TUI" bullets, remove the sentence: "When constructed with `family=` (from `modelman download <family>`), the cursor scrolls to that family's first row after the initial load instead of opening a separate filtered screen." from the `ModelmanApp`/`app.py` bullet.

Replace the entire "Downloads (async, background)" section (from `### Downloads (async, background)` through the `ProgressTqdm.set_lock(threading.RLock())`-adjacent `HF_DOWNLOAD_LOCK` bullet, i.e. everything up to but not including "### Adding a new TUI screen") with:

```
### Downloads (queued, applied on exit)

- Every TUI action — including a real download/pull against a
  reconcilable provider (ollama/omlx/llamacpp) — just populates
  `ModelScreen`'s `queued_ready`/`queued_deletes`/`queued_moves`/
  `queued_exposes` dicts. Nothing runs during the TUI session; there is
  no background download manager, no live progress screen, and no quit
  guard, because nothing is ever mid-flight while the TUI is open.
- `Escape`/`ctrl+q` with a pending queue shows `ConfirmExitDialog`;
  `Apply` exits the app carrying a `queue.py::QueuedOps` (the app's
  `App[QueuedOps | None]` return value), `Discard` restores the
  pre-session snapshot and exits with `None`, `Cancel` stays in the TUI.
  `main.py::run_tui()` runs `run_queued_ops()` against fresh on-disk
  state after the TUI process's `run()` call returns — see "Entry point"
  above.
- `PendingChanges.apply()` (`queue.py`) owns real downloads again: a
  ready-on against a mapped, non-`manages_own_cache` provider calls
  `provider.download(variant, on_progress=...)` directly, sequentially,
  the same way it did before an async `DownloadManager` briefly existed.
  `Ctrl+C` during `run_queued_ops()` raises `KeyboardInterrupt` in the
  foreground process; the runner catches it, calls `pending.cancel()`
  (reusing the existing `cancelled`/`aborted()` gate), and reports how
  many steps completed vs. were skipped. Nothing is saved for an
  interrupted run.
```

- [ ] **Step 5: `docs/guides/02-providers-and-models.md`**

Replace:

```
Scope split (verified via `uv run modelman --help` and the TUI key lists below): **TUI-only** = adding/editing/deleting providers and models (writes `registry.toml`), display-name edits, and downloads with live progress (`download` opens the TUI at a model screen). **CLI** = `expose`/`unexpose`, `sync`, `migrate`, `benchmark`, `usage`; bare `modelman` and `modelman download <family>` open the TUI.
```

with:

```
Scope split (verified via `uv run modelman --help` and the TUI key lists below): **TUI-only** = adding/editing/deleting providers and models (writes `registry.toml`), display-name edits, and queuing ready-on/off (downloaded/applied only after you exit via Apply — see modelman/CLAUDE.md's "Downloads (queued, applied on exit)"). **CLI** = `expose`/`unexpose`, `sync`, `migrate`, `benchmark`, `usage`; bare `modelman` opens the TUI.
```

Remove or update the two `uv run modelman download ...` example command blocks (around lines 151 and 170) — since the command is gone, replace both with the equivalent bare-`modelman` + `r` keypress instructions, or drop the examples if the surrounding prose no longer needs them. Read the surrounding paragraph in that file before editing to keep the prose coherent (this file wasn't part of this plan's research — confirm the exact wording fits before committing).

- [ ] **Step 6: Verify links and prose**

Run: `cd modelman && cd .. && make check-links`
Expected: PASS.

Read back all three changed files once to confirm the prose reads coherently end to end (no dangling "the two screens" references, no leftover mentions of `StatusScreen`/`DownloadScreen`/`download <family>`).

- [ ] **Step 7: Commit**

```bash
git add modelman/CLAUDE.md modelman/README.md docs/guides/02-providers-and-models.md
git commit -m "$(cat <<'EOF'
docs(modelman): document the downloads-on-exit redesign

Removes references to DownloadManager/DownloadScreen/StatusScreen/the
download <family> command; documents QueuedOps and the post-exit
runner. Part of the downloads-on-exit redesign - completes plan item #8.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

## Task 9: Full verification

**Files:** none (verification only)

**Interfaces:** N/A — verification only.

- [ ] **Step 1: Run the full monorepo test-all target**

Run: `cd /Users/keith/github/ohanaverse/local-ai-setup && make test-all`
Expected: PASS — lint (`make lint-shell` + `check-links`), modelman `make check`/`make test`, wt `go build`/`vet`/`test` all green. (wt is unaffected by this change but `make test-all` runs it anyway; a wt failure here would indicate an unrelated pre-existing issue, not this plan's work.)

- [ ] **Step 2: Confirm no stray references remain**

Run: `cd modelman && grep -rn "DownloadManager\|DownloadScreen\|StatusScreen\|QuitBlockedModal\|CancelApplyDialog\|scroll_to_family\|_initial_family" src/ tests/`
Expected: no output.

Run: `cd modelman && grep -rn "modelman download" README.md ../docs/guides/*.md ../modelman/CLAUDE.md 2>/dev/null`
Expected: no output (aside from historical `docs/superpowers/plans/`/`specs/` files, which are intentionally left as historical record and are out of scope for this grep — restrict to the three files this plan actually edits).

- [ ] **Step 3: Manual end-to-end smoke test**

Repeat Task 4 Step 16's smoke test, extended to cover the full spec:
1. Start with a scratch `MODELMAN_REGISTRY`/`MODELMAN_STATE` pointing at an empty directory.
2. `uv run modelman`, add two ollama models (`a`), press `r` on both to queue ready-on, `escape`, confirm the dialog lists both as `↓` rows, press `y`.
3. Confirm the TUI exits to the shell and both downloads run **sequentially** (the second doesn't start until the first's progress lines finish), with a thin `Downloading .../  done: downloaded ...` line pair per model.
4. Re-run `uv run modelman`, queue one ready-off (delete) on a downloaded model and one expose toggle, `escape`, `d` (Discard) — confirm the TUI exits immediately with no output and `registry.toml`/`modelman.toml` are unchanged from before that session.
5. Re-run `uv run modelman`, queue a ready-on for a model backed by a provider that will fail (e.g. an intentionally-wrong ollama tag), `escape`, `y` — confirm the process prints the `Completed with errors:` block and exits non-zero (`echo $?`).
6. Re-run `uv run modelman`, queue a ready-on, `escape`, `y`, and press `Ctrl+C` while the download is in progress — confirm a `Cancelled: N steps completed, M remaining skipped.` line prints and the process exits non-zero.

Record the outcome in the PR description or final summary; this step has no automated equivalent (per the spec's own "Manual" testing section) and is the actual acceptance gate for the redesign's core promise (real, working sequential downloads with a working Ctrl+C).

- [ ] **Step 4: Final commit (if Step 3 surfaced any fixes)**

If Step 3 required any code changes, commit them with a `fix(modelman):` message describing what the manual smoke test caught, tagged `PR review fixes` style (not a plan-item reference, since the plan's own execution is complete at this point).
