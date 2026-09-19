"""Category directory loading for `modelman benchmark eval`.

A category lives under benchmarks/tasks/eval/<name>/: items.toml (the fixed
prompt set) plus rubric.toml/rubric.md (except `coding`, which EvalPlus
grades and which instead uses items.toml to hold dataset/limit config).
"""

from __future__ import annotations

import re
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.judge_core import Rubric

CODING_CATEGORY = "coding"
_ITEM_ID_RE = re.compile(r"[A-Za-z0-9._-]+")


@dataclass
class Item:
    id: str
    prompt: str
    meta: dict[str, Any] = field(default_factory=dict)


@dataclass
class Category:
    name: str
    path: Path
    items: list[Item]
    rubric: Rubric | None
    rubric_md: str | None
    coding_dataset: str | None = None
    coding_limit: int | None = None


def _load_coding_category(name: str, path: Path, items_raw: dict[str, Any]) -> Category:
    return Category(
        name=name,
        path=path,
        items=[],
        rubric=None,
        rubric_md=None,
        coding_dataset=items_raw.get("dataset", "humaneval"),
        coding_limit=items_raw.get("limit"),
    )


def _load_toml(path: Path) -> dict[str, Any]:
    try:
        with path.open("rb") as f:
            return tomllib.load(f)
    except tomllib.TOMLDecodeError as exc:
        raise BenchmarkError(f"malformed TOML in {path}: {exc}") from exc


def load_category(path: Path) -> Category:
    path = Path(path)
    name = path.name
    if not path.is_dir():
        raise BenchmarkError(f"category not found: {path}")

    items_path = path / "items.toml"
    if not items_path.is_file():
        raise BenchmarkError(f"category {path} missing items.toml")
    items_raw = _load_toml(items_path)

    if name == CODING_CATEGORY:
        return _load_coding_category(name, path, items_raw)

    rubric_toml_path = path / "rubric.toml"
    rubric_md_path = path / "rubric.md"
    missing = [
        n
        for n, p in (("rubric.toml", rubric_toml_path), ("rubric.md", rubric_md_path))
        if not p.is_file()
    ]
    if missing:
        raise BenchmarkError(f"category {path} missing required entries: {', '.join(missing)}")

    rubric_raw = _load_toml(rubric_toml_path)
    dimensions = rubric_raw.get("dimensions", {})
    if not dimensions:
        raise BenchmarkError(f"category {path} rubric.toml has no [dimensions]")
    # Weights must be positive integers: floats such as 33.3/33.3/33.4 sum
    # to 99.99999999999999, negative weights can offset each other to 100,
    # and a string weight would raise a bare TypeError from sum().
    if not isinstance(dimensions, dict):
        raise BenchmarkError(f"category {path} rubric.toml [dimensions] must be a table")
    bad_weights = {
        dim: weight
        for dim, weight in dimensions.items()
        if isinstance(weight, bool) or not isinstance(weight, int) or weight <= 0
    }
    if bad_weights:
        raise BenchmarkError(
            f"category {path} rubric dimensions must be positive integers, got {bad_weights!r}"
        )
    total_points = sum(dimensions.values())
    if total_points != 100:
        raise BenchmarkError(
            f"category {path} rubric dimensions sum to {total_points}, must sum to 100"
        )
    verdicts = rubric_raw.get("verdicts")
    if verdicts is not None and (
        not isinstance(verdicts, list) or not all(isinstance(v, str) for v in verdicts)
    ):
        raise BenchmarkError(f"category {path} rubric.toml verdicts must be a list of strings")
    rubric = Rubric(dimensions=dict(dimensions), verdicts=frozenset(verdicts) if verdicts else None)

    items: list[Item] = []
    for index, raw in enumerate(items_raw.get("items", []), start=1):
        if (
            not isinstance(raw, dict)
            or not isinstance(raw.get("id"), str)
            or not isinstance(raw.get("prompt"), str)
        ):
            raise BenchmarkError(
                f"category {path} items.toml [[items]] #{index} needs string `id` and `prompt`"
            )
        meta = raw.get("meta", {})
        if not isinstance(meta, dict):
            raise BenchmarkError(
                f"category {path} items.toml [[items]] #{index} meta must be a table"
            )
        items.append(Item(id=raw["id"], prompt=raw["prompt"], meta=meta))
    if not items:
        raise BenchmarkError(f"category {path} items.toml has no [[items]]")
    # Ids become directory names and lookup keys: a duplicate silently
    # overwrites artifacts and mis-pairs judging, and a path-like id escapes
    # the category directory.
    seen: set[str] = set()
    for item in items:
        if not _ITEM_ID_RE.fullmatch(item.id) or item.id in (".", ".."):
            raise BenchmarkError(f"category {path} has invalid item id {item.id!r}")
        if item.id in seen:
            raise BenchmarkError(f"category {path} has duplicate item id {item.id!r}")
        seen.add(item.id)

    return Category(
        name=name,
        path=path,
        items=items,
        rubric=rubric,
        rubric_md=rubric_md_path.read_text(encoding="utf-8"),
    )


def list_categories(root: Path) -> list[Category]:
    root = Path(root)
    if not root.is_dir():
        return []
    return [
        load_category(child)
        for child in sorted(root.iterdir())
        if child.is_dir() and (child / "items.toml").is_file()
    ]
