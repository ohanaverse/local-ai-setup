# Config file map

> Use this to: find which tool owns, writes, and reads each config file before you edit one.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

None — this is a reference doc, not a procedure.

## TL;DR

| File | Owner (writes) | Consumers | Purpose |
|---|---|---|---|
| `~/.config/local-ai/registry.toml` | `modelman` (TUI add/edit, `modelman migrate`) | `wt` (read-only; source of the LiteLLM routes), `llmbench` (read-only) | Canonical providers + models |
| `~/.config/local-ai/modelman.toml` | `modelman` | `modelman` (writes), `wt` (read-only: the `price_refresh_last_run` date, and `[litellm]` only as a legacy fallback — no per-model key) | Per-machine state: downloads, running hints, family display names (its `[litellm]` table is a legacy read-only fallback) |
| `~/.config/local-ai/benchmarks/latest.toml` | `llmbench` | `llmbench` | Latest-run pointers behind `--latest`; the run directories sit beside it |
| `~/.config/local-ai/settings.yaml` | `modelman` | `modelman` | User preferences (theme) |
| `~/.config/local-ai/config.yaml` | you by hand (pre-modelman) | `modelman migrate` (read-only input) | Legacy provider types — superseded by `registry.toml` |
| `~/.config/local-ai/families/*.yaml` | pre-registry modelman tooling | `modelman migrate` (read-only input) | Legacy per-family variants + download markers |
| `~/.config/litellm/config.yaml` | `wt` (`wt litellm ...`; `modelman` asks wt to sync), you by hand | LiteLLM proxy | `model_list`, general settings |
| `~/.config/agent-wt/config.toml` | `wt` | `wt` | Agents, default rotation tag, and the `[litellm]` routing state (enabled/url/api_key). (NO providers/models — modelman owns those in registry.toml) |
| `~/.config/agent-wt/themes.toml` | `wt` (`wt config theme`) | `wt` | Active theme |
| `~/.config/agent-wt/models.conf` | you by hand (pre-wt bash era) | `wt` first-run migration (read-only input) | Legacy bash rotation config |
| `~/.config/agent-wt/usage.jsonl` | `wt` | `modelman usage` | Launch log |
| `~/.config/agent-wt/rotation.state` + `rotation-*.state` | `wt` | `wt`, `modelman usage` | Rotation position |
| `~/Library/LaunchAgents/local.litellm.proxy.plist` | you (setup = `01-initial-setup.md`) | launchd | LiteLLM proxy on :4000 |
| `~/Library/LaunchAgents/homebrew.mxcl.omlx.plist` | Homebrew (setup = `01-initial-setup.md`) — **optional**: wt/modelman lifecycle backends run `omlx start` on demand; the 2026-09-30 rebuild omits it (`brew services start omlx` restores it) | launchd (when present) | oMLX server on :8000 |
| `~/Library/LaunchAgents/homebrew.mxcl.redis.plist` | Homebrew | launchd | Redis for LiteLLM coordination |
| `~/Library/LaunchAgents/homebrew.mxcl.postgresql@16.plist` | Homebrew | launchd | Postgres for LiteLLM (`localhost:5432/litellm`) |

Ollama has no LaunchAgent plist — it runs as the Ollama.app login item (`com.ollama.ollama` in `launchctl list`).

## The files

### `~/.config/local-ai/registry.toml`

