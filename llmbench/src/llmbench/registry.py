"""Read-only view of registry.toml: the fields the benchmarks read, no more.

wt owns and writes the file. llmbench never writes it, so this reader keeps
no unknown keys, parses no prices and accepts a top-level table it does not
know.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from ._env import env_first

# The local providers the benchmarks can isolate and run against.
DEFAULT_PROVIDER_IDS: tuple[str, ...] = ("ollama", "omlx", "mlx_lm_server", "mtplx")


class RegistryError(Exception):
    """registry.toml is missing or cannot be read."""


@dataclass
class ProviderEntry:
    id: str
    name: str = ""
    location: str | None = None  # "local" | "cloud"


@dataclass
class Fetch:
    repo: str | None = None
    local_path: str | None = None


@dataclass
class DraftSpec:
    """The draft model paired with a target's `fetch` (mlx_lm_server pairings)."""

    repo: str | None = None
    local_path: str | None = None


@dataclass
class ModelEntry:
    id: str
    family: str
    provider_id: str
    model_name: str
    location: str | None = None
    fetch: Fetch | None = None
    draft: DraftSpec | None = None


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


def _is_local_location(location: str | None) -> bool:
    return location is None or location == "" or location == "local"


def is_model_local(
    location: str | None,
    provider_id: str,
    registry: Registry,
    *,
    missing_provider_is_local: bool = False,
) -> bool:
    """Whether a model counts as local: its own `location` when set, else its
    provider's. A provider missing from the registry is not local unless
    `missing_provider_is_local`."""
    if location is not None:
        return _is_local_location(location)
    try:
        provider = registry.provider(provider_id)
    except KeyError:
        return missing_provider_is_local
    return _is_local_location(provider.location)


# The variables that name registry.toml outright, in precedence order.
# WT_REGISTRY is the name wt and llmbench share; MODELMAN_REGISTRY is the older
# name, kept as an alias.
_REGISTRY_ENV_NAMES = ("WT_REGISTRY", "MODELMAN_REGISTRY")


def registry_path() -> Path:
    """Where the registry lives: WT_REGISTRY > MODELMAN_REGISTRY >
    XDG_CONFIG_HOME > ~/.config.

    The same precedence as wt's config.RegistryPath."""
    override = env_first(*_REGISTRY_ENV_NAMES)
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "registry.toml").expanduser()


def _refuse_dangling_symlink(path: Path) -> None:
    """A link to a file that is not there is a pointer at where the registry
    lives, not an absent registry: refuse it (#248)."""
    if path.is_symlink() and not path.exists():
        raise RegistryError(f"{path} is a symlink to {os.readlink(path)}, which does not exist")


def registry_read_path(path: Path | None = None) -> Path:
    """The file load_registry reads: `path`, or registry_path(). A dangling
    symlink is refused (#248); a file that is not there is an error naming
    it. The same file wt's config.RegistryPath names: there is no second
    place to look."""
    wanted = Path(path) if path else registry_path()
    _refuse_dangling_symlink(wanted)
    if wanted.exists():
        return wanted
    raise RegistryError(f"Registry file not found: {wanted}")


def _parse_provider(raw: dict[str, Any]) -> ProviderEntry:
    if "id" not in raw:
        raise RegistryError(f"Provider entry missing required `id` field: {raw}")
    return ProviderEntry(
        id=raw["id"], name=raw.get("name", raw["id"]), location=raw.get("location")
    )


def _parse_model(raw: dict[str, Any]) -> ModelEntry:
    missing = {"id", "family", "provider_id", "model_name"} - set(raw)
    if missing:
        raise RegistryError(f"Model entry missing required fields {missing}: {raw}")
    fetch = raw.get("fetch")
    draft = raw.get("draft")
    return ModelEntry(
        id=raw["id"],
        family=raw["family"],
        provider_id=raw["provider_id"],
        model_name=raw["model_name"],
        location=raw.get("location"),
        fetch=Fetch(repo=fetch.get("repo"), local_path=fetch.get("local_path"))
        if fetch is not None
        else None,
        draft=DraftSpec(repo=draft.get("repo"), local_path=draft.get("local_path"))
        if draft is not None
        else None,
    )


def load_registry(path: Path | None = None) -> Registry:
    registry_file = registry_read_path(path)
    try:
        with open(registry_file, "rb") as f:
            raw = tomllib.load(f)
    except (OSError, ValueError) as exc:  # ValueError: TOMLDecodeError and UnicodeDecodeError
        raise RegistryError(f"cannot read {registry_file}: {exc}") from exc
    return Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )
