from datetime import UTC, datetime
from pathlib import Path

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.results import BenchmarkMetrics, BenchmarkRun, TargetResult
from modelman.benchmark.runner import RunSavedButRestoreFailed, discover_targets, run_benchmark
from modelman.benchmark.workloads.base import WorkloadSpec
from modelman.registry import DraftSpec, Fetch, ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore


class _FakeWorkload:
    spec = WorkloadSpec(
        name="chat",
        display_name="Chat",
        prompt="hi",
        max_tokens=1,
        temperature=0.0,
        stream=True,
    )

    def build_payload(self, model_id):
        return {"model": model_id}

    def run(self, session, url, payload):
        raise AssertionError("workload.run must not be called (route is faked)")

    def metrics(self, raw):
        return BenchmarkMetrics(ttft_ms=1, total_ms=2, completion_tokens=3, prompt_tokens=4)


def test_run_benchmark_saves_results_when_restore_fails(tmp_path, monkeypatch):
    """A completed run must be written to disk even when restore_providers
    raises, and the failure must surface as RunSavedButRestoreFailed carrying
    the run dir and the completed run."""
    import modelman.benchmark.runner as runner_module

    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(litellm_exposed=True))

    def _fake_isolate(pid):
        return type("I", (), {"ok": True, "direct_url": "http://localhost:8080"})()

    def _fail_restore():
        raise BenchmarkError("llamacpp down")

    def _fake_route(session, target, route, url, workload, pass_number):
        return TargetResult(
            model_id=target.model_id,
            provider_id=target.provider_id,
            route=route,
            pass_number=pass_number,
            metrics=BenchmarkMetrics(ttft_ms=1, total_ms=2, completion_tokens=3, prompt_tokens=4),
            error=None,
        )

    monkeypatch.setattr(runner_module, "isolate_provider", _fake_isolate)
    monkeypatch.setattr(runner_module, "restore_providers", _fail_restore)
    monkeypatch.setattr(runner_module, "_run_route", _fake_route)

    with pytest.raises(RunSavedButRestoreFailed) as excinfo:
        run_benchmark(registry, state, _FakeWorkload(), results_dir=tmp_path)

    exc = excinfo.value
    assert exc.run_dir == tmp_path / exc.run.run_id
    assert (exc.run_dir / "results.json").exists()
    assert len(exc.run.results) == 2  # direct + litellm routes


def test_run_saved_but_restore_failed_carries_run_dir_and_run():
    """The exception must carry the surviving run dir and the completed run so
    the CLI can still record the --latest pointer after a restore failure."""
    run = BenchmarkRun(
        run_id="run-2026-01-01",
        workload_name="chat",
        started_at=datetime(2026, 1, 1, tzinfo=UTC),
        results=[],
    )
    exc = RunSavedButRestoreFailed("boom", run_dir=Path("/tmp/x"), run=run)
    assert exc.run_dir == Path("/tmp/x")
    assert exc.run is run
    assert isinstance(exc, BenchmarkError)


def test_discover_targets_defaults_to_exposed_local_models():
    """Exposed local models are benchmarked by default.

    Without explicit --model or --family filters, discover_targets should only
    return local models that are currently exposed through LiteLLM. Remote
    providers and unexposed local models must be skipped so the runner does not
    hit endpoints that are not configured.
    """
    registry = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local"),
            ProviderEntry(id="openrouter", name="OpenRouter", location="remote"),
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b"),
            ModelEntry(id="openrouter/c", family="f", provider_id="openrouter", model_name="c"),
        ],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(litellm_exposed=True))
    state.set("ollama/b", ModelState(litellm_exposed=False))

    targets = discover_targets(registry, state)
    assert [t.model_id for t in targets] == ["ollama/a"]


