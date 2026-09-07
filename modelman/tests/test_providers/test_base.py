from modelman.providers.base import Provider, VariantSpec
from modelman.providers.registry import ProviderRegistry


class FakeProvider(Provider):
    name = "fake"

    def is_downloaded(self, variant: VariantSpec) -> bool:
        return True

    def download(self, variant: VariantSpec) -> str:
        return "/tmp/fake"

    def list_local(self) -> list[dict]:
        return []


def test_variantspec_has_cost_key_no_usage_tier():
    """The dialog-facing spec dict carries billing metadata as a plain cost
    dict and no longer exposes the legacy usage_tier field."""
    assert "cost" in VariantSpec.__annotations__
    assert "usage_tier" not in VariantSpec.__annotations__


def test_provider_subclass_works():
    p = FakeProvider({})
    variant: VariantSpec = {"id": "x", "provider": "fake", "name": "fake-model"}
    assert p.is_downloaded(variant) is True
    assert p.download(variant) == "/tmp/fake"
    assert p.list_local() == []


def test_registry_register_and_get():
    ProviderRegistry.register(FakeProvider)
    assert "fake" in ProviderRegistry.available()
    instance = ProviderRegistry.get("fake", {})
    assert isinstance(instance, FakeProvider)
    ProviderRegistry._providers.pop("fake", None)  # cleanup


def test_registry_get_unknown_raises():
    import pytest

    ProviderRegistry._providers.pop("does-not-exist", None)
    with pytest.raises(KeyError):
        ProviderRegistry.get("does-not-exist", {})


def test_provider_size_of_default_is_none():
    from modelman.providers.ollama import OllamaProvider

    p = FakeProvider({})
    assert p.size_of({"id": "x", "provider": "fake", "name": "x"}) is None
    assert hasattr(OllamaProvider({}), "size_of")


def test_provider_path_of_default_is_none():
    from modelman.providers.ollama import OllamaProvider

    p = FakeProvider({})
    assert p.path_of({"id": "x", "provider": "fake", "name": "x"}) is None
    assert hasattr(OllamaProvider({}), "path_of")


def test_cleanup_partial_download_default_is_noop():
    # Providers with no partial-download artifacts to clean up (Ollama:
    # `ollama pull` is resumable and reconciles its own state) must not
    # need to implement this — the base class default is a no-op so
    # DownloadManager can call it unconditionally on every provider.
    class _NoopProvider(Provider):
        name = "noop"

        def is_downloaded(self, variant):
            return False

        def download(self, variant):
            return ""

        def list_local(self):
            return []

    p = _NoopProvider({})
    p.cleanup_partial_download({"id": "x", "provider": "noop"})  # must not raise
