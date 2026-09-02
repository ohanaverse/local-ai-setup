"""Provider sync — reconcile configured models against provider state.

`modelman sync` updates the downloaded state of models already in
registry.toml; it never adds new models. Reconciles ollama (`ollama list`)
and the model-dir providers (llamacpp via HF cache, oMLX via model_dir).
See docs/superpowers/specs/2026-08-28-modelman-sync-ollama-reconcile-design.md
and docs/superpowers/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md.
"""

from __future__ import annotations

import subprocess
from dataclasses import dataclass, field
from typing import Any, Protocol

# Import the providers package to ensure ProviderRegistry is populated.
from . import providers  # noqa: F401
from .providers.base import Provider, VariantSpec
from .providers.ollama import _parse_ollama_list_sizes
from .providers.registry import ProviderRegistry
from .registry import (
    DEFAULT_PROVIDER_IDS,
    ModelEntry,
    Registry,
    default_provider_entry,
    provider_config,
    sync_agent_providers,
)
from .state import ModelState, StateStore

# Providers `modelman sync` reconciles against the filesystem. Same set as
# registry.DEFAULT_PROVIDER_IDS — that constant is the single source.
RECONCILABLE_PROVIDERS = DEFAULT_PROVIDER_IDS


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


def _model_entry_to_variant(entry: ModelEntry) -> VariantSpec:
    """Build a VariantSpec-shaped dict from a ModelEntry for provider APIs.

    This is the provider-only subset: it omits `cost`, which providers do
    not consume and which the UI layer serializes differently (Cost as a
    plain dict). Keeping the provider call lean avoids leaking UI-specific
    serialization into sync.
    """
    repo = entry.fetch.repo if entry.fetch else None
    files = entry.fetch.files if entry.fetch else None
    quantizations = entry.fetch.quantizations if entry.fetch else None
    return {
        "id": entry.id,
        "provider": entry.provider_id,
        "name": entry.model_name,
        "repo": repo,
        "files": files,
        "quantizations": quantizations,
        "location": entry.location,
        "model_info": dict(entry.model_info),
    }


def _ollama_downloaded(registry: Registry, sizes: dict[str, int]) -> dict[str, tuple[str, int]]:
    """Map ollama list output to {model_id: (disk_path, size_bytes)}.

    Only configured ollama models are returned; unconfigured models in `sizes`
    are ignored.
    """
    downloaded: dict[str, tuple[str, int]] = {}
    for m in registry.models:
        if m.provider_id != "ollama":
            continue
        size = sizes.get(m.model_name)
        if size is not None:
            downloaded[m.id] = (f"ollama:{m.model_name}", size)
    return downloaded


def _ensure_provider_entries(registry: Registry) -> list[str]:
    """Create default entries for reconcilable providers referenced by models
    but missing from the registry.

    Repairs a `providers = []` registry (models referencing a provider with
    no entry) so wt's fail-closed validation accepts it. Returns the ids of
    the provider entries added. Each entry is a fresh instance (via
    registry.default_provider_entry) so mutating one registry never corrupts
    the shared default.
    """
    referenced = {m.provider_id for m in registry.models}
    existing = {p.id for p in registry.providers}
    added: list[str] = []
    for pid in DEFAULT_PROVIDER_IDS:
        if pid in referenced and pid not in existing:
            registry.providers.append(default_provider_entry(pid))
            added.append(pid)
    return added


def _modeldir_providers(registry: Registry) -> dict[str, Provider]:
    """Build llamacpp/omlx provider instances from registry provider entries."""
    provider_instances: dict[str, Provider] = {}
    for provider_id in ("llamacpp", "omlx"):
        try:
            entry = registry.provider(provider_id)
        except KeyError:
            continue
        provider_instances[provider_id] = ProviderRegistry.get(provider_id, provider_config(entry))
    return provider_instances


def list_modeldir(
    registry: Registry, provider_instances: dict[str, Provider]
) -> dict[str, tuple[str, int]]:
    """Return {model_id: (disk_path, size_bytes)} for downloaded model-dir models."""
    downloaded: dict[str, tuple[str, int]] = {}
    for m in registry.models:
        if m.provider_id not in ("llamacpp", "omlx"):
            continue
        provider = provider_instances.get(m.provider_id)
        if provider is None:
            continue
        variant = _model_entry_to_variant(m)
        if not provider.is_downloaded(variant):
            continue
        disk_path = provider.path_of(variant)
        size = provider.size_of(variant)
        if disk_path is None or size is None:
            continue
        downloaded[m.id] = (disk_path, size)
    return downloaded


@dataclass
class SyncResult:
    downloaded: list[str] = field(default_factory=list)
    not_downloaded: list[str] = field(default_factory=list)
    providers_added: list[str] = field(default_factory=list)


def reconcile(
    registry: Registry, state: StateStore, downloaded: dict[str, tuple[str, int]]
) -> SyncResult:
    """Update downloaded/disk_path/size_bytes for configured reconcilable models.

    `downloaded` maps model_id -> (disk_path, size_bytes). Models not in the
    map are marked not downloaded. litellm_exposed is preserved (owned by the
    LiteLLM feature, not sync). Non-reconcilable providers are untouched.
    """
    result = SyncResult()
    for m in registry.models:
        if m.provider_id not in RECONCILABLE_PROVIDERS:
            continue
        existing = state.get(m.id)
        if m.id in downloaded:
            disk_path, size = downloaded[m.id]
            state.set(
                m.id,
                ModelState(
                    ready=True,
                    disk_path=disk_path,
                    size_bytes=size,
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
            result.downloaded.append(m.id)
        else:
            state.set(
                m.id,
                ModelState(
                    ready=False,
                    disk_path=None,
                    size_bytes=None,
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
    """Reconcile configured ollama and model-dir models against their providers."""
    providers_added = _ensure_provider_entries(registry)
    providers_added += sync_agent_providers(registry)
    downloaded = _ollama_downloaded(registry, list_ollama(runner))
    downloaded.update(list_modeldir(registry, _modeldir_providers(registry)))
    result = reconcile(registry, state, downloaded)
    result.providers_added = providers_added
    return result
