# `wt cleanup` PR B — local removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user remove the worktrees and local branches they ticked in `wt cleanup` (and name on the command line with `wt remove`), re-checking every item immediately before acting, and never removing anything the classifier would not have offered.

**Architecture:** Deletion lives in a new package `wt/internal/removal` that imports the read-only classifier `wt/internal/cleanup`; `internal/cleanup` stays free of deletion code. The classifier gains a scoped `Rescan` so each item is re-judged right before it is touched. `wt remove` and the list's `enter` flow call the same `removal.Engine`. Worktrees go with plain `git worktree remove`; branches with a compare-and-swap `git update-ref -d <ref> <sha>`.

**Tech Stack:** Go 1.26 (module root `wt/`), git plumbing, bubbletea (existing TUI), cobra, JSON Schema + sample fixture in `docs/contracts/`.

**Spec:** `docs/superpowers/specs/2026-11-04-wt-cleanup-design.md` (PR B section, Rulings 14/17 and the "Before wt cleanup deletes anything" list in `wt/CLAUDE.md`). Carried values: `docs/superpowers/specs/2026-11-04-worktrunk-ports-design.md` (force scopes, `-D` matrix, `branch_outcome`, registered-artifact refusal). Prior work: `docs/superpowers/plans/2026-11-04-wt-cleanup-pr-a.md`.

## Calls this plan makes beyond the approved design (review these first)

1. **No trash-rename or background drain in PR B.** The design carries worktrunk's "rename to trash, delete in a detached drain". Research shows it is the most trap-heavy part (detached process, flock, mount boundaries, symlinks, cross-device, a crash-recovery path) and it conflicts with the design's "plain `git worktree remove`". PR B uses plain, synchronous `git worktree remove` (large trees take a while; the list shows progress). The drain is left for a later PR if it proves needed. *Cost: removing a worktree with a huge `.venv`/`node_modules` blocks until done.*
2. **`--force` means exactly one thing:** pass `--force` to `git worktree remove` for a dirty worktree, only after the user approved the stated loss. It never overrides a hard blocker (primary, current, held, locked, nested, operation in progress, protected artifact, default branch). `-D` means exactly one thing: delete an unmerged branch.
3. **`branch_outcome` enum is pinned here:** `deleted`, `not_attempted`, `retained_unmerged`, `retained_moved`, `retained_checked_out`, `retained_default`, `retained_unknown`. (`deferred` and `retained_capped` from the ports spec are dropped: PR B deletes, and the ladder has no 500-commit "proof" walk.) Worktree outcomes: `removed`, `skipped_changed`, `refused`, `failed`, `not_requested`.
4. **Non-interactive approval:** `--yes` approves proven rows only. Removing a less-sure or unique-work row needs the matching explicit flag (`--force` for dirty, `-D` for unmerged) **and** `--yes`; without `--yes` and without a terminal the command exits 3 and removes nothing.
5. **Exit codes for `wt remove`:** 0 everything requested was removed (a retained branch under the `-d` rule is not a failure); 1 could not start (usage, no default branch, registry failed to load); 2 some items were skipped, refused or failed; 3 approval missing. The JSON result is printed first, always.
6. **Remote rows are never removable in PR B** (PR C owns `origin`). They stay `[-]` in the list.
7. **Registry load failure refuses removal** (artifact protection cannot be trusted), and `local_path` protection is fixed per the carry list (home expansion with `config.ExpandHome`, relative paths resolved against the repo root, a malformed `local_path` fails closed).
8. **`--min-age` gets a floor of 1 minute**; a value below the 10-minute default shows a header note.
9. **Tests duplicate a slim git fixture** inside `internal/removal` rather than extracting a shared package from PR A's tests (extraction is a later refactor).

## Global Constraints

