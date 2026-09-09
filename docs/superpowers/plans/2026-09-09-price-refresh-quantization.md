# Price Refresh + Quantization Field Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a user-editable `quantization` field and automatic OpenRouter price refresh to `modelman`, with manual-edit timestamp tracking, a TUI background refresh worker, and a root `make install` target.

**Architecture:** Extend `ModelEntry` with `quantization` (round-tripped through TOML, shown/edited in `ModelForm`) and `pricing_updated_at` (set on manual price edits and API refreshes, never leaked into `VariantSpec`/LiteLLM). Introduce a focused `pricing.py` module that fetches OpenRouter prices and maps per-token values to per-million with fail-soft warnings. Wire it to a new CLI subcommand and a daily-gated background worker on app mount. Surface stale-pricing reminders in the exit confirmation dialog. Add a root Makefile aggregate target for installing all components.

**Tech Stack:** Python 3.13, textual, typer, requests, pytest, pytest-asyncio.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `modelman/src/modelman/registry.py` | Add `quantization` and `pricing_updated_at` to `ModelEntry`; serialize/deserialize. |
| `modelman/src/modelman/pricing.py` | OpenRouter fetch, per-token→per-million mapping, cloud-model filter, `RefreshResult`. |
| `modelman/src/modelman/main.py` | New `modelman refresh-prices` subcommand. |
| `modelman/src/modelman/state.py` | Helpers for `price_refresh_last_run` top-level key in `StateStore.extra`. |
| `modelman/src/modelman/providers/base.py` | Add `quantization` to `VariantSpec` TypedDict. |
| `modelman/src/modelman/screens/models.py` | Pass `quantization` through `_variant_to_model_entry` / `model_entry_to_variant`; preserve/update `pricing_updated_at` on add/edit. |
| `modelman/src/modelman/screens/forms.py` | Quantization Input, read-only pricing timestamp label, stale-pricing reminder in `ConfirmExitDialog`. |
| `modelman/src/modelman/app.py` | Daily-gated startup background price refresh worker. |
| `Makefile` (root) | `make install` target. |
| Tests under `modelman/tests/` | Round-trip, mapping, fail-soft, CLI, daily gate, form prefill, exit reminder. |

---

## Task 1: Extend `ModelEntry` schema

**Files:**
- Modify: `modelman/src/modelman/registry.py`
- Test: `modelman/tests/test_registry.py`

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/test_registry.py`:

```python
def test_registry_round_trips_quantization_and_pricing_updated_at(tmp_path):
    path = tmp_path / "registry.toml"
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/x",
                family="x",
                provider_id="ollama",
                model_name="x",
                quantization="Q4_K_M",
                pricing_updated_at="2026-09-09T14:32:00+00:00",
            ),
        ],
    )
    save_registry(registry, path)
    loaded = load_registry(path)
    m = loaded.model("ollama/x")
    assert m.quantization == "Q4_K_M"
    assert m.pricing_updated_at == "2026-09-09T14:32:00+00:00"


def test_registry_absent_quantization_and_pricing_updated_at_are_none(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\n'
        'model_name = "x"\n'
    )
    loaded = load_registry(path)
    assert loaded.model("ollama/x").quantization is None
    assert loaded.model("ollama/x").pricing_updated_at is None
```

Run:
```bash
cd modelman && uv run pytest tests/test_registry.py::test_registry_round_trips_quantization_and_pricing_updated_at tests/test_registry.py::test_registry_absent_quantization_and_pricing_updated_at_are_none -v
```

Expected: FAIL with `TypeError: ModelEntry.__init__() got an unexpected keyword argument 'quantization'`.

- [ ] **Step 2: Add the two fields to `ModelEntry`**

In `modelman/src/modelman/registry.py`, inside the `ModelEntry` dataclass add after `model_info`:

```python
    quantization: str | None = None
    pricing_updated_at: str | None = None
```

- [ ] **Step 3: Serialize the new fields**

In `_model_to_dict` (same file), add to the `d` dict:

```python
        "quantization": m.quantization,
        "pricing_updated_at": m.pricing_updated_at,
```

- [ ] **Step 4: Deserialize the new fields**

In `_parse_model`, add to the `ModelEntry(...)` constructor call:

```python
        quantization=raw.get("quantization"),
        pricing_updated_at=raw.get("pricing_updated_at"),
```

And add both keys to the `unknown_keys` whitelist:

```python
        extra=unknown_keys(
            raw,
            {
                "id",
                "family",
                "provider_id",
                "model_name",
                "location",
                "source",
                "tags",
                "cost",
                "model_info",
                "fetch",
                "draft",
                "usage_tier",
                "quantization",
                "pricing_updated_at",
            },
        ),
