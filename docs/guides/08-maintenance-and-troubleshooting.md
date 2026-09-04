# Maintenance and troubleshooting — health check, restarts, log triage, upgrades

> Use this to: keep the running stack healthy day-to-day — one health-check block, per-service restart/repair, crash-loop triage, and safe upgrades.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- Full stack installed and initially configured per [01-initial-setup](01-initial-setup.md) — the five LaunchAgents exist and load (`~/Library/LaunchAgents/`: `local.litellm.proxy.plist`, `local.llamacpp.server.plist`, `homebrew.mxcl.omlx.plist`, `homebrew.mxcl.postgresql@16.plist`, `homebrew.mxcl.redis.plist`).
- modelman runnable from its repo, not a global install (it is not installed as a `uv tool` — guide 02 Gotchas).
- This repo checked out — `bin/llm-isolate-provider` / `bin/llm-restore-providers` live here (guide 05), and `~/.local/bin/llm-restart` is on PATH for whole-stack restarts.
- Every restart command below assumes your terminal user is the one whose launchd domain owns the agents (`gui/$(id -u)`), i.e. a normal logged-in session, not an SSH-into-a-different-user session.

## TL;DR

Full health check — every answer is read-only, safe to run any time. Run it after any change; the whole block ran live on 2026-08-29 and output below is verbatim:

```bash
curl -s -m 2 http://localhost:4000/v1/models -o /dev/null -w "4000(litellm):%{http_code}\n"   # 401 = proxy up, demanding key
curl -s -m 2 http://localhost:8080/health -o /dev/null -w "8080(llama.cpp):%{http_code}\n"
curl -s -m 2 http://localhost:8000/health -o /dev/null -w "8000(omlx):%{http_code}\n"         # /health — plain / gives 404
curl -s -m 2 http://localhost:11434/api/tags -o /dev/null -w "11434(ollama):%{http_code}\n"
launchctl list | grep -E 'litellm|llamacpp|omlx|postgresql|redis|ollama'
pg_isready -h localhost
redis-cli ping
```

```text
4000(litellm):401
8080(llama.cpp):200
8000(omlx):200
11434(ollama):200
-	0	com.ollama.ollama
96295	-15	local.litellm.proxy
80374	0	homebrew.mxcl.postgresql@16
94631	0	local.llamacpp.server
97297	0	homebrew.mxcl.omlx
88057	0	homebrew.mxcl.redis
17810	0	application.com.electron.ollama.2312009772.2312009778.64DD861F-BA99-4B1C-A478-2478B317DA0D
localhost:5432 - accepting connections
PONG
```

Reading it fast:

- 401 on `:4000` = healthy (proxy up, correctly refusing keyless requests); 200 elsewhere.
- `launchctl list` columns are PID / last-exit-status / label. `local.llamacpp.server` has `RunAtLoad` and `KeepAlive` both `true` in its plist (verified) — **:8080 is expected to be up at every login**, and launchd respawns it if it dies. Same two keys are `true` in all five plists.
- `local.litellm.proxy` shows status `-15` here because it was SIGTERMed by a `kickstart -k` earlier that day (see Gotchas — `0`/`-15` are the only healthy readings for the middle column).
- Ollama row has no PID (`-`) and no LaunchAgent plist exists for it — the `com.ollama.ollama` login item (Ollama.app) owns the daemon; the `application.com.electron.ollama.*` row appears only while the app window is open.
- Any line that differs → §1 for restart mechanics, §3 for logs.

## Steps

### 1. After a reboot: what auto-starts, what needs a kick

Read the plists, not memory. All five launchd jobs carry both keys (verified live on 2026-08-29):

```bash
grep -A1 'RunAtLoad\|KeepAlive' ~/Library/LaunchAgents/local.llamacpp.server.plist ~/Library/LaunchAgents/local.litellm.proxy.plist /Users/keith/Library/LaunchAgents/homebrew.mxcl.omlx.plist
```

