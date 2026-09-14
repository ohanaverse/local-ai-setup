# Code Review Fixes (downloads-on-exit) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all 10 findings from the `/code-review high` run on the `downloads-on-exit` branch (worktree: `.worktrees/downloads-on-exit`), without regressing any existing test or the branch's documented design (`docs/superpowers/specs/2026-09-13-downloads-on-exit-design.md`).

**Architecture:** Each finding is fixed in place with a focused change plus a regression test (or, for the pure comment/perf cleanups, verified via the existing suite). No new abstractions beyond what a finding explicitly calls for (e.g. finding #7's `_finish_ready` helper).

**Tech Stack:** Python 3.13, `uv`/pytest, Typer CLI, Textual TUI (unaffected by these changes).

**Spec:** This plan implements review findings, not a spec document. Where a finding's literal ask would fight the branch's own design spec, this plan says so and narrows the fix — see Task 3.

## Global Constraints

- Run focused tests per task, not the full suite (repo convention — see `modelman/CLAUDE.md`). Task 10 runs the full suite once at the end.
- Every new test needs a comment/docstring explaining what it covers and why (user's global test-documentation preference) — already reflected in the test bodies below.
- `make lint` enforces Ruff bugbear B905 (`zip()` needs explicit `strict=`) — not touched by this plan, noted for awareness only.
- Do not change the "cancellation discards everything" semantics documented in `docs/superpowers/specs/2026-09-13-downloads-on-exit-design.md` and locked in by `test_apply_cancelled_before_moves_skips_them`, `test_apply_cancelled_persists_no_family_entry`, and `test_apply_cancelled_mid_ready_loop_skips_remaining_and_does_not_save` (all in `tests/test_queue.py`) — Task 3 must not touch that path.
- Each task's commit message must end with "- completes plan item #N" per the user's global workflow preference, plus the standard Claude attribution lines already established for this session.

---

### Task 1: Fix overly-broad `except TypeError` in `PendingChanges._download()`

**Finding #3.** `_download()`'s `except TypeError: return provider.download(variant)` fallback exists to support a provider whose `download()` has no `on_progress` parameter, but as written it also swallows a genuine `TypeError` raised from *inside* a provider's real download logic — silently retrying (doubling network/disk work) instead of surfacing the real bug as a failure.

**Files:**
- Modify: `src/modelman/queue.py:618-623` (`PendingChanges._download`)
- Test: `tests/test_queue.py`

**Interfaces:**
- No signature changes. `_download(self, variant: VariantSpec, on_progress: EventFn | None = None) -> str` behavior only.

- [ ] **Step 1: Write the two failing/pinning tests**

Add to `tests/test_queue.py` (near the other `_download`-adjacent tests, e.g. after `test_apply_ready_on_forwards_progress_lines`):

```python
def test_apply_ready_on_download_falls_back_when_provider_lacks_on_progress_param(tmp_path):
    """A provider whose download() has no on_progress parameter at all
    (a minimal/legacy Provider implementation) must still complete via
    the args-only fallback — the case the except TypeError branch exists
    for. Regression guard for the fix in test below: this fallback must
    keep working after narrowing the except to only this case."""
    reg, reg_path = _registry_with(
        tmp_path, _entry(id="ollama/x", family="f", provider="ollama", name="x:7b")
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()

    class _NoProgressProvider:
        def __init__(self):
            self.calls = 0

        def download(self, variant):
            self.calls += 1
            return "ollama:x:7b"

        def size_of(self, variant):
            return None

    provider = _NoProgressProvider()
    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[("ollama/x", _variant(id="ollama/x", provider="ollama", name="x:7b"), True)],
    )
    pending.apply(on_progress=lambda line: None)

    assert provider.calls == 1
    assert state.get("ollama/x").ready is True
    assert pending.failures == []


def test_apply_ready_on_download_propagates_typeerror_from_inside_provider(tmp_path):
    """A TypeError raised from inside provider.download() itself (not a
    signature mismatch on the on_progress parameter) must be recorded as
    a real failure, not silently retried without on_progress. Regression
    for a review finding: the old `except TypeError: retry without
    on_progress` fallback caught ANY TypeError from anywhere in the
    download call tree, masking real bugs and doubling download work."""
    reg, reg_path = _registry_with(
        tmp_path, _entry(id="ollama/x", family="f", provider="ollama", name="x:7b")
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()

    class _BuggyProvider:
        def __init__(self):
            self.calls = 0

        def download(self, variant, on_progress=None):
            self.calls += 1
            raise TypeError("boom: unrelated bug inside download")

    provider = _BuggyProvider()
    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[("ollama/x", _variant(id="ollama/x", provider="ollama", name="x:7b"), True)],
    )
    pending.apply()

    assert provider.calls == 1  # not silently retried
    assert any("download ollama/x: boom" in f for f in pending.failures)
```

- [ ] **Step 2: Run the new tests to see the second one fail**

Run: `uv run pytest tests/test_queue.py -k "download_falls_back_when_provider_lacks or download_propagates_typeerror" -v`
Expected: the first test PASSes already (current code handles that case); the second FAILs because `provider.calls == 2` (silently retried) instead of `1`, and no failure is recorded (the retry raises `TypeError: download() got an unexpected keyword argument 'on_progress'`... actually recheck: `_BuggyProvider.download` accepts `on_progress`, so the retry-without-on_progress call also raises the same `TypeError("boom...")` again, uncaught by the current bare `except TypeError`, which only catches once — so the *current* code will actually raise this TypeError out of `_download` uncaught by the `except Exception as exc` wrapper in `apply()`'s ready loop, i.e. it WILL be caught as a normal failure by that wrapper. Confirm the actual observed failure mode via the test run, and record it here before writing the fix.)

- [ ] **Step 3: Implement the traceback-depth check**

In `src/modelman/queue.py`, replace:

```python
    def _download(self, variant: VariantSpec, on_progress: EventFn | None = None) -> str:
        provider = self.providers[variant["provider"]]
        try:
            return provider.download(variant, on_progress=on_progress)  # type: ignore[attr-defined]
        except TypeError:
            return provider.download(variant)  # type: ignore[attr-defined]
```

with:

```python
    def _download(self, variant: VariantSpec, on_progress: EventFn | None = None) -> str:
        provider = self.providers[variant["provider"]]
        try:
            return provider.download(variant, on_progress=on_progress)  # type: ignore[attr-defined]
        except TypeError as exc:
            # Only treat this as "provider.download() has no on_progress
            # parameter" when the TypeError was raised at the call site
            # itself (no further frames — the callee's body never started
            # executing). A TypeError raised from inside a provider's real
            # download logic has at least one additional frame and must
            # propagate as a real failure instead of triggering a silent,
            # work-doubling retry.
            if exc.__traceback__ is not None and exc.__traceback__.tb_next is not None:
                raise
            return provider.download(variant)  # type: ignore[attr-defined]
```

- [ ] **Step 4: Run the tests to verify both pass**

Run: `uv run pytest tests/test_queue.py -k "download_falls_back_when_provider_lacks or download_propagates_typeerror" -v`
Expected: both PASS.

- [ ] **Step 5: Run the full queue test file to check for regressions**

Run: `uv run pytest tests/test_queue.py -q`
Expected: all PASS (2099-line file — this confirms no other test relied on the old broad-catch behavior).

- [ ] **Step 6: Commit**

```bash
git add src/modelman/queue.py tests/test_queue.py
git commit -m "$(cat <<'EOF'
fix(modelman): narrow _download's TypeError fallback to signature mismatches only

The except TypeError fallback in PendingChanges._download() was meant to
detect a provider without an on_progress parameter, but caught any
TypeError raised anywhere inside provider.download(), silently retrying
(and masking) real bugs. Narrowed via traceback depth: only a TypeError
raised at the call site itself (no further frames) triggers the fallback.

completes plan item #1 (review finding: overly broad except TypeError in _download)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

### Task 2: Fix falsy-zero `size_bytes` check and deduplicate the ready-loop finish logic

**Findings #4 and #7.** `if size_bytes:` at `queue.py:507` treats a legitimate 0-byte artifact the same as "size unknown" (unlike the `is None` check two lines above it). Separately, the ready loop's three branches (flag-only flip, real download, clear) duplicate the same `state.set(...)` / `_touched_model_ids.add(...)` / `emit(...)` sequence, and the copies are already drifting — the size-bytes bug above is a direct symptom. Fixing both together: extracting the shared `_finish_ready` helper is where the `is not None` fix naturally lives.

**Files:**
- Modify: `src/modelman/queue.py` (new method `_finish_ready`, and the three ready-loop branches at what is currently lines 442-531)
- Test: `tests/test_queue.py`

**Interfaces:**
- New method: `PendingChanges._finish_ready(self, emit: EventFn, model_id: str, label: str, *, ready: bool, disk_path: str | None = None, size_bytes: int | None = None, keep_existing_path: bool = False, done_tag: str) -> None`
- No change to `apply()`'s public signature or emitted tag *names* — only the size-suffix behavior for `download:done` changes (0 bytes now shown as `0 B` instead of omitted).

- [ ] **Step 1: Write the failing test for the size_bytes fix**

Add to `tests/test_queue.py` (near `test_apply_ready_on_forwards_progress_lines`):

```python
def test_apply_ready_on_download_zero_byte_size_still_shown(tmp_path):
    """size_of() legitimately returning 0 (a truncated/corrupt artifact
    that still 'completed') must still show in the done event as '0 B',
    not be treated as size-unknown and silently dropped. Regression:
    `if size_bytes:` is a falsy-zero check that behaved identically for
    0 and None, unlike the `is None` check used for the same value two
    lines above it."""
    reg, reg_path = _registry_with(
        tmp_path, _entry(id="ollama/x", family="f", provider="ollama", name="x:7b")
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()
    provider = MagicMock()
    provider.download.return_value = "ollama:x:7b"  # not a real file -> _size_of() returns None
    provider.size_of.return_value = 0  # provider fallback reports a real, legitimate 0

    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[("ollama/x", _variant(id="ollama/x", provider="ollama", name="x:7b"), True)],
    )
    events: list[str] = []
    pending.apply(on_event=events.append)

    assert "download:done|ollama/x|x:7b|0 B" in events
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `uv run pytest tests/test_queue.py -k zero_byte_size -v`
Expected: FAIL — current code emits `"download:done|ollama/x|x:7b"` (no suffix) because `if size_bytes:` is falsy for `0`.

- [ ] **Step 3: Add the `_finish_ready` helper**

In `src/modelman/queue.py`, add this method to `PendingChanges`, near `_cleanup_partial_download` (after it, before `cancel`):

```python
    def _finish_ready(
        self,
        emit: EventFn,
        model_id: str,
        *,
        ready: bool,
        disk_path: str | None = None,
        size_bytes: int | None = None,
        keep_existing_path: bool = False,
        done_tag: str,
    ) -> None:
        """Persist a ready-loop item's final state.ready/disk_path/size_bytes,
        mark it touched for the final save, and emit its pre-built :done tag.

        Shared by all three ready-loop branches (flag-only flip, real
        download, clear) so a change to this sequence — a new field, a
        changed emit shape — needs one edit instead of three separately
        drifting copies. `keep_existing_path=True` is for the flag-only
        ready-ON flip, which must not clobber a disk_path/size_bytes it
        never itself set.
        """
        if keep_existing_path:
            self.state.set(model_id, replace(self.state.get(model_id), ready=ready))
        else:
            self.state.set(
                model_id,
                replace(
                    self.state.get(model_id),
                    ready=ready,
                    disk_path=disk_path,
                    size_bytes=size_bytes,
                ),
            )
        self._touched_model_ids.add(model_id)
        emit(done_tag)
```

- [ ] **Step 4: Rewrite the three ready-loop branches to use it**

In `apply()`, replace the flag-only branch:

```python
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
```

with:

```python
                emit(f"ready:start|{model_id}|{label}")
                if not target:
                    try:
                        _remove_local_artifact(self.state, variant)
                    except Exception as exc:  # noqa: BLE001
                        reason = _reason(exc)
                        self.failures.append(f"clear {model_id}: {reason}")
                        emit(f"delete:fail|{model_id}|{label}|{reason}")
                self._finish_ready(
                    emit,
                    model_id,
                    ready=target,
                    keep_existing_path=target,
                    done_tag=f"ready:done|{model_id}|{label}",
                )
```

Replace the real-download branch's tail (from `size_bytes = self._size_of(local_path)` through the final `emit(f"download:done|{model_id}|{label}")`):

```python
                size_bytes = self._size_of(local_path)
                if size_bytes is None:
                    try:
                        size_bytes = provider.size_of(variant)  # type: ignore[attr-defined]
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
```

with:

```python
                size_bytes = self._size_of(local_path)
                if size_bytes is None:
                    try:
                        size_bytes = provider.size_of(variant)  # type: ignore[attr-defined]
                    except Exception:  # noqa: BLE001
                        size_bytes = None
                suffix = f"|{human_bytes(size_bytes)}" if size_bytes is not None else ""
                self._finish_ready(
                    emit,
                    model_id,
                    ready=True,
                    disk_path=local_path,
                    size_bytes=size_bytes,
                    done_tag=f"download:done|{model_id}|{label}{suffix}",
                )
```

Replace the clear branch's tail (from `self.state.set(` through `emit(f"delete:done|{model_id}|{label}")`):

```python
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

with:

```python
                self._finish_ready(
                    emit,
                    model_id,
                    ready=False,
                    done_tag=f"delete:done|{model_id}|{label}",
                )
```

- [ ] **Step 5: Run the new test and the full queue suite**

Run: `uv run pytest tests/test_queue.py -k zero_byte_size -v`
Expected: PASS.

Run: `uv run pytest tests/test_queue.py -q`
Expected: all PASS — this file has extensive coverage of all three ready-loop branches (ready-on flag-only, ready-off flag-only, ready-on download, ready-off clear, shared-artifact skips), so a full pass here is strong evidence the refactor preserved behavior exactly except for the intended 0-byte fix.

- [ ] **Step 6: Commit**

```bash
git add src/modelman/queue.py tests/test_queue.py
git commit -m "$(cat <<'EOF'
fix(modelman): fix falsy-zero size check and dedupe ready-loop finish logic

if size_bytes: treated a legitimate 0-byte artifact the same as
size-unknown, unlike the `is None` check two lines above it. Fixed by
extracting the ready loop's three duplicated state.set/touched-ids/emit
sequences into one _finish_ready helper, which is also where the
is-not-None fix naturally lives — closing the drift risk the duplication
itself created.

completes plan item #2 (review findings: falsy-zero size_bytes check; triplicated finish-sequence logic)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

### Task 3: Persist already-completed deletes/moves before an exception escapes `apply()`

**Finding #1**, narrowed. `apply()` saves once, at the very end. If a `BaseException` (a real Ctrl+C landing inside the blocking `provider.download()` call, caught at line 482 and re-raised after cleanup) propagates out of the ready loop, it skips the final save entirely — discarding already-completed **deletes** and **moves** from the same batch, which are irreversible (a real on-disk file was already removed, a registry row already dropped in memory).

**Important scope note:** the project's own design spec (`docs/superpowers/specs/2026-09-13-downloads-on-exit-design.md`) and three existing tests explicitly lock in "cancellation (the `self.cancelled` flag / `aborted()` early-return path) discards everything, nothing saved" as intentional. **Do not change that.** The fix here only adds a safety net for a genuine exception/`KeyboardInterrupt` *propagating out* of `apply()` — which in the current single-threaded design is the only way a real Ctrl+C actually manifests (a raw `KeyboardInterrupt` raised inside whatever's currently executing, i.e. inside `provider.download()`'s blocking call — `aborted()`'s flag-based check is never hit by an actual OS signal in this codebase, only by tests calling `pending.cancel()` manually before `apply()` runs). A plain `except BaseException: ...; raise` does not fire on the existing tests' `return`-based early exits, so this cannot regress them.

**Files:**
- Modify: `src/modelman/queue.py` (`apply()`, and a new `_persist` method)
- Test: `tests/test_queue.py`

**Interfaces:**
- New method: `PendingChanges._persist(self, emit: EventFn) -> None` — the exact body of the current final save block (`emit("save:start")` through the `except Exception` / `emit(f"save:fail|...")`), extracted so both the normal end-of-apply save and the new exception safety net call the same code.

- [ ] **Step 1: Write the failing test**

Add to `tests/test_queue.py` (near `test_apply_cancelled_mid_ready_loop_skips_remaining_and_does_not_save`):

```python
def test_apply_persists_prior_deletes_when_download_raises_keyboardinterrupt(tmp_path):
    """A real Ctrl+C landing inside provider.download() (KeyboardInterrupt
    raised from the blocking call, not a pre-set cancelled flag) must not
    discard an already-completed delete from earlier in the same apply()
    call — the on-disk artifact is already gone and the registry row
    already dropped in memory; losing the save leaves registry.toml
    listing a model whose weights no longer exist, with nothing to ever
    repair it. This is distinct from (and must not change) the existing
    flag-based cancellation semantics, which intentionally save nothing —
    see test_apply_cancelled_mid_ready_loop_skips_remaining_and_does_not_save."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="ollama/a", family="f", provider="ollama", name="a:7b"),
        _entry(id="ollama/b", family="f", provider="ollama", name="b:7b"),
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()
    provider = MagicMock()
    provider.is_downloaded.return_value = True
    provider.artifact_paths.return_value = None
    provider.download.side_effect = KeyboardInterrupt()

    pending = PendingChanges(
        registry=reg,
        state=state,
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="a:7b"))],
        ready=[("ollama/b", _variant(id="ollama/b", provider="ollama", name="b:7b"), True)],
    )
    with pytest.raises(KeyboardInterrupt):
        pending.apply()

    provider.delete.assert_called_once()  # the delete really ran
    reloaded = load_registry(reg_path)
    assert all(m.id != "ollama/a" for m in reloaded.models)  # and is persisted
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `uv run pytest tests/test_queue.py -k persists_prior_deletes_when_download_raises -v`
Expected: FAIL — `pytest.raises(KeyboardInterrupt)` passes (the exception does propagate today), but `load_registry(reg_path)` still lists `ollama/a` because the final save never ran.

- [ ] **Step 3: Extract `_persist` and wrap the body in a safety-net `try`**

In `src/modelman/queue.py`, add this method to `PendingChanges` (after `_finish_ready`, before `cancel`):

```python
    def _persist(self, emit: EventFn) -> None:
        """Save registry.toml and merge this run's touched state rows onto
        a freshly-loaded modelman.toml. Called once at the normal end of
        apply(), and again (best-effort) from the exception safety net
        below if a BaseException propagates out of apply() early — so an
        already-completed delete or move from this same batch is not lost
        just because a later step raised or was interrupted.
        """
        emit("save:start")
        try:
            save_registry(self.registry, self.registry_path)
            with locked_state(self.state_path) as fresh_state:
                for mid in self._touched_model_ids:
                    if mid in self.state.models:
                        fresh_state.set(mid, self.state.get(mid))
                    else:
                        # The deletes loop removed this id from self.state —
                        # "touching" it means removal, so mirror that on the
                        # fresh store. Writing self.state.get(mid) here would
                        # insert a default (ready=False) entry and resurrect
                        # the row this apply just deleted.
                        fresh_state.models.pop(mid, None)
                for family in self._forgotten_families:
                    fresh_state.forget_family(family)
            emit("save:done")
        except Exception as exc:  # noqa: BLE001
            reason = _reason(exc)
            self.failures.append(f"save: {exc}")
            emit(f"save:fail|{reason}")
```

Then replace the final block of `apply()`:

```python
        emit("save:start")
        try:
            save_registry(self.registry, self.registry_path)
            with locked_state(self.state_path) as fresh_state:
                for mid in self._touched_model_ids:
                    if mid in self.state.models:
                        fresh_state.set(mid, self.state.get(mid))
                    else:
                        # The deletes loop removed this id from self.state —
                        # "touching" it means removal, so mirror that on the
                        # fresh store. Writing self.state.get(mid) here would
                        # insert a default (ready=False) entry and resurrect
                        # the row this apply just deleted.
                        fresh_state.models.pop(mid, None)
                for family in self._forgotten_families:
                    fresh_state.forget_family(family)
            emit("save:done")
        except Exception as exc:  # noqa: BLE001
            reason = _reason(exc)
            self.failures.append(f"save: {exc}")
            emit(f"save:fail|{reason}")

        emit("apply:done")
```

with:

```python
        self._persist(emit)
        emit("apply:done")
```

Now wrap the whole operational body in the new safety net. The body runs from the deletes loop through the exposes block — i.e. everything between the empty-queue fast-path `return` and the `self._persist(emit)` line just added. Indent that entire region one level deeper inside a new `try:`, and add the `except BaseException` handler immediately before `self._persist(emit)` / `emit("apply:done")`:

```python
        try:
            # ids removed by this apply — moves referencing them are moot
            deleted_ids: set[str] = set()
            ... # (everything currently between here and the old final-save block, re-indented one level)
        except BaseException:
            # A real exception (including a genuine Ctrl+C KeyboardInterrupt
            # raised inside a blocking provider call, see the download
            # branch's own except BaseException above) propagating out of
            # the loops above must not discard already-completed
            # irreversible steps (deletes, moves) from this same apply()
            # call. This does NOT apply to the aborted()/self.cancelled
            # early-return path above (a plain `return`, not an exception)
            # — that path's "nothing saved" semantics are intentional and
            # tested (see docs/superpowers/specs/2026-09-13-downloads-on-
            # exit-design.md and the cancellation tests in this file).
            self._persist(emit)
            raise

        self._persist(emit)
        emit("apply:done")
```

Re-indenting a ~180-line block is mechanical but must be done carefully (every `return` inside stays a bare `return` — those are the intentional no-save exits and must NOT be touched or converted to anything that would trigger the `except BaseException` handler). Use your editor's block-indent, then re-read the whole method once to confirm indentation is consistent and no `return` was accidentally changed.

- [ ] **Step 4: Run the new test and the full cancellation-related tests**

Run: `uv run pytest tests/test_queue.py -k "persists_prior_deletes_when_download_raises or cancelled" -v`
Expected: all PASS, including the three pre-existing cancellation tests (confirming their "nothing saved" semantics are untouched).

- [ ] **Step 5: Run the full queue suite**

Run: `uv run pytest tests/test_queue.py -q`
Expected: all PASS.

- [ ] **Step 6: Run the run_tui command tests too** (they exercise `apply()` through `run_queued_ops`, including the real `test_run_queued_ops_keyboard_interrupt_prints_cancelled_summary` test)

Run: `uv run pytest tests/commands/test_run_tui.py -q`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add src/modelman/queue.py tests/test_queue.py
git commit -m "$(cat <<'EOF'
fix(modelman): persist completed deletes/moves before a raised exception exits apply()

apply() saved registry/state exactly once, at the very end. A real
Ctrl+C landing inside provider.download() raises KeyboardInterrupt,
caught and re-raised past that save — discarding an already-completed
delete or move from the same batch, even though the on-disk artifact was
already removed and the registry row already dropped in memory. Added a
best-effort safety-net save (extracted into _persist, shared with the
normal end-of-apply save) that fires only when a real exception
propagates out of apply(), not on the existing flag-based cancellation
path, whose "nothing saved" semantics remain exactly as documented in
the downloads-on-exit design spec and locked in by existing tests.

completes plan item #3 (review finding: exception mid-apply discards prior destructive-op state)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

### Task 4: Add test coverage for `_cleanup_partial_download`'s shared-artifact guard

**Finding #6.** `_cleanup_partial_download`'s shared-artifact-owner guard (ported from the deleted `DownloadManager._finish`) has zero test coverage in the current suite. The deleted `tests/test_downloads.py` had an explicit test for this exact guard (`test_cancel_cleanup_skips_rmtree_when_artifact_is_shared`) that was never replaced with an equivalent for `queue.py`'s version.

**Files:**
- Test only: `tests/test_queue.py`

**Interfaces:**
- No production code changes — this task only adds coverage for existing, correct behavior (verified by reading `_cleanup_partial_download` in `src/modelman/queue.py:245-257`, which already correctly checks `find_shared_artifact_owner(...) is None` before calling `provider.cleanup_partial_download(variant)`).

- [ ] **Step 1: Write the two tests**

Add to `tests/test_queue.py` (near `test_apply_delete_skips_artifact_shared_with_other_entry`):

```python
def test_cleanup_partial_download_skips_when_artifact_shared_with_other_entry(tmp_path):
    """_cleanup_partial_download (run after a cancelled/failed download,
    see apply()'s DownloadCancelled/BaseException handlers) must not
    remove a partial artifact when another registry entry's on-disk path
    overlaps (omlx keys storage on the repo basename, so two different
    repos can collide) — that would destroy the other entry's
    already-completed weights. Regression test restoring coverage lost
    when the DownloadManager-era test_downloads.py
    (test_cancel_cleanup_skips_rmtree_when_artifact_is_shared) was
    deleted without an equivalent for this queue.py method."""
    reg_path = tmp_path / "registry.toml"
    a = ModelEntry(
        id="omlx/a",
        family="f",
        provider_id="omlx",
        model_name="a",
        fetch=Fetch(repo="org1/qwen", files=None, quantizations=None),
    )
    b = ModelEntry(
        id="omlx/b",
        family="f",
        provider_id="omlx",
        model_name="b",
        fetch=Fetch(repo="org2/qwen", files=None, quantizations=None),
    )
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", auth=AuthConfig(type="none"))],
        models=[a, b],
    )
    save_registry(reg, reg_path)

    omlx = MagicMock()
    omlx.name = "omlx"
    # Both entries resolve to the same on-disk target — the collision
    # find_shared_artifact_owner is meant to detect.
    omlx.path_of.return_value = str(tmp_path / "omlx-models" / "qwen")
    omlx.artifact_paths.side_effect = lambda v: frozenset([omlx.path_of(v)])

    pending = PendingChanges(
        registry=reg,
        state=_make_state(),
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={"omlx": omlx},
    )
    pending._cleanup_partial_download(
        omlx, _variant(id="omlx/a", provider="omlx", name="a", repo="org1/qwen")
    )

    omlx.cleanup_partial_download.assert_not_called()


def test_cleanup_partial_download_runs_when_no_shared_owner(tmp_path):
    """The common case: no conflicting registry entry, so a cancelled or
    failed download's partial artifact is actually cleaned up. Paired
    with the skip test above so the guard's both branches are covered."""
    reg_path = tmp_path / "registry.toml"
    a = ModelEntry(
        id="omlx/a",
        family="f",
        provider_id="omlx",
        model_name="a",
        fetch=Fetch(repo="org1/qwen", files=None, quantizations=None),
    )
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", auth=AuthConfig(type="none"))],
        models=[a],
    )
    save_registry(reg, reg_path)

    omlx = MagicMock()
    omlx.name = "omlx"
    omlx.path_of.return_value = str(tmp_path / "omlx-models" / "qwen")
    omlx.artifact_paths.side_effect = lambda v: frozenset([omlx.path_of(v)])

    pending = PendingChanges(
        registry=reg,
        state=_make_state(),
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={"omlx": omlx},
    )
    pending._cleanup_partial_download(
        omlx, _variant(id="omlx/a", provider="omlx", name="a", repo="org1/qwen")
    )

    omlx.cleanup_partial_download.assert_called_once()
