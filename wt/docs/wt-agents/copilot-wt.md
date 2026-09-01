# copilot-wt

## Overview

`copilot-wt` is the worktree launcher for [GitHub Copilot CLI](https://github.com/github/copilot-cli). Copilot CLI authenticates against the GitHub Copilot subscription and supports routing to alternative providers via env-var configuration (e.g., the [Ollama-Copilot integration](https://docs.ollama.com/integrations/copilot-cli)). See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
npm install -g @github/copilot
```

This installs the `copilot` binary. The `copilot-wt` file in this repo is a shim that forwards to `wt --agent copilot`. Install `copilot-wt` itself by copying `bin/copilot-wt` into `~/.local/bin/`.

## Configuration files & locations

Copilot CLI reads its primary state from `~/.copilot/` (per upstream Copilot CLI docs). Auth state is tied to the user's GitHub session. See the [Copilot CLI docs](https://github.com/github/copilot-cli) for the full file layout.

## Authentication & credentials

Native auth: `gh auth login` plus a Copilot subscription. Provider override: set `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, and `COPILOT_PROVIDER_WIRE_API` to point Copilot at a non-GitHub endpoint (Ollama, OpenAI-compatible proxies). See the [Ollama integration docs](https://docs.ollama.com/integrations/copilot-cli) for the canonical example.

## Model selection

Copilot picks a model from `--model <name>` or via the `COPILOT_MODEL` environment variable. `copilot-wt` does not pass `--model` directly; it handles two cases:

- **`copilot/native`** — clears any inherited `COPILOT_*` env vars, so Copilot uses its native subscription.
- **Ollama-routed models** — sets `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_PROVIDER_WIRE_API`, and `COPILOT_MODEL` env vars pointing at the local Ollama OpenAI-compatible gateway (see [Cloud models via Ollama](#cloud-models-via-ollama)).

### Cloud models via Ollama

When a non-native model (e.g., `minimax-m2.7:cloud`) is selected, `copilot-wt` sets:

```bash
COPILOT_PROVIDER_BASE_URL="http://localhost:11434/v1"
COPILOT_PROVIDER_API_KEY=""
COPILOT_PROVIDER_WIRE_API="completions"
COPILOT_MODEL="<bare provider-specific name>"  # NOT <provider>/<model>
```

`<bare provider-specific name>` is `config.Model.ModelName` (e.g. `minimax-m3:cloud`). The registry key `config.Model.ID` would carry the `ollama/` prefix and reach the Ollama-side upstream unresolved. The base URL is `config.OllamaBaseURL` (`http://localhost:11434`) with a `/v1` suffix, matching the OpenAI-compatible endpoint that Copilot CLI's BYOK provider expects.

`WIRE_API=completions` (chat-completions) is deliberate: Copilot CLI's `responses` wire drops leading characters through the OpenAI-compatible bridge, making one-shot prompts unreliable (observed via LiteLLM's responses bridge with `glm-5.3-flash:cloud`). This diverges from `ollama launch copilot`, which still prescribes `responses`. Re-test when LiteLLM's responses bridge is fixed ([BerriAI/litellm#37452](https://github.com/BerriAI/litellm/issues/37452)) or copilot CLI's responses client changes.

### Gateway mode (LiteLLM)

When `[gateway].mode = "litellm"` is set in `~/.config/agent-wt/config.toml`, `copilot-wt` routes non-native models through the LiteLLM proxy at `http://localhost:4000` instead of the local Ollama gateway. The launcher sets:

```bash
COPILOT_PROVIDER_BASE_URL="http://localhost:4000/v1"  # trailing slash normalized
COPILOT_PROVIDER_API_KEY="<gateway.api_key>"
COPILOT_PROVIDER_WIRE_API="completions"
COPILOT_MODEL="<registry-model-id>"  # e.g. ollama/qwen3.8:27b-mlx
```

The `COPILOT_MODEL` value is the full registry model id, not the bare provider-specific name. The `COPILOT_PROVIDER_API_KEY` comes from `[gateway].api_key`. Native models (`copilot/native`) continue to use the native subscription and ignore the gateway.

Verified in both gateway modes 2026-09-01 with `ollama/glm-5.3-flash:cloud` (one-shot prompt answered end-to-end in direct and litellm modes; the earlier 2026-08-31 litellm-mode matrix run had verified the `responses` wire with the proxy-side `drop_params` workaround below, before the truncation was attributed to the responses wire itself — the responses-era PASS and the truncation both went through the same bridge, which is why the wire was switched). This requires the proxy to tolerate params copilot sends that `ollama_chat` does not support (e.g. `parallel_tool_calls`) — set `litellm_settings: drop_params: true` in `~/.config/litellm/config.yaml` and restart the proxy; otherwise litellm answers `400 UnsupportedParamsError`. See [litellm-troubleshooting.md](litellm-troubleshooting.md).

## Agent init

`copilot-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)
- `.github/copilot-instructions.md` — pointer containing `Read AGENTS.md and follow all instructions in it.` (if missing)

Copilot doesn't support file imports, so the pointer file uses a plain-text instruction.

## Verified on this machine

**Not installed on this machine.** Statements above are sourced from the upstream GitHub Copilot CLI docs and the linked Ollama integration page. Re-verify before relying on details for env-var names or auth flow.
