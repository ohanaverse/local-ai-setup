# wt-core: Replace Hard Block on main with Contextual Warning

## Problem

When a `*-wt` launcher is used inside a git repo where `main` is the only available branch, selecting `[current] main` triggers a hard `die`:

```
pi-wt: Launching on the default branch (main) is blocked.
```

This creates a dead-end UX. The user cannot proceed without first manually creating a feature branch or worktree from another terminal.

## Solution

Replace the hard block with a contextual warning that checks whether the repo's pre-commit guard (installed by `wt-install-guard`) is present. The launcher then behaves differently based on guard status.

## Behavior

### Guard installed

Print a one-line notice to stderr and **launch immediately**:

```
pi-wt: main guard is installed — commits to main are blocked.
```

The guard already prevents accidental commits, so friction is unnecessary.

### Guard NOT installed

Print a warning, then prompt via fzf for explicit confirmation before launching:

```
pi-wt: WARNING: no main guard installed — commits to main are NOT blocked.

[ fzf prompt ]
Launch on main without commit protection?
> Proceed anyway
  Cancel
```

If the user selects **Proceed anyway**, the agent launches.
If the user selects **Cancel**, the script exits gracefully with code `0`.

### Non-interactive / piped stdin

When `stdin` is not a TTY, fzf cannot run. In this case, the script prints the warning to stderr and proceeds without blocking.

## Affected Code Paths

The change is isolated to the `type == "current"` branch-guard check in `wt_main()` inside `bin/wt-core.sh`.

```
# BEFORE (hard block)
if [[ "$type" == "current" ]]; then
  if [[ "$b" == "$(default_branch)" && -n "$b" ]]; then
    die "Launching on the default branch ($b) is blocked. ..."
  fi
fi

# AFTER (contextual warning)
if [[ "$type" == "current" ]]; then
  if [[ "$b" == "$(default_branch)" && -n "$b" ]]; then
    # guard_status returns 0 if installed, 1 if not
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

## Scope & Exclusions

- **Only file changed:** `bin/wt-core.sh`
- **No wrapper changes** required (`claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`, `agy-wt`)
- **No new files** created
- **Unchanged paths:**
  - `--worktree <name>` fast-path (explicit intent, no picker friction)
  - `--no-guard` flag
  - `--check-guard` flag
  - Bare-branch creation flow
  - Non-default branch selection
  - Session resume (`claude-wt` `wt_pre_exec` hook)

## Edge Cases

| Scenario | Behavior |
|---|---|
| Guard installed, on main | Notice printed, launch immediately |
| Guard missing, on main, interactive | Warning + fzf confirmation |
| Guard missing, on main, non-interactive | Warning printed, launch anyway |
| On non-default branch | Unchanged (no check at all) |
| No guard, but only option is `[current] main` | Warning + confirmation, not a hard error |

## Testing Notes

- Test in a repo with the guard installed: `claude-wt --check-guard` should return 0, selecting main should show notice and launch.
- Test in a repo without the guard: `--check-guard` returns 1, selecting main should show warning + confirmation prompt.
- Test piping: `echo "" | pi-wt` (non-TTY stdin) should not hang on fzf.

## Dependencies

- `guard_status()` — already defined in `wt-core.sh`, returns 0/1
- `default_branch()` — already defined in `wt-core.sh`
- `fzf` — already a dependency for the picker flow
