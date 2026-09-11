#!/bin/bash
# poll.sh — shared "retry until condition or timeout" primitive for the
# provider isolation/restore helpers (bin/llm-isolate-provider,
# bin/llm-restore-providers). Sourced, not executed directly. Each of those
# scripts resolves its own path to this file (relative to its own location),
# so they stay independently runnable — neither depends on the other.

# Poll a URL until it responds (curl succeeds), sleeping INTERVAL seconds
# between attempts, up to TRIES times. Uses a 2s per-attempt curl timeout,
# matching bin/llm-restore-providers' original wait_for.
poll_until_up() {
    local url="$1" interval="$2" tries="$3"
    local i
    for ((i = 0; i < tries; i++)); do
        if curl -s -m 2 "$url" >/dev/null 2>&1; then
            return 0
        fi
        sleep "$interval"
    done
    return 1
}

# Poll a URL until it stops responding (curl fails), sleeping INTERVAL
# seconds between attempts, up to TRIES times. Uses a 1s per-attempt curl
# timeout, matching bin/llm-isolate-provider's original wait_for_port_closed.
poll_until_down() {
    local url="$1" interval="$2" tries="$3"
    local i
    for ((i = 0; i < tries; i++)); do
        if ! curl -s -m 1 "$url" >/dev/null 2>&1; then
            return 0
        fi
        sleep "$interval"
    done
    return 1
}

# Discard a command's stdout narration. Use for commands that print
# progress/status on stdout but whose output must not pollute a caller's
# stdout contract (e.g. `omlx stop` printing "Stopping `omlx`..." would
# corrupt the JSON contract of bin/llm-isolate-provider).
silence_stdout() {
    "$@" 1>/dev/null
}

# Poll until `url` stops responding or we hit the deadline (tries × 0.2s).
# Thin wrapper around poll_until_down with the interval used historically
# by the isolation/restore helpers after issuing a stop.
wait_for_port_closed() {
    local url="$1" tries="${2:-5}"
    poll_until_down "$url" 0.2 "$tries"
}
