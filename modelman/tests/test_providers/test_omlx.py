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


def test_download_flips_cancel_flag_when_interrupted(provider):
    """A real Ctrl+C (or any other exception) unwinding through
    snapshot_download must flip _cancel_requested immediately, so any HF
    worker thread still mid-download picks up the cancellation on its
    next progress update instead of only learning about it after
    apply() has already fully unwound (see queue.py's DownloadCancelled
    handling). Regression for a review finding: this provider never
    mirrored ollama.py's immediate-interrupt hardening."""
    variant: VariantSpec = {
        "id": "q4",
        "provider": "omlx",
        "name": "x-mlx",
        "repo": "foo/bar",
    }
    with (
        patch("modelman.providers.omlx.snapshot_download", side_effect=KeyboardInterrupt),
        pytest.raises(KeyboardInterrupt),
    ):
        provider.download(variant)
    assert provider._cancel_requested is True


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
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": str(local_dir),
    }
    assert provider.is_downloaded(variant) is True


def test_is_downloaded_local_path_false_when_missing(tmp_path):
    # A local_path variant pointing at a nonexistent directory must report
    # False, not raise — reconcile and the delete step call is_downloaded()
    # on user-typo'd paths and must treat "absent" as a normal answer.
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
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": str(local_dir),
    }
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
    # size_of() must walk the user-produced directory recursively and sum
    # file sizes (not the directory entry itself) — the TUI SIZE column is
    # fed by this, and a None here hides real disk usage.
    local_dir = tmp_path / "my-model"
    local_dir.mkdir()
    (local_dir / "a.safetensors").write_bytes(b"a" * 50)
    (local_dir / "b.safetensors").write_bytes(b"b" * 30)
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": str(local_dir),
    }
    assert provider.size_of(variant) == 80


def test_size_of_local_path_returns_none_when_missing(tmp_path):
    # A missing local_path directory yields None (unknown size), not 0 or an
    # exception — reconcile treats None as "provider can't say" and leaves
    # the column blank rather than showing a bogus zero.
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": str(tmp_path / "missing"),
    }
    assert provider.size_of(variant) is None


def test_path_of_local_path_returns_dir(tmp_path):
    # path_of() must return the local_path directory verbatim (already
    # absolute, user-produced) so the TUI details panel and sync write the
    # real location into state.disk_path instead of a repo-basename
    # download dir the artifact was never placed in.
    local_dir = tmp_path / "my-model"
    local_dir.mkdir()
    (local_dir / "config.json").write_text("{}")
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": str(local_dir),
    }
    assert provider.path_of(variant) == str(local_dir)


def test_path_of_local_path_returns_none_when_missing(tmp_path):
    # A missing local_path directory must yield None — path_of() feeds
    # reconcile's disk_path (and the details panel), and inventing a path
    # for an absent directory would make a not-downloaded model look
    # located.
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


def test_local_path_with_a_tilde_is_read_from_home(tmp_path, monkeypatch):
    # #235: a local_path is stored as typed. Unexpanded, `~/...` named a
    # directory literally called `~` and the entry read as not downloaded.
    # (The guard test below passes without this: the guard expands the path
    # itself.)
    monkeypatch.setenv("HOME", str(tmp_path))
    local_dir = tmp_path / "quant" / "my-model"
    local_dir.mkdir(parents=True)
    (local_dir / "config.json").write_text("{}")
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": "~/quant/my-model",
    }
    assert provider.is_downloaded(variant)
    assert provider.path_of(variant) == str(local_dir)
    assert provider.download(variant) == str(local_dir)


def test_local_path_naming_no_such_user_is_absent_not_an_error(tmp_path, monkeypatch):
    # `~name/...` where `name` is no user cannot be expanded. It must read as
    # a directory that is not there — pathlib's expanduser raises
    # RuntimeError for it, and `modelman sync` calls is_downloaded() bare.
    monkeypatch.chdir(tmp_path)
    provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
    variant: VariantSpec = {
        "id": "x",
        "provider": "omlx",
        "name": "x",
        "local_path": "~no-such-user-modelman-test/model",
    }
    assert provider.is_downloaded(variant) is False
    assert provider.path_of(variant) is None
    assert provider.artifact_paths(variant) == frozenset(["~no-such-user-modelman-test/model"])


def test_guard_sees_a_local_path_however_it_is_spelled(tmp_path, shared_owner, respell):
    # #235: see the same test for mtplx. omlx's Path(local_path) only dropped
    # a trailing slash.
    from modelman.registry import Fetch, ModelEntry

    pool = tmp_path / "pool"
    (pool / "qwen").mkdir(parents=True)
    (pool / "qwen" / "weights.safetensors").write_bytes(b"x")
    provider = OMLXProvider({"model_dir": str(pool)})
    downloaded = ModelEntry(
        id="omlx/dl", family="f", provider_id="omlx", model_name="dl", fetch=Fetch(repo="org/qwen")
    )
    local = ModelEntry(
        id="omlx/local",
        family="f",
        provider_id="omlx",
        model_name="local",
        fetch=Fetch(local_path=respell(pool / "qwen")),
    )
    assert shared_owner(provider, downloaded, local) == "omlx/local"
    assert shared_owner(provider, local, downloaded) is None


