# Ollama catalog sync + time-windowed pricing — design

Date: 2026-09-28
Status: approved design, pending implementation plan

## Goal

One repeatable command (plus a Claude skill that drives it) that makes
modelman's ollama **cloud** entries match Ollama's published catalog at
<https://ollama.com/pricing>:

- add registry entries for every model on the page,
- refresh their per-token prices, including **off-peak** prices (a new
  concept for modelman),
- offer to delete pulled cloud stubs that Ollama no longer lists.

Off-peak pricing is **stored only** for now. wt will consume it later; no
display or routing behavior changes in this work.

## Non-goals

- Showing time-dependent prices in the modelman TUI or wt (later work;
  `price_at()` below is the reference implementation for it).
- Pulling (`ollama pull`), marking ready, or exposing newly added models.
- Touching real local ollama models (`ornith-1.5:35b`, `*-mlx`, …) — the
  pricing page only lists cloud models.
- Folding this into the daily `refresh-prices` startup worker.

## 1. Schema: time-windowed pricing

The existing flat `Cost` prices remain the **default row**. A new optional
list of timed rows sits beside them:

```toml
[models.cost]
input_price_per_million  = 1.32     # default: applies when no timed row matches
cache_price_per_million  = 0.044
output_price_per_million = 3.96
subscription_price  = 100.0
subscription_period = "month"

[[models.cost.time_prices]]
label    = "off-peak"               # informational only
timezone = "UTC"                    # IANA name
input_price_per_million  = 0.66
cache_price_per_million  = 0.022
output_price_per_million = 1.98
windows = [                         # union; start inclusive, end exclusive
  { days = ["mon","tue","wed","thu","fri"], start = "00:00", end = "12:00" },
  { days = ["mon","tue","wed","thu","fri"], start = "18:00", end = "24:00" },
  { days = ["sat","sun"],                   start = "00:00", end = "24:00" },
]
```

### Types (`registry.py`)

- `Window(days: list[str], start: str, end: str)`
- `TimePrice(label: str | None, timezone: str, input_price_per_million,
  cache_price_per_million, output_price_per_million: float | None,
  windows: list[Window], extra: dict)` — `extra` preserves unknown keys on
  round-trip, like `Cost.extra`.
- `Cost.time_prices: list[TimePrice]` (default empty; omitted from TOML
  when empty).

### Validation (extends `_validate_cost`)

- `timezone` resolves via `zoneinfo.ZoneInfo`.
- `days` non-empty, each in `mon tue wed thu fri sat sun`.
- `start`/`end` are `HH:MM`; `start` in `00:00`–`23:59`; `end` in
  `00:01`–`24:00`; `start < end` (a window never crosses midnight — split
  it into two).
- `windows` non-empty.
- Prices follow the existing rule: non-negative finite numbers or absent.

### Resolution rule

At instant *t*: take the **first** `time_prices` row that has any window
containing *t* converted to that row's timezone. Each price field comes
from that row, falling back **per field** to the default row when the
timed row omits it. If no timed row matches, the default row applies.
Overlapping timed rows are allowed; list order breaks ties.

Implemented as a pure helper `price_at(cost: Cost, at: datetime) ->
tuple[float | None, float | None, float | None]` (input, cache, output).
No caller in this work.

### Unchanged readers

- The TUI COST column and all current cost readers keep using the
  default row.
- `pricing.py::_merge_api_cost` (OpenRouter refresh) must carry
  `time_prices` through untouched.

### Cross-language contract

- Add one `time_prices` row to `docs/contracts/registry.sample.toml`.
- wt only **reads** `registry.toml` (its TOML encoders write only its own
  `config.toml`), so no wt write path can drop the field. Add a decode-only
  `TimePrices []TimePrice` to wt's `config.ModelCost` so the contract test
  covers it and later wt work has a typed starting point. The plan must
  confirm wt's contract test doesn't reject unknown keys before the Go
  side lands.

## 2. Components

### `src/modelman/ollama_catalog.py`

