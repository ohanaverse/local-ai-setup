from __future__ import annotations

import os
from datetime import UTC, datetime, timedelta
from typing import Any

import typer

from modelman.litellm import (
    LiteLLMConfigError,
    default_litellm_config_path,
    load_litellm_config,
)
from modelman.registry import RegistryError, load_registry
from modelman.usage.db import PostgresSpendStore, database_url
from modelman.usage.errors import UsageError
from modelman.usage.reconcile import reconcile
from modelman.usage.report import format_report
from modelman.usage.wt_state import read_last_launched, read_usage_counts, wt_dir

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


def _run_report(
    *,
    days: int,
    model_filter: str | None,
    family_filter: str | None,
) -> None:
    as_of = datetime.now(UTC)
    start = as_of - timedelta(days=days)
    end = as_of

    wt_state_dir = wt_dir()
    wt_counts = read_usage_counts(wt_state_dir / "usage.jsonl", as_of)
    last_launched = read_last_launched(wt_state_dir / "rotation.state")

    try:
        registry = load_registry()
    except RegistryError as exc:
        raise UsageError(str(exc)) from exc

    config_path = default_litellm_config_path()
    env_url = os.environ.get("MODELMAN_LITELLM_DATABASE_URL")
    if env_url:
        # Env var takes precedence; the config file isn't required.
        dsn = env_url
        litellm_model_list: list[dict[str, Any]] = []
    else:
        try:
            litellm_config = load_litellm_config(config_path)
        except LiteLLMConfigError as exc:
            raise UsageError(str(exc)) from exc
        dsn = database_url(config_path, config=litellm_config)
        litellm_model_list = litellm_config.get("model_list") or []
    spend_store = PostgresSpendStore(dsn)

    result = reconcile(
        wt_counts=wt_counts,
        spend_store=spend_store,
        registry=registry,
        start=start,
        end=end,
        model_filter=model_filter,
        family_filter=family_filter,
        litellm_model_list=litellm_model_list,
    )

    report = format_report(result, start=start, end=end, last_launched=last_launched)
    typer.echo(report)
