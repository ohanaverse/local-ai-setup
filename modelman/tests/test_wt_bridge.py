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


def test_expose_builds_argv_and_parses_json(calls):
    # Pins the wire contract with `wt litellm expose`: modelman always passes
    # --skip-ready-gate (it applied the gate against its own in-memory state)
    # and --json, and per-id errors surface as BridgeOutcome.error rather than
    # exceptions so the queue can report them per model. A partial batch exits
    # 1 but still prints JSON, which must be parsed regardless of exit code.
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
    res = wt_bridge.expose(["a", "b"])
    assert seen[0][0] == ["wt", "litellm", "expose", "--json", "--skip-ready-gate", "--", "a", "b"]
    assert [(o.id, o.action, o.error) for o in res.outcomes] == [
        ("a", "exposed", None),
        ("b", None, "nope"),
    ]
    assert res.changed and res.warnings == ["w"]


def test_unexpose_partial_batch_parses_json_on_exit_1(calls):
    # Same partial-batch rule for unexpose: exit 1 with JSON is per-id errors,
    # not a bridge failure.
    seen, state = calls
    state["out"] = _cp(
        json.dumps(
            {"outcomes": [{"id": "a", "error": "not routed"}], "changed": False, "warnings": []}
        ),
        returncode=1,
    )
    res = wt_bridge.unexpose(["a"])
    assert seen[0][0] == ["wt", "litellm", "unexpose", "--json", "--", "a"]
    assert res.outcomes[0].error == "not routed"


def test_litellm_path_is_passed_via_env(calls):
    # Tests and custom setups point modelman at a non-default config.yaml;
    # the bridge must forward that to wt through WT_LITELLM_CONFIG or wt would
    # edit the developer's real file.
    seen, _ = calls
    wt_bridge.unexpose(["a"], litellm_path=Path("/tmp/x.yaml"))
    assert seen[0][1]["WT_LITELLM_CONFIG"] == "/tmp/x.yaml"


def test_file_level_failure_raises(calls):
    # A non-JSON failure (missing/invalid config.yaml) means nothing applied:
    # the bridge must raise so callers treat the whole batch as failed rather
    # than parsing garbage.
    _, state = calls
    state["out"] = _cp("", returncode=1, stderr="LiteLLM config not found: /x")
    with pytest.raises(wt_bridge.WtBridgeError, match="LiteLLM config not found"):
        wt_bridge.expose(["a"])


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


def test_provider_cloud_flags_never_raises_and_warns_once(monkeypatch, capsys):
    # provider_cloud_flags() runs on TUI render paths for every model row, so a
    # missing/broken wt must degrade to {} with exactly one stderr warning per
    # process rather than crash or spam the terminal.
    def boom(args, env=None, timeout=120):
        raise wt_bridge.WtNotFoundError("wt not found on PATH; install it with `make install`")

    monkeypatch.setattr(wt_bridge, "_run", boom)
    wt_bridge._reset_provider_cache()
    assert wt_bridge.provider_cloud_flags() == {}
    assert wt_bridge.provider_cloud_flags() == {}
    err = capsys.readouterr().err
    assert err.count("cannot read wt's LiteLLM provider table") == 1
    wt_bridge._reset_provider_cache()


def test_provider_cloud_flags_caches_success(monkeypatch):
    # A successful read is cached for the process: wt is only spawned once
    # even though render paths call this per row.
    n = {"c": 0}

    def ok(args, env=None, timeout=120):
        n["c"] += 1
        return _cp(
            json.dumps({"providers": {"ollama": {"cloud": False}, "openrouter": {"cloud": True}}})
        )

    monkeypatch.setattr(wt_bridge, "_run", ok)
    wt_bridge._reset_provider_cache()
    assert wt_bridge.provider_cloud_flags() == {"ollama": False, "openrouter": True}
    assert wt_bridge.provider_cloud_flags() == {"ollama": False, "openrouter": True}
    assert n["c"] == 1
    wt_bridge._reset_provider_cache()


def test_dash_prefixed_id_is_not_parsed_as_flag(calls):
    # Model ids are user data; an id like "--dry-run" must come after a "--"
    # separator or wt (pflag) would treat it as a flag. Verified against a
    # real wt build that `expose -- --dry-run` treats it as a model id.
    seen, _ = calls
    wt_bridge.expose(["--dry-run", "-x"])
    wt_bridge.unexpose(["--dry-run"])
    assert seen[0][0][-3:] == ["--", "--dry-run", "-x"]
    assert seen[1][0][-2:] == ["--", "--dry-run"]


