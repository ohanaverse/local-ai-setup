---
name: ollama-catalog
description: Sync modelman's ollama cloud models and prices (including off-peak) from https://ollama.com/pricing, and offer to delete pulled cloud models Ollama no longer lists. Use when asked to update/refresh ollama cloud models or ollama pricing, or when the ollama pricing scrape breaks.
---

# Ollama catalog sync

Keeps `registry.toml`'s ollama cloud entries in line with
<https://ollama.com/pricing> via `modelman ollama-catalog sync`.

- **Off-peak prices.** These are stored as a `[[models.cost.time_prices]]`
  row labelled `off-peak`: UTC, weekdays outside 12:00–18:00, and all day
  on weekends.
- **Real local models are never touched.** `ornith-1.5:35b`, `*-mlx` and
  similar are left alone. Only pulled cloud stubs (`*:cloud` / `*-cloud`)
  can be offered for deletion.
- **Entries missing from the page are only reported.** Registry entries
  no longer on the page, and not pulled, are listed but never changed.

Run everything from `modelman/`.

## Steps

1. Snapshot the guide drift surface (root CLAUDE.md rule):
   `git -C .. grep -n "exposed = " docs/guides/ > /tmp/exposed-before.txt`
2. Dry run: `uv run modelman ollama-catalog sync --dry-run`
   - Summarize the plan for the user: updates (old → new prices), additions
     (id + family), delete candidates, and report-only entries.
   - Point out any `warning:` lines, e.g. a subscription disagreement or an
     unrecognized price cell.
   - Ask whether any added model's family should be changed. If so, edit
     `family` in registry.toml after the sync.
3. Go through each delete candidate with the user, one at a time, before
   running for real. Deleting a registered model also removes its registry
   entry and unexposes it.
4. Real run. Your Bash tool has no TTY, so the CLI's prompts cannot be
   answered (click reads EOF and aborts). Pass every decision as a flag:
   - `--yes` applies the registry changes the user approved in the dry
     run. It never deletes anything.
   - For deletes, pass `--delete <tag>` once per stub the user chose to
     delete. Candidates not named are kept. If the user chose none, pass
     `--no-deletes`.
   - Example: `uv run modelman ollama-catalog sync --yes --delete old-model:cloud`
   - Exit 4 with `error: not delete candidates: ...` means the page or
     `ollama list` changed since the dry run. Nothing was written. Re-run
     the dry run and ask again.
5. Re-run the grep from step 1 and `diff` against the snapshot. The sync
   doesn't change `exposed`, but deletes of exposed models do, so report
   any drift in the six guides.
6. New entries are not pulled or exposed. To use one, run
   `uv run modelman expose ollama/<tag>`, or start it from wt.

## Exit codes

| Code | Meaning | Do |
|---|---|---|
| 0 | done | — |
| 1 | a delete or the registry save failed | read the error and retry the failed piece |
| 2 | page fetch failed | check network, retry; or pass `--html <saved page>` |
| 3 | page shape changed | follow "Repairing the parser" |
| 4 | invalid request, nothing changed: `--delete` with `--no-deletes`, or a `--delete` tag that is no longer a candidate | fix the flags, or re-run the dry run and ask again |

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