```

- [ ] **Step 2: Run the tests**

Run: `uv run pytest tests/test_queue.py -k cleanup_partial_download -v`
Expected: both PASS immediately (production code is already correct — this task is pure coverage).

- [ ] **Step 3: Commit**

```bash
git add tests/test_queue.py
git commit -m "$(cat <<'EOF'
test(modelman): cover _cleanup_partial_download's shared-artifact guard

The guard (ported from the deleted DownloadManager._finish) had zero
test coverage after the old test_downloads.py's equivalent test was
deleted without a replacement. No production change — the guard was
already correct.

completes plan item #4 (review finding: shared-artifact cleanup guard has no test coverage)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

### Task 5: Fix `cancel_current()` being a no-op on the real Ctrl+C path

**Finding #2.** `cancel_current()` is a guaranteed no-op on the real interrupt path: `_tracked_popen_runner`'s `finally` clears `provider._current_proc` to `None` during exception unwinding, *before* `main.py`'s `except KeyboardInterrupt:` handler ever gets a chance to call `pending.cancel()` — by which point `apply()` has already fully propagated the exception past that frame. The fix must act inside `_tracked_popen_runner` itself, at the moment the interrupt is caught, not rely on a later external `cancel()` call.

**Files:**
- Modify: `src/modelman/providers/ollama.py`
- Test: `tests/test_providers/test_ollama.py`

