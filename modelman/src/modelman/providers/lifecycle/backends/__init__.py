"""Backend registry.

Later tasks register their own backend instances here as each backend is
ported (ollama, omlx, mtplx, mlx_lm_server, llamacpp). Empty for now —
nothing in the live isolate/stop/stop_all code paths reads this yet.
"""

from __future__ import annotations

from .base import Backend

BACKENDS: dict[str, Backend] = {}
