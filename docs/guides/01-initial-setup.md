# Initial setup — LiteLLM proxy + Ollama, oMLX, Postgres/Redis (llama.cpp retired)

> Use this to: install and auto-start the full local-AI stack on a fresh macOS (Apple Silicon) machine and smoke-test it through the LiteLLM proxy on :4000.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · **rebuild-verified 2026-09-30** on LiteLLM 1.103.1 — fresh box restored from the old machine's backup volume (§1 LiteLLM+Prisma, §4 oMLX, §6 Postgres/Redis, §7 LaunchAgent)

## Prerequisites

Check what's already installed before installing anything:

```bash
uv tool list | grep -i litellm
ollama --version
brew list --versions 2>/dev/null | grep -E 'redis|postgresql|omlx'
which omlx litellm hf
```

```text
litellm v1.98.0
- litellm
- litellm-proxy
ollama version is 0.33.2
omlx 0.6.3rc3
postgresql@16 16.15
redis 8.10.1
/opt/homebrew/bin/omlx
/Users/keith/.local/bin/litellm
/Users/keith/.local/bin/hf
```

You need:

- macOS Apple Silicon.
- Homebrew (`brew --version`).
- `uv` (`brew install uv` — LiteLLM is installed through it).
- Go — only to build `wt` ([06-wt-agents-and-models](06-wt-agents-and-models.md)). Install via mise, pinned to wt-ci's version: `mise use -g go@1.26.7`. **Verified 2026-09-30:** a fresh box has no Go and `make install` dies with `make[1]: go: No such file or directory`; after the mise install it completes.
- OpenRouter API key from openrouter.ai/keys (starts with `sk-or-v1-`).
- Hugging Face account (only for pulling HF-hosted models; gated ones need a token).

On this machine Ollama ships as the Ollama.app login item, not a brew service — `ollama` is at `/usr/local/bin/ollama`.

## TL;DR

<!-- UNVERIFIED — not run as one block on a fresh machine. Every line targets a state verified on this install (versions, services, ports — see Steps for expected outputs); the brew/uv install lines themselves are the standard way to reach those states and were not re-run here. -->

```bash
# TL;DR — full install on a fresh Mac.
# Plists referenced by the launchctl line are created in "Steps" below
# (LiteLLM §7); on a clean machine do Steps first, then rerun this block.

# 1. brew installs (versions above are what's on this machine)
brew install omlx postgresql@16 redis   # redis: use --build-from-source if the pour hits an EPERM (§6)
brew services start postgresql@16
brew services start redis
# Ollama: brew install ollama, or install Ollama.app from ollama.com

# 2. LiteLLM proxy
uv tool install --python 3.11 'litellm[proxy,extra-proxy]'   # extra-proxy: Prisma ships there on LiteLLM ≥1.98 (§1/§6)
mkdir -p ~/.config/litellm
echo "model_list: []" > ~/.config/litellm/config.yaml   # FRESH MACHINE ONLY — on an existing box this wipes the live model_list (see Steps §1)

# 3. Pull a registry model (already pulled here → instant "success")
ollama pull qwen3.8:27b-mlx

# 4. modelman setup + expose a model to LiteLLM
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv sync
# uv run modelman        # TUI (interactive) — skip in one-shot mode; 'expose' below is non-interactive
uv run modelman expose ollama/qwen3.8:27b-mlx   # non-interactive expose (delegates to `wt litellm expose`, which writes the model_list entry and restarts the proxy; needs `wt` on PATH)

# 5. Restart the LiteLLM LaunchAgent (takes ~20 s to come back; wt already restarted it after the expose — this is only needed if that restart was skipped or failed)
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy

# 6. Smoke curls
LITELLM_MASTER_KEY=$(awk '/<key>LITELLM_MASTER_KEY<\/key>/{getline; sub(/.*<string>/,""); sub(/<\/string>.*/,""); print}' ~/Library/LaunchAgents/local.litellm.proxy.plist)
curl -s http://localhost:4000/v1/models \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  | python3 -c 'import json,sys; [print(m["id"]) for m in json.load(sys.stdin)["data"]]'
```

```text
ollama/qwen3.8:27b-mlx
omlx/mlx-community--Qwen3.8-27B-4bit   # only while it runs — wt writes the route on start
openrouter/qwen/qwen3.8-27b
openrouter/qwen/qwen3.8-flash
openrouter/qwen/qwen3.8-2.4t-a95b
openrouter/qwen/qwen3.8-max
ollama/ornith-1.5:35b

# (your registry's ids differ — ≥1 model present = success)
```

