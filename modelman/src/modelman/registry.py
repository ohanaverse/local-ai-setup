"""registry.toml — the canonical, shared model/provider/family registry.

Owned exclusively by modelman (see docs/superpowers/specs/2026-08-27-
shared-model-registry-design.md). wt reads this file
read-only; it never writes it. Families are first-class [[families]]
entries here (see docs/superpowers/specs/2026-08-29-modelman-first-class-
families-design.md); their display names live in the entry, not in
modelman.toml.
"""

from __future__ import annotations

import contextlib
import math
import os
import threading
import tomllib
from collections.abc import Generator
from dataclasses import dataclass, field, replace
from pathlib import Path
from typing import TYPE_CHECKING, Any

from ._toml_io import atomic_write_toml, drop_none, unknown_keys
from .providers._paths import keys_overlap, overlap_keys
from .providers.mtplx import MTPLX_V1_BASE
from .providers.registry import ProviderRegistry
from .time_pricing import TimePrice, parse_time_prices, time_price_to_dict

if TYPE_CHECKING:
    from .providers.base import VariantSpec
    from .state import StateStore


# Valid subscription period values used for cost validation and serialization.
SUBSCRIPTION_PERIODS = ("month", "year")


def _validate_cost(cost: Cost, *, source: str = "Cost") -> None:
    """Validate that all present prices are non-negative finite numbers and
    that subscription_period is valid when subscription_price is set."""
    for name in (
        "input_price_per_million",
        "cache_price_per_million",
        "output_price_per_million",
        "subscription_price",
    ):
        value = getattr(cost, name)
        if value is None:
            continue
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise ValueError(f"{source} `{name}` must be a number")
        if not math.isfinite(value):
            raise ValueError(f"{source} `{name}` must be finite")
        if value < 0:
            raise ValueError(f"{source} `{name}` must be non-negative")
    if cost.subscription_price is not None and cost.subscription_period not in SUBSCRIPTION_PERIODS:
        raise ValueError(
            f"{source} subscription_period must be one of {SUBSCRIPTION_PERIODS}, got {cost.subscription_period!r}"
        )
    for tp in cost.time_prices:
        if not isinstance(tp, TimePrice):
            raise ValueError(f"{source} `time_prices` entries must be TimePrice")


# Flat cost field names. Used for serialization, dict reconstruction, and
# unknown-key whitelisting.
_COST_FIELDS = {
    "input_price_per_million",
    "cache_price_per_million",
    "output_price_per_million",
    "subscription_price",
    "subscription_period",
}

# Legacy cost field names. These are migrated to _COST_FIELDS on load and
# must never leak into `extra` so they cannot survive a save.
_LEGACY_COST_FIELDS = {"kind", "price_per_million_tokens", "price_per_period", "period"}

# Keys handled explicitly (not flat scalars) that must never land in
# Cost.extra.
_COST_STRUCTURED_FIELDS = {"time_prices"}

# Canonical location values used across the TUI and providers.
LOCATION_LOCAL = "local"
LOCATION_CLOUD = "cloud"


class RegistryError(Exception):
    """Raised when registry.toml is missing or malformed."""


class RegistryNotFoundError(RegistryError):
    """Raised when there is no registry.toml at all — as opposed to one that
    cannot be read, which a caller must never treat as empty and overwrite."""


class RegistryPathError(RegistryError):
    """Raised when the registry path is a symlink to a file that is not there.

    Distinct from RegistryNotFoundError, which it looks like at the filesystem
    level (`Path.exists()` is False for both): a link is a user's pointer at
    where their registry lives — a dotfiles checkout, a file on a volume that
    is not mounted right now — so a caller must neither read it as empty nor
    write over it, which would shadow the real registry when the target comes
    back (#248). Carries the two names to report: the link and what it points
    at.
    """

    def __init__(self, link: Path, target: str) -> None:
        self.link = link
        self.target = target
        super().__init__(f"{link} is a symlink to {target}, which does not exist")


def _default_registry_path() -> Path:
    """Compute the registry path lazily so env overrides work in tests.

    Precedence: MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config. This must
    stay in sync with wt's config.RegistryPath (wt reads the
    registry read-only and has no independent default).
    """
    override = os.environ.get("MODELMAN_REGISTRY")
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "registry.toml").expanduser()


@dataclass
class AuthConfig:
    type: str  # "none" | "api_key" | "oauth" | "native"
    secret_ref: str | None = None
    base_url: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class ProviderEntry:
    id: str
    name: str
    location: str | None = None  # "local" | "cloud"
    model_dir: str | None = None
    protocols: list[str] = field(default_factory=lambda: ["openai-chat"])
    auth: AuthConfig = field(default_factory=lambda: AuthConfig(type="none"))
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class Cost:
    input_price_per_million: float | None = None
    cache_price_per_million: float | None = None
    output_price_per_million: float | None = None
    subscription_price: float | None = None
    subscription_period: str | None = None
    time_prices: list[TimePrice] = field(default_factory=list)
    extra: dict[str, Any] = field(default_factory=dict, repr=False)

    def __post_init__(self) -> None:
        _validate_cost(self, source="Cost")