```

- [ ] **Step 5: Run tests and commit**

```bash
cd modelman && uv run pytest tests/test_registry.py::test_registry_round_trips_quantization_and_pricing_updated_at tests/test_registry.py::test_registry_absent_quantization_and_pricing_updated_at_are_none -v
```

Expected: PASS.

```bash
cd modelman && uv run ruff check src/modelman/registry.py tests/test_registry.py
git add modelman/src/modelman/registry.py modelman/tests/test_registry.py
git commit -m "feat(registry): add quantization and pricing_updated_at to ModelEntry"
```

---

## Task 2: Create `pricing.py` module

**Files:**
- Create: `modelman/src/modelman/pricing.py`
- Test: `modelman/tests/test_pricing.py`

- [ ] **Step 1: Write the failing tests**

Create `modelman/tests/test_pricing.py`:

```python
"""Tests for OpenRouter price refresh logic."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any
from unittest.mock import MagicMock

import pytest

from modelman.pricing import RefreshResult, refresh_prices
from modelman.registry import AuthConfig, Cost, ModelEntry, ProviderEntry, Registry


class _FakeResponse:
    def __init__(self, payload: dict[str, Any] | None = None, exc: Exception | None = None):
        self._payload = payload
        self._exc = exc

    def raise_for_status(self) -> None:
        if self._exc:
            raise self._exc

    def json(self) -> dict[str, Any]:
        assert self._payload is not None
        return self._payload


def _runner(payload: dict[str, Any] | None = None, exc: Exception | None = None):
    def _run(_url: str, **_kwargs: Any) -> _FakeResponse:
        return _FakeResponse(payload=payload, exc=exc)
    return _run


def _make_registry(*models: ModelEntry) -> Registry:
    providers = [
        ProviderEntry(id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")),
        ProviderEntry(id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")),
        ProviderEntry(id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native")),
    ]
    return Registry(providers=providers, models=list(models))


def test_refresh_prices_maps_per_token_to_per_million():
    registry = _make_registry(
        ModelEntry(
            id="openrouter/gpt-4o",
            family="openai",
            provider_id="openrouter",
            model_name="openai/gpt-4o",
            location="cloud",
        ),
    )
    payload = {
        "data": [
            {
                "id": "openai/gpt-4o",
                "pricing": {
                    "prompt": "0.0000025",
                    "completion": "0.0000100",
                    "input_cache_read": "0.0000010",
                },
            }
        ]
    }
    result = refresh_prices(registry, runner=_runner(payload))

    assert result.error is None
    assert result.updated == 1
    assert result.warnings == []
    cost = registry.model("openrouter/gpt-4o").cost
    assert cost is not None
    assert cost.input_price_per_million == pytest.approx(2.5)
    assert cost.output_price_per_million == pytest.approx(10.0)
    assert cost.cache_price_per_million == pytest.approx(1.0)
    assert registry.model("openrouter/gpt-4o").pricing_updated_at is not None


def test_refresh_prices_skips_models_with_no_openrouter_match():
    registry = _make_registry(
        ModelEntry(
            id="openrouter/missing",
            family="x",
            provider_id="openrouter",
            model_name="vendor/missing",
            location="cloud",
        ),
    )
    payload = {"data": [{"id": "vendor/other", "pricing": {"prompt": "0.000001"}}]}
    result = refresh_prices(registry, runner=_runner(payload))

    assert result.error is None
    assert result.updated == 0
    assert len(result.warnings) == 1
    assert "vendor/missing" in result.warnings[0]
    assert registry.model("openrouter/missing").cost is None


def test_refresh_prices_leaves_prices_untouched_on_api_failure():
    registry = _make_registry(
        ModelEntry(
            id="openrouter/gpt-4o",
            family="x",
            provider_id="openrouter",
            model_name="openai/gpt-4o",
            location="cloud",
            cost=Cost(input_price_per_million=1.0),
        ),
    )
    result = refresh_prices(registry, runner=_runner(exc=RuntimeError("offline")))

    assert result.error == "offline"
    assert result.updated == 0
    assert registry.model("openrouter/gpt-4o").cost.input_price_per_million == 1.0


def test_refresh_prices_matches_cloud_provider_as_well_as_model_location():
    registry = Registry(
        providers=[
            ProviderEntry(id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native")),
        ],
        models=[
            ModelEntry(
                id="claude/opus",
                family="x",
                provider_id="claude",
                model_name="anthropic/claude-opus",
            )
        ],
    )
    payload = {
        "data": [
            {
                "id": "anthropic/claude-opus",
                "pricing": {"prompt": "0.000015", "completion": "0.000075"},
            }
        ]
    }
    result = refresh_prices(registry, runner=_runner(payload))

    assert result.error is None
    assert result.updated == 1
    assert registry.model("claude/opus").cost.output_price_per_million == pytest.approx(75.0)


def test_refresh_prices_missing_cache_price_becomes_none():
    registry = _make_registry(
        ModelEntry(
            id="openrouter/gpt-4o",
            family="x",
            provider_id="openrouter",
            model_name="openai/gpt-4o",
        ),
    )
    payload = {"data": [{"id": "openai/gpt-4o", "pricing": {"prompt": "0.000001", "completion": "0.000002"}}]}
    result = refresh_prices(registry, runner=_runner(payload))

    cost = registry.model("openrouter/gpt-4o").cost
    assert cost.cache_price_per_million is None
```

Run:
```bash
cd modelman && uv run pytest tests/test_pricing.py -v
```

Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.pricing'`.

- [ ] **Step 2: Implement `pricing.py`**

Create `modelman/src/modelman/pricing.py`:

```python
"""OpenRouter price refresh for cloud models."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date, datetime, timezone
from typing import Any, Protocol

import requests

from .registry import Cost, ModelEntry, Registry
from .state import StateStore


OPENROUTER_MODELS_URL = "https://openrouter.ai/api/v1/models"


class _HTTPRunner(Protocol):
    def __call__(self, url: str, **kwargs: Any) -> Any: ...


@dataclass
class RefreshResult:
    updated: int = 0
    warnings: list[str] = field(default_factory=list)
    error: str | None = None


def _default_runner(url: str, **kwargs: Any) -> requests.Response:
    return requests.get(url, timeout=30, **kwargs)


def _per_token_to_per_million(value: Any) -> float | None:
    if value is None:
        return None
    if isinstance(value, (int, float)):
        return float(value) * 1_000_000
    try:
        return float(value) * 1_000_000
    except (TypeError, ValueError):
        return None


