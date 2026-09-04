# Code-Review Round 2 Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all 10 findings from the 2026-09-04 `/code-review` run on `feat/ready-toggle-delete`: two silent data-loss paths (llamacpp blob scan, omlx basename rmtree), a multi-GB OOM, four apply/screen correctness bugs, a false comment, and the two-layer cascade architecture that caused the interleaving bugs.

**Architecture:** Two user-approved structural decisions shape this plan. (1) The ready→expose cascade collapses to a single screen-level rule — "expose depends on *projected* ready" — enforced via one helper at every queue mutation; the mirrored `_expose_cascade_for_ready` set and the screen's phantom unexpose queueing are deleted (`apply()` already owns the real rule and re-derives the unexpose). (2) The omlx shared-directory data loss is fixed provider-neutrally in `apply()`: before any artifact removal, check the registry for another entry whose `path_of()` resolves to the same on-disk target; if found, keep the file and surface why.

**Tech Stack:** Python 3.13, Textual TUI, pytest + pytest-asyncio, `uv` for packaging.

**Spec:** This plan is self-contained. It implements the 10 findings reported by the `/code-review` run on 2026-09-04 (level: high), plus four cut items the reviewer noted (test docstrings, a tautological test, test placement, stale `modelman/CLAUDE.md`).

## Global Constraints