@dataclass
class Fetch:
    repo: str | None = None
    files: list[str] | None = None
    quantizations: list[str] | None = None
    local_path: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class DraftSpec:
    """The speculative-decoding draft model paired with a target ModelEntry's
    `fetch` (mlx_lm_server target+draft pairs). Either `repo` (HF repo id) or
    `local_path` (locally-produced mlx-lm directory) identifies the draft
    model; both may be set only in unusual hand-edited configs — callers
    don't enforce mutual exclusivity here, matching Fetch's own laxness."""

    repo: str | None = None
    local_path: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class ModelEntry:
    id: str
    family: str
    provider_id: str
    model_name: str
    location: str | None = None
    source: str | None = None  # "curated" | "discovered"
    tags: list[str] = field(default_factory=list)
    cost: Cost | None = None
    model_info: dict[str, Any] = field(default_factory=dict)
    fetch: Fetch | None = None
    draft: DraftSpec | None = None
    native: bool = False
    quantization: str | None = None
    pricing_updated_at: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class FamilyEntry:
    name: str
    display_name: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class Registry:
    providers: list[ProviderEntry] = field(default_factory=list)
    families: list[FamilyEntry] = field(default_factory=list)
    models: list[ModelEntry] = field(default_factory=list)

    def provider(self, provider_id: str) -> ProviderEntry:
        for p in self.providers:
            if p.id == provider_id:
                return p
        raise KeyError(f"Unknown provider: {provider_id}")

    def model(self, model_id: str) -> ModelEntry:
        for m in self.models:
            if m.id == model_id:
                return m
        raise KeyError(f"Unknown model: {model_id}")

    def family(self, name: str) -> FamilyEntry | None:
        for f in self.families:
            if f.name == name:
                return f
        return None

    def derived_families(self) -> list[str]:
        return sorted({m.family for m in self.models})

    def models_by_family(self, family: str) -> list[ModelEntry]:
        return [m for m in self.models if m.family == family]


def known_families(registry: Registry, state: StateStore) -> list[str]:
    """Sorted union of every family the TUI should show: families derived
    from models, first-class [[families]] entry names, and legacy
    state.families keys (read-side fallback)."""
    return sorted(
        set(registry.derived_families())
        | {f.name for f in registry.families}
        | set(state.families.keys())
    )


def base_origin(url: str | None) -> str | None:
    """Normalize a stored base_url to a bare origin (no /v1 suffix) so
    readers can compare/derive endpoints without mutating the stored
    value — registry base_url values are written verbatim into LiteLLM's
    config.yaml and must never be rewritten in place.
    """
    if url is None:
        return None
    trimmed = url.rstrip("/")
    if trimmed.endswith("/v1"):
        trimmed = trimmed[: -len("/v1")].rstrip("/")
    return trimmed


def provider_config(entry: ProviderEntry) -> dict[str, Any]:
    """Build the config dict `ProviderRegistry.get()` expects from a
    registry ProviderEntry. Only `model_dir` is read by any provider today
    (OMLXProvider) — kept minimal rather than mirroring every ProviderEntry
    field so providers stay decoupled from the registry schema.
    """
    config: dict[str, Any] = {}
    if entry.model_dir is not None:
        config["model_dir"] = entry.model_dir
    return config


# Canonical default entries for the reconcilable local providers. Shared by
# sync's repair path (sync.py) and migrate's legacy import (migrate.py) so a
# migrated registry and a sync-repaired one expose identically — same display
# name and same auth base_url. Kept as templates, never appended directly:
# callers must go through default_provider_entry() to get a fresh copy.
_DEFAULT_PROVIDER_TEMPLATES: dict[str, ProviderEntry] = {
    "ollama": ProviderEntry(
        id="ollama",
        name="Ollama",
        location="local",
        protocols=["anthropic", "openai-chat"],
        auth=AuthConfig(type="none", base_url="http://localhost:11434"),
    ),
    "llamacpp": ProviderEntry(id="llamacpp", name="llama.cpp", location="local"),
    "omlx": ProviderEntry(
        id="omlx",
        name="oMLX",
        location="local",
        protocols=["openai-chat"],
        auth=AuthConfig(type="none", base_url="http://localhost:8000"),
    ),
    "mlx_lm_server": ProviderEntry(
        id="mlx_lm_server",
        name="mlx-lm server (target+draft)",
        location="local",
        auth=AuthConfig(type="none", base_url="http://localhost:8001/v1"),
    ),
    "mtplx": ProviderEntry(
        id="mtplx",
        name="MTPLX",
        location="local",
        model_dir="~/.mtplx/models",
        protocols=["openai-chat"],
        auth=AuthConfig(type="none", base_url=MTPLX_V1_BASE),
    ),
}

