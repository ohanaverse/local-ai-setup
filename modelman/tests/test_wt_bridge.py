"""Tests for the wt subprocess bridge (wt owns LiteLLM management)."""

import json
import subprocess
from pathlib import Path

import pytest

from modelman import wt_bridge

# The genuine _run, captured at import (collection) time, before any fixture can
# patch it. Tests that need the real _run re-install it with monkeypatch.setattr
# instead of a blanket monkeypatch undo, which would also revert every autouse safety guard.
_REAL_RUN = wt_bridge._run


def _cp(stdout="", returncode=0, stderr=""):
    return subprocess.CompletedProcess(args=[], returncode=returncode, stdout=stdout, stderr=stderr)


@pytest.fixture
def calls(monkeypatch):
    """Record argv/env of every wt invocation and return canned output."""
    seen = []
    state = {"out": _cp(json.dumps({"outcomes": [], "changed": False, "warnings": []}))}

    def fake_run(argv, **kwargs):
        seen.append((argv, kwargs.get("env") or {}))
        return state["out"]

    monkeypatch.setattr(
        wt_bridge,
        "_run",
        lambda args, env=None, timeout=120: fake_run(["wt", "litellm", *args], env=env),
    )
    return seen, state


def test_sync_builds_argv_and_parses_partial_batch_json(calls):
    # Pins the wire contract with `wt litellm sync --json`; per-id errors
    # surface as BridgeOutcome.error rather than exceptions so sync_routes can
    # report them per model. A partial batch exits 1 but still prints JSON,
    # which must be parsed regardless of exit code.
    seen, state = calls
    state["out"] = _cp(
        json.dumps(
            {
                "outcomes": [{"id": "a", "action": "exposed"}, {"id": "b", "error": "nope"}],
                "changed": True,
                "warnings": ["w"],
            }
        ),
        returncode=1,
    )
    res = wt_bridge.sync()
    assert seen[0][0] == ["wt", "litellm", "sync", "--json"]
    assert [(o.id, o.action, o.error) for o in res.outcomes] == [
        ("a", "exposed", None),
        ("b", None, "nope"),
    ]
    assert res.changed and res.warnings == ["w"]


def test_litellm_path_is_passed_via_env(calls):
    # Tests and custom setups point modelman at a non-default config.yaml;
    # the bridge must forward that to wt through WT_LITELLM_CONFIG or wt would
    # edit the developer's real file.
    seen, _ = calls
    wt_bridge.sync(litellm_path=Path("/tmp/x.yaml"))
    assert seen[0][1]["WT_LITELLM_CONFIG"] == "/tmp/x.yaml"


def test_file_level_failure_raises(calls):
    # A non-JSON failure (missing/invalid config.yaml) means nothing applied:
    # the bridge must raise so callers treat the whole batch as failed rather
    # than parsing garbage.
    _, state = calls
    state["out"] = _cp("", returncode=1, stderr="LiteLLM config not found: /x")
    with pytest.raises(wt_bridge.WtBridgeError, match="LiteLLM config not found"):
        wt_bridge.sync()


def test_missing_wt_binary_is_a_clear_error(monkeypatch):
    # modelman now requires the wt binary; fail before any state change with
    # an actionable message instead of a bare FileNotFoundError.
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda _: None)
    with pytest.raises(wt_bridge.WtNotFoundError, match="make install"):
        wt_bridge.ensure_wt()


def test_litellm_status_text_returns_stdout(calls):
    # modelman's `litellm status` CLI passes wt's text through because the
    # ***last4 key masking lives in wt; the bridge must call status WITHOUT
    # --json and return stdout verbatim.
    seen, state = calls
    state["out"] = _cp("litellm: on\n  url: http://x\n  api_key: ***abcd\n")
    assert wt_bridge.litellm_status_text() == "litellm: on\n  url: http://x\n  api_key: ***abcd\n"
    assert seen[0][0] == ["wt", "litellm", "status"]