- Python 3.13 only (`requires-python = "==3.13.*"`); run everything via `uv run` from `modelman/`.
- Run focused tests per change, not the full suite. The full suite is slow (~5+ min, mostly Textual screen tests); run it once in the final task.
- `make check` (lint + typecheck) must pass. Ruff flags unused imports (F401) and unused variables.
- Every test needs a docstring comment describing what it verifies and why (user's global test-documentation rule).
- Commit messages end with `Co-Authored-By: Claude Code <noreply@anthropic.com>` and reference the plan task they complete, e.g. `fix(modelman): … — completes plan item Task 3`.
- Work in the worktree at `/Users/keith/github/ohanaverse/local-ai-setup/.worktrees/ready-toggle-delete`; all paths below are relative to `modelman/` unless they start with `docs/`.
- `tests/test_queue.py` + `tests/test_expose.py` together take ~2.5 min — use a longer timeout or run in background.

---

## File Structure

- `src/modelman/providers/llamacpp.py` — `delete()` blob-cleanup: chunked hashing, recursive reference scan, dangling-symlink handling (Task 1).
- `src/modelman/registry.py` — gains `model_entry_to_variant()` moved from `screens/models.py` (Task 2).
- `src/modelman/queue.py` — `apply()`: shared-artifact guard (Task 2), ready-loop `is_downloaded()` guard (Task 3), attempted-deletes skip (Task 4), flag-only artifact removal (Task 5).
- `src/modelman/screens/models.py` — the single-rule cascade rework (Task 6); also drops its local copy of the variant converter (Task 2).
- `tests/test_queue.py` — apply-level tests for Tasks 2–5.
- `tests/screens/test_models.py` — screen-level cascade tests for Task 6.
- `tests/test_providers/test_llamacpp.py`, `tests/test_providers/test_omlx.py` — provider delete tests move here from `tests/test_ready_toggle_delete.py` (Tasks 1 and 7).
- `modelman/CLAUDE.md` — stale `'r'`-no-op text and the cascade bookkeeping paragraph (Task 7).

---

## Task 1: llamacpp `delete()` — chunked hashing, recursive blob scan, dangling symlinks

Fixes findings 1 (non-recursive scan deletes referenced blobs), 2 (`read_bytes()` OOM fallback), and 9 (`exists()` misses dangling symlinks).

**Files:**
- Modify: `src/modelman/providers/llamacpp.py`
- Test: `tests/test_ready_toggle_delete.py` (moves to `tests/test_providers/test_llamacpp.py` in Task 7 — write the tests there now to avoid moving them twice)

**Interfaces:**
- Consumes: `_hf_cache_dir()`, `_blob_hash_of(snapshot_file) -> str | None` (both already exist at module level).
- Produces: `_hash_file(path: Path) -> str` (module-level; SHA256 in 1MB chunks). `delete()` behavior: never calls `read_bytes()`; the reference scan walks snapshots recursively; dangling symlinks are unlinked.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_providers/test_llamacpp.py` (match that file's existing import style; it already imports `LlamaCppProvider` and patches `_hf_cache_dir` — check its current fixtures and reuse them):

```python
def test_delete_preserves_blob_referenced_by_nested_file(tmp_path):
    """A blob whose only surviving reference is a file inside a snapshot
    subdirectory (nested layout, common in HF GGUF repos) must not be judged
    orphaned. Regression: the reference scan was non-recursive (iterdir +
    is_file), so it never saw the nested reference and deleted the shared
    blob — silently destroying another variant's multi-GB weights."""
    import hashlib

    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--org--repo" / "snapshots"
    blobs_dir = hub_dir / "models--org--repo" / "blobs"
    snap1 = repo_dir / "aaa"
    snap2 = repo_dir / "bbb" / "GGUF"
    snap1.mkdir(parents=True)
    snap2.mkdir(parents=True)
    blobs_dir.mkdir(parents=True)

    blob_hash = hashlib.sha256(b"weights").hexdigest()
    blob = blobs_dir / blob_hash
    blob.write_bytes(b"weights")
    # Snapshot 1 keeps the file top-level; snapshot 2 keeps identical
    # content nested under GGUF/ — the layout real HF GGUF repos use.
    (snap1 / "model.gguf").symlink_to(blob)
    (snap2 / "model.gguf").symlink_to(blob)

    provider = LlamaCppProvider({})
    variant = {"id": "org--repo", "provider": "llamacpp",
               "repo": "org/repo", "files": ["model.gguf"]}

    with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
        provider.delete(variant)

    assert not (snap1 / "model.gguf").exists()      # deleted variant's link gone
    assert (snap2 / "model.gguf").exists()          # nested reference survives
    assert blob.exists()                            # shared blob NOT orphan-deleted


def test_delete_unlinks_dangling_snapshot_symlink(tmp_path):
    """A snapshot file whose blob is already gone (dangling symlink) must
    still be unlinked. Regression: exists() follows symlinks and returned
    False for the dangling link, so the stale entry survived every delete
    and would break later stat() walks in reconcile/list_local."""
    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--org--repo" / "snapshots"
    snap = repo_dir / "aaa"
    snap.mkdir(parents=True)
    (hub_dir / "models--org--repo" / "blobs").mkdir()

    gguf = snap / "model.gguf"
    gguf.symlink_to(hub_dir / "models--org--repo" / "blobs" / "deadbeef")
    assert not gguf.exists()  # precondition: the link dangles

    provider = LlamaCppProvider({})
    variant = {"id": "org--repo", "provider": "llamacpp",
               "repo": "org/repo", "files": ["model.gguf"]}

    with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
        provider.delete(variant)

    assert not gguf.is_symlink()  # the stale link itself was removed


def test_delete_never_reads_file_via_read_bytes(tmp_path, monkeypatch):
    """The non-symlink fallback hash must read files in bounded chunks, never
    read_bytes(). Regression: the fallback loaded whole multi-GB GGUFs into
    memory on no-symlink filesystems (exFAT/SMB, HF_HUB_DISABLE_SYMLINKS=1)
    — the exact OOM the symlink fast path exists to prevent. Patching
    read_bytes to raise makes any regression fail loudly instead of OOMing
    the CI runner."""
    from pathlib import Path

    def _no_read_bytes(self):
        raise AssertionError("read_bytes() called — multi-GB OOM regression")

    monkeypatch.setattr(Path, "read_bytes", _no_read_bytes)

    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--org--repo"
    snap = repo_dir / "snapshots" / "aaa"
    snap.mkdir(parents=True)
    (repo_dir / "blobs").mkdir()
    # A regular file, not a symlink — forces the content-hash fallback.
    (snap / "model.gguf").write_bytes(b"weights")
    (repo_dir / "blobs" / hashlib.sha256(b"weights").hexdigest()).write_bytes(b"weights")

    provider = LlamaCppProvider({})
    variant = {"id": "org--repo", "provider": "llamacpp",
               "repo": "org/repo", "files": ["model.gguf"]}

    with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
        provider.delete(variant)  # must not raise

    assert not (snap / "model.gguf").exists()
    assert not list((repo_dir / "blobs").iterdir())  # orphan blob removed via chunked hash
```

(`hashlib` is imported at the top of the test file if not already present.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_providers/test_llamacpp.py -k "nested or dangling or read_bytes" -v`
Expected: the nested-file and dangling-symlink tests FAIL (blob deleted / link survives); the read_bytes test FAILS with `AssertionError: read_bytes() called`.

- [ ] **Step 3: Write the implementation**

In `src/modelman/providers/llamacpp.py`: move `import hashlib` from inside `delete()` to the module top (next to `import os`), and add after `_blob_hash_of`:

```python
_HASH_CHUNK = 1024 * 1024


def _hash_file(path: Path) -> str:
    """SHA256 a file's contents in bounded 1MB chunks.

    The non-symlink fallback of _blob_hash_of lands here (no-symlink
    filesystems, test fixtures). read_bytes() would materialize a
    multi-GB GGUF in memory — the exact OOM the symlink fast path
    exists to prevent — so hash in chunks instead.
    """
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(_HASH_CHUNK), b""):
            h.update(chunk)
    return h.hexdigest()
```

Replace the deletion loop in `delete()` (current lines 180–197) — note `is_symlink() or` for lexists semantics, and `is_file()` on the target must not be required since the target may dangle:

```python
        # Delete specified files from all snapshots. `files` entries may be
        # nested ("GGUF/model.gguf"); snap / f already handles that. A
        # snapshot entry may also be a dangling symlink (blob already gone):
        # unlink it too, or the stale link survives every delete and breaks
        # later stat() walks in reconcile/list_local.
        for snap in snapshots_dir.iterdir():
            if not snap.is_dir():
                continue
            for f in files:
                file_path = snap / f
                if file_path.is_symlink() or file_path.exists():
                    # Blobs are named by their SHA256; the snapshot file is a
                    # symlink to blobs/<hash>, so read the link target instead
                    # of re-hashing the (multi-GB) file body.
                    blob_hash = _blob_hash_of(file_path)
                    if blob_hash is None:
                        try:
                            blob_hash = _hash_file(file_path)
                        except OSError:
                            blob_hash = None
                    if blob_hash is not None:
                        blobs_to_check.add(blob_hash)
                    file_path.unlink()
```

Replace the reference scan (current lines 203–217) with a recursive walk using the same semantics:

```python
        # Build the set of blobs still referenced by remaining snapshots.
        # Walk recursively (rglob) with the same nested-path semantics the
        # deletion loop uses: a blob referenced only by a file inside a
        # snapshot subdirectory (common in HF GGUF repos) is NOT an orphan.
        referenced_blobs: set[str] = set()
        for snap in snapshots_dir.iterdir():
            if not snap.is_dir():
                continue
            for entry in snap.rglob("*"):
                if not (entry.is_symlink() or entry.is_file()):
                    continue
                blob_hash = _blob_hash_of(entry)
                if blob_hash is None:
                    try:
                        blob_hash = _hash_file(entry)
                    except OSError:
                        blob_hash = None
                if blob_hash is not None:
                    referenced_blobs.add(blob_hash)
```

Also fix the `delete()` docstring: `snapshots/<commit-hash>/<files>  (hard-links to blobs)` → `(symlinks to blobs — regular files when symlinks are disabled)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_providers/test_llamacpp.py tests/test_ready_toggle_delete.py -v`
Expected: PASS — the new tests, the existing llamacpp delete tests in both files (the regular-file tests exercise the chunked fallback), and ruff/mypy stay clean (`uv run ruff check src/modelman/providers/llamacpp.py && uv run mypy src/modelman/providers/llamacpp.py`).

- [ ] **Step 5: Commit**

```bash
git add src/modelman/providers/llamacpp.py tests/test_providers/test_llamacpp.py
git commit -m "fix(modelman): recursive blob scan, chunked fallback hash, dangling-symlink cleanup

The orphan-blob reference scan was non-recursive while deletion supported
nested paths, so a blob referenced only from a snapshot subdirectory was
deleted — destroying another variant's weights. The non-symlink fallback
hashed whole multi-GB files via read_bytes() (OOM on no-symlink
filesystems); hash in 1MB chunks instead. exists() missed dangling
snapshot symlinks; use is_symlink() for lexists semantics.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 2: Shared-artifact guard in `apply()` (omlx basename collision)

Fixes finding 3: `omlx.delete()` rmtree's `~/.omlx/models/<repo-basename>`, so two registry entries sharing a repo basename share one directory and deleting either destroys the other's weights. The guard is provider-neutral, lives in `apply()`, and uses `provider.path_of()`.

**Files:**
- Modify: `src/modelman/registry.py` (move + public rename of the variant converter)
- Modify: `src/modelman/screens/models.py` (import the moved function; delete the local copy)
- Modify: `src/modelman/queue.py`
- Test: `tests/test_queue.py`

**Interfaces:**
- Consumes: `_model_entry_to_variant(entry) -> VariantSpec` (currently in `screens/models.py:160`); `provider.path_of(variant) -> str | None` (exists on llamacpp + omlx).
- Produces: `registry.model_entry_to_variant(entry: ModelEntry) -> VariantSpec` (public, same body); `queue._shared_artifact_owner(registry, provider, variant) -> ModelEntry | None`.

- [ ] **Step 1: Move the converter to `registry.py`**

Cut `_model_entry_to_variant` (screens/models.py:160–185) and paste it into `src/modelman/registry.py` as a module-level function renamed `model_entry_to_variant` (drop the leading underscore; keep the docstring). `registry.py` already defines `ModelEntry`, `Fetch`, and `_cost_to_dict`, so it needs no new imports.

In `screens/models.py`: add `model_entry_to_variant` to the `from ..registry import (...)` block and replace all three uses of `_model_entry_to_variant(` (in `action_delete_model`, `action_edit_model`, `_run_apply`). Then run `git grep -n "_model_entry_to_variant"` — update any test/other references the same way (there is no back-compat alias; fix every call site).

- [ ] **Step 2: Write the failing tests**

Add to `tests/test_queue.py` (imports `MagicMock`, `ModelEntry`, `Fetch` usage per existing helpers; `_variant` builds the omlx-shaped specs):

```python
def test_apply_delete_skips_artifact_shared_with_other_entry(tmp_path):
    """Deleting an entry whose on-disk artifact is shared with another
    registry entry (omlx keys its dir on the repo *basename*, so org1/qwen
    and org2/qwen collide) must not remove the file — that would destroy
    the other entry's weights. The registry/state cleanup must still run."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    a = ModelEntry(id="omlx/a", family="f", provider_id="omlx",
                   model_name="a", fetch=Fetch(repo="org1/qwen", files=None, quantizations=None))
    b = ModelEntry(id="omlx/b", family="f", provider_id="omlx",
                   model_name="b", fetch=Fetch(repo="org2/qwen", files=None, quantizations=None))
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", auth=AuthConfig(type="none"))],
        models=[a, b],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("omlx/a", ModelState(ready=True))

    omlx = MagicMock()
    omlx.name = "omlx"
    # Both entries resolve to ~/.omlx/models/qwen — the basename collision.
    omlx.path_of.return_value = str(tmp_path / "omlx-models" / "qwen")
    omlx.is_downloaded.return_value = True

    pending = PendingChanges(
        registry=reg, state=state, family="f",
        registry_path=reg_path, state_path=state_path,
        providers={"omlx": omlx},
        deletes=[("omlx/a", _variant(id="omlx/a", provider="omlx", name="a", repo="org1/qwen"))],
    )
    pending.apply()

    assert omlx.delete.call_count == 0          # the shared file was kept
    assert all(m.id != "omlx/a" for m in reg.models)  # registry row still removed
    assert "omlx/a" not in state.models         # state row still removed
    assert any("shared with omlx/b" in f for f in pending.failures)


def test_apply_ready_off_skips_artifact_shared_with_other_entry(tmp_path):
    """Ready-off on an entry whose artifact directory is shared with another
    registry entry must not remove the file. Regression: the ready-off loop
    called provider.delete() unconditionally, rmtree'ing the directory the
    other entry's weights live in. State still clears and the unexpose
    cascade still runs — only the file survives."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    a = ModelEntry(id="omlx/a", family="f", provider_id="omlx",
                   model_name="a", fetch=Fetch(repo="org1/qwen", files=None, quantizations=None))
    b = ModelEntry(id="omlx/b", family="f", provider_id="omlx",
                   model_name="b", fetch=Fetch(repo="org2/qwen", files=None, quantizations=None))
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", auth=AuthConfig(type="none"))],
        models=[a, b],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("omlx/a", ModelState(ready=True, litellm_exposed=True))

    omlx = MagicMock()
    omlx.name = "omlx"
    omlx.path_of.return_value = str(tmp_path / "omlx-models" / "qwen")
    omlx.is_downloaded.return_value = True

    pending = PendingChanges(
        registry=reg, state=state, family="f",
        registry_path=reg_path, state_path=state_path,
        providers={"omlx": omlx},
        ready=[("omlx/a", _variant(id="omlx/a", provider="omlx", name="a", repo="org1/qwen"), False)],
    )
    pending.apply()

    assert omlx.delete.call_count == 0
    assert state.get("omlx/a").ready is False            # state cleared
    assert state.get("omlx/a").litellm_exposed is False  # cascade still ran
    assert any("shared with omlx/b" in f for f in pending.failures)
    # Ready-off never removes registry rows — the entry survives.
    assert any(m.id == "omlx/a" for m in reg.models)
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `uv run pytest tests/test_queue.py -k "shared_artifact" -v`
Expected: FAIL — `omlx.delete.call_count` is 1 in both; no "shared with" failure text.

- [ ] **Step 4: Write the implementation**

In `src/modelman/queue.py`, add a module-level helper (after `_reason`):

```python
def _shared_artifact_owner(
    registry: Registry, provider: object, variant: VariantSpec
) -> ModelEntry | None:
    """Another registry entry whose provider artifact resolves to the same
    on-disk target as `variant`'s, or None.

    Dir-based providers key their storage on a coarse segment of the repo
    id (omlx uses the repo *basename*), so two registry entries can share
    one artifact directory; removing it for one silently destroys the
    other's weights. path_of() is the provider-neutral way to ask "where
    does this variant live on disk". Returns None when the provider has
    no path_of or the target can't be resolved — callers treat that as
    "no conflict" and proceed with the normal delete.
    """
    from .registry import ModelEntry, model_entry_to_variant

    path_of = getattr(provider, "path_of", None)
    if not callable(path_of):
        return None
    try:
        mine = path_of(variant)
    except Exception:  # noqa: BLE001
        return None
    if mine is None:
        return None
    for m in registry.models:
        if m.id == variant["id"] or m.provider_id != variant["provider"]:
            continue
        try:
            theirs = path_of(model_entry_to_variant(m))
        except Exception:  # noqa: BLE001
            continue
        if theirs == mine:
            return m
    return None
```

(Type-checking note: `ModelEntry` is imported only for the annotation; if mypy flags it unused at runtime, keep the import inside the function as written — it is used by `model_entry_to_variant`'s module, and the annotation is a string under `from __future__ import annotations`. If ruff flags it, move `ModelEntry`/`model_entry_to_variant` to the module's TYPE_CHECKING/runtime import block at the top alongside `FamilyEntry, save_registry` — `model_entry_to_variant` is runtime-needed, `ModelEntry` annotation-only.)

Wire it into the **deletes loop**: inside `if provider is not None and artifact_present:` (queue.py:215), before `self._delete(variant)`:

```python
            if provider is not None and artifact_present:
                conflict = _shared_artifact_owner(self.registry, provider, variant)
                if conflict is not None:
                    # Another registry entry resolves to the same on-disk
                    # artifact; removing it would destroy that entry's
                    # weights. Keep the file, still drop this entry's
                    # registry/state rows, and surface why.
                    reason = f"artifact shared with {conflict.id} — not removed"
                    self.failures.append(f"delete {model_id}: {reason}")
                    emit(f"delete:fail|{model_id}|{label}|{reason}")
                else:
                    try:
                        self._delete(variant)
                    except Exception as exc:  # noqa: BLE001
                        reason = _reason(exc)
                        self.failures.append(f"delete {model_id}: {exc}")
                        emit(f"delete:fail|{model_id}|{label}|{reason}")
                        continue
```

(That is: the existing `try: self._delete(...)` block moves under the new `else:`. The flow then falls through to the registry/state cleanup and `delete:done`, so the entry is removed but the file survives.)

Wire it into the **ready loop's target-False branch** (queue.py:335–343): compute `conflict` the same way (provider may be `None` — guard with `if provider is not None`), and on conflict append `f"clear {model_id}: artifact shared with {conflict.id} — not removed"`, emit the `delete:fail` event, and **fall through** to the state clear + unexpose cascade (do not `continue` — only real delete failures skip those, and Task 3 restructures this branch further). Concretely the branch becomes:

```python
            else:
                emit(f"delete:start|{model_id}|{label}")
                conflict = (
                    _shared_artifact_owner(self.registry, provider, variant)
                    if provider is not None
                    else None
                )
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
                # (state clear + cascade code below, unchanged)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py -k "shared_artifact or delete" -v`
Expected: PASS — both new tests, plus all existing delete tests (the MagicMocks in `_setup_apply_test` return unequal `path_of` MagicMocks, so the guard is a no-op for them).

- [ ] **Step 6: Commit**

```bash
git add src/modelman/registry.py src/modelman/screens/models.py src/modelman/queue.py tests/test_queue.py
git commit -m "fix(modelman): refuse artifact removal when another entry shares the target

omlx keys its model dir on the repo basename, so two registry entries
with colliding basenames share one directory; deleting either rmtree'd
the other's weights. Check the registry for an entry whose path_of()
resolves to the same on-disk target before any artifact removal (deletes
loop and ready-off loop); keep the file, surface why, still run the
registry/state cleanup. Also moves model_entry_to_variant to registry.py
so queue.py can build sibling specs without importing the screen layer.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 3: Ready-loop `is_downloaded()` guard (stale-ready cleanup fails hard)

Fixes finding 6: the ready-off branch calls `self._delete(variant)` without the `is_downloaded()` guard the deletes loop has, so clearing a stale-ready model whose artifact is already gone surfaces a hard failure, leaves `state.ready` stale-True, and skips the unexpose cascade via `continue`.

**Files:**
- Modify: `src/modelman/queue.py` (the ready loop's `else:` branch from Task 2)
- Test: `tests/test_queue.py`

**Interfaces:**
- Consumes: the deletes loop's guard pattern (queue.py:206–213).
- Produces: the ready-off branch skips `provider.delete()` when `is_downloaded()` reports False (or the provider is flag-only), but still clears state and cascades the unexpose.

- [ ] **Step 1: Write the failing test**

```python
def test_apply_ready_off_absent_artifact_clears_state_without_provider_call(tmp_path):
    """Ready-off on a stale-ready model (artifact removed outside modelman,
    reconcile hasn't run since) must clear cleanly, not fail. Regression:
    the ready-off branch lacked the deletes loop's is_downloaded() guard,
    so `ollama rm` on an absent tag raised, state.ready stayed stale-True,
    and the unexpose cascade was skipped via continue — leaving a route in
    config.yaml to a model whose file is gone."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    state.set("ollama/a", ModelState(ready=True, litellm_exposed=True))
    providers["ollama"].is_downloaded.return_value = False  # artifact already gone

    pending = PendingChanges(
        registry=reg, state=state, family="f",
        registry_path=reg_path, state_path=state_path,
        providers=providers,
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), False)],
    )
    pending.apply()

    assert providers["ollama"].delete.call_count == 0     # no rm on a gone model
    assert state.get("ollama/a").ready is False           # stale flag cleared
    assert state.get("ollama/a").litellm_exposed is False # cascade ran
    assert pending.failures == []
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_queue.py::test_apply_ready_off_absent_artifact_clears_state_without_provider_call -v`
Expected: FAIL — `delete.call_count` is 1 and `failures` contains `clear ollama/a: ...`.

- [ ] **Step 3: Write the implementation**

In the ready-off `else:` branch, mirror the deletes loop's guard before the conflict/delete logic from Task 2:

```python
            else:
                emit(f"delete:start|{model_id}|{label}")
                # Mirror the deletes loop's guard: a stale-ready model whose
                # artifact is already gone must clear cleanly, not fail and
                # strand state.ready=True with the unexpose cascade skipped.
                artifact_present = True
                if provider is not None:
                    try:
                        artifact_present = bool(provider.is_downloaded(variant))  # type: ignore[attr-defined]
                    except Exception:  # noqa: BLE001
                        # Cannot confirm absence; fall back to "try the call".
                        artifact_present = True
                if artifact_present:
                    conflict = (
                        _shared_artifact_owner(self.registry, provider, variant)
                        if provider is not None
                        else None
                    )
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
                # (state clear + cascade code below, unchanged — runs whether
                # the artifact was removed, shared-kept, or already absent)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py -k "ready" -v`
Expected: PASS — the new test, the shared-artifact tests from Task 2 (their `is_downloaded` returns True so the guard lets them through), and `test_apply_ready_false_reconcilable_clears_without_removing_registry_entry` (its stub's `is_downloaded` must return True — verify and set it if the fixture default MagicMock returns truthy; MagicMock's default return is truthy so it passes as-is).

- [ ] **Step 5: Commit**

```bash
git add src/modelman/queue.py tests/test_queue.py
git commit -m "fix(modelman): guard ready-off artifact removal with is_downloaded()

The ready-off branch called provider.delete() unconditionally, so
clearing a stale-ready model whose artifact was already removed outside
modelman failed hard, left state.ready stale-True, and skipped the
unexpose cascade. Mirror the deletes loop's is_downloaded() guard: skip
the provider call when the artifact is absent, still clear state and
cascade the unexpose.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 4: Ready loop skips ids queued for deletion, even when the delete failed

Fixes finding 8: `deleted_ids` only records *successful* deletes (the deletes loop `continue`s past `deleted_ids.add` on failure), so a model queued for both delete and ready=False whose delete fails is re-deleted by the ready loop — two failures for one problem.

**Files:**
- Modify: `src/modelman/queue.py`
- Test: `tests/test_queue.py`

**Interfaces:**
- Consumes: `self.deletes` (list of `(model_id, VariantSpec)`); `deleted_ids` (moves loop still uses it — keep it).
- Produces: the ready loop skips any id that was *queued* for deletion in this apply, not just successfully deleted ones.

- [ ] **Step 1: Write the failing test**

```python
def test_apply_failed_delete_not_retried_by_ready_loop(tmp_path):
    """A model queued for both delete and ready=False whose provider.delete()
    fails must surface exactly one failure, not two. Regression: deleted_ids
    only recorded successful deletes, so the ready loop re-ran _delete on the
    same model and appended a duplicate 'clear <id>' failure for the same
    underlying problem (e.g. ollama daemon down)."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    state.set("ollama/a", ModelState(ready=True, disk_path="/old/path"))
    providers["ollama"].is_downloaded.return_value = True
    providers["ollama"].delete.side_effect = RuntimeError("daemon down")

    pending = PendingChanges(
        registry=reg, state=state, family="f",
        registry_path=reg_path, state_path=state_path,
        providers=providers,
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), False)],
    )
    pending.apply()

    assert providers["ollama"].delete.call_count == 1  # deletes loop only
    delete_failures = [f for f in pending.failures if "daemon down" in f]
    assert len(delete_failures) == 1                   # one failure, not two
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_queue.py::test_apply_failed_delete_not_retried_by_ready_loop -v`
Expected: FAIL — `delete.call_count` is 2 and `delete_failures` has 2 entries.

- [ ] **Step 3: Write the implementation**

In `apply()`, just before the ready loop (after `deleted_ids` is populated), add:

```python
        # Ids queued for deletion in this apply, whether or not the delete
        # succeeded. The ready loop must not touch any of them: a successful
        # delete already removed the rows, and a failed one has already
        # surfaced its failure — re-running _delete would only duplicate it.
        attempted_deletes = {mid for mid, _ in self.deletes}