# Provider ids that have a canonical default entry (the reconcilable local
# providers). migrate.py uses this to decide whether to use the default or
# fall back to its generic title()-cased import.
# llamacpp retired 2026-09-07 (issue #33): provider code kept in
# providers/llamacpp.py; re-enable steps in docs/reference/provider-artifacts.md.
DEFAULT_PROVIDER_IDS: tuple[str, ...] = ("ollama", "omlx", "mlx_lm_server", "mtplx")


def _default_wt_config_path() -> Path:
    """wt's config.toml, whose `[[agents]]` list names the
    native-provider agents (claude, codex, ...). Precedence: MODELMAN_WT_DIR
    > ~/.config/agent-wt, matching the usage subsystem's existing
    MODELMAN_WT_DIR convention (usage/wt_state.py)."""
    override = os.environ.get("MODELMAN_WT_DIR")
    base = Path(override).expanduser() if override else Path.home() / ".config" / "agent-wt"
    return base / "config.toml"


def sync_agent_providers(registry: Registry, wt_config_path: Path | None = None) -> list[str]:
    """Register every wt agent name missing from
    `registry.providers` as a native provider (auth.type="native",
    location="cloud"). Mutates `registry` in place; returns the ids added.

    A missing or unreadable wt config is not fatal — returns [] — matching
    migrate.py's existing tolerance for an absent wt install.
    """
    path = wt_config_path if wt_config_path is not None else _default_wt_config_path()
    if not path.exists():
        return []
    try:
        with open(path, "rb") as f:
            raw = tomllib.load(f)
    except (OSError, tomllib.TOMLDecodeError):
        # Malformed or unreadable wt config (partial edit, crash mid-write):
        # tolerate it like the missing-file case above, per this
        # function's own contract.
        return []
    existing = {p.id for p in registry.providers}
    added: list[str] = []
    agents = raw.get("agents", [])
    # Tolerate hand-edited configs where agents is not a list of tables.
    if not isinstance(agents, list):
        return []
    for agent in agents:
        if not isinstance(agent, dict):
            continue
        name = agent.get("name")
        if not name or name in existing:
            continue
        registry.providers.append(
            ProviderEntry(
                id=name,
                name=name.title(),
                location="cloud",
                auth=AuthConfig(type="native"),
            )
        )
        existing.add(name)
        added.append(name)
    if added:
        # load_registry ran _derive_native before these native providers
        # existed; re-derive so models referencing the new agents are
        # marked native in the same in-memory registry.
        _derive_native(registry)
    return added


def is_local_location(location: str | None) -> bool:
    """Return True only when a model/provider location is explicitly local.

    Legacy entries (location=None or empty) default to local because the
    original registry did not distinguish cloud entries; only entries that
    say ``location = "cloud"`` (or another non-local value) are excluded.
    """
    return location is None or location == "" or location == LOCATION_LOCAL


def is_model_local(
    location: str | None,
    provider_id: str,
    registry: Registry,
    *,
    missing_provider_is_local: bool = False,
) -> bool:
    """Whether a model counts as local: its own `location` override when
    set, else its provider's `location`. A `provider_id` missing from the
    registry is NOT local — mirroring model_has_local_artifact()'s
    treatment of the same edge case — unless `missing_provider_is_local`
    (sync's reconcile and benchmark discovery, which only need to skip
    explicitly-cloud models such as ollama `:cloud` stubs).

    Single definition for a resolution that had drifted into near-
    duplicate inline copies (queue.py's delete loop, its ready-off
    cascade, and the TUI's RUNNING column), each of which
    defaulted a missing provider to local via is_local_location(None).
    """
    if location is not None:
        return is_local_location(location)
    try:
        provider = registry.provider(provider_id)
    except KeyError:
        return missing_provider_is_local
    return is_local_location(provider.location)


def is_native_provider(provider: ProviderEntry) -> bool:
    """True when the provider authenticates natively (agent-managed, no
    LiteLLM route). The single shared predicate for native-ness — mirrors
    wt's deriveNative — so _derive_native, the form-kind map, and any
    future consumer can never disagree about which providers are native.
    """
    return provider.auth.type == "native"


def model_has_local_artifact(model: ModelEntry, provider: ProviderEntry | None) -> bool:
    """True when reconcile can sync `state.ready` from the filesystem.

    Classification is config-driven (ModelEntry.location,
    ProviderEntry.location), never a hard-coded provider id list, so a
    newly added local provider opts into reconcile-sync automatically.
    A model tagged location="cloud" (the ollama-cloud case: local
    provider, cloud model) or living on a cloud provider (openrouter,
    native agents) has no on-disk artifact reconcile could observe. A
    model referencing a provider missing from the registry is treated
    the same way — there is no reconciled source either way.
    """
    if model.location == LOCATION_CLOUD:
        return False
    if provider is not None and provider.location == LOCATION_CLOUD:
        return False
    return provider is not None


