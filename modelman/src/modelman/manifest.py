"""Family manifest: list of variants and which are downloaded.

As of Phase 2 (see docs/superpowers/specs/2026-08-27-shared-model-
registry-design.md), families/*.yaml and MODELMAN_FAMILY_DIR are read
only by `modelman migrate` (src/modelman/main.py, migrate.py) — every
TUI code path was migrated onto registry.toml/modelman.toml
(registry.py/state.py) by Phase 2 PR 2/PR 3. Do not add a new caller
of load_manifest()/get_family_dir() outside the migrate path.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml

from .providers.base import VariantSpec


def get_family_dir() -> Path:
    """Compute the family dir lazily so env overrides work in tests."""
    return Path(os.environ.get("MODELMAN_FAMILY_DIR", "~/.config/local-ai/families")).expanduser()


class ManifestError(Exception):
    """Raised when a family manifest is missing or malformed."""


@dataclass
class FamilyManifest:
    family: str
    display_name: str = ""
    variants: list[VariantSpec] = field(default_factory=list)
    downloaded: dict[str, dict[str, Any]] = field(default_factory=dict)


def _coerce_variant(raw: dict) -> VariantSpec:
    """Normalize a YAML-loaded variant dict into a VariantSpec TypedDict."""
    required = {"id", "provider"}
    missing = required - set(raw.keys())
    if missing:
        raise ManifestError(f"Variant missing required fields: {missing}. Got: {raw}")

    # Derive `name` if not explicitly provided.
    name = raw.get("name")
    if not name:
        if raw.get("files"):
            name = raw["files"][0]
        elif raw.get("repo"):
            name = raw["repo"].split("/")[-1]
        else:
            name = raw["id"]

    return VariantSpec(
        id=raw["id"],
        provider=raw["provider"],
        name=name,
        repo=raw.get("repo"),
        files=raw.get("files"),
        quantizations=raw.get("quantizations"),
        model_info=raw.get("model_info"),
    )


def load_manifest(family: str, family_dir: Path | None = None) -> FamilyManifest:
    base = family_dir or get_family_dir()
    path = base / f"{family}.yaml"
    if not path.exists():
        raise ManifestError(f"No family `{family}` at {path}. Create the manifest first.")

    with open(path) as f:
        raw = yaml.safe_load(f) or {}

    if "family" not in raw:
        raise ManifestError(f"Manifest at {path} missing required `family` field")

    variants = [_coerce_variant(v) for v in raw.get("variants", [])]

    return FamilyManifest(
        family=raw["family"],
        display_name=raw.get("display_name", raw["family"]),
        variants=variants,
        downloaded=raw.get("downloaded", {}),
    )


def save_manifest(manifest: FamilyManifest, path: Path) -> None:
    """Write the manifest to disk in canonical YAML form."""
    payload = {
        "family": manifest.family,
        "display_name": manifest.display_name,
        "variants": [dict(v) for v in manifest.variants],
        "downloaded": manifest.downloaded,
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w") as f:
        yaml.safe_dump(payload, f, sort_keys=False, default_flow_style=False)
