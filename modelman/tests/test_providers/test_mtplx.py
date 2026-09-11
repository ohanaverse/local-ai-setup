from modelman.providers.mtplx import MTPLXProvider


def _variant(name: str) -> dict:
    return {"id": f"mtplx/{name}", "provider": "mtplx", "name": name}


def test_is_downloaded_checks_org_dash_dash_model_dir(tmp_path):
    """is_downloaded() must map a registry `org/model` variant id to the
    `org--model` directory MTPLX actually creates on disk and correctly
    reject a variant whose directory doesn't exist. A wrong dir-name
    mapping would make reconcile misreport already-cached MTPLX models as
    missing (or vice versa)."""
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    (tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality").mkdir(parents=True)
    (tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality" / "weights.safetensors").write_bytes(b"x")
    assert p.is_downloaded(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")) is True
    assert p.is_downloaded(_variant("Other/Model")) is False


def test_is_downloaded_empty_dir_is_not_downloaded(tmp_path):
    """An existing but empty model directory (no weight files inside) must
    not count as downloaded. Without this check, reconcile could mark a
    partially-created or emptied MTPLX model dir as ready, exposing a model
    that can't actually be served."""
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    (tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality").mkdir(parents=True)
    assert p.is_downloaded(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")) is False


def test_list_local_maps_dir_names_back_to_repo_ids(tmp_path):
    """list_local() must translate an on-disk `org--model` directory name
    back into the registry's `org/model` variant id, alongside the
    directory's path. A broken round trip would make locally-cached MTPLX
    models invisible to (or mismatched against) the registry during
    reconcile."""
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    d = tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"
    d.mkdir(parents=True)
    (d / "w").write_bytes(b"x")
    local = p.list_local()
    assert len(local) == 1
    assert local[0]["variant_id"] == "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
    assert local[0]["path"] == str(d)


def test_size_of_sums_files(tmp_path):
    """size_of() must sum the byte sizes of every file inside a model's
    directory. Undercounting here would make the family/model size totals
    shown in the TUI misrepresent how much disk an MTPLX model actually
    consumes."""
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    d = tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"
    d.mkdir(parents=True)
    (d / "a").write_bytes(b"1234")
    (d / "b").write_bytes(b"12")
    assert p.size_of(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")) == 6


def test_download_raises(tmp_path):
    """download() must raise NotImplementedError — this is a deliberate
    manages-own-cache contract, not an unfinished stub: MTPLX caches models
    via its own CLI, and modelman must never drive a download for it. If a
    future change made this succeed instead of raising, modelman would
    silently start writing into a cache layout MTPLX itself owns."""
    import pytest

    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    with pytest.raises(NotImplementedError):
        p.download(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"))


def test_mtplx_declares_manages_own_cache():
    """MTPLXProvider must declare manages_own_cache = True — this is what
    ModelScreen._provider_can_download() reads instead of hardcoding the
    provider id, so a missing flag here would silently make MTPLX
    downloadable through DownloadManager (which raises NotImplementedError,
    per test_download_raises above)."""
    assert MTPLXProvider.manages_own_cache is True


def test_delete_removes_model_dir(tmp_path):
    """delete() must remove an MTPLX model's on-disk directory. If this
    regressed, a queued delete in the TUI would report success while
    leaving the artifact orphaned on disk, drifting from the registry's
    view that the model is gone."""
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    d = tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"
    d.mkdir(parents=True)
    (d / "w").write_bytes(b"x")
    p.delete(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"))
    assert not d.exists()


def test_delete_skips_local_path_variants(tmp_path):
    """Variants carrying an explicit local_path are treated as user-produced
    artifacts (mirroring oMLX). modelman must never delete them, even if a
    directory with the same repo-derived name happens to exist."""
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    d = tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"
    d.mkdir(parents=True)
    (d / "w").write_bytes(b"x")
    variant = {
        "id": "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
        "provider": "mtplx",
        "name": "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
        "local_path": "/data/user-model",
    }
    p.delete(variant)
    assert d.exists()


def test_dir_name_and_repo_id_round_trip_for_multi_slash_repo():
    """_dir_name/_repo_id must round-trip correctly for repo ids with more
    than one slash (org/sub/model), not just the common two-segment case.
    A lossy or one-directional mapping here would corrupt multi-segment
    repo ids everywhere MTPLX's dir-name convention is used (is_downloaded,
    list_local, delete)."""
    from modelman.providers.mtplx import _dir_name, _repo_id

    assert _dir_name("org/sub/model") == "org--sub--model"
    assert _repo_id("org--sub--model") == "org/sub/model"
    assert _repo_id(_dir_name("org/model")) == "org/model"
