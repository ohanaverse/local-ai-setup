"""MTPLX backend: the contract file's mtplx entry on the generic
`ProcessServerBackend`. mtplx is the one backend that tears a failed start
back down (`cleanup_on_failure=True`).
"""

from __future__ import annotations

from ...process_spec import ProcessSpecError, process_spec
from .process_server import ProcessServerBackend

_SPEC = process_spec("mtplx")
if _SPEC is None:
    raise ProcessSpecError("the process-provider contract has no mtplx entry")

MTPLX = ProcessServerBackend(_SPEC, cleanup_on_failure=True)

# Names the tests pin; derived from the contract entry.
MTPLX_PIDFILE = _SPEC.pidfile
MTPLX_LOG = _SPEC.log
MTPLX_DIRECT_URL = f"{_SPEC.default_origin}/v1/chat/completions"