```text
/Users/keith/Library/LaunchAgents/local.llamacpp.server.plist:    <key>RunAtLoad</key>
/Users/keith/Library/LaunchAgents/local.llamacpp.server.plist:    <true/>
/Users/keith/Library/LaunchAgents/local.llamacpp.server.plist:    <key>KeepAlive</key>
/Users/keith/Library/LaunchAgents/local.llamacpp.server.plist:    <true/>
/Users/keith/Library/LaunchAgents/local.litellm.proxy.plist:    <key>RunAtLoad</key>
/Users/keith/Library/LaunchAgents/local.litellm.proxy.plist:    <true/>
/Users/keith/Library/LaunchAgents/local.litellm.proxy.plist:    <key>KeepAlive</key>
/Users/keith/Library/LaunchAgents/local.litellm.proxy.plist:    <true/>
/Users/keith/Library/LaunchAgents/homebrew.mxcl.omlx.plist:	<key>RunAtLoad</key>
/Users/keith/Library/LaunchAgents/homebrew.mxcl.omlx.plist:	<true/>
/Users/keith/Library/LaunchAgents/homebrew.mxcl.omlx.plist:	<key>KeepAlive</key>
/Users/keith/Library/LaunchAgents/homebrew.mxcl.omlx.plist:	<true/>
```

(`postgresql@16` and `redis` plists also carry `RunAtLoad`/`KeepAlive` `true` — verified by reading those two files directly; same shape, skipped above for brevity.)

So after login: LiteLLM (:4000), llama.cpp (:8080), oMLX (:8000), Postgres, Redis all come up on their own — llama.cpp included, because of its `RunAtLoad`/`KeepAlive`. KeepAlive also means **if any of them crash, launchd restarts them automatically**; a service that stays down means it is crash-looping against KeepAlive, not waiting for you (go to §3).

Ollama is the exception: **no plist exists** — the daemon is owned by the Ollama.app login item (`com.ollama.ollama`), which starts at login only if the app is enabled as a login item and launches. When :11434 is dead after a reboot, open Ollama.app and wait a few seconds.

Run `brew services list` to see which of your jobs brew considers its own (this is the trio you manage via `brew services`; LiteLLM and llama.cpp are hand-rolled plists outside brew):

```bash
brew services list
```

```text
Name          Status User  File
omlx          started         keith ~/Library/LaunchAgents/homebrew.mxcl.omlx.plist
postgresql@16 started         keith ~/Library/LaunchAgents/homebrew.mxcl.postgresql@16.plist
redis         started         keith ~/Library/LaunchAgents/homebrew.mxcl.redis.plist
```

Per-backend reference — commands and log paths verified against the live plists (`StandardErrorPath`/`StandardOutPath` keys) and the brew service list; only the LiteLLM restart has a measured end-to-end run (§2). All restarts are disruptive — see the UNVERIFIED note before using them blind:

<!-- UNVERIFIED — restart column not exercised in this session except the LiteLLM kickstart (measured in Steps §2). Check/log columns verified from the live plists and `brew services list` above. -->

| Backend | Check (§TL;DR) | Restart | Logs |
|---------|----------------|---------|------|
| LiteLLM :4000 | `401` curl | `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` | `/Users/keith/.litellm.err.log` (+ `/Users/keith/.litellm.log`) |
| llama.cpp :8080 | `200` curl | `launchctl kickstart -k gui/$(id -u)/local.llamacpp.server` | `/Users/keith/.llamacpp.err.log` (+ `/Users/keith/.llamacpp.log`) |
| oMLX :8000 | `200` on `/health` | `omlx restart` or `brew services restart omlx` | `/opt/homebrew/var/log/omlx.log` |
| Ollama :11434 | `200` on `/api/tags` | relaunch Ollama.app (no plist — launchd does not own it) | `/Users/keith/.ollama/logs/server.log` |
| Postgres 5432 | `pg_isready -h localhost` | `brew services restart postgresql@16` | `/opt/homebrew/var/log/postgresql@16.log` |
| Redis 6379 | `redis-cli ping` | `brew services restart redis` | `/opt/homebrew/var/log/redis.log` |

