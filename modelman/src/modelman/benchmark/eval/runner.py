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
import sys
import time
import tomllib
from dataclasses import asdict, dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from statistics import mean

from modelman.benchmark import isolation
from modelman.benchmark._routes import (
    LITELLM_PLIST,
    LIVE_PI_MODELS_PATH,
    OPENROUTER_BASE_URL,
    litellm_credentials,
    openrouter_key,
)
from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval import evalplus_runner, judged_runner, report
from modelman.benchmark.eval.category import CODING_CATEGORY, Category
from modelman.benchmark.eval.suite import (
    JudgeConfig,
    RowConfig,
    Suite,
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
from modelman.local_process import ENV_VAR_BY_PROVIDER as _ENV_VAR_BY_PROVIDER
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


def _row_index(row: RowConfig) -> int:
    """The row's 1-based SUITE position (stashed on the RowConfig when the
    suite was loaded/selected) — the stable identity a row's directory is
    numbered by, independent of execution order (rows execute sorted by
    (provider, model) for isolation grouping, which on any multi-provider
    suite differs from suite order)."""
    return row.suite_index


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
    base_url, api_key = litellm_credentials(LIVE_PI_MODELS_PATH)
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


def _row_isolation_spec(
    row: RowConfig, registry: Registry
) -> tuple[tuple[str, ...], dict[str, str] | None] | None:
    """(extra_args, env) for isolating `row`'s provider with the ROW'S model
    loaded, or None when the row needs no isolation at all.

    Mirrors `modelman start`'s in-process pattern (local_control.py):
    mlx_lm_server/mtplx take positional model args, ollama/omlx name their
    model through the LLM_ISOLATE_*_MODEL env dict — isolate_provider's only
    channel for them, since their backends' resolve() reads extra_args as
    absent. Without it, warmup loads the backend's baked-in default (ornith)
    instead of the row's model: direct-route omlx rows then 404 against the
    still-loaded default (single-model-per-daemon), and ollama rows lazily
    stack the row's model on top of the warmed default, breaking the
    one-model-at-a-time isolation invariant this call exists to enforce.

    The warm name must be the name the row's own requests will use, so the
    warmed weights and the benchmarked weights agree: `direct_model` when the
    row sets one (omlx rows need the basename the daemon serves, not the
    org-prefixed registry model_name), else the registry model_name (ollama
    tags, omlx via litellm — both spellings live-verified against the oMLX
    daemon 2026-09-18).

    None = a cloud model on a local provider's id (e.g. an `ollama/*:cloud`
    registry row): its requests go to a remote API, so it contends with
    nothing on this machine — and naming a model would make warmup POST a
    cloud model name at the local daemon and fail after the 600s timeout.
    Skip isolation for it, the same cloud-row stance the agent benchmark
    takes."""
    if row.provider_id == "mlx_lm_server":
        return (
            isolation.mlx_lm_server_pairing_args(
                row.model_id,
                row.target_local_path,
                row.target_repo,
                row.draft_local_path,
                row.draft_repo,
            ),
            None,
        )
    if row.provider_id == "mtplx":
        assert row.mtplx_model_name is not None
        return (row.mtplx_model_name,), None
    if row.provider_id in _ENV_VAR_BY_PROVIDER:
        try:
            entry = registry.model(row.model_id)
        except KeyError:
            # Unreachable in practice (suite load/preflight already resolved
            # the model), but an isolate call must never be what crashes the
            # sweep — fall back to the provider's default-warm behavior.
            return (), None
        if entry.location == "cloud":
            return None
        warm_name = row.direct_model or entry.model_name
        return (), {_ENV_VAR_BY_PROVIDER[row.provider_id]: warm_name}
    return (), None


def _select_rows(rows: list[RowConfig], row_filter: list[str] | None) -> list[RowConfig]:
    # Stash each row's 1-based SUITE position on the RowConfig so run_suite
    # can number its row directories by it (see _row_index): rows execute
    # sorted by (provider, model) for isolation grouping, so on any
    # multi-provider suite the execution order differs from suite order —
    # numbering dirs by suite position is what makes `judge --row N` resolve
    # to the same row `run --row N` selected.
    for i, row in enumerate(rows, start=1):
        row.suite_index = i
    if not row_filter:
        return list(rows)
    wanted = set(row_filter)
    selected = [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]
    if not selected:
        # A --row value matching nothing must fail loudly (BenchmarkError →
        # run_cmd exits 1): a silent [] would still create a run dir, write
        # empty summary.md/metrics.jsonl/run.toml, and repoint eval_last_run
        # at an empty run — the same silent-empty failure mode the rejudge
        # selector was already fixed to raise on.
        known = [r.label for r in rows]
        raise BenchmarkError(
            f"--row {', '.join(sorted(wanted))} matched no suite rows (known: {', '.join(known)})"
        )
    return selected


def _select_row_dirs(row_dirs: list[Path], row_filter: list[str]) -> list[Path]:
    """Match rejudge's --row against row directories the way `run --row`
    matches suite rows: basename, label (the basename minus its "NN--"
    index prefix), or a numeric index parsed from the basename's OWN
    zero-padded NN-- prefix — which is the row's SUITE position (that is
    what _row_dir numbers dirs by), so `judge --row N` resolves to the
    same row `run --row N` selected even when execution order differs
    from suite order (multi-provider suites) and even when a filtered
    run left gaps in the dir numbering (run --row 1,3 → dirs 01--/03--).
    A value that matches nothing raises — a silent empty rejudge would
    rewrite summary.md/metrics.jsonl and exit 0 while re-judging nothing."""
    if not row_filter:
        return list(row_dirs)
    selected: list[Path] = []
    known = [d.name for d in row_dirs]
    for value in row_filter:
        value_matched = False
        for row_dir in row_dirs:
            dir_index = _row_index_from_dir_name(row_dir.name)
            if (
                row_dir.name == value
                or _row_label_from_dir_name(row_dir.name) == value
                or (value.isdigit() and dir_index is not None and dir_index == int(value))
            ):
                if row_dir not in selected:
                    selected.append(row_dir)
                value_matched = True
        if not value_matched:
            raise BenchmarkError(
                f"--row {value!r} matched no row directories in this run "
                f"(known: {', '.join(known)})"
            )
    return selected


class _RowCategoryFailed(Exception):
    """Internal signal from _run_row: a category's dispatch raised partway
    through the row, but earlier categories in the same row already
    finished. Carries whatever category_results were collected before the
    failure so run_suite can persist that partial (possibly API-billed)
    work instead of discarding it along with the exception."""

    def __init__(self, message: str, *, partial_results: dict[str, object]) -> None:
        super().__init__(message)
        self.partial_results = partial_results


def _judge_all(
    results: list[RowRunResult],
    categories: list[Category],
    judge_transport: JudgeTransport,
    *,
    judge_temperature: float,
    judge_samples: int,
    judge_max_attempts: int,
) -> None:
    """Phase 2. Runs after restore_providers(): judging is a cloud call and
    must not hold the exclusively-isolated local model in RAM while it
    works (agent/runner.py's _judge_all ordering rule, made explicit in
    the design spec's step 4). Judge failures here are recorded per-item
    as judge_fail outcomes — never raised — so one flaky judge round-trip
    degrades that item to N/A instead of aborting the sweep; every row's
    generation is already safe on disk from the finally block above."""
    by_name = {c.name: c for c in categories if c.rubric is not None}
    for result in results:
        # NOT skipped for error rows: a _RowCategoryFailed row keeps its
        # completed categories in category_results, and "a failure never
        # costs already-computed results" extends to judging them — only
        # the failed category is absent from the dict. (agent/runner.py's
        # _judge_all skips error rows, but there an error means no diff
        # exists to judge; here the partial responses are real.)
        for name, cat_result in result.category_results.items():
            category = by_name.get(name)
            if not isinstance(cat_result, judged_runner.CategoryRowResult) or category is None:
                continue
            try:
                judged_runner.judge_category(
                    category,
                    cat_result,
                    judge_transport,
                    judge_temperature=judge_temperature,
                    judge_samples=judge_samples,
                    judge_max_attempts=judge_max_attempts,
                )
            except Exception as exc:
                # A judge transport that raises through judge_category's
                # retry loop (e.g. JudgeTransportError after its internal
                # retry) marks the whole category judge_fail — this keeps
                # the sweep alive instead of one dead route aborting every
                # remaining row's scoring.
                cat_result.score_100 = None
                for item_result in cat_result.items:
                    if item_result.judge is None:
                        item_result.judge = JudgeOutcome(
                            status="judge_fail",
                            samples=[],
                            combined=None,
                            attempts_used=0,
                            error=str(exc),
                        )
                print(
                    f"warning: judge failed for {result.row.label}/{name}: {exc}", file=sys.stderr
                )


def _run_row(
    row: RowConfig,
    categories: list[Category],
    suite: Suite,
    registry: Registry,
) -> dict[str, object]:
    """Phase 1 for one row (under isolation): generation only, no judging.
    Judged categories come back UNJUDGED (judge=None) — run_suite scores
    them after restore_providers(), per the design spec's
    judge-after-generation ordering rule."""
    model = registry.model(row.model_id)
    base_url, model_name, api_key = resolve_row_endpoint(row, model.model_name, suite.routes_direct)
    row_transport = LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=model_name)

    results: dict[str, object] = {}
    for category in _row_categories(row, categories):
        try:
            if category.name == CODING_CATEGORY:
                dataset = suite.coding.dataset or category.coding_dataset or DEFAULT_CODING_DATASET
                limit = (
                    suite.coding.limit if suite.coding.limit is not None else category.coding_limit
                )
                results[category.name] = evalplus_runner.run_coding_category(
                    base_url=base_url,
                    model=model_name,
                    api_key=api_key,
                    dataset=dataset,
                    limit=limit,
                )
            else:
                results[category.name] = judged_runner.generate_category(
                    category, row_transport, temperature=0.0
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
    rows = _select_rows(suite.rows, row_filter)
    # A row whose own `categories` is disjoint from the runner's category
    # set (CLI --category scoping, or a --category value valid overall but
    # absent from that row's own list) must be SKIPPED with a notice, not
    # run with zero categories: running it would isolate a provider and
    # produce an empty row directory, an all-N/A matrix row with no
    # anomaly, and a metrics.jsonl line with "categories": {} — the exact
    # silent-empty failure mode the surrounding guards reject. This is
    # deliberately a skip, not a suite rejection: one row legitimately
    # scoped to coding does not block a --category reasoning run of the
    # rest of the suite.
    skipped: list[RowConfig] = [row for row in rows if not _row_categories(row, categories)]
    rows = [row for row in rows if _row_categories(row, categories)]
    for row in skipped:
        print(
            f"skipping row {row.label!r}: no categories overlap with the selection", file=sys.stderr
        )
    if not rows:
        raise BenchmarkError(
            "no rows remain after category scoping: every selected row's "
            "`categories` is disjoint from the requested category set"
        )
    # The judge transport hard-requires an API key (LiteLLM or OpenRouter);
    # build it only when at least one selected row will run a judged
    # category, so a coding-only EvalPlus run stays purely local.
    needs_judge = any(
        category.name != CODING_CATEGORY
        for row in rows
        for category in _row_categories(row, categories)
    )
    # Preflight the SELECTION, not the whole suite: a scoped run must not
    # be blocked by an unselected row's provider being down or its key
    # missing.
    preflight(suite, registry, rows=rows, judge_route_active=needs_judge)

    results_dir = results_dir or DEFAULT_RESULTS_DIR
    run_id = "eval-" + datetime.now(UTC).strftime("%Y%m%d-%H%M%S")
    run_dir = results_dir / run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    results: list[RowRunResult] = []
    isolated_any = False
    restore_error: str | None = None
    # Build the judge transport BEFORE generation (it was previously built
    # lazily right before _judge_all): its factory hard-fails on a missing
    # LiteLLM/OpenRouter key, and with judging now strictly post-restore a
    # lazy build would surface that only after paying for the whole
    # generation sweep. LiteLLMJudgeTransport.__init__ only stores strings
    # — no connection is made until the first judge call, well after
    # restore — so building early restores the fail-fast without
    # reintroducing the judge-before-restore ordering bug.
    judge_transport: JudgeTransport | None = None
    if needs_judge:
        judge_transport = (judge_transport_factory or _default_judge_transport_factory)(suite.judge)
    # Everything from isolation through the row loop is wrapped in a
    # try/finally: a failed restore, or any per-row failure that somehow
    # isn't caught by the narrower try/except below, must never skip the
    # restore+persist step and lose a whole sweep's already-collected
    # results. Matches the design spec's "restore providers in a finally
    # block... a failed restore never costs already-collected results"
    # requirement.
    try:
        for provider_id, group in itertools.groupby(
            sorted(rows, key=lambda r: (r.provider_id, r.model_id)), key=lambda r: r.provider_id
        ):
            # The isolation key is the full (extra_args, env) spec, not just
            # extra_args: two ollama/omlx rows on the same provider but
            # different models now carry different env dicts and must
            # re-isolate between them (the single-model-per-daemon case
            # mlx_lm_server has always had via its pairing args).
            prev_spec: tuple[tuple[str, ...], dict[str, str] | None] | None = None
            for row_position, row in enumerate(group):
                # Thermal settling between rows within a provider group
                # (agent/runner.py honors cooldown_s between passes the
                # same way); none before the first row — it runs right
                # after the group's isolation warmup, with nothing to
                # settle from.
                if row_position > 0:
                    time.sleep(suite.cooldown_s)
                try:
                    spec = _row_isolation_spec(row, registry)
                except BenchmarkError as exc:
                    results.append(
                        RowRunResult(
                            row=row, row_dir=_row_dir(run_dir, _row_index(row), row), error=str(exc)
                        )
                    )
                    continue

                # spec None = a cloud model on a local provider's id (e.g. an
                # ollama/*:cloud registry row): it hits a remote API through
                # LiteLLM, contends with nothing on this machine, and naming
                # a model would make warmup POST a cloud name at the local
                # daemon — skip isolation for it entirely, same cloud-row
                # stance the agent benchmark takes.
                if spec is not None and provider_id in ISOLATABLE_PROVIDERS and spec != prev_spec:
                    try:
                        isolation.isolate_provider(provider_id, *spec[0], env=spec[1])
                        isolated_any = True
                    except BenchmarkError as exc2:
                        results.append(
                            RowRunResult(
                                row=row,
                                row_dir=_row_dir(run_dir, _row_index(row), row),
                                error=str(exc2),
                            )
                        )
                        continue
                    prev_spec = spec

                row_dir = _row_dir(run_dir, _row_index(row), row)
                try:
                    category_results = _run_row(row, categories, suite, registry)
                    results.append(
                        RowRunResult(row=row, row_dir=row_dir, category_results=category_results)
                    )
                except _RowCategoryFailed as exc3:
                    # A category raised partway through the row (e.g. EvalPlus's
                    # 1800s subprocess timeout, or judge_core.JudgeTransportError
                    # from generate_category) — _run_row already collected
                    # whatever categories finished first; keep them (report.py/
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

        # Persist the full run snapshot BEFORE judging (agent/runner.py's
        # ordering): a crash or interrupt during the judge phase still
        # leaves every response on disk — judged categories render as
        # UNJUDGED, and run.toml (which rejudge_run reads its judge config
        # back from) exists, so `eval judge --run-id <id>` recovers the run
        # without regenerating anything. The post-judging persist below
        # rewrites the score-dependent artifacts with real scores.
        for result in results:
            report.write_row_artifacts(result)
        (run_dir / "summary.md").write_text(
            report.render_summary(run_id, results, registry, [c.name for c in categories]),
            encoding="utf-8",
        )
        report.write_metrics_jsonl(run_dir / "metrics.jsonl", results)
        report.write_run_toml(run_dir / "run.toml", suite, git_sha=_git_sha())

    # Phase 2 — judging, strictly AFTER restore_providers() (design spec
    # step 4): judge calls are cloud round-trips, and with the reference
    # judge config (samples=3, OpenRouter) a multi-item category holds the
    # exclusively-isolated local model pinned in RAM through minutes of
    # judge round-trips while its provider stays down/locked — the exact
    # interleaving the spec forbids. _run_row already left every judged
    # category UNJUDGED; score them now, in place, with the transport
    # built before generation (see the pre-generation comment above).
    if needs_judge:
        assert judge_transport is not None
        _judge_all(
            results,
            categories,
            judge_transport,
            judge_temperature=suite.judge.temperature,
            judge_samples=suite.judge.samples,
            judge_max_attempts=suite.judge.max_attempts,
        )
        # Rewrite the score-dependent artifacts with real scores (the
        # finally block's pre-judge snapshot wrote them as UNJUDGED). If
        # the judge phase raised, this rewrite is skipped — but the
        # finally block's snapshot (responses + UNJUDGED markers +
        # run.toml) is already a complete, rejudge-recoverable run, so the
        # exception propagates with nothing lost.
        for result in results:
            report.write_row_artifacts(result)
        (run_dir / "summary.md").write_text(
            report.render_summary(run_id, results, registry, [c.name for c in categories]),
            encoding="utf-8",
        )
        report.write_metrics_jsonl(run_dir / "metrics.jsonl", results)

    if restore_error is not None:
        raise RunSavedButRestoreFailed(
            f"providers failed to restore after the run (all results were saved to {run_dir}): "
            f"{restore_error}",
            run_dir=run_dir,
            results=results,
        )
    return run_dir, results


def _judge_config_from_run_toml(run_dir: Path, samples_override: int | None) -> JudgeConfig:
    # run.toml is user-editable on disk: a hand-edit or a merge-conflict
    # resolution can drop [suite.judge] or mangle the TOML, and that must
    # surface as the same clean BenchmarkError every other malformation in
    # the judge command path produces — judge_cmd catches only BenchmarkError
    # and FileNotFoundError, so a raw KeyError/TOMLDecodeError would escape
    # as a traceback.
    try:
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
    except (KeyError, TypeError, tomllib.TOMLDecodeError) as exc:
        raise BenchmarkError(
            f"run.toml in {run_dir} is missing or malformed around [suite.judge]: {exc}"
        ) from exc


def _read_metrics_row_meta(run_dir: Path) -> dict[str, dict]:
    """row-dir-basename -> the row's persisted metrics.jsonl record
    (model_id/route/error). This is the only place a post-rejudge
    reconstruction can recover those fields — they were never written
    per-row-directory, only into the run's aggregate metrics.jsonl by the
    original run_suite call.

    Records are keyed by the row_dir field write_metrics_jsonl now writes
    (unique per run). Older runs' metrics.jsonl have no row_dir; those
    lines fall back to the label key, which is NOT guaranteed unique —
    the label keying is exactly what allowed two same-labeled rows to
    overwrite each other's metadata."""
    metrics_path = run_dir / "metrics.jsonl"
    meta: dict[str, dict] = {}
    if not metrics_path.is_file():
        return meta
    for line in metrics_path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            # A run interrupted mid-write (or a hand-edited line) leaves a
            # partial line; skip it — the affected row just degrades to the
            # label-fallback keying (house convention: malformed lines are
            # skipped, not fatal, same as usage/wt_state).
            continue
        key = row.get("row_dir") or row.get("label")
        meta[key] = row
    return meta


def _load_json_artifact(path: Path) -> object | None:
    """Read a JSON artifact, tolerating malformed content: a run
    interrupted mid-write (or a hand-edited file) leaves a truncated JSON
    document, and one corrupted judge.json/evalplus_result.json anywhere
    in the run must not crash the whole rejudge with a raw JSONDecodeError
    (which judge_cmd doesn't catch). House convention from
    _read_metrics_row_meta: malformed artifacts are skipped with a warning
    to stderr, not fatal."""
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        print(f"warning: skipping malformed artifact: {path}", file=sys.stderr)
        return None


def _reconstruct_category_result(category_dir: Path) -> object | None:
    """Rebuild a CodingResult or CategoryRowResult from one row's on-disk
    category directory, reading whatever judge.json/score.json currently
    say (i.e. post-rejudge for anything rejudge_run just rewrote).

    Well-formed JSON whose shape no longer matches the dataclass (a
    hand-edited judge.json, or a file written by a newer/older modelman
    with different fields) is tolerated the same way malformed JSON is —
    a stderr warning and a skip — per _load_json_artifact's documented
    contract that one corrupted artifact must not crash the whole
    rejudge."""
    coding_path = category_dir / "evalplus_result.json"
    if coding_path.is_file():
        data = _load_json_artifact(coding_path)
        if not isinstance(data, dict):
            # None (malformed) or well-formed JSON of the wrong shape —
            # either way this category reconstructs as empty, not a crash.
            return None
        try:
            return evalplus_runner.CodingResult(**data)
        except TypeError as exc:
            # Unexpected/missing keys vs the current CodingResult fields.
            print(f"warning: skipping malformed artifact: {coding_path}: {exc}", file=sys.stderr)
            return None

    item_results: list[judged_runner.ItemResult] = []
    for item_dir in sorted(p for p in category_dir.iterdir() if p.is_dir()):
        response_path = item_dir / "response.txt"
        judge_path = item_dir / "judge.json"
        if not response_path.is_file():
            continue
        response_text = response_path.read_text(encoding="utf-8")
        # An item directory with response.txt but no judge.json is an
        # UNJUDGED item — a run whose judge phase was interrupted (see
        # run_suite's pre-judge snapshot) or an item whose judge.json was
        # removed. It still reconstructs (as judge=None) so summary.md
        # shows UNJUDGED rather than the row vanishing; rejudge_run will
        # score it from the response on this same pass.
        if not judge_path.is_file():
            item_results.append(
                judged_runner.ItemResult(
                    item_id=item_dir.name, response_text=response_text, judge=None, score_100=None
                )
            )
            continue
        judge_data = _load_json_artifact(judge_path)
        if judge_data is None:
            continue
        if not isinstance(judge_data, dict) or "status" not in judge_data:
            # Well-formed JSON but not a judge.json shape (e.g. someone
            # parked a list there); same tolerance as a malformed file.
            print(f"warning: skipping malformed artifact: {judge_path}", file=sys.stderr)
            continue
        combined_data = judge_data.get("combined")
        combined: JudgeScore | None = None
        if combined_data:
            try:
                combined = JudgeScore(**combined_data)
            except TypeError as exc:
                # Unexpected/missing keys vs the current JudgeScore fields —
                # skip the item's score rather than crashing the rejudge.
                print(f"warning: skipping malformed artifact: {judge_path}: {exc}", file=sys.stderr)
                continue
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


def _row_index_from_dir_name(dir_name: str) -> int | None:
    """The SUITE position a row directory was numbered by (the leading
    zero-padded NN in _row_dir's f"{index:02d}--{row.label}" name), or None
    when the name carries no numeric prefix (e.g. a hand-made or legacy
    directory). Derived from the directory's OWN name, not its position in
    a sorted listing — that keeps index selection correct when execution
    order differs from suite order and when a filtered run left gaps in
    the numbering (run --row 1,3 produces dirs 01-- and 03--)."""
    prefix = dir_name.split("--", 1)[0]
    return int(prefix) if prefix.isdigit() else None


def _reconstruct_run_results(run_dir: Path) -> list[RowRunResult]:
    """Rebuild the whole run's RowRunResult list from on-disk artifacts —
    used after rejudge_run rewrites judge.json files, so summary.md/
    metrics.jsonl/score.json can be regenerated to match instead of going
    stale. row.model_id/route come back from metrics.jsonl (see
    _read_metrics_row_meta); provider_id is not recoverable and is left
    blank since report.py never reads it.

    metrics.jsonl is keyed by the row's directory basename (the row_dir
    field write_metrics_jsonl writes), not by the row's label — _row_dir
    prepends an index prefix on top of a label that (for an auto-generated
    label) already starts with one, so the directory name and the label are
    NOT the same string, and the label is not guaranteed unique anyway.
    The directory basename needs no inversion, so it is looked up directly.

    A row with an error AND already-scored categories on disk (a later
    category failed after earlier ones finished) reconstructs BOTH: the
    error and every category whose artifacts survived — a failure never
    costs already-computed results."""
    meta = _read_metrics_row_meta(run_dir)
    results: list[RowRunResult] = []
    for row_dir in sorted(p for p in run_dir.iterdir() if p.is_dir()):
        # A row directory always holds error.txt and/or category
        # subdirectories — skip anything else (e.g. unrelated scratch dirs
        # that happen to live under a run dir), which would otherwise be
        # reconstructed as a garbage row with a directory-name model_id.
        has_categories = any(p.is_dir() for p in row_dir.iterdir())
        if not (row_dir / "error.txt").is_file() and not has_categories:
            continue
        row_meta = meta.get(row_dir.name) or meta.get(_row_label_from_dir_name(row_dir.name), {})
        row = RowConfig(
            # Prefer metrics.jsonl's own persisted label (stable across a
            # rejudge, matches what the original run rendered) over the
            # recovered one, which is only a fallback for a row with no
            # metrics.jsonl entry at all.
            label=row_meta.get("label", _row_label_from_dir_name(row_dir.name)),
            model_id=row_meta.get("model_id", row_dir.name),
            route=row_meta.get("route", ""),
            provider_id="",
        )
        error_path = row_dir / "error.txt"
        error_text = error_path.read_text(encoding="utf-8") if error_path.is_file() else None
        category_results: dict[str, object] = {}
        for category_dir in sorted(p for p in row_dir.iterdir() if p.is_dir()):
            result = _reconstruct_category_result(category_dir)
            if result is not None:
                category_results[category_dir.name] = result
        results.append(
            RowRunResult(
                row=row, row_dir=row_dir, category_results=category_results, error=error_text
            )
        )
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
    # Select rows BEFORE building the judge transport: a bad --row value
    # should surface as the clear "matched no row directories" error, not
    # as a missing-judge-key transport error the user has to look past.
    row_dirs = sorted(p for p in run_dir.iterdir() if p.is_dir())
    selected_row_dirs = _select_row_dirs(row_dirs, list(row_filter or []))
    judge_cfg = _judge_config_from_run_toml(run_dir, samples_override)
    transport = (judge_transport_factory or _default_judge_transport_factory)(judge_cfg)

    outcomes: list[dict] = []
    for row_dir in selected_row_dirs:
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
