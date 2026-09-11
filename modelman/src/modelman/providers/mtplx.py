"""MTPLX provider — discovers cached models under ~/.mtplx/models.

MTPLX manages its own cache via the `mtplx` CLI; modelman only discovers
what is already present. A cached model is a directory named
`<org>--<model>` (e.g. `Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality`)
under model_dir, while the registry `model_name` is the upstream
`org/model` repo id that `mtplx serve --model` accepts.
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Any

from .base import LocalModel, Provider, VariantSpec, _Runner
from .registry import ProviderRegistry


def _model_dir(config: dict) -> Path:
    raw = config.get("model_dir", "~/.mtplx/models")
    return Path(os.path.expanduser(raw))


def _dir_name(model_name: str) -> str:
    """Map an upstream `org/model` repo id to MTPLX's `<org>--<model>` dir."""
    return model_name.replace("/", "--")


def _repo_id(dir_name: str) -> str:
    """Map an MTPLX `<org>--<model>` dir name back to `org/model`.

    This is the exact inverse of `_dir_name`, so multi-slash repo ids
    round-trip correctly (`org/sub/model` <-> `org--sub--model`)."""
    return dir_name.replace("--", "/")


class MTPLXProvider(Provider):
    name = "mtplx"

    def _target_dir(self, variant: VariantSpec) -> Path | None:
        name = variant.get("name")
        if not name:
            return None
        return _model_dir(self.config) / _dir_name(name)

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        target = self._target_dir(variant)
        if target is None:
            return False
        return target.is_dir() and any(target.iterdir())

    def download(self, variant: VariantSpec, runner: _Runner | None = None, **kwargs: Any) -> str:
        # MTPLX manages its own cache via the `mtplx` CLI; modelman only
        # discovers what is already present.
        raise NotImplementedError(
            "MTPLX manages its own cache via the `mtplx` CLI; modelman only "
            "discovers already-cached models"
        )

    def list_local(self, runner: _Runner | None = None) -> list[LocalModel]:
        models: list[LocalModel] = []
        md = _model_dir(self.config)
        if not md.exists():
            return models
        for d in md.iterdir():
            if d.is_dir():
                models.append(
                    {
                        "variant_id": _repo_id(d.name),
                        "path": str(d),
                        "size_bytes": None,
                    }
                )
        return models

    def size_of(self, variant: VariantSpec) -> int | None:
        target = self._target_dir(variant)
        if target is None or not target.is_dir():
            return None
        total = 0
        for f in target.rglob("*"):
            if f.is_file():
                total += f.stat().st_size
        return total or None

    def path_of(self, variant: VariantSpec) -> str | None:
        target = self._target_dir(variant)
        if target is None or not target.is_dir() or not any(target.iterdir()):
            return None
        return str(target)

    def delete(self, variant: VariantSpec, runner: _Runner | None = None) -> None:
        import shutil

        target = self._target_dir(variant)
        if target is None:
            raise ValueError(f"mtplx variant {variant['id']} missing name")
        if target.exists():
            shutil.rmtree(target)


ProviderRegistry.register(MTPLXProvider)
