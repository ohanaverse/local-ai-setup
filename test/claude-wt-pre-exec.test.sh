#!/usr/bin/env bash
# Regression test: wt_pre_exec returns early when model mode is active.
#
# claude-wt is the only launcher with a session-resume hook (wt_pre_exec).
# Without the early return, launching with --code/--design/--native would
# still prompt to resume a prior session, and the resumed session would
# ignore the requested model selection. This test verifies the fix.
#
# Run: ./test/claude-wt-pre-exec.test.sh

set -euo pipefail

pass=0
fail=0

# --- Mocks for wt-core.sh functions used by wt_pre_exec ---
# shellcheck disable=SC2329
wt_config_dir() { printf '%s' "/tmp/fake-config"; }

# Track whether find_latest_session is ever called.
# shellcheck disable=SC2329
find_latest_session_called=0
# shellcheck disable=SC2329
find_latest_session() {
  find_latest_session_called=1
  printf '%s\t%s\n' "session-id" "1234567890"
}

# Must return empty so wt_pre_exec hits `[[ -z "$choice" ]] && return`
# rather than falling through to `exec claude --resume`.
# shellcheck disable=SC2329
session_resume_prompt() {
  printf '%s\n' ""
}

# --- Extract the real wt_pre_exec from bin/claude-wt ---
eval "$(sed -n '/^wt_pre_exec() {/,/^}$/p' bin/claude-wt)"

# --- Test helper ---
assert_early_return() {
  local label="$1" cwd="${2:-0}" mode="${3:-}" native="${4:-0}"

  find_latest_session_called=0
  WT_CWD="$cwd"
  WT_MODEL_MODE="$mode"
  WT_NATIVE="$native"

  # Run wt_pre_exec; output is discarded.
  # We only care that it never reaches find_latest_session.
  wt_pre_exec "/tmp/fake-path" >/dev/null 2>&1 || true

  if [[ $find_latest_session_called -eq 0 ]]; then
    echo "  PASS: $label"
    pass=$((pass + 1))
  else
    echo "  FAIL: $label — find_latest_session was called (expected early return)"
    fail=$((fail + 1))
  fi
}

echo "=== claude-wt wt_pre_exec early-return tests ==="

assert_early_return "WT_MODEL_MODE=design" 0 "design" 0
assert_early_return "WT_MODEL_MODE=code" 0 "code" 0
assert_early_return "WT_NATIVE=1" 0 "" 1
assert_early_return "WT_CWD=1" 1 "" 0

echo ""
echo "Results: $pass passed, $fail failed"
exit $fail
