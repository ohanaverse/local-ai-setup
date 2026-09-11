import json
from unittest.mock import MagicMock, mock_open, patch

import pytest

from modelman.providers.lifecycle import (
    LifecycleResult,
    _start_mtplx_serve,
    isolate,
    stop,
    stop_all,
)


def _mtplx_registry():
    from modelman.registry import ModelEntry, Registry

    return Registry(
        providers=[],
        models=[
            ModelEntry(
                id="mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
                family="qwen3.8",
                provider_id="mtplx",
                model_name="Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
            )
        ],
    )


def test_isolate_mtplx_starts_serve_and_warmup():
    with (
        patch("modelman.providers.lifecycle._stop_others") as mock_stop,
        # _start_mtplx_serve now returns the Popen handle isolate() must
        # forward into _wait_for_model — configure the mock's return value
        # explicitly so this flow stays visible even though Mock()'s
        # implicit MagicMock return would already satisfy it.
        patch("modelman.providers.lifecycle._start_mtplx_serve", return_value=MagicMock()) as mock_start,
        patch("modelman.providers.lifecycle._wait_for_model") as mock_wait,
        patch("modelman.providers.lifecycle._warmup") as mock_warmup,
        patch("modelman.providers.lifecycle.load_registry", return_value=_mtplx_registry()),
    ):
        result = isolate("mtplx")
    mock_stop.assert_called_once()
    mock_start.assert_called_once_with("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")
    mock_wait.assert_called_once_with("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", mock_start.return_value)
    mock_warmup.assert_called_once()
    assert result.ok is True
    assert result.provider == "mtplx"
    assert result.direct_url == "http://localhost:8003/v1/chat/completions"


def test_isolate_mtplx_uses_explicit_model():
    with (
        patch("modelman.providers.lifecycle._stop_others"),
        patch("modelman.providers.lifecycle._start_mtplx_serve") as mock_start,
        patch("modelman.providers.lifecycle._wait_for_model"),
        patch("modelman.providers.lifecycle._warmup"),
    ):
        isolate("mtplx", "Some/Explicit-Model")
    mock_start.assert_called_once_with("Some/Explicit-Model")


def test_isolate_mtplx_no_model_in_registry_returns_error():
    from modelman.registry import Registry

    with (
        patch("modelman.providers.lifecycle._stop_others"),
        patch("modelman.providers.lifecycle.load_registry", return_value=Registry()),
    ):
        result = isolate("mtplx")
    assert result.ok is False
    assert "no mtplx model" in (result.error or "")


def test_start_mtplx_serve_pins_model_id():
    # Live smoke test (2026-09-10) found that mtplx serve, without
    # --model-id, reports a slug it derives from the artifact (e.g.
    # "mtplx-qwen38-27b-optimized-quality") in /v1/models — never the
    # org/model repo id _wait_for_model polls for — so isolation timed
    # out even though the server was healthy. Pinning --model-id to the
    # resolved model makes /v1/models report exactly what's expected.
    with (
        patch("modelman.providers.lifecycle.shutil.which", return_value="/usr/local/bin/mtplx"),
        patch("modelman.providers.lifecycle._wait_for_port_closed"),
        patch("modelman.providers.lifecycle.subprocess.Popen") as mock_popen,
        patch("builtins.open", mock_open()),
        patch("modelman.providers.lifecycle.time.sleep"),
    ):
        mock_popen.return_value = MagicMock(pid=1234, poll=MagicMock(return_value=None))
        _start_mtplx_serve("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")
    args = mock_popen.call_args.args[0]
    assert "--model-id" in args
    assert args[args.index("--model-id") + 1] == "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"


