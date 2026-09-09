from unittest.mock import patch

import pytest

from modelman.providers.base import VariantSpec
from modelman.providers.mlx_lm_server import MLXLMServerProvider
from modelman.providers.registry import ProviderRegistry


@pytest.fixture
def provider(tmp_path):
    return MLXLMServerProvider({"model_dir": str(tmp_path / "models")})


def _make_dir(path, *files):
    path.mkdir(parents=True)
    for name, data in files:
        (path / name).write_bytes(data)


def test_registered():
    """The mlx_lm_server provider must be discoverable through ProviderRegistry."""
    assert "mlx_lm_server" in ProviderRegistry.available()


# --- is_downloaded: requires BOTH target and draft present ---


def test_is_downloaded_true_when_both_repo_dirs_present(provider, tmp_path):
    """A complete target+draft pairing is considered downloaded only when both
    sides exist on disk."""
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("config.json", b"{}"))
    _make_dir(md / "Draft-MLX", ("config.json", b"{}"))
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    assert provider.is_downloaded(variant) is True


def test_is_downloaded_false_when_draft_missing(provider, tmp_path):
    # Pairing-specific case: target present but no draft at all (neither
    # draft_repo nor draft_local_path configured) — is_downloaded must be
    # False. This provider models a target+draft *pairing*; a target alone
    # is not a complete, servable artifact for mlx_lm_server.
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("config.json", b"{}"))
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
    }
    assert provider.is_downloaded(variant) is False


def test_is_downloaded_false_when_target_missing(provider, tmp_path):
    """A missing target side makes the pairing unservable, even if the draft
    is present."""
    md = tmp_path / "models"
    _make_dir(md / "Draft-MLX", ("config.json", b"{}"))
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "draft_repo": "org/Draft-MLX",
    }
    assert provider.is_downloaded(variant) is False


def test_is_downloaded_true_with_mixed_sourcing(provider, tmp_path):
    # Mixed sourcing: repo-downloaded target + locally-produced draft.
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("config.json", b"{}"))
    draft_dir = tmp_path / "user-draft"
    _make_dir(draft_dir, ("config.json", b"{}"))
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_local_path": str(draft_dir),
    }
    assert provider.is_downloaded(variant) is True


# --- download ---


def test_download_calls_snapshot_download_for_repo_sides(provider, tmp_path):
    """Downloading a fully repo-sourced pairing must fetch both the target
    and draft via snapshot_download and return the target directory."""
    md = tmp_path / "models"
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    with patch("modelman.providers.mlx_lm_server.snapshot_download") as mock_dl:
        path = provider.download(variant)
        assert mock_dl.call_count == 2
        calls = {c.kwargs["repo_id"]: c.kwargs["local_dir"] for c in mock_dl.call_args_list}
        assert calls["org/Target-MLX"] == str(md / "Target-MLX")
        assert calls["org/Draft-MLX"] == str(md / "Draft-MLX")
        assert path == str(md / "Target-MLX")


def test_download_mixed_sourcing_downloads_only_repo_side(provider, tmp_path):
    # Mixed sourcing: repo target + local_path draft -> snapshot_download is
    # called exactly once (for the target), and the local draft directory is
    # never touched by the download machinery.
    md = tmp_path / "models"
    draft_dir = tmp_path / "user-draft"
    draft_dir.mkdir()
    (draft_dir / "config.json").write_text("{}")
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_local_path": str(draft_dir),
    }
    with patch("modelman.providers.mlx_lm_server.snapshot_download") as mock_dl:
        path = provider.download(variant)
        mock_dl.assert_called_once()
        assert mock_dl.call_args.kwargs["repo_id"] == "org/Target-MLX"
        assert path == str(md / "Target-MLX")


def test_download_local_path_target_is_noop(provider, tmp_path):
    """A local_path target side is never downloaded by modelman; only the
    repo-sourced draft side is fetched in this configuration."""
    target_dir = tmp_path / "user-target"
    target_dir.mkdir()
    md = tmp_path / "models"
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "local_path": str(target_dir),
        "draft_repo": "org/Draft-MLX",
    }
    with patch("modelman.providers.mlx_lm_server.snapshot_download") as mock_dl:
        path = provider.download(variant)
        mock_dl.assert_called_once()
        assert mock_dl.call_args.kwargs["repo_id"] == "org/Draft-MLX"
        assert mock_dl.call_args.kwargs["local_dir"] == str(md / "Draft-MLX")
        assert path == str(target_dir)


def test_download_raises_when_target_side_has_no_source(provider):
    """A pairing cannot be downloaded if the target side has neither a repo
    nor a local_path configured."""
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "draft_repo": "org/Draft-MLX",
    }
    with pytest.raises(ValueError, match="target"):
        provider.download(variant)


