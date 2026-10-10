# Maintenance and troubleshooting — health check, restarts, log triage, upgrades

> Use this to: keep the running stack healthy day-to-day — one health-check block, per-service restart/repair, crash-loop triage, and safe upgrades.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

- Full stack installed and initially configured per [01-initial-setup](01-initial-setup.md) — the LaunchAgents exist and load (`~/Library/LaunchAgents/`: `local.litellm.proxy.plist`, `homebrew.mxcl.postgresql@16.plist`, `homebrew.mxcl.redis.plist`; **`homebrew.mxcl.omlx.plist` is optional since the 2026-09-30 rebuild** — wt's and llmbench's lifecycle backends run `omlx start` on demand, and the rebuild omits it; `brew services start omlx` restores it if you want oMLX always-on) — llama.cpp's plist was retired 2026-09-07 (see [provider-artifacts.md](../reference/provider-artifacts.md))
- This repo checked out — `llmbench` (the `provider isolate`/`provider restore` CLI, guide 05) lives at `llmbench/`.
- Every restart command below assumes your terminal user is the one whose launchd domain owns the agents (`gui/$(id -u)`), i.e. a normal logged-in session, not an SSH-into-a-different-user session.

## TL;DR

Full health check — every answer is read-only, safe to run any time. Run it after any change; the whole block ran live on 2026-08-29 and output below is verbatim:

```bash
curl -s -m 2 http://localhost:4000/v1/models -o /dev/null -w "4000(litellm):%{http_code}\n"   # 401 = proxy up, demanding key
curl -s -m 2 http://localhost:8000/health -o /dev/null -w "8000(omlx):%{http_code}\n"         # /health — plain / gives 404
curl -s -m 2 http://localhost:11434/api/tags -o /dev/null -w "11434(ollama):%{http_code}\n"
launchctl list | grep -E 'litellm|omlx|postgresql|redis|ollama'
pg_isready -h localhost
redis-cli ping
```

```text
4000(litellm):401
8000(omlx):200
11434(ollama):200
-	0	com.ollama.ollama
96295	-15	local.litellm.proxy
80374	0	homebrew.mxcl.postgresql@16
97297	0	homebrew.mxcl.omlx
88057	0	homebrew.mxcl.redis
17810	0	application.com.electron.ollama.2312009772.2312009778.64DD861F-BA99-4B1C-A478-2478B317DA0D
localhost:5432 - accepting connections
PONG
```

Reading it fast:

- 401 on `:4000` = healthy (proxy up, correctly refusing keyless requests); 200 elsewhere.
- `launchctl list` columns are PID / last-exit-status / label. Same two keys are `true` in all four remaining plists.
- `local.litellm.proxy` shows status `-15` here because it was SIGTERMed by a `kickstart -k` earlier that day (see Gotchas — `0`/`-15` are the only healthy readings for the middle column).
- Ollama row has no PID (`-`) and no LaunchAgent plist exists for it — the `com.ollama.ollama` login item (Ollama.app) owns the daemon; the `application.com.electron.ollama.*` row appears only while the app window is open.
- Any line that differs → §1 for restart mechanics, §3 for logs.
- Looking for a `modelman` command? §6 has the `wt` or `llmbench` command for each.
- Cleaning up after modelman? §7 lists the files it left and what to check first.

## Steps

### 1. After a reboot: what auto-starts, what needs a kick

Read the plists, not memory. All four launchd jobs carry both keys (verified live on 2026-08-29):

```bash
grep -A1 'RunAtLoad\|KeepAlive' ~/Library/LaunchAgents/local.litellm.proxy.plist /Users/keith/Library/LaunchAgents/homebrew.mxcl.omlx.plist
```

