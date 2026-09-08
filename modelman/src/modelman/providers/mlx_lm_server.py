"""mlx-lm server provider — one variant is a target+draft pairing served by
`mlx_lm.server --draft-model` for speculative decoding. Mirrors omlx.py's
structure: each side (target, draft) resolves independently from either a
locally-produced directory (`local_path`/`draft_local_path`) or a Hugging
Face repo (`repo`/`draft_repo`) downloaded into
~/.mlx_lm_server/models/<basename-of-repo>/.
"""

from __future__ import annotations

import os
from collections.abc import Callable
from pathlib import Path
from typing import Any

from huggingface_hub import snapshot_download

from ._progress import HF_DOWNLOAD_LOCK, ProgressTqdm
from .base import LocalModel, Provider, VariantSpec, _Runner
from .registry import ProviderRegistry


def _model_dir(config: dict) -> Path:
    raw = config.get("model_dir", "~/.mlx_lm_server/models")
    return Path(os.path.expanduser(raw))


def _basename(repo: str) -> str:
    """Last /-separated component of the repo id."""
    return repo.split("/")[-1]


def _is_local_path(value: str | None) -> bool:
    return bool(value)


class MLXLMServerProvider(Provider):
    name = "mlx_lm_server"

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
        """Resolve the target side's on-disk directory, or None if it has
        neither local_path nor repo. local_path (a user-produced mlx-lm
        model directory) takes precedence when set."""
        local_path = variant.get("local_path")
        if local_path:
            return Path(local_path)
        repo = variant.get("repo")
        if repo:
            return _model_dir(self.config) / _basename(repo)
        return None

    def _draft_dir(self, variant: VariantSpec) -> Path | None:
        """Same resolution as _target_dir, for the speculative-decoding
        draft side (draft_local_path / draft_repo)."""
        local_path = variant.get("draft_local_path")
        if local_path:
            return Path(local_path)
        repo = variant.get("draft_repo")
        if repo:
            return _model_dir(self.config) / _basename(repo)
        return None

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        target = self._target_dir(variant)
        draft = self._draft_dir(variant)
        if target is None or draft is None:
            return False
        return (
            target.is_dir()
            and any(target.iterdir())
            and draft.is_dir()
            and any(draft.iterdir())
        )

    def _download_side(
        self,
        variant: VariantSpec,
        side: str,
        repo: str | None,
        local_path: str | None,
        on_progress: Callable[[str], None] | None,
    ) -> Path:
        """Resolve and (if repo-sourced) fetch one side of the pairing.
        Mirrors OMLXProvider.download's local_path-noop / snapshot_download
        branches, applied independently per side."""
        if local_path:
            # A local_path side is produced by the user (mlx_lm.convert,
            # dwq, etc.), not downloaded by modelman — this is a no-op
            # success once the directory is confirmed to exist. A missing
            # directory surfaces as a ValueError now, at "download" time,
            # rather than silently at serve time.
            p = Path(local_path)
            if not p.is_dir():
                raise ValueError(f"local_path does not exist: {p}")
            return p
        if repo:
            target = _model_dir(self.config) / _basename(repo)
            kwargs: dict[str, Any] = {"repo_id": repo, "local_dir": str(target)}
            with HF_DOWNLOAD_LOCK:
                ProgressTqdm.set_active_context(on_progress, lambda: self._cancel_requested)
                try:
                    if on_progress is not None:
                        kwargs["tqdm_class"] = ProgressTqdm
                    snapshot_download(**kwargs)
                    return target
                finally:
                    ProgressTqdm.clear_active_context()
        raise ValueError(f"mlx_lm_server variant {variant['id']} missing {side} source")

    def download(
        self,
        variant: VariantSpec,
        runner: _Runner | None = None,
        on_progress: Callable[[str], None] | None = None,
    ) -> str:
        self._cancel_requested = False
        target = self._download_side(
            variant, "target", variant.get("repo"), variant.get("local_path"), on_progress
        )
        self._download_side(
            variant,
            "draft",
            variant.get("draft_repo"),
            variant.get("draft_local_path"),
            on_progress,
        )
        return str(target)

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
        draft = self._draft_dir(variant)
        if target is None and draft is None:
            return None
        total = 0
        for d in (target, draft):
            if d is not None and d.is_dir():
                for f in d.rglob("*"):
                    if f.is_file():
                        total += f.stat().st_size
        return total or None

    def path_of(self, variant: VariantSpec) -> str | None:
        """Return the TARGET directory path only (never the draft's), or
        None if not downloaded. Keeping this target-only (rather than some
        combined/draft path) is what keeps find_shared_artifact_owner()
        meaningful — a draft model shared across multiple pairings
        shouldn't be spuriously matched as "the same artifact" as another
        pairing's target."""
        target = self._target_dir(variant)
        if target is None or not target.is_dir() or not any(target.iterdir()):
            return None
        return str(target)

    def delete(self, variant: VariantSpec, runner: _Runner | None = None) -> None:
        """Remove the target and draft on-disk directories, independently.

        A pairing may mix a repo-downloaded side with a local_path side (or
        vice versa): each side is evaluated on its own, and a local_path
        side — a directory the USER produced by hand (e.g. mlx_lm.convert
        or dwq output) that modelman never created — is never removed,
        regardless of whether that directory currently exists.
        """
        import shutil

        target = self._target_dir(variant)
        draft = self._draft_dir(variant)
        if target is None and draft is None:
            raise ValueError(f"mlx_lm_server variant {variant['id']} missing target/draft source")
        for d, local in (
            (target, _is_local_path(variant.get("local_path"))),
            (draft, _is_local_path(variant.get("draft_local_path"))),
        ):
            if d is not None and not local and d.exists():
                shutil.rmtree(d)

    def cleanup_partial_download(self, variant: VariantSpec) -> None:
        """Remove partially-populated target/draft directories left behind
        by a cancelled or failed download.

        Same per-side refinement-C exemption as delete(): a local_path side
        is never touched, even if the cleanup runs after a cancel/fail on
        the other side of the pairing.
        """
        import shutil

        target = self._target_dir(variant)
        draft = self._draft_dir(variant)
        for d, local in (
            (target, _is_local_path(variant.get("local_path"))),
            (draft, _is_local_path(variant.get("draft_local_path"))),
        ):
            if d is not None and not local and d.exists():
                shutil.rmtree(d)


ProviderRegistry.register(MLXLMServerProvider)
