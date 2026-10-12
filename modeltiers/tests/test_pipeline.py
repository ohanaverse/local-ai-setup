from datetime import UTC, datetime
from pathlib import Path

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import aliases as aliases_mod
from modeltiers import inventory as inventory_mod
from modeltiers import loader, pipeline
from modeltiers.aliases import Alias, Aliases
from modeltiers.errors import ConfigError
from modeltiers.loader import Sources, SourceSpecs
from modeltiers.records import Inventory

FIXTURES = Path(__file__).parent / "fixtures"
T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
Answers = dict[str, list[bytes | Exception]]


@pytest.fixture
def inventory() -> Inventory:
    return inventory_mod.parse_inventory((FIXTURES / "inventory.json").read_text())


@pytest.fixture
def sources(specs: SourceSpecs, good_answers: Answers, tmp_path: Path) -> Sources:
    return loader.load_all(
        specs,
        cache=tmp_path / "cache",
        fetcher=FakeFetcher(good_answers),
        sleep=Sleeps(),
        now=T0,
        offline=False,
        warn=lambda message: None,
    )


@pytest.fixture
def fixture_aliases() -> Aliases:
    return aliases_mod.load_aliases(FIXTURES / "aliases.toml")


def _outcomes(result: pipeline.Identity) -> list[tuple[str, str | None, str | None]]:
    return [(r.row.model_name, r.resolved_by, r.canonical) for r in result.rows]


def test_every_inventory_row_gets_one_outcome_in_wt_order(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases
) -> None:
    result = pipeline.resolve_all(inventory, sources, fixture_aliases)
    assert _outcomes(result) == [
        ("glm-5.3:cloud", "host", "zhipuai/glm-5.3"),
        ("deepseek-v4-pro:cloud", "alias", "deepseek/deepseek-v4-pro-0813"),
        ("z-ai/glm-5.3", "host", "zhipuai/glm-5.3"),
        ("qwen3.8:27b-mlx", "normalized", "alibaba/qwen3.8-27b"),
        ("medgemma:27b", None, None),
        ("minimax-m3:cloud", "normalized", "minimax/MiniMax-M3"),
        ("nomic-embed-text:latest", None, None),
        ("gemma4:31b-mlx", "normalized", "google/gemma-4-31b-it"),
    ]
    assert [r.row.model_name for r in result.unmapped] == ["medgemma:27b"]
    assert [r.row.model_name for r in result.review] == [
        "qwen3.8:27b-mlx",
        "minimax-m3:cloud",
        "gemma4:31b-mlx",
    ]
    by_name = {r.row.model_name: r for r in result.rows}
    assert by_name["nomic-embed-text:latest"].ignore_reason == "embedding model"
    assert by_name["qwen3.8:27b-mlx"].basis == "inherited"
    assert by_name["minimax-m3:cloud"].notes == ("host row has no canonical id; matched by name",)
    # The host row found on the way, canonical or not: step 3 reads its context.
    assert by_name["minimax-m3:cloud"].host_row is not None
    assert by_name["minimax-m3:cloud"].host_row.context == 512000
    assert result.warnings == ()


def test_the_three_sources_resolve_against_the_same_index(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases
) -> None:
    result = pipeline.resolve_all(inventory, sources, fixture_aliases)
    assert [s.source for s in result.sources()] == ["openrouter", "arena", "epoch"]
    assert [(s.records, len(s.best), len(s.unresolved), s.ignored) for s in result.sources()] == [
        (4, 2, 1, 0),  # glm-5.3 and its :batch share a key; lfm has no key
        (5, 4, 1, 0),  # Qwen3.8 Max has no key in the fixture
        (5, 4, 0, 1),  # GPT-5 is ignored by the fixture aliases
    ]
    assert result.arena.best["zhipuai/glm-5.3"].record.effort == "max"
    assert result.arena.best["deepseek/deepseek-v4-pro-0813"].resolved_by == "alias"
    assert result.epoch.best["minimax/minimax-m3"].canonical == "minimax/MiniMax-M3"
    assert [r.source_id for r in result.openrouter.unresolved] == ["liquid/lfm-2.5-2.6b:free"]
    assert result.index.spelling("ZHIPUAI/GLM-5.3") == "zhipuai/glm-5.3"


def test_without_the_alias_deepseek_cloud_reaches_the_april_key(
    inventory: Inventory, sources: Sources
) -> None:
    result = pipeline.resolve_all(inventory, sources, Aliases())
    by_name = {r.row.model_name: r for r in result.rows}
    assert by_name["deepseek-v4-pro:cloud"].canonical == "deepseek/deepseek-v4-pro"
    assert by_name["deepseek-v4-pro:cloud"].resolved_by == "normalized"
    assert by_name["nomic-embed-text:latest"].unmapped
    assert result.epoch.ignored == 0


def test_an_unanchored_alias_is_a_config_error(inventory: Inventory, sources: Sources) -> None:
    unanchored = aliases_mod.build(
        [Alias("google/gemini-4-argon", inventory=(("ollama", "medgemma:27b"),))], []
    )
    with pytest.raises(ConfigError, match=r"\(google/gemini-4-argon\): canonical is not a models"):
        pipeline.resolve_all(inventory, sources, unanchored)


def test_an_alias_id_absent_from_this_run_is_a_warning_not_an_error(
    inventory: Inventory, sources: Sources
) -> None:
    stale = aliases_mod.build(
        [Alias("zhipuai/glm-5.3", arena=("GLM 5.3",), epoch=("GLM-5.3", "GLM 5.3 (retired)"))], []
    )
    result = pipeline.resolve_all(inventory, sources, stale)
    assert result.warnings == (
        "alias for zhipuai/glm-5.3 names epoch id 'GLM 5.3 (retired)', "
        "which is not in this run's epoch data",
    )
    assert result.epoch.best["zhipuai/glm-5.3"].resolved_by == "alias"
