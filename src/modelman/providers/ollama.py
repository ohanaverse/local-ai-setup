"""Ollama provider — uses `ollama pull`, `ollama show`, `ollama list`."""
from __future__ import annotations

from pathlib import Path
from typing import Any, Protocol

from .base import LocalModel, Provider, VariantSpec
from .registry import ProviderRegistry


class _Runner(Protocol):
    def __call__(self, args: list[str], **kwargs: Any) -> Any: ...


def _default_runner(args: list[str], **kwargs: Any):
    import subprocess
    return subprocess.run(args, **kwargs)


class OllamaProvider(Provider):
    name = "ollama"

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        r = (runner or _default_runner)(["ollama", "show", variant["name"]], capture_output=True, text=True)
        return r.returncode == 0

    def download(self, variant: VariantSpec, runner: _Runner | None = None) -> str:
        r = (runner or _default_runner)(["ollama", "pull", variant["name"]])
        if r.returncode != 0:
            raise RuntimeError(f"`ollama pull {variant['name']}` failed (exit {r.returncode})")
        return f"ollama:{variant['name']}"

    def list_local(self, runner: _Runner | None = None) -> list[LocalModel]:
        r = (runner or _default_runner)(["ollama", "list"], capture_output=True, text=True)
        models: list[LocalModel] = []
        for line in r.stdout.splitlines():
            line = line.strip()
            if not line or line.startswith("NAME"):
                continue
            parts = line.split()
            if len(parts) >= 1:
                models.append({
                    "variant_id": parts[0],
                    "path": f"ollama:{parts[0]}",
                    "size_bytes": None,
                })
        return models


ProviderRegistry.register(OllamaProvider)