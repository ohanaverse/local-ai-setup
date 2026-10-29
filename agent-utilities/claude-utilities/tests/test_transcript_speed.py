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
    [
        (10, "<200"),
        (199, "<200"),
        (200, "200-1000"),
        (999, "200-1000"),
        (1000, "1000-3000"),
        (2999, "1000-3000"),
        (3000, "3000+"),
    ],
)
def test_band_edges(out, band):
    assert ts.band_of(out) == band


@pytest.mark.parametrize(
    ("ctx", "band"),
    [
        (0, "<10k"),
        (9_999, "<10k"),
        (10_000, "10k-30k"),
        (29_999, "10k-30k"),
        (30_000, "30k-100k"),
        (99_999, "30k-100k"),
        (100_000, "100k+"),
    ],
)
def test_ctx_band_edges(ctx, band):
    assert ts.ctx_band_of(ctx) == band


def test_aggregate_groups_by_model_and_band():
    rows = [row("a", 100, 10.0), row("a", 150, 30.0), row("a", 500, 50.0), row("b", 100, 5.0)]
    by_key = {(b["model"], b["band"]): b for b in ts.aggregate(rows, 50)["bands"]}
    assert set(by_key) == {("a", "<200"), ("a", "200-1000"), ("b", "<200")}
    small = by_key[("a", "<200")]
    assert small["n"] == 2
    assert small["tps_total_p50"] == 20.0
    assert small["tps_total_p90"] == 30.0


def test_aggregate_carries_no_session_data():
    r = row("a", 100, 10.0) | {"session": "secret-project.jsonl"}
    assert "secret-project" not in json.dumps(ts.aggregate([r], 50))


def agg_file(path: Path, bands: list[dict], min_tokens: int | None = 50) -> str:
    data = {"bands": bands}
    if min_tokens is not None:
        data["min_tokens"] = min_tokens
    path.write_text(json.dumps(data))
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


def test_compare_handles_long_model_names_and_null_percentiles(tmp_path):
    name = "claude-haiku-4-5-20251001"
    a = agg_file(tmp_path / "a.json", [band(name, "<200", None, None)])
    b = agg_file(tmp_path / "b.json", [band(name, "<200", 12.0, 12.0)])
    out = ts.compare(a, b)
    assert f"{name}  <200" in out
    assert "n/a" in out


def test_compare_orders_bands_by_size(tmp_path):
    bands = [band("m", n, 1.0, 1.0) for n in ("3000+", "<200", "1000-3000", "200-1000")]
    f = agg_file(tmp_path / "a.json", bands)
    out = ts.compare(f, f)
    assert out.index("<200") < out.index("200-1000") < out.index("1000-3000") < out.index("3000+")


def test_compare_rejects_malformed_band_entry(tmp_path):
    bad = agg_file(tmp_path / "bad.json", [{"model": "m"}])
    with pytest.raises(ValueError, match="bad band entry"):
        ts.compare(bad, bad)


def test_aggregate_records_min_tokens():
    assert ts.aggregate([], 200)["min_tokens"] == 200


def test_compare_warns_when_min_tokens_differ(tmp_path):
    a = agg_file(tmp_path / "a.json", [band("m", "<200", 1.0, 1.0)], 50)
    b = agg_file(tmp_path / "b.json", [band("m", "<200", 1.0, 1.0)], 200)
    assert "--min-tokens differs (A=50, B=200)" in ts.compare(a, b)


def test_compare_notes_missing_min_tokens_and_is_quiet_when_equal(tmp_path):
    bands = [band("m", "<200", 1.0, 1.0)]
    old = agg_file(tmp_path / "old.json", bands, None)
    new = agg_file(tmp_path / "new.json", bands, 50)
    assert "no min_tokens" in ts.compare(old, new)
    out = ts.compare(new, new)
    assert "WARNING" not in out
    assert "no min_tokens" not in out


def test_require_python_rejects_old_interpreter():
    with pytest.raises(SystemExit) as e:
        ts.require_python((3, 9, 6))
    assert "needs Python >= 3.11, found 3.9.6" in str(e.value)
    assert "uv run" in str(e.value)


def test_require_python_accepts_current_interpreter():
    ts.require_python()
    ts.require_python((3, 11, 0))


# --- context size, cache, startup, fallbacks -------------------------------------------------


def usage_assistant(ts_: str, mid: str, out: int, **extra) -> dict:
    usage = {
        "input_tokens": 10,
        "cache_read_input_tokens": 900,
        "cache_creation_input_tokens": 90,
        "output_tokens": out,
    }
    msg = {"id": mid, "model": "m", "usage": usage}
    return {"type": "assistant", "timestamp": ts_, "message": msg} | extra


