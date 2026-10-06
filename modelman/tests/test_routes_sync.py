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


def test_sync_routes_passes_on_a_redirected_registry_refusal(monkeypatch):
    # wt refuses a redirected registry paired with the default config.yaml;
    # the same shell's `wt litellm sync` is refused too, so do not advise it.
    refusal = (
        "LiteLLM routes not touched: the registry is /tmp/r.toml but config.yaml is the "
        "default /x/config.yaml — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to"
    )

    def fake(args, env=None, timeout=None):
        return subprocess.CompletedProcess(args, 1, "", f"Error: {refusal}\nwt: {refusal}\n")

    monkeypatch.setattr(wt_bridge, "_run", fake)
    assert sync_routes() == [refusal]


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


def test_sync_routes_skips_wt_without_a_litellm_config(tmp_path, monkeypatch, wt_calls):
    # A user without LiteLLM has no config.yaml; wt's sync would fail with
    # "LiteLLM config not found" and every modelman write would warn. Parity
    # with wt's own route hook: a missing config.yaml is silent, wt not run.
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(tmp_path / "missing.yaml"))
    assert sync_routes() == []
    assert wt_calls == []


def test_sync_routes_skips_wt_for_a_missing_explicit_path(tmp_path, wt_calls):
    assert sync_routes(litellm_path=tmp_path / "missing.yaml") == []
    assert wt_calls == []


def test_sync_routes_runs_wt_for_an_existing_explicit_path(tmp_path, wt_calls):
    path = tmp_path / "config.yaml"
    path.write_text("model_list: []\n")
    assert sync_routes(litellm_path=path) == []
    assert wt_calls == [["sync", "--json"]]
