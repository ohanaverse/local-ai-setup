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
