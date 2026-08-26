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


def test_list_local_finds_gguf_files(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    hub = tmp_path / ".cache" / "huggingface" / "hub"
    snap = hub / "models--foo--bar" / "snapshots" / "abc"
    snap.mkdir(parents=True)
    (snap / "model.gguf").write_bytes(b"x" * 100)
    (snap / "config.json").write_text("{}")  # non-gguf, should be skipped

    models = provider.list_local()
    assert len(models) == 1
    assert models[0]["variant_id"] == "model.gguf"
    assert models[0]["path"].endswith("model.gguf")
    assert models[0]["size_bytes"] == 100


def test_list_local_empty_when_no_cache(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    assert provider.list_local() == []


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