**Interfaces:**
- New module-level helper: `_terminate_with_escalation(proc: subprocess.Popen) -> None` — the SIGTERM-then-watchdog-SIGKILL logic, extracted from `OllamaProvider.cancel_current` so both `cancel_current()` and `_tracked_popen_runner`'s new interrupt handler share it.
- `OllamaProvider.cancel_current()` keeps its exact current external behavior (tested by the three existing `test_cancel_current_*` tests — do not change their assertions).

- [ ] **Step 1: Write the failing test**

Add to `tests/test_providers/test_ollama.py` (near `test_cancel_current_terminates_running_proc`):

```python
def test_tracked_popen_runner_terminates_proc_on_keyboard_interrupt():
    """A real Ctrl+C during `ollama pull` raises KeyboardInterrupt from
    inside proc.wait(); the runner must terminate the child immediately
    right there, instead of relying on a later cancel_current() call —
    which races against _current_proc already being cleared to None by
    this same function's finally block during the same unwind, before
    main.py's except KeyboardInterrupt handler (which calls cancel())
    ever runs. Regression for a review finding: cancel_current() was a
    guaranteed no-op on the real interrupt path."""
    from unittest.mock import MagicMock, patch

    from modelman.providers.ollama import OllamaProvider, _tracked_popen_runner

    fake_proc = MagicMock()
    fake_proc.wait.side_effect = KeyboardInterrupt()
    fake_proc.poll.return_value = None
    fake_proc.stdout = None
    fake_proc.terminate = MagicMock()

    p = OllamaProvider({})
    with patch("modelman.providers.ollama.subprocess.Popen", return_value=fake_proc):
        with pytest.raises(KeyboardInterrupt):
            _tracked_popen_runner(p, ["ollama", "pull", "x"])

    fake_proc.terminate.assert_called_once()
    assert p._current_proc is None
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `uv run pytest tests/test_providers/test_ollama.py -k terminates_proc_on_keyboard_interrupt -v`
Expected: FAIL — `fake_proc.terminate` is never called today; the `finally` only clears `_current_proc`, it doesn't terminate anything.

- [ ] **Step 3: Extract `_terminate_with_escalation` and use it in both places**

In `src/modelman/providers/ollama.py`, add this module-level function (after `_strip_ansi`, before `_tracked_popen_runner`):

```python
def _terminate_with_escalation(proc: subprocess.Popen) -> None:
    """Send SIGTERM, escalating to SIGKILL on a daemon watchdog thread if
    the process is still alive after ~1s. No-op if already exited.
    Shared by cancel_current() (external cancellation) and
    _tracked_popen_runner's own interrupt handling (a real Ctrl+C
    raising KeyboardInterrupt inside proc.wait(), which must terminate
    the child immediately rather than rely on a cancel_current() call
    arriving after this process's finally has already cleared the
    tracked reference).
    """
    if proc.poll() is not None:
        return
    with contextlib.suppress(Exception):
        proc.terminate()

    def _watchdog() -> None:
        import time as _time

        for _ in range(10):  # up to ~1s in 100ms ticks
            _time.sleep(0.1)
            if proc.poll() is not None:
                return
        with contextlib.suppress(Exception):
            proc.kill()

    threading.Thread(target=_watchdog, daemon=True).start()
