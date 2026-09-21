#!/usr/bin/env bash
# agents-smoke.sh -- live one-shot smoke test for every wt agent.
#
# The run covers every routing mode in --modes (default direct,litellm) by
# flipping wt's routing mode (`wt litellm on|off`, which writes wt's own
# config.toml) between passes and restoring the original config afterwards. Native rows and shell are
# mode-invariant and run once, in the first pass.

set -o nounset -o pipefail

DEFAULT_TIMEOUT=180
DEFAULT_OLLAMA_MODEL="ollama/glm-5.3-flash:cloud"
DEFAULT_MODES="direct,litellm"

# agent|model|one-shot-args-template|comment|xfail
# model "-" means launch without -M.
# @PROMPT@ is replaced with the row's prompt (a single shell-quoted argument).
# Optional 5th field marks a known-failing row: "xfail" (fails in every
# routing mode) or "xfail:direct"/"xfail:litellm" (mode-scoped). A marked
# row that fails prints XFAIL and leaves the exit code alone; a marked row
# that unexpectedly passes prints XPASS — a loud stale-mark warning. The
# marks mirror the Known Limitations section of
# docs/reference/glm-5.3-flash-openrouter.md.
read -r -d '' MATRIX <<'EOF' || true
claude|claude/native|-p @PROMPT@|own subscription, no model args
claude|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|gateway round-trip
claude|openrouter/z-ai/glm-5.3-flash|-p @PROMPT@|OpenRouter GLM-5.3-Flash
codex|DEFAULT_OLLAMA_MODEL|exec @PROMPT@|
codex|openrouter/z-ai/glm-5.3-flash|exec @PROMPT@|OpenRouter GLM-5.3-Flash|xfail
copilot|copilot/native|-p @PROMPT@|own subscription
copilot|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|
copilot|openrouter/z-ai/glm-5.3-flash|-p @PROMPT@|OpenRouter GLM-5.3-Flash
opencode|DEFAULT_OLLAMA_MODEL|run @PROMPT@|
opencode|openrouter/z-ai/glm-5.3-flash|run @PROMPT@|OpenRouter GLM-5.3-Flash|xfail
pi|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|
pi|openrouter/z-ai/glm-5.3-flash|-p @PROMPT@|OpenRouter GLM-5.3-Flash
agy|agy/native|-p @PROMPT@|native model; driver ignores it
shell|-|echo @PROMPT@|-- passthrough becomes argv
EOF

build_prompt() {
  local agent="$1" runid="$2"
  echo "Reply with exactly this text and nothing else: WT-SMOKE-${agent}-${runid}"
}

