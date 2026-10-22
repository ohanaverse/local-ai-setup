"""The `llmbench` command tree."""

import pytest
from typer.testing import CliRunner

from llmbench.main import app


def test_benchmark_commands_sit_at_the_top_level():
    """`llmbench run`, not `llmbench benchmark run`: the tool is the
    benchmark, so its commands need no group word in front of them. A script
    or a guide that says `llmbench list-workloads` must keep working."""
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
    `llmbench provider`; `list` is its one command that touches nothing.

    A row is the id, a tab, then `occupancy=` (only for an id that shares
    another id's server) or `health=`. The `[supported]` tag every row used
    to carry said nothing once the retired backend was removed, so it was
    dropped; a row that starts with anything else after the tab means the
    tag, or some other prefix, came back into output people read."""
    result = CliRunner().invoke(app, ["provider", "list"])
    assert result.exit_code == 0, result.output
    assert all(
        line.split("\t")[1].startswith(("health=", "occupancy="))
        for line in result.output.splitlines()
    )
    assert [line.split("\t")[0] for line in result.output.splitlines()] == [
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
