# Config file map

> Use this to: find which tool owns, writes, and reads each config file before you edit one.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

None — this is a reference doc, not a procedure.

## TL;DR

| File | Owner (writes) | Consumers | Purpose |
|---|---|---|---|
| `~/.config/local-ai/registry.toml` | `modelman` (TUI add/edit, `modelman migrate`) | `wt` (read-only), LiteLLM exposure | Canonical providers + models |
| `~/.config/local-ai/modelman.toml` | `modelman` | `modelman` | Per-machine state: downloads, LiteLLM exposure flags, family display names |
| `~/.config/local-ai/settings.yaml` | `modelman` | `modelman` | User preferences (theme) |
| `~/.config/local-ai/config.yaml` | you by hand (pre-modelman) | `modelman migrate` (read-only input) | Legacy provider types — superseded by `registry.toml` |
| `~/.config/local-ai/families/*.yaml` | pre-registry modelman tooling | `modelman migrate` (read-only input) | Legacy per-family variants + download markers |
| `~/.config/litellm/config.yaml` | `modelman` (expose toggle), you by hand | LiteLLM proxy | `model_list`, general settings |
| `~/.config/agent-wt/config.toml` | `wt` | `wt` | Agents, default rotation tag (NO providers/models — modelman owns those in registry.toml) |
| `~/.config/agent-wt/themes.toml` | `wt` (`wt config theme`) | `wt` | Active theme |
| `~/.config/agent-wt/models.conf` | you by hand (pre-wt bash era) | `wt` first-run migration (read-only input) | Legacy bash rotation config |
| `~/.config/agent-wt/usage.jsonl` | `wt` | `modelman usage` | Launch log |
| `~/.config/agent-wt/rotation.state` + `rotation-*.state` | `wt` | `wt`, `modelman usage` | Rotation position |
| `~/Library/LaunchAgents/local.litellm.proxy.plist` | you (setup = `01-initial-setup.md`) | launchd | LiteLLM proxy on :4000 |
| `~/Library/LaunchAgents/local.llamacpp.server.plist` | you (setup = `01-initial-setup.md`) | launchd | `llama-server` on :8080 |
| `~/Library/LaunchAgents/homebrew.mxcl.omlx.plist` | Homebrew (setup = `01-initial-setup.md`) | launchd | oMLX server on :8000 |
| `~/Library/LaunchAgents/homebrew.mxcl.redis.plist` | Homebrew | launchd | Redis for LiteLLM coordination |
| `~/Library/LaunchAgents/homebrew.mxcl.postgresql@16.plist` | Homebrew | launchd | Postgres for LiteLLM (`localhost:5432/litellm`) |

Ollama has no LaunchAgent plist — it runs as the Ollama.app login item (`com.ollama.ollama` in `launchctl list`).

## The files

### `~/.config/local-ai/registry.toml`

- **Owner:** `modelman` — TUI queue applies on exit, and `modelman migrate`.
- **Consumers:** `wt` (read-only; joins it in memory with `~/.config/agent-wt/config.toml`), `modelman` when exposing (copies `model_info` into LiteLLM `model_list` entries).
- **Purpose:** canonical providers + models. `providers` is still empty on this machine; only Ollama models are recorded, with `source = "discovered"`.
- **Env override:** `MODELMAN_REGISTRY`.

```toml
providers = []

[[models]]
id = "ollama/glm-5.3-flash:cloud"
family = "glm-5.3-flash:cloud"
provider_id = "ollama"
model_name = "glm-5.3-flash:cloud"
location = "cloud"
source = "discovered"
tags = []
```

### `~/.config/local-ai/modelman.toml`

- **Owner:** `modelman` (written on every TUI apply, `sync`, `expose`/`unexpose`).
- **Consumers:** `modelman` only.
- **Purpose:** per-machine state: downloads, LiteLLM exposure flags, family display names (`[families]` is present but empty here).
- **Env override:** `MODELMAN_STATE`.

```toml
[model_state."ollama/qwen3.8:27b-mlx"]
downloaded = true
disk_path = "ollama:qwen3.8:27b-mlx"
size_bytes = 19327352832
litellm_exposed = false

[model_state."ollama/ornith-1.5:35b"]
downloaded = true
disk_path = "ollama:ornith-1.5:35b"
size_bytes = 23622320128
litellm_exposed = false
```

### `~/.config/local-ai/settings.yaml`

- **Owner:** `modelman` (TUI preferences).
- **Consumers:** `modelman` only.
- **Purpose:** user preferences (theme).
- **Env override:** `MODELMAN_SETTINGS`.

```yaml
theme: atom-one-light
```

### `~/.config/local-ai/config.yaml` (legacy)

