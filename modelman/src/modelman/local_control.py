"""modelman-owned local-model lifecycle control (issue #65; same-provider-
only concurrency since 2026-09-14). See
docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md.

`modelman start`/`modelman stop` (main.py) are the only place a local
model's process is started or stopped for normal (non-benchmark) usage.
Both delegate the actual stop/start to modelman.benchmark.isolation —
the same in-process lifecycle contract `modelman benchmark` uses, which
drives modelman.providers.lifecycle's backends — and record which models
are running via a per-model
`running: bool` flag on modelman.toml's ModelState (state.py), which wt's
model picker reads read-only to filter its catalog to running local
models plus cloud models. Cross-provider concurrency is unrestricted —
multiple local models on DIFFERENT providers may run at once. Single-port
providers (omlx, mtplx, mlx_lm_server) can still only ever serve one
model each, so starting a different model on the SAME provider replaces
that provider's occupant first.

State ownership: these functions own each model's `running` flag
read/write lifecycle entirely — they read it with load_state() and write
it with short locked_state() transactions (state.py), while the
stop/isolate/warmup subprocesses run OUTSIDE any lock. `locked_state` is
an atomic read-modify-write of the whole file, and holding it across a
warmup that can block for minutes would (a) leave wt reading stale flags
for that whole window and (b) serialize any future in-process caller
behind a minutes-long hold. The flag writes are the only mutations made
here, so the short transactions stay correct.
"""

from __future__ import annotations

import contextlib
import subprocess
from collections import defaultdict
from dataclasses import dataclass, field, replace
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
    stop_provider,
)
from .litellm import (
    ExposeError,
    LiteLLMConfigError,
    apply_unexpose_queue,
    default_litellm_config_path,
    expose_model,
    unexpose_model,
)
from .local_process import ENV_VAR_BY_PROVIDER as _ENV_VAR_BY_PROVIDER
from .local_process import http_models_ids as _http_models_ids
from .providers.base import LocalModel, Provider, _Runner
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

# omlx and omlx-6bit are two registry provider ids but ONE physical
# process/port (8000) — a single-port occupancy domain. A model flagged
# running under either spelling occupies the same server as one flagged
# under the other, so same-provider-occupant detection (and the
# stop_provider() call that replaces it) must treat them as one group,
# never as two independent providers.
_OMLX_PROVIDER_IDS: frozenset[str] = frozenset({"omlx", "omlx-6bit"})

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
    other_running: list[str] = field(default_factory=list)


@dataclass
class StopResult:
    # The marker that was cleared, or None if nothing was running.
    stopped_model_id: str | None
    warnings: list[str] = field(default_factory=list)


