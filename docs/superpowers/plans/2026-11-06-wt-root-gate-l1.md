# `wt` Root Gate (L1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A launch reaches the repository root only by a deliberate act: `--root`, or a confirmed `--cwd` standing in it. The picker no longer offers the root, and `-W <the root's branch>` is refused.

**Architecture:** The decision is made once, in `rootCmd`'s `RunE`, on the *resolved* launch directory, before any picker or `runLaunchPath` work. A small `cmd/wt/rootgate.go` holds the gate and the `-W` refusal; `worktree.PrimaryOf` finds the root from anywhere; the picker drops the primary checkout's row. The existing shared-root menu (`sessionclaim` / `confirmSharedRoot` / the TUI screen) is untouched and still runs downstream when another live session holds the root.

**Tech Stack:** Go (module `wt/`), cobra, Bubble Tea (`internal/tui`).

**Spec:** `docs/superpowers/specs/2026-11-06-wt-worktree-by-default-design.md` ("The launch changes", "Where a launch goes"). This plan is L1 only; L2 (`-W` creation confirmation, `--new`) and L3 (exit prompt) get their own plans.

## Global Constraints

- Flag name is exactly `--root`. `--root`, `--cwd` and `-W` are mutually exclusive: any two together is a usage error.
- `--root` finds the root by checkout (primary entry of `git worktree list`), never by branch name. It refuses outside a repository and in a bare repository.
- `--root` is the consent: no prompt. It does **not** waive the shared-root menu; `--allow-shared-root` still pre-answers that.
- `--cwd` whose directory is the root: asks on a terminal (default **no**); with no terminal it refuses and names `--root`. A linked worktree or a subdirectory of the root is not the root and is not gated (known limit, as today).
- If another live session holds the root, only the shared-root menu is shown, never also this prompt.
- A launch under a `wt` session that already holds the root (a parent process's claim, e.g. `shell-wt --cwd -- npm test` run by an agent at the root) is not gated.
- `-W <name>` where the resolved path is the root is refused with a message pointing at `--root`, for any root branch.
- The picker has no root row. Quitting the prompt ends the launch with status 0, no message, no claim, nothing recorded (same as quitting the shared-root menu: `errCancelled`).
- Every `Test*` has a top-level `//` comment saying what it tests and why. Commits end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.

## Review Focus

- `--root` run from a linked worktree and from a subdirectory launches in the primary checkout, not the current directory (Task 5).
- A symlinked path to the root (macOS `/var` vs `/private/var`) is still recognised as the root by the gate and the `-W` refusal (Task 4).
- `--cwd` in a subdirectory of the root is not gated and launches silently (Task 5).
- Another live session on the root plus `--cwd`: one question (the shared-root menu), not two (Task 4).
- An agent at the root running a nested `wt --cwd` is not newly refused (Task 4).
- Standing in the root, the picker opens with "+ New worktree…" selected, and standing in a linked worktree it still opens on that worktree (Task 6).
- Quitting the prompt leaves no claim file behind (Task 5).

## File Structure

- `internal/worktree/enumerate.go` — add `PrimaryOf`, `Entry.Root`; `RootOf` reuses `PrimaryOf`.
- `internal/sessionclaim/sessionclaim.go` — add `AncestorHolds`.
- `internal/rootclaim/rootclaim.go` — add `ConfirmRoot`.
- `cmd/wt/rootgate.go` (new) — `gateRoot`, `refuseRootBranch`.
- `cmd/wt/main.go` — `--root` flag, exclusivity, wiring of the gate and the refusal.
- `internal/tui/worktree_list.go` — `buildList` skips `Entry.Root`.
- Docs: `wt/CLAUDE.md`, `wt/docs/internals/launch-flow.md`, `wt/README.md` flag table, the design doc.

---

### Task 1: Find the primary checkout from anywhere; mark it on the picker's entries

**Files:**
- Modify: `wt/internal/worktree/enumerate.go` (`RootOf`, `Entry`, `listWorktrees`)
- Test: `wt/internal/worktree/enumerate_test.go` (or the package's existing test file for these functions)

**Interfaces:**
- Produces: `func PrimaryOf(dir string) (string, error)` — the primary checkout's path, symlinks resolved; error for a bare repository, a path outside any repository, or a git failure. `Entry.Root bool` — true only for the primary checkout's entry.
- Consumes: existing `runGit`, `RootOf`.

- [ ] **Step 1: Run the existing `RootOf` tests** (`go test ./internal/worktree -run RootOf`) and note they pass; they must still pass after the refactor.
- [ ] **Step 2: Write failing tests** (use the package's `gitInit` helper and a real `git worktree add` for the linked case):
  - `TestPrimaryOfFromRootLinkedAndSubdir`: `PrimaryOf` of the root, of a linked worktree, and of `<root>/sub` all equal the root with symlinks resolved.
  - `TestPrimaryOfBareAndOutside`: a bare repository and a plain `t.TempDir()` each return a non-nil error.
  - `TestListWorktreesMarksOnlyThePrimary`: a repo with one linked worktree yields exactly one entry with `Root == true`, its `Path` the root; in a bare repository with a linked worktree no entry has `Root == true`.
- [ ] **Step 3: Run them; expect FAIL** (`PrimaryOf` and `Entry.Root` undefined).
- [ ] **Step 4: Implement `PrimaryOf`** by parsing the first block of `git worktree list --porcelain` run in `dir` (error on `bare`), then `filepath.EvalSymlinks`. Rewrite `RootOf` as: resolve `dir`, call `PrimaryOf(resolved)`, `ok` when it succeeds and equals `resolved`. In `listWorktrees`'s `flush`, set `Root` on the first block only (track a `seen` flag that is set on the first flush even for a bare entry, so a bare repository's first linked worktree is not marked).
- [ ] **Step 5: Run `go test ./internal/worktree`; expect PASS** (new and existing).
- [ ] **Step 6: Commit** — `feat(wt): worktree.PrimaryOf and Entry.Root`.

### Task 2: Know when a parent session already holds the root

**Files:**
- Modify: `wt/internal/sessionclaim/sessionclaim.go`
- Test: `wt/internal/sessionclaim/sessionclaim_test.go`

**Interfaces:**
- Produces: `func AncestorHolds(commonDir string, c Claim) (bool, error)` — true when `commonDir/wt-sessions` holds a parseable claim on `c.Path` whose pid is this process or one of its parents (`ancestors()`); no `wt-sessions` directory means false. Takes no lock and changes nothing. **Corrected after review (2026-11-06):** the claim must also be *live* — the contract's liveness rule (`docs/contracts/session-claims.md`: pid running and started as recorded; an unreadable start time counts as not held) — so a stale claim whose pid was recycled never waives the gate; pinned by `TestAncestorHoldsIgnoresAStaleAncestorsClaim`.

- [ ] **Step 1: Write failing tests** with the package's claim-writing helper:
  - `TestAncestorHoldsSeesAParentsClaim`: a claim file named `os.Getppid()` on path P → `AncestorHolds` true for a `Claim{Path: P}`.
  - `TestAncestorHoldsIgnoresOtherSessions`: a claim for an unrelated live process (a spawned `sleep` child, not an ancestor) → false.
  - `TestAncestorHoldsNoClaimsDir`: no `wt-sessions` directory → false, nil error.
  - `TestAncestorHoldsOtherPath`: a parent's claim on a different path → false.
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement `AncestorHolds`** reading the directory the way `scan` does (claim files are named by pid; skip `.lock` and temp files) and testing each parsed claim's pid against `ancestors()`.
- [ ] **Step 4: Run `go test ./internal/sessionclaim`; expect PASS.**
- [ ] **Step 5: Commit** — `feat(wt): sessionclaim.AncestorHolds`.

### Task 3: The y/N root prompt

**Files:**
- Modify: `wt/internal/rootclaim/rootclaim.go`
- Test: `wt/internal/rootclaim/rootclaim_test.go`

**Interfaces:**
- Produces: `func ConfirmRoot(r Root, in io.Reader, out io.Writer) bool` — prints `wt: <r.Path> is the repository root (branch <r.Branch>). Work here instead of in a worktree? [y/N]: ` on `out`, reads one key from `in` (single-key raw read on a terminal, as `readChoice` does), returns true only for `y`/`Y`.

- [ ] **Step 1: Write failing test `TestConfirmRootDefaultsToNo`**: inputs `"y"`, `"Y"` → true; `"n"`, `"\n"`, `"q"`, `"\x1b"`, `"\x03"` and an empty reader (end of input) → false; the output contains the root path, the branch and `[y/N]`.
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement `ConfirmRoot`**, factoring the raw-terminal handling out of `readChoice` into a shared helper rather than duplicating it.
- [ ] **Step 4: Run `go test ./internal/rootclaim`; expect PASS** (existing `Choose` tests included).
- [ ] **Step 5: Commit** — `feat(wt): rootclaim.ConfirmRoot`.

### Task 4: The gate and the `-W` refusal

**Files:**
- Create: `wt/cmd/wt/rootgate.go`
- Test: `wt/cmd/wt/rootgate_test.go`

**Interfaces:**
- Consumes: `worktree.RootOf`, `rootclaim.Lookup/Self/ConfirmRoot`, `sessionclaim.Others/AncestorHolds`, `errCancelled` (`cmd/wt/sharedroot.go`).
- Produces:
  - `func gateRoot(launchPath, agent string, in io.Reader, out io.Writer, isTTY bool) error` — nil when `launchPath` is not a root; nil when another live session holds the root (the shared-root menu follows) or an ancestor session holds it; otherwise with no terminal an error containing `--root` (`the repository root <path> needs your say-so to launch in; pass --root, or -W <name> to use a worktree`); on a terminal `ConfirmRoot`, `errCancelled` on no. A claims-directory read failure counts as "no other session" and still asks.
  - `func refuseRootBranch(name, path string) error` — non-nil, naming `--root`, when `worktree.RootOf(path)` says `path` is the root; nil for a linked worktree.

- [ ] **Step 1: Write failing tests** (reuse `sharedRootRepo(t)`, the live-other-session claim helper and `failReader` from `sharedroot_test.go`):
  - `TestGateIgnoresNonRoots`: a linked worktree, a subdirectory of the root and a directory outside any repository → nil, no read from `in`.
  - `TestGateNoTerminalRefusesNamingRoot`: at the root, `isTTY=false` → error whose text contains `--root`.
  - `TestGateTerminalAnswers`: `"y"` → nil; `"n"` → `errors.Is(err, errCancelled)`.
  - `TestGateLeavesTheQuestionToTheSharedMenu`: another live session holds the root → nil with `failReader`.
  - `TestGateAllowsANestedLaunchUnderAHeldRoot`: a claim written for `os.Getppid()` → nil with `failReader`, `isTTY=false`.
  - `TestGateSeesTheRootThroughASymlink`: `launchPath` is a symlink to the root → treated as the root (refuses with no terminal).
  - `TestRefuseRootBranch`: the root path → error containing `--root`; a linked worktree path → nil.
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement** `gateRoot` and `refuseRootBranch` as specified; the "others" read is `sessionclaim.Others` on `root.CommonDir` with `root.Self(agent)`.
- [ ] **Step 4: Run `go test ./cmd/wt -run 'Gate|RefuseRoot'`; expect PASS.**
- [ ] **Step 5: Commit** — `feat(wt): root gate and -W root refusal`.

### Task 5: Wire `--root`, the exclusivity rule, the gate and the refusal into `RunE`

**Files:**
- Modify: `wt/cmd/wt/main.go` (flag registration near line 548; checks after the `--cwd`-value check near line 354; `--root` branch before the `-W` block near line 469; refusal after `EnsureForName`; gate in the `--cwd` block near line 501)
- Test: `wt/cmd/wt/rootflag_test.go` (new); update `wt/cmd/wt/sharedroot_test.go` and any other `--cwd`-at-a-root tests that now fail

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces: `--root` (bool, on the root command only, help: "Launch in the repository's primary checkout, from anywhere; asks nothing (default for a bare wt is a worktree)").

- [ ] **Step 1: Write failing tests**, following the launch-capture pattern of `TestNamedWorktreeLaunchDecidesOnTheResolvedDir`:
  - `TestRootFlagLaunchesInThePrimaryFromALinkedWorktreeAndASubdir`: cwd a linked worktree, then `<root>/sub`; `--root -A shell -- true` launches with dir == root.
  - `TestRootFlagIsMutuallyExclusive`: `--root --cwd`, `--root -W x`, `--cwd -W x` each return an error containing `mutually exclusive`, launching nothing.
  - `TestRootFlagOutsideARepositoryRefuses`: error, nothing launched.
  - `TestWorktreeFlagNamingTheRootBranchIsRefused`: `-W <root's branch>` → error containing `--root`, nothing launched, no claim written.
  - `TestCwdAtRootWithoutTerminalRefusesNamingRoot`, `TestCwdInASubdirectoryOfRootIsNotGated`, `TestQuitAtTheRootPromptEndsTheLaunchQuietly` (returns nil, no `wt-sessions` claim file for this pid).
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement the wiring.** After the `--cwd`-value check: count `--root`, `--cwd`, `-W`; more than one → `fmt.Errorf("--root, --cwd and -W are mutually exclusive: each says where to launch")`. `--root` branch: `worktree.PrimaryOf(".")` (wrap a failure as `not in a git repo` / `no primary checkout`), then `runLaunchPath(..., root, root)`. `-W` block: call `refuseRootBranch` on the path `EnsureForName` returned. `--cwd` block: call `gateRoot(wd, agent, os.Stdin, osStderr, stdinTTY())`; `errCancelled` → return nil; other errors set `SilenceErrors`/`SilenceUsage` as `holdSharedRoot` does.
- [ ] **Step 4: Run `go test ./cmd/wt`.** Existing tests that launch with `--cwd` inside a repository root will now fail; for each, switch the launch to `--root` or, where the test is about `--cwd` itself, answer through the gate's `in`/`isTTY` (a package-level seam alongside `stdinTTY` if needed). Do not weaken the new tests to make them pass.
- [ ] **Step 5: Run `go vet ./... && go test ./...`; expect PASS.**
- [ ] **Step 6: Commit** — `feat(wt): --root, and a root launch needs consent`.

### Task 6: The picker drops the root row

**Files:**
- Modify: `wt/internal/tui/worktree_list.go` (`buildList`)
- Test: `wt/internal/tui/worktree_list_test.go`

**Interfaces:**
- Consumes: `Entry.Root` (Task 1).

- [ ] **Step 1: Write failing tests:**
  - `TestBuildListOmitsThePrimaryCheckout`: groups holding a `Root: true` entry on `main` plus a linked worktree → no list item has the root's path; the linked worktree is present.
  - `TestPickerStartsOnNewWorktreeWhenStandingInTheRoot`: repo root is the launch directory → after the load message the selected item is the `kindNewWorktree` sentinel.
  - `TestPickerStartsOnTheCurrentLinkedWorktree`: launch directory is a linked worktree → the selected item is that worktree's `(current)` row.
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement:** in `buildList`'s entry loop, `continue` when `e.Root`. The existing start-selection code (`selectFirstEntry` on the `(current)` label) then selects nothing when standing in the root, leaving the sentinel at index 0; confirm that and add no new selection code. Leave `isDefaultBranchOnly` as is (a linked worktree on the default branch made outside wt still needs the warning).
- [ ] **Step 4: Run `go test ./internal/tui`; expect PASS** (update any existing picker test that expected a root row).
- [ ] **Step 5: Commit** — `feat(wt): picker no longer offers the repository root`.

### Task 7: Docs, and a correction to the design

**Files:**
- Modify: `wt/CLAUDE.md` (key-flags table; the "Guard, init seeding, sessions" bullet; the Worktree "Default branch is never a linked worktree" note), `wt/docs/internals/launch-flow.md` (entry table, "Shared-root check" skips paragraph), `wt/README.md` (flag table), `--allow-shared-root` help text in `main.go`, and the design doc.

- [ ] **Step 1: Update the docs** to say: `--root` exists and is exclusive with `--cwd`/`-W`; `--cwd` at the root asks (no terminal: refuses, names `--root`); `-W <root's branch>` is refused; the picker has no root row; the gate runs in `RunE` before `runLaunchPath`; a nested launch under a held root is not gated.
- [ ] **Step 2: Make `wt/CLAUDE.md`'s "Default branch is never a linked worktree" note accurate.** `EnsureForName` returns an already-checked-out branch's path *before* its default-branch refusal, so `-W main` with the root on `main` resolves to the root today (and, after this plan, is refused by `refuseRootBranch`); #516's refusal covers only the not-checked-out forms (`origin/main`, `<remote>/HEAD`). The design doc already says this.
- [ ] **Step 3: Run `bin/check-links` from the repo root; expect no broken links.**
- [ ] **Step 4: Commit** — `docs: root gate (L1) behaviour and design correction`.

### Task 8: Acceptance on the built binary

**Files:** none.

- [ ] **Step 1: Build to a temp dir** (`D=$(mktemp -d)`, `go build -o "$D/wt" ./cmd/wt`) and run it with the redirected environment from `wt/CLAUDE.md` against a scratch repo with one linked worktree.
- [ ] **Step 2: Non-terminal checks** (`-A shell -- pwd`): `--cwd` at the root exits non-zero naming `--root`; `--root` from the linked worktree and from a subdirectory prints the root; `--root --cwd` and `-W <root's branch>` fail with their messages; `--cwd` in a subdirectory of the root prints that subdirectory.
- [ ] **Step 3: Terminal checks** through a pty (see `memory/tui-screens-via-pty`; answer the OSC 11 query; 80x24): `--cwd` at the root with `n` and with `y`; a bare `wt` standing in the root opens on "+ New worktree…" with no root row. If no pty driver is available, report these as not run rather than inferring them from the model-level tests.
- [ ] **Step 4: Report** the commands and outputs in the PR description.

---

## Self-review

- **Spec coverage:** `--root` (T4–5), exclusivity (T5), root gate with default-no and no-terminal refusal (T3–5), subdirectory/linked-worktree not gated (T4–5), one question when another session holds the root (T4), `-W` root refusal (T4–5), picker row dropped (T1, T6), docs (T7). The "`--root` does not waive the shared-root menu" rule needs no code: the shared check stays downstream and is covered by the existing tests.
- **Found while planning:** the design's claim that `-W main` is refused today was wrong when the root is on `main`; the design is corrected in the same commit as this plan. A nested `wt` launched by an agent at the root would have been newly refused; T2 and T4 exist to prevent that.
- **Behavior change to call out in the PR:** scripts that run `wt --cwd` in a repository root with no terminal now refuse and must pass `--root`; `-W` together with `--cwd` is now an error.
- **Proportion:** eight tasks for roughly 250 lines of production code plus tests.
