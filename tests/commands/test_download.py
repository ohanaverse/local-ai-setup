import pytest
from pathlib import Path
from unittest.mock import patch, MagicMock
from typer.testing import CliRunner

from modelman.main import app


@pytest.fixture
def runner():
    return CliRunner()


def test_download_no_family_arg_shows_help(runner):
    result = runner.invoke(app, ["download"])
    assert result.exit_code != 0


def test_download_happy_path(runner, tmp_path, monkeypatch):
    # Set up: config with 2 providers, manifest with 2 variants
    config_file = tmp_path / "config.yaml"
    config_file.write_text("""
providers:
  ollama:
    type: ollama
  omlx:
    type: omlx
    model_dir: ~/.omlx/models
""")

    family_dir = tmp_path / "families"
    family_dir.mkdir()
    family_file = family_dir / "testfam.yaml"
    family_file.write_text("""
family: testfam
display_name: Test Family
variants:
  - id: a
    provider: ollama
    name: test:1
  - id: b
    provider: omlx
    repo: foo/bar
""")

    monkeypatch.setenv("MODELMAN_CONFIG", str(config_file))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))

    # Mock: ollama is_downloaded=False (so it shows as missing), omlx=True
    fake_ollama = MagicMock()
    fake_ollama.is_downloaded.return_value = False
    fake_ollama.download.return_value = "ollama:test:1"
    fake_omlx = MagicMock()
    fake_omlx.is_downloaded.return_value = True
    fake_omlx.download.return_value = "/Users/keith/.omlx/models/bar"

    with patch("modelman.providers.registry.ProviderRegistry.get") as mock_get:
        mock_get.side_effect = lambda name, cfg: {"ollama": fake_ollama, "omlx": fake_omlx}[name]

        # User accepts default selection (just presses Enter)
        with patch("questionary.checkbox") as mock_q:
            mock_q.return_value.ask.return_value = ["a"]  # select variant a only

            result = runner.invoke(app, ["download", "testfam"])

    assert result.exit_code == 0, result.output
    fake_ollama.download.assert_called_once()
    # Manifest should be updated
    content = family_file.read_text()
    assert "downloaded:" in content
    assert "a:" in content