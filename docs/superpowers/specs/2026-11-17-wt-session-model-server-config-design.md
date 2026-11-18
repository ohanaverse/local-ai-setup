# wt-session: a stored, editable model-server configuration (`native` | `local` | `remote`)

Status: design, written 2026-11-17 from a conversation with the user (approach A chosen). Approved by the user 2026-11-17. Partly supersedes the automatic native fallback of #615 (`2026-11-16-wt-native-launch-without-model-server-design.md`, approach 1); the user confirmed that reversal. See "Relationship to #615".

## Intent

Since the session/model-server split every session reaches its models through a wt-model server, and a session finds that server only through a hand-edited `[model-server]` table in `~/.config/agent-wt/config.toml` (or `WT_MODEL_*` env). Nothing writes that table, nothing helps create it, and a machine with none of it silently launches the agent natively (#615), so models simply stop being offered.

That is what happened on the user's machine: wt used to offer models; after the split `config.toml` has no `[model-server]`, no `WT_MODEL_*` is set, wt-model is not running and not registered with launchd (only the LiteLLM proxy is, on 4000), so `wt` quietly went native and showed no models. The user's words: wt-session "should have a way to create and update its wt-model config; maybe this should be a more robust feature", and "it needs to be reachable even when the wt-model config is missing or invalid; wt config makes sense".

So: a session stores an explicit choice of how it reaches models, asks for it when it has none, and the choice can be created, checked and changed from inside `wt config` even when the configuration is missing or broken.

## The three modes

Names are my calls; they extend the existing vocabulary ("native" is already what the code and docs call a launch with no model server).

| Mode | Meaning | What runs where |
|---|---|---|
| `native` | No model server. Launch agents with whatever local configuration they have. | Nothing. |
| `local` | wt-model runs on this machine, started at login beside the LiteLLM proxy. | A wt-owned LaunchAgent runs `wt-model serve` on loopback. |
| `remote` | Use a wt-model that already runs on another machine. | Nothing here; the user supplies url and token. |

## Goals

- `wt config` has a **Model server** tab that shows the effective mode and live status, and creates, checks and changes the configuration for all three modes.
- The tab works when the configuration is missing, semantically invalid, or the wt-model home is missing or broken (see "Reachability").
- A launch with **no stored mode** asks the user to choose one (by opening that tab) instead of silently going native.
- `local` is real: one apply generates the token, makes wt-model's LiteLLM pair available, installs and starts a LaunchAgent, waits until the server answers, then stores the mode.
- Every change is shown as a plan first (what files, what launchd action) and written only after the user confirms.
- `remote` checks the server (`Ping`) and says exactly what is wrong: unreachable, not a wt-model, wrong token, version skew.
- For `local`, the tab also checks that LiteLLM is set up correctly for wt-model (reachable, database connected, the pair's key accepted, routes in sync, a restart owed, port and file mode), and says what to fix.
- The repo has one authoritative guide for installing and configuring LiteLLM and its related components, written so a human or an agent can follow it, and the tool points at it instead of re-implementing it.

## Non-goals

- No TLS and no tunnelling. A remote wt-model is plain http carrying a bearer token, as today; protecting that link is the user's job.
- No automatic token distribution to a remote machine. The tab can take a pasted token and write the 0600 file; getting it from the server's machine is manual.
- No `wt model-server ...` command group now. The setup logic lives in a screen-free package precisely so a CLI can be added later without rework.
- No LiteLLM **installation or general configuration** from the tool: the package, Postgres and Redis, Prisma, provider keys, the admin UI login and the rest of a LiteLLM deployment are out of scope for `wt config` and are covered by a guide (see "LiteLLM: scope and guide"). The tool touches the LiteLLM LaunchAgent only narrowly: it creates a minimal one when none exists and LiteLLM is already installed, and otherwise changes only `LITELLM_MASTER_KEY`.
- No non-macOS service manager. `local` needs launchd; elsewhere the tab disables it and says why.
- No change to the wire protocol, the gateway, or Phase 4's plan to move host admin commands.
- No restarting of wt-model by `bin/update-tools` (a follow-up; the tab gets a manual restart).

## Anti-goals

- Native by default. A machine that never chose `native` must not launch natively without being told.
- Overwriting a hand-edited `config.toml` the tool cannot parse.
- Writing a secret inline: the token is only ever a path (`token_file`), never a value in `config.toml`.
- A tab that freezes on a slow `launchctl`, `Ping` or file read: every outside call runs in a `tea.Cmd`.
- Leaving a half-applied `local` setup that the config points at: config is written last.

## Relationship to #615

#615 chose "automatic fallback when nothing is configured" and rejected an explicit opt-in as needing setup on every machine. This design reverses that on the user's request, with the setup cost removed by a first-run flow: `native` becomes an explicit stored mode, and "nothing configured" becomes "ask". The #615 behaviour for a *configured* `native` machine is unchanged (same launch, same refusals for `-M/-T/-F`); only how a session arrives at it changes. The #615 spec, `docs/wt-session.md`, guide 13 and `wt/CLAUDE.md` get the matching edit. Machines with org-managed agent gateways pick `native` once.

Cost to say plainly: a script or CI job on a machine that has never picked a mode used to launch natively and will now refuse, with a message naming the fix. `WT_MODEL_MODE=native` (below) is the one-line escape.

## Constraints (verified in the code, 2026-11-17)

- Today a session has no stored mode. `cmd/wt-session/models.go` `resolveModels` reads `config.LoadSession`, expands `~` in `token_file`, and calls `modelclient.ResolveEndpoint` (`internal/modelclient/endpoint.go`), where env beats config (`WT_MODEL_URL`, `WT_MODEL_TOKEN_FILE`, then a literal `WT_MODEL_TOKEN`). `NoServerConfigured` is true only when both url and token are missing; exactly one missing is a half-configured server and refuses.
- Native use sites: `main.go` (pre-launch gate and `runLaunchPath`), `launch_remote.go` (`launchNative`), `nativeFallback`/`wouldLaunchNative` in `models.go`, and the TUI's nil client in `internal/tui/app.go` (`enterModelPhaseForAgent`). `stats_usage.go` and `model_cmds.go` treat `NotConfiguredError` as a soft case. About 21 call sites in 13 files go through `buildModels`/`openModels`.
- `config.ModelServerState` (`internal/config/modelserver.go`) has `url`, `token_file` and a discouraged inline `token`; no code writes the table. Its fields carry no `omitempty`, so a bare save would write empty strings.
- `readConfigFile` (`internal/config/config.go`) returns an error on a TOML **syntax** error and defaults on a missing file; `LoadSession` and `PatchSave` both go through it. `writeConfigFile` re-encodes the whole file (comments are not preserved; true of every wt writer today) and writes 0600 only when an inline secret is present.
- `wt config` opens on `a.hostConfig()` and a possibly nil config plus its error (`cmd/wt-session/commands_config.go`), so the editor is reachable when validation fails. The Models tab takes injected deps (`ModelsDeps`) and runs outside calls in commands; the Agents tab buffers edits and saves with `ctrl+s`.
- wt-model: `serve` requires `--listen` (no default) and `--token-file` (0600, required even on loopback, read once at startup); `--idle-grace` defaults to 10m. Its LiteLLM pair (`url`, `key_file` plus a 0600 `litellm.key`) lives in `~/.config/wt-model/` and is written by `config.SetProxy`; `serve` exits 1 at startup without it, and reads it once. On this machine the pair is already written.
- Nothing in the repo writes a LaunchAgent. The LiteLLM plist (`~/Library/LaunchAgents/local.litellm.proxy.plist`) is hand-authored with `RunAtLoad` and `KeepAlive`; wt reads its `EnvironmentVariables` (`internal/litellm/proxyenv.go`, `WT_LITELLM_PLIST`) and kickstarts it. Tests refuse `launchctl` in a test binary through a seam (`internal/litellm/restart.go`).
- The user's LiteLLM plist holds far more than a master key: `DATABASE_URL`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `OPENROUTER_API_KEY`, `UI_USERNAME`/`UI_PASSWORD`, five size/logging limits, `PATH`, `SoftResourceLimits`, a binary path, a `--config` path and `--port 4000`; it is mode 0644 while holding secrets. The salt key must never change once the database holds encrypted data. Fresh-machine installation is `uv tool install` of `litellm[proxy,extra-proxy]` on Python 3.11 plus Postgres 16 and a Prisma client generation (`bin/update/litellm`, guide 01).
- wt never edits the LiteLLM plist and reads it only through `litellm.plistEnvironment` (string `EnvironmentVariables`, for `os.environ/NAME` references and `DATABASE_URL`); no code asks for `LITELLM_MASTER_KEY`, so the plist's key and wt-model's pair key agree only by operator discipline. `/health/liveliness` is unauthenticated, so wt's readiness wait does not prove the key is right; `/health/readiness` (unauthenticated) reports `db: connected`; an authenticated `/v1/models` (200 versus 401) is the only key check. wt-model already polls `/v1/models` with the pair key for routing and fails soft (unknown), never reporting a bad key.
- wt-model starts model processes (ollama, omlx, mtplx, rec, sushi) by binary name, and launchd's default `PATH` is minimal, so a LaunchAgent without a `PATH` could not start them.
- The client (`internal/modelclient`) never follows redirects, ignores proxy env, and `Ping` classifies `UnreachableError`, `UnauthorizedError` (401), `NotModelServerError`, `SkewError` (API major) and `SchemaError`. Ping needs the token, so a 401 separates a bad token from a wrong address.
- Style guide: prompts that write config are default-No (P1) and read `/dev/tty` (P3); with no terminal a consent-needing command refuses and names the flag (P4); errors are `wt: ...` once via `reportExit` (M3); a refusal says what is still true (M7); anything printed during the alt screen is lost.
- `configeditor` may not link `internal/tui` (ratchet rule, WP4) and session library packages may not link admin packages (`internal/depcheck`).

## Approaches considered

- **A. A Model server tab over a screen-free setup package (chosen by the user).** New admin-side package `internal/modelsetup` observes, plans and applies; the tab is a thin view; a launch with no mode opens the tab. Cost: structure up front; scripts configure by hand-editing the table until a CLI is added over the same package.
- **B. CLI first (`wt model-server init|show|set|check`), tab second.** Scriptable and good for ssh setup, but a larger surface now (JSON contracts, a new reserved word at the root that shadows the shell-wt passthrough) and no new capability for the stated problem.
- **C. Minimal:** store `mode`, edit url and token path, start wt-model by hand. Does not deliver `local` ("starts with the LiteLLM proxy"), so rejected.
- For `local`'s start-up: a **second LaunchAgent** rather than wrapping or editing the user's hand-written LiteLLM plist, because launchd runs one program per plist and editing a hand-authored file is invasive.

## Design

### Stored configuration

```toml
[model-server]
mode = "local"                                  # "native" | "local" | "remote"
url = "http://127.0.0.1:8787"                   # local, remote
token_file = "~/.config/wt-model/server.token"  # local, remote; a path, never the secret
```

`native` stores only `mode`. `ModelServerState` gains `Mode`; all fields get `omitempty` so a save never writes empty strings.

**Effective mode** (new `modelclient.ResolveMode`, pure). Environment first, then the table, then inference, first match wins:

1. `WT_MODEL_MODE` if set and valid (`native`, `local`, `remote`); an invalid value is a problem and refuses naming the valid ones. It is the explicit choice, so it beats everything below: `WT_MODEL_MODE=native` with `WT_MODEL_URL` set is native.
2. Environment supplies **both** an endpoint and a token (`WT_MODEL_URL` plus `WT_MODEL_TOKEN_FILE` or `WT_MODEL_TOKEN`): effective `remote`, source `env`. This is a per-invocation override that behaves exactly as today, so existing scripts and the env-only test setups keep working, even over a stored `native` or `local`.
3. The stored `[model-server] mode`, if set.
4. Inference for older configs: no mode, but url **and** token_file in the table: effective `remote`, source `inferred`, never written by a read.
5. Otherwise **unset**.

Validation runs on the merged values (environment over table), so an environment variable counts as supplying a field: `mode = "remote"` with no `token_file` but `WT_MODEL_TOKEN` set is valid. A stored `remote` or `local` still missing a url or token after the merge is invalid: refuse naming the missing setting and `wt config`. Exactly one of url/token present with no mode anywhere (step 4's half-configured case) is invalid and refuses as today. An explicit `native` (step 1 or 3) ignores stray url/token values. The tab shows when an environment override is in force, so the stored values are not mistaken for the live ones.

### What a session does

| Effective mode | Launch, no `-M/-T/-F` | `-M/-T/-F`, slot profile | `start/stop/job/smoke/stats/model list --server` |
|---|---|---|---|
| `native` | launches natively; one `wt:` line (reworded: "native mode, set in wt config") | refuse: "native mode has no model server; change the mode in `wt config`" | refuse, same wording (stats degrades softly as today) |
| `local`, `remote` | uses the server | uses the server | uses the server |
| server unreachable | error names the mode and the fix: `local`: open `wt config` > Model server, or `launchctl kickstart -k gui/<uid>/local.wt-model.serve`; `remote`: the url and the classified `Ping` error | same | same |
| unset | on a terminal: open `wt config` on the Model server tab, then re-resolve; if still unset the launch ends, exit 1, nothing launched. With no terminal: refuse, exit 1, naming `wt config` and `WT_MODEL_MODE=native` | same | refuse naming `wt config`; one-shot commands do not open a TUI |

**Which commands.** Only launches prompt. Commands that need a server today (they call `buildModels`/`openModels`): `wt rotate`, `job`, `start`/`stop`, `model list --server`, `smoke`, `stats` (soft: it degrades as now), `profile` with `-M profile/<name>` and `profile slots usage|candidates|add|set`, and the slot-profile launch. On an unset mode these refuse, exit 1, naming `wt config`; none opens a TUI. Commands that never build a model session are untouched: `worktrees`/`remove`/`rescue`/`restore-root`, `litellm`, `cloud-sync`, the registry commands (`model add|edit|rm|init`, plain `model list`), `config`, and `-A shell` or any command agent.

**Where the gate sits.** `main.go` already refuses a model-driven launch before the launch question, any worktree or branch, and the guard (`(agent != "" && !agents.IsCommand(agent)) || (agent == "" && stdinTTY())`, calling `buildModels`). The unset-mode gate goes in that same block, so nothing is created before the answer is known; bare `wt` with no terminal keeps the picker's own refusal first. When the gate opens `wt config`, it is a separate Bubble Tea program run to completion; the picker's program starts afterwards. `buildModels` caches its outcome for the process (`a.built`, `a.builtErr`, `a.builtSet`, and `a.models`), so the gate clears that cache after the tab returns and resolves again. `launch_remote.go` and `tui.enterModelPhaseForAgent` keep their native/server decisions, which now read the resolved mode rather than inferring it. The `NotConfiguredError` text ("wt never starts one") is rewritten: it is false for `local`.

### Reachability (what "missing or invalid" means)

The tab does not use `config.Load` or `LoadSession`. It reads `config.toml` itself in two steps: decode to a generic map (syntax only), then decode just the `model-server` table from it, so a semantic error elsewhere in the file never hides it.

| Condition | Tab behaviour |
|---|---|
| `config.toml` missing | Empty state; the first apply creates it. |
| Invalid elsewhere (agents, providers) or unknown keys | Reads the table normally; writes go through `PatchSave`, which only needs a parseable file. |
| `config.toml` has a TOML syntax error | The tab opens, shows the path and the parse error, disables every write, and re-reads on `r`. It never overwrites or moves the file. Recovery is a manual edit. |
| `[model-server]` malformed or contradictory | Shown as problems with the field named; apply replaces the table. |
| `~/.config/wt-model/` missing or `litellm.key` unreadable | Status shows "pair: unset/unreadable"; a `local` apply rewrites it (see below). |
| token file missing, loose mode, or unreadable | Status shows which, with the `chmod 600` hint; apply creates or tightens it. |

### The setup package `internal/modelsetup` (admin side, screen-free)

Per-call seams, no package globals: file system, `launchctl`, `Ping`, a reader for the LiteLLM plist environment, random bytes, and the running binary's location. A test never touches launchd or the real config home.

- `Observe(env) State`: read-only and total (it never errors; each part carries its own problem). Parts: the config table, the token file, wt-model's pair, the LaunchAgent (plist present? loaded? running? pid), and a `Ping` result when a url is known.
- `Plan(state, want) (Plan, error)`: pure. `Plan.Format()` is deterministic and is the text the user confirms; `Apply` writes exactly what it shows.
- `Apply(env, plan, progress) (Result, error)`: executes the steps in order and stops at the first failure, saying which steps ran. Idempotent: re-applying the same desired state changes nothing.

**`native` plan:** set `mode = "native"` (preserving other tables). If a wt-owned LaunchAgent is installed, offer to unload and remove it (listed in the plan; default keep).

**`remote` plan:** inputs url and token (a path to an existing 0600 file, or a pasted token written to `~/.config/agent-wt/remote-model-server.token`, mode 0600; one remote at a time, so the name is fixed). A pasted token that differs from an existing file at that path replaces it only after the plan's confirm names the replacement; an identical one changes nothing. `Ping` first. A pass: write the token file if pasted, then `[model-server]`. A failure shows the classified error and offers to save anyway (default No), because the remote machine may be off. The same offer to remove an installed local LaunchAgent applies.

**`local` plan**, in this order, config last so a half-applied setup is never pointed at:

1. Token: create `~/.config/wt-model/server.token` (32 random bytes, base64, mode 0600) if absent; keep an existing valid one.
2. wt-model's LiteLLM pair: keep it if set. Otherwise take `LITELLM_MASTER_KEY` from the proxy plist's `EnvironmentVariables` (the existing plist reader; no code asks for that key today) and the url from the plist's `--port` argument (default `http://localhost:4000`), and write them with `config.SetProxy`. If the key cannot be found, the tab asks for it (masked input) rather than failing.
3. Port and ownership checks, then the LaunchAgent. If something already listens on the chosen port and it is not this LaunchAgent's own process, refuse, naming the process and offering another port (a hand-started wt-model is not adopted: it would not survive a reboot, and a second bind would loop under `KeepAlive`). A plist already at the target path is overwritten only if it carries wt's marker (an XML comment `managed by wt` naming the label); one without the marker is refused, naming the path. Write `~/Library/LaunchAgents/local.wt-model.serve.plist` with label `local.wt-model.serve`, program arguments `[<wt-model>, serve, --listen, 127.0.0.1:<port>, --token-file, <token>]`, `RunAtLoad` and `KeepAlive` true (the same lifecycle as the LiteLLM agent: both start at login, and wt-model needs LiteLLM only when proxying), log paths under the user's home, and an `EnvironmentVariables` `PATH` captured from the process running the setup (shown in the plan; without it the agent cannot find ollama, omlx, mtplx, rec or sushi). `<wt-model>` is the absolute path of the `wt-model` binary beside the running `wt` (`os.Executable`'s directory), else the first `wt-model` on `PATH`, written as found (not symlink-resolved, so a stable install path survives upgrades); if neither exists the plan fails and names `make install`. Port defaults to 8787 (the documented example) and is editable; the tab refuses a port that is already listening and is not ours.
4. Load: `launchctl bootout` (ignore not-loaded) then `bootstrap`, or `kickstart -k` when only arguments are unchanged.
5. Wait for the server: `Ping` until it answers (bounded; progress shown), reporting the classified error on timeout and pointing at the log.
6. Write `[model-server]` (`mode`, `url = http://127.0.0.1:<port>`, `token_file`).

Re-running `local` with a changed port or token path rewrites the plist and reloads. Changing away from `local` offers step 3's inverse (bootout and remove the plist); it never deletes the token file or the pair.

### LiteLLM: scope and guide

**What the tool does** (option B, chosen by the user, scoped to "wt never installs LiteLLM"), all in `internal/modelsetup`, all shown as a plan first:

- **Observe and check** (read-only, any time, for `local`): proxy reachable (`/health/liveliness`); database connected (`/health/readiness`, informational, since only spend and the admin UI need it); the pair's key accepted (authenticated `/v1/models`: 200 versus 401, reported as "key accepted/rejected", never as proof it is the master key); routes in sync (the proxy's model list against the set wt would write); `config.yaml` newer than the running proxy (restart owed); the plist's `--port` agreeing with the pair's url; the plist's file mode. It cannot judge the salt key or provider keys; the tab says so rather than guessing. Reporting these for a `remote` server would need a new read-only wt-model control endpoint (a wire change) and is not included.
- **Create when absent.** If no LiteLLM plist exists and a `litellm` binary does, offer a minimal LaunchAgent: label `local.litellm.proxy` (must match what `wt litellm restart` targets), `[litellm, --config <config.yaml>, --port 4000]`, `EnvironmentVariables` with a generated `LITELLM_MASTER_KEY`, a generated `LITELLM_SALT_KEY` and the captured `PATH`, `RunAtLoad`/`KeepAlive`, log paths, mode 0600. No `DATABASE_URL`, UI login or provider keys: the plan says so and points at the guide. If `litellm` or `config.yaml` is missing it refuses naming the guide.
- **Existing plist: one key, on request.** The tab offers "use the plist's master key" (default: read it into wt-model's pair, no plist edit) or "generate a new master key". The second edits only `EnvironmentVariables.LITELLM_MASTER_KEY` in place (`plutil -replace`, or a plist library, so every other key survives), after a dated backup of the plist, tightens the file to 0600 when the plan says so, writes the same key to wt-model's pair, restarts LiteLLM and wt-model, and lists what else may hold the old key (benchmark scripts that read the plist, other clients). It never touches the salt key, `DATABASE_URL`, provider keys or the UI login, and never replaces the file.
- A binary (bplist) plist is converted or refused with a clear message; the plan shows the exact key change and nothing else.

**What the guide is.** One guide, `docs/guides/14-litellm-install-and-configuration.md` (number and title a builder's call), written for a human or an agent on a fresh machine and kept free of live state: install LiteLLM (`uv tool install --python 3.11 'litellm[proxy,extra-proxy]'`, why 3.11 and `extra-proxy`), Postgres 16 and database creation, Prisma client generation (migrations run at startup, no `db push`), the seed `config.yaml` and the fact that wt owns it, **an annotated plist listing every `EnvironmentVariables` key and what it is** (master key and salt key generation commands; the salt-key never-change warning and a backup note; UI login; provider keys and how `os.environ/NAME` references resolve; the size/logging limits; `SoftResourceLimits`), file mode 0600, `launchctl bootstrap`, ports and logs, how wt restarts it (`wt litellm restart`, the drain, `WT_LITELLM_RESTART_CMD`, `WT_LITELLM_PLIST`), how `bin/update/litellm` upgrades it, linking the master key to wt-model (`wt-model litellm set`, and a wt-model restart afterwards), verification (liveliness, readiness, the authenticated check, `wt litellm status|list`), what the `wt config` Model server tab will and will not do for it, a wt-model LaunchAgent template, and troubleshooting. It replaces the three drifting plist templates and conflicting restart advice by linking to one source.

**Doc fixes that ride with it** (found by the audit): guide 01's malformed `<key>StandardErrorPath</string>`; the reference doc's `prisma db push` advice against the automatic migrations; the "verified against 1.98.0" headers; hard-coded `/Users/keith` paths in guides 01 and 04; conflicting restart advice in guides 01, 04, 08 and the reference doc (use `wt litellm restart`); the plist's 0600 mode (absent everywhere); and guide 00's and 01's wording that the plist is "owner: you", which changes once the tool can create it and rotate one key.

### The tab

```
 Agents   Models   Model server
 ─────────────────────────────────────────────────────────────
 Mode     ( ) native   (•) local   ( ) remote
 URL      http://127.0.0.1:8787                     [port 8787]
 Token    ~/.config/wt-model/server.token           0600 ok
 Pair     LiteLLM http://127.0.0.1:4000             key set
 Agent    local.wt-model.serve: running (pid 4123)
 Status   ✓ wt-model answers: api 1, schema 2
 LiteLLM  ✓ reachable · db connected · key accepted · 12 routes in sync
          ! plist is mode 0644 and holds secrets (chmod 600?)
 env      WT_MODEL_URL is set and overrides the url above
 ─────────────────────────────────────────────────────────────
 a apply   c check   s start   x stop   r re-read   k master key   tab next
```

Shown fields depend on the mode. `a` builds the plan and shows its text on a confirm screen (default No); `c` runs `Observe` and a `Ping` without writing; `s`/`x`/`r` start, stop and re-read the LaunchAgent for `local`. Selecting `local` on a non-macOS host is disabled with the reason. The tab joins `tabNames`, gets a `TabModelServer` constant, `Options.StartTab` can open it, and its deps arrive as an injected `ModelServerDeps` like `ModelsDeps`. It uses `themes.ListDelegate`/`ErrorStyle` and `tuilayout` only; nothing prints during the alt screen.

### First run

`runConfigEditor` gains a start-tab argument for the gate. When the gate opens the tab and the user leaves without a stored mode, the launch ends (exit 1, "no model-server mode chosen; nothing was launched") rather than guessing. Choosing `native` explicitly is how a user asks for native launches.

### Where code lives

- `internal/modelsetup` (new, admin side): added to `adminBanned` in `internal/depcheck` so no session library package links it. It may import `config`, `tokenfile`, `modelclient`, `litellm` (plist env reader) and `provider`.
- `internal/configeditor`: the tab and its deps type. It already links admin packages and stays off `tui`.
- `internal/config`: `ModelServerState.Mode`, `omitempty`, a tolerant table reader and a table writer over `PatchSave`.
- `internal/modelclient`: `ResolveMode`, the rewritten `NotConfiguredError`, a `ModeError` for invalid combinations.
- `cmd/wt-session`: the gate at the existing pre-flight points, `WT_MODEL_MODE`, the `ModelServerDeps` wiring in `commands_config.go`.

### Testing

Per-call seams and a per-package `TestMain` isolating the config home and processes (`config.IsolateConfigHomeForTest`, `lifecycle.IsolateProcessesForTest`); a `launchctl` seam that refuses inside a test binary. `modelsetup`: plan text goldens (including the plist), idempotence, order and stop-on-failure per mode, token modes, the pair fallbacks. `modelclient`: a `ResolveMode` table over env × table × inference. `cmd/wt-session`: the launch matrix above replaces and extends the seven `native_test.go` tests and the `models_test.go` refusal cases. The tab: through `Update` against a fake `ModelServerDeps` (syntax-error state disables writes; env override shown; confirm default-No). A fake model server answers `Ping` with each failure shape. `depcheck` gains `modelsetup` in the admin set.

### Docs

The new LiteLLM guide and the fixes above; `wt/CLAUDE.md` ("LiteLLM routing state", "Config"), `docs/wt-session.md`, `docs/wt-model-server.md` (setup now starts with the tab), `docs/guides` 00 and 13, the `wt config` page, the #615 spec (a "superseded in part" note), `docs/internals/packages.md`, and the style guide (a pick-one first-run flow is handled by opening the tab, so no new prompt rule is needed).

## Suggested stages (each independently shippable, additive first)

0. **The LiteLLM guide and the doc fixes above.** Documentation only, no code dependency, and the tool's messages will link to it, so it ships first.
1. `ModelServerState.Mode` + `omitempty`, the tolerant table reader/writer, and `internal/modelsetup` (`Observe` with the LiteLLM checks, `Plan`, `Apply` for the wt-model agent) with its seams. No behaviour change.
2. The Model server tab in `wt config` (reachable by hand; nothing launches differently yet).
3. `ResolveMode`, the launch matrix, the first-run gate, `WT_MODEL_MODE`, message rewrites. This is the behaviour change, made after a tab exists to point at.
4. The LiteLLM plist writer: create-when-absent and the one-key master-key edit (it edits a secrets file, so it is its own stage and its own review).
5. Docs and `wt/CLAUDE.md` sweep (rides with each stage).

## Calls I made (not the user's words)

- The names `native`, `local`, `remote`.
- Mode lives in the existing `[model-server]` table of `config.toml`, not in a new file; env overrides it (`WT_MODEL_MODE` added beside the existing `WT_MODEL_*`).
- A first-run launch opens the tab instead of an inline prompt; one-shot commands refuse with the fix rather than open a TUI.
- With no terminal and no mode: refuse (exit 1), not native with a note.
- A syntax-broken `config.toml` is never auto-repaired or moved aside; the tab explains and waits.
- A second LaunchAgent (`local.wt-model.serve`), not a wrapper around the LiteLLM plist; loopback only, port 8787 by default; `PATH` captured from the applying process.
- `remote` accepts a pasted token and writes the 0600 file; a failed check offers "save anyway" (default No).
- The config is written last in every plan.
- Environment rules: `WT_MODEL_MODE` beats everything; an env endpoint-plus-token pair acts as `remote` over any stored mode (so existing scripts keep working); env counts as supplying a field for validation.
- Only launches prompt; commands that need a server refuse with the pointer; commands that never build a model session are untouched (list above).
- A hand-started wt-model on the chosen port is refused, not adopted; a plist is overwritten only when it carries wt's marker.
- `wt-model` is found beside the running `wt`, else on `PATH`, recorded as found; a missing binary fails the plan naming `make install`.
- Changing away from `local` offers to remove the LaunchAgent (default keep) and never deletes the token or the pair.
- LiteLLM scope is option B as the user chose, read as "wt never installs LiteLLM": minimal plist only when none exists and the binary does; otherwise one key; everything else lives in the guide.
- The guide is stage 0 and a single new guide rather than edits spread over 01, 04 and the reference doc; the LiteLLM checks are local-only (a remote report needs a wire change and is excluded).
- The key check says "accepted/rejected", never "is the master key".

## Left to the builder

The LaunchAgent log paths; exact wording of the messages and the tab's keys within the style guide; how the port-in-use probe finds the owning process; how `Observe` reads launchd state (`launchctl print` parsing) and its timeouts; whether the masked key entry reuses an existing text-input helper; the bounded wait's duration; `ModelServerDeps` field names; whether the stage 1 package is split into more files; `plutil -replace` versus a plist library for the one-key edit, and bplist handling; the guide's number and exact outline.

## As built

Recorded after the build (2026-11-18), from the execution ledger. Everything above stays as approved; this section lists where the shipped code differs from it and what was decided on the way.

### Deviations from the spec

- **ASCII words, not glyphs.** The tab shows words (style guide V5), not the sketch's glyphs.
- **The master-key edit is a text edit, not `plutil -replace`.** `ReplaceMasterKey` locates the one `LITELLM_MASTER_KEY` structurally (root dict, `EnvironmentVariables`, the key's following `<string>`) with `encoding/xml` offsets, splices only that value, and verifies the result (parses, carries the new key, every other variable unchanged). The key never appears in argv and the untouched bytes stay identical. A key text in a comment, a CDATA value, a binary plist or a duplicate is refused.
- **`Apply` returns one `Result`**, with `Notes` (for example the backup path) and `Recovery` (a command that undoes or abandons the step).
- **Inference to `remote` on any endpoint value**, stored or from the environment, not only a url plus token file, so a half-configured server still refuses as before.
- **A type error elsewhere in `config.toml` also blocks a write.** `UpdateModelServer` refuses any decode error with the file untouched (re-encoding would drop what it could not decode); `ReadModelServerFile` stays tolerant.
- **`NativeNote` is reworded**, and an explicit `native` ignores stray `url`/`token_file` values silently; an inline `token` earns one `wt: note:` (never the value) instead of a refusal.
- **The unset-mode refusal keeps the old lead** that existing tests and llmbench key on; only its tail changed (it names `wt config` and `WT_MODEL_MODE`). Exit code 1 for the no-terminal case.
- **The #615 reversal.** The no-config launch is no longer native by default: with no mode chosen a launch refuses, or opens the tab. Native is only the explicit value.
- **The first-run gate** (`ensureMode`) runs once per launch, on a terminal only, before any worktree is created; it applies to model-driven launches, not command agents, and one-shot commands refuse instead of opening a TUI. `wt -A shell` needs no mode and never opens it; a bare `wt` on a terminal opens it before the agent picker, even for a user who then picks `shell` (by design; `errNoModeChosen` ends that launch when no mode is chosen).
- **Rotate's backup lives in `<config home>/wt-model/backups/`** (0700, created or tightened by `realFS.Backup`), not dated beside the plist: a copy in `~/Library/LaunchAgents` carries the same label and the old key, and launchd or Background Task Management could load or register it. The backup step first dry-runs `ReplaceMasterKey` and the `DATABASE_URL`-without-salt guard on the plist as it is at apply time, so a refusal there leaves no backup (`Recovery`: nothing was changed).
- **Quit held for a failed apply is called off**, not completed after the result lands (a quit would hide the failure); a held quit during a successful apply that has notes is called off too, so the backup path is read.

### Decisions made during the build

- **Secrets.** `Desired.PastedToken` and `Desired.MasterKey` are `modelsetup.Secret` (redacting under `%v`, `%+v`, `%#v` and JSON; `Reveal()` only at the point of use). `Observe` strips userinfo from a stored url in `State` and reports it as a problem.
- **Token precedence mirrors the session's `modelclient.ResolveEndpoint`.** `Observe` reads the token the way a session would: `WT_MODEL_TOKEN_FILE` (env) wins and never falls back to the literal; otherwise the config's `token_file` (with `~` expanded) is used unless `WT_MODEL_TOKEN` is set, in which case that literal is used. A token read from a file is trimmed; an env literal is used untrimmed (so a file with a trailing newline and the same text as an env literal are not the same token). Only in `local` mode with nothing set does the local agent's own token file apply, which a session would not do. An inline `[model-server] token` is blanked in `State` (never held) and reported as a problem.
- **`ServerState` records the pinged url and token source**, and `BuildPlan` treats a check of any other url as not checked; the tab pings the ENTERED url and token before a remote plan.
- **Tab safeguards added in review:** a `planWant` snapshot taken at `a` (the apply runs it, not later keystrokes); form keys inert while a plan or apply is in flight; prompts (`[y/N]`, default No) visible at every terminal size; a parse error in `config.toml` disables every write path; the clip drops lower-ranked status rows first and marks them; a held quit.
- **Tolerant tab on a broken `config.toml`:** `wt config` opens with a syntax-broken file and says so with the path; nothing is repaired or moved aside.
- **LiteLLM actions** (Create, Rotate, Tighten on the tab's `k` and `t`) gained these safeguards in review: the edit and its backup are written at mode `perm &^ 0o077` (secrets are never group/other readable, backups included); backup names never collide; Create refuses a foreign listener on port 4000 at plan time and, after starting, requires the listener to be the job it started and one authenticated request with the new key to be accepted; Rotate refuses a plist with `DATABASE_URL` and no salt key and points to guide 14 section 6; Rotate waits for LiteLLM after the restart and checks the new key; `bootout` is polled until the job is gone before `bootstrap`; the plan is validated before it is rendered; `LiteLLMTighten` is a chmod-only action; "use the plist's key" re-pairs even over a set pair; the tab shows `Notes` and `Recovery` on a scrollable result screen and scrubs `sk-` keys from anything it draws.
- **Final-review hardening.** A typed url (or listen address) with a `user:password` is refused, naming only the host (config.toml is 0644); TOML errors on a `token`/`api_key` line and XML decoder errors from the LiteLLM plist are reported without their text; the wt-model agent plist write and removal re-read the file at apply time (absent, or managed by wt); `BuildPlan` refuses a `[model-server]` table it cannot decode (`TableErr`; the table cannot be fixed through the tab, since `UpdateModelServer` refuses the same file) and, for a plan that writes `config.toml`, a decode error elsewhere in the file (`DecodeErr`, new on `config.ModelServerFile`); a plan that runs `kickstart -k` or `bootout` on a running job says that requests in flight are cut; under `testing.Testing()` the real file seams refuse paths outside `os.TempDir()` and the real home seam refuses a HOME outside it (`realNet` is unchanged: it reaches the network, not files). A discard held for two writes in flight now survives the first one (cleared only when the quit happens).
- **Real `launchd` was not exercised.** The Stage 4 gate ran no live Create or Rotate (a scratch HOME does not isolate the per-user launchd domain, so it would have touched the real `local.litellm.proxy` label); `Apply` is covered by a fake `Launchd` and real-filesystem tests for the backup and mode. One manual live check is still owed.

### Known limitations and follow-ups

- Create's listener check requires the listener pid to equal the job's pid, so a `litellm` that is a shell wrapper or runs several workers can fail a correct Create (safe: the pair is not written, `Recovery` is given). Accepting a listener whose parent is the job pid would fix it.
- Rotate's key check after the restart is one-shot: an old process finishing its shutdown can answer and then reject, a false failure with consistent state and a correct `Recovery`.
- A plist holding the master key twice is offered Rotate (the menu checks only that a key exists) and refused at apply by the dry run before the backup, so nothing is written.
- Create checks that the LiteLLM plist is absent and then writes it (stat, then rename): a plist that appears in between is replaced. `os.Link` into place would close the race.
- No lock is held around `Apply`: two `wt config` sessions can interleave. A Rotate still ends consistent (each re-reads the plist), but two Creates may leave the pair and the plist holding different keys until `keyAccepted` on the next apply catches it.
- A plist that carries a copied `managed by wt` marker and the `local.wt-model.serve` label reads as wt's, so it can be removed after an explicit removal request and `y`.
- Create is port 4000 only and does not boot out another plist's label that is already running.
- The tab's key scrub is a prefix heuristic (`sk-` followed by a long key run, with a word boundary): a raw hex salt key, a token without that prefix and a hyphen-prefixed key are not caught.
- Pressing `c` or `r` while the pre-plan observation or the plan itself is in flight drops that plan; the status line says so (it begins `planning cancelled; `, then the new read's text).
- Smaller items stay with the ledger and the review notes (for example a 403 from LiteLLM reads as "key rejected", the `--port=4000` argument form is not recognised, and a listener check on a URL with no port is untested).
