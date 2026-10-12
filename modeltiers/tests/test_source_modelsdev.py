import json
from collections.abc import Callable
from typing import Any

import pytest

from modeltiers.errors import SourceParseError
from modeltiers.records import ModelsDevRow
from modeltiers.sources import modelsdev


def _doc(row: dict[str, Any]) -> bytes:
    return json.dumps({"ollama-cloud": {"models": {"m": row}}}).encode()


def test_golden(read_fixture: Callable[[str], bytes]) -> None:
    parsed = modelsdev.parse(read_fixture("modelsdev.json"), min_rows=12)
    assert len(parsed.rows) == 12
    assert parsed.rows[0] == ModelsDevRow(
        provider="ollama-cloud",
        model_id="glm-5.3",
        canonical="zhipuai/glm-5.3",
        tool_call=True,
        open_weights=True,
        context=1048576,
    )
    by_host = {(r.provider, r.model_id): r for r in parsed.rows}
    # A host row with no canonical_model_id is kept, with canonical None.
    assert by_host[("ollama-cloud", "deepseek-v4-pro")].canonical is None
    assert by_host[("ollama-cloud", "deepseek-v4-pro")].context == 1048576
    # The canonical key keeps models.dev's own spelling, capitals included.
    assert by_host[("openrouter", "minimax/minimax-m3")].canonical == "minimax/MiniMax-M3"
    assert by_host[("openrouter", "anthropic/claude-opus-5.5")].open_weights is False


def test_a_row_without_limit_context_has_no_context() -> None:
    for row in ({"tool_call": True}, {"tool_call": True, "limit": {"output": 8192}}):
        (parsed,) = modelsdev.parse(_doc(row), min_rows=1).rows
        assert parsed.context is None
        assert parsed.tool_call is True


def test_missing_flags_read_as_false() -> None:
    (parsed,) = modelsdev.parse(_doc({"limit": {"context": 4096}}), min_rows=1).rows
    assert parsed.tool_call is False
    assert parsed.open_weights is False
    assert parsed.canonical is None


def test_malformed_fixture_raises(read_fixture: Callable[[str], bytes]) -> None:
    with pytest.raises(SourceParseError, match="provider ollama-cloud has no models object"):
        modelsdev.parse(read_fixture("modelsdev_malformed.json"), min_rows=1)


def test_too_few_rows_raises(read_fixture: Callable[[str], bytes]) -> None:
    with pytest.raises(SourceParseError, match="1 rows, under the minimum of 2"):
        modelsdev.parse(read_fixture("modelsdev_too_few.json"), min_rows=2)


def test_the_default_minimum_is_the_specs() -> None:
    assert modelsdev.MIN_ROWS == 1000
    with pytest.raises(SourceParseError, match="under the minimum of 1000"):
        modelsdev.parse(_doc({"tool_call": True}))


@pytest.mark.parametrize(
    ("data", "message"),
    [
        (b'{"ollama-cloud": ', "not JSON"),
        (b"[]", "expected an object of providers"),
        (b'{"p": "x"}', "provider p is not an object"),
        (b'{"p": {}}', "provider p has no models object"),
        (b'{"p": {"models": {"m": 7}}}', "p/m is not an object"),
        (_doc({"canonical_model_id": 7}), "canonical_model_id is not a string"),
        (_doc({"tool_call": "yes"}), "tool_call is not a boolean"),
        (_doc({"limit": []}), "limit is not an object"),
        (_doc({"limit": {"context": "1M"}}), "limit.context is not an integer"),
    ],
)
def test_an_unrecognised_shape_raises(data: bytes, message: str) -> None:
    with pytest.raises(SourceParseError, match=message):
        modelsdev.parse(data, min_rows=1)
