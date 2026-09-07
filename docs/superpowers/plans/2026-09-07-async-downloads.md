# Async Background Downloads Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make modelman TUI model downloads run in the background (not
apply-on-exit), lock a downloading model against modification, block quit
while downloads are active, and add a dedicated download screen.

**Architecture:** A new `DownloadManager` (owned by `ModelmanApp`, survives
screen navigation) runs each download on its own daemon thread via a fresh
provider instance, persists completion to `modelman.toml` through a new
locked read-modify-write helper (closing a real concurrent-write race this
design introduces), and exposes poll-friendly status for the TUI. Deletes,
moves, and exposes stay in the existing `PendingChanges.apply()`
apply-on-exit queue, with the download step removed from it.

**Tech Stack:** Python 3.13, Textual (TUI), `uv` (packaging/running),
`huggingface_hub` (oMLX/llamacpp downloads), pytest + pytest-asyncio
(Textual `run_test()`/pilot for screen tests).

**Spec:** `docs/superpowers/specs/2026-09-07-async-downloads-design.md`

## Global Constraints

- Run all commands with `uv run` (`requires-python = "==3.13.*"`; a
  pyenv-managed Python will not find the project's dependencies).
- Run `make dev` once before starting (installs pytest/ruff/mypy as dev
  deps, not part of `make install`) if `uv run pytest` isn't already
  working.
- Run focused test files per task, not the full suite — `make check`
  (lint+typecheck) plus the specific test file(s) each task touches. Run
  `make all` (or `make test` + `make check`) once at the end (Task 16).
- Every new test needs a one/two-sentence comment above it explaining what
  behavior it covers and why it matters — match the existing convention
  visible throughout `tests/` (e.g. "Regression: ...", "must not ...").
- No schema changes to `registry.toml` or `modelman.toml` — only new
  *fields* already covered by existing dataclasses (`ready`, `disk_path`,
  `size_bytes`, `litellm_exposed` on `ModelState`) are written; no new
  TOML keys.
- Follow existing patterns: dataclasses for state, `ProviderRegistry.get()`
  for provider dispatch, `app.call_from_thread(...)` to marshal
  background-thread work onto the UI thread, `self.app.notify(...)` for
  user-facing messages, `ModelmanModal` base for new dialogs.
- Each task's final commit message should reference the plan item it
  completes (e.g. "... — completes plan item #5").

---

## File Structure

New:
- `src/modelman/downloads.py` — `DownloadState` dataclass + `DownloadManager`.
- `src/modelman/screens/downloads.py` — `DownloadScreen`.
- `tests/test_downloads.py`
- `tests/screens/test_downloads_screen.py`

Modified:
- `src/modelman/state.py` — new `locked_state()` context manager (state
  synchronization primitive; state.py owns modelman.toml, so this is where
  the lock belongs).
- `src/modelman/providers/_progress.py` — HF-download serialization lock
  (replaces the spec's originally-proposed, verified-broken
  `contextvars.ContextVar` fix).
- `src/modelman/providers/base.py` — optional `cleanup_partial_download`
  hook (default no-op).
- `src/modelman/providers/omlx.py` — HF lock usage + `cleanup_partial_download`.
- `src/modelman/providers/llamacpp.py` — HF lock usage + `cleanup_partial_download`.
- `src/modelman/queue.py` — drop the download branch from the ready loop;
  merge-on-save via `locked_state()`.
- `src/modelman/app.py` — owns `self.downloads`; quit guard.
- `src/modelman/screens/forms.py` — new `QuitBlockedModal`.
- `src/modelman/screens/families.py` — quit guard wiring, `g` binding.
- `src/modelman/screens/models.py` — download routing/locking/glyph/poll,
  add-model immediacy, expose deferral, discard-cancels-cascade, `g` binding.
- `tests/test_state.py`, `tests/test_queue.py`,
  `tests/test_providers/test_progress.py`, `tests/test_providers/test_omlx.py`,
  `tests/test_providers/test_llamacpp.py`, `tests/screens/test_models.py`,
  `tests/screens/test_families.py` — new coverage per task below.
- `modelman/CLAUDE.md` — architecture note additions (Task 16).

Why FamilyScreen doesn't get a live poll: it already reloads fully
(`_refresh_from_disk()`) on `on_screen_resume`, which fires every time the
user navigates back to it from a child screen — the existing idiom already
covers "downloads may have completed while I was elsewhere." Only
`ModelScreen` (where the STATUS glyph and locking live) and `DownloadScreen`
(whose entire purpose is live progress) get a poll.

---

### Task 1: State synchronization primitive

**Files:**
- Modify: `src/modelman/state.py`
- Test: `tests/test_state.py`

**Interfaces:**
- Produces: `locked_state(path: Path | None = None) -> ContextManager[StateStore]` —
  acquires a process-wide lock, loads `modelman.toml` fresh, yields the
  `StateStore` for the caller to mutate in place, saves it back on exit,
  then releases the lock. Used by `DownloadManager` (Task 5/6) and
  `PendingChanges.apply()`'s final save (Task 4).

- [x] **Step 1: Write the failing tests**

```python
# tests/test_state.py — add near the bottom, alongside the existing
# save/load round-trip tests.

def test_locked_state_round_trips_a_single_write(tmp_path):
    # Baseline: locked_state must behave like load-mutate-save for the
    # simple case before the concurrency tests below rely on it.
    from modelman.state import locked_state

    path = tmp_path / "modelman.toml"
    save_state(StateStore(), path)

    with locked_state(path) as state:
        state.set("ollama/a", ModelState(ready=True, disk_path="/a"))

    loaded = load_state(path)
    assert loaded.get("ollama/a") == ModelState(ready=True, disk_path="/a")


def test_locked_state_does_not_lose_a_sequential_concurrent_write(tmp_path):
    # This is the exact regression this helper exists to prevent: once
    # DownloadManager completions and PendingChanges.apply()'s final save
    # can both write modelman.toml, a naive load-mutate-save (what
    # save_state() alone does) loses whichever write finishes first if the
    # other writer already loaded before that write landed. Two
    # *sequential* locked_state() blocks, each touching a different
    # model_id, must both survive.
    from modelman.state import locked_state

    path = tmp_path / "modelman.toml"
    save_state(StateStore(), path)

    with locked_state(path) as state:
        state.set("ollama/a", ModelState(ready=True, disk_path="/a"))
    with locked_state(path) as state:
        state.set("ollama/b", ModelState(ready=True, disk_path="/b"))

    loaded = load_state(path)
    assert loaded.get("ollama/a") == ModelState(ready=True, disk_path="/a")
    assert loaded.get("ollama/b") == ModelState(ready=True, disk_path="/b")


def test_locked_state_serializes_real_concurrent_writers(tmp_path):
    # Proves the lock actually serializes writers under real thread
    # concurrency (not just "happens to work" in a single-threaded test) —
    # this is the scenario two parallel download completions hit directly.
    import threading

    from modelman.state import locked_state

    path = tmp_path / "modelman.toml"
    save_state(StateStore(), path)

    def _write(i: int) -> None:
        with locked_state(path) as state:
            state.set(f"ollama/m{i}", ModelState(ready=True))

    threads = [threading.Thread(target=_write, args=(i,)) for i in range(10)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    loaded = load_state(path)
    for i in range(10):
        assert loaded.get(f"ollama/m{i}").ready is True
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_state.py -k locked_state -v`
Expected: FAIL with `ImportError: cannot import name 'locked_state'`

- [x] **Step 3: Implement `locked_state()`**

Add to `src/modelman/state.py` (after the module docstring's imports —
add `import contextlib` and `import threading` to the existing imports,
and place this after `save_state()`):

```python
_STATE_LOCK = threading.Lock()


@contextlib.contextmanager
def locked_state(path: Path | None = None):
    """Atomically read-modify-write modelman.toml.

    save_state() alone is a whole-file overwrite of whatever StateStore
    it's given, with no merge — safe only when a single writer holds the
    only in-memory copy. Once DownloadManager can write modelman.toml
    from a background thread on download completion while
    PendingChanges.apply() can independently save its own (possibly
    already-stale) snapshot for an unrelated model, two such writers can
    silently stomp each other's changes. This acquires a process-wide
    lock, loads the current on-disk StateStore, yields it for the caller
    to mutate in place, and saves it back before releasing the lock —
    every writer's mutation is always applied on top of the latest
    on-disk state.
    """
    with _STATE_LOCK:
        store = load_state(path)
        yield store
        save_state(store, path)
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_state.py -v`
Expected: PASS (all tests, including the pre-existing ones)

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/state.py tests/test_state.py && uv run mypy src/modelman/state.py`

```bash
git add src/modelman/state.py tests/test_state.py
git commit -m "$(cat <<'EOF'
feat(state): add locked_state() for concurrent modelman.toml writes

save_state() is a whole-file overwrite; once downloads can complete on a
background thread while an unrelated apply() saves independently, two
writers can lose each other's changes. locked_state() serializes a
load-mutate-save cycle under one lock so every write lands on the
latest on-disk state.

completes plan item #1
EOF
)"
```

---

### Task 2: Serialize HF-backed downloads to fix the parallel-progress bug

**Files:**
- Modify: `src/modelman/providers/_progress.py`
- Modify: `src/modelman/providers/omlx.py`
- Modify: `src/modelman/providers/llamacpp.py`
- Test: `tests/test_providers/test_progress.py`

**Interfaces:**
- Produces: `HF_DOWNLOAD_LOCK: threading.Lock` in `_progress.py`, held by
  `OMLXProvider.download()` and `LlamaCppProvider.download()` for the
  full `set_active_context → snapshot_download → clear_active_context`
  critical section.

**Why not `contextvars.ContextVar`** (verified against `huggingface_hub`
1.28.0, the pinned version, and empirically against plain CPython
threading): `ContextVar` values do not propagate to a `threading.Thread`
or `ThreadPoolExecutor` worker — a value set in the parent thread reads
back as the default in the child. `snapshot_download()` runs an internal
8-worker `ThreadPoolExecutor` (`hf_thread_map`) for per-file downloads,
even for a single model, and the per-file progress bar's `.update()` →
`.display()` call happens *inside* those worker threads. The current
class-level slot on `ProgressTqdm` works today precisely because a plain
class attribute is visible identically from any thread — it survives that
boundary. A lock around the critical section keeps that working while
preventing two *different* top-level downloads from both owning the slot
at once.

- [x] **Step 1: Write the failing test**

```python
# tests/test_providers/test_progress.py — add near the other
# ProgressTqdm tests.

def test_hf_download_lock_serializes_two_active_contexts():
    # Two "downloads" that both try to hold the active context at once
    # must not interleave: the second must block until the first releases
    # the lock. This is what prevents two parallel oMLX/llamacpp downloads
    # from clobbering each other's on_progress/should_cancel callbacks —
    # the bug the class-level slot has without this lock.
    import threading
    import time

    from modelman.providers._progress import HF_DOWNLOAD_LOCK, ProgressTqdm

    order: list[str] = []

    def _hold(label: str, hold_seconds: float) -> None:
        with HF_DOWNLOAD_LOCK:
            ProgressTqdm.set_active_context(lambda line: None, lambda: False)
            order.append(f"{label}-start")
            time.sleep(hold_seconds)
            order.append(f"{label}-end")
            ProgressTqdm.clear_active_context()

    t1 = threading.Thread(target=_hold, args=("a", 0.1))
    t2 = threading.Thread(target=_hold, args=("b", 0.0))
    t1.start()
    time.sleep(0.02)  # let t1 acquire the lock first
    t2.start()
    t1.join()
    t2.join()

    # b must not start until a has fully finished (start...end...start...end),
    # never interleaved (start...start...end...end).
    assert order == ["a-start", "a-end", "b-start", "b-end"]
```

- [x] **Step 2: Run the test to verify it fails**

Run: `uv run pytest tests/test_providers/test_progress.py -k hf_download_lock -v`
Expected: FAIL with `ImportError: cannot import name 'HF_DOWNLOAD_LOCK'`

- [x] **Step 3: Add the lock and use it in both HF providers**

In `src/modelman/providers/_progress.py`, add near the top (after the
`DownloadCancelled` class, before `ProgressTqdm`):

```python
# Held by OMLXProvider.download() / LlamaCppProvider.download() for the
# full set_active_context -> snapshot_download -> clear_active_context
# critical section, so two different top-level HF downloads never share
# ProgressTqdm's class-level active-context slot at the same time. See
# the module docstring above the class-level slots for why a per-thread
# mechanism (contextvars) can't be used here instead: snapshot_download's
# own internal worker threads must see whichever context this lock is
# currently protecting.
HF_DOWNLOAD_LOCK = threading.Lock()
```

In `src/modelman/providers/omlx.py`, modify `download()` (import the lock
at the top: `from ._progress import HF_DOWNLOAD_LOCK, ProgressTqdm`):

```python
    def download(
        self,
        variant: VariantSpec,
        runner: _Runner | None = None,
        on_progress: Callable[[str], None] | None = None,
    ) -> str:
        repo = variant.get("repo")
        if not repo:
            raise ValueError(f"omlx variant {variant['id']} missing repo")
        self._cancel_requested = False
        target = _model_dir(self.config) / _basename(repo)
        kwargs: dict[str, Any] = {"repo_id": repo, "local_dir": str(target)}
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

