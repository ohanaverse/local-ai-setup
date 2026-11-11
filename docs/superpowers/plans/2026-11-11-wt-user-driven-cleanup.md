# wt user-driven repo maintenance — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One worktree/branch list (launch selector and a new `wt worktrees` sub-command, `wt cleanup` kept as an alias) that always shows every row with a status, warns instead of blocking, and offers maintenance actions on each row.

**Architecture:** `internal/cleanup` stays the single classifier (Gather → Classify → Row). The picker becomes a view over its rows (no second classifier); the interactive list becomes one TUI component with a launch mode and a manage mode. Gates become one `launchwarn` package that warns and proceeds. Automatic behaviors (fetch on open, exit prompt) are removed; claims become labels.

**Tech Stack:** Go (module `wt/`, Go 1.26.7), Bubble Tea TUI, cobra, real-git fixtures via `internal/gittest`; JSON contracts under `docs/contracts/`.

**Spec:** `docs/superpowers/specs/2026-11-11-wt-user-driven-cleanup-design.md`. Read it and `wt/CLAUDE.md` before any task.

## Delivery shape

Five stacked PRs, one per Part below, each green on its own (`make test-all`), merged in order. Each Part starts with a **Task 0 re-read** because the later Parts' file lists were gathered against `main`, not against the earlier Parts' result. PR bodies say `Refs #N`, never a closing keyword. Work in a worktree off the previous Part's branch; never use bare `git stash`.

## Global Constraints

- Contract fixtures (`docs/contracts/*`) and their Go contract tests change in the same commit as any JSON key or code change.
- `docs/guides/` embeds no live state (no counts, no routing snapshots); show the command that reveals it.
- Docs, `CLAUDE.md` and `wt/CLAUDE.md` lines for a behavior change in the same commit as the code; `make check-links` must pass (it skips paths containing `superpowers`).
- Git-heavy Go tests use the `internal/gittest` template kit (`gittest.Seed(t, gittest.SeedOpts{...}).Clone(t)`); pure classifier cases use `handSnapshot()` (`internal/cleanup/classify_test.go`) with no git.
- TUI changes get a real-screen check at 80x24 through the pty driver that answers the OSC 11 query (project memory `tui-screens-via-pty`).
- Removal stays only in `internal/removal` (re-check before acting, compare-and-swap). `wt remove` flags and exit codes (`--yes --accept-loss --force -D --remote`, 0/1/2/3) do not change.
- `rescue`, `restore-root`, `ReleaseRoot`, the pre-commit hook script, `--no-fetch`, `--no-guard` and `WT_SKIP_MAIN_BLOCK` stay.
- Hard stops, the only refusals: removing the cwd or an ancestor of it, the primary checkout, an operation in progress (rebase/merge/cherry-pick), a locked worktree, and the engine's integrity refusals (submodules, non-sha1, case-colliding branch names, missing directory).

## Decisions this plan makes (the spec leaves them to the builder)

- Sub-command is `wt worktrees`; `cleanup` is a cobra alias. Same command object, same `--json` bytes.
- **One keymap in every mode of the list:** `x` remove highlighted row · `d` delete its local branch · `o` delete its origin ref · `r` rescue · `R` restore root · `F` refresh (`git fetch --prune`). Enter launches (launch mode) or removes ticked rows (manage mode). Where Part 3's recon proposed `r`/`Shift-R` for refresh, `F` replaces it.
- `delete origin` stays the typed phrase for origin rows; plain `delete` for everything at risk; `y` for proven.
- `hard_stop` is a single string on the row; `IsHardStop(code, kind)` decides which blocker codes are hard stops. `held` and `default_branch` (on a branch or remote row) are verdict floors, not hard stops.
- Remote refs folded into a local row stay classified in `Report.Folded`, reachable through `Report.RemoteRows()`.
- PR lookup is split: `Gather` with `SkipPRs` then `FillPRs`; unknown PR state sets `Row.PRUnknown` and the row is never `proven`.
- Claims are keyed by worktree path and store pid plus process start time.

## Review Focus

Inputs the spec implies but no obvious task covers; each has a named test in the owning task.

1. Root on a detached HEAD, or no `origin/HEAD` (no known default branch): list still shows every row; no drift badge is invented without a default (Part 4 Task 11, Part 1 Task 3).
2. `gh` missing or offline: list opens at once, tags read `PR ?`, no row is `proven` on git evidence alone when PRs were asked (Part 1 Task 6).
3. Branch names with slashes or case variants folded with a remote ref; two local branches differing only by case (Part 1 Task 4).
4. A reused pid or a dead wt: no permanent "held" label, and opening the list prunes nothing (Part 4 Tasks 1, 3, 13).
5. Removing the cwd or one of its ancestors through every path — `x` in the list, `wt remove`, `--json` sweep — still refuses; the primary checkout can never be `proven` (Part 1 Task 1-2, Part 5 Task 6).

---

# Part 1 — One row model and a unified list

Branch `wt-list-1-rows`. Verify: `cd wt && go test ./internal/cleanup ./internal/removal ./internal/rescue ./internal/tui ./internal/worktree ./cmd/wt -count=1`, then `make test-all` from the repo root.

### Task 0: Re-read