```text
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

So after login: LiteLLM (:4000), oMLX (:8000), Postgres, Redis all come up on their own. KeepAlive also means **if any of them crash, launchd restarts them automatically**; a service that stays down means it is crash-looping against KeepAlive, not waiting for you (go to §3).

Ollama is the exception: **no plist exists** — the daemon is owned by the Ollama.app login item (`com.ollama.ollama`), which starts at login only if the app is enabled as a login item and launches. When :11434 is dead after a reboot, open Ollama.app and wait a few seconds.

Run `brew services list` to see which of your jobs brew considers its own (this is the trio you manage via `brew services`; LiteLLM is a hand-rolled plist outside brew):

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
| oMLX :8000 | `200` on `/health` | `omlx restart` or `brew services restart omlx` | `/opt/homebrew/var/log/omlx.log` |
| Ollama :11434 | `200` on `/api/tags` | relaunch Ollama.app (no plist — launchd does not own it) | `/Users/keith/.ollama/logs/server.log` |
| Postgres 5432 | `pg_isready -h localhost` | `brew services restart postgresql@16` | `/opt/homebrew/var/log/postgresql@16.log` |
| Redis 6379 | `redis-cli ping` | `brew services restart redis` | `/opt/homebrew/var/log/redis.log` |

Whole stack in one shot (post-download, post-upgrade): `uv run --directory llmbench llmbench provider restore` (from the repo root) starts ollama, oMLX and the LiteLLM proxy when they are not answering, and stops mtplx and mlx_lm_server. It leaves a service that already answers alone, so to bounce one use its restart command from the table above.

### 2. "Model missing from LiteLLM" — ordered debug flow

Worked on a model you expect on `:4000` but don't see (or a client gets 404/`model not found` for). Substitute your model's id for `<model-id>` throughout. Steps (1–3, 5–7) ran live read-only on 2026-08-29 against a then-healthy model; the outputs below are trimmed, labeled examples of what clean looks like at each stage — your ids will differ; step 4 is mutating (not run; marked UNVERIFIED); step 8 restarts the proxy — the one mutating step run, measured below.

**Step 1 — is it in the proxy at all?** Without auth you learn nothing (`401`); pull the master key from the plist env (value stays un-printed) and list model ids:

```bash
LITELLM_MASTER_KEY=$(awk '/<key>LITELLM_MASTER_KEY<\/key>/{getline; sub(/.*<string>/,""); sub(/<\/string>.*/,""); print}' /Users/keith/Library/LaunchAgents/local.litellm.proxy.plist)
curl -s http://localhost:4000/v1/models -H "Authorization: Bearer $LITELLM_MASTER_KEY" | python3 -c 'import json,sys; [print(m["id"]) for m in json.load(sys.stdin)["data"]]'
```

Output is one model id per line. Example (illustrative — your ids will differ):

```text
ollama/qwen3.8:27b-mlx
openrouter/qwen/qwen3.8-27b
```

Local-model routes appear only while that model runs — wt writes the route on start and, for omlx/mtplx/mlx_lm_server, removes it on stop. **ollama is the exception**: a pulled ollama model stays routed whether or not it is loaded (stopping it only unloads it; ollama loads it again on the next request). Present in this list but not routeable? Skip to step 5 (backend down). Absent? Continue.

**Step 2 — does `model_list` in config.yaml carry it?** config.yaml is what the proxy routes from:

```bash
grep -n 'model_name:' /Users/keith/.config/litellm/config.yaml
```

Output is one `<line>:  - model_name: <id>` row per routed model. Example (illustrative — your ids and line numbers will differ):

```text
3:  - model_name: ollama/qwen3.8:27b-mlx
12:  - model_name: openrouter/qwen/qwen3.8-27b
```

`wt litellm list` shows the same routing from wt's side.

Present here but absent from `:4000`? config.yaml changed since the proxy last started → jump to step 7 (restart). Absent here too? Continue.

**Step 3 — is the model on disk and running?** Ask wt's live inventory:

```bash
wt model list
```

Read the row for the id. STATUS `ok` means on disk, `missing` means the registry names it and no probe finds it, `new` means on disk with no registry entry (`unknown`: the provider could not be asked). RUNNING `run` or `load` means the model is serving (`load`: omlx is still loading it), which is what a local model's route depends on. Routing itself is recorded nowhere but `config.yaml`, and `wt litellm list` reads it back. What to do, by kind of model:

- **Local model** (omlx/mtplx/mlx_lm_server): its route exists only while it runs, so an on-disk model with no route is simply a stopped model. Start it — the route appears: `wt start <model-id>` (an `mlx_lm_server` pairing: Step 9). A model that is running with no route was started outside wt: launching it through wt, or `wt start <model-id>`, writes the route (Step 10). A model *missing from wt's picker* is a different question: wt lists only what its live probes of ollama/omlx/mtplx find — plus a running `mlx_lm_server` pairing, which has no artifact scan (a stopped one is not listed) — so a local model that is not on disk is not listed at all — download it with the provider's own tool (`ollama pull`, `hf download` into omlx's model directory, `mtplx pull` — [02-providers-and-models](02-providers-and-models.md) Step 5). Run `wt -A <agent>` and read the STATUS/RUNNING columns: a non-running local model on disk is a start row, and a model with no registry entry is listed as `new` (hidden while `-T`/`-F` is set).
- **Ollama model**: routed while it is *pulled* — `ollama list` must show it (and the daemon must be answering, or `wt litellm sync` leaves ollama's routes alone with a warning; a daemon that refuses the connection gets its local routes removed). A pulled ollama model with no route usually means no sync has run since the pull: run `wt litellm sync`. Launching the model through wt, or `wt start <model-id>`, also writes the route once the model is running (Step 10); `wt litellm sync` stays the fix for a client that reaches the proxy without wt.
- **Cloud model** (openrouter, or `location = "cloud"`): there is nothing to download or start — wt routes a cloud model whenever `registry.toml` configures it, so a missing route means no sync has run since it was added (a hand edit of `registry.toml`; `wt model add` syncs for itself) or the last sync could not build its row. `wt litellm sync --dry-run` tells them apart: `<model-id>: would route` for the first, a per-id error for the second (a `secret_ref` that `resolved empty`, or an empty `model_name`). If the id is not in the plan at all, its provider has no LiteLLM mapping (check `wt litellm providers`) — such a model is never routed.

**Step 4 — give it a route.** There is no per-model routing command (`wt litellm expose` was removed in #179). A route comes from the registry plus the model's live state, so the two moves are a start and a sync:

```bash
wt start <model-id>                     # local model: load it; the start writes its route
wt litellm sync --dry-run               # any model: what the next sync would route, and any per-id error
wt litellm sync                         # write it (restarts the proxy only if config.yaml changed)
```

```text
wt: <model-id> is running
```

A *cloud* model that is not in `registry.toml` cannot be routed by wt at all — add it first (guide 02). A *local* model needs no registry entry: one that is on disk is started by its discovered id `<family>/<artifact>` (`wt model list` shows them as `new`), and is routed under that id — only an `mlx_lm_server` pairing must be registered. For a local model, starting it is the durable fix: the next sync removes a stopped omlx/mtplx/mlx_lm_server model's route again.

Bad ids refuse instead — `wt: unknown model "<id>"` on stderr, exit 1 — meaning the id from Steps 1–3 never existed; fix the id, not the config.

**Step 5 — backend port actually up?** The `api_base` in the model's row points at a backend; probe the one that owns your model (both ran live, 2026-08-29):

```bash
curl -s -m 2 http://localhost:11434/api/tags -o /dev/null -w "11434(ollama):%{http_code}\n"
curl -s -m 2 http://localhost:8000/health -o /dev/null -w "8000(omlx):%{http_code}\n"
```

```text
11434(ollama):200
8000(omlx):200
```

A dead backend is a §1/§3 problem, not a config problem — fix the backend first.

**Step 6 — api_base right?** The row must point the proxy at the right port:

```bash
grep -A3 'model_name: <model-id>' /Users/keith/.config/litellm/config.yaml
```

Example (illustrative — an ollama-served row):

```text
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx
      api_base: http://localhost:11434
