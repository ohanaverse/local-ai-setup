# Split `wt` into `wt-model` (model server) and `wt-session` (session launcher)

Status: draft for approval, 2026-11-15. Calls the author made without the user's say are marked **[call]**. Everything else was decided by the user.

## Intent and why

`wt` today does two jobs in one binary and one process: it **serves models** (registry, provider lifecycle for ollama/omlx/mtplx/rec/sushi, LiteLLM routes, tiers, cloud sync, spend) and it **runs coding-agent sessions** (worktree/branch choice, agent choice, model choice, launch, cleanup). The two share files and in-process calls.

The goal is that the model half runs on its own, as a server, so the session half can live somewhere else:

1. `wt-session` in an isolated VM, `wt-model` on the host.
2. `wt-session` on one physical machine, `wt-model` on another.
3. Both on one machine.

The VM is a sandbox with minimal network access. Whatever `wt-session` needs from models must reach it over **one** network connection, to one address.

## Goals

- `wt-model` runs and is useful with no `wt-session` present: a model server with a CLI, a TUI for administration, and an API.
- `wt-session` has no model configuration of its own: no `registry.toml`, no provider origins, no LiteLLM config, no `tiers.toml`. Its entire model setup is one endpoint and one token.
- **The session's model access is read-only plus start/stop.** From `wt-session` a user can see the catalog (live state, tier ranking, context, price), resolve a `-M` pin, start and stop **local** models on the `wt-model` server (including `--replace` / `--others`), and run `smoke` on the client. A session can **not** edit the registry, update prices, refresh tiers, or run cloud-sync. Those administrative actions exist only on the `wt-model` host (CLI and TUI) and are not exposed over the network API at all.
- **Many sessions, one server.** Several `wt-session`s, on one or more machines, can use one `wt-model` at once. Sessions are tracked individually so the server knows which local models are in use.
- **Look and feel are the same across both tools.** A style guide for CLI and TUI standards across every wt tool is a deliverable of this work (see Migration, step 0), and both binaries share the TUI building blocks (`tuilayout`, `themes`).
- One code path for all three use cases (case 3 is loopback to the same API).
- The sandbox needs exactly one open connection (to `wt-model`'s listen address), for both control and inference traffic.

## Non-goals

- Not a multi-user service with accounts: one token, one trust level. The intended population is one developer with several sessions on several machines; the design must not assume exactly one session.
- No TLS, token scopes, or user accounts in v1 (see Security).
- No new gateway protocol translation; LiteLLM keeps doing that.
- No change to how providers are started/stopped, to the registry schema, or to modeltiers (it still reads `model list --json`, on the host).
- Not solved here (tracked as follow-up issues): mixing Claude subscription models with open-source models across slots through the gateway; slot profiles built on LiteLLM aliases/groups/routing; hot-reload of LiteLLM routes.

## Anti-goals (would make this a failure even if it works)

- Two code paths (in-process vs remote) that drift. Same-machine sessions use the same API as remote ones.
- A `wt-session` that silently starts or falls back to a local `wt-model`. The user must say where `wt-model serve` runs.
- Putting the LiteLLM master key, provider keys or any `wt-model` credential on the session side. (A credential the agent itself already owns, such as a subscription login for a native agent, stays wherever it is today.)
- A session able to change what models exist, what they cost, or how they rank.

## Constraints from the current code

- `internal/agents` imports only `config`, `profiles`, `tiers`; the model side (`config`, `provider`, `localmodels`, `litellm`, `lifecycle`, `catalog`, `cloudsync`, `modeladmin`, `spend`) is already mostly separable. `internal/tui` (~36k lines) mixes the model picker, the model-admin screens and the worktree-cleanup screens, and is the main untangling job.
- `config.ResolveRoute(model, agentProtocols)` builds the agent's base origin/key today, including the "direct" path (agents dialling providers) and pi's `models.json` rewriting. It becomes: gateway origin + token + gateway model id.
- A LiteLLM route change rewrites `config.yaml` and restarts the proxy (drains up to 3 minutes, refusing new connections). Today this is partly avoided by direct routes. **Accepted consequence of the user's decision:** every local start/stop now goes through that restart. Mitigation (hot-reload of routes) is a follow-up.
- Start/stop is slow (a ~28 GB model loads for minutes) and carries interactive questions (occupancy `*OccupiedError`, `Options.Others`, `--replace`).
- Session-use tracking (`internal/refcount`, `sessionclaim`) is pid-based and file-based and cannot work across machines.
- Repo rules that still apply: wt is the only writer of `registry.toml` and `config.yaml`; contracts in `docs/contracts/` are fixtures read by tests; guides never embed live model state.

## Approaches considered

- **A. `wt-model serve`: one daemon, one port, control + gateway. (chosen)** `/v1/*` reverse-proxies to LiteLLM; `/control/*` is a JSON API; one bearer token. Same code path everywhere.
- **B. CLI over a transport** (`wt-model … --json` through ssh/vsock). No control daemon, but the inference path still needs its own port (two connections), and the sandbox would hold an ssh credential. Rejected.
- **C. In-process library plus a remote adapter.** Smallest change for same-machine use, but two code paths that drift. Rejected.

User decisions on A: LiteLLM-only gateway; **all sessions, local included, use the gateway path (direct routes are removed)**; `wt-session` never auto-starts `wt-model`; the user must be explicit about where `wt-model serve` runs; start is explicit, stop is automatic (below); `wt-model` gets its own TUI; the gateway never forwards session credentials.

## Design

### Repo and binaries

One repo, one Go module, two binaries: `cmd/wt-model` and `cmd/wt-session`. **[call]** The contract between them is the HTTP API plus fixtures in `docs/contracts/`, exactly how llmbench and modeltiers already read wt output. Shared types live in a small package both import (the API's request/response types and the catalog row), not in `config`.

The `wt` name and `*-wt` shims **[call]** stay pointing at `wt-session` (the launcher is what users type).

### What lives where

| `wt-model` (host) | `wt-session` (client) |
|---|---|
| `registry.toml`, LiteLLM `config.yaml`, `tiers.toml` (read; modeltiers writes it on the host) | `config.toml` (agents, default tag, `[launch]`), `slot-profiles.toml`, profile backups |
| providers, lifecycle (start/stop/auto-stop), localmodels probes, catalog, tiers read, cloud-sync, spend, `model list --json`, the admin CLI and TUI | worktree/branch/agent/model pickers (read-only catalog), launch, `smoke`, cleanup (`worktrees`, `remove`, `rescue`, `restore-root`), `rotation.state`, `last-picked.state`, `usage.jsonl` **[call]** (session history) |
| the gateway and control API, the session registry | endpoint + token config, an API client |

The session-side `[litellm]` table (`enabled/url/api_key`) is **removed**: routing is always through the gateway; `url` and the token become `wt-session`'s `[model-server]` (name is the builder's). `wt litellm on|off` is retired.

**Command homes.** On the host (`wt-model`): `model init|add|edit|rm|list`, `cloud-sync`, `litellm sync|list|providers|status|restart`, `start`, `stop`, the Models-tab TUI. In `wt-session`: the launcher, `start`/`stop` (API clients), `smoke`, `model list` (read-only), `litellm list|status` (read-only), `profile slots`, `stats`, and the cleanup commands. The admin commands on the old `wt` name (`model add|edit|rm|init`, `cloud-sync`, `litellm sync|restart|on|off`, the `wt config` Models tab) stop working in `wt-session` and print a pointer to `wt-model`; read-only and start/stop/smoke commands keep today's flags, output, JSON shapes and exit codes (the `wt-manage-slot-profiles` skill depends on them). The `wt-cloud-sync` skill is updated to drive `wt-model cloud-sync` on the host.

### Control API (JSON over HTTP)

All under `/control/`, bearer token required. The builder shapes the exact schema; the capabilities are fixed. **There are no registry-write, price-update, tier-update or cloud-sync endpoints.**

- **Catalog:** the picker's rows (what `catalog` builds today), including per-row state (`running`/`loading`/`stopped`/`blocked` + reason), action (launch/start/block), tier, context window, price, family, tags, protocols, which sessions are using it, and the **gateway model id** an agent must send. Slot-profile candidates come from here too. Carries a schema version.
- **Resolve:** model pin → row or the `MissingReason`.
- **Start / stop (local models only):** each is a **job**. `POST` returns a job id. `GET /control/v1/jobs/{id}` is a pollable status endpoint (state `queued|running|succeeded|failed|cancelled`, a human-readable step and progress where known, the typed result or error). A streaming variant (SSE or NDJSON, builder's choice) may sit on top, but polling is the baseline. Prompts become typed errors in the job result: `occupied` (needs `replace`), `others` (needs `others=keep|stop`), `occupancy_unknown`; the client answers by re-posting with the choice, so the interactive questions stay in the client UI. A stop the provider accepted but has not finished (`lifecycle.StillFinishingError`, ollama) is not a typed error: the stop job succeeds and its step text reads `still finishing` (style guide S5). A start job does not succeed until the model is **routed and answering through the gateway** (this replaces `WaitPendingRoutes`: the server owns the route restart and its wait). An explicit stop of a model that other live client sessions use needs the client to pass a confirmation flag; the response names those client sessions.
- **Sessions** (replaces pid-based `refcount` for models): the session creates its own **session id** (a UUID) and registers it with the models it uses (`POST /control/sessions`), **heartbeats** while it is alive, and **ends** the session explicitly on exit. A session with no heartbeat for the TTL is considered done. See Auto-stop.
- **Read-only support data:** candidate lists and tier data for `wt profile slots`, spend rollups for `wt stats` (from the server's LiteLLM Postgres).

### Auto-stop (start is explicit, stop is automatic)

A **start** is always an explicit client request (a 28 GB load plus a proxy restart is too costly to trigger from a stray request). **Stop** is automatic for local models: when no live client session uses a local model that wt-model's session registry knows about, and it has been unused for the **idle grace period** (**[call]** default 10 minutes, a `wt-model serve` setting), the server stops it through the same lifecycle engine (route removal included, under the route serializer). Cloud models and models the server did not start for a session are never auto-stopped **[call]** (a model started by hand on the host with no session ever registered is left alone). A session's end (explicit or by missed heartbeats) starts the grace clock; a new session using the model cancels it. Clients can still request an explicit stop. The session-side post-exit stop picker is replaced by this behavior; the client's exit step reduces to ending its session.

### Gateway

`/v1/*` is a streaming reverse proxy to LiteLLM, supporting both the Anthropic and OpenAI protocols LiteLLM already serves. The client's token is swapped for the LiteLLM key on the way through, so the LiteLLM key and every provider key stay on the host. **The gateway never forwards a session's own `Authorization` upstream.** Gateway model ids are those in the catalog; the session sends them as today's routes do. Models that run on a subscription login owned by the agent itself (today's `claude/native` and `copilot/native` rows) are not routed through the gateway and keep working as they do now; mixing such a slot with gateway models in one Claude Code session is a follow-up issue.

### `smoke` runs on the client

`wt smoke` stays in `wt-session`: it starts the model through the API when needed and then runs the real agent configuration through the gateway, so it proves that the **client's** setup works with the chosen remote model (the thing a server-side check cannot show). It keeps its TTY prompts; its exit stop prompt becomes "end session" (auto-stop handles the model). **[call]** `wt-model` also gets a plain server-side `check` command (model answers through the gateway) as a quick host-side sanity test; it is not a substitute for `wt smoke`.

### Connection and configuration

- `wt-model serve --listen ADDR --token-file PATH`. **No default listen address, and a token is always required** (also on loopback): without one the gateway could not be used and the control API would be open. A token file readable by group or others refuses to start. **[call]**
- `wt-session` reads its endpoint and token from its config (token from a file or env var). **If either is missing it stops with a message naming both settings and the command to run `wt-model serve`; it never starts `wt-model` or guesses an address.** Case 3 users run `wt-model serve --listen 127.0.0.1:PORT` themselves and point the session at it.
- A session whose server is unreachable fails fast with the address it tried; it does not fall back to anything local. (Carve-out, #615: with *no* server configured at all, a launch that asks for no model runs the agent natively with its own configuration, using no wt-model route; a half-configured or unreachable server never does. See `2026-11-16-wt-native-launch-without-model-server-design.md`.)

### Security (v1)

One shared bearer token over plain HTTP. The token grants: read the catalog, start/stop local models, register sessions, use the gateway. It grants **no** administrative capability. The intended deployment is a VM-to-host link that the host controls. **[call]** Documented limits: no TLS, no scopes; anyone holding the token can load or stop local models and spend on routed cloud models. Binding to a non-loopback address is an explicit user act. The gateway forwards only the inference paths with the host LiteLLM key — the inference-path allowlist landed in Phase 2 (see Migration status) — so a token holder cannot reach LiteLLM's other `/v1` routes (files, batches, model info), which the gateway does not serve. TLS and scoped tokens are later work; the API's auth is one middleware so adding them does not change handlers.

### Further decisions (from the builder check; all **[call]** unless marked)

- **Gateway ids and routes.** The catalog lists a gateway model id for every row, stopped ones included; the id is the stable canonical model id. Routes stay *derived* as today: created at start, removed at stop (local), always present for cloud. A start job ends only when the id answers through the gateway.
- **Concurrent route changes.** All route changes are serialized in the server (one mutex; changes arriving during a restart coalesce into one). The existing drain semantics are kept: in-flight streams finish (up to 3 minutes, then are cut as today). While the proxy restarts, the gateway returns `503` with `Retry-After`. The agent owns its gateway retries (R12): wt retries only what it drives itself — `WaitGatewayReady` before a `smoke` row, and once more when a row FAILs while the gateway answers `503`, a row that still sees `503` reading SKIP, never FAIL — and an agent that does not retry is a recorded limit (#598). A session starting or auto-stopping a model can therefore stall other agents for the length of a drain; this is the accepted cost of dropping direct routes.
- **Sessions.** Heartbeat every 20 s, TTL 60 s. A session lists the models it uses; it may add models later. Sessions are advisory for launch (not required to launch). `sessionclaim`/`rootclaim` (worktree `in_use`, `root_held`) stay local and file-based on the session side; they are not model state.
- **Versioning and skew.** The API has a major version in the path and the catalog a schema version. A client refuses a server with a different major and says which side to upgrade; a newer minor is accepted and unknown fields ignored.
- **Migration of existing installs.** No migration and no auto-start: the first run without a `[model-server]` endpoint stops before anything starts when it needs a model (since #615 a plain `-A <agent>` launch or the agent picker instead runs the agent natively, with one note line), with the refusal the session prints (`wt/docs/wt-session.md` quotes it and the per-command last clause): `wt: no model server is configured; [litellm] is no longer used by wt — set url and token_file under [model-server] in config.toml (or WT_MODEL_URL and WT_MODEL_TOKEN_FILE); start a server yourself with 'wt-model serve --listen ADDR --token-file PATH', because wt never starts one; nothing was launched`.
- **Slot profiles.** `wt profile slots add|set|candidates` need the live catalog and fail hard when the server is unreachable; `rm` and `usage` work offline. Slot profiles live in the session's config and are read-only consumers of the catalog.
- **pi and agent protocols.** Each agent receives the gateway's Anthropic or OpenAI path chosen from the protocols in the catalog row. pi's `models.json` is still written, but only ever pointing at the gateway; the direct-provider rewrite is deleted. `piCompat` (issues.md #6) stays out of scope.
- **Jobs.** In memory only. A job continues if its client disconnects; any client can poll or attach by id; Ctrl-C in the client detaches, and there is a separate explicit cancel. After a `wt-model serve` restart, jobs are gone and state is re-derived from live probes; the session registry is rebuilt from the next heartbeats (a model with no session known after a restart is not auto-stopped until a session has registered for it).
- **Process management.** `wt-model serve` is a foreground process. Running it at boot (a LaunchAgent) is documented as an optional recipe, not built.
- **Gateway hardening.** Drops any client `Authorization` header before forwarding, compares the token in constant time, refuses to start if the token file is group- or world-readable. It sets no idle timeout or body-size limit on streams (agent streams run for hours); LiteLLM's limits apply.

## Migration (phases)

0. **Style guide.** Write the CLI and TUI standards for all wt tools (`docs/`; output shapes, key footers, table and detail-screen conventions, exit codes, wording of warnings and errors, theme use), derived from what the shipped screens already do, and agree it before the split moves screens between binaries.
1. Define the API types and contract fixtures first; build `wt-model serve` (control API, jobs, sessions, auto-stop, gateway) around existing packages with no behavior change on the server side.
2. Add the API client in `wt-session`; make the picker, `-M` resolution, start/stop, `smoke` and launch routes consume it. Remove the direct-route path, pi's direct `models.json` rewrite, and `[litellm] enabled`; replace `refcount` and the stop picker with sessions.
3. Untangle `internal/tui` into model-admin screens (`wt-model`) and session screens (`wt-session`) along the same line, both following the style guide.
4. Move the model-admin commands and the Models-tab TUI to `wt-model`; leave pointers in `wt-session`; update the `wt-cloud-sync` skill.
5. Guides and the CLAUDE.md files (`wt`, root) are updated in the same change sets; `docs/guides/` gets a deployment guide for the three use cases (commands only, no live state).

**Phase 2 status (shipped 2026-11-15/16).** Step 2 is complete, with the boundary rulings the plan settled recorded here by number (full wording: `2026-11-15-wt-session-client.md`, §Scope, R1–R18): binary naming — `cmd/wt-session`, built and installed as `wt-session` and `wt`, shims unchanged (R1) — and the transitional dual path flipped and deleted (R2); the host commands kept in the session binary (R3, R17) reading the registry through `hostConfig()`, the host `[litellm]` key inert to sessions (R4), `protectedPaths` fail-closed as before (R10); one fixed wire per driver (R5), per-agent `supported_providers` eligibility (R6), the tri-state `routed` (R7); Ctrl-C detaches and cancel is explicit (R8); the stale-pricing notice on the session against the server's clock (R9, R16); the session token may reach pi's `models.json` and the agent env, written mode 0600 (R11); the agent owns its gateway retries and wt retries only what it drives (R12); session-only machines synthesize their agent list in memory (R13); `wt start` registers a client session around its job so the idle grace applies (R14); one exit-code table (R15); a killed wt cannot end its client session and the server's 60 s TTL does (R18). The gateway's inference-path allowlist (Security) landed with this phase — the gateway serves only the inference paths — and a stop the provider accepted but has not finished is the **succeeded** job of decision 9 (its step text begins `still finishing`), as Control API records. **Interim limit (R17, closed by Phase 4):** the admin commands (`model init|add|edit|rm|list`, `cloud-sync`, `litellm sync|list|providers|status|set|restart`, `served`, `warm`, `stop <provider>|--all`, the Models tab) still run in the session binary against the files of whichever machine runs them, and the `[litellm]` url/key stays in the `config.toml` the session also reads (as an inert table) — the state the anti-goals above call a failure for a session, temporary by ruling (`wt/docs/wt-session.md` "Interim limits" records it too). **Deferred:** Phase 3 takes the `internal/tui` untangling and the import graph (Phase 2 removed the calls, not the imports: the session binary still links `lifecycle`, `litellm`, `localmodels`); Phase 4 moves the admin commands and the Models tab to `wt-model` with pointers in `wt-session`, adds `wt-model litellm set` and its config home, `wt-model check`, the session's `litellm list|status`, the `wt-cloud-sync` skill rewrite and a host-side stale-pricing notice; the follow-up issues below (#596, #597, #598) stay open.

## Follow-up issues (known gaps this design does not solve)

- Slot profiles and LiteLLM aliases, groups and routing for agent configuration.
- Mixing Claude subscription models with open-source models across slots in one Claude Code session when routed through the gateway (how LiteLLM can carry a subscription is not yet understood).
- Hot-reload of LiteLLM routes, to remove the proxy restart/drain from every local start and stop.
- CLI/TUI style guide (phase 0 above, tracked as its own issue).

## Left to the builder

Exact JSON schema and versioning scheme; polling-only vs polling plus SSE/NDJSON; the shared-types package name; how the session config keys are named and laid out; port selection; how to stage the work into PRs.
