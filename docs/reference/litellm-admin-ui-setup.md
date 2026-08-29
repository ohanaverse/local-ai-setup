# LiteLLM Admin UI Setup

**Date:** 2026-08-27
**Rotated 2026-08-29.** The original master key, salt key, and UI password were
committed in this document and in `issues.md`. They were rotated on the
machine via the LaunchAgent plist, and the repo now uses placeholders. Git
history was accepted rather than rewritten: the leaked values are dead after
rotation, and rewriting would have orphaned the checkout-SHA stamps in the
maintenance guide.

**Prerequisite:** LiteLLM proxy installed and running (see `docs/archive/Local AI Setup 2026-08-25.md`)

---

## Why this doc exists

The LiteLLM proxy serves a built-in dashboard at `http://localhost:4000/ui` with usage analytics, spend tracking, virtual key management, and request logs. Three things are required for it to work:

1. **UI login credentials** — `UI_USERNAME` / `UI_PASSWORD` env vars
2. **A PostgreSQL database** — LiteLLM's DB features (virtual keys, spend tracking, UI login) require Postgres. SQLite is **not supported** and causes a startup hang.
3. **Redis (optional but strongly recommended)** — LiteLLM warns at startup without Redis. Redis provides cross-worker rate limits, budget enforcement, router state, and cache invalidation.

This doc covers all three, plus the Prisma client setup that the proxy needs to talk to the database.

---

## Step 1 — Set UI login credentials

Add `UI_USERNAME`, `UI_PASSWORD`, and `LITELLM_MASTER_KEY` to the LaunchAgent's `EnvironmentVariables` block in `~/Library/LaunchAgents/local.litellm.proxy.plist`:

```xml
<key>LITELLM_MASTER_KEY</key>
<string>sk-litellm-<rotate-me></string>
<key>UI_USERNAME</key>
<string>admin</string>
<key>UI_PASSWORD</key>
<string><strong-password></string>
```

> **Change the password.** `admin/admin` is a placeholder. Edit the plist and restart the service.

`LITELLM_MASTER_KEY` is the proxy admin key — it must start with `sk-`. You'll use it as the `Authorization: Bearer` header for API calls and it grants full admin access.

---

## Step 2 — Install PostgreSQL

```bash
brew install postgresql@16
```

Homebrew pre-creates a database cluster at `/opt/homebrew/var/postgresql@16` with trust auth (no password needed for local connections).

Start it:

```bash
brew services start postgresql@16
```

Verify:

```bash
/opt/homebrew/opt/postgresql@16/bin/pg_isready -h localhost -p 5432
# localhost:5432 - accepting connections
```

PostgreSQL runs as a brew service and starts on login.

---

## Step 3 — Create the `litellm` database

```bash
/opt/homebrew/opt/postgresql@16/bin/psql -U "$(whoami)" -h localhost -p 5432 -d postgres -c "CREATE DATABASE litellm;"
```

Verify:

```bash
/opt/homebrew/opt/postgresql@16/bin/psql -U "$(whoami)" -h localhost -p 5432 -d postgres -c "\l" | grep litellm
#  litellm   | keith | UTF8 ...
```

> Connect to the `postgres` database first (`-d postgres`) — the default database matching your username may not exist yet.

---

## Step 4 — Add `database_url` to the LiteLLM config

Edit `~/.config/litellm/config.yaml` and add a `general_settings` block at the bottom:

```yaml
general_settings:
  database_url: "postgresql://keith@localhost:5432/litellm"
```

This tells the proxy where to find the database. The URL uses `keith` (your macOS user) with trust auth — no password needed for local connections.

---

## Step 5 — Install and start Redis

```bash
brew install redis
brew services start redis
```

