# Plan: fix PR #29 cost/tier review findings

**Date:** 2026-08-30  
**Branch:** `feat/cost-tier-tui`  
**Goal:** Address the 8 verified findings from the code review of PR #29, plus one closely related regression (unset cost promoted to `free`) that multiple review angles flagged.

## Verified findings

| # | File:line | Severity | Summary |
|---|-----------|----------|---------|
| 1 | `registry.py:379` | Crash / data corruption | Cost numeric prices are loaded without type validation; a string price crashes `_format_cost`. |
| 2 | `forms.py:448` | Silent data loss | Subscription periods other than `month`/`year` are silently replaced with `month` on edit-save. |
| 3 | `models.py:597` | Data-integrity regression | Same-family edits call `save_registry` immediately, but *Discard* only restores in-memory state, so the registry file keeps the edited cost/tier. |
| 4 | `registry.py:380` | Garbage data propagation | Any `kind` string is accepted and displayed verbatim in the COST column. |
| 5 | `models.py:341` | UX regression | Missing/empty `location` renders as local (`▤`) instead of unknown (`—`). |
| 6 | `registry.py:410` | Garbage data propagation | `usage_tier` accepts arbitrary types; booleans/numbers render as `True`/`1`. |
| 7 | `models.py:146` | Contract break | `_model_entry_to_variant` passes a non-JSON-serializable `Cost` dataclass in the provider-facing `VariantSpec`. |
| 8 | `tests/screens/test_forms.py:1067` | Style / docs | New test functions lack docstrings/comments explaining what they cover. |

## Related regression (not in final ranked list, but flagged by two angles)

| | File:line | Summary |
|---|---|---|
| 9 | `forms.py:431` | `ModelForm` defaults cost kind to `free` in edit mode, so a model whose registry cost is `None` is silently upgraded to `Cost(kind="free")` on a no-touch save. There is no test covering this case. |

## Implementation approach

### Item 1: Harden registry cost parsing (`registry.py`)

In `_parse_model`, validate `cost_raw` before constructing `Cost`:

- `cost_raw` must be `dict` or `None`; otherwise raise `RegistryError`.
- `kind` must be one of `"free"`, `"per_token"`, `"subscription"`; otherwise raise `RegistryError`.
- `price_per_million_tokens` and `price_per_period` must be `int | float | None` and finite; otherwise raise `RegistryError`.
- `period` must be `str | None`; otherwise raise `RegistryError`.

This fixes findings #1 and #4 and prevents garbage from entering the registry.

**Tests:** add to `tests/test_registry.py`:
- `cost = "free"` (string) raises.
- `cost.price_per_million_tokens = "abc"` raises.
- `cost.kind = "enterprise"` raises.
- `cost.price_per_million_tokens = float("inf")` raises.
- Valid numeric prices still round-trip.

### Item 2: Preserve custom subscription periods (`forms.py`)

The period `Select` currently only offers `month`/`year`. When editing a model whose stored `cost.period` is outside that set (e.g. `"week"`, `"quarter"`), add the stored value as a temporary option so it is preserved on a no-touch save. The user can still switch to `month` or `year` explicitly.

**Tests:** add a TUI test that opens `ModelForm` for a model with `cost.period = "quarter"`, submits without touching the period field, and verifies the dismissed spec keeps `"quarter"`.

### Item 3: Make *Discard* revert the registry file (`models.py`)

In `_on_exit_confirm`, when `choice == "discard"`, call `save_registry(self.registry, self.registry_path)` after `_restore_snapshot()`. This reverts on-disk registry metadata to the mount-time snapshot, matching the in-memory state restoration.

**Tests:** add a TUI test that edits a model's cost/tier, queues another action, presses Escape, chooses *Discard*, and asserts the registry file no longer contains the edited cost/tier.

### Item 4: Render unknown location as em dash (`models.py`)

Change the LOC renderer so:
- `m.location is None` or empty string → `—`
- `m.location == "cloud"` → `↗`
- `m.location == "local"` → `▤`
- any other value → render the raw value (surfaces unexpected data instead of hiding it)

**Tests:** add formatter/row tests for `None`, `""`, `"cloud"`, `"local"`, and an unexpected string.

### Item 5: Type-validate `usage_tier` at registry load (`registry.py`)

In `_parse_model`, validate that `usage_tier` is `str | None`; reject booleans, numbers, lists, etc. with `RegistryError`.