Whole stack in one shot (post-download, post-upgrade): `~/.local/bin/llm-restart` restarts all four providers with per-service health checks; scoped variants take a service name (e.g. `llm-restart litellm`) — guide 01 §7.

### 2. "Model missing from LiteLLM" — ordered debug flow

Worked on a model you expect on `:4000` but don't see (or a client gets 404/`model not found` for). Steps (1–3, 5–7) ran live read-only on 2026-08-29 against a healthy model (`ollama/qwen3.8:27b-mlx`), so the outputs show what clean looks like at each stage; step 4 is mutating (not run; marked UNVERIFIED); step 8 restarts the proxy — the one mutating step run, measured below.

**Step 1 — is it in the proxy at all?** Without auth you learn nothing (`401`); pull the master key from the plist env (value stays un-printed) and list model ids:

```bash
LITELLM_MASTER_KEY=$(awk '/<key>LITELLM_MASTER_KEY<\/key>/{getline; sub(/.*<string>/,""); sub(/<\/string>.*/,""); print}' /Users/keith/Library/LaunchAgents/local.litellm.proxy.plist)
curl -s http://localhost:4000/v1/models -H "Authorization: Bearer $LITELLM_MASTER_KEY" | python3 -c 'import json,sys; [print(m["id"]) for m in json.load(sys.stdin)["data"]]'
```

```text
ollama/qwen3.8:27b-mlx
omlx/Qwen3.8-27B-4bit
openrouter/qwen/qwen3.8-27b
openrouter/qwen/qwen3.8-flash
openrouter/qwen/qwen3.8-2.4t-a95b
openrouter/qwen/qwen3.8-max
llama.cpp/local-llama
ollama/ornith-1.5:35b
omlx/Ornith-1.5-35B-A3B-MLX-4bit
omlx/Ornith-1.5-35B-A3B-MLX-6bit
llama.cpp/ornith-1.5-35b
```

Present in this list but not routeable? Skip to step 5 (backend down). Absent? Continue.

**Step 2 — does `model_list` in config.yaml carry it?** config.yaml is what the proxy routes from:

```bash
grep -n 'model_name:' /Users/keith/.config/litellm/config.yaml
```

```text
3:  - model_name: ollama/qwen3.8:27b-mlx
12:  - model_name: omlx/Qwen3.8-27B-4bit
19:  - model_name: openrouter/qwen/qwen3.8-27b
26:  - model_name: openrouter/qwen/qwen3.8-flash
32:  - model_name: openrouter/qwen/qwen3.8-2.4t-a95b
38:  - model_name: openrouter/qwen/qwen3.8-max
45:  - model_name: llama.cpp/local-llama
52:  - model_name: ollama/ornith-1.5:35b
60:  - model_name: omlx/Ornith-1.5-35B-A3B-MLX-4bit
67:  - model_name: omlx/Ornith-1.5-35B-A3B-MLX-6bit
74:  - model_name: llama.cpp/ornith-1.5-35b
```

Present here but absent from `:4000`? config.yaml changed since the proxy last started → jump to step 7 (restart). Absent here too? Continue.

**Step 3 — is it exposed in modelman's state?** Check the per-model exposure flag, then count how many models modelman considers exposed:

```bash
grep -B1 'litellm_exposed' /Users/keith/.config/local-ai/modelman.toml | grep -c 'litellm_exposed = true'
grep -A5 '^\[model_state."ollama/qwen3.8:27b-mlx"\]' /Users/keith/.config/local-ai/modelman.toml
```

