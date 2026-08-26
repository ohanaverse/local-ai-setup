"""Provider registry for pluggable dispatch."""
from __future__ import annotations

from .base import Provider


class ProviderRegistry:
    _providers: dict[str, type[Provider]] = {}

    @classmethod
    def register(cls, provider_cls: type[Provider]) -> None:
        if not provider_cls.name:
            raise ValueError(f"{provider_cls.__name__} has no name attribute")
        cls._providers[provider_cls.name] = provider_cls

    @classmethod
    def get(cls, name: str, config: dict) -> Provider:
        if name not in cls._providers:
            raise KeyError(f"Unknown provider: {name}. Registered: {list(cls._providers)}")
        return cls._providers[name](config)

    @classmethod
    def available(cls) -> list[str]:
        return sorted(cls._providers)