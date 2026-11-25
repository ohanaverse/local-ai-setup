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

**A model server over `https://` with a private certificate.** When the gateway's url is `https://` and `ca_file` (or `WT_MODEL_CA_FILE`) is set ([wt-session.md](../wt-session.md#tls)), wt sets `NODE_EXTRA_CA_CERTS` to that file for pi (and `WT_NODE_EXTRA_CA_CERTS`, a marker naming the same path), unless your environment already names another file there: then wt leaves yours and prints one `wt: note:`. A launch that does not reach an `https://` gateway removes a value wt set (the marker says which). Whether pi honours it end to end is to be confirmed by the phase 3 live check (#597).

## Model selection

pi picks a model from `~/.pi/agent/models.json` based on `--model <id>` and `--provider <name>` arguments. With no flags, pi prompts the user (or, in the LiteLLM wrapper, falls back to the injected default).

`pi-wt` interacts with this via `--model` passthrough. The model comes from the model server's catalog (`wt-model serve`; agents and their eligibility live in `~/.config/agent-wt/config.toml`); `pi-wt` then verifies the model is both present in `~/.pi/agent/models.json` under the `litellm` provider **and** marked `._launch: true` (only models explicitly configured for launch are used — orphaned entries are skipped) using native Go JSON parsing (no `jq` dependency). If verified, `pi-wt` execs `pi --model litellm/<gateway id>` (the model's gateway id is its registry id unless the server names another, e.g. `litellm/ollama/minimax-m3:cloud`). The route is always the model server's gateway: there are no direct routes, so pi's own provider entries are never used for a wt launch. See the "Model id contract" note in [docs/internals/agents.md](../internals/agents.md) for the rationale shared across all drivers.

**Sync on launch** (`SyncGateway`): before every non-native launch `pi-wt` writes `~/.pi/agent/models.json`'s `litellm` block, the one block a wt launch uses. The file gets one wt-created `litellm` block — `{"api": "openai-completions", "baseUrl": "<gateway origin>/v1", "apiKey": "<gateway token>"}` plus one `_launch: true` entry per non-native model of the server's catalog and the launch target, each keyed by its gateway id. Every other block marked `_wtOwned` (the non-ollama direct-route blocks wt wrote before the model server) is removed. A block without the marker is left exactly as it is: one you wrote yourself, and also the local `ollama` block older wt versions wrote (they never marked it), which may still point at `http://localhost:11434/v1`; no wt launch uses it, and you may delete it. An entry you set `_launch: false` is kept as it is. No provider origin or provider key is ever written. The file holds the gateway token, so it is written `0600` (temp file, then rename) and tightened to `0600` even when nothing else changed. The sync is idempotent and silent.

Proxy-routed models **cannot** live under a provider named like the id's first segment: pi splits a `--model` value on the first slash and matches the remainder against the named provider's entries, so `ollama/<registry-id>` would resolve to a bare-id entry and the gateway would receive the unprefixed name. That is why the block is named `litellm`: the `litellm` prefix selects the provider while the gateway id survives verbatim as the API model name.

**If the model ID is not present in `models.json`** (an entry you set `_launch: false`, an unreadable `models.json`), `pi-wt` prints a warning and falls back to pi's default model — this is intentional, not a bug. `wt smoke` treats the same fallback as a FAIL, since the row would otherwise test pi's default model instead of the one named.

