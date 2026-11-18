# wt-prefixed skills Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every skill in this repo a `wt-` name, and bring `repo-cleanup` in as `wt-repo-cleanup` with Makefile install targets for Claude Code, Pi and Codex.

**Architecture:** Skills stay where they are (`.claude/skills/` and `wt/.claude/skills/`); each directory and its `name:` field are renamed with `git mv`. `wt-repo-cleanup` is copied from the other repo (read-only source) and is the only skill with global install targets. A shell lint in the root Makefile enforces the naming rule.

**Tech Stack:** Make, POSIX shell, Markdown.

**Spec:** `docs/superpowers/specs/2026-11-14-wt-prefixed-skills-design.md`

## Global Constraints

- Directory name == frontmatter `name:`, lowercase, starts with `wt-` (Agent Skills format; works on Claude Code, Pi, Codex).
- Do not modify `/Users/user_name/github/ohanaverse/repo-cleanup-skill`. Source commit for the copy: `3ae75af`.
- Do not rename the `wt cloud-sync` command or any other CLI text; only references to the *skill* change.
- A global gitignore ignores `.claude/`: new files there need `git add -f`. `git mv` of tracked files is unaffected.
- `wt/CHANGELOG.md` is history; leave its old skill names.
- Run from the worktree `/Users/user_name/github/ohanaverse/local-ai-setup/.worktrees/move-repo-cleanup`.

## Review Focus

- A skill directory renamed but `name:` left old (or the reverse): the lint must catch both.
- A skill reference missed in prose (`the \`cloud-sync\` skill`): the grep in Task 2 Step 5 must be empty.
- `sed` over-reach turning the `wt cloud-sync` command into `wt wt-cloud-sync`: Task 2 Step 5 greps for that.
- Install run twice, or with the old global `repo-cleanup` present: must end with exactly one `wt-repo-cleanup` and no `repo-cleanup`.
- `make install-skill-*` must not touch the real home in tests: tests pass `HOME=<scratch>`.

---

### Task 1: Naming lint (fails first)

**Files:**
- Modify: `Makefile` (add `lint-skills`, make `lint` depend on it)

**Interfaces:**
- Produces: `make lint-skills` — exit 1 and print `FAIL: <path>: ...` for any `SKILL.md` under `.claude/skills/*/` or `wt/.claude/skills/*/` whose `name:` differs from its directory name or lacks the `wt-` prefix.

- [ ] **Step 1: Add the target.** Add `lint-skills` to `.PHONY`; `lint: lint-shell lint-skills check-links`. The recipe loops over `.claude/skills/*/SKILL.md wt/.claude/skills/*/SKILL.md`, reads the first `name:` line in the frontmatter (`sed -n 's/^name: *//p' | head -1`), compares with `basename $(dirname f)`, and requires the `wt-` prefix. Print `  OK: <n> skills named wt-*` on success.
- [ ] **Step 2: Run it and see it fail.** `make lint-skills` → exit 2 (make), output has 7 `FAIL:` lines (4 root + 3 under `wt/`), each saying the name lacks `wt-`.
- [ ] **Step 3: Commit.**

```bash
git add Makefile
git commit -m "chore: lint that every skill is named wt-<dir>"
```

### Task 2: Rename the seven existing skills and their references

**Files:**
- Rename (`git mv`): `.claude/skills/{manage-slot-profiles,adding-a-benchmark-backend,mlx-lm-quantization,sentinel-failure}` and `wt/.claude/skills/{adding-a-provider,adding-a-wt-agent,cloud-sync}` to `wt-<same>`
- Modify: each renamed `SKILL.md` (`name:` line), plus the reference sites below

**Interfaces:**
- Consumes: `make lint-skills` from Task 1.
- Produces: skill names `wt-manage-slot-profiles`, `wt-adding-a-benchmark-backend`, `wt-mlx-lm-quantization`, `wt-sentinel-failure`, `wt-adding-a-provider`, `wt-adding-a-wt-agent`, `wt-cloud-sync`.

- [ ] **Step 1: `git mv` the seven directories** and edit each `SKILL.md` frontmatter `name:` to match.
- [ ] **Step 2: Fix references to the skill (prose and paths), by hand, in these files:** `CLAUDE.md` (line 6 path and name; line 23 `the \`cloud-sync\` skill`; line 79), `wt/CLAUDE.md` (lines 131, 218), `llmbench/CLAUDE.md:67`, `modeltiers/CLAUDE.md:33`, `benchmarks/README.md:66`, `benchmarks/qwen3.8-benchmark.md:312`, `docs/guides/08-maintenance-and-troubleshooting.md:610`, `docs/guides/12-model-tiers.md:524`, `wt/docs/wt-cloud-sync.md:15-16`, `wt/docs/internals/cloud-sync.md` (lines 3, 29), `wt/docs/wt-agents/slot-profiles.md:515`, and the cross-references inside the skills (`wt-manage-slot-profiles` -> `wt-sentinel-failure`; `wt-adding-a-benchmark-backend` -> `wt-adding-a-provider`; `wt-adding-a-provider` -> `wt-adding-a-benchmark-backend`).
- [ ] **Step 3: Fix the two code strings that name a skill.** `modeltiers/src/modeltiers/cli.py:249` and `modeltiers/tests/test_cli.py:714` (`or the sentinel-failure skill` -> `or the wt-sentinel-failure skill`). Go comments in `wt/cmd/wt/cloudsync*_test.go` and `wt/internal/cloudsync/catalog_test.go` that say "cloud-sync skill" are updated to "wt-cloud-sync skill" (comments only).
- [ ] **Step 4: Verify lint and tests.** `make lint-skills` → `OK: 7 skills`; `cd modeltiers && uv run pytest tests/test_cli.py -q` → pass; `cd wt && go vet ./cmd/wt ./internal/cloudsync` → clean.
- [ ] **Step 5: Verify no stale skill reference remains.**

