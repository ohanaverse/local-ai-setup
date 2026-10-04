# LiteLLM config — `~/.config/litellm/config.yaml`, wt routes, admin UI

> Use this to: read and audit the LiteLLM proxy config, understand how `wt litellm sync` decides which models are on :4000 (and preview it with `--dry-run`), hand-edit rows and the proxy-only sections without fighting the tool, and use the admin dashboard at :4000/ui.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- [01-initial-setup](01-initial-setup.md) complete: proxy running under the `local.litellm.proxy` LaunchAgent with `--config /Users/keith/.config/litellm/config.yaml --port 4000`, master key in the plist's `EnvironmentVariables`, Postgres + Redis up (health checks: guide 01 §6).
- `wt` on PATH (`make install` from the repo root) — it owns every write to `config.yaml`. modelman is runnable from its repo (not installed globally — run from `~/github/ohanaverse/local-ai-setup/modelman` with `uv run modelman …`) and requires `wt` on PATH. Registry context (adding providers and models — which is what puts a model on the proxy): [02-providers-and-models](02-providers-and-models.md).
- Live pre-flight — all three lines must match before relying on anything below:

```bash
launchctl list | grep local.litellm.proxy
curl -s -o /dev/null -w "api:%{http_code}\n" http://localhost:4000/v1/models
curl -s -o /dev/null -w "ui:%{http_code}\n" http://localhost:4000/ui
```

```text
40191	0	local.litellm.proxy
api:401
ui:307
```

(PID differs per boot. 401 = proxy up and demanding the master key. 307 = admin UI redirecting to login. The `launchctl list` middle column may read `0` or `-15` right after a `kickstart -k` (SIGTERM residue).)

## TL;DR

<!-- UNVERIFIED — not run as one block: a real `wt litellm sync` mutates the live /Users/keith/.config/litellm/config.yaml and restarts the proxy. `wt litellm sync --dry-run` and `wt litellm list` (both read-only) were run live 2026-10-03; the kickstart + confirm-curl portion was run live earlier, see §5 and Verification. -->