- `fetch_pricing_html(runner=None) -> str` — HTTP GET with an injectable
  runner (same pattern as `pricing.py`'s `_HTTPRunner`).
- `parse_pricing(html: str) -> list[CatalogModel]` — the **only** function
  that knows the page's HTML shape. It is kept small and isolated so that
  when Ollama changes the page, updating means editing this function plus
  the fixture.
  - Uses the stdlib `html.parser` (no new dependency).
  - Finds the table whose header cells contain, case-insensitively,
    `model`, `input`, `cached`, `output`; maps columns **by header text,
    not position**.
  - Model name: the cell text; a trailing `(Off-Peak)` (case-insensitive)
    marks the row as the off-peak variant of the base name.
  - Prices: `$<number>` → float; `—`, `-`, empty → `None`; any other text
    → `None` plus a warning.
  - Off-peak window: fixed in code as the constant `OLLAMA_OFFPEAK`
    (UTC; weekdays outside 12:00–18:00, all day weekends), matching the
    page's stated rule. The page's wording is not parsed.
- `CatalogModel(name, input, cache, output, offpeak: PriceTriple | None)`.
- `CatalogParseError(check: str)` — raised when any sanity check fails:
  - no matching table,
  - a required header is missing,
  - fewer than 5 model rows,
  - an off-peak row with no base row,
  - a duplicate base name.
- `plan_sync(registry, catalog, ollama_list: list[str] | None) ->
  SyncPlan` — pure, no I/O. Returns `updates`, `additions`,
  `delete_candidates`, `unlisted_registry` (report-only), `warnings`.
- `apply_sync(plan, registry, …)` — mutates the registry; deletions go
  through the existing delete path (below).

### CLI: `modelman ollama-catalog sync`

Options:

- `--dry-run` — print the plan and exit 0.
- `--html FILE` — parse a saved page instead of fetching (offline use,
  and parser repair).
- `--yes` — apply additions/updates without the confirm. It **never**
  auto-confirms deletes.

Flow:

1. Fetch and parse the page.
2. Run `ollama list`.
3. Build the plan and print it.
4. Confirm, then write the registry via `save_registry()`.
5. Prompt for each delete candidate, default **no**.

### Skill: `modelman/.claude/skills/ollama-catalog/SKILL.md`

Tells Claude to:

1. Run `uv run modelman ollama-catalog sync --dry-run` and summarize the
   plan for the user.
2. Walk through each delete candidate with the user, one at a time.
3. Run the real sync.
4. Run `git grep -n "exposed = " docs/guides/` before and after, per the
   root CLAUDE.md drift rule.
5. On exit code 3 (parse failure), open the saved HTML, update
   `parse_pricing`, refresh `tests/fixtures/ollama_pricing.html`, re-run
   the parser tests, then retry.

## 3. Sync behavior

### Name mapping

Page name `X` maps to the ollama tag `X:cloud`, or `X-cloud` when `X`
already contains `:` (e.g. `gpt-oss:120b` → `gpt-oss:120b-cloud`). This
matches the current registry convention.

A registry entry matches a page row if either:

- its `extra.catalog_name == X`, or
- its `provider_id == "ollama"` and its `model_name` equals the derived
  tag.

The first match writes `catalog_name = X` into the entry's preserved
unknown keys, so later tag renames still match.

### Cases

| Situation | Action |
|---|---|
| On page, in registry | Update default row prices. Off-peak row on page → set/replace the single `label = "off-peak"` `time_prices` row. No off-peak row → remove any `off-peak` row. Leave subscription, family, `exposed`, `ready`, `model_info` and other `time_prices` rows untouched. |
| On page, not in registry | Add `ollama/<tag>`: `provider_id = "ollama"`, `model_name = <tag>`, `location = "cloud"`, `source = "curated"` (wt treats `source` as a `curated`/`discovered` enum; provenance is marked by `catalog_name` instead), `catalog_name = X`, `family` = page name minus any `:size` suffix, prices as above. `subscription_*` is copied from the other ollama cloud entries **only if they all agree**; otherwise it is left unset with a warning. Not pulled, not ready, not exposed. |
| In `ollama list` as a cloud stub (tag ends `:cloud` or `-cloud`), not on page | Delete candidate: prompt per model, default no. Yes → the existing delete path (provider `delete()` + registry/state cleanup + unexpose cascade, as `PendingChanges.apply()` does). |
| In `ollama list` as a real local model | Ignored. |
| Registry ollama cloud entry, not on page, not pulled | Report only (listed under "no longer on ollama.com/pricing"); never modified. |

## 4. Error handling

| Failure | Behavior |
|---|---|
| Fetch fails (network, non-200) | Exit 2; registry untouched. |
| `CatalogParseError` | Save the HTML to `$TMPDIR/ollama-pricing-<timestamp>.html`, print the failed check and the path, exit 3; nothing written. |
| Unrecognized price cell text | That price → `None`, warning printed, sync continues. |
| `ollama list` fails (daemon down) | Warn, skip the delete step, still apply adds/updates. |
| A delete fails | Report it, continue with the rest; exit 1 at the end. |

All registry writes go through `save_registry()`, so the validation
above runs before anything reaches disk.

## 5. Testing

- **Parser** (`tests/test_ollama_catalog.py`), against a captured fixture
  `tests/fixtures/ollama_pricing.html`:
  - happy path (19 models; off-peak folded into the two deepseek entries),
  - reordered columns still parse,
  - a renamed or missing header raises `CatalogParseError`,
  - too few rows raises,
  - an orphan off-peak row raises,
  - `—` becomes `None`,
  - unknown cell text produces a warning.
- **`plan_sync`**, table-driven:
  - add,
  - update,
  - off-peak set, replace and remove,
  - `catalog_name` match after a tag rename,
  - `-cloud` vs `:cloud` derivation,
  - delete candidates (only pulled cloud stubs; never local tags),
  - report-only entries,
  - subscription copy when entries agree vs. disagree,
  - `ollama_list=None` → no delete candidates.
- **Schema** (`tests/test_registry*.py`):
  - `time_prices` round-trip including unknown keys,
  - each validation error,
  - `price_at` at weekday 11:59/12:00/17:59/18:00 UTC, on a weekend, with
    a non-UTC timezone, and with a per-field fallback to the default row.
- **OpenRouter merge:** `_merge_api_cost` preserves `time_prices`.
- **CLI** (`tests/commands/test_ollama_catalog.py`), using a CliRunner with
  a mocked HTTP runner and ollama runner:
  - `--dry-run` writes nothing,
  - exit code 3 saves the HTML,
  - `--yes` never skips delete prompts.
- **Contracts:** `docs/contracts/registry.sample.toml` gains a
  `time_prices` row, checked by both the modelman `tests/contracts/` test
  and the wt `internal/config` test.
- No test hits ollama.com; the existing autouse ollama guards stay in
  force.

## Open follow-ups (out of scope)

- wt: use `price_at()`-equivalent logic for time-of-day cost display.
- Possibly offer deletion of report-only registry entries (currently
  report-only by decision).
- Possibly run the price-only half of the sync from `refresh-prices`.
