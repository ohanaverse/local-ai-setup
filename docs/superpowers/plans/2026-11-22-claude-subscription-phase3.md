# A Claude subscription behind wt-model (phase 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One Claude Code session mixes the owner's own Claude subscription (for example `fable` and `opus` on `claude-fable-5-2` and `claude-opus-5-5`) with other models (`sonnet` and `haiku` on `alias/...` ids or plain registry ids), all through wt-model's gateway, for local and remote sessions. The gateway gains an opt-in header mode (`x-wt-model-token`) that forwards the client's own claude.ai login in `Authorization`. The registry gains a `claude-subscription` provider whose rows are keyless `anthropic/<id>` LiteLLM rows. Those rows are offered only to the claude agent. A claude launch that names one keeps the login (no `ANTHROPIC_AUTH_TOKEN`), sends the wt token in `ANTHROPIC_CUSTOM_HEADERS`, and refuses to start without a claude.ai login or without the session machine's own opt-in (#597).

**Architecture:** Host side: `modelgateway.Config.SubscriptionPassthrough` (from a `[gateway]` table in the wt-model home `config.toml`, read once at `serve` start) turns on a second authentication branch in the gateway. It is chosen only when the request carries `x-wt-model-token`, runs only from a loopback peer (or TLS), and forwards only a `Bearer sk-ant-oat…` `Authorization`. When the header is absent the existing token swap runs unchanged. The ping advertises the mode. The catalog blocks subscription rows while it is off. Provider side: one family-less, cloud `Spec` in `internal/provider/table.go` with `Policy{Prefix: "anthropic/", Cloud: true}`. Its models use the real Claude id as registry id and gateway id, and are routed like every other cloud model (a `config.yaml` row with no `api_key` and no `api_base`), unless the proxy could leak or bill them (an `ANTHROPIC_API_KEY` in its env, header forwarding on, or a foreign `anthropic/` row anywhere in the proxy). Session side: `catalog` decides "this row uses the subscription" from the provider id, its own and its alias targets'. Eligibility gives such rows only to `claude`. The claude driver picks header mode for the whole session when any slot (or the `-M` model) uses one. Every claude launch, native included, strips an inherited wt token line from `ANTHROPIC_CUSTOM_HEADERS`. The four launch paths run one pre-launch guard before any client session or start job: the session machine opted in, the endpoint is https or loopback, the ping says the mode is on, Claude Code is new enough, and `claude auth status` shows a claude.ai login. `wt smoke` skips subscription rows unless they are named and opted in.

**Tech Stack:** Go 1.26.7 (`wt/` module), `net/http/httputil.ReverseProxy` (`Rewrite`), `net/netip`, `github.com/BurntSushi/toml` (home config), LiteLLM 1.109.1 (keyless `anthropic/` rows; it forwards a client OAuth token to every `anthropic/` deployment whatever its `api_base`, `ANTHROPIC_OAUTH_FORWARD_PROVIDERS`, spike 4), Claude Code >= 2.1.227 (`ANTHROPIC_CUSTOM_HEADERS`; checked on 2.1.330).

