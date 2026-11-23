# A Claude subscription behind wt-model (phase 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One Claude Code session mixes the owner's own Claude subscription (for example `fable` and `opus` on `claude-fable-5-2` and `claude-opus-5-5`) with other models (`sonnet` and `haiku` on `alias/...` ids or plain registry ids), all through wt-model's gateway, for local and remote sessions. The gateway gains an opt-in header mode (`x-wt-model-token`) that forwards the client's own claude.ai login in `Authorization`. The registry gains a `claude-subscription` provider whose rows are keyless `anthropic/<id>` LiteLLM rows. Those rows are offered only to the claude agent. A claude launch that names one keeps the login (no `ANTHROPIC_AUTH_TOKEN`), sends the wt token in `ANTHROPIC_CUSTOM_HEADERS`, and refuses to start without a claude.ai login or without the session machine's own opt-in (#597).

**Architecture:** Host side: `modelgateway.Config.SubscriptionPassthrough` (from a `[gateway]` table in the wt-model home `config.toml`, read once at `serve` start) turns on a second authentication branch in the gateway. It is chosen only when the request carries `x-wt-model-token`, runs only from a loopback peer or over a real TLS connection to wt-model itself, and forwards only a `Bearer sk-ant-oat…` `Authorization`. When the header is absent the existing token swap runs unchanged. Serve can speak TLS itself (opt-in `[gateway] tls_cert`/`tls_key` in the same home `config.toml`; plain http stays the default), and a session trusts such a server through one `[model-server] ca_file`. The ping advertises the mode. The catalog blocks subscription rows while it is off. Provider side: one family-less, cloud `Spec` in `internal/provider/table.go` with `Policy{Prefix: "anthropic/", Cloud: true}`. Its models use the real Claude id as registry id and gateway id, and are routed like every other cloud model (a `config.yaml` row with no `api_key` and no `api_base`), unless the proxy could leak or bill them (an `ANTHROPIC_API_KEY` in its env, header forwarding on, or a foreign `anthropic/` row anywhere in the proxy). Session side: `catalog` decides "this row uses the subscription" from the provider id, its own and its alias targets'. Eligibility gives such rows only to `claude`. The claude driver picks header mode for the whole session when any slot (or the `-M` model) uses one. Every claude launch, native included, strips an inherited wt token line from `ANTHROPIC_CUSTOM_HEADERS`. The four launch paths run one pre-launch guard before any client session or start job: the session machine opted in, the endpoint is https or loopback, the ping says the mode is on, Claude Code is new enough, and `claude auth status` shows a claude.ai login. `wt smoke` skips subscription rows unless they are named and opted in.

**Tech Stack:** Go 1.26.7 (`wt/` module), `net/http/httputil.ReverseProxy` (`Rewrite`), `net/netip`, `crypto/tls` + `crypto/x509` (native TLS, stdlib only), `github.com/BurntSushi/toml` (home config), LiteLLM 1.109.1 (keyless `anthropic/` rows; it forwards a client OAuth token to every `anthropic/` deployment whatever its `api_base`, `ANTHROPIC_OAUTH_FORWARD_PROVIDERS`, spike 4), Claude Code >= 2.1.227 (`ANTHROPIC_CUSTOM_HEADERS`; checked on 2.1.330).

