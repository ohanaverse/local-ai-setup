"""Provider sync — reconcile configured models against provider state.

First provider: ollama (`ollama list`). Sync updates the downloaded state
of models already in registry.toml; it never adds new models. See
docs/superpowers/specs/2026-08-28-modelman-sync-ollama-reconcile-design.md.
"""

from __future__ import annotations

import subprocess
from dataclasses import dataclass, field
from typing import Any, Protocol

from .registry import Registry
from .state import ModelState, StateStore

_SIZE_UNITS = {"B": 1, "KB": 1024, "MB": 1024**2, "GB": 1024**3, "TB": 1024**4}


def _parse_size(num_str: str, unit: str) -> int | None:
    """Parse a `<number> <UNIT>` size pair into bytes, or None if invalid."""
    unit = unit.upper()
    if unit not in _SIZE_UNITS:
        return None
    try:
        return int(float(num_str) * _SIZE_UNITS[unit])
    except ValueError:
        return None


def _parse_ollama_list_sizes(stdout: str) -> dict[str, int]:
    """Parse `ollama list` into {model_name: size_bytes} for downloaded models.

    Cloud rows (SIZE column `-`) are skipped — they are not downloaded.
    """
    sizes: dict[str, int] = {}
    for line in stdout.splitlines():
        line = line.strip()
        if not line or line.startswith("NAME"):
            continue
        parts = line.split()
        if len(parts) < 4:
            continue
        name = parts[0]
        if parts[2] == "-":
            continue
        size = _parse_size(parts[2], parts[3])
        if size is not None:
            sizes[name] = size
    return sizes


class SyncError(Exception):
    """Raised when a provider's discovery command fails."""


class _Runner(Protocol):
    def __call__(self, args: list[str], **kwargs: Any) -> Any: ...


def _default_runner(args: list[str], **kwargs: Any):
    return subprocess.run(args, **kwargs)


def list_ollama(runner: _Runner | None = None) -> dict[str, int]:
    """Run `ollama list` and return {model_name: size_bytes} for downloaded models."""
    r = (runner or _default_runner)(["ollama", "list"], capture_output=True, text=True)
    if r.returncode != 0:
        raise SyncError(f"`ollama list` failed (exit {r.returncode})")
    return _parse_ollama_list_sizes(r.stdout)


@dataclass
class SyncResult:
    downloaded: list[str] = field(default_factory=list)
    not_downloaded: list[str] = field(default_factory=list)


def reconcile(
    registry: Registry, state: StateStore, downloaded: dict[str, int]
) -> SyncResult:
    """Update downloaded/disk_path/size_bytes for configured ollama models.

    litellm_exposed is preserved (owned by the LiteLLM feature, not sync).
    Non-ollama models are untouched.
    """
    result = SyncResult()
    for m in registry.models:
        if m.provider_id != "ollama":
            continue
        existing = state.get(m.id)
        if m.model_name in downloaded:
            state.set(
                m.id,
                ModelState(
                    downloaded=True,
                    disk_path=f"ollama:{m.model_name}",
                    size_bytes=downloaded[m.model_name],
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
            result.downloaded.append(m.id)
        else:
            state.set(
                m.id,
                ModelState(
                    downloaded=False,
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
            result.not_downloaded.append(m.id)
    return result


def sync(
    registry: Registry,
    state: StateStore,
    runner: _Runner | None = None,
) -> SyncResult:
    """Reconcile configured ollama models against `ollama list`."""
    downloaded = list_ollama(runner)
    return reconcile(registry, state, downloaded)
