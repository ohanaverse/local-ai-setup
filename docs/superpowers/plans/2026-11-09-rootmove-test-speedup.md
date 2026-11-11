# Rootmove Test Speedup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `internal/rootmove`'s test suite parallel and template-seeded (target: package wall 125–143s → ≤40s) while proving — mechanically — that none of the design doc's six missed-failure classes regresses.

**Architecture:** First slice of the wave-2 test-infrastructure migration (lightest package first). Three foundations land here and are reused by later packages: a shared `internal/gittest` fixture kit (seeded template origins, copy-then-mutate clones, lineage gate, index-probe arming, real-git round-trip asserts), per-call test seams replacing rootmove's package-var seams and `cleanup`'s gh seam plumbing, and a declared serial-island registry with an AST drift guard.

**Tech Stack:** Go 1.26.7 (module root `wt/`), real git subprocesses, stdlib `testing` only.

**Spec:** `docs/superpowers/specs/2026-11-09-git-heavy-test-speedup-design.md` (the plan argues from the spec; executors read both).

## Global Constraints

- Ruling 1: production test-facing signatures may change, behavior-identical. Ruling 2: `go test -race` clean is required and verified per PR; CI never runs `-race`. Ruling 3: table-driven merges allowed; every rename/merge flagged in the PR description; assertion coverage stays 1:1.
- Constraint 1: armed tests assert their hook FIRED (fire-count + exact outcome); no armed test may pass with zero evidence.
- Constraint 2: `t.Setenv`-owning tests are permanently serial and listed in one registry; never convert `t.Setenv` to `os.Setenv`.
- Constraint 3: the raw-index byte-diff proof counts only after `ArmIndexProbe`+`AssertProbeArmed` ran.
- Constraint 4: templates are immutable; every clone passes `Lineage` before the test body; copies never share an origin path or carry linked worktrees.
- Constraint 5/6: fixtures built by hand must round-trip through the real git read the engine uses (`RoundTrip`).
- Constraint 7: if any test gains a goroutine wait or timing assertion, its package joins the `single-p` CI list in the same change.
- Repo guards the build must keep green (they scan the new code automatically): `TestDeletionStringsOnlyInRemovalPackage` (no `os.Remove*` outside the allow-list), `TestHardResetExistsNowhereOutsideRemoval` (argv words `--hard`, `reset`, `clean`, `checkout`, `restore`, `stash` banned outside `internal/removal`), `TestRootmoveContainsNoDeletion`, `TestEveryPackageThatRunsGitIsolatesIt` (a new package that runs git needs `gitenv.IsolateForTest()` in its `TestMain`).
- House rules: every `Test*` carries its `//` what-and-why comment; assertions use `git for-each-ref` output, never `refExists`; case-sensitivity-dependent checks keep `caseInsensitiveFS`'s probe-and-skip.

## Review Focus

1. Two engines under parallel tests cross-talk through a hook fallback (the failure mode that makes fail-closed verdicts pass for the wrong reason) — pinned by `TestHooksAreNotSharedAcrossEngines` in Task 4.
2. One test poisons a shared template and silently corrupts every later clone — pinned by `TestLineageGateNamesThePoison` in Task 2.
3. A locked `git status` inside the code under test rewrites a fresh copy's index and the byte-diff proof stays mute — pinned by `TestArmIndexProbeMakesAWriteVisible` in Task 2.
4. A hand-built `.git` shape git treats as corrupt quietly tests git's error path instead of the engine's — pinned by `TestRoundTripRejectsCorruptShape` in Task 2.
5. A future test adds `t.Setenv` without joining the serial registry and races its siblings — pinned by `TestSerialIslandsAreExactlyTheEnvOwningTests` in Task 6.

## File Structure

- `wt/internal/gittest/` (new): `gittest.go` — template seed/clone, lineage gate, index probe, round-trip assert (one responsibility: shared git fixture kit); `testmain_test.go`, `gittest_test.go`.
- `wt/internal/rootmove/hooks.go` (new): `Hooks` struct + nil-defaulting methods (one responsibility: per-call seam state).
- `wt/internal/rootmove/execute.go`: the `var` seam block (lines 32–47) folds into `hooks.go`; consult sites (224, 249, 264, 277, 287) call through `Engine.Hooks`.
- `wt/internal/rootmove/fastforward.go`: consult site (55) same.
- `wt/internal/rootmove/types.go`: `Engine` (line 46) gains `Hooks Hooks`.
- `wt/internal/cleanup/snapshot.go`: `GatherInputs` (line 19) gains `PRList`/`PRBaseList` hooks.
- `wt/internal/cleanup/prseam.go`, `pr.go`: hook resolution; consults at the `ghPRList`/`ghPRBaseList` reads go through it.
- `wt/internal/rootmove/*_test.go`: fixture re-base, hook migration, `t.Parallel`, island registry (`islands_test.go`, new).

