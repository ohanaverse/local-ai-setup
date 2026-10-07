"""Tests for llmbench.benchmark.agent.cli — the llmbench agent
subcommand surface.

--dry-run resolving the matrix without executing anything is the harness's
own safeguard against loading a 27B model six times because of a typo'd
suite (spec) — the test for it asserts run_suite is never called, not just
that the command exits 0.
"""

from pathlib import Path

import pytest
from typer.testing import CliRunner

import llmbench.benchmark.agent.cli as cli_module
from llmbench.benchmark.agent.cli import agent_app
from llmbench.benchmark.agent.pidriver import RowConfig
from llmbench.benchmark.agent.runner import RowRunResult
from llmbench.registry import ModelEntry, ProviderEntry, Registry

FIXTURE_TASKS = Path(__file__).parent / "fixtures" / "tasks"

runner = CliRunner()


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )


def test_list_tasks_lists_bundles_under_root():
    result = runner.invoke(agent_app, ["list-tasks", "--root", str(FIXTURE_TASKS)])
    assert result.exit_code == 0
    assert "mini-drift" in result.output


def test_dry_run_never_calls_run_suite(tmp_path, monkeypatch):
    def _boom(*args, **kwargs):
        raise AssertionError("run_suite must not be called during --dry-run")

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(cli_module, "run_suite", _boom)

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path), "--dry-run"])
    assert result.exit_code == 0
    assert "01" in result.output
    assert "dry run" in result.output


def test_run_records_agent_last_run_pointer(tmp_path, monkeypatch):
    row = RowConfig(
        label="r1", model_id="ollama/a", thinking="off", route="direct", provider_id="ollama"
    )
    fake_run_dir = tmp_path / "results" / "20260101-000000"

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(
        cli_module,
        "run_suite",
        lambda *a, **k: (
            fake_run_dir,
            [
                RowRunResult(
                    row=row,
                    pass_number=1,
                    row_dir=fake_run_dir / "01",
                    gates=None,
                    metrics=None,
                    diff_raw="",
                    error=None,
                )
            ],
        ),
    )
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "latest.toml"))

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path)])
    assert result.exit_code == 0

    from llmbench.state import load_state

    state = load_state()
    assert state.extra["benchmarks"]["agent_last_run"] == str(fake_run_dir)


def test_run_prints_each_row_error_once(tmp_path, monkeypatch, capsys):
    """When a row has an isolation error, the harness must not print it both
    inside _record_run_and_report and again in run_cmd; duplicate lines clutter
    stderr and make log-based alerting unreliable."""
    row = RowConfig(
        label="r1", model_id="ollama/a", thinking="off", route="direct", provider_id="ollama"
    )
    fake_run_dir = tmp_path / "results" / "20260101-000000"

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(
        cli_module,
        "run_suite",
        lambda *a, **k: (
            fake_run_dir,
            [
                RowRunResult(
                    row=row,
                    pass_number=1,
                    row_dir=fake_run_dir / "01",
                    gates=None,
                    metrics=None,
                    diff_raw="",
                    error="provider failed",
                )
            ],
        ),
    )
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "latest.toml"))

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path)])
    assert result.exit_code == 0
    assert result.output.count("provider failed") == 1


def test_run_passes_skip_judge_through_to_run_suite(tmp_path, monkeypatch):
    captured = {}

    def _fake_run_suite(suite_obj, registry_obj, **kwargs):
        captured.update(kwargs)
        return tmp_path / "results" / "run1", []

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(cli_module, "run_suite", _fake_run_suite)
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "latest.toml"))

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path), "--skip-judge"])
    assert result.exit_code == 0
    assert captured["skip_judge"] is True


def test_show_prints_persisted_summary(tmp_path, monkeypatch):
    run_dir = tmp_path / "results" / "run1"
    run_dir.mkdir(parents=True)
    (run_dir / "summary.md").write_text("# hello from disk\n", encoding="utf-8")
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "latest.toml"))

    from llmbench.state import StateStore, save_state

    state = StateStore()
    state.extra["benchmarks"] = {"agent_last_run": str(run_dir)}
    save_state(state)

    result = runner.invoke(agent_app, ["show", "--latest"])
    assert result.exit_code == 0
    assert "hello from disk" in result.output


