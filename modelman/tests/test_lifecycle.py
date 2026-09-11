import json
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
        patch("modelman.providers.lifecycle.subprocess.Popen") as mock_popen,
        patch("builtins.open", mock_open()),
    ):
        mock_popen.return_value = MagicMock(pid=1234)
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