```

In the ready loop, replace the current `deleted_ids` skip (queue.py:291–295) with:

```python
            if model_id in attempted_deletes:
                # Queued for deletion in this same apply (succeeded or
                # failed); the ready toggle is moot either way.
                continue
```

(`deleted_ids` stays — the moves loop's "deleted earlier" check still uses it.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py -k "delete or ready" -v`
Expected: PASS — the new test, `test_apply_ready_loop_skips_deleted_ids`, `test_apply_ready_loop_skips_deleted_ids_redownload` (successful deletes are in `attempted_deletes` too), and the move-drop test.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/queue.py tests/test_queue.py
git commit -m "fix(modelman): skip ready processing for failed deletes too

deleted_ids only recorded successful deletes, so a model queued for
both delete and ready=False whose delete failed was re-deleted by the
ready loop, surfacing a duplicate failure for one problem. Skip every
id queued for deletion, not just the successfully deleted ones.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 5: Flag-only ready-off removes the recorded artifact (false comment)

Fixes finding 7: the comment at `screens/models.py:420-423` claims "apply() will call provider.delete() to remove the file", but on the flag-only path (provider id has a `[[providers]]` entry but no registered Provider class) apply only flips `state.ready=False` — the file stays on disk forever with no writer ever correcting the contradiction. Fix the behavior, not just the comment: flag-only ready-off removes the artifact recorded in `state.disk_path`, mirroring `_delete()`'s existing fallback.

