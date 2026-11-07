# wt cleanup PR C: origin removal (as built)

Spec section: [PR C in the design](../specs/2026-11-04-wt-cleanup-design.md).
This note records the rulings taken while building it and where reality
differed from the spec's wording.

## Rulings

- **R1 origin only.** Rows of other remotes stay listed and locked; the engine
  refuses them. PR facts, the own repo and the base are origin's repository.
- **R2 proven only.** No `--accept-loss` or `-D` path for a remote: status must
  be proven with no blockers, and the engine refuses `Saw != proven`.
- **R3 four-way identity.** The listed tip, the fresh tracking ref, a fresh
  `git ls-remote` and the merged PR's `HeadRefOid` must be one commit.
- **R4 push form.** `git push --porcelain --no-verify
  --force-with-lease=refs/heads/<n>:<sha> origin :refs/heads/<n>`. Never bare
  `--force`/`-f`, a `+` refspec or a lease without a sha: a bare lease uses the
  tracking ref, which a background fetch may have advanced.
- **R5 no prompts.** `GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=never` through
  `cleanup.Git.Env`, applied to the fetch too; 120s limit.
- **R6 outcomes reused.** No new enum value and no envelope version bump: a
  remote item prints `kind: remote`, `worktree: not_requested` (or a gate's
  refused/skipped_changed) and the existing branch outcomes.
- **R7 own approval.** `Needs.Remote`: CLI `--remote`, TUI typed `delete
  origin`; nothing else grants it and it grants nothing else.
- **R8 tick list.** A proven origin row is tickable, never ticked for you, not
  by `a` either.
- **R9 fetch per remote.** `git fetch --prune <remote>` in a loop (honouring
  `skipFetchAll`) instead of `fetch --all`, so the header and `fetch.remotes`
  can say which remotes were fetched. `Fetch.OK` is origin's.
- **R10 blind spot closed twice.** An origin row is unproven unless origin was
  fetched cleanly this run, and a live `gh pr list --base <n> --state open`
  (no head-repo filter) must answer completely.
- **R11 rescan.** A remote target asks `gh` live (head and base, that branch
  only) and never fetches; `GatherInputs.Fetched` carries the first fetch
  result to a refresh.
- **R12 tighter PR rung.** A remote row is proven only with a merged PR into
  the default branch whose head is the tip and whose merge commit is in the
  base; otherwise `pr_head_moved` / `no_merged_pr` (less sure).
- **R13 verdict by reading.** A fresh ls-remote decides, never the push's exit
  status; `DeletedTip` is set before the push and kept while the ref is not
  proven present.
- **R14 dry run** performs every read and never pushes.
- **R15 names.** The engine refuses a remote name that is empty, starts with
  `-`, or holds `:`, whitespace, control characters, `..`, `*`, `?`, `[`...
- **R16 local branch.** Never touched; a warning names a local branch that
  tracked the deleted one.
- **R17 restore.** The reason ends with `git push origin <sha>:refs/heads/<n>`;
  a warning names GitHub's Restore branch button.

## Where reality differed from the spec

- The spec and `wt cleanup --help` promised a fetch with
  `GIT_TERMINAL_PROMPT=0`; nothing set it. Fixed.
- The spec says "fetch --all"; the build loops per remote (R9).
- The spec says "`--force-with-lease`" with no expected sha; a bare lease is
  unsafe (R4).
- The classifier accepted any merged PR for content already in the base; the
  engine requires the exact head, so the list showed PROVEN rows the engine
  would refuse (R12). Some `wt cleanup` output changes: origin rows whose tip
  moved after the merge are now less sure.
- "Rescan: no fetch, no gh" is now "no fetch; gh only for a remote target".
- The deletion scanner did not flag a `":refs/heads/"+name` refspec; its
  `deleteRefspec` now reads a runtime part anywhere after the colon.
- The push has only been run against local bare repositories. Before relying
  on it, delete one merged branch in a throwaway GitHub repository by hand.
