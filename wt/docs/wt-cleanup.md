# `wt cleanup`, `wt remove` and `wt rescue`

`wt cleanup` lists the repository's worktrees, local branches and `origin`
branches in four groups, by how sure wt is that each can go, with the
evidence for every row. On a terminal the list removes the worktrees and
local branches you tick, after a confirmation. `wt remove` does the same for
targets named on the command line. Both use one removal engine, which also
deletes a **proven `origin` branch** when you give it the approval no other
flag implies (`--remote`, or the typed phrase `delete origin`): see
[Deleting an origin branch](#deleting-an-origin-branch).

```bash
wt cleanup                  # interactive tick list on a terminal; enter removes the ticked rows
wt cleanup --plain          # text list, also used when output is a pipe (removes nothing)
wt cleanup --json           # one JSON document (printed on failure too; removes nothing)
wt cleanup --no-fetch       # skip `git fetch --prune <remote>` for every remote
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

Apart from the fetch, which updates and prunes every remote's remote-tracking
refs (`--no-fetch` skips it), listing writes no ref, index or working file.
The fetch takes `origin` first and on its own (it alone drives classification,
120s), then every other remote concurrently with a shorter limit (30s); the
header and `fetch.remotes` in the JSON report each remote's result, also when
the repository has no `origin` (`fetch.skipped`). It never prompts: git's
terminal and credential-manager prompts are off, and ssh runs with
`-o BatchMode=yes` unless you set `GIT_SSH_COMMAND` or `core.sshCommand`
yourself, in which case your ssh command is used as it is and any question it
asks is yours.
The merge-adds-nothing proof
(`git merge-tree --write-tree`) may leave unreachable tree objects in the
object store, which `git gc` collects.

## `wt cleanup` flags

| Flag | Effect |
|---|---|
| `--json` | Machine-readable report: [cleanup-list.schema.json](../../docs/contracts/cleanup-list.schema.json), sample [cleanup-list.sample.json](../../docs/contracts/cleanup-list.sample.json). Its `notes` array holds the caveats the text header prints (a lowered `--min-age`, artifact protection off, no registry); read it before acting on `rows`. |
| `--no-fetch` | Skip the fetch (every remote, never prompting). A repository with no remote skips it anyway; the header says how it went, remote by remote. With the fetch skipped or failed no `origin` row can be proven. |
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
prune` clears its registration), a dirty worktree that holds submodules
("contains submodules; remove by hand": `git worktree remove --force` would
delete the submodules' repositories; this covers an initialised module, an
embedded repository added as one, and a leftover `modules/` directory in the
worktree's git dir, and a check that cannot be read refuses too), and a branch beside one
whose name differs only in case (`Topic` and `topic`: on a case-insensitive
volume git deletes both, so it is kept as `retained_unknown`; delete it by
hand). A repository whose object format is not sha1 is not supported: both
commands stop before reading anything else (exit 1).

Every deleted branch's full commit id is reported (the `branch_tip` key in
JSON, "branch tip was ..." in text and on the results screen, kept after the
list closes). Restore one with `git branch <name> <sha>` while git still has
the commit (until a `git gc` prunes it); for a branch whose name starts with
`-`, which `git branch` refuses, the restore line is `git update-ref
refs/heads/<name> <sha> ""` (the empty old value makes it create-only, like
`git branch`: it fails rather than move a branch re-created since). The
"survives only until git gc prunes it" warning appears (on every path: deleted,
re-created, not restored) only when no other local branch or remote-tracking
branch (`main`, `origin/<other>`, another branch) reaches the deleted tip;
otherwise a gc loses nothing and the warning is left out. Tags and stash
entries do not count, and neither does `origin/<the deleted branch's own
name>`, which goes in the same cleanup or is what you are removing. If git
cannot answer, the warning stays.

A deleted branch's upstream settings (`branch.<name>.remote` and `.merge`) go
with it, as with `git branch -D`, so a branch restored under the same name does
not inherit a stale upstream; a restore does not bring them back. If they
cannot be removed the outcome carries a warning.

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
on screen. A proven `origin` row can be ticked, but is never ticked for you
(`a` skips it too); any other remote row cannot. A confirmation that includes
an `origin` branch has its own group (`ORIGIN BRANCHES`), says how many
branches are on `origin` and that they are visible to everyone with access to
the repository, refuses `y` and the plain `delete`, and needs `delete origin`
typed. While removal runs, keys,
ctrl+c and termination and hangup signals wait until it ends; the results screen shows
every outcome and the same outcomes print again when wt exits.

