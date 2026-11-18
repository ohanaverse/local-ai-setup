# LiteLLM install and configuration — proxy, database, LaunchAgent, wt-model link

> Use this to: install the LiteLLM proxy and its database on a fresh macOS machine, write its LaunchAgent plist (every key explained), start it, link its master key to `wt-model`, verify it, restart and upgrade it, and fix the common failures. wt never installs LiteLLM; this guide is the authority a human or an agent follows.
>
> Verified against: LiteLLM 1.109.1 (`litellm --version`) on 2026-11-17. Behaviour of `wt litellm restart`, `bin/update/litellm` and the plist reader taken from their source (`wt/internal/litellm/restart.go`, `proxyenv.go`, `bin/update/litellm`); the install steps were not re-run on a fresh machine for this guide.

Paths below use `/Users/user_name` for your home directory; substitute your own. `launchd` does not expand `~` or `$HOME` inside a plist, so a plist always carries absolute paths.

## Prerequisites

- macOS with [Homebrew](https://brew.sh) and [uv](https://docs.astral.sh/uv/) on PATH.
- `wt` and `wt-model` installed (`make install` from the repo root) if you want the wt steps (restart, sync, linking); the proxy itself runs without them.
- Roughly 10 minutes; nothing here loads a model.

## TL;DR

```bash
uv tool install --python 3.11 'litellm[proxy,extra-proxy]'      # 1. the proxy (+ Prisma)
brew install postgresql@16 && brew services start postgresql@16  # 2. the database server
/opt/homebrew/opt/postgresql@16/bin/psql -h localhost -d postgres -c "CREATE DATABASE litellm;"
mkdir -p ~/.config/litellm && echo "model_list: []" > ~/.config/litellm/config.yaml   # 4. seed config
# 5-6. write the plist from section 5 (keys from section 6), chmod 600, then:
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.litellm.proxy.plist
wt-model litellm set --url http://localhost:4000 --api-key <master key>   # 10. link wt-model
```

## Steps

### 1. Install the proxy

```bash
uv tool install --python 3.11 'litellm[proxy,extra-proxy]'
litellm --version
```

- **`extra-proxy`**: on LiteLLM 1.98 and later the Prisma database stack lives in this extra. `litellm[proxy]` alone boots and prints its routes, then dies at startup with `ModuleNotFoundError: No module named 'prisma'`.
- **Python 3.11**: an unpinned install can land on a newer Python where Prisma's engine binaries are missing (`Unable to find Prisma binaries`). Keep the pin on every reinstall; `bin/update/litellm` does.
- The binary is `~/.local/bin/litellm`; the tool environment is `~/.local/share/uv/tools/litellm/`.

### 2. Install Postgres 16 and create the database

LiteLLM's virtual keys, spend tracking and Admin UI login need Postgres. SQLite is not supported and hangs the proxy at startup.

```bash
brew install postgresql@16
brew services start postgresql@16
/opt/homebrew/opt/postgresql@16/bin/pg_isready -h localhost -p 5432
/opt/homebrew/opt/postgresql@16/bin/psql -h localhost -p 5432 -d postgres -c "CREATE DATABASE litellm;"
```

Homebrew's default cluster trusts local connections, so no password is needed and the URL is `postgresql://user_name@localhost:5432/litellm` (your macOS user name). Connect to `-d postgres` for the create: a database named after your user may not exist yet.

Redis is optional (cross-worker rate limits and router state; LiteLLM warns at startup without it). Its setup, including a macOS module-loading problem, is in [litellm-admin-ui-setup.md](../reference/litellm-admin-ui-setup.md) steps 5-6.

### 3. Prisma client and migrations

Installing the package is not enough: the Prisma client is generated from the schema of the installed LiteLLM, so generate it after the first install and after every upgrade (`bin/update/litellm` repeats it for you).

```bash
SP=$(ls -d "$HOME"/.local/share/uv/tools/litellm/lib/python3.*/site-packages | tail -1)
PATH="$HOME/.local/share/uv/tools/litellm/bin:$PATH" \
  "$HOME/.local/share/uv/tools/litellm/bin/python" -m prisma generate --schema="$SP/litellm/proxy/schema.prisma"
```

The glob avoids hard-coding a Python version. **Do not run `prisma db push`.** On 1.98 and later the proxy runs `prisma migrate deploy` against `DATABASE_URL` itself at every startup, creating the `LiteLLM_*` tables on first start; a `db push` only adds a competing schema history.

### 4. Seed the config

```bash
mkdir -p ~/.config/litellm
echo "model_list: []" > ~/.config/litellm/config.yaml
```

Do not `touch` an empty file: LiteLLM fails to start on it, while `model_list: []` is a valid empty config. From here **wt owns this file**: `wt litellm sync` (and `wt start`/`stop`, `wt cloud-sync`) rewrites `model_list` to match the registry and the running local models. Edit only the proxy-only sections (for example `general_settings`) by hand; what is safe to edit is in [04-litellm-config.md](04-litellm-config.md). `WT_LITELLM_CONFIG` redirects the path wt writes. Optional Redis wiring goes under `general_settings` (`coordination_redis`, see the reference doc above).

### 5. The plist

Save as `~/Library/LaunchAgents/local.litellm.proxy.plist`. The Label **must** be `local.litellm.proxy`: `wt litellm restart` targets that job, and wt reads the proxy's environment from that file name.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>local.litellm.proxy</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/user_name/.local/bin/litellm</string>
        <string>--config</string>
        <string>/Users/user_name/.config/litellm/config.yaml</string>
        <string>--port</string>
        <string>4000</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/Users/user_name/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
        <key>DATABASE_URL</key>
        <string>postgresql://user_name@localhost:5432/litellm</string>
        <key>LITELLM_MASTER_KEY</key>
        <string>sk-REPLACE_WITH_GENERATED_KEY</string>
        <key>LITELLM_SALT_KEY</key>
        <string>REPLACE_WITH_GENERATED_SALT</string>
        <key>OPENROUTER_API_KEY</key>
        <string>REPLACE_OR_OMIT</string>
        <key>UI_USERNAME</key>
        <string>REPLACE</string>
        <key>UI_PASSWORD</key>
        <string>REPLACE</string>
        <key>MAX_BASE64_LENGTH_FOR_LOGGING</key>
        <string>10000000</string>
        <key>MAX_BASE64_LENGTH_STDOUT_LOG</key>
        <string>10000000</string>
        <key>MAX_EXCEPTION_MESSAGE_LENGTH</key>
        <string>10000000</string>
        <key>MAX_STRING_LENGTH_PROMPT_IN_DB</key>
        <string>10000000</string>
        <key>MAX_STRING_LENGTH_STDOUT_LOG</key>
        <string>10000000</string>
    </dict>
    <key>SoftResourceLimits</key>
    <dict>
        <key>NumberOfFiles</key>
        <integer>10240</integer>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/user_name/.litellm.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/user_name/.litellm.err.log</string>
</dict>
</plist>
```

Every `<key>` is followed by a value element of its own: `StandardErrorPath` takes a `<string>`, not a mismatched `</string>` after a `<key>` open tag (an error an older template carried; launchd refuses the file).

| Key | What it is | Required? |
|---|---|---|
| `Label` | The launchd job name; wt kickstarts `gui/<uid>/local.litellm.proxy` | Yes, exactly this |
| `ProgramArguments` | The `litellm` binary, `--config <absolute config.yaml>`, `--port 4000` | Yes; 4000 is the port wt and the guides assume |
| `RunAtLoad` / `KeepAlive` | Start at login; relaunch when it exits (this is how a restart comes back) | Yes for `wt litellm restart` to work |
| `StandardOutPath` / `StandardErrorPath` | Log files (`~/.litellm.log`, `~/.litellm.err.log`). Lines carry only `HH:MM:SS`, no date | Recommended |
| `SoftResourceLimits.NumberOfFiles` | Open-file limit; launchd's default is low for a proxy holding many connections | Recommended |
| `EnvironmentVariables.PATH` | Where the proxy finds helper binaries | Yes (launchd's default PATH is minimal) |
| `DATABASE_URL` | Postgres connection URL from section 2 | Yes for keys, spend, UI login |
| `LITELLM_MASTER_KEY` | Admin key and the bearer token for the API; must start with `sk-` | Yes |
| `LITELLM_SALT_KEY` | Encrypts provider keys stored in the database (section 6) | Yes once the database holds encrypted data; set it from the start |
| `OPENROUTER_API_KEY` | A provider key. Any config reference `os.environ/OPENROUTER_API_KEY` is resolved by the **proxy's** process, which gets only this dict, not your shell. Add one key per provider your routes name | Optional (only for OpenRouter routes) |
| `UI_USERNAME` / `UI_PASSWORD` | Login for the Admin UI at `/ui` | Optional; without them the UI login does not work |
| `MAX_BASE64_LENGTH_FOR_LOGGING`, `MAX_BASE64_LENGTH_STDOUT_LOG`, `MAX_EXCEPTION_MESSAGE_LENGTH`, `MAX_STRING_LENGTH_PROMPT_IN_DB`, `MAX_STRING_LENGTH_STDOUT_LOG` | Size limits LiteLLM applies to logged and stored text. Only `MAX_STRING_LENGTH_PROMPT_IN_DB` is documented in this repo: it truncates long `content`/`reasoning_content` before the database write ([litellm-session-logs/CLAUDE.md](../../litellm-session-logs/CLAUDE.md)). The purposes of the other four (base64 and string length in logs, exception message length) are inferred from the key names; check LiteLLM's own documentation before relying on them | Optional |

wt reads only the **string** values of `EnvironmentVariables` (to check that an `os.environ/NAME` reference is set, and `DATABASE_URL`). With `WT_LITELLM_RESTART_CMD` set, or an unreadable/binary plist, wt falls back to its own environment instead. `WT_LITELLM_PLIST` points wt at a plist elsewhere.

### 6. Generate the keys; file mode

```bash
echo "sk-$(openssl rand -hex 32)"     # LITELLM_MASTER_KEY
openssl rand -hex 32                  # LITELLM_SALT_KEY
```

Paste each into the plist. Do not echo them into logs or chat, and do not commit the plist.

**The salt key must never change once the database holds encrypted data** (provider keys added through the Admin UI or API). Changing it makes every stored key undecryptable and you must re-enter them. Back the value up in a password manager before first use. The master key can be rotated: edit the plist, restart, then update wt-model (section 10) and any other client.

**A plist with `DATABASE_URL` and no `LITELLM_SALT_KEY` needs care before any key change.** LiteLLM falls back to the master key when no salt key is set (see its documentation), so provider credentials stored in the database are then encrypted with the master key, and adding a fresh random salt key, or rotating the master key, would leave them unreadable. If the database already holds stored credentials, first set `LITELLM_SALT_KEY` to the *current* master key's value and restart; after that the master key can be rotated safely. If you are sure the database holds no stored credentials, or it is a fresh database, generate a random salt key as above. `wt config` refuses to rotate such a plist and points here.

The plist holds secrets, so restrict it, and keep it out of group/other reach:

```bash
chmod 600 ~/Library/LaunchAgents/local.litellm.proxy.plist
plutil -lint ~/Library/LaunchAgents/local.litellm.proxy.plist   # syntax check; prints OK
```

### 7. Load it

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.litellm.proxy.plist
```

To reload after an edit to a plist already loaded, `launchctl bootout gui/$(id -u)/local.litellm.proxy` first, then `bootstrap` again. (Editing `config.yaml` needs only a restart, section 9.) The proxy then starts at every login.

### 8. Ports, logs, overrides

| What | Where |
|---|---|
| Proxy API | `http://localhost:4000/v1` (bearer = master key) |
| Admin UI | `http://localhost:4000/ui` |
| Logs | `~/.litellm.log`, `~/.litellm.err.log` (time of day only; bound a grep with `tail`) |
| Config wt writes | `~/.config/litellm/config.yaml`; override `WT_LITELLM_CONFIG` |
| Plist wt reads | `~/Library/LaunchAgents/local.litellm.proxy.plist`; override `WT_LITELLM_PLIST` |
| Restart command | `WT_LITELLM_RESTART_CMD` (legacy `MODELMAN_LITELLM_*` names still read) |

### 9. Restart and upgrade

```bash
wt litellm restart
```

This sends the proxy one SIGTERM and waits for it to finish a request in flight (up to **3 minutes**), then launchd starts it again and wt waits for it to answer. The port refuses connections for the whole drain. Past 3 minutes it is killed and the in-flight request is cut off, with a notice. If the job is not running, or `launchctl` finds no pid, wt runs the forced `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`, which kills after 5 seconds. Set `WT_LITELLM_RESTART_CMD` to run your own command instead (for a proxy not under that LaunchAgent). Prefer `wt litellm restart` over a bare `kickstart -k` whenever an agent might be mid-request.

Upgrade with the repo script, never a bare `uv tool upgrade` (the Prisma client and the running process would both go stale):

```bash
bin/update/litellm --check     # installed version, whether uv sees an update
bin/update/litellm --dry-run   # the commands it would run
bin/update/litellm             # upgrade, re-pin Python 3.11 + extra-proxy, regenerate Prisma, restart
```

Details: [08-maintenance-and-troubleshooting.md](08-maintenance-and-troubleshooting.md) §4.

### 10. Link the master key to wt-model

`wt-model serve` forwards inference to LiteLLM with its own copy of the master key (the "pair"), kept apart from the plist: nothing in wt reads `LITELLM_MASTER_KEY` from the plist, so the two agree only because they are kept in step: the `wt config` Model server tab does it when it links or rotates the key (below); otherwise you do.

```bash
wt-model litellm set --url http://localhost:4000 --api-key <master key>
wt-model litellm status      # url and masked key; the key is never printed
```

The pair lives in `~/.config/wt-model/` (`config.toml` plus a `0600` `litellm.key`). **A running `wt-model` reads the pair once at startup: restart it afterwards** (section 12). `wt-model serve` exits 1 without a pair.

## Verification

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:4000/health/liveliness     # 200, no key needed
curl -s http://localhost:4000/health/readiness                                        # no key; shows "db":"connected"
curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer <master key>" http://localhost:4000/v1/models
wt litellm status        # proxy url, whether wt's key is set (never printed)
wt litellm list          # the model ids currently routed (live state: run it, do not copy it)
```

- Liveliness only proves the process is up, not that your key is right.
- Readiness reports the database; if `db` is not connected, check `DATABASE_URL`, Postgres and `~/.litellm.err.log`.
- `/v1/models` is the only key check: **200** means the key is accepted, **401** means the key you sent differs from the plist's `LITELLM_MASTER_KEY`.
- An empty `model_list` returns an empty list: that is fine before `wt litellm sync`.

## What the `wt config` Model server tab does for LiteLLM

The `wt config` Model server tab (design: [the model-server configuration spec](../superpowers/specs/2026-11-17-wt-session-model-server-config-design.md); session side: [06-wt-agents-and-models.md](06-wt-agents-and-models.md)) sets up the `wt-model` side. For LiteLLM it checks that the proxy is reachable, its database connected, the pair's key accepted and routes in sync, and says what to fix. It also warns when the plist is readable by group or others, and offers two LiteLLM actions in local mode: `k` (the key menu) and `t` (tighten). Both change nothing until you confirm a plan that lists every step.

`k` opens a menu of up to three options, each offered only when it can work; a line under the menu says why an option is missing:

- **use the plist's key**: offered when the plist has a `LITELLM_MASTER_KEY`. It reads that key and sets wt-model's pair to it; it never edits the plist. This is the default fix for a pair that was never set or holds a stale key.
- **generate a new key**: offered when the plist is an XML plist that has a `LITELLM_MASTER_KEY`, and not when it has `DATABASE_URL` but no `LITELLM_SALT_KEY` (see section 6: set the salt key to the current master key first).
- **create the LiteLLM agent**: offered only when no plist exists **and** a `litellm` binary is found (`~/.local/bin`, `/opt/homebrew/bin`, `/usr/local/bin` or `PATH`) **and** LiteLLM's `config.yaml` exists **and** nothing listens on port 4000. It is port 4000 only. The minimal plist it writes (mode 0600, label `local.litellm.proxy`) holds the program arguments, `RunAtLoad`/`KeepAlive`, the two log paths and `PATH`, `LITELLM_MASTER_KEY` and `LITELLM_SALT_KEY`, both newly generated. It leaves out `DATABASE_URL`, `SoftResourceLimits.NumberOfFiles`, the UI login and provider keys: add them as sections 5 and 6 describe (spend tracking and the Admin UI need `DATABASE_URL`). It then loads the agent, waits for LiteLLM, checks that the new job is what listens on port 4000 and that it accepts the new key, and sets wt-model's pair to the same key. Keep a copy of the plist: the generated salt key must never change once LiteLLM stores credentials with it.

`t` tightens a plist that is looser than 0600 to 0600 with one `chmod`, and changes nothing else; it is offered only when the plist exists and is looser.

**Rotating the master key** (`k`, generate a new key) runs these steps in order, stopping at the first failure:

1. check the plist as it is now (the key edit is tried without writing, and the `DATABASE_URL`-without-salt rule is checked again), then back it up to `~/.config/wt-model/backups/local.litellm.proxy.plist.bak-<date>-<time>` (a second backup in the same second gets `-2`, `-3`, ...; an earlier backup is never replaced), at the plist's mode without group or other bits; the backup holds the old key. The directory is created 0700 (a looser one is set to 0700, and the result says so); it is outside `~/Library/LaunchAgents` on purpose, since a copy there carries the same label and launchd could load it;
2. replace `LITELLM_MASTER_KEY` in the plist with a new generated key, keeping every other byte, written without group or other bits;
3. `chmod 0600` the plist when it was looser;
4. update wt-model's pair to the same key;
5. restart LiteLLM (`launchctl kickstart -k`) and wait until it answers and accepts the new key;
6. restart wt-model so it reads the new pair.

Both restarts are `kickstart -k`: a request in flight through LiteLLM or wt-model is cut at once (unlike `wt litellm restart`, which lets it finish), and the plan says so. Rotate when no agent is mid-request. To restore the old plist by hand: `cp -p <backup> ~/Library/LaunchAgents/local.litellm.proxy.plist && chmod 600 ~/Library/LaunchAgents/local.litellm.proxy.plist` (the result names the backup).

Anything else that holds the old master key stops working once LiteLLM restarts: other clients and scripts that send it, a `[litellm]` `api_key` in `~/.config/agent-wt/config.toml` if one is set there, and copies of the plist (the backup keeps the old key; remove it once the new key works). Rotation never touches the salt key, `DATABASE_URL`, provider keys or the UI login.

**Refusals** say why and what to do, and nothing is changed:

| Refusal | What it tells you |
|---|---|
| a plist already exists (create) | wt creates a plist only when none exists; use the plist's key or generate a new one |
| no `litellm` binary, or no `config.yaml` (create) | wt never installs LiteLLM nor writes a first config; install it as sections 1 and 4 describe |
| something listens on port 4000 (create) | names the pid; stop it, or keep it and enter its master key instead |
| a plist wt will not edit, found only when applying (rotate): the key appears twice, or sits in a comment, CDATA, the wrong dict or a non-string value | the plan is offered, then refused at apply before the backup; the plist and wt-model's pair were not touched and no backup was kept. Fix the plist by hand |
| a binary property list (rotate) | run `plutil -convert xml1 <plist>`, then check again |
| an unreadable plist, or no `LITELLM_MASTER_KEY` (rotate) | fix the plist as sections 5 and 6 describe |
| `DATABASE_URL` but no `LITELLM_SALT_KEY` (rotate; checked when planning and again when applying, before the backup) | set `LITELLM_SALT_KEY` to the current master key's value first, or confirm the database holds no stored credentials (section 6) |

When a step fails, or an apply stops with a service out of step, a result screen shows the commands to run by hand (to put the plist, wt-model's pair or a service right) first, then the notes (the backup's path), then the result's own text; up and down scroll it, esc leaves it. A quit asked for during such an apply is held back until the notes can be read. Secrets never appear on it.

The tab never installs LiteLLM, Postgres, Redis or Prisma, never touches the salt key, `DATABASE_URL`, provider keys or the UI login of an existing plist, and never replaces an existing plist. The manual steps above remain the whole procedure for anything it does not do.

### 11. Optional: a wt-model LaunchAgent

`wt-model serve` is a foreground process; this runs it at login beside the proxy. This is the file the `wt config` Model server tab manages for `local` mode (`local.wt-model.serve`, token `~/.config/wt-model/server.token`); the tab recognises a plist with that label only when it carries wt's `managed by wt` marker comment, and otherwise reports it as not managed and never overwrites it, so a hand-made plist is yours to maintain. Use a token file (mode `0600`) that sessions also name (`token_file` under `[model-server]`, see [13-deployment-use-cases.md](13-deployment-use-cases.md)):

```bash
mkdir -p ~/.config/wt-model
umask 077; head -c 32 /dev/urandom | base64 | tr -d '\n=/' > ~/.config/wt-model/server.token
```

Save as `~/Library/LaunchAgents/local.wt-model.serve.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>local.wt-model.serve</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/user_name/.local/bin/wt-model</string>
        <string>serve</string>
        <string>--listen</string>
        <string>127.0.0.1:8787</string>
        <string>--token-file</string>
        <string>/Users/user_name/.config/wt-model/server.token</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/Users/user_name/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/user_name/.wt-model.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/user_name/.wt-model.err.log</string>
</dict>
</plist>
```

`PATH` must include wherever `ollama`, `omlx`, `mtplx`, `rec` and `sushi` live, or wt-model cannot start local models. Load it with `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.wt-model.serve.plist`. After `wt-model litellm set` or a token change, `launchctl kickstart -k gui/$(id -u)/local.wt-model.serve` restarts it. Do not run a second, hand-started `wt-model` on the same port: `KeepAlive` would loop on the bind failure.

## Gotchas

| Symptom | Cause and fix |
|---|---|
| `ModuleNotFoundError: No module named 'prisma'` at startup | Installed without `extra-proxy`. Reinstall with section 1's command (or `bin/update/litellm --reinstall`). |
| `Unable to find Prisma binaries` / run `prisma generate` | Wrong Python, or the client is stale after an upgrade. Reinstall on Python 3.11, then run section 3's generate. |
| Proxy hangs at startup or the UI login fails | No Postgres, a wrong `DATABASE_URL`, or SQLite. Check `pg_isready` and the readiness `db` field. |
| `launchctl bootstrap` fails (`Input/output error`) | The plist is malformed or already loaded. Run `plutil -lint`; `bootout` first if loaded. |
| Port 4000 refuses connections for up to ~3 minutes after `wt litellm restart` | The drain waiting on a request in flight. Watch `~/.litellm.log`; normal. |
| `/v1/models` returns 401 | Wrong bearer. Compare the key with the plist's `LITELLM_MASTER_KEY`; update wt-model with `wt-model litellm set` and restart it. |
| wt-model returns auth errors after you rotated the master key | wt-model reads the pair once. Restart it. |
| Stored provider keys fail to decrypt | `LITELLM_SALT_KEY` changed. Restore the original; there is no recovery otherwise. |
| `wt` warns an `os.environ/NAME` is unset | `NAME` is missing from the plist's `EnvironmentVariables` (your shell does not count). Add it and restart. |
| Edits to the plist have no effect | `launchd` reads it at load: `bootout`, then `bootstrap` again; a restart alone does not re-read it. |
| Plist is world-readable | `chmod 600`; it holds the master key, salt key and provider keys. |

## Going deeper

- Proxy config, `wt litellm sync`, the Admin UI: [04-litellm-config.md](04-litellm-config.md)
- Restarts, upgrades, log triage: [08-maintenance-and-troubleshooting.md](08-maintenance-and-troubleshooting.md)
- Redis and the Admin UI setup history: [litellm-admin-ui-setup.md](../reference/litellm-admin-ui-setup.md) (its `prisma db push` step is wrong for 1.98+; use section 3 here)
- wt-model server, its token and the pair: [wt-model-server.md](../../wt/docs/wt-model-server.md); layouts: [13-deployment-use-cases.md](13-deployment-use-cases.md)
- Who owns which file: [00-config-map.md](00-config-map.md)
