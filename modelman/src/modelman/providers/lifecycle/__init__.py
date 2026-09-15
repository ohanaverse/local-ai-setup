"""Public API for modelman's local-provider lifecycle.

`orchestrate.py` holds the isolate/stop/stop_all/restore implementation;
`backends/` holds the per-provider logic; `probe.py`/`launchd.py`/
`pidproc.py`/`binaries.py` hold the primitives they share. This module is
pure re-exports so callers keep importing one stable name
(`modelman.providers.lifecycle`) regardless of how the internals are split.

As of the orchestration port (issue #79) nothing here shells out to
`bin/llm-isolate-provider` any more: every provider is driven in-process
through `backends.BACKENDS`.
"""

from __future__ import annotations

from .backends import BACKENDS, SUPPORTED_PROVIDER_IDS
from .envelope import LifecycleError, LifecycleResult
from .orchestrate import isolate, restore, stop, stop_all

__all__ = [
    "BACKENDS",
    "SUPPORTED_PROVIDER_IDS",
    "LifecycleError",
    "LifecycleResult",
    "isolate",
    "restore",
    "stop",
    "stop_all",
]
