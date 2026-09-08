#!/bin/bash
# mlx-lm-server.sh — pidfile-based process management for a backgrounded
# `mlx_lm.server` subprocess.
#
# `mlx_lm.server` is fundamentally unlike the other local providers (ollama,
# omlx): it is not an always-on multi-model daemon you can point at a
# different model on the fly, it's a plain process bound to exactly one
# target+draft pairing for its whole lifetime. Sweeping many pairings across
# a benchmark session means starting and stopping this process repeatedly,
# so — unlike `launchctl`-managed services elsewhere in this repo — there is
# no OS-level service manager to ask "is it running" / "stop it". A pidfile
# is the minimal mechanism that answers both questions across separate
# invocations of this script's functions.
#
# Usage:
#   source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mlx-lm-server.sh"
#   mlx_lm_server_start "$target_repo" "$draft_repo" 8001
#   mlx_lm_server_stop

# shellcheck source=mlx-lm-resolve.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/mlx-lm-resolve.sh"
# shellcheck source=poll.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/poll.sh"

MLX_LM_SERVER_PIDFILE="/tmp/local-ai-setup-mlx-lm-server.pid"
MLX_LM_SERVER_LOG="/tmp/local-ai-setup-mlx-lm-server.log"

# Start `mlx_lm.server` for one target+draft pairing, backgrounded, and
# record its pid. Does not poll for readiness — the caller (llm-isolate-
# provider's start_mlx_lm_server) owns warmup, since readiness semantics
# (which URL, how long to wait) are an isolate-script concern, not a process-
# management one.
#
# Unlike ollama/omlx (real daemons where issuing "start" again is a safe
# no-op — the daemon just keeps serving whatever it already had loaded),
# `mlx_lm.server` is a plain subprocess with no such awareness: a second
# spawn while the first is still bound to `port` either fails to bind
# outright, or — worse — leaves warmup's health check passing against the
# *old* process (possibly the wrong target+draft pairing) while the pidfile
# gets overwritten to point at the new, half-dead one, orphaning the
# original. Unconditionally stopping any prior instance before spawning
# makes every call to this function idempotent: calling it twice in a row
# (same pairing or a different one) always ends with exactly one live
# server, correctly tracked by the pidfile.
#
# Both stdout AND stderr are redirected to $MLX_LM_SERVER_LOG, never left to
# inherit the caller's stdout. bin/llm-isolate-provider's stdout is a strict
# JSON contract parsed by isolation.py — a single stray line from this
# server (banner, warning, traceback) landing on that stdout breaks every
# isolate call, not just mlx_lm_server's. See the `silence_stdout` comment in
# bin/llm-isolate-provider for the full story of why this repo is strict
# about it.
mlx_lm_server_start() {
    local target="$1" draft="$2" port="${3:-8001}"
    local bin
    bin=$(resolve_mlx_lm_bin server) || return 1

    mlx_lm_server_stop
    # Bounded wait for the OS to actually reclaim the port before rebinding
    # it — `kill` above only sends SIGTERM, it doesn't wait for the process
    # to exit. A timeout here is a warning, not fatal: worst case the new
    # process fails to bind and warmup_or_die (in the isolate script) times
    # out with a clear error, rather than this function hanging.
    poll_until_down "http://localhost:$port/v1/models" 0.2 15 \
        || echo "warning: port $port still in use after stopping prior mlx_lm_server" >&2

    "$bin" --model "$target" --draft-model "$draft" --port "$port" \
        >"$MLX_LM_SERVER_LOG" 2>&1 &
    echo "$!" >"$MLX_LM_SERVER_PIDFILE"
}

# Stop a previously-started `mlx_lm.server`, tolerating a missing pidfile
# (nothing to stop — silent success) and a stale pid (process already dead,
# e.g. it crashed or was killed out-of-band) so this is safe to call
# unconditionally from both llm-isolate-provider's stop_all_local and
# llm-restore-providers, every time, whether or not mlx_lm_server was ever
# started this session.
mlx_lm_server_stop() {
    [ -f "$MLX_LM_SERVER_PIDFILE" ] || return 0

    local pid
    pid=$(cat "$MLX_LM_SERVER_PIDFILE" 2>/dev/null)
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
    fi
    rm -f "$MLX_LM_SERVER_PIDFILE"
}
