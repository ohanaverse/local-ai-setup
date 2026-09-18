"""summary.md rendering and per-row artifact writes for
`modelman benchmark eval` runs."""

from __future__ import annotations

import json
from dataclasses import asdict
from pathlib import Path
from typing import Any

from modelman._toml_io import atomic_write_toml
from modelman.benchmark.eval.evalplus_runner import CodingResult
from modelman.benchmark.eval.judged_runner import CategoryRowResult
from modelman.benchmark.eval.suite import Suite
from modelman.registry import Registry


def _family_of(model_id: str, registry: Registry) -> str:
    try:
        return registry.model(model_id).family
    except KeyError:
        return model_id


def _cell(value: object) -> str:
    if value is None:
        return "N/A"
    if isinstance(value, CodingResult):
        if value.pass_at_1 is not None:
            return f"{value.pass_at_1 * 100:.1f}%"
        # "EVALPLUS_ERROR", not "JUDGE_FAIL" — coding has no judge (EvalPlus
        # grades it directly), so labeling an EvalPlus execution failure
        # JUDGE_FAIL would contradict the Anomalies table (which correctly
        # calls it an evalplus error) and send someone hunting for a
        # nonexistent judge problem.
        return "EVALPLUS_ERROR" if value.error else "N/A"
    if isinstance(value, CategoryRowResult):
        return f"{value.score_100:.1f}" if value.score_100 is not None else "JUDGE_FAIL"
    return "N/A"


def _capability_matrix(results: list, registry: Registry, category_names: list[str]) -> str:
    header = "| family | row | route | " + " | ".join(category_names) + " |"
    sep = "|---|---|---|" + "---|" * len(category_names)
    lines = [header, sep]
    for r in sorted(results, key=lambda r: (_family_of(r.row.model_id, registry), r.row.label)):
        family = _family_of(r.row.model_id, registry)
        # A row can have an error AND partial category_results (a later
        # category failed after earlier ones already scored) — render the
        # categories that actually ran, and ISOLATION_ERROR only for the
        # ones that didn't.
        cells = [
            _cell(r.category_results[name])
            if name in r.category_results
            else ("ISOLATION_ERROR" if r.error else _cell(None))
            for name in category_names
        ]
        lines.append(f"| {family} | {r.row.label} | {r.row.route} | " + " | ".join(cells) + " |")
    return "\n".join(lines)


def _score_of(result, category_name: str) -> float | None:
    value = result.category_results.get(category_name)
    if isinstance(value, CodingResult):
        return value.pass_at_1 * 100 if value.pass_at_1 is not None else None
    if isinstance(value, CategoryRowResult):
        return value.score_100
    return None


def _leaderboard(results: list, category_names: list[str]) -> str:
    lines = ["| category | rank | row | score |", "|---|---|---|---|"]
    for name in category_names:
        # An explicit filter loop (rather than a single filtered generator
        # expression re-calling _score_of) so mypy can narrow score to
        # `float` before the sort key negates it.
        pairs: list[tuple[Any, float]] = []
        for r in results:
            score = _score_of(r, name)
            if score is not None:
                pairs.append((r, score))
        scored = sorted(pairs, key=lambda pair: -pair[1])
        for rank, (r, score) in enumerate(scored[:3], start=1):
            lines.append(f"| {name} | {rank} | {r.row.label} | {score:.1f} |")
        if not scored:
            lines.append(f"| {name} | — | — | no scored rows |")
    return "\n".join(lines)


def _anomalies(results: list, category_names: list[str]) -> str:
    lines = ["| label | anomaly |", "|---|---|"]
    for r in results:
        if r.error:
            lines.append(f"| {r.row.label} | ISOLATION_ERROR: {r.error[:200]} |")
        # Not an `elif`/`continue` — a row can carry an error alongside
        # partial category_results, and those completed categories still
        # deserve their own judge_fail/evalplus-error anomaly rows.
        for name in category_names:
            value = r.category_results.get(name)
            if isinstance(value, CategoryRowResult):
                for item in value.items:
                    if item.judge.status == "judge_fail":
                        lines.append(f"| {r.row.label} | JUDGE_FAIL ({name}/{item.item_id}) |")
            if isinstance(value, CodingResult) and value.error:
                lines.append(f"| {r.row.label} | evalplus error ({name}): {value.error[:150]} |")
    if len(lines) == 2:
        lines.append("| — | none |")
    return "\n".join(lines)


