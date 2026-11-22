# LiteLLM aliases, groups and a Claude subscription behind wt-model

Status: decisions taken 2026-11-22 (see Decisions); phase 1 is next. Refs #596, #597, #598, #618, #626. Nothing in `wt/` is changed by this document; the spike harness is in `2026-11-22-litellm-aliases-groups-spike/`.

## Intent

Let a Claude Code session use a mix like: `fable` and `opus` on a Claude subscription, `sonnet` on a cloud open-weight model, `haiku` on a local MLX model, with the model behind each slot changeable without restarting the agent, and with a slot able to fall back to another provider. Claude, pi and Codex are the agents in scope.

Goals: say what LiteLLM can do (verified, not assumed); say what wt-model must add; keep wt-model's rule that a session never holds the host's LiteLLM credentials. Non-goals: a design for non-Claude agents using a subscription (not allowed by Anthropic's terms and not wanted), changes to any agent's own code.

## Answers

| Question | Answer | How it was checked |
|---|---|---|
| Can agents use LiteLLM aliases and groups instead of real model names? | **Yes**, on every wire the three agents speak: Claude Code's `/v1/messages`, pi's `/v1/chat/completions`, Codex's `/v1/responses`. | Real `claude`, `pi` and `codex` against a scratch proxy through wt-model's gateway (spikes 1, 6, 7) |
| ...can the model behind an alias change mid-session? | **Yes for DB-stored LiteLLM models**: `PATCH /model/{id}/update` returned in 40 ms, the next request went to the new backend, a stream already running finished on the old one, and the agent-visible model name never changed. One Claude Code session (same session id) survived three re-points: cloud, local, then a subscription route. Not for rows written in `config.yaml` (the API answers 404; a restart is the only way). | Spike 2, spike 6d |
| ...can a group use a subscription and fall back to another provider? | **Yes.** A group whose first member is the subscription (an `anthropic/` row with no `api_key`) falls back on 429 and 500, for streaming and non-streaming, and recovers after the primary's cooldown. A client that sends no OAuth token (pi, Codex) skips the subscription member and gets the fallback without the primary being contacted. Not mid-stream: an error after the first byte is not retried. | Spike 3, 6c, 7 |
| Can wt-model manage groups and aliases? | **LiteLLM can** (`/model/new`, `/model/{id}/update`, `/model/delete`, per-deployment `order`). **wt-model cannot yet**: it writes only `wt_managed` rows in `config.yaml`, one per registry id, and no `router_settings`; the live proxy has no `store_model_in_db`. See Design. | Code reading; live config structure; spike 2 |
| Can a Claude subscription use LiteLLM through wt-model? | **Not today, two blockers; feasible with a small change.** The gateway authenticates a caller by `Authorization`/`x-api-key` and then overwrites both, so a subscription OAuth token cannot pass; and wt's claude launch sets `ANTHROPIC_AUTH_TOKEN`, which Anthropic's gateway page says replaces the subscription login. A gateway mode that takes the wt token in a dedicated header and forwards the client's own `Authorization` was ten lines in a throwaway checkout and worked end to end with the real Claude Code login. | Spike 5, 6; Anthropic's gateway page (fetched today) |
| Can the four slots (fable, opus, sonnet, haiku) each use an alias or group? | **Yes.** All four `ANTHROPIC_DEFAULT_*_MODEL` variables were honored behind a gateway: `--model fable`, `opus`, `sonnet` and `haiku` each reached their own backend. Claude Code also makes a background call on the haiku slot at the start of every session. wt's claude driver already knows all four slot names. | Spike 6 |
| Can Claude Code mix subscription and other providers through them? | **Yes.** Fable and opus carried the real OAuth token to their subscription-style backends; sonnet and haiku reached the cloud and local backends carrying no token (`Bearer none`). | Spike 6 |

## What was run

Scratch LiteLLM 1.109.1 (the version the live proxy runs) on port 4099 with a scratch Postgres database, mock model backends on port 9100 (so no real credential or model quota is involved), and two scratch `wt-model` gateways: the current build and a patched one. The mock masks every credential header to 12 characters before logging it. The real Claude Code login was used in spikes 6 and 7; its token went only to the mock on localhost, never past it.

| # | Spike | Result |
|---|---|---|
| 1 | Alias on chat, messages and responses | All three resolve the alias to the right backend. `/v1/responses` against an `openai/` backend is forwarded to that backend's own `/v1/responses` (LiteLLM treats `openai/` as Responses-capable), so a backend that does not serve it fails after routing; ollama-style providers are bridged by LiteLLM. |
| 2 | DB alias re-pointed during a slow stream | 40 ms PATCH; first new request on the new backend 10 ms after the PATCH returned; in-flight stream finished on the old backend. Re-pointing an `openai/` alias to an `anthropic/` subscription route also works. A config-file row cannot be patched (404). `router_settings.fallbacks` set in the file cannot be changed by `POST /config/update`. |
| 2b | Group via deployment `order` | Two DB rows with one `model_name`, `order` 1 and 2: healthy traffic goes only to order 1, a 429 falls through to order 2 in the same request, no-OAuth clients go straight to order 2, order 1 resumes after recovery. No `fallbacks` block needed. |
| 3 | Group fallback | Falls back on 429 and 500, streaming included, recovers after cooldown. While the primary is failing, every request still tries it first (4 of 4), so a rate-limited subscription adds one failed call of latency per request. |
| 4 | Where a client's OAuth token goes | LiteLLM sends it to `anthropic/` deployments with `anthropic-beta: oauth-2025-04-20` added; open-weight and local backends see only the deployment's own key. An `anthropic/` row with no OAuth and no key answers 401 `Missing Anthropic API Key`. |
| 5 | Through wt-model's gateway | Current build: 401 whenever the OAuth token occupies `Authorization`. Patched build (token in `x-wt-model-token`, client `Authorization` forwarded, LiteLLM key in `x-litellm-api-key`): subscription alias reaches the backend with the OAuth token; open-weight alias does not; wrong token still 401. |
| 6 | Real Claude Code, four slots, patched gateway | Each slot reached its own backend; fable and opus carried the real token (115 characters, masked); a group in the opus slot fell back to open-weight when the subscription stand-in returned 429 and Claude Code accepted the answer; the background haiku call is visible. Claude Code itself sends `oauth-2025-04-20` and body fields `thinking`, `output_config` and `context_management`; LiteLLM translated or dropped them for the open-weight backends. An empty `ANTHROPIC_API_KEY=` (which wt sets today) does not displace the login. |
| 7 | pi and Codex through the current gateway | Alias, group (no OAuth, so the fallback) and a re-pointed alias all work on pi's chat wire and Codex's Responses wire. |

## What wt does today

- The gateway (`wt/internal/modelgateway/gateway.go`) takes the caller's token from `Authorization: Bearer` or `x-api-key`, deletes both, and sends LiteLLM `Authorization: Bearer <host key>`. Everything else passes.
- The claude driver (`wt/internal/agents/claude_slots.go`) maps `fable`, `opus`, `sonnet`, `haiku` to registry ids through `ANTHROPIC_DEFAULT_<SLOT>_MODEL` and sets `ANTHROPIC_AUTH_TOKEN` to the gateway token. A slot profile (`slot-profiles.toml`) maps each slot to a registry model.
- `config.yaml` has one row per registry id with `model_name` equal to that id (about 60 rows here, 54 marked `wt_managed`), plus `drop_params` and `use_chat_completions_url_for_anthropic_messages`; there is no `router_settings`, no alias, no group, and the live proxy runs one process with `coordination_redis` and no `store_model_in_db`.
- pi gets a `litellm` provider block (chat wire) and Codex `-c` provider overrides (Responses wire), both pointed at the gateway with the wt token.

## Constraints and risks

1. **Policy.** Anthropic's gateway page (fetched 2026-11-22) says a saved claude.ai login stays the active credential behind `ANTHROPIC_BASE_URL`, and that gateways passing this traffic on "must forward `Authorization` and the OAuth capability in `anthropic-beta`". It also says it "doesn't support routing Claude Code to non-Claude models through any gateway": unsupported, not prohibited. The legal page forbids third parties from routing requests "through Free, Pro, Max, or Ultra plan credentials on behalf of their users" and from collecting, storing or intermediating Claude.ai credentials; it does not address a person's own gateway. So subscription passthrough must stay one person's own login routed through their own gateway: **the token is never stored or logged by wt-model or LiteLLM** (phase 3 checks LiteLLM's spend logs and its own log for it), and the host operator turns the mode on deliberately. Remote use is allowed (decision 2) with the guards under Design. Enforcement is Anthropic's to apply "without prior notice".
2. **Not verified against the real service.** No request reached Anthropic. That LiteLLM's forwarded token is accepted by the real API is Anthropic's documented flow, not something this spike observed. One tiny real call would settle it; it uses the subscription, so it is an opt-in step (see Open decisions).
3. **Context window.** Claude Code reads the slot variables once, and wt sets `CLAUDE_CODE_MAX_CONTEXT_TOKENS` from the model's window at launch. An alias re-pointed to a smaller-window model keeps the old figure. Per the research note (not verified here), unrecognized ids also get a 200K default. A group or alias should advertise the smallest window among its targets, or wt should refuse a re-point that shrinks it.
4. **Cross-provider fidelity.** The note reports prompt-cache loss and thinking-signature 400s when the backend changes under a running conversation. The mocks cannot produce signed thinking blocks, so this is untested; Claude Code's own recovery (drop old thinking blocks, retry) is documented but not exercised.
5. **Fallback cost.** While the subscription is limited every request tries it first. LiteLLM's `allowed_fails` and `cooldown_time` can shorten that; the right values need a real limit event.
6. **Mid-stream failure** is not retried by LiteLLM.
7. **Two kinds of rows.** Rows in `config.yaml` cannot be edited through the API and a `fallbacks` key in the file blocks API changes to it. Aliases and groups therefore belong in the database, expressed with `order` rather than `fallbacks`, and `wt-model litellm sync` must leave database rows alone.
8. **Deploy dependency.** Database rows need `general_settings.store_model_in_db: true` on the live proxy: one restart, an owner step shared with #598.

## Approaches

**A. Aliases and groups as registry entries, kept in LiteLLM's database by wt-model (recommended).** `registry.toml` gains an alias entry (one or more ordered targets); `wt-model` reconciles one database row per target with a deterministic `model_info.id`, hot, using the API verified above. Slot profiles and the catalog accept alias ids. Subscription support is a separate, opt-in layer. Trades: a new registry concept and a reconciler; needs `store_model_in_db` (which #598 needs anyway); in return a re-point is live and survives restarts.

**B. Aliases as more `config.yaml` rows.** Reuses today's sync: an alias is a row with another `model_name`, a group is several rows. Trades: every change is a rewrite plus the restart that #598 exists to avoid (a drain of up to 3 minutes), no mid-session re-pointing, no API edits. Cheapest, but it only delivers the static half of the question.

**C. Leave it to the user.** Follow the note's recipe: Claude Code talks to LiteLLM directly with the OAuth header and the user maintains rows by hand. Trades: nothing for wt to build, but aliases and groups never appear in the catalog, the picker or slot profiles, and a mixed session bypasses wt-model's token and session tracking.

Recommendation: **A, in phases**, because it is the only one that delivers the live re-point and it shares its foundation (database rows, deterministic ids, a reconcile at start-up) with #598.

## Design of A

**Data model.** An alias entry names a gateway id and ordered targets:

```toml
[[aliases]]
id = "alias/sonnet-main"
targets = [
  { model = "ollama/mimo-v2.6-pro:cloud", order = 1 },
  { model = "omlx/<local qwen 3.8>",       order = 2 },
]
```

One target is an alias, several are a priority group (equal `order` load-balances). LiteLLM mapping: `model_name` is the alias id, one database row per target copying that target's `litellm_params`, with `order` set. wt-model owns these rows through the API and a marker in `model_info`.

**Subscription models.** A `claude-subscription` provider in the registry whose models are the subscription's real ids (`claude-fable-5-2`, `claude-opus-5-5`, ...), routed as `anthropic/<id>` rows with no `api_key`. They are offered only to the claude agent, only when a login is present, and marked in the catalog; pi and Codex never see them (without OAuth those rows answer 401, and a group containing one falls back, as verified).

**Gateway.** An opt-in header mode, off by default and turned on by the host operator: when `x-wt-model-token` is present it is the only credential checked, the header is removed, the client's own `Authorization` and `anthropic-beta` are forwarded, and LiteLLM is authenticated with `x-litellm-api-key`. Without the header, behavior is unchanged. It works for local and remote sessions (decision 2). Guards proposed for remote use, which are my additions and easy to change: the host setting must be explicit; the gateway refuses the mode from a non-loopback peer over plain http, because the subscription token would cross the network in the clear (https, or a tunnel that arrives from loopback, is accepted); and the docs state that it is for the owner's own login, since a shared host would be intermediating other people's credentials, which Anthropic's terms forbid and wt cannot detect. `wt/docs/wt-model-server.md`, the wire fixture and the style guide move with it.

**Claude launch.** For a subscription profile wt does not set `ANTHROPIC_AUTH_TOKEN`; it sets `ANTHROPIC_CUSTOM_HEADERS` (process environment only, nothing written to a file) and refuses to launch if `claude auth status` shows no login. Slot variables stay as today, now pointing at alias or subscription ids; `CLAUDE_CODE_MAX_CONTEXT_TOKENS` takes the smallest window among the targets.

**Surface.** `wt-model alias list|set|rm` and a screen under the Models tab; `wt profile slots` accepts alias ids; `wt smoke` covers an alias like any model; `wt-model litellm list` shows the rows with their order.

**Phases**, each with its own plan:
1. Enable `store_model_in_db` and move local routes to database rows with deterministic ids (#598).
2. Aliases and groups in the registry, the reconciler, catalog, slot profiles, window rule (#596, #626).
3. Subscription provider, gateway header mode, claude launch mode (#597).
4. #618 (an external gateway for pi) is independent: first answer whether pi can run the credential command itself.

## Your example

| Slot | Entry | Backed by |
|---|---|---|
| fable | `claude-fable-5-2` | subscription row, `anthropic/claude-fable-5-2`, no key |
| opus | `claude-opus-5-5` | subscription row, optionally a group with the sonnet-slot model as order 2 |
| sonnet | `alias/sonnet-main` | `ollama/mimo-v2.6-pro:cloud` (the cloud model through ollama), never `anthropic/` with a foreign `api_base` |
| haiku | `alias/haiku-local` | the local qwen 3.8 MLX model (omlx or mtplx), whose window is far below the Claude slots' |

Launching with this profile starts claude without `ANTHROPIC_AUTH_TOKEN`. Switching sonnet from mimo to another cloud model is `wt-model alias set alias/sonnet-main ...` and takes effect on the next request in every running session. The haiku slot also takes Claude Code's background calls, so a slow local model adds latency to titles and summaries but not to the main turn.

## Not verified

- A real request to Anthropic through LiteLLM with the subscription token (Anthropic's documented flow; opt-in).
- Thinking-signature and cache behavior across a real provider switch.
- Behavior with LiteLLM's Redis coordination on and under a second worker (the live proxy is one process; the scratch proxy had no Redis).
- The wildcard row `anthropic/*` from the research note; explicit rows were used here.
- Anything in the research note not repeated above.

## Decisions (2026-11-22)

1. **Approach A, in the four phases, starting with phase 1 (#598).** Taken.
2. **Subscription passthrough for local and remote sessions.** Taken. The guards above for remote use are proposed, not yet agreed.
3. **Header name `x-wt-model-token`.** Taken.
4. **Alias ids start as `alias/<name>`, real Claude ids for subscription models.** Taken. An id can be renamed later, at a cost: it is the LiteLLM `model_name` and the registry key, so a rename is create-new, repoint the slot profiles that name it, delete-old, and any session launched with the old id still sends it (the slot variables are read once). `wt-model alias rename` should update slot profiles and keep the old id as a redirect to the new one until no client session uses it (the server knows its client sessions).
5. **One real subscription request to verify end to end.** Authorized; result below.

## Real subscription check

(filled in by the verification run)
