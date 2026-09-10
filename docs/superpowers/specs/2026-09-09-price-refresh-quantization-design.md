# Price Refresh + Quantization Field — Design

**Date:** 2026-09-09
**Status:** Approved
**Scope:** `modelman` (registry schema + TUI + CLI). No `wt` changes.

## Problem

Two open issues:

- **#63** — Two requested model fields:
  - `quantization` — a free-form string like `Q4_K_M` (user-filled, not parsed).
  - `sale` — marking OpenRouter models on sale, with `%` off.
- **#64** — Auto-update OpenRouter pricing/sales from an API.

Research findings that shape the design:

- OpenRouter's public API (`GET https://openrouter.ai/api/v1/models`) returns current
  per-token prices (`prompt`, `completion`, `input_cache_read`) that already reflect
  any sale-adjusted pricing. It exposes **no sale/discount flag** in the data. So a
  `sale` field cannot be populated from the API → **drop the `sale` field entirely**
  (close #64 as "not done"). The token prices we pull will be current/sale-adjusted,
  which is what actually matters.
- Ollama's pricing is only available via HTML scraping of a library page
  (`~z-ai/glm-5.3` → `data-rate="base"` blocks). We explicitly **avoid** HTML scraping —
  too fragile. Only OpenRouter's JSON API is used. Ollama cloud models therefore get
  prices via the manual/startup refresh only when their OpenRouter counterpart exists;
  otherwise the user enters prices by hand.

## Decisions

1. **`quantization`** — add a free-form `quantization: str | None` field to `ModelEntry`,
   round-tripped through `registry.toml` and editable in `ModelForm`. It is a
   display/annotative field; `wt` ignores unknown top-level model keys (confirmed:
   wt's BurntSushi decoder tolerates unknown keys without error), so **no Go/contract
   change is required**. To keep the change strictly modelman-only, the shared contract
   fixture (`docs/contracts/registry.sample.toml`) is **not** modified.

2. **No `sale` field.** Close #64 without implementing a sale flag (it cannot be
   fetched). Pricing pulled from the API is already current/sale-adjusted.

3. **`pricing_updated_at`** — add `pricing_updated_at: str | None` to `ModelEntry`
   (an ISO-8601 timestamp). Stored on the model (not inside `Cost`) so it never leaks
   into LiteLLM's `model_info` pricing path. Set on two occasions:
   - a successful API refresh (startup auto-refresh or the `refresh-prices` subcommand);
   - a manual price edit saved through `ModelForm`.
   The `ModelForm` dialog shows it as a read-only line
   ("Token pricing updated: 2026-09-09 14:32", or "never"). Editing nothing but
   re-saving preserves the existing timestamp.

4. **`modelman refresh-prices` subcommand** — Python, using the existing `requests`
   dependency (no new deps). Hits `GET https://openrouter.ai/api/v1/models`, iterates
   registry models owned by a cloud provider (`openrouter`, or `location = "cloud"` on
   the model/provider), matches by `model_name` → API `id`, and maps:
   - `prompt` → `cost.input_price_per_million` (multiply per-token by 1e6)
   - `completion` → `cost.output_price_per_million`
   - `input_cache_read` → `cost.cache_price_per_million`
   On success also sets `pricing_updated_at`. **Fail-soft:** any per-model fetch/parse
   error becomes a warning, never a block; total API failure reports the error and
   leaves all pricing untouched. Unmatched models are noted and skipped.

5. **Daily-gated startup auto-refresh** — on TUI mount (and the CLI's `download`
   path which pushes the ModelScreen), after screens render, kick a background worker
   thread (mirroring the existing `reconcile` pattern in `screens/__init__.py`). It
   reads a persisted "last refresh date" stored as a top-level `StateStore.extra` key
   in `modelman.toml`. If today's fetch has not happened yet **and** at least one
   cloud/openrouter model exists in the registry, it runs the OpenRouter refresh and
   updates prices + `pricing_updated_at` + the last-refresh date. Non-blocking; results
   surface as a notification (success → "token prices refreshed"; warnings on errors).
   No fetch on exit — quitting is always fast and side-effect-free. `StateStore.extra`
   now gains a `price_refresh_last_run` date key (whitelisted top-level key).

6. **Exit reminder** — if a family/model changed during the session **and** the
   startup auto-refresh was skipped or failed (e.g. offline, or a new day with no
   refresh run yet), `ConfirmExitDialog` shows a one-line reminder:
   "token pricing may be stale — run `modelman refresh-prices`." This is the vegetarian
   option the user chose over an exit-time fetch; the startup auto-refresh usually
   makes it moot, but it covers the offline/failed case.

7. **Root `make` target** — add a root-level convenience target (e.g. `make install`)
   that compiles/installs all wt components (`cd wt && make install`) and installs
   modelman (`cd modelman && uv sync`). With no separate Go price helper, this is
   just an aggregation of existing targets — no new build tooling.

### Deferred (issue #69)

- `wt` notifying the user when prices are stale, with instructions to run
  `modelman refresh-prices`. Deliberately **deferred** to keep this change
  modelman-only. Filed as ohanaverse/local-ai-setup#69.

## Files

- `modelman/src/modelman/registry.py` — `ModelEntry.quantization`, `ModelEntry.pricing_updated_at`; `_model_to_dict` / `_parse_model` / `unknown_keys` whitelist.
- `modelman/src/modelman/screens/forms.py` — `Quantization:` text `Input`; read-only `pricing_updated_at` line; set timestamp on price-field save.
- `modelman/src/modelman/screens/models.py` — pass `quantization` through `_variant_to_model_entry` / `model_entry_to_variant`.
- `modelman/src/modelman/state.py` — `price_refresh_last_run` top-level key (read/write + whitelist).
- `modelman/src/modelman/pricing.py` *(new)* — OpenRouter fetch + mapping + fail-soft logic, shared by the subcommand and the startup worker.
- `modelman/src/modelman/main.py` — `refresh-prices` subcommand.
- `modelman/src/modelman/app.py` — startup background refresh worker.
- `modelman/src/modelman/screens/forms.py` (`ConfirmExitDialog`) — exit reminder.
- `Makefile` (root) — `make install` aggregate target.
- Tests: registry round-trip for the two new fields; `pricing.py` mapping/fail-soft; startup worker gating; `refresh-prices` subcommand; form prefill/timestamp.

## Testing

- Registry: serialize/deserialize `quantization` and `pricing_updated_at`; unknown-key
  whitelist preserves them; absent keys → `None`.
- Pricing mapping: per-token → per-million conversion; missing cache read → `None`;
  unknown/unmatched model → skip with notice; API failure → no mutation.
- Daily gate: runs once per day; gated on ≥1 cloud model; not run when already run today.
- Manual edit: sets `pricing_updated_at`; re-save without price change preserves it.
- Form: quantization prefill on edit; timestamp line renders "never" when unset.
- `refresh-prices` CLI: happy path + failure paths (uses the same HTTP runner injection
  pattern as `ollama_caps.auto_detect_model_info` for hermetic tests).
