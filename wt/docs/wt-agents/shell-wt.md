# shell-wt

## Overview

`shell-wt` is the worktree launcher for executing a single command with arguments. Unlike other `*-wt` wrappers, it does not launch an AI agent. Instead, it presents the worktree/branch picker, changes into the chosen directory, and executes a command. With no command, it opens an interactive `bash` in the chosen directory.

Because it has no model layer, `shell-wt` works on a fresh machine without a model registry; model-driven agents still need one (`wt model init` creates `registry.toml`). Shell metacharacters (`|`, `>`, `&&`, etc.) are not interpreted as shell syntax — the arguments are passed to the command verbatim as argv, with no shell and no quoting step. To run pipelines or redirections, use an explicit shell: `shell-wt -- bash -lc 'cmd1 | cmd2'`.

## Installation

`shell-wt` is a one-line shim that forwards to the `wt` binary (`exec wt --agent shell "$@"`). Build `wt` and put it on `$PATH` (see the repo's top-level `CLAUDE.md`), then put `bin/shell-wt` on `$PATH` too, e.g. via `make install`.

## Usage

```bash
# Simple command (no -- needed when no argument starts with a dash and the
# first word is not one of wt's own; see below)
shell-wt ls

# Any dash-prefixed argument needs -- (`wt`'s flag parser would otherwise
# read it as a wt flag: -la is rejected as an unknown flag, and --init
# would be taken as wt's own --init)
shell-wt -- ls -la
shell-wt -- rm --init

# No command: open an interactive bash in the chosen worktree
shell-wt -W my-feature

# Skip picker, use/create a named worktree
shell-wt -W my-feature -- make test

# Run in the current directory (skip picker)
shell-wt --cwd -- npm test
```

After `--`, every word is the command, whatever its name. Without `--`, `wt`
reads the first word before it treats it as a command, so two kinds of first
word need the `--`:

- the name of a `wt` subcommand (`config`, `start`, `stop`, `stats`, `model`
  and the rest of `wt --help`'s list): `shell-wt config` opens `wt config`,
  `shell-wt -- config` runs a command named `config`;
- `models` and `agents`, two subcommands `wt` no longer has: `shell-wt models`
  exits with ``wt models is removed; use `wt config` to view models``,
  `shell-wt -- models` runs a command named `models`.

A `--` placed after such a word does not help (`shell-wt models -- x` is
still refused): put it before the command.

## Command execution

`shell-wt` runs the passthrough args directly as argv in a child process — no shell is involved, so there is no re-quoting step. When the command exits, `wt` prints its one-line summary (`wt: shell · <duration>`) and exits with the command's exit code.

> **Limitation:** Because the command is run directly (not interpreted by a shell), shell metacharacters (`|`, `>`, `&&`, etc.) are treated as literal argument characters. To use pipelines, redirections, or other shell features, wrap the command in an explicit shell invocation:
>
> ```bash
> shell-wt -- bash -lc 'cmd1 | cmd2 > output.txt'
> ```

## Supported flags

| Flag | Description |
|------|-------------|
| `-W <name>`, `--worktree <name>` | Use/create worktree for branch, skip worktree picker |
| `--cwd` | Run in the current directory, skip worktree picker |
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
model rotation is a single global sequence and shell has no model concept.

## Verified on this machine

**Verified on this machine, 2026-08-16.**
