"""Tests for modelman.benchmark.agent.runner — phase 0/1 orchestration.

The isolation loop's grouping-by-provider and its failure-containment (one
provider's isolation failure must not abort the rest of the suite,
matching modelman.benchmark.runner's existing per-target error pattern)
are the two behaviors most likely to silently waste a multi-hour local
sweep if they regress. run_pi_process itself is mocked here — Tasks 5-7
already cover its own correctness against a real subprocess.
"""

import gzip
import json
from pathlib import Path

import pytest

import modelman.benchmark.isolation as isolation_module
from modelman.benchmark.agent import pidriver as pidriver_module
from modelman.benchmark.agent.pidriver import PiRunResult
from modelman.benchmark.agent.runner import run_suite
from modelman.benchmark.agent.suite import JudgeConfig, load_suite
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import DraftSpec, Fetch, ModelEntry, ProviderEntry, Registry

MINI_DRIFT = Path(__file__).parent / "fixtures" / "tasks" / "mini-drift"


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )


def _suite_toml(task_path: Path, models: str = '["ollama/a"]') -> str:
    return f"""
name = "test suite"
task = "{task_path}"
passes = 1
cooldown_s = 0
agent_timeout_s = 5

[judge]
model = "some-cloud/model"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[routes.direct.ollama]
base_url = "http://localhost:11434/v1"
api = "openai-completions"

[[rows]]
models = {models}
thinking = ["off"]
routes = ["direct"]
"""


def _write_suite(tmp_path: Path, body: str, name: str = "suite.toml") -> Path:
    path = tmp_path / name
    path.write_text(body, encoding="utf-8")
    return path


def _fake_run(session: bool = True):
    """Build a stand-in for a real pi subprocess.

    pi is never launched here (Tasks 5–7 already cover run_pi_process against
    a real process), so nothing writes a session file — but gate 1 requires
    one, and the runner passes `--session-dir <row_dir>` in `cmd`. Recreating
    that single side effect is what keeps these rows reaching the gate they
    are meant to exercise: without it every mocked row short-circuits at
    AGENT_ERROR and the NO_DIFF assertions below become unreachable.
    """

    def _run(cmd, *args, **kwargs) -> tuple[list[dict], PiRunResult]:
        if session:
            session_dir = Path(cmd[cmd.index("--session-dir") + 1])
            session_dir.mkdir(parents=True, exist_ok=True)
            (session_dir / "2026-01-01T00-00-00-000Z-fake.jsonl").write_text(
                json.dumps({"type": "session", "version": 3, "id": "fake"}) + "\n", encoding="utf-8"
            )
        # an assistant message_end is what gate 1 looks for, so the double has
        # to produce one in the event list as well as on the result
        events: list[dict] = [
            {"type": "message_end", "message": {"role": "assistant", "usage": {}, "content": []}}
        ]
        return events, PiRunResult(
            exit_code=0,
            timed_out=False,
            aborted=False,
            seen_message_end=True,
            unparsed_lines=0,
            stderr_tail="",
        )

    return _run


_no_diff_run = _fake_run()


@pytest.fixture(autouse=True)
def _hermetic_preflight(monkeypatch):
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")


def test_run_suite_isolates_once_per_provider_group(tmp_path, monkeypatch):
    """Two rows on the same provider share one isolate_provider() call."""
    calls = []
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: calls.append(pid))
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: calls.append("restore"))
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    body = _suite_toml(MINI_DRIFT, models='["ollama/a", "ollama/a"]')
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,  # phase 1 only; the judge transport is Task 18's concern
    )

    assert calls == ["ollama", "restore"]
    assert len(results) == 2
    assert all(r.error is None for r in results)
    assert all(r.gates is not None and r.gates.results[2].code == "NO_DIFF" for r in results)


