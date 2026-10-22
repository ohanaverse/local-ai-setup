# `wt cloud-sync`: remove vanished OpenRouter models; keep hand-added models until priced

Date: 2026-10-22. Package: `wt` (`internal/cloudsync`, `internal/config`,
`internal/modeladmin`, `cmd/wt`), plus `docs/contracts/` fixtures and
`wt/docs/wt-cloud-sync.md`, `wt/docs/wt-model.md`.

## Intent and why

`wt cloud-sync` mirrors two public sources into `registry.toml`: OpenRouter's
model list and ollama.com/pricing. Two gaps:

1. **OpenRouter never removes.** A model OpenRouter stops listing only gets
   `warning: No OpenRouter match for …` (`PlanPrices`,
   `internal/cloudsync/openrouter.go`) and stays in the registry forever,
   still routed. The user wants it removed.
2. **ollama removes what the page does not list.** Some ollama cloud models
   are free and absent from ollama.com/pricing (example:
   `ollama.com/library/beam`). A user who adds one with `wt model add` has it
   deleted, and its tag `ollama rm`'d, on the next sync.

The user's model of the world: *a free model eventually becomes priced and
never goes back to free.* So a hand-added model is protected only until the
source lists it; after that it is an ordinary synced model.

## Decisions (the user's)

- Removal of a vanished OpenRouter model is part of `cloud-sync`.
- **All removals are gated by a prompt**, in both flows: interactive
  confirmation, or `--yes --approve-removals <digest>` without a terminal.
- A hand-added model is added with `wt model add` and is not removed by
  `cloud-sync` for being off the pricing page.
- It stays protected until it appears on the source (ollama pricing page /
  OpenRouter list). Then it becomes `curated` and is subject to normal
  removal if it later disappears.
- Existing hand-added entries are marked with `wt model edit <id> --source
  manual`. No automatic migration.

## Design

### The `manual` source

`config.Source` (`internal/config/config.go`) has `curated` and `discovered`.
Add `manual`. It means: "added by hand; not yet seen on its cloud-sync
source."

- `wt model add` of a cloud model that either flow would cover (provider
  `openrouter`, or provider `ollama` with `location = "cloud"`) writes
  `source = "manual"`. Other adds are unchanged.