def test_guard_sees_an_owner_on_another_provider(tmp_path, shared_owner):
    # #241: the guard only looked at entries on the same server. omlx and
    # mlx_lm_server both read MLX directories, so a pairing whose local_path
    # is an omlx download is a natural thing to have — and deleting the omlx
    # entry removed the pairing's target.
    from modelman.registry import DraftSpec, Fetch, ModelEntry

    pool = tmp_path / "pool"
    (pool / "qwen").mkdir(parents=True)
    (pool / "qwen" / "weights.safetensors").write_bytes(b"x")
    provider = OMLXProvider({"model_dir": str(pool)})
    downloaded = ModelEntry(
        id="omlx/dl", family="f", provider_id="omlx", model_name="dl", fetch=Fetch(repo="org/qwen")
    )
    pairing = ModelEntry(
        id="mlx_lm_server/pair",
        family="f",
        provider_id="mlx_lm_server",
        model_name="pair",
        fetch=Fetch(local_path=str(pool / "qwen")),
        draft=DraftSpec(local_path=str(tmp_path / "user-draft")),
    )
    assert shared_owner(provider, downloaded, pairing) == "mlx_lm_server/pair"


# #261: omlx's two-level model directory layout.


def _model(path):
    path.mkdir(parents=True)
    (path / "config.json").write_text("{}")
    return path


def test_list_local_finds_models_inside_an_organization_folder(tmp_path):
    # #261, seen on a real machine: omlx stores a download at
    # models/mlx-community/<name>/ and serves it as <name>. modelman listed a
    # model called "mlx-community" and never saw the real one, so it could
    # not be started or registered.
    _model(tmp_path / "Flat-4bit")
    _model(tmp_path / "mlx-community" / "Nested-6bit")
    _model(tmp_path / "mlx-community" / "Other-8bit")
    (tmp_path / "mlx-community" / "notes").mkdir()
    (tmp_path / "empty-folder").mkdir()
    adapter = _model(tmp_path / "Adapter")
    (adapter / "adapter_config.json").write_text("{}")
    _model(tmp_path / ".cache" / "Hidden")
    (tmp_path / "models--Org--Cached" / "snapshots" / "abc").mkdir(parents=True)
    _model(tmp_path / "zz-org" / "Flat-4bit")  # duplicate name: the first wins

    found = {
        m["variant_id"]: m["path"] for m in OMLXProvider({"model_dir": str(tmp_path)}).list_local()
    }

    assert sorted(found) == ["Flat-4bit", "Nested-6bit", "Other-8bit"]
    assert found["Nested-6bit"] == str(tmp_path / "mlx-community" / "Nested-6bit")
    assert found["Flat-4bit"] == str(tmp_path / "Flat-4bit")


def test_list_local_reads_a_model_dir_that_is_itself_one_model(tmp_path):
    # omlx accepts a model_dir pointed straight at one model's folder.
    _model(tmp_path / "Solo-4bit")
    provider = OMLXProvider({"model_dir": str(tmp_path / "Solo-4bit")})
    assert [m["variant_id"] for m in provider.list_local()] == ["Solo-4bit"]


def test_a_registered_model_inside_an_organization_folder_reads_as_downloaded(tmp_path):
    # A registry entry names a repo; its directory is looked up by the repo's
    # basename. When omlx put that directory inside an organization folder,
    # the entry read as not downloaded although omlx was serving it.
    _model(tmp_path / "mlx-community" / "Nested-6bit")
    provider = OMLXProvider({"model_dir": str(tmp_path)})
    variant: VariantSpec = {"id": "omlx/x", "repo": "mlx-community/Nested-6bit"}
    assert provider.is_downloaded(variant)
    assert provider.path_of(variant) == str(tmp_path / "mlx-community" / "Nested-6bit")


def test_delete_of_a_nested_registered_model_removes_only_that_model(tmp_path):
    # The directory found inside an organization folder is the model's own:
    # a delete must leave the folder and its other models alone.
    nested = _model(tmp_path / "mlx-community" / "Nested-6bit")
    sibling = _model(tmp_path / "mlx-community" / "Sibling-4bit")
    provider = OMLXProvider({"model_dir": str(tmp_path)})
    variant: VariantSpec = {"id": "omlx/x", "repo": "mlx-community/Nested-6bit"}

    assert provider.removable_paths(variant) == frozenset([str(nested)])
    provider.delete(variant)

    assert not nested.exists()
    assert sibling.is_dir()
    assert (tmp_path / "mlx-community").is_dir()