def _cost_from_api_entry(entry: dict[str, Any]) -> Cost | None:
    """Build a Cost from an OpenRouter model object.

    Tries ``pricing.<key>`` first, then top-level keys.
    """
    pricing = entry.get("pricing", entry)
    if not isinstance(pricing, dict):
        pricing = {}

    prompt = pricing.get("prompt") if isinstance(pricing, dict) else None
    completion = pricing.get("completion") if isinstance(pricing, dict) else None
    input_cache_read = pricing.get("input_cache_read") if isinstance(pricing, dict) else None

    input_price = _per_token_to_per_million(prompt)
    output_price = _per_token_to_per_million(completion)
    cache_price = _per_token_to_per_million(input_cache_read)

    if input_price is None and output_price is None and cache_price is None:
        return None
    return Cost(
        input_price_per_million=input_price,
        output_price_per_million=output_price,
        cache_price_per_million=cache_price,
    )


def _is_cloud_model(registry: Registry, model: ModelEntry) -> bool:
    if model.provider_id == "openrouter" or model.location == "cloud":
        return True
    try:
        provider = registry.provider(model.provider_id)
    except KeyError:
        return False
    return provider.location == "cloud"


def fetch_openrouter_pricing(runner: _HTTPRunner | None = None) -> list[dict[str, Any]]:
    response = (runner or _default_runner)(OPENROUTER_MODELS_URL)
    response.raise_for_status()
    data = response.json()
    if not isinstance(data, dict):
        raise ValueError("OpenRouter response is not a JSON object")
    models = data.get("data")
    if not isinstance(models, list):
        raise ValueError("OpenRouter response missing 'data' list")
    return models


def refresh_prices(registry: Registry, *, runner: _HTTPRunner | None = None) -> RefreshResult:
    """Fetch current OpenRouter prices and apply them to cloud models in
    ``registry``. Mutates ``registry.models`` in place. On total API
    failure returns ``error`` and makes no mutations. Per-model errors
    are collected as ``warnings`` and do not block other models."""
    candidates = [m for m in registry.models if _is_cloud_model(registry, m)]
    if not candidates:
        return RefreshResult(updated=0, warnings=[], error=None)

    try:
        api_models = fetch_openrouter_pricing(runner=runner)
    except Exception as exc:  # noqa: BLE001
        return RefreshResult(updated=0, warnings=[], error=str(exc))

    api_by_id: dict[str, dict[str, Any]] = {}
    for item in api_models:
        if isinstance(item, dict) and "id" in item:
            api_by_id[item["id"]] = item

    updated = 0
    warnings: list[str] = []
    now = datetime.now(timezone.utc).replace(microsecond=0).isoformat()

    for model in candidates:
        api_entry = api_by_id.get(model.model_name)
        if api_entry is None:
            warnings.append(f"No OpenRouter match for {model.id} ({model.model_name})")
            continue
        try:
            cost = _cost_from_api_entry(api_entry)
        except Exception as exc:  # noqa: BLE001
            warnings.append(f"Could not parse pricing for {model.id}: {exc}")
            continue
        if cost is None:
            warnings.append(f"No pricing data for {model.id}")
            continue
        model.cost = cost
        model.pricing_updated_at = now
        updated += 1

    return RefreshResult(updated=updated, warnings=warnings, error=None)


def should_run_price_refresh(state: StateStore, registry: Registry) -> bool:
    """Return True when today's refresh has not run and at least one
    cloud/openrouter model exists."""
    last = state.extra.get("price_refresh_last_run")
    today = date.today().isoformat()
    if last == today:
        return False
    return any(_is_cloud_model(registry, m) for m in registry.models)
```

- [ ] **Step 3: Run tests**

```bash
cd modelman && uv run pytest tests/test_pricing.py -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd modelman && uv run ruff check src/modelman/pricing.py tests/test_pricing.py
git add modelman/src/modelman/pricing.py modelman/tests/test_pricing.py
git commit -m "feat(pricing): add OpenRouter price refresh module with fail-soft mapping"
```

Note: `should_run_price_refresh` intentionally reads `price_refresh_last_run` directly from `state.extra` here. Task 4 refactors this to use the new helper.

---

## Task 3: Add `modelman refresh-prices` CLI command

**Files:**
- Modify: `modelman/src/modelman/main.py`
- Test: `modelman/tests/commands/test_refresh_prices.py`

- [ ] **Step 1: Write the failing test**

Create `modelman/tests/commands/test_refresh_prices.py`:

```python
"""Tests for `modelman refresh-prices`."""

from __future__ import annotations

from pathlib import Path
from typing import Any
from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from modelman.main import app
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry, save_registry


class _FakeResponse:
    def __init__(self, payload: dict[str, Any] | None = None, exc: Exception | None = None):
        self._payload = payload
        self._exc = exc

    def raise_for_status(self) -> None:
        if self._exc:
            raise self._exc

    def json(self) -> dict[str, Any]:
        assert self._payload is not None
        return self._payload


def _runner(payload: dict[str, Any] | None = None, exc: Exception | None = None):
    def _run(_url: str, **_kwargs: Any) -> _FakeResponse:
        return _FakeResponse(payload=payload, exc=exc)
    return _run


