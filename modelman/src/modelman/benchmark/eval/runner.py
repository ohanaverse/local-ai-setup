"""Suite orchestration for `modelman benchmark eval`: group rows by
provider, isolate once per group, run every row's categories, restore.

Mirrors modelman.benchmark.agent.runner's isolation-grouping loop (same
(provider, extra_args) keying for mlx_lm_server's per-pairing re-isolation)
but with no pi process, no workspace, and no gates — each row's "run" is a
handful of direct HTTP calls per category instead of a full agent session.
"""

from __future__ import annotations

import itertools
import json
import subprocess
import tomllib
from dataclasses import asdict, dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from statistics import mean

from modelman.benchmark import isolation
from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval import evalplus_runner, judged_runner, report
from modelman.benchmark.eval.category import CODING_CATEGORY, Category
from modelman.benchmark.eval.suite import (
    LITELLM_PLIST,
    LIVE_PI_MODELS_PATH,
    OPENROUTER_BASE_URL,
    JudgeConfig,
    RowConfig,
    Suite,
    load_live_models,
    openrouter_key,
    preflight,
    resolve_row_endpoint,
)
from modelman.benchmark.judge_core import (
    JudgeOutcome,
    JudgeScore,
    JudgeTransport,
    LiteLLMJudgeTransport,
    judge_row,
)
from modelman.registry import Registry

DEFAULT_RESULTS_DIR = Path.home() / ".config" / "local-ai" / "benchmarks"
ISOLATABLE_PROVIDERS = isolation.SUPPORTED_PROVIDER_IDS
DEFAULT_CODING_DATASET = "humaneval"


@dataclass
class RowRunResult:
    row: RowConfig
    row_dir: Path
    category_results: dict[str, object] = field(default_factory=dict)
    error: str | None = None


class RunSavedButRestoreFailed(BenchmarkError):
    """Every row completed and is on disk; only putting the backends back
    failed. Carries run_dir/results so the CLI can still record --latest."""

    def __init__(self, message: str, *, run_dir: Path, results: list[RowRunResult]) -> None:
        super().__init__(message)
        self.run_dir = run_dir
        self.results = results


def _row_dir(run_dir: Path, index: int, row: RowConfig) -> Path:
    return run_dir / f"{index:02d}--{row.label}"


def _default_judge_transport_factory(judge_cfg) -> JudgeTransport:
    """Takes a JudgeConfig (not a full Suite) so both run_suite (passes
    suite.judge) and rejudge_run (passes a JudgeConfig reconstructed from a
    persisted run.toml, with no Suite in hand) can share this."""
    if judge_cfg.route == "openrouter":
        key = openrouter_key(LITELLM_PLIST)
        if not key:
            raise BenchmarkError("judge route=openrouter needs OPENROUTER_API_KEY")
        model = judge_cfg.model
        if model.startswith("openrouter/"):
            model = model[len("openrouter/") :]
        return LiteLLMJudgeTransport(base_url=OPENROUTER_BASE_URL, api_key=key, model=model)
    live = load_live_models(LIVE_PI_MODELS_PATH)
    litellm_entry = live.get("providers", {}).get("litellm", {})
    api_key = litellm_entry.get("apiKey")
    if not api_key:
        raise BenchmarkError(
            "no LiteLLM apiKey found in ~/.pi/agent/models.json for the judge transport"
        )
    base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
    return LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=judge_cfg.model)


def _git_sha() -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=False
        )
        return result.stdout.strip() or "unknown"
    except OSError:
        return "unknown"


def _row_categories(row: RowConfig, categories: list[Category]) -> list[Category]:
    if row.categories is None:
        return categories
    wanted = set(row.categories)
    return [c for c in categories if c.name in wanted]


def _isolation_extra_args(row: RowConfig) -> tuple[str, ...]:
    if row.provider_id == "mlx_lm_server":
        return isolation.mlx_lm_server_pairing_args(
            row.model_id,
            row.target_local_path,
            row.target_repo,
            row.draft_local_path,
            row.draft_repo,
        )
    if row.provider_id == "mtplx":
        assert row.mtplx_model_name is not None
        return (row.mtplx_model_name,)
    return ()


def _select_rows(rows: list[RowConfig], row_filter: list[str] | None) -> list[RowConfig]:
    if not row_filter:
        return list(rows)
    wanted = set(row_filter)
    return [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]


class _RowCategoryFailed(Exception):
    """Internal signal from _run_row: a category's dispatch raised partway
    through the row, but earlier categories in the same row already
    finished. Carries whatever category_results were collected before the
    failure so run_suite can persist that partial (possibly API-billed)
    work instead of discarding it along with the exception."""

    def __init__(self, message: str, *, partial_results: dict[str, object]) -> None:
        super().__init__(message)
        self.partial_results = partial_results


