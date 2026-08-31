"""Abstract base class for model providers."""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Any, TypedDict


class VariantSpec(TypedDict, total=False):
    """A single model variant within a family manifest. All fields optional
    in the TypedDict sense, but providers require specific ones at runtime."""

    id: str  # stable id within the family
    provider: str  # "ollama" | "llamacpp" | "omlx"
    name: str  # provider-specific (e.g. "ornith-1.5:35b" for ollama)
    repo: str | None  # HF repo id (for llamacpp/omlx)
    files: list[str] | None  # files in repo (for llamacpp)
    quantizations: list[str] | None  # quant tags (for omlx)
    model_info: dict | None  # freeform LiteLLM model_info keys
    location: str | None  # "local" | "cloud"
    # Cost as a plain dict so providers can JSON-serialize VariantSpec if
    # needed; None when unset. Use registry._cost_from_dict() to rebuild a
    # registry.Cost object.
    cost: dict[str, Any] | None
    usage_tier: (
        str | None
    )  # ollama usage tier ("low" | "medium" | "high" | "extra high"); None otherwise


class LocalModel(TypedDict):
    """A model that exists on the local machine."""

    variant_id: str
    path: str
    size_bytes: int | None


class Provider(ABC):
    """Base class for all model providers."""

    name: str = ""

    def __init__(self, config: dict):
        self.config = config

    @abstractmethod
    def is_downloaded(self, variant: VariantSpec) -> bool: ...

    @abstractmethod
    def download(self, variant: VariantSpec) -> str:
        """Download the variant. Returns the local path on success."""
        ...

    @abstractmethod
    def list_local(self) -> list[LocalModel]: ...

    def size_of(self, variant: VariantSpec) -> int | None:
        """Return the on-disk size in bytes for this variant, or None if unknown.

        Providers override this when they can determine a size. Default is None
        so unknown providers don't crash the size columns.
        """
        return None

    def path_of(self, variant: VariantSpec) -> str | None:
        """Return the on-disk path for this variant, or None if not downloaded.

        Providers override this when they can locate a downloaded model. Default
        is None so unknown providers don't crash path columns.
        """
        return None
