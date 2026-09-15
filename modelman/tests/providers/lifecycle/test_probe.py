from unittest.mock import MagicMock, patch

import pytest

from modelman.providers.lifecycle.envelope import LifecycleError
from modelman.providers.lifecycle.probe import (
    port_closed_within,
    serving_model,
    wait_for_model,
    wait_for_port_closed,
    wait_for_port_open,
    warmup,
)


def _mock_urlopen(body: bytes):
    """Return a MagicMock that behaves as a context manager yielding an object
    whose .read() returns `body`."""
    m = MagicMock()
    m.return_value.__enter__.return_value.read.return_value = body
    return m


def test_wait_for_port_closed_treats_http_error_as_still_open():
    """HTTPError (e.g. a 404/500 from the health path) means the server
    answered — the port is NOT closed. HTTPError is a URLError/OSError
    subclass, so a bare `except OSError` would misread it as "closed" and
    let a spawn proceed into a port that is still held by the prior
    process. This is the single most important exception-ordering
    invariant ported from today's lifecycle.py."""
    from urllib.error import HTTPError

    mock_urlopen = MagicMock(side_effect=HTTPError("http://x", 404, "not found", None, None))
    with (
        patch("modelman.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("modelman.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch("modelman.providers.lifecycle.probe.time.sleep"),
        pytest.raises(LifecycleError, match="still answering"),
    ):
        wait_for_port_closed("http://localhost:8003/v1/models", timeout=1.0)
    mock_urlopen.assert_called_once()


def test_wait_for_port_closed_returns_on_connection_refused():
    """A true connection-level failure (port actually closed) must return
    immediately rather than being misread as "still open"."""
    with patch(
        "modelman.providers.lifecycle.probe.urllib.request.urlopen",
        side_effect=ConnectionRefusedError(),
    ):
        wait_for_port_closed("http://localhost:8003/v1/models", timeout=1.0)  # must not raise


def test_wait_for_port_closed_read_timeout_means_still_open():
    """A read TimeoutError means the listener accepted the connection and
    then stalled (hung server / still draining) — the port is still held.
    Declaring it closed would let a caller spawn into the busy port.
    time.monotonic and time.sleep are mocked (rather than a short
    real-time budget) so this exercises exactly one bounded loop
    iteration instead of real-time busy-spinning."""
    mock_urlopen = MagicMock(side_effect=TimeoutError)
    with (
        patch("modelman.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("modelman.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch("modelman.providers.lifecycle.probe.time.sleep"),
        pytest.raises(LifecycleError, match="still answering"),
    ):
        wait_for_port_closed("http://localhost:8003/v1/models", timeout=1.0)
    mock_urlopen.assert_called_once()


def test_port_closed_within_returns_true_on_connection_refused():
    """port_closed_within is the non-raising sibling of wait_for_port_closed:
    a real connection-level failure must resolve to True without raising,
    so stop-and-wait callers can warn instead of failing outright."""
    with patch(
        "modelman.providers.lifecycle.probe.urllib.request.urlopen",
        side_effect=ConnectionRefusedError(),
    ):
        assert port_closed_within("http://localhost:8003/v1/models", timeout=1.0) is True


def test_port_closed_within_returns_false_on_timeout():
    """When the port never closes within the budget, port_closed_within
    must return False rather than raising — callers use this to warn, not
    fail, when a stop didn't fully release its port. time.monotonic and
    time.sleep are mocked so this exercises one bounded loop iteration
    instead of a real-time busy-spin."""
    mock_urlopen = MagicMock(side_effect=TimeoutError)
    with (
        patch("modelman.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("modelman.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch("modelman.providers.lifecycle.probe.time.sleep"),
    ):
        assert port_closed_within("http://localhost:8003/v1/models", timeout=1.0) is False
    mock_urlopen.assert_called_once()


def test_wait_for_port_open_returns_true_once_reachable():
    """wait_for_port_open is bash's poll_until_up mirror: it must return
    True (not raise) as soon as the port answers, even with an HTTP error
    status, since any response means something is listening."""
    with patch(
        "modelman.providers.lifecycle.probe.urllib.request.urlopen",
        _mock_urlopen(b"{}"),
    ):
        assert wait_for_port_open("http://localhost:8000/v1/models", timeout=1.0) is True


def test_wait_for_port_open_returns_false_after_timeout():
    """A port that never comes up must resolve to False, never raise —
    bash's poll_until_up never raises either, and callers depend on this
    to decide whether to proceed or warn. time.monotonic and time.sleep
    are mocked so this exercises one bounded loop iteration instead of a
    real-time busy-spin."""
    mock_urlopen = MagicMock(side_effect=ConnectionRefusedError())
    with (
        patch("modelman.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("modelman.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch("modelman.providers.lifecycle.probe.time.sleep"),
    ):
        assert wait_for_port_open("http://localhost:8000/v1/models", timeout=1.0) is False
    mock_urlopen.assert_called_once()


def test_wait_for_model_raises_promptly_when_serve_dies():
    """A serve process that dies mid-load (OOM kill, missing weights found
    late) must fail the wait immediately, not poll a dead port for the
    full deadline — the real crash cause would otherwise sit unread."""
    proc = MagicMock()
    proc.poll.return_value = 137  # SIGKILL'd (OOM) on first check
    proc.returncode = 137
    with (
        patch(
            "modelman.providers.lifecycle.probe.http_models_ids",
            side_effect=AssertionError("must not poll HTTP after process death"),
        ),
        pytest.raises(LifecycleError, match="exited during model load"),
    ):
        wait_for_model("http://localhost:8003/v1/models", "Org/Model", proc=proc, timeout=300.0)


def test_wait_for_model_matches_lenient_suffix():
    """A model id served under an org/ prefix must still match the bare
    model name a caller polls for (lenient suffix match), matching
    today's _wait_for_model behavior."""
    with patch(
        "modelman.providers.lifecycle.probe.http_models_ids",
        return_value=["Org/Model"],
    ):
        wait_for_model("http://localhost:8003/v1/models", "Model", timeout=300.0)  # must not raise


def test_serving_model_true_when_listed():
    """serving_model must return True when the resolved model (or an
    org/-prefixed spelling of it) is already listed — this is what lets a
    same-model re-isolate skip a full reload."""
    with patch(
        "modelman.providers.lifecycle.probe.http_models_ids",
        return_value=["Org/Model"],
    ):
        assert serving_model("http://localhost:8003/v1/models", "Model") is True


def test_serving_model_false_on_probe_error():
    """http_models_ids returns [] on any error; serving_model must read
    that as "not serving" — the safe fallback for a down or wedged
    server."""
    with patch(
        "modelman.providers.lifecycle.probe.http_models_ids",
        return_value=[],
    ):
        assert serving_model("http://localhost:8003/v1/models", "Model") is False


def test_warmup_matches_spaced_json():
    """A backend may serialize the response with spaces between keys and
    values; the warmup success check must match that form, not only
    compact JSON. The same mock answers both the health probe (whose body
    is never inspected) and the chat POST."""
    body = b'{"object": "chat.completion", "choices": []}'
    with patch("modelman.providers.lifecycle.probe.urllib.request.urlopen", _mock_urlopen(body)):
        # Should return without raising.
        warmup("http://x/chat", "org/repo", health_url="http://x/health")


def test_warmup_fails_when_no_chat_completion_marker():
    """A response that never contains a chat.completion marker must time
    out (here the deadline is made to expire after exactly one loop
    iteration by mocking time). The `side_effect` must leave room for one
    real iteration ([0.0, 0.5, 1000.0]: deadline=1.0, first check 0.5<1.0
    is True) — a deadline that expires before the first check (as in
    [0.0, 1.0, 1000.0], where 1.0<1.0 is False) would make this test pass
    vacuously without ever exercising the health-poll/marker-match code,
    which is exactly the bug this test used to have."""
    body = b'{"object":"list"}'
    mock_urlopen = _mock_urlopen(body)
    with (
        patch("modelman.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("modelman.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.5, 1000.0]),
        patch("modelman.providers.lifecycle.probe.time.sleep"),
        pytest.raises(LifecycleError, match="warm up"),
    ):
        warmup("http://x/chat", "org/repo", health_url="http://x/health", timeout=1.0)
    # Proves the loop body actually ran: one health-liveness call, one
    # chat POST, both against the same mock (which never contains the
    # chat.completion marker).
    assert mock_urlopen.call_count == 2


def test_warmup_retries_while_health_url_unreachable():
    """warmup must keep polling health_url (never attempt the chat POST)
    until it's reachable — an unreachable health endpoint must not be
    misread as a chat-completion failure. Same deadline-off-by-one
    hazard as above: the `side_effect` must allow at least one real loop
    iteration ([0.0, 0.5, 1000.0]), or this test would pass without
    urlopen ever being called."""
    health_error = MagicMock(side_effect=OSError("connection refused"))
    with (
        patch("modelman.providers.lifecycle.probe.urllib.request.urlopen", health_error),
        patch("modelman.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.5, 1000.0]),
        patch("modelman.providers.lifecycle.probe.time.sleep"),
        pytest.raises(LifecycleError, match="warm up"),
    ):
        warmup("http://x/chat", "org/repo", health_url="http://x/health", timeout=1.0)
    # Proves the health-poll branch actually ran, and that only the
    # health URL was ever polled (the chat POST must never be attempted
    # while health is unreachable).
    assert health_error.call_count >= 1
    for call in health_error.call_args_list:
        assert call.args[0] == "http://x/health"
