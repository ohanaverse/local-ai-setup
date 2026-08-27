"""oMLX provider — uses ~/.omlx/models/<basename-of-repo>/ for downloads."""

from __future__ import annotations

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


def _model_dir(config: dict) -> Path:
    raw = config.get("model_dir", "~/.omlx/models")
    return Path(os.path.expanduser(raw))


def _basename(repo: str) -> str:
    """Last /-separated component of the repo id."""
    return repo.split("/")[-1]


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

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        repo = variant.get("repo")
        if not repo:
            return False
        target = _model_dir(self.config) / _basename(repo)
        return target.is_dir() and any(target.iterdir())

    def download(
        self,
        variant: VariantSpec,
        runner: _Runner | None = None,
        on_progress: Callable[[str], None] | None = None,
    ) -> str:
        repo = variant.get("repo")
        if not repo:
            raise ValueError(f"omlx variant {variant['id']} missing repo")
        # Reset cancellation flag at the start of each download so a
        # previous Cancel on this provider doesn't immediately abort the
        # next attempt.
        self._cancel_requested = False
        target = _model_dir(self.config) / _basename(repo)
        kwargs: dict[str, Any] = {"repo_id": repo, "local_dir": str(target)}
        ProgressTqdm.set_active_context(
            on_progress, lambda: self._cancel_requested
        )
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
        repo = variant.get("repo")
        if not repo:
            return None
        target = _model_dir(self.config) / _basename(repo)
        if not target.is_dir():
            return None
        total = 0
        for f in target.rglob("*"):
            if f.is_file():
                total += f.stat().st_size
        return total or None


ProviderRegistry.register(OMLXProvider)
