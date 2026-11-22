# Config file map

> Use this to: find which tool owns, writes, and reads each config file before you edit one.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

None — this is a reference doc, not a procedure.

## TL;DR

| File | Owner (writes) | Consumers | Purpose |
|---|---|---|---|
| `~/.config/local-ai/registry.toml` | `wt-model` and `wt` (`wt-model model add\|edit\|rm` and the Models tab of `wt-model model`: models; `wt-model model init`: creates it, adds provider rows; `wt-model cloud-sync`: cloud prices and the ollama cloud entries), and you by hand for the few things wt has no command for | `wt` (source of the LiteLLM routes), `llmbench` (read-only) | Canonical providers + models |
| `~/.config/local-ai/modelman.toml`, `settings.yaml`, `config.yaml`, `families/` | nobody | nothing | Left by modelman; safe to remove ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §7) |
| `~/.config/local-ai/benchmarks/latest.toml` | `llmbench` | `llmbench` | Latest-run pointers behind `--latest`; the run directories sit beside it |
| `~/.config/local-ai/tiers.toml` | `modeltiers` (`modeltiers run`) | `modeltiers show` and `explain`; wt's model picker (TIER column, read only) | Advisory capability tier per model, with the evidence ([12-model-tiers](12-model-tiers.md)) |
| `~/.cache/local-ai/modeltiers/` | `modeltiers` (`modeltiers run`) | `modeltiers run` (fallback, and `--offline`) | Snapshots of the four public sources' downloads; safe to delete |
| `~/.config/litellm/config.yaml` | `wt-model` (`wt-model litellm ...`, the route syncs of `wt-model model …`/`cloud-sync` and the model server's start/stop jobs), you by hand | LiteLLM proxy | `model_list`, general settings |
| `~/.config/wt-model/config.toml` | `wt-model`/`wt` (`wt-model litellm set` / `wt litellm set`, and `wt-model setup`, whose local plans set the pair), you by hand for `key_file` | `wt-model serve`, `wt-model litellm status` / `wt litellm status` | The LiteLLM proxy pair's `url` and `key_file` pointer (`[litellm]`; never the key itself) |
| `~/.config/wt-model/litellm.key` | `wt-model`/`wt` (`wt-model litellm set` / `wt litellm set`, and `wt-model setup`; wt never deletes it) | `wt-model serve` (the file `key_file` names) | The LiteLLM api key, mode 0600 (`litellm.key` when `key_file` is absent) |
| `~/.config/wt-model/server.token` | `wt-model setup` (local mode, created when absent), or you by hand; wt never deletes it | `wt-model serve --token-file`, and a session on the same machine (`token_file` under `[model-server]`) | The model server's bearer token, mode 0600 |
| `~/.config/wt-model/backups/` | `wt-model setup` (a LiteLLM master-key rotation; directory 0700); wt never deletes a backup | you, to restore the plist by hand | Dated copies `local.litellm.proxy.plist.bak-<date>-<time>[-N]` of the LiteLLM plist, mode 0600 or tighter; each holds the old master key, so remove it once the new key works |
| `~/.config/agent-wt/remote-model-server.token` | the `wt config` Mode tab (remote mode, a pasted token), or you by hand; wt never deletes it | a session on this machine (`token_file` under `[model-server]`) | A remote model server's bearer token, mode 0600 |
| `~/.config/agent-wt/config.toml` | `wt`; the `wt config` Mode tab or `wt-model setup` (or you by hand) for `[model-server]` | `wt` (sessions and host commands), `wt-model serve` (the `[litellm]` fallback) | Agents, default rotation tag, `[model-server]` (the model server a session uses: `mode`, `url`, `token_file`), the `[litellm]` url/api_key, the read-only fallback for the proxy pair above (`enabled` is retired and ignored) and the legacy `[launch]` table (`exit_prompt`, tolerated and ignored). (NO providers/models — those live in registry.toml) |
| `~/.config/agent-wt/themes.toml` | `wt` (`wt config theme set`) | `wt` | Active theme |
| `~/.config/agent-wt/models.conf` | you by hand (pre-wt bash era) | `wt` first-run migration (read-only input) | Legacy bash rotation config |
| `~/.config/agent-wt/usage.jsonl` | `wt` | `wt stats` | Launch log |
| `~/.config/agent-wt/rotation.state` + `rotation-*.state` | `wt` | `wt` | Rotation position |
| `~/.config/agent-wt/last-picked.state` | `wt` | `wt` | The row launched last, a model or a slot profile: the model picker's `> ` marker |
| `~/.config/agent-wt/slot-profiles.toml` | you by hand, or `wt profile slots add\|set\|rm` (backs up, validates) | `wt` | Slot profiles: named sets that map each of Claude Code's model slots to a registry model ([06-wt-agents-and-models](06-wt-agents-and-models.md) §10). Absent by default |
| `~/.config/agent-wt/profiles.toml` | you by hand; `wt profile on\|off` sets its `enabled` line | `wt` | Local-model launch profiles ([06-wt-agents-and-models](06-wt-agents-and-models.md) §9). Absent by default; `profile-backups/` beside it holds wt's snapshots of the files a profile rewrites |
| `~/.config/agent-wt/survey.jsonl` | `wt` (the post-session survey, which is switched off) | `wt stats`, the model picker | Recorded survey answers ([07-usage-and-spend](07-usage-and-spend.md)) |
| `~/Library/LaunchAgents/local.litellm.proxy.plist` | you, or wt (created when absent; one key rotated on request) — `wt-model setup`, [14-litellm-install-and-configuration](14-litellm-install-and-configuration.md) | launchd | LiteLLM proxy on :4000 |
| `~/Library/LaunchAgents/local.wt-model.serve.plist` | wt (`wt-model setup`, local mode; a plist without wt's `managed by wt` marker is yours and is never overwritten) | launchd | `wt-model serve` at login |
| `~/Library/LaunchAgents/sh.brew.omlx.plist` | Homebrew (setup = `01-initial-setup.md`) — **optional**: wt's and llmbench's lifecycle backends run `omlx start` on demand; the 2026-09-30 rebuild omits it (`brew services start omlx` restores it) | launchd (when present) | oMLX server on :8000 |
| `~/Library/LaunchAgents/sh.brew.redis.plist` | Homebrew | launchd | Redis for LiteLLM coordination |
| `~/Library/LaunchAgents/sh.brew.postgresql@16.plist` | Homebrew | launchd | Postgres for LiteLLM (`localhost:5432/litellm`) |

Ollama has no LaunchAgent plist — it runs as the Ollama.app login item (`com.ollama.ollama` in `launchctl list`).

## The files

### `~/.config/local-ai/registry.toml`

- **Owner:** `wt`. `wt-model model add`, `edit` and `rm`, and the Models tab of `wt-model model`, add, change and remove models, each followed by a route sync; `wt-model model init` creates the file when it is missing and appends provider rows, never editing a row that exists; `wt-model cloud-sync` refreshes cloud prices and mirrors ollama's cloud catalog: it re-prices, adds and removes ollama cloud entries, and stamps `pricing_updated_at` on the models it priced. The procedure is in [02-providers-and-models](02-providers-and-models.md) Step 1, which also lists what stays a hand edit (a provider row wt has no default for, a `local_path` entry, `[[families]]`). wt takes a lock (`registry.toml.lock`, beside the file) and re-checks the file before replacing it.
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

### `~/.config/local-ai/tiers.toml`

- **Owner:** `modeltiers`. `modeltiers run` replaces the whole file in one step (a temporary file beside it, then a rename); `--dry-run` leaves it alone. Never edit it by hand: the next run overwrites it. What a run decides is changed in the two committed data files, `modeltiers/src/modeltiers/data/aliases.toml` and `contracts.toml` ([12-model-tiers](12-model-tiers.md) steps 4 and 5).
- **Consumers:** `modeltiers show` and `modeltiers explain`, and wt's model picker, which reads it (same path rule, `MODELTIERS_OUT` first) each time it builds its table and shows each model's tier in its TIER column; a claude launch also reads a cloud model's `context` from it to set `CLAUDE_CODE_MAX_CONTEXT_TOKENS` ([12-model-tiers](12-model-tiers.md#see-tiers-in-the-wt-picker)). wt never writes it, and nothing routes or sorts on it.
- **Purpose:** one advisory tier (`marathon`, `deep`, `standard`, `quick` or `unrated`) for every model `wt-model model list --json` reports apart from the native agent rows, with the votes and scores behind it, plus the candidates: models a source scores that are not in the inventory. See yours with `uv run --directory modeltiers modeltiers show`.
- **Contract for readers:** a model absent from the file, or under `[[unmapped]]`, is `unrated`; a file that cannot be parsed is treated as absent. The format is [`docs/contracts/tiers.sample.toml`](../contracts/tiers.sample.toml).
- **Env override:** `MODELTIERS_OUT` (a file path), else `$XDG_CONFIG_HOME/local-ai/tiers.toml`. It does **not** follow `WT_REGISTRY`: when the registry wt read is not `registry.toml` beside the output file and `MODELTIERS_OUT` is unset, `modeltiers run` refuses and exits 1, so an inventory from a scratch registry cannot replace this file (`run --dry-run` writes nothing, so it warns and goes on).
- `modeltiers` reads the inventory only through `wt-model model list --json`. It never opens `registry.toml` and never writes it, `config.yaml` or any wt state.

Illustrative example from the test fixtures, not from any machine: the first two lines and the first `[[models]]` entry of [`docs/contracts/tiers.sample.toml`](../contracts/tiers.sample.toml), which `run` regenerates from `modeltiers/tests/fixtures/` and the sample inventory [`docs/contracts/model-list.sample.json`](../contracts/model-list.sample.json) — the test suite only pins it, it never rewrites the file (see `modeltiers/CLAUDE.md` for the regenerate procedure). Your models will differ.

```toml
generated_at = "2026-10-12T15:58:00Z"
contracts_version = "fixture-1"

[[models]]
provider_id = "ollama"
model_name = "glm-5.3:cloud"
registered = true
canonical = "zhipuai/glm-5.3"
resolved_by = "host"
tier = "standard"
code = "S"
confidence = "high"
basis = "measured"
open_weights = true
tool_call = true
context = 1048576
context_basis = "host"
card_kind = "huggingface"
card_url = "https://huggingface.co/zai-org/GLM-5.3"
aligned_score = 44.3
aligned_sources = [
    "intelligence_index",
    "arena_agent",
    "eci",
]
notes = []

[models.votes]
intelligence_index = "standard"
arena_agent = "standard"
eci = "standard"

[models.efforts]
arena_agent = "max"

[models.scores]
intelligence_index = 44.8
coding_index = 74.8
agentic_index = 53.1
arena_agent = 0.02147446416824893
arena_agent_ci_lower = 0.014306504009895325
eci = 155.61
```

### `~/.cache/local-ai/modeltiers/`

- **Owner:** `modeltiers run`. Each source has a directory (`modelsdev/`, `openrouter/`, `arena/`, `epoch/`) holding its downloads as `<UTC stamp>.json` or `.zip`, saved before they are parsed. A download that fails to parse is renamed `<UTC stamp>.bad.<ext>` and is never read again. After a successful write each source keeps its four newest good snapshots and its two newest bad ones.
- **Consumers:** `modeltiers run`: the newest good snapshot stands in when a source's download or parse fails (the source is marked `stale`, exit 2), and `--offline` reads nothing else.
- **Purpose:** a cache. Deleting it loses only the fallback; the next run downloads everything again.
- **Env override:** `MODELTIERS_CACHE`, else `$XDG_CACHE_HOME/local-ai/modeltiers/`.

### Left on disk, read by nothing

`~/.config/local-ai/modelman.toml`, `settings.yaml`, `config.yaml` and
`families/` were modelman's. modelman is retired and deleted; neither wt nor
llmbench opens any of them. `modelman.toml` holds an API key in its
`[litellm]` table. How to remove them, and what to check first:
[08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md),
"Files modelman left behind".

### `~/.config/litellm/config.yaml`

- **Owner:** `wt` (`wt-model litellm sync` — preview with `--dry-run` — plus the automatic route updates from `wt start`/`wt stop`/the lifecycle hook, and the launch-time check that writes the launched model's missing route — a running local model's, or a registry cloud model's), you by hand for rows wt does not own.
- **Hand-managed entries:** wt writes the routes it manages — every configured cloud model (in `config.yaml`), plus local models while they run (rows in LiteLLM's database when `wt-model` has the proxy pair and `store_model_in_db` is on, else in `config.yaml`; [04-litellm-config](04-litellm-config.md) §2 *Database routes*) (an ollama model while it is pulled; issue #66), under the registry id or, for a model with no registry entry, its discovered id `<family>/<artifact>`; rows wt writes carry `model_info: {wt_managed: true}`. A `model_list` row without that marker whose name is not a registry id (e.g. a hand-written OpenRouter alias or a short-name alias for an Ollama model) is hand-managed and wt leaves it alone. `wt-model litellm list` prints a table `ID  STORE  OWNER` of the routes in the file and, when the proxy answers, the wt routes in LiteLLM's database (STORE `file` or `db`), with OWNER `hand-written` for rows without wt's marker (that includes rows a pre-#179 wt wrote, until the first sync adopts them — [04-litellm-config](04-litellm-config.md) §2 *Upgrading from a pre-#179 wt*); `grep -c '^  - model_name:' ~/.config/litellm/config.yaml` counts every row, wt-written or not. The 3 hand-written omlx rows were removed 2026-09-30 (#168): an omlx route now exists only while its model runs, written by `wt start` under the registry id (or the discovered id, for an unregistered model). Don't hand-write rows under a registry id — `wt-model litellm sync` treats such a row as its own (adopts it while the model is desired, removes it when not); see [04-litellm-config](04-litellm-config.md) §2. (The 2 llama.cpp rows were retired 2026-09-07 — see [provider-artifacts.md](../reference/provider-artifacts.md).)
- **Consumers:** LiteLLM proxy (started by `~/Library/LaunchAgents/local.litellm.proxy.plist`, port 4000).
- **Purpose:** `model_list` (one entry per routed model: Ollama, oMLX, OpenRouter) plus `general_settings` (`database_url` → local Postgres, `coordination_redis` → local Redis). wt only touches `model_list` (plus a few launcher-required `litellm_settings` keys, and `general_settings.cancel_on_disconnect: true` when that key is absent — a hand-written value is kept); the rest of `general_settings`, other sections and comments are preserved (the first wt write normalizes list indentation and drops blank lines).
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

### `~/.config/wt-model/`

- **Owner:** `wt-model litellm set` and `wt litellm set` (the same writer; both create what they write when it is missing, and a no-op write changes nothing), `wt-model setup` (its local, create and rotate plans set the pair through the same writer), you by hand for `key_file`. `wt-model setup` also writes `server.token` (local mode) and, for a master-key rotation, the dated plist backups under `backups/` (0700).
- **Consumers:** `wt-model serve` (the pair the gateway forwards to LiteLLM with, read once at startup) and `wt-model litellm status` / `wt litellm status`.
- **Purpose:** the LiteLLM proxy pair. `config.toml` holds `[litellm]` `url` and `key_file` (a leading `~` is expanded; absent is `litellm.key`), and the api key itself is the 0600 file `key_file` names — never inside `config.toml`. A missing or empty key file is no key at all, `--api-key ""` clears the value without deleting the file, and wt never deletes either file. A `config.toml` with no `[litellm]` table falls back to the legacy `[litellm]` table of `~/.config/agent-wt/config.toml` (read-only), and such a run prints one note naming the writer. Full rules (the key-file refusals, the read-once rule, exit codes): [wt-model-server.md](../../wt/docs/wt-model-server.md), *Proxy url and key*.
- **Env override:** `XDG_CONFIG_HOME` (the home is `~/.config/wt-model/` or `$XDG_CONFIG_HOME/wt-model/`).

### `~/.config/agent-wt/config.toml`

- **Owner:** `wt` (`wt config` editor; writes atomically on save).
- **Consumers:** `wt` only.
- **Purpose:** agents and default rotation tag (`default_tag = "code"`). NO live providers/models — those live in `registry.toml`; `wt` joins that file in memory, and nothing writes providers or models into this one: the Models tab of `wt-model model` and `wt-model model add|edit|rm` write `registry.toml`.
- **`[launch]` (hand-edited, legacy).** `[launch] exit_prompt` is tolerated and ignored: nothing is offered when an agent exits (the exit prompt it once configured is removed — cleanup is user-driven, [06-wt-agents-and-models](06-wt-agents-and-models.md), TL;DR), no value is validated or consulted, and any value survives wt's saves. A non-empty value prints one note per process: `wt: note: config.toml: [launch] exit_prompt is no longer used and is ignored`. The `wt config` editor does not show the table.
- **`[model-server]` names the model server a session uses.** It is written by the Mode tab of `wt config` (native, remote, or local as a pointer with the default url and token file) or by `wt-model setup` (which sets up a local server), and stays hand-editable. `mode` (`native`, `local` or `remote`), `url` and `token_file` (a 0600 file holding the server's bearer token). A `url` (or `WT_MODEL_URL`) that contains an `@` is refused without being shown: credentials never go in the url, only in `token_file`. An inline `token` key is never used for auth: while it is set a save writes this file 0600 (and never loosens an existing stricter mode), and a command that needs a model refuses, naming the problem, until the token moves into a `token_file` (native mode ignores it, with one note asking you to remove it). `WT_MODEL_MODE`, `WT_MODEL_URL`, `WT_MODEL_TOKEN_FILE` and `WT_MODEL_TOKEN` override them. With no mode chosen (and no url or token), every command that needs a model (`-M`, `-T` or `-F`, `wt start`, a model's `wt stop`, `wt smoke`, …) refuses and starts nothing; a session never starts a server itself (`wt-model setup` sets up the LaunchAgent that does). The exception is a plain `wt -A <agent>` (and the agent picker) in native mode (`mode = "native"` in this table, or `WT_MODEL_MODE=native`): it launches the agent natively, with its own configuration. With no mode chosen it refuses, naming `wt config` and `WT_MODEL_MODE`, and a half-configured server still refuses. What needs the server and what does not, the exact refusal and the exit codes: [wt-session.md](../../wt/docs/wt-session.md).
- **The LiteLLM proxy's url and key live in the `~/.config/wt-model/` home (wt-owned host state), with the `[litellm]` table here (`url`/`api_key`) its read-only fallback.** The pair is set with `wt-model litellm set --url URL --api-key KEY` (the `wt litellm set` twin writes the same home) and shown with `wt litellm status`; `wt-model serve` reads it to reach the proxy (once, at startup — restart it after a set), and no session reads it. The fallback is consulted only when the home holds no `[litellm]` table, and nothing writes it again. A save writes this file 0600 while the table holds a key or `[model-server]` an inline `token`, and no save loosens an existing stricter mode. Its `enabled` key is retired: every session routes through the model server, so the key is ignored (one note per process when it is `true`), kept as written on every save, and `wt litellm on|off` are retired stubs that change nothing and exit 1. A legacy `[gateway]` block is dropped on wt's next save with a notice and is not imported — re-enter the values with `wt-model litellm set --url URL --api-key KEY`. Writes to this file are a locked read-modify-write (`config.toml.lock`): the `wt config` editor saves only the agents and `default_tag` (it leaves `[launch]`, `[litellm]` and `[model-server]` alone), and `wt-model litellm set` writes only the home, so neither overwrites the other's change.
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
- **Purpose:** rotation position — the file body is just the model id of the last single-model launch. A slot-profile launch does not write it. Per-slot files such as `rotation-claude-code-_.state` / `rotation-pi-code-_.state`, if present, are legacy — wt deletes them after its one-time migration (source: `~/github/ohanaverse/local-ai-setup/wt/internal/rotation/rotation.go`).

Example (illustrative — your id will differ):

```
ollama/glm-5.3-flash:cloud
```

### `~/.config/agent-wt/last-picked.state`

- **Owner:** `wt` (every launch that records: a model launch and a slot-profile launch).
- **Consumers:** `wt` (the model picker's `> ` marker, and the starting cursor when the last pick was a slot profile).
- **Purpose:** one line, the id of the row launched last. It is separate from `rotation.state` so that launching a slot profile moves the marker without moving the rotation position. When the file is missing, wt reads `rotation.state` instead, so state written before this file existed keeps working.

Example (illustrative — your id will differ):

```
profile/<name>
```

### `~/.config/agent-wt/slot-profiles.toml`

- **Owner:** you, by hand, or `wt profile slots add|set|rm`, the only wt commands that write it (each keeps a `slot-profiles.toml.bak-<stamp>` backup and validates first). Absent by default; a missing file means no profile rows.
- **Consumers:** `wt` (the claude model picker, `-M profile/<name>`, `wt profile list`, `wt profile show`).
- **Purpose:** slot profiles. Each `[[profile]]` names an agent, assigns a registry model id to some or all of the agent's model slots, and names the slot the session starts on.

Shape (the ids are placeholders — take real ones from `wt-model model list`):

```toml
[[profile]]
name    = "<name>"
agent   = "claude"
default = "sonnet"

[profile.slots]
opus   = "<registry id>"
sonnet = "<registry id>"
haiku  = "<registry id>"
```

See what wt makes of the file:

```bash
wt profile list                                # from: any dir, read-only
wt profile show -A claude -M profile/<name>    # from: any dir; probes local providers, changes nothing
```

Manage it with `wt profile slots add|set|rm|usage|candidates` (see [08 §8](08-maintenance-and-troubleshooting.md)).

Full reference: [wt/docs/wt-agents/slot-profiles.md](../../wt/docs/wt-agents/slot-profiles.md).

### `~/Library/LaunchAgents/` plists

- **Owner:** you (service setup = `01-initial-setup.md`; the `sh.brew.*` ones came from Homebrew installs), except two `wt-model setup` can write: `local.wt-model.serve.plist` (wt owns it) and `local.litellm.proxy.plist` (wt creates it only when absent and, on request, replaces its one `LITELLM_MASTER_KEY` value after a backup; it never touches the rest of that file).
- **Consumers:** launchd (`RunAtLoad` + `KeepAlive` on each).
- **Purpose:** keep the service stack alive: LiteLLM proxy (:4000, reads `~/.config/litellm/config.yaml`), oMLX (:8000), Redis, Postgres. Note: the litellm plist carries secrets as `EnvironmentVariables` (`OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `UI_PASSWORD`, `DATABASE_URL` — the latter may embed the local Postgres password) — redact before sharing.
- **Contents:** [14-litellm-install-and-configuration](14-litellm-install-and-configuration.md) §5 holds the annotated plist — the one source for it. This map says who owns the file, not what is inside it.

## Verification

Files exist and match the table above:

```bash
ls ~/.config/local-ai/registry.toml ~/.config/agent-wt/config.toml ~/.config/litellm/config.yaml
```

```text
/Users/user_name/.config/local-ai/registry.toml
/Users/user_name/.config/agent-wt/config.toml
/Users/user_name/.config/litellm/config.yaml
```

LaunchAgent labels are loaded:

```bash
launchctl list | grep -E 'litellm|omlx|redis|ollama|postgres'
```

```text
-	0	com.ollama.ollama
94146	0	local.litellm.proxy
97297	0	sh.brew.omlx
88057	0	sh.brew.redis
80374	0	sh.brew.postgresql@16
17810	0	application.com.electron.ollama.2312009772.2312009778.64DD861F-BA99-4B1C-A478-2478B317DA0D
```

(PIDs column will differ on your machine; a `-` with exit status means loaded but the app manages restarts itself.)

While the Ollama.app window is running, transient `application.com.electron.ollama.*` rows also appear in the output (PID in the first column; the long trailing suffix is a per-launch UUID).

## Gotchas

- **Models change through `wt-model model add|edit|rm` or `wt-model model`.** A model is added, edited or removed with `wt-model model add|edit|rm` or on the Models tab of `wt-model model`, and the routes follow — [02-providers-and-models](02-providers-and-models.md) Step 1. Downloads are the provider's own tool (guide 02 Step 5), and routes change through `wt-model litellm sync`.
- **Routing is derived, not stored.** There is no routing flag to edit (the per-model `exposed` flag went in #179): `wt` builds `model_list` from `registry.toml` plus live probes, so the `config.yaml` entry (or, for a local model with database routes on, the LiteLLM database row) is the only copy that exists and `wt-model litellm list` is the way to read it. Don't hand-edit a managed row — change the registry (or start/stop the model) and let the sync that follows do the writing.
- **`~/.config/agent-wt/config.toml` trap:** the `[[providers]]`/`[[models]]` blocks you see there are stale migration output that `wt` ignores. Only `default_tag`, `[[agents]]`, `[model-server]`, the `[litellm]` table (the read-only fallback pair; the pair itself lives in `~/.config/wt-model/`) and `[launch]` are live; providers/models come from `registry.toml`, which only the host commands (`wt-model model …`, `wt-model cloud-sync`, `wt-model litellm …`, `wt-model served|warm`, `wt-model stop <provider>|--all`, the Models tab) and `wt worktrees`' artifact protection read.
- **Legacy files are not config:** `~/.config/local-ai/config.yaml` and `~/.config/local-ai/families/*.yaml` are read by nothing (see "Left on disk, read by nothing" above), and `~/.config/agent-wt/models.conf` only by wt's first-run migration. Fix models in the new files, don't resurrect the old ones.
- **Secrets on disk:** OpenRouter `api_key` values in `~/.config/litellm/config.yaml`; `OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `UI_PASSWORD`, `DATABASE_URL` (the latter may embed the local Postgres password) in the litellm LaunchAgent plist; the LiteLLM API key in modelman's leftover `~/.config/local-ai/modelman.toml` ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md), "Files modelman left behind"), and the proxy's own api key in `~/.config/wt-model/litellm.key`. Redact before pasting any of them into issues, docs, or chats.
- **Files appear on first run of their owner:** `themes.toml` only after the first `wt config theme set <name>`, `rotation*.state` / `usage.jsonl` after the first `wt` launch, `~/.config/local-ai/benchmarks/` after `llmbench run`, `tiers.toml` and `~/.cache/local-ai/modeltiers/` after `modeltiers run`. Don't create them by hand.
- Stray siblings are uninteresting: `config.toml.bak`, `*.plist.qwen3.8.bak`, `config.yaml.qwen3.8.bak` are manual backups; `usage.jsonl.lock`, `survey.jsonl.lock`, `config.toml.lock` and `slot-profiles.toml.lock` are wt's lock files. An existing install may also still hold `refcount.jsonl` and `refcount.jsonl.lock` beside them (the in-use counting behind the old stop picker, replaced by the model server's client sessions); nothing reads or writes them any more and wt never deletes them — remove them by hand when you are ready.

## Going deeper

- Service setup (plists above): `~/github/ohanaverse/local-ai-setup/docs/guides/01-initial-setup.md`
- wt config/registry relationship: `~/github/ohanaverse/local-ai-setup/wt/README.md`
- wt config TUI + config dir layout: `~/github/ohanaverse/local-ai-setup/wt/docs/wt-config.md`
- Registry data model (wt consumer side): `~/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md`
- oMLX backend reference: `~/github/ohanaverse/local-ai-setup/docs/reference/oMLX Download and Run.md`
- Provider artifacts + llama.cpp re-enable procedure: `~/github/ohanaverse/local-ai-setup/docs/reference/provider-artifacts.md`
- Model tiers (`tiers.toml`, its two data files): [12-model-tiers](12-model-tiers.md)