def test_run_suite_reisolates_mlx_lm_server_between_pairings(tmp_path, monkeypatch):
    """Two mlx_lm_server rows with DIFFERENT target+draft pairings must each
    trigger their own isolate call: mlx_lm_server is one-model-per-process, so
    serving the second row's agent against the first row's still-running
    server would silently measure the wrong model."""
    calls: list[tuple] = []

    def _isolate(pid, *extra_args):
        calls.append((pid, *extra_args))

    monkeypatch.setattr(isolation_module, "isolate_provider", _isolate)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    registry = Registry(
        providers=[ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local")],
        models=[
            ModelEntry(
                id="mlx_lm_server/pair1",
                family="f",
                provider_id="mlx_lm_server",
                model_name="pair1",
                fetch=Fetch(local_path="/models/target1"),
                draft=DraftSpec(local_path="/models/draft1"),
            ),
            ModelEntry(
                id="mlx_lm_server/pair2",
                family="f",
                provider_id="mlx_lm_server",
                model_name="pair2",
                fetch=Fetch(local_path="/models/target2"),
                draft=DraftSpec(local_path="/models/draft2"),
            ),
        ],
    )

    body = (
        _suite_toml(MINI_DRIFT, models='["mlx_lm_server/pair1", "mlx_lm_server/pair2"]')
        .replace("[routes.direct.ollama]", "[routes.direct.mlx_lm_server]", 1)
    )
    # _suite_toml's route block only contains one ollama section; swap it once.
    suite = load_suite(_write_suite(tmp_path, body), registry)
    run_suite(
        suite,
        registry,
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )
    assert calls[0] == ("mlx_lm_server", "/models/target1", "/models/draft1")
    assert calls[1] == ("mlx_lm_server", "/models/target2", "/models/draft2")
    assert calls[-1] == calls[1]  # exactly two isolates, then restore


def test_diff_captured_before_hidden_tests_are_seeded(tmp_path, monkeypatch):
    """Gate 9 seeds the hidden tests into the workspace; the judge's diff must
    be captured before that seeding, or the frontier-model judge reads the exact
    acceptance tests it is meant to be blind to. The mini-drift bundle ships a
    hidden test, so a diff captured after seeding would stage it and leak it."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)

    def _run_with_change(cmd, *args, **kwargs):
        # write a real agent change into the workspace so the diff is non-empty
        ws = Path(kwargs["workspace_path"])
        (ws / "pkg" / "__init__.py").write_text(
            "def add_one(n: int) -> int:\n    return n + 1\n", encoding="utf-8"
        )
        (ws / "tests" / "test_regression.py").write_text("import unittest\n", encoding="utf-8")
        return _no_diff_run(cmd, *args, **kwargs)

    monkeypatch.setattr(pidriver_module, "run_pi_process", _run_with_change)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )
    # gate 9 seeded the hidden test, but it must not appear in the judge's diff
    assert "test_hidden.py" not in results[0].diff_raw
    assert "test_hidden" not in results[0].diff_raw
    # the agent's own change is still there
    assert "test_regression.py" in results[0].diff_raw


def test_run_suite_contains_isolation_failure_to_its_group(tmp_path, monkeypatch):
    """An isolation failure marks that provider's rows with the error and
    the suite continues — matching the existing single-turn runner's
    per-target error containment."""

    def _fail_isolate(pid):
        raise BenchmarkError(f"isolation failed for {pid}")

    monkeypatch.setattr(isolation_module, "isolate_provider", _fail_isolate)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,  # phase 1 only; the judge transport is Task 18's concern
    )

    assert len(results) == 1
    assert results[0].error == "isolation failed for ollama"
    assert results[0].gates is None


def test_run_suite_marks_agent_error_when_the_agent_writes_no_session_file(tmp_path, monkeypatch):
    """Gate 1's session-file evidence is a real requirement, not decoration:
    a process that exits 0 without ever producing a session must not be scored
    as a completed row (nothing downstream can be trusted about what it did)."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _fake_run(session=False))

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,  # phase 1 only; the judge transport is Task 18's concern
    )

    assert results[0].gates.results[0].code == "AGENT_ERROR"
    assert results[0].gates.cap == 0.0

    # NOTE for Task 18: once run_suite grows its judge phase, add
    # skip_judge=True to the call above — this row has gates (an
    # AGENT_ERROR report), so _judge_all would otherwise try to build a real
    # LiteLLM transport from the missing live_models_path and raise.


def test_run_suite_row_filter_selects_by_label(tmp_path, monkeypatch):
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    body = _suite_toml(MINI_DRIFT, models='["ollama/a", "ollama/a"]')
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    wanted_label = suite.rows[0].label
    run_dir, results = run_suite(
        suite,
        _registry(),
        row_filter=[wanted_label],
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,  # phase 1 only; the judge transport is Task 18's concern
    )
    assert len(results) == 1
    assert results[0].row.label == wanted_label