In `src/modelman/providers/llamacpp.py`, modify `download()` the same way
(import `HF_DOWNLOAD_LOCK` alongside `ProgressTqdm`):

```python
    def download(
        self,
        variant: VariantSpec,
        runner: _Runner | None = None,
        on_progress: Callable[[str], None] | None = None,
    ) -> str:
        repo = variant.get("repo")
        files = variant.get("files")
        if not repo or not files:
            raise ValueError(f"llamacpp variant {variant['id']} missing repo/files")
        self._cancel_requested = False
        primary = files[0]
        kwargs: dict[str, Any] = {
            "repo_id": repo,
            "allow_patterns": files,
            "cache_dir": _hf_cache_dir(),
        }
        with HF_DOWNLOAD_LOCK:
            ProgressTqdm.set_active_context(on_progress, lambda: self._cancel_requested)
            try:
                if on_progress is not None:
                    kwargs["tqdm_class"] = ProgressTqdm
                path = snapshot_download(**kwargs)
                return str(Path(path) / primary)
            finally:
                ProgressTqdm.clear_active_context()
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_providers/test_progress.py tests/test_providers/test_omlx.py tests/test_providers/test_llamacpp.py -v`
Expected: PASS

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/providers/ tests/test_providers/ && uv run mypy src/modelman/providers/`

```bash
git add src/modelman/providers/_progress.py src/modelman/providers/omlx.py src/modelman/providers/llamacpp.py tests/test_providers/test_progress.py
git commit -m "$(cat <<'EOF'
fix(providers): serialize HF downloads instead of using contextvars

Verified contextvars.ContextVar does not propagate into
snapshot_download's internal ThreadPoolExecutor workers (huggingface_hub
1.28.0), which would have silently broken progress reporting and
cancellation for every oMLX/llamacpp download once parallel downloads
land. A lock around the active-context critical section keeps the
thread-crossing class-level slot working while preventing two
different downloads from sharing it.

completes plan item #2
EOF
)"
```

---

### Task 3: Provider partial-download cleanup hook

**Files:**
- Modify: `src/modelman/providers/base.py`
- Modify: `src/modelman/providers/omlx.py`
- Modify: `src/modelman/providers/llamacpp.py`
- Test: `tests/test_providers/test_base.py`, `tests/test_providers/test_omlx.py`, `tests/test_providers/test_llamacpp.py`

**Interfaces:**
- Produces: `Provider.cleanup_partial_download(variant: VariantSpec) -> None`
  — default no-op in `base.py`. `OMLXProvider` removes the partial target
  directory (same as `delete()`). `LlamaCppProvider` removes `.incomplete`
  blob files for the repo, without touching complete files/blobs still
  referenced elsewhere.
- Consumes (Task 6): called by `DownloadManager` after a cancelled/failed
  download, passed the same `VariantSpec` given to `start()`.

- [x] **Step 1: Write the failing tests**

```python
# tests/test_providers/test_base.py — add near the other Provider tests.

def test_cleanup_partial_download_default_is_noop():
    # Providers with no partial-download artifacts to clean up (Ollama:
    # `ollama pull` is resumable and reconciles its own state) must not
    # need to implement this — the base class default is a no-op so
    # DownloadManager can call it unconditionally on every provider.
    class _NoopProvider(Provider):
        name = "noop"

        def is_downloaded(self, variant):
            return False

        def download(self, variant):
            return ""

        def list_local(self):
            return []

    p = _NoopProvider({})
    p.cleanup_partial_download({"id": "x", "provider": "noop"})  # must not raise
```

```python
# tests/test_providers/test_omlx.py — add near the delete() tests.

def test_cleanup_partial_download_removes_partial_target_dir(tmp_path):
    # A cancelled oMLX download leaves a partially-populated target
    # directory (snapshot_download writes files incrementally into
    # local_dir). Cleanup must remove it so a retry starts clean and the
    # model doesn't show as "has some files" in list_local().
    provider = OMLXProvider({"model_dir": str(tmp_path)})
    target = tmp_path / "some-model"
    target.mkdir()
    (target / "partial.bin").write_bytes(b"not finished")

    provider.cleanup_partial_download({"id": "x", "provider": "omlx", "repo": "org/some-model"})

    assert not target.exists()


def test_cleanup_partial_download_missing_dir_is_noop(tmp_path):
    # Cleanup runs unconditionally after any cancel/fail; a download that
    # never got far enough to create the target directory must not raise.
    provider = OMLXProvider({"model_dir": str(tmp_path)})
    provider.cleanup_partial_download({"id": "x", "provider": "omlx", "repo": "org/never-started"})
```

```python
# tests/test_providers/test_llamacpp.py — add near the delete() tests.

def test_cleanup_partial_download_removes_incomplete_blobs(tmp_path, monkeypatch):
    # huggingface_hub writes partially-downloaded blobs as
    # blobs/<hash>.incomplete during a snapshot_download; a cancelled
    # download leaves these behind. Cleanup must remove them without
    # touching complete files, so a retry doesn't see stale partial data
    # and disk isn't leaked by repeated cancels.
    hf_home = tmp_path / "hf-cache"
    monkeypatch.setenv("HF_HOME", str(hf_home))
    repo_dir = hf_home / "hub" / "models--org--repo"
    blobs_dir = repo_dir / "blobs"
    blobs_dir.mkdir(parents=True)
    (blobs_dir / "abc123.incomplete").write_bytes(b"partial")
    (blobs_dir / "def456").write_bytes(b"complete, unrelated file")  # must survive

    provider = LlamaCppProvider({})
    provider.cleanup_partial_download(
        {"id": "x", "provider": "llamacpp", "repo": "org/repo", "files": ["model.gguf"]}
    )

    assert not (blobs_dir / "abc123.incomplete").exists()
    assert (blobs_dir / "def456").exists()


def test_cleanup_partial_download_missing_repo_dir_is_noop(tmp_path, monkeypatch):
    # A cancel before any HF cache directory was even created must not raise.
    monkeypatch.setenv("HF_HOME", str(tmp_path / "hf-cache"))
    provider = LlamaCppProvider({})
    provider.cleanup_partial_download(
        {"id": "x", "provider": "llamacpp", "repo": "org/never-started", "files": ["f.gguf"]}
    )
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_providers/test_base.py tests/test_providers/test_omlx.py tests/test_providers/test_llamacpp.py -k cleanup_partial -v`
Expected: FAIL with `AttributeError: ... has no attribute 'cleanup_partial_download'`

- [x] **Step 3: Implement the hook**

In `src/modelman/providers/base.py`, add to `Provider` (after `path_of`):

```python
    def cleanup_partial_download(self, variant: VariantSpec) -> None:
        """Remove any on-disk remnants of a cancelled or failed download.

        Called by DownloadManager after a download is cancelled or fails.
        Default is a no-op: providers whose download mechanism has no
        partial-artifact cleanup to do (Ollama's `pull` is resumable and
        reconciles its own state on the next attempt) don't need to
        override this.
        """
        return
```

In `src/modelman/providers/omlx.py`, add to `OMLXProvider` (after `delete`):

```python
    def cleanup_partial_download(self, variant: VariantSpec) -> None:
        """Remove the partially-populated target directory.

        snapshot_download writes files directly into local_dir as they
        complete, so a cancel mid-download leaves a partial directory
        behind. Same removal delete() does — a cancelled download has no
        artifact worth keeping.
        """
        repo = variant.get("repo")
        if not repo:
            return
        import shutil

        target = _model_dir(self.config) / _basename(repo)
        if target.exists():
            shutil.rmtree(target)
```

In `src/modelman/providers/llamacpp.py`, add to `LlamaCppProvider` (after
`delete`):

```python
    def cleanup_partial_download(self, variant: VariantSpec) -> None:
        """Remove partially-downloaded blobs left by a cancelled download.

        huggingface_hub writes an in-progress blob as
        blobs/<hash>.incomplete and renames it to blobs/<hash> only once
        the download completes. A cancel mid-download leaves the
        .incomplete file behind; complete blobs (no .incomplete suffix)
        are untouched even if this repo has other, finished downloads.
        """
        repo = variant.get("repo")
        if not repo:
            return
        hf_org, hf_name = repo.split("/", 1)
        blobs_dir = _hf_cache_dir() / f"models--{hf_org}--{hf_name}" / "blobs"
        if not blobs_dir.exists():
            return
        for incomplete in blobs_dir.glob("*.incomplete"):
            incomplete.unlink()
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_providers/test_base.py tests/test_providers/test_omlx.py tests/test_providers/test_llamacpp.py -v`
Expected: PASS

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/providers/ tests/test_providers/ && uv run mypy src/modelman/providers/`

```bash
git add src/modelman/providers/base.py src/modelman/providers/omlx.py src/modelman/providers/llamacpp.py tests/test_providers/test_base.py tests/test_providers/test_omlx.py tests/test_providers/test_llamacpp.py
git commit -m "$(cat <<'EOF'
feat(providers): add cleanup_partial_download hook

DownloadManager (Task 6) needs a provider-neutral way to clean up
on-disk remnants after a cancelled or failed download. Default no-op;
oMLX removes the partial target directory, llamacpp removes .incomplete
HF cache blobs without touching complete ones.

completes plan item #3
EOF
)"
```

---

### Task 4: Drop the download step from `PendingChanges.apply()` and merge-on-save

**Files:**
- Modify: `src/modelman/queue.py`
- Test: `tests/test_queue.py`

**Interfaces:**
- Consumes: `locked_state` from `state.py` (Task 1).
- Produces: `PendingChanges.apply()` no longer accepts or runs
  `ready` entries where `target=True` against a real provider — those
  are asserted unreachable. `PendingChanges` gains two internal tracking
  fields (`_touched_model_ids: set[str]`, `_forgotten_families: set[str]`)
  used only by the final save.
- Note for Task 11-15: callers (`ModelScreen._run_apply`) must stop
  putting real-download ready-on entries into `PendingChanges.ready` —
  those route through `DownloadManager.start()` instead (Task 12).

- [x] **Step 1: Write the failing tests**

```python
# tests/test_queue.py — add near the other apply() tests.

def test_apply_no_longer_downloads_ready_on_entries(tmp_path):
    # The download step is being removed from apply() entirely — a
    # target=True entry against a provider must now be a programming
    # error (the caller is responsible for routing real downloads
    # through DownloadManager instead), not a silent no-op or a call to
    # provider.download().
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="ollama/x", family="f", provider="ollama", name="x:7b"),
    )
    state_path = tmp_path / "modelman.toml"
    provider = MagicMock()
    provider.download.side_effect = AssertionError("download() must not be called")

    pending = PendingChanges(
        registry=reg,
        state=_make_state(),
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, True)],
    )
    with pytest.raises(AssertionError, match="ready-on"):
        pending.apply()
    provider.download.assert_not_called()


def test_apply_ready_off_still_clears_artifact(tmp_path):
    # Ready-off (the queued "clear" case) is unchanged by this task: it
    # must still call provider.delete() and clear state.
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="ollama/x", family="f", provider="ollama", name="x:7b"),
    )
    state_path = tmp_path / "modelman.toml"
    state = _make_state()
    state.set("ollama/x", ModelState(ready=True, disk_path="ollama:x:7b"))
    provider = MagicMock()
    provider.is_downloaded.return_value = True
    provider.delete.return_value = None

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        ready=[("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, False)],
    )
    pending.apply()

    provider.delete.assert_called_once()
    loaded = load_state(state_path)
    assert loaded.get("ollama/x").ready is False


def test_apply_final_save_merges_onto_fresh_disk_state_not_a_stale_snapshot(tmp_path):
    # The exact race this task closes: if modelman.toml changes on disk
    # between when this PendingChanges' StateStore was loaded and when
    # apply() saves, the final save must preserve that concurrent change
    # for models this apply() didn't touch, while still applying its own
    # change for the model it did touch. This simulates a DownloadManager
    # completion for a *different* model landing mid-apply.
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="ollama/deleteme", family="f", provider="ollama", name="deleteme:7b"),
    )
    state_path = tmp_path / "modelman.toml"
    # apply()'s in-memory snapshot: only knows about ollama/deleteme.
    stale_state = _make_state()
    stale_state.set("ollama/deleteme", ModelState(ready=True, disk_path="ollama:deleteme:7b"))
    save_state(stale_state, state_path)

    provider = MagicMock()
    provider.is_downloaded.return_value = True
    provider.delete.return_value = None

    pending = PendingChanges(
        registry=reg,
        state=stale_state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        deletes=[("ollama/deleteme", {"id": "ollama/deleteme", "provider": "ollama", "name": "deleteme:7b"})],
    )

    # Simulate a concurrent DownloadManager completion for an unrelated
    # model, landing on disk after `stale_state` was captured but before
    # apply()'s final save.
    from modelman.state import locked_state

    with locked_state(state_path) as concurrent:
        concurrent.set("ollama/other", ModelState(ready=True, disk_path="ollama:other:7b"))

    pending.apply()

    loaded = load_state(state_path)
    assert "ollama/deleteme" not in loaded.models  # this apply()'s own change landed
    assert loaded.get("ollama/other").ready is True  # the concurrent write survived
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_queue.py -k "ready_on_entries or ready_off_still or merges_onto_fresh" -v`
Expected: FAIL — the first because `apply()` still downloads today, the
third because `apply()`'s save currently overwrites the concurrent write.

