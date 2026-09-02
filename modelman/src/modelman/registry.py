"""registry.toml — the canonical, shared model/provider/family registry.

Owned exclusively by modelman (see docs/superpowers/specs/2026-08-27-
shared-model-registry-design.md). wt reads this file
read-only; it never writes it. Families are first-class [[families]]
entries here (see docs/superpowers/specs/2026-08-29-modelman-first-class-
families-design.md); their display names live in the entry, not in
modelman.toml.
"""

from __future__ import annotations

import math
import os
import tomllib
from dataclasses import dataclass, field, replace
from pathlib import Path
from typing import TYPE_CHECKING, Any

from ._toml_io import atomic_write_toml, drop_none, unknown_keys

if TYPE_CHECKING:
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

# Canonical location values used across the TUI and providers.
LOCATION_LOCAL = "local"
LOCATION_CLOUD = "cloud"


class RegistryError(Exception):
    """Raised when registry.toml is missing or malformed."""


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
    auth: AuthConfig = field(default_factory=lambda: AuthConfig(type="none"))
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class Cost:
    input_price_per_million: float | None = None
    cache_price_per_million: float | None = None
    output_price_per_million: float | None = None
    subscription_price: float | None = None
    subscription_period: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)

    def __post_init__(self) -> None:
        _validate_cost(self, source="Cost")


@dataclass
class Fetch:
    repo: str | None = None
    files: list[str] | None = None
    quantizations: list[str] | None = None
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


def family_display_name(registry: Registry, state: StateStore, family: str) -> str | None:
    """Registry entry display_name, else legacy state display_name, else
    None. Callers decide the fallback (table column: ""; edit prefill:
    the family name)."""
    entry = registry.family(family)
    if entry is not None and entry.display_name:
        return entry.display_name
    legacy = state.families.get(family)
    if legacy is not None and legacy.display_name:
        return legacy.display_name
    return None


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
        auth=AuthConfig(type="none", base_url="http://localhost:11434"),
    ),
    "llamacpp": ProviderEntry(id="llamacpp", name="llama.cpp", location="local"),
    "omlx": ProviderEntry(id="omlx", name="oMLX", location="local"),
}

# Provider ids that have a canonical default entry (the reconcilable local
# providers). migrate.py uses this to decide whether to use the default or
# fall back to its generic title()-cased import.
DEFAULT_PROVIDER_IDS: tuple[str, ...] = ("ollama", "llamacpp", "omlx")


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
    with open(path, "rb") as f:
        raw = tomllib.load(f)
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
    return added


def is_local_location(location: str | None) -> bool:
    """Return True only when a model/provider location is explicitly local.

    Legacy entries (location=None or empty) default to local because the
    original registry did not distinguish cloud entries; only entries that
    say ``location = "cloud"`` (or another non-local value) are excluded.
    """
    return location is None or location == "" or location == LOCATION_LOCAL


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


def load_registry(path: Path | None = None) -> Registry:
    registry_path = Path(path) if path else _default_registry_path()
    if not registry_path.exists():
        # Fall back to the pre-XDG location for users who created a registry
        # before the XDG alignment and have XDG_CONFIG_HOME set. The next
        # save_registry writes to the canonical (XDG) path, migrating it.
        legacy = Path("~/.config/local-ai/registry.toml").expanduser()
        if path is None and registry_path != legacy and legacy.exists():
            registry_path = legacy
        else:
            raise RegistryError(f"Registry file not found: {registry_path}")
    with open(registry_path, "rb") as f:
        raw = tomllib.load(f)
    return Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        families=[_parse_family(f) for f in raw.get("families", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )


def _auth_to_dict(a: AuthConfig) -> dict[str, Any]:
    d = {"type": a.type, "secret_ref": a.secret_ref, "base_url": a.base_url}
    return drop_none({**a.extra, **d})


def _provider_to_dict(p: ProviderEntry) -> dict[str, Any]:
    d = {
        "id": p.id,
        "name": p.name,
        "location": p.location,
        "model_dir": p.model_dir,
        "auth": _auth_to_dict(p.auth),
    }
    return drop_none({**p.extra, **d})


def _cost_to_dict(c: Cost) -> dict[str, Any]:
    d = {field: getattr(c, field) for field in _COST_FIELDS}
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
        extra=unknown_keys(d, _COST_FIELDS | _LEGACY_COST_FIELDS),
    )


