# Slot profile management: design

Date: 2026-10-30. Status: draft for approval.

## Intent

Slot profiles (`~/.config/agent-wt/slot-profiles.toml`, format in
`wt/docs/wt-agents/slot-profiles.md`) give each of Claude Code's four model
slots (`fable`, `opus`, `sonnet`, `haiku`) its own registry model. Today an
agent hand-writes each profile. The file now holds six profiles, several of
them one-time tests. New models are released often, so the models in a
profile go stale, and picking the right model for a slot is not
straightforward.

This work gives the user a skill and a `wt profile` toolset to keep the file
healthy: remove profiles that are no longer used, update profiles when
better models appear, and create new profiles with guided, researched model
choices.

## Goals

1. **Clean up.** List profiles that look unused, with evidence, and delete
   the ones the user confirms.
2. **Keep current.** Refresh `modeltiers` when its data is stale, then compare
   each profile's slot models against current tier scores and suggest swaps.
3. **Create and edit.** Guide the user through the four slots and the other
   options, then write the profile.
4. **Provider twins.** Make it easy to hold a pair of similar profiles, one on
   ollama models and one on openrouter models, so the user can switch
   provider by choosing the other row in the picker.
5. **Verify.** Offer to run `wt smoke` on the chosen models before the user
   relies on a profile.

## Non-goals and anti-goals

- No automatic provider failover or routing. The user switches by hand in the
  picker (their decision). Nothing routes on a tier.
- No change to how a profile launches (`launchSlotProfile`, `slot_flow.go`).
- The tools never change a profile without the user's confirmation, and never
  delete one silently.
- No writes to `registry.toml`, `tiers.toml` (except by running `modeltiers
  run`) or the LiteLLM config.
- Failure to avoid: a rewrite that drops the user's comments, or leaves a
  file `wt profile list` reports as broken.
- The guides stay state-independent: no model names or counts embedded in
  docs, only the commands that reveal them.

## Decisions made with the user

- **Tools live in `wt`**, as `wt profile` subcommands. The skill does the
  conversation and the research; `wt` does the file edits and checks. wt
  currently never writes `slot-profiles.toml`; this is the first writer, and
  the docs that say otherwise must change.
- **"Cleaned up" means removing profiles no longer in use.** Some were set up
  for one-off tests.
- **Models need not be identical across providers.** For a twin, show
  models from each provider with similar tier scores and let the user pick
  each side.
- **Primary driver is open models with similar tier scores; secondary is
  price.**
- **Haiku is the fast, cheap slot**, and the most likely place for a local
  model. The user asked me to check these assumptions; the skill's guidance
  below reflects that check.
- **The skill offers `wt smoke`** to verify chosen models.

## Design

### 1. `wt profile` subcommands (Go, in `wt/cmd/wt/profile_slots.go` and a new `internal/` package for the file edit)

Existing: `wt profile list|show`. Added (names are proposals; the builder may
align them with house style):

| Command | Does |
|---|---|
| `wt profile slots add <name> --agent claude --slot fable=<id> ... --default <slot> [--env K=V] [--comment "..."]` | Append a `[[profile]]` entry. Refuses a duplicate name. |
| `wt profile slots set <name> [--slot s=<id>] [--default ...] [--env K=V] [--unset-env K]` | Change fields of one entry in place. |
| `wt profile slots rm <name>` | Remove one entry and the comment block directly above it. |
| `wt profile slots usage` | Per profile: launch count and last launch from `usage.jsonl`, plus the entry's leading comment. JSON with `--json`. |
| `wt profile slots candidates [--slot s] [--near <id>] [--open-only] [--provider ollama,openrouter] [--json]` | Models from the registry ranked by tier score (from `tiers.toml`) with price, filtered to the slot's requirements. |

Rules for every write:

- Back up the file first, using the existing `slot-profiles.toml.bak-<stamp>`
  naming.
- Edit text surgically: change only the targeted entry and keep every other
  byte, including comments (the file is hand-annotated).
- Write atomically, then re-parse with the same parser launch uses and fail
  loudly (restoring the backup) if the result has warnings the old file did
  not.
- Validate slot ids against the registry; refuse an id that is not there.
- Respect `WT_REGISTRY` and the other path overrides like the rest of wt, so
  tests run against scratch files.

### 2. Candidate ranking

`candidates` reads tier data only through the existing `internal/tiers`
reader. It reports for each model: tier, the composite scores `modeltiers
show` prints, confidence, context window, location, and price (output $/M
from the registry; subscription-priced ollama models show the subscription
cost and are flagged as not comparable per token). Models that are
`unrated` are listed last and flagged. A model whose confidence is low is
flagged, never hidden. Ordering: open-weight first, then tier score, then
price.

For a twin, `candidates --provider ollama,openrouter --near <id>` returns the
nearest-scoring models on each provider side by side.

### 3. The skill (`.claude/skills/manage-slot-profiles/SKILL.md`)

A project skill, in the style of `sentinel-failure`. It:

