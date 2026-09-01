from pathlib import Path

import pytest

from modelman.manifest import (
    ManifestError,
    load_manifest,
    save_manifest,
)


def test_load_manifest_from_file(tmp_path):
    family_file = tmp_path / "ornith-1.5.yaml"
    family_file.write_text(Path("tests/fixtures/sample_family.yaml").read_text())

    manifest = load_manifest("ornith-1.5", family_dir=tmp_path)

    assert manifest.family == "ornith-1.5"
    assert manifest.display_name == "Ornith 1.5"
    assert len(manifest.variants) == 3
    assert manifest.variants[0]["id"] == "ollama-35b"
    assert manifest.variants[1]["repo"] == "ornith-ai/Ornith-1.5-35B-A3B-GGUF"
    assert "ollama-35b" in manifest.downloaded


def test_save_and_reload_round_trip(tmp_path):
    family_file = tmp_path / "ornith-1.5.yaml"
    family_file.write_text(Path("tests/fixtures/sample_family.yaml").read_text())

    original = load_manifest("ornith-1.5", family_dir=tmp_path)
    original.downloaded["llamacpp-35b-q4"] = {
        "downloaded_at": "2026-08-26T08:00:00Z",
        "local_path": "/Users/keith/.cache/huggingface/hub/.../Q4_K_M.gguf",
    }
    save_manifest(original, family_file)

    reloaded = load_manifest("ornith-1.5", family_dir=tmp_path)
    assert "llamacpp-35b-q4" in reloaded.downloaded
    assert reloaded.downloaded["llamacpp-35b-q4"]["local_path"].endswith("Q4_K_M.gguf")


def test_load_missing_manifest_raises(tmp_path):
    with pytest.raises(ManifestError):
        load_manifest("nonexistent", family_dir=tmp_path)


def test_model_info_round_trip(tmp_path):
    family_file = tmp_path / "ornith-1.5.yaml"
    family_file.write_text(Path("tests/fixtures/sample_family.yaml").read_text())
    manifest = load_manifest("ornith-1.5", family_dir=tmp_path)

    manifest.variants[0]["model_info"] = {  # type: ignore[typeddict-unknown-key]
        "supports_function_calling": True,
        "mode": "chat",
    }
    save_manifest(manifest, family_file)

    reloaded = load_manifest("ornith-1.5", family_dir=tmp_path)
    assert reloaded.variants[0]["model_info"] == {  # type: ignore[typeddict-unknown-key]
        "supports_function_calling": True,
        "mode": "chat",
    }
