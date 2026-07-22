# shell-wt Design

## Summary

A `shell-wt` launcher that reuses `wt-core.sh` for worktree/branch selection, then executes a shell command in the chosen directory and exits. No AI agent is launched. This fills a gap where a user wants the worktree picker UI for directory selection but only needs to run a shell command.

## Motivation

The existing `*-wt` wrappers all exec into AI coding agents. There is no way to use the worktree/branch picker to select a directory, run a one-shot command there, and return. For example:

- Run `npm test` in a feature branch worktree
- Run `make build` in a branch that has no worktree yet (create on demand)
- Run `git log` across multiple branches via the picker

## Design

### Launcher contract

`shell-wt` sources `wt-core.sh` and implements the required contract:

| Item | Value |
|---|---|
| `WT_NAME` | `shell-wt` |
| `WT_DEFAULT_CODE` | *not set* (no rotation) |
| `WT_AGENT_NAME` | *not set* (no rotation) |
| `wt_check_deps()` | verify `bash` is available |
| `wt_yolo_flag()` | empty string |
| `wt_exec()` | parse `--`, error if missing, execute command |

Flags inherited from `wt-core.sh`:
- `-w <name>`, `--worktree <name>` — use/create worktree for branch, skip picker
- `--cwd` — run in current repo root, skip picker
- `--init` — seed agent instruction files (`AGENTS.md`) and exit
- `--no-guard` — remove the main-branch commit guard
- `--check-guard` — report whether the guard is installed

Flags **ignored** (no model rotation or permission prompts to skip):
- `--code`, `--design`, `--native` — no-op because `WT_DEFAULT_CODE` is not set
- `--yolo` — no-op because `shell-wt` has no permission prompts

### Command parsing

`parse_wt_args` collects unknown args into `WT_PASSTHROUGH_ARGS`. `wt_exec` receives them as `"$@"`:

```bash
wt_exec() {
  if [[ $# -eq 0 ]]; then
    die "usage: shell-wt [-w <name>] [--cwd] -- <command>"
  fi
  if [[ "$1" == "--" ]]; then
    shift
  fi
  if [[ $# -eq 0 ]]; then
    die "usage: shell-wt [-w <name>] [--cwd] -- <command>"
  fi
  local cmd=""
  for arg in "$@"; do
    cmd+="$(printf '%q ' "$arg")"
  done
  exec bash -c "$cmd"
}
```

The `cd` into the chosen directory is already handled by `handle_worktree_selection` before `wt_exec` is called.

The `--` separator is required when the command contains flag-like arguments (e.g. `shell-wt -- rm --init`). For simple commands with no leading dashes, `--` may be omitted: `shell-wt ls -la` works because `ls` is not a launcher flag. If `shell-wt --init` is passed without `--`, `--init` is consumed by `parse_wt_args` as a launcher flag, which is a no-op for `shell-wt`.

### Execution behavior

- Runs `exec bash -c "<command>"` with arguments individually shell-quoted via `printf '%q'` — replaces the launcher process with the shell running the command.
- The shell's exit code propagates naturally (no wrapper process remaining).
- Interactive TTY is preserved (the shell inherits stdin/stdout/stderr).

### Error handling

- No command provided after `--` → die with usage message.
- Outside a git repo with no `-w`/`--cwd` → falls through to `wt_main` passthrough path, which runs the command in the current directory.
- Worktree creation failure → handled by `ensure_worktree_for_name` in `wt-core.sh`.

### Files added

- `bin/shell-wt` — new launcher script (~20 lines, patterned after `agy-wt`)
- `docs/wt-agents/shell-wt.md` — reference doc

## Open Questions

None.

## Trade-offs

### `--` required vs. implicit passthrough

**Chosen:** Explicit `--` with tolerance for simple commands.

- The `agy-wt` style of pure passthrough works because `agy` flags never collide with the command flags (in practice). For a shell command, `--init` is a very plausible command argument (e.g. `rm --init` or a script flag). `--` removes ambiguity.
- The `-c` flag (like `sh -c`) was rejected because nested quoting is painful and it deviates from the passthrough pattern used by every other `*-wt` wrapper.
