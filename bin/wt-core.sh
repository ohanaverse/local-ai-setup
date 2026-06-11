#!/usr/bin/env bash
# wt-core.sh — shared worktree/branch engine for AI agent launchers.
# Not executable on its own. Source from a wrapper and call wt_main.
#
# Wrapper contract (define these before calling wt_main):
#   WT_NAME=<name>        — launcher name (set by wrapper, e.g. "claude-wt")
#   SCRIPT_DIR=<path>     — absolute path to directory containing wrapper + wt-core.sh
#   WT_DEFAULT_CODE       — fallback model for code mode (e.g. "native:claude")
#   WT_DEFAULT_DESIGN     — fallback model for design mode
#   WT_AGENT_NAME          — agent identifier for model-usability checks (e.g. "claude")
#   wt_check_deps()        — verify binary exists, die with install hint
#   wt_yolo_flag()         — echo the tool's skip-permissions flag, or empty
#   wt_exec "$@"           — construct and exec the final launch command
#   wt_pre_exec()          — OPTIONAL: session resume hook called after cd
#
# Install with wrapper scripts in the same directory.

set -euo pipefail

die() {
  printf '%s: %s\n' "${WT_NAME:-wt}" "$*" >&2
  exit 1
}

# Config directory for rotation state and model configs.
wt_config_dir() {
  printf '%s' "${XDG_CONFIG_HOME:-${HOME}/.config}/agent-wt"
}

# Returns the path to the rotation state file for a mode (code/design).
wt_rotation_state() {
  local mode="$1"
  printf '%s/rotation-%s.state' "$(wt_config_dir)" "$mode"
}

# Converts an absolute path to a slug for session directories.
# Replaces every character not in [a-zA-Z0-9-] with -.
compute_project_slug() {
  printf '%s' "$1" | sed 's/[^a-zA-Z0-9-]/-/g'
}

# Checks ~/.claude/projects/<slug>/*.jsonl for session files.
# <slug> is computed from the given path.
# Output: tab-separated <session-id>\t<mtime-epoch> (empty if no sessions found).
find_latest_session() {
  local path="$1"
  local slug
  slug="$(compute_project_slug "$path")"
  local dir="$HOME/.claude/projects/$slug"

  if [[ ! -d "$dir" ]]; then
    return
  fi

  # Use stat to get mtime (macOS/BSD format).
  # Find all .jsonl files, stat each, sort by mtime descending, take the newest.
  local newest
  newest="$(find "$dir" -maxdepth 1 -name '*.jsonl' -exec stat -f '%m%t%N' {} + 2>/dev/null |
    sort -rn |
    head -1)" || true

  [[ -z "$newest" ]] && return

  local mtime="${newest%%$'\t'*}"
  local filepath="${newest#*$'\t'}"
  local basename
  basename="$(basename "$filepath" .jsonl)"
  printf '%s\t%s\n' "$basename" "$mtime"
}

# Converts a unix-epoch timestamp to a human-readable relative time string.
# Output examples: "just now", "5m ago", "3h ago", "2d ago", "1w ago".
relative_time() {
  local epoch="$1"
  local now
  now="$(date +%s)"
  local diff=$((now - epoch))

  if [[ $diff -lt 60 ]]; then
    printf 'just now'
  elif [[ $diff -lt 3600 ]]; then
    printf '%dm ago' $((diff / 60))
  elif [[ $diff -lt 86400 ]]; then
    printf '%dh ago' $((diff / 3600))
  elif [[ $diff -lt 604800 ]]; then
    printf '%dd ago' $((diff / 86400))
  else
    printf '%dw ago' $((diff / 604800))
  fi
}

# Shows an fzf prompt offering to resume a session or start fresh.
# $1 = branch name (display), $2 = session UUID, $3 = mtime epoch
# Output: session UUID if user chose Resume, empty string if Start fresh.
session_resume_prompt() {
  local branch="$1" session_id="$2" mtime="$3"
  local rel
  rel="$(relative_time "$mtime")"

  local choice
  choice="$(printf 'Resume session on %s (last active %s)\nStart fresh on %s\n' \
    "$branch" "$rel" "$branch" |
    fzf --no-multi \
      --prompt='wt> ' \
      --header='Previous session found')" || return 0

  case "$choice" in
    Resume*) printf '%s\n' "$session_id" ;;
    *) return 0 ;;
  esac
}

