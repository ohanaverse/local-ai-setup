#!/opt/homebrew/bin/bash
# benchmark-multi.sh — shared pass-loop/cooldown/results-listing logic for
# the qwen3.8-benchmark-multi and ornith-1.5-benchmark-multi wrapper
# scripts. Sourced, not executed directly.

# Run PASSES invocations of a benchmark script (looked up next to this lib
# file's parent directory), with a cool-down between passes, then list the
# result files written under /tmp.
#
# Usage: run_benchmark_multi <script-basename> <passes> <max_tokens> <cooldown>
run_benchmark_multi() {
    local script_name="$1" passes="$2" max_tokens="$3" cooldown="$4"
    local lib_dir
    lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local script_path="$lib_dir/../$script_name"

    echo "Running $passes passes with max_tokens=$max_tokens, ${cooldown}s cool-down between passes"
    echo

    local i
    for i in $(seq 1 "$passes"); do
        echo "===== Pass $i / $passes ====="
        "$script_path" "$max_tokens"
        echo
        if [ "$i" -lt "$passes" ]; then
            echo "Cooling down for ${cooldown}s..."
            sleep "$cooldown"
        fi
    done

    echo
    echo "===== All $passes passes complete ====="
    echo "Latest result: $(ls -t "/tmp/${script_name}-"*.md | head -1)"
    echo "All results:"
    ls -lt "/tmp/${script_name}-"*.md | head -"$passes"
}
