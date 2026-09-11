#!/opt/homebrew/bin/bash
# benchmark-common.sh — shared helpers for the qwen3.8-benchmark and
# ornith-1.5-benchmark scripts. Sourced, not executed directly.
#
# Expects the sourcing script to have already set MAX_TOKENS and PROMPT
# (used by build_payload), and to set OUTFILE before calling run_streaming
# or write_table_header. isolate_one expects ISOLATE_HELPER, ISOLATE_ID,
# ISOLATE_ENV, and DIRECT_MODELS to be set by the sourcing script (each
# script keeps its own copies of these — they differ per backend set).
# ISOLATE_EXTRA is optional: a sourcing script may set
# ISOLATE_EXTRA[key]="second-positional-arg" for a future backend whose
# no-ISOLATE_ENV branch needs more than one positional arg after the model
# (e.g. a draft-model repo); every current backend leaves it unset, which
# isolate_one treats as no extra args.

BENCHMARK_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

declare -A ISOLATE_EXTRA

# Build JSON payload via Python
build_payload() {
    local model="$1"
    local stream="$2"
    python3 - "$model" "$stream" "$MAX_TOKENS" "$PROMPT" <<'PYEOF'
import json, sys
model, stream, max_tokens, prompt = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
stream_bool = stream == "true"
payload = {
    "model": model,
    "messages": [{"role": "user", "content": prompt}],
    "max_tokens": max_tokens,
    "stream": stream_bool,
    "temperature": 0.0
}
if stream_bool:
    payload["stream_options"] = {"include_usage": True}
print(json.dumps(payload))
PYEOF
}

# --- Service management ----------------------------------------------------

# Stop all local providers, then start+warmup only the requested backend's
# model (the helper polls until the model actually answers). A backend with
# no ISOLATE_ENV entry (e.g. mtplx) is single-model-per-process and takes its
# model as a positional arg instead of an env var (bin/llm-isolate-provider's
# mtplx case reads $2) — forward DIRECT_MODELS[$key] positionally so
# isolation selects the model this run actually requested, instead of
# falling back to registry resolution: silently the wrong weights if some
# other mtplx entry happens to be the registry's single match, or an
# outright refusal once the registry holds more than one.
# ISOLATE_EXTRA[$key], if set, is word-split and appended after the model —
# a future backend needing a second positional arg (e.g. a draft model) sets
# it; every current backend leaves it unset/empty.
isolate_one() {
    local key="$1"
    echo "  [isolation] isolating ${ISOLATE_ID[$key]} (${DIRECT_MODELS[$key]})..."
    if [ -n "${ISOLATE_ENV[$key]:-}" ]; then
        env "${ISOLATE_ENV[$key]}=${DIRECT_MODELS[$key]}" \
            "$ISOLATE_HELPER" "${ISOLATE_ID[$key]}" >/dev/null
    else
        # ISOLATE_EXTRA[$key] is deliberately word-split below: it's a
        # space-separated list of additional positional args, empty for
        # every current single-arg backend.
        # shellcheck disable=SC2086
        "$ISOLATE_HELPER" "${ISOLATE_ID[$key]}" "${DIRECT_MODELS[$key]}" ${ISOLATE_EXTRA[$key]:-} >/dev/null
    fi
}

# Ensure all local services are running (called at script start). Delegates
# to the already-idempotent bin/llm-restore-providers (it no-ops per-service
# when already up) rather than duplicating its start/poll logic here.
#
# Best-effort, like the inline version this replaced: llm-restore-providers
# exits 1 when any one service fails its health poll, and these scripts run
# under set -e, so an unchecked call would abort the whole run (including
# the OpenRouter rows that need no local service) leaving a header-only
# result file. A degraded run — some local rows erroring — still beats a
# zero-row run, so warn and continue instead.
ensure_all_local_started() {
    echo "[setup] ensuring all local services are running..."
    if ! "$BENCHMARK_LIB_DIR/../../bin/llm-restore-providers"; then
        echo "[setup] WARNING: one or more local providers failed to start; " \
            "affected rows will error out, cloud rows still run" >&2
    fi
}

