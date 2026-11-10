# Git-heavy test suites: speedup design (wave 2)

Date: 2026-11-09. Status: builder-reviewed; all rulings resolved (see
"Rulings"). Follows the CI work in PRs #561/#563 (Refs #555); the
drift-guard follow-up is #562.

## Intent and why

The four git-heavy suites — `wt/internal/removal`, `wt/internal/rescue`,
`wt/internal/cleanup`, `wt/internal/rootmove` — are the tests around wt's
deletion, copy, classification and restore-root engines, all exercising real
git subprocesses. They are the wall clock of every CI run and every local
`go test ./...`:

| package | runner (per-package, multi-P) | local |
|---|---|---|
| removal | 545–600s | ~350s |
| cleanup | 231–259s | — |
| rescue | 209–264s | — |
| rootmove | 125–143s | — |

The `test-git-heavy` shard (~10 min, bounded by `removal`) is now the critical
path of PR CI (run 42730018371), and `removal` recently tripped `go test`'s
default 10m per-package timeout on a slow runner (run 42728452039; the shard
now carries `-timeout 20m` as a stopgap). Every test seeds a fresh git
scenario with real subprocesses and none is parallel — a deliberate design
that this document must not spend carelessly.

These suites carry irreplaceable safety coverage. They have caught real bugs:
ignored-file deletion during a fast-forward merge through case/Unicode path
aliasing; squash-merge classification that would have deleted unmerged work; a
compare-and-swap window where a branch recreated after a check got deleted;
index-byte changes proving the classifier wrote to a repository it promised to
only read; mid-operation source edits that the rescue fingerprint must refuse.
The engines' whole trust story is "fail closed, prove before delete", and these
tests are the proof.

## Goals

- `test-git-heavy` wall from ~10 min to ≤3 min on the runner (target ≥3x;
  `removal` ≤90s, the others in proportion). Local runs improve the same way.
- Every currently-green assertion keeps its meaning. A deliberately changed
  assertion is listed explicitly in the implementing PR.
- The speedup is demonstrably safe against the six anti-goals below — each
  anti-goal's mitigation is an implemented guard, not a promise.

## Non-goals

- No engine behavior changes. Production edits are limited to the seam
  reshaping Ruling 1 authorizes (test-facing signatures only,
  behavior-identical).
