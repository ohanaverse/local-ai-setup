"""modelman-owned local-model lifecycle control (issue #65 — one local
model at a time). See docs/superpowers/specs/2026-09-10-one-local-model-
at-a-time-design.md.

`modelman start`/`modelman stop` (main.py) are the only place a local
model's process is started or stopped for normal (non-benchmark) usage.
Both delegate the actual stop/start to bin/llm-isolate-provider via
modelman.benchmark.isolation — the same subprocess contract `modelman
benchmark` uses — and record which model is running in modelman.toml's
`[local].running_model` (state.py's LocalState), the marker wt's model
picker reads read-only to filter its catalog to this one local model plus
cloud models.

State ownership: these functions own the marker's read/write lifecycle
entirely — they read it with load_state() and write it with short
locked_state() transactions (state.py), while the stop-all/isolate/warmup
subprocesses run OUTSIDE any lock. `locked_state` is an atomic
read-modify-write of the whole file, and holding it across a warmup that
can block for minutes would (a) leave wt reading a stale marker for that
whole window and (b) serialize any future in-process caller behind a
minutes-long hold. The marker writes are the only mutations made here, so
the short transactions stay correct.
"""

from __future__ import annotations

import subprocess
from collections import defaultdict
from dataclasses import dataclass, field
from pathlib import Path

# Import the providers package to ensure ProviderRegistry is populated —
# mirrors sync.py's identical defensive import.
from . import providers  # noqa: F401
from .benchmark.errors import BenchmarkError
from .benchmark.isolation import (
    SUPPORTED_PROVIDER_IDS,
    isolate_provider,
    mlx_lm_server_pairing_args,
    stop_all_local_providers,
)
from .litellm import ExposeError, default_litellm_config_path, expose_model
from .local_process import ENV_VAR_BY_PROVIDER as _ENV_VAR_BY_PROVIDER
from .local_process import http_models_ids as _http_models_ids
from .providers.base import LocalModel, Provider
from .providers.mtplx import MTPLX_BASE
from .providers.registry import ProviderRegistry
from .registry import (
    LOCATION_LOCAL,
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
    base_origin,
    is_local_location,
    known_families,
    locked_registry,
    model_entry_to_variant,
    model_has_local_artifact,
    provider_config,
)
from .state import ModelState, StateStore, load_state, locked_state

# Probe endpoint fallbacks for providers whose registry entry lacks an
# auth.base_url (mirroring _DEFAULT_PROVIDER_TEMPLATES in registry.py).
_DEFAULT_BASE_ORIGIN = {
    "ollama": "http://localhost:11434",
    "omlx": "http://localhost:8000",
    "omlx-6bit": "http://localhost:8000",
    "mlx_lm_server": "http://localhost:8001",
    "mtplx": MTPLX_BASE,
}

# Subprocess seam so tests can keep the probe hermetic (conftest patches
# it); production calls subprocess.run directly.
_default_runner = subprocess.run


class LocalControlError(Exception):
    """Raised for user-facing `modelman start`/`modelman stop` failures."""


class DiscoveredModelNeedsFamily(LocalControlError):
    """Raised by start_local_model() when `model_id` matches an on-disk,
    unregistered artifact but no `family` was supplied. The CLI catches
    this, prompts the user, and retries with `family` set — nothing is
    written to registry.toml/modelman.toml/LiteLLM before this is raised.
    """

    def __init__(self, provider_id: str, variant_id: str, suggested_families: list[str]):
        self.provider_id = provider_id
        self.variant_id = variant_id
        self.suggested_families = suggested_families
        super().__init__(
            f"{provider_id}/{variant_id} is on disk but not registered — a family is required"
        )


@dataclass
class StartResult:
    model_id: str
    already_running: bool
    direct_url: str | None = None
    warnings: list[str] = field(default_factory=list)


@dataclass
class StopResult:
    # The marker that was cleared, or None if nothing was running.
    stopped_model_id: str | None


@dataclass
class DiscoveredModel:
    """An artifact a provider reports on disk with no matching registry.toml
    entry (no ModelEntry sharing its (provider_id, model_name))."""

    provider_id: str
    variant_id: str
    path: str
    size_bytes: int | None


