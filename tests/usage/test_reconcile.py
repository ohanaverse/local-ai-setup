from __future__ import annotations

from datetime import datetime

from modelman.usage.db import InMemorySpendStore, SpendLogRow
from modelman.usage.reconcile import reconcile
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


# A model with both wt launches and LiteLLM spend in the window should land
# in `matched` with combined counts — this is the core join the report exists for.
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


# A registry model launched via wt but with no matching LiteLLM spend should
# surface in `wt_only` so a user can spot local-only usage never proxied through LiteLLM.
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


# Spend logged against a registry model with no wt launches should surface in
# `litellm_only`, the mirror case of wt-only usage — e.g. requests made outside wt.
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


# Spend rows outside [start, end] must be excluded from the aggregate so the
# report reflects only the requested date range, not all-time totals.
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


# LiteLLM_SpendLogs can have a NULL model_name; the litellm_model -> registry
# id reverse index must recover the registry id so spend isn't dropped as unidentifiable.
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


# A spend row with no model_name and no reverse-index match (e.g. a failed
# proxy request) has nowhere to attribute — it must be dropped, not crash or misattribute.
def test_reconcile_skips_rows_without_identifiable_model() -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    spend_rows = [
        SpendLogRow(
            model_name="",
            litellm_model="",
            spend=0.5,
            prompt_tokens=1,
            completion_tokens=1,
            total_tokens=2,
            start_time=now,
        ),
    ]
    registry = _make_registry([])
    result = reconcile(
        wt_counts={},
        spend_store=InMemorySpendStore(spend_rows),
        registry=registry,
        start=now,
        end=now,
        as_of=now,
    )
    assert result.matched == []
    assert result.wt_only == []
    assert result.litellm_only == []


# --model should narrow the report to a single registry model id, hiding
# other registry-known models that also have wt/LiteLLM activity in the window.
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


# Regression test: family_filter previously only narrowed the registry lookup
# dict, so a spend-only model excluded from the filtered family still leaked
# into litellm_only via the no-registry-entry fallback branch. The filter must
# apply to every observed model id, not just registry-known ones.
def test_reconcile_family_filter_excludes_spend_only_model() -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    spend_rows = [
        SpendLogRow(
            model_name="openrouter/b",
            litellm_model="b",
            spend=5.0,
            prompt_tokens=1,
            completion_tokens=1,
            total_tokens=2,
            start_time=now,
        ),
    ]
    registry = _make_registry([("openrouter/b", "bar")])
    result = reconcile(
        wt_counts={},
        spend_store=InMemorySpendStore(spend_rows),
        registry=registry,
        start=now,
        end=now,
        as_of=now,
        family_filter="foo",
    )
    assert result.matched == []
    assert result.wt_only == []
    assert result.litellm_only == []