def render_summary(
    run_id: str, results: list, registry: Registry, category_names: list[str]
) -> str:
    return (
        f"# Eval benchmark run {run_id}\n\n"
        f"## Capability matrix\n\n{_capability_matrix(results, registry, category_names)}\n\n"
        f"## Per-category leaderboard\n\n{_leaderboard(results, category_names)}\n\n"
        f"## Anomalies\n\n{_anomalies(results, category_names)}\n"
    )


def write_row_artifacts(result) -> None:
    result.row_dir.mkdir(parents=True, exist_ok=True)
    # error and category_results are independent: a row can have BOTH (a
    # later category failed after earlier ones already scored) — write
    # whatever categories completed first, so a failure never costs
    # already-computed (and possibly API-billed) results, then error.txt.
    for name, cat_result in result.category_results.items():
        cat_dir = result.row_dir / name
        cat_dir.mkdir(parents=True, exist_ok=True)
        if isinstance(cat_result, CodingResult):
            (cat_dir / "evalplus_result.json").write_text(
                json.dumps(asdict(cat_result), indent=2), encoding="utf-8"
            )
            # Normalize pass@1 (a 0-1 fraction) to the same /100 scale the
            # judged-category score.json below uses, so every category's
            # score.json is directly comparable regardless of scoring method.
            coding_score_100 = (
                cat_result.pass_at_1 * 100 if cat_result.pass_at_1 is not None else None
            )
            (cat_dir / "score.json").write_text(
                json.dumps({"score_100": coding_score_100}), encoding="utf-8"
            )
        elif isinstance(cat_result, CategoryRowResult):
            for item_result in cat_result.items:
                item_dir = cat_dir / item_result.item_id
                item_dir.mkdir(parents=True, exist_ok=True)
                (item_dir / "response.txt").write_text(item_result.response_text, encoding="utf-8")
                (item_dir / "judge.json").write_text(
                    json.dumps(asdict(item_result.judge), indent=2), encoding="utf-8"
                )
            (cat_dir / "score.json").write_text(
                json.dumps({"score_100": cat_result.score_100}), encoding="utf-8"
            )
    if result.error is not None:
        (result.row_dir / "error.txt").write_text(result.error, encoding="utf-8")


def write_run_toml(path: Path, suite: Suite, *, git_sha: str) -> None:
    payload = {
        "run": {"git_sha": git_sha},
        "suite": {
            "name": suite.name,
            "judge": {
                "model": suite.judge.model,
                "temperature": suite.judge.temperature,
                "samples": suite.judge.samples,
                "max_attempts": suite.judge.max_attempts,
                "route": suite.judge.route,
            },
            # TOML has no null literal — omit an unset [coding] override
            # entirely rather than writing dataset/limit as None (tomli-w
            # cannot serialize None).
            "coding": {
                k: v
                for k, v in {"dataset": suite.coding.dataset, "limit": suite.coding.limit}.items()
                if v is not None
            },
        },
    }
    atomic_write_toml(payload, path)


def write_metrics_jsonl(path: Path, results: list) -> None:
    with path.open("w", encoding="utf-8") as f:
        for r in results:
            categories = {}
            for name, value in r.category_results.items():
                if isinstance(value, CodingResult):
                    categories[name] = (
                        value.pass_at_1 * 100 if value.pass_at_1 is not None else None
                    )
                elif isinstance(value, CategoryRowResult):
                    categories[name] = value.score_100
            f.write(
                json.dumps(
                    {
                        "label": r.row.label,
                        "model_id": r.row.model_id,
                        "route": r.row.route,
                        "error": r.error,
                        "categories": categories,
                    }
                )
                + "\n"
            )