def test_context_tokens_sum_input_and_both_cache_fields(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [request("2026-01-01T00:00:00Z"), usage_assistant("2026-01-01T00:00:02Z", "m1", 200)],
    )
    (call,) = ts.calls_from_transcript(f)
    r = ts.derive(call)
    assert r["ctx_tokens"] == 1000
    assert r["cache_hit"] == 0.9


def test_missing_cache_fields_leave_cache_hit_unknown_but_keep_input(tmp_path):
    rec = usage_assistant("2026-01-01T00:00:02Z", "m1", 200)
    del rec["message"]["usage"]["cache_read_input_tokens"]
    del rec["message"]["usage"]["cache_creation_input_tokens"]
    f = write_transcript(tmp_path / "s.jsonl", [request("2026-01-01T00:00:00Z"), rec])
    (call,) = ts.calls_from_transcript(f)
    r = ts.derive(call)
    assert r["ctx_tokens"] == 10
    assert r["cache_hit"] is None


def test_missing_usage_input_leaves_context_unknown(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [request("2026-01-01T00:00:00Z"), assistant("2026-01-01T00:00:02Z", "m1", 200)],
    )
    (call,) = ts.calls_from_transcript(f)
    r = ts.derive(call)
    assert r["ctx_tokens"] is None
    assert r["ctx_band"] is None


def test_first_main_call_ignores_sidechain_and_marks_one_per_file(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [
            request("2026-01-01T00:00:00Z"),
            usage_assistant("2026-01-01T00:00:02Z", "m1", 10, isSidechain=True),
            request("2026-01-01T00:00:10Z"),
            usage_assistant("2026-01-01T00:00:12Z", "m2", 10, isSidechain=False),
            request("2026-01-01T00:00:20Z"),
            usage_assistant("2026-01-01T00:00:22Z", "m3", 10, isSidechain=False),
        ],
    )
    rows = [ts.derive(c) for c in ts.calls_from_transcript(f)]
    assert [r["first_main"] for r in rows] == [False, True, False]


def test_requested_model_is_carried_through(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [
            request("2026-01-01T00:00:00Z"),
            usage_assistant("2026-01-01T00:00:02Z", "m1", 10, requestedModel="x[1m]"),
        ],
    )
    (call,) = ts.calls_from_transcript(f)
    assert ts.derive(call)["requested_model"] == "x[1m]"


def crow(model="a", out=100, tps=10.0, ctx=5_000, hit=0.5, **kw) -> dict:
    base = row(model, out, tps) | {
        "ctx_tokens": ctx,
        "ctx_band": ts.ctx_band_of(ctx) if ctx is not None else None,
        "cache_hit": hit,
        "first_main": False,
        "requested_model": None,
    }
    return base | kw


def test_aggregate_adds_context_cache_and_p90_fields():
    rows = [crow(ctx=1_000, hit=0.2), crow(ctx=3_000, hit=0.6), crow(ctx=5_000, hit=1.0)]
    (b,) = ts.aggregate(rows, 50)["bands"]
    assert b["ctx_tokens_p50"] == 3_000
    assert b["ctx_tokens_p90"] == 5_000
    assert b["cache_hit_p50"] == 0.6
    assert b["ttfb_s_p90"] == 1.0
    assert b["total_s_p90"] == 2.0


def test_aggregate_ctx_bands_group_by_model_and_context_size():
    rows = [crow(ctx=5_000, tps=10.0), crow(ctx=6_000, tps=30.0), crow(ctx=50_000, tps=20.0)]
    by_key = {(b["model"], b["band"]): b for b in ts.aggregate(rows, 50)["ctx_bands"]}
    assert set(by_key) == {("a", "<10k"), ("a", "30k-100k")}
    assert by_key[("a", "<10k")]["n"] == 2
    assert by_key[("a", "<10k")]["tps_total_p50"] == 20.0


def test_aggregate_ctx_bands_skip_calls_without_context():
    assert ts.aggregate([crow(ctx=None, hit=None)], 50)["ctx_bands"] == []


def test_aggregate_startup_uses_first_main_calls_even_below_min_tokens():
    short = crow(out=5, ctx=20_000, first_main=True)
    other = crow(out=500, ctx=90_000)
    agg = ts.aggregate([other], 50, all_rows=[short, other])
    (s,) = agg["startup"]
    assert s == {"model": "a", "n": 1, "ctx_tokens_p50": 20_000, "ctx_tokens_p90": 20_000}


def test_aggregate_fallbacks_strip_suffix_and_count_only_checked_calls():
    rows = [
        crow(requested_model="a[1m]"),
        crow(requested_model="a"),
        crow(requested_model="opusplan"),
        crow(requested_model=None),
    ]
    fb = ts.aggregate(rows, 50)["fallbacks"]
    assert fb["checked"] == 3
    assert fb["mismatched"] == 1
    assert fb["pairs"] == [{"requested": "opusplan", "answered": "a", "n": 1}]