# Reads `git worktree list --porcelain` from stdin.
# Emits one row per non-bare worktree:  worktree<TAB>BRANCH<TAB>PATH
# BRANCH is "(detached)" for detached HEADs.
parse_worktree_porcelain() {
  local path="" branch="" is_bare=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ -z "$line" ]]; then
      # End of block — emit if non-bare and we collected a path.
      if [[ -n "$path" && "$is_bare" -eq 0 ]]; then
        printf 'worktree\t%s\t%s\n' "${branch:-(detached)}" "$path"
      fi
      path="" branch="" is_bare=0
      continue
    fi
    case "$line" in
      worktree\ *) path="${line#worktree }" ;;
      branch\ refs/heads/*) branch="${line#branch refs/heads/}" ;;
      bare) is_bare=1 ;;
      detached) : ;; # leave branch empty; emitter substitutes (detached)
    esac
  done
  # Flush trailing block if input did not end with blank line.
  if [[ -n "$path" && "$is_bare" -eq 0 ]]; then
    printf 'worktree\t%s\t%s\n' "${branch:-(detached)}" "$path"
  fi
}

# Emits branches present in $2 but not in $1, preserving order from $2.
# Both arguments are newline-separated branch lists. Empty lines are skipped.
compute_bare_branches() {
  local in_use="$1" all="$2"
  local b
  while IFS= read -r b; do
    [[ -z "$b" ]] && continue
    if ! grep -Fxq -- "$b" <<<"$in_use"; then
      printf '%s\n' "$b"
    fi
  done <<<"$all"
}

# Returns the repo's default branch (e.g. main, master) from origin/HEAD.
# Falls back to empty string if no remote or origin/HEAD is not set.
default_branch() {
  git symbolic-ref refs/remotes/origin/HEAD 2>/dev/null |
    sed 's@^refs/remotes/origin/@@' ||
    echo ""
}

# Returns 0 if the block-main-commit guard is installed in this repo, 1 otherwise.
# Uses --git-common-dir to handle worktrees correctly.
guard_status() {
  local git_common
  git_common="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  [[ -z "$git_common" ]] && return 1
  [[ -f "$git_common/hooks/pre-commit" ]] || return 1
  local rc=0
  grep -q "block-main-commit v1" "$git_common/hooks/pre-commit" 2>/dev/null || rc=$?
  if [[ $rc -eq 0 ]]; then
    return 0
  elif [[ $rc -eq 2 ]]; then
    printf '%s: cannot read hook file (permission denied?)\n' "${WT_NAME:-wt}" >&2
    return 1
  else
    return 1
  fi
}

# Emits one row per pickable target: TYPE<TAB>BRANCH<TAB>PATH
# TYPE is "current" for the cwd's worktree, "worktree" for other worktrees,
# or "branch" for local/remote branches not checked out anywhere (PATH is empty).
# For remote branches, BRANCH contains the full ref (e.g. origin/feature).
gather_entries() {
  local cwd_root
  cwd_root="$(git rev-parse --show-toplevel)"

  # Worktrees, with cwd tagged.
  local worktree_rows in_use
  worktree_rows="$(git worktree list --porcelain | parse_worktree_porcelain)"

  # Tag the cwd's row by replacing leading "worktree\t" with "current\t" on the matching path.
  local row tagged_rows=""
  while IFS= read -r row; do
    [[ -z "$row" ]] && continue
    local row_path="${row##*$'\t'}"
    if [[ "$row_path" == "$cwd_root" ]]; then
      tagged_rows+="current${row#worktree}"$'\n'
    else
      tagged_rows+="$row"$'\n'
    fi
  done <<<"$worktree_rows"

  # Branches in use across all worktrees (extract column 2 of the untagged porcelain output).
  in_use="$(awk -F'\t' '$2 != "(detached)" {print $2}' <<<"$worktree_rows")"

  # Prevent the default branch from appearing as a bare-branch option
  # so users are encouraged to create feature branches instead of working directly on main/master.
  local default_b
  default_b="$(default_branch)"
  [[ -n "$default_b" ]] && in_use+=$'\n'"$default_b"

  # Collect local branches
  local local_branches
  local_branches="$(git for-each-ref --format='%(refname:short)' refs/heads)"

  # Collect remote-tracking branches, filter out */HEAD symbolic refs and remote names without branches
  local remote_branches
  remote_branches="$(git for-each-ref --format='%(refname:short)' refs/remotes/ |
    grep '/' | grep -v '/HEAD$' || true)"

  # Build list of all branches (local + remote), excluding collisions
  # A remote branch is excluded if a local branch with the same name exists
  local all_branches=""
  local b short_name

  # Add all local branches
  while IFS= read -r b; do
    [[ -z "$b" ]] && continue
    all_branches+="$b"$'\n'
  done <<<"$local_branches"

  # Add remote branches whose short name doesn't match any local branch
  while IFS= read -r b; do
    [[ -z "$b" ]] && continue
    # Strip remote prefix (e.g. origin/feature → feature)
    short_name="${b#*/}"
    # Only include if no local branch with this name exists
    if ! grep -Fxq -- "$short_name" <<<"$local_branches"; then
      all_branches+="$b"$'\n'
    fi
  done <<<"$remote_branches"

  local bare_rows=""
  while IFS= read -r b; do
    [[ -z "$b" ]] && continue
    bare_rows+=$'branch\t'"$b"$'\t\n'
  done < <(compute_bare_branches "$in_use" "$all_branches")

  # Emit (trim trailing newlines).
  printf '%s%s' "$tagged_rows" "$bare_rows" | sed -e '$ { /^$/d; }'
}

