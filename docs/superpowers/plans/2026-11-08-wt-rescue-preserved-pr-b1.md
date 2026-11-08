# Clean Rescued Originals Read Removable (PR B1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After `wt rescue`, the original worktree (clean apart from its commits) reads `proven` with evidence `rescued` in `wt cleanup`, is removed with a plain `--yes`, and takes its own branch with it, because a record under `refs/wt/rescued/*` holds every commit it has.

**Architecture:** `internal/cleanup` lists the records once per `Gather` and once per `Rescan`, decides per worktree whether a record covers its commits (git work in the snapshot; `Classify` stays pure), and the classifier turns that into `Row.Preserved`, evidence `rescued`, an informational reason `preserved` and `BranchProven`. A new `Row.EffectiveLosses()` view (covered commits no longer count as lost) replaces the raw `Losses` at every removal-side read site except the deliberately raw ones, pinned by a `go/ast` guard test. Dirt coverage is PR B2 and is not built here: a dirty source reads exactly as today.

**Tech Stack:** Go (module `wt/`), the existing cleanup/removal test fixtures, the JSON-schema contract tests.

**Spec:** `docs/superpowers/specs/2026-11-07-wt-rescue-preserved-by-design.md` ("The classifier rule", "The effective view", "The engine", "PR B1"), as amended by "Amendments" in Task 9 below. PR A (merged as #542) wrote the record; this plan reads it. Go and test paths are relative to `wt/`; a path starting with `wt/` or `docs/` is from the repository root.

## Global Constraints

- Fail closed. A record that is missing, unparsable, ambiguous, stale, whose objects are missing, or whose listing fails means `Preserved` is nil and the row reads exactly as today. Unknown stays Unknown.
- `Classify` stays pure (no I/O). Every git call goes through `cleanup.Git`. `internal/cleanup` must not contain the substring `update-ref` (not even in a comment), `"push"`, `--delete`, `worktree remove`, `os.Remove(`, `os.RemoveAll` (`TestCleanupCmdContainsNoDeletion` scans comments too); it may not import `internal/rescue` (cycle), so the ref prefix and trailer keys are duplicated in `internal/cleanup`.
- The signal applies to the source worktree row only (branch or detached). Never to a branch row, a remote row, the rescue destination, a primary, or a worktree with a hard blocker.
- Preserved applies only when the row would otherwise be `unique_work` for its commits: it has no ladder evidence, and either (branch row) raw `Losses.Unpushed > 0` or (detached row) `Losses.DetachedUnique`. A row with `Unpushed < 0` (unknown), any dirt (`Gates.Dirty`, including hidden index flags), `GatesErr != ""`, or ladder evidence is never preserved.
- A record matches a worktree only when ALL hold: trailer `Wt-Version` is `1`; `Wt-Source-Path` equals `cleanup.ResolvePath(worktree path)`; `Wt-Source-Ref` equals the worktree's full refname (`detached` for a detached worktree) exactly; `Wt-Source-Head` equals the worktree HEAD (and, for a branch row, the fresh branch tip); `cat-file -e` finds the record commit and its parent; `git rev-list --count <HEAD> --not --remotes <record ref>` is `0`. Any one matching record suffices; an unparsable or non-matching record is ignored.
- `Rescan` lists records afresh and never copies them from `prior`; a record-listing error degrades to "no records" and is not returned as an error (a `Rescan` error is `refused`, not `skipped_changed`).
- Raw `Losses` stay as the classifier computes them. `Row.EffectiveLosses()` clears only `Unpushed > 0` and `DetachedUnique` of a `Preserved` row (never `-1`, never `Dirty`, `IgnoredEntries`, `IgnoredBytes`, `GatesUnknown`). `Losses` stays a comparable struct.
- Raw (deliberate) read sites, unchanged: `internal/exitprompt` `Eligible`, `internal/rescue` `Preflight`/`Offered`, `removal` worktree gate `GatesUnknown`, `removal` `gitForce`. A second `wt rescue` of a preserved original must still be possible.
- Evidence string `rescued`; reason code `preserved` (informational: in neither unique, unknown nor soft; it does not stop `proven`). Text of the reason: `preserved by <record ref> (copy: <dest path>)`, all values through `flat()`.
- Contracts additive only: `cleanup-list` `evidence` gains `rescued`, `reasons[].code` gains `preserved`; `EnvelopeVersion` stays 1; `remove-result.*` does not change; `RowJSON` gains no field.
- Every `Test*` has a top-level comment saying what and why; scratch repo per case; `TestMain` isolation as in each package. Every implementer dispatch also runs `go test ./cmd/wt -run 'Rescue|Cleanup|Remove' -count=1` (the guard tests read sources across packages).
- PR body says `Refs #532`, `Refs #522`, never a closing keyword. The pruning follow-up is #543 (already filed).

## Review Focus

Inputs the spec implies that no task would otherwise exercise, most likely first (each has its test in the named task):

1. A hand-made ref under `refs/wt/rescued/` with garbage trailers (duplicates, control characters, a newline in a value): ignored or sanitised, never a crash and never a preserved row (Task 1, Task 2).
2. gh unavailable or the PR lookup unknown for the source branch: the row reads `less_sure`, not `proven` (Task 3).
3. The source branch has an open PR: stays `unique_work` (Task 3).
4. A record for the right path but a different (older) head, next to a correct one: preserved by the correct one (Task 2).
5. Two worktrees, one rescued, one not, in one `Gather`: only the rescued one is preserved (Task 2).

## File Structure

- Create `internal/cleanup/records.go` (record type, listing, parsing, matching) and `internal/cleanup/preserved.go` (`Preserved`, `preservedOf`, `worktreeState`); tests `records_test.go`, `preserved_test.go`, `losses_guard_test.go`.
- Modify `internal/cleanup`: `classify.go` (`Row.Preserved`, `judge.info`, `ReasonPreserved`, `local()`/`worktree()`/`branchProven()`), `ladder.go` (`EvRescued`), `snapshot.go` (`Snapshot.Records`, `WorktreeState.Preserved`, `Gather`), `rescan.go`, `present.go` (`EffectiveLosses`, `LossLines`, wording, `evidenceText`).
- Modify `internal/removal`: `engine.go`, `preview.go`, `branch.go`; tests `fixture_test.go` (`itemOf`), `engine_test.go`, `branch_test.go`.
- Modify `internal/exitprompt` (test only), `internal/rescue` (tests only), `cmd/wt/rescue.go` (help and success text), `cmd/wt` tests.
- Contracts: `docs/contracts/cleanup-list.schema.json`, `docs/contracts/cleanup-list.sample.json`, `docs/contracts/rescue-result.schema.json` (one sentence).
- Docs listed in Task 9.

---

### Task 1: record listing and parsing

**Files:**
- Create: `internal/cleanup/records.go`, `internal/cleanup/records_test.go`
- Modify: `internal/cleanup/fixture_test.go` (a hand-built record helper)

**Interfaces:**
- Produces:
  - `const RecordRefPrefix = "refs/wt/rescued/"` and unexported trailer-key constants (`Wt-Version`, `Wt-Source-Path`, `Wt-Source-Ref`, `Wt-Source-Head`, `Wt-Dest-Branch`, `Wt-Dest-Path`).
  - `type RescueRecord struct{ Ref, Commit, SourcePath, SourceRef, SourceHead, DestBranch, DestPath string }`.
  - `func ListRecords(g Git) ([]RescueRecord, error)`: one `for-each-ref` over `RecordRefPrefix`; keeps only records with `Wt-Version: 1` and exactly one value for each required trailer; drops the rest. Fields are NUL-separated, no reuse of `parseRefRecord`.
  - Test helper `(f *fixture) addRecord(t, srcPath string, o recordOpts) string` building the chain by hand (I = tree of the source's index via `git write-tree` on a copy or `commit-tree` of HEAD's tree, W = child, six trailers, ref `refs/wt/rescued/<16 hex of W>`); `recordOpts` overrides each trailer to build the negative cases.

- [ ] **Step 1: failing tests** in `records_test.go`: `TestListRecordsParsesWhatRescueWrites` (a hand-built chain returns the six values and the ref/commit); `TestListRecordsIgnoresMalformedRecords` — table: duplicate `Wt-Source-Path`, missing `Wt-Source-Head`, `Wt-Version: 2`, a value with a control character, a ref whose commit has no trailers, a non-commit object at the ref; each is dropped, the valid one beside it is kept; `TestListRecordsFailsClosed` (a failing git runner returns an error and no records); `TestRecordConstantsAvoidDeletionWords` (the file text contains none of the strings the deletion scan bans).
- [ ] **Step 2: run** `go test ./internal/cleanup -run 'Record' -count=1` — expect FAIL (undefined).
- [ ] **Step 3: implement** `ListRecords` with `for-each-ref --format=%(refname)%00%(objectname)%00%(trailers:key=Wt-Version,valueonly,unfold)%00…` per key (verify the exact trailer atom output in a scratch repo and trim); a trailer atom printing several values means a duplicate, so drop the record.
- [ ] **Step 4: run** the same tests plus `go test ./cmd/wt -run TestCleanupCmdContainsNoDeletion -count=1` — expect PASS.
- [ ] **Step 5: commit** — `feat(wt): cleanup lists the rescue records`.

### Task 2: which worktrees a record preserves

**Files:**
- Create: `internal/cleanup/preserved.go`, `internal/cleanup/preserved_test.go`
- Modify: `internal/cleanup/snapshot.go` (`Snapshot.Records`, `WorktreeState.Preserved`, `Gather`), `internal/cleanup/rescan.go`

**Interfaces:**
- Consumes: Task 1's `RescueRecord`, `ListRecords`.
- Produces:
  - `type Preserved struct{ Ref, Commit, DestBranch, DestPath string }` (no `CoversDirt`: B2).
  - `func preservedOf(g Git, ws WorktreeState, recs []RescueRecord) *Preserved` — nil unless the Global Constraints' preconditions and match rules hold (it does not look at the classifier; the classifier decides whether the covered part exists).
  - `func worktreeState(g Git, w WorktreeFact, gin gateInputs, recs []RescueRecord) WorktreeState` wrapping `gateState` + `preservedOf`; both `Gather` (the loop at snapshot.go ~186) and `Rescan` (rescan.go ~118) call it, so a third caller cannot skip it. `Snapshot.Records []RescueRecord` filled by `Gather` before the loop; `Rescan` calls `ListRecords` itself, ignoring `prior`; a listing error becomes `nil` records, never an error.

- [ ] **Step 1: failing tests** (`preserved_test.go`, hand-built records via Task 1's helper; each case a fresh repo): `TestPreservedOfMatchesTheRescuedWorktree` (clean worktree with an unpushed commit and a matching record → non-nil, `DestBranch`/`DestPath` from the trailers); mismatch table, each nil: different `Wt-Source-Path`, different `Wt-Source-Ref`, different `Wt-Source-Head`, a commit added after the record, record commit or its parent object missing (`git prune` of a loose object or a crafted ref), a commit present on a remote-tracking ref only (covered anyway: still preserved, count 0), dirty worktree, `GatesErr` set, a primary worktree, a worktree with an operation in progress, a detached worktree with `Wt-Source-Ref: refs/heads/x` and the reverse; `TestPreservedOfUsesTheMatchingRecordAmongSeveral` (a stale record with an older head beside the right one → preserved); `TestPreservedOfIgnoresAForgedRecord` (garbage trailers, control characters, duplicate keys → nil, no panic, `DestPath` sanitised if it is ever shown); `TestGatherAndRescanAgreeOnPreserved` (full `Gather`+`Classify` row equals the `Rescan` row for the preserved worktree via the existing `sameRow`; and a second, unrescued worktree is not preserved); `TestRescanReadsRecordsAfresh` (delete the record between `Gather` and `Rescan` → the `Rescan` row has no `Preserved`); `TestNoRecordsMeansNoExtraGitWork` (with no `refs/wt/rescued/` ref, only the one `for-each-ref` runs; `TestRescanCostIsScoped` still passes).
- [ ] **Step 2: run** — expect FAIL.
- [ ] **Step 3: implement** the types and functions with the signatures above; the coverage check is `cat-file -e` on the record commit and `<commit>^`, and `rev-list --count <HEAD> --not --remotes <ref>` equal to `0`. Compare paths with `ResolvePath`, refs exactly, `HEAD` with `ws.Fact.Head`.
- [ ] **Step 4: run** `go test ./internal/cleanup -count=1` — expect PASS (all existing cleanup tests unchanged).
- [ ] **Step 5: commit** — `feat(wt): cleanup decides which worktrees a rescue record preserves`.

### Task 3: the classifier uses it

**Files:**
- Modify: `internal/cleanup/classify.go`, `internal/cleanup/ladder.go`, `internal/cleanup/present.go`, `internal/cleanup/classify_test.go` or a new `preserved_classify_test.go`

**Interfaces:**
- Consumes: `WorktreeState.Preserved`.
- Produces: `Row.Preserved *Preserved` (unserialised); `const EvRescued Evidence = "rescued"`; `const ReasonPreserved = "preserved"`; `judge.info []Reason` (appended to `Reasons` after soft in `fill`, ignored by `status()` and by `fill`'s `not_merged` fallback); `reasonWording[ReasonPreserved]`, `detailIsSentence` entry, `evidenceText` case for `EvRescued`.

- [ ] **Step 1: failing tests** — a table over a rescued worktree built with Task 1's helper, asserting status, evidence, reason codes, `Row.BranchProven`, `Row.Preserved`:
  - clean branch worktree with unpushed commits and a matching record → `proven`, `rescued`, reasons `[preserved]`, `BranchProven` true, raw `Losses.Unpushed` unchanged (>0);
  - same with an ignored file → `less_sure` (`ignored_files`), `BranchProven` true;
  - base fallback (`base_is_local`) → `less_sure`;
  - PR lookup unknown for the branch (gh down) → `less_sure` with `pr_unknown`, `Preserved` set (Review Focus 2);
  - open PR on the branch → `unique_work` (`open_pr`) (Review Focus 3);
  - dirty worktree with a record → exactly today's row (`unique_work`, `dirty` and `unpushed`);
  - raw `Unpushed == -1` → not preserved, `unpushed_unknown` as today;
  - a row that already has ladder evidence (merged branch) → `Preserved` ignored, unchanged;
  - detached worktree whose unique HEAD a record covers → `proven`, `rescued`, no `detached_unique`;
  - a branch row (no worktree) and the rescue destination (`rescued/x` worktree) → never preserved;
  - `TestEveryCodeHasWording`, `TestCleanupJSONUnchangedByNewRowFields` pass with the new values.
- [ ] **Step 2: run** — expect FAIL.
- [ ] **Step 3: implement.** In `local()` take a `preserved bool`: skip raising `unpushed` for it, leave every other check (`open_pr`, `pr_head_moved`, `pr_unknown`, `weak_signal`) as they are, and set `j.ev = EvRescued` after those checks when the proof is `NotMerged` and `j.ev` is still empty; pass the same flag in `branchProven()` so `BranchProven` is true for the worktree's own branch; `worktree()` decides `preserved := ws.Preserved != nil && ws.GatesErr == "" && !g.Dirty && <branch: tip == Preserved.Commit's head and raw Unpushed > 0 | detached: DetachedUnique>`, skips `detached_unique` for a preserved detached row and sets `j.ev` for it, then `j.info = append(j.info, preserved reason)`. `minAge` must not fire (it is age-gated only for `same-commit`/`ancestor`); `net_zero_unpushed` must not fire for a preserved row.
- [ ] **Step 4: run** `go test ./internal/cleanup -count=1` — expect PASS.
- [ ] **Step 5: commit** — `feat(wt): a clean rescued original reads proven (rescued evidence)`.

### Task 4: the effective view and its guard

**Files:**
- Modify: `internal/cleanup/present.go` (`LossLines`), `internal/cleanup/classify.go` (`Row.EffectiveLosses`)
- Create: `internal/cleanup/losses_guard_test.go`
- Test: `internal/cleanup/rowfacts_test.go` (extend)

**Interfaces:**
- Produces: `func (r Row) EffectiveLosses() Losses`; `LossLines(r Row)` reads it.

- [ ] **Step 1: failing tests:** `TestEffectiveLossesOfAPreservedRow` — `Unpushed 3` → `0`; `Unpushed -1` stays `-1`; `DetachedUnique` → false; `Dirty`, `IgnoredEntries`, `IgnoredBytes`, `GatesUnknown` unchanged; a row without `Preserved` returns `Losses` unchanged; `TestLossLinesFollowsTheEffectiveView` (a preserved row with 3 raw unpushed has no "commits exist only on this machine" line; one with an ignored file keeps that line); `TestLossesStaysComparable` (`Losses{} == Losses{}` compiles and the struct has no slice/map/pointer field); the guard (below) with its "bites" subtests.
- [ ] **Step 2: run** — expect FAIL.
- [ ] **Step 3: implement** `EffectiveLosses` and the `LossLines` switch. Guard `TestEveryLossesReadSiteIsLabelled` in `losses_guard_test.go`: a `go/ast` walk (as `deletionscan_test.go` does; `go/types` is not available) of non-test `.go` files under `../../cmd/wt` and `../../internal`, finding every `*ast.SelectorExpr` whose `Sel` is `Losses`, `SawLosses` or `sawLosses` and whose receiver is not a package name; each is keyed `relative-file#EnclosingFunc` (with receiver type) in an explicit table `map[string]lossSite{kind, count}`, `kind` one of `raw`, `effective`, `write`; the test fails on an unlisted site, a changed count, a stale entry; `raw` sites require an inline comment `// losses: raw, <why>` on the same or previous line; `effective` sites require the function to call `EffectiveLosses` or `LossLines`/`lossLines`. Fill the table from the PR's recon: removal `needsOf` (effective), `unapproved` (effective), worktree gate `lossesWorse` and the branch-stage `lossesWorse` (effective), `ItemFromRow` (effective), the seal copies (follow `ItemFromRow`), worktree gate `GatesUnknown` and `gitForce` (raw), `exitprompt` `Eligible` (raw), `rescue` `Preflight` and `Offered` (raw), cleanup's own constructors in `classify.go` (write). `TestLossesGuardBites` runs the scanner on synthetic snippets: an unlabelled new read, a raw site without its marker, an effective site that never calls the view.
- [ ] **Step 4: run** `go test ./internal/cleanup -count=1` — expect the table test to FAIL until Task 6 switches the removal sites, so add the removal entries in Task 6; here run the effective-view tests only and keep the guard test skipped with `t.Skip` removed in Task 6 (note this in the commit).
- [ ] **Step 5: commit** — `feat(wt): Row.EffectiveLosses and the Losses read-site guard`.

### Task 5: the contract

**Files:**
- Modify: `docs/contracts/cleanup-list.schema.json`, `docs/contracts/cleanup-list.sample.json`, `cmd/wt/cleanup_contract_test.go` only if a count or list is asserted there

- [ ] **Step 1: failing tests:** run `go test ./cmd/wt -run 'CleanupSchemaEnumsCoverEveryCode|CleanupFixtureMatchesEnvelopeShape' -count=1` after Task 3 — expect FAIL (`ReasonPreserved` and `EvRescued` are not in the schema).
- [ ] **Step 2: implement:** `evidence` enum gains `rescued`; `reasons[].code` enum gains `preserved`; the schema's top description sentence about `evidence` ("only ever holds a proof") says `rescued` means a wt rescue record holds the commits; add one proven worktree row to `cleanup-list.sample.json` (`evidence: "rescued"`, `reasons: [{"code":"preserved","detail":"preserved by refs/wt/rescued/0123456789abcdef (copy: /Users/me/repo/.worktrees/rescued-spike)"}]`).
- [ ] **Step 3: run** `go test ./cmd/wt -run Cleanup -count=1` — expect PASS.
- [ ] **Step 4: commit** — `feat(wt): cleanup-list gains the rescued evidence and preserved reason`.

### Task 6: the removal engine

**Files:**
- Modify: `internal/removal/engine.go`, `internal/removal/preview.go`, `internal/removal/branch.go`, `internal/cleanup/losses_guard_test.go` (add the removal entries, drop the Task 4 skip)
- Test: `internal/removal/fixture_test.go` (`itemOf` uses `row.EffectiveLosses()`), `engine_test.go`, `branch_test.go`

**Interfaces:**
- Consumes: `Row.EffectiveLosses()`, `Row.Preserved`, `Row.BranchProven`.

- [ ] **Step 1: failing tests** (a rescued original built through a hand-written record helper in the removal fixture, or through `internal/rescue` which `removal` tests may import):
  - `TestEngineRemovesAPreservedOriginalWithAPlainItem`: `Needs{}` is empty, the worktree is removed, the branch is `deleted` unasked, no gc caveat; the destination branch and worktree are untouched; the record ref still exists;
  - `TestPreviewOfAPreservedOriginalIsProven` (`Approval == ApprovalProven`, no loss lines, no "branch will be kept" note);
  - with an ignored file: `needs_confirm` only, loss line for the ignored entries only;
  - `TestPreservedItemLapses` table, each `WtSkippedChanged` with nothing removed: the record ref deleted between `Preview` and `Run` (status message "status changed ... (unique_work, was proven)"); a new commit in the source; the branch moved; the record's commit made unreadable; `Rescan` made to fail (stub the `rescan` seam) is `WtRefused` (documented, not skipped);
  - `TestTipHeldByTheRecordAlone` (destination branch deleted first: no `gcCaveat` warning is printed);
  - `TestBranchStageKnowsThePreservedCode` (a preserved row that is also Unknown and approved with `-D` is not rejected only for carrying `preserved`).
- [ ] **Step 2: run** — expect FAIL.
- [ ] **Step 3: implement:** `needsOf` Force and `unapproved` read `fresh.EffectiveLosses()`; both `lossesWorse` calls (worktree gate, branch stage) take `fresh.EffectiveLosses()`; `ItemFromRow` seals `r.EffectiveLosses()`; `knownNotUnknown` gains `cleanup.ReasonPreserved`; `tipHeldElsewhere` adds `refs/wt/rescued/` to its `for-each-ref` argv and its comment; leave `GatesUnknown` and `gitForce` raw with `// losses: raw, <why>` markers. Update the guard table with the removal entries and remove the Task 4 `t.Skip`.
- [ ] **Step 4: run** `go test ./internal/removal ./internal/cleanup -count=1` and `go test ./internal/removal -run TestDeletionStringsOnlyInRemovalPackage -count=1` — expect PASS.
- [ ] **Step 5: commit** — `feat(wt): wt remove treats a preserved original as proven`.

### Task 7: what must stay raw

**Files:**
- Test: `internal/exitprompt/offer_test.go`, `internal/rescue/offer_test.go`, `internal/rescue/preflight_test.go`

- [ ] **Step 1: tests (they pass today; they pin rawness):** `TestEligibleRejectsAPreservedRow` — extend `TestEligibleNamesTheFailingCondition`'s table with a row having `Preserved` set and raw `Unpushed = 2`, `p.LossLines = nil`: `Eligible` is false with the "lose something" reason; `TestOfferedStillOffersAPreservedRow` and `TestPreflightStillAcceptsASecondRescueOfAPreservedOriginal` (rescue, classify, rescue again with `--to`: `nothing_to_keep` is not raised, a second distinct record appears).
- [ ] **Step 2: run** `go test ./internal/exitprompt ./internal/rescue -count=1` — expect PASS.
- [ ] **Step 3: commit** — `test(wt): the exit prompt and rescue keep reading the raw losses`.

### Task 8: end to end

**Files:**
- Create: `cmd/wt/rescue_removable_test.go` (package main; may use `rescueApp`, `cleanupRepo`, `runRescue`, `runCleanup`, `runRemove`, `decodeRescue`, `decodeOne`, `hasBranch`, `exists`, `cgit`, `cleanupCommit`, plus a new `cleanWorktree(t, dir)` helper: `git worktree add -b spike`, one unpushed commit, no dirt)

- [ ] **Step 1: tests:**
  - `TestRescuedOriginalReadsRemovableAndGoesWithItsBranch`: rescue (`--yes --json --no-fetch`, record present), `wt cleanup --json --no-fetch` shows the source `proven`/`rescued`/`preserved` and the destination `rescued/spike` still `unique_work`; `wt remove --no-fetch --yes <source>` exits 0, the source worktree and branch `spike` are gone, `rescued/spike` and the record remain, no gc warning;
  - `TestCleanupReadsWhatRescueWrites` (pins the duplicated prefix and trailer keys: after a real rescue the cleanup row's `Preserved.DestBranch` and `DestPath` equal the rescue result's destination);
  - negatives, each leaving the source `unique_work` (or `less_sure` for the ignored file): a new commit in the source after the rescue; the record ref deleted; a dirty source (also rescued earlier clean, then edited); an ignored file; gh unavailable → `less_sure`;
  - `TestRescueTwiceThenRemove` (two records; removal works);
  - `TestExitPromptDoesNotOfferARescuedOriginal` is covered in Task 7; here only the combined run.
- [ ] **Step 2: run** `go test ./cmd/wt -run 'RescuedOriginal|ReadsWhatRescueWrites|RescueTwice' -count=1` — expect PASS.
- [ ] **Step 3: commit** — `test(wt): a rescued original is removed end to end`.

### Task 9: docs, help text and the spec amendments

**Files:**
- Modify: `cmd/wt/rescue.go` (help paragraph "not made removable either…" and the success sentence), `docs/contracts/rescue-result.schema.json` (top-level sentence "`moved` does NOT make the source removable…"), `wt/docs/wt-cleanup.md` (groups table, rescue section "The original stays as it was…", approvals, the manual record removal command stays), `wt/CLAUDE.md` (cleanup intro, Rescue invariants bullets 1 and the "nothing reads it yet" record bullet, "Still to do"), root `CLAUDE.md` (the `wt rescue` Commands line), `docs/guides/08-maintenance-and-troubleshooting.md` §9 (the group bullets and the sentence "The original is not made removable by this…"; describe the rule and show `wt cleanup --plain` as the command that reveals it, no live state), `wt/docs/internals/packages.md`, `wt/docs/internals/testing.md` (the guard test and the end-to-end test), `wt/CHANGELOG.md` (Unreleased entry), `docs/superpowers/specs/2026-11-04-wt-cleanup-design.md` (a second "Amended" note), and `docs/superpowers/specs/2026-11-07-wt-rescue-preserved-by-design.md` (a new `## Amendments (2026-11-08)` section stating: commit I's tree is built by `dirtsnap.IndexTree`, not `git write-tree`; `Preserved` applies only when the row's commits are the uncovered part; `Rescan` errors are `refused`, not `skipped_changed`, so record-listing failures degrade to no records; the informational reason needs a fifth judge slot; `EffectiveLosses` clears only `Unpushed > 0`; the B2 `write-tree` note is moot).

- [ ] **Step 1: edit** the files. Every sentence that says the original is not made removable becomes true-to-code: a *clean* original (commits only) reads `proven` and goes with a plain `--yes`; a dirty one is unchanged (B2). No sentence may claim dirt is covered.
- [ ] **Step 2: run** `bin/check-links` from the repo root (`ALL LINKS OK`), `go test ./cmd/wt -run 'Rescue|Contract' -count=1` (the help text and schema sentence are not asserted, but run them).
- [ ] **Step 3: commit** — `docs(wt): a clean rescued original is removable`.

### Task 10: acceptance on the built binary

- [ ] **Step 1:** `D=$(mktemp -d); go build -o "$D/wt" ./cmd/wt` (no `make install`); scratch repo with a bare `origin` and the redirected environment from `wt/CLAUDE.md`.
- [ ] **Step 2:** a clean worktree with two unpushed commits: `wt rescue <wt> --yes --json`; `"$D/wt" cleanup --plain --no-fetch` lists the original under proven merged with the evidence text for a rescue record; `--json` shows `evidence: rescued`, reason `preserved`; the destination is under "has work that exists nowhere else".
- [ ] **Step 3:** `"$D/wt" remove <wt> --dry-run --no-fetch` says it would remove the worktree and branch with no approval needed; `--yes` removes both; `git for-each-ref refs/wt/rescued` still lists the record; `rescued/<name>` still exists.
- [ ] **Step 4:** repeat with an extra commit made after the rescue (reads `unique_work`), with a deleted record (`unique_work`), with an ignored file (`less_sure`, `--accept-loss` needed), and with a dirty edit (unchanged from today); show `wt remove --dry-run` output for each.
- [ ] **Step 5:** the full gate from `wt/`: `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `make check`, `make -C .. check-links`; report commands and outputs for the PR body (`Refs #532`, `Refs #522`).

---

## Self-review

- **Spec coverage:** record listing and match rules (Tasks 1, 2); `Row.Preserved`, `EvRescued`, `preserved` informational, `BranchProven` (Task 3); raw `Losses` kept plus `EffectiveLosses` and the guard (Tasks 4, 6); `needsOf`, `unapproved`, `lossesWorse` x2, seal, `LossLines` (Tasks 4, 6); `knownNotUnknown`, `tipHeldElsewhere` (Task 6); schema/sample/enums (Task 5); lapse tests with the corrected `skipped_changed`/`refused` split (Task 6); exit prompt and rescue stay raw (Task 7); end to end including the second rescue and the destination untouched (Task 8); docs and the design amendments (Task 9). Deliberately not in B1: dirt coverage, `CoversDirt`, the pre-remove re-check, internal `--force`, the `less_sure` approval for dirt (B2); record pruning (#543).
- **Type consistency:** `RescueRecord`, `ListRecords`, `Preserved{Ref, Commit, DestBranch, DestPath}`, `preservedOf`, `worktreeState`, `Row.Preserved`, `Row.EffectiveLosses`, `EvRescued`, `ReasonPreserved` are named identically across tasks.
- **Proportion:** bodies appear nowhere; every step is a signature, a test name with assertions, or a command.
