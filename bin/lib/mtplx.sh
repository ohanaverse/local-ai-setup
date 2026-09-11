#!/bin/bash
# mtplx.sh — shared MTPLX stop helper for llm-isolate-provider and
# llm-restore-providers (mirrors mlx-lm-server.sh's mlx_lm_server_stop: one
# stop implementation shared by both scripts instead of two copies that can
# silently drift — a change to the stop invocation, e.g. grace-seconds or
# port, previously had to be made in both places).
#
# Usage:
#   source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mtplx.sh"
#   mtplx_stop

# shellcheck source=poll.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/poll.sh"

MTPLX_STOP_PORT=8003

# Stop MTPLX via `mtplx stop --grace-seconds 10`, tolerating "nothing
# running" (mtplx stop exits 0 either way), then poll (bounded, 5 tries) for
# the port to actually close — a warning, not a failure, if it doesn't:
# neither caller can afford to abort its whole run over a stuck mtplx stop.
mtplx_stop() {
    silence_stdout mtplx stop --port "$MTPLX_STOP_PORT" --grace-seconds 10 || true
    wait_for_port_closed "http://localhost:$MTPLX_STOP_PORT/v1/models" 5 \
        || echo "warning: mtplx still listening on port $MTPLX_STOP_PORT" >&2
}
