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

# The mtplx serve port and derived base URLs — the single source of
# truth inside modelman: lifecycle.py (server management), registry.py
# (the provider template's auth.base_url), and local_control.py (probe
# fallback) all import from here. The bash (bin/lib/mtplx.sh) and Go
# (wt/internal/localgate) sides each keep their own single in-file
# constant — cross-language sharing happens through registry.toml's
# auth.base_url, not imports.
MTPLX_PORT = 8003
MTPLX_BASE = f"http://localhost:{MTPLX_PORT}"
MTPLX_V1_BASE = f"{MTPLX_BASE}/v1"


def _model_dir(config: dict) -> Path:
    raw = config.get("model_dir", "~/.mtplx/models")
    return Path(os.path.expanduser(raw))


def _dir_name(model_name: str) -> str:
    """Map an upstream `org/model` repo id to MTPLX's `<org>--<model>` dir."""
    return model_name.replace("/", "--")


def _repo_id(dir_name: str) -> str:
    """Map an MTPLX `<org>--<model>` dir name back to `org/model`.

    Round-trips correctly for multi-slash repo ids (`org/sub/model` <->
    `org--sub--model`), the common case. NOT a true inverse of `_dir_name`
    when an org/model segment itself contains a literal `--`: MTPLX's own
    `<org>--<model>` directory-naming convention (which this function only
    matches, not defines) has no way to distinguish a `--` that came from a
    `/` from one that was already there, so e.g. `org/model--v2` round-trips
    to `org/model/v2`, not the original id. Harmless in practice — real HF
    repo ids essentially never contain a literal double-hyphen — but not the
    exact inverse the earlier version of this docstring claimed."""
    return dir_name.replace("--", "/")


def _is_local_path_entry(variant: VariantSpec) -> bool:
    return bool(variant.get("local_path"))


class MTPLXProvider(Provider):
    name = "mtplx"
    manages_own_cache = True

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
        """Remove the cached model directory under ~/.mtplx/models.

        Mirrors oMLX's shared-artifact guard: a variant carrying an explicit
        `local_path` is treated as a user-produced artifact (even though the
        MTPLX form does not currently expose the field), and modelman must
        never delete those.
        """
        import shutil

        target = self._target_dir(variant)
        if target is None:
            raise ValueError(f"mtplx variant {variant['id']} missing name")
        if not _is_local_path_entry(variant) and target.exists():
            shutil.rmtree(target)


ProviderRegistry.register(MTPLXProvider)
