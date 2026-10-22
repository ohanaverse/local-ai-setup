from pathlib import Path

import pytest

from modeltiers import aliases
from modeltiers.aliases import Alias, Aliases, Ignore
from modeltiers.errors import ConfigError

FIXTURE = Path(__file__).parent / "fixtures" / "aliases.toml"

ALIAS = """
[[alias]]
canonical = "deepseek/deepseek-v4-pro-0813"
inventory = [{ provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }]
arena = ["DeepSeek V4 Pro (0813)"]
"""


def test_the_fixture_file_loads() -> None:
    loaded = aliases.load_aliases(FIXTURE)
    assert loaded.origin == "aliases.toml"
    assert loaded.aliases == (
        Alias(
            canonical="deepseek/deepseek-v4-pro-0813",
            note="ollama :cloud and :0813-cloud shared digest 6ed7420bc3a4 on 2026-10-12",
            inventory=(("ollama", "deepseek-v4-pro:cloud"),),
            arena=("DeepSeek V4 Pro (0813)",),
            epoch=("DeepSeek V4 Pro 0813",),
        ),
    )
    assert loaded.ignores == (
        Ignore(reason="embedding model", inventory=("ollama", "nomic-embed-text:latest")),
        Ignore(
            reason="fixture: stands for a name no key will ever match", source="epoch", id="GPT-5"
        ),
    )
    assert loaded.inventory_alias == {
        ("ollama", "deepseek-v4-pro:cloud"): "deepseek/deepseek-v4-pro-0813"
    }
    assert loaded.source_alias == {
        ("arena", "DeepSeek V4 Pro (0813)"): "deepseek/deepseek-v4-pro-0813",
        ("epoch", "DeepSeek V4 Pro 0813"): "deepseek/deepseek-v4-pro-0813",
    }
    assert loaded.inventory_ignore == {("ollama", "nomic-embed-text:latest"): "embedding model"}
    assert loaded.source_ignore == {
        ("epoch", "GPT-5"): "fixture: stands for a name no key will ever match"
    }


def test_an_empty_file_is_no_aliases() -> None:
    assert aliases.parse_aliases("") == Aliases()


def test_an_arena_id_loses_its_effort_parenthesis() -> None:
    parsed = aliases.parse_aliases(
        """
[[alias]]
canonical = "deepseek/deepseek-v4-pro-0813"
arena = ["DeepSeek V4 Pro (High) (0813)"]

[[ignore]]
source = "arena"
id = "Gemini 4 Argon (Max)"
reason = "no models.dev key yet"

[[ignore]]
source = "epoch"
id = "Gemini 2.5 Pro (Max)"
reason = "only Arena ids carry an effort"
"""
    )
    assert ("arena", "DeepSeek V4 Pro (0813)") in parsed.source_alias
    assert ("arena", "Gemini 4 Argon") in parsed.source_ignore
    assert ("epoch", "Gemini 2.5 Pro (Max)") in parsed.source_ignore


def test_an_entry_built_in_code_loses_its_effort_parenthesis_too() -> None:
    built = aliases.build(
        [Alias("zhipuai/glm-5.3", arena=("GLM 5.3 (Max)",), epoch=("GLM-5.3 (Max)",))],
        [
            Ignore("no models.dev key yet", source="arena", id="Gemini 4 Argon (Max)"),
            Ignore("only Arena ids carry an effort", source="epoch", id="Gemini 2.5 Pro (Max)"),
        ],
    )
    assert built.aliases[0].arena == ("GLM 5.3",)
    assert built.source_alias == {
        ("arena", "GLM 5.3"): "zhipuai/glm-5.3",
        ("epoch", "GLM-5.3 (Max)"): "zhipuai/glm-5.3",
    }
    assert set(built.source_ignore) == {
        ("arena", "Gemini 4 Argon"),
        ("epoch", "Gemini 2.5 Pro (Max)"),
    }
    # The warning names the id the records are compared with.
    assert aliases.stale_ids(built, {"arena": frozenset({"GLM 5.3"})}) == [
        "alias for zhipuai/glm-5.3 names epoch id 'GLM-5.3 (Max)', "
        "which is not in this run's epoch data"
    ]


