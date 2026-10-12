import dataclasses

import pytest

from modeltiers import aliases, loader, pipeline
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


def _declared(*canonicals: str) -> aliases.Aliases:
    return aliases.build([Alias(key, epoch=(f"id {n}",)) for n, key in enumerate(canonicals)], [])


def test_a_declared_key_that_reads_like_a_models_dev_key_is_an_error() -> None:
    with pytest.raises(ConfigError) as caught:
        aliases.check_spelling(_declared("minimax/minimax-m-3"), KEYS)
    assert str(caught.value) == (
        "aliases.toml: [[alias]] #1 (minimax/minimax-m-3): canonical reads the same as "
        "the key minimax/MiniMax-M3, so name matching could not tell the two apart; "
        "use that key, or one that reads differently"
    )


def test_two_declared_keys_that_read_the_same_are_an_error() -> None:
    with pytest.raises(ConfigError, match=r"#2 \(google/gemini_4_argon\): canonical reads"):
        aliases.check_spelling(_declared("google/gemini-4-argon", "google/gemini_4_argon"), KEYS)


def test_keys_that_cannot_confuse_name_matching_pass_the_spelling_check() -> None:
    entries = _declared(
        "MiniMax/minimax-m3",  # a models.dev key in another case
        "google/gemini-4-argon",  # declared, and reads like no other key
        "Google/Gemini-4-Argon",  # the same declared key again
        "zhipuai/glm-5-3-latest",  # floating: never matched by name
    )
    aliases.check_spelling(entries, [*KEYS, "zhipuai/glm-latest"])


def test_every_structure_keyed_by_source_has_a_field_per_source() -> None:
    for structure in (Alias, loader.Sources, pipeline.Identity):
        assert set(aliases.SOURCES) <= {f.name for f in dataclasses.fields(structure)}