def test_download_raises_when_draft_side_has_no_source(provider):
    """A pairing cannot be downloaded if the draft side has neither a repo
    nor a local_path configured."""
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
    }
    with (
        patch("modelman.providers.mlx_lm_server.snapshot_download"),
        pytest.raises(ValueError, match="draft"),
    ):
        provider.download(variant)


def test_download_local_path_missing_raises(provider, tmp_path):
    """A configured local_path that does not exist must fail fast at download
    time rather than surfacing as a cryptic serve-time error."""
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "local_path": str(tmp_path / "does-not-exist"),
        "draft_repo": "org/Draft-MLX",
    }
    with pytest.raises(ValueError, match="local_path does not exist"):
        provider.download(variant)


# --- path_of: returns the TARGET dir only (never the draft) ---


def test_path_of_returns_target_dir_only(provider, tmp_path):
    """path_of() is display/sync-oriented and must return only the target
    directory, not the draft."""
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("config.json", b"{}"))
    _make_dir(md / "Draft-MLX", ("config.json", b"{}"))
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    assert provider.path_of(variant) == str(md / "Target-MLX")


def test_path_of_returns_none_when_target_missing(provider, tmp_path):
    """path_of() must report None when the target side is absent, even if the
    draft side exists."""
    md = tmp_path / "models"
    _make_dir(md / "Draft-MLX", ("config.json", b"{}"))
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    assert provider.path_of(variant) is None


# --- artifact_paths: target + draft, used for shared-ownership checks ---


def test_artifact_paths_returns_both_target_and_draft_dirs(provider, tmp_path):
    """artifact_paths() must expose both sides so registry shared-artifact
    detection can catch a draft model reused across multiple pairings."""
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("config.json", b"{}"))
    _make_dir(md / "Draft-MLX", ("config.json", b"{}"))
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    assert provider.artifact_paths(variant) == frozenset(
        [str(md / "Target-MLX"), str(md / "Draft-MLX")]
    )


def test_artifact_paths_excludes_missing_side(provider, tmp_path):
    """artifact_paths() must only include directories that actually exist,
    returning an empty set when neither side is present."""
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    assert provider.artifact_paths(variant) == frozenset()


# --- size_of: sum of target + draft ---


def test_size_of_sums_target_and_draft(provider, tmp_path):
    """size_of() must aggregate the byte sizes of both the target and draft
    directories so sync reports the true disk cost of the pairing."""
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("a.safetensors", b"a" * 50))
    _make_dir(md / "Draft-MLX", ("b.safetensors", b"b" * 30))
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    assert provider.size_of(variant) == 80


def test_size_of_returns_none_when_neither_side_resolves(provider):
    """size_of() must return None when neither target nor draft can be
    located, matching other providers' behavior for absent artifacts."""
    variant: VariantSpec = {"id": "x", "provider": "mlx_lm_server", "name": "x"}
    assert provider.size_of(variant) is None


def test_size_of_counts_only_present_side_when_other_missing(provider, tmp_path):
    """size_of() must still report the present side's size even if the other
    side is missing, rather than returning None."""
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("a.safetensors", b"a" * 50))
    variant: VariantSpec = {
        "id": "x",
        "provider": "mlx_lm_server",
        "name": "x",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",  # not downloaded
    }
    assert provider.size_of(variant) == 50


# --- list_local ---


def test_list_local_finds_model_dirs(provider, tmp_path):
    """list_local() must enumerate every directory under the provider's
    model_dir, treating each basename as a variant id."""
    md = tmp_path / "models"
    _make_dir(md / "Model-A", ("config.json", b"{}"))
    _make_dir(md / "Model-B", ("config.json", b"{}"))
    models = provider.list_local()
    ids = [m["variant_id"] for m in models]
    assert "Model-A" in ids
    assert "Model-B" in ids


def test_list_local_empty_when_no_dir(provider):
    """list_local() must return an empty list when the model_dir has not been
    created yet."""
    assert provider.list_local() == []


# --- resolve_local: unsupported, mirrors omlx (base default) ---


def test_resolve_local_not_implemented(provider):
    """MLXLMServerProvider does not implement batch resolve_local; callers fall
    back to the per-variant methods."""
    assert provider.resolve_local([]) is None


# --- delete: per-side refinement C ---


