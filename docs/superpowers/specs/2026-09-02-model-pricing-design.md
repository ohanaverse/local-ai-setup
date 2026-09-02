# Model Pricing Info — Design

**Date:** 2026-09-02
**Status:** Approved
**Scope:** `modelman` registry schema + TUI, `wt` model picker, LiteLLM config pass-through

## Problem

The registry only supports three mutually exclusive cost kinds:
`free`, `per_token`, or `subscription`. This is wrong for Ollama Pro-style
models: a monthly subscription gives a credit pool, while each model has its
own per-token burn rate. The two prices are independent and both matter.

Additionally:
- The current per-token price is a single number, not the industry-standard
  input/cache/output trio.
- `usage_tier` is obsolete now that real pricing exists.
- Pricing is not shown in `wt`'s model picker or in modelman's model table.
- LiteLLM cannot use our pricing for usage tracking because we never pass it.

## Decision

Replace the legacy `cost.kind` enum with flat optional pricing fields on every
model. Both per-token and subscription can coexist. Render the prices in both
modelman and wt, and pass per-token prices through to LiteLLM's
`model_info` cost keys.

## New registry schema

```toml
[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
tags = ["code"]
[models.cost]
input_price_per_million  = 0.50
cache_price_per_million  = 0.25
output_price_per_million = 1.00
subscription_price       = 19.99
subscription_period      = "month"   # "month" or "year"
```

A free model has no price fields. A model may have only per-token prices,
only a subscription, or both.

## Legacy migration

On load, modelman converts old `cost.kind` entries into the new fields:

| Legacy field | New mapping |
| --- | --- |
| `kind = "free"` | no prices |
| `kind = "per_token"`, `price_per_million_tokens = X` | `input_price_per_million = X`, `output_price_per_million = X` |
| `kind = "subscription"`, `price_per_period = P`, `period = T` | `subscription_price = P`, `subscription_period = T` |

The `usage_tier` field is dropped completely: not parsed, not stored, not
saved. Unknown legacy keys survive in `extra` as usual.

On save, only the new fields are written; old `kind`, `price_per_million_tokens`,
`price_per_period`, and `period` disappear.

## Display formatting

### Per-token prices

Rendered as `<input>/<cache>/<output>` with a single leading `$`:

- All present: `$0.50/0.25/1.00`
- Cache absent: `$0.50/-/1.00`
- All absent: `—`

Formatting rule:
- Minimum 2 decimal places (`1.5` → `1.50`, `20` → `20.00`).
- Preserve precision for fractional cents (`0.0001` → `0.0001`, not `0.00`).
- Strip only trailing zeros beyond the minimum.

### Subscription prices

Rendered as `$<price>/<period>`:
- `$19.99/mo`
- `$199.99/yr`
- Subscription absent: `—`

Period abbreviation:
- `month` → `mo`
- `year` → `yr`

### Free per-token usage under a subscription

If a model has a subscription but no per-token prices:
- COST column shows `—`
- SUB column shows the subscription amount
LiteLLM receives explicit `input_cost_per_token: 0` and
`output_cost_per_token: 0` so budget checks are bypassed.

## modelman changes

### Registry module (`src/modelman/registry.py`)

- Replace `Cost` dataclass fields:
  - Remove: `kind`, `price_per_million_tokens`, `price_per_period`, `period`
  - Add: `input_price_per_million`, `cache_price_per_million`,
    `output_price_per_million`, `subscription_price`, `subscription_period`
- Update `_parse_cost`, `_cost_to_dict`, `_cost_from_dict`, save/load round trip.
- Add a `validate_cost` step: prices must be non-negative finite numbers when
  present; `subscription_period` must be `"month"` or `"year"` when
  `subscription_price` is set.
- Drop all `usage_tier` parsing/serialization.

### Model screen (`src/modelman/screens/models.py`)

- Replace the `COST` and `TIER` columns with `COST` and `SUB`.
- `COST` shows per-token trio; `SUB` shows subscription.
- Remove `_format_tier`.
- Update `_format_cost` to render per-token and subscription separately.
- Keep `_model_entry_to_variant` and `_variant_to_model_entry` in sync with the
  new Cost shape.

### Model form (`src/modelman/screens/forms.py`)

- Remove `cost-kind-select`.
- Add two checkboxes:
  - "Per-token pricing" — reveals `input_price`, `cache_price`, `output_price`
    Inputs.
  - "Subscription pricing" — reveals `subscription_price` Input and
    `subscription_period` Select (`month`/`year`).
