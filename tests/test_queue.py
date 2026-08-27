from pathlib import Path
from unittest.mock import MagicMock

from modelman.manifest import FamilyManifest
from modelman.queue import PendingChanges


def _manifest_with_downloads(tmp_path):
    fam_path = tmp_path / "fam.yaml"
    m = FamilyManifest(
        family="f",
        display_name="F",
        variants=[
            {"id": "a", "provider": "ollama", "name": "f:a"},
            {
                "id": "b",
                "provider": "llamacpp",
                "name": "f:b",
                "repo": "org/repo",
                "files": ["x.gguf"],
            },
        ],
    )
    m.mark_downloaded("a", str(tmp_path / "downloaded-a"))
    Path(m.downloaded["a"]["local_path"]).mkdir()
    m.mark_downloaded("b", str(tmp_path / "downloaded-b"))
    Path(m.downloaded["b"]["local_path"]).write_bytes(b"old")
    return m, fam_path


def test_apply_deletes_before_downloads(tmp_path):
    """On apply, delete steps must run before download steps (free disk first)."""
    m, fam_path = _manifest_with_downloads(tmp_path)
    order: list[str] = []

    provider_ollama = MagicMock()
    provider_ollama.download.return_value = str(tmp_path / "new-a")
    provider_ollama.delete.return_value = None
    provider_ollama.name = "ollama"

    provider_llama = MagicMock()
    provider_llama.download.return_value = str(tmp_path / "new-b")
    provider_llama.delete.return_value = None
    provider_llama.name = "llamacpp"

    def track_delete(variant):
        order.append(f"delete:{variant['id']}")

    def track_download(variant):
        order.append(f"download:{variant['id']}")
        return f"/tmp/new-{variant['id']}"

    provider_ollama.delete.side_effect = track_delete
    provider_ollama.download.side_effect = track_download
    provider_llama.delete.side_effect = track_delete
    provider_llama.download.side_effect = track_download

    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": provider_ollama, "llamacpp": provider_llama},
        deletes=[m.variants[0]],
        downloads=[m.variants[1]],
    )
    pending.apply()

    assert order.index("delete:a") < order.index("download:b")
    assert "a" not in m.downloaded
    assert "b" in m.downloaded
    assert fam_path.exists()


def test_apply_collects_failures(tmp_path):
    m, fam_path = _manifest_with_downloads(tmp_path)
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    provider.download.side_effect = RuntimeError("network down")

    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": provider},
        downloads=[m.variants[0]],
    )
    pending.apply()

    assert fam_path.exists()
    assert pending.failures
    assert "network down" in str(pending.failures[0])


def test_apply_empty_is_noop(tmp_path):
    m, fam_path = _manifest_with_downloads(tmp_path)
    pending = PendingChanges(manifest=m, manifest_path=fam_path, providers={})
    pending.apply()
    assert not fam_path.exists()


def test_apply_download_cancelled_is_not_a_failure(tmp_path):
    """When a provider raises DownloadCancelled mid-download, apply()
    must emit apply:cancelled, NOT record a failure and NOT save."""
    from modelman.providers._progress import DownloadCancelled

    fam_path = tmp_path / "fam.yaml"
    m = FamilyManifest(
        family="f",
        display_name="F",
        variants=[
            {
                "id": "b",
                "provider": "llamacpp",
                "name": "f:b",
                "repo": "org/repo",
                "files": ["x.gguf"],
            },
        ],
    )
    provider = MagicMock()
    provider.name = "llamacpp"
    provider.download.side_effect = DownloadCancelled("weights.bin")

    progress_lines: list[str] = []
    events: list[str] = []

    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"llamacpp": provider},
        downloads=[m.variants[0]],
    )
    pending.apply(on_event=events.append, on_progress=progress_lines.append)

    # Cancelled cleanly; not recorded as a failure.
    assert pending.failures == []
    assert any(tag.startswith("download:cancelled") for tag in events)
    assert "apply:cancelled" in events
    assert "apply:done" not in events
    # Manifest must NOT be saved on cancel.
    assert not fam_path.exists()


