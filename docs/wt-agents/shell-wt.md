# shell-wt

## Overview

`shell-wt` is the worktree launcher for executing a single command with arguments. Unlike other `*-wt` wrappers, it does not launch an AI agent. Instead, it presents the worktree/branch picker, changes into the chosen directory, and executes a command. Shell metacharacters (`|`, `>`, `&&`, etc.) are not interpreted as shell syntax — each argument is shell-quoted individually. To run pipelines or redirections, use an explicit shell: `shell-wt -- bash -lc 'cmd1 | cmd2'`.

## Installation

`shell-wt` is a standalone script. Copy `bin/shell-wt` into `~/.local/bin/` alongside `wt-core.sh`.

## Usage

```bash
# Simple command (no -- needed if no flag-like args)
shell-wt ls -la

# Command with flag-like arguments
shell-wt -- rm --init

# Skip picker, use/create a named worktree
shell-wt -w my-feature -- make test

# Run in current repo root (skip picker)
shell-wt --cwd -- npm test
```

## Command execution

`shell-wt` runs `exec bash -c "<command>"` with arguments individually shell-quoted via `printf '%q'`. The launcher process is replaced by the shell, so exit codes propagate naturally.

> **Limitation:** Because each argument is shell-quoted, shell metacharacters (`|`, `>`, `&&`, etc.) are treated as literal characters rather than shell syntax. To use pipelines, redirections, or other shell features, wrap the command in an explicit shell invocation:
>
> ```bash
> shell-wt -- bash -lc 'cmd1 | cmd2 > output.txt'
> ```

## Supported flags

| Flag | Description |
|------|-------------|
| `-w <name>`, `--worktree <name>` | Use/create worktree for branch, skip picker |
| `--cwd` | Run in current repo root, skip picker |
| `--init` | Seed agent instruction files (AGENTS.md) and exit |
| `--no-guard` | Remove main-branch commit guard |
| `--check-guard` | Report guard status |
| `--code`, `--design`, `--native` | No-op (no model rotation) |
| `--yolo` | No-op (no permission prompts) |

## Verified on this machine

**Verified on this machine, 2026-07-22.**
