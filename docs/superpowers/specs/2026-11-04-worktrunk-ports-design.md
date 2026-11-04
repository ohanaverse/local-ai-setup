# Worktrunk Ports PRs 2–5 — Series Design (handoff)

> **For agentic workers:** this is a series SPEC covering four future PRs, not a
> single plan. Each [PR 2–PR 5] section below is scoped to be picked up
> independently. At a PR's start, run `superpowers:writing-plans` against that
> section (PRs 2–3 earn their own dated plan in `docs/superpowers/plans/`; PRs
> 4–5 may be executed straight from their section with
> `superpowers:executing-plans`). The checkbox task lists are for tracking.

**Goal:** Port the four remaining ranked ideas from worktrunk
(https://github.com/max-sixty/worktrunk) into `wt` — safe worktree removal and
prune, JSON output discipline, copy-ignored seeding, tether teardown — without
importing worktrunk's hook/alias or merge-pipeline surface.

**Architecture:** All four ports extend `wt`'s existing structures rather than
adding subsystems: removal lives beside the creation code it reuses
(`branchAtPath`, `listWorktrees`, the default-branch guard); the JSON contract
follows the `docs/contracts/` fixture pattern the model side already uses;
copy-ignored bolts onto the creation paths after `git worktree add`; tether is
bookkeeping on `internal/sessionclaim` plus a signaling safety layer. Worktrunk
supplies the safety-kernel *design* (dirty gate, integrated-checks, force
scopes, `branch_outcome`); `wt` keeps its narrow launcher surface — it deletes
worktrees, it does not become a merge tool.

**Tech Stack:** Go 1.26 (module root `wt/`), git porcelain + plumbing,
APFS `clonefile(2)` (`unix.Clonefile`), JSON Schema + sample fixtures in
`docs/contracts/`.

**Sources:** `docs/reference/worktrunk-comparison.md` (**uncommitted in the main
checkout as of 2026-11-04** — see Source-doc status) and
https://github.com/max-sixty/worktrunk (read its docs and source; its binary is
also named `wt`, so run it only from a scratch directory in a spike, **never on
PATH**).

## Status at handoff (2026-11-04)