```

OpenAI-compatible local backends (omlx, omlx-6bit, mtplx, mlx_lm_server) must have `api_base` ending in `/v1`, because LiteLLM's `openai/` provider appends only `/chat/completions`. Without it, oMLX answers `404 {'detail': 'Not Found'}` even though the model is loaded and `wt start`'s warmup passed. wt has written `<origin>/v1` since #168 (PR #173); a row written by an older wt is repaired by `wt litellm sync`.

(OpenRouter rows use `api_base: https://openrouter.ai/api/v1` and carry the key resolved from the provider's `secret_ref` — guide 04 §1.)

**Step 7 — config.yaml still valid YAML?** A hand-edit typo keeps the proxy in a crash loop (it re-reads only at start):

```bash
python3 -c "import yaml;d=yaml.safe_load(open('/Users/keith/.config/litellm/config.yaml'));print('model_list entries:',len(d['model_list']))"
```

Output is one line, `model_list entries: <N>`. A traceback instead means a parse error → fix it by hand (wt refuses to write a file it cannot parse, so `wt litellm sync` cannot repair it); once it parses, `wt litellm sync` rebuilds any wt row you deleted (wt does atomic, comment-preserving writes, guide 04 §1). `<N>` is however many rows `config.yaml` carries — compare it with the number of ids Step 1's authenticated `/v1/models` call returns (pipe that command into `wc -l`); after a restart (Step 8) the two should match.

**Step 8 — restart, then re-check Step 1.** The proxy reads config.yaml only at start:

```bash
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy && echo "kickstart OK"
```

```text
kickstart OK
```

Measured live 2026-08-29 (this session): old PID `65475` → new PID `96295`; the port refused connections and answered `401` again after **7 s**. Guides 01/04 measured 9–15 s on earlier runs — plan for a ~10–20 s dead window and confirm with the Step 1 curl rather than assuming. Everything else uses the mechanics in the §1 table.

