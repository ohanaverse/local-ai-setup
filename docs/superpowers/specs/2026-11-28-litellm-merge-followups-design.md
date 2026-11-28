# LiteLLM config reads: the three PR #759 follow-ups (#760, #761, #762)

Date: 2026-11-28. Size: a small change, one PR. Closes the follow-ups filed by the `/code-review --fix` pass on #759; `#758` (the false affinity alarm) was fixed by #759 and is closed as housekeeping.

## Intent and why

`wt` writes `router_settings` (session affinity) and `general_settings` (`cancel_on_disconnect`, `store_model_in_db`) into `~/.config/litellm/config.yaml` only where a key is absent, and warns when it cannot. #759 made those warnings conditional on what the shared block actually holds. Three gaps remain where wt either reads a block differently from LiteLLM's YAML loader (PyYAML) or says nothing. Two of them let wt write a key over a value the proxy sees as the user's opt-out, so they are correctness bugs, not wording.

## The three changes (all in `wt/internal/litellm/configfile.go`)

**#760: `<<` aliasing a list.** `mergedGetDepth` and `mergeAmbiguous` test whether a merge value is a sequence before unwrapping an alias, so `<<: *overrides` (an anchored list of mappings) is never searched. Fix: resolve the merge value with `aliasValue` first, in both functions (one shared `mergeSources` helper so they cannot drift again). The same reads feed the ollama `api_base` repair, `cancel_on_disconnect` and the subscription-hazard checks, so all of them gain correct reads; no caller changes.

**#761: literal block with repeated `<<`.** PyYAML takes the last of several `<<` keys, `mergedGet` the first. For a *literal* `router_settings` or `general_settings` that has repeated merge keys, wt can print a false "affinity is off", stay silent when it is really off, and write a default over a value the proxy sees. Decision: treat such a block as unreadable, the same as a shared block already is (fail closed): write nothing, say nothing about the checks list, print the skip warning with a new reason, "is read through repeated merge keys (<<)". Block-level, not per key: the fix for the user is to fold the merges into one list (`<<: [*a, *b]`). `general_settings` follows the same rule through `cancelOnDisconnectSkipped`.

**#762: literal non-mapping block.** `router_settings: 42` (or a list) is skipped with no message. Decision: warn, with the existing skip-warning wording and the reason "is not a mapping", for both `router_settings` and `general_settings`. The `general_settings is not a mapping` branch in `dbRoutesOffWarning` becomes dead (the new skip case fires first, byte-identical text) and is deleted.

## Approaches

1. Fix all three, fail-closed (chosen). Small, no new mechanism: the skip predicates gain two cases and the merge walkers one unwrap.
2. Record #761 and #762 as spec-intended silence and fix only #760. Rejected: #761 leaves a write over a proxy-visible opt-out, and the repo rule is that a check that must not call a protection on treats an unreadable block as unreadable.
3. Per-key skip for #761 (stay quiet when all three keys are written explicitly). Rejected: it needs the key set threaded through both skip predicates for a duplicate-key config nobody should have. A false-alarm warning that says how to fix it is cheaper.

## Goals, non-goals, anti-goals

Goals: the three reads match PyYAML; no write lands in a block wt cannot read; every skip says why.

Non-goals: no change to which keys wt writes; no change to the no-warning-when-keys-present rule from #759; no hardening of `store_model_in_db` beyond what `cancelOnDisconnectSkipped` already gives it.

Anti-goals: never overwrite a value a merge supplies; never claim affinity is off when wt cannot tell.

## Tests and docs (moves together)

Tests in `configfile_test.go` (and `service_test.go` for `PlanSync`): alias-of-list reads for block and root merge in both blocks; the #760 scenario (`repin: false` supplied by an aliased list is not overwritten, with a control); literal repeated-merge block, both orders, plus a source that itself has two merges, for both blocks (the existing fail-closed table extends); literal `42`, `[a, b]` and `plain` for both blocks; null and mapping blocks stay silent and are filled; `dbRoutesOffWarning` text unchanged after the branch is deleted.

Docs: this spec's sibling `2026-11-28-litellm-model-groups-step-b-design.md` (the Block bullet), `docs/guides/04-litellm-config.md` §7 and the cancel_on_disconnect bullet, `wt/docs/internals/litellm-routes.md`, the `EnsureSettings` paragraph in `wt/CLAUDE.md`, `wt/CHANGELOG.md`. The PR body uses `Refs #N` only where an issue must stay open; #760-#762 are meant to close, so it says `Fixes` for them and nothing else.

## Left to the builder

Exact warning constant names, the order of the new switch cases (alias first, since an alias is also non-mapping), and whether the `mergeSources` helper lives beside `aliasValue` in `dburl.go`.