def _fetch_to_dict(f: Fetch) -> dict[str, Any]:
    d = {"repo": f.repo, "files": f.files, "quantizations": f.quantizations}
    return drop_none({**f.extra, **d})


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
    }
    return drop_none({**m.extra, **d})


def _family_to_dict(f: FamilyEntry) -> dict[str, Any]:
    d = {"name": f.name, "display_name": f.display_name}
    return drop_none({**f.extra, **d})


def save_registry(registry: Registry, path: Path | None = None) -> None:
    registry_path = Path(path) if path else _default_registry_path()
    payload = {
        "providers": [_provider_to_dict(p) for p in registry.providers],
        "families": [_family_to_dict(f) for f in registry.families],
        "models": [_model_to_dict(m) for m in registry.models],
    }
    atomic_write_toml(payload, registry_path)


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
        auth=AuthConfig(
            type=auth_raw["type"],
            secret_ref=auth_raw.get("secret_ref"),
            base_url=auth_raw.get("base_url"),
            extra=unknown_keys(auth_raw, {"type", "secret_ref", "base_url"}),
        ),
        extra=unknown_keys(raw, {"id", "name", "location", "model_dir", "auth"}),
    )


def _parse_family(raw: dict[str, Any]) -> FamilyEntry:
    if "name" not in raw:
        raise RegistryError(f"Family entry missing required `name` field: {raw}")
    return FamilyEntry(
        name=raw["name"],
        display_name=raw.get("display_name"),
        extra=unknown_keys(raw, {"name", "display_name"}),
    )


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
            try:
                return Cost(extra=unknown_keys(cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS))
            except ValueError as exc:
                msg = str(exc).replace("Cost", f"Model `{model_id}` cost", 1)
                raise RegistryError(msg) from exc
        if kind == "per_token":
            price = _number_or_none("price_per_million_tokens")
            try:
                return Cost(
                    input_price_per_million=price,
                    output_price_per_million=price,
                    extra=unknown_keys(cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS),
                )
            except ValueError as exc:
                msg = str(exc).replace("Cost", f"Model `{model_id}` cost", 1)
                raise RegistryError(msg) from exc
        if kind == "subscription":
            try:
                return Cost(
                    subscription_price=_number_or_none("price_per_period"),
                    subscription_period=_subscription_period_or_none("period"),
                    extra=unknown_keys(cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS),
                )
            except ValueError as exc:
                msg = str(exc).replace("Cost", f"Model `{model_id}` cost", 1)
                raise RegistryError(msg) from exc
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

    try:
        return Cost(
            input_price_per_million=_number_or_none("input_price_per_million"),
            cache_price_per_million=_number_or_none("cache_price_per_million"),
            output_price_per_million=_number_or_none("output_price_per_million"),
            subscription_price=subscription_price,
            subscription_period=subscription_period,
            extra=unknown_keys(cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS),
        )
    except ValueError as exc:
        msg = str(exc).replace("Cost", f"Model `{model_id}` cost", 1)
        raise RegistryError(msg) from exc


def _parse_model(raw: dict[str, Any]) -> ModelEntry:
    required = {"id", "family", "provider_id", "model_name"}
    missing = required - set(raw.keys())
    if missing:
        raise RegistryError(f"Model entry missing required fields {missing}: {raw}")
    cost_raw = raw.get("cost")
    cost = _parse_cost(raw["id"], cost_raw) if cost_raw is not None else None
    fetch_raw = raw.get("fetch")
    fetch = (
        Fetch(
            repo=fetch_raw.get("repo"),
            files=fetch_raw.get("files"),
            quantizations=fetch_raw.get("quantizations"),
            extra=unknown_keys(fetch_raw, {"repo", "files", "quantizations"}),
        )
        if fetch_raw is not None
        else None
    )
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
                "usage_tier",
            },
        ),
    )
