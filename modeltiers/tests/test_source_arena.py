import json
from collections.abc import Callable
from typing import Any

import pytest

from modeltiers import fetch
from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources import arena


def _wrap(*pages: Any) -> bytes:
    return fetch.wrap_arena_pages([json.dumps(page).encode() for page in pages])


def _row(name: Any = "M", score: Any = 0.1, low: Any = 0.05) -> dict[str, Any]:
    return {"row": {"model_name": name, "score": score, "score_ci_lower": low}}


@pytest.mark.parametrize(
    ("name", "kept", "effort"),
    [
        ("Claude Opus 5.5 (High)", "Claude Opus 5.5", "high"),
        ("GLM 5.3 (Max)", "GLM 5.3", "max"),
        ("GPT 5.6 Sol (xHigh)", "GPT 5.6 Sol", "xhigh"),
        ("Qwen3.8 Max", "Qwen3.8 Max", None),  # max outside a parenthesis is the name
        ("DeepSeek V4 Pro (0813)", "DeepSeek V4 Pro (0813)", None),  # not an effort word
        ("DeepSeek V4 Pro (High) (0813)", "DeepSeek V4 Pro (0813)", "high"),
        ("Hy4 preview", "Hy4 preview", None),
        ("M (none)", "M", "none"),
        ("M (minimal)", "M", "minimal"),
        ("M (low)", "M", "low"),
        ("M ( Medium )", "M", "medium"),
    ],
)
def test_split_effort(name: str, kept: str, effort: str | None) -> None:
    assert arena.split_effort(name) == (kept, effort)


def test_golden_reads_both_pages_to_num_rows_total(arena_doc: Callable[..., bytes]) -> None:
    records = arena.parse(arena_doc(), min_rows=5)
    assert records[0] == ScoreRecord(
        source="arena",
        source_id="Claude Opus 5.5",
        score=0.14331280058415052,
        ci_lower=0.12150663912969831,
        effort="high",
    )
    assert [(r.source_id, r.effort) for r in records] == [
        ("Claude Opus 5.5", "high"),
        ("GLM 5.3", "max"),
        ("Qwen3.8 Max", None),
        ("DeepSeek V4 Pro (0813)", "high"),
        ("Minimax M3", None),
        ("Claude Fable 5", "high"),
    ]
    assert records[4].score < 0  # the score is centred near zero


def test_a_row_without_a_confidence_interval_keeps_none() -> None:
    """`ScoreRecord.ci_lower` is optional, so a row the board published no
    interval for must not fail its own row, let alone the whole source."""
    data = _wrap({"rows": [_row(low=None)], "num_rows_total": 1})
    (record,) = arena.parse(data, min_rows=1)
    assert record.ci_lower is None


def test_fewer_rows_than_num_rows_total_raises(arena_doc: Callable[..., bytes]) -> None:
    # Page 1 alone: 3 rows, and it says the dataset has 6.
    with pytest.raises(SourceParseError, match="read 3 rows of num_rows_total 6"):
        arena.parse(arena_doc("arena_page1.json"), min_rows=1)


def test_an_error_body_is_a_failed_download(arena_doc: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="error body: the dataset index is loading"):
        arena.parse(arena_doc("arena_error.json"), min_rows=1)
    with pytest.raises(SourceParseError, match=r"pages\[1\] is an error body"):
        arena.parse(arena_doc("arena_page1.json", "arena_error.json"), min_rows=1)


def test_malformed_fixture_raises(arena_doc: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="no rows list or no num_rows_total"):
        arena.parse(arena_doc("arena_malformed.json"), min_rows=1)


def test_too_few_rows_raises(arena_doc: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="1 rows, under the minimum of 2"):
        arena.parse(arena_doc("arena_too_few.json"), min_rows=2)


def test_the_default_minimum_is_the_specs(arena_doc: Callable[..., bytes]) -> None:
    assert arena.MIN_ROWS == 20
    with pytest.raises(SourceParseError, match="under the minimum of 20"):
        arena.parse(arena_doc())


@pytest.mark.parametrize(
    ("data", "message"),
    [
        (b"<html>", "not JSON"),
        (b'{"rows": []}', "expected an object with a non-empty pages list"),
        (b'{"pages": []}', "expected an object with a non-empty pages list"),
        (b'{"pages": [{"rows": []}]}', r"pages\[0\] is not a string"),
        (b'{"pages": ["<html>"]}', "not JSON"),
        (b'{"pages": ["[]"]}', r"pages\[0\] is not a JSON object"),
        (_wrap({"rows": [7], "num_rows_total": 1}), "a row with no model_name"),
        (_wrap({"rows": [_row(name=None)], "num_rows_total": 1}), "a row with no model_name"),
        (_wrap({"rows": [_row(score=None)], "num_rows_total": 1}), "M.score is not a number"),
        (
            _wrap({"rows": [_row(score=float("nan"))], "num_rows_total": 1}),
            "M.score is not a finite number",
        ),
        (
            _wrap({"rows": [_row(low="0.05")], "num_rows_total": 1}),
            "M.score_ci_lower is not a number",
        ),
    ],
)
def test_an_unrecognised_shape_raises(data: bytes, message: str) -> None:
    with pytest.raises(SourceParseError, match=message):
        arena.parse(data, min_rows=1)