def test_run_suite_judges_rows_after_restore_and_sets_composite(tmp_path, monkeypatch):
    """Judging happens after restore_providers, on every row with gates
    evaluated, and the composite is rubric_total x cap (spec Scoring)."""
    order = []
    monkeypatch.setattr(
        isolation_module, "isolate_provider", lambda pid: order.append(("isolate", pid))
    )
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: order.append(("restore",)))
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    class _FakeJudgeTransport:
        def complete(self, prompt, *, temperature):
            order.append(("judge",))
            return json.dumps(
                {
                    "scores": {
                        "root_cause": 30,
                        "approach": 25,
                        "test_quality": 20,
                        "scope": 15,
                        "coherence": 10,
                    },
                    "total": 100,
                    "verdict": "principled_fix",
                    "flags": [],
                    "rationale": "ok",
                }
            )

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        judge_transport_factory=lambda judge_cfg, path: _FakeJudgeTransport(),
    )

    assert order == [("isolate", "ollama"), ("restore",), ("judge",)]
    assert results[0].judge is not None
    assert results[0].judge.status == "scored"
    # This row is NO_DIFF (cap x0.00), so the composite is 0 regardless of
    # the (fake, maximal) rubric score — proving the cap is actually applied.
    assert results[0].composite == 0


def test_persist_judge_artifact_does_not_rewrite_the_full_row(tmp_path, monkeypatch):
    """The post-judge persist step must add judge.json through the cheap
    write_judge_json() path (already used by rejudge_run for this exact
    purpose), not by re-running the full write_row_artifacts() — which
    re-gzips the entire event stream and rewrites diff/gates/metrics files
    that did not change, a second time, per row, on every sweep with any
    judge phase at all."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    import modelman.benchmark.agent.report as report_module

    calls = {"write_row_artifacts": 0}
    original = report_module.write_row_artifacts

    def _counting(*args, **kwargs):
        calls["write_row_artifacts"] += 1
        return original(*args, **kwargs)

    monkeypatch.setattr(report_module, "write_row_artifacts", _counting)

    class _FakeJudgeTransport:
        def complete(self, prompt, *, temperature):
            return json.dumps(
                {
                    "scores": {
                        "root_cause": 30,
                        "approach": 25,
                        "test_quality": 20,
                        "scope": 15,
                        "coherence": 10,
                    },
                    "total": 100,
                    "verdict": "principled_fix",
                    "flags": [],
                    "rationale": "ok",
                }
            )

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        judge_transport_factory=lambda cfg, path: _FakeJudgeTransport(),
    )

    assert calls["write_row_artifacts"] == 1, (
        "write_row_artifacts ran a second time just to add judge.json"
    )
    judge_json = json.loads((results[0].row_dir / "judge.json").read_text(encoding="utf-8"))
    assert judge_json["combined"]["total"] == 100


def test_run_suite_skip_judge_leaves_composite_none(tmp_path, monkeypatch):
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )
    assert results[0].judge is None
    assert results[0].composite is None


def test_run_suite_writes_run_artifacts(tmp_path, monkeypatch):
    """run_suite persists run.toml, summary.md, and metrics.jsonl — the CLI's
    show command reads these files, not the in-memory result list."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )

    assert (run_dir / "run.toml").exists()
    assert (run_dir / "summary.md").exists()
    assert (run_dir / "metrics.jsonl").exists()
    assert (results[0].row_dir / "gates.json").exists()
    assert (results[0].row_dir / "row.json").exists()


