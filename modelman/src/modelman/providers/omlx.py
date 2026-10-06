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


def _exists(path: Path) -> bool:
    """Path.exists(), False instead of PermissionError (an unreadable parent
    can make the stat fail)."""
    try:
        return path.exists()
    except OSError:
        return False


def _is_dir(path: Path) -> bool:
    try:
        return path.is_dir()
    except OSError:
        return False


def _has_entries(path: Path) -> bool:
    """Whether directory `path` holds anything; False when it cannot be
    listed (unreadable, gone, not a directory)."""
    try:
        return any(path.iterdir())
    except OSError:
        return False


def _is_model_dir(path: Path) -> bool:
    # Existence, as omlx's _is_model_dir and wt's scan test it.
    return _exists(path / "config.json") and not _exists(path / "adapter_config.json")


def _scan_model_dirs(md: Path) -> tuple[list[Path], bool]:
    """Every model directory under `md` by omlx's two-level rule, in scan
    order and BEFORE its first-of-two-equal-names rule, plus whether a
    Hugging Face cache entry was seen. `md` itself is never among them."""

    def subdirs(path: Path) -> list[Path]:
        try:
            return sorted(d for d in path.iterdir() if d.is_dir() and not d.name.startswith("."))
        except OSError:
            return []

    found: list[Path] = []
    saw_hf_cache = False
    for entry in subdirs(md):
        if _exists(entry / "adapter_config.json"):
            continue
        if _is_model_dir(entry):
            found.append(entry)
            continue
        if entry.name.startswith("models--") and _is_dir(entry / "snapshots"):
            saw_hf_cache = True
            continue
        found.extend(child for child in subdirs(entry) if _is_model_dir(child))
    return found, saw_hf_cache


def omlx_model_dirs(md: Path) -> list[Path]:
    """The model directories omlx discovers under `md`, by its two-level rule
    (omlx 0.7.0, model_discovery.discover_models; wt's scan in
    wt/internal/localmodels/sources.go follows the same rule, and the two
    must list the same names):

    - an entry holding config.json is a model, named after the directory;
    - an entry holding adapter_config.json is a LoRA adapter, skipped;
    - any other entry is an organization folder: its child directories that
      hold config.json are models, named after the CHILD;
    - dot-directories are skipped, and the first of two equal names wins;
    - if nothing was found, `md` itself is the one model when it holds
      config.json and no adapter_config.json, unless it holds a Hugging Face
      cache entry. That condition is a deliberate conservative answer: omlx
      registers a model from a usable cache entry, and then its own fallback
      does not fire, so the fallback is skipped whenever an entry is present.
      When the entry is one omlx cannot use, omlx itself would take its
      fallback and serve `md`, and this scan does not list it.

    A Hugging Face cache entry (models--Org--Name/snapshots/...) is not
    listed: omlx names it by its own rules. A missing or unreadable `md` is
    no models, and an unreadable organization folder contributes nothing (as
    omlx and wt skip it), so one bad folder never hides the rest."""
    scanned, saw_hf_cache = _scan_model_dirs(md)
    found: dict[str, Path] = {}
    for path in scanned:
        found.setdefault(path.name, path)
    if not found and not saw_hf_cache and _is_model_dir(md):
        found[md.name] = md
    return [found[name] for name in sorted(found)]


def _is_local_path_entry(variant: VariantSpec) -> bool:
    return bool(variant.get("local_path"))


class OMLXProvider(Provider):
    name = "omlx"
    pooled = True

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
        falls back to the repo-keyed download directory, looked up in this
        order:

        1. the flat `<md>/<repo basename>`, where modelman downloads;
        2. `<md>/<repo>` — the repo's own `org/name` path — when that is a
           model directory strictly inside `md` (#261: omlx may store a
           model inside an organization folder);
        3. the one model directory under `md` with that basename, when
           exactly one has it. Two organizations can hold a model of the
           same name, and the first in sorted order is not this repo's: the
           path this returns is what delete() removes, so with two or more
           and none at the repo's own path it does not guess;
        4. otherwise the flat path — where a download goes — so the entry
           reads as not downloaded and a delete finds nothing to remove.

        Never `md` itself (omlx's single-model fallback): a delete would
        remove the user's configured model directory. Unreadable
        directories read as absent; nothing here raises on one."""
        local_path = variant.get("local_path")
        if local_path:
            # Stored as typed, so a leading `~` is still there; unexpanded it
            # names a directory literally called `~` and the entry reads as
            # not downloaded (#235). os.path's expanduser, as _model_dir
            # uses: pathlib's raises RuntimeError for a `~name` that is no
            # user, which would take `modelman sync` down with it.
            return Path(os.path.expanduser(local_path))
        repo = variant.get("repo")
        if repo:
            md = _model_dir(self.config)
            flat = md / repo_basename(repo)
            if _is_dir(flat):
                return flat
            own = md / repo
            if md in own.parents and ".." not in Path(repo).parts and _is_model_dir(own):
                return own
            named = [d for d in _scan_model_dirs(md)[0] if d.name == flat.name]
            if len(named) == 1:
                return named[0]
            return flat
        return None

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        target = self._target_dir(variant)
        if target is None:
            return False
        return _is_dir(target) and _has_entries(target)

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
            except BaseException:
                # Flips the cancellation flag immediately on a real Ctrl+C
                # (or any other interrupt) unwinding through this call.
                # This mostly does NOT extend should_cancel's actual reach:
                # the `finally` below clears the class-level active context
                # on the way out, and display() only ever reads
                # should_cancel from that same class-level slot (HF's
                # tqdm_class gets no per-instance kwargs — see
                # ProgressTqdm's docstring) — so a straggling worker thread
                # only sees this flip if its display() call lands in the
                # brief window before clear_active_context() runs. The
                # mechanism that actually protects a concurrent download is
                # a separate cancel_current() call landing while this
                # download's context is still set (e.g. from another
                # thread, mid-download) — this flip is belt-and-braces for
                # that path, not a guarantee for the real-interrupt case.
                self._cancel_requested = True
                raise
            finally:
                ProgressTqdm.clear_active_context()

    def list_local(self, runner: _Runner | None = None) -> list[LocalModel]:
        return [
            {"variant_id": d.name, "path": str(d), "size_bytes": None}
            for d in omlx_model_dirs(_model_dir(self.config))
        ]

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
        if not _is_dir(target) or not _has_entries(target):
            return None
        return str(target)

    def artifact_paths(self, variant: VariantSpec) -> frozenset[str]:
        """The directory this variant is configured to live in, present or
        not — unlike path_of(), which is for display and answers None until
        the weights are there. The shared-artifact guard has to see an entry
        whose directory it cannot currently read as well (#235), as it does
        for mtplx and mlx_lm_server."""
        target = self._target_dir(variant)
        return frozenset([str(target)]) if target is not None else frozenset()

    def removable_paths(self, variant: VariantSpec) -> frozenset[str]:
        """Nothing for a local_path entry: delete() and
        cleanup_partial_download() both leave a user-produced directory
        alone, so there is nothing for the shared-artifact guard to protect."""
        if _is_local_path_entry(variant):
            return frozenset()
        return self.artifact_paths(variant)

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
