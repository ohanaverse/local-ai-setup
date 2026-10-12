"""Epoch AI's benchmark ZIP: `epoch_capabilities_index/eci_scores.csv`."""

import csv
import io
import zipfile

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources._shape import require_rows

SOURCE = "epoch"
MIN_ROWS = 100
MEMBER = "epoch_capabilities_index/eci_scores.csv"
NAME_COLUMN = "Display name"
SCORE_COLUMN = "eci"
CI_LOW_COLUMN = "eci_ci_low"


def _float(where: str, text: str) -> float:
    try:
        return float(text)
    except ValueError as exc:
        raise SourceParseError(f"{SOURCE}: {where} is not a number: {text!r}") from exc


def parse(data: bytes, *, min_rows: int = MIN_ROWS) -> tuple[ScoreRecord, ...]:
    """One record per CSV row that has a name and an `eci` value. An empty
    `eci_ci_low` reads as None."""
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            text = archive.read(MEMBER).decode("utf-8-sig")
    except zipfile.BadZipFile as exc:
        raise SourceParseError(f"{SOURCE}: not a ZIP archive") from exc
    except KeyError as exc:
        raise SourceParseError(f"{SOURCE}: the archive has no {MEMBER}") from exc
    except UnicodeDecodeError as exc:
        raise SourceParseError(f"{SOURCE}: {MEMBER} is not UTF-8") from exc
    reader = csv.DictReader(io.StringIO(text))
    columns = reader.fieldnames or []
    for column in (NAME_COLUMN, SCORE_COLUMN, CI_LOW_COLUMN):
        if column not in columns:
            raise SourceParseError(f"{SOURCE}: {MEMBER} has no {column!r} column")
    records: list[ScoreRecord] = []
    for row in reader:
        name = (row[NAME_COLUMN] or "").strip()
        score = (row[SCORE_COLUMN] or "").strip()
        if not name or not score:
            continue
        ci_low = (row[CI_LOW_COLUMN] or "").strip()
        records.append(
            ScoreRecord(
                source=SOURCE,
                source_id=name,
                score=_float(f"{name}.eci", score),
                ci_lower=_float(f"{name}.eci_ci_low", ci_low) if ci_low else None,
            )
        )
    require_rows(SOURCE, len(records), min_rows)
    return tuple(records)
