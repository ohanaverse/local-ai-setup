# `wt worktrees`, `wt remove`, `wt rescue` and `wt restore-root`

`wt worktrees` (`wt cleanup` is an alias that does the same) lists the
repository's worktrees, local branches and remote branches. On a terminal the
list is grouped by kind (WORKTREES, LOCAL BRANCHES, REMOTE BRANCHES), each
row with a verdict (how sure wt is that it can go, with the evidence) and
tags; `--plain` and `--json` carry the same verdict per row, `--plain` grouped
by verdict instead (see [Groups](#groups)). On a terminal the list removes the
worktrees and local branches you tick, after a confirmation (its `origin`
rows read less sure, because the list has not fetched). `wt remove` does the same for
targets named on the command line. Both use one removal engine, which also
deletes a **proven `origin` branch** when you give it the approval no other
flag implies (`--remote`): see
[Deleting an origin branch](#deleting-an-origin-branch).

```bash
wt worktrees                # interactive tick list on a terminal; enter removes the ticked rows
wt worktrees --plain        # text list, also used when output is a pipe (removes nothing)
wt worktrees --json         # one JSON document (printed on failure too; removes nothing)
wt worktrees --no-fetch     # skip `git fetch --prune <remote>` for every remote
wt worktrees --min-age 30m  # raise the fresh-branch threshold (default 10m)
wt cleanup ...              # the same command under its old name

wt remove <worktree-path-or-branch>... --dry-run   # what would happen; changes nothing
wt remove ../repo-wt/topic                         # asks y/N on a terminal
wt remove topic old-spike --yes --accept-loss -D   # non-interactive, with explicit approvals
wt remove origin/merged-topic --remote --yes       # delete a proven branch on origin
```

## What is listed

Every worktree, including the primary checkout (the root) and the one you are
in, every local branch, and the remote branches, are rows. The default branch
is listed too: as its own row when no worktree holds it, and `origin/<default>`
folds into it like any other pair. A remote ref folds into a local row only
when it is that branch's own upstream, or `origin/<name>` when the branch has
no upstream: in the terminal list it is the row's `upstream` (`ref`, `gone`,
and its own status and evidence) and not a row of its own, so
`upstream/foo` beside a local `foo` that tracks `origin/foo` stays its own row.
`--plain` and `--json` still list a folded ref as its own row as well (the
JSON row also carries the `upstream` field), but they list only `origin`'s
branches and those of remotes a local branch tracks; the terminal list shows
every remote's. On a terminal the list does not fetch when it opens (`r` is not a
refresh; `--fetch` fetches every remote first and the list's `F` fetches while
it is open; `--json`/`--plain` fetch first unless
`--no-fetch`). Stale session claims are dropped on the list's `F` refresh and on
a fetch-first `--json` run only (`sessionclaim.Prune`; `--plain` fetches but
never prunes, `--no-fetch` drops nothing, and the list's open drops nothing).
Each row carries tags such as `root`, `you are here`, `held`,
`dirty`, `unpushed`, the PR, and `PR ?` while a PR lookup is unknown. When the
root sits off the default `origin/HEAD` names (or on a detached HEAD) its row carries a
`root_drift` reason and a drift badge (and, in the launch picker, the header
names the drift: `root is on <b>, not <d>`, or `root is detached`); the drift
label floors nothing on its own — it says where the root is, and `wt
restore-root` is the way back. No `origin/HEAD` default, no drift is invented. A bare
repository's own entry is listed as the root with a `bare` tag: it is never
removable, and in the launch picker enter on it only says that a bare
repository has no working tree to launch in.

A **hard stop** is a state no flag overrides (the primary checkout, the
directory you are in, a locked or nested worktree, a rebase or merge in
progress, a registered model artifact, or an open PR on an `origin` branch
or one that targets it); the row's `hard_stop` JSON field names it. A local
branch or worktree with an open PR is not a hard stop: it reads unique work and
needs `--accept-loss` and `-D`. A **floor** is a verdict ceiling the user can accept: a held
session claim (`held`), the default branch (`default_branch`) and an unknown
pull request (`pr_unknown`) leave the row `less_sure` at best, so `wt remove`
needs `--accept-loss` for it, and, for a branch that is not proven merged,
`-D` as well (the engine asks for `-D` for any unproven branch).

An unknown PR lookup (`gh` missing, signed out or offline, or the lookup
failed) floors a row below proven, with a `PR ?` tag, because a merged PR is
part of the proof. The exception is a repository whose `origin` is not on
GitHub: the lookup does not apply there (an origin wt does not recognise as
github.com, such as an ssh host alias, an `insteadOf` short form or GitHub
Enterprise, counts as having no pull requests), so it floors nothing and shows no
tag or `needs gh` wording on any row. An `origin` branch there is still never
proven by a PR: its row reads `not_checked` (no pull request is checked) and
stays less sure or worse.

## Groups

| Group | Meaning |
|---|---|
| Proven merged | A rung of the evidence ladder holds: same commit, ancestor of the base, trees match, no added changes, or `merge-tree` adds nothing; or a clean worktree whose own commits a `wt rescue` record keeps (evidence `rescued`, see [The record](#wt-rescue-keep-work-before-it-is-lost)). |
| Looks merged (less sure) | Only a weak signal (`git cherry`, patch-id), no proof, or a fact wt could not read; also a rescued original whose uncommitted changes still equal its record. |
| Has work that exists nowhere else | Commits not in the base and not on any remote-tracking ref (unless a rescue record keeps them and the worktree is clean), uncommitted or untracked changes (unless a rescue record holds them exactly: then less sure), or an open PR on a local branch or worktree. |
| In use / locked | A primary, current, locked, nested or mid-operation worktree, a registered model artifact (`protected_artifact`) or an `origin` branch with an open PR, or one an open PR targets: hard stops no flag overrides (the row's `hard_stop`). A held session claim (`held`), the default branch (`default_branch`) and an unknown PR (`pr_unknown`) are floors, not hard stops: the row reads less sure and `wt remove` needs `--accept-loss` for it. |

`origin` branches are rows in these same groups, judged the same way (a proven
one is the only kind that can be removed).

Weak signals are never proofs. Any fact wt cannot establish makes the row
less sure, never proven.

Apart from the fetch, which updates and prunes every remote's remote-tracking
refs (`--no-fetch` skips it), listing writes no ref, index or working file.
It reads the `wt rescue` records under `refs/wt/rescued/` (one `for-each-ref`).
The per-remote fetch (`--json`, `--plain` and a pipe) takes `origin` first
and on its own (it alone drives classification, 120s), then every other
remote concurrently with a shorter limit (30s); the header and
`fetch.remotes` in the JSON report each remote's result, also when the
repository has no `origin` (`fetch.skipped`). It never prompts: git's
terminal and credential-manager prompts are off, and ssh runs with
`-o BatchMode=yes` unless you set `GIT_SSH_COMMAND` or `core.sshCommand`
yourself, in which case your ssh command is used as it is and any question it
asks is yours. The terminal list's explicit fetches (`--fetch`, the list's
`F`) are one `git fetch --all --prune`, capped at 5s, at the repository's
root: one result for every remote at once, not one per remote.
The merge-adds-nothing proof
(`git merge-tree --write-tree`) may leave unreachable tree objects in the
object store, which `git gc` collects.

## `wt worktrees` flags

| Flag | Effect |
|---|---|
| `--json` | Machine-readable report: [cleanup-list.schema.json](../../docs/contracts/cleanup-list.schema.json), sample [cleanup-list.sample.json](../../docs/contracts/cleanup-list.sample.json). Its `notes` array holds the caveats the text header prints (a lowered `--min-age`, artifact protection off, no registry); read it before acting on `rows`. |
| `--no-fetch` | Skip the fetch (every remote, never prompting). A repository with no remote skips it anyway; the header says how it went, remote by remote. With the fetch skipped or failed no `origin` row can be proven. |
| `--min-age <duration>` | A branch younger than this that sits at or behind the base is never proven merged (a brand-new branch looks merged). At least `1m` (less is a usage error, exit 1); a value below the default `10m` is named in the header. |
| `--plain` | Text output on a terminal. |

## `wt worktrees` exit codes

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

Approvals are per item. Each one means exactly one thing (`--dry-run` lists the losses and changes nothing, and needs none):

| Approval | `wt remove` flag | In the list | Covers |
|---|---|---|---|
| go ahead | `--yes` (or `y` at the prompt) | `y` | a proven item; nothing else |
| accept the loss | `--accept-loss` | the typed word `delete` | any item that is not proven or loses something (ignored files such as `.venv`, commits only on this machine, a fresh branch, a PR state wt could not read) |
| force | `--force` | the typed word `delete` | also lose a worktree's uncommitted changes, or a detached HEAD's commits |
| delete unmerged branch | `-D` | `tab` on that entry, then `delete` | delete a branch not proven merged: a named branch, or (per entry) a removed worktree's branch |
| delete on origin | `--remote` | the typed phrase `delete origin` (reachable from the list once a fetch proved the row — tick it, type `delete origin` at the confirmation) | a proven `origin/<name>` row; no other approval grants it |

`--force` never implies `-D`, and neither implies `--accept-loss`: a dirty
worktree needs `--force --accept-loss`, an unmerged branch `-D
--accept-loss`. An item whose approval is missing is refused (`refused`,
naming the missing flags) and the others go on. No approval overrides a hard
blocker: the primary checkout, the directory you are in, a locked or nested
worktree, a rebase or merge in progress, a registered model artifact, or an
open PR on an `origin` branch (or one that targets it). A held session and the default branch are floors, not blockers:
`--accept-loss` approves them (and `-D` a branch that is not proven merged;
`origin/<default>` is still never deleted). A registry that did not load refuses
every removal.

`wt remove` without `--yes` asks on a terminal (default No), after listing
each item's losses, the processes running in it, and every unmerged branch
`-D` would delete by name. Without a terminal it removes nothing (exit 3).
`--dry-run` needs neither and applies the same approval checks, so
`--dry-run` with your flags shows exactly what the real run would do.

A clean original that `wt rescue` has copied (its commits are the only thing
it holds that exists nowhere else, and a record keeps them; see
[the record](#wt-rescue-keep-work-before-it-is-lost)) reads proven, so `--yes`
alone removes it and its own branch. One with uncommitted changes needs
`--accept-loss` alone while its index and working tree still equal the record
(it reads less sure); once they differ it needs `--accept-loss`, `--force`
and `-D` again.

In the list, enter shows a confirmation with every ticked entry: its group,
what it would lose, running processes, and the approvals it needs. When every
entry is proven `y` confirms; otherwise only the typed word `delete` then
enter does, and any other key cancels. The prompt always states how many
items will be removed and how many need force or `-D`, and how many are not
on screen. The list does not fetch when it opens, so without a fetch this run
(`--fetch`, or the list's `F`) an `origin` row is never proven. These
fetch reasons appear only where pull requests are checked (a
GitHub-recognised origin); a non-GitHub origin's rows read `not_checked`
(above) and never name the fetch state. With no fetch
flags its reason says `origin was not refreshed in this list` (it names no
flag, since you passed none) and the header says
`fetch: not run (this list does not fetch when it opens)`. With `--no-fetch`
the reason names the flag instead —
`origin was not fetched this run (--no-fetch)` — and the header says
`fetch: skipped`. Either way the row cannot be ticked — only a
proven `origin` row can be, and one you ticked is removed only through the
confirmation's `ORIGIN BRANCHES` group, which names how many
branches are on `origin`, refuses `y` and the plain `delete`, and needs
`delete origin` typed. `wt remove origin/<name> --remote` removes one by name
instead, fetching first. While removal runs, keys,
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
and `--min-age` behave as for `wt worktrees`.

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

**The original stays as it was.** It is never changed or removed by the rescue. It is read with
optional locks off (no index refresh) and compared before and after: its HEAD,
its own branch ref, its staged content (not the index file's bytes, which a
`git status` elsewhere may refresh) and the files the status names. Other
refs, other worktrees' commits and a background fetch do not fail the run. Its commits
are counted against remote-tracking refs only and its files were copied, not
moved. What happens to it afterwards depends on the record (next paragraph):
while it is clean (no staged, unstaged or untracked changes) and the record
keeps its commits, `wt cleanup` lists it as proven and `wt remove <path> --yes`
removes it and its own branch. An original with uncommitted changes is
covered only while it is exactly what the record captured: its index and
working tree (staged, unstaged and untracked files that are not ignored) must
equal the record's, and its HEAD and branch tip must still be the commit the
record names. Then `wt cleanup` lists it as less sure (reason `preserved`,
evidence unchanged) and `wt remove <path> --accept-loss` removes it; it never
needs `--force`, and it is not ticked by default in the list. Any change that
leaves it different (a change of file modes other than the executable bit is
not seen, see below),
a file marked skip-worktree or assume-unchanged, or a record that no longer
matches returns it to having work
that exists nowhere else, needing `--accept-loss`, `--force` and `-D`.
`wt remove` re-reads the record immediately before `git worktree remove` and
skips the item (`skipped_changed`) if the original changed since the
preview; an error in that re-read refuses it. The preview notes where the
changes are kept: `uncommitted changes are kept in <ref> (copy: <path>)`.
What the removal loses even then: ignored files, file modes beyond the
executable bit, empty directories, and anything written to the original in the
short window between the re-check and `git worktree remove`. Cost: a covered
row hashes its changed files on every list and re-check, and once more right
before removal (a worktree with over 20000 changed files (tracked changes and untracked)
or 512 MiB in them is not covered). Remove it after you have checked the copy.

**The record.** After the copy is verified, and only then, wt re-reads the
source and, if it did not change, leaves a record in the source's repository:
the ref `refs/wt/rescued/<id>`, printed as a `record:` line (JSON
`record {ref, commit}`). It points at two commits: the source's index state
(parent: the source's HEAD) and its working state on top of that. So it holds
the commits and the working state: staged, unstaged and untracked files that
are not ignored. It does not hold ignored files, file modes beyond the
executable bit, or empty directories. The commits carry the source path,
branch and HEAD in `Wt-*` trailers. Inspect it:

```bash
git for-each-ref refs/wt/rescued
git log --stat <ref>
```

If the record cannot be written (the source changed after the verification,
a SHA-256 repository, or a git step failed), the rescue stays `moved` and the
report carries a warning starting `record_not_written:`; the copy is unaffected. `wt cleanup` and
`wt remove` read the records (`for-each-ref refs/wt/rescued/`, afresh on every
classification, including the re-check before a removal). A record counts for a
branch or detached worktree only when everything below holds; otherwise it is
ignored and the row reads as it would without it:

- the ref is `refs/wt/rescued/` plus the first 16 hex digits of its commit, and
  its trailers are well formed (`Wt-Version` 1);
- its source path, ref (`detached` for a detached HEAD) and head equal the
  worktree's, and for a branch the branch tip is still that head;
- its commit and the commit's parent exist, and the worktree's HEAD reaches
  nothing that neither the record nor a remote-tracking ref reaches;
- the worktree has no uncommitted, staged or untracked change, nothing wt
  could not read, and no hard blocker;
- the row has no merge proof and its own commits are what exists nowhere else.

Then the row's evidence is `rescued` and it carries the informational reason
`preserved` (`preserved by <record ref> (copy: <path>)`; it never lowers the
status). Everything else keeps its usual effect: an open or unknown PR, a
branch judged against a local default, and ignored files (the row stays less
sure and needs `--accept-loss`). `rescued` rows are not age-gated. The source ref must
match the record exactly: on a case-insensitive volume, a worktree whose HEAD
spells the branch in a different case from the ref's spelling does not read
removable (it fails closed; `--accept-loss` still works). Only the source of a
rescue is ever preserved, never the copy. A record pins its objects until you
remove it, and wt does not prune records yet (#543). To drop one after you have
checked the copy:

```bash
git update-ref -d refs/wt/rescued/<id>
```

Refused, with nothing created (JSON `code` in brackets): nothing to keep
(`nothing_to_keep`); the primary checkout (`primary`; [`wt restore-root`](#wt-restore-root-move-the-roots-branch-into-a-worktree) moves its branch and work into a worktree); a rebase, merge
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
files together (`too_large`; a changed tracked file counts as the larger of its
working file and its index or HEAD blob, so a staged deletion of a big file
counts too, whatever its name holds; a staged rename is one file, counted once
at the larger of the old path's HEAD blob and the new path's blob, while a
deletion plus a different big addition counts both); a source that changed while it was being checked
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
`wt remove` it if you do not want it. When copying the untracked files is what
failed, the directories made so far already carry the original's modes; if one
of them could not be set (its source is gone), the message ends with
"directory modes could not be restored".

One narrow window is not closed: a branch that someone else creates under the
new name in the instant between wt's last look at the name and `git worktree
add` is indistinguishable from a leftover of the failed add, so a failure of
that add may then report it as kept. It is untestable without a race and not
guarded against.

The JSON `code` says which step failed: `create_failed` (git could not create
the worktree; `created` says whether anything was left), `carry_failed` (the
worktree exists but staged changes, unstaged changes, file modes or an
untracked file could not be copied into it) and `verify_failed` (the copy was
made but the comparison with the source found a difference).

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

## `wt restore-root`: move the root's branch into a worktree

Work started directly in the repository root leaves it on a branch other than
the default, which blocks sessions and tools that expect the default branch
there. `wt restore-root` moves that same branch (its ref, commits, upstream and
config stay; nothing is copied or renamed) into a new worktree under
`.worktrees/` and puts the root back on the default branch. It runs from
anywhere in the repository and builds on `wt rescue`'s engine.

```bash
wt restore-root --dry-run          # what would move and what stays; changes nothing, fetches nothing
wt restore-root                    # asks Move? [y/N] on a terminal
wt restore-root --to spike --yes   # no prompt; --to names only the directory under .worktrees/
```

| Flag | Meaning |
|---|---|
| `--to DIR-NAME` | the new worktree's directory under `.worktrees/` (default: the branch name flattened); names only the directory; the branch is never renamed |
| `--dry-run` | say what would happen; writes no ref, index or working file and does not fetch |
| `--yes` | move without asking |
| `--json` | one document, [restore-root-result.schema.json](../../docs/contracts/restore-root-result.schema.json) (samples beside it), printed on refusal and failure too |
| `--no-fetch` | do not fetch before the fast-forward check |

A real run first fetches every remote (it updates remote-tracking refs,
nothing else), exactly as `wt cleanup` does (one `git fetch --prune` per
remote). Uncommitted work is carried by the rescue engine:
a new worktree is added detached at the branch's tip, staged, unstaged and
untracked non-ignored files are copied into it and verified against the root
(`git status` equal, every path equal in kind, mode, content and link target),
and the record `refs/wt/rescued/<id>` is written. The record is required: with
no record the root is not released. Only then is the root reset, the carried
untracked files removed (exactly those paths, never `git clean`) and the root
switched to the default branch; the new worktree is switched onto the branch,
and the default branch is fast-forwarded to `origin/<default>` when that is a
strict descendant (never a merge; skipped with a warning, still `moved`, when
the update would overwrite an ignored file, or remove one under a path the
update replaces by a file). Git hooks do not run in any of
these steps; a `post-checkout` hook gets the same warning as in `wt rescue`.
`moved` is reported only after both checkouts are read back. A root with
nothing uncommitted skips the copy and the record; it needs an unchanged
fingerprint and a clean status to be released. Copying untracked files is the
slow part: measured at about 17 ms per file (2000 untracked files took about
34 s), with no progress output while it runs; this is an observed cost, not a
promise.

Refused, with nothing created (JSON `code` in brackets): a detached HEAD
(`detached_root`; run `git switch -c <name>` first); a root already on the
default branch (`already_default`; exit 0, nothing to do); no commit yet
(`no_commit`); no default branch, or one that exists only as `origin/<name>`
(`no_default_branch`); the default branch checked out in another worktree
(`default_in_worktree`); a root that does not ignore
`.worktrees/` (`worktrees_not_ignored`; the message gives the one-line fix, and
wt writes nothing to the repository); an ignored file the reset would destroy,
or one at a path the default branch tracks (`ignored_collision`; ignored files
are never overwritten); any assume-unchanged or skip-worktree entry
(`hidden_index_flags`, which includes a sparse checkout); and every state
`wt rescue` refuses (see above), except that a root always counts as having
something to keep. The JSON `code` of a failure after the new worktree exists
is `create_failed`, `carry_failed`, `verify_failed`, `record_not_written` (the
copy could not be recorded, so the root was not released), `release_refused`
(a proof was missing at the release; the root was not changed),
`release_failed` (the reset or switch stopped part-way), `switch_failed` (the
new worktree could not be switched onto the branch) or
`source_changed_while_copying` (the root changed during the copy).

A root held by another live `wt` session refuses nothing and is never a
`code`: the plan carries one `root_held: ` warning line naming the holder
(`HeldWarningPrefix`, JSON `warnings`), and the move goes on once confirmed —
a y/N on a terminal, `--yes` without one, exit 3 without either, like any run.
Nothing checks the session claims again after that read.

| Exit | Meaning |
|---|---|
| 0 | moved (with `--dry-run`: would move), or `already_default` |
| 1 | could not start: not in a repository, a bad flag, the repository could not be read |
| 2 | refused or failed |
| 3 | approval missing: no `--yes` and no terminal, or the prompt was declined; nothing was changed |

**Recovery.** A failure stops at its step and nothing is rolled back; the
message ends with where the root, the new worktree and the work are:

| Failed at | Root | New worktree | Work |
|---|---|---|---|
| creating the worktree, the copy or the record | untouched (a record that could not be written means the root was NOT released) | absent, half-built or the verified copy; kept, path named | all in the root (and, after the copy, in the kept copy too) |
| the release, before the reset (a live session appeared, or a proof failed) | untouched | the verified copy | in the root and the copy |
| the release, after the reset began (`release_failed`) | reset or partly changed; run `git status` there | the verified copy, detached at the branch tip | in the copy and the record; finish with `git switch <default>` in the root, then `git switch <branch>` in the new worktree (look at the root's `git status` first) |
| switching the new worktree onto the branch (`switch_failed`) | on the default branch, clean | detached with all the work | in the copy and the record; `git switch <branch>` there finishes it, unless the branch moved during the run: then the message says not to switch blindly (that would lay the work over the moved branch), the new worktree stays detached at the old tip, and you decide where the work belongs first |
| the fast-forward | on the default branch | on the branch | all moved; only the fast-forward is missing (a warning) |
| the final read-back (`verify_failed`) | as read back | as read back | nothing is undone; in the copy and the record |

Once the confirmation is answered (or with `--yes`), SIGINT, SIGTERM and
SIGHUP to wt do not stop the move: wt finishes, prints the result and then
`interrupted (signal ...): the move was not stopped; see the result above`. A
Ctrl-C on the terminal also reaches the git command running at that moment;
the step it kills fails and its row above is printed. A move killed from
outside (SIGKILL, power loss) is recovered by the same table: the root is
untouched until the release, and a half-built `.worktrees/<name>` is cleared
with `wt remove <path> --force --accept-loss` (add `--yes` without a terminal).

Running `wt restore-root` again after a failure is refused while the kept
directory exists; check the work, then clear it with `wt remove`. For a kept
copy holding uncommitted work, `wt remove <path> --yes --dry-run` says
`refused: needs --force, --accept-loss` (verified on a half-built copy), so
the command is `wt remove <path> --force --accept-loss`.

**Declared losses.** Ignored files and empty directories stay in the root
(ignored files are never overwritten or removed; the count is reported). File
modes beyond the executable bit are not kept. A change made to the root in the
instant between the last claim and record checks and the reset is lost, the
same window as `wt remove` of a dirty rescued original (a carried untracked
file edited then is kept). After the move the record names the root as its
source, so `wt cleanup` does not list the new worktree as preserved by it.
`wt rescue` still truncates its messages at 200 characters; `wt restore-root`
prints reasons, warnings and steps in full.

When the root is on a non-default branch, the launch warnings' hint is one
line pointing here, printed by `cmd/wt`'s `confirmLaunch` and the picker's
launch gate (hidden when `origin/HEAD` is not set).

Guide: [08 §9](../../docs/guides/08-maintenance-and-troubleshooting.md#9-cleaning-up-worktrees-and-branches).
Design: [the spec](../../docs/superpowers/specs/2026-11-08-wt-restore-root-design.md).

## Deleting an origin branch

`wt remove origin/<name> --remote` deletes the branch on `origin` (the
terminal list does not fetch when it opens, so without `--fetch`/`F` its
origin rows are never proven and cannot
be ticked). It is visible
to everyone with access to the repository and this tool cannot undo it, so
the rules are narrower than for a local branch:

- Only a **proven** row on **`origin`**: a MERGED pull request into the default
  branch whose head is exactly the branch's tip, whose merge commit is in the
  base; no open PR with the branch as head; no open PR (from this repository or
  a fork) with it as base; content in the base; and `origin` fetched cleanly
  **this run**. `--no-fetch`, a terminal list that fetched nothing (it does
  not fetch when it opens; `--fetch` and the list's `F` fetch), a failed fetch,
  a missing `gh` or a failed query each make the row
  unproven. There is no `--accept-loss` or `-D` path.
- The approval is its own: `--remote` on the CLI (`delete origin` in the list's
  confirmation, once the list can prove an origin row).
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
  locked. The PR lookups (by head branch, and the open PRs found by base)
  describe the repository `origin` points at, so a row of another remote reads
  none of them: a PR found for `origin/foo` never blocks, adds a reason to or
  names a dependant of `upstream/foo`. Nothing asks `gh` about another remote,
  so that row's pull-request state is unknown and it is at most less sure
  (reason `not_checked`, "the pull-request state of another remote's branch
  is not checked"; it is not a gh failure, so the header's gh line never names
  it), never proven by git's proofs alone.
