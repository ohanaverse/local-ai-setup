"""mlx-lm server provider — one variant is a target+draft pairing served by
`mlx_lm.server --draft-model` for speculative decoding. Mirrors omlx.py's
structure: each side (target, draft) resolves independently from either a
locally-produced directory (`local_path`/`draft_local_path`) or a Hugging
Face repo (`repo`/`draft_repo`) downloaded into
~/.mlx_lm_server/models/<basename-of-repo>/.
"""

from __future__ import annotations

import os
import weakref
from collections.abc import Callable
from pathlib import Path
from typing import Any

from huggingface_hub import snapshot_download

from ._progress import HF_DOWNLOAD_LOCK, ProgressTqdm, repo_basename
from .base import LocalModel, Provider, VariantSpec, _Runner
from .registry import ProviderRegistry


def _model_dir(config: dict) -> Path:
    raw = config.get("model_dir", "~/.mlx_lm_server/models")
    return Path(os.path.expanduser(raw))


def _resolve_local_path(raw: str) -> Path:
    """Normalize a user-supplied local_path the same way
    modelman.benchmark.isolation._normalize_pairing_arg does (expanduser +
    abspath + normpath), so the provider and the isolation helper agree on
    which directory a relative or tilde path names. Otherwise a registry
    local_path like `~/mlx/quant/dwq-model` is read literally (a directory
    literally named `~`) by reconcile/delete while the benchmark expands it
    to $HOME — the ready-state and the benchmark then disagree about where
    the weights live. normpath() collapses '..' and '.' components so both
    sides normalize '~/models/../quant' to '/Users/keith/quant'."""
    return Path(os.path.normpath(os.path.abspath(os.path.expanduser(raw))))


