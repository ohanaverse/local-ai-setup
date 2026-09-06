# Design — Align wt's exposure predicate with the TUI (issue #28, Option 1)

Date: 2026-09-07
Issue: #28 — Resolve wt-vs-TUI semantic divergence for model exposure
Roadmap: `docs/superpowers/plans/2026-09-06-issue-roadmap.md`, Milestone C

## Decision

Option 1 from the issue: **wt filters on the same predicate the modelman TUI
uses.** A model appears in wt's model picker iff:

```
is_native(provider) OR (litellm_exposed AND (ready OR model.location == "cloud"))
```

Rejected alternatives: Option 2 (revert TUI to flag-only — loses the readiness
signal PR #27 deliberately added) and Option 3 (keep the documented divergence
— guide-00's note is a fragile single point of truth).

## Parity with the Python predicate

The TUI's rule (`modelman/src/modelman/litellm.py`,
`is_effectively_exposed` semantics currently inlined at the `_validated_entry`
ready gate) is:

- `litellm_exposed` flag set, AND
- `state.ready`, OR the model is cloud-effective.

Cloud-effectiveness in Python is `is_cloud(provider_id)` (from
`PROVIDER_POLICIES`; only `openrouter` is cloud) OR
`model.location == "cloud"`. For real registry data every openrouter model row
is marked `location = "cloud"`, so keying the Go port on
**`model.location == "cloud"` alone** reproduces Python's behavior exactly.

Two deliberate exclusions, mirrored from the Python docstrings:

- Provider-level `location = "cloud"` (native/agent providers) does **not**
  grant the exemption — those rows are flag-only and never route through
  LiteLLM.
- Unknown or missing model_state entries are not exposed (unchanged from
  today).

Missing `ready` key (legacy states) decodes to the TOML zero value `false`,
so a local model without a ready flag is filtered out — conservative, and it
matches the existing fixture case "a model that exists in state but was never
downloaded".

## Changes

### 1. `wt/internal/config/modelman.go`

Extend the parsed state struct with `Ready bool` (toml `ready`).
`loadModelmanState` returns `map[string]modelmanModelState{Exposed, Ready}`
instead of `map[string]bool`. Missing-file and parse-error semantics are
unchanged (missing file → empty map; parse error → error).

### 2. `wt/internal/config/config.go`

`cfg.exposed` holds the richer map. `IsExposed(m Model)` becomes:

```go
if m.Native { return true }
st, ok := c.exposed[m.ID]
if !ok || !st.LitellmExposed { return false }
if m.Location == "cloud" { return true }
return st.Ready
```

Test helpers (`SetExposedForTest`, `ExposeAllForTest`) are updated to the new
map shape; `ExposeAllForTest` marks models ready so existing picker tests keep
passing.

### 3. Contract fixture — `docs/contracts/modelman.sample.toml`

Add entries pinning the new rule:

- Local model, `litellm_exposed = true`, `ready = false` → both tools treat it
  as **not** effectively exposed.
- Cloud model, `litellm_exposed = true`, no `ready` key → both tools treat it
  as effectively exposed.

Update the header comment: wt reads `litellm_exposed` **and** `ready`, and
applies the cloud-location exemption.

### 4. Contract tests, updated in the same PR

- `wt/internal/config/modelman_fixture_test.go` (Go) asserts the predicate
  against the fixture rows.
- `modelman/tests/contracts/test_modelman_fixture.py` asserts the same
  predicate on the same rows.

A future divergence between the two implementations fails both CI jobs in one
PR.

### 5. Unit tests

`wt/internal/config/modelman_test.go`: update `loadModelmanState` tests for
the new return type; add an `IsExposed` table test covering: native, flag
off, flag on + ready, flag on + not ready (local), flag on + cloud (no ready
key), missing entry.

### 6. Docs

- Remove the "deliberate divergence" note from `docs/guides/00-config-map.md`;
  replace with a short statement that both tools use the same predicate.
- Update the contract fixture header comment.
- Cross-reference the rule in `modelman/CLAUDE.md` and `wt/CLAUDE.md`.
- Run `make check-links` after doc edits.

## Error handling

No new error paths. Filtering is strictly more conservative: the worst case
is a genuinely serving local model with a stale `ready = false` being hidden
from wt until modelman refreshes state — the same information the TUI already
displays as `–`. Callers who must bypass the gate use `SetExposedForTest`
(tests only); there is no runtime bypass.

## Out of scope

- Extracting `is_effectively_exposed()` in Python (issue #29 / Milestone D) —
  follows this change so the helper encodes the now-agreed semantics.
- The wt last-model indicator (issue #35 / Milestone E).

## Verification

```bash
cd wt && go build ./... && go vet ./... && go test ./...
cd modelman && make check && make test   # includes contract tests
make lint                                # shell lint + check-links
```

Manual check: expose a local model in the TUI, stop its backend so
`ready = false`, launch `wt` — the model is not offered; a cloud model stays
offered regardless of `ready`.
