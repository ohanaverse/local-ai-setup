"""`modelman benchmark` and `modelman provider` keep working, served by
llmbench, until modelman is retired. Deleted with modelman."""

from typer.testing import CliRunner

from modelman.main import app


def test_benchmark_is_mounted_from_llmbench():
    result = CliRunner().invoke(app, ["benchmark", "list-workloads"])
    assert result.exit_code == 0, result.output
    assert result.output.split() == ["chat", "code", "long", "short"]


def test_provider_is_mounted_from_llmbench():
    result = CliRunner().invoke(app, ["provider", "list"])
    assert result.exit_code == 0, result.output
    assert "omlx-6bit\t[supported] occupancy=omlx" in result.output


def test_provider_is_not_nested_under_benchmark():
    """llmbench's own root app mounts `provider` beside the benchmark
    commands. That must not leak a second `modelman benchmark provider`."""
    result = CliRunner().invoke(app, ["benchmark", "provider", "list"])
    assert result.exit_code == 2
    assert "No such command 'provider'" in result.output
