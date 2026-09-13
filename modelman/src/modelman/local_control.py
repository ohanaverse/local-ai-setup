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
from .litellm import ExposeError, default_litellm_config_path, expose_model, is_effectively_exposed
from .local_process import ENV_VAR_BY_PROVIDER as _ENV_VAR_BY_PROVIDER
from .local_process import http_models_ids as _http_models_ids
from .providers.mtplx import MTPLX_BASE
from .providers.registry import ProviderRegistry
from .registry import (
    LOCATION_LOCAL,
    ModelEntry,
    Registry,
    base_origin,
    known_families,
    locked_registry,
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

# Providers modelman can both list a live on-disk catalog for and start via
# bin/llm-isolate-provider. mlx_lm_server is a target+draft pairing chosen at
# start time (not a single downloaded artifact) and llamacpp is retired —
# neither maps onto "discover one artifact, register it" (see the design's
# Non-goals).
DISCOVERY_PROVIDER_IDS: frozenset[str] = frozenset({"ollama", "omlx", "omlx-6bit", "mtplx"})


class LocalControlError(Exception):
    """Raised for user-facing `modelman start`/`modelman stop` failures."""


@dataclass
class StartResult:
    model_id: str
    already_running: bool
    direct_url: str | None = None


@dataclass
class StopResult:
    # The marker that was cleared, or None if nothing was running.
    stopped_model_id: str | None


@dataclass
class LocalModelStatus:
    model_id: str
    running: bool


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


def _provider_local_models(registry: Registry) -> dict[tuple[str, str], dict]:
    """(provider_id, variant_id) -> LocalModel for every artifact every
    in-scope, registered, live provider currently reports on disk.

    Tolerant by design: a provider id with no registered Provider class
    (e.g. a hand-edited "omlx-6bit" registry.toml row today — see
    DISCOVERY_PROVIDER_IDS's docstring) or whose list_local() raises
    contributes nothing rather than failing the whole call — this backs
    both the `start` listing and the start-by-native-name fallback, and
    neither should go blind because one provider is unreachable.
    """
    found: dict[tuple[str, str], dict] = {}
    for entry in registry.providers:
        if entry.id not in DISCOVERY_PROVIDER_IDS:
            continue
        if ProviderRegistry.get_class(entry.id) is None:
            continue
        provider = ProviderRegistry.get(entry.id, provider_config(entry))
        try:
            local_models = provider.list_local()
        except Exception:
            local_models = []
        for local_model in local_models:
            found[(entry.id, local_model["variant_id"])] = local_model
    return found


def _registered_pairs(registry: Registry) -> set[tuple[str, str]]:
    return {(m.provider_id, m.model_name) for m in registry.models}


def inventory_local_models(registry: Registry, state: StateStore) -> LocalModelInventory:
    """Live, three-way view of local models: registered models split into
    downloaded/not-downloaded by cross-referencing each in-scope provider's
    list_local() against registry.toml, plus on-disk artifacts with no
    registry.toml entry at all ("discovered").

    `modelman start` (no model_id) prints this. Unlike the exposed-only
    listing this replaces, `downloaded`/`not_downloaded` include every
    local-artifact registered model regardless of its exposed flag — this
    is a full local inventory, not just "what can I start right now".
    """
    local_map = _provider_local_models(registry)
    registered_pairs = _registered_pairs(registry)
    running_marker = state.local.running_model

    local_models = [
        model
        for model in registry.models
        if model_has_local_artifact(
            model, next((p for p in registry.providers if p.id == model.provider_id), None)
        )
    ]

    downloaded: list[InventoryEntry] = []
    not_downloaded: list[str] = []
    for model in sorted(local_models, key=lambda m: m.id):
        hit = local_map.get((model.provider_id, model.model_name))
        if hit is None:
            not_downloaded.append(model.id)
            continue
        running = False
        if model.id == running_marker:
            provider = next((p for p in registry.providers if p.id == model.provider_id), None)
            probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
            running = _probe_running(model.provider_id, model.model_name, probe_origin)
        downloaded.append(
            InventoryEntry(model_id=model.id, running=running, size_bytes=hit.get("size_bytes"))
        )

    discovered = [
        DiscoveredModel(
            provider_id=provider_id,
            variant_id=variant_id,
            path=local_model["path"],
            size_bytes=local_model.get("size_bytes"),
        )
        for (provider_id, variant_id), local_model in sorted(local_map.items())
        if (provider_id, variant_id) not in registered_pairs
    ]
    return LocalModelInventory(downloaded=downloaded, not_downloaded=not_downloaded, discovered=discovered)


def start_local_model(
    registry: Registry, model_id: str, state_path: Path | None = None
) -> StartResult:
    """Stop whatever local model is running (if any) and start model_id,
    recording it as the new `[local].running_model` marker.

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
    try:
        model = registry.model(model_id)
    except KeyError as exc:
        raise LocalControlError(f"unknown model: {model_id}") from exc

    provider = next((p for p in registry.providers if p.id == model.provider_id), None)
    if not model_has_local_artifact(model, provider):
        raise LocalControlError(f"{model_id} is a cloud model — modelman start only runs local models")

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
    if current == model_id:
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            return StartResult(model_id=model_id, already_running=True)
        # Marker names this model but nothing is serving it — clear the dead
        # marker before the restart so a mid-flight wt launch doesn't gate
        # on a model that just failed its probe.
        _clear_stale_marker((model_id,), state_path)

    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        # Teardown failed: the previously marked model (if any) may still be
        # serving, so its marker is still true — leave it untouched.
        raise LocalControlError(f"failed to stop the currently-running local model: {exc}") from exc

    try:
        result = isolate_provider(model.provider_id, *extra_args, env=env)
    except BenchmarkError as exc:
        _clear_stale_marker((current or "", model_id), state_path)
        raise LocalControlError(
            f"failed to start {model_id}: {exc} — cleared the stale "
            "[local].running_model marker (the previously running model was "
            "already stopped)"
        ) from exc
    if not result.ok:
        _clear_stale_marker((current or "", model_id), state_path)
        raise LocalControlError(
            f"failed to start {model_id}: {result.error or 'unknown error'} — "
            "cleared the stale [local].running_model marker (the previously "
            "running model was already stopped)"
        )

    with locked_state(state_path) as fresh:
        fresh.local.running_model = model_id
    return StartResult(model_id=model_id, already_running=False, direct_url=result.direct_url or None)


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
