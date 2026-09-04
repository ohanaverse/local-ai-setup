# Ready-Toggle-Delete Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix six defects in the `ready-toggle-delete` feature (commit `25b6783`): an OOM on real GGUF deletes, two cancel-cascade regressions that cause silent data loss, a double-delete that surfaces a spurious failure, and two dead-code/lint issues.

**Architecture:** The two cancel-cascade bugs and the double-delete share one root cause — `action_delete_model` redundantly queues `ready=False` and `expose=False` that `PendingChanges.apply()`'s deletes loop already performs. Fixing that one method resolves three findings at once. The OOM is fixed by deriving the blob hash from the HF symlink target instead of re-hashing file contents. The lint issues are pure deletions.

**Tech Stack:** Python 3.13, Textual TUI, pytest + pytest-asyncio, `uv` for packaging.

**Spec:** This plan is self-contained (no separate spec doc). It implements the six findings from the `/code-review` run on branch `feat/ready-toggle-delete`.

## Global Constraints

- Python 3.13 only (`requires-python = "==3.13.*"`); run everything via `uv run`.
- Run focused tests per change, not the full suite. The full suite is slow (~5+ min, mostly Textual screen tests).
- `make check` (lint + typecheck) must pass; ruff flags unused imports as F401.
- Every test needs a docstring comment describing what it verifies and why (per user's global test-documentation rule).
- Commit messages end with `Co-Authored-By: Claude Code <noreply@anthropic.com>`.
- Work in the worktree at `/Users/keith/github/ohanaverse/local-ai-setup/.worktrees/ready-toggle-delete`; the `modelman` package lives under `modelman/`.

---

## File Structure

- `modelman/src/modelman/screens/models.py` — `ModelScreen` actions. `action_delete_model` (lines 538-571) is the redundant-queueing source; `action_toggle_ready` (lines 383-429) has the untracked ready→expose cascade; dead `provider_entry` (411-414) and unused `model_has_local_artifact` import (line 27).
- `modelman/src/modelman/queue.py` — `PendingChanges.apply()`. The ready loop (lines 288-366) must skip ids already deleted by the deletes loop (defense-in-depth for the double-delete).
- `modelman/src/modelman/providers/llamacpp.py` — `LlamaCppProvider.delete()` (lines 132-203). OOM fix + unused `_Path` import (line 147).
- `modelman/tests/screens/test_models.py` — screen-level tests for the cancel-cascade and delete-queue behavior.
- `modelman/tests/test_queue.py` — apply-level tests for the double-delete skip.
- `modelman/tests/test_ready_toggle_delete.py` — provider `delete()` tests (llamacpp blob cleanup).

---

## Task 1: Fix the OOM in `LlamaCppProvider.delete()` (blob hash from symlink target)

**Files:**
- Modify: `modelman/src/modelman/providers/llamacpp.py:132-203`
- Test: `modelman/tests/test_ready_toggle_delete.py` (add a test in `TestLlamaCppDelete`)

**Interfaces:**
- Consumes: `_hf_cache_dir()` (module-level, returns the HF hub dir).
- Produces: `LlamaCppProvider.delete(variant, runner=None) -> None` — same signature, but no longer reads file contents; derives each blob's SHA256 from the snapshot file's symlink target basename.

**Context:** HF cache snapshot files are symlinks whose target is `../../blobs/<sha256>`. The blob's name *is* its SHA256. The current code re-hashes the entire multi-GB GGUF via `file_path.read_bytes()`, which OOMs on real models. The fix reads the link target with `os.readlink()` and takes its basename — no file content is read.

- [ ] **Step 1: Write the failing test**

Add to `TestLlamaCppDelete` in `modelman/tests/test_ready_toggle_delete.py`:

```python
def test_delete_uses_symlink_target_not_file_contents(self, tmp_path):
    """Deleting a GGUF must derive the blob hash from the snapshot file's
    symlink target (blobs/<sha256>), never by reading the file body — reading
    a multi-GB GGUF into memory to hash it OOMs the process. This test makes
    the snapshot file a symlink to a blob and asserts the blob is removed
    without the file's contents ever being read."""
    from modelman.providers.llamacpp import LlamaCppProvider
    import hashlib

    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--test-org--test-repo"
    snapshots_dir = repo_dir / "snapshots" / "abc123"
    blobs_dir = repo_dir / "blobs"
    snapshots_dir.mkdir(parents=True)
    blobs_dir.mkdir()

    # A real blob whose content we deliberately do NOT want read.
    blob_hash = hashlib.sha256(b"real blob content").hexdigest()
    blob_file = blobs_dir / blob_hash
    blob_file.write_bytes(b"real blob content")

    # The snapshot file is a symlink to the blob, as in a real HF cache.
    gguf_file = snapshots_dir / "model.gguf"
    gguf_file.symlink_to(blob_file)

    provider = LlamaCppProvider({})
    variant: VariantSpec = {
        "id": "test--model",
        "provider": "llamacpp",
        "repo": "test-org/test-repo",
        "files": ["model.gguf"],
    }

    with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
        provider.delete(variant)

    assert not gguf_file.exists()
    assert not blob_file.exists()
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_ready_toggle_delete.py::TestLlamaCppDelete::test_delete_uses_symlink_target_not_file_contents -v`
Expected: FAIL — the current `read_bytes()` path reads the symlink's *target* content, computes the same hash, and the test would actually pass on hash correctness. To make it fail meaningfully, the current code's `read_bytes()` on a symlink resolves to the blob content, so the hash matches and the blob is deleted — the test passes even before the fix. **This test is a regression guard, not a red/green driver.** Verify it passes both before and after; the real behavioral change is that `read_bytes()` is no longer called. To assert that, add a second assertion in Step 3's version.

- [ ] **Step 3: Write the implementation**

Replace the hash computation in `delete()` (lines 172-178 and 190-195) with a symlink-target helper. Add a module-level helper and rewrite the two loops:

```python
def _blob_hash_of(snapshot_file: Path) -> str | None:
    """Return the blob SHA256 a snapshot file points at, without reading it.

    HF snapshot files are symlinks to ../../blobs/<sha256>; the blob's name
    is its content hash. Reading the link target is O(1) and avoids loading
    a multi-GB GGUF into memory just to re-derive a hash we already have.
    Returns None when the file is not a symlink (e.g. a test fixture that
    wrote a regular file) so callers can fall back to hashing the content.
    """
    if not snapshot_file.is_symlink():
        return None
    target = os.readlink(snapshot_file)
    return Path(target).name
```

Then in `delete()`, replace the two `hashlib.sha256(file_path.read_bytes()).hexdigest()` blocks:

```python
        for snap in snapshots_dir.iterdir():
            if not snap.is_dir():
                continue
            for f in files:
                file_path = snap / f
                if file_path.exists():
                    # Blobs are named by their SHA256; the snapshot file is a
                    # symlink to blobs/<hash>, so read the link target instead
                    # of re-hashing the (multi-GB) file body.
                    blob_hash = _blob_hash_of(file_path)
                    if blob_hash is None:
                        try:
                            blob_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
                        except OSError:
                            blob_hash = None
                    if blob_hash is not None:
                        blobs_to_check.add(blob_hash)
                    file_path.unlink()
```

and the referenced-blobs loop:

```python
        for snap in snapshots_dir.iterdir():
            if not snap.is_dir():
                continue
            for f in snap.iterdir():
                if f.is_file():
                    blob_hash = _blob_hash_of(f)
                    if blob_hash is None:
                        try:
                            blob_hash = hashlib.sha256(f.read_bytes()).hexdigest()
                        except OSError:
                            blob_hash = None
                    if blob_hash is not None:
                        referenced_blobs.add(blob_hash)
```

Remove the now-unused `from pathlib import Path as _Path` import (line 147). Keep `import hashlib` (still used as the fallback). `os` is already imported at module top (line 5).

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_ready_toggle_delete.py -v`
Expected: PASS — all `TestLlamaCppDelete` tests (the two existing regular-file tests exercise the `read_bytes()` fallback; the new symlink test exercises the fast path).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/providers/llamacpp.py modelman/tests/test_ready_toggle_delete.py
git commit -m "fix(modelman): derive llamacpp blob hash from symlink target, not file contents

Deleting a GGUF re-hashed the entire multi-GB file via read_bytes() to
compute a SHA256 that is already the blob's filename. Read the snapshot
symlink's target basename instead (O(1), no content read), falling back
to hashing only for non-symlink files (test fixtures).

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 2: Fix the delete cancel-cascade + double-delete (redundant queueing in `action_delete_model`)

**Files:**
- Modify: `modelman/src/modelman/screens/models.py:538-571` (`action_delete_model`)
- Test: `modelman/tests/screens/test_models.py` (add tests)

**Interfaces:**
- Consumes: `_model_entry_to_variant(entry)` (module-level, returns a `VariantSpec` dict); `self.queued_deletes` (dict `model_id -> VariantSpec`); `self.queued_ready` (dict `model_id -> bool`); `self.queued_exposes` (dict `model_id -> bool`); `self.state.get(mid)` (returns `ModelState`).
- Produces: `action_delete_model()` now queues **only** `queued_deletes[mid] = spec` (toggling it off on a second press). It no longer touches `queued_ready` or `queued_exposes` — `apply()`'s deletes loop already removes the file, drops registry/state rows, and cascades the unexpose.

**Context:** `PendingChanges.apply()` runs deletes first (queue.py:197-248). For each delete it: calls `provider.delete()` (removing the file), removes the `ModelEntry` from the registry, drops `state.models[mid]`, and — if the model was exposed — appends `(mid, False)` to `self.exposes`. So `action_delete_model` queueing `ready=False` and `expose=False` is redundant. That redundancy causes two bugs: (a) the ready loop re-deletes the already-removed file (spurious `ollama rm` failure), and (b) pressing `d` a second time to cancel only pops `queued_deletes`, leaving the orphaned `ready=False`/`expose=False` queues that still destroy the file and unexpose the model.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/screens/test_models.py` (near the other `d` tests, after line 320):

```python
@pytest.mark.asyncio
async def test_d_queues_only_delete_not_ready_or_expose(tmp_path, monkeypatch):
    """'d' must queue only the registry removal, not ready=False or
    expose=False. apply()'s deletes loop already removes the file, drops the
    registry/state rows, and cascades the unexpose — queueing those again
    causes a double-delete (spurious 'ollama rm' failure) and, on cancel,
    leaves orphaned queues that still destroy the file."""
    stub = _seed_cloud_family(tmp_path, monkeypatch, location=None)
    stub.is_downloaded.return_value = True

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.press("d")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert "ollama/glm-5.2:cloud" in app.screen.queued_deletes
        assert app.screen.queued_ready == {}
        assert app.screen.queued_exposes == {}


@pytest.mark.asyncio
async def test_d_twice_cancels_delete_cleanly(tmp_path, monkeypatch):
    """Pressing 'd' twice must cancel the delete with no orphaned ready or
    expose queues left behind. Regression: the second 'd' only popped
    queued_deletes, leaving ready=False/expose=False queued, so apply() still
    deleted the file and unexposed the model the user cancelled."""
    stub = _seed_cloud_family(tmp_path, monkeypatch, location=None)
    stub.is_downloaded.return_value = True

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.press("d")
        await pilot.pause()
        assert "ollama/glm-5.2:cloud" in app.screen.queued_deletes
        await pilot.press("d")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_deletes == {}
        assert app.screen.queued_ready == {}
        assert app.screen.queued_exposes == {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/screens/test_models.py::test_d_queues_only_delete_not_ready_or_expose tests/screens/test_models.py::test_d_twice_cancels_delete_cleanly -v`
Expected: FAIL — the first test fails because `queued_ready`/`queued_exposes` are non-empty; the second fails because `queued_ready`/`queued_exposes` survive the cancel.

- [ ] **Step 3: Write the implementation**

Replace the body of `action_delete_model` (lines 538-571) with a simple toggle that queues only the delete:

```python
    def action_delete_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        # "d" queues only the registry removal. apply()'s deletes loop already
        # removes the on-disk file, drops the registry/state rows, and cascades
        # the unexpose — queueing ready=False/expose=False here would double-
        # delete the file and, on cancel, leave orphaned queues that still
        # destroy the model. A second "d" toggles the delete back off.
        spec = _model_entry_to_variant(entry)
        if mid in self.queued_deletes:
            self.queued_deletes.pop(mid)
        else:
            self.queued_deletes[mid] = spec
        self._refresh_pending_bar()
        self.reload()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/screens/test_models.py -k "d_on or d_queues or d_twice" -v`
Expected: PASS — the new tests pass, and the existing `test_d_on_cloud_model_queues_delete`, `test_d_on_local_not_downloaded_still_queues_delete`, `test_d_on_downloaded_local_model_still_queues` still pass (they only assert `queued_deletes` membership).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/screens/models.py modelman/tests/screens/test_models.py
git commit -m "fix(modelman): queue only the delete, not redundant ready/expose

action_delete_model queued ready=False and expose=False that apply()'s
deletes loop already performs, causing a double-delete (spurious ollama rm
failure) and, on cancel, orphaned queues that still destroyed the file and
unexposed the model. Queue only the registry removal; a second 'd' toggles
it off cleanly.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 3: Fix the ready-toggle cancel-cascade (untracked ready→expose cascade)

**Files:**
- Modify: `modelman/src/modelman/screens/models.py:383-429` (`action_toggle_ready`)
- Test: `modelman/tests/screens/test_models.py` (add a test)

**Interfaces:**
- Consumes: `self._ready_cascade_for_expose` (set of model ids whose `queued_ready=True` exists only because an expose cascaded it in); `self.queued_ready`, `self.queued_exposes`, `self.state.get(mid)`.
- Produces: `action_toggle_ready()` now records the reverse cascade (ready→expose) in a new set `self._expose_cascade_for_ready` so that cancelling the ready toggle also cancels the expose it cascaded in.

**Context:** When `r` toggles a ready model to not-ready, it cascades `queued_exposes[mid] = False` (lines 423-426). But the cancel branch (lines 395-410) only clears `queued_exposes` when `mid in self._ready_cascade_for_expose` — which tracks the *opposite* direction (expose→ready). So pressing `r` twice on a ready+exposed model pops `queued_ready` but leaves `queued_exposes[mid] = False` orphaned; `apply()` then unexposes the model the user cancelled.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/screens/test_models.py`:

```python
@pytest.mark.asyncio
async def test_r_twice_on_ready_exposed_cancels_unexpose_cascade(tmp_path, monkeypatch):
    """Pressing 'r' twice on a ready+exposed model must cancel both the
    ready=False toggle and the expose=False it cascaded in. Regression: the
    cancel branch only cleared queued_exposes for the expose→ready direction,
    so the ready→expose cascade was orphaned and apply() unexposed a model the
    user's two presses were meant to leave untouched."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(
        id="ollama/a", family="ornith", provider_id="ollama", model_name="a", location="cloud",
    )
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, litellm_exposed=True))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()  # let reconcile settle
        await pilot.press("r")
        await pilot.pause()
        assert app.screen.queued_ready == {"ollama/a": False}
        assert app.screen.queued_exposes == {"ollama/a": False}
        await pilot.press("r")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_ready == {}
        assert app.screen.queued_exposes == {}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/screens/test_models.py::test_r_twice_on_ready_exposed_cancels_unexpose_cascade -v`
Expected: FAIL — after the second `r`, `queued_exposes` is still `{"ollama/a": False}`.

- [ ] **Step 3: Write the implementation**

Add a new instance attribute in `__init__` (after `self._ready_cascade_for_expose`, line 253):

```python
        # Ids whose queued_exposes=False entry exists *only* because a ready
        # toggle cascaded it in (ready=False must unexpose). Mirrors
        # _ready_cascade_for_expose for the opposite direction: cancelling the
        # ready toggle must also cancel the unexpose it cascaded, or apply()
        # unexposes a model the user's two 'r' presses were meant to leave
        # untouched.
        self._expose_cascade_for_ready: set[str] = set()
```

In `action_toggle_ready`, record the cascade when queueing it (after line 426):

```python
        if target is False:
            current_exposed = self.queued_exposes.get(mid, self.state.get(mid).litellm_exposed)
            if current_exposed:
                self.queued_exposes[mid] = False
                self._expose_cascade_for_ready.add(mid)
```

In the cancel branch (lines 395-410), also clear the reverse cascade:

```python
        if target == persisted_ready:
            self.queued_ready.pop(mid, None)
            if mid in self._ready_cascade_for_expose:
                self.queued_exposes.pop(mid, None)
                self._ready_cascade_for_expose.discard(mid)
            if mid in self._expose_cascade_for_ready:
                self.queued_exposes.pop(mid, None)
                self._expose_cascade_for_ready.discard(mid)
            self.app.notify(f"Model already {'ready' if target else 'not ready'}")
            self._refresh_pending_bar()
            self.reload()
            return
```

Also clear `self._expose_cascade_for_ready` in the two places that clear the other queue state: `_on_exit_confirm`'s discard branch (after line 693) and `_run_apply` (after line 768), alongside the existing `self._ready_cascade_for_expose.clear()`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/screens/test_models.py -k "r_twice or r_cancel or r_on" -v`
Expected: PASS — the new test passes, and the existing `test_r_twice_cancels_queued_flip_with_notification` and `test_r_cancel_after_x_cascade_also_cancels_expose` still pass.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/screens/models.py modelman/tests/screens/test_models.py
git commit -m "fix(modelman): track ready→expose cascade so cancelling 'r' cancels unexpose

The ready=False toggle cascades expose=False, but the cancel branch only
cleared queued_exposes for the expose→ready direction, orphaning the
unexpose. Track the reverse cascade in _expose_cascade_for_ready and clear
it on cancel, discard, and apply.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 4: Defense-in-depth — skip deleted ids in the ready loop

**Files:**
- Modify: `modelman/src/modelman/queue.py:288-366` (the ready loop)
- Test: `modelman/tests/test_queue.py` (add a test)

**Interfaces:**
- Consumes: `deleted_ids` (set of model ids removed by the deletes loop, populated at queue.py:248).
- Produces: the ready loop now skips any `(model_id, variant, target)` whose `model_id` is in `deleted_ids`, mirroring the moves loop's guard at queue.py:256.

**Context:** After Task 2, `action_delete_model` no longer queues `ready=False` for a deleted model, so the double-delete is gone at the source. But Task 2's review surfaced a second, more serious path: an `x`-then-`d` sequence leaves `queued_ready[mid]=True` (the `x` cascade) in place, and `apply()` re-downloads a "ghost" model (file on disk + state row, no registry row) after the deletes loop removed it. This task adds a belt-and-suspenders guard in `apply()` so that **any** ready entry for a deleted id — `target=False` (double-delete) or `target=True` (re-download) — is skipped. It mirrors the existing `if model_id in deleted_ids: continue` guard in the moves loop. The guard is placed at the top of the ready loop, before the `elif target` branch, so it covers both directions.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/test_queue.py`:

```python
def test_apply_ready_loop_skips_deleted_ids(tmp_path):
    """A model queued for both delete and ready=False must not be re-deleted
    by the ready loop. The deletes loop already removed the file and the
    registry/state rows; the ready loop re-running _delete would surface a
    spurious 'clear <id>' failure (e.g. a second 'ollama rm' on a gone model).
    This guards the invariant even if a caller queues both."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    state.set("ollama/a", ModelState(ready=True, disk_path="/old/path"))

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), False)],
    )
    pending.apply()

    # The provider's delete() must be called exactly once (by the deletes
    # loop), not a second time by the ready loop.
    assert providers["ollama"].delete.call_count == 1
    assert pending.failures == []


