"""Benchmark runner orchestration."""

from __future__ import annotations

import time
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

import requests

from llmbench.benchmark.errors import BenchmarkError
from llmbench.benchmark.isolation import (
    isolate_provider,
    mlx_lm_server_pairing_args,
    restore_providers,
)
from llmbench.benchmark.results import BenchmarkRun, TargetResult, write_results
from llmbench.benchmark.workloads import Workload
from llmbench.benchmark.workloads.base import BenchmarkMetrics
from llmbench.local_process import ENV_VAR_BY_PROVIDER
from llmbench.providers.registry import ProviderRegistry
from llmbench.providers.secret import SecretError, provider_key
from llmbench.registry import DEFAULT_PROVIDER_IDS, Registry, is_model_local


class WorkloadRunSavedButRestoreFailed(BenchmarkError):
    """Every row completed and is on disk; only putting the backends back failed.

    Distinct from errors.RunSavedButRestoreFailed (the agent/eval benchmarks'
    list-of-rows variant): this one carries the single workload `run`, and
    sharing the name made it easy to catch or import the wrong class.

    Carries `run_dir` and the completed `run` so the CLI can still record the
    `--latest` pointer and report the row count. Without it, a host whose
    provider restore fails turns a finished, fully persisted sweep into an exit
    code with nothing to show for it, and `show-results --latest` has no idea
    the run ever happened."""

    def __init__(self, message: str, *, run_dir: Path, run: BenchmarkRun) -> None:
        super().__init__(message)
        self.run_dir = run_dir
        self.run = run


class NoTargetSelection(ValueError):
    """Neither model ids nor a family was given (#179): a usage error, so the
    CLI exits 2. A ValueError subclass so the CLI catches exactly this and not
    an unrelated ValueError escaping a benchmark run."""


# Providers `llmbench` can isolate+run locally. Derived from the
# registry's canonical provider-id tuple so the set can't drift from it.
LOCAL_PROVIDERS = set(DEFAULT_PROVIDER_IDS)
LITELLM_URL = "http://localhost:4000/v1/chat/completions"
DEFAULT_RESULTS_DIR = Path.home() / ".config" / "local-ai" / "benchmarks"


@dataclass
class Target:
    model_id: str
    provider_id: str
    model_name: str
    family: str
    # Only populated (and only meaningful) for provider_id == "mlx_lm_server":
    # mlx_lm_server has no baked-in default target/draft pairing (unlike
    # ollama/omlx), so the pairing must be resolved from the registry here
    # and threaded through to isolate_provider() on every isolate call.
    repo: str | None = None
    local_path: str | None = None
    draft_repo: str | None = None
    draft_local_path: str | None = None


def discover_targets(
    registry: Registry,
    model_ids: list[str] | None = None,
    family: str | None = None,
) -> list[Target]:
    """Return benchmark targets based on the registry and the CLI filters.

    There is no default selection (#179): routing no longer says which local
    models are "in use", so the caller must name models or a family.
    """
    if model_ids is None and family is None:
        raise NoTargetSelection("name models (--model) or pass --family")
    targets: list[Target] = []
    for model in registry.models:
        if ProviderRegistry.resolve(model.provider_id) not in LOCAL_PROVIDERS:
            continue
        # Cloud models on a local provider (ollama `:cloud`) aren't local
        # benchmark targets; only an explicit --model selects one.
        if model_ids is None and not is_model_local(
            model.location, model.provider_id, registry, missing_provider_is_local=True
        ):
            continue
        if family is not None and model.family != family:
            continue
        if model_ids is not None and model.id not in model_ids:
            continue
        targets.append(
            Target(
                model_id=model.id,
                provider_id=model.provider_id,
                model_name=model.model_name,
                family=model.family,
                repo=model.fetch.repo if model.fetch else None,
                local_path=model.fetch.local_path if model.fetch else None,
                draft_repo=model.draft.repo if model.draft else None,
                draft_local_path=model.draft.local_path if model.draft else None,
            )
        )
    return targets


def _run_route(
    session: requests.Session,
    target: Target,
    route: str,
    url: str,
    workload: Workload,
    pass_number: int,
) -> TargetResult:
    payload = workload.build_payload(target.model_name if route == "direct" else target.model_id)
    raw = workload.run(session, url, payload)
    metrics = workload.metrics(raw)
    return TargetResult(
        model_id=target.model_id,
        provider_id=target.provider_id,
        route=route,
        pass_number=pass_number,
        metrics=metrics,
        error=raw.error,
    )


def _direct_session(
    provider_id: str, plain: requests.Session, keyed: dict[str, requests.Session]
) -> requests.Session:
    """The session for `provider_id`'s direct route: `plain` when its
    registry row names no key, else one session per provider carrying
    `Authorization: Bearer <key>` (resolved once per run). Raises
    SecretError (naming the provider, never the value) when the key cannot
    be read."""
    if provider_id in keyed:
        return keyed[provider_id]
    key = provider_key(provider_id)
    if key is None:
        return plain
    keyed_session = requests.Session()
    keyed_session.headers["Authorization"] = f"Bearer {key}"
    keyed[provider_id] = keyed_session
    return keyed_session


