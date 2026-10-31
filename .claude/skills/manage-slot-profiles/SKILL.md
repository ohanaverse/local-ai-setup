---
name: manage-slot-profiles
description: Keep Claude Code slot profiles (~/.config/agent-wt/slot-profiles.toml) healthy with `wt profile slots` - refresh modeltiers when stale, remove profiles no longer launched, propose better models for each slot, create new profiles (including an ollama/openrouter twin pair), research thinly scored models and smoke-test the chosen ones. Asks before every write. Use when the user wants to create, clean up or review slot profiles, asks which models to put in a slot, or says new models were released.
---

## When to use

A slot profile gives each of Claude Code's four slots (`fable`, `opus`, `sonnet`, `haiku`) its own registry model; the format and launch behavior are in `wt/docs/wt-agents/slot-profiles.md`. Use this skill when the user wants to:

- create a profile, or a twin pair of profiles on ollama and openrouter models;
- clean up profiles they no longer launch;
- check whether a profile's models are still good choices, for example after new models were released or `wt cloud-sync` added some.

Do the steps that fit the request, in this order. A plain "clean up my profiles" is steps 1-3; "make me a profile" is steps 1, 4, 5 and 6. Steps 2-4 change the file, so step 6 follows any of them.

Every write goes through `wt profile slots add|set|rm`, which backs the file up (`slot-profiles.toml.bak-<stamp>`), keeps every other entry and comment byte for byte, checks slot ids against the registry and refuses a result that would leave a profile unusable. Each command prints the backup path and then the `wt profile list` slot section; read it, and stop and report if any profile there is not `ok`.

## 1 Freshness

The ranking is only as current as `tiers.toml`.

```bash
wt profile slots candidates --json > /tmp/candidates.json
```

Read `tiers_generated_at` and `stale_sources`. If `tiers_generated_at` is `null` (no `tiers.toml`), older than 7 days, or `stale_sources` is not empty, tell the user which, and offer to refresh:

```bash
uv run --directory modeltiers modeltiers run     # add --dry-run first if the user wants to see the report without writing
```

Run it only on a yes. Then by exit code (`docs/guides/12-model-tiers.md`, step 3):

- **0**: written; re-run `candidates --json` and go on.
- **1**: not written. Show the last stderr line (`ERROR: modeltiers run FAILED (exit 1): …`), which names the cause; go on with the old file and say how old it is.
- **2**: written, but a source fell back to its last good snapshot; name it, say its tiers are as old as that snapshot, and go on.
- **3**: a sentinel moved and nothing was written. Stop this step and hand off to the `sentinel-failure` skill; do not re-pin anything from here. Go on with the old file only if the user wants to, and say it is stale.

If the user declines, go on and say the tiers are from `<tiers_generated_at>`.

## 2 Cleanup

```bash
wt profile slots usage --json
```

Each element is `{name, launches_30d, last_launch, comment, slots}`. Usage history is pruned at 30 days, so `launches_30d == 0` means "not launched in 30 days", not "never used". List those profiles with their `last_launch` (or "none recorded") and their `comment` verbatim; the comment often says the profile was a one-off test, or why it is kept.

Ask about each one separately ("Remove `<name>`?"). Remove only the ones the user confirms, one command per profile:

```bash
wt profile slots rm <name>
```

`rm` also removes the comment block directly above the entry. A profile launched recently is not a removal candidate unless the user names it.

## 3 Review

For each remaining profile, one profile at a time, compare every filled slot with what the registry offers now. For each slot model:

```bash
wt profile slots candidates --near <slot-model-id> --provider ollama,openrouter --json
```

`--near` lists, per provider, the rated models whose score is nearest the slot model's (itself excluded; `--per-provider N` for more than 3). Its list is empty when the slot model is unrated or unscored; then rank without it, `wt profile slots candidates --provider <the slot model's provider> --json`, and say the comparison has no anchor. Read the slot model's own row (tier, score, confidence, price, flags) from the plain `candidates --json` output. A slot model missing from that output is either not in the registry (`wt profile list` already says so) or excluded because `tiers.toml` says it cannot call tools: report it as a problem, not a choice.

Propose a swap only when it gains something, and say what:

- **Tier first.** A higher tier, or a clearly higher score in the same tier, is the reason to swap. Open-weights models come before closed ones at a similar score.
- **Price second.** Between models of similar score, the cheaper output `$/M` wins. A row flagged `subscription` (ollama cloud) shows a subscription price and period, not a per-token price, and is not comparable per token with an openrouter row; say so instead of comparing the numbers.
- **Say when the evidence is thin.** Name any `low-confidence`, `unrated` or `tool_call unknown` flag on the current or the proposed model, and offer step 5 for it.
- Apply the slot guidance below (haiku floor, local models).

Present the proposals for one profile as a short table (slot, current model, proposed model, tier and score of each, price of each, reason), then ask which to apply. Apply the accepted ones with one command for that profile:

```bash
wt profile slots set <name> --slot sonnet=<model-id> --slot haiku=<model-id>
```

`set` also takes `--default <slot|opusplan>`, `--env K=V` and `--unset-env K`. It cannot change the comment above the entry; if a swap makes that comment wrong, say so and leave the edit to the user.

## 4 Create

Walk the user through the profile, one decision at a time:

1. **Slots, in order: `fable`, `opus`, `sonnet`, `haiku`.** For each, show the top of `wt profile slots candidates --json` (open weights first, then score, then price; `--open-only` and `--provider a,b` narrow it) and let the user pick. A slot may be left unfilled; it then takes the `default` slot's model. `sonnet` is where the session works, so it usually gets the strongest model the user is willing to pay for per token; `opus` gets a stronger model than `sonnet` only if it is worth paying for in plan mode or `/model opus`.
2. **`default`**: the slot the session starts on, usually `sonnet`. Offer `opusplan` when `opus` holds a stronger model than `sonnet`: plan mode then runs on `opus` and everything else on `sonnet`. It needs both `opus` and `sonnet` filled (the command refuses otherwise).
3. **`REMEMBER_MODEL`**: when any slot is a local model, offer `--env REMEMBER_MODEL=<cloud-model-id>` so the remember plugin's background summarizer goes to a cloud model and does not queue behind the session on the local model or reload it after wt stops it.
4. **Context window of a local model.** wt declares `CLAUDE_CODE_MAX_CONTEXT_TOKENS` itself only from a cloud model's host window in `tiers.toml`; a local build has none, and Claude Code assumes 200K for an id it does not recognize. When the default slot (or any slot `/model` may switch to) is a local model whose real window is known, offer `--env CLAUDE_CODE_MAX_CONTEXT_TOKENS=<window minus a margin>` and say where the window figure came from.
5. **Comment**: always pass `--comment` with today's date and the reason, for example `--comment "Added <YYYY-MM-DD>: <why, and how the models were chosen>"`. Step 2 of a later cleanup reads it.

Show the full command and ask before running it:

```bash
wt profile slots add <name> --slot fable=<model-id> --slot opus=<model-id> \
  --slot sonnet=<model-id> --slot haiku=<model-id> --default sonnet \
  --env REMEMBER_MODEL=<cloud-model-id> --comment "Added <YYYY-MM-DD>: <reason>"
