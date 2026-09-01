#!/usr/bin/env bash
# agents-smoke.sh -- live one-shot smoke test for every wt agent.
#
# The run covers every gateway mode in --modes (default direct,litellm) by
# flipping the [gateway].mode field of wt's own config.toml between passes
# and restoring the original afterwards. Native rows and shell are gateway
# mode-invariant and run once, in the first pass.

set -o nounset -o pipefail

DEFAULT_TIMEOUT=180
DEFAULT_OLLAMA_MODEL="ollama/glm-5.3-flash:cloud"
DEFAULT_MODES="direct,litellm"

# agent|model|one-shot-args-template|comment
# model "-" means launch without -M.
# @PROMPT@ is replaced with the row's prompt (a single shell-quoted argument).
read -r -d '' MATRIX <<'EOF' || true
claude|claude/native|-p @PROMPT@|own subscription, no model args
claude|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|gateway round-trip
codex|DEFAULT_OLLAMA_MODEL|exec @PROMPT@|
copilot|copilot/native|-p @PROMPT@|own subscription
copilot|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|
opencode|DEFAULT_OLLAMA_MODEL|run @PROMPT@|
pi|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|
agy|agy/native|-p @PROMPT@|native model; driver ignores it
shell|-|echo @PROMPT@|-- passthrough becomes argv
EOF

build_prompt() {
  local agent="$1" runid="$2"
  echo "Reply with exactly this text and nothing else: WT-SMOKE-${agent}-${runid}"
}

print_cmd() {
  local i=0 arg
  for arg in "$@"; do
    [[ $i -gt 0 ]] && printf ' '
    printf '%q' "$arg"
    i=$((i + 1))
  done
  printf '\n'
}

build_cmd() {
  local agent="$1" model="$2" args_template="$3" prompt="$4"
  local cmd=(wt --cwd -A "$agent")
  if [[ "$model" != "-" ]]; then
    cmd+=(-M "$model")
  fi
  cmd+=(--)
  # Split the template on whitespace and substitute @PROMPT@ with the prompt as
  # a single array element. No eval: the template is treated as literal tokens,
  # so a future row can never smuggle shell metacharacters into the command.
  local token
  for token in $args_template; do
    if [[ "$token" == "@PROMPT@" ]]; then
      cmd+=("$prompt")
    else
      cmd+=("$token")
    fi
  done
  print_cmd "${cmd[@]}"
}

display_model() {
  local m="$1"
  [[ "$m" == "-" ]] && echo "default" || echo "$m"
}

# is_mode_invariant reports whether a row's launch behavior is identical in
# every gateway mode: native models clear all gateway env (the subscription
# wins) and shell execs argv directly, so neither consults [gateway].
# Mode-invariant rows run exactly once, in the first pass.
is_mode_invariant() {
  local agent="$1" model="$2"
  [[ "$agent" == "shell" || "$model" == */native ]]
}

# with_timeout runs the row command under a TIMEOUT-second alarm.
# gtimeout needs --foreground: by default it puts the child in its own
# process group, so a terminal Ctrl-C (delivered to the foreground group)
# never reaches the running agent, gtimeout neither dies nor forwards, and
# the script's INT trap is deferred forever — leaving the flipped gateway
# config unrestored. --foreground keeps the child in the terminal's group;
# Ctrl-C kills the row, the trap fires, and the EXIT trap restores the
# config. (Documented gtimeout caveat: with --foreground, grandchildren of
# the timed command are not themselves timed out — an orphaned agent can
# outlive a timed-out row, the same profile as without the flag.)
# The perl fallback execs the command (replacing perl), so the whole chain
# stays in one group and needs no equivalent flag.
with_timeout() {
  local secs="$1"
  shift
  if command -v gtimeout >/dev/null 2>&1; then
    gtimeout --foreground "$secs" "$@"
  elif command -v perl >/dev/null 2>&1; then
    perl -e 'alarm shift; exec @ARGV' "$secs" "$@"
  else
    echo "warning: no gtimeout/perl; running without timeout" >&2
    "$@"
  fi
}

preflight() {
  if ! command -v wt >/dev/null 2>&1; then
    echo "error: wt not found on PATH" >&2
    exit 1
  fi
  if ! wt --version >/dev/null 2>&1; then
    echo "error: wt --version failed" >&2
    exit 1
  fi
}

# ── Gateway-mode flipping ─────────────────────────────────────────────
# The config file is located via `wt config path` so the script and wt
# always agree on the config directory (XDG_CONFIG_HOME and ~ expansion
# included) — the script never duplicates wt's precedence rules. Only the
# [gateway] mode line is rewritten; url/api_key and every other section
# stay verbatim, and the original is restored on any exit path.

CONFIG_FILE=""
CONFIG_BAK=""