@dataclass
class InventoryEntry:
    model_id: str
    running: bool
    size_bytes: int | None


@dataclass
class LocalModelInventory:
    downloaded: list[InventoryEntry]
    not_downloaded: list[str]
    discovered: list[DiscoveredModel]
    # Provider ids modelman could not ask (no Provider class registered for
    # the id, or the provider raised). For these, "not downloaded"/"nothing
    # discovered" means UNKNOWN, not "confirmed absent" — a stopped ollama
    # daemon would otherwise silently relabel every registered ollama model
    # as missing. The CLI prints a caveat naming them.
    unqueryable_providers: list[str] = field(default_factory=list)


def _ollama_loaded_names() -> list[str]:
    """Model names ollama currently has LOADED (`ollama ps` first column) —
    not `ollama list`'s downloaded-but-idle catalog. Empty on any failure:
    a down daemon has nothing loaded."""
    try:
        result = _default_runner(
            ["ollama", "ps"], capture_output=True, text=True, check=False
        )
    except OSError:
        return []
    if result.returncode != 0:
        return []
    names = []
    for line in result.stdout.splitlines()[1:]:  # skip the NAME header
        fields = line.split()
        if fields:
            names.append(fields[0])
    return names


def _name_matches(served: str, want: str) -> bool:
    """Whether a server-reported model id names the same model as `want`.

    Lenient on prefix (a server may report a path-ish spelling of the same
    model), strict on suffix (the quantization/variant tail is exactly what
    distinguishes omlx's 4-bit from 6-bit variants, which share one port —
    a mismatched tail is a different model, never a spelling variant).
    """
    return served == want or served.endswith("/" + want) or want.endswith("/" + served)


def _probe_running(provider_id: str, model_name: str, base_origin_url: str | None) -> bool:
    """Best-effort: is this model actually serving right now?

    False on any failure — a false "not running" only costs a harmless
    stop+reload; a false "yes" would make `modelman start` a no-op exactly
    when it's needed (the marker's process died; see the idempotency note
    in start_local_model).
    """
    if provider_id == "ollama":
        return any(_name_matches(n, model_name) for n in _ollama_loaded_names())
    base = base_origin_url or _DEFAULT_BASE_ORIGIN.get(provider_id)
    if not base:
        return False
    ids = _http_models_ids(f"{base}/v1/models")
    if provider_id == "mlx_lm_server":
        # One target+draft pairing per process (bin/llm-isolate-provider's
        # mlx_lm_server branch is the only thing that starts one): the
        # server loads its model before serving, so a non-empty /v1/models
        # is already model-accurate. An exact-name check would false-fail
        # permanently when the registry's local_path/repo spelling differs
        # from what the server reports, turning every idempotent
        # `modelman start` into an unnecessary reload.
        return bool(ids)
    return any(_name_matches(served, model_name) for served in ids)


def _clear_stale_marker(expected: tuple[str, ...], state_path: Path | None) -> None:
    """Best-effort: clear [local].running_model when it still names one of
    `expected` — models stop-all has torn down or failed to start, so any
    remaining marker naming them is stale (wt's gate treats a marker whose
    probe fails as fatal for every launch; clearing it degrades to
    cloud-only instead). A marker naming something else means a concurrent
    writer moved on; leave it alone."""
    try:
        with locked_state(state_path) as fresh:
            if fresh.local.running_model in expected:
                fresh.local.running_model = None
    except OSError:
        pass  # the LocalControlError about the failed start is the user's answer


def _provider_entry(registry: Registry, provider_id: str) -> ProviderEntry | None:
    """The registry.toml provider row for `provider_id`, or None."""
    return next((p for p in registry.providers if p.id == provider_id), None)


