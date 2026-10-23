# Open issue backlog: fix-first design (2026-10-23)

Covers the eight open issues from the 2026-10-23 review of
`https://github.com/ohanaverse/local-ai-setup/issues`: #449, #447, #432, #423, #405, #403, #399, #389.

Parked for a later pass (owner decisions still open): #405 (context-window auto-inject, feature) and
#432 (per-model mtplx serve flags, feature/design). Out of scope here: live issues that closed since
the review, and the local `issues.md` follow-ups that have not yet become live GitHub issues (#6, the
`piCompat` deferred item).

Companion deep-dive: `docs/superpowers/specs/2026-10-23-wt-invisible-family-design.md` (#423) is the
per-issue design that the fix-first order puts first among the bugs.

## Intent

Fix the open backlog in severity order — safety and correctness bugs first, maintenance next, then
existing-behavior fixes — before any net-new feature is picked up.

## What is wrong today

- Eight open issues, mostly wt / llmbench / modelman, in three shapes: bugs, a maintenance follow-up,
  and two parked features.
- #399 — omlx keeps generating abandoned requests after the client exits; held memory then makes
  omlx refuse new prompts (`400 prefill_memory_exceeded`). Clearing requires a proxy restart that
  disturbs every session.
- #423 — a provider family with no registry footprint is invisible to the start's others check, so a
  start can silently double-load beside a serving-but-unregistered family (the #412 class, still
  reachable).
- #403 — omlx Qwen3.8-27B answers are padded checklists and score 5–8 points below ollama / mtplx on
  planning; 2026-10-21 probe results shifted the lead, so this is investigation, not a fix yet.
- #449 — cloud-sync plans removals for OpenRouter rows that were never going to be listed, and
  kept `:variant` rows are not repriced, so their price goes stale.
- #447 — `openrouterGate` nearly duplicates `ollamaGate`; maintenance debt only.
- #389 — picker shows the old PR when a branch name is reused after its PR merged.

## Goals / non-goals / anti-goals

Goals:

- No safety or correctness bug stays open while a parked feature is being built.
- Each fix in scope ships with tests and docs.

Non-goals:

- No new features in this pass (#405, #432 stay parked).
- No changes to backends or suites beyond what a fix requires.

Anti-goals:

- No scope creep into new provider families, new benchmarks, or new surfaces.

Constraints:

- #432's design constraints are documented in the issue itself (wt-owned flags must not be
  overridable; positional `isMtplxServer`; `--model-id` vs turbo; registry contract).
- #405's data splits cloud from local: tiers.toml carries `context` for cloud only, local builds are
  deliberately unknown.
- #399's investigation boundaries: where the cancel is lost (Claude Code → LiteLLM → omlx).

## Approaches considered (the plan shape)

1. **Fix-first backlog plan** (picked): one plan covering all eight open issues, ordered by bug
   severity, with the two feature asks parked behind the fixes.
2. Slice-first: pick one or two fixes, ship, then re-plan the rest.
3. Per-issue designs: one design doc per open bug, then a separate plan for the deferred features.

Picked: 1. It matches the owner's stated priority and keeps the backlog reviewable in one place; the
per-issue deep-dives already written (#423) stay as the fix's design.

## Decisions (mine, pending confirmation)

- Adopt the fix-first backlog plan (approach 1).
- Proposed severity order for the fix pass: #399, #423, #403, #449, #389, #447; #405 and #432 parked.
- #449's acceptance requires choices for its open questions (price from base id vs leave alone; alias
  detection), so the fix pass may need owner decisions inline.

## Delivery (proposed)

1. **#399** — find where the client-exit cancel is lost and wire it; narrower clear path if found.
2. **#423** — start-path-only unknown signal for an answering-but-unprobed family; keep the stop
   picker consistent; tests.
3. **#403** — repeat the probes twice more; add sampling-penalty flags to `probe thinking`; draft the
   upstream mtplx report; decide fix vs. accept.
4. **#449** — chosen repricing/alias behavior + dropped-suffix handling; e2e coverage.
5. **#389** — stale-PR tag on reused branch names.
6. **#447** — shared gate helper for ollama / openrouter removal gates.

## Testing

- Per-issue tests, as each issue already sketches: #449 `PlanPrices` + end-to-end through
  `cloud-sync --yes --approve-removals`; #423 `TestResidentOthers` + the inventory family-set test.

## Out of scope

- #405 and #432 (parked features, owner decisions open).
- #399's investigation beyond the cancel path.
- The `piCompat` tracker item in `issues.md` (local follow-up, no live issue).

## Calls made by the author (not the owner)

- Ranked the fix-first pass as safety bugs (#399, #423) first, investigation (#403) next, then
  #449, #389 and the #447 maintenance item.
- Parked the two features behind the fixes.

## Amendments after implementation

- None yet.