There is no per-model "put this on the proxy" command. **wt derives the routes**: every cloud model `registry.toml` configures, plus each local model while it runs (an ollama model while it is pulled) — a local model needs no registry entry (#179 Phase B). So the workflow is: add a cloud model to the registry, or start a local one (guide 02), then let `wt litellm sync` write its row. modelman runs that sync itself after its own changes (TUI exit, `start`/`stop`, `sync`, …); run it by hand after a hand-edit of `registry.toml` or a server started outside wt.

```bash
wt litellm sync --dry-run   # read-only: what a sync would add/adopt/rewrite/remove, plus its probe warnings
wt litellm sync             # write it: config.yaml is updated and the proxy restarted, only if something changed
wt litellm list             # what is routed now — hand-written rows are marked "(hand-written)"

launchctl kickstart -k gui/$(id -u)/local.litellm.proxy && echo "kickstart OK"   # only if wt's automatic restart failed: LiteLLM re-reads config.yaml only at start; down ~10–20 s (§5)

LITELLM_MASTER_KEY=$(awk '/<key>LITELLM_MASTER_KEY<\/key>/{getline; sub(/.*<string>/,""); sub(/<\/string>.*/,""); print}' ~/Library/LaunchAgents/local.litellm.proxy.plist)   # value never echoed
curl -s -H "Authorization: Bearer $LITELLM_MASTER_KEY" http://localhost:4000/v1/models \
  | python3 -m json.tool | grep '"id"' | grep '<model-id>'
```

Example output after adding one cloud model to the registry (illustrative — `openrouter/qwen/qwen3.8-27b` stands in for your id; `wt litellm list` prints every routed id, one per line):

```text
openrouter/qwen/qwen3.8-27b: would route
openrouter/qwen/qwen3.8-27b: routed
openrouter/qwen/qwen3.8-27b
kickstart OK
        "id": "openrouter/qwen/qwen3.8-27b",
```

To take a model off the proxy, remove it from the registry (TUI `d`, guide 02) — the sync that follows drops its row. For a local model on a single-model provider (omlx, mtplx, mlx_lm_server), stopping it does the same; a pulled ollama model stays routed until it is removed (`ollama rm`, then a sync).

## Steps

### 1. config.yaml anatomy

One file drives the proxy; the LaunchAgent starts `litellm` with it and the env block in the same plist carries the secrets (`LITELLM_MASTER_KEY`, `UI_USERNAME`, `UI_PASSWORD`, `OPENROUTER_API_KEY`, `DATABASE_URL`, `LITELLM_SALT_KEY` — 6 secret keys (plus `PATH`); **values never printed here**). **wt** writes this file (`wt litellm sync` and the automatic route updates of `wt start`/`wt stop`; override the path with `WT_LITELLM_CONFIG`, legacy alias `MODELMAN_LITELLM_CONFIG`; default `/Users/keith/.config/litellm/config.yaml`); modelman only reads it (for `modelman usage`).

Shape — example rows, redacted; your `model_list` holds whatever is routed (`wt litellm list` shows it). The `api_key` on an OpenRouter row is a **real key on disk**; shown as `sk-or-v1-…`:

```yaml
model_list:
  # ---- Ollama (local) ----                      # ← comment banners are hand-written; wt preserves comments (§1)
  - model_name: ollama/qwen3.8:27b-mlx            # = registry model id (also the client-facing id)
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx          # = provider prefix + registry model_name
      api_base: http://localhost:11434
      additional_drop_params: [reasoning_effort, frequency_penalty, presence_penalty]   # bridge param wt adds to ollama_chat/ rows
    model_info:
      input_cost_per_token: 0                     # pricing derived from the registry's [models.cost]
      output_cost_per_token: 0
      supports_function_calling: true             # copied from the registry model by wt
      wt_managed: true                            # ownership marker: wt wrote this row

  - model_name: openrouter/qwen/qwen3.8-27b
    litellm_params:
      model: openrouter/qwen/qwen3.8-27b
      api_base: https://openrouter.ai/api/v1
      api_key: sk-or-v1-…                         # REDACTED — the on-disk file carries the real key (see Gotchas)
    model_info:
      input_cost_per_token: 1.5e-07
      output_cost_per_token: 6.0e-07
      wt_managed: true

  - model_name: my-alias                          # no wt_managed marker, not a registry id → hand-written; wt never touches it
    litellm_params:
      model: openrouter/qwen/qwen3.8-27b
      api_key: sk-or-v1-…

general_settings:
  database_url: "postgresql://keith@localhost:5432/litellm"
  coordination_redis:
    host: localhost
    port: 6379
```

Field provenance (from `wt/internal/litellm/policy.go` — the provider policy table — and `entry.go`, the entry builder):

| Field | Comes from |
|---|---|
| `model_name` | registry model id (`registry.toml` `id`, e.g. `ollama/qwen3.8:27b-mlx`); for a local model with no registry entry, its discovered id `<family>/<artifact>` |
| `litellm_params.model` | provider prefix + registry `model_name` (the artifact name for a model with no registry entry) — `ollama_chat/` (ollama), `openai/` (omlx, omlx-6bit, mtplx, mlx_lm_server), `openrouter/` |
| `api_base` | provider `auth.base_url` — `:11434` ollama; the OpenAI-compatible local servers get `<origin>/v1` (e.g. `:8000/v1` omlx); `https://openrouter.ai/api/v1` |
| `api_key` | ollama: omitted · omlx/omlx-6bit/mtplx/mlx_lm_server: literal `"not-needed"` · openrouter: the provider's `auth.secret_ref`, **resolved** (an env-var name or `os.environ/NAME`, an `exec:` helper, or a literal key) |
| `model_info` | per-token pricing derived from the registry's `[models.cost]` (explicit `0` when a price is absent), then the registry model's own `model_info` keys, then `wt_managed: true` — always last, so a registry `model_info` cannot disown a wt row. A model with no registry entry gets the `0` prices and the marker only |

`general_settings` (block above) is the Postgres/Redis wiring: `database_url` points at the `litellm` database (trust auth, no password on the local socket — the plist additionally carries `DATABASE_URL` + `LITELLM_SALT_KEY` for the proxy process). There is **no** `master_key` in config.yaml on this machine — auth comes from the plist env `LITELLM_MASTER_KEY`.

What wt manages vs preserves (enforced in code, `wt/internal/litellm`):

- **Owns the rows it wrote, plus a few launcher-required `litellm_settings`.** Every row wt writes carries `model_info.wt_managed: true`. wt touches a row only when it owns it: it carries that marker, or its `model_name` is a registry id wt manages (a cloud or local model with a LiteLLM mapping — this clause adopts rows written before the marker existed). wt value-enforces `litellm_settings.drop_params: true` and `litellm_settings.use_chat_completions_url_for_anthropic_messages: true`, and adds the per-row bridge params (`additional_drop_params` on `ollama_chat/*` rows, `use_chat_completions_api` on loopback `openai/*` rows) when missing — on every row, hand-written ones included.
- **Never removes or rewrites a hand-written row** — an unmarked row whose `model_name` is not a managed registry id keeps its content apart from the bridge params above (§2). That includes an unmarked row named like a *discovered* model's id: wt leaves the name to your row and writes none of its own.
- **Preserves everything else** — `general_settings` and unrecognized sections survive every write. Writes are atomic (unique temp file + rename), keep permission bits, and take a flock on `<config>.lock`.
- **Comments are preserved** (yaml.v3), with one cosmetic caveat: the first wt write normalizes list indentation and drops blank lines. A comment attached to a row wt replaces moves to the new row; a comment next to a row wt removes can be dropped.

Count the live list:

```bash
python3 -c "import yaml;d=yaml.safe_load(open('/Users/keith/.config/litellm/config.yaml'));print('model_list entries:',len(d['model_list']))"
```

Output is one line, `model_list entries: <N>`, where N is however many rows `config.yaml` holds (wt's rows plus any hand-written ones — `wt litellm list` shows which is which).

### 2. How a model gets on the proxy — `wt litellm sync`

`wt litellm sync` reconciles `model_list` with the registry and the live providers. It takes no ids: the set it routes (the *desired* set) is derived, never stored.

**Desired set.**

- **Every registry cloud model** — location `cloud` on the model or its provider (OpenRouter models, ollama `:cloud` models) — whose provider is not native and has a LiteLLM mapping (`wt litellm providers`). There is no ready gate and no download: configured means routed.
- **Every running local model, registered or discovered.** "Running" comes from live probes of ollama, omlx, mtplx and mlx_lm_server — one probe per local provider family the registry has a `[[providers]]` row or a local model for. For **ollama it means pulled**: ollama loads a model on its first request, so a pulled ollama model is desired whether or not it is loaded right now (only while ollama's probe fully succeeds). A model a registry entry matches (same provider family and `model_name`) is routed under that entry's `id`; one with no entry is *discovered* and routed under `<family>/<artifact>` — `ollama/<name:tag>`, `omlx/<model directory name>`, `mtplx/<org>/<name>` — with a marked row like any other.
- **Never**: native models (`claude/native`, `copilot/native` — they need no route), models of a provider with no LiteLLM mapping, cloud models not in `registry.toml`, and an unregistered `mlx_lm_server` pairing (that family cannot be discovered, so it needs its entry).

**What one sync does** (the plan is made under the `config.yaml` lock):

| Plan bucket | Means | Output (`sync` / `sync --dry-run`) |
|---|---|---|
| add | a desired id has no row — wt appends one | `<id>: routed` / `<id>: would route` |
| adopt | a desired id has an **unmarked** row (written by hand or by a pre-#179 wt) — wt replaces it with a marked row | `<id>: adopted` / `<id>: would adopt` |
| rewrite | a desired id's marked row differs from what the registry now builds (a price, base URL or credential change) | `<id>: rewritten` / `<id>: would rewrite` |
| remove | a row wt owns whose id is no longer desired (cloud model deleted from the registry, a single-model local server stopped, an ollama model no longer pulled, a discovered model's artifact gone) | `<id>: unrouted` / `<id>: would unroute` |

A row that already matches is left alone, so a sync that changes nothing writes nothing and does not restart the proxy. When the file did change, wt restarts LiteLLM once (below).

**Hand edits across a rebuild.** When wt adopts or rewrites a row, it rebuilds `model`, `api_base` and `api_key` (in `litellm_params`) and all of `model_info` from the registry — a hand-edit to any of those is overwritten on the next sync. Every **other** `litellm_params` key on the old row (a `timeout`, an `rpm` limit, extra headers, …) is carried over onto the rebuilt row, unless the rebuilt row already sets that key; comments attached to the row ride along. Registry-level customization belongs in the registry entry instead: its `model_info` keys are merged into every row wt builds.

**Hand-written rows.** An unmarked row whose `model_name` is not a managed registry id is yours: no sync removes or rewrites it (wt still adds the bridge params above when missing). That is how to route something wt has no policy for, or an alias. Three cases cross over:

- An unmarked row **named like a managed registry id** is treated as wt's: adopted (marked, rebuilt as above) when the id is desired, removed when it is not — e.g. a hand-written row for a local model that is not running. `wt litellm list` still prints `(hand-written)` beside such a row until the sync that adopts or removes it, so check `wt litellm sync --dry-run` before relying on one. Don't hand-write rows for registry local models.
- A marked row whose id is **not** a managed registry id and is not a desired discovered model's id either (its registry model was deleted, its provider lost its mapping, or the discovered model stopped or left the disk) is removed by marker alone — and only the marked copy: an unmarked row of the same name stays.
- An unmarked row **named like a discovered model's id** (say an `ollama/<name:tag>` alias you wrote for a model you never registered) is *not* adopted: a discovered route never replaces a hand-written row. wt writes no row for that model — neither sync nor `wt start` — and yours keeps serving the name. Delete it if you want wt's marked row, then run `wt litellm sync`.

**When sync leaves routes alone.**

- A local family (ollama, omlx/omlx-6bit, mtplx, mlx_lm_server) whose probe ran and did not fully succeed is *untrusted*: every local route of the family — its registry models' rows and its discovered rows (any row named `<family>/…`) — is left exactly as it is, neither added, rewritten nor removed, with a `warning: provider "<family>" probe did not succeed …` line (for a family with only discovered routes, the warning appears when `config.yaml` holds a wt-marked row of it). Models whose provider has no probe (retired llamacpp) are left alone silently.
- The exception is a server that **refused the connection**: nothing is listening, so its local routes are treated as stale and removed. That warns only when it matters — a registry local route of the family was in `config.yaml`, or the family serves registry cloud models (ollama `:cloud`), which stay routed but fail until the daemon is back.
- A family the registry references but cannot place — a provider row with no `location`, or a model of the family with a data gap (next bullet) — and that therefore went unprobed is frozen the same way, with `warning: provider "<family>" could not be probed (its registry entry has no resolvable location) …`. A family the registry does not reference at all (no local provider row, no local model) is not probed and **not** frozen: no probe could ever vouch for it, so its leftover wt-marked routes are removed rather than kept forever.
- A registry model with a data gap (a `provider_id` naming no `[[providers]]` entry, or no location on model or provider) keeps its existing row until the registry is repaired.
- A desired row that cannot be built — most often a `secret_ref` that resolves empty (`secret_ref "…" for provider "openrouter" resolved empty (variable unset in this shell?)`), or an empty `model_name` — is reported per id on stderr and **its existing row is kept**; the rest of the sync still applies, and the command exits 1.
- A `config.yaml` whose `model_list` is present but is not a list is refused before anything is planned or written — the real sync and `--dry-run` exit 1 with `LiteLLM config is invalid: model_list is not a list in …`, and the `wt start`/`wt stop` route updates skip the write with that error as a warning (the start or stop itself still succeeds). A file with no `model_list`, or a bare `model_list:` (null), is fine. A file that is not valid YAML is refused the same way (`wt litellm ...` exits 1 and leaves it untouched).

**Preview first: `--dry-run`.** It prints the plan the real sync would carry out, plus the same probe warnings, and writes nothing. Two differences from a real run: it does not report the `litellm_settings`/bridge-param fixes a write would make, and it does not re-check removals against a fresh probe (the real sync re-verifies local removals under the lock, so a model that finished starting in between keeps its route and a `would unroute` line is not a commitment). Either way the plan is only as fresh as the probes it was built from, so a model that starts or stops between the two runs can still move the other way. Example (illustrative — your ids will differ):

```bash
wt litellm sync --dry-run
```

```text
openrouter/qwen/qwen3.8-27b: would route
ollama/qwen3.8:27b-mlx: would adopt
openrouter/qwen/qwen3.8-max: would rewrite
omlx/Ornith-1.5-35B-A3B-MLX-4bit: would unroute
warning: provider "mtplx" probe did not succeed (status "partial"); its model routes were left unchanged
```

(`would …` lines go to stdout; per-id errors and `warning:` lines to stderr. No output at all means config.yaml already matches.)

**Who runs sync.** You rarely need to:

- **modelman** runs one `wt litellm sync` after anything that can change routing — a TUI exit that changed `registry.toml` (the add/edit dialogs write it immediately), a queue applied on exit, `modelman sync`, `migrate`, `refresh-prices`, `ollama-catalog sync`, every `start`/`stop`, and a TUI mount that found a stale `running` flag. A failed sync is a warning, never a failed command; the next sync converges.
- **`wt start` / `wt stop`** (and the TUI start flow, `wt smoke`, the stop picker) write targeted route updates — the started model's row, under its registry id or its discovered id, marked like any other (§6 *Automatic routes*).
- **By hand**: after hand-editing `registry.toml`, or after starting or stopping a server outside wt and modelman. That last case is a known gap: a local model something else started is `run` in wt's picker and launches, but has no route until a sync runs — with LiteLLM routing on, the agent gets `400 Invalid model name` ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §2 Step 10).

**Upgrading from a pre-#179 wt.** Rows an older wt wrote carry no marker, so until the first post-upgrade sync `wt litellm list` prints every one of them as `(hand-written)`. That first sync runs without a preview as soon as any `modelman` command changes state (`start`/`stop`, `sync`, a TUI change, `refresh-prices`, …). It routes **every** registry cloud model — including ones you never routed before — adopts each unmarked row named after a registry id (rebuilding `model`/`api_base`/`api_key`/`model_info`, carrying other `litellm_params` per the rule above), and removes unmarked rows named like a registry local model that is not running. Scripts that call `wt litellm expose|unexpose` now exit 1 with a pointer to `wt litellm sync`. So, right after upgrading wt and before any modelman command:

```bash
cp ~/.config/litellm/config.yaml ~/.config/litellm/config.yaml.pre-179
wt litellm sync --dry-run   # read every "would adopt" and "would unroute" line before letting a real sync run
```

If a `would unroute` line names a row you want to keep, rename that row so its `model_name` is not a registry id (it then stays hand-written), or fix the registry; then run `wt litellm sync`.

**Upgrading to discovered local models (#179 Phase B).** The first sync after this upgrade also adds a marked route for every pulled ollama model that has no registry entry, and for every running omlx/mtplx model that has none — each under its discovered id `<family>/<artifact>`. The change is additive: the same `wt litellm sync --dry-run` lists these rows as `<id>: would route` (under `plan.add` with `--json`), nothing you wrote by hand is replaced (an unmarked row of the same name keeps the name), and a discovered route goes away on its own when the artifact does (`ollama rm`, then a sync) or the single-model server stops. As above, the first modelman command that changes state triggers that sync, so run the dry run first if you want to read the list.

**Reading the result: `wt litellm list`.** One routed id per line, read straight from `config.yaml`. A wt row prints as its bare id (so `wt litellm list | grep -x '<id>'` works); a hand-written row prints as `<id><TAB>(hand-written)`. Illustrative:

```text
openrouter/qwen/qwen3.8-27b
ollama/qwen3.8:27b-mlx
my-alias	(hand-written)
```

`modelman.toml` is untouched by all of this — no field there records routing. The `config.yaml` row is the whole record.

On the proxy vs. **through** the proxy: `wt litellm sync` (and the automatic route updates) decide which models exist on :4000, while the `[litellm]` table in wt's `~/.config/agent-wt/config.toml` (`wt litellm on|off`, see [00-config-map](00-config-map.md)) decides whether wt routes *agents* through it. Toggling `[litellm].enabled` therefore needs no route change — and `wt litellm on|off|set` never touches the proxy service (no restart).

**wt** restarts LiteLLM after a route change (`wt litellm sync`, and the automatic route updates from `wt start`/`wt stop`) — it runs `WT_LITELLM_RESTART_CMD` (legacy alias `MODELMAN_LITELLM_RESTART_CMD`), falling back to the canonical `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`, and returns — the proxy is down for ~10–20 s, so a sync's new row may need a few seconds to go live. Only the *automatic* route updates (`wt start`, `wt stop`, `wt smoke`, the TUI start flow and the stop picker) also wait up to 30 s for `/health/liveliness`, and only when a LiteLLM URL is configured and a 1.5 s pre-probe before the restart was not refused (proxy down ~10–20 s; if the start fails, KeepAlive crash-loops it and `~/.litellm.err.log` is the tell). A failed restart is a non-fatal warning — restart manually per §5.

### 3. Hand-editing

When to hand-edit vs wt:

| Edit | Tool |
|---|---|
| Which models are on the proxy | the registry (guide 02) + `wt litellm sync` — or no command at all, since modelman's own changes and `wt start`/`wt stop` keep the rows current |
| Price, base URL, credential or `model_info` of a wt row | the registry entry (`[models.cost]`, `[models.model_info]`, the provider's `auth`) — a hand-edit of these in `config.yaml` is overwritten by the next sync |
| Extra `litellm_params` on a wt row (`timeout`, rate limits, headers, …) | hand-edit — carried across every adopt/rewrite (§2) |
| A row wt has no registry entry for (an alias, an unmapped provider) | hand-edit, **without** `wt_managed` and under a `model_name` that is not a registry id — then wt never touches it |
| `general_settings`, logging, router/callback settings, any non-`model_list` section | hand-edit — wt never rewrites these, so the edits survive indefinitely |
| `litellm_settings` keys **other than** `drop_params`/`use_chat_completions_url_for_anthropic_messages` | hand-edit — survives indefinitely, same as above |
| `litellm_settings.drop_params`, `litellm_settings.use_chat_completions_url_for_anthropic_messages` | wt — value-enforced to `true` on every write (`wt/internal/litellm`); a hand-set `false` is silently reverted on the next write |

Hand-edit flow — always back up, edit, validate, then preview what wt would make of it, then restart:

<!-- UNVERIFIED — $EDITOR is interactive (not drivable here). The cp and the yaml-check line's equivalent form ran fine live (§1 count uses the same safe_load on the same file); rerun as a block when editing. -->

```bash
cp /Users/keith/.config/litellm/config.yaml /Users/keith/.config/litellm/config.yaml.bak
$EDITOR /Users/keith/.config/litellm/config.yaml
python3 -c "import yaml; yaml.safe_load(open('/Users/keith/.config/litellm/config.yaml')); print('YAML OK')"
wt litellm sync --dry-run   # would the next sync adopt, rewrite or remove what you just wrote?
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy && echo "kickstart OK"   # LiteLLM re-reads config.yaml only at start, and wt bounces it only when its own sync changed the file — so a row wt left alone needs this to take effect
```

```text
YAML OK
kickstart OK
```

(Exit 0 with `YAML OK`. On a syntax error: a `yaml.YAMLError` traceback and exit 1; fix before restarting or the proxy dies on start — the plist's `StandardErrorPath` `~/.litellm.err.log` shows why. wt likewise refuses to write a config it cannot parse and leaves the file untouched; an unreadable config makes `wt litellm ...` exit 1 with an error. If the dry run lists your edited id as `would adopt` / `would unroute`, its `model_name` collides with a registry id — rename the row or change the registry instead.)

Never hand-edit under time pressure without the YAML check — a broken config takes the whole :4000 endpoint down on restart (KeepAlive then respawns a crash loop; `~/.litellm.err.log` is the tell).

### 4. Admin UI

The proxy ships a dashboard at port 4000's `/ui` path — 307 to login (verified in the Prerequisites pre-flight):

- **URL:** `http://localhost:4000/ui`
- **Login:** `UI_USERNAME` / `UI_PASSWORD` from `~/Library/LaunchAgents/local.litellm.proxy.plist` (`EnvironmentVariables` — names verified live; change the values there, then §5 restart).
- **Admin API key:** the same plist's `LITELLM_MASTER_KEY` (must start `sk-`); it is the `Authorization: Bearer` value for API calls and grants full admin.
- **Requires Postgres** (SQLite is unsupported and hangs the proxy at startup) **+ Redis** — see `general_settings` + plist `DATABASE_URL`/`LITELLM_SALT_KEY`; first-time Prisma client + schema push is in the reference doc.

The dashboard shows: monthly usage/spend with per-model and per-provider breakdowns, endpoint activity (counts, success/failure, tokens), request-level logs with live tail, virtual-key management (keys with rate limits, budgets, model access), and per-customer/team spend.

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:4000/ui
```

```text
307
```

Full walk-through (Postgres install, Prisma generate/db push, Redis module warnings, plist template, troubleshooting): `docs/reference/litellm-admin-ui-setup.md`.

### 5. Restart via LaunchAgent

The proxy only re-reads `config.yaml` (and plist env) at start. Restart with the real label:

```bash
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy && echo "kickstart OK"
```

```text
kickstart OK
```

Measured recovery (live, 2026-08-29): old PID `40191` → new PID `65475`; the port refused connections and answered 401 again after **9 s**. Guide 01's measurement on the same label was ~15 s down, 401 by the 20 s mark. Plan for a ~10–20 s dead window and confirm rather than assume:

```bash
for i in $(seq 1 60); do CODE=$(curl -s -o /dev/null -m 1 -w "%{http_code}" http://localhost:4000/v1/models); [ "$CODE" = "401" ] && { echo "401 after ${i}s"; break; }; sleep 1; done
launchctl list | awk '/local.litellm.proxy/{print "new PID:", $1}'
```

```text
401 after 9s
new PID: 65475
```

(401, not 000/200: the proxy is up and once again demanding the master key. `KeepAlive=true` in the plist means launchd respawns it if it dies — use `kickstart -k`, not bare `stop`.) Whole-stack alternative with per-service health checks: `~/.local/bin/llm-restart` (see guide 01 §7).

## Verification

Auth is live and the routed model is in the served list:

```bash
LITELLM_MASTER_KEY=$(awk '/<key>LITELLM_MASTER_KEY<\/key>/{getline; sub(/.*<string>/,""); sub(/<\/string>.*/,""); print}' ~/Library/LaunchAgents/local.litellm.proxy.plist)
curl -s -H "Authorization: Bearer $LITELLM_MASTER_KEY" http://localhost:4000/v1/models | python3 -m json.tool | grep '"id"' | head -5
```

Example output — your ids will differ:

```text
            "id": "ollama/qwen3.8:27b-mlx",
            "id": "openrouter/qwen/qwen3.8-27b",
```

(Expect ≥1 `"id"` line, one per served model; `| grep -c '"id"'` instead of `| head -5` gives the total.)

Auth is actually enforced, and the row is on disk:

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:4000/v1/models
grep -c 'model_name: <model-id>' /Users/keith/.config/litellm/config.yaml   # <model-id> = a model you routed
```

```text
401
1
```

(401 unauthenticated = master key required; `1` = exactly one `model_list` row for that id — `0` means it is not routed. `wt litellm list | grep -x '<model-id>'` answers the same question for a wt row without grepping YAML.)

### 6. `wt litellm` reference

wt owns all LiteLLM management. Commands:

| Command | What it does |
|---|---|
| `wt litellm sync [--json] [--dry-run]` | Reconcile `model_list` with the registry and the live providers (§2): route every registry cloud model and every running local model (an ollama model when pulled), registered or discovered — a discovered one under `<family>/<artifact>` — adopt unmarked rows named like the desired registry ids, rewrite wt rows that drifted from the registry, remove rows wt owns that are no longer desired; a hand-written row (no marker, and a `model_name` that is not a registry id) is never removed or rewritten — it does still get the §1 bridge params — and a discovered route never replaces one. A local family whose probe ran and did not fully succeed is left alone, discovered routes included (with a warning), and so are models whose provider has no probe (retired llamacpp; no warning); a server that *refuses the connection* has its local routes removed. Restarts the proxy only when the file changed. `--dry-run` prints the plan and the probe warnings and writes nothing (it does not report `litellm_settings` fixes) |
| `wt litellm list [--json]` | Routed ids currently in `config.yaml`, one per line; a hand-written row is printed as `<id><TAB>(hand-written)` — **the authoritative answer to "is this model routed?"** |
| `wt litellm providers [--json]` | Providers with a LiteLLM mapping (and whether each is cloud) |
| `wt litellm status [--json]` | Routing state: enabled, url, whether an api key is set (never printed) |
| `wt litellm on` / `off` | Route non-native models through the proxy / dial providers directly (policy only; the proxy is untouched) |
| `wt litellm set [--url U] [--api-key K]` | Update the proxy URL and/or key wt uses |

`wt litellm expose` / `unexpose` were removed in #179; they remain only as hidden stubs that exit 1 and point to `wt litellm sync`.

Exit code for `sync` (and `sync --dry-run`) is 0 when every desired row could be built, 1 when any could not (the per-id errors and the `--json` document are still printed; the healthy rows still apply) or the config could not be processed. `--json` shapes, pinned by [`docs/contracts/litellm-cli.sample.json`](../contracts/litellm-cli.sample.json):

| Command | Shape |
|---|---|
| `sync --json` | `{"outcomes":[{"id","action"} or {"id","error"}],"changed":bool,"warnings":[...]}` — `action` is `routed`, `adopted`, `rewritten` or `unrouted`; `warnings` holds probe and proxy-restart warnings |
| `sync --dry-run --json` | `{"dry_run":true,"plan":{"add":[...],"adopt":[...],"rewrite":[...],"remove":[...],"errors":[{"id","error"}]},"warnings":[...]}` — `adopt` and `rewrite` are subsets of `add` |
| `list --json` | `{"routed":[...],"rows":[{"id","managed":bool}]}` — `managed` is the `wt_managed` marker |
| `providers --json` | `{"providers":{"<id>":{"cloud":bool}}}` |
| `status --json` | `{"enabled":bool,"url":"...","api_key_set":bool}` |

**Automatic routes.** `wt start`, `wt stop`, the TUI start flow, `wt smoke` and the stop picker update routes after a successful start/stop (progress stage "updating LiteLLM routes"): the started model's row is added, with the marker — under its registry id when a registry entry matches it, else under its discovered id `<family>/<artifact>` (skipped when a hand-written row already has that name). omlx and mtplx serve one model per process, so a start also removes every other route of that family, and a stop removes them all: every wt-marked row of the family, discovered ones included, plus the rows of the family's registry models — never a hand-written row under another name. A failed replacement removes the old occupant's route too. wt has no lifecycle backend for `mlx_lm_server` at all (`wt/CLAUDE.md`, "Local-model resolution"), so that family's routes move only when `modelman` starts or stops a pairing and its sync runs. **Stopping an ollama model only unloads it**: a pulled model is still served on request, so its route stays. LiteLLM problems only warn; a missing `config.yaml` is silent. These updates happen only when wt itself starts or stops the model: launching a model that is *already* running writes nothing, and `wt start <id>` on one only reports `already running`. Run `wt litellm sync` after starting or stopping a server outside wt.

**Routing state ownership.** The `[litellm]` table (`enabled`/`url`/`api_key`) lives in wt's `~/.config/agent-wt/config.toml` (0600 when a key is stored), copied once from modelman.toml's legacy `[litellm]` on first load. `modelman litellm status|on|off|set` pass through to these commands. config.toml writes are whole-file last-writer-wins: an open `wt config` editor session and `wt litellm on|off|set` overwrite each other (issue #143).

**Environment variables.** `WT_LITELLM_CONFIG` (legacy `MODELMAN_LITELLM_CONFIG`): config.yaml path. `WT_LITELLM_RESTART_CMD` (legacy `MODELMAN_LITELLM_RESTART_CMD`): restart command, else `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`.

**Troubleshooting a `400 Invalid model name`.** The proxy answered but does not have that model.
1. `wt litellm list` — is the id routed? If not, `wt litellm sync --dry-run`: a per-id error names a row wt could not build (a `secret_ref` that resolves empty, an empty `model_name`). A `<id>: would route` line for a local model that is running means it was started outside wt and no sync has run since — `wt litellm sync` writes the row (guide 08 §2 Step 10). If the id does not appear in the plan at all, it is not in the desired set — a cloud model not in `registry.toml`, a native model, a provider with no LiteLLM mapping (check `wt litellm providers`), a local model that is not running (for ollama: not pulled), or a local family whose probe failed (the `warning:` line says so). Fix that, then `wt litellm sync` (or start the local model).
2. If it is listed, the proxy has a stale model list (it reads `config.yaml` only at start): restart it per §5 and check `~/.litellm.err.log`. wt's own restart may have failed — that surfaces as a warning at the time of the change.
3. Check routing is actually on: `wt litellm status`.
4. Neither the modelman TUI nor wt's picker shows a routing column — routing is derived, so there is nothing in `modelman.toml` to display. `wt litellm list` is the only answer to "is it routed?".

## Gotchas

- **wt owns the rows it marked (plus a few enforced `litellm_settings`).** On a wt row, hand-edits to `model`, `api_base`, `api_key` and `model_info` are replaced by the next sync — including the automatic one after a `modelman start`/`stop` — while any other `litellm_params` key you add is carried across (§2). Hand-edits to `general_settings` and any other section survive every wt write.
- **Comments survive, whitespace changes.** wt edits the file with yaml.v3: comments (the `# ---- Ollama (local) ----` banners) are kept, but the first wt write normalizes list indentation and drops blank lines. Expect a one-time whitespace diff.
- **`config.yaml` carries literal api_key values.** OpenRouter entries hold the real `sk-or-v1-…` key inline — **not** `os.environ/OPENROUTER_API_KEY` indirection. wt resolves the provider's `auth.secret_ref` (env var, `exec:` helper, or literal) and writes the resulting key into `api_key` (`wt/internal/litellm/entry.go`), so the key surfaces in plaintext here. A `secret_ref` naming an env var that is unset in the shell running the sync is an error, not an empty key — the existing rows are kept. Treat `config.yaml` (and the plist) as secret material; redact before pasting anywhere.
- **A hand-written row is yours to keep — if its name is not a registry id.** An unmarked row under a `model_name` wt does not manage (a provider wt has no policy for, an alias, a retired backend) is never removed or rewritten, and `wt litellm list` prints it with `(hand-written)`; it stays until you delete it (retired llama.cpp rows: see [provider-artifacts.md](../reference/provider-artifacts.md)). An unmarked row named like a registry id is adopted or removed by the next sync (§2); one named like a discovered model's id is left alone, and wt writes no row of its own for that model.
- **The route name is the registry `id`; the upstream model is the registry `model_name`.** (A local model with no registry entry has one name for both: the route is `<family>/<artifact>`, the upstream model the artifact.) When the two disagree (a stale suffix on the id, a typo), the route table shows the mismatch as-is: hand-correcting the row's `litellm_params.model` no longer masks it, because sync rebuilds `model` from the registry. Fix the registry entry (guide 02 Step 3).
- **4000 is the proxy; backends live elsewhere.** `api_base` targets are oMLX `:8000`, ollama `:11434` — never `:4000` (that loops back into LiteLLM). Also: an omlx row in `model_list` doesn't mean that quant variant is loaded on the oMLX server — see guide 01 Gotchas (`modelman provider isolate`).
- **Syntax errors take the proxy down on restart.** Validate YAML before bouncing (§3 command); a dead start shows up as repeated respawns with errors in `~/.litellm.err.log`.
- **Postgres/Redis down ⇒ proxy fails to boot.** KeepAlive turns a dead dependency into a crash loop — respawns with connection errors in the plist's `StandardErrorPath` log (`~/.litellm.err.log`, per `~/Library/LaunchAgents/local.litellm.proxy.plist`). Pre-flight with guide 01 §6's `pg_isready -h localhost` and `redis-cli ping`.

## Going deeper

- Admin UI in depth — Postgres/Redis/Prisma setup, plist template, troubleshooting: [`../reference/litellm-admin-ui-setup.md`](../reference/litellm-admin-ui-setup.md)
- LiteLLM proxy deep-dive (prefixes, `ollama_chat/` vs `openai/`, security): [`../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md`](../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md)
- Route ownership and reconciliation design (current, #179): `docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md`; wt-owned LiteLLM management: `docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md` (the original per-model expose design, `modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`, is historical — #179 removed that flag and its commands)
- Source of the writer/policies: `wt/internal/litellm/` (`policy.go`, `entry.go`, `configfile.go`, `restart.go`, `service.go`) and `wt/cmd/wt/litellm.go`
- Benchmarks through the proxy: [05-benchmarks](05-benchmarks.md)
- When :4000 misbehaves: [08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md)
