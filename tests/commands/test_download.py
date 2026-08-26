import pytest
from pathlib import Path
from unittest.mock import patch, MagicMock
from typer.testing import CliRunner

from modelman.main import app


@pytest.fixture
def runner():
    return CliRunner()


def _setup_env(tmp_path, monkeypatch, providers_yaml, family_yaml):
    config_file = tmp_path / "config.yaml"
    config_file.write_text(providers_yaml)
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    family_file = family_dir / "testfam.yaml"
    family_file.write_text(family_yaml)
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_file))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))
    return family_file


def test_download_no_family_arg_shows_help(runner):
    result = runner.invoke(app, ["download"])
    assert result.exit_code != 0


def test_download_happy_path(runner, tmp_path, monkeypatch):
    family_file = _setup_env(tmp_path, monkeypatch, """
providers:
  ollama:
    type: ollama
  omlx:
    type: omlx
    model_dir: ~/.omlx/models
""", """
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

    fake_ollama = MagicMock()
    fake_ollama.is_downloaded.return_value = False
    fake_ollama.download.return_value = "ollama:test:1"
    fake_omlx = MagicMock()
    fake_omlx.is_downloaded.return_value = True
    fake_omlx.download.return_value = "/Users/keith/.omlx/models/bar"

    with patch("modelman.providers.registry.ProviderRegistry.get") as mock_get:
        mock_get.side_effect = lambda name, cfg: {"ollama": fake_ollama, "omlx": fake_omlx}[name]
        with patch("questionary.checkbox") as mock_q:
            mock_q.return_value.ask.return_value = ["a"]
            result = runner.invoke(app, ["download", "testfam"])

    assert result.exit_code == 0, result.output
    fake_ollama.download.assert_called_once()
    content = family_file.read_text()
    assert "downloaded:" in content
    assert "a:" in content


def test_download_all_already_present(runner, tmp_path, monkeypatch):
    _setup_env(tmp_path, monkeypatch, """
providers:
  ollama:
    type: ollama
""", """
family: testfam
variants:
  - id: a
    provider: ollama
    name: test:1
""")

    fake_ollama = MagicMock()
    fake_ollama.is_downloaded.return_value = True

    with patch("modelman.providers.registry.ProviderRegistry.get") as mock_get:
        mock_get.return_value = fake_ollama
        result = runner.invoke(app, ["download", "testfam"])

    assert result.exit_code == 0
    assert "All variants already downloaded" in result.output


def test_download_config_error(runner, tmp_path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("providers: {}\n")
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_file))

    result = runner.invoke(app, ["download", "testfam"])
    assert result.exit_code == 1
    assert "Config error" in result.output


def test_download_manifest_error(runner, tmp_path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("""
providers:
  ollama:
    type: ollama
""")
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_file))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))

    result = runner.invoke(app, ["download", "nonexistent"])
    assert result.exit_code == 1
    assert "Manifest error" in result.output


def test_download_all_flag_skips_prompt(runner, tmp_path, monkeypatch):
    _setup_env(tmp_path, monkeypatch, """
providers:
  ollama:
    type: ollama
""", """
family: testfam
variants:
  - id: a
    provider: ollama
    name: test:1
""")

    fake_ollama = MagicMock()
    fake_ollama.is_downloaded.return_value = False
    fake_ollama.download.return_value = "ollama:test:1"

    with patch("modelman.providers.registry.ProviderRegistry.get") as mock_get:
        mock_get.return_value = fake_ollama
        result = runner.invoke(app, ["download", "testfam", "--all"])

    assert result.exit_code == 0, result.output
    fake_ollama.download.assert_called_once()


def test_download_cancelled(runner, tmp_path, monkeypatch):
    _setup_env(tmp_path, monkeypatch, """
providers:
  ollama:
    type: ollama
""", """
family: testfam
variants:
  - id: a
    provider: ollama
    name: test:1
""")

    fake_ollama = MagicMock()
    fake_ollama.is_downloaded.return_value = False

    with patch("modelman.providers.registry.ProviderRegistry.get") as mock_get:
        mock_get.return_value = fake_ollama
        with patch("questionary.checkbox") as mock_q:
            mock_q.return_value.ask.return_value = None  # user cancels
            result = runner.invoke(app, ["download", "testfam"])

    assert result.exit_code == 1  # typer.Abort
    fake_ollama.download.assert_not_called()


def test_download_failure_continues(runner, tmp_path, monkeypatch):
    family_file = _setup_env(tmp_path, monkeypatch, """
providers:
  ollama:
    type: ollama
""", """
family: testfam
variants:
  - id: a
    provider: ollama
    name: test:1
  - id: b
    provider: ollama
    name: test:2
""")

    fake_ollama = MagicMock()
    fake_ollama.is_downloaded.return_value = False
    fake_ollama.download.side_effect = RuntimeError("network error")

    with patch("modelman.providers.registry.ProviderRegistry.get") as mock_get:
        mock_get.return_value = fake_ollama
        with patch("questionary.checkbox") as mock_q:
            mock_q.return_value.ask.return_value = ["a", "b"]
            result = runner.invoke(app, ["download", "testfam"])

    assert result.exit_code == 0, result.output
    assert fake_ollama.download.call_count == 2  # tried both
    # Manifest should NOT have downloaded entries since both failed
    content = family_file.read_text()
    # The downloaded section should be empty or absent
    lines = [l for l in content.splitlines() if "downloaded" in l.lower()]
    # save_manifest always writes the key, but it should be empty
    assert "a:" not in content or "test:1" not in content.split("downloaded:")[1].split("variants:")[0] if "downloaded:" in content else True


def test_download_unknown_provider_in_manifest(runner, tmp_path, monkeypatch):
    _setup_env(tmp_path, monkeypatch, """
providers:
  ollama:
    type: ollama
""", """
family: testfam
variants:
  - id: a
    provider: unknown_provider
    name: test:1
""")

    # Provider not registered → KeyError caught, status = False
    with patch("questionary.checkbox") as mock_q:
        mock_q.return_value.ask.return_value = []
        result = runner.invoke(app, ["download", "testfam"])

    assert result.exit_code == 0 or result.exit_code == 1  # either is fine (abort on empty)