# config_file_path resolves wt's config.toml via `wt config path`. Fails
# loudly when wt cannot answer (wt config path prints config.Dir() without
# loading config.toml, so a broken config still resolves here).
config_file_path() {
  local dir
  dir=$(wt config path) || {
    echo "error: 'wt config path' failed; cannot locate config.toml" >&2
    return 1
  }
  printf '%s\n' "${dir}/config.toml"
}

# snapshot_config backs up the original config once, before any flip.
# Refuses to run when a backup already exists: a leftover backup means a
# previous run died between snapshot and restore, and overwriting it could
# destroy the only copy of the user's original config.
snapshot_config() {
  if [[ ! -f "$CONFIG_FILE" ]]; then
    echo "error: $CONFIG_FILE not found; wt needs a config.toml to flip gateway modes" >&2
    return 1
  fi
  if [[ -e "$CONFIG_BAK" ]]; then
    echo "error: stale backup $CONFIG_BAK exists; a previous run may have died mid-run." >&2
    echo "       Inspect it, restore it by hand to $CONFIG_FILE if it holds your config, then remove it." >&2
    return 1
  fi
  cp -p "$CONFIG_FILE" "$CONFIG_BAK"
}

# restore_config returns the user's config.toml from the backup. Idempotent:
# safe to call from both the explicit restore and the EXIT trap.
restore_config() {
  if [[ -n "$CONFIG_BAK" && -f "$CONFIG_BAK" ]]; then
    mv "$CONFIG_BAK" "$CONFIG_FILE"
    CONFIG_BAK=""
  fi
}

# set_gateway_mode rewrites only the [gateway] mode line in CONFIG_FILE to
# "direct" or "litellm". The temp file first inherits the original's
# permissions via cp -p, then awk truncates and rewrites it, so the swapped-
# in file keeps the original's mode/ownership without a stat/chown dance.
# Atomic mv (same directory). Fails when no [gateway] mode line exists: a
# section-less config keeps wt in direct mode by default and the script
# cannot flip what is not written.
set_gateway_mode() {
  local mode="$1"
  local tmp
  tmp=$(mktemp "${CONFIG_FILE}.XXXXXX") || return 1
  if ! cp -p "$CONFIG_FILE" "$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  if ! awk -v mode="$mode" '
    BEGIN { ingw = 0 }
    /^\[gateway\]$/ { ingw = 1; print; next }
    /^\[/ { ingw = 0 }
    ingw && /^mode[[:space:]]*=/ { sub(/^mode[[:space:]]*=.*/, "mode = \"" mode "\"") }
    { print }
  ' "$CONFIG_FILE" >"$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  if ! grep -q "^mode = \"${mode}\"" "$tmp"; then
    echo "error: cannot flip gateway mode: no [gateway] mode line in $CONFIG_FILE" >&2
    rm -f "$tmp"
    return 1
  fi
  mv "$tmp" "$CONFIG_FILE"
}

# gateway_field reads one field from the [gateway] section (mode, url, or
# api_key), stripping TOML quoting. Prints nothing when absent. The dynamic
# regex "^<field>[[:space:]]*=" matches the same key forms as
# set_gateway_mode's rewrite rule, so read and write stay in lockstep.
gateway_field() {
  local field="$1"
  awk -v f="$field" '
    BEGIN { ingw = 0 }
    /^\[gateway\]$/ { ingw = 1; next }
    /^\[/ { ingw = 0 }
    ingw && $0 ~ ("^" f "[[:space:]]*=") {
      line = $0
      sub(/^[^=]*=[[:space:]]*/, "", line)
      gsub(/"/, "", line)
      print line
      exit
    }
  ' "$CONFIG_FILE"
}

# current_gateway_mode prints the effective gateway mode: the [gateway]
# mode value, or "direct" when the section or key is absent (wt's IsDirect
# treats "" and "direct" alike).
current_gateway_mode() {
  local mode
  mode=$(gateway_field "mode")
  [[ -z "$mode" ]] && mode="direct"
  printf '%s\n' "$mode"
}

# require_litellm_credentials verifies [gateway] carries url and api_key: a
# litellm pass without credentials fails every row with connection errors
# that read as agent bugs. Failing fast beats a wall of misleading FAILs.
require_litellm_credentials() {
  local url key
  url=$(gateway_field "url")
  key=$(gateway_field "api_key")
  if [[ -z "$url" || -z "$key" ]]; then
    echo "skip: [gateway] lacks url/api_key; the litellm pass would fail every row (see $CONFIG_FILE)" >&2
    return 1
  fi
}

# ── Matrix row execution ───────────────────────────────────────────────

dry_run() {
  local runid
  runid="$(date +%s)-$RANDOM"
  while IFS='|' read -r agent model args_template comment; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    should_run_agent "$agent" || continue
    model="${model/DEFAULT_OLLAMA_MODEL/$DEFAULT_OLLAMA_MODEL}"
    local prompt
    prompt=$(build_prompt "$agent" "$runid")
    echo "--- $agent × $(display_model "$model") ---"
    build_cmd "$agent" "$model" "$args_template" "$prompt"
  done <<<"$MATRIX"
}

run_row() {
  local agent="$1" model="$2" args_template="$3" comment="$4" runid="$5"
  local prompt result status elapsed
  prompt=$(build_prompt "$agent" "$runid")
  local cmdline
  cmdline=$(build_cmd "$agent" "$model" "$args_template" "$prompt")

  local start end
  start=$(date +%s)
  result=$(with_timeout "$TIMEOUT" bash -c "$cmdline" </dev/null 2>&1) && status=0 || status=$?
  end=$(date +%s)
  elapsed=$((end - start))

  local verdict="FAIL"
  # SKIP classifier: wt emits "agent <bin> not installed" (internal/agents/agents.go)
  # when the agent binary is missing. Match only that exact message — generic
  # substrings like "command not found" or "No such file" also appear in real
  # agent failures and must not be masked as SKIP.
  if [[ $status -ne 0 ]] && [[ "$result" == *"agent ${agent} not installed"* ]]; then
    verdict="SKIP"
  elif [[ $status -eq 0 ]] && [[ "$result" == *"WT-SMOKE-${agent}-${runid}"* ]]; then
    verdict="PASS"
  fi

  printf '[%-4s] %-10s × %-28s (%ss)\n' "$verdict" "$agent" "$(display_model "$model")" "$elapsed"
  if [[ "$verdict" == "FAIL" ]]; then
    echo "  command: $cmdline"
    echo "  output:"
    while IFS= read -r line; do
      printf '    %s\n' "$line"
    done <<<"$result"
  fi
  case "$verdict" in
    PASS) return 0 ;;
    SKIP) return 2 ;;
    *) return 1 ;;
  esac
}

