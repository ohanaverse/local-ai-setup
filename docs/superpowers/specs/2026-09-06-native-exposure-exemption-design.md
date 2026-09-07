# Design: Native-Provider Exposure Exemption (#41)

Date: 2026-09-06
Issue: #41
Context: `modelman` / `wt` shared exposure predicate.

## Goal

Resolve the native-provider exposure divergence between `wt` (Go) and `modelman`
(Python). Both tools are documented to apply the same rule — "a model whose
provider authenticates natively is always exposed" — but only `wt` actually
implements it. Fix `modelman` to match, and pin the rule as a shared
cross-language contract so the two cannot drift again.

Also close the remaining structural debt from #29: confirm the ready-rule already
has a single source of truth, and document why the apply/routing gate intentionally
does **not** route through the display predicate.

## Current State

The exposure rule is documented identically in three places:

- `docs/guides/00-config-map.md:61` — "...iff `litellm_exposed = true` AND
  (`ready = true` OR `location = "cloud"`). Native models (provider
  `auth.type = "native"`) are always exposed — they cannot route through
  LiteLLM. Both `wt` and the TUI apply this same rule, so a model offered by
  `wt` always shows `Y` in the TUI's EXPOSED column."
- `modelman/CLAUDE.md:67` — carries both sentences ("litellm_exposed = true AND
  (ready = true OR location = cloud)" and "Native models are always exposed").

But the code diverges:

- **`wt` implements it**: `deriveNative` (`wt/internal/config/config.go:348`)
  marks each model whose provider has `auth.type == "native"` as `Native`;
  `IsExposed` (`config.go:367`) short-circuits `if m.Native { return true }`.
  Pinned by `TestIsExposedPredicate` (`modelman_test.go:247`) and
  `TestLoadExposesOnlyLitellmExposedModels` (`modelman_test.go:122`).
- **`modelman` omits it**: `is_effectively_exposed` (`litellm.py:134`) returns
  `exposed AND passes_ready_gate(...)` with no native exemption. A native model
  with `litellm_exposed=false, ready=false` renders `–` in the TUI's EXPOSED
  column while `wt` still offers it.

This was found by code review on #40; intentionally deferred as a behavioral change.

### Two predicates, deliberately distinct

During exploration, a subtlety emerged that shapes the design. There are two
different predicates, and they must not be merged:

1. **Display/catalog predicate** — `is_effectively_exposed` (TUI EXPOSED column,
   wt `IsExposed`): "does this model show up as available?"
2. **Apply/routing gate** — `passes_ready_gate` / `_validated_entry` (expose-time
   LiteLLM config write): "can this model be written into LiteLLM's config now?"

The apply gate must **not** adopt the native exemption or route through
`is_effectively_exposed`, because:

- Native/agent providers have **no LiteLLM mapping** (not in
  `PROVIDER_POLICIES`); `_validated_entry` already rejects them earlier via the
  `policy is None` check. Granting them "exposed" status would attempt to route
  them through LiteLLM — wrong.
- `is_effectively_exposed` requires `litellm_exposed = true`, but an expose
  operation's job is precisely to set that flag to true. An apply gate cannot
  require the flag it is about to flip.

The ready-rule already lives in a single function (`passes_ready_gate`), and
both paths already route through it: display via `is_effectively_exposed`, apply
via `_validated_entry` (litellm.py:482, which `apply_expose_queue` and the CLI
delegate to). So #29's code-side goal is already achieved; the apply paths do
**not** need to be rewritten to use `is_effectively_exposed`.

## Design

### 1. Derive native-ness onto `ModelEntry` (mirror wt)

Add a derived `native: bool = False` field to `ModelEntry`
(`modelman/src/modelman/registry.py:132`), populated by a post-load pass that
mirrors wt's `deriveNative`:

```python
# registry.py
def _derive_native(registry: Registry) -> None:
    """Mark each model whose provider authenticates natively
    (auth.type == "native") as native. Mirrors wt's deriveNative.
    Runs after providers and models are parsed so the registry is the
    single source of truth for native-ness."""
    native_ids = {p.id for p in registry.providers if p.auth.type == "native"}
    for m in registry.models:
        m.native = m.provider_id in native_ids
```

Call `_derive_native(registry)` at the end of `load_registry`
(`registry.py:334`), after both providers and models are parsed.

Rationale for a post-load pass over wiring each constructor: `ModelEntry` is
constructed in several places (registry parse, TUI form adapter, snapshot
copies, migrate). A single derivation pass centralizes native-ness in one spot,
matches wt's source-of-truth pattern, and lets snapshot copies / form / migrate
paths inherit registry behavior without each implementing its own native logic.

`_model_to_dict` (`registry.py:429`) uses an explicit whitelist, so the derived
`native` field is naturally **excluded from persistence** — nothing to change to
keep it from leaking into saved registry files.

### 2. Apply the exemption in `is_effectively_exposed`

`modelman/src/modelman/litellm.py:134`:

```python
def is_effectively_exposed(
    model: ModelEntry,
    state: StateStore,
    exposed_override: bool | None = None,
    ready_override: bool | None = None,
) -> bool:
    ...
    if model.native:
        return True   # native models bypass LiteLLM; always effectively exposed
    exposed = (
        exposed_override if exposed_override is not None else state.get(model.id).litellm_exposed
    )
    if not exposed:
        return False
    return passes_ready_gate(model, state, ready_override=ready_override)
```

Update the docstring to name the three conditions: native OR (flag AND
(ready OR cloud-exemption)).

### 3. Do NOT touch the apply/routing gate

`_validated_entry`, `passes_ready_gate`, `apply_expose_queue`, and the TUI queue
invariants all stay as-is. The native exemption is a **catalog** property only.
Optionally add a clarifying comment on `_validated_entry` / `passes_ready_gate`
noting they deliberately govern routing (LiteLLM-writable, flag-flippable) and
are distinct from the display predicate. This is the #29 closure.

### 4. Pin the rule as a shared contract

Extend `docs/contracts/registry.sample.toml` with a native provider and model
(**no** `model_state` row — the strongest case, "native is exposed even when
unflagged"):

```toml
[[providers]]
id = "agy"
name = "Agy"
location = "local"
[providers.auth]
type = "native"

[[models]]
id = "agy/contract-fixture:native"
family = "contract-fixture"
provider_id = "agy"
model_name = "contract-fixture:native"
tags = ["native"]
```

This makes the fixture exercise "all provider auth types" (none, api_key,
native) — matching its own header comment.

## Verification Plan

1.  **Python unit tests** (`modelman/tests/test_litellm.py`):
    - Add `test_is_effectively_exposed_native_not_exposed_not_ready`: a native
      model with `litellm_exposed=False, ready=False` → `True`.
    - Assert the existing 4 `test_is_effectively_exposed_*` cases still pass
      unchanged (they use non-native `ollama`/`openrouter` models, so the native
      branch does not affect them).
2.  **Python contract test** (`modelman/tests/contracts/test_registry_fixture.py`):
    - provider count 2→3, model count 2→3.
    - assert `agy.auth.type == "native"` and the agy model's `native` is `True`.
3.  **Python registry tests** (`modelman/tests/test_registry.py`):
    - `load_registry` derives `native` from provider auth; non-native model stays
      `False`.
    - `_model_to_dict` / `save_registry` does **not** persist `native`.
4.  **Go contract test** (`wt/internal/config/registry_fixture_test.go`):
    - Add `TestRegistryFixtureNativeExposure`: load the shared fixture, assert
      the agy model is `Native` and `cfg.IsExposed(...)` is `true` with no
      `model_state` row in the fixture. This is the cross-language pin — the
      same fixture the Python contract test reads.
5.  **Docs**: `00-config-map.md:61` and `modelman/CLAUDE.md:67` need no text
   edits — the fix reconciles the two sentences they already carry (the tension
   existed only because the code omitted the exemption). Note this in the PR
   body.
6.  **Regression**: run `make test-all` (lint + modelman check/test + wt
   build/vet/test).

## Scope / Non-Goals

- **This spec creates the exemption and pins the contract.** A follow-up plan
  (via `superpowers:writing-plans`) will produce the implementation plan and PR.
  This design doc is saved/committed as the canonical record of #41.
- **No behavioral change to the apply gate** — native models remain unreachable
  through LiteLLM (correctly rejected by `_validated_entry`'s `policy is None`
  check). Only the *catalog* representation changes.
- **No migration** — `native` is derived, not persisted.