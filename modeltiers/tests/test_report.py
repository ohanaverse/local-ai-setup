from dataclasses import replace

from modeltiers import assign, identity, pipeline, report
from modeltiers.aliases import Aliases
from modeltiers.contracts import Contracts, Sentinel
from modeltiers.identity import RowIdentity, SourceIdentity
from modeltiers.loader import Sources
from modeltiers.pipeline import Assignment, Identity
from modeltiers.records import Inventory, InventoryRow, ModelsDev


def _identity(*rows: RowIdentity) -> Identity:
    def empty(source: str) -> SourceIdentity:
        return SourceIdentity(source=source, records=0, best={}, unresolved=(), ignored=0)

    return Identity(
        index=identity.build_index(ModelsDev(rows=()), Aliases()),
        rows=rows,
        openrouter=empty("openrouter"),
        arena=empty("arena"),
        epoch=empty("epoch"),
        warnings=(),
    )


def test_an_empty_inventory_still_prints_every_section() -> None:
    assert report.identity_lines(_identity()) == [
        "identity: 0 rows: 0 resolved, 0 ignored, 0 unmapped",
        "unmapped: 0 (each needs an [[alias]] or an [[ignore]] in aliases.toml, "
        "or is left open until evidence identifies it)",
        "review: 0 matched by name alone (check each)",
    ]


def test_the_name_column_is_as_wide_as_the_longest_name() -> None:
    short = RowIdentity(InventoryRow("omlx", "m", "local", True))
    long = RowIdentity(
        InventoryRow("mtplx", "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", "local", True),
        canonical="alibaba/qwen3.8-27b",
        resolved_by="alias",
    )
    lines = report.identity_lines(_identity(short, long))
    assert lines[1] == "  omlx/m" + " " * 45 + "  unmapped"
    assert lines[2] == (
        "  mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality  alias       "
        "alibaba/qwen3.8-27b  [inherited]"
    )
    # An alias-resolved row is not on the review list; only name matches are.
    assert lines[5] == "review: 0 matched by name alone (check each)"


def test_unresolved_records_at_a_floor_are_named_and_the_rest_counted(
    assignment: Assignment,
) -> None:
    assert report.unresolved_lines(assignment) == [
        "unresolved: 1 at or above a standard floor "
        "(each needs an [[alias]] or an [[ignore]] in aliases.toml, "
        "or is left open until evidence identifies it)",
        "  arena  Qwen3.8 Max  0.0235",
        "  openrouter: 1 of 6 records unresolved, 0 ignored, 4 keys resolved",
        "  arena: 1 of 6 records unresolved, 0 ignored, 5 keys resolved",
        "  epoch: 0 of 6 records unresolved, 1 ignored, 5 keys resolved",
    ]


def test_sentinels_that_hold_are_one_line(assignment: Assignment) -> None:
    assert report.sentinel_lines(assignment) == ["sentinels: 4 of 4 hold"]


def test_a_failed_check_prints_every_sentinel_with_scores_votes_and_pins(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    scaled = tuple(replace(r, score=r.score * 0.8) for r in sources.openrouter.data)
    drifted = replace(sources, openrouter=replace(sources.openrouter, data=scaled))
    result = pipeline.assign_all(pipeline.resolve_all(inventory, drifted, fixture_aliases), rule)
    lines = report.sentinel_lines(result)
    assert lines[0] == "sentinels: 2 of 4 FAILED"
    # Fable 5: the vote moved, the tier did not.
    assert lines[6:11] == [
        "  FAIL  anthropic/claude-fable-5",
        "        scores:  intelligence_index 39.68, coding_index 76.5, agentic_index 50.7, "
        "arena_agent 0.0894, arena_agent_ci_lower 0.0767, eci 162.06",
        "        votes:   intelligence_index standard, arena_agent deep, eci deep; "
        "tier before the gates deep",
        "        pinned:  intelligence_index deep, arena_agent deep, eci deep; tier deep",
        "        differs: intelligence_index voted standard, pinned deep",
    ]
    # A sentinel that holds is printed too, without a `differs` line.
    assert lines[-4:] == [
        "  ok    minimax/MiniMax-M3",
        "        scores:  arena_agent -0.0695, arena_agent_ci_lower -0.0786, eci 146.95",
        "        votes:   arena_agent quick, eci quick; tier before the gates quick",
        "        pinned:  arena_agent quick, eci quick; tier quick",
    ]


def test_a_sentinel_with_no_record_prints_none(assignment: Assignment, rule: Contracts) -> None:
    absent = assign.check_sentinel(
        Sentinel("alibaba/qwen3.8-27b", "quick", {}), "alibaba/qwen3.8-27b", assign.weigh({}, rule)
    )
    lines = report.sentinel_lines(replace(assignment, sentinels=(absent,)))
    assert lines == [
        "sentinels: 1 of 1 FAILED",
        "  FAIL  alibaba/qwen3.8-27b",
        "        scores:  none",
        "        votes:   none; tier before the gates unrated",
        "        pinned:  none; tier quick",
        "        differs: tier before the gates is unrated, pinned quick",
    ]
