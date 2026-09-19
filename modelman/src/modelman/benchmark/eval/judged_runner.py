"""Single-turn generation + judge scoring for the judged eval categories
(reasoning, planning, code_review, doc_summary).

Generation and judging are deliberately separate functions (design spec
step 4): generate_category runs under benchmark isolation against the
row's own endpoint, judge_category runs after restore_providers() — judge
calls are cloud round-trips that must not hold the exclusively-isolated
local model in RAM while they work, the same ordering rule the agent
benchmark enforces between local generation and cloud judging.
"""

from __future__ import annotations

from dataclasses import dataclass
from statistics import mean

from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.judge_core import JudgeOutcome, JudgeTransport, judge_row

# Cap on the model response embedded in a judge prompt, matching
# benchmark/agent/judge.py's anonymize_diff max_chars: a looping model can
# emit 100KB+ on a reasoning item, and at judge samples=3 that payload is
# sent to the (paid) judge three times — ballooning spend and risking the
# transport's 120s timeout, which turns into judge_fail/N/A for the item.
MAX_JUDGE_RESPONSE_CHARS = 20000


@dataclass
class ItemResult:
    item_id: str
    response_text: str
    # None = generated but never judged (an interrupted judge phase —
    # rejudge-recoverable from the persisted response.txt).
    judge: JudgeOutcome | None
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
    if len(response_text) > MAX_JUDGE_RESPONSE_CHARS:
        # Truncate with an explicit marker (agent/judge.py's convention) so
        # the judge knows the tail was cut, rather than silently scoring a
        # response that appears to end mid-thought.
        response_text = response_text[:MAX_JUDGE_RESPONSE_CHARS] + "\n[TRUNCATED]\n"
    return (
        f"{category.rubric_md}\n\n"
        f"## Prompt given to the model\n{item.prompt}\n\n"
        f"## Model's response\n{response_text}{meta_section}\n\n"
        "Respond with strict JSON matching the schema above. No prose outside the JSON object."
    )


def generate_category(
    category: Category,
    row_transport: JudgeTransport,
    *,
    temperature: float,
) -> CategoryRowResult:
    """Phase 1 (under isolation): one generation call per item, no judging.
    Items come back with judge=None/score_100=None — run_suite judges them
    after restore_providers() and persists the responses first, so an
    interrupted judge phase leaves this row's slow, possibly API-billed
    generation work on disk as rejudge-recoverable UNJUDGED items."""
    assert category.rubric is not None, "generate_category is not for the coding category"
    item_results = [
        ItemResult(
            item_id=item.id,
            response_text=row_transport.complete(item.prompt, temperature=temperature),
            judge=None,
            score_100=None,
        )
        for item in category.items
    ]
    return CategoryRowResult(category=category.name, items=item_results, score_100=None)


def judge_category(
    category: Category,
    result: CategoryRowResult,
    judge_transport: JudgeTransport,
    *,
    judge_temperature: float,
    judge_samples: int,
    judge_max_attempts: int,
) -> None:
    """Phase 2 (after restore): score each generated response in place, then
    set the category's score_100 — the mean of its scored items' totals
    (every judged category's rubric sums to 100, so this is already a /100
    value). Items already carrying a judge outcome are left as-is: judging
    is idempotent, so a partially-judged result can be resumed without
    re-paying for the finished items."""
    assert category.rubric is not None, "judge_category is not for the coding category"
    by_id = {item.id: item for item in category.items}
    for item_result in result.items:
        if item_result.judge is not None:
            continue
        item = by_id[item_result.item_id]
        prompt = _build_judge_prompt(item, category, item_result.response_text)
        outcome = judge_row(
            judge_transport,
            prompt,
            category.rubric,
            temperature=judge_temperature,
            samples=judge_samples,
            max_attempts=judge_max_attempts,
        )
        item_result.judge = outcome
        item_result.score_100 = (
            float(outcome.combined.total)
            if outcome.status == "scored" and outcome.combined is not None
            else None
        )
    scored = [r.score_100 for r in result.items if r.score_100 is not None]
    result.score_100 = round(mean(scored), 2) if scored else None