- Validation:
  - If per-token is checked, at least one of the three prices must be provided.
  - If subscription is checked, both price and period must be provided.
  - All provided prices must be non-negative finite numbers.
- Remove `usage-tier-label` and `usage-tier-select`.
- Update `parse_cost_fields` or replace it with a new helper.
- Update edit-mode prefill.

### LiteLLM pass-through (`src/modelman/litellm.py`)

In `build_model_list_entry`, add per-token costs to `model_info` (converted
from $/M to per-token):

| Registry field | LiteLLM `model_info` key |
| --- | --- |
| `input_price_per_million` | `input_cost_per_token` |
| `output_price_per_million` | `output_cost_per_token` |
| `cache_price_per_million` | `cache_creation_input_token_cost`, `cache_read_input_token_cost` |

- If no per-token prices are set, write `input_cost_per_token: 0` and
  `output_cost_per_token: 0` explicitly so LiteLLM bypasses budget checks.
- Subscription prices are not passed to LiteLLM.

## wt changes

### Config model (`wt/internal/config/config.go`)

Add a `Cost` struct to `config.Model`:

```go
type ModelCost struct {
    InputPricePerMillion  *float64 `toml:"input_price_per_million,omitempty"`
    CachePricePerMillion  *float64 `toml:"cache_price_per_million,omitempty"`
    OutputPricePerMillion *float64 `toml:"output_price_per_million,omitempty"`
    SubscriptionPrice     *float64 `toml:"subscription_price,omitempty"`
    SubscriptionPeriod    string   `toml:"subscription_period,omitempty"`
}
```

Embed it in `Model`:
```go
type Model struct {
    ...
    Cost Cost `toml:"cost,omitempty"`
}
```

Remove any `usage_tier` references if present (none currently in Go).

### Model picker line (`wt/internal/tui/model_list.go`)

Append pricing after the `1d/7d/30d` counts:

```
gemma4    0   ollama/gemma4:9b        $0.50/0.25/1.00   —          local   0/0/0   [code]
gemma4    0   ollama/gemma4:9b-pro    —                 $19.99/mo   local   0/0/0   [code]
qwen3.8   1   openrouter/qwen3.8:27b  $0.50/0.25/1.00   —          cloud   1/2/3   [code]
```

- Per-token uses the same `$in/cache/out` format.
- Missing segments render as `-`.
- Subscription absent renders as `-`.
- Preserve dynamic column padding for whatever column widths the eligible
  list actually contains.

## Cross-language contract

Update `docs/contracts/registry.sample.toml` to exercise the new shape:

```toml
[[models]]
id = "ollama/contract-fixture:cloud"
family = "contract-fixture"
provider_id = "ollama"
model_name = "contract-fixture:cloud"
tags = ["chat"]
[models.cost]
input_price_per_million  = 0.50
cache_price_per_million  = 0.25
output_price_per_million = 1.00
subscription_price       = 19.99
subscription_period      = "month"
```

Update both contract tests:
- `modelman/tests/contracts/test_registry_fixture.py`
- `wt/internal/config/registry_fixture_test.go`

Remove the old fixture model that used `usage_tier` and the legacy
`price_per_million_tokens` / `price_per_period` fields.

## Testing

### modelman

- `tests/test_registry.py`: load/save round trip with new fields; validation of
  bad prices and period strings; migration of legacy `kind` entries.
- `tests/screens/test_forms.py`: new cost-section visibility, validation
  errors, edit prefill, save output.
- `tests/screens/test_models.py`: COST/SUB columns render correctly.
- `tests/test_litellm.py`: per-token prices pass through as per-token keys;
  free models get explicit zeros.
- `tests/contracts/test_registry_fixture.py`: match updated sample.

### wt

- `internal/config/registry_fixture_test.go`: decode new cost fields.
- `internal/tui/model_list_test.go`: pricing appears after usage counts;
  missing prices render `-`.
- `make test`, `make vet`, `make lint`.

## Docs

- Update `docs/contracts/registry.sample.toml`.
- No guide drift expected for `litellm_exposed` snapshots, but run
  `git grep -n "litellm_exposed = " docs/guides/` before and after to confirm.

## Out of scope

- Provider-level subscription pricing.
- Usage/credit tracking inside modelman or wt.
- Per-character, per-image, per-second, or other non-token pricing models.
- Changing how providers calculate actual spend.
