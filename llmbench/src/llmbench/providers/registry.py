"""Provider-id aliases: a registry row that is a second name for one server."""

from __future__ import annotations

# `omlx-6bit` is the omlx server reached through its own registry row (wt's
# localmodels.Family maps it the same way). Two rows that resolve to the same
# name are one server, so the benchmark runner isolates them as one.
_ALIASES: dict[str, str] = {"omlx-6bit": "omlx"}


class ProviderRegistry:
    @classmethod
    def resolve(cls, name: str) -> str:
        """`name`, or the provider it is an alias for."""
        return _ALIASES.get(name, name)
