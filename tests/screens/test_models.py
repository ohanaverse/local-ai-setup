"""Tests for ModelScreen helpers (the add/edit adapter)."""

from modelman.registry import AuthConfig, ProviderEntry, Registry
from modelman.screens.models import _variant_to_model_entry


def test_variant_to_model_entry_sets_source_curated():
    registry = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
        ]
    )
    variant = {"id": "ollama/x", "provider": "ollama", "name": "x"}
    entry = _variant_to_model_entry(variant, family="x", registry=registry)
    assert entry.source == "curated"
