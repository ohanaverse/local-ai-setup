"""Backend registry.

Later tasks register their own backend instances here as each backend is
ported (llamacpp remains). `lifecycle/__init__.py`'s `isolate()`/`stop()`
now call `MTPLX`'s methods directly for `provider_id == "mtplx"`; every
other provider still delegates to the bash helper and does not read this
registry yet.
"""

from __future__ import annotations

from .base import Backend
from .mlx_lm_server import MLX_LM_SERVER
from .mtplx import MTPLX
from .ollama import OLLAMA
from .omlx import OMLX_4BIT, OMLX_6BIT

BACKENDS: dict[str, Backend] = {}
BACKENDS[OLLAMA.id] = OLLAMA
BACKENDS[OMLX_4BIT.id] = OMLX_4BIT
BACKENDS[OMLX_6BIT.id] = OMLX_6BIT
BACKENDS[MLX_LM_SERVER.id] = MLX_LM_SERVER
BACKENDS[MTPLX.id] = MTPLX
