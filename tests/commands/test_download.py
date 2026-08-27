"""The `download` command is now a TUI shortcut; verify it launches ModelScreen."""

from unittest.mock import patch

import pytest
from typer.testing import CliRunner

from modelman.main import app


@pytest.mark.skip(reason="FamilyScreen migrates in PR 3")
def test_download_launches_tui_at_family(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    save_manifest(FamilyManifest(family="ornith"), fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, ["download", "ornith"])
        assert result.exit_code == 0
        run_tui.assert_called_once_with("ornith")


def test_no_args_launches_tui_at_family_list(tmp_path, monkeypatch):
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(tmp_path / "f"))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "c"))
    (tmp_path / "f").mkdir()
    (tmp_path / "c").write_text("providers:\n  ollama:\n    type: ollama\n")

    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, [])
        assert result.exit_code == 0
        run_tui.assert_called_once_with(None)


def test_download_no_family_arg_shows_help():
    runner = CliRunner()
    result = runner.invoke(app, ["download"])
    assert result.exit_code != 0