**Files:**
- Modify: `src/modelman/queue.py`
- Modify: `src/modelman/screens/models.py:420-423` (comment only)
- Test: `tests/test_queue.py`

**Interfaces:**
- Consumes: `state.get(mid).disk_path` (the recorded artifact path).
- Produces: `queue._remove_local_artifact(state, variant) -> None` (module-level; also replaces the inline fallback in `_delete`).

- [ ] **Step 1: Write the failing test**

```python
def test_apply_ready_off_flag_only_removes_recorded_artifact(tmp_path):
    """Ready-off on a flag-only provider (a [[providers]] entry with no
    registered Provider class — e.g. a hand-edited registry) must remove the
    artifact recorded in state.disk_path. Regression: the branch only
    flipped state.ready=False, so the file stayed on disk forever while
    state claimed not-ready, with no writer (reconcile skips unmapped
    providers) ever correcting the contradiction."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    model = ModelEntry(id="mlx/a", family="f", provider_id="mlx", model_name="a",
                       location="local")
    reg = Registry(
        providers=[ProviderEntry(id="mlx", name="MLX", location="local",
                                auth=AuthConfig(type="none"))],
        models=[model],
    )
    save_registry(reg, reg_path)
    artifact = tmp_path / "weights.bin"
    artifact.write_bytes(b"weights")
    state = StateStore()
    state.set("mlx/a", ModelState(ready=True, disk_path=str(artifact)))

    pending = PendingChanges(
        registry=reg, state=state, family="f",
        registry_path=reg_path, state_path=state_path,
        providers={},  # no Provider class registered for 'mlx' → flag-only
        ready=[("mlx/a", _variant(id="mlx/a", provider="mlx", name="a"), False)],
    )
    pending.apply()

    assert not artifact.exists()                    # recorded artifact removed
    assert state.get("mlx/a").ready is False
    assert state.get("mlx/a").disk_path is None
    assert pending.failures == []
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_queue.py::test_apply_ready_off_flag_only_removes_recorded_artifact -v`
Expected: FAIL — `artifact.exists()` is still True and `disk_path` is still set.