- `wt model edit <id> --source manual|curated` sets it (at minimum `manual`;
  accepting `curated` too is the builder's call). `discovered` is not
  settable by hand.
- Registry validation, the fixtures in `docs/contracts/` and any reader that
  switches on `Source` accept the new value.

### Graduation

When a sync matches a `manual` entry to the source (ollama: the page lists it
or wt can match it to a listed name; openrouter: the id is in the list), the
plan re-prices it as today **and** flips `source` to `curated`. The plan
shows it: `<id>: manual -> curated`. It is a registry change, so a sync that
only graduates entries counts as having changed something.

### Protection while `manual`

A `manual` entry is never a removal candidate in either flow:

- ollama: not removed, not re-tagged, its tag never given to `ollama rm`, and
  not pulled-then-removed as a stray. It still gets `ollama pull` if its tag
  is missing from `ollama list`, as any entry does. (Builder: confirm this
  reads right against the existing pull rules; if it conflicts, say so in the
  plan.)
- openrouter: not removed when absent from the list.
- It does not count toward the mass-removal threshold, in the numerator or
  the denominator.
- When it is off the source the plan says so on one line, e.g. `kept
  (manual): ollama/beam:cloud is not on ollama.com/pricing`, so it does not
  look forgotten. For openrouter this replaces the "No OpenRouter match"
  warning for manual entries.

### OpenRouter removals

For OpenRouter-priced entries (provider `openrouter`, same scope as
`OpenRouterPriced`) that are `curated`/`discovered` and **absent** from the
fetched list: plan a removal.

- "Absent" means the id is not in the API response at all. A model listed
  with no price, a negative or unreadable price, or time-of-day pricing wt
  cannot read still **exists**; it keeps today's warnings and is left alone.
- Plan section: `openrouter: Registry removals (N):`, one line per id, a
  removal digest line like the ollama one.
- **Fetch safety:** if the fetch fails, or the response lists no models (or
  fewer than a sanity floor the builder picks and documents), plan no
  removals, say why, and exit as the flow does today for a failed fetch. An
  outage must never read as "everything is gone."
- Mass removal: more than half of the OpenRouter-priced, non-manual entries
  planned for removal is refused like ollama's (`--force` overrides). Exit
  code for the openrouter refusal is the builder's call; codes 2–5 today are
  ollama-only, so extending them to name the flow, or adding codes, must be
  documented in the exit-code table and the `cloud-sync` skill.
- Applying a removal deletes the registry entry; LiteLLM routes follow
  through the existing route sync (routes are derived, never stored).
  Anything else that references a model id (rotation state, slot profiles,
  `last-picked`) is handled however the ollama removal path already handles
  it; the builder checks and mirrors it.

### One approval, one digest

The two flows currently gate separately and the digest covers only ollama's
removals. Extend so the user approves all removals once: the printed digest
covers ollama registry removals, stray `ollama rm`s and openrouter removals;
`--yes` still requires `--approve-removals <digest>`; a stale digest (the
sources changed since the dry run) is refused as today. Whether this is one
combined digest or one per flow is the builder's call, but a single
`--yes --approve-removals X` invocation must be able to apply a reviewed run
of both flows, and the interactive path asks once.

A refusal in one flow still must not stop the other, as today (the
flows are independent; exit-code precedence rules stay as documented).

## Goals

- A vanished OpenRouter model leaves the registry, with approval.
- A hand-added free ollama model survives every sync until the pricing page
  lists it, then is managed like any other.
- Nothing is removed without a prompt or a reviewed digest.

## Non-goals

- No change to which OpenRouter models are in scope, to price handling, or
  to time-of-day pricing.
- No persistent "pinned forever" state. Protection ends by design when the
  source lists the model.
- No automatic detection of a model returning to free.
- No removal of local models, as today.
- No migration of existing registry entries.

## Anti-goals (a failure even if it works)

- A network or parse failure, or an empty list, causing mass removal.
- A manual entry deleted, or its tag `ollama rm`'d, before graduating.
- `--yes` removing anything without the digest.
- `source = "manual"` leaking into behaviour other than cloud-sync removal
  protection (pricing, routing, picker order, tiers).

## Constraints

- Follow `wt/CLAUDE.md` and root `CLAUDE.md`: registry is written only
  through `config.UpdateRegistry`; guides never embed live model state;
  contract fixtures (`docs/contracts/registry*.sample.toml`) change in the
  same change as any schema/validation change; `modeltiers` reads inventory
  only through `wt model list --json`, so if that JSON gains or changes a key
  for source, `model-list.sample.json` and its contract test move too (only
  if `source` is already exposed there).
- Docs updated: `wt/docs/wt-cloud-sync.md` (both flows, removal gate, exit
  codes, `manual` lifecycle), `wt/docs/wt-model.md` (`add` sets `manual`,
  `edit --source`), the `cloud-sync` skill
  (`wt/.claude/skills/cloud-sync/SKILL.md`), `wt/CHANGELOG.md`.
- Ad-hoc runs against a scratch registry set `WT_LITELLM_CONFIG` too.
- Tests: plan-level tests in `internal/cloudsync` (manual kept, graduates,
  openrouter removal, empty/failed fetch plans nothing, mass removal,
  digest), `modeladmin` tests for the add/edit paths, a registry round-trip
  for `manual`.

## Left to the builder

Exact flag spelling for `edit --source`; whether `edit` accepts `curated`;
openrouter sanity floor and mass-removal exit code; combined vs per-flow
digest; plan-line wording beyond the examples; how to treat a `manual` entry
whose tag is missing locally (pull, per existing rules).

## My calls (not the user's), to check

- Reusing `source` for the state rather than a new flag.
- `manual` set only for cloud models of the two flows, not every add.
- Manual entries excluded from the mass-removal count.
- An empty/failed OpenRouter list plans no removals.
- "Absent" (not "unpriced") is the only OpenRouter removal trigger.
- One approval covering both flows.

## Settled after the builder check

Answered by the code or by this design:

- **Removal candidates today** (`PlanCatalog`, `internal/cloudsync/catalog.go`):
  every ollama cloud entry (`isOllamaCloud`) not in `matched` is removed,
  whatever its `source`. The `manual` exclusion goes on that predicate. Stray
  tags are pulled cloud tags that are unlisted and unregistered; a manual
  entry is registered, so its tag is never a stray.
- **Graduation match** is exactly the existing `matched[e.ID]` set (the
  match that today keeps an entry from removal), never a looser one.
- **Listed means present on the source, whatever the price.** An OpenRouter
  model listed at price 0 (free) graduates, as does one with an unreadable
  price. Presence is the signal, not usability.

My calls (minor), mark them as such in the plan:

- **Prompt placement:** unchanged from today. One question shows every
  flow's plan; declining applies nothing. A flow whose removals are not
  approved (no digest, stale digest, mass-removal) changes nothing in that
  flow, as the ollama flow does today; the other flow proceeds. A plan with
  no removals needs no digest under `--yes`.
- **Digest:** computed from the removals each flow plans, not raw state.
  The apply-time re-plan under the registry lock is compared with the
  printed plan; any difference (including an OpenRouter fetch that now
  fails) is the existing "registry/source changed" refusal for that flow.
- **Existing hand-added entries** are not warned about specially. They are
  `curated` to the code, so they show up in the removal list the user must
  approve; a CHANGELOG/doc note tells users to run `wt model edit <id>
  --source manual` first.
- **`edit --source manual`** is accepted only for entries the two flows
  cover (provider `openrouter`, or provider `ollama` with `location =
  "cloud"`); otherwise it is refused with a message saying why.
