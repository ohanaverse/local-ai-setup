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
#                  bounced onto a keg whose version string did not move (a
#                  revision bump, or --restart)
#   current        already at the newest available version; nothing run
#   stale          an update is available but was not applied (--check, or the
#                  package manager still offering one after an upgrade ran)
#   delegated      another script in the same run owns this tool's update
#   dry-run        --dry-run: the commands were printed, nothing was run
#   manual         no scriptable update path; a human must do it
#   not-installed  the tool is not on this machine
#   failed         the update was attempted and failed — which includes a service
#                  that was restarted and never answered again: the upgrade may
#                  be on disk, but a sweep that reports it as done would go on to
#                  restart the things that depend on it
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
# after the upgrade ran, that is what gets reported — so <outdated-line> must be
# queried again after the upgrade, never the pre-upgrade answer reused: a
# Homebrew revision bump (16.15 -> 16.15_1) leaves the tool's own version string
# where it was, and the stale answer would report a finished upgrade as
# "still outdated". A non-empty
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

# finish_against_published <installed> <latest> <how-to-update> — the tail
# shared by the app-managed tools (update/ollama, update/mtplx), which can only
# compare what is installed against what is published and say how to apply it.
# An installed version that could not be read is reported as such rather than
# compared: `sort -V` orders a word after every number, so "unknown" would win
# the comparison below and be reported as `current`, ahead of the release.
finish_against_published() {
    local installed="$1" latest="$2" how="$3" newest
    update_note "latest: $latest"
    if [ "$installed" = "unknown" ]; then
        update_finish manual "installed version unknown, $latest published — $how"
    fi
    if [ "$latest" = "$installed" ]; then
        update_finish current "$installed"
    fi
    newest=$(printf '%s\n%s\n' "$installed" "$latest" | sort -V | tail -1)
    if [ "$newest" = "$installed" ]; then
        update_finish current "$installed (ahead of the published $latest)"
    fi
    update_finish manual "$installed installed, $latest available — $how"
}

# --- brew helpers -----------------------------------------------------------
# Shared by update/omlx, update/postgresql and update/redis, which are the same
# shape three times over: check availability, upgrade one formula, bounce its
# brew service, verify the service answers. Only the version command and the
# liveness probe differ, so those stay in each script.

# brew_outdated <formula> — brew's "what would change" line, empty when current.
# --verbose is what makes it that line: with stdout captured rather than a
# terminal, brew otherwise prints the bare formula name and the trailer would
# read "brew has omlx" instead of "brew has omlx (0.7.0) < 0.8.0".
brew_outdated() { brew outdated --formula --verbose "$1" 2> /dev/null || true; }

# brew_refresh — fetch Homebrew's formula metadata, so that brew_outdated
# answers from what is published rather than from the last fetch. Only a
# formula from a third-party tap needs it (update/omlx): a tap is a git clone
# that moves only on `brew update`, where core formulae are read from an API
# file brew refreshes by itself. It touches Homebrew's own metadata and no
# installed tool, which is why --check may run it. Offline is not an error —
# the answer is then only as fresh as the last fetch, and the note says so.
brew_refresh() {
    update_note "run: brew update (refresh tap metadata)"
    brew update --quiet > /dev/null 2>&1 ||
        update_note "brew update failed; availability is as of the last fetch"
}

# brew_upgrade <formula> [extra brew flags...] — upgrade, or fail the script.
brew_upgrade() { run brew upgrade "$@" || update_fail "brew upgrade $* failed"; }

# brew_restart <formula> — bounce the service, or fail the script.
brew_restart() { run brew services restart "$1" || update_fail "brew services restart $1 failed"; }

# brew_restart_due <outdated-before> <before> <after> — true when the service
# should be bounced: brew had something to install (which covers a revision
# bump that leaves the version string alone), the version moved, or the caller
# was given --restart. Without this gate a sweep over a fully current machine
# would restart every service for nothing — and an omlx restart unloads every
# model in its pool, under whatever agent is using one.
brew_restart_due() {
    [ -n "${FORCE_RESTART:-}" ] || [ -n "$1" ] || [ "$2" != "$3" ]
}

# brew_dry_run_finish <formula> <outdated> <installed> — the --dry-run tail of
# the three brew scripts: say what the restart step would do, then finish.
brew_dry_run_finish() {
    if [ -n "${NO_RESTART:-}" ]; then
        :
    elif [ -n "${FORCE_RESTART:-}" ] || [ -n "$2" ]; then
        update_note "would run: brew services restart $1"
    else
        update_note "brew has nothing newer, so the service would be left alone (--restart bounces it anyway)"
    fi
    update_finish dry-run "$3 -> newest $1, then service restart if anything changed"
}

# RESTART_WAIT — how many one-second polls a restarted service gets before the
# script calls it down. Generous on purpose: the proxy alone takes 10-20s to
# come back, and a failure here fails the whole run.
RESTART_WAIT=60

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
