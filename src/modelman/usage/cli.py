from __future__ import annotations

import os
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

import typer

from modelman.litellm import default_litellm_config_path
from modelman.registry import Registry, RegistryError, load_registry
from modelman.usage.db import PostgresSpendStore, database_url
from modelman.usage.errors import UsageError
from modelman.usage.reconcile import reconcile
from modelman.usage.report import format_report
from modelman.usage.wt_state import read_last_launched, read_usage_counts

usage_app = typer.Typer(help="Reconcile wt launches with LiteLLM spend data.")


@usage_app.command("report")
def report_cmd(
    days: int = typer.Option(7, "--days", min=1, help="Number of days to report on"),
    model: str | None = typer.Option(None, "--model", help="Filter to a registry model id"),
    family: str | None = typer.Option(None, "--family", help="Filter to a model family"),
) -> None:
    """Print a usage report reconciling wt launches and LiteLLM spend."""
    try:
        _run_report(days=days, model_filter=model, family_filter=family)
    except UsageError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc


def _wt_dir() -> Path:
    env = os.environ.get("MODELMAN_WT_DIR")
    if env:
        return Path(env).expanduser()
    return Path.home() / ".config" / "agent-wt"


def _registry_to_raw(registry: Registry) -> dict[str, Any]:
    """Convert the parsed Registry into the raw dict shape reconcile() expects."""
    return {
        "models": [
            {
                "id": model.id,
                "family": model.family,
                "provider_id": model.provider_id,
                "model_name": model.model_name,
                "tags": list(model.tags),
            }
            for model in registry.models
        ]
    }


def _run_report(
    *,
    days: int,
    model_filter: str | None,
    family_filter: str | None,
) -> None:
    as_of = datetime.now(UTC)
    start = as_of - timedelta(days=days)
    end = as_of

    wt_dir = _wt_dir()
    wt_counts = read_usage_counts(wt_dir / "usage.jsonl", as_of)
    last_launched = read_last_launched(wt_dir / "rotation.state")

    try:
        registry_obj = load_registry()
    except RegistryError as exc:
        raise UsageError(str(exc)) from exc
    registry = _registry_to_raw(registry_obj)

    config_path = default_litellm_config_path()
    dsn = database_url(config_path)
    spend_store = PostgresSpendStore(dsn)

    # Read the LiteLLM model_list for the reverse mapping fallback.
    litellm_model_list = _read_litellm_model_list(config_path)

    result = reconcile(
        wt_counts=wt_counts,
        spend_store=spend_store,
        registry=registry,
        start=start,
        end=end,
        as_of=as_of,
        model_filter=model_filter,
        family_filter=family_filter,
        litellm_model_list=litellm_model_list,
    )

    report = format_report(result, start=start, end=end, last_launched=last_launched)
    typer.echo(report)


def _read_litellm_model_list(config_path: Path) -> list[dict[str, Any]]:
    import yaml

    if not config_path.exists():
        return []
    with open(config_path) as f:
        raw = yaml.safe_load(f) or {}
    return list(raw.get("model_list", []))
