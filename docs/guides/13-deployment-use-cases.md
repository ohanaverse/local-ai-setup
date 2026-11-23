# Deployment use cases — one `wt-model` server, `wt` sessions anywhere

> Use this to: run the model half of wt (`wt-model serve`) and its sessions (`wt` / `wt-session`) in one of three layouts — both on one machine, a VM sandbox against its host, or two machines — and point a session at a server.
>
> Verified against: the `wt-model`/`wt-session` split on 2026-11-16 (commands taken from `wt-model --help` and [wt-model-server.md](../../wt/docs/wt-model-server.md); not run live for this guide)

## Prerequisites

- **wt installed on every machine that needs it** — `make install` from the repo root (or `make -C wt install`): it builds `wt-session`, `wt` (the same binary under two names) and `wt-model`, and copies them and the `*-wt` shims to `~/.local/bin/`. The machine that runs the models needs all of them; a session machine needs `wt` / `wt-session` and the shims only.
- **The LiteLLM proxy configured on the model host** — `wt-model litellm set --url URL --api-key KEY` there ([04-litellm-config](04-litellm-config.md)). `wt-model serve` reads that pair and refuses to start without it. A session never reads it.

## TL;DR — both on one machine

```bash
# one shared token file, mode 0600:
umask 077; head -c 32 /dev/urandom | base64 > ~/.config/wt-model/server.token

# the model server (a foreground process; ctrl+c stops it):
wt-model serve --listen 127.0.0.1:8787 --token-file ~/.config/wt-model/server.token

# name it for every session on this machine — ~/.config/agent-wt/config.toml:
#   [model-server]
#   mode = "local"
#   url = "http://127.0.0.1:8787"
#   token_file = "~/.config/wt-model/server.token"

wt model list --server      # the catalog this session's pickers will show
```

## Steps

`wt-model setup` and the `wt config` Mode tab do most of this for you: on the serving machine, `wt-model setup`'s `local` makes the token file `~/.config/wt-model/server.token`, installs a `wt-model` LaunchAgent and writes the `[model-server]` table below; on a session machine, the Mode tab's `remote` writes the table and keeps the token in `~/.config/agent-wt/remote-model-server.token` (its `local` stores the default url and token file and points at `wt-model setup`). The commands here are the same steps by hand. A session with no mode chosen refuses a launch that needs a model (on a terminal it opens the Mode tab first).

### 1. Make one token file

The same command in every layout (a fresh 32-byte token, nobody else can read the file):

```bash
mkdir -p ~/.config/wt-model; umask 077; head -c 32 /dev/urandom | base64 > ~/.config/wt-model/server.token
```

`wt-model serve` takes the server's file as `--token-file PATH`; on the same machine a session names that file as `token_file` under `[model-server]` (or `WT_MODEL_TOKEN_FILE`). On a machine that only runs sessions (use cases 2 and 3), put a copy of the server's token at `~/.config/agent-wt/remote-model-server.token` and name that file as the session's `token_file`. A file a group or other can read is refused by both sides (`chmod 600 PATH` is the fix). To rotate: write a new file with the command above, restart `wt-model serve`, and give every session machine the new token.

### 2. Use case 1 — both on one machine

```bash
wt-model serve --listen 127.0.0.1:8787 --token-file ~/.config/wt-model/server.token
```

```toml
# ~/.config/agent-wt/config.toml (same machine)
[model-server]
mode = "local"
url = "http://127.0.0.1:8787"
token_file = "~/.config/wt-model/server.token"
```

Loopback needs the token all the same — the server never opens without one. Everything a session does (pickers, `-M`, start/stop jobs, `wt smoke`, spend in `wt stats`) is a call to that one address.

### 3. Use case 2 — a VM sandbox and its host, one open connection

The agent runs in the VM; the models run on the host. The VM needs exactly one open connection — to the server's listen address — for both control and inference traffic.

```bash
# on the host: make the token, then listen on an address the VM can reach
# (the host's VM-facing interface if you can avoid 0.0.0.0):
umask 077; head -c 32 /dev/urandom | base64 > ~/.config/wt-model/server.token
wt-model serve --listen 0.0.0.0:8787 --token-file ~/.config/wt-model/server.token

# give the VM the same token file (mode 0600 on both sides):
scp ~/.config/wt-model/server.token <vm>:~/.config/agent-wt/remote-model-server.token
```

```toml
# ~/.config/agent-wt/config.toml (in the VM; the host's address as the VM sees it)
[model-server]
mode = "remote"
url = "http://<host>:8787"
token_file = "~/.config/agent-wt/remote-model-server.token"
```

The host keeps its own `[model-server]` (use case 1) or none: a host command (`wt-model model add`, `wt-model cloud-sync`, `wt-model litellm …`) needs no server.

### 4. Use case 3 — two machines

The same two halves as use case 2 over a LAN: `wt-model serve` on the machine that owns the models, listening on its LAN address; the session machine gets the token file and a `[model-server]` naming that address.

```bash
# on the model machine:
wt-model serve --listen <lan-address>:8787 --token-file ~/.config/wt-model/server.token
```

```toml
# ~/.config/agent-wt/config.toml (on the session machine)
[model-server]
mode = "remote"
url = "http://<lan-address>:8787"
token_file = "~/.config/agent-wt/remote-model-server.token"
```

Several session machines (and several `wt` runs on each) can use one server at once; each run registers its own client session, and the server stops a local model no client session uses after its idle grace.

