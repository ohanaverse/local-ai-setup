# `wt` Exit Prompt (L3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Status: done.** Built and merged as #526 (Refs #521, which stays open for the follow-ups #530 and #531). Task 6's acceptance ran on 2026-11-07 against the installed binary with a real claude and is recorded in the spec's "Left to the builder". The checkboxes below are left as written (the plan was executed by sub-agents, not ticked off here).

**Goal:** When the agent exits, a worktree (and branch) this launch created and nobody touched is offered for removal with one keypress, through the same removal engine as `wt remove`.

**Architecture:** A new package `internal/exitprompt` holds the launch-side record ("this launch created branch B at tip T in path P", in memory only), the eligibility predicate over the engine's own preview, and the prompt. The three creation routes record; the four post-exit tails (non-TUI single model, non-TUI slot profile, TUI single model, TUI slot profile) call `Offer` after the stop picker and before the summary line. Removal is `Engine.Preview` → `ConfirmedDeletingBranch` → `Engine.Run`, so rescan, compare-and-swap and hard blockers still apply.

**Tech Stack:** Go (module `wt/`), cobra, Bubble Tea (`internal/tui`).

**Spec:** `docs/superpowers/specs/2026-11-06-wt-worktree-by-default-design.md` ("Exit prompt (#521)", including its ruling on the min-age rule). L1 (#523) and L2 (#524) are merged.

## Global Constraints

- Eligible only when **all** hold: this launch created the *branch and* its worktree (a new worktree on an existing branch, `--cwd`, `--root`, and a reused worktree never qualify); the branch tip still equals the tip recorded at creation; `Engine.Preview` shows no losses (`LossLines`) and no running processes; no hard blocker; the row's status is less sure and every soft reason code is `fresh_branch` or `base_is_local`; its evidence is `same-commit` or `ancestor`; `CanDeleteBranch` is true; a terminal is attached.
- Not eligible → keep silently. An error while checking → one line `wt: note: could not check <path> for removal (<cause>); kept`, never a failed launch.
- The prompt is one line, **Enter keeps** (default no): `<path> was created for this session and nothing changed in it. Remove it and its branch <name>?` via `rootclaim.AskYesNo`.
- "y" removes the worktree **and** its branch through `ItemPreview.ConfirmedDeletingBranch(Needs{Confirm: true, Delete: true})` then `Engine.Run`. Nothing else deletes. An engine refusal or skip is printed (the engine's own `Headline`/`DetailLines`) and the worktree is kept.
- The creation tip is held **in memory** by the launching process. Nothing is written to disk to remember it.
- The agent's exit status is unchanged by the prompt, a "y", or any failure in this step.
- Setting: `[launch] exit_prompt = "ask" | "keep"` in `~/.config/agent-wt/config.toml`; absent means `ask`; there is **no** value that removes without a keypress.
- Position in the post-exit order, identical on all four tails: release → survey → stop picker → **worktree prompt** → summary line → after-survey stats → pricing notice.
- Every `Test*` has a top-level `//` comment saying what it tests and why. Commits end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.

## Review Focus

- A touched worktree never prompts: an uncommitted file, a new commit, an ignored file, a process still running in it (Task 3).
- Enter, "n", end of input, an engine refusal and a moved tip all leave the worktree **and** the branch in place, and the agent's exit status is preserved (Tasks 3, 4).
- Only routes that create the *branch* record: a reused worktree, a new worktree on an existing branch (picker bare-branch row), `--cwd` and `--root` never prompt (Task 2).
- No terminal never prompts (Task 4).
- The prompt lands after the stop picker and before the summary on both launch paths and for slot and non-slot profiles (Task 4).
- `exit_prompt = "keep"` is honoured, a bad value is rejected naming the key, and the table survives a config save (Task 1).
- A repository with no remote still offers (via `base_is_local`) (Task 3).
- A claude launch (which writes and restores `<worktree>/.claude/settings.local.json`) leaves a worktree the engine still calls clean (Task 6; do not loosen the predicate to hide a failure).

## File Structure

- `internal/config/config.go` (+ a small `launch.go` beside it) — `LaunchState`, `Config.ExitPromptKeep()`.
- `internal/exitprompt/exitprompt.go` (new) — `Record`, `Recorded`, `Reset`, `Eligible`, `Offer`.
- `cmd/wt/main.go`, `cmd/wt/sharedroot.go`, `internal/tui/new_worktree.go`, `internal/tui/sharedroot.go` — creation routes call `Record`.
- `cmd/wt/launch.go` (`runAgentCmd`), `cmd/wt/slot_launch.go` (`runSlotAgentCmd`), `internal/tui/app.go` (`printPendingSummaryAndSurvey`, both branches) — call `Offer`.
- Docs: `wt/CLAUDE.md`, `wt/docs/wt-agents/README.md` (post-exit order), `wt/docs/internals/launch-flow.md`, `wt/docs/configuration.md`, `wt/README.md`, `wt/CHANGELOG.md`.

---

### Task 1: The `[launch]` setting

**Files:**
- Modify: `wt/internal/config/config.go` (add `LaunchTable *LaunchState` beside `LitellmTable`); create `wt/internal/config/launch.go`
- Test: `wt/internal/config/launch_test.go`

**Interfaces:**
- Produces: `type LaunchState struct { ExitPrompt string \`toml:"exit_prompt"\` }`; `Config.LaunchTable *LaunchState \`toml:"launch,omitempty"\``; `func (c *Config) ExitPromptKeep() bool` (true only for `"keep"`); load-time validation rejecting any value other than `""`, `"ask"`, `"keep"` with an error naming `[launch] exit_prompt` and the allowed values, in the style of the other config validation errors.

- [ ] **Step 1: Write failing tests:** `TestLaunchExitPromptDefaultsToAsk` (no table, `""`, `"ask"` → `ExitPromptKeep()` false; `"keep"` → true); `TestLaunchExitPromptRejectsOtherValues` (`"remove"`, `"kepp"` → load error containing `exit_prompt`); `TestLaunchTableSurvivesASave` (a config with `[launch] exit_prompt = "keep"` goes through `PatchSave` for an unrelated change and still has it, so wt's own writes do not drop a hand-edited table).
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement** as specified, following how `LitellmTable` is declared, loaded and preserved by `PatchSave`/`lockedApply`.
- [ ] **Step 4: Run `go test ./internal/config`; expect PASS.**
- [ ] **Step 5: Commit** — `feat(wt): [launch] exit_prompt setting`.

### Task 2: Creation routes record what they made

**Files:**
- Create: `wt/internal/exitprompt/exitprompt.go` (record half only)
- Modify: `wt/cmd/wt/main.go` (the `-W` block), `wt/cmd/wt/sharedroot.go` (`confirmSharedRoot`'s `w`), `wt/internal/tui/new_worktree.go` (`ensureNewWorktreeCmd`), `wt/internal/tui/sharedroot.go` (`sharedRootWorktreeCmd`)
- Test: `wt/internal/exitprompt/exitprompt_test.go`; route tests beside each (`cmd/wt/newflag_test.go`, `cmd/wt/sharedroot_test.go`, `internal/tui/new_worktree_test.go`, `internal/tui/sharedroot_test.go`)

**Interfaces:**
- Produces: `type Created struct { Path, Branch, Tip string }` (`Branch` is the short name; `Tip` the full sha); `func Record(path, branch string) error` (reads `git rev-parse HEAD` in `path` immediately, stores under a mutex; the latest call wins); `func Recorded() (Created, bool)`; `func Reset()` (tests).
- Consumes: `worktree.WouldCreate` (L2) to know whether a typed name creates a branch.

- [ ] **Step 1: Write failing tests:** `TestRecordStoresTheCreationTip` (a fresh repo, create a worktree with `EnsureForName`, `Record` → `Recorded()` returns that path, branch and the worktree's HEAD sha); `TestRecordedIsEmptyUntilRecorded`. Route tests, each in a fresh repo with `Reset()` first: `-W typo --new` and `-W typo` answered yes record; `-W existing-worktree`, `-W existing-branch` (branch with no worktree), `--root`, `--cwd` do not; the shared-root `w` choice (non-TUI and TUI) records; the picker's typed new name records, while typing an existing name or choosing a bare-branch row (`EnsureForBranch`) does not.
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement** `Record`/`Recorded`/`Reset`, and call `Record` on each route only when a new branch was created (`WouldCreate` true for the typed-name routes; always for the auto-named shared-root worktree). A `Record` error is a one-line note, never a failed launch.
- [ ] **Step 4: Run `go test ./internal/exitprompt ./cmd/wt ./internal/tui`; expect PASS.**
- [ ] **Step 5: Commit** — `feat(wt): creation routes record the branch they created`.

### Task 3: Eligibility and the offer

**Files:**
- Modify: `wt/internal/exitprompt/exitprompt.go`
- Test: `wt/internal/exitprompt/offer_test.go`

**Interfaces:**
- Consumes: `cleanup.Gather/Classify`, `removal.Engine` (`Preview`, `Run`), `removal.ItemFromRow`, `ItemPreview.ConfirmedDeletingBranch`, `rootclaim.AskYesNo`, `cleanup.ResolvePath`.
- Produces:
  - `type Env struct { Inputs func(cwd string) (cleanup.GatherInputs, error); Procs removal.ProcFinder; Now func() time.Time; Keep bool }` — built by the caller (`cmd/wt` owns `protectedPaths`/`cleanupInputs`; `Inputs` must return fetch off and the protected artifacts).
  - `func Eligible(p removal.ItemPreview, c Created) (ok bool, why string)` — the predicate in Global Constraints, `why` naming the first failing condition.
  - `func Offer(env Env, in io.Reader, out io.Writer, isTTY bool)` — returns immediately when nothing was recorded, `env.Keep`, or `!isTTY`; otherwise gathers, classifies with the default min-age, finds the row by resolved path, previews it through an `Engine`, checks `Eligible`, asks, and on yes confirms and runs; prints the engine's headline and detail lines; never returns an error and never exits.

- [ ] **Step 1: Write failing tests** (real fresh repos with a bare `origin`, an engine with a stub `ProcFinder`; the matrix is one test per row, each with a top-level comment):
  - `TestOfferUntouchedWorktreeRemovesWorktreeAndBranchOnYes` — answer `y`: path gone, `git branch --list <name>` empty, headline printed.
  - `TestOfferDeclineKeepsEverything` — `n`, Enter, end of input: worktree and branch intact.
  - `TestOfferIsSilentWhenTouched` — table: an untracked file; a modified tracked file; a new commit; an ignored file; a stubbed running process → no output, nothing removed, `in` never read (use `failReader`).
  - `TestOfferStillEligibleAfterAResetToTheCreationTip` — commit then `reset --hard` to the creation tip with a clean tree → offered.
  - `TestOfferWithoutARemoteStillOffers` — repository with no `origin` (reason `base_is_local`) → offered.
  - `TestOfferIsSilentWhenTheBranchStartedAheadOfTheBase` — created from a HEAD with commits not in `origin/main` → not eligible.
  - `TestOfferSkipsWhenTheTipMovesAfterTheQuestion` — a seam (or a hook in `in`) moves the tip between ask and run → the engine's `skipped_changed` is printed, nothing removed.
  - `TestOfferKeepAndNoTerminalAreSilent` — `Keep: true`; `isTTY=false`; nothing recorded.
  - `TestOfferReportsACheckErrorAsANote` — `Inputs` returns an error → the one-line note, nothing removed, no panic.
  - `TestEligibleNamesTheFailingCondition` — each failing condition returns a distinct `why`.
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement.** Reuse `ItemFromRow`, never hand-build an `Item` (the engine seals approvals). Keep the predicate a pure function of `ItemPreview` and `Created`.
- [ ] **Step 4: Run `go test ./internal/exitprompt`; expect PASS.**
- [ ] **Step 5: Commit** — `feat(wt): exit prompt eligibility and offer`.

### Task 4: Call it from the four tails

**Files:**
- Modify: `wt/cmd/wt/launch.go` (`runAgentCmd`), `wt/cmd/wt/slot_launch.go` (`runSlotAgentCmd`), `wt/internal/tui/app.go` (`printPendingSummaryAndSurvey`, both the profile and non-profile branches), `wt/cmd/wt/main.go` (hand the TUI its `Env`, as `tui.SharedRoot` is handed)
- Test: `wt/cmd/wt/exitprompt_test.go`, `wt/internal/tui/exitprompt_test.go`

**Interfaces:**
- Consumes: `exitprompt.Offer`, `Env`; `Config.ExitPromptKeep()` (Task 1); `stdinTTY()`.
- Produces: `tui.ExitPrompt exitprompt.Env` package var (set by `cmd/wt` before `tui.Run`, like `tui.SharedRoot`).

- [ ] **Step 1: Write failing tests:**
  - `TestExitPromptRunsAfterTheStopPickerAndBeforeTheSummary` — for `runAgentCmd`, `runSlotAgentCmd`, and the TUI's profile and non-profile tails: record the order of release, stop picker, prompt, summary and assert it.
  - `TestExitPromptNeverChangesTheAgentExitStatus` — agent exits 3 and the answer is `y`; agent exits 0 and the engine refuses; wt's status is the agent's in each.
  - `TestExitPromptSkippedWithoutATerminalOrWhenKept` — non-TTY and `exit_prompt = "keep"` print nothing on both paths.
  - `TestCommandAgentTailsOfferToo` — a `shell` launch in a created worktree is offered like a model launch (find every tail with `grep -n 'runStopPicker\|runStopPhase'`; any other tail that runs after a worktree launch gets the same call).
- [ ] **Step 2: Run; expect FAIL.**
- [ ] **Step 3: Implement.** Build the `Env` once in `cmd/wt` (`protectedPaths(a)` for the inputs; a failure there means the offer prints its note and keeps). Call `Offer` immediately after the stop picker in each tail and before the summary line; the TUI calls it after the alt screen is gone, as the stop picker does.
- [ ] **Step 4: Run `gofmt -l .`, `go vet ./...`, `go test ./... -count=1`; expect PASS.**
- [ ] **Step 5: Commit** — `feat(wt): offer to remove an untouched session worktree on exit`.

### Task 5: Docs

**Files:**
- Modify: `wt/CLAUDE.md` (Post-exit flow; Config section's "holds only …" sentence now names `[launch]`; Worktree section), `wt/docs/wt-agents/README.md` (post-exit order), `wt/docs/internals/launch-flow.md`, `wt/docs/configuration.md`, `wt/README.md`, `wt/CHANGELOG.md`, `docs/guides/06-wt-agents-and-models.md`

- [ ] **Step 1: Update the docs** to say: the new post-exit order; what makes a worktree eligible and that anything else is kept silently; Enter keeps; "y" removes the worktree and its branch through the engine; the `[launch] exit_prompt` setting (`ask`/`keep`, no value removes unasked); only branches this launch created qualify.
- [ ] **Step 2: Run `bin/check-links`; expect no broken links.**
- [ ] **Step 3: Commit** — `docs: exit prompt for an untouched session worktree`.

### Task 6: Acceptance on the built binary

**Files:** none.

- [ ] **Step 1: Build to a temp dir** and run with the redirected environment from `wt/CLAUDE.md` against a scratch repo with a bare `origin`, `-A shell`.
- [ ] **Step 2: Non-terminal:** `-W topic --new -- true` creates, prints no prompt, keeps the worktree and branch.
- [ ] **Step 3: Terminal checks through a pty** (80x24; answer the OSC 11 and CSI 6n queries): `-W topic --new -- true` then `n`, Enter and `y` (after `y` the worktree directory and the branch are gone); `-- touch x`, `-- git commit --allow-empty -m x`, an ignored file written by the command, and a `sleep` left running in the background → no prompt each time; `[launch] exit_prompt = "keep"` → no prompt; an existing worktree and an existing branch (`-W existing`) → no prompt; the agent's exit status (`-- sh -c 'exit 3'`) is 3 after a `y`.
- [ ] **Step 4: Claude settings file.** With a stub `claude` executable on `PATH` that exits at once and a one-model registry, launch `-A claude -W topic --new` and confirm whether the worktree is still offered afterwards (the profile writes and restores `.claude/settings.local.json`). If it is not offered, report exactly what the engine calls a loss; do not change the predicate in this task.
- [ ] **Step 5: Report** commands, outputs and anything not run for the PR description.

---

## Self-review

- **Spec coverage:** eligibility and the ruling on min-age (T3), in-memory creation tip (T2), all routes that create a branch (T2), Enter keeps and `ask`/`keep` only (T1, T3), position in the post-exit order on both paths and exit status unchanged (T4), claude settings-file question (T6).
- **Behavior change to call out in the PR:** a new prompt at the end of sessions in worktrees this launch created; `keep` in `[launch] exit_prompt` turns it off.
- **Risks:** the claude settings file (T6); a `fresh_branch`-only row also needs the branch to have started at or behind the base, so a worktree created from a HEAD ahead of the base is never offered (accepted, documented in the design ruling).
- **Proportion:** six tasks; one new package, no classifier or engine change.
