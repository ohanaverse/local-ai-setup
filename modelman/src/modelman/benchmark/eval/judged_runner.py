"""Single-turn generation + judge scoring for the judged eval categories
(reasoning, planning, code_review, doc_summary).
"""

from __future__ import annotations

from dataclasses import dataclass
from statistics import mean

from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.judge_core import JudgeOutcome, JudgeTransport, judge_row


@dataclass
class ItemResult:
    item_id: str
    response_text: str
    judge: JudgeOutcome
    score_100: float | None


@dataclass
class CategoryRowResult:
    category: str
    items: list[ItemResult]
    score_100: float | None


def _build_judge_prompt(item: Item, category: Category, response_text: str) -> str:
    meta_note = item.meta.get("reference_notes") or item.meta.get("seeded_issue") or ""
    meta_section = (
        f"\n\n## Reference notes (never shown to the model)\n{meta_note}" if meta_note else ""
    )
    return (
        f"{category.rubric_md}\n\n"
        f"## Prompt given to the model\n{item.prompt}\n\n"
        f"## Model's response\n{response_text}{meta_section}\n\n"
        "Respond with strict JSON matching the schema above. No prose outside the JSON object."
    )


def run_judged_category(
    category: Category,
    row_transport: JudgeTransport,
    judge_transport: JudgeTransport,
    *,
    temperature: float,
    judge_temperature: float,
    judge_samples: int,
    judge_max_attempts: int,
) -> CategoryRowResult:
    """One generation call per item, then a judge call scoring it. A row's
    category score is the mean of its scored items' totals — every judged
    category's rubric sums to 100, so this is already a /100 value."""
    assert category.rubric is not None, "run_judged_category is not for the coding category"
    item_results: list[ItemResult] = []
    for item in category.items:
        response_text = row_transport.complete(item.prompt, temperature=temperature)
        judge_prompt = _build_judge_prompt(item, category, response_text)
        outcome = judge_row(
            judge_transport,
            judge_prompt,
            category.rubric,
            temperature=judge_temperature,
            samples=judge_samples,
            max_attempts=judge_max_attempts,
        )
        score = (
            float(outcome.combined.total)
            if outcome.status == "scored" and outcome.combined is not None
            else None
        )
        item_results.append(
            ItemResult(item_id=item.id, response_text=response_text, judge=outcome, score_100=score)
        )

    scored = [r.score_100 for r in item_results if r.score_100 is not None]
    category_score = round(mean(scored), 2) if scored else None
    return CategoryRowResult(category=category.name, items=item_results, score_100=category_score)
