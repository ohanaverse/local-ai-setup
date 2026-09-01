# shell-wt

## Overview

`shell-wt` is the worktree launcher for executing a single command with arguments. Unlike other `*-wt` wrappers, it does not launch an AI agent. Instead, it presents the worktree/branch picker, changes into the chosen directory, and executes a command. Shell metacharacters (`|`, `>`, `&&`, etc.) are not interpreted as shell syntax — each argument is shell-quoted individually. To run pipelines or redirections, use an explicit shell: `shell-wt -- bash -lc 'cmd1 | cmd2'`.

## Installation

`shell-wt` is a one-line shim that forwards to the `wt` binary (`exec wt --agent shell "$@"`). Build `wt` and put it on `$PATH` (see the repo's top-level `CLAUDE.md`), then put `bin/shell-wt` on `$PATH` too, e.g. via `make install`.

## Usage

```bash
# Simple command (no -- needed if no flag-like args)
shell-wt ls -la

# Command with flag-like arguments (required: `wt`'s flag parser would
# otherwise try to interpret --init as a wt flag, not a shell-wt argument)
shell-wt -- rm --init

# Skip picker, use/create a named worktree
shell-wt -W my-feature -- make test

# Run in current repo root (skip picker)
shell-wt --cwd -- npm test
```

## Command execution

`shell-wt` execs the passthrough args directly as argv — no shell is involved, so there is no re-quoting step. The launcher process is replaced by the command, so exit codes propagate naturally.

> **Limitation:** Because the command is exec'd directly (not interpreted by a shell), shell metacharacters (`|`, `>`, `&&`, etc.) are treated as literal argument characters. To use pipelines, redirections, or other shell features, wrap the command in an explicit shell invocation:
>
> ```bash
> shell-wt -- bash -lc 'cmd1 | cmd2 > output.txt'
> ```

## Supported flags

| Flag | Description |
|------|-------------|
| `-W <name>`, `--worktree <name>` | Use/create worktree for branch, skip worktree picker |
| `--cwd` | Run in current repo root, skip worktree picker |
| `--init` | Seed agent instruction files (AGENTS.md) and exit |
| `--no-guard` | Remove main-branch commit guard |
| `--check-guard` | Report guard status |
| `--yolo` | No-op (no permission prompts) |

Passing `-M` with `-A shell` (e.g. `shell-wt -M foo -- ls`) is allowed and
ignored — `wt` prints a stderr note `wt: -M ignored for command "shell"`
because model pinning has no meaning for command agents.

Legacy short flag `-w` for `--worktree` has been removed — use `-W` or
`--worktree`. Legacy bash flags `--code`, `--design`, and `--native` are
not supported by `wt` — passing them now exits with `unknown flag`, since
model rotation is slot-based and shell has no model concept.

## Verified on this machine

**Verified on this machine, 2026-08-16.**
