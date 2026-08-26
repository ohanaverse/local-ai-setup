import pytest
from pathlib import Path
from unittest.mock import patch
from modelman.providers.llamacpp import LlamaCppProvider
from modelman.providers.registry import ProviderRegistry
from modelman.providers.base import VariantSpec


@pytest.fixture
def provider():
    from modelman.providers import llamacpp as _mod
    return LlamaCppProvider({})


def test_registered():
    assert "llamacpp" in ProviderRegistry.available()


def test_is_downloaded_true(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    hf_cache = tmp_path / ".cache" / "huggingface" / "hub"
    repo_dir = hf_cache / "models--ornith-ai--Ornith-1.5-35B-A3B-GGUF" / "snapshots" / "abc123"
    repo_dir.mkdir(parents=True)
    (repo_dir / "Ornith-1.5-35B-Q4_K_M.gguf").write_bytes(b"x" * 100)

    variant: VariantSpec = {
        "id": "q4", "provider": "llamacpp", "name": "Ornith-1.5-35B-Q4_K_M.gguf",
        "repo": "ornith-ai/Ornith-1.5-35B-A3B-GGUF", "files": ["Ornith-1.5-35B-Q4_K_M.gguf"],
    }
    assert provider.is_downloaded(variant) is True


def test_is_downloaded_false(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    variant: VariantSpec = {
        "id": "q4", "provider": "llamacpp", "name": "x.gguf",
        "repo": "missing/repo", "files": ["x.gguf"],
    }
    assert provider.is_downloaded(variant) is False


def test_download_calls_snapshot_download(provider):
    from huggingface_hub import snapshot_download
    variant: VariantSpec = {
        "id": "q4", "provider": "llamacpp", "name": "x.gguf",
        "repo": "foo/bar", "files": ["x.gguf"],
    }
    with patch("modelman.providers.llamacpp.snapshot_download") as mock_dl:
        mock_dl.return_value = "/Users/keith/.cache/huggingface/hub/.../x.gguf"
        path = provider.download(variant)
        mock_dl.assert_called_once()
        kwargs = mock_dl.call_args.kwargs
        assert kwargs["repo_id"] == "foo/bar"
        assert kwargs["allow_patterns"] == ["x.gguf"]
        assert path.endswith("x.gguf")