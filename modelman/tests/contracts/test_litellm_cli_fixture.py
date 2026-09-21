"""Cross-language contract: the wt `litellm --json` shapes modelman parses.

Read together with wt/cmd/wt/litellm_test.go; both consume
docs/contracts/litellm-cli.sample.json, so a schema change on either side
fails both CI jobs in the same PR."""

import json
from pathlib import Path

from modelman.wt_bridge import parse_change_result, parse_status

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "litellm-cli.sample.json"


def test_change_fixture_parses():
    # Pins that every key wt emits for expose/unexpose/sync is understood by
    # the bridge, including per-id errors and restart warnings.
    doc = json.loads(FIXTURE.read_text())
    res = parse_change_result(json.dumps(doc["change"]))
    assert [o.id for o in res.outcomes] == ["ollama/gemma:9b", "claude/sonnet"]
    assert res.outcomes[1].error and res.changed and res.warnings


def test_status_fixture_parses():
    # Pins the routing-state shape modelman's TUI and CLI read.
    doc = json.loads(FIXTURE.read_text())
    st = parse_status(json.dumps(doc["status"]))
    assert st.enabled and st.url == "http://localhost:4000" and st.api_key_set