```

Change `_tracked_popen_runner`'s wait block from:

```python
    try:
        proc.wait()
    finally:
        if reader_thread is not None:
            reader_thread.join(timeout=1.0)
        provider._current_proc = None
    return subprocess.CompletedProcess(args, proc.returncode, "", "")
```

to:

```python
    try:
        proc.wait()
    except BaseException:
        _terminate_with_escalation(proc)
        raise
    finally:
        if reader_thread is not None:
            reader_thread.join(timeout=1.0)
        provider._current_proc = None
    return subprocess.CompletedProcess(args, proc.returncode, "", "")
```

Change `OllamaProvider.cancel_current` from:

```python
    def cancel_current(self) -> None:
        """Terminate the active subprocess, escalating to SIGKILL if needed.

        Called by the apply-queue cancellation flow when the user
        presses Cancel mid-download. Safe to call from any thread.
        Spawns a watchdog thread that escalates to `kill()` if the
        proc hasn't exited within ~1s of SIGTERM. The watchdog is a
        daemon so it never blocks process exit.
        """
        proc = self._current_proc
        if proc is None or proc.poll() is not None:
            return
        with contextlib.suppress(Exception):
            proc.terminate()

        # Watchdog: if SIGTERM doesn't take effect within a short window,
        # escalate to SIGKILL so the user actually sees the download stop.
        def _watchdog() -> None:
            import time as _time

            for _ in range(10):  # up to ~1s in 100ms ticks
                _time.sleep(0.1)
                if proc.poll() is not None:
                    return
            with contextlib.suppress(Exception):
                proc.kill()

        threading.Thread(target=_watchdog, daemon=True).start()
