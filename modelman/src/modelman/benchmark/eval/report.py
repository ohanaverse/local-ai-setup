from __future__ import annotations

from pathlib import Path


def write_row_artifacts(result) -> None:
    result.row_dir.mkdir(parents=True, exist_ok=True)


def render_summary(run_id, results, registry, category_names) -> str:
    return f"# eval run {run_id}\n"


def write_metrics_jsonl(path: Path, results) -> None:
    path.write_text("", encoding="utf-8")


def write_run_toml(path: Path, suite, *, git_sha: str) -> None:
    path.write_text("", encoding="utf-8")
