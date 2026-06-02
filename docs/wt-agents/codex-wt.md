# codex-wt

## Overview

`codex-wt` is the worktree launcher for [OpenAI Codex CLI](https://github.com/openai/codex). Codex CLI uses TOML for configuration and a profiles system to switch between provider/model combinations. See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
npm install -g @openai/codex
```

This installs the `codex` binary. The `codex-wt` launcher in this repo wraps `codex` with worktree-aware FZF selection and code/design model rotation. Install `codex-wt` itself by copying `bin/codex-wt` into `~/.local/bin/`.

## Configuration files & locations

Codex reads from `~/.codex/`. Briefly:

| File | Purpose |
|---|---|
| `~/.codex/config.toml` | Primary entry point. Global settings: `sandbox_mode`, `approval_policy`, default model, profiles. |
| `~/.codex/AGENTS.md` | Universal instructions applied to every Codex project. |
| `~/.codex/AGENTS.override.md` | Local-only override file with absolute precedence. |
| `~/.codex/auth.json` | API keys and organization identifiers. |
| `~/.codex/multi-auth/` | Multiple-account identity management. |

## Authentication & credentials

Codex stores API keys in `~/.codex/auth.json`. See the [Codex CLI authentication docs](https://github.com/openai/codex#authentication) for setup. Profiles in `config.toml` can reference different credentials, enabling provider switching.

## Model selection

Codex picks a model from a `--profile <name>` and `-m <model>` flag combination. `codex-wt` passes the rotation-selected model with `--profile ollama-launch -m <model>`. **A profile named `ollama-launch` must exist in `~/.codex/config.toml` for rotation to work**; without it, codex errors out. Define the profile to point at whichever provider/endpoint the rotation models live behind. See the [Codex profiles docs](https://github.com/openai/codex#profiles) for syntax.

The launcher's `native:codex` sentinel maps to codex's own default model for that profile.

## Verified on this machine

**Convention only; codex is used on other machines, not this one.** Statements above are sourced from the upstream Codex CLI repository and docs. Re-verify against the local machine before relying on details for credentials or profile syntax.
