import pytest
from pathlib import Path
from unittest.mock import patch
from modelman.providers.omlx import OMLXProvider
from modelman.providers.registry import ProviderRegistry
from modelman.providers.base import VariantSpec


@pytest.fixture
def provider():
    from modelman.providers import omlx as _mod
    return OMLXProvider({"model_dir": "~/.omlx/models"})


def test_registered():
    assert "omlx" in ProviderRegistry.available()


def test_is_downloaded_true(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    model_dir = tmp_path / ".omlx" / "models" / "Ornith-1.5-35B-A3B-MLX-4bit"
    model_dir.mkdir(parents=True)
    (model_dir / "config.json").write_text("{}")

    variant: VariantSpec = {
        "id": "4bit", "provider": "omlx", "name": "Ornith-1.5-35B-A3B-MLX-4bit",
        "repo": "ornith-ai/Ornith-1.5-35B-A3B-MLX-4bit",
    }
    assert provider.is_downloaded(variant) is True


def test_is_downloaded_false_when_dir_missing(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    variant: VariantSpec = {
        "id": "x", "provider": "omlx", "name": "x",
        "repo": "foo/Bar-MLX",
    }
    assert provider.is_downloaded(variant) is False


def test_is_downloaded_false_when_dir_empty(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    model_dir = tmp_path / ".omlx" / "models" / "Bar-MLX"
    model_dir.mkdir(parents=True)
    variant: VariantSpec = {
        "id": "x", "provider": "omlx", "name": "x",
        "repo": "foo/Bar-MLX",
    }
    assert provider.is_downloaded(variant) is False


def test_download_calls_snapshot_download_with_local_dir(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    model_dir = tmp_path / ".omlx" / "models"

    variant: VariantSpec = {
        "id": "4bit", "provider": "omlx", "name": "Ornith-1.5-35B-A3B-MLX-4bit",
        "repo": "ornith-ai/Ornith-1.5-35B-A3B-MLX-4bit",
    }
    with patch("modelman.providers.omlx.snapshot_download") as mock_dl:
        mock_dl.return_value = str(model_dir / "Ornith-1.5-35B-A3B-MLX-4bit")
        path = provider.download(variant)
        kwargs = mock_dl.call_args.kwargs
        assert kwargs["local_dir"] == str(model_dir / "Ornith-1.5-35B-A3B-MLX-4bit")
        assert path == str(model_dir / "Ornith-1.5-35B-A3B-MLX-4bit")