- No reduction in what is asserted (see Rulings, #3).
- Not cross-package parallelism — the shard already runs the four packages
  concurrently; the cost is inside each package's serial test sum.
- Not the `single-p` guard job's policy beyond re-validating one
  classification (Constraint 7).

## Anti-goals: what the speedup must not stop catching

Derived from a 64-agent sweep of the four suites (199 evidence rows) narrowed
by adversarial verification: 18 candidate risks, 7 survived three independent
refutation lenses (grounding in a real catcher today; "would still be caught
after the change"; plausibility in this system), grouped into 6 classes below.
The 11 refuted candidates are recorded in the sweep's journal; the design does
not need to defend against them.

### 1. Fake ages → real deletions (data loss)

`cleanup`'s min-age gate (`classify.go:856-871`) decides "fresh" from three
independent age sources — branch reflog age, `packed-refs` mtime, loose-ref
mtime — because a fresh branch off a stale local main otherwise reads
ancestor-merged and gets deleted as proven. `TestClassifyAgeWithoutReflog`'s
six cases (`classify_test.go:527-597`) each build exactly one source shape.
Batched or hand-built fixtures with fixed identity/dates collapse the three
sources into one code path; swallowed non-zero exits also lose `mustFail`'s
guarantee (`cleanup/fixture_test.go:161-172`) that the stopped-rebase/merge/
cherry-pick states genuinely stopped on a conflict. **Missed if unfixed: a
young branch with real work deleted as merged.**

### 2. Fail-closed verdicts passing for the wrong reason (data loss)

Every fault injection and gh answer is a mutable package-level var armed and
restored per test: rescue's fingerprint seams `afterCarry`/`afterVerify`/
`beforeRecordRef` (`rescue/execute.go:17-27`) and `afterReadWork`
(`rescue/preflight.go:21-22`), removal's `failProbe`/`stubRefGit`/`stubRescan`,
and `cleanup.StubPRListForTest` (`cleanup/prseam.go:8`) which all four
packages' `noGh` swaps. Under `t.Parallel` a neighbour's `t.Cleanup`
restore-to-no-op lands inside the armed window and the injection never fires —
the test passes green with zero evidence (`TestExecuteIgnoresChangesOutside-
TheSource` asserts only `StatusMoved`, so a disarmed churn passes while the
"the fingerprint ignores unrelated refs" guard goes unexercised).
Representatives: `TestSubmoduleCheckFailsClosed` (`removal/failclosed_test.go:
29-75`), `TestExecuteFailsWhenTheSourceChangesWhileCopying`
(`rescue/execute_test.go:229`). **Missed if unfixed: fail-closed refusals and
the poison-gh-stub guarantee pass vacuously; an unmerged branch reads proven.**

### 3. Process-global attribution destroyed (safety regression)

A small set of tests own process-global state while they run (builder-review
inventory: `removal/root_test.go:1200-1235` exports `GIT_DIR`/`GIT_INDEX_FILE`
at a decoy repo; `removal/types_test.go:128-160` points `GIT_CONFIG_GLOBAL`
at a failing `core.hooksPath`; PATH git shims intercepting every
`exec.Command("git")` in `rescue/hardening_test.go:214-328`,
`cleanup/dirt_test.go`, `cleanup/preserved_test.go`; `cleanup/git_test.go`
sets `GIT_SSH_COMMAND`; rootmove's `traceGit` writes `GIT_TRACE` files at 9
call sites in `execute_test.go`). There is no parallel-safe rewrite of the
core ones: `TestReleaseRootRefusesWithRepoEnvironment`
(`removal/root_test.go:1200-1235`) is the only proof that `ReleaseRoot`
refuses `GIT_DIR`/`GIT_INDEX_FILE` before any write (`t.Setenv` panics under
`t.Parallel` by design); `TestFixtureIgnoresHostileGlobalConfig`
(`removal/types_test.go:128-160`) is the only proof TestMain's HOME/XDG/
GIT_CONFIG isolation defeats a hostile `core.hooksPath`; the PATH git-shim
tests (`rescue/hardening_test.go:214-328`) intercept every `exec.Command("git")`
process-wide; and GIT_TRACE-based never-fetch/argv checks
(`rootmove/preflight_test.go:415-438`, `rootmove/execute_test.go:93-164`) read
a trace file whose lines can belong to another test. Per-exec `Env` injection
silently disarms the PATH shims; `os.Setenv` leaks decoy `GIT_DIR` into every
concurrent test's git children. **Missed if unfixed: the env-trap safety gate
loses its only proof while the test stays green.**

### 4. Copied indexes mute the read-only proof (false green)

The "the classifier wrote to a repo it promised to only read" proof compares
RAW INDEX FILE bytes before and after (`rescue/fixture_test.go:218-252`,
`rescue/record_test.go:216-231`, `rootmove/fixture_test.go:188-216`) and only
bites when the index stat cache is stale or racily clean. A template copy
hands each test either a fresh stat cache — a buggy reader's rewrite is
byte-identical, the detector mute while the write happens — or re-timed files
and false failures. Baked `fsmonitor`/`cache-tree` index extensions blind plain
`git status` in every copy. This is not hypothetical: the repo records the copy
hazard at `cleanup/fixture_test.go:294-303` ("a copied index's newer mtime
makes a racily clean entry look clean") — the cause of a real 1-in-8 flake.
Related: `TestDirtCoveredRacyCleanEntry` (`cleanup/dirt_test.go:334-386`)
forces the racily-clean shape and cannot exist inside a copy;
`staleFsmonitor` (`removal/root_test.go:1416-1432`) needs the fsmonitor token
its own git wrote. **Missed if unfixed: index-writing regressions in the
read-only classifier and hidden-edit detection.**

### 5. Template lineage scales the masking hazard (masked bug)

The fresh-repo-per-case rule exists because "one wrong success leaves
`.worktrees/<name>`, and the path-collision error then masks every later case"
(`removal/fixture_test.go:15-19`). Shared templates move that failure up one
level: a wrong success while seeding, or a variant mutating a template in
place, poisons every descendant copy at once. Repo-global state a template
carries — `info/exclude` (append-only `IgnoredIn`, `cleanup/fixture_test.go:
114-134`, `removal/fixture_test.go:108-115`), per-repo config, `hook()`
markers (`rootmove/fixture_test.go:218-229`) — flips exact-count assertions
(`'1 ignored entry'`, `IgnoredCount==0`, `fixture.row()`'s
exactly-one-matching-row) without any assertion naming the cause. A
fail-closed precondition at risk of passing for the wrong reason:
`failclosed_test.go:84-97` asserts NO admin `modules/` exists so the
submodule-status check is the one reached. **Missed if unfixed: whole classes
of classification bugs hide behind corrupted fixture premises.**

### 6. Hand-built git state pins the author's format model (masked bug)

Success-verification and held-state tests are defined against what REAL git
reports after REAL operations: `TestRemoveVerifiesGitSuccess` and
`TestInterruptedRemoveLeavesPrunableAndIsReported`
(`removal/worktree_test.go:36-158`) assert against real `worktree list
--porcelain` output. A hand-rolled `.git/worktrees/<name>` admin dir,
rebase-merge/sequencer files or `ls-files -v` tags encode the fixture author's
guess of git's on-disk format: a shape git treats as corrupt makes
"git exit 0 but the registration remains" assertions exercise git's error path
instead of the engine's read-back, and a git upgrade changing a layout passes
every test silently. **Missed if unfixed: engine read-back regressions and
git-upgrade breakage, both green.**

## Constraints (binding; one per mitigation)

1. **Seam state is per-call or fire-counted.** Ruling 1 authorizes the full
   reshape: move test seams off package vars onto per-call state — a
   hooks/env struct threaded through `Plan`/`Options`/`GatherInputs`, the
   pattern `internal/lifecycle`'s `env` struct already uses — across all four
   packages, `internal/cleanup`'s exported surface and `cmd/wt`'s tests. Where
   a global must remain, every armed test asserts its seam actually FIRED at the
   intended rung (fire-count/observation + the exact refusal code), so
   cross-fire fails loudly instead of passing vacuously. Negative guards
   assert the injection ran (`TestExecuteIgnoresChangesOutsideTheSource` must
   assert churn ran AND `StatusMoved`); the rung-ordering table
   (`rescue/execute_test.go:1067-1146`) stays an assertion, never
   green-by-default; the poison gh stub (`removal/testmain_test.go:31-36`)
   stays observable — forgetting to stub must error, not classify with an
   empty listing.
2. **Serial islands are declared and tracked.** The Anti-goal-3 tests are
   permanently serial (they cannot be `t.Parallel`), listed in one place in the
   test infrastructure, and excluded from this project's speedup claims —
   their coverage is tracked, never assumed. The island list self-assembles in
   practice: a `t.Setenv` user cannot call `t.Parallel` at all (the runtime
   panics), and Go's release semantics make the islands sound — top-level
   parallel tests run only after the whole serial scan completes (testing's
   parent barrier), so a serial island holds exclusive process state
   (builder-verified). Never convert `t.Setenv` to `os.Setenv` to force
   parallelism.
3. **Index proofs re-arm their own precondition.** Never share index files
   across tests. A copy re-arms the racily-clean/stale-stat shape in-test (the
   `dirt_test.go:334-386` technique) and asserts that shape held before its
   byte-diff proof is allowed to count; a copy that cannot satisfy the
   precondition fails loudly. Templates carry no `fsmonitor`/`cache-tree`
   extension; a test needing one writes it itself (`staleFsmonitor`).
4. **Templates are immutable base layers.** Variants copy-then-mutate; no
   lineage is shared across mutually exclusive shapes (hidden-index flags,
   forged `MERGE_HEAD`, stopped-rebase dirs, admin `modules/`, packed/loose ref
   splits each get their own base). Every copy passes a lineage-freshness gate
   before the test body — no `.worktrees/<name>`, no `refs/wt/rescued/*`, no
   leftover `refs/heads/topic`, empty `info/exclude`, expected config/hooks —
   whose failure names the poisoned lineage. Exact-count assertions keep their
   pristine premises enforced by that gate. Two template-carried hazards fold
   into the same gate (builder-verified as solvable here): a template holding
   linked worktrees must repair or rewrite their absolute `.git`/`gitdir`
   back-pointers (`git worktree repair`), and the gate checks remote URLs so
   copies never silently share one origin path.
5. **Age and conflict construction stays per-test and explicit.** Any batched
   builder parameterizes all three age sources separately (reflog,
   `packed-refs` mtime, loose-ref mtime) and each age case asserts which
   source its fixture actually presents before classification runs.
   `mustFail`'s non-zero-exit guarantee survives batching: a stopped-on-conflict
   state must genuinely stop.
6. **Every hand-built `.git` shape round-trips through real git.** After
   building one, run the same command the engine reads and assert git reports
   the claimed state (the `worktree_test.go` read-back pattern). A shape real
   git rejects fails setup loudly. This turns Anti-goal 6 into a feature: git
   format drift fails the suite instead of hiding behind it.
7. **The `single-p` classification is re-validated.** `removal`, `rescue`,
   `cleanup`, `rootmove` are excluded from CI's GOMAXPROCS=1 flake-guard job
   because "they do not depend on core count". If any test gains a
   timing/ordering assertion or waits on a goroutine, its package joins the
   guard's list in the same change (and #562's drift guard, once built, covers
   it permanently).
8. **Race-clean by construction (Ruling 2).** The suites stay
   `go test -race` clean under the speedup; per-call seam state is what makes
   this structural. Each implementing PR verifies `go test -race` over the
   four packages and `cmd/wt`'s migrated stub sites and reports it; CI never
   runs `-race`.

## The how — decided

- **Three axes, combined per package as needed:**
  A) `t.Parallel` within a package under the isolation the constraints demand;
  B) immutable seeded template fixtures, copy-then-mutate per test;
  C) batched fixture seeding (fewer real git calls per setup) where Constraint
  5/6 realism checks still pass.