**Spec:** `docs/superpowers/specs/2026-11-22-litellm-aliases-groups-design.md` ("Design of A": Subscription models, Gateway, Claude launch; "Constraints and risks" 1; "Real subscription check"; decisions 2, 3, 4). Spike patch: `docs/superpowers/specs/2026-11-22-litellm-aliases-groups-spike/gateway-header-mode.patch`. Builds on phase 1 (`2026-11-22-litellm-db-routes-phase1.md`, PR #684) and phase 2 (`2026-11-22-litellm-aliases-groups-phase2.md`, PR #686).

## Global Constraints

Owner decisions (binding) and values from the spec and the code.

- The header name is `x-wt-model-token` (Go canonical form `X-Wt-Model-Token`).
- Subscription passthrough is for local and remote sessions alike.
- Passthrough is **off by default**, on both sides. The host operator turns it on with `[gateway] subscription_passthrough = true` in `~/.config/wt-model/config.toml` (`config.ProxyHomePath()`), then restarts serve. A session machine opts in separately with `[model-server] subscription = true` in `~/.config/agent-wt/config.toml` (decision 12). No production code path writes `true` to either key: not `wt-model setup`, not `litellm set`, not the Mode tab. Tests may write it only into a scratch home under `t.TempDir()`.
- The gateway accepts the mode from a loopback peer or over a real TLS connection to wt-model itself (`r.TLS != nil`, native TLS, decision 13), and refuses it from a non-loopback peer over plain http. Loopback is `127.0.0.0/8`, `::1` or `::ffff:127.x`, judged from `r.RemoteAddr`. `X-Forwarded-*`, `Forwarded` and `X-Real-IP` are never consulted to **allow** a request; they are read only to **refuse** one, every element of every value counts, and any proto indicator other than `https` refuses on both branches (decision 3).
- Native TLS (decisions 13, 14): plain http stays the default. The host turns TLS on with `[gateway] tls_cert` and `tls_key` in `~/.config/wt-model/config.toml`; a session machine trusts a private certificate with `[model-server] ca_file` in `~/.config/agent-wt/config.toml` (or `WT_MODEL_CA_FILE`). All three keys are hand-edited: no production code path writes them (not `wt-model setup`, not `litellm set`, not the Mode tab); setup's own readiness wait trusts the home's `tls_cert` directly, and when the local session lacks `ca_file` setup prints the line to add (decision 13). The private key file has no group or other permission bits (0600 or stricter, the token file's rule). A `tls_cert` file holding a private-key block, or `tls_cert` and `tls_key` naming the same file, refuses; a `ca_file` holding a private-key block refuses on the client. Serve refuses to start on a bad, half-set or wrong-typed certificate configuration, before listening, and no message ever holds key or certificate bytes (paths, a fixed reason and the OS error only). TLS 1.2 is the floor on both sides; for TLS 1.2 serve offers only the six ECDHE AES-GCM/ChaCha20-Poly1305 suites (Go's default 1.2 list still has ECDHE-CBC-SHA). Nothing anywhere sets `InsecureSkipVerify`, and no flag or env var skips verification.
- When `x-wt-model-token` is absent, the gateway is **byte-for-byte unchanged**: the existing tests `TestGatewayRejectsMissingOrWrongToken`, `TestGatewaySwapsCredentials`, `TestGatewayAcceptsXAPIKey`, `TestGatewayStreamsWithoutBuffering` and `TestGateway401IsOpenAIStyle` in `wt/internal/modelgateway/gateway_test.go` pass unmodified. That includes the existing pass-through of a client `x-litellm-api-key` in legacy mode (see "Not in this plan").
- Neither wt-model nor LiteLLM stores or logs the wt token or the client's login. No access log, and no `log`/`slog`/`fmt` print of a request, header or body, in `modelgateway` or `modelserver`. Tests (Tasks 1 and 2) and the live check (Task 9) pin this.
- Subscription rows (and every alias with a subscription target) are offered only to the agent `claude`. pi, codex, copilot and opencode never see them: not in eligibility, not in pi's `models.json`, not in rotation, not in smoke.
- The docs say: the owner's own login only. A shared host would be intermediating other people's credentials, which Anthropic's terms forbid and wt cannot detect.
- wt places the wt token only as the value of one process-environment entry, `ANTHROPIC_CUSTOM_HEADERS=…x-wt-model-token: <token>…`. wt never puts it in argv, the `--settings` JSON or a file, nor in `wt profile show` unmasked, and never sends it on a control API call (control keeps `Authorization: Bearer <token>` only). Claude Code's tool subprocesses and hooks inherit that env (as they inherit `ANTHROPIC_AUTH_TOKEN` today), so a header-mode session's shell carries the token; the docs say so, and every wt launch strips an inherited wt line (decision 7).
- Wire changes stay additive within `modelapi.APIMajor = 1` / `SchemaVersion = 2`. A new key reaches its fixture **and** its row on `wt/docs/wt-model-server.md` in the same commit: `docs/contracts/model-api.sample.json` (ping field, three gateway error members) and `docs/contracts/wt-model-config.sample.toml` (`[gateway]` with `subscription_passthrough` only; `tls_cert`/`tls_key` stay out of the sample, since an empty string would read as "set but empty", and are pinned by a Go test on a scratch config instead). `docs/contracts/registry.sample.toml` gains one provider and one model; `registry.written.sample.toml` (generated with tomli-w, checked byte-for-byte) does not change.
- `serve`'s announce line (`announceFormat`, `wt/cmd/wt-model/serve.go:30`) stays byte-identical and first, still reporting `ln.Addr()` with no scheme, TLS on or off. The TLS state and the mode are reported on separate stderr lines printed after it (style guide S1 is amended in Task 8).
- `serve.json` (`servestate.Record`, `RecordVersion = 1`) gains one additive field, `TLSCert` (`json:"tls_cert,omitempty"`, the public certificate's path, never the key's), because `servestate.Sessions` dials the server by its listen address and must learn the scheme (decision 13). `RecordVersion` stays 1; an older reader ignores the key.
- Tests stay hermetic (`wt/CLAUDE.md` "Go tests"):
  - no real `claude`, proxy or psql; every serve-level gateway test points the upstream at an `httptest.NewServer`, never at `127.0.0.1:4000`;
  - TestMain isolation as each package has it;
  - **per-call seams** (a field on `hooks`, `Config.ErrorLog`, an injected login func), **never a new package var**; a nil `hooks.claudeLogin` in a test binary refuses (decision 8);
  - no test listens on a non-loopback address;
  - TLS tests use `httptest` TLS servers or certificates generated in memory (`internal/tlstest`, ECDSA P-256); certificate and key files exist only under `t.TempDir()`, and no test reads the owner's real certificate paths;
  - every `Test*` has a what-and-why comment;
  - `make pre-pr` passes on a machine with no claude login.
- Test sentinels: the login sentinel must look like an OAuth token to be forwarded, so it is `sk-ant-oat01-wtSENTINEL`; the wt token sentinel is `wt-SENTINEL-tok`. Task 9's token-absence counts exclude lines holding `SENTINEL` or `FAKE`.
- Import ratchet (`internal/depcheck`): the gateway change stays in `modelgateway`/`modelserver`. Session libraries (`catalog`, `agents`, `profiles`, `smoke`, `tui`) read only `modelapi` fields plus the provider id constant through `config`. No allow-list entry is added. The new `internal/claudelogin`, `internal/tlsfile` and `internal/tlstest` import only the standard library (`tlstest` also `testing`, like `internal/gittest`) and go in `otherPackages`.
- Implementers never read, `cat`, `plutil` or grep the LiteLLM LaunchAgent plist (`~/Library/LaunchAgents/local.litellm.proxy.plist`) or the wt-model one. A subagent once printed a secret from it. `litellm.ProxyEnv.IsSet` is the only way code asks about the proxy environment.
- Implementers never commit a built binary (a docs commit once swept in `wt/bin/…`). Stage paths by name, never `git add -A`/`.`, and check `git status --short` before each commit.
- Long Go runs (`./internal/...`, about 10 min) go through `run_in_background` with the exit read from the log; the per-task runs below are scoped to stay under the foreground limit.
- Commits end with the attribution line the session's system reminder gives. The PR body says `Refs #597`, never a closing keyword. Run `make pre-pr` with `run_in_background` (about 12.5 min) and read the exit from its log.

## Review Focus

The risks the tests would otherwise miss, each with the tests that pin it.

1. **A Claude login leaves the client or the gateway when it should not**: an off gateway, a plain-http remote peer, a spoofed or mixed forwarding header, a foreign `anthropic/` row in the proxy, a server the session machine never opted into, an https server whose certificate was never verified, serve falling back to plain http after the operator asked for TLS (a half-set or wrong-typed key, a loose key file), or a non-OAuth `Authorization` (the wt token itself) forwarded upstream. Pinned by `TestSubscriptionRefusedFromNonLoopbackOverHTTP`, `TestSubscriptionModeOffForwardsNothing`, `TestSubscriptionNonOAuthAuthorizationRefused` (Task 1); `TestServeRefusesABadTLSConfig`, `TestLoadRefusesALooseKey`, `TestGatewaySettingsTLSKeys` (Task 2b); `TestSubscriptionHazards`, `TestServeKeepsModeOffOnHazard` (Task 3); `TestSubscriptionWithoutOptInRefuses`, `TestSubscriptionPlainHTTPRemoteRefusedBeforeSending` (Task 6); `TestSubscriptionGuardUntrustedCertRefusesBeforeRegistration` (Task 6b).
2. **The token-swap mode changes for clients without the header**, breaking every pi, codex and non-subscription claude session. Pinned by the five unmodified legacy tests, `TestLegacyUpstreamHeadersUnchanged`, `TestLegacyRequestFromNonLoopbackUnaffected` (Task 1); `TestClaudeBuildSlotsTokenModeUnchanged`, `TestTokenModeStripsStaleWtHeader` (Task 5); `TestPlainServeUnchanged` (Task 2b).
3. **A secret is logged, printed or written**: a header or login in a log, key bytes in a refusal, a private key copied out as a "certificate", or the wt token reaching a third party through an inherited `ANTHROPIC_CUSTOM_HEADERS` on a native or nested launch. Pinned by `TestSubscriptionSecretsNeverLogged` (Task 1); `TestServeNeverPrintsForwardedSecrets` (Task 2); `TestErrorsNeverCarryKeyBytes`, `TestLoadRefusesBadFiles` (key in cert, Task 2b); `TestNativeLaunchStripsInheritedWtHeader`, `TestEveryAgentStripsInheritedWtHeader`, `TestProfileShowMasksTheHeaderToken` (Task 5); `TestSubscriptionLaunchWritesNoFileAndNoArgv`, `TestSubscriptionLaunchChildEnvAndArgv` (Task 6); `TestCAFileWithoutPEMRefused` (key in ca_file, Task 6b).
4. **A non-claude agent, or a paid API key, gets a subscription route.** Pinned by `TestSubscriptionRowsUnroutedWhileProxyHasAnthropicKey`, `TestOnlySubscriptionProviderBuildsAnthropicRows` (Task 3); `TestSubscriptionRowsOnlyForClaude`, `TestNonNativeModelsDropsSubscriptionRows`, `TestRotationSkipsSubscriptionRows` (Task 4); `TestSmokeNeverSpendsTheSubscriptionByDefault` (Task 7).
5. **A launch or host command that cannot work starts anyway and fails opaquely** (no login, an off or older server, an old Claude Code, a launch or slot profile overriding the header env, a server certificate the client or the agent does not trust, setup waiting on a TLS port it does not trust, `wt-model stop`/`wt-model alias` no longer reading client sessions once TLS is on), on the CLI and TUI paths alike. Pinned by `TestSubscriptionNoLoginRefusesBeforeAnything`, `TestSubscriptionServerOffOrOlderRefuses`, `TestSubscriptionOldClaudeRefused`, `TestSubscriptionLaunchProfileCannotOverrideHeaderEnv`, `TestSlotFlowParity` (Task 6); `TestSlotProfileHeaderValueRefused` (Task 5); `TestUntrustedServerCertGivesTheCAFileHint`, `TestSchemeMismatchHints`, `TestNodeCAEnv` (Task 6b); `TestSessionsDialHTTPSWhenRecorded`, `TestSetupWaitTrustsTheTLSCert`, `TestModeLocalWritesHTTPSWhenTLSConfigured` (Task 2b); `TestSmokeSubscriptionRunsTheGuard` (Task 7).

## Plan decisions

Each decision is binding for this plan. "If wrong" is what reversing it later costs.

**Needs owner confirmation** (the plan proceeds on the stated default; nothing here is invented):

1. **Subscription ids and windows.** Published on the Anthropic models overview and context-windows pages and Claude Code's model-config page (fetched 2026-11-22; high confidence for ids and windows): `claude-fable-5-2` (fable slot), `claude-opus-5-5` (opus), `claude-sonnet-5-5` (sonnet; a real API id, the `sonnet` alias target since Claude Code 2.1.284), `claude-haiku-5-5` (haiku), each with a 1M window (`context_window = 1000000`), no beta header and no `[1m]` suffix. Default: these four ids, bare, with 1000000. The `--family` value of each is not published and is the owner's to give. Whether the claude.ai subscription path gives the same 1M window as the API is unverified (Task 9 step 7 measures it).
2. **Plan billing.** Claude Code's docs say Fable may bill usage credits depending on plan and seat tier (the `/model` picker shows "Requires usage credits"), and give no per-plan statement for Sonnet 5.5 or Haiku 5.5 beyond "Haiku 5.5 runs with the 1M context window on every plan". Default: the live check uses only `claude-haiku-5-5`, and nothing in this plan runs a fable row; the owner says whether the docs should warn about credits for fable rows.
3. **An already-expired TLS certificate at serve start.** Default: refuse (decision 13; loud, but a LaunchAgent with KeepAlive then restarts and refuses in a loop). Alternative: a warning line and serve anyway.
4. **The owner's TLS endpoint form**: a LAN IP, a DNS name or a Tailscale name. It decides the subjectAltName the docs recipe leads with and Task 9's scratch certificate; a `tailscale cert` certificate is publicly trusted and needs no `ca_file`. Default: the docs lead with an IP SAN plus `127.0.0.1`, and Task 9 uses the LAN IP.

1. **Host switch: a `[gateway]` table in the wt-model home `config.toml`, read once at `serve` start.** The key is `subscription_passthrough` (bool, absent = false). It is not a `serve` flag, because the plist renderer drops flags: `wt-model setup` re-renders the plist (`modelsetup.RenderAgentPlist`, fixed `ProgramArguments`), and `ParseAgentPlist` reads only `--listen`/`--token-file`. It is not agent-wt's `[model-server]` either, which is the client's side. The home config survives setup, is already read by serve (`loadServeConfig`), and is decoded non-strictly, so an older binary tolerates the table.

   `proxyHomeDoc` gains `Gateway map[string]any` (`toml:"gateway,omitempty"`), so this binary's `SetProxy` round-trips whatever the operator wrote and a bad value never breaks `[litellm]` decoding. `GatewaySettings` interprets it: `subscription_passthrough = true` is on; absent or `false` is off; any other type (`"true"`, `1`) is off with one `wt-model: warning:` line naming the key and the expected type (off is the safe direction). A TOML syntax error in the file still fails start, as it does today for `[litellm]`.

   A `[gateway]` table in the **wrong** file (`~/.config/agent-wt/config.toml`) already triggers `config.dropLegacyGateway`'s legacy notice. When that table holds `subscription_passthrough`, the notice instead says the key belongs in `~/.config/wt-model/config.toml`.

   *Cost:* an **older** `wt-model litellm set` re-encodes the document and drops `[gateway]`, turning the mode off at the next serve restart (safe direction; documented), and with it `tls_cert`/`tls_key`, so serve comes back on plain http (sessions configured with `https://` then fail loudly at the handshake, never in clear). `SetProxy` re-encodes and drops comments, so the fixture carries no inline comment. There is no command to flip it. *If wrong:* a `wt-model gateway subscription on|off` writer, or a setup-tab toggle, can be added later over the same key.
2. **How a client learns the mode:** `modelapi.Ping.SubscriptionPassthrough bool` (`json:"subscription_passthrough,omitempty"`), set from `Options.SubscriptionPassthrough`. With `omitempty`, an older server and a server with the mode off look the same, so the client prints one message for both: `the model server at <addr> does not accept a Claude login: its subscription passthrough is off, or it is older than this wt (on the host: make install, set [gateway] subscription_passthrough = true in ~/.config/wt-model/config.toml, restart wt-model serve)`.

   The session reads the ping from `ms.client.Ping(ctx)`: `modelclient.Client.Ping` caches the first answer (`client.go:166`), so this costs no extra request and works on the TUI path, where `openModels` never runs and the picker's first request does the ping. No `modelSession` field is added.

   An off or older server already marks a subscription row `block` (Task 4's `wireRow`, or `Unmapped` on an older server), so catalog resolve refuses first. When the refused row's `ProviderID` is the subscription provider and the cached ping lacks the field, the session prints decision 2's message instead of the block reason; the guard's ping check is the backstop.

   Serve prints `wt-model: subscription passthrough is on (x-wt-model-token; refused from non-loopback peers over plain http; owner's own login only)` after the announce line: informational, so a bare line (style guide M4), not `note:`. When the listen address is not loopback and TLS is off it adds a `wt-model: warning:` line saying remote subscription requests need TLS (set `tls_cert`/`tls_key` under `[gateway]`) or a tunnel arriving from loopback, while other remote traffic is unaffected; with TLS on there is no warning. Both lines come from one pure function `subscriptionLines(listenAddr string, on, tlsOn bool) []string`. *If wrong:* a distinct "older" signal needs a second field.
3. **Guard: a loopback peer, or a real TLS connection to wt-model itself (`r.TLS != nil`), evaluated after the token check; forwarding headers only refuse.** Owner decision (revised 2026-11-22): remote header mode is allowed over native TLS (decision 13). `r.TLS != nil` says the hop into wt-model is encrypted, not that the client is who it claims: the shared wt token still authenticates, as today. So with TLS exposed on a LAN any holder of that token can send header-mode requests from anywhere, forwarding whatever login it carries; the docs say the shared token is the only authenticator and that the owner-only rule is not something wt can enforce. A direct TLS peer is the real peer, so the client-address check is skipped on that branch (a spoofed `X-Forwarded-For` cannot make it worse); the proto rule below still applies, since a re-encrypting terminator that took its client over plain http says so.

   Deployments that choose a terminator instead of native TLS: one on the same host (`ssh -L`, `tailscale serve`, Caddy or cloudflared on the server machine) reaches wt-model from 127.0.0.1 and passes; one on another host that re-encrypts to wt-model's own TLS port arrives with `r.TLS != nil` and passes; one on another host that forwards plain http is a non-loopback peer without TLS and is refused. The same-host caveats below apply only to deployments that choose a terminator.

   A same-host plain-http forwarder (Caddy/nginx without TLS, socat, Docker/OrbStack `-p`) makes a LAN client arrive from loopback. Every proto indicator counts, and every element of every value: proxies append to a client-supplied list (`X-Forwarded-For: 127.0.0.1, 192.0.2.7`) and pass through a header they do not rewrite. On either branch, any `X-Forwarded-Proto` value or `Forwarded` `proto=` element that is not `https` refuses with `subscription_insecure`. On the loopback branch, when any element of any `X-Forwarded-For`/`X-Real-IP` value or any `Forwarded` `for=` element names a non-loopback client (an unparseable or obfuscated one counts as non-loopback), the request is refused unless at least one proto indicator is present and every one present says `https`. Reading the headers only to refuse cannot be spoofed open. A same-host plain-http forwarder that adds none of them is undetectable (documented; the operator who runs a tunnel vouches for its encryption).

   A client-side twin refuses before sending. It checks `ms.ep`, the endpoint actually used (`WT_MODEL_URL` included). A header-mode launch whose url is `http://` to a host that is not loopback exits 1, since by the time the server refuses, the login has crossed the wire. The rule is one pure predicate, `subscriptionEndpointOK(ep modelclient.Endpoint) error` (no dial), which the guard calls. The host is parsed with `netip.ParseAddr` + `Unmap` and must be in `127.0.0.0/8` or `::1`; the name `localhost` (trailing dot allowed) matches exactly; anything else, `0.0.0.0` and `127.1` included, is not loopback. An `https://` url to any host is accepted; its certificate is verified against the system roots plus `ca_file` (decision 14), and nothing skips verification. There is no operator escape for plain-http remote. *Cost:* the guard is no longer "loopback only"; it is "loopback or encrypted", and remote safety rests on the shared token plus the certificate check. *If wrong:* narrowing TLS peers to an allow-list is one predicate in `peerTrusted` plus a key; dropping native TLS reverts Tasks 2b and 6b and leaves the terminator rules above as they are.
4. **Header handling and check order.**

   Check order in `authenticated`:

   | Step | Condition | Response |
   |---|---|---|
   | 1 | Header present: exactly one value, non-empty, equal to the token (constant time); `x-api-key` is **not** examined. Header absent: today's `validToken`. | else 401, existing body |
   | 2 | Header branch and mode off | 403 `subscription_off` |
   | 3 | Header branch and `peerTrusted` false: peer neither loopback nor TLS, or the forwarding-header rule refuses (decision 3) | 403 `subscription_insecure` |
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

   *Model rows:* the registry id is the bare real id, equal to the Anthropic API id (`claude-fable-5-2`, `claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5`; no date suffix, no `anthropic/` prefix, no `[1m]`; `model_name` the same), per spec decision 4. The dateless ids are pinned snapshots, and LiteLLM's `anthropic/<id>` row forwards the id verbatim. `modeladmin.checkID` and `DeriveID` accept and derive a slash-less id **only** for this provider. Family stays required like any model (`--family <f>`). Every subscription model needs a `context_window` that the owner supplies (1000000 for the four published ids, owner confirmation 1): `wt-model model add` refuses one without `--context-window`, because without it `claudeWindow` withdraws the declaration for the whole profile. Cost is left empty, so LiteLLM books $0 while Claude Code prints list price (documented). No `source` key is written (`planAdd` sets it only for providers cloud-sync covers), and cloud-sync never touches the provider.

   *LiteLLM rows:* these are cloud rows, so they live in `config.yaml` by phase 1's rule. Adding or removing a subscription model is a `config.yaml` change and costs the usual serialized proxy restart. An alias over one is a database row (phase 2) and is hot. `prepareModel` refuses a `claude-subscription` model whose provider row has `auth.base_url` or `auth.secret_ref`, or whose `auth.type` is `native`.

   A sync leaves every subscription row **unrouted**, with one warning saying why, while any of these holds:
   - `litellm.ProxyEnv.IsSet("ANTHROPIC_API_KEY")`: a keyless row would serve any caller without a login (any holder of the shared token, by model name) on the host's API key;
   - `config.yaml` (which wt owns) turns on client-header forwarding (`forward_client_headers_to_llm_api`) in `general_settings` or `litellm_settings`: the host master key could be forwarded to Anthropic;
   - `/model/info` lists any deployment, wt-managed or not, whose provider is `anthropic` and whose `api_base` is set and is not `https://api.anthropic.com`, or an `anthropic/*` wildcard: LiteLLM forwards a client OAuth token to every `anthropic/` deployment, and Claude Code can name any `model_name`.

   `store_prompts_in_spend_logs` on is one warning only (prompts, not headers). The check is `litellm.SubscriptionHazards(ctx, …)`, shared with serve (decision 1's start path), and takes the proxy env and the `/model/info` reader as arguments (no package var). Serve runs it synchronously, after `GatewaySettings` and before `NewServer`, under a 5 s bound, with `ProxyEnv` and `ModelInfo` taken from two new fields on `host` (`testHost` returns fakes, so no serve test reads the real plist). With the mode on and a hazard found serve keeps the mode **off**, prints one `wt-model: warning:` line naming the hazard (never a value), and the ping reports off. A proxy that does not answer within the bound (at login it may still be starting) is a warning and the mode stays on, because the sync gates routing: a sync that cannot read `/model/info` leaves every subscription row **unrouted** with one warning (fail closed) and routes it on the next sync that can.

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

   `ANTHROPIC_CUSTOM_HEADERS` is **not** added to `ReservedEnvKeys`. Slot-profile validation instead refuses a profile `env`/`config_content.env` value for it that holds an `x-wt-model-token` or `authorization` line. A slot profile's `env` and `config_content.env` are not process env: `claudeSlotSettings` writes them into the `--settings` env block, which outranks the process env, so in header mode `BuildSlots` refuses a profile whose `env` or `config_content.env` names `ANTHROPIC_CUSTOM_HEADERS` (it would replace the merged value: an opaque 401) or `CLAUDE_CODE_OAUTH_TOKEN` (it would displace the checked login), naming the key; in token mode both stay allowed as today. A resolved **launch** profile (applied after the build, `profiles.ApplyEnvAndArgs`, last entry wins) whose env names `ANTHROPIC_CUSTOM_HEADERS`, `ANTHROPIC_AUTH_TOKEN` or `CLAUDE_CODE_OAUTH_TOKEN` is refused in header mode, exit 1: it would replace the merged header (an opaque 401) or displace the checked login. *If wrong:* the merge is one pure helper; switching to replace is one line.
8. **No login: refuse, never warn or fall back.** The login check runs `claude auth status --json` (and `claude --version`) through `exec.LookPath("claude")`. The environment is scrubbed of `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_OAUTH_TOKEN` and `CLAUDE_CODE_USE_*`, because with those set the command reports `oauth_token`, a false positive. A 10 s timeout applies. Only `loggedIn`, `authMethod`, `apiProvider` and `loginExpiresAt` are decoded, so the email and org fields are never kept, printed or logged. A login is accepted only when `loggedIn && authMethod == "claude.ai" && apiProvider == "firstParty"`. Exit 1, a parse failure or a timeout means no login.

   In a test binary (`testing.Testing()`), a nil `hooks.claudeLogin` returns `claude login check not stubbed in a test` instead of running the real `claude`, as `hooks.newClient` already refuses outside loopback (`app.go:106`).

   The refusal is `wt: <model> uses your Claude subscription, but no claude.ai login was found: run 'claude auth login' (nothing was started or registered)`, exit 1, before the client session and before any start job. A version below 2.1.227 refuses the same way, naming the minimum version. A login expiring within 24 h is one `wt: note:` line.

   Falling back to token mode would send subscription ids with the wt token as `Authorization`, so the keyless row answers 401. In a group, it would silently land every turn on the fallback. Either way the cause would be hidden. *If wrong:* a warn-and-continue mode is one branch.
9. **Smoke never spends the subscription by default.** `smoke.walkWire` drops subscription rows (and aliases with a subscription target) from candidates and eligibility, and returns them: `walkWire(...) (cands []Candidate, droppedSubscription []string)`; `CandidatesWire` and `EligibilityWire` pass the list on (signatures in Task 7). In a walk without an id, each dropped model is one ordinary smoke row `{agent: "claude", model: <id>, status: "SKIP", error: "uses your Claude subscription: pass --subscription to smoke it"}`, printed through the existing per-row SKIP line and the existing `--json` row shape (no new key). A subscription id named without `--subscription` is exit 1 with that hint. Only `wt smoke <id> --subscription` runs one, and only for `claude`, after Task 6's guard. `--json` and non-TTY runs follow the same rule. Automated tests never exec a real `claude`.
10. **Old binaries.**
    - *New session, old server:* the old catalog has no `claude-subscription` Policy, so its rows are `block` (`Unmapped`); decision 2 maps that to the upgrade message before any login is sent.
    - *Old session, new server with the mode on:* the old wt shows subscription rows to every AllProviders agent and launches claude in token mode. The keyless row answers 401 `Missing Anthropic API Key` (decision 5 keeps the rows unrouted while the proxy has `ANTHROPIC_API_KEY`). The docs say to upgrade every session machine before turning the mode on.
    - *Old `wt-model` binaries:* an old `litellm set` drops `[gateway]` (decision 1). An old `serve` or sync meeting a `claude-subscription` model has no Policy for it and treats it as unmapped: its `config.yaml` rewrite drops the row and restarts the live proxy, and the next new sync adds it back with another restart. **Every wt-model binary that can sync must be upgraded before the first subscription model is added** (docs; Task 9 checks `pgrep -fl wt-model` first).
    - *Install order:* the docs say `make install` and a serve restart before adding the provider.
11. **Window rule unchanged.** Subscription rows carry their registry `context_window` like any model, so `claudeSlotWindow`'s minimum rule still computes one `CLAUDE_CODE_MAX_CONTEXT_TOKENS` for a mixed profile. Claude Code's model-config page (2026-11-22) says the variable takes effect for an id it *recognizes* (the four subscription ids, also as a substring such as `anthropic/claude-opus-5-5`) only when `DISABLE_COMPACT` is also set, and it honors it alone for an unrecognized id (`alias/...`, local ids). wt never sets `DISABLE_COMPACT`, so a 32K local haiku slot does not cap an opus subscription slot; the subscription slots get Claude Code's own 1M window (auto-compact near 967K), and the declaration still governs the alias and local slots as phase 2 shipped. Task 9 step 7 confirms it with `/context`. *If wrong:* skipping the declaration when the default slot is a subscription model is one condition in `claudeSlotWindow`; a gateway that enforces less than 1M is handled with `CLAUDE_CODE_AUTO_COMPACT_WINDOW` in the profile env (documented).
12. **Session-side opt-in.** Whether a launch sends the user's claude.ai login is otherwise decided by data the server controls (a row's `ProviderID`, alias targets, a phase-2 hot re-point). A remote operator could re-point an alias the user's profile already names to a subscription target. So the session machine must opt in with `[model-server] subscription = true` in `~/.config/agent-wt/config.toml`, hand-edited like the host's key; the Mode tab's writer round-trips it and never sets it. Without it, `subscriptionGuard` refuses header mode: `wt: <model> uses your Claude subscription, but this machine has not opted in to sending its Claude login to a model server: set [model-server] subscription = true in ~/.config/agent-wt/config.toml`. When the endpoint is not loopback, the guard prints one `wt: note:` line naming the host the login will be sent to. *If wrong:* the key can move to a command later.
13. **Native TLS on serve: `[gateway] tls_cert` and `tls_key` in the wt-model home `config.toml`, read once at start.** Same table and reason as decision 1: the plist renderer drops serve flags, so there is no `--tls-cert` flag. Values are paths to PEM files; a leading `~` is expanded like `token_file`. Absent means plain http, unchanged.
   - *Fail closed, not off:* unlike `subscription_passthrough`, a wrong-typed value or one key without the other **refuses start** (`wt-model: [gateway] tls_cert must be a string`, `wt-model: [gateway] tls_cert and tls_key must be set together`), because silently serving plain http after the operator asked for TLS is the unsafe direction. The same keys in the wrong file (agent-wt `config.toml`) get decision 1's notice only, since serve never reads that file.
   - *Loading:* `internal/tlsfile.Load` stats the key (following links, like `tokenfile`), refuses a non-regular file or `perm&0o077 != 0` with tokenfile's wording (`TLS key <path> is accessible by group or other (mode 0644); run chmod 600 <path>`), reads both, and checks PEM, that the pair matches, and the leaf's validity. The certificate file may be 0644, so it must hold no key: a `*PRIVATE KEY` block in it is `KindKeyInCert`, and `tls_cert` equal to `tls_key` after `~` expansion and `filepath.Clean` is `KindSameFile` (a combined PEM would otherwise be recorded in `serve.json`, printed as "the certificate" and copied to session machines as `ca_file`). Every failure is one line, exit 1, before listening and before `serve.json` is written: `wt-model: [gateway] tls_cert/tls_key: <fixed reason> (<path>)`. The reason is a fixed string per `tlsfile.Kind`, never the parser's text, never file bytes.
   - *Validity:* an already-expired or not-yet-valid leaf refuses (owner confirmation 3); one expiring within 14 days is a `wt-model: warning:` line.
   - *Protocol:* `tls.Config{MinVersion: tls.VersionTLS12, CipherSuites: <the six ECDHE AES-GCM/ChaCha20-Poly1305 suites>}` set explicitly (the list affects TLS 1.2 only; Go's default 1.2 list still includes ECDHE-CBC-SHA; 1.3 is not forced, because re-encrypting terminators and corporate proxies may still negotiate 1.2). HTTP/2 stays on (default ALPN): `ReverseProxy` with `FlushInterval -1` streams SSE over h2 and a client abort still cancels the request context; if the live check finds an agent that misbehaves, an empty `TLSNextProto` map forces 1.1. Only when `Options.TLS` is set does `http.Server` gain `IdleTimeout: 120s` and `ErrorLog` (handshake errors carry the client IP only); plain-http serve keeps today's `http.Server{Handler, ReadHeaderTimeout: 10s}` exactly. Still no `ReadTimeout`/`WriteTimeout`, since prompts and streams are long. `Server.Serve` calls `srv.ServeTLS(ln, "", "")` when `Options.TLS` is set; announce, loop and shutdown order are untouched.
   - *Report:* the announce line is unchanged. A bare line (M4) follows it, `wt-model: TLS is on (tls_cert <path>, TLS 1.2+; clients that do not trust it need ca_file or a CA the system trusts)`, then the expiry warning, then a warning when the certificate names neither `127.0.0.1` nor `localhost` (local sessions dial loopback). One pure function, `tlsLines`.
   - *Host commands:* `servestate.Sessions` (used by `wt-model stop` and `wt-model alias`) dials `http://<listen>` from `serve.json` today (`internal/servestate/sessions.go`, `dialURL`), so TLS would break it. `Record.TLSCert` (Global Constraints) tells it to dial `https://`, trusting exactly that certificate file, with `ServerName` set to `tlsfile.DialName(leaf)`: `127.0.0.1` or `localhost` when the certificate names one, else its first DNS SAN, else its first IP SAN, because the dial goes to loopback.
   - *Setup and the Mode tab:* `wt-model setup` still renders the same four `ProgramArguments`, so TLS survives a re-render. When the home's `[gateway]` has `tls_cert`, three places that build a local url from a hard-coded `http://` use `https://` instead: setup's local plan (`modelsetup/plan.go`, `want.URL`), the Mode tab's local plan (`modelmode/plan.go`, both `"http://" + DefaultListen` sites) and `modelmode.Observe`'s derived local url (`observe.go`). Each reads the home through `config.GatewaySettings`, and dials `127.0.0.1` when the listen host is unspecified (`0.0.0.0`, `::`), since no certificate names `0.0.0.0`. Setup's wait step (`waitReady`) pings with `modelping.Endpoint{CAFile: tls_cert, ServerName: tlsfile.DialName(leaf)}`, the trust `servestate` gets, so a private certificate does not end setup in a `WaitError`. Setup never writes `tls_*` or `ca_file`: when the local session's `ca_file` (and `WT_MODEL_CA_FILE`) is unset and TLS is on, the plan carries one note step naming the exact line to add (`ca_file = "<tls_cert path>"` under `[model-server]` in `~/.config/agent-wt/config.toml`), the Mode tab's local plan the same note; until it is added, local launches fail at once with decision 14's `ca_file` hint, never in clear. *If wrong:* setup writing `ca_file` for local mode only is one step and an exception to the Global Constraints rule.
   - *Certificates:* creating one is documented (openssl with a subjectAltName, mkcert, `tailscale cert`); there is no generator in code. A file added as a trust anchor can mint certificates for any name (api.anthropic.com included) in every process that trusts it, so the recipe leads with a leaf that is not a CA (`basicConstraints=critical,CA:FALSE`; Go and Node accept a directly trusted self-signed leaf, confirmed in Task 9 step 5b), falls back to a CA with `nameConstraints` limited to the SANs, and says plainly that mkcert's root is unconstrained and trusts every name. Rotation is "replace both files, restart the LaunchAgent"; there is no hot reload.

   *Cost:* rotation cuts in-flight streams, as the passthrough switch does; a certificate that expires after start keeps serving until the next restart, which then refuses. *If wrong:* hot reload is `GetCertificate` over the same loader; warning instead of refusing on expiry is one `Kind`'s handling.
14. **Client trust: one mechanism, a CA file.** `[model-server] ca_file` in agent-wt `config.toml` (hand-edited; the Mode tab round-trips it and never sets it) or `WT_MODEL_CA_FILE` (env wins), a PEM file of extra trusted roots added to the system pool. A self-signed server certificate is its own CA, so the operator copies `server.crt` to the session machine. A file, not a directory. `ResolveEndpoint` expands `~`, then makes the path absolute (`filepath.Abs`), so the agent, which runs in the worktree, reads the same file. It is public material, so there is no mode check; a file holding no PEM certificate is exit 1 before any request (`wt: ca_file <path> holds no PEM certificate`), and so is one holding a private-key block (`wt: ca_file <path> holds a private key: copy only the certificate`; no bytes echoed). A certificate in it that is a CA without name constraints gets one `wt: note:` line (it can vouch for any host, Anthropic's included). No fingerprint pinning, no `InsecureSkipVerify`, no flag or env that skips verification. A pin is rejected because the agents dial the gateway with their own TLS stacks (claude and pi are Node, codex is Rust): a Go-side pin would protect only wt's control calls, while a PEM file serves both legs.
   - *Agent leg:* a claude or pi launch whose gateway origin is `https://` with `ca_file` set gets `NODE_EXTRA_CA_CERTS=<ca_file>` in its env when the inherited env lacks it (a path, not a secret; additive to Node's roots). When the user's own value names another file, wt leaves it (Node takes one file; no merging): in token mode with one `wt: note:` line; in header mode it refuses, exit 1, unless that file holds every certificate in `ca_file` (`wt: NODE_EXTRA_CA_CERTS names <path>, which does not hold the ca_file certificate: unset it or add the certificate to it`), since the agent's first gateway request would otherwise fail opaquely. When wt sets the variable it also sets `WT_NODE_EXTRA_CA_CERTS=<same path>` (a path, not a secret). A launch that does not reach an `https://` gateway (native launches, an `http://` origin) removes an inherited `NODE_EXTRA_CA_CERTS` whose value equals an inherited `WT_NODE_EXTRA_CA_CERTS`, and the marker with it, as `stripWtHeader` handles the header line (the native branch has no `ca_file` in hand, so the marker, not the config, says the value is wt's; a user's own value has no marker and is left alone), so a nested `wt -M claude/opus` does not keep trusting the gateway's certificate on its way to Anthropic. codex, opencode and copilot get nothing set; the docs name the system trust store and mark their per-agent variable unverified until the live check records it.
   - *Failures:* `modelping.Classify` maps each to one hint, exit 1. For the certificate failures (unknown authority, hostname, expiry) and `https` to a plain server, the token is never written, because the handshake fails first. `http` to a TLS server is different: the client has already written the whole request, `Authorization: Bearer <wt token>` included, in clear before Go's 400 comes back, so that hint says so (`the model server speaks https: use https://<addr> in [model-server] url; this request already sent the token unencrypted to <addr>`, plus `rotate the token on the host (wt-model setup)` when the host is not loopback). Unknown authority: `the model server's certificate is not trusted: set ca_file under [model-server] in ~/.config/agent-wt/config.toml (or WT_MODEL_CA_FILE) to the server's certificate`. `x509.HostnameError`: `the certificate does not name <host>: regenerate it with a subjectAltName for that host (an IP needs an IP SAN)`. Expired or not yet valid: says so with the date. `https` to a plain-http server: a hint naming the scheme. `ca_file` with an `http://` url is one `wt: note: ca_file is ignored for an http url`. TLS 1.2 is the client floor too.

   *If wrong:* a pin can be added beside `ca_file`; a directory form is one `os.ReadDir`.

## Not in this plan

- TLS extras: certificate hot reload, client certificates (mTLS) or per-client identity, fingerprint pinning, a certificate generator in code, and a command that writes `[gateway] tls_cert`/`tls_key` or `[model-server] ca_file` (hand-edited, like the passthrough switches).
- Setting a CA variable for codex, opencode or copilot launches (only the Node agents claude and pi get `NODE_EXTRA_CA_CERTS`; decision 14).
- Stripping a client `x-litellm-api-key` (or `x-wt-model-token`) in **legacy** mode. It is an existing hole: an authenticated client can pick another LiteLLM key. The owner's byte-for-byte rule keeps it out of this phase. Follow-up #692.
- A command or setup-tab toggle for `[gateway] subscription_passthrough` or `[model-server] subscription`: follow-up #693 (decisions 1, 12).
- Cloud model rows as LiteLLM database rows (no restart when a subscription model is added): follow-up #694.
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
  - `func peerTrusted(r *http.Request) bool`: false when any proto indicator (every `X-Forwarded-Proto` value, every `Forwarded` `proto=` element) is not `https`; else true when `r.TLS != nil` (client-address headers not read), or when `RemoteAddr` parses (`net.SplitHostPort`, `netip.ParseAddr`, `Unmap`) to a loopback address and no element of any `X-Forwarded-For`/`X-Real-IP` value or `Forwarded` `for=` element names a non-loopback client, unless at least one proto indicator is present and all say `https` (decision 3). Unparseable or empty `RemoteAddr` means false.
  - `type authKind int` with an unexported context key; `authenticated(c Config, next)` replaces `authenticated(token, next)`.

- [ ] **Step 1: Write failing tests** (sentinels per Global Constraints; the upstream is an `httptest` server recording headers with an atomic call counter; non-loopback cases call `New(cfg).ServeHTTP` directly with `req.RemoteAddr` set):
  - `TestSubscriptionHeaderForwardsTheLogin`: mode on, peer 127.0.0.1. The upstream sees `Authorization: Bearer <login>` byte-equal, `anthropic-beta` with `oauth-2025-04-20` untouched, `x-litellm-api-key == testKey`, and no `x-wt-model-token` and no `x-api-key`. It covers `/v1/messages`, `/v1/messages/count_tokens` and `/v1/models`.
  - `TestSubscriptionHeaderWithoutAuthorizationAddsNone`: the upstream has no `Authorization`.
  - `TestSubscriptionNonOAuthAuthorizationRefused`: `Authorization` = the wt token gives 401; an `sk-ant-api…` key and a random string give 403 `subscription_no_login`; each with 0 upstream calls.
  - `TestSubscriptionWrongOrDuplicateHeaderForwardsNothing`: wrong, empty and two-valued header, each with and without a valid wt Bearer beside it, give 401 with the existing body and 0 upstream calls.
  - `TestSubscriptionModeOffForwardsNothing`: valid header plus login, mode off, gives 403 `subscription_off` and 0 calls. A wrong header with the mode off is a 401 (no mode disclosure).
  - `TestSubscriptionRefusedFromNonLoopbackOverHTTP`: `192.0.2.7:1`, `10.0.0.2:1`, `[2001:db8::1]:1`, `garbage` and an empty addr give 403 `subscription_insecure` and 0 calls. Rows from `192.0.2.7` that send `X-Forwarded-For: 127.0.0.1`, `X-Forwarded-Proto: https` or `Forwarded: proto=https;for=127.0.0.1` are still refused. A loopback peer with `X-Forwarded-For: 192.0.2.7` is refused; the same plus `X-Forwarded-Proto: https` is accepted. Also refused from a loopback peer: `X-Forwarded-For: 127.0.0.1, 192.0.2.7`; `X-Forwarded-For: 192.0.2.7` with `X-Forwarded-Proto: http` plus `Forwarded: proto=https`; `Forwarded: for=127.0.0.1, for=192.0.2.7;proto=https` with `X-Forwarded-Proto: http`. Refused from a TLS peer: `X-Forwarded-Proto: http`.
  - `TestSubscriptionAcceptedFromLoopbackOrTLS`: `127.0.0.1`, `127.9.9.9`, `[::1]`, `[::ffff:127.0.0.1]` and `192.0.2.7` with `req.TLS = &tls.ConnectionState{}` give 200; so does `192.0.2.7` with TLS and `X-Forwarded-For: 192.0.2.8` (a direct TLS peer is the real peer; decision 3), and a loopback peer with `X-Forwarded-For: 127.0.0.1, 127.0.0.2`.
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
  - `func subscriptionLines(listenAddr string, on, tlsOn bool) []string` (in `cmd/wt-model`; serve passes `tlsOn = false` until Task 2b)

- [ ] **Step 1: Write failing tests**:
  - `TestGatewayTableRoundTripsThroughSetProxy`: `SetProxy` keeps `[gateway] subscription_passthrough = true`, and keeps a wrong-typed value as written.
  - `TestGatewaySettingsAbsentIsOff`.
  - `TestGatewaySettingsWrongTypeIsOffWithWarning`: `"true"` and `1` give off and one warning naming the key; `[litellm]` still decodes.
  - `TestWrongFileGatewayKeyPointsAtTheHome`: a `[gateway] subscription_passthrough` in agent-wt's `config.toml` gives the notice naming `~/.config/wt-model/config.toml`, not the legacy wording.
  - `TestPingAdvertisesSubscriptionPassthrough`: true only with the option; the raw JSON has no key when it is off.
  - `TestOldClientDecodesNewPing`: `modelping.CheckPing` on the new body succeeds.
  - `TestGatewayHeaderModeThroughServer`: full `Handler`; control calls keep Bearer in both modes, and control refuses `x-wt-model-token` alone with 401.
  - `TestSubscriptionLines`: a table over `0.0.0.0:8787`, `[::]:8787`, `192.0.2.7:8787` (info + warning naming `tls_cert`/`tls_key`), the same three with `tlsOn` (info only), `127.0.0.1:0` (info only), and off (none).
  - `TestServeSubscriptionOffByDefault`: no subscription line.
  - `TestServeSubscriptionLineAfterAnnounce`: loopback listen; the announce line is byte-identical and first; the subscription line follows; with a legacy-fallback pair also seeded, `TestServeNotesTheLegacyFallback`'s one-note rule still holds.
  - `TestServeNeverPrintsForwardedSecrets`: the upstream is an `httptest.NewServer` whose URL goes to `seedProxyHome(t, up.URL, …)`, and the test asserts the upstream saw the request. Sends both sentinels through the running serve's gateway in header mode: a success, a 403, and an upstream that aborts mid-body (so a proxy log line is written to serve's stderr and scanned). `readStderr`/`readStdout` lack the sentinels, and so does the scratch home's `serve.json`.
  - `TestSetupNeverEnablesPassthrough` (`internal/modelsetup`): a setup plan applied on a scratch home leaves `[gateway]` absent or unchanged.
- [ ] **Step 2: Run** `cd wt && go test ./internal/config ./internal/modelserver ./internal/modelping ./internal/modelsetup ./cmd/wt-model -run 'Gateway|Ping|Subscription|SetupNever|WrongFile|ProxyHomeFixture' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** decision 1 and decision 2's serve half. Serve reads the setting once, after `loadServeConfig` and before listening; a syntax error fails start, worded without values (`secretSafe`). (Decision 5's hazard check at serve start lands in Task 3.)
- [ ] **Step 4: Run** `cd wt && go test ./internal/config ./internal/modelserver ./internal/modelapi ./internal/modelping ./internal/modelsetup ./cmd/wt-model`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt-model): [gateway] subscription_passthrough host switch, advertised by ping (#597)`.

### Task 2b: Native TLS on serve

**Files:**
- Create: `wt/internal/tlsfile/tlsfile.go`, `wt/internal/tlsfile/tlsfile_test.go`, `wt/internal/tlstest/tlstest.go` (test helper package, like `internal/gittest`)
- Modify: `wt/internal/config/proxyhome.go` (`GatewayConf.TLSCert`/`TLSKey`; `GatewaySettings` returns an error, not a warning, for these two keys), `wt/internal/config/migrate.go` (`dropLegacyGateway` names `tls_cert`/`tls_key` too), `wt/internal/modelserver/server.go` (`Options.TLS`; `ServeTLS`; `IdleTimeout` and `srv.ErrorLog = o.ErrorLog` only when `Options.TLS` is set), `wt/internal/modelping/endpoint.go` (`Endpoint.CAFile`, `Endpoint.ServerName`), `wt/internal/modelping/ping.go` (`NewHTTPClientFor`; `Ping`/`GetJSON` use it), `wt/internal/servestate/record.go` (`TLSCert`), `wt/internal/servestate/sessions.go` (dial `https://` when set; the `pidAlive` package var becomes a per-call argument of an unexported `sessions(ctx, r, alive func(int) bool)` behind `Sessions`, per `wt/CLAUDE.md`, and `sessions_test.go` stops swapping it), `wt/cmd/wt-model/serve.go` (load after `GatewaySettings` and `LoadToken`, refuse before listening, `tlsLines`, `Record.TLSCert`, `subscriptionLines(…, tlsOn)`, `Long` help: the TLS paragraph and its exit-1 reasons), `wt/internal/modelsetup/plan.go` (local url `https://` when `tls_cert` is set, `127.0.0.1` for an unspecified listen host; the `ca_file` note step), `wt/internal/modelsetup/apply.go` (`waitReady` sets `CAFile`/`ServerName` from the home's `tls_cert`), `wt/internal/modelmode/plan.go` and `wt/internal/modelmode/observe.go` (the three `"http://" + …` local-url sites follow the same rule; the Mode tab's `ca_file` note), `wt/internal/depcheck/depcheck_test.go` (`internal/tlsfile`, `internal/tlstest` in `otherPackages`), `wt/docs/wt-model-server.md` (replace "The server speaks plain HTTP and has no TLS…" with the decision 3 wording; a "Native TLS" section: keys, both-or-neither, 0600, refusal reasons, TLS 1.2+, h2, rotation, the `serve.json` `tls_cert` row)
- Unchanged: `docs/contracts/model-api.sample.json`, `docs/contracts/wt-model-config.sample.toml` (Global Constraints)
- Test: `wt/internal/config/proxyhome_test.go`, `wt/internal/config/migrate_test.go`, `wt/internal/modelserver/server_test.go`, `wt/internal/modelping/ping_test.go`, `wt/internal/servestate/sessions_test.go`, `wt/internal/modelsetup` plan and apply tests, `wt/internal/modelmode` plan and observe tests, `wt/cmd/wt-model/serve_test.go`

**Interfaces:**
- Consumes: Task 1's `peerTrusted` TLS branch; Task 2's `GatewaySettings`, `Options.ErrorLog`, `subscriptionLines`.
- Produces:
  - `type GatewayConf struct { SubscriptionPassthrough bool; TLSCert, TLSKey string }` (`~` expanded; both or neither, else `GatewaySettings` returns an error)
  - `func tlsfile.Load(certPath, keyPath string, now time.Time) (*tls.Certificate, *x509.Certificate, error)`: the pair and its parsed leaf
  - `type tlsfile.Error struct { Path string; Kind Kind; Err error }` with `KindStat`, `KindRead`, `KindNotRegular`, `KindLoose`, `KindBadPEM`, `KindKeyInCert`, `KindSameFile`, `KindMismatch`, `KindExpired`, `KindNotYetValid`; `Error()` is a fixed reason per `Kind` plus the path (plus the OS reason for stat/read)
  - `func tlsfile.HasPrivateKey(pemBytes []byte) bool` (scans PEM block types for `PRIVATE KEY`; reused by Task 6b's `ca_file` check)
  - `func tlsfile.Config(c *tls.Certificate) *tls.Config` (`MinVersion: tls.VersionTLS12`, `CipherSuites` = the six ECDHE AES-GCM/ChaCha20-Poly1305 suites)
  - `func tlsfile.DialName(leaf *x509.Certificate) string`
  - `modelserver.Options.TLS *tls.Config` (nil = plain http)
  - `modelping.Endpoint.CAFile, ServerName string`; `func modelping.NewHTTPClientFor(ep Endpoint) (*http.Client, error)`: `NewHTTPClient`'s transport plus, when `CAFile` is set, `RootCAs` = system pool + the file and `ServerName`; `MinVersion` 1.2; `NewHTTPClient()` stays and equals the no-CA case
  - `servestate.Record.TLSCert string` (`json:"tls_cert,omitempty"`)
  - `func tlsLines(certPath string, leaf *x509.Certificate, now time.Time) []string` (in `cmd/wt-model`)
  - `func modelmode.LocalURL(listen string, tlsOn bool) string`: `http://<listen>` as today when `tlsOn` is false; `https://<listen>` with an unspecified host replaced by `127.0.0.1` when true. Setup's local plan, the Mode tab's local plan and `Observe` all call it, with `tlsOn` from `config.GatewaySettings` (a read error there is a refusal reason, never a silent `http://`)
  - `func tlstest.Gen(t testing.TB, dir string, o tlstest.Opts) (certPath, keyPath string)`; `type Opts struct { DNS []string; IPs []net.IP; NotBefore, NotAfter time.Time; KeyMode os.FileMode }` (zero values: `127.0.0.1` + `localhost`, valid from an hour ago for a day, 0600); ECDSA P-256 in memory, files written only under `dir`

- [ ] **Step 1: Write failing tests**:
  - `TestLoadAcceptsA0600Key`.
  - `TestLoadRefusesALooseKey`: 0644 and 0640 give `KindLoose`; the message names `chmod 600` and the path.
  - `TestLoadRefusesBadFiles`: a missing file, a directory as key, a non-PEM file, a 0644 cert file that also holds the key, the same combined file as both `tls_cert` and `tls_key` (also via a `~`/`./` spelling), and a cert and key from two different pairs give `KindStat`, `KindNotRegular`, `KindBadPEM`, `KindKeyInCert`, `KindSameFile` and `KindMismatch`.
  - `TestLoadRefusesExpiredAndNotYetValid`.
  - `TestErrorsNeverCarryKeyBytes`: for every `Kind` above, `KindKeyInCert` and `KindSameFile` included, `err.Error()` contains no line of the key's or the certificate's PEM body.
  - `TestConfigFloorIsTLS12`: `MinVersion == tls.VersionTLS12`; every entry of `CipherSuites` is AEAD (its `tls.CipherSuiteName` has no `CBC`), and the list is non-empty.
  - `TestDialName`: loopback SAN preferred, then DNS, then IP.
  - `TestGatewaySettingsTLSKeys`: both set (a `~` path expanded); one alone, and a wrong type (`tls_cert = 1`), each an error naming the key and no value; absent means plain.
  - `TestWrongFileGatewayTLSKeysPointAtTheHome`.
  - `TestServeOverTLS` (`modelserver`): `Options.TLS` from `tlstest`, `Listen: "127.0.0.1:0"`, the announce callback gives the port; a client trusting the cert pings 200 over h2 (`resp.ProtoMajor == 2`); plain http to the same port is not a ping.
  - `TestGatewayStreamsOverTLS`: an `httptest` SSE upstream; each event is flushed over h2, and a client abort cancels the upstream request.
  - `TestNewHTTPClientForTrustsTheCAFile` (`modelping`): an `httptest.NewUnstartedServer` + `StartTLS`, its `srv.Certificate()` written as PEM under `t.TempDir()`; `Ping` with `CAFile` succeeds, without it fails; a file with no PEM certificate is an error before any dial.
  - `TestServeRefusesABadTLSConfig` (`cmd/wt-model`): loose key, half pair, wrong type, mismatch, expired. Each is exit 1 with one line naming the path; no announce line, no `serve.json`; stderr holds no PEM body line.
  - `TestServeTLSAnnounceUnchanged`: the announce line is byte-identical and first; the TLS line follows, then the subscription line (no remote warning with TLS); `serve.json` has `tls_cert` and no key path.
  - `TestTLSLines`: plain (none), on (one), expiring in 10 days (+ warning), a certificate naming neither `127.0.0.1` nor `localhost` (+ warning).
  - `TestPlainServeUnchanged`: no TLS keys; the existing serve tests pass unmodified, `serve.json` has no `tls_cert` key, and the `http.Server` has neither `IdleTimeout` nor `ErrorLog` set (only `Handler` and `ReadHeaderTimeout`, as today).
  - `TestSessionsDialHTTPSWhenRecorded` (`servestate`): a TLS `httptest` server with a `tlstest` cert; a record with `TLSCert` reads the sessions (liveness through the per-call `alive` argument, no package var); a record without it against the same server is an error, never an empty map.
  - `TestSetupWritesHTTPSWhenTLSConfigured` and `TestSetupNeverWritesTLSKeys` (`modelsetup`): `https://127.0.0.1:<port>` for a `0.0.0.0` listen; neither `tls_*` nor `ca_file` is written; with `ca_file` unset the plan has the note step naming the line to add.
  - `TestSetupWaitTrustsTheTLSCert` (`modelsetup`): a scratch home whose `[gateway]` names a `tlstest` pair, the apply's wait step against a TLS `httptest` server using that pair succeeds (no `WaitError`); the same wait with `tls_cert` pointing at another certificate fails with an x509 error (Task 6b adds the hint).
  - `TestModeLocalWritesHTTPSWhenTLSConfigured` (`modelmode`): with the home's `[gateway]` naming `tls_cert`, the Mode tab's local plan (stored-local and fresh) and `Observe`'s derived url are `https://`, never `http://`; with no `[gateway]`, both stay `http://` as today.
  - `TestLocalURL`: the table for `modelmode.LocalURL` (`127.0.0.1:8787`, `0.0.0.0:8787`, `[::]:8787`, TLS on and off).
- [ ] **Step 2: Run** `cd wt && go test ./internal/tlsfile ./internal/config ./internal/modelserver ./internal/modelping ./internal/servestate ./internal/modelsetup ./internal/modelmode ./cmd/wt-model -run 'Load|ErrorsNever|ConfigFloor|DialName|GatewaySettingsTLS|WrongFileGatewayTLS|OverTLS|NewHTTPClientFor|BadTLS|TLSAnnounce|TLSLines|PlainServe|SessionsDialHTTPS|SetupWritesHTTPS|SetupNeverWritesTLS|SetupWait|ModeLocal|LocalURL' -v`. Expected: FAIL (undefined `tlsfile`, `Options.TLS`, `NewHTTPClientFor`).
- [ ] **Step 3: Implement** decision 13 and the reference section of `wt/docs/wt-model-server.md` in the same commit.
- [ ] **Step 4: Run** `cd wt && go test ./internal/tlsfile ./internal/tlstest ./internal/config ./internal/modelserver ./internal/modelgateway ./internal/modelping ./internal/servestate ./internal/modelsetup ./internal/modelmode ./internal/depcheck ./cmd/wt-model` and `go test -race ./internal/modelserver ./internal/modelgateway`. Expected: PASS. Then, still in `wt/`, `grep -rn InsecureSkipVerify . --include='*.go' | grep -v _test.go` prints nothing.
- [ ] **Step 5: Commit** `feat(wt-model): native TLS on serve from [gateway] tls_cert/tls_key, plain http by default (#597)`.

### Task 3: The `claude-subscription` provider, its registry rows and keyless LiteLLM rows

**Files:**
- Modify: `wt/internal/provider/table.go` (the `Spec`, `IDClaudeSubscription`), `wt/internal/provider/provider.go` (`SeedRow` omits an empty `base_url`), `wt/internal/config/config.go` (`SubscriptionProvider`, `SubscriptionAgents`, `func IsSubscriptionProvider(id string) bool`), `wt/internal/modeladmin/apply.go` (`checkID`), `wt/internal/modeladmin/fields.go` (`DeriveID`; the `--context-window` requirement), `wt/internal/modeleditor/models_form.go` (`formProviders` leaves the provider out), `wt/internal/litellm/service.go` (`prepareModel` refusals; the unrouted-with-warning rule), `wt/internal/litellm/subscription_hazards.go` (new), `wt/cmd/wt-model/serve.go` (the hazard check when the mode is on, synchronous before `NewServer`, 5 s bound), `wt/cmd/wt-model/host.go` (`host.ProxyEnv func() litellm.ProxyEnv`, `host.ModelInfo litellm.ModelInfoFunc`; `testHost` in `helpers_test.go` sets fakes), `docs/contracts/registry.sample.toml` (the provider row and one model, `claude-haiku-5-5`, with `context_window = 1000000`, the published value; owner confirmation 1)
- Test: `wt/internal/provider/provider_test.go` (`wantPolicies`, `SeedableCloudIDs` becomes `["claude-subscription", "openrouter"]` in the table's order), `wt/internal/litellm/policy_table_test.go`, `wt/internal/litellm/entry_test.go`, `wt/internal/modeladmin` id tests, `wt/internal/modeleditor` form test, `wt/cmd/wt-model/model_write_test.go`, `wt/cmd/wt-model/serve_test.go`, `wt/internal/config/registry_fixture_test.go`, `llmbench/tests/test_registry.py` (only if it reads `registry.sample.toml`'s lists), and the two existing tests that sync `registry.sample.toml` (`TestLitellmSyncWorksOnValidationOnlyError` in `cmd/wt-model/litellm_sync_test.go`, `TestLitellmStateCommandsFailOnAnUnreadableProxyConfig` in `cmd/wt-session/litellm_proxy_test.go`): once the sample has a subscription row they set `WT_LITELLM_PLIST` to a scratch file, so a plain `go test` outside pre-pr's scratch HOME never reads the real plist

**Interfaces:**
- Consumes: `provider.Policy`, `BuildEntry`, `prepareModel`, `litellm.ProxyEnv`.
- Produces: the decision 5 representation. `wt-model model add claude-subscription claude-opus-5-5 --family <f> --context-window N` writes the id `claude-opus-5-5`, and without `--context-window` it refuses: `a claude-subscription model needs --context-window (Claude Code's window for it)`. `func litellm.SubscriptionHazards(ctx context.Context, cfgPath string, env ProxyEnv, info ModelInfoFunc) (hazards, warnings []string, err error)`, worded without values; `err` is non-nil only when `/model/info` could not be read (the sync then leaves the rows unrouted with one warning, serve warns and keeps the mode on; decision 5); `type ModelInfoFunc func(ctx context.Context) ([]DeploymentInfo, error)` is new (provider, `model_name`, `api_base` per `/model/info` row; the sync passes its existing proxy reader and its `ProxyEnv` per call, never through `loadProxyEnv`; tests a fake). The `host` fields above are the serve seams.

- [ ] **Step 1: Write failing tests**:
  - `TestSubscriptionRowIsKeylessAnthropic`: `BuildEntry` gives `model: anthropic/claude-haiku-5-5`, `model_name: claude-haiku-5-5`, and no `api_key`/`api_base`; pricing is explicit zero.
  - `TestOnlySubscriptionProviderBuildsAnthropicRows`: over every provider in the table, only `claude-subscription` has an `anthropic/` prefix, and its Policy has no `SecretRef`/`APIKey`/`V1Base`.
  - `TestSubscriptionProviderWithBaseURLOrSecretRefused`: `prepareModel` errors for `base_url`, `secret_ref` and `auth.type = native` on the provider row; sync leaves the row unrouted with one warning; an alias over it freezes per phase 2 decision 3.
  - `TestSubscriptionRowsUnroutedWhileProxyHasAnthropicKey`: injected `ProxyEnv`; the row is unrouted, the warning names the variable, never a value.
  - `TestSubscriptionHazards`: a hand-written (no `wt_managed`) `anthropic/` row with a foreign `api_base`, an `anthropic/*` wildcard, and `forward_client_headers_to_llm_api: true` each give a hazard and an unrouted row; an `anthropic/` row with `api_base: https://api.anthropic.com` gives none; `store_prompts_in_spend_logs: true` gives a warning only.
  - `TestServeKeepsModeOffOnHazard`: mode on, `testHost`'s `ModelInfo` fake returns a foreign `anthropic/` row; serve prints one warning, the ping reports off. A `ModelInfo` that blocks past the bound gives a warning and the mode stays on; serve still listens.
  - `TestSubscriptionRowsUnroutedWhenModelInfoUnreadable`: the sync's `ModelInfoFunc` errors; the row is unrouted with one warning.
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
- Modify: `wt/internal/config/config.go` (`Model.Subscription bool` with `toml:"-"`, session-side like `GatewayID`; `Route.Subscription bool`), `wt/internal/catalog/wire.go` (`ToModel` sets it from `UsesSubscription`), `wt/internal/agents/gateway.go` (`GatewayRoute` copies it), `wt/internal/agents/claude.go` (`Build`, native branch included), `wt/internal/agents/claude_slots.go` (`BuildSlots`; the header-mode refusal of `ANTHROPIC_CUSTOM_HEADERS`/`CLAUDE_CODE_OAUTH_TOKEN` in the settings env), `wt/internal/agents/slots.go` (`slotLaunchCmd`; `BuildSlotCommand` derives `Subscription` from `sl.Models`), `wt/internal/agents/agents.go` (`command`; `LaunchCmd.HeaderToken`), `wt/internal/profiles/slot.go` (`SlotBuildInput.Subscription`, `SlotCommand.HeaderToken`), `wt/internal/profiles/slot_validate.go` (the header-value refusal), `wt/cmd/wt-session/profile_slots.go` (`writeSlotProfileShow` prints `header: x-wt-model-token: <config.MaskKey(token)>` when `HeaderToken` is set)
- Create: `wt/internal/agents/custom_headers.go`, `wt/internal/agents/claude_subscription_test.go`
- Test: `wt/internal/agents/claude_slots_test.go` (`wantKeys` unchanged), `wt/internal/profiles/slot_validate_test.go`, `wt/cmd/wt-session/profile_slots_test.go`

**Interfaces:**
- Consumes: Task 4's `UsesSubscription`/`ToModel`.
- Produces:
  - `func mergeCustomHeaders(inherited, token string) string`: header mode (`token != ""`) drops `x-wt-model-token:` and `authorization:` lines (case-insensitive, trimmed) and appends `x-wt-model-token: <token>`.
  - `func stripWtHeader(inherited string) string`: every other launch; drops only `x-wt-model-token:` lines. An empty result from either means the variable goes to `ClearEnv`.
  - `LaunchCmd.HeaderToken string` and `SlotCommand.HeaderToken string`. They are set only in header mode; the env assembler (`command`, `slotLaunchCmd`) turns them into the merged variable, and applies `stripWtHeader` on every other launch for every agent.
  - `SlotBuildInput.Subscription bool`: true when any filled slot model `Subscription`; both `BuildSlots` callers (the launch path and `BuildSlotCommand`, which `wt profile show` uses) derive it from `sl.Models`.
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
  - `TestSlotProfileHeaderValueRefused`: profile `env` and `config_content.env` setting `ANTHROPIC_CUSTOM_HEADERS` with an `x-wt-model-token` or `authorization` line are refused statically; with `x-litellm-tags: a` alone, accepted statically and in token mode, and refused by `BuildSlots` in header mode (it would land in `--settings` and replace the merged header). `CLAUDE_CODE_OAUTH_TOKEN` in either table: allowed in token mode, refused in header mode. In every accepted header-mode build, the decoded `--settings` env has neither `ANTHROPIC_CUSTOM_HEADERS` nor `CLAUDE_CODE_OAUTH_TOKEN`.
  - `TestProfileShowMasksTheHeaderToken`: `wt profile show` of a profile with a subscription slot prints `header: x-wt-model-token: ` followed by `config.MaskKey(token)`, and the full token appears nowhere.
  - `TestHeaderTokenRejectsControlChars`.
- [ ] **Step 2: Run** `cd wt && go test ./internal/agents ./internal/profiles ./internal/catalog ./cmd/wt-session -run 'Subscription|CustomHeaders|TokenMode|WtHeader|HeaderValue|ProfileShow|HeaderToken' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** decision 7.
- [ ] **Step 4: Run** `cd wt && go test ./internal/agents ./internal/profiles ./internal/catalog ./cmd/wt-session`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(agents): claude header mode keeps the login, wt token in ANTHROPIC_CUSTOM_HEADERS (#597)`.

### Task 6: Pre-launch guards in every launch path

**Files:**
- Create: `wt/internal/claudelogin/claudelogin.go`, `wt/internal/claudelogin/claudelogin_test.go`, `wt/cmd/wt-session/subscription_guard.go`, `wt/cmd/wt-session/subscription_guard_test.go`
- Modify: `wt/internal/config/modelserver.go` (`ModelServerState.Subscription bool`, `toml:"subscription,omitempty"`), `wt/internal/modelmode` (the Mode tab's writer round-trips the key), `wt/cmd/wt-session/app.go` (`hooks.claudeLogin`), `wt/cmd/wt-session/main.go` (builds the TUI guard closure and sets it on the `tui.ProfileLayer` it passes to `tui.Run`), `wt/cmd/wt-session/slot_launch.go` (`launchSlotProfileRemote`; the slot applier's launch-profile check), `wt/cmd/wt-session/launch.go` (`applyResolvedProfile`'s launch-profile check), `wt/cmd/wt-session/launch_remote.go`, `wt/internal/tui/launch.go` (`ProfileLayer.Guard SubscriptionGuard`: the guard rides on an existing `Run` argument, so `Run`'s 16 positional parameters and the `tuiRun` stubs in `helpers_test.go`, `flip_test.go`, `cwd_norepo_test.go` and `launch_remote_test.go` do not change), `wt/internal/tui/app.go` (`Run` reads `profileLayer.Guard`), `wt/internal/tui/slot_flow.go` (`beginRemoteSlotLaunch`), `wt/internal/tui/remote_start.go`, `wt/internal/profiles/testdata/slot_launch_remote_parity.txt` (a `guard` token and one subscription scenario; both parsers emit it), `wt/internal/depcheck/depcheck_test.go` (`internal/claudelogin` in `otherPackages`)
- Test: `wt/cmd/wt-session/launch_remote_test.go` (the fake `claude` script answers `auth status --json` and `--version` from env-named files, and dumps its env and argv to env-named files), `wt/cmd/wt-session/slot_launch_remote_test.go` and `wt/internal/tui/slot_flow_remote_test.go` (both `TestSlotFlowParity`)

**Interfaces:**
- Consumes: Task 2's ping field, Task 4's `UsesSubscription`, Task 5's builders.
- Produces:
  - `type Status struct { LoggedIn bool; Method, Provider string; ExpiresAt time.Time; Version string }`
  - `func claudelogin.Check(ctx context.Context, bin string, environ []string) (Status, error)`: scrubs the env per decision 8 and decodes only the four fields.
  - `func (s Status) OK() bool`
  - `const MinVersion = "2.1.227"`
  - `func subscriptionGuard(ctx context.Context, ms *modelSession, rows []modelapi.Row, profileEnvKeys []string, login func(context.Context) (claudelogin.Status, error)) error`: returns nil when no row `UsesSubscription`. Otherwise it checks, in order: opt-in (decision 12), endpoint `ms.ep` through `subscriptionEndpointOK` (decision 3), ping via `ms.client.Ping(ctx)` (decision 2), launch-profile env keys (decision 7), version, login (decision 8).
  - `func subscriptionEndpointOK(ep modelclient.Endpoint) error`: pure (scheme, host parse, `Unmap`, the loopback set, the `localhost`/`localhost.` rule); never dials.
  - `type tui.SubscriptionGuard func(ctx context.Context, rows []modelapi.Row) error`, carried as `tui.ProfileLayer.Guard`: `main.go` closes over `ms`, the resolved launch profile's env keys and `a.hooks.claudeLogin`. The TUI runs it inside a `tea.Cmd` (it execs `claude` with a 10 s timeout, so never in `Update`), before `runRemoteStart` registers the client session and before the slot flow's register/start. A nil guard (tests that build no subscription rows) is a no-op.
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
  - `TestSubscriptionPlainHTTPRemoteRefusedBeforeSending`: a table driven against `subscriptionEndpointOK` directly (no client, no dial), plus one rig row (`WT_MODEL_URL=http://192.0.2.7:1`) showing the guard refuses before any request. Refused: `http://192.0.2.7:8787`, `http://[::ffff:192.0.2.7]:1`, `http://localhost.example.com`, `http://127.1`, `http://0.0.0.0`, and `WT_MODEL_URL=http://192.0.2.7:1` with a loopback `[model-server] url`. Pass: `https://host`, `https://192.0.2.7:8787`, `http://127.0.0.1`, `http://[::1]`, `http://localhost`, `http://localhost.` (the endpoint rule only; certificate trust is Task 6b).
  - `TestSubscriptionOldClaudeRefused`: version 2.1.200.
  - `TestSubscriptionLaunchProfileCannotOverrideHeaderEnv`: a matching launch profile setting each of the three keys is refused, exit 1, before any client session.
  - `TestSubscriptionLaunchChildEnvAndArgv`: the dump has the merged `ANTHROPIC_CUSTOM_HEADERS` once, no `ANTHROPIC_AUTH_TOKEN`, and argv free of the token. A nested case (the parent env already holds a header-mode `ANTHROPIC_CUSTOM_HEADERS`) launching `claude/opus` natively has no `x-wt-model-token` in the child.
  - `TestSubscriptionLaunchWritesNoFileAndNoArgv`: the rig passes the token as `WT_MODEL_TOKEN` with no token file, then walks the scratch worktree and scratch HOME for the token bytes (none expected anywhere).
  - `TestNonSubscriptionLaunchSkipsTheGuard`: the login hook is never called.
  - `TestTUISubscriptionLaunchAfterPickerPing`: the picker fetched the catalog from a server whose ping has `subscription_passthrough: true` (so `openModels` never ran); the launch is not refused.
  - `TestSlotFlowParity` (both): the shared file gains a `guard` token and a subscription scenario. The cmd/wt-session one asserts the guard runs before `openClientSession`; the internal/tui one that it runs before `runRemoteStart`/register.
  - `TestLoginExpiringSoonNotes`.
- [ ] **Step 2: Run** `cd wt && go test ./internal/claudelogin ./cmd/wt-session ./internal/tui ./internal/depcheck -run 'Check|Subscription|SlotFlowParity|LoginExpiring|NilLoginHook|TUISubscription' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** The guard runs after catalog resolve and before `openClientSession`/any start, in all four paths. The `TestSessionPathsNeverTouchTheRegistryOrLifecycle` boundary stays green.
- [ ] **Step 4: Run** `cd wt && go test ./internal/claudelogin ./internal/tui ./internal/profiles ./internal/config ./internal/modelmode ./internal/depcheck ./cmd/wt-session`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): refuse a subscription launch without opt-in, a login, an enabled server or a safe endpoint (#597)`.

### Task 6b: A session trusts a TLS model server through `ca_file`

**Files:**
- Create: `wt/internal/agents/ca_env.go`, `wt/internal/agents/ca_env_test.go`
- Modify: `wt/internal/config/modelserver.go` (`ModelServerState.CAFile`, `toml:"ca_file,omitempty"`), `wt/internal/modelclient/endpoint.go` (`ResolveEndpoint` gains the `caFile` argument; `WT_MODEL_CA_FILE` wins; `~` expanded, then made absolute; the file is validated; the http-url note), `wt/internal/modelclient/client.go` (`New` builds its client with `modelping.NewHTTPClientFor(ep)`; on its error `New` keeps the error and every request returns it, never falling back to `NewHTTPClient()`; `New`'s signature and its callers stay), every `ResolveEndpoint` caller (passes `ModelServerState.CAFile`), `wt/internal/modelping/classify.go` + `errors.go` (the TLS hints), `wt/internal/modelmode` (the Mode tab's writer round-trips `ca_file`; its check ping uses the CA), `wt/internal/config/config.go` (`Route.CAFile`, copied by `GatewayRoute`, so `command` and `slotLaunchCmd` get the CA from the `Route` they already take), `wt/internal/agents/gateway.go` (`Gateway.CAFile`; `GatewayRoute` copies it), `wt/internal/agents/agents.go` (`command`) and `wt/internal/agents/slots.go` (`slotLaunchCmd` gains a `caFile string` parameter after `key`, its callers in `slots.go` updated): the CA env for claude and pi, and the inherited-value strip
- Modify (every place that builds a `Gateway` or an `Endpoint` from a session's endpoint, so `CAFile` is never dropped): `wt/cmd/wt-session/launch_remote.go` (:161), `wt/cmd/wt-session/slot_launch.go` (:288), `wt/cmd/wt-session/smoke.go` (:659), `wt/internal/tui/models_client.go` (:67), all `agents.Gateway{…, CAFile: ms.ep.CAFile}`; `wt/internal/smoke/smoke_wire.go` (`gatewayIs503`'s `modelclient.Endpoint` carries `gw.CAFile`); `wt/cmd/wt-session/app.go` (`loopbackOnlyClient` builds its transport from `modelping.NewHTTPClientFor(ep)`'s, wrapping only `DialContext`, so a test binary honors `ep.CAFile`)
- Test: `wt/internal/modelclient/endpoint_test.go`, `wt/internal/modelclient/client_test.go`, `wt/internal/modelping/classify_test.go`, `wt/internal/modelmode` round-trip test, `wt/internal/smoke/smoke_wire_test.go`, `wt/cmd/wt-session/subscription_guard_test.go`, `wt/cmd/wt-session/launch_remote_test.go`

**Interfaces:**
- Consumes: Task 2b's `modelping.Endpoint.CAFile`, `NewHTTPClientFor`, `tlsfile.HasPrivateKey` and `tlstest.Gen`; Task 5's env assembler (`command`, `slotLaunchCmd`); Task 6's `subscriptionGuard`.
- Produces:
  - `config.ModelServerState.CAFile string`
  - `func modelclient.ResolveEndpoint(url, tokenFile, caFile string, env func(string) string) (Endpoint, error)`: a `ca_file` holding no PEM certificate is an error (`ca_file <path> holds no PEM certificate`)
  - `type modelping.TLSTrustError struct { Addr, Hint string; Err error }`, returned by `Classify` for unknown authority, `x509.HostnameError`, expiry and a scheme mismatch, each with decision 14's hint
  - `agents.Gateway.CAFile string`
  - `config.Route.CAFile string` (copied by `GatewayRoute`; empty for a native route); `slotLaunchCmd(sl, worktreePath, yolo, origin, key, caFile string, extraArgs)`
  - `func nodeCAEnv(agent, origin, caFile string, header bool, environ []string) (set, clear []string, note string, err error)`: for `claude` and `pi` over `https://` with `caFile` set, `set` is `NODE_EXTRA_CA_CERTS=<caFile>` and `WT_NODE_EXTRA_CA_CERTS=<caFile>` when `environ` lacks the variable; when it names another file, a note in token mode, and in header mode an error unless that file holds every certificate in `caFile`. For every launch not reaching an `https://` gateway, `clear` holds both names when the inherited value equals the inherited marker. `caFile` is already absolute (`ResolveEndpoint`).
  - `ResolveEndpoint`'s `ca_file` checks reuse `tlsfile.HasPrivateKey` and note a CA certificate without name constraints

- [ ] **Step 1: Write failing tests**:
  - `TestHTTPSEndpointAccepted`: `https://192.0.2.7:8787` resolves unchanged (no code change expected).
  - `TestCAFileEnvWinsAndExpandsHome`: `~` expanded and a relative path made absolute, so the endpoint's `CAFile` is absolute.
  - `TestCAFileWithoutPEMRefused`: a file with no PEM certificate, and one holding a `PRIVATE KEY` block beside a certificate, each exit 1 before any request; the message holds the path and no file bytes. A CA certificate without name constraints resolves with one note.
  - `TestClientWithBadCAFileNeverDials`: a `CAFile` that went bad after `ResolveEndpoint` makes `New`'s client return an error on `Ping` and on a gateway call, with no dial (a counting listener sees no connection) and no fallback to the system pool.
  - `TestCAFileIgnoredForHTTPNote`.
  - `TestUntrustedServerCertGivesTheCAFileHint`: a `StartTLS` `httptest` server; without `ca_file` the ping fails with the hint, and the server's handler saw no request (the bearer token was never written); with `ca_file` = its certificate it succeeds.
  - `TestHostnameMismatchHint`: a `tlstest` cert naming only `wrong.invalid`; the hint names the host.
  - `TestExpiredServerCertHint`.
  - `TestSchemeMismatchHints`: `https` to a plain server; `http` to a TLS server, whose hint says the token was already sent unencrypted to the address (and, for a non-loopback host, to rotate it on the host).
  - `TestModeTabRoundTripsCAFile`: a Mode tab write keeps a hand-set `ca_file` and never adds one.
  - `TestNodeCAEnv`: a table over claude and pi (set, with the marker), codex (not set), an `http` origin (not set), an inherited equal value (unchanged), an inherited other value in token mode (unchanged + note), in header mode (error) and in header mode when that file also holds the `ca_file` certificate (unchanged); a native launch inheriting a value equal to its marker (both cleared) and a user value with no marker (kept).
  - `TestSmokeRowOverTLSWithCAFile` (`internal/smoke`): one gateway row run against a `StartTLS` fake with `CAFile` set; the 503 probe reaches the fake (it is not silently "not 503").
  - `TestSubscriptionGuardUntrustedCertRefusesBeforeRegistration`: an opted-in header-mode launch to an `https://` fake server without `ca_file` exits 1 with the hint; no `POST /sessions`, no start, the login hook never called.
  - `TestSubscriptionLaunchOverHTTPSWithCAFile`: the same with `ca_file` launches (through `loopbackOnlyClient`, which now honors `ep.CAFile`); the child env dump has `NODE_EXTRA_CA_CERTS` once, equal to the absolute `ca_file`.
- [ ] **Step 2: Run** `cd wt && go test ./internal/modelclient ./internal/modelping ./internal/modelmode ./internal/agents ./internal/smoke ./cmd/wt-session -run 'HTTPSEndpoint|CAFile|ServerCert|Hostname|SchemeMismatch|NodeCAEnv|UntrustedCert|OverHTTPS|OverTLS' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** decision 14 (the client half). No `InsecureSkipVerify`, no new flag.
- [ ] **Step 4: Run** `cd wt && go test ./internal/modelclient ./internal/modelping ./internal/modelmode ./internal/config ./internal/agents ./internal/smoke ./internal/tui ./internal/depcheck ./cmd/wt-session`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): trust a TLS model server through ca_file; NODE_EXTRA_CA_CERTS for claude and pi (#597)`.

### Task 7: `wt smoke` never spends the subscription by default

**Files:**
- Modify: `wt/internal/smoke/smoke_wire.go` (`walkWire` returns the dropped ids; `CandidatesWire` and `EligibilityWire` pass them on), `wt/cmd/wt-session/smoke.go` (the `--subscription` flag, the SKIP rows, and Task 6's guard before a subscription row runs), `wt/docs/wt-smoke.md`
- Test: `wt/internal/smoke/smoke_wire_test.go`, `wt/cmd/wt-session/smoke_test.go`

**Interfaces:**
- Consumes: `catalog.UsesSubscription`, `subscriptionGuard`.
- Produces: `walkWire(rows []modelapi.Row, ags []config.Agent, includeStart bool, allowSubscription string) (cands []Candidate, droppedSubscription []string)`; `CandidatesWire(rows []modelapi.Row, ags []config.Agent, allowSubscription string) ([]Candidate, []string)`; `EligibilityWire(rows []modelapi.Row, ags []config.Agent, allowSubscription string) (models []config.Model, agentsForModel map[string][]string, droppedSubscription []string)`; their callers (`cmd/wt-session/smoke.go`, `internal/smoke/smoke_wire_test.go`, and any other caller `go build ./...` finds) updated; `wt smoke <id> --subscription` (the flag without an id, or with a non-subscription id, is exit 1 `--subscription needs the id of one subscription model or alias`); decision 9's SKIP rows in the existing text and `--json` shapes.

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
- Modify: `wt/docs/wt-model-server.md` (the ping and error rows landed in Tasks 1 and 2, the "Native TLS" reference section in Task 2b):
  - "Where it runs" (plain http by default; native TLS for remote header mode; terminator deployments: same host passes, another host re-encrypting to the TLS port passes, another host forwarding plain http is refused; forwarding headers only refuse, on the loopback branch)
  - certificate recipes under "Native TLS" (decision 13's constrained-certificate rule): first, openssl making a leaf that is not a CA, with a mandatory subjectAltName (`umask 077`; `openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 825 -subj '/CN=wt-model' -addext 'basicConstraints=critical,CA:FALSE' -addext 'subjectAltName=IP:ADDR,IP:127.0.0.1' -keyout … -out …`, adding `DNS:HOST` for a name; the lead form follows owner confirmation 4; then `chmod 644` the cert and `chmod 600` the key; key and certificate in separate files, never one combined PEM; if macOS LibreSSL rejects `-addext`, use Homebrew openssl); a name-constrained CA as the alternative; mkcert, saying plainly that its `rootCA.pem` (the clients' `ca_file`) trusts every name, so whoever holds `rootCA-key.pem` can impersonate any site to every agent on the session machine; `tailscale cert` (publicly trusted, no `ca_file`); rotation = replace both files, then `launchctl kickstart -k gui/$(id -u)/<label>` from a terminal that does not depend on the gateway; on the host, the local session needs `ca_file` set to the `tls_cert` path (setup prints the line)
  - new "Subscription passthrough" section: the key, the wrong-type rule, restart, header table, guard, the hazards that keep it off, owner-only, token placement, the old-binary notes (upgrade every syncing `wt-model` first)
  - "Known limits" (TLS encrypts the hop but does not identify the client: the shared token is the only authenticator; undetectable same-host plain-http forwarder; an older `litellm set` drops `[gateway]` and with it TLS; a session still on `http://` after TLS is turned on has already sent its token in clear when the hint appears: rotate it)
  - replace the sentence "The server speaks plain HTTP and has no TLS…" (line 82 today) with the plain-by-default, native-TLS wording
- Modify: `wt/docs/style-guide.md`: S7 (the gateway forwards the client's own login in the opt-in header mode only), S3 (the gateway's own errors include the three 403 codes), S1 (the TLS and subscription lines after the announce line).
- Modify: `docs/guides/00-config-map.md`: the `~/.config/wt-model/config.toml` row gains `[gateway] subscription_passthrough`, `tls_cert` and `tls_key`, written by hand only; the agent-wt `config.toml` row gains `[model-server] subscription` and `ca_file`, by hand only.
- Modify: `wt/docs/wt-session.md`:
  - "The refusals" (six new: opt-in, endpoint, server, version, login, launch profile) and the TLS trust hints (decision 14)
  - native vs passthrough contrast; a header-mode shell carries the token; owner-only paragraph
- Modify: `wt/docs/wt-config.md` (remote plain http cannot use passthrough, https can; `[model-server] subscription`; `ca_file` and `WT_MODEL_CA_FILE`; owner-only paragraph).
- Modify: `wt/docs/wt-agents/pi-wt.md`, `codex-wt.md`, `opencode-wt.md`, `copilot-wt.md` (one line each on trusting a private gateway certificate: what wt sets for pi, and the system trust store for the others, marked unverified until Task 9 step 5b records it).
- Modify: `wt/docs/wt-agents/claude-wt.md`:
  - "Authentication & credentials"; `NODE_EXTRA_CA_CERTS` over an `https://` gateway with `ca_file` (and its removal on native launches)
  - cost: LiteLLM books $0 for subscription rows while Claude Code shows list price; a usage-credits note for fable rows if the owner says so (owner confirmation 2)
  - slot profiles
  - the `settings.json` env override caveat (what the gateway answers)
  - the window note (decision 11: a recognized Claude id keeps Claude Code's own window; `CLAUDE_CODE_AUTO_COMPACT_WINDOW` when a gateway enforces less)
- Modify: `wt/docs/wt-agents/slot-profiles.md`:
  - "What Claude Code is given"
  - the fable/opus subscription + sonnet/haiku alias example
  - failures (the header-value refusal; a launch profile cannot override the header env); owner-only paragraph
- Modify: `wt/docs/wt-model.md` (`model add claude-subscription … --family <f> --context-window N`, bare id, not in the Models tab form).
- Modify: `wt/docs/internals/packages.md` (`internal/claudelogin`, `internal/tlsfile`, `internal/tlstest`, `custom_headers.go`, `ca_env.go`, `subscription_guard.go`, `subscription_hazards.go`) and `wt/docs/internals/agents.md` (header mode, the strip on every launch).
- Modify: `docs/guides/02-providers-and-models.md` (the provider row; owner-only warning).
- Modify: `docs/guides/04-litellm-config.md` and `docs/guides/14-litellm-install-and-configuration.md`:
  - keyless rows, unrouted while the proxy has `ANTHROPIC_API_KEY`, header forwarding or a foreign `anthropic/` row
  - `store_prompts_in_spend_logs`: never paste a token into a prompt
- Modify: `docs/guides/06-wt-agents-and-models.md` (a short section pointing at the commands, no live state).
- Modify: `docs/guides/13-deployment-use-cases.md` (replace the "**No TLS.** Plain HTTP. Keep it on a network you trust…" bullet with: plain http by default, native TLS through `[gateway] tls_cert`/`tls_key`, `ca_file` on the session machine; remote over native TLS or via a tunnel; a pointer to the certificate recipes; owner-only paragraph in the remote section).
- Modify: `wt/CLAUDE.md` ("wt-model" gateway mode, native TLS, `Config`/`Options` fields, `serve.json`'s `tls_cert`, the never-logged invariant; "Agents" header mode and the CA env; the "Go module" package list gains `internal/claudelogin`, `internal/tlsfile`, `internal/tlstest`).
- Modify: root `CLAUDE.md` (wt-model line: `[gateway]` with `subscription_passthrough`, `tls_cert`/`tls_key`, and `serve.json`'s `tls_cert`; `wt smoke --subscription`).
- Modify: `.claude/skills/wt-manage-slot-profiles/SKILL.md` (subscription ids as fable/opus targets; never smoke them without asking).
- Modify: the spec (`docs/superpowers/specs/2026-11-22-litellm-aliases-groups-design.md`): its "Phases" line (phase 3 delivered, plan path); its "Subscription models" paragraph (decision 6's deviation); its "Gateway" paragraph (the mode is accepted from loopback or over native TLS on serve, `r.TLS != nil`; same-host and re-encrypting terminators pass; the forwarding-header refusal rule); and a dated entry under "Decisions (2026-11-22)" recording the owner's revised decision that native TLS is in scope, pointing at plan decisions 13 and 14.

- [ ] **Step 1: Update the docs.** Keep them state-independent: show `wt-model model list` and `wt model list --server`, and no live ids beyond illustrative examples.
- [ ] **Step 2: Run** `make lint` from the repo root. Expected: exit 0, `ALL LINKS OK`. Then `grep -rn 'ANTHROPIC_AUTH_TOKEN' wt/docs docs/guides` and check every hit says token mode, and `grep -rniE 'no TLS|has no TLS|plain HTTP\. Keep' wt/docs docs/guides` prints nothing, and `grep -L 'own login' docs/guides/02-providers-and-models.md docs/guides/13-deployment-use-cases.md wt/docs/wt-model-server.md wt/docs/wt-session.md wt/docs/wt-config.md wt/docs/wt-agents/slot-profiles.md` prints nothing.
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
  - `wt-model model add claude-subscription claude-haiku-5-5 --family <owner's family> --context-window 1000000` (owner confirmation 1). This adds the provider row and a `config.yaml` row, one serialized proxy restart, which needs an explicit go-ahead.
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
- [ ] **Step 5b: Native TLS checks** (owner approves; dummy credentials, step 4's scratch serve and scratch home only; the live serve, `~/.config/wt-model/config.toml` and the owner's real certificate paths are never used without a separate approval; no private key is ever printed, `cat`-ed or pasted):
  - Scratch certificate: `umask 077; mkdir "$scratch/tls"`, then Task 8's openssl recipe (`-addext 'basicConstraints=critical,CA:FALSE'`, `-addext 'subjectAltName=IP:<lan ip>,IP:127.0.0.1'`, the SAN form per owner confirmation 4), `-keyout "$scratch/tls/server.key" -out "$scratch/tls/server.crt"`, then `chmod 644` the cert and `chmod 600` the key. If LibreSSL rejects `-addext`, use Homebrew openssl, or mkcert with `CAROOT="$scratch/ca"` (never `mkcert -install`).
  - Constrained-anchor check: sign a second certificate for `DNS:api.anthropic.com` with the same scratch key and `server.crt` as issuer; `openssl verify -CAfile "$scratch/tls/server.crt"` on it must fail. Record that Go (the `curl`/wt pings below) and Node (the `claude -p` below) accept the `CA:FALSE` self-signed leaf as a directly trusted anchor; if either refuses it, stop and switch the recipe to the name-constrained CA before going on.
  - Combined-PEM check: `cat server.crt server.key > "$scratch/tls/combined.pem"` (never displayed) as `tls_cert`: the scratch serve exits 1 naming the path, with no key line in its stderr. Save the key's PEM body lines into a 0600 pattern file `$scratch/tls/keybody` for the counts (`grep -v -- '-----' server.key > keybody`, never echoed).
  - Negative first: `chmod 644` the key, point the scratch home's `[gateway]` at the pair and start the scratch serve: exit 1, the line names `chmod 600` and the path, and `grep -cF -f "$scratch/tls/keybody"` over its stderr is 0. Restore 0600. Then a half pair (`tls_key` removed): exit 1.
  - Start the scratch serve on the LAN address with the pair and `subscription_passthrough = true`: the announce line is unchanged and first, the TLS line follows, no remote warning, and the scratch `serve.json` holds `tls_cert`.
  - `curl --cacert "$scratch/tls/server.crt" https://<lan ip>:<port>/control/v1/ping` with the scratch token as Bearer: 200. Without `--cacert`: curl exit 60 (verification failed).
  - From the LAN address, `curl --cacert "$scratch/tls/server.crt" -H @"$scratch/h" -H 'Authorization: Bearer sk-ant-oat-FAKE' https://<lan ip>:<port>/v1/messages`: forwarded (the mock's log shows it, as a count), where step 5's plain-http request got 403 `subscription_insecure`. A plain `http://` request to the TLS port gets Go's 400, never the gateway.
  - `wt-model alias list` against the scratch home reads the scratch serve's sessions over https (proves `Record.TLSCert`).
  - With a scratch agent-wt config (opted in), `WT_MODEL_MODE=remote WT_MODEL_URL=https://<lan ip>:<port> WT_MODEL_TOKEN_FILE=<scratch> WT_MODEL_CA_FILE="$scratch/tls/server.crt"`: one real `claude -p` on the subscription row reaches the mock (the agent leg honors `NODE_EXTRA_CA_CERTS`). Without `WT_MODEL_CA_FILE`: wt refuses with the `ca_file` hint and the scratch serve saw no `POST /sessions`.
  - One tiny streamed request each through pi and codex on mock rows over TLS (h2 streaming per agent family). Record whether codex trusts the certificate with nothing set (decides the codex docs line).
  - A second scratch certificate with only `DNS:wrong.invalid`: restart the scratch serve on it; wt gives the hostname hint.
  - Stop the scratch serve; keep `$scratch/tls` for step 9's counts.
- [ ] **Step 6: Canary through the live proxy** (no approval needed): `curl -H @"$scratch/h"` to the live gateway on 127.0.0.1 with `Bearer sk-ant-oat-FAKE` for `claude-haiku-5-5` (expect an upstream 401 from Anthropic). Repeat step 1's counts: the token-absence counts must equal the baseline, and the canary count must be 0 in every place. Any canary hit means the live proxy logs or stores the header: stop before step 7.
- [ ] **Step 7: Owner approves the real requests:**
  - Set `[model-server] subscription = true` in `~/.config/agent-wt/config.toml` (backup first; owner's edit).
  - `wt smoke claude-haiku-5-5 --subscription`: one turn, success.
  - Then a mixed session: a test slot profile with fable/opus = `claude-haiku-5-5`, the cheap stand-in for both, and sonnet/haiku = `alias/…` non-subscription aliases. Run one tiny `claude -p --model opus` and one `--model sonnet`.
  - Per-model counts come from `wt stats` or the spend endpoint, not prompts. The opus call books on `claude-haiku-5-5`. The sonnet call and Claude Code's background haiku call book on the alias targets.
  - Record what `/context` shows for the window (decision 11: expected Claude Code's 1M for the subscription id, not the profile's declared minimum).
  - The child env (via `ps eww <pid> | tr ' ' '\n' | grep -c '^ANTHROPIC_AUTH_TOKEN='`) counts 0.
- [ ] **Step 8: Fallback view:** `curl -H @"$scratch/h"` with no `Authorization` to `alias/haiku-sub`. LiteLLM skips the keyless member and lands on order 2. Confirm from the spend log's `model_group`/model, as a count.
- [ ] **Step 9: Token-absence counts:** repeat step 1's counts (logs, `pg_dump`, Redis). All token-absence counts must equal the baseline (0). Also count `x-wt-model-token` occurrences in the same logs (expected 0), and lines of the scratch key body (`grep -cF -f "$scratch/tls/keybody"`) in the same logs, the scratch serve's stderr captures and scratch `serve.json` (expected 0). The `~/.claude/projects` count is recorded (tool subprocesses inherit the env; documented).
- [ ] **Step 10: Owner approves cleanup:**
  - `wt-model alias rm alias/haiku-sub --yes`.
  - `wt-model model rm claude-haiku-5-5` (the one other proxy restart; go-ahead needed). Steps 3 and 10 are the only proxy restarts in the check.
  - Remove the `claude-subscription` provider row, or record that it is kept (owner's choice).
  - `wt-model litellm list` shows no `anthropic/` row left.
  - `wt profile slots rm <the test profile>`.
  - `wt worktrees`, then `wt remove` the worktrees and branches the step 4 and step 7 launches created, with approval.
  - Leave or revert `[gateway]` and `[model-server] subscription` as the owner says (backups from steps 2 and 7).
  - Stop the scratch harness; delete `$scratch/h` and `$scratch/tls` (and `$scratch/ca` if mkcert was used).
- [ ] **Step 11:** Run `make pre-pr` with `run_in_background` and read the exit from the log. Expected: exit 0.
- [ ] **Step 12: Open the PR** with `gh pr create --body-file` and `Refs #597`, with no closing keyword. The body ends with the generated-with line, and lists the live-check results (steps 0–9, 5b included) and the follow-ups as `Refs`: #692, #693, #694, plus any issue newly filed (for example the modeltiers keys); search open issues before filing so none is duplicated.

---

## Self-review notes

- **Coverage of the owner's decisions:**
  - local and remote: Tasks 1, 2 and 6 (the guard is about transport, not location)
  - header name: Task 1
  - off by default and explicit: Task 2, decisions 1 and 12, `TestSetupNeverEnablesPassthrough`
  - loopback-or-TLS guard: Task 1, plus the client twin in Task 6
  - native TLS (revised decision): serve side Task 2b (plain http default, fail-closed config, 0600 key, TLS 1.2 floor, announce unchanged, host commands over https), client side Task 6b (one `ca_file`, no skip-verify, agent leg via `NODE_EXTRA_CA_CERTS`), live Task 9 step 5b
  - owner-only docs: Task 8
  - token never stored or logged: Tasks 1, 2, 5, 6 and Task 9 steps 6 and 9
  - claude-only: Task 4
  - legacy byte-for-byte: Task 1's unmodified tests plus `TestLegacyUpstreamHeadersUnchanged`
- **Order of Consumes:** gateway (T1) → switch and ping (T2, uses T1's Config fields) → native TLS on serve (T2b, uses T1's TLS branch and T2's `GatewaySettings`, `Options.ErrorLog`, `subscriptionLines`; produces the CA-aware `modelping` client, `tlsfile.HasPrivateKey`, `modelmode.LocalURL` and `tlstest` that T6b needs) → provider, rows and hazards (T3) → catalog (T4, uses T2's option and T3's constants) → driver (T5, uses T4's `UsesSubscription`/`ToModel`) → guards (T6, uses T2's ping, T4 and T5) → client trust (T6b, uses T2b's client and `tlstest`, T5's env assembler, T6's guard) → smoke (T7, uses T6's guard) → docs (T8) → live (T9). T2b and T6b are lettered so every other task number and cross-reference stays as it was.
- **Lessons carried from the phase 1 and phase 2 reviews** (only those this phase touches):
  - Route warnings must reach lifecycle output: Task 3's hazard warnings go through `plan.Warnings`, which `SyncAndWarn`/`routePrintf` already print.
  - Every proxy restart stays serialized through the existing path, and no new restart caller is added.
  - A zero option keeps old behavior: `Config{}` is legacy, and `CatalogOptions{}` blocks subscription rows.
  - Hermetic tests: TestMain isolation, no package vars, per-call seams (`hooks.claudeLogin`, `Config.ErrorLog`, injected `ProxyEnv`), httptest upstreams only.
  - Old-binary hazard: the live check restarts serve on the new binary first and proves it (step 0), as phase 2's did.
  - Implementers never read the LiteLLM plist and never commit binaries (Global Constraints).
- **Unverified before execution (live check, not mocks):**
  - that `CLAUDE_CODE_MAX_CONTEXT_TOKENS` leaves a recognized Claude id alone without `DISABLE_COMPACT` (documented 2026-11-22; decision 11, step 7), and whether the subscription path gives the full 1M window
  - the byte-exact id Claude Code sends per slot (follows from the docs; step 4's mock capture confirms it)
  - h2 streaming through `ServeTLS` with pi and codex, and which CA variable codex, opencode and copilot honor (step 5b)
  - the minimum Claude Code version for header mode: the plan's 2.1.227 is unverified against the alias thresholds the docs give (2.1.280 to 2.1.321); wt always sets the slot variables, so the aliases' own resolution does not matter
  - whether `/model/info` and `/v1/models` list a keyless `config.yaml` `anthropic/` row like a keyed one (the `routed` map depends on it; step 3)
  - whether Claude Code attaches `ANTHROPIC_CUSTOM_HEADERS` to hosts other than `ANTHROPIC_BASE_URL` (step 4 records hosts only)
  - whether the scratch LiteLLM honors `ANTHROPIC_API_BASE` (step 4's pre-check decides)
  - `claude auth status` behavior offline (the 10 s timeout and "no login" cover it, but the message may mislead)
  - Claude Code versions after 2.1.330
- **Unresolved for the owner:**
  - the four items under "Needs owner confirmation" at the top of Plan decisions (ids and windows plus families; plan billing; expired-certificate handling; the TLS endpoint form). `claude-sonnet-5-5` is now known to be a real API id; no seed list is shipped, so models are still added by hand.
  - whether to file modeltiers keys for `claude-fable-5-2`/`claude-sonnet-5-5` (they read `unrated` until then) as a follow-up now (the legacy `x-litellm-api-key` strip, the `[gateway]` writer and cloud rows in the database are already #692, #693 and #694)
  - not an owner question, but a stated default the owner may overrule: setup and the Mode tab print the `ca_file` line for a TLS host's local session rather than write it (decision 13, *Setup and the Mode tab*)
