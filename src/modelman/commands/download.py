"""`modelman download <family>` — download missing variants for a family."""
from __future__ import annotations

from pathlib import Path
from typing import Optional

import questionary
import typer

from ..config import Config, ConfigError, load_config
from ..manifest import ManifestError, get_family_dir, load_manifest, save_manifest
from ..providers.registry import ProviderRegistry
from ._common import console, relpath, render_picker


def _detect_statuses(manifest, config: Config) -> dict[str, bool]:
    statuses: dict[str, bool] = {}
    for v in manifest.variants:
        try:
            provider = ProviderRegistry.get(v["provider"], config.provider(v["provider"]))
        except KeyError:
            statuses[v["id"]] = False
            continue
        statuses[v["id"]] = provider.is_downloaded(v)
    return statuses


def _download_one(variant_id: str, manifest, config: Config) -> Optional[str]:
    variant = manifest.variant_by_id(variant_id)
    if variant is None:
        console.print(f"[red]Unknown variant:[/red] {variant_id}")
        return None

    provider = ProviderRegistry.get(variant["provider"], config.provider(variant["provider"]))
    console.print(f"[cyan]→[/cyan] Downloading [bold]{variant_id}[/bold] via {variant['provider']}…")
    try:
        path = provider.download(variant)
    except Exception as e:
        console.print(f"[red]✗[/red] {variant_id} failed: {e}")
        return None
    manifest.mark_downloaded(variant_id, path)
    console.print(f"[green]✓[/green] {variant_id} → {relpath(path)}")
    return path


def _do_download(family: str, all_missing: bool, yes: bool) -> None:
    """Core download logic — called by the Typer command in main.py."""
    try:
        config = load_config()
    except ConfigError as e:
        console.print(f"[red]Config error:[/red] {e}")
        raise typer.Exit(1)

    try:
        manifest = load_manifest(family)
    except ManifestError as e:
        console.print(f"[red]Manifest error:[/red] {e}")
        raise typer.Exit(1)

    statuses = _detect_statuses(manifest, config)

    console.print()
    console.print(render_picker(manifest, statuses))
    console.print()

    missing_ids = [v["id"] for v in manifest.variants if not statuses.get(v["id"], False)]
    if not missing_ids:
        console.print("[green]All variants already downloaded.[/green]")
        return

    if all_missing or yes:
        selected = missing_ids
    else:
        answer = questionary.checkbox(
            "Select variants to download:",
            choices=[
                questionary.Choice(
                    title=f"{v['id']}  ({v['provider']}: {v['name']})",
                    value=v["id"],
                    checked=True,
                )
                for v in manifest.variants
                if not statuses.get(v["id"], False)
            ],
        ).ask()
        if not answer:
            console.print("[yellow]Cancelled.[/yellow]")
            raise typer.Abort()
        selected = answer

    if not selected:
        console.print("[yellow]No variants selected.[/yellow]")
        return

    for vid in selected:
        _download_one(vid, manifest, config)

    manifest_path = get_family_dir() / f"{family}.yaml"
    save_manifest(manifest, manifest_path)
    console.print(f"\n[green]Updated {relpath(manifest_path)}[/green]")