# run_tests_for_mode runs the matrix rows under the currently configured
# gateway mode. Mode-invariant rows (native, shell) run only when
# is_first_pass is "yes" — they already ran under the first mode.
run_tests_for_mode() {
  local mode="$1" is_first_pass="$2"
  local runid rc fail_count=0 skip_count=0 total=0
  runid="$(date +%s)-$RANDOM"
  echo ""
  echo "=== Agents Smoke Run (gateway=${mode}, timeout=${TIMEOUT}s, runid=${runid}) ==="
  while IFS='|' read -r agent model args_template comment; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    should_run_agent "$agent" || continue
    if is_mode_invariant "$agent" "$model" && [[ "$is_first_pass" != "yes" ]]; then
      # Mode-invariant row, already covered by the first pass.
      continue
    fi
    model="${model/DEFAULT_OLLAMA_MODEL/$DEFAULT_OLLAMA_MODEL}"
    run_row "$agent" "$model" "$args_template" "$comment" "$runid"
    rc=$?
    if [[ $rc -eq 1 ]]; then ((fail_count++)); fi
    if [[ $rc -eq 2 ]]; then ((skip_count++)); fi
    ((total++)) || true
  done <<<"$MATRIX"
  echo "=== PASS: $((total - fail_count - skip_count)) FAIL: $fail_count SKIP: $skip_count (gateway=${mode}) ==="
  [[ $fail_count -eq 0 ]]
}

# ── Argument parsing ──────────────────────────────────────────────────

usage() {
  cat <<'USAGE'
Usage: scripts/agents-smoke.sh [options]
  (no args)       Run the live agent smoke tests in both gateway modes
                  (direct then litellm; config.toml is restored afterwards)
  --list          Print the test matrix and exit
  --dry-run       Print the wt command for each row and exit
  --only AGENTS   Comma-separated agents to run (default all)
  --modes MODES   Comma-separated gateway modes to run (default direct,litellm).
                  "current" runs only the mode already configured.
  --timeout SECS  Per-row timeout (default 180)
  -h, --help      Show this help
USAGE
}

list_rows() {
  printf '%-10s %-30s %s\n' "AGENT" "MODEL" "COMMENT"
  while IFS='|' read -r agent model _args comment; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    should_run_agent "$agent" || continue
    model="${model/DEFAULT_OLLAMA_MODEL/$DEFAULT_OLLAMA_MODEL}"
    printf '%-10s %-30s %s\n' "$agent" "$(display_model "$model")" "$comment"
  done <<<"$MATRIX"
}

ONLY_AGENTS=""
TIMEOUT="$DEFAULT_TIMEOUT"
MODES="$DEFAULT_MODES"

