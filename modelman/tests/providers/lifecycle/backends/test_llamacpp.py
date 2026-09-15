from unittest.mock import patch

from modelman.providers.lifecycle.backends import BACKENDS, SUPPORTED_PROVIDER_IDS
from modelman.providers.lifecycle.backends.llamacpp import (
    DEFAULT_MODEL,
    ENV_VAR,
    LLAMACPP,
    LLAMACPP_CHAT_URL,
    LLAMACPP_HEALTH_URL,
)

MODULE = "modelman.providers.lifecycle.backends.llamacpp"


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
        mock_plist.__str__ = lambda self: "/Users/x/Library/LaunchAgents/local.llamacpp.server.plist"
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


def test_start_loads_plist_then_warms():
    """bash: `launchctl load -w <plist>` then warmup_or_die. Warming is what
    proves the server actually came up, so it must not be skipped."""
    plan = LLAMACPP.resolve("local-llama", ())
    with (
        patch(f"{MODULE}.launchd.load") as mock_load,
        patch(f"{MODULE}.launchd.LLAMACPP_PLIST", "/plist/path"),
        patch.object(type(LLAMACPP), "warm") as mock_warm,
    ):
        LLAMACPP.start(plan)
    mock_load.assert_called_once_with("/plist/path")
    mock_warm.assert_called_once_with(plan)


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
