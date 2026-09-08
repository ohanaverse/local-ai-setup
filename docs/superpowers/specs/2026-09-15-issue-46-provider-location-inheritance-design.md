# Provider-Location Inheritance in Exposure Predicates (fixes #46)

Date: 2026-09-15
Status: Approved

## Problem

The two "shared" exposure predicates disagree on provider-inherited cloud
location:

- **Go** `IsExposed` (`wt/internal/config/config.go`) resolves location via
  `ResolveLocation`: model location first, then provider location. A model on
  a `location = "cloud"` provider counts as cloud.
- **Python** `is_cloud_effective` (`modelman/src/modelman/litellm.py`) checks
  only the static `PROVIDER_POLICIES` cloud flag or the model's own
  `location`. It ignores `provider.location`.

Failure scenario: a provider with `location = "cloud"` that is absent from
`PROVIDER_POLICIES`, a model with no explicit location, `litellm_exposed =
true`, `ready = false`. Go `IsExposed` → True; Python `is_effectively_exposed`
→ False. Reachability is low (modelman's apply gate refuses policy-less
providers, so this requires hand-edited state), but the goal is predicate
parity — and no contract test pins provider-location inheritance on either
side. The shared fixture masks the gap: its openrouter model carries an
explicit `location = "cloud"`.

`docs/guides/00-config-map.md` claims "Both wt and the TUI apply this same
rule." Today that claim is false.

## Decision

Make Python inherit provider location the way wt's `ResolveLocation` does
(issue's recommended direction, confirmed with the user). Go is unchanged.
The inheritance applies to **both** the display predicate
(`is_effectively_exposed`) and the apply-time ready gate
(`passes_ready_gate` / `_validated_entry`): one predicate, true parity,
matching how wt treats the world.

The existing docstring note claiming provider location was "deliberately
excluded" is rewritten: that rationale was about native providers, which
`is_effectively_exposed` short-circuits before `is_cloud_effective` is ever
consulted.

## Changes

### 1. `modelman/src/modelman/litellm.py`

- `is_cloud_effective(model, registry)` gains a required `registry: Registry`
  parameter. Returns True when any of:
  1. the provider policy declares cloud (`PROVIDER_POLICIES`, unchanged),
  2. `model.location == "cloud"` (unchanged),
  3. **new:** `registry.provider(model.provider_id).location == "cloud"`.
- Unknown provider (`KeyError` from `registry.provider`) is treated as
  not-cloud — conservative, mirroring `is_cloud`'s fallback for unknown
  policies.
- `passes_ready_gate(model, state, registry, ...)` and
  `is_effectively_exposed(model, state, registry, ...)` gain the same
  required parameter and thread it through.
- Callers updated: `screens/models.py` (3 sites — all already hold
  `self.registry`) and `_validated_entry` (already receives `registry`).
- Docstring of `is_cloud_effective` rewritten per the Decision above.

### 2. Shared contract fixture (`docs/contracts/`)

- `registry.sample.toml`: add a provider `pinned-cloud`
  (`auth.type = "api_key"`, `location = "cloud"`, **not** in
  `PROVIDER_POLICIES`) with one model `pinned-cloud/contract-fixture:inherit`
  carrying **no** `location` field.
- `modelman.sample.toml`: matching exposure example with a comment noting the
  provider-location inheritance rule.
- Both contract tests must assert the new model's exposure:
  `wt/internal/config/registry_fixture_test.go` (Go `IsExposed` via
  `ResolveLocation`) and `modelman/tests/contracts/test_registry_fixture.py`
  (Python `is_effectively_exposed` with `ready = false`, `litellm_exposed =
  true`). Changing the fixture without updating both tests breaks both CIs —
  that is the fixture's stated purpose.

### 3. Go — no code changes

Optionally add an explicit wt test asserting `IsExposed` on the new fixture
model, so the inheritance contract is pinned from both directions.

### 4. Docs

- `docs/guides/00-config-map.md`: the exposure-rule wording is updated to
  state that effective location resolves model-then-provider, making the
  "same rule" claim true.
- Per CLAUDE.md: run
  `git grep -n "litellm_exposed = " docs/guides/` before and after the change
  to catch guide-snapshot drift (guides 00, 02, 04, 05, 06, 08 embed live
  exposure state).

## Error Handling

Only new failure path: `registry.provider()` raising `KeyError` for a
provider id missing from the registry — caught in `is_cloud_effective` and
treated as not-cloud. All other behavior flows through existing paths.

## Tests

Python (`modelman/tests/test_litellm.py`, `modelman/tests/screens/test_models.py`):

- Provider `location = "cloud"` + model location unset → `is_cloud_effective`
  True; `passes_ready_gate` True with `ready = false`;
  `is_effectively_exposed` True with `litellm_exposed = true`, `ready = false`.
- Unknown provider id → conservative False.
- Existing tests updated for the new signatures (fixtures build a minimal
  `Registry`).

Contract tests: assertions for the new fixture model on both sides (see §2).

## Verification

- `cd modelman && make test && make check` (unit + contract tests)
- `cd wt && go build ./... && go vet ./... && go test ./...`
- `make lint` at repo root