# --- Benchmark runner --------------------------------------------------------

run_streaming() {
    local label="$1"
    local url="$2"
    local model="$3"
    local auth="${4:-}"

    local payload
    payload=$(build_payload "$model" "true")
    local tmpfile
    tmpfile=$(mktemp)

    local start_ns
    start_ns=$(date +%s%N)
    local first_token_ns=""

    local auth_args=()
    if [ -n "$auth" ]; then
        auth_args=(-H "Authorization: Bearer $auth")
    fi

    curl -s -m 600 -N "$url" \
        -H "Content-Type: application/json" \
        "${auth_args[@]}" \
        -d "$payload" > "$tmpfile" 2>&1 &
    local pid=$!

    while kill -0 "$pid" 2>/dev/null; do
        local current_size
        current_size=$(wc -c < "$tmpfile" 2>/dev/null | tr -d ' ' || echo 0)
        if [ -z "$first_token_ns" ] && [ "$current_size" -gt 50 ]; then
            first_token_ns=$(date +%s%N)
        fi
        sleep 0.02
    done

    wait "$pid" || true

    # Detect error responses (auth failure, provider 429/5xx, etc.)
    if grep -q '"error"' "$tmpfile"; then
        local errmsg
        errmsg=$(grep -oE '"message": *"[^"]*"' "$tmpfile" | head -1)
        echo "  $label: ERROR ${errmsg:-unknown}"
        echo "| $label | N/A | N/A | N/A | N/A |" >> "$OUTFILE"
        rm -f "$tmpfile"
        return 0
    fi

    local end_ns
    end_ns=$(date +%s%N)

    local duration_ms=$(( (end_ns - start_ns) / 1000000 ))
    local ttft_ms="N/A"
    if [ -n "$first_token_ns" ]; then
        ttft_ms=$(( (first_token_ns - start_ns) / 1000000 ))
    fi

    # Both patterns tolerate an optional space after the colon: ollama/omlx
    # emit compact JSON ("completion_tokens":30), while mtplx's serializer
    # inserts a space ("completion_tokens": 30) — a no-space-only pattern
    # silently reports tokens=0 for any backend using the spaced style.
    local token_count
    token_count=$(grep -oE '"completion_tokens": *[0-9]+' "$tmpfile" | tail -1 | grep -oE '[0-9]+' || echo "0")

    if [ "$token_count" = "0" ]; then
        token_count=$(grep -E '"content": *"[^"]' "$tmpfile" | wc -l | tr -d ' ')
    fi

    local throughput="N/A"
    if [ "${token_count:-0}" -gt 0 ] 2>/dev/null && [ "$duration_ms" -gt 0 ]; then
        # ttft_ms can be "N/A" when no poll sample ever saw >50 bytes (a
        # backend that buffers the whole reply into one burst, a tiny
        # max_tokens smoke run, ...). Arithmetic on the string would abort
        # run_streaming under set -e (row and remaining backends silently
        # dropped), so fall back to the full duration: the resulting
        # throughput is then total-time based, not gen-time based.
        local gen_time_ms="$duration_ms"
        if [ "$ttft_ms" != "N/A" ]; then
            gen_time_ms=$(( duration_ms - ttft_ms ))
        fi
        if [ "$gen_time_ms" -gt 0 ]; then
            throughput=$(python3 -c "print(f'{int($token_count) / ($gen_time_ms/1000):.2f}')")
        fi
    fi

    echo "  $label: TTFT=${ttft_ms}ms, total=${duration_ms}ms, tokens=${token_count}, throughput=${throughput} tok/s"
    echo "| $label | $ttft_ms | $duration_ms | $token_count | $throughput |" >> "$OUTFILE"

    rm -f "$tmpfile"
}

# Append a "## <title>" heading followed by the standard results-table header
# to $OUTFILE. Callers add any blank-line separation they need before calling
# this (some sections need a leading blank line, some don't, depending on
# what was written immediately before).
write_table_header() {
    local title="$1"
    {
        echo "## $title"
        echo
        echo "| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |"
        echo "|---------|----------:|-----------:|-------:|-------------------:|"
    } >> "$OUTFILE"
}