def _error_result(target: Target, route: str, pass_number: int, error: str) -> TargetResult:
    return TargetResult(
        model_id=target.model_id,
        provider_id=target.provider_id,
        route=route,
        pass_number=pass_number,
        metrics=BenchmarkMetrics(
            ttft_ms=None, total_ms=0, completion_tokens=None, prompt_tokens=None
        ),
        error=error,
    )


def run_benchmark(
    registry: Registry,
    workload: Workload,
    *,
    model_ids: list[str] | None = None,
    family: str | None = None,
    passes: int = 1,
    cooldown_seconds: float = 15.0,
    routes: list[str] | None = None,
    results_dir: Path | None = None,
) -> BenchmarkRun:
    """Run a benchmark across targets and write results."""
    routes = routes or ["direct", "litellm"]
    results_dir = results_dir or DEFAULT_RESULTS_DIR

    targets = discover_targets(registry, model_ids=model_ids, family=family)
    if not targets:
        raise BenchmarkError("no benchmark targets found")

    run_id = datetime.now(UTC).strftime("%Y%m%d-%H%M%S")
    run = BenchmarkRun(
        run_id=run_id,
        workload_name=workload.spec.name,
        started_at=datetime.now(UTC),
        results=[],
    )

    session = requests.Session()
    keyed_sessions: dict[str, requests.Session] = {}
    # Isolation is keyed on (provider_id, positional args, warmed model):
    # consecutive targets on the same key reuse the running isolation instead
    # of paying another stop-others + warmup cycle (~a minute of polling per
    # target otherwise). A target with a different model always re-isolates —
    # mtplx and mlx_lm_server serve one model per process, and on ollama and
    # omlx the new isolation unloads the previous target's model, so each
    # target runs with only its own model loaded.
    last_isolation_key: tuple[str, tuple[str, ...], str | None] | None = None
    direct_url: str | None = None
    try:
        for target in targets:
            try:
                # Resolve provider-specific positional args inline. ollama and
                # omlx take none: they name their model through the env dict
                # below, or the backend warms its baked-in default instead.
                extra_args: tuple[str, ...]
                if target.provider_id == "mlx_lm_server":
                    extra_args = mlx_lm_server_pairing_args(
                        target.model_id,
                        target.local_path,
                        target.repo,
                        target.draft_local_path,
                        target.draft_repo,
                    )
                elif target.provider_id == "mtplx":
                    # MTPLX is single-model-per-process; pass the repo id so
                    # isolation starts the requested model.
                    extra_args = (target.model_name,)
                else:
                    extra_args = ()
                env_var = ENV_VAR_BY_PROVIDER.get(target.provider_id)
                warm_name = target.model_name if env_var else None
                isolation_key = (target.provider_id, extra_args, warm_name)
                if isolation_key != last_isolation_key:
                    isolate = (
                        isolate_provider(
                            target.provider_id, *extra_args, env={env_var: target.model_name}
                        )
                        if env_var
                        else isolate_provider(target.provider_id, *extra_args)
                    )
                    direct_url = isolate.direct_url
                    last_isolation_key = isolation_key
            except BenchmarkError as exc:
                for route in routes:
                    for pass_number in range(1, passes + 1):
                        run.results.append(
                            TargetResult(
                                model_id=target.model_id,
                                provider_id=target.provider_id,
                                route=route,
                                pass_number=pass_number,
                                metrics=BenchmarkMetrics(
                                    ttft_ms=None,
                                    total_ms=0,
                                    completion_tokens=None,
                                    prompt_tokens=None,
                                ),
                                error=str(exc),
                            )
                        )
                continue

            # Reached only when isolation succeeded (fresh or reused), so
            # direct_url was assigned either above or on a prior iteration.
            assert direct_url is not None
            # The direct route of a keyed provider (rec refuses every route
            # without its bearer key) goes through a session of its own that
            # carries the key; every other request — key-less providers, and
            # every litellm route — stays on the plain, header-free session.
            direct_session = session
            key_error: str | None = None
            if "direct" in routes:
                try:
                    direct_session = _direct_session(target.provider_id, session, keyed_sessions)
                except SecretError as exc:
                    key_error = str(exc)
            for pass_number in range(1, passes + 1):
                for route in routes:
                    if route == "direct" and key_error is not None:
                        run.results.append(_error_result(target, route, pass_number, key_error))
                        continue
                    url = direct_url if route == "direct" else LITELLM_URL
                    route_session = direct_session if route == "direct" else session
                    result = _run_route(route_session, target, route, url, workload, pass_number)
                    run.results.append(result)
                if pass_number < passes:
                    time.sleep(cooldown_seconds)
    finally:
        for keyed in keyed_sessions.values():
            keyed.close()
        restore_error: str | None = None
        try:
            restore_providers()
        except Exception as exc:
            restore_error = str(exc)

    write_results(run, results_dir)

    if restore_error is not None:
        raise WorkloadRunSavedButRestoreFailed(
            f"providers failed to restore after the run (all results were saved "
            f"to {results_dir / run.run_id}): {restore_error}",
            run_dir=results_dir / run.run_id,
            run=run,
        )
    return run
