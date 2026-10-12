from modeltiers import aliases as aliases_mod
from modeltiers import identity
from modeltiers.aliases import Alias, Aliases, Ignore
from modeltiers.identity import Match
from modeltiers.records import ModelsDev, ModelsDevRow, ScoreRecord


def _md(provider: str, model_id: str, canonical: str | None) -> ModelsDevRow:
    return ModelsDevRow(provider, model_id, canonical, True, True, 1000000)


MODELSDEV = ModelsDev(
    rows=(
        _md("openrouter", "z-ai/glm-5.3", "zhipuai/glm-5.3"),
        _md("openrouter", "minimax/minimax-m3", "minimax/MiniMax-M3"),
        _md("openrouter", "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"),
        _md("openrouter", "nex-agi/nex-n2.5-pro", None),
        _md("deepinfra", "deepseek-ai/DeepSeek-V4-Pro-0813", "deepseek/deepseek-v4-pro-0813"),
        _md("anthropic", "claude-opus-5-5", "anthropic/claude-opus-5-5"),
        _md("ollama-cloud", "cogito-3", "deepcogito/cogito-3"),
    )
)
NO_ALIASES = Aliases()
INDEX = identity.build_index(MODELSDEV, NO_ALIASES)


def _or(source_id: str, score: float, coding: float | None = None) -> ScoreRecord:
    return ScoreRecord("openrouter", source_id, score, coding_index=coding)


def _arena(source_id: str, score: float, ci_lower: float, effort: str | None) -> ScoreRecord:
    return ScoreRecord("arena", source_id, score, ci_lower=ci_lower, effort=effort)


def test_an_openrouter_record_resolves_through_its_host_row() -> None:
    record = _or("z-ai/glm-5.3", 44.8)
    assert identity.resolve_record(record, INDEX, NO_ALIASES) == Match(
        "zhipuai/glm-5.3", "host", record
    )


def test_an_arena_or_epoch_record_resolves_by_name() -> None:
    arena = _arena("Claude Opus 5.5", 0.1433, 0.1215, "high")
    assert identity.resolve_record(arena, INDEX, NO_ALIASES) == Match(
        "anthropic/claude-opus-5-5", "normalized", arena
    )
    epoch = ScoreRecord("epoch", "MiniMax-M3", 146.95, ci_lower=142.59)
    assert identity.resolve_record(epoch, INDEX, NO_ALIASES) == Match(
        "minimax/MiniMax-M3", "normalized", epoch
    )


def test_a_source_other_than_openrouter_has_no_host_step() -> None:
    # `cogito-3` is an ollama-cloud host id. Arena is not that host, so the
    # record can only resolve by name, and the name alone matches the key.
    record = _arena("cogito-3", 0.01, 0.0, None)
    assert identity.resolve_record(record, INDEX, NO_ALIASES) == Match(
        "deepcogito/cogito-3", "normalized", record
    )


def test_an_alias_answers_before_the_host_row() -> None:
    pinned = aliases_mod.build(
        [Alias("deepseek/deepseek-v4-pro-0813", openrouter=("deepseek/deepseek-v4-pro",))], []
    )
    record = _or("deepseek/deepseek-v4-pro", 41.0)
    assert identity.resolve_record(record, INDEX, pinned) == Match(
        "deepseek/deepseek-v4-pro-0813", "alias", record
    )
    # The alias is on the exact id; the `:free` variant still goes to its host row.
    free = _or("deepseek/deepseek-v4-pro:free", 41.0)
    assert identity.resolve_record(free, INDEX, pinned) == Match(
        "deepseek/deepseek-v4-pro", "host", free
    )


def test_an_arena_alias_covers_every_effort_row() -> None:
    pinned = aliases_mod.build(
        [Alias("deepseek/deepseek-v4-pro-0813", arena=("DeepSeek V4 Pro (High) (0813)",))], []
    )
    # The parser has already removed the effort from the record's id.
    rows = (
        _arena("DeepSeek V4 Pro (0813)", 0.0134, -0.0032, "high"),
        _arena("DeepSeek V4 Pro (0813)", 0.0101, -0.0040, "max"),
    )
    resolved = identity.resolve_source("arena", rows, INDEX, pinned)
    assert resolved.unresolved == ()
    assert list(resolved.best) == ["deepseek/deepseek-v4-pro-0813"]


