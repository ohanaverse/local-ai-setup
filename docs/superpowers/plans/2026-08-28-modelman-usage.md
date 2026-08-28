# Usage/Spend Tracking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `modelman usage report` subcommand that reconciles `wt`'s local launch history (`usage.jsonl` + `rotation.state`) with LiteLLM's Postgres spend logs (`LiteLLM_SpendLogs`) and prints a Markdown report.

**Architecture:** A new `src/modelman/usage/` package contains focused adapters for `wt` state (`wt_state.py`), LiteLLM spend logs (`db.py`), join logic (`reconcile.py`), and Markdown output (`report.py`). A thin `cli.py` wires them to `typer` and `src/modelman/main.py` registers the subcommand. The implementation is read-only: it never writes to `usage.jsonl`, `rotation.state`, or the LiteLLM DB.

**Tech Stack:** Python 3.13, `typer`, `psycopg2-binary`, `pyyaml`, `pytest`. Tests use in-memory fakes and fixtures; Postgres integration is optional.

---

## File Structure

### New source files

- `src/modelman/usage/__init__.py` — package marker; re-exports public helpers used by tests.
- `src/modelman/usage/errors.py` — `UsageError` exception type.
- `src/modelman/usage/wt_state.py` — read `usage.jsonl` and `rotation.state`; compute launch counts.
- `src/modelman/usage/db.py` — `SpendStore` Protocol, in-memory fake, Postgres adapter, and LiteLLM config reader.
- `src/modelman/usage/reconcile.py` — join wt launches, LiteLLM spend, and registry models.
- `src/modelman/usage/report.py` — render a Markdown report from reconciled data.
- `src/modelman/usage/cli.py` — `typer` subcommand definition and option handling.

### Modified source files

- `src/modelman/main.py` — register `usage_app`.
- `pyproject.toml` — add `psycopg2-binary` dependency.

### New test files

- `tests/usage/__init__.py`
- `tests/usage/test_errors.py`
- `tests/usage/test_wt_state.py`
- `tests/usage/test_db.py`
- `tests/usage/test_reconcile.py`
- `tests/usage/test_report.py`
- `tests/usage/test_cli.py`

---

## Task 1: Add `psycopg2-binary` dependency

**Files:**
- Modify: `pyproject.toml`

- [ ] **Step 1: Add the dependency**

Add `psycopg2-binary` to the `dependencies` list in `pyproject.toml`:

```toml
dependencies = [
    "typer>=0.15.3",
    "rich>=14.0.0",
    "pyyaml>=6.0.2",
    "huggingface_hub>=1.0.0",
    "questionary>=2.0.0",
    "textual>=0.80.0",
    "tomli-w>=1.2.0",
    "requests>=2.32.0",
    "psycopg2-binary>=2.9.10",
]
```

- [ ] **Step 2: Update the lockfile**

```bash
uv lock
```

Expected: `uv.lock` is updated and `git diff` shows `psycopg2-binary` entries.

- [ ] **Step 3: Verify import**

```bash
uv run python -c "import psycopg2; print(psycopg2.__version__)"
```

Expected: a version string is printed.

- [ ] **Step 4: Commit**

```bash
git add pyproject.toml uv.lock
git commit -m "deps: add psycopg2-binary for usage/spend tracking"
```

---

## Task 2: Define `UsageError`

**Files:**
- Create: `src/modelman/usage/errors.py`
- Test: `tests/usage/test_errors.py`

- [ ] **Step 1: Write the failing test**

Create `tests/usage/test_errors.py`:

```python
from __future__ import annotations

from modelman.usage.errors import UsageError


def test_usage_error_is_runtime_error():
    exc = UsageError("something went wrong")
    assert isinstance(exc, RuntimeError)
    assert str(exc) == "something went wrong"
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
uv run pytest tests/usage/test_errors.py -v
```

Expected: `ModuleNotFoundError: No module named 'modelman.usage'` or `ImportError`.

- [ ] **Step 3: Implement minimal code**

Create `src/modelman/usage/errors.py`:

```python
from __future__ import annotations


class UsageError(RuntimeError):
    """Raised when usage report data cannot be read or reconciled."""
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
uv run pytest tests/usage/test_errors.py -v
```

Expected: one passing test.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/usage/errors.py tests/usage/test_errors.py
git commit -m "feat(usage): add UsageError exception type"
```

---

## Task 3: Read `wt` usage and rotation state

**Files:**
- Create: `src/modelman/usage/wt_state.py`
- Test: `tests/usage/test_wt_state.py`

- [ ] **Step 1: Write the failing tests**

Create `tests/usage/test_wt_state.py`:

```python
from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path

import pytest

