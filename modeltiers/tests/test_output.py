import re
import tomllib
from dataclasses import replace
from datetime import timedelta
from pathlib import Path
from typing import Any

import pytest
from conftest import SAMPLE, T0, without_timestamps

from modeltiers import aliases as aliases_mod
from modeltiers import output, pipeline
from modeltiers.aliases import Alias, Aliases
from modeltiers.contracts import Contracts
from modeltiers.errors import OutputFileError
from modeltiers.loader import Sources
from modeltiers.pipeline import Assignment
from modeltiers.records import Inventory


def test_the_document_is_the_contract_sample_apart_from_timestamps(
    document: dict[str, Any],
) -> None:
    assert without_timestamps(output.render(document)) == without_timestamps(SAMPLE.read_text())


def test_the_rendered_text_reads_back_as_the_same_document(document: dict[str, Any]) -> None:
    assert tomllib.loads(output.render(document)) == document


def test_tables_are_written_in_the_specs_order(document: dict[str, Any]) -> None:
    assert list(document) == [
        "generated_at",
        "contracts_version",
        "inventory",
        "sources",
        "alignment",
        "models",
        "candidates",
        "unmapped",
        "unresolved",
    ]
    headers = re.findall(r"^\[+[a-z]+", output.render(document), re.MULTILINE)
    assert list(dict.fromkeys(headers)) == [
        "[inventory",
        "[sources",
        "[alignment",
        "[[models",
        "[models",
        "[[candidates",
        "[candidates",
        "[[unmapped",
        "[[unresolved",
    ]
    assert list(document["sources"]) == ["modelsdev", "openrouter", "arena", "epoch"]


def test_the_head_of_the_file(document: dict[str, Any]) -> None:
    assert document["generated_at"] == "2026-10-12T15:58:00Z"
    assert document["contracts_version"] == "fixture-1"
    assert document["inventory"] == {
        "registry": "/home/owner/.config/local-ai/registry.toml",
        "providers": {"mtplx": "partial", "ollama": "ok", "omlx": "ok"},
    }
    assert document["sources"]["arena"] == {
        "status": "ok",
        "fetched_at": "2026-10-12T15:58:00Z",
        "records": 6,
        "unresolved": 1,
        "attribution": "Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0",
    }
    # models.dev is the identity spine: nothing of it is "unresolved".
    assert "unresolved" not in document["sources"]["modelsdev"]


def test_a_rated_model_entry_has_every_field(document: dict[str, Any]) -> None:
    assert document["models"][0] == {
        "provider_id": "ollama",
        "model_name": "glm-5.3:cloud",
        "registered": True,
        "canonical": "zhipuai/glm-5.3",
        "resolved_by": "host",
        "tier": "standard",
        "code": "S",
        "confidence": "high",
        "basis": "measured",
        "open_weights": True,
        "tool_call": True,
        "context": 1048576,
        "aligned_score": 44.3,
        "aligned_sources": ["intelligence_index", "arena_agent", "eci"],
        "votes": {
            "intelligence_index": "standard",
            "arena_agent": "standard",
            "eci": "standard",
        },
        "efforts": {"arena_agent": "max"},
        "notes": [],
        "scores": {
            "intelligence_index": 44.8,
            "coding_index": 74.8,
            "agentic_index": 53.1,
            "arena_agent": 0.02147446416824893,
            "arena_agent_ci_lower": 0.014306504009895325,
            "eci": 155.61,
        },
    }
    assert list(document["models"][0]) == [
        "provider_id",
        "model_name",
        "registered",
        "canonical",
        "resolved_by",
        "tier",
        "code",
        "confidence",
        "basis",
        "open_weights",
        "tool_call",
        "context",
        "aligned_score",
        "aligned_sources",
        "votes",
        "efforts",
        "notes",
        "scores",
    ]


def test_optional_fields_are_absent_when_unknown(document: dict[str, Any]) -> None:
    by_name = {m["model_name"]: m for m in document["models"]}
    # A score the source did not give, an effort the record did not name.
    assert set(by_name["deepseek-v4-pro:cloud"]["scores"]) == {
        "arena_agent",
        "arena_agent_ci_lower",
        "eci",
    }
    assert by_name["minimax-m3:cloud"]["efforts"] == {}
    # reason only on an unrated entry; context absent for a local build.
    assert "reason" not in by_name["glm-5.3:cloud"]
    local = by_name["gemma4:31b-mlx"]
    assert (local["tier"], local["code"], local["reason"]) == ("unrated", "-", "no_votes")
    assert (local["basis"], local["tool_call"]) == ("inherited", True)
    assert "context" not in local
    # The identity note comes before the gate's.
    assert by_name["minimax-m3:cloud"]["notes"] == ["host row has no canonical id; matched by name"]
    assert by_name["gemma4:31b-mlx"]["registered"] is False


