# `wt rescue` leaves a record, so a rescued original can read removable — Design

**Status:** draft for the builder (2026-11-07). Issue #532. It is also the foundation #522 needs. The five product decisions below are the user's (the four first made, then the fidelity answer of 2026-11-07); every call the author made on top of them is marked **(my call)** and listed in "Calls to confirm". Reads with `2026-11-04-wt-cleanup-design.md` (PR D, rescue) and its plan `docs/superpowers/plans/2026-11-07-wt-cleanup-pr-d.md`.

All paths are relative to `wt/` unless they start with `docs/`.

## Intent

`wt rescue <worktree>` (#529) copies a worktree's local-only work to a new branch `rescued/<name>` in a new worktree. It deliberately leaves the original alone, and the original is not made removable. After a verified rescue, `wt cleanup` still lists the original as `unique_work`, and `wt remove` needs `--accept-loss --force -D` for it. The user rescued the work precisely so the original could go; today they must approve losing work that is, in fact, kept.

What the user wants from #532: after a verified rescue, the original reads removable, with the reason named ("preserved by ..."), and without approvals for losses that no longer exist.

Why it stays `unique_work` today (verified in the code):

- `unpushed` is `git rev-list --count <tip> --not --remotes` (`internal/cleanup/snapshot.go`). It counts remote-tracking refs only. `refs/heads/rescued/*` holds the commits and does not count.
- `dirty` (`internal/cleanup/worktrees.go`) is `git status --porcelain` non-empty, or any `ls-files -v` entry tagged `S` or lowercase. The rescue copies the dirt, so the original is still dirty.
- `judge.status()` (`internal/cleanup/classify.go`) gives `unique_work` for any unique reason, and `proven` needs a non-empty evidence from the merge-proof ladder. A rescued original has none.
- Nothing records that a rescue happened. `rescued/` is a naming convention and `--to` lets the user choose any name. The rescue's fingerprint (`p.fp`, `internal/rescue/state.go`) is unexported and dies with the process.

The hard part is the dirt. Uncommitted work exists only in an editable working directory. The user keeps editing the rescued copy (that is the point of rescuing), or deletes it. So "the destination still has this dirt" cannot be re-verified later. Only an immutable object held by a ref can make "preserved" true after the user touches the destination.

## Decisions (the user's)

1. **Build an immutable record in git under `refs/wt/rescued/*`** (the design called "Design 1" in the comparison). No sidecar file.
2. **A clean rescued original** (its commits held by the rescue, no dirt, no ignored files) becomes status `proven` with evidence `rescued`: default-ticked, removable with plain `--yes`. The meaning of `proven` widens to "every commit is held elsewhere or merged". The exit-prompt predicate is unchanged.
3. **A dirty rescued original whose dirt the snapshot covers** becomes `less_sure` and needs `--accept-loss` only. The engine passes git's `--force` internally after a pre-remove re-check. The user passes no `--force` and no `-D`.
4. **Record pruning is deferred.** Ship without it. Document `git update-ref -d refs/wt/rescued/<id>` for now. File a follow-up issue before the second PR merges.

## Goals

- A rescued original reads removable with the evidence named, and `wt remove` needs only the approvals that match what is really at stake.
- The claim "preserved" is about the source's content, so it survives the user editing or deleting the destination, `git gc`, and branch deletion.
- Every state change, missing object or read error between rescue and removal returns the row to exactly what it reads today.
- A record usable by #522 as "an immutable object holds this state and the source still equals it".

## Non-goals

- Pruning records (deferred, decision 4).
- A backfill command for originals rescued before this feature.
- Any change to the exit prompt (`internal/exitprompt`).
- The root-restore of #522 (see "How #522 builds on this").
- Treating a *destination* as preserved, or any local branch as a preserver (`rescued/x` rows keep being judged on their own).
- Capturing ignored files, exact permission bits beyond the exec bit, or empty directories.
- Any change to how origin rows, `Needs.Remote` or the merge ladder work.

## Anti-goals

The work is a failure, even if every test is green, if any of these can happen:

- Work that exists only in the original reads removable, in a way that a plain `--yes` (clean case) or `--accept-loss` (dirt case) removes it. The sequences in "Holes found and closed" are the known routes; the builder must treat a new route as a blocker.
- A row reads preserved because a record exists, without the source's current commits, index tree and working-tree tree being what the record holds.
- A read error, a missing object or an unparsable record makes a row better than today. Unknown must stay Unknown.
- A destination worktree or branch inherits the signal and is removed with its dirt.
- The word `proven` starts to mean something the exit prompt, the tick list or a script cannot tell apart from "merged" in a way that offers the wrong action. (The exit prompt stays on `same-commit`/`ancestor`; a script sees the evidence string `rescued`.)
- A new deletion path outside `internal/removal`.

## Constraints

- **Fail closed.** Any git error, unreadable ref, mismatch or ambiguity means "not preserved", which is today's behaviour.
- **`internal/removal` is the only deletion code.** `TestDeletionStringsOnlyInRemovalPackage` scans every non-test file under `wt/` for git deletion shapes and file deletes; `fileOpNever` (`internal/removal/deletionscan_test.go`) bans any file delete or rename in `internal/cleanup/`, `internal/rescue/`, `cmd/wt/rescue*.go` and `cmd/wt/cleanup*.go`, with no allow-list entry possible. So the snapshot must be built without a temp index file. The new package must hold no deletion either.
- **Frozen wire strings.** No existing status, evidence, reason, blocker, outcome, approval or rescue code is renamed. Additions are additive; each schema (`additionalProperties: false`, enums) and every sample move in the same commit as the code (`docs/contracts/cleanup-list.*`, `docs/contracts/rescue-result.*`), held by `cmd/wt/cleanup_contract_test.go` and `cmd/wt/rescue_contract_test.go`. Versions stay 1.
- **Contract fixtures move with code.** `TestEveryCodeHasWording` needs `reasonWording` for a new reason; `evidenceText` needs the new evidence.
- **No sidecar files.** The record is git objects plus one ref.
- **The ref lives outside `refs/heads` and `refs/remotes`**, so branch rows, `unpushed` counts and `for-each-ref` over those namespaces do not move. It is not fetched or cloned by default.
- **`Classify` stays pure.** New facts enter through `Snapshot`, in both `Gather` and `Rescan`. Every git call goes through `cleanup.Git` (`GIT_OPTIONAL_LOCKS=0`).
- **The source is only read by rescue.** Creating objects and one ref in the shared repo is allowed (rescue already creates a branch); the source's HEAD, branch ref, index content, status and file bytes must be unchanged.
- **Approvals stay per item and sealed.** Hard blockers (`primary`, `current`, `held`, `locked`, `nested`, `operation_in_progress`, `protected_artifact`, `default_branch`), `GatesUnknown` and the submodule refusal under `--force` are not weakened.
- **Tests:** every `Test*` has a top-level comment saying what and why; a new package that can run git needs `gitenv.IsolateForTest()` in its `TestMain` (`TestEveryPackageThatRunsGitIsolatesIt`); scratch repos per case (`wt/CLAUDE.md` "Go tests").

## The design

### The record

At the end of a verified rescue, `internal/rescue` writes one ref pointing at a two-commit chain built on the source's HEAD.

- **Ref:** `refs/wt/rescued/<first 16 hex of W's sha>` (**my call:** 16 hex), created create-only with `git update-ref <ref> <W> ""`. The name carries no meaning beyond uniqueness; binding is in the trailers, so `--to` and the `rescued/` prefix do not matter. If the ref already exists and points at the same commit, that counts as written. If it exists and points elsewhere, the write fails (see failure behavior).
- **Commit I:** tree = the source's index tree (`git write-tree` run in the source), parent = the source's HEAD.
- **Commit W:** tree = the source's working-tree tree (HEAD plus staged, unstaged and non-ignored untracked content), parent = I.
- **Identity:** author and committer come from `GIT_AUTHOR_*`/`GIT_COMMITTER_*` environment variables, not from the user's config (which may be unset) (**my call:** fixed name `wt rescue`, fixed address `wt@invalid`, the real clock).
- **Trailers** in W's message (and I's), bound to the source: `Wt-Source-Path` (symlink-resolved absolute path, `cleanup.ResolvePath`), `Wt-Source-Ref` (full refname, or `detached`), `Wt-Source-Head` (sha), `Wt-Dest-Branch`, `Wt-Dest-Path`, `Wt-Version: 1`. A path or ref containing a control character cannot be written as a trailer; then no record is written (**my call**; failure behavior applies).
- **Safety does not rest on the trailers.** The real property is content equality plus ref reachability. The trailers give the display text and a cheap early-out: with no record naming a worktree's path, nothing is hashed.
- **Write-after-verify.** The record is written only after `verify()` passed and the source fingerprint was equal before and after the copy (`internal/rescue/execute.go`, just before `o.Status = StatusMoved`). Never before: a record for a copy that failed verification would be a false claim. Before writing, rescue retakes the fingerprint once more and writes nothing if it differs (**my call**; the record would still be a true snapshot of the source, but the copy and the record should describe the same state).
- **Create-only, never rewritten.** Nothing in this work updates or deletes a record ref.

