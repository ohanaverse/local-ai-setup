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
- **Every other model** — sets `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_PROVIDER_WIRE_API`, and `COPILOT_MODEL` env vars pointing at the model server's gateway (see [Through the model server's gateway](#through-the-model-servers-gateway)).

### Through the model server's gateway

Every non-native model goes through the model server's gateway (`wt-model serve`; see [wt-session.md](../wt-session.md)); there are no direct routes. The launcher sets:

```bash
COPILOT_PROVIDER_BASE_URL="<gateway origin>/v1"  # trailing slash normalized
COPILOT_PROVIDER_API_KEY="<gateway token>"
COPILOT_PROVIDER_WIRE_API="completions"
COPILOT_MODEL="<gateway id>"  # e.g. ollama/qwen3.8:27b-mlx
```

The `COPILOT_MODEL` value is the model's gateway id — its full registry id unless the server names another — not the bare provider-specific name. The token is the model server's client token, which the gateway swaps for the LiteLLM key. Native models (`copilot/native`) continue to use the native subscription and never see the gateway.

`WIRE_API=completions` (chat-completions) is deliberate: Copilot CLI's `responses` wire drops leading characters through the OpenAI-compatible bridge, making one-shot prompts unreliable (observed via LiteLLM's responses bridge with `glm-5.3-flash:cloud`). This diverges from `ollama launch copilot`, which still prescribes `responses`. Re-test when LiteLLM's responses bridge is fixed ([BerriAI/litellm#37452](https://github.com/BerriAI/litellm/issues/37452)) or copilot CLI's responses client changes.

Verified 2026-09-01 with `ollama/glm-5.3-flash:cloud` (one-shot prompt answered end-to-end straight against ollama and through LiteLLM, before the model server; the earlier 2026-08-31 LiteLLM matrix run had verified the `responses` wire with the proxy-side `drop_params` workaround below, before the truncation was attributed to the responses wire itself — the responses-era PASS and the truncation both went through the same bridge, which is why the wire was switched). This requires the proxy to tolerate params copilot sends that `ollama_chat` does not support (e.g. `parallel_tool_calls`) — wt's route writes enforce `litellm_settings: drop_params: true` in `~/.config/litellm/config.yaml`; otherwise litellm answers `400 UnsupportedParamsError`. See [litellm-troubleshooting.md](litellm-troubleshooting.md).

## Agent init

`copilot-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)
- `.github/copilot-instructions.md` — pointer containing `Read AGENTS.md and follow all instructions in it.` (if missing)

Copilot doesn't support file imports, so the pointer file uses a plain-text instruction.

## Verified on this machine

**Not installed on this machine.** Statements above are sourced from the upstream GitHub Copilot CLI docs and the linked Ollama integration page. Re-verify before relying on details for env-var names or auth flow.
