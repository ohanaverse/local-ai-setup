# claude-wt

## Overview

`claude-wt` is the worktree launcher for [Claude Code](https://claude.ai/code), Anthropic's official CLI. Claude Code reads global identity, skills, and session state from `~/.claude/` and tracks OAuth state in `~/.claude.json`. See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
curl -fsSL https://claude.ai/install.sh | bash
```

This installs the `claude` binary. The `claude-wt` file in this repo is a shim that forwards to `wt --agent claude`. Install `claude-wt` itself by copying `bin/claude-wt` (and the rest of `bin/`) into `~/.local/bin/`.

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

Claude Code picks a model from `--model <name>` or, absent that, from `~/.claude/settings.json`'s default. `claude-wt` passes the rotation-selected model directly through with `--model`. The model name format is whatever Claude Code accepts (e.g., `claude-opus-4-7` for native models, or the bare Ollama name like `minimax-m3:cloud` for Ollama-routed models).

When routing through the Ollama Anthropic-compatible gateway, the launcher passes the **bare** provider-specific name (`config.Model.ModelName`), not the registry key (`config.Model.ID`, which is `provider/model` form). Using the registry key here would forward `ollama/<model>` to the Ollama gateway, which would not recognize the prefixed id.

## Session resume

`wt` detects a previous Claude Code session (via `internal/session`) and, in the TUI, prompts to **Resume** or **Start fresh**; the non-TUI launch path appends `--resume <id>` automatically. Sessions are stored under `~/.claude/projects/<slug>/*.jsonl`, where `<slug>` is the worktree path with non-alphanumeric chars replaced by `-`.

### Cloud models via Ollama

When a cloud model (e.g., `minimax-m3:cloud`) is selected from rotation and is available in Ollama, `claude-wt` sets Anthropic-compatible environment variables:

```bash
ANTHROPIC_AUTH_TOKEN=ollama
ANTHROPIC_API_KEY=""
ANTHROPIC_BASE_URL="http://localhost:11434/v1"
```

The Ollama base URL is read from `PROVIDER_OLLAMA_BASE_URL` in `~/.config/agent-wt/models.conf`, falling back to `http://localhost:11434`. This allows Claude Code to use Ollama-hosted models that follow the `:cloud` naming convention.

## Agent init

`claude-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)
- `CLAUDE.md` — pointer containing `@AGENTS.md` (if missing)

Claude natively supports `@path` imports in `CLAUDE.md`. When Claude reads `CLAUDE.md`, it automatically expands the `@AGENTS.md` import. Users can add Claude-specific instructions below the import.

## Verified on this machine

**Verified on this machine, 2026-06-01.**

- **Binary:** `~/.local/bin/claude`.
- **Home directory:** `~/.claude/` exists with `CLAUDE.md`, `settings.json`, `hooks/`, `skills/`, `plugins/`, `sessions/`, and additional subdirectories (`backups/`, `bin/`, `cache/`, `debug/`, `docs/`, `downloads/`, `file-history/`, `history.jsonl`, `ide/`, `memory/`, `plans/`, `projects/`, `security/`, `session-env/`).
- **OAuth state:** `~/.claude.json` exists. Internals are an OAuth-token cache; treat as opaque.
- **No credential wrapper.** `claude-wt`'s `exec claude …` works directly against the installed `claude` binary; no equivalent to pi's `activate-litellm.sh` is needed in this deployment.
