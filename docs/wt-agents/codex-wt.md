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

Codex picks a model from `--model <name>` (or a `--profile <name>` + `-m <model>` combination). `codex-wt` handles two cases:

- **`codex/native`** — passes no `--model` flag, so codex uses its own default model.
- **Ollama-routed models** — passes `--model <name>`, where `<name>` is the **bare** provider-specific name (`config.Model.ModelName`). The registry key `config.Model.ID` has the `provider/model` form and would reach Ollama with a prefix it cannot resolve.

The launcher does not generate a `~/.codex/ollama-launch.config.toml` profile (that was the legacy bash engine's approach). To route a model through Ollama, configure the endpoint in `~/.codex/config.toml` yourself; `codex-wt` only selects the model name.

## Agent init

`codex-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)

Codex reads `AGENTS.md` natively, so no pointer file is needed.

## Verified on this machine

The legacy bash engine's `ollama-launch` profile generation (described in earlier versions of this doc) is no longer performed by the Go driver — it passes `--model <ModelName>` directly and relies on the user's `~/.codex/config.toml` for the Ollama endpoint. Statements about codex CLI behavior are sourced from the [Codex CLI docs](https://developers.openai.com/codex/cli/) and the [Ollama Codex integration guide](https://docs.ollama.com/integrations/codex). Re-verify after codex CLI upgrades.
