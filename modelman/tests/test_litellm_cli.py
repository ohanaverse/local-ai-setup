"""modelman litellm CLI: status/on/off/set are pure passthroughs to
`wt litellm ...` (wt owns the routing state since 2026-09-21). modelman must
never write [litellm] into modelman.toml, never make route calls or bounce
the proxy, and never leak the api key or a traceback."""

import subprocess

from typer.testing import CliRunner

from modelman import wt_bridge
from modelman.main import app

runner = CliRunner()


def test_litellm_status_passes_wt_text_through(monkeypatch):
    # `status` must print wt's text verbatim (the ***last4 masking lives in
    # wt) and must not consult the legacy modelman.toml table.
    text = "litellm: on\n  url: http://localhost:4000\n  api_key: ***cret\n"
    monkeypatch.setattr(wt_bridge, "litellm_status_text", lambda: text)
    result = runner.invoke(app, ["litellm", "status"])
    assert result.exit_code == 0
    assert result.output == text


def test_litellm_on_off_set_call_wt(monkeypatch):
    # Pins that on/off/set only call the bridge (wt is the sole writer) and
    # forward only what the user passed; confirmation wording is unchanged.
    seen = []
    monkeypatch.setattr(wt_bridge, "litellm_set_enabled", lambda on: seen.append(("enabled", on)))
    monkeypatch.setattr(wt_bridge, "litellm_set", lambda url, key: seen.append(("set", url, key)))
    on = runner.invoke(app, ["litellm", "on"])
    off = runner.invoke(app, ["litellm", "off"])
    st = runner.invoke(app, ["litellm", "set", "--url", "http://u", "--api-key", "k"])
    only_url = runner.invoke(app, ["litellm", "set", "--url", "http://v"])
    assert seen == [
        ("enabled", True),
        ("enabled", False),
        ("set", "http://u", "k"),
        ("set", "http://v", None),
    ]
    assert "litellm: on" in on.output
    assert "litellm: off" in off.output
    assert "litellm: updated" in st.output
    assert "litellm: updated" in only_url.output


def test_litellm_set_writes_nothing_to_modelman_toml(tmp_path, monkeypatch):
    # The whole point of the move: modelman.toml is never created/modified by
    # the litellm commands.
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    runner.invoke(app, ["litellm", "on"])
    runner.invoke(app, ["litellm", "set", "--url", "http://x", "--api-key", "k"])
    assert not state_path.exists()


def test_litellm_on_warns_when_wt_config_incomplete(monkeypatch):
    # An unconfigured url/key is still a real problem; `on` and `off` keep
    # warning (on stderr, via wt's status) so the state does not go unnoticed.
    # The two commands describe different consequences.
    monkeypatch.setattr(
        wt_bridge, "litellm_status", lambda: wt_bridge.LitellmStatus(True, "", False)
    )
    on = runner.invoke(app, ["litellm", "on"])
    off = runner.invoke(app, ["litellm", "off"])
    assert "litellm.url or litellm.api_key is not set" in on.output
    assert "wt will fail at launch time" in on.output
    assert "litellm.url or litellm.api_key is not set" in off.output
    assert "claude+openrouter" in off.output


def test_litellm_off_no_warning_when_configured(monkeypatch):
    # Counterpart: once wt reports url and key set, no false-positive warning
    # (a noisy warning trains users to ignore it).
    monkeypatch.setattr(
        wt_bridge, "litellm_status", lambda: wt_bridge.LitellmStatus(False, "http://u", True)
    )
    result = runner.invoke(app, ["litellm", "off"])
    assert result.exit_code == 0
    assert "warning" not in result.output


def test_litellm_errors_exit_1_without_traceback_or_key(monkeypatch):
    # A WtBridgeError (wt missing/failed) must become `error: <msg>` + exit 1
    # for every subcommand, with no traceback and no api key in the output.
    def boom(*a, **k):
        raise wt_bridge.WtNotFoundError("wt not found on PATH; install it with `make install`")

    monkeypatch.setattr(wt_bridge, "litellm_status_text", boom)
    monkeypatch.setattr(wt_bridge, "litellm_set_enabled", boom)
    monkeypatch.setattr(wt_bridge, "litellm_set", boom)
    for argv in (
        ["litellm", "status"],
        ["litellm", "on"],
        ["litellm", "off"],
        ["litellm", "set", "--api-key", "sk-secret-value"],
    ):
        result = runner.invoke(app, argv)
        assert result.exit_code == 1, argv
        assert "error: wt not found" in result.output
        assert "sk-secret-value" not in result.output
        assert "Traceback" not in result.output


def test_litellm_set_with_nothing_to_set_still_calls_wt_once(monkeypatch):
    # Behavior parity with the old command: no flags -> still "litellm: updated"
    # (wt treats it as a no-op); the bridge is not given phantom values.
    seen = []
    monkeypatch.setattr(wt_bridge, "litellm_set", lambda url, key: seen.append((url, key)))
    result = runner.invoke(app, ["litellm", "set"])
    assert result.exit_code == 0
    assert seen == [(None, None)]


def test_litellm_commands_never_touch_routes_or_the_proxy(monkeypatch):
    # The on/off/set toggle is routing POLICY only: never a route call, since
    # a toggle that bounced the shared proxy would be an outage for others.
    calls = []
    monkeypatch.setattr(wt_bridge, "expose", lambda *a, **k: calls.append("expose"))
    monkeypatch.setattr(wt_bridge, "unexpose", lambda *a, **k: calls.append("unexpose"))
    monkeypatch.setattr(
        subprocess,
        "run",
        lambda *a, **k: calls.append("subprocess"),  # noqa: ARG005
    )
    runner.invoke(app, ["litellm", "on"])
    runner.invoke(app, ["litellm", "off"])
    runner.invoke(app, ["litellm", "set", "--url", "http://x", "--api-key", "k"])
    assert "expose" not in calls and "unexpose" not in calls
