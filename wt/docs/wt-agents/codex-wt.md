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

When using `codex-wt` with a non-native model, codex talks to a wt-declared provider endpoint instead of `api.openai.com`; that endpoint is the model server's gateway (`wt-model serve`; see [Through the model server's gateway](#through-the-model-servers-gateway) below). `codex-wt` declares that provider inline on the command line, so **no `auth.json` and no `~/.codex/config.toml` entry are required** for that flow. The old failure mode — codex defaulting to the `openai` provider and prompting to **sign in** — no longer happens for wt-launched models.

**A model server over `https://` with a private certificate.** wt sets no certificate variable for codex: it must trust the gateway's certificate through the system trust store (on macOS, the login keychain), or through its own setting for extra roots, which is unverified until the phase 3 live check (#597) records it. wt's own control calls trust `ca_file` ([wt-session.md](../wt-session.md#tls)); the agent does not read it.

## Model selection

Codex picks a model from `--model <name>` (or a `--profile <name>` + `-m <model>` combination). `codex-wt` handles two cases:

- **`codex/native`** — passes no provider config and no `--model` flag, so codex uses its own default model and its own subscription (it may prompt to **sign in**; that is the expected behavior for an explicit native choice).
- **Every other model** — declares wt's provider via inline `-c` config overrides pointed at the model server's gateway, then passes `--model <gateway id>` (see [Through the model server's gateway](#through-the-model-servers-gateway) below).

## Agent init

`codex-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)

Codex reads `AGENTS.md` natively, so no pointer file is needed.

### Through the model server's gateway

Every non-native model goes through the model server's gateway (see [wt-session.md](../wt-session.md)); there are no direct routes. The provider block is:

```
-c model_provider=agent-wt
-c 'model_providers.agent-wt.name="Ollama"'
-c 'model_providers.agent-wt.base_url="<gateway origin>/v1/"'  # trailing slash normalized
-c 'model_providers.agent-wt.wire_api="responses"'
-c 'model_providers.agent-wt.env_key="AGENT_WT_GATEWAY_API_KEY"'
--model <gateway id>
```

The `--model` value is the model's gateway id — its full registry id (e.g. `ollama/qwen3.8:27b-mlx`) unless the server names another — not the bare provider-specific name. codex custom providers take no inline API key: the gateway token is exported as `AGENT_WT_GATEWAY_API_KEY` and read via the `env_key` override above — without it the gateway rejects every request. Native models (`codex/native`) continue to use codex's own subscription and never see the gateway.

Two properties matter. First, the base_url is the gateway origin with the OpenAI-compatible `/v1/` path (`r.BaseOrigin + "/v1/"`); `claude` uses the bare origin (no path suffix) for the anthropic-compatible endpoint, while `copilot` uses `r.BaseOrigin + "/v1"` with `COPILOT_PROVIDER_WIRE_API="completions"` (chat-completions — its `responses` wire drops leading characters through the OpenAI-compatible bridge; see [copilot-wt.md](copilot-wt.md)). Second, the provider is namespaced `agent-wt` rather than `ollama-launch` so a user-authored `[model_providers.ollama-launch]` (e.g. written by `ollama launch codex`) in their main `config.toml` cannot leak a stray field into the fields `codex-wt` fully controls. The `-c` overrides are self-contained on the command line, so the launcher writes nothing to `~/.codex/`.

> **Upstream incompatibility (litellm ≤ 1.98.0) — worked around.** codex ≥ 0.148 only speaks the responses API (`wire_api = "chat"` was removed), and litellm's responses→`ollama_chat` bridge crashes on codex's structured `reasoning` object (`TypeError: unhashable type: 'dict'` → 500 on every attempt; codex masks it as "high demand"). Workaround: add `additional_drop_params: ["reasoning_effort"]` to the model's `litellm_params` in `~/.config/litellm/config.yaml` and restart the proxy (applied to `ollama/glm-5.3-flash:cloud` on 2026-08-31; every `ollama_chat/*` model needs it for codex). Proper fix ships in a litellm release past 1.98.0. Details: [litellm-troubleshooting.md](litellm-troubleshooting.md).

## Verified on this machine

Before the LiteLLM cutover, ollama-routed `codex-wt` launches (straight to ollama, long gone) reproduced a `provider: ollama-launch`-style run against the local `http://127.0.0.1:11434` gateway with no sign-in prompt and no `~/.codex/` file writes, even with no `~/.codex/auth.json` and no `model_provider` set in the user's `config.toml`. The provider name shown in codex's banner is the configured `agent-wt` provider. Statements about codex CLI behavior are sourced from the [Codex CLI docs](https://developers.openai.com/codex/cli/) and the [Ollama Codex integration guide](https://docs.ollama.com/integrations/codex). Re-verify after codex CLI upgrades.
