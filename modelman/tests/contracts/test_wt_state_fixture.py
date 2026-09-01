from datetime import UTC, datetime
from pathlib import Path

from modelman.usage.wt_state import read_last_launched, read_usage_counts

FIXTURES = Path(__file__).resolve().parents[3] / "docs" / "contracts"


def test_read_usage_counts_matches_fixture():
    """Guards modelman's usage.jsonl reader against wt's writer
    (wt/internal/usage/usage_fixture_test.go reads the same fixture file).
    Exercises the 1d/7d/30d window boundaries and the "omit if outside the
    largest window" behavior, which a naive schema change could silently
    break without any test noticing.
    """
    as_of = datetime(2026, 8, 31, tzinfo=UTC)
    counts = read_usage_counts(FIXTURES / "usage.sample.jsonl", as_of)

    local = counts["ollama/contract-fixture:local"]
    assert (local._1d, local._7d, local._30d) == (1, 1, 2)
    assert "openrouter/contract-fixture:cloud" not in counts


def test_read_last_launched_matches_fixture():
    """Guards the single-line rotation.state format wt writes and modelman
    reads to report the last-launched model."""
    assert read_last_launched(FIXTURES / "rotation.sample.state") == "ollama/contract-fixture:local"
