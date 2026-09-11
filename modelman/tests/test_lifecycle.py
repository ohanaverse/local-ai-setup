import json
from unittest.mock import patch

from modelman.providers.lifecycle import (
    LifecycleResult,
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