def _provider_instance(registry: Registry, provider_id: str) -> Provider | None:
    """A live Provider for `provider_id`, or None when it cannot be built (no
    registry.toml row, nothing registered under that id — e.g. a hand-edited
    "omlx-6bit" row today — or construction raised).

    None always means "this provider cannot be asked", never "this provider
    reports nothing": every caller has to keep those apart (see
    LocalModelInventory.unqueryable_providers).
    """
    entry = _provider_entry(registry, provider_id)
    if entry is None:
        return None
    if ProviderRegistry.get_class(provider_id) is None:
        return None
    try:
        return ProviderRegistry.get(provider_id, provider_config(entry))
    except Exception:
        return None


@dataclass
class _RegisteredPresence:
    """Answer to "which REGISTERED models are actually on disk?"."""

    # model.id -> size in bytes, or None when present but unsized. A missing
    # key means "not on disk" — unless the model's provider is in
    # `unqueryable`, in which case it means "couldn't tell".
    on_disk: dict[str, int | None]
    unqueryable: set[str]


def _registered_presence(registry: Registry, models: list[ModelEntry]) -> _RegisteredPresence:
    """Ask each provider whether each of `models` is on disk, and how big.

    Deliberately NOT derived from list_local(): a provider's on-disk spelling
    is not always the registry's. omlx's list_local() reports the model
    directory's basename ("Qwen3.8-27B-4bit") while the matching
    ModelEntry.model_name holds the full HF repo id
    ("mlx-community/Qwen3.8-27B-4bit"), so joining the two on an exact
    (provider_id, model_name) match bucketed every registered-and-present omlx
    model as "not downloaded". is_downloaded()/size_of()/resolve_local() route
    through each provider's OWN path derivation (OMLXProvider._target_dir()
    derives the basename from the variant's repo), so they are provider-correct
    by construction — and omlx's size_of() reports a real size where its
    list_local() always reports None.

    Same strategy as screens/__init__.py's reconcile_model_state(): try the
    batched resolve_local() per provider first (only ollama implements it —
    one `ollama list` for the whole batch), fall back to the per-model calls
    when it is unimplemented, misaligned, or raises.
    """
    by_provider: dict[str, list[ModelEntry]] = defaultdict(list)
    for model in models:
        by_provider[model.provider_id].append(model)

    on_disk: dict[str, int | None] = {}
    unqueryable: set[str] = set()
    for provider_id, entries in by_provider.items():
        provider = _provider_instance(registry, provider_id)
        if provider is None:
            unqueryable.add(provider_id)
            continue
        specs = [model_entry_to_variant(m) for m in entries]
        try:
            resolved = provider.resolve_local(specs)
        except Exception:
            resolved = None
        if isinstance(resolved, list) and len(resolved) == len(specs):
            # Batch contract (mirrors reconcile_model_state): anything that
            # is not a positionally-aligned list degrades to the per-model
            # path below rather than silently dropping models.
            for model, hit in zip(entries, resolved, strict=True):
                if hit is None:
                    continue
                size = hit.get("size_bytes")
                on_disk[model.id] = size if isinstance(size, int) else None
            continue
        for model, spec in zip(entries, specs, strict=True):
            try:
                downloaded = bool(provider.is_downloaded(spec))
            except Exception:
                # A raising is_downloaded() is "unknown", not "absent" —
                # OllamaProvider raises precisely when the daemon is down.
                unqueryable.add(provider_id)
                continue
            if not downloaded:
                continue
            try:
                size = provider.size_of(spec)
            except Exception:
                size = None
            on_disk[model.id] = size if isinstance(size, int) else None
    return _RegisteredPresence(on_disk=on_disk, unqueryable=unqueryable)


