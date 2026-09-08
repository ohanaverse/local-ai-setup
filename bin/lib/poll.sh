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
