# opencode-wt Launcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create an `opencode-wt` launcher that wraps OpenCode CLI with git worktree management, model rotation, session resume, and permission-skip support.

**Architecture:** Follows the claude-wt pattern: source `wt-core.sh`, declare contract globals, implement `wt_check_deps`, `wt_yolo_flag`, `wt_exec`, and `wt_pre_exec`. Uses `OPENCODE_CONFIG_CONTENT` env var to pass ollama config inline. Session resume uses git commit hash (OpenCode's project ID) instead of path-based slug.

**Tech Stack:** Bash, git worktree, fzf, OpenCode CLI

---

### Task 1: Create the opencode-wt launcher script

**Files:**
- Create: `bin/opencode-wt`

- [ ] **Step 1: Write `bin/opencode-wt`**

```bash
#!/usr/bin/env bash
# opencode-wt — launch OpenCode in a chosen worktree or branch.
# Usage: opencode-wt [-w/--worktree <name>] [--cwd] [--yolo] [--code] [--design] [--native] [opencode-args...]
#
# Install: copy to ~/.local/bin/opencode-wt alongside wt-core.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Bootstrap guard: wt-core.sh must be co-located
if [[ ! -f "$SCRIPT_DIR/wt-core.sh" ]]; then
  printf '%s: wt-core.sh not found at %s/wt-core.sh\n' "$(basename "$0")" "$SCRIPT_DIR" >&2
  printf '%s: Ensure both wt-core.sh and %s are copied to the same directory.\n' "$(basename "$0")" "$(basename "$0")" >&2
  exit 1
fi

# Model rotation defaults for opencode — must be set before sourcing wt-core.sh.
WT_DEFAULT_CODE="native:opencode"
WT_DEFAULT_DESIGN="native:opencode"
WT_AGENT_NAME="opencode"

source "$SCRIPT_DIR/wt-core.sh"

WT_NAME="$(basename "$0")"

wt_check_deps() {
  command -v opencode >/dev/null 2>&1 || die "opencode not found. Install: curl -fsSL https://opencode.ai/install | bash"
}

wt_yolo_flag() {
  echo "--dangerously-skip-permissions"
}

# Config helper: read PROVIDER_OLLAMA_BASE_URL from models.conf.
_ollama_base_url() {
  local base="http://localhost:11434"
  if [[ -f "$(wt_config_dir)/models.conf" ]]; then
    local configured
    configured=$(source "$(wt_config_dir)/models.conf" && printf '%s' "${PROVIDER_OLLAMA_BASE_URL:-}")
    [[ -n "$configured" ]] && base="$configured"
  fi
  printf '%s' "${base%/v1}"
}

# Find OpenCode sessions for the current worktree.
# OpenCode sessions are stored in ~/.local/share/opencode/storage/session/<project-id>/
# where project-id is the git commit hash of the repo's root commit.
_find_opencode_sessions() {
  local path="$1"
  local project_id
  project_id="$(git -C "$path" rev-list --max-parents=0 --all 2>/dev/null | sort | head -1)" || return
  [[ -z "$project_id" ]] && return

  local dir="$HOME/.local/share/opencode/storage/session/$project_id"
  [[ -d "$dir" ]] || return

  # Find newest session by mtime.
  local newest
  newest="$(find "$dir" -maxdepth 1 -name '*.json' -exec stat -f '%m%t%N' {} + 2>/dev/null |
    sort -rn |
    head -1)" || true

  [[ -z "$newest" ]] && return

  local mtime="${newest%%$'\t'*}"
  local filepath="${newest#*$'\t'}"
  local basename
  basename="$(basename "$filepath" .json)"
  printf '%s\t%s\n' "$basename" "$mtime"
}

wt_exec() {
  local model_to_use=""

  # --native flag: use agent's native model, error if not configured.
  if [[ "${WT_NATIVE:-0}" -eq 1 ]]; then
    local config_file="$(wt_config_dir)/models.conf"
    if [[ -f "$config_file" ]]; then
      source "$config_file"
      if [[ -n "${NATIVE_OPENCODE:-}" ]]; then
        model_to_use="$NATIVE_OPENCODE"
      else
        die "--native requires NATIVE_OPENCODE to be configured in models.conf"
      fi
    else
      die "--native requires models.conf with NATIVE_OPENCODE configured"
    fi
  elif [[ -n "${WT_MODEL_MODE:-}" ]]; then
    model_to_use=$(get_model_from_rotation) || exit 1
  fi

  if [[ -n "$model_to_use" ]]; then
    if [[ "$model_to_use" == native:* && "$model_to_use" != "native:opencode" ]]; then
      printf '%s: skipping "%s" (not an opencode model)\n' "$WT_NAME" "$model_to_use" >&2
      exec opencode "$@"
    fi
    if [[ "$model_to_use" == "native" || "$model_to_use" == "native:opencode" ]]; then
      exec opencode "$@"
    fi
    # Ollama model — pass config inline via OPENCODE_CONFIG_CONTENT.
    local base_url
    base_url="$(_ollama_base_url)"
    exec env \
      OPENCODE_CONFIG_CONTENT='{"model":"ollama/'"$model_to_use"'","provider":{"ollama":{"options":{"baseURL":"'"$base_url"'/v1","apiKey":""}}}}' \
      opencode "$@"
  fi
  exec opencode "$@"
}

wt_pre_exec() {
  local path="$1"
  shift

  # Skip session resume prompt when --cwd is used.
  [[ "${WT_CWD:-0}" -eq 1 ]] && return

  # Skip session resume when a model mode is explicitly requested.
  [[ -n "${WT_MODEL_MODE:-}" || "${WT_NATIVE:-0}" -eq 1 ]] && return

  local session_info
  session_info="$(_find_opencode_sessions "$path")"
  if [[ -z "$session_info" ]]; then
    return
  fi

  local session_id mtime branch choice
  IFS=$'\t' read -r session_id mtime <<<"$session_info"
  branch="$(git -C "$path" branch --show-current 2>/dev/null || basename "$path")"
  choice="$(session_resume_prompt "$branch" "$session_id" "$mtime")"
  [[ -z "$choice" ]] && return

  # --native flag: read NATIVE_OPENCODE directly.
  local model_to_use=""
  if [[ "${WT_NATIVE:-0}" -eq 1 ]]; then
    local config_file="$(wt_config_dir)/models.conf"
    if [[ -f "$config_file" ]]; then
      source "$config_file"
      if [[ -n "${NATIVE_OPENCODE:-}" ]]; then
        model_to_use="$NATIVE_OPENCODE"
      else
        die "--native requires NATIVE_OPENCODE to be configured in models.conf"
      fi
    else
      die "--native requires models.conf with NATIVE_OPENCODE configured"
    fi
  fi

  if [[ -n "$model_to_use" ]]; then
    if [[ "$model_to_use" == native:* && "$model_to_use" != "native:opencode" ]]; then
      printf '%s: skipping "%s" (not an opencode model)\n' "$WT_NAME" "$model_to_use" >&2
    elif [[ "$model_to_use" != "native" && "$model_to_use" != "native:opencode" ]]; then
      local base_url
      base_url="$(_ollama_base_url)"
      exec env \
        OPENCODE_CONFIG_CONTENT='{"model":"ollama/'"$model_to_use"'","provider":{"ollama":{"options":{"baseURL":"'"$base_url"'/v1","apiKey":""}}}}' \
        opencode --continue "$@" 
    fi
  fi
  exec opencode --continue "$@"
}

wt_main "$@"
```

- [ ] **Step 2: Make executable**

```bash
chmod +x bin/opencode-wt
```

- [ ] **Step 3: Verify syntax**

```bash
bash -n bin/opencode-wt
```

Expected: no output (syntax OK)

- [ ] **Step 4: Commit**

```bash
git add bin/opencode-wt
git commit -m "feat: add opencode-wt launcher"
```

---

### Task 2: Create per-agent reference doc

**Files:**
- Create: `docs/wt-agents/opencode-wt.md`

- [ ] **Step 1: Write `docs/wt-agents/opencode-wt.md`**

```markdown
# opencode-wt

## Overview

`opencode-wt` is the worktree launcher for [OpenCode](https://opencode.ai) ([anomalyco/opencode](https://github.com/anomalyco/opencode)), an open-source, provider-agnostic AI coding agent. OpenCode supports 75+ LLM providers and runs as a terminal app, desktop app, or IDE extension. See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
curl -fsSL https://opencode.ai/install | bash
```

This installs the `opencode` binary. The `opencode-wt` launcher in this repo wraps `opencode` with worktree-aware FZF selection and code/design model rotation. Install `opencode-wt` itself by copying `bin/opencode-wt` (and the rest of `bin/`) into `~/.local/bin/`.

Alternative install methods: `npm install -g opencode-ai@latest`, `brew install anomalyco/tap/opencode`, etc.

## Configuration files & locations

OpenCode reads from `~/.config/opencode/`. Key files:

| File / directory | Purpose |
|---|---|
| `~/.config/opencode/opencode.json` | Global config: providers, models, permissions, plugins, agents, tools. |
| `opencode.json` (project root) | Per-project overrides. |
| `~/.local/share/opencode/auth.json` | API keys configured via `/connect` command. |
| `~/.local/share/opencode/storage/` | Sessions, messages, parts, diffs. |

Config format is JSON/JSONC. OpenCode deep-merges config sources: remote → global → custom → project → inline.

## Authentication & credentials

OpenCode stores API keys in `~/.local/share/opencode/auth.json`, configured interactively via the `/connect` command in the TUI. Alternatively, environment variables can be referenced in config:

```json
{
  "provider": {
    "anthropic": {
      "options": {
        "apiKey": "{env:ANTHROPIC_API_KEY}"
      }
    }
  }
}
```

The `opencode-wt` launcher does not manage credentials — it relies on the user having configured providers via `/connect` or environment variables.

## Model selection

OpenCode selects models via `--model provider/model` (e.g., `--model anthropic/claude-sonnet-4-5`). `opencode-wt` handles two cases:

- **`native:opencode`** — no `--model` flag; OpenCode uses its configured default.
- **Ollama models** — generates inline JSON via `OPENCODE_CONFIG_CONTENT` environment variable:

```json
{"model":"ollama/<model>","provider":{"ollama":{"options":{"baseURL":"<url>/v1","apiKey":""}}}}
```

The Ollama base URL is read from `PROVIDER_OLLAMA_BASE_URL` in `~/.config/agent-wt/models.conf`, defaulting to `http://localhost:11434`. OpenCode deep-merges this inline config with existing config files, so user settings in `~/.config/opencode/opencode.json` are preserved.

## Session resume

`opencode-wt` implements `wt_pre_exec` (like `claude-wt`). When entering a worktree with prior OpenCode sessions, it prompts via fzf to **Resume** or **Start fresh**. Sessions are detected by git commit hash (OpenCode's project ID), not path-based slug.

**Model-rotation interaction:** When `--code`, `--design`, or `--native` is explicitly requested, session resume is skipped — same behavior as `claude-wt`.

## Agent init

`opencode-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)

OpenCode reads `AGENTS.md` natively and also has its own `/init` command for project-specific setup. No pointer file is needed.

## Verified on this machine

**Not installed on this machine.** Statements above are sourced from the [OpenCode docs](https://opencode.ai/docs) and the [Ollama integration guide](https://docs.ollama.com/integrations/opencode). Re-verify after installing OpenCode.
```

- [ ] **Step 2: Commit**

```bash
git add docs/wt-agents/opencode-wt.md
git commit -m "docs: add opencode-wt reference doc"
```

---

### Task 3: Update index files

**Files:**
- Modify: `docs/wt-agents/README.md`
- Modify: `docs/wt-agents/supported-agents.md`

- [ ] **Step 1: Add opencode-wt row to README table**

In `docs/wt-agents/README.md`, add a row to the Agents table after pi-wt:

```
| OpenCode | [opencode-wt.md](opencode-wt.md) | Not installed on this machine (2026-06-11) |
```

- [ ] **Step 2: Add OpenCode to supported-agents.md**

In `docs/wt-agents/supported-agents.md`, add a row to the table before Antigravity CLI:

```
| OpenCode | `opencode-wt` | [opencode.ai](https://opencode.ai) | [anomalyco/opencode](https://github.com/anomalyco/opencode) | `curl -fsSL https://opencode.ai/install \| bash` |
```

And add a note bullet to the Notes section:

```
- **opencode** — npm scope is `opencode-ai`; GitHub org is `anomalyco`.
```

- [ ] **Step 3: Commit**

```bash
git add docs/wt-agents/README.md docs/wt-agents/supported-agents.md
git commit -m "docs: add OpenCode to agent index and supported agents reference"
```

---

### Task 4: Verify end-to-end

**Files:**
- Verify: `bin/opencode-wt`

- [ ] **Step 1: Check lint**

```bash
shellcheck bin/opencode-wt
```

Expected: no errors (warnings about SC1090, SC1091, SC2034, SC2155 may appear — these are expected per project conventions)

- [ ] **Step 2: Check format**

```bash
shfmt -d -i 2 -ci bin/opencode-wt
```

Expected: no diff output

- [ ] **Step 3: Verify help flag (requires opencode installed)**

```bash
# If opencode is installed:
opencode-wt --help  # should show opencode help, not error
# If opencode is NOT installed:
bash bin/opencode-wt --help  # should die with install hint
```

Expected (not installed): `opencode-wt: opencode not found. Install: curl -fsSL https://opencode.ai/install | bash`

- [ ] **Step 4: Commit any fixes**

```bash
git add -A
git commit -m "chore: lint/format fixes for opencode-wt"
```
