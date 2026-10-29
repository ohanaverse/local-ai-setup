import json
from pathlib import Path

FIXTURES = Path(__file__).parent / "fixtures"


def load_fixture(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text())


def test_tiny_fixture_has_expected_fields():
    d = load_fixture("claude_p_tiny.json")
    for key in ("session_id", "is_error", "duration_ms", "duration_api_ms", "result"):
        assert key in d
    assert d["is_error"] is False
    assert d["duration_ms"] >= d["duration_api_ms"] >= 0
