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


def test_delete_removes_model_dir(tmp_path):
    """delete() removes the model's whole on-disk directory (keyed on the
    repo basename under model_dir) while preserving the model_dir root
    itself. Important: ready-off and delete flows both rely on delete()
    actually reclaiming the multi-GB artifact, and a delete that removed
    the parent model_dir would wipe every other omlx model's files."""
    model_dir = tmp_path / "models" / "test-repo"
    model_dir.mkdir(parents=True)
    (model_dir / "model.safetensors").write_bytes(b"fake data")
    (model_dir / "config.json").write_bytes(b"{}")

    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant = {
        "id": "test--model",
        "provider": "omlx",
        "repo": "test-org/test-repo",
    }

    provider.delete(variant)

    assert not model_dir.exists()
    assert (tmp_path / "models").exists()  # parent dir preserved


def test_delete_noop_if_dir_absent(tmp_path):
    """delete() on a model whose directory is already gone must be a silent
    no-op, not a raise. Important: the ready-off loop and the apply() deletes
    loop both call delete() after an is_downloaded() guard that can race with
    an externally removed artifact — a raise there would abort the whole
    apply run and leave registry/state cleanup unwritten."""
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant = {
        "id": "test--model",
        "provider": "omlx",
        "repo": "test-org/test-repo",
    }

    provider.delete(variant)  # must not raise


def test_cleanup_partial_download_removes_partial_target_dir(tmp_path):
    # A cancelled oMLX download leaves a partially-populated target
    # directory (snapshot_download writes files incrementally into
    # local_dir). Cleanup must remove it so a retry starts clean and the
    # model doesn't show as "has some files" in list_local().
    provider = OMLXProvider({"model_dir": str(tmp_path)})
    target = tmp_path / "some-model"
    target.mkdir()
    (target / "partial.bin").write_bytes(b"not finished")

    provider.cleanup_partial_download({"id": "x", "provider": "omlx", "repo": "org/some-model"})

    assert not target.exists()


def test_cleanup_partial_download_missing_dir_is_noop(tmp_path):
    # Cleanup runs unconditionally after any cancel/fail; a download that
    # never got far enough to create the target directory must not raise.
    provider = OMLXProvider({"model_dir": str(tmp_path)})
    provider.cleanup_partial_download({"id": "x", "provider": "omlx", "repo": "org/never-started"})


# --- local_path support (Task 2: locally-produced mlx-lm convert/dwq output) ---


def test_is_downloaded_local_path_true(tmp_path):
    # A local_path-sourced variant (no repo at all) must resolve the same
    # way a repo-sourced one does: present + non-empty directory -> True.
    local_dir = tmp_path / "my-custom-dwq-model"
    local_dir.mkdir()
    (local_dir / "config.json").write_text("{}")
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {"id": "x", "provider": "omlx", "name": "x", "local_path": str(local_dir)}
    assert provider.is_downloaded(variant) is True


def test_is_downloaded_local_path_false_when_missing(tmp_path):
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": str(tmp_path / "does-not-exist"),
    }
    assert provider.is_downloaded(variant) is False


def test_download_local_path_is_noop_and_returns_path(tmp_path):
    # download() on a local_path variant must not call snapshot_download at
    # all — the artifact already exists by definition (the user produced it).
    local_dir = tmp_path / "my-custom-dwq-model"
    local_dir.mkdir()
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {"id": "x", "provider": "omlx", "name": "x", "local_path": str(local_dir)}
    with patch("modelman.providers.omlx.snapshot_download") as mock_dl:
        path = provider.download(variant)
        mock_dl.assert_not_called()
        assert path == str(local_dir)


def test_download_local_path_missing_raises(tmp_path):
    # A typo'd local_path must surface at "download" time (an explicit,
    # actionable ValueError) rather than silently failing later at serve time.
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    missing = tmp_path / "does-not-exist"
    variant: VariantSpec = {"id": "x", "provider": "omlx", "name": "x", "local_path": str(missing)}
    with pytest.raises(ValueError, match="local_path does not exist"):
        provider.download(variant)


def test_size_of_local_path_sums_dir(tmp_path):
    local_dir = tmp_path / "my-model"
    local_dir.mkdir()
    (local_dir / "a.safetensors").write_bytes(b"a" * 50)
    (local_dir / "b.safetensors").write_bytes(b"b" * 30)
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {"id": "x", "provider": "omlx", "name": "x", "local_path": str(local_dir)}
    assert provider.size_of(variant) == 80


def test_size_of_local_path_returns_none_when_missing(tmp_path):
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": str(tmp_path / "missing"),
    }
    assert provider.size_of(variant) is None


def test_path_of_local_path_returns_dir(tmp_path):
    local_dir = tmp_path / "my-model"
    local_dir.mkdir()
    (local_dir / "config.json").write_text("{}")
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {"id": "x", "provider": "omlx", "name": "x", "local_path": str(local_dir)}
    assert provider.path_of(variant) == str(local_dir)


def test_path_of_local_path_returns_none_when_missing(tmp_path):
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": str(tmp_path / "missing"),
    }
    assert provider.path_of(variant) is None


def test_delete_local_path_is_noop_directory_still_exists(tmp_path):
    """Safety-critical (refinement C): a local_path-sourced artifact is a
    directory the USER produced by hand (e.g. mlx_lm.convert/dwq output) —
    modelman never created it and must never delete it. delete() must skip
    shutil.rmtree entirely for a local_path entry, regardless of whether the
    directory exists, and must not raise. This is the regression guard for
    the single most important behavior in this task."""
    local_dir = tmp_path / "user-produced-model"
    local_dir.mkdir()
    (local_dir / "model.safetensors").write_bytes(b"precious user data")
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant = {"id": "x", "provider": "omlx", "local_path": str(local_dir)}

    provider.delete(variant)  # must not raise, must not touch the directory

    assert local_dir.exists()
    assert (local_dir / "model.safetensors").exists()


def test_cleanup_partial_download_local_path_is_noop_directory_still_exists(tmp_path):
    """Safety-critical (refinement C): same guarantee as delete() above, but
    for the cancel/fail cleanup path. A cancelled download callback must
    never be able to remove a user-produced local_path artifact just because
    it happens to share the cleanup code path with repo-sourced downloads."""
    local_dir = tmp_path / "user-produced-model"
    local_dir.mkdir()
    (local_dir / "model.safetensors").write_bytes(b"precious user data")
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant = {"id": "x", "provider": "omlx", "local_path": str(local_dir)}

    provider.cleanup_partial_download(variant)  # must not raise or touch the dir

    assert local_dir.exists()
    assert (local_dir / "model.safetensors").exists()
