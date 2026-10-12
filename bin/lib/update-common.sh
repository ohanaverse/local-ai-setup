#!/bin/bash
# update-common.sh — the shared contract behind bin/update/* and bin/update-tools.
#
# Why this file exists: eight per-tool scripts would otherwise each invent their
# own banner, flag parsing, dry-run handling and exit codes, and the umbrella
# would have nothing stable to summarise them with. What the umbrella reads is
# the one-line trailer every script prints when it finishes (see below) —
# deliberately a visible stdout line rather than an env var or a temp file, so
# that running a script by hand shows exactly what the umbrella reports, and
# neither path can drift from the other.
#
# Usage in a per-tool script:
#
#   # shellcheck source=lib/update-common.sh
#   source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/update-common.sh"
#   update_usage() { cat <<'EOF'
#   ...help text...
#   EOF
#   }
#   update_init <tool> "$@"
#   update_note "current: $(tool --version)"
#   run brew upgrade <tool> || update_fail "brew upgrade failed"
#   update_finish updated "$before -> $after"
#
# Trailer vocabulary — the umbrella's status column:
#   updated        the update ran and the tool answers at a new version
#   restarted      the version did not move, but the running service was replaced
#                  with what is on disk — the restart was itself the work, which
#                  is the normal outcome for litellm (its proxy holds the code
#                  it imported at boot) and the truthful one for a brew service
#                  that was bounced
#   current        already at the newest available version; nothing run
#   stale          an update is available but was not applied (--check, or the
#                  package manager still offering one after an upgrade ran)
#   delegated      another script in the same run owns this tool's update
#   dry-run        --dry-run: the commands were printed, nothing was run
#   manual         no scriptable update path; a human must do it
#   not-installed  the tool is not on this machine
#   failed         the update was attempted and failed
#
# Exit codes, which the umbrella turns into its own exit status:
#   0  updated | restarted | current | stale | delegated | dry-run
#   1  failed
#   3  manual | not-installed   (nothing attempted; not an error in itself)

UPDATE_TOOL=""
UPDATE_DRY_RUN=0
UPDATE_CHECK=0
UPDATE_ARGS=()

# update_init <tool> [flags...] — set the tool name, parse the two flags every
# script shares, and print the banner. Anything unrecognised lands in
# UPDATE_ARGS for the script to validate itself (e.g. update/wt's --pull).
# Each script must define update_usage before calling this.
update_init() {
    UPDATE_TOOL="$1"
    shift
    for arg in "$@"; do
        case "$arg" in
            --dry-run) UPDATE_DRY_RUN=1 ;;
            --check) UPDATE_CHECK=1 ;;
            -h | --help)
                update_usage
                exit 0
                ;;
            *) UPDATE_ARGS+=("$arg") ;;
        esac
    done
    printf '==> %s\n' "$UPDATE_TOOL"
}

update_note() { printf '    %s\n' "$*"; }

# run <cmd...> — run a command, or under --dry-run print it instead. Returns
# the command's status so the caller can decide how to report the failure.
run() {
    if [ "$UPDATE_DRY_RUN" -eq 1 ]; then
        printf '    would run: %s\n' "$*"
        return 0
    fi
    printf '    run: %s\n' "$*"
    "$@"
}

# dry_run_stop — true when the caller has finished printing its commands and
# must not go on to verify or restart anything.
dry_run_stop() { [ "$UPDATE_DRY_RUN" -eq 1 ]; }

# require_cmd <cmd> — bail out as not-installed rather than letting a later
# command fail obscurely. Leaves a partial install a skip, not a failure.
require_cmd() {
    command -v "$1" > /dev/null 2>&1 ||
        update_finish not-installed "$1 is not on PATH"
}

# update_finish <status> [detail] — print the trailer and exit.
update_finish() {
    printf '[update:%s] %s' "$UPDATE_TOOL" "$1"
    [ -n "${2:-}" ] && printf ' — %s' "$2"
    printf '\n'
    case "$1" in
        failed) exit 1 ;;
        manual | not-installed) exit 3 ;;
        *) exit 0 ;;
    esac
}

update_fail() {
    printf '    FAILED: %s\n' "$*" >&2
    update_finish failed "$*"
}

# finish_after_upgrade <before> <after> <offering> <outdated-line> [restart-detail]
# — the tail shared by the brew and uv tools. `updated` is claimed only when the
# version actually moved: an upgrade that reports success while the version
# stands still is not an update, and printing "updated 0.7.0 -> 0.7.0" would hide
# exactly the case worth looking at. If the package manager still offers one
# after the upgrade ran, that is what gets reported. A non-empty
# [restart-detail] — the caller passes it only when it really did restart the
# service — means the run did work whose value the version number cannot show,
# so it is reported as `restarted` rather than the `current` that would claim
# nothing happened.
finish_after_upgrade() {
    local before="$1" after="$2" offering="$3" outdated="$4" restarted="${5:-}"
    if [ "$before" != "$after" ]; then
        update_finish updated "$before -> $after"
    fi
    if [ -n "$outdated" ]; then
        update_finish stale "$after (still outdated per $offering)"
    fi
    if [ -n "$restarted" ]; then
        update_finish restarted "$after (unchanged); $restarted"
    fi
    update_finish current "$after"
}

# --- brew helpers -----------------------------------------------------------
# Shared by update/omlx, update/postgresql and update/redis, which are the same
# shape three times over: check availability, upgrade one formula, bounce its
# brew service, verify the service answers. Only the version command and the
# liveness probe differ, so those stay in each script.

# brew_outdated <formula> — brew's "what would change" line, empty when current.
brew_outdated() { brew outdated --formula "$1" 2> /dev/null || true; }

# brew_upgrade <formula> [extra brew flags...] — upgrade, or fail the script.
brew_upgrade() { run brew upgrade "$@" || update_fail "brew upgrade $* failed"; }

# brew_restart <formula> — bounce the service, or fail the script.
brew_restart() { run brew services restart "$1" || update_fail "brew services restart $1 failed"; }

# wait_for_http <url> [tries] — poll until anything answers, setting HTTP_CODE.
# The loop is wrapped in an `if` rather than a `&&` chain on purpose: a bare
# `[ ... ] && break` as the last statement of a loop body returns 1 when the
# test fails, and `set -e` would abort the script on the first poll that says
# "not up yet" — which is most of them.
wait_for_http() {
    local url="$1" tries="${2:-20}" code i
    HTTP_CODE=000
    for ((i = 0; i < tries; i++)); do
        code=$(curl -s -m 2 "$url" -o /dev/null -w '%{http_code}' 2> /dev/null || true)
        if [ -n "$code" ] && [ "$code" != "000" ]; then
            HTTP_CODE="$code"
            break
        fi
        sleep 1
    done
}

# wait_for_cmd [tries] <cmd...> — poll until the command exits 0, leaving its
# output in CMD_OUTPUT; returns non-zero if it never does. For the two services
# whose liveness probe is a command rather than a URL (redis-cli ping,
# pg_isready). This exists because `brew services restart` returns as soon as
# launchd has kicked the job, not when the service is listening: probing once
# right afterwards reports a race as a failure, and the note then sends the
# reader off to a crash-loop log for a server that was starting normally.
wait_for_cmd() {
    local tries="${1:-20}" i
    shift
    CMD_OUTPUT=""
    for ((i = 0; i < tries; i++)); do
        if CMD_OUTPUT=$("$@" 2>&1); then
            return 0
        fi
        sleep 1
    done
    return 1
}
