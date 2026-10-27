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


def row(model: str, out: int, tps: float) -> dict:
    return {"model": model, "out_tokens": out, "tps_total": tps, "ttfb_s": 1.0, "total_s": 2.0}


@pytest.mark.parametrize(
    ("out", "band"),
    [(10, "<200"), (199, "<200"), (200, "200-1000"), (999, "200-1000"), (1000, "1000+")],
)
def test_band_edges(out, band):
    assert ts.band_of(out) == band


def test_aggregate_groups_by_model_and_band():
    rows = [row("a", 100, 10.0), row("a", 150, 30.0), row("a", 500, 50.0), row("b", 100, 5.0)]
    by_key = {(b["model"], b["band"]): b for b in ts.aggregate(rows)["bands"]}
    assert set(by_key) == {("a", "<200"), ("a", "200-1000"), ("b", "<200")}
    small = by_key[("a", "<200")]
    assert small["n"] == 2
    assert small["tps_total_p50"] == 20.0
    assert small["tps_total_p90"] == 30.0


def test_aggregate_carries_no_session_data():
    r = row("a", 100, 10.0) | {"session": "secret-project.jsonl"}
    assert "secret-project" not in json.dumps(ts.aggregate([r]))


def agg_file(path: Path, bands: list[dict]) -> str:
    path.write_text(json.dumps({"bands": bands}))
    return str(path)


def band(model: str, name: str, p50: float, p90: float) -> dict:
    return {
        "model": model,
        "band": name,
        "n": 100,
        "tps_total_p50": p50,
        "tps_total_p90": p90,
        "ttfb_s_p50": 1.0,
        "total_s_p50": 2.0,
    }


def test_compare_reports_percent_difference_and_unmatched_rows(tmp_path):
    a = agg_file(
        tmp_path / "a.json", [band("m", "<200", 100.0, 120.0), band("only-a", "<200", 1, 1)]
    )
    b = agg_file(
        tmp_path / "b.json", [band("m", "<200", 80.0, 120.0), band("only-b", "200-1000", 1, 1)]
    )
    out = ts.compare(a, b)
    assert "-20.0%" in out
    assert "+0.0%" in out
    assert "only in A" in out
    assert "only-a" in out
    assert "only in B" in out
    assert "only-b" in out


def test_compare_rejects_non_aggregate_file(tmp_path):
    bad = tmp_path / "bad.json"
    bad.write_text("[1, 2]")
    good = agg_file(tmp_path / "g.json", [])
    with pytest.raises(ValueError, match="not a transcript_speed --json file"):
        ts.compare(str(bad), good)
