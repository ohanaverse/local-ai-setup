# Spec: surface Cost and ollama usage tier in the model screen and edit dialog

**Date:** 2026-08-30
**Status:** approved (in chat)
**Branch:** feat/tui

## Problem

`Cost` already exists as a field on `ModelEntry` (registry.py:68-75):
`kind: Literal["free", "per_token", "subscription"]` with
`price_per_million_tokens`, `price_per_period`, and `period`. The field
is preserved on registry load/save but is invisible to the user: there
is no table column, no add/edit dialog, no usage report. So the only
way to set cost today is to hand-edit `registry.toml`.

Ollama's hosted cloud models also carry a separate "usage tier"
(low / medium / high) on `ollama.com/library/<model>`. It identifies
which Ollama hosted tier the model runs on and is a billing-side
signal humans use to choose between hosted cloud models. It is *not*
a price — Ollama's pricing is `subscription $X/month` regardless of
tier — and so it needs to be tracked independently from `Cost`.

Both pieces of information are human-consumption input: they help the
user pick which model to download/expose. They do not need to be
auto-derived or computed.

## Goal

Surface `Cost` and ollama usage tier in the model screen and edit
dialog so the user can see and set both without leaving the TUI.

Non-goals:
- Auto-fetching prices or tiers from ollama.com / OpenRouter etc.
  Both fields stay hand-curated.
- Currency handling. USD is assumed (matches the existing schema).
- Wiring `Cost` into the LiteLLM spend report. That's a separate
  effort.
- Adding a new `usage_tier` for non-ollama providers. Only ollama
  has tiers today.

## Design

### 1. Schema additions

**`src/modelman/registry.py`**: add `usage_tier: str | None = None` to
`ModelEntry` (sits next to `tags`, `cost`, `source` — the cluster of
optional metadata fields at the end of the dataclass).

Add a type alias for documentation/typing:

```python
UsageTier = Literal["low", "medium", "high"]
```

The field stays typed `str | None` at parse time so adding new tier
names later doesn't break loading existing registries. New entries
written from the TUI/CLI will use the canonical `low | medium | high`
spellings.

`_model_to_dict()` emits `usage_tier` only when set
(via the existing `drop_none` helper). `_parse_model()` reads it.

`Cost` is unchanged. Subscription pricing covers the ollama-cloud
`$X/month` case via `price_per_period` + `period: "month"`.

### 2. Table layout (`src/modelman/screens/models.py`)

The current 8-column table is reshaped to 9 columns plus a path
details panel. Final layout:

| FAMILY | PROVIDER | MODEL | LOC | STATUS | EXPOSED | COST | TIER | SIZE |

With the path rendered in a Static below the table.

- **LOCATION**: `↗` (cloud) / `▤` (local). Replaces the existing
  `cloud` / `local` strings. Width: 1 char.
- **STATUS**: unchanged. Glyphs are `✗` (queued delete), `↓`
  (queued download), `↑` (queued un-ready), `→` (queued move),
  `✓` (ready), `○` (not ready). Width: 1 char.
- **EXPOSED**: `Y` / `–`. Replaces `L` / `–`. The `L` was opaque;
  `Y` is unambiguous. Width: 1 char.
- **COST**: formatted price string (see §3). Width: 4-7 chars
  depending on data; auto-sized by Textual.
- **TIER**: ollama usage tier or `–`. Width: up to 6 chars
  (`medium`). Auto-sized.
- **SIZE**: unchanged.

Textual's DataTable auto-sizes columns from the rendered content, so
explicit column widths are not set — the table picks the tightest
size that fits the largest cell in each column.

### 3. Cost formatter

New helper in `screens/models.py`:

```python
def _format_cost(cost: Cost | None) -> str:
    """Short human-readable cost string for the table column."""
    if cost is None:
        return "—"
    if cost.kind == "free":
        return "free"
    if cost.kind == "per_token":
        p = cost.price_per_million_tokens
        return f"${p:.2f}/M" if p is not None else "$/M"
    if cost.kind == "subscription":
        p = cost.price_per_period
        per = cost.period
        if p is not None:
            return f"${p:.0f}" + (f"/{per}" if per else "")
        return f"$/{per}" if per else "$/"
    return cost.kind
```

`—` is rendered when no cost is set (still useful to see — a
free local ollama model and a model with no cost set look the same to
the user, but the formatter is honest about it). In the subscription
branch, a price without a period renders as `$20` (no suffix), a
period without a price as `$/month`, and neither as `$/`.

### 4. Tier formatter

```python
def _format_tier(model: ModelEntry) -> str:
    if model.usage_tier is None:
        return "—"
    return model.usage_tier
```

`—` when not set; lowercase tier name (`low` / `medium` / `high`)
otherwise.

### 5. Details panel — path

A new `Static(id="details-panel")` is mounted in `compose()` directly
below the DataTable, mirroring the existing `#pending-bar` pattern.
It is always visible; the user said the path should be there without
ceremony (no toggle, no keybinding).

Content format (single line):

```
path: /Users/me/.ollama/models/blobs/sha256-abc...
```

