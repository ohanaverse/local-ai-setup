from typer.testing import CliRunner

from modelman.main import app


def test_benchmark_list_workloads():
    runner = CliRunner()
    result = runner.invoke(app, ["benchmark", "list-workloads"])
    assert result.exit_code == 0
    assert "chat" in result.output
