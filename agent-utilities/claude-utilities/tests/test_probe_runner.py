import json
import stat
import sys
from pathlib import Path

import pytest

import probe_runner as pr

FIXTURES = Path(__file__).parent / "fixtures"


def load_fixture(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text())


def test_tiny_fixture_has_expected_fields():
    d = load_fixture("claude_p_tiny.json")
    for key in ("session_id", "is_error", "duration_ms", "duration_api_ms", "result"):
        assert key in d
    assert d["is_error"] is False
    assert d["duration_ms"] >= d["duration_api_ms"] >= 0


# --- cases and prompts -----------------------------------------------------------------------


def test_cases_match_the_spec():
    assert list(pr.CASES) == ["tiny", "count", "fill20k", "fill80k"]
    assert [pr.CASES[n].filler_tokens for n in pr.CASES] == [0, 0, 20_000, 80_000]


def test_make_filler_is_deterministic_and_scales():
    assert pr.make_filler(1_000) == pr.make_filler(1_000)
    ratio = len(pr.make_filler(2_000)) / len(pr.make_filler(1_000))
    assert 1.8 < ratio < 2.2


def test_filler_is_made_of_real_lines():
    lines = pr.make_filler(200).splitlines()
    assert len(lines) > 10
    assert all(line.startswith("record ") for line in lines[:-1])
    assert "\\n" not in pr.make_filler(200)


def test_prompt_ends_with_nonce():
    for case in pr.CASES.values():
        assert pr.build_prompt(case, "123").endswith("(ref 123)")


def test_prompts_share_prefix_across_nonces():
    case = pr.CASES["fill20k"]
    a, b = pr.build_prompt(case, "111111"), pr.build_prompt(case, "222222")
    assert a != b
    assert a.rsplit("(ref", 1)[0] == b.rsplit("(ref", 1)[0]
    assert len(a.rsplit("(ref", 1)[0]) > len(pr.make_filler(20_000))


def test_lean_flags_are_exact_and_never_bare():
    assert pr.VARIANT_FLAGS["default"] == []
    assert pr.VARIANT_FLAGS["lean"] == [
        "--setting-sources",
        "project",
        "--strict-mcp-config",
        "--disable-slash-commands",
    ]
    flat = [f for flags in pr.VARIANT_FLAGS.values() for f in flags]
    assert "--bare" not in flat
    assert "--no-session-persistence" not in flat


# --- one run, against a fake `claude` --------------------------------------------------------

MODEL = "claude-sonnet-5-5"

FAKE_CLAUDE = """#!{python}
import json, os, sys, time
from pathlib import Path

here = Path(__file__).parent
spec = json.loads((here / "spec.json").read_text())
(here / "invocation.json").write_text(
    json.dumps({{"argv": sys.argv[1:], "stdin": sys.stdin.read(), "cwd": os.getcwd()}})
)
time.sleep(spec.get("sleep", 0))
if "stdout_raw" in spec:
    print(spec["stdout_raw"])
    sys.exit(spec.get("exit_code", 0))
out = json.loads(Path(spec["fixture"]).read_text())
out.update(spec.get("result", {{}}))
out["session_id"] = spec["session_id"]
if spec.get("write_transcript", True):
    d = Path(spec["projects_dir"]) / "-probe"
    d.mkdir(parents=True, exist_ok=True)
    lines = []
    for i, c in enumerate(spec["calls"]):
        base = i * 10
        usage = {{"input_tokens": 10, "cache_read_input_tokens": 900,
                  "cache_creation_input_tokens": 90, "output_tokens": c["out"]}}
        msg = {{"id": f"m{{i}}", "model": c["model"], "usage": usage}}
        lines.append({{"type": "api-request", "timestamp": f"2026-01-01T00:00:{{base:02d}}Z"}})
        for offset in (1, 3):
            stamp = f"2026-01-01T00:00:{{base + offset:02d}}Z"
            lines.append({{"type": "assistant", "timestamp": stamp, "message": msg}})
    (d / (spec["session_id"] + ".jsonl")).write_text("\\n".join(map(json.dumps, lines)) + "\\n")
print(json.dumps(out))
sys.exit(spec.get("exit_code", 0))
"""