> **macOS module loading issue (Redis 8.x):** If Redis fails to start with an error about `redisearch.so` (`dlopen ... errno=1`), comment out the module lines in `/opt/homebrew/etc/redis.conf`:
> ```
> # loadmodule /opt/homebrew/opt/redis/lib/redis/modules/redisbloom.so
> # loadmodule /opt/homebrew/opt/redis/lib/redis/modules/redisearch.so
> # loadmodule /opt/homebrew/opt/redis/lib/redis/modules/rejson.so
> # loadmodule /opt/homebrew/opt/redis/lib/redis/modules/redistimeseries.so
> ```
> Then run `brew services restart redis`. LiteLLM only needs basic Redis key-value operations — no modules required.

Verify:

```bash
redis-cli ping
# PONG
```

Redis runs as a brew service on port 6379 and starts on login.

---

## Step 6 — Add `coordination_redis` to the LiteLLM config

Edit `~/.config/litellm/config.yaml` and add `coordination_redis` under `general_settings`:

```yaml
general_settings:
  database_url: "postgresql://keith@localhost:5432/litellm"
  coordination_redis:
    host: localhost
    port: 6379
```

> **Why `coordination_redis`?** LiteLLM's UI banner checks for a **coordination Redis** — the cache used for cross-worker rate limits, spend tracking, and router state. Setting `redis_url` alone under `general_settings` does not initialize this cache. The `coordination_redis` block (or a `cache:` section with a Redis backend) is what makes the warning disappear.

---

## Step 7 — Add `DATABASE_URL` and `LITELLM_SALT_KEY` to the LaunchAgent

Generate a salt key (used to encrypt/decrypt LLM API keys stored in the DB — cannot be changed after a model is added):

```bash
openssl rand -base64 32 | tr -d '\n/=' | head -c 40
# e.g. <generated-salt>
```

Add both to the plist's `EnvironmentVariables` block:

```xml
<key>DATABASE_URL</key>
<string>postgresql://keith@localhost:5432/litellm</string>
<key>LITELLM_SALT_KEY</key>
<string><generated-salt></string>
```

---

## Step 8 — Install Prisma and generate the client

The LiteLLM proxy uses Prisma as its database ORM. The `prisma` Python package is **not** included in the `litellm[proxy]` extra and must be installed separately into the litellm venv:

```bash
uv pip install --python ~/.local/share/uv/tools/litellm/bin/python prisma
```

Then generate the Prisma client from LiteLLM's schema:

```bash
cd ~/.local/share/uv/tools/litellm/lib/python3.13/site-packages/litellm/proxy
PATH="$HOME/.local/share/uv/tools/litellm/bin:$PATH" \
  ~/.local/share/uv/tools/litellm/bin/prisma generate --schema=./schema.prisma
```

This creates the Prisma client at `site-packages/prisma/`.

---

## Step 9 — Push the database schema

Create all 72 LiteLLM tables in the `litellm` database:

```bash
cd ~/.local/share/uv/tools/litellm/lib/python3.13/site-packages/litellm/proxy
PATH="$HOME/.local/share/uv/tools/litellm/bin:$PATH" \
DATABASE_URL="postgresql://keith@localhost:5432/litellm" \
  ~/.local/share/uv/tools/litellm/bin/prisma db push --schema=./schema.prisma
```

Verify tables exist:

```bash
/opt/homebrew/opt/postgresql@16/bin/psql -U keith -h localhost -p 5432 -d litellm -c "\dt" | head -10
```

You should see 72 tables (`LiteLLM_SpendLogs`, `LiteLLM_UserTable`, `LiteLLM_TeamTable`, etc.).

---

## Step 10 — Restart the proxy and verify

```bash
launchctl stop local.litellm.proxy
sleep 2
launchctl start local.litellm.proxy
sleep 10
```

Verify the proxy is running and the database is connected:

```bash
# Process running
pgrep -f '[l]itellm.*config' && echo "running" || echo "not running"

# API responds (was 500 before DB was configured, now 200 with auth)
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:4000/v1/models \
  -H "Authorization: Bearer sk-litellm-<rotate-me>"
# 200

# UI redirects to login
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:4000/ui
# 307

# No fatal errors in log
tail -5 ~/.litellm.err.log
```