# Reads TYPE<TAB>BRANCH<TAB>PATH rows from stdin.
# Emits TYPE<TAB>BRANCH<TAB>PATH<TAB><display> rows.
# <display> is a fixed-width pretty form for fzf to show.
# For remote branches (e.g. origin/feature), strips the remote prefix for display.
format_entries() {
  awk -F'\t' '
    {
      tag = "[" $1 "]"
      branch = $2
      # Strip remote prefix for display (e.g. origin/feature → feature)
      if (index(branch, "/") > 0) {
        display_branch = substr(branch, index(branch, "/") + 1)
      } else {
        display_branch = branch
      }
      path = ($3 == "" ? "(no worktree)" : $3)
      display = sprintf("%-10s %-33s %s", tag, display_branch, path)
      printf "%s\t%s\t%s\t%s\n", $1, branch, path, display
    }
  '
}

# Consumes --code/--design/--native and -w/--worktree/--yolo/--cwd/--no-guard/--check-guard
# from "$@".  Sets globals:
#   WT_MODEL_MODE   — "code", "design", or "" (default "code" if model rotation is active)
#   WT_NATIVE       — 1 if --native was given, 0 otherwise
#   WT_WORKTREE_NAME
#   WT_PASSTHROUGH_ARGS
#   WT_YOLO / WT_CWD / WT_NO_GUARD / WT_CHECK_GUARD / WT_INIT
# Dies if -w/--worktree is given without a value.
parse_wt_args() {
  WT_MODEL_MODE=""
  WT_NATIVE=0
  WT_WORKTREE_NAME=""
  WT_PASSTHROUGH_ARGS=()
  WT_YOLO=0
  WT_CWD=0
  WT_NO_GUARD=0
  WT_CHECK_GUARD=0
  WT_INIT=0

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --code)
        if [[ -n "${WT_DEFAULT_CODE:-}" ]]; then
          WT_MODEL_MODE="code"
        else
          WT_PASSTHROUGH_ARGS+=("$1")
        fi
        shift
        ;;
      --design)
        if [[ -n "${WT_DEFAULT_CODE:-}" ]]; then
          WT_MODEL_MODE="design"
        else
          WT_PASSTHROUGH_ARGS+=("$1")
        fi
        shift
        ;;
      --native)
        if [[ -n "${WT_DEFAULT_CODE:-}" ]]; then
          WT_NATIVE=1
        else
          WT_PASSTHROUGH_ARGS+=("$1")
        fi
        shift
        ;;
      -w | --worktree)
        [[ $# -lt 2 ]] && die "$1 requires a worktree name"
        WT_WORKTREE_NAME="$2"
        shift 2
        ;;
      --yolo)
        WT_YOLO=1
        shift
        ;;
      --no-guard)
        WT_NO_GUARD=1
        shift
        ;;
      --check-guard)
        WT_CHECK_GUARD=1
        shift
        ;;
      --cwd)
        WT_CWD=1
        shift
        ;;
      --init)
        WT_INIT=1
        shift
        ;;
      *)
        WT_PASSTHROUGH_ARGS+=("$1")
        shift
        ;;
    esac
  done

  # Default to code mode when model rotation is active but neither --code nor --design given.
  # Only applies if the wrapper sets WT_DEFAULT_CODE (i.e. model rotation is enabled).
  if [[ -z "$WT_MODEL_MODE" && -n "${WT_DEFAULT_CODE:-}" ]]; then
    WT_MODEL_MODE="code"
  fi
}

