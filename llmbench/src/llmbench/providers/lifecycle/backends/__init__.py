"""Backend registry — the single source of truth for what local providers
llmbench knows how to start and stop.

`orchestrate.py` drives everything in `BACKENDS`: `isolate()` looks a
provider up here, `_stop_others()`/`stop_all()` fan out over it, and
`restore()` reads each backend's `restore_action` from it. Adding a backend
module and registering it below is all it takes to wire a new provider into
every one of those paths.
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

# What llmbench may isolate on a caller's behalf, and the set
# `orchestrate.stop()` accepts: every backend. It keeps its own name because
# `llmbench.benchmark.isolation.SUPPORTED_PROVIDER_IDS` re-exports it and the
# benchmark runners filter on it.
SUPPORTED_PROVIDER_IDS: frozenset[str] = frozenset(BACKENDS)