What survives: anything reachable from a ref survives `git gc`. The recon measured this on a throwaway repo: branch deleted, all reflogs expired, `git gc --prune=now`, and the record, its parent and the untracked file contents were still readable. `for-each-ref --contains` over `refs/heads` and `refs/remotes` then returned no holder, which is why the record ref must be counted separately from branches.

### `internal/dirtsnap`

A new package that builds the working-tree tree id without a temp file and without deleting anything.

- **Input:** a repo directory and a runner. To avoid an import cycle (`internal/cleanup` will import `dirtsnap`), the runner is a function type, not `cleanup.Git`: `func(stdin []byte, args ...string) ([]byte, error)` (**my call**). Callers wrap `cleanup.Git` (so `GIT_OPTIONAL_LOCKS=0` and `core.fsmonitor=false` hold).
- **Algorithm:** start from HEAD's tree. For each path in `git status --porcelain=v1 -z --untracked-files=all --no-renames`, take the current on-disk state: file bytes (blob via `git hash-object --no-filters`, mode `100644` or `100755`), symlink target (`120000`), or absent (entry removed). Rebuild only the trees along touched paths, reusing sibling entries from `ls-tree`, emit raw tree bytes in git sort order (a directory sorts as its name plus `/`), and hash with `git hash-object -t tree --stdin`.
- **Two modes.** `Build(write=true)` adds `-w` to every `hash-object` (rescue's write path; the objects must exist for the commit). `Build(write=false)` hashes only and writes nothing (cleanup's read path).
- **Tree-mode trap.** `git ls-tree` prints `040000` for a tree; a raw tree entry needs `40000`. This bit the recon's first run.
- **Oracle test.** Every dirtsnap unit test compares its tree id with the one from a temp index in the test: `GIT_INDEX_FILE=<tmp> git add -A && git write-tree`. The recon's experiment found them byte-identical on a repo with a staged change, an unstaged change, untracked files in new and nested directories, a dotfile and a `0600` file. Test files may create temp files (the deletion scan covers non-test files only). Cover: nested new directories, a file replaced by a directory and the reverse, deletes (tracked file removed from disk), symlinks, the exec bit, names that sort around `/` (`a`, `a.b`, `a/`), a path with spaces and non-ASCII bytes.
- **A dirtsnap bug fails closed**, because a wrong tree means a mismatch means "not preserved". A false match would need a hash collision.
- **Size cap:** the read path gives up (not preserved) beyond rescue's own limits, 20000 files or 512 MiB (`MaxUntrackedFiles`, `MaxUntrackedBytes` in `internal/rescue/types.go`) (**my call**). It cannot be slower than the rescue that made the record.
- Tests need `TestMain` with `gitenv.IsolateForTest()`.

