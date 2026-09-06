from pathlib import Path

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.results import BenchmarkMetrics, TargetResult
from modelman.benchmark.runner import RunSavedButRestoreFailed, discover_targets, run_benchmark
from modelman.benchmark.workloads.base import WorkloadSpec
from modelman.registry import ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore


class _FakeWorkload:
    spec = WorkloadSpec(
        name="chat", display_name="Chat", prompt="hi",
        max_tokens=1, temperature=0.0, stream=True,
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
    run = object()
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