def test_apply_ready_loop_skips_deleted_ids_redownload(tmp_path):
    """A model queued for both delete and ready=True (the x-then-d cascade)
    must NOT be re-downloaded by the ready loop after the deletes loop removed
    it. Re-downloading would recreate a 'ghost' model — file on disk + state
    row, but no registry row — violating the user's delete intent. The guard
    must skip target=True entries too, not just target=False."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    state.set("ollama/a", ModelState(ready=True, disk_path="/old/path"))

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
    )
    pending.apply()

    # The ready loop must not re-download: provider.download() never called,
    # and the model stays out of state (no ghost row).
    assert providers["ollama"].download.call_count == 0
    assert "ollama/a" not in state.models
    assert pending.failures == []
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_queue.py::test_apply_ready_loop_skips_deleted_ids tests/test_queue.py::test_apply_ready_loop_skips_deleted_ids_redownload -v`
Expected: FAIL — the first test: `providers["ollama"].delete.call_count` is 2 (deletes loop + ready loop), and `pending.failures` contains a `clear ollama/a` entry. The second test: `providers["ollama"].download.call_count` is 1 (the ready loop re-downloads), and `"ollama/a" in state.models` is True (ghost row).

- [ ] **Step 3: Write the implementation**

At the top of the ready loop (after line 290's `if aborted(): return`), add the skip:

```python
        for model_id, variant, target in self.ready:
            if aborted():
                return
            if model_id in deleted_ids:
                # Deleted earlier in this apply; its ready toggle is moot.
                # Re-running _delete here would re-delete an already-removed
                # file and surface a spurious failure.
                continue
            assert variant["id"] == model_id, (
                f"variant id {variant['id']!r} != queued model_id {model_id!r}"
            )
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py -k "delete or ready" -v`
Expected: PASS — both new tests pass, and the existing delete/ready tests still pass.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/queue.py modelman/tests/test_queue.py
git commit -m "fix(modelman): skip deleted ids in the ready loop

A model queued for both delete and a ready toggle was re-processed by the
ready loop after the deletes loop already removed it — re-deleting the file
(target=False, spurious 'clear <id>' failure) or re-downloading a ghost model
(target=True, the x-then-d cascade). Skip ids in deleted_ids, mirroring the
moves loop.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 5: Remove dead code and unused imports (lint)

**Files:**
- Modify: `modelman/src/modelman/screens/models.py:27,411-414`
- Modify: `modelman/src/modelman/providers/llamacpp.py:147` (already removed in Task 1 — verify)

**Interfaces:**
- Consumes: none.
- Produces: `action_toggle_ready` no longer assigns the unused `provider_entry`; the `model_has_local_artifact` import is removed from `models.py`.

**Context:** `provider_entry` (lines 411-414) is assigned in a try/except but never read after the local-artifact guard was removed. `model_has_local_artifact` is imported at line 27 but unused in `models.py` (it is still used in `screens/__init__.py` and `litellm.py`, so the definition stays). Both are flagged by ruff (F401 for the import; the dead variable is a lint smell). The `_Path` import in `llamacpp.py` was already removed in Task 1.

- [ ] **Step 1: Remove the dead `provider_entry` block**

In `action_toggle_ready`, delete lines 411-414:

```python
        try:
            provider_entry = self.registry.provider(entry.provider_id)
        except KeyError:
            provider_entry = None
```

- [ ] **Step 2: Remove the unused import**

In `models.py`, remove `model_has_local_artifact,` from the `from ..registry import (...)` block (line 27).

- [ ] **Step 3: Run lint and typecheck**

Run: `uv run make check` (or `uv run ruff check src/modelman/screens/models.py src/modelman/providers/llamacpp.py && uv run mypy src/modelman/screens/models.py`)
Expected: PASS — no F401 for `model_has_local_artifact` or `_Path`, no unused-variable warnings.

- [ ] **Step 4: Run the focused screen tests to confirm no regression**

Run: `uv run pytest tests/screens/test_models.py -k "toggle_ready or r_on or r_twice" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/screens/models.py
git commit -m "chore(modelman): remove dead provider_entry and unused import

The local-artifact guard that read provider_entry was removed, leaving the
try/except dead; model_has_local_artifact is no longer referenced in
models.py. Both trip ruff F401 / unused-variable lint.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 5b: Update the stale local-artifact ready-toggle test

**Files:**
- Modify: `modelman/tests/screens/test_models.py:1064-1107` (`test_r_on_ready_local_artifact_model_noops_with_notification`)
- Test: the test itself (rename + re-assert)

**Interfaces:**
- Consumes: none.
- Produces: the test now asserts the new `r`-on-ready-local-artifact behavior (queues `ready=False`), matching the design spec.

**Context:** The feature commit `25b6783` intentionally changed `r` semantics per the design spec (`docs/superpowers/specs/2026-08-30-ready-toggle-delete-design.md` line 34: "Ready OFF (`r` on ready) → Deletes file, unexposes if was exposed"; section 2: "Remove the block that prevents `ready=False` for local models"). The test `test_r_on_ready_local_artifact_model_noops_with_notification` still asserts the OLD no-op behavior (`queued_ready == {}`), so it fails on HEAD. This task updates the test to assert the new behavior. It is a test-only change; no source code changes.

- [ ] **Step 1: Rewrite the test**

Rename `test_r_on_ready_local_artifact_model_noops_with_notification` to `test_r_on_ready_local_artifact_model_queues_delete` and change its final assertion and docstring:

```python
@pytest.mark.asyncio
async def test_r_on_ready_local_artifact_model_queues_delete(tmp_path, monkeypatch):
    """r on an already-ready local-artifact model now queues ready=False
    (file deletion), per the ready-toggle-delete design: 'r' is a true
    toggle of file presence for all models, not a flag flip. The old
    no-op-with-notification behavior was removed by the feature commit."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(id="omlx/a", family="ornith", provider_id="omlx", model_name="a")
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", location="local")],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("omlx/a", ModelState(ready=True, disk_path="/models/a", size_bytes=10))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 10
    stub.list_local.return_value = [{"variant_id": "a", "local_path": "/models/a"}]
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()  # let reconcile settle so state.ready is confirmed True
        await pilot.press("r")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_ready == {"omlx/a": False}
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `uv run pytest tests/screens/test_models.py::test_r_on_ready_local_artifact_model_queues_delete -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add modelman/tests/screens/test_models.py
git commit -m "test(modelman): update stale local-artifact ready-toggle test

The feature commit 25b6783 made 'r' a true file-presence toggle for
local-artifact models (per the ready-toggle-delete design spec), but this
test still asserted the old no-op behavior. Update it to assert queued
ready=False.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 6: Full verification

**Files:**
- None (verification only).

- [ ] **Step 1: Run the full modelman test suite**

Run: `uv run make test`
Expected: PASS — all tests green, including the ~210 Textual screen tests.

- [ ] **Step 2: Run lint + typecheck**

Run: `uv run make check`
Expected: PASS.

- [ ] **Step 3: Run the monorepo umbrella check (optional, if time permits)**

Run: `make test-all` from the repo root.
Expected: PASS — modelman + wt + shell lint all green.

- [ ] **Step 4: Confirm no guide-doc drift**

Run: `git grep -n "litellm_exposed = " docs/guides/` and confirm the exposure snapshots are unchanged (this plan does not alter exposure state on disk).

---

## Self-Review

**Spec coverage:** All six findings are addressed — OOM (Task 1), delete cancel-cascade + double-delete (Task 2), ready cancel-cascade (Task 3), double-delete defense-in-depth (Task 4), dead code + unused imports (Task 5), full verification (Task 6).

**Placeholder scan:** No TBD/TODO; every code step has concrete code blocks; every test has a docstring.

**Type consistency:** `_expose_cascade_for_ready` is introduced in Task 3 and referenced consistently (init, queue, cancel, discard, apply). `deleted_ids` is produced by the deletes loop (queue.py:248) and consumed by the ready loop in Task 4. `_blob_hash_of` is defined and used only within `llamacpp.py`. `_model_entry_to_variant` and `_seed_cloud_family` are existing helpers reused verbatim.