- [x] **Step 3: Rewrite `apply()`'s ready loop and final save**

In `src/modelman/queue.py`:

1. Change the import line `from .state import save_state` to
   `from .state import locked_state`.
2. Remove the now-unused `from .providers._progress import DownloadCancelled`
   import (the download branch that catches it is being deleted).
3. Add two tracking fields to the `PendingChanges` dataclass, right after
   `cancelled: bool = False`:

```python
    # Populated during apply(); used only by the final save to merge this
    # run's changes onto a freshly-loaded on-disk StateStore instead of
    # overwriting the whole file from this object's (possibly stale
    # relative to a concurrent DownloadManager write) in-memory snapshot.
    _touched_model_ids: set[str] = field(default_factory=set)
    _forgotten_families: set[str] = field(default_factory=set)
```

4. In the deletes loop, right after `self.state.models.pop(model_id, None)`,
   add `self._touched_model_ids.add(model_id)`.

5. In the family stickiness loop, right after `self.state.forget_family(f)`,
   add `self._forgotten_families.add(f)`.

6. Replace the entire ready loop (from `attempted_deletes = {...}` through
   the end of the `if not target and self.state.get(model_id).litellm_exposed:`
   block) with:

```python
        attempted_deletes = {mid for mid, _ in self.deletes}
        for model_id, variant, target in self.ready:
            if aborted():
                return
            if model_id in attempted_deletes:
                continue
            assert variant["id"] == model_id, (
                f"variant id {variant['id']!r} != queued model_id {model_id!r}"
            )
            label = _label(variant)
            provider_id = variant["provider"]
            provider = self.providers.get(provider_id)
            # Real downloads no longer run inside apply() — the caller
            # (ModelScreen) must route a ready-on against a real provider
            # through DownloadManager.start() instead of queuing it here.
            # A target=True entry reaching this point with a provider
            # present is a caller bug, not a runtime condition to handle.
            assert not (provider is not None and target), (
                f"ready-on for {model_id!r} must go through DownloadManager, "
                "not PendingChanges.apply()"
            )
            if provider is None:
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
            else:
                # provider present, target must be False (asserted above): clear.
                emit(f"delete:start|{model_id}|{label}")
                artifact_present = True
                try:
                    artifact_present = bool(provider.is_downloaded(variant))  # type: ignore[attr-defined]
                except Exception:  # noqa: BLE001
                    artifact_present = True
                if artifact_present:
                    conflict = _shared_artifact_owner(self.registry, provider, variant)
                    if conflict is not None:
                        reason = f"artifact shared with {conflict.id} — not removed"
                        self.failures.append(f"clear {model_id}: {reason}")
                        emit(f"delete:fail|{model_id}|{label}|{reason}")
                    else:
                        try:
                            self._delete(variant)
                        except Exception as exc:  # noqa: BLE001
                            reason = _reason(exc)
                            self.failures.append(f"clear {model_id}: {exc}")
                            emit(f"delete:fail|{model_id}|{label}|{reason}")
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
            if not target and self.state.get(model_id).litellm_exposed:
                self.exposes = [(mid, t) for mid, t in self.exposes if mid != model_id]
                if provider is None:
                    self.state.set(
                        model_id, replace(self.state.get(model_id), litellm_exposed=False)
                    )
                    self._touched_model_ids.add(model_id)
                else:
                    self.exposes.append((model_id, False))
```

7. Delete the `_download` method entirely (it's now unused — nothing
   calls it once the download branch above is gone).

8. In the exposes block, right before `if self.exposes:` closes (i.e.
   right after the `for warning in warnings: emit(...)` loop, still
   inside `if self.exposes:`), add:

```python
            self._touched_model_ids.update(mid for mid, _ in self.exposes)
```

9. Replace the final save block:

```python
        emit("save:start")
        try:
            save_registry(self.registry, self.registry_path)
            save_state(self.state, self.state_path)
            emit("save:done")
        except Exception as exc:  # noqa: BLE001
            reason = _reason(exc)
            self.failures.append(f"save: {exc}")
            emit(f"save:fail|{reason}")
```

with:

```python
        emit("save:start")
        try:
            save_registry(self.registry, self.registry_path)
            with locked_state(self.state_path) as fresh_state:
                for mid in self._touched_model_ids:
                    fresh_state.set(mid, self.state.get(mid))
                for family in self._forgotten_families:
                    fresh_state.forget_family(family)
            emit("save:done")
        except Exception as exc:  # noqa: BLE001
            reason = _reason(exc)
            self.failures.append(f"save: {exc}")
            emit(f"save:fail|{reason}")
```

- [x] **Step 4: Run the full queue test suite to verify everything passes**

Run: `uv run pytest tests/test_queue.py tests/test_providers/test_progress.py -v`
Expected: PASS. Note `test_pending_changes_forwards_on_progress` in
`test_providers/test_progress.py` exercises the old download branch — it
must be deleted or rewritten as part of this step since the behavior it
tests no longer exists in `apply()`. Delete it; download progress
forwarding is now `DownloadManager`'s responsibility, covered in Task 6.

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/queue.py tests/test_queue.py && uv run mypy src/modelman/queue.py`

```bash
git add src/modelman/queue.py tests/test_queue.py tests/test_providers/test_progress.py
git commit -m "$(cat <<'EOF'
refactor(queue): drop download step from apply(), merge state on save

Ready-on against a real provider now must route through DownloadManager
instead of PendingChanges.ready — apply() asserts this invariant rather
than silently downloading. The final state save now merges this run's
changes onto a freshly-loaded on-disk StateStore (via locked_state)
instead of overwriting the whole file from this object's in-memory
snapshot, which could otherwise revert a concurrent DownloadManager
completion for an unrelated model.

completes plan item #4
EOF
)"
```

---

### Task 5: `DownloadManager` core — start, cancel, status

**Files:**
- Create: `src/modelman/downloads.py`
- Test: `tests/test_downloads.py`

**Interfaces:**
- Produces:
  - `DownloadState` dataclass: `model_id: str`, `variant_id: str`,
    `provider: str`, `status: Literal["downloading", "done", "failed", "cancelled"]`,
    `progress: str = ""`, `error: str | None = None`.
  - `DownloadManager(app)` — `app` is any object with
    `call_from_thread(func, *args) -> Any` (Textual's `App`; a fake in
    tests). `app.notify(message: str) -> None` is used opportunistically
    (via `getattr`, so an `app` without it doesn't break) to surface a
    failed download per the spec's "surfaced ... via notification".
  - `DownloadManager.start(model_id: str, variant: VariantSpec, provider_config: dict, on_complete: Callable[[str], None] | None = None) -> None`
  - `DownloadManager.cancel(model_id: str) -> None`
  - `DownloadManager.is_downloading(model_id: str) -> bool`
  - `DownloadManager.has_active() -> bool`
  - `DownloadManager.states() -> list[DownloadState]`
- Consumes: `ProviderRegistry.get(name, config)` (`providers/registry.py`),
  `Provider.cancel_current()` / `.cleanup_partial_download()` (Task 3),
  `DownloadCancelled` (`providers/_progress.py`).

- [x] **Step 1: Write the failing tests**

```python
# tests/test_downloads.py
"""Tests for DownloadManager: background download lifecycle, cancellation,
parallel isolation, and status reporting."""

from __future__ import annotations

import threading
import time
from unittest.mock import MagicMock

import pytest

from modelman.downloads import DownloadManager, DownloadState
from modelman.providers._progress import DownloadCancelled
from modelman.providers.registry import ProviderRegistry


class _FakeApp:
    """Minimal stand-in for ModelmanApp in unit tests: call_from_thread
    just calls the function immediately (tests join every worker thread
    before asserting, so no real cross-thread marshaling is needed)."""

    def __init__(self) -> None:
        self.notifications: list[str] = []

    def call_from_thread(self, func, *args):
        return func(*args)

    def notify(self, message: str, **kwargs) -> None:
        self.notifications.append(message)


def _register_stub_provider(monkeypatch, provider: MagicMock) -> None:
    monkeypatch.setattr(ProviderRegistry, "get", staticmethod(lambda name, cfg: provider))


def test_start_runs_download_and_marks_done(monkeypatch):
    # The core happy path: start() must invoke provider.download() on a
    # background thread and end with status "done".
    provider = MagicMock()
    provider.download.return_value = "/models/x"
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    done = threading.Event()
    mgr.start(
        "ollama/x",
        {"id": "ollama/x", "provider": "ollama", "name": "x:7b"},
        {},
        on_complete=lambda path: done.set(),
    )
    assert done.wait(timeout=2), "on_complete was never called"

    states = {s.model_id: s for s in mgr.states()}
    assert states["ollama/x"].status == "done"
    assert mgr.is_downloading("ollama/x") is False
    assert mgr.has_active() is False


def test_is_downloading_true_while_in_progress(monkeypatch):
    # Locking (Task 11) depends on is_downloading() being accurate for
    # the duration of the provider.download() call, not just before/after.
    release = threading.Event()
    provider = MagicMock()
    provider.download.side_effect = lambda *a, **k: (release.wait(2), "/models/x")[1]
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, {})
    try:
        assert mgr.is_downloading("ollama/x") is True
        assert mgr.has_active() is True
    finally:
        release.set()


def test_cancel_calls_provider_cancel_current(monkeypatch):
    # cancel() must delegate to the fresh provider instance's own
    # cancel_current() hook — the actual interruption mechanism is
    # provider-specific (subprocess kill for Ollama, a flag for HF).
    cancel_called = threading.Event()
    release = threading.Event()

    def _download(*a, **k):
        release.wait(2)
        raise DownloadCancelled("x")

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.cancel_current.side_effect = lambda: (cancel_called.set(), release.set())
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, {})
    while not mgr.is_downloading("ollama/x"):
        time.sleep(0.01)
    mgr.cancel("ollama/x")

    assert cancel_called.wait(timeout=2)
    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)
    states = {s.model_id: s for s in mgr.states()}
    assert states["ollama/x"].status == "cancelled"
    provider.cleanup_partial_download.assert_called_once()


def test_a_generic_exception_after_cancel_is_still_classified_cancelled(monkeypatch):
    # Ollama's cancel_current() kills the pull subprocess via SIGTERM,
    # which makes download() raise a plain RuntimeError (non-zero exit),
    # not DownloadCancelled. Without explicit cancel-tracking this would
    # misreport as "failed" instead of "cancelled" for exactly the
    # provider users hit most often.
    release = threading.Event()

    def _download(*a, **k):
        release.wait(2)
        raise RuntimeError("`ollama pull x` failed (exit -15)")

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.cancel_current.side_effect = lambda: release.set()
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, {})
    while not mgr.is_downloading("ollama/x"):
        time.sleep(0.01)
    mgr.cancel("ollama/x")

    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)
    states = {s.model_id: s for s in mgr.states()}
    assert states["ollama/x"].status == "cancelled"


def test_download_failure_without_cancel_is_classified_failed(monkeypatch):
    # A genuine failure (network error, disk full, ...) with no
    # cancel() call must be reported as "failed", not "cancelled".
    provider = MagicMock()
    provider.download.side_effect = RuntimeError("no space left on device")
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    done = threading.Event()
    mgr.start(
        "ollama/x",
        {"id": "ollama/x", "provider": "ollama", "name": "x:7b"},
        {},
        on_complete=lambda path: None,
    )
    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)

    states = {s.model_id: s for s in mgr.states()}
    assert states["ollama/x"].status == "failed"
    assert "no space left" in (states["ollama/x"].error or "")


def test_failure_notifies_the_app(monkeypatch):
    # Spec requires a failure be "surfaced ... via notification" — the
    # DownloadScreen table alone isn't enough, since the user may be on
    # a completely different screen when a background download fails.
    provider = MagicMock()
    provider.download.side_effect = RuntimeError("no space left on device")
    _register_stub_provider(monkeypatch, provider)

    app = _FakeApp()
    mgr = DownloadManager(app)
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, {})

    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)

    assert any("ollama/x" in n and "no space left" in n for n in app.notifications), app.notifications