- [ ] Branch off `main` (this spec's branch is already merged or rebased). `go build ./... && go test ./internal/cleanup ./cmd/wt -count=1` is green before starting.

### Task 1: Hard stop vs verdict floor in the classifier

**Files:** Modify `wt/internal/cleanup/classify.go` (`judge.status` :206, `judge.fill` :218, `worktree` :307, `branchRow` :420, `remoteRow` :626), `wt/internal/cleanup/worktrees.go` (`CheckGates` :186-202); Test `wt/internal/cleanup/classify_test.go`.

**Interfaces:**
- Produces: `func IsHardStop(code string, k Kind) bool` — true for primary, current, locked, nested, operation_in_progress, protected_artifact, open_pr, base_of_open_pr on every kind; false for `held` and `default_branch` (both are floors that need `--accept-loss`). `Row.HardStop string` (first hard-stop code, "" if none), `Row.Primary bool`, `Row.Current bool`. A row with only floor blockers (held, default-branch worktree) reads `less_sure`, never `proven`, never `in_use`.

- [ ] Write failing tests: `TestHardStopCodesAreExactlyThese` (table: every `Block*` const × each `Kind`), `TestHeldLinkedWorktreeIsLessSureNotInUse`, `TestPrimaryAndCurrentRowsKeepAHardStop` (root: `in_use`, `HardStop=="primary"`), `TestDefaultBranchWorktreeIsAFloorNotAHardStop`, `TestRowsThatCanNeverBeProven` (primary, current, held, default-branch worktree and branch row all `!= proven` and `!Tickable`).
- [ ] Run `go test ./internal/cleanup -run 'HardStop|Floor|NeverBeProven|HeldLinked|PrimaryAndCurrent' -v` → FAIL (undefined `IsHardStop`).
- [ ] Implement per the interfaces; `judge.status()` returns `in_use` only when a hard-stop blocker is present, and a `judge.hold` floor caps `proven` to `less_sure`.
- [ ] Run the package tests → PASS; commit `feat(wt): hard stops vs verdict floors in the cleanup classifier`.

### Task 2: Consumers act on hard stops only; held and default need `--accept-loss`

**Files:** Modify `wt/internal/removal/engine.go` (worktreeGate :269-275, coverLapsed :453, branchGate :511-516, `blockerReason` :661), `wt/internal/removal/remote.go` (:115-120), `wt/cmd/wt/remove.go` (`planRemove` :272, `inUseReason` :339), `wt/internal/rescue/preflight.go` (:59-72 keeps held as a warning); Tests `removal/engine_test.go` (`blockerCases` ~:190-215: drop the `BlockDefaultBranch` entry), `cmd/wt/remove_test.go`, `internal/tui/cleanup_list_test.go` (row at :155).

**Interfaces:** Consumes `Row.HardStop`, `IsHardStop`. Produces: `wt remove` on a held or default-branch item without `--accept-loss` → exit 2 with a message containing `needs --accept-loss`; with it → removed. Primary and cwd never removable.

- [ ] Write failing tests: `TestHeldWorktreeNeedsConfirmNotRefused` (engine; claim a linked path), `TestRemoveCmdHeldNeedsAcceptLoss`, `TestRemoveCmdPrimaryAndCwdStillRefusedWithAcceptLoss` (Review Focus 5: cwd and an ancestor of cwd), `TestDeleteDefaultBranchRowNeedsAcceptLoss`.
- [ ] Run → FAIL.
- [ ] Switch every consumer from `Status==in_use`/`len(Blockers)>0` to `row.HardStop != ""`; floors add an approval requirement, not a refusal.
- [ ] Run `go test ./internal/removal ./cmd/wt ./internal/rescue -count=1` → PASS; commit.

### Task 3: List the default branch (local head and remote ref)

**Files:** Modify `wt/internal/cleanup/snapshot.go` (`readRefs` :294-335, `isDefault` :372), `wt/internal/cleanup/rescan.go` (:166-178); Tests `snapshot_test.go`, `rescan_test.go` (:561-568), `classify_test.go` (:331), `removal/branch_test.go`.

- [ ] Write failing tests: `TestGatherListsTheDefaultBranchWhenNoWorktreeHoldsIt` (root switched to `feat`), `TestGatherDefaultBranchOnTheRootIsOneRow`, `TestRescanDefaultBranchBranchRowReturnsARow`, `TestDefaultBranchIsListedOnce`, `TestDeleteDefaultBranchRowNeedsAcceptLoss` (Engine Preview/Run without accept-loss is refused; with it, allowed), `TestNoDefaultKnownStillListsEveryRow` (Review Focus 1: `NoDefault` snapshot, detached root).
- [ ] Run → FAIL; stop skipping the default in `readRefs`/`Rescan`; default rows get the `default_branch` floor.
- [ ] Run → PASS; commit.

### Task 4: Fold a remote ref into its local row

**Files:** Modify `classify.go` (`remoteRow` :626, held map :252-263), `rescan.go` (:228-236), `cmd/wt/remove.go` (`resolveRemoveArgs` ~:195-232); Test `classify_test.go` (`findRow` :34 must search `RemoteRows()` for `KindRemote`), `remote_rows_test.go`.

**Interfaces:** Produces `Row.Upstream *Upstream` (`type Upstream struct{ Ref, Remote, Name, Tip string; Gone bool; Status Status; Evidence Evidence }`), `Report.Folded []Row`, `func (r Report) RemoteRows() []Row` (remote rows in `Rows` plus `Folded`).

- [ ] Write failing tests: `TestRemoteRefFoldsIntoSameNamedLocalBranch`, `TestFoldFollowsTheHeadsUpstreamRemote` (`fork/topic` vs `origin/topic`), `TestFoldFallsBackToOrigin`, `TestGoneUpstreamIsAFieldNotARow`, `TestFoldedWorktreeRowCarriesUpstream`, `TestFoldSlashAndCaseBranchNames` (Review Focus 3).
- [ ] Run → FAIL; implement; `wt remove origin/x` and `Rescan` still find folded rows through `RemoteRows()`.
- [ ] Run `go test ./internal/cleanup ./internal/removal ./cmd/wt -count=1` → PASS; commit.

### Task 5: JSON `hard_stop` and `upstream`, schema and fixtures

**Files:** Modify `cleanup/json.go` (`RowJSON` :83-96, `Encode` :156-171), `docs/contracts/cleanup-list.schema.json`, `cleanup-list.sample.json`, `cleanup-list.fetch-failed.sample.json`, `cleanup-list.no-origin.sample.json`, `cmd/wt/cleanup_contract_test.go`; Tests `json_test.go`, `cmd/wt/cleanup_test.go`.

**Interfaces:** Produces `RowJSON.HardStop`, `RowJSON.Upstream`, `UpstreamJSON`. New `Row` display fields (`Primary`, `Current`, `PRUnknown`, `PRTag`) do **not** serialize.

- [ ] Write failing tests: `TestEncodeHardStopAndUpstream`, `TestCleanupJSONUnchangedByNewRowFields` (update :359), contract tests require `hard_stop`/`upstream` in the fixture, `TestCleanupCmdJSONIncludesRootDefaultAndHeldRows` (real fixture).
- [ ] Run → FAIL; implement; update schema and all samples in the same commit.
- [ ] Run `go test ./internal/cleanup ./cmd/wt -count=1` → PASS; commit.

### Task 6: Deferred PR lookup and the PR-unknown floor

**Files:** Modify `cleanup/snapshot.go` (`Gather` :144-224, PR lookup :218-222), `cleanup/classify.go` (`local()` :546-596), `cleanup/rescan.go`, `cleanup/present.go`; Tests `pr_test.go`, `classify_test.go`, `present_test.go`, `rescan_test.go`.

**Interfaces:** Produces `GatherInputs.SkipPRs bool`, `Snapshot.PRsAsked bool`, `func FillPRs(s *Snapshot, in GatherInputs)`, `Row.PRUnknown bool`, `Row.PRTag *PRInfo`.

- [ ] Write failing tests: `TestGatherSkipPRsMakesNoGhCall`, `TestFillPRsMatchesAFullGather`, `TestPRUnknownIsNeverProvenWhenAsked` (git proof + no PR entry → not proven; Review Focus 2), `TestPRUnknownAppliesToWorktreeBranchAndOriginRows`, `TestRowTagsShowsPRQuestionMarkWhileUnknown`, `TestRescanKeepsPriorPRsAsked`.
- [ ] Run → FAIL; implement; with `PRsAsked` false the old behavior holds (non-interactive callers unchanged).
- [ ] Run → PASS; commit.

### Task 7: Shared words — `KindGroups` and `RowTags`

**Files:** Modify `cleanup/present.go`; Test `present_test.go`.

**Interfaces:** Produces `func KindGroups(rows []Row) []KindGroup` (headings WORKTREES, LOCAL BRANCHES, REMOTE BRANCHES; root first), `func RowTags(r Row) []Tag` (`root`, `you are here`, `held by session …`, `dirty`, `unpushed`, `upstream gone`, `PR #N merged|open|closed`, `PR ?`, `+remote`).

- [ ] Write failing `TestKindGroupsOrder`, `TestRowTagsEveryFact`; keep `TestEveryCodeHasWording` green.
- [ ] Run → FAIL; implement; run → PASS; commit.

### Task 8: One list component with launch and manage modes

**Files:** Modify `wt/internal/tui/cleanup_list.go` (`cleanupListModel` :63-97, `newCleanupList` :97-117, `cleanupLocked` :53-57), `cleanup_confirm.go`; Test `cleanup_list_test.go`.

**Interfaces:** Produces `func newRowList(rep cleanup.Report, o rowListOpts) cleanupListModel`, `type rowListOpts struct{ Mode listMode; ... }`, `listMode` (`modeManage`, `modeLaunch`), `rowChosenMsg{Row cleanup.Row}`, `newWorktreeChosenMsg{}`. Launch mode: first item `+ New worktree…`, Enter emits `rowChosenMsg`, `x` removes the highlighted row; manage mode keeps tick-and-Enter semantics. Both modes use `cleanup.KindGroups`.

- [ ] Write failing tests: `TestCleanupListGroupsByKind` (replaces `TestCleanupListGroupOrder` :83), `TestLaunchModeEnterChoosesTheRow`, `TestLaunchModeNewWorktreeRow`, `TestManageModeEnterStillRemovesTicked`, `TestXRemovesTickedInBothModes`; keep `TestCleanupListDefaultTicksOnlyProven` (:109), `...SpaceTogglesLocalRows` (:203) green.
- [ ] Run → FAIL; implement; run `go test ./internal/tui -count=1` → PASS; commit.

### Task 9: The picker is a view over cleanup rows

**Files:** Modify `wt/internal/tui/app.go` (`loadEntriesAtCmd` :1417-1426, `entriesLoadedMsg` :316-382, `selectedEntryMsg` :599, `isDefaultBranchOnly` :1482), `tui/worktree_list.go` (`buildList` :411-492 — delete the root drop :443, `SkipInPicker` calls :431/:456), `worktree/enumerate.go` (delete `SkipInPicker` :357, the `!used[b]` :404 and `localSet` :410-413 folds, fetch :24-35 moves to `worktree.FetchRemotes`), delete `worktree/prs.go` once unreferenced; Tests `tui/app_test.go`.

**Interfaces:** Produces `func entryOfRow(r cleanup.Row) worktree.Entry`, `func loadRowPRsCmd(...) tea.Cmd`, `rowPRsLoadedMsg{Report cleanup.Report}`, `func worktree.FetchRemotes(root string) error`. The picker opens with `cleanup.Gather{Fetch:false, SkipPRs:true}`, renders at once, and swaps in the filled report when `rowPRsLoadedMsg` arrives, keeping the cursor on the same row.

- [ ] Write failing tests: `TestPickerLoadsRowsFromCleanupAndRendersRootDefaultAndHeld`, `TestPickerStartsOnCurrentRow`, `TestPickerEnterOnRootRowLaunchesViaSelectedEntryMsg`, `TestPickerEnterOnBareBranchRowCreatesWorktree`, `TestPickerEnterOnRemoteOnlyRowUsesOriginForm`, `TestPickerPRFillArrivesAfterFirstPaint` (first `View()` has `PR ?`).
- [ ] Run → FAIL; implement; remove the filters; run `go test ./internal/tui ./internal/worktree -count=1` → PASS; commit.

### Task 10: The `wt worktrees` sub-command

**Files:** Modify `wt/cmd/wt/cleanup.go` (`executeCleanup` :131-163); Test `cmd/wt/cleanup_test.go` (:477, :574).

**Interfaces:** Produces cobra command `Use: "worktrees"`, `Aliases: []string{"cleanup"}`. Interactive TTY gathers with `Fetch:false, SkipPRs:true` and opens `newRowList(... modeManage)`; `--json` and `--plain` and non-TTY keep fetch-first unless `--no-fetch`.

- [ ] Write failing tests: `TestWorktreesCmdRegisteredWithCleanupAlias` (`root.Find` both names → same command), `TestWorktreesCmdTTYHandsOffToManageList`, `TestWorktreesCmdTTYDoesNotFetchOnOpen`, `TestWorktreesCmdJSONSameUnderBothNames` (byte-identical); `TestCleanupCmdContainsNoDeletion` (:574) stays green.
- [ ] Run → FAIL; implement; run `go test ./cmd/wt -count=1` → PASS; commit.

### Task 11: Docs and real-screen check

- [ ] Update `wt/docs/wt-cleanup.md`, `docs/guides/08-maintenance-and-troubleshooting.md` §9 for the new sub-command and the root/default rows; `CLAUDE.md` command line for `wt cleanup` names `wt worktrees`; run `make check-links`.
- [ ] Real-screen check at 80x24 via the pty driver: picker and `wt worktrees` against a scratch repo whose root is on a feature branch with a held and a merged-PR row; read each screen. Run `make test-all`. Open the PR (`Refs #N`).

---

# Part 2 — Warnings replace gates

Branch `wt-list-2-warnings`, off Part 1.

### Task 0: Re-read

- [ ] Grep `gateRoot|refuseRootBranch|confirmCreate|confirmSharedRoot|holdRoot|phaseSharedRoot|allow-shared-root|session-<pid>` and re-confirm the file list below against Part 1's tree.

### Task 1: `launchwarn.Collect` — the warning rules as a pure function

**Files:** Create `wt/internal/launchwarn/{launchwarn.go,check.go,target.go,testmain_test.go,launchwarn_test.go}`.

**Interfaces:** Produces `type Target struct{ Dir, Branch, Default, RootBranch string; IsRoot, RootChosen, UpstreamGone, Dirty bool; PRState string; PRNumber int; Holders []sessionclaim.Claim; Creation *Creation; Hint string }`, `type Warning struct{ Code, Text string }`, `func Collect(t Target) (warnings []Warning, info string)`. Codes: root, root_drift, held, dirty_held, default_branch, pr_merged, upstream_gone. Root warns unless `RootChosen`; default-branch warns only when `!IsRoot`; unknown PR gives no merged warning.

- [ ] Write failing tests: `TestCollectNoWarningsIsOneInfoLine`, `TestCollectRootWarns`, `TestCollectRootChosenSuppressesRootWarning` (drift still reported), `TestCollectHeld`, `TestCollectDefaultBranchOnlyWhenNotRoot`, `TestCollectPRMergedUpstreamGoneDirtyHeld`.
- [ ] Run `go test ./internal/launchwarn -count=1` → FAIL; implement; → PASS; commit.

### Task 2: `launchwarn.Check` — TTY vs non-TTY

**Files:** `launchwarn/check.go`; modify `wt/internal/rootclaim/rootclaim.go` (add `AskContinue`); Tests `launchwarn_test.go`, `rootclaim_test.go`.

**Interfaces:** Produces `func Check(in io.Reader, out io.Writer, isTTY bool, w []Warning, info string, creation *Creation) bool`, `func rootclaim.AskContinue(in io.Reader, out io.Writer) bool` (default yes: `""`, `y`, `Y` → true; `n`, `N`, Esc, Ctrl-C, EOF → false). No TTY: print warnings, return true, never read. TTY with warnings: one `[Y/n]`. TTY with no warnings or creation only: no prompt. Every line starts `wt: `.

- [ ] Write failing tests: `TestAskContinueDefaultsToYes`, `TestCheckNoTTYPrintsWarningsAndProceeds`, `TestCheckTTYWithWarningsPromptsOnce`, `TestCheckTTYNoWarningsDoesNotPrompt`, `TestCheckCreationOnlyDoesNotPrompt`, `TestCheckLinesStartWithWtPrefix`.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 3: `ForDir` and `ForNew` from live git and claims

**Files:** `launchwarn/target.go`; Test `launchwarn_test.go` (`gittest.Seed(t, SeedOpts{ExcludeWorktrees:true}).Clone(t)`, `filepath.EvalSymlinks` on the repo).

**Interfaces:** Produces `func ForDir(dir string) (Target, error)`, `func ForNew(repoRoot, branch string, plan *worktree.Plan) (Target, error)`; non-repo → empty target, nil error. Holders are live others only; an ancestor holding the root gives `RootChosen`.

- [ ] Write failing tests: `TestForDirRoot`, `TestForDirRootDrift`, `TestForDirLinkedWorktree`, `TestForDirNotARepo`, `TestForDirSubdirOfRootIsNotRoot`, `TestForDirSeesTheRootThroughASymlink`, `TestForDirHoldersAreLiveOthers`, `TestForDirNestedUnderAHeldRootHasNoHolders`.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 4: `internal/worktree` stops refusing the default branch

**Files:** Modify `wt/internal/worktree/create.go` (`refuseDefaultBranch` :51, :444, :494+); Test `create_test.go` (rewrite `TestEnsureForNameRefusesDefaultBranch` :683, `...RemoteForm` :729-845, `TestEnsureForBranchRefusesDefaultBranch` :846 and `...AnyRemote` :866).

**Interfaces:** Produces `func (p *Plan) Branch() string`, `Tracks() string`, `Path() string`, `type Created struct{ Branch, Tracks string; NewBranch bool }`; `EnsureForBranch` returns what it made. Git's own error stays for a branch already checked out elsewhere.

- [ ] Write failing tests: rewrites above (default branch gets `.worktrees/<db>` when root is on another branch; second call reuses), `TestPlanAccessors`, `TestEnsureForBranchDetail`, `TestEnsureForBranchAlreadyCheckedOutKeepsGitsError`.
- [ ] Run → FAIL; implement; run `go test ./internal/worktree -count=1` → PASS; commit.

### Task 5: Flag paths use `launchwarn`

**Files:** Delete `cmd/wt/rootgate.go`, `cmd/wt/sharedroot.go` (+ their tests' obsolete cases); Create `cmd/wt/launchwarn.go`; Modify `cmd/wt/main.go`, `cmd/wt/smoke.go`; Tests `rootflag_test.go` (:88), `sharedroot_test.go` (:358), `newflag_test.go` (:60, :76), `smoke_test.go` (:788, :816, :837).

**Interfaces:** Produces `main.confirmLaunch(...)`, `main.holdRoot(...)`. `-W <root branch>` launches in the root with a warning; `--root` consents to the root warning but not to a held warning; `-W <unknown>` without a terminal creates and says so; no `--allow-shared-root`.

- [ ] Write failing tests: `TestWorktreeFlagNamingTheRootBranchLaunchesWithWarning`, `TestNamedWorktreeLaunchDecidesOnTheResolvedDir`, `TestRootFlagIsConsentForTheRootWarningButNotForHeld`, `TestWorktreeFlagUnknownNameWithoutTerminalCreatesAndSaysSo`, `TestWorktreeFlagUnknownNameDoesNotAskOnATerminal`, `TestSmokeCmdCwdAtRootWarnsAndProceeds`; delete the rootgate tests (:29, :52, :62, :75, :86, :105, :119, :148) that Task 3 replaces.
- [ ] Run `go test ./cmd/wt -count=1` → FAIL; implement; → PASS; commit.

### Task 6: TUI — delete the shared-root screen; `launchGate` at the commit point

**Files:** Delete `internal/tui/sharedroot.go`; Create `internal/tui/launchgate.go`; Modify `tui/app.go`, `tui/new_worktree.go`, `tui/launch.go`, `tui/slot_flow.go`; move test helpers `rootRepo`, `linkedWorktree`, `liveOther`, `myClaim`, `claimExists`, `stubShellLaunch`, `pickRow` out of the deleted test file first.

**Interfaces:** Produces `func (m model) launchGate(t *launchwarn.Target) (ok bool)`, `type launchCreation`. Bare-branch and remote-only selections state "creates branch/worktree" in the info line; no `session-<pid>-<time>` branch is ever auto-named.

- [ ] Write failing tests: `TestLaunchGateRootNoOthersPrintsInfoAndClaims`, `TestLaunchGateLinkedWorktreeIsSilentAndUnclaimed`, `TestLaunchGateRootHeldWarnsAndContinues`, `TestLaunchGateNoQuitsQuietly`, `TestLaunchGateClaimFailureIsANote`.
- [ ] Run `go test ./internal/tui -count=1` → FAIL; implement; → PASS; commit.

### Task 7: Delete dead menu code

- [ ] Remove `rootclaim.Choose/ConfirmRoot/AskYesNo` and `sessionclaim` menu helpers and their tests (`TestChooseKeys`, `TestConfirmRootDefaultsToNo`, `TestAskYesNoDefaultsToNo`, `TestChooseAndConfirmRootPrintTheHint`, `TestMenuListsOthersAndKeys`, `TestNewWorktreeName`); keep `Others/Describe/CheckFailed`. `go build ./... && go vet ./... && go test ./internal/gitenv ./internal/rootclaim ./internal/sessionclaim -count=1` green (`TestEveryPackageThatRunsGitIsolatesIt` covers `launchwarn`); commit.

### Task 8: `FromRow` adapter

**Interfaces:** Produces `func launchwarn.FromRow(r cleanup.Row) *Target` (PR state/number from `Row.PRTag`, `Dirty` from losses, unknown PR → no merged warning, remote-only row → creation). `launchGate` takes the row's target when the pick came from the list.

- [ ] Write failing `TestFromRowMapsTheUnifiedRow`, `TestPickRowTargetCarriesPRAndUpstream`; implement; run → PASS; commit.

### Task 9: Docs

- [ ] Update `docs/guides` launch text, `wt/docs/internals/launch-flow.md`, `wt/CLAUDE.md`, `CLAUDE.md` (remove root-launch gating, `--allow-shared-root`, shared-root menu); add "superseded by" notes to `2026-11-04-wt-cleanup-design.md` and `2026-11-08-wt-restore-root-design.md`. Run `grep -rnE 'confirmSharedRoot|gateRoot|refuseRootBranch|allow-shared-root|session-<pid>' wt docs CLAUDE.md` → nothing outside history docs; `make check-links`; 80x24 real-screen check of a root launch warning; `make test-all`; open the PR.

---

# Part 3 — Remove automatic behaviors

Branch `wt-list-3-no-auto`, off Part 2.

### Task 0: Re-read

- [ ] Part 2 deleted `cmd/wt/sharedroot.go` and `internal/tui/sharedroot.go`: drop any step below that edits them. Part 1 already created `worktree.FetchRemotes`; skip Task 1's export if present.

### Task 1: Enumerate never fetches; `FetchRemotes` and `LastFetched`

**Files:** Modify `wt/internal/worktree/enumerate.go` (:24-35), `enumerate_test.go` (:437, ~:497, ~:543), `testmain_test.go`.

**Interfaces:** Produces `func FetchRemotes(root string) error` (`git fetch --all --prune`, 5s, one-line error), `func LastFetched(root string) (time.Time, bool)` (reads `FETCH_HEAD` mtime).

- [ ] Write failing tests: rename `TestEnumerateFetchesNewRemoteBranches` → `TestEnumerateDoesNotFetch`, `TestEnumeratePrunesDeletedRemoteBranches` → `TestEnumerateDoesNotPrune`; add `TestFetchRemotesFetchesNewAndPrunesDeleted`, `TestFetchRemotesReportsFailure`, `TestLastFetched`.
- [ ] Run `go test ./internal/worktree -count=1` → FAIL; implement; → PASS; commit.

### Task 2: List refresh key `F` and last-refreshed note

**Files:** Modify `internal/tui/app.go`, `internal/tui/worktree_list.go`, `internal/tui/cleanup_list.go`, `cleanup_confirm.go` (`RemoveFuncs.Refetch`); Tests `tui/app_test.go`, `cleanup_list_test.go`.

**Interfaces:** Produces `RemoveFuncs.Refetch func() (cleanup.Report, error)`; header text `refreshed 3m ago` / `not refreshed here`; refresh failure shows a one-line note and keeps the old list.

- [ ] Write failing tests: `TestPickerOpenDoesNotFetch` (counting `fetchRemotesFn`), `TestPickerFKeyRefreshes`, `TestPickerFKeyIgnoredWhileTyping`, `TestPickerFKeyIgnoredWhileRefreshing`, `TestPickerRefreshFailureShowsNote`, `TestPickerHeaderShowsLastRefreshed`, `TestCleanupListFKeyRefetches`, `TestCleanupListRKeyStillRescues`.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 3: `wt worktrees`: no fetch on open, `--fetch`

**Files:** Modify `cmd/wt/cleanup.go`; Test `cmd/wt/cleanup_test.go`.

- [ ] Write failing tests: `TestCleanupCmdTerminalDoesNotFetchOnOpen`, `TestCleanupCmdTerminalFetchFlagFetches`, `TestCleanupCmdJSONStillFetchesByDefault`, `TestCleanupCmdPlainStillFetchesByDefault`, `TestCleanupCmdFetchAndNoFetchConflict` (usage error naming both), `TestCleanupRemoveFuncsRefetchFetches`.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 4: Remove the exit prompt

**Files:** Delete `cmd/wt/exitprompt.go`, `cmd/wt/exitprompt_test.go`, `internal/exitprompt/`, `internal/tui/exitprompt_test.go`; Modify `cmd/wt/app.go`, `cmd/wt/launch.go`, `cmd/wt/slot_launch.go`, `internal/tui/{survey,launch,slot_flow,app,new_worktree}.go`, `internal/cleanup/losses_guard_test.go` (drop the `exitprompt.Eligible` allow-list entry); Tests `cmd/wt/newflag_test.go`.

- [ ] Before deleting, copy the tail-order test pattern from `exitprompt_test.go:45` into a surviving test: `TestTailsRunStopPickerThenSummary` (no removal offer, no `gh` call). Run → PASS on current code order minus prompt (FAIL until removed).
- [ ] Delete code and tests; `go vet ./... && go build ./...`; grep for now-dead exported helpers the compiler will not flag (creation-route records, `exit_prompt` accessors); `go test ./... -count=1` → PASS; commit.

### Task 5: Tolerate and ignore `[launch] exit_prompt`

**Files:** Modify `internal/config/launch.go`, `internal/config/config.go`, `cmd/wt/app.go`; Tests `config/launch_test.go`, `cmd/wt/app_test.go`.

**Interfaces:** Produces `func IgnoredLaunchKeys() []string` (`["[launch] exit_prompt"]` when the key has a non-empty value), `func warnIgnoredLaunchKeys(w io.Writer, cfg ...)` printing one note per process; the key survives a config save.

- [ ] Write failing tests: `TestLaunchExitPromptAnyValueIsTolerated`, `TestIgnoredLaunchKeys`, `TestLaunchTableSurvivesASave`, `TestWarnIgnoredLaunchKeysPrintsOnce`, `TestNewAppWarnsAboutLegacyExitPrompt`; delete `TestLaunchExitPromptDefaultsToAsk`.
- [ ] Run `go test ./internal/config ./cmd/wt -count=1` → FAIL; implement; → PASS; commit.

### Task 6: Hook install is announced, never silent

**Files:** Modify `internal/guard/guard.go`, `cmd/wt/main.go` (:168, :594), `cmd/wt/helpers.go`; Tests `guard_test.go`, `cmd/wt/helpers_test.go`, `main_test.go`.

**Interfaces:** Produces an `EnsureOnLaunch` result telling whether it installed (first time) or found it present; stderr prints `wt: installed the block-main-commit hook (--no-guard to skip)` once at install; a present hook is left untouched and silent. `--no-guard` and `WT_SKIP_MAIN_BLOCK=1` unchanged.

- [ ] Write failing tests: `TestEnsureOnLaunchInstallsOnceAndReportsIt`, `TestEnsureOnLaunchLeavesAnExistingHookAlone`, `TestMaybeInstallGuardAnnouncesTheInstall`, `TestNoGuardFlagStillSkips`.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 7: Docs, CHANGELOG, CLAUDE.md

- [ ] `grep -rniE 'exit_prompt|exitprompt|exit prompt|fetches before listing|fetch --all --prune' docs wt --include='*.md'` shows nothing stale; update `wt/CLAUDE.md`, `wt/docs/internals/tui.md`, `wt/CHANGELOG.md`; `make check-links`; `make test-all`; PR.

---

# Part 4 — Claims for every launch, root drift, `root_held` as a warning

Branch `wt-list-4-claims`, off Part 3.

### Task 0: Re-read

- [ ] Part 2's `launchGate` already claims root launches; this Part generalizes it. Edits to `cmd/wt/sharedroot.go` / `tui/sharedroot.go` do not apply (deleted). Reuse `launchwarn` instead of adding a second warning path; `launchwarn.ForDir` should end up calling `rootclaim.Drift` (Task 11).

### Task 1: `sessionclaim.Hold`

**Files:** Modify `wt/internal/sessionclaim/sessionclaim.go`; Test `sessionclaim_test.go` (fake process table as existing tests).

**Interfaces:** Produces `func Hold(commonDir string, c Claim) (release func(), err error)`: writes `wt-sessions/<pid>` with pid, process start time and path, prunes dead and pid-reused claims first, release removes only its own file and is safe twice.

- [ ] Write failing tests: `TestHoldWritesClaimWithoutAsking`, `TestHoldPrunesStaleBeforeWriting` (dead pid; live pid with start time differing > 5s), `TestHoldReleaseRemovesOwnFileOnly`, `TestHoldUnwritableDirReturnsErrorNotPanic`.
- [ ] Run `go test ./internal/sessionclaim -count=1` → FAIL; implement; → PASS; commit.

### Task 2: `sessionclaim.Holders` (read-only)

**Interfaces:** Produces `func Holders(commonDir string, self Claim) (map[string][]Claim, error)` keyed by worktree path, excluding self and ancestors and reused pids; `func DescribeAll(cs []Claim, now time.Time) string`.

- [ ] Write failing tests: `TestHoldersGroupsLiveClaimsByPath`, `TestHoldersExcludesSelfAndAncestors`, `TestHoldersReusedPidIsNotAHolder` (Review Focus 4, 5s boundary), `TestHoldersIsReadOnly` (no file changes, stale files remain), `TestHoldersNoDirIsEmptyNil`, `TestHoldersMalformedIgnored`.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 3: `sessionclaim.Prune`

**Interfaces:** Produces `func Prune(commonDir string) (removed int, err error)` taking the directory lock; keeps live and unknown-start-time claims.

- [ ] Write failing tests: `TestPruneRemovesDeadAndReusedKeepsLiveAndUnknown`, `TestPruneTakesTheLock`, `TestPruneMissingDirIsZeroNil`; run → FAIL; implement; → PASS; commit.

### Task 4: `worktree.ToplevelOf`, `rootclaim.LocateSite`, `HoldLaunch`

**Interfaces:** Produces `func worktree.ToplevelOf(dir string) (string, error)`, `type rootclaim.Site struct{ Path, CommonDir, Branch string; IsRoot bool }`, `func rootclaim.LocateSite(dir string) (Site, error)`, `func rootclaim.HoldLaunch(dir, agent string) (release func(), others []sessionclaim.Claim, err error)` (others = live claims on the same path only; not-a-repo → no-op).

- [ ] Write failing tests: `TestToplevelOfRootLinkedSubdirAndSymlink`, `TestLocateSiteLinkedWorktree`, `TestLocateSiteRootMatchesLookup`, `TestHoldLaunchWritesClaimKeyedByWorktreePath`, `TestHoldLaunchReportsOthersOnSamePathOnly`, `TestHoldLaunchNotARepoIsNoop`.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 5: Every launch path claims, after the worktree exists

**Files:** Modify `cmd/wt/launch.go`, `cmd/wt/slot_launch.go`, `internal/tui/launch.go`, and the Part 2 `launchGate`; Tests `cmd/wt/launch_test.go`, `cmd/wt/slot_launch_test.go`, `internal/tui/launch_test.go`.

- [ ] Write failing tests: `TestLaunchInLinkedWorktreeWritesClaim`, `TestLaunchBareBranchClaimsTheCreatedWorktree` (path is the new worktree, never the root), `TestLaunchRootWritesClaimAndDoesNotBlockOnAnother` (exit 0, one-line warning naming the holder), `TestPickerLaunchClaimsPickedWorktree`, `TestSlotLaunchWritesClaim`.
- [ ] Run → FAIL; implement with `HoldLaunch` after worktree creation; release at agent exit; → PASS; commit.

### Task 6: Cleanup — held is a label, not a blocker

**Files:** Modify `cleanup/worktrees.go` (:29, :198), `cleanup/snapshot.go`, `cleanup/classify.go`, `cleanup/present.go`, `cleanup/json.go`; Tests `worktrees_test.go` (drop the `{"held",...}` case :125-127), `snapshot_test.go` (:52, :93-95), `json_test.go`.

**Interfaces:** Produces `cleanup.ReasonHeld`, `func (s Snapshot) HeldOn(path string) []Claim`. Held worktrees read `less_sure` (floor), reason carries the claim description; dirty + held stays `unique_work` with both reasons; `blockers` never contains `held`.

- [ ] Write failing tests: `TestHeldLabelOnLinkedWorktree`, `TestHeldWorktreeNeverProven`, `TestHeldDoesNotMaskUniqueWork`, rewritten `TestGatherReadsClaimsReadOnly`, JSON test for reason code `held`.
- [ ] Run → FAIL; implement (reads claims with `Holders`, never `Join`/`Prune`); → PASS; update schema/samples if enums change; commit.

### Task 7: `rescue` keeps its held warning

- [ ] Write failing `TestPreflightHeldWarns` (`rescue/preflight_test.go`); update hand-built rows using `BlockHeld` in `rescue` and `removal` tests to the `ReasonHeld` form; run `go test ./internal/rescue ./internal/removal -count=1` → PASS; commit.

### Task 8: `restore-root` preflight — `root_held` becomes a warning

**Files:** Modify `rootmove/types.go` (remove `CodeRootHeld`), `rootmove/preflight.go` (:36, :103), `rootmove/json.go`; Tests `preflight_test.go` (:164-168, :320), `json_test.go` (:72, :81).

**Interfaces:** Produces `rootmove.HeldWarningPrefix`; the plan carries a warning naming the holder pid; a claim on a linked worktree path never warns about the root.

- [ ] Write failing tests: `TestPreflightHeldWarnsAndPlans`, `TestPreflightHeldFromLinkedWorktreeClaimIsIgnored`; update `TestCodesCoverEveryCodeRootmoveEmits`.
- [ ] Run `go test ./internal/rootmove -count=1` → FAIL; implement; → PASS; commit.

### Task 9: `restore-root` Execute drops claim re-checks

- [ ] Rewrite `execute_test.go` :466-490 "live claim" case to expect the move proceeds; delete subtests "claims unreadable" (:491-515) and the claim rechecks in `TestExecuteStartRecheck` (:915-990); add `TestExecuteIgnoresClaimsEntirely` (chmod-0 `wt-sessions`); remove the `held` row (:381-386) from the exit-code table in `cmd/wt/restore_root_test.go`. Run → FAIL then PASS after removing the checks in `rootmove/execute.go`; commit.

### Task 10: `restore-root` command and contract

**Files:** Modify `cmd/wt/restore_root.go`, `docs/contracts/restore-root-result.schema.json`, create `docs/contracts/restore-root-result.held.sample.json`, `cmd/wt/restore_root_contract_test.go`.

- [ ] Write failing tests: `TestRestoreRootCmdHeldRootWarnsAndMoves` (`--yes --json --no-fetch` → exit 0, outcome moved, warning present), `TestRestoreRootCmdHeldRootPromptNamesHolder`, `TestRestoreRootCmdHeldRootNoTerminalNoYesIsExit3`, `TestRestoreRootCmdHeldRootDryRunReportsWarning`; contract test adds the held sample.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 11: `rootclaim.Drift` and the root row's drift reason

**Interfaces:** Produces `func rootclaim.Drift(s Site, defaultBranch string) string` (`root is on topic, not main`; `""` on default; detached HEAD reads `root is detached`; empty when no default known), `cleanup.ReasonRootDrift`. `launchwarn.ForDir` calls `Drift`. Always computed live.

- [ ] Write failing tests: `TestDriftTable`, `TestDriftIsLive` (two reads around a branch switch), `TestRootRowCarriesDriftReason`, `TestRootDriftNotEmittedWithoutDefault` (Review Focus 1), `TestRescanRereadsRootDrift`.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 12: Drift badge, restore-root action, launch header note

**Files:** Modify `internal/tui/cleanup_list.go`, `internal/tui/worktree_list.go`, `cmd/wt/launch.go`.

- [ ] Write failing tests: `TestRootRowShowsDriftBadge`, `TestPickerHeaderShowsRootDrift`, `TestLaunchPrintsRootDriftNote` (plain `-W existing` launch while root is on `topic` prints `wt: note: root is on topic, not main — wt restore-root`).
- [ ] Run → FAIL; implement; → PASS; commit. (The `R` action itself lands in Part 5.)

### Task 13: Prune stale claims on refresh only

- [ ] Write failing tests: `TestRefreshPrunesStaleClaims` (`F` in the list), `TestOpeningTheListPrunesNothing` (anti-goal), `TestCleanupJSONFetchPrunesStaleClaims`, `TestCleanupJSONNoFetchLeavesClaims`. Implement by calling `sessionclaim.Prune` from the refresh path and from fetch-first JSON; run → PASS; commit.

### Task 14: Contract doc, guides, CLAUDE.md

- [ ] Update `docs/contracts/session-claims.md`, `wt/docs/wt-cleanup.md`, `wt/docs/internals/{packages,launch-flow}.md`, `wt/CLAUDE.md`, `CLAUDE.md`, guide 08 §9. Verify `grep -rn 'BlockHeld' wt` and `grep -rn 'root_held' wt docs CLAUDE.md` are limited to the held sample, `HeldWarningPrefix`, schema description and explanatory docs; contract tests pass; `make check-links`; 80x24 check of the drift badge; `make test-all`; PR.

---

# Part 5 — Actions in the list and docs

Branch `wt-list-5-actions`, off Part 4.

### Task 0: Re-read

- [ ] `go build ./... && go test ./internal/tui ./internal/cleanup ./internal/removal ./internal/rescue ./internal/rootmove ./cmd/wt -count=1`; confirm the names Part 1 produced (`Row.HardStop`, `Row.Upstream`, `newRowList`, `rowChosenMsg`) before using them.

### Task 1: `rootmove.Offered`

**Files:** Create `wt/internal/rootmove/offer.go`, `offer_test.go`.

**Interfaces:** Produces `func Offered(row cleanup.Row) (bool, string)`: root on a non-default branch or detached → `(true, "")`; root on default → `(false, "root is already on main")`; any non-root row → `(false, "only the root can be restored")`. Reads the row, not git.

- [ ] Write failing tests: `TestOfferedRootOnFeatureBranch`, `TestOfferedRootOnDefaultSaysAlreadyThere`, `TestOfferedDetachedRoot`, `TestOfferedNonRootRowsRefused`, `TestOfferedUsesRowNotGit`; run → FAIL; implement; → PASS; commit.

### Task 2: List key `R` restores the root

**Files:** Modify `tui/cleanup_list.go`, `cleanup_confirm.go`.

**Interfaces:** Produces `RemoveFuncs.RestoreRoot func() error`, `cleanupListModel.restoreRoot bool`, `func runRestoreRoot(fm cleanupListModel, rm RemoveFuncs, stop func()) error` (hand-off after leaving the alt screen, like rescue; signal forwarding stopped first).

- [ ] Write failing tests: `TestCleanupListRestoreRootKeyHandsOff`, `TestCleanupListRestoreRootExplainsWhenNotOffered`, `TestCleanupListRestoreRootNotWired`, `TestRunCleanupListRestoresRootAfterTheScreen`, `TestRunRestoreRootStopsSignalForwardingFirst`.
- [ ] Run `go test ./internal/tui -count=1` → FAIL; implement; → PASS; commit.

### Task 3: Wire `RestoreRoot` in `cmd/wt`, end to end

**Files:** Modify `cmd/wt/cleanup.go` (:177-180 area: wired even when the registry is off), `cmd/wt/restore_root.go`; Create `cmd/wt/cleanup_restoreroot_test.go`.

- [ ] Write failing tests: `TestCleanupRestoreRootFuncMovesTheRoot` (template-seeded repo via the gittest kit as `restore_root_test.go` does), `TestCleanupRemoveFuncsRestoreRootWiredEvenWhenRegistryOff`; run → FAIL; implement; → PASS; commit.

### Task 4: List key `F` refreshes in place

**Interfaces:** Produces `RemoveFuncs.Fetch func() (cleanup.Report, error)` (shared with Part 3's `Refetch`; reconcile to one field name), `func (m cleanupListModel) startRefresh() (cleanupListModel, tea.Cmd)`.

- [ ] Write failing tests: `TestCleanupListFetchKeyRefreshes`, `TestCleanupListFetchFailureKeepsOldListAndSaysWhy`, `TestCleanupListFetchNotWired`, `TestCleanupListTitleLineNamesAllActions` (100x30 shows `x remove`, `R restore root`, `F refresh`, `r rescue`; an 80x24 variant fits), `TestCleanupListFetchKeepsCursorOnSameRow`.
- [ ] Run → FAIL; implement (skip if Part 3 already did; then only add the title-line test); → PASS; commit.

### Task 5: Per-row removal keys `x`, `d`, `o`

**Interfaces:** Produces `type rowAction int`, `func (m cleanupListModel) startRowAction(kind rowAction) (cleanupListModel, tea.Cmd)`. `x` previews only the highlighted row (ticks ignored); `d` on a worktree row seeds `delBranch=true`; `d` on a branch row equals `x`; `o` deletes only the origin ref and needs typed `delete origin`.

- [ ] Write failing tests: `TestCleanupListXPreviewsOnlyTheHighlightedRow`, `TestCleanupListXOnProvenWorktreeAcceptsY`, `TestCleanupListXOnDirtyWorktreeNeedsTypedDelete`, `TestCleanupListDOnWorktreeRowSeedsDeleteBranch`, `TestCleanupListDOnBranchRowIsRemove`, `TestCleanupListOOnFoldedRowDeletesOnlyTheOriginRef`.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 6: Lock only on hard stops

**Files:** Modify `tui/cleanup_list.go` (`cleanupLocked` :53-57); Test `cleanup_list_test.go` (:134, :376).

- [ ] Write failing `TestCleanupLockedOnlyForHardStops` (primary, current, locked, mid-operation, nested, missing dir locked; default branch, held, root-drift not locked), `TestCleanupListRemoveOnCwdOrAncestorIsRefused` (Review Focus 5); rewrite `TestCleanupListInUseRowsNotToggleable` (:134) to hard-stop rows only; keep `TestCleanupListDefaultTicksOnlyProven` (:109) and `...AllTicksOnlyProven` (:224) unchanged.
- [ ] Run → FAIL; implement; → PASS; commit.

### Task 7: Confirmation simplification pin

- [ ] Keep `TestConfirmProvenOnlyAcceptsY` (:353) and `TestConfirmUnprovenRequiresTypedDelete` (:382) green; add `TestConfirmAnyApprovalKindNeedsPlainDelete` (needs_confirm, needs_force, needs_delete entries all accept `delete`), `TestConfirmOriginStillNeedsDeleteOrigin`, `TestConfirmNamesTheHoldingSession` (`held by session <id>` line); add a held-row screen and a single-row-action screen to `confirmScreens` (:766) so the 80x24 test (:819) covers them. Run `go test ./internal/tui -count=1` → PASS; commit.

### Task 8: Re-enter the list after rescue and restore-root

- [ ] Write failing tests: `TestRunCleanupListReentersAfterRescue`, `TestRunCleanupListReentersAfterRestoreRoot`, `TestRunCleanupListStopsLoopingOnActionError`, `TestRunCleanupListPrintsLogOncePerSession` (swap `runCleanupProgram` as `TestRunCleanupListStopsForwardingThenRescues` does); implement the loop; → PASS; commit.

### Task 9: The launch selector offers the same actions

- [ ] Write failing tests: `TestLaunchSelectorPassesRemoveFuncs`, `TestLaunchSelectorEnterStillLaunchesRow`, `TestLaunchSelectorActionKeysNotCapturedByFilter` (typing `x` into the filter box filters; action keys act only outside it). Implement by passing the same `RemoveFuncs` into `newRowList(..., modeLaunch)`; run `go test ./internal/tui ./cmd/wt -count=1` → PASS; commit.

### Task 10: Real-screen check (manual, no committed code)

- [ ] With the pty driver at 80x24, capture and read: (1) drifted root with badge and `R` offered, (2) dirty worktree row after `x` showing the typed-`delete` prompt, (3) held row confirm naming the session, (4) launch selector with root, default and held rows, (5) refresh failure note. Fix anything truncated.

### Task 11: Docs and superseded notes

- [ ] Update guide 08 §9 (list keys, hard stops vs floors, no auto cleanup), `wt/docs/wt-cleanup.md`, `wt/docs/internals/testing.md`, `CLAUDE.md` and `wt/CLAUDE.md` command lines; mark the superseded sections of `2026-11-04-wt-cleanup-design.md` and `2026-11-08-wt-restore-root-design.md`; no live counts or snapshots in the guide. `make check-links`, `make test-all`, PR.
- [ ] Outside the repo (note for the user, do not edit from here): `~/.claude/skills/repo-cleanup/SKILL.md` should call `wt worktrees --json` and stop switching the root checkout itself; tell the user which lines.