def _seed_registry(tmp_path: Path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    registry = Registry(
        providers=[
            ProviderEntry(
                id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")
            )
        ],
        models=[
            ModelEntry(
                id="openrouter/gpt-4o",
                family="x",
                provider_id="openrouter",
                model_name="openai/gpt-4o",
                location="cloud",
            )
        ],
    )
    save_registry(registry, registry_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return registry_path, state_path


def test_refresh_prices_updates_registry_and_reports(tmp_path, monkeypatch):
    _seed_registry(tmp_path, monkeypatch)
    payload = {
        "data": [
            {"id": "openai/gpt-4o", "pricing": {"prompt": "0.0000025", "completion": "0.00001"}}
        ]
    }
    with patch("modelman.pricing._default_runner", side_effect=_runner(payload)):
        result = CliRunner().invoke(app, ["refresh-prices"])

    assert result.exit_code == 0
    assert "Refreshed prices for 1 model" in result.stdout


def test_refresh_prices_reports_api_error_and_exits(tmp_path, monkeypatch):
    _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.pricing._default_runner", side_effect=_runner(exc=RuntimeError("offline"))):
        result = CliRunner().invoke(app, ["refresh-prices"])

    assert result.exit_code == 1
    assert "offline" in result.output
```

Run:
```bash
cd modelman && uv run pytest tests/commands/test_refresh_prices.py -v
```

Expected: FAIL with `No such command 'refresh-prices'`.

- [ ] **Step 2: Implement the subcommand**

In `modelman/src/modelman/main.py`, add after the `unexpose` command:

```python


@app.command()
def refresh_prices() -> None:
    """Refresh per-token pricing for cloud models from OpenRouter."""
    from .pricing import refresh_prices as run_refresh

    registry = load_registry()
    result = run_refresh(registry)
    if result.error is not None:
        typer.echo(f"error: {result.error}", err=True)
        raise typer.Exit(1)
    try:
        save_registry(registry)
    except OSError as exc:
        typer.echo(f"error: failed to save registry: {exc}", err=True)
        raise typer.Exit(1) from exc
    for warning in result.warnings:
        typer.echo(f"warning: {warning}", err=True)
    typer.echo(f"Refreshed prices for {result.updated} model(s).")
```

- [ ] **Step 3: Run tests**

```bash
cd modelman && uv run pytest tests/commands/test_refresh_prices.py -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd modelman && uv run ruff check src/modelman/main.py tests/commands/test_refresh_prices.py
git add modelman/src/modelman/main.py modelman/tests/commands/test_refresh_prices.py
git commit -m "feat(cli): add modelman refresh-prices command"
```

---

## Task 4: Add `price_refresh_last_run` state helpers

**Files:**
- Modify: `modelman/src/modelman/state.py`
- Test: `modelman/tests/test_state.py`

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/test_state.py`:

```python
def test_price_refresh_last_run_round_trips(tmp_path):
    path = tmp_path / "modelman.toml"
    store = StateStore()
    set_price_refresh_last_run(store, "2026-09-09")
    save_state(store, path)
    loaded = load_state(path)
    assert get_price_refresh_last_run(loaded) == "2026-09-09"


def test_price_refresh_last_run_deletion(tmp_path):
    path = tmp_path / "modelman.toml"
    store = StateStore()
    set_price_refresh_last_run(store, "2026-09-09")
    set_price_refresh_last_run(store, None)
    save_state(store, path)
    loaded = load_state(path)
    assert get_price_refresh_last_run(loaded) is None
    assert "price_refresh_last_run" not in path.read_text()
```

Run:
```bash
cd modelman && uv run pytest tests/test_state.py::test_price_refresh_last_run_round_trips tests/test_state.py::test_price_refresh_last_run_deletion -v
```

Expected: FAIL with `NameError: name 'get_price_refresh_last_run' is not defined`.

- [ ] **Step 2: Implement helpers**

In `modelman/src/modelman/state.py`, add after the imports:

```python
_PRICE_REFRESH_LAST_RUN_KEY = "price_refresh_last_run"


def get_price_refresh_last_run(state: StateStore) -> str | None:
    return state.extra.get(_PRICE_REFRESH_LAST_RUN_KEY)


def set_price_refresh_last_run(state: StateStore, date: str | None) -> None:
    if date is None:
        state.extra.pop(_PRICE_REFRESH_LAST_RUN_KEY, None)
    else:
        state.extra[_PRICE_REFRESH_LAST_RUN_KEY] = date
```

- [ ] **Step 3: Run tests**

```bash
cd modelman && uv run pytest tests/test_state.py::test_price_refresh_last_run_round_trips tests/test_state.py::test_price_refresh_last_run_deletion -v
```

Expected: PASS.

- [ ] **Step 4: Refactor `pricing.py` to use the new helper**

In `modelman/src/modelman/pricing.py`:

1. Update the import:

```python
from .state import StateStore, get_price_refresh_last_run
```

2. Update `should_run_price_refresh`:

```python
    last = get_price_refresh_last_run(state)
```

```bash
cd modelman && uv run pytest tests/test_pricing.py tests/test_app_pricing.py -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd modelman && uv run ruff check src/modelman/state.py src/modelman/pricing.py tests/test_state.py tests/test_pricing.py tests/test_app_pricing.py
git add modelman/src/modelman/state.py modelman/src/modelman/pricing.py modelman/tests/test_state.py modelman/tests/test_pricing.py modelman/tests/test_app_pricing.py
git commit -m "feat(state): add price_refresh_last_run helpers"
```

---

## Task 5: Pass `quantization` through `VariantSpec`

**Files:**
- Modify: `modelman/src/modelman/providers/base.py`
- Modify: `modelman/src/modelman/registry.py` (`model_entry_to_variant`)
- Modify: `modelman/src/modelman/screens/models.py` (`_variant_to_model_entry`)
- Test: `modelman/tests/test_registry.py`

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/test_registry.py`:

```python
def test_model_entry_to_variant_carries_quantization():
    entry = ModelEntry(
        id="llamacpp/q4",
        family="f",
        provider_id="llamacpp",
        model_name="q4.gguf",
        quantization="Q4_K_M",
    )
    spec = model_entry_to_variant(entry)
    assert spec["quantization"] == "Q4_K_M"


def test_variant_dict_quantization_round_trips(tmp_path):
    path = tmp_path / "registry.toml"
    registry = Registry(
        providers=[ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="llamacpp/q4",
                family="f",
                provider_id="llamacpp",
                model_name="q4.gguf",
                quantization="Q4_K_M",
            )
        ],
    )
    save_registry(registry, path)
    loaded = load_registry(path)
    from modelman.screens.models import _variant_to_model_entry

    entry = _variant_to_model_entry(
        model_entry_to_variant(loaded.model("llamacpp/q4")),
        family="f",
        registry=loaded,
    )
    assert entry.quantization == "Q4_K_M"
```

Run:
```bash
cd modelman && uv run pytest tests/test_registry.py::test_model_entry_to_variant_carries_quantization tests/test_registry.py::test_variant_dict_quantization_round_trips -v
```

Expected: FAIL with `KeyError: 'quantization'` or TypedDict error.

- [ ] **Step 2: Add `quantization` to `VariantSpec`**

In `modelman/src/modelman/providers/base.py`, inside `VariantSpec` add:

```python
    quantization: str | None  # free-form quant tag (e.g. Q4_K_M); ignored by providers
```

- [ ] **Step 3: Serialize `quantization` in `model_entry_to_variant`**

In `modelman/src/modelman/registry.py`, in `model_entry_to_variant`, add to the returned dict:

```python
        "quantization": entry.quantization,
```

- [ ] **Step 4: Deserialize `quantization` in `_variant_to_model_entry`**

In `modelman/src/modelman/screens/models.py`, in `_variant_to_model_entry`, add to the `ModelEntry(...)` constructor:

```python
        quantization=variant.get("quantization"),
```

- [ ] **Step 5: Run tests and commit**

```bash
cd modelman && uv run pytest tests/test_registry.py::test_model_entry_to_variant_carries_quantization tests/test_registry.py::test_variant_dict_quantization_round_trips -v
```

Expected: PASS.

```bash
cd modelman && uv run ruff check src/modelman/providers/base.py src/modelman/registry.py src/modelman/screens/models.py tests/test_registry.py
git add modelman/src/modelman/providers/base.py src/modelman/registry.py src/modelman/screens/models.py tests/test_registry.py
git commit -m "feat(variant): pass quantization through VariantSpec"
```

---

## Task 6: Add `quantization` Input and `pricing_updated_at` label to `ModelForm`

**Files:**
- Modify: `modelman/src/modelman/screens/forms.py`
- Test: `modelman/tests/screens/test_forms.py`

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/screens/test_forms.py`:

```python
@pytest.mark.asyncio
async def test_modelform_edit_prefills_quantization():
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "q4.gguf",
        "repo": "foo/bar",
        "files": ["q4.gguf"],
        "quantization": "Q4_K_M",
    }
    form = ModelForm(providers=["llamacpp"], variant=variant)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        assert app.screen.query_one("#quantization", Input).value == "Q4_K_M"


@pytest.mark.asyncio
async def test_modelform_edit_shows_pricing_timestamp_never_when_unset():
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "q4.gguf",
        "repo": "foo/bar",
        "files": ["q4.gguf"],
    }
    form = ModelForm(providers=["llamacpp"], variant=variant, pricing_updated_at=None)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        label = app.screen.query_one("#pricing-timestamp-label", Label)
        assert "never" in str(label.renderable)


@pytest.mark.asyncio
async def test_modelform_edit_shows_pricing_timestamp_when_set():
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "q4.gguf",
        "repo": "foo/bar",
        "files": ["q4.gguf"],
    }
    form = ModelForm(providers=["llamacpp"], variant=variant, pricing_updated_at="2026-09-09T14:32:00+00:00")
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        label = app.screen.query_one("#pricing-timestamp-label", Label)
        assert "2026-09-09" in str(label.renderable)


@pytest.mark.asyncio
async def test_modelform_submit_carries_quantization():
    form = ModelForm(providers=["ollama"], default_provider="ollama", families=["ornith"], family="ornith")
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        _fill_model(app, "test:1b")
        app.screen.query_one("#quantization", Input).value = "Q4_K_M"
        await _submit(app, pilot)
        await pilot.pause()

    assert len(dismissed) == 1
    assert dismissed[0].spec["quantization"] == "Q4_K_M"
```

Run:
```bash
cd modelman && uv run pytest tests/screens/test_forms.py::test_modelform_edit_prefills_quantization tests/screens/test_forms.py::test_modelform_edit_shows_pricing_timestamp_never_when_unset tests/screens/test_forms.py::test_modelform_edit_shows_pricing_timestamp_when_set tests/screens/test_forms.py::test_modelform_submit_carries_quantization -v
```

Expected: FAIL with `NoMatches` for `#quantization` / `#pricing-timestamp-label`.

- [ ] **Step 2: Update `ModelFormResult` and `ModelForm` constructor**

In `modelman/src/modelman/screens/forms.py`, change:

```python
class ModelFormResult(NamedTuple):
    spec: VariantSpec
    family: str
    pricing_updated_at: str | None = None
```

And update `ModelForm.__init__` signature:

```python
    def __init__(
        self,
        providers: list[str],
        variant: VariantSpec | None = None,
        default_provider: str | None = None,
        families: list[str] | None = None,
        family: str | None = None,
        provider_kinds: dict[str, str] | None = None,
        pricing_updated_at: str | None = None,
    ) -> None:
```

Then add near the end of `__init__`:

```python
        self._pricing_updated_at = pricing_updated_at
```

- [ ] **Step 3: Add widgets to compose**

In `ModelForm.compose`, after the location-select block and before per-token pricing, add:

```python
            quantization_prefill = (v.get("quantization") or "") if editing else ""
            yield Label("Quantization:")
            yield Input(
                value=quantization_prefill,
                placeholder="e.g. Q4_K_M",
                id="quantization",
            )
```

And after the subscription-period-select (end of pricing section), add:

```python
            timestamp_text = (
                f"Token pricing updated: {self._pricing_updated_at}"
                if self._pricing_updated_at
                else "Token pricing updated: never"
            )
            yield Label(timestamp_text, id="pricing-timestamp-label")
```

- [ ] **Step 4: Capture quantization and preserve timestamp on submit**

In `_submit` (single-model path), before building the `spec` dict, capture:

```python
        quantization = self.query_one("#quantization", Input).value.strip() or None
```

Add `quantization` to the `spec` dict:

```python
        spec: VariantSpec = {
            ...
            "cost": _cost_to_dict(cost) if cost is not None else None,
            "quantization": quantization,
        }
```

Change the dismiss to:

```python
        self.dismiss(ModelFormResult(spec=spec, family=family, pricing_updated_at=self._pricing_updated_at))
```

In `_submit_dual_model`, similarly:

```python
        quantization = self.query_one("#quantization", Input).value.strip() or None
```

Add it to the dual-model spec and update the dismiss call.

- [ ] **Step 5: Run tests and commit**

```bash
cd modelman && uv run pytest tests/screens/test_forms.py::test_modelform_edit_prefills_quantization tests/screens/test_forms.py::test_modelform_edit_shows_pricing_timestamp_never_when_unset tests/screens/test_forms.py::test_modelform_edit_shows_pricing_timestamp_when_set tests/screens/test_forms.py::test_modelform_submit_carries_quantization -v
```

Expected: PASS.

```bash
cd modelman && uv run ruff check src/modelman/screens/forms.py tests/screens/test_forms.py
git add modelman/src/modelman/screens/forms.py tests/screens/test_forms.py
git commit -m "feat(forms): add quantization input and pricing updated-at label"
```

---

## Task 7: Wire `pricing_updated_at` preservation/update in `ModelScreen`

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/screens/test_models.py` (or create it if it does not exist; it does in this repo):

```python
from modelman.registry import Cost


def test_variant_to_model_entry_preserves_quantization():
    variant = {
        "id": "llamacpp/q4",
        "provider": "llamacpp",
        "name": "q4.gguf",
        "repo": "foo/bar",
        "files": ["q4.gguf"],
        "quantization": "Q4_K_M",
    }
    registry = Registry(providers=[ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none"))])
    entry = _variant_to_model_entry(variant, family="f", registry=registry)
    assert entry.quantization == "Q4_K_M"


def test_variant_to_model_entry_has_no_pricing_updated_at():
    variant = {
        "id": "llamacpp/q4",
        "provider": "llamacpp",
        "name": "q4.gguf",
        "repo": "foo/bar",
        "files": ["q4.gguf"],
    }
    registry = Registry(providers=[ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none"))])
    entry = _variant_to_model_entry(variant, family="f", registry=registry)
    assert entry.pricing_updated_at is None
```

Run:
```bash
cd modelman && uv run pytest tests/screens/test_models.py::test_variant_to_model_entry_preserves_quantization tests/screens/test_models.py::test_variant_to_model_entry_has_no_pricing_updated_at -v
```

Expected: FAIL (test should already pass if Task 5 is done, but assertion on pricing_updated_at may fail if not yet implemented; adjust expectation if needed). If already passing, skip to verification.

- [ ] **Step 2: Implement timestamp helpers in `ModelScreen`**

Add module-level helpers in `modelman/src/modelman/screens/models.py`:

```python
from datetime import datetime, timezone


def _now_iso() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat()


def _cost_changed(old: Cost | None, new: Cost | None) -> bool:
    if old is None and new is None:
        return False
    if old is None or new is None:
        return True
    return (
        old.input_price_per_million != new.input_price_per_million
        or old.cache_price_per_million != new.cache_price_per_million
        or old.output_price_per_million != new.output_price_per_million
        or old.subscription_price != new.subscription_price
        or old.subscription_period != new.subscription_period
    )
```

- [ ] **Step 3: Update `_on_add_model` to set timestamp when cost exists**

In `ModelScreen._on_add_model`, after:

```python
        entry = _variant_to_model_entry(variant, family=result.family, registry=self.registry)
```

Add:

```python
        if entry.cost is not None:
            entry.pricing_updated_at = _now_iso()
```

- [ ] **Step 4: Update `_on_edit_model` to preserve or refresh timestamp**

In `ModelScreen._on_edit_model`, replace the existing overwrite loop with:

```python
    def _on_edit_model(self, result) -> None:
        if result is None:
            return
        updated = result.spec
        old_entry = next((m for m in self.registry.models if m.id == updated["id"]), None)
        if old_entry is None:
            return
        new_entry = _variant_to_model_entry(updated, family=self.family, registry=self.registry)
        if _cost_changed(old_entry.cost, new_entry.cost):
            new_entry.pricing_updated_at = _now_iso()
        else:
            new_entry.pricing_updated_at = old_entry.pricing_updated_at
        for i, m in enumerate(self.registry.models):
            if m.id == updated["id"]:
                self.registry.models[i] = new_entry
                break
        save_registry(self.registry, self.registry_path)
        if result.family != self.family:
            self.queued_moves[updated["id"]] = result.family
        else:
            self.queued_moves.pop(updated["id"], None)
        self._last_provider_used = updated["provider"]
        self.reload()
        self._refresh_pending_bar()
```

Note: this function already contains `save_registry` and related logic; refactor carefully so the new cost/timestamp block sits between constructing `new_entry` and the existing save.

- [ ] **Step 5: Pass `pricing_updated_at` into `ModelForm` from `_action_edit_model`**

In `ModelScreen.action_edit_model`, after constructing `spec = model_entry_to_variant(entry)`, pass the timestamp to the `ModelForm` constructor:

```python
        spec = model_entry_to_variant(entry)
        self.app.push_screen(
            ModelForm(
                providers=self._provider_list(),
                variant=spec,
                families=self._families_list(),
                family=self.queued_moves.get(mid, self.family),
                provider_kinds=self._provider_kinds(),
                pricing_updated_at=entry.pricing_updated_at,
            ),
            self._on_edit_model,
        )
```

- [ ] **Step 6: Run tests and commit**

```bash
cd modelman && uv run pytest tests/screens/test_models.py -v
```

Expected: PASS.

```bash
cd modelman && uv run ruff check src/modelman/screens/models.py tests/screens/test_models.py
git add modelman/src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "feat(models): preserve pricing_updated_at on edit, set on add with cost"
```

---

## Task 8: Add startup price refresh worker to `ModelmanApp`

**Files:**
- Modify: `modelman/src/modelman/app.py`
- Test: `modelman/tests/test_app_pricing.py` (new)

- [ ] **Step 1: Write the failing gate tests**

Create `modelman/tests/test_app_pricing.py`:

```python
"""Tests for the daily-gated startup price refresh worker."""

from datetime import date, timedelta

from modelman.pricing import should_run_price_refresh
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.state import StateStore, set_price_refresh_last_run


def test_should_run_when_cloud_model_present_and_never_run():
    state = StateStore()
    registry = Registry(
        providers=[
            ProviderEntry(id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key"))
        ],
        models=[
            ModelEntry(
                id="openrouter/x",
                family="x",
                provider_id="openrouter",
                model_name="x",
            )
        ],
    )
    assert should_run_price_refresh(state, registry) is True


def test_should_not_run_if_already_run_today():
    state = StateStore()
    set_price_refresh_last_run(state, date.today().isoformat())
    registry = Registry(
        providers=[
            ProviderEntry(id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key"))
        ],
        models=[
            ModelEntry(
                id="openrouter/x",
                family="x",
                provider_id="openrouter",
                model_name="x",
            )
        ],
    )
    assert should_run_price_refresh(state, registry) is False


def test_should_not_run_when_no_cloud_models():
    state = StateStore()
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/x", family="x", provider_id="ollama", model_name="x")],
    )
    assert should_run_price_refresh(state, registry) is False


def test_should_run_after_date_changes():
    state = StateStore()
    yesterday = (date.today() - timedelta(days=1)).isoformat()
    set_price_refresh_last_run(state, yesterday)
    registry = Registry(
        providers=[
            ProviderEntry(id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key"))
        ],
        models=[
            ModelEntry(
                id="openrouter/x",
                family="x",
                provider_id="openrouter",
                model_name="x",
            )
        ],
    )
    assert should_run_price_refresh(state, registry) is True
```

Run:
```bash
cd modelman && uv run pytest tests/test_app_pricing.py -v
```

Expected: PASS (the `should_run_price_refresh` function already exists from Task 2).

- [ ] **Step 2: Wire the worker in `ModelmanApp`**

In `modelman/src/modelman/app.py`, modify `ModelmanApp.__init__`:

```python
        self.downloads = DownloadManager(self)
        self._price_refresh_skipped_or_failed = False
```

Update `on_mount` to dispatch the worker after pushing screens:

```python
    def on_mount(self) -> None:
        configured = _configured_providers()
        self.push_screen(FamilyScreen())
        if self._initial_family is not None:
            try:
                registry = load_registry()
                sync_agent_providers(registry)
            except Exception:
                registry = None
            if registry is None:
                self.run_worker(self._run_price_refresh, exclusive=True, thread=True)
                return
            from .registry import _default_registry_path
            from .state import _default_state_path

            self.push_screen(
                ModelScreen(
                    registry=registry,
                    state=load_state(),
                    family=self._initial_family,
                    registry_path=_default_registry_path(),
                    state_path=_default_state_path(),
                    available_providers=configured,
                )
            )
        self.run_worker(self._run_price_refresh, exclusive=True, thread=True)
```

Add the worker method to `ModelmanApp`:

```python
    def _run_price_refresh(self) -> None:
        """Daily-gated background refresh of OpenRouter prices."""
        from datetime import date

        from .pricing import refresh_prices, should_run_price_refresh
        from .registry import load_registry, save_registry
        from .state import StateStore, load_state, locked_state, set_price_refresh_last_run

        try:
            registry = load_registry()
        except Exception as exc:  # noqa: BLE001
            self.call_from_thread(self.notify, f"Could not load registry for price refresh: {exc}")
            self._price_refresh_skipped_or_failed = True
            return

        try:
            state = load_state()
        except Exception:  # noqa: BLE001
            state = StateStore()

        if not should_run_price_refresh(state, registry):
            return

        result = refresh_prices(registry)
        if result.error is not None:
            self.call_from_thread(self.notify, f"Token price refresh failed: {result.error}")
            self._price_refresh_skipped_or_failed = True
            return

        try:
            save_registry(registry)
        except Exception as exc:  # noqa: BLE001
            self.call_from_thread(
                self.notify, f"Token prices refreshed but could not save registry: {exc}"
            )
            self._price_refresh_skipped_or_failed = True
            return

        try:
            with locked_state() as disk_state:
                set_price_refresh_last_run(disk_state, date.today().isoformat())
        except Exception as exc:  # noqa: BLE001
            self.call_from_thread(
                self.notify, f"Token prices refreshed but could not record refresh date: {exc}"
            )

        for warning in result.warnings:
            self.call_from_thread(self.notify, warning)
        self.call_from_thread(self.notify, f"Token prices refreshed for {result.updated} model(s)")
```

- [ ] **Step 3: Run the gate tests plus app tests**

```bash
cd modelman && uv run pytest tests/test_app_pricing.py tests/screens/test_app_navigation.py tests/test_app_settings.py -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd modelman && uv run ruff check src/modelman/app.py src/modelman/pricing.py tests/test_app_pricing.py
git add modelman/src/modelman/app.py modelman/src/modelman/pricing.py modelman/tests/test_app_pricing.py
git commit -m "feat(app): add daily-gated startup OpenRouter price refresh worker"
```

---

## Task 9: Add stale-pricing reminder to `ConfirmExitDialog`

**Files:**
- Modify: `modelman/src/modelman/screens/forms.py`
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_forms.py`

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/screens/test_forms.py`:

```python
@pytest.mark.asyncio
async def test_confirm_exit_dialog_shows_price_reminder_when_requested():
    from modelman.screens.forms import ConfirmExitDialog

    modal = ConfirmExitDialog(ready=[], deletes=[], exposes=[], moves=[], show_price_reminder=True)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(modal)
        await pilot.pause()
        assert "modelman refresh-prices" in app.screen.query_one("#price-reminder", Label).renderable
```

Run:
```bash
cd modelman && uv run pytest tests/screens/test_forms.py::test_confirm_exit_dialog_shows_price_reminder_when_requested -v
```

Expected: FAIL with `TypeError` for unexpected `show_price_reminder` kwarg.

- [ ] **Step 2: Update `ConfirmExitDialog`**

In `modelman/src/modelman/screens/forms.py`, update the constructor:

```python
    def __init__(
        self,
        ready: list[tuple[str, bool]],
        deletes: list,
        exposes: list[tuple[str, bool]] | None = None,
        moves: list[tuple[str, str]] | None = None,
        show_price_reminder: bool = False,
    ) -> None:
        super().__init__()
        self._ready = ready
        self._deletes = deletes
        self._exposes = exposes or []
        self._moves = moves or []
        self._show_price_reminder = show_price_reminder
```

In `compose`, after the pending-changes list and before the question/buttons,add:

```python
            if self._show_price_reminder:
                yield Label(
                    "token pricing may be stale — run `modelman refresh-prices`.",
                    id="price-reminder",
                )
```

- [ ] **Step 3: Pass the reminder from `ModelScreen.action_back`**

In `modelman/src/modelman/screens/models.py`, update `action_back`:

```python
    def action_back(self) -> None:
        if not self.has_pending_changes():
            self.app.pop_screen()
            return
        from .forms import ConfirmExitDialog

        show_reminder = getattr(self.app, "_price_refresh_skipped_or_failed", False)
        self.app.push_screen(
            ConfirmExitDialog(
                ready=list(self.queued_ready.items()),
                deletes=list(self.queued_deletes.values()),
                exposes=list(self.queued_exposes.items()),
                moves=list(self.queued_moves.items()),
                show_price_reminder=show_reminder,
            ),
            self._on_exit_confirm,
        )
```

- [ ] **Step 4: Run tests**

```bash
cd modelman && uv run pytest tests/screens/test_forms.py::test_confirm_exit_dialog_shows_price_reminder_when_requested -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd modelman && uv run ruff check src/modelman/screens/forms.py src/modelman/screens/models.py tests/screens/test_forms.py
git add modelman/src/modelman/screens/forms.py modelman/src/modelman/screens/models.py tests/screens/test_forms.py
git commit -m "feat(forms): stale-pricing reminder on exit when refresh skipped/failed"
```

---

## Task 10: Add root `make install` target

**Files:**
- Modify: `Makefile` (root)
- Test: `make -n install`

- [ ] **Step 1: Write the failing check**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup && grep -E '^install:' Makefile
```

Expected: no match.

- [ ] **Step 2: Add the target**

Append to the root `Makefile`:

```makefile

install: ## Install all monorepo components (wt + modelman).
	cd wt && make install
	cd modelman && uv sync
```

Ensure the lines are indented with a **tab**, not spaces.

- [ ] **Step 3: Dry-run the target**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup && make -n install
```

Expected: prints `cd wt && make install` and `cd modelman && uv sync` without executing.

- [ ] **Step 4: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup && make lint
git add Makefile
git commit -m "build(make): add install target for wt + modelman"
```

---

## Task 11: Final verification

- [ ] **Step 1: Run modelman check + tests**

```bash
cd modelman && make check && make test
```

Expected: lint, typecheck, and full pytest suite pass.

- [ ] **Step 2: Run root lint**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup && make lint
```

Expected: shell lint and link checks pass.

- [ ] **Step 3: Run monorepo test-all**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup && make test-all
```

Expected: all components pass.

- [ ] **Step 4: Final commit (if any changes)**

```bash
git diff --quiet || git commit -am "fixup: address final review findings"
```

---

## Self-Review

1. **Spec coverage:**
   - `quantization` field → Task 1, 5, 6.
   - `pricing_updated_at` field → Task 1, 6, 7.
   - `modelman refresh-prices` → Task 3.
   - OpenRouter fail-soft mapping → Task 2.
   - Daily-gated startup worker → Task 8.
   - Exit reminder → Task 9.
   - Root `make install` → Task 10.
   - No `sale` field (dropped per spec) → not implemented.

2. **Placeholder scan:** No TBD/TODO or vague steps; every step includes exact file paths and code/commands.

3. **Type consistency:** `ModelFormResult` gains optional `pricing_updated_at`; `ModelForm` constructor matches; `VariantSpec` gains optional `quantization`; `_variant_to_model_entry` and `model_entry_to_variant` both use the same key. `RefreshResult` is defined once in `pricing.py` and used by CLI and worker.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-09-09-price-refresh-quantization.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — Dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
