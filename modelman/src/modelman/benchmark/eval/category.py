"""Category directory loading for `modelman benchmark eval`.

A category lives under benchmarks/tasks/eval/<name>/: items.toml (the fixed
prompt set) plus rubric.toml/rubric.md (except `coding`, which EvalPlus
grades and which instead uses items.toml to hold dataset/limit config).
"""

from __future__ import annotations

import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.judge_core import Rubric

CODING_CATEGORY = "coding"


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


def load_category(path: Path) -> Category:
    path = Path(path)
    name = path.name
    if not path.is_dir():
        raise BenchmarkError(f"category not found: {path}")

    items_path = path / "items.toml"
    if not items_path.is_file():
        raise BenchmarkError(f"category {path} missing items.toml")
    with items_path.open("rb") as f:
        items_raw = tomllib.load(f)

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

    with rubric_toml_path.open("rb") as f:
        rubric_raw = tomllib.load(f)
    dimensions = rubric_raw.get("dimensions", {})
    if not dimensions:
        raise BenchmarkError(f"category {path} rubric.toml has no [dimensions]")
    total_points = sum(dimensions.values())
    if total_points != 100:
        raise BenchmarkError(
            f"category {path} rubric dimensions sum to {total_points}, must sum to 100"
        )
    verdicts = rubric_raw.get("verdicts")
    rubric = Rubric(dimensions=dict(dimensions), verdicts=frozenset(verdicts) if verdicts else None)

    items = [
        Item(id=raw["id"], prompt=raw["prompt"], meta=raw.get("meta", {}))
        for raw in items_raw.get("items", [])
    ]
    if not items:
        raise BenchmarkError(f"category {path} items.toml has no [[items]]")

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
