# CLAUDE.md

Guidance for Claude Code when working in `modeltiers/`.

## Project overview

`modeltiers` is a Python 3.13 CLI (argparse) that writes an advisory capability tier (`marathon`, `deep`, `standard`, `quick` or `unrated`) for every model wt lists, from four public sources, into `tiers.toml`. Nothing routes on a tier yet. Design: `../docs/superpowers/specs/2026-10-12-model-tiers-design.md`. The spec's Delivery section lists four steps; this package is at step 3: everything is built, and the committed `aliases.toml` and `contracts.toml` are not yet calibrated against a live run (step 4).

It never writes `registry.toml`, `config.yaml` or any wt state, and it never reads `registry.toml`: the inventory comes only from `wt model list --json`.

## Commands

Run from the repo root as `uv run --directory modeltiers modeltiers <command>`, or from this directory as `uv run modeltiers <command>`. `--directory` makes `modeltiers/` the working directory, so a relative `--inventory` path resolves from here.

| Command | Effect |
|---|---|
| `modeltiers run [--dry-run] [--offline] [--inventory FILE]` | Validate `aliases.toml` and `contracts.toml`, read the inventory, load the four sources, resolve names, assign tiers, check the sentinels, print the report and write `tiers.toml`. `--dry-run` does everything except write the output file, and returns the code the real run would return; the one exception is a redirected registry (below), where the real run is refused with 1 and the dry run reports and warns. `--offline` fetches nothing and reads each source's newest good snapshot. `--inventory FILE` reads saved `wt model list --json` output and does not run wt. |
| `modeltiers show [--candidates]` | The tier table from `tiers.toml`: inventory models, the unmapped rows, and the candidates with `--candidates`. First line is the file's age in days; past 14 days a `warning:` goes to stderr. Ends with the attribution lines. |
| `modeltiers explain <model>` | One entry's identity, tier, votes, scores, efforts, gates and notes, then the attribution lines of the sources it used. The argument is tried as an exact `provider_id/model_name`, then as a canonical key in any case (every model that resolved to it, else the candidate). An unmapped row says it needs an alias. No match exits 1 with the file's generated date. |

`show` and `explain` read only the output file: no fetch, no wt, no `contracts.toml`.

What `run` prints, in order: the inventory line; one line per source; the identity section (every row, the unmapped list, the review list of name matches); the unresolved records at or above a standard floor, and per-source counts; `sentinels: N of N hold`; the change report against the existing file (or, with none, the full table); the attribution lines; `wrote <path>` or `dry run: <path> not written`.

| Exit | Meaning |
|---|---|
| 0 | Written. Every source `ok`, or `stale` because `--offline` was given |
| 1 | Not written: an invalid `aliases.toml` or `contracts.toml`, a sentinel naming an unknown key, an inventory error, a redirected registry (without `--dry-run`), a source with no usable data. Also `show`/`explain` with no readable file or no match |
| 2 | Written, with a source that fell back to a snapshot |
| 3 | Not written: a sentinel's pinned votes or tier differ. The sentinel table is printed; the fix is an edit to `contracts.toml` |

1 takes precedence over 3 and 3 over 2. Unmapped rows and unresolved records do not change the exit code.

**Redirected registry.** wt follows `WT_REGISTRY`; the output path does not. When the registry path the inventory reports is not `registry.toml` in the output file's directory and `MODELTIERS_OUT` is unset, `run` exits 1 before any fetch. `run --dry-run` writes nothing, so it goes on: it prints its whole report, compared against the default output file, after a `warning:` on stderr that the same run without `--dry-run` is refused; its exit code is the one the report gives (0, 2 or 3). Set `MODELTIERS_OUT` for any run against a scratch registry.

Development: `make install` (`uv sync`), `make check` (ruff lint, `ruff format --check`, mypy strict on `src/`), `make test` (pytest with coverage; fails under the floor in `pyproject.toml`), `make all`. One test: `uv run pytest tests/test_loader.py::test_a_clean_fetch_is_ok_and_saved`.

## Files and environment

| Thing | Default | Override |
|---|---|---|
| Output file | `$XDG_CONFIG_HOME/local-ai/tiers.toml`, else `~/.config/local-ai/tiers.toml` | `MODELTIERS_OUT` |
| Snapshots | `$XDG_CACHE_HOME/local-ai/modeltiers/`, else `~/.cache/local-ai/modeltiers/` | `MODELTIERS_CACHE` |
| Aliases | `src/modeltiers/data/aliases.toml` (committed, package data) | none |
| Contracts | `src/modeltiers/data/contracts.toml` (committed, package data) | none |

