from pathlib import Path

import pytest

from modeltiers import contracts
from modeltiers.errors import ConfigError

FIXTURE = Path(__file__).parent / "fixtures" / "contracts.toml"
TEXT = FIXTURE.read_text()


def _broken(old: str, new: str) -> str:
    """The fixture text with one exact piece replaced."""
    assert TEXT.count(old) == 1, old
    return TEXT.replace(old, new)


def test_the_fixture_loads() -> None:
    loaded = contracts.load_contracts(FIXTURE)
    assert loaded.version == "fixture-1"
    assert loaded.origin == "contracts.toml"
    assert list(loaded.tiers) == ["quick", "standard", "deep", "marathon"]
    assert [(t.code, t.context_min) for t in loaded.tiers.values()] == [
        ("Q", 32000),
        ("S", 128000),
        ("D", 200000),
        ("M", 400000),
    ]
    assert list(loaded.signals) == ["intelligence_index", "arena_agent", "eci"]
    arena = loaded.signals["arena_agent"]
    assert (arena.source, arena.max_vote, arena.marathon_ci_lower) == ("arena", "deep", 0.097)
    assert arena.floors == {"quick": -0.090, "standard": -0.029, "deep": 0.056}
    assert loaded.signals["eci"].marathon_ci_lower is None
    assert loaded.marathon is arena
    assert list(loaded.attribution) == ["modelsdev", "openrouter", "arena", "epoch"]
    assert loaded.attribution["arena"] == (
        "Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0"
    )
    assert [(s.canonical, s.tier) for s in loaded.sentinels] == [
        ("anthropic/claude-opus-5-5", "marathon"),
        ("anthropic/claude-fable-5", "deep"),
        ("zhipuai/glm-5.3", "standard"),
        ("minimax/minimax-m3", "quick"),
    ]
    # A signal a sentinel leaves out is not pinned.
    assert loaded.sentinels[3].votes == {"arena_agent": "quick", "eci": "quick"}


def test_codes_and_the_unrated_dash() -> None:
    loaded = contracts.load_contracts(FIXTURE)
    assert [loaded.code(t) for t in ("marathon", "deep", "standard", "quick", "unrated")] == [
        "M",
        "D",
        "S",
        "Q",
        "-",
    ]
    assert contracts.RANK["unrated"] < contracts.RANK["quick"] < contracts.RANK["marathon"]


def test_integer_floors_are_read_as_numbers() -> None:
    text = _broken(
        "floors = { quick = 137.1, standard = 150.7, deep = 160.6 }",
        "floors = { quick = 137, standard = 150.7, deep = 161 }",
    )
    assert contracts.parse_contracts(text).signals["eci"].floors == {
        "quick": 137.0,
        "standard": 150.7,
        "deep": 161.0,
    }


def test_no_sentinels_is_valid() -> None:
    text = TEXT[: TEXT.index("[[sentinels]]")]
    assert contracts.parse_contracts(text).sentinels == ()


