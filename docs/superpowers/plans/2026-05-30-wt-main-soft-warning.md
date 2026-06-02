# wt-core: Replace Hard Block on main with Contextual Warning — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hard `die` when launching on `main` with a contextual warning that checks pre-commit guard status and either notifies or confirms before proceeding.

**Architecture:** A single localized change in `bin/wt-core.sh` inside `wt_main()`. The existing `guard_status` function (returns 0/1) and `default_branch` function are reused. If guard is installed → print notice and launch. If guard is missing and TTY → fzf confirmation. If guard is missing and non-TTY → print warning and launch.

**Tech Stack:** bash, fzf, git

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `bin/wt-core.sh` | Modify | Replace hard block with guard-aware warning/confirmation logic |

---

### Task 1: Replace hard block with contextual warning in `bin/wt-core.sh`

**Files:**
- Modify: `bin/wt-core.sh` — lines around the "Final safety net" block in `wt_main()`

- [ ] **Step 1: Edit the safety-net block**

Locate this block in `wt_main()` (near the end, just before the `case "$type" in`):

```bash
  # Final safety net: if somehow current directory on default branch was selected, block.
  if [[ "$type" == "current" ]]; then
    local b
    b="$(git -C "$path" branch --show-current 2>/dev/null)" || die "failed to detect branch in $path — worktree may be corrupted"
    if [[ "$b" == "$(default_branch)" && -n "$b" ]]; then
      die "Launching on the default branch ($b) is blocked. Pick a different branch or create a worktree with: $WT_NAME --worktree <name>"
    fi
  fi
```

Replace it with:

```bash
  # Contextual warning: if on default branch, check guard status and warn/confirm.
  if [[ "$type" == "current" ]]; then
    local b
    b="$(git -C "$path" branch --show-current 2>/dev/null)" || die "failed to detect branch in $path — worktree may be corrupted"
    if [[ "$b" == "$(default_branch)" && -n "$b" ]]; then
      if guard_status; then
        printf '%s: main guard is installed — commits to %s are blocked.\n' "$WT_NAME" "$b" >&2
      elif [[ -t 0 ]]; then
        printf '%s: WARNING: no main guard installed — commits to %s are NOT blocked.\n' "$WT_NAME" "$b" >&2
        local choice
        choice="$(printf 'Proceed anyway\nCancel\n' | fzf --no-multi --prompt='wt> ' --header="Launch on $b without commit protection?")" || exit 0
        [[ "$choice" != "Proceed anyway" ]] && exit 0
      else
        printf '%s: WARNING: no main guard installed — commits to %s are NOT blocked.\n' "$WT_NAME" "$b" >&2
      fi
    fi
  fi
```

- [ ] **Step 2: Validate bash syntax**

Run:
```bash
bash -n bin/wt-core.sh
```

Expected: no output (success exit code 0).

- [ ] **Step 3: Commit**

```bash
git add bin/wt-core.sh
git commit -m "fix(wt-core): replace hard main block with guard-aware warning/confirm

When the user selects [current] main in the worktree picker:
- If the main guard is installed, print a notice and launch.
- If the guard is missing and stdin is a TTY, prompt via fzf to proceed or cancel.
- If the guard is missing and stdin is not a TTY, print a warning and launch.

This removes the dead-end UX where selecting main was a hard error."
```

---

### Task 2: Verify structure integrity

**Files:**
- None (validation only)

- [ ] **Step 1: Run marketplace validation**

Run:
```bash
python3 scripts/validate_marketplace.py
```

Expected: passes with no errors.

- [ ] **Step 2: Commit**

```bash
git commit --allow-empty -m "chore: validate marketplace structure after wt-core change"
```

---

## Self-Review Checklist

1. **Spec coverage:**
   - Guard installed → notice + immediate launch ✅ (Task 1, Step 1)
   - Guard missing + TTY → warning + fzf confirmation ✅ (Task 1, Step 1)
   - Guard missing + non-TTY → warning + launch anyway ✅ (Task 1, Step 1)
   - Only file changed: `bin/wt-core.sh` ✅ (File Map)
   - `--worktree`, `--no-guard`, `--check-guard` unchanged ✅ (no edits near those paths)

2. **Placeholder scan:** No TBDs, TODOs, or vague instructions found.

3. **Type consistency:** Uses existing `guard_status()` (returns 0/1) and `default_branch()`; no new functions or signatures introduced.