def test_aggregate_declares_schema_and_carries_no_session_data():
    r = crow() | {"session": "secret-project.jsonl"}
    out = ts.aggregate([r], 50, all_rows=[r | {"first_main": True}])
    assert out["schema"] == 2
    assert "secret-project" not in json.dumps(out)


def ctx_file(path: Path, **sections) -> str:
    data = {"schema": 2, "min_tokens": 50, "bands": [band("m", "<200", 100.0, 120.0)]} | sections
    path.write_text(json.dumps(data))
    return str(path)


def ctx_band(model, name, p50, n=100) -> dict:
    return {"model": model, "band": name, "n": n, "tps_total_p50": p50, "tps_total_p90": p50}


def test_compare_prints_context_and_cache_per_band(tmp_path):
    base = band("m", "<200", 100.0, 120.0)
    a = ctx_file(
        tmp_path / "a.json", bands=[base | {"ctx_tokens_p50": 10_000, "cache_hit_p50": 0.9}]
    )
    b = ctx_file(
        tmp_path / "b.json", bands=[base | {"ctx_tokens_p50": 15_000, "cache_hit_p50": 0.0}]
    )
    out = ts.compare(a, b)
    assert "context tokens" in out
    assert "+50.0%" in out
    assert "0.9" in out


def test_compare_prints_speed_by_context_band(tmp_path):
    a = ctx_file(tmp_path / "a.json", ctx_bands=[ctx_band("m", "<10k", 100.0)])
    b = ctx_file(tmp_path / "b.json", ctx_bands=[ctx_band("m", "<10k", 80.0)])
    out = ts.compare(a, b)
    assert "speed by context size" in out
    assert "-20.0%" in out


def test_compare_prints_startup_and_fallbacks(tmp_path):
    startup_a = [{"model": "m", "n": 20, "ctx_tokens_p50": 20_000, "ctx_tokens_p90": 22_000}]
    startup_b = [{"model": "m", "n": 30, "ctx_tokens_p50": 40_000, "ctx_tokens_p90": 44_000}]
    fb = {"checked": 10, "mismatched": 2, "pairs": [{"requested": "x", "answered": "y", "n": 2}]}
    a = ctx_file(
        tmp_path / "a.json",
        startup=startup_a,
        fallbacks={"checked": 10, "mismatched": 0, "pairs": []},
    )
    b = ctx_file(tmp_path / "b.json", startup=startup_b, fallbacks=fb)
    out = ts.compare(a, b)
    assert "session startup" in out
    assert "+100.0%" in out
    assert "requested != answered" in out
    assert "x -> y" in out


def test_compare_old_file_without_new_sections_shows_na_and_notes_schema(tmp_path):
    old = agg_file(tmp_path / "old.json", [band("m", "<200", 100.0, 120.0)])
    new = ctx_file(
        tmp_path / "new.json",
        bands=[band("m", "<200", 90.0, 110.0) | {"ctx_tokens_p50": 9_000, "cache_hit_p50": 0.5}],
    )
    out = ts.compare(old, new)
    assert "predates the context columns" in out
    assert "n/a" in out


def test_compare_two_current_files_has_no_schema_note(tmp_path):
    f = ctx_file(tmp_path / "f.json")
    assert "predates" not in ts.compare(f, f)


def test_aggregate_context_percentiles_are_whole_tokens():
    rows = [crow(ctx=1_001), crow(ctx=1_002)]
    (b,) = ts.aggregate(rows, 50)["bands"]
    assert b["ctx_tokens_p50"] == 1_002
    assert isinstance(b["ctx_tokens_p50"], int)


def test_compare_says_so_when_only_one_file_has_context_sections(tmp_path):
    old = agg_file(tmp_path / "old.json", [band("m", "<200", 100.0, 120.0)])
    new = ctx_file(
        tmp_path / "new.json",
        ctx_bands=[ctx_band("m", "<10k", 80.0)],
        startup=[{"model": "m", "n": 5, "ctx_tokens_p50": 1, "ctx_tokens_p90": 1}],
    )
    out = ts.compare(old, new)
    assert "speed by context size: n/a" in out
    assert "session startup: n/a" in out


# --- review fixes -------------------------------------------------------------------------------


def test_first_main_is_per_model_so_a_side_call_does_not_hide_the_main_one(tmp_path):
    def with_model(rec, model):
        rec["message"]["model"] = model
        return rec

    f = write_transcript(
        tmp_path / "s.jsonl",
        [
            request("2026-01-01T00:00:00Z"),
            with_model(usage_assistant("2026-01-01T00:00:02Z", "m1", 10), "haiku"),
            request("2026-01-01T00:00:10Z"),
            with_model(usage_assistant("2026-01-01T00:00:12Z", "m2", 10), "sonnet"),
        ],
    )
    rows = [ts.derive(c) for c in ts.calls_from_transcript(f)]
    assert [(r["model"], r["first_main"]) for r in rows] == [("haiku", True), ("sonnet", True)]


