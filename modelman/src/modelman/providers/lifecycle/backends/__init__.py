"""Backend registry.

Later tasks register their own backend instances here as each backend is
ported (mtplx, llamacpp remain). Nothing in the live isolate/stop/stop_all
code paths reads this yet.
"""

from __future__ import annotations

from .base import Backend
from .mlx_lm_server import MLX_LM_SERVER
from .ollama import OLLAMA
from .omlx import OMLX_4BIT, OMLX_6BIT

BACKENDS: dict[str, Backend] = {}
BACKENDS[OLLAMA.id] = OLLAMA
BACKENDS[OMLX_4BIT.id] = OMLX_4BIT
BACKENDS[OMLX_6BIT.id] = OMLX_6BIT
BACKENDS[MLX_LM_SERVER.id] = MLX_LM_SERVER