- TDD with red-green-revert; fresh worktree per PR; each task ends with an independently testable deliverable.
- **`internal/cleanup` and `cmd/wt/cleanup*.go` stay free of deletion code** (the existing source-scan test must keep passing); the only package allowed to run `worktree remove`, `update-ref -d`, `branch -d/-D` or `os.Remove*` on user data is `internal/removal` (a new test pins this for the rest of `wt/`).
- Fail closed: any git error, unknown PR state, unresolved default branch or unreadable gate means **refuse**, never "go ahead".
- Never classify or delete by name alone: compare full refnames from `for-each-ref`; compare paths after `filepath.EvalSymlinks`; compare branches case-folded when checking "checked out elsewhere".
- Any `docs/contracts/` change rides in the same change as the code that writes or reads it, held by a Go contract test.
- `wt` Go tests: top-level `//` what-and-why comment on every `Test*`; fresh repo per case; branch name `topic`, not `feature`; `gitenv.IsolateForTest()` in `TestMain`; `gh` stubbed; live-process scans stubbed (no test scans the machine).
- Guides document the command, never live state. Docs that say "read-only / only lists / use repo-cleanup" change in the same PR: `cmd/wt/cleanup.go` header and `Long`, `wt/CLAUDE.md` classifier block, `wt/docs/wt-agents/README.md`, guide 08 §9, the TUI title line and `viewOnlyMessage`.
- PR bodies use `Refs #N`, never a closing keyword. Commits end `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`; PR bodies end `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- Verification before any commit: `go test ./...`, `go vet ./...`, and `make check` (check its **real** exit status: `make check >/dev/null 2>&1; echo $?` from `wt/`); `make test-all` and `bin/check-links` before the PR.

## Review Focus

1. **Acting on a stale list.** Every item is re-classified by `cleanup.Rescan` immediately before it is touched; it is skipped if its status got worse than the status the user saw or its tip moved (`TestEngineSkipsItemThatChanged`, Task 5).
2. **`update-ref -d` bypasses git's checked-out protection.** A fresh worktree listing (case-folded) must show no other checkout, and the CAS must use the re-classified tip (`TestBranchRetainedWhenCheckedOutElsewhere`, `TestBranchCASLosesRace`, Task 6).
3. **Force scopes conflated or hard blockers overridden.** `--force` and `-D` are independent; no flag overrides a hard blocker (`TestForceNeverOverridesHardBlockers`, Task 5).
4. **Symlinks and case-insensitive volumes.** `/var` vs `/private/var` paths, `Feature/x` vs `feature/x` (`TestEngineComparesPathsAfterEvalSymlinks`, `TestCheckedOutElsewhereIsCaseFolded`, Tasks 5-6).
5. **Half-done removals.** A worktree removed but its branch retained, or an interrupted `git worktree remove`, is reported accurately per item, never as success or silence (`TestOutcomeReportsWorktreeRemovedBranchRetained`, `TestInterruptedRemoveLeavesPrunableAndIsReported`, Tasks 5-6).

---

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/cleanup/rank.go` | `Status.Rank`, `Status.WorseThan` |
| `wt/internal/cleanup/rescan.go` | `Target`, `Rescan` (scoped re-classification, read-only) |
| `wt/internal/cleanup/classify.go`, `snapshot.go` | `Row` gains `Ref`, `Tip`, `Head`, `Losses`; fed from the snapshot |
| `wt/internal/cleanup/present.go` | `LossLines(Row)` wording, min-age header note |
| `wt/internal/removal/types.go` | `Item`, `Options`, outcome enums, `Outcome` |
| `wt/internal/removal/engine.go` | `Engine.Run` / `Engine.Plan`: per-item rescan, gates, ordering |
| `wt/internal/removal/worktree.go` | worktree removal (`git worktree remove`) |
| `wt/internal/removal/branch.go` | branch deletion (CAS `update-ref -d`) |
| `wt/internal/removal/procs.go` | live-process-by-cwd finder (seam, macOS `lsof`, fail-open) |
| `wt/internal/removal/json.go` | removal result envelope + encoder |
| `wt/internal/removal/*_test.go`, `fixture_test.go`, `testmain_test.go` | tests and slim real-git fixture |
| `wt/cmd/wt/remove.go` | `wt remove` |
| `wt/cmd/wt/cleanup.go` | min-age floor, registry-failure handling, `protectedPaths` fixes, wiring the remove function into the list |
| `wt/internal/tui/cleanup_list.go` (+ `cleanup_confirm.go`) | confirm / progress / results phases |
| `docs/contracts/remove-result.sample.json`, `remove-result.schema.json` | removal result contract |

---

### Task 1: Classifier additions the engine needs (read-only)

**Files:**
- Create: `wt/internal/cleanup/rank.go`, `rank_test.go`
- Modify: `wt/internal/cleanup/classify.go`, `snapshot.go`, `present.go` (+ tests)

