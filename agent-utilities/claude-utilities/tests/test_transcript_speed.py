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
