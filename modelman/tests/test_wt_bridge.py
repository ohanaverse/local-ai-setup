"""Tests for the wt subprocess bridge (wt owns LiteLLM management)."""

import json
import subprocess
from pathlib import Path

import pytest

from modelman import wt_bridge


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
    monkeypatch.undo()  # drop the autouse fake _run; exercise the real one
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda _: "/bin/wt")
    monkeypatch.setattr(wt_bridge.subprocess, "run", _boom(exc))
    with pytest.raises(wt_bridge.WtBridgeError) as ei:
        wt_bridge._run(["set", "--api-key", "SECRETKEY"])
    assert "SECRETKEY" not in str(ei.value)


def test_run_uses_short_timeout_and_utf8(monkeypatch):
    # The render-path providers call needs a short timeout, writes keep 120s,
    # and output is decoded as utf-8 regardless of locale.
    monkeypatch.undo()
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
    monkeypatch.undo()
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
