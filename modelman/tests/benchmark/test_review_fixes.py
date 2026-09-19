"""Regression tests for the /code-review high findings on the eval benchmark."""

from __future__ import annotations

import subprocess

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.category import load_category
from modelman.benchmark.eval.evalplus_runner import run_coding_category
from modelman.benchmark.judge_core import (
    JudgeContractError,
    JudgeTransportError,
    LiteLLMJudgeTransport,
    Rubric,
    judge_row,
    parse_response,
)

RUBRIC = Rubric(dimensions={"a": 100})


class _Resp:
    status_code = 200
    text = "x"

    def __init__(self, content):
        self._content = content

    def json(self):
        return {"choices": [{"message": {"content": self._content}}]}


def test_null_content_is_transport_error(monkeypatch):
    # A 200 with content=null must fail as a transport error; otherwise None
    # flows into write_text/len() and aborts the whole run's persistence.
    monkeypatch.setattr("requests.post", lambda *a, **k: _Resp(None))
    t = LiteLLMJudgeTransport("http://x", "k", "m", retry_backoff_s=0)
    with pytest.raises(JudgeTransportError):
        t.complete("p", temperature=0.0)


@pytest.mark.parametrize(
    "raw",
    [
        '{"scores": null, "total": 1}',
        '{"scores": {"a": 100}, "total": 100, "flags": null}',
        None,
    ],
)
def test_wrong_typed_fields_are_contract_errors(raw):
    # Wrong-typed judge fields must surface as JudgeContractError so
    # judge_row's retry loop handles them instead of a raw TypeError.
    with pytest.raises(JudgeContractError):
        parse_response(raw, RUBRIC)


def test_json_found_after_unrelated_fence():
    # The answer must be found even when an earlier fence quotes code.
    raw = '```python\nx = {"y": 1}\n```\nAnswer: {"scores": {"a": 100}, "total": 100}'
    assert parse_response(raw, RUBRIC).total == 100


def test_zero_samples_rejected():
    # samples=0 used to reach median([]) and crash with StatisticsError.
    with pytest.raises(ValueError):
        judge_row(None, "p", RUBRIC, temperature=0.0, samples=0, max_attempts=1)


def test_duplicate_and_unsafe_item_ids_rejected(tmp_path):
    # Item ids are directory names; duplicates overwrite and '/' escapes.
    cat = tmp_path / "reasoning"
    cat.mkdir()
    (cat / "rubric.md").write_text("r")
    (cat / "rubric.toml").write_text("[dimensions]\na = 100\n")
    for body in (
        '[[items]]\nid="x"\nprompt="p"\n[[items]]\nid="x"\nprompt="q"\n',
        '[[items]]\nid="../x"\nprompt="p"\n',
    ):
        (cat / "items.toml").write_text(body)
        with pytest.raises(BenchmarkError):
            load_category(cat)


@pytest.mark.parametrize("exc", [subprocess.TimeoutExpired("evalplus", 1), FileNotFoundError("x")])
def test_evalplus_failures_become_coding_error(exc):
    # A timeout or missing binary must not abort the row's other categories.
    def boom(*a, **k):
        raise exc

    result = run_coding_category(
        base_url="http://x", model="m", api_key="k", dataset="humaneval", limit=1, run_cmd=boom
    )
    assert result.error and result.pass_at_1 is None
