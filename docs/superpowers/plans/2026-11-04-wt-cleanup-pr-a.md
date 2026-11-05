# `wt cleanup` PR A — read-only classifier, list and `--json` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `wt cleanup`, which classifies every worktree, local branch and `origin` branch with its proof and shows it as a grouped list and as `--json` — and contains no code that deletes anything.

**Architecture:** A new package `wt/internal/cleanup` holds a pure classifier: `Gather` builds one `Snapshot` of facts, `Classify` turns it into rows with a status, an evidence name and reasons. Git is run through a new exit-code-aware runner (the existing `internal/worktree.runGit` has no `-C`, timeout or exit code). A view-only bubbletea list in `internal/tui` and a `--json` envelope in `cmd/wt` render the same `Report`. PRs B–D later add removal, `origin` removal and move-to-worktree on top of this package.

**Tech Stack:** Go 1.26 (module root `wt/`), git plumbing, `gh`, bubbletea/lipgloss (already used by `internal/tui`), cobra, JSON Schema + sample fixture in `docs/contracts/`.

**Spec:** `docs/superpowers/specs/2026-11-04-wt-cleanup-design.md` (read it first; "Details settled at review" decides the row model, status precedence, "pushed", PR query fields, exit codes). Carried details live in `docs/superpowers/specs/2026-11-04-worktrunk-ports-design.md`.

## Global Constraints

- TDD with red-green-revert: no production code without a failing test watched first; revert the change once and see the test fail again. Fresh worktree per PR.
- **This PR contains no deletion, no `git worktree remove`, no `update-ref -d`, no `branch -d/-D`, no `push --delete`.** A review check greps for them.
- Any `docs/contracts/` fixture change rides in the same change as the code that reads or writes it, held by a Go contract test (pattern: `docs/contracts/model-list.sample.json` + `cmd/wt/model_list_contract_test.go`).
- Guides document the command, never live state (no counts or snapshots in `docs/guides/`). A user-visible change updates `wt/CLAUDE.md` and the matching `wt/docs/wt-agents/*.md` page in the same PR.
- `wt` Go tests: a top-level `//` what-and-why comment on every `Test*`; a test that creates worktrees builds a fresh repo per case; branch name `topic`, not `feature`, when a case needs `feature/<x>`; `gitenv.IsolateForTest()` in `TestMain`; `gh` is stubbed (a real call from a test binary must fail, as `internal/worktree.realListPRs` does).
- Fail closed: any git error, unresolved default branch, or unknown PR state yields `Unknown`, never `Merged`/`proven`. Exit code 1 is exactly "could not classify at all" (spec, "Exit codes").
- This repo squash-merges PRs; PR bodies never contain `close`/`fix`/`resolve` followed by `#N` — write `Refs #N`.
- Commits end `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`; PR bodies end `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- Verification before any commit: `go test ./...`, `go vet ./...`, `make check` in `wt/`; `make test-all` at the repo root before the PR; `make check-links` whenever a `.md` changes.

## Review Focus

Most likely to bite first. Each line names the test that pins it.

1. A branch squash-merged and then **reverted on main** must not read as proven merged (`TestProveSquashThenRevertIsNotMerged`, Task 3).
2. `gh` missing, failing, or timing out must show `unknown`, never "no PR" (`TestPRLookupFailureIsUnknown`, Task 5; `TestClassifyUnknownPRIsNeverProven`, Task 6).
3. An unresolved default branch must make every row unknown, not silently skip guards (`TestClassifyNoDefaultBranchAllUnknown`, Task 6; `TestCleanupCmdExit1WithoutDefaultBranch`, Task 8).
4. Ignored files (`.env`, `.superpowers/`) and untracked files in an otherwise merged worktree must demote it (`TestGatesIgnoredFilesDemoteToLessSure`, `TestClassifyDirtyMergedIsUniqueWork`, Tasks 4/6).
5. A brand-new branch sitting on the base, and a `feature/x` / `feature-x` flatten collision, must not classify one thing and show another (`TestClassifyFreshBranchNotProven`, `TestRowsMapWorktreeToBranchByPorcelain`, Task 6).

---

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/cleanup/git.go` | `Git` runner: `-C`, timeout, stdout, exit code |
| `wt/internal/cleanup/base.go` | default-branch resolution, base ref, `Fetch` |
| `wt/internal/cleanup/ladder.go` | the evidence ladder and the weak (non-proof) signals |
| `wt/internal/cleanup/worktrees.go` | porcelain parse, dirty / ignored / lock / nested / op-in-progress gates |
| `wt/internal/cleanup/pr.go` | per-branch PR lookup, tri-state, `gh` seam |
| `wt/internal/cleanup/snapshot.go` | `Snapshot` and `Gather` |
| `wt/internal/cleanup/classify.go` | statuses, precedence, row model, min-age, reasons |
| `wt/internal/cleanup/json.go` | `Envelope` and encoding |
| `wt/internal/cleanup/*_test.go`, `testmain_test.go`, `fixture_test.go` | tests, real-git fixture builder, `TestMain` |
| `wt/internal/tui/cleanup_list.go` (+ test) | view-only grouped list |
| `wt/cmd/wt/cleanup.go` (+ tests, `cleanup_contract_test.go`) | the command |
| `docs/contracts/cleanup-list.sample.json`, `cleanup-list.schema.json` | contract fixture and published schema |