```

to:

```python
    def cancel_current(self) -> None:
        """Terminate the active subprocess, escalating to SIGKILL if needed.

        Called by the apply-queue cancellation flow. Safe to call from
        any thread. In practice this is now mostly a no-op safety net on
        the real Ctrl+C path — _tracked_popen_runner terminates the
        child itself the moment KeyboardInterrupt lands inside
        proc.wait(), before this method could ever see a live proc.
        """
        proc = self._current_proc
        if proc is None:
            return
        _terminate_with_escalation(proc)
```

- [ ] **Step 4: Run the new test and the three existing `cancel_current` tests**

Run: `uv run pytest tests/test_providers/test_ollama.py -k "terminates_proc_on_keyboard_interrupt or cancel_current" -v`
Expected: all PASS (the three pre-existing tests confirm `cancel_current`'s external behavior — terminate, escalate-to-kill, no-op-if-finished — is unchanged).

- [ ] **Step 5: Run the full ollama provider test file**

Run: `uv run pytest tests/test_providers/test_ollama.py -q`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add src/modelman/providers/ollama.py tests/test_providers/test_ollama.py
git commit -m "$(cat <<'EOF'
fix(modelman): terminate ollama subprocess immediately on real Ctrl+C

cancel_current() was a guaranteed no-op on the real interrupt path:
_tracked_popen_runner's finally clears provider._current_proc to None
during the same unwind that a KeyboardInterrupt from proc.wait() starts,
which completes before main.py's except KeyboardInterrupt handler (the
only caller of cancel()) ever runs — by then apply() has already fully
propagated the exception past that frame. Fixed by having
_tracked_popen_runner terminate the child itself the moment the
interrupt lands, via a _terminate_with_escalation helper now shared with
cancel_current() (kept as an external-cancellation safety net).

completes plan item #5 (review finding: cancel_current() never actually kills the child process)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

### Task 6: Route missing-ready failures through the live event channel

**Finding #5.** An unknown queued-ready model id (`KeyError` from `registry.model(mid)`) is appended straight into `pending.failures` in `main.py::run_queued_ops`, bypassing the `on_event`/`print_event` live-output mechanism every other queued-op failure type (deletes/moves/exposes, inside `queue.py`'s `apply()`) already uses — e.g. the moves loop's identical missing-id case both appends to failures *and* emits `move:fail` for live printing.

**Files:**
- Modify: `src/modelman/main.py` (`run_queued_ops`, `print_event`)
- Test: `tests/commands/test_run_tui.py`

**Interfaces:**
- `print_event` gains one new branch for the `ready:fail` tag (format matches the existing `delete:fail`/`move:fail` branches: `"  FAILED: {parts[2]}: {parts[3]}"` style — see Step 3).

- [ ] **Step 1: Write the failing test**

Add to `tests/commands/test_run_tui.py` (near `test_run_queued_ops_missing_ready_model_id_does_not_crash_whole_run`):

```python
def test_run_queued_ops_missing_ready_model_id_prints_live_failure(tmp_path, monkeypatch, capsys):
    # A missing ready id must surface through the same live on_event
    # channel as every other queued-op failure type — deletes/moves/
    # exposes already do via queue.py's own emit() calls (see the moves
    # loop's identical KeyError case). Regression: the ready lookup
    # happens before PendingChanges even exists, so it was appending
    # straight to pending.failures with no live print, unlike every
    # other op type.
    reg_path, state_path = _seed(tmp_path, monkeypatch)
    failed = run_queued_ops(QueuedOps(ready={"ollama/missing": True}))

    assert failed is True
    out = capsys.readouterr().out
    assert "FAILED: ready ollama/missing: Unknown model" in out
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `uv run pytest tests/commands/test_run_tui.py -k missing_ready_model_id_prints_live_failure -v`
Expected: FAIL — nothing containing "FAILED: ready" is printed live today (only the end-of-run summary line, in a different format, appears).

