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
from modelman.benchmark.agent.suite import load_suite
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import ModelEntry, ProviderEntry, Registry

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
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: order.append(("isolate", pid)))
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: order.append(("restore",)))
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    class _FakeJudgeTransport:
        def complete(self, prompt, *, temperature):
            order.append(("judge",))
            return json.dumps(
                {
                    "scores": {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10},
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
                    "scores": {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10},
                    "total": 100, "verdict": "principled_fix", "flags": [], "rationale": "ok",
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
        assert json.loads(f.readline())["type"] == "agent_settled", "re-judging truncated the raw stream"