def test_change_unexpected_json_shape_is_unparseable_error(calls):
    # JSON that is present but the wrong shape means changes may already have
    # been applied; the error must say the output was unparseable rather than
    # a misleading "wt exited 0".
    _, state = calls
    state["out"] = _cp(json.dumps({"outcomes": [{"nope": 1}]}), returncode=0)
    with pytest.raises(wt_bridge.WtBridgeError, match="unparseable wt output"):
        wt_bridge.expose(["a"])


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
    wt_bridge.expose(["a"])
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
    # The render-path providers call needs a short timeout, writes keep 120s,
    # and output is decoded as utf-8 regardless of locale.
    monkeypatch.setattr(wt_bridge, "_run", _REAL_RUN)
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda _: "/bin/wt")
    seen = []

    def run(argv, **kw):
        seen.append(kw)
        return _cp("{}")

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    wt_bridge._run(["providers", "--json"], timeout=5)
    wt_bridge._run(["on"])
    assert seen[0]["timeout"] == 5 and seen[1]["timeout"] == 120
    assert seen[0]["encoding"] == "utf-8"


@pytest.mark.parametrize("exc", [subprocess.TimeoutExpired(["wt"], 5), PermissionError("x")])
def test_provider_cloud_flags_survives_subprocess_failures(monkeypatch, capsys, exc):
    # Render paths must get {} plus a single warning even when wt hangs or is
    # unexecutable, not a crash.
    monkeypatch.setattr(wt_bridge, "_run", _REAL_RUN)
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda _: "/bin/wt")
    monkeypatch.setattr(wt_bridge.subprocess, "run", _boom(exc))
    wt_bridge._reset_provider_cache()
    assert wt_bridge.provider_cloud_flags() == {}
    assert wt_bridge.provider_cloud_flags() == {}
    assert capsys.readouterr().err.count("cannot read wt's LiteLLM provider table") == 1
    wt_bridge._reset_provider_cache()


def test_provider_failure_is_negative_cached_for_a_window(monkeypatch):
    # A wt that is installed but erroring must not be re-spawned per model row
    # per render: failures are remembered for a short window, then retried once.
    n = {"c": 0}
    now = {"t": 1000.0}

    def bad(args, env=None, timeout=120):
        n["c"] += 1
        return _cp("", returncode=1, stderr="broken")

    monkeypatch.setattr(wt_bridge, "_run", bad)
    monkeypatch.setattr(wt_bridge.time, "monotonic", lambda: now["t"])
    wt_bridge._reset_provider_cache()
    for _ in range(5):
        assert wt_bridge.provider_cloud_flags() == {}
    assert n["c"] == 1
    now["t"] += wt_bridge._PROVIDER_FAILURE_TTL + 1
    wt_bridge.provider_cloud_flags()
    assert n["c"] == 2
    wt_bridge._reset_provider_cache()


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


def test_expose_timeout_reconciles_via_routed_ids(monkeypatch):
    # A timed-out `wt litellm expose` may have already written config.yaml
    # and restarted the proxy before the kill — the bridge must check the
    # actual routed set (`wt litellm list`, unaffected by the timed-out
    # call) instead of assuming nothing applied, so a timeout can't
    # silently leave modelman.toml's exposed flag out of sync with the
    # real route.
    from modelman import wt_bridge

    def fake_run(args, env=None, timeout=120):
        if args[0] == "expose":
            raise wt_bridge.WtBridgeTimeoutError("wt litellm expose timed out after 120s")
        raise AssertionError(f"unexpected _run call: {args}")

    monkeypatch.setattr(wt_bridge, "_run", fake_run)
    monkeypatch.setattr(wt_bridge, "routed_ids", lambda litellm_path=None: ["m1"])

    result = wt_bridge.expose(["m1", "m2"])
    outcomes = {o.id: o for o in result.outcomes}
    assert outcomes["m1"].action == "exposed" and outcomes["m1"].error is None
    assert outcomes["m2"].action is None and "timed out" in outcomes["m2"].error


def test_expose_timeout_reconcile_failure_still_raises(monkeypatch):
    # If the post-timeout reconciliation read (`wt litellm list`) itself
    # fails, the caller must still see a clear error rather than a
    # silently-wrong success/failure guess.
    from modelman import wt_bridge

    def fake_run(args, env=None, timeout=120):
        if args[0] == "expose":
            raise wt_bridge.WtBridgeTimeoutError("wt litellm expose timed out after 120s")
        raise AssertionError(f"unexpected _run call: {args}")

    def broken_routed_ids(litellm_path=None):
        raise wt_bridge.WtBridgeError("wt litellm list failed")

    monkeypatch.setattr(wt_bridge, "_run", fake_run)
    monkeypatch.setattr(wt_bridge, "routed_ids", broken_routed_ids)

    with pytest.raises(wt_bridge.WtBridgeError, match="timed out and its result is unknown"):
        wt_bridge.expose(["m1"])