def test_start_mtplx_serve_does_not_stop_mtplx_again():
    """isolate() already stops mtplx (via _stop_others()'s stop-all) before
    calling _start_mtplx_serve(). A second _stop_mtplx() call here just pays
    an extra 10s-grace subprocess + port-poll for no behavioral benefit."""
    with (
        patch("modelman.providers.lifecycle.shutil.which", return_value="/usr/local/bin/mtplx"),
        patch("modelman.providers.lifecycle._stop_mtplx") as mock_stop,
        patch("modelman.providers.lifecycle._wait_for_port_closed"),
        patch("modelman.providers.lifecycle.subprocess.Popen") as mock_popen,
        patch("builtins.open", mock_open()),
        patch("modelman.providers.lifecycle.time.sleep"),
    ):
        mock_popen.return_value = MagicMock(pid=1234, poll=MagicMock(return_value=None))
        _start_mtplx_serve("org/repo")
    mock_stop.assert_not_called()


def test_stop_mtplx_runs_mtplx_stop():
    """subprocess.run must be invoked with the shutil.which-resolved binary
    path, not a bare "mtplx" literal — matching _start_mtplx_serve's own
    resolve-once-then-reuse pattern. Mocking shutil.which also makes this
    test hermetic: it previously depended on a real `mtplx` being on the
    ambient PATH to reach the (correctly, but accidentally) same result."""
    with (
        patch("modelman.providers.lifecycle.shutil.which", return_value="/usr/local/bin/mtplx"),
        patch("modelman.providers.lifecycle.subprocess.run") as mock_run,
    ):
        mock_run.return_value.returncode = 0
        result = stop("mtplx")
    mock_run.assert_called_once()
    args = mock_run.call_args.args[0]
    assert args[:2] == ["/usr/local/bin/mtplx", "stop"]
    assert "--port" in args and "8003" in args
    assert result.ok is True


def test_stop_non_mtplx_returns_error_envelope_not_raise():
    """stop() must return a {"ok": false, "error": ...} LifecycleResult for
    any provider it doesn't (yet) implement, matching every other path in
    this module's JSON-envelope contract — not raise, which would crash
    _main() with an uncaught traceback instead of the envelope the CLI's
    usage string promises for `stop [provider]`."""
    result = stop("omlx")
    assert result.ok is False
    assert "omlx" in (result.error or "")


def test_stop_all_delegates_to_bash_helper_only():
    """stop_all() must delegate entirely to _delegate_stop_all() and return
    its result as-is, without also calling stop("mtplx") directly: the bash
    helper's stop-all mode already tears down mtplx via the shared
    mtplx_stop function, so a direct call here would double-stop it and
    (on a machine missing the mtplx binary) misreport overall failure even
    though every real provider was torn down cleanly. stop() itself is
    unaffected and is tested separately above."""
    with (
        patch("modelman.providers.lifecycle.stop") as mock_stop,
        patch("modelman.providers.lifecycle._delegate_stop_all", return_value=LifecycleResult("stop-all", "", "", True, None)) as mock_delegate,
    ):
        result = stop_all()
    mock_stop.assert_not_called()
    mock_delegate.assert_called_once()
    assert result.ok is True
    assert result.provider == "stop-all"


def test_cli_prints_json_envelope(capsys):
    from modelman.providers.lifecycle import _main

    with patch("modelman.providers.lifecycle.isolate", return_value=LifecycleResult("mtplx", "m", "http://localhost:8003/v1/chat/completions", True, None)):
        rc = _main(["isolate", "mtplx"])
    out = capsys.readouterr().out
    assert rc == 0
    data = json.loads(out)
    assert data == {"provider": "mtplx", "model": "m", "direct_url": "http://localhost:8003/v1/chat/completions", "ok": True, "error": None}