- **Owner:** you by hand, pre-modelman. modelman no longer reads it outside `modelman migrate`.
- **Consumers:** `modelman migrate` (read-only input; imports providers into `registry.toml`).
- **Purpose:** legacy provider types + paths. Superseded by `registry.toml` `[[providers]]`.
- **Env override:** `MODELMAN_CONFIG`.

```yaml
providers:
  ollama:
    type: ollama
  llamacpp:
    type: llamacpp
  omlx:
    type: omlx
    model_dir: ~/.omlx/models
```

### `~/.config/local-ai/families/*.yaml` (legacy)

- **Owner:** pre-registry modelman tooling (files dated 2026-08-27; current `modelman.toml` supersedes the download markers in them).
- **Consumers:** `modelman migrate` (read-only input). Every current TUI path reads `registry.toml`/`modelman.toml` instead.
- **Purpose:** legacy family manifests: variants per provider, per-variant download state.
- **Env override:** `MODELMAN_FAMILY_DIR`.

```yaml
family: qwen3.8
display_name: Qwen 3.8
variants:
- id: ollama/qwen3.8:27b-mlx
  provider: ollama
  name: qwen3.8:27b-mlx
  repo: null
  files: null
  quantizations: null
  model_info:
    supports_vision: true
```

### `~/.config/litellm/config.yaml`

- **Owner:** `modelman` (expose/unexpose writes `model_list` entries), you by hand.
- **Consumers:** LiteLLM proxy (started by `~/Library/LaunchAgents/local.litellm.proxy.plist`, port 4000).
- **Purpose:** `model_list` (one entry per exposed model: Ollama, oMLX, llama.cpp, OpenRouter) plus `general_settings` (`database_url` → local Postgres, `coordination_redis` → local Redis). modelman only touches `model_list`; `general_settings` and unrecognized sections are preserved.
- **Env override:** `MODELMAN_LITELLM_CONFIG`.
- OpenRouter entries contain real `api_key: sk-or-v1-…` values — redact before sharing this file.

```yaml
model_list:
  # ---- Ollama (local) ----
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true

  # ---- oMLX / MLX (Apple Silicon fourth backend) ----
  # oMLX is OpenAI-compatible; use the openai/ prefix with api_base override
  - model_name: omlx/Qwen3.8-27B-4bit
    litellm_params:
      model: openai/Qwen3.8-27B-4bit
```

### `~/.config/agent-wt/config.toml`

- **Owner:** `wt` (`wt config` editor; writes atomically on save).
- **Consumers:** `wt` only.
- **Purpose:** agents and default rotation tag (`default_tag = "code"`). NO live providers/models — modelman owns those in `registry.toml`; `wt` joins that file in memory and `wt config` never writes providers or models.
- On this machine the file still contains `[[providers]]`/`[[models]]` blocks: wt's one-time `models.conf` migration wrote them as an exchange format for `modelman migrate`. `wt` never reads Providers/Models back out of `config.toml` — treat those blocks as inert.
- **Env override:** `XDG_CONFIG_HOME` (config dir is `~/.config/agent-wt/` or `$XDG_CONFIG_HOME/agent-wt/`).

```toml
default_tag = "code"

[[providers]]
  id = "ollama"
  name = "Ollama"
  [providers.auth]
    type = "none"
    base_url = "http://localhost:11434"
```

### `~/.config/agent-wt/themes.toml`

- **Owner:** `wt` (`wt config theme <name>`).
- **Consumers:** `wt` (every TUI picker, CLI tables).
- **Purpose:** active theme (`tokyo-night` currently set).

```toml
theme = "tokyo-night"
```

### `~/.config/agent-wt/models.conf` (legacy)

- **Owner:** you by hand, in the bash `ai-shell` era ("Managed by dotfiles").
- **Consumers:** wt's first-run migration (runs once if `config.toml` is missing; reads `CODE_MODELS`/`DESIGN_MODELS` arrays and provider base URLs).
- **Purpose:** legacy rotation + provider config. Superseded by `~/.config/agent-wt/config.toml` + `registry.toml`.

```bash
# Default model (fallback when the selected rotation array is empty)
DEFAULT_MODEL="minimax-m3:cloud"

# Coding rotation — all available models
CODE_MODELS=(
  "native:copilot"
  "deepseek-v4-pro:cloud"
  "native:claude"
```

### `~/.config/agent-wt/usage.jsonl`

- **Owner:** `wt` (appends one line per launch).
- **Consumers:** `modelman usage` (usage reports / reconciliation with LiteLLM logs).
- **Purpose:** launch log. A sibling `usage.jsonl.lock` is wt's empty lock file.

```json
{"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.102105Z"}
{"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.133588Z"}
```

### `~/.config/agent-wt/rotation.state` (+ `rotation-*.state`)

