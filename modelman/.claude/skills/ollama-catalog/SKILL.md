---
name: ollama-catalog
description: Mirror https://ollama.com/pricing into ollama, modelman and LiteLLM — pull and expose cloud models, rm/unregister/unroute ones Ollama no longer lists, and sync prices (including off-peak) into registry.toml and config.yaml. Use when asked to update/refresh ollama cloud models or ollama pricing, or when the ollama pricing scrape breaks.
---

# Ollama catalog sync

`modelman ollama-catalog sync` makes three things match
<https://ollama.com/pricing>: the cloud models in `ollama list`, the
ollama cloud entries in `registry.toml` along with their prices, and
LiteLLM's routes in `config.yaml`.

- **Full mirror.** A sync does four things. It adds registry entries for
  new page models. It `ollama pull`s every page model that isn't pulled.
  It removes registry entries the page no longer lists, running `ollama rm`
  first if they're pulled. It also `ollama rm`s pulled cloud stubs that
  have no registry entry.
- **LiteLLM mirrors the page.** Every page model is exposed, or
  re-exposed so that its `config.yaml` row is rebuilt with current prices.
  wt writes prices only at expose time. A removed entry that was exposed
  loses its route, and the plan marks these `(exposed — will be unexposed)`.
  wt restarts the proxy only if `config.yaml` actually changed.
- **Real cloud tags.** A page name doesn't determine its tag: some models
  publish `<name>:cloud`, others only a sized `<name>:<size>-cloud`. Each
  bare name is resolved from `https://ollama.com/library/<name>/tags`. A
  model with no cloud tag, or several, is skipped with a `warning:` line;
  ask the user before hand-editing anything for it. An existing entry under
  the wrong tag is replaced by the correctly tagged one.
- **Off-peak prices.** These are stored as a `[[models.cost.time_prices]]`
  row labelled `off-peak`: UTC, weekdays outside 12:00–18:00, and all day
  on weekends.
- **Real local models are never touched.** Only ollama cloud entries and
  `*:cloud`/`*-cloud` tags are in scope.

Run everything from `modelman/`.

## Steps

1. Dry run: `uv run modelman ollama-catalog sync --dry-run`
   - Summarize the plan for the user: price updates (old → new), registry
     additions (id + family), pulls, registry removals (call out every
     `exposed` one), stray `ollama rm`s, and the models that will get a
     new LiteLLM route ("Not yet exposed").
   - Point out any `warning:` lines, e.g. a subscription disagreement or an
     unrecognized price cell.
   - Ask whether any added model's family should be changed. If so, edit
     `family` in registry.toml after the sync.
2. Get the user's go-ahead for the whole plan, especially the removals.
3. Real run: `uv run modelman ollama-catalog sync --yes`. Your Bash tool
   has no TTY, so the CLI's single confirmation prompt can't be answered
   (click reads EOF and aborts). Pass `--yes` only once the user has
   approved the dry-run plan.
   - Exit 4 means more than half of the ollama cloud entries would be
     removed. Nothing was written. Check the dry run's page parse with the
     user, and add `--force` only if they confirm the removals are real.
4. Check the routes with `wt litellm list`.

## Exit codes

| Code | Meaning | Do |
|---|---|---|
| 0 | done (or nothing to do) | — |
| 1 | a pull, an `ollama rm`, a LiteLLM expose/unexpose, or the registry save failed; the other steps still ran | read the error, then re-run the sync (it only redoes what is still out of sync) |
| 2 | page fetch failed, `ollama list` couldn't run, or no cloud tag resolved (ollama.com/library unreachable); nothing changed | check network / start ollama, retry; or pass `--html <saved page>` |
| 3 | page shape changed | follow "Repairing the parser" |
| 4 | mass removal refused (> half the cloud entries); nothing changed | verify the parse, then `--force` with the user's OK |

## Repairing the parser (exit 3)

The error names the failed check and a saved file
(`$TMPDIR/ollama-pricing-<timestamp>.html`).

1. Open the saved HTML and find the pricing table/markup.
2. Update **only** `src/modelman/ollama_catalog.py::parse_pricing` (and
   `_TableCollector`/`_map_columns` if the structure moved). Keep the
   sanity checks: header-keyed columns, ≥ `MIN_ROWS` rows, off-peak rows
   must have a base row, and most price cells must parse.
3. Replace `tests/fixtures/ollama_pricing.html` with the saved page, update
   `test_parse_fixture_happy_path`'s expected counts/prices, and run
   `uv run pytest tests/test_ollama_catalog.py -q && make check`.
4. Retry with `--html <saved page> --dry-run`, then run the steps above.

If the page's off-peak **window** wording changes (it's not parsed), update
`offpeak_time_price()` and its test to match the new window.
