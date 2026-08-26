"""Abstract base class for model providers."""
from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TypedDict


class VariantSpec(TypedDict):
    """A single model variant within a family manifest."""
    id: str                           # stable id within the family
    provider: str                     # "ollama" | "llamacpp" | "omlx"
    name: str                         # provider-specific (e.g. "ornith-1.5:35b" for ollama)
    repo: str | None                  # HF repo id (for llamacpp/omlx)
    files: list[str] | None           # files in repo (for llamacpp)
    quantizations: list[str] | None   # quant tags (for omlx)


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