**Step 9 — omlx/mtplx/mlx_lm_server route dropped by `wt litellm sync`? Start the model.** (Observed 2026-09-30, rebuild session; updated for #179.) `wt litellm sync` has no ready gate and routes exactly the local models that are *running* at that moment — plus, for ollama, every model that is *pulled*, registered or not — and every other local model wt owns a row for loses it (sync prints a `<model-id>: unrouted` line for each; preview with `wt litellm sync --dry-run`). "Running" is a live probe (`wt/internal/localmodels/sources.go`); "pulled" is ollama's `/api/tags`, with the daemon fully answering (`wt/cmd/wt/litellm.go` `desiredLocalIDs`). Downloading an omlx/mtplx artifact does not route it. The durable fix is to start the model:

```bash
wt start <model-id>   # omlx, mtplx (and ollama); the start writes the route
# an mlx_lm_server pairing — wt has no backend for one:
uv run --directory llmbench llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>
wt litellm sync
```

A model that is already running — one you started by hand — gets its route back from `wt start <model-id>` or from the next launch through wt (Step 10).

A model with no registry entry is routed the same way, under its discovered id `<family>/<artifact>` (`wt litellm sync --dry-run` lists it as `<id>: would route`). A `(hand-written)` row in `wt litellm list` whose name equals a discovered model's id (an alias you wrote before #179, say) keeps serving that name: wt never replaces it, so delete the row from `config.yaml` if you want wt's marked row, then run `wt litellm sync`.

**Step 10 — the model is running, but something else started it? wt writes the route when you launch it; a client that skips wt needs `wt litellm sync`.** (A known gap in #179 Phase B, closed by #192.) A route is written when *wt* starts a model, or by a sync. A local model — registered or discovered — that something else started (an omlx or mtplx model you loaded by hand with the provider's own CLI, say) with no sync since is `run` in wt's picker but has no row yet. wt checks for that when it hands a running local model to an agent through LiteLLM — a `-M` pin, rotation, the picker — and in `wt smoke` and `wt start <model-id>`: it writes the model's route if it is missing, prints `wt: LiteLLM route for <id> updated` on stderr, and waits for the proxy restart before going on. The check only adds that one route: it never starts or stops a model, never removes a route (a stale one is left to the next start, stop or `wt litellm sync`), never replaces a hand-written row whose name is not a registry model id (one named like a registry id is adopted as wt's own row, as a sync does — [04-litellm-config](04-litellm-config.md) Gotchas), and never fails the launch. A client that reaches the proxy without wt gets no such check, and until a route is written it gets the proxy's `400 Invalid model name`:

```bash
wt litellm list | grep -x '<model-id>'   # no line = no route
wt litellm sync --dry-run                # expect: <model-id>: would route
wt litellm sync                          # writes the row and restarts the proxy; give it ~10–20 s
```

`wt start <model-id>` does the same for that one model: for a model that is already running it writes the route if it is missing, then prints `wt: <model-id> is already running`. If a launch through wt still ends in `Invalid model name`, look on stderr for `wt: LiteLLM route not updated: …` (the write failed). Launched from the `wt` picker, the check shows an "Updating the LiteLLM route" screen while the proxy restarts, and that line and any route warning are printed on the terminal when the agent starts (or after wt exits, if the launch did not happen). They are not shown in the picker's status line. If the launch itself fails, the status line shows the launch error, and the route line is printed on the terminal when wt exits. Also check `wt litellm list` for a `(hand-written)` row under that name: wt leaves it in place when the name is a discovered model's id (Step 9), and adopts it as its own row when the name is a registry model id ([04-litellm-config](04-litellm-config.md) Gotchas). One case the check cannot see: the row is listed in `wt litellm list` but the proxy is stale, because a restart failed or was interrupted — restart the proxy (Step 8).

### 3. Log triage

Log homes (all from live plists / on-disk checks, 2026-08-29):

| Service | Log file(s) | Notes |
|---------|-------------|-------|
| LiteLLM | `/Users/keith/.litellm.err.log`, `/Users/keith/.litellm.log` | two files (stderr/stdout); launchd appends — the file only grows |
| oMLX | `/opt/homebrew/var/log/omlx.log` | single file, both streams; `omlx diagnose` for install/runtime issues |
| Postgres | `/opt/homebrew/var/log/postgresql@16.log` | single file |
| Redis | `/opt/homebrew/var/log/redis.log` | single file |
| Ollama | `/Users/keith/.ollama/logs/server.log`, `app.log` | app-managed; rotated as `server-1.log` … `server-5.log` |

Crash-loop recognition — with `KeepAlive = true`, a service that dies on startup is relaunched by launchd immediately, forever. Look for the signature in `launchctl list`:

```bash
launchctl list | grep -E 'litellm|omlx|postgresql|redis'
```

```text
96295	-15	local.litellm.proxy
80374	0	homebrew.mxcl.postgresql@16
97297	0	homebrew.mxcl.omlx
88057	0	homebrew.mxcl.redis
```

- **Healthy running:** a PID + `0` (or `-15` right after a kickstart — see Gotchas). E.g. the `local.litellm.proxy` row above (live).
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

**wt** — rebuild over the PATH copy (`~/.local/bin/wt`), not GOPATH; the gotcha below explains why:

<!-- UNVERIFIED — build not run in this session (writes a binary); checkout was bf98d14, 2026-08-29. -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/wt
# (in the merged monorepo; wt lives at local-ai-setup/wt/)
cd /Users/keith/github/ohanaverse/local-ai-setup/wt
git pull && go build -o /Users/keith/.local/bin/wt ./cmd/wt
```

Upgrading wt across #179 (the `wt_managed` route marker)? Read [04-litellm-config](04-litellm-config.md) §2 *Upgrading from a pre-#179 wt* and *Upgrading to discovered local models (#179 Phase B)*, and run `wt litellm sync --dry-run` before the first `wt model add|edit|rm`, `wt model init` or `wt cloud-sync` triggers the first real sync.

Verify — `wt --version` ran live, 2026-08-29:

```bash
wt --version
```

```text
wt 0.1.0
```

**Gotcha (verified on this machine, 2026-08-29):** `go env GOPATH` is `/Users/keith/.asdf/installs/golang/1.26.7/packages` (asdf-managed), so the fresh binary lands in `…/packages/bin/wt` — but `which -a wt` resolves **only** to `/Users/keith/.local/bin/wt` (the repeated identical lines in `which -a wt` output are PATH repeats of the same binary, not four installs), because `~/.local/bin` comes first in PATH and the asdf packages `bin` dir carries no `wt` at all (checked: `ls "$(go env GOPATH)/bin/wt"` → `No such file or directory`). The stale binary is dated 2026-08-27 (guide 06's pre-registry-generation build). After rebuilding into GOPATH the shadowed stale binary keeps answering `wt` — this is exactly what guide 06's "the installed binary is STALE" caveat describes. The fix that actually changes what runs: build straight over the shadowing copy (`cd /Users/keith/github/ohanaverse/local-ai-setup/wt && go build -o /Users/keith/.local/bin/wt ./cmd/wt`) or delete the stale one and ensure the asdf GOPATH bin is on PATH; then re-check `which wt` and `wt --version` **and** `ls -la` the file mtime.

**LiteLLM** — `uv tool upgrade` takes the tool's name (`uv tool upgrade --help`). The extras and the Python version are the ones recorded at install, in `~/.local/share/uv/tools/litellm/uv-receipt.toml`; check that file names `proxy`, `extra-proxy` and Python 3.11 before upgrading, and if it does not, use the reinstall form in item 1 instead:

<!-- UNVERIFIED — the upgrade itself was not run when this was written; the receipt was read. -->

```bash
uv tool upgrade litellm
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

1. **The tool env is re-created** — anything pip-installed into it afterwards disappears. On this machine that is `prisma` + `prisma-client-py` (verified present in `/Users/keith/.local/share/uv/tools/litellm/bin/`), which the proxy needs for the Postgres-backed Admin UI. After any `uv tool upgrade` **or** `uv tool install --force --reinstall --python 3.11 'litellm[proxy,extra-proxy]'` (the nuclear fallback, with the extras and Python of guide 01 §1: bare `litellm[proxy]` drops Prisma; `--force`/`--reinstall` flags verified via `uv tool install --help`), re-check:

   ```bash
   ls /Users/keith/.local/share/uv/tools/litellm/bin/ | grep prisma
   ```

   ```text
   prisma
   prisma-client-py
   ```

   Both names listed is not the whole check: the generated client lives inside the tool env too, so re-run guide 01 §6's `prisma generate` after any upgrade or reinstall, then kickstart the proxy. Empty → the `extra-proxy` extra is missing; reinstall with the command above first.

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

**brew-managed tools** — upgrade then restart the affected service:

<!-- UNVERIFIED — upgrade not run in this session (mutates Homebrew state). -->

```bash
brew upgrade omlx postgresql@16
brew upgrade --build-from-source redis   # redisearch.so bottle pour EPERMs on this machine (Gotchas) — 2026-09-30
```

Verify after with a command run live, 2026-08-29 (current versions as of writing):

```bash
brew list --versions omlx postgresql@16 redis
```

```text
omlx 0.6.3rc3
postgresql@16 16.15
redis 8.10.1
```

New numbers should appear there, and the TL;DR block must be green again (`brew services list` should still read `started` for omlx/postgresql@16/redis).

### 5. Benchmark leftovers — restore the stack, recognize residue

After any benchmark work (guide 05), make sure the stack was restored. `llmbench run` isolates and restores internally (in a `finally`), so a completed clean run leaves nothing behind — manual restore is needed when you called `llmbench provider isolate` by hand, or the run was hard-killed (SIGKILL/SIGTERM; Ctrl-C still restores):

<!-- UNVERIFIED — restarting live services; not run in this session (this host currently has omlx down on an unrelated Xcode-CLT license gate, which would make a live restore hang/fail for reasons unrelated to this doc). Output below is read from the CLI's source (`src/llmbench/providers/lifecycle/cli.py::restore_cmd`), not captured from a live run: -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/llmbench
uv run llmbench provider restore
```

```text
restored providers
```

(`--json` instead prints the `{"provider": "restore", "model": "", "direct_url": "", "ok": ..., "error": ...}` envelope.) It restarts ollama, omlx and the LiteLLM proxy in parallel, skips the ones already answering, and exits 1 if any fails to come back. It also stops a running mtplx model and a running mlx_lm_server pairing: neither is part of the standing baseline, and a stop that fails is only a warning.

**Isolation residue fingerprint:** after a benchmark (or a hard-killed run), the two local backend ports answer — `4000(litellm):401` still answers, :11434 always answers 200 (the ollama daemon stays up; `ollama ps` is header-only unless an ollama isolation loaded a model), and the isolated target is whichever of :11434/:8000 is alive; only the non-isolated one goes dark. For an `ollama` isolation: :11434 answers with a loaded row in `ollama ps` (that's the residue) while :8000 refuses — the healthy stack's `ollama ps` prints the bare header only (run live, 2026-08-29). For an `omlx` isolation: :8000 answers while `ollama ps` is header-only. Cure: `uv run llmbench provider restore`, then re-run the TL;DR block.

<!-- UNVERIFIED — the residue states above were not induced in this session (inducing them means stopping live backends); the detection commands and the loaded-header `ollama ps` output are the same ones verified live in the healthy state (header only, no rows). -->

### 6. Where modelman's commands went

modelman, the Python model manager this repo used to ship, was retired into
`wt` and deleted. `uv run --directory modelman modelman` fails because the
directory is gone; that is expected. Each thing it did:

| modelman | Now |
|---|---|
| TUI (`a`, `e`, `d` keys) | `wt config` Models tab, or `wt model add\|edit\|rm` |
| `start` with no argument | `wt model list` |
| `start <id>`, `stop <id>` | `wt start <id>`, `wt stop <id>` |
| `stop --all` | `wt stop --all`, and for a running `mlx_lm_server` pairing, which wt does not stop, `uv run --directory llmbench llmbench provider stop mlx_lm_server` |
| `start <mlx_lm_server pairing>` | `uv run --directory llmbench llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`, then `wt litellm sync` to route it |
| `stop <mlx_lm_server pairing>` | `uv run --directory llmbench llmbench provider stop mlx_lm_server`, then `wt litellm sync` to drop its route |
| `sync` | `wt model init` for provider rows; nothing for state — wt probes disk and servers each time |
| `refresh-prices`, `ollama-catalog sync` | `wt cloud-sync` |
| `usage report` | `wt stats` |
| `usage report --days N`, the Markdown report, its Reconciliation sections and "Last wt launch" line | dropped — `wt stats --window 1d\|7d\|30d`, `wt stats --json` ([07-usage-and-spend](07-usage-and-spend.md)) |
| `litellm …` | `wt litellm …` |
| `benchmark …`, `provider …` | `uv run --directory llmbench llmbench …`, `… llmbench provider …` |
| `migrate`, `delete-family`, TUI `r` (download, delete), `l`, `s` | dropped — download with the provider's own tool (guide 02 Step 5); `wt litellm on\|off`, `wt start` and `wt stop` cover `l` and `s` |

llmbench is not installed on `PATH`. Where wt prints a command that starts
with `llmbench provider …` (the start hint of a stopped pairing, the text of
`wt stop --help`), run it from the repo root with
`uv run --directory llmbench` in front, as in the table.

`MODELMAN_REGISTRY`, `MODELMAN_LITELLM_CONFIG`, `MODELMAN_LITELLM_RESTART_CMD`
and `MODELMAN_LITELLM_DATABASE_URL` still work: wt reads each after its `WT_`
name. `MODELMAN_BENCHMARK_WORKLOAD` and `MODELMAN_AGENT_DEBUG` still work in
llmbench, after `LLMBENCH_WORKLOAD` and `LLMBENCH_AGENT_DEBUG`. No other
`MODELMAN_*` variable is read by anything.

### 7. Files modelman left behind

Nothing reads these any more: neither `wt` nor `llmbench` opens any of them,
and wt never deletes them. Remove them by hand when you are ready. **Some of
them hold a secret:** do not `cat` them in a shared terminal or paste them
into an issue.

| File | Holds a secret? | What it was |
|---|---|---|
| `~/.config/local-ai/modelman.toml` | **Yes** — its `[litellm]` table has the LiteLLM API key | modelman's per-machine state |
| copies of it that you or an old runbook made (`modelman.toml.bak-*`, or any other name) | **Yes**, the same key | hand-made backups; modelman never wrote one, so check each name before deleting |
| `.modelman.toml.*.tmp` (the name starts with a dot) | **Yes**, the same key | left by a modelman write that crashed; usually absent |
| `~/.config/local-ai/config.yaml` — modelman's legacy file, **not** LiteLLM's `~/.config/litellm/config.yaml` | Check: `grep -c -i 'api_key\|secret' ~/.config/local-ai/config.yaml` (a number above 0 means treat it as one) | provider types from before `registry.toml` |
| `~/.config/local-ai/settings.yaml` | No | the TUI's theme |
| `~/.config/local-ai/families/` | No | per-family variants and download markers from before `registry.toml` |
| `.registry.toml.*.tmp` (the name starts with a dot) | No | left by a modelman write of the registry that crashed; usually absent |

**Leave these alone**, in the same directory, because they are in use:

- `registry.toml` — the model list wt and llmbench read.
- `registry.toml.lock` — the lock wt takes before it writes the registry.
- `registry.toml.<random>.tmp`, with **no** leading dot — a wt write in
  progress.
- `benchmarks/` and everything in it, `benchmarks/latest.toml` included —
  llmbench's results and its latest-run pointers.

**Where the files are.** The paths above are the defaults. If you ran
modelman with `XDG_CONFIG_HOME` set, `modelman.toml` (and `registry.toml`)
are under `$XDG_CONFIG_HOME/local-ai/` instead, while `settings.yaml`,
`config.yaml`, `families/` and `benchmarks/` stay under `~/.config/local-ai/`.
A `MODELMAN_STATE` export in your shell profile named yet another place for
`modelman.toml`: look there too, then delete the export. Nothing reads that
variable now, llmbench included.

Before deleting anything, three checks:

```bash
ls ~/.config/local-ai/registry.toml             # the model list wt uses
wt litellm status                               # routing state wt holds in its own config.toml
ls ~/.config/local-ai/benchmarks/latest.toml    # llmbench's latest-run pointers
```

- **No `registry.toml`** (a machine that never ran `modelman migrate`):
  modelman's `config.yaml` and `families/` are the only record of your
  models, and nothing imports them any more. Read the model names out of
  them and add each with `wt model add`
  ([02-providers-and-models](02-providers-and-models.md) Step 1) before you
  delete the two.
- **`wt litellm status` prints `litellm: off` with `url: (unset)`, and you do
  route through LiteLLM:** wt has no copy of the routing state. wt used to
  read the `[litellm]` table of `modelman.toml` as a fallback, and copied it
  into `~/.config/agent-wt/config.toml` the first time it ran, on or after
  2026-09-21, with a `config.toml` that had no `[litellm]` table. A machine
  with no `config.toml`, or one where wt has not run since that date, never
  got the copy, and wt no longer reads the old file. Until you restore the
  state, agents dial their providers directly (nothing is logged by the
  proxy), and a launch that needs the proxy (codex, always) stops with
  `litellm routing is required for this model but no URL is configured`.
  Take the URL and key from the `[litellm]` table of `modelman.toml` and run
  `wt litellm set --url <url> --api-key <key>`, then `wt litellm on`. Both
  write `config.toml` (mode 0600), creating it if it does not exist; run
  `wt litellm status` again to see `litellm: on`. If wt then stops with
  `config error: agent "agy": unknown provider "agy"`, run `wt model init`:
  the restore created `config.toml`, and that command adds the provider row
  it needs to `registry.toml`. The key is on the command
  line, the only form wt offers, so it lands in your shell history: start
  the line with a space if your shell is set to skip such lines
  (`HISTCONTROL=ignorespace` in bash, `setopt HIST_IGNORE_SPACE` in zsh), or
  remove the line from the history file afterwards.
- **No `latest.toml`, and you want `--latest` to keep finding a run you made
  through modelman:** llmbench no longer falls back to `modelman.toml`, so
  `--latest` answers `error: no latest run recorded` (or `… agent run …`,
  `… eval run …`). Copy the four keys of the old file's `[benchmarks]` table
  (`last_run`, `last_run_dir`, `agent_last_run`, `eval_last_run`) into
  `latest.toml` as top-level keys. The results themselves are untouched
  either way, and a new run writes the file by itself.

Then:

```bash
ls -la ~/.config/local-ai/        # see what is there first
# modelman.toml moves with XDG_CONFIG_HOME — see "Where the files are" above
rm "${XDG_CONFIG_HOME:-$HOME/.config}/local-ai/modelman.toml"   # the one holding the key
rm ~/.config/local-ai/settings.yaml
rm -r ~/.config/local-ai/families
rm ~/.config/local-ai/config.yaml       # modelman's legacy file, after the check above
```

Delete each backup copy and each dot-prefixed `.tmp` file by its own name
once you have read the listing; do not use a wildcard in this directory,
which also holds `registry.toml`.

In each checkout of this repo, `git pull` removes modelman's tracked files
and leaves the untracked ones (`modelman/.venv`, caches). If
`git ls-files modelman | wc -l` prints `0`, remove the directory:
`rm -r modelman`.

## Verification

Everything this guide promises reduces to the TL;DR block being green — no destructive simulation is needed to verify it, and none was used. On the healthy 2026-08-29 stack, the block's verbatim output is pasted in TL;DR above; re-running it must reproduce: `401` on :4000, `200` on the two backend ports, five launchd rows with live PIDs (Ollama's row legitimately shows `-`), `accepting connections`, `PONG`.

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

Expect `model_list entries: <N>`, with `<N>` equal to the number of ids the auth'd `/v1/models` list returns (Steps §2 step 1 piped into `wc -l`) once the proxy has restarted since the last config change.

## Gotchas

- **KeepAlive turns "it crashed" into "it loops".** Every service here has `KeepAlive=true`, so after a config typo launchd respawns the failing job immediately and indefinitely — the port never comes up and `launchctl list` may even look normal (fresh PID). Check the logs (§3), not just the ports.
- **Use `launchctl kickstart -k gui/$(id -u)/<label>` to restart.** With `KeepAlive=true`, bare `launchctl stop` (or killing the PID) just gets the job relaunched — you cannot hold a service down that way, and it does not re-read anything in a controlled way. `kickstart -k` is the verified, one-shot kill+restart used by all the guides; for the brew trio use `brew services restart <name>`.
- **The `launchctl list` middle column reads `0` or `-15` here — both healthy.** `0` = last exit was clean; `-15` = last exit was from SIGTERM, i.e. normal residue after a `kickstart -k` (live example above). What's *not* healthy: `-` PID with a non-zero status, or a PID that changes while the port stays dead (§3).
- **A model still loaded after wt stopped it means two Ollama servers.** `wt stop` (or the stop prompt on exit) prints `done`, yet `ollama ps` still lists the model and the memory is not freed: run `pgrep -fl "ollama serve"`. Two lines means LiteLLM started a second server beside the app's, which it does at proxy startup for any ollama row in `config.yaml` with no `api_base` ([04-litellm-config](04-litellm-config.md) Gotchas). wt fills that field in on its next write; to clear it at once, kill the extra `ollama serve` (the one that is not under `Ollama.app`) and restart the proxy (§2 Step 8). Left alone, the stray copy unloads after Ollama's keep-alive, five minutes by default, which makes this look intermittent.
- **An omlx model is running only when omlx has it loaded.** The omlx service lists every model in its directory on `/v1/models` whether or not it is loaded, so that list is not "what is running"; wt asks omlx what is loaded instead (`curl -s http://localhost:8000/health` shows the pool's `model_count` and `loaded_count`). When some omlx models are loaded and others are not, only omlx's key-protected status endpoint says which: if the server has an API key, set `auth.secret_ref` on the registry's omlx provider, or `wt litellm sync` warns that the omlx probe did not succeed and leaves its routes unchanged, and a start asks before replacing.
- **There is no Ollama LaunchAgent.** `launchctl list | grep ollama` shows `-	0	com.ollama.ollama` because the row comes from the app's login item — you cannot `kickstart` Ollama back; relaunch Ollama.app. (The `application.com.electron.ollama.*` row appears only while the app window lives — don't grep for it in scripts.)
- **`wt`'s GOPATH build vs PATH binary.** A GOPATH build writes `$(go env GOPATH)/bin/wt` (asdf: `/Users/keith/.asdf/installs/golang/1.26.7/packages/bin/wt`), but PATH resolves `wt` to `/Users/keith/.local/bin/wt` first — checked live: `which -a wt` lists only the `~/.local/bin` path, and the asdf GOPATH bin currently holds no `wt`. Rebuilding into GOPATH therefore leaves the stale 2026-08-27 binary in charge. Build over `~/.local/bin/wt` (or evict it) and re-verify `which wt` before trusting a post-build `wt` (guide 06's stale-binary gotcha is the same story from the model-catalog side).
- **Upgrades recreate the LiteLLM tool env** — after `uv tool upgrade` (or a `--force` reinstall), redo it as `uv tool install --force --python 3.11 'litellm[proxy,extra-proxy]'` and re-run `prisma generate` (guide 01 §1/§6), or the Postgres-backed UI/auth features die on the next kickstart with one of: `ModuleNotFoundError: No module named 'prisma'` (Prisma ships in the `extra-proxy` extra on LiteLLM ≥1.98, not `[proxy]`) or `Unable to find Prisma binaries. Please run 'prisma generate' first.` (unpinned Python — uv picked 3.14 on the 2026-09-30 rebuild, where the engines are missing; 3.11 is known-good). Both symptoms verified 2026-09-30.
- **Redis crash-loops on `redisearch.so` on this machine (2026-09-30).** The module file is unreadable system-wide (`dlopen ... errno=1` in `/opt/homebrew/var/log/redis.log`), even source-built; the bottle pour itself can also `EPERM` on it during `brew install`/`upgrade`, so use `--build-from-source`. Fix applied in `/opt/homebrew/etc/redis.conf`: only the `redisearch.so` `loadmodule` line is commented out (RedisBloom/ReJSON/Timeseries load fine; original at `redis.conf.bak-original`). LiteLLM needs plain Redis only.
- **`Registry file not found` from llmbench with `XDG_CONFIG_HOME` set.** With that variable set, llmbench used to fall back to a registry at `~/.config/local-ai/registry.toml`. It now looks only at `$XDG_CONFIG_HOME/local-ai/registry.toml`, the file wt names in that case, and says `Registry file not found: <that path>` when it is missing. Move the file there, or name it with `WT_REGISTRY`.
- **`homebrew.mxcl.omlx.plist` is optional (2026-09-30 rebuild omitted it).** wt's and llmbench's lifecycle backends run `omlx start` on demand when an omlx model starts, and wt's live probes keep `omlx/*` routes out of `config.yaml` while it's down — `omlx none` in `brew services list` is the *healthy* state now, not something to fix. `brew services start omlx` only if you want oMLX always-on; remember KeepAlive then fights `omlx stop` during benchmark isolation.

## Going deeper

- Full install, plist templates, and the secret-redaction rules (plist `EnvironmentVariables`, config.yaml `api_key`s): [01-initial-setup](01-initial-setup.md)
- config.yaml anatomy, how `wt litellm sync` decides the routes, and the same kickstart with measured recovery: [04-litellm-config](04-litellm-config.md)
- Benchmark isolation/restore contracts behind §5, and what a clean restore guarantees: [05-benchmarks](05-benchmarks.md)
- The actual source of truth for everything §1 asserts (start-at-load, keep-alive, log paths, env keys): `~/Library/LaunchAgents/` — read the plist before guessing about any service