# Seeds project-level agent instruction files if they don't already exist.
# Creates AGENTS.md (seed template) and an agent-specific pointer file
# determined by WT_AGENT_NAME. Requires a git repository.
# Skips any file that already exists (skip-if-exists).
seed_agent_instructions() {
  local repo_root
  repo_root="$(git rev-parse --show-toplevel)" || die "not in a git working tree"
  local created=0

  # Seed AGENTS.md if missing
  local agents_md="$repo_root/AGENTS.md"
  if [[ ! -f "$agents_md" ]]; then
    cat >"$agents_md" <<'SEED'
# Agent Instructions

> **Uninitialized.** If this is your first time reading this file in a new project, ask the user about the project and fill in the sections below. Remove this notice once the file has been customized.

## Project

<!-- What does this project do? -->

## Stack

<!-- Language, framework, key dependencies -->

## Commands

<!-- Build, test, lint, deploy -->

## Conventions

<!-- Code style, naming patterns, important rules -->

## Architecture

<!-- Key directories, modules, data flow -->
SEED
    created=1
  fi

  # Seed agent-specific pointer file if applicable
  local pointer_file pointer_content
  case "${WT_AGENT_NAME:-}" in
    claude)
      pointer_file="CLAUDE.md"
      pointer_content="@AGENTS.md"
      ;;
    copilot)
      pointer_file=".github/copilot-instructions.md"
      pointer_content="Read AGENTS.md and follow all instructions in it."
      ;;
    *)
      # Codex, Pi, Agy: no pointer file needed
      if [[ "$created" -eq 1 ]]; then
        printf '%s: instruction files seeded in %s\n' "$WT_NAME" "$repo_root"
      else
        printf '%s: instruction files already exist in %s\n' "$WT_NAME" "$repo_root"
      fi
      return 0
      ;;
  esac

  local pointer_path="$repo_root/$pointer_file"
  if [[ ! -f "$pointer_path" ]]; then
    mkdir -p "$(dirname "$pointer_path")"
    printf '%s\n' "$pointer_content" >"$pointer_path"
    created=1
  fi

  if [[ "$created" -eq 1 ]]; then
    printf '%s: instruction files seeded in %s\n' "$WT_NAME" "$repo_root"
  else
    printf '%s: instruction files already exist in %s\n' "$WT_NAME" "$repo_root"
  fi
}