# new_runid generates the run identifier embedded in every row's prompt.
# The id must stay alphanumeric: Pi's privacy filter redacts numeric
# sequences matching phone numbers (e.g. a 10-digit epoch timestamp), which
# would turn the echoed marker into a false FAIL. The md5/md5sum hex digest
# keeps the id phone-number-shaped-free; cksum was rejected because its
# output is a ~10-digit decimal — exactly the redaction trigger this avoids.
# md5 is macOS; md5sum is Linux — the fallback keeps the format stable when
# the script runs on either platform (a missing digest command degrades to
# timestamp+RANDOM, still prefixed, still alphanumeric).
new_runid() {
  local digest
  if command -v md5 >/dev/null 2>&1; then
    digest=$(date +%s | md5)
  elif command -v md5sum >/dev/null 2>&1; then
    digest=$(date +%s | md5sum)
  fi
  printf 'run-%s-%s\n' "${digest:0:8}" "$RANDOM"
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
# the script's INT trap is deferred forever — leaving the flipped routing
# state unrestored. --foreground keeps the child in the terminal's group;
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

# ── LiteLLM routing-mode flipping ─────────────────────────────────────────────
# wt owns the routing on/off switch: the [litellm] table of its own
# ${XDG_CONFIG_HOME:-$HOME/.config}/agent-wt/config.toml (modelman.toml's
# [litellm] is only a legacy fallback that wt copies in once). The script
# flips the mode through the tool itself — `wt litellm on` / `wt litellm off`,
# using the same `wt` found on PATH that runs the matrix rows, so the state is
# written properly (0600 when a key is stored) and a wt built from a branch is
# the one that is flipped. Routing policy only: the proxy process is never
# started or stopped. wt's config.toml is snapshotted before the first flip
# and restored on any exit path; if it did not exist before, it is removed on
# restore (an "absent" marker file records that, so a stale run is detected
# either way).

WT_CONFIG_FILE=""
WT_CONFIG_BAK=""
WT_CONFIG_ABSENT=""
WT_CONFIG_SNAPSHOTTED=""

# wt_config_path resolves wt's own config file, honoring XDG_CONFIG_HOME
# exactly like wt (internal/config.Dir).
wt_config_path() {
  local base="${XDG_CONFIG_HOME:-$HOME/.config}"
  printf '%s\n' "$base/agent-wt/config.toml"
}

# snapshot_wt_config backs up the original config once, before any flip.
# Refuses to run when a backup or absent-marker already exists: a leftover
# means a previous run died between snapshot and restore, and overwriting it
# could destroy the only copy of the user's original config.toml.
snapshot_wt_config() {
  if [[ -e "$WT_CONFIG_BAK" || -e "$WT_CONFIG_ABSENT" ]]; then
    echo "error: stale backup $WT_CONFIG_BAK (or $WT_CONFIG_ABSENT) exists; a previous run may have died mid-run." >&2
    echo "       Inspect it, restore it by hand to $WT_CONFIG_FILE if it holds your config (or delete $WT_CONFIG_FILE if the marker exists), then remove the backup/marker." >&2
    return 1
  fi
  mkdir -p "$(dirname "$WT_CONFIG_FILE")" || return 1
  if [[ -f "$WT_CONFIG_FILE" ]]; then
    cp -p "$WT_CONFIG_FILE" "$WT_CONFIG_BAK" || return 1
  else
    : >"$WT_CONFIG_ABSENT" || return 1
  fi
  WT_CONFIG_SNAPSHOTTED=yes
}

# restore_wt_config returns wt's config.toml to its pre-run state: the backup
# is moved back, or the file is removed when it did not exist before.
# Idempotent: safe to call from both the explicit restore and the EXIT trap.
restore_wt_config() {
  [[ -n "$WT_CONFIG_SNAPSHOTTED" ]] || return 0
  if [[ -f "$WT_CONFIG_BAK" ]]; then
    mv "$WT_CONFIG_BAK" "$WT_CONFIG_FILE"
  elif [[ -e "$WT_CONFIG_ABSENT" ]]; then
    rm -f "$WT_CONFIG_FILE" "$WT_CONFIG_ABSENT"
  fi
  WT_CONFIG_SNAPSHOTTED=""
}

# set_litellm_enabled flips routing through the tool: `wt litellm on|off`.
# Errors loudly on failure — a silent no-op here would run the whole matrix
# against the wrong mode without telling you.
set_litellm_enabled() {
  local value="$1" sub="off"
  [[ "$value" == "true" ]] && sub="on"
  if ! wt litellm "$sub" >/dev/null; then
    echo "error: cannot flip routing mode: 'wt litellm $sub' failed" >&2
    return 1
  fi
}

# litellm_status_json prints `wt litellm status --json` (the api key is never
# printed by wt; only api_key_set).
litellm_status_json() {
  wt litellm status --json 2>/dev/null
}

# current_gateway_mode prints the effective routing mode: "litellm" when
# routing is enabled, else "direct".
current_gateway_mode() {
  if litellm_status_json | grep -q '"enabled":true'; then
    printf 'litellm\n'
  else
    printf 'direct\n'
  fi
}

# require_litellm_credentials verifies wt has a LiteLLM url and api key: a
# litellm pass without credentials fails every row with connection errors
# that read as agent bugs. Failing fast beats a wall of misleading FAILs.
require_litellm_credentials() {
  local st
  st=$(litellm_status_json)
  if [[ "$st" != *'"api_key_set":true'* || "$st" == *'"url":""'* ]]; then
    echo "skip: wt litellm lacks url/api_key; the litellm pass would fail every row (run 'wt litellm set --url ... --api-key ...')" >&2
    return 1
  fi
}

# ── Matrix row execution ───────────────────────────────────────────────

# xfail_applies reports whether a row's optional 5th matrix field marks it
# as expected-to-fail under the current gateway mode: bare "xfail" covers
# every mode; "xfail:<mode>" covers only that mode. Any other value is
# treated as no mark, so a typo can't silently invert a row's verdict.
xfail_applies() {
  local mark="$1" mode="$2"
  case "$mark" in
    xfail) return 0 ;;
    xfail:*) [[ "${mark#xfail:}" == "$mode" ]] ;;
    *) return 1 ;;
  esac
}

dry_run() {
  local runid
  runid="$(new_runid)"
  while IFS='|' read -r agent model args_template _ _; do
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
  local agent="$1" model="$2" args_template="$3" comment="$4" runid="$5" xfail_mark="$6"
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

  # XFAIL: a marked row failing as expected is not a regression and must not
  # fail the suite. XPASS: a marked row passing means the underlying agent
  # behavior changed — report loudly (never silent), but keep the exit code
  # green so the stale mark surfaces without blocking.
  if [[ $verdict == "FAIL" ]] && xfail_applies "$xfail_mark" "$GATEWAY_MODE"; then
    verdict="XFAIL"
  elif [[ $verdict == "PASS" ]] && xfail_applies "$xfail_mark" "$GATEWAY_MODE"; then
    verdict="XPASS"
  fi

  printf '[%-5s] %-10s × %-28s (%ss)\n' "$verdict" "$agent" "$(display_model "$model")" "$elapsed"
  if [[ "$verdict" == "FAIL" || "$verdict" == "XPASS" ]]; then
    [[ "$verdict" == "XPASS" ]] && echo "  unexpected pass: xfail mark is stale, remove it from the matrix"
    echo "  command: $cmdline"
    echo "  output:"
    while IFS= read -r line; do
      printf '    %s\n' "$line"
    done <<<"$result"
  fi
  case "$verdict" in
    PASS) return 0 ;;
    SKIP) return 2 ;;
    XFAIL) return 3 ;;
    XPASS) return 4 ;;
    *) return 1 ;;
  esac
}

