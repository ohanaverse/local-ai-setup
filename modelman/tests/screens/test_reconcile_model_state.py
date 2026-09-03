"""Tests for the shared reconcile_model_state helper.

FamilyScreen and ModelScreen both delegate their background reconcile
worker to this single function so the ready/disk_path/size_bytes write
semantics can't drift between the two screens.
"""

from unittest.mock import MagicMock

import pytest

from modelman.providers import registry as prov_registry
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.screens import reconcile_model_state
from modelman.state import ModelState, StateStore


def _seed(monkeypatch, *, models, provider_location="local"):
    reg = Registry(
        providers=[
            ProviderEntry(
                id="ollama", name="O", auth=AuthConfig(type="none"), location=provider_location
            )
        ],
        models=models,
    )
    state = StateStore()
    stub = MagicMock()
    stub.name = "ollama"
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))
    return reg, state, stub


def test_reconcile_model_state_calls_list_local_once_per_provider(monkeypatch):
    """Regression: reconcile previously called provider.list_local() inside
    the per-model loop, so N ready models on the same provider triggered N
    subprocess/filesystem scans (e.g. `ollama list`). It must be called at
    most once per provider regardless of how many models that provider has."""
    models = [
        ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
        ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b"),
        ModelEntry(id="ollama/c", family="f", provider_id="ollama", model_name="c"),
    ]
    reg, state, stub = _seed(monkeypatch, models=models)
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 100
    stub.list_local.return_value = [
        {"name": "a", "local_path": "/models/a"},
        {"name": "b", "local_path": "/models/b"},
        {"name": "c", "local_path": "/models/c"},
    ]

    reconcile_model_state(models, reg, state)

    assert stub.list_local.call_count == 1
    assert state.get("ollama/a").disk_path == "/models/a"
    assert state.get("ollama/b").disk_path == "/models/b"
    assert state.get("ollama/c").disk_path == "/models/c"


def test_reconcile_model_state_skips_list_local_when_nothing_ready(monkeypatch):
    """Regression: an earlier version of the per-provider fix called
    list_local() unconditionally once per provider, adding a real
    subprocess/filesystem scan to every reconcile pass even when nothing
    in that provider's batch is downloaded — this slowed FamilyScreen's
    background reconcile worker enough to leave its table disabled past
    the point a test (or a user) expected to interact with it. list_local
    must only be called when at least one model in the batch is ready."""
    models = [
        ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
        ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b"),
    ]
    reg, state, stub = _seed(monkeypatch, models=models)
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None

    reconcile_model_state(models, reg, state)

    assert stub.list_local.call_count == 0
    assert state.get("ollama/a").ready is False
    assert state.get("ollama/b").ready is False


def test_reconcile_model_state_marks_local_artifact_ready(monkeypatch):
    """A local-artifact model the provider reports as downloaded must have
    ready/disk_path/size_bytes written into state — the core contract both
    screens rely on to show ready/size without a manual toggle."""
    models = [ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")]
    reg, state, stub = _seed(monkeypatch, models=models)
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 12345
    stub.list_local.return_value = [{"name": "a", "local_path": "/models/a"}]

    reconcile_model_state(models, reg, state)

    result = state.get("ollama/a")
    assert result.ready is True
    assert result.disk_path == "/models/a"
    assert result.size_bytes == 12345


def test_reconcile_model_state_clears_ready_when_artifact_missing(monkeypatch):
    """A local-artifact model whose files are gone must have ready flipped
    back to False and its path/size cleared, not left stale."""
    models = [ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")]
    reg, state, stub = _seed(monkeypatch, models=models)
    state.set("ollama/a", ModelState(ready=True, disk_path="/models/a", size_bytes=12345))
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    stub.list_local.return_value = []

    reconcile_model_state(models, reg, state)

    result = state.get("ollama/a")
    assert result.ready is False
    assert result.disk_path is None
    assert result.size_bytes is None


def test_reconcile_model_state_never_readies_cloud_located_model(monkeypatch):
    """A model tagged location='cloud' has no on-disk artifact reconcile can
    observe, even if the provider (wrongly, or for an unrelated variant)
    reports is_downloaded=True — reconcile must never flip ready for it."""
    models = [
        ModelEntry(
            id="ollama/a", family="f", provider_id="ollama", model_name="a", location="cloud"
        )
    ]
    reg, state, stub = _seed(monkeypatch, models=models)
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 999
    stub.list_local.return_value = []

    reconcile_model_state(models, reg, state)

    assert state.get("ollama/a").ready is False


@pytest.mark.parametrize("side_effect", [Exception("boom")])
def test_reconcile_model_state_survives_list_local_failure(monkeypatch, side_effect):
    """A provider's list_local() call is best-effort (e.g. a transient
    `ollama list` failure) and must not abort reconcile for every other
    model — is_downloaded/size_of/ready still get written."""
    models = [ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")]
    reg, state, stub = _seed(monkeypatch, models=models)
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 42
    stub.list_local.side_effect = side_effect

    reconcile_model_state(models, reg, state)

    result = state.get("ollama/a")
    assert result.ready is True
    assert result.size_bytes == 42
