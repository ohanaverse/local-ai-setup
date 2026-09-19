"""Tests for modelman.benchmark.eval.judged_runner — one generation call per
item (phase 1, under isolation), scored by the shared judge_core mechanics
in a separate judge phase (after restore), aggregated to a /100 category
score.
"""

import json

from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.judged_runner import generate_category, judge_category
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
        self.prompts_seen: list[str] = []

    def complete(self, prompt: str, *, temperature: float) -> str:
        self.prompts_seen.append(prompt)
        if not self.totals:
            return "not json"
        total = self.totals.pop(0)
        bug_det = min(total, 60)
        action = min(total - bug_det, 40)
        return json.dumps(
            {"scores": {"bug_detection": bug_det, "actionability": action}, "total": total}
        )


def _judge_kwargs(transport: _FakeJudgeTransport) -> dict:
    return {
        "judge_transport": transport,
        "judge_temperature": 0.0,
        "judge_samples": 1,
        "judge_max_attempts": 1,
    }


def test_generate_category_sends_item_prompt_verbatim_and_judges_nothing():
    # Phase 1 must send exactly each item's own prompt text (no judge/rubric
    # wrapping mixed in — that only belongs in the separate judge prompt),
    # and must return items with judge=None: judging is phase 2, after
    # restore_providers(), so an interrupted judge phase leaves the
    # generated responses on disk as rejudge-recoverable UNJUDGED items.
    row_transport = _FakeRowTransport()
    result = generate_category(CATEGORY, row_transport, temperature=0.0)
    assert row_transport.prompts_seen == ["review this", "review that"]
    assert all(item.judge is None and item.score_100 is None for item in result.items)
    assert result.score_100 is None


def test_judge_category_scores_as_mean_of_item_totals():
    # A category's row-level score_100 is the mean of its items' judge
    # totals (already /100 since every rubric sums to 100) — this pins the
    # aggregation formula so a future change to it is deliberate, not
    # accidental.
    result = generate_category(CATEGORY, _FakeRowTransport(), temperature=0.0)
    judge_category(CATEGORY, result, **_judge_kwargs(_FakeJudgeTransport([60, 40])))
    assert result.score_100 == 50.0
    assert [item.score_100 for item in result.items] == [60.0, 40.0]


def test_judge_category_ignores_judge_fail_items_in_the_mean():
    # An item whose judge call fails to parse must not drag the category
    # mean down to zero, and must not silently vanish either — it's excluded
    # from the mean but still recorded as a judge_fail item, so a partial
    # judge outage degrades the score gracefully instead of corrupting it.
    result = generate_category(CATEGORY, _FakeRowTransport(), temperature=0.0)
    # only one valid reply; item 2's judge call fails to parse
    judge_category(CATEGORY, result, **_judge_kwargs(_FakeJudgeTransport([80])))
    assert result.items[1].judge is not None
    assert result.items[1].judge.status == "judge_fail"  # type: ignore[union-attr]
    assert result.items[1].score_100 is None
    assert result.score_100 == 80.0


def test_judge_category_does_not_rejudge_already_judged_items():
    # Judging is idempotent: re-invoking judge_category on a fully-judged
    # result must make no further judge calls. This is what lets run_suite
    # resume an interrupted judge phase without re-paying the judge for
    # items that already carry a score.
    result = generate_category(CATEGORY, _FakeRowTransport(), temperature=0.0)
    transport = _FakeJudgeTransport([60, 40])
    judge_category(CATEGORY, result, **_judge_kwargs(transport))
    calls_after_first_pass = len(transport.prompts_seen)
    judge_category(CATEGORY, result, **_judge_kwargs(transport))
    assert len(transport.prompts_seen) == calls_after_first_pass
    assert result.score_100 == 50.0  # scores unchanged by the second pass


def test_judge_category_resumes_from_a_partial_judgment():
    # A judge phase interrupted after item 1 scored must not re-judge item 1
    # on resume — only the UNJUDGED item 2 gets a judge call, so the
    # already-paid score survives untouched (no double spend) and the
    # category mean covers both items.
    result = generate_category(CATEGORY, _FakeRowTransport(), temperature=0.0)
    judge_category(CATEGORY, result, **_judge_kwargs(_FakeJudgeTransport([60, 40])))
    assert result.items[0].score_100 == 60.0
    # Simulate the interrupt: item 2's outcome is discarded (never judged —
    # exactly the state an interrupted run_suite judge phase leaves).
    result.items[1].judge = None
    result.items[1].score_100 = None
    # Resume: the transport carries ONE reply, for item 2 only — if item 1
    # were re-judged it would consume it and item 2 would come back
    # judge_fail.
    judge_category(CATEGORY, result, **_judge_kwargs(_FakeJudgeTransport([40])))
    assert result.items[0].score_100 == 60.0  # untouched by the resume
    assert result.items[1].score_100 == 40.0
    assert result.score_100 == 50.0


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


def test_generate_category_failure_carries_items_generated_so_far():
    # A generation call failing on item 2 must not discard item 1's response:
    # generate_category raises CategoryGenerationError carrying the partial
    # category, so the runner can persist the finished (slow, possibly
    # API-billed) work instead of losing every item in the category.
    import pytest

    from modelman.benchmark.eval.judged_runner import CategoryGenerationError

    class _FlakyTransport:
        def __init__(self):
            self.calls = 0

        def complete(self, prompt: str, *, temperature: float) -> str:
            self.calls += 1
            if self.calls == 2:
                raise RuntimeError("timeout on item 2")
            return "first answer"

    with pytest.raises(CategoryGenerationError, match="timeout on item 2") as excinfo:
        generate_category(CATEGORY, _FlakyTransport(), temperature=0.0)
    assert [i.item_id for i in excinfo.value.partial.items] == ["i1"]
    assert excinfo.value.partial.items[0].response_text == "first answer"