1. **Checks freshness.** Reads the `generated_at` in `tiers.toml`; if older
   than a threshold (default 7 days, the skill's call) or a source is stale,
   offers `uv run --directory modeltiers modeltiers run` (exit 2 means a
   stale snapshot was used, 3 means a sentinel moved and points to the
   `sentinel-failure` skill).
2. **Cleanup pass.** Runs `usage`, presents removal candidates with last
   launch and comment, and removes only the ones the user confirms.
3. **Review pass.** For each remaining profile, compares each slot's model
   with `candidates` and proposes swaps with the reason (tier gain, price).
   Presents proposals one profile at a time; applies only the accepted ones.
4. **Guided create.** Walks the four slots, then options: `default`
   (including `opusplan`), `REMEMBER_MODEL`, the context cap for a local
   model Claude Code does not recognize, and a trailing comment recording
   the date and reason. For a twin pair it builds both profiles, naming them
   `<stem>-ollama` and `<stem>-openrouter` (builder's choice if the user
   prefers another scheme).
5. **Researches when scores are thin.** When `candidates` shows an unrated
   or low-confidence model the user is considering, the skill may search the
   web for release notes and independent benchmarks and present the
   findings as supporting evidence, labelled as such.
6. **Offers verification.** After a write, offers `wt smoke` (see below).

Slot guidance the skill applies (from the assumptions the user asked to
have checked):

- `haiku` is the fast, cheap slot: background titles and summaries and
  haiku-tier subagents. Use a tier floor, not "cheapest wins", because a
  weak model's summaries mislead the main model. Tool calling must work.
- A local model suits `haiku` best, while the other slots are cloud. Costs
  to state: memory (tens of GB), load time, the context cap env var, and
  queueing behind the main session when the same local model serves
  `sonnet` too. Point `REMEMBER_MODEL` at a cloud model.
- `opusplan` needs both `opus` and `sonnet` filled.

### 4. Verification with `wt smoke`

`wt smoke` exercises one model, and a profile launch exercises only the
`sonnet` slot. So the skill smokes each distinct slot model, using
`wt smoke <model-id> --tools [--hops N]` (the flags merged in #503), one at
a time, and reports PASS/FAIL per slot. A local model is started by smoke
and the user is asked about stopping it afterwards, as `wt smoke` already
does; the skill leaves that prompt to wt. A failing slot model is reported,
and the user chooses whether to swap it.

### 5. Docs

- `wt/docs/wt-agents/slot-profiles.md`: replace "hand-written, wt never
  writes it" with the new commands; keep hand-editing valid.
- `wt/CLAUDE.md` and the root `CLAUDE.md`: update the "never written by wt"
  statements and the config-ownership map in `docs/guides/00-config-map.md`.
- A guide section in `docs/guides/` (state-independent) for the
  maintenance workflow.

## Constraints

- Go 1.26.7, house style of `wt/`; new package follows `internal/` layout and
  `tomlw` for TOML writing if it can preserve comments, otherwise a
  line-oriented editor of the entry block.
- Tests use scratch paths (`WT_REGISTRY`, `WT_LITELLM_CONFIG`,
  `MODELTIERS_OUT`); `bin/check-config-dirs-untouched` must still pass.
- No new network dependency in `wt`. Web research happens in the skill only.

## Left to the builder

- Exact subcommand and flag names.
- The comment-preserving edit technique.
- The staleness threshold for `tiers.toml`.
- Output formats of `usage` and `candidates` (table plus `--json`).
- Whether `candidates` reuses the picker's sorting code.

## Facts checked in the code (2026-10-30)

- **Usage history is 30 days.** `internal/usage` prunes events older than
  30 days on every write. A profile launch is recorded as
  `model_id = "profile/<name>"` (43 such rows today). So `usage` can only say
  "launched N times in the last 30 days, last at T"; "none in 30 days" is
  its "unused" signal. That is the signal, not proof: the cleanup pass
  always shows the entry's comment and asks per profile.
- **The parser returns structured results**, not logs: `profiles.ParseSlots`
  gives a store plus `Unknown` keys, and `ValidateSlots` judges each
  profile. The "no new warnings" check compares those lists before and
  after a write. (Launch-time warnings are built in `slotWarnings`; the
  builder reuses the underlying data, not the log text.)
- **Tool calling is a field in `tiers.toml`** (`tool_call`, from models.dev),
  so `candidates` can filter and flag on it in `wt`. It is a hint, not a
  guarantee; `wt smoke --tools` is the real test.
- **Subscription pricing is in the registry** (`subscription_price`,
  `subscription_period` beside `output_price_per_million`), so `candidates`
  can show it.

## Calls made by the author (not discussed with the user)

1. **Twin profiles are two independent entries.** The naming scheme
   `<stem>-ollama` / `<stem>-openrouter` is a convention the skill follows;
   the picker and `wt profile list` do not change.
2. **"Filtered to the slot's requirements" means:** `tool_call = true` is
   required for every slot; an `unrated` model is listed last with a flag;
   `opusplan` requires `opus` and `sonnet` filled (the existing validator
   enforces it).
3. **Haiku floor is skill-side guidance, not Go code.** Default: a rated
   model with `tool_call = true`, and a warning when its intelligence score
   is far below the `sonnet` slot's. The skill states the numbers it used.
4. **Unused = no launch in the 30-day window** (see above); shown with the
   comment, confirmed per profile.
5. **Testing the skill:** a scripted check against a scratch
   `slot-profiles.toml` (the Go tests cover the commands; the skill gets
   one dry-run walkthrough recorded in the plan, no harness).
6. **A comment "belongs" to the entry it sits directly above** (contiguous
   `#` lines with no blank line between them and the `[[profile]]` line).
   `rm` removes those lines with the entry; a blank line ends the block.
