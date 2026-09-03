# LiteLLM config — `~/.config/litellm/config.yaml`, modelman exposure, admin UI

> Use this to: read and audit the LiteLLM proxy config, expose/unexpose models through :4000 with modelman, hand-edit the proxy-only sections without fighting the tool, and use the admin dashboard at :4000/ui.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- [01-initial-setup](01-initial-setup.md) complete: proxy running under the `local.litellm.proxy` LaunchAgent with `--config /Users/keith/.config/litellm/config.yaml --port 4000`, master key in the plist's `EnvironmentVariables`, Postgres + Redis up (health checks: guide 01 §6).
- modelman runnable from its repo (not installed globally — run from `~/github/ohanaverse/local-ai-setup/modelman` with `uv run modelman …`). Exposure context (providers, registry): [02-providers-and-models](02-providers-and-models.md).
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
uv run modelman expose ollama/gemma4:12b-mlx   # writes/updates the model_list row in /Users/keith/.config/litellm/config.yaml

launchctl kickstart -k gui/$(id -u)/local.litellm.proxy && echo "kickstart OK"   # LiteLLM re-reads config.yaml only at start; down ~10–20 s (§5)

LITELLM_MASTER_KEY=$(awk '/<key>LITELLM_MASTER_KEY<\/key>/{getline; sub(/.*<string>/,""); sub(/<\/string>.*/,""); print}' ~/Library/LaunchAgents/local.litellm.proxy.plist)   # value never echoed
curl -s -H "Authorization: Bearer $LITELLM_MASTER_KEY" http://localhost:4000/v1/models \
  | python3 -m json.tool | grep '"id"' | grep gemma4

# to revert instead:
# from: ~/github/ohanaverse/local-ai-setup/modelman
# uv run modelman unexpose ollama/gemma4:12b-mlx    # removes the row; restart to apply
```

```text
Exposed ollama/gemma4:12b-mlx through LiteLLM.
kickstart OK
        "id": "ollama/gemma4:12b-mlx",
```

## Steps

### 1. config.yaml anatomy

One file drives the proxy; the LaunchAgent starts `litellm` with it and the env block in the same plist carries the secrets (`LITELLM_MASTER_KEY`, `UI_USERNAME`, `UI_PASSWORD`, `OPENROUTER_API_KEY`, `DATABASE_URL`, `LITELLM_SALT_KEY` — 6 secret keys (plus `PATH`); **values never printed here**). modelman reads/writes the same file (override with `MODELMAN_LITELLM_CONFIG`, default `/Users/keith/.config/litellm/config.yaml`).

Shape — current file, redacted (`api_key` on the OpenRouter row is a **real key on disk**; shown as `sk-or-v1-…`):

```yaml
model_list:
  # ---- Ollama (local) ----                      # ← comment banners are hand-written; modelman strips them on rewrite (§2)
  - model_name: ollama/qwen3.8:27b-mlx            # = registry model id (also the client-facing id)
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx          # = provider prefix + model name
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true             # copied from the registry model by modelman

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

Field provenance (from `~/github/ohanaverse/local-ai-setup/modelman/src/modelman/litellm.py` — `PROVIDER_POLICIES` and the entry builder at ~line 110):

| Field | Comes from |
|---|---|
| `model_name` | registry model id (`modelman.toml`/`registry.toml` id, e.g. `ollama/qwen3.8:27b-mlx`) |
| `litellm_params.model` | provider prefix + model name — `ollama_chat/`, `openai/` (omlx), `openai/local-model` (llamacpp, fixed string for every model), `openrouter/` |
| `api_base` | provider `auth.base_url` — `:11434` ollama, `:8000/v1` omlx, `:8080/v1` llama.cpp, `https://openrouter.ai/api/v1` |
| `api_key` | ollama: omitted · omlx: literal `"not-needed"` · llamacpp: literal `"dummy-key"` · openrouter: `provider.auth.secret_ref` **verbatim** |
| `model_info` | copied from the registry model when present (e.g. `supports_function_calling`) |

