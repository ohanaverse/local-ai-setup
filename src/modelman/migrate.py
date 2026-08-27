"""One-time migration from legacy modelman (config.yaml + families/*.yaml)
and, optionally, agent-worktree's config.toml into the new canonical
registry.toml + modelman.toml. See the "One-time migration" section of
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md.

Collision policy: agent-worktree's config.toml is imported first, so its
curated tags/cost/family/location win for any model that also appears in
a modelman family manifest. modelman's own legacy providers/variants only
add new entries or fill in fields it uniquely owns (fetch, model_info,
download state) — they never overwrite tags/cost/family/location.
"""

from __future__ import annotations

import tomllib
from dataclasses import dataclass, field
from pathlib import Path

import yaml

from .manifest import load_manifest
from .registry import AuthConfig, Fetch, ModelEntry, ProviderEntry, Registry
from .state import ModelState, StateStore


@dataclass
class MigrationResult:
    registry: Registry
    state: StateStore
    warnings: list[str] = field(default_factory=list)


def migrate(
    legacy_config_path: Path,
    legacy_family_dir: Path,
    wt_config_path: Path | None = None,
) -> MigrationResult:
    warnings: list[str] = []
    registry = Registry()
    state = StateStore()

    if wt_config_path is not None and wt_config_path.exists():
        _import_wt_config(wt_config_path, registry)
    else:
        warnings.append(
            f"wt config not found at {wt_config_path}; skipped "
            "(fine for a modelman-only install)"
        )

    _import_modelman_providers(legacy_config_path, registry)
    _import_modelman_families(legacy_family_dir, registry, state)

    return MigrationResult(registry=registry, state=state, warnings=warnings)


def _has_provider(registry: Registry, provider_id: str) -> bool:
    return any(p.id == provider_id for p in registry.providers)


def _find_model(registry: Registry, model_id: str) -> ModelEntry | None:
    for m in registry.models:
        if m.id == model_id:
            return m
    return None


def _import_wt_config(wt_config_path: Path, registry: Registry) -> None:
    with open(wt_config_path, "rb") as f:
        raw = tomllib.load(f)
    for p in raw.get("providers", []):
        if _has_provider(registry, p["id"]):
            continue
        auth_raw = p.get("auth", {})
        registry.providers.append(
            ProviderEntry(
                id=p["id"],
                name=p.get("name", p["id"]),
                location=p.get("location"),
                auth=AuthConfig(
                    type=auth_raw.get("type", "none"),
                    secret_ref=auth_raw.get("secret_ref"),
                    base_url=auth_raw.get("base_url"),
                ),
            )
        )
    for m in raw.get("models", []):
        registry.models.append(
            ModelEntry(
                id=m["id"],
                family=m.get("family", m["id"]),
                provider_id=m["provider_id"],
                model_name=m["model_name"],
                location=m.get("location"),
                source=m.get("source"),
                tags=list(m.get("tags", [])),
            )
        )


def _import_modelman_providers(config_path: Path, registry: Registry) -> None:
    if not config_path.exists():
        return
    with open(config_path) as f:
        raw = yaml.safe_load(f) or {}
    for provider_id, cfg in (raw.get("providers") or {}).items():
        if _has_provider(registry, provider_id):
            continue
        registry.providers.append(
            ProviderEntry(
                id=provider_id,
                name=provider_id.title(),
                location="local",
                model_dir=cfg.get("model_dir"),
                auth=AuthConfig(type="none"),
            )
        )


def _import_modelman_families(family_dir: Path, registry: Registry, state: StateStore) -> None:
    if not family_dir.exists():
        return
    for manifest_path in sorted(family_dir.glob("*.yaml")):
        manifest = load_manifest(manifest_path.stem, family_dir=family_dir)
        for variant in manifest.variants:
            model_id = f"{variant['provider']}/{variant['name']}"
            existing = _find_model(registry, model_id)
            if existing is None:
                existing = ModelEntry(
                    id=model_id,
                    family=manifest.family,
                    provider_id=variant["provider"],
                    model_name=variant["name"],
                )
                registry.models.append(existing)
            model_info = variant.get("model_info")
            if model_info:
                existing.model_info = {**existing.model_info, **model_info}
            if variant.get("repo") or variant.get("files") or variant.get("quantizations"):
                existing.fetch = Fetch(
                    repo=variant.get("repo"),
                    files=variant.get("files"),
                    quantizations=variant.get("quantizations"),
                )
            downloaded_info = manifest.downloaded.get(variant["id"])
            if downloaded_info is not None:
                state.set(
                    model_id,
                    ModelState(downloaded=True, disk_path=downloaded_info.get("local_path")),
                )