def test_rejudge_run_rewrites_judge_json_from_persisted_artifacts(tmp_path, monkeypatch):
    """`agent judge` re-scores from row.json/diff.patch/gates.json alone — no
    agent process, no workspace, no isolation — and leaves every other artifact
    in the row directory byte-identical."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )

    from modelman.benchmark.agent.runner import rejudge_run

    class _FakeJudgeTransport:
        def complete(self, prompt, *, temperature):
            return json.dumps(
                {
                    "scores": {
                        "root_cause": 30,
                        "approach": 25,
                        "test_quality": 20,
                        "scope": 15,
                        "coherence": 10,
                    },
                    "total": 100,
                    "verdict": "principled_fix",
                    "flags": [],
                    "rationale": "ok",
                }
            )

    # Seed a raw event stream so "the re-judge left it alone" is assertable.
    with gzip.open(results[0].row_dir / "agent.jsonl.gz", "wt", encoding="utf-8") as f:
        f.write(json.dumps({"type": "agent_settled"}) + "\n")

    outcomes = rejudge_run(run_dir, judge_transport_factory=lambda cfg, path: _FakeJudgeTransport())
    assert len(outcomes) == 1
    assert outcomes[0]["rubric_total"] == 100
    judge_json = results[0].row_dir / "judge.json"
    assert json.loads(judge_json.read_text(encoding="utf-8"))["combined"]["total"] == 100
    with gzip.open(results[0].row_dir / "agent.jsonl.gz", "rt", encoding="utf-8") as f:
        assert json.loads(f.readline())["type"] == "agent_settled", (
            "re-judging truncated the raw stream"
        )


def test_failed_restore_still_persists_the_sweep(tmp_path, monkeypatch):
    """On this host llm-restore-providers can time out on llama.cpp with every
    row's data intact; the failure must surface, but never by discarding the
    run it happened to interrupt."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    def _boom() -> None:
        raise BenchmarkError("llamacpp did not come back up")

    monkeypatch.setattr(isolation_module, "restore_providers", _boom)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    with pytest.raises(BenchmarkError, match="llamacpp did not come back up"):
        run_suite(
            suite,
            _registry(),
            results_dir=tmp_path / "results",
            live_models_path=tmp_path / "missing.json",
            skip_judge=True,
        )

    # row artifacts from before judging, so a row's raw stream also survives
    streams = sorted((tmp_path / "results").rglob("agent.jsonl.gz"))
    assert streams, "no row stream was persisted"
    for path in streams:
        with gzip.open(path, "rt", encoding="utf-8") as f:
            assert json.loads(f.readline())["type"] == "message_end"
    run_dirs = list((tmp_path / "results").iterdir())
    assert run_dirs, "the run directory vanished with the results"
    assert (run_dirs[0] / "summary.md").exists()
    assert (run_dirs[0] / "metrics.jsonl").exists()


def _cloud_registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local"),
            ProviderEntry(id="openrouter", name="OpenRouter", location="cloud"),
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="openrouter/z", family="f", provider_id="openrouter", model_name="z"),
        ],
    )


def _cloud_suite(tmp_path: Path) -> Path:
    return _write_suite(
        tmp_path,
        _suite_toml(MINI_DRIFT, models='["openrouter/z"]').replace(
            'routes = ["direct"]', 'routes = ["litellm"]'
        ),
    )


def test_run_suite_never_isolates_a_cloud_provider(tmp_path, monkeypatch, litellm_models_json):
    """bin/llm-isolate-provider knows only the local backends; isolating
    openrouter fails the helper and used to mark every cloud row ISOLATION_ERROR
    - and a cloud row contends with nothing on this machine."""
    isolated: list[str] = []

    def _isolate(pid: str) -> None:
        isolated.append(pid)
        if pid not in ("ollama", "llamacpp", "omlx", "omlx-6bit"):
            raise BenchmarkError(f"[llm-isolate-provider] unknown provider: {pid}")

    restored: list[bool] = []
    monkeypatch.setattr(isolation_module, "isolate_provider", _isolate)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: restored.append(True))
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_cloud_suite(tmp_path), _cloud_registry())
    run_dir, results = run_suite(
        suite,
        _cloud_registry(),
        results_dir=tmp_path / "results",
        live_models_path=litellm_models_json,
        skip_judge=True,
    )
    assert isolated == [], f"a cloud row was isolated: {isolated}"
    assert restored == [], "a run that isolated nothing still restarted services"
    assert results[0].error is None
    assert results[0].gates is not None


def test_run_suite_still_isolates_local_rows_once(tmp_path, monkeypatch):
    """The guard must not quietly disable isolation for the local backends it
    exists to protect - that is the whole point of the harness."""
    isolated: list[str] = []
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: isolated.append(pid))
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )
    assert isolated == ["ollama"]


def test_omlx_6bit_override_still_isolates(tmp_path, monkeypatch, litellm_models_json):
    """omlx-6bit is a row-level provider override and is not in
    DEFAULT_PROVIDER_IDS, so a guard built from that constant alone would skip
    isolating the variant that most needs it (4-bit and 6-bit share one
    process, and isolating the wrong one benchmarks the wrong weights)."""
    isolated: list[str] = []
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: isolated.append(pid))
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    old = '[[rows]]\nmodels = ["ollama/a"]\nthinking = ["off"]\nroutes = ["direct"]'
    new = (
        '[[rows]]\nmodel = "ollama/a"\nthinking = "off"\nroute = "litellm"\nprovider = "omlx-6bit"'
    )
    toml = _suite_toml(MINI_DRIFT).replace(old, new)
    assert new in toml, "the fixture suite's row block changed shape"
    run_suite(
        load_suite(_write_suite(tmp_path, toml), _registry()),
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=litellm_models_json,
        skip_judge=True,
    )
    assert isolated == ["omlx-6bit"]