def _is_local_path_entry(value: str | None) -> bool:
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
            return _resolve_local_path(local_path)
        repo = variant.get("repo")
        if repo:
            return _model_dir(self.config) / repo_basename(repo)
        return None

    def _draft_dir(self, variant: VariantSpec) -> Path | None:
        """Same resolution as _target_dir, for the speculative-decoding
        draft side (draft_local_path / draft_repo)."""
        local_path = variant.get("draft_local_path")
        if local_path:
            return _resolve_local_path(local_path)
        repo = variant.get("draft_repo")
        if repo:
            return _model_dir(self.config) / repo_basename(repo)
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
            # success once the directory is confirmed to exist and be
            # non-empty. A missing (or empty) directory surfaces as a
            # ValueError now, at "download" time, rather than silently at
            # serve time — or, for an empty dir, as a ready-state that
            # oscillates: `is_downloaded` requires any(iterdir()), so an
            # empty dir would flip straight back to not-ready on the next
            # reconcile.
            p = _resolve_local_path(local_path)
            if not p.is_dir():
                raise ValueError(f"local_path does not exist: {p}")
            if not any(p.iterdir()):
                raise ValueError(f"local_path is empty: {p}")
            return p
        if repo:
            target = _model_dir(self.config) / repo_basename(repo)
            kwargs: dict[str, Any] = {"repo_id": repo, "local_dir": str(target)}
            with HF_DOWNLOAD_LOCK:
                # Use a weak reference to avoid keeping the entire
                # MLXLMServerProvider instance alive for the download
                # duration. The lambda captures only the weakref object.
                weak_self = weakref.ref(self)
                ProgressTqdm.set_active_context(
                    on_progress, lambda: getattr(weak_self(), "_cancel_requested", False)
                )
                try:
                    if on_progress is not None:
                        kwargs["tqdm_class"] = ProgressTqdm
                    snapshot_download(**kwargs)
                    return target
                finally:
                    ProgressTqdm.clear_active_context()
        raise ValueError(
            f"mlx_lm_server variant {variant['id']} is missing its {side} "
            "model — a target+draft speculative-decoding pairing needs a "
            f"source (repo or local_path) for the {side} side"
        )

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
        # Dedupe the dirs first: both sides resolving to the same directory
        # (e.g. a hand-edited config pointing target and draft at one dir)
        # must be summed once, not twice — the old code double-counted it.
        total = 0
        seen: set[Path] = set()
        for d in (target, draft):
            if d is None or not d.is_dir() or d in seen:
                continue
            seen.add(d)
            for f in d.rglob("*"):
                if f.is_file():
                    total += f.stat().st_size
        return total or None

    def path_of(self, variant: VariantSpec) -> str | None:
        """Return the TARGET directory path (for display/sync), or None if
        the target is not present.

        The UI and sync only need one path for status display; the draft is
        tracked separately for shared-artifact safety via artifact_paths()."""
        target = self._target_dir(variant)
        if target is None or not target.is_dir() or not any(target.iterdir()):
            return None
        return str(target)

    def artifact_paths(self, variant: VariantSpec) -> frozenset[str]:
        """Return the configured target and draft paths, whether present or not.

        A draft model shared across multiple pairings must be detected as
        still in use, otherwise one pairing's delete can silently remove the
        draft weights another pairing needs. path_of() intentionally stays
        target-only for display; this method is the ownership check.

        Unlike path_of() — which is display-only and requires the directory
        to exist — this method returns the paths the variant is configured
        to use (local_path or repo-derived, per side), so shared-artifact
        detection works even before download. Callers in queue.py and
        downloads.py treat a path conflict as "another entry owns this
        artifact" regardless of whether it's on disk yet.
        """
        paths: list[str] = []
        # Target side: local_path takes precedence; otherwise repo-derived dir.
        target_local = variant.get("local_path")
        target_repo = variant.get("repo")
        if target_local:
            paths.append(str(_resolve_local_path(target_local)))
        elif target_repo:
            target_dir = self._repo_dir(target_repo)
            if target_dir is not None:
                paths.append(str(target_dir))
        # Draft side: same resolution order.
        draft_local = variant.get("draft_local_path")
        draft_repo = variant.get("draft_repo")
        if draft_local:
            paths.append(str(_resolve_local_path(draft_local)))
        elif draft_repo:
            draft_dir = self._repo_dir(draft_repo)
            if draft_dir is not None:
                paths.append(str(draft_dir))
        return frozenset(paths)

    def _repo_dir(self, repo: str | None) -> Path | None:
        """The on-disk directory a repo-downloaded side would occupy under
        model_dir/<basename> — the artifact modelman itself creates when it
        downloads that side. Distinct from _target_dir/_draft_dir because
        local_path takes precedence there: when a side carries BOTH a repo
        and a local_path (a hand-edited config; the forms reject it), the
        local_path dir is user-produced and exempt, but the repo-downloaded
        dir under model_dir is modelman's own and becomes orphaned unless
        delete/cleanup also consider it."""
        if not repo:
            return None
        return _model_dir(self.config) / repo_basename(repo)

    def _removable_dirs(self, variant: VariantSpec) -> list[Path]:
        """Every on-disk directory delete/cleanup may remove for this variant:
        each side's repo-downloaded dir plus (when the side has no local_path)
        the resolved target/draft dir. A local_path dir — produced by the user,
        never by modelman — is never included, even when it takes precedence
        over a repo on the same side."""
        dirs: list[Path] = []
        # target side
        target_repo = self._repo_dir(variant.get("repo"))
        if not _is_local_path_entry(variant.get("local_path")):
            target = self._target_dir(variant)
            if target is not None:
                dirs.append(target)
        elif target_repo is not None:
            # local_path precedence: only the repo-downloaded dir is removable.
            dirs.append(target_repo)
        # draft side
        draft_repo = self._repo_dir(variant.get("draft_repo"))
        if not _is_local_path_entry(variant.get("draft_local_path")):
            draft = self._draft_dir(variant)
            if draft is not None:
                dirs.append(draft)
        elif draft_repo is not None:
            dirs.append(draft_repo)
        return list(dict.fromkeys(dirs))

    def delete(self, variant: VariantSpec, runner: _Runner | None = None) -> None:
        """Remove the target and draft on-disk directories, independently.

        A pairing may mix a repo-downloaded side with a local_path side (or
        vice versa): each side is evaluated on its own, and a local_path
        side — a directory the USER produced by hand (e.g. mlx_lm.convert
        or dwq output) that modelman never created — is never removed,
        regardless of whether that directory currently exists.
        """
        import shutil

        if self._target_dir(variant) is None and self._draft_dir(variant) is None:
            raise ValueError(f"mlx_lm_server variant {variant['id']} missing target/draft source")
        for d in self._removable_dirs(variant):
            if d.exists():
                shutil.rmtree(d)

    def cleanup_partial_download(self, variant: VariantSpec) -> None:
        """Remove partially-populated target/draft directories left behind
        by a cancelled or failed download.

        Same per-side refinement-C exemption as delete(): a local_path side
        is never touched, even if the cleanup runs after a cancel/fail on
        the other side of the pairing.
        """
        import shutil

        for d in self._removable_dirs(variant):
            if d.exists():
                shutil.rmtree(d)


ProviderRegistry.register(MLXLMServerProvider)