# Ensures a worktree exists for branch <name>.
# - If the branch is already checked out in any worktree, returns that worktree's path.
# - Else, if a worktree at .worktrees/<name> already exists, returns that path.
# - Else, creates a new worktree at .worktrees/<name> (using an existing branch
#   or creating a new one) and returns its path.
# Dies on any failure.
ensure_worktree_for_name() {
  local name="$1"
  [[ -z "$name" ]] && die "worktree name cannot be empty"
  local root path safe_name
  root="$(git rev-parse --show-toplevel)" || die "not in a git repo"

  # If the branch is already checked out in any worktree, use that worktree.
  local existing_path
  existing_path="$(git worktree list --porcelain |
    parse_worktree_porcelain |
    awk -F'\t' -v branch="$name" '$2 == branch {print $3; exit}')"
  if [[ -n "$existing_path" ]]; then
    printf '%s\n' "$existing_path"
    return
  fi

  # Handle branch names with slashes (e.g., origin/feature, feature/my-branch)
  # by extracting the last component for the worktree directory name.
  if [[ "$name" == */* ]]; then
    safe_name="${name##*/}"
  else
    safe_name="$name"
  fi

  # Safety: reject names that still contain path separators after extraction
  [[ "$safe_name" =~ / ]] && die "worktree name must not contain path separators: $name"

  path="$root/.worktrees/$safe_name"

  # Skip creation if the path is already a registered worktree (idempotent).
  if ! git worktree list --porcelain | grep -Fxq -- "worktree $path"; then
    if git show-ref --verify --quiet "refs/heads/$name"; then
      git worktree add "$path" "$name" >/dev/null || die "git worktree add failed"
    else
      git worktree add -b "$name" "$path" >/dev/null || die "git worktree add failed"
    fi
  fi
  printf '%s\n' "$path"
}

# Returns the resolved model name on stdout, or "" if no model selection applies.
# Exits with 1 if model rotation was requested but no usable model found.
# Uses globals: WT_MODEL_MODE, WT_NATIVE, WT_DEFAULT_CODE, WT_DEFAULT_DESIGN, WT_AGENT_NAME, WT_NAME.
get_model_from_rotation() {
  local mode="$WT_MODEL_MODE"

  # Nothing to do if no model mode is set (wrapper does not support rotation, or --cwd was used)
  [[ -z "$mode" ]] && return 1

  local config_file="$(wt_config_dir)/models.conf"
  local state_file="$(wt_rotation_state "$mode")"
  local other_mode
  other_mode=$([[ "$mode" == "code" ]] && echo "design" || echo "code")
  local other_state_file="$(wt_rotation_state "$other_mode")"

  # Agent-specific default fallback.
  local default_model
  if [[ "$mode" == "design" ]]; then
    default_model="${WT_DEFAULT_DESIGN:-native:${WT_AGENT_NAME:-wt}}"
  else
    default_model="${WT_DEFAULT_CODE:-native:${WT_AGENT_NAME:-wt}}"
  fi

  # Handle missing config file — use agent defaults.
  if [[ ! -f "$config_file" ]]; then
    echo "$default_model"
    return
  fi

  source "$config_file"

  local model_var
  if [[ "$mode" == "code" ]]; then
    model_var="CODE_MODELS[@]"
  else
    model_var="DESIGN_MODELS[@]"
  fi

  local models=("${!model_var}")
  local num_models=${#models[@]}

  # Empty rotation array — fall back to agent defaults.
  if [[ "$num_models" -eq 0 ]]; then
    echo "$default_model"
    return
  fi

  # Read current index for this mode.
  local current_index=0
  if [[ -f "$state_file" ]]; then
    current_index=$(sed -n '1p' "$state_file" 2>/dev/null || echo 0)
    if ! [[ "$current_index" =~ ^[0-9]+$ ]] || [[ "$current_index" -ge "$num_models" ]]; then
      current_index=0
    fi
  fi

  # Read other mode's last selected model for cross-rotation skip.
  local other_last=""
  if [[ -f "$other_state_file" ]]; then
    other_last=$(sed -n '2p' "$other_state_file" 2>/dev/null || echo "")
  fi

  # Cache ollama models list once.
  local ollama_models
  ollama_models=$(ollama list 2>/dev/null | awk 'NR>1 {print $1}')

  local selected=""
  local attempts=0
  local fallback_selected=""
  local fallback_index=-1

  while [[ $attempts -lt $num_models ]]; do
    selected="${models[$current_index]}"

    # Skip models not usable by this agent (e.g. native:claude when agent is pi).
    if [[ "$selected" == native:* && "$selected" != "native:${WT_AGENT_NAME:-wt}" ]]; then
      current_index=$(((current_index + 1) % num_models))
      attempts=$((attempts + 1))
      continue
    fi

    # Cloud model — verify it is available in ollama.
    if [[ "$selected" != native:* ]]; then
      if ! echo "$ollama_models" | grep -qFx "$selected"; then
        printf '%s: model "%s" not in ollama — skipping\n' "${WT_NAME:-wt}" "$selected" >&2
        current_index=$(((current_index + 1) % num_models))
        attempts=$((attempts + 1))
        continue
      fi
    fi

    # Record first usable model as cross-rotation fallback.
    if [[ -z "$fallback_selected" ]]; then
      fallback_selected="$selected"
      fallback_index=$current_index
    fi

    # Cross-rotation skip: prefer a model the other mode didn't just use.
    if [[ -n "$other_last" && "$selected" == "$other_last" ]]; then
      current_index=$(((current_index + 1) % num_models))
      attempts=$((attempts + 1))
      continue
    fi

    break
  done

  if [[ $attempts -ge $num_models ]]; then
    if [[ -n "$fallback_selected" ]]; then
      selected="$fallback_selected"
      current_index=$fallback_index
    else
      printf '%s: no usable model found in %s rotation\n' "${WT_NAME:-wt}" "$mode" >&2
      return 1
    fi
  fi

  # Advance index for next invocation.
  local next_index=$(((current_index + 1) % num_models))
  printf '%s\n%s\n' "$next_index" "$selected" >"$state_file"

  echo "$selected"
}
handle_create_or_use_worktree() {
  local name="$1"
  shift
  local path
  path="$(ensure_worktree_for_name "$name")"
  handle_worktree_selection "$path" "$@"
}

# Reads formatted rows on stdin (4 tab-separated columns).
# Runs fzf, displaying only column 4. Echoes the chosen full row to stdout.
# Exits with fzf's exit code (130 on Esc/Ctrl-C).
select_entry() {
  fzf --ansi \
    --delimiter=$'\t' \
    --with-nth=4 \
    --no-multi \
    --header="${1:-Pick a worktree or branch (Enter to select, Esc to cancel)}" \
    --prompt='wt> '
}

# $1 = branch name. Prompts via fzf with two options.
# Stdout: "yes" if user picked Create, "no" otherwise.
confirm_create_worktree() {
  local branch="$1"
  local choice
  choice="$(printf 'Create worktree for %s\nCancel\n' "$branch" |
    fzf --no-multi --prompt='wt> ' --header="Branch '$branch' has no worktree.")" || return 0
  case "$choice" in
    Create*) printf 'yes\n' ;;
    *) printf 'no\n' ;;
  esac
}