- **PR 1 — full-branch-name worktree paths: BUILT** (delivered in the same PR
  as this file). `worktreeDirName` in `wt/internal/worktree/create.go` derives
  the worktree directory from the **full** branch name — path-hostile
  characters (`/`, `\`, `:`, ASCII control chars) flatten to `-`, derived
  `.`/`..` are rejected — and all four creation paths use it (`EnsureForName`,
  `EnsureForBranch` local and new-branch cases, `addTrackingWorktree`).
  `feature/x` + `bug/x` now coexist (`.worktrees/feature-x`,
  `.worktrees/bug-x`) instead of racing for `.worktrees/x`. The three
  user-approved calls, locked: (1) that sanitize rule; (2) sanitize collisions
  (`feature/x` vs `feature-x`) reuse the existing `worktree path … is already
  in use by branch …` error — no new machinery; (3) no template config —
  worktrunk's minijinja `worktree-path` is deliberately not ported. Compat: the
  reuse loop matches by branch name, so old basename-layout worktrees
  (`.worktrees/x` holding `feature/x`) resolve with no migration
  (`TestEnsureForNameReusesSlashedBranchWorktree` pins this).
- **PRs 2–5: not started.** Everything below about worktrunk's behavior comes
  from docs and source reading — neither tool has been executed (the research's
  Gaps section says so explicitly). Kernel edge behavior (error paths, exact
  reap triggers) is *inferred*; the worktrunk spike (PR 2 task T0, decided at
  handoff 2026-11-04) pins these before any schema does.
- **Source-doc status:** `docs/reference/worktrunk-comparison.md` exists only in
  the main checkout's working tree, uncommitted (written 2026-11-04; three
  post-PR-516 edits applied). A pickup session should: commit it; fill its
  "Decision (open)" section with the 5-PR packaging below; and refresh its two
  stale rows claiming "`-W` never consults remotes" (feature table +
  "Deliberately absent") — `-W origin/<name>` resolution merged 2026-11-04.

## Global constraints (every PR inherits all of these)

- TDD with the red-green-revert cycle; no production code without a failing
  test watched first. Each PR gets a fresh worktree
  (`superpowers:using-git-worktrees` → `EnterWorktree`).
- Any `docs/contracts/` fixture change rides in the **same change** as the code
  that reads or writes it (the repo's cross-language rule; existing pattern:
  a `docs/contracts/*.sample.*` fixture plus a Go contract test holding it).
- Guides document the command, never live state (no counts or snapshots in
  `docs/guides/`); a user-visible behavior change updates `wt/CLAUDE.md` and
  the matching `wt/docs/wt-agents/*.md` page in the same PR.
- `wt` Go tests (from `wt/CLAUDE.md`): top-level `//` what-and-why comment on
  every `Test*`; a test that creates worktrees builds a fresh repo per case;
  branch name `topic`, not `feature`, when a case needs `feature/<x>`;
  `gitenv.IsolateForTest()` in TestMain (package already does); never the
  stock `toml.Encoder` on the registry; a scratch registry sets
  `WT_REGISTRY` **and** `WT_LITELLM_CONFIG`; a scratch `modeltiers run` sets
  `MODELTIERS_OUT` and `MODELTIERS_CACHE`.
- This repo squash-merges PRs: any "is this branch merged" verdict must be
  **squash-aware (patch-id)**, and PR bodies never contain
  `close`/`fix`/`resolve` followed by `#N` — write `Refs #N`.
- Commits end `Co-Authored-By: Claude Code <noreply@anthropic.com>`; PR bodies
  end the `🤖 Generated with [Claude Code](https://claude.com/claude-code)` line.
- Verification before any commit: `go test ./...`, `go vet ./...`, `make check`
  in `wt/`; `make test-all` at the repo root for the CI mirror (at minimum
  `make check-links` whenever any `.md` changes).
- Worktrunk itself runs only from a scratch directory in a spike, never on
  PATH (binary-name collision with our `wt`).
- Destructive-scope rule: reaping and deletion are side effects of `remove`
  and inherit its force scopes and confirmations; anything purely additive
  (tethering a command, seeding files) stays safe by default.

## Shared vocabulary (worktrunk semantics being ported)

- **Dirty gate:** `git status --porcelain` **including untracked** blocks
  removal; gitignored files are exempt.
- **Integrated** (branch deletion allowed) = 6 checks, all passing: same
  commit; ancestor; empty 3-dot diff; matching trees; simulated merge adds
  nothing; patch-id match (**squash-aware**). The walk is capped at 500
  commits.
- **Force scopes, never conflated:** `--force` = proceed despite a dirty
  worktree; `-D` = delete an unmerged branch.
- **Trash-rename + single drain:** at removal, rename the worktree directory
  to a trash name and delete it in one background drain (never stepwise
  `rm`); `worktree lock`-ed worktrees are immune to removal.
- **`branch_outcome` enum (stable strings):** `deleted` / `deferred` /
  `not_attempted` / `retained_*`. Designed for incremental delivery: PR 2
  reports `deferred` for branch deletion, PR 3 flips those to
  `deleted`/`retained_*` with **no contract change** — the staging seam ships
  inside the vocabulary.
- **JSON discipline:** versioned envelope with a published schema (like
  worktrunk's `list-v2.json`); absent-vs-`null` distinguishes "nothing" from
  "not determined"; exactly one structured result even on nonzero exit; stable
  outcome enums.
- **`.worktreeinclude`:** gitignore-syntax filter file at the repo root
  selecting which ignored paths seed into new worktrees; de-facto shared with
  Claude Code desktop's worktree feature (one repo-level config can govern
  seeding across tools).

## PR 2 — `wt remove` + JSON contract (large; ports #2a + #4)

**Depends on:** nothing (parallel with PR 1). **Size:** large — roughly 2× the
PR 516 change.

### Scope

**In:**
- `wt remove <worktree>` removes one linked worktree: dirty gate (modified
  tracked + untracked block; gitignored exempt), one confirmation prompt
  (skippable with `--yes`), trash-rename + single background drain,
  `worktree lock` immunity.
- Force scopes as flags from day one: `--force` (dirty worktree) and `-D`
  (unmerged branch — its end state is the git `-d`/`-D` split, decided at
  handoff 2026-11-04; see the matrix in PR 3). Branch *deletion* is
  reporting-only in this PR: `deferred` when the removed worktree had a
  branch (with or without `-D`), `not_attempted` when it had none
  (detached HEAD).
- JSON contract from day one: `--json` emits the versioned envelope with a
  published schema; the schema, its sample fixture, and the Go contract test
  land in the same change (`docs/contracts/`, following the
  `model-list.sample.json` + `cmd/wt/model_list_contract_test.go` pattern).
- The ignored-file death hazard: ignored files are invisible to git and die
  with the directory (`.superpowers/sdd` ledgers, `.venv`, local `.env`).
  `remove` must surface what is about to die before it dies (at minimum, the
  confirmation prompt names ignored-file count/size; the mechanism is the
  plan's one open call).
- Registry-protected artifacts: a registered `local_path` model artifact
  under the target (from `bin/mlx-quantize` or the omlx / `mlx_lm_server`
  local-path fields) makes `remove` **refuse outright and print the paths** —
  the repo invariant is that wt never deletes these (cleanup of a failed
  experiment is a manual `rm -rf`), and `--force` does not override it.
  Test: `TestRemoveRefusesRegisteredLocalPathArtifacts`. (Answered by the
  invariant in `wt/CLAUDE.md`, builder-check 2026-11-04.)
- Live sessions: the confirmation prompt names processes whose cwd is inside
  the tree (a by-cwd `ps` scan) and states they will be orphaned until PR 5's
  `--reap` lands — a warning, not a gate, since removal under a running agent
  is exactly the flow `--reap` completes in the end state and wt's claims
  philosophy is fail-open. Session-claim records are left alone: claims are
  root-launch-only today (so none name a worktree session), and
  `docs/contracts/session-claims.md` already makes stale claims tolerated and
  self-healing. **My call — say so at pickup if you'd rather hard-gate on a
  live session.**

**Out:** the 6-check kernel and actual branch deletion (PR 3), `prune`
(PR 3), reaping/tether (PR 5), merge tooling (never).

### Files (expected)

- Create: `wt/internal/worktree/remove.go`, `wt/internal/worktree/remove_test.go`
  — same package as `create.go` (reuses `branchAtPath`, `listWorktrees`,
  `refExists`, `refuseDefaultBranch` unexported helpers).
- Create: `docs/contracts/worktree-remove.sample.json` (+ published schema
  file beside it), held by `wt/internal/worktree/remove_contract_test.go`.
- Modify: `wt/cmd/wt/` (command + flags `--force`, `-D`, `--yes`, `--json`),
  `wt/CLAUDE.md` (Worktree/Lifecycle contract lines), `wt/docs/wt-agents/README.md`
  (command table), a `docs/guides/` maintenance page if removal belongs there.

### Decided values

- Outcome strings: `deleted` (worktree), `deferred` / `not_attempted` /
  `retained_*` (branch), exactly as the vocabulary section. Envelope key names
  are the one thing to pin at plan time against worktrunk's `list-v2.json`
  shape (adopt its envelope keys where they fit; the rules above are
  non-negotiable either way).
- `--force` never deletes branches. PR 2 never deletes branches at all: the
  branch stage reports `deferred` when the worktree had a branch and
  `not_attempted` when it had none — the git-parity answer to the `-D`
  question (decided at handoff 2026-11-04) means even an integrated branch
  is `deferred` here; PR 3 owns every `deleted`/`retained_*` verdict.

### Tasks (coarse — expand to steps with `superpowers:writing-plans`)

- [ ] T0 Worktrunk spike — the decided prerequisite (2026-11-04): install
      worktrunk in a scratch directory (never on PATH — binary-name
      collision) and exercise `remove`/`prune`/tether against real error
      paths (dirty-gate edges incl. ignored-only dirt and untracked
      detection, lock immunity, trash-drain crash recovery, reap triggers).
      Record observed semantics in this doc's Shared vocabulary section in
      a small commit BEFORE any schema pins them.
- [ ] T1 Remove kernel: dirty gate, confirmation, trash-rename + single
      background drain, lock immunity. Tests: `TestRemoveRefusesDirtyTracked`,
      `TestRemoveRefusesDirtyUntracked`, `TestRemoveAllowsGitignoredOnlyDirt`,
      `TestRemoveLockedWorktreeImmune`, `TestRemoveTrashRenamesThenDrains`,
      `TestRemoveDefaultBranchRefused` (reuses the guard).
- [ ] T2 Outcome reporting + JSON envelope + `docs/contracts/` fixture +
      contract test. Tests: `TestRemoveJSONEnvelopeVersioned`,
      `TestRemoveOutcomeEnumStable`, `TestRemoveJSONAbsentVsNull`,
      `TestRemoveJSONOneResultOnNonzeroExit`.
- [ ] T3 CLI wiring + docs (`wt/CLAUDE.md`, `wt/docs/wt-agents/README.md`,
      guides) in the same PR.
- [ ] T4 Three-lens review + fresh full verification + PR (body uses `Refs`,
      never a closing keyword).

### Acceptance

- `wt remove` refuses a worktree with modified tracked or untracked files,
  proceeds when only ignored files are dirty, and never touches the default
  branch or a locked worktree.
- `--json` output validates against the published schema; the same shape is
  emitted on failure with the error inside the envelope.
- No branch is ever deleted by this PR's code path; outcomes report
  `deferred` (branch present) / `not_attempted` (detached) exactly as the
  git-parity matrix requires.

### Review focus (this PR)

- Ignored-but-valuable files dying silently (the death hazard) — pinned by
  `TestRemoveAllowsGitignoredOnlyDirt` plus the prompt-surfacing test.
- Trash-drain leaving a half-deleted directory after a crash mid-drain —
  pinned by `TestRemoveTrashRenamesThenDrains` (re-running `remove`/`prune`
  must finish the drain, not refuse).

## PR 3 — integrated-only branch deletion + `wt prune` (large; port #2b)

**Depends on:** PR 2 (removal machinery + outcome enum). **Size:** large —
roughly 2× the PR 516 change.

### Scope

**In:**
- The 6-check integrated kernel, verbatim from the vocabulary section, with
  the 500-commit walk cap. A branch is deleted when every check passes; with `-D`, behind its own
  confirmation, the branch is deleted regardless of the kernel verdict.
  The end-state matrix (git parity, decided at handoff 2026-11-04):
  **without `-D`** — integrated → `deleted`; unmerged →
  `retained_unmerged`; checks unable to prove (walk cap, CAS skip) →
  `retained_capped` / the plan's chosen skip value. **With `-D`** —
  `deleted` regardless of the merge verdict, with one carve-out: the CAS
  identity skip stands (force never deletes a ref that moved). Force deletes *despite* a verdict, never *in
  place of* one, so the kernel always runs and always reports.
- CAS-raced deletion: the ref is deleted atomically against the OID the
  checks validated (`git update-ref -d <ref> <old-oid>` semantics) so a
  concurrent push can never turn a passed check into a wrong deletion.
- `--min-age` guard on `wt prune` candidates; candidates that are locked,
  dirty, or the default branch are skipped (the default-branch rule is
  absolute — see `refuseDefaultBranch`).
- Outcome flips: `deferred` becomes `deleted` or `retained_*` — **no schema
  change** (the staging seam was designed in).
- `wt prune`: the batch form of the same kernel. Its exact candidacy list
  (stale registrations vs. live-but-unwanted worktrees) is the one item to
  settle at plan time from worktrunk's prune docs/source.

**Out:** per-worktree interactive removal (PR 2 owns it), reaping (PR 5).

### Files (expected)

- Create: `wt/internal/worktree/integrated.go` (+ tests) — the 6 checks as
  one predicate over (branch, default-branch history).
- Modify: `wt/internal/worktree/remove.go` (deletion call sites), `wt/cmd/wt/`
  (`wt prune`, `--min-age`), the PR 2 fixture only via **additive** enum
  values already reserved (`deleted`, `retained_*`).

### Decided values

- The 6 checks and the 500 cap are fixed; no seventh check, no configurable
  cap in v1.
- The full `retained_*` member set is pinned when PR 2's fixture lands (PR 3
  must not change the schema), with `retained_unmerged` (check failed) and
  `retained_capped` (walk cap hit) mandatory; the PR 2 plan names any
  further member PR 3 needs (e.g. a CAS-race skip). This extends the "one
  thing to pin at plan time" from envelope keys to outcome members.
  (Settled at builder-check triage, 2026-11-04.)
- `--min-age` takes a duration; default decided at plan time but must be
  non-zero (protects worktrees created moments ago).

### Tasks (coarse)

- [ ] T1 `isIntegrated` kernel. Tests: `TestIntegratedSameCommit`,
      `TestIntegratedAncestor`, `TestIntegratedEmptyThreeDot`,
      `TestIntegratedMatchingTrees`,      `TestIntegratedMergeAddsNothing`,
      `TestIntegratedPatchIdSquash` (squash-merged branch passes — the case
      `git branch -d` gets wrong in this very repo), `TestIntegratedWalkCap500`.
- [ ] T2 CAS deletion + outcome flips. Tests: `TestDeleteRefCasRaced`,
      `TestOutcomeDeferredFlipsToDeletedOrRetained`.
- [ ] T3 `wt prune` + `--min-age` + skips (locked/dirty/default). Tests:
      `TestPruneSkipsLockedDirtyDefault`, `TestPruneMinAge`.
- [ ] T4 Docs + review + verification + PR.

### Acceptance

- A squash-merged branch (this repo's norm) is recognized as integrated and
  deleted; a genuinely unmerged branch is `retained_unmerged` and untouched
  without `-D`, deleted only with `-D` behind its own confirmation.
- Concurrent ref movement between check and delete causes a safe skip, not a
      wrong deletion.

### Review focus (this PR)

- Patch-id false results on merge commits / criss-cross history — pinned by
      the squash + cap tests with hand-built fixtures.
- The 500-commit walk silently giving up and deleting anyway — the cap must
      yield `retained_*`, pinned by `TestIntegratedWalkCap500`.

## PR 4 — copy-ignored (medium; port #3)

**Depends on:** PR 1 (seeding must target full-branch-name paths). **Size:**
medium (a copy engine + a gitignore matcher + tests). Purely additive — it
can move earlier or in parallel if cold-start pain outranks the removal gap.

### Scope

**In:**
- Automatic seeding after `git worktree add` in both creation paths: carry
  selected gitignored files (dependency trees like `llmbench/.venv`,
  `modeltiers/.venv`, `node_modules`, build caches) into the new worktree so
  a `wt -W` launch does not start cold; non-regenerable files like a local
  `.env` are carried **only** when `.worktreeinclude` opts in (see Decided
  values — it is neither a cache nor regenerable and may hold secrets).
  Optionally a standalone `wt copy-ignored <src> [dst]` for manual seeding of
  an existing tree.
- Copy engine: `unix.Clonefile` (APFS reflink — shares blocks until first
  write, so a 2 GB `.venv` seeds in milliseconds at ~zero disk) per file,
  with a plain-copy fallback for cross-device/non-APFS, and
  skip-if-destination-exists — **never overwrite** what is already there.
- Filter: `.worktreeinclude` at the repo root (gitignore pattern semantics),
  plus a hard-coded exclude list that is never seedable: `.git*`,
  `.worktrees/`, sockets, FIFOs, pidfiles, lock state, known tool state.
- File-type rules: symlinks are copied as links (never followed); non-regular
  files are skipped outright.
- Fallback bound: the plain-copy path gets a size/time budget or warning —
  reflink is instant, `cp -r` of a huge tree is not.

**Out:** machine-scoped state (`~/.omlx`, provider data, LiteLLM config) —
same scoping rule as PR 5; seeding is repo-tree-local. `wt step promote`
(swap branch + ignored files into the primary worktree) is deliberately not
ported.

### Decided values

- **Regenerable caches only by default.** Seeding *state* would recreate the
  cleanup problem PR 2 solves; the excludes keep the feature on the cache
  side of that line. Named tension: `.superpowers/sdd` ledgers are gitignored,
  worktree-local, and worth carrying — the filter can express them, but that
  is a deliberate per-repo choice via `.worktreeinclude`, never a default.
- Overwrite never happens; existing destination files always win.
- With no `.worktreeinclude` at the repo root, seeding uses a built-in
  default set of well-known regenerable cache paths (`.venv/`,
  `node_modules/`, `target/`, `dist/`, `__pycache__/`; the plan finalizes
  the list). The file's patterns add to that set; `!` patterns subtract
  (gitignore semantics). **My call — say so at pickup if a present file
  should instead replace the defaults entirely.**

### Tasks (coarse)

- [ ] T1 Filter + excludes. Tests: `TestWorktreeincludePatterns`,
      `TestSeedExcludeListNeverCopiesPidfileSocketOrDotGit`.
- [ ] T2 Clonefile engine + fallback + skip-existing + symlink/special-file
      rules. Tests: `TestSeedClonesIgnoredCaches`,
      `TestSeedSkipsExistingDestination`, `TestSeedSymlinkCopiedAsLink`,
      `TestSeedPlainCopyFallbackBounded`.
- [ ] T3 Hook into both creation paths + optional `wt copy-ignored` + docs.
      Tests: `TestEnsureForNameSeedsIgnoredFiles`.
- [ ] T4 Review + verification + PR.

### Acceptance

- A new worktree for `feature/x` arrives with the `.worktreeinclude`-selected
  caches present and identical in content, at near-zero disk cost on APFS.
- Nothing excluded (pidfile, socket, `.git*`) ever appears in the new tree;
  nothing already present is overwritten.

### Review focus (this PR)

- Seeding state (not caches) recreating the cleanup hazard — pinned by the
  exclude-list test.
- Cross-device plain-copy of a huge tree hanging the create path — pinned by
  `TestSeedPlainCopyFallbackBounded` (seeding must never block `EnsureForName`
  beyond the budget).

## PR 5 — tether / process teardown (small, conditional; port #6)

**Depends on:** PR 2 (the "worktree died" event and the force-scope vocabulary
it rides on). Packaging decided at handoff 2026-11-04: **standalone PR 5** (signal
handling gets its own review; PR 3 stays focused on the destructive core).

### Scope

The problem: a worktree dies but the processes inside it don't — orphans hold
a deleted cwd, keep RAM/GPU/ports (in this environment: a ~28 GB mtplx
resident, a loaded omlx model, a bound dev port), or keep an agent loop
spending tokens against a vanished tree. Worktrunk answers with an explicit
tether command plus `wt remove --reap`. **`wt` has the structural advantage of
already being the launcher**, so the port is bookkeeping + a signaling safety
layer:

**In:**
- **Implicit tether at launch:** every launched agent's process group is
  recorded as bound to its worktree, extending `internal/sessionclaim`'s
  registry (`<git-common-dir>/wt-sessions/<pid>` — pid + start-time, which
  already defeats pid reuse; contract `docs/contracts/session-claims.md`,
  which `repo-cleanup` already reads).
- **`wt remove --reap`:** at removal, find processes whose cwd is inside the
  worktree and TERM → grace → KILL them (covers children wt did not track).
- **Explicit `wt tether <cmd…>…`** only for processes wt did not launch (a
  dev server started by hand in the worktree) — thin wrapper that runs the
  command and binds its process group.
- **Janitor pass:** removal happens outside wt too (`repo-cleanup`, plain
  git), so a later wt command (picker open, `wt list`, `wt remove`) notices
  "path gone, claim alive" and reaps — tombstone semantics, not a removal
  hook.

### Decided values (the scoping rules — this design's real work)

- **Machine-singleton providers are exempt:** mtplx, rec, sushi, omlx are
  one-instance-per-machine (port-scoped) — a dying worktree must never stop
  them. The by-cwd rule scopes this naturally (their cwd is not the
  worktree); anything explicit must check the provider registry.
- **Verify identity before signaling:** confirm the pid is a same-user
  process of the expected shape before any signal — the discipline
  `wt stop sushi` already follows. Never trust a recorded pid alone.
- **Fail open:** a reap failure is a `wt: note:`, never a refusal or a blocked
  removal (consistent with wt's claims philosophy).
- **Destructive parts inherit remove's force scopes and confirmations;**
  `tether` alone is additive and safe. SIGTERM → grace → SIGKILL ordering.

### Tasks (coarse)

- [ ] T1 Launch-time tether registry on `sessionclaim`. Tests:
      `TestLaunchRecordsProcessGroup`, `TestRegistryDefeatsPidReuse`.
- [ ] T2 `remove --reap` by-cwd sweep + identity verification. Tests:
      `TestReapKillsWorktreeCwdProcesses`,
      `TestReapExemptsMachineSingletons`,
      `TestReapSignalsOnlyAfterIdentityVerify`, `TestReapFailsOpen`.
- [ ] T3 `wt tether <cmd…>` + janitor pass (extend
      `docs/contracts/session-claims.md` in the same change). Tests:
      `TestTetherReapsProcessGroupOnWorktreeDeath`,
      `TestJanitorReapsWhenPathGoneClaimAlive`.
- [ ] T4 Docs + review + verification + PR.

### Acceptance

- Removing a worktree with a live tethered agent reaps its process group; a
  hand-started server inside it is reaped by `--reap` or the janitor; mtplx /
  rec / sushi / omlx survive every removal.
- A worktree deleted by `repo-cleanup` or plain git is reaped on the next wt
  command that touches the claims registry.

### Review focus (this PR)

- Pid reuse → wrong process signaled — pinned by
  `TestReapSignalsOnlyAfterIdentityVerify` and the registry's start-time pin.
- A machine singleton (28 GB mtplx) dying because a worktree did — pinned by
  `TestReapExemptsMachineSingletons`.

## Addressing shortcuts (port #5) — assigned to PR 6 (decided 2026-11-04)

The remainder of the addressing port — `-` (previous worktree), `^`
(default-branch-as-root), `pr:{N}`, PR web URL for `-W` and the picker —
lands as a small **PR 6** after the series (the remote-name half was
overtaken when `-W origin/<name>` picker-parity resolution merged
2026-11-04; the rest is independent QoL surface and must not grow PR 2).
Scope for that PR's own plan: the four address forms for `-W` and the
picker, reusing `remoteRefParts`/`refuseDefaultBranch` for anything that
resolves a name.

## Not ported (deliberate — do not rebuild these)

- The hook/alias subsystem (10 hook types, minijinja pipelines, `wt-<name>`
  subcommands) — scope creep for a launcher.
- The merge/squash/rebase pipeline (`wt merge`, `step squash/rebase/push`) —
  launched agents own their git workflow; only the integrated-check *kernel*
  belongs in a `remove`.
- The 8-tab picker preview — wt's picker is deliberately thin (worktree →
  agent → model).
- `wt step promote` — see PR 4 scope-out.

## Decided at handoff (2026-11-04) — the builder-check questions raised to the user

1. **`-D` end-state matrix → git parity.** The 6-check kernel gates the
   default (integrated → `deleted`; unmerged → `retained_unmerged`; unable
   to prove → `retained_capped`/skip) and `-D`, behind its own
   confirmation, deletes the branch regardless of the kernel verdict. PR 2's
   mapping follows: `deferred` whenever a branch exists, `not_attempted`
   when detached. (The source material's "delete an unmerged branch" wins
   over its "unmerged is retained and untouched" line, which now reads as
   the no-`-D` default.)
2. **Addressing-shortcut remainder → a small PR 6** after the series (its
   scope is pinned in the port's section above).
3. **PR 5 packaging → standalone PR 5.**
4. **Worktrunk spike → run first:** it is PR 2's task T0, and its observed
   semantics update this doc's Shared vocabulary before any schema pins
   them.

## Open questions for the user at pickup

1. **Comparison-doc housekeeping:** commit
   `docs/reference/worktrunk-comparison.md` from the main checkout, fill its
   "Decision (open)" section with this packaging, refresh its two stale
   "`-W` never consults remotes" rows.

Builder-check questions already settled in this document: what `remove` does
about live sessions and claim records (PR 2 scope, warning-not-gate — my
call), whether `.env` seeds by default (it does not; PR 4 decided values) and
the no-`.worktreeinclude` default set (PR 4 decided values — my call),
registry-protected `local_path` artifacts (PR 2 scope, hard refusal per the
repo invariant), and the `retained_*` pinning rule (PR 3 decided values).

## Review Focus (series-level — the five most likely bites)

1. **Squash-merge verdicts** (this repo squash-merges): patch-id false
   negatives/positives deciding a branch's death — pinned in PR 3
   (`TestIntegratedPatchIdSquash`, `TestIntegratedWalkCap500`).
2. **Pid reuse** causing the wrong process to be signaled — pinned in PR 5
   (`TestReapSignalsOnlyAfterIdentityVerify`).
3. **Machine singletons** (mtplx/rec/sushi/omlx) killed by teardown — pinned
   in PR 5 (`TestReapExemptsMachineSingletons`).
4. **Ignored files dying silently** with a removed worktree (the recorded
   death hazard) — pinned in PR 2 (dirty-gate + prompt-surfacing tests).
5. **Seeding state instead of caches** recreating the cleanup problem PR 2
   solves — pinned in PR 4 (exclude-list + never-overwrite tests).
