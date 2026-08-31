#!/usr/bin/env bash
# block-main-commit v1
# Installed by wt. Blocks commits to main/master unless explicitly bypassed.
set -euo pipefail
branch="$(git symbolic-ref --short HEAD 2>/dev/null || true)"
case "$branch" in
    main|master)
        if [ "${WT_SKIP_MAIN_BLOCK:-0}" = "1" ]; then
            exit 0
        fi
        echo "block-main-commit: commits to '$branch' are blocked."
        echo "Bypass with: git commit --no-verify (or WT_SKIP_MAIN_BLOCK=1)"
        exit 1
        ;;
esac
exit 0
