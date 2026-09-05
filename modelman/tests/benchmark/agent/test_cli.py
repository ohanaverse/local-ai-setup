"""Tests for modelman.benchmark.agent.cli — the modelman benchmark agent
subcommand surface.

--dry-run resolving the matrix without executing anything is the harness's
own safeguard against loading a 27B model six times because of a typo'd
suite (spec) — the test for it asserts run_suite is never called, not just
that the command exits 0.
"""

from pathlib import Path

from typer.testing import CliRunner

import modelman.benchmark.agent.cli as cli_module
from modelman.benchmark.agent.cli import agent_app
from modelman.benchmark.agent.pidriver import RowConfig
from modelman.benchmark.agent.runner import RowRunResult
from modelman.registry import ModelEntry, ProviderEntry, Registry

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
    row = RowConfig(label="r1", model_id="ollama/a", thinking="off", route="direct", provider_id="ollama")
    fake_run_dir = tmp_path / "results" / "20260101-000000"

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(
        cli_module,
        "run_suite",
        lambda *a, **k: (fake_run_dir, [RowRunResult(row=row, pass_number=1, row_dir=fake_run_dir / "01", gates=None, metrics=None, diff_raw="", error=None)]),
    )
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

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

    from modelman.state import load_state

    state = load_state()
    assert state.extra["benchmarks"]["agent_last_run"] == str(fake_run_dir)

def test_run_passes_skip_judge_through_to_run_suite(tmp_path, monkeypatch):
    captured = {}

    def _fake_run_suite(suite_obj, registry_obj, **kwargs):
        captured.update(kwargs)
        return tmp_path / "results" / "run1", []

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(cli_module, "run_suite", _fake_run_suite)
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

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
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    from modelman.state import StateStore, save_state

    state = StateStore()
    state.extra["benchmarks"] = {"agent_last_run": str(run_dir)}
    save_state(state)

    result = runner.invoke(agent_app, ["show", "--latest"])
    assert result.exit_code == 0
    assert "hello from disk" in result.output