### `wt remove` arguments, output and exit codes

An argument is a worktree path (relative to the current directory) or a local
branch's exact name; a branch name also selects the worktree that has it
checked out; `origin/<branch>` names that branch on origin (a local branch
literally called `origin/<branch>` makes it ambiguous). An argument matching nothing, or
two different targets, stops the run before anything is touched.

`--json` prints one document,
[remove-result.schema.json](../../docs/contracts/remove-result.schema.json)
(samples beside it), on failure too, a usage error (no arguments, an unknown
flag) included: it prints the error document and exits 1. A bad flag is seen
as a `--json` request only when `--json` comes before it. If stdout cannot be written (a closed pipe) the result
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

## `wt rescue`: keep work before it is lost

A worktree in the "has work that exists nowhere else" group holds commits and
files that exist only there. `wt rescue` copies them into a new branch and
worktree so they survive; it only creates, and never changes or removes the
original.

```bash
wt rescue ../repo-wt/spike --dry-run   # what would be copied; changes nothing
wt rescue spike                        # asks y/N on a terminal
wt rescue spike --yes --to keep/spike  # no prompt; choose the branch name
```

In `wt cleanup`'s list, `r` on a worktree row with work to keep does the same
after the list closes.

The new branch (default `rescued/<branch>`, or `rescued/<directory>` for a
detached worktree) starts at the original's exact HEAD, so every commit
comes with it, and its worktree goes under `.worktrees/`. Carried on top:
staged changes (still staged), unstaged changes and untracked files that are
not ignored. Not carried: ignored files (`.env`, `.venv`; the count is
reported) and empty directories. Before it reports `moved`, wt checks that the
new worktree's `git status` equals the original's and that every changed file
has the same content, mode and link target (a changed tracked file gets the
original's exact permission bits, since `git apply` would write `0666` minus
the umask). The new worktree is added with git hooks disabled
(`core.hooksPath=/dev/null`): when the repository has a `post-checkout` hook, the
plan carries a warning that it did not run there, so run whatever it sets up
yourself. A directory the copy creates for
untracked files gets the original directory's mode (a `0700` one stays
`0700`), and the check compares those modes too.

**The original stays as it was, and is not made removable.** It is read with
optional locks off (no index refresh) and compared before and after: its HEAD,
its own branch ref, its staged content (not the index file's bytes, which a
`git status` elsewhere may refresh) and the files the status names. Other
refs, other worktrees' commits and a background fetch do not fail the run. Its commits
are counted against remote-tracking refs only and its files were copied, not
moved, so `wt cleanup` still lists it as having work that exists nowhere else
and `wt remove` still needs `--accept-loss`, `--force` and `-D`. Remove it
after you have checked the copy.