---

### Task 1: Git runner and real-repo fixture

**Files:**
- Create: `wt/internal/cleanup/git.go`, `git_test.go`, `testmain_test.go`, `fixture_test.go`

**Interfaces:**
- Produces:
  - `type Git struct { Dir string; Timeout time.Duration }` (zero `Timeout` means 30s)
  - `type Out struct { Stdout string; Stderr string; Code int }`
  - `func (g Git) Run(args ...string) (Out, error)` — runs `git -C g.Dir args...`; a non-zero exit is **not** an error (`Code` carries it); `error` is only for spawn failure or timeout.
  - test-only `type fixture struct{...}` with `newFixture(t) *fixture` (bare "origin" + clone, `main` pushed, `origin/HEAD` set), and methods `Commit(file, content, msg) string` (returns sha), `Branch(name)`, `Checkout(name)`, `Push(ref)`, `Squash(branch)` (squash-merge `branch` into `main` as one commit and push), `Revert(sha)`, `Worktree(branch) string`, `Dirty(path)`, `Ignored(path)`.

- [ ] **Step 1: Write `TestRunReportsExitCodeWithoutError`** — `git merge-base --is-ancestor <a> <b>` on a non-ancestor returns `Out.Code == 1` and `err == nil`; a bad revision returns `Code == 128`, `err == nil`.
- [ ] **Step 2: Write `TestRunTimeoutIsAnError`** — `Git{Timeout: 50*time.Millisecond}` running a `git` alias `!sleep 5` returns a non-nil error.
- [ ] **Step 3:** Run `go test ./internal/cleanup -run 'TestRun' -v`; expect FAIL (package does not compile).
- [ ] **Step 4: Implement `Git.Run`** with `exec.CommandContext`, `cmd.Dir = g.Dir`, separate stdout/stderr buffers; extract the code with `errors.As(*exec.ExitError)`.
- [ ] **Step 5: Implement `fixture`** in `fixture_test.go`; `TestMain` calls `gitenv.IsolateForTest()`; no `gh` seam yet (Task 5 adds it).
- [ ] **Step 6:** Run the tests; expect PASS. Revert `Run`'s exit-code extraction, see `TestRunReportsExitCodeWithoutError` fail, restore.
- [ ] **Step 7: Commit** `feat(wt): cleanup git runner and test fixture`.

---

### Task 2: Default branch, base ref, fetch

**Files:**
- Create: `wt/internal/cleanup/base.go`, `base_test.go`

**Interfaces:**
- Consumes: `Git`.
- Produces:
  - `type Base struct { Default string; Ref string; Fallback bool; FetchedAt time.Time }` — `Ref` is `origin/<Default>` when it exists, else the local default with `Fallback: true`.
  - `func ResolveBase(g Git) (Base, error)` — chain: `refs/remotes/origin/HEAD`, local `main`, `master`, then `origin/main`, `origin/master`. Returns `ErrNoDefaultBranch` when none resolves.
  - `type FetchResult struct { OK bool; Err string; At time.Time }`; `func Fetch(g Git) FetchResult` — `fetch --prune` with `g.Timeout`; failure is a value, never swallowed.
  - `var ErrNoDefaultBranch = errors.New(...)`