- [ ] **Step 3: Write the implementation**

Add a module-level function in `queue.py` (after `_reason`):

```python
def _remove_local_artifact(state: StateStore, variant: VariantSpec) -> None:
    """Best-effort removal of the on-disk artifact recorded in state.

    Used when no provider delete() runs — flag-only providers (native or
    unmapped) on ready-off, or a provider class without a delete
    implementation: unlink the recorded disk_path file (or dangling
    symlink), or an empty directory at that path. A non-empty directory
    is left alone — it may hold content the registry doesn't know about.
    """
    import os
    import shutil

    local_path = state.get(variant["id"]).disk_path
    if not local_path:
        return
    p = Path(local_path)
    if p.is_symlink() or p.is_file():
        p.unlink()
    elif p.is_dir() and not os.listdir(p):
        shutil.rmtree(p)
```

Replace the fallback half of `_delete` (queue.py:435–449) with a call to it:

```python
        # Fallback: providers without delete() just remove the recorded file.
        _remove_local_artifact(self.state, variant)
```

In the ready loop's flag-only branch (queue.py:302–307), remove the artifact on ready-off before flipping the flag:

```python
            if provider is None:
                # Flag-only provider (native or unmapped): no provider call
                # exists — but ready-off still means "remove the artifact",
                # so drop the file recorded in state.disk_path, mirroring
                # what a mapped provider's delete() would do.
                emit(f"ready:start|{model_id}|{label}")
                if not target:
                    _remove_local_artifact(self.state, variant)
                self.state.set(model_id, replace(self.state.get(model_id), ready=target))
                emit(f"ready:done|{model_id}|{label}")
```

And correct the now-true comment in `screens/models.py` `action_toggle_ready` (lines 420–423):

```python
        # Allow ready=False for all models. The apply() step removes the
        # on-disk artifact — provider.delete() for mapped providers, or the
        # disk_path recorded in state for flag-only ones — then cascades the
        # unexpose if the model was exposed. Reconcile re-syncs ready=True
        # only if the file somehow still exists after delete.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py -k "flag_only or ready" -v`
