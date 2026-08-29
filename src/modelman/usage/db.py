from __future__ import annotations

import os
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Protocol

import yaml

from modelman.litellm import default_litellm_config_path
from modelman.usage.errors import UsageError


@dataclass
class SpendLogRow:
    request_id: str | None = None
    model_name: str | None = None
    litellm_model: str | None = None
    provider: str | None = None
    spend: float = 0.0
    prompt_tokens: int = 0
    completion_tokens: int = 0
    total_tokens: int = 0
    start_time: datetime | None = None


class SpendStore(Protocol):
    """Abstract source of LiteLLM spend log rows."""

    def query(
        self,
        *,
        start: datetime,
        end: datetime,
        model_names: list[str] | None = None,
    ) -> list[SpendLogRow]: ...


class InMemorySpendStore:
    """Fake spend store for unit tests."""

    def __init__(self, rows: list[SpendLogRow]) -> None:
        self.rows = rows

    def query(
        self,
        *,
        start: datetime,
        end: datetime,
        model_names: list[str] | None = None,
    ) -> list[SpendLogRow]:
        result = [
            row
            for row in self.rows
            if row.start_time is not None and start <= row.start_time <= end
        ]
        if model_names is not None:
            result = [row for row in result if row.model_name in model_names]
        return result


class PostgresSpendStore:
    """Read LiteLLM_SpendLogs from Postgres."""

    def __init__(self, dsn: str) -> None:
        self.dsn = dsn

    def query(
        self,
        *,
        start: datetime,
        end: datetime,
        model_names: list[str] | None = None,
    ) -> list[SpendLogRow]:
        import psycopg2
        from psycopg2.extras import RealDictCursor

        sql = """
            SELECT
                request_id,
                model_group AS model_name,
                model AS litellm_model,
                custom_llm_provider AS provider,
                spend,
                prompt_tokens,
                completion_tokens,
                total_tokens,
                "startTime" AS start_time
            FROM "LiteLLM_SpendLogs"
            WHERE "startTime" >= %s AND "startTime" <= %s
        """
        params: list[Any] = [start, end]
        if model_names:
            # Filter on the real column (model_group), not the SELECT-list
            # alias (model_name) — WHERE is evaluated before SELECT aliases
            # are resolved, so filtering on the alias would raise in Postgres.
            sql += " AND model_group = ANY(%s)"
            params.append(model_names)
        sql += ' ORDER BY "startTime" DESC'

        conn = psycopg2.connect(self.dsn)
        try:
            with conn, conn.cursor(cursor_factory=RealDictCursor) as cur:
                cur.execute(sql, params)
                return [SpendLogRow(**dict(row)) for row in cur.fetchall()]
        finally:
            conn.close()


def _read_litellm_database_url(config_path: Path) -> str | None:
    """Read general_settings.database_url from a LiteLLM config file."""
    if not config_path.exists():
        return None
    with open(config_path) as f:
        raw = yaml.safe_load(f) or {}
    general = raw.get("general_settings") or {}
    return general.get("database_url")


def _reverse_model_index(model_list: list[dict[str, Any]]) -> dict[str, str]:
    """Map litellm_params.model -> model_list.model_name.

    Used to recover the registry model id when LiteLLM_SpendLogs.model_name
    is NULL but the litellm_model field is present. If two entries share the
    same litellm_params.model, the first one in the list wins.
    """
    index: dict[str, str] = {}
    for entry in model_list:
        model_name = entry.get("model_name")
        litellm_model = entry.get("litellm_params", {}).get("model")
        if model_name and litellm_model and litellm_model not in index:
            index[litellm_model] = model_name
    return index


def database_url(config_path: Path | None = None) -> str:
    """Resolve the LiteLLM database URL.

    Precedence:
    1. MODELMAN_LITELLM_DATABASE_URL env var
    2. general_settings.database_url in config.yaml
    """
    env_url = os.environ.get("MODELMAN_LITELLM_DATABASE_URL")
    if env_url:
        return env_url
    path = config_path or default_litellm_config_path()
    file_url = _read_litellm_database_url(path)
    if not file_url:
        raise UsageError(f"Could not find LiteLLM database_url in {path}")
    return file_url