```

Names are lowercase letters, digits and hyphens. `add` refuses a name already in the file.

**Twin pair.** When the user wants to switch between ollama and openrouter by picking the other row in the picker, build two independent profiles named `<stem>-ollama` and `<stem>-openrouter`. The models need not be the same on both sides. Choose one side first (or start from an existing profile), then for each of its slot models run `wt profile slots candidates --near <that-model-id> --provider <the other provider> --json` and let the user pick each slot of the other side from those nearest-scoring models. Show both profiles side by side (slot, model, tier, score, price) before writing, then run two `add` commands, each confirmed. Give both the same `default` and options, and a comment that names its twin.

## 5 Research

When a model the user is considering is `unrated` or `low-confidence`, or two candidates are too close to call, offer to look it up. On a yes, search the web for its release notes or model card and for independent benchmarks (not only the vendor's own numbers), and check tool-calling support. Present what you found as **supporting evidence**, kept apart from the tier data, with the source and date of each claim. It never overrides a tier; it helps the user decide whether to take a thinly scored model, and whether to smoke it first.

## 6 Verify

`wt smoke` exercises one model, and a profile launch exercises only its `default` slot (usually `sonnet`), so trying the profile once proves nothing about the other slots. So after a write, offer to smoke each distinct model the profile names, one at a time:

```bash
wt smoke <model-id> --only claude --tools --hops 3
```

`--tools` checks a real tool call and result round trip; `--hops N` (1-10) chains N dependent reads. Report PASS or FAIL per slot, with the failing row's detail. A failing model is the user's call: offer a swap (step 3) and do not change anything on your own.

A local model is started by smoke. On a terminal, wt then asks whether to stop it; leave that prompt to wt. Run from a shell without a terminal (or with `--json`), wt skips that prompt and the model stays running. So for a local model, either ask the user to run the command in their own terminal (in Claude Code, `! wt smoke …`), or run it yourself and then tell them it is still running and offer `wt stop <model-id>`. Unload each local model before smoking the next one, so a memory failure belongs to the model under test.

## Slot guidance

- **`haiku` is the fast, cheap slot**: background titles and summaries, and haiku-tier subagents. Use a tier floor, not "cheapest wins", because a weak model's summaries mislead the main model. The floor this skill uses: a rated model, with tool calling not ruled out, and a warning to the user when its score is more than 10 points under the `sonnet` slot model's. State those numbers when you apply them. Tool calling must work; smoke it (step 6).
- **A local model suits `haiku` best; the other slots are cloud.** Say the costs before the user picks one:
  - memory: tens of GB resident while it runs;
  - load time: wt starts it at launch when it is not running, and the first request waits for the load;
  - the context cap: `CLAUDE_CODE_MAX_CONTEXT_TOKENS` (step 4) when it is a slot the session may run on;
  - queueing: when the same local model also serves `sonnet`, background requests wait behind the main session;
  - point `REMEMBER_MODEL` at a cloud model.
  A profile may name only one distinct local model. For an mtplx model on a slot that gets Claude Code's full prompt, see "A local slot model and memory" in `wt/docs/wt-agents/slot-profiles.md`.
- **`opusplan` needs both `opus` and `sonnet` filled.**

## Rules

- Never edit `slot-profiles.toml` by hand, and never write it with anything but `wt profile slots`. If the user asks for something those commands cannot do (an `args`, `wrapper` or `config_content` change, a comment edit), say so and leave the hand edit to them.
- Every write is shown and confirmed first. Removals are confirmed and run one profile at a time; review proposals are presented one profile at a time.
- Open weights first and tier score first; price second.
- Say plainly when confidence is low, a model is unrated, tool calling is unknown, or the tiers are stale. Never present a subscription price as comparable with a per-token one.
- Model names, counts and scores come from the commands at the time you run them. Never write them into repo docs, and never state them from memory.
- Nothing routes on a tier; this skill only edits the file. It does not run `wt cloud-sync`, add registry models or change LiteLLM routes; suggest those instead when a model the user wants is not in the registry (`wt model add`, guide 02).