---

### Task 1: Baseline measurement and island census

**Files:** none (measurement only; the table goes in the implementing PR's description).

**Interfaces:**
- Consumes: nothing.
- Produces: the per-test duration table (top-level tests and `TestExecuteRecoveryRows`'s rung subtests), the package wall, and the island census (the top-level tests owning `t.Setenv`/`traceGit`) — Tasks 4–7 and the PR body use these exact numbers and names.

- [ ] **Step 1: Capture per-test durations**

Run: `cd wt && go test -count=1 -v ./internal/rootmove 2>&1 | grep -E '^(--- PASS|--- FAIL|ok  )'`
Expected: PASS lines carrying `TestName (Ns)` for all 25 top-level tests plus subtest times, and one `ok ... rootmove Ns` package line.

- [ ] **Step 2: Capture the rung-level times**

Run: `cd wt && go test -count=1 -v -run TestExecuteRecoveryRows ./internal/rootmove 2>&1 | grep -E '^    --- (PASS|FAIL)'`
Expected: one line per recovery rung case with its duration; record which five use `traceGit` (grep `traceGit(` in `execute_test.go`, lines ~416, 439, 457, 483, 551).

- [ ] **Step 3: Produce the island census**

Run: `cd wt && grep -ln 't\.Setenv(\|traceGit(' internal/rootmove/*_test.go`
Expected: the files `execute_test.go` and `preflight_test.go`; the containing top-level tests (four islands) are `TestExecuteRecoveryRows` (traceGit 416/439/457/483/551), `TestExecuteRecordNotWritableBlocksRelease` (753), `TestExecuteStartRecheck` (821 and 867), `TestPreflightNeverFetches` (`preflight_test.go:425`). `TestExecuteRunsGitInRootAndDestFromALinkedWorktree` owns NO env and is not an island (it swaps seam vars, whose shared state Task 4 dissolves).

- [ ] **Step 4: Record both tables**

Paste the duration table and the four-name island census into the PR description working file. No repo files change; no commit.

---

### Task 2: The shared fixture kit, `internal/gittest`

**Files:**
- Create: `wt/internal/gittest/gittest.go`
- Create: `wt/internal/gittest/gittest_test.go`
- Create: `wt/internal/gittest/testmain_test.go`

**Interfaces (Tasks 3–7 consume these exact names):**
- `type SeedOpts struct{ ExcludeWorktrees bool }` — the only template dimension; each value is its own immutable lineage.
- `func Seed(t testing.TB, opts SeedOpts) *Template` — memoized per `SeedOpts` behind `sync.OnceValues`; the template is a bare `origin.git` seeded with one `README` commit on `main`, `HEAD` → `main`, and (when `ExcludeWorktrees`) `.worktrees/` in its `info/exclude`. Never mutated after seeding. Template directories are process-scoped (`os.MkdirTemp("", "gittest-tpl-")` — never a test's `t.TempDir`, which would delete the shared template when its first caller ends) and cleaned via `CleanupTemplates()`.
- `func CleanupTemplates()` — best-effort removal of every seeded template directory: `os.RemoveAll`, confined to this function, with ONE `fileOpAllowed` entry for `internal/gittest/gittest.go` in the deletion guard (the allow-list is that guard's designed remedy for self-created scratch cleanup; mirror `internal/config/fortest.go`'s precedent entry). Each consuming package's `TestMain` calls it just before `os.Exit`.
- `func (tpl *Template) Clone(t testing.TB) (origin, repo string)` — per-test pair under `t.TempDir()`: `git clone --bare <tpl.Origin> <tmp>/origin.git`, then `git clone <tmp>/origin.git <tmp>/repo`, then repo config `user.email=t@example.com`, `user.name=T`, `commit.gpgsign=false`. Returns both absolute paths.
- `func Lineage(t testing.TB, repo string)` — fails the test naming the poison on: any entry under `.worktrees/`, any ref under `refs/wt/rescued/`, any ref `refs/heads/topic`, `info/exclude` content other than the template's expected one, or HEAD not on `main`. (Core `lineageErr(repo string) error` is what tests call directly.)
- `func RawIndex(t testing.TB, repo string) string` — raw `.git/index` bytes (`"absent"` if missing), read without optional locks, as `rootmove.rootState` reads them today.
- `func ArmIndexProbe(t testing.TB, repo string)` — re-times the repo's tracked worktree files AND the raw index file to the same past second (the racy-clean `set()` shape from `internal/cleanup/dirt_test.go`'s `TestDirtCoveredRacyCleanEntry`, whose comment carries the flake history). Index-mtime-only arming is mute: a locked status rewrite stays byte-identical (empirically verified on git 2.54).
- `func AssertProbeArmed(t testing.TB, repo string)` — fails unless the index file's mtime reads older than 30 minutes.
- `func RoundTrip(t testing.TB, repo, want string, args ...string)` — runs `git -C repo args...` ignoring the exit code and fails unless the combined output equals `want`. (Core `roundTripErr` returns the mismatch instead.)

- [ ] **Step 1: Write the failing helper tests**

In `gittest_test.go`, with `//` what-and-why comments per house rule:
- `TestSeedCloneIsolatesOrigins`: two `Clone` pairs from one `Seed`; commit on pair A's repo and push; assert pair B's `for-each-ref` and `tpl.Origin`'s lack it, and each repo's `git remote get-url origin` equals its own origin's absolute path.
- `TestLineageGateNamesThePoison`: clone; plant `.worktrees/stray`; assert `lineageErr` is non-nil and its message contains `stray`.
- `TestArmIndexProbeMakesAWriteVisible`: clone; `RawIndex`; run plain `git status`; assert bytes unchanged (the muting a fresh copy causes); `ArmIndexProbe` + `AssertProbeArmed`; run `git status` again; assert `RawIndex` bytes changed (a locked read is now visible).
- `TestRoundTripRejectsCorruptShape`: clone; write a garbage `.git/worktrees/ghost/gitdir`; assert `roundTripErr(repo, "worktree list --porcelain", <expected sans ghost>)` is non-nil, and a matching `want` gives nil.
- `TestTemplateNeverCarriesWorktrees`: after `Seed`, assert `git -C tpl.Origin worktree list --porcelain` names only the bare root — the builder-review hazard (copied `gitdir` back-pointers) is impossible by construction.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd wt && go test ./internal/gittest/...`
Expected: FAIL — `undefined: Seed` (and friends).

- [ ] **Step 3: Implement the kit in `gittest.go`**

One rule shapes every helper: fixture state is created by real git (`init`, `clone --bare`, `clone`, `add`, `commit`, `push`, `switch -c`, `config`) and never deleted (banned argv words keep `TestHardResetExistsNowhereOutsideRemoval` green; the guards' `os.Remove*` scan passes with at most the one `fileOpAllowed` entry `CleanupTemplates` needs — see Interfaces; seed commits run with `-c user.name=T -c user.email=t@example.com -c commit.gpgsign=false`). `Lineage` compares ref names exactly (never case-folded) and reads with `GIT_OPTIONAL_LOCKS=0`.

- [ ] **Step 4: `testmain_test.go` calls `gitenv.IsolateForTest()` first**

Required by `TestEveryPackageThatRunsGitIsolatesIt`; mirror `internal/rootmove/testmain_test.go`'s HOME/XDG/GIT_CONFIG isolation and the poison-gh comment.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/gittest/... && go test ./internal/gitenv/...`
Expected: PASS both (the second proves the isolation guard accepts the new package). `testmain_test.go` also calls `gittest.CleanupTemplates()` just before `os.Exit`.

- [ ] **Step 6: Commit**

```bash
git add internal/gittest
git commit -m "test(gittest): shared git fixture kit - templates, lineage gate, index probe"
```

---

### Task 3: Per-call gh hooks in `cleanup` (Ruling 1, cross-package half)

**Files:**
- Modify: `wt/internal/cleanup/snapshot.go:19` (`GatherInputs`)
- Modify: `wt/internal/cleanup/prseam.go`
- Modify: `wt/internal/cleanup/pr.go` (the `ghPRList` read near line 57 and the `ghPRBaseList` read near line 238)
- Test: `wt/internal/cleanup/pr_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `GatherInputs.PRList func(dir, branch string) ([]byte, error)` and `GatherInputs.PRBaseList func(dir, base string) ([]byte, error)` — nil means the legacy package seam (later plans migrate and finally delete it); the resolution helpers `prListFn(in GatherInputs)` / `prBaseListFn(in GatherInputs)` in `prseam.go` are how every consult reads them. `StubPRListForTest`/`StubPRBaseListForTest` stay exported and unchanged for `cmd/wt`'s 252 legacy call sites. Any exported function between `Gather`/`Rescan` and the two reads that lacks inputs gains an `in GatherInputs` parameter (Ruling 1 authorizes the signature change; update its callers in-repo).

- [ ] **Step 1: Write the failing test**

`TestPRHooksArePerCall` (with `//` comment): build two `GatherInputs` values with different `PRList` fns (each increments its own counter and returns `"[]"`), run the lookup both feed concurrently, assert each counter is ≥1 and the outputs carry no cross-talk. Plus `TestPRHookFallbackStillPoisoned`: a zero `GatherInputs` in a test binary resolves to `errGhUnderTest` (the forgetting-to-stub guarantee intact).

- [ ] **Step 2: Run to verify it fails**

Run: `cd wt && go test ./internal/cleanup -run 'TestPRHooksArePerCall|TestPRHookFallbackStillPoisoned'`
Expected: FAIL — `unknown field PRList` (compile failure counts).

- [ ] **Step 3: Implement the hooks**

Add the two fields; `prListFn`/`prBaseListFn` return the field when non-nil, else `ghPRList`/`ghPRBaseList`; switch the two consults in `pr.go` to the resolved fn; thread `in` per the Interfaces rule. No behavior change when both are nil.

- [ ] **Step 4: Run the package**

Run: `cd wt && go test ./internal/cleanup ./cmd/wt`
Expected: PASS (legacy stub users unaffected).

- [ ] **Step 5: Commit**

```bash
git add internal/cleanup
git commit -m "refactor(cleanup): per-call gh pr-list hooks on GatherInputs (Ruling 1)"
```

---

### Task 4: `Hooks` struct, rootmove's seams go per-call (Ruling 1, local half)

**Files:**
- Create: `wt/internal/rootmove/hooks.go`
- Modify: `wt/internal/rootmove/types.go:46` (`Engine` gains `Hooks Hooks`)
- Modify: `wt/internal/rootmove/execute.go` (delete the `var` block lines 32–47; consults at 224, 249, 264, 277, 287 call through `e.Hooks`)
- Modify: `wt/internal/rootmove/fastforward.go:55` (same)
- Test: `wt/internal/rootmove/hooks_test.go` (new); migrate the 18 `swap(` sites in `execute_test.go`/`fastforward_test.go`; delete `swap` (`execute_test.go:20-26`) once none remain.

**Interfaces:**
- Consumes: nothing new.
- Produces (later packages copy this shape):

```go
type Hooks struct {
    ExecuteInto  func(rescue.Engine, *rescue.Plan, string) rescue.Outcome
    BeforeRelease func()
    ReleaseRoot  func(cleanup.Git, removal.RootRelease) (removal.ReleaseResult, error)
    StepGit      func(cleanup.Git, ...string) (cleanup.Out, error)
    AfterRelease func()
}
```

Each nil field resolves to today's real behavior (`e.ExecuteInto`, no-op, `removal.ReleaseRoot`, `g.Run`, no-op) through unexported nil-defaulting methods (`h.into(...)`, `h.before()`, `h.release(...)`, `h.step(...)`, `h.after()`); production code and zero-value engines are unchanged.

- [ ] **Step 1: Write the failing tests**

- `TestExecuteHooksFireAtTheirRungs` (with `//` comment): happy-path run with all five hooks armed to record `"<name>"` into a slice and delegate to the real behavior; assert the recorded order equals `["ExecuteInto","BeforeRelease","ReleaseRoot","AfterRelease","StepGit"]` exactly (the consult-site order in `execute.go` is 224, 249, 264, 277, 287) and each hook's counter is 1.
- `TestHooksAreNotSharedAcrossEngines` (Review Focus 1): two engines with distinct `BeforeRelease` counters; run both to their hook points (a failed `ReleaseRoot` stub stops each at row 4 — reuse `TestExecuteRecoveryRows`'s stub shapes); assert each counter is exactly 1 and no cross-count.

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/rootmove -run 'TestExecuteHooksFireAtTheirRungs|TestHooksAreNotSharedAcrossEngines'`
Expected: FAIL — `unknown field Hooks` (compile failure counts).

- [ ] **Step 3: Implement `hooks.go` and move the consults**

Signature and defaults are fixed above; each consult site becomes `e.Hooks.<method>(<the exact arguments the site passes today>)` where `e` is the rootmove `Engine` the site's enclosing function already receives; if any intermediate helper holds no `Engine`, thread the `Hooks` value (by value) into it. Keep each seam's existing comment as the field's doc comment (the recovery-row mapping is the contract).

- [ ] **Step 4: Migrate the 18 `swap(` sites to per-engine hooks**

Pattern per site: `swap(t, &beforeRelease, fn)` becomes `e.Hooks.BeforeRelease = fn` before the run, with a fire assertion added (Constraint 1: `fired := false` … `if !fired { t.Fatal("hook did not fire") }`). Ruling 3: structurally identical rung rows may table-merge; list every rename/merge for the PR. Delete `swap` when the last site is gone.

- [ ] **Step 5: Run the package (and `-race`)**

Run: `cd wt && go test ./internal/rootmove && go test -race ./internal/rootmove`
Expected: PASS both.

- [ ] **Step 6: Commit**

```bash
git add internal/rootmove
git commit -m "refactor(rootmove): per-engine Hooks replace package seam vars (Ruling 1)"
```

---

### Task 5: Re-base `rootmove`'s fixture on `gittest` templates

**Files:**
- Modify: `wt/internal/rootmove/fixture_test.go` (`newFixtureExcluding` 44–65, `rootState` 188–216, `noGh` 125–128)
- Test: existing suite is the cycle; one new assertion in `fixture_test.go`.

**Interfaces:**
- Consumes: Task 2's `Seed`/`Clone`/`Lineage`/`RawIndex`/`ArmIndexProbe`/`AssertProbeArmed`; Task 3's `GatherInputs.PRList`.
- Produces: unchanged `fixture` API (`newFixture`, `newFixtureExcluding`, `Commit`, `Worktree`, `Detached`, `engine`, `onBranch`, `dirty`, `originAhead`, `rootState`, `hook`) — later packages copy this re-based shape.

- [ ] **Step 1: Verify the suite is green before the change**

Run: `cd wt && go test ./internal/rootmove`
Expected: PASS (27 tests — Task 1's 25 plus Task 4's two hooks tests).

- [ ] **Step 2: Re-base the fixture**

`newFixtureExcluding(t, excludeWorktrees)` becomes: `tpl := gittest.Seed(t, gittest.SeedOpts{ExcludeWorktrees: excludeWorktrees})`; `origin, repo := tpl.Clone(t)`; fill `fixture{t, Origin: origin, Dir: repo}`; `gittest.Lineage(t, repo)`. The hand-rolled init/clone/config/commit/push/set-head sequence goes away (it is the template's job). `noGh(t)` becomes `f.engine()`'s inputs carrying `PRList: func(string, string) ([]byte, error) { return []byte("[]"), nil }` (Task 3's field) — the TestMain poison stays as the fallback.

- [ ] **Step 3: Arm the index probe inside `rootState`**

`rootState` first calls `gittest.ArmIndexProbe(t, f.Dir)` + `gittest.AssertProbeArmed(t, f.Dir)`, then snapshots with `gittest.RawIndex` in place of its local raw read (Constraint 3: the proof counts only when armed). Add one line to `rootState`'s comment naming the flake history it guards.

- [ ] **Step 4: Run the suite (and `-race`)**

Run: `cd wt && go test ./internal/rootmove && go test -race ./internal/rootmove`
Expected: PASS both — same 27 tests, same assertions.

- [ ] **Step 5: Commit**

```bash
git add internal/rootmove/fixture_test.go
git commit -m "test(rootmove): fixtures seed from gittest templates with lineage gate"
```

---

### Task 6: Parallelize, register the islands, round-trip the hand-built shapes

**Files:**
- Create: `wt/internal/rootmove/islands_test.go`
- Modify: every `wt/internal/rootmove/*_test.go` except the island owners (add `t.Parallel()`); `execute_test.go`/`preflight_test.go` (parallelize only the non-`t.Setenv` subtests of island tests)
- Modify: any hand-built `.git` shape site found in the audit (likely `fixture_test.go`'s `hook`)

**Interfaces:**
- Consumes: Task 2's `RoundTrip`.
- Produces: `var serialIslands = map[string]string{...}` in `islands_test.go` (test name → why it owns process-global state) — the later packages' plans copy this registry pattern.

- [ ] **Step 1: Write the failing drift guard**

`TestSerialIslandsAreExactlyTheEnvOwningTests` (with `//` comment): a name-keyed `go/ast` scan (the `losses_guard` style) of `internal/rootmove/*_test.go` for `t.Setenv(` and `traceGit(` calls; map each to its enclosing top-level `func Test`; assert the resulting set equals `serialIslands`'s keys exactly (a new env-owning test without a registry entry fails here — Review Focus 5). Seed the map with Task 1's census — the four env/trace owners: `TestExecuteRecoveryRows`, `TestExecuteRecordNotWritableBlocksRelease`, `TestExecuteStartRecheck`, `TestPreflightNeverFetches` — each with a one-line reason. Seam-var swapping is NOT an island criterion (Task 4 removes the shared state).

- [ ] **Step 2: Run to verify it fails**

Run: `cd wt && go test ./internal/rootmove -run TestSerialIslandsAreExactlyTheEnvOwningTests`
Expected: FAIL — `undefined: serialIslands` (then, with only the map added, PASS; keep it red first by writing the scan before the map).

- [ ] **Step 3: Add `t.Parallel()` everywhere the registry allows**

All top-level tests not in `serialIslands`; inside island tests, only subtests that never reach `t.Setenv` (the barrier releases them after the serial rungs finish — the spec's Constraint 2 note). Never touch an island's own body.

- [ ] **Step 4: Audit and round-trip hand-built `.git` state**

Run: `cd wt && grep -n 'os.WriteFile\|os.MkdirAll' internal/rootmove/*_test.go`
For each site writing inside a `.git` directory (expected: `fixture_test.go`'s `hook`; none other), add a `gittest.RoundTrip` assertion against the read the engine makes (for hooks: `rev-parse --path-format=absolute --git-path hooks/<name>` output must equal the installed hook path). If the audit finds zero such sites, record that sentence in the PR body instead.

- [ ] **Step 5: Run the package hard (and `-race`)**

Run: `cd wt && go test -count=5 ./internal/rootmove && go test -race -count=2 ./internal/rootmove`
Expected: PASS every repetition (parallelism must be deterministic, not lucky).

- [ ] **Step 6: Commit**

```bash
git add internal/rootmove
git commit -m "test(rootmove): parallel tests with a serial-island registry and drift guard"
```

---

### Task 7: Verify, measure, open the PR

**Files:** none new (the PR body is the deliverable).

**Interfaces:**
- Consumes: Tasks 1–6.
- Produces: the implementing PR (branch `rootmove-test-speedup` off the design's branch lineage), carrying the before/after tables, the Ruling-3 rename list, and the `-race` result.

- [ ] **Step 1: Full suite plus guards**

Run: `cd wt && go test ./... && go test -race ./internal/rootmove ./internal/gittest ./internal/cleanup ./cmd/wt && go vet ./... && make check`
Expected: PASS (the deletion/argv/isolation guards scan the new code automatically).

- [ ] **Step 2: Re-measure**

Task 1's Step 1 command again; assemble the before/after table (per-test and package wall) and check the target: package wall ≤40s.

- [ ] **Step 3: Commit nothing new; push and open the PR**

Run: `git push -u origin rootmove-test-speedup` and open the PR with the body template: Summary (what changed per task), Evidence (before/after table, `-race` output line, `go test -count=5` line, Ruling-3 rename/merge list), Merge Danger (door: two-way — the fixture kit and hooks are additive and the old fixture code is recoverable from git; blast radius: test infrastructure plus ruling-1 production seam signatures in `rootmove`/`cleanup`, no engine behavior change). Every claim cites the command output that shows it.