`path: —` (em dash) when no row is selected, or when the row's
path is unknown (e.g. not ready and reconcile hasn't run).

Updated on `DataTable.RowHighlighted` (cursor move) and on
`DataTable.RowSelected`. The handler reads `mt.coordinate_row` (or
the row key from the event payload) and looks up the
`ModelEntry.disk_path` / reconcile cache.

### 6. Add/edit dialog (`src/modelman/screens/forms.py`)

`ModelForm` grows a Cost section and (for ollama providers) a Tier
section.

Cost section:

- A `kind` `Select` (`#cost-kind-select`) with options
  `free | per_token | subscription`. Default: `free`.
- A conditional price `Input` (`#price-per-mtok`) shown only when
  `kind == per_token`. Placeholder: `e.g. 2.50`.
- A conditional price `Input` (`#price-per-period`) shown only when
  `kind == subscription`. Placeholder: `e.g. 20`.
- A conditional period `Select` (`#period-select`) shown only when
  `kind == subscription`. Options: `month | year`. Default: `month`.

Tier section (ollama only — gated by `provider_kind == "ollama"`):

- A `tier` `Select` (`#usage-tier-select`) with options
  `— | low | medium | high`. Default: `—` (None).

`on_select_changed` already handles provider-driven placeholder/
location updates. It is extended to also drive cost-field
visibility: when `#cost-kind-select` changes, show/hide the relevant
`Input` and `Select` widgets. When `#provider-select` changes,
show/hide the tier section based on the new provider's kind.

### 7. Cost parsing helper

New helper in `forms.py`:

```python
def parse_cost_fields(
    kind: str, price_mtok: str, price_period: str, period: str
) -> Cost:
    """Build a Cost from the dialog fields. Raises ValueError on bad input."""
    if kind == "free":
        return Cost(kind="free")
    if kind == "per_token":
        try:
            p = float(price_mtok)
        except ValueError:
            raise ValueError("price_per_million_tokens must be a number")
        return Cost(kind="per_token", price_per_million_tokens=p)
    if kind == "subscription":
        try:
            p = float(price_period)
        except ValueError:
            raise ValueError("price_per_period must be a number")
        return Cost(kind="subscription", price_per_period=p, period=period)
    raise ValueError(f"unknown cost kind: {kind}")
```

The dialog shows the resulting `ValueError` via the existing
`#model-error` Label (reused — already wired up).

### 8. Wire-up: `_variant_to_model_entry`

`models.py:24` currently drops Cost. Updated to read from the
`ModelFormResult.spec` dict and pass through to `ModelEntry`:

```python
def _variant_to_model_entry(variant, *, family, registry) -> ModelEntry:
    ...
    return ModelEntry(
        ...
        cost=variant.get("cost"),
        usage_tier=variant.get("usage_tier"),
    )
```

The `spec: VariantSpec` TypedDict (defined in
`providers/base.py`) grows two new optional fields
(`cost: Cost | None`, `usage_tier: str | None`) and the form result
carries both on save.

### 9. Wire-up: `_model_entry_to_variant`

The reverse adapter (`models.py:97`) for edit-mode prefill includes
the new fields so they round-trip cleanly:

```python
return {
    ...
    "cost": entry.cost,
    "usage_tier": entry.usage_tier,
}
```

### 10. `ModelForm._submit` extension

After `parse_model` succeeds, also call `parse_cost_fields` and, if
the provider is ollama, read the tier. The result extends `spec`:

```python
spec: VariantSpec = {
    ...
    "cost": cost,  # from parse_cost_fields
    "usage_tier": usage_tier,  # str | None, only for ollama
}
```

For non-ollama providers `usage_tier` is forced to `None` regardless
of any stale value in the dialog.

### 11. Tests

- **`tests/test_registry.py`**: round-trip a `ModelEntry` with
  `cost=Cost(kind="subscription", price_per_period=20.0,
  period="month")` and `usage_tier="high"`. Verify both load.
- **`tests/test_registry.py`**: a model with `usage_tier=None` and
  no cost must round-trip without either key appearing in the TOML
  (`drop_none` behavior).
- **`tests/test_registry.py`**: existing cost-missing-kind test
  still passes.
- **`tests/test_models_screen.py`** (or wherever `_format_cost` /
  `_format_tier` tests live): the formatters return the expected
  strings for each kind / tier / None combination.
- **Dialog tests** (existing TUI tests): `parse_cost_fields` raises
  for bad numeric input; `ModelForm` round-trips cost & tier on
  save; `ModelForm` toggles field visibility when `cost-kind-select`
  changes.

### 12. Out of scope (explicit)

- No automated price refresh.
- No currency support beyond USD.
- No `usage_tier` for non-ollama providers.
- No `usage_tier` column rendering for providers that don't have one
  — it's `—` for everyone else.
- No `usage_tier` change history / audit log.
- No LiteLLM spend-report wiring.

## Risks

- **Column count grows to 9** with two narrow icon columns and one
  short COST column. On narrow terminals (≤ 100 cols) the DataTable
  may need to truncate `FAMILY` or `MODEL`. Textual handles this
  with auto-scroll; users on narrow screens already see truncated
  model names today, so no regression in worst case.
- **The `$2.50/M` format** assumes USD. If a user wants EUR or
  per-1k-token pricing, that's a schema change deferred until
  someone asks.
- **The `Y` exposed indicator** is one character that could collide
  with future single-character columns if anyone adds another.
  Acceptable for now; if a third "1-char yes/no" column lands,
  revisit.

## Rollout

1. Add `usage_tier` to `ModelEntry` + parsers + serializers.
2. Add `_format_cost` and `_format_tier` helpers in `screens/models.py`.
3. Reshape the table: drop PATH, add LOC/STATUS/EXPOSED icons, add
   COST/TIER columns.
4. Add the details-panel Static + RowHighlighted handler.
5. Extend `ModelForm` with the cost & tier sections + visibility
   handlers.
6. Extend `_variant_to_model_entry` / `_model_entry_to_variant`.
7. Update tests; run `make check`.