- [ ] **Step 1: Write tests** (each in a fresh `newFixture`):
  - `TestResolveBaseUsesOriginHEAD` — `Default == "main"`, `Ref == "origin/main"`, `Fallback == false`.
  - `TestResolveBaseFallbackChain` — delete `origin/HEAD`: still `main`; rename local and remote to `master`: resolves `master`.
  - `TestResolveBaseLocalOnlyIsFallback` — no remote: `Ref == "main"`, `Fallback == true`.
  - `TestResolveBaseNoneIsErrNoDefaultBranch` — a repo whose only branch is `topic` returns `ErrNoDefaultBranch`.
  - `TestFetchFailureIsReported` — point `origin` at a missing path; `FetchResult.OK == false`, `Err != ""`.
- [ ] **Step 2:** Run; expect FAIL.
- [ ] **Step 3: Implement** `ResolveBase` and `Fetch` per the signatures; the chain order is the spec's.
- [ ] **Step 4:** Run; expect PASS; revert-cycle `TestResolveBaseFallbackChain`.
- [ ] **Step 5: Commit** `feat(wt): cleanup base resolution`.

---

### Task 3: Evidence ladder

**Files:**
- Create: `wt/internal/cleanup/ladder.go`, `ladder_test.go`

**Interfaces:**
- Consumes: `Git`, `Base`.
- Produces:
  - `type Evidence string` with constants `EvSameCommit`, `EvAncestor`, `EvTreesMatch`, `EvNoAddedChanges`, `EvMergeAddsNothing` (proofs); `EvPRTip` (set in Task 6); `EvCherryEquivalent`, `EvPatchID` (weak, never proofs).
  - `type Verdict int` with `NotMerged`, `Merged`, `Undetermined`.
  - `func Prove(g Git, base, tip string) (Evidence, Verdict)` — rungs in the spec's order, first proof wins. `Undetermined` when a rung returns an unexpected exit code or a git error; the ladder does not continue past an error to a later rung as if the earlier had said "no".
  - `func WeakSignals(g Git, base, tip string) []Evidence` — `git cherry` all `-`, and whole-diff patch-id match (walk capped at 500 commits).

- [ ] **Step 1: Write the table test `TestProveLadder`** over fixture scenarios (one subtest each, fresh repo), expecting `(Evidence, Verdict)`:
  - true merge → `(EvAncestor, Merged)`; fast-forward → `(EvAncestor, Merged)`
  - 1:1 rebase-merge (each commit re-applied on main) → `(EvMergeAddsNothing, Merged)`
  - multi-commit squash with main advanced afterwards → `(EvMergeAddsNothing, Merged)`
  - branch with a commit main lacks → `(_, NotMerged)`
  - merge commit with unique conflict-resolution content → `NotMerged`
- [ ] **Step 2: Write `TestProveSquashThenRevertIsNotMerged`** — squash `topic` into main, then `Revert` it on main: `Verdict == NotMerged` **and** `WeakSignals` contains `EvCherryEquivalent` (proving cherry alone would have lied).
- [ ] **Step 3: Write `TestProveGitErrorIsUndetermined`** — `tip` is a nonexistent ref: `Undetermined`.
- [ ] **Step 4: Write `TestProveMergeTreeExitCodes`** — conflicting branch: `NotMerged` (exit 1), not `Undetermined`.
- [ ] **Step 5:** Run; expect FAIL.
- [ ] **Step 6: Implement `Prove` and `WeakSignals`.** `merge-tree --write-tree <base> <tip>`: exit 0 and line 1 equals `<base>^{tree}` → `EvMergeAddsNothing`; exit 1 → conflict, `NotMerged`; any other code → `Undetermined`. Same-commit/ancestor use exit codes 0/1, anything else `Undetermined`. Walk cap 500 → `Undetermined` for patch-id only (weak signal omitted).
- [ ] **Step 7:** Run; expect PASS; revert-cycle the `merge-tree` tree comparison.
- [ ] **Step 8: Commit** `feat(wt): cleanup evidence ladder`.

---

### Task 4: Worktree facts and gates

**Files:**
- Create: `wt/internal/cleanup/worktrees.go`, `worktrees_test.go`

