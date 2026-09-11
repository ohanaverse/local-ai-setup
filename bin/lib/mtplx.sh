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

# Stop MTPLX via `mtplx stop --grace-seconds 10`, tolerating "nothing
# running" (mtplx stop exits 0 either way), then poll (bounded, 5 tries) for
# the port to actually close — a warning, not a failure, if it doesn't:
# neither caller can afford to abort its whole run over a stuck mtplx stop.
mtplx_stop() {
    # Port 8003 is mtplx's fixed serve port. modelman's single source for
    # it is modelman/providers/mtplx.py (MTPLX_PORT); this bash constant
    # and wt's localgate.go each carry the number once — keep the three
    # in lockstep when it ever moves.
    local port=8003
    silence_stdout mtplx stop --port "$port" --grace-seconds 10 || true
    wait_for_port_closed "http://localhost:$port/v1/models" 5 \
        || echo "warning: mtplx still listening on port $port" >&2
}