- **Measure first.** Before any restructure: a per-test timing table (from
  `go test -count=1 -json` per package) lands as the baseline. Each package's
  implementing PR carries its before/after table and the CI shard number.
- **One package at a time, lightest first:** `rootmove` (125s, 5 test files)
  proves the pattern end to end, then `cleanup`, `rescue`, and `removal` last
  (largest, most seams). Every step keeps all four suites green.
- **Safety guards are infrastructure, not prose.** Each constraint that can be
  mechanical becomes one shared test helper (lineage-freshness gate,
  real-git round-trip assert, seam fire-count wrapper, per-test env ownership)
  defined once and used everywhere, in the spirit of `gitenv.IsolateForTest`.
- **The `-timeout 20m` stopgap stays** regardless of the speedup (it is hang
  detection, and the runner wobble that tripped 10m is real).
- **No new production-concurrency hazard** (builder-verified):
  `cleanup.commonDirs` is already a `sync.Map` and the gh lookup paths are
  mutex-guarded; parallel tests add none.

## What's left to the builder

- The helper API shapes (Constraint 1's seam struct, Constraint 4's lineage
  gate, Constraint 6's round-trip assert, Constraint 2's serial-island
  registry) and where they live (`internal/testkit`? per-package? follow
  `gitenv`'s precedent).
- The per-test migration plan: which test takes which technique, the 1:1
  assertion mapping (Ruling 3: names may merge — every rename/merge flagged
  for the implementing PR's description), and every deliberately changed
  assertion flagged likewise.
- The template catalogue: the enumerated mutually-exclusive scenario shapes
  and which tests draw from each.
- PR slicing (one per package vs one overall) and the measurement format.

## Rulings (2026-11-09, all resolved)

1. **Seam reshaping: full per-call reshape, authorized.** Test-facing
   fields/signatures in production files may change — behavior-identical —
   across all four packages, `internal/cleanup`'s exported test surface and
   `cmd/wt`'s tests. Carrier: per-call hooks/env structs threaded through
   `Plan`/`Options`/`GatherInputs` (which already carry per-call knobs like
   `Parallel`/`OriginRows`). The four no-arg seams (`beforeUpdateRef`,
   `afterUpdateRef`, `beforePush`, `afterReadWork`) gain parameters or move
   into the hooks struct; the gh seams move off `cleanup.StubPRListForTest`
   onto per-call state, migrating its ~252 swap sites (the ripple into
   `cmd/wt`'s tests is included). The globals + fire-count fallback is
   closed by ruling 2.
2. **Race-clean: required, PR-verified.** The suites must stay
   `go test -race` clean under the speedup — structural under ruling 1's
   per-call seam state. Each implementing PR runs `go test -race` over the
   four packages and the migrated seam call sites (`cmd/wt`'s tests
   included) and reports the result in its description. CI does not run
   `-race`: the wall-clock goal stands.
3. **Test merges: allowed.** Structurally identical cases may merge into
   table-driven tests (fewer `Test*`, same assertions). The migration plan
   keeps a 1:1 ASSERTION mapping, and every implementing PR flags each
   rename or merge in its description.