def _provider_local_models(
    registry: Registry,
) -> tuple[dict[tuple[str, str], LocalModel], set[str]]:
    """(provider_id, variant_id) -> LocalModel for every artifact every
    in-scope, registered, live provider currently reports on disk, plus the
    set of in-scope provider ids that could not be enumerated.

    This answers only "what is on disk that modelman may not know about?" —
    enumeration is the one question a presence check keyed on already-known
    names cannot answer. Whether a given entry here is already registered is
    decided by _registered_under_name(), never by comparing this map's keys
    to registry ids: the keys are the provider's spelling, not the registry's.

    Tolerant by design: a provider with no registered Provider class or whose
    list_local() raises contributes nothing rather than failing the whole
    call — but it is reported in the second return value so callers can say
    "unknown" instead of "nothing there". Cloud-located providers (location
    not local/legacy-empty — openrouter, native agents) are skipped by
    design, not reported as failures: they have no on-disk artifacts to
    discover. A local provider whose class opts out via
    Provider.supports_discovery (mlx_lm_server's pairing, retired llamacpp)
    is skipped the same way. A local provider id with NO registered class
    (e.g. a hand-edited "omlx-6bit" row) falls through to the unqueryable
    path below, same as any other provider construction failure.
    """
    found: dict[tuple[str, str], LocalModel] = {}
    unqueryable: set[str] = set()
    for entry in registry.providers:
        if not is_local_location(entry.location):
            continue
        provider_cls = ProviderRegistry.get_class(entry.id)
        if provider_cls is not None and not getattr(provider_cls, "supports_discovery", True):
            continue
        provider = _provider_instance(registry, entry.id)
        if provider is None:
            unqueryable.add(entry.id)
            continue
        try:
            local_models = provider.list_local()
        except Exception:
            unqueryable.add(entry.id)
            continue
        for local_model in local_models:
            found[(entry.id, local_model["variant_id"])] = local_model
    return found, unqueryable


def _registered_under_name(registry: Registry, provider_id: str, variant_id: str) -> bool:
    """Whether some registry.toml entry on `provider_id` already names the
    artifact that provider reports on disk as `variant_id`.

    Uses _name_matches rather than an exact model_name comparison for the same
    reason _probe_running does: the provider's spelling and the registry's
    differ (omlx reports a directory basename, the registry holds the full HF
    repo id), and an exact match re-"discovers" every registered omlx model.
    _name_matches is symmetric — each of its three disjuncts has a mirror — so
    the argument order carries no meaning here.
    """
    return any(
        m.provider_id == provider_id and _name_matches(m.model_name, variant_id)
        for m in registry.models
    )


def _find_discovered(registry: Registry, name: str) -> list[DiscoveredModel]:
    """Unregistered on-disk artifacts, across every in-scope provider, whose
    native name matches `name` (leniently, per _name_matches: a user may type
    either the on-disk basename the listing shows or the full repo id)."""
    local_map, _unqueryable = _provider_local_models(registry)
    return [
        DiscoveredModel(
            provider_id=provider_id, variant_id=variant_id,
            path=local_model["path"], size_bytes=local_model.get("size_bytes"),
        )
        for (provider_id, variant_id), local_model in local_map.items()
        if _name_matches(variant_id, name)
        and not _registered_under_name(registry, provider_id, variant_id)
    ]


