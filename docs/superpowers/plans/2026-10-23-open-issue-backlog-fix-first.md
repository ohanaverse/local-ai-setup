# Open Issue Backlog: Fix-First Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the open bugs and existing-behavior issues from the 2026-10-23 issue review in severity order, per the approved design, before touching the parked #405 / #432 features.

**Architecture:** Fixes land one issue at a time, in the design's proposed order (#399, #423, #403, #449, #389, #447), each as its own PR from `main` in its own worktree; the parked features are out of scope for this plan.

**Tech Stack:** Go 1.26.7 (`wt/` module `github.com/ohanaverse/local-ai-setup/wt`) for #399, #423, #449, #389, #447; Go + Python (`llmbench/`) for #403; `gh` CLI; pytest via `uv` for any llmbench-side test.

**Spec:** `docs/superpowers/specs/2026-10-23-open-issue-backlog-fix-first-design.md`.

## Global Constraints

- Run from `wt/`: `go build ./... && go vet ./... && go test ./...` must pass at the end of every task that changes Go code; repo gate `make test-all` from the repo root before each PR.
- No new features in this pass (#405, #432 stay parked).
- Scope note: this plan is deliberately **one** plan — the fix-first backlog — not per-subsystem plans. The issues it sequences span wt / llmbench / modelman, but they are one ordered backlog, not independent subsystems; the parked features are out of scope.
- Guides never embed live model state (`docs/guides/`); fixes touch `wt/docs/` / `wt/docs/wt-agents/` / `wt/CLAUDE.md` / `wt/CHANGELOG.md` only where an issue says so.
- No test may reach the developer's GitHub account or real config home; use the existing stubs (`StubPRListForTest`, scratch HOME fixtures).
- Commit messages end with the Co-Authored-By line naming the model that wrote the commit; PR bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.

## Review Focus

Inputs and conditions the spec implies but the obvious tests would miss, most likely first. Each has a pinned test in the task named in brackets.

1. #399: whether the cancel is lost between Claude Code and LiteLLM, or between LiteLLM and omlx; a fix must cover both directions. [Task 1]
2. #423: the stop picker's `ProbeTrusted` rule must not offer a stop for a family it did not probe, while the start's others check treats an answering unprobed family as unknown. [Task 2]
3. #403: repeat-probe run-to-run spread before treating the answer-shape difference as a fix; sampling penalties as a possible cause. [Task 3]
4. #449: the chosen repricing behavior for kept `:variant` rows and dropped-suffix handling. [Task 4]
5. #389: a stale tag must only appear via a true head-repo / head-commit match, never via a reused branch name. [Task 5]
6. #447: the shared gate helper must keep ollama- and openrouter-specific exit wording distinct where it should. [Task 6]

## File Structure

| File | Responsibility | Tasks |
|---|---|---|
| `wt/internal/lifecycle/`, `wt/internal/localmodels/` | #423 fix + tests | 2 |
| `wt/internal/cloudsync/`, `wt/cmd/wt/` | #449 fix + tests; #447 gate dedupe | 4, 6 |
| `wt/internal/worktree/`, `wt/internal/tui/` | #389 stale-tag fix + tests | 5 |
| `llmbench/` (probe, eval) | #403 repeat probes / sampling penalties / upstream report | 3 |

## Tasks

### Task 1: #399 — abandoned omlx generations hold memory after client exit

**Files:**
- Modify: `wt/internal/lifecycle/*` (proxy shutdown / upstream cancel wiring), plus docs the issue directs.

**Interfaces:**
- Consumes: the LiteLLM → upstream stream shutdown path and the stop/kill path.
- Produces: cancel-on-disconnect behavior; or a narrower clear path if the investigation finds one.

- [ ] **Step 1: Investigate where the cancel is lost** — Claude Code → LiteLLM vs LiteLLM → omlx — using the issue's commands and logs; record findings in the task/PR notes.
- [ ] **Step 2: Write the failing test** for the chosen surface (upstream stream cancel on proxy shutdown).
- [ ] **Step 3: Run it to verify it fails.**
- [ ] **Step 4: Implement the minimal cancel path.**
- [ ] **Step 5: Run the tests and verify they pass.**
- [ ] **Step 6: Commit.**

### Task 2: #423 — invisible provider family in the others check

**Files:**
- Modify: `wt/internal/lifecycle/lifecycle.go` (`ProbeTrusted`), `wt/internal/lifecycle/evictions.go` (`residentOthers`), `wt/internal/localmodels/inventory.go`.

**Interfaces:**
- Consumes: the snapshot's `Providers` / `Down` / `Entries` shapes.
- Produces: start-path-only unknown signal for an answering-but-unprobed family; stop picker unchanged where required.

- [ ] **Step 1: Write the failing test** in `wt/internal/lifecycle` (`TestResidentOthers`) and the inventory family-set test pinning the probed set.
- [ ] **Step 2: Run it to verify it fails.**
- [ ] **Step 3: Implement the minimal fix** (design approach B: report not-probed families as unknown when they answer).
- [ ] **Step 4: Run the tests and verify they pass.**
- [ ] **Step 5: Commit.**

### Task 3: #403 — omlx answer-shape investigation follow-ups

**Files:**
- Modify: `llmbench` probe surface (sampling-penalty flags), docs.

**Interfaces:**
- Consumes: `llmbench probe thinking` targets and output.
- Produces: repeat-probe runs, sampling-penalty data, upstream mtplx report draft.

- [ ] **Step 1: Repeat the probe commands twice more** into separate `--out` directories; compare against the recorded 2026-10-21 results. (Investigation-first task: no code fix is in scope until the repeats and penalties are in.)
- [ ] **Step 2: Add sampling-penalty flags** to the probe if the issue's next-step list calls for them.
- [ ] **Step 3: Draft the upstream mtplx report** from the issue's facts, flagging unverified claims.
- [ ] **Step 4: Run the checks and record findings.**

### Task 4: #449 — cloud-sync variant/alias repricing and removal classification

**Files:**
- Modify: `wt/internal/cloudsync/openrouter.go` (`PlanPrices`, `suffixedListedModel`), `wt/internal/cloudsync/catalog.go`, `wt/cmd/wt/cloudsync.go`.

**Interfaces:**
- Consumes: the catalog / OpenRouter model list.
- Produces: decided repricing behavior for kept variant rows; alias handling; dropped-suffix handling; tests.

- [ ] **Step 1: Resolve the open owner decisions** for repricing-from-base vs leave-alone and alias detection (the design marks these the owner's call).
- [ ] **Step 2: Write the failing tests** (`PlanPrices`) incl. a dropped suffix.
- [ ] **Step 3: Implement the chosen behavior.**
- [ ] **Step 4: Run the tests and verify they pass.**
- [ ] **Step 5: Commit.**

### Task 5: #389 — picker stale-PR tag on reused branch names

**Files:**
- Modify: `wt/internal/worktree/prs.go` (PR map shape), `wt/internal/tui/*`.

**Interfaces:**
- Consumes: PR head branch / head commit identity.
- Produces: tag only on true match; stale tags dropped.

- [ ] **Step 1: Write the failing tests** for the stale-tag cases.
- [ ] **Step 2: Run it to verify it fails.**
- [ ] **Step 3: Implement the minimal fix.**
- [ ] **Step 4: Run the tests and verify they pass.**
- [ ] **Step 5: Commit.**

### Task 6: #447 — cloud-sync ollama/openrouter gate dedupe

**Files:**
- Modify: `wt/cmd/wt/cloudsync.go` (gate helpers).

**Interfaces:**
- Consumes: the shared removal-gate logic.
- Produces: one gate helper; ollama/openrouter exit wording kept distinct.

- [ ] **Step 1: Write the failing tests** for the shared gate.
- [ ] **Step 2: Run it to verify it fails.**
- [ ] **Step 3: Implement the minimal dedupe.**
- [ ] **Step 4: Run the tests and verify they pass.**
- [ ] **Step 5: Commit.**

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-10-23-open-issue-backlog-fix-first.md`. Please review the plan. Does it capture what you want?

**Which execution approach would you prefer?**

- **Subagent-driven** - A fresh subagent implements each task and a fresh reviewer checks it before the next one starts, then a whole-branch review at the end. Most thorough; costs a fresh context per task and per review.
- **Native** - I implement every task myself in this session, then one fresh reviewer checks the whole branch. Cheapest and fastest.

For this plan I recommend **subagent-driven**, because the tasks are independent safety fixes whose order matters less than their isolation, and each carries its own test cycle.

Does the plan capture what you want, and which approach should we use?