from __future__ import annotations

from datetime import datetime

from modelman.usage.reconcile import ReconcileResult


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
                        f"- {row.registry_model_id} — ${row.spend:.4f} spend, 0 wt launches"
                    )
                lines.append("")
    else:
        lines.append("No usage data found in the requested window.")
        lines.append("")

    if last_launched:
        lines.append("## Last wt launch")
        lines.append("")
        lines.append(f"**{last_launched}**")
        lines.append("")

    return "\n".join(lines)


def _fmt_int(value: int) -> str:
    return f"{value:,}"
