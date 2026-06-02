# Worktree Scripts: Include Remote-Tracking Branches in Picker

**Date:** 2026-06-01  
**Status:** Approved

## Problem

The `*-wt` scripts' branch picker only shows local branches (`refs/heads/*`), missing remote-tracking branches (`refs/remotes/*/*`). Users cannot select remote branches to create worktrees from.

## Root Cause

In `wt-core.sh`, the `gather_entries()` function uses:

```bash
all_branches="$(git for-each-ref --format='%(refname:short)' refs/heads)"
```

This only fetches local branches, missing all remote-tracking branches.

## Design

### Scope

- Include all remote-tracking branches in the picker (Option 2 from brainstorming)
- Filter out `*/HEAD` symbolic refs (noise)
- Strip remote prefix when creating local branches (e.g., `origin/feature` → `feature`)
- Handle name collisions: if local branch `feature` exists, don't show `origin/feature` as a bare option

### Architecture

**Modified function:** `gather_entries()` in `wt-core.sh`

**Changes:**

1. **Fetch remote branches:** Add a second `git for-each-ref` call for `refs/remotes/`
2. **Filter `*/HEAD`:** Remove symbolic refs like `origin/HEAD`
3. **Strip remote prefix:** Convert `origin/feature` → `feature` for display and branch creation
4. **Collision detection:** If local branch `feature` exists, exclude `origin/feature` from bare options

**Data flow:**

```
git for-each-ref refs/heads      → local branches
git for-each-ref refs/remotes/   → remote branches
  ├─ Filter out */HEAD
  ├─ Strip remote prefix (origin/, upstream/, etc.)
  └─ Exclude if local branch with same name exists
       ↓
compute_bare_branches() compares against in-use branches
       ↓
fzf picker shows unified list
       ↓
User selects → handle_branch_selection() creates worktree
```

### Implementation Details

**Remote branch extraction:**

```bash
# Get remote branches, strip prefix, filter HEAD
git for-each-ref --format='%(refname:short)' refs/remotes/ \
  | grep -v '/HEAD$' \
  | sed 's@^[^/]*/@@'
```

**Collision handling:**

The existing `compute_bare_branches()` function already excludes branches that are "in use" (checked out in worktrees). We extend this to also exclude remote branches whose stripped name matches any local branch.

**Branch creation:**

When user selects a remote branch (now displayed as `feature`), `handle_branch_selection()` needs to:
1. Check if local branch exists
2. If not, create it tracking the remote: `git worktree add -b feature path origin/feature`
3. If yes, this shouldn't happen (collision excluded from list)

Actually, we need to track which branches are remote origins. Let me refine:

**Revised approach:**

Store both the display name and the full remote ref. When user selects:
- Local branch: use as-is
- Remote branch: create local tracking branch

This requires changing the data structure in `gather_entries()` to include the full ref for remote branches.

**Revised data structure:**

Current format: `TYPE<TAB>BRANCH<TAB>PATH`

For remote branches, we need to preserve the full remote ref. Options:
1. Add a 4th column: `TYPE<TAB>DISPLAY<TAB>PATH<TAB>FULL_REF`
2. Encode in BRANCH: `TYPE<TAB>origin/feature<TAB>PATH` (keep full ref, strip for display only)

Option 2 is simpler — keep the full `origin/feature` in the data, but strip it for the display column in `format_entries()`.

**Revised plan:**

1. `gather_entries()` fetches remote branches with full `origin/feature` names
2. `format_entries()` strips the remote prefix for display: `origin/feature` → `feature`
3. `handle_branch_selection()` receives `origin/feature`, extracts short name, creates tracking branch

### Error Handling

- Remote no longer exists: `git worktree add` fails → die with message
- Name collision slipped through: check before create, error if local exists

### Testing

Manual verification:
1. Run `claude-wt` (or other wrapper) in repo with remote branches
2. Verify picker shows local + remote branches
3. Verify `origin/HEAD` is filtered out
4. Select a remote branch, verify worktree creates with correct tracking

## Alternatives Considered

**Option 1: Show all remote branches including `*/HEAD`**
- Rejected: `origin/HEAD` is not a real branch, confuses users

**Option 3: Only show `origin/*` branches**
- Rejected: Users with multiple remotes (upstream, fork) lose visibility

**Keep remote prefix in display (e.g., `origin/feature`)**
- Rejected: Clutters display, user thinks they're on `origin/feature` branch when they're actually on `feature`

## Success Criteria

- [ ] Remote branches appear in `*-wt` picker
- [ ] `origin/HEAD` and similar filtered out
- [ ] Selecting `origin/feature` creates local `feature` branch tracking `origin/feature`
- [ ] Local branch `feature` excludes `origin/feature` from picker (no collision)
- [ ] All existing functionality preserved (local branches, worktrees)
