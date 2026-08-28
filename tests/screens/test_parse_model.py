"""Unit tests for the parse_model helper."""

from __future__ import annotations

import pytest

from modelman.screens.forms import parse_model

# ---------------------------------------------------------------------------
# ollama: model_input is the tag verbatim
# ---------------------------------------------------------------------------


def test_parse_model_ollama_tag_verbatim():
    """For ollama, the model_input IS the tag (e.g. 'ornith-1.5:35b').
    No repo or filename. Returns (name, None, None)."""
    name, repo, filename = parse_model("ollama", "ornith-1.5:35b")
    assert name == "ornith-1.5:35b"
    assert repo is None
    assert filename is None


def test_parse_model_ollama_rejects_slash():
    """Ollama tags don't contain '/'. If the user pastes one anyway,
    raise ValueError so the form can show an inline error."""
    with pytest.raises(ValueError, match="ollama"):
        parse_model("ollama", "someuser/some-model:tag")


# ---------------------------------------------------------------------------
# llamacpp / omlx: parse on '/'
# ---------------------------------------------------------------------------


def test_parse_model_hf_whole_repo_two_segments():
    """Two '/'-separated segments = whole repo. filename is empty."""
    name, repo, filename = parse_model("llamacpp", "unsloth/Ornith-1.5-35B-GGUF")
    assert repo == "unsloth/Ornith-1.5-35B-GGUF"
    assert filename == ""  # empty string, not None — signals "no file filter"
    # name = the same string the user typed, for display
    assert name == "unsloth/Ornith-1.5-35B-GGUF"


def test_parse_model_hf_one_file_three_segments():
    """Three segments = one specific file within the repo."""
    name, repo, filename = parse_model(
        "llamacpp", "unsloth/Ornith-1.5-35B-GGUF/Ornith-1.5-35B-Q8_0.gguf"
    )
    assert repo == "unsloth/Ornith-1.5-35B-GGUF"
    assert filename == "Ornith-1.5-35B-Q8_0.gguf"
    assert name == "unsloth/Ornith-1.5-35B-GGUF/Ornith-1.5-35B-Q8_0.gguf"


def test_parse_model_hf_deep_path():
    """Subdirectory paths work: more than 3 segments."""
    name, repo, filename = parse_model("llamacpp", "org/repo/sub/dir/file.gguf")
    assert repo == "org/repo"
    assert filename == "sub/dir/file.gguf"


def test_parse_model_hf_one_segment_raises():
    """HF repos are always 'org/name'. A single segment is invalid."""
    with pytest.raises(ValueError, match="repo"):
        parse_model("llamacpp", "single-segment")


def test_parse_model_hf_empty_first_segment_raises():
    """Leading slash is not allowed."""
    with pytest.raises(ValueError):
        parse_model("llamacpp", "/org/repo")


def test_parse_model_hf_empty_model_raises():
    """Empty input is invalid for HF providers."""
    with pytest.raises(ValueError):
        parse_model("llamacpp", "")


def test_parse_model_hf_omlx_same_as_llamacpp():
    """oMLX uses the same HF parser (it also calls huggingface_hub)."""
    name, repo, filename = parse_model("omlx", "org/repo/file.safetensors")
    assert repo == "org/repo"
    assert filename == "file.safetensors"
    assert name == "org/repo/file.safetensors"


def test_parse_model_hf_whitespace_stripped():
    """Leading/trailing whitespace on the input is trimmed before parsing."""
    name, repo, filename = parse_model(
        "llamacpp", "  unsloth/Ornith-1.5-35B-GGUF/Ornith-1.5-35B-Q8_0.gguf  "
    )
    assert repo == "unsloth/Ornith-1.5-35B-GGUF"
    assert filename == "Ornith-1.5-35B-Q8_0.gguf"