@dataclass
class StopAllResult:
    # Every model id stopped by this call, sorted.
    stopped: list[str]
    warnings: list[str] = field(default_factory=list)


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

    Ollama is EXEMPT from live verification and always reads as running:
    `modelman start` for ollama is deliberately flag-only (no warmup call
    — ollama lazy-loads on first request), so `ollama ps` (which lists
    only currently-LOADED models) would read the freshly-flagged model as
    not-running the moment anything probes it before its first real
    request — permanently self-clearing a flag that was never wrong. There
    is no live "is this specific model loaded" signal that corresponds to
    what the running flag means for ollama (the daemon serves whatever's
    requested, flag or no flag), so the flag is trusted as-is once set,
    with no probe-based self-healing for this one provider.
    """
    if provider_id == "ollama":
        return True
    base = base_origin_url or _DEFAULT_BASE_ORIGIN.get(provider_id)
    if not base:
        return False
    ids = _http_models_ids(f"{base}/v1/models")
    if provider_id == "mlx_lm_server":
        # One target+draft pairing per process (the lifecycle's
        # mlx_lm_server backend is the only thing that starts one): the
        # server loads its model before serving, so a non-empty /v1/models
        # is already model-accurate. An exact-name check would false-fail
        # permanently when the registry's local_path/repo spelling differs
        # from what the server reports, turning every idempotent
        # `modelman start` into an unnecessary reload.
        return bool(ids)
    return any(_name_matches(served, model_name) for served in ids)


def _clear_stale_running_flag(
    model_id: str, state_path: Path | None, litellm_path: Path | None = None
) -> None:
    """Best-effort: clear ONE model's running flag when it's still True —
    used when a probe finds it not actually serving (stale flag), or after
    a failed start/stop for that specific model. Never touches any other
    model's flag.

    Also best-effort un-exposes the model when it was exposed: a model
    modelman no longer believes is running must not stay routable through
    LiteLLM (the same reasoning as stop_local_model's unexpose). Done
    OUTSIDE the locked_state transaction below, mirroring
    _expose_for_start/_unexpose_for_stop's own pattern, so the LiteLLM
    config write and proxy restart never happen while holding the
    process-wide state lock.
    """
    try:
        state = load_state(state_path)
    except OSError:
        return
    existing = state.models.get(model_id)
    if existing is None or not existing.running:
        return
    unexpose_ok = True
    if existing.exposed:
        try:
            unexpose_model(state, model_id, litellm_path or default_litellm_config_path())
        except (LiteLLMConfigError, OSError):
            unexpose_ok = False
    try:
        with locked_state(state_path) as fresh:
            fresh_existing = fresh.models.get(model_id)
            if fresh_existing is not None and fresh_existing.running:
                fresh.models[model_id] = replace(
                    fresh_existing,
                    running=False,
                    exposed=state.models[model_id].exposed if unexpose_ok else fresh_existing.exposed,
                )
    except OSError:
        pass  # the LocalControlError about the failed start is the user's answer


def _same_provider_occupant(
    state: StateStore, provider_model_ids: set[str], exclude_model_id: str
) -> str | None:
    """The id of another model belonging to the same provider
    (provider_model_ids — every registry id on that provider) currently
    flagged running, or None. Single-port providers (omlx, mtplx,
    mlx_lm_server) can only ever serve one model — this finds the one
    that needs replacing before starting a different model on the same
    provider."""
    for model_id in provider_model_ids:
        if model_id != exclude_model_id and state.models.get(model_id, ModelState()).running:
            return model_id
    return None


def same_provider_occupant(
    registry: Registry, state: StateStore, model_id: str, provider_id: str
) -> str | None:
    """The id of another model currently flagged running on the same
    single-port occupancy domain as `model_id`, or None.

    The shared answer to "will starting this model silently replace
    something?" — used by start_local_model() to decide whose flag to
    clear, and by the TUI's `s` confirm dialog to warn the user BEFORE
    the replacement happens (design §2/§4). Always None for ollama: the
    daemon is multi-tenant, so many ollama models may run at once and
    nothing is replaced. omlx/omlx-6bit are ONE domain (_OMLX_PROVIDER_IDS
    — they share port 8000), so the occupant may be registered under the
    other spelling.
    """
    if provider_id == "ollama":
        return None
    if provider_id in _OMLX_PROVIDER_IDS:
        domain = {m.id for m in registry.models if m.provider_id in _OMLX_PROVIDER_IDS}
    else:
        domain = {m.id for m in registry.models if m.provider_id == provider_id}
    return _same_provider_occupant(state, domain, exclude_model_id=model_id)


def _stop_ollama_model(model_name: str, runner: _Runner | None = None) -> None:
    """Stop exactly one loaded ollama model (`ollama stop <name>`),
    tolerating any failure (model already unloaded, daemon down) the same
    way the bash helper's stop-all path does.

    `runner` is late-bound (looked up at call time via `runner or
    _default_runner`), matching every other injectable seam in this
    codebase (e.g. this module's own `_ollama_loaded_names`,
    `providers/ollama.py`'s methods) — an early-bound default parameter
    would capture `_default_runner` at import time, so a test's
    `monkeypatch.setattr("modelman.local_control._default_runner", ...)`
    could never reach it.
    """
    with contextlib.suppress(OSError):
        (runner or _default_runner)(
            ["ollama", "stop", model_name], capture_output=True, text=True, check=False
        )


def _provider_entry(registry: Registry, provider_id: str) -> ProviderEntry | None:
    """The registry.toml provider row for `provider_id`, or None."""
    return next((p for p in registry.providers if p.id == provider_id), None)


def _provider_by_id(registry: Registry) -> dict[str, ProviderEntry]:
    """provider_id -> ProviderEntry for every registry.toml provider row.

    Built once and reused by callers that would otherwise call
    _provider_entry() (an O(n_providers) linear scan) once per model in a
    loop over registry.models — that pattern is O(n_models * n_providers)
    for no reason, since registry.providers never changes mid-call.
    """
    return {p.id: p for p in registry.providers}


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


def _discovered_models(
    registry: Registry, local_map: dict[tuple[str, str], LocalModel]
) -> list[DiscoveredModel]:
    """Every entry in `local_map` (a _provider_local_models() result) with no
    matching registry.toml entry, as DiscoveredModel. Shared by
    _find_discovered() (name-filtered, for resolving a single `start <name>`)
    and inventory_local_models() (unfiltered, for the no-arg listing) so the
    "already registered?" construction/filter logic can't drift between the
    two callers.
    """
    return [
        DiscoveredModel(
            provider_id=provider_id, variant_id=variant_id,
            path=local_model["path"], size_bytes=local_model.get("size_bytes"),
        )
        for (provider_id, variant_id), local_model in sorted(local_map.items())
        if not _registered_under_name(registry, provider_id, variant_id)
    ]


def discover_unregistered_models(
    registry: Registry,
    local_map: dict[tuple[str, str], LocalModel] | None = None,
) -> list[DiscoveredModel]:
    """Every on-disk artifact from an in-scope local provider with no
    matching registry.toml entry — the standalone entry point the TUI's
    models screen uses to surface discovered models. Reuses the same
    enumeration and name-matching logic `modelman start`'s no-arg
    inventory listing already relies on
    (_provider_local_models/_discovered_models), so the TUI never needs
    its own provider-scanning code.

    `local_map`, when passed, is a `_provider_local_models()` result the
    caller already fetched — ModelScreen's reconcile worker computes one
    map per mount and hands it to both this function and
    `reconcile_model_state()`'s path-resolution fallback so each in-scope
    provider's `list_local()` runs once per mount, not twice. Callers with
    no reconcile step of their own (e.g. `modelman start`'s no-arg
    listing) omit it and get a freshly-fetched map, as before.
    """
    if local_map is None:
        local_map, _unqueryable = _provider_local_models(registry)
    return _discovered_models(registry, local_map)


def _find_discovered(registry: Registry, name: str) -> list[DiscoveredModel]:
    """Unregistered on-disk artifacts, across every in-scope provider, whose
    native name matches `name` (leniently, per _name_matches: a user may type
    either the on-disk basename the listing shows or the full repo id)."""
    local_map, _unqueryable = _provider_local_models(registry)
    return [d for d in _discovered_models(registry, local_map) if _name_matches(d.variant_id, name)]


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
    providers_by_id = _provider_by_id(registry)
    native_matches = [
        m
        for m in registry.models
        if _name_matches(m.model_name, model_id)
        and model_has_local_artifact(m, providers_by_id.get(m.provider_id))
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
    providers_by_id = _provider_by_id(registry)

    local_models = [
        model
        for model in registry.models
        if model_has_local_artifact(model, providers_by_id.get(model.provider_id))
    ]
    presence = _registered_presence(registry, local_models)

    downloaded: list[InventoryEntry] = []
    not_downloaded: list[str] = []
    for model in sorted(local_models, key=lambda m: m.id):
        if model.id not in presence.on_disk:
            not_downloaded.append(model.id)
            continue
        running = False
        if state.get(model.id).running:
            provider = providers_by_id.get(model.provider_id)
            probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
            running = _probe_running(model.provider_id, model.model_name, probe_origin)
        # A provider that can't size an artifact it confirms is present (no
        # size_of() implementation) must not regress the size modelman.toml
        # already cached from an earlier reconcile into a "—".
        size = presence.on_disk[model.id]
        if size is None:
            size = state.get(model.id).size_bytes
        downloaded.append(InventoryEntry(model_id=model.id, running=running, size_bytes=size))

    discovered = _discovered_models(registry, local_map)
    return LocalModelInventory(
        downloaded=downloaded,
        not_downloaded=not_downloaded,
        discovered=discovered,
        unqueryable_providers=sorted(presence.unqueryable | discovery_unqueryable),
    )


def running_model_ids(
    registry: Registry,
    state: StateStore,
    state_path: Path | None = None,
    litellm_path: Path | None = None,
) -> list[str]:
    """Every local model flagged running AND confirmed by a live probe —
    the same self-healing rule every other consumer (the TUI indicator,
    wt's picker) applies. A flagged-but-dead entry is opportunistically
    cleared (running AND exposed — see _clear_stale_running_flag). Sorted
    for stable display."""
    providers_by_id = _provider_by_id(registry)
    models_by_id = {m.id: m for m in registry.models}
    verified: list[str] = []
    for model_id, model_state in state.models.items():
        if not model_state.running:
            continue
        model = models_by_id.get(model_id)
        if model is None:
            continue
        provider = providers_by_id.get(model.provider_id)
        probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            verified.append(model_id)
        else:
            _clear_stale_running_flag(model_id, state_path, litellm_path)
    return sorted(verified)


def _expose_for_start(
    registry: Registry,
    state: StateStore,
    model_id: str,
    litellm_path: Path | None,
) -> tuple[bool, list[str]]:
    """Best-effort expose of a model that is (or is about to be) running,
    so LiteLLM's model_list stays in sync for agents whose route to it is
    forced through the proxy — see
    docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md.

    Mutates `state.models[model_id]` in place on success (mirrors
    `_resolve_or_register`'s own expose_model call above) and returns
    (True, warnings). Catches ExposeError/LiteLLMConfigError/OSError (the
    latter covers a plain filesystem failure writing LiteLLM's config,
    e.g. ENOSPC/EACCES/a read-only config dir) and returns (False,
    warnings) instead of raising, so an expose failure degrades
    gracefully rather than blocking an otherwise-successful start —
    since wt's local-model picker no longer depends on `exposed` at all.

    The caller uses the returned bool, not `state.models[model_id].exposed`
    directly, to decide whether to persist exposed=True: `state` is read
    before this call runs (sometimes well before, e.g. across
    isolate_provider()'s real wall time), so re-reading it afterwards
    would still reflect that stale snapshot when this function fails
    before mutating it — silently reverting any exposed/unexpose written
    by a concurrent process in the meantime.
    """
    try:
        return True, expose_model(registry, state, model_id, litellm_path or default_litellm_config_path())
    except (ExposeError, LiteLLMConfigError, OSError) as exc:
        if isinstance(exc, ExposeError) and "not ready" in str(exc):
            return False, [
                f"{model_id} is running but could not be exposed to LiteLLM because it "
                f"is not yet marked ready: {exc} — run `modelman sync` (or wait for the "
                f"next reconcile) so readiness catches up, then re-run `modelman start "
                f"{model_id}` or `modelman expose {model_id}`; re-running `modelman expose "
                f"{model_id}` right now will hit the same readiness check and fail the "
                f"same way. Agents whose route to it is forced through LiteLLM won't "
                f"reach it until this is resolved."
            ]
        return False, [
            f"{model_id} is running but could not be exposed to LiteLLM: {exc} — "
            f"agents whose route to it is forced through LiteLLM won't reach it "
            f"until you run `modelman expose {model_id}`"
        ]


def _unexpose_for_stop(
    state: StateStore,
    model_id: str,
    litellm_path: Path | None,
) -> tuple[bool, list[str]]:
    """Best-effort unexpose of a model that just stopped (or was found not
    actually running), so LiteLLM's model_list stops pointing at a dead
    backend — the counterpart to `_expose_for_start`.

    Mutates `state.models[model_id]` in place on success (mirrors
    `_expose_for_start`'s own expose_model call) and returns (True,
    warnings). Catches LiteLLMConfigError/OSError and returns (False,
    warnings) instead of raising, so a failed unexpose degrades gracefully
    rather than blocking an otherwise-successful stop — the caller still
    clears `running`, and `exposed` is left as-is.
    """
    try:
        return True, unexpose_model(state, model_id, litellm_path or default_litellm_config_path())
    except (LiteLLMConfigError, OSError) as exc:
        return False, [
            f"{model_id} was stopped but could not be un-exposed from LiteLLM: {exc} — "
            f"run `modelman unexpose {model_id}` to finish removing it"
        ]


def start_local_model(
    registry: Registry,
    model_id: str,
    state_path: Path | None = None,
    *,
    family: str | None = None,
    registry_path: Path | None = None,
    litellm_path: Path | None = None,
) -> StartResult:
    """Start model_id as a running local model, alongside any other local
    models already running (cross-provider concurrency is unrestricted —
    see docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md).
    If model_id's OWN provider is already running a DIFFERENT model (a
    single-port provider: omlx, mtplx, mlx_lm_server), that occupant is
    stopped first — those providers can only ever serve one model.
    Ollama never gets a process call: the flag flips, and ollama lazy-
    loads on first request.

    model_id may be a registry id, an existing model's native
    provider-side name, or (with `family` set) the native name of an
    on-disk artifact with no registry.toml entry yet — see
    _resolve_or_register. Raises DiscoveredModelNeedsFamily when the third
    case needs a family the caller hasn't supplied yet.

    Raises LocalControlError when model_id is unknown, not a local model,
    its provider cannot be isolated, or an mlx_lm_server pairing can't be
    resolved.

    Idempotent: when model_id is already flagged running, it is PROBED
    before trusting the flag. A dead flag is cleared and a full start
    runs — modelman start is the recovery command wt's own "not running"
    message prescribes, and must not no-op on a flag whose process died.
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
        extra_args = (model.model_name,)
    elif model.provider_id != "ollama":
        env = {_ENV_VAR_BY_PROVIDER[model.provider_id]: model.model_name}

    probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None

    # Re-read rather than reusing the state loaded above: _resolve_or_register()
    # may have done a real LiteLLM config write that takes non-trivial wall
    # time, long enough for a concurrent start/stop to change flags meanwhile.
    fresh_state = load_state(state_path)
    other_running = sorted(
        mid for mid, s in fresh_state.models.items() if mid != resolved_id and s.running
    )

    already = fresh_state.get(resolved_id).running
    if already:
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            expose_ok, expose_warnings = _expose_for_start(registry, fresh_state, resolved_id, litellm_path)
            if expose_ok:
                try:
                    with locked_state(state_path) as fresh:
                        existing = fresh.models.get(resolved_id, ModelState())
                        if not existing.exposed:
                            fresh.models[resolved_id] = replace(existing, exposed=True)
                except OSError as exc:
                    expose_warnings = expose_warnings + [
                        f"{resolved_id}'s exposed flag could not be persisted: {exc}"
                    ]
            return StartResult(
                model_id=resolved_id, already_running=True,
                warnings=registration_warnings + expose_warnings, other_running=other_running,
            )
        _clear_stale_running_flag(resolved_id, state_path, litellm_path)

    if model.provider_id == "ollama":
        # Flag-only: no process action, ollama lazy-loads on request.
        direct_url = None
    else:
        # The occupant LOOKUP runs for every single-port provider, mtplx
        # included: mtplx's own isolate() stops its predecessor's PROCESS
        # internally, but nothing there touches modelman.toml, so this
        # function still owns clearing the replaced occupant's FLAG. Skipping
        # the lookup left two mtplx models both reading as running (the TUI's
        # RUNNING column, `modelman start`'s other-running warning) until some
        # later probe happened to self-heal it.
        occupant = same_provider_occupant(registry, fresh_state, resolved_id, model.provider_id)
        if occupant is not None and model.provider_id in _OMLX_PROVIDER_IDS:
            # omlx/omlx-6bit: stop_provider() tears down the occupant's
            # process itself, right here — clear its flag as soon as that's
            # confirmed, same as before. A failure leaves the occupant's
            # flag untouched (it raises before reaching the clear).
            try:
                stop_provider(model.provider_id)
            except BenchmarkError as exc:
                raise LocalControlError(
                    f"failed to stop {occupant} before starting {resolved_id}: {exc}"
                ) from exc
            _clear_stale_running_flag(occupant, state_path, litellm_path)

        try:
            result = isolate_provider(model.provider_id, *extra_args, env=env, solo=True)
        except BenchmarkError as exc:
            _clear_stale_running_flag(resolved_id, state_path, litellm_path)
            raise LocalControlError(f"failed to start {resolved_id}: {exc}") from exc
        if not result.ok:
            _clear_stale_running_flag(resolved_id, state_path, litellm_path)
            raise LocalControlError(f"failed to start {resolved_id}: {result.error or 'unknown error'}")
        direct_url = result.direct_url or None

        if occupant is not None and model.provider_id not in _OMLX_PROVIDER_IDS:
            # mtplx and mlx_lm_server tear down their own prior occupant
            # INSIDE the isolate_provider(..., solo=True) call just above
            # (providers/lifecycle's orchestrate.isolate()), not before it — so the
            # occupant's flag can only be cleared here, once that call has
            # actually succeeded. Clearing it earlier (before
            # isolate_provider() even ran) would mark the occupant stopped
            # on the mere promise of a teardown that hadn't been attempted
            # yet: if isolate_provider() then failed, the occupant could
            # still be running while reading as stopped everywhere.
            # mlx_lm_server's own probe only checks "is *anything* serving
            # on port 8001" (not name-checked), so it can never self-heal a
            # stale mlx_lm_server flag once a DIFFERENT pairing takes the
            # port, and mtplx's name-checked probe self-heals only on some
            # later read — too late for the warning/indicator this start
            # emits now.
            _clear_stale_running_flag(occupant, state_path, litellm_path)

    expose_ok, expose_warnings = _expose_for_start(registry, fresh_state, resolved_id, litellm_path)
    try:
        with locked_state(state_path) as fresh:
            existing = fresh.models.get(resolved_id, ModelState())
            fresh.models[resolved_id] = replace(
                existing,
                running=True,
                exposed=True if expose_ok else existing.exposed,
            )
    except OSError as exc:
        raise LocalControlError(
            f"{resolved_id} started successfully but its running flag could not be "
            f"persisted: {exc} — wt's picker will not see it as running until this succeeds"
        ) from exc
    return StartResult(
        model_id=resolved_id, already_running=False, direct_url=direct_url,
        warnings=registration_warnings + expose_warnings, other_running=other_running,
    )


def stop_local_model(
    model_id: str, state_path: Path | None = None, *, litellm_path: Path | None = None
) -> StopResult:
    """Stop exactly one running local model, un-expose it from LiteLLM if
    it was exposed, and clear its flags. No-op when it isn't running.
    Raises LocalControlError for an unknown provider id embedded in
    model_id's prefix.

    A stopped model must not stay routable through LiteLLM — `exposed` is
    no longer sticky across a stop/start cycle (a fresh start always
    re-exposes via `_expose_for_start` anyway, so nothing extra is needed
    on the way back up).
    """
    state = load_state(state_path)
    current = state.get(model_id)
    if not current.running:
        return StopResult(stopped_model_id=None)

    provider_id = model_id.split("/", 1)[0]
    if provider_id == "ollama":
        model_name = model_id.split("/", 1)[1]
        _stop_ollama_model(model_name)
    elif provider_id == "mtplx":
        from .providers.lifecycle import stop as lifecycle_stop

        result = lifecycle_stop("mtplx")
        if not result.ok:
            raise LocalControlError(f"failed to stop {model_id}: {result.error}")
    else:
        try:
            stop_provider(provider_id)
        except BenchmarkError as exc:
            raise LocalControlError(f"failed to stop {model_id}: {exc}") from exc

    unexpose_ok = True
    warnings: list[str] = []
    if current.exposed:
        unexpose_ok, warnings = _unexpose_for_stop(state, model_id, litellm_path)

    with locked_state(state_path) as fresh:
        existing = fresh.models.get(model_id)
        if existing is not None and existing.running:
            fresh.models[model_id] = replace(
                existing,
                running=False,
                exposed=state.models[model_id].exposed if unexpose_ok else existing.exposed,
            )
    return StopResult(stopped_model_id=model_id, warnings=warnings)


def stop_all_local_models(
    state_path: Path | None = None, *, litellm_path: Path | None = None
) -> StopAllResult:
    """Stop every currently-running local model, best-effort un-expose
    every one that was exposed in a single batched LiteLLM write, and
    clear all their flags. Returns the sorted list of model ids that were
    stopped plus any non-fatal warnings (e.g. a failed batch unexpose)."""
    state = load_state(state_path)
    running_ids = sorted(mid for mid, s in state.models.items() if s.running)
    if not running_ids:
        return StopAllResult(stopped=[])
    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to stop local models: {exc}") from exc

    exposed_ids = [mid for mid in running_ids if state.models[mid].exposed]
    unexpose_ok = True
    warnings: list[str] = []
    if exposed_ids:
        try:
            warnings = apply_unexpose_queue(
                state, exposed_ids, litellm_path or default_litellm_config_path()
            )
        except (LiteLLMConfigError, OSError) as exc:
            unexpose_ok = False
            warnings = [
                f"{len(exposed_ids)} stopped model(s) could not be un-exposed from "
                f"LiteLLM ({exc}); they may still be routable through a dead backend "
                f"until you run `modelman unexpose <id>` for each: "
                f"{', '.join(exposed_ids)}"
            ]

    with locked_state(state_path) as fresh:
        for mid in running_ids:
            existing = fresh.models.get(mid)
            if existing is not None and existing.running:
                fresh.models[mid] = replace(
                    existing,
                    running=False,
                    exposed=state.models[mid].exposed if unexpose_ok else existing.exposed,
                )
    return StopAllResult(stopped=running_ids, warnings=warnings)
