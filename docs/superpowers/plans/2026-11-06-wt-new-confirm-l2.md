# `wt -W` Creation Confirmation (L2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `-W <name>` that would create a new branch and worktree asks first on a terminal and refuses without one unless `--new` is passed, so a typo can no longer silently create a branch.

**Architecture:** One classifier in `internal/worktree` decides what a typed name means (reuse a worktree, reuse a branch, track a remote branch, or create). `EnsureForName` and a new `WouldCreate` both use it, so the question is asked about exactly the case that would create. `cmd/wt`'s `RunE` asks before calling `EnsureForName`, on the plain terminal, before any picker starts (the same place L1 put the root gate).

**Tech Stack:** Go (module `wt/`), cobra.

**Spec:** `docs/superpowers/specs/2026-11-06-wt-worktree-by-default-design.md` ("Creating a worktree by typing a name"). This is L2 of L1 → L2 → L3; L1 (root gate, #523) is merged.

## Global Constraints

- A `-W` name is a **creation** only when it matches no existing worktree, no existing local branch, and no remote-tracking ref (the `git worktree add -b` case in `EnsureForName`). Everything else is reused or tracked with no question, exactly as today.
- On a terminal the question is `no branch or worktree named "<name>"; create it from <base>? [y/N]`, default **no**. Quitting or answering no ends the launch with status 0, no message beyond the question, nothing created, nothing recorded (`errCancelled`).
- With no terminal, a creation is refused unless `--new` is passed; the error names `--new`.
- `--new` is valid only with `-W`. With an existing name it is a no-op. With `--cwd`, `--root`, or no `-W` it is a usage error.
- The base is whatever creation uses today (`git worktree add -b` from the directory handed to `EnsureForName`); this plan does not change it. The prompt names it: the current branch, else `detached HEAD <short sha>`.
- A name that `EnsureForName` would reject (default branch forms, path collision, too long, empty) is rejected with that same error **before** any question is asked.
- The picker's "+ New worktree…" prompt and the shared-root `w` choice are unchanged and never ask this question.
- Every `Test*` has a top-level `//` comment saying what it tests and why. Commits end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.

## Review Focus

- Answering no, quitting, or having no terminal leaves nothing behind: no branch, no `.worktrees/<dir>`, no claim file (Task 3).
- An existing local branch with no worktree, an existing worktree, and the root's branch (still refused by `refuseRootBranch`) never produce the question (Tasks 1, 3).
- `origin/feature` style names (tracking an existing remote branch) never produce the question (Task 1).
- An invalid name errors before the question, not after "y" (Tasks 1, 3).
- The prompt's base is right on a branch and on a detached HEAD (Task 1).
- The picker's new-worktree prompt and the shared-root `w` still create without asking (Task 3).
- `--new` with an existing name is a no-op; `--new` without `-W` is a usage error (Task 3).

## File Structure

- `internal/worktree/create.go` — `classifyName`, `WouldCreate`; `EnsureForName` refactored onto the classifier.
- `internal/rootclaim/rootclaim.go` — export `AskYesNo`; `ConfirmRoot` uses it.
- `cmd/wt/main.go` — `--new` flag, validation, the question in the `-W` block.
- `cmd/wt/rootgate.go` — `confirmCreate` (beside `gateRoot`).
- Docs: `wt/CLAUDE.md`, `wt/docs/internals/launch-flow.md`, `wt/README.md`, `docs/guides/06-wt-agents-and-models.md`, `docs/guides/01-initial-setup.md`, the `-W` help text.

---

### Task 1: One classifier for what a typed name means

**Files:**
- Modify: `wt/internal/worktree/create.go`
- Test: `wt/internal/worktree/create_test.go` (the package's existing test file for `EnsureForName`)

**Interfaces:**
- Produces: `func WouldCreate(dir, name string) (creates bool, base string, err error)` — `creates` is true only when `EnsureForName(dir, name)` would run `git worktree add -b` for a branch that does not exist; `base` is the current branch of `dir`, else `detached HEAD <short sha>`; `err` is the error `EnsureForName` would return for the same input (empty name, default-branch form, path collision, too-long name, git failure), with `creates` false.
- `EnsureForName`'s behavior and error messages must not change.

- [ ] **Step 1: Characterize `EnsureForName` first.** Run `go test ./internal/worktree` and confirm existing coverage passes. Add any missing characterization tests (each in a fresh repo) pinning today's behavior: name checked out in a worktree → that path; existing local branch with no worktree → new worktree on it, branch unchanged; `origin/feature` with a remote-tracking ref → worktree tracking it; unknown name → new branch from `dir`'s HEAD; default-branch forms refused; path collision refused; too-long refused. They must pass before the refactor.
- [ ] **Step 2: Write failing tests for `WouldCreate`:** `TestWouldCreate` table — unknown name → `true` with base `main`; same on a detached HEAD → base starts with `detached HEAD `; existing worktree, existing branch, `origin/feature` tracking name, the default branch checked out in the root → `false`, nil error; empty name, `origin/main`, a colliding name (`feature/x` vs existing `feature-x` worktree), a 300-byte name → `false` with the same error text `EnsureForName` returns (assert equality of messages).
- [ ] **Step 3: Run; expect FAIL** (`WouldCreate` undefined).
- [ ] **Step 4: Implement.** Extract the decision order from `EnsureForName` into an unexported `classifyName(dir, name string, entries []Entry) (kind nameKind, path string, err error)` with kinds reuse-worktree, existing-branch, track-remote, create; `EnsureForName` switches on the kind to run the same git commands it runs today; `WouldCreate` lists the worktrees, calls `classifyName` and reports `kind == create`. Use `CurrentBranchAt` and a short `rev-parse` for the base label.
- [ ] **Step 5: Run `go test ./internal/worktree`; expect PASS** (characterization tests unchanged).
- [ ] **Step 6: Commit** — `refactor(wt): one classifier for -W names, and worktree.WouldCreate`.

### Task 2: A reusable y/N prompt

**Files:**
- Modify: `wt/internal/rootclaim/rootclaim.go`
- Test: `wt/internal/rootclaim/rootclaim_test.go`

**Interfaces:**
- Produces: `func AskYesNo(prompt string, in io.Reader, out io.Writer) bool` — prints `wt: ` + `prompt` + ` [y/N]: ` on `out`, reads one key (single-key raw read on a terminal), true only for `y`/`Y`; `ConfirmRoot` becomes a call to it with its existing text, output unchanged.

- [ ] **Step 1: Write failing test `TestAskYesNoDefaultsToNo`:** `y`, `Y` → true; `n`, `\n`, `q`, `\x1b`, `\x03`, end of input → false; output contains the prompt text and `[y/N]`. Keep `TestConfirmRootDefaultsToNo` passing unchanged (it pins `ConfirmRoot`'s exact output).
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement `AskYesNo`** and re-express `ConfirmRoot` through it.
- [ ] **Step 4: Run `go test ./internal/rootclaim`; expect PASS.**
- [ ] **Step 5: Commit** — `refactor(wt): rootclaim.AskYesNo`.

### Task 3: `--new` and the question

**Files:**
- Modify: `wt/cmd/wt/main.go` (flag near the other root-command flags; validation beside L1's exclusivity check; the `-W` block before `EnsureForName`)
- Modify: `wt/cmd/wt/rootgate.go` (add `confirmCreate`)
- Test: `wt/cmd/wt/newflag_test.go` (new); update existing `-W` tests that name a branch that does not exist

**Interfaces:**
- Consumes: `worktree.WouldCreate`, `rootclaim.AskYesNo`, `errCancelled`, `stdinTTY()`, `osStderr`.
- Produces: `--new` (bool, root command only; help: "With -W, create the branch and worktree when the name does not exist yet (default: ask; with no terminal, refuse)"); `func confirmCreate(name, base string, isNew bool, in io.Reader, out io.Writer, isTTY bool) error` — nil when `isNew` or the answer is yes; `errCancelled` on no; with no terminal and not `isNew`, an error containing `--new` (`no branch or worktree named "<name>"; pass --new to create it`).

- [ ] **Step 1: Write failing tests** (launch-capture pattern of `rootflag_test.go`):
  - `TestWorktreeFlagUnknownNameWithoutTerminalRefusesNamingNew`: error contains `--new`; afterwards `git branch --list <name>` is empty and `.worktrees/<name>` does not exist.
  - `TestWorktreeFlagUnknownNameAskedOnATerminal`: answer `n` → nil, nothing created; answer `y` → branch and worktree created, agent launched in it; the question text contains the name and the base.
  - `TestWorktreeFlagNewCreatesWithoutAsking`: `--new` with an unknown name creates and launches, reading nothing from `in`.
  - `TestWorktreeFlagExistingNamesNeverAsk`: an existing local branch with no worktree, an existing worktree → launch with `failReader`, no terminal, no `--new`; the root's branch still returns the L1 `--root` refusal, not the question.
  - `TestWorktreeFlagInvalidNameErrorsBeforeTheQuestion`: `origin/main` and a 300-byte name → their `EnsureForName` errors, `failReader` never read.
  - `TestNewFlagUsage`: `--new` without `-W`, and with `--cwd` or `--root`, each a usage error; `--new` with an existing name is a no-op.
  - `TestOtherCreationPathsDoNotAsk`: the shared-root `w` choice (`confirmSharedRoot`) and `tui`'s new-worktree command still create with no question.
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement.** In the `-W` block, after resolving the repo root: `creates, base, err := worktree.WouldCreate(root, name)`; return `err` if set; if `creates`, call `confirmCreate` (`errCancelled` → return nil, silencing cobra as the root gate does); then `EnsureForName` and L1's `refuseRootBranch` as today. Validate `--new` next to the exclusivity check.
- [ ] **Step 4: Run `go test ./cmd/wt`.** Existing tests that pass `-W` with a name that does not exist now fail; add `--new` to them (or create the branch first when the test is about reuse). Do not weaken the new tests.
- [ ] **Step 5: Run `gofmt -l .`, `go vet ./...`, `go test ./... -count=1`; expect PASS.**
- [ ] **Step 6: Commit** — `feat(wt): -W asks before creating a branch; --new answers it`.

### Task 4: Docs

**Files:**
- Modify: `wt/CLAUDE.md` (key-flags table `-W` row and a `--new` row; Worktree section), `wt/docs/internals/launch-flow.md`, `wt/README.md` flag table, `docs/guides/06-wt-agents-and-models.md` (lines showing `claude-wt -W my-feature`), `docs/guides/01-initial-setup.md` (the `claude-wt -W smoke-test` example), the `-W` flag help in `main.go`.

- [ ] **Step 1: Update the docs** to say: `-W <name>` creates only after you say yes (or pass `--new`); an existing branch or worktree is reused silently; with no terminal a new name needs `--new`; the picker's own prompt and the shared-root choice are unchanged. Examples that create a new worktree gain `--new` or a one-line note.
- [ ] **Step 2: Grep** the repo (excluding `docs/superpowers` and `docs/archive`) for other scripts, skills and docs that run `wt -W`/`*-wt -W` with a name that may not exist, and update or list them.
- [ ] **Step 3: Run `bin/check-links`; expect no broken links.**
- [ ] **Step 4: Commit** — `docs: -W confirmation and --new`.

### Task 5: Acceptance on the built binary

**Files:** none.

- [ ] **Step 1: Build to a temp dir** and run with the redirected environment from `wt/CLAUDE.md` against a scratch repo (one commit on `main`, one linked worktree `feat`, one branch `spare` with no worktree), `-A shell -- pwd`.
- [ ] **Step 2: Non-terminal checks:** `-W typo` refuses naming `--new` and leaves no branch or directory; `-W typo --new` creates and prints the new worktree; `-W feat` and `-W spare` launch silently; `-W main` and `-W origin/main` error with the guard's message (no question); `--new` alone, `--new --cwd` and `--new --root` are usage errors; `-W typo --new` a second time reuses silently.
- [ ] **Step 3: Terminal checks through a pty** (80x24, answer the OSC 11 query before any key): `-W typo` answered `n`, Enter and `y`; a detached-HEAD repository shows `detached HEAD` in the base. If no pty can be driven, report these as not run.
- [ ] **Step 4: Report** commands and outputs for the PR description.

---

## Self-review

- **Spec coverage:** ask on a terminal, default no (T2–3); refuse without a terminal unless `--new` (T3); existing names reused silently (T1, T3); `--new` validity rules (T3); base named, unchanged (T1); picker row and shared-root `w` unchanged (T3 test); error-before-question (T1, T3).
- **Behavior change to call out in the PR:** scripts or docs that create a worktree with `claude-wt -W newname` and no terminal now need `--new`.
- **Proportion:** five tasks; the only structural change is the classifier extraction in T1, which the spec's "one rule" requires.
