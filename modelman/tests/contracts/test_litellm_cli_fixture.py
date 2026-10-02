"""Cross-language contract: the wt `litellm --json` shapes modelman parses.

Read together with wt/cmd/wt/litellm_test.go; both consume
docs/contracts/litellm-cli.sample.json, so a schema change on either side
fails both CI jobs in the same PR."""

import json
from pathlib import Path

from modelman.wt_bridge import parse_change_result, parse_routed, parse_status

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "litellm-cli.sample.json"


def test_change_fixture_parses():
    # Pins that every key of wt's change-result shape (shared by sync) is
    # understood by the bridge, including per-id errors and restart warnings.
    doc = json.loads(FIXTURE.read_text())
    res = parse_change_result(json.dumps(doc["change"]))
    assert [o.id for o in res.outcomes] == ["ollama/gemma:9b", "claude/sonnet"]
    assert res.outcomes[0].action == "exposed"
    assert res.outcomes[1].error and res.changed and res.warnings


def test_sync_fixture_parses():
    # Pins `wt litellm sync --json`, which modelman's bridge parses with
    # parse_change_result: every sync action survives the parse, and
    # a per-id build error arrives as an error, not an action.
    doc = json.loads(FIXTURE.read_text())
    res = parse_change_result(json.dumps(doc["sync"]))
    assert [(o.id, o.action) for o in res.outcomes if not o.error] == [
        ("openrouter/old", "unrouted"),
        ("openrouter/x", "rewritten"),
        ("openrouter/adopt", "adopted"),
        ("openrouter/new", "routed"),
    ]
    assert [o.id for o in res.outcomes if o.error] == ["openrouter/bad"]
    assert res.changed and res.warnings == []


def test_sync_fixture_error_grouping_strips_wt_prefix(monkeypatch):
    # Pins the Python half of wt's `model "<id>": ` per-id error prefix: the
    # fixture's sync block is produced by a real wt `litellm.Sync` (Go contract
    # test), and sync_routes strips that prefix when grouping. If wt rewords
    # the prefix, this fails instead of grouping silently degrading into a
    # duplicated-prefix warning per model.
    import subprocess

    from modelman import wt_bridge
    from modelman.litellm import sync_routes

    doc = json.loads(FIXTURE.read_text())

    def fake(args, env=None, timeout=None):
        return subprocess.CompletedProcess(args, 1, json.dumps(doc["sync"]), "")

    monkeypatch.setattr(wt_bridge, "_run", fake)
    assert sync_routes() == ["openrouter/bad: empty model_name"]


def test_status_fixture_parses():
    # Pins the routing-state shape modelman's TUI and CLI read.
    doc = json.loads(FIXTURE.read_text())
    st = parse_status(json.dumps(doc["status"]))
    assert st.enabled and st.url == "http://localhost:4000" and st.api_key_set


def test_list_fixture_parses():
    # Pins the `wt litellm list --json` shape routed_ids reads.
    doc = json.loads(FIXTURE.read_text())
    assert parse_routed(json.dumps(doc["list"])) == ["ollama/gemma:9b", "openrouter/x/y"]