def test_delegate_isolate_sets_env_var_for_mapped_providers():
    """ollama/omlx env overrides must use the exact variable names the bash
    helper reads; the magic-formula approach was wrong for providers like
    mlx_lm_server and never applied to MTPLX."""
    from modelman.providers.lifecycle import _delegate_isolate

    with patch("modelman.providers.lifecycle.shutil.which", return_value="/bin/llm-isolate-provider"), patch("modelman.providers.lifecycle.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = json.dumps({"provider": "ollama", "model": "m", "direct_url": "http://localhost:11434/v1/chat/completions", "ok": True, "error": None})
        _delegate_isolate("ollama", "ornith-1.5:35b", ())
    env = mock_run.call_args.kwargs.get("env")
    assert env is not None
    assert env["LLM_ISOLATE_OLLAMA_MODEL"] == "ornith-1.5:35b"


def test_delegate_isolate_mtplx_uses_positional_arg_not_env_var():
    """MTPLX receives the model via the bash helper's positional arg, not an
    env var — this is the contract the shim now forwards to the lifecycle
    module."""
    from modelman.providers.lifecycle import _delegate_isolate

    with patch("modelman.providers.lifecycle.shutil.which", return_value="/bin/llm-isolate-provider"), patch("modelman.providers.lifecycle.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = json.dumps({"provider": "mtplx", "model": "m", "direct_url": "http://localhost:8003/v1/chat/completions", "ok": True, "error": None})
        _delegate_isolate("mtplx", "org/repo", ())
    args = mock_run.call_args.args[0]
    assert args == ["/bin/llm-isolate-provider", "mtplx"]
    env = mock_run.call_args.kwargs.get("env")
    assert env is None


def _mock_urlopen(body: bytes):
    """Return a MagicMock that behaves as a context manager yielding an object
    whose .read() returns `body`."""
    m = MagicMock()
    m.return_value.__enter__.return_value.read.return_value = body
    return m


def test_warmup_matches_spaced_json():
    """MTPLX may serialize the response with spaces between keys and values;
    the warmup success check must match that form, not only compact JSON."""
    from modelman.providers.lifecycle import _warmup

    body = b'{"object": "chat.completion", "choices": []}'
    with patch("modelman.providers.lifecycle.urllib.request.urlopen", _mock_urlopen(body)):
        # Should return without raising.
        _warmup("org/repo")


def test_warmup_fails_when_no_chat_completion_marker():
    """A response that never contains a chat.completion marker must time out
    (here we make the deadline immediate by mocking time)."""
    from modelman.providers.lifecycle import LifecycleError, _warmup

    body = b'{"object":"list"}'
    with (
        patch("modelman.providers.lifecycle.urllib.request.urlopen", _mock_urlopen(body)),
        patch("modelman.providers.lifecycle.time.monotonic", side_effect=[0.0, 1.0, 1000.0]),
        patch("modelman.providers.lifecycle.time.sleep"),
        pytest.raises(LifecycleError, match="warm up"),
    ):
        _warmup("org/repo", timeout=1.0)


def test_start_mtplx_serve_raises_when_port_still_held():
    """If the previous mtplx process has not released port 8003, start must
    fail fast instead of spawning into a port it cannot bind."""
    from modelman.providers.lifecycle import LifecycleError, _start_mtplx_serve

    with (
        patch("modelman.providers.lifecycle.shutil.which", return_value="/usr/local/bin/mtplx"),
        patch("modelman.providers.lifecycle._wait_for_port_closed", side_effect=LifecycleError("port still answering")),
        patch("modelman.providers.lifecycle.subprocess.Popen") as mock_popen,
        pytest.raises(LifecycleError, match="port still answering"),
    ):
        _start_mtplx_serve("org/repo")
    mock_popen.assert_not_called()


def test_wait_for_port_closed_treats_http_error_as_still_open():
    """HTTPError (e.g. a 404/500 from the health path) means the server
    answered — the port is NOT closed. HTTPError is a URLError/OSError
    subclass, so a bare `except OSError` would misread it as "closed" and
    let a spawn proceed into a port that is still held by the prior
    process."""
    from urllib.error import HTTPError

    from modelman.providers.lifecycle import LifecycleError, _wait_for_port_closed

    mock_urlopen = MagicMock(side_effect=HTTPError("http://x", 404, "not found", None, None))
    with (
        patch("modelman.providers.lifecycle.urllib.request.urlopen", mock_urlopen),
        patch("modelman.providers.lifecycle.time.monotonic", side_effect=[0.0, 0.0, 1000.0]),
        patch("modelman.providers.lifecycle.time.sleep"),
        pytest.raises(LifecycleError, match="still answering"),
    ):
        _wait_for_port_closed("http://localhost:8003/v1/models", timeout=1.0)
    mock_urlopen.assert_called_once()


def test_wait_for_port_closed_returns_on_connection_refused():
    """A true connection-level failure (port actually closed) must return
    immediately rather than being misread as "still open"."""
    from modelman.providers.lifecycle import _wait_for_port_closed

    with patch(
        "modelman.providers.lifecycle.urllib.request.urlopen",
        side_effect=ConnectionRefusedError(),
    ):
        _wait_for_port_closed("http://localhost:8003/v1/models", timeout=1.0)  # must not raise


def test_wait_for_port_closed_read_timeout_means_still_open():
    """A read TimeoutError means the listener accepted the connection and
    then stalled (hung server / still draining under the 10s stop grace)
    — the port is still held. Declaring it closed spawns a new mtplx
    serve into the busy port, which dies as the misleading 'exited
    immediately (address already in use)' instead of the intended 'port
    still held' error."""
    from modelman.providers.lifecycle import LifecycleError, _wait_for_port_closed

    with (
        patch(
            "modelman.providers.lifecycle.urllib.request.urlopen",
            side_effect=TimeoutError,
        ),
        pytest.raises(LifecycleError, match="still answering"),
    ):
        _wait_for_port_closed("http://localhost:8003/v1/models", timeout=0.3)


def test_start_mtplx_serve_raises_when_process_exits_immediately():
    """A bad CLI flag or missing weights can make mtplx serve exit before the
    model ever loads; start must surface that instead of waiting 300s."""
    from modelman.providers.lifecycle import LifecycleError, _start_mtplx_serve

    proc = MagicMock()
    proc.pid = 1234
    proc.poll.return_value = 1
    # mock_open() doesn't simulate real file position tracking — .tell()
    # and read_data must be configured explicitly so the log-tail read
    # (f.seek(0, 2); size = f.tell(); f.seek(...); f.read().decode(...))
    # behaves like a real (here, empty) file instead of returning a bare
    # MagicMock from .tell() that can't be compared against an int.
    log_mock = mock_open(read_data=b"")
    log_mock.return_value.tell.return_value = 0
    with (
        patch("modelman.providers.lifecycle.shutil.which", return_value="/usr/local/bin/mtplx"),
        patch("modelman.providers.lifecycle._wait_for_port_closed"),
        patch("modelman.providers.lifecycle.subprocess.Popen", return_value=proc),
        patch("modelman.providers.lifecycle.time.sleep"),
        patch("builtins.open", log_mock),
        pytest.raises(LifecycleError, match="exited immediately"),
    ):
        _start_mtplx_serve("org/repo")


def test_delegate_isolate_forwards_extra_args():
    """mlx_lm_server target/draft positional args must pass through to the
    helper unchanged."""
    from modelman.providers.lifecycle import _delegate_isolate

    with patch("modelman.providers.lifecycle.shutil.which", return_value="/bin/llm-isolate-provider"), patch("modelman.providers.lifecycle.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = json.dumps({"provider": "mlx_lm_server", "model": "m", "direct_url": "http://localhost:8001/v1/chat/completions", "ok": True, "error": None})
        _delegate_isolate("mlx_lm_server", None, ("org/target", "org/draft"))
    args = mock_run.call_args.args[0]
    assert args == ["/bin/llm-isolate-provider", "mlx_lm_server", "org/target", "org/draft"]
    assert mock_run.call_args.kwargs.get("env") is None


def test_stop_others_prefers_llm_isolate_helper_env(monkeypatch):
    """_stop_others must resolve the isolation helper via the
    LLM_ISOLATE_HELPER env var before PATH.

    The bash shim is routinely invoked by absolute path (the benchmark
    scripts resolve it via `dirname $0`) with bin/ NOT on PATH, so
    shutil.which() returns None there — without the env var, the mtplx
    isolate fails after the helper already stopped every other provider.
    The shim exports its own path so resolution never depends on PATH."""
    calls = []
    monkeypatch.setenv("LLM_ISOLATE_HELPER", "/abs/llm-isolate-provider")
    monkeypatch.setattr(
        "modelman.providers.lifecycle.shutil.which", lambda name: None
    )

    def fake_run(cmd, **kwargs):
        calls.append(cmd)
        return MagicMock(returncode=0)

    monkeypatch.setattr("modelman.providers.lifecycle.subprocess.run", fake_run)
    from modelman.providers.lifecycle import _stop_others

    _stop_others()
    assert calls == [["/abs/llm-isolate-provider", "stop-all"]]


def test_stop_others_without_env_or_path_raises(monkeypatch):
    """No env var AND no PATH entry must surface a clean LifecycleError
    (envelope contract), never a None-subscript crash."""
    monkeypatch.delenv("LLM_ISOLATE_HELPER", raising=False)
    monkeypatch.setattr(
        "modelman.providers.lifecycle.shutil.which", lambda name: None
    )
    from modelman.providers.lifecycle import LifecycleError, _stop_others

    with pytest.raises(LifecycleError, match="not found"):
        _stop_others()


def test_isolate_stops_serve_when_model_wait_fails():
    """A half-started isolate (serve up, model load/warmup failed) must
    stop the spawned mtplx serve.

    Otherwise the orphan holds port 8003 and GPU/RAM while local_control
    clears the [local].running_model marker on failure, so nothing tears
    it down — violating the one-local-model-at-a-time invariant this
    module exists to enforce."""
    from modelman.providers.lifecycle import LifecycleError

    with (
        patch("modelman.providers.lifecycle._stop_others"),
        patch("modelman.providers.lifecycle._start_mtplx_serve"),
        patch(
            "modelman.providers.lifecycle._wait_for_model",
            side_effect=LifecycleError("timed out"),
        ),
        patch("modelman.providers.lifecycle._warmup"),
        patch("modelman.providers.lifecycle._stop_mtplx") as mock_cleanup,
    ):
        result = isolate("mtplx", "Some/Model")
    mock_cleanup.assert_called_once()
    assert result.ok is False
    assert "timed out" in (result.error or "")


def test_isolate_registry_error_returns_envelope_not_traceback():
    """A corrupt registry.toml (RegistryError, not LifecycleError) must
    surface as an ok=False envelope, never a raw traceback — the bash
    shim and benchmark isolation parse stdout as JSON."""
    from modelman.registry import RegistryError

    with (
        patch("modelman.providers.lifecycle._stop_others") as mock_stop,
        patch(
            "modelman.providers.lifecycle.load_registry",
            side_effect=RegistryError("corrupt"),
        ),
        patch("modelman.providers.lifecycle._stop_mtplx") as mock_cleanup,
    ):
        result = isolate("mtplx")
    assert result.ok is False
    assert "corrupt" in (result.error or "")
    # The failure happened before serve started: nothing to tear down,
    # and stop-others must not have run either (fail before any teardown).
    mock_stop.assert_not_called()
    mock_cleanup.assert_not_called()


def test_cli_registry_error_prints_json_envelope(capsys):
    """The CLI entry point must answer a corrupt registry with a JSON
    envelope on stdout and exit 1 — this is the bash-shim stdout contract
    that modelman/benchmark/isolation.py parses (invalid JSON there
    surfaces as the non-actionable 'isolation helper returned invalid
    JSON')."""
    from modelman.providers.lifecycle import _main
    from modelman.registry import RegistryError

    with patch(
        "modelman.providers.lifecycle.load_registry",
        side_effect=RegistryError("corrupt"),
    ):
        code = _main(["isolate", "mtplx"])
    out = json.loads(capsys.readouterr().out)
    assert code == 1
    assert out["ok"] is False
    assert "corrupt" in out["error"]


def test_wait_for_model_raises_promptly_when_serve_dies():
    """A serve process that dies mid-load (OOM kill, missing weights
    found late) must fail the wait immediately with the log tail, not
    poll a dead port for the full 300s deadline — the real crash cause
    would otherwise sit unread in the mtplx log."""
    from modelman.providers.lifecycle import LifecycleError, _wait_for_model

    proc = MagicMock()
    proc.poll.return_value = 137  # SIGKILL'd (OOM) on first check
    proc.returncode = 137
    with (
        patch(
            "modelman.providers.lifecycle._http_models_ids",
            side_effect=AssertionError("must not poll HTTP after process death"),
        ),
        pytest.raises(LifecycleError, match="exited during model load"),
    ):
        _wait_for_model("Org/Model", proc, timeout=300.0)


def test_isolate_mtplx_same_model_keeps_loaded():
    """Re-isolating the model mtplx is already serving must not pay a
    full stop + reload: the bash providers keep an already-loaded target
    (stop_all_local's keep arg), and the mtplx path now does the same by
    name-checking /v1/models before tearing down — mtplx is
    single-model-per-process, so 'same provider' is only keepable when
    it is the same model."""
    with (
        patch(
            "modelman.providers.lifecycle._serving_model", return_value=True
        ) as mock_probe,
        patch("modelman.providers.lifecycle._stop_others") as mock_stop,
        patch("modelman.providers.lifecycle._start_mtplx_serve") as mock_start,
        patch("modelman.providers.lifecycle._wait_for_model"),
        patch("modelman.providers.lifecycle._warmup") as mock_warmup,
    ):
        result = isolate("mtplx", "Some/Model")
    mock_probe.assert_called_once_with("Some/Model")
    mock_stop.assert_called_once_with(keep="mtplx")
    mock_start.assert_not_called()
    mock_warmup.assert_called_once_with("Some/Model")
    assert result.ok is True


def test_isolate_mtplx_different_model_restarts():
    """A different model (or no server) must take the full restart path:
    mtplx is single-model-per-process, so serving model B means a stop +
    respawn, and _stop_others must tear mtplx down too (no keep)."""
    with (
        patch(
            "modelman.providers.lifecycle._serving_model", return_value=False
        ),
        patch("modelman.providers.lifecycle._stop_others") as mock_stop,
        patch("modelman.providers.lifecycle._start_mtplx_serve") as mock_start,
        patch("modelman.providers.lifecycle._wait_for_model"),
        patch("modelman.providers.lifecycle._warmup"),
    ):
        result = isolate("mtplx", "Some/Model")
    mock_stop.assert_called_once_with()
    mock_start.assert_called_once_with("Some/Model")
    assert result.ok is True


def test_isolate_mtplx_ambiguous_registry_refuses_instead_of_guessing():
    """With no explicit model and more than one mtplx entry, isolate()
    must refuse, not silently serve the first registry match.

    This branch already had to patch four callers that failed to
    forward the model name; without the refusal a fifth such gap would
    serve (and benchmark) the wrong weights with ok=true — the
    silent-wrong-weights failure mode."""
    from modelman.registry import ModelEntry, Registry

    two = Registry(
        providers=[],
        models=[
            ModelEntry(
                id=f"mtplx/Org/m{n}",
                family="qwen3.8",
                provider_id="mtplx",
                model_name=f"Org/m{n}",
            )
            for n in (1, 2)
        ],
    )
    with (
        patch("modelman.providers.lifecycle._stop_others") as mock_stop,
        patch("modelman.providers.lifecycle.load_registry", return_value=two),
    ):
        result = isolate("mtplx")
    assert result.ok is False
    assert "model required" in (result.error or "")
    mock_stop.assert_not_called()  # refuse before any teardown