def default_provider_entry(provider_id: str) -> ProviderEntry:
    """Return a fresh default ProviderEntry for a reconcilable local provider.

    A new instance (and a new nested AuthConfig) is returned on every call so
    callers can mutate the result without corrupting the shared template.
    Raises KeyError for providers without a default.
    """
    template = _DEFAULT_PROVIDER_TEMPLATES.get(provider_id)
    if template is None:
        raise KeyError(f"No default provider entry for: {provider_id}")
    return replace(template, auth=replace(template.auth))


def _derive_native(registry: Registry) -> None:
    """Mark each model whose provider authenticates natively
    (auth.type == "native") as native. Mirrors wt's deriveNative.
    Runs after providers and models are parsed so the registry is the
    single source of truth for native-ness.
    """
    native_ids = {p.id for p in registry.providers if is_native_provider(p)}
    for m in registry.models:
        m.native = m.provider_id in native_ids


def unreadable_registry_message(exc: BaseException) -> str:
    """What to tell the user when load_registry() failed on a registry that
    is there: the file that was read — the pre-XDG one when XDG_CONFIG_HOME
    is set and holds no registry yet — and why. The caller adds what to run
    again. Not for RegistryNotFoundError, which is no failure to read."""
    if isinstance(exc, RegistryPathError):
        # A dangling symlink carries the path to report, because looking the
        # path up again is exactly what just failed (#248).
        return f"cannot read {exc.link}: {exc} — fix or move it aside"
    try:
        unreadable = _registry_read_path()
    except (RegistryError, OSError):
        # OSError: looking for the file is what failed (a directory that
        # cannot be searched), and it fails here as it did in load_registry.
        # RegistryError: the same, for a path that no longer resolves.
        # Raising either again would turn the caller's report into a traceback.
        unreadable = _default_registry_path()
    return f"cannot read {unreadable}: {exc} — fix or move it aside"


def _registry_read_path(path: Path | None = None) -> Path:
    """The file load_registry reads — not always _default_registry_path():
    see the pre-XDG fallback below. A caller reporting on a registry it could
    not read names this one. Raises RegistryNotFoundError when there is none,
    RegistryPathError when it is a link to a file that is not there."""
    registry_path = Path(path) if path else _default_registry_path()
    # Before exists(), which is False both for a dangling symlink and for
    # nothing at all — and a link is a pointer at where the registry lives,
    # not an absent registry, so it is refused rather than fallen back from
    # (#248). A link that resolves reads through as any other file does.
    if registry_path.is_symlink() and not registry_path.exists():
        raise RegistryPathError(registry_path, os.readlink(registry_path))
    if registry_path.exists():
        return registry_path
    # Fall back to the pre-XDG location for users who created a registry
    # before the XDG alignment and have XDG_CONFIG_HOME set. The next
    # save_registry writes to the canonical (XDG) path, migrating it.
    # Not past MODELMAN_REGISTRY: that names the file outright, and a
    # missing one there is missing, not "use the one in ~/.config".
    legacy = Path("~/.config/local-ai/registry.toml").expanduser()
    if (
        path is None
        and not os.environ.get("MODELMAN_REGISTRY")
        and registry_path != legacy
        and legacy.exists()
    ):
        return legacy
    raise RegistryNotFoundError(f"Registry file not found: {registry_path}")


# The top-level tables a registry may hold. _write_registry emits exactly
# these three, so anything else is a section a save would drop (#247) —
# which _reject_unknown_top_level_keys refuses rather than reading past.
_TOP_LEVEL_KEYS = frozenset({"providers", "families", "models"})


def _reject_unknown_top_level_keys(raw: dict[str, Any]) -> None:
    """Refuse a registry whose content sits under a top-level key nothing reads.

    `[[model]]` for `[[models]]` is a plausible hand edit, and it reads as
    zero models: the file parses, load_registry returns an empty Registry with
    no message, and the first save rewrites the file without the entries. That
    is the data loss #240 covers for a file that fails to parse, reached
    through a file that parses perfectly well (#247). The parser is the only
    place that can tell "no models" from "models under a name I do not read",
    so it refuses here, where the callers that already report an unreadable
    registry (#240) pick it up.

    A nested unknown key is deliberately NOT this: it belongs to one entry and
    survives the round trip through unknown_keys(). Only a top-level one names
    a whole section the writer cannot emit.
    """
    unknown = sorted(set(raw) - _TOP_LEVEL_KEYS)
    if not unknown:
        return
    keys = ", ".join(f"`{key}`" for key in unknown)
    known = "/".join(sorted(_TOP_LEVEL_KEYS))
    raise RegistryError(
        f"unknown top-level key(s) {keys}: a registry holds {known} only, "
        f"and saving would drop everything under anything else"
    )


