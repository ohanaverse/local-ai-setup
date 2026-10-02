"""sync_routes: modelman's single LiteLLM touchpoint after #179."""

import json
import subprocess

from modelman import wt_bridge
from modelman.litellm import sync_routes


def test_sync_routes_runs_wt_sync_once(wt_calls):
    assert sync_routes() == []
    assert wt_calls == [["sync", "--json"]]


def test_sync_routes_reports_per_id_errors_and_warnings(monkeypatch):
    def fake(args, env=None, timeout=None):
        out = '{"outcomes":[{"id":"x/y","error":"provider \\"x\\" has no LiteLLM mapping"}],"changed":true,"warnings":["restart: boom"]}'
        return subprocess.CompletedProcess(args, 1, out, "")

    monkeypatch.setattr(wt_bridge, "_run", fake)
    warnings = sync_routes()
    assert "restart: boom" in warnings
    assert any(w.startswith("x/y: ") for w in warnings)


def test_sync_routes_timeout_warns(monkeypatch):
    def fake(args, env=None, timeout=None):
        raise wt_bridge.WtBridgeTimeoutError("wt litellm sync timed out after 120s")

    monkeypatch.setattr(wt_bridge, "_run", fake)
    warnings = sync_routes()
    assert len(warnings) == 1
    assert "timed out" in warnings[0] and "wt litellm sync" in warnings[0]


def test_sync_routes_wt_missing_warns(monkeypatch):
    def fake(args, env=None, timeout=None):
        raise wt_bridge.WtNotFoundError("wt not found on PATH; install it with `make install`")

    monkeypatch.setattr(wt_bridge, "_run", fake)
    assert "wt not found" in sync_routes()[0]


def _fake_sync(monkeypatch, outcomes, warnings=()):
    def fake(args, env=None, timeout=None):
        out = json.dumps({"outcomes": outcomes, "changed": True, "warnings": list(warnings)})
        return subprocess.CompletedProcess(args, 1, out, "")

    monkeypatch.setattr(wt_bridge, "_run", fake)


def test_sync_routes_groups_identical_errors(monkeypatch):
    text = 'provider "openrouter": secret_ref OPENROUTER_API_KEY resolved empty'
    _fake_sync(
        monkeypatch,
        [
            {"id": "openrouter/a", "error": f'model "openrouter/a": {text}'},
            {"id": "openrouter/b", "error": f'model "openrouter/b": {text}'},
            {"id": "ollama/ok", "action": "rewritten"},
        ],
        warnings=["restart: boom"],
    )
    assert sync_routes() == [
        "restart: boom",
        f"{text} (2 models: openrouter/a, openrouter/b)",
    ]


def test_sync_routes_strips_wt_prefix_for_single_id(monkeypatch):
    _fake_sync(monkeypatch, [{"id": "x/y", "error": 'model "x/y": empty model_name'}])
    assert sync_routes() == ["x/y: empty model_name"]


def test_sync_routes_keeps_distinct_errors_in_first_seen_order(monkeypatch):
    _fake_sync(
        monkeypatch,
        [
            {"id": "b/1", "error": 'model "b/1": second'},
            {"id": "a/1", "error": 'model "a/1": first'},
            {"id": "b/2", "error": 'model "b/2": second'},
        ],
    )
    assert sync_routes() == ["second (2 models: b/1, b/2)", "a/1: first"]