def test_unpaired_first_call_stops_a_later_call_becoming_first_main(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [
            usage_assistant("2026-01-01T00:00:02Z", "m1", 10),
            request("2026-01-01T00:00:10Z"),
            usage_assistant("2026-01-01T00:00:12Z", "m2", 10),
        ],
    )
    (row_,) = [ts.derive(c) for c in ts.calls_from_transcript(f)]
    assert row_["first_main"] is False


def test_non_string_requested_model_is_ignored(tmp_path):
    f = write_transcript(
        tmp_path / "s.jsonl",
        [
            request("2026-01-01T00:00:00Z"),
            usage_assistant("2026-01-01T00:00:02Z", "m1", 10, requestedModel=["x"]),
        ],
    )
    (call,) = ts.calls_from_transcript(f)
    assert ts.derive(call)["requested_model"] is None


def test_fallbacks_accept_aliases_prefixes_dates_and_skip_synthetic():
    rows = [
        crow(model="claude-sonnet-5-5", requested_model="sonnet"),
        crow(model="qwen", requested_model="ollama/qwen"),
        crow(model="claude-haiku-5-5-20260101", requested_model="claude-haiku-5-5"),
        crow(model="<synthetic>", requested_model="sonnet"),
        crow(model="claude-opus-5-5", requested_model="sonnet"),
    ]
    fb = ts.fallbacks(rows)
    assert fb["checked"] == 4
    assert fb["pairs"] == [{"requested": "sonnet", "answered": "claude-opus-5-5", "n": 1}]


def test_compare_tolerates_section_entries_missing_fields(tmp_path):
    a = ctx_file(
        tmp_path / "a.json",
        startup=[{"model": "m"}],
        ctx_bands=[{"model": "m", "band": "<10k"}],
        fallbacks={"pairs": [{}, "junk"]},
    )
    b = ctx_file(
        tmp_path / "b.json",
        startup=[{"model": "m"}],
        ctx_bands=[{"model": "m", "band": "<10k"}],
        fallbacks={"pairs": []},
    )
    out = ts.compare(a, b)
    assert "n/a" in out


def test_compare_newer_schema_gets_its_own_note(tmp_path):
    a = ctx_file(tmp_path / "a.json")
    b = ctx_file(tmp_path / "b.json")
    data = json.loads(Path(b).read_text())
    data["schema"] = 99
    Path(b).write_text(json.dumps(data))
    out = ts.compare(a, b)
    assert "newer than this script" in out
    assert "predates" not in out


def test_fallbacks_keep_a_shorter_versioned_id_as_a_fallback():
    rows = [crow(model="claude-sonnet-5", requested_model="claude-sonnet-5-5")]
    fb = ts.fallbacks(rows)
    assert fb["pairs"] == [
        {"requested": "claude-sonnet-5-5", "answered": "claude-sonnet-5", "n": 1}
    ]


# --- probe sessions are kept out of organic stats -------------------------------------------

PROBE_FOLDER = "-Users-x--cache-claude-utilities-probe-cwd"


def probe_tree(tmp_path: Path) -> Path:
    for folder in ("-Users-x-github-proj", PROBE_FOLDER):
        d = tmp_path / folder
        d.mkdir()
        (d / "s.jsonl").write_text("")
    return tmp_path


def test_is_probe_path_matches_marker_in_project_folder(tmp_path):
    assert ts.is_probe_path(tmp_path / PROBE_FOLDER / "s.jsonl")
    assert not ts.is_probe_path(tmp_path / "-Users-x-github-proj" / "s.jsonl")


def test_directory_scan_skips_probe_sessions_by_default(tmp_path):
    files = ts.collect_files([probe_tree(tmp_path)])
    assert [f.parent.name for f in files] == ["-Users-x-github-proj"]


def test_include_probes_keeps_them(tmp_path):
    files = ts.collect_files([probe_tree(tmp_path)], include_probes=True)
    assert {f.parent.name for f in files} == {"-Users-x-github-proj", PROBE_FOLDER}


def test_file_named_directly_is_kept(tmp_path):
    probe = probe_tree(tmp_path) / PROBE_FOLDER / "s.jsonl"
    assert ts.collect_files([probe]) == [probe]


def test_collect_files_keeps_an_explicitly_named_probe_folder(tmp_path):
    folder = tmp_path / PROBE_FOLDER
    folder.mkdir()
    (folder / "s.jsonl").write_text("")
    assert ts.collect_files([folder]) == [folder / "s.jsonl"]
