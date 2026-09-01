# wt Flow Cleanup — PR 5: Docs + Deprecation Polish

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Update user-facing docs to reflect the new flag surface, the three-input mental model, and the picker-ordering rules. Surface the `-w`→`-W` migration prominently. No code changes; no new tests.

**Architecture:** Plain Markdown updates. New `docs/wt-cli.md` walkthrough; refresh `docs/configuration.md`, per-agent docs, README, CHANGELOG.

**Tech Stack:** Markdown.

**Spec:** `docs/superpowers/specs/2026-08-18-wt-flow-cleanup-design.md` (section "Documentation (PR 5)").

## Global Constraints

- All Markdown links use repo-relative paths.
- Examples in docs use real flag values; run them mentally against the spec to confirm.
- PR 4 is already merged; this PR does not change code.

---

## File Structure (this PR)

### Created

- `docs/wt-cli.md` — the three-input mental model walkthrough.

### Modified

- `docs/configuration.md` — refresh flag table; add `-T`/`-F` documentation; add examples.
- `docs/wt-agents/claude.md`, `docs/wt-agents/codex.md`, etc. — note any agent-specific flag interactions (likely none).
- `README.md` — flag table refresh; migration callout.
- `CHANGELOG.md` — append a "Released" entry capturing the full PR 1-4 series.

### Untouched

- `docs/superpowers/specs/2026-08-18-wt-flow-cleanup-design.md` — the spec stays as-is per global rules (attached to PR comments, not committed).
- All Go code, all tests.

---

## Task 1: Create `docs/wt-cli.md`

**Files:**
- Create: `docs/wt-cli.md`.

- [ ] **Step 1: Write the walkthrough**

```markdown
# wt CLI Walkthrough

The `wt` command collects three pieces of information before launching an agent:

1. **Where to run?** A directory (worktree, branch, or current repo root).
2. **What runs?** An agent (claude, codex, ...) or a command (shell).
3. **Which model?** (agents only) A provider/model pair, optionally filtered by tag and family.

Each input has its own CLI flag(s) and its own picker screen. Pass flags to skip the corresponding picker.

## Flags at a glance

| Flag | Effect |
|---|---|
| `-W`, `--worktree <name>` | Use/create worktree for the named branch; skip directory picker |
| `--cwd` | Use the current repo root; skip directory picker |
| `-A`, `--agent <name>` | Pin the agent or command (claude, codex, copilot, pi, agy, opencode, shell) |
| `-M`, `--model <provider>/<name>` | Pin the model; verified against the eligible list |
| `-T`, `--tags <list>` | Filter models by tag (comma-delimited, OR) |
| `-F`, `--family <list>` | Filter models by family (comma-delimited, OR) |
| `--yolo` | Prepend skip-permissions flag |

## Examples

### Fully interactive

```bash
wt
```

Walks you through: pick a worktree → pick an agent or command → pick a model → launch.

### Skip the worktree picker

```bash
wt -W feature-x
```

Creates (or reuses) the `feature-x` worktree and proceeds to the agent picker.

### Skip worktree + agent pickers

```bash
wt -W feature-x -A claude
```

Launches claude in the `feature-x` worktree using the default claude model.

### Pin the model

```bash
wt -W feature-x -A claude -M claude/opus
```

Verifies `claude/opus` is in the eligible list for claude and launches it.

### Filter models by tag

```bash
wt -W feature-x -A claude -T code
```

If `code` and `design` tags both have models, only `code`-tagged models are eligible.

### Filter by family

```bash
wt -W feature-x -A pi -F gemma4
```

Limits pi's eligible models to the `gemma4` family.

### Combined filters

```bash
wt -W feature-x -A pi -T code,design -F gemma4
```

Eligible: models tagged `code` OR `design` AND in the `gemma4` family.

### Shell command

```bash
wt -W feature-x -A shell -- ls -la
```

Runs `ls -la` in the `feature-x` worktree. No model layer involved.

### Launch in current directory, outside a worktree picker

```bash
wt --cwd -A codex
```

Uses the current repo root as the launch directory.

## Migration from earlier versions

The `-w` short flag for `--worktree` has been removed. Use `-W` (capital) or `--worktree`. Existing invocations like `wt -w my-feature` now error with: `-w is removed; use -W or --worktree`.

## Resolution order

When multiple flags cover the same input, the more specific flag wins:

- Directory: `-W` > `--cwd` > picker.
- Agent: `-A` > picker.
- Model: `-M` (if in eligible list) > picker (with rotation within the eligible list).

If a flag is given and the corresponding screen would show 1+ options, the screen is skipped.

## Picker order

The worktree picker always renders:

1. `+ New worktree…` (sentinel)
2. Local branches and worktrees, alphabetical
3. A separator
4. Remote-only branches, alphabetical

The default branch is always present, even when checked out in a worktree.
```