def test_a_duplicate_inventory_pair_is_an_error() -> None:
    text = (
        ALIAS
        + """
[[alias]]
canonical = "deepseek/deepseek-v4-pro"
inventory = [{ provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }]
"""
    )
    with pytest.raises(ConfigError) as caught:
        aliases.parse_aliases(text)
    assert str(caught.value) == (
        "aliases.toml: inventory pair ollama/deepseek-v4-pro:cloud is claimed twice: "
        "by [[alias]] #1 (deepseek/deepseek-v4-pro-0813) "
        "and by [[alias]] #2 (deepseek/deepseek-v4-pro)"
    )


def test_an_alias_and_an_ignore_for_one_pair_is_an_error() -> None:
    text = (
        ALIAS
        + """
[[ignore]]
inventory = { provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }
reason = "listed twice"
"""
    )
    with pytest.raises(
        ConfigError, match=r"claimed twice: by \[\[alias\]\] #1 .* \[\[ignore\]\] #1"
    ):
        aliases.parse_aliases(text)


def test_a_duplicate_source_id_is_an_error() -> None:
    # The second claim spells the Arena id with its effort; it is the same id.
    text = (
        ALIAS
        + """
[[alias]]
canonical = "deepseek/deepseek-v4-pro"
arena = ["DeepSeek V4 Pro (High) (0813)"]
"""
    )
    with pytest.raises(ConfigError) as caught:
        aliases.parse_aliases(text)
    assert str(caught.value) == (
        "aliases.toml: arena id 'DeepSeek V4 Pro (0813)' is claimed twice: "
        "by [[alias]] #1 (deepseek/deepseek-v4-pro-0813) "
        "and by [[alias]] #2 (deepseek/deepseek-v4-pro)"
    )


def test_one_id_under_two_sources_is_two_ids() -> None:
    parsed = aliases.parse_aliases(
        """
[[alias]]
canonical = "zhipuai/glm-5.3"
arena = ["GLM-5.3"]
epoch = ["GLM-5.3"]
"""
    )
    assert len(parsed.source_alias) == 2


def test_one_entry_that_lists_an_id_twice_is_an_error() -> None:
    text = '[[alias]]\ncanonical = "zhipuai/glm-5.3"\nepoch = ["GLM-5.3", "GLM-5.3"]\n'
    with pytest.raises(ConfigError, match="epoch id 'GLM-5.3' is claimed twice"):
        aliases.parse_aliases(text)


@pytest.mark.parametrize(
    "entry",
    [
        'inventory = { provider_id = "ollama", model_name = "nomic-embed-text:latest" }',
        'inventory = { provider_id = "ollama", model_name = "x" }\nreason = ""',
        'inventory = { provider_id = "ollama", model_name = "x" }\nreason = "  "',
        'source = "epoch"\nid = "Mistral Large"\nreason = 7',
    ],
)
def test_an_ignore_without_a_reason_is_an_error(entry: str) -> None:
    with pytest.raises(ConfigError) as caught:
        aliases.parse_aliases(f"[[ignore]]\n{entry}\n")
    assert str(caught.value) == "aliases.toml: [[ignore]] #1: an ignore needs a reason"