### The classifier rule

A source row is a worktree row (on a branch, or detached). It gets `Row.Preserved` (a pointer, nil when not preserved) with `{Ref, Commit, DestBranch, DestPath, CoversDirt}`.

Candidate record: a ref under `refs/wt/rescued/` whose W commit is readable and whose trailers `Wt-Source-Path`, `Wt-Source-Ref` and `Wt-Source-Head` equal the row's resolved path, its `Row.Ref` (or `detached`) and its HEAD/tip. Records are listed once per `Gather` and once per `Rescan` with `for-each-ref --format='%(refname) %(objectname) %(contents:trailers:only,unfold,valueonly)' refs/wt/rescued/`.

**Commits are covered** when, for a candidate record, `W` is reachable from the record ref, `cat-file -e` finds I and W, and `git rev-list --count <tip> --not --remotes <recordref>` is 0 (for a detached row, `HEAD` instead of `<tip>`). Then:

- A worktree on a branch that is otherwise clean: `Row.Preserved` is set with `CoversDirt` false. No tree comparison is needed, because nothing but commits is at stake (**my call**; a source that is clean today and whose record had dirt is still safe to remove, since the commits are the only thing it holds).
- If the source has dirt, `CoversDirt` is also required, and is true only when **all** hold:
  1. `git write-tree` in the source equals I's tree (so the staged state is the recorded one);
  2. `dirtsnap.Build(write=false)` of the source's current state equals W's tree;
  3. none of the states below, which are never preserved.

