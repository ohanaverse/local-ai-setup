import pytest

from modeltiers.sources.arena import split_effort
from modeltiers.tokens import DROP, is_floating, tokens


@pytest.mark.parametrize(
    ("name", "expected"),
    [
        # The spec's worked examples: three spellings of one model.
        ("Qwen3.8-27B-4bit", ("qwen", "3", "8", "27b")),
        ("qwen3.8:27b-mlx", ("qwen", "3", "8", "27b")),
        ("alibaba/qwen3.8-27b", ("qwen", "3", "8", "27b")),
        ("Claude Opus 5.5", ("claude", "opus", "5", "5")),
        ("anthropic/claude-opus-5-5", ("claude", "opus", "5", "5")),
        # A digit followed by letters stays one piece.
        ("qwen3-8b", ("qwen", "3", "8b")),
        ("gpt-oss:120b-cloud", ("gpt", "oss", "120b")),
        # Everything up to the first slash goes: vendor, uploader, or nothing.
        ("z-ai/glm-5.3", ("glm", "5", "3")),
        ("zhipuai/glm-5.3", ("glm", "5", "3")),
        ("mlx-community/Qwen3.8-27B-4bit", ("qwen", "3", "8", "27b")),
        ("GLM-5.3", ("glm", "5", "3")),
        # Only the first slash is a prefix; a later one is a separator.
        ("a/b/c", ("b", "c")),
        # A parenthesis is a separator, so `(0813)` stays as a piece.
        ("DeepSeek V4 Pro (0813)", ("deepseek", "v", "4", "pro", "0813")),
        ("deepseek/deepseek-v4-pro-0813", ("deepseek", "v", "4", "pro", "0813")),
        # `max` is not in the drop list: `Qwen3.8 Max` is a model.
        ("Qwen3.8 Max", ("qwen", "3", "8", "max")),
        # `it` is dropped from the name and from the key.
        ("Gemma 4 31B IT", ("gemma", "4", "31b")),
        ("google/gemma-4-31b-it", ("gemma", "4", "31b")),
        ("gemma4:31b-mlx", ("gemma", "4", "31b")),
        # Dropped whole, before the letter-digit split could cut them up.
        ("Qwen3.8-27B-nvfp4", ("qwen", "3", "8", "27b")),
        ("qwen3.8-27b-bf16", ("qwen", "3", "8", "27b")),
        ("model_fp8", ("model",)),
        # Mixed case and underscores.
        ("minimax/MiniMax-M3", ("minimax", "m", "3")),
        ("Minimax M3", ("minimax", "m", "3")),
        ("Qwen-1_8B", ("qwen", "1", "8b")),
        # A dot is cut only between digits.
        ("v1.x", ("v", "1.x")),
        ("lfm-2.5-2.6b", ("lfm", "2", "5", "2", "6b")),
        # Nothing left.
        ("", ()),
        ("vendor/", ()),
        ("mlx-4bit:latest", ()),
    ],
)
def test_tokens(name: str, expected: tuple[str, ...]) -> None:
    assert tokens(name) == expected


def test_qwen3_8_never_matches_qwen3_8b() -> None:
    assert tokens("qwen3.8") == ("qwen", "3", "8")
    assert tokens("qwen3-8b") != tokens("qwen3.8")


def test_an_arena_name_matches_its_key_once_the_effort_is_removed() -> None:
    name, effort = split_effort("Claude Opus 5.5 (High)")
    assert effort == "high"
    assert tokens(name) == tokens("anthropic/claude-opus-5-5")


def test_max_is_removed_as_an_effort_and_kept_as_a_name() -> None:
    assert tokens(split_effort("GLM 5.3 (Max)")[0]) == tokens("zhipuai/glm-5.3")
    assert tokens(split_effort("Qwen3.8 Max")[0]) == ("qwen", "3", "8", "max")


def test_the_drop_list_is_the_specs() -> None:
    assert sorted(DROP) == sorted(
        ["cloud", "free", "batch", "latest", "it", "mlx", "4bit", "6bit", "8bit"]
        + ["nvfp4", "fp4", "fp8", "fp16", "bf16"]
    )


def test_a_latest_key_is_floating() -> None:
    assert is_floating("mistral/mistral-large-latest")
    assert is_floating("Mistral/Mistral-Large-LATEST")
    assert not is_floating("mistral/mistral-large-4")
    assert not is_floating("vendor/latest-model")