**Interfaces:**
- Produces:
  - `func (s Status) Rank() int` — `proven`0 < `less_sure`1 < `unique_work`2 < `in_use`3; unknown value ranks worst (4).
  - `func (s Status) WorseThan(o Status) bool` — `s.Rank() > o.Rank()`.
  - `type Losses struct { Dirty bool; IgnoredEntries int; IgnoredBytes int64; Unpushed int; DetachedUnique bool }` — `Unpushed` is -1 when unknown.
  - `Row` gains `Ref string` (full refname `refs/heads/x` / `refs/remotes/o/x`, `""` for a detached worktree), `Tip string` (full sha of that ref), `Head string` (worktree HEAD sha, `""` for branch rows), `Losses Losses`. **None of them are serialised**: `RowJSON` is built field by field, so the cleanup JSON contract does not change (assert it with the existing contract test).
  - `func LossLines(r Row) []string` — one plain sentence per non-zero loss ("2 ignored entries, 1.3 MB will be deleted", "3 commits exist only on this machine", "uncommitted changes will be lost"); human-readable sizes.

- [ ] **Step 1: Write failing tests:** `TestStatusRankOrder` (proven<less_sure<unique_work<in_use; unknown status ranks worst); `TestStatusWorseThan`; `TestClassifyFillsRefTipHeadLosses` (a worktree with a dirty file and 2 ignored entries, and a local branch with 3 unpushed commits: `Ref` is the full refname, `Tip`/`Head` are 40-hex, `Losses` match); `TestLossLinesWording` (singular/plural, MB formatting, nothing for a clean row); `TestCleanupJSONUnchangedByNewRowFields` (encode a report and compare to the PR A sample shape: no `ref`/`tip`/`head`/`losses` keys).
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement** the signatures above; fill the new fields in `classify.go` from the `Snapshot` (`RefFact.Ref/Tip`, `WorktreeFact.Head`, `Gates`, `Proof.Unpushed`). Human size formatting: reuse an existing helper if the repo has one, else a small local function.
- [ ] **Step 4:** Run; expect PASS; revert-cycle `Rank`.
- [ ] **Step 5: Commit** `feat(wt): cleanup rows carry ref, tip and losses for removal`.

---

### Task 2: Scoped re-classification

**Files:**
- Create: `wt/internal/cleanup/rescan.go`, `rescan_test.go`

**Interfaces:**
- Consumes: `Gather` internals (`readRefs`, `ListWorktrees`, `CheckGates`, `prove`, `unpushed`, age helpers), `Classify`.
- Produces:
  - `type Target struct { Kind Kind; Path string; Ref string }` — a worktree is identified by `Path` (and its branch `Ref` if any), a branch by `Ref`.
  - `func Rescan(g Git, prior Snapshot, in GatherInputs, o Options, t Target) (Row, error)` — re-reads the repo state for **that target only** (its worktree list entry, its ref tip, its gates, its proof, its unpushed count, claims) and reuses `prior.PRs`, `prior.Merges`, `prior.Base`/`BaseTip`, **without any fetch or `gh` call**; builds a mini `Snapshot` and calls the pure `Classify`; returns the target's fresh row. A target that no longer exists returns `ErrTargetGone`. Any git failure returns an error (the engine refuses on error).
  - `var ErrTargetGone = errors.New(...)`.

- [ ] **Step 1: Write failing tests** (real-git fixture): `TestRescanReflectsNewDirtyFile` (a file added after the first `Gather` → the worktree row is now `unique_work`); `TestRescanReflectsMovedTip` (a commit added to the branch → `Tip` differs); `TestRescanGoneTarget` (`ErrTargetGone` after `git worktree remove` done by the test); `TestRescanDoesNotCallGhOrFetch` (stub `ghPRList` to fail the test if called; `GatherInputs.Fetch` ignored); `TestRescanMatchesGatherForUnchangedTarget` (for an untouched repo the rescanned row equals the row from a full `Gather`+`Classify`, field by field except `AgeDays`); `TestRescanCostIsScoped` (only the target worktree's gates run: count `git status` invocations via the `Git` runner seam, or assert the other worktree's dirty file is not read).
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement** by extracting the per-worktree and per-ref pieces of `Gather` into helpers both paths call (no behaviour change to `Gather`; the full PR A suite must still pass).
- [ ] **Step 4:** Run; expect PASS. **Step 5: Commit** `feat(wt): cleanup scoped rescan of one target`.

---

### Task 3: PR A carries that must precede deletion

**Files:**
- Modify: `wt/cmd/wt/cleanup.go` (+ tests), `wt/internal/cleanup/present.go` (+ tests), `wt/internal/cleanup/classify.go` (`Report` gains `MinAge`)