def test_discover_targets_by_family_overrides_exposed():
    """--family selects every model in that family regardless of expose state.

    When a family is explicitly requested, all local models in that family
    become targets; the LiteLLM-exposed gate is bypassed because the user has
    narrowed the scope intentionally.
    """
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="g", provider_id="ollama", model_name="b"),
        ],
    )
    state = StateStore()
    targets = discover_targets(registry, state, family="f")
    assert [t.model_id for t in targets] == ["ollama/a"]


def test_discover_targets_by_model_ids():
    """--model selects specific local models regardless of expose state.

    Explicit model ids should override the default exposed-only filter so a
    user can benchmark a downloaded-but-unexposed model directly.
    """
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    targets = discover_targets(registry, state, model_ids=["ollama/a"])
    assert [t.model_id for t in targets] == ["ollama/a"]


def test_discover_targets_remote_providers_excluded():
    """Remote providers are never benchmark targets.

    Even when explicitly requested by model id, remote/cloud providers lack a
    local isolation path and must be excluded to avoid unsupported direct
    routes.
    """
    registry = Registry(
        providers=[ProviderEntry(id="openrouter", name="OpenRouter", location="remote")],
        models=[
            ModelEntry(id="openrouter/a", family="f", provider_id="openrouter", model_name="a")
        ],
    )
    state = StateStore()
    targets = discover_targets(registry, state, model_ids=["openrouter/a"])
    assert targets == []


