import gzip
import json
import threading
import urllib.error
import urllib.request

import pytest

import otel_receiver
import otel_speed


def span(name="claude_code.llm_request", **attrs):
    def wrap(v):
        if isinstance(v, bool):
            return {"boolValue": v}
        if isinstance(v, int):
            return {"intValue": str(v)}  # OTLP/JSON encodes 64-bit ints as strings
        if isinstance(v, float):
            return {"doubleValue": v}
        return {"stringValue": v}

    return {"name": name, "attributes": [{"key": k, "value": wrap(v)} for k, v in attrs.items()]}


def export(*spans):
    return {"resourceSpans": [{"scopeSpans": [{"spans": list(spans)}]}]}


def write_exports(path, *payloads):
    path.write_text("\n".join(json.dumps(p) for p in payloads) + "\n")
    return path


GOOD = {
    "model": "m",
    "output_tokens": 200,
    "duration_ms": 4000,
    "ttft_ms": 1000,
    "attempt": 1,
    "success": True,
}


def test_derive_computes_ttft_and_decode_rate():
    row = otel_speed.derive(GOOD, "s")
    assert row["ttft_s"] == 1.0
    assert row["total_s"] == 4.0
    assert row["tps_total"] == 50.0
    assert row["tps_decode"] == pytest.approx(66.7)


@pytest.mark.parametrize(
    "override",
    [{"success": False}, {"attempt": 2}, {"duration_ms": 0}, {"output_tokens": None}],
)
def test_derive_skips_unclean_calls(override):
    assert otel_speed.derive({**GOOD, **override}, "s") is None


def test_derive_without_ttft_still_gives_total_rate():
    row = otel_speed.derive({k: v for k, v in GOOD.items() if k != "ttft_ms"}, "s")
    assert row["ttft_s"] is None
    assert row["tps_decode"] is None
    assert row["tps_total"] == 50.0


def test_only_llm_request_spans_are_read(tmp_path):
    f = write_exports(
        tmp_path / "t.jsonl",
        export(span(**GOOD), span("claude_code.tool", tool_name="Bash")),
        export(span(**{**GOOD, "model": "other"})),
    )
    models = [a["model"] for a in otel_speed.llm_request_spans(f)]
    assert models == ["m", "other"]
    assert next(otel_speed.llm_request_spans(f))["output_tokens"] == 200


def test_malformed_lines_and_shapes_are_skipped(tmp_path):
    f = tmp_path / "t.jsonl"
    f.write_text(
        "not json\nnull\n[1]\n"
        + json.dumps({"resourceSpans": [None, {"scopeSpans": None}]})
        + "\n"
        + json.dumps(export(span(**GOOD)))
        + "\n"
    )
    assert len(list(otel_speed.llm_request_spans(f))) == 1


@pytest.fixture
def receiver(tmp_path):
    out = tmp_path / "sub" / "traces.jsonl"
    server = otel_receiver.make_server(0, out)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{server.server_address[1]}", out
    server.shutdown()
    server.server_close()


def post(url, body, headers=None):
    req = urllib.request.Request(url, data=body, headers=headers or {}, method="POST")
    return urllib.request.urlopen(req)


def test_receiver_appends_one_line_per_export_and_speed_reads_it(receiver):
    base, out = receiver
    for _ in range(2):
        resp = post(base + "/v1/traces", json.dumps(export(span(**GOOD))).encode())
        assert resp.status == 200
    assert len(out.read_text().splitlines()) == 2
    assert len(list(otel_speed.llm_request_spans(out))) == 2


def test_receiver_accepts_gzip(receiver):
    base, out = receiver
    body = gzip.compress(json.dumps(export(span(**GOOD))).encode())
    post(base + "/v1/traces", body, {"Content-Encoding": "gzip"})
    assert len(list(otel_speed.llm_request_spans(out))) == 1


def test_receiver_rejects_non_json_traces(receiver):
    base, out = receiver
    with pytest.raises(urllib.error.HTTPError) as e:
        post(base + "/v1/traces", b"\x0a\x02protobuf")
    assert e.value.code == 415
    assert not out.exists()


def test_receiver_discards_other_signals(receiver):
    base, out = receiver
    assert post(base + "/v1/metrics", b"anything").status == 200
    assert not out.exists()