def test_apply_download_fail_includes_reason_in_event(tmp_path):
    """When a download raises, the fail event must include the
    exception so the StatusScreen can show WHY it failed (otherwise
    the user sees just "Failed to download X" with no clue)."""
    m, fam_path = _manifest_with_downloads(tmp_path)
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    provider.download.side_effect = ConnectionError("dial tcp: i/o timeout")

    events: list[str] = []

    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": provider},
        downloads=[m.variants[0]],
    )
    pending.apply(on_event=events.append)

    fail_events = [t for t in events if t.startswith("download:fail")]
    assert fail_events, f"expected a download:fail event; got {events}"
    fail = fail_events[0]
    parts = fail.split("|", 3)
    # 4 fields: verb:status|vid|label|reason
    assert len(parts) == 4, f"expected 4 pipe-delimited fields; got {fail!r}"
    assert "i/o timeout" in parts[3]


def test_apply_save_fail_includes_reason_in_event(tmp_path):
    """On save failure, the event should carry the underlying error."""
    m, fam_path = _manifest_with_downloads(tmp_path)
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    provider.download.return_value = str(tmp_path / "downloaded-a-new")

    # Make the manifest unwritable so save raises. save_manifest
    # creates parents, so we can't rely on a missing dir; force a
    # genuine I/O error by patching yaml.safe_dump on the manifest
    # module to raise.
    from modelman import manifest as manifest_mod

    orig = manifest_mod.yaml.safe_dump

    def boom(*a, **kw):
        raise OSError("disk full")

    manifest_mod.yaml.safe_dump = boom
    try:
        events: list[str] = []
        pending = PendingChanges(
            manifest=m,
            manifest_path=fam_path,
            providers={"ollama": provider},
            downloads=[m.variants[0]],
        )
        pending.apply(on_event=events.append)
    finally:
        manifest_mod.yaml.safe_dump = orig

    save_fail = [t for t in events if t.startswith("save:fail")]
    assert save_fail
    parts = save_fail[0].split("|", 3)
    # save:fail is global, so verb only (no vid); reason is the second pipe field.
    assert parts[0] == "save:fail"
    assert len(parts) >= 2
    assert parts[1], f"expected a reason after the pipe; got {save_fail[0]!r}"
    assert "disk full" in parts[1]


def test_apply_delete_fail_includes_reason_in_event(tmp_path):
    """Delete failures carry an exception reason in the event too."""
    fam_path = tmp_path / "fam.yaml"
    m = FamilyManifest(
        family="f",
        display_name="F",
        variants=[
            {"id": "a", "provider": "ollama", "name": "f:a"},
        ],
    )
    m.mark_downloaded("a", str(tmp_path / "dl-a"))
    Path(m.downloaded["a"]["local_path"]).write_bytes(b"x")

    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.side_effect = PermissionError("read-only fs")

    events: list[str] = []
    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": provider},
        deletes=[m.variants[0]],
    )
    pending.apply(on_event=events.append)

    fails = [t for t in events if t.startswith("delete:fail")]
    assert fails
    parts = fails[0].split("|", 3)
    assert len(parts) == 4
    assert "read-only fs" in parts[3]


def test_apply_download_done_includes_size_of_file(tmp_path):
    """The download:done event should carry the on-disk size of the
    downloaded file (e.g. "21.7 GB") so the StatusScreen can show
    concrete proof the download landed at the expected size, not
    silently produce an empty file.

    A successful download must not look like a 0-byte "empty download"
    in the log just because snapshot_download skipped real network
    fetches (cache hit, resume, etc.)."""
    m, fam_path = _manifest_with_downloads(tmp_path)
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None

    real_path = tmp_path / "downloaded.bin"
    real_path.write_bytes(b"x" * (1536 * 1024 * 1024))  # ~ 1.5 GB
    provider.download.return_value = str(real_path)

    events: list[str] = []
    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": provider},
        downloads=[m.variants[0]],
    )
    pending.apply(on_event=events.append)

    done_events = [t for t in events if t.startswith("download:done")]
    assert done_events
    parts = done_events[0].split("|", 3)
    # expect 4 fields: verb:status|vid|label|size
    assert len(parts) == 4
    assert "1.5 GB" in parts[3], parts[3]


def test_apply_download_done_omits_size_when_zero_or_unreadable(tmp_path):
    """If stat-ing the path fails or returns 0 (extremely defensive),
    the 4th field is dropped so older consumers still see a 3-field
    event."""
    m, fam_path = _manifest_with_downloads(tmp_path)
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    # Path that doesn't exist — stat() raises.
    provider.download.return_value = str(tmp_path / "nope-does-not-exist.bin")

    events: list[str] = []
    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": provider},
        downloads=[m.variants[0]],
    )
    pending.apply(on_event=events.append)

    done_events = [t for t in events if t.startswith("download:done")]
    assert done_events
    assert len(done_events[0].split("|", 3)) == 3, done_events[0]
