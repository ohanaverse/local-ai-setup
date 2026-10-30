# CLAUDE.md

Guidance for Claude Code when working in `modeltiers/`. Mechanism detail (full command reference, module map, identity/assignment rules, data-file procedures, file-format notes, test patterns) is in [`docs/internals.md`](docs/internals.md); this file holds the rules, gotchas and commands.

## Project overview

`modeltiers` is a Python 3.13 CLI (argparse) that writes an advisory capability tier (`marathon`, `deep`, `standard`, `quick` or `unrated`) for every model wt lists, from four public sources, into `tiers.toml`. Nothing routes on a tier yet. Design: `../docs/superpowers/specs/2026-10-12-model-tiers-design.md`. User guide: `../docs/guides/12-model-tiers.md`. The committed `aliases.toml` and `contracts.toml` were calibrated against the live sources and the owner's inventory on 2026-10-12 (the contracts' `version` is `2026-10-21`: the Fable 5.1 and MiniMax-M3 sentinels were re-pinned after a source re-score, floors kept): every sentinel held, and each alias rests on evidence that identifies its model. What that run could not identify was left listed, not guessed; the record is the Calibration Record in `../docs/superpowers/plans/2026-10-12-model-tiers-step4-calibration-docs.md`.

It never writes `registry.toml`, `config.yaml` or any wt state, and it never reads `registry.toml`: the inventory comes only from `wt model list --json`.

## Commands

Run from the repo root as `uv run --directory modeltiers modeltiers <command>`, or from this directory as `uv run modeltiers <command>`. `--directory` makes `modeltiers/` the working directory, so a relative `--inventory` path resolves from here. Full flag semantics and what `run` prints: [internals, Command reference](docs/internals.md#command-reference).

| Command | Effect |
|---|---|
| `modeltiers run [--dry-run] [--offline] [--inventory FILE]` | Validate the data files (`aliases.toml`, `contracts.toml`, `vendors.toml`), read the inventory, load the four sources, follow each closed lab's catalog address through its redirects (not under `--offline`), resolve, assign tiers, check sentinels, print the report, write `tiers.toml`. `--dry-run` does everything except write and returns the code the real run would (except under a redirected registry, below). `--offline` fetches nothing and reads each source's newest good snapshot. `--inventory FILE` reads saved `wt model list --json` output and does not run wt. |
| `modeltiers show [--candidates] [--sort COLUMN] [--wide] [--html FILE]` | The tier table from `tiers.toml`, led by the file's age in days (a `warning:` on stderr past 14), rows by aligned score, highest first, unless `--sort` says otherwise. `--html FILE` writes one self-contained sortable page, opening in that order, with each canonical key linked to its model card (Hugging Face, the lab's catalog of models, or OpenRouter), prints `wrote <absolute path>`, and refuses (exit 1) a `FILE` that is the output file under any name (symlink, case variant). |
| `modeltiers explain <model>` | One entry's identity, tier, votes, scores, aligned score, gates and notes. Argument: exact `provider_id/model_name`, else a canonical key in any case. No match exits 1. |
| `modeltiers ladder <signal> [--low SCORE] [--high SCORE]` | One signal's resolved scores highest first with gaps and midpoints; the tool for moving a floor (guide step 5). No window match exits 1. |

`show` and `explain` read only the output file: no fetch, no wt, no `contracts.toml`. `ladder` reads `aliases.toml` and the newest good snapshots, as `run --offline` would: no fetch, no wt, no output file, no `contracts.toml`.

| Exit | Meaning |
|---|---|
| 0 | Written. Every source `ok`, or `stale` because `--offline` was given |
| 1 | Not written: an invalid `aliases.toml` or `contracts.toml`, a sentinel naming an unknown key, an inventory error, a redirected registry (without `--dry-run`), a source with no usable data. Also `show`/`explain` with no readable file or no match |
| 2 | Written, with a source that fell back to a snapshot |
| 3 | Not written: a sentinel's pinned votes or tier differ. The sentinel table is printed; the fix is an edit to `contracts.toml` |

1 takes precedence over 3 and 3 over 2. Unmapped rows and unresolved records do not change the exit code.

