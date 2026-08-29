from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from typing import Any

from modelman.usage.db import SpendLogRow, SpendStore, _reverse_model_index
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
        if not model_name and row.litellm_model in reverse_index:
            model_name = reverse_index[row.litellm_model]
        if not model_name:
            # Rows without an identifiable model (e.g. failed proxy requests
            # logged with an empty model) have no place in the report.
            continue
        spend_by_model.setdefault(model_name, []).append(row)

    # Registry model lookup (unfiltered; model_filter/family_filter are applied
    # below to every observed id, not just registry-known ones).
    models = _registry_models(registry)

    # Build combined set of observed model ids.
    all_ids = set(models.keys()) | set(wt_counts.keys()) | set(spend_by_model.keys())

    matched: list[ModelUsage] = []
    wt_only: list[ModelUsage] = []
    litellm_only: list[ModelUsage] = []

    for model_id in sorted(all_ids):
        counts = wt_counts.get(model_id)
        spend_group = spend_by_model.get(model_id, [])
        registry_entry = models.get(model_id)

        if registry_entry is not None:
            family = str(registry_entry["family"])
        elif spend_group:
            # No registry metadata; use the provider prefix as family.
            family = model_id.split("/", 1)[0] if "/" in model_id else "unknown"
        else:
            # Unknown to both the registry and LiteLLM; nothing to report.
            continue

        if model_filter and model_id != model_filter:
            continue
        if family_filter and family != family_filter:
            continue

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