Refused, with nothing created (JSON `code` in brackets): nothing to keep
(`nothing_to_keep`); the primary checkout (`primary`; #522); a rebase, merge
or similar in progress (`operation_in_progress`) or unmerged paths
(`unmerged_paths`); a skip-worktree or assume-unchanged file
(`hidden_index_flags`); a changed submodule (`submodule_change`); a file both
staged and modified (`staged_and_modified`); an intent-to-add file
(`intent_to_add`); a nested git repository (`nested_repo`); an untracked path that is a fifo, socket or other
non-file, or whose path is unsafe to copy (`special_file`); a changed file whose
line endings git converts (a `text`/`eol` attribute, `core.autocrlf` or `core.eol`)
and that holds CRLF or would be written with CRLF there, such as an LF file
under `core.eol=crlf` with a `text` attribute (`line_endings`, naming only the
affected paths); a changed file a `filter`, `ident` or `working-tree-encoding`
attribute handles, git-lfs or any clean/smudge filter, which a copy could not
reproduce byte for byte (`content_filter`); more than 20000
untracked files, or more than 512 MiB of untracked files and changed tracked
files together (`too_large`); a source that changed while it was being checked
(`source_changed`); a commit count that cannot be read, or reads 0 while the classifier counted unpushed
commits (exit 1, never a plan claiming 0 commits; a detached HEAD that a remote-tracking ref
already holds legitimately counts 0); an unreadable worktree
(`gates_unknown`, `unreadable`); a name already taken (`name_unavailable`,
naming `--to`; there is no automatic suffix; it can also be answered while the
y/N prompt waited, when someone took the branch or directory in the meantime:
then it is a `refused` outcome, nothing was created and nothing is kept); a branch with no worktree
(`not_worktree`; `wt -W <branch>` gives it one). Current, locked and held
worktrees are allowed (a held one is warned about: if its session edits files
during the copy the run fails with `source_changed_while_copying` rather than
report a stale copy). With `--yes` the source is not re-read between the check
and the copy (nothing was shown in between); a change in that instant is
caught by the comparison after the copy and fails the run, keeping the new
worktree. After a prompt the source is re-read before anything is created.

**A failure after the new worktree exists keeps it** (`created: true`, its
path in the message; when creating the worktree failed after git made the
branch, `branch_kept` is true and the message names the branch, which a rerun
would otherwise report as `name_unavailable`): nothing is rolled back, because undoing it would mean a
forced removal of a directory that now holds your work. Look at it, then
`wt remove` it if you do not want it.

| Exit | Meaning |
|---|---|
| 0 | the work was copied and verified (with `--dry-run`: it would be) |
| 1 | could not start: no or ambiguous target, `--min-age` below `1m`, the repository could not be classified |
| 2 | refused or failed |
| 3 | approval missing: no `--yes` and no terminal, or the prompt was declined; nothing was created |

`--json` prints one document,
[rescue-result.schema.json](../../docs/contracts/rescue-result.schema.json)
(samples beside it), on failure too. `--no-fetch` and `--min-age` behave as
for `wt cleanup`. `--dry-run` writes no ref, index or working file (like
listing, the merge proof may leave unreachable tree objects): it does not
fetch, so what counts as unpushed or merged is as of the last fetch, and the
report says so in `warnings`.

Guide: [08 §9](../../docs/guides/08-maintenance-and-troubleshooting.md#9-cleaning-up-worktrees-and-branches).
Design: [the spec](../../docs/superpowers/specs/2026-11-04-wt-cleanup-design.md).

## Deleting an origin branch

`wt remove origin/<name> --remote` (or an origin row ticked in the list and
confirmed with `delete origin`) deletes the branch on `origin`. It is visible
to everyone with access to the repository and this tool cannot undo it, so
the rules are narrower than for a local branch:

- Only a **proven** row on **`origin`**: a MERGED pull request into the default
  branch whose head is exactly the branch's tip, whose merge commit is in the
  base; no open PR with the branch as head; no open PR (from this repository or
  a fork) with it as base; content in the base; and `origin` fetched cleanly
  **this run**. `--no-fetch`, a failed fetch, a missing `gh` or a failed
  query each make the row unproven. There is no `--accept-loss` or `-D` path.
- The approval is its own: `--remote` on the CLI, `delete origin` in the list.
  `--yes`, `y`, `--accept-loss`, `-D`, `--force` and the plain `delete` never
  grant it, and `--remote` grants nothing else.
- Right before the push the engine re-asks `gh` about this one branch (so a PR
  opened since the list is seen), re-reads origin with `git ls-remote`, and
  compares four things that must be the same commit: the listed tip, the
  tracking ref, origin's live ref and the merged PR's head. Then it runs
  `git push --porcelain --no-verify --force-with-lease=refs/heads/<n>:<sha> origin :refs/heads/<n>`;
  the server compares `<sha>`, so a branch pushed to since is left alone
  (`retained_moved`). Git runs with prompts disabled (see the fetch note above for ssh) and a 120s limit. Just before pushing, the gate also asks git itself that the merged PR's merge commit is an ancestor of the base; it does not rely on the list's answer.
- Success is judged by reading origin again, never by the push's exit status:
  `deleted` means a fresh read found the ref gone; `retained_unknown` means
  origin refused it (branch protection, a server hook, `receive.denyDeletes`)
  or could not be read; `retained_moved` also covers a branch that was already
  gone because someone else deleted it while wt's own push failed (nothing is
  recorded as deleted by wt and no restore line is printed). The JSON `branch_tip` and the printed restore line
  (`git push origin <sha>:refs/heads/<n>`) are present whenever the ref may be
  gone; GitHub's "Restore branch" button on the merged PR also works. Your
  clone keeps the commit only until `git gc`.
- A same-named local branch and its worktree are not touched; a warning names
  a local branch that tracked it.
- One race stays open: a PR opened against the branch between the engine's
  last `gh` read and the push is closed by GitHub. Restore the branch and
  reopen the PR.
- Only `origin` rows are removable; rows of other remotes stay listed and
  locked.
