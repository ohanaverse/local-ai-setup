from unittest.mock import patch

import pytest

from modelman.providers.lifecycle import launchd
from modelman.providers.lifecycle.backends import BACKENDS, SUPPORTED_PROVIDER_IDS
from modelman.providers.lifecycle.backends.llamacpp import (
    DEFAULT_MODEL,
    ENV_VAR,
    LLAMACPP,
    LLAMACPP_CHAT_URL,
    LLAMACPP_HEALTH_URL,
    LlamaCppBackend,
)
from modelman.providers.lifecycle.envelope import LifecycleError

MODULE = "modelman.providers.lifecycle.backends.llamacpp"
BASE_MODULE = "modelman.providers.lifecycle.backends.base"


def test_llamacpp_registered_in_backends_but_not_supported_ids():
    """llamacpp is retired (issue #33) yet its bash start branch still works
    if invoked directly, so it must be a live BACKENDS entry (isolate/
    stop-all can reach it) while staying out of SUPPORTED_PROVIDER_IDS
    (orchestrate.stop() and modelman start must reject it, matching bash's
    `stop` verb, which has no llamacpp case)."""
    assert BACKENDS["llamacpp"] is LLAMACPP
    assert "llamacpp" not in SUPPORTED_PROVIDER_IDS


def test_llamacpp_does_not_respect_solo():
    """THE one backend whose bash start branch ignores --solo: every other
    case arm guards teardown with `[ -n "$SOLO" ] ||`, while `llamacpp)`
    runs `stop_all_local llamacpp` unconditionally. orchestrate.isolate()
    reads this flag to reproduce that, so a silent flip to True would make a
    solo llamacpp start stop nothing at all."""
    assert LLAMACPP.respects_solo is False


def test_llamacpp_is_never_restored():
    """bin/llm-restore-providers deliberately does not restart llamacpp
    (retired 2026-09-07), so restore() must skip it rather than resurrect a
    retired provider after every benchmark run."""
    assert LLAMACPP.restore_action == "skip"


# --- check_available() -------------------------------------------------


def test_check_available_returns_message_when_plist_missing():
    """The START path fails loudly on a missing plist, with bash's own
    message shape — llama.cpp is LaunchAgent-managed, so the plist (not a
    PATH binary) is what "installed" means here."""
    with patch(f"{MODULE}.launchd.LLAMACPP_PLIST") as mock_plist:
        mock_plist.exists.return_value = False
        mock_plist.__str__ = lambda self: (
            "/Users/x/Library/LaunchAgents/local.llamacpp.server.plist"
        )
        reason = LLAMACPP.check_available()
    assert reason == (
        "llamacpp service not configured: "
        "/Users/x/Library/LaunchAgents/local.llamacpp.server.plist missing"
    )


def test_check_available_returns_none_when_plist_present():
    with patch(f"{MODULE}.launchd.LLAMACPP_PLIST") as mock_plist:
        mock_plist.exists.return_value = True
        assert LLAMACPP.check_available() is None


# --- resolve() ---------------------------------------------------------


def test_resolve_falls_back_to_default_model():
    """With no explicit model and no env var, the plan must carry bash's
    baked-in LLAMACPP_MODEL default ("local-llama") — the name warmup posts
    to the server."""
    with patch.dict("os.environ", {}, clear=True):
        plan = LLAMACPP.resolve(None, ())
    assert plan.model == DEFAULT_MODEL
    assert plan.direct_url == LLAMACPP_CHAT_URL


def test_resolve_prefers_explicit_model_then_env_var(monkeypatch):
    """Precedence must be explicit > env var > default, matching bash's
    `${LLM_ISOLATE_LLAMACPP_MODEL:-local-llama}` plus the caller's own
    override."""
    monkeypatch.setenv(ENV_VAR, "from-env")
    assert LLAMACPP.resolve(None, ()).model == "from-env"
    assert LLAMACPP.resolve("explicit", ()).model == "explicit"


# --- start() -----------------------------------------------------------


def test_start_loads_plist():
    """bash: `launchctl load -w <plist>` then warmup_or_die."""
    plan = LLAMACPP.resolve("local-llama", ())
    with (
        patch(f"{MODULE}.launchd.load") as mock_load,
        patch(f"{MODULE}.launchd.LLAMACPP_PLIST", "/plist/path"),
    ):
        LLAMACPP.start(plan)
    mock_load.assert_called_once_with("/plist/path")


def test_start_does_not_warm():
    """warm() is orchestrate.isolate()'s job (called once after
    start()+wait_ready() for every backend) — start() calling it too would
    warm the model twice per isolate(). Regression test for that bug."""
    plan = LLAMACPP.resolve("local-llama", ())
    with (
        patch(f"{MODULE}.launchd.load"),
        patch(f"{MODULE}.launchd.LLAMACPP_PLIST", "/plist/path"),
        patch.object(type(LLAMACPP), "warm") as mock_warm,
    ):
        LLAMACPP.start(plan)
    mock_warm.assert_not_called()


# --- stop_and_wait() ---------------------------------------------------