## Steps

### 1. LiteLLM proxy

Install and verify the version:

```bash
uv tool install --python 3.11 'litellm[proxy,extra-proxy]'
litellm --version
```

```text
LiteLLM: Current Version = 1.103.1
```

**2026-09-30 (rebuild-verified):** on LiteLLM ≥1.98 the Prisma DB stack moved out of `[proxy]` into a separate **`extra-proxy`** extra — `litellm[proxy]` alone boots and prints all routes, then dies at startup with `ModuleNotFoundError: No module named 'prisma'`. Pin `--python 3.11` as well: an unpinned reinstall landed on Python 3.14, where Prisma's engine binaries are missing (`Unable to find Prisma binaries. Please run 'prisma generate' first.`). §6 still generates the client — installing the package is necessary but not sufficient.

Create the config (fresh machine only):

<!-- UNVERIFIED — `~/.config/litellm/config.yaml` already exists here and wt manages it (`wt litellm ...`); running this on this machine would discard the live `model_list`. -->

```bash
mkdir -p ~/.config/litellm
echo "model_list: []" > ~/.config/litellm/config.yaml
```

**Do not** use `touch` — an empty file makes LiteLLM fail to start; `model_list: []` is a valid empty config.

First foreground run (sanity check; this machine runs it under a LaunchAgent — see §7):

<!-- UNVERIFIED — skipped on this machine; the proxy runs under `local.litellm.proxy` (§7). Output from the same command in the archive doc. -->

```bash
litellm --config ~/.config/litellm/config.yaml --port 4000
```

```text
# INFO: Proxy running on http://0.0.0.0:4000
```

The proxy serves:

- OpenAI-compatible API: `http://localhost:4000/v1` (master-key auth — see §7)
- Admin UI: `http://localhost:4000/ui`

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:4000/ui
```

```text
307
```

(307 = redirect to login; UI credentials are `UI_USERNAME`/`UI_PASSWORD` from the LaunchAgent.)

On this machine the config also carries the Postgres/Redis wiring — check before assuming a fresh install:

```bash
sed -n '/general_settings/,$p' ~/.config/litellm/config.yaml
```

```yaml
general_settings:
  database_url: "postgresql://keith@localhost:5432/litellm"
  coordination_redis:
    host: localhost
    port: 6379