```text
24
[model_state."ollama/qwen3.8:27b-mlx"]
ready = true
disk_path = "ollama:qwen3.8:27b-mlx"
size_bytes = 19327352832
litellm_exposed = true
```

Historical note (2026-08-30, updated 2026-09-03): `modelman.toml` flags were out of sync because the non-ollama entries were seeded outside modelman. The count above is now 24: thirteen ollama models (the two local MLX downloads `ollama/qwen3.8:27b-mlx` and `ollama/ornith-1.5:35b` plus eleven cloud-hosted ollama models) and eleven openrouter models. The omlx/llamacpp entries remain hand-managed by design and will still show `litellm_exposed = false` (or be absent from `[model_state...]` entirely) even though they're live in `config.yaml`. `config.yaml` is the routing source of truth; `litellm_exposed` is bookkeeping. A `false` here does not prove the model is missing. A `true` with no `config.yaml` row is one of two things — disambiguate before re-exposing:

- **`ready = false` alongside the flag** → mid-cascade: the user pressed `x` on a not-ready model in the TUI, which queues `litellm_exposed = true` AND `ready = true` (a download). The flag is set, but the apply step hasn't run yet, so `config.yaml` has no row. **Do not re-expose** — apply the pending changes from the TUI (or `modelman apply` if exposed via CLI), and the row appears. Re-exposing now is a redundant op that bounces the proxy without fixing the gap.
- **`ready = true` alongside the flag** (or the model is a cloud model — `provider_id` in `openrouter`, or `location = "cloud"` for an ollama model, where `ready` is permanently false) → genuine drift: modelman expects the row, and it was lost. → step 4 (re-expose replaces the row by id).

(Drift is a known guide 04 gotcha.)

**Step 4 — not exposed anywhere: expose it.** modelman writes the `model_list` row and flips the flag (local models must be downloaded first):

<!-- UNVERIFIED — mutating; not run in this session (would rewrite the live config.yaml + modelman.toml). Success line from live-verified source (guide 04 §2, guide 02 §7). -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman expose ollama/qwen3.8:27b-mlx
```

```text
Exposed ollama/qwen3.8:27b-mlx through LiteLLM.
```

Bad ids refuse instead — an `error: …` line on stderr, exit 1 (live-verified error paths: guide 02 §7, guide 04 §2) — meaning the id from Steps 1–3 never existed; fix the id, not the config.

**Step 5 — backend port actually up?** The `api_base` in the model's row points at a backend; probe the one that owns your model (all three ran live, 2026-08-29):

```bash
curl -s -m 2 http://localhost:11434/api/tags -o /dev/null -w "11434(ollama):%{http_code}\n"
curl -s -m 2 http://localhost:8000/health -o /dev/null -w "8000(omlx):%{http_code}\n"
curl -s -m 2 http://localhost:8080/health -o /dev/null -w "8080(llama.cpp):%{http_code}\n"
```

```text
11434(ollama):200
8000(omlx):200
8080(llama.cpp):200
```

A dead backend is a §1/§3 problem, not a config problem — fix the backend first.

**Step 6 — api_base right?** The row must point the proxy at the right port (8080 vs 8000 mixups are classic):

```bash
grep -A3 'model_name: ollama/qwen3.8:27b-mlx' /Users/keith/.config/litellm/config.yaml
```

```text
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx
      api_base: http://localhost:11434
```

(omlx rows use `http://localhost:8000`, llama.cpp rows `http://localhost:8080`, OpenRouter rows no `api_base` — key from plist env instead.)

**Step 7 — config.yaml still valid YAML?** A hand-edit typo keeps the proxy in a crash loop (it re-reads only at start):

```bash
python3 -c "import yaml;d=yaml.safe_load(open('/Users/keith/.config/litellm/config.yaml'));print('model_list entries:',len(d['model_list']))"
```

```text
model_list entries: 11
```

