# codex-wt

## Overview

`codex-wt` is the worktree launcher for [OpenAI Codex CLI](https://github.com/openai/codex). Codex CLI uses TOML for configuration and a profiles system to switch between provider/model combinations. See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
npm install -g @openai/codex
```

This installs the `codex` binary. The `codex-wt` file in this repo is a shim that forwards to `wt --agent codex`. Install `codex-wt` itself by copying `bin/codex-wt` into `~/.local/bin/`.

## Configuration files & locations

Codex reads from `~/.codex/`. Briefly:

| File | Purpose |
|---|---|
| `~/.codex/config.toml` | Primary entry point. Global settings: `sandbox_mode`, `approval_policy`, default model, profiles. |
| `~/.codex/AGENTS.md` | Universal instructions applied to every Codex project. |
| `~/.codex/AGENTS.override.md` | Local-only override file with absolute precedence. |
| `~/.codex/auth.json` | API keys and organization identifiers. |
| `~/.codex/multi-auth/` | Multiple-account identity management. |
| `~/.codex/<name>.config.toml` | Per-profile TOML layered on top of `config.toml` (codex >= 0.134.0). Selected with `--profile <name>`. |

## Authentication & credentials

Codex stores API keys in `~/.codex/auth.json`. See the [Codex CLI authentication docs](https://github.com/openai/codex#authentication) for setup. Profiles in `config.toml` can reference different credentials, enabling provider switching.

When using `codex-wt` with a rotation-selected model, codex talks to a local Ollama endpoint instead of `api.openai.com` — see [Model selection](#model-selection) below. No `auth.json` is required in that flow.

## Model selection

Codex picks a model from a `--profile <name>` and `-m <model>` flag combination. `codex-wt` passes the rotation-selected model with `--profile ollama-launch -m <model>`. The launcher's `native:codex` sentinel maps to codex's own default model for that profile (no `--profile` flag is passed in that case).

### Ollama launch profile

When rotation selects a non-`native:codex` model, `codex-wt` ensures `~/.codex/ollama-launch.config.toml` exists with a known-good shape:

```toml
model = "<rotation-picked model>"
model_provider = "ollama-launch"

[model_providers.ollama-launch]
name = "Ollama"
base_url = "<PROVIDER_OLLAMA_BASE_URL from models.conf, default http://localhost:11434>/v1"
wire_api = "responses"
```

The launcher regenerates this file on each launch — but only when its contents differ from the desired shape, so the on-disk file is touched once per model change. Both top-level `model` and `model_provider` settings are required: if either is missing, codex falls back to the default `openai` provider, which then shows a "Sign in with ChatGPT" prompt when no `auth.json` is present.

Ollama base URL is read from `PROVIDER_OLLAMA_BASE_URL` in `~/.config/agent-wt/models.conf`, with a default of `http://localhost:11434`. Trailing `/v1` and trailing slashes are normalized so the profile always has a single canonical `/v1` suffix.

The launcher's `native:codex` sentinel skips profile generation entirely and invokes `codex` directly.

## Agent init

`codex-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)

Codex reads `AGENTS.md` natively, so no pointer file is needed.

## Verified on this machine

The ollama-launch profile generation, the ollama base-URL fallback, and the `--profile ollama-launch` model resolution were all verified live against the local ollama instance on 2026-06-03. Statements about codex CLI behavior are sourced from the [Codex CLI docs](https://developers.openai.com/codex/cli/) and the [Ollama Codex integration guide](https://docs.ollama.com/integrations/codex). Re-verify after codex CLI upgrades.
