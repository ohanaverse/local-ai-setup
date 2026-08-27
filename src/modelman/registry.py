"""registry.toml — the canonical, shared model/provider registry.

Owned exclusively by modelman (see docs/superpowers/specs/2026-08-27-
shared-model-registry-design.md). agent-worktree reads this file
read-only; it never writes it.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Literal

from ._toml_io import atomic_write_toml, drop_none


class RegistryError(Exception):
    """Raised when registry.toml is missing or malformed."""


def _default_registry_path() -> Path:
    """Compute the registry path lazily so env overrides work in tests."""
    return Path(
        os.environ.get("MODELMAN_REGISTRY", "~/.config/local-ai/registry.toml")
    ).expanduser()


@dataclass
class AuthConfig:
    type: str  # "none" | "api_key" | "oauth" | "native"
    secret_ref: str | None = None
    base_url: str | None = None


@dataclass
class ProviderEntry:
    id: str
    name: str
    location: str | None = None  # "local" | "cloud"
    model_dir: str | None = None
    auth: AuthConfig = field(default_factory=lambda: AuthConfig(type="none"))


@dataclass
class Cost:
    kind: Literal["free", "per_token", "subscription"]
    price_per_million_tokens: float | None = None
    price_per_period: float | None = None
    period: str | None = None


@dataclass
class Fetch:
    repo: str | None = None
    files: list[str] | None = None
    quantizations: list[str] | None = None


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


def load_registry(path: Path | None = None) -> Registry:
    registry_path = Path(path) if path else _default_registry_path()
    if not registry_path.exists():
        raise RegistryError(f"Registry file not found: {registry_path}")
    with open(registry_path, "rb") as f:
        raw = tomllib.load(f)
    return Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )


def save_registry(registry: Registry, path: Path | None = None) -> None:
    registry_path = Path(path) if path else _default_registry_path()
    payload = {
        "providers": [drop_none(asdict(p)) for p in registry.providers],
        "models": [drop_none(asdict(m)) for m in registry.models],
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
        ),
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
        )
    fetch_raw = raw.get("fetch")
    fetch = (
        Fetch(
            repo=fetch_raw.get("repo"),
            files=fetch_raw.get("files"),
            quantizations=fetch_raw.get("quantizations"),
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
    )