- [ ] **Step 3: Add the `ready:fail` branch to `print_event` and emit it from `run_queued_ops`**

In `src/modelman/main.py`, add a branch to `print_event` right after the existing `ready:done` branch:

```python
    elif verb == "ready:done":
        typer.echo(f"  done: {parts[2]} ready")
    elif verb == "ready:fail":
        typer.echo(f"  FAILED: ready {parts[2]}: {parts[3]}")
```

Then in `run_queued_ops`, move the `on_event`/`completed`/`done_verbs` block up so it exists before the missing-ready handling, and replace the current `pending.failures.extend(...)` line. Current code:

```python
    pending = PendingChanges(
        registry=registry,
        state=state,
        registry_path=_default_registry_path(),
        state_path=_default_state_path(),
        providers=provider_instances,
        ready=[
            (mid, ready_specs[mid], target)
            for mid, target in queued.ready.items()
            if mid in ready_specs
        ],
        deletes=list(queued.deletes.items()),
        moves=list(queued.moves.items()),
        exposes=list(queued.exposes.items()),
        litellm_path=default_litellm_config_path(),
    )
    # A model id queued while the TUI was open but missing from a
    # freshly-loaded registry (deleted out-of-band, or a hand-edited
    # registry.toml) must not take down the whole run — every other op
    # (deletes/moves/exposes) already degrades to a per-item failure on
    # a missing id; ready needs the same treatment, recorded here since
    # its lookup happens before PendingChanges even exists.
    pending.failures.extend(f"ready {mid}: Unknown model: {mid}" for mid in missing_ready)
    total = (
        len(pending.ready)
        + len(missing_ready)
        + len(pending.deletes)
        + len(pending.moves)
        + len(pending.exposes)
    )
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
```

Replace with:

```python
    pending = PendingChanges(
        registry=registry,
        state=state,
        registry_path=_default_registry_path(),
        state_path=_default_state_path(),
        providers=provider_instances,
        ready=[
            (mid, ready_specs[mid], target)
            for mid, target in queued.ready.items()
            if mid in ready_specs
        ],
        deletes=list(queued.deletes.items()),
        moves=list(queued.moves.items()),
        exposes=list(queued.exposes.items()),
        litellm_path=default_litellm_config_path(),
    )
    total = (
        len(pending.ready)
        + len(missing_ready)
        + len(pending.deletes)
        + len(pending.moves)
        + len(pending.exposes)
    )
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

    try:
        pending.apply(on_event=on_event, on_progress=typer.echo)
```

- [ ] **Step 4: Run the new test and the existing missing-ready test**

Run: `uv run pytest tests/commands/test_run_tui.py -k "missing_ready" -v`
Expected: both PASS (the pre-existing `test_run_queued_ops_missing_ready_model_id_does_not_crash_whole_run` still finds its exact substring, now inside the new live line too).

- [ ] **Step 5: Run the full command test file**

Run: `uv run pytest tests/commands/test_run_tui.py -q`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add src/modelman/main.py tests/commands/test_run_tui.py
git commit -m "$(cat <<'EOF'
fix(modelman): print missing-ready failures live, matching every other op type

An unknown queued-ready model id was recorded straight into
pending.failures, bypassing the on_event/print_event live-output channel
every other queued-op failure type already uses (the moves loop's
identical KeyError case both records the failure and emits move:fail).
Added a ready:fail branch to print_event and emit it alongside the
recorded failure.

completes plan item #6 (review finding: ready-lookup KeyError skips live failure emit)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

### Task 7: Fix the reintroduced O(R×N) registry lookup in `run_queued_ops`

**Finding #8.** `run_queued_ops` calls `registry.model(mid)` — an O(N) linear scan over `Registry.models` (`src/modelman/registry.py:188-192`) — once per queued-ready id, i.e. O(R×N) instead of O(N), reintroducing an anti-pattern the deleted single-pass `ModelScreen._run_apply` avoided.

