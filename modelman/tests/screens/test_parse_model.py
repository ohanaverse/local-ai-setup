"""Unit tests for the parse_model helper."""

from __future__ import annotations

import pytest

from modelman.screens import forms
from modelman.screens.forms import default_form_kind, parse_dual_model, parse_model

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


# ---------------------------------------------------------------------------
# native / openrouter: plain model names
# ---------------------------------------------------------------------------


def test_parse_model_native_blank_defaults_to_native_sentinel():
    name, repo, filename = parse_model("claude", "", is_native=True)
    assert name == "native"
    assert repo is None
    assert filename is None


def test_parse_model_native_named_is_verbatim():
    name, repo, filename = parse_model("claude", "opus", is_native=True)
    assert name == "opus"
    assert repo is None
    assert filename is None


def test_parse_model_openrouter_plain_string_no_split():
    name, repo, filename = parse_model("openrouter", "anthropic/claude-opus")
    assert name == "anthropic/claude-opus"
    assert repo is None
    assert filename is None


# ---------------------------------------------------------------------------
# default_form_kind: provider -> form kind mapping
# ---------------------------------------------------------------------------


def test_default_form_kind_mlx_lm_server_is_dual_model():
    """mlx_lm_server represents a target+draft pairing, which needs its own
    field group (4 inputs) — not the single-Input 'local-only' kind used by
    llamacpp/omlx. Confirms the new provider maps to the new kind."""
    assert default_form_kind("mlx_lm_server") == "dual-model"


def test_hf_repo_providers_single_source_of_truth():
    """Regression guard for the plan's called-out hazard: the
    ('llamacpp', 'omlx') provider tuple used to be hardcoded independently
    in default_form_kind() and parse_model(). If those two ever drift
    apart again, parse_model would apply the wrong parsing branch for
    whatever default_form_kind now calls 'local-only'. Both functions are
    checked against the single hoisted HF_REPO_PROVIDERS constant here so
    a future edit to one without the other fails this test."""
    for provider in forms.HF_REPO_PROVIDERS:
        assert default_form_kind(provider) == "local-only"
        # parse_model must apply org/repo splitting for this provider: a
        # bare, slash-free model string is rejected as an invalid HF repo
        # rather than silently accepted as a plain string.
        with pytest.raises(ValueError, match="repo"):
            parse_model(provider, "single-segment")
    # Providers deliberately outside the HF set must not be treated as
    # HF-repo-shaped by either function.
    for provider in ("ollama", "mlx_lm_server", "openrouter"):
        assert provider not in forms.HF_REPO_PROVIDERS


# ---------------------------------------------------------------------------
# parse_dual_model: mlx_lm_server target+draft pairing (4 inputs)
# ---------------------------------------------------------------------------


def test_parse_dual_model_both_sides_repo():
    """The common case: target and draft are both plain HF repos."""
    result = parse_dual_model("org/target-repo", "", "org/draft-repo", "")
    assert result == ("org/target-repo", None, "org/draft-repo", None)


def test_parse_dual_model_both_sides_local_path():
    """Both sides can be locally-produced mlx-lm directories instead of HF
    repos (Feature 1 output fed into a Feature 2 pairing)."""
    result = parse_dual_model("", "/data/target", "", "/data/draft")
    assert result == (None, "/data/target", None, "/data/draft")


def test_parse_dual_model_mixed_sourcing():
    """A pairing may mix sourcing per side (repo target + local-path
    draft, or vice versa) — the plan calls this out explicitly as a case
    the provider layer must support, so the form-level parser must be
    able to produce it too."""
    result = parse_dual_model("org/target-repo", "", "", "/data/draft")
    assert result == ("org/target-repo", None, None, "/data/draft")


def test_parse_dual_model_side_missing_both_raises():
    """Each side needs exactly one source; leaving both blank must be
    rejected at parse time (naming the offending side) rather than
    silently producing a variant the provider can never resolve."""
    with pytest.raises(ValueError, match="target"):
        parse_dual_model("", "", "org/draft-repo", "")


def test_parse_dual_model_side_sets_both_raises():
    """Setting both a repo and a local path on the same side is
    ambiguous — reject it (naming the offending side) rather than
    silently picking one."""
    with pytest.raises(ValueError, match="draft"):
        parse_dual_model("org/target-repo", "", "org/draft-repo", "/data/draft")


def test_parse_dual_model_strips_whitespace():
    """Leading/trailing whitespace on any of the four inputs is trimmed
    before the mutual-exclusion check, matching parse_model's convention."""
    result = parse_dual_model(" org/target-repo ", "  ", "  ", " /data/draft ")
    assert result == ("org/target-repo", None, None, "/data/draft")
