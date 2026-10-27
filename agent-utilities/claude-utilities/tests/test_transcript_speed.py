import json
from pathlib import Path

import pytest

import transcript_speed as ts


def write_transcript(path: Path, records: list[dict | str]) -> Path:
    path.write_text("\n".join(r if isinstance(r, str) else json.dumps(r) for r in records) + "\n")
    return path


def assistant(ts_: str, mid: str, out: int) -> dict:
    return {
        "type": "assistant",
        "timestamp": ts_,
        "message": {"id": mid, "model": "m", "usage": {"output_tokens": out}},
    }


def request(ts_: str) -> dict:
    return {"type": "api-request", "timestamp": ts_}


def test_derive_from_two_block_call(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [
            request("2026-01-01T00:00:00Z"),
            assistant("2026-01-01T00:00:02Z", "m1", 200),
            assistant("2026-01-01T00:00:04Z", "m1", 200),
        ],
    )
    (call,) = ts.calls_from_transcript(f)
    row = ts.derive(call)
    assert row["ttfb_s"] == 2.0
    assert row["total_s"] == 4.0
    assert row["tps_total"] == 50.0
    assert row["tps_window"] == 100.0


@pytest.mark.parametrize(
    "records",
    [
        # no preceding api-request: dropped
        [assistant("2026-01-01T00:00:02Z", "m1", 200)],
        # unparseable lines are skipped, leaving no call
        ["not json"],
    ],
)
def test_calls_without_request_or_valid_json_are_dropped(tmp_path, records):
    f = write_transcript(tmp_path / "s.jsonl", records)
    assert list(ts.calls_from_transcript(f)) == []


def test_single_block_call_has_no_window_rate(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [
            request("2026-01-01T00:00:00Z"),
            assistant("2026-01-01T00:00:03Z", "m1", 300),
        ],
    )
    (call,) = ts.calls_from_transcript(f)
    assert ts.derive(call)["tps_window"] is None


def test_non_record_lines_and_bad_timestamps_are_skipped(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [
            "null",
            "[1]",
            {"type": "api-request", "timestamp": "garbage"},
            request("2026-01-01T00:00:00Z"),
            assistant("2026-01-01T00:00:02Z", "m1", 200),
        ],
    )
    (call,) = ts.calls_from_transcript(f)
    assert ts.derive(call)["total_s"] == 2.0


def test_assistant_line_with_null_message_does_not_crash(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [{"type": "assistant", "timestamp": "2026-01-01T00:00:01Z", "message": None}],
    )
    assert list(ts.calls_from_transcript(f)) == []


def test_each_message_pairs_with_its_own_preceding_request(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [
            request("2026-01-01T00:00:00Z"),
            assistant("2026-01-01T00:00:02Z", "m1", 100),
            request("2026-01-01T00:00:10Z"),
            assistant("2026-01-01T00:00:15Z", "m2", 100),
        ],
    )
    rows = [ts.derive(c) for c in ts.calls_from_transcript(f)]
    assert [r["total_s"] for r in rows] == [2.0, 5.0]


@pytest.mark.parametrize(
    ("vals", "p", "want"),
    [([1.0], 0.9, 1.0), (list(map(float, range(1, 11))), 0.9, 9.0), ([1.0, 2.0], 0.9, 2.0)],
)
def test_percentile_is_nearest_rank(vals, p, want):
    assert ts.percentile(vals, p) == want