`general_settings` (real block above) is the Postgres/Redis wiring: `database_url` points at the `litellm` database (trust auth, no password on the local socket — the plist additionally carries `DATABASE_URL` + `LITELLM_SALT_KEY` for the proxy process). There is **no** `master_key` in config.yaml on this machine — auth comes from the plist env `LITELLM_MASTER_KEY`.

What modelman manages vs preserves (enforced in code, `src/modelman/litellm.py`):

- **Owns only `model_list`** — `set_exposed`/`remove_exposed` add/replace/remove rows keyed by `model_name` (replace-by-id, else append; non-dict rows are skipped, not crashed on).
- **Preserves everything else as data** — `general_settings` and unrecognized top-level/other sections survive every write (writes are atomic: temp file + rename, permission bits kept).
- **Comments are NOT preserved** — PyYAML round-trip; the `# ---- <backend> ----` banners in the current file vanish on the first modelman write.

Count the live list:

```bash
python3 -c "import yaml;d=yaml.safe_load(open('/Users/keith/.config/litellm/config.yaml'));print('model_list entries:',len(d['model_list']))"
```

```text
model_list entries: 11
```

### 2. Expose / unexpose via modelman

CLI (positional model id; success lines from `src/modelman/main.py`):

<!-- UNVERIFIED — mutating commands; not run on this machine (they rewrite the live config.yaml + modelman.toml). Help text/success strings verified in source and in guide 02 §7 (identical command set); errors go to stderr with exit 1, e.g. `error: ...` for unknown ids, non-downloaded local models, or providers with no LiteLLM mapping. -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman expose ollama/gemma4:12b-mlx     # model must be downloaded (cloud models exempt)
uv run modelman unexpose ollama/gemma4:12b-mlx
```

```text
Exposed ollama/gemma4:12b-mlx through LiteLLM.
Unexposed ollama/gemma4:12b-mlx.
```

TUI — same toggle from the interactive UI (bare `uv run modelman`): press `x` on a model row; it queues the change and applies it on exit (downloading/pulling first if the model isn't ready yet); the EXPOSED column shows `Y` (see [02-providers-and-models](02-providers-and-models.md)).

Before/after, using the real files read-only. This illustrates the `expose` operation on `ollama/gpt-oss:20b`, a model that is in the registry and ready but currently has no LiteLLM row and `litellm_exposed = false`:

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
litellm_exposed = false
```

Expected after `uv run modelman expose ollama/gpt-oss:20b`:

