"""The `llmbench` command tree, and the one rule about what it imports."""

import ast
from pathlib import Path

import pytest
from typer.testing import CliRunner

from llmbench.main import app


def test_benchmark_commands_sit_at_the_top_level():
    """`llmbench run`, not `llmbench benchmark run`: the tool is the
    benchmark, so the extra word modelman needed is gone."""
    result = CliRunner().invoke(app, ["list-workloads"])
    assert result.exit_code == 0, result.output
    assert result.output.split() == ["chat", "code", "long", "short"]


@pytest.mark.parametrize(
    ("argv", "subcommands"),
    [
        (["agent", "--help"], ["list-tasks", "list-suites", "run", "show", "judge"]),
        (["eval", "--help"], ["list-categories", "list-items", "run", "show", "judge"]),
        (["provider", "--help"], ["isolate", "stop", "stop-all", "restore", "list"]),
    ],
    ids=["agent", "eval", "provider"],
)
def test_sub_apps_are_mounted(argv, subcommands):
    result = CliRunner().invoke(app, argv)
    assert result.exit_code == 0, result.output
    for name in subcommands:
        assert name in result.output


def test_provider_list_names_every_backend():
    """The bash benchmark scripts and the docs reach isolation through
    `llmbench provider`; `list` is its one command that touches nothing."""
    result = CliRunner().invoke(app, ["provider", "list"])
    assert result.exit_code == 0, result.output
    assert [line.split("\t")[0] for line in result.output.splitlines()] == [
        "llamacpp",
        "mlx_lm_server",
        "mtplx",
        "ollama",
        "omlx",
        "omlx-6bit",
    ]


def test_there_is_no_benchmark_level():
    result = CliRunner().invoke(app, ["benchmark", "run"])
    assert result.exit_code == 2
    assert "No such command 'benchmark'" in result.output


def test_llmbench_never_imports_modelman():
    """llmbench outlives modelman. A module-level import would fail in this
    venv, where modelman is not installed; a lazy one inside a function, in a
    branch no test reaches, would pass every other test here and fail the day
    modelman is deleted."""
    src = Path(__file__).resolve().parents[1] / "src" / "llmbench"
    offenders = []
    for path in sorted(src.rglob("*.py")):
        for node in ast.walk(ast.parse(path.read_text(encoding="utf-8"))):
            if isinstance(node, ast.Import):
                names = [alias.name for alias in node.names]
            elif isinstance(node, ast.ImportFrom) and node.level == 0:
                names = [node.module or ""]
            else:
                continue
            if any(name.split(".")[0] == "modelman" for name in names):
                offenders.append(f"{path.relative_to(src)}:{node.lineno}")
    assert offenders == []
