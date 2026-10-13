"""What `show --html` writes: one self-contained page with every model and
candidate of the output document in a table the reader can sort and filter.

Like `show.py` it reads the document alone. Names and attribution lines come
from public sources, so nothing from the document is written as markup: the
rows travel as JSON in a script element that does not run, with every `<`,
`>` and `&` escaped, and the page's script puts each value in as text.
"""

import html
import json
import re
from collections.abc import Mapping
from datetime import datetime
from importlib import resources
from typing import Any

from modeltiers import show
from modeltiers.contracts import RANK, UNRATED
from modeltiers.show import SCORE_COLUMNS, UNMAPPED, Document

_PLACEHOLDER = re.compile(r"\{\{(\w+)\}\}")
_JSON_ESCAPES = {"<": "\\u003c", ">": "\\u003e", "&": "\\u0026"}


def _text(value: Any) -> str | None:
    return value if isinstance(value, str) else None


def _row(kind: str, name: str, entry: Mapping[str, Any]) -> dict[str, Any]:
    opened = entry.get("open_weights")
    context = show.number(entry.get("context"))
    row: dict[str, Any] = {
        "kind": kind,
        "name": name,
        "canonical": _text(entry.get("canonical")),
        "tier": _text(entry.get("tier")),
        "confidence": _text(entry.get("confidence")),
        "reason": _text(entry.get("reason")),
        "open_weights": opened if isinstance(opened, bool) else None,
        "context": None if context is None else int(context),
    }
    row.update({column: show.score(entry, column) for column in SCORE_COLUMNS})
    row["aligned_score"] = show.aligned_score(entry)
    row["aligned_sources"] = show.aligned_count(entry)
    return row


def page_rows(doc: Document) -> list[dict[str, Any]]:
    """One row per `[[models]]` entry, per `[[unmapped]]` row and per
    `[[candidates]]` entry, in that order and each in file order. Every row
    has every column; a value the file does not give, or gives as another
    type, is None."""
    rows = [_row("model", show.model_name(m), m) for m in show.entries(doc, "models")]
    rows.extend(
        _row(
            "model",
            show.model_name(entry),
            {"tier": UNRATED, "reason": UNMAPPED},
        )
        for entry in show.entries(doc, "unmapped")
    )
    rows.extend(
        _row("candidate", show.candidate_name(c), c) for c in show.entries(doc, "candidates")
    )
    return rows


def _alignment_note(doc: Document) -> str:
    """What the page says under the table about the aligned score: how far
    the lines fitted for this file typically miss, in the file's own words."""
    misses = []
    for signal, fit in show.table(doc.get("alignment")).items():
        miss = show.number(show.table(fit).get("typical_miss"))
        if miss is not None:
            misses.append(f"{miss:.1f} points ({signal})")
    if not misses:
        return (
            "This file holds no fitted line: an aligned score in it is a model's own "
            "intelligence index, and a file written before the score existed has none."
        )
    return (
        f"The lines fitted for this file typically miss by {' and '.join(misses)}; "
        "a smaller difference between two models means nothing."
    )


def _embedded(data: Mapping[str, Any]) -> str:
    """JSON that is safe inside a script element: no character that could
    end the element or start a comment survives as itself."""
    text = json.dumps(data, ensure_ascii=True, allow_nan=False)
    return re.sub("[<>&]", lambda found: _JSON_ESCAPES[found.group()], text)


def page_html(doc: Document, now: datetime, *, sort: str | None = None) -> str:
    """The whole page. `sort` (a name from `show.SORT_COLUMNS`) is the order
    it opens in; None is the file's order. The age line is the file's age at
    `now`; the page's script has `generated_at` to say the age again whenever
    the page is opened."""
    template = resources.files("modeltiers").joinpath("data/page.html").read_text("utf-8")
    values = {
        "age": html.escape(show.age_line(doc, now)),
        "alignment": html.escape(_alignment_note(doc)),
        "attribution": "\n".join(
            f"      <li>{html.escape(line)}</li>" for line in show.attribution_lines(doc)
        ),
        "data": _embedded(
            {
                "rows": page_rows(doc),
                "sort": sort,
                "ranks": dict(RANK),
                "generated_at": _text(doc.get("generated_at")),
            }
        ),
    }
    # One pass: a value that happens to hold a placeholder is not filled again.
    return _PLACEHOLDER.sub(lambda found: values[found.group(1)], template)