def _register_discovered_model(
    registry: Registry,
    state: StateStore,
    match: DiscoveredModel,
    family: str,
    registry_path: Path | None,
    state_path: Path | None,
    litellm_path: Path | None,
) -> tuple[ModelEntry, list[str]]:
    """Write a new registry.toml entry for `match`, mark it ready+exposed
    in modelman.toml, and add its LiteLLM model_list row. Mutates `registry`
    and `state` in place (the caller's in-memory copies) so the rest of
    start_local_model's flow sees the new model immediately.
    """
    model_id = f"{match.provider_id}/{match.variant_id.replace('/', '--')}"
    entry = ModelEntry(
        id=model_id,
        family=family,
        provider_id=match.provider_id,
        model_name=match.variant_id,
        location=LOCATION_LOCAL,
        source="discovered",
        # Providers that derive their on-disk path from fetch.repo/local_path
        # (omlx) rather than model_name (ollama, mtplx) can only resolve this
        # artifact again if repo is populated — list_local()'s variant_id IS
        # the repo-basename-shaped identifier omlx's own repo_basename()
        # would produce, so it round-trips through _target_dir() correctly.
        # A no-op for name-keyed providers, which never read fetch.repo.
        fetch=Fetch(repo=match.variant_id),
    )
    try:
        with locked_registry(registry_path) as fresh:
            # Defense-in-depth against a concurrent registration or a hand-edited
            # registry.toml: the id is derived from (provider, native name), so a
            # collision means this artifact is already registered and appending
            # would duplicate it in registry.toml AND in LiteLLM's model_list.
            # Raising here (inside the lock, before the write) leaves the file
            # untouched — locked_registry only saves on a clean exit.
            if any(m.id == model_id for m in fresh.models):
                raise LocalControlError(f"{model_id} is already registered")
            fresh.models.append(entry)
        registry.models.append(entry)

        model_state = ModelState(ready=True, disk_path=match.path, size_bytes=match.size_bytes)
        with locked_state(state_path) as fresh_state:
            fresh_state.set(model_id, model_state)
        state.set(model_id, model_state)
    except OSError as exc:
        raise LocalControlError(f"failed to register {model_id}: {exc}") from exc

    try:
        warnings = expose_model(registry, state, model_id, litellm_path or default_litellm_config_path())
    except ExposeError as exc:
        # The registry entry and its ready=true state are already persisted at
        # this point (both are prerequisites for exposing), so this leaves a
        # registered-but-unexposed model rather than nothing — say so, and say
        # that a retry reuses it: the entry now resolves by its native name
        # (_resolve_or_register's second step), so a re-run exposes and starts
        # it instead of registering a duplicate.
        raise LocalControlError(
            f"{model_id} was registered (registry.toml + modelman.toml, ready=true) "
            f"but could not be exposed to LiteLLM: {exc} — fix the LiteLLM config and "
            f"re-run `modelman start {match.variant_id}`; it will reuse that entry "
            "rather than register it again"
        ) from exc
    # expose_model() mutates state.models[model_id] in place (sets exposed)
    # but never persists it - merge just this one key back, mirroring
    # main.py's `expose` command.
    try:
        with locked_state(state_path) as fresh_state:
            fresh_state.models[model_id] = state.models[model_id]
    except OSError as exc:
        raise LocalControlError(
            f"{model_id} was registered and exposed but the final state merge "
            f"failed: {exc} — re-run `modelman start {match.variant_id}`; it will "
            "reuse that entry rather than register it again"
        ) from exc
    return entry, warnings


def _resolve_or_register(
    registry: Registry,
    state: StateStore,
    model_id: str,
    family: str | None,
    registry_path: Path | None,
    state_path: Path | None,
    litellm_path: Path | None,
) -> tuple[ModelEntry, list[str]]:
    """Resolve `model_id` to a ModelEntry, trying — in order — a registry
    id, an existing model's native provider-side name (so a discovered
    model that was auto-registered under `<provider>/<name>` still
    resolves when the user re-types its bare native name), and finally an
    on-disk-but-unregistered artifact (which requires `family` and
    registers it). Raises LocalControlError('unknown model: ...') if none
    match, or DiscoveredModelNeedsFamily if the third case needs a family.
    """
    try:
        return registry.model(model_id), []
    except KeyError:
        pass

    # _name_matches, not ==: the name a user types comes from the provider's
    # spelling (the discovered listing prints omlx's directory basename,
    # "Qwen3.8-27B-4bit") while model_name holds the registry's (the full repo
    # id, "mlx-community/Qwen3.8-27B-4bit"). An exact comparison missed that
    # model and fell through to the register-a-discovered-model step below,
    # duplicating an already-registered, already-exposed artifact.
    native_matches = [
        m
        for m in registry.models
        if _name_matches(m.model_name, model_id)
        and model_has_local_artifact(m, _provider_entry(registry, m.provider_id))
    ]
    if len(native_matches) == 1:
        return native_matches[0], []
    if len(native_matches) > 1:
        ids = ", ".join(sorted(m.id for m in native_matches))
        raise LocalControlError(f"{model_id!r} matches multiple registered models ({ids}) — use the full model id")

    discovered = _find_discovered(registry, model_id)
    if not discovered:
        raise LocalControlError(f"unknown model: {model_id}")
    if len(discovered) > 1:
        providers = ", ".join(sorted(m.provider_id for m in discovered))
        raise LocalControlError(
            f"{model_id!r} matches on-disk models from multiple providers ({providers}) — "
            "register one manually to disambiguate"
        )
    if family is None:
        match = discovered[0]
        raise DiscoveredModelNeedsFamily(match.provider_id, match.variant_id, known_families(registry, state))
    return _register_discovered_model(
        registry, state, discovered[0], family, registry_path, state_path, litellm_path
    )


