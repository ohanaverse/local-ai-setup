# claude-wt

## Overview

`claude-wt` is the worktree launcher for [Claude Code](https://claude.ai/code), Anthropic's official CLI. Claude Code reads global identity, skills, and session state from `~/.claude/` and tracks OAuth state in `~/.claude.json`. See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
curl -fsSL https://claude.ai/install.sh | bash
```

This installs the `claude` binary. The `claude-wt` launcher in this repo wraps `claude` with worktree-aware FZF selection and code/design model rotation. Install `claude-wt` itself by copying `bin/claude-wt` (and the rest of `bin/`) into `~/.local/bin/`.

## Configuration files & locations

Claude Code reads from `~/.claude/`. Briefly:

| File / directory | Purpose |
|---|---|
| `~/.claude/CLAUDE.md` | Global persistent instructions for every session. |
| `~/.claude/settings.json` | Permissions, theme, default model. |
| `~/.claude/hooks/` | Pre/post-tool hooks for automation and safety. |
| `~/.claude/skills/` | Reusable workflows (each becomes a slash command). |
| `~/.claude/plugins/` | Installed Claude Code plugins. |
| `~/.claude/sessions/` | Session history. |
| `~/.claude.json` | OAuth sessions, MCP server config, internal caches. |

## Authentication & credentials

Claude Code uses OAuth, with state stored in `~/.claude.json`. The internals of that file are observable but should be treated as opaque — the supported way to (re-)authenticate is `claude /login`, not editing `~/.claude.json` by hand. There is no environment-variable credential pattern equivalent to pi's `$ANTHROPIC_API_KEY`-via-wrapper setup; `claude-wt` does not need a credential wrapper.

## Model selection

Claude Code picks a model from `--model <name>` or, absent that, from `~/.claude/settings.json`'s default. `claude-wt` passes the rotation-selected model directly through with `--model`. The model name format is whatever Claude Code accepts (e.g., `claude-opus-4-7`).

## Verified on this machine

**Verified on this machine, 2026-06-01.**

- **Binary:** `~/.local/bin/claude`.
- **Home directory:** `~/.claude/` exists with `CLAUDE.md`, `settings.json`, `hooks/`, `skills/`, `plugins/`, `sessions/`, and additional subdirectories (`backups/`, `bin/`, `cache/`, `debug/`, `docs/`, `downloads/`, `file-history/`, `history.jsonl`, `ide/`, `memory/`, `plans/`, `projects/`, `security/`, `session-env/`).
- **OAuth state:** `~/.claude.json` exists. Internals are an OAuth-token cache; treat as opaque.
- **No credential wrapper.** `claude-wt`'s `exec claude …` works directly against the installed `claude` binary; no equivalent to pi's `activate-litellm.sh` is needed in this deployment.