def _run_row(
    row: RowConfig,
    categories: list[Category],
    suite: Suite,
    registry: Registry,
    judge_transport: JudgeTransport,
) -> dict[str, object]:
    model = registry.model(row.model_id)
    base_url, model_name, api_key = resolve_row_endpoint(row, model.model_name, suite.routes_direct)
    row_transport = LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=model_name)

    results: dict[str, object] = {}
    for category in _row_categories(row, categories):
        try:
            if category.name == CODING_CATEGORY:
                dataset = suite.coding.dataset or category.coding_dataset or DEFAULT_CODING_DATASET
                limit = suite.coding.limit if suite.coding.limit is not None else category.coding_limit
                results[category.name] = evalplus_runner.run_coding_category(
                    base_url=base_url, model=model_name, api_key=api_key, dataset=dataset, limit=limit
                )
            else:
                results[category.name] = judged_runner.run_judged_category(
                    category,
                    row_transport,
                    judge_transport,
                    temperature=0.0,
                    judge_temperature=suite.judge.temperature,
                    judge_samples=suite.judge.samples,
                    judge_max_attempts=suite.judge.max_attempts,
                )
        except Exception as exc:
            raise _RowCategoryFailed(str(exc), partial_results=results) from exc
    return results


def run_suite(
    suite: Suite,
    registry: Registry,
    categories: list[Category],
    *,
    row_filter: list[str] | None = None,
    results_dir: Path | None = None,
    judge_transport_factory=None,
) -> tuple[Path, list[RowRunResult]]:
    preflight(suite, registry)
    rows = _select_rows(suite.rows, row_filter)

    results_dir = results_dir or DEFAULT_RESULTS_DIR
    run_id = "eval-" + datetime.now(UTC).strftime("%Y%m%d-%H%M%S")
    run_dir = results_dir / run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    judge_transport = (judge_transport_factory or _default_judge_transport_factory)(suite.judge)

    results: list[RowRunResult] = []
    index = 0
    isolated_any = False
    restore_error: str | None = None
    # Everything from isolation through the row loop is wrapped in a
    # try/finally: a failed restore, or any per-row failure that somehow
    # isn't caught by the narrower try/except below, must never skip the
    # persist step and lose a whole sweep's already-collected results.
    # Matches the design spec's "restore providers in a finally block...
    # a failed restore never costs already-collected results" requirement.
    try:
        for provider_id, group in itertools.groupby(
            sorted(rows, key=lambda r: (r.provider_id, r.model_id)), key=lambda r: r.provider_id
        ):
            prev_extra: tuple[str, ...] | None = None
            for row in group:
                try:
                    extra_args = _isolation_extra_args(row)
                except BenchmarkError as exc:
                    index += 1
                    results.append(
                        RowRunResult(row=row, row_dir=_row_dir(run_dir, index, row), error=str(exc))
                    )
                    continue

                if provider_id in ISOLATABLE_PROVIDERS and extra_args != prev_extra:
                    try:
                        isolation.isolate_provider(provider_id, *extra_args)
                        isolated_any = True
                    except BenchmarkError as exc2:
                        index += 1
                        results.append(
                            RowRunResult(
                                row=row, row_dir=_row_dir(run_dir, index, row), error=str(exc2)
                            )
                        )
                        continue
                    prev_extra = extra_args

                index += 1
                row_dir = _row_dir(run_dir, index, row)
                try:
                    category_results = _run_row(row, categories, suite, registry, judge_transport)
                    results.append(
                        RowRunResult(row=row, row_dir=row_dir, category_results=category_results)
                    )
                except _RowCategoryFailed as exc3:
                    # A category raised partway through the row (e.g. EvalPlus's
                    # 1800s subprocess timeout, or judge_core.JudgeTransportError
                    # from judged_runner) — _run_row already collected whatever
                    # categories finished first; keep them (report.py/
                    # write_row_artifacts persists both the completed categories
                    # and the error) instead of discarding a row's real, possibly
                    # API-billed, work just because a later category failed.
                    results.append(
                        RowRunResult(
                            row=row,
                            row_dir=row_dir,
                            category_results=exc3.partial_results,
                            error=str(exc3),
                        )
                    )
                except Exception as exc3:
                    # Everything else — e.g. registry.model()/resolve_row_endpoint()
                    # itself raising before any category ran. Broadened from
                    # `except BenchmarkError`: escaping uncaught here would skip
                    # restore_providers() below and every persist call, discarding
                    # a whole sweep's results and leaving a local provider
                    # isolated/stopped.
                    results.append(RowRunResult(row=row, row_dir=row_dir, error=str(exc3)))
    finally:
        if isolated_any:
            try:
                isolation.restore_providers()
            except BenchmarkError as exc:
                restore_error = str(exc)

        for result in results:
            report.write_row_artifacts(result)
        (run_dir / "summary.md").write_text(
            report.render_summary(run_id, results, registry, [c.name for c in categories]),
            encoding="utf-8",
        )
        report.write_metrics_jsonl(run_dir / "metrics.jsonl", results)
        report.write_run_toml(run_dir / "run.toml", suite, git_sha=_git_sha())

    if restore_error is not None:
        raise RunSavedButRestoreFailed(
            f"providers failed to restore after the run (all results were saved to {run_dir}): "
            f"{restore_error}",
            run_dir=run_dir,
            results=results,
        )
    return run_dir, results