def inventory_local_models(registry: Registry, state: StateStore) -> LocalModelInventory:
    """Live, three-way view of local models: registered models split into
    downloaded/not-downloaded by asking each provider whether its artifact is
    on disk, plus on-disk artifacts with no registry.toml entry at all
    ("discovered").

    Those are two different questions and each gets the tool that answers it
    correctly. "Is this registered model present?" goes through
    _registered_presence() (the providers' own presence checks, immune to the
    spelling mismatch between list_local() and model_name); "what is on disk
    that isn't registered?" goes through _provider_local_models() (list_local()
    is the only enumeration there is), with _registered_under_name() deciding
    what counts as already-known.

    `modelman start` (no model_id) prints this. Unlike the exposed-only
    listing this replaces, `downloaded`/`not_downloaded` include every
    local-artifact registered model regardless of its exposed flag — this
    is a full local inventory, not just "what can I start right now".
    """
    local_map, discovery_unqueryable = _provider_local_models(registry)
    running_marker = state.local.running_model

    local_models = [
        model
        for model in registry.models
        if model_has_local_artifact(model, _provider_entry(registry, model.provider_id))
    ]
    presence = _registered_presence(registry, local_models)

    downloaded: list[InventoryEntry] = []
    not_downloaded: list[str] = []
    for model in sorted(local_models, key=lambda m: m.id):
        if model.id not in presence.on_disk:
            not_downloaded.append(model.id)
            continue
        running = False
        if model.id == running_marker:
            provider = _provider_entry(registry, model.provider_id)
            probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
            running = _probe_running(model.provider_id, model.model_name, probe_origin)
        # A provider that can't size an artifact it confirms is present (no
        # size_of() implementation) must not regress the size modelman.toml
        # already cached from an earlier reconcile into a "—".
        size = presence.on_disk[model.id]
        if size is None:
            size = state.get(model.id).size_bytes
        downloaded.append(InventoryEntry(model_id=model.id, running=running, size_bytes=size))

    discovered = [
        DiscoveredModel(
            provider_id=provider_id,
            variant_id=variant_id,
            path=local_model["path"],
            size_bytes=local_model.get("size_bytes"),
        )
        for (provider_id, variant_id), local_model in sorted(local_map.items())
        if not _registered_under_name(registry, provider_id, variant_id)
    ]
    return LocalModelInventory(
        downloaded=downloaded,
        not_downloaded=not_downloaded,
        discovered=discovered,
        unqueryable_providers=sorted(presence.unqueryable | discovery_unqueryable),
    )