A `run` that fails (exit 1 or 3) ends its stderr with a blank line and `ERROR: modeltiers run FAILED (exit N): <reason>; tiers.toml was not written.` (`cli._run_failed`); exit 2 is a warning, and `show`, `explain` and `ladder` keep the plain `modeltiers: <message>`. For an exit 3, the `sentinel-failure` skill (`.claude/skills/`) investigates, recommends a `contracts.toml` fix and asks before editing.

**Redirected registry.** wt follows `WT_REGISTRY`; the output path does not. When the registry path the inventory reports is not `registry.toml` in the output file's directory and `MODELTIERS_OUT` is unset, `run` exits 1 before any fetch. `run --dry-run` writes nothing, so it goes on: it prints its whole report, compared against the default output file, after a `warning:` on stderr that the same run without `--dry-run` is refused; its exit code is the one the report gives (0, 2 or 3). Set `MODELTIERS_OUT` for any run against a scratch registry.

Development: `make install` (`uv sync`), `make check` (ruff lint, `ruff format --check`, mypy strict on `src/`), `make test` (pytest with coverage; fails under the floor in `pyproject.toml`), `make all`. One test: `uv run pytest tests/test_loader.py::test_a_clean_fetch_is_ok_and_saved`.

## Files and environment

| Thing | Default | Override |
|---|---|---|
| Output file | `$XDG_CONFIG_HOME/local-ai/tiers.toml`, else `~/.config/local-ai/tiers.toml` | `MODELTIERS_OUT` |
| Snapshots | `$XDG_CACHE_HOME/local-ai/modeltiers/`, else `~/.cache/local-ai/modeltiers/` | `MODELTIERS_CACHE` |
| Vendor links | `<snapshots>/vendor-links.toml`: where each closed lab's catalog address last ended after redirects | none |
| Aliases | `src/modeltiers/data/aliases.toml` (committed, package data) | none |
| Contracts | `src/modeltiers/data/contracts.toml` (committed, package data) | none |
| Vendors | `src/modeltiers/data/vendors.toml` (committed, package data): each closed lab's catalog of models, for card links | none |

An ad-hoc run from a shell must set `MODELTIERS_CACHE` and `MODELTIERS_OUT` to scratch paths unless it is meant to touch the real cache and the real `tiers.toml`.

## Pipeline

