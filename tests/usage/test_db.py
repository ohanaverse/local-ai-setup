from __future__ import annotations

from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import MagicMock

import psycopg2
import pytest

from modelman.usage.db import (
    InMemorySpendStore,
    PostgresSpendStore,
    SpendLogRow,
    database_url,
)


def _mock_psycopg2(monkeypatch) -> tuple[MagicMock, MagicMock]:
    """Return a patched (connection, cursor) pair for PostgresSpendStore tests."""
    mock_cursor = MagicMock()
    mock_cursor.__enter__.return_value = mock_cursor
    mock_cursor.__exit__.return_value = False
    mock_cursor.fetchall.return_value = []

    mock_conn = MagicMock()
    mock_conn.__enter__.return_value = mock_conn
    mock_conn.__exit__.return_value = False
    mock_conn.cursor.return_value = mock_cursor

    monkeypatch.setattr(psycopg2, "connect", lambda dsn: mock_conn)
    return mock_conn, mock_cursor


def test_in_memory_spend_store_query() -> None:
    now = datetime.now(UTC)
    rows = [
        SpendLogRow(
            request_id="r1",
            model_name="ollama/qwen3.8:27b-mlx",
            litellm_model="ollama_chat/qwen3.8:27b-mlx",
            provider="ollama_chat",
            spend=0.01,
            prompt_tokens=10,
            completion_tokens=20,
            total_tokens=30,
            start_time=now,
        ),
    ]
    store = InMemorySpendStore(rows)
    result = store.query(start=now, end=now)
    assert len(result) == 1
    assert result[0].request_id == "r1"


# The fake store's model_names filter must behave like PostgresSpendStore's
# so tests using InMemorySpendStore accurately exercise filtered-query behavior.
def test_in_memory_spend_store_query_filters_by_model_name() -> None:
    now = datetime.now(UTC)
    rows = [
        SpendLogRow(
            request_id="r1",
            model_name="ollama/a",
            litellm_model="a",
            provider="p",
            spend=0.0,
            prompt_tokens=0,
            completion_tokens=0,
            total_tokens=0,
            start_time=now,
        ),
        SpendLogRow(
            request_id="r2",
            model_name="ollama/b",
            litellm_model="b",
            provider="p",
            spend=0.0,
            prompt_tokens=0,
            completion_tokens=0,
            total_tokens=0,
            start_time=now,
        ),
    ]
    store = InMemorySpendStore(rows)
    result = store.query(start=now, end=now, model_names=["ollama/a"])
    assert [r.request_id for r in result] == ["r1"]


# SpendLogRow's fields must all have safe defaults so a partially-populated
# row (e.g. from a code path that doesn't set request_id) doesn't require a TypeError.
def test_spend_log_row_default_request_id() -> None:
    row = SpendLogRow(litellm_model="x")
    assert row.request_id is None


# The database_url env var is optional; the config file's general_settings
# block is the fallback source, so reading it correctly is required for CLI startup.
def test_database_url_from_config(monkeypatch) -> None:
    monkeypatch.delenv("MODELMAN_LITELLM_DATABASE_URL", raising=False)
    config = {
        "model_list": [],
        "general_settings": {"database_url": "postgresql://user@localhost/db"},
    }
    assert database_url(config=config) == "postgresql://user@localhost/db"


# A config with no general_settings.database_url key must raise UsageError
# so the CLI can report the problem clearly.
def test_database_url_missing_in_config(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.delenv("MODELMAN_LITELLM_DATABASE_URL", raising=False)
    from modelman.usage.errors import UsageError

    with pytest.raises(UsageError):
        database_url(config={"model_list": []})


# Regression test: database_url() without a pre-loaded config must raise
# UsageError (the CLI's caught type) for a missing config file, not leak
# LiteLLMConfigError — the config=None path is a supported API, not dead code.
def test_database_url_missing_file_raises_usage_error(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.delenv("MODELMAN_LITELLM_DATABASE_URL", raising=False)
    from modelman.usage.errors import UsageError

    with pytest.raises(UsageError):
        database_url(config_path=tmp_path / "missing-config.yaml")


# Regression test: model_names filtering previously referenced `model_name`,
# a SELECT-list alias, in the WHERE clause — invalid SQL evaluation order that
# Postgres rejects. The query must filter on the real `model_group` column instead.
def test_postgres_spend_store_query_filters_on_real_column(monkeypatch) -> None:
    mock_conn, mock_cursor = _mock_psycopg2(monkeypatch)

    now = datetime.now(UTC)
    store = PostgresSpendStore("fake-dsn")
    store.query(start=now, end=now, model_names=["ollama/a"])

    sql = mock_cursor.execute.call_args[0][0]
    assert "model_group = ANY(%s)" in sql
    assert "model_name = ANY" not in sql


# Regression test: the connection must be explicitly closed — psycopg2's
# connection context manager only commits/rolls back the transaction.
def test_postgres_spend_store_query_closes_connection(monkeypatch) -> None:
    mock_conn, _mock_cursor = _mock_psycopg2(monkeypatch)

    now = datetime.now(UTC)
    store = PostgresSpendStore("fake-dsn")
    store.query(start=now, end=now)

    mock_conn.close.assert_called_once()