# Helper: invokes wt_exec with optional yolo flag prepended.
_wt_exec_with_yolo() {
  local yolo_flag=""
  if [[ "${WT_YOLO:-0}" -eq 1 ]]; then
    yolo_flag="$(wt_yolo_flag)"
  fi
  if [[ -n "$yolo_flag" ]]; then
    wt_exec "$yolo_flag" "$@"
  else
    wt_exec "$@"
  fi
}

# $1 = path. cd's into it, calls optional wt_pre_exec hook, then wt_exec.
# Remaining args forwarded to wt_exec (with yolo flag prepended if --yolo was given).
handle_worktree_selection() {
  local path="$1"
  shift
  [[ -d "$path" ]] || die "worktree path is not a directory: $path"
  cd "$path"

  # Optional session resume hook (only claude-wt defines this).
  if declare -f wt_pre_exec >/dev/null 2>&1; then
    wt_pre_exec "$path" "$@"
    # If wt_pre_exec exec'd, we never reach here.
    # If it returned, fall through to wt_exec.
  fi

  _wt_exec_with_yolo "$@"
}

# $1 = branch name. Remaining args forwarded to wt_exec via handle_worktree_selection.
# If branch contains a / (e.g. origin/feature), it's a remote branch — create
# a local tracking branch with the short name.
handle_branch_selection() {
  local branch="$1"
  shift
  local choice
  choice="$(confirm_create_worktree "$branch")"
  [[ "$choice" != "yes" ]] && exit 0

  local root path short_name
  root="$(git rev-parse --show-toplevel)"

  # Check if this is a remote branch (e.g. origin/feature) vs a local
  # branch with a slash (e.g. feature/my-branch).
  if git show-ref --verify --quiet "refs/heads/$branch"; then
    # Local branch (possibly with slashes) — use last component for directory.
    short_name="${branch##*/}"
    path="$root/.worktrees/$short_name"
    git worktree add "$path" "$branch" >/dev/null || die "git worktree add failed"
  elif git show-ref --verify --quiet "refs/remotes/$branch"; then
    # Remote tracking branch — create local branch tracking it.
    short_name="${branch#*/}"
    # Safety: reject names that contain path separators or traversal after extraction
    [[ "$short_name" =~ / ]] && die "worktree name must not contain path separators: $short_name"
    if git show-ref --verify --quiet "refs/heads/$short_name"; then
      die "local branch '$short_name' already exists — cannot create from remote '$branch'"
    fi
    path="$root/.worktrees/$short_name"
    git worktree add -b "$short_name" "$path" "$branch" >/dev/null || die "git worktree add failed"
  else
    # Local branch without slashes — use as-is.
    short_name="$branch"
    path="$root/.worktrees/$short_name"
    git worktree add "$path" "$branch" >/dev/null || die "git worktree add failed"
  fi

  handle_worktree_selection "$path" "$@"
}