**Interfaces:**
- Consumes: `Git`.
- Produces:
  - `type WorktreeFact struct { Path, Branch, Head string; Detached, Bare, Primary, Locked, Prunable, Current bool }` — `Branch` is the full ref name from porcelain, `""` when detached.
  - `func ListWorktrees(g Git, cwd string) ([]WorktreeFact, error)` — `worktree list --porcelain -z`; `Primary` is the first entry; `Current` is cwd inside it (after `EvalSymlinks`, as `internal/worktree` does).
  - `type Gates struct { Blockers []Reason; Dirty bool; IgnoredCount int; IgnoredBytes int64; DetachedUnique bool }`
  - `type Reason struct { Code string; Detail string }`
  - `type GateInputs struct { Base string; HeldBy func(path string) bool; ProtectedPaths []string }`
  - `func CheckGates(g Git, w WorktreeFact, in GateInputs) (Gates, error)`
  - blocker codes (stable strings): `primary`, `current`, `held`, `locked`, `nested`, `operation_in_progress`, `protected_artifact`.

- [ ] **Step 1: Write tests**, each a fresh fixture with a linked worktree:
  - `TestListWorktreesParsesLockedPrunableDetached`.
  - `TestGatesBlockers` — table: primary, current dir inside, `git worktree lock`, a worktree nested inside another, a rebase stopped mid-way (`operation_in_progress`), `ProtectedPaths` containing a path under the worktree, `HeldBy` returning true → expected blocker code each.
  - `TestGatesDirtyUsesPinnedStatus` — `status.showUntrackedFiles=no` and a submodule config set in the repo config must not hide an untracked file: `Dirty == true`.
  - `TestGatesIgnoredFilesDemoteToLessSure` — an ignored `.env` gives `IgnoredCount == 1`, `Dirty == false`.
  - `TestGatesDetachedReachableVsUnique` — detached at a commit that is an ancestor of `Base` → `DetachedUnique == false`; detached on a commit on no branch → `true`.
