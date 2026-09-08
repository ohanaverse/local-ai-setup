# Native-Provider Invariant Hardening (modelman) — Design

**Date:** 2026-09-15
**Issues:** #47, #48
**Scope:** `modelman/src/modelman/litellm.py` + tests only.

## Problem

Two unenforced/ambiguous rules around native providers in `litellm.py`:

1. **#48 — Override vs native precedence.** `is_effectively_exposed`
   short-circuits on native *before* consulting `exposed_override`, so an
   explicit `exposed_override=False` cannot hide a native model. Neither the
   behavior nor the intended precedence is documented or test-pinned.
2. **#47 — native ⇒ no LiteLLM policy invariant.** A provider with
   `auth.type = "native"` must not have a `PROVIDER_POLICIES` entry. This is
   enforced only by convention: a hand-edited registry can pair a native
   provider with a policy, producing display/apply inconsistency (the
   EXPOSED column ignores the flag the apply path writes).

## Decisions

- **#48:** Native is *unconditionally* exposed. `exposed_override` applies
  only to non-native models. Documented in the docstring and pinned by tests.
- **#47:** Enforced at the litellm boundary (the apply gate), not at
  registry load. `load_registry` stays decoupled from `PROVIDER_POLICIES`.

## Change 1 — `is_effectively_exposed` native precedence (#48)

- Keep the `if model.native: return True` short-circuit first.
- Docstring: state explicitly that `exposed_override` (and the persisted
  flag) are consulted only for non-native models; native models always
  return True regardless of overrides.
- Tests:
  - native model + `exposed_override=False` → True
  - native model + `ready_override=False` → True
  - non-native + `exposed_override=False` (persisted true) → False
    (pins the override path for the non-native case)

## Change 2 — native ⇒ no policy, enforced at the apply gate (#47)

- In `_validated_entry`, add an early guard: if the model is native, raise
  the existing native-provider `ExposeError` ("native providers cannot be
  exposed through LiteLLM") *even when* a `PROVIDER_POLICIES` entry exists
  for the provider.
- `provider_policy()` remains a pure table lookup (no native check) — the
  guard lives at the write gate where the error message and TUI handling
  already exist.
- Effect: display (`is_effectively_exposed`) and apply (`_validated_entry`)
  are consistent by construction; a stale `PROVIDER_POLICIES` entry for a
  native provider is dead config that can no longer cause harm.
- Tests:
  - native model whose provider has a `PROVIDER_POLICIES` entry:
    `expose_model` raises `ExposeError`
  - `apply_expose_queue` with the same queued expose: rejected identically
  - non-native behavior unchanged (existing tests cover)

## Docs

- Update the `litellm.py` module/docstring claim that `PROVIDER_POLICIES`
  is "the single source of truth for provider-specific exposure rules":
  note native providers are exempt by rule, enforced at the apply gate.
- No `docs/guides/` changes (the invariant is not guide-documented).

## Out of scope

- #46 (Python/Go `is_cloud_effective` provider-location inheritance
  divergence) — separate issue and decision.

## Verification

- `make check` (ruff + mypy) and full pytest in `modelman/`.
- No changes to wt, guides, or registry loading.
