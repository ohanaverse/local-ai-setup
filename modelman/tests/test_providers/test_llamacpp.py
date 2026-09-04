import hashlib
from pathlib import Path
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


def test_delete_preserves_blob_referenced_by_nested_file(tmp_path):
    """A blob whose only surviving reference is a file inside a snapshot
    subdirectory (nested layout, common in HF GGUF repos) must not be judged
    orphaned. Regression: the reference scan was non-recursive (iterdir +
    is_file), so it never saw the nested reference and deleted the shared
    blob — silently destroying another variant's multi-GB weights."""
    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--org--repo" / "snapshots"
    blobs_dir = hub_dir / "models--org--repo" / "blobs"
    snap1 = repo_dir / "aaa"
    snap2 = repo_dir / "bbb" / "GGUF"
    snap1.mkdir(parents=True)
    snap2.mkdir(parents=True)
    blobs_dir.mkdir(parents=True)

    blob_hash = hashlib.sha256(b"weights").hexdigest()
    blob = blobs_dir / blob_hash
    blob.write_bytes(b"weights")
    # Snapshot 1 keeps the file top-level; snapshot 2 keeps identical
    # content nested under GGUF/ — the layout real HF GGUF repos use.
    (snap1 / "model.gguf").symlink_to(blob)
    (snap2 / "model.gguf").symlink_to(blob)

    provider = LlamaCppProvider({})
    variant = {"id": "org--repo", "provider": "llamacpp",
               "repo": "org/repo", "files": ["model.gguf"]}

    with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
        provider.delete(variant)

    assert not (snap1 / "model.gguf").exists()      # deleted variant's link gone
    assert (snap2 / "model.gguf").exists()          # nested reference survives
    assert blob.exists()                            # shared blob NOT orphan-deleted


def test_delete_unlinks_dangling_snapshot_symlink(tmp_path):
    """A snapshot file whose blob is already gone (dangling symlink) must
    still be unlinked. Regression: exists() follows symlinks and returned
    False for the dangling link, so the stale entry survived every delete
    and would break later stat() walks in reconcile/list_local."""
    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--org--repo" / "snapshots"
    snap = repo_dir / "aaa"
    snap.mkdir(parents=True)
    (hub_dir / "models--org--repo" / "blobs").mkdir()

    gguf = snap / "model.gguf"
    gguf.symlink_to(hub_dir / "models--org--repo" / "blobs" / "deadbeef")
    assert not gguf.exists()  # precondition: the link dangles

    provider = LlamaCppProvider({})
    variant = {"id": "org--repo", "provider": "llamacpp",
               "repo": "org/repo", "files": ["model.gguf"]}

    with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
        provider.delete(variant)

    assert not gguf.is_symlink()  # the stale link itself was removed


def test_delete_never_reads_file_via_read_bytes(tmp_path, monkeypatch):
    """The non-symlink fallback hash must read files in bounded chunks, never
    read_bytes(). Regression: the fallback loaded whole multi-GB GGUFs into
    memory on no-symlink filesystems (exFAT/SMB, HF_HUB_DISABLE_SYMLINKS=1)
    — the exact OOM the symlink fast path exists to prevent. Patching
    read_bytes to raise makes any regression fail loudly instead of OOMing
    the CI runner."""
    def _no_read_bytes(self):
        raise AssertionError("read_bytes() called — multi-GB OOM regression")

    monkeypatch.setattr(Path, "read_bytes", _no_read_bytes)

    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--org--repo"
    snap = repo_dir / "snapshots" / "aaa"
    snap.mkdir(parents=True)
    (repo_dir / "blobs").mkdir()
    # A regular file, not a symlink — forces the content-hash fallback.
    (snap / "model.gguf").write_bytes(b"weights")
    (repo_dir / "blobs" / hashlib.sha256(b"weights").hexdigest()).write_bytes(b"weights")

    provider = LlamaCppProvider({})
    variant = {"id": "org--repo", "provider": "llamacpp",
               "repo": "org/repo", "files": ["model.gguf"]}

    with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
        provider.delete(variant)  # must not raise

    assert not (snap / "model.gguf").exists()
    assert not list((repo_dir / "blobs").iterdir())  # orphan blob removed via chunked hash


