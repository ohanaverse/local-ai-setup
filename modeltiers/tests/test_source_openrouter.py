import json
from collections.abc import Callable
from typing import Any

import pytest

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources import openrouter


def _doc(model: Any) -> bytes:
    return json.dumps({"data": [model]}).encode()


def _scored(indices: Any) -> bytes:
    return _doc({"id": "v/m", "benchmarks": {"artificial_analysis": indices}})


def test_golden(read_fixture: Callable[[str], bytes]) -> None:
    records = openrouter.parse(read_fixture("openrouter.json"), min_rows=4)
    assert records[0] == ScoreRecord(
        source="openrouter",
        source_id="z-ai/glm-5.3",
        score=44.8,
        coding_index=74.8,
        agentic_index=53.1,
        hugging_face_id="zai-org/GLM-5.3",
    )
    # Eight models; the one with no benchmarks and the one whose
    # artificial_analysis is null have no score and give no record.
    assert [r.source_id for r in records] == [
        "z-ai/glm-5.3",
        "z-ai/glm-5.3:batch",
        "anthropic/claude-opus-5.5",
        "liquid/lfm-2.5-2.6b:free",
        "anthropic/claude-fable-5",
        "qwen/qwen3.8-27b",
    ]


def test_null_coding_and_agentic_indices_are_none(read_fixture: Callable[[str], bytes]) -> None:
    records = openrouter.parse(read_fixture("openrouter.json"), min_rows=4)
    opus = records[2]
    assert (opus.score, opus.coding_index, opus.agentic_index) == (57.6, None, None)
    lfm = records[3]
    assert (lfm.coding_index, lfm.agentic_index) == (7.7, None)


def test_a_null_intelligence_index_gives_no_record() -> None:
    data = _scored({"intelligence_index": None, "coding_index": 50.0})
    with pytest.raises(SourceParseError, match="0 rows, under the minimum of 1"):
        openrouter.parse(data, min_rows=1)


def test_an_integer_index_is_a_float() -> None:
    (record,) = openrouter.parse(_scored({"intelligence_index": 25}), min_rows=1)
    assert record.score == 25.0
    assert isinstance(record.score, float)


def test_malformed_fixture_raises(read_fixture: Callable[[str], bytes]) -> None:
    with pytest.raises(SourceParseError, match="expected an object with a data list"):
        openrouter.parse(read_fixture("openrouter_malformed.json"), min_rows=1)


def test_too_few_rows_counts_scored_records(read_fixture: Callable[[str], bytes]) -> None:
    # Two models in the file, one with a score.
    with pytest.raises(SourceParseError, match="1 rows, under the minimum of 2"):
        openrouter.parse(read_fixture("openrouter_too_few.json"), min_rows=2)


def test_the_default_minimum_is_the_specs(read_fixture: Callable[[str], bytes]) -> None:
    assert openrouter.MIN_ROWS == 100
    with pytest.raises(SourceParseError, match="under the minimum of 100"):
        openrouter.parse(read_fixture("openrouter.json"))


@pytest.mark.parametrize(
    ("data", "message"),
    [
        (b"<html>", "not JSON"),
        (b"[]", "expected an object with a data list"),
        (_doc("z-ai/glm-5.3"), r"data\[0\] has no string id"),
        (_doc({"name": "no id"}), r"data\[0\] has no string id"),
        (_doc({"id": "v/m", "benchmarks": []}), "v/m.benchmarks is not an object"),
        (_scored([]), "v/m.artificial_analysis is not an object"),
        (_scored({"intelligence_index": "44.8"}), "v/m.intelligence_index is not a number"),
        (_scored({"intelligence_index": True}), "v/m.intelligence_index is not a number"),
        (
            _scored({"intelligence_index": 44.8, "coding_index": "x"}),
            "v/m.coding_index is not a number",
        ),
    ],
)
def test_an_unrecognised_shape_raises(data: bytes, message: str) -> None:
    with pytest.raises(SourceParseError, match=message):
        openrouter.parse(data, min_rows=1)


@pytest.mark.parametrize(
    ("given", "kept"),
    [("Org/Model", "Org/Model"), ("", None), (None, None), (7, None), (["x"], None)],
)
def test_the_hugging_face_id_is_kept_only_when_it_is_a_nonempty_string(
    given: Any, kept: str | None
) -> None:
    body = _doc(
        {
            "id": "v/m",
            "hugging_face_id": given,
            "benchmarks": {"artificial_analysis": {"intelligence_index": 10}},
        }
    )
    assert openrouter.parse(body, min_rows=1)[0].hugging_face_id == kept


def test_a_model_without_the_field_has_no_hugging_face_id() -> None:
    assert (
        openrouter.parse(_scored({"intelligence_index": 10}), min_rows=1)[0].hugging_face_id is None
    )