- **Owner:** `modelman` — TUI queue applies on exit, and `modelman migrate`.
- **Consumers:** `wt` (read-only; joins it in memory with `~/.config/agent-wt/config.toml` and builds the LiteLLM `model_list` entries from it, copying each model's `model_info`).
- **Purpose:** canonical providers + models. `providers` may be empty (`providers = []`) when only discovered models are recorded; discovered entries carry `source = "discovered"` (entries modelman registered from an on-disk artifact — distinct from a *discovered model*, a local model on disk with no entry at all, which wt lists and routes anyway: [02-providers-and-models](02-providers-and-models.md) Step 3). See what yours holds with `grep -c '^\[\[models\]\]' ~/.config/local-ai/registry.toml` (model count) and `grep '^provider_id = ' ~/.config/local-ai/registry.toml | sort | uniq -c` (models per provider).
- **Env override:** `WT_REGISTRY` (legacy alias `MODELMAN_REGISTRY`, read after it). wt, modelman and llmbench all honor both.

Example (illustrative — your ids will differ):

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

- **Owner:** `modelman` (written on every TUI apply, `sync`, and the local-model lifecycle; the `[litellm]` table is no longer written — `modelman litellm ...` passes through to wt).
- **Consumers:** `modelman`, plus `wt` (read-only — it reads the global `price_refresh_last_run` date, and the legacy `[litellm]` table as a fallback for routing state wt has not migrated; it reads **no** `[model_state]` key, not even `ready`: what is on disk and what is running come from wt's own live probes (#179 Phase B). That read-side contract is pinned by `docs/contracts/modelman.sample.toml`).
- **Purpose:** per-machine state: ready/downloaded status, disk path, size, the `running` hint (modelman's start/stop intent, confirmed by a live probe whenever `wt` or modelman needs the truth), a legacy `[litellm]` routing table (modelman round-trips it verbatim; wt owns the live copy), and family display names.
- **Env override:** `MODELMAN_STATE`.
- **Benchmark pointers moved out.** The `[benchmarks]` table (`last_run`, `last_run_dir`, `agent_last_run`, `eval_last_run`) is no longer written. `llmbench` keeps those four keys in `~/.config/local-ai/benchmarks/latest.toml` (override: `LLMBENCH_LATEST`) and reads the old table only while that file does not exist yet.
- **No exposure flag (#179).** `wt` derives the routes: every cloud model `registry.toml` configures, and a local model while it runs (for ollama, while it is pulled) — with or without a registry entry. A legacy pre-#179 `exposed`/`litellm_exposed` key is ignored by both tools — modelman drops it on the next save, so an older file self-cleans; never write one. The authoritative "is it routed" answer is `wt litellm list`. Routing decisions live in `wt/CLAUDE.md`'s "Local-model resolution" section — this guide does not repeat them.

Count your own entries with:

```bash
grep -c '^\[model_state\.' ~/.config/local-ai/modelman.toml   # model_state entries
grep -c '^running = true' ~/.config/local-ai/modelman.toml    # entries believed running
```

Each `[model_state."<id>"]` block has one of two shapes: a downloaded local model carries `ready = true` plus `disk_path`/`size_bytes`; a cloud model has no download, so it carries `ready = false` and is routed on its registry entry alone. The legacy `[litellm]` routing table, when present, sits at the top.

Example (illustrative — your ids, sizes and flags will differ):

```toml
[litellm]
enabled = true
url = "http://localhost:4000"
api_key = "sk-litellm-…"

[model_state."ollama/qwen3.8:27b-mlx"]
ready = true
disk_path = "ollama:qwen3.8:27b-mlx"
size_bytes = 19327352832
running = false

[model_state."ollama/kimi-k3:cloud"]
ready = false

[families.deepseek-v4]
display_name = "deepseek-v4"
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

Legacy example (illustrative — your families will differ):

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

- **Owner:** `wt` (`wt litellm sync` — preview with `--dry-run` — plus the automatic route updates from `wt start`/`wt stop`/the lifecycle hook, and the launch-time check that writes the launched model's missing route — a running local model's, or a registry cloud model's; `modelman` asks for a sync after its own state changes), you by hand for rows wt does not own. modelman never writes this file.
- **Hand-managed entries:** wt writes the routes it manages — every configured cloud model, plus local models while they run (an ollama model while it is pulled; issue #66), under the registry id or, for a model with no registry entry, its discovered id `<family>/<artifact>`; rows wt writes carry `model_info: {wt_managed: true}`. A `model_list` row without that marker whose name is not a registry id (e.g. a hand-written OpenRouter alias or a short-name alias for an Ollama model) is hand-managed and wt leaves it alone. `wt litellm list` prints the routes currently in the file, marking rows without wt's marker `(hand-written)` (that includes rows a pre-#179 wt wrote, until the first sync adopts them — [04-litellm-config](04-litellm-config.md) §2 *Upgrading from a pre-#179 wt*); `grep -c '^  - model_name:' ~/.config/litellm/config.yaml` counts every row, wt-written or not. The 3 hand-written omlx rows were removed 2026-09-30 (#168): an omlx route now exists only while its model runs, written by `wt start` under the registry id (or the discovered id, for an unregistered model). Don't hand-write rows under a registry id — `wt litellm sync` treats such a row as its own (adopts it while the model is desired, removes it when not); see [04-litellm-config](04-litellm-config.md) §2. (The 2 llama.cpp rows were retired 2026-09-07 — see [provider-artifacts.md](../reference/provider-artifacts.md).)
- **Consumers:** LiteLLM proxy (started by `~/Library/LaunchAgents/local.litellm.proxy.plist`, port 4000).
- **Purpose:** `model_list` (one entry per routed model: Ollama, oMLX, OpenRouter) plus `general_settings` (`database_url` → local Postgres, `coordination_redis` → local Redis). wt only touches `model_list` (plus a few launcher-required `litellm_settings` keys); `general_settings`, other sections and comments are preserved (the first wt write normalizes list indentation and drops blank lines).
- **Env override:** `WT_LITELLM_CONFIG` (legacy alias `MODELMAN_LITELLM_CONFIG`). The proxy restart command is `WT_LITELLM_RESTART_CMD` (legacy alias `MODELMAN_LITELLM_RESTART_CMD`). This path does **not** follow `WT_REGISTRY`, `MODELMAN_REGISTRY` or `XDG_CONFIG_HOME`: when either sends the registry somewhere else, wt refuses to write (or dry-run) routes until `WT_LITELLM_CONFIG` names the `config.yaml` that registry belongs to.
- **Spend database:** `wt stats` reads spend from the database named by `WT_LITELLM_DATABASE_URL` (legacy alias `MODELMAN_LITELLM_DATABASE_URL`), else by `general_settings.database_url` in this file, else by `DATABASE_URL`. An `os.environ/NAME` value in this file, and `DATABASE_URL`, are read from wt's environment and then from the proxy's LaunchAgent plist, so the proxy's own setup is enough. It only reads. See [07-usage-and-spend](07-usage-and-spend.md).
- OpenRouter entries contain real `api_key: sk-or-v1-…` values — redact before sharing this file.

Example (illustrative — your ids will differ; the second row is the shape `wt start` writes for an oMLX model while it runs):

```yaml
model_list:
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true
      wt_managed: true          # ownership marker on every row wt writes (pricing keys omitted here)

  - model_name: omlx/<registry-model-id>
    litellm_params:
      model: openai/<served-model-name>
      api_base: http://localhost:8000/v1
      api_key: not-needed
    model_info:
      wt_managed: true
```

### `~/.config/agent-wt/config.toml`

- **Owner:** `wt` (`wt config` editor; writes atomically on save).
- **Consumers:** `wt` only.
- **Purpose:** agents and default rotation tag (`default_tag = "code"`). NO live providers/models — modelman owns those in `registry.toml`; `wt` joins that file in memory and `wt config` never writes providers or models.
- **LiteLLM routing state lives here (wt-owned).** The `[litellm]` table (`enabled`/`url`/`api_key`; file is 0600 when a key is stored) is controlled with `wt litellm status|on|off|set --url ... --api-key ...` (`modelman litellm ...` passes through to the same commands). On the first wt load where this file already exists, wt copies modelman.toml's legacy `[litellm]` table in once; afterwards wt's copy wins and modelman.toml's is ignored. A legacy `[gateway]` block is dropped on wt's next save with a notice and is not imported — re-enter the values with `wt litellm set --url ... --api-key ...`. Writes to this file are whole-file last-writer-wins: an open `wt config` editor session and a `wt litellm on|off|set` will overwrite each other's changes (issue #143).
- On this machine the file still contains `[[providers]]`/`[[models]]` blocks: wt's one-time `models.conf` migration wrote them as an exchange format for `modelman migrate`. `wt` never reads Providers/Models back out of `config.toml` — treat those blocks as inert.
- **Env override:** `XDG_CONFIG_HOME` (config dir is `~/.config/agent-wt/` or `$XDG_CONFIG_HOME/agent-wt/`).
- **Env override:** `MODELMAN_WT_CONFIG` — used by `modelman migrate` to point at a non-default wt `config.toml` to import from.

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
- **Purpose:** active theme.

Example:

```toml
theme = "tokyo-night"
```

### `~/.config/agent-wt/models.conf` (legacy)

- **Owner:** you by hand, in the bash `ai-shell` era ("Managed by dotfiles").
- **Consumers:** wt's first-run migration (runs once if `config.toml` is missing; reads `CODE_MODELS`/`DESIGN_MODELS` arrays and provider base URLs).
- **Purpose:** legacy rotation + provider config. Superseded by `~/.config/agent-wt/config.toml` + `registry.toml`.

Legacy example (illustrative — your model ids will differ):

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

Example (illustrative — your ids will differ):

```json
{"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.102105Z"}
{"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.133588Z"}
```

### `~/.config/agent-wt/rotation.state` (+ `rotation-*.state`)

- **Owner:** `wt` (single global rotation file; per-slot files legacy).
- **Consumers:** `wt` (rotation cursor), `modelman usage` (last-launched model).
- **Purpose:** rotation position — the file body is just the model id of the last launch. Per-slot files such as `rotation-claude-code-_.state` / `rotation-pi-code-_.state`, if present, are legacy — wt deletes them after its one-time migration (source: `~/github/ohanaverse/local-ai-setup/wt/internal/rotation/rotation.go:138-175`).

Example (illustrative — your id will differ):

```
ollama/glm-5.3-flash:cloud
```

### `~/Library/LaunchAgents/` plists

- **Owner:** you (service setup = `01-initial-setup.md`; the `homebrew.mxcl.*` ones came from Homebrew installs).
- **Consumers:** launchd (`RunAtLoad` + `KeepAlive` on each).
- **Purpose:** keep the service stack alive: LiteLLM proxy (:4000, reads `~/.config/litellm/config.yaml`), oMLX (:8000), Redis, Postgres. Note: the litellm plist carries secrets as `EnvironmentVariables` (`OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `UI_PASSWORD`, `DATABASE_URL` — the latter may embed the local Postgres password) — redact before sharing.

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
launchctl list | grep -E 'litellm|omlx|redis|ollama|postgres'
```

```text
-	0	com.ollama.ollama
94146	0	local.litellm.proxy
97297	0	homebrew.mxcl.omlx
88057	0	homebrew.mxcl.redis
80374	0	homebrew.mxcl.postgresql@16
17810	0	application.com.electron.ollama.2312009772.2312009778.64DD861F-BA99-4B1C-A478-2478B317DA0D
```

(PIDs column will differ on your machine; a `-` with exit status means loaded but the app manages restarts itself.)

While the Ollama.app window is running, transient `application.com.electron.ollama.*` rows also appear in the output (PID in the first column; the long trailing suffix is a per-launch UUID).

## Gotchas

- **Never hand-edit `registry.toml` to change what `wt` sees.** It is read-only to `wt`; change models through `modelman` (TUI, `sync`) and routes through `wt litellm ...`.
- **Routing is derived, not stored.** There is no routing flag to edit (the per-model `exposed` flag went in #179): `wt` builds `model_list` from `registry.toml` plus live probes, so the `config.yaml` entry is the only copy that exists and `wt litellm list` is the way to read it. Don't hand-edit a managed row — change the registry (or start/stop the model) and let the sync that follows do the writing.
- **`~/.config/agent-wt/config.toml` trap:** the `[[providers]]`/`[[models]]` blocks you see there are stale migration output that `wt` ignores. Only `default_tag` and `[[agents]]` are live; providers/models come from `registry.toml`.
- **Legacy files are migration inputs, not config:** `~/.config/local-ai/config.yaml`, `~/.config/local-ai/families/*.yaml`, and `~/.config/agent-wt/models.conf` are read only by `modelman migrate` / wt's first-run migration. Fix models in the new files, don't resurrect the old ones.
- **Secrets on disk:** OpenRouter `api_key` values in `~/.config/litellm/config.yaml`; `OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `UI_PASSWORD`, `DATABASE_URL` (the latter may embed the local Postgres password) in the litellm LaunchAgent plist. Redact before pasting either into issues, docs, or chats.
- **Run modelman from the `modelman/` directory.** modelman is no longer installed as a global `uv tool`. Run it from `~/github/ohanaverse/local-ai-setup/modelman` with `uv run modelman …`; the repo root has no `pyproject.toml`, so `uv run modelman` from the root will fail.
- **Files appear on first run of their owner:** `themes.toml` only after the first `wt config theme`, `rotation*.state` / `usage.jsonl` after the first `wt` launch, `~/.config/local-ai/benchmarks/` after `llmbench run`. Don't create them by hand.
- Stray siblings are uninteresting: `config.toml.bak`, `*.plist.qwen3.8.bak`, `config.yaml.qwen3.8.bak` are manual backups; `usage.jsonl.lock` is wt's lock file.

## Going deeper

- Service setup (plists above): `/Users/keith/github/ohanaverse/local-ai-setup/docs/guides/01-initial-setup.md`
- modelman file semantics + CLI: `/Users/keith/github/ohanaverse/local-ai-setup/modelman/README.md`
- wt config/registry relationship: `/Users/keith/github/ohanaverse/local-ai-setup/wt/README.md`
- wt config TUI + config dir layout: `/Users/keith/github/ohanaverse/local-ai-setup/wt/docs/wt-config.md`
- Registry data model (wt consumer side): `/Users/keith/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md`
- oMLX backend reference: `/Users/keith/github/ohanaverse/local-ai-setup/docs/reference/oMLX Download and Run.md`
- Provider artifacts + llama.cpp re-enable procedure: `/Users/keith/github/ohanaverse/local-ai-setup/docs/reference/provider-artifacts.md`
