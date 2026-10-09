# LiteLLM config — `~/.config/litellm/config.yaml`, wt routes, admin UI

> Use this to: read and audit the LiteLLM proxy config, understand how `wt litellm sync` decides which models are on :4000 (and preview it with `--dry-run`), hand-edit rows and the proxy-only sections without fighting the tool, and use the admin dashboard at :4000/ui.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

- [01-initial-setup](01-initial-setup.md) complete: proxy running under the `local.litellm.proxy` LaunchAgent with `--config /Users/keith/.config/litellm/config.yaml --port 4000`, master key in the plist's `EnvironmentVariables`, Postgres + Redis up (health checks: guide 01 §6).
- `wt` on PATH (`make install` from the repo root) — it owns every write to `config.yaml`. Registry context (adding providers and models — which is what puts a model on the proxy): [02-providers-and-models](02-providers-and-models.md).
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

There is no per-model "put this on the proxy" command. **wt derives the routes**: every cloud model `registry.toml` configures, plus each local model while it runs (an ollama model while it is pulled) — a local model needs no registry entry (#179 Phase B). So the workflow is: add a cloud model to the registry, or start a local one (guide 02), and the sync that follows writes its row. `wt model add|edit|rm` and the Models tab of `wt config` run that sync after their own write (guide 02 Step 1), `wt start`/`wt stop` update the routes themselves; run `wt litellm sync` yourself after a hand edit of `registry.toml`, or after starting a server outside wt when the client will not go through wt (a launch through wt writes a running model's missing route itself — §2).

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

To take a **cloud** model off the proxy, remove it from the registry — `wt model rm <id>`, or `d` on its row in the Models tab of `wt config` (guide 02 Step 1) — and the sync that follows drops its row. A **local** model is unrouted by stopping it (`wt stop <omlx model>` unroutes just that model; mtplx and mlx_lm_server unroute the whole provider; `wt stop omlx` and `wt stop --all` halt the omlx service and unroute every omlx model); a pulled ollama model stays routed until it is removed (`ollama rm`, then a sync). Deleting only a local model's registry entry does not unroute it while it is on disk and running — it is then routed under its discovered id (to unroute it, stop it — for ollama, `ollama rm` it — and sync). An `mlx_lm_server` pairing is the exception: it has no discovered id, so deleting its entry does unroute it at the next sync, running or not.

## Steps

### 1. config.yaml anatomy

One file drives the proxy; the LaunchAgent starts `litellm` with it and the env block in the same plist carries the secrets (`LITELLM_MASTER_KEY`, `UI_USERNAME`, `UI_PASSWORD`, `OPENROUTER_API_KEY`, `DATABASE_URL`, `LITELLM_SALT_KEY` — 6 secret keys (plus `PATH`); **values never printed here**). **wt** writes this file (`wt litellm sync` and the automatic route updates of `wt start`/`wt stop`; override the path with `WT_LITELLM_CONFIG`, legacy alias `MODELMAN_LITELLM_CONFIG`; default `/Users/keith/.config/litellm/config.yaml`).

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
- **Never removes or rewrites a hand-written row** — an unmarked row whose `model_name` is not a managed registry id keeps its content apart from the bridge params above (§2) and one more repair: **an ollama row with no `api_base` is given the registry's ollama address — `http://localhost:11434` when the registry names none —** on the next write, and the sync says so (`<id>: api_base set`). LiteLLM starts its own `ollama serve` at proxy startup for any ollama row without one; with Ollama already running, that second server shares the port, clients split between the two, and a model ends up loaded in both (Gotchas). A row that names an address of its own is never changed. wt also leaves a row alone when LiteLLM's own test catches it but the ollama address would be wrong for it (a model like `openai/ollama-proxy`) — it warns instead (Gotchas). That includes an unmarked row named like a *discovered* model's id: wt leaves the name to your row and writes none of its own.
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
- **Every running local model, registered or discovered.** "Running" comes from live probes of ollama, omlx, mtplx and mlx_lm_server — one probe per local provider family the registry has a local `[[providers]]` row (`location = "local"`) or a local model for. For **ollama it means pulled**: ollama loads a model on its first request, so a pulled ollama model is desired whether or not it is loaded right now (only while ollama's probe fully succeeds). A model a registry entry matches (same provider family and `model_name`) is routed under that entry's `id`; one with no entry is *discovered* and routed under `<family>/<artifact>` — `ollama/<name:tag>`, `omlx/<model directory name>`, `mtplx/<org>/<name>` — with a marked row like any other.
- **Never**: native models (`claude/native`, `copilot/native` — they need no route), models of a provider with no LiteLLM mapping, cloud models not in `registry.toml`, and an unregistered `mlx_lm_server` pairing (that family cannot be discovered, so it needs its entry).

**What one sync does** (the plan is made under the `config.yaml` lock):

| Plan bucket | Means | Output (`sync` / `sync --dry-run`) |
|---|---|---|
| add | a desired id has no row — wt appends one | `<id>: routed` / `<id>: would route` |
| adopt | a desired id has an **unmarked** row (written by hand or by a pre-#179 wt) — wt replaces it with a marked row | `<id>: adopted` / `<id>: would adopt` |
| rewrite | a desired id's marked row differs from what the registry now builds (a price, base URL or credential change) | `<id>: rewritten` / `<id>: would rewrite` |
| remove | a row wt owns whose id is no longer desired (cloud model deleted from the registry, a local model stopped (`wt stop <omlx model>` drops only that model; a service halt drops all its models), an ollama model no longer pulled, a discovered model's artifact gone) | `<id>: unrouted` / `<id>: would unroute` |
| repair | an ollama row the sync otherwise leaves in place — hand-written ones included — has no `api_base`; wt sets that one field (§1, Gotchas). A row with no `model_name` is named `(no model_name: <model>)` | `<id>: api_base set` / `<id>: would set api_base` |

A row that already matches is left alone, so a sync that changes nothing writes nothing and does not restart the proxy. When the file did change, wt restarts LiteLLM once (below).

**Hand edits across a rebuild.** When wt adopts or rewrites a row, it rebuilds `model`, `api_base` and `api_key` (in `litellm_params`) and all of `model_info` from the registry — a hand-edit to any of those is overwritten on the next sync. Every **other** `litellm_params` key on the old row (a `timeout`, an `rpm` limit, extra headers, …) is carried over onto the rebuilt row, unless the rebuilt row already sets that key; comments attached to the row ride along. Registry-level customization belongs in the registry entry instead: its `model_info` keys are merged into every row wt builds.

**Hand-written rows.** An unmarked row whose `model_name` is not a managed registry id is yours: no sync removes or rewrites it (wt still adds the bridge params above when missing, and fills an empty `api_base` on an ollama row). That is how to route something wt has no policy for, or an alias. Three cases cross over:

- An unmarked row **named like a managed registry id** is treated as wt's: adopted (marked, rebuilt as above) when the id is desired, removed when it is not — e.g. a hand-written row for a local model that is not running. `wt litellm list` still prints `(hand-written)` beside such a row until the sync that adopts or removes it, so check `wt litellm sync --dry-run` before relying on one. Don't hand-write rows for registry local models.
- A marked row whose id is **not** a managed registry id and is not a desired discovered model's id either (its registry model was deleted, its provider lost its mapping, or the discovered model stopped or left the disk) is removed by marker alone — and only the marked copy: an unmarked row of the same name stays.
- An unmarked row **named like a discovered model's id** (say an `ollama/<name:tag>` alias you wrote for a model you never registered) is *not* adopted: a discovered route never replaces a hand-written row. wt writes no row for that model — neither sync nor `wt start` — and yours keeps serving the name. Delete it if you want wt's marked row, then run `wt litellm sync`.

**When sync leaves routes alone.**

- A local family (ollama, omlx/omlx-6bit, mtplx, mlx_lm_server) whose probe ran and did not fully succeed is *untrusted*: every local route of the family — its registry models' rows and its discovered rows (any row named `<family>/…`) — is left exactly as it is, neither added, rewritten nor removed (the `api_base` repair above still applies: it does not depend on the probe), with a `warning: provider "<family>" probe did not succeed …` line (for a family with only discovered routes, the warning appears when `config.yaml` holds a wt-marked row of it). Models whose provider has no probe (retired llamacpp) are left alone silently.
- The exception is a server that **refused the connection**: nothing is listening, so its local routes are treated as stale and removed. That warns only when it matters — a local route of the family was in `config.yaml` (a registry model's, or a discovered model's wt-marked row), or the family serves registry cloud models (ollama `:cloud`), which stay routed but fail until the daemon is back.
- A family the registry references but cannot place — a provider row with no `location`, a provider row whose `location` is neither `local` nor `cloud` (a typo such as `Local`), or a model of the family with a data gap (next bullet) — and that therefore went unprobed is frozen the same way, with `warning: provider "<family>" could not be probed (<reason>) …` when `config.yaml` holds a wt-marked row of it — a discovered model's, or one of the family's own gap models'. The reason names what to repair: `its registry entry has no location`, `its registry entry has location "Local"; expected "local" or "cloud"`, `the registry has models for it but no provider entry`, or `model "<id>" has location "Local"; expected "local" or "cloud"` when the typo is on the model's own `location`. A family the registry does not reference at all (no local provider row, no local model) is not probed and **not** frozen: no probe could ever vouch for it, so its leftover wt-marked routes are removed rather than kept forever.
- A registry model with a data gap (a `provider_id` naming no `[[providers]]` entry, no location on model or provider, or a location that is neither `local` nor `cloud`) keeps its existing row until the registry is repaired.
- A desired row that cannot be built — most often a `secret_ref` that resolves empty (`secret_ref "…" for provider "openrouter" resolved empty (variable unset in this shell?)`), or an empty `model_name` — is reported per id on stderr and **its existing row is kept**; the rest of the sync still applies, and the command exits 1.
- A `config.yaml` whose `model_list` is present but is not a list is refused before anything is planned or written — the real sync and `--dry-run` exit 1 with `LiteLLM config is invalid: model_list is not a list in …`, and the `wt start`/`wt stop` route updates skip the write with that error as a warning (the start or stop itself still succeeds). A file with no `model_list`, or a bare `model_list:` (null), is fine. A file that is not valid YAML is refused the same way (`wt litellm ...` exits 1 and leaves it untouched).

**Preview first: `--dry-run`.** It prints the plan the real sync would carry out, plus the same warnings — probe warnings and any row LiteLLM would start its own `ollama serve` for (Gotchas) — and writes nothing. It lists an ollama row whose empty `api_base` the write would fill as `<id>: would set api_base`. Two differences from a real run: it does not report the `litellm_settings`/bridge-param fixes a write would make, and it does not re-check removals against a fresh probe (the real sync re-verifies local removals under the lock, so a model that finished starting in between keeps its route and a `would unroute` line is not a commitment). Either way the plan is only as fresh as the probes it was built from, so a model that starts or stops between the two runs can still move the other way. Example (illustrative — your ids will differ):

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

- **`wt cloud-sync`** ends with one sync when a price or the set of models changed, or it pulled or removed an ollama tag, and with none when it only re-stamped prices that were already current (a sync can restart the proxy). Its sync lines are prefixed `routes:`, and a failed one is a `routes: warning:`, never a failed run — [wt/docs/wt-cloud-sync.md](../../wt/docs/wt-cloud-sync.md).
- **`wt start` / `wt stop`** (and the TUI start flow, `wt smoke`, the stop picker) write targeted route updates — the started model's row, under its registry id or its discovered id, marked like any other (§6 *Automatic routes*).
- **A launch through wt** writes one route at most: handing a running local model to an agent through LiteLLM (a `-M` pin, rotation, the picker), `wt smoke` on a running target and `wt start <id>` on a running model write that model's route if it is missing, and print `wt: LiteLLM route for <id> updated`. A registry cloud model launched through LiteLLM (or smoke-tested) gets the same repair when `config.yaml` has no row for it; a cloud row that is there is left exactly as it is — keeping it current is `wt litellm sync`'s job. It never removes a route — a stale one is left to the next start, stop or `wt litellm sync` — and never replaces a hand-written row whose name is not a registry id (one named like a registry id is adopted, as a sync does — see Gotchas).
- **By hand**: after hand-editing `registry.toml`, or after starting or stopping a server outside wt. A local model something else started has no route until a sync runs or wt launches it, so a client that reaches the proxy without wt gets `400 Invalid model name` ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §2 Step 10).

**Upgrading from a pre-#179 wt.** Rows an older wt wrote carry no marker, so until the first post-upgrade sync `wt litellm list` prints every one of them as `(hand-written)`. That first sync runs without a preview on the first `wt start`, `wt stop` or launch through LiteLLM. It routes **every** registry cloud model — including ones you never routed before — adopts each unmarked row named after a registry id (rebuilding `model`/`api_base`/`api_key`/`model_info`, carrying other `litellm_params` per the rule above), and removes unmarked rows named like a registry local model that is not running. Scripts that call `wt litellm expose|unexpose` now exit 1 with a pointer to `wt litellm sync`. So, right after upgrading wt and before the first `wt start` or `wt stop`:

```bash
cp ~/.config/litellm/config.yaml ~/.config/litellm/config.yaml.pre-179
wt litellm sync --dry-run   # read every "would adopt" and "would unroute" line before letting a real sync run
```

If a `would unroute` line names a row you want to keep, rename that row so its `model_name` is not a registry id (it then stays hand-written), or fix the registry; then run `wt litellm sync`.

**Upgrading to discovered local models (#179 Phase B).** Four things change:

- **Registry local models that are not on disk lose their picker row.** wt lists a local model only when a probe finds it on disk or running; pinning a missing one (`-M`, `wt start`, `wt smoke`) says `<id> is not on disk — pull or download it first`. Download it with the provider's own tool (guide 02 Step 5); `wt model list` still shows it, with STATUS `missing`.
- **The first sync adds routes.** It adds a marked route for every pulled ollama model that has no registry entry, and for every running omlx/mtplx model that has none — each under its discovered id `<family>/<artifact>`. The change is additive: the same `wt litellm sync --dry-run` lists these rows as `<id>: would route` (under `plan.add` with `--json`), nothing you wrote by hand is replaced (an unmarked row of the same name keeps the name), and a discovered route goes away on its own when the artifact does (`ollama rm`, then a sync) or the model stops. As above, the first `wt start`, `wt stop` or launch through LiteLLM triggers that sync, so run the dry run first if you want to read the list.
- **`wt start <artifact>` does not register the model or ask for a family.** The model runs without a registry entry: in wt it is a `new` row with no family or tags, which `-T`/`-F` hide and rotation never picks. To give it a family and tags, register it with `wt model add <provider> <name> --family <family>`, or `enter` on its row in the Models tab of `wt config`; either keeps its discovered id (guide 02 Steps 1 and 3).
- **A model started outside wt has no route until a sync runs or wt launches it** — [08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §2 Step 10.

**Reading the result: `wt litellm list`.** One routed id per line, read straight from `config.yaml`. A wt row prints as its bare id (so `wt litellm list | grep -x '<id>'` works); a hand-written row prints as `<id><TAB>(hand-written)`. Illustrative:

```text
openrouter/qwen/qwen3.8-27b
ollama/qwen3.8:27b-mlx
my-alias	(hand-written)
```

No file records routing. The `config.yaml` row is the whole record.

On the proxy vs. **through** the proxy: `wt litellm sync` (and the automatic route updates) decide which models exist on :4000, while the `[litellm]` table in wt's `~/.config/agent-wt/config.toml` (`wt litellm on|off`, see [00-config-map](00-config-map.md)) decides whether wt routes *agents* through it. Toggling `[litellm].enabled` therefore needs no route change — and `wt litellm on|off|set` never touches the proxy service (no restart).

**wt** restarts LiteLLM after a route change (`wt litellm sync`, and the automatic route updates from `wt start`/`wt stop`) — it runs `WT_LITELLM_RESTART_CMD` (legacy alias `MODELMAN_LITELLM_RESTART_CMD`), falling back to the canonical `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`, and returns — the proxy is down for ~10–20 s, so a sync's new row may need a few seconds to go live. Only the *automatic* route updates (`wt start`, `wt stop`, `wt smoke`, the TUI start flow, the stop picker and the route check at launch) also wait up to 30 s for `/health/liveliness`, and only when a LiteLLM URL is configured and a 1.5 s pre-probe before the restart was not refused (proxy down ~10–20 s; if the start fails, KeepAlive crash-loops it and `~/.litellm.err.log` is the tell). A failed restart is a non-fatal warning — restart manually per §5.

### 3. Hand-editing

When to hand-edit vs wt:

| Edit | Tool |
|---|---|
| Which models are on the proxy | cloud: the registry (guide 02) + `wt litellm sync`; local: start or stop the model (ollama: pull or `ollama rm`, then a sync) — or no command at all, since `wt start`/`wt stop` keep the rows current |
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
| `wt litellm sync [--json] [--dry-run]` | Reconcile `model_list` with the registry and the live providers (§2): route every registry cloud model and every running local model (an ollama model when pulled), registered or discovered — a discovered one under `<family>/<artifact>` — adopt unmarked rows named like the desired registry ids, rewrite wt rows that drifted from the registry, remove rows wt owns that are no longer desired; a hand-written row (no marker, and a `model_name` that is not a registry id) is never removed or rewritten — it does still get the §1 bridge params, and an ollama row with no `api_base` gets one — and a discovered route never replaces one. A local family whose probe ran and did not fully succeed is left alone, discovered routes included (with a warning), and so are models whose provider has no probe (retired llamacpp; no warning); a server that *refuses the connection* has its local routes removed. Restarts the proxy only when the file changed. `--dry-run` prints the plan and the same warnings the real sync reports (`warnings`: the probe warnings plus any row LiteLLM would start its own `ollama serve` for) and writes nothing (it does not report `litellm_settings` fixes) |
| `wt litellm list [--json]` | Routed ids currently in `config.yaml`, one per line; a hand-written row is printed as `<id><TAB>(hand-written)` — **the authoritative answer to "is this model routed?"** |
| `wt litellm providers [--json]` | Providers with a LiteLLM mapping (and whether each is cloud) |
| `wt litellm status [--json]` | Routing state: enabled, url, whether an api key is set (never printed) |
| `wt litellm on` / `off` | Route non-native models through the proxy / dial providers directly (policy only; the proxy is untouched) |
| `wt litellm set [--url U] [--api-key K]` | Update the proxy URL and/or key wt uses |

`wt litellm expose` / `unexpose` were removed in #179; they remain only as hidden stubs that exit 1 and point to `wt litellm sync`.

Exit code for `sync` (and `sync --dry-run`) is 0 when every desired row could be built, 1 when any could not (the per-id errors and the `--json` document are still printed; the healthy rows still apply) or the config could not be processed. `--json` shapes, pinned by [`docs/contracts/litellm-cli.sample.json`](../contracts/litellm-cli.sample.json):

| Command | Shape |
|---|---|
| `sync --json` | `{"outcomes":[{"id","action"} or {"id","error"}],"changed":bool,"warnings":[...]}` — `action` is `routed`, `adopted`, `rewritten`, `unrouted` or `api_base set`; `warnings` holds probe and proxy-restart warnings, and one per row LiteLLM starts its own `ollama serve` for that wt cannot repair (Gotchas) |
| `sync --dry-run --json` | `{"dry_run":true,"plan":{"add":[...],"adopt":[...],"rewrite":[...],"remove":[...],"repair":[...],"errors":[{"id","error"}]},"warnings":[...]}` — `adopt` and `rewrite` are subsets of `add`; `repair` never names a row the same plan adopts, rewrites or removes |
| `list --json` | `{"routed":[...],"rows":[{"id","managed":bool}]}` — `managed` is the `wt_managed` marker |
| `providers --json` | `{"providers":{"<id>":{"cloud":bool}}}` |
| `status --json` | `{"enabled":bool,"url":"...","api_key_set":bool}` |

**Automatic routes.** `wt start`, `wt stop`, the TUI start flow, `wt smoke` and the stop picker update routes after a successful start/stop (progress stage "updating LiteLLM routes"): the started model's row is added, with the marker — under its registry id when a registry entry matches it, else under its discovered id `<family>/<artifact>` (skipped when a hand-written row already has that name). mtplx serves one model per process, so a start also removes every other route of that family, and a stop removes them all: every wt-marked row of the family, discovered ones included, plus the rows of the family's registry models — never a hand-written row under another name. A failed replacement removes the old occupant's route too. omlx is a pool: a start adds only the started model's route and removes the routes of the models omlx unloaded to make room (also when the load then fails), and a model stop removes only that model's route; `wt stop omlx` halts the service and removes every omlx route. wt has no lifecycle backend for `mlx_lm_server` at all (`wt/CLAUDE.md`, "Local-model resolution"), so that family's routes move only when a `wt litellm sync` runs after llmbench has started or stopped the pairing (`uv run --directory llmbench llmbench provider isolate --solo mlx_lm_server …` or `… provider stop mlx_lm_server`), or when wt launches it. **Stopping an ollama model only unloads it**: a pulled model is still served on request, so its route stays. LiteLLM problems only warn; a missing `config.yaml` is silent. These updates happen only when wt itself starts or stops the model. A local model that is *already* running gets a narrower check: when wt launches an agent on it through LiteLLM (a `-M` pin, rotation or the picker), and in `wt smoke` and `wt start <id>`, wt writes that model's route if it is missing and prints `wt: LiteLLM route for <id> updated` — only when that model's own row was written. Any write of `config.yaml` also fills an empty ollama `api_base` and restores wt's `litellm_settings`, so a launch can rewrite the file with the model's route untouched: it then prints the repair's own line, or `wt: LiteLLM config.yaml updated` when there is nothing more specific to say, and restarts the proxy as for any change. The check adds that one route and nothing else: it never starts or stops a model, never removes a route (a stale one is left to the next start, stop or `wt litellm sync`), never replaces a hand-written row whose name is not a registry id (one named like a registry id is adopted, as a sync does — see Gotchas), and writes nothing when the launch dials the provider directly. When the check writes a route it restarts the proxy, which briefly interrupts any other session going through it, as a start, stop or sync that changes `config.yaml` already does. Run `wt litellm sync` after stopping a server outside wt, or after starting one that a client will reach without wt.

**Routing state ownership.** The `[litellm]` table (`enabled`/`url`/`api_key`) lives in wt's `~/.config/agent-wt/config.toml` (0600 when a key is stored), copied once from modelman.toml's legacy `[litellm]` on first load. config.toml writes are whole-file last-writer-wins: an open `wt config` editor session and `wt litellm on|off|set` overwrite each other (issue #143).

**Environment variables.** `WT_LITELLM_CONFIG` (legacy `MODELMAN_LITELLM_CONFIG`): config.yaml path. It is required whenever the registry is redirected (`WT_REGISTRY`, its legacy alias `MODELMAN_REGISTRY`, or an `XDG_CONFIG_HOME` other than `~/.config`): the config.yaml path follows none of them, and syncing a redirected registry onto the default config.yaml would remove every route that registry lacks, so `wt litellm sync` (dry run included) and the start/stop/launch route updates refuse with `LiteLLM routes not touched: the registry is … but config.yaml is the default …`. Setting it to the default path is a valid answer. `WT_LITELLM_RESTART_CMD` (legacy `MODELMAN_LITELLM_RESTART_CMD`): restart command, else `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`. `WT_LITELLM_PLIST`: the proxy LaunchAgent plist wt reads to tell whether an `api_base: os.environ/VAR` row's variable is set for the proxy (the warning in the ollama `api_base` gotcha below); default `~/Library/LaunchAgents/local.litellm.proxy.plist`.

**Troubleshooting a `400 Invalid model name`.** The proxy answered but does not have that model.
1. `wt litellm list` — is the id routed? If not, `wt litellm sync --dry-run`: a per-id error names a row wt could not build (a `secret_ref` that resolves empty, an empty `model_name`). A `<id>: would route` line for a local model that is running means it was started outside wt and no sync has run since — `wt litellm sync` writes the row, and so does launching it through wt or `wt start <id>` (guide 08 §2 Step 10). If the id does not appear in the plan at all, it is not in the desired set — a cloud model not in `registry.toml`, a native model, a provider with no LiteLLM mapping (check `wt litellm providers`), a local model that is not running (for ollama: not pulled), or a local family whose probe failed (the `warning:` line says so). Fix that, then `wt litellm sync` (or start the local model).
2. If it is listed, the proxy has a stale model list (it reads `config.yaml` only at start): restart it per §5 and check `~/.litellm.err.log`. wt's own restart may have failed — that surfaces as a warning at the time of the change.
3. Check routing is actually on: `wt litellm status`.
4. wt's picker shows no routing column — routing is derived, so there is nothing stored to display. `wt litellm list` is the only answer to "is it routed?".

## Gotchas

- **wt owns the rows it marked (plus a few enforced `litellm_settings`).** On a wt row, hand-edits to `model`, `api_base`, `api_key` and `model_info` are replaced by the next sync — including the automatic one after a `wt start`/`wt stop` — while any other `litellm_params` key you add is carried across (§2). Hand-edits to `general_settings` and any other section survive every wt write.
- **Comments survive, whitespace changes.** wt edits the file with yaml.v3: comments (the `# ---- Ollama (local) ----` banners) are kept, but the first wt write normalizes list indentation and drops blank lines. Expect a one-time whitespace diff.
- **`config.yaml` carries literal api_key values.** OpenRouter entries hold the real `sk-or-v1-…` key inline — **not** `os.environ/OPENROUTER_API_KEY` indirection. wt resolves the provider's `auth.secret_ref` (env var, `exec:` helper, or literal) and writes the resulting key into `api_key` (`wt/internal/litellm/entry.go`), so the key surfaces in plaintext here. A `secret_ref` naming an env var that is unset in the shell running the sync is an error, not an empty key — the existing rows are kept. Treat `config.yaml` (and the plist) as secret material; redact before pasting anywhere.
- **Every ollama row needs an `api_base`, and wt fills in a missing one.** For each `model_list` row whose model is `ollama/…` or `ollama_chat/…` and has no `api_base`, LiteLLM runs `ollama serve` itself when the proxy starts. If Ollama is already running, the new server binds `127.0.0.1:11434` beside it, and from then on a client reaches one server or the other depending on whether `localhost` resolved to IPv4 or IPv6. A model gets loaded in both; `wt stop` and the exit prompt unload it in the one wt can see and report `done`, while `ollama ps` (which asks `127.0.0.1`) still shows it loaded. Check with `pgrep -fl "ollama serve"`: more than one line is the problem. wt sets a missing `api_base` to the registry's ollama address — `http://localhost:11434` when the registry names none — whenever it writes `config.yaml`, on hand-written rows too, and never changes one that is set — including one a row inherits through a YAML merge key (`<<: *defaults`). A sync lists each repaired row; a `wt start`, `wt stop` or launch whose write makes the repair prints `wt: LiteLLM route for <id>: api_base set`. This assumes Ollama is already running: with an `api_base` on every row, LiteLLM no longer starts it for you. **LiteLLM's own test is wider than wt's repair**: it starts `ollama serve` for any row whose model merely contains the word `ollama` and has no `api_base` — `openai/ollama-proxy`, say. wt does not fill those in, because the ollama address would be wrong for them; `wt litellm sync` (and `--dry-run`) instead prints `warning: row "<name>" (model <model>) has no api_base: …` on every run until you give the row one. **An `api_base: os.environ/VAR` row is checked, not repaired.** LiteLLM resolves the reference before its test, and an unset variable becomes `None` — the value it starts a server for. So sync also warns `row "<name>" … has api_base os.environ/VAR, and VAR is not set in …` when the variable is not set **for the proxy**. That is the proxy LaunchAgent's `EnvironmentVariables` (`~/Library/LaunchAgents/local.litellm.proxy.plist`; `WT_LITELLM_PLIST` overrides the path), not the shell you run wt in: a variable exported only in your shell does not reach a launchd-started proxy. wt falls back to its own environment only when it cannot ask the plist — `WT_LITELLM_RESTART_CMD` (or its legacy alias) is set, so something else starts the proxy, or the plist is missing or unreadable — and the warning says which it used. wt never rewrites such a row, since the variable may point at another server on purpose: set the variable in the plist, or write the address itself. (Variables given to launchd with `launchctl setenv` are not consulted.) A bare `os.environ/` with no name after it gets its own warning (LiteLLM reads it as no address at all). **A row written through a YAML alias is checked, not repaired, as well** (`litellm_params: *params`, `model: *model`, `api_base: *base`): wt reads through the alias to check it, and never fills a missing `api_base` there, because writing through an alias would change the row that defines the anchor. The row that defines the anchor is left alone too when the anchor is on an empty or null `api_base` itself (`api_base: &base ""`): replacing that value would remove the anchor the other rows refer to, and `config.yaml` would no longer parse. Give such a row an address by hand; a null one is in the sync's `has no api_base` warnings until you do.
- **A hand-written row is yours to keep — if its name is not a registry id.** An unmarked row under a `model_name` wt does not manage (a provider wt has no policy for, an alias, a retired backend) is never removed or rewritten, and `wt litellm list` prints it with `(hand-written)`; it stays until you delete it (retired llama.cpp rows: see [provider-artifacts.md](../reference/provider-artifacts.md)). An unmarked row named like a registry id is adopted or removed by the next sync (§2); one named like a discovered model's id is left alone, and wt writes no row of its own for that model.
- **The route name is the registry `id`; the upstream model is the registry `model_name`.** (A local model with no registry entry has one name for both: the route is `<family>/<artifact>`, the upstream model the artifact.) When the two disagree (a stale suffix on the id, a typo), the route table shows the mismatch as-is: hand-correcting the row's `litellm_params.model` no longer masks it, because sync rebuilds `model` from the registry. Fix the registry entry (guide 02 Step 3).
- **4000 is the proxy; backends live elsewhere.** `api_base` targets are oMLX `:8000`, ollama `:11434` — never `:4000` (that loops back into LiteLLM). Also: an omlx row in `model_list` doesn't mean that quant variant is loaded on the oMLX server — see guide 01 Gotchas (`llmbench provider isolate`).
- **Syntax errors take the proxy down on restart.** Validate YAML before bouncing (§3 command); a dead start shows up as repeated respawns with errors in `~/.litellm.err.log`.
- **Postgres/Redis down ⇒ proxy fails to boot.** KeepAlive turns a dead dependency into a crash loop — respawns with connection errors in the plist's `StandardErrorPath` log (`~/.litellm.err.log`, per `~/Library/LaunchAgents/local.litellm.proxy.plist`). Pre-flight with guide 01 §6's `pg_isready -h localhost` and `redis-cli ping`.

## Going deeper

- Admin UI in depth — Postgres/Redis/Prisma setup, plist template, troubleshooting: [`../reference/litellm-admin-ui-setup.md`](../reference/litellm-admin-ui-setup.md)
- LiteLLM proxy deep-dive (prefixes, `ollama_chat/` vs `openai/`, security): [`../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md`](../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md)
- Route ownership and reconciliation design (current, #179): `docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md`; wt-owned LiteLLM management: `docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md` (the original per-model expose design, `modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`, is historical — #179 removed that flag and its commands)
- Source of the writer/policies: `wt/internal/litellm/` (`policy.go`, `entry.go`, `configfile.go`, `restart.go`, `service.go`) and `wt/cmd/wt/litellm.go`
- Benchmarks through the proxy: [05-benchmarks](05-benchmarks.md)
- When :4000 misbehaves: [08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md)