def test_a_local_build_is_written_with_its_keys_tier_votes_and_tool_call(
    document: dict[str, Any],
) -> None:
    """A local row is rated through inheritance: `basis = "inherited"` beside
    a rated tier, with the key's votes, scores and tool-calling flag."""
    local = next(m for m in document["models"] if m["model_name"] == "qwen3.8:27b-mlx")
    assert local == {
        "provider_id": "ollama",
        "model_name": "qwen3.8:27b-mlx",
        "registered": True,
        "canonical": "alibaba/qwen3.8-27b",
        "resolved_by": "normalized",
        "tier": "standard",
        "code": "S",
        "confidence": "low",
        "basis": "inherited",
        "open_weights": True,
        "tool_call": True,
        "aligned_score": 33.7,
        "aligned_sources": ["intelligence_index"],
        "votes": {"intelligence_index": "standard"},
        "efforts": {},
        "notes": ["local build: served context unknown; context gate skipped"],
        "scores": {"intelligence_index": 33.7, "coding_index": 68.1, "agentic_index": 45.8},
    }
    # The contract sample wt reads holds the same entry.
    sample = tomllib.loads(SAMPLE.read_text())
    assert next(m for m in sample["models"] if m["model_name"] == "qwen3.8:27b-mlx") == local
    # Every other inherited entry of the fixtures is unrated.
    assert [
        (m["model_name"], m["tier"]) for m in sample["models"] if m["basis"] == "inherited"
    ] == [
        ("qwen3.8:27b-mlx", "standard"),
        ("nomic-embed-text:latest", "unrated"),
        ("gemma4:31b-mlx", "unrated"),
    ]


def test_an_ignored_row_is_in_models_as_not_applicable(document: dict[str, Any]) -> None:
    ignored = next(m for m in document["models"] if m["model_name"] == "nomic-embed-text:latest")
    assert ignored == {
        "provider_id": "ollama",
        "model_name": "nomic-embed-text:latest",
        "registered": True,
        "tier": "unrated",
        "code": "-",
        "reason": "not_applicable",
        "confidence": "none",
        "basis": "inherited",
        "votes": {},
        "efforts": {},
        "notes": ["ignored: embedding model"],
        "scores": {},
    }
    assert document["unmapped"] == [{"provider_id": "ollama", "model_name": "medgemma:27b"}]


def test_a_model_has_open_weights_when_its_key_has_models_dev_rows(
    document: dict[str, Any],
) -> None:
    """True when any models.dev row of the key says so; left out for an
    ignored row, which has no key to ask about."""
    assert [(m["model_name"], m.get("open_weights")) for m in document["models"]] == [
        ("glm-5.3:cloud", True),
        ("deepseek-v4-pro:cloud", True),
        ("z-ai/glm-5.3", True),
        ("qwen3.8:27b-mlx", True),
        ("minimax-m3:cloud", True),
        ("nomic-embed-text:latest", None),
        ("gemma4:31b-mlx", True),
    ]
    glm = document["models"][0]
    assert list(glm)[list(glm).index("basis") + 1] == "open_weights"


def test_a_candidate_has_open_weights_and_no_inventory_fields(document: dict[str, Any]) -> None:
    opus = document["candidates"][0]
    assert list(opus) == [
        "canonical",
        "resolved_by",
        "tier",
        "code",
        "confidence",
        "open_weights",
        "tool_call",
        "context",
        "aligned_score",
        "aligned_sources",
        "votes",
        "efforts",
        "notes",
        "scores",
    ]
    assert (opus["canonical"], opus["tier"], opus["code"]) == (
        "anthropic/claude-opus-5-5",
        "marathon",
        "M",
    )
    assert (opus["open_weights"], opus["context"]) == (False, 1000000)
    assert [c["canonical"] for c in document["candidates"]] == [
        "anthropic/claude-opus-5-5",
        "anthropic/claude-fable-5",
    ]
    assert document["unresolved"] == [
        {"source": "arena", "id": "Qwen3.8 Max", "score": 0.023467932612357068}
    ]