---

## Accessing the dashboard

Open http://localhost:4000/ui in a browser and log in with:

- **Username:** `admin`
- **Password:** `<strong-password>`

The dashboard provides:

- **Usage / Cost** — monthly spend, top keys, top models, spend by provider, daily trends
- **Endpoint Activity** — per-endpoint request counts, success/failure rates, token consumption
- **Logs** — full request-level spend logs with live-tail, filtering, session grouping
- **Virtual Keys** — create keys with rate limits, budgets, model access controls
- **Customer / Team / Tag usage** — per-entity spend breakdowns

---

## Final plist

The complete `~/Library/LaunchAgents/local.litellm.proxy.plist` after all steps:

```xml
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
        <key>LITELLM_MASTER_KEY</key>
        <string>sk-litellm-<rotate-me></string>
        <key>UI_USERNAME</key>
        <string>admin</string>
        <key>UI_PASSWORD</key>
        <string><strong-password></string>
        <key>OPENROUTER_API_KEY</key>
        <string>sk-or-v1-REPLACE_WITH_YOUR_KEY</string>
        <key>DATABASE_URL</key>
        <string>postgresql://keith@localhost:5432/litellm</string>
        <key>LITELLM_SALT_KEY</key>
        <string><generated-salt></string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/keith/.litellm.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/keith/.litellm.err.log</string>
</dict>
</plist>
```

---

## Troubleshooting

### `ModuleNotFoundError: No module named 'prisma'`

The `prisma` package isn't bundled with `litellm[proxy]`. Install it manually:

```bash
uv pip install --python ~/.local/share/uv/tools/litellm/bin/python prisma
```

### `Unable to find Prisma binaries. Please run 'prisma generate' first.`

The Prisma client hasn't been generated. Run:

```bash
cd ~/.local/share/uv/tools/litellm/lib/python3.13/site-packages/litellm/proxy
PATH="$HOME/.local/share/uv/tools/litellm/bin:$PATH" \
  ~/.local/share/uv/tools/litellm/bin/prisma generate --schema=./schema.prisma
```

### `relation "LiteLLM_SpendLogs" does not exist`

The database schema hasn't been pushed. Run:

```bash
cd ~/.local/share/uv/tools/litellm/lib/python3.13/site-packages/litellm/proxy
PATH="$HOME/.local/share/uv/tools/litellm/bin:$PATH" \
DATABASE_URL="postgresql://keith@localhost:5432/litellm" \
  ~/.local/share/uv/tools/litellm/bin/prisma db push --schema=./schema.prisma
```

### `/v1/models` returns 500

Database isn't connected. Check that PostgreSQL is running (`pg_isready -h localhost -p 5432`) and that `DATABASE_URL` is set in the plist and `database_url` is in `config.yaml`.

### `/v1/models` returns 401

This is correct behavior — the master key is now set, so API calls require `Authorization: Bearer sk-litellm-<rotate-me>`. Before the DB was configured, it returned 500 because the proxy couldn't function without a database.

### Proxy doesn't pick up new env vars after plist edit

`launchctl unload/load` with `KeepAlive=true` may not fully restart. Use:

```bash
launchctl stop local.litellm.proxy
sleep 2
launchctl start local.litellm.proxy
```

### PostgreSQL not running after reboot

```bash
brew services start postgresql@16
```

Or check status:

```bash
brew services list | grep postgresql
```

### Redis not running after reboot

```bash
brew services start redis
```

Or check status:

```bash
brew services list | grep redis
redis-cli ping
```

### Redis fails to start with `dlopen ... errno=1`

On macOS with Redis 8.x from Homebrew, signed module binaries may fail Gatekeeper checks. If the log shows `redisearch.so` failing to load, comment out all `loadmodule` lines in `/opt/homebrew/etc/redis.conf` and restart. LiteLLM does not need Redis modules — basic key-value operations are sufficient.