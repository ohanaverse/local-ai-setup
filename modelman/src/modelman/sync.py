"""Provider sync — reconcile configured models against provider state.

`modelman sync` updates the downloaded state of models already in
registry.toml; it never adds new models. Reconciles ollama (`ollama list`)
and the model-dir providers (llamacpp via HF cache, oMLX via model_dir).
See docs/superpowers/specs/2026-08-28-modelman-sync-ollama-reconcile-design.md
and docs/superpowers/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md.
"""

from __future__ import annotations

import shutil
import subprocess
from dataclasses import dataclass, field, replace
from typing import Any, Protocol

# Import the providers package to ensure ProviderRegistry is populated.
from . import providers  # noqa: F401
from .providers.base import Provider, VariantSpec
from .providers.ollama import _listed_name, _parse_ollama_list_sizes
from .providers.registry import ProviderRegistry
from .registry import (
    _DEFAULT_PROVIDER_TEMPLATES,
    DEFAULT_PROVIDER_IDS,
    ModelEntry,
    Registry,
    default_provider_entry,
    is_model_local,
    provider_config,
    sync_agent_providers,
)
from .state import StateStore

# Providers `modelman sync` reconciles against the filesystem. ollama has its
# own discovery path (`ollama list`); every other reconcilable provider stores
# weights in per-model directories on disk. This tuple is derived from
# DEFAULT_PROVIDER_IDS by excluding ollama (the only default that uses a
# daemon-list discovery path), plus retired-but-still-registered providers
# (llamacpp) so existing registry entries continue to be reconciled until
# removed. Adding a new model-directory provider to DEFAULT_PROVIDER_IDS
# automatically includes it here — no second update needed.
MODELDIR_PROVIDER_IDS: tuple[str, ...] = tuple(
    sorted(set(DEFAULT_PROVIDER_IDS) - {"ollama"} | {"llamacpp"})
)

# Full set of providers sync can determine downloaded state for: ollama plus
# every model-dir provider. Computed from MODELDIR_PROVIDER_IDS so the set
# cannot drift.
RECONCILABLE_PROVIDERS: tuple[str, ...] = tuple(sorted(set(("ollama",) + MODELDIR_PROVIDER_IDS)))


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
    local_path = entry.fetch.local_path if entry.fetch else None
    draft_repo = entry.draft.repo if entry.draft else None
    draft_local_path = entry.draft.local_path if entry.draft else None
    return {
        "id": entry.id,
        "provider": entry.provider_id,
        "name": entry.model_name,
        "repo": repo,
        "files": files,
        "quantizations": quantizations,
        "local_path": local_path,
        "draft_repo": draft_repo,
        "draft_local_path": draft_local_path,
        "location": entry.location,
        "model_info": dict(entry.model_info),
        "quantization": entry.quantization,
    }


def _ollama_downloaded(registry: Registry, sizes: dict[str, int]) -> dict[str, tuple[str, int]]:
    """Map ollama list output to {model_id: (disk_path, size_bytes)}.

    Only configured ollama models are returned; unconfigured models in `sizes`
    are ignored. A tagless registry name matches its `:latest` row, as the
    TUI's reconcile (OllamaProvider.resolve_local) and `modelman start` read
    it: reconcile clears the running flag of a model that is not here (#233),
    so a pulled `x:latest` must not read as a missing `x`.
    """
    downloaded: dict[str, tuple[str, int]] = {}
    for m in registry.models:
        if m.provider_id != "ollama":
            continue
        listed = _listed_name(m.model_name, sizes)
        if listed is not None:
            downloaded[m.id] = (f"ollama:{listed}", sizes[listed])
    return downloaded


# The command that shows a default local provider is installed. mlx_lm_server
# has none of its own (it is a module run from omlx's bundled Python), so it
# only ever gets a row from a model that references it.
_LOCAL_PROVIDER_COMMANDS: dict[str, str] = {"ollama": "ollama", "omlx": "omlx", "mtplx": "mtplx"}


def _installed_local_providers() -> list[str]:
    """The default local providers whose command is on PATH."""
    return [pid for pid, cmd in _LOCAL_PROVIDER_COMMANDS.items() if shutil.which(cmd)]