# run_tests_for_mode runs the matrix rows under the currently configured
# gateway mode. Mode-invariant rows (native, shell) run only when
# is_first_pass is "yes" — they already ran under the first mode.
run_tests_for_mode() {
  local mode="$1" is_first_pass="$2"
  GATEWAY_MODE="$mode"
  local runid rc fail_count=0 skip_count=0 xfail_count=0 xpass_count=0 total=0
  runid="$(new_runid)"
  echo ""
  echo "=== Agents Smoke Run (gateway=${mode}, timeout=${TIMEOUT}s, runid=${runid}) ==="
  while IFS='|' read -r agent model args_template comment xfail_mark; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    should_run_agent "$agent" || continue
    if is_mode_invariant "$agent" "$model" && [[ "$is_first_pass" != "yes" ]]; then
      # Mode-invariant row, already covered by the first pass.
      continue
    fi
    model="${model/DEFAULT_OLLAMA_MODEL/$DEFAULT_OLLAMA_MODEL}"
    run_row "$agent" "$model" "$args_template" "$comment" "$runid" "$xfail_mark"
    rc=$?
    if [[ $rc -eq 1 ]]; then ((fail_count++)); fi
    if [[ $rc -eq 2 ]]; then ((skip_count++)); fi
    if [[ $rc -eq 3 ]]; then ((xfail_count++)); fi
    if [[ $rc -eq 4 ]]; then ((xpass_count++)); fi
    ((total++)) || true
  done <<<"$MATRIX"
  echo "=== PASS: $((total - fail_count - skip_count - xfail_count - xpass_count)) FAIL: $fail_count SKIP: $skip_count XFAIL: $xfail_count XPASS: $xpass_count (gateway=${mode}) ==="
  [[ $fail_count -eq 0 ]]
}

# ── Argument parsing ──────────────────────────────────────────────────

usage() {
  cat <<'USAGE'
Usage: scripts/agents-smoke.sh [options]
  (no args)       Run the live agent smoke tests in both routing modes
                  (direct then litellm; wt's config.toml is
                  restored afterwards)
  --list          Print the test matrix and exit
  --dry-run       Print the wt command for each row and exit
  --only AGENTS   Comma-separated agents to run (default all)
  --modes MODES   Comma-separated routing modes to run (default direct,litellm).
                  "current" runs only the mode already configured.
  --timeout SECS  Per-row timeout (default 180)
  -h, --help      Show this help
USAGE
}

list_rows() {
  printf '%-10s %-30s %s\n' "AGENT" "MODEL" "COMMENT"
  while IFS='|' read -r agent model _args comment _; do
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
# Gateway mode of the pass currently executing, set by run_tests_for_mode
# before run_row classifies a row's xfail mark against it.
GATEWAY_MODE=""

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

  # Everything below flips the [litellm] routing state: snapshot wt's
  # config.toml (or record its absence) before any flip. (list/dry-run never touch the config.)
  WT_CONFIG_FILE=$(wt_config_path) || exit 1
  WT_CONFIG_BAK="${WT_CONFIG_FILE}.agents-smoke.bak"
  WT_CONFIG_ABSENT="${WT_CONFIG_FILE}.agents-smoke.absent"
  snapshot_wt_config || exit 1
  # Restore the original on any exit path. INT/TERM exit so the EXIT trap
  # fires the restore (a plain restore-and-return handler would let the
  # script continue after Ctrl-C).
  trap 'restore_wt_config' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  MODES=$(expand_modes)
  local overall_rc=0 pass_idx=0
  local -a mode_list
  IFS=',' read -r -a mode_list <<<"$MODES"
  for mode in "${mode_list[@]}"; do
    if [[ "$mode" == "litellm" ]] && ! require_litellm_credentials; then
      echo "=== litellm pass skipped (no [litellm] credentials) ===" >&2
      continue
    fi
    want="false"
    [[ "$mode" == "litellm" ]] && want="true"
    if [[ "$mode" != "$(current_gateway_mode)" ]]; then
      set_litellm_enabled "$want" || exit 1
    fi
    ((pass_idx++)) || true
    run_tests_for_mode "$mode" "$([[ $pass_idx -eq 1 ]] && echo yes || echo no)" || overall_rc=1
  done
  restore_wt_config
  echo "=== wt config.toml restored to original state ==="
  exit $overall_rc
}

main "$@"