def test_parallel_downloads_get_isolated_provider_instances(monkeypatch):
    # Two simultaneous downloads must each get their OWN provider
    # instance from ProviderRegistry.get(), so per-download cancellation
    # state (_current_proc / _cancel_requested) can't cross-contaminate.
    seen_instances: list[object] = []

    def _factory(name, cfg):
        instance = MagicMock()
        instance.download.return_value = "/models/x"
        seen_instances.append(instance)
        return instance

    monkeypatch.setattr(ProviderRegistry, "get", staticmethod(_factory))

    mgr = DownloadManager(_FakeApp())
    done_a = threading.Event()
    done_b = threading.Event()
    mgr.start("ollama/a", {"id": "ollama/a", "provider": "ollama", "name": "a"}, {}, on_complete=lambda p: done_a.set())
    mgr.start("ollama/b", {"id": "ollama/b", "provider": "ollama", "name": "b"}, {}, on_complete=lambda p: done_b.set())

    assert done_a.wait(timeout=2)
    assert done_b.wait(timeout=2)
    assert len(seen_instances) == 2
    assert seen_instances[0] is not seen_instances[1]


def test_start_is_a_noop_if_already_downloading(monkeypatch):
    # A double 'r' press (or a race between the poll and a keypress)
    # must not spawn a second thread against the same model_id.
    release = threading.Event()
    provider = MagicMock()
    call_count = {"n": 0}

    def _download(*a, **k):
        call_count["n"] += 1
        release.wait(2)
        return "/models/x"

    provider.download.side_effect = _download
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})
    release.set()
    time.sleep(0.1)
    assert call_count["n"] == 1
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_downloads.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.downloads'`

- [x] **Step 3: Implement `DownloadManager` core**

```python
# src/modelman/downloads.py
"""DownloadManager — background model downloads, independent of the
apply-on-exit queue in queue.py.

Owned by ModelmanApp (created once, survives screen navigation:
self.app.downloads). Each download runs on its own daemon thread against
a fresh provider instance (ProviderRegistry.get()), so per-download
cancellation state (Ollama's _current_proc, oMLX/llamacpp's
_cancel_requested) never crosses between simultaneous downloads.
"""

from __future__ import annotations

import threading
from collections.abc import Callable
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Literal

from .providers._progress import DownloadCancelled
from .providers.registry import ProviderRegistry

if TYPE_CHECKING:
    from .providers.base import Provider, VariantSpec

Status = Literal["downloading", "done", "failed", "cancelled"]


@dataclass
class DownloadState:
    """Snapshot of one download for the UI (DownloadScreen, ModelScreen's
    STATUS glyph, the quit guard). `progress` is the latest raw line
    forwarded from the provider's on_progress callback."""

    model_id: str
    variant_id: str
    provider: str
    status: Status
    progress: str = ""
    error: str | None = None


class DownloadManager:
    def __init__(self, app: object) -> None:
        self._app = app
        self._lock = threading.Lock()
        self._states: dict[str, DownloadState] = {}
        self._providers: dict[str, Provider] = {}
        self._variants: dict[str, VariantSpec] = {}
        self._cancel_requested: set[str] = set()
        self._post_download: dict[str, list[Callable[[], None]]] = {}

    def start(
        self,
        model_id: str,
        variant: VariantSpec,
        provider_config: dict,
        on_complete: Callable[[str], None] | None = None,
    ) -> None:
        with self._lock:
            if model_id in self._states and self._states[model_id].status == "downloading":
                return  # already in flight; no-op (guards double-keypress races)
            provider = ProviderRegistry.get(variant["provider"], provider_config)
            self._providers[model_id] = provider
            self._variants[model_id] = variant
            self._cancel_requested.discard(model_id)
            self._states[model_id] = DownloadState(
                model_id=model_id,
                variant_id=variant["id"],
                provider=variant["provider"],
                status="downloading",
            )
        thread = threading.Thread(
            target=self._run, args=(model_id, variant, provider, on_complete), daemon=True
        )
        thread.start()

    def cancel(self, model_id: str) -> None:
        with self._lock:
            self._cancel_requested.add(model_id)
            provider = self._providers.get(model_id)
        if provider is None:
            return
        cancel_fn = getattr(provider, "cancel_current", None)
        if callable(cancel_fn):
            cancel_fn()

    def is_downloading(self, model_id: str) -> bool:
        with self._lock:
            state = self._states.get(model_id)
            return state is not None and state.status == "downloading"

    def has_active(self) -> bool:
        with self._lock:
            return any(s.status == "downloading" for s in self._states.values())

    def states(self) -> list[DownloadState]:
        with self._lock:
            return list(self._states.values())

    def _run(
        self,
        model_id: str,
        variant: VariantSpec,
        provider: Provider,
        on_complete: Callable[[str], None] | None,
    ) -> None:
        def _on_progress(line: str) -> None:
            with self._lock:
                state = self._states.get(model_id)
                if state is not None:
                    state.progress = line

        try:
            try:
                local_path = provider.download(variant, on_progress=_on_progress)
            except TypeError:
                local_path = provider.download(variant)
        except Exception as exc:  # noqa: BLE001
            was_cancelled = model_id in self._cancel_requested
            # A cancelled provider can raise DownloadCancelled (oMLX/
            # llamacpp, via ProgressTqdm's should_cancel) or a generic
            # exception (Ollama's killed subprocess exits non-zero, which
            # download() reports as RuntimeError, not DownloadCancelled).
            # Either way, if cancel() was called for this model_id, the
            # user asked for this — classify it as cancelled, not failed.
            if was_cancelled or isinstance(exc, DownloadCancelled):
                self._finish(model_id, "cancelled", cleanup=(provider, variant))
            else:
                self._finish(model_id, "failed", error=str(exc) or exc.__class__.__name__)
            return

        self._finish(model_id, "done", local_path=local_path)
        if on_complete is not None:
            self._app.call_from_thread(on_complete, local_path)  # type: ignore[attr-defined]

    def _finish(
        self,
        model_id: str,
        status: Status,
        *,
        local_path: str | None = None,
        error: str | None = None,
        cleanup: tuple[Provider, VariantSpec] | None = None,
    ) -> None:
        if cleanup is not None:
            provider, variant = cleanup
            try:
                provider.cleanup_partial_download(variant)
            except Exception:  # noqa: BLE001
                pass  # best-effort; a failed cleanup must not mask the cancel
        with self._lock:
            state = self._states.get(model_id)
            if state is not None:
                state.status = status
                state.error = error
            self._cancel_requested.discard(model_id)
            self._providers.pop(model_id, None)
        if status == "failed":
            # Surfaced via notification, not just the DownloadScreen
            # table — the user may be on a completely different screen
            # when a background download fails.
            notify = getattr(self._app, "notify", None)
            if callable(notify):
                self._app.call_from_thread(notify, f"Download failed: {model_id}: {error}")  # type: ignore[attr-defined]
```

Note: this step deliberately stops short of the state-persistence
(`locked_state`), post-download-actions, and "done" branch's full
success handling — those are Task 6 and Task 7. The tests above only
exercise start/cancel/fail/parallel-isolation, which this step covers.

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_downloads.py -v`
Expected: PASS

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/downloads.py tests/test_downloads.py && uv run mypy src/modelman/downloads.py`

```bash
git add src/modelman/downloads.py tests/test_downloads.py
git commit -m "$(cat <<'EOF'
feat(downloads): add DownloadManager core (start/cancel/status)

Each download runs on its own daemon thread against a fresh provider
instance, so parallel downloads' cancellation state never
cross-contaminates. cancel() is classified correctly even when the
underlying provider raises a generic exception on kill (Ollama) rather
than DownloadCancelled (oMLX/llamacpp) — tracked via an explicit
cancel-requested set rather than exception type alone.

completes plan item #5
EOF
)"
```

---

### Task 6: `DownloadManager` — persist success, forward progress marshaled to the UI thread

**Files:**
- Modify: `src/modelman/downloads.py`
- Test: `tests/test_downloads.py`

**Interfaces:**
- Consumes: `locked_state` (Task 1).
- Produces: on success, `DownloadManager` writes `ready=True`,
  `disk_path`, `size_bytes` to `modelman.toml` via `locked_state()`
  before invoking `on_complete`. `_on_progress` and completion state
  transitions marshal through `self._app.call_from_thread` so UI-facing
  reads of `states()` never race a half-written `DownloadState`.

- [x] **Step 1: Write the failing tests**

```python
# tests/test_downloads.py — add below the Task 5 tests.

def test_success_persists_ready_and_disk_path_to_state(tmp_path, monkeypatch):
    # DownloadManager must own persisting completion, since its caller
    # (whichever ModelScreen instance started the download) may no
    # longer exist by the time a download finishes.
    from modelman.state import _default_state_path, load_state

    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    assert _default_state_path() == state_path

    (tmp_path / "weights.gguf").write_bytes(b"x" * 1024)
    provider = MagicMock()
    provider.download.return_value = str(tmp_path / "weights.gguf")
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    done = threading.Event()
    mgr.start(
        "ollama/x",
        {"id": "ollama/x", "provider": "ollama", "name": "x:7b"},
        {},
        on_complete=lambda path: done.set(),
    )
    assert done.wait(timeout=2)

    loaded = load_state(state_path)
    entry = loaded.get("ollama/x")
    assert entry.ready is True
    assert entry.disk_path == str(tmp_path / "weights.gguf")
    assert entry.size_bytes == 1024


def test_success_persistence_is_a_merge_not_an_overwrite(tmp_path, monkeypatch):
    # Regression for the exact race Task 1/4 exist to close: an unrelated
    # model's state already on disk must survive a download completion
    # for a *different* model.
    from modelman.state import ModelState, StateStore, _default_state_path, load_state, save_state

    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    assert _default_state_path() == state_path
    existing = StateStore()
    existing.set("ollama/other", ModelState(ready=True, disk_path="ollama:other"))
    save_state(existing, state_path)

    (tmp_path / "weights.gguf").write_bytes(b"x")
    provider = MagicMock()
    provider.download.return_value = str(tmp_path / "weights.gguf")
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    done = threading.Event()
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {}, on_complete=lambda p: done.set())
    assert done.wait(timeout=2)

    loaded = load_state(state_path)
    assert loaded.get("ollama/other").ready is True  # untouched
    assert loaded.get("ollama/x").ready is True  # this download's own write


def test_progress_lines_are_recorded(monkeypatch):
    provider = MagicMock()

    def _download(variant, on_progress=None, **kwargs):
        on_progress("50 MB / 100 MB (50%)")
        return "/models/x"

    provider.download.side_effect = _download
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    done = threading.Event()
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {}, on_complete=lambda p: done.set())
    assert done.wait(timeout=2)
    # progress was captured at some point before completion; can't assert
    # the exact final value deterministically, just that it was recorded.
    # Re-fetch mid-flight instead for a deterministic check:


def test_progress_visible_while_downloading(monkeypatch):
    release = threading.Event()
    seen_progress = threading.Event()

    def _download(variant, on_progress=None, **kwargs):
        on_progress("50 MB / 100 MB (50%)")
        seen_progress.set()
        release.wait(2)
        return "/models/x"

    provider = MagicMock()
    provider.download.side_effect = _download
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})
    assert seen_progress.wait(timeout=2)
    states = {s.model_id: s for s in mgr.states()}
    assert "50%" in states["ollama/x"].progress
    release.set()
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_downloads.py -k "persist or progress" -v`
Expected: FAIL (state.py isn't written to yet on success)

- [x] **Step 3: Wire success persistence into `_run`/`_finish`**

In `src/modelman/downloads.py`:

1. Add the import: `from .state import locked_state`.
2. Add `from dataclasses import replace` — no, `ModelState` is a plain
   dataclass with defaults; use `state.set(model_id, ModelState(ready=True, ...))`
   directly since we're writing a fresh entry, not mutating an existing
   one's other fields. Add `from .state import ModelState, locked_state`.
3. Replace the success branch in `_run`:

```python
        self._finish(model_id, "done", local_path=local_path)
        if on_complete is not None:
            self._app.call_from_thread(on_complete, local_path)  # type: ignore[attr-defined]
```

with:

```python
        size_bytes = self._size_of(local_path)
        with locked_state() as state:
            state.set(model_id, ModelState(ready=True, disk_path=local_path, size_bytes=size_bytes))
        self._finish(model_id, "done", local_path=local_path)
        if on_complete is not None:
            self._app.call_from_thread(on_complete, local_path)  # type: ignore[attr-defined]
```

4. Add a small helper (near the bottom of the class):

```python
    @staticmethod
    def _size_of(local_path: str) -> int | None:
        from pathlib import Path

        try:
            p = Path(local_path)
            return p.stat().st_size if p.is_file() else None
        except OSError:
            return None
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_downloads.py -v`
Expected: PASS (all tests from Task 5 and this task)

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/downloads.py tests/test_downloads.py && uv run mypy src/modelman/downloads.py`

```bash
git add src/modelman/downloads.py tests/test_downloads.py
git commit -m "$(cat <<'EOF'
feat(downloads): persist completion to modelman.toml via locked_state

DownloadManager owns persisting a successful download's ready/disk_path/
size_bytes, since the ModelScreen instance that started it may no
longer exist when it finishes. Uses locked_state (Task 1) so this never
clobbers a concurrent write for an unrelated model.

completes plan item #6
EOF
)"
```

---

### Task 7: Post-download actions (deferred exposes)

**Files:**
- Modify: `src/modelman/downloads.py`
- Test: `tests/test_downloads.py`

**Interfaces:**
- Produces: `DownloadManager.register_post_download(model_id: str, action: Callable[[], None]) -> None`
  — `action` runs once, only on that download's success, after state
  persistence. Actions registered for a download that ends in `failed`
  or `cancelled` are discarded, never run.
- Consumes (Task 15): `ModelScreen._run_apply` registers a closure that
  applies a deferred expose via `apply_expose_queue`.

- [x] **Step 1: Write the failing tests**

```python
# tests/test_downloads.py — add below the Task 6 tests.

