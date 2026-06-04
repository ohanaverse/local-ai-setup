# copilot-wt

## Overview

`copilot-wt` is the worktree launcher for [GitHub Copilot CLI](https://github.com/github/copilot-cli). Copilot CLI authenticates against the GitHub Copilot subscription and supports routing to alternative providers via env-var configuration (e.g., the [Ollama-Copilot integration](https://docs.ollama.com/integrations/copilot-cli)). See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
npm install -g @github/copilot
```

This installs the `copilot` binary. The `copilot-wt` launcher in this repo wraps `copilot` with worktree-aware FZF selection and code/design model rotation. Install `copilot-wt` itself by copying `bin/copilot-wt` into `~/.local/bin/`.

## Configuration files & locations

Copilot CLI reads its primary state from `~/.copilot/` (per upstream Copilot CLI docs). Auth state is tied to the user's GitHub session. See the [Copilot CLI docs](https://github.com/github/copilot-cli) for the full file layout.

## Authentication & credentials

Native auth: `gh auth login` plus a Copilot subscription. Provider override: set `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, and `COPILOT_PROVIDER_WIRE_API` to point Copilot at a non-GitHub endpoint (Ollama, OpenAI-compatible proxies). See the [Ollama integration docs](https://docs.ollama.com/integrations/copilot-cli) for the canonical example.

## Model selection

Copilot picks a model from `--model <name>` or via the `COPILOT_MODEL` environment variable. `copilot-wt` does not pass `--model` directly; instead it sets the `COPILOT_PROVIDER_*` env vars when the rotation selects an Ollama-style local model, and maps `native:copilot` to `--model auto` so Copilot uses its own model selection logic. The Ollama base URL is read from `PROVIDER_OLLAMA_BASE_URL` in `~/.config/ai-shell/models.conf`, falling back to `http://localhost:11434`.

### Cloud models via Ollama

When a cloud model (e.g., `minimax-m2.7:cloud`) is selected from rotation and is available in Ollama, `copilot-wt` sets:

```bash
COPILOT_PROVIDER_BASE_URL="http://localhost:11434/v1"
COPILOT_PROVIDER_API_KEY=""
COPILOT_PROVIDER_WIRE_API="responses"
COPILOT_PROVIDER_MODEL_ID="gpt-4"  # Suppresses "not in catalog" warning
COPILOT_MODEL="<selected-model>"
```

The `COPILOT_PROVIDER_MODEL_ID` tells Copilot to use a well-known model's configuration (token limits, agent behavior) while still sending the actual model name to Ollama. This suppresses the warning: `! Model "<name>" is not in the built-in catalog.`

## Agent init

`copilot-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)
- `.github/copilot-instructions.md` — pointer containing `Read AGENTS.md and follow all instructions in it.` (if missing)

Copilot doesn't support file imports, so the pointer file uses a plain-text instruction.

## Verified on this machine

**Not installed on this machine.** Statements above are sourced from the upstream GitHub Copilot CLI docs and the linked Ollama integration page. Re-verify before relying on details for env-var names or auth flow.
