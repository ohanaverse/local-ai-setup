# `wt restore-root`: move the root's branch into a worktree and put the root back on the default branch — Design

**Status:** draft (2026-11-08). Issue #522. Written from the issue, the rescue engine (#529, #542, #547, #550) and the root-claim code, with no interview: the user asked for a draft straight from the issue. Every decision below is therefore **my call**, marked `[call]`, for the user to confirm or overturn. Nothing is built.

All paths are relative to `wt/` unless they start with `docs/`.

## Intent

Work sometimes starts directly in the repository root, leaving it on a branch other than the default (`main`/`master`). That blocks other sessions and tools that expect the root on the default branch, and `wt` assumes the default branch is only ever the primary checkout (`CLAUDE.md`: "Default branch is never a linked worktree"). The user wants one command that moves that branch, with its uncommitted work, into a linked worktree and leaves the root on the default branch, fast-forwarded when that is safe.

`repo-cleanup` (the user's separate tool) already offers a "move the root's branch to `.worktrees/<branch>`" step; this is the `wt`-native version, built on `wt rescue`'s engine and its proofs.

## What exists to build on (verified in the code)

- `internal/rescue` copies any worktree's local-only work (commits stay on the branch; staged, unstaged and untracked files are carried) into a **new** branch and worktree, verifies the destination against the source byte for byte, and writes the immutable record `refs/wt/rescued/<id>` (commits I = index snapshot, W = working-tree snapshot). Its package comment says the engine accepts the primary checkout as a source; `Preflight` refuses it with `CodePrimary` ("a separate step (#522)"), and `CLAUDE.md` says #522 "adds its own post-verify 'restore root' step" that is not built or stubbed.
- It never deletes, switches or moves a ref ("Deletion belongs to internal/removal alone"); `internal/cleanup` code must not contain `update-ref`, `worktree remove`, `os.Remove(` and similar (guard tests).
- `internal/cleanup` reads the record: a worktree whose index and working tree still equal it reads `less_sure` (#550). So "the root's dirt equals the record" is already a checkable, fail-closed question (`dirtCovered`).
- `internal/sessionclaim` and `internal/rootclaim` record live `wt` sessions in the root under `<git-common-dir>/wt-sessions/<pid>`; `cleanup` surfaces one as `BlockHeld`.
- `worktree.CheckNew(repo, name)` picks the destination (`.worktrees/<flattened branch>`); `worktree.CreateAt` runs `git worktree add -b` with hooks off.

## Goals

- From a root on a non-default branch, one command leaves the root on the default branch and the branch (with all its uncommitted work) in a new linked worktree, **the same branch, not a copy**: its name, its commits and its upstream stay.
- No work is lost on any path: every refusal happens before anything changes; every failure after the new worktree exists leaves both places recoverable and says so.
- `--dry-run` shows exactly what would move and what would be left behind; a terminal gets a confirmation; no terminal needs `--yes`.
- The root is fast-forwarded to `origin/<default>` only when that is a clean fast-forward, and never by a merge.

## Non-goals

- A detached root. There is no branch to keep (the issue says refuse). `[call]` v1 refuses and names `git switch -c <name>` first. A rescue-style `rescued/` branch for a detached root is a later option.
- A root already on the default branch (nothing to do; exit 0 with that message).
- Carrying ignored files (they stay in the root, as in `wt rescue`; the count is shown).
- Anything the rescue engine already refuses (see "Refusals"). Lifting those is `wt rescue`'s job, and both commands improve together.
- A TUI row/key. `[call]` v1 is a command plus a hint line; a `wt cleanup` list key can follow.
- Pruning records (#543).
- Stashing as a fallback. `git stash` is a shared stack and loses untracked/mode fidelity; the verified copy plus the record is the safer carrier.

## Anti-goals (a failure even if it "works")

- Deleting the root's uncommitted work before a verified copy exists **and** the record ref exists **and** the root is unchanged since the copy was verified.
- Switching the root under a live `wt` session that holds it.
- Merging, rebasing or force-moving the default branch.
- Leaving the root detached or on a half-switched state after a failure.

## Decisions (all `[call]`)

1. **Command:** `wt restore-root [--to DIR-NAME] [--dry-run] [--yes] [--json] [--no-fetch]`, run from anywhere in the repository (like `--root`, it finds the primary checkout). `[call]` the name; `wt root restore` and `wt rescue --root` are the alternatives. A new command because `rescue` creates a *new* branch and leaves the source untouched, while this moves the *same* branch and changes the root.
2. **Same branch moves, via a detached first step.** Git will not check one branch out in two worktrees, so the order is: (a) add the new worktree **detached** at the branch's HEAD, (b) carry and verify the uncommitted work into it with the rescue engine, (c) write the record, (d) release the root, (e) switch the new worktree onto the branch. The root is untouched until (d).
3. **The destructive step (d) lives in `internal/removal`**, which owns deletion, behind its own guard tests; `internal/rescue` and `internal/cleanup` stay deletion-free. A new package `internal/rootmove` composes: rescue's `Preflight`/carry/verify/record, then removal's `ReleaseRoot`, then the final switch and fast-forward.
4. **Reuse rescue's refusals unchanged**, plus the root-specific ones below. `[call]` the destination can reach the same object database, so a stronger carrier (check out W's tree, `read-tree` I) could lift rescue's "path both staged and modified" refusal. Deliberately not done in v1: it would be a second carrier to prove. Recorded as a follow-up.

## The flow

`Preflight` (reads only, nothing created):

1. Find the primary checkout; refuse if it is bare, or has no commit.
2. Read its branch. Refuse **detached**, and refuse if the branch **is** the default (success message, nothing to do).
3. Resolve the default branch (`rescue.DefaultBranch` / the `cleanup` resolver) and refuse if it is checked out in a linked worktree (the repository already violates the invariant; moving the root there would put the default in two places). `[call]`
4. Refuse if another live `wt` session holds the root (`sessionclaim.Others`, the same read `cleanup` uses): moving a branch under a running agent is the harm the claim exists to prevent. This command's own process holds no claim.
5. Run rescue's `Preflight` against the root as a source, with `CodePrimary` lifted for this caller (the engine takes the root; the command layer decides which sources a command offers). All its refusals apply: operation in progress, unmerged paths, hidden index flags, submodule change, staged-and-modified path, intent-to-add, nested repository, special file, size caps (20000 files / 512 MiB), line-ending and content-filter conversions, unreadable state.
6. Choose the destination (`worktree.CheckNew`) and take the root's fingerprint.
7. Compute `default_ff`: whether `origin/<default>` is a strict fast-forward of local `<default>` (using the last fetch; `--dry-run` and `--no-fetch` never fetch, a real run fetches `origin` once before this step). Informational; a failure to fast-forward never refuses the move.

If there is **nothing uncommitted**, steps (b)-(c) are skipped: the move is just `worktree add`, release and switch. The branch's commits need no copy; they live on the branch ref.

`Execute` (after confirmation; each step re-checks the one before it):

1. `git worktree add --detach <dest> <branch HEAD>` with hooks off (`CreateAt` gains a detached variant; branch-creating callers unchanged).
2. Carry and verify the uncommitted work into `<dest>` with the rescue engine's existing copy and `verify` (status byte-for-byte equal, every path equal in kind, mode, content and link target).
3. Write the record `refs/wt/rescued/<id>` exactly as `wt rescue` does (create-only `update-ref`, after re-taking the root fingerprint). `[call]` the move **requires** the record: if it cannot be written, step 4 does not run and the command reports `failed` with the new worktree kept. (`wt rescue` treats a missing record as a warning; here the record is what makes the next step safe.) With nothing uncommitted there is no record and no step 4 work.
4. **Release the root** (`removal.ReleaseRoot`), only if all three hold: the verified copy exists, the record ref exists, and the root fingerprint equals the one the copy was verified against (nothing changed since). It runs `git reset --hard HEAD` and removes **exactly the untracked paths the copy carried** (never `clean -x`, never a wildcard), then `git switch <default>`. If the fingerprint differs, it stops before touching anything: `failed`, code `source_changed_while_copying`, both places kept.
5. In `<dest>`, `git switch <branch>` (the branch is free now). The branch's upstream and config are unchanged because the branch ref never moved.
6. In the root, fast-forward `<default>` to `origin/<default>` with `git merge --ff-only` when `default_ff`; otherwise leave it and say why. Never a merge, never `--force`.
7. Final read-back: the root is on `<default>`, clean; `<dest>` is on `<branch>` and its status equals the verified state. `moved` is claimed only after this read-back, as in `wt rescue`.

Recovery after a failure at each step (the report prints the one that applies):

| Failed at | Root | New worktree | Work |
|---|---|---|---|
| 1 | untouched | absent or empty (kept, path named) | all in root |
| 2 | untouched | half-built, kept | all in root |
| 3 | untouched | verified copy, kept | in root and copy |
| 4 (before the reset) | untouched | verified copy | in root and copy |
| 4 (after the reset, before the switch) | on the branch, clean | verified copy | in copy and the record |
| 5 | on `<default>`, clean | detached at the branch tip with all work | in the copy and the record; `git switch <branch>` there finishes it |
| 6 | on `<default>` | on `<branch>` | all moved; only the fast-forward is missing |

There is no rollback (rescue's rule: the only rollback is a forced removal of a directory holding carried work). The state in every row keeps the work reachable.

## Why the release step is safe

Step 4 discards the root's working-tree changes, so its guard is the whole safety argument: (1) the copy was verified equal to the root at fingerprint F; (2) the record ref holds immutable commits I and W for that state, so even if the new worktree is deleted later the uncommitted work is recoverable from git; (3) the root still equals F at the instant of the reset. This is the same triple `wt remove` relies on for a dirty rescued original (#550's `dirtCovered`); the implementation should call `cleanup`'s check (the root's index and working tree equal the just-written record) as the final gate rather than re-derive it, so the two agree by construction.

The window between that last check and `git reset --hard` is not zero, as in #550. The report states it and the docs say so.

## Output and contracts

- Text for a terminal: what moves (branch, commit count, staged/unstaged/untracked counts, ignored entries left behind), where (`<dest>`), what the root becomes, whether the fast-forward will happen, then `Move? [y/N]`.
- `--json`: outcome `moved|would_move|refused|failed`, `code`, `reason`, `branch`, `dest_path`, `record`, `default_ff`, `completed` (steps), `created`. New contract `docs/contracts/restore-root-result.*`, additive in the style of `rescue-result.*`, with a contract test in `cmd/wt`. Refusal codes reuse rescue's strings and add: `detached_root`, `already_default`, `default_in_worktree`, `root_held`, `no_commit`, `release_refused`.
- Exit codes follow `wt rescue`: 0 moved (or `--dry-run` would), 1 could not start, 2 refused or failed, 3 approval missing.

## Surfacing

- The shared-root menu (`c`/`w`/`q`) and the root gate in `cmd/wt/rootgate.go` print, when the root is on a non-default branch, one added line: ``run `wt restore-root` to move it and put the root back on <default>``. Text only, no new key. `[call]`
- `docs/guides/08-maintenance-and-troubleshooting.md` §9 and `wt/docs/wt-cleanup.md` get a short pointer; `wt/CLAUDE.md` replaces the "#522 reuses the engine" bullet with the real description.

## Constraints

- Fail closed: any git/IO error, unexpected state or limit hit refuses before step 1, or stops at the step it occurs in with the table above.
- Hooks off during `worktree add` (as rescue); a `post-checkout` hook warning is shown.
- The command never fetches under `--dry-run`/`--no-fetch`; a real run fetches `origin` once, read-only (it rewrites remote-tracking refs only). `[call]`
- The destructive step is the only deletion; it is in `internal/removal`, covered by that package's deletion guard (extend its allow-list by one named function) and by a new test that fails if `ReleaseRoot` runs without a verified copy, a record or an unchanged fingerprint.
- Ad-hoc `wt` runs against a scratch registry need `WT_LITELLM_CONFIG` as usual (this command touches no registry, but the test harness sets it).

## Test plan (outline)

Real scratch repos throughout, as in the rescue and removal tests:

- Happy paths: clean branch; branch with unpushed commits; staged + unstaged + untracked; upstream preserved; modes preserved; fast-forward done / skipped (diverged, behind-only, no remote).
- Every refusal code, each with the root untouched (checked by a before/after snapshot of refs, index and files).
- Held root (a live claim from another pid) refuses; a stale claim does not.
- Failure injection at each step 1-7 using seams like rescue's `afterCarry`/`afterVerify`/`beforeRecordRef`, asserting the recovery table row.
- Root edited between verify and release: `release_refused`, nothing reset.
- Record not writable: step 4 does not run.
- Default branch checked out in a linked worktree: refused.
- Contract test for the JSON; guard tests (deletion words, losses) stay exact.
- Acceptance on the built binary against a scratch repo with a bare `origin`, including `--dry-run`, the no-terminal `--yes` path and an edit made while the confirmation prompt is held.

## Left to the builder

- The exact fingerprint/ gate plumbing between `rescue`, `removal` and `rootmove`, as long as the three-part guard holds.
- Whether `CreateAt`'s detached variant is a new function or a parameter.
- Wording of messages, and the exact `--json` field names beyond those listed.
- How the fetch is done (reuse `cleanup`'s fetch helper).

## Open questions for the user (my calls to confirm)

1. Command name: `wt restore-root` vs `wt root restore` vs `wt rescue --root`.
2. A detached root: refuse (v1) vs offer a `rescued/` branch.
3. Requiring the record for the move (the release step discards the root's dirt, so I made the record mandatory, unlike `wt rescue`).
4. Fetching `origin` on a real run before the fast-forward, vs never fetching and fast-forwarding only to the last-fetched `origin/<default>`.
5. Surfacing only as a hint line now, or also as a `wt cleanup` list key in the same PR.