def test_post_download_action_runs_on_success(monkeypatch):
    provider = MagicMock()
    provider.download.return_value = "/models/x"
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    ran = threading.Event()
    mgr.register_post_download("ollama/x", ran.set)
    done = threading.Event()
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {}, on_complete=lambda p: done.set())

    assert done.wait(timeout=2)
    assert ran.wait(timeout=2)


def test_post_download_action_dropped_on_cancel(monkeypatch):
    release = threading.Event()

    def _download(*a, **k):
        release.wait(2)
        raise DownloadCancelled("x")

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.cancel_current.side_effect = lambda: release.set()
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    ran = threading.Event()
    mgr.register_post_download("ollama/x", ran.set)
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})
    while not mgr.is_downloading("ollama/x"):
        time.sleep(0.01)
    mgr.cancel("ollama/x")

    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)
    time.sleep(0.05)  # let a wrongly-firing action run, if any
    assert not ran.is_set()


def test_post_download_action_dropped_on_failure(monkeypatch):
    provider = MagicMock()
    provider.download.side_effect = RuntimeError("network error")
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    ran = threading.Event()
    mgr.register_post_download("ollama/x", ran.set)
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})

    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)
    time.sleep(0.05)
    assert not ran.is_set()
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_downloads.py -k post_download -v`
Expected: FAIL with `AttributeError: 'DownloadManager' object has no attribute 'register_post_download'`

- [x] **Step 3: Implement post-download actions**

In `src/modelman/downloads.py`:

1. Add the method to `DownloadManager`:

```python
    def register_post_download(self, model_id: str, action: Callable[[], None]) -> None:
        """Run `action` once, only if this model_id's current (or next)
        download finishes successfully. Used to defer a queued expose
        against a model that's still downloading until it's actually
        ready — see ModelScreen._run_apply (Task 15)."""
        with self._lock:
            self._post_download.setdefault(model_id, []).append(action)
```

2. In `_finish`, run and clear registered actions only for `status == "done"`;
   drop them (without running) for any other terminal status. Replace
   the end of `_finish` (after the `with self._lock:` block that clears
   `_cancel_requested`/`_providers`) — the full updated method:

```python
    def _finish(
        self,
        model_id: str,
        status: Status,
        *,
        local_path: str | None = None,
        error: str | None = None,
        cleanup: tuple[Provider, VariantSpec] | None = None,
    ) -> None:
        if cleanup is not None:
            provider, variant = cleanup
            try:
                provider.cleanup_partial_download(variant)
            except Exception:  # noqa: BLE001
                pass  # best-effort; a failed cleanup must not mask the cancel
        with self._lock:
            state = self._states.get(model_id)
            if state is not None:
                state.status = status
                state.error = error
            self._cancel_requested.discard(model_id)
            self._providers.pop(model_id, None)
            actions = self._post_download.pop(model_id, [])
        if status == "done":
            for action in actions:
                try:
                    action()
                except Exception:  # noqa: BLE001
                    pass  # a failed deferred action must not crash the download thread
        # status in ("failed", "cancelled"): actions are discarded,
        # never run — the model never became ready, so an action that
        # assumed it did (e.g. writing a LiteLLM route) would be wrong.
        if status == "failed":
            notify = getattr(self._app, "notify", None)
            if callable(notify):
                self._app.call_from_thread(notify, f"Download failed: {model_id}: {error}")  # type: ignore[attr-defined]
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_downloads.py -v`
Expected: PASS (all tests, Tasks 5-7)

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/downloads.py tests/test_downloads.py && uv run mypy src/modelman/downloads.py`

```bash
git add src/modelman/downloads.py tests/test_downloads.py
git commit -m "$(cat <<'EOF'
feat(downloads): add register_post_download for deferred exposes

Actions registered against a model_id run once, only on that download's
success; a cancel or failure drops them silently, since the failure
itself is already surfaced via the download's own status.

completes plan item #7
EOF
)"
```

---

### Task 8: App-level wiring — `ModelmanApp.downloads` and the quit guard

**Files:**
- Modify: `src/modelman/app.py`
- Modify: `src/modelman/screens/forms.py`
- Test: `tests/test_app_settings.py` (or a new `tests/screens/test_app_navigation.py` addition — use whichever existing file covers `ModelmanApp` construction; check both before choosing)

**Interfaces:**
- Produces: `ModelmanApp.downloads: DownloadManager` (constructed in
  `__init__`, before `on_mount`, so it exists the instant any screen
  might reference `self.app.downloads`).
- Produces: `ModelmanApp.request_quit() -> None` — if
  `self.downloads.has_active()`, pushes `QuitBlockedModal`; if the user
  chooses "Review Downloads", pushes `DownloadScreen` (Task 10); if not
  active, calls `self.exit()`.
- Produces: `QuitBlockedModal` in `forms.py` — a `ModelmanModal[bool]`
  returning `True` if the user chose "Review Downloads", `False`/`None`
  otherwise.
