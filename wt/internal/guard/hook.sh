#!/usr/bin/env bash
# block-main-commit v2
# Installed by wt. Blocks commits to main/master anywhere, and commits on any
# named branch in the primary checkout (work belongs in a linked worktree).
# A detached HEAD is allowed. Bypass: git commit --no-verify, or
# WT_SKIP_MAIN_BLOCK=1.
set -euo pipefail
if [ "${WT_SKIP_MAIN_BLOCK:-0}" = "1" ]; then
    exit 0
fi
branch="$(git symbolic-ref --short HEAD 2>/dev/null || true)"
case "$branch" in
    main|master)
        echo "block-main-commit: commits to '$branch' are blocked."
        echo "Bypass with: git commit --no-verify (or WT_SKIP_MAIN_BLOCK=1)"
        exit 1
        ;;
esac
if [ -n "$branch" ]; then
    gitdir="$(git rev-parse --absolute-git-dir 2>/dev/null || true)"
    common="$(git rev-parse --git-common-dir 2>/dev/null || true)"
    if [ -n "$gitdir" ] && [ -n "$common" ]; then
        gitdir="$(cd "$gitdir" 2>/dev/null && pwd -P || true)"
        common="$(cd "$common" 2>/dev/null && pwd -P || true)"
        if [ -n "$gitdir" ] && [ "$gitdir" = "$common" ]; then
            echo "block-main-commit: this is the primary checkout; commits on branch '$branch' belong in a worktree."
            echo "Work in a worktree: wt -W <name>. If your worktree is gone: wt restore-root."
            echo "Bypass with: git commit --no-verify (or WT_SKIP_MAIN_BLOCK=1)"
            exit 1
        fi
    fi
fi
exit 0
