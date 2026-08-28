"""Provider sync — reconcile configured ollama models against `ollama list`."""

from unittest.mock import MagicMock

import pytest

from modelman.registry import ModelEntry, Registry
from modelman.state import ModelState, StateStore
from modelman.sync import (
    SyncError,
    _parse_ollama_list_sizes,
    list_ollama,
    reconcile,
    sync,
)


def test_parse_ollama_list_sizes_local_row():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "ornith-1.5:9b     e5df7dcdd8a2    6.6 GB    4 days ago\n"
    )
    assert _parse_ollama_list_sizes(stdout) == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_parse_ollama_list_sizes_skips_cloud_row():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "some-cloud        def456          -         3 days ago\n"
    )
    assert _parse_ollama_list_sizes(stdout) == {}


def test_parse_ollama_list_sizes_skips_header_and_malformed():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "ornith-1.5:9b     e5df7dcdd8a2    6.6 GB    4 days ago\n"
        "\n"
        "short\n"
    )
    assert _parse_ollama_list_sizes(stdout) == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_list_ollama_runs_ollama_list(mock_runner):
    runner = mock_runner(
        returncode=0,
        stdout="NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n",
    )
    sizes = list_ollama(runner)
    runner.assert_called_with(["ollama", "list"], capture_output=True, text=True)
    assert sizes == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_list_ollama_raises_on_failure(mock_runner):
    runner = mock_runner(returncode=1, stdout="", stderr="ollama not found")
    with pytest.raises(SyncError, match="ollama list"):
        list_ollama(runner)


def test_reconcile_downloaded_model():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama", model_name="a",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {"a": 1024})
    assert result.downloaded == ["ollama/a"]
    assert result.not_downloaded == []
    s = state.get("ollama/a")
    assert s.downloaded is True
    assert s.disk_path == "ollama:a"
    assert s.size_bytes == 1024


def test_reconcile_not_downloaded_model():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama", model_name="a",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {})
    assert result.downloaded == []
    assert result.not_downloaded == ["ollama/a"]
    s = state.get("ollama/a")
    assert s.downloaded is False
    assert s.disk_path is None
    assert s.size_bytes is None


def test_reconcile_skips_non_ollama_models():
    registry = Registry(
        models=[
            ModelEntry(
                id="openrouter/x", family="x", provider_id="openrouter", model_name="x",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {"x": 1024})
    assert result.downloaded == []
    assert result.not_downloaded == []
    assert state.get("openrouter/x").downloaded is False


def test_reconcile_preserves_litellm_exposed():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama", model_name="a",
            ),
        ]
    )
    state = StateStore()
    state.set("ollama/a", ModelState(downloaded=False, litellm_exposed=True))
    reconcile(registry, state, {"a": 1024})
    assert state.get("ollama/a").litellm_exposed is True


def _result(returncode: int, stdout: str) -> MagicMock:
    r = MagicMock()
    r.returncode = returncode
    r.stdout = stdout
    r.stderr = ""
    return r


def test_sync_reconciles_configured_models():
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n"),
    ]
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/ornith-1.5:9b", family="ornith-1.5:9b",
                provider_id="ollama", model_name="ornith-1.5:9b",
            ),
            ModelEntry(
                id="ollama/other", family="other",
                provider_id="ollama", model_name="other",
            ),
        ]
    )
    state = StateStore()

    result = sync(registry, state, runner)

    assert result.downloaded == ["ollama/ornith-1.5:9b"]
    assert result.not_downloaded == ["ollama/other"]
    assert state.get("ollama/ornith-1.5:9b").downloaded is True
    assert state.get("ollama/other").downloaded is False
    # registry is untouched (no new models added)
    assert len(registry.models) == 2


def test_sync_ignores_ollama_models_not_in_registry():
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\nunconfigured  abc  6.6 GB  4 days ago\n"),
    ]
    registry = Registry(models=[])
    state = StateStore()

    result = sync(registry, state, runner)

    assert result.downloaded == []
    assert result.not_downloaded == []
    assert len(registry.models) == 0
    assert state.models == {}
