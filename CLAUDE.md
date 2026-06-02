# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

Shell scripts that wrap AI coding agent CLIs (claude, codex, copilot, pi, agy) with git worktree management. Each `*-wt` launcher presents an fzf picker of worktrees and branches, creates worktrees on demand, optionally selects a model via rotation, and then `exec`s the underlying agent.

## Installation

Copy everything in `bin/` to a single directory on `$PATH` (e.g., `~/.local/bin/`). All scripts must remain co-located — wrappers find `wt-core.sh` and `wt-install-guard` via `SCRIPT_DIR`. If `wt-install-guard` is missing, launchers print a warning and skip auto-installing the main-branch commit guard.

## Architecture

### Plugin pattern

`bin/wt-core.sh` is the shared engine. It is never executed directly — wrappers `source` it and implement a contract before calling `wt_main`. The contract is:

| Global / Function | Required | Purpose |
|---|---|---|
| `WT_DEFAULT_CODE` | Yes (model rotators) | Fallback model for `--code` mode |
| `WT_DEFAULT_DESIGN` | Yes (model rotators) | Fallback model for `--design` mode |
| `WT_AGENT_NAME` | Yes (model rotators) | Agent identifier for model-usability checks (e.g. `claude`) |
| `wt_check_deps()` | Yes | Verify agent binary exists; call `die` with install hint if not |
| `wt_yolo_flag()` | Yes | Echo the tool's skip-permissions flag (or empty string) |
| `wt_exec "$@"` | Yes | Construct and `exec` the final agent launch command |
| `wt_pre_exec()` | No | Hook called after `cd` into worktree, before `wt_exec` (only `claude-wt` defines this) |

### Core flow (`wt_main`)

1. Call `wt_check_deps`
2. Parse flags (`--code`, `--design`, `--native`, `-w`, `--yolo`, `--cwd`, `--no-guard`, `--check-guard`) — all flag parsing is shared in `wt-core.sh`
3. Auto-install `block-main-commit` pre-commit hook via `wt-install-guard`
4. If `-w <name>` given: `ensure_worktree_for_name` then launch — skip fzf
5. If `--cwd` given: launch in current repo root — skip fzf
6. If outside a git repo: pure passthrough to agent — skip fzf
7. Otherwise: `gather_entries` → fzf → `handle_worktree_selection` or `handle_branch_selection`

### Model rotation

Applies to `claude-wt`, `codex-wt`, `copilot-wt`, and `pi-wt` (not `agy-wt`, which has no CLI `--model` flag).

- `get_model_from_rotation()` is in `wt-core.sh` — shared across all model-rotating launchers. It reads `WT_DEFAULT_CODE`, `WT_DEFAULT_DESIGN`, `WT_AGENT_NAME`, and `WT_MODEL_MODE` directly.
- Config: `~/.config/ai-shell/models.conf` — defines `CODE_MODELS`, `DESIGN_MODELS` arrays, `NATIVE_<AGENT>` vars, and `PROVIDER_OLLAMA_BASE_URL` (used by copilot-wt). Respects `XDG_CONFIG_HOME` override.
- State: `~/.config/ai-shell/rotation-{code,design}.state` — two-line file: `<next_index>\n<last_selected>`
- Cross-rotation coordination: each mode checks the other's last-used model and skips it to avoid duplication
- Model values: `native:<agent>` or bare `native` (use agent's own default) or a model name string (cloud/ollama)
- `--code` (default) and `--design` select which rotation list to use
- `--native` bypasses rotation entirely, reads `NATIVE_<AGENT>` from `models.conf`; errors if not configured

### Main guard

`wt-install-guard` writes a `block-main-commit v1` pre-commit hook that blocks commits to `main`/`master`. Launchers auto-install it on every invocation when inside a git repo. The hook can be bypassed by:
- `git commit --no-verify` (emergency)
- `WT_SKIP_MAIN_BLOCK=1` env var (CI/automation)
- `<launcher> --no-guard` (removes the hook via `wt-install-guard --uninstall`)

### Session resume (claude-wt only)

`wt_pre_exec` checks `~/.claude/projects/<slug>/*.jsonl` for prior sessions. The slug is the worktree path with non-alphanumeric chars replaced by `-`. When a session is found, fzf offers "Resume" or "Start fresh." Skipped when `--cwd` is used.

## Key flags (all launchers)

| Flag | Effect |
|---|---|
| `-w <name>`, `--worktree <name>` | Use/create worktree for branch `<name>`; skip fzf |
| `--cwd` | Launch in current repo root; skip fzf and session resume |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--code` | Use code model rotation (default) — rotation-supporting launchers only |
| `--design` | Use design model rotation — rotation-supporting launchers only |
| `--native` | Use `NATIVE_<AGENT>` from `models.conf`; error if not configured — rotation-supporting launchers only |
| `--no-guard` | Remove the main-branch commit guard and exit |
| `--check-guard` | Report guard status and exit |

## Adding a new launcher

1. Copy an existing wrapper (e.g., `agy-wt` for no model rotation, or `claude-wt` for model rotation)
2. Set `SCRIPT_DIR` at the top (via `"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"`)
3. If the agent supports model rotation, set `WT_DEFAULT_CODE`, `WT_DEFAULT_DESIGN`, and `WT_AGENT_NAME` **before** `source "$SCRIPT_DIR/wt-core.sh"` — these must exist before the source so `parse_wt_args` can detect rotation support
4. `source "$SCRIPT_DIR/wt-core.sh"`, then set `WT_NAME="$(basename "$0")"`
5. Implement `wt_check_deps()`, `wt_yolo_flag()`, `wt_exec`
6. Implement `wt_pre_exec()` if the agent has a session concept (only `claude-wt` does)
7. Call `wt_main "$@"`
8. Add a doc file to `docs/wt-agents/`

## Copilot-specific: Ollama model passthrough

`copilot-wt` does not pass `--model` for Ollama models. Instead it sets four environment variables (`COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_PROVIDER_WIRE_API`, `COPILOT_MODEL`) before `exec copilot`. The Ollama base URL is read from `PROVIDER_OLLAMA_BASE_URL` in `models.conf`, defaulting to `http://localhost:11434`.

## No test suite

These are bash scripts; there is no automated test runner. Validate changes by running the launcher manually. Representative invocations:

```bash
# Basic flow — fzf picker inside a git repo
claude-wt

# Skip picker, use/create a named worktree
claude-wt -w my-feature

# Launch in current directory (no worktree switch)
claude-wt --cwd

# Model rotation flags
claude-wt --code
claude-wt --design
claude-wt --native          # requires NATIVE_CLAUDE in models.conf

# Guard management
claude-wt --check-guard
claude-wt --no-guard

# Non-rotation wrapper (agy-wt passes unknown flags through to agy)
agy-wt -w my-feature
```