def test_delete_removes_both_repo_dirs(provider, tmp_path):
    """Deleting a fully repo-sourced pairing must remove both the target and
    draft directories from model_dir."""
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("model.safetensors", b"data"))
    _make_dir(md / "Draft-MLX", ("model.safetensors", b"data"))
    variant = {
        "id": "x",
        "provider": "mlx_lm_server",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    provider.delete(variant)
    assert not (md / "Target-MLX").exists()
    assert not (md / "Draft-MLX").exists()
    assert md.exists()


def test_delete_with_local_path_draft_removes_only_target(provider, tmp_path):
    """Safety-critical mixed-sourcing case: a pairing may have a
    repo-downloaded target and a local_path draft. delete() must remove the
    repo-downloaded target (modelman's own download) but must never touch
    the user-produced local_path draft directory, even though both sides go
    through the same delete() call."""
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("model.safetensors", b"data"))
    draft_dir = tmp_path / "user-draft"
    _make_dir(draft_dir, ("model.safetensors", b"precious"))
    variant = {
        "id": "x",
        "provider": "mlx_lm_server",
        "repo": "org/Target-MLX",
        "draft_local_path": str(draft_dir),
    }
    provider.delete(variant)
    assert not (md / "Target-MLX").exists()
    assert draft_dir.exists()
    assert (draft_dir / "model.safetensors").exists()


def test_delete_with_local_path_target_removes_only_draft(provider, tmp_path):
    """Mirror of the above with sides swapped: local_path target + a
    repo-downloaded draft. Only the repo-downloaded draft is removed."""
    target_dir = tmp_path / "user-target"
    _make_dir(target_dir, ("model.safetensors", b"precious"))
    md = tmp_path / "models"
    _make_dir(md / "Draft-MLX", ("model.safetensors", b"data"))
    variant = {
        "id": "x",
        "provider": "mlx_lm_server",
        "local_path": str(target_dir),
        "draft_repo": "org/Draft-MLX",
    }
    provider.delete(variant)
    assert target_dir.exists()
    assert (target_dir / "model.safetensors").exists()
    assert not (md / "Draft-MLX").exists()


def test_delete_noop_when_both_sides_local_path(provider, tmp_path):
    """Safety-critical: a pairing entirely sourced from local_path/
    draft_local_path must leave both directories completely untouched."""
    target_dir = tmp_path / "user-target"
    draft_dir = tmp_path / "user-draft"
    _make_dir(target_dir, ("model.safetensors", b"precious-target"))
    _make_dir(draft_dir, ("model.safetensors", b"precious-draft"))
    variant = {
        "id": "x",
        "provider": "mlx_lm_server",
        "local_path": str(target_dir),
        "draft_local_path": str(draft_dir),
    }

    provider.delete(variant)  # must not raise, must not touch either dir

    assert target_dir.exists()
    assert draft_dir.exists()


def test_delete_noop_if_dirs_absent(provider):
    """delete() must be idempotent and safe when the on-disk directories are
    already gone."""
    variant = {
        "id": "x",
        "provider": "mlx_lm_server",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    provider.delete(variant)  # must not raise


def test_delete_raises_when_neither_side_has_source(provider):
    """delete() must raise a clear error when there is nothing to delete,
    avoiding silent no-ops caused by misconfigured entries."""
    variant = {"id": "x", "provider": "mlx_lm_server"}
    with pytest.raises(ValueError):
        provider.delete(variant)


# --- cleanup_partial_download: per-side refinement C ---


def test_cleanup_partial_download_removes_partial_dirs(provider, tmp_path):
    """cleanup_partial_download() must remove leftover directories from a
    cancelled or failed repo download so they do not appear as complete."""
    md = tmp_path / "models"
    _make_dir(md / "Target-MLX", ("partial.bin", b"not finished"))
    variant = {
        "id": "x",
        "provider": "mlx_lm_server",
        "repo": "org/Target-MLX",
        "draft_repo": "org/Draft-MLX",
    }
    provider.cleanup_partial_download(variant)
    assert not (md / "Target-MLX").exists()


def test_cleanup_partial_download_noop_for_local_path_sides(provider, tmp_path):
    """Safety-critical (refinement C, both sides independently): a cancelled
    download's cleanup must never remove a local_path-sourced side, whether
    it's the target, the draft, or both."""
    target_dir = tmp_path / "user-target"
    draft_dir = tmp_path / "user-draft"
    _make_dir(target_dir, ("model.safetensors", b"precious-target"))
    _make_dir(draft_dir, ("model.safetensors", b"precious-draft"))
    variant = {
        "id": "x",
        "provider": "mlx_lm_server",
        "local_path": str(target_dir),
        "draft_local_path": str(draft_dir),
    }

    provider.cleanup_partial_download(variant)  # must not raise or touch either dir

    assert target_dir.exists()
    assert draft_dir.exists()


def test_cleanup_partial_download_missing_dirs_is_noop(provider):
    """cleanup_partial_download() must be safe to call even when no download
    was ever started for either side."""
    variant = {
        "id": "x",
        "provider": "mlx_lm_server",
        "repo": "org/never-started",
        "draft_repo": "org/never-started-draft",
    }
    provider.cleanup_partial_download(variant)  # must not raise
