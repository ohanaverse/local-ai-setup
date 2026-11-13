# `wt cleanup` — Design

> **Superseded by `docs/superpowers/specs/2026-11-11-wt-user-driven-cleanup-design.md`.** This design note keeps its as-of-date wording; where the two disagree, that design describes the release.

**Status:** built (status note 2026-11-07): PR A (#518 and follow-ups), B #520, C #528, D #529. Originally a draft for approval (2026-11-04). Supersedes PRs 2–3 of
`2026-11-04-worktrunk-ports-design.md`; PRs 4–6 of that series are unchanged.

**Superseded in part:** the list grouping, the picker's row filters (root, default branch, held and in-use rows hidden) and the `in_use` treatment of a held worktree and the default branch (now floors needing `--accept-loss`, not hard blockers) are replaced by `2026-11-11-wt-user-driven-cleanup-design.md` (Part 1).

## Intent

After a few PRs merge, the user wants to clear out the worktrees, local
branches and `origin` branches that are no longer needed — and to be able to
*see why* each one is or is not safe to remove before anything is deleted.
`wt` becomes the main cleanup tool: it determines state in code and puts the
evidence in front of the user, who ticks what to remove. Today that job is the
`repo-cleanup` skill (bash, ~26 state checks); we measured that all but about
six of those checks need only git, and every mutation is a git command. So the
state logic moves into `wt`; only the *decisions* stay with the user.

Why now: PRs 2–3 of the worktrunk ports would have built a single-item
`wt remove` and a batch `wt prune` with their own proof rules. Building the
cleanup list first gives one set of proof rules, shared by every path that
deletes.

## Goals

- `wt cleanup` opens a list you tick through (the wt picker's look and keys),
  grouped by how sure `wt` is, with plain-words evidence on each row and a
  detail line for the highlighted row.
- Local worktrees, local branches, and `origin` branches whose PR merged are
  all in the one list.
- Every verdict carries its proof, so the user can judge a tick. The user
  decides; `wt` never removes on its own.
- Removal re-checks state at delete time and deletes a branch only if it still
  points at the commit that was classified.
- Work that exists nowhere else can be moved to a new worktree, then the
  original removed.
- `--json` for the list, with a published schema and contract fixture
  (the repo's cross-language rule).

## Non-goals

- Merge tooling, PR creation, hooks/aliases (worktrunk's surface stays out).
- Stashing, resetting or otherwise rewriting the default branch or the repo
  root (repo-cleanup's "diverged main" and "root on another branch" menus).
  `wt` refuses and says what to do; it does not run that menu.
- Any `--force` that overrides a hard blocker (below).
- Automatic or scheduled cleanup. Nothing is removed without a tick.

## Anti-goals (a failure even if it "works")

- **A false "safe".** Marking work that exists nowhere else as proven merged,
  or removing it. This is the dangerous direction; every design choice below
  errs the other way.
- Silent degradation: `gh` missing, a git error, an unknown default branch or
  a truncated PR list must show as such and never read as "no PRs / not
  merged / fine".
- Two proof rules (one in `cleanup`, one in `remove`).

## The list

> Superseded in part by `2026-11-11-wt-user-driven-cleanup-design.md`: the list is no longer grouped by verdict on a terminal: it is grouped by kind (WORKTREES, LOCAL BRANCHES, REMOTE BRANCHES) with the verdict on each row, and every row is shown (the root, the default branch, held and in-use rows are never hidden; an in-use row is locked, not dropped). The four verdict groups survive in `--plain`. The keys are also no longer just space, `a`, enter and `q`: `x`, `d`, `o`, `r`, `R` and `F` act on the highlighted row (see `wt/docs/wt-cleanup.md`, "The list's keys and the session loop"). The mockup above is the original as-of-date layout.

Illustrative layout (chosen from mockups; rows below are made up):

```
wt cleanup   space tick · a all proven · enter remove ticked · q quit

PROVEN MERGED (ticked by default)
 [x] worktree  full-branch-name-paths   squash merged, PR #517      clean
 [x] branch    fix-default-guard        squash merged, PR #516      no worktree
 [x] branch    eval-four-way            ancestor of main            2d ago
LOOKS MERGED, LESS SURE
 [ ] branch    spike-picker             patch-id match only (no PR)
 [ ] worktree  old-experiment           merged, but 3 ignored files (.env …)
 [ ] branch    rescue-notes             open PR #498, not merged
HAS WORK THAT EXISTS NOWHERE ELSE
 [ ] worktree  mtplx-profiles           4 unpushed commits, 2 dirty files
IN USE / LOCKED (cannot remove)
 [-] worktree  current directory        you are here
── spike-picker ────────────────────────────────────────
 No PR found. 2 commits; patches appear in main, merge-tree not proven.
```

- Four groups, in that order. Only PROVEN rows start ticked.
- A footer line explains the highlighted row (the evidence, or what is
  missing).
- `origin` branches appear as rows of kind `remote` under the same groups.
- Must be usable at 80x24 and with long branch names (truncate the name, never
  the evidence). Verify the real screen via the pty driver (see
  `memory/tui-screens-via-pty`), not only model-level tests.

## Classification

Classification is a **pure, read-only function over one snapshot**. Nothing is
stored (matches "routing is derived, not stored"). It never mutates.

### Snapshot (facts)

One `for-each-ref` (heads and remotes, with upstream and gone state; the
synthetic `<remote>/HEAD` symbolic refs are skipped, as
`internal/worktree/enumerate.go` does);
`git worktree list --porcelain` including `locked` and `prunable`; the default
branch; the base ref; PR facts; live session claims; the caller's cwd; and
whether a fetch ran and when.

- **Default branch:** try `origin/HEAD`, then local `main`, `master`, then
  `origin/main`, `origin/master` (repo-cleanup's chain). If none resolves,
  nothing is classified: no snapshot is built and `wt cleanup` exits 1 with
  the cause (see Exit codes). Today's `DefaultBranch` returns `""` silently
  (`internal/worktree/enumerate.go`); a deleter must treat `""` as a refusal.
- **Base = `origin/<default>`**, never the local default (a local main with
  unpushed commits must not make work look merged). Local default only as a
  fallback, and then every verdict is capped at "less sure".
- **Fetch:** `wt cleanup` runs an explicit `fetch --all --prune` (every remote,
  because "pushed" below reads all of `refs/remotes/*`) and reports its
  result in the header. The fetch is the one thing the command writes, and it
  writes only remote-tracking refs. It runs with `GIT_TERMINAL_PROMPT=0` so a
  credential prompt cannot hang the screen. Failure is shown, not swallowed (the picker's silent
  5s fetch is not reused). A stale base only under-detects merges, so skipping
  is the safe direction; the header says how old the base is.
- **PR facts are tri-state** per branch: known (open / merged / closed, number,
  head OID, base) · none · **unknown**. A per-branch query
  (`gh pr list --head <b> --state all`), keyed by head repo as
  `internal/worktree/prs.go` does. The picker's existing `--limit 200`
  listing is not acceptable as a gate: it can miss an open PR and turns a
  failure into an empty set.

### Evidence ladder (proof of "merged")

First proof wins; the evidence name is kept for the row and the JSON.

1. same commit as base — *excluded when the branch is new* (see min-age)
2. ancestor of base (`merge-base --is-ancestor`, by exit code)
3. trees match
4. no changes added (`diff-tree --quiet` against the merge-base)
5. **merge-tree adds nothing**: `git merge-tree --write-tree <base> <branch>`;
   exit 0 and line 1 equals `<base>^{tree}` → proven; exit 1 → conflict, not
   merged; anything else → error → UNKNOWN. Covers a multi-commit squash
   merge with no `gh`, and — unlike `git cherry` — fails safe if the change
   was later reverted on main.
6. **PR evidence** (needs `gh`): a MERGED PR into the default branch whose
   head OID equals the branch tip and whose merge commit is an ancestor of the
   base.

Not proofs (shown as LESS SURE, never ticked): `git cherry` all-equivalent
and whole-diff patch-id. Both prove patch equivalence, not that the content
is still present. Cherry also ignores merge commits.

### Statuses

- **PROVEN** — an ladder rung holds; no blocker applies.
- **LESS SURE** — only a non-proof signal, or a proof exists but a soft issue
  does (ignored files present, gh unavailable where a rung needs it).
- **UNIQUE WORK** — commits whose content is not in the base (no ladder rung
  holds) and that are on no pushed ref, a merged PR whose head moved on, or
  uncommitted/untracked files, or detached commits on no branch. A proven rung
  clears the commit test (a squash-merged branch whose remote branch was
  deleted has unpushed commits by ancestry and is still PROVEN); it never
  clears the dirty-file test.
- **No proof, nothing unique** — no rung holds, no weak signal, and every
  commit is on a pushed ref (e.g. an open PR): LESS SURE, reason
  `no_proof`. PROVEN is never a fall-through: it requires a holding rung.
- **IN USE** — hard blockers; the row cannot be ticked.
- **UNKNOWN** — any git error, unresolved default, or unknown PR state where a
  proof needs it. Shown in LESS SURE with the cause; never PROVEN.

### Worktree gates (independent of the branch)

> Superseded in part by `2026-11-11-wt-user-driven-cleanup-design.md`: "root held by another live `wt` session" is no longer a hard blocker: a held row is a floor (the row reads less sure and removal needs the typed `delete` / `--accept-loss`), and the confirmation names `held by session <id>`. The note that claims exist for the repo root only is stale too: every launch claims the worktree its agent runs in. The other hard blockers (primary, cwd inside it, locked, contains another worktree, an operation in progress, a registered artifact) stay, and a worktree whose directory is gone still reads less sure and now also locks the row in the list.

A **prunable** worktree (directory gone) runs no gates against the missing
path: its row is LESS SURE with reason `prunable`.

Hard blockers (not tickable, not overridable): primary worktree · cwd is
inside it · root held by another live `wt` session · locked · contains
another worktree · rebase/merge/cherry-pick/revert/bisect in progress ·
registered `local_path` model artifact under it (carried from PR 2: wt never
deletes these; print the paths).

Soft (row moves out of PROVEN): dirty (`status --porcelain -z
--untracked-files=normal --ignore-submodules=none`, pinned so user config
cannot hide changes; re-run for fsmonitor) · **ignored files present**
(`ls-files --others --ignored --exclude-standard --directory`; listed with
count/size — they die with the directory, e.g. `.superpowers/sdd`, `.venv`,
`.env`) · detached HEAD (clean and reachable from the base → can be PROVEN;
otherwise UNIQUE WORK) · skip-worktree/assume-unchanged files.

Live agents in a **linked** worktree are not detectable by claims (wt writes a
claim for the repo root only). Carried decision from PR 2: a by-cwd process
scan names live processes in the confirmation; it is a warning, not a gate.

### Remote (`origin`) branches

Tickable only with a MERGED PR for the head, no open PR on the head, no open
PR using it as base (deleting a base closes the dependent PR), and content
proven in `origin/<default>`. Deletion is leased to the scanned tip
(`--force-with-lease`), so a later push is never deleted. "Merged into main"
without a merged PR stays out of PROVEN (it may be an unrelated new branch at
main). Without `gh`, rows say "can't verify" and are not tickable.

### Safety rules that apply everywhere

> Superseded in part by `2026-11-11-wt-user-driven-cleanup-design.md`: "Default branch in any form is never listed as removable" no longer holds: the default branch is listed like any row and is a floor (never proven, removable with the typed `delete` / `--accept-loss`); `origin/<default>` is still never deleted (`retained_default`).

- **Min-age:** a branch with no commits of its own (its tip is the base or an
  ancestor of it) that is younger than a non-zero threshold is never PROVEN (a
  brand-new branch looks merged). Age is the ref's creation time (the oldest
  reflog entry; a worktree's `gitdir` file mtime for its branch), never the tip
  commit's date, which is the base's. No readable creation time counts as
  young. Default decided at plan time (non-zero).
- Never classify or delete by name alone: map worktree→branch from the
  porcelain list (feature/x and feature-x flatten to the same directory), and
  compare full refnames from `for-each-ref` (case-insensitive volumes resolve
  `Feature/x` via `feature/x`).
- Default branch in any form (`IsDefaultBranchForm`) is never listed as
  removable.

## Details settled at review (2026-11-04)

Items marked **(my call)** were decided by the author, not the user; the user
checks them while reading.

- **Row model (my call).** One row per worktree, carrying its branch; a
  separate `branch` row only for a local branch with no worktree. Ticking a
  worktree row removes the worktree and, if that branch is itself PROVEN,
  deletes the branch afterwards (CAS); otherwise the branch is kept and the
  outcome says why (`retained_*`). A branch checked out in a worktree is never
  its own row. A worktree that is IN USE shows its branch in the detail line
  only. A `remote` row is separate from the local branch of the same name.
- **Status precedence (my call).** First match wins: IN USE → UNIQUE WORK →
  LESS SURE → PROVEN. Tracked-modified or non-ignored untracked files count
  as UNIQUE WORK even when the branch is proven merged; ignored-only files
  are LESS SURE.
- **"Pushed" (my call).** A commit counts as saved if reachable from any
  `refs/remotes/*` ref after the fetch (not only the upstream or
  `origin/<same-name>`); otherwise it is local-only. A gone upstream does not
  change that: only reachability does.
- **PR query.** Per branch, `gh pr list --head <b> --state all --json
  number,state,headRefOid,baseRefName,mergeCommit,headRepository`, filtered to
  this repository's own head repo (no fork PRs). With several PRs for one
  head: the open-PR veto applies if any is open; PR evidence uses the merged
  PR whose head OID equals the branch tip. A failed or timed-out query is
  UNKNOWN, never "none". Parallelism, timeout and rate-limit handling are the
  builder's.
- **Claims.** Read with the existing `internal/sessionclaim` (`Others()`
  read-only; liveness is the writer's own `live()`). It never writes or
  removes a claim, and a stale claim is treated as absent as the claims
  contract already says.
- **Re-check in PR B (my call).** Per item, sequentially, immediately before
  acting: re-snapshot that item and re-run the ladder (no new fetch). The CAS
  uses the re-classified commit. An item is skipped only if its status dropped
  below what the user saw ticked or its commit moved; a change in the
  *evidence name* alone is not a skip. The skip list is reported at the end.
- **Exit codes, PR A (my call).** `0` the list was produced (whatever the
  statuses); `1` it could not be classified at all (no repo, default branch
  unresolved, base missing); usage errors per the existing wt CLI. A failed
  fetch or an UNKNOWN row is data in the output, not an exit code.
- **Live processes by cwd.** macOS only; tool (`lsof`/`ps`) is the builder's.
  Warning only, as carried from PR 2.
- **Remote (my call).** Rows use the remote the branch's upstream names, else
  `origin` (PR C narrowed this: origin only, other remotes stay listed and locked; see its plan, R1); fork workflows are out of scope (single-user setup).
- **Carried details.** Trash-rename, drain, lock immunity, the `-D`
  git-parity matrix, `branch_outcome`, and registered-artifact refusal keep
  the wording of PR 2/PR 3 in `2026-11-04-worktrunk-ports-design.md`, which
  stays the source for those details; this document supersedes only what it
  changes.

## Series

Each PR: TDD red-green-revert, fresh worktree, three-lens review, contracts
fixture in the same change, docs in the same PR (`wt/CLAUDE.md` and a
`wt/docs/wt-cleanup.md` command reference, like `wt-smoke.md`; guides
document the command, never live state). Branch names in tests: `topic`, not `feature`.

### PR A — read-only `wt cleanup` (and `--json`)

New classifier package (builder's choice of name/location; `internal/worktree`
holds the helpers it reuses), exit-code-aware git runner (the existing
`runGit` has no timeout, `-C` or exit-code surface), the snapshot, the ladder,
gates, statuses, the list TUI, and `--json`. **No deletion code exists in this
PR.** Without `gh` it still works; PR rungs report "needs gh".

- `--json`: versioned envelope with published schema + sample fixture in
  `docs/contracts/` + a Go contract test, same change. Rows carry
  `kind`, `name`, `status`, `evidence`, `unknown`, `reasons[]`, `blockers[]`;
  an absent `evidence` is "no proof", `"unknown": true` is "not determined"
  (never `null`); one structured result
  even on nonzero exit; stable enums. Envelope keys pinned at plan time
  against worktrunk's `list-v2.json` where they fit.
- **Acceptance:** a table of cases ported from repo-cleanup's
  `setup_test_repo` fixtures (true merge, fast-forward, 1:1 rebase,
  multi-commit squash with main advanced, squash-then-revert (must **not** be
  PROVEN), reused branch name, empty new branch, detached with unique
  commits, dirty / ignored / locked / nested, merge commit with unique
  content, git failure → UNKNOWN). Plus a recorded comparison of `wt cleanup
  --json` against `repo-cleanup --dry-run` on this repo; any disagreement is
  explained in the PR.

### PR B — local removal

Enter removes ticked worktrees and local branches. Also `wt remove
<worktree>` as the single-item form over the **same engine**.

> **As built** (see the PR B plan,
> `docs/superpowers/plans/2026-11-05-wt-cleanup-pr-b.md`, its "Calls" and
> the note on rulings A-U after them): no trash-rename or drain (plain,
> synchronous `git worktree remove`; `--force` only for an approved dirty
> worktree); approvals are per item and sealed (`--accept-loss` / typed
> `delete` for anything not proven, `--force` for dirt, `-D` per item);
> `branch_outcome` drops `retained_capped`; `wt remove` takes several
> targets.

- Re-classify each item immediately before acting; skip and report any that
  changed since the list was drawn.
- Worktrees: plain `git worktree remove`, never `--force`; trash-rename and a
  single background drain (re-running finishes an interrupted drain); locked
  immune. Branches: `git update-ref -d <ref> <classified-sha>` (compare and
  swap) after a fresh worktree list shows no other checkout.
- Force scopes, never conflated (carried): `--force` = proceed despite a dirty
  worktree; `-D` = delete an unmerged branch, behind its own confirmation. A
  hard blocker is never overridden.
- `branch_outcome`: `deleted` / `not_attempted` / `retained_unmerged` /
  `retained_capped` / `retained_*` (CAS-race skip named at plan time).
  Force deletes *despite* a verdict, never *in place of* one; the CAS skip
  stands under `-D`.
- Confirmation lists what will be lost: ignored-file counts/sizes, live
  processes by cwd, unique commits for anything unticked-then-ticked.
- Accept: dirty/locked/default/current refused; squash-merged branch
  (this repo's norm) deleted; a moved ref between classify and delete causes a
  safe skip.

### PR C — `origin` removal

Rows of kind `remote`, rules above, leased delete, confirmation that names the
audience ("visible to everyone with access to the repository") and the count.
Requires `gh`; absence leaves local rows fully working.

**As built:** [../plans/2026-11-07-wt-cleanup-pr-c.md](../plans/2026-11-07-wt-cleanup-pr-c.md)
(rulings and where reality differed from this section: an explicit-sha
lease, `origin` only, its own `Remote` approval, a per-remote fetch).

### PR D — move local-only work to a new worktree

From UNIQUE WORK rows, `wt rescue` (and `r` in the list) creates a new branch
(default `rescued/<name>`) at the original's exact HEAD, in a new worktree;
the unpushed commits are on it, and the uncommitted files come along still
uncommitted (staged stays staged). Refuse (leave the original untouched) if a
file is both staged and modified, or a nested repository is present. Never
deletes the original.

*Amended when PR D was built (2026-11-07).* The first draft said "the
original becomes removable". It does not: `unpushed` is counted against
remote-tracking refs only, so a local `rescued/*` branch does not clear it,
and the uncommitted files are **copied**, not moved, so the original stays
dirty. It therefore still reads unique work and removing it still takes
`--accept-loss --force -D`. Making it provably removable is a separate
classifier change ("preserved by branch X": a local ref at the tip plus a
verified snapshot of the dirt) with its own fail-closed proof. Details and
rulings: [the PR D plan](../plans/2026-11-07-wt-cleanup-pr-d.md).

*Amended again when the rescue record's reading side was built (2026-11-08).*
A *clean* original (commits only, no staged, unstaged or untracked changes)
now does read removable once a `wt rescue` record keeps its commits: it is
`proven` with evidence `rescued` and `wt remove --yes` takes it. A dirty
original is unchanged and still takes `--accept-loss --force -D` (covering
dirt is a later PR). Design:
[the preserved-by-design spec](2026-11-07-wt-rescue-preserved-by-design.md).

## Effect on the worktrunk-ports series

- PR 2 (`wt remove` + JSON) and PR 3 (integrated-only deletion + `wt prune`)
  are **replaced** by PRs A–C above; their decided values carried: dirty gate,
  force scopes, git-parity `-D` matrix, trash-rename drain, lock immunity,
  registered-artifact refusal, live-process warning, CAS deletion, min-age,
  `branch_outcome` enum, contracts discipline. `wt prune` is dropped as a
  separate command; `wt cleanup` is the batch form.
- **Changed from PR 3's spec (flag for review):** the spec's "six checks, all
  passing, patch-id included" becomes the ladder above, where the first
  proof wins and patch-id/cherry are *not* proofs. Reason: the research found
  `git cherry` and patch-id can mark reverted work as merged; `merge-tree`
  and PR-tip evidence cover this repo's squash-merge norm without that risk.
  Worktrunk's own ladder is first-proof-wins, so the spec's "all" reading
  looks like a misstatement — the builder confirms against
  `worktrunk/src/…/integration.rs` (:159–:482).
- PRs 4 (copy-ignored), 5 (tether teardown), 6 (addressing shortcuts) are
  unchanged. PR 5's `--reap` plugs into PR B's confirmation.
- The T0 worktrunk spike (never run worktrunk on PATH — binary collision)
  still applies before any schema pins an edge case.

## Fate of the `repo-cleanup` skill (my call)

> Superseded in part by `2026-11-11-wt-user-driven-cleanup-design.md`: the skill's job is meant to become the same sub-command: it is to call `wt worktrees --json` for what is safe to remove, and the root-switching menus have an equivalent in `wt restore-root` (and `R` in the list), so the skill is meant to stop switching the root itself (the skill file lives outside this repository and is unchanged).

Unchanged until PR A's comparison is clean. After PR B it can shrink to a
wrapper that calls `wt cleanup`, or be retired; the user decides then. Its
root/diverged-main menus have no `wt` equivalent in this design.

## Left to the builder

Package name/location; TUI details within the picker's conventions;
min-age default; exact JSON key names; schema file layout; whether the
min-age/new-branch rule also hides rows or only demotes them.

## Open items

- ~~Confirm the ladder reading against worktrunk's source.~~ Confirmed
  2026-11-04: `check_integration` in `src/git/mod.rs:275` returns "as soon as
  any integration reason is found" (same commit → ancestor → no added changes
  → trees match → merge adds nothing → patch-id). The earlier spec's "all
  passing" was a misstatement. Patch-id remains in worktrunk's ladder; this
  design deliberately does *not* count it as a proof (see the Evidence
  ladder), which is a departure from worktrunk, not a port.
- Linked-worktree session claims are out of scope here; revisit if the
  by-cwd warning proves too weak.