Equality with any one candidate suffices; because a record is immutable, one equal record proves the claim and two candidates that disagree do not need a tie-break (**my call**, simpler than the comparison's "ambiguous means not preserved").

**States never preserved** (the whole row reads as today, `Preserved` nil): any `ls-files -v` entry tagged `S` or lowercase (hidden index flags: skip-worktree, assume-unchanged); an intent-to-add entry; a submodule in the status output (`--ignore-submodules=none`) or a nested repository; unmerged paths (also a `git write-tree` failure); an operation in progress (merge, rebase, cherry-pick, bisect, revert); unreadable gates (`GatesErr`). These are rescue's own refusals, and a snapshot of such a state does not describe it.

**When `Preserved` is set**, the row changes as follows.

- **Raw `Losses` are kept** exactly as the classifier computes them today. A new method `Row.EffectiveLosses() Losses` returns them with the covered parts cleared: `Unpushed` becomes 0 and `DetachedUnique` false (commits covered); `Dirty` false only when `CoversDirt`. `IgnoredEntries`, `IgnoredBytes` and `GatesUnknown` are never cleared.
- The unique reasons `unpushed`, `dirty` and `detached_unique` are not raised for the covered parts.
- **New reason `preserved`**, text "preserved by refs/wt/rescued/<id> (copy: <dest>)". It is informational (neither soft nor unknown) when `CoversDirt` is false, so a clean original can be `proven`. It is **soft** when `CoversDirt` is true (**my call**, the mechanism that realises decision 3: a soft reason caps status at `less_sure`, so `needsOf` sees `Confirm` from the status, and no Force follows because the effective `Dirty` is false).
- **New evidence `rescued`**, set only when the row has no stronger ladder evidence already. A row that already has `same-commit` or `ancestor` keeps it. Evidence being non-empty is what lets `judge.status()` reach `proven`.
- Status follows the existing precedence, unchanged: blockers give `in_use`; any other unique reason gives `unique_work`; unknowns (including `gates_unavailable`, `unpushed_unknown`) or soft reasons (including `ignored_files` and `base_is_local`) give `less_sure`; otherwise `proven`. So: a clean preserved original with no ignored files and nothing unknown is `proven`, evidence `rescued`; a preserved original with ignored files (`.venv`, `.env`: rescue never carries them) is `less_sure` with a loss line; with dirt covered it is `less_sure`; a local-default fallback (`capFallback`) still caps it at `less_sure`.
- `rescued` evidence is not subject to the min-age floor, which exists only for `same-commit`/`ancestor` (a fresh branch reads as ancestor-merged) (**my call**).
- **`BranchProven`** (Ruling A) becomes true for the worktree row's own branch when `Preserved` is set, `Row.Ref` is a branch and the tip equals the record's `Wt-Source-Head`. The worktree item then deletes its own branch unasked, as a merged one is. Branch rows that have no worktree never get `Preserved` in this work, because their path is not a live worktree (**my call**); a stand-alone branch left behind reads as it does today.
- **`tipHeldElsewhere`** (`internal/removal/branch.go`) counts `refs/wt/rescued/` as a holder (`for-each-ref --count=2 --contains <tip>` over `refs/heads/`, `refs/remotes/` and `refs/wt/rescued/`; the deleted branch's own `origin/<name>` is still ignored). The gc caveat is then suppressed correctly, because the record ref keeps the tip.
- **`knownNotUnknown`** (`internal/removal/branch.go`) gets `cleanup.ReasonPreserved`. Without it the branch stage reads the code as an Unknown and keeps the branch.
- **Scope:** the signal applies to the source row and its own branch only. It is never set for the rescue destination (its path differs from every `Wt-Source-Path`; the destination is judged on its own dirt and commits).

### The effective view: every Losses read site

The comparison found that zeroing `Losses` corrupts raw facts that rescue's `Preflight` and `Offered` read, and that keeping them raw needs every other consumer to switch. The decision is: raw kept, one `EffectiveLosses` helper, every non-rescue consumer calls it. Read sites verified in the repo today:

| Site | File | Reads |
|---|---|---|
| `needsOf`: `Confirm` via `LossLines`, `Force` via `Dirty`/`DetachedUnique` | `internal/removal/engine.go` | effective |
| `unapproved`: the `Force` messages | `internal/removal/engine.go` | effective |
| `lossesWorse(fresh, saw)` (worktree gate and the branch stage) | `internal/removal/engine.go` | effective, both sides |
| the seal: `ItemFromRow` `SawLosses`, and the copy at `preview.go` ~205 | `internal/removal/preview.go`, `types.go` | effective |
| `Tickable` | `internal/cleanup/present.go` | status-based; stays so, and tests assert it follows effective losses |
| `LossLines` | `internal/cleanup/present.go` | effective |
| `GatesUnknown` refusal (step 6) | `internal/removal/engine.go` | raw (never cleared, identical either way) |
| `gitForce = fresh.Losses.Dirty` | `internal/removal/engine.go` | **raw, deliberately**: git refuses a dirty tree without `--force`, so the engine passes it whenever the tree is dirty, covered or not |
| rescue `Preflight`, `Offered`, the recount check | `internal/rescue/preflight.go`, `offer.go` | **raw, deliberately** |
| exit prompt `Eligible` (`f.Losses != (cleanup.Losses{})`) | `internal/exitprompt/exitprompt.go` | **raw, deliberately**: a preserved row is never offered at exit |

**Guard test (required, in `internal/cleanup`):** a `go/ast` scan of non-test files in `cmd/wt` and `internal/` that lists every selector use of `.Losses` on a `cleanup.Row` (and every `SawLosses`), and compares it with an explicit allow-list of file+function names labelled `raw` or `effective`. A new read site fails the test until someone decides which it is. This is the control against a consumer forgetting the view. Companion tests: for each effective site, a table test with a preserved row.

The seal holds the effective losses. If preservation lapses between preview and run, the fresh effective losses equal the raw ones, `lossesWorse` fires (newly `Dirty`, more `Unpushed`, newly `DetachedUnique`) and the item is `skipped_changed`; the status also drops (`unique_work` is worse than `Saw`). No engine change is needed for that, but tests must prove it for each lapse (see the PR plan).

### The engine

- **Clean original (PR B1).** `needsOf` is empty for a `proven` row with no loss lines, so plain `--yes` removes it. `gitForce` is false because raw `Dirty` is false.
- **Dirt covered (PR B2).** The row is `less_sure`, so `Needs{Confirm}` only. `gitForce` stays raw `Dirty`, so the engine passes `git worktree remove --force` internally. Before it does, the worktree gate re-runs the preservation check immediately before the git call (a second `cleanup.Rescan` of just that row, comparing the fresh `Preserved` and effective losses with what the gate had accepted). A mismatch gives `skipped_changed` and nothing is removed (**my call:** implement it as a second `Rescan`, inside the existing gate in `internal/removal/engine.go`). The submodule refusal under `--force` (Ruling X) still applies.
- **Residual risk (declared, not closed).** A write between the final re-check and `git worktree remove --force` is destroyed. This window is milliseconds; today's approved dirty removal has the same window, only larger. The human approval in decision 3 is the control.
- **Pruning stays out.** Nothing deletes a record ref.

## Safety argument

"Preserved" means: every commit and every byte of tracked change and untracked non-ignored content of this source is held by a git ref, and the source still is exactly that state. It rests on content equality and the immutability of git objects, not on the destination.

### Holes found and closed

Each: the sequence that would have made it fail, and how it is closed.

1. **Destination edited, deleted or reset afterwards.** A design that re-checked "the copy still equals the source" would break on the user's first edit, or trust a deleted copy. Closed: the record snapshots the source, the destination is only a convenience, and its path never matches `Wt-Source-Path`.
2. **Source changed after the rescue.** User rescues, then edits the original, then removes it with plain `--yes`. Closed: the recomputed index tree or working-tree tree differs from the record, `Preserved` is not set, raw losses return and the row is `unique_work` again. For commits: a new commit on the source moves the tip off `Wt-Source-Head`, so no record matches.
3. **Change in the Rescan-to-remove window.** Preview says preserved, the user edits, `Run` removes. Closed: the engine rescans each item before acting; lapse gives `skipped_changed` (seal and status checks above). The last milliseconds before `git worktree remove --force` are the declared residual risk, narrowed by the pre-remove re-check in B2.
4. **Force window (judge's finding).** With dirt zeroed, a plain `--yes` would run `--force` over a dirty tree with no human involved. Closed: dirt coverage reads `less_sure`, never `proven`, so it always needs `--accept-loss` (decision 3). For a clean original `--force` is never passed (raw `Dirty` false), and git itself then refuses a tree dirtied in the window.
5. **Branch force-moved, reset or recreated.** The tip must equal `Wt-Source-Head`, and path and ref must match. A branch recreated under the same name at another commit does not match. Commits added after the rescue are not reachable from the record ref, so they stay counted as unpushed.
6. **Destination `rescued/x` row read removable (judge's finding).** The first sketch let the destination branch take the signal, which would delete the user's visible copy while a worktree is probably on it. Closed: the signal applies to the source row and its own branch only; the destination's dirt is its own.
7. **Dirt the snapshot cannot represent (judge's finding).** Hidden index flags (skip-worktree, assume-unchanged), submodules, nested repositories, unmerged paths and an operation in progress are not captured by a tree. Closed: these states are never preserved (rule above); they match rescue's own refusals.
8. **Declared losses.** A git tree stores only `100644`, `100755` and `120000`. Exact permission bits beyond the exec bit (a `0600` untracked file reads back as `100644`; the recon measured it), empty directories, and ignored files are not in the record. Ignored files keep the `ignored_files` soft reason and loss line, so the row is capped at `less_sure` and `--accept-loss` shows the count and size. Modes and empty directories are a small declared loss: the rescued destination keeps them (rescue copies modes and creates the directories), and the docs must say that if the destination is also gone, they are gone. **Decided by the user (2026-11-07): accept and document.** No loss line names these files at confirmation, and a source holding them is still preserved.
9. **Record written before verification.** A crash or a failed verify would leave a record for a copy that never matched. Closed: write-after-verify only; a crash between `moved` and the write leaves a correct copy and no signal (harmless).
10. **gc prunes the snapshot.** A bare hash key with no ref would claim preservation while gc deletes the objects. Closed: the ref holds both commits; measured.
11. **A second rescue of the same source.** Makes a second record. Either can prove the state; equality with one immutable record is enough. A rescue after the state changed makes a record for the new state, and the old record no longer matches the changed source.
12. **Forged or stale records.** A record is a ref; anyone who can write refs can already delete everything, so forgery is outside the threat model. A record whose trailers do not parse, or whose objects are missing, is ignored (`cat-file -e`).
13. **Path recycling, `git worktree move`, copied repo, case-insensitive volume.** The record binds path, ref and head. A recycled path with another head or ref does not match. A moved worktree or a copied repo matches nothing (fail closed, the user can rescue again). Paths go through `cleanup.ResolvePath`; refs compare by exact name.
14. **Mutual preservation** (two sibling branches at one commit each "preserving" the other, a design the comparison rejected). Closed: the preserver is a `refs/wt` ref, never a branch row, so it can never be removed in the same run and two branches cannot preserve each other.
15. **A consumer forgets the effective view.** Fail-open in the wrong direction. Closed: the guard test enumerating every `Losses` read site, with an explicit raw/effective label.
16. **The branch stage keeps or deletes wrongly.** A new reason code outside `knownNotUnknown` makes the branch stage treat it as Unknown and keep the branch. Closed: the code is added there; without `BranchProven` the worktree would go and the branch would remain `retained_unmerged`. A test per outcome.
17. **Exit prompt starts offering rescued originals.** Closed: its predicate is evidence `same-commit`/`ancestor`, and it reads raw losses (non-zero for a preserved row); unchanged and tested.
18. **Re-rescue refused.** If `Losses` were zeroed, `nothing_to_keep` would refuse a second rescue. Closed: rescue reads raw losses, which stay.
19. **Hash cost.** `Gather` would hash every dirty worktree. Closed: no record naming the path means nothing is hashed; the size cap falls back to "not preserved".

## Failure behavior

- **Record write fails** (update-ref, hash-object, commit-tree or write-tree error; ref collision; a path or ref that cannot be a trailer; the last fingerprint differs): the rescue stays `moved`, the destination is kept, and the output carries a warning. No signal is written, so the original stays `unique_work` (fail closed). The copy is correct and verified, so reporting `failed` would mislead.
- **Wire form.** The recon assumed rescue warnings have codes. They do not: `Outcome.Plan.Warnings` is `[]string` free text (`internal/rescue/types.go`, `json.go`). So the stable `record_not_written` is the fixed prefix of one warning line (`record_not_written: <sanitised reason>`), pinned by a test and described in the schema's `warnings` description (**my call**). The `code` enum (refusal and failure codes) does not change.
- **Success form.** `rescue-result` gets an additive optional `record` object `{ref, commit}` (the record ref and W's full commit id), present when written, absent on failure and on `would_move`. `EnvelopeVersion` stays 1; `docs/contracts/rescue-result.schema.json` and its samples (`rescue-result.sample.json`, and any other sample with a `moved` outcome) move in the same change. The text output prints one line naming the ref.
- **Unreadable record or ref in cleanup:** not preserved. A failing `for-each-ref` listing leaves every row as today.
- **Ordering of refusals** does not change: every rescue refusal still comes before anything exists.

## Contracts and docs that change

- `docs/contracts/cleanup-list.schema.json`: `rescued` added to the `row.evidence` enum, `preserved` to the `reasons[].code` enum; `cleanup-list.sample.json` gains a preserved row (the `.fetch-failed` and `.no-origin` samples only if they carry the enums). Purely additive, version 1. `RowJSON` (`internal/cleanup/json.go`) is built field by field and does not serialise `Losses`, so `TestCleanupJSONUnchangedByNewRowFields` pins that nothing else appears; do not add a `preserved_by` field now (**my call**; the reason text carries the ref).
- `docs/contracts/rescue-result.schema.json` and samples: the `record` object and the `warnings` description. Its top-level description today says "`moved` does NOT make the source removable"; that sentence is rewritten (the source may read removable in `wt cleanup`).
- `docs/contracts/remove-result.*`: no wire change. Approval strings, outcomes unchanged.
- `internal/cleanup/present.go`: `reasonWording` for `preserved`, `evidenceText` for `rescued`; `internal/removal/branch.go`: `knownNotUnknown`, `tipHeldElsewhere`.
- Docs to update in the PR that changes the behavior: `wt/docs/wt-cleanup.md` ("The original stays as it was, and is not made removable", the evidence and reason tables, the approvals, the declared losses, the manual record removal command); `wt/CLAUDE.md` (Rescue invariants bullet 1, the "Still to do" bullet that names this signal, the read-only sentence of the cleanup section, which must say `write-tree` may leave unreachable tree objects like `merge-tree --write-tree`); root `CLAUDE.md` (Commands/Architecture lines for rescue and cleanup); `wt/CHANGELOG.md`; `docs/guides/08-maintenance-and-troubleshooting.md` §9; `docs/internals/packages.md` and `testing.md` for `dirtsnap` and the guard test; the "Amended" paragraph of `docs/superpowers/specs/2026-11-04-wt-cleanup-design.md`; `cmd/wt/rescue.go` help ("not made removable either") and its success text (~line 305). The cleanup-read-only claim: `git write-tree` in the source writes tree objects only (no ref, index or working file).
- PR bodies use `Refs #532` and `Refs #522`, never a closing keyword.

## Staged PR plan

### PR A: inert (record plus `dirtsnap` plus the rescue contract)

No classifier or removal change. Every `wt cleanup` and `wt remove` output is identical to today.

- `internal/dirtsnap` with the oracle tests.
- `internal/rescue/execute.go`: write the record after verify; `types.go`/`json.go`: `Outcome` carries the record, the envelope's `record` field and the `record_not_written` warning; `cmd/wt/rescue.go`: one text line.
- `docs/contracts/rescue-result.*` and samples; `rescue_contract_test.go`.
- Proves: the tree id equals the temp-index oracle on every case above; a rescue of a clean, a staged, an unstaged, an untracked-nested and a detached source writes a record whose commits are reachable and whose trees equal the oracle's; the source fingerprint is unchanged (existing "every refusal test asserts the source is unchanged" pattern); no record exists after any refusal or failure; a record-write failure (a seam that makes `update-ref` fail) leaves `moved` and the warning; the branch survives `git branch -D`, reflog expiry and `git gc --prune=now` with the record intact; `TestDeletionStringsOnlyInRemovalPackage` passes with no new allow-list entry.

### PR B1: clean originals (commits only) become `proven`

- Record listing in `Gather` and `Rescan` (both), the commits-covered rule, `Row.Preserved`, `Row.EffectiveLosses`, evidence `rescued`, reason `preserved` (informational, `CoversDirt` false), `BranchProven`, `tipHeldElsewhere`, `knownNotUnknown`.
- Switch every site in the table above; the guard test.
- `cleanup-list` schema and sample, wording, docs. Dirt is not covered here: a dirty source stays as today.
- Proves, end to end in a throwaway repo (rescue, then classify, then remove): a clean rescued original is `proven`, `rescued`, ticked, and removed by plain `--yes` with its branch; one new commit after the rescue returns `unique_work`; a changed tip, a deleted record ref, a record with a missing object, a different path, a different ref all return today's row; the destination row is untouched; ignored files give `less_sure`; a lapse between preview and run (edit, commit, record deleted, `Rescan` failing) gives `skipped_changed`; the exit prompt does not offer it; `nothing_to_keep` still refuses nothing wrongly (a second rescue works); the branch is deleted unasked and no gc caveat is printed; JSON contract and `TestEveryCodeHasWording`.

### PR B2: dirt covered, pre-remove re-check, internal `--force`

- Dirt rule (index tree equality, `dirtsnap` equality, the never-preserved states), `CoversDirt`, reason `preserved` soft in that case, the pre-remove re-check in the worktree gate, `gitForce` on raw `Dirty`, the less_sure approval.
- Proves: a dirty rescued original is `less_sure`, needs `--accept-loss` only, and is removed with no `--force` and no `-D` from the user; the destination's later edits or deletion change nothing; any edit to the source (tracked, untracked, mode flip of the exec bit, staging change) returns `unique_work` with `Force` needed; hidden flags, a submodule, an unmerged path and an operation in progress are each not preserved; the pre-remove re-check catches an edit made after the gate (seam like `afterRescueConfirm` in `cmd/wt`) with `skipped_changed` and the tree untouched; the submodule refusal under `--force` still fires; `0600` and empty-directory behavior is documented and tested as the declared loss.

### Follow-up issue (decision 4)

File the pruning issue before B1 merges (B1 is the second PR, the first to make originals removable) (**my call:** reading "second PR" as B1). Its content: list records in `wt cleanup`, and delete them through an `internal/removal` operation (`update-ref --no-deref -d <ref> <commit>`, compare-and-swap). Until then the docs give `git update-ref -d refs/wt/rescued/<id>`.

## How #522 builds on it

#522 moves a non-default root branch into a worktree and restores the root. Its destructive step discards the root's dirt and switches the root to the default branch. That needs exactly what the record gives: "an immutable object holds this state, and the source still equals it". The rescue engine already takes any worktree as a source; the preflight refuses the primary (`CodePrimary`) and that stays for now.

Not part of this work. For #522: a new `internal/removal` operation kind (not a flag; the engine refuses the primary and current worktree today), which requires a valid record for the root, re-checks it at the destructive step, refuses while another live session claims the root, and refuses on an operation in progress, submodules and unmerged paths. Record this requirement in the #522 issue (**my call**: in the issue only; the root-restore gets its own design pass). The record format is designed to serve it: it binds path, ref and head, and its match key does not depend on the destination.

## Backward compatibility

- Originals rescued before this feature have no record, so they read as they do today (`unique_work`, `--accept-loss --force -D`). No backfill command: it would need an unverifiable claim about an old destination's state.
- A second rescue of such an original works, and now leaves a record. The second rescue works also for a preserved one, because `nothing_to_keep` reads raw losses, which stay.
- `rescue.Offered` still offers `r` on a preserved row (raw losses). Accepted: it just makes another copy and record.
- `EnvelopeVersion` and `cleanup-list` version stay 1. A script matching on `unique_work` sees rescued originals move to `proven`/`less_sure`; the evidence string `rescued` and the reason `preserved` tell why.
- `git clone`, `fetch` and `git gc` do not carry or collect `refs/wt`; a clone has no records.

## Left to the builder

- Exact Go names and signatures of `Preserved`, `EffectiveLosses`, the record reader and the `dirtsnap` API.
- The text of the `preserved` reason, the success line of `wt rescue`, the wording in `wt-cleanup.md`, and how the TUI row and confirmation show the ref.
- Whether `Gather` lists records once per run (recommended) and how `Rescan` shares the listing with `prior`.
- The trailer parser (unfolded values, rejecting duplicates) and the test seams (a failing `update-ref`, an edit between the gate and the git call).
- How the guard test identifies a `cleanup.Row` selector (type-check with `go/types`, or a name list).
- The pruning follow-up issue's text, and the line for #522.

## Calls to confirm

The marked calls above, gathered. All are implementation-level; the user's fidelity answer (accept and document) closed the one that needed an owner.

1. (Closed.) The fidelity loss for modes beyond the exec bit and empty directories (hole 8) is accepted and documented: the user's decision, not a call.
2. Record id is 16 hex of W; an existing ref at the same commit counts as written.
3. Fixed author identity through environment variables, real clock.
4. No record when the path or ref has a control character; retake the fingerprint before writing.
5. `dirtsnap` takes a function runner (import cycle); size cap equals rescue's limits.
6. Commits-only coverage needs no tree equality for a clean source (test: a clean source whose record also held dirt is still covered, the record being a superset); coverage also requires `Wt-Source-Head` to equal the row's tip (reused by `BranchProven`); one equal record suffices, candidates are tried in turn and an unparsable or stale one is ignored, no ambiguity rule.
7. `preserved` is informational when only commits are covered and soft when dirt is covered.
8. `rescued` evidence is not age-gated; a stronger existing evidence is kept.
9. Branch rows without a worktree never get `Preserved`; the branch goes with the worktree item.
10. `gitForce` stays on raw `Dirty`; the seal holds effective losses; the pre-remove re-check is a second `Rescan`.
11. `record_not_written` is a warning-line prefix, not an enum code (rescue warnings are free text); `record` is an additive object.
12. No `preserved_by` field in `cleanup --json`.
13. The follow-up issue is filed before B1 (read as "the second PR"); the #522 requirement is recorded in the issue only.
14. The PR split A, B1, B2 (the comparison's recommendation, adopted).
15. Settled during the builder check, all minor: the B2 pre-remove re-check runs only for rows with `Preserved` and `CoversDirt`; dirtsnap's size cap counts only the paths it would hash (changed and untracked); branch refs are stored as full refnames so `detached` cannot collide with a branch; a record on a primary or locked worktree still yields the hard blocker (`in_use`) with one test; one text line names the ref in `wt rescue` and the reason text shows in the TUI confirmation; the implementer drafts the pruning issue in the B1 PR checklist and the owner (or the assistant on request) files it.

## Amendments (2026-11-08)

Settled while building PR B1:

- Commit I's tree is built by `dirtsnap.IndexTree` from `ls-files --stage -z`, not by `git write-tree` (which rewrites the source's raw index file). The B2 note about `write-tree` leaving unreachable objects is therefore moot.
- `Preserved` is applied only when the row's own commits are the uncovered part: no ladder evidence, raw `Unpushed > 0` for a branch or `DetachedUnique` for a detached HEAD, the fresh tip equals `Wt-Source-Head`, no dirt and no gates error.
- PR safety checks (`open_pr`, `pr_unknown`, `pr_head_moved`) still apply to a preserved row.
- A `Rescan` error is `refused`, not `skipped_changed`, so a record-listing failure degrades to "no records" instead of returning an error; the lapse tests expect `skipped_changed` via a dropped status.
- The informational reason (`preserved`) needs a fifth judge slot (`info`): it is in neither the unique, unknown nor soft lists.
- `EffectiveLosses` clears only `Unpushed > 0` (and `DetachedUnique`).
- `Preserved` carries `SourceHead`.
- A record whose ref is not `refs/wt/rescued/<first 16 hex of its commit>` is ignored.
- The Losses read-site guard is a name-keyed `go/ast` scan in `internal/cleanup` (no `go/types`).
