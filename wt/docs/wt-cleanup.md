# `wt cleanup` and `wt remove`

`wt cleanup` lists the repository's worktrees, local branches and `origin`
branches in four groups, by how sure wt is that each can go, with the
evidence for every row. On a terminal the list removes the worktrees and
local branches you tick, after a confirmation. `wt remove` does the same for
targets named on the command line. Both use one removal engine; `origin`
branches are never removed (a later release).

```bash
wt cleanup                  # interactive tick list on a terminal; enter removes the ticked rows
wt cleanup --plain          # text list, also used when output is a pipe (removes nothing)
wt cleanup --json           # one JSON document (printed on failure too; removes nothing)
wt cleanup --no-fetch       # skip `git fetch --prune origin`
wt cleanup --min-age 30m    # raise the fresh-branch threshold (default 10m)

wt remove <worktree-path-or-branch>... --dry-run   # what would happen; changes nothing
wt remove ../repo-wt/topic                         # asks y/N on a terminal
wt remove topic old-spike --yes --accept-loss -D   # non-interactive, with explicit approvals
```

## Groups

| Group | Meaning |
|---|---|
| Proven merged | A rung of the evidence ladder holds: same commit, ancestor of the base, trees match, no added changes, or `merge-tree` adds nothing. |
| Looks merged (less sure) | Only a weak signal (`git cherry`, patch-id), no proof, or a fact wt could not read. |
| Has work that exists nowhere else | Commits not in the base and not on any remote-tracking ref. |
| In use / locked | A primary, current, locked, nested or mid-operation worktree, a held session claim, a registered model artifact (`protected_artifact`), or the default branch (`default_branch`). |

Weak signals are never proofs. Any fact wt cannot establish makes the row
less sure, never proven.

Listing writes no ref, index or working file. The merge-adds-nothing proof
(`git merge-tree --write-tree`) may leave unreachable tree objects in the
object store, which `git gc` collects.

## `wt cleanup` flags

| Flag | Effect |
|---|---|
| `--json` | Machine-readable report: [cleanup-list.schema.json](../../docs/contracts/cleanup-list.schema.json), sample [cleanup-list.sample.json](../../docs/contracts/cleanup-list.sample.json). |
| `--no-fetch` | Skip the fetch. A repository with no `origin` remote skips it anyway; the header says how it went. |
| `--min-age <duration>` | A branch younger than this that sits at or behind the base is never proven merged (a brand-new branch looks merged). At least `1m` (less is a usage error, exit 1); a value below the default `10m` is named in the header. |
| `--plain` | Text output on a terminal. |

## `wt cleanup` exit codes

0: a list was produced. 1: the repository could not be classified (not a
repository, no default branch), or a usage error (an unknown flag, an
argument), which prints no JSON document even under `--json`. A failed fetch, a missing `gh` and a row wt
could not judge are shown in the output, never an exit status. So is a
registry wt could not read, or a `local_path` in it that is malformed or
relative (write every `local_path` as an absolute path; `~/` is fine): the
list still shows, the header says artifact protection is OFF, registered
model artifacts are not marked in use, and removal is off (enter in the list
says why; `wt remove` exits 1). No registry at the default path means
nothing is registered: the header says `registry: none at <path>`; a missing
registry anywhere else (`WT_REGISTRY`, a relocated `XDG_CONFIG_HOME`, no home
directory) counts as could not be read. A `local_path` that is an ancestor of
the repository (`~`, or `/`) blocks every worktree under it as
`protected_artifact`, and the row's detail names that path.

## Removing

Every target is re-checked immediately before it is touched (no new fetch).
If it got worse since you saw it (a less safe group, a moved tip, more to
lose: new uncommitted changes, more unpushed commits, more ignored files) it
is skipped with `skipped_changed` and nothing of it is touched. A worktree
goes with plain `git worktree remove` (`--force` only for an approved dirty
worktree); a branch with a compare-and-swap on the tip that was re-checked,
and only when no other worktree holds it. A worktree's branch is deleted with
it only when the branch is proven merged; otherwise it is kept
(`retained_unmerged`) unless you also approved deleting it.

