"""Provider registry for pluggable dispatch."""

from __future__ import annotations

from .base import Provider

# A registry provider row that is a second name for a server another class
# already drives. `omlx-6bit` is the omlx server reached through its own row
# (wt's localmodels.Family maps it the same way): it has no class of its own,
# so without this a registry whose only omlx row is `omlx-6bit` could not be
# listed, reconciled or downloaded into at all (#194). The row's own settings
# (its model_dir) are still what the instance is built with.
_ALIASES: dict[str, str] = {"omlx-6bit": "omlx"}


class ProviderRegistry:
    _providers: dict[str, type[Provider]] = {}

    @classmethod
    def resolve(cls, name: str) -> str:
        """`name`, or the class name it is an alias for when nothing is
        registered under `name` itself (a registered class always wins).

        Two registry rows that resolve to the same name are the same server:
        callers that must treat them as one (sync's model-directory reconcile)
        compare this, never the row ids. The shared-artifact guard used to; it
        now asks every row, sibling or not (#241)."""
        if name in cls._providers:
            return name
        return _ALIASES.get(name, name)

    @classmethod
    def register(cls, provider_cls: type[Provider]) -> None:
        if not provider_cls.name:
            raise ValueError(f"{provider_cls.__name__} has no name attribute")
        cls._providers[provider_cls.name] = provider_cls

    @classmethod
    def get(cls, name: str, config: dict) -> Provider:
        resolved = cls.resolve(name)
        if resolved not in cls._providers:
            raise KeyError(f"Unknown provider: {name}. Registered: {list(cls._providers)}")
        return cls._providers[resolved](config)

    @classmethod
    def available(cls) -> list[str]:
        return sorted(cls._providers)

    @classmethod
    def get_class(cls, name: str) -> type[Provider] | None:
        """The registered Provider class for `name`, or None if unregistered.

        Unlike get(), this doesn't require a config dict or construct an
        instance — for callers that only need to read a class-level
        capability flag (e.g. Provider.manages_own_cache). An alias row
        (`omlx-6bit`) answers with the class of the server it names."""
        return cls._providers.get(cls.resolve(name))
