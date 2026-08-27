"""Model providers — one module per backend, registered with ProviderRegistry."""

# Import provider modules to trigger their register() calls at import time.
from . import (
    llamacpp,  # noqa: F401
    ollama,  # noqa: F401
    omlx,  # noqa: F401
)
from .base import LocalModel, Provider, VariantSpec
from .registry import ProviderRegistry

__all__ = ["LocalModel", "Provider", "ProviderRegistry", "VariantSpec"]