Parse error → fix by hand or rebuild the row via modelman (it does atomic PyYAML writes, guide 04 §3); count `11` on this machine = current healthy state (your count is however many you exposed).

**Step 8 — restart, then re-check Step 1.** The proxy reads config.yaml only at start:

```bash
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy && echo "kickstart OK"
```

```text
kickstart OK
```

Measured live 2026-08-29 (this session): old PID `65475` → new PID `96295`; the port refused connections and answered `401` again after **7 s**. Guides 01/04 measured 9–15 s on earlier runs — plan for a ~10–20 s dead window and confirm with the Step 1 curl rather than assuming. The same `kickstart -k` works for `local.llamacpp.server` (it also reloads the pinned GGUF — slower); everything else uses the mechanics in the §1 table.

### 3. Log triage

Log homes (all from live plists / on-disk checks, 2026-08-29):

| Service | Log file(s) | Notes |
|---------|-------------|-------|
| LiteLLM | `/Users/keith/.litellm.err.log`, `/Users/keith/.litellm.log` | two files (stderr/stdout); launchd appends — the file only grows |
| llama.cpp | `/Users/keith/.llamacpp.err.log`, `/Users/keith/.llamacpp.log` | llama-server is chatty; 65k+ lines is normal, not a loop |
| oMLX | `/opt/homebrew/var/log/omlx.log` | single file, both streams; `omlx diagnose` for install/runtime issues |
| Postgres | `/opt/homebrew/var/log/postgresql@16.log` | single file |
| Redis | `/opt/homebrew/var/log/redis.log` | single file |
| Ollama | `/Users/keith/.ollama/logs/server.log`, `app.log` | app-managed; rotated as `server-1.log` … `server-5.log` |

Crash-loop recognition — with `KeepAlive = true`, a service that dies on startup is relaunched by launchd immediately, forever. Look for the signature in `launchctl list`:

```bash
launchctl list | grep -E 'litellm|llamacpp|omlx|postgresql|redis'
```

```text
96295	-15	local.litellm.proxy
80374	0	homebrew.mxcl.postgresql@16
94631	0	local.llamacpp.server
97297	0	homebrew.mxcl.omlx
88057	0	homebrew.mxcl.redis
```

- **Healthy running:** a PID + `0` (or `-15` right after a kickstart — see Gotchas). E.g. the `local.litellm.proxy` / `local.llamacpp.server` rows above (live).
- **Died and launchd gave up / between retries:** `-` PID with a **non-zero** status. Then:
- **Crash loop you can't see in the middle column:** a PID present but changing every time you look, with the port still dead. Tells: watch the PID (`launchctl list | grep <label>` twice a minute), or a fast-growing err log — `ls -la` the StandardErrorPath twice and compare mtime/size.

Then read the actual error:

```bash
tail -50 /Users/keith/.litellm.err.log
```

Most tail traffic on this machine is benign — e.g. the traceback shape below is what an unauthenticated scan/probe produces (every 401 in the health check lands here too) and is *not* a fault:

```text
Traceback (most recent call last):
  File "/Users/keith/.local/share/uv/tools/litellm/lib/python3.13/site-packages/litellm/proxy/auth/user_api_key_auth.py", line 1489, in _user_api_key_auth_builder
    raise Exception("No api key passed in.")
Exception: No api key passed in.
```

Startup-fault lines worth reacting to: repeated `Error loading config`, YAML parse errors, database-connection errors (Postgres down — check `pg_isready`), or the same traceback re-printed every few seconds (looping). For the brew trio the log file is shared stdout+stderr, so `tail -50` those directly.

**Redaction rule:** err logs can echo API keys, `Authorization` headers, and request bodies. Before pasting any log excerpt into an issue, a repo, or an agent transcript: strip anything `sk-…`, `Bearer …`, or base64-ish that isn't obviously a path. The LiteLLM plist env values themselves never appear in these logs, but never quote plist contents either — its `EnvironmentVariables` block is the secret store (guide 01 §7).

