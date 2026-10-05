"""modelman-owned local-model lifecycle control (issue #65; same-provider-
only concurrency since 2026-09-14). See
docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md.

`modelman start`/`modelman stop` (main.py) are the only place a local
model's process is started or stopped for normal (non-benchmark) usage.
Both delegate the actual stop/start to modelman.benchmark.isolation —
the same in-process lifecycle contract `modelman benchmark` uses, which
drives modelman.providers.lifecycle's backends — and record which models
are running via a per-model
`running: bool` flag on modelman.toml's ModelState (state.py) — a hint
only modelman's own readers use, and only after a live probe confirms it.
wt reads no per-model state key at all (#179 Phase B): it probes the
providers themselves and discovers what is on disk, so this flag shapes
modelman's own views (the TUI's RUNNING column, `modelman start`'s
inventory, stop paths) alone. Cross-provider concurrency is unrestricted —
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
from .litellm import sync_routes
from .local_process import ENV_VAR_BY_PROVIDER as _ENV_VAR_BY_PROVIDER
from .local_process import http_answers as _http_answers
from .local_process import http_json as _http_json
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

# The omlx family's registry provider rows, in the order wt picks one to
# stand for the family (wt/internal/localmodels/inventory.go's familyIDs /
# familyProviderID): plain `omlx` first, `omlx-6bit` on a registry that
# defines no plain row.
_OMLX_FAMILY = "omlx"
_OMLX_FAMILY_PROVIDER_IDS: tuple[str, ...] = ("omlx", "omlx-6bit")

# Subprocess seam so tests can keep the probe hermetic (conftest patches
# it); production calls subprocess.run directly.
_default_runner = subprocess.run


class LocalControlError(Exception):
    """Raised for user-facing `modelman start`/`modelman stop` failures."""


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
    # Set only by inventory_local_models(): the artifact is flagged running
    # under its discovered id (it was started without a registry entry) and
    # a live probe confirms it.
    running: bool = False

    @property
    def model_id(self) -> str:
        """The id this artifact runs, is stopped and is routed under while
        it has no registry entry — see _discovered_id."""
        return _discovered_id(self.provider_id, self.variant_id)


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
        result = _default_runner(["ollama", "ps"], capture_output=True, text=True, check=False)
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


def _ollama_name_matches(have: str, want: str) -> bool:
    """Whether ollama's artifact name `have` satisfies a registry
    model_name `want`: exact, or — when `want` carries no ":" tag — `have`
    is `want` with ollama's implicit ":latest" tag. A verbatim mirror of
    wt's OllamaNameMatches (wt/internal/localmodels/match.go), one-directional
    like it, because the two must agree on which overlay owns which pulled
    model (see _artifact_matches)."""
    if have == want:
        return True
    return ":" not in want and have == want + ":latest"


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
    if provider_id in ("omlx", "omlx-6bit"):
        ids = _omlx_loaded(base, ids)
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


def _omlx_loaded(base: str, listed: list[str]) -> list[str]:
    """The ids among `listed` (omlx's /v1/models) that are actually loaded.

    omlx's /v1/models lists every model in its engine pool — the whole model
    directory, minus hidden ones — loaded or not (wt#201), so on its own it
    reads every omlx model as running while the service is up. /health gives
    the pool's counts without a key:

    - nothing loaded -> nothing is running;
    - every pool model loaded, and the list is the whole pool -> the list;
    - anything else (some loaded; or all loaded but a hidden model makes the
      list shorter than the pool) -> [] : only the key-protected
      /v1/models/status says which, and modelman does not ask it. Not-running
      is _probe_running's safe direction. wt's probe (localmodels.ServedIDs)
      does ask, with the registry's secret_ref.

    An omlx whose /health gives no pool counts predates them: `listed` is all
    there is, as before."""
    health = _http_json(f"{base}/health")
    pool = health.get("engine_pool") if health else None
    if not isinstance(pool, dict):
        return listed
    loaded, count = pool.get("loaded_count"), pool.get("model_count")
    if not isinstance(loaded, int) or not isinstance(count, int):
        return listed
    if loaded and loaded == count == len(listed):
        return listed
    return []


def _ollama_origin(provider: ProviderEntry | None) -> str:
    """The origin the ollama provider row points at — the one wt probes."""
    origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
    return origin or _DEFAULT_BASE_ORIGIN["ollama"]


def _require_ollama_daemon(provider: ProviderEntry | None) -> None:
    """Raise unless the ollama daemon answers at the origin wt would probe.

    Ollama's start is deliberately flag-only (see _probe_running) — but the
    one `wt litellm sync` that every start runs makes routing follow LIVE
    provider state, and a refused ollama probe is the single case wt prunes
    rather than skips (Snapshot.Down): it reads "nothing is pulled" and drops
    every configured ollama model's route, the one just reported as started
    included. Refusing here keeps `modelman start` from claiming a success it
    cannot deliver and from that destructive sync — the same posture as wt's
    own lifecycle, which never kickstarts a dead ollama.

    Asks `/api/tags` — the endpoint wt's own ollama probe reads — so the two
    always describe the same server.
    """
    base = _ollama_origin(provider)
    if not _http_answers(f"{base}/api/tags"):
        raise LocalControlError(
            f"the ollama daemon is not answering at {base} — start it "
            "(the Ollama app, or `ollama serve`) and retry"
        )


def _require_ollama_pulled(provider: ProviderEntry | None, model: ModelEntry) -> None:
    """Raise unless ollama has `model` pulled — or cannot say.

    Ollama's start is flag-only (see _probe_running), so nothing else notices
    a model that was never pulled: the start "succeeded", the flag read as
    running, and wt — which routes an ollama model only while it is pulled —
    wrote no route. The refusal needs a positive "not pulled": a tags read
    that gives no usable listing is unknown, and the start goes ahead as
    before.

    Reads `/api/tags` at the provider row's origin, as _require_ollama_daemon
    and wt's probe do, and parses it as wt does (ollamaModelNames: an entry
    with a remote_host is an ollama.com cloud model, not a pulled one;
    OllamaNameMatches for the implicit ":latest"). The local `ollama list`
    CLI is not the authority: with base_url on another host or port it
    describes a different server, and refused a model wt would have routed.
    """
    body = _http_json(f"{_ollama_origin(provider)}/api/tags")
    listed = body.get("models") if body else None
    if not isinstance(listed, list):
        return
    for item in listed:
        if not isinstance(item, dict) or item.get("remote_host"):
            continue
        name = item.get("name")
        if isinstance(name, str) and _ollama_name_matches(name, model.model_name):
            return
    raise LocalControlError(f"{model.id} is not pulled — ollama pull {model.model_name}")


def _clear_running_flag(fresh: StateStore, model_id: str) -> None:
    """Clear `model_id`'s running flag in `fresh`, dropping the row when that
    leaves it saying nothing. A discovered model has no registry entry, so
    its row exists only to carry the flag; left behind, an all-default row
    stayed in modelman.toml for good."""
    existing = fresh.models.get(model_id)
    if existing is None or not existing.running:
        return
    cleared = replace(existing, running=False)
    if cleared == ModelState():
        del fresh.models[model_id]
    else:
        fresh.models[model_id] = cleared


def _clear_stale_running_flag(model_id: str, state_path: Path | None) -> None:
    """Best-effort: clear ONE model's running flag when it's still True —
    used when a probe finds it not actually serving (stale flag), or after
    a failed start/stop for that specific model. Never touches any other
    model's flag, and never touches LiteLLM: routes follow live provider
    state through `wt litellm sync` (#179), which each caller runs itself —
    the start/stop paths at every exit, and the TUI's mount reconcile
    (screens/models.py::_run_reconcile) once per run that cleared a flag.
    Doing it here would double-sync inside those callers.
    """
    try:
        state = load_state(state_path)
    except OSError:
        return
    existing = state.models.get(model_id)
    if existing is None or not existing.running:
        return
    try:
        with locked_state(state_path) as fresh:
            _clear_running_flag(fresh, model_id)
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
    slot_providers = _OMLX_PROVIDER_IDS if provider_id in _OMLX_PROVIDER_IDS else {provider_id}
    domain = {m.id for m in registry.models if m.provider_id in slot_providers}
    # A model started without a registry entry (#179 Phase B) is flagged
    # under its discovered id `<family>/<name>` (see _discovered_id; the
    # omlx family's prefix is in _OMLX_PROVIDER_IDS): it occupies the slot too.
    registered = {m.id for m in registry.models}
    domain |= {
        mid
        for mid in state.models
        if mid not in registered and mid.partition("/")[0] in slot_providers
    }
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
    registry.toml row, no class for that id or the server it is an alias of
    (ProviderRegistry resolves `omlx-6bit` to omlx's), or construction raised).

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


def _artifact_matches(provider_id: str, artifact: str, model_name: str) -> bool:
    """Whether a registry model_name on `provider_id`'s family names the
    on-disk `artifact` — wt's source.matchArtifact
    (wt/internal/localmodels/inventory.go), rule for rule: ollama is exact
    plus the implicit ":latest" tag (_ollama_name_matches); omlx and mtplx
    use _name_matches, because the provider's spelling and the registry's
    differ (omlx reports a directory basename, the registry holds the full HF
    repo id) and an exact match re-"discovers" every registered omlx model.

    The rules must be wt's: wt routes a matched artifact under its overlay's
    registry id and an unmatched one under its discovered id, so a model
    modelman calls discovered while wt calls it registered (or the reverse)
    is flagged under an id wt never routes.
    """
    if provider_id == "ollama":
        return _ollama_name_matches(artifact, model_name)
    return _name_matches(artifact, model_name)


def _registered_under_name(registry: Registry, provider_id: str, variant_id: str) -> bool:
    """Whether some registry.toml entry already names the artifact that
    `provider_id` reports on disk as `variant_id`.

    Matched across the provider's whole FAMILY, as wt does (it keys on
    familyOf(m.ProviderID)): omlx and omlx-6bit are one server over one set
    of artifacts, so an overlay on either row owns the artifact whichever
    row listed it. The name rule is _artifact_matches.
    """
    family = _discovered_family(provider_id)
    return any(
        _discovered_family(m.provider_id) == family
        and _artifact_matches(m.provider_id, variant_id, m.model_name)
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

    One entry per discovered id: two provider rows of one family (omlx,
    omlx-6bit) listing the same artifact are the same model, kept under the
    row that sorts first.
    """
    found: dict[str, DiscoveredModel] = {}
    for (provider_id, variant_id), local_model in sorted(local_map.items()):
        if _registered_under_name(registry, provider_id, variant_id):
            continue
        match = DiscoveredModel(
            provider_id=provider_id,
            variant_id=variant_id,
            path=local_model["path"],
            size_bytes=local_model.get("size_bytes"),
        )
        found.setdefault(match.model_id, match)
    return list(found.values())


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
    return _listable(registry, _discovered_models(registry, local_map))


def _listable(registry: Registry, discovered: list[DiscoveredModel]) -> list[DiscoveredModel]:
    """`discovered` minus the artifacts whose discovered id is already the id
    of a registry model that names a different artifact. Such an artifact can
    be neither started (_resolve_local_model refuses, naming the clash) nor
    routed (wt drops the colliding entry), and registering it from the TUI
    would write a duplicate id — so no listing offers it. _find_discovered
    keeps it, which is how that refusal still gets to explain itself."""
    taken = {m.id for m in registry.models}
    return [d for d in discovered if d.model_id not in taken]


def _find_discovered(registry: Registry, name: str) -> list[DiscoveredModel]:
    """Unregistered on-disk artifacts, across every in-scope provider, whose
    native name matches `name` (leniently, per _name_matches: a user may type
    either the on-disk basename the listing shows or the full repo id)."""
    local_map, _unqueryable = _provider_local_models(registry)
    return [d for d in _discovered_models(registry, local_map) if _name_matches(d.variant_id, name)]


def _discovered_family(provider_id: str) -> str:
    """The provider family a discovered id is prefixed with — wt's
    localmodels.Family(): omlx and omlx-6bit are one physical server and
    share the family "omlx"; every other local provider is its own family.
    """
    return _OMLX_FAMILY if provider_id in _OMLX_PROVIDER_IDS else provider_id


def _discovered_id(provider_id: str, variant_id: str) -> str:
    """The id of an on-disk artifact with no registry.toml entry:
    `<family>/<artifact name as the provider lists it>`.

    Must equal wt's id for the same artifact —
    config.DiscoveredModelID(localmodels.Family(providerID), artifact) in
    wt/internal/litellm/service.go's DiscoveredModel — because the running
    flag, `modelman stop <id>` and the LiteLLM route wt writes all key on
    it. So the prefix is the FAMILY, not the registry provider row: an
    artifact found through an `omlx-6bit` row is `omlx/<name>`, never
    `omlx-6bit/<name>`. The artifact spellings already agree: ollama's tag,
    omlx's directory basename, and for mtplx the repo id `org/name` both
    sides derive from the `org--name` directory (MTPLXProvider._repo_id,
    wt's mtplxRepoID) — the "/" is kept, unlike a registered id.
    """
    return f"{_discovered_family(provider_id)}/{variant_id}"


def _discovered_entry(match: DiscoveredModel) -> ModelEntry:
    """An in-memory ModelEntry for an on-disk artifact with no registry.toml
    overlay, so start_local_model can drive its provider. It is never
    written to registry.toml (#179 Phase B: local models are discovered, not
    configured): its id is `<family>/<native name>` (_discovered_id) — the
    id wt's catalog and LiteLLM route give the running model — and the
    start's one `wt litellm sync` routes it under that id. provider_id stays
    the registry row the artifact was found through (e.g., "omlx-6bit") —
    this INTENTIONAL mismatch with the id's family prefix ("omlx/") picks
    the correct lifecycle backend and env var. `fetch.repo` lets omlx, which
    derives its on-disk path from fetch rather than model_name, find the
    artifact; name-keyed providers (ollama, mtplx) never read it.
    """
    return ModelEntry(
        id=match.model_id,
        family="",
        provider_id=match.provider_id,
        model_name=match.variant_id,
        location=LOCATION_LOCAL,
        source="discovered",
        fetch=Fetch(repo=match.variant_id),
    )


def _names_a_local_provider(registry: Registry, prefix: str) -> bool:
    """Whether `prefix` (the text before a typed id's first "/") is a local
    provider family or a local provider row id. A row with a legacy empty
    location is local (is_local_location), as everywhere else."""
    return (
        prefix == _OMLX_FAMILY
        or prefix in SUPPORTED_PROVIDER_IDS
        or any(p.id == prefix and is_local_location(p.location) for p in registry.providers)
    )


def _names_a_provider(registry: Registry, prefix: str) -> bool:
    """Whether `prefix` (the text before a typed id's first "/") is a
    provider family or a provider row id rather than part of a model name
    (an mtplx/omlx repo id's org segment)."""
    return _names_a_local_provider(registry, prefix) or any(
        p.id == prefix for p in registry.providers
    )


def _typed_name_matches(model: ModelEntry, typed: str) -> bool:
    """Whether what the user typed names registered `model` by its native
    provider-side name.

    omlx and mtplx: _name_matches, not ==: the name a user types comes from
    the provider's spelling (the discovered listing prints omlx's directory
    basename, "Qwen3.8-27B-4bit") while model_name holds the registry's (the
    full repo id, "mlx-community/Qwen3.8-27B-4bit"). An exact comparison
    missed that model and fell through to the discovered-artifact step,
    starting an already-registered artifact under a second id.

    ollama: the name is exact, as in _artifact_matches — a user namespace is
    part of it, so a typed "someuser/qwen3:8b" is NOT a registered "qwen3:8b"
    (a tail match started the registered model in place of the pulled one the
    user named, and "qwen3:8b" started a registered "someuser/qwen3:8b").
    Accepted: the name itself, with or without the `ollama/` family prefix,
    and the implicit ":latest" tag (_ollama_name_matches) — an overlay named
    "llama3.2" owns the pulled "llama3.2:latest", which the listing would
    otherwise have printed as `ollama/llama3.2:latest`, so both that spelling
    and the bare one resolve to the overlay.
    """
    if model.provider_id != "ollama":
        return _name_matches(model.model_name, typed)
    return any(
        _ollama_name_matches(name, model.model_name)
        for name in (typed, typed.removeprefix("ollama/"))
    )


def _resolve_local_model(registry: Registry, model_id: str) -> ModelEntry:
    """Resolve `model_id` to a ModelEntry, trying — in order — a registry
    id, an existing model's native provider-side name, and finally an
    on-disk artifact with no registry entry (an unsaved _discovered_entry;
    nothing is registered). Raises LocalControlError('unknown model: ...')
    if none match.
    """
    try:
        return registry.model(model_id)
    except KeyError:
        pass

    # An exact discovered id is an id, not a name: it is what the listing
    # printed for that artifact, so it wins before the lenient name match
    # below — which for omlx and mtplx compares name tails, and would hand
    # `mtplx/Org/Name` to a registered omlx `Name`. Only a typed
    # "<local family>/<name>" can be one, so a bare name or a repo id
    # ("mlx-community/Name") costs no provider listing here.
    discovered: list[DiscoveredModel] | None = None
    exact: list[DiscoveredModel] = []
    prefix, slash, _ = model_id.partition("/")
    if slash and _names_a_local_provider(registry, prefix):
        discovered = _find_discovered(registry, model_id)
        exact = [d for d in discovered if d.model_id == model_id]

    providers_by_id = _provider_by_id(registry)
    native_matches = (
        []
        if exact
        else [
            m
            for m in registry.models
            if _typed_name_matches(m, model_id)
            and model_has_local_artifact(m, providers_by_id.get(m.provider_id))
        ]
    )
    if len(native_matches) == 1:
        return native_matches[0]
    if len(native_matches) > 1:
        ids = ", ".join(sorted(m.id for m in native_matches))
        raise LocalControlError(
            f"{model_id!r} matches multiple registered models ({ids}) — use the full model id"
        )

    # _find_discovered matches leniently on the name's tail, so
    # `ollama/shared` also matches mtplx's `shared`, and `omlx/foo` matches
    # an artifact `foo` only ollama has. The exact discovered id therefore
    # wins outright, whatever else matched.
    if discovered is None:
        discovered = _find_discovered(registry, model_id)
    if exact:
        match = exact[0]
    else:
        ids = ", ".join(sorted(d.model_id for d in discovered))
        # An artifact named exactly what was typed beats the tail matches:
        # `qwen3:8b` is the pulled `qwen3:8b`, not also `someuser/qwen3:8b`.
        # That holds for a typed name with a "/" too. A repo id or user
        # namespace can begin with a CLOUD provider's id ("openai/whisper"
        # while an `openai` provider exists). No local model lives on a cloud
        # provider, so when the typed text is an artifact's whole name it is
        # that name, not a claim about where the model is. A local family
        # prefix stays a claim either way, and a cloud prefix in front of a
        # shorter artifact name ("openrouter/foo" for `foo`) is still not
        # that artifact.
        if not slash or not _names_a_local_provider(registry, prefix):
            named_in_full = [d for d in discovered if d.variant_id == model_id]
            if named_in_full:
                discovered, slash = named_in_full, ""
        if not discovered or (slash and _names_a_provider(registry, prefix)):
            # A typed provider/family prefix says WHERE the model is. With no
            # artifact under exactly that id, starting the same-named one on
            # another provider would be a silent wrong guess (and replace a
            # single-port provider's occupant on the way).
            hint = f" — on disk as: {ids}" if discovered else ""
            raise LocalControlError(f"unknown model: {model_id}{hint}")
        if len(discovered) > 1:
            raise LocalControlError(
                f"{model_id!r} matches on-disk models from multiple providers ({ids}) — "
                "use the full <family>/<name> id"
            )
        match = discovered[0]

    # The discovered id can coincide with the id of a registry model that
    # names a DIFFERENT artifact (registry `omlx/foo` with model_name "bar",
    # on-disk `foo`). Starting it would write the running flag onto that
    # model's row, and wt drops the colliding discovered entry rather than
    # route it — so it cannot run under this id.
    owner = next((m for m in registry.models if m.id == match.model_id), None)
    if owner is not None:
        raise LocalControlError(
            f"cannot start the on-disk model {match.variant_id!r}: its id {match.model_id} is "
            f"already the id of a registered model (model_name {owner.model_name!r}) — "
            "register the on-disk model under its own id first"
        )
    return _discovered_entry(match)


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

    `modelman start` (no model_id) prints this. `downloaded`/`not_downloaded`
    include every local-artifact registered model — this is a full local
    inventory, not just "what can I start right now".
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
            probe_origin = (
                base_origin(provider.auth.base_url) if provider and provider.auth else None
            )
            running = _probe_running(model.provider_id, model.model_name, probe_origin)
        # A provider that can't size an artifact it confirms is present (no
        # size_of() implementation) must not regress the size modelman.toml
        # already cached from an earlier reconcile into a "—".
        size = presence.on_disk[model.id]
        if size is None:
            size = state.get(model.id).size_bytes
        downloaded.append(InventoryEntry(model_id=model.id, running=running, size_bytes=size))

    discovered = _listable(registry, _discovered_models(registry, local_map))
    for found in discovered:
        # A model started without a registry entry is flagged under its
        # discovered id; verify it exactly as a registered row is above.
        if not state.get(found.model_id).running:
            continue
        provider = providers_by_id.get(found.provider_id)
        probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
        found.running = _probe_running(found.provider_id, found.variant_id, probe_origin)
    return LocalModelInventory(
        downloaded=downloaded,
        not_downloaded=not_downloaded,
        discovered=discovered,
        unqueryable_providers=sorted(presence.unqueryable | discovery_unqueryable),
    )


def _flagged_target(
    model_id: str,
    models_by_id: dict[str, ModelEntry],
    providers_by_id: dict[str, ProviderEntry],
) -> tuple[str, str] | None:
    """(provider_id, model_name) to probe for a flagged model id: its
    registry entry's, or — for a model started without one (#179 Phase B,
    see _discovered_entry) — the `<family>/<native name>` its discovered
    id spells, provided the registry has a provider row of that family
    (for the omlx family the first of omlx / omlx-6bit, as wt's
    familyProviderID picks it). None when the id names neither (nothing to
    probe, so the flag is not verified)."""
    model = models_by_id.get(model_id)
    if model is not None:
        return model.provider_id, model.model_name
    family, _, model_name = model_id.partition("/")
    if not model_name:
        return None
    candidates = _OMLX_FAMILY_PROVIDER_IDS if family == _OMLX_FAMILY else (family,)
    for provider_id in candidates:
        if provider_id in providers_by_id:
            return provider_id, model_name
    return None


def running_model_ids(
    registry: Registry,
    state: StateStore,
    state_path: Path | None = None,
) -> list[str]:
    """Every local model flagged running AND confirmed by a live probe —
    the same self-healing rule every other consumer (the TUI indicator,
    wt's picker) applies. A flagged-but-dead entry's running flag is
    opportunistically cleared (see _clear_stale_running_flag). Sorted for
    stable display."""
    providers_by_id = _provider_by_id(registry)
    models_by_id = {m.id: m for m in registry.models}
    verified: list[str] = []
    for model_id, model_state in state.models.items():
        if not model_state.running:
            continue
        target = _flagged_target(model_id, models_by_id, providers_by_id)
        if target is None:
            continue
        provider_id, model_name = target
        provider = providers_by_id.get(provider_id)
        probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
        if _probe_running(provider_id, model_name, probe_origin):
            verified.append(model_id)
            continue
        if (
            model_id not in models_by_id
            and "--" in model_name
            and _probe_running(provider_id, model_name.replace("--", "/"), probe_origin)
        ):
            # A flagged id that no registry entry owns but that is spelled the
            # REGISTERED way (the id's "/" → "--": "mtplx/org--name"): the
            # server reports the repo id ("org/name"), so the spelling above
            # can never name-match it. Before #194 this is where registering a
            # discovered artifact from its `+` row left the running flag, and
            # it stayed on disk through a Discard, which rolls the entry back.
            # The `+` row registers under the discovered id now, so only a
            # modelman.toml written before that still holds such a row —
            # verify it against the repo-id spelling (wt's mtplxRepoID /
            # MTPLXProvider._repo_id) before declaring the flag stale, or the
            # next TUI mount clears a serving model's flag.
            verified.append(model_id)
            continue
        _clear_stale_running_flag(model_id, state_path)
    return sorted(verified)


def _with_warnings(message: str, warnings: list[str]) -> str:
    """`message` plus any sync warnings, for a path that raises instead of
    returning a result with a `warnings` list."""
    if not warnings:
        return message
    return f"{message} (also: {'; '.join(warnings)})"


def start_local_model(
    registry: Registry,
    model_id: str,
    state_path: Path | None = None,
    *,
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
    provider-side name, or the native name (or `<family>/<name>` id) of
    an on-disk artifact with no registry.toml entry — see
    _resolve_local_model. The last case is started without registering it
    (#179 Phase B): its one `wt litellm sync` routes it under its
    discovered id, and its running flag is kept under that id.

    Raises LocalControlError when model_id is unknown, not a local model,
    its provider cannot be isolated, or an mlx_lm_server pairing can't be
    resolved.

    Idempotent: when model_id is already flagged running, it is PROBED
    before trusting the flag. A dead flag is cleared and a full start
    runs — modelman start is the recovery command wt's own "not running"
    message prescribes, and must not no-op on a flag whose process died.
    """
    model = _resolve_local_model(registry, model_id)
    resolved_id = model.id

    provider = _provider_entry(registry, model.provider_id)
    if not model_has_local_artifact(model, provider):
        raise LocalControlError(
            f"{resolved_id} is a cloud model — modelman start only runs local models"
        )

    if model.provider_id not in SUPPORTED_PROVIDER_IDS:
        raise LocalControlError(
            f"provider {model.provider_id!r} cannot be started/stopped by modelman "
            f"(supported: {sorted(SUPPORTED_PROVIDER_IDS)})"
        )

    # Before the already-running early return below, not just on the fresh-start
    # branch: the idempotent path runs the same sync, so it would drop the same
    # routes on a machine whose daemon is not there.
    if model.provider_id == "ollama":
        _require_ollama_daemon(provider)
        _require_ollama_pulled(provider, model)

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

    # Read as late as possible: a concurrent start/stop may have changed
    # flags while the provider checks above ran.
    fresh_state = load_state(state_path)
    other_running = sorted(
        mid for mid, s in fresh_state.models.items() if mid != resolved_id and s.running
    )

    already = fresh_state.get(resolved_id).running
    if already:
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            # Re-sync even on the idempotent path: `modelman start <id>` is
            # the remediation for a running model whose route drifted.
            sync_warnings = sync_routes(litellm_path=litellm_path)
            return StartResult(
                model_id=resolved_id,
                already_running=True,
                warnings=sync_warnings,
                other_running=other_running,
            )
        _clear_stale_running_flag(resolved_id, state_path)

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
            _clear_stale_running_flag(occupant, state_path)

        # A failed isolate may follow an occupant teardown (omlx's
        # stop_provider above, or mtplx/mlx_lm_server's inside isolate), so
        # routes are synced before the failure is reported: the occupant's
        # route must not keep pointing at a dead backend.
        try:
            result = isolate_provider(model.provider_id, *extra_args, env=env, solo=True)
        except BenchmarkError as exc:
            _clear_stale_running_flag(resolved_id, state_path)
            raise LocalControlError(
                _with_warnings(
                    f"failed to start {resolved_id}: {exc}",
                    sync_routes(litellm_path=litellm_path),
                )
            ) from exc
        if not result.ok:
            _clear_stale_running_flag(resolved_id, state_path)
            raise LocalControlError(
                _with_warnings(
                    f"failed to start {resolved_id}: {result.error or 'unknown error'}",
                    sync_routes(litellm_path=litellm_path),
                )
            )
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
            _clear_stale_running_flag(occupant, state_path)

    sync_warnings = sync_routes(litellm_path=litellm_path)
    try:
        with locked_state(state_path) as fresh:
            existing = fresh.models.get(resolved_id, ModelState())
            fresh.models[resolved_id] = replace(existing, running=True)
    except OSError as exc:
        raise LocalControlError(
            _with_warnings(
                f"{resolved_id} started successfully but its running flag could not be "
                f"persisted: {exc} — wt's picker will not see it as running until this "
                "succeeds",
                sync_warnings,
            )
        ) from exc
    return StartResult(
        model_id=resolved_id,
        already_running=False,
        direct_url=direct_url,
        warnings=sync_warnings,
        other_running=other_running,
    )


def stop_local_model(
    model_id: str, state_path: Path | None = None, *, litellm_path: Path | None = None
) -> StopResult:
    """Stop exactly one running local model, clear its running flag, then
    run one `wt litellm sync` so LiteLLM's routes follow the new live state
    (#179). No-op when it isn't running. Raises LocalControlError for an
    unknown provider id embedded in model_id's prefix.

    The sync runs AFTER the stop and the flag clear: wt probes the
    providers itself, so the process must already be gone.
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

    with locked_state(state_path) as fresh:
        _clear_running_flag(fresh, model_id)
    return StopResult(stopped_model_id=model_id, warnings=sync_routes(litellm_path=litellm_path))


def stop_all_local_models(
    state_path: Path | None = None, *, litellm_path: Path | None = None
) -> StopAllResult:
    """Stop every currently-running local model, clear all their running
    flags, then run ONE `wt litellm sync` for the lot (#179). Returns the
    sorted list of model ids that were stopped plus any non-fatal warnings
    (e.g. a failed sync)."""
    state = load_state(state_path)
    running_ids = sorted(mid for mid, s in state.models.items() if s.running)
    if not running_ids:
        return StopAllResult(stopped=[])
    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to stop local models: {exc}") from exc

    with locked_state(state_path) as fresh:
        for mid in running_ids:
            _clear_running_flag(fresh, mid)
    return StopAllResult(stopped=running_ids, warnings=sync_routes(litellm_path=litellm_path))