def test_delete_removes_file_and_orphaned_blob(tmp_path):
    """delete() removes the GGUF from the snapshot and garbage-collects the
    HF cache blob it was the only reference to. Important: without blob GC a
    delete would reclaim almost no disk — the multi-GB payload lives in
    blobs/, not in the snapshot file — so orphaned blobs would pile up in the
    cache forever."""
    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--test-org--test-repo"
    snap = repo_dir / "snapshots" / "abc123"
    blobs_dir = repo_dir / "blobs"
    snap.mkdir(parents=True)
    blobs_dir.mkdir()

    gguf_content = b"fake gguf data"
    blob_hash = hashlib.sha256(gguf_content).hexdigest()
    gguf_file = snap / "model.gguf"
    blob_file = blobs_dir / blob_hash

    gguf_file.write_bytes(gguf_content)
    blob_file.write_bytes(gguf_content)  # regular-file layout sharing one payload

    provider = LlamaCppProvider({})
    variant = {
        "id": "test--model",
        "provider": "llamacpp",
        "repo": "test-org/test-repo",
        "files": ["model.gguf"],
    }

    with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
        provider.delete(variant)

    assert not gguf_file.exists()  # snapshot file deleted
    assert not blob_file.exists()  # orphaned blob deleted


def test_delete_preserves_blob_referenced_by_other_snapshot(tmp_path):
    """delete() must remove the target file from its snapshots but keep the
    blob when another snapshot still references it. Important: two variants
    of the same repo share blobs in a real HF cache — a blob GC that only
    counted the deleted variant's files would destroy the surviving
    variant's multi-GB weights."""
    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--test-org--test-repo"
    snap1 = repo_dir / "snapshots" / "abc123"
    snap2 = repo_dir / "snapshots" / "def456"
    blobs_dir = repo_dir / "blobs"
    snap1.mkdir(parents=True)
    snap2.mkdir()
    blobs_dir.mkdir()

    shared_content = b"shared data"
    blob_hash = hashlib.sha256(shared_content).hexdigest()
    blob_file = blobs_dir / blob_hash
    blob_file.write_bytes(shared_content)

    # Both snapshots reference the same blob (regular-file layout,
    # different filenames so only model.gguf is in the delete set).
    (snap1 / "model.gguf").write_bytes(shared_content)
    (snap2 / "other.gguf").write_bytes(shared_content)

    provider = LlamaCppProvider({})
    variant = {
        "id": "test--model",
        "provider": "llamacpp",
        "repo": "test-org/test-repo",
        "files": ["model.gguf"],  # only delete model.gguf
    }

    with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
        provider.delete(variant)

    assert not (snap1 / "model.gguf").exists()
    assert (snap2 / "other.gguf").exists()  # other snapshot untouched
    assert blob_file.exists()  # blob still referenced — kept


def test_delete_uses_symlink_target_not_file_contents(tmp_path):
    """Deleting a GGUF must derive the blob hash from the snapshot file's
    symlink target (blobs/<sha256>), never by reading the file body — reading
    a multi-GB GGUF into memory to hash it OOMs the process. This test makes
    the snapshot file a symlink to a blob and asserts the blob is removed
    without the file's contents ever being read."""
    hub_dir = tmp_path / "hub"
    repo_dir = hub_dir / "models--test-org--test-repo"
    snapshots_dir = repo_dir / "snapshots" / "abc123"
    blobs_dir = repo_dir / "blobs"
    snapshots_dir.mkdir(parents=True)
    blobs_dir.mkdir()

    # A real blob whose content we deliberately do NOT want read.
    blob_hash = hashlib.sha256(b"real blob content").hexdigest()
    blob_file = blobs_dir / blob_hash
    blob_file.write_bytes(b"real blob content")

    # The snapshot file is a symlink to the blob, as in a real HF cache.
    gguf_file = snapshots_dir / "model.gguf"
    gguf_file.symlink_to(blob_file)

    provider = LlamaCppProvider({})
    variant = {
        "id": "test--model",
        "provider": "llamacpp",
        "repo": "test-org/test-repo",
        "files": ["model.gguf"],
    }

    # The fast path derives the blob hash from the symlink target via
    # os.readlink, never by reading the file body. Patch read_bytes to
    # raise so a regression back to hashing the file contents fails.
    with (
        patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir),
        patch.object(
            type(gguf_file), "read_bytes", side_effect=AssertionError("read_bytes called — OOM regression")
        ),
    ):
        provider.delete(variant)

    assert not gguf_file.exists()
    assert not blob_file.exists()