- [ ] **Step 2: Commit**

```bash
git add docs/wt-cli.md
git commit -m "docs: add wt-cli walkthrough covering three-input model"
```

---

## Task 2: Refresh `docs/configuration.md`

**Files:**
- Modify: `docs/configuration.md`.

- [ ] **Step 1: Find the flag table**

Read the existing `docs/configuration.md` and locate the flag table.

- [ ] **Step 2: Replace with the new flag table**

Replace any existing flag table with the one from Task 1's "Flags at a glance" section.

- [ ] **Step 3: Add filter documentation**

Add a new subsection:

```markdown
## Filtering models by tag and family

Models carry a `tags` list (e.g. `["code", "design"]`) and a `family`
string (e.g. `"gemma4"`). At the CLI, the `-T`/`--tags` and `-F`/`--family`
flags narrow the eligible model set:

- `-T code,design` → model must have at least one matching tag.
- `-F gemma4,claude` → model.Family must equal one of the listed.
- Combined: tag set AND family set.

Both flags accept comma-delimited lists and use OR-within-flag semantics.
```

- [ ] **Step 4: Commit**

```bash
git add docs/configuration.md
git commit -m "docs(configuration): refresh flag table; add -T/-F docs"
```

---

## Task 3: Refresh `README.md`

**Files:**
- Modify: `README.md`.

- [ ] **Step 1: Find existing flag content**

```bash
grep -n "wt -w\|--worktree\|-A\|--agent" README.md
```

- [ ] **Step 2: Replace flag examples**

Replace any `wt -w` example with `wt -W`. Replace any `wt --agent` with `wt -A` where appropriate (preserve `wt --agent codex` if there's a reason; the new short form is preferred but the long form still works).

- [ ] **Step 3: Add migration callout**

Insert a "Migration" section near the top:

```markdown
> **Migration note (≥ unreleased):** The `-w` short flag for `--worktree`
> has been removed. Use `-W` or `--worktree`. New flags: `-A`/`--agent`,
> `-M`/`--model`, `-T`/`--tags`, `-F`/`--family`.
```

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs(README): refresh flag examples; add migration note"
```

---

## Task 4: Finalize `CHANGELOG.md`

**Files:**
- Modify: `CHANGELOG.md`.

- [ ] **Step 1: Move Unreleased entries to a dated release**

If a version is being cut, replace the `## Unreleased` header with the version + date, and add a new `## Unreleased` above for future work.

- [ ] **Step 2: Add a "Released" summary**

```markdown
## vNEXT (YYYY-MM-DD)

### Breaking changes

- `-w` short flag for `--worktree` removed (use `-W` or `--worktree`).

### Added

- `-A`/`--agent` short flag (alias for `--agent`).
- `-M`/`--model` flag to pin a model as `<provider>/<name>`.
- `-T`/`--tags` flag to filter models by tag.
- `-F`/`--family` flag to filter models by family.
- New `phaseAgent` picker screen between worktree and model pickers.
- Agent+command picker lists every configured agent and every registered
  command; commands launch directly without the model layer.
- Model rotation is now scoped to a `(agent, tag, family)` slot.
- State files: `rotation-<agent>-<tag>-<family>.state`. Legacy
  `rotation-<tag>.state` files remain readable on read; new writes go
  to the per-slot file.
- Worktree picker always presents at least the default branch row plus
  the `+ New worktree…` sentinel. Rows are ordered: sentinel → locals
  alphabetical → remotes alphabetical with a separator.

### Notes

- See `docs/superpowers/specs/2026-08-18-wt-flow-cleanup-design.md`
  for the design rationale (attached to the PR series).
```

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: finalize CHANGELOG for wt flow cleanup release"
```

---

## Self-Review

- [x] **Spec coverage:** PR 5 covers the entire "Documentation (PR 5)" section of the spec.
- [x] **Placeholder scan:** No TBDs.
- [x] **Type consistency:** N/A (Markdown only).
- [x] **Back-compat note:** Migration callout is prominent in README and CHANGELOG.
