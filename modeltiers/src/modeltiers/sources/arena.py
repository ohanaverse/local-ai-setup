"""Arena's agent leaderboard, as saved by `fetch.fetch_arena`: one JSON
document, `{"pages": [...]}`, holding each rows-API page body unparsed."""

import re

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources._shape import load_json, number, require_rows

SOURCE = "arena"
MIN_ROWS = 20
EFFORTS = frozenset({"none", "minimal", "low", "medium", "high", "xhigh", "max"})
_PAREN = re.compile(r"\(([^()]*)\)")


def split_effort(name: str) -> tuple[str, str | None]:
    """Remove each parenthesis whose content is an effort word, whatever its
    case, and return the name left and the effort in lower case.

    `GLM 5.3 (Max)` gives (`GLM 5.3`, `max`); `DeepSeek V4 Pro (High) (0813)`
    gives (`DeepSeek V4 Pro (0813)`, `high`); `Qwen3.8 Max` is unchanged.
    """
    efforts: list[str] = []

    def drop(match: re.Match[str]) -> str:
        word = match.group(1).strip().lower()
        if word not in EFFORTS:
            return match.group(0)
        efforts.append(word)
        return ""

    kept = " ".join(_PAREN.sub(drop, name).split())
    return kept, (efforts[0] if efforts else None)


def parse(data: bytes, *, min_rows: int = MIN_ROWS) -> tuple[ScoreRecord, ...]:
    """One record per leaderboard row, across every page. The pages together
    must hold exactly `num_rows_total` rows; a page with an `error` key is a
    failed download."""
    doc = load_json(SOURCE, data)
    pages = doc.get("pages") if isinstance(doc, dict) else None
    if not isinstance(pages, list) or not pages:
        raise SourceParseError(f"{SOURCE}: expected an object with a non-empty pages list")
    records: list[ScoreRecord] = []
    total: int | None = None
    for index, body in enumerate(pages):
        if not isinstance(body, str):
            raise SourceParseError(f"{SOURCE}: pages[{index}] is not a string")
        page = load_json(SOURCE, body.encode("utf-8"))
        if not isinstance(page, dict):
            raise SourceParseError(f"{SOURCE}: pages[{index}] is not a JSON object")
        if "error" in page:
            raise SourceParseError(f"{SOURCE}: pages[{index}] is an error body: {page['error']}")
        rows = page.get("rows")
        page_total = page.get("num_rows_total")
        if not isinstance(rows, list) or not isinstance(page_total, int):
            raise SourceParseError(
                f"{SOURCE}: pages[{index}] has no rows list or no num_rows_total"
            )
        total = page_total
        for item in rows:
            row = item.get("row") if isinstance(item, dict) else None
            if not isinstance(row, dict) or not isinstance(row.get("model_name"), str):
                raise SourceParseError(f"{SOURCE}: pages[{index}] has a row with no model_name")
            name, effort = split_effort(row["model_name"])
            where = row["model_name"]
            records.append(
                ScoreRecord(
                    source=SOURCE,
                    source_id=name,
                    score=number(SOURCE, f"{where}.score", row.get("score")),
                    ci_lower=number(SOURCE, f"{where}.score_ci_lower", row.get("score_ci_lower")),
                    effort=effort,
                )
            )
    if len(records) != total:
        raise SourceParseError(f"{SOURCE}: read {len(records)} rows of num_rows_total {total}")
    require_rows(SOURCE, len(records), min_rows)
    return tuple(records)
