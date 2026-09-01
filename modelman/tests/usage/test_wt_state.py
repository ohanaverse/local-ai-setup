from __future__ import annotations

import json
from datetime import UTC, datetime
from pathlib import Path

from modelman.usage.wt_state import (
    LaunchCounts,
    read_last_launched,
    read_usage_counts,
)


def _write_usage_jsonl(path: Path, records: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w") as f:
        for record in records:
            f.write(json.dumps(record) + "\n")


# Multiple launches of the same model on the same day must be counted per
# model id, and each window (1d/7d/30d) should include an event that falls within it.
def test_read_usage_counts_basic(tmp_path: Path) -> None:
    now = datetime.now(UTC)
    records = [
        {"model_id": "ollama/a", "timestamp": now.isoformat()},
        {"model_id": "ollama/a", "timestamp": now.isoformat()},
        {"model_id": "ollama/b", "timestamp": now.isoformat()},
    ]
    usage_path = tmp_path / "usage.jsonl"
    _write_usage_jsonl(usage_path, records)

    counts = read_usage_counts(usage_path, now)
    assert counts["ollama/a"] == LaunchCounts(_1d=2, _7d=2, _30d=2)
    assert counts["ollama/b"] == LaunchCounts(_1d=1, _7d=1, _30d=1)


# A launch older than the largest window (30d) must be excluded from the
# result entirely, and per-window boundaries must be respected independently.
def test_read_usage_counts_ignores_outside_window(tmp_path: Path) -> None:
    now = datetime.fromisoformat("2026-08-28T12:00:00+00:00")
    records = [
        {"model_id": "ollama/old", "timestamp": "2026-07-01T12:00:00+00:00"},  # >30d
        {"model_id": "ollama/recent", "timestamp": "2026-08-27T12:00:00+00:00"},
        {"model_id": "ollama/today", "timestamp": "2026-08-28T11:00:00+00:00"},
    ]
    usage_path = tmp_path / "usage.jsonl"
    _write_usage_jsonl(usage_path, records)

    counts = read_usage_counts(usage_path, now)
    assert "ollama/old" not in counts
    assert counts["ollama/recent"] == LaunchCounts(_1d=0, _7d=1, _30d=1)
    assert counts["ollama/today"] == LaunchCounts(_1d=1, _7d=1, _30d=1)


# wt hasn't necessarily created usage.jsonl yet on a fresh install; a missing
# file must return an empty result rather than raising FileNotFoundError.
def test_read_usage_counts_missing_file(tmp_path: Path) -> None:
    usage_path = tmp_path / "usage.jsonl"
    counts = read_usage_counts(usage_path, datetime.now(UTC))
    assert counts == {}


# Regression test: a single malformed/legacy line in usage.jsonl (bad JSON,
# missing model_id, or a non-ISO timestamp) previously crashed the whole
# `usage report` command. Bad lines must be skipped, valid ones still counted.
def test_read_usage_counts_skips_malformed_lines(tmp_path: Path) -> None:
    now = datetime.now(UTC)
    usage_path = tmp_path / "usage.jsonl"
    usage_path.write_text(
        "not json at all\n"
        f'{{"model_id":"ollama/a","timestamp":"{now.isoformat()}"}}\n'
        '{"model_id":"ollama/b"}\n'
        '{"model_id":"ollama/c","timestamp":"not-a-timestamp"}\n'
    )

    counts = read_usage_counts(usage_path, now)
    assert counts == {"ollama/a": LaunchCounts(_1d=1, _7d=1, _30d=1)}


# The rotation.state file (written by wt) holds the most recently launched
# model id as plain text; the report footer uses this to show "last launched".
def test_read_last_launched_present(tmp_path: Path) -> None:
    rotation_path = tmp_path / "rotation.state"
    rotation_path.write_text("ollama/kimi-k2.6:cloud\n")
    assert read_last_launched(rotation_path) == "ollama/kimi-k2.6:cloud"


# rotation.state may not exist yet (e.g. wt never launched a model); this
# must return None instead of raising.
def test_read_last_launched_missing(tmp_path: Path) -> None:
    rotation_path = tmp_path / "rotation.state"
    assert read_last_launched(rotation_path) is None


# wt writes the model id with a trailing newline; leading/trailing whitespace
# must be stripped so the id matches the registry's id exactly.
def test_read_last_launched_strips_whitespace(tmp_path: Path) -> None:
    rotation_path = tmp_path / "rotation.state"
    rotation_path.write_text("  ollama/foo  \n")
    assert read_last_launched(rotation_path) == "ollama/foo"