> **Spec note:** `docs/superpowers/specs/2026-08-30-modelman-cost-tier-tui-design.md` says the field stays `str | None` at parse time so new tier names do not break existing registries. We therefore validate *type* but not *value* — arbitrary string tiers are still accepted for forward compatibility.

**Tests:**
- `usage_tier = true` raises.
- `usage_tier = "premium"` round-trips (forward-compatible string).
- `usage_tier = "high"` round-trips.

### Item 6: Serialize `Cost` as a plain dict in `VariantSpec` (`models.py`, `providers/base.py`, `sync.py`)

`VariantSpec` is the provider-facing contract. Passing a `Cost` dataclass breaks any provider that JSON-serializes the spec.

- In `_model_entry_to_variant` (`screens/models.py`), emit `cost` as a plain dict using the existing `_cost_to_dict` helper.
- In `_variant_to_model_entry` (`screens/models.py`), accept either a `Cost` object or a `dict` and normalize to `Cost` before building `ModelEntry`.
- In `ModelForm.compose`, normalize a dict cost back to a `Cost` object for prefill, or adjust the prefill logic to read dict fields directly.
- In `sync.py`, decide whether to mirror the dict conversion or omit `cost`/`usage_tier` entirely from the sync variant. Since providers do not consume these fields, omitting them is simpler and makes the sync adapter's provider-only contract explicit. Add a docstring note explaining the difference from the UI adapter.

**Tests:**
- `_model_entry_to_variant` returns a plain dict under `"cost"`, not a `Cost` instance.
- `_variant_to_model_entry` round-trips a dict cost.
- `ModelForm` edit prefill works with the new dict shape.

### Item 7: Validate cost prices in `parse_cost_fields` (`forms.py`)

Reject negative, NaN, and infinite prices in `parse_cost_fields`. A price must be a finite, non-negative number.

**Tests:**
- `"-2.5"` raises `ValueError`.
- `"inf"` / `"nan"` raise `ValueError`.
- Valid positive prices still work.

### Item 8: Preserve unset cost in `ModelForm` edit mode (`forms.py`)

Add an explicit "unset" option to the cost-kind `Select` so edit mode can represent `cost = None`. The option is only relevant for edit mode; add mode may keep `free` as the default, matching the spec.

Approach:
- Add an option with label `"—"` / value `""` (or similar sentinel) to the kind `Select`.
- In edit mode, pre-select the sentinel when `variant["cost"] is None`.
- In `_submit`, if the sentinel is selected, set `cost = None`.
- In add mode, default to `free` (existing behavior).

**Tests:**
- Edit a model with `cost=None`, submit without touching cost, verify the dismissed spec has `cost=None`.
- Add mode still defaults to `Cost(kind="free")`.

### Item 9: Add docstrings/comments to new tests (`tests/screens/test_forms.py`)

Per `CLAUDE.md` Test Documentation rules, add a one-to-two-sentence docstring to each new test added by PR #29 that currently lacks one. Focus on the ones flagged:

- `test_price_str_whole_number_drops_decimal`
- `test_price_str_binary_inexact_uses_shortest_round_trip`
- `test_parse_cost_fields_free`
- `test_model_form_cost_section_per_token_shows_price_input`
- `test_variant_to_model_entry_passes_through_cost_and_tier`
- `test_format_cost_none`
- `test_registry_round_trips_usage_tier`

(Other nearby new tests should also get docstrings if they lack them.)

## Out of scope

- UI redesign beyond fixing the listed bugs.
- New cost kinds, currencies, or usage tiers.
- Broad refactor of the sync mirror unless it falls naturally out of Item 6.

## Verification

1. Run `make test` — all tests pass.
2. Run `make check` — lint, format, and typecheck pass.
3. Optionally run the TUI manually and verify: edit a model with no cost, save, and confirm the COST column still shows `—`.

## Rollout order

1. Items 1, 5 (registry validation) — establishes the data invariants the rest of the code can rely on.
2. Items 4, 6, 7 (screen/model adapter and formatter fixes) — depend on validated registry data.
3. Items 2, 8 (form round-trip fixes) — depend on adapter shape and validation.
4. Item 3 (discard reverts file) — builds on the snapshot/restore behavior.
5. Item 9 (test docstrings) — last, cosmetic but required by project rules.