**Interfaces:**
- Produces:
  - `const MinAgeFloor = time.Minute`; `wt cleanup --min-age` below the floor is a usage error (exit 1). `Report` carries the `Options.MinAge` used; `HeaderLines` adds `min-age: lowered to <d> (default 10m): fresh branches may read as merged` when it is below `DefaultMinAge`.
  - `func protectedPaths(a *app, repoRoot string) ([]string, error)` in `cmd/wt/cleanup.go`: uses `config.ExpandHome` (bare `~` too); a relative `local_path` is resolved against `repoRoot` (as llmbench does, `model_write.go:137`), not the cwd; a `local_path` that is present but not a string, or a `local_path` that cannot be resolved, returns an error (fail closed); a registry that failed to load (`a.loadErr != nil`) returns `errRegistryUnavailable`.
  - `wt cleanup` (listing only) still works when `protectedPaths` errors, but the header gains `registry: failed to load; artifact protection is OFF and removal is refused`.

- [ ] **Step 1: Write failing tests:** `TestMinAgeFloorRejected` (`--min-age 30s` → exit 1, `--min-age 1m` ok); `TestHeaderNotesLoweredMinAge` (below default shows the note, default and above do not); `TestProtectedPathsExpandsBareHome`; `TestProtectedPathsRelativeIsAgainstRepoRoot`; `TestProtectedPathsMalformedFailsClosed`; `TestProtectedPathsRegistryFailedIsError`; `TestCleanupListHeaderWarnsWhenRegistryFailed`.
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement.** **Step 4:** Run; expect PASS. **Step 5: Commit** `fix(wt): cleanup min-age floor and fail-closed artifact protection`.

---

### Task 4: Removal package skeleton and slim fixture

**Files:**
- Create: `wt/internal/removal/types.go`, `testmain_test.go`, `fixture_test.go`, `types_test.go`

**Interfaces:**
- Produces:
  - `type Item struct { Kind cleanup.Kind; Path string; Ref string; Name string; Saw cleanup.Status; SawTip string }` — `Saw`/`SawTip` are what the user saw ticked.
  - `type WorktreeOutcome string` consts `WtRemoved`, `WtSkippedChanged`, `WtRefused`, `WtFailed`, `WtNotRequested` = `removed`, `skipped_changed`, `refused`, `failed`, `not_requested`.
  - `type BranchOutcome string` consts `BrDeleted`, `BrNotAttempted`, `BrRetainedUnmerged`, `BrRetainedMoved`, `BrRetainedCheckedOut`, `BrRetainedDefault`, `BrRetainedUnknown` = the wire strings in the calls list.
  - `type Outcome struct { Item Item; Worktree WorktreeOutcome; Branch BranchOutcome; Reason string; Warnings []string }` — `Reason` is one sanitised line (use the cleanup text sanitiser via an exported `cleanup.OneLine` if needed; export it, do not copy it).
  - `type Options struct { Force bool; DeleteUnmerged bool; DryRun bool; Now func() time.Time; Procs ProcFinder; RemoveTimeout time.Duration }` (zero `RemoveTimeout` means 10 minutes).
  - `type Engine struct { G cleanup.Git; Prior cleanup.Snapshot; In cleanup.GatherInputs; Classify cleanup.Options; Opts Options }`.
  - Test fixture: a slim real-git builder (`newFixture(t)`: bare origin + clone, `Commit`, `Branch`, `Worktree(branch) string` placing worktrees under `<repo>/.worktrees/`, `Dirty(dir,file)`, `Ignored(dir,file)`, `Lock(path)`, `SquashMerge(branch)`); `TestMain` with `gitenv.IsolateForTest()` and `ghPRList` stubbed via `cleanup.StubPRListForTest`.

- [ ] **Step 1: Write failing tests:** `TestOutcomeEnumStrings` (exact wire strings); `TestOutcomeReasonIsOneLine` (a multi-line/ESC reason is sanitised); `TestDeletionStringsOnlyInRemovalPackage` — scans every non-test `.go` file under `wt/` **except** `internal/removal/` and the pre-existing non-ref/worktree `os.Remove*` users (pidfiles, temp, backups, claim and theme files: list them explicitly by path in the test) for the banned tokens `"worktree", "remove"`, `"update-ref"`, `"branch", "-d"`, `"branch", "-D"`, `--delete`, a push refspec `":` + branch, `gh api -X DELETE`, `os.RemoveAll`; fails if any appears. Extend the existing `TestCleanupCmdContainsNoDeletion` banlist with the refspec and `gh api -X DELETE` forms.
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement** types and fixture. **Step 4:** Run; expect PASS. **Step 5: Commit** `feat(wt): removal package skeleton`.

