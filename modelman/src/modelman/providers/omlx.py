"""oMLX provider — uses ~/.omlx/models/<basename-of-repo>/ for downloads."""

from __future__ import annotations

import os
from collections.abc import Callable
from pathlib import Path
from typing import Any

from huggingface_hub import snapshot_download

from ._progress import HF_DOWNLOAD_LOCK, ProgressTqdm, repo_basename
from .base import LocalModel, Provider, VariantSpec, _Runner
from .registry import ProviderRegistry


def _model_dir(config: dict) -> Path:
    raw = config.get("model_dir", "~/.omlx/models")
    return Path(os.path.expanduser(raw))


def _is_local_path_entry(variant: VariantSpec) -> bool:
    return bool(variant.get("local_path"))


class OMLXProvider(Provider):
    name = "omlx"

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

    def _target_dir(self, variant: VariantSpec) -> Path | None:
        """Resolve the on-disk directory for `variant`, or None if it has
        neither a local_path nor a repo. local_path (a user-produced
        mlx-lm.convert/dwq output) takes precedence when set; otherwise
        falls back to the existing repo-keyed download directory."""
        local_path = variant.get("local_path")
        if local_path:
            return Path(local_path)
        repo = variant.get("repo")
        if repo:
            return _model_dir(self.config) / repo_basename(repo)
        return None

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        target = self._target_dir(variant)
        if target is None:
            return False
        return target.is_dir() and any(target.iterdir())

    def download(
        self,
        variant: VariantSpec,
        runner: _Runner | None = None,
        on_progress: Callable[[str], None] | None = None,
    ) -> str:
        target = self._target_dir(variant)
        if target is None:
            raise ValueError(f"omlx variant {variant['id']} missing repo")
        if variant.get("local_path"):
            # local_path artifacts are produced by the user (mlx_lm.convert,
            # dwq, etc.), not downloaded by modelman — download() is a no-op
            # success once the directory is confirmed to exist. A missing
            # directory surfaces as a ValueError now, at "download" time,
            # rather than silently at serve time.
            if not target.is_dir():
                raise ValueError(f"local_path does not exist: {target}")
            return str(target)
        self._cancel_requested = False
        repo = variant.get("repo")
        if not repo:
            raise ValueError(
                f"omlx variant {variant['id']} missing repo "
                "(local_path not set, so repo must be provided)"
            )
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

    def list_local(self, runner: _Runner | None = None) -> list[LocalModel]:
        models: list[LocalModel] = []
        md = _model_dir(self.config)
        if not md.exists():
            return models
        for d in md.iterdir():
            if d.is_dir():
                models.append(
                    {
                        "variant_id": d.name,
                        "path": str(d),
                        "size_bytes": None,
                    }
                )
        return models

    def size_of(self, variant: VariantSpec) -> int | None:
        target = self._target_dir(variant)
        if target is None:
            return None
        if not target.is_dir():
            return None
        total = 0
        for f in target.rglob("*"):
            if f.is_file():
                total += f.stat().st_size
        return total or None

    def path_of(self, variant: VariantSpec) -> str | None:
        """Return the model directory path, or None if not downloaded."""
        target = self._target_dir(variant)
        if target is None:
            return None
        if not target.is_dir() or not any(target.iterdir()):
            return None
        return str(target)

    def delete(self, variant: VariantSpec, runner: _Runner | None = None) -> None:
        """Remove the model directory (~/.omlx/models/<repo-basename>).

        Deletes the entire directory for the repo. Safe to call even if
        the directory is already absent (no-op).
        """
        import shutil

        target = self._target_dir(variant)
        if target is None:
            raise ValueError(f"omlx variant {variant['id']} missing repo")
        # local_path entries are convert/dwq output the USER produced by
        # hand, not a modelman download — modelman must never delete them,
        # regardless of whether the directory currently exists.
        if not _is_local_path_entry(variant) and target.exists():
            shutil.rmtree(target)

    def cleanup_partial_download(self, variant: VariantSpec) -> None:
        """Remove the partially-populated target directory.

        snapshot_download writes files directly into local_dir as they
        complete, so a cancel mid-download leaves a partial directory
        behind. Same removal delete() does — a cancelled download has no
        artifact worth keeping.
        """
        import shutil

        target = self._target_dir(variant)
        if target is None:
            return
        # Same local_path exemption as delete(): a user-produced convert/dwq
        # directory is never something modelman created, so it's never
        # something modelman removes here either.
        if not _is_local_path_entry(variant) and target.exists():
            shutil.rmtree(target)


ProviderRegistry.register(OMLXProvider)
