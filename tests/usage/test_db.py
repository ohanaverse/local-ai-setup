from __future__ import annotations

from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import MagicMock

import psycopg2
import yaml

from modelman.usage.db import (
    InMemorySpendStore,
    PostgresSpendStore,
    SpendLogRow,
    _read_litellm_database_url,
    _reverse_model_index,
)


# The database_url env var is optional; the config file's general_settings
# block is the fallback source, so reading it correctly is required for CLI startup.
def test_read_litellm_database_url(tmp_path: Path) -> None:
    config = {
        "model_list": [],
        "general_settings": {"database_url": "postgresql://user@localhost/db"},
    }
    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump(config))

    assert _read_litellm_database_url(config_path) == "postgresql://user@localhost/db"


# A config with no general_settings.database_url key must return None rather
# than raise, so the caller can fall back to the env var or a clear UsageError.
def test_read_litellm_database_url_missing(tmp_path: Path) -> None:
    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump({"model_list": []}))
    assert _read_litellm_database_url(config_path) is None


# A nonexistent config path (e.g. LiteLLM never configured) must return None
# instead of raising FileNotFoundError, keeping this a soft lookup.
def test_read_litellm_database_url_missing_file(tmp_path: Path) -> None:
    assert _read_litellm_database_url(tmp_path / "nope.yaml") is None


# The reverse index must map each model_list entry's litellm_params.model back
# to its registry model_name, since that's how NULL-model_name spend rows get resolved.
def test_reverse_model_index() -> None:
    model_list = [
        {
            "model_name": "ollama/qwen3.8:27b-mlx",
            "litellm_params": {"model": "ollama_chat/qwen3.8:27b-mlx"},
        },
        {
            "model_name": "openrouter/qwen/qwen3.8-27b",
            "litellm_params": {"model": "openrouter/qwen/qwen3.8-27b"},
        },
        {
            "model_name": "omlx/Qwen3.8-27B-4bit",
            "litellm_params": {"model": "openai/Qwen3.8-27B-4bit"},
        },
    ]
    index = _reverse_model_index(model_list)
    assert index["ollama_chat/qwen3.8:27b-mlx"] == "ollama/qwen3.8:27b-mlx"
    assert index["openrouter/qwen/qwen3.8-27b"] == "openrouter/qwen/qwen3.8-27b"
    assert index["openai/Qwen3.8-27B-4bit"] == "omlx/Qwen3.8-27B-4bit"


# Two model_list entries can point at the same litellm_params.model (e.g. a
# migration or alias); the first entry must win deterministically rather than
# silently letting whichever entry is last in config.yaml overwrite it.
def test_reverse_model_index_first_entry_wins_on_duplicate() -> None:
    model_list = [
        {
            "model_name": "ollama/a",
            "litellm_params": {"model": "shared/target"},
        },
        {
            "model_name": "ollama/b",
            "litellm_params": {"model": "shared/target"},
        },
    ]
    index = _reverse_model_index(model_list)
    assert index["shared/target"] == "ollama/a"


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


# Regression test: model_names filtering previously referenced `model_name`,
# a SELECT-list alias, in the WHERE clause — invalid SQL evaluation order that
# Postgres rejects. The query must filter on the real `model_group` column instead.
def test_postgres_spend_store_query_filters_on_real_column(monkeypatch) -> None:
    mock_cursor = MagicMock()
    mock_cursor.__enter__.return_value = mock_cursor
    mock_cursor.__exit__.return_value = False
    mock_cursor.fetchall.return_value = []

    mock_conn = MagicMock()
    mock_conn.__enter__.return_value = mock_conn
    mock_conn.__exit__.return_value = False
    mock_conn.cursor.return_value = mock_cursor

    monkeypatch.setattr(psycopg2, "connect", lambda dsn: mock_conn)

    now = datetime.now(UTC)
    store = PostgresSpendStore("fake-dsn")
    store.query(start=now, end=now, model_names=["ollama/a"])

    sql = mock_cursor.execute.call_args[0][0]
    assert "model_group = ANY(%s)" in sql
    assert "model_name = ANY" not in sql


# Regression test: psycopg2's connection context manager only commits/rolls
# back the transaction on exit, it does NOT close the connection — the query
# must explicitly close it or every report run leaks a Postgres connection.
def test_postgres_spend_store_query_closes_connection(monkeypatch) -> None:
    mock_cursor = MagicMock()
    mock_cursor.__enter__.return_value = mock_cursor
    mock_cursor.__exit__.return_value = False
    mock_cursor.fetchall.return_value = []

    mock_conn = MagicMock()
    mock_conn.__enter__.return_value = mock_conn
    mock_conn.__exit__.return_value = False
    mock_conn.cursor.return_value = mock_cursor

    monkeypatch.setattr(psycopg2, "connect", lambda dsn: mock_conn)

    now = datetime.now(UTC)
    store = PostgresSpendStore("fake-dsn")
    store.query(start=now, end=now)

    mock_conn.close.assert_called_once()
