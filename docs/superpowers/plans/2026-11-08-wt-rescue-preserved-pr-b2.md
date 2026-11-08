# Dirty Rescued Originals Read Removable (PR B2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After `wt rescue`, the original worktree reads removable even when it still has uncommitted work, provided its working tree and index are exactly what the rescue record captured. It is `less_sure` (needs `--accept-loss`, never `--force`), and removal re-checks the record immediately before `git worktree remove`.

**Architecture:** `internal/cleanup` decides inside `preservedOf` (so `Gather`, `Rescan`, the removal gate and rescue's preflight all agree) whether the worktree's index and working tree equal the record's commits I and W, with a lazy helper that also rejects the states a snapshot cannot describe. `Preserved` gains `CoversDirt`; the classifier suppresses `dirty` for a covered row, raises `preserved` as a soft reason, and `Row.EffectiveLosses()` clears `Dirty`. `internal/removal` re-runs `Rescan` of that one row right before the git call and shows a preview note. Spec: `docs/superpowers/specs/2026-11-07-wt-rescue-preserved-by-design.md` ("The classifier rule", "The engine"), amended in Task 6.

**Tech Stack:** Go (module `wt/`), `internal/dirtsnap`, the cleanup/removal fixtures, the JSON-schema contract tests.

**Spec:** as above. PR A (#542) wrote the record; PR B1 (#547) reads it for commits. Paths are relative to `wt/` unless they start with `wt/` or `docs/`.

## Global Constraints

- Fail closed everywhere. Any git or IO error, missing object, limit hit, `ErrUnsupported` or `ErrTooLarge` in the dirt check means `CoversDirt` is false and the row reads exactly as today (`unique_work`, `dirty`, needs `--force`). A dirty row with a record that does not cover it is never `Preserved`.
- `Classify` stays pure; all git work lives in `worktreeState`/`preservedOf` (shared by `Gather` and `Rescan`). `internal/cleanup` code and comments must not contain `update-ref`, `"push"`, `--delete`, `worktree remove`, `os.Remove(`, `os.RemoveAll`. `cleanup` may not import `internal/rescue`; it may import `internal/dirtsnap` (stdlib-only).
- The dirt check runs only when ALL of B1's match rules already hold (trailers, `recordCovers`) and `Gates.Dirty` is true. A clean row never hashes anything (B1's cost promise). Cheapest checks first.
- `CoversDirt` is true only when ALL hold: no hard blocker and `GatesErr == ""` (operation in progress is already a blocker); `rev-parse W^{tree} W^^{tree} W^^^` resolves and `W^^^` equals `Wt-Source-Head`; no hidden index flag (`ls-files -v`: `S` or lowercase); no intent-to-add entry; `git diff-index --cached --quiet <I>` exits 0 (index tree equals I; unmerged entries and ITA make it differ); `dirtsnap.Build(dir, run, false, limits)` equals W's tree (`ErrUnsupported` for nested repo, submodule, special file, unmerged; `ErrTooLarge` over the caps).
- Limits equal rescue's: 20000 files, 512 MiB. They are duplicated as exported consts in `cleanup` and pinned equal to `rescue.MaxUntrackedFiles`/`MaxUntrackedBytes` by a drift test in package `rescue`. A state rescue accepted but cleanup's cap refuses reads "not preserved" (safe); documented.
- The `dirtsnap.Runner` for cleanup copies the worktree-level `Git` with `Config=["core.fsmonitor=false"]` and `Timeout=2*time.Minute` (the worktree `Git` drops the root's Config/Env; the 30 s default would silently turn a big hash into "not preserved"); non-zero exit is an error; `GIT_OPTIONAL_LOCKS=0` comes from `Git.RunInput`.
- `Preserved` is applied to a dirt-covered row independently of ladder evidence (a merged-branch or detached original whose only uncovered part is dirt): in a separate attach step, only for rows with no blockers, `ws.Preserved.SourceHead == Head`, and (branch rows) `Tip == SourceHead`. A default-branch row (`BlockDefaultBranch`) stays `in_use` and gets no `Preserved`.
- `ReasonPreserved` is **soft** (via `j.weak`) when `CoversDirt`, informational otherwise (B1). `ReasonDirty` is not raised for a covered row. Evidence stays what the ladder found; `rescued` only when commits were covered by `branchPreserved`/`detachedPreserved`.
- `Row.EffectiveLosses()` additionally clears `Dirty` only when `Preserved.CoversDirt`. Its `Unpushed` clearing is unchanged in effect; raw `Losses` stay raw. Raw read sites stay raw: `exitprompt.Eligible` (a dirty preserved row is never offered at exit), `rescue.Preflight`/`Offered`, `gitForce` (internal `--force`), `GatesUnknown`.
- The pre-remove re-check sits in `worktreeAct` after the dry-run return and immediately before `removeWorktree`, only for `Preserved != nil && CoversDirt`; nothing slow runs between it and the git call. A `Rescan` error there is `refused` (`ErrTargetGone` is `skipped_changed`); any mismatch is `skipped_changed`, nothing removed.
- Contracts additive/none: no schema change (`preserved` and `rescued` exist); `remove-result` unchanged; the preview `Note` is display-only (not in the seal or JSON). Wire strings frozen.
- Every `Test*` has a top-level comment saying what and why. Every implementer dispatch also runs `go test ./cmd/wt -run 'Rescue|Cleanup|Remove' -count=1` (the guard tests read sources across packages).
- PR body says `Refs #532`, `Refs #522`, never a closing keyword. Follow-ups stay #543 (prune), #548 (unreadable records).

## Review Focus

Inputs the spec implies that no task would otherwise exercise, most likely first (each has its test in the named task):

1. An assume-unchanged or skip-worktree file edited after the rescue: I and W still equal the record, but the edit is invisible to `status`; must not be covered (Task 1).
2. A dirty original on a branch already merged (ancestor/same-commit) or detached with no unique commits: dirt is the only uncovered part, so `Preserved` must still attach and the dirt must not be silently dropped from the losses (Task 3).
3. Edit-then-revert to identical bytes between rescue and removal: still covered; a one-byte change, an exec-bit flip, a restage or a new untracked file between preview and remove: `skipped_changed`, nothing removed (Tasks 2, 4).
4. A dirty row on the default branch or with an open PR: stays `in_use` / `unique_work`; covered dirt never overrides another reason (Task 3).
5. A hand-forged record whose chain has the right trailers but a different shape (W's grandparent is not `Wt-Source-Head`), or a dirty source with a clean (HEAD-tree) record: not covered (Task 1).

## File Structure

- Create `internal/cleanup/dirt.go` (`dirtCovered`, `dirtRunner`, limit consts) and `dirt_test.go`.
- Modify `internal/cleanup`: `preserved.go` (`Preserved.CoversDirt`, `preservedOf`), `classify.go` (`preservable`, `worktree`, `preserve`, attach step, `EffectiveLosses`), `fixture_test.go` (`addRecord` snapshot mode), `losses_guard_test.go`, `present.go` (detail text).
- Modify `internal/removal`: `engine.go` (`worktreeAct` re-check), `preview.go` (`Notes`); tests `preserved_test.go`, `engine_test.go`, `saw_test.go` helpers; guard table.
- Modify `internal/rescue` (drift test only), `internal/exitprompt` (test only), `cmd/wt` (`rescue.go` help text, tests).
- Docs: `cmd/wt/rescue.go`, `docs/contracts/rescue-result.schema.json` sentence, `wt/docs/wt-cleanup.md`, `wt/CLAUDE.md`, root `CLAUDE.md`, the spec amendment, `docs/guides/08-maintenance-and-troubleshooting.md` §9 if it states dirty originals are not removable.

---

### Task 1: does a record cover this worktree's dirt

**Files:**
- Create: `internal/cleanup/dirt.go`, `internal/cleanup/dirt_test.go`
- Modify: `internal/cleanup/fixture_test.go` (`recordOpts.Snapshot`), `internal/rescue` test (limit drift)

**Interfaces:**
- Consumes: B1's `RescueRecord`, `hiddenIndexFlags(wg Git) (bool, error)` in `worktrees.go`, `dirtsnap.Build`, `dirtsnap.Runner`, `dirtsnap.Limits`.
- Produces:
  - `const DirtMaxFiles = 20000`, `DirtMaxBytes = 512 << 20`.
  - `func dirtRunner(wg Git) dirtsnap.Runner`.
  - `func dirtCovered(wg Git, w WorktreeFact, r RescueRecord) bool` — every error is false; order: tree ids and `W^^^ == r.SourceHead`, hidden flags, intent-to-add (`diff-files --diff-filter=A --name-only -z` non-empty), `diff-index --cached --quiet <r.Commit>^` (the I commit), then `Build`.
  - Test helper: `recordOpts.Snapshot bool` makes `addRecord` compute I from the source's index and W from the working tree with an oracle independent of the code under test (temporary `GIT_INDEX_FILE` copy plus `git add -A` plus `git write-tree`); default false keeps the HEAD-tree record.

- [ ] **Step 1: failing tests** in `dirt_test.go`, each with a real scratch repo and `addRecord(..., Snapshot: true)` after dirtying: `TestDirtCoveredMatrix` — table of (mutation after the record, covered?): none (covered); edit tracked file, edit untracked file, new untracked file, delete tracked file, chmod +x, restage a tracked edit, stage a new file, replace file with symlink (all not covered); edit-then-revert to identical bytes (covered); `0600` mode change (covered, declared loss, comment says so); empty directory added (covered, declared loss). `TestDirtCoveredNeverPreservedStates` — skip-worktree and assume-unchanged on an edited file (the record taken before the flag), intent-to-add, an unmerged path, a nested repository, a changed submodule: each false. `TestDirtCoveredRejectsForgedChain` — a record whose W grandparent is not `r.SourceHead` is false; a record with HEAD-tree I and W against a dirty source is false. `TestDirtCoveredFailsClosed` — a missing tree object, a cap lowered through the exported var seam, a failing runner: false. `TestDirtCoveredReadsNothingWhenClean` — via a counting `Git` seam: a clean worktree issues no git call (guard on `preservedOf` in Task 2 shows the same). `TestDirtLimitsMatchRescue` in package `rescue`: `cleanup.DirtMaxFiles == MaxUntrackedFiles`, `cleanup.DirtMaxBytes == MaxUntrackedBytes`.
- [ ] **Step 2: run** `go test ./internal/cleanup -run 'Dirt' -count=1` — expect FAIL (undefined).
- [ ] **Step 3: implement** `dirtRunner`, `dirtCovered` and the consts in `dirt.go` per the Interfaces; reuse `hiddenIndexFlags` as is; use `diff-index --cached --quiet` rather than `dirtsnap.IndexTree` (that forks one `hash-object` per index directory on every Gather and Rescan; ruling recorded in the ledger).
- [ ] **Step 4: run** the same tests, then `go test ./internal/rescue -run Limits -count=1` and `go test ./cmd/wt -run TestCleanupCmdContainsNoDeletion -count=1` — expect PASS.
- [ ] **Step 5: commit** — `feat(wt): cleanup can tell a record covers a worktree's uncommitted work`.

### Task 2: `Preserved.CoversDirt` through Gather and Rescan

**Files:**
- Modify: `internal/cleanup/preserved.go`, `internal/cleanup/preserved_test.go`

**Interfaces:**
- Consumes: Task 1's `dirtCovered`.
- Produces: `type Preserved struct{ Ref, Commit, SourceHead, DestBranch, DestPath string; CoversDirt bool }`. `preservedOf` no longer returns nil for `Gates.Dirty`; after the trailer match and `recordCovers` it returns `Preserved{CoversDirt:false}` for a clean row, and for a dirty row `Preserved{CoversDirt:true}` only if `dirtCovered` holds, else it tries the next record. `len(Blockers)>0`, `GatesErr`, Primary, Bare and empty HEAD still return nil.

- [ ] **Step 1: failing tests**: `TestPreservedOfDirtyRow` (matching dirt gives `CoversDirt`; edited file, no record snapshot, and a dirty source with only a clean record give nil — the existing "dirty worktree" case in `preserved_test.go` stays as the mismatch case); `TestPreservedOfPicksTheCoveringRecord` (two records, one older and mismatching, one covering: preserved by the second); `TestGatherAndRescanAgreeOnDirt` (same row from `Gather` and `Rescan` for covered, edited-after, and flagged-hidden cases); `TestPreservedOfHardBlockerStaysNil` (rebase in progress with a matching record: nil, row `in_use`).
- [ ] **Step 2: run** `go test ./internal/cleanup -run 'PreservedOf|GatherAndRescan' -count=1` — expect FAIL.
- [ ] **Step 3: implement** the `preservedOf` change; pass the worktree-level `Git` the state builder already uses.
- [ ] **Step 4: run** the same tests plus `go test ./internal/cleanup -count=1` — expect PASS (existing clean tests unchanged since the zero value is false).
- [ ] **Step 5: commit** — `feat(wt): preservedOf covers uncommitted work the record holds`.

### Task 3: the classifier and the effective view

**Files:**
- Modify: `internal/cleanup/classify.go`, `internal/cleanup/present.go`, `internal/cleanup/classify_test.go` (or the existing preserved classifier tests), `internal/cleanup/losses_guard_test.go`

**Interfaces:**
- Consumes: Task 2's `Preserved.CoversDirt`.
- Produces:
  - `preservable(ws)` admits `Gates.Dirty` when `ws.Preserved.CoversDirt` (so `branchPreserved`, `detachedPreserved` and `BranchProven` work for a dirty covered row).
  - `classifier.dirtCovered(ws WorktreeState) bool`; `worktree()` skips `j.uniq(ReasonDirty, ...)` when true, `row.Losses.Dirty` stays raw.
  - `(c *classifier) preserve` raises `j.weak(ReasonPreserved, ...)` when `CoversDirt` (detail adds `", including its uncommitted changes"`), `j.info` otherwise.
  - An attach step after the branch/detached switch: when the dirt is covered, `row.Preserved == nil`, no blockers, `SourceHead == Head`, and (branch rows) `Tip == SourceHead`, set `row.Preserved = c.preserve(&j, ws.Preserved)`; it never sets `ev`.
  - `Row.EffectiveLosses()` also clears `Dirty` when `Preserved.CoversDirt`.

- [ ] **Step 1: failing tests**: `TestClassifyDirtyCoveredBranchWithUnpushed` (status `less_sure`, evidence `rescued`, reasons contain soft `preserved`, no `dirty`, no `unpushed`, `BranchProven` true, effective `Dirty` false and raw true); `TestClassifyDirtyCoveredMergedBranch` (branch an ancestor of main, dirt only: `less_sure`, evidence `ancestor` kept, `Preserved` set, effective `Dirty` false, no `dirty` reason — the silent-drop case); `TestClassifyDirtyCoveredDetached` (detached with no unique commits, and with unique commits); `TestClassifyDirtyCoveredDoesNotOverride` — table: open PR, `pr_unknown`, ignored files, local base (`capFallback`), default branch (`in_use`, `Preserved` nil), blocker present: the other reason still decides status, default branch gets no `Preserved`; `TestClassifyDirtyNotCoveredUnchanged` (edited after the record: `unique_work`, `dirty`, `Preserved` nil); `TestEffectiveLossesClearsDirtOnlyWhenCovered` (also never `GatesUnknown`, ignored fields, `Unpushed == -1`); `TestPreservedDetailMentionsDirtOnlyWhenCovered`; `TestRowTickableWhenDirtCovered` (`less_sure` ticks).
- [ ] **Step 2: run** `go test ./internal/cleanup -run 'Classify|EffectiveLosses|Preserved' -count=1` — expect FAIL.
- [ ] **Step 3: implement** the signatures above in `classify.go`; keep `ReasonDetachedUnique` conditioned as is. Update `losses_guard_test.go` counts exactly (`classifier.worktree` stays `write: 7`; any new `.Losses` read carries a `// losses: raw, <why>` marker or calls `EffectiveLosses`); write the new table before the change so the guard fails first.
- [ ] **Step 4: run** the same tests, `go test ./internal/cleanup -count=1` and `TestEveryCodeHasWording` — expect PASS.
- [ ] **Step 5: commit** — `feat(wt): a dirty rescued original reads less_sure, not unique_work`.

### Task 4: the removal engine re-checks right before removing

**Files:**
- Modify: `internal/removal/engine.go` (`worktreeAct`), `internal/removal/preview.go` (`Notes`), `internal/removal/saw_test.go`, `internal/removal/preserved_test.go`, `internal/cleanup/losses_guard_test.go`

**Interfaces:**
- Consumes: Task 3's `Row.Preserved.CoversDirt`, `Row.EffectiveLosses`, the `rescan` seam.
- Produces:
  - In `worktreeAct`, after the dry-run return and before `e.removeWorktree`: when `g.fresh.Preserved != nil && g.fresh.Preserved.CoversDirt`, one `rescan(e.G, e.Prior, e.In, e.classifyOpts(), Target{Kind: KindWorktree, Path: it.Path, Ref: it.Ref})`. `ErrTargetGone` returns `skipped_changed`; any other error `refused` (`could not re-check`); then compare and return `skipped_changed` (reason `changed while being checked: the rescue record no longer covers the uncommitted changes`, `o.LossLines = lossLines(fresh2)`) unless: `fresh2.Preserved` non-nil with `CoversDirt` and the same `Ref` and `Commit`; same `Tip` (or `Head` if detached) as the gate's and `it.SawTip`; no blockers, not `in_use`, `!Losses.GatesUnknown`; `!Status.WorseThan(it.Saw)`; `!lossesWorse(fresh2.EffectiveLosses(), it.SawLosses)`. `gitForce` is recomputed from `fresh2.Losses.Dirty`.
  - `ItemPreview.Notes` gains `uncommitted changes are kept in <ref> (copy: <path>)` for such an item (one sanitised line, display-only, not in `previewBasis`/the seal, no JSON).

- [ ] **Step 1: failing tests** (real rescue engine via `rescueWorktree`, dirty the source first, then `stubRescan`/`countRescans`): `TestDirtyPreservedItemRemovesWithAcceptLoss` (needs exactly `Confirm`, `Approval needs_confirm`, one `worktree remove --force -- <path>`, branch deleted unasked, record ref and `rescued/<name>` survive); `TestDirtyPreservedItemLapses` — table, each `skipped_changed` with the exact reason and zero removal commands: edit tracked, edit untracked, exec-bit flip, restage, new untracked file, record deleted, all between preview and `Run`; `TestPreReCheckCatchesEditAfterGate` (`stubRescan` call 1 real, call 2 preceded by an edit: `skipped_changed`, nothing removed); `TestPreReCheckErrorIsRefused` and `...TargetGoneIsSkipped`; `TestEditThenRevertStillRemoves`; `TestPreviewDoesNotRunTheReCheck` (preview and dry run issue one rescan); `TestPreviewNoteForCoveredDirt` (note present, absent for a clean preserved row, not part of the seal); `TestSubmoduleStillRefusedUnderForce` (hand-forged or post-rescue-initialised submodule: not covered, needs Force, `submodulesIn` still refuses); `TestMissingAcceptLossRefuses` (`needs --accept-loss`, no `Force` message).
- [ ] **Step 2: run** `go test ./internal/removal -run 'Dirty|ReCheck|Preview' -count=1` — expect FAIL.
- [ ] **Step 3: implement** the re-check and the note; share the lapse comparison with the gate's steps 6-7 if it stays small, else duplicate with a comment and keep the guard table exact (new key for the function that reads `EffectiveLosses`/`SawLosses`).
- [ ] **Step 4: run** the same tests, `go test ./internal/removal ./internal/cleanup -count=1`, and the cross-package guards — expect PASS.
- [ ] **Step 5: commit** — `feat(wt): re-check the record right before removing a dirty rescued original`.

### Task 5: end to end and what must stay raw

**Files:**
- Modify: `cmd/wt/rescue_removable_test.go` (or a sibling `rescue_dirty_removable_test.go`), `internal/exitprompt/exitprompt_test.go`, `internal/rescue` preflight/offer tests

- [ ] **Step 1: tests**: `TestRescueDirtyOriginalIsRemovableWithAcceptLoss` (real binary path as the existing `rescueClean` helper: `wt rescue --yes`, `cleanup --json` row `less_sure`, reason `preserved`, effective loss lines without `uncommitted changes`; `remove` without flags exits 3 naming `--accept-loss`; with `--accept-loss` exits 0; branch and worktree gone, destination and record intact); negatives `TestRescueDirtyOriginalNegatives` — edit after the rescue, hidden flag, an op in progress, a primary/default-branch worktree: `unique_work` or `in_use`, `--force` still required; `TestExitPromptNeverOffersDirtyPreserved` and `TestRescueStillOffersDirtyPreserved` (raw reads: a second rescue stays possible, the exit prompt does not offer it).
- [ ] **Step 2: run** `go test ./cmd/wt -run 'Rescue|Cleanup|Remove' -count=1` and the package tests — expect FAIL first for the new cases, then PASS after wiring any gap.
- [ ] **Step 3: commit** — `test(wt): a dirty rescued original end to end`.

### Task 6: docs, help text and spec amendments

**Files:**
- Modify: `cmd/wt/rescue.go` (help paragraph and success text), `docs/contracts/rescue-result.schema.json` (one sentence), `wt/docs/wt-cleanup.md`, `wt/CLAUDE.md`, root `CLAUDE.md` (`wt rescue` bullet), `docs/guides/08-maintenance-and-troubleshooting.md` §9 if affected, the spec "Amendments (2026-11-08)" section

- [ ] **Step 1: edit**: every B1 sentence saying a dirty original stays as it was becomes true-to-code (a dirty original whose current state equals the record reads `less_sure` and needs `--accept-loss`, not `--force`; anything else is unchanged). State the declared losses (file modes beyond the exec bit, empty directories, ignored files, the write window before `git worktree remove`) and the cost (a covered-dirt row hashes its changed files on every Gather and Rescan, and once more right before removal). Spec amendment records: `Preserved` attaches to a dirt-covered row independently of evidence; `diff-index --cached` replaces `IndexTree` for the index comparison; limits duplicated and pinned; re-check sits in `worktreeAct`, not the gate (preview would pay a second Rescan per row); a re-check `Rescan` error is `refused`; clean initialised submodules are not detected by the classifier and are refused by `submodulesIn` at removal.
- [ ] **Step 2: run** `bin/check-links` from the repo root (`ALL LINKS OK`) and `go test ./cmd/wt -run 'Rescue|Contract' -count=1`.
- [ ] **Step 3: commit** — `docs(wt): a dirty rescued original is removable with --accept-loss`.

### Task 7: acceptance on the built binary

- [ ] **Step 1:** `D=$(mktemp -d); go build -o "$D/wt" ./cmd/wt` (no `make install`); scratch repo with a bare `origin` and the redirected environment from `wt/CLAUDE.md` (`WT_REGISTRY`, `WT_LITELLM_CONFIG`, `XDG_CONFIG_HOME`).
- [ ] **Step 2:** a worktree with two unpushed commits, a tracked edit, a staged file and an untracked file: `wt rescue <wt> --yes --json`; `cleanup --plain --no-fetch` lists it `less_sure` with the preserved reason naming its uncommitted changes; `remove <wt> --dry-run` shows the note and `--accept-loss` as the only need; `remove <wt> --accept-loss --yes` removes worktree and branch; record and `rescued/<name>` remain.
- [ ] **Step 3:** repeat with: an edit after the rescue (`unique_work`, `--force` needed); `git update-index --assume-unchanged` on an edited file (`unique_work`); a rebase in progress (`in_use`); a dirty original on a merged branch (`less_sure`, evidence `ancestor`); an edit made between `remove --dry-run` and `remove` via a held terminal prompt (`skipped_changed`).
- [ ] **Step 4:** the full gate from `wt/`: `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `make check`, `make -C .. check-links`; report commands and outputs for the PR body (`Refs #532`, `Refs #522`).

---

## Self-review

- **Spec coverage:** dirt rule and never-preserved states (Task 1), `CoversDirt` and Gather/Rescan agreement (Task 2), soft `preserved`, `dirty` suppression, `EffectiveLosses` `Dirty`, `BranchProven` and the independent attach step (Task 3), `gitForce` raw, `Needs{Confirm}` only, pre-remove re-check, residual window, preview note (Task 4), raw sites and end to end (Task 5), docs and amendments (Task 6), acceptance (Task 7).
- **Rulings made here** (to ledger at execution start): `diff-index --cached` over `IndexTree`; re-check in `worktreeAct` with `Rescan` errors `refused`; same caps as rescue; attach step independent of evidence; preview `Note` and no Outcome warning; clean initialised submodule left to `submodulesIn`.
- **Type consistency:** `Preserved.CoversDirt`, `dirtCovered`, `dirtRunner`, `DirtMaxFiles`/`DirtMaxBytes`, `EffectiveLosses`, `preservable`, `ReasonPreserved` are named identically across tasks.
- **Proportion:** no step carries a body; each is a signature, a named test with assertions, or a command.
