# Config file map

> Use this to: find which tool owns, writes, and reads each config file before you edit one.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

None — this is a reference doc, not a procedure.

## TL;DR

| File | Owner (writes) | Consumers | Purpose |
|---|---|---|---|
| `~/.config/local-ai/registry.toml` | `wt` (`wt model add\|edit\|rm` and the Models tab of `wt config`: models; `wt model init`: creates it, adds provider rows; `wt cloud-sync`: cloud prices and the ollama cloud entries), and you by hand for the few things wt has no command for | `wt` (source of the LiteLLM routes), `llmbench` (read-only) | Canonical providers + models |
| `~/.config/local-ai/modelman.toml`, `settings.yaml`, `config.yaml`, `families/` | nobody | nothing | Left by modelman; safe to remove ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §7) |
| `~/.config/local-ai/benchmarks/latest.toml` | `llmbench` | `llmbench` | Latest-run pointers behind `--latest`; the run directories sit beside it |
| `~/.config/litellm/config.yaml` | `wt` (`wt litellm ...`), you by hand | LiteLLM proxy | `model_list`, general settings |
| `~/.config/agent-wt/config.toml` | `wt` | `wt` | Agents, default rotation tag, and the `[litellm]` routing state (enabled/url/api_key). (NO providers/models — those live in registry.toml) |
| `~/.config/agent-wt/themes.toml` | `wt` (`wt config theme set`) | `wt` | Active theme |
| `~/.config/agent-wt/models.conf` | you by hand (pre-wt bash era) | `wt` first-run migration (read-only input) | Legacy bash rotation config |
| `~/.config/agent-wt/usage.jsonl` | `wt` | `wt stats` | Launch log |
| `~/.config/agent-wt/rotation.state` + `rotation-*.state` | `wt` | `wt` | Rotation position |
| `~/.config/agent-wt/profiles.toml` | you by hand; `wt profile on\|off` sets its `enabled` line | `wt` | Local-model launch profiles ([06-wt-agents-and-models](06-wt-agents-and-models.md) §9). Absent by default; `profile-backups/` beside it holds wt's snapshots of the files a profile rewrites |
| `~/.config/agent-wt/survey.jsonl` | `wt` (the post-session survey, which is switched off) | `wt stats`, the model picker | Recorded survey answers ([07-usage-and-spend](07-usage-and-spend.md)) |
| `~/.config/agent-wt/refcount.jsonl` | `wt` | `wt` | Which live wt sessions use which model; an entry lasts until its session's process is gone |
| `~/Library/LaunchAgents/local.litellm.proxy.plist` | you (setup = `01-initial-setup.md`) | launchd | LiteLLM proxy on :4000 |
| `~/Library/LaunchAgents/homebrew.mxcl.omlx.plist` | Homebrew (setup = `01-initial-setup.md`) — **optional**: wt's and llmbench's lifecycle backends run `omlx start` on demand; the 2026-09-30 rebuild omits it (`brew services start omlx` restores it) | launchd (when present) | oMLX server on :8000 |
| `~/Library/LaunchAgents/homebrew.mxcl.redis.plist` | Homebrew | launchd | Redis for LiteLLM coordination |
| `~/Library/LaunchAgents/homebrew.mxcl.postgresql@16.plist` | Homebrew | launchd | Postgres for LiteLLM (`localhost:5432/litellm`) |

Ollama has no LaunchAgent plist — it runs as the Ollama.app login item (`com.ollama.ollama` in `launchctl list`).

## The files

### `~/.config/local-ai/registry.toml`

