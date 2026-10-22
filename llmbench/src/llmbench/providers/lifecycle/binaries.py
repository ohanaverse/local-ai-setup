"""Binary resolution helpers, ported from bash.

`resolve_mlx_lm_bin` is a second, independent implementation of
`bin/lib/mlx-lm-resolve.sh`'s `resolve_mlx_lm_bin` function — that shell
script is NOT deleted by this port (`bin/mlx-quantize` still sources it
directly), so this Python copy must be kept in lockstep by comment with
the bash original (same convention as `MTPLX_PORT` being duplicated in
`wt/internal/localmodels/inventory.go`).
"""

from __future__ import annotations

import glob
import os
import re
import shutil

from .envelope import LifecycleError


def require_binary(name: str) -> str:
    """`shutil.which(name)`, or raise LifecycleError if not found on PATH."""
    path = shutil.which(name)
    if path is None:
        raise LifecycleError(f"{name} binary not found on PATH")
    return path


def _version_key(path: str) -> tuple[int, ...]:
    """Extract the Cellar version segment (the path component right after
    'omlx/') and turn it into a numeric tuple for sorting — 'sort -V'
    semantics, so 0.10.0 sorts after 0.9.0 (a plain string sort would put
    0.10.0 before 0.9.0)."""
    parts = path.split(os.sep)
    try:
        idx = parts.index("omlx")
        version_segment = parts[idx + 1]
    except (ValueError, IndexError):
        version_segment = ""
    return tuple(int(p) for p in re.findall(r"\d+", version_segment))


def resolve_mlx_lm_bin(tool: str) -> str:
    """Resolve the mlx_lm.<tool> binary the same way bash's
    resolve_mlx_lm_bin does:

    1. If $MLX_LM_BIN_DIR is set and <dir>/mlx_lm.<tool> exists and is
       executable, return it.
    2. Else glob `/opt/homebrew/Cellar/omlx/*/libexec/bin/mlx_lm.<tool>`,
       sort matches by their Cellar version segment (numeric-tuple
       comparison, NOT lexicographic string comparison — 'sort -V'
       semantics: 0.10.0 sorts after 0.9.0), skip non-executable matches,
       and return the highest-versioned executable match.
    3. Else raise LifecycleError("omlx not found — install via 'brew
       install omlx', or set MLX_LM_BIN_DIR to a directory containing
       mlx_lm.* tools") — bash's exact message text.
    """
    bin_dir = os.environ.get("MLX_LM_BIN_DIR")
    if bin_dir:
        candidate = os.path.join(bin_dir, f"mlx_lm.{tool}")
        if os.access(candidate, os.X_OK):
            return candidate

    matches = [
        path
        for path in glob.glob(f"/opt/homebrew/Cellar/omlx/*/libexec/bin/mlx_lm.{tool}")
        if os.access(path, os.X_OK)
    ]
    if matches:
        return max(matches, key=_version_key)

    raise LifecycleError(
        "omlx not found — install via 'brew install omlx', or set "
        "MLX_LM_BIN_DIR to a directory containing mlx_lm.* tools"
    )