**Files:**
- Modify: `src/modelman/main.py` (`run_queued_ops`)
- No new test — this is a pure non-behavioral refactor; existing tests (Task 6's included) already cover both the found and missing-id paths and will catch any regression.

**Interfaces:**
- No signature changes.

- [ ] **Step 1: Implement the single-pass lookup**

In `src/modelman/main.py`, replace:

```python
    registry = load_registry()
    state = load_state()
    ready_specs = {}
    missing_ready = []
    for mid in queued.ready:
        try:
            ready_specs[mid] = model_entry_to_variant(registry.model(mid))
        except KeyError:
            missing_ready.append(mid)
```

with:

```python
    registry = load_registry()
    state = load_state()
    models_by_id = {m.id: m for m in registry.models}
    ready_specs = {}
    missing_ready = []
    for mid in queued.ready:
        entry = models_by_id.get(mid)
        if entry is None:
            missing_ready.append(mid)
        else:
            ready_specs[mid] = model_entry_to_variant(entry)
```

- [ ] **Step 2: Run the tests that exercise both the found and missing-id paths**

Run: `uv run pytest tests/commands/test_run_tui.py -q`
Expected: all PASS — `test_run_queued_ops_downloads_a_queued_ready_on`, `test_run_queued_ops_missing_ready_model_id_does_not_crash_whole_run`, and both Task 6 tests all cover this exact code path.

- [ ] **Step 3: Commit**

```bash
git add src/modelman/main.py
git commit -m "$(cat <<'EOF'
perf(modelman): restore single-pass registry lookup in run_queued_ops

registry.model(mid) is an O(N) linear scan; calling it once per queued-
ready id made this O(R×N) instead of O(N), reintroducing an anti-pattern
the deleted single-pass ModelScreen._run_apply avoided. Build one
id->entry dict up front instead.

completes plan item #7 (review finding: reintroduced O(R×N) registry lookup in ready loop)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

### Task 8: Add a catch-all branch to `print_event`

**Finding #9.** `print_event` is an if/elif chain matching exact event-tag strings from `queue.py`'s `emit()` calls, with no trailing `else`. A future or misspelled event verb would silently produce no CLI output at all. Three tags (`apply:done`, `apply:cancelled`, `save:start`) are *intentionally* silent per the function's own docstring — the catch-all must not flag those.

**Files:**
- Modify: `src/modelman/main.py` (`print_event`)
- Test: `tests/commands/test_run_tui.py`

**Interfaces:**
- No signature changes. Unrecognized tags now print a line to stderr instead of nothing.

- [ ] **Step 1: Write the failing tests**

Add to `tests/commands/test_run_tui.py` (near `test_print_event_formats_download_lifecycle`):

```python
def test_print_event_warns_on_unrecognized_tag(capsys):
    # A future or misspelled event verb must not be silently dropped —
    # regression for a review finding where the if/elif chain had no
    # trailing else, so a new lifecycle tag would print nothing at all
    # and no test or runtime check would catch the gap.
    print_event("mystery:verb|x|y")
    err = capsys.readouterr().err
    assert "mystery:verb" in err


def test_print_event_stays_silent_for_known_no_op_tags(capsys):
    # apply:done / apply:cancelled / save:start are intentionally silent
    # (run_queued_ops prints its own summary for these) and must not be
    # flagged as unrecognized by the new catch-all branch.
    print_event("apply:done")
    print_event("apply:cancelled")
    print_event("save:start")
    out = capsys.readouterr()
    assert out.out == ""
    assert out.err == ""
```

- [ ] **Step 2: Run them to confirm the first one fails**

Run: `uv run pytest tests/commands/test_run_tui.py -k "warns_on_unrecognized_tag or stays_silent_for_known_no_op_tags" -v`
Expected: `test_print_event_warns_on_unrecognized_tag` FAILs (nothing printed anywhere today); `test_print_event_stays_silent_for_known_no_op_tags` already PASSes.

- [ ] **Step 3: Add the catch-all**

In `src/modelman/main.py`, `print_event`'s current tail:

```python
    elif verb == "save:done":
        typer.echo("Saved.")
    elif verb == "save:fail":
        typer.echo(f"  FAILED: save: {parts[1]}")
    # apply:done / apply:cancelled / save:start: no line — run_queued_ops
    # prints its own summary once apply() returns.
```

becomes:

```python
    elif verb == "save:done":
        typer.echo("Saved.")
    elif verb == "save:fail":
        typer.echo(f"  FAILED: save: {parts[1]}")
    elif verb in ("apply:done", "apply:cancelled", "save:start"):
        pass  # run_queued_ops prints its own summary once apply() returns.
    else:
        typer.echo(f"  (unhandled event: {tag})", err=True)
```

- [ ] **Step 4: Run the two new tests**

Run: `uv run pytest tests/commands/test_run_tui.py -k "warns_on_unrecognized_tag or stays_silent_for_known_no_op_tags" -v`
Expected: both PASS.

- [ ] **Step 5: Run the full command test file**

Run: `uv run pytest tests/commands/test_run_tui.py -q`
Expected: all PASS — confirms every tag `queue.py` actually emits is still handled by an explicit branch above the new `else`.

- [ ] **Step 6: Commit**

```bash
git add src/modelman/main.py tests/commands/test_run_tui.py
git commit -m "$(cat <<'EOF'
fix(modelman): add catch-all branch to print_event for unrecognized tags

The if/elif chain had no trailing else, so a future or misspelled event
verb from queue.py's emit() calls would silently produce no CLI output
and nothing would catch the gap. Added an explicit else that prints to
stderr, with apply:done/apply:cancelled/save:start carved out as the
already-documented intentionally-silent tags.

completes plan item #8 (review finding: print_event has no catch-all for unknown events)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

### Task 9: Clean up stale comments referencing deleted machinery

**Finding #10.** Comments/docstrings in several files still describe machinery this branch deleted (`StatusScreen`, `_apply_queued`, a download quit guard, `DownloadManager` as a live concurrent writer), risking a future maintainer reintroducing an invalidated assumption. Pure comment changes — no behavior change, no new tests; verified via `make lint`/`make typecheck` (comment-only diffs can't break tests, but a stray syntax slip could break linting).

**Files:**
- Modify: `src/modelman/queue.py` (module docstring/comment block, one dataclass-field comment, one comment in `apply()`)
- Modify: `src/modelman/app.py` (`action_quit` docstring)
- Modify: `src/modelman/screens/models.py` (two comment blocks, one docstring)

- [ ] **Step 1: `queue.py` module-level event-tag comment (lines 34-41)**

Replace:

```python
# Event tags fired via the optional on_event callback during apply(). The
# StatusScreen consumes these to render live progress. Format is unchanged
# from the legacy FamilyManifest-based implementation so StatusScreen can
# keep consuming pipe-delimited tags without modification:
```

with:

```python
# Event tags fired via the optional on_event callback during apply().
# main.py's print_event() renders these as plain terminal lines now that
# apply() runs after the TUI has exited (StatusScreen, the old in-TUI
# renderer, is gone). Format is unchanged from the legacy
# FamilyManifest-based implementation:
```

(leave the rest of that comment block — the tag-format bullet list — as-is; only the first two lines describing the consumer are stale).

- [ ] **Step 2: `queue.py`'s other `StatusScreen` mention (in `_sanitize`'s docstring, ~line 49)**

Replace:

```python
    Tags are pipe-delimited ("verb:status|field|field"); a literal '|'
    in a field would shift the split in StatusScreen._handle_event and
    corrupt the fields after it.
```

with:

```python
    Tags are pipe-delimited ("verb:status|field|field"); a literal '|'
    in a field would shift the split in main.py's print_event() and
    corrupt the fields after it.
```

- [ ] **Step 3: `queue.py`'s stale "concurrent DownloadManager write" comment (~line 182)**

Replace:

```python
    # Populated during apply(); used only by the final save to merge this
    # run's changes onto a freshly-loaded on-disk StateStore instead of
    # overwriting the whole file from this object's (possibly stale
    # relative to a concurrent DownloadManager write) in-memory snapshot.
```

with:

```python
    # Populated during apply(); used only by the final save to merge this
    # run's changes onto a freshly-loaded on-disk StateStore instead of
    # overwriting the whole file from this object's in-memory snapshot,
    # which may be stale relative to modelman.toml if another modelman
    # process wrote to it concurrently.
```

