"""Suite orchestration for `modelman benchmark eval`: group rows by
provider, isolate once per group, run every row's categories, restore.

Mirrors modelman.benchmark.agent.runner's isolation-grouping loop (same
(provider, extra_args) keying for mlx_lm_server's per-pairing re-isolation)
but with no pi process, no workspace, and no gates — each row's "run" is a
handful of direct HTTP calls per category instead of a full agent session.
"""

from __future__ import annotations

import itertools
import subprocess
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path

from modelman.benchmark import isolation
from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval import evalplus_runner, judged_runner, report
from modelman.benchmark.eval.category import CODING_CATEGORY, Category
from modelman.benchmark.eval.suite import (
    LITELLM_PLIST,
    LIVE_PI_MODELS_PATH,
    OPENROUTER_BASE_URL,
    RowConfig,
    Suite,
    load_live_models,
    openrouter_key,
    preflight,
    resolve_row_endpoint,
)
from modelman.benchmark.judge_core import JudgeTransport, LiteLLMJudgeTransport
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
            except BenchmarkError as exc3:
                results.append(RowRunResult(row=row, row_dir=row_dir, error=str(exc3)))

    restore_error: str | None = None
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
