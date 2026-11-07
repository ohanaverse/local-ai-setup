# wt cleanup PR D: `wt rescue` (move local-only work to a new worktree)

Built from the cleanup design spec's PR D paragraph. Status: implemented in
the same change as this note; the rulings below record what was decided and
where reality disagreed with the spec.

## Shape

- A new copy-only engine `wt/internal/rescue` and a command
  `wt rescue <worktree-path-or-branch>` (`--to`, `--dry-run`, `--yes`,
  `--json`, `--no-fetch`, `--min-age`), plus `r` in the `wt cleanup` list.
- `internal/worktree.CreateAt` makes the branch and worktree at a full commit
  id; `CheckNew` is its read-only twin used by Preflight.
- Contract: `docs/contracts/rescue-result.schema.json` + samples, held by
  `cmd/wt/rescue_contract_test.go`.

## Rulings (recommended answers applied)

1. **Entry point**: a new subcommand `wt rescue` and the list's `r` key, not a
   flag on `wt remove` (which is named for deletion and says "removed").
2. **Carried**: the exact HEAD (commits), staged and unstaged tracked changes
   as binary full-index diffs applied with `git apply` (`--index` for staged),
   untracked non-ignored files copied with O_EXCL. Ignored files stay behind
   and are counted.
3. **Refused**: a file both staged and modified, a nested repository,
   submodule changes, intent-to-add, skip-worktree/assume-unchanged, unmerged
   paths, an operation in progress, special files, over 20000 files / 512 MiB.
4. **Eligible rows**: worktree rows with something to keep. Not the primary
   checkout (#522), not branch rows (`wt -W`), not remote rows. Current,
   locked and held worktrees are allowed.
5. **Naming**: `rescued/<branch>` or `rescued/<directory>`; `--to` is the full
   branch name; a taken name is a refusal naming `--to`, never a suffix.
6. **Approval**: y/N on a terminal, `--yes`, exit 3 without either; `--dry-run`
   needs neither. No `--accept-loss`/`--force`/`-D`: nothing is lost.
7. **Spec reality check**: the original does **not** become removable (see the
   amended spec paragraph). Follow-up: a "preserved by branch X" classifier
   signal with its own proof.
8. **Failure after creation keeps the destination** and reports it; no rollback
   (it would be a forced worktree removal of a directory holding carried work).
9. **Source untouched, testably**: optional locks off for every source read, a
   fingerprint (HEAD, refs, index bytes, status, file hashes) before and after,
   asserted in every refusal and failure test.
10. **#522 reuse**: the engine takes any worktree as source and a destination
    name; the "restore root" step #522 adds is not built or stubbed.
11. **Deletion scan**: `internal/rescue` and `cmd/wt/rescue*.go` are read by
    `TestDeletionStringsOnlyInRemovalPackage` and kept off its allow-list.
12. **Limits**: 20000 untracked files / 512 MiB, constants, no flag.
13. **TUI**: `r` quits the list and the rescue runs on the real terminal; the
    list is not refreshed in place.

## Found while building

- The classifier's `git status` ran without `GIT_OPTIONAL_LOCKS=0`, so listing
  could refresh a worktree's index stat cache, contradicting "writes no index".
  `cleanup.Git` now sets it for every call (`TestGitRunTurnsOptionalLocksOff`).
- `git status` never lists a fifo or socket, so the special-file refusal is a
  defensive check that git itself normally pre-empts.
- A worktree stopped mid-rebase reads as detached, so its branch name matches a
  branch row; the refusal says to name the worktree by path.
