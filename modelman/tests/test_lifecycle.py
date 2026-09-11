import json

import pytest
from unittest.mock import MagicMock, mock_open, patch

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
        patch("modelman.providers.lifecycle._start_mtplx_serve") as mock_start,
        patch("modelman.providers.lifecycle._wait_for_model") as mock_wait,
        patch("modelman.providers.lifecycle._warmup") as mock_warmup,
        patch("modelman.providers.lifecycle.load_registry", return_value=_mtplx_registry()),
    ):
        result = isolate("mtplx")
    mock_stop.assert_called_once()
    mock_start.assert_called_once_with("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")
    mock_wait.assert_called_once()
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
        patch("modelman.providers.lifecycle._stop_mtplx"),
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


def test_stop_mtplx_runs_mtplx_stop():
    with patch("modelman.providers.lifecycle.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 0
        result = stop("mtplx")
    mock_run.assert_called_once()
    args = mock_run.call_args.args[0]
    assert args[:2] == ["mtplx", "stop"]
    assert "--port" in args and "8003" in args
    assert result.ok is True


def test_stop_all_stops_mtplx_and_delegates_others():
    with (
        patch("modelman.providers.lifecycle.stop", return_value=LifecycleResult("mtplx", "", "", True, None)) as mock_stop,
        patch("modelman.providers.lifecycle._delegate_stop_all", return_value=LifecycleResult("stop-all", "", "", True, None)) as mock_delegate,
    ):
        result = stop_all()
    mock_stop.assert_called_once_with("mtplx")
    mock_delegate.assert_called_once()
    assert result.ok is True


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

    body = '{"object": "chat.completion", "choices": []}'.encode()
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
    ):
        with pytest.raises(LifecycleError, match="warm up"):
            _warmup("org/repo", timeout=1.0)


def test_start_mtplx_serve_raises_when_port_still_held():
    """If the previous mtplx process has not released port 8003, start must
    fail fast instead of spawning into a port it cannot bind."""
    from modelman.providers.lifecycle import LifecycleError, _start_mtplx_serve

    with (
        patch("modelman.providers.lifecycle.shutil.which", return_value="/usr/local/bin/mtplx"),
        patch("modelman.providers.lifecycle._stop_mtplx"),
        patch("modelman.providers.lifecycle._wait_for_port_closed", side_effect=LifecycleError("port still answering")),
        patch("modelman.providers.lifecycle.subprocess.Popen") as mock_popen,
    ):
        with pytest.raises(LifecycleError, match="port still answering"):
            _start_mtplx_serve("org/repo")
    mock_popen.assert_not_called()


def test_start_mtplx_serve_raises_when_process_exits_immediately():
    """A bad CLI flag or missing weights can make mtplx serve exit before the
    model ever loads; start must surface that instead of waiting 300s."""
    from modelman.providers.lifecycle import LifecycleError, _start_mtplx_serve

    proc = MagicMock()
    proc.pid = 1234
    proc.poll.return_value = 1
    with (
        patch("modelman.providers.lifecycle.shutil.which", return_value="/usr/local/bin/mtplx"),
        patch("modelman.providers.lifecycle._stop_mtplx"),
        patch("modelman.providers.lifecycle._wait_for_port_closed"),
        patch("modelman.providers.lifecycle.subprocess.Popen", return_value=proc),
        patch("modelman.providers.lifecycle.time.sleep"),
        patch("builtins.open", mock_open()),
    ):
        with pytest.raises(LifecycleError, match="exited immediately"):
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