### 4. Upgrades — command per tool, then verify

**modelman** (repo-local; it is not installed globally — guide 02):

<!-- UNVERIFIED — upgrade not run in this session (mutates the repo + tool env); checkout was d93f5b9, 2026-08-29. Under-way output depends on how far behind the checkout is; when current: `Already up to date.` plus uv's install line. -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
git pull && uv sync
```

Verify — these commands ran live, 2026-08-29, on current checkout `d93f5b9`:

```bash
git -C /Users/keith/github/ohanaverse/local-ai-setup/modelman rev-parse --short HEAD
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman sync --help
```

```text
d93f5b9
Usage: modelman sync [OPTIONS]

  Reconcile configured models against their providers.
```

(A `VIRTUAL_ENV does not match the project environment` warning from pyenv is normal noise; see guide 07.)

**wt** — rebuild over the PATH copy (`~/.local/bin/wt`), not GOPATH; the gotcha below explains why:

<!-- UNVERIFIED — build not run in this session (writes a binary); checkout was bf98d14, 2026-08-29. -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/wt
# (in the merged monorepo; wt lives at local-ai-setup/wt/)
cd /Users/keith/github/ohanaverse/local-ai-setup/wt
git pull && go build -o /Users/keith/.local/bin/wt ./cmd/wt
```

Verify — `wt --version` ran live, 2026-08-29:

```bash
wt --version
```

```text
wt 0.1.0
```

