"""Model providers — one module per backend, registered with ProviderRegistry."""

from .base import LocalModel, Provider, VariantSpec
from .registry import ProviderRegistry

__all__ = ["LocalModel", "Provider", "ProviderRegistry", "VariantSpec"]