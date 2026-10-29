import json
from pathlib import Path

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