@pytest.mark.parametrize(
    ("text", "message"),
    [
        ("[[alias]", "aliases.toml: not TOML"),
        ("[alias]\ncanonical = 'a/b'\n", r"alias is not an array of tables; write \[\[alias\]\]"),
        ("[[aliases]]\ncanonical = 'a/b'\n", "unknown table 'aliases'"),
        ("[[alias]]\nnote = 'x'\n", r"\[\[alias\]\] #1: canonical is missing, empty"),
        ("[[alias]]\ncanonical = 'a/b'\nopenroutr = ['x']\n", r"#1: unknown key 'openroutr'"),
        ("[[alias]]\ncanonical = 'a/b'\nnote = 7\n", r"#1 \(a/b\): note is not a string"),
        ("[[alias]]\ncanonical = 'a/b'\narena = 'x'\n", "arena is not a list of non-empty"),
        ("[[alias]]\ncanonical = 'a/b'\nepoch = ['']\n", "epoch is not a list of non-empty"),
        (
            "[[alias]]\ncanonical = 'a/b'\ninventory = { provider_id = 'p', model_name = 'm' }\n",
            "inventory is not a list of pairs",
        ),
        (
            "[[alias]]\ncanonical = 'a/b'\ninventory = ['ollama/x']\n",
            r"inventory\[0\]: expected \{ provider_id = ..., model_name = ... \}",
        ),
        (
            "[[alias]]\ncanonical = 'a/b'\ninventory = [{ provider_id = 'p' }]\n",
            r"inventory\[0\]: model_name is missing",
        ),
        (
            "[[alias]]\ncanonical = 'a/b'\n"
            "inventory = [{ provider_id = 'p', model_name = 'm', id = 'p/m' }]\n",
            r"inventory\[0\]: unknown key 'id'",
        ),
        (
            "[[ignore]]\nreason = 'r'\n",
            r"\[\[ignore\]\] #1: give either inventory, or source and id",
        ),
        (
            "[[ignore]]\nreason = 'r'\nsource = 'epoch'\nid = 'x'\n"
            "inventory = { provider_id = 'p', model_name = 'm' }\n",
            "give either inventory, or source and id",
        ),
        ("[[ignore]]\nreason = 'r'\nsource = 'epoch'\n", "#1: id is missing"),
        ("[[ignore]]\nreason = 'r'\nid = 'x'\n", "#1: source is missing"),
        (
            "[[ignore]]\nreason = 'r'\nsource = 'modelsdev'\nid = 'x'\n",
            "source 'modelsdev' is not one of openrouter, arena, epoch",
        ),
        (
            "[[ignore]]\nreason = 'r'\nsource = 'epoch'\nid = 'x'\nnote = 'n'\n",
            "unknown key 'note'",
        ),
    ],
)
def test_a_file_in_another_shape_is_a_config_error(text: str, message: str) -> None:
    with pytest.raises(ConfigError, match=message):
        aliases.parse_aliases(text)


def test_the_message_names_the_file_it_was_given(tmp_path: Path) -> None:
    path = tmp_path / "mine.toml"
    path.write_text("[[ignore]]\nsource = 'epoch'\nid = 'x'\n")
    with pytest.raises(
        ConfigError, match=r"^mine\.toml: \[\[ignore\]\] #1: an ignore needs a reason$"
    ):
        aliases.load_aliases(path)


def test_a_file_that_is_not_utf8_is_a_config_error(tmp_path: Path) -> None:
    path = tmp_path / "aliases.toml"
    path.write_bytes(b"\xff\xfe[[alias]]")
    with pytest.raises(ConfigError, match=r"aliases: .*aliases\.toml is not UTF-8 text"):
        aliases.load_aliases(path)


def test_a_missing_file_is_a_config_error(tmp_path: Path) -> None:
    with pytest.raises(ConfigError, match="aliases: cannot read .*absent.toml"):
        aliases.load_aliases(tmp_path / "absent.toml")


@pytest.mark.parametrize(
    ("entry", "message"),
    [
        (
            '[[alias]]\ncanonical = " zhipuai/glm-5.3"',
            "aliases.toml: [[alias]] #1: canonical ' zhipuai/glm-5.3' "
            "has a space at its start or its end",
        ),
        (
            '[[alias]]\ncanonical = "a/b"\n'
            'inventory = [{ provider_id = "ollama ", model_name = "m" }]',
            "aliases.toml: [[alias]] #1 (a/b) inventory[0]: provider_id 'ollama ' "
            "has a space at its start or its end",
        ),
        (
            '[[alias]]\ncanonical = "a/b"\nepoch = ["GLM-5.3\\t"]',
            "aliases.toml: [[alias]] #1 (a/b): epoch id 'GLM-5.3\\t' "
            "has a space at its start or its end",
        ),
        (
            '[[ignore]]\nreason = "r"\nsource = "epoch"\nid = " GPT-5"',
            "aliases.toml: [[ignore]] #1: id ' GPT-5' has a space at its start or its end",
        ),
    ],
)
def test_a_string_with_a_space_around_it_is_an_error(entry: str, message: str) -> None:
    with pytest.raises(ConfigError) as caught:
        aliases.parse_aliases(entry)
    assert str(caught.value) == message