**Spec:** `docs/superpowers/specs/2026-11-22-litellm-aliases-groups-design.md` ("Design of A": Subscription models, Gateway, Claude launch; "Constraints and risks" 1; "Real subscription check"; decisions 2, 3, 4). Spike patch: `docs/superpowers/specs/2026-11-22-litellm-aliases-groups-spike/gateway-header-mode.patch`. Builds on phase 1 (`2026-11-22-litellm-db-routes-phase1.md`, PR #684) and phase 2 (`2026-11-22-litellm-aliases-groups-phase2.md`, PR #686).

## Global Constraints

Owner decisions (binding) and values from the spec and the code.

- The header name is `x-wt-model-token` (Go canonical form `X-Wt-Model-Token`).
- Subscription passthrough is for local and remote sessions alike.
- Passthrough is **off by default**, on both sides. The host operator turns it on with `[gateway] subscription_passthrough = true` in `~/.config/wt-model/config.toml` (`config.ProxyHomePath()`), then restarts serve. A session machine opts in separately with `[model-server] subscription = true` in `~/.config/agent-wt/config.toml` (decision 12). No production code path writes `true` to either key: not `wt-model setup`, not `litellm set`, not the Mode tab. Tests may write it only into a scratch home under `t.TempDir()`.
- The gateway refuses the mode from a non-loopback peer over plain http. Loopback is `127.0.0.0/8`, `::1` or `::ffff:127.x`, judged from `r.RemoteAddr`. A request whose `r.TLS != nil` is also accepted. `X-Forwarded-*`, `Forwarded` and `X-Real-IP` are never consulted to **allow** a request; they are read only to **refuse** one (decision 3).
- When `x-wt-model-token` is absent, the gateway is **byte-for-byte unchanged**: the existing tests `TestGatewayRejectsMissingOrWrongToken`, `TestGatewaySwapsCredentials`, `TestGatewayAcceptsXAPIKey`, `TestGatewayStreamsWithoutBuffering` and `TestGateway401IsOpenAIStyle` in `wt/internal/modelgateway/gateway_test.go` pass unmodified. That includes the existing pass-through of a client `x-litellm-api-key` in legacy mode (see "Not in this plan").
- Neither wt-model nor LiteLLM stores or logs the wt token or the client's login. No access log, and no `log`/`slog`/`fmt` print of a request, header or body, in `modelgateway` or `modelserver`. Tests (Tasks 1 and 2) and the live check (Task 9) pin this.
- Subscription rows (and every alias with a subscription target) are offered only to the agent `claude`. pi, codex, copilot and opencode never see them: not in eligibility, not in pi's `models.json`, not in rotation, not in smoke.
- The docs say: the owner's own login only. A shared host would be intermediating other people's credentials, which Anthropic's terms forbid and wt cannot detect.
- wt places the wt token only as the value of one process-environment entry, `ANTHROPIC_CUSTOM_HEADERS=…x-wt-model-token: <token>…`. wt never puts it in argv, the `--settings` JSON or a file, nor in `wt profile show` unmasked, and never sends it on a control API call (control keeps `Authorization: Bearer <token>` only). Claude Code's tool subprocesses and hooks inherit that env (as they inherit `ANTHROPIC_AUTH_TOKEN` today), so a header-mode session's shell carries the token; the docs say so, and every wt launch strips an inherited wt line (decision 7).
- Wire changes stay additive within `modelapi.APIMajor = 1` / `SchemaVersion = 2`. A new key reaches its fixture **and** its row on `wt/docs/wt-model-server.md` in the same commit: `docs/contracts/model-api.sample.json` (ping field, three gateway error members) and `docs/contracts/wt-model-config.sample.toml` (`[gateway]`). `docs/contracts/registry.sample.toml` gains one provider and one model; `registry.written.sample.toml` (generated with tomli-w, checked byte-for-byte) does not change.
- `serve`'s announce line (`announceFormat`, `wt/cmd/wt-model/serve.go:30`) stays byte-identical and first. The mode is reported on separate stderr lines printed after it (style guide S1 is amended in Task 8).
- `serve.json` (`servestate.Record`, `RecordVersion = 1`) does not change.
- Tests stay hermetic (`wt/CLAUDE.md` "Go tests"):
  - no real `claude`, proxy or psql; every serve-level gateway test points the upstream at an `httptest.NewServer`, never at `127.0.0.1:4000`;
  - TestMain isolation as each package has it;
  - **per-call seams** (a field on `hooks`, `Config.ErrorLog`, an injected login func), **never a new package var**; a nil `hooks.claudeLogin` in a test binary refuses (decision 8);
  - no test listens on a non-loopback address;
  - every `Test*` has a what-and-why comment;
  - `make pre-pr` passes on a machine with no claude login.
- Test sentinels: the login sentinel must look like an OAuth token to be forwarded, so it is `sk-ant-oat01-wtSENTINEL`; the wt token sentinel is `wt-SENTINEL-tok`. Task 9's token-absence counts exclude lines holding `SENTINEL` or `FAKE`.
- Import ratchet (`internal/depcheck`): the gateway change stays in `modelgateway`/`modelserver`. Session libraries (`catalog`, `agents`, `profiles`, `smoke`, `tui`) read only `modelapi` fields plus the provider id constant through `config`. No allow-list entry is added. The new `internal/claudelogin` imports only the standard library.
- Implementers never read, `cat`, `plutil` or grep the LiteLLM LaunchAgent plist (`~/Library/LaunchAgents/local.litellm.proxy.plist`) or the wt-model one. A subagent once printed a secret from it. `litellm.ProxyEnv.IsSet` is the only way code asks about the proxy environment.
- Implementers never commit a built binary (a docs commit once swept in `wt/bin/…`). Stage paths by name, never `git add -A`/`.`, and check `git status --short` before each commit.
- Long Go runs (`./internal/...`, about 10 min) go through `run_in_background` with the exit read from the log; the per-task runs below are scoped to stay under the foreground limit.
- Commits end with the attribution line the session's system reminder gives. The PR body says `Refs #597`, never a closing keyword. Run `make pre-pr` with `run_in_background` (about 12.5 min) and read the exit from its log.

## Review Focus

The risks the tests would otherwise miss; each task names its pinning tests.

1. **A Claude login leaves the client or the gateway when it should not**: an off gateway, a plain-http remote peer, a spoofed forwarding header, a foreign `anthropic/` row in the proxy, a server the session machine never opted into, or a non-OAuth `Authorization` (the wt token itself) forwarded upstream. Tasks 1, 3 and 6.
2. **The token-swap mode changes for clients without the header**, breaking every pi, codex and non-subscription claude session. Tasks 1 and 5.
3. **A secret is logged, printed or written**, or the wt token reaches a third party through an inherited `ANTHROPIC_CUSTOM_HEADERS` on a native or nested launch. Tasks 1, 2, 5 and 6.
4. **A non-claude agent, or a paid API key, gets a subscription route.** Tasks 3 and 4.
5. **A launch that cannot work starts anyway and fails opaquely** (no login, an off or older server, an old Claude Code, a launch profile overriding the header env), on the CLI and TUI paths alike. Tasks 6 and 7.

## Plan decisions

Each decision is binding for this plan. "If wrong" is what reversing it later costs.

1. **Host switch: a `[gateway]` table in the wt-model home `config.toml`, read once at `serve` start.** The key is `subscription_passthrough` (bool, absent = false). It is not a `serve` flag, because the plist renderer drops flags: `wt-model setup` re-renders the plist (`modelsetup.RenderAgentPlist`, fixed `ProgramArguments`), and `ParseAgentPlist` reads only `--listen`/`--token-file`. It is not agent-wt's `[model-server]` either, which is the client's side. The home config survives setup, is already read by serve (`loadServeConfig`), and is decoded non-strictly, so an older binary tolerates the table.

   `proxyHomeDoc` gains `Gateway map[string]any` (`toml:"gateway,omitempty"`), so this binary's `SetProxy` round-trips whatever the operator wrote and a bad value never breaks `[litellm]` decoding. `GatewaySettings` interprets it: `subscription_passthrough = true` is on; absent or `false` is off; any other type (`"true"`, `1`) is off with one `wt-model: warning:` line naming the key and the expected type (off is the safe direction). A TOML syntax error in the file still fails start, as it does today for `[litellm]`.

   A `[gateway]` table in the **wrong** file (`~/.config/agent-wt/config.toml`) already triggers `config.dropLegacyGateway`'s legacy notice. When that table holds `subscription_passthrough`, the notice instead says the key belongs in `~/.config/wt-model/config.toml`.

   *Cost:* an **older** `wt-model litellm set` re-encodes the document and drops `[gateway]`, turning the mode off at the next serve restart (safe direction; documented). `SetProxy` re-encodes and drops comments, so the fixture carries no inline comment. There is no command to flip it. *If wrong:* a `wt-model gateway subscription on|off` writer, or a setup-tab toggle, can be added later over the same key.
2. **How a client learns the mode:** `modelapi.Ping.SubscriptionPassthrough bool` (`json:"subscription_passthrough,omitempty"`), set from `Options.SubscriptionPassthrough`. With `omitempty`, an older server and a server with the mode off look the same, so the client prints one message for both: `the model server at <addr> does not accept a Claude login: its subscription passthrough is off, or it is older than this wt (on the host: make install, set [gateway] subscription_passthrough = true in ~/.config/wt-model/config.toml, restart wt-model serve)`.

   The session reads the ping from `ms.client.Ping(ctx)`: `modelclient.Client.Ping` caches the first answer (`client.go:166`), so this costs no extra request and works on the TUI path, where `openModels` never runs and the picker's first request does the ping. No `modelSession` field is added.

   An off or older server already marks a subscription row `block` (Task 4's `wireRow`, or `Unmapped` on an older server), so catalog resolve refuses first. When the refused row's `ProviderID` is the subscription provider and the cached ping lacks the field, the session prints decision 2's message instead of the block reason; the guard's ping check is the backstop.

   Serve prints `wt-model: subscription passthrough is on (x-wt-model-token; refused from non-loopback peers over plain http; owner's own login only)` after the announce line: informational, so a bare line (style guide M4), not `note:`. When the listen address is not loopback it adds a `wt-model: warning:` line saying remote subscription requests need https or a tunnel arriving from loopback, while other remote traffic is unaffected. Both lines come from one pure function `subscriptionLines(listenAddr string, on bool) []string`. *If wrong:* a distinct "older" signal needs a second field.
3. **Guard: loopback peer, or `r.TLS != nil`, evaluated after the token check; forwarding headers only refuse.** "https accepted" concretely means a TLS terminator or tunnel on the host itself (`ssh -L`, `tailscale serve`, Caddy or cloudflared on the server machine), which reaches wt-model from 127.0.0.1. A terminator on another machine arrives as a non-loopback peer and is refused. No `--tls-cert` is added (follow-up).

   A same-host plain-http forwarder (Caddy/nginx without TLS, socat, Docker/OrbStack `-p`) makes a LAN client arrive from loopback. When `Forwarded`, `X-Forwarded-For` or `X-Real-IP` is present and names a non-loopback client, the header branch refuses with `subscription_insecure` unless `Forwarded: proto=https` or `X-Forwarded-Proto: https` is also present. Reading the headers only to refuse cannot be spoofed open. A same-host plain-http forwarder that adds none of them is undetectable (documented; the operator who runs a tunnel vouches for its encryption).

   A client-side twin refuses before sending. It checks `ms.ep`, the endpoint actually used (`WT_MODEL_URL` included). A header-mode launch whose url is `http://` to a host that is not loopback exits 1, since by the time the server refuses, the login has crossed the wire. The host is parsed with `netip.ParseAddr` + `Unmap` and must be in `127.0.0.0/8` or `::1`; the name `localhost` (trailing dot allowed) matches exactly; anything else, `0.0.0.0` and `127.1` included, is not loopback. There is no operator escape for plain-http remote. *If wrong:* native TLS is an additive serve option plus `r.TLS` (already accepted).
4. **Header handling and check order.**

   Check order in `authenticated`:

   | Step | Condition | Response |
   |---|---|---|
   | 1 | Header present: exactly one value, non-empty, equal to the token (constant time); `x-api-key` is **not** examined. Header absent: today's `validToken`. | else 401, existing body |
   | 2 | Header branch and mode off | 403 `subscription_off` |
   | 3 | Header branch and peer neither loopback nor TLS, or a forwarding header names a non-loopback client without https (decision 3) | 403 `subscription_insecure` |
   | 4 | Header branch: `Authorization` equal to the wt token (constant time) | 401, existing body |
   | 5 | Header branch: `Authorization` present and not `Bearer sk-ant-oat…` | 403 `subscription_no_login` |
   | 6 | `allowlisted` | else 404, existing |
   | 7 | proxy | |

   Effects on the upstream headers:

   | Upstream header | Header branch | Legacy branch (unchanged) |
   |---|---|---|
   | `x-wt-model-token` | removed | (absent) |
   | `Authorization` | forwarded as sent when it is `Bearer sk-ant-oat…`; absent stays absent | replaced by `Bearer <host key>` |
   | `x-api-key` | removed | removed |
   | `x-litellm-api-key` | removed, then set to the raw host key | passed through as today |
   | `anthropic-beta`, `anthropic-version`, `content-type`, other | forwarded unchanged; nothing added | forwarded unchanged |
   | hop-by-hop, `X-Forwarded-*`, `Forwarded` | removed by `ReverseProxy` before `Rewrite` (unchanged) | same |

   A header present with an empty value, or with two values, is a 401. A valid wt token in `Authorization` beside a wrong header is a 401, with no fallback. The branch decision travels to `Rewrite` in the request context under an unexported key type set by `authenticated`. `Rewrite` never re-reads the header. The errors are `modelapi.TypePermission = "permission_error"`, `CodeSubscriptionOff = "subscription_off"`, `CodeSubscriptionInsecure = "subscription_insecure"` and `CodeSubscriptionNoLogin = "subscription_no_login"`, with their `Msg*` constants; the six gateway errors are documented. *If wrong:* the table is one function and two tests.
5. **Provider and registry representation.**

   *Provider row:* id `claude-subscription` (`provider.IDClaudeSubscription`). `Seed{Name: "Claude subscription", Location: "cloud", Protocols: ["anthropic"], AuthType: "client-login"}` with no `base_url` key written (`SeedRow` omits an empty `BaseURL`). `Policy{Prefix: "anthropic/", Cloud: true}`: no `SecretRef`, no `APIKey`, no `V1Base`. The Models tab's add form (`modeleditor.formProviders`) leaves it out, because the form has no context-window field; it is added only with `wt-model model add`.

   *Model rows:* the registry id is the bare real id (`claude-opus-5-5`; `model_name` the same), per spec decision 4. `modeladmin.checkID` and `DeriveID` accept and derive a slash-less id **only** for this provider. Family stays required like any model (`--family <f>`). Every subscription model needs a `context_window` that the owner supplies: `wt-model model add` refuses one without `--context-window`, because without it `claudeWindow` withdraws the declaration for the whole profile. Cost is left empty, so LiteLLM books $0 while Claude Code prints list price (documented). No `source` key is written (`planAdd` sets it only for providers cloud-sync covers), and cloud-sync never touches the provider.

   *LiteLLM rows:* these are cloud rows, so they live in `config.yaml` by phase 1's rule. Adding or removing a subscription model is a `config.yaml` change and costs the usual serialized proxy restart. An alias over one is a database row (phase 2) and is hot. `prepareModel` refuses a `claude-subscription` model whose provider row has `auth.base_url` or `auth.secret_ref`, or whose `auth.type` is `native`.

   A sync leaves every subscription row **unrouted**, with one warning saying why, while any of these holds:
   - `litellm.ProxyEnv.IsSet("ANTHROPIC_API_KEY")`: a keyless row would serve any caller without a login (any holder of the shared token, by model name) on the host's API key;
   - `config.yaml` (which wt owns) turns on client-header forwarding (`forward_client_headers_to_llm_api`) in `general_settings` or `litellm_settings`: the host master key could be forwarded to Anthropic;
   - `/model/info` lists any deployment, wt-managed or not, whose provider is `anthropic` and whose `api_base` is set and is not `https://api.anthropic.com`, or an `anthropic/*` wildcard: LiteLLM forwards a client OAuth token to every `anthropic/` deployment, and Claude Code can name any `model_name`.

   `store_prompts_in_spend_logs` on is one warning only (prompts, not headers). The check is `litellm.SubscriptionHazards(ctx, …) []string`, shared with serve (decision 1's start path): serve with the mode on and a hazard found keeps the mode **off**, prints one `wt-model: warning:` line naming the hazard (never a value), and the ping reports off. A proxy that does not answer at serve start is a warning and the mode stays on (the sync check still gates routing).

   *If wrong:* moving cloud rows to the database is a phase-1-shaped follow-up. A prefixed registry id would need `model_name` ≠ id in `BuildEntry`.
6. **Catalog offer.** The host can't see a login, so the catalog only marks the rows and enforces the mode:
   - With the mode off, `wireRow` turns a subscription row into `block` with `subscription passthrough is off on the model server ([gateway] subscription_passthrough in ~/.config/wt-model/config.toml)`. `aliasAction` then degrades a subscription-first group to its other targets as phase 2 already does for blocked targets.
   - No new `Row` field: the session identifies a subscription row by `ProviderID == config.SubscriptionProvider` (or an alias target's `ProviderID`), as `sushi` is bound to `config.SushiAgents`.
   - `providerEligible` starts with `if config.IsSubscriptionProvider(id) { return slices.Contains(config.SubscriptionAgents, a.Name) }`, before every other clause, so neither `SupportedProviders` nor `AllProviders` admits it for another agent. `config.SubscriptionAgents = []string{"claude"}`. An alias with any subscription target is therefore claude-only, because `EligibleWire` already needs every target to pass.
   - `NonNativeModels` and `RegisteredOrdered` drop such rows.
   - The **client** decides login presence (Task 6).

   *Deviation from the spec:* the spec offers subscription rows "only when a login is present, and marked in the catalog". This plan adds no marker in the picker or `wt model list --server`, and checks the login at launch, not at offer time: claude's picker lists the rows on a machine without a login, and the launch refuses with decision 8's message. Task 8 records this in the spec. *Cost:* pi and codex cannot use a group whose order 1 is the subscription; a twin alias without the subscription member serves them. *If wrong:* a marker is one column derived from `catalog.UsesSubscription`; relaxing the alias rule is one predicate.
7. **Claude launch mode.** Header mode is per session: it applies when any filled slot, any alias target of a slot, or the single `-M` model uses the subscription. A mixed profile such as fable/opus on the subscription with sonnet/haiku on aliases runs wholly in header mode. Claude Code then sends the login on every request; LiteLLM forwards it only to `anthropic/` deployments (spike 4, live check step 4), which decision 5 keeps to `api.anthropic.com`.

   Env in header mode:
   - `ANTHROPIC_BASE_URL=<gateway>`
   - `ANTHROPIC_API_KEY=` (empty; verified to keep the login)
   - `ANTHROPIC_CUSTOM_HEADERS=<merged>`
   - `ClearEnv` adds `ANTHROPIC_AUTH_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN`, so the credential used is the stored login that was checked.

   The merged value keeps the inherited `ANTHROPIC_CUSTOM_HEADERS` lines minus any `x-wt-model-token` or `authorization` line (case-insensitive), then appends `x-wt-model-token: <token>`. Merging happens where `cmd.Env` is built (`slotLaunchCmd`, `command`), because the builders are pure.

   **Every other launch, for every agent, strips only `x-wt-model-token` lines** from an inherited `ANTHROPIC_CUSTOM_HEADERS` (token mode, the native branch of `Build` for `claude/native` and `claude/<model>`, pi, codex, every slot launch). When nothing is left, the variable is added to `ClearEnv`. This closes the nested case: `wt -M claude/opus` started from a header-mode session's shell would otherwise send the wt token to api.anthropic.com. `authorization` lines and other headers (`x-litellm-tags`) are left alone outside header mode, so a session with no inherited wt line keeps today's env exactly.

   `ANTHROPIC_CUSTOM_HEADERS` is **not** added to `ReservedEnvKeys`. Slot-profile validation instead refuses a profile `env`/`config_content.env` value for it that holds an `x-wt-model-token` or `authorization` line. A resolved **launch** profile (applied after the build, `profiles.ApplyEnvAndArgs`, last entry wins) whose env names `ANTHROPIC_CUSTOM_HEADERS`, `ANTHROPIC_AUTH_TOKEN` or `CLAUDE_CODE_OAUTH_TOKEN` is refused in header mode, exit 1: it would replace the merged header (an opaque 401) or displace the checked login. *If wrong:* the merge is one pure helper; switching to replace is one line.
8. **No login: refuse, never warn or fall back.** The login check runs `claude auth status --json` (and `claude --version`) through `exec.LookPath("claude")`. The environment is scrubbed of `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_OAUTH_TOKEN` and `CLAUDE_CODE_USE_*`, because with those set the command reports `oauth_token`, a false positive. A 10 s timeout applies. Only `loggedIn`, `authMethod`, `apiProvider` and `loginExpiresAt` are decoded, so the email and org fields are never kept, printed or logged. A login is accepted only when `loggedIn && authMethod == "claude.ai" && apiProvider == "firstParty"`. Exit 1, a parse failure or a timeout means no login.

   In a test binary (`testing.Testing()`), a nil `hooks.claudeLogin` returns `claude login check not stubbed in a test` instead of running the real `claude`, as `hooks.newClient` already refuses outside loopback (`app.go:106`).

   The refusal is `wt: <model> uses your Claude subscription, but no claude.ai login was found: run 'claude auth login' (nothing was started or registered)`, exit 1, before the client session and before any start job. A version below 2.1.227 refuses the same way, naming the minimum version. A login expiring within 24 h is one `wt: note:` line.

   Falling back to token mode would send subscription ids with the wt token as `Authorization`, so the keyless row answers 401. In a group, it would silently land every turn on the fallback. Either way the cause would be hidden. *If wrong:* a warn-and-continue mode is one branch.
9. **Smoke never spends the subscription by default.** `smoke.walkWire` drops subscription rows (and aliases with a subscription target) from candidates and eligibility, and returns them: `walkWire(...) (cands []Candidate, droppedSubscription []string)`; `CandidatesWire` and `EligibilityWire` pass the list on. In a walk without an id, each dropped model is one ordinary smoke row `{agent: "claude", model: <id>, status: "SKIP", error: "uses your Claude subscription: pass --subscription to smoke it"}`, printed through the existing per-row SKIP line and the existing `--json` row shape (no new key). A subscription id named without `--subscription` is exit 1 with that hint. Only `wt smoke <id> --subscription` runs one, and only for `claude`, after Task 6's guard. `--json` and non-TTY runs follow the same rule. Automated tests never exec a real `claude`.
10. **Old binaries.**
    - *New session, old server:* the old catalog has no `claude-subscription` Policy, so its rows are `block` (`Unmapped`); decision 2 maps that to the upgrade message before any login is sent.
    - *Old session, new server with the mode on:* the old wt shows subscription rows to every AllProviders agent and launches claude in token mode. The keyless row answers 401 `Missing Anthropic API Key` (decision 5 keeps the rows unrouted while the proxy has `ANTHROPIC_API_KEY`). The docs say to upgrade every session machine before turning the mode on.
    - *Old `wt-model` binaries:* an old `litellm set` drops `[gateway]` (decision 1). An old `serve` or sync meeting a `claude-subscription` model has no Policy for it and treats it as unmapped: its `config.yaml` rewrite drops the row and restarts the live proxy, and the next new sync adds it back with another restart. **Every wt-model binary that can sync must be upgraded before the first subscription model is added** (docs; Task 9 checks `pgrep -fl wt-model` first).
    - *Install order:* the docs say `make install` and a serve restart before adding the provider.
11. **Window rule unchanged.** Subscription rows carry their registry `context_window` like any model, so `claudeSlotWindow`'s minimum rule applies to mixed profiles. It is unverified whether `CLAUDE_CODE_MAX_CONTEXT_TOKENS` caps a *recognized* Claude id. If it does, a profile with a 32K local haiku caps an opus session at 32K. That is the same trade phase 2 shipped (under-declaring compacts early, never overflows), and the profile's own env is the escape hatch. Task 9 step 7 measures it with `/context`, and the result decides a follow-up. *If wrong:* skipping the declaration when the default slot is a subscription model is one condition in `claudeSlotWindow`.
12. **Session-side opt-in.** Whether a launch sends the user's claude.ai login is otherwise decided by data the server controls (a row's `ProviderID`, alias targets, a phase-2 hot re-point). A remote operator could re-point an alias the user's profile already names to a subscription target. So the session machine must opt in with `[model-server] subscription = true` in `~/.config/agent-wt/config.toml`, hand-edited like the host's key; the Mode tab's writer round-trips it and never sets it. Without it, `subscriptionGuard` refuses header mode: `wt: <model> uses your Claude subscription, but this machine has not opted in to sending its Claude login to a model server: set [model-server] subscription = true in ~/.config/agent-wt/config.toml`. When the endpoint is not loopback, the guard prints one `wt: note:` line naming the host the login will be sent to. *If wrong:* the key can move to a command later.

## Not in this plan

- Native TLS on `wt-model serve` (`--tls-cert/--tls-key`), and accepting a non-loopback TLS terminator: follow-up.
- Stripping a client `x-litellm-api-key` (or `x-wt-model-token`) in **legacy** mode. It is an existing hole: an authenticated client can pick another LiteLLM key. The owner's byte-for-byte rule keeps it out of this phase. Follow-up issue.
- A command or setup-tab toggle for `[gateway] subscription_passthrough` or `[model-server] subscription`: follow-up (decisions 1, 12).
- Cloud model rows as LiteLLM database rows (no restart when a subscription model is added): follow-up.
- Per-client tokens (the one shared token still does not scope clients; existing known limit).
- Detecting a `~/.claude/settings.json` env block that sets `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_CUSTOM_HEADERS`. Such a block outranks the process env; the gateway refuses what it sends (401 when it is the wt token, 403 `subscription_no_login` for another non-OAuth value), and the docs describe it.
- A picker or list marker for subscription rows (decision 6).
- A real provider switch inside one conversation (thinking signatures, prompt cache), and LiteLLM `allowed_fails`/`cooldown_time` tuning (spec "Not verified").
- modeltiers host basis for `claude-subscription` (tiers resolve by name normalization once re-run; windows come from the registry).
- pi/codex use of subscription-first groups (decision 6).

---

### Task 1: Gateway header mode and guards

**Files:**
- Modify: `wt/internal/modelgateway/gateway.go`, `wt/internal/modelapi/modelapi.go` (`TypePermission`, `CodeSubscriptionOff`, `CodeSubscriptionInsecure`, `CodeSubscriptionNoLogin`, their `Msg*`, `ErrorCodes`), `docs/contracts/model-api.sample.json` (members `gateway_error_subscription_off`, `gateway_error_subscription_insecure`, `gateway_error_subscription_no_login`), `wt/docs/wt-model-server.md` (the three gateway error rows)
- Create: `wt/internal/modelgateway/gateway_subscription_test.go`
- Test: `wt/internal/modelapi/modelapi_test.go` (`members` map, codes table, `TestErrorSamplesUseConstants`' struct and comparisons)

**Interfaces:**
- Consumes: `modelapi.GatewayErrorObject`, `writeError`, `InferencePaths`.
- Produces:
  - `const TokenHeader = "x-wt-model-token"`
  - `Config.SubscriptionPassthrough bool`; `Config.ErrorLog *log.Logger` (assigned to the `ReverseProxy`; nil = today)
  - `func peerTrusted(r *http.Request) bool`: `r.TLS != nil`, or `RemoteAddr` parses (`net.SplitHostPort`, `netip.ParseAddr`, `Unmap`) to a loopback address and no forwarding header names a non-loopback client without https. Unparseable or empty means false.
  - `type authKind int` with an unexported context key; `authenticated(c Config, next)` replaces `authenticated(token, next)`.

- [ ] **Step 1: Write failing tests** (sentinels per Global Constraints; the upstream is an `httptest` server recording headers with an atomic call counter; non-loopback cases call `New(cfg).ServeHTTP` directly with `req.RemoteAddr` set):
  - `TestSubscriptionHeaderForwardsTheLogin`: mode on, peer 127.0.0.1. The upstream sees `Authorization: Bearer <login>` byte-equal, `anthropic-beta` with `oauth-2025-04-20` untouched, `x-litellm-api-key == testKey`, and no `x-wt-model-token` and no `x-api-key`. It covers `/v1/messages`, `/v1/messages/count_tokens` and `/v1/models`.
  - `TestSubscriptionHeaderWithoutAuthorizationAddsNone`: the upstream has no `Authorization`.
  - `TestSubscriptionNonOAuthAuthorizationRefused`: `Authorization` = the wt token gives 401; an `sk-ant-api…` key and a random string give 403 `subscription_no_login`; each with 0 upstream calls.
  - `TestSubscriptionWrongOrDuplicateHeaderForwardsNothing`: wrong, empty and two-valued header, each with and without a valid wt Bearer beside it, give 401 with the existing body and 0 upstream calls.
  - `TestSubscriptionModeOffForwardsNothing`: valid header plus login, mode off, gives 403 `subscription_off` and 0 calls. A wrong header with the mode off is a 401 (no mode disclosure).
  - `TestSubscriptionRefusedFromNonLoopbackOverHTTP`: `192.0.2.7:1`, `10.0.0.2:1`, `[2001:db8::1]:1`, `garbage` and an empty addr give 403 `subscription_insecure` and 0 calls. Rows from `192.0.2.7` that send `X-Forwarded-For: 127.0.0.1`, `X-Forwarded-Proto: https` or `Forwarded: proto=https;for=127.0.0.1` are still refused. A loopback peer with `X-Forwarded-For: 192.0.2.7` is refused; the same plus `X-Forwarded-Proto: https` is accepted.
  - `TestSubscriptionAcceptedFromLoopbackOrTLS`: `127.0.0.1`, `127.9.9.9`, `[::1]`, `[::ffff:127.0.0.1]` and `192.0.2.7` with `req.TLS = &tls.ConnectionState{}` give 200.
  - `TestLegacyRequestFromNonLoopbackUnaffected`: no header, mode on and off, peer `192.0.2.7`, gives 200 with the swap.
  - `TestLegacyUpstreamHeadersUnchanged`: one header set (`Authorization` Bearer token, `x-api-key`, `anthropic-beta`, `anthropic-version`, `content-type`, `x-custom`, `x-litellm-api-key: client`, `X-Forwarded-For: 192.0.2.7`) to `Config{}` and to `Config{SubscriptionPassthrough: true}` gives the identical upstream header map, equal to today's.
  - `TestSubscriptionClientLiteLLMKeyReplaced`: header branch with a client `x-litellm-api-key: evil`; the upstream sees only `testKey`.
  - `TestSubscriptionPathCheckStillApplies`: a valid header on `/v1/files` gives 404.
  - `TestSubscriptionSecretsNeverLogged`: `ErrorLog = log.New(&buf, "", 0)`. Drives a success, a 401, the three 403s, an upstream-down 503, an upstream that aborts mid-body and a client that cancels mid-stream. `buf`, every response body and every response header contain neither sentinel.
  - `TestSubscriptionStreams`: SSE flush in header mode (clone of `TestGatewayStreamsWithoutBuffering`).
  - In `modelapi`: `TestFixtureMatchesTypes`, `TestEveryProblemCodeIsDocumented` and `TestErrorSamplesUseConstants` with the three new members and codes.
- [ ] **Step 2: Run** `cd wt && go test ./internal/modelgateway ./internal/modelapi -run 'Subscription|Legacy|Fixture|ProblemCode|ErrorSamples' -v`. Expected: FAIL (undefined `TokenHeader`, `Config.SubscriptionPassthrough`, `CodeSubscriptionOff`).
- [ ] **Step 3: Implement** decisions 3 and 4, plus the codes, fixture members and server-page rows in the same commit. No `log` call anywhere in the package.
- [ ] **Step 4: Run** the same command, then `go test -race ./internal/modelgateway ./internal/modelapi`, and confirm the five legacy tests are untouched with `git diff --stat wt/internal/modelgateway/gateway_test.go` (expect no change). Expected: PASS.
- [ ] **Step 5: Commit** `feat(modelgateway): x-wt-model-token header mode, off by default, loopback/TLS guard (#597)`.

### Task 2: The host switch, the ping field and serve's report

**Files:**
- Modify: `wt/internal/config/proxyhome.go` (`proxyHomeDoc.Gateway`, `proxyHomeHeader` wording mentions the hand-edited `[gateway]` table, `func GatewaySettings() (GatewayConf, []string, error)`), `wt/internal/config/migrate.go` (`dropLegacyGateway` points a `subscription_passthrough` key at the right file), `wt/internal/modelserver/server.go` (`Options.SubscriptionPassthrough`, `Options.ErrorLog`, the gateway `Config`, `ping`), `wt/internal/modelapi/modelapi.go` (`Ping.SubscriptionPassthrough`), `wt/cmd/wt-model/serve.go` (read it after `loadServeConfig`, `subscriptionLines`, `ErrorLog: log.New(cmd.ErrOrStderr(), "wt-model: ", 0)`, `Long` help), `docs/contracts/model-api.sample.json` (`ping` carries `"subscription_passthrough": true`), `docs/contracts/wt-model-config.sample.toml` (`[gateway]\nsubscription_passthrough = false` appended after `[litellm]`, no inline comment), `wt/docs/wt-model-server.md` (the ping row)
- Test: `wt/internal/config/proxyhome_test.go` (`TestProxyHomeFixtureMatchesTheTypes` asserts the `[gateway]` table decodes to off), `wt/internal/config/migrate_test.go`, `wt/internal/modelserver/server_test.go`, `wt/cmd/wt-model/serve_test.go`, `wt/internal/modelping` ping tests, `wt/internal/modelsetup`

**Interfaces:**
- Consumes: Task 1's `Config.SubscriptionPassthrough`, `Config.ErrorLog`.
- Produces:
  - `type GatewayConf struct { SubscriptionPassthrough bool }`. `func config.GatewaySettings() (GatewayConf, []string, error)` reads `ProxyHomePath()` through `decodeProxyHome`: no file means the zero value; a wrong-typed key is off plus one warning string; a TOML syntax error is an error.
  - `modelserver.Options.SubscriptionPassthrough bool`, `modelserver.Options.ErrorLog *log.Logger` (passed to `modelgateway.Config.ErrorLog`)
  - `modelapi.Ping.SubscriptionPassthrough bool` (`json:"subscription_passthrough,omitempty"`)
  - `func subscriptionLines(listenAddr string, on bool) []string` (in `cmd/wt-model`)

- [ ] **Step 1: Write failing tests**:
  - `TestGatewayTableRoundTripsThroughSetProxy`: `SetProxy` keeps `[gateway] subscription_passthrough = true`, and keeps a wrong-typed value as written.
  - `TestGatewaySettingsAbsentIsOff`.
  - `TestGatewaySettingsWrongTypeIsOffWithWarning`: `"true"` and `1` give off and one warning naming the key; `[litellm]` still decodes.
  - `TestWrongFileGatewayKeyPointsAtTheHome`: a `[gateway] subscription_passthrough` in agent-wt's `config.toml` gives the notice naming `~/.config/wt-model/config.toml`, not the legacy wording.
  - `TestPingAdvertisesSubscriptionPassthrough`: true only with the option; the raw JSON has no key when it is off.
  - `TestOldClientDecodesNewPing`: `modelping.CheckPing` on the new body succeeds.
  - `TestGatewayHeaderModeThroughServer`: full `Handler`; control calls keep Bearer in both modes, and control refuses `x-wt-model-token` alone with 401.
  - `TestSubscriptionLines`: a table over `0.0.0.0:8787`, `[::]:8787`, `192.0.2.7:8787` (info + warning) and `127.0.0.1:0` (info only), and off (none).
  - `TestServeSubscriptionOffByDefault`: no subscription line.
  - `TestServeSubscriptionLineAfterAnnounce`: loopback listen; the announce line is byte-identical and first; the subscription line follows; with a legacy-fallback pair also seeded, `TestServeNotesTheLegacyFallback`'s one-note rule still holds.
  - `TestServeNeverPrintsForwardedSecrets`: the upstream is an `httptest.NewServer` whose URL goes to `seedProxyHome(t, up.URL, …)`, and the test asserts the upstream saw the request. Sends both sentinels through the running serve's gateway in header mode: a success, a 403, and an upstream that aborts mid-body (so a proxy log line is written to serve's stderr and scanned). `readStderr`/`readStdout` lack the sentinels, and so does the scratch home's `serve.json`.
  - `TestSetupNeverEnablesPassthrough` (`internal/modelsetup`): a setup plan applied on a scratch home leaves `[gateway]` absent or unchanged.
- [ ] **Step 2: Run** `cd wt && go test ./internal/config ./internal/modelserver ./internal/modelping ./internal/modelsetup ./cmd/wt-model -run 'Gateway|Ping|Subscription|SetupNever|WrongFile|ProxyHomeFixture' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** decision 1 and decision 2's serve half. Serve reads the setting once, after `loadServeConfig` and before listening; a syntax error fails start, worded without values (`secretSafe`). (Decision 5's hazard check at serve start lands in Task 3.)
- [ ] **Step 4: Run** `cd wt && go test ./internal/config ./internal/modelserver ./internal/modelapi ./internal/modelping ./internal/modelsetup ./cmd/wt-model`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt-model): [gateway] subscription_passthrough host switch, advertised by ping (#597)`.

### Task 3: The `claude-subscription` provider, its registry rows and keyless LiteLLM rows

**Files:**
- Modify: `wt/internal/provider/table.go` (the `Spec`, `IDClaudeSubscription`), `wt/internal/provider/provider.go` (`SeedRow` omits an empty `base_url`), `wt/internal/config/config.go` (`SubscriptionProvider`, `SubscriptionAgents`, `func IsSubscriptionProvider(id string) bool`), `wt/internal/modeladmin/apply.go` (`checkID`), `wt/internal/modeladmin/fields.go` (`DeriveID`; the `--context-window` requirement), `wt/internal/modeleditor/models_form.go` (`formProviders` leaves the provider out), `wt/internal/litellm/service.go` (`prepareModel` refusals; the unrouted-with-warning rule), `wt/internal/litellm/subscription_hazards.go` (new), `wt/cmd/wt-model/serve.go` (the hazard check when the mode is on), `docs/contracts/registry.sample.toml` (the provider row and one model, `claude-haiku-5-5`, with an illustrative `context_window`)
- Test: `wt/internal/provider/provider_test.go` (`wantPolicies`, `SeedableCloudIDs` becomes `["claude-subscription", "openrouter"]` in the table's order), `wt/internal/litellm/policy_table_test.go`, `wt/internal/litellm/entry_test.go`, `wt/internal/modeladmin` id tests, `wt/internal/modeleditor` form test, `wt/cmd/wt-model/model_write_test.go`, `wt/cmd/wt-model/serve_test.go`, `wt/internal/config/registry_fixture_test.go`, `llmbench/tests/test_registry.py` (only if it reads `registry.sample.toml`'s lists)

**Interfaces:**
- Consumes: `provider.Policy`, `BuildEntry`, `prepareModel`, `litellm.ProxyEnv`.
- Produces: the decision 5 representation. `wt-model model add claude-subscription claude-opus-5-5 --family <f> --context-window N` writes the id `claude-opus-5-5`, and without `--context-window` it refuses: `a claude-subscription model needs --context-window (Claude Code's window for it)`. `func litellm.SubscriptionHazards(ctx context.Context, cfgPath string, env ProxyEnv, info ModelInfoFunc) (hazards, warnings []string)`, worded without values; `type ModelInfoFunc func(ctx context.Context) ([]DeploymentInfo, error)` is new (provider, `model_name`, `api_base` per `/model/info` row; the sync passes its existing proxy reader, tests a fake).

- [ ] **Step 1: Write failing tests**:
  - `TestSubscriptionRowIsKeylessAnthropic`: `BuildEntry` gives `model: anthropic/claude-haiku-5-5`, `model_name: claude-haiku-5-5`, and no `api_key`/`api_base`; pricing is explicit zero.
  - `TestOnlySubscriptionProviderBuildsAnthropicRows`: over every provider in the table, only `claude-subscription` has an `anthropic/` prefix, and its Policy has no `SecretRef`/`APIKey`/`V1Base`.
  - `TestSubscriptionProviderWithBaseURLOrSecretRefused`: `prepareModel` errors for `base_url`, `secret_ref` and `auth.type = native` on the provider row; sync leaves the row unrouted with one warning; an alias over it freezes per phase 2 decision 3.
  - `TestSubscriptionRowsUnroutedWhileProxyHasAnthropicKey`: injected `ProxyEnv`; the row is unrouted, the warning names the variable, never a value.
  - `TestSubscriptionHazards`: a hand-written (no `wt_managed`) `anthropic/` row with a foreign `api_base`, an `anthropic/*` wildcard, and `forward_client_headers_to_llm_api: true` each give a hazard and an unrouted row; an `anthropic/` row with `api_base: https://api.anthropic.com` gives none; `store_prompts_in_spend_logs: true` gives a warning only.
  - `TestServeKeepsModeOffOnHazard`: mode on, a fake `/model/info` (httptest) with a foreign `anthropic/` row; serve prints one warning, the ping reports off. An unreachable proxy gives a warning and the mode stays on.
  - `TestAddSubscriptionModelUsesTheBareID` (with `--family`).
  - `TestAddSubscriptionModelNeedsContextWindow`.
  - `TestAddSubscriptionModelWritesNoSource`.
  - `TestBareIDRefusedForOtherProviders`.
  - `TestModelsFormOmitsSubscriptionProvider`.
  - `TestSeedRowOmitsEmptyBaseURL`.
  - `TestCloudSyncIgnoresSubscriptionProvider`.
  - Fixture tests (Go, and llmbench if it reads the sample) with the new rows.
- [ ] **Step 2: Run** `cd wt && go test ./internal/provider ./internal/litellm ./internal/modeladmin ./internal/modeleditor ./internal/config ./internal/cloudsync ./cmd/wt-model -run 'Subscription|Anthropic|BareID|SeedRow|Fixture|Polic|ModelsForm|Hazard' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** No change to `BuildEntry` should be needed. If one is, stop and report: the keyless path is supposed to fall out of the Policy.
- [ ] **Step 4: Run** `cd wt && go test ./internal/provider ./internal/litellm ./internal/modeladmin ./internal/modeleditor ./internal/config ./internal/tomlw ./internal/catalog ./internal/cloudsync ./internal/depcheck ./cmd/wt-model` and `cd llmbench && uv run pytest tests/test_registry.py -q`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt-model): claude-subscription provider, keyless anthropic rows (#597)`.

### Task 4: Catalog: blocked while off, claude-only everywhere

**Files:**
- Modify: `wt/internal/modelserver/catalog.go` (`BuildCatalog` takes `CatalogOptions`; `wireRow` blocks), `wt/internal/modelserver/server.go` (the two `BuildCatalog` calls pass `CatalogOptions{SubscriptionOn: s.o.SubscriptionPassthrough}`), `wt/internal/catalog/wire.go` (`providerEligible`, `NonNativeModels`, `RegisteredOrdered`, `func UsesSubscription(r modelapi.Row) bool`), `wt/cmd/wt-session/commands.go` (`rotateModelsRemote` via `RegisteredOrdered`), `wt/internal/candidates/candidates.go` (no change unless a test shows a leak)
- Test: `wt/internal/modelserver/catalog_test.go`, `catalog_alias_test.go`, `catalog_parity_test.go`, `alias_jobs_test.go` (every existing call gains `CatalogOptions{}`), `wt/internal/catalog/wire_test.go`, `wt/cmd/wt-session` rotation tests

**Interfaces:**
- Consumes: Task 3's `config.SubscriptionProvider`/`SubscriptionAgents`/`IsSubscriptionProvider`, Task 2's option.
- Produces: `type CatalogOptions struct { SubscriptionOn bool }`; `func BuildCatalog(cfg *config.Config, snap localmodels.Snapshot, ix tiers.Index, sessions *sessionreg.Registry, routed map[string]bool, now time.Time, opts CatalogOptions) modelapi.Catalog`; `catalog.UsesSubscription(r)` (the row's provider, or any alias target's).

- [ ] **Step 1: Write failing tests**:
  - `TestCatalogSubscriptionRowBlockedWhenOff`: the reason names the key.
  - `TestCatalogSubscriptionRowLaunchableWhenOn`.
  - `TestSubscriptionFirstGroupDegradesWhenOff`: `aliasAction` gives the order-2 target's action.
  - `TestSubscriptionRowsOnlyForClaude`: claude with explicit providers, claude AllProviders, pi AllProviders, pi with `SupportedProviders = ["claude-subscription"]`, codex, opencode, copilot; the row and an alias with one subscription target appear only for claude.
  - `TestNonNativeModelsDropsSubscriptionRows`: alias included.
  - `TestRotationSkipsSubscriptionRows`.
  - `TestUsesSubscription`: a table.
- [ ] **Step 2: Run** `cd wt && go test ./internal/modelserver ./internal/catalog ./cmd/wt-session -run 'Subscription|Rotation' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** decision 6; `providerEligible`'s subscription clause comes first.
- [ ] **Step 4: Run** `cd wt && go test ./internal/modelserver ./internal/catalog ./internal/smoke ./cmd/wt-session ./internal/depcheck`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(catalog): subscription rows blocked while off and offered only to claude (#597)`.

### Task 5: Claude header-mode launch (driver)

**Files:**
- Modify: `wt/internal/config/config.go` (`Model.Subscription bool` with `toml:"-"`, session-side like `GatewayID`; `Route.Subscription bool`), `wt/internal/catalog/wire.go` (`ToModel` sets it from `UsesSubscription`), `wt/internal/agents/gateway.go` (`GatewayRoute` copies it), `wt/internal/agents/claude.go` (`Build`, native branch included), `wt/internal/agents/claude_slots.go` (`BuildSlots`), `wt/internal/agents/slots.go` (`slotLaunchCmd`), `wt/internal/agents/agents.go` (`command`; `LaunchCmd.HeaderToken`), `wt/internal/profiles/slot.go` (`SlotBuildInput.Subscription`, `SlotCommand.HeaderToken`), `wt/internal/profiles/slot_validate.go` (the header-value refusal)
- Create: `wt/internal/agents/custom_headers.go`, `wt/internal/agents/claude_subscription_test.go`
- Test: `wt/internal/agents/claude_slots_test.go` (`wantKeys` unchanged), `wt/internal/profiles/slot_validate_test.go`, `wt/cmd/wt-session/profile_slots_test.go`

**Interfaces:**
- Consumes: Task 4's `UsesSubscription`/`ToModel`.
- Produces:
  - `func mergeCustomHeaders(inherited, token string) string`: header mode (`token != ""`) drops `x-wt-model-token:` and `authorization:` lines (case-insensitive, trimmed) and appends `x-wt-model-token: <token>`.
  - `func stripWtHeader(inherited string) string`: every other launch; drops only `x-wt-model-token:` lines. An empty result from either means the variable goes to `ClearEnv`.
  - `LaunchCmd.HeaderToken string` and `SlotCommand.HeaderToken string`. They are set only in header mode; the env assembler (`command`, `slotLaunchCmd`) turns them into the merged variable, and applies `stripWtHeader` on every other launch for every agent.
  - `SlotBuildInput.Subscription bool`: true when any filled slot model `Subscription`.
  - The token is refused when it holds a character outside printable ASCII or a newline (`ErrBadHeaderToken`).

- [ ] **Step 1: Write failing tests**:
  - `TestClaudeBuildSlotsSubscriptionEnv`: `Env == [ANTHROPIC_BASE_URL=…, ANTHROPIC_API_KEY=]` with `HeaderToken == key`. No entry starts with `ANTHROPIC_AUTH_TOKEN=`. `ClearEnv` holds `ANTHROPIC_AUTH_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN`. The key is a substring of no `Args` element; the `--settings` payload is decoded and also substring-scanned, and has neither `ANTHROPIC_AUTH_TOKEN` nor `ANTHROPIC_CUSTOM_HEADERS`. The slot vars carry `claude-fable-5-2`/`claude-opus-5-5` and `alias/sonnet-main`/`alias/haiku-local` (a second golden).
  - `TestClaudeBuildSlotsTokenModeUnchanged`: the existing golden and command tests run unmodified. A new assertion checks that a non-subscription input yields an empty `HeaderToken`.
  - `TestClaudeSingleModelSubscription`: `-M claude-opus-5-5` uses header mode.
  - `TestMergeCustomHeaders`: a table covering inherited `x-litellm-tags: a`, a stale `X-WT-Model-Token: old`, an `Authorization: x` line, empty input, and `\r\n` line ends; `stripWtHeader` over the same table keeps the `Authorization` line.
  - `TestSlotLaunchCmdMergesInheritedHeaders`: with `t.Setenv`, the exact joined string appears once.
  - `TestTokenModeStripsStaleWtHeader`: the assembled env of a token-mode launch with an inherited `x-litellm-tags: a\nx-wt-model-token: old\nauthorization: x` is exactly `x-litellm-tags: a\nauthorization: x`; with only the wt line inherited, the variable is in `ClearEnv`.
  - `TestNativeLaunchStripsInheritedWtHeader`: `claude/native` and `claude/opus`, with `t.Setenv` of a header-mode value; the child env has no `x-wt-model-token`.
  - `TestEveryAgentStripsInheritedWtHeader`: pi and codex launches, same rule.
  - `TestSlotProfileHeaderValueRefused`: profile `env` and `config_content.env` setting `ANTHROPIC_CUSTOM_HEADERS` with an `x-wt-model-token` or `authorization` line are refused; with `x-litellm-tags: a` alone, accepted.
  - `TestProfileShowMasksTheHeaderToken`: `wt profile show` prints `***` plus the last 4 characters, and the full token appears nowhere.
  - `TestHeaderTokenRejectsControlChars`.
- [ ] **Step 2: Run** `cd wt && go test ./internal/agents ./internal/profiles ./internal/catalog ./cmd/wt-session -run 'Subscription|CustomHeaders|TokenMode|WtHeader|HeaderValue|ProfileShow|HeaderToken' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** decision 7.
- [ ] **Step 4: Run** `cd wt && go test ./internal/agents ./internal/profiles ./internal/catalog ./cmd/wt-session`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(agents): claude header mode keeps the login, wt token in ANTHROPIC_CUSTOM_HEADERS (#597)`.

### Task 6: Pre-launch guards in every launch path

**Files:**
- Create: `wt/internal/claudelogin/claudelogin.go`, `wt/internal/claudelogin/claudelogin_test.go`, `wt/cmd/wt-session/subscription_guard.go`, `wt/cmd/wt-session/subscription_guard_test.go`
- Modify: `wt/internal/config/modelserver.go` (`ModelServerState.Subscription bool`, `toml:"subscription,omitempty"`), `wt/internal/modelmode` (the Mode tab's writer round-trips the key), `wt/cmd/wt-session/app.go` (`hooks.claudeLogin`), `wt/cmd/wt-session/main.go` (builds the TUI guard closure and passes it to `tui.Run`), `wt/cmd/wt-session/slot_launch.go` (`launchSlotProfileRemote`; the slot applier's launch-profile check), `wt/cmd/wt-session/launch.go` (`applyResolvedProfile`'s launch-profile check), `wt/cmd/wt-session/launch_remote.go`, `wt/internal/tui/app.go` (`Run` gains `guard SubscriptionGuard`), `wt/internal/tui/slot_flow.go` (`beginRemoteSlotLaunch`), `wt/internal/tui/remote_start.go`, `wt/internal/profiles/testdata/slot_launch_remote_parity.txt` (a `guard` token and one subscription scenario; both parsers emit it), `wt/internal/depcheck/depcheck_test.go` (`internal/claudelogin` in `otherPackages`)
- Test: `wt/cmd/wt-session/launch_remote_test.go` (the fake `claude` script answers `auth status --json` and `--version` from env-named files, and dumps its env and argv to env-named files), `wt/cmd/wt-session/slot_launch_remote_test.go` and `wt/internal/tui/slot_flow_remote_test.go` (both `TestSlotFlowParity`)

**Interfaces:**
- Consumes: Task 2's ping field, Task 4's `UsesSubscription`, Task 5's builders.
- Produces:
  - `type Status struct { LoggedIn bool; Method, Provider string; ExpiresAt time.Time; Version string }`
  - `func claudelogin.Check(ctx context.Context, bin string, environ []string) (Status, error)`: scrubs the env per decision 8 and decodes only the four fields.
  - `func (s Status) OK() bool`
  - `const MinVersion = "2.1.227"`
  - `func subscriptionGuard(ctx context.Context, ms *modelSession, rows []modelapi.Row, profileEnvKeys []string, login func(context.Context) (claudelogin.Status, error)) error`: returns nil when no row `UsesSubscription`. Otherwise it checks, in order: opt-in (decision 12), endpoint `ms.ep` (decision 3), ping via `ms.client.Ping(ctx)` (decision 2), launch-profile env keys (decision 7), version, login (decision 8).
  - `type tui.SubscriptionGuard func(ctx context.Context, rows []modelapi.Row) error`: `main.go` closes over `ms`, the resolved launch profile's env keys and `a.hooks.claudeLogin`. The TUI runs it inside a `tea.Cmd` (it execs `claude` with a 10 s timeout, so never in `Update`), before `runRemoteStart` registers the client session and before the slot flow's register/start. A nil guard (tests that build no subscription rows) is a no-op.
  - The appliers (`applyResolvedProfile`, slot_launch.go's applier; the TUI's `slotApplier` and smoke reach one of them) also refuse a header-mode command whose launch profile names `ANTHROPIC_CUSTOM_HEADERS`, `ANTHROPIC_AUTH_TOKEN` or `CLAUDE_CODE_OAUTH_TOKEN`, as a backstop for paths where the profile resolves after the guard.

- [ ] **Step 1: Write failing tests**:
  - `TestCheckScrubsCredentialEnv`: a fake binary prints the env it sees; none of the scrubbed names appear.
  - `TestCheckAcceptsOnlyClaudeAILogin`: `oauth_token`, `none`, exit 1, garbage and a timeout all mean not OK.
  - `TestCheckNeverReturnsPersonalFields`: email and org values from the fake output are absent from `Status` and from any error text.
  - `TestNilLoginHookRefusesInTests`: a nil `hooks.claudeLogin` under `testing.Testing()` returns the "not stubbed" error and runs nothing.
  - `TestSubscriptionNoLoginRefusesBeforeAnything`: rig. Exit 1, the message names `claude auth login`, the fake server saw no `POST /sessions`, no `/start` and no gateway call, and no BUILD was logged.
  - `TestSubscriptionWithoutOptInRefuses`: no `[model-server] subscription`; refused, nothing registered, the login hook never called. An alias re-pointed to a subscription target does not switch a non-opted-in profile into header mode.
  - `TestSubscriptionRemoteHostNoted`: an opted-in non-loopback https endpoint prints one note naming the host.
  - `TestSubscriptionServerOffOrOlderRefuses`: a ping without the field gives decision 2's message, with nothing registered. A realistic old-server catalog (the row blocked as unmapped) gives the same message, not the unmapped reason.
  - `TestSubscriptionPlainHTTPRemoteRefusedBeforeSending`: refused: `http://192.0.2.7:8787`, `http://[::ffff:192.0.2.7]:1`, `http://localhost.example.com`, `http://127.1`, `http://0.0.0.0`, and `WT_MODEL_URL=http://192.0.2.7:1` with a loopback `[model-server] url`. Pass: `https://host`, `http://127.0.0.1`, `http://[::1]`, `http://localhost`, `http://localhost.`.
  - `TestSubscriptionOldClaudeRefused`: version 2.1.200.
  - `TestSubscriptionLaunchProfileCannotOverrideHeaderEnv`: a matching launch profile setting each of the three keys is refused, exit 1, before any client session.
  - `TestSubscriptionLaunchChildEnvAndArgv`: the dump has the merged `ANTHROPIC_CUSTOM_HEADERS` once, no `ANTHROPIC_AUTH_TOKEN`, and argv free of the token. A nested case (the parent env already holds a header-mode `ANTHROPIC_CUSTOM_HEADERS`) launching `claude/opus` natively has no `x-wt-model-token` in the child.
  - `TestSubscriptionLaunchWritesNoFileAndNoArgv`: walks the scratch worktree and scratch HOME for the token bytes.
  - `TestNonSubscriptionLaunchSkipsTheGuard`: the login hook is never called.
  - `TestTUISubscriptionLaunchAfterPickerPing`: the picker fetched the catalog from a server whose ping has `subscription_passthrough: true` (so `openModels` never ran); the launch is not refused.
  - `TestSlotFlowParity` (both): the shared file gains a `guard` token and a subscription scenario. The cmd/wt-session one asserts the guard runs before `openClientSession`; the internal/tui one that it runs before `runRemoteStart`/register.
  - `TestLoginExpiringSoonNotes`.
- [ ] **Step 2: Run** `cd wt && go test ./internal/claudelogin ./cmd/wt-session ./internal/tui ./internal/depcheck -run 'Check|Subscription|SlotFlowParity|LoginExpiring|NilLoginHook|TUISubscription' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** The guard runs after catalog resolve and before `openClientSession`/any start, in all four paths. The `TestSessionPathsNeverTouchTheRegistryOrLifecycle` boundary stays green.
- [ ] **Step 4: Run** `cd wt && go test ./internal/claudelogin ./internal/tui ./internal/profiles ./internal/config ./internal/modelmode ./internal/depcheck ./cmd/wt-session`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): refuse a subscription launch without opt-in, a login, an enabled server or a safe endpoint (#597)`.

### Task 7: `wt smoke` never spends the subscription by default

**Files:**
- Modify: `wt/internal/smoke/smoke_wire.go` (`walkWire` returns the dropped ids; `CandidatesWire` and `EligibilityWire` pass them on), `wt/cmd/wt-session/smoke.go` (the `--subscription` flag, the SKIP rows, and Task 6's guard before a subscription row runs), `wt/docs/wt-smoke.md`
- Test: `wt/internal/smoke/smoke_wire_test.go`, `wt/cmd/wt-session/smoke_test.go`

**Interfaces:**
- Consumes: `catalog.UsesSubscription`, `subscriptionGuard`.
- Produces: `walkWire(rows, ags, includeStart, allowSubscription string) (cands []Candidate, droppedSubscription []string)`; `wt smoke <id> --subscription` (the flag without an id, or with a non-subscription id, is exit 1 `--subscription needs the id of one subscription model or alias`); decision 9's SKIP rows in the existing text and `--json` shapes.

- [ ] **Step 1: Write failing tests**:
  - `TestSmokeNeverSpendsTheSubscriptionByDefault`: plain, `--json` and non-TTY runs; a fake `claude` records invocations; none happen for subscription rows or aliases containing one; each dropped id is one SKIP row with `agent: "claude"` and the hint in `error`.
  - `TestSmokeNamedSubscriptionNeedsTheFlag`: `wt smoke claude-haiku-5-5` without `--subscription` is exit 1 with the hint.
  - `TestSmokeSubscriptionNeedsTheID`.
  - `TestSmokeSubscriptionRunsOnlyClaude`.
  - `TestSmokeSubscriptionRunsTheGuard`: no login gives exit 1 before any agent runs.
- [ ] **Step 2: Run** `cd wt && go test ./internal/smoke ./cmd/wt-session -run 'Smoke.*Subscription|SmokeNever' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** decision 9 and the smoke page.
- [ ] **Step 4: Run** `cd wt && go test ./internal/smoke ./cmd/wt-session`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(smoke): subscription rows only on explicit opt-in (#597)`.

### Task 8: Docs

**Files:**
- Modify: `wt/docs/wt-model-server.md` (the ping and error rows already landed in Tasks 1 and 2):
  - "Where it runs" (no TLS; same-host terminator; forwarding headers only refuse)
  - new "Subscription passthrough" section: the key, the wrong-type rule, restart, header table, guard, the hazards that keep it off, owner-only, token placement, the old-binary notes (upgrade every syncing `wt-model` first)
  - "Known limits" (tunnel caveat; undetectable plain-http forwarder; shared token)
- Modify: `wt/docs/style-guide.md`: S7 (the gateway forwards the client's own login in the opt-in header mode only), S3 (the gateway's own errors include the three 403 codes), S1 (the subscription lines after the announce line).
- Modify: `docs/guides/00-config-map.md`: the `~/.config/wt-model/config.toml` row gains `[gateway] subscription_passthrough`, written by hand only; the agent-wt `config.toml` row gains `[model-server] subscription`, by hand only.
- Modify: `wt/docs/wt-session.md`:
  - "The refusals" (six new: opt-in, endpoint, server, version, login, launch profile)
  - native vs passthrough contrast; a header-mode shell carries the token; owner-only paragraph
- Modify: `wt/docs/wt-config.md` (remote plain http cannot use passthrough; `[model-server] subscription`; owner-only paragraph).
- Modify: `wt/docs/wt-agents/claude-wt.md`:
  - "Authentication & credentials"
  - slot profiles
  - the `settings.json` env override caveat (what the gateway answers)
  - the window note (decision 11)
- Modify: `wt/docs/wt-agents/slot-profiles.md`:
  - "What Claude Code is given"
  - the fable/opus subscription + sonnet/haiku alias example
  - failures (the header-value refusal; a launch profile cannot override the header env); owner-only paragraph
- Modify: `wt/docs/wt-model.md` (`model add claude-subscription … --family <f> --context-window N`, bare id, not in the Models tab form).
- Modify: `wt/docs/internals/packages.md` (`internal/claudelogin`, `custom_headers.go`, `subscription_guard.go`, `subscription_hazards.go`) and `wt/docs/internals/agents.md` (header mode, the strip on every launch).
- Modify: `docs/guides/02-providers-and-models.md` (the provider row; owner-only warning).
- Modify: `docs/guides/04-litellm-config.md` and `docs/guides/14-litellm-install-and-configuration.md`:
  - keyless rows, unrouted while the proxy has `ANTHROPIC_API_KEY`, header forwarding or a foreign `anthropic/` row
  - `store_prompts_in_spend_logs`: never paste a token into a prompt
- Modify: `docs/guides/06-wt-agents-and-models.md` (a short section pointing at the commands, no live state).
- Modify: `docs/guides/13-deployment-use-cases.md` (remote via a tunnel; owner-only paragraph in the remote section).
- Modify: `wt/CLAUDE.md` ("wt-model" gateway mode, `Config`/`Options` fields, the never-logged invariant; "Agents" header mode; the "Go module" package list gains `internal/claudelogin`).
- Modify: root `CLAUDE.md` (wt-model line: `[gateway]`; `wt smoke --subscription`).
- Modify: `.claude/skills/wt-manage-slot-profiles/SKILL.md` (subscription ids as fable/opus targets; never smoke them without asking).
- Modify: the spec's "Phases" line (phase 3 delivered, plan path) and its "Subscription models" paragraph (decision 6's deviation).

- [ ] **Step 1: Update the docs.** Keep them state-independent: show `wt-model model list` and `wt model list --server`, and no live ids beyond illustrative examples.
- [ ] **Step 2: Run** `make lint` from the repo root. Expected: exit 0, `ALL LINKS OK`. Then `grep -rn 'ANTHROPIC_AUTH_TOKEN' wt/docs docs/guides` and check every hit says token mode, and `grep -L 'own login' docs/guides/02-providers-and-models.md docs/guides/13-deployment-use-cases.md wt/docs/wt-model-server.md wt/docs/wt-session.md wt/docs/wt-config.md wt/docs/wt-agents/slot-profiles.md` prints nothing.
- [ ] **Step 3: Commit** `docs: Claude subscription passthrough (#597)`. Stage the paths by name, and confirm with `git status --short` that no `wt/bin/` file is staged.

### Task 9: Live check and PR

Run after `make install` from this branch. The owner approves each marked step. Never read either LaunchAgent plist or `~/.config/litellm/`. Never print a token or a matching log line (counts only). Never provoke a rate limit (single tiny requests, no loops). Keep `store_prompts_in_spend_logs` in mind: never type a token into a prompt.

**Sending the wt token:** only through a 0600 header file built with shell builtins, never on a command line: `umask 077; { printf 'x-wt-model-token: '; tr -d '\n' < "$tokfile"; printf '\n'; } > "$scratch/h"`, then `curl -H @"$scratch/h" …`, and `rm "$scratch/h"` after the step. **Counts:** "token-absence" counts lines matching `sk-ant-oat` but not `FAKE` or `SENTINEL`; "canary" counts lines matching `sk-ant-oat-FAKE`.

- [ ] **Step 0: Owner approves: restart serve on the new binary**, with `[gateway]` still absent. First confirm `pgrep -fl wt-model` shows no other wt-model (another worktree's build) that could sync. The owner runs the restart from a terminal that does not depend on the gateway. Prove the new code runs: `ps -o lstart= -p <serve pid>` (pid from `serve.json`) is later than `~/.local/bin/wt-model`'s mtime, and step 1's curl answers 403 `subscription_off`. A 401 there means an old serve: stop.
- [ ] **Step 1: Baseline** (no approval needed):
  - Record token-absence and canary counts over `~/.litellm.log ~/.litellm.err.log ~/.wt-model.log ~/.wt-model.err.log`, `pg_dump -h localhost litellm`, the Redis values (`redis-cli --scan` piped into a per-key `GET`/`DUMP`), and the number of files under `~/.claude/projects` holding the wt token (`grep -rlF -f "$tokfile" ~/.claude/projects | wc -l`). Expected 0 except the last, which is recorded as is.
  - With the mode still off, `curl -H @"$scratch/h"` on `/v1/messages` with a dummy `Authorization: Bearer sk-ant-oat-FAKE`. Expect 403 `subscription_off`.
- [ ] **Step 2: Owner approves the switch:**
  - Take a dated backup of `~/.config/wt-model/config.toml`, then set `[gateway] subscription_passthrough = true`.
  - Validate before restarting: `wt-model litellm status` exits 0 and prints no `[gateway]` warning.
  - The owner restarts the `wt-model` serve LaunchAgent from a terminal that does not depend on the gateway (this cuts in-flight streams, the checking session's included), with the rollback ready: restore the backup, then kickstart.
  - Check that serve prints the subscription line and no hazard warning, and that the ping shows the field.
- [ ] **Step 3: Owner approves (registry write + one proxy restart):**
  - `wt-model litellm sync --dry-run` after adding the model with `--dry-run`: abort the live check if it reports any hazard (`ANTHROPIC_API_KEY`, header forwarding, a foreign `anthropic/` row anywhere in `wt-model litellm list`).
  - `wt-model model add claude-subscription claude-haiku-5-5 --family <f> --context-window <owner's value>`. This adds the provider row and a `config.yaml` row, one serialized proxy restart, which needs an explicit go-ahead.
  - `wt-model alias set alias/haiku-sub --target claude-haiku-5-5:1 --target <a cheap cloud model>:2 --dry-run`, then a real run (a database row, hot).
  - `wt-model litellm list` shows both rows, the `anthropic/` row has no `api_base`, and no other `anthropic/` row has a foreign `api_base`.
- [ ] **Step 4: No-foreign-backend check** (owner approves; the spike harness, never the live proxy or the real login):
  - Scratch LiteLLM on 4099 started with `ANTHROPIC_API_BASE=http://127.0.0.1:9100/b/sub` in its environment, and the mock on 9100, as in `2026-11-22-litellm-aliases-groups-spike/README.md`.
  - Pre-check: send a dummy `Bearer sk-ant-oat-FAKE` request for the subscription id straight to the scratch LiteLLM, and confirm the mock's log shows it. If the mock did not receive it, abort the step: LiteLLM would send the real login to api.anthropic.com.
  - A scratch `wt-model serve` from this branch, isolated: `HOME`/`XDG_CONFIG_HOME` on a scratch dir (a scratch wt-model home with `[gateway]` on, so `serve.json` is not overwritten), `WT_REGISTRY` on a scratch registry, `WT_LITELLM_CONFIG` on the scratch config, `WT_LITELLM_RESTART_CMD` a no-op (or the scratch proxy's restart), and a scratch token file.
  - Launch a real four-slot claude session there with a **sentinel** wt token and the session env pointed at the scratch serve (`WT_MODEL_MODE=remote`, `WT_MODEL_URL`, `WT_MODEL_TOKEN_FILE`): fable/opus on the subscription row (reaching the mock), sonnet/haiku on mock `openai/` rows. Run one `claude -p` per slot. The mock's masked log shows an OAuth-class `Authorization` only on the subscription path, and `Bearer none`/the row key on the others.
  - Run the same `claude` with `HTTPS_PROXY` pointed at a local CONNECT-logging proxy and record every host it contacted besides the gateway. CONNECT shows hosts only; any Anthropic host other than the gateway is recorded as an open question (whether `ANTHROPIC_CUSTOM_HEADERS` reaches it) and a follow-up, before step 7.
- [ ] **Step 5: Guard checks** (dummy credentials only, against step 4's scratch serve rebound to the host's LAN address; the live serve and `~/.config/agent-wt/config.toml` are not touched):
  - From the LAN address, `curl -H @"$scratch/h" http://<lan ip>:<port>/v1/messages` with `Bearer sk-ant-oat-FAKE`. Expect 403 `subscription_insecure`.
  - The same without the header file and with the scratch token as Bearer. Expect today's legacy behavior.
  - Via `ssh -L` to 127.0.0.1 with the header. Expect forwarded (to the mock).
  - `WT_MODEL_MODE=remote WT_MODEL_URL=http://<lan ip>:<port> WT_MODEL_TOKEN_FILE=<scratch>` `wt` refuses a subscription launch client-side.
  - Stop the scratch harness's LAN listener.
- [ ] **Step 6: Canary through the live proxy** (no approval needed): `curl -H @"$scratch/h"` to the live gateway on 127.0.0.1 with `Bearer sk-ant-oat-FAKE` for `claude-haiku-5-5` (expect an upstream 401 from Anthropic). Repeat step 1's counts: the token-absence counts must equal the baseline, and the canary count must be 0 in every place. Any canary hit means the live proxy logs or stores the header: stop before step 7.
- [ ] **Step 7: Owner approves the real requests:**
  - Set `[model-server] subscription = true` in `~/.config/agent-wt/config.toml` (backup first; owner's edit).
  - `wt smoke claude-haiku-5-5 --subscription`: one turn, success.
  - Then a mixed session: a test slot profile with fable/opus = `claude-haiku-5-5`, the cheap stand-in for both, and sonnet/haiku = `alias/…` non-subscription aliases. Run one tiny `claude -p --model opus` and one `--model sonnet`.
  - Per-model counts come from `wt stats` or the spend endpoint, not prompts. The opus call books on `claude-haiku-5-5`. The sonnet call and Claude Code's background haiku call book on the alias targets.
  - Record what `/context` shows for the window (decision 11).
  - The child env (via `ps eww <pid> | tr ' ' '\n' | grep -c '^ANTHROPIC_AUTH_TOKEN='`) counts 0.
- [ ] **Step 8: Fallback view:** `curl -H @"$scratch/h"` with no `Authorization` to `alias/haiku-sub`. LiteLLM skips the keyless member and lands on order 2. Confirm from the spend log's `model_group`/model, as a count.
- [ ] **Step 9: Token-absence counts:** repeat step 1's counts (logs, `pg_dump`, Redis). All token-absence counts must equal the baseline (0). Also count `x-wt-model-token` occurrences in the same logs (expected 0). The `~/.claude/projects` count is recorded (tool subprocesses inherit the env; documented).
- [ ] **Step 10: Owner approves cleanup:**
  - `wt-model alias rm alias/haiku-sub --yes`.
  - `wt-model model rm claude-haiku-5-5` (the one other proxy restart; go-ahead needed). Steps 3 and 10 are the only proxy restarts in the check.
  - Remove the `claude-subscription` provider row, or record that it is kept (owner's choice).
  - `wt-model litellm list` shows no `anthropic/` row left.
  - `wt profile slots rm <the test profile>`.
  - `wt worktrees`, then `wt remove` the worktrees and branches the step 4 and step 7 launches created, with approval.
  - Leave or revert `[gateway]` and `[model-server] subscription` as the owner says (backups from steps 2 and 7).
  - Stop the scratch harness; delete `$scratch/h`.
- [ ] **Step 11:** Run `make pre-pr` with `run_in_background` and read the exit from the log. Expected: exit 0.
- [ ] **Step 12: Open the PR** with `gh pr create --body-file` and `Refs #597`, with no closing keyword. The body ends with the generated-with line, and lists the live-check results (steps 0–9) and the follow-up issues filed for "Not in this plan".

---

## Self-review notes

- **Coverage of the owner's decisions:**
  - local and remote: Tasks 1, 2 and 6 (the guard is about transport, not location)
  - header name: Task 1
  - off by default and explicit: Task 2, decisions 1 and 12, `TestSetupNeverEnablesPassthrough`
  - loopback/https guard: Task 1, plus the client twin in Task 6
  - owner-only docs: Task 8
  - token never stored or logged: Tasks 1, 2, 5, 6 and Task 9 steps 6 and 9
  - claude-only: Task 4
  - legacy byte-for-byte: Task 1's unmodified tests plus `TestLegacyUpstreamHeadersUnchanged`
- **Order of Consumes:** gateway (T1) → switch and ping (T2, uses T1's Config fields) → provider, rows and hazards (T3) → catalog (T4, uses T2's option and T3's constants) → driver (T5, uses T4's `UsesSubscription`/`ToModel`) → guards (T6, uses T2's ping, T4 and T5) → smoke (T7, uses T6's guard) → docs (T8) → live (T9).
- **Lessons carried from the phase 1 and phase 2 reviews:**
  - Real LiteLLM facts beat assumptions: a duplicate `/model/new` is a 500, `/model/info` strips `api_key`, and equality goes by the stamped hash. This phase adds no database writer of its own; the subscription alias rows go through phase 2's `addConverged`.
  - The one 15 s database-half deadline is unchanged.
  - Route warnings must reach lifecycle output: Task 3's hazard warnings go through `plan.Warnings`, which `SyncAndWarn`/`routePrintf` already print.
  - Every proxy restart stays serialized through the existing path, and no new restart caller is added.
  - A zero option keeps old behavior: `Config{}` is legacy, and `CatalogOptions{}` blocks subscription rows.
  - Hermetic tests: TestMain isolation, no package vars, per-call seams (`hooks.claudeLogin`, `Config.ErrorLog`, injected `ProxyEnv`), httptest upstreams only.
  - Old-binary hazard: the live check restarts serve on the new binary first and proves it (step 0), as phase 2's did.
  - Implementers never read the LiteLLM plist and never commit binaries (Global Constraints).
- **Unverified before execution (live check, not mocks):**
  - whether `CLAUDE_CODE_MAX_CONTEXT_TOKENS` caps a recognized Claude id (decision 11)
  - whether `/model/info` and `/v1/models` list a keyless `config.yaml` `anthropic/` row like a keyed one (the `routed` map depends on it; step 3)
  - whether Claude Code attaches `ANTHROPIC_CUSTOM_HEADERS` to hosts other than `ANTHROPIC_BASE_URL` (step 4 records hosts only)
  - whether the scratch LiteLLM honors `ANTHROPIC_API_BASE` (step 4's pre-check decides)
  - `claude auth status` behavior offline (the 10 s timeout and "no login" cover it, but the message may mislead)
  - Claude Code versions after 2.1.330
- **Unresolved for the owner:**
  - the `context_window` values and family for the subscription ids
  - whether `claude-sonnet-5-x` is a real subscription id (not in the repo; no seed list is shipped, so models are added by hand)
  - whether to file the legacy `x-litellm-api-key` strip and native TLS as follow-ups now