# Main entry point. Wrappers set WT_NAME and call this.
wt_main() {
  # Wrapper must set WT_NAME before calling wt_main, e.g.:
  #   WT_NAME="$(basename "$0")"
  [[ -z "${WT_NAME:-}" ]] && die "WT_NAME must be set by wrapper"

  # Consume wt-core flags before checking deps so --init can run without
  # the agent binary being installed.
  parse_wt_args "$@"
  set -- ${WT_PASSTHROUGH_ARGS[@]+"${WT_PASSTHROUGH_ARGS[@]}"}

  # --init: seed instruction files and exit (no agent binary needed).
  if [[ "${WT_INIT:-0}" -eq 1 ]]; then
    [[ -n "${WT_WORKTREE_NAME:-}" ]] &&
      printf '%s: -w is ignored with --init\n' "$WT_NAME" >&2
    if ! command -v git >/dev/null 2>&1; then
      die "git is required"
    fi
    if ! git rev-parse --show-toplevel >/dev/null 2>&1; then
      die "--init requires a git working tree"
    fi
    seed_agent_instructions
    # Auto-install the main guard, same as a normal launch would.
    if ! guard_status; then
      local installer="${SCRIPT_DIR:-}/wt-install-guard"
      if [[ -x "$installer" ]]; then
        bash "$installer" >/dev/null 2>&1 ||
          printf '%s: failed to auto-install main guard\n' "$WT_NAME" >&2
      fi
    fi
    exit 0
  fi

  # Check tool binary exists.
  wt_check_deps

  # --no-guard: remove guard and exit.
  if [[ "${WT_NO_GUARD:-0}" -eq 1 ]]; then
    if ! command -v git >/dev/null 2>&1; then
      die "git is required"
    fi
    if ! git rev-parse --git-dir >/dev/null 2>&1; then
      die "--no-guard must be run inside a git repository"
    fi
    local installer="${SCRIPT_DIR:-}/wt-install-guard"
    [[ -x "$installer" ]] || die "installer not found or not executable: $installer"
    exec "$installer" --uninstall
  fi

  # Auto-install main guard when inside a git repo.
  # Skip when --check-guard is active so the flag remains a read-only diagnostic.
  if [[ "${WT_CHECK_GUARD:-0}" -eq 0 ]] && git rev-parse --git-dir >/dev/null 2>&1; then
    if ! guard_status; then
      local installer="${SCRIPT_DIR:-}/wt-install-guard"
      if [[ -x "$installer" ]]; then
        if ! bash "$installer" >/dev/null 2>&1; then
          printf '%s: failed to auto-install main guard\n' "$WT_NAME" >&2
        fi
      else
        printf '%s: wt-install-guard not found — main guard cannot be auto-installed.\n' "$WT_NAME" >&2
        printf '%s: Copy bin/wt-install-guard to the same directory as %s and chmod +x it.\n' "$WT_NAME" "$WT_NAME" >&2
      fi
    fi
  fi

  # --check-guard: report status and exit.
  if [[ "${WT_CHECK_GUARD:-0}" -eq 1 ]]; then
    if guard_status; then
      printf '%s: main guard is installed in this repo.\n' "$WT_NAME"
    else
      printf '%s: main guard is NOT installed in this repo.\n' "$WT_NAME"
      exit 1
    fi
    exit 0
  fi

  # -w/--worktree was given: must be in a repo, skip the picker entirely.
  if [[ -n "${WT_WORKTREE_NAME:-}" ]]; then
    git rev-parse --git-dir >/dev/null 2>&1 ||
      die "-w/--worktree requires a git repository"
    handle_create_or_use_worktree "$WT_WORKTREE_NAME" "$@"
  fi

  # --cwd: run in current directory, skip the picker.
  if [[ "${WT_CWD:-0}" -eq 1 ]]; then
    git rev-parse --git-dir >/dev/null 2>&1 ||
      die "--cwd requires a git repository"
    handle_worktree_selection "$(git rev-parse --show-toplevel)" "$@"
  fi

  # Outside a git repo: pure passthrough.
  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    _wt_exec_with_yolo "$@"
  fi

  # Inside a repo: fzf becomes mandatory.
  command -v fzf >/dev/null 2>&1 || die "fzf not found in PATH; install with: brew install fzf"

  local entries formatted
  entries="$(gather_entries)"

  # No entries → no repo state worth choosing among.
  if [[ -z "$entries" ]]; then
    _wt_exec_with_yolo "$@"
    return
  fi

  # Single entry, and it is the cwd itself → nothing to choose.
  # Exception: if we are on the default branch, force the picker
  # so the user sees the warning and has to make an explicit choice.
  local on_default_branch=0
  if [[ "$(wc -l <<<"$entries")" -eq 1 ]]; then
    local only_type="${entries%%$'\t'*}"
    if [[ "$only_type" == "current" ]]; then
      local only_branch="${entries#*$'\t'}"
      only_branch="${only_branch%%$'\t'*}"
      local default_b
      default_b="$(default_branch)"
      if [[ "$only_branch" == "$default_b" && -n "$default_b" ]]; then
        on_default_branch=1
      else
        handle_worktree_selection "$(git rev-parse --show-toplevel)" "$@"
      fi
    fi
  fi

  formatted="$(printf '%s\n' "$entries" | format_entries)"

  local fzf_header
  fzf_header="Pick a worktree or branch (Enter to select, Esc to cancel)"
  if [[ "$on_default_branch" -eq 1 ]]; then
    fzf_header="WARNING: You are on the default branch ($(default_branch)).\nLaunching here risks committing directly to main.\nSelect a different branch or worktree below.\n\n$fzf_header"
  fi

  local picked
  picked="$(printf '%s\n' "$formatted" | select_entry "$fzf_header")" || exit $?

  # picked = TYPE\tBRANCH\tPATH\t<display>
  local type branch path
  IFS=$'\t' read -r type branch path _ <<<"$picked"

  # Contextual check: if on default branch, verify guard status — notice-only if
  # installed, warn+confirm if interactive without guard, warn+proceed otherwise.
  if [[ "$type" == "current" ]]; then
    local b
    b="$(git -C "$path" branch --show-current 2>/dev/null)" || die "failed to detect branch in $path — worktree may be corrupted"
    if [[ "$b" == "$(default_branch)" && -n "$b" ]]; then
      if guard_status; then
        printf '%s: main guard is installed — commits to %s are blocked.\n' "$WT_NAME" "$b" >&2
      else
        local warn_msg="WARNING: no main guard installed — commits to $b are NOT blocked."
        if [[ -t 0 ]]; then
          printf '%s: %s\n' "$WT_NAME" "$warn_msg" >&2
          local choice
          choice="$(printf 'Proceed anyway\nCancel\n' | fzf --no-multi --prompt='wt> ' --header="Launch on $b without commit protection?")" || exit 0
          [[ "$choice" != "Proceed anyway" ]] && exit 0
        else
          printf '%s: %s\n' "$WT_NAME" "$warn_msg" >&2
        fi
      fi
    fi
  fi

  case "$type" in
    current | worktree) handle_worktree_selection "$path" "$@" ;;
    branch) handle_branch_selection "$branch" "$@" ;;
    *) die "unknown selection type: $type" ;;
  esac
}