def load_registry(path: Path | None = None) -> Registry:
    registry_path = _registry_read_path(path)
    with open(registry_path, "rb") as f:
        raw = tomllib.load(f)
    _reject_unknown_top_level_keys(raw)
    registry = Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        families=[_parse_family(f) for f in raw.get("families", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )
    _derive_native(registry)
    return registry


def _auth_to_dict(a: AuthConfig) -> dict[str, Any]:
    d = {"type": a.type, "secret_ref": a.secret_ref, "base_url": a.base_url}
    return drop_none({**a.extra, **d})


def _provider_to_dict(p: ProviderEntry) -> dict[str, Any]:
    d = {
        "id": p.id,
        "name": p.name,
        "location": p.location,
        "model_dir": p.model_dir,
        "protocols": list(p.protocols) if p.protocols and p.protocols != ["openai-chat"] else None,
        "auth": _auth_to_dict(p.auth),
    }
    return drop_none({**p.extra, **d})


def _cost_to_dict(c: Cost) -> dict[str, Any]:
    d: dict[str, Any] = {field: getattr(c, field) for field in _COST_FIELDS}
    d["time_prices"] = [time_price_to_dict(tp) for tp in c.time_prices] or None
    return drop_none({**c.extra, **d})


def _cost_from_dict(d: dict[str, Any]) -> Cost:
    """Reconstruct a Cost from a plain dict (e.g. a VariantSpec value).

    Unknown keys are preserved in `extra` so hand-edited fields survive
    the round-trip. Callers that have already validated the dict may use
    this directly; `_parse_cost` is the path that validates raw TOML.
    """
    return Cost(
        input_price_per_million=d.get("input_price_per_million"),
        cache_price_per_million=d.get("cache_price_per_million"),
        output_price_per_million=d.get("output_price_per_million"),
        subscription_price=d.get("subscription_price"),
        subscription_period=d.get("subscription_period"),
        time_prices=parse_time_prices(d.get("time_prices")),
        extra=unknown_keys(d, _COST_FIELDS | _LEGACY_COST_FIELDS | _COST_STRUCTURED_FIELDS),
    )


def model_entry_to_variant(entry: ModelEntry) -> VariantSpec:
    """Build a VariantSpec-shaped dict from a ModelEntry for provider APIs.

    Providers consume the legacy TypedDict (provider, name, repo,
    files, model_info). ModelEntry stores repo/files in `fetch`. We
    don't carry `model_info` from the registry into the provider call
    (providers read what they need from their own state).

    `cost` is serialized to a plain dict so any provider that JSON-
    serializes its VariantSpec argument does not receive a non-JSON
    dataclass.

    Lives here (not in the screen layer) so queue.py and sync-adjacent
    callers can build specs without importing screens (screens import
    queue — importing back would be circular).
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
        "cost": _cost_to_dict(entry.cost) if entry.cost is not None else None,
        "quantization": entry.quantization,
    }


def find_shared_artifact_owner(
    registry: Registry, provider: object, variant: VariantSpec
) -> ModelEntry | None:
    """Another registry entry whose provider artifact resolves to the same
    on-disk target as `variant`'s, or None.

    Dir-based providers key their storage on a coarse segment of the repo
    id (omlx uses the repo *basename*), so two registry entries can share
    one artifact directory; removing it for one silently destroys the
    other's weights. artifact_paths() is the provider-neutral way to ask
    "where does this variant live on disk" (path_of() is intentionally
    display-only for multi-directory providers). Returns None when the
    provider has no artifact_paths or the paths can't be resolved —
    callers treat that as "no conflict" and proceed with the normal delete.

    What is compared is the set the provider's delete() would really remove
    (Provider.removable_paths) against everywhere each other entry lives
    (artifact_paths): an entry whose delete is a no-op shares nothing that
    can be lost. Both sides are resolved to real directories first, so the
    spelling of a path does not matter.

    "Another entry" is any other entry in the registry, on any provider,
    compared through its own row's settings; it owns what is removed when
    one of its paths is the same directory, inside it, or a parent of it
    (#241).

    Shared by queue.py's delete/ready-off steps and its cancelled-download
    cleanup (_cleanup_partial_download) — all three remove an on-disk
    artifact and must not do so when another registry entry still owns it.
    """
    artifact_paths = getattr(provider, "artifact_paths", None)
    if not callable(artifact_paths):
        return None
    # Only what delete() would actually remove is at risk (#227): an omlx
    # `local_path` directory is never removed, so sharing it is no conflict
    # and must not be reported as one. And it is the whole of what is at
    # risk, wherever the entry itself lives (#229): an mlx_lm_server side
    # with both a repo and a local_path lives in the local_path and removes
    # the repo's download. A provider that does not say (or a test double
    # answering with something that is not a set) guards every path it
    # lives in.
    mine: Any = None
    removable_paths = getattr(provider, "removable_paths", None)
    if callable(removable_paths):
        with contextlib.suppress(Exception):
            mine = removable_paths(variant)
    if not isinstance(mine, (set, frozenset)):
        try:
            mine = artifact_paths(variant)
        except Exception:  # noqa: BLE001
            return None
    if not mine:
        return None
    # Every other entry in the registry is a possible owner, whichever
    # provider it is on (#241): omlx and mlx_lm_server both read MLX
    # directories, and a `local_path` can name any directory at all. An entry
    # on another row is asked through ITS OWN row's provider — its own
    # model_dir — never through the deleting row's: two rows pointed at
    # different directories share nothing (#224).
    #
    # What is removed is resolved once, not once per entry: every key costs a
    # realpath and a stat, for the directory and for each of its parents.
    mine_keys = overlap_keys(mine)
    others: dict[str, Any] = {variant["provider"]: artifact_paths}
    for m in registry.models:
        if m.id == variant["id"]:
            continue
        if m.provider_id not in others:
            others[m.provider_id] = _row_artifact_paths(registry, m.provider_id)
        their_paths = others[m.provider_id]
        if their_paths is None:
            continue
        try:
            theirs = overlap_keys(their_paths(model_entry_to_variant(m)))
        except Exception:  # noqa: BLE001
            continue
        if keys_overlap(mine_keys, theirs):
            return m
    return None


def _row_artifact_paths(registry: Registry, provider_id: str) -> Any:
    """artifact_paths of the provider built for registry row `provider_id`,
    or None when that row is missing or its provider cannot be built."""
    try:
        row = registry.provider(provider_id)
        provider = ProviderRegistry.get(provider_id, provider_config(row))
    except Exception:  # noqa: BLE001
        return None
    fn = getattr(provider, "artifact_paths", None)
    return fn if callable(fn) else None


def _fetch_to_dict(f: Fetch) -> dict[str, Any]:
    d = {
        "repo": f.repo,
        "files": f.files,
        "quantizations": f.quantizations,
        "local_path": f.local_path,
    }
    return drop_none({**f.extra, **d})


def _draft_to_dict(d: DraftSpec) -> dict[str, Any]:
    """Modeled line-for-line on _fetch_to_dict — same drop_none() +
    typed-fields-win merge order for the `.extra` round-trip."""
    fields = {"repo": d.repo, "local_path": d.local_path}
    return drop_none({**d.extra, **fields})


def _model_to_dict(m: ModelEntry) -> dict[str, Any]:
    d = {
        "id": m.id,
        "family": m.family,
        "provider_id": m.provider_id,
        "model_name": m.model_name,
        "location": m.location,
        "source": m.source,
        "tags": m.tags,
        "cost": _cost_to_dict(m.cost) if m.cost is not None else None,
        "model_info": m.model_info,
        "fetch": _fetch_to_dict(m.fetch) if m.fetch is not None else None,
        "quantization": m.quantization,
        "pricing_updated_at": m.pricing_updated_at,
        # A DraftSpec with no fields and no extra set serializes to {}; drop
        # it (rather than writing an empty [models.draft] table) since
        # `draft is None` and "draft carries nothing" should look identical
        # on disk — most mlx_lm_server target-only entries have no draft.
        "draft": (_draft_to_dict(m.draft) or None) if m.draft is not None else None,
    }
    return drop_none({**m.extra, **d})


def _family_to_dict(f: FamilyEntry) -> dict[str, Any]:
    d = {"name": f.name, "display_name": f.display_name}
    return drop_none({**f.extra, **d})


def _write_registry(registry: Registry, path: Path) -> None:
    """Serialize ``registry`` to ``path`` atomically.

    The write half of save_registry, split out so locked_registry() can save
    under the same lock without calling save_registry() (which would deadlock
    on a non-reentrant lock).

    Refuses to write when ``path`` is a symlink to a file that is not there:
    os.replace() swaps the link itself for a regular file, so a volume that
    unmounted while this process held a registry loaded would be shadowed by
    the empty one written in its place (#248). A link that resolves is a
    normal save (the link is still replaced by the file it pointed at; the
    content is what the user was editing either way).
    """
    if path.is_symlink() and not path.exists():
        raise RegistryPathError(path, os.readlink(path))
    payload = {
        "providers": [_provider_to_dict(p) for p in registry.providers],
        "families": [_family_to_dict(f) for f in registry.families],
        "models": [_model_to_dict(m) for m in registry.models],
    }
    atomic_write_toml(payload, path)


# Process-wide lock serializing every registry.toml write. registry.toml has
# multiple concurrent writers once the startup price-refresh worker (app.py)
# can save on a background thread while ModelScreen.save_registry and
# PendingChanges.apply() save from the main thread. save_registry() is a
# whole-file overwrite of whatever Registry object it is handed — with no
# lock, two such writers silently clobber each other's unrelated changes.
_REGISTRY_LOCK = threading.Lock()


def save_registry(registry: Registry, path: Path | None = None) -> None:
    """Serialize ``registry`` to registry.toml, holding the process lock so
    concurrent writers (the startup price-refresh worker vs. main-thread
    screen/apply saves) cannot interleave and drop each other's changes.

    Callers that already hold _REGISTRY_LOCK (inside locked_registry()) must
    call _write_registry() directly to avoid deadlocking on the
    non-reentrant lock.
    """
    registry_path = Path(path) if path else _default_registry_path()
    with _REGISTRY_LOCK:
        _write_registry(registry, registry_path)


@contextlib.contextmanager
def locked_registry(path: Path | None = None) -> Generator[Registry]:
    """Atomically read-modify-write registry.toml.

    The registry analogue of state.locked_state(): acquires the process lock,
    loads the current on-disk Registry, yields it for the caller to mutate in
    place, and saves it back before releasing the lock. The startup
    price-refresh worker uses this so its refresh prices are always applied on
    top of the latest on-disk registry, rather than a possibly-stale snapshot
    that overwrites a model the user added/edited while the API fetch was in
    flight.
    """
    with _REGISTRY_LOCK:
        registry = load_registry(path)
        yield registry
        _write_registry(registry, path if path is not None else _default_registry_path())


def _parse_provider(raw: dict[str, Any]) -> ProviderEntry:
    if "id" not in raw:
        raise RegistryError(f"Provider entry missing required `id` field: {raw}")
    auth_raw = raw.get("auth", {})
    if "type" not in auth_raw:
        raise RegistryError(f"Provider `{raw['id']}` auth missing required `type` field")
    return ProviderEntry(
        id=raw["id"],
        name=raw.get("name", raw["id"]),
        location=raw.get("location"),
        model_dir=raw.get("model_dir"),
        # An absent (or empty) `protocols` key must parse to [], not the
        # ["openai-chat"] default — otherwise backfill_provider_defaults
        # (sync.py) can never tell "field predates this schema" from
        # "field explicitly set", and a pre-upgrade omlx/ollama entry stays
        # permanently stuck without its template's protocols.
        protocols=list(raw.get("protocols") or []),
        auth=AuthConfig(
            type=auth_raw["type"],
            secret_ref=auth_raw.get("secret_ref"),
            base_url=auth_raw.get("base_url"),
            extra=unknown_keys(auth_raw, {"type", "secret_ref", "base_url"}),
        ),
        extra=unknown_keys(raw, {"id", "name", "location", "model_dir", "auth", "protocols"}),
    )


def _parse_family(raw: dict[str, Any]) -> FamilyEntry:
    if "name" not in raw:
        raise RegistryError(f"Family entry missing required `name` field: {raw}")
    return FamilyEntry(
        name=raw["name"],
        display_name=raw.get("display_name"),
        extra=unknown_keys(raw, {"name", "display_name"}),
    )


def _build_cost(model_id: str, **kwargs: Any) -> Cost:
    """Construct a Cost, translating a validation ValueError into a
    model-scoped RegistryError.

    `Cost.__post_init__` raises ValueError with a message prefixed
    "Cost ..."; every _parse_cost call site needs that reworded to name
    the offending model, so this centralizes the translation once instead
    of repeating the try/except at each construction site.
    """
    try:
        return Cost(**kwargs)
    except ValueError as exc:
        msg = str(exc).replace("Cost", f"Model `{model_id}` cost", 1)
        raise RegistryError(msg) from exc


def _time_prices_or_error(model_id: str, raw: Any) -> list[TimePrice]:
    try:
        return parse_time_prices(raw)
    except ValueError as exc:
        raise RegistryError(f"Model `{model_id}` cost {exc}") from exc


def _parse_cost(model_id: str, cost_raw: Any) -> Cost:
    """Validate and construct a Cost from its raw TOML table.

    Supports the legacy `kind` enum for backward migration and the new
    flat per-token/subscription fields.
    """
    if not isinstance(cost_raw, dict):
        raise RegistryError(
            f"Model `{model_id}` cost must be a table, got {type(cost_raw).__name__}"
        )

    def _number_or_none(name: str) -> float | None:
        value = cost_raw.get(name)
        if value is None:
            return None
        # Reject booleans explicitly: bool is a subclass of int in Python.
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise RegistryError(
                f"Model `{model_id}` cost `{name}` must be a number, got {type(value).__name__}"
            )
        if not math.isfinite(value):
            raise RegistryError(f"Model `{model_id}` cost `{name}` must be finite, got {value}")
        return float(value)

    def _subscription_period_or_none(name: str) -> str | None:
        value = cost_raw.get(name)
        if value is None:
            return None
        if not isinstance(value, str):
            raise RegistryError(
                f"Model `{model_id}` cost `{name}` must be a string, got {type(value).__name__}"
            )
        return value

    # Legacy `kind` enum migration path.
    if "kind" in cost_raw:
        kind = cost_raw["kind"]
        if kind == "free":
            return _build_cost(
                model_id,
                time_prices=_time_prices_or_error(model_id, cost_raw.get("time_prices")),
                extra=unknown_keys(
                    cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS | _COST_STRUCTURED_FIELDS
                ),
            )
        if kind == "per_token":
            price = _number_or_none("price_per_million_tokens")
            return _build_cost(
                model_id,
                input_price_per_million=price,
                output_price_per_million=price,
                time_prices=_time_prices_or_error(model_id, cost_raw.get("time_prices")),
                extra=unknown_keys(
                    cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS | _COST_STRUCTURED_FIELDS
                ),
            )
        if kind == "subscription":
            return _build_cost(
                model_id,
                subscription_price=_number_or_none("price_per_period"),
                subscription_period=_subscription_period_or_none("period"),
                time_prices=_time_prices_or_error(model_id, cost_raw.get("time_prices")),
                extra=unknown_keys(
                    cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS | _COST_STRUCTURED_FIELDS
                ),
            )
        raise RegistryError(
            f"Model `{model_id}` cost kind must be free/per_token/subscription, got {kind!r}"
        )

    # New flat fields.
    subscription_price = _number_or_none("subscription_price")
    subscription_period = _subscription_period_or_none("subscription_period")
    if subscription_price is not None and subscription_price >= 0 and subscription_period is None:
        raise RegistryError(
            f"Model `{model_id}` cost `subscription_period` is required when `subscription_price` is set"
        )

    return _build_cost(
        model_id,
        input_price_per_million=_number_or_none("input_price_per_million"),
        cache_price_per_million=_number_or_none("cache_price_per_million"),
        output_price_per_million=_number_or_none("output_price_per_million"),
        subscription_price=subscription_price,
        subscription_period=subscription_period,
        time_prices=_time_prices_or_error(model_id, cost_raw.get("time_prices")),
        extra=unknown_keys(cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS | _COST_STRUCTURED_FIELDS),
    )


def _parse_fetch(fetch_raw: dict[str, Any]) -> Fetch:
    return Fetch(
        repo=fetch_raw.get("repo"),
        files=fetch_raw.get("files"),
        quantizations=fetch_raw.get("quantizations"),
        local_path=fetch_raw.get("local_path"),
        extra=unknown_keys(fetch_raw, {"repo", "files", "quantizations", "local_path"}),
    )


def _parse_draft(draft_raw: dict[str, Any]) -> DraftSpec:
    """Modeled line-for-line on _parse_fetch — same unknown_keys() usage
    and typed-fields-win extra merge order for the round-trip."""
    return DraftSpec(
        repo=draft_raw.get("repo"),
        local_path=draft_raw.get("local_path"),
        extra=unknown_keys(draft_raw, {"repo", "local_path"}),
    )


def _parse_model(raw: dict[str, Any]) -> ModelEntry:
    required = {"id", "family", "provider_id", "model_name"}
    missing = required - set(raw.keys())
    if missing:
        raise RegistryError(f"Model entry missing required fields {missing}: {raw}")
    cost_raw = raw.get("cost")
    cost = _parse_cost(raw["id"], cost_raw) if cost_raw is not None else None
    fetch_raw = raw.get("fetch")
    fetch = _parse_fetch(fetch_raw) if fetch_raw is not None else None
    draft_raw = raw.get("draft")
    draft = _parse_draft(draft_raw) if draft_raw is not None else None
    return ModelEntry(
        id=raw["id"],
        family=raw["family"],
        provider_id=raw["provider_id"],
        model_name=raw["model_name"],
        location=raw.get("location"),
        source=raw.get("source"),
        tags=list(raw.get("tags", [])),
        cost=cost,
        model_info=dict(raw.get("model_info", {})),
        fetch=fetch,
        draft=draft,
        quantization=raw.get("quantization"),
        pricing_updated_at=raw.get("pricing_updated_at"),
        extra=unknown_keys(
            raw,
            {
                "id",
                "family",
                "provider_id",
                "model_name",
                "location",
                "source",
                "tags",
                "cost",
                "model_info",
                "fetch",
                "draft",
                "usage_tier",
                "quantization",
                "pricing_updated_at",
            },
        ),
    )
