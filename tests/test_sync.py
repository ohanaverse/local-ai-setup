"""Provider sync — ollama discovery, curated-wins merge, state update."""

from unittest.mock import MagicMock

import pytest

from modelman.registry import ModelEntry, Registry
from modelman.state import ModelState, StateStore
from modelman.sync import (
    SyncError,
    _parse_ollama_list,
    discover_ollama,
    merge,
    sync,
    update_state,
)


def test_parse_ollama_list_local_row():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "ornith-1.5:9b     e5df7dcdd8a2    6.6 GB    4 days ago\n"
    )
    models, sizes = _parse_ollama_list(stdout)
    assert len(models) == 1
    m = models[0]
    assert m.id == "ollama/ornith-1.5:9b"
    assert m.family == "ornith-1.5:9b"
    assert m.provider_id == "ollama"
    assert m.model_name == "ornith-1.5:9b"
    assert m.location == "local"
    assert m.source == "discovered"
    assert sizes == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_parse_ollama_list_cloud_row():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "some-cloud        def456          -         3 days ago\n"
    )
    models, sizes = _parse_ollama_list(stdout)
    assert len(models) == 1
    assert models[0].location == "cloud"
    assert sizes == {}


def test_parse_ollama_list_skips_header_and_malformed():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "ornith-1.5:9b     e5df7dcdd8a2    6.6 GB    4 days ago\n"
        "\n"
        "short\n"
    )
    models, sizes = _parse_ollama_list(stdout)
    assert len(models) == 1


def test_discover_ollama_runs_ollama_list(mock_runner):
    runner = mock_runner(
        returncode=0,
        stdout="NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n",
    )
    models, sizes = discover_ollama(runner)
    runner.assert_called_with(["ollama", "list"], capture_output=True, text=True)
    assert len(models) == 1
    assert models[0].id == "ollama/ornith-1.5:9b"
    assert sizes == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_discover_ollama_raises_on_failure(mock_runner):
    runner = mock_runner(returncode=1, stdout="", stderr="ollama not found")
    with pytest.raises(SyncError, match="ollama list"):
        discover_ollama(runner)


def test_merge_skips_curated():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama",
                model_name="a", source="curated", tags=["code"],
            ),
        ]
    )
    discovered = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", source="discovered", location="cloud",
        ),
    ]
    result = merge(registry, discovered)
    assert result.skipped == ["ollama/a"]
    assert result.added == []
    assert result.refreshed == []
    # curated row untouched: tags preserved, location NOT refreshed
    assert registry.models[0].tags == ["code"]
    assert registry.models[0].location is None


def test_merge_adds_new():
    registry = Registry(models=[])
    discovered = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", source="discovered", location="local",
        ),
    ]
    result = merge(registry, discovered)
    assert result.added == ["ollama/a"]
    assert len(registry.models) == 1


def test_merge_refreshes_discovered_location():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama",
                model_name="a", source="discovered", location="local",
            ),
        ]
    )
    discovered = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", source="discovered", location="cloud",
        ),
    ]
    result = merge(registry, discovered)
    assert result.refreshed == ["ollama/a"]
    assert registry.models[0].location == "cloud"


def test_update_state_local_model():
    state = StateStore()
    models = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", location="local",
        ),
    ]
    update_state(state, models, {"a": 1024})
    s = state.get("ollama/a")
    assert s.downloaded is True
    assert s.disk_path == "ollama:a"
    assert s.size_bytes == 1024


def test_update_state_cloud_model():
    state = StateStore()
    models = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", location="cloud",
        ),
    ]
    update_state(state, models, {})
    s = state.get("ollama/a")
    assert s.downloaded is False
    assert s.disk_path is None
    assert s.size_bytes is None


def test_update_state_preserves_litellm_exposed():
    state = StateStore()
    state.set("ollama/a", ModelState(downloaded=False, litellm_exposed=True))
    models = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", location="local",
        ),
    ]
    update_state(state, models, {"a": 1024})
    assert state.get("ollama/a").litellm_exposed is True


def _result(returncode: int, stdout: str) -> MagicMock:
    r = MagicMock()
    r.returncode = returncode
    r.stdout = stdout
    r.stderr = ""
    return r


def test_sync_full_flow():
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n"),
        _result(0, "Capabilities\n    tools\n"),
    ]
    registry = Registry(models=[])
    state = StateStore()

    result = sync(registry, state, runner)

    assert result.added == ["ollama/ornith-1.5:9b"]
    assert registry.models[0].model_info == {"supports_function_calling": True}
    assert state.get("ollama/ornith-1.5:9b").downloaded is True


def test_sync_does_not_rerun_ollama_show_for_existing_model():
    # Existing discovered rows keep their model_info; only new models
    # trigger `ollama show`.
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n"),
    ]
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/ornith-1.5:9b", family="ornith-1.5:9b",
                provider_id="ollama", model_name="ornith-1.5:9b",
                source="discovered", location="local",
                model_info={"supports_vision": True},
            ),
        ]
    )
    state = StateStore()

    sync(registry, state, runner)

    # Only one subprocess call (ollama list); no ollama show.
    assert runner.call_count == 1
    assert registry.models[0].model_info == {"supports_vision": True}
