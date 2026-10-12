import pytest

from modeltiers import aliases
from modeltiers.aliases import Alias
from modeltiers.errors import ConfigError

KEYS = ["zhipuai/glm-5.3", "minimax/MiniMax-M3"]


def test_an_alias_to_a_models_dev_key_needs_no_source_id() -> None:
    entries = aliases.build([Alias("zhipuai/glm-5.3", inventory=(("ollama", "glm:cloud"),))], [])
    aliases.check_anchored(entries, KEYS)


def test_the_key_is_compared_without_regard_to_case() -> None:
    entries = aliases.build([Alias("MiniMax/minimax-m3", inventory=(("ollama", "m3"),))], [])
    aliases.check_anchored(entries, KEYS)


def test_an_unanchored_canonical_is_an_error() -> None:
    entries = aliases.build(
        [
            Alias("zhipuai/glm-5.3", inventory=(("ollama", "glm:cloud"),)),
            Alias("google/gemini-4-argon", inventory=(("openrouter", "google/gemini-4-argon"),)),
        ],
        [],
    )
    with pytest.raises(ConfigError) as caught:
        aliases.check_anchored(entries, KEYS)
    assert str(caught.value) == (
        "aliases.toml: [[alias]] #2 (google/gemini-4-argon): canonical is not a models.dev key "
        "and the entry lists no openrouter, arena or epoch id"
    )


@pytest.mark.parametrize("source", ["openrouter", "arena", "epoch"])
def test_one_source_id_anchors_a_declared_key(source: str) -> None:
    alias = Alias("google/gemini-4-argon", **{source: ("Gemini 4 Argon",)})
    aliases.check_anchored(aliases.build([alias], []), KEYS)


def test_a_source_id_absent_from_this_run_is_a_warning() -> None:
    entries = aliases.build(
        [
            Alias(
                "deepseek/deepseek-v4-pro-0813",
                openrouter=("deepseek/deepseek-v4-pro-0813",),
                arena=("DeepSeek V4 Pro (0813)",),
                epoch=("DeepSeek V4 Pro 0813",),
            )
        ],
        [],
    )
    present = {
        "openrouter": frozenset({"deepseek/deepseek-v4-pro-0813"}),
        "arena": frozenset({"GLM 5.3"}),
        # no "epoch" entry at all: every Epoch id is absent
    }
    assert aliases.stale_ids(entries, present) == [
        "alias for deepseek/deepseek-v4-pro-0813 names arena id 'DeepSeek V4 Pro (0813)', "
        "which is not in this run's arena data",
        "alias for deepseek/deepseek-v4-pro-0813 names epoch id 'DeepSeek V4 Pro 0813', "
        "which is not in this run's epoch data",
    ]


def test_ids_that_are_present_give_no_warning() -> None:
    entries = aliases.build([Alias("zhipuai/glm-5.3", epoch=("GLM-5.3",))], [])
    assert aliases.stale_ids(entries, {"epoch": frozenset({"GLM-5.3"})}) == []
