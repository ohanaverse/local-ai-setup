"""llama.cpp provider — uses the HF cache; no separate model dir by default."""

from __future__ import annotations

import hashlib
import os
from collections.abc import Callable
from pathlib import Path
from typing import Any, Protocol

from huggingface_hub import snapshot_download

from ._progress import ProgressTqdm
from .base import LocalModel, Provider, VariantSpec
from .registry import ProviderRegistry


class _Runner(Protocol):
    def __call__(self, *args: Any, **kwargs: Any) -> Any: ...


def _hf_cache_dir() -> Path:
    return Path(os.environ.get("HF_HOME", "~/.cache/huggingface")).expanduser() / "hub"


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


def _files_in_hf_cache(repo: str, files: list[str]) -> bool:
    """Return True if every `file` exists somewhere in the repo's HF cache snapshots."""
    hf_org, hf_name = repo.split("/", 1)
    repo_dir = _hf_cache_dir() / f"models--{hf_org}--{hf_name}" / "snapshots"
    if not repo_dir.exists():
        return False
    for snap in repo_dir.iterdir():  # noqa: SIM110 (clearer as a loop)
        if all((snap / f).exists() for f in files):
            return True
    return False


class LlamaCppProvider(Provider):
    name = "llamacpp"

    def __init__(self, options: dict[str, Any]) -> None:
        super().__init__(options)
        # Set by cancel_current(); ProgressTqdm polls this on each
        # display() update to abort snapshot_download via DownloadCancelled.
        self._cancel_requested = False

    def cancel_current(self) -> None:
        """Request cancellation of an in-progress HF download.

        Sets a flag the ProgressTqdm polls on every display update;
        when it returns True, the bar raises DownloadCancelled, which
        propagates out of snapshot_download and out of the apply loop.
        Safe to call from any thread.
        """
        self._cancel_requested = True

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        if not variant.get("repo") or not variant.get("files"):
            return False
        repo: str = variant["repo"]  # type: ignore[assignment]
        files: list[str] = variant["files"]  # type: ignore[assignment]
        return _files_in_hf_cache(repo, files)

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

        # Reset cancellation flag at the start of each download so a
        # previous Cancel on this provider doesn't immediately abort the
        # next attempt.
        self._cancel_requested = False

        primary = files[0]
        kwargs: dict[str, Any] = {
            "repo_id": repo,
            "allow_patterns": files,
            "cache_dir": _hf_cache_dir(),
        }
        # huggingface_hub doesn't accept per-call kwargs for the user's
        # tqdm_class, so we route the callbacks through a class-level
        # active context on ProgressTqdm. Set BEFORE snapshot_download and
        # cleared in finally so a stray bar from a previous call can
        # never pick up the wrong callbacks.
        ProgressTqdm.set_active_context(on_progress, lambda: self._cancel_requested)
        try:
            if on_progress is not None:
                kwargs["tqdm_class"] = ProgressTqdm
            path = snapshot_download(**kwargs)
            return str(Path(path) / primary)
        finally:
            ProgressTqdm.clear_active_context()

    def size_of(self, variant: VariantSpec) -> int | None:
        repo = variant.get("repo")
        files = variant.get("files")
        if not repo or not files:
            return None
        hf_org, hf_name = repo.split("/", 1)
        repo_dir = _hf_cache_dir() / f"models--{hf_org}--{hf_name}" / "snapshots"
        if not repo_dir.exists():
            return None
        primary = files[0]
        for snap in repo_dir.iterdir():
            candidate = snap / primary
            if candidate.exists():
                return candidate.stat().st_size
        return None

    def path_of(self, variant: VariantSpec) -> str | None:
        """Return the primary file path in the HF cache, or None if missing."""
        repo = variant.get("repo")
        files = variant.get("files")
        if not repo or not files:
            return None
        hf_org, hf_name = repo.split("/", 1)
        repo_dir = _hf_cache_dir() / f"models--{hf_org}--{hf_name}" / "snapshots"
        if not repo_dir.exists():
            return None
        primary = files[0]
        for snap in repo_dir.iterdir():
            candidate = snap / primary
            if candidate.exists():
                return str(candidate)
        return None

    def delete(self, variant: VariantSpec, runner: _Runner | None = None) -> None:
        """Remove the GGUF file(s) from HF cache and clean up orphaned blobs.

        Deletes the specific file(s) listed in the variant from all snapshot
        directories, then removes any orphaned blobs from the blobs/ directory.

        HF cache structure:
          hub/models--<org>--<repo>/
            snapshots/<commit-hash>/<files>  (symlinks to blobs — regular
                                              files when symlinks are disabled)
            blobs/<sha256-hash>              (actual file content)

        We delete the snapshot file links, then check if each blob is still
        referenced by other snapshots. If not, we remove the blob too.
        """
        repo = variant.get("repo")
        files = variant.get("files")
        if not repo or not files:
            raise ValueError(f"llamacpp variant {variant['id']} missing repo/files")

        hf_org, hf_name = repo.split("/", 1)
        repo_dir = _hf_cache_dir() / f"models--{hf_org}--{hf_name}"
        snapshots_dir = repo_dir / "snapshots"
        blobs_dir = repo_dir / "blobs"

        if not snapshots_dir.exists():
            return  # Already absent

        # Collect all files we're about to delete (for blob cleanup)
        blobs_to_check: set[str] = set()

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

        # Clean up orphaned blobs
        if not blobs_to_check:
            return

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

        # Delete blobs no longer referenced
        if blobs_dir.exists():
            for blob_hash in blobs_to_check:
                if blob_hash not in referenced_blobs:
                    blob_path = blobs_dir / blob_hash
                    if blob_path.exists():
                        blob_path.unlink()

    def list_local(self, runner: _Runner | None = None) -> list[LocalModel]:
        models: list[LocalModel] = []
        hub = _hf_cache_dir()
        if not hub.exists():
            return models
        for repo_dir in hub.iterdir():
            if not repo_dir.name.startswith("models--"):
                continue
            snapshots_dir = repo_dir / "snapshots"
            if not snapshots_dir.exists():
                continue
            for snap in snapshots_dir.iterdir():
                if not snap.is_dir():
                    continue
                for f in snap.iterdir():
                    if f.suffix == ".gguf":
                        models.append(
                            {
                                "variant_id": str(f.relative_to(snap)),
                                "path": str(f),
                                "size_bytes": f.stat().st_size,
                            }
                        )
        return models


ProviderRegistry.register(LlamaCppProvider)