def test_litellm_status_text_raises_on_failure(calls):
    # A non-zero exit must surface wt's message rather than return garbage.
    _, state = calls
    state["out"] = _cp("", returncode=1, stderr="boom")
    with pytest.raises(wt_bridge.WtBridgeError, match="boom"):
        wt_bridge.litellm_status_text()


def test_change_unexpected_json_shape_is_unparseable_error(calls):
    # JSON that is present but the wrong shape means changes may already have
    # been applied; the error must say the output was unparseable rather than
    # a misleading "wt exited 0".
    _, state = calls
    state["out"] = _cp(json.dumps({"outcomes": [{"nope": 1}]}), returncode=0)
    with pytest.raises(wt_bridge.WtBridgeError, match="unparseable wt output"):
        wt_bridge.sync()


def test_routed_ids_argv_and_parse(calls):
    # routed_ids must call `list --json` and return the routed ids.
    seen, state = calls
    state["out"] = _cp(json.dumps({"routed": ["a", "b"]}))
    assert wt_bridge.routed_ids() == ["a", "b"]
    assert seen[0][0] == ["wt", "litellm", "list", "--json"]


@pytest.mark.parametrize(
    ("url", "key", "expected"),
    [
        ("http://x", None, ["set", "--url", "http://x"]),
        (None, "k", ["set", "--api-key", "k"]),
        ("http://x", "k", ["set", "--url", "http://x", "--api-key", "k"]),
    ],
)
def test_litellm_set_argv(calls, url, key, expected):
    # Pins which flags litellm_set forwards: only the ones actually provided,
    # so an unset field is never blanked in wt's config.
    seen, _ = calls
    wt_bridge.litellm_set(url, key)
    assert seen[0][0] == ["wt", "litellm", *expected]


def test_env_empty_without_litellm_path(calls):
    # With no litellm_path the bridge must not inject WT_LITELLM_CONFIG, so
    # wt uses its own default location.
    seen, _ = calls
    wt_bridge.sync()
    assert not seen[0][1]


def _boom(exc):
    def run(*a, **k):
        raise exc

    return run


@pytest.mark.parametrize(
    "exc",
    [
        subprocess.TimeoutExpired(["wt", "litellm", "set", "--api-key", "SECRETKEY"], 120),
        PermissionError("denied wt"),
        FileNotFoundError("gone"),
    ],
)
def test_run_converts_subprocess_failures_without_leaking_argv(monkeypatch, exc):
    # A hung or unexecutable wt must surface as WtBridgeError, and the message
    # must never carry argv: `litellm set --api-key <secret>` would otherwise
    # leak the key into tracebacks.
    monkeypatch.setattr(wt_bridge, "_run", _REAL_RUN)  # exercise the real _run; guards stay armed
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda _: "/bin/wt")
    monkeypatch.setattr(wt_bridge.subprocess, "run", _boom(exc))
    with pytest.raises(wt_bridge.WtBridgeError) as ei:
        wt_bridge._run(["set", "--api-key", "SECRETKEY"])
    assert "SECRETKEY" not in str(ei.value)


def test_run_uses_short_timeout_and_utf8(monkeypatch):
    # The TUI's status read needs a short timeout, writes keep 120s, and
    # output is decoded as utf-8 regardless of locale.
    monkeypatch.setattr(wt_bridge, "_run", _REAL_RUN)
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda _: "/bin/wt")
    seen = []

    def run(argv, **kw):
        seen.append(kw)
        return _cp("{}")

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    wt_bridge._run(["status", "--json"], timeout=5)
    wt_bridge._run(["on"])
    assert seen[0]["timeout"] == 5 and seen[1]["timeout"] == 120
    assert seen[0]["encoding"] == "utf-8"


