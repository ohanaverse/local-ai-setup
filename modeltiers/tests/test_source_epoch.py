import io
import zipfile
from collections.abc import Callable

import pytest

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources import epoch


def _zip(member: str, content: bytes) -> bytes:
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w") as archive:
        archive.writestr(member, content)
    return buffer.getvalue()


def _csv(*lines: str) -> bytes:
    text = "\n".join(("Display name,eci,eci_ci_low", *lines)) + "\n"
    return _zip(epoch.MEMBER, text.encode())


def test_golden(epoch_zip: Callable[..., bytes]) -> None:
    records = epoch.parse(epoch_zip(), min_rows=5)
    assert records[0] == ScoreRecord(
        source="epoch", source_id="Claude Opus 5.5", score=167.33, ci_lower=164.06
    )
    assert [(r.source_id, r.score) for r in records] == [
        ("Claude Opus 5.5", 167.33),
        ("GLM-5.3", 155.61),
        ("DeepSeek V4 Pro 0813", 155.31),
        ("MiniMax-M3", 146.95),
        ("GPT-5", 150.0),
    ]
    assert all(r.effort is None for r in records)


def test_an_empty_interval_is_none(epoch_zip: Callable[..., bytes]) -> None:
    records = epoch.parse(epoch_zip(), min_rows=5)
    assert records[4].ci_lower is None


def test_a_byte_order_mark_is_ignored() -> None:
    data = _zip(epoch.MEMBER, b"\xef\xbb\xbfDisplay name,eci,eci_ci_low\nM,150.5,149\n")
    (record,) = epoch.parse(data, min_rows=1)
    assert (record.source_id, record.score, record.ci_lower) == ("M", 150.5, 149.0)


def test_a_row_without_a_name_or_a_score_gives_no_record() -> None:
    records = epoch.parse(_csv("A,150,149", ",151,150", "C,,"), min_rows=1)
    assert [r.source_id for r in records] == ["A"]


def test_malformed_fixture_raises(epoch_zip: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="has no 'eci' column"):
        epoch.parse(epoch_zip("eci_scores_malformed.csv"), min_rows=1)


def test_too_few_rows_raises(epoch_zip: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="1 rows, under the minimum of 2"):
        epoch.parse(epoch_zip("eci_scores_too_few.csv"), min_rows=2)


def test_the_default_minimum_is_the_specs(epoch_zip: Callable[..., bytes]) -> None:
    assert epoch.MIN_ROWS == 100
    with pytest.raises(SourceParseError, match="under the minimum of 100"):
        epoch.parse(epoch_zip())


@pytest.mark.parametrize(
    ("data", "message"),
    [
        (b"<html>502 Bad Gateway</html>", "not a ZIP archive"),
        (_zip("README.md", b"x"), "the archive has no epoch_capabilities_index/eci_scores.csv"),
        (_zip(epoch.MEMBER, b"\xff\xfe\x00"), "is not UTF-8"),
        (_zip(epoch.MEMBER, b""), "has no 'Display name' column"),
        (_csv("M,high,149"), "M.eci is not a number"),
        (_csv("M,150,low"), "M.eci_ci_low is not a number"),
    ],
)
def test_an_unrecognised_shape_raises(data: bytes, message: str) -> None:
    with pytest.raises(SourceParseError, match=message):
        epoch.parse(data, min_rows=1)
