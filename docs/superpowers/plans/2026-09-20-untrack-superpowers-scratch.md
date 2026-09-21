# Untrack `.superpowers/` scratch Implementation Plan (Batch A, issue #130)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the one tracked file under `.superpowers/` and make the repository itself (not each clone's `.git/info/exclude`) record that `.superpowers/` is git-ignored scratch.

**Architecture:** Two small git changes at the repo root: `git rm --cached` the report, and add `.superpowers/` to the tracked `.gitignore`. No code. The report's content stays recoverable from history (`f805af9`).

**Tech Stack:** git.

**Spec:** GitHub issue #130.

## Global Constraints

- Run from the repo root `/Users/keith/github/ohanaverse/local-ai-setup`.
- Do not delete the working-tree copy of anything under `.superpowers/` — the directory is live scratch for in-flight subagent-driven work (`.superpowers/sdd/2026-09-19-wt-stats-any-pair/` exists right now). Untrack only.
- Do not push or open a PR without asking the user.
- The global `~/.gitignore` interacts with repo ignore rules (git-rules.md); `.superpowers/` is not covered by it today (only `.claude/` is), so a plain edit to `.gitignore` works.
- Commit trailer: `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`; commits reference the plan item (`- completes plan item #N`).
- **Unrelated dirty state:** the working tree already shows ` D docs/superpowers/plans/2026-09-20-wt-post-exit.md`. Do not stage or commit it as part of this work; ask the user what they want done with it.

## Facts established while planning

- `git ls-files .superpowers/` → only `.superpowers/sdd/port-the-bash-isolation-joyful-eagle/final-fix-report.md` (396 lines, a dated plan-progress report from the bash→Python lifecycle port), added by `f805af9` (#90, 2026-09-15).
- `.superpowers/` is currently ignored only via this clone's `.git/info/exclude` (line 18–19, listed twice) and an **untracked** `.superpowers/sdd/.gitignore` containing `*` — neither travels with the repo. The repo `.gitignore` says nothing about it.
- `docs/superpowers/{plans,specs}/` is where dated, tracked records belong (excluded from `check-links`).

## Decision to confirm with the user before Task 2

The issue asks whether the report belongs in the repo at all. Plan default: **untrack it** (it is scratch by construction; the content is one `git show f805af9:<path>` away). Alternative: `git mv` it to `docs/superpowers/reports/2026-09-15-lifecycle-port-final-fix.md` if the user wants it kept as a permanent record. Ask, and follow the answer; the tasks below are written for the default.

---

### Task 1: Verify provenance before acting

**Files:** none modified

- [ ] **Step 1: Re-check the facts**

```bash
git ls-files .superpowers/
git log --format='%h %ci %s' -- .superpowers/ | head
git check-ignore -v .superpowers/sdd/any-file.md
git show f805af9:.superpowers/sdd/port-the-bash-isolation-joyful-eagle/final-fix-report.md | head -20
grep -n superpowers .git/info/exclude
```

Expected: one tracked path; introduced by `f805af9`; `check-ignore` names `.git/info/exclude` (or the untracked `.gitignore`), not a tracked file; the show output is the "Final fix wave" report. If anything differs from "Facts established" above, stop and tell the user.

- [ ] **Step 2: Check nothing references the file**

```bash
git grep -n "final-fix-report\|port-the-bash-isolation" -- ':!.superpowers'
```

Expected: no hits outside the file (docs excluded from `check-links` may cite it; if so, list them and ask the user whether to update the citations).

- [ ] **Step 3: Ask the user the keep-or-untrack question** (see "Decision to confirm").

---

### Task 2: Ignore `.superpowers/` in the repo and untrack the report

**Files:**
- Modify: `.gitignore`
- Untrack: `.superpowers/sdd/port-the-bash-isolation-joyful-eagle/final-fix-report.md`

- [ ] **Step 1: Add the ignore rule to the tracked `.gitignore`**

Append (with a blank line before it):

```gitignore

# Subagent-driven-development scratch (ledgers, briefs, reports, review
# packages for in-flight plans). Never tracked: dated records that belong in the
# permanent history go under docs/superpowers/. Recorded here so the rule
# travels with the repo instead of living in each clone's .git/info/exclude.
.superpowers/
```

- [ ] **Step 2: Untrack the report, keeping the working copy**

```bash
git rm --cached .superpowers/sdd/port-the-bash-isolation-joyful-eagle/final-fix-report.md
```

Expected: `rm '.superpowers/sdd/port-the-bash-isolation-joyful-eagle/final-fix-report.md'`; the file still exists on disk.

- [ ] **Step 3: Verify**

```bash
git ls-files .superpowers/                       # expect: no output
test -f .superpowers/sdd/port-the-bash-isolation-joyful-eagle/final-fix-report.md && echo "working copy kept"
git status --short                               # expect: M .gitignore, D .superpowers/... (staged), plus the pre-existing unrelated D
git check-ignore -v .superpowers/sdd/new-file.md # expect: .gitignore:<line>:.superpowers/  (the tracked rule, not info/exclude)
```

To prove the rule works without the local exclude, run once:
`git -c core.excludesFile=/dev/null check-ignore -v --no-index .superpowers/sdd/new-file.md` and confirm it cites `.gitignore`.

- [ ] **Step 4: Run the repo lint**

Run: `make lint`
Expected: PASS (shell lint + link check; nothing here touches either).

- [ ] **Step 5: Commit only the two intended paths**

```bash
git add .gitignore
git commit -m "chore: untrack .superpowers scratch report and ignore the directory repo-wide (#130) - completes plan item #2" -- .gitignore .superpowers/sdd/port-the-bash-isolation-joyful-eagle/final-fix-report.md
```

(Do **not** pass the paths after `--` here: `git commit -- <paths>` commits the working-tree state of those paths, which re-adds the just-untracked report and leaves it tracked. Stage `.gitignore` and the `git rm --cached` removal, and make sure nothing else is staged, then commit plainly. Confirm with `git show --stat HEAD`: exactly two paths, one of them a deletion, and `git ls-files .superpowers/` prints nothing. Found during execution.)

---

### Task 3: Hand-off

- [ ] **Step 1:** Report to the user: what was untracked, that the report is recoverable via `git show f805af9:<path>`, and that clones which already have the file tracked will see it removed on pull (their local copy is deleted by git when they pull; anyone relying on it should recover it from `f805af9` first).
- [ ] **Step 2:** Ask before pushing or opening a PR ("Ready to create a PR?"). The suggested PR closes #130.
- [ ] **Step 3:** Remind the user about the pre-existing ` D docs/superpowers/plans/2026-09-20-wt-post-exit.md` in the working tree, which is outside this plan.

## Self-review

- Covers both of the issue's problems: the wrong home (Task 2 untracks; the keep-alternative is named) and the provenance/robustness gap (tracked `.gitignore` rule, verified against the local exclude).
- Nothing destructive: only `--cached`; working copies of live scratch are untouched.
