"""Provider sync — refresh registry.toml + modelman.toml from live providers.

First provider: ollama (`ollama list`). See
docs/superpowers/specs/2026-08-27-modelman-sync-ollama-design.md.
"""

from __future__ import annotations

import subprocess
from dataclasses import dataclass, field
from typing import Any, Protocol

from .ollama_caps import auto_detect_model_info
from .registry import ModelEntry, Registry
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


def _parse_ollama_list(stdout: str) -> tuple[list[ModelEntry], dict[str, int]]:
    """Parse `ollama list` output into (models, sizes).

    The SIZE column's first token is `-` for cloud models (not pulled
    locally) and a number for local models. `sizes` maps tag -> size_bytes
    for local models only.
    """
    models: list[ModelEntry] = []
    sizes: dict[str, int] = {}
    for line in stdout.splitlines():
        line = line.strip()
        if not line or line.startswith("NAME"):
            continue
        parts = line.split()
        if len(parts) < 3:
            continue
        name = parts[0]
        if parts[2] == "-":
            location = "cloud"
        else:
            location = "local"
            if len(parts) >= 4:
                size = _parse_size(parts[2], parts[3])
                if size is not None:
                    sizes[name] = size
        models.append(
            ModelEntry(
                id=f"ollama/{name}",
                family=name,
                provider_id="ollama",
                model_name=name,
                location=location,
                source="discovered",
            )
        )
    return models, sizes


class SyncError(Exception):
    """Raised when a provider's discovery command fails."""


class _Runner(Protocol):
    def __call__(self, args: list[str], **kwargs: Any) -> Any: ...


def _default_runner(args: list[str], **kwargs: Any):
    return subprocess.run(args, **kwargs)


def discover_ollama(
    runner: _Runner | None = None,
) -> tuple[list[ModelEntry], dict[str, int]]:
    """Run `ollama list` and return (models, sizes)."""
    r = (runner or _default_runner)(["ollama", "list"], capture_output=True, text=True)
    if r.returncode != 0:
        raise SyncError(f"`ollama list` failed (exit {r.returncode})")
    return _parse_ollama_list(r.stdout)


@dataclass
class SyncResult:
    added: list[str] = field(default_factory=list)
    refreshed: list[str] = field(default_factory=list)
    skipped: list[str] = field(default_factory=list)


def merge(registry: Registry, discovered: list[ModelEntry]) -> SyncResult:
    """Apply curated-wins merge in place. Returns a SyncResult.

    - id absent -> append (added).
    - id present and source == "curated" -> skip (skipped).
    - id present and source == "discovered" -> refresh location (refreshed).

    tags/cost/family are never touched.
    """
    result = SyncResult()
    by_id = {m.id: m for m in registry.models}
    for d in discovered:
        existing = by_id.get(d.id)
        if existing is None:
            registry.models.append(d)
            by_id[d.id] = d
            result.added.append(d.id)
        elif existing.source == "curated":
            result.skipped.append(d.id)
        else:
            existing.location = d.location
            result.refreshed.append(d.id)
    return result


def update_state(
    state: StateStore, models: list[ModelEntry], sizes: dict[str, int]
) -> None:
    """Update downloaded/disk_path/size_bytes for discovered models.

    local -> downloaded=True + disk_path + size_bytes; cloud ->
    downloaded=False. litellm_exposed is preserved (owned by the LiteLLM
    feature, not sync).
    """
    for m in models:
        existing = state.get(m.id)
        if m.location == "local":
            state.set(
                m.id,
                ModelState(
                    downloaded=True,
                    disk_path=f"ollama:{m.model_name}",
                    size_bytes=sizes.get(m.model_name),
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
        else:
            state.set(
                m.id,
                ModelState(
                    downloaded=False,
                    litellm_exposed=existing.litellm_exposed,
                ),
            )


def sync(
    registry: Registry,
    state: StateStore,
    runner: _Runner | None = None,
) -> SyncResult:
    """Discover ollama models, merge into the registry, update state."""
    models, sizes = discover_ollama(runner)
    existing_ids = {m.id for m in registry.models}
    for m in models:
        if m.id not in existing_ids:
            m.model_info = auto_detect_model_info(m.model_name, runner)
    result = merge(registry, models)
    update_state(state, models, sizes)
    return result
