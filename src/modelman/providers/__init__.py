"""Model providers — one module per backend, registered with ProviderRegistry."""

from .base import LocalModel, Provider, VariantSpec
from .registry import ProviderRegistry

# Import provider modules to trigger their register() calls at import time.
from . import ollama  # noqa: F401
from . import llamacpp  # noqa: F401
from . import omlx  # noqa: F401

__all__ = ["LocalModel", "Provider", "ProviderRegistry", "VariantSpec"]