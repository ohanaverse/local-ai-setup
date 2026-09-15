"""Backend registry — the single source of truth for what local providers
modelman knows how to start and stop.

`orchestrate.py` drives everything in `BACKENDS`: `isolate()` looks a
provider up here, `_stop_others()`/`stop_all()` fan out over it, and
`restore()` reads each backend's `restore_action` from it. Adding a backend
module and registering it below is all it takes to wire a new provider into
every one of those paths.

`llamacpp` is deliberately IN `BACKENDS` but NOT in `SUPPORTED_PROVIDER_IDS`
— see `SUPPORTED_PROVIDER_IDS` below.
"""

from __future__ import annotations

from .base import Backend
from .llamacpp import LLAMACPP
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
BACKENDS[LLAMACPP.id] = LLAMACPP

# What modelman may isolate on a caller's behalf, and the set
# `orchestrate.stop()` accepts. Narrower than BACKENDS on purpose:
# `llamacpp` is retired (2026-09-07, issue #33 — re-enable steps in
# docs/reference/provider-artifacts.md) and has no `stop` case arm in bash
# either, yet its start branch still works if invoked directly, so it stays
# a live BACKENDS entry that stop_all()/_stop_others() still tear down.
# This is the one place modelman checks isolability; `modelman.benchmark.
# isolation.SUPPORTED_PROVIDER_IDS` re-exports it.
SUPPORTED_PROVIDER_IDS: frozenset[str] = frozenset(
    {"ollama", "omlx", "omlx-6bit", "mlx_lm_server", "mtplx"}
)