```

### 2. Ollama

Install Ollama.app from ollama.com (this machine) or `brew install ollama`. The app registers the `com.ollama.ollama` login item — there is **no LaunchAgent plist for Ollama**.

```bash
ollama --version
launchctl list | grep -E 'ollama'
```

```text
ollama version is 0.33.2
-	0	com.ollama.ollama
17810	0	application.com.electron.ollama.2312009772.2312009778.64DD861F-BA99-4B1C-A478-2478B317DA0D
```

(The `application.com.electron.ollama.*` row only appears while the app window is running; a `-` PID just means launchd doesn't own the process.)

Pull a model and confirm:

```bash
ollama pull qwen3.8:27b-mlx
```

```text
success
```

(Verified live on an already-pulled model — manifest re-check is instant. A cold pull prints a progress bar first.)

```bash
ollama list | head -5
```

```text
NAME                 ID               SIZE      MODIFIED
glm-5.3:cloud        8477dab3e25b     -         22 hours ago
glm-5.3-flash:cloud  3e780905abc0     -         2 days ago
qwen3.8:27b-mlx      5642e97495e1     18 GB     2 days ago
ornith-1.5:35b       9f3b89b25219     22 GB     2 days ago
ornith-1.5:9b        e5df7dcdd8a2     6.6 GB    2 days ago
```

Same registry through the Ollama REST API (read-only):

```bash
curl -s http://localhost:11434/api/tags | head -3
```

```text
{"models":[{"name":"qwen3.8:27b-mlx","model":"qwen3.8:27b-mlx","modified_at":"2026-08-29T14:58:32.086592327-04:00","size":18174721847,"digest":"5642e97495e1a088883805981563dcdc4a040c2f53388b7a41d1f24d3622cf7e","details":{"parent_model":"","format":"safetensors","family":"","families":null,"parameter_size":"","quantization_level":"nvfp4"},"capabilities":["completion","vision","tools","thinking"]},{"name":"glm-5.3:cloud","model":"glm-5.3:cloud","remote_model":"glm-5.3","remote_host":"https://ollama.com","modified_at":"2026-08-28T16:48:46.568542897-04:00","size":293,"digest":"8477dab3e25bb0f93c468af220186f55394262ee5e9f39262af4b60b54a8c4ba","details":{"parent_model":"","format":"","family":"","families":null,"parameter_size":"753B","quantization_level":"FP8","context_length":1048576},"capabilities":["completion","thinking","tools"]},...
```

(Output is a single JSON line — `head -3` shows it whole in a terminal; elided here after two entries. Verified live 2026-08-29: 23 models, first entry is the `qwen3.8:27b-mlx` pulled above.)

### 3. llama.cpp — RETIRED

> **Retired 2026-09-07** (issue #33): the LaunchAgent's pinned GGUF no longer
> existed, so the agent crash-looped at every login and the provider-restore
> step (then `bin/llm-restore-providers`, now `modelman provider restore`
> after issue #79's port) could never succeed. Removed from this host:
> both plists, the logs, and the Homebrew formula. The verbatim plist, the
> litellm rows, the registry block, and the full re-enable procedure live in
> [provider-artifacts.md](../reference/provider-artifacts.md).

### 4. oMLX

```bash
brew install omlx    # 0.7.0 on the 2026-09-30 rebuild (0.6.3rc3 on the old machine)
brew services list | grep omlx
```

```text
omlx          none            keith   # 2026-09-30 rebuild — deliberately NO service ("started" was the old machine)
```

(It used to run as a brew service; since the wt/modelman lifecycle engines it's **optional**. Starting any omlx model — `wt start`, TUI `s`, `modelman provider isolate omlx` — runs `omlx start` on demand, and wt's live probes keep `omlx/*` routes out of `config.yaml` while the daemon is down. The 2026-09-30 rebuild omits `homebrew.mxcl.omlx.plist` on purpose: cleaner for benchmark isolation, where `omlx stop` must not fight a launchd KeepAlive. `brew services start omlx` restores the always-on setup (worth it if you want the `:8000/admin` HF downloader permanently available); `omlx stop` halts it either way.)

oMLX auto-discovers models in `~/.omlx/models/` (set via `~/.omlx/settings.json`, key `model.model_dirs`). Get a model in with the HF CLI:

<!-- UNVERIFIED — already downloaded on this machine. -->

```bash
hf download mlx-community/Qwen3.8-27B-4bit
ls ~/.omlx/models/
```

```text
Qwen3.8-27B-4bit
```

Turn off API-key auth for local inference. oMLX 0.7.0's first-run setup (the `:8000/admin` wizard) enables it, and then every request without a key gets `401 API key required`, including wt's warmup (`wt start` retries for up to 10 minutes) and every LiteLLM omlx route (`api_key: not-needed`). Set `auth.allow_unauthenticated_inference` to `true` in `~/.omlx/settings.json`, then `omlx stop` (the next `wt start` restarts it). Verified on the 2026-09-30 rebuild. Then verify the server answers without a key:

```bash
curl -s http://localhost:8000/v1/models | head -c 250
```

```text
{"object":"list","data":[{"id":"Qwen3.8-27B-4bit","object":"model","created":1788029848,"owned_by":"omlx","max_model_len":262144}]}
```

The `max_model_len` 262144 is the *server setting*; effective context is capped by `sampling.max_context_window` (32768) in `~/.omlx/settings.json`.

Restart oMLX to pick up a new model file (it scans `model_dirs` at startup):

<!-- UNVERIFIED — not run on this machine (oMLX was up and serving; didn't bounce it). Restart commands print nothing on success. Valid subcommands (verified via `omlx --help`): start|stop|restart|serve|launch|diagnose|cluster — there is NO `omlx status` despite the archive doc. -->

```bash
omlx restart
curl -s http://localhost:8000/v1/models | head -c 250
```

```text
{"object":"list","data":[{"id":"Qwen3.8-27B-4bit","object":"model","created":1788029951,"owned_by":"omlx","max_model_len":262144}]}
```

(Same JSON list shape as the §4 verify curl above — the restarted server re-announces every model in `~/.omlx/models/`.)

### 5. OpenRouter + Hugging Face auth

OpenRouter — an account + key from [openrouter.ai/keys](https://openrouter.ai/keys). LiteLLM reads the variable **`OPENROUTER_API_KEY`** (not `OPENAI_API_KEY`). On this machine it lives in the LiteLLM LaunchAgent plist so the proxy has it after reboots (§7). OpenRouter model entries in `~/.config/litellm/config.yaml` use `model: openrouter/<author>/<slug>` with `api_key: os.environ/OPENROUTER_API_KEY`.

Confirm all secret env keys are present in the plist (values not printed):

```bash
grep -cE 'OPENROUTER_API_KEY|LITELLM_MASTER_KEY|DATABASE_URL|LITELLM_SALT_KEY|UI_PASSWORD|UI_USERNAME' ~/Library/LaunchAgents/local.litellm.proxy.plist
```

```text
6
```

Hugging Face — install and log in:

<!-- UNVERIFIED — `uv tool install` / `hf auth login` not rerun; installed + authed state verified via `uv tool list` and `hf auth whoami`. **Still pending on the 2026-09-30 rebuild** — hf is absent until this runs (Tier-1 artifact downloads below need it). -->

```bash
uv tool install huggingface_hub
hf auth login
hf auth whoami
```

```text
user=gitmanntoo orgs=Wisconsin,mlx-community
```

**Rebuilding from a backup volume — skip the login** (verified 2026-09-30): copy the stored token(s), then confirm:

```bash
mkdir -p ~/.cache/huggingface
cp "/Volumes/Users Bak/keith/.cache/huggingface/token" ~/.cache/huggingface/
cp -R "/Volumes/Users Bak/keith/.cache/huggingface/stored_tokens" ~/.cache/huggingface/   # if present
hf auth whoami   # -> user=gitmanntoo orgs=Wisconsin,mlx-community
```

What hf is needed for here:

- **Downloading oMLX artifacts** the modelman registry references — e.g. the Tier-1 starter `hf download mlx-community/Qwen3.8-27B-4bit --local-dir ~/.omlx/models/Qwen3.8-27B-4bit` (paths must match the registry `disk_path`, and the directory must be **flat** — oMLX cannot parse the nested HF-cache layout; modelman's own TUI download path does the same snapshot-to-flat-dir correctly if you queue the download there instead)
- `bin/mlx-quantize` (see [10-mlx-lm-quantization](10-mlx-lm-quantization.md)) and `benchmarks/` scripts that fetch models

(Auth is not required for public repos like `mlx-community`; it raises rate limits and covers gated orgs.)

### 6. PostgreSQL + Redis

LiteLLM's virtual keys, spend tracking, and Admin UI login require Postgres (SQLite hangs the proxy at startup). Redis gives cross-worker coordination/cache.

```bash
brew install postgresql@16 redis
brew services start postgresql@16
brew services start redis
/opt/homebrew/opt/postgresql@16/bin/pg_isready -h localhost -p 5432
redis-cli ping
```

```text
localhost:5432 - accepting connections
PONG
```

**2026-09-30 (rebuild-verified):** this machine threw a wrench at the Redis bottle — `brew install redis` aborted at the pour with `Failed to read Mach-O binary ... redisearch.so ... Errno::EPERM`, and even the source-built file was unreadable system-wide, so Redis aborted on module load (`dlopen ... errno=1`). No third-party AV is installed — it's a per-file read denial specific to that module. Fix: `brew install --build-from-source redis`, then comment out **only** the `redisearch.so` line in `/opt/homebrew/etc/redis.conf` (RedisBloom/ReJSON/Timeseries load fine; original conf kept as `redis.conf.bak-original`). LiteLLM needs plain Redis only. Future upgrades: `brew upgrade --build-from-source redis` — the bottle pour hits the same EPERM.

Create the database the proxy logs into (fresh machine only):

<!-- UNVERIFIED — `litellm` DB already exists here (verified via pg_database = 1); rerunning would error "already exists". -->

```bash
/opt/homebrew/opt/postgresql@16/bin/psql -U "$(whoami)" -h localhost -p 5432 -d postgres -c "CREATE DATABASE litellm;"
```

```text
CREATE DATABASE
```

**Spend history does not migrate** (verified 2026-09-30): the `/Volumes/Users Bak/` backup carries user config dirs (`~/.config`, `~/Library/LaunchAgents`) — not the Postgres *data cluster* at `/opt/homebrew/var/postgresql@16` — so `LiteLLM_SpendLogs` and the Admin-UI tables start empty on the rebuild. LiteLLM's startup `migrate deploy` creates the full schema with zero rows; the `litellm-session-logs/` pipeline will find new rows only going forward. (If the old cluster was ever backed up separately, `pg_dump`/restore into this database before first launch.)

Point the proxy at both services — `general_settings` in `~/.config/litellm/config.yaml` (see §1 for the verified block) plus `DATABASE_URL` + `LITELLM_SALT_KEY` in the LaunchAgent plist (§7).

One-time Prisma setup (**verified 2026-09-30**; see also [litellm-admin-ui-setup.md](../reference/litellm-admin-ui-setup.md) §8–9):

```bash
# 1. prisma ships in the `extra-proxy` extra on LiteLLM ≥1.98 — install via §1's command
#    (fallback for older envs: uv pip install --python ~/.local/share/uv/tools/litellm/bin/python prisma)
# 2. generate the client — glob the site-packages dir, don't hardcode the Python version
SP=$(ls -d "$HOME"/.local/share/uv/tools/litellm/lib/python3.* | tail -1)
PATH="$HOME/.local/share/uv/tools/litellm/bin:$PATH" \
  "$HOME/.local/share/uv/tools/litellm/bin/python" -m prisma generate --schema="$SP/litellm/proxy/schema.prisma"
```

No `prisma db push` needed on ≥1.98 — the proxy runs `prisma migrate deploy` against `$DATABASE_URL` automatically at startup. Expect the `LiteLLM_*` tables — `psql ... -d litellm -c "\dt"` lists them.

### 7. LaunchAgents

The LiteLLM proxy is a hand-rolled LaunchAgent (the brew services in §4/§6 are already in place by then).

Rebuilding from a backup volume with the same username? Skip the template — every path inside the old plist matches this machine verbatim (`/Users/keith/.local/bin/litellm`, `~/.config/litellm/config.yaml`, `.litellm.log`/`.litellm.err.log`), including the secrets:

```bash
cp "/Volumes/Users Bak/keith/Library/LaunchAgents/local.litellm.proxy.plist" ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.litellm.proxy.plist   # bootout gui/$(id -u)/local.litellm.proxy first if already loaded
```

Otherwise, its plist template — fill in real secrets (`LITELLM_MASTER_KEY`/`LITELLM_SALT_KEY` start with `sk-` / random 40 chars; `OPENROUTER_API_KEY` from openrouter.ai; `UI_USERNAME`/`UI_PASSWORD` of your choice; `DATABASE_URL` from §6):

<!-- UNVERIFIED — existing plist not overwritten; template mirrors the live plist's structure (verified: PATH, LITELLM_MASTER_KEY, UI_USERNAME, UI_PASSWORD, OPENROUTER_API_KEY, DATABASE_URL, LITELLM_SALT_KEY) merged with the archive doc's template. -->

```bash
cat > ~/Library/LaunchAgents/local.litellm.proxy.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
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
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/Users/keith/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
        <key>OPENROUTER_API_KEY</key>
        <string>sk-or-v1-REPLACE_WITH_YOUR_KEY</string>
        <key>LITELLM_MASTER_KEY</key>
        <string>sk-REPLACE</string>
        <key>LITELLM_SALT_KEY</key>
        <string>REPLACE_WITH_openssl_rand_base64_32</string>
        <key>UI_USERNAME</key>
        <string>REPLACE</string>
        <key>UI_PASSWORD</key>
        <string>REPLACE</string>
        <key>DATABASE_URL</key>
        <string>postgresql://keith@localhost:5432/litellm</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/keith/.litellm.log</string>
    <key>StandardErrorPath</string>
    <string>/Users/keith/.litellm.err.log</string>
</dict>
</plist>
EOF

launchctl load -w ~/Library/LaunchAgents/local.litellm.proxy.plist   # (modern equivalent: launchctl bootstrap gui/$(id -u) <plist>; unload via launchctl bootout)
launchctl list | grep -E 'litellm|omlx|ollama|redis|postgres'
```

```text
-	0	com.ollama.ollama
94146	0	local.litellm.proxy
80374	0	homebrew.mxcl.postgresql@16
97297	0	homebrew.mxcl.omlx
88057	0	homebrew.mxcl.redis
17810	0	application.com.electron.ollama.2312009772.2312009778.64DD861F-BA99-4B1C-A478-2478B317DA0D
```

(PIDs will differ. The plist's `EnvironmentVariables` block carries the secrets — `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `OPENROUTER_API_KEY`, `UI_USERNAME`, `UI_PASSWORD`, `DATABASE_URL` — redact before sharing.)

Restart the proxy after any edit to `~/.config/litellm/config.yaml` or the plist:

```bash
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy && echo "kickstart OK"
```

```text
kickstart OK
```

Expect the port to refuse connections for ~15 s — the proxy was answering 401 again by the 20 s mark (verified live; new PID proves the bounce).

`~/.local/bin/llm-restart` restarts the whole stack in one shot with per-service health checks (`llm-restart`, or scoped: `llm-restart litellm|omlx|ollama`).

## Verification

End-to-end smoke, from nothing running:

```bash
curl -s -m 3 http://localhost:4000/v1/models
```

```text
{"error":{"message":"Authentication Error, No api key passed in.","type":"auth_error","param":"None","code":"401"}}
```

(401 = proxy is up and correctly demanding the master key.)

With the master key from the plist:

```bash
LITELLM_MASTER_KEY=$(awk '/<key>LITELLM_MASTER_KEY<\/key>/{getline; sub(/.*<string>/,""); sub(/<\/string>.*/,""); print}' ~/Library/LaunchAgents/local.litellm.proxy.plist)
curl -s http://localhost:4000/v1/models \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  | python3 -c 'import json,sys; [print(m["id"]) for m in json.load(sys.stdin)["data"]]'
```

```text
ollama/qwen3.8:27b-mlx
omlx/mlx-community--Qwen3.8-27B-4bit   # only while it runs — wt writes the route on start
openrouter/qwen/qwen3.8-27b
openrouter/qwen/qwen3.8-flash
openrouter/qwen/qwen3.8-2.4t-a95b
openrouter/qwen/qwen3.8-max
ollama/ornith-1.5:35b