from modelman.usage.wt_state import (
    LaunchCounts,
    read_last_launched,
    read_usage_counts,
)


def _write_usage_jsonl(path: Path, records: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w") as f:
        for record in records:
            f.write(json.dumps(record) + "\n")


def test_read_usage_counts_basic(tmp_path: Path) -> None:
    now = datetime.now(timezone.utc)
    records = [
        {"model_id": "ollama/a", "timestamp": now.isoformat()},
        {"model_id": "ollama/a", "timestamp": now.isoformat()},
        {"model_id": "ollama/b", "timestamp": now.isoformat()},
    ]
    usage_path = tmp_path / "usage.jsonl"
    _write_usage_jsonl(usage_path, records)

    counts = read_usage_counts(usage_path, now)
    assert counts["ollama/a"] == LaunchCounts(_1d=2, _7d=2, _30d=2)
    assert counts["ollama/b"] == LaunchCounts(_1d=1, _7d=1, _30d=1)


def test_read_usage_counts_ignores_outside_window(tmp_path: Path) -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    records = [
        {"model_id": "ollama/old", "timestamp": "2026-07-01T12:00:00+00:00"},  # >30d
        {"model_id": "ollama/recent", "timestamp": "2026-08-27T12:00:00+00:00"},
        {"model_id": "ollama/today", "timestamp": "2026-08-28T11:00:00+00:00"},
    ]
    usage_path = tmp_path / "usage.jsonl"
    _write_usage_jsonl(usage_path, records)

    counts = read_usage_counts(usage_path, now)
    assert "ollama/old" not in counts
    assert counts["ollama/recent"] == LaunchCounts(_1d=0, _7d=1, _30d=1)
    assert counts["ollama/today"] == LaunchCounts(_1d=1, _7d=1, _30d=1)


def test_read_usage_counts_missing_file(tmp_path: Path) -> None:
    usage_path = tmp_path / "usage.jsonl"
    counts = read_usage_counts(usage_path, datetime.now(timezone.utc))
    assert counts == {}


def test_read_last_launched_present(tmp_path: Path) -> None:
    rotation_path = tmp_path / "rotation.state"
    rotation_path.write_text("ollama/kimi-k2.6:cloud\n")
    assert read_last_launched(rotation_path) == "ollama/kimi-k2.6:cloud"


def test_read_last_launched_missing(tmp_path: Path) -> None:
    rotation_path = tmp_path / "rotation.state"
    assert read_last_launched(rotation_path) is None


def test_read_last_launched_strips_whitespace(tmp_path: Path) -> None:
    rotation_path = tmp_path / "rotation.state"
    rotation_path.write_text("  ollama/foo  \n")
    assert read_last_launched(rotation_path) == "ollama/foo"
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
uv run pytest tests/usage/test_wt_state.py -v
```

Expected: `ImportError` for `modelman.usage.wt_state`.

- [ ] **Step 3: Implement minimal code**

Create `src/modelman/usage/wt_state.py`:

```python
from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path


@dataclass(frozen=True)
class LaunchCounts:
    _1d: int
    _7d: int
    _30d: int


def read_usage_counts(
    path: Path,
    as_of: datetime,
    windows: tuple[timedelta, timedelta, timedelta] = (
        timedelta(days=1),
        timedelta(days=7),
        timedelta(days=30),
    ),
) -> dict[str, LaunchCounts]:
    """Read usage.jsonl and return per-model launch counts for 1d/7d/30d.

    `as_of` is the reference time; counts include events strictly after
    `as_of - window` and on or before `as_of`.
    """
    raw: dict[str, list[datetime]] = {}
    if not path.exists():
        return {}

    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            record = json.loads(line)
            model_id = record["model_id"]
            ts = datetime.fromisoformat(record["timestamp"])
            if ts.tzinfo is None:
                ts = ts.replace(tzinfo=timezone.utc)
            raw.setdefault(model_id, []).append(ts)

    counts: dict[str, LaunchCounts] = {}
    for model_id, timestamps in raw.items():
        counts[model_id] = LaunchCounts(
            _1d=_count_in_window(timestamps, as_of, windows[0]),
            _7d=_count_in_window(timestamps, as_of, windows[1]),
            _30d=_count_in_window(timestamps, as_of, windows[2]),
        )
    return counts


def _count_in_window(timestamps: list[datetime], as_of: datetime, window: timedelta) -> int:
    cutoff = as_of - window
    return sum(1 for ts in timestamps if cutoff < ts <= as_of)


def read_last_launched(path: Path) -> str | None:
    """Return the single most recently launched model id, if recorded."""
    if not path.exists():
        return None
    return path.read_text().strip() or None
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
uv run pytest tests/usage/test_wt_state.py -v
```

Expected: all six tests pass.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/usage/wt_state.py tests/usage/test_wt_state.py
git commit -m "feat(usage): read wt usage.jsonl and rotation.state"
```

---

## Task 4: Spend store adapter and LiteLLM config reader

**Files:**
- Create: `src/modelman/usage/db.py`
- Test: `tests/usage/test_db.py`

- [ ] **Step 1: Write the failing tests**

Create `tests/usage/test_db.py`:

```python
from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

import pytest
import yaml

from modelman.usage.db import (
    FakeSpendStore,
    InMemorySpendStore,
    SpendLogRow,
    _read_litellm_database_url,
    _reverse_model_index,
)


def test_read_litellm_database_url(tmp_path: Path) -> None:
    config = {
        "model_list": [],
        "general_settings": {"database_url": "postgresql://user@localhost/db"},
    }
    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump(config))

    assert _read_litellm_database_url(config_path) == "postgresql://user@localhost/db"


def test_read_litellm_database_url_missing(tmp_path: Path) -> None:
    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump({"model_list": []}))
    assert _read_litellm_database_url(config_path) is None


def test_read_litellm_database_url_missing_file(tmp_path: Path) -> None:
    assert _read_litellm_database_url(tmp_path / "nope.yaml") is None


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


def test_fake_spend_store_query() -> None:
    now = datetime.now(timezone.utc)
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


def test_fake_spend_store_query_filters_by_model_name() -> None:
    now = datetime.now(timezone.utc)
    rows = [
        SpendLogRow(request_id="r1", model_name="ollama/a", litellm_model="a", provider="p", spend=0.0, prompt_tokens=0, completion_tokens=0, total_tokens=0, start_time=now),
        SpendLogRow(request_id="r2", model_name="ollama/b", litellm_model="b", provider="p", spend=0.0, prompt_tokens=0, completion_tokens=0, total_tokens=0, start_time=now),
    ]
    store = InMemorySpendStore(rows)
    result = store.query(start=now, end=now, model_names=["ollama/a"])
    assert [r.request_id for r in result] == ["r1"]


def test_spend_log_row_default_request_id() -> None:
    row = SpendLogRow(litellm_model="x")
    assert row.request_id is None
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
uv run pytest tests/usage/test_db.py -v
```

Expected: `ImportError` for `modelman.usage.db`.

- [ ] **Step 3: Implement minimal code**

Create `src/modelman/usage/db.py`:

```python
from __future__ import annotations

import os
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Protocol

import yaml


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


# Backward-compatible alias used by early tests.
FakeSpendStore = InMemorySpendStore


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
                model_name,
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
            sql += " AND model_name = ANY(%s)"
            params.append(model_names)
        sql += ' ORDER BY "startTime" DESC'

        with psycopg2.connect(self.dsn) as conn:
            with conn.cursor(cursor_factory=RealDictCursor) as cur:
                cur.execute(sql, params)
                return [SpendLogRow(**dict(row)) for row in cur.fetchall()]


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
    is NULL but the litellm_model field is present.
    """
    index: dict[str, str] = {}
    for entry in model_list:
        model_name = entry.get("model_name")
        litellm_model = entry.get("litellm_params", {}).get("model")
        if model_name and litellm_model:
            index[litellm_model] = model_name
    return index


def default_litellm_config_path() -> Path:
    return Path(
        os.environ.get("MODELMAN_LITELLM_CONFIG", "~/.config/litellm/config.yaml")
    ).expanduser()


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


# Import at the bottom to avoid circular import issues; UsageError lives in errors.py.
from modelman.usage.errors import UsageError  # noqa: E402
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
uv run pytest tests/usage/test_db.py -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/usage/db.py tests/usage/test_db.py
git commit -m "feat(usage): add spend store adapter and LiteLLM DB config reader"
```

---

## Task 5: Reconciliation logic

**Files:**
- Create: `src/modelman/usage/reconcile.py`
- Test: `tests/usage/test_reconcile.py`

- [ ] **Step 1: Write the failing tests**

Create `tests/usage/test_reconcile.py`:

```python
from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path

import pytest

from modelman.usage.db import InMemorySpendStore, SpendLogRow
from modelman.usage.reconcile import ModelUsage, reconcile
from modelman.usage.wt_state import LaunchCounts


def _make_registry(models: list[tuple[str, str]]) -> dict:
    return {
        "models": [
            {
                "id": model_id,
                "family": family,
                "provider_id": model_id.split("/")[0],
                "model_name": model_id.split("/", 1)[1],
                "tags": [],
            }
            for model_id, family in models
        ]
    }


def test_reconcile_matched_model() -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    wt_counts = {"ollama/a": LaunchCounts(_1d=1, _7d=2, _30d=3)}
    spend_rows = [
        SpendLogRow(
            model_name="ollama/a",
            litellm_model="a",
            spend=0.01,
            prompt_tokens=10,
            completion_tokens=20,
            total_tokens=30,
            start_time=now,
        ),
    ]
    registry = _make_registry([("ollama/a", "fam")])
    result = reconcile(
        wt_counts=wt_counts,
        spend_store=InMemorySpendStore(spend_rows),
        registry=registry,
        start=now,
        end=now,
        as_of=now,
    )
    assert len(result.matched) == 1
    usage = result.matched[0]
    assert usage.registry_model_id == "ollama/a"
    assert usage.family == "fam"
    assert usage.wt_launches_1d == 1
    assert usage.wt_launches_7d == 2
    assert usage.wt_launches_30d == 3
    assert usage.litellm_requests == 1
    assert usage.prompt_tokens == 10
    assert usage.completion_tokens == 20
    assert usage.spend == 0.01


def test_reconcile_wt_only() -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    wt_counts = {"ollama/a": LaunchCounts(_1d=1, _7d=1, _30d=1)}
    registry = _make_registry([("ollama/a", "fam")])
    result = reconcile(
        wt_counts=wt_counts,
        spend_store=InMemorySpendStore([]),
        registry=registry,
        start=now,
        end=now,
        as_of=now,
    )
    assert len(result.wt_only) == 1
    assert result.wt_only[0].registry_model_id == "ollama/a"


def test_reconcile_litellm_only() -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    spend_rows = [
        SpendLogRow(
            model_name="ollama/b",
            litellm_model="b",
            spend=0.02,
            prompt_tokens=5,
            completion_tokens=5,
            total_tokens=10,
            start_time=now,
        ),
    ]
    registry = _make_registry([("ollama/b", "fam")])
    result = reconcile(
        wt_counts={},
        spend_store=InMemorySpendStore(spend_rows),
        registry=registry,
        start=now,
        end=now,
        as_of=now,
    )
    assert len(result.litellm_only) == 1
    assert result.litellm_only[0].registry_model_id == "ollama/b"


def test_reconcile_filters_by_time_window() -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    spend_rows = [
        SpendLogRow(
            model_name="ollama/a",
            litellm_model="a",
            spend=0.01,
            prompt_tokens=10,
            completion_tokens=20,
            total_tokens=30,
            start_time=datetime.fromisoformat("2026-08-27T12:00:00+00:00"),
        ),
        SpendLogRow(
            model_name="ollama/a",
            litellm_model="a",
            spend=0.01,
            prompt_tokens=10,
            completion_tokens=20,
            total_tokens=30,
            start_time=datetime.fromisoformat("2026-08-20T12:00:00+00:00"),
        ),
    ]
    registry = _make_registry([("ollama/a", "fam")])
    result = reconcile(
        wt_counts={},
        spend_store=InMemorySpendStore(spend_rows),
        registry=registry,
        start=datetime.fromisoformat("2026-08-25T00:00:00+00:00"),
        end=now,
        as_of=now,
    )
    assert len(result.litellm_only) == 1
    assert result.litellm_only[0].litellm_requests == 1


def test_reconcile_uses_reverse_index_for_null_model_name() -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    spend_rows = [
        SpendLogRow(
            model_name=None,
            litellm_model="ollama_chat/qwen3.8:27b-mlx",
            spend=0.01,
            prompt_tokens=10,
            completion_tokens=20,
            total_tokens=30,
            start_time=now,
        ),
    ]
    registry = _make_registry([("ollama/qwen3.8:27b-mlx", "qwen3.8")])
    model_list = [
        {
            "model_name": "ollama/qwen3.8:27b-mlx",
            "litellm_params": {"model": "ollama_chat/qwen3.8:27b-mlx"},
        }
    ]
    result = reconcile(
        wt_counts={},
        spend_store=InMemorySpendStore(spend_rows),
        registry=registry,
        start=now,
        end=now,
        as_of=now,
        litellm_model_list=model_list,
    )
    assert len(result.litellm_only) == 1
    assert result.litellm_only[0].registry_model_id == "ollama/qwen3.8:27b-mlx"


def test_reconcile_model_filter() -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    wt_counts = {
        "ollama/a": LaunchCounts(_1d=1, _7d=1, _30d=1),
        "ollama/b": LaunchCounts(_1d=1, _7d=1, _30d=1),
    }
    registry = _make_registry([("ollama/a", "fam"), ("ollama/b", "fam")])
    result = reconcile(
        wt_counts=wt_counts,
        spend_store=InMemorySpendStore([]),
        registry=registry,
        start=now,
        end=now,
        as_of=now,
        model_filter="ollama/a",
    )
    assert len(result.wt_only) == 1
    assert result.wt_only[0].registry_model_id == "ollama/a"


def test_reconcile_family_filter() -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    wt_counts = {
        "ollama/a": LaunchCounts(_1d=1, _7d=1, _30d=1),
        "ollama/b": LaunchCounts(_1d=1, _7d=1, _30d=1),
    }
    registry = _make_registry([("ollama/a", "foo"), ("ollama/b", "bar")])
    result = reconcile(
        wt_counts=wt_counts,
        spend_store=InMemorySpendStore([]),
        registry=registry,
        start=now,
        end=now,
        as_of=now,
        family_filter="foo",
    )
    assert len(result.wt_only) == 1
    assert result.wt_only[0].registry_model_id == "ollama/a"
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
uv run pytest tests/usage/test_reconcile.py -v
```

Expected: `ImportError` for `modelman.usage.reconcile`.

- [ ] **Step 3: Implement minimal code**

Create `src/modelman/usage/reconcile.py`:

```python
from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from typing import Any

from modelman.usage.db import InMemorySpendStore, SpendLogRow, SpendStore, _reverse_model_index
from modelman.usage.wt_state import LaunchCounts


@dataclass(frozen=True)
class ModelUsage:
    registry_model_id: str
    family: str
    wt_launches_1d: int
    wt_launches_7d: int
    wt_launches_30d: int
    litellm_requests: int
    prompt_tokens: int
    completion_tokens: int
    spend: float


@dataclass
class ReconcileResult:
    matched: list[ModelUsage]
    wt_only: list[ModelUsage]
    litellm_only: list[ModelUsage]


def reconcile(
    *,
    wt_counts: dict[str, LaunchCounts],
    spend_store: SpendStore,
    registry: dict[str, Any],
    start: datetime,
    end: datetime,
    as_of: datetime,
    model_filter: str | None = None,
    family_filter: str | None = None,
    litellm_model_list: list[dict[str, Any]] | None = None,
) -> ReconcileResult:
    """Join wt launch counts with LiteLLM spend logs over a date window.

    `registry` is the raw registry.toml dict; `registry.models[].id` is the
    canonical registry model id and `registry.models[].family` is its family.
    """
    reverse_index = _reverse_model_index(litellm_model_list or [])
    spend_rows = spend_store.query(start=start, end=end)

    # Aggregate spend by registry model id.
    spend_by_model: dict[str, list[SpendLogRow]] = {}
    for row in spend_rows:
        model_name = row.model_name
        if model_name is None and row.litellm_model in reverse_index:
            model_name = reverse_index[row.litellm_model]
        if model_name is None:
            continue
        spend_by_model.setdefault(model_name, []).append(row)

    # Registry model lookup.
    models = _registry_models(registry)
    if model_filter:
        models = {k: v for k, v in models.items() if k == model_filter}
    if family_filter:
        models = {k: v for k, v in models.items() if v["family"] == family_filter}

    # Build combined set of observed model ids.
    all_ids = set(models.keys()) | set(wt_counts.keys()) | set(spend_by_model.keys())

    matched: list[ModelUsage] = []
    wt_only: list[ModelUsage] = []
    litellm_only: list[ModelUsage] = []

    for model_id in sorted(all_ids):
        counts = wt_counts.get(model_id)
        spend_group = spend_by_model.get(model_id, [])
        registry_entry = models.get(model_id)

        if registry_entry is None and counts is None and spend_group:
            # No registry metadata for this model; use id as family.
            family = model_id.split("/")[0] if "/" in model_id else "unknown"
        elif registry_entry is None:
            continue
        else:
            family = registry_entry["family"] if registry_entry else "unknown"

        usage = ModelUsage(
            registry_model_id=model_id,
            family=family,
            wt_launches_1d=counts._1d if counts else 0,
            wt_launches_7d=counts._7d if counts else 0,
            wt_launches_30d=counts._30d if counts else 0,
            litellm_requests=len(spend_group),
            prompt_tokens=sum(r.prompt_tokens for r in spend_group),
            completion_tokens=sum(r.completion_tokens for r in spend_group),
            spend=sum(r.spend for r in spend_group),
        )

        if counts and spend_group:
            matched.append(usage)
        elif counts:
            wt_only.append(usage)
        elif spend_group:
            litellm_only.append(usage)

    return ReconcileResult(matched=matched, wt_only=wt_only, litellm_only=litellm_only)


def _registry_models(registry: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return {m["id"]: m for m in registry.get("models", [])}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
uv run pytest tests/usage/test_reconcile.py -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/usage/reconcile.py tests/usage/test_reconcile.py
git commit -m "feat(usage): reconcile wt launches and LiteLLM spend"
```

---

## Task 6: Markdown report output

**Files:**
- Create: `src/modelman/usage/report.py`
- Test: `tests/usage/test_report.py`

- [ ] **Step 1: Write the failing tests**

Create `tests/usage/test_report.py`:

```python
from __future__ import annotations

from datetime import datetime, timezone

from modelman.usage.reconcile import ModelUsage, ReconcileResult
from modelman.usage.report import format_report


def test_format_report_includes_header_and_summary() -> None:
    start = datetime.fromisoformat("2026-08-21T00:00:00+00:00")
    end = datetime.fromisoformat("2026-08-28T00:00:00+00:00")
    matched = [
        ModelUsage(
            registry_model_id="ollama/qwen3.8:27b-mlx",
            family="qwen3.8",
            wt_launches_1d=5,
            wt_launches_7d=12,
            wt_launches_30d=34,
            litellm_requests=48,
            prompt_tokens=5432,
            completion_tokens=6602,
            spend=0.0342,
        ),
    ]
    result = ReconcileResult(matched=matched, wt_only=[], litellm_only=[])
    text = format_report(result, start=start, end=end, last_launched="ollama/kimi-k2.6:cloud")

    assert "# Usage Report" in text
    assert "2026-08-21" in text
    assert "2026-08-28" in text
    assert "ollama/qwen3.8:27b-mlx" in text
    assert "5 / 12 / 34" in text
    assert "48" in text
    assert "5,432" in text
    assert "6,602" in text
    assert "$0.0342" in text
    assert "ollama/kimi-k2.6:cloud" in text


def test_format_report_reconciliation_sections() -> None:
    start = datetime.fromisoformat("2026-08-28T00:00:00+00:00")
    end = datetime.fromisoformat("2026-08-28T23:59:59+00:00")
    wt_only = [
        ModelUsage(
            registry_model_id="ollama/gemma4:9b",
            family="gemma4",
            wt_launches_1d=3,
            wt_launches_7d=3,
            wt_launches_30d=3,
            litellm_requests=0,
            prompt_tokens=0,
            completion_tokens=0,
            spend=0.0,
        ),
    ]
    litellm_only = [
        ModelUsage(
            registry_model_id="openrouter/qwen/qwen3.8-flash",
            family="qwen3.8",
            wt_launches_1d=0,
            wt_launches_7d=0,
            wt_launches_30d=0,
            litellm_requests=7,
            prompt_tokens=1200,
            completion_tokens=4500,
            spend=0.12,
        ),
    ]
    result = ReconcileResult(matched=[], wt_only=wt_only, litellm_only=litellm_only)
    text = format_report(result, start=start, end=end, last_launched=None)

    assert "WT-only launches" in text
    assert "ollama/gemma4:9b" in text
    assert "LiteLLM-only spend" in text
    assert "openrouter/qwen/qwen3.8-flash" in text
    assert "$0.1200" in text


def test_format_report_no_last_launched() -> None:
    result = ReconcileResult(matched=[], wt_only=[], litellm_only=[])
    text = format_report(result, start=datetime.now(timezone.utc), end=datetime.now(timezone.utc))
    assert "Last wt launch" not in text
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
uv run pytest tests/usage/test_report.py -v
```

Expected: `ImportError` for `modelman.usage.report`.

- [ ] **Step 3: Implement minimal code**

Create `src/modelman/usage/report.py`:

```python
from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

from modelman.usage.reconcile import ModelUsage, ReconcileResult


def format_report(
    result: ReconcileResult,
    *,
    start: datetime,
    end: datetime,
    last_launched: str | None = None,
) -> str:
    """Render a Markdown usage report."""
    lines: list[str] = []
    lines.append(f"# Usage Report — {start.date()} to {end.date()}")
    lines.append("")

    all_rows = result.matched + result.wt_only + result.litellm_only
    if all_rows:
        lines.append("## Summary")
        lines.append("")
        lines.append(
            "| Family | Model | WT launches (1d/7d/30d) | Requests | "
            "Prompt tokens | Completion tokens | Spend |"
        )
        lines.append("|---|---|---:|---:|---:|---:|---:|")
        for row in sorted(all_rows, key=lambda r: (r.family, r.registry_model_id)):
            lines.append(
                f"| {row.family} | {row.registry_model_id} | "
                f"{row.wt_launches_1d} / {row.wt_launches_7d} / {row.wt_launches_30d} | "
                f"{row.litellm_requests} | "
                f"{_fmt_int(row.prompt_tokens)} | "
                f"{_fmt_int(row.completion_tokens)} | "
                f"${row.spend:.4f} |"
            )
        lines.append("")

        if result.wt_only or result.litellm_only:
            lines.append("## Reconciliation")
            lines.append("")
            if result.wt_only:
                lines.append("### WT-only launches")
                for row in result.wt_only:
                    lines.append(
                        f"- {row.registry_model_id} — "
                        f"{row.wt_launches_7d} launches in the last 7 days, no LiteLLM spend"
                    )
                lines.append("")
            if result.litellm_only:
                lines.append("### LiteLLM-only spend")
                for row in result.litellm_only:
                    lines.append(
                        f"- {row.registry_model_id} — ${row.spend:.4f} spend, "
                        f"0 wt launches"
                    )
                lines.append("")
    else:
        lines.append("No usage data found in the requested window.")
        lines.append("")

    if last_launched:
        lines.append(f"## Last wt launch")
        lines.append("")
        lines.append(f"**{last_launched}**")
        lines.append("")

    return "\n".join(lines)


def _fmt_int(value: int) -> str:
    return f"{value:,}"
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
uv run pytest tests/usage/test_report.py -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/usage/report.py tests/usage/test_report.py
git commit -m "feat(usage): format Markdown usage report"
```

---

## Task 7: `modelman usage` CLI

**Files:**
- Create: `src/modelman/usage/cli.py`
- Test: `tests/usage/test_cli.py`

- [ ] **Step 1: Write the failing tests**

Create `tests/usage/test_cli.py`:

```python
from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import MagicMock

import pytest
import yaml
from typer.testing import CliRunner

from modelman.main import app


runner = CliRunner()


def test_usage_report_command_exists() -> None:
    result = runner.invoke(app, ["usage", "--help"])
    assert result.exit_code == 0
    assert "report" in result.output


def test_usage_report_runs_with_mocked_dependencies(monkeypatch, tmp_path: Path) -> None:
    usage_jsonl = tmp_path / "usage.jsonl"
    usage_jsonl.write_text(
        '{"model_id":"ollama/a","timestamp":"2026-08-28T12:00:00+00:00"}\n'
    )
    rotation_state = tmp_path / "rotation.state"
    rotation_state.write_text("ollama/a\n")

    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[models]\n\n[[models]]\nid = "ollama/a"\nfamily = "fam"\nprovider_id = "ollama"\nmodel_name = "a"\ntags = []\n'
    )

    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump({"model_list": [], "general_settings": {"database_url": "fake"}}))

    monkeypatch.setenv("MODELMAN_WT_DIR", str(tmp_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(config_path))
    monkeypatch.setenv("MODELMAN_LITELLM_DATABASE_URL", "fake")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))

    fake_store = MagicMock()
    fake_store.query.return_value = []
    monkeypatch.setattr("modelman.usage.cli.PostgresSpendStore", lambda dsn: fake_store)

    result = runner.invoke(app, ["usage", "report", "--days", "1"])
    assert result.exit_code == 0, result.output
    assert "Usage Report" in result.output
    assert "ollama/a" in result.output
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
uv run pytest tests/usage/test_cli.py -v
```

Expected: `ImportError` or `AttributeError` for `modelman.usage.cli` / `usage_app`.

- [ ] **Step 3: Implement minimal code**

Create `src/modelman/usage/cli.py`:

```python
from __future__ import annotations

import os
from datetime import datetime, timedelta, timezone
from pathlib import Path

import typer

from modelman.registry import load_registry
from modelman.usage.db import PostgresSpendStore, database_url, default_litellm_config_path
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


def _run_report(
    *,
    days: int,
    model_filter: str | None,
    family_filter: str | None,
) -> None:
    as_of = datetime.now(timezone.utc)
    start = as_of - timedelta(days=days)
    end = as_of

    wt_dir = _wt_dir()
    wt_counts = read_usage_counts(wt_dir / "usage.jsonl", as_of)
    last_launched = read_last_launched(wt_dir / "rotation.state")

    registry = load_registry()

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


def _read_litellm_model_list(config_path: Path) -> list[dict]:
    import yaml

    if not config_path.exists():
        return []
    with open(config_path) as f:
        raw = yaml.safe_load(f) or {}
    return raw.get("model_list", [])
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
uv run pytest tests/usage/test_cli.py -v
```

Expected: CLI help test passes; report test may need main.py wiring first.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/usage/cli.py tests/usage/test_cli.py
git commit -m "feat(usage): add modelman usage report CLI"
```

---

## Task 8: Wire `usage_app` into `src/modelman/main.py`

**Files:**
- Modify: `src/modelman/main.py`

- [ ] **Step 1: Add the import and registration**

Modify `src/modelman/main.py`:

```python
from .benchmark.cli import benchmark_app
from .usage.cli import usage_app
```

And add:

```python
app.add_typer(benchmark_app, name="benchmark")
app.add_typer(usage_app, name="usage")
```

- [ ] **Step 2: Run the CLI test**

```bash
uv run pytest tests/usage/test_cli.py -v
```

Expected: both tests pass.

- [ ] **Step 3: Commit**

```bash
git add src/modelman/main.py
git commit -m "feat(usage): register usage subcommand in main CLI"
```

---

## Task 9: Add `__init__.py` for the usage package

**Files:**
- Create: `src/modelman/usage/__init__.py`

- [ ] **Step 1: Create the package marker**

Create `src/modelman/usage/__init__.py`:

```python
from __future__ import annotations

from modelman.usage.cli import usage_app
from modelman.usage.db import InMemorySpendStore, PostgresSpendStore, SpendLogRow
from modelman.usage.errors import UsageError
from modelman.usage.reconcile import ModelUsage, ReconcileResult, reconcile
from modelman.usage.report import format_report
from modelman.usage.wt_state import LaunchCounts, read_last_launched, read_usage_counts

__all__ = [
    "LaunchCounts",
    "ModelUsage",
    "PostgresSpendStore",
    "InMemorySpendStore",
    "ReconcileResult",
    "SpendLogRow",
    "UsageError",
    "format_report",
    "read_last_launched",
    "read_usage_counts",
    "reconcile",
    "usage_app",
]
```

- [ ] **Step 2: Verify imports**

```bash
uv run python -c "from modelman.usage import usage_app, reconcile; print('ok')"
```

Expected: prints `ok`.

- [ ] **Step 3: Commit**

```bash
git add src/modelman/usage/__init__.py
git commit -m "feat(usage): export public API from usage package"
```

---

## Final Verification

- [ ] **Step 1: Run the full usage test suite**

```bash
uv run pytest tests/usage -v
```

Expected: all tests pass.

- [ ] **Step 2: Run the full modelman test suite**

```bash
uv run pytest -q
```

Expected: existing tests continue to pass; new tests included.

- [ ] **Step 3: Run lint and type checks**

```bash
make check
```

Expected: `ruff check` and `mypy src/` both pass.

- [ ] **Step 4: Manual smoke test**

```bash
uv run python -m modelman usage --help
uv run python -m modelman usage report --help
```

Expected: help text is printed for both.

- [ ] **Step 5: Build**

```bash
go build ./...
```

Wait, wrong project. For `modelman`, run:

```bash
uv build
```

Expected: wheel builds cleanly.

- [ ] **Step 6: Update tracker and docs**

After merging the implementation PR, update:
- `agent-worktree/docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md` to mark Sub-project 3 implementation complete.
- `modelman/docs/ROADMAP.md` if it tracks sub-project status.

---

## Self-Review Notes

**Spec coverage check:**

| Spec requirement | Plan task |
|---|---|
| Add `modelman usage` subcommand | Task 7 + Task 8 |
| Read `wt` `usage.jsonl` + `rotation.state` | Task 3 |
| Read LiteLLM DB URL from `config.yaml` | Task 4 |
| Query `LiteLLM_SpendLogs` via Postgres | Task 4 |
| Reverse-map `litellm_model` when `model_name` is NULL | Task 4 + Task 5 |
| Per-model/family spend + token counts | Task 5 |
| Reconciliation sections (matched, wt-only, litellm-only) | Task 5 + Task 6 |
| Show last launched model | Task 6 |
| Read-only (no writes to wt/LiteLLM state) | All tasks (no write functions) |
| Tests with in-memory fakes and optional Postgres integration | Task 4, 5, 6, 7 |

**Placeholder scan:** No `TBD`, `TODO`, `implement later`, `add validation`, or `Similar to Task` patterns found. Each step contains code or exact commands.

**Type consistency check:**
- `SpendLogRow` dataclass fields match across `db.py`, `reconcile.py`, tests.
- `ModelUsage` fields match across `reconcile.py`, `report.py`, tests.
- `LaunchCounts` fields match across `wt_state.py`, `reconcile.py`, tests.

**Potential integration issue:** The CLI test mocks `PostgresSpendStore`; an actual run requires Postgres. The plan does not add an integration test by default to keep CI green. A future task could add `tests/usage/integration/` guarded by `MODELMAN_USAGE_INTEGRATION=1`.

**Dependency note:** `psycopg2-binary` is added as a required dependency. If that causes platform issues, move it to an optional dependency group named `usage` and update import in `PostgresSpendStore` to raise `UsageError` with a clear message when missing.