<!-- UNVERIFIED — not run; row shape is deterministic from PROVIDER_POLICIES + the registry entry, and set_exposed would append a new row because none exists. The comment-strip behavior is stated in the save_litellm_config docstring (src/modelman/litellm.py:215-219), not exercised here. -->

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
litellm_exposed = true                            # ← only field modelman flips; ready/disk_path/size_bytes untouched
```

This model is currently unexposed on this machine — running the command above would produce the "after" state.

modelman **does** restart LiteLLM after an exposing write — `expose`/`unexpose` run the restart command from `MODELMAN_LITELLM_RESTART_CMD`, falling back to the canonical `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` when the var is unset, so a new row is live right away (proxy down ~10–20 s; if the start fails, KeepAlive crash-loops it and `~/.litellm.err.log` is the tell). The TUI status pane and CLI surface a non-fatal warning if the restart itself fails — restart manually per §5. For a genuinely new model (no existing row) `expose` appends instead of replacing; for an id whose provider has no policy it refuses with `provider '<id>' has no LiteLLM mapping`.

### 3. Hand-editing

When to hand-edit vs modelman:

| Edit | Tool |
|---|---|
| `model_list` rows (expose a model) | modelman — `expose`/`unexpose`/TUI `l` (keeps `modelman.toml` flags honest) |
| One-off tweak inside an existing `model_list` row | hand-edit (temporary: next `expose`/`unexpose` of that id replaces the row) — see Gotchas |
| `general_settings`, `litellm_settings`, logging, router/callback settings, any non-`model_list` section | hand-edit — modelman never rewrites these, so the edits survive indefinitely |

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

(Exit 0 with `YAML OK`. On a syntax error: a `yaml.YAMLError` traceback and exit 1; fix before restarting or the proxy dies on start — the plist's `StandardErrorPath` `~/.litellm.err.log` shows why. modelman surfaces the same condition cleanly: `error: LiteLLM config is not valid YAML: …` on stderr, exit 1, live config untouched.)

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

## Gotchas

- **modelman manages ONLY `model_list`.** Hand-edits to a `model_list` row are silently replaced the next time you `expose` the same id (`set_exposed` replaces by `model_name`, else appends — `src/modelman/litellm.py`). Hand-edits to `general_settings` and any other section survive every modelman write.
- **Comments don't survive modelman writes.** The save path is a PyYAML round-trip (`save_litellm_config` docstring, `src/modelman/litellm.py:215-219`): the current `# ---- Ollama (local) ----`-style banners disappear on the first `expose`/`unexpose`. Keep structural notes in this guide, not the YAML.
- **`config.yaml` carries literal api_key values on this machine.** The OpenRouter entries hold the real `sk-or-v1-…` key inline — **not** `os.environ/OPENROUTER_API_KEY` indirection. modelman writes `provider.auth.secret_ref` verbatim into `api_key` (`src/modelman/litellm.py:110`), so anything put in the registry surfaces in plaintext here. Treat `config.yaml` (and the plist) as secret material; redact before pasting anywhere.
- **modelman's bookkeeping drift (historical):** `modelman.toml` flags were out of sync because non-ollama entries were seeded outside modelman. Twenty-four in-registry models (thirteen ollama + eleven openrouter) are now modelman-exposed; the omlx/llamacpp rows, the hand-managed `openrouter/qwen/qwen3.8-*` set, and `ollama/q8`/`ollama/o35` remain hand-managed by design. First modelman write also strips the comment banners (above).
- **4000 is the proxy; backends live elsewhere.** `api_base` targets are oMLX `:8000`, llama.cpp `:8080`, ollama `:11434` — never `:4000` (that loops back into LiteLLM). Also: an omlx row in `model_list` doesn't mean that quant variant is loaded on the oMLX server — see guide 01 Gotchas (`bin/llm-isolate-provider`).
- **Syntax errors take the proxy down on restart.** Validate YAML before bouncing (§3 command); a dead start shows up as repeated respawns with errors in `~/.litellm.err.log`.
- **Postgres/Redis down ⇒ proxy fails to boot.** KeepAlive turns a dead dependency into a crash loop — respawns with connection errors in the plist's `StandardErrorPath` log (`~/.litellm.err.log`, per `~/Library/LaunchAgents/local.litellm.proxy.plist`). Pre-flight with guide 01 §6's `pg_isready -h localhost` and `redis-cli ping`.

## Going deeper

- Admin UI in depth — Postgres/Redis/Prisma setup, plist template, troubleshooting: [`../reference/litellm-admin-ui-setup.md`](../reference/litellm-admin-ui-setup.md)
- LiteLLM proxy deep-dive (prefixes, `ollama_chat/` vs `openai/`, security): [`../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md`](../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md)
- modelman's LiteLLM exposure design (writer contract, upsert semantics): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`
- Source of the writer/policies on this machine: `~/github/ohanaverse/local-ai-setup/modelman/src/modelman/litellm.py` (policies ~54–58, entry builder ~96–125, save semantics ~215–219)
- Benchmarks through the proxy: [05-benchmarks](05-benchmarks.md)
- When :4000 misbehaves: [08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md)