def _ensure_provider_entries(registry: Registry) -> list[str]:
    """Create default entries for the reconcilable providers that are missing
    from the registry and are either referenced by a model or installed on
    this machine.

    Referenced: repairs a `providers = []` registry (models referencing a
    provider with no entry) so wt's fail-closed validation accepts it.
    Installed: on a fresh machine no model references anything yet, and
    without a provider row a pulled model is unknown to `modelman start` and
    invisible to wt — so a local provider whose command is on PATH gets its
    row too. One that is not installed does not: wt would probe a server the
    machine does not have. Nor does an installed `omlx` when an `omlx-6bit`
    row exists: the two are one server, so the tool already has its row, and
    a second one (with the default model_dir) would change which row
    discovery and the running-flag probe use.

    There is deliberately no opt-out: a row deleted by hand for an installed
    tool comes back on the next sync.

    Returns the ids of the provider entries added. Each entry is a fresh
    instance (via registry.default_provider_entry) so mutating one registry
    never corrupts the shared default.
    """
    referenced = {m.provider_id for m in registry.models}
    existing = {p.id for p in registry.providers}
    referenced.update(
        pid
        for pid in _installed_local_providers()
        if not (pid == "omlx" and "omlx-6bit" in existing)
    )
    added: list[str] = []
    for pid in DEFAULT_PROVIDER_IDS:
        if pid in referenced and pid not in existing:
            registry.providers.append(default_provider_entry(pid))
            added.append(pid)
    return added


def backfill_provider_defaults(registry: Registry) -> Registry:
    """Fill auth.base_url/protocols on an *existing* provider entry from
    its default template when the field is missing — _ensure_provider_entries
    only ever appends providers that are wholly absent, so a provider added
    before this field existed (every pre-upgrade omlx entry) would
    otherwise stay permanently unroutable in direct mode. Never overwrites
    a value the user already set, and providers without a default template
    (e.g. openrouter) are left alone."""
    for provider in registry.providers:
        template = _DEFAULT_PROVIDER_TEMPLATES.get(provider.id)
        if template is None:
            continue
        if not provider.auth.base_url and template.auth.base_url:
            provider.auth.base_url = template.auth.base_url
        if not provider.protocols and template.protocols:
            provider.protocols = list(template.protocols)
    return registry


def _modeldir_providers(registry: Registry) -> dict[str, Provider]:
    """Build model-dir provider instances from registry provider entries.

    Covers every local provider that stores its weights on disk under a
    per-model directory. The set is defined by MODELDIR_PROVIDER_IDS.
    """
    provider_instances: dict[str, Provider] = {}
    for entry in registry.providers:
        if not _is_modeldir_row(entry.id):
            continue
        provider_instances[entry.id] = ProviderRegistry.get(entry.id, provider_config(entry))
    return provider_instances


def _is_modeldir_row(provider_id: str) -> bool:
    """Whether registry row `provider_id` is a model-directory provider: by
    the class it resolves to, not its raw id, so a second row for the same
    server (`omlx-6bit`, which is omlx's) is reconciled like the first. The
    TUI's reconcile already resolved rows this way; a fixed id list here made
    `modelman sync` alone skip those entries (#225)."""
    return ProviderRegistry.resolve(provider_id) in MODELDIR_PROVIDER_IDS


def list_modeldir(
    registry: Registry, provider_instances: dict[str, Provider]
) -> dict[str, tuple[str, int]]:
    """Return {model_id: (disk_path, size_bytes)} for downloaded model-dir models."""
    downloaded: dict[str, tuple[str, int]] = {}
    for m in registry.models:
        if not _is_modeldir_row(m.provider_id):
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
    map are marked not downloaded. Non-reconcilable providers are untouched,
    and so are cloud models on them (ollama `:cloud` stubs): they have no
    on-disk artifact to observe, so their ready flag is left as-is.
    """
    result = SyncResult()
    for m in registry.models:
        # By the class the row resolves to, like _is_modeldir_row: an entry on
        # an `omlx-6bit` row is omlx's and is reconciled with it (#225).
        if ProviderRegistry.resolve(
            m.provider_id
        ) not in RECONCILABLE_PROVIDERS or not is_model_local(
            m.location, m.provider_id, registry, missing_provider_is_local=True
        ):
            continue
        # Only what reconcile observes is written — ready, disk_path and
        # size_bytes. The rest of the row is kept: rebuilding it reset
        # `running` to its default, so every sync recorded every registered
        # local model as stopped (#231). Whether a model that is gone from
        # disk can still be running is the probe's call
        # (local_control.running_model_ids, which `modelman sync` and the
        # TUI's mount both run after this), not a side effect here.
        current = state.get(m.id)
        if m.id in downloaded:
            disk_path, size = downloaded[m.id]
            state.set(m.id, replace(current, ready=True, disk_path=disk_path, size_bytes=size))
            result.downloaded.append(m.id)
        else:
            state.set(m.id, replace(current, ready=False, disk_path=None, size_bytes=None))
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
    # Repair pre-existing provider entries that predate a template field
    # (omlx's auth.base_url, any provider's protocols) — idempotent, and a
    # no-op for entries the user has already populated.
    backfill_provider_defaults(registry)
    downloaded = _ollama_downloaded(registry, list_ollama(runner))
    downloaded.update(list_modeldir(registry, _modeldir_providers(registry)))
    result = reconcile(registry, state, downloaded)
    result.providers_added = providers_added
    return result
