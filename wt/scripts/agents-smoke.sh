#!/usr/bin/env bash
# agents-smoke.sh -- live one-shot smoke test for every wt agent.
#
# wt reaches its models through one model server's gateway (wt-model serve),
# so the matrix runs in the single mode "gateway". The script needs the
# server's URL and token file in WT_MODEL_URL and WT_MODEL_TOKEN_FILE (the same
# variables wt reads; they override [model-server] in config.toml) and fails
# fast naming whichever is missing. It changes no wt config. Native rows and
# shell never consult the gateway but run in the same pass.

set -o nounset -o pipefail

DEFAULT_TIMEOUT=180
DEFAULT_OLLAMA_MODEL="ollama/glm-5.3-flash:cloud"
DEFAULT_MODES="gateway"

# agent|model|one-shot-args-template|comment|xfail
# model "-" means launch without -M.
# @PROMPT@ is replaced with the row's prompt (a single shell-quoted argument).
# Optional 5th field marks a known-failing row: "xfail" (fails in every
# routing mode) or "xfail:gateway" (mode-scoped; gateway is the only mode). A marked
# row that fails prints XFAIL and leaves the exit code alone; a marked row
# that unexpectedly passes prints XPASS — a loud stale-mark warning. The
# marks mirror the Known Limitations section of
# docs/reference/glm-5.3-flash-openrouter.md.
read -r -d '' MATRIX <<'EOF' || true
claude|claude/native|-p @PROMPT@|own subscription, no model args
claude|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|gateway round-trip
claude|openrouter/z-ai/glm-5.3-flash|-p @PROMPT@|OpenRouter GLM-5.3-Flash
codex|DEFAULT_OLLAMA_MODEL|exec @PROMPT@|
codex|openrouter/z-ai/glm-5.3-flash|exec @PROMPT@|OpenRouter GLM-5.3-Flash
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

# smoke_text is the lower-case text a row's prompt carries. The prompt must
# never contain the sentinel itself (expected_reply): codex prints its prompt
# back, so a sentinel the prompt contains passes on that echo whatever the
# model replied -- the false PASS `wt smoke` had (#193, smoke.DefaultPrompt).
smoke_text() {
  local agent="$1" runid="$2"
  printf 'wt-smoke-%s-%s\n' "$agent" "$runid" | tr '[:upper:]' '[:lower:]'
}

build_prompt() {
  local agent="$1" runid="$2"
  echo "Reply with exactly this text converted to upper case and nothing else: $(smoke_text "$agent" "$runid")"
}

# expected_reply is what a row's output must contain to PASS: the prompt's
# text in upper case, which only a model's reply can produce. The shell row
# has no model -- it echoes its argv to prove the `--` passthrough -- so there
# the prompt's own text coming back is the pass condition.
expected_reply() {
  local agent="$1" runid="$2" text
  text=$(smoke_text "$agent" "$runid")
  if [[ "$agent" == "shell" ]]; then
    echo "$text"
  else
    echo "$text" | tr '[:lower:]' '[:upper:]'
  fi
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

# with_timeout runs the row command under a TIMEOUT-second alarm.
# gtimeout needs --foreground: by default it puts the child in its own
# process group, so a terminal Ctrl-C (delivered to the foreground group)
# never reaches the running agent, and gtimeout neither dies nor forwards,
# so Ctrl-C would not stop the run. --foreground keeps the child in the
# terminal's group; Ctrl-C kills the row and the script. (Documented gtimeout caveat: with --foreground, grandchildren of
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

# ── Model-server settings ─────────────────────────────────────────────
# require_model_server verifies the two variables a gateway pass needs: a pass
# without them fails every row with connection errors that read as agent bugs.
# Failing fast beats a wall of misleading FAILs.
require_model_server() {
  local missing=0
  if [[ -z "${WT_MODEL_URL:-}" ]]; then
    echo "error: WT_MODEL_URL is not set (the model server's base URL, e.g. http://127.0.0.1:8765)" >&2
    missing=1
  fi
  if [[ -z "${WT_MODEL_TOKEN_FILE:-}" ]]; then
    echo "error: WT_MODEL_TOKEN_FILE is not set (the path of the server's token file)" >&2
    missing=1
  elif [[ ! -r "$WT_MODEL_TOKEN_FILE" ]]; then
    echo "error: WT_MODEL_TOKEN_FILE $WT_MODEL_TOKEN_FILE is not a readable file" >&2
    missing=1
  fi
  [[ $missing -eq 0 ]]
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
  elif [[ $status -eq 0 ]] && [[ "$result" == *"$(expected_reply "$agent" "$runid")"* ]]; then
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

# run_tests_for_mode runs the whole matrix under one gateway mode (gateway is
# the only one today; the function keeps the xfail mark's mode scoping).
run_tests_for_mode() {
  local mode="$1"
  GATEWAY_MODE="$mode"
  local runid rc fail_count=0 skip_count=0 xfail_count=0 xpass_count=0 total=0
  runid="$(new_runid)"
  echo ""
  echo "=== Agents Smoke Run (gateway=${mode}, timeout=${TIMEOUT}s, runid=${runid}) ==="
  while IFS='|' read -r agent model args_template comment xfail_mark; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    should_run_agent "$agent" || continue
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
Environment (required for a live run; --list, --dry-run and --help need neither):
  WT_MODEL_URL         base URL of the wt-model server (wt-model serve)
  WT_MODEL_TOKEN_FILE  path of that server's token file (mode 0600)

  (no args)       Run the live agent smoke tests through the gateway
  --list          Print the test matrix and exit
  --dry-run       Print the wt command for each row and exit
  --only AGENTS   Comma-separated agents to run (default all)
  --modes MODES   Comma-separated routing modes to run (default gateway,
                  the only mode)
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
# must be gateway, the only mode.
validate_modes() {
  [[ -z "$MODES" ]] && {
    echo "error: --modes requires at least one mode" >&2
    exit 1
  }
  local IFS=',' token
  for token in $MODES; do
    [[ -z "$token" ]] && continue
    case "$token" in
      gateway) ;;
      *)
        echo "error: unknown mode '${token}' in --modes (known: gateway)" >&2
        exit 1
        ;;
    esac
  done
}

# expand_modes dedupes the --modes tokens, keeping their order.
expand_modes() {
  local IFS=','
  local out="" token
  for token in $MODES; do
    [[ -z "$token" ]] && continue
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

  require_model_server || exit 1

  MODES=$(expand_modes)
  local overall_rc=0 mode
  local -a mode_list
  IFS=',' read -r -a mode_list <<<"$MODES"
  for mode in "${mode_list[@]}"; do
    run_tests_for_mode "$mode" || overall_rc=1
  done
  exit $overall_rc
}

main "$@"
