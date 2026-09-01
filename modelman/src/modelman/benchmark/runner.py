"""Benchmark runner orchestration."""

from __future__ import annotations

import time
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

import requests

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.isolation import isolate_provider, restore_providers
from modelman.benchmark.results import BenchmarkRun, TargetResult, write_results
from modelman.benchmark.workloads import Workload
from modelman.benchmark.workloads.base import BenchmarkMetrics
from modelman.registry import DEFAULT_PROVIDER_IDS, Registry
from modelman.state import StateStore

# Providers `modelman benchmark` can isolate+run locally. Derived from the
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


def discover_targets(
    registry: Registry,
    state: StateStore,
    model_ids: list[str] | None = None,
    family: str | None = None,
) -> list[Target]:
    """Return benchmark targets based on registry + state + CLI filters."""
    targets: list[Target] = []
    for model in registry.models:
        if model.provider_id not in LOCAL_PROVIDERS:
            continue
        if family is not None and model.family != family:
            continue
        if model_ids is not None and model.id not in model_ids:
            continue
        if model_ids is None and family is None and not state.get(model.id).litellm_exposed:
            # Default: only exposed local models.
            continue
        targets.append(
            Target(
                model_id=model.id,
                provider_id=model.provider_id,
                model_name=model.model_name,
                family=model.family,
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


def run_benchmark(
    registry: Registry,
    state: StateStore,
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

    targets = discover_targets(registry, state, model_ids=model_ids, family=family)
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
    # Isolation is provider-scoped: consecutive targets on the same provider
    # reuse the running isolation instead of paying another stop-others +
    # warmup cycle (~a minute of polling per target otherwise).
    last_provider: str | None = None
    direct_url: str | None = None
    try:
        for target in targets:
            try:
                if target.provider_id != last_provider:
                    isolate = isolate_provider(target.provider_id)
                    if not isolate.ok:
                        raise BenchmarkError(
                            f"failed to isolate provider {target.provider_id}: "
                            f"{isolate.error or 'unknown error'}"
                        )
                    direct_url = isolate.direct_url
                    last_provider = target.provider_id
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
            for pass_number in range(1, passes + 1):
                for route in routes:
                    url = direct_url if route == "direct" else LITELLM_URL
                    result = _run_route(session, target, route, url, workload, pass_number)
                    run.results.append(result)
                if pass_number < passes:
                    time.sleep(cooldown_seconds)
    finally:
        restore_providers()

    write_results(run, results_dir)
    return run