def test_a_record_no_step_answers_is_unresolved() -> None:
    no_canonical = _or("nex-agi/nex-n2.5-pro:free", 30.0)  # host row without a canonical id
    no_row = _or("liquid/lfm-2.5-2.6b:free", 8.4)
    resolved = identity.resolve_source("openrouter", (no_canonical, no_row), INDEX, NO_ALIASES)
    assert resolved.unresolved == (no_canonical, no_row)
    assert (resolved.records, len(resolved.best), resolved.ignored) == (2, 0, 0)


def test_free_and_batch_collapse_to_the_base_and_the_first_stays_on_a_tie() -> None:
    base = _or("z-ai/glm-5.3", 44.8, coding=74.8)
    batch = _or("z-ai/glm-5.3:batch", 44.8, coding=74.8)
    free = _or("z-ai/glm-5.3:free", 44.8)
    resolved = identity.resolve_source("openrouter", (base, batch, free), INDEX, NO_ALIASES)
    assert resolved.records == 3
    assert resolved.best == {"zhipuai/glm-5.3": Match("zhipuai/glm-5.3", "host", base)}


def test_two_records_for_one_key_keep_the_better_score_with_its_own_interval_and_effort() -> None:
    high = _arena("Claude Opus 5.5", 0.1433, 0.1215, "high")
    maximum = _arena("Claude Opus 5.5", 0.1391, 0.1300, "max")
    for rows in ((high, maximum), (maximum, high)):
        best = identity.resolve_source("arena", rows, INDEX, NO_ALIASES).best
        kept = best["anthropic/claude-opus-5-5"].record
        # The lower score has the higher lower bound; it is not borrowed.
        assert (kept.score, kept.ci_lower, kept.effort) == (0.1433, 0.1215, "high")


def test_a_dated_and_an_undated_name_can_share_a_key() -> None:
    pinned = aliases_mod.build(
        [Alias("anthropic/claude-opus-5-5", epoch=("Claude Opus 5.5 (Sep 2026)",))], []
    )
    dated = ScoreRecord("epoch", "Claude Opus 5.5 (Sep 2026)", 167.33, ci_lower=164.06)
    undated = ScoreRecord("epoch", "Claude Opus 5.5", 165.0, ci_lower=None)
    resolved = identity.resolve_source("epoch", (undated, dated), INDEX, pinned)
    assert resolved.best == {
        "anthropic/claude-opus-5-5": Match("anthropic/claude-opus-5-5", "alias", dated)
    }


def test_best_is_keyed_in_lower_case_and_holds_the_stored_spelling() -> None:
    record = _or("minimax/minimax-m3", 20.1)
    best = identity.resolve_source("openrouter", (record,), INDEX, NO_ALIASES).best
    assert best == {"minimax/minimax-m3": Match("minimax/MiniMax-M3", "host", record)}


def test_an_ignored_record_is_counted_and_is_not_unresolved() -> None:
    ignoring = aliases_mod.build(
        [], [Ignore("floating name; no fixed model", source="epoch", id="Mistral Large")]
    )
    rows = (
        ScoreRecord("epoch", "Mistral Large", 140.2),
        ScoreRecord("epoch", "Mistral Medium 3.5", 151.0),
    )
    resolved = identity.resolve_source("epoch", rows, INDEX, ignoring)
    assert resolved.ignored == 1
    assert [r.source_id for r in resolved.unresolved] == ["Mistral Medium 3.5"]
    assert resolved.records == 2


def test_an_ignore_for_one_source_does_not_hide_the_id_in_another() -> None:
    ignoring = aliases_mod.build([], [Ignore("r", source="epoch", id="Mistral Large")])
    resolved = identity.resolve_source(
        "arena", (_arena("Mistral Large", 0.0, -0.01, None),), INDEX, ignoring
    )
    assert (resolved.ignored, len(resolved.unresolved)) == (0, 1)