parse_args() {
  ACTION=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --list)
        ACTION=list
        shift
        ;;
      --dry-run)
        ACTION=dry-run
        shift
        ;;
      --only)
        if [[ $# -lt 2 ]]; then
          echo "Option $1 requires a value" >&2
          usage >&2
          exit 1
        fi
        ONLY_AGENTS="$2"
        shift 2
        ;;
      --modes)
        if [[ $# -lt 2 ]]; then
          echo "Option $1 requires a value" >&2
          usage >&2
          exit 1
        fi
        MODES="$2"
        shift 2
        ;;
      --timeout)
        if [[ $# -lt 2 ]]; then
          echo "Option $1 requires a value" >&2
          usage >&2
          exit 1
        fi
        if [[ ! "$2" =~ ^[1-9][0-9]*$ ]]; then
          echo "error: --timeout requires a positive integer" >&2
          usage >&2
          exit 1
        fi
        TIMEOUT="$2"
        shift 2
        ;;
      -h | --help)
        usage
        exit 0
        ;;
      *)
        echo "unknown arg: $1" >&2
        usage >&2
        exit 1
        ;;
    esac
  done
}

known_agents() {
  local agents=""
  while IFS='|' read -r agent _ _ _; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    [[ "$agents" != *",${agent},"* ]] && agents+=",${agent},"
  done <<<"$MATRIX"
  echo "$agents"
}

validate_only_agents() {
  [[ -z "$ONLY_AGENTS" ]] && return 0
  local known
  known=$(known_agents)
  local IFS=',' token
  for token in $ONLY_AGENTS; do
    [[ -z "$token" ]] && continue
    if [[ "$known" != *",${token},"* ]]; then
      echo "error: unknown agent '${token}' in --only" >&2
      usage >&2
      exit 1
    fi
  done
}

# validate_modes checks the raw --modes value before expansion: each token
# must be direct, litellm, or current (resolved to the configured mode at
# expansion time).
validate_modes() {
  [[ -z "$MODES" ]] && {
    echo "error: --modes requires at least one mode" >&2
    exit 1
  }
  local IFS=',' token
  for token in $MODES; do
    [[ -z "$token" ]] && continue
    case "$token" in
      direct | litellm | current) ;;
      *)
        echo "error: unknown mode '${token}' in --modes (known: direct, litellm, current)" >&2
        exit 1
        ;;
    esac
  done
}

# expand_modes resolves "current" to the effective gateway mode and
# dedupes. "current" is the single-mode escape hatch that reproduces the
# pre-mode-pairing behavior.
expand_modes() {
  local IFS=','
  local out="" token cur
  cur=$(current_gateway_mode)
  for token in $MODES; do
    [[ -z "$token" ]] && continue
    [[ "$token" == "current" ]] && token="$cur"
    case ",$out," in
      *",$token,"*) ;; # already present
      *)
        [[ -z "$out" ]] && out="$token" || out+=",${token}"
        ;;
    esac
  done
  printf '%s\n' "$out"
}

should_run_agent() {
  local agent="$1"
  [[ -z "$ONLY_AGENTS" ]] && return 0
  local IFS=',' target
  for target in $ONLY_AGENTS; do
    [[ "$target" == "$agent" ]] && return 0
  done
  return 1
}

# ── Main ───────────────────────────────────────────────────────────────

main() {
  parse_args "$@"
  validate_only_agents
  validate_modes

  case "$ACTION" in
    list)
      list_rows
      exit 0
      ;;
    dry-run)
      dry_run
      exit 0
      ;;
  esac

  preflight

  # Everything below flips the gateway config: resolve the path and guard
  # the original before any flip. (list/dry-run never touch the config.)
  CONFIG_FILE=$(config_file_path) || exit 1
  CONFIG_BAK="${CONFIG_FILE}.agents-smoke.bak"
  snapshot_config || exit 1
  # Restore the original on any exit path. INT/TERM exit so the EXIT trap
  # fires the restore (a plain restore-and-return handler would let the
  # script continue after Ctrl-C).
  trap 'restore_config' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  MODES=$(expand_modes)
  local overall_rc=0 pass_idx=0
  local -a mode_list
  IFS=',' read -r -a mode_list <<<"$MODES"
  for mode in "${mode_list[@]}"; do
    if [[ "$mode" == "litellm" ]] && ! require_litellm_credentials; then
      echo "=== litellm pass skipped (no gateway credentials) ===" >&2
      continue
    fi
    if [[ "$mode" != "$(current_gateway_mode)" ]]; then
      set_gateway_mode "$mode" || exit 1
    fi
    ((pass_idx++)) || true
    run_tests_for_mode "$mode" "$([[ $pass_idx -eq 1 ]] && echo yes || echo no)" || overall_rc=1
  done
  restore_config
  echo "=== gateway config restored to original mode ==="
  exit $overall_rc
}

main "$@"