def test_real_run_never_executes_a_real_wt_binary(monkeypatch, tmp_path):
    # Safety net: even the genuine _run must be intercepted by the suite's global
    # subprocess guard. A marker-writing `wt` on PATH stands in for the real
    # binary (which would rewrite ~/.config/litellm and bounce the live proxy);
    # if the guard were disarmed the marker file would appear.
    marker = tmp_path / "ran"
    fake_wt = tmp_path / "wt"
    fake_wt.write_text(f"#!/bin/sh\ntouch {marker}\n")
    fake_wt.chmod(0o755)
    monkeypatch.setenv("PATH", f"{tmp_path}:/usr/bin:/bin")
    monkeypatch.setattr(wt_bridge, "_run", _REAL_RUN)
    proc = wt_bridge._run(["status", "--json"])
    assert not marker.exists(), "real wt binary was executed by the test suite"
    assert proc.returncode == 0


def test_no_test_reverts_the_autouse_guards():
    # Reverting the shared monkeypatch disarms every autouse safety guard (fake
    # wt, subprocess interception, litellm config redirect), so no test may do
    # it; tests re-install what they need with monkeypatch.setattr instead.
    needle = "monkeypatch." + "undo("
    offenders = [
        str(p) for p in Path(__file__).parent.rglob("*.py") if needle in p.read_text("utf-8")
    ]
    assert offenders == []


def test_litellm_status_forwards_optional_timeout(monkeypatch):
    # The TUI passes a short timeout; other callers keep _run's default. Only
    # forwarding when given keeps the default behavior unchanged.
    got = []

    def spy(args, env=None, timeout=120):
        got.append(timeout)
        return _cp(json.dumps({"enabled": True, "url": "u", "api_key_set": False}))

    monkeypatch.setattr(wt_bridge, "_run", spy)
    wt_bridge.litellm_status()
    wt_bridge.litellm_status(timeout=3)
    assert got == [120, 3]


@pytest.mark.parametrize(
    "stderr,stdout,rc,want",
    [
        (
            "Error: nothing to set: pass --url and/or --api-key\nwt: nothing to set: pass --url and/or --api-key\n",
            "",
            1,
            "nothing to set: pass --url and/or --api-key",
        ),
        ("LiteLLM config not found: /x\n", "", 1, "LiteLLM config not found: /x"),
        ("Error: a\n\nwt: b\n", "", 1, "a; b"),
        ("", "some stdout\n", 1, "some stdout"),
        ("  \n", "", 3, "wt exited 3"),
    ],
)
def test_msg_cleans_wt_output(stderr, stdout, rc, want):
    # wt prints cobra's `Error: x` plus its own `wt: x`; the CLI prefixes
    # `error: ` itself, so the bridge must yield one clean line (deduped,
    # prefix-stripped), falling back to stdout then "wt exited N".
    proc = _cp(stdout, returncode=rc, stderr=stderr)
    assert wt_bridge._msg(proc, f"wt exited {rc}") == want


def test_sync_timeout_propagates_as_timeout_error(monkeypatch):
    # #179 deleted the post-timeout reconcile (_change used to catch a
    # timeout, read `wt litellm list` back and return a guessed result). A
    # timed-out sync must now surface as WtBridgeTimeoutError — sync_routes
    # turns it into a warning and the next sync converges — with no `list`
    # follow-up. `list` answers here, so a revived reconcile would return a
    # result instead of raising and would show up in `calls`.
    calls: list[list[str]] = []

    def fake_run(args, env=None, timeout=120):
        calls.append(list(args))
        if args[:1] == ["list"]:
            return _cp('{"routed": []}')
        raise wt_bridge.WtBridgeTimeoutError("wt litellm sync timed out after 120s")

    monkeypatch.setattr(wt_bridge, "_run", fake_run)
    with pytest.raises(wt_bridge.WtBridgeTimeoutError, match="sync timed out"):
        wt_bridge.sync()
    assert calls == [["sync", "--json"]]
