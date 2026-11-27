# LiteLLM model groups: claude-* and open-* (design)

Date: 2026-11-27. Status: draft for approval. Step A is specified in full; step B is a marked follow-up.

## Intent and why

The `alias/fable-mixed` alias (Claude subscription first, one Ollama model second) was a proof of
concept (#596, #690). This formalizes it into a fixed set of named groups that the slots of a Claude
Code session (and pi, codex) can name like a model:

- keep working when the Claude subscription is rate-limited, by falling back to open models;
- spread load across several models in a tier instead of leaning on one;
- keep one session on one model (session affinity), so a long conversation is not served by a
  different model on each turn.

The groups are built on `wt-model alias` (registry `[[aliases]]`, database rows with `order`). The
research pasted with the request (a hand-written `model_list` with `ANTHROPIC_API_KEY`) was guidance
only and is not followed: `config.yaml` is written only by `wt`, and Claude must go through the
subscription, never the API.

## Goals

- Six aliases: `alias/claude-fable`, `alias/claude-opus`, `alias/claude-sonnet`,
  `alias/claude-haiku`, `alias/open-pro`, `alias/open-flash`.
- Within a tier the model is chosen at random (equal `order` shares requests).
- Session affinity on, so a session sticks to the deployment it was given, including after a
  failover (step A turns the router setting on; step B makes `wt` own it).
- All targets are models in this machine's registry.

## Non-goals and anti-goals

- No new `wt` or `wt-model` code in step A (a registry edit and a router setting only).
- No Anthropic API key, and no Anthropic model through OpenRouter (it bills the API per token and
  defeats a cheap fallback).
- No local-model tier, no nested groups (an alias target is a registry model, never another alias).
- Anti-goal: a fallback that silently shrinks the context window below 1M, or one that makes the
  subscription route unusable (the subscription hazard check must stay honored).

## Constraints

- `~/.config/litellm/config.yaml` is written only by `wt`; a hand-written block it does not manage
  must survive a sync (checked in the plan, see "Left to the builder").
- Alias rows live in LiteLLM's database; `alias set` needs the proxy pair and `store_model_in_db`.
- A claude-subscription target is routed only while the proxy passes the subscription hazard check.
- An alias window is the smallest of its targets' windows; all targets below are about 1M.
- Work happens in a worktree (`wt -W`), never the primary checkout. `make pre-pr` before any PR.

## Approaches considered

- **A. Aliases only plus a one-time `router_settings` block.** No Go code; affinity and the
  session header live outside `wt`. Picked first.
- **B. A, plus `wt` owning the affinity settings and the session header.** The real formalization:
  any `wt` session gets sticky routing with no setup; costs Go changes, tests, a gate. Picked as
  the follow-up, after the groups have shown their failover behaviour.
- **C. Hand-written `model_list` as in the research.** Rejected: collides with `wt` owning
  `config.yaml`, cannot express keyless subscription passthrough, never follows the registry.

Decision: A now, B after. The table below is final: no claude-* alias falls back to another Claude
tier, because a subscription rate limit normally hits every Claude model at once (the user's
spec).

## The groups (the user's decision)

`open-pro` and `open-flash` come from the proposal; the claude groups reuse them as fallbacks:
fable and opus use the open-pro pools, sonnet and haiku use the open-flash pools.

Pools (registry ids; the builder confirms each with `wt-model model list`):

| Pool | Ollama (cloud) | OpenRouter |
|---|---|---|
| pro | `ollama/kimi-k3.1-pro:cloud`, `ollama/glm-5.3:cloud`, `ollama/mimo-v2.6-pro:cloud`, `ollama/deepseek-v4-pro:cloud` | `openrouter/z-ai--glm-5.3`, `openrouter/moonshotai--kimi-k3`, `openrouter/deepseek--deepseek-v4-pro-0813` |
| flash | `ollama/glm-5.3-flash:cloud`, `ollama/mimo-v2.6-flash:cloud`, `ollama/deepseek-v4.1-flash:cloud` | `openrouter/z-ai/glm-5.3-flash`, `openrouter/xiaomi--mimo-v2.6-flash` |

`minimax-m3.1-flash` is excluded from every pool: it does not work with tool calls (the user's finding).
The flash OpenRouter pool therefore has two members.


Aliases (lower `order` tried first; equal order = random pool):

| Alias | order 1 | order 2 | order 3 |
|---|---|---|---|
| `alias/claude-fable` | `claude-fable-5-2` | pro Ollama pool | pro OpenRouter pool |
| `alias/claude-opus` | `claude-opus-5-5` | pro Ollama pool | pro OpenRouter pool |
| `alias/claude-sonnet` | `claude-sonnet-5-5` | flash Ollama pool | flash OpenRouter pool |
| `alias/claude-haiku` | `claude-haiku-5-5` | flash Ollama pool | flash OpenRouter pool |
| `alias/open-pro` | pro Ollama pool | pro OpenRouter pool | none |
| `alias/open-flash` | flash Ollama pool | flash OpenRouter pool | none |

Prerequisite: register `claude-sonnet-5-5` (provider `claude-subscription`, `context_window =
1000000`, like its siblings); the registry has fable, opus and haiku only.

## Step A: how it gets built

1. Register `claude-sonnet-5-5`.
2. `wt-model alias set` for each of the six (equal-order cloud targets share requests, documented
   in `wt/docs/wt-model.md`; #690 only added several *local* targets) (each with `--dry-run` first), after `make install` and
   a `wt-model serve` restart if the installed `wt-model` predates `[[aliases]]` support.
3. Enable session affinity with a `router_settings` block in `config.yaml`
   (`optional_pre_call_checks: ["session_affinity"]`, `session_affinity_repin_on_failover: true`, a
   `deployment_affinity_ttl_seconds`), check against the installed LiteLLM's option names, and
   check that it survives `wt-model litellm sync`.
4. The session id header (`x-litellm-session-id`) is sent by a shell function around **`wt`**, not
   bare `claude`: `wt` launches claude and already merges an inherited `ANTHROPIC_CUSTOM_HEADERS`
   (it replaces only its own `x-wt-model-token`, keeping other lines such as `x-litellm-tags`). The
   function must append to the variable, never overwrite it, or it would break subscription
   passthrough. The gateway drops only credential headers, so the session header should reach
   LiteLLM; the plan verifies it with one request. Affinity in step A covers `wt`-launched claude
   only; pi and codex get it in step B.
5. Keep `alias/fable-mixed` (my call). It is never removed in step A; removing it is a separate
   `wt-model alias rm` the user can run once nothing uses it. Two machines work this repo, so
   "no session uses it" cannot be established from here.
6. Document in `docs/guides/04-litellm-config.md` section 7 as a worked example, with a command
   that shows the live groups and no model counts (guides never embed live state).

## Step B (follow-up, its own design pass)

`wt` writes the affinity `router_settings` on sync and sets the session header when it launches an
agent; the six groups become reproducible on a fresh machine. Out of scope for step A's plan.

## Verification

- `wt-model alias list` shows six aliases with the intended targets, window 1000000, ROUTED yes.
- Failover: with the subscription route refused, a request on `alias/claude-opus` is answered by
  an order 2 model; with order 2 down, by order 3. Spend logs show which.
- Pool spread: repeated requests without a session id hit more than one model of a tier.
- Affinity: two requests with the same session id hit the same deployment; after a forced failover
  the session stays on the fallback.
- A `wt` claude launch with the opus slot set to `alias/claude-opus` still works.

## Left to the builder

- Exact option names and units for affinity, from the installed LiteLLM (verified present:
  `session_affinity`, `session_affinity_repin_on_failover`).
- Where the `router_settings` block lives so a sync keeps it.
- How a shared affinity pin interacts with a 429-driven cooldown on the subscription route.
- Test shape for failover. A refused subscription route is hard to cause on purpose, so a stand-in
  is acceptable (my call): a temporary alias whose order 1 target is stopped or unroutable, which
  exercises the same ordering. One live observation is taken opportunistically when the real
  subscription limit is hit, and is not a gate.
- If `wt-model litellm sync` strips the hand-written `router_settings` block and no other place
  keeps it, stop and report instead of adding Go code (my call): that pulls part of step B forward,
  which needs the user's say.
- If a pool target turns out to have a window below 1M, drop it from the pool rather than shrink
  the alias window (my call, from the anti-goal). All twelve ids in the table exist in the
  registry as written, including the one `/` id (`openrouter/z-ai/glm-5.3-flash`).