- [ ] **Step 2:** Run; expect FAIL.
- [ ] **Step 3: Implement.** Dirty: `status --porcelain -z --untracked-files=normal --ignore-submodules=none`; ignored: `ls-files --others --ignored --exclude-standard --directory` (sum sizes with `os.Lstat`, count only); operation in progress via `rev-parse --git-path` for `rebase-merge`, `rebase-apply`, `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `BISECT_LOG`; nested = another worktree path has this path as a prefix.
- [ ] **Step 4:** Run; expect PASS; revert-cycle the pinned `--untracked-files=normal`.
- [ ] **Step 5: Commit** `feat(wt): cleanup worktree gates`.

---

### Task 5: PR facts (tri-state)

**Files:**
- Create: `wt/internal/cleanup/pr.go`, `pr_test.go`; modify `testmain_test.go` (stub the seam to fail)

**Interfaces:**
- Produces:
  - `type PRInfo struct { Number int; State string; HeadRefOid, BaseRef, MergeCommit string }` — `State` is `OPEN`, `MERGED` or `CLOSED`.
  - `type Lookup int` with `LookupKnown`, `LookupNone`, `LookupUnknown`.
  - `type PRResult struct { Lookup Lookup; PRs []PRInfo; Err string }`
  - `var ghPRList = realGhPRList` — `func(dir, branch string) ([]byte, error)`; the real one runs `gh pr list --head <branch> --state all --json number,state,headRefOid,baseRefName,mergeCommit,headRepository`, refuses inside a test binary (`errGhUnderTest`), and has a timeout.
  - `func LookupPRs(dir, branch, ownRepo string, parallel int) map[string]PRResult` — keyed by branch; filters to PRs whose head repository is `ownRepo`.

- [ ] **Step 1: Write tests** with `ghPRList` stubbed per case:
  - `TestPRLookupFailureIsUnknown` — stub returns an error → `LookupUnknown`, `Err != ""`.
  - `TestPRLookupEmptyIsNone` — stub returns `[]` → `LookupNone`.
  - `TestPRLookupFiltersForkPRs` — a PR from another head repo is dropped.
  - `TestPRLookupTruncatedIsUnknown` — stub returns exactly 100 PRs (the page limit we request) → `LookupUnknown`.
  - `TestRealGhPRListRefusesInTestBinary`.
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement**; request `--limit 100` and treat a full page as truncated. Bounded worker pool of `parallel`.
- [ ] **Step 4:** Run; expect PASS. **Step 5: Commit** `feat(wt): cleanup per-branch PR lookup`.

---

### Task 6: Snapshot, classification and row model

**Files:**
- Create: `wt/internal/cleanup/snapshot.go`, `classify.go`, `snapshot_test.go`, `classify_test.go`

**Interfaces:**
- Consumes: everything above; `sessionclaim.Others(commonDir, sessionclaim.Claim)` (read-only; never `Join`, never writes).
- Produces:
  - `type Kind string` with `KindWorktree`, `KindBranch`, `KindRemote`.
  - `type Status string` with `StatusProven`, `StatusLessSure`, `StatusUniqueWork`, `StatusInUse`.
  - `type Row struct { Kind Kind; Name string; Path string; Branch string; Remote string; Status Status; Evidence Evidence; Unknown bool; Reasons []Reason; Blockers []Reason; PR *PRInfo; AgeDays int }`
  - `type Report struct { Repo string; Base Base; Fetch FetchResult; GH GHState; Rows []Row }` where `GHState` is `ok`, `unavailable`, or `partial`.
  - `type GatherInputs struct { CWD string; Fetch bool; OwnRepo string; ProtectedPaths []string; Claim sessionclaim.Claim; Parallel int }`
  - `func Gather(g Git, in GatherInputs) (Snapshot, error)` — error only when classification is impossible (not a repo; no default branch → `ErrNoDefaultBranch`).
  - `type Options struct { Now time.Time; MinAge time.Duration }` — `DefaultMinAge = 10 * time.Minute` (decided here; non-zero).
  - `func Classify(s Snapshot, o Options) Report` — pure; no I/O.

Rules the tests pin (all from the spec; first match wins: `in_use` → `unique_work` → `less_sure` → `proven`):
- a worktree row carries its branch; a `KindBranch` row exists only for a local branch no worktree holds; a `KindRemote` row is separate;
- a proof makes `proven`; `PR` evidence (`EvPRTip`) = a MERGED PR into the default branch whose `HeadRefOid` equals the tip and whose merge commit is an ancestor of the base;
- weak signals only → `less_sure`; any `Unknown` → `less_sure` with `Unknown: true` and a reason naming the cause;
- tracked-modified or untracked (non-ignored) files, unpushed commits (no `refs/remotes/*` ref reaches them), a MERGED PR whose head moved on, or `DetachedUnique` → `unique_work`;
- ignored-only files, or a proof that needs `gh` while `GH != ok` → `less_sure`;
- a tip equal to the base and younger than `MinAge` is never `proven`;
- the default branch in any form is never a row.

- [ ] **Step 1: Write `TestClassify` as a table over fixture scenarios**, mirroring repo-cleanup's fixtures in `/Users/keith/github/ohanaverse/repo-cleanup-skill/bin/repo-cleanup-lib.sh:865-1044` (`setup_test_repo`) — one subtest per fixture, each asserting `(Kind, Status, Evidence)` and the reason codes: true merge; fast-forward; 1:1 rebase; multi-commit squash with main advanced; squash-then-revert (`less_sure`, **not** `proven`); reused branch name with an old MERGED PR (head OID ≠ tip → not `EvPRTip`); fresh empty branch; detached with unique commits; dirty; ignored-only; locked; nested; merge commit with unique content; git failure → `Unknown`.
- [ ] **Step 2: Write the named review-focus tests:** `TestClassifyNoDefaultBranchAllUnknown`, `TestClassifyUnknownPRIsNeverProven`, `TestClassifyDirtyMergedIsUniqueWork`, `TestClassifyFreshBranchNotProven`, `TestRowsMapWorktreeToBranchByPorcelain` (branches `topic/x` and `topic-x` each in a worktree; both directory names flatten to the same `topic-x` basename style yet each row keeps its own branch), `TestClassifyOrderIsInUseFirst`, `TestClassifyRemoteRowRules` (merged PR + no open PR on head or as base → tickable-class `proven`; merged into main without a PR → not `proven`; open PR using it as base → `in_use`-class blocker `base_of_open_pr`; `GH != ok` → `less_sure`, reason `cannot_verify`).
- [ ] **Step 3: Write `TestGatherReadsClaimsReadOnly`** — a live claim on the root makes the root row `in_use` with blocker `held`, and the claim directory is byte-identical afterwards.
- [ ] **Step 4:** Run; expect FAIL.
- [ ] **Step 5: Implement** `Gather` (one `for-each-ref` over `refs/heads` and `refs/remotes` for tips and upstream/gone; `ListWorktrees`; `LookupPRs`; claims) and `Classify` per the rules. Reason codes are stable strings; pin the ones the tests use and document them in the schema (Task 7).
- [ ] **Step 6:** Run; expect PASS; revert-cycle the `MinAge` rule and the status precedence.
- [ ] **Step 7: Commit** `feat(wt): cleanup snapshot and classifier`.

---

### Task 7: JSON envelope and contract

**Files:**
- Create: `wt/internal/cleanup/json.go`, `json_test.go`, `docs/contracts/cleanup-list.sample.json`, `docs/contracts/cleanup-list.schema.json`, `wt/cmd/wt/cleanup_contract_test.go`

**Interfaces:**
- Produces:
  - `type Envelope struct { Version int; Repo string; Base *BaseJSON; Fetch *FetchJSON; GH string; Rows []RowJSON; Error *ErrorJSON }` with `json` tags; `const EnvelopeVersion = 1`.
  - `func Encode(r Report) ([]byte, error)` and `func EncodeError(repo string, err error) ([]byte, error)`.
  - `RowJSON` keys: `kind, name, path, branch, remote, status, evidence, unknown, reasons[{code,detail}], blockers[{code,detail}], pr{number,state}`.

Rules: `evidence` is **absent** when none was found and `null`/`"unknown"` is never used for it — absent means "no proof", `"unknown": true` means "could not be determined"; `rows` is always present (possibly empty); on failure the same envelope is emitted with `error` set and `rows` empty, and nothing else is printed to stdout.

- [ ] **Step 1: Write the tests:**
  - `TestEnvelopeVersioned`; `TestEnvelopeAbsentVersusUnknown` (a `less_sure` row from a failed `merge-tree` has `unknown:true` and no `evidence` key); `TestEnvelopeErrorIsOneDocument`; `TestStatusAndEvidenceEnumsStable` (the exact strings from this plan).
  - In `cleanup_contract_test.go`: `TestCleanupFixtureMatchesEnvelopeShape` — reflect over `cleanup.Envelope` and compare to the keys of `docs/contracts/cleanup-list.sample.json` (copy the approach of `model_list_contract_test.go`: `jsonFieldSpecs`, `rawJSONKind`), and validate the fixture against `cleanup-list.schema.json` (enum values included).
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement** `Envelope`, `Encode`, `EncodeError`; write the schema and a sample fixture covering one row of each status.
- [ ] **Step 4:** Run; expect PASS; rename one key in the Go struct and see the contract test fail, restore.
- [ ] **Step 5: Commit** `feat(wt): cleanup --json envelope and contract`.

---

### Task 8: `wt cleanup` command (plain output and `--json`)

**Files:**
- Create: `wt/cmd/wt/cleanup.go`, `cleanup_test.go`; modify `wt/cmd/wt/main.go` (add `cleanupCmd(a)` to the `cmd.AddCommand(...)` list)

**Interfaces:**
- Consumes: `cleanup.Gather`, `cleanup.Classify`, `cleanup.Encode`.
- Produces: `func cleanupCmd(a *app) *cobra.Command` — `wt cleanup [--json] [--no-fetch] [--min-age DURATION] [--plain]`.

Behavior: runs `Fetch` unless `--no-fetch`, prints the header (base ref and its age, fetch result, `gh` state), then — with a TTY and no `--json`/`--plain` — hands the `Report` to the list (Task 9); otherwise prints the grouped plain text (same four groups, same wording as the list). Exit 0 when a report was produced; exit 1 (via the existing `exitcode.go` mechanism) when classification is impossible; a failed fetch or `Unknown` row is data, never an exit code. `ProtectedPaths` come from the registry's `local_path` fields (read through `a.cfg`; a missing registry is not an error).

- [ ] **Step 1: Write tests** (build a fixture repo, `chdir` into it, run `rootCmd` with args):
  - `TestCleanupCmdJSONIsOneDocument` — stdout parses as one `Envelope`; stderr empty.
  - `TestCleanupCmdPlainGroupsInOrder` — group headings appear in the order proven, less sure, unique work, in use.
  - `TestCleanupCmdExit1WithoutDefaultBranch` — `--json` still prints one envelope with `error` set, exit code 1.
  - `TestCleanupCmdFetchFailureExits0` — unreachable origin: exit 0, header and JSON `fetch.ok == false`.
  - `TestCleanupCmdContainsNoDeletion` — grep the `internal/cleanup` and `cmd/wt/cleanup.go` sources for `worktree remove`, `update-ref`, `branch", "-d`, `branch", "-D`, `--delete` and fail if any appear.
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement.** **Step 4:** Run; expect PASS. **Step 5: Commit** `feat(wt): wt cleanup command`.

---

### Task 9: View-only list

**Files:**
- Create: `wt/internal/tui/cleanup_list.go`, `cleanup_list_test.go`

**Interfaces:**
- Consumes: `cleanup.Report`.
- Produces: `func RunCleanupList(r cleanup.Report, th themes.Theme) error` and the bubbletea model behind it.

Behavior (layout A from the design): four group headings in the spec's order; a checkbox column showing the default ticks (only `proven` rows ticked; `in_use` rows show `[-]` and cannot be toggled); `space` toggles, `a` ticks all proven, `q` quits; a footer line explains the highlighted row (evidence, or what is missing); `enter` prints `view only — would remove N items: …` and exits 0 (**nothing is removed**; PR B replaces this). Truncate the name column, never the evidence.

- [ ] **Step 1: Write model-level tests:** `TestCleanupListGroupOrder`; `TestCleanupListDefaultTicksOnlyProven`; `TestCleanupListInUseRowsNotToggleable`; `TestCleanupListEnterIsViewOnly` (no command is returned that touches git); `TestCleanupListNarrowWidthKeepsEvidence` (render at width 80).
- [ ] **Step 2:** Run; expect FAIL. **Step 3: Implement** with the existing `list`/delegate styling in `internal/tui/delegate.go` as the pattern.
- [ ] **Step 4:** Run; expect PASS.
- [ ] **Step 5: Check the real screen at 80x24** through the pty driver (see `memory/tui-screens-via-pty`) in a scratch repo; paste the capture into the PR body.
- [ ] **Step 6: Commit** `feat(wt): wt cleanup list view`.

---

### Task 10: Comparison, docs, review, PR

**Files:**
- Modify: `wt/CLAUDE.md`, `wt/docs/wt-agents/README.md` (command table), `docs/guides/08-maintenance-and-troubleshooting.md` (a short "Cleaning up worktrees and branches" pointer to the command, no live state)

- [ ] **Step 1: Comparison run.** In this repo and in a scratch repo built to cover the Task 6 fixtures, run `wt cleanup --json --no-fetch` and `repo-cleanup --dry-run`; list every row where the two disagree and the reason. Disagreements are expected where `merge-tree` proves more than `git cherry`, or where wt demotes on ignored files; any other disagreement is a bug to fix before the PR. Put the table in the PR body (the spec's acceptance).
- [ ] **Step 2: Docs** — `wt/CLAUDE.md` gets a "Cleanup classifier" invariant block (fail closed; no deletion in `internal/cleanup`; base is `origin/<default>`; weak signals are not proofs); update the command table; run `make check-links`.
- [ ] **Step 3: Verification** — `go test ./...`, `go vet ./...`, `make check` in `wt/`; `make test-all` at the repo root.
- [ ] **Step 4: Three-lens review** (`/code-review` scoped with the PR number, as the repo does), fix findings.
- [ ] **Step 5: Open the PR** from the worktree; the body uses `Refs`, never a closing keyword, and ends with the attribution line.

---

## Self-Review

- **Spec coverage:** ladder rungs 1–5 and weak signals (T3); PR rung 6 (T5/T6); worktree gates incl. protected artifacts, held claims, locks, nested, ops, detached (T4/T6); base/fetch/default chain (T2); tri-state PR (T5); row model, precedence, pushed, min-age, remote rules (T6); `--json` + contract + exit codes (T7/T8); list (T9); comparison acceptance (T10). Not in PR A by design: removal, `wt remove`, trash drain, CAS delete, `origin` removal, move-to-worktree.
- **Types:** `Evidence`, `Verdict`, `Reason`, `Row`, `Report`, `Status` are defined once (T3/T4/T6) and reused unchanged in T7–T9.
- **Proportion:** the plan states signatures, test names and assertions, and the rules the spec pins; bodies are the implementer's.
- **Unresolved by design (builder's call):** reason-code strings beyond those named; exact column widths; the `Parallel` default; whether `--plain` is needed beyond non-TTY.
