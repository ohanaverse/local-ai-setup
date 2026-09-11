from modelman.providers.mtplx import MTPLXProvider


def _variant(name: str) -> dict:
    return {"id": f"mtplx/{name}", "provider": "mtplx", "name": name}


def test_is_downloaded_checks_org_dash_dash_model_dir(tmp_path):
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    (tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality").mkdir(parents=True)
    (tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality" / "weights.safetensors").write_bytes(b"x")
    assert p.is_downloaded(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")) is True
    assert p.is_downloaded(_variant("Other/Model")) is False


def test_is_downloaded_empty_dir_is_not_downloaded(tmp_path):
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    (tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality").mkdir(parents=True)
    assert p.is_downloaded(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")) is False


def test_list_local_maps_dir_names_back_to_repo_ids(tmp_path):
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    d = tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"
    d.mkdir(parents=True)
    (d / "w").write_bytes(b"x")
    local = p.list_local()
    assert len(local) == 1
    assert local[0]["variant_id"] == "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
    assert local[0]["path"] == str(d)


def test_size_of_sums_files(tmp_path):
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    d = tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"
    d.mkdir(parents=True)
    (d / "a").write_bytes(b"1234")
    (d / "b").write_bytes(b"12")
    assert p.size_of(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")) == 6


def test_download_raises(tmp_path):
    import pytest

    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    with pytest.raises(NotImplementedError):
        p.download(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"))


def test_delete_removes_model_dir(tmp_path):
    p = MTPLXProvider({"model_dir": str(tmp_path / "models")})
    d = tmp_path / "models" / "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"
    d.mkdir(parents=True)
    (d / "w").write_bytes(b"x")
    p.delete(_variant("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"))
    assert not d.exists()