def test_discover_targets_carries_mlx_lm_server_pairing():
    """discover_targets must resolve the target/draft repo|local_path fields
    onto Target, since isolate_provider() has nowhere else to get the pairing
    from (mlx_lm_server has no default in bin/llm-isolate-provider)."""
    registry = Registry(
        providers=[ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local")],
        models=[
            ModelEntry(
                id="mlx_lm_server/a+draft-b",
                family="f",
                provider_id="mlx_lm_server",
                model_name="a+draft-b",
                fetch=Fetch(repo="org/target"),
                draft=DraftSpec(local_path="/models/draft"),
            )
        ],
    )
    state = StateStore()
    targets = discover_targets(registry, state, model_ids=["mlx_lm_server/a+draft-b"])
    assert len(targets) == 1
    target = targets[0]
    assert target.repo == "org/target"
    assert target.local_path is None
    assert target.draft_repo is None
    assert target.draft_local_path == "/models/draft"


def test_run_benchmark_forwards_mlx_lm_server_pairing_to_isolate(tmp_path, monkeypatch):
    """An mlx_lm_server target's resolved target/draft strings must reach
    isolate_provider() as extra_args — without this, bin/llm-isolate-provider
    mlx_lm_server has no pairing to fall back to and errors out on every
    mlx_lm_server benchmark row."""
    import modelman.benchmark.runner as runner_module

    registry = Registry(
        providers=[ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local")],
        models=[
            ModelEntry(
                id="mlx_lm_server/pair",
                family="f",
                provider_id="mlx_lm_server",
                model_name="pair",
                fetch=Fetch(local_path="/models/target"),
                draft=DraftSpec(repo="org/draft"),
            )
        ],
    )
    state = StateStore()
    state.set("mlx_lm_server/pair", ModelState(litellm_exposed=True))

    calls: list[tuple[str, ...]] = []

    def _fake_isolate(provider_id, *extra_args):
        calls.append((provider_id, *extra_args))
        return type("I", (), {"ok": True, "direct_url": "http://localhost:8001"})()

    def _fake_route(session, target, route, url, workload, pass_number):
        return TargetResult(
            model_id=target.model_id,
            provider_id=target.provider_id,
            route=route,
            pass_number=pass_number,
            metrics=BenchmarkMetrics(ttft_ms=1, total_ms=2, completion_tokens=3, prompt_tokens=4),
            error=None,
        )

    monkeypatch.setattr(runner_module, "isolate_provider", _fake_isolate)
    monkeypatch.setattr(runner_module, "restore_providers", lambda: None)
    monkeypatch.setattr(runner_module, "_run_route", _fake_route)

    run_benchmark(registry, state, _FakeWorkload(), results_dir=tmp_path)

    assert calls == [
        ("mlx_lm_server", "/models/target", str(Path("org/draft").resolve()))
    ]


def test_run_benchmark_reisolates_between_different_mlx_lm_server_pairings(tmp_path, monkeypatch):
    """Two consecutive mlx_lm_server targets with different pairings must
    each trigger a fresh isolate_provider() call.

    mlx_lm_server is one-model-per-process (unlike ollama/omlx, which serve
    whichever model is requested from an already-running daemon), so reusing
    isolation across a provider_id match alone would silently keep serving
    the first pairing's target+draft for the second target's requests."""
    import modelman.benchmark.runner as runner_module

    registry = Registry(
        providers=[ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local")],
        models=[
            ModelEntry(
                id="mlx_lm_server/pair-1",
                family="f",
                provider_id="mlx_lm_server",
                model_name="pair-1",
                fetch=Fetch(repo="org/target-1"),
                draft=DraftSpec(repo="org/draft-1"),
            ),
            ModelEntry(
                id="mlx_lm_server/pair-2",
                family="f",
                provider_id="mlx_lm_server",
                model_name="pair-2",
                fetch=Fetch(repo="org/target-2"),
                draft=DraftSpec(repo="org/draft-2"),
            ),
        ],
    )
    state = StateStore()
    state.set("mlx_lm_server/pair-1", ModelState(litellm_exposed=True))
    state.set("mlx_lm_server/pair-2", ModelState(litellm_exposed=True))

    calls: list[tuple[str, ...]] = []

    def _fake_isolate(provider_id, *extra_args):
        calls.append((provider_id, *extra_args))
        return type("I", (), {"ok": True, "direct_url": "http://localhost:8001"})()

    def _fake_route(session, target, route, url, workload, pass_number):
        return TargetResult(
            model_id=target.model_id,
            provider_id=target.provider_id,
            route=route,
            pass_number=pass_number,
            metrics=BenchmarkMetrics(ttft_ms=1, total_ms=2, completion_tokens=3, prompt_tokens=4),
            error=None,
        )

    monkeypatch.setattr(runner_module, "isolate_provider", _fake_isolate)
    monkeypatch.setattr(runner_module, "restore_providers", lambda: None)
    monkeypatch.setattr(runner_module, "_run_route", _fake_route)

    run_benchmark(registry, state, _FakeWorkload(), results_dir=tmp_path)

    assert calls == [
        ("mlx_lm_server", str(Path("org/target-1").resolve()), str(Path("org/draft-1").resolve())),
        ("mlx_lm_server", str(Path("org/target-2").resolve()), str(Path("org/draft-2").resolve())),
    ]


def test_run_benchmark_records_error_when_mlx_lm_server_pairing_incomplete(tmp_path, monkeypatch):
    """A mistakenly-registered mlx_lm_server model with no draft source must
    fail that single target with a clear error, not crash the whole run or
    silently call isolate_provider() with a missing positional arg."""
    import modelman.benchmark.runner as runner_module

    registry = Registry(
        providers=[ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local")],
        models=[
            ModelEntry(
                id="mlx_lm_server/broken",
                family="f",
                provider_id="mlx_lm_server",
                model_name="broken",
                fetch=Fetch(repo="org/target"),
                draft=None,
            )
        ],
    )
    state = StateStore()
    state.set("mlx_lm_server/broken", ModelState(litellm_exposed=True))

    def _unexpected_isolate(*args, **kwargs):
        raise AssertionError("isolate_provider must not be called with an incomplete pairing")

    monkeypatch.setattr(runner_module, "isolate_provider", _unexpected_isolate)
    monkeypatch.setattr(runner_module, "restore_providers", lambda: None)

    run = run_benchmark(registry, state, _FakeWorkload(), results_dir=tmp_path)

    assert all(r.error is not None for r in run.results)
    assert "draft" in run.results[0].error.lower() or "target" in run.results[0].error.lower()
