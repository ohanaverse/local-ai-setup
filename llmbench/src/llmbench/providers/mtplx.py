"""The mtplx serve port and the base URLs derived from it.

wt has its own copy in `wt/internal/localmodels` (FamilyOrigin's default-port
table). The two agree through registry.toml's `auth.base_url`, not imports.
"""

MTPLX_PORT = 8003
MTPLX_BASE = f"http://localhost:{MTPLX_PORT}"
MTPLX_V1_BASE = f"{MTPLX_BASE}/v1"