def _judge_config_from_run_toml(run_dir: Path, samples_override: int | None) -> JudgeConfig:
    with (run_dir / "run.toml").open("rb") as f:
        run_data = tomllib.load(f)
    judge_raw = run_data["suite"]["judge"]
    return JudgeConfig(
        model=judge_raw["model"],
        temperature=judge_raw["temperature"],
        samples=samples_override if samples_override is not None else judge_raw["samples"],
        max_attempts=judge_raw["max_attempts"],
        route=judge_raw["route"],
    )


def _read_metrics_row_meta(run_dir: Path) -> dict[str, dict]:
    """label -> the row's persisted metrics.jsonl record (model_id/route/
    error). This is the only place a post-rejudge reconstruction can recover
    those fields — they were never written per-row-directory, only into the
    run's aggregate metrics.jsonl by the original run_suite call."""
    metrics_path = run_dir / "metrics.jsonl"
    meta: dict[str, dict] = {}
    if not metrics_path.is_file():
        return meta
    for line in metrics_path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        row = json.loads(line)
        meta[row["label"]] = row
    return meta


def _reconstruct_category_result(category_dir: Path) -> object | None:
    """Rebuild a CodingResult or CategoryRowResult from one row's on-disk
    category directory, reading whatever judge.json/score.json currently
    say (i.e. post-rejudge for anything rejudge_run just rewrote)."""
    coding_path = category_dir / "evalplus_result.json"
    if coding_path.is_file():
        data = json.loads(coding_path.read_text(encoding="utf-8"))
        return evalplus_runner.CodingResult(**data)

    item_results: list[judged_runner.ItemResult] = []
    for item_dir in sorted(p for p in category_dir.iterdir() if p.is_dir()):
        response_path = item_dir / "response.txt"
        judge_path = item_dir / "judge.json"
        if not response_path.is_file() or not judge_path.is_file():
            continue
        response_text = response_path.read_text(encoding="utf-8")
        judge_data = json.loads(judge_path.read_text(encoding="utf-8"))
        combined_data = judge_data.get("combined")
        combined = JudgeScore(**combined_data) if combined_data else None
        outcome = JudgeOutcome(
            status=judge_data["status"],
            samples=[],
            combined=combined,
            attempts_used=judge_data.get("attempts_used", 0),
            error=judge_data.get("error"),
        )
        score = (
            float(combined.total) if outcome.status == "scored" and combined is not None else None
        )
        item_results.append(
            judged_runner.ItemResult(
                item_id=item_dir.name, response_text=response_text, judge=outcome, score_100=score
            )
        )
    if not item_results:
        return None
    scored = [r.score_100 for r in item_results if r.score_100 is not None]
    score_100 = round(mean(scored), 2) if scored else None
    return judged_runner.CategoryRowResult(
        category=category_dir.name, items=item_results, score_100=score_100
    )


def _row_label_from_dir_name(dir_name: str) -> str:
    """Invert _row_dir's f"{index:02d}--{row.label}" naming: strip only the
    leading index segment, on the FIRST "--", so a row.label that itself
    contains "--" (the common case — suite.py's auto-generated labels are
    "NN--model--route") round-trips intact. Falls back to the whole
    directory name if it somehow contains no "--" at all."""
    _, _, label = dir_name.partition("--")
    return label or dir_name


