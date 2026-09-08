#!/bin/bash
# mlx-lm-resolve.sh — locate mlx_lm.* tools shipped inside the omlx
# Homebrew keg.
#
# Usage:
#   source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mlx-lm-resolve.sh"
#   server_bin=$(resolve_mlx_lm_bin server) || exit 1
#   convert_bin=$(resolve_mlx_lm_bin convert) || exit 1
#
# The mlx_lm.* console-script shims (mlx_lm.server, mlx_lm.convert, ...) are
# not on PATH anywhere on this machine, and mlx-lm is not a declared
# dependency of this repo (no mention in modelman/pyproject.toml or
# elsewhere) — the only real, working copies live inside the versioned omlx
# Homebrew keg, built against that keg's own pinned Python/mlx versions.
# Nothing else on the system is guaranteed to work, so we don't fall back to
# a bare `command -v mlx_lm.<tool>`. The keg version (e.g. 0.6.4) drifts on
# `brew upgrade omlx`, so it must never be hardcoded — always glob for it and
# take the newest match.
resolve_mlx_lm_bin() {
    local tool="$1"

    if [ -n "$MLX_LM_BIN_DIR" ] && [ -x "$MLX_LM_BIN_DIR/mlx_lm.$tool" ]; then
        echo "$MLX_LM_BIN_DIR/mlx_lm.$tool"
        return 0
    fi

    # nullglob (scoped to this function via a subshell-free save/restore) so
    # a no-match glob doesn't fall through as its own literal string.
    local had_nullglob=0
    shopt -q nullglob && had_nullglob=1
    shopt -s nullglob
    local candidates=(/opt/homebrew/Cellar/omlx/*/libexec/bin/"mlx_lm.$tool")
    [ "$had_nullglob" -eq 0 ] && shopt -u nullglob

    if [ "${#candidates[@]}" -gt 0 ]; then
        local newest
        newest=$(printf '%s\n' "${candidates[@]}" | sort -V | tail -n 1)
        if [ -x "$newest" ]; then
            echo "$newest"
            return 0
        fi
    fi

    echo "omlx not found — install via 'brew install omlx', or set MLX_LM_BIN_DIR to a directory containing mlx_lm.* tools" >&2
    return 1
}