---

### Task 5: Worktree removal

**Files:**
- Create: `wt/internal/removal/engine.go`, `worktree.go`, `engine_test.go`, `worktree_test.go`

**Interfaces:**
- Consumes: `cleanup.Rescan`, `Row.Tip/Head/Losses/Blockers/Status`, `Status.WorseThan`.
- Produces: `func (e Engine) Run(items []Item, progress func(Outcome)) []Outcome` and `func (e Engine) Plan(items []Item) []Outcome` (same decisions, no mutation; `Options.DryRun` makes `Run` call `Plan`). Items run **sequentially** in the order given (a worktree containing the cwd carries the hard `current` blocker and is simply refused, so there is no "remove the current directory last" case); `progress` is called after each item.

Per worktree item, in order (each refusal sets `Worktree=WtRefused` and a one-line `Reason`; an error from `Rescan` refuses with the error text; `ErrTargetGone` is `skipped_changed`):
1. `Rescan` the target (fresh `Row`).
2. Any hard blocker on the fresh row → refuse (the blocker code is in `Reason`). Not overridable by any option.
3. Fresh `Status.WorseThan(item.Saw)` or fresh `Tip != item.SawTip` → `skipped_changed` (never an error).
4. `Status` `unique_work`/`less_sure` is allowed only because the user ticked it; then: if `Losses.Dirty` and `!Opts.Force` → refuse ("uncommitted changes; --force required"); unpushed/net-zero commits with a branch that would be deleted are handled in Task 6.
5. Live processes by cwd (Task 7) → a `Warnings` entry, never a refusal.
6. Run `git worktree remove [--force] <path>` (`--force` only when step 4 found dirty and `Opts.Force`) with `Opts.RemoveTimeout`; a non-zero exit or timeout → `WtFailed` with git's sanitised stderr; verify afterwards with `ListWorktrees` that the path is gone (else `WtFailed`, and report a leftover prunable registration in `Reason`).
7. On success `WtRemoved`, then the branch stage (Task 6).