- [ ] **Step 4: `queue.py`'s empty-queue fast-path comment (lines 307-309)**

Replace:

```python
        if not self.ready and not self.deletes and not self.exposes and not self.moves:
            # Empty-queue fast path. Today's only call site is the TUI's
            # `_apply_queued`, which always has at least one queued item
            # (the TUI only opens the apply confirm dialog with a non-empty
            # queue). A future programmatic caller would see "All operations
            # completed successfully" on the status screen — fine for the
            # current contract, but if you reach this branch from elsewhere
            # consider whether the user is misled by zero attempted work.
            emit("apply:done")
            return
```

with:

```python
        if not self.ready and not self.deletes and not self.exposes and not self.moves:
            # Empty-queue fast path. Today's only call site is main.py's
            # run_queued_ops, which always has at least one queued item
            # (the TUI only returns a QueuedOps via Apply when the queue is
            # non-empty). A future programmatic caller would see nothing
            # printed beyond "apply:done" — fine for the current contract,
            # but if you reach this branch from elsewhere consider whether
            # the user is misled by zero attempted work.
            emit("apply:done")
            return
```

- [ ] **Step 5: `app.py`'s `action_quit` docstring**

Replace:

```python
    async def action_quit(self) -> None:
        """Override Textual's default (self.exit()) to route through the
        download quit guard. This also fixes a pre-existing quirk where
        ctrl+q quit immediately from any screen, bypassing ModelScreen's
        apply-on-exit confirm — it's now gated by request_quit()."""
        self.request_quit()
```

with:

```python
    async def action_quit(self) -> None:
        """Override Textual's default (self.exit()) to route through
        request_quit(), so ctrl+q shows ModelScreen's apply/discard/cancel
        confirm dialog when a queue is pending, instead of quitting
        immediately from any screen (a pre-existing quirk this fixes)."""
        self.request_quit()
```

- [ ] **Step 6: `screens/models.py`'s snapshot-retake comment (~lines 261-266) and `_retake_snapshot` docstring (~lines 828-836)**

Replace:

```python
        # Retaken after every apply run (see _on_status_screen_dismissed):
        # this screen is the app's single long-lived root now (never
        # recreated per family), so without retaking it a Discard many
        # applies later would roll all the way back to the state at app
        # launch — silently resurrecting earlier applies that already
        # succeeded and were saved to disk.
```

with:

```python
        # Taken once, here, at construction. This screen is the app's
        # single long-lived root and exits (with an Apply/Discard/None
        # QueuedOps) rather than resuming — apply() itself only ever runs
        # after the TUI process has already exited (see main.py's
        # run_queued_ops) — so there is no later point in this screen's
        # life where the baseline needs retaking.
```

Replace:

```python
    def _retake_snapshot(self) -> None:
        """(Re)capture the discard baseline from the current registry/state.

        Called from __init__ (the initial baseline) and again whenever
        control returns from a StatusScreen apply run (see
        _on_status_screen_dismissed), so a later Discard only undoes
        changes queued since that point — never an earlier apply that
        already succeeded and was saved to disk.
        """
```

with:

```python
    def _retake_snapshot(self) -> None:
        """Capture the discard baseline from the current registry/state.

        Called once, from __init__. There is no later retake: this
        screen exits (via Apply/Discard) rather than resuming, and
        apply() itself only ever runs after the TUI process has exited
        (see main.py's run_queued_ops).
        """
```

- [ ] **Step 7: `screens/models.py`'s expose-cascade comment (~lines 540-547)**

Replace:

```python
            # Exposing requires ready — the same gate _validated_entry
            # applies at apply time. If the user has a ready toggle queued
            # that leaves the model not-ready, refuse rather than overwrite
            # their request; otherwise cascade the download in: a mapped
            # provider's ready-on starts the real download immediately
            # (DownloadManager), a flag-only provider's is queued (apply
            # runs the ready loop before the expose loop, so the order
            # works).
```

with:

```python
            # Exposing requires ready — the same gate _validated_entry
            # applies at apply time. If the user has a ready toggle queued
            # that leaves the model not-ready, refuse rather than overwrite
            # their request; otherwise cascade a ready=True queue in for
            # either a mapped or flag-only provider — nothing runs until
            # Apply, at which point apply() runs the ready loop before the
            # expose loop, so the order works.
```

- [ ] **Step 8: Verify nothing else references the deleted names**

Run: `grep -rn "StatusScreen\|_apply_queued\|_on_status_screen_dismissed\|download quit guard" src/modelman/`
Expected: no output (all instances cleaned up).

- [ ] **Step 9: Run lint/typecheck to confirm the comment edits didn't break anything**

Run: `uv run ruff check src/modelman/queue.py src/modelman/app.py src/modelman/screens/models.py && uv run mypy src/modelman/queue.py src/modelman/app.py src/modelman/screens/models.py` (or `make check` if that's simpler)
Expected: clean.

- [ ] **Step 10: Run the screen and queue test files as a sanity check**

Run: `uv run pytest tests/screens/test_app_navigation.py tests/test_queue.py -q`
Expected: all PASS (comment-only changes, but confirms nothing was accidentally altered).

- [ ] **Step 11: Commit**

```bash
git add src/modelman/queue.py src/modelman/app.py src/modelman/screens/models.py
git commit -m "$(cat <<'EOF'
docs(modelman): fix comments/docstrings referencing deleted download machinery

Several comments still described StatusScreen, _apply_queued,
_on_status_screen_dismissed, and a "download quit guard" — all deleted
by the downloads-on-exit redesign — or described DownloadManager as a
still-live concurrent writer. Updated each to describe the current
apply-after-exit design so a future maintainer doesn't reason from an
invalidated assumption.

completes plan item #9 (review finding: stale comments reference deleted machinery)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FEwpdxXynoBmU3sFafj1Lu
EOF
)"
```

---

### Task 10: Final verification

**Files:** none — verification only.

- [ ] **Step 1: Run the full modelman test suite**

Run: `cd modelman && make test` (or `uv run pytest -q` from `modelman/`)
Expected: all ~1113+ tests PASS (the exact count drifts — re-measure rather than trusting the number in `modelman/CLAUDE.md`).

- [ ] **Step 2: Run lint + typecheck**

Run: `cd modelman && make check`
Expected: clean (no Ruff findings, no mypy errors).

- [ ] **Step 3: Run the monorepo-root umbrella check** (covers shell lint + link check; wt is untouched by this plan but costs nothing to include)

Run: `make test-all` (from the repo root)
Expected: clean.

- [ ] **Step 4: Re-verify every original finding is actually addressed**

Go through the 10 findings list one more time (from the `/code-review high` report) and confirm each has a corresponding commit from Tasks 1-9. This is a self-check, not a new test — no code changes expected here.

- [ ] **Step 5: Report to the user**

Summarize: all 10 findings fixed across 9 commits, full suite passing, plus the one deliberate scope note (Task 3 narrowed finding #1 to not fight the documented cancellation-semantics spec). Ask whether to push the branch / open a PR — do NOT do either without explicit approval (per the user's global PR-discipline preference).
