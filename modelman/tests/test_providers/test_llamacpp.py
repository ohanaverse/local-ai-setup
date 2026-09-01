from unittest.mock import patch

import pytest

from modelman.providers._progress import ProgressTqdm
from modelman.providers.base import VariantSpec
from modelman.providers.llamacpp import LlamaCppProvider
from modelman.providers.registry import ProviderRegistry


@pytest.fixture
def provider():
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
        "id": "q4",
        "provider": "llamacpp",
        "name": "Ornith-1.5-35B-Q4_K_M.gguf",
        "repo": "ornith-ai/Ornith-1.5-35B-A3B-GGUF",
        "files": ["Ornith-1.5-35B-Q4_K_M.gguf"],
    }
    assert provider.is_downloaded(variant) is True


def test_is_downloaded_false(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "x.gguf",
        "repo": "missing/repo",
        "files": ["x.gguf"],
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
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "x.gguf",
        "repo": "foo/bar",
        "files": ["x.gguf"],
    }
    with patch("modelman.providers.llamacpp.snapshot_download") as mock_dl:
        mock_dl.return_value = "/Users/keith/.cache/huggingface/hub/.../x.gguf"
        path = provider.download(variant)
        mock_dl.assert_called_once()
        kwargs = mock_dl.call_args.kwargs
        assert kwargs["repo_id"] == "foo/bar"
        assert kwargs["allow_patterns"] == ["x.gguf"]
        assert path.endswith("x.gguf")


def test_size_of_stats_primary_file(tmp_path, monkeypatch):
    hf = tmp_path / "hf"
    snap = hf / "hub" / "models--ornith--test" / "snapshots" / "rev1"
    snap.mkdir(parents=True)
    f = snap / "model.gguf"
    f.write_bytes(b"x" * 100)

    monkeypatch.setenv("HF_HOME", str(hf))
    p = LlamaCppProvider({})
    size = p.size_of(
        {
            "id": "x",
            "provider": "llamacpp",
            "name": "x",
            "repo": "ornith/test",
            "files": ["model.gguf"],
        }
    )
    assert size == 100


def test_size_of_returns_none_when_not_in_cache(tmp_path, monkeypatch):
    monkeypatch.setenv("HF_HOME", str(tmp_path / "empty"))
    p = LlamaCppProvider({})
    assert (
        p.size_of(
            {
                "id": "x",
                "provider": "llamacpp",
                "name": "x",
                "repo": "ornith/missing",
                "files": ["model.gguf"],
            }
        )
        is None
    )


def test_download_passes_should_cancel_to_tqdm(provider):
    """Provider wires should_cancel into ProgressTqdm so the Cancel
    button can interrupt snapshot_download via DownloadCancelled.

    huggingface_hub doesn't let callers pass kwargs to a user-supplied
    tqdm_class, so the provider sets a class-level active context on
    ProgressTqdm that every bar created during snapshot_download reads.
    """
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "x.gguf",
        "repo": "foo/bar",
        "files": ["x.gguf"],
    }
    progress_lines: list[str] = []
    with patch("modelman.providers.llamacpp.snapshot_download") as mock_dl:
        mock_dl.return_value = "/Users/keith/.cache/huggingface/hub/.../x.gguf"
        provider.download(variant, on_progress=progress_lines.append)
        kwargs = mock_dl.call_args.kwargs
        assert kwargs.get("tqdm_class") is ProgressTqdm
        # Active context was set during the call and cleared after.
        assert ProgressTqdm._active_on_progress is None
        assert ProgressTqdm._active_should_cancel is None


def test_cancel_current_resets_on_next_download(provider):
    """After cancel_current(), should_cancel returns True until the next
    download() call resets the flag."""
    provider.cancel_current()
    assert provider._cancel_requested is True

    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "x.gguf",
        "repo": "foo/bar",
        "files": ["x.gguf"],
    }
    with patch("modelman.providers.llamacpp.snapshot_download") as mock_dl:
        mock_dl.return_value = "/tmp/x.gguf"
        provider.download(variant)
        assert provider._cancel_requested is False


def test_path_of_returns_primary_file_path(tmp_path, monkeypatch):
    hf = tmp_path / "hf"
    snap = hf / "hub" / "models--ornith--test" / "snapshots" / "rev1"
    snap.mkdir(parents=True)
    f = snap / "model.gguf"
    f.write_bytes(b"x" * 100)

    monkeypatch.setenv("HF_HOME", str(hf))
    p = LlamaCppProvider({})
    path = p.path_of(
        {
            "id": "x",
            "provider": "llamacpp",
            "name": "x",
            "repo": "ornith/test",
            "files": ["model.gguf"],
        }
    )
    assert path == str(f)


def test_path_of_returns_none_when_not_in_cache(tmp_path, monkeypatch):
    monkeypatch.setenv("HF_HOME", str(tmp_path / "empty"))
    p = LlamaCppProvider({})
    assert (
        p.path_of(
            {
                "id": "x",
                "provider": "llamacpp",
                "name": "x",
                "repo": "ornith/missing",
                "files": ["model.gguf"],
            }
        )
        is None
    )