- [ ] **Step 1: Write failing tests** (real git): `TestEngineRemovesProvenWorktree`; `TestEngineSkipsItemThatChanged` (dirty file added after the first Gather → `skipped_changed`); `TestEngineSkipsWhenTipMoved`; `TestEngineRefusesEveryHardBlocker` (table: primary, current dir, locked, nested, operation in progress, protected artifact, default-branch worktree → `refused` with that code; with `Force` and `DeleteUnmerged` both set it still refuses: `TestForceNeverOverridesHardBlockers`); `TestEngineRefusesDirtyWithoutForce`; `TestEngineForceRemovesDirtyAfterApproval`; `TestEngineRemovesWorktreeWithIgnoredFiles` (a 200 MB-free `.env`-style fixture: ignored files go with the directory and `Losses` told the user first); `TestEngineRunsItemsInGivenOrder`; `TestEngineComparesPathsAfterEvalSymlinks` (item path given as `/var/...`, repo reports `/private/var/...`); `TestInterruptedRemoveLeavesPrunableAndIsReported` (a stub runner that fails after deleting the directory but before unregistering → `WtFailed` with "prunable" in `Reason`); `TestPlanMutatesNothing` (every refusal/skip decision equals `Run`'s, repo bytes unchanged); `TestRunProgressCalledPerItem`.
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement** `engine.go` (ordering, rescan, gates) and `worktree.go` (the git call through `cleanup.Git` with the timeout override; build the argument vector explicitly, never shell). **Step 4:** Run; expect PASS; revert-cycle the "status worse than saw" check and the hard-blocker refusal. **Step 5: Commit** `feat(wt): remove worktrees with per-item rescan`.

---

### Task 6: Branch deletion

**Files:**
- Create: `wt/internal/removal/branch.go`, `branch_test.go`; modify `engine.go`

**Interfaces:**
- Produces: `func (e Engine) deleteBranch(ctx itemCtx, fresh cleanup.Row) (BranchOutcome, string)` (unexported), called after a worktree removal succeeds and for branch-only items.

Rules, in order: no `Ref` (detached) → `not_attempted`; the ref is a default-branch form (`cleanup` already excludes it; also fail closed if the default branch is unresolved) → `retained_default`; the **fresh** row is not `proven` and `!Opts.DeleteUnmerged` → `retained_unmerged` (for a worktree item, this is the normal `-d` outcome and exit 0); fresh `Status` for the branch is unknown/`Unknown` flagged → `retained_unknown`; a **fresh** `ListWorktrees` (case-folded, by full refname) shows another worktree has the branch checked out → `retained_checked_out`; then `git update-ref -d <Ref> <fresh Tip>`: success → `deleted`; the ref moved so git refuses the stale old value → `retained_moved`; any other failure → `retained_unknown` with the sanitised stderr. With `DeleteUnmerged` the proven check is skipped but every other rule (default, checked-out, CAS) still applies.

- [ ] **Step 1: Write failing tests:** `TestBranchDeletedAfterWorktreeRemoval`; `TestBranchRetainedUnmergedWithoutD`; `TestBranchDeletedWithDOnUnmerged` (and the commits are still reachable via reflog hint in `Reason`: "recoverable with git reflog until it expires"); `TestBranchCASLosesRace` (a hook seam between the rescan and the `update-ref` moves the ref → `retained_moved`, branch intact); `TestBranchRetainedWhenCheckedOutElsewhere`; `TestCheckedOutElsewhereIsCaseFolded` (`Feature/x` vs `feature/x`, skipped on case-sensitive filesystems with a clear skip message); `TestBranchRetainedDefault`; `TestBranchNotAttemptedForDetached`; `TestBranchOnlyItem` (a local branch with no worktree); `TestOutcomeReportsWorktreeRemovedBranchRetained` (the item outcome has both fields and the right reason); `TestBranchNetZeroUnpushedNeedsD` (the `net_zero_unpushed` demotion from PR A: removing it without `-D` retains the branch).
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement** with an unexported test seam `var beforeUpdateRef = func() {}`. **Step 4:** Run; expect PASS; revert-cycle the CAS (use `update-ref -d <ref>` without the old value) and see `TestBranchCASLosesRace` fail. **Step 5: Commit** `feat(wt): delete local branches with compare-and-swap`.

---

### Task 7: Live processes and the confirmation data

**Files:**
- Create: `wt/internal/removal/procs.go`, `procs_test.go`

**Interfaces:**
- Produces:
  - `type ProcFinder interface { InDir(dir string) ([]Proc, error) }`, `type Proc struct { PID int; Command string }`.
  - `func LsofFinder() ProcFinder` — macOS: `lsof -a -d cwd -F pcn +D`-style lookup bounded by a 5 s timeout; on any error or timeout returns the error (the engine turns it into a `Warnings` entry "could not check for running processes", never a refusal).
  - `func Preview(e Engine, items []Item) []ItemPreview` where `ItemPreview{Item; Fresh cleanup.Row; LossLines []string; Procs []Proc; Warnings []string; Approval Approval}` and `Approval` is `ApprovalProven | ApprovalNeedsForce | ApprovalNeedsDelete | ApprovalRefused` — what a confirmation must ask for: proven → plain yes; dirty → typed confirm; unmerged branch with `-D` → typed confirm.

- [ ] **Step 1: Write failing tests** with a stub `ProcFinder` (no test scans the machine): `TestPreviewProvenNeedsPlainYes`; `TestPreviewDirtyNeedsForce`; `TestPreviewUnmergedNeedsDelete`; `TestPreviewRefusedShowsReason`; `TestPreviewListsProcesses`; `TestProcFinderErrorBecomesWarning`; `TestLsofParsing` (a canned `lsof -F` output with spaces in names).
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement.** **Step 4:** Run; expect PASS. **Step 5: Commit** `feat(wt): removal previews with losses and live processes`.

---

### Task 8: Removal result contract

**Files:**
- Create: `wt/internal/removal/json.go`, `json_test.go`, `docs/contracts/remove-result.sample.json`, `docs/contracts/remove-result.schema.json`; modify `wt/cmd/wt/cleanup_contract_test.go` (parameterise its schema validator and key-shape helpers so both contracts use them)

**Interfaces:**
- Produces: `type Envelope struct { Version int; Repo string; DryRun bool; Items []ItemJSON; Error *ErrorJSON }`, `ItemJSON{Kind, Name, Path, Ref, Saw, Worktree, Branch, Reason, Warnings, Losses []string}`, `func Encode(outcomes []Outcome, meta Meta) ([]byte, error)`, `func EncodeError(repo string, err error) ([]byte, error)`; `const EnvelopeVersion = 1`. Rules as for the cleanup contract: `items` always present (`[]` never `null`); enums for worktree and branch outcomes; absent vs empty `warnings`; one document even on failure; deterministic order (input order).

- [ ] **Step 1: Write failing tests:** `TestRemoveEnvelopeVersioned`; `TestRemoveEnvelopeItemsNeverNull`; `TestRemoveEnumsStable`; `TestRemoveErrorIsOneDocument`; `TestRemoveFixtureMatchesEnvelopeShape` (same key-shape + schema + enum-coverage tests as PR A, covering every outcome value in the fixture).
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement.** **Step 4:** Run; expect PASS. **Step 5: Commit** `feat(wt): wt remove result contract`.

---

### Task 9: `wt remove`

**Files:**
- Create: `wt/cmd/wt/remove.go`, `remove_test.go`; modify `main.go` (register next to `cleanupCmd`), `wt/cmd/wt/cleanup.go` (shared helpers for building `GatherInputs`/`Engine`)

**Interfaces:**
- Produces: `func removeCmd(a *app) *cobra.Command` — `wt remove <worktree-path-or-branch>... [--yes] [--force] [-D] [--dry-run] [--json] [--no-fetch] [--min-age D]`. Args resolve to rows by exact worktree path (after `EvalSymlinks`) or exact local branch name; an arg matching nothing, or more than one, is a usage error (exit 1) naming the argument. Each item's `Saw` status is its fresh status at command start (non-interactive flow has no earlier view), so the engine's "worse than saw" check still protects against changes during the run.
- Approval rules: proven items need `--yes` or a TTY `y/N` prompt (default No); an item whose preview approval is `NeedsForce` needs `--force`, `NeedsDelete` needs `-D`; without the flag the item is `refused` ("needs --force"); without `--yes` and without a terminal → exit 3, nothing removed. The prompt prints the `LossLines` and processes for every item first. Registry load failure → exit 1 before anything else.
- Exit codes per the calls list; the JSON envelope is printed first and alone on stdout under `--json`; human text goes to stderr only for notes.

- [ ] **Step 1: Write failing tests** (real git + scratch registry with `WT_REGISTRY` **and** `WT_LITELLM_CONFIG`): `TestRemoveCmdRemovesProvenWorktreeAndBranch`; `TestRemoveCmdNeedsYesWithoutTTY` (exit 3, repo untouched); `TestRemoveCmdUnmergedNeedsDeleteFlag`; `TestRemoveCmdDirtyNeedsForce`; `TestRemoveCmdDryRunMutatesNothing`; `TestRemoveCmdUnknownArgIsUsageError`; `TestRemoveCmdAmbiguousArgIsUsageError`; `TestRemoveCmdRegistryFailedRefuses`; `TestRemoveCmdPartialFailureExits2WithJSON`; `TestRemoveCmdJSONIsOneDocument`; `TestRemoveCmdProtectedArtifactRefused`; `TestRemoveCmdPromptShowsLosses` (prompt text contains each loss line).
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement.** **Step 4:** Run; expect PASS; check the process exit codes on a built binary in a scratch repo (separate call, outside the worktree). **Step 5: Commit** `feat(wt): wt remove`.

---

### Task 10: The list removes what you ticked

**Files:**
- Create: `wt/internal/tui/cleanup_confirm.go`, `cleanup_confirm_test.go`; modify `wt/internal/tui/cleanup_list.go` (+ tests), `wt/cmd/wt/cleanup.go` (wire a remove function into `runCleanupList`)

**Interfaces:**
- Consumes: `removal.Preview`, `removal.Engine.Run`.
- Produces: `type RemoveFuncs struct { Preview func(items []removal.Item) []removal.ItemPreview; Run func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome }`; `RunCleanupList(r cleanup.Report, th themes.Theme, rm RemoveFuncs) error`.
- Behaviour: `enter` with ticked rows opens a **confirm phase** (the list stays underneath): it lists every ticked item with its `LossLines` and processes, grouped by approval kind. Proven-only selections confirm with `y` (anything else cancels); a selection containing `NeedsForce`/`NeedsDelete` items requires typing the word `delete` and shows which flag each needs. Remote rows and `[-]` rows can never be in the selection. After confirming, a **progress phase** runs `Run` as a `tea.Cmd` stream (one message per item, screen shows the item in flight and finished outcomes); then a **results phase** lists each outcome with its reason and waits for a key; the list is rebuilt from a fresh `Gather` on return (or the program exits printing the results). `esc`/`n`/`ctrl+c` at the confirm step cancels with nothing removed. With nothing ticked, `enter` says "nothing ticked". The old view-only message and title text are removed.

- [ ] **Step 1: Write failing model-level tests** (drive `Update`, assert state and `View()` text; stub `RemoveFuncs`): `TestConfirmListsLossesForEveryTickedItem`; `TestConfirmProvenOnlyAcceptsY`; `TestConfirmUnprovenRequiresTypedDelete` (wrong word cancels, `delete` proceeds); `TestConfirmCancelRemovesNothing` (the stub `Run` is never called); `TestProgressShowsEachOutcome`; `TestResultsShowReasonsAndRetainedBranches`; `TestRemoteRowsNeverEnterTheSelection`; `TestConfirmFitsTheTerminal80x24` (and the repo's view-fits-the-terminal check); `TestEnterWithNothingTickedSaysSo`.
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement.** **Step 4:** Run; expect PASS. **Step 5: Real-screen check** through a pty driver at 80x24 in a scratch repo (confirm screen with a dirty worktree, typed confirm, results screen); put the capture in the PR body. **Step 6: Commit** `feat(wt): wt cleanup removes ticked rows after confirmation`.

---

### Task 11: Docs, end-to-end acceptance, PR

**Files:**
- Modify: `wt/CLAUDE.md`, `wt/docs/wt-agents/README.md`, `docs/guides/08-maintenance-and-troubleshooting.md` §9, root `CLAUDE.md` (Commands), `wt/cmd/wt/cleanup.go` header and `Long`, `wt/internal/cleanup/git.go` package comment, `wt/internal/tui/cleanup_list.go` comments

- [ ] **Step 1: End-to-end acceptance in scratch repos** (outside the worktree, scripted): build a repo with: a proven squash-merged worktree, a proven merged branch with no worktree, a dirty worktree, an unmerged branch, a locked worktree, a worktree holding a registered artifact, a worktree whose branch is also checked out elsewhere. Run `wt remove --dry-run --json`, then real `wt remove` for the proven items, then confirm with `git worktree list`, `git branch`, and the filesystem that **exactly** the requested items are gone, the unrequested and refused ones are byte-identical, and a second run is a no-op. Record the result table for the PR body.
- [ ] **Step 2: Docs.** `wt/CLAUDE.md`: classifier block now says the classifier is read-only and `internal/removal` is the only deletion code; replace the "Before wt cleanup deletes anything" list with what shipped and what remains (PR C: origin rows, `base_of_open_pr` under a failed fetch, remote `HeadRefOid==tip`; later: trash drain); add the removal invariants (rescan before acting, CAS, force scopes, outcomes frozen). README command table: add `wt remove`, update `wt cleanup`. Guide 08 §9: how to remove (list, `wt remove`), what each approval means, no live state. Root `CLAUDE.md` Commands line for `wt remove`. Run `bin/check-links`.
- [ ] **Step 3: Verification:** `go test ./...`, `go vet ./...`, real `make check` status in `wt/`; `make test-all` at the repo root.
- [ ] **Step 4: Review** with the process used for PR A (task reviews + one whole-branch review, one fix wave, one scoped re-review); fix findings.
- [ ] **Step 5: Open the PR** (base: the branch PR A was merged into until #518 reaches `main`, then `main`); the body uses `Refs #N`, includes the acceptance table and the pty capture, lists the calls made, and ends with the attribution line.

---

## Self-Review

- **Spec coverage:** per-item rescan and "worse than saw" (T1-T2, T5); plain `git worktree remove`, never forced past a blocker, `--force`/`-D` scopes (T5-T6); CAS branch delete with fresh topology (T6); `branch_outcome` pinned (T4, T6, T8); confirmation of losses and live processes (T7, T9-T10); Ruling 14 typed confirmation (T10); `wt remove` single-item form over the same engine (T9); the carry list (min-age floor, registry-failed refusal, `ExpandHome`, banlist extension, remote rows not removable) (T3-T4, T10); docs that claim read-only (T11). **Deliberately not in PR B:** trash-rename drain, origin rows (PR C), move-to-worktree (PR D).
- **Types:** `Item`, `Outcome`, `Engine`, `Options` defined once in T4 and reused unchanged in T5-T10; `Row.Ref/Tip/Head/Losses` defined in T1 and consumed in T2, T5-T7.
- **Proportion:** signatures, rules and test names are specified; bodies are the implementer's.
- **Open for the builder:** exact lsof flags; wording of loss sentences; the pty script; how `Rescan` shares helpers with `Gather` without changing its behaviour.