Expected: PASS — the new test plus `test_apply_ready_false_flag_only_clears_flag_and_cascades_unexpose` (native model has no disk_path → `_remove_local_artifact` no-ops) and `test_apply_ready_true_flag_only_sets_flag_no_provider_call`.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/queue.py src/modelman/screens/models.py tests/test_queue.py
git commit -m "fix(modelman): flag-only ready-off removes the recorded artifact

The screen's comment claimed apply() removes the file on ready-off, but
the flag-only path (provider entry without a registered Provider class)
only flipped state.ready — the file stayed on disk forever with state
claiming not-ready and no writer ever correcting it. Extract
_remove_local_artifact (the _delete fallback, now also unlinking dangling
symlinks) and call it on flag-only ready-off; fix the comment.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 6: Structural cascade rework — one rule, projected ready

Fixes findings 4, 5, and 10. The screen's mirrored provenance sets are replaced by a single invariant — **a queued expose=True cannot survive a model whose *projected* ready (queued, else persisted) is False** — enforced at every queue mutation, with cloud models exempt (the same exemption `_validated_entry` applies at apply time). The screen stops queueing phantom unexposes; `apply()` already re-derives them. The EXPOSED column gates on projected ready so the display matches the invariant.

**Behavior spec (the executor's source of truth for updating old tests):**

| Sequence (local model, ready R, exposed E) | queued_ready | queued_exposes | note |
|---|---|---|---|
| ready+exposed, `r` | `{mid: False}` | `{}` | no phantom unexpose; apply cascades it; column shows `–` |
| ready+exposed, `r`, `r` | `{}` | `{}` | clean cancel |
| ready+exposed, `r`, `x` | `{mid: False}` | `{mid: False}` | `x` flips toward *unexpose*, which needs no ready gate — allowed |
| ready+not-exposed, `x` | `{}` | `{mid: True}` | unchanged |
| ready+not-exposed, `r`, `x` | `{mid: False}` | `{}` | `x` REFUSES with notify ("queued not-ready — cancel that before exposing") |
| ready+not-exposed, `x`, `r` | `{mid: False}` | `{}` | expose dropped by the rule, with notify |
| not-ready+not-exposed, `x` | `{mid: True}` | `{mid: True}` | cascade (marker in `_ready_cascade_for_expose`) |
| not-ready+not-exposed, `x`, `x` | `{}` | `{}` | cancel pops both halves |
| not-ready+not-exposed, `x`, `r` | `{}` | `{}` | cancel branch pops both halves (target == persisted) |
| not-ready+not-exposed, `r`, `x` | `{mid: True}` | `{mid: True}` | x sees projected ready True — no cascade marker |
| not-ready+not-exposed, `r`, `x`, `r` | `{}` | `{}` | final r cancels the ready flip; the rule drops the now-impossible expose with a notify |
| any cloud model | — | — | rule + cascade both skip cloud rows (`is_cloud_effective`) |

**Files:**
- Modify: `src/modelman/screens/models.py`
- Test: `tests/screens/test_models.py`

**Interfaces:**
- Consumes: `is_cloud_effective` (already imported), `self._ready_cascade_for_expose` (kept, only direction needed).
- Produces: `ModelScreen._projected_ready(model_id) -> bool`; `ModelScreen._enforce_expose_ready_rule(mid, entry) -> None`. `_expose_cascade_for_ready` is deleted.

- [ ] **Step 1: Write the failing tests**

Add to `tests/screens/test_models.py` after the existing interleaving tests (all use this seeding block; only the `ModelState(...)` flags and key presses differ — location is `"local"` so the ready gate applies, unlike the older cloud-seeded tests):

```python
@pytest.mark.asyncio
async def test_x_then_r_drops_queued_expose_with_notification(tmp_path, monkeypatch):
    """'x' then 'r' on a ready, not-exposed local model must drop the queued
    expose, not silently overwrite it. Regression: the ready→expose cascade
    overwrote queued_exposes[mid]=False even when the entry was the user's
    explicit expose=True, discarding their request with no notification and
    showing a phantom 'unexpose' for a model that was never exposed."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(id="ollama/a", family="ornith", provider_id="ollama",
                       model_name="a", location="local")
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, litellm_exposed=False))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get",
                        staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        assert app.screen.queued_exposes == {"ollama/a": True}
        await pilot.press("r")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_ready == {"ollama/a": False}
        # The user's expose was cancelled (with a notification), not
        # silently flipped to a phantom unexpose.
        assert app.screen.queued_exposes == {}


@pytest.mark.asyncio
async def test_r_then_x_refuses_expose_when_ready_off_queued(tmp_path, monkeypatch):
    """'r' then 'x' on a ready, not-exposed local model must refuse the
    expose. Regression: 'x' validated against *persisted* ready=True, queued
    the expose, and apply() always failed with 'model is not ready' after
    the ready loop deleted the file."""
    # (Identical seeding block to the test above: ready=True, exposed=False,
    #  location="local".)
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(id="ollama/a", family="ornith", provider_id="ollama",
                       model_name="a", location="local")
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, litellm_exposed=False))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get",
                        staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()
        await pilot.press("r")
        await pilot.pause()
        assert app.screen.queued_ready == {"ollama/a": False}
        await pilot.press("x")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_exposes == {}   # refused, not queued
        assert app.screen.queued_ready == {"ollama/a": False}  # user's ready kept


@pytest.mark.asyncio
async def test_r_x_r_on_not_ready_model_drops_stranded_expose(tmp_path, monkeypatch):
    """'r' → 'x' → 'r' on a not-ready, not-exposed local model must end with
    an empty queue. Regression: the third 'r' cancelled the ready flip but
    left the expose queued (it was never cascade-marked because 'r' had
    already queued ready=True), and apply() always failed with 'model is
    not ready'."""
    # (Identical seeding block, except ModelState(ready=False,
    #  litellm_exposed=False).)
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(id="ollama/a", family="ornith", provider_id="ollama",
                       model_name="a", location="local")
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=False, litellm_exposed=False))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get",
                        staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()
        await pilot.press("r")
        await pilot.pause()
        assert app.screen.queued_ready == {"ollama/a": True}
        await pilot.press("x")
        await pilot.pause()
        assert app.screen.queued_exposes == {"ollama/a": True}
        await pilot.press("r")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_ready == {}      # ready flip cancelled
        assert app.screen.queued_exposes == {}     # stranded expose dropped


@pytest.mark.asyncio
async def test_exposed_column_gates_on_projected_ready(tmp_path, monkeypatch):
    """The EXPOSED column must read the *projected* ready value, not the
    persisted one: after 'r' queues ready-off on a ready+exposed model, the
    row renders '–' even before apply runs. Regression: the column kept
    rendering 'Y' because the screen no longer queues a phantom unexpose
    entry for it to pick up."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(id="ollama/a", family="ornith", provider_id="ollama",
                       model_name="a", location="local")
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
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get",
                        staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()
        from textual.widgets import DataTable

        def _exposed_cell() -> str:
            table = app.screen.query_one("#model-table", DataTable)
            return str(table.get_cell(list(table.rows.keys())[0], "EXPOSED"))

        assert _exposed_cell() == "Y"   # ready+exposed renders Y
        await pilot.press("r")
        await pilot.pause()
        assert _exposed_cell() == "–"    # projected not-ready gates it to –
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/screens/test_models.py -k "x_then_r or r_then_x or r_x_r or projected_ready" -v`
Expected: all four FAIL — first: `queued_exposes == {"ollama/a": False}` (phantom unexpose); second: `queued_exposes == {"ollama/a": True}` (expose queued against a ready-off); third: `queued_exposes == {"ollama/a": True}` (stranded); fourth: column still `Y`.

- [ ] **Step 3: Write the implementation**

In `screens/models.py`:

**(a)** Delete the `_expose_cascade_for_ready` attribute block in `__init__` (lines 253–259) and its two `.clear()` calls (in `_on_exit_confirm` and `_run_apply`).

**(b)** Add the two helpers after `_is_ready`:

```python
    def _projected_ready(self, model_id: str) -> bool:
        """The ready value this model will have after apply(): the queued
        target if one exists, otherwise the persisted flag."""
        return self.queued_ready.get(model_id, self.state.get(model_id).ready)

    def _enforce_expose_ready_rule(self, mid: str, entry: ModelEntry) -> None:
        """Single invariant: expose depends on ready. A queued expose=True
        cannot survive a model whose projected ready is False — apply()
        enforces the same rule at the gate (_validated_entry rejects the
        expose with 'model is not ready'); this keeps the queue consistent
        with it instead of leaving a doomed entry for apply() to fail on.
        Cloud rows are exempt, matching _validated_entry. Drops the expose
        with a notification rather than silently overwriting the user's
        request."""
        if is_cloud_effective(entry):
            return
        if self.queued_exposes.get(mid) is True and not self._projected_ready(mid):
            self.queued_exposes.pop(mid, None)
            self.app.notify(f"Expose cancelled: {mid} will not be ready")
```

**(c)** Rewrite `action_toggle_ready` (drop the cascade-queueing block; enforce the rule after every mutation):

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
        # Queue the ready target only. apply() owns the consequences — it
        # removes the artifact (provider delete, or the recorded disk_path
        # for flag-only providers) on ready-off and re-derives the unexpose
        # cascade from the persisted exposure flag — and the invariant below
        # drops any queued expose the new ready value makes impossible, so
        # the screen queue can never strand a doomed expose.
        self.queued_ready[mid] = target
        self._enforce_expose_ready_rule(mid, entry)
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()
```

**(d)** Rewrite `action_toggle_expose` (validate against *projected* ready; refuse rather than overwrite a user's ready-off; delete the `_expose_cascade_for_ready.discard`):

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
                # The download was only queued to serve this expose; the
                # user never asked for it independently.
                self.queued_ready.pop(mid, None)
                self._ready_cascade_for_expose.discard(mid)
            self.app.notify(f"Model already {'exposed' if target else 'not exposed'}")
            self._refresh_pending_bar()
            self.reload()
            return
        if target and not self._projected_ready(mid) and not is_cloud_effective(entry):
            # Exposing requires ready — the same gate _validated_entry
            # applies at apply time. If the user has a ready toggle queued
            # that leaves the model not-ready, refuse rather than overwrite
            # their request; otherwise cascade the download in (apply runs
            # the ready loop before the expose loop, so the order works).
            if mid in self.queued_ready:
                self.app.notify(
                    "Model is queued to be made not ready — cancel that before exposing"
                )
                return
            self._ready_cascade_for_expose.add(mid)
            self.queued_ready[mid] = True
        self.queued_exposes[mid] = target
        self._refresh_pending_bar()
        self.reload()
```

**(e)** In `_load_models`, gate the column on projected ready:

```python
                exposed = self.state.get(m.id).litellm_exposed
                if m.id in self.queued_exposes:
                    exposed = self.queued_exposes[m.id]
                # Effective exposure = the (queued or persisted) flag AND the
                # *projected* ready value — the same gate `_validated_entry`
                # applies at apply time, so the column shows what the model
                # will be after apply, not what it was before the queue.
                # Cloud rows are exempt from the ready gate.
                ready_or_cloud = self._projected_ready(m.id) or is_cloud_effective(m)
                exposed_str = "Y" if (exposed and ready_or_cloud) else "–"
```

- [ ] **Step 4: Update the tests that pinned the old behavior, then run the full screen file**

Run: `uv run pytest tests/screens/test_models.py -v` and fix per the behavior spec table above:

- `test_r_twice_on_ready_exposed_cancels_unexpose_cascade` (line 1337): the mid-press assertion `queued_exposes == {"ollama/a": False}` no longer holds — the structural change means `'r'` queues no expose at all. Change that assertion to `assert app.screen.queued_exposes == {}` and rename the test `test_r_twice_on_ready_exposed_leaves_queue_empty`; update the docstring to describe the new semantics (no phantom unexpose; apply re-derives it).
- `test_r_x_x_r_interleaving_preserves_independent_unexpose` (line 1390): the marker-reclamation bug it guarded is gone with `_expose_cascade_for_ready`. Replace it with `test_r_x_r_preserves_independent_unexpose`: `r` (ready-off), `x` (independent unexpose → `queued_exposes == {"ollama/a": False}`), `r` (cancel ready-off; the unexpose survives — the invariant only drops expose=True) → final `queued_ready == {}`, `queued_exposes == {"ollama/a": False}`. Seed with `location="local"`; same docstring intent.
- `test_x_on_not_ready_model_cascades_ready_and_expose` (line 1153): if its model is cloud-seeded, the cascade no longer fires (cloud rows skip the ready gate) — re-seed it `location="local"` (cascade still fires per spec row 5) or, if it must stay cloud, invert the assertions to `queued_ready == {}` + `queued_exposes == {mid: True}`. Prefer re-seeding local.
- `test_exposed_column_requires_ready_but_exempts_cloud` (line 665): the column now reads projected ready — a queued ready=True makes a flagged row render `Y` pre-apply. Update any assertion that pairs a queued ready with a persisted-gate expectation.
- `test_r_cancel_after_x_cascade_also_cancels_expose`, `test_x_cancel_after_x_cascade_also_cancels_ready`, `test_x_on_ready_model_queues_only_expose`, `test_x_twice_cancels_queued_expose_with_notification`, `test_r_twice_cancels_queued_flip_with_notification`: expected to pass unchanged (cancel-branch logic is identical); if one fails, reconcile it against the spec table.

Expected: full file PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "fix(modelman): enforce expose-depends-on-ready as one rule on projected state

The screen kept two mirrored provenance sets to predict apply()'s
cascade, which re-derives it from persisted state and ignores the
screen's queue — the drift stranded exposes (guaranteed apply failures)
and silently overwrote explicit user requests. Replace both layers with
one invariant enforced at every queue mutation: a queued expose=True is
dropped (with a notification) whenever the projected ready is False;
expose queuing validates against projected ready and refuses rather
than overwriting a user's ready-off. Cloud rows are exempt, matching
_validated_entry. The screen no longer queues phantom unexposes (apply
already re-derives them) and the EXPOSED column gates on projected
ready. Deletes _expose_cascade_for_ready.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 7: Test hygiene and documentation

Fixes the four cut items the reviewer noted. No behavior changes.

**Files:**
- Modify: `tests/test_ready_toggle_delete.py` → **delete the file**, moving its surviving tests
- Modify: `tests/test_providers/test_llamacpp.py`, `tests/test_providers/test_omlx.py`
- Modify: `modelman/CLAUDE.md`

- [ ] **Step 1: Move the provider tests and delete the tautological one**

`tests/test_ready_toggle_delete.py` contains `TestOmlxDelete`, `TestLlamaCppDelete`, and `TestReadyToggleCascade`. `TestReadyToggleCascade.test_ready_off_cascades_unexpose_logic` re-implements the cascade logic and asserts its own copy — it can't fail against a real regression (and Task 6 changed the logic it mirrors). **Delete the whole `TestReadyToggleCascade` class.** Move `TestOmlxDelete` (its two tests) into `tests/test_providers/test_omlx.py` and `TestLlamaCppDelete` (its three tests — plus Task 1's, already in `test_llamacpp.py`) into `tests/test_providers/test_llamacpp.py`, adapting imports/fixtures to each file's existing style (they already patch `_hf_cache_dir` / use `tmp_path` the same way). Ensure every moved test has a docstring describing behavior + importance (add any missing ones — the reviewer flagged four). Then `git rm tests/test_ready_toggle_delete.py`.