One module per stage (`inventory`, `fetch`, `snapshots`, `sources/*`, `loader`, `configfile`, `tokens`, `aliases`, `identity`, `contracts`, `assign`, `align`, `pipeline`, `cards`, `output`, `report`, `changes`, `show`, `page`, `ladder`, `cli`), plain frozen dataclasses (`records.py`) in and out. Per-module jobs and function lists: [internals, Pipeline](docs/internals.md#pipeline-module-map). Rules that bite:

- The inventory join key is (`provider_id`, `model_name`), never the registry `id`; rows with `model_name` `native` are dropped.
- Sources are pure parsers (bytes to records); an unrecognised shape or a count under `min_rows` raises `SourceParseError`. A models.dev *provider* entry with no `models` object is a shape change that fails the whole source (falls back to its newest good snapshot), never a quiet empty provider.
- Snapshots: `<cache>/<source>/<UTC stamp>.<ext>`; bad ones are renamed `<stamp>.bad.<ext>`; `prune` keeps 4 good and 2 bad and runs only after a successful write, never under `--dry-run` or `--offline`, never after a failed sentinel check. The store takes no lock; tolerate a shared stamp and a vanished file.
- `aliases.py` refuses, before any fetch, an unknown key, a matched string with leading/trailing space, an `[[ignore]]` without a reason and a pair or source id claimed twice; it also refuses an alias whose canonical is not a models.dev key and lists no source id, and a declared key whose token sequence another key already has.
- `align.py`'s aligned score is advisory and read by no tier, gate or sentinel. `show.py` re-derives it for `explain` and imports nothing from `align.py` but `ANCHOR`; `test_explain_translates_a_score_as_the_run_that_wrote_the_file_did` holds the two to one rule.
- `show.py` and `page.py`: a new numeric sort column is one `_SORT_VALUES` entry plus a page column (`tests/test_page.py` enforces it); the page writes nothing from the document as markup (JSON in a non-executing script element, cells set with `textContent`) and loads nothing external. Details in internals.
- `cards.py`: a card link is worked out by `run` and written as `card_kind`/`card_url`, so `show` and the page still read the file alone and fetch nothing. The order is fixed: OpenRouter's `hugging_face_id`, the lab's catalog in `vendors.toml`, the OpenRouter page, no link. Only an https address reaches a page's `href` (`cards.is_https`, checked when `run` writes and again when the page reads); redirects are followed in `run` alone, never `--offline`, `show` or the page.
- `cli.py`: `main(argv, *, deps=None)`; `Deps` carries every seam (`aliases_path`, `contracts_path`, `vendors_path`, `resolver`, `align_min` included).

## Identity

Canonical key is `vendor/model` in models.dev's spelling, compared in lower case; an alias declaring a key that reads the same as another (`anthropic/claude-opus-5.5` beside `anthropic/claude-opus-5-5`) is refused. Order: alias, host row, normalization (`resolved_by` records which); normalization answers only when exactly one key has the name's token sequence, and a `-latest` key is never in that index. Rows resolved by normalization go on the review list. An inventory row is in exactly one state: resolved, ignored or unmapped (`RowIdentity` raises `ValueError` otherwise); a local row has `basis == "inherited"`. Host-row lookup, tie-keeping and unresolved-record rules: [internals, Identity](docs/internals.md#identity).

## Assignment

The order is the spec's and is fixed: votes, base tier, marathon promotion, sentinel check, tool-calling gate, context gate.

- `coding_index` and `agentic_index` are written to `scores` and never vote; only `intelligence_index`, `arena_agent` and `eci` vote.
- Base tier is the lower median of the votes. Marathon is reached one way: base `deep` and the Arena `score_ci_lower` at or above `marathon_ci_lower`.
- Sentinels pin votes and the pre-gate tier. A failure writes nothing (exit 3) and there is no override flag.
- Every `unrated` entry has a `reason` (`no_votes`, `below_floor`, `gate_tool_call`, `gate_context`, `not_applicable`).
- Re-calibrating is editing `src/modeltiers/data/contracts.toml`. Votes, gates and confidence: [internals, Assignment](docs/internals.md#assignment).

## The two data files

Both are package data under `src/modeltiers/data/`, loaded with `importlib.resources` and validated on every run, before wt runs and before any fetch. Editing one is the only way to change what a run decides: no flag or environment variable overrides either. Procedure and evidence commands: `../docs/guides/12-model-tiers.md` steps 4 and 5; rules in full: [internals, data files](docs/internals.md#the-two-data-files-procedures-and-evidence-rules).

**`aliases.toml`** answers identity by hand.

- Every `[[alias]]` has a `note` stating its evidence and the date read (`tests/test_data_files.py` fails without one). Evidence is an id the publisher, distributor or benchmark source itself gives, leading to exactly one key. A matching name, release date, architecture or parameter count is not evidence, alone or together. Never alias on the name alone: normalization already does that.
- No evidence, no entry: the row stays listed, with no alias and no ignore. `[[ignore]]` is for a finding (with a `reason` saying what was checked and when); the owner's own knowledge counts, noted `owner-confirmed <date>`.
- An alias whose `canonical` is not a models.dev key must list a source id (`check_anchored`); it becomes a candidate with both gates skipped. Replace it with models.dev's key when one appears.
- Stale without a message: an inventory pair that left the inventory is not reported, and an `[[ignore]]` is checked before every other step, so an ignored row stays `unrated` after models.dev gains a key. Re-check entries whose reason ends `(checked <date>)`.
- The review list is never emptied; checking an entry changes nothing unless the match is wrong, then it gets an `[[alias]]`.

**`contracts.toml`** is the tier rule: a code and context minimum per tier, three floors per signal, the one `marathon_ci_lower`, attribution lines and the sentinels.

- A failed sentinel is fixed here and nowhere else (one signal off: re-pin that vote; several off the same way on one signal: move that signal's floors to the midpoint of the widest gap, using `modeltiers ladder`; a signal that `voted nothing`: drop it from the sentinel or pick another). Guide step 5 has the procedure.
- Change `version` with every edit (`tiers.toml` records it as `contracts_version`).
- Check an edit with `modeltiers run --dry-run --offline`; a run that fails sentinels writes nothing, so compare against the file the last passing run wrote (guide step 5's `tierdump` through `diff` for the full per-key comparison).

## Inventory contract

`docs/contracts/model-list.sample.json` is one `wt model list --json` document, shared with wt: wt's `cmd/wt/model_list_contract_test.go` fails when it and the command's output differ in a key or JSON type, and these tests read it as the golden inventory (`INVENTORY` in `tests/conftest.py`). A key wt renames reaches this suite through the fixture, in a change that runs `modeltiers-ci`. The registry path in it is made up; `size_bytes` and `path` are nulled. It is not in `tests/fixtures/`; a row added to it moves the same pinned counts as a golden-fixture row, and wt's test must still pass.

## Output file

`docs/contracts/tiers.sample.toml` is the contract: what `run` writes from the test fixtures and the inventory contract, compared by two tests apart from timestamps. wt's `wt/internal/tiers/contract_test.go` parses it too (model picker TIER column), so a renamed or retyped `[[models]]` key fails wt-ci as well. Regenerate it after a deliberate format change with `MODELTIERS_OUT` and `MODELTIERS_CACHE` at scratch paths; never hand-edit it. Table layout, omitted-when-unknown keys and aligned-score reconstruction: [internals, Output file](docs/internals.md#output-file-format-notes).

- Scores are written as the source gave them; `show`, `explain` and the page round to four places for display only.
- **Contract for readers:** a model absent from the file, or in `[[unmapped]]`, is `unrated`; a file that cannot be parsed is treated as absent. `run` applies the same rule to the existing file for the change report.

## Testing patterns

Full detail: [internals, Testing patterns](docs/internals.md#testing-patterns-detail).

- **Nothing live.** The autouse `_hermetic` fixture in `tests/conftest.py` makes `urllib.request.urlopen` and `subprocess.run` raise and points `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `MODELTIERS_OUT` and `MODELTIERS_CACHE` under `tmp_path`. `tests/test_conftest_guards.py` fails if that stops holding. CI's `bin/check-config-dirs-untouched` covers the config home only; nothing but this fixture protects the real cache.
- **Seams are parameters.** Network is a `Fetcher`, the wait a `Sleep`, the clock a `now`, wt a `RunWt`, the data files `Deps.aliases_path`/`Deps.contracts_path`. Use `fakes.FakeFetcher`, `fakes.Sleeps` (`tests/fakes.py`) or a whole `cli.Deps`.
- **Small fixtures and fixture-local data files.** Tests use `tests/fixtures/aliases.toml` and `contracts.toml`, small minimums (`specs` fixture, `ALIGN_MIN = 3`). Only `tests/test_data_files.py` reads the committed data files, and only asserts they load and every alias has a note, so calibration changes no test.
- **Shared pipeline fixtures** live in `tests/conftest.py` (`inventory`, `sources`, `fixture_aliases`, `rule`, `assignment`, `document`, `out`, `scaled_openrouter`). Read the golden inventory through `INVENTORY`, never a path of your own.
- **Calibration moves** are pinned over the fixtures by `tests/test_calibration.py`; no other test reads its aliases file.
- **The guide's samples are pinned.** `tests/test_guide_samples.py` fails when a ```` ```text ```` block in `../docs/guides/12-model-tiers.md` is not a word-for-word slice of what the fixtures make `run` and `explain` print, when the `tiers.toml` example in `../docs/guides/00-config-map.md` is not a slice of `docs/contracts/tiers.sample.toml`, or when a guide command block names a fixture model. A change to a golden fixture or a printed line needs the guide sample updated in the same commit; both guides are in `modeltiers-ci`'s path filter for this reason.
- **Adding a row to a golden fixture** moves counts pinned in `tests/test_source_*.py`, `test_loader.py`, `test_pipeline.py`, `test_cli.py`, `test_calibration.py`, the `specs` minimums, `docs/contracts/tiers.sample.toml` and the two guides' samples.
- **A new benchmark source** is a `SOURCE` constant in its parser, an entry in `aliases.SOURCES` and a field of that name on `Alias`, `loader.Sources` and `pipeline.Identity`; `test_every_structure_keyed_by_source_has_a_field_per_source` fails until all three have it.
- **The page is tested as text** (`tests/test_page.py`); no test runs its script. After changing `data/page.html`, open a page written from a real `tiers.toml` and click through the headings and filters.
- `tests/fixtures/README.md` credits each source; keep it current when a fixture is added.
