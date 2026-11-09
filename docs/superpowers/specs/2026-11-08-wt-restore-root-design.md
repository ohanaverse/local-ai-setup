# `wt restore-root`: move the root's branch into a worktree and put the root back on the default branch — Design

**Status:** approved by the user for planning (2026-11-08). Issue #522. Written from the issue, the rescue engine (#529, #542, #547, #550) and the root-claim code. The five open questions of the first draft are answered (see "Decisions"): the user confirmed all five as drafted. Anything still marked `[call]` is my call, not yet confirmed. Built in two PRs (A: engine seams, B: the command); this document is amended to match (see Amendments).

All paths are relative to `wt/` unless they start with `docs/`.

## Intent

Work sometimes starts directly in the repository root, leaving it on a branch other than the default (`main`/`master`). That blocks other sessions and tools that expect the root on the default branch, and `wt` assumes the default branch is only ever the primary checkout (`CLAUDE.md`: "Default branch is never a linked worktree"). The user wants one command that moves that branch, with its uncommitted work, into a linked worktree and leaves the root on the default branch, fast-forwarded when that is safe.

`repo-cleanup` (the user's separate tool) already offers a "move the root's branch to `.worktrees/<branch>`" step; this is the `wt`-native version, built on `wt rescue`'s engine and its proofs.

## What exists to build on (verified in the code)

- `internal/rescue` copies any worktree's local-only work (commits stay on the branch; staged, unstaged and untracked files are carried) into a **new** branch and worktree, verifies the destination against the source byte for byte, and writes the immutable record `refs/wt/rescued/<id>` (commits I = index snapshot, W = working-tree snapshot). Before this work its package comment said the engine accepts the primary checkout as a source, but `Preflight` refused it with `CodePrimary` ("a separate step (#522)"), and `wt/CLAUDE.md` said #522 "adds its own post-verify 'restore root' step" that was not built or stubbed. Now `rescue.Engine.ForRoot` takes the primary checkout (see Amendments), and the `primary` refusal of `wt rescue` names `wt restore-root`.
- It never deletes, switches or moves a ref ("Deletion belongs to internal/removal alone"); `internal/cleanup` code must not contain `update-ref`, `worktree remove`, `os.Remove(` and similar (guard tests).
- `internal/cleanup` reads the record: a worktree whose index and working tree still equal it reads `less_sure` (#550). So "the root's dirt equals the record" is already a checkable, fail-closed question (`dirtCovered`).
- `internal/sessionclaim` and `internal/rootclaim` record live `wt` sessions in the root under `<git-common-dir>/wt-sessions/<pid>`; `cleanup` surfaces one as `BlockHeld`.
- `worktree.CheckNew` picks a new branch's worktree (`.worktrees/<flattened branch>`) and `worktree.CreateAt` runs `git worktree add -b` with hooks off; this command's destination is `worktree.CheckNewDetached`, created by `worktree.CreateDetachedAt` (a new function; `CreateAt` unchanged).

## Goals

- From a root on a non-default branch, one command leaves the root on the default branch and the branch (with all its uncommitted work) in a new linked worktree, **the same branch, not a copy**: its name, its commits and its upstream stay.
- No work is lost on any path: every refusal happens before anything changes; every failure after the new worktree exists leaves both places recoverable and says so.
- `--dry-run` shows exactly what would move and what would be left behind; a terminal gets a confirmation; no terminal needs `--yes`.
- The root is fast-forwarded to `origin/<default>` only when that is a clean fast-forward, and never by a merge.

## Non-goals

- A detached root. There is no branch to keep (the issue says refuse). **The user's decision:** v1 refuses and names `git switch -c <name>` first. A rescue-style `rescued/` branch for a detached root is a later option.
- A root already on the default branch (nothing to do; exit 0 with that message).
- Carrying ignored files (they stay in the root, as in `wt rescue`; the count is shown).
- Anything the rescue engine already refuses (see "Refusals"). Lifting those is `wt rescue`'s job, and both commands improve together.
- A TUI row/key. **The user's decision:** v1 is a command plus a hint line; a `wt cleanup` list key can follow.
- Pruning records (#543).
- Stashing as a fallback. `git stash` is a shared stack and loses untracked/mode fidelity; the verified copy plus the record is the safer carrier.

## Anti-goals (a failure even if it "works")

- Deleting the root's uncommitted work before a verified copy exists **and** the record ref exists **and** the root is unchanged since the copy was verified.
- Switching the root under a live `wt` session that holds it.
- Merging, rebasing or force-moving the default branch.
- Leaving the root detached or on a half-switched state after a failure.

## Decisions (all `[call]`)

1. **Command (the user's):** `wt restore-root [--to DIR-NAME] [--dry-run] [--yes] [--json] [--no-fetch]`, run from anywhere in the repository (like `--root`, it finds the primary checkout). The user chose this name over `wt root restore` and `wt rescue --root`. A new command because `rescue` creates a *new* branch and leaves the source untouched, while this moves the *same* branch and changes the root.
2. **Same branch moves, via a detached first step.** Git will not check one branch out in two worktrees, so the order is: (a) add the new worktree **detached** at the branch's HEAD, (b) carry and verify the uncommitted work into it with the rescue engine, (c) write the record, (d) release the root, (e) switch the new worktree onto the branch. The root is untouched until (d).
3. **The destructive step (d) lives in `internal/removal`**, which owns deletion, behind its own guard tests; `internal/rescue` and `internal/cleanup` stay deletion-free. A new package `internal/rootmove` composes: rescue's `Preflight`/carry/verify/record, then removal's `ReleaseRoot`, then the final switch and fast-forward.
4. **Reuse rescue's refusals unchanged**, plus the root-specific ones below. `[call]` the destination can reach the same object database, so a stronger carrier (check out W's tree, `read-tree` I) could lift rescue's "path both staged and modified" refusal. Deliberately not done in v1: it would be a second carrier to prove. Recorded as a follow-up.

## The flow

`Preflight` (reads only, nothing created):

1. Find the primary checkout; refuse if it is bare, or has no commit.
2. Read its branch. Refuse **detached**, and refuse if the branch **is** the default (success message, nothing to do).
3. Resolve the default branch (`cleanup.ResolveBase`; no default, or a default with no local branch, refuses `no_default_branch`) and refuse if it is checked out in a linked worktree (the repository already violates the invariant; moving the root there would put the default in two places). `[call]`
4. Refuse if another live `wt` session holds the root (`sessionclaim.Others`, the same read `cleanup` uses): moving a branch under a running agent is the harm the claim exists to prevent. This command's own process holds no claim.
5. Run rescue's `Preflight` against the root as a source through `rescue.Engine.ForRoot`, which skips `primary` and `nothing_to_keep` and takes a detached destination (the engine takes the root; the command layer decides which sources a command offers). All its refusals apply: operation in progress, unmerged paths, hidden index flags, submodule change, staged-and-modified path, intent-to-add, nested repository, special file, size caps (20000 files / 512 MiB), line-ending and content-filter conversions, unreadable state. After rescue's refusals: `ignored_collision` (an ignored file sits at a path the default branch tracks, or where the reset would destroy it; ignored files are never overwritten).
6. Choose the destination (`worktree.CheckNewDetached`) and take the root's fingerprint.
7. Compute `default_ff`: whether `origin/<default>` is a strict fast-forward of local `<default>` (using the last fetch; `--dry-run` and `--no-fetch` never fetch; a real run fetches once through `cleanup.Gather`, every remote, read-only, as `wt cleanup` does, before this step). Informational; a failure to fast-forward never refuses the move.

If there is **nothing uncommitted**, steps (b)-(c) are skipped: the move is just `worktree add`, release and switch. The branch's commits need no copy; they live on the branch ref.

`Execute` (after confirmation; each step re-checks the one before it):

1. `git worktree add --detach <dest> <branch HEAD>` with hooks off (`worktree.CreateDetachedAt`, a new function; `CreateAt` and its callers unchanged).
2. Carry and verify the uncommitted work into `<dest>` with the rescue engine's existing copy and `verify` (status byte-for-byte equal, every path equal in kind, mode, content and link target).
3. Write the record `refs/wt/rescued/<id>` exactly as `wt rescue` does (create-only `update-ref`, after re-taking the root fingerprint). **The user's decision:** the move **requires** the record: if it cannot be written, step 4 does not run and the command reports `failed`, code `record_not_written`, with the new worktree kept (the engine keeps its warning; `Record == nil` after a dirty copy is the failure here). (`wt rescue` treats a missing record as a warning; here the record is what makes the next step safe.) With nothing uncommitted there is no record and no step 4 work.
4. **Release the root** (`removal.ReleaseRoot`), only if all three hold: the verified copy exists, the record ref exists, and the root fingerprint equals the one the copy was verified against (nothing changed since). Execute repeats the claim check immediately before the release; the window after it is the one the report states. `ReleaseRoot` runs every git call in the root, whatever the caller's directory. It runs `git reset --hard HEAD` and removes **exactly the untracked paths the copy carried** (never `clean -x`, never a wildcard; a carried path the reset restored from HEAD is skipped; the empty directories removal leaves stay), then `git switch --no-overwrite-ignore <default>`. If the fingerprint differs or a proof is missing at the release, it stops before touching anything: `failed`, code `release_refused` (`source_changed` / `source_changed_while_copying` remain rescue's codes for the earlier checks), both places kept. With nothing uncommitted the release needs an unchanged fingerprint and a clean status.
5. In `<dest>`, `git switch <branch>` (the branch is free now). The branch's upstream and config are unchanged because the branch ref never moved.
6. In the root, fast-forward `<default>` to `origin/<default>` with `git merge --ff-only` only when `origin/<default>` is a strict descendant (`default_ff`); otherwise leave it and say why. It is skipped with a warning, status still `moved`, when the fast-forward would overwrite an ignored file. Never a merge, never `--force`.
7. Final read-back: the root is on `<default>`, clean; `<dest>` is on `<branch>` and its status equals the verified state. `moved` is claimed only after this read-back, as in `wt rescue`.

Recovery after a failure at each step (the report prints the one that applies):

| Failed at | Root | New worktree | Work |
|---|---|---|---|
| 1 | untouched | absent or empty (kept, path named) | all in root |
| 2 | untouched | half-built, kept | all in root |
| 3 | untouched | verified copy, kept | in root and copy |
| 4 (before the reset: a live claim appeared, or the release refused) | untouched | verified copy | in root and copy |
| 4 (after the reset, before the switch) | reset, or part reset (`release_failed`; may be partly changed when the reset or switch call itself failed) | verified copy | in copy and the record; finish by hand with `git switch <default>` in the root, then `git switch <branch>` in the destination |
| 5 (the destination switch failed, `switch_failed`) | on `<default>`, clean | detached at the branch tip with all work | in the copy and the record; `git switch <branch>` there finishes it. If the branch moved meanwhile, do not switch blindly |
| 6 | on `<default>` | on `<branch>` | all moved; only the fast-forward is missing (a warning, never a failure) |
| 7 (read-back mismatch, `verify_failed`) | as read back | as read back | nothing undone; the work is in the copy and the record |

Every `failed` reason ends with its row's sentence.

There is no rollback (rescue's rule: the only rollback is a forced removal of a directory holding carried work). The state in every row keeps the work reachable.

## Why the release step is safe

Step 4 discards the root's working-tree changes, so its guard is the whole safety argument: (1) the copy was verified equal to the root at fingerprint F; (2) the record ref holds immutable commits I and W for that state, so even if the new worktree is deleted later the uncommitted work is recoverable from git; (3) the root still equals F at the instant of the reset. This is the same triple `wt remove` relies on for a dirty rescued original (#550's `dirtCovered`); the implementation calls `cleanup.RecordCoversDirt` (the root's index and working tree equal the just-written record) as a final gate rather than re-deriving it, so the two agree by construction. It is a new exported read-only entry because `dirtCovered` ignores the primary checkout. The ignored-files check comes after it, last.

The window between that last check and `git reset --hard` is not zero, as in #550. The report states it and the docs say so.

## Output and contracts

- Text for a terminal: what moves (branch, commit count, staged/unstaged/untracked counts, ignored entries left behind), where (`<dest>`), what the root becomes, whether the fast-forward will happen, then `Move? [y/N]`.
- `--json`: a flat envelope that always writes `version`, `repo`, `dry_run`, `created`, `completed` and `warnings`, plus `outcome` (`moved|would_move|refused|failed`), `code`, `reason`, `branch`, `dest_path`, `record`, `default_ff` and, as optional keys, `carried` (counts), `left_behind` (`{ignored_entries}`), `fast_forwarded`, `default` and `error`. New contract `docs/contracts/restore-root-result.*`, additive in the style of `rescue-result.*`, with a contract test in `cmd/wt`. Refusal and failure codes reuse rescue's strings (not `primary` or `nothing_to_keep`) and add: `detached_root`, `already_default`, `default_in_worktree`, `root_held`, `no_commit`, `release_refused`, `ignored_collision`, `no_default_branch` and `record_not_written` (see Amendments for the rest). `already_default` is outcome `refused` with exit 0.
- Exit codes follow `wt rescue`: 0 moved (or `--dry-run` would), 1 could not start, 2 refused or failed, 3 approval missing.

## Surfacing

- The shared-root menu (`c`/`w`/`q`) and the root gate in `cmd/wt/rootgate.go` print, when the root is on a non-default branch, one added line: ``run `wt restore-root` to move it and put the root back on <default>``. Text only, no new key (the user's decision). The line is hidden when `origin/HEAD` is unset (`worktree.DefaultBranch` is its source); `wt restore-root` itself still works there. In the confirmation prompt it is its own `wt:` line before the question. After the move the new worktree is not marked preserved by the record in `wt cleanup` (the record's source path is the root).
- `docs/guides/08-maintenance-and-troubleshooting.md` §9 and `wt/docs/wt-cleanup.md` get a short pointer; `wt/CLAUDE.md` replaces the "#522 reuses the engine" bullet with the real description.

## Constraints

- Fail closed: any git/IO error, unexpected state or limit hit refuses before step 1, or stops at the step it occurs in with the table above.
- Hooks off (`core.hooksPath=/dev/null`) for the detached `worktree add`, the root switch, the destination switch and the fast-forward; a `post-checkout` hook warning is shown. `--to` names only the directory, never the branch.
- The command never fetches under `--dry-run`/`--no-fetch`; a real run fetches once through `cleanup.Gather`, every remote, read-only (it rewrites remote-tracking refs only), exactly as `wt cleanup` does (the user's decision, as built: see Amendments).
- The destructive step is the only deletion; it is in `internal/removal`, covered by `TestRootMutationFunctionsAreExactlyTheAllowList` and `TestHardResetExistsNowhereOutsideRemoval` (the deletion scan skips `internal/removal`, so the guard is a function-set test plus a module-wide scan for the reset words), with `internal/rootmove` and `cmd/wt/restore_root*.go` joining the deletion scan under `rootmoveBanned`, and by a test that fails if `ReleaseRoot` runs without a verified copy, a record or an unchanged fingerprint.
- Ad-hoc `wt` runs against a scratch registry need `WT_LITELLM_CONFIG` as usual (this command touches no registry, but the test harness sets it).

## Test plan (outline)

Real scratch repos throughout, as in the rescue and removal tests:

- Happy paths: clean branch; branch with unpushed commits; staged + unstaged + untracked; upstream preserved; modes preserved; fast-forward done / skipped (diverged, behind-only, no remote).
- Every refusal code, each with the root untouched (checked by a before/after snapshot of refs, index and files).
- Held root (a live claim from another pid) refuses; a stale claim does not.
- Failure injection at each step 1-7 using seams like rescue's `afterCarry`/`afterVerify`/`beforeRecordRef`, asserting the recovery table row.
- Root edited between verify and release, or claimed by another session while the confirmation prompt was held: `release_refused` / `root_held`, nothing reset.
- Record not writable: step 4 does not run.
- Default branch checked out in a linked worktree: refused.
- Contract test for the JSON; guard tests (deletion words, losses) stay exact.
- Acceptance on the built binary against a scratch repo with a bare `origin`, including `--dry-run`, the no-terminal `--yes` path and an edit made while the confirmation prompt is held.

## Left to the builder

- The exact fingerprint/ gate plumbing between `rescue`, `removal` and `rootmove`, as long as the three-part guard holds.
- Whether `CreateAt`'s detached variant is a new function or a parameter.
- Wording of messages, and the exact `--json` field names beyond those listed.
- How the fetch is done (reuse `cleanup`'s fetch helper).

## Decisions confirmed by the user (2026-11-08)

1. Command name `wt restore-root`.
2. A detached root is refused in v1.
3. The record is required before the root is released.
4. A real run fetches once before the fast-forward (as built: every remote, through `cleanup.Gather`); `--dry-run` and `--no-fetch` never fetch.
5. Surfacing in v1 is the hint line only; a `wt cleanup` list key can follow.

Still my calls (unconfirmed): refusing when the default branch is checked out in a linked worktree; the `internal/rootmove` package and `removal.ReleaseRoot` split; deferring a stronger carrier that would lift rescue's "path both staged and modified" refusal.

## Amendments (2026-11-08)

Written after the command was built (PR A: the engine seams; PR B: the command). The five Decisions the user confirmed above stand; the rulings below record what the build decided or found, and the sentences each one made stale were rewritten in place (the old wording is quoted here).

### Rulings

1. **The allow-list guard.** The spec said to "extend its allow-list by one named function"; there is none, because the deletion scan skips `internal/removal`. The guard is `TestRootMutationFunctionsAreExactlyTheAllowList` (the functions in `internal/removal` that hold a reset, switch or file-removal word are exactly `ReleaseRoot` and `removeCarried`) plus `TestHardResetExistsNowhereOutsideRemoval` (no other package holds `"--hard"`, `"reset"`, `"clean"`, `"checkout"`, `"restore"` or `"stash"` as an argv word). `internal/rootmove` and `cmd/wt/restore_root*.go` join the deletion scan with `rootmoveBanned` (`TestRootmoveContainsNoDeletion`, `TestRiskyRootmoveSourceBites`, `TestRootmoveIsInsideTheDeletionScanAndNeverAllowed`).
2. **`cleanup.RecordCoversDirt`.** `dirtCovered` ignores the primary checkout, so the final gate is a new exported read-only entry point rather than a re-derivation. It checks content and head, not the record's identity.
3. **The default branch** comes from `cleanup.ResolveBase`, not a rescue helper. No default, or a default that exists only as `origin/<d>`, refuses `no_default_branch`.
4. **`record_not_written`** is a rootmove code. The engine keeps its warning semantics (`rescue.Codes` and `rescue-result.*` are unchanged); here `Record == nil` after a dirty copy is the failure.
5. **`already_default`** is outcome `refused` with exit 0 (nothing to do).
6. **Hooks are off** for the detached add, the root switch, the destination switch and the fast-forward, and submodule recursion is off for the root's reset and switch.
7. **Empty directories** left by removing the carried untracked files stay in the root (a declared loss, with ignored files).
8. **Nothing uncommitted** skips the copy and the record; the release then needs an unchanged fingerprint and a clean status.
9. **`--to`** names only the directory under `.worktrees/`, never the branch (the same branch moves).
10. **The envelope is flat** (`branch`, `dest_path` at top level), unlike rescue's nested shape; it always writes `version`, `repo`, `dry_run`, `created`, `completed` and `warnings`.
11. **The record's `Wt-Source-Path` is the root** after the move, so `wt cleanup` does not mark the new worktree preserved by it. Accepted.
12. **`ignored_collision`** (additive): ignored files are never overwritten. Preflight refuses an ignored file the reset would destroy or one at a path the default branch tracks; the root switch uses `--no-overwrite-ignore`; the fast-forward is skipped (a warning, still `moved`) when it would touch an ignored path or an ignored parent path (case-folded when `core.ignorecase`).
13. **The fetch** is `cleanup.Gather`'s: every remote, read-only, once. The spec said `origin` once.
14. **A carried path the reset restored from HEAD** (a staged delete with an untracked twin) is skipped by `ReleaseRoot`, not a failure.
15. **The `CodePrimary` refusal** of `wt rescue` now names `wt restore-root` (moved from PR A to PR B).
16. **The hint** stays hidden when `origin/HEAD` is unset (a declared limit).
17. **`ReleaseRoot` ignores the caller's directory** and runs in the root; Execute re-checks the live claim immediately before the release.

Found while building, beyond the numbered rulings:

- **`worktrees_not_ignored`.** The destination is inside the root. A root that does not ignore `.worktrees/` would show the new worktree as untracked work (and existing ones as nested repositories), so Preflight refuses up front with the one-line remedy (`echo '.worktrees/' >> "$(git rev-parse --git-path info/exclude)"`) and writes nothing to the user's repository.
- **`hidden_index_flags` covers sparse checkouts.** Any skip-worktree entry, and any assume-unchanged entry, is refused in Preflight and again by `ReleaseRoot`, which fails closed because status cannot see an edit to one.
- **`release_failed` and `switch_failed`** are failure codes: `release_refused` would wrongly say nothing was touched after a reset. `release_failed` is a `*removal.ReleaseFailed` (its `Uncertain` flag is set for any reset or switch call that errored, so the text does not claim the root is untouched); `switch_failed` is step 5, the destination switch (also when the branch ref moved meanwhile). A read-back mismatch (step 7) is rescue's `verify_failed`. The code list has 32 entries.
- **`ReleaseRoot`'s gate order** is: inputs, the fingerprint, the status, hidden index flags, the record check, and the ignored-files check last; every git call runs in the root with fsmonitor off, and the reset and switch with hooks and submodule recursion off.
- **Output.** Reasons, steps and warnings print in full (one line, control characters stripped, no length cap) so a failure's recovery text is never cut. A benign note (the default already at origin) is a step in `completed`, not a warning. `wt rescue` still truncates its messages at 200 characters (a known limitation).

### Stale sentences rewritten in place

1. Status line: "Nothing is built." now reads "Built in two PRs (A: engine seams, B: the command); this document is amended to match (see Amendments)."
2. "What exists to build on": the `CodePrimary` sentence ("`Preflight` refuses it with `CodePrimary` ("a separate step (#522)"), and `CLAUDE.md` says #522 "adds its own post-verify 'restore root' step" that is not built or stubbed") is now past tense, names `Engine.ForRoot`, and the refusal text names `wt restore-root` (ruling 15).
3. "`worktree.CheckNew`" and "`CreateAt` gains a detached variant": the destination is `worktree.CheckNewDetached`, created by `worktree.CreateDetachedAt` (a new function; `CreateAt` unchanged).
4. Preflight step 3, "`rescue.DefaultBranch` / the `cleanup` resolver": `cleanup.ResolveBase`, with `no_default_branch` (ruling 3).
5. Preflight step 5, "with `CodePrimary` lifted for this caller": `rescue.Engine.ForRoot` skips `primary` and `nothing_to_keep` and takes a detached destination.
6. Preflight step 7, Constraints and Decision 4, "fetches `origin` once": a real run fetches once through `cleanup.Gather`, every remote, read-only (ruling 13).
7. Execute step 3, "reports `failed`" with no code: the code is `record_not_written` (ruling 4).
8. Execute step 4: the live-claim re-check before the release, `ReleaseRoot` running in the root, `--no-overwrite-ignore`, a carried path the reset restored skipped (ruling 14), empty directories left (ruling 7); "if the fingerprint differs ... code `source_changed_while_copying`" is now `release_refused` (fingerprint or proof missing at the release), `source_changed` / `source_changed_while_copying` remaining rescue's codes for the earlier checks; the nothing-uncommitted release needs an unchanged fingerprint and a clean status (ruling 8).
9. Execute step 6: "only when `origin/<default>` is a strict descendant", skipped with a warning (status still `moved`) when it would overwrite an ignored file.
10. The recovery table: row 4 has the live-claim variant, the after-reset row and row 5 name both manual finishes for a failed switch, rows for `switch_failed`'s moved branch and the read-back were added, and every `failed` reason ends with its row's sentence.
11. "Why the release step is safe", "the implementation should call `cleanup`'s check": `cleanup.RecordCoversDirt` (ruling 2).
12. Output and contracts: the envelope is flat with the always-written keys above and optional `carried`, `left_behind`, `fast_forwarded`, `default` and `error`; the code list adds `ignored_collision`, `no_default_branch` and `record_not_written` (and the later `release_failed`, `switch_failed`, `worktrees_not_ignored`); `already_default` is `refused` with exit 0 (ruling 5); `primary` and `nothing_to_keep` are not in this command's list.
13. Flow, step 2 and Preflight order: `ignored_collision` after rescue's refusals (ruling 12).
14. Constraints, hooks: "Hooks off during `worktree add` (as rescue)" now covers the root switch, the destination switch and the fast-forward (ruling 6); `--to` names only the directory (ruling 9).
15. Constraints, the destructive step: "extend its allow-list by one named function" is now the two guard tests and `rootmoveBanned` (ruling 1).
16. Surfacing: the hint is hidden when `origin/HEAD` is unset (ruling 16); the new worktree is not marked preserved after the move (ruling 11).
17. Test plan: "`release_refused`, nothing reset" also covers the claim-while-prompt-held case.
