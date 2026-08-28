"""registry.toml — the canonical, shared model/provider registry.

Owned exclusively by modelman (see docs/superpowers/specs/2026-08-27-
shared-model-registry-design.md). agent-worktree reads this file
read-only; it never writes it.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field, replace
from pathlib import Path
from typing import Any, Literal

from ._toml_io import atomic_write_toml, drop_none, unknown_keys


class RegistryError(Exception):
    """Raised when registry.toml is missing or malformed."""


def _default_registry_path() -> Path:
    """Compute the registry path lazily so env overrides work in tests.

    Precedence: MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config. This must
    stay in sync with agent-worktree's config.RegistryPath (wt reads the
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
    kind: Literal["free", "per_token", "subscription"]
    price_per_million_tokens: float | None = None
    price_per_period: float | None = None
    period: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


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
class Registry:
    providers: list[ProviderEntry] = field(default_factory=list)
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

    def families(self) -> list[str]:
        return sorted({m.family for m in self.models})

    def models_by_family(self, family: str) -> list[ModelEntry]:
        return [m for m in self.models if m.family == family]


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
    d = {
        "kind": c.kind,
        "price_per_million_tokens": c.price_per_million_tokens,
        "price_per_period": c.price_per_period,
        "period": c.period,
    }
    return drop_none({**c.extra, **d})


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


def save_registry(registry: Registry, path: Path | None = None) -> None:
    registry_path = Path(path) if path else _default_registry_path()
    payload = {
        "providers": [_provider_to_dict(p) for p in registry.providers],
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


def _parse_model(raw: dict[str, Any]) -> ModelEntry:
    required = {"id", "family", "provider_id", "model_name"}
    missing = required - set(raw.keys())
    if missing:
        raise RegistryError(f"Model entry missing required fields {missing}: {raw}")
    cost_raw = raw.get("cost")
    cost = None
    if cost_raw is not None:
        if "kind" not in cost_raw:
            raise RegistryError(f"Model `{raw['id']}` cost missing required `kind` field")
        cost = Cost(
            kind=cost_raw["kind"],
            price_per_million_tokens=cost_raw.get("price_per_million_tokens"),
            price_per_period=cost_raw.get("price_per_period"),
            period=cost_raw.get("period"),
            extra=unknown_keys(
                cost_raw, {"kind", "price_per_million_tokens", "price_per_period", "period"}
            ),
        )
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
            },
        ),
    )