def make_fake_claude(tmp_path: Path, **spec) -> dict:
    """A stand-in `claude` that prints the recorded JSON and writes a matching transcript.

    Returns the keyword arguments run_once needs, plus `invocation` (a callable that reads back
    how the fake was called)."""
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    projects = tmp_path / "projects"
    probe_dir = tmp_path / "probe-cwd"
    probe_dir.mkdir()
    full = {
        "fixture": str(FIXTURES / "claude_p_tiny.json"),
        "session_id": "11111111-2222-3333-4444-555555555555",
        "projects_dir": str(projects),
        "calls": [{"model": MODEL, "out": 300}],
        "result": {"duration_ms": 30, "duration_api_ms": 10, "ttft_ms": 15},
    } | spec
    (bin_dir / "spec.json").write_text(json.dumps(full))
    script = bin_dir / "claude"
    script.write_text(FAKE_CLAUDE.format(python=sys.executable))
    script.chmod(script.stat().st_mode | stat.S_IEXEC)
    return {
        "model": MODEL,
        "claude_bin": str(script),
        "probe_dir": probe_dir,
        "projects_dir": projects,
        "invocation": lambda: json.loads((bin_dir / "invocation.json").read_text()),
    }


def run_fake(tmp_path, case="tiny", variant="default", **spec):
    kw = make_fake_claude(tmp_path, **spec)
    kw.pop("invocation")
    return pr.run_once(pr.CASES[case], variant, 0, transcript_wait_s=0.3, **kw)


def test_run_once_measures_pinned_model_call(tmp_path):
    run = run_fake(tmp_path)
    assert run.discard_reason is None
    assert (run.case, run.variant, run.index) == ("tiny", "default", 0)
    assert run.ttfb_s == 1.0
    assert run.total_s == 3.0
    assert run.tps_total == 100.0
    assert run.ctx_tokens == 1000
    assert run.cache_hit == 0.9
    assert run.api_s == pytest.approx(0.010)
    assert run.client_overhead_s == pytest.approx(0.020)
    assert run.ttft_s == pytest.approx(0.015)
    assert run.startup_s == pytest.approx(run.wall_s - 0.030)


def test_side_call_to_other_model_is_ignored(tmp_path):
    calls = [{"model": "claude-haiku-5-5", "out": 20}, {"model": MODEL, "out": 300}]
    run = run_fake(tmp_path, calls=calls)
    assert run.discard_reason is None
    assert run.tps_total == 100.0


def test_two_pinned_calls_are_discarded_as_extra_calls(tmp_path):
    run = run_fake(tmp_path, calls=[{"model": MODEL, "out": 300}, {"model": MODEL, "out": 300}])
    assert run.discard_reason == "extra-calls"
    assert run.tps_total is None


def test_no_pinned_call_is_discarded_as_no_call(tmp_path):
    run = run_fake(tmp_path, calls=[{"model": "claude-haiku-5-5", "out": 20}])
    assert run.discard_reason == "no-call"


def test_is_error_is_discarded(tmp_path):
    run = run_fake(tmp_path, result={"is_error": True, "duration_ms": 30, "duration_api_ms": 10})
    assert run.discard_reason == "error"


def test_non_json_stdout_is_discarded(tmp_path):
    run = run_fake(tmp_path, stdout_raw="not json at all")
    assert run.discard_reason == "bad-json"


def test_missing_transcript_is_discarded_after_bounded_wait(tmp_path):
    run = run_fake(tmp_path, write_transcript=False)
    assert run.discard_reason == "no-transcript"


def test_hang_is_discarded_as_timeout(tmp_path):
    kw = make_fake_claude(tmp_path, sleep=5)
    kw.pop("invocation")
    run = pr.run_once(pr.CASES["tiny"], "default", 0, timeout_s=0.5, **kw)
    assert run.discard_reason == "timeout"


def test_command_uses_variant_flags_and_stdin_prompt(tmp_path):
    kw = make_fake_claude(tmp_path)
    invocation = kw.pop("invocation")
    pr.run_once(pr.CASES["count"], "lean", 3, transcript_wait_s=0.3, **kw)
    seen = invocation()
    assert seen["argv"] == [
        "-p",
        "--output-format",
        "json",
        "--model",
        MODEL,
        *pr.VARIANT_FLAGS["lean"],
    ]
    assert seen["stdin"].startswith(pr.CASES["count"].body)
    assert seen["stdin"].rstrip().endswith(")") and "(ref " in seen["stdin"]
    assert Path(seen["cwd"]).resolve() == kw["probe_dir"].resolve()
