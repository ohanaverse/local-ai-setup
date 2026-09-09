"""OpenRouter price refresh for cloud models."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import UTC, date, datetime
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
    now = datetime.now(UTC).replace(microsecond=0).isoformat()

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