An ad-hoc run from a shell must set `MODELTIERS_CACHE` and `MODELTIERS_OUT` to scratch paths unless it is meant to touch the real cache and the real `tiers.toml`.

## Pipeline

One module per stage, plain frozen dataclasses (`records.py`) in and out.

| Module | Job |
|---|---|
| `inventory.py` | `wt model list --json` or `--inventory FILE` to an `Inventory`. Drops rows whose `model_name` is `native`; keeps discovered rows and rows with missing weights. The join key downstream is (`provider_id`, `model_name`), never the registry `id`. `provider_warnings` names each provider whose probe status is not `ok`. |
| `fetch.py` | One function per source behind `Fetcher` (`Callable[[str], bytes]`). `http_fetcher` has a 30-second timeout; `fetch_with_retry` tries once more after 2 seconds on a `FetchError` with `retryable` set (timeout, 5xx, Arena `error` body). `fetch_arena` pages the Hugging Face rows API to `num_rows_total` and returns one wrapper document, `{"pages": [...]}`, each page body unparsed. |
| `snapshots.py` | `<cache>/<source>/<UTC stamp>.<ext>`; `mark_bad` renames to `<stamp>.bad.<ext>`; `good_snapshots` newest first; `prune` keeps 4 good and 2 bad; `run` calls it for each source after a successful write, never under `--dry-run` or `--offline` and never after a failed sentinel check. The store takes no lock: two runs started in the same second share a stamped file, and both `mark_bad` and a snapshot whose file has gone are tolerated rather than fatal. |
| `sources/*.py` | Pure parsers, bytes to records. An unrecognised shape or a count under `min_rows` raises `SourceParseError`. `min_rows` defaults to the spec's floor (models.dev 1,000, OpenRouter 100, Arena 20, Epoch 100). |
| `loader.py` | `load_source`: fetch, save, parse; on a failure fall back to the newest good snapshot (`status = "stale"`, `fallback = True`); with none, `SourceUnavailable`. `load_all` loads the four and names every unavailable source in one error. |
| `tokens.py` | `tokens(name)`: the token function, the same for a name and for a canonical key. `is_floating(key)`: a key ending in `-latest`. |
| `aliases.py` | `load_aliases` / `parse_aliases`: `aliases.toml` to an `Aliases` with four lookups. Refuses, before any fetch, an unknown key, a matched string with a space at its start or end, an `[[ignore]]` without a reason and a pair or source id claimed twice. `check_anchored` (needs models.dev) refuses an alias whose canonical is not a models.dev key and lists no source id; `check_spelling` refuses a declared key whose token sequence another key already has. `SOURCES` is the one list of benchmark source names, built from the parsers' `SOURCE` constants. `stale_ids` gives a warning per alias source id absent from this run. |
| `identity.py` | `build_index` (keys, models.dev rows per key, host rows, token sequences), `host_row`, `resolve_row` (inventory), `resolve_record` and `resolve_source` (benchmark records). Order: alias, host row, normalization; `resolved_by` records which. |
| `contracts.py` | `load_contracts` / `parse_contracts`: `contracts.toml` to a `Contracts` (four `Tier`s, three `Signal`s, four attribution lines, the `Sentinel`s). Refuses an unknown key, floors that do not rise, a number of `marathon_ci_lower` carriers other than one, a sentinel naming an unknown tier or signal. Also the constants `TIERS`, `RANK`, `SIGNALS`, `SIGNAL_SOURCE`, `SOURCES`, and `STAMP`, the timestamp format of `tiers.toml`. |
| `assign.py` | Pure functions, one key at a time. `weigh` (votes, lower-median base tier, marathon promotion: the pre-gate tier, plus scores, efforts and confidence), `check_sentinel`, `gather` (the context table: what models.dev says for the gates), `decide` (tool-calling gate, context gate, final tier and `reason`). |
| `pipeline.py` | `resolve_all(inventory, sources, aliases)` to an `Identity`; `assign_all(identity, contracts)` to an `Assignment`: a `ModelResult` per resolved or ignored row, the `Candidate`s, the unresolved records worth listing, a `SentinelResult` per sentinel. Raises `ConfigError` for a sentinel whose key is unknown. |
| `output.py` | `build_document` (the whole file as a dict), `render` (TOML text in the spec's table order, values by `tomli_w`), `write_document` (temporary file in the same directory, then rename), `read_document`, `read_previous`. |
| `report.py` | What `run` prints from pipeline objects: `identity_lines`, `unresolved_lines`, `sentinel_lines`. |
| `changes.py` | `change_lines(old, new, path)`: the change report, from two documents. A row that left `[[unmapped]]` is `newly resolved`, or `now ignored: <pair> (<reason>)` when an `[[ignore]]` covers it. |
| `show.py` | What `show` and `explain` print, from one document: `table_lines`, `age_line`, `stale_warning`, `attribution_lines`, `explain_lines`. Of the package it imports only constants from `contracts.py`. |
| `cli.py` | `main(argv, *, deps=None)`; `run`, `show`, `explain`. `Deps` carries every seam, `aliases_path` and `contracts_path` included. |

Parser details worth knowing:

- **OpenRouter** gives one record per model that has an `intelligence_index`. A model with no `benchmarks` or a null `artificial_analysis` is not a record, and the row minimum counts records.
- **Arena** removes every parenthesis whose content is an effort word and keeps the word in `ScoreRecord.effort`: `DeepSeek V4 Pro (High) (0813)` becomes `DeepSeek V4 Pro (0813)` with effort `high`. `Qwen3.8 Max` is untouched.
- **Epoch** reads `epoch_capabilities_index/eci_scores.csv` from the ZIP, keyed on the `Display name` column.
- **models.dev** keeps `canonical_model_id` in its own spelling (`minimax/MiniMax-M3`); a host row without one is kept with `canonical = None`. A *provider* entry with no `models` object — or that is not an object — is a shape change and fails the whole source, which then falls back to its newest good snapshot. It is not a provider with no models: no default would be true, and a parser here has no way to report a partial row set, so quietly dropping a provider's rows would be a short list nobody could see.

## Identity

- **Canonical key.** `vendor/model` exactly as models.dev spells `canonical_model_id` (`minimax/MiniMax-M3`). Stored in that spelling, compared in lower case: `Index.keys`, `Index.rows` and `SourceIdentity.best` are keyed by the lower-case key and hold the stored spelling as the value. An alias written in another case reaches the same key. An alias that declares a key reading the same as another (`anthropic/claude-opus-5.5` beside models.dev's `anthropic/claude-opus-5-5`) is refused: it would be a second key for one model and would stop name matching for both.
- **Host rows.** Inventory `provider_id` `ollama` (cloud rows only) is looked up in models.dev's `ollama-cloud` provider with `:cloud` or `-cloud` removed; `openrouter` rows and OpenRouter records in the `openrouter` provider with `:free` or `:batch` removed. The id as written is tried first: models.dev lists some `:free` ids as rows of their own, sometimes the model's only row and sometimes with another context than the base row. A host row with no canonical id does not answer, but stays on `RowIdentity.host_row`: the next step needs its context.
- **Normalization** answers only when exactly one key has the name's token sequence. A `-latest` key is never in that index. A row resolved this way is on the review list; when it also passed a host row with no canonical id it carries the note `host row has no canonical id; matched by name`.
- **Aliases** match the inventory pair or the source's own id exactly. Arena ids are compared with the effort parenthesis removed, in the file and in the records, so one alias covers every effort row.
- **Three states for an inventory row:** resolved (`canonical`, `resolved_by`), ignored (`ignore_reason`), unmapped (neither). `RowIdentity` raises `ValueError` for any other combination. A local row has `basis == "inherited"`.
- **Records that share a key** (effort rows, `:free` and `:batch`, dated and undated names): the best score is kept as a whole record, so its effort and interval are its own; a tie keeps the earlier record.
- **Unresolved records** are kept in full on `SourceIdentity.unresolved`. `[[unresolved]]` lists the best record of each id at or above its signal's standard floor; `[sources.*].unresolved` counts them all.

## Assignment

The order is the spec's and is fixed: votes, base tier, marathon promotion, sentinel check, tool-calling gate, context gate.

- **Votes.** Each signal (`intelligence_index` from OpenRouter, `arena_agent` from Arena, `eci` from Epoch) votes the highest tier whose floor its score reaches, capped at its `max_vote`; under the quick floor it votes `unrated`; with no record it abstains. `coding_index` and `agentic_index` are written to `scores` and never vote.
- **Base tier** is the lower median: the vote at index `(n-1)//2` of the votes sorted lowest first. Two votes give the lower.
- **Marathon** is reached one way: base tier `deep` and the Arena record's `score_ci_lower` at or above `marathon_ci_lower`.
- **Sentinels** pin votes and the pre-gate tier, so a source drifting under an unchanged median is still caught. A failure writes nothing (exit 3) and there is no override flag.
- **Gates.** Tool calling: the key has models.dev rows and none has `tool_call`. Context: below the tier's `context_min` the entry drops to the highest tier it meets, with a `gate_context:` note, or to `unrated`. Context is the cloud row's own host row's; else the largest among the key's rows (noted); unknown for a local build and for a key with no models.dev rows (noted, gate skipped).
- **`reason`** is on every `unrated` entry: `no_votes`, `below_floor`, `gate_tool_call`, `gate_context`, `not_applicable` (an `[[ignore]]`d row).
- **Confidence** comes from the votes alone: `high` (two or more, equal), `medium` (ranks span one), `low` (one vote, or a span of two or more), `none`.
- **Re-calibrating** is editing `src/modeltiers/data/contracts.toml`: a floor, a context minimum or a sentinel's pinned values.

## Output file

`docs/contracts/tiers.sample.toml` is the contract: it is what `run` writes from the test fixtures, and two tests compare against it apart from timestamps. Regenerate it after a deliberate format change with `MODELTIERS_OUT` and `MODELTIERS_CACHE` pointed at scratch paths; never hand-edit it.

- Top level: `generated_at`, `contracts_version`, `[inventory]`, `[sources.<name>]`, then `[[models]]`, `[[candidates]]`, `[[unmapped]]`, `[[unresolved]]`. An array with no entries is absent.
- Left out when unknown: `reason` (rated entries), `tool_call` (a key with no models.dev rows), `context`, any score a source did not give, `canonical` and `resolved_by` (an ignored row), `unresolved` under `[sources.modelsdev]`. `open_weights` is not a `[[models]]` field: the spec gives it to `[[candidates]]` entries only.
- Scores are written as the source gave them; `show` and `explain` round to four places for display only.
- **Contract for readers:** a model absent from the file, or in `[[unmapped]]`, is `unrated`; a file that cannot be parsed is treated as absent. `run` applies the same rule to the existing file when it builds the change report.

## Testing patterns

- **Nothing live.** The autouse `_hermetic` fixture in `tests/conftest.py` makes `urllib.request.urlopen` and `subprocess.run` raise and points `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `MODELTIERS_OUT` and `MODELTIERS_CACHE` under `tmp_path`. `tests/test_conftest_guards.py` fails if that stops holding. CI's `bin/check-config-dirs-untouched` covers the config home only; nothing but this fixture protects the real cache.
- **Seams are parameters.** The network is a `Fetcher`, the wait is a `Sleep`, the clock is a `now`, wt is a `RunWt`, the two data files are `Deps.aliases_path` and `Deps.contracts_path`. Tests pass `fakes.FakeFetcher` and `fakes.Sleeps` (`tests/fakes.py`), or a whole `cli.Deps`.
- **Small fixtures.** `tests/fixtures/` holds a few rows per source, so tests pass small minimums: the `specs` fixture is `loader.build_specs(modelsdev_min=12, openrouter_min=6, arena_min=6, epoch_min=6)`. The Epoch fixtures are CSV text; the `epoch_zip` fixture zips one in memory. The Arena fixtures are single page bodies; `arena_doc` wraps them as `fetch_arena` would.
- **Fixture-local data files.** Tests pass `tests/fixtures/aliases.toml` and `tests/fixtures/contracts.toml` (as `Deps.aliases_path` / `Deps.contracts_path`, or through the `fixture_aliases` and `rule` fixtures), or build an `Aliases` with `aliases.build`. Only `tests/test_data_files.py` reads the committed files under `src/modeltiers/data/`, and it asserts nothing about which entries or values they hold, so calibration changes no test.
- **Shared pipeline fixtures** in `tests/conftest.py`: `inventory`, `sources`, `fixture_aliases`, `rule`, `assignment` (the golden fixtures resolved and assigned) and `document` (`tiers.toml` for them, as a dict, generated at `T0`). `SAMPLE` and `without_timestamps` are there for the two tests that compare against `docs/contracts/tiers.sample.toml`.
- **Adding a row to a golden fixture** moves the counts pinned in `tests/test_source_*.py`, `tests/test_loader.py`, `tests/test_pipeline.py`, `tests/test_cli.py`, the `specs` minimums, and `docs/contracts/tiers.sample.toml`.
- **Identity tests build their own models.dev.** `tests/test_identity.py` and `tests/test_identity_sources.py` use hand-built `ModelsDev` rows; `tests/test_pipeline.py` and `tests/test_cli.py` use the golden fixtures.
- **A new benchmark source** is a `SOURCE` constant in its parser, an entry in `aliases.SOURCES` and a field of that name on `Alias`, `loader.Sources` and `pipeline.Identity`; `test_every_structure_keyed_by_source_has_a_field_per_source` fails until all three have it.
- `tests/fixtures/README.md` credits each source. Keep it current when a fixture is added.
