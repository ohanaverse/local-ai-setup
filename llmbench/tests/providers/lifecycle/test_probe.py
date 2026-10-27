import subprocess
from unittest.mock import MagicMock, patch

import pytest

from llmbench.providers.lifecycle.envelope import LifecycleError
from llmbench.providers.lifecycle.probe import (
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
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("llmbench.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
        pytest.raises(LifecycleError, match="still answering"),
    ):
        wait_for_port_closed("http://localhost:8003/v1/models", timeout=1.0)
    mock_urlopen.assert_called_once()


def test_wait_for_port_closed_returns_on_connection_refused():
    """A true connection-level failure (port actually closed) must return
    immediately rather than being misread as "still open"."""
    with patch(
        "llmbench.providers.lifecycle.probe.urllib.request.urlopen",
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
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("llmbench.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
        pytest.raises(LifecycleError, match="still answering"),
    ):
        wait_for_port_closed("http://localhost:8003/v1/models", timeout=1.0)
    mock_urlopen.assert_called_once()


def test_port_closed_within_returns_true_on_connection_refused():
    """port_closed_within is the non-raising sibling of wait_for_port_closed:
    a real connection-level failure must resolve to True without raising,
    so stop-and-wait callers can warn instead of failing outright."""
    with patch(
        "llmbench.providers.lifecycle.probe.urllib.request.urlopen",
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
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("llmbench.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
    ):
        assert port_closed_within("http://localhost:8003/v1/models", timeout=1.0) is False
    mock_urlopen.assert_called_once()


def test_wait_for_port_open_returns_true_once_reachable():
    """wait_for_port_open is bash's poll_until_up mirror: it must return
    True (not raise) as soon as the port answers, even with an HTTP error
    status, since any response means something is listening."""
    with patch(
        "llmbench.providers.lifecycle.probe.urllib.request.urlopen",
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
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("llmbench.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
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
            "llmbench.providers.lifecycle.probe.http_models_ids",
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
        "llmbench.providers.lifecycle.probe.http_models_ids",
        return_value=["Org/Model"],
    ):
        wait_for_model("http://localhost:8003/v1/models", "Model", timeout=300.0)  # must not raise


def test_serving_model_true_when_listed():
    """serving_model must return True when the resolved model (or an
    org/-prefixed spelling of it) is already listed — this is what lets a
    same-model re-isolate skip a full reload."""
    with patch(
        "llmbench.providers.lifecycle.probe.http_models_ids",
        return_value=["Org/Model"],
    ):
        assert serving_model("http://localhost:8003/v1/models", "Model") is True


def test_serving_model_false_on_probe_error():
    """http_models_ids returns [] on any error; serving_model must read
    that as "not serving" — the safe fallback for a down or wedged
    server."""
    with patch(
        "llmbench.providers.lifecycle.probe.http_models_ids",
        return_value=[],
    ):
        assert serving_model("http://localhost:8003/v1/models", "Model") is False


def test_warmup_matches_spaced_json():
    """A backend may serialize the response with spaces between keys and
    values; the warmup success check must match that form, not only
    compact JSON. The same mock answers both the health probe (whose body
    is never inspected) and the chat POST."""
    body = b'{"object": "chat.completion", "choices": []}'
    with patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", _mock_urlopen(body)):
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
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", mock_urlopen),
        patch("llmbench.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.5, 1000.0]),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
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
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", health_error),
        patch("llmbench.providers.lifecycle.probe.time.monotonic", side_effect=[0.0, 0.5, 1000.0]),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
        pytest.raises(LifecycleError, match="warm up"),
    ):
        warmup("http://x/chat", "org/repo", health_url="http://x/health", timeout=1.0)
    # Proves the health-poll branch actually ran, and that only the
    # health URL was ever polled (the chat POST must never be attempted
    # while health is unreachable).
    assert health_error.call_count >= 1
    for call in health_error.call_args_list:
        assert call.args[0] == "http://x/health"


def _http_error(code: int, body: bytes = b'{"error":{"message":"API key required"}}'):
    import io
    import urllib.error

    return urllib.error.HTTPError("http://x", code, "refused", {}, io.BytesIO(body))  # type: ignore[arg-type]


def test_warmup_stops_at_once_when_the_server_wants_a_key():
    """#256: an omlx with an API key answers 401 to the keyless liveness probe
    and to the keyless chat. The 401 on the probe is a server that is up, and
    the 401 on the chat is final — waiting does not change it — so warmup
    raises WarmupRefusedError on the first chat instead of polling out its
    whole (ten-minute) budget and reporting a timeout."""
    from llmbench.providers.lifecycle.probe import WarmupRefusedError

    refused = MagicMock(side_effect=lambda *a, **k: (_ for _ in ()).throw(_http_error(401)))
    with (
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", refused),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
        pytest.raises(WarmupRefusedError, match="API key required"),
    ):
        warmup("http://x/chat", "org/repo", health_url="http://x/health", timeout=600.0)
    # One liveness probe, one chat: no retry.
    assert refused.call_count == 2


def _chat_answering(*answers):
    """A urlopen whose liveness probe succeeds and whose chat gets the next HTTP error
    of `answers` ((status, body) pairs), then the last one for ever."""
    chats = {"n": 0}

    def urlopen(target, **kwargs):
        if isinstance(target, str):  # the liveness probe
            return MagicMock()
        status, body = answers[min(chats["n"], len(answers) - 1)]
        chats["n"] += 1
        raise _http_error(status, body)

    urlopen.chats = chats
    return urlopen


def test_warmup_stops_when_the_same_request_error_repeats():
    """A server that answers the warmup chat with the same HTTP 400, body and all, ten
    times running is not loading: omlx answered "Chat template error: unicode-escape" to
    every request for as long as the warmup polled (ten minutes), and the real message
    was only in the server's log. The warmup now stops after ten identical answers and
    says what the server said."""
    from llmbench.providers.lifecycle.probe import WarmupRefusedError, WarmupRejectedError

    urlopen = _chat_answering((400, b'{"error":{"message":"Chat template error: unicode-escape"}}'))
    with (
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", urlopen),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
        pytest.raises(
            WarmupRejectedError, match=r"10 times in a row \(HTTP 400: .*unicode-escape"
        ) as excinfo,
    ):
        warmup("http://x/chat", "org/repo", health_url="http://x/health", timeout=600.0)
    assert urlopen.chats["n"] == 10
    # Not the key refusal: omlx's backend answers that one by trying `wt warm`, which a 400 would not fix.
    assert not isinstance(excinfo.value, WarmupRefusedError)


def _warmup_for_attempts(urlopen, attempts):
    """Run `warmup` for exactly `attempts` chat attempts, then let its deadline pass."""
    with (
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", urlopen),
        patch(
            "llmbench.providers.lifecycle.probe.time.monotonic",
            side_effect=[0.0] + [1.0] * attempts + [1000.0],
        ),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
        pytest.raises(LifecycleError, match="failed to warm up") as excinfo,
    ):
        warmup("http://x/chat", "org/repo", health_url="http://x/health", timeout=600.0)
    return excinfo.value


def test_warmup_keeps_polling_when_a_503_intervenes():
    """The repeat count is the margin for a server that words "not ready" as a 400 for a
    few seconds: any other status in between (a 503 while loading) starts the count again,
    so nine 400s then a 503 then nine more never trip it."""
    from llmbench.providers.lifecycle.probe import WarmupRejectedError

    nine = [(400, b"loading weights")] * 9
    urlopen = _chat_answering(*nine, (503, b"busy"), *nine)
    error = _warmup_for_attempts(urlopen, 19)
    assert not isinstance(error, WarmupRejectedError)
    assert urlopen.chats["n"] == 19


def test_warmup_keeps_polling_when_the_400_body_changes():
    """Ten 400s only count when they are the same 400: bodies that differ attempt to attempt
    (a server still working through its start) never reach the repeat count."""
    from llmbench.providers.lifecycle.probe import WarmupRejectedError

    urlopen = _chat_answering(*([(400, b"a"), (400, b"b")] * 10))
    error = _warmup_for_attempts(urlopen, 20)
    assert not isinstance(error, WarmupRejectedError)
    assert urlopen.chats["n"] == 20


def test_warmup_still_retries_other_http_errors():
    """Only 401/403 are final. A 404 or 503 while a model loads is the
    ordinary "not yet", retried until the deadline as before."""
    calls = {"n": 0}

    def urlopen(target, **kwargs):
        calls["n"] += 1
        if isinstance(target, str):  # the liveness probe
            return MagicMock()
        raise _http_error(503, b"loading")

    with (
        patch("llmbench.providers.lifecycle.probe.urllib.request.urlopen", urlopen),
        patch(
            "llmbench.providers.lifecycle.probe.time.monotonic",
            side_effect=[0.0, 0.2, 0.4, 1000.0],
        ),
        patch("llmbench.providers.lifecycle.probe.time.sleep"),
        pytest.raises(LifecycleError, match="warm up") as excinfo,
    ):
        warmup("http://x/chat", "org/repo", health_url="http://x/health", timeout=1.0)
    assert calls["n"] == 4  # two iterations, probe + chat each
    from llmbench.providers.lifecycle.probe import WarmupRefusedError

    assert not isinstance(excinfo.value, WarmupRefusedError)


# --- keyed probes (rec needs a bearer on every route) -----------------------
#
# These run against the real stdlib fake endpoint from tests/probe/conftest.py,
# so the header assertions are on what actually crossed the wire. The autouse
# guard patches `urllib.request.urlopen` to raise; the fixture puts a real
# opener back for the test's duration (no proxy handler: 127.0.0.1 only).

PROBE = "llmbench.providers.lifecycle.probe"


@pytest.fixture
def endpoint(monkeypatch):
    import urllib.request

    from tests.probe.conftest import serve_fake

    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    monkeypatch.setattr(f"{PROBE}.urllib.request.urlopen", opener.open)
    with serve_fake() as fake:
        yield fake


def _probe_everything(base: str, key: str | None) -> None:
    """Call each keyed probe once against a live endpoint serving `flint`."""
    with (
        patch(f"{PROBE}.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch(f"{PROBE}.time.sleep"),
    ):
        assert port_closed_within(f"{base}/models", timeout=1.0, key=key) is False
    assert wait_for_port_open(f"{base}/models", timeout=5.0, key=key) is True
    wait_for_model(f"{base}/models", "flint", timeout=5.0, key=key)
    assert serving_model(f"{base}/models", "flint", key=key) is True
    warmup(f"{base}/chat/completions", "no-reasoning", health_url=f"{base}/models", key=key)


def test_probes_without_a_key_send_no_authorization_header(endpoint):
    """A provider with no secret_ref (ollama, omlx, mtplx, mlx_lm_server)
    must send no Authorization header on any probe: byte-identical to the
    probes before keys existed."""
    _probe_everything(endpoint.base_url, key=None)
    assert len(endpoint.requests) == 6  # port x2, models x2, health + chat
    carried = [r["path"] for r in endpoint.requests if "Authorization" in r["headers"]]
    assert carried == []


def test_probes_with_a_key_send_the_bearer_on_every_request(endpoint):
    """rec answers 401 on every route, health and /v1/models included, unless
    the request carries `Authorization: Bearer <key>`: every probe sends it."""
    endpoint.required_key = "k"
    _probe_everything(endpoint.base_url, key="k")
    assert len(endpoint.requests) == 6
    bearer_ok = [r["headers"].get("Authorization") == "Bearer k" for r in endpoint.requests]
    assert all(bearer_ok)


def test_wait_for_model_stops_at_once_when_the_key_is_refused(endpoint):
    """A 401 to a request that carried a key is final: polling does not
    change it, so wait_for_model raises WarmupRefusedError on the first
    answer instead of polling out its timeout. The key never appears in
    the message."""
    from llmbench.providers.lifecycle.probe import WarmupRefusedError

    endpoint.required_key = "right-key"
    with (
        patch(f"{PROBE}.time.sleep", side_effect=AssertionError("must not poll again")),
        pytest.raises(WarmupRefusedError, match="HTTP 401") as excinfo,
    ):
        wait_for_model(f"{endpoint.base_url}/models", "flint", timeout=300.0, key="wrong-key")
    assert len(endpoint.requests) == 1
    leaked = "wrong-key" in str(excinfo.value) or "right-key" in str(excinfo.value)
    assert not leaked


def test_wait_for_model_without_a_key_still_polls_a_401(endpoint):
    """Without a key a 401 stays the ordinary "not serving yet" it always
    was: the wait polls to its deadline and reports a timeout, not a
    refusal (unchanged behavior for key-less providers)."""
    from llmbench.providers.lifecycle.probe import WarmupRefusedError

    endpoint.required_key = "k"
    with (
        patch(f"{PROBE}.time.monotonic", side_effect=[0.0, 0.0, 0.5, 1000.0]),
        patch(f"{PROBE}.time.sleep"),
        pytest.raises(LifecycleError, match="timed out") as excinfo,
    ):
        wait_for_model(f"{endpoint.base_url}/models", "flint", timeout=1.0)
    assert not isinstance(excinfo.value, WarmupRefusedError)
    assert len(endpoint.requests) == 2


def test_serving_model_with_a_refused_key_reads_as_not_serving(endpoint):
    """serving_model never raises: a refused key is "not serving", the same
    safe fallback as a down server."""
    endpoint.required_key = "right-key"
    assert serving_model(f"{endpoint.base_url}/models", "flint", key="wrong-key") is False


def test_warmup_with_a_refused_key_raises_without_the_key(endpoint):
    """warmup with a key the server refuses: the 401 on the liveness URL is
    a server that is up, the 401 on the chat is final. The key appears
    nowhere in the message."""
    from llmbench.providers.lifecycle.probe import WarmupRefusedError

    endpoint.required_key = "right-key"
    with (
        patch(f"{PROBE}.time.sleep"),
        pytest.raises(WarmupRefusedError, match="HTTP 401") as excinfo,
    ):
        warmup(
            f"{endpoint.base_url}/chat/completions",
            "no-reasoning",
            health_url=f"{endpoint.base_url}/health",
            timeout=600.0,
            key="wrong-key",
        )
    assert len(endpoint.requests) == 2
    sent = [r["headers"].get("Authorization") == "Bearer wrong-key" for r in endpoint.requests]
    assert all(sent)
    assert "wrong-key" not in str(excinfo.value)


def test_warmup_scrubs_a_key_the_server_echoes_back():
    """A server that echoes the request's Authorization header into its
    refusal body must not get the key into the WarmupRefusedError text."""
    from llmbench.providers.lifecycle.probe import WarmupRefusedError

    def urlopen(target, **kwargs):
        if target.get_method() == "GET":
            return MagicMock()
        raise _http_error(401, b"bad header Bearer sekrit-value")

    with (
        patch(f"{PROBE}.urllib.request.urlopen", urlopen),
        pytest.raises(WarmupRefusedError) as excinfo,
    ):
        warmup("http://x/chat", "m", health_url="http://x/health", key="sekrit-value")
    assert "sekrit-value" not in str(excinfo.value)


def test_port_probes_with_a_key_count_a_refusal_as_a_listener(endpoint):
    """A 401 is still an HTTP answer: something listens on the port."""
    endpoint.required_key = "right-key"
    url = f"{endpoint.base_url}/models"
    assert wait_for_port_open(url, timeout=5.0, key="wrong-key") is True
    with (
        patch(f"{PROBE}.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch(f"{PROBE}.time.sleep"),
    ):
        assert port_closed_within(url, timeout=1.0, key="wrong-key") is False


# --- command-JSON readiness (`rec status`) ----------------------------------


def _completed(stdout: str, returncode: int = 0) -> subprocess.CompletedProcess[str]:
    return subprocess.CompletedProcess(["rec", "status"], returncode, stdout=stdout, stderr="")


@pytest.mark.parametrize(
    ("stdout", "returncode", "path", "expected"),
    [
        ('{"ready":true,"model":"flint"}', 0, "ready", True),
        ('{"ready":false}', 0, "ready", False),
        ('{"ready":true,"model":"flint"}', 0, "model", "flint"),
        ('{"a":{"b":7}}', 0, "a.b", 7),
        ('{"a":{"b":"x"}}\n', 0, "a.b", "x"),
        ('{"ready":true}', 1, "ready", None),  # exit non-zero
        ("", 1, "ready", None),  # rec status with nothing running
        ("", 0, "ready", None),  # empty output
        ("not json", 0, "ready", None),
        ("{}", 0, "ready", None),  # missing path
        ("[1, 2]", 0, "ready", None),  # non-object root
        ('{"a":1}', 0, "a.b", None),  # path through a scalar
        ('{"a":{"b":1}}', 0, "a", None),  # not a scalar
    ],
)
def test_command_json_reads_one_scalar_or_none(stdout, returncode, path, expected):
    """command_json returns the scalar at the dot path, and None for every
    way `rec status` can fail to say: exit non-zero, empty, invalid JSON, a
    non-object root, a missing path. It never raises."""
    from llmbench.providers.lifecycle.probe import command_json

    with patch(f"{PROBE}.subprocess.run", return_value=_completed(stdout, returncode)) as run:
        got = command_json(["rec", "status"], path=path)
    assert got == expected
    assert type(got) is type(expected)
    assert run.call_args.args[0] == ["rec", "status"]
    assert run.call_args.kwargs["timeout"] == 5.0


@pytest.mark.parametrize(
    "error",
    [
        subprocess.TimeoutExpired(["rec", "status"], 5.0),
        FileNotFoundError(2, "No such file or directory", "rec"),
        PermissionError(13, "Permission denied", "rec"),
    ],
)
def test_command_json_is_none_when_the_command_cannot_run(error):
    """A hung command (timeout) or a missing/unrunnable executable is "no
    answer", never an exception."""
    from llmbench.providers.lifecycle.probe import command_json

    with patch(f"{PROBE}.subprocess.run", side_effect=error):
        assert command_json(["rec", "status"], path="ready", timeout=0.5) is None


@pytest.mark.parametrize(
    ("stdout", "returncode", "expected"),
    [
        ('{"ready":false}', 0, ("answered", False)),
        ('{"ready":true}', 0, ("answered", True)),
        ("", 1, ("gone", None)),  # rec: no managed rec instance is reachable
        ('{"ready":true}', 1, ("gone", None)),  # any non-zero exit
        ('{"ready":null}', 0, ("unknown", None)),
        ("{}", 0, ("unknown", None)),  # missing path
        ("", 0, ("unknown", None)),  # empty output
        ("not json", 0, ("unknown", None)),
        ("[1]", 0, ("unknown", None)),
        ('{"ready":{"x":1}}', 0, ("unknown", None)),  # not a scalar
    ],
)
def test_command_status_tells_gone_from_answered_from_unknown(stdout, returncode, expected):
    """For "is the instance gone": only a failing status command (non-zero
    exit) says gone; a value says it answered; a zero exit with nothing
    readable at the path is unknown — never gone."""
    from llmbench.providers.lifecycle.probe import command_status

    with patch(f"{PROBE}.subprocess.run", return_value=_completed(stdout, returncode)) as run:
        assert command_status(["rec", "status"], path="ready") == expected
    assert run.call_args.kwargs["timeout"] == 5.0


@pytest.mark.parametrize(
    ("error", "expected"),
    [
        (subprocess.TimeoutExpired(["rec", "status"], 5.0), ("unknown", None)),
        (FileNotFoundError(2, "No such file or directory", "rec"), ("gone", None)),
        (PermissionError(13, "Permission denied", "rec"), ("gone", None)),
    ],
)
def test_command_status_timeout_is_unknown_and_a_missing_binary_gone(error, expected):
    """A hung status command says nothing about the instance (a slow `rec
    status` under a multi-GB teardown is exactly the case); one that cannot
    run at all has no instance to report."""
    from llmbench.providers.lifecycle.probe import command_status

    with patch(f"{PROBE}.subprocess.run", side_effect=error):
        assert command_status(["rec", "status"], path="ready", timeout=0.5) == expected


def test_wait_for_command_ready_polls_until_ready():
    """Not ready twice (still loading, then nothing answering), then ready:
    returns after three polls, one second apart."""
    from llmbench.providers.lifecycle.probe import wait_for_command_ready

    replies = [
        _completed('{"ready":false,"model":"flint"}'),
        _completed("", returncode=1),
        _completed('{"ready":true,"model":"flint"}'),
    ]
    proc = MagicMock()
    proc.poll.return_value = None
    with (
        patch(f"{PROBE}.subprocess.run", side_effect=replies) as run,
        patch(f"{PROBE}.time.sleep") as sleep,
    ):
        wait_for_command_ready(
            ["rec", "status"], path="ready", equals="true", proc=proc, timeout=60.0
        )
    assert run.call_count == 3
    assert [c.args for c in sleep.call_args_list] == [(1.0,), (1.0,)]


def test_wait_for_command_ready_renders_numbers_as_text():
    """equals compares the scalar rendered as text: a number via str()."""
    from llmbench.providers.lifecycle.probe import wait_for_command_ready

    with patch(f"{PROBE}.subprocess.run", return_value=_completed('{"n":3}')):
        wait_for_command_ready(["rec", "status"], path="n", equals="3", timeout=5.0)


def test_wait_for_command_ready_fails_fast_when_the_process_dies():
    """The serve process exiting mid-load ends the wait at once: no status
    command is run against a process that is gone."""
    from llmbench.providers.lifecycle.probe import wait_for_command_ready

    proc = MagicMock()
    proc.poll.return_value = 1
    proc.returncode = 1
    with (
        patch(f"{PROBE}.subprocess.run", side_effect=AssertionError("must not poll")),
        pytest.raises(LifecycleError, match=r"serve process exited during model load: exit 1"),
    ):
        wait_for_command_ready(
            ["rec", "status"], path="ready", equals="true", proc=proc, timeout=300.0
        )


def test_wait_for_command_ready_times_out_naming_the_binary():
    """A status that never reports ready times out with a message naming the
    command's binary (the basename, not a resolved path)."""
    from llmbench.providers.lifecycle.probe import wait_for_command_ready

    with (
        patch(f"{PROBE}.subprocess.run", return_value=_completed('{"ready":false}')) as run,
        patch(f"{PROBE}.time.monotonic", side_effect=[0.0, 0.5, 1000.0]),
        patch(f"{PROBE}.time.sleep"),
        pytest.raises(LifecycleError, match=r"timed out waiting for rec ") as excinfo,
    ):
        wait_for_command_ready(
            ["/Users/x/.local/bin/rec", "status"], path="ready", equals="true", timeout=1.0
        )
    assert run.call_count == 1
    assert "/Users/x" not in str(excinfo.value)


# --- command_json never raises (fix round 1) --------------------------------
#
# Real child processes (the test interpreter itself, which the autouse guard
# lets through), so the decoding and parsing run exactly as in production.


def _py(code: str) -> list[str]:
    import sys

    return [sys.executable, "-c", code]


def test_command_json_ignores_non_utf8_stderr_noise():
    """A status command that prints undecodable progress/ANSI bytes to
    stderr while printing a valid answer on stdout: the answer still
    parses."""
    from llmbench.providers.lifecycle.probe import command_json

    argv = _py(
        "import sys; sys.stderr.buffer.write(b'\\xff\\xfe\\x1b[0m'); "
        "sys.stdout.write('{\"ready\": true}')"
    )
    assert command_json(argv, path="ready") is True


def test_command_json_survives_non_utf8_stdout():
    """Undecodable bytes on stdout are replaced, not raised: inside a string
    value the rest of the answer still reads."""
    from llmbench.providers.lifecycle.probe import command_json

    argv = _py('import sys; sys.stdout.buffer.write(b\'{"ready": true, "m": "\\xff"}\')')
    assert command_json(argv, path="ready") is True
    garbage = _py("import sys; sys.stdout.buffer.write(b'\\xff\\xfe\\xfd')")
    assert command_json(garbage, path="ready") is None


def test_command_json_is_none_for_deeply_nested_json():
    """json.loads raises RecursionError (not ValueError) on deep nesting."""
    from llmbench.providers.lifecycle.probe import command_json

    argv = _py("import sys; sys.stdout.write('{\"a\":' * 100000 + '1' + '}' * 100000)")
    assert command_json(argv, path="a") is None


@pytest.mark.parametrize(
    "argv",
    [[], ["/bin/echo", "nul\0byte"]],
    ids=["empty-argv", "nul-in-argv"],
)
def test_command_json_is_none_for_an_argv_that_cannot_run(argv):
    """An empty argv (IndexError) or a NUL byte in an argument (ValueError)
    is "no answer", like a missing executable."""
    from llmbench.providers.lifecycle.probe import command_json

    assert command_json(argv, path="ready") is None


def test_warmup_masks_an_echoed_key_that_straddles_the_detail_cut():
    """The refusal detail is cut to 300 bytes. A key echoed across that cut
    must not leave its leading part in the message: mask first, then cut."""
    from llmbench.providers.lifecycle.probe import WarmupRefusedError

    key = "sekrit-value-0123456789"
    # 286 + len(" Bearer ") = 294: the 300-byte cut falls 6 bytes into the key.
    body = b"x" * 286 + b" Bearer " + key.encode() + b" trailing"

    def urlopen(target, **kwargs):
        if target.get_method() == "GET":
            return MagicMock()
        raise _http_error(401, body)

    with (
        patch(f"{PROBE}.urllib.request.urlopen", urlopen),
        pytest.raises(WarmupRefusedError) as excinfo,
    ):
        warmup("http://x/chat", "m", health_url="http://x/health", key=key)
    message = str(excinfo.value)
    leaked = any(key[:n] in message for n in range(3, len(key) + 1))
    assert not leaked


def test_command_object_is_one_run_read_many_ways():
    from llmbench.providers.lifecycle.probe import command_object, scalar_at

    out = subprocess.CompletedProcess([], 0, stdout='{"ready": true, "a": {"b": "x"}}')
    with patch("llmbench.providers.lifecycle.probe.subprocess.run", return_value=out) as run:
        outcome, root = command_object(["rec", "status"])
    run.assert_called_once()
    assert outcome == "answered"
    assert root is not None
    assert scalar_at(root, "ready") is True
    assert scalar_at(root, "a.b") == "x"
    assert scalar_at(root, "a") is None
    assert scalar_at(root, "missing.path") is None