- **Owner:** `wt` (one file per rotation slot).
- **Consumers:** `wt` (rotation cursor), `modelman usage` (last-launched model).
- **Purpose:** rotation position — the file body is just the model id of the last launch. Per-slot variants on this machine: `rotation-claude-code-_.state`, `rotation-pi-code-_.state`.

```
ollama/glm-5.3-flash:cloud
```

### `~/Library/LaunchAgents/` plists

- **Owner:** you (service setup = `01-initial-setup.md`; the `homebrew.mxcl.*` ones came from Homebrew installs).
- **Consumers:** launchd (`RunAtLoad` + `KeepAlive` on each).
- **Purpose:** keep the service stack alive: LiteLLM proxy (:4000, reads `~/.config/litellm/config.yaml`), llama-server (:8080, pinned GGUF), oMLX (:8000), Redis, Postgres. Note: the litellm plist carries secrets as `EnvironmentVariables` (`OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`) — redact before sharing.

```xml
    <key>Label</key>
    <string>local.litellm.proxy</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/keith/.local/bin/litellm</string>
        <string>--config</string>
        <string>/Users/keith/.config/litellm/config.yaml</string>
        <string>--port</string>
        <string>4000</string>
    </array>
```

## Verification

Files exist and match the table above:

```bash
ls ~/.config/local-ai/registry.toml ~/.config/local-ai/modelman.toml ~/.config/agent-wt/config.toml ~/.config/litellm/config.yaml
```

```text
/Users/keith/.config/local-ai/registry.toml
/Users/keith/.config/local-ai/modelman.toml
/Users/keith/.config/agent-wt/config.toml
/Users/keith/.config/litellm/config.yaml
```

LaunchAgent labels are loaded:

```bash
launchctl list | grep -E 'litellm|llamacpp|omlx|redis|ollama'
```

```text
-	0	com.ollama.ollama
94146	0	local.litellm.proxy
94631	0	local.llamacpp.server
97297	0	homebrew.mxcl.omlx
88057	0	homebrew.mxcl.redis
```

(PIDs column will differ on your machine; a `-` with exit status means loaded but the app manages restarts itself.)

## Gotchas

- **Never hand-edit `registry.toml` to change what `wt` sees.** It is read-only to `wt`; change models through `modelman` (TUI, `expose`, `sync`).
- **Exposure state lives in two places by design:** the `litellm_exposed` flag in `~/.config/local-ai/modelman.toml`, and the generated `model_list` entry in `~/.config/litellm/config.yaml`. Edit neither by hand — use `modelman expose` / `modelman unexpose`.
- **`~/.config/agent-wt/config.toml` trap:** the `[[providers]]`/`[[models]]` blocks you see there are stale migration output that `wt` ignores. Only `default_tag` and `[[agents]]` are live; providers/models come from `registry.toml`.
- **Legacy files are migration inputs, not config:** `~/.config/local-ai/config.yaml`, `~/.config/local-ai/families/*.yaml`, and `~/.config/agent-wt/models.conf` are read only by `modelman migrate` / wt's first-run migration. Fix models in the new files, don't resurrect the old ones.
- **Secrets on disk:** OpenRouter `api_key` values in `~/.config/litellm/config.yaml`; `OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY` in the litellm LaunchAgent plist. Redact before pasting either into issues, docs, or chats.
- **The `modelman` binary on `PATH` lags the repo source:** `modelman --help` currently lists only `download`, while the source at `/Users/keith/github/ohanaverse/modelman` adds `migrate`, `sync`, `expose`, `unexpose`, `benchmark`, `usage`, and the TUI on bare `modelman`. Check `modelman --help` before scripting against it.
- **Files appear on first run of their owner:** `themes.toml` only after the first `wt config theme`, `rotation*.state` / `usage.jsonl` after the first `wt` launch, `~/.config/local-ai/benchmarks/` after `modelman benchmark run`. Don't create them by hand.
- Stray siblings are uninteresting: `config.toml.bak`, `*.plist.qwen3.8.bak`, `config.yaml.qwen3.8.bak` are manual backups; `usage.jsonl.lock` is wt's lock file.

## Going deeper

- Service setup (plists above): `/Users/keith/github/ohanaverse/local-ai-setup/docs/guides/01-initial-setup.md`
- modelman file semantics + CLI: `/Users/keith/github/ohanaverse/modelman/README.md`
- wt config/registry relationship: `/Users/keith/github/ohanaverse/agent-worktree/README.md`
- wt config TUI + config dir layout: `/Users/keith/github/ohanaverse/agent-worktree/docs/wt-config.md`
- Registry data model (wt consumer side): `/Users/keith/github/ohanaverse/agent-worktree/docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md`
- oMLX backend reference: `/Users/keith/github/ohanaverse/local-ai-setup/docs/reference/oMLX Download and Run.md`