Some targets are always refused, whatever you approve: a worktree whose
directory is gone ("could not read the worktree's state"; `git worktree
prune` clears its registration), a dirty worktree that holds initialised
submodules ("contains submodules; remove by hand": `git worktree remove
--force` would delete the submodules' repositories), and a branch beside one
whose name differs only in case (`Topic` and `topic`: on a case-insensitive
volume git deletes both, so it is kept as `retained_unknown`; delete it by
hand). A repository whose object format is not sha1 is not supported: both
commands stop before reading anything else (exit 1).

Every deleted branch's full commit id is reported (the `branch_tip` key in
JSON, "branch tip was ..." in text and on the results screen, kept after the
list closes). Restore one with `git branch <name> <sha>` while git still has
the commit (until a `git gc` prunes it); for a branch whose name starts with
`-`, which `git branch` refuses, the restore line is `git update-ref
refs/heads/<name> <sha>`.

### Approvals

Approvals are per item. Each one means exactly one thing:

| Approval | `wt remove` flag | In the list | Covers |
|---|---|---|---|
| go ahead | `--yes` (or `y` at the prompt) | `y` | a proven item; nothing else |
| accept the loss | `--accept-loss` | the typed word `delete` | any item that is not proven or loses something (ignored files such as `.venv`, commits only on this machine, a fresh branch, a PR state wt could not read) |
| force | `--force` | the typed word `delete` | also lose a worktree's uncommitted changes, or a detached HEAD's commits |
| delete unmerged branch | `-D` | `tab` on that entry, then `delete` | delete a branch not proven merged: a named branch, or (per entry) a removed worktree's branch |

`--force` never implies `-D`, and neither implies `--accept-loss`: a dirty
worktree needs `--force --accept-loss`, an unmerged branch `-D
--accept-loss`. An item whose approval is missing is refused (`refused`,
naming the missing flags) and the others go on. No approval overrides a hard
blocker: the primary checkout, the directory you are in, a locked or nested
worktree, a rebase or merge in progress, a registered model artifact, the
default branch, or a live session. A registry that did not load refuses
every removal.

`wt remove` without `--yes` asks on a terminal (default No), after listing
each item's losses, the processes running in it, and every unmerged branch
`-D` would delete by name. Without a terminal it removes nothing (exit 3).
`--dry-run` needs neither and applies the same approval checks, so
`--dry-run` with your flags shows exactly what the real run would do.

In the list, enter shows a confirmation with every ticked entry: its group,
what it would lose, running processes, and the approvals it needs. When every
entry is proven `y` confirms; otherwise only the typed word `delete` then
enter does, and any other key cancels. The prompt always states how many
items will be removed and how many need force or `-D`, and how many are not
on screen. `origin` rows cannot be ticked. While removal runs, keys,
ctrl+c and termination and hangup signals wait until it ends; the results screen shows
every outcome and the same outcomes print again when wt exits.

### `wt remove` arguments, output and exit codes

An argument is a worktree path (relative to the current directory) or a local
branch's exact name; a branch name also selects the worktree that has it
checked out. `origin/<branch>` never matches. An argument matching nothing, or
two different targets, stops the run before anything is touched.

`--json` prints one document,
[remove-result.schema.json](../../docs/contracts/remove-result.schema.json)
(samples beside it), on failure too, except for a usage error the argument
parser reports itself (an unknown flag, no arguments): that prints no
document and exits 1. If stdout cannot be written (a closed pipe) the result
goes to stderr as text, restore ids included, and the exit is 2. `--no-fetch`
and `--min-age` behave as for `wt cleanup`.

| Exit | Meaning |
|---|---|
| 0 | every target was removed (a removed worktree whose branch was kept because it is unmerged, checked out elsewhere or the default branch counts) |
| 1 | could not start: an argument matched no target or more than one, no default branch, a registry that did not load, a `--min-age` below `1m` |
| 2 | some item was refused, skipped or failed, or a branch was kept because it moved or its state could not be read |
| 3 | approval missing: no `--yes` and no terminal, or the prompt was declined; nothing was removed |

The tick list exits 0 after a removal run, whatever the outcomes: they were
on screen and are printed at exit, unless the repository could not be
re-read afterwards (exit 1; the outcomes still print).

Guide: [08 §9](../../docs/guides/08-maintenance-and-troubleshooting.md#9-cleaning-up-worktrees-and-branches).
Design: [the spec](../../docs/superpowers/specs/2026-11-04-wt-cleanup-design.md).