- [ ] **Step 2: Update `modelman/CLAUDE.md`**

In the `screens/models.py` paragraph, replace:

> `r` toggle ready (queues download/pull for reconcilable providers, or a flag flip for cloud/native; a no-op with a notification for a local-artifact model already on disk — reconcile is the only writer of `ready=False` for those, so delete the file instead)

with:

> `r` toggle ready — a true file-presence toggle: ready-off queues the artifact removal (provider `delete()`, or the `state.disk_path` file for flag-only providers) and apply() re-derives the unexpose from the persisted exposure flag; ready-on queues a download/pull or flag flip

Replace the cascade sentence:

> Cancelling either half of a cascade (pressing `r` or `x` a second time back to the persisted value) cancels the other half too — only when that half was itself cascade-added, not when the user queued it independently — so cancelling never leaves an expose queued against a model that will end up not-ready, or a download queued for an expose the user un-queued.

with:

> The expose-depends-on-ready invariant is enforced in one place — `_enforce_expose_ready_rule`, run at every queue mutation: a queued `expose=True` is dropped (with a notification) whenever the *projected* ready value (`queued_ready` target, else persisted) is False, and `x` on a model queued to be made not-ready refuses rather than overwrites. Cloud rows are exempt (matching `_validated_entry`). Only the expose→ready cascade direction carries a provenance marker (`_ready_cascade_for_expose`): an `x`-cascaded download is cancelled when its expose is cancelled. The screen never queues unexpose cascades itself — `apply()` re-derives them — and the EXPOSED column gates on projected ready, so it shows the post-apply state.