def test_a_key_with_no_models_dev_rows_has_no_tool_call_or_open_weights(
    inventory: Inventory, sources: Sources, rule: Contracts
) -> None:
    declared = aliases_mod.build([Alias("alibaba/qwen3.8-max", arena=("Qwen3.8 Max",))], [])
    assignment = pipeline.assign_all(pipeline.resolve_all(inventory, sources, declared), rule)
    document = output.build_document(assignment, inventory, sources, T0)
    (entry,) = [c for c in document["candidates"] if c["canonical"] == "alibaba/qwen3.8-max"]
    assert not {"tool_call", "open_weights", "context"} & set(entry)
    # Nothing unresolved is left at or above a floor: the array is absent.
    assert "unresolved" not in document
    assert "[[unresolved]]" not in output.render(document)


def test_a_stale_source_keeps_its_snapshots_own_time(
    assignment: Assignment, inventory: Inventory, sources: Sources
) -> None:
    stale = replace(sources, arena=replace(sources.arena, status="stale", fallback=True))
    document = output.build_document(assignment, inventory, stale, T0 + timedelta(days=7))
    assert document["generated_at"] == "2026-10-19T15:58:00Z"
    assert document["sources"]["arena"]["status"] == "stale"
    assert document["sources"]["arena"]["fetched_at"] == "2026-10-12T15:58:00Z"


def test_write_then_read(document: dict[str, Any], tmp_path: Path) -> None:
    path = tmp_path / "new" / "dir" / "tiers.toml"
    output.write_document(path, document)
    assert output.read_document(path) == document
    assert [p.name for p in path.parent.iterdir()] == ["tiers.toml"]  # no temporary file left


def test_a_failed_write_leaves_the_old_file_and_no_temporary_file(
    document: dict[str, Any], tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "tiers.toml"
    path.write_text('generated_at = "old"\n')

    def broken(doc: dict[str, Any]) -> str:
        raise RuntimeError("disk full")

    monkeypatch.setattr(output, "render", broken)
    with pytest.raises(RuntimeError, match="disk full"):
        output.write_document(path, document)
    assert path.read_text() == 'generated_at = "old"\n'
    assert [p.name for p in tmp_path.iterdir() if p.name.startswith("tiers.toml")] == ["tiers.toml"]


def test_reading_a_missing_file_names_the_run_command(tmp_path: Path) -> None:
    with pytest.raises(OutputFileError, match="cannot read .*tiers.toml.*run `modeltiers run`"):
        output.read_document(tmp_path / "tiers.toml")
    assert output.read_previous(tmp_path / "tiers.toml") is None


def test_a_file_that_is_not_toml_reads_as_absent_for_the_change_report(tmp_path: Path) -> None:
    path = tmp_path / "tiers.toml"
    path.write_text("generated_at = \n")
    with pytest.raises(OutputFileError, match="is not TOML"):
        output.read_document(path)
    assert output.read_previous(path) is None


def test_a_file_that_is_not_utf8_reads_as_absent_for_the_change_report(
    tmp_path: Path,
) -> None:
    path = tmp_path / "tiers.toml"
    path.write_bytes(b"\xff\xfe")
    with pytest.raises(OutputFileError, match="is not UTF-8 text"):
        output.read_document(path)
    assert output.read_previous(path) is None


def test_the_file_holds_each_fitted_line_so_a_score_can_be_worked_out_again(
    document: dict[str, Any],
) -> None:
    assert list(document["alignment"]) == ["arena_agent", "eci"]
    eci = document["alignment"]["eci"]
    assert list(eci) == ["slope", "intercept", "shared", "typical_miss"]
    assert eci["shared"] == 3
    # DeepSeek has no index: its score is the mean of its two translated scores.
    deepseek = document["models"][1]
    parts = [
        document["alignment"][name]["slope"] * deepseek["scores"][name]
        + document["alignment"][name]["intercept"]
        for name in deepseek["aligned_sources"]
    ]
    assert deepseek["aligned_sources"] == ["arena_agent", "eci"]
    assert deepseek["aligned_score"] == round(sum(parts) / len(parts), 1) == 43.5
    assert tomllib.loads(output.render(document))["alignment"] == document["alignment"]


def test_an_entry_with_nothing_to_align_has_neither_field_and_no_fit_means_no_table(
    assignment: Assignment,
    inventory: Inventory,
    sources: Sources,
    fixture_aliases: Aliases,
    rule: Contracts,
) -> None:
    document = output.build_document(assignment, inventory, sources, T0)
    ignored = next(m for m in document["models"] if m["model_name"] == "nomic-embed-text:latest")
    assert not {"aligned_score", "aligned_sources"} & set(ignored)
    unfitted = pipeline.assign_all(pipeline.resolve_all(inventory, sources, fixture_aliases), rule)
    bare = output.build_document(unfitted, inventory, sources, T0)
    assert "alignment" not in bare
    assert "[alignment" not in output.render(bare)