@pytest.mark.parametrize(
    ("old", "new", "message"),
    [
        # Floors must rise with the tier.
        (
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0 }",
            "floors = { quick = 10.6, standard = 49.0, deep = 33.0 }",
            r"contracts.toml: \[signals.intelligence_index\]: floors must rise with the tier",
        ),
        (
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0 }",
            "floors = { quick = 10.6, standard = 33.0 }",
            r"\[signals.intelligence_index\]: floors.deep is missing or not a number",
        ),
        (
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0 }",
            "floors = { quick = true, standard = 33.0, deep = 49.0 }",
            r"\[signals.intelligence_index\]: floors.quick is missing or not a number",
        ),
        (
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0 }",
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0, marathon = 60.0 }",
            r"\[signals.intelligence_index\] floors: unknown key 'marathon'",
        ),
        # Exactly one signal carries marathon_ci_lower.
        (
            "marathon_ci_lower = 0.097\n",
            "",
            "exactly one signal must carry marathon_ci_lower; found none",
        ),
        (
            'source = "epoch"\n',
            'source = "epoch"\nmarathon_ci_lower = 165.0\n',
            "exactly one signal must carry marathon_ci_lower; found arena_agent, eci",
        ),
        (
            "marathon_ci_lower = 0.097",
            'marathon_ci_lower = "0.097"',
            r"\[signals.arena_agent\]: marathon_ci_lower is missing or not a number",
        ),
        (
            'source = "arena"',
            'source = "epoch"',
            r"\[signals.arena_agent\]: source must be 'arena', not 'epoch'",
        ),
        (
            'source = "arena"\nmax_vote = "deep"',
            'source = "arena"\nmax_vote = "marathon"',
            r"\[signals.arena_agent\]: max_vote 'marathon' is not one of quick, standard, deep",
        ),
        ("[signals.eci]", "[signals.ecii]", r"contracts.toml: no \[signals.eci\] table"),
        (
            "[signals.eci]",
            '[signals.speed]\nsource = "x"\n\n[signals.eci]',
            "contracts.toml: unknown signals entry 'speed'",
        ),
        (
            'source = "arena"\n',
            'source = "arena"\nweight = 2\n',
            r"\[signals.arena_agent\]: unknown key 'weight'",
        ),
        # Tiers.
        ("[tiers.deep]", "[tiers.deeper]", r"contracts.toml: no \[tiers.deep\] table"),
        (
            'code = "D"',
            'code = "S"',
            r"\[tiers.deep\]: code 'S' is used by another tier",
        ),
        (
            'code = "D"',
            'code = "-"',
            r"\[tiers.deep\]: code must be one character other than '-'",
        ),
        (
            "context_min = 200000",
            "context_min = 100000",
            r"\[tiers.deep\]: context_min must not fall as the tier rises",
        ),
        (
            "context_min = 200000",
            'context_min = "200k"',
            r"\[tiers.deep\]: context_min is missing or not a whole number",
        ),
        # Sources.
        (
            "[sources.epoch]",
            "[sources.epoc]",
            r"contracts.toml: no \[sources.epoch\] table",
        ),
        (
            'attribution = "models.dev (https://models.dev), MIT"',
            'attribution = ""',
            r"\[sources.modelsdev\]: attribution is missing, empty or not a string",
        ),
        # Sentinels name a known tier and known signals.
        (
            'canonical = "anthropic/claude-fable-5"\ntier = "deep"',
            'canonical = "anthropic/claude-fable-5"\ntier = "deeper"',
            r"\[\[sentinels\]\] #2 \(anthropic/claude-fable-5\): tier 'deeper' is not one of",
        ),
        (
            'votes = { arena_agent = "quick", eci = "quick" }',
            'votes = { arena = "quick", eci = "quick" }',
            r"\[\[sentinels\]\] #4 \(minimax/minimax-m3\): votes names an unknown signal 'arena'",
        ),
        (
            'votes = { arena_agent = "quick", eci = "quick" }',
            'votes = { arena_agent = "marathon", eci = "quick" }',
            r"#4 \(minimax/minimax-m3\): votes.arena_agent 'marathon' is not one of unrated, quick",
        ),
        (
            'canonical = "minimax/minimax-m3"',
            'canonical = "Anthropic/Claude-Opus-5-5"',
            r"\[\[sentinels\]\] #4 \(Anthropic/Claude-Opus-5-5\): listed twice",
        ),
        (
            'canonical = "minimax/minimax-m3"\n',
            "",
            r"\[\[sentinels\]\] #4: canonical is missing, empty or not a string",
        ),
        (
            'votes = { arena_agent = "quick", eci = "quick" }',
            "",
            r"\[\[sentinels\]\] #4 \(minimax/minimax-m3\) votes: missing or not a table",
        ),
        # The file as a whole.
        ('version = "fixture-1"', "", "contracts.toml: version is missing, empty or not a string"),
        ('version = "fixture-1"', 'version = "fixture-1"\nfloors = 1', "unknown key 'floors'"),
        ('version = "fixture-1"', "version = ", "contracts.toml: not TOML"),
    ],
)
def test_an_invalid_file_is_a_config_error_naming_the_entry(
    old: str, new: str, message: str
) -> None:
    with pytest.raises(ConfigError, match=message):
        contracts.parse_contracts(_broken(old, new))


def test_a_single_sentinels_table_is_refused() -> None:
    text = TEXT[: TEXT.index("[[sentinels]]")] + '[sentinels]\ncanonical = "a/b"\n'
    with pytest.raises(ConfigError, match=r"sentinels is not an array of tables; write \[\["):
        contracts.parse_contracts(text)


def test_a_file_with_no_tiers_table_is_refused() -> None:
    with pytest.raises(ConfigError, match=r"other.toml: \[tiers\]: missing or not a table"):
        contracts.parse_contracts('version = "1"\n', origin="other.toml")


def test_a_missing_file_is_a_config_error(tmp_path: Path) -> None:
    with pytest.raises(ConfigError, match="contracts: cannot read .*absent.toml"):
        contracts.load_contracts(tmp_path / "absent.toml")


def test_a_file_that_is_not_utf8_is_a_config_error(tmp_path: Path) -> None:
    path = tmp_path / "contracts.toml"
    path.write_bytes(b"\xff\xfe")
    with pytest.raises(ConfigError, match=r"contracts: .*contracts\.toml is not UTF-8 text"):
        contracts.load_contracts(path)
