"""Tests for modelman.benchmark.eval.judged_runner — one generation call per
item, scored by the shared judge_core mechanics, aggregated to a /100
category score.
"""

import json

from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.judged_runner import run_judged_category
from modelman.benchmark.judge_core import Rubric

RUBRIC = Rubric(dimensions={"bug_detection": 60, "actionability": 40})
CATEGORY = Category(
    name="mini_review",
    path=None,
    items=[
        Item(id="i1", prompt="review this", meta={"seeded_issue": "off by one"}),
        Item(id="i2", prompt="review that", meta={}),
    ],
    rubric=RUBRIC,
    rubric_md="score bug_detection (60) and actionability (40)",
)


class _FakeRowTransport:
    def __init__(self):
        self.prompts_seen: list[str] = []

    def complete(self, prompt: str, *, temperature: float) -> str:
        self.prompts_seen.append(prompt)
        return "There is an off-by-one error in the loop bound."


class _FakeJudgeTransport:
    def __init__(self, totals: list[int]):
        self.totals = list(totals)

    def complete(self, prompt: str, *, temperature: float) -> str:
        if not self.totals:
            return "not json"
        total = self.totals.pop(0)
        bug_det = min(total, 60)
        action = min(total - bug_det, 40)
        return json.dumps(
            {"scores": {"bug_detection": bug_det, "actionability": action}, "total": total}
        )


def test_run_judged_category_sends_item_prompt_verbatim_to_row_transport():
    # The row transport must receive exactly each item's own prompt text,
    # with no judge/rubric wrapping mixed in — that wrapping only belongs in
    # the separate judge prompt built after generation.
    row_transport = _FakeRowTransport()
    run_judged_category(
        CATEGORY,
        row_transport,
        _FakeJudgeTransport([60, 40]),
        temperature=0.0,
        judge_temperature=0.0,
        judge_samples=1,
        judge_max_attempts=1,
    )
    assert row_transport.prompts_seen == ["review this", "review that"]


def test_run_judged_category_scores_as_mean_of_item_totals():
    # A category's row-level score_100 is the mean of its items' judge
    # totals (already /100 since every rubric sums to 100) — this pins the
    # aggregation formula so a future change to it is deliberate, not
    # accidental.
    result = run_judged_category(
        CATEGORY,
        _FakeRowTransport(),
        _FakeJudgeTransport([60, 40]),
        temperature=0.0,
        judge_temperature=0.0,
        judge_samples=1,
        judge_max_attempts=1,
    )
    assert result.score_100 == 50.0
    assert len(result.items) == 2


def test_run_judged_category_ignores_judge_fail_items_in_the_mean():
    # An item whose judge call fails to parse must not drag the category
    # mean down to zero, and must not silently vanish either — it's excluded
    # from the mean but still recorded as a judge_fail item, so a partial
    # judge outage degrades the score gracefully instead of corrupting it.
    result = run_judged_category(
        CATEGORY,
        _FakeRowTransport(),
        _FakeJudgeTransport([80]),  # only one valid reply; item 2's judge call fails to parse
        temperature=0.0,
        judge_temperature=0.0,
        judge_samples=1,
        judge_max_attempts=1,
    )
    assert result.items[1].judge.status == "judge_fail"
    assert result.items[1].score_100 is None
    assert result.score_100 == 80.0


def test_build_judge_prompt_truncates_oversized_responses():
    # A looping model can emit 100KB+ on a single item; at judge samples=3
    # that payload is sent to the paid judge three times, ballooning spend
    # and risking the transport's 120s timeout (which would score the item
    # judge_fail/N/A instead). The judge prompt must cap the embedded
    # response, with an explicit marker so the judge knows the tail was
    # cut — the agent benchmark's anonymize_diff applies the same 20k-char
    # cap to its judged payloads.
    from modelman.benchmark.eval.judged_runner import (
        MAX_JUDGE_RESPONSE_CHARS,
        _build_judge_prompt,
    )

    huge = "x" * (MAX_JUDGE_RESPONSE_CHARS + 5000)
    prompt = _build_judge_prompt(CATEGORY.items[0], CATEGORY, huge)
    # "x" appears nowhere else in this prompt (rubric, item prompt, meta
    # note are all x-free), so the count pins exactly how much of the
    # response survived the cap — 20k, not the full 25k.
    assert prompt.count("x") == MAX_JUDGE_RESPONSE_CHARS
    # The cut is marked, not silent: a judge shown an unmarked mid-thought
    # ending would score the response as incomplete rather than truncated.
    assert "[TRUNCATED]" in prompt
    # A short response passes through verbatim, marker-free.
    short = _build_judge_prompt(CATEGORY.items[0], CATEGORY, "short response")
    assert "short response" in short
    assert "[TRUNCATED]" not in short
