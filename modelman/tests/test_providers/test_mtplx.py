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
    (
        tmp_path
        / "models"
        / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"
        / "weights.safetensors"
    ).write_bytes(b"x")
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


# --- artifact_paths, removable_paths and the shared-artifact guard (#227) ---


def _local_variant(name: str, local_path: str) -> dict:
    return {**_variant(name), "local_path": local_path}


def test_artifact_paths_names_the_directory_an_entry_lives_in(tmp_path):
    # Where the entry's weights are: the user's directory for a local_path
    # entry, otherwise MTPLX's own `org--model` cache directory — whether or
    # not it exists yet, since the guard must work before a download too.
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    user = str(tmp_path / "user-model")
    assert p.artifact_paths(_local_variant("Org/M", user)) == frozenset([user])
    assert p.artifact_paths(_variant("Org/M")) == frozenset([str(tmp_path / "models" / "Org--M")])
    assert p.artifact_paths({"id": "mtplx/x", "provider": "mtplx"}) == frozenset()


def test_removable_paths_is_exactly_what_delete_removes(tmp_path):
    # The contract the guard rests on, against the real delete(): a
    # local_path entry removes nothing (so it names nothing), a cached entry
    # removes its cache directory and nothing else.
    models = tmp_path / "models"
    user = tmp_path / "user-model"
    p = MTPLXProvider({"model_dir": str(models)})
    for label, variant in (
        ("local_path entry", _local_variant("Org/M", str(user))),
        ("cached entry", _variant("Org/M")),
    ):
        for d in (models / "Org--M", models / "Org--Other", user):
            d.mkdir(parents=True, exist_ok=True)
            (d / "weights.safetensors").write_bytes(b"x")
        removable = {str(x) for x in p.removable_paths(variant)}
        p.delete(variant)
        for d in (models / "Org--M", models / "Org--Other", user):
            assert d.exists() == (str(d) not in removable), f"{label}: {d.name}"
    assert p.removable_paths(_local_variant("Org/M", str(user))) == frozenset()


def _mtplx_entry(model_id: str, name: str, local_path: str | None = None):
    from modelman.registry import Fetch, ModelEntry

    return ModelEntry(
        id=model_id,
        family="f",
        provider_id="mtplx",
        model_name=name,
        fetch=Fetch(local_path=local_path) if local_path else None,
    )


def test_guard_reports_nothing_for_two_entries_on_one_local_path(tmp_path, shared_owner):
    # #227: neither delete removes the user's directory, so there is no
    # conflict to report from either side.
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    user = str(tmp_path / "user-model")
    a = _mtplx_entry("mtplx/a", "Org/A", user)
    b = _mtplx_entry("mtplx/b", "Org/B", user)
    assert shared_owner(p, a, b) is None
    assert shared_owner(p, b, a) is None


def test_guard_refuses_a_cached_delete_another_entry_depends_on(tmp_path, shared_owner):
    # Two entries naming the same model share one cache directory, and a
    # local_path entry can point INTO the cache: in both cases deleting the
    # cached entry would rmtree what the other one uses.
    models = tmp_path / "models"
    p = MTPLXProvider({"model_dir": str(models)})
    a = _mtplx_entry("mtplx/a", "Org/M")
    twin = _mtplx_entry("mtplx/twin", "Org/M")
    assert shared_owner(p, a, twin) == "mtplx/twin"
    pointing_in = _mtplx_entry("mtplx/local", "Org/Elsewhere", str(models / "Org--M"))
    assert shared_owner(p, a, pointing_in) == "mtplx/local"
    # The local_path entry's own delete removes nothing of the cached one's.
    assert shared_owner(p, pointing_in, a) is None


def test_guard_sees_a_local_path_however_it_is_spelled(tmp_path, shared_owner, respell):
    # #235: the paths were compared as strings, so a local_path naming the
    # cache directory any other way than the string MTPLX derives for it was
    # not seen as an owner, and deleting the cached entry removed it.
    models = tmp_path / "models"
    (models / "Org--M").mkdir(parents=True)
    (models / "Org--M" / "weights.safetensors").write_bytes(b"x")
    p = MTPLXProvider({"model_dir": str(models)})
    cached = _mtplx_entry("mtplx/a", "Org/M")
    pointing_in = _mtplx_entry("mtplx/local", "Org/Elsewhere", respell(models / "Org--M"))
    assert shared_owner(p, cached, pointing_in) == "mtplx/local"
    assert shared_owner(p, pointing_in, cached) is None


def test_guard_sees_an_entry_nested_inside_the_directory_it_would_remove(tmp_path, shared_owner):
    # #241: only an identical directory counted as shared, so an entry living
    # INSIDE the cache directory (a quantize output written beside its
    # source) was removed with it — a user-produced artifact, which modelman
    # says it never deletes.
    models = tmp_path / "models"
    (models / "Org--M" / "dwq-out").mkdir(parents=True)
    p = MTPLXProvider({"model_dir": str(models)})
    cached = _mtplx_entry("mtplx/a", "Org/M")
    nested = _mtplx_entry("mtplx/dwq", "Org/Dwq", str(models / "Org--M" / "dwq-out"))
    assert shared_owner(p, cached, nested) == "mtplx/dwq"
    assert shared_owner(p, nested, cached) is None


def test_guard_sees_an_entry_whose_directory_contains_the_one_it_would_remove(
    tmp_path, shared_owner
):
    # The reverse nesting: an entry naming a parent of the cache directory
    # loses part of what it points at. Refusing a delete can be undone; the
    # rmtree cannot.
    models = tmp_path / "models"
    (models / "Org--M").mkdir(parents=True)
    p = MTPLXProvider({"model_dir": str(models)})
    cached = _mtplx_entry("mtplx/a", "Org/M")
    whole_pool = _mtplx_entry("mtplx/pool", "Org/Pool", str(models))
    assert shared_owner(p, cached, whole_pool) == "mtplx/pool"


def test_guard_does_not_take_a_name_prefix_for_nesting(tmp_path, shared_owner):
    # models/Org--M2 starts with models/Org--M and is not inside it.
    models = tmp_path / "models"
    (models / "Org--M").mkdir(parents=True)
    (models / "Org--M2").mkdir()
    p = MTPLXProvider({"model_dir": str(models)})
    cached = _mtplx_entry("mtplx/a", "Org/M")
    neighbour = _mtplx_entry("mtplx/n", "Org/N", str(models / "Org--M2"))
    assert shared_owner(p, cached, neighbour) is None