In the `queue.py` paragraph, after the existing delete-step sentence, append:

> The deletes loop and the ready-off loop both check `_shared_artifact_owner` before artifact removal: when another registry entry's `path_of()` resolves to the same on-disk target (omlx dirs are keyed on the repo basename, so colliding repos share one), the file is kept and the reason surfaced, while registry/state cleanup still runs. The ready-off loop also guards removal with `provider.is_downloaded()` like the deletes loop, skips any id queued for deletion (succeeded or failed), and flag-only providers remove the artifact recorded in `state.disk_path` on ready-off via `_remove_local_artifact`.

- [ ] **Step 3: Run the moved tests and lint**

Run: `uv run pytest tests/test_providers/ -v && uv run make check`
Expected: PASS — moved tests green in their new homes, no F401s from the deleted file, links intact (`bin/check-links` runs in CI; no markdown links were added).

- [ ] **Step 4: Commit**

```bash
git add tests/ CLAUDE.md
git commit -m "test(modelman): move delete tests to tests/test_providers, drop tautological cascade test

TestReadyToggleCascade.test_ready_off_cascades_unexpose_logic
re-implemented the cascade and asserted its own copy — it could not fail
against a real regression. Move the omlx/llamacpp delete tests next to
their provider's existing tests, add the missing docstrings, and update
CLAUDE.md for the single-rule cascade and the new apply() guards.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Task 8: Full verification

**Files:** None (verification only).

- [ ] **Step 1: Run the full modelman suite**

Run: `uv run make test` (background it; ~5+ min)
Expected: PASS — all tests green including the ~210 Textual screen tests.

- [ ] **Step 2: Lint + typecheck + monorepo umbrella**

Run: `uv run make check`, then `make test-all` from the repo root.
Expected: PASS.

- [ ] **Step 3: Confirm no guide-doc drift**

Run: `git grep -n "litellm_exposed = " docs/guides/` — the snapshots must be unchanged (this plan alters no exposure state on disk).

---

## Self-Review

**Spec coverage:** All 10 findings addressed — 1 (Task 1, recursive scan), 2 (Task 1, chunked hash), 3 (Task 2, shared-artifact guard), 4 (Task 6, rule drops queued expose with notify), 5 (Task 6, projected-ready validation + rule), 6 (Task 3, is_downloaded guard), 7 (Task 5, flag-only artifact removal + comment), 8 (Task 4, attempted-deletes skip), 9 (Task 1, dangling-symlink unlink), 10 (Task 6, marker sets collapsed to one rule). Cut items: docstrings/tautological test/test placement/CLAUDE.md (Task 7).

**Placeholder scan:** No TBD/TODO; every code step carries concrete code; every new test has a docstring; the Task 6 Step 4 test updates enumerate each affected test with its new expected state.

**Type consistency:** `model_entry_to_variant` is moved to `registry.py` in Task 2 and consumed by `queue.py` (`_shared_artifact_owner`) and `screens/models.py`; `_remove_local_artifact(state, variant)` is defined in Task 5 and used by both `_delete`'s fallback and the flag-only ready branch; `_shared_artifact_owner(registry, provider, variant)` is defined in Task 2 and wired into both loops (Tasks 2–3 restructure the ready-off branch coherently); `_projected_ready`/`_enforce_expose_ready_rule` are introduced and called only within `ModelScreen`. The Task 2 ready-loop snippet is superseded by Task 3's fuller branch — implement Task 3's version.