def start_local_model(
    registry: Registry,
    model_id: str,
    state_path: Path | None = None,
    *,
    family: str | None = None,
    registry_path: Path | None = None,
    litellm_path: Path | None = None,
) -> StartResult:
    """Stop whatever local model is running (if any) and start model_id,
    recording it as the new `[local].running_model` marker.

    `model_id` may be a registry id, an existing model's native
    provider-side name, or (with `family` set) the native name of an
    on-disk artifact with no registry.toml entry yet — see
    _resolve_or_register. Raises DiscoveredModelNeedsFamily when the third
    case needs a family the caller hasn't supplied yet.

    Raises LocalControlError when model_id is unknown, not a local model,
    its provider cannot be isolated by bin/llm-isolate-provider, or the
    mlx_lm_server pairing can't be resolved.

    Idempotent: when model_id is already the running marker, the marked
    model is PROBED before trusting the marker — `modelman start` is the
    recovery command wt's own "not running" message prescribes, so it must
    not no-op on a marker whose process died (crash, reboot, omlx stop). A
    dead marker is cleared and the full start runs. A false probe negative
    only costs a stop+reload of an already-serving model.
    """
    state = load_state(state_path)
    model, registration_warnings = _resolve_or_register(
        registry, state, model_id, family, registry_path, state_path, litellm_path
    )
    resolved_id = model.id

    provider = _provider_entry(registry, model.provider_id)
    if not model_has_local_artifact(model, provider):
        raise LocalControlError(f"{resolved_id} is a cloud model — modelman start only runs local models")

    if model.provider_id not in SUPPORTED_PROVIDER_IDS:
        raise LocalControlError(
            f"provider {model.provider_id!r} cannot be started/stopped by modelman "
            f"(supported: {sorted(SUPPORTED_PROVIDER_IDS)})"
        )

    # Resolve the isolate arguments BEFORE any teardown so a broken mlx_lm_server
    # pairing fails fast — stopping the running model first would tear down a
    # healthy model and then leave the GPU empty when this raises.
    extra_args: tuple[str, ...] = ()
    env: dict[str, str] | None = None
    if model.provider_id == "mlx_lm_server":
        try:
            target, draft = mlx_lm_server_pairing_args(
                model.id,
                model.fetch.local_path if model.fetch else None,
                model.fetch.repo if model.fetch else None,
                model.draft.local_path if model.draft else None,
                model.draft.repo if model.draft else None,
            )
        except BenchmarkError as exc:
            raise LocalControlError(str(exc)) from exc
        extra_args = (target, draft)
    elif model.provider_id == "mtplx":
        # MTPLX is single-model-per-process and has no baked-in default in the
        # bash helper. Pass the requested repo id as a positional arg so the
        # lifecycle module starts exactly this model; no env override is used.
        extra_args = (model.model_name,)
        env = None
    else:
        env = {_ENV_VAR_BY_PROVIDER[model.provider_id]: model.model_name}

    probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
    current = load_state(state_path).local.running_model
    if current == resolved_id:
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            return StartResult(model_id=resolved_id, already_running=True, warnings=registration_warnings)
        # Marker names this model but nothing is serving it — clear the dead
        # marker before the restart so a mid-flight wt launch doesn't gate
        # on a model that just failed its probe.
        _clear_stale_marker((resolved_id,), state_path)

    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        # Teardown failed: the previously marked model (if any) may still be
        # serving, so its marker is still true — leave it untouched.
        raise LocalControlError(f"failed to stop the currently-running local model: {exc}") from exc

    try:
        result = isolate_provider(model.provider_id, *extra_args, env=env)
    except BenchmarkError as exc:
        _clear_stale_marker((current or "", resolved_id), state_path)
        raise LocalControlError(
            f"failed to start {resolved_id}: {exc} — cleared the stale "
            "[local].running_model marker (the previously running model was "
            "already stopped)"
        ) from exc
    if not result.ok:
        _clear_stale_marker((current or "", resolved_id), state_path)
        raise LocalControlError(
            f"failed to start {resolved_id}: {result.error or 'unknown error'} — "
            "cleared the stale [local].running_model marker (the previously "
            "running model was already stopped)"
        )

    try:
        with locked_state(state_path) as fresh:
            fresh.local.running_model = resolved_id
    except OSError as exc:
        raise LocalControlError(
            f"{resolved_id} started successfully but the [local].running_model "
            f"marker could not be persisted: {exc} — wt's picker will not see it "
            "as running until `modelman start` succeeds"
        ) from exc
    return StartResult(
        model_id=resolved_id,
        already_running=False,
        direct_url=result.direct_url or None,
        warnings=registration_warnings,
    )


def stop_local_model(state_path: Path | None = None) -> StopResult:
    """Stop the currently-running local model (if any) and clear the
    marker. No-op when nothing is running."""
    running = load_state(state_path).local.running_model
    if not running:
        return StopResult(stopped_model_id=None)
    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        # The model may still be serving — its marker stays true.
        raise LocalControlError(f"failed to stop {running}: {exc}") from exc
    with locked_state(state_path) as fresh:
        # Only clear OUR marker: a concurrent `modelman start` that finished
        # between stop-all and this write has already named a new model.
        if fresh.local.running_model == running:
            fresh.local.running_model = None
    return StopResult(stopped_model_id=running)
