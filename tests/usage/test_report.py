from __future__ import annotations

from datetime import UTC, datetime

from modelman.usage.reconcile import ModelUsage, ReconcileResult
from modelman.usage.report import format_report


# A matched model's row must show the date range, launch counts, request/token
# totals with thousands separators, and formatted spend — the core report layout.
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


# wt-only and LiteLLM-only rows need their own labeled sections so a user can
# tell "launched locally but never proxied" apart from "proxied but not via wt".
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


# The "last wt launch" line should be omitted entirely when there's no
# rotation.state data, rather than printing "Last wt launch: None".
def test_format_report_no_last_launched() -> None:
    result = ReconcileResult(matched=[], wt_only=[], litellm_only=[])
    text = format_report(result, start=datetime.now(UTC), end=datetime.now(UTC))
    assert "Last wt launch" not in text
