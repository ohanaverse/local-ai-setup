"""oMLX provider — uses ~/.omlx/models/<basename-of-repo>/ for downloads."""
from __future__ import annotations

import os
from pathlib import Path
from typing import Any, Protocol

from huggingface_hub import snapshot_download

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

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        if not variant.get("repo"):
            return False
        target = _model_dir(self.config) / _basename(variant["repo"])
        return target.is_dir() and any(target.iterdir())

    def download(self, variant: VariantSpec, runner: _Runner | None = None) -> str:
        repo = variant.get("repo")
        if not repo:
            raise ValueError(f"omlx variant {variant['id']} missing repo")
        target = _model_dir(self.config) / _basename(repo)
        snapshot_download(repo_id=repo, local_dir=str(target))
        return str(target)

    def list_local(self, runner: _Runner | None = None) -> list[LocalModel]:
        models: list[LocalModel] = []
        md = _model_dir(self.config)
        if not md.exists():
            return models
        for d in md.iterdir():
            if d.is_dir():
                models.append({
                    "variant_id": d.name,
                    "path": str(d),
                    "size_bytes": None,
                })
        return models


ProviderRegistry.register(OMLXProvider)