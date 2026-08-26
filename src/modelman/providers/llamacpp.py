"""llama.cpp provider — uses the HF cache; no separate model dir by default."""
from __future__ import annotations

import os
from pathlib import Path
from typing import Any, Protocol

from huggingface_hub import snapshot_download

from .base import LocalModel, Provider, VariantSpec
from .registry import ProviderRegistry


class _Runner(Protocol):
    def __call__(self, *args: Any, **kwargs: Any) -> Any: ...


def _hf_cache_dir() -> Path:
    return Path(os.environ.get("HF_HOME", "~/.cache/huggingface")).expanduser() / "hub"


def _files_in_hf_cache(repo: str, files: list[str]) -> bool:
    """Return True if every `file` exists somewhere in the repo's HF cache snapshots."""
    hf_org, hf_name = repo.split("/", 1)
    repo_dir = _hf_cache_dir() / f"models--{hf_org}--{hf_name}" / "snapshots"
    if not repo_dir.exists():
        return False
    for snap in repo_dir.iterdir():
        if all((snap / f).exists() for f in files):
            return True
    return False


class LlamaCppProvider(Provider):
    name = "llamacpp"

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        if not variant.get("repo") or not variant.get("files"):
            return False
        return _files_in_hf_cache(variant["repo"], variant["files"])

    def download(self, variant: VariantSpec, runner: _Runner | None = None) -> str:
        repo = variant.get("repo")
        files = variant.get("files")
        if not repo or not files:
            raise ValueError(f"llamacpp variant {variant['id']} missing repo/files")

        primary = files[0]
        path = snapshot_download(
            repo_id=repo,
            allow_patterns=files,
            cache_dir=_hf_cache_dir(),
        )
        return str(Path(path) / primary)

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
                        models.append({
                            "variant_id": str(f.relative_to(snap)),
                            "path": str(f),
                            "size_bytes": f.stat().st_size,
                        })
        return models


ProviderRegistry.register(LlamaCppProvider)