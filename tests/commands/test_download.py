"""The `download` command is now a TUI shortcut; verify it launches ModelScreen."""

from unittest.mock import patch

from typer.testing import CliRunner

from modelman.main import app


def test_download_launches_tui_at_family():
    """`modelman download <family>` is a thin CLI wrapper around
    run_tui(family); it never touches registry.toml/modelman.toml
    itself (ModelmanApp.on_mount does, later, when .run() is called —
    which run_tui does, but run_tui is mocked here). No registry/state
    fixture is needed."""
    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, ["download", "ornith"])
        assert result.exit_code == 0
        run_tui.assert_called_once_with("ornith")


def test_no_args_launches_tui_at_family_list():
    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, [])
        assert result.exit_code == 0
        run_tui.assert_called_once_with(None)


def test_download_no_family_arg_shows_help():
    runner = CliRunner()
    result = runner.invoke(app, ["download"])
    assert result.exit_code != 0
