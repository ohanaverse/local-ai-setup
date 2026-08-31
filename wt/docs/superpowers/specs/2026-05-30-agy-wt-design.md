# Design: `agy-wt` — Antigravity CLI Worktree Launcher

## Problem Statement

The `*-wt` launcher family (`claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`) provides
interactive git-worktree selection before launching an AI coding agent.
[Antigravity CLI (`agy`)](https://antigravity.google/docs/cli-overview) is a new
terminal-first agent from Google. It should be supported by the same launcher
pattern.

## Goals

- Add `agy-wt` that follows the exact `wt-core.sh` wrapper contract.
- Provide `--yolo` → `--dangerously-skip-permissions` mapping, consistent with
  other wrappers.
- **Do not** implement `AI_CODING_MODEL` passthrough because `agy` does not accept
  `--model` on the CLI (model selection is done inside the TUI via `/model`).
- **Do not** implement a session resume hook because `agy` stores conversation
  state internally, not in a predictable directory.
- Update `bin/README.md` documentation.

## Non-Goals

- Model passthrough / Ollama support.
- Session auto-resume (no scannable session directory).
- Any changes to `wt-core.sh`.

## Architecture

Same thin-wrapper architecture as `pi-wt`:

```
agy-wt  →  sources wt-core.sh  →  wt_main()
                ↑
         defines:
         WT_NAME, wt_check_deps(),
         wt_yolo_flag(), wt_exec()
```

### Wrapper Functions

| Function | Implementation |
|----------|---------------|
| `wt_check_deps()` | `command -v agy` → hint: `curl -fsSL https://antigravity.google/cli/install.sh \| bash` |
| `wt_yolo_flag()` | `echo "--dangerously-skip-permissions"` |
| `wt_exec()` | `exec agy "$@"` (no model logic) |

### `--yolo` Flag Chain

`wt_main()` → `_wt_exec_with_yolo()` → prepends `--dangerously-skip-permissions` → `wt_exec()`

## Changeset

1. **New file:** `bin/agy-wt` (executable, < 50 lines)
2. **Edit:** `bin/README.md`
   - Add `agy-wt` to launcher lists, usage examples, install commands.
   - Add row to agent-specific flags table:
     `agy-wt` | `curl -fsSL https://antigravity.google/cli/install.sh | bash` | `--dangerously-skip-permissions` | *none* |

## Testing Plan

- `bash -n bin/agy-wt`
- `sh -n bin/agy-wt`
- `shellcheck bin/agy-wt`
- Run `python3 scripts/validate_marketplace.py` (structure validation)
- Manual: `bin/agy-wt` in a git repo → worktree picker opens, `agy` launches.
- Manual: `bin/agy-wt --yolo` → `agy` receives `--dangerously-skip-permissions`.
- Manual: outside git repo → `agy` launches directly.

## Risks & Mitigations

| Risk | Mitigation |
|------|-----------|
| `agy` CLI flag changes in future | `wt_exec()` is pure passthrough; only `--yolo` hardcodes a flag. If `agy` renames `--dangerously-skip-permissions`, update `wt_yolo_flag()`. |
| `wt-core.sh` changes break new wrapper | Follows exact wrapper contract; no divergence. |

## Approval

User approved in conversation (2026-05-30).