# (your registry's ids differ — ≥1 model present = success)
```

A model must appear in this list **and** in `~/.config/litellm/config.yaml` `model_list` to be routable. Real generation through the proxy:

```bash
curl -s -m 60 http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"omlx/Qwen3.8-27B-4bit","messages":[{"role":"user","content":"Say hi in 5 words"}],"max_tokens":20}' \
  | python3 -m json.tool
```

```text
{
    "id": "chatcmpl-1fcc63d6",
    "created": 1788029910,
    "model": "omlx/Qwen3.8-27B-4bit",
    "object": "chat.completion",
    "choices": [
        {
            "finish_reason": "length",
            "index": 0,
            "message": {
                "content": "We need to respond to user: \"Say hi in 5 words\". Need final exactly 5",
                "role": "assistant"
            }
        }
    ],
    "usage": {
        "completion_tokens": 20,
        "prompt_tokens": 58,
        "total_tokens": 78,
        "output_tokens": 20,
        "model_load_duration": 8.08,
        "total_time": 3.55
    }
}
```

(`provider_specific_fields` objects and `input_tokens` omitted for brevity.)

Final check — a `wt` agent launch actually streaming through this proxy:

<!-- UNVERIFIED — interactive TUI/session, not launched from this session. Run it by hand; expect the claude TUI streaming from the worktree. -->

```bash
# from: anywhere
claude-wt -W smoke-test -M ollama/qwen3.8:27b-mlx
```

## Gotchas

- **oMLX routes are wt-written and exist only while the model runs.** `wt start omlx/mlx-community--Qwen3.8-27B-4bit` writes a `model_list` row named after the registry id, with `api_base: http://localhost:8000/v1`; `wt stop` removes it. The old hand-written omlx rows (`omlx/Qwen3.8-27B-4bit`, `omlx/Ornith-1.5-35B-A3B-MLX-{4,6}bit`) were removed 2026-09-30 (#168). Don't add rows by hand: wt's sync never removes ids that aren't in the registry. oMLX serves only models present in `~/.omlx/models/`; for benchmark isolation, `uv run --directory modelman modelman provider isolate omlx` (4-bit) or `... omlx-6bit` (6-bit), then `... provider restore`.
- **Per-backend stop mechanics differ.** Ollama model: `ollama stop <model-id>` (daemon stays up); oMLX: `omlx stop` (halts the service); LiteLLM: `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` or `~/.local/bin/llm-restart`.
- **Postgres credentials are not in this repo.** The proxy gets them from `DATABASE_URL` in `~/Library/LaunchAgents/local.litellm.proxy.plist` and `general_settings.database_url` in `~/.config/litellm/config.yaml` (`postgresql://keith@localhost:5432/litellm`, trust auth, no password on local socket connections).
- **"Installed ≠ loaded" for LaunchAgents.** A plist sitting in `~/Library/LaunchAgents/` proves nothing; check `launchctl list | grep -E 'litellm|omlx|ollama|redis|postgres'`. If a job shows `-` in the PID column it is loaded but exited (check the plist's `StandardErrorPath` log: `~/.litellm.err.log`).
- **Discrepancies in the archive doc, reality wins:** (1) it says `omlx status` — oMLX 0.6.3rc3 has no `status` subcommand (`start|stop|restart|serve|launch|diagnose|cluster` only); (2) its `sk-1234` Bearer examples are placeholders — real key is `LITELLM_MASTER_KEY` from the plist; (3) its `hf login` is stale — current huggingface-hub v1.28.0 CLI says `hf auth login`. (The archive doc's llama.cpp items are moot — llama.cpp was retired 2026-09-07, see [provider-artifacts.md](../reference/provider-artifacts.md).)
- **Run modelman from the `modelman/` directory.** modelman is not installed globally anymore. Run it from `~/github/ohanaverse/local-ai-setup/modelman`:

  ```bash
  # from: ~/github/ohanaverse/local-ai-setup/modelman
  uv run modelman          # bare = TUI
  uv run modelman expose <model-id>
  uv run modelman sync
  ```

- **`echo "model_list: []"` beats `touch`** for the initial LiteLLM config — an empty file fails to start.
- **Reinstalling LiteLLM wipes its tool env and resets the Python version.** Always reinstall as `uv tool install --force --python 3.11 'litellm[proxy,extra-proxy]'` (§1): bare `litellm[proxy]` drops Prisma (`No module named 'prisma'` at proxy startup), and without the 3.11 pin uv may pick 3.14, where Prisma's engine binaries are missing. After any reinstall/upgrade, re-run §6's `prisma generate` — the generated client lives inside the tool env.
- **`redisearch.so` is unreadable on this machine (2026-09-30).** The brew bottle pour fails on it (`Errno::EPERM`), and even a source-built copy aborts Redis at module load — no third-party AV is running; it's a per-file read denial this box applies to that module. If `brew services list` shows redis `error`/crash-looping, check `/opt/homebrew/var/log/redis.log` for `redisearch` first and apply the §6 fix (build-from-source + commented `loadmodule` line; original conf at `redis.conf.bak-original`).
- **A tiny `max_tokens` against qwen3.8 returns empty `content`.** Reasoning tokens consume the cap before any visible text — the rebuild's first proxy smoke call (`max_tokens: 20`) came back empty; re-running at 80 returned the expected string with `finish_reason: stop`. Bump the cap when smoke-testing reasoning models through the proxy (verified 2026-09-30).
- **`litellm` plist secrets:** the `EnvironmentVariables` block carries `OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `UI_USERNAME`, `UI_PASSWORD`, `DATABASE_URL` — redact all of it (plus OpenRouter `api_key` values inside `~/.config/litellm/config.yaml`) before pasting anything anywhere.
- **brew services run in the user session** — services start at login, not bare boot (headless setups would need `/Library/LaunchAgents/` instead).

## Going deeper

- Full original write-up with the plist templates and model-switching tables: [`../archive/Local%20AI%20Setup%202026-08-25.md`](../archive/Local%20AI%20Setup%202026-08-25.md)
- Admin UI, Postgres/Redis setup, Prisma: [`../reference/litellm-admin-ui-setup.md`](../reference/litellm-admin-ui-setup.md)
- LiteLLM proxy deep-dive (prefixes, `ollama_chat/` vs `openai/`, security): [`../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md`](../reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md)
- oMLX backend reference: [`../reference/oMLX%20Download%20and%20Run.md`](../reference/oMLX%20Download%20and%20Run.md)
- Hugging Face downloads (`hf download`, cache layout): [`../reference/Downloading%20and%20Managing%20Hugging%20Face%20Models%20on%20macOS%20for%20Local%20LLM%20Inference%20%282026%29.md`](../reference/Downloading%20and%20Managing%20Hugging%20Face%20Models%20on%20macOS%20for%20Local%20LLM%20Inference%20%282026%29.md)
- Next in this set — register/expose models via modelman: [02-providers-and-models](02-providers-and-models.md)
