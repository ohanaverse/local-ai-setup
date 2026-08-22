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

When using `codex-wt` with a rotation-selected (ollama-routed) model, codex talks to a local Ollama endpoint instead of `api.openai.com` — see [Model selection](#model-selection) below. `codex-wt` declares that Ollama provider inline on the command line, so **no `auth.json` and no `~/.codex/config.toml` entry are required** for that flow. The old failure mode — codex defaulting to the `openai` provider and prompting to **sign in** — no longer happens for ollama-routed models.

## Model selection

Codex picks a model from `--model <name>` (or a `--profile <name>` + `-m <model>` combination). `codex-wt` handles two cases:

- **`codex/native`** — passes no provider config and no `--model` flag, so codex uses its own default model and its own subscription (it may prompt to **sign in**; that is the expected behavior for an explicit native choice).
- **Ollama-routed models** — declares the Ollama provider via four inline `-c` config overrides, then passes `--model <name>`, where `<name>` is the **bare** provider-specific name (`config.Model.ModelName`). The registry key `config.Model.ID` has the `provider/model` form and would reach Ollama with a prefix it cannot resolve. The provider block looks like:

  ```
  -c model_provider=agent-wt
  -c 'model_providers.agent-wt.name="Ollama"'
  -c 'model_providers.agent-wt.base_url="http://localhost:11434/v1/"'
  -c 'model_providers.agent-wt.wire_api="responses"'
  --model <name>
  ```

  Two properties matter. First, the base_url uses Ollama's OpenAI-compatible `/v1/` path (`config.OllamaBaseURL + "/v1/"`); `claude`/`copilot` use the bare gateway URL instead because they target the anthropic-compatible endpoint. Second, the provider is namespaced `agent-wt` rather than `ollama-launch` so a user-authored `[model_providers.ollama-launch]` (e.g. written by `ollama launch codex`) in their main `config.toml` cannot leak a stray field into the fields `codex-wt` fully controls. The `-c` overrides are self-contained on the command line, so the launcher writes nothing to `~/.codex/`.

## Agent init

`codex-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)

Codex reads `AGENTS.md` natively, so no pointer file is needed.

## Verified on this machine

Ollama-routed `codex-wt` launches reproduce a `provider: ollama-launch`-style run against the local `http://127.0.0.1:11434` gateway with no sign-in prompt and no `~/.codex/` file writes, even with no `~/.codex/auth.json` and no `model_provider` set in the user's `config.toml`. The provider name shown in codex's banner is the configured `agent-wt` provider. Statements about codex CLI behavior are sourced from the [Codex CLI docs](https://developers.openai.com/codex/cli/) and the [Ollama Codex integration guide](https://docs.ollama.com/integrations/codex). Re-verify after codex CLI upgrades.