def test_stop_and_wait_is_a_silent_noop_when_plist_missing():
    """bash's stop_all_local guards its llamacpp branch with `[ -f <plist> ]`
    and therefore neither unloads nor warns when the service was never
    configured. This is deliberately the INVERSE of check_available()'s loud
    START-path failure: a machine that never had llama.cpp must not have
    every stop-all print a warning about it."""
    with (
        patch(f"{MODULE}.launchd.LLAMACPP_PLIST") as mock_plist,
        patch(f"{MODULE}.launchd.unload") as mock_unload,
        patch(f"{MODULE}.probe.port_closed_within") as mock_poll,
    ):
        mock_plist.exists.return_value = False
        assert LLAMACPP.stop_and_wait() is None
    mock_unload.assert_not_called()
    mock_poll.assert_not_called()


def test_stop_and_wait_unloads_and_polls_when_plist_present():
    with (
        patch(f"{MODULE}.launchd.LLAMACPP_PLIST") as mock_plist,
        patch(f"{MODULE}.launchd.unload") as mock_unload,
        patch(f"{MODULE}.probe.port_closed_within", return_value=True) as mock_poll,
    ):
        mock_plist.exists.return_value = True
        assert LLAMACPP.stop_and_wait() is None
    mock_unload.assert_called_once_with(mock_plist)
    assert mock_poll.call_args.args[0] == LLAMACPP_HEALTH_URL


def test_stop_and_wait_warns_when_port_stays_open():
    """A port that never closes must come back as bash's exact warning text.

    llamacpp is reached only through stop-all/_stop_others (orchestrate.stop()
    rejects it — it is outside SUPPORTED_PROVIDER_IDS), and those log the
    string to stderr rather than failing. So the wording is what a user sees
    when a retired llama.cpp refuses to let go of port 8080 during an
    isolate — worth keeping byte-identical to bash's."""
    with (
        patch(f"{MODULE}.launchd.LLAMACPP_PLIST") as mock_plist,
        patch(f"{MODULE}.launchd.unload"),
        patch(f"{MODULE}.probe.port_closed_within", return_value=False),
    ):
        mock_plist.exists.return_value = True
        assert LLAMACPP.stop_and_wait() == "llama.cpp still listening on port 8080"


# --- restore() ---------------------------------------------------------
#
# restore_action is "skip" today, so orchestrate.restore() never calls
# these paths on the current retired configuration. They are tested anyway
# because docs/reference/provider-artifacts.md documents re-enabling
# llama.cpp as a ONE-FIELD flip of restore_action to "restart": without a
# working override, that flip would silently land on Backend.restore()'s
# inherited no-op and report success while llama.cpp stayed down.


def _restartable() -> LlamaCppBackend:
    """A llamacpp backend with restore_action already flipped to
    "restart" — exactly what the re-enable runbook's step 7 produces."""
    backend = LlamaCppBackend()
    backend.restore_action = "restart"
    return backend


def test_restore_no_ops_while_restore_action_is_skip():
    """The shipped default must stay inert: a retired provider must not be
    resurrected by every post-benchmark restore."""
    with (
        patch(f"{BASE_MODULE}.urllib.request.urlopen") as mock_urlopen,
        patch(f"{MODULE}.launchd.load") as mock_load,
        patch(f"{BASE_MODULE}.wait_for_port_open") as mock_wait,
    ):
        LLAMACPP.restore()
    mock_urlopen.assert_not_called()
    mock_load.assert_not_called()
    mock_wait.assert_not_called()


def test_restore_no_ops_when_already_up():
    """Mirrors OmlxBackend.restore(): one 2s health probe first, and a
    server that already answers is left completely alone rather than being
    bounced."""
    with (
        patch(f"{BASE_MODULE}.urllib.request.urlopen") as mock_urlopen,
        patch(f"{MODULE}.launchd.load") as mock_load,
        patch(f"{BASE_MODULE}.wait_for_port_open") as mock_wait,
    ):
        mock_urlopen.return_value.__enter__ = lambda self: self
        mock_urlopen.return_value.__exit__ = lambda *a: None
        _restartable().restore()
    assert mock_urlopen.call_args.args[0] == LLAMACPP_HEALTH_URL
    mock_load.assert_not_called()
    mock_wait.assert_not_called()


def test_restore_loads_plist_and_waits_when_down():
    """The behavior the re-enable runbook promises: a down llama.cpp is
    actually brought back by loading its LaunchAgent and waiting for the
    port to answer."""
    with (
        patch(f"{BASE_MODULE}.urllib.request.urlopen", side_effect=OSError("refused")),
        patch(f"{MODULE}.launchd.load") as mock_load,
        patch(f"{BASE_MODULE}.wait_for_port_open", return_value=True) as mock_wait,
    ):
        _restartable().restore()
    mock_load.assert_called_once_with(launchd.LLAMACPP_PLIST)
    assert mock_wait.call_args.args[0] == LLAMACPP_HEALTH_URL


def test_restore_raises_lifecycle_error_when_it_never_comes_back():
    """A restore that loaded the plist but never saw the port open must
    RAISE, so orchestrate.restore() counts it as a failed restore instead
    of reporting a success nobody can act on."""
    with (
        patch(f"{BASE_MODULE}.urllib.request.urlopen", side_effect=OSError("refused")),
        patch(f"{MODULE}.launchd.load"),
        patch(f"{BASE_MODULE}.wait_for_port_open", return_value=False),
        pytest.raises(LifecycleError, match="llamacpp did not come back up"),
    ):
        _restartable().restore()
