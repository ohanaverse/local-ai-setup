# pi-wt

## Overview

`pi-wt` is the worktree launcher for [pi](https://github.com/mariozechner/pi-coding-agent), a provider-agnostic AI coding agent built by Mario Zechner. pi reads its provider list and model catalogue from `~/.pi/agent/models.json` at startup and presents whichever models are reachable with the current credentials. See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
npm install -g @mariozechner/pi-coding-agent
```

This installs the `pi` binary globally. The `pi-wt` file in this repo is a shim that forwards to `wt --agent pi`. Install `pi-wt` itself by copying `bin/pi-wt` (and the rest of `bin/`) into `~/.local/bin/`. The launcher verifies models against `~/.pi/agent/models.json` using native Go JSON parsing (no `jq` dependency).

## Configuration files & locations

pi reads from `~/.pi/agent/`. Key files:

| File | Purpose |
|---|---|
| `~/.pi/agent/models.json` | Provider and model catalogue. pi only shows models declared here. |
| `~/.pi/agent/settings.json` | Non-credential UI state (last-seen pi version, theme, installed packages). |
| `~/.pi/agent/activate-litellm.sh` | **NYT-specific wrapper.** Fetches LiteLLM credentials, exports them, and `exec`s `pi`. Optional — vanilla pi users do not have this file. |

`models.json` supports environment-variable substitution at runtime: `apiKey: $ANTHROPIC_API_KEY` reads the env var when pi launches, so credentials never have to live on disk inside `models.json`.

## Authentication & credentials

pi obtains API keys from environment variables referenced in `~/.pi/agent/models.json`. The credential-refresh-on-launch pattern is: a wrapper script fetches a fresh key (typically via a credential helper that talks to a vault or SSO endpoint), exports the key into the environment, and `exec`s `pi`. pi then resolves `$ANTHROPIC_API_KEY` (or whichever variable the provider references) when it parses `models.json`.

In the NYT LiteLLM deployment on this machine, the wrapper is `~/.pi/agent/activate-litellm.sh`. It calls `/usr/local/bin/litellm-credential-helper.sh` to fetch a LiteLLM key, exports `ANTHROPIC_API_KEY` and `LITELLM_API_KEY`, sets `ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-7` and `ANTHROPIC_CUSTOM_HEADERS="x-litellm-tags: cc_settings:6ebaf51"`, then `exec`s `pi "$@"`. The wrapper also injects `--provider anthropic --model claude-opus-4-7` when neither `--model` nor `--provider` is already in `"$@"`.

## Model selection

pi picks a model from `~/.pi/agent/models.json` based on `--model <id>` and `--provider <name>` arguments. With no flags, pi prompts the user (or, in the LiteLLM wrapper, falls back to the injected default).

`pi-wt` interacts with this via `--model` passthrough. The launcher's rotation chooses a model from `~/.config/agent-wt/config.toml`; `pi-wt` then verifies the model is both present in `~/.pi/agent/models.json` **and** marked `._launch: true` (only models explicitly configured for launch are used — orphaned entries are skipped) using native Go JSON parsing (no `jq` dependency). If verified, `pi-wt` execs `pi --model <id>` where `<id>` is the **bare** provider-specific name (`config.Model.ModelName`), not the registry key. pi was the first driver to follow this contract; see the "Model id contract" note in CLAUDE.md for the rationale shared across all drivers.

**Auto-sync on launch:** `pi-wt` automatically syncs non-native models from `config.toml` into `~/.pi/agent/models.json` if they are missing — including both cloud models (`:cloud` suffix) and local models (MLX, etc.). This happens on every launch and is idempotent. Direct mode only ever adds entries under the `ollama` provider; gateway mode additionally maintains the `litellm` provider block (above), reverts stale gateway values found in the `ollama` block, and removes only wt-generated gateway entries from it — user-authored entries always survive. The sync runs silently unless it changes state, in which case it logs:
```bash
pi-wt: synced N model(s) to pi models.json
```

**If the model ID is not present in `models.json` and sync fails**, `pi-wt` prints a warning and falls back to pi's default model — this is intentional, not a bug. The sync function uses native Go JSON parsing (no `jq` dependency).

For `pi/native` (the launcher's "use pi's own default" sentinel), `pi-wt` execs `pi` with no `--model` flag and lets pi (or the LiteLLM wrapper, if present) choose.

### Gateway mode (LiteLLM)

When `[gateway].mode = "litellm"` is set in `~/.config/agent-wt/config.toml`, `pi-wt` routes non-native models through the LiteLLM proxy at `http://localhost:4000` instead of the local Ollama gateway.

Gateway models **cannot** live under pi's `ollama` provider: pi splits a `--model` value on the first slash and matches the remainder against the named provider's entries, so `ollama/<registry-id>` would always resolve to the bare-id entry and LiteLLM would receive the unprefixed name. The sync therefore manages two provider blocks:

- **`litellm` (wt-created)** — `{"api": "openai-completions", "baseUrl": "http://localhost:4000/v1", "apiKey": "<gateway.api_key>"}` plus one `_launch: true` entry per non-native registry model, keyed by the full registry id (e.g. `ollama/qwen3.8:27b-mlx`). Launches use `--model litellm/<registry-model-id>`; the `litellm` prefix selects the provider while the registry id survives verbatim as the API model name.
- **`ollama` (pi's direct provider)** — always reset to the standard local endpoint with pi's placeholder key when its values match the gateway or a local-ollama variant, and pruned of wt-generated registry-id entries an older sync added there (pi's `--model` grammar can never launch them). Bare-name entries and every non-`ollama`/`litellm` provider round-trip untouched.

All three values are required: pi's models.json schema rejects an empty `apiKey` (a single bad provider invalidates the whole catalog — "No models available"), so direct-mode reverts write the placeholder `"ollama"`, never `""`. Native models (`pi/native`) continue to use pi's default provider and ignore the gateway. See [litellm-troubleshooting.md](litellm-troubleshooting.md) for the resolution-grammar details and diagnostics.

## Agent init

`pi-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)

Pi has no project-level instruction file convention, so no pointer file is created.

## Verified on this machine

**Verified on this machine, 2026-06-01.**

Concrete observations from this machine:

- **Binary:** `~/.asdf/installs/nodejs/25.8.0/bin/pi` (npm-installed, raw).
- **Wrapper script:** `~/.pi/agent/activate-litellm.sh`. Fetches LiteLLM key via `/usr/local/bin/litellm-credential-helper.sh`, exports `ANTHROPIC_API_KEY` and `LITELLM_API_KEY`, sets `ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-7` and `ANTHROPIC_CUSTOM_HEADERS="x-litellm-tags: cc_settings:6ebaf51"`. Injects `--provider anthropic --model claude-opus-4-7` only when neither `--model` nor `--provider` is present in the args, then `exec pi "$@"`.
- **Interactive entry point:** `pi()` zsh function in `~/.zshrc` (lines 161–165), calling `activate-litellm.sh "$@"`.
- **Provider config:** `~/.pi/agent/models.json` declares one provider, `anthropic`, named "Anthropic (LiteLLM proxy)", with:
  - `baseUrl: https://llm-gateway.nyt.net`
  - `api: openai-completions` (an OpenAI-compatible API surface routed through LiteLLM to Anthropic)
  - `apiKey: $ANTHROPIC_API_KEY` (env-var substitution at runtime)
  - Four model IDs: `claude-opus-4-7`, `claude-opus-4-6`, `claude-sonnet-4-6`, `claude-haiku-4-5`
  - Bare model IDs (no `anthropic/` prefix), differing from the Obsidian note's generic example.
- **Credential helper:** `/usr/local/bin/litellm-credential-helper.sh` (root-owned, executable, ~5.9KB). Implementation details out of scope; treated as opaque dependency.
- **Settings:** `~/.pi/agent/settings.json` records last-seen pi version (`0.78.0`), theme, and one installed package (`https://github.com/obra/superpowers`).

### Documented gotcha (pi-wt-specific)

`pi-wt` is a bash script. Bash scripts do not inherit zsh shell functions, so `pi-wt`'s `exec pi …` call performs PATH lookup and finds the raw npm binary, not the wrapper. The result: pi launches without LiteLLM credentials and reports "No models available."

This is fixed by the sibling spec [`2026-06-01-pi-wt-litellm-wrapper-design.md`](../superpowers/specs/2026-06-01-pi-wt-litellm-wrapper-design.md), which auto-detects `~/.pi/agent/activate-litellm.sh` and execs it instead of `pi` when present.

### Background context (provider system)

pi's provider system is provider-agnostic: any provider declared in `models.json` (or via an extension that overrides selection) can serve models, as long as its API surface matches one of pi's supported types (`openai-completions`, `anthropic`, etc.). The env-var substitution syntax (`$NAME` in any string field) is resolved at launch. The credential-refresh-on-launch pattern — fetch a fresh key in a wrapper, then `exec pi` — keeps short-lived enterprise credentials working without persisting them to disk.

The Obsidian note `Pi Coding Agent with LiteLLM.md` describes a generic LiteLLM topology with `localhost:4000` and `anthropic/`-prefixed model IDs. The deployed reality on this machine differs (real gateway URL `https://llm-gateway.nyt.net`, bare model IDs). This file records the deployed reality; the Obsidian note remains the broader narrative reference for users with a different LiteLLM topology.