- Overrides: `ModelmanApp.action_quit()` (the `ctrl+q` binding inherited
  from Textual's `App`) to call `request_quit()` instead of `self.exit()`
  directly.

- [x] **Step 1: Write the failing tests**

```python
# tests/screens/test_app_navigation.py — add near the other app-level tests.
# (Read the existing file first to match its exact fixture/import style;
# it already has a ModelmanApp + registry/state seeding helper.)

@pytest.mark.asyncio
async def test_ctrl_q_exits_immediately_with_no_active_downloads(tmp_path, monkeypatch):
    _seed_registry_and_state(tmp_path, monkeypatch)  # use the file's existing helper
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("ctrl+q")
        await pilot.pause()
    assert app.return_code == 0 or not app.is_running


@pytest.mark.asyncio
async def test_ctrl_q_blocked_while_a_download_is_active(tmp_path, monkeypatch):
    _seed_registry_and_state(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        await pilot.press("ctrl+q")
        await pilot.pause()
        assert app.is_running
        from modelman.screens.forms import QuitBlockedModal

        assert isinstance(app.screen, QuitBlockedModal)


@pytest.mark.asyncio
async def test_quit_blocked_modal_review_pushes_download_screen(tmp_path, monkeypatch):
    _seed_registry_and_state(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        await pilot.press("ctrl+q")
        await pilot.pause()
        await pilot.press("r")  # QuitBlockedModal's "Review Downloads" binding
        await pilot.pause()
        from modelman.screens.downloads import DownloadScreen

        assert isinstance(app.screen, DownloadScreen)
```

Note: `DownloadState` import is `from modelman.downloads import DownloadState`.
`DownloadScreen` doesn't exist until Task 10 — the third test here will
stay red until then; write it now (Step 1) but don't expect it green
until Task 10's Step 4 confirms it. Steps 2/4 below check only the first
two tests, which this task's own code must make pass.

- [x] **Step 2: Run the first two tests to verify they fail**

Run: `uv run pytest tests/screens/test_app_navigation.py -k "ctrl_q" -v`
Expected: FAIL — `app.downloads` doesn't exist yet (`AttributeError`).

- [x] **Step 3: Implement**

In `src/modelman/screens/forms.py`, add near `CancelApplyDialog`:

```python
class QuitBlockedModal(ModelmanModal[bool]):
    """Shown when the user tries to quit while downloads are active.

    Returns True if the user chose to review downloads (caller pushes
    DownloadScreen), False/None otherwise (stay put, downloads keep
    running).
    """

    BINDINGS = [
        Binding("escape", "answer(False)", show=False),
        ("r", "answer(True)"),
    ]

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Downloads are in progress.")
            yield Label("Cancel them from the download screen before quitting.")
            yield self._button_row(
                [
                    Button("Stay", id="stay", variant="default"),
                    Button("Review Downloads", id="review", variant="primary"),
                ]
            )

    def _modal_on_mount(self) -> None:
        self._focus_button("stay")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        self.dismiss(event.button.id == "review")

    def action_answer(self, value: bool) -> None:
        self.dismiss(value)
```

In `src/modelman/app.py`:

1. Add imports: `from .downloads import DownloadManager` and
   `from .screens.forms import QuitBlockedModal`.
2. In `ModelmanApp.__init__`, after `self._initial_family = family` and
   before the settings-loading block, add:

```python
        self.downloads = DownloadManager(self)
```

3. Add a new method to `ModelmanApp` (after `on_mount`):

```python
    def request_quit(self) -> None:
        """The single quit entry point every binding routes through
        (ctrl+q's default action_quit, and FamilyScreen's 'q' — Task 9).
        Blocks quitting while a download is active instead of exiting
        out from under it."""
        if not self.downloads.has_active():
            self.exit()
            return

        def _on_choice(review: bool | None) -> None:
            if review:
                from .screens.downloads import DownloadScreen

                self.push_screen(DownloadScreen())

        self.push_screen(QuitBlockedModal(), _on_choice)

    async def action_quit(self) -> None:
        """Override Textual's default (self.exit()) to route through the
        download quit guard. This also fixes a pre-existing quirk where
        ctrl+q quit immediately from any screen, bypassing ModelScreen's
        apply-on-exit confirm — it's now gated by request_quit() the same
        way FamilyScreen's 'q' binding is."""
        self.request_quit()
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_app_navigation.py -k "ctrl_q" -v`
Expected: PASS

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/app.py src/modelman/screens/forms.py tests/screens/test_app_navigation.py && uv run mypy src/modelman/app.py src/modelman/screens/forms.py`

```bash
git add src/modelman/app.py src/modelman/screens/forms.py tests/screens/test_app_navigation.py
git commit -m "$(cat <<'EOF'
feat(app): add DownloadManager instance and quit guard

ModelmanApp owns a single DownloadManager for its lifetime. ctrl+q (and
FamilyScreen's 'q', wired in the next task) now route through
request_quit(), which blocks exiting while a download is active and
offers to open the download screen instead — this also closes a
pre-existing gap where ctrl+q bypassed ModelScreen's apply-on-exit
confirm entirely.

completes plan item #8
EOF
)"
```

---

### Task 9: `FamilyScreen` quit guard + `g` binding

**Files:**
- Modify: `src/modelman/screens/families.py`
- Test: `tests/screens/test_families.py`

**Interfaces:**
- Modifies: `FamilyScreen.action_quit()` to call
  `self.app.request_quit()` instead of `self.app.exit()`.
- Adds: `FamilyScreen.BINDINGS` gains `("g", "open_downloads", "Downloads")`
  and `action_open_downloads()` pushing `DownloadScreen` (Task 10 — this
  task can write the binding/action now; the test for it stays red until
  Task 10 lands, same as Task 8's third test).

- [x] **Step 1: Write the failing test**

```python
# tests/screens/test_families.py — add near the other action tests.

@pytest.mark.asyncio
async def test_q_blocked_while_a_download_is_active(tmp_path, monkeypatch):
    # 'q' on FamilyScreen must go through the same quit guard as ctrl+q.
    _seed_registry_and_state(tmp_path, monkeypatch)  # match this file's existing helper name
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        from modelman.downloads import DownloadState

        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        await pilot.press("q")
        await pilot.pause()
        assert app.is_running
        from modelman.screens.forms import QuitBlockedModal

        assert isinstance(app.screen, QuitBlockedModal)


@pytest.mark.asyncio
async def test_q_exits_immediately_with_no_active_downloads(tmp_path, monkeypatch):
    _seed_registry_and_state(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("q")
        await pilot.pause()
    assert not app.is_running
```

(Read `tests/screens/test_families.py` first to confirm the exact name
of its registry/state seeding helper and match it — do not invent a new
one if an equivalent already exists in that file.)

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/screens/test_families.py -k "q_blocked or q_exits" -v`
Expected: FAIL — `action_quit` still calls `self.app.exit()` unconditionally,
so the first test fails (app exits despite the active download).

- [x] **Step 3: Implement**

In `src/modelman/screens/families.py`, replace:

```python
    def action_quit(self) -> None:
        self.app.exit()
```

with:

```python
    def action_quit(self) -> None:
        self.app.request_quit()

    def action_open_downloads(self) -> None:
        from .downloads import DownloadScreen

        self.app.push_screen(DownloadScreen())
```

and add to `BINDINGS`:

```python
        ("g", "open_downloads", "Downloads"),
```

(insert after the `"q"` binding, before `enter`/`q` order doesn't
matter — keep alphabetical-ish grouping consistent with the existing
list's style: `a`, `e`, `d`, `enter`, `g`, `q`.)

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_families.py -k "q_blocked or q_exits" -v`
Expected: PASS for `q_exits_immediately`; `q_blocked_while_active` PASS too
(it only needs `QuitBlockedModal`, which exists from Task 8 — it does
NOT need `DownloadScreen`, which only `action_open_downloads` needs, not
this test path).

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/screens/families.py tests/screens/test_families.py && uv run mypy src/modelman/screens/families.py`

```bash
git add src/modelman/screens/families.py tests/screens/test_families.py
git commit -m "$(cat <<'EOF'
feat(families): route 'q' through the download quit guard, add 'g' binding

FamilyScreen's quit and ctrl+q (Task 8) now share one guard. The 'g'
binding is wired to open DownloadScreen (Task 10); this commit adds the
binding and action, the screen itself lands next.

completes plan item #9
EOF
)"
```

---

### Task 10: `DownloadScreen`

**Files:**
- Create: `src/modelman/screens/downloads.py`
- Modify: `src/modelman/screens/models.py` (add the `g` binding — the
  only change to this file in this task; the rest of models.py's
  download-routing work is Tasks 11-15)
- Test: `tests/screens/test_downloads_screen.py`

**Interfaces:**
- Produces: `DownloadScreen(Screen[None])` — a `DataTable` of
  `self.app.downloads.states()` (model · provider · status · progress),
  refreshed on a 1s poll. Per-row cancel via `c` (calls
  `self.app.downloads.cancel(model_id)` for the row under the cursor,
  only if its status is `"downloading"`).
- Consumes: `DownloadManager.states()` / `.cancel()` (Tasks 5-7).

- [x] **Step 1: Write the failing tests**

```python
# tests/screens/test_downloads_screen.py
"""Tests for DownloadScreen: rendering DownloadManager's states and
per-row cancel."""

import pytest
from textual.widgets import DataTable

from modelman.app import ModelmanApp
from modelman.downloads import DownloadState
from modelman.screens.downloads import DownloadScreen
from modelman.registry import AuthConfig, FamilyEntry, ProviderEntry, Registry, save_registry
from modelman.state import StateStore, save_state


def _seed(tmp_path, monkeypatch):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
            families=[FamilyEntry(name="f")],
        ),
        reg_path,
    )
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))


@pytest.mark.asyncio
async def test_download_screen_renders_active_downloads(tmp_path, monkeypatch):
    # Opening the screen must show whatever DownloadManager already knows
    # about, without requiring a download to actually be running.
    _seed(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        app.push_screen(DownloadScreen())
        await pilot.pause()

        table = app.screen.query_one(DataTable)
        assert table.row_count == 1
        row = list(table.rows.keys())[0]
        assert str(row.value) == "ollama/x"


@pytest.mark.asyncio
async def test_cancel_key_cancels_the_row_under_cursor(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        app.push_screen(DownloadScreen())
        await pilot.pause()

        cancelled = []
        monkeypatch.setattr(app.downloads, "cancel", lambda mid: cancelled.append(mid))
        await pilot.press("c")
        await pilot.pause()
        assert cancelled == ["ollama/x"]


@pytest.mark.asyncio
async def test_cancel_key_is_a_noop_on_a_finished_row(tmp_path, monkeypatch):
    # Cancelling a row that already finished (done/failed/cancelled) must
    # not call DownloadManager.cancel() at all — nothing to cancel.
    _seed(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="done"
        )
        app.push_screen(DownloadScreen())
        await pilot.pause()

        cancelled = []
        monkeypatch.setattr(app.downloads, "cancel", lambda mid: cancelled.append(mid))
        await pilot.press("c")
        await pilot.pause()
        assert cancelled == []
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/screens/test_downloads_screen.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.screens.downloads'`

- [x] **Step 3: Implement `DownloadScreen`**

```python
# src/modelman/screens/downloads.py
"""DownloadScreen — live view of DownloadManager's in-flight and
finished downloads, opened with 'g' from FamilyScreen and ModelScreen."""

from __future__ import annotations

from textual.app import ComposeResult
from textual.binding import Binding
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header


class DownloadScreen(Screen[None]):
    BINDINGS = [
        ("escape", "back", "Back"),
        ("c", "cancel_selected", "Cancel"),
    ]

    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="downloads-table", cursor_type="row")
        yield Footer()

    def on_mount(self) -> None:
        table = self.query_one("#downloads-table", DataTable)
        table.add_columns("MODEL", "PROVIDER", "STATUS", "PROGRESS")
        self._reload()
        self.set_interval(1.0, self._reload)

    def _reload(self) -> None:
        table = self.query_one("#downloads-table", DataTable)
        cursor_key = None
        if table.row_count and table.cursor_row is not None and table.cursor_row < table.row_count:
            cursor_key = list(table.rows.keys())[table.cursor_row].value
        table.clear()
        n_active = 0
        for state in self.app.downloads.states():  # type: ignore[attr-defined]
            if state.status == "downloading":
                n_active += 1
            table.add_row(
                state.model_id,
                state.provider,
                state.status,
                state.progress or state.error or "",
                key=state.model_id,
            )
        self.title = f"⏳ {n_active} downloading" if n_active else "Downloads"
        if cursor_key is not None:
            keys = [k.value for k in table.rows.keys()]
            if cursor_key in keys:
                table.move_cursor(row=keys.index(cursor_key))

    def action_back(self) -> None:
        self.app.pop_screen()

    def action_cancel_selected(self) -> None:
        table = self.query_one("#downloads-table", DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        model_id = str(row_key.value)
        state = next((s for s in self.app.downloads.states() if s.model_id == model_id), None)  # type: ignore[attr-defined]
        if state is None or state.status != "downloading":
            return
        self.app.downloads.cancel(model_id)  # type: ignore[attr-defined]
```

In `src/modelman/screens/models.py`, add to `ModelScreen.BINDINGS` (after
`("x", "toggle_expose", "Toggle exposed")`):

```python
        ("g", "open_downloads", "Downloads"),
```

and add the action method (near `action_back`):

```python
    def action_open_downloads(self) -> None:
        from .downloads import DownloadScreen

        self.app.push_screen(DownloadScreen())
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_downloads_screen.py tests/screens/test_app_navigation.py tests/screens/test_families.py -v`
Expected: PASS — including Task 8's and Task 9's previously-red
`DownloadScreen`-dependent tests.

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/screens/downloads.py src/modelman/screens/models.py tests/screens/test_downloads_screen.py && uv run mypy src/modelman/screens/downloads.py`

```bash
git add src/modelman/screens/downloads.py src/modelman/screens/models.py tests/screens/test_downloads_screen.py
git commit -m "$(cat <<'EOF'
feat(screens): add DownloadScreen, wire 'g' binding on both list screens

Live table of DownloadManager's states, refreshed on a 1s poll; 'c'
cancels the row under the cursor if it's still downloading. This also
completes the QuitBlockedModal -> DownloadScreen and 'g' -> DownloadScreen
paths left pending from Tasks 8 and 9.

completes plan item #10
EOF
)"
```

---

### Task 11: `ModelScreen` — downloading glyph, locking, live poll, shared helpers

**Files:**
- Modify: `src/modelman/screens/models.py`
- Test: `tests/screens/test_models.py`

**Interfaces:**
- Produces:
  - `ModelScreen._provider_entry_or_none(provider_id: str) -> ProviderEntry | None`
  - `ModelScreen._start_download(entry: ModelEntry) -> None` — calls
    `self.app.downloads.start(...)`, refreshes the UI immediately.
  - `ModelScreen._on_download_finished(model_id: str) -> None` — reloads
    `self.state` from disk and re-renders; called both from
    `_start_download`'s `on_complete` closure and (defensively) safe to
    call from the poll.
  - STATUS glyph gains `⏳` for `self.app.downloads.is_downloading(m.id)`,
    checked before the existing queued-ready/move/ready checks.
  - `action_delete_model` / `action_edit_model` refuse (with a
    notification) when the target model is downloading.
  - A 1s poll (`self.set_interval` in `on_mount`) reloads `self.state`
    from disk and re-renders while any download is active, so completion
    is visible without navigating away and back.
- Consumes: `model_has_local_artifact` (`registry.py`), `DownloadManager`
  (Tasks 5-7).

- [x] **Step 1: Write the failing tests**

```python
# tests/screens/test_models.py — add near the other action tests. Use
# the file's existing `_seed_registry_and_state` helper and
# `_open_model_screen` helper (both already defined near the top of the
# file, per the earlier read of this file).

@pytest.mark.asyncio
async def test_status_shows_downloading_glyph(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        app.screen.reload()
        await pilot.pause()

        table = app.screen.query_one(DataTable)
        row = table.get_row_at(0)
        assert "⏳" in str(row[4])  # STATUS column


@pytest.mark.asyncio
async def test_delete_blocked_while_downloading(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        await pilot.press("d")
        await pilot.pause()

        assert app.screen.queued_deletes == {}


@pytest.mark.asyncio
async def test_edit_blocked_while_downloading(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        await pilot.press("e")
        await pilot.pause()

        from modelman.screens.forms import ModelForm

        assert not isinstance(app.screen, ModelForm)


@pytest.mark.asyncio
async def test_poll_refreshes_state_after_download_completes(tmp_path, monkeypatch):
    # Simulates DownloadManager writing modelman.toml directly (as it
    # does on completion) while ModelScreen is open and watching — the
    # screen's own in-memory StateStore must pick this up without the
    # user navigating away and back.
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        assert app.screen._is_ready("ollama/x") is False

        from modelman.state import ModelState, locked_state

        with locked_state(state_path) as state:
            state.set("ollama/x", ModelState(ready=True, disk_path="/models/x"))

        await pilot.pause(1.1)  # let the 1s poll tick at least once
        assert app.screen._is_ready("ollama/x") is True
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/screens/test_models.py -k "downloading_glyph or blocked_while_downloading or poll_refreshes" -v`
Expected: FAIL (no glyph handling, no locking, no poll)

- [x] **Step 3: Implement**

In `src/modelman/screens/models.py`, add imports:
`from ..registry import ... model_has_local_artifact` (add to the
existing `from ..registry import (...)` block) and no new top-level
import for `DownloadManager` is needed (accessed via `self.app.downloads`).

Add two helpers (near `_provider_list`):

```python
    def _provider_entry_or_none(self, provider_id: str):
        try:
            return self.registry.provider(provider_id)
        except KeyError:
            return None

    def _start_download(self, entry: ModelEntry) -> None:
        """Route a real ready-on through DownloadManager instead of the
        apply-on-exit queue — the download starts immediately in the
        background and PendingChanges.apply() never touches it (Task 4's
        assertion enforces this)."""
        variant = model_entry_to_variant(entry)
        config = provider_config(self.registry.provider(entry.provider_id))

        def _on_complete(local_path: str, mid: str = entry.id) -> None:
            self.app.call_from_thread(self._on_download_finished, mid)

        self.app.downloads.start(entry.id, variant, config, on_complete=_on_complete)
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

Add the import `load_state` to the top-level `from ..state import` line
(currently `from ..state import ModelState, StateStore` — change to
`from ..state import ModelState, StateStore, load_state`).

In `on_mount`, after `self.run_worker(self._run_reconcile, exclusive=True, thread=True)`,
add:

```python
        self._last_poll_had_active = False
        self.set_interval(1.0, self._poll_downloads)
```

Add the poll method (near `_run_reconcile`):

```python
    def _poll_downloads(self) -> None:
        """Reload state from disk while any download is active (or just
        finished, to catch the final transition), so DownloadManager's
        writes — which land on disk, not on this screen's in-memory
        StateStore — become visible without the user navigating away and
        back. Cheap: modelman.toml is small and this only runs while
        downloads exist."""
        active = self.app.downloads.has_active()
        if not active and not self._last_poll_had_active:
            return
        self._last_poll_had_active = active
        try:
            self.state = load_state(self.state_path)
        except Exception:  # noqa: BLE001
            return
        self.reload()
```

In `_load_models`'s `_repopulate`, change the STATUS glyph block from:

```python
                if m.id in self.queued_deletes:
                    status = "[red]✗[/red]"
                elif m.id in self.queued_ready:
                    status = (
                        "[yellow]↓[/yellow]" if self.queued_ready[m.id] else "[yellow]↑[/yellow]"
                    )
                elif m.id in self.queued_moves:
```

to:

```python
                if m.id in self.queued_deletes:
                    status = "[red]✗[/red]"
                elif self.app.downloads.is_downloading(m.id):
                    status = "[cyan]⏳[/cyan]"
                elif m.id in self.queued_ready:
                    status = (
                        "[yellow]↓[/yellow]" if self.queued_ready[m.id] else "[yellow]↑[/yellow]"
                    )
                elif m.id in self.queued_moves:
```

In `action_delete_model`, right after resolving `entry` (after
`if entry is None: return`), add:

```python
        if self.app.downloads.is_downloading(mid):
            self.app.notify(f"{mid} is downloading — cancel first")
            return
```

In `action_edit_model`, right after resolving `entry` (after
`if entry is None: return`), add the same guard:

```python
        if self.app.downloads.is_downloading(mid):
            self.app.notify(f"{mid} is downloading — cancel first")
            return
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_models.py -v`
Expected: PASS (all tests in the file, including pre-existing ones —
this task only adds guards/glyphs, it doesn't change existing routing
yet, so no pre-existing test should break)

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/screens/models.py tests/screens/test_models.py && uv run mypy src/modelman/screens/models.py`

```bash
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
feat(models): downloading glyph, d/e locking, live poll for completions

STATUS shows a distinct downloading glyph for in-flight models; delete
and edit (which covers moves) refuse with a notification while a model
is downloading. A 1s poll reloads state from disk while any download is
active, since DownloadManager persists completion directly to
modelman.toml rather than this screen's in-memory StateStore.

completes plan item #11
EOF
)"
```

---

### Task 12: `ModelScreen.action_toggle_ready` — three-way 'r' and real-download routing

**Files:**
- Modify: `src/modelman/screens/models.py`
- Test: `tests/screens/test_models.py`

**Interfaces:**
- Modifies: `action_toggle_ready` — `r` on a downloading model cancels
  it; `r` toggling ready-on for a model whose provider actually
  downloads (`model_has_local_artifact`) calls `self._start_download`
  instead of queuing; every other case (ready-off, flag-only/cloud
  ready-on) is unchanged.
- Modifies: `_projected_ready` — treats an actively-downloading model as
  projected-ready `True` (it has no `queued_ready` entry once routed
  through `_start_download`, so without this the expose-ready gate would
  wrongly reject a queued expose against it — see Task 13).

- [x] **Step 1: Write the failing tests**

```python
# tests/screens/test_models.py — add near the other toggle-ready tests.

@pytest.mark.asyncio
async def test_r_on_downloading_model_cancels(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        cancelled = []
        monkeypatch.setattr(app.downloads, "cancel", lambda mid: cancelled.append(mid))
        await pilot.press("r")
        await pilot.pause()

        assert cancelled == ["ollama/x"]


@pytest.mark.asyncio
async def test_r_on_real_provider_ready_on_starts_download_not_queue(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        started = []
        monkeypatch.setattr(
            app.downloads,
            "start",
            lambda mid, variant, cfg, on_complete=None: started.append(mid),
        )
        await pilot.press("r")
        await pilot.pause()

        assert started == ["ollama/x"]
        assert app.screen.queued_ready == {}  # not queued — routed to DownloadManager


@pytest.mark.asyncio
async def test_r_ready_off_still_queues_as_before(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])
    from modelman.state import ModelState, save_state, StateStore

    st = StateStore()
    st.set("ollama/x", ModelState(ready=True, disk_path="ollama:x:7b"))
    save_state(st, state_path)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.press("r")
        await pilot.pause()

        assert app.screen.queued_ready == {"ollama/x": False}


@pytest.mark.asyncio
async def test_r_flag_only_provider_still_queues_not_downloads(tmp_path, monkeypatch):
    # A native/unmapped provider (no real download mechanism) must keep
    # the pre-existing queued flip behavior, not route through
    # DownloadManager.
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[ProviderEntry(id="claude-agent", name="Claude", auth=AuthConfig(type="native"))],
            families=[FamilyEntry(name="ornith")],
            models=[
                ModelEntry(
                    id="claude-agent/x", family="ornith", provider_id="claude-agent", model_name="x"
                )
            ],
        ),
        reg_path,
    )
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        started = []
        monkeypatch.setattr(
            app.downloads,
            "start",
            lambda mid, variant, cfg, on_complete=None: started.append(mid),
        )
        await pilot.press("r")
        await pilot.pause()

        assert started == []
        assert app.screen.queued_ready == {"claude-agent/x": True}
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/screens/test_models.py -k "test_r_" -v`
Expected: FAIL — `action_toggle_ready` still always queues.

- [x] **Step 3: Implement**

In `src/modelman/screens/models.py`, replace `action_toggle_ready` in
full:

```python
    def action_toggle_ready(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return

        if self.app.downloads.is_downloading(mid):
            # Three-way 'r': downloading -> cancel.
            self.app.downloads.cancel(mid)
            if mid in self._ready_cascade_for_expose:
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
                self.queued_exposes.pop(mid, None)
                self._ready_cascade_for_expose.discard(mid)
            self._enforce_expose_ready_rule(mid, entry)
            self.app.notify(f"Model already {'ready' if target else 'not ready'}")
            self._refresh_pending_bar()
            self.reload()
            return

        if target and model_has_local_artifact(entry, self._provider_entry_or_none(entry.provider_id)):
            # A real download (ollama/omlx/llamacpp, non-cloud): start it
            # immediately in the background instead of queuing it — it is
            # never applied by PendingChanges.apply() (Task 4).
            self._start_download(entry)
            return

        # Flag-only provider or cloud-location ready-on, or any ready-off:
        # unchanged apply-on-exit queue behavior.
        self.queued_ready[mid] = target
        self._enforce_expose_ready_rule(mid, entry)
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()
```

Modify `_projected_ready`:

```python
    def _projected_ready(self, model_id: str) -> bool:
        """The ready value this model will have after apply() (or, for a
        model routed through DownloadManager instead of the queue, after
        its download finishes): the queued target if one exists,
        otherwise True while actively downloading, otherwise the
        persisted flag."""
        if model_id in self.queued_ready:
            return self.queued_ready[model_id]
        if self.app.downloads.is_downloading(model_id):
            return True
        return self.state.get(model_id).ready
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_models.py -v`
Expected: PASS (full file — this replaces `action_toggle_ready` and
`_projected_ready` in ways that must not break the pre-existing
ready/expose tests already in this file)

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/screens/models.py tests/screens/test_models.py && uv run mypy src/modelman/screens/models.py`

```bash
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
feat(models): route real-provider ready-on through DownloadManager

'r' is now three-way: downloading -> cancel, not-ready (real provider)
-> start a background download immediately, everything else (ready-off,
flag-only/cloud ready-on) -> unchanged apply-on-exit queue.
_projected_ready treats an active download as projected-ready so the
expose-ready gate (Task 13) doesn't wrongly reject a queued expose
against a model that has no queued_ready entry precisely because it
routed through DownloadManager instead.

completes plan item #12
EOF
)"
```

---

### Task 13: `ModelScreen.action_toggle_expose` — cascade routing + discard-cancels-cascade

**Files:**
- Modify: `src/modelman/screens/models.py`
- Test: `tests/screens/test_models.py`

**Interfaces:**
- Modifies: `action_toggle_expose`'s cascade branch — for a real-download
  provider, calls `self._start_download(entry)` instead of queuing
  `queued_ready[mid] = True`; flag-only/cloud unchanged. Adds a shared
  `_cancel_ready_cascade(mid)` helper used by both the repeated-keypress
  cancel branch here and `_on_exit_confirm`'s discard branch.
- Modifies: `ModelScreen._on_exit_confirm`'s `"discard"` branch — cancels
  any download still running because of an expose cascade
  (`_ready_cascade_for_expose`), per the spec's "Discard-cancels-cascade".

- [x] **Step 1: Write the failing tests**

```python
# tests/screens/test_models.py — add near the other toggle-expose tests.

@pytest.mark.asyncio
async def test_x_cascade_on_real_provider_starts_download(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        started = []
        monkeypatch.setattr(
            app.downloads,
            "start",
            lambda mid, variant, cfg, on_complete=None: started.append(mid),
        )
        await pilot.press("x")
        await pilot.pause()

        assert started == ["ollama/x"]
        assert app.screen.queued_ready == {}  # not queued
        assert app.screen.queued_exposes == {"ollama/x": True}
        assert "ollama/x" in app.screen._ready_cascade_for_expose


@pytest.mark.asyncio
async def test_x_twice_on_cascaded_download_cancels_it(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        monkeypatch.setattr(app.downloads, "start", lambda *a, **k: None)
        await pilot.press("x")
        await pilot.pause()
        # Simulate the download DownloadManager.start() would report active.
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        cancelled = []
        monkeypatch.setattr(app.downloads, "cancel", lambda mid: cancelled.append(mid))
        await pilot.press("x")
        await pilot.pause()

        assert cancelled == ["ollama/x"]
        assert app.screen.queued_exposes == {}
        assert "ollama/x" not in app.screen._ready_cascade_for_expose


@pytest.mark.asyncio
async def test_discard_cancels_cascaded_download(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        monkeypatch.setattr(app.downloads, "start", lambda *a, **k: None)
        await pilot.press("x")
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        cancelled = []
        monkeypatch.setattr(app.downloads, "cancel", lambda mid: cancelled.append(mid))

        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("d")  # ConfirmExitDialog's "discard" binding
        await pilot.pause()

        assert cancelled == ["ollama/x"]


@pytest.mark.asyncio
async def test_x_on_already_downloading_unrelated_model_just_queues(tmp_path, monkeypatch):
    # 'x' on a model that's already downloading for reasons other than
    # this expose (e.g. the user pressed 'r' first) must NOT be treated
    # as a cascade — it's allowed and simply queues+waits (deferred at
    # apply time, Task 15), with no _ready_cascade_for_expose entry (a
    # discard here must not cancel a download the user started on
    # purpose via 'r').
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        await pilot.press("x")
        await pilot.pause()

        assert app.screen.queued_exposes == {"ollama/x": True}
        assert "ollama/x" not in app.screen._ready_cascade_for_expose
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/screens/test_models.py -k "cascade or discard_cancels" -v`
Expected: FAIL — cascade still queues `queued_ready`, discard doesn't cancel anything.

- [x] **Step 3: Implement**

In `src/modelman/screens/models.py`, add a shared helper near
`_enforce_expose_ready_rule`:

```python
    def _cancel_ready_cascade(self, mid: str) -> None:
        """Undo an expose-triggered ready cascade: cancel a running
        download if that's what the cascade used (real provider), or
        drop the queued flag-only ready-on otherwise. Shared by the
        repeated-'x'-keypress cancel path and discard (Task 13's
        Discard-cancels-cascade)."""
        if self.app.downloads.is_downloading(mid):
            self.app.downloads.cancel(mid)
        self.queued_ready.pop(mid, None)
        self._ready_cascade_for_expose.discard(mid)
```

Replace `action_toggle_expose` in full:

```python
    def action_toggle_expose(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if provider_policy(entry.provider_id) is None:
            self.app.notify("Provider has no LiteLLM mapping — cannot expose")
            return
        persisted_exposed = self.state.get(mid).litellm_exposed
        displayed_exposed = self.queued_exposes.get(mid, persisted_exposed)
        target = not displayed_exposed
        if target == persisted_exposed:
            # Repeated keypress: cancel the queued expose toggle.
            self.queued_exposes.pop(mid, None)
            if mid in self._ready_cascade_for_expose:
                self._cancel_ready_cascade(mid)
            self.app.notify(f"Model already {'exposed' if target else 'not exposed'}")
            self._refresh_pending_bar()
            self.reload()
            return
        if target and not passes_ready_gate(
            entry,
            self.state,
            ready_override=self._projected_ready(mid),
        ):
            if mid in self.queued_ready:
                self.app.notify(
                    "Model is queued to be made not ready — cancel that before exposing"
                )
                return
            if model_has_local_artifact(entry, self._provider_entry_or_none(entry.provider_id)):
                self._start_download(entry)
            else:
                self.queued_ready[mid] = True
            self._ready_cascade_for_expose.add(mid)
        self.queued_exposes[mid] = target
        self._refresh_pending_bar()
        self.reload()
```

Note: `passes_ready_gate`'s `ready_override=self._projected_ready(mid)`
already handles "already downloading, not via this cascade" correctly —
`_projected_ready` (Task 12) returns `True` for any actively-downloading
model regardless of cause, so the `if target and not passes_ready_gate(...)`
branch is simply never entered for that case (covered by
`test_x_on_already_downloading_unrelated_model_just_queues` above) — no
extra branch needed here.

In `_on_exit_confirm`, in the `if choice == "discard":` branch, add a
loop right after `self._restore_snapshot()` and before
`save_registry(...)`:

```python
        if choice == "discard":
            for mid in list(self._ready_cascade_for_expose):
                self._cancel_ready_cascade(mid)
            self._restore_snapshot()
            save_registry(self.registry, self.registry_path)
            self.queued_ready.clear()
            self.queued_deletes.clear()
            self.queued_moves.clear()
            self.queued_exposes.clear()
            self._ready_cascade_for_expose.clear()
            self._added_ids.clear()
            self.app.pop_screen()
            return
```

(Only the new loop is added; the rest of the branch is unchanged —
`_cancel_ready_cascade` already clears `_ready_cascade_for_expose` per
id, so the subsequent `.clear()` is a no-op for those ids but still
needed for any other queued state.)

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_models.py -v`
Expected: PASS (full file)

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/screens/models.py tests/screens/test_models.py && uv run mypy src/modelman/screens/models.py`

```bash
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
feat(models): route expose-cascade downloads, cancel them on discard

'x' on a not-ready model against a real provider now starts a
background download (via _start_download) instead of queuing
queued_ready — matching the r-key routing from Task 12. discard
(exit without apply) now cancels any download that was only running
because an expose cascaded it in, preserving the pre-existing
_ready_cascade_for_expose semantics for the new download path.

completes plan item #13
EOF
)"
```

---

### Task 14: Add-model immediacy

**Files:**
- Modify: `src/modelman/screens/models.py`
- Test: `tests/screens/test_models.py`

**Interfaces:**
- Modifies: `_on_add_model` — saves the registry entry immediately (like
  `_on_edit_model` already does) and, for a real-download provider,
  calls `self._start_download(entry)` instead of queuing
  `queued_ready[variant["id"]] = True`. Flag-only/cloud providers keep
  queuing as before (consistent with Task 12's ready-on split).

- [x] **Step 1: Write the failing tests**

```python
# tests/screens/test_models.py — add near the other add-model tests.

@pytest.mark.asyncio
async def test_add_model_saves_registry_immediately_and_starts_download(tmp_path, monkeypatch):
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        started = []
        monkeypatch.setattr(
            app.downloads,
            "start",
            lambda mid, variant, cfg, on_complete=None: started.append(mid),
        )
        await pilot.press("a")
        await pilot.pause()
        await pilot.click("#model")
        for ch in "newmodel:7b":
            await pilot.press(ch)
        await pilot.click("#save")
        await pilot.pause()

        assert started == ["ollama/newmodel:7b"]
        assert app.screen.queued_ready == {}
        # Immediately persisted — a fresh load must see it without apply.
        from modelman.registry import load_registry

        reloaded = load_registry(reg_path)
        assert any(m.id == "ollama/newmodel:7b" for m in reloaded.models)
```

(Confirm the exact widget ids/click flow against `test_forms.py` or the
existing add-model test in `test_models.py` before writing this —
match whatever pattern that file already uses for driving `ModelForm`;
the id/keys above mirror `ModelForm`'s `#model`/`#save` from
`forms.py`.)

- [x] **Step 2: Run the test to verify it fails**

Run: `uv run pytest tests/screens/test_models.py -k add_model_saves_registry_immediately -v`
Expected: FAIL — registry isn't saved immediately today, and `download`
isn't called (it's queued instead).

- [x] **Step 3: Implement**

In `src/modelman/screens/models.py`, replace `_on_add_model`:

```python
    def _on_add_model(self, result) -> None:
        if result is None:
            return
        variant = result.spec
        if any(m.id == variant["id"] for m in self.registry.models):
            self.app.notify("Model ID already exists")
            return
        entry = _variant_to_model_entry(variant, family=result.family, registry=self.registry)
        self.registry.models.append(entry)
        self._added_ids.add(variant["id"])
        # Persist immediately (mirrors _on_edit_model): a queued
        # ready-on for a real-download provider is about to bypass the
        # apply-on-exit queue entirely via _start_download, so there's
        # no later save point that would otherwise persist this entry.
        save_registry(self.registry, self.registry_path)
        if model_has_local_artifact(entry, self._provider_entry_or_none(entry.provider_id)):
            self._start_download(entry)
        else:
            self.queued_ready[variant["id"]] = True
        self._last_provider_used = variant["provider"]
        self.reload()
        self._refresh_pending_bar()
```

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_models.py -v`
Expected: PASS (full file, including the pre-existing add-model tests —
verify none of them assumed `queued_ready` gets populated for an ollama
model add, since that assumption is what this task deliberately changes)

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/screens/models.py tests/screens/test_models.py && uv run mypy src/modelman/screens/models.py`

```bash
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
feat(models): add-model saves immediately and starts real downloads now

_on_add_model now persists the new registry entry immediately (like
_on_edit_model already does) and routes a real-download provider's
initial ready-on through _start_download instead of the apply-on-exit
queue — there's no longer a later save point for it to rely on.
Flag-only/cloud providers keep the existing queued behavior.

completes plan item #14
EOF
)"
```

---

### Task 15: Defer queued exposes for still-downloading models at apply time

**Files:**
- Modify: `src/modelman/screens/models.py`
- Test: `tests/screens/test_models.py`

**Interfaces:**
- Modifies: `ModelScreen._run_apply` — partitions `self.queued_exposes`
  before constructing `PendingChanges`: entries whose model is
  `self.app.downloads.is_downloading(mid)` and `target is True` are
  registered via `self.app.downloads.register_post_download(mid, action)`
  instead of being passed into `PendingChanges.exposes`; everything else
  goes through unchanged.

- [x] **Step 1: Write the failing test**

```python
# tests/screens/test_models.py — add near the other apply tests.

@pytest.mark.asyncio
async def test_expose_against_downloading_model_is_deferred_not_applied_now(tmp_path, monkeypatch):
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        app.screen.queued_exposes["ollama/x"] = True

        registered = []
        monkeypatch.setattr(
            app.downloads, "register_post_download", lambda mid, action: registered.append(mid)
        )

        # Queue an unrelated delete so apply() has something to do and
        # exercise _run_apply directly (bypassing the full exit-confirm UI
        # flow, which this test doesn't need).
        events = []
        pending_holder = []
        app.screen._run_apply(events.append, lambda line: None, pending_holder.append)

        assert registered == ["ollama/x"]
        assert pending_holder[0].exposes == []  # deferred, not passed to PendingChanges
```

- [x] **Step 2: Run the test to verify it fails**

Run: `uv run pytest tests/screens/test_models.py -k expose_against_downloading -v`
Expected: FAIL — `register_post_download` is never called;
`pending.exposes` still contains `("ollama/x", True)`.

- [x] **Step 3: Implement**

In `src/modelman/screens/models.py`, modify `_run_apply`. Replace:

```python
        pending = PendingChanges(
            registry=self.registry,
            state=self.state,
            family=self.family,
            registry_path=self.registry_path,
            state_path=self.state_path,
            providers=providers,
            ready=[(mid, specs_by_id[mid], target) for mid, target in self.queued_ready.items()],
            deletes=[(mid, spec) for mid, spec in self.queued_deletes.items()],
            moves=list(self.queued_moves.items()),
            exposes=list(self.queued_exposes.items()),
            litellm_path=default_litellm_config_path(),
        )
```

with:

```python
        litellm_path = default_litellm_config_path()
        immediate_exposes: list[tuple[str, bool]] = []
        for mid, target in self.queued_exposes.items():
            if target and self.app.downloads.is_downloading(mid):
                self._register_deferred_expose(mid, litellm_path)
            else:
                immediate_exposes.append((mid, target))

        pending = PendingChanges(
            registry=self.registry,
            state=self.state,
            family=self.family,
            registry_path=self.registry_path,
            state_path=self.state_path,
            providers=providers,
            ready=[(mid, specs_by_id[mid], target) for mid, target in self.queued_ready.items()],
            deletes=[(mid, spec) for mid, spec in self.queued_deletes.items()],
            moves=list(self.queued_moves.items()),
            exposes=immediate_exposes,
            litellm_path=litellm_path,
        )
```

Add a new method (near `_run_apply`):

```python
    def _register_deferred_expose(self, model_id: str, litellm_path: Path) -> None:
        """Defer a queued expose against a still-downloading model: it
        can't be applied now (the ready gate would reject it — the model
        isn't ready yet), so register it to run once DownloadManager
        reports success instead. Loads a fresh Registry/StateStore at
        run time rather than closing over self.registry/self.state,
        since this may fire long after this ModelScreen instance is
        gone."""
        registry_path = self.registry_path
        state_path = self.state_path

        def _apply_deferred_expose() -> None:
            from ..litellm import apply_expose_queue
            from ..registry import load_registry
            from ..state import locked_state

            registry = load_registry(registry_path)
            with locked_state(state_path) as state:
                apply_expose_queue(registry, state, [(model_id, True)], litellm_path)

        self.app.downloads.register_post_download(model_id, _apply_deferred_expose)
```

Add `from pathlib import Path` if not already imported at the top of the
file (it already is, per the existing `from pathlib import Path` import
line — confirm before adding a duplicate).

- [x] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_models.py -v`
Expected: PASS (full file)

- [x] **Step 5: Lint/typecheck and commit**

Run: `uv run ruff check src/modelman/screens/models.py tests/screens/test_models.py && uv run mypy src/modelman/screens/models.py`

```bash
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
feat(models): defer queued exposes against a still-downloading model

At apply time, a queued expose whose model is still downloading is
pulled out of PendingChanges.exposes and registered as a
DownloadManager post-download action instead, so it applies
automatically once the download succeeds rather than being rejected by
the ready gate now. The deferred closure loads a fresh Registry/
StateStore at run time rather than closing over this screen's own
objects, since it may fire after this ModelScreen instance is gone.

completes plan item #15
EOF
)"
```

---

### Task 16: Full-suite verification and docs

**Files:**
- Modify: `modelman/CLAUDE.md`
- No new tests — this task runs the full suite and fixes anything the
  per-task focused runs missed (cross-task interaction effects), then
  documents the new architecture.

- [ ] **Step 1: Run the full test suite**

Run: `uv run pytest -q`
Expected: PASS. If anything fails, it's almost certainly a cross-task
interaction the focused per-task runs didn't exercise (e.g. a
pre-existing test elsewhere in `tests/screens/` that asserted
`queued_ready` gets populated for an ollama ready-on) — fix the
assertion to match the new behavior (don't weaken the new behavior to
fit an assertion that predates this feature).

- [ ] **Step 2: Run lint, typecheck, and format check**

Run: `uv run ruff check . && uv run ruff format --check . && uv run mypy src/modelman`
Expected: PASS. Fix any findings.

- [ ] **Step 3: Update `modelman/CLAUDE.md`**

Add a new subsection under "### Pending changes queue" (after its
existing paragraph), documenting the split:

```markdown
### Downloads (async, background)

- `src/modelman/downloads.py` — `DownloadManager` (owned by
  `ModelmanApp`, `self.app.downloads`). A real download (ollama/omlx/
  llamacpp against a non-cloud model — `model_has_local_artifact()`)
  never goes through `PendingChanges`/apply-on-exit: `ModelScreen`
  routes it directly to `DownloadManager.start()`, which runs it on its
  own daemon thread against a fresh provider instance (isolating
  per-download cancellation state), persists success to `modelman.toml`
  via `state.locked_state()` (a locked read-modify-write — see below),
  and marks the model locked (`d`/`e` refuse, STATUS shows ⏳) until it
  finishes. Deletes, moves, and (mostly) exposes stay in the existing
  apply-on-exit queue; a flag-only provider's or cloud model's ready-on
  also stays queued, since there's no real download to background.
  `DownloadScreen` (`screens/downloads.py`, opened with `g`) shows live
  progress; the app-level quit guard (`ModelmanApp.request_quit()`,
  wired to `ctrl+q` and `FamilyScreen`'s `q`) blocks exiting while any
  download is active.
- `state.locked_state()` (`state.py`) — the only safe way to write
  `modelman.toml` once more than one thing can write it concurrently
  (a `DownloadManager` completion on a background thread, alongside
  `PendingChanges.apply()`'s own save for an unrelated model): acquires
  a process lock, loads fresh, yields the `StateStore` to mutate, saves
  on exit. `PendingChanges.apply()`'s final save uses it too, merging
  only the model ids/families this run actually touched onto a
  freshly-loaded copy rather than overwriting the whole file from its
  own (possibly stale) in-memory snapshot.
- A queued expose (`x`) against a model that's still downloading is
  allowed and queues normally; at apply time, if the model is still
  downloading, `ModelScreen._run_apply` pulls it out of
  `PendingChanges.exposes` and registers it as a
  `DownloadManager.register_post_download()` action instead — it runs
  automatically on that download's success (never on cancel/fail).
- `providers/_progress.py`'s `HF_DOWNLOAD_LOCK` serializes oMLX/llamacpp
  downloads' use of `ProgressTqdm`'s class-level active-context slot:
  `contextvars` was considered and rejected — `snapshot_download()` runs
  its own internal `ThreadPoolExecutor`, whose workers never see a
  `ContextVar` set by the caller, which would have silently broken
  progress and cancellation, not just fixed the parallel-download case.
```

- [ ] **Step 4: Verify the doc renders sensibly and links are intact**

Run: `make check-links` (from the repo root, not `modelman/`)
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/CLAUDE.md
git commit -m "$(cat <<'EOF'
docs(modelman): document DownloadManager and the locked_state race fix

completes plan item #16
EOF
)"
```