```bash
grep -rnE '(`|/)(manage-slot-profiles|adding-a-benchmark-backend|mlx-lm-quantization|sentinel-failure|adding-a-provider|adding-a-wt-agent)(`|/| skill)' \
  --exclude-dir=.git --exclude-dir=.venv . | grep -v 'docs/superpowers\|\.remember\|CHANGELOG'
grep -rn 'cloud-sync` skill\|skills/cloud-sync' --exclude-dir=.git . | grep -v 'docs/superpowers\|CHANGELOG'
grep -rn 'wt wt-' --exclude-dir=.git . | grep -v 'docs/superpowers'
```

Expected: all three print nothing (the `10-mlx-lm-quantization.md` guide filename is not a skill reference and does not match).
- [ ] **Step 6: Commit** `git add -A wt .claude CLAUDE.md llmbench modeltiers benchmarks docs && git commit -m "refactor: prefix every skill name with wt-"`.

### Task 3: Bring in `wt-repo-cleanup` and the install targets

**Files:**
- Create: `.claude/skills/wt-repo-cleanup/SKILL.md` (use `git add -f`)
- Modify: `Makefile`, `docs/reference/worktrunk-comparison.md:225`

**Interfaces:**
- Consumes: `make lint-skills` (now also covers the new skill).
- Produces: `make install-skill-claude`, `install-skill-pi`, `install-skill-codex`, `uninstall-skill-claude|pi|codex`; destinations `$(HOME)/.claude/skills/wt-repo-cleanup/`, `$(HOME)/.agents/skills/wt-repo-cleanup/`, `$(HOME)/.codex/skills/wt-repo-cleanup/`. Each install first removes the same host's old `repo-cleanup` and `wt-repo-cleanup` directories.

- [ ] **Step 1: Copy.** `cp /Users/user_name/github/ohanaverse/repo-cleanup-skill/skill/repo-cleanup/SKILL.md .claude/skills/wt-repo-cleanup/SKILL.md` (`mkdir -p` first). Edit frontmatter `name: wt-repo-cleanup`; change the `# /repo-cleanup` heading to `# /wt-repo-cleanup`; replace every other self-reference to `/repo-cleanup`; change "install the current `wt` (`make install` in the local-ai-setup repo)" to "(`make install` in the repo that holds this skill)".
- [ ] **Step 2: Lint.** `make lint-skills` → `OK: 8 skills`.
- [ ] **Step 3: Write the install targets** with the old repo's Makefile as the model (host paths as above). Add them to `.PHONY`. Do not add them to `make install`.
- [ ] **Step 4: Test in a scratch home.**

```bash
H=$(mktemp -d); mkdir -p $H/.claude/skills/repo-cleanup && touch $H/.claude/skills/repo-cleanup/SKILL.md
make install-skill-claude install-skill-pi install-skill-codex HOME=$H
make install-skill-claude HOME=$H            # second run is idempotent
ls $H/.claude/skills $H/.agents/skills $H/.codex/skills
make uninstall-skill-claude uninstall-skill-pi uninstall-skill-codex HOME=$H
ls $H/.claude/skills $H/.agents/skills $H/.codex/skills
```

Expected: after install each of the three lists exactly `wt-repo-cleanup` (no `repo-cleanup`); after uninstall each is empty.
- [ ] **Step 5: Update `docs/reference/worktrunk-comparison.md:225`** to `wt-repo-cleanup` skill (`.claude/skills/wt-repo-cleanup/SKILL.md`).
- [ ] **Step 6: Commit.**

```bash
git add -f .claude/skills/wt-repo-cleanup/SKILL.md && git add Makefile docs
git commit -m "feat: bring repo-cleanup in as the wt-repo-cleanup skill with install targets"
```

### Task 4: Docs and final gate

**Files:**
- Modify: `CLAUDE.md` (Docs section and Commands), `docs/guides/08-maintenance-and-troubleshooting.md` §9 (cleanup), `docs/superpowers/specs/2026-11-14-wt-prefixed-skills-design.md` (commit it)

- [ ] **Step 1: CLAUDE.md.** In the Docs list, add one bullet: skills are named `wt-*`, `wt-repo-cleanup` (`.claude/skills/wt-repo-cleanup/`) drives `wt worktrees`/`remove`/`rescue`/`restore-root`, and `make install-skill-claude|pi|codex` installs it globally. Add the three install targets to the Commands list (one line, name the guide).
- [ ] **Step 2: Guide 08 §9.** Add a short subsection: what `/wt-repo-cleanup` does (preview, ask per item, apply through wt, root move offered by `wt restore-root`), its requirements (`wt worktrees --help` prints the list's help; `gh` optional, origin branches cannot be proven merged without it), the install commands, and that it never switches the root on its own. State no live counts (guides are state-independent).
- [ ] **Step 3: Gate.** `make lint` → passes (shell, skills, links; `make check-links` covers the new text). `git status` clean except for the committed work.
- [ ] **Step 4: Commit** `git add -f` nothing new under `.claude/`; `git add CLAUDE.md docs && git commit -m "docs: wt-* skills and the wt-repo-cleanup install targets"`.
- [ ] **Step 5: Hand off to the user (not run by the plan):** the real `make install-skill-claude` replaces the global `~/.claude/skills/repo-cleanup`; the user runs it after merge.