def test_judge_transport_follows_the_suites_route(tmp_path, monkeypatch, litellm_models_json):
    """A frontier judge is the one thing a local gateway may not have (this
    host's serves 37 models and no claude among them), so [judge].route =
    "openrouter" has to actually reach OpenRouter rather than the gateway — and
    it must drop the LiteLLM-style openrouter/ prefix, which OpenRouter itself
    does not know."""
    from modelman.benchmark.agent.runner import _build_judge_transport

    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-or-test")
    cfg = JudgeConfig(
        model="openrouter/anthropic/claude-opus-5",
        thinking="low",
        temperature=0.0,
        samples=1,
        max_attempts=2,
        route="openrouter",
    )
    transport = _build_judge_transport(cfg, litellm_models_json)
    assert transport.base_url == "https://openrouter.ai/api/v1"
    assert transport.model == "anthropic/claude-opus-5", (
        "the openrouter/ prefix is LiteLLM's, not OpenRouter's"
    )
    assert transport.api_key == "sk-or-test"

    cfg2 = JudgeConfig(
        model="some/model",
        thinking="low",
        temperature=0.0,
        samples=1,
        max_attempts=2,
        route="litellm",
    )
    gateway = _build_judge_transport(cfg2, litellm_models_json)
    assert gateway.base_url == "http://localhost:4000/v1"
    assert gateway.model == "some/model"


def test_judge_transport_without_openrouter_key_names_the_missing_key(monkeypatch):
    from modelman.benchmark.agent.runner import _build_judge_transport

    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    cfg = JudgeConfig(
        model="openrouter/x",
        thinking="low",
        temperature=0.0,
        samples=1,
        max_attempts=2,
        route="openrouter",
    )
    with pytest.raises(BenchmarkError, match="OPENROUTER_API_KEY"):
        _build_judge_transport(
            cfg, Path("/nonexistent/models.json"), plist_path=Path("/nonexistent.plist")
        )


def test_row_dir_has_no_metrics_log_unless_debug(tmp_path, monkeypatch, litellm_models_json):
    """The metrics trace is half a megabyte of prose per row restating the
    compressed event stream beside it; the first live run wrote four."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)
    monkeypatch.delenv("MODELMAN_AGENT_DEBUG", raising=False)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=litellm_models_json,
        skip_judge=True,
    )
    assert not (results[0].row_dir / "metrics.log").exists()
    assert (results[0].row_dir / "agent.jsonl.gz").exists()
    assert run_dir.exists()


def _mlx_lm_registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local")
        ],
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


def test_run_suite_isolates_mlx_lm_server_with_pairing_args(tmp_path, monkeypatch):
    """An mlx_lm_server group must resolve its target+draft pairing from the
    registry and forward it as extra args: the helper exits 1 without the
    pairing, so a bare isolate_provider(provider_id) call would mark the
    whole group ISOLATION_ERROR after the suite's setup cost was paid."""
    calls: list[tuple] = []

    def _isolate(pid, *extra_args):
        calls.append((pid, *extra_args))

    monkeypatch.setattr(isolation_module, "isolate_provider", _isolate)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    body = _suite_toml(MINI_DRIFT, models='["mlx_lm_server/pair"]').replace(
        "[routes.direct.ollama]",
        "[routes.direct.mlx_lm_server]",
    )
    suite = load_suite(_write_suite(tmp_path, body), _mlx_lm_registry())
    run_suite(
        suite,
        _mlx_lm_registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )
    # local_path target forwarded verbatim (already absolute); repo-id draft
    # passed through un-mangled — the same contract
    # benchmark/runner._isolate_extra_args is tested to honor.
    assert calls == [("mlx_lm_server", "/models/target", "org/draft")]


def test_run_suite_mlx_lm_server_missing_pairing_errors_that_group(tmp_path, monkeypatch):
    """A misregistered mlx_lm_server model with no draft source must fail
    that group's rows with a clear error, not crash the run or call
    isolate_provider without the pairing the helper requires."""
    from modelman.benchmark import isolation

    registry = Registry(
        providers=[ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local")],
        models=[
            ModelEntry(
                id="mlx_lm_server/broken",
                family="f",
                provider_id="mlx_lm_server",
                model_name="broken",
            )
        ],
    )
    with pytest.raises(BenchmarkError, match="missing a target or draft"):
        isolation.mlx_lm_server_pairing_args(
            "mlx_lm_server/broken",
            None,  # target_local_path
            None,  # target_repo
            None,  # draft_local_path
            None,  # draft_repo
        )