- **Owner:** `wt`. `wt model add`, `edit` and `rm`, and the Models tab of `wt config` (`wt model`), add, change and remove models, each followed by a route sync; `wt model init` creates the file when it is missing and appends provider rows, never editing a row that exists; `wt cloud-sync` refreshes cloud prices and mirrors ollama's cloud catalog: it re-prices, adds and removes ollama cloud entries, and stamps `pricing_updated_at` on the models it priced. The procedure is in [02-providers-and-models](02-providers-and-models.md) Step 1, which also lists what stays a hand edit (a provider row wt has no default for, a `local_path` entry, `[[families]]`). wt takes a lock (`registry.toml.lock`, beside the file) and re-checks the file before replacing it.
- **Consumers:** `wt` (joins it in memory with `~/.config/agent-wt/config.toml` and builds the LiteLLM `model_list` entries from it, copying each model's `model_info`), `llmbench` (read-only).
- **Purpose:** canonical providers + models. `providers` may be empty (`providers = []`) when only discovered models are recorded; discovered entries carry `source = "discovered"` (entries registered from an on-disk artifact, on older rows — distinct from a *discovered model*, a local model on disk with no entry at all, which wt lists and routes anyway: [02-providers-and-models](02-providers-and-models.md) Step 3). See what yours holds with `grep -c '^\[\[models\]\]' ~/.config/local-ai/registry.toml` (model count) and `grep '^provider_id = ' ~/.config/local-ai/registry.toml | sort | uniq -c` (models per provider).
- **Env override:** `WT_REGISTRY` (legacy alias `MODELMAN_REGISTRY`, read after it). wt and llmbench both honor both.

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

### Left on disk, read by nothing

`~/.config/local-ai/modelman.toml`, `settings.yaml`, `config.yaml` and
`families/` were modelman's. modelman is retired and deleted; neither wt nor
llmbench opens any of them. `modelman.toml` holds an API key in its
`[litellm]` table. How to remove them, and what to check first:
[08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md),
"Files modelman left behind".

### `~/.config/litellm/config.yaml`

- **Owner:** `wt` (`wt litellm sync` — preview with `--dry-run` — plus the automatic route updates from `wt start`/`wt stop`/the lifecycle hook, and the launch-time check that writes the launched model's missing route — a running local model's, or a registry cloud model's), you by hand for rows wt does not own.
- **Hand-managed entries:** wt writes the routes it manages — every configured cloud model, plus local models while they run (an ollama model while it is pulled; issue #66), under the registry id or, for a model with no registry entry, its discovered id `<family>/<artifact>`; rows wt writes carry `model_info: {wt_managed: true}`. A `model_list` row without that marker whose name is not a registry id (e.g. a hand-written OpenRouter alias or a short-name alias for an Ollama model) is hand-managed and wt leaves it alone. `wt litellm list` prints the routes currently in the file, marking rows without wt's marker `(hand-written)` (that includes rows a pre-#179 wt wrote, until the first sync adopts them — [04-litellm-config](04-litellm-config.md) §2 *Upgrading from a pre-#179 wt*); `grep -c '^  - model_name:' ~/.config/litellm/config.yaml` counts every row, wt-written or not. The 3 hand-written omlx rows were removed 2026-09-30 (#168): an omlx route now exists only while its model runs, written by `wt start` under the registry id (or the discovered id, for an unregistered model). Don't hand-write rows under a registry id — `wt litellm sync` treats such a row as its own (adopts it while the model is desired, removes it when not); see [04-litellm-config](04-litellm-config.md) §2. (The 2 llama.cpp rows were retired 2026-09-07 — see [provider-artifacts.md](../reference/provider-artifacts.md).)
- **Consumers:** LiteLLM proxy (started by `~/Library/LaunchAgents/local.litellm.proxy.plist`, port 4000).
- **Purpose:** `model_list` (one entry per routed model: Ollama, oMLX, OpenRouter) plus `general_settings` (`database_url` → local Postgres, `coordination_redis` → local Redis). wt only touches `model_list` (plus a few launcher-required `litellm_settings` keys); `general_settings`, other sections and comments are preserved (the first wt write normalizes list indentation and drops blank lines).
- **Env override:** `WT_LITELLM_CONFIG` (legacy alias `MODELMAN_LITELLM_CONFIG`). The proxy restart command is `WT_LITELLM_RESTART_CMD` (legacy alias `MODELMAN_LITELLM_RESTART_CMD`). This path does **not** follow `WT_REGISTRY`, `MODELMAN_REGISTRY` or `XDG_CONFIG_HOME`: when any of them sends the registry somewhere else, wt refuses to write (or dry-run) routes until `WT_LITELLM_CONFIG` names the `config.yaml` that registry belongs to.
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
- **Purpose:** agents and default rotation tag (`default_tag = "code"`). NO live providers/models — those live in `registry.toml`; `wt` joins that file in memory, and nothing writes providers or models into this one: the Models tab of `wt config` and `wt model add|edit|rm` write `registry.toml`.
- **LiteLLM routing state lives here (wt-owned).** The `[litellm]` table (`enabled`/`url`/`api_key`; file is 0600 when a key is stored) is controlled with `wt litellm status|on|off|set --url ... --api-key ...`. wt reads this state from here and nowhere else. A legacy `[gateway]` block is dropped on wt's next save with a notice and is not imported — re-enter the values with `wt litellm set --url ... --api-key ...`. Writes to this file are a locked read-modify-write (`config.toml.lock`): the `wt config` editor saves only the agents and `default_tag`, and `wt litellm on|off|set` only the `[litellm]` table, so neither overwrites the other's change.
- On this machine the file still contains `[[providers]]`/`[[models]]` blocks: wt's one-time `models.conf` migration wrote them for a migration tool that no longer exists. `wt` never reads Providers/Models back out of `config.toml` — treat those blocks as inert.
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

- **Owner:** `wt` (`wt config theme set <name>` writes it; `wt config theme unset` removes the file). A bare `wt config theme` only prints the active and available themes.
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
- **Consumers:** `wt stats` (launch counts per model).
- **Purpose:** launch log. A sibling `usage.jsonl.lock` is wt's empty lock file.

Example (illustrative — your ids will differ):

```json
{"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.102105Z"}
{"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.133588Z"}
```

### `~/.config/agent-wt/rotation.state` (+ `rotation-*.state`)

- **Owner:** `wt` (single global rotation file; per-slot files legacy).
- **Consumers:** `wt` (rotation cursor).
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
ls ~/.config/local-ai/registry.toml ~/.config/agent-wt/config.toml ~/.config/litellm/config.yaml
```

```text
/Users/keith/.config/local-ai/registry.toml
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

- **Models change through `wt model`.** A model is added, edited or removed with `wt model add|edit|rm` or on the Models tab of `wt config` (`wt model`), and the routes follow — [02-providers-and-models](02-providers-and-models.md) Step 1. Downloads are the provider's own tool (guide 02 Step 5), and routes change through `wt litellm ...`.
- **Routing is derived, not stored.** There is no routing flag to edit (the per-model `exposed` flag went in #179): `wt` builds `model_list` from `registry.toml` plus live probes, so the `config.yaml` entry is the only copy that exists and `wt litellm list` is the way to read it. Don't hand-edit a managed row — change the registry (or start/stop the model) and let the sync that follows do the writing.
- **`~/.config/agent-wt/config.toml` trap:** the `[[providers]]`/`[[models]]` blocks you see there are stale migration output that `wt` ignores. Only `default_tag`, `[[agents]]` and the `[litellm]` table are live; providers/models come from `registry.toml`.
- **Legacy files are not config:** `~/.config/local-ai/config.yaml` and `~/.config/local-ai/families/*.yaml` are read by nothing (see "Left on disk, read by nothing" above), and `~/.config/agent-wt/models.conf` only by wt's first-run migration. Fix models in the new files, don't resurrect the old ones.
- **Secrets on disk:** OpenRouter `api_key` values in `~/.config/litellm/config.yaml`; `OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `UI_PASSWORD`, `DATABASE_URL` (the latter may embed the local Postgres password) in the litellm LaunchAgent plist; the LiteLLM API key in modelman's leftover `~/.config/local-ai/modelman.toml` ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md), "Files modelman left behind"). Redact before pasting any of them into issues, docs, or chats.
- **Files appear on first run of their owner:** `themes.toml` only after the first `wt config theme set <name>`, `rotation*.state` / `usage.jsonl` after the first `wt` launch, `~/.config/local-ai/benchmarks/` after `llmbench run`. Don't create them by hand.
- Stray siblings are uninteresting: `config.toml.bak`, `*.plist.qwen3.8.bak`, `config.yaml.qwen3.8.bak` are manual backups; `usage.jsonl.lock`, `survey.jsonl.lock`, `refcount.jsonl.lock` and `config.toml.lock` are wt's lock files.

## Going deeper

- Service setup (plists above): `/Users/keith/github/ohanaverse/local-ai-setup/docs/guides/01-initial-setup.md`
- wt config/registry relationship: `/Users/keith/github/ohanaverse/local-ai-setup/wt/README.md`
- wt config TUI + config dir layout: `/Users/keith/github/ohanaverse/local-ai-setup/wt/docs/wt-config.md`
- Registry data model (wt consumer side): `/Users/keith/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md`
- oMLX backend reference: `/Users/keith/github/ohanaverse/local-ai-setup/docs/reference/oMLX Download and Run.md`
- Provider artifacts + llama.cpp re-enable procedure: `/Users/keith/github/ohanaverse/local-ai-setup/docs/reference/provider-artifacts.md`
