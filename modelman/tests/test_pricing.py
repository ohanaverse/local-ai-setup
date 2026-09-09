"""Tests for OpenRouter price refresh logic."""

from __future__ import annotations

from typing import Any

import pytest

from modelman.pricing import refresh_prices
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
    refresh_prices(registry, runner=_runner(payload))

    cost = registry.model("openrouter/gpt-4o").cost
    assert cost.cache_price_per_million is None