def _reconstruct_run_results(run_dir: Path) -> list[RowRunResult]:
    """Rebuild the whole run's RowRunResult list from on-disk artifacts —
    used after rejudge_run rewrites judge.json files, so summary.md/
    metrics.jsonl/score.json can be regenerated to match instead of going
    stale. row.model_id/route come back from metrics.jsonl (see
    _read_metrics_row_meta); provider_id is not recoverable and is left
    blank since report.py never reads it.

    metrics.jsonl is keyed by row.label (what write_metrics_jsonl writes),
    not by the row's directory name — _row_dir prepends its own index
    prefix on top of a label that (for an auto-generated label) already
    starts with one, so the directory name and the label are NOT the same
    string. Recover the label via _row_label_from_dir_name before looking
    it up, or every row falls back to a garbage model_id/blank route."""
    meta = _read_metrics_row_meta(run_dir)
    results: list[RowRunResult] = []
    for row_dir in sorted(p for p in run_dir.iterdir() if p.is_dir()):
        recovered_label = _row_label_from_dir_name(row_dir.name)
        row_meta = meta.get(recovered_label, {})
        row = RowConfig(
            # Prefer metrics.jsonl's own persisted label (stable across a
            # rejudge, matches what the original run rendered) over the
            # recovered one, which is only a fallback for a row with no
            # metrics.jsonl entry at all.
            label=row_meta.get("label", recovered_label),
            model_id=row_meta.get("model_id", row_dir.name),
            route=row_meta.get("route", ""),
            provider_id="",
        )
        error_path = row_dir / "error.txt"
        if error_path.is_file():
            results.append(
                RowRunResult(row=row, row_dir=row_dir, error=error_path.read_text(encoding="utf-8"))
            )
            continue
        category_results: dict[str, object] = {}
        for category_dir in sorted(p for p in row_dir.iterdir() if p.is_dir()):
            result = _reconstruct_category_result(category_dir)
            if result is not None:
                category_results[category_dir.name] = result
        results.append(RowRunResult(row=row, row_dir=row_dir, category_results=category_results))
    return results


def rejudge_run(
    run_dir: Path,
    categories: list[Category],
    *,
    row_filter: list[str] | None = None,
    samples_override: int | None = None,
    judge_transport_factory=None,
    registry: Registry | None = None,
) -> list[dict]:
    """Re-score every judged-category item from its persisted response.txt,
    without regenerating anything. `coding` has nothing to re-judge —
    EvalPlus's pass@1 is deterministic and not touched here. There is no
    Suite in hand at rejudge time — the judge config comes back from the
    run's own persisted run.toml (Task 8's write_run_toml).

    After rescoring, recomputes every rescored category's score.json (mean
    of its items' fresh judge.json totals) and re-renders the whole run's
    summary.md/metrics.jsonl from current on-disk state, via
    _reconstruct_run_results — otherwise `eval show --latest` would keep
    displaying the old, pre-rejudge numbers with no indication anything had
    gone stale, which is the worst failure mode for a benchmark tool.
    `registry` is optional (unknown at rejudge time in general) — without
    one, the capability matrix's family column falls back to the raw
    model_id, same as report.py's own registry-miss fallback."""
    by_name = {c.name: c for c in categories}
    judge_cfg = _judge_config_from_run_toml(run_dir, samples_override)
    transport = (judge_transport_factory or _default_judge_transport_factory)(judge_cfg)

    outcomes: list[dict] = []
    for row_dir in sorted(p for p in run_dir.iterdir() if p.is_dir()):
        if row_filter and row_dir.name not in row_filter:
            continue
        for category_dir in sorted(p for p in row_dir.iterdir() if p.is_dir()):
            category = by_name.get(category_dir.name)
            if category is None or category.rubric is None:
                continue  # coding, or a category this rejudge call wasn't given
            for item_dir in sorted(p for p in category_dir.iterdir() if p.is_dir()):
                response_path = item_dir / "response.txt"
                if not response_path.is_file():
                    continue
                item = next((i for i in category.items if i.id == item_dir.name), None)
                if item is None:
                    continue
                response_text = response_path.read_text(encoding="utf-8")
                prompt = judged_runner._build_judge_prompt(item, category, response_text)
                outcome = judge_row(
                    transport,
                    prompt,
                    category.rubric,
                    temperature=judge_cfg.temperature,
                    samples=judge_cfg.samples,
                    max_attempts=judge_cfg.max_attempts,
                )
                (item_dir / "judge.json").write_text(
                    json.dumps(asdict(outcome), indent=2), encoding="utf-8"
                )
                outcomes.append(
                    {
                        "row": row_dir.name,
                        "category": category.name,
                        "item": item.id,
                        "total": outcome.combined.total if outcome.combined else None,
                    }
                )

    # Recompute every category's score.json from the judge.json files just
    # rewritten above (plus untouched categories' existing files), then
    # re-render summary.md/metrics.jsonl for the whole run so `eval show`
    # reflects the rejudge instead of silently serving stale numbers.
    results = _reconstruct_run_results(run_dir)
    for result in results:
        for name, cat_result in result.category_results.items():
            if isinstance(cat_result, judged_runner.CategoryRowResult):
                (result.row_dir / name / "score.json").write_text(
                    json.dumps({"score_100": cat_result.score_100}), encoding="utf-8"
                )
    run_id = run_dir.name
    category_names = [c.name for c in categories]
    (run_dir / "summary.md").write_text(
        report.render_summary(run_id, results, registry or Registry(), category_names),
        encoding="utf-8",
    )
    report.write_metrics_jsonl(run_dir / "metrics.jsonl", results)
    return outcomes