**Over native TLS.** To encrypt the hop, or to use a Claude subscription model from the session machine, give the server a certificate for its LAN IP address ([the recipe](../../wt/docs/wt-model-server.md#making-a-certificate)), name it under `[gateway]` on the model machine, restart serve, and point the session at `https://`:

```toml
# ~/.config/wt-model/config.toml (on the model machine; hand-edited)
[gateway]
tls_cert = "~/.config/wt-model/tls/server.crt"
tls_key  = "~/.config/wt-model/tls/server.key"
```

```toml
# ~/.config/agent-wt/config.toml (on the session machine)
[model-server]
mode = "remote"
url = "https://<lan-address>:8787"
token_file = "~/.config/agent-wt/remote-model-server.token"
ca_file = "~/.config/agent-wt/model-server.crt"   # a copy of the server's certificate (never its key)
```

A tunnel that arrives on the model machine's loopback (`ssh -L`, `tailscale serve`) works too, with an `http://127.0.0.1:<port>` url on the session side.

**A Claude subscription model from the session machine** (#597) needs that encrypted path: a remote server over plain `http://` is refused before the login is sent. The host turns the mode on (`[gateway] subscription_passthrough = true`, then restart serve) and the session machine opts in (`[model-server] subscription = true`); [wt-model-server.md](../../wt/docs/wt-model-server.md#subscription-passthrough) and [wt-session.md](../../wt/docs/wt-session.md#claude-subscription-header-mode) have the steps and the checks.

- **Your own login only.** The subscription is the user's own claude.ai login. Do not share it or pool it: a model server used by other people with their logins would be intermediating their credentials, which Anthropic's terms forbid, and wt cannot tell whose login a request carries.
- **Usage credits.** Depending on the plan and seat tier, a Fable row and other large models may need usage credits or count against the plan's limits (Claude Code's `/model` can show "Requires usage credits"). Check the plan before pointing a slot at one.

### 5. The documented v1 limits

- **Plain HTTP by default; TLS is opt-in.** The server speaks TLS itself when `[gateway] tls_cert` and `tls_key` (hand-edited, in `~/.config/wt-model/config.toml`) name a certificate and its key; a session machine trusts a private certificate with `ca_file` under `[model-server]` (or `WT_MODEL_CA_FILE`). Remote sessions use native TLS (a certificate for the host's LAN IP) or a tunnel; without either keep the server on a network you trust. Binding to a non-loopback address is your explicit act. Making the certificate and renewing it: [Making a certificate](../../wt/docs/wt-model-server.md#making-a-certificate), [Renewal and rotation](../../wt/docs/wt-model-server.md#renewal-and-rotation). An expired certificate does not stop the server: at its next start it serves plain HTTP with one warning, `https://` sessions fail and remote subscription requests are refused until the certificate is renewed and serve restarted. TLS encrypts the hop; it does not identify the client.
- **No token scopes.** One shared token, one trust level: anyone holding it can read the catalog, load and stop local models, spend on routed cloud models and register client sessions. It grants no admin capability (the registry, prices and cloud-sync stay on the host's CLI), but it is not a gateway-only credential.
- **The agent holds the token, so it can call the control API too.** The token travels to each session machine and into the launched agent's environment (it authenticates to the gateway); nothing stops that holder from calling `/control/v1/`. The token is the whole boundary — keep the file 0600 and rotate it when a machine that held it goes away.

## Verification

```bash
wt-model --version                       # wt-model <version>
curl -s -H "Authorization: Bearer $(cat ~/.config/wt-model/server.token)" \
  http://127.0.0.1:8787/control/v1/ping   # {"api_major":1,"schema":N,"server":"…"} — wt refuses a server whose schema differs: restart wt-model after updating wt
wt model list --server                   # from a session machine: the server's catalog
```

A session with no `[model-server]` that asks for a model (`-M`, `-T`, `-F`, `wt start`, …) refuses before anything starts, naming `wt config` and `WT_MODEL_MODE` (a local or remote mode missing a setting names the missing setting) ([wt-session.md](../../wt/docs/wt-session.md) quotes the refusal).

## Gotchas

- **A session never starts a server and never guesses an address.** Each layout above runs `wt-model serve` yourself (or, on the same machine, `local` mode in `wt-model setup` sets up a LaunchAgent that does); with none configured, a `wt` that needs a model (`-M`, `-T`, `-F`, `wt start`, `wt smoke`, …) refuses (exit 1) instead of falling back to a local server. A plain `wt -A <agent>` launches the agent natively, with its own configuration and one `wt:` line saying so, only in native mode (`mode = "native"` under `[model-server]`, or `WT_MODEL_MODE=native`), never with no mode chosen and never when a server is half-configured (`wt/docs/wt-session.md`, "Native launch").
- **`wt-model serve` is a foreground process.** Start it in a terminal, under `nohup`, or from your own LaunchAgent — running it at boot is an optional recipe, not something wt builds.
- **Host commands run on the host.** `wt-model model add|edit|rm|init`, `wt-model cloud-sync` and `wt-model litellm …` read and write `registry.toml` and `config.yaml` on the machine they run on, never the server's ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §6).

## Going deeper

- The server itself — flags, endpoints, client sessions, auto-stop, errors: [wt-model-server.md](../../wt/docs/wt-model-server.md)
- The session side — what needs a model server, the refusals, exit codes: [wt-session.md](../../wt/docs/wt-session.md)
- Launching agents and models once it is up: [06-wt-agents-and-models](06-wt-agents-and-models.md); the proxy and its routes: [04-litellm-config](04-litellm-config.md)
- Which tool owns which file: [00-config-map](00-config-map.md)