def test_run_records_the_pointer_even_when_restore_failed(tmp_path, monkeypatch):
    """The sweep is finished and persisted; only a backend is down. The operator
    still gets the row count, the Results path, the --latest pointer, and a
    nonzero exit — the two facts are not in conflict."""
    from llmbench.benchmark.agent.runner import RunSavedButRestoreFailed

    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "latest.toml"))
    # run_cmd loads the registry before anything else, and load_registry reads
    # ~/.config/local-ai/registry.toml — a real run passed this test on a dev
    # machine that happens to own that file and failed on CI that does not.
    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    run_dir = tmp_path / "results" / "20260101-000000"
    run_dir.mkdir(parents=True)

    def _raise(*args, **kwargs):
        raise RunSavedButRestoreFailed(
            f"providers failed to restore (saved to {run_dir}): llamacpp down",
            run_dir=run_dir,
            results=[],
        )

    monkeypatch.setattr(cli_module, "run_suite", _raise)
    suite_path = tmp_path / "suite.toml"
    suite_path.write_text('name = "x"\n', encoding="utf-8")
    monkeypatch.setattr(cli_module, "load_suite", lambda *a, **k: type("S", (), {"rows": []})())

    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path)])
    assert result.exit_code == 1
    assert "Agent benchmark complete" in result.output
    assert "llamacpp down" in result.output
    from llmbench.state import load_state

    assert load_state().extra["benchmarks"]["agent_last_run"] == str(run_dir)


def test_list_suites_keeps_listing_past_a_non_agent_suite(tmp_path, monkeypatch):
    """An eval suite sharing the directory (it sorts first here) is reported as
    an error line; the agent suites after it are still listed and the command
    exits 0."""
    monkeypatch.setattr(cli_module, "load_registry", _registry)
    (tmp_path / "a-eval.toml").write_text(
        """
name = "eval-sweep"

[judge]
model = "x"
temperature = 0.0
route = "openrouter"
""",
        encoding="utf-8",
    )
    (tmp_path / "b-agent.toml").write_text(
        """
name = "agent suite"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
route = "litellm"

[[rows]]
model = "ollama/a"
thinking = "off"
route = "litellm"
""",
        encoding="utf-8",
    )

    result = runner.invoke(agent_app, ["list-suites", "--root", str(tmp_path)])

    assert result.exit_code == 0
    assert "a-eval.toml: error:" in result.output
    assert "b-agent.toml  (agent suite, 1 rows)" in result.output


def _results_dir_with_a_summary_at(tmp_path, run_id):
    """A results dir nested deep enough that `run_id` resolves inside tmp_path,
    with a summary.md planted where the unchecked join would land."""
    results_dir = tmp_path / "a" / "b" / "results"
    results_dir.mkdir(parents=True)
    outside = results_dir / run_id
    outside.mkdir(parents=True, exist_ok=True)
    (outside / "summary.md").write_text("OUTSIDE-THE-RESULTS-DIR", encoding="utf-8")
    return results_dir


@pytest.mark.parametrize("run_id", ["..", "../../x"])
def test_show_refuses_a_run_id_outside_the_results_dir(tmp_path, run_id):
    """#277: agent show joined --run-id onto --results-dir unchecked. Same
    usage error as `eval show`, and nothing is read."""
    results_dir = _results_dir_with_a_summary_at(tmp_path, run_id)

    result = runner.invoke(
        agent_app, ["show", "--run-id", run_id, "--results-dir", str(results_dir)]
    )

    assert result.exit_code == 1, result.output
    assert f"error: invalid --run-id {run_id!r}" in result.output
    assert "OUTSIDE-THE-RESULTS-DIR" not in result.output


def test_show_prints_the_summary_for_a_valid_run_id(tmp_path):
    run_dir = tmp_path / "20260905-143200"
    run_dir.mkdir()
    (run_dir / "summary.md").write_text("# agent summary", encoding="utf-8")

    result = runner.invoke(
        agent_app, ["show", "--run-id", "20260905-143200", "--results-dir", str(tmp_path)]
    )

    assert result.exit_code == 0, result.output
    assert "# agent summary" in result.output


@pytest.mark.parametrize("run_id", ["..", "../../x"])
def test_judge_refuses_a_run_id_outside_the_results_dir(tmp_path, monkeypatch, run_id):
    """#277: agent judge is the one that WRITES — rejudge_run rewrites every
    row's judge.json under the directory it is handed — so a bad id must be
    refused before rejudge_run is called at all."""
    results_dir = _results_dir_with_a_summary_at(tmp_path, run_id)
    calls = []
    monkeypatch.setattr(cli_module, "rejudge_run", lambda *a, **k: calls.append((a, k)) or [])

    result = runner.invoke(
        agent_app, ["judge", "--run-id", run_id, "--results-dir", str(results_dir)]
    )

    assert result.exit_code == 1, result.output
    assert f"error: invalid --run-id {run_id!r}" in result.output
    assert calls == []


def test_judge_hands_rejudge_run_the_directory_of_a_valid_run_id(tmp_path, monkeypatch):
    calls = []

    def fake_rejudge_run(target_dir, **kwargs):
        calls.append(target_dir)
        return [{"label": "row-a", "rubric_total": 7, "composite": 0.7, "verdict": "pass"}]

    monkeypatch.setattr(cli_module, "rejudge_run", fake_rejudge_run)

    result = runner.invoke(
        agent_app, ["judge", "--run-id", "20260905-143200", "--results-dir", str(tmp_path)]
    )

    assert result.exit_code == 0, result.output
    assert calls == [tmp_path / "20260905-143200"]
    assert "row-a: rubric=7 composite=0.7 verdict=pass" in result.output