For `pi/native` (the launcher's "use pi's own default" sentinel), `pi-wt` execs `pi` with no `--model` flag, writes nothing to `models.json`, and lets pi choose. See [litellm-troubleshooting.md](litellm-troubleshooting.md) for the resolution-grammar details and diagnostics.

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

`pi-wt` is a shim for the Go `wt` binary, which resolves `pi` by PATH lookup. It never sees a zsh `pi()` function, so it finds the raw npm binary, not the wrapper. The result: pi launches without LiteLLM credentials and reports "No models available."

wt does not auto-detect `~/.pi/agent/activate-litellm.sh`; the sibling spec [`2026-06-01-pi-wt-litellm-wrapper-design.md`](../superpowers/specs/2026-06-01-pi-wt-litellm-wrapper-design.md) proposes that and is not implemented. To launch pi through a wrapper, declare it as a `wrapper` profile for pi in `profiles.toml` (see [profiles.md](profiles.md)). A profile applies only when `profiles.toml` has `enabled` on and the launched model is a non-native model the profile matches. A `pi/native` launch never gets a profile: it always runs the `pi` found on PATH.

### The window wt writes into models.json (#689)

For every non-native model of the catalog and the launch target, wt keeps an
entry under the `litellm` provider with a `contextWindow` taken from the
catalog row's `context` (the registry `context_window`, else a cloud model's
host window in `tiers.toml`, else for an alias the smallest of its targets).
A new entry gets that window, or 262144 when the catalog does not know one. An
existing entry that wt launches (`_launch: true`) is brought to the catalog's
window when the catalog has one and it differs: the catalog is the source of
truth, and no other field or the order changes. An entry with an unknown
catalog window keeps whatever value it has, and a `_launch: false` entry is
left alone. A launch of a model with no known window prints one `wt: note:`
(`pi assumes 262144`) naming `wt-model model edit <id> --context-window N`.
Slot profiles are claude-only, so pi has no `context` key.

### Reaching an external LiteLLM gateway (pi's own credential command)

For a LiteLLM gateway that is not the `wt-model` server (for instance an enterprise one with a rotating credential), write the provider in `~/.pi/agent/models.json` yourself and let pi run the credential command. wt has no part in it: launch with `wt -A pi` and the `pi/native` entry (no `--model`), or run `pi --model <provider>/<id>` directly.

```json
{
  "providers": {
    "my-gateway": {
      "api": "openai-completions",
      "baseUrl": "https://<gateway>/v1",
      "apiKey": "!/path/to/credential-command",
      "compat": { "supportsStore": false },
      "models": [ { "id": "<model id>", "contextWindow": 200000, "input": ["text"], "reasoning": true } ]
    }
  }
}
```

- `apiKey` starting with `!` is a shell command whose stdout is the key. Checked with pi 1.6.0 (2026-11-23): pi runs it on each model request, not once per process, so a rotated credential is picked up mid-session; `pi --list-models` does not run it. Older pi docs describe per-process caching, so re-check after a pi upgrade.
- `compat.supportsStore = false` is for gateway models whose backend (Bedrock) rejects the OpenAI `store` parameter. wt no longer writes it; it is yours to set.
- Leave out `_wtOwned`. A block carrying that marker is a leftover of the old direct-route sync, and the next non-native launch deletes it. A block without it is never touched: wt writes it back exactly as it read it, including `headers`, extra `compat` keys and per-model fields it has no struct for (`TestPiGatewayKeepsAnExternalGatewayBlockByteForByte`).
- This route has no wt picker and no `-M`: wt only knows the models in the `wt-model` catalog. Pick the model inside pi.
- Do not put a literal credential in the file; the command is what keeps it current.

### Background context (provider system)

pi's provider system is provider-agnostic: any provider declared in `models.json` (or via an extension that overrides selection) can serve models, as long as its API surface matches one of pi's supported types (`openai-completions`, `anthropic`, etc.). The env-var substitution syntax (`$NAME` in any string field) is resolved at launch. The credential-refresh-on-launch pattern — fetch a fresh key in a wrapper, then `exec pi` — keeps short-lived enterprise credentials working without persisting them to disk.

The Obsidian note `Pi Coding Agent with LiteLLM.md` describes a generic LiteLLM topology with `localhost:4000` and `anthropic/`-prefixed model IDs. The deployed reality on this machine differs (real gateway URL `https://llm-gateway.nyt.net`, bare model IDs). This file records the deployed reality; the Obsidian note remains the broader narrative reference for users with a different LiteLLM topology.
