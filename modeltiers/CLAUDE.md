# CLAUDE.md

Guidance for Claude Code when working in `modeltiers/`.

## Project overview

`modeltiers` is a Python 3.13 CLI (argparse) that will write an advisory capability tier for every model wt lists, from four public sources. Design: `../docs/superpowers/specs/2026-10-12-model-tiers-design.md`. The spec's Delivery section lists four steps; this package is at step 1: it reads the inventory, downloads and parses the sources, and reports on them. It resolves no names, assigns no tiers and writes no output file yet.

It never writes `registry.toml`, `config.yaml` or any wt state, and it never reads `registry.toml`: the inventory comes only from `wt model list --json`.

## Commands

Run from the repo root as `uv run --directory modeltiers modeltiers <command>`, or from this directory as `uv run modeltiers <command>`. `--directory` makes `modeltiers/` the working directory, so a relative `--inventory` path resolves from here.

| Command | Effect |
|---|---|
| `modeltiers run [--dry-run] [--offline] [--inventory FILE]` | Read the inventory, load the four sources, print one line for the inventory and one per source (status, record count, download time). `--offline` fetches nothing and reads each source's newest good snapshot. `--inventory FILE` reads saved `wt model list --json` output and does not run wt. At this step `run` writes no output file with or without `--dry-run`, and says so on its last line. |

Exit codes: 0 every source `ok` (or `stale` because of `--offline`); 1 the inventory could not be read, or a source failed and has no good snapshot; 2 a source failed and its last good snapshot was used.

Development: `make install` (`uv sync`), `make check` (ruff lint, `ruff format --check`, mypy strict on `src/`), `make test` (pytest with coverage; fails under the floor in `pyproject.toml`), `make all`. One test: `uv run pytest tests/test_loader.py::test_a_clean_fetch_is_ok_and_saved`.

## Files and environment

| Thing | Default | Override |
|---|---|---|
| Output file (not written yet) | `$XDG_CONFIG_HOME/local-ai/tiers.toml`, else `~/.config/local-ai/tiers.toml` | `MODELTIERS_OUT` |
| Snapshots | `$XDG_CACHE_HOME/local-ai/modeltiers/`, else `~/.cache/local-ai/modeltiers/` | `MODELTIERS_CACHE` |

## Pipeline

One module per stage, plain frozen dataclasses (`records.py`) in and out.

| Module | Job |
|---|---|
| `inventory.py` | `wt model list --json` or `--inventory FILE` to an `Inventory`. Drops rows whose `model_name` is `native`; keeps discovered rows and rows with missing weights. The join key downstream is (`provider_id`, `model_name`), never the registry `id`. `provider_warnings` names each provider whose probe status is not `ok`. |
| `fetch.py` | One function per source behind `Fetcher` (`Callable[[str], bytes]`). `http_fetcher` has a 30-second timeout; `fetch_with_retry` tries once more after 2 seconds on a `FetchError` with `retryable` set (timeout, 5xx, Arena `error` body). `fetch_arena` pages the Hugging Face rows API to `num_rows_total` and returns one wrapper document, `{"pages": [...]}`, each page body unparsed. |
| `snapshots.py` | `<cache>/<source>/<UTC stamp>.<ext>`; `mark_bad` renames to `<stamp>.bad.<ext>`; `good_snapshots` newest first; `prune` keeps 4 good and 2 bad. `prune` is not called by any command yet. The store takes no lock: two runs started in the same second share a stamped file, and both `mark_bad` and a snapshot whose file has gone are tolerated rather than fatal. |
| `sources/*.py` | Pure parsers, bytes to records. An unrecognised shape or a count under `min_rows` raises `SourceParseError`. `min_rows` defaults to the spec's floor (models.dev 1,000, OpenRouter 100, Arena 20, Epoch 100). |
| `loader.py` | `load_source`: fetch, save, parse; on a failure fall back to the newest good snapshot (`status = "stale"`, `fallback = True`); with none, `SourceUnavailable`. `load_all` loads the four and names every unavailable source in one error. |
| `cli.py` | `main(argv, *, deps=None)`. `Deps` carries every seam. |

Parser details worth knowing:

- **OpenRouter** gives one record per model that has an `intelligence_index`. A model with no `benchmarks` or a null `artificial_analysis` is not a record, and the row minimum counts records.
- **Arena** removes every parenthesis whose content is an effort word and keeps the word in `ScoreRecord.effort`: `DeepSeek V4 Pro (High) (0813)` becomes `DeepSeek V4 Pro (0813)` with effort `high`. `Qwen3.8 Max` is untouched.
- **Epoch** reads `epoch_capabilities_index/eci_scores.csv` from the ZIP, keyed on the `Display name` column.
- **models.dev** keeps `canonical_model_id` in its own spelling (`minimax/MiniMax-M3`); a host row without one is kept with `canonical = None`. A *provider* entry with no `models` object — or that is not an object — is a shape change and fails the whole source, which then falls back to its newest good snapshot. It is not a provider with no models: no default would be true, and a parser here has no way to report a partial row set, so quietly dropping a provider's rows would be a short list nobody could see.

## Testing patterns

- **Nothing live.** The autouse `_hermetic` fixture in `tests/conftest.py` makes `urllib.request.urlopen` and `subprocess.run` raise and points `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `MODELTIERS_OUT` and `MODELTIERS_CACHE` under `tmp_path`. `tests/test_conftest_guards.py` fails if that stops holding. CI's `bin/check-config-dirs-untouched` covers the config home only; nothing but this fixture protects the real cache.
- **Seams are parameters.** The network is a `Fetcher`, the wait is a `Sleep`, the clock is a `now`, wt is a `RunWt`. Tests pass `fakes.FakeFetcher` and `fakes.Sleeps` (`tests/fakes.py`), or a whole `cli.Deps`.
- **Small fixtures.** `tests/fixtures/` holds a few rows per source, so tests pass small minimums: the `specs` fixture is `loader.build_specs(modelsdev_min=8, openrouter_min=4, arena_min=5, epoch_min=5)`. The Epoch fixtures are CSV text; the `epoch_zip` fixture zips one in memory. The Arena fixtures are single page bodies; `arena_doc` wraps them as `fetch_arena` would.
- `tests/fixtures/README.md` credits each source. Keep it current when a fixture is added.
