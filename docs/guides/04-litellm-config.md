# LiteLLM config — `~/.config/litellm/config.yaml`, wt routes, admin UI

> Use this to: read and audit the LiteLLM proxy config, expose/unexpose models through :4000 with `wt litellm` (or `modelman expose`, which delegates to it), hand-edit the proxy-only sections without fighting the tool, and use the admin dashboard at :4000/ui.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- [01-initial-setup](01-initial-setup.md) complete: proxy running under the `local.litellm.proxy` LaunchAgent with `--config /Users/keith/.config/litellm/config.yaml --port 4000`, master key in the plist's `EnvironmentVariables`, Postgres + Redis up (health checks: guide 01 §6).
- `wt` on PATH (`make install` from the repo root) — it owns every write to `config.yaml`. modelman is runnable from its repo (not installed globally — run from `~/github/ohanaverse/local-ai-setup/modelman` with `uv run modelman …`) and requires `wt` on PATH. Exposure context (providers, registry): [02-providers-and-models](02-providers-and-models.md).
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

<!-- UNVERIFIED — not run as one block: expose/unexpose mutate the live /Users/keith/.config/litellm/config.yaml and /Users/keith/.config/local-ai/modelman.toml (this machine's 11 current entries all pre-date modelman bookkeeping — see Gotchas). The kickstart + confirm-curl portion was run live; see §5 and Verification for those outputs. -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman expose ollama/gemma4:12b-mlx   # gates in modelman, then `wt litellm expose`: wt writes/updates the model_list row in /Users/keith/.config/litellm/config.yaml and restarts the proxy
# (or, without modelman:  wt litellm expose ollama/gemma4:12b-mlx)

launchctl kickstart -k gui/$(id -u)/local.litellm.proxy && echo "kickstart OK"   # only if wt's automatic restart was skipped/failed: LiteLLM re-reads config.yaml only at start; down ~10–20 s (§5)

LITELLM_MASTER_KEY=$(awk '/<key>LITELLM_MASTER_KEY<\/key>/{getline; sub(/.*<string>/,""); sub(/<\/string>.*/,""); print}' ~/Library/LaunchAgents/local.litellm.proxy.plist)   # value never echoed
curl -s -H "Authorization: Bearer $LITELLM_MASTER_KEY" http://localhost:4000/v1/models \
  | python3 -m json.tool | grep '"id"' | grep gemma4

# to revert instead:
# from: ~/github/ohanaverse/local-ai-setup/modelman
# uv run modelman unexpose ollama/gemma4:12b-mlx    # removes the row (wt restarts the proxy)
```

```text
Exposed ollama/gemma4:12b-mlx through LiteLLM.
kickstart OK
        "id": "ollama/gemma4:12b-mlx",
```

## Steps

### 1. config.yaml anatomy

One file drives the proxy; the LaunchAgent starts `litellm` with it and the env block in the same plist carries the secrets (`LITELLM_MASTER_KEY`, `UI_USERNAME`, `UI_PASSWORD`, `OPENROUTER_API_KEY`, `DATABASE_URL`, `LITELLM_SALT_KEY` — 6 secret keys (plus `PATH`); **values never printed here**). **wt** writes this file (`wt litellm ...`; override the path with `WT_LITELLM_CONFIG`, legacy alias `MODELMAN_LITELLM_CONFIG`; default `/Users/keith/.config/litellm/config.yaml`); modelman only reads it (for `modelman usage`).

Shape — current file, redacted (`api_key` on the OpenRouter row is a **real key on disk**; shown as `sk-or-v1-…`):

```yaml
model_list:
  # ---- Ollama (local) ----                      # ← comment banners are hand-written; wt preserves comments (§1)
  - model_name: ollama/qwen3.8:27b-mlx            # = registry model id (also the client-facing id)
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx          # = provider prefix + model name
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true             # copied from the registry model by wt

  - model_name: openrouter/qwen/qwen3.8-27b
    litellm_params:
      model: openrouter/qwen/qwen3.8-27b
      api_key: sk-or-v1-…                         # REDACTED — the on-disk file carries the real key (see Gotchas)
      api_base: https://openrouter.ai/api/v1

general_settings:
  database_url: "postgresql://keith@localhost:5432/litellm"
  coordination_redis:
    host: localhost
    port: 6379
```

Field provenance (from `wt/internal/litellm/policy.go` — the provider policy table — and `entry.go`, the entry builder; the old Python table was removed 2026-09-21):

| Field | Comes from |
|---|---|
| `model_name` | registry model id (`modelman.toml`/`registry.toml` id, e.g. `ollama/qwen3.8:27b-mlx`) |
| `litellm_params.model` | provider prefix + model name — `ollama_chat/`, `openai/` (omlx), `openrouter/` |
| `api_base` | provider `auth.base_url` — `:11434` ollama, `:8000/v1` omlx, `https://openrouter.ai/api/v1` |
| `api_key` | ollama: omitted · omlx: literal `"not-needed"` · openrouter: `provider.auth.secret_ref` **verbatim** |
| `model_info` | copied from the registry model when present (e.g. `supports_function_calling`) |

`general_settings` (real block above) is the Postgres/Redis wiring: `database_url` points at the `litellm` database (trust auth, no password on the local socket — the plist additionally carries `DATABASE_URL` + `LITELLM_SALT_KEY` for the proxy process). There is **no** `master_key` in config.yaml on this machine — auth comes from the plist env `LITELLM_MASTER_KEY`.

What wt manages vs preserves (enforced in code, `wt/internal/litellm`):

- **Owns `model_list` plus a few launcher-required `litellm_settings`** — `expose`/`unexpose`/`sync` add/replace/remove rows keyed by `model_name` (replace-by-id, else append). wt value-enforces `litellm_settings.drop_params: true` and `litellm_settings.use_chat_completions_url_for_anthropic_messages: true`, and adds the per-row bridge params (`additional_drop_params` on `ollama_chat/*` rows, `use_chat_completions_api` on loopback `openai/*` rows) when missing.
- **Preserves everything else** — `general_settings` and unrecognized sections survive every write. Writes are atomic (unique temp file + rename), keep permission bits, and take a flock on `<config>.lock`.
- **Comments are preserved** (yaml.v3), with one cosmetic caveat: the first wt write normalizes list indentation and drops blank lines. A comment sitting next to a row wt removes or replaces can be dropped.

Count the live list:

```bash
python3 -c "import yaml;d=yaml.safe_load(open('/Users/keith/.config/litellm/config.yaml'));print('model_list entries:',len(d['model_list']))"
```

```text
model_list entries: 11
```

### 2. Expose / unexpose via wt (or modelman)

CLI (positional model id). `wt litellm` is the primary interface; `modelman expose|unexpose` applies modelman's own gates and then delegates to it (success lines from `src/modelman/main.py`):

<!-- UNVERIFIED — mutating commands; not run on this machine (they rewrite the live config.yaml, and modelman's also modelman.toml). Help text/success strings verified in source and in guide 02 §7 (identical command set); errors go to stderr with exit 1, e.g. `error: ...` for unknown ids, non-downloaded local models, or providers with no LiteLLM mapping. -->

```bash
wt litellm expose ollama/gemma4:12b-mlx          # wt checks the model is ready (cloud models exempt); --dry-run validates only
wt litellm unexpose ollama/gemma4:12b-mlx

# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman expose ollama/gemma4:12b-mlx     # model must be downloaded (cloud models exempt); also flips `exposed` in modelman.toml
uv run modelman unexpose ollama/gemma4:12b-mlx
```

```text
Exposed ollama/gemma4:12b-mlx through LiteLLM.
Unexposed ollama/gemma4:12b-mlx.
```

TUI — same toggle from the interactive UI (bare `uv run modelman`): press `x` on a model row; it queues the change and applies it on exit (downloading/pulling first if the model isn't ready yet); the EXPOSED column shows `Y` once the flag is set AND the model is ready (cloud models — `openrouter/*` or `location = "cloud"` rows — are exempt from the ready gate — see [02-providers-and-models](02-providers-and-models.md)).

Before/after, using the real files read-only. This illustrates the `expose` operation on `ollama/gpt-oss:20b`, a model that is in the registry and ready but currently has no LiteLLM row and `exposed = false`:

```bash
grep -n -A6 'model_name: ollama/gpt-oss:20b' /Users/keith/.config/litellm/config.yaml
grep -A4 '^\[model_state."ollama/gpt-oss:20b"\]' /Users/keith/.config/local-ai/modelman.toml
```

```text
# config.yaml: (no output — no existing LiteLLM row for this id)

[model_state."ollama/gpt-oss:20b"]
ready = true
disk_path = "ollama:gpt-oss:20b"
size_bytes = 13958643712
exposed = false
```

Expected after `uv run modelman expose ollama/gpt-oss:20b`:

<!-- UNVERIFIED — not run; row shape is deterministic from wt's provider policy table (`wt/internal/litellm/policy.go`) + the registry entry, and wt would append a new row because none exists. -->

```yaml
model_list:                                       # banners/comments gone — PyYAML round-trip
  - model_name: ollama/gpt-oss:20b                # appended because no matching row existed
    litellm_params:
      model: ollama_chat/gpt-oss:20b
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true
```

```toml
[model_state."ollama/gpt-oss:20b"]
exposed = true                                    # ← only field modelman flips; ready/disk_path/size_bytes untouched
```

This model is currently unexposed on this machine — running the command above would produce the "after" state.

Exposure and routing are independent: `expose`/`unexpose` decide which models exist on the proxy, while the `[litellm]` table in wt's `~/.config/agent-wt/config.toml` (`wt litellm on|off`, see [00-config-map](00-config-map.md)) only decides whether wt routes agents through it. Toggling `[litellm].enabled` therefore needs no re-exposing of models — and `wt litellm on|off|set` never touches the proxy service (no restart).

**wt** restarts LiteLLM after a route change (`wt litellm expose|unexpose|sync`, and the automatic route updates from `wt start`/`wt stop`) — it runs `WT_LITELLM_RESTART_CMD` (legacy alias `MODELMAN_LITELLM_RESTART_CMD`), falling back to the canonical `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`, and then waits up to 30 s for `/health/liveliness` when a LiteLLM URL is configured, so a new row is live right away (proxy down ~10–20 s; if the start fails, KeepAlive crash-loops it and `~/.litellm.err.log` is the tell). A failed restart is a non-fatal warning — restart manually per §5. For a genuinely new model (no existing row) `expose` appends instead of replacing; for an id whose provider has no policy it refuses with `provider '<id>' has no LiteLLM mapping`.

### 3. Hand-editing

When to hand-edit vs wt:

| Edit | Tool |
|---|---|
| `model_list` rows (expose a model) | wt — `wt litellm expose`/`unexpose`/`sync`, or `modelman expose`/`unexpose`/TUI `x` (which delegate and keep `modelman.toml` flags honest) |
| One-off tweak inside an existing `model_list` row | hand-edit (temporary: next `expose`/`unexpose` of that id replaces the row) — see Gotchas |
| `general_settings`, logging, router/callback settings, any non-`model_list` section | hand-edit — wt never rewrites these, so the edits survive indefinitely |
| `litellm_settings` keys **other than** `drop_params`/`use_chat_completions_url_for_anthropic_messages` | hand-edit — survives indefinitely, same as above |
| `litellm_settings.drop_params`, `litellm_settings.use_chat_completions_url_for_anthropic_messages` | wt — value-enforced to `true` on every write (`wt/internal/litellm`); a hand-set `false` is silently reverted on the next write |

Hand-edit flow — always back up, edit, validate, restart:

<!-- UNVERIFIED — $EDITOR is interactive (not drivable here). The cp and the yaml-check line's equivalent form ran fine live (§1 count uses the same safe_load on the same file); rerun as a block when editing. -->

```bash
cp /Users/keith/.config/litellm/config.yaml /Users/keith/.config/litellm/config.yaml.bak
$EDITOR /Users/keith/.config/litellm/config.yaml
python3 -c "import yaml; yaml.safe_load(open('/Users/keith/.config/litellm/config.yaml')); print('YAML OK')"
```

```text
YAML OK
```

(Exit 0 with `YAML OK`. On a syntax error: a `yaml.YAMLError` traceback and exit 1; fix before restarting or the proxy dies on start — the plist's `StandardErrorPath` `~/.litellm.err.log` shows why. wt likewise refuses to write a config it cannot parse and leaves the file untouched; an unreadable config makes `wt litellm ...` exit 1 with an error.)

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

Auth is live and the exposed model is in the served list:

```bash
LITELLM_MASTER_KEY=$(awk '/<key>LITELLM_MASTER_KEY<\/key>/{getline; sub(/.*<string>/,""); sub(/<\/string>.*/,""); print}' ~/Library/LaunchAgents/local.litellm.proxy.plist)
curl -s -H "Authorization: Bearer $LITELLM_MASTER_KEY" http://localhost:4000/v1/models | python3 -m json.tool | grep '"id"' | head -5
```

```text
            "id": "ollama/qwen3.8:27b-mlx",
            "id": "omlx/Qwen3.8-27B-4bit",
            "id": "openrouter/qwen/qwen3.8-27b",
            "id": "openrouter/qwen/qwen3.8-flash",
            "id": "openrouter/qwen/qwen3.8-2.4t-a95b",
```

(expected ≥1 id; 11 total here — `| grep -c '"id"'` confirms. Your list differs.)

Auth is actually enforced, and the row is on disk:

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:4000/v1/models
grep -c 'model_name: ollama/qwen3.8:27b-mlx' /Users/keith/.config/litellm/config.yaml
```

```text
401
1
```

(401 unauthenticated = master key required; `1` = exactly one `model_list` row for the id.)

### 6. `wt litellm` reference

wt owns all LiteLLM management. Commands:

| Command | What it does |
|---|---|
| `wt litellm expose <id>... [--json] [--dry-run] [--skip-ready-gate]` | Add `model_list` rows for registry models (restarts the proxy on change). `--dry-run` validates only; `--skip-ready-gate` is for callers that already verified readiness (modelman does) |
| `wt litellm unexpose <id>... [--json]` | Remove rows |
| `wt litellm sync [--json]` | Make local-model routes match the models actually running. Provider families whose live probe status is not `ok` are left alone, so it never removes routes it cannot verify |
| `wt litellm list [--json]` | Routed ids currently in `config.yaml` — **the authoritative answer to "is this local model routed?"** |
| `wt litellm providers [--json]` | Providers with a LiteLLM mapping (and whether each is cloud) |
| `wt litellm status [--json]` | Routing state: enabled, url, whether an api key is set (never printed) |
| `wt litellm on` / `off` | Route non-native models through the proxy / dial providers directly (policy only; the proxy is untouched) |
| `wt litellm set [--url U] [--api-key K]` | Update the proxy URL and/or key wt uses |

Exit code is 0 when every id applied, 1 when any id failed (the `--json` document is still printed) or the config could not be processed. `--json` shapes: change commands `{"outcomes":[{"id","action","error"}],"changed":bool,"warnings":[...]}`; `list` `{"routed":[...]}`; `providers` `{"providers":{"<id>":{"cloud":bool}}}`; `status` `{"enabled":bool,"url":"...","api_key_set":bool}`.

**Automatic routes.** `wt start`, `wt stop`, the TUI start flow, `wt smoke` and the stop picker update routes after a successful start/stop (progress stage "updating LiteLLM routes"): the started model is added (omlx and mtplx serve one model per process, so their sibling routes are removed), stopped models are removed, and a failed replacement removes the old occupant's route. LiteLLM problems only warn; a missing `config.yaml` is silent. Run `wt litellm sync` after starting or stopping a server outside wt.

**Routing state ownership.** The `[litellm]` table (`enabled`/`url`/`api_key`) lives in wt's `~/.config/agent-wt/config.toml` (0600 when a key is stored), copied once from modelman.toml's legacy `[litellm]` on first load. `modelman litellm status|on|off|set` pass through to these commands. config.toml writes are whole-file last-writer-wins: an open `wt config` editor session and `wt litellm on|off|set` overwrite each other (issue #143).

**Environment variables.** `WT_LITELLM_CONFIG` (legacy `MODELMAN_LITELLM_CONFIG`): config.yaml path. `WT_LITELLM_RESTART_CMD` (legacy `MODELMAN_LITELLM_RESTART_CMD`): restart command, else `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`.

**Troubleshooting a `400 Invalid model name`.** The proxy answered but does not have that model.
1. `wt litellm list` — is the id routed? If not: `wt litellm expose <id>` (cloud/native rules apply) or, for a local model that is running, `wt litellm sync`.
2. If it is listed, the proxy has a stale model list (it reads `config.yaml` only at start): restart it per §5 and check `~/.litellm.err.log`. wt's own restart may have failed — that surfaces as a warning at the time of the change.
3. Check routing is actually on: `wt litellm status`.
4. modelman's EXPOSED column for a *local* model reads modelman.toml's `exposed` flag, not `config.yaml`, so it can disagree with `wt litellm list` after `wt start`/`wt stop`/`sync`. Trust `wt litellm list`.

## Gotchas

- **wt manages `model_list` (plus a few enforced `litellm_settings`).** Hand-edits to a `model_list` row are silently replaced the next time you expose the same id (replace by `model_name`, else append). Hand-edits to `general_settings` and any other section survive every wt write.
- **Comments survive, whitespace changes.** wt edits the file with yaml.v3: comments (the `# ---- Ollama (local) ----` banners) are kept, but the first wt write normalizes list indentation and drops blank lines. Expect a one-time whitespace diff.
- **`config.yaml` carries literal api_key values on this machine.** The OpenRouter entries hold the real `sk-or-v1-…` key inline — **not** `os.environ/OPENROUTER_API_KEY` indirection. wt writes `provider.auth.secret_ref` verbatim into `api_key` (`wt/internal/litellm/entry.go`), so anything put in the registry surfaces in plaintext here. Treat `config.yaml` (and the plist) as secret material; redact before pasting anywhere.
- **modelman's bookkeeping drift (historical):** `modelman.toml` flags were out of sync because non-ollama entries were seeded outside modelman. Twenty-seven in-registry models (thirteen ollama + twelve openrouter + one omlx + one mtplx, issue #66) are now modelman-exposed; the remaining omlx rows, the hand-managed `openrouter/qwen/qwen3.8-*` set, and `ollama/q8`/`ollama/o35` remain hand-managed by design (the 2 llama.cpp rows were retired 2026-09-07 — see [provider-artifacts.md](../reference/provider-artifacts.md)). 
- **4000 is the proxy; backends live elsewhere.** `api_base` targets are oMLX `:8000`, ollama `:11434` — never `:4000` (that loops back into LiteLLM). Also: an omlx row in `model_list` doesn't mean that quant variant is loaded on the oMLX server — see guide 01 Gotchas (`modelman provider isolate`).
- **Syntax errors take the proxy down on restart.** Validate YAML before bouncing (§3 command); a dead start shows up as repeated respawns with errors in `~/.litellm.err.log`.
- **Postgres/Redis down ⇒ proxy fails to boot.** KeepAlive turns a dead dependency into a crash loop — respawns with connection errors in the plist's `StandardErrorPath` log (`~/.litellm.err.log`, per `~/Library/LaunchAgents/local.litellm.proxy.plist`). Pre-flight with guide 01 §6's `pg_isready -h localhost` and `redis-cli ping`.

## Going deeper

- Admin UI in depth — Postgres/Redis/Prisma setup, plist template, troubleshooting: [`../reference/litellm-admin-ui-setup.md`](../reference/litellm-admin-ui-setup.md)
- LiteLLM proxy deep-dive (prefixes, `ollama_chat/` vs `openai/`, security): [`../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md`](../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md)
- wt-owned LiteLLM management design (current): `docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md` (the original modelman exposure design, `modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`, is historical)
- Source of the writer/policies: `wt/internal/litellm/` (`policy.go`, `entry.go`, `configfile.go`, `restart.go`, `service.go`) and `wt/cmd/wt/litellm.go`
- Benchmarks through the proxy: [05-benchmarks](05-benchmarks.md)
- When :4000 misbehaves: [08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md)