**Gotcha (verified on this machine, 2026-08-29):** `go env GOPATH` is `/Users/keith/.asdf/installs/golang/1.26.7/packages` (asdf-managed), so the fresh binary lands in `…/packages/bin/wt` — but `which -a wt` resolves **only** to `/Users/keith/.local/bin/wt` (the repeated identical lines in `which -a wt` output are PATH repeats of the same binary, not four installs), because `~/.local/bin` comes first in PATH and the asdf packages `bin` dir carries no `wt` at all (checked: `ls "$(go env GOPATH)/bin/wt"` → `No such file or directory`). The stale binary is dated 2026-08-27 (guide 06's pre-registry-generation build). After rebuilding into GOPATH the shadowed stale binary keeps answering `wt` — this is exactly what guide 06's "the installed binary is STALE" caveat describes. The fix that actually changes what runs: build straight over the shadowing copy (`cd /Users/keith/github/ohanaverse/local-ai-setup/wt && go build -o /Users/keith/.local/bin/wt ./cmd/wt`) or delete the stale one and ensure the asdf GOPATH bin is on PATH; then re-check `which wt` and `wt --version` **and** `ls -la` the file mtime.

**LiteLLM** — real syntax verified via `uv tool upgrade --help` (the tool accepts a package spec, so the extra-quoted name is valid):

<!-- UNVERIFIED — upgrade not run in this session (Litellm already current: 1.98.0). -->

```bash
uv tool upgrade 'litellm[proxy]'
```

Verify, then expect the proxy to need a kickstart (item 2 below):

```bash
litellm --version
curl -s -m 2 http://localhost:4000/v1/models -o /dev/null -w "%{http_code}\n"   # 401 = proxy alive
```

```text
LiteLLM: Current Version = 1.98.0
401
```

Two things upgrade can break (guide 01 §6/§7):

1. **The tool env is re-created** — anything pip-installed into it afterwards disappears. On this machine that is `prisma` + `prisma-client-py` (verified present in `/Users/keith/.local/share/uv/tools/litellm/bin/`), which the proxy needs for the Postgres-backed Admin UI. After any `uv tool upgrade` **or** `uv tool install --force --reinstall 'litellm[proxy]'` (the nuclear fallback; `--force`/`--reinstall` flags verified via `uv tool install --help`), re-check:

   ```bash
   ls /Users/keith/.local/share/uv/tools/litellm/bin/ | grep prisma
   ```

   ```text
   prisma
   prisma-client-py
   ```

   Empty → re-run the Prisma steps in guide 01 §6, then kickstart the proxy.

2. **The proxy must be restarted after upgrade + any config change** — `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` (§2 step 8 for the measured recovery).

**Ollama** — it is the app (`/usr/local/bin/ollama`, `com.ollama.ollama` login item), not a brew service, not a plist: update through Ollama.app's own update flow (or re-download from ollama.com), then verify the daemon came back:

<!-- UNVERIFIED — the app update itself not run in this session; the two checks below ran live, 2026-08-29, on current 0.33.2. -->

```bash
ollama --version
curl -s -m 2 http://localhost:11434/api/tags -o /dev/null -w "%{http_code}\n"
```

```text
ollama version is 0.33.2
200
```

**brew-managed tools** — upgrade then restart the affected service (llama.cpp is brew-*installed* but **not** brew-*serviced* on this machine — its plist is hand-rolled, see §1's `brew services list`):

<!-- UNVERIFIED — upgrade not run in this session (mutates Homebrew state). -->

```bash
brew upgrade llama.cpp omlx postgresql@16 redis
```

Verify after with a command run live, 2026-08-29 (current versions as of writing):

```bash
brew list --versions llama.cpp omlx postgresql@16 redis
```

```text
llama.cpp 0.3.0
omlx 0.6.3rc3
postgresql@16 16.15
redis 8.10.1
```

New numbers should appear there, and the TL;DR block must be green again (`brew services list` should still read `started` for omlx/postgresql@16/redis; for llama.cpp use the `:8080` curl).

### 5. Benchmark leftovers — restore the stack, recognize residue

After any benchmark work (guide 05), make sure the stack was restored. `modelman benchmark run` isolates and restores internally (in a `finally`), so a completed clean run leaves nothing behind — manual restore is needed when you called `bin/llm-isolate-provider` by hand, or the run was hard-killed (SIGKILL/SIGTERM; Ctrl-C still restores):

<!-- UNVERIFIED — restarting live services; not run in this session. Expected output per the script's verified contract (guide 05): -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
bin/llm-restore-providers
```

```text
[llm-restore-providers] providers restored
```

It restarts all four providers in parallel, skips the ones already answering, and exits 1 if any fails to come back.

**Isolation residue fingerprint:** after a benchmark (or a hard-killed run), ALL THREE ports answer — `4000(litellm):401` still answers, :11434 always answers 200 (the ollama daemon stays up; `ollama ps` is header-only unless an ollama isolation loaded a model), and the isolated target is whichever of :8000/:8080 is alive; only the non-isolated one of :8000/:8080 goes dark (TL;DR block shows e.g. `8000(omlx):200` with `8080(llama.cpp)` dead/curl-erroring and `11434(ollama):200`). The discriminator: `local.llamacpp.server` missing from `launchctl list` = ollama/omlx residue (llamacpp's plist was unloaded); present = llamacpp residue. For an `ollama` isolation: :11434 answers with a loaded row in `ollama ps` (that's the residue) while :8000/:8080 both refuse — the healthy stack's `ollama ps` prints the bare header only (run live, 2026-08-29). Cure: `bin/llm-restore-providers`, then re-run the TL;DR block. One asymmetry: a llama.cpp residue won't self-heal — the job was `launchctl unload`ed deliberately (it disappears from `launchctl list` entirely), so there is nothing for launchd to restart; only a restore or `launchctl load -w /Users/keith/Library/LaunchAgents/local.llamacpp.server.plist` brings :8080 back.

<!-- UNVERIFIED — the residue states above were not induced in this session (inducing them means stopping live backends); the detection commands and the loaded-header `ollama ps` output are the same ones verified live in the healthy state (header only, no rows). -->

## Verification

Everything this guide promises reduces to the TL;DR block being green — no destructive simulation is needed to verify it, and none was used. On the healthy 2026-08-29 stack, the block's verbatim output is pasted in TL;DR above; re-running it must reproduce: `401` on :4000, `200` on the three backend ports, six launchd rows with live PIDs (Ollama's row legitimately shows `-`), `accepting connections`, `PONG`.

Two non-block checks this guide also ran live and are worth repeating after maintenance:

```bash
launchctl list | grep litellm   # PID present; middle column 0 (or -15 post-kickstart)
```

```text
96295	-15	local.litellm.proxy
```

```bash
python3 -c "import yaml;d=yaml.safe_load(open('/Users/keith/.config/litellm/config.yaml'));print('model_list entries:',len(d['model_list']))"
```

```text
model_list entries: 11
```

(Count matches the 11 ids the auth'd `/v1/models` list returns — Steps §2 step 1.)

## Gotchas

- **KeepAlive turns "it crashed" into "it loops".** Every service here has `KeepAlive=true`, so after a config typo launchd respawns the failing job immediately and indefinitely — the port never comes up and `launchctl list` may even look normal (fresh PID). Check the logs (§3), not just the ports.
- **Use `launchctl kickstart -k gui/$(id -u)/<label>` to restart.** With `KeepAlive=true`, bare `launchctl stop` (or killing the PID) just gets the job relaunched — you cannot hold a service down that way, and it does not re-read anything in a controlled way. `kickstart -k` is the verified, one-shot kill+restart used by all the guides; for the brew trio use `brew services restart <name>`.
- **The `launchctl list` middle column reads `0` or `-15` here — both healthy.** `0` = last exit was clean; `-15` = last exit was from SIGTERM, i.e. normal residue after a `kickstart -k` (live example above). What's *not* healthy: `-` PID with a non-zero status, or a PID that changes while the port stays dead (§3).
- **There is no Ollama LaunchAgent.** `launchctl list | grep ollama` shows `-	0	com.ollama.ollama` because the row comes from the app's login item — you cannot `kickstart` Ollama back; relaunch Ollama.app. (The `application.com.electron.ollama.*` row appears only while the app window lives — don't grep for it in scripts.)
- **Run modelman from the `modelman/` directory.** modelman is not installed globally (only `download` would be available from an old install; guide 02 Gotchas) — always `# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman` + `uv run modelman …`.
- **`wt`'s GOPATH build vs PATH binary.** A GOPATH build writes `$(go env GOPATH)/bin/wt` (asdf: `/Users/keith/.asdf/installs/golang/1.26.7/packages/bin/wt`), but PATH resolves `wt` to `/Users/keith/.local/bin/wt` first — checked live: `which -a wt` lists only the `~/.local/bin` path, and the asdf GOPATH bin currently holds no `wt`. Rebuilding into GOPATH therefore leaves the stale 2026-08-27 binary in charge. Build over `~/.local/bin/wt` (or evict it) and re-verify `which wt` before trusting a post-build `wt` (guide 06's stale-binary gotcha is the same story from the model-catalog side).
- **Upgrades recreate the LiteLLM tool env** — recheck `prisma` in `/Users/keith/.local/share/uv/tools/litellm/bin/` after `uv tool upgrade` or `--force --reinstall`, or the Postgres-backed UI/auth features die on the next kickstart (guide 01 §6).

## Going deeper

- Full install, plist templates, and the secret-redaction rules (plist `EnvironmentVariables`, config.yaml `api_key`s): [01-initial-setup](01-initial-setup.md)
- config.yaml anatomy, modelman exposure mechanics, and the same kickstart with measured recovery: [04-litellm-config](04-litellm-config.md)
- Benchmark isolation/restore contracts behind §5, and what a clean restore guarantees: [05-benchmarks](05-benchmarks.md)
- The actual source of truth for everything §1 asserts (start-at-load, keep-alive, log paths, env keys): `~/Library/LaunchAgents/` — read the plist before guessing about any service
