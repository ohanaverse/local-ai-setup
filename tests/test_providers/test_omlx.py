from unittest.mock import patch

import pytest

from modelman.providers._progress import ProgressTqdm
from modelman.providers.base import VariantSpec
from modelman.providers.omlx import OMLXProvider
from modelman.providers.registry import ProviderRegistry


@pytest.fixture
def provider():
    return OMLXProvider({"model_dir": "~/.omlx/models"})


def test_registered():
    assert "omlx" in ProviderRegistry.available()


def test_is_downloaded_true(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    model_dir = tmp_path / ".omlx" / "models" / "Ornith-1.5-35B-A3B-MLX-4bit"
    model_dir.mkdir(parents=True)
    (model_dir / "config.json").write_text("{}")

    variant: VariantSpec = {
        "id": "4bit",
        "provider": "omlx",
        "name": "Ornith-1.5-35B-A3B-MLX-4bit",
        "repo": "ornith-ai/Ornith-1.5-35B-A3B-MLX-4bit",
    }
    assert provider.is_downloaded(variant) is True


def test_is_downloaded_false_when_dir_missing(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "repo": "foo/Bar-MLX",
    }
    assert provider.is_downloaded(variant) is False


def test_is_downloaded_false_when_dir_empty(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    model_dir = tmp_path / ".omlx" / "models" / "Bar-MLX"
    model_dir.mkdir(parents=True)
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "repo": "foo/Bar-MLX",
    }
    assert provider.is_downloaded(variant) is False


def test_list_local_finds_model_dirs(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    model_dir = tmp_path / ".omlx" / "models"
    model_dir.mkdir(parents=True)
    (model_dir / "Model-A").mkdir()
    (model_dir / "Model-A" / "config.json").write_text("{}")
    (model_dir / "Model-B").mkdir()
    (model_dir / "Model-B" / "config.json").write_text("{}")

    models = provider.list_local()
    assert len(models) == 2
    ids = [m["variant_id"] for m in models]
    assert "Model-A" in ids
    assert "Model-B" in ids


def test_list_local_empty_when_no_dir(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    assert provider.list_local() == []


def test_download_calls_snapshot_download_with_local_dir(provider, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    model_dir = tmp_path / ".omlx" / "models"

    variant: VariantSpec = {
        "id": "4bit",
        "provider": "omlx",
        "name": "Ornith-1.5-35B-A3B-MLX-4bit",
        "repo": "ornith-ai/Ornith-1.5-35B-A3B-MLX-4bit",
    }
    with patch("modelman.providers.omlx.snapshot_download") as mock_dl:
        mock_dl.return_value = str(model_dir / "Ornith-1.5-35B-A3B-MLX-4bit")
        path = provider.download(variant)
        kwargs = mock_dl.call_args.kwargs
        assert kwargs["local_dir"] == str(model_dir / "Ornith-1.5-35B-A3B-MLX-4bit")
        assert path == str(model_dir / "Ornith-1.5-35B-A3B-MLX-4bit")


def test_size_of_sums_model_dir(tmp_path):
    md = tmp_path / "models"
    target = md / "Ornith-1.5"
    target.mkdir(parents=True)
    (target / "a.safetensors").write_bytes(b"a" * 50)
    (target / "b.safetensors").write_bytes(b"b" * 30)

    p = OMLXProvider({"model_dir": str(md)})
    size = p.size_of(
        {
            "id": "x",
            "provider": "omlx",
            "name": "x",
            "repo": "ornith/Ornith-1.5",
            "files": None,
        }
    )
    assert size == 80


def test_size_of_returns_none_when_missing(tmp_path):
    p = OMLXProvider({"model_dir": str(tmp_path / "models")})
    assert (
        p.size_of(
            {
                "id": "x",
                "provider": "omlx",
                "name": "x",
                "repo": "ornith/missing",
                "files": None,
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
        "provider": "omlx",
        "name": "x-mlx",
        "repo": "foo/bar",
    }
    progress_lines: list[str] = []
    with patch("modelman.providers.omlx.snapshot_download") as mock_dl:
        provider.download(variant, on_progress=progress_lines.append)
        kwargs = mock_dl.call_args.kwargs
        assert kwargs.get("tqdm_class") is ProgressTqdm
        assert ProgressTqdm._active_on_progress is None
        assert ProgressTqdm._active_should_cancel is None


def test_cancel_current_resets_on_next_download(provider):
    provider.cancel_current()
    assert provider._cancel_requested is True
    variant: VariantSpec = {
        "id": "q4",
        "provider": "omlx",
        "name": "x-mlx",
        "repo": "foo/bar",
    }
    with patch("modelman.providers.omlx.snapshot_download"):
        provider.download(variant)
        assert provider._cancel_requested is False


def test_path_of_returns_model_dir(tmp_path):
    md = tmp_path / "models"
    target = md / "Ornith-1.5"
    target.mkdir(parents=True)
    (target / "config.json").write_text("{}")

    p = OMLXProvider({"model_dir": str(md)})
    path = p.path_of(
        {
            "id": "x",
            "provider": "omlx",
            "name": "x",
            "repo": "ornith/Ornith-1.5",
            "files": None,
        }
    )
    assert path == str(target)


def test_path_of_returns_none_when_dir_missing(tmp_path):
    p = OMLXProvider({"model_dir": str(tmp_path / "models")})
    assert (
        p.path_of(
            {
                "id": "x",
                "provider": "omlx",
                "name": "x",
                "repo": "ornith/missing",
                "files": None,
            }
        )
        is None
    )


def test_path_of_returns_none_when_dir_empty(tmp_path):
    md = tmp_path / "models"
    target = md / "Empty-MLX"
    target.mkdir(parents=True)
    p = OMLXProvider({"model_dir": str(md)})
    assert (
        p.path_of(
            {
                "id": "x",
                "provider": "omlx",
                "name": "x",
                "repo": "ornith/Empty-MLX",
                "files": None,
            }
        )
        is None
    )
