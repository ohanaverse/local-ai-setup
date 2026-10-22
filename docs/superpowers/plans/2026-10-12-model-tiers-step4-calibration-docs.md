# modeltiers Step 4: Calibration and Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The committed `aliases.toml` is calibrated so that every alias rests on evidence that identifies its model, a live `modeltiers run` on the owner's machine leaves no inventory row unmapped and every sentinel holding, and the only benchmark records it leaves unresolved are the ones this plan records as open for want of evidence. The user guide, the config map and the two `CLAUDE.md` files describe the finished tool. The spec is not edited here: the owner corrects it on the docs branch, and it already carries the list of that day's tiers as this calibration found it.

**Architecture:** No source module changes. Calibration is data: `modeltiers/src/modeltiers/data/aliases.toml` grows from one alias and two ignores to eleven aliases and five ignores, each alias carrying the evidence it rests on; `contracts.toml` is confirmed as it stands. The moves a calibration makes (an alias or an ignore for an unmapped row, a declared key for an unresolved record, re-pinning a moved sentinel) are pinned over the test fixtures by one new test file, so the committed data stays free to change without changing a test. The docs are written from the working tool, and a second new test file holds their sample output to what the fixtures really print.

**Tech Stack:** Python `==3.13.*`, uv, pytest (no new dependency, no new source file). For the live re-verification: `wt model list --json`, `curl`, `jq`, `shasum`, `unzip`, `python3`. Markdown for the docs; `bin/check-links` for their links.

**Spec:** docs/superpowers/specs/2026-10-12-model-tiers-design.md

**Verified against:** main at 51887c0, and a working prototype of this step. The calibration was performed, not planned. The prototype ran `modeltiers run` against the four live sources and the owner's real `wt model list --json` output on 2026-10-12 (17:28 and 17:32 UTC), with `MODELTIERS_OUT` and `MODELTIERS_CACHE` in a scratch directory. With step 3's `aliases.toml` that run left 10 rows unmapped and 6 records unresolved. After the coverage review tightened the evidence rule, and again after a second review withdrew the `Gemini 3.1 Pro` alias, the run was repeated the same day with the file in Task 2, `--offline` over the 17:32 UTC snapshots and the same inventory: 0 rows unmapped, 3 records unresolved (all three left open on purpose, see the Calibration Record), all eight sentinels held, exit 0. Every evidence command in Task 3 Step 2 was run on 2026-10-12 against those snapshots, the ollama registry and Hugging Face, and its answer is quoted beside it. Every code block below was then extracted from this file into a clone holding the commits of steps 1 to 3 and run in task order: each "Expected: FAIL" failed with the quoted message, each "Expected: PASS" passed, and the resulting tree equals the prototype file for file.

**Depends on:** docs/superpowers/plans/2026-10-12-model-tiers-step3-assignment.md, merged (and the plans of steps 1 and 2 before it).

## Global Constraints

- Python is `==3.13.*`, uv-managed, src layout. The only runtime dependency is `tomli-w`. No dependency, no source module and no CLI flag is added in this step.
- The tool never writes `registry.toml`, `config.yaml` or any wt state, and never reads `registry.toml`. wt is run only as `wt model list --json`.
- **Never, in any step of this plan:** read or print `~/.config/local-ai/registry.toml` (it holds a secret); run any wt command other than `wt model list --json`; write under `~/.config` or `~/.cache`, except the one step marked **owner only** (Task 3 Step 6), which is the first real `modeltiers run`.
- **Every ad-hoc run sets `MODELTIERS_OUT` and `MODELTIERS_CACHE`** to scratch paths, inline on its own command line. A bare `modeltiers run` writes `~/.config/local-ai/tiers.toml` and prunes `~/.cache/local-ai/modeltiers/`; the only bare one in this plan is Task 3 Step 6.
- The join key for an inventory row is the pair (`provider_id`, `model_name`), never the registry `id`. An alias's `inventory` entry is that pair exactly as `wt model list --json` prints it.
- No test touches the network, the real config home or the real cache. Step 1's autouse `_hermetic` fixture stays as it is. The live run in Task 3 is done by hand and is not a test.
- **An alias needs evidence that identifies the model.** That is an id which the model's publisher, its distributor or the benchmark source itself gives, and which leads to exactly one key: an ollama registry `remote_model`; a manifest digest shared with a fully spelled tag of the same library; the library's own readme; a Hugging Face `base_model` that the repo is a quantization of (relation `quantized`, or the publisher's explicit statement of a conversion), or the origin a publisher's manifest names in the same way; the versioned model ids a benchmark ran a display name under, when all of them belong to one key. Every `[[alias]]` in the committed file has a `note` naming that evidence and the date it was read.
- **These are not evidence, alone or together:** a name that resembles the key, a release date that equals the key's, and an architecture, a layer count, a vocabulary or a parameter count read from a registry config or a `config.json`. A fine-tune, a sibling and a successor share all of them. A `base_model` whose relation is `finetune`, `merge` or `adapter` is not evidence either: such a repo is another model and does not inherit the base model's key.
- **No evidence, no entry.** A row or a record that lacks identifying evidence is not aliased, and it is not ignored to empty a list: the row stays under `[[unmapped]]`, the record under `[[unresolved]]`, and the Calibration Record says what was found and which single entry would close it. The owner's own word about a model is evidence; such a note reads `owner-confirmed <date>`.
- **An `[[ignore]]` is a finding,** not a fallback: models.dev has no key for the model and no source scores it, or the model takes no chat work. Its `reason` says what was checked and ends `(checked <date>)`.
- **Calibration is done when,** on the owner's machine, `run` reports `unmapped: 0` and `sentinels: 8 of 8 hold`, every record under `unresolved:` is one the Calibration Record lists as open, and every row on the `review:` list has been checked (spec, Testing, "Live", which leaves a row with no identifying evidence listed and recorded as open; Delivery item 4). On 2026-10-12 three records were open. A listed row or record does not change the exit code.
- **The review list is not emptied.** The spec asks only that each name-matched row be checked. A checked row stays resolved by normalization; only a wrong match gets an alias. None of the twelve was wrong.
- **`contracts.toml` does not change in this step.** The live run confirmed the starting floors, the context minimums and the eight sentinels. Its `version` stays `2026-10-12`.
- **Starting floors, confirmed** (quick / standard / deep): `intelligence_index` 10.6 / 33.0 / 49.0; `arena_agent` -0.090 / -0.029 / 0.056, marathon 0.097 on `score_ci_lower`; `eci` 137.1 / 150.7 / 160.6. **Context minimums, confirmed:** `quick` 32,000; `standard` 128,000; `deep` 200,000; `marathon` 400,000.
- Tests use the data files under `modeltiers/tests/fixtures/`. `tests/test_data_files.py` is the one file that loads the committed data, and it asserts nothing about which entries or values they hold.
- `docs/contracts/tiers.sample.toml` does not change: it is made from `tests/fixtures/aliases.toml`, which this step leaves alone.
- **Guides never embed live model state.** In `docs/guides/12-model-tiers.md` and the new `docs/guides/00-config-map.md` sections: every command names its model as `<provider>/<model>` or `<canonical key>`; every sample block is word for word what the package prints for its test fixtures and is labelled an illustrative example from the test fixtures; no count, inventory row or tier of the live run appears. `tests/test_guide_samples.py` and the three greps of Task 4 Step 7 hold this. The live run's numbers are in this plan and in `aliases.toml`'s notes, nowhere else.
- Exit codes are unchanged: 0 written; 1 not written (configuration, inventory or source error, or an inventory from a redirected registry on a run without `--dry-run`); 2 written with a source that fell back; 3 not written, blocked by sentinels. Unmapped rows, unresolved records and warnings do not change the exit code. `--dry-run` returns the code the real run would return, with one exception: under a redirected registry the real run is refused with 1, and the dry run reports and warns and returns the code its report gives (step 3). The help text, the guide and both `CLAUDE.md` files say it in these words.
- Quality bar: `make check` (ruff lint, `ruff format --check`, mypy) and `make test` (pytest, coverage floor 90), plus `make lint` at the repo root.
- Commit messages follow the repo's conventional style, lower case after the type, no trailing period, with the attribution trailer your session is told to add, if any. Pushing a branch and opening a PR need the owner's OK.
- **Chosen where the spec is silent** (each is pinned by a test or stated in the Calibration Record):
  - **A model no source scores and models.dev has no key for is ignored.** A declared key must list a source id, and there is none. Three inventory rows are ignored for this reason; each reason ends `(checked 2026-10-12)` so the entry is found again when models.dev catches up.
  - **A model a source scores and models.dev does not know gets a declared key** (`google/gemini-4-argon`, for Arena's `Gemini 4 Argon`; the spelling is this plan's). It becomes a candidate with both gates skipped. This is not an identity claim about an existing key, so the evidence rule asks only that no key or row of models.dev could be the model.
  - **One alias beyond what the two lists demanded.** Epoch's `Nemotron 3 Ultra` (146.17) is under the standard floor, so it was never listed, but it is an inventory model's missing vote. It is aliased on the same kind of evidence as the others.
  - **The spec's example entries keep their place and change their words.** The `deepseek/deepseek-v4-pro-0813` note gains the registry's `remote_model` and Epoch's model ids. The `Mistral Large` ignore stays, with a reason that says what Epoch's data shows: its runs are of `mistral-large-2402`, a fixed model models.dev has no key for, not a floating name.
  - **The committed-file test gains one assertion:** no alias has an empty `note`. It asserts no value, so calibration still changes no test.
  - **`README.md` is updated too** (component table, guide index, repo layout). The spec's Docs list does not name it; it is the guide index, and a guide not listed there is not found.
  - **The guide's samples are tested.** The spec asks for samples "labelled illustrative". This plan also makes them fixture output word for word and fails the suite when they stop being so.

## Review Focus

1. **An inventory row that nothing will ever resolve** (a fine-tune no source scores: `ollama/medgemma:27b`). Expected: under `[[ignore]]` it is written to `[[models]]` as `unrated` with `reason = "not_applicable"`, no `canonical` and the note `ignored: <reason>`; `[[unmapped]]` is absent from the file and the report says `unmapped: 0`. Pinned in Task 1: `test_an_ignored_row_is_written_unrated_and_not_applicable`, `test_a_calibrated_run_leaves_nothing_unmapped_or_unresolved`.
2. **A benchmark record for a model models.dev has no key for** (Arena's `Gemini 4 Argon`; in the fixtures, `Qwen3.8 Max`). Expected: an alias that declares a new key and lists the source id makes it a candidate, resolved by `alias`, with the note that both gates were skipped and with no `tool_call`, `context` or `open_weights` key; `[[unresolved]]` is absent from the file. Pinned in Task 1: `test_a_declared_key_becomes_a_candidate_with_both_gates_skipped`.
3. **A calibrated `aliases.toml` met by an inventory that lacks one of its pairs** (the owner removed the model, or the file is used on another machine). Expected: silence. No warning, no error, exit 0; only alias source ids are checked for staleness. Pinned in Task 1: `test_an_alias_for_a_model_the_inventory_no_longer_has_is_silent`.
4. **An existing `tiers.toml` written before calibration,** with `[[unmapped]]` and `[[unresolved]]` entries. Expected: the first calibrated run's change report names each row that left the unmapped list, in the words that say how it left, and counts the candidates added, then replaces the file. A row an `[[ignore]]` now covers is `now ignored: <pair> (<reason>)`, pinned by `test_the_first_calibrated_run_reports_what_changed_and_replaces_the_file`. A row an `[[alias]]` now gives a key is `newly resolved: <pair> <tier>` with its real tier, and the entry is written with `resolved_by = "alias"`, pinned by `test_an_aliased_row_leaves_unmapped_and_is_reported_with_its_tier`.
5. **A sentinel that has genuinely moved since 2026-10-12** (a source re-scored a pinned model). Expected: exit 3 and no file, on every run, until the pinned votes in `contracts.toml` are edited; then exit 0, and the file records the new `contracts_version`. No flag gets round it. Pinned in Task 1: `test_editing_the_pinned_votes_is_what_clears_a_moved_sentinel`.
6. **A row or a record nobody can identify** (Epoch's `Gemini 3.1 Pro`, `Muse Spark` and `Grok 4.20` on 2026-10-12). Expected: no entry in `aliases.toml`; the record stays under `unresolved:` on every run, the exit code is still 0 and the file is written with the record in `[[unresolved]]`. Step 3 pins that behaviour (`test_run_writes_the_contract_sample_and_reports`, where the fixtures' `Qwen3.8 Max` is in that state); here the thing to review is the committed file against the Calibration Record: no alias whose note cites only a name, a date or an architecture, and no ignore whose reason is "could not tell".
7. **A guide whose sample drifts from the tool** (someone edits a line `run` prints, or pastes output from their own machine into the guide). Expected: `make -C modeltiers test` fails, naming the block. Pinned in Task 4: `test_every_output_sample_in_the_guide_is_fixture_output`, `test_the_config_map_example_is_the_committed_sample`, `test_no_command_in_the_guide_names_a_fixture_model`.

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `modeltiers/tests/test_calibration.py` | Create | The calibration moves, end to end through the CLI over the golden fixtures |
| `modeltiers/tests/fixtures/aliases_calibrated.toml` | Create | The fixture `aliases.toml` after one calibration pass; read only by `test_calibration.py` |
| `modeltiers/tests/fixtures/README.md` | Modify | Credits the new fixture; says the sample is not made with it |
| `modeltiers/tests/test_data_files.py` | Modify | The committed-file test also requires a note on every alias |
| `modeltiers/src/modeltiers/data/aliases.toml` | Replace | The calibrated identity answers: 11 aliases, 5 ignores, each with its evidence |
| `modeltiers/tests/test_guide_samples.py` | Create | The guides' sample blocks are fixture output word for word; no command names a fixture model |
| `docs/guides/12-model-tiers.md` | Create | User guide: what a tier means, run, read, add an alias or an ignore, re-calibrate |
| `docs/guides/00-config-map.md` | Modify | `tiers.toml` and the snapshot directory: two table rows, two file sections, two cross-references |
| `README.md` | Modify | Component row, guide index row, repo-layout lines |
| `CLAUDE.md` | Modify | Guides list, the `modeltiers run` Commands line, the `modeltiers/` Architecture bullet, a new "Tiers file" bullet |
| `modeltiers/CLAUDE.md` | Replace | Final form: calibrated status, "The two data files" section, the calibration and guide-sample tests |

Not changed: every file under `modeltiers/src/modeltiers/` except `data/aliases.toml`; `modeltiers/src/modeltiers/data/contracts.toml`; `docs/contracts/tiers.sample.toml`; the root `Makefile`; `.github/workflows/modeltiers-ci.yml` (step 1 already lists `docs/guides/12-model-tiers.md` and `docs/guides/00-config-map.md` among its paths, so a pull request that edits only a guide still runs `tests/test_guide_samples.py`); the spec, `docs/superpowers/specs/2026-10-12-model-tiers-design.md`, which the owner corrects on the docs branch.

---

## Calibration Record (2026-10-12)

What the live run found and what was decided. Task 2 turns the decisions into the file; Task 3 is how the owner checks them again, and Task 3 Step 2 holds one command, with its answer of 2026-10-12, for every piece of evidence named here. The helper names used below (`idkeys`, `keys`, `epoch_ids`, `hf_base`, `ollama_config`, `ollama_digest`) are defined there.

### The run

`modeltiers run`, live sources (downloaded 17:32 UTC), the owner's `wt model list --json` (49 rows, 3 native dropped, provider `mtplx` reporting `partial`), scratch output and cache. The left column is the live run with step 3's file; the right column is the same downloads and inventory, read again with `--offline`, with Task 2's file.

| | Step 3's `aliases.toml` | Task 2's `aliases.toml` |
|---|---|---|
| Sources | models.dev 8,499 rows, OpenRouter 284 records, Arena 52, Epoch 274; all `ok` | the same snapshots, `stale` because `--offline` |
| Identity (46 rows) | 35 resolved, 1 ignored, 10 unmapped | 42 resolved, 4 ignored, 0 unmapped |
| `unresolved:` at or above a standard floor | 6 | 3: Epoch `Gemini 3.1 Pro`, `Muse Spark` and `Grok 4.20`, left open |
| Review list | 12 | 12, each checked below |
| Sentinels | 8 of 8 hold | 8 of 8 hold |
| Inventory tiers | 14 standard, 17 quick, 5 unrated | 15 standard, 21 quick, 10 unrated (4 `below_floor`, 4 `not_applicable`, 2 `no_votes`) |
| Candidates | 167: 3 marathon, 7 deep, 31 standard, 85 quick, 41 unrated (39 `below_floor`, 2 `gate_tool_call`) | 167: 3 marathon, 8 deep, 32 standard, 84 quick, 40 unrated (38 `below_floor`, 2 `gate_tool_call`) |
| Keys with at least one vote | 190: 167 candidates and 23 inventory keys | 192: 167 candidates and 25 inventory keys |
| Exit | 0 | 0 |

The candidates at `marathon`, in full: `anthropic/claude-fable-5-1`, `anthropic/claude-opus-5-5`, `openai/gpt-6-astra`. At `deep`, in full: `anthropic/claude-fable-5`, `anthropic/claude-opus-5`, `anthropic/claude-sonnet-5-5`, `google/gemini-4-argon`, `openai/gpt-5.5-pro`, `openai/gpt-5.6-sol`, `openai/gpt-6-sol`, `openai/gpt-6.1-sol`. No inventory row is at either tier.

The candidate count is 167 in both columns and they are not the same 167. Task 2's file adds two candidates, `google/gemini-3-pro-preview` (`standard`) and `google/gemini-4-argon` (`deep`), and takes two away, `mistral/mistral-large-2512` and `nvidia/nemotron-3.5-lightning`, which became inventory keys when their rows were aliased.

### The ten unmapped rows

Each alias has evidence that identifies the model, then one step from that model to its key. Nothing here rests on the row's own name, a date or an architecture.

| Inventory row | Decision | Evidence, read 2026-10-12 |
|---|---|---|
| `ollama/gemma4:cloud` | alias → `google/gemma-4-31b-it` | The registry config for `gemma4:cloud` has `remote_model` `gemma4:31b`, and its manifest is byte for byte that of `gemma4:31b-cloud` (sha256 `ef09f235533c`). `keys 'gemma.4.31b'` prints one key, this one |
| `ollama/mistral-large-3:675b-cloud` | alias → `mistral/mistral-large-2512` | `remote_model` is `mistral-large-3:675b`. models.dev's own row for that tag has no key; its Bedrock row `mistral.mistral-large-3-675b-instruct`, the same model and size, carries this key |
| `ollama/nemotron-3.5-lightning:30b-mlx` | alias → `nvidia/nemotron-3.5-lightning` | `:30b-mlx` and `:30b-a3b-mlx` have one digest, `8b1474be6e54`. `keys` prints one Nemotron 3.5 Lightning key |
| `ollama/ornith-1.5:35b` | alias → `deepreinforce/ornith-1.5-35b-a3b` | The ollama library `ornith-1.5` has three tags, `397b`, `35b` and `9b`, and its readme names three models, Ornith-1.5-397B, Ornith-1.5-35B-A3B and Ornith-1.5-9B. models.dev's only Ornith 1.5 key is this one |
| `ollama/qwen3.6:35b-mlx` | alias → `alibaba/qwen3.6-35b-a3b` | `:35b-mlx` and `:35b-a3b-nvfp4` have one digest, `e92a3e94bbca`. `keys 'qwen3\.6.35b'` prints one key |
| `mtplx/Youssofal/Qwen3.6-35B-A3B-MTPLX-Optimized-Balance` | alias → `alibaba/qwen3.6-35b-a3b` | The card has no `base_model`. The publisher's `mtplx_runtime.json` in the repo names `base_trunk` `mlx-community/Qwen3.6-35B-A3B-6bit`; that repo's card gives `base_model` `Qwen/Qwen3.6-35B-A3B`; 26 models.dev rows with that id carry this key and none carries another |
| `mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality` | alias → `alibaba/qwen3.8-27b` | The card's `base_model` is `Qwen/Qwen3.8-27B`; 40 models.dev rows with that id carry this key and none carries another |
| `ollama/medgemma:27b` | ignore | A finding, not a doubt: `keys medgemma` prints nothing and no OpenRouter record has the name; Arena and Epoch do not list it. There is nothing to resolve to |
| `ollama/ornith-1.5:9b` | ignore | models.dev's Ornith keys are `ornith-1.0-35b` and `ornith-1.5-35b-a3b`; no source has a record of the 9B |
| `openrouter/nex-agi/nex-n2.5-pro:free` | ignore | The two models.dev rows with id `nex-n2.5-pro` have no key; the only Nex key is `nex-agi/nex-n2-pro`, another model; OpenRouter gives the model no intelligence index |

The earlier draft of this record aliased the MTPLX Balance row on a `config.json` that matched the base model's in architecture. That identifies nothing (a fine-tune shares it) and is withdrawn; the alias now rests on the `base_trunk` chain above. `ornith-1.5:35b` resolves with no vote and the Nemotron 3.5 row with one (an intelligence index of 12.9): that is what the sources hold, not a gap in the aliases.

### The unresolved records

Epoch's `eci_scores.csv` has a `Model` column (the first) beside `Display name` (the second). For all eight rows below the two are the same string, so column 1 identifies nothing more than the name. What does identify the model is in the same download: `epoch_capabilities_index/processed_data_for_eci.csv` lists every benchmark run behind an ECI score with its `model_version`, the versioned model id that was run. `epoch_ids '<display name>'` prints those ids with the number of runs under each, and `idkeys <id>` prints the keys that models.dev rows with that model id carry.

| Record | `Model` (column 1) | Model ids Epoch ran, with runs | Decision | Why |
|---|---|---|---|---|
| Epoch `Gemini 3 Pro` (152.92) | `Gemini 3 Pro` | `gemini-3-pro-preview` 21, `…_low` 1 | alias → `google/gemini-3-pro-preview` | Every run is under the key's own id. The other keys `keys 'gemini.3.pro'` prints are `gemini-3-pro-image` and `-image-preview`, ids Epoch did not run |
| Epoch `Gemini 3 Flash` (151.8) | `Gemini 3 Flash` | `gemini-3-flash-preview` 13, `…_high` 6, `…_low` 1, `…_minimal` 1 | alias → `google/gemini-3-flash-preview` | Every run is under the key's own id; 18 rows with that id carry this key, none another |
| Epoch `Nemotron 3 Ultra` (146.17; not listed, under the standard floor) | `Nemotron 3 Ultra` | `nemotron-3-ultra` 9 | alias → `nvidia/nemotron-3-ultra-550b-a55b` | Both models.dev rows with model id `nemotron-3-ultra` carry this key, and `keys 'nemotron.3.ultra'` prints no other. Gives the inventory row `ollama/nemotron-3-ultra:cloud` its second vote |
| Epoch `DeepSeek V4 Pro 0813` (155.31; the spec's alias) | `DeepSeek V4 Pro 0813` | `deepseek-v4-pro-0813_max` 9, `…_unknown` 4, `…_high` 1, `…_low` 1 | alias kept | Every run is under the key's own id; 41 rows with that id carry `deepseek/deepseek-v4-pro-0813` |
| Arena `Gemini 4 Argon` (0.0926) | — | Arena publishes no model id; `organization` is `google` | alias, declared key `google/gemini-4-argon` | `keys 'gemini.4\|argon'` prints nothing: no key or row of models.dev could be this model, so the key is declared, not matched |
| Epoch `Gemini 3.1 Pro` (154.77) | `Gemini 3.1 Pro` | `gemini-3.1-pro-preview` 19, `…_high` 9, `…_medium` 1, `gemini-3.1-pro-preview-customtools` 1 | **left unresolved** | Twenty-nine runs are under the id of the key `google/gemini-3.1-pro-preview` (24 models.dev rows with that id carry it, none another). The thirtieth is under `gemini-3.1-pro-preview-customtools`, the id of a second key (8 rows carry `google/gemini-3.1-pro-preview-customtools`). Epoch ran the name under ids of two keys, so the record does not identify one key. The rule counts keys, not runs: one run under another key's id is enough, exactly as for `Grok 4.20` |
| Epoch `Muse Spark` (152.04) | `Muse Spark` | `muse-spark` 6 | **left unresolved** | No models.dev row has the model id `muse-spark`. The keys are `meta/muse-spark-1.1`, `-1.2` and `-1.3`, and Epoch lists `Muse Spark 1.1` as its own row (ids `muse-spark-1.1…`). models.dev dates its 1.1 key 2026-04-08, Epoch's date for the unversioned row, which may mean models.dev folds the first release into 1.1 or may be a coincidence. A date decides nothing |
| Epoch `Grok 4.20` (151.98) | `Grok 4.20` | `grok-4-20` 8, `grok-4.20-0309-reasoning` 7 | **left unresolved** | Seven runs are under the id of the key `xai/grok-4.20-0309-reasoning`. Eight are under `grok-4-20`, an id whose one models.dev row has no key and which could be the reasoning model, the non-reasoning one or the February beta. The row is a blend of two ids and only one is identified |
| Epoch `Mistral Large` (122.01; the spec's ignore) | `Mistral Large` | `mistral-large-2402` 5 | ignore kept, reason corrected | The spec's reason was "floating name; no fixed model". Epoch's runs are of one fixed model, `mistral-large-2402`. The one models.dev row with that id has no key, and the keys named Mistral Large are 2411, 2512, 4 and `-latest`. The ignore stays so the bare name can never match one of those; its reason now says this |

The earlier draft aliased the three Gemini rows and the Nemotron row because Epoch's date equalled the key's release date, and ignored `Muse Spark` and `Grok 4.20` because the dates left two readings. Both are withdrawn: a date identifies nothing, and "cannot tell" is not a reason to ignore. A later draft aliased `Gemini 3.1 Pro` again, on 29 runs of 30 under one key's id. That is withdrawn too: the rule has no tolerance for a stray run.

What the two Gemini aliases that remain do, and what the withdrawn one would have done:

- **`Gemini 3 Flash`** adds an Epoch vote, `standard`, to `google/gemini-3-flash-preview`, which had one vote (`intelligence_index`, `quick`). The key stays `quick`, because two votes take the lower, and its confidence goes from `low` to `medium`.
- **`Gemini 3 Pro`** gives `google/gemini-3-pro-preview` its only vote. The key had none and was in no file; it becomes a candidate at `standard`, confidence `low`.
- **`Gemini 3.1 Pro`, left open,** costs `google/gemini-3.1-pro-preview` an Epoch vote and no tier. The key has two votes, `quick` and `quick`, and is `quick` with confidence `high`. With Epoch's 154.77 it would have `quick`, `quick`, `standard` and still be `quick`, the lower median, with confidence `medium`.

### Left open, for the owner

Nothing in the inventory is unmapped. Three benchmark records stay in `[[unresolved]]`; each costs one key an Epoch vote and nothing else. The owner's word is acceptable evidence. If the owner confirms the identity stated, this one entry closes the row (write the date in the note):

| Open row | What the owner would be confirming | The entry that closes it |
|---|---|---|
| Epoch `Gemini 3.1 Pro` | That Epoch's one run under `gemini-3.1-pro-preview-customtools` is a run of the same model as the other 29 (one model served behind two ids) | `[[alias]]` with `canonical = "google/gemini-3.1-pro-preview"`, `note = "Epoch ran Gemini 3.1 Pro as gemini-3.1-pro-preview (29 runs) and gemini-3.1-pro-preview-customtools (1); owner-confirmed <date> that both are this model"`, `epoch = ["Gemini 3.1 Pro"]` |
| Epoch `Muse Spark` | That models.dev's `meta/muse-spark-1.1` is not the April model, so the model has no key | `[[alias]]` with `canonical = "meta/muse-spark"`, `note = "declared here: Epoch's model_version muse-spark, the 2026-04-08 release; models.dev has no key for it; owner-confirmed <date>"`, `epoch = ["Muse Spark"]` |
| Epoch `Grok 4.20` | That Epoch's `grok-4-20` runs are of the reasoning model | `[[alias]]` with `canonical = "xai/grok-4.20-0309-reasoning"`, `note = "Epoch ran Grok 4.20 as grok-4.20-0309-reasoning and grok-4-20; owner-confirmed <date> that both are this model"`, `epoch = ["Grok 4.20"]` |

If the owner instead confirms that `meta/muse-spark-1.1` covers the April model, the `Muse Spark` entry's `canonical` is `meta/muse-spark-1.1`; that key then has two Epoch records and keeps the better score. The `Gemini 3.1 Pro` row also closes without the owner, by the same entry with a note that names the ids and the date, on the day `epoch_ids 'Gemini 3.1 Pro'` prints ids of one key only (Task 3 Step 3).

### The review list: twelve rows matched by name, each checked

| Inventory row | Key | Check |
|---|---|---|
| `ollama/gpt-oss:120b-cloud` | `openai/gpt-oss-120b` | `remote_model` `gpt-oss:120b`; the models.dev row with that id that has a key carries this one |
| `ollama/gpt-oss:20b-cloud` | `openai/gpt-oss-20b` | `remote_model` `gpt-oss:20b`; one gpt-oss 20b key |
| `ollama/gpt-oss:20b` | `openai/gpt-oss-20b` | The fully spelled tag itself; one gpt-oss 20b key |
| `ollama/kimi-k2.6:cloud` | `moonshotai/kimi-k2.6` | `remote_model` `kimi-k2.6`; 52 rows with that id carry this key, none another |
| `ollama/minimax-m2.7:cloud` | `minimax/MiniMax-M2.7` | `remote_model` `minimax-m2.7`; 31 rows with that id carry this key, none the `-highspeed` key |
| `ollama/minimax-m3:cloud` | `minimax/MiniMax-M3` | `remote_model` `minimax-m3`; 46 rows with that id carry this key, none another |
| `ollama/qwen3.6:27b-mlx` | `alibaba/qwen3.6-27b` | Digest `d49fd9e0da45`, shared with `:27b-nvfp4`; one key |
| `ollama/qwen3.8:27b-mlx` | `alibaba/qwen3.8-27b` | Digest `5642e97495e1`, shared with `:27b-nvfp4`; one key |
| `omlx/mlx-community/Qwen3.8-27B-4bit` | `alibaba/qwen3.8-27b` | Hugging Face `base_model` `Qwen/Qwen3.8-27B` |
| `ollama/gemma4:31b-mlx` | `google/gemma-4-31b-it` | Digest `a22a363052da`, shared with `:31b-nvfp4`; one Gemma 4 31B key |
| `ollama/muse-glimmer:30b-mlx` | `meta/muse-glimmer-30b` | Digest `015fa21845be`, shared with `:30b-nvfp4-dflash`; one Muse Glimmer key |
| `omlx/Qwen3.6-35B-A3B-6bit` | `alibaba/qwen3.6-35b-a3b` | A local directory with no upstream id to query. It was not aliased, because a name is not evidence; it stays a name match, on the review list, which is the tool saying exactly that. The owner built or downloaded it and can confirm |

None was found wrong, so none became an alias. The last row is the one check that could only read the name; if the owner confirms it, nothing changes in the file.

### Floors and sentinels: confirmed, no correction

| Sentinel | Scores (intelligence index / Arena score, lower bound / Epoch) | Votes | Tier before the gates |
|---|---|---|---|
| `anthropic/claude-fable-5-1` | 53.4 / 0.1266, 0.1061 / 164.7 | deep, deep, deep | `marathon` |
| `anthropic/claude-opus-5-5` | 57.6 / 0.1433, 0.1215 / 167.33 | deep, deep, deep | `marathon` |
| `anthropic/claude-fable-5` | 49.6 / 0.0894, 0.0767 / 162.06 | deep, deep, deep | `deep` |
| `anthropic/claude-opus-5` | 50.8 / 0.0812, 0.0664 / 162.78 | deep, deep, deep | `deep` |
| `anthropic/claude-sonnet-5` | 38.2 / 0.0491, 0.0323 / 156.21 | standard ×3 | `standard` |
| `zhipuai/glm-5.2` | 33.7 / 0.0319, 0.0246 / 151.78 | standard ×3 | `standard` |
| `minimax/MiniMax-M3` | 29.2 / -0.0695, -0.0786 / 146.95 | quick ×3 | `quick` |
| `google/gemma-4-31b-it` | 14.7 / no Arena row / 142.74 | quick, quick | `quick` |

- The thinnest margins: Claude Fable 5's intelligence index is 0.6 above the deep floor, GLM-5.2's is 0.7 above the standard floor, and Fable 5.1's Arena lower bound is 0.009 above the marathon floor. These are the sentinels most likely to fail first; that is what they are for.
- No model's votes span two tiers. The context gate lowered nobody. The tool-calling gate moved two candidates from `quick` to `unrated` (`alibaba/qwq-32b`, `perplexity/sonar-reasoning-pro`).
- The highest intelligence index among inventory models is 44.8, under the deep floor of 49.0: no inventory model is `deep` or `marathon`, as the spec says.

### The spec's "run over the 2026-10-12 downloads" list, checked against the final run

The spec's Testing section asks for that list to be confirmed or corrected against the live run. The owner corrects the spec directly on the docs branch, so this plan does not edit the spec, and the spec already carries the corrected list. What the final run gave (Task 2's file over the 17:32 UTC downloads), line by line, for whoever compares the two:

- **`marathon`: confirmed.** Claude Opus 5.5, Claude Fable 5.1, GPT-6 Astra.
- **`deep`: the first list's four are right and it was short by four.** Claude Sonnet 5.5, GPT-6.1 Sol, Claude Opus 5, Claude Fable 5 (all three votes `deep`), plus GPT-6 Sol and GPT-5.6 Sol (votes `standard`, `deep`, `deep`: the lower median of three is the middle vote), GPT-5.5 Pro (one Epoch vote, confidence `low`) and Gemini 4 Argon (one Arena vote through the declared key, confidence `low`). The rules give these; the throwaway run that made the first list left them out.
- **`standard` and `quick`: confirmed** for every model the list names. The inventory also has DeepSeek V4 Flash 0731 at `standard`.
- **"About 190 keys had at least one vote":** 192 with Task 2's file (167 candidates and 25 inventory keys); 190 with step 3's.
- **"35 of the 46 inventory rows resolved" with the one alias: confirmed.**
- **Withdrawing the `Gemini 3.1 Pro` alias moved none of these lines.** Its key keeps two votes and its tier, so the eleven keys at `marathon` and `deep`, the tier of every key and the count of 192 are what they were with the alias in place.
---

### Task 1: The calibration moves, pinned over the fixtures

**Files:**
- Create: `modeltiers/tests/test_calibration.py`
- Create: `modeltiers/tests/fixtures/aliases_calibrated.toml`
- Modify: `modeltiers/tests/fixtures/README.md` (a new table row after the `aliases.toml` row, and the last paragraph)

**Interfaces:**
- Consumes (steps 1 to 3, unchanged): `cli.main(argv: list[str] | None = None, *, deps: Deps | None = None) -> int`; `cli.Deps(fetcher, sleep, now, run_wt, specs, aliases_path: Path | None, contracts_path: Path | None)`; `fetch.OPENROUTER_URL`; `assign.NOTE_NO_ROWS`; `loader.SourceSpecs`; from `tests/conftest.py`: `FIXTURES`, `T0`, and the fixtures `specs`, `good_answers`, `read_fixture`; from `tests/fakes.py`: `FakeFetcher`, `Sleeps`.
- Produces: `tests/fixtures/aliases_calibrated.toml`, read by this test file only. No source name.

These tests need no new source code. Six fail first because the fixture they read does not exist; with the fixture in place they pass against the code of steps 2 and 3. The seventh, `test_an_aliased_row_leaves_unmapped_and_is_reported_with_its_tier`, builds its own aliases file and passes from the start: it pins behaviour step 3 already has and no earlier test names. A test here that still fails after Step 3 is a defect in an earlier step: stop and report it, do not change the test.

- [ ] **Step 1: Write the tests**

Write `modeltiers/tests/test_calibration.py`:

```python
"""What calibration does to a run: the moves `docs/guides/12-model-tiers.md`
describes, over the golden fixtures. `aliases_calibrated.toml` is the fixture
`aliases.toml` after one pass: an ignore for the unmapped row, a declared key
for the unresolved record, and an alias for a model the inventory no longer
has. The alias that gives the unmapped row a key instead is built in the test
that needs it."""

import json
import tomllib
from collections.abc import Callable
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

import pytest
from conftest import FIXTURES, T0
from fakes import FakeFetcher, Sleeps

from modeltiers import cli, fetch
from modeltiers.assign import NOTE_NO_ROWS
from modeltiers.loader import SourceSpecs

INVENTORY = FIXTURES / "inventory.json"
UNCALIBRATED = FIXTURES / "aliases.toml"
CALIBRATED = FIXTURES / "aliases_calibrated.toml"
CONTRACTS = FIXTURES / "contracts.toml"
RUN = ["run", "--inventory", str(INVENTORY)]
Answers = dict[str, list[bytes | Exception]]

MTPLX_WARNING = (
    "warning: provider mtplx probe status is partial; "
    "models it would have discovered may be absent from this run\n"
)


def _deps(
    specs: SourceSpecs,
    answers: Answers,
    *,
    now: datetime = T0,
    aliases_path: Path = CALIBRATED,
    contracts_path: Path = CONTRACTS,
) -> cli.Deps:
    return cli.Deps(
        fetcher=FakeFetcher(answers),
        sleep=Sleeps(),
        now=lambda: now,
        specs=specs,
        aliases_path=aliases_path,
        contracts_path=contracts_path,
    )


@pytest.fixture
def out(tmp_path: Path) -> Path:
    """Where conftest's MODELTIERS_OUT points."""
    return tmp_path / "out" / "tiers.toml"


def _written(out: Path) -> dict[str, Any]:
    return tomllib.loads(out.read_text())


def test_a_calibrated_run_leaves_nothing_unmapped_or_unresolved(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers)) == 0
    report = capsys.readouterr().out
    assert "identity: 8 rows: 6 resolved, 2 ignored, 0 unmapped\n" in report
    assert (
        "unmapped: 0 (each needs an [[alias]] or an [[ignore]] in aliases.toml, "
        "or is left open until evidence identifies it)\n"
    ) in report
    assert "unresolved: 0 at or above a standard floor" in report
    doc = _written(out)
    # An array with no entries is absent from the file.
    assert "unmapped" not in doc
    assert "unresolved" not in doc
    assert len(doc["models"]) == 8


def test_an_ignored_row_is_written_unrated_and_not_applicable(
    specs: SourceSpecs, good_answers: Answers, out: Path
) -> None:
    cli.main(RUN, deps=_deps(specs, good_answers))
    (entry,) = [m for m in _written(out)["models"] if m["model_name"] == "medgemma:27b"]
    assert entry["tier"] == "unrated"
    assert entry["code"] == "-"
    assert entry["reason"] == "not_applicable"
    assert entry["confidence"] == "none"
    assert entry["notes"] == ["ignored: fixture: a fine-tune no source scores"]
    assert "canonical" not in entry
    assert "resolved_by" not in entry


def test_a_declared_key_becomes_a_candidate_with_both_gates_skipped(
    specs: SourceSpecs, good_answers: Answers, out: Path
) -> None:
    cli.main(RUN, deps=_deps(specs, good_answers))
    (entry,) = [c for c in _written(out)["candidates"] if c["canonical"] == "alibaba/qwen3.8-max"]
    assert entry["resolved_by"] == "alias"
    assert entry["tier"] == "standard"
    assert entry["confidence"] == "low"
    assert entry["votes"] == {"arena_agent": "standard"}
    assert entry["notes"] == [NOTE_NO_ROWS]
    # models.dev has no row for a declared key, so it knows neither of these.
    assert "tool_call" not in entry
    assert "context" not in entry
    assert "open_weights" not in entry


def test_an_alias_for_a_model_the_inventory_no_longer_has_is_silent(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main([*RUN, "--dry-run"], deps=_deps(specs, good_answers)) == 0
    captured = capsys.readouterr()
    # Only the fixture's mtplx probe warning: nothing about the unused alias.
    assert captured.err == MTPLX_WARNING
    assert "MTPLX" not in captured.out


def test_the_first_calibrated_run_reports_what_changed_and_replaces_the_file(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    # A file from before calibration: medgemma unmapped, Qwen3.8 Max unresolved.
    assert cli.main(RUN, deps=_deps(specs, good_answers, aliases_path=UNCALIBRATED)) == 0
    before = _written(out)
    assert [u["model_name"] for u in before["unmapped"]] == ["medgemma:27b"]
    assert [u["id"] for u in before["unresolved"]] == ["Qwen3.8 Max"]
    capsys.readouterr()
    later = T0 + timedelta(days=1)
    assert cli.main(RUN, deps=_deps(specs, good_answers, now=later)) == 0
    report = capsys.readouterr().out
    assert (
        "changes since 2026-10-12T15:58:00Z:\n"
        "  now ignored: ollama/medgemma:27b (fixture: a fine-tune no source scores)\n"
        "  confidence only: 0\n"
        "  candidates: 1 added, 0 removed, 0 changed tier\n"
        "  sources: modelsdev ok, openrouter ok, arena ok, epoch ok\n"
    ) in report
    assert _written(out)["generated_at"] == "2026-10-13T15:58:00Z"


def _aliased(tmp_path: Path) -> Path:
    """The fixture aliases.toml after the owner found evidence for the
    unmapped row. Any key with votes stands for the model the evidence named."""
    entry = (
        "\n[[alias]]\n"
        'canonical = "zhipuai/glm-5.3"\n'
        'note = "fixture: stands for evidence that names the model"\n'
        'inventory = [{ provider_id = "ollama", model_name = "medgemma:27b" }]\n'
    )
    path = tmp_path / "aliases.toml"
    path.write_text(UNCALIBRATED.read_text() + entry)
    return path


def test_an_aliased_row_leaves_unmapped_and_is_reported_with_its_tier(
    specs: SourceSpecs,
    good_answers: Answers,
    out: Path,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers, aliases_path=UNCALIBRATED)) == 0
    assert [u["model_name"] for u in _written(out)["unmapped"]] == ["medgemma:27b"]
    capsys.readouterr()
    later = T0 + timedelta(days=1)
    deps = _deps(specs, good_answers, now=later, aliases_path=_aliased(tmp_path))
    assert cli.main(RUN, deps=deps) == 0
    report = capsys.readouterr().out
    assert "identity: 8 rows: 7 resolved, 1 ignored, 0 unmapped\n" in report
    assert (
        "changes since 2026-10-12T15:58:00Z:\n"
        "  newly resolved: ollama/medgemma:27b standard\n"
        "  confidence only: 0\n"
        "  candidates: 0 added, 0 removed, 0 changed tier\n"
    ) in report
    doc = _written(out)
    assert "unmapped" not in doc
    (entry,) = [m for m in doc["models"] if m["model_name"] == "medgemma:27b"]
    assert entry["canonical"] == "zhipuai/glm-5.3"
    assert entry["resolved_by"] == "alias"
    assert entry["tier"] == "standard"
    assert entry["basis"] == "inherited"


def _recalibrated(tmp_path: Path) -> Path:
    """The fixture contracts.toml after the owner accepts a moved vote: the
    two sentinels that pinned three deep votes now pin a standard
    intelligence index, and the version is new."""
    old = 'votes = { intelligence_index = "deep", arena_agent = "deep", eci = "deep" }'
    new = 'votes = { intelligence_index = "standard", arena_agent = "deep", eci = "deep" }'
    text = CONTRACTS.read_text()
    assert text.count(old) == 2
    path = tmp_path / "contracts.toml"
    path.write_text(text.replace(old, new).replace('"fixture-1"', '"fixture-2"'))
    return path


def test_editing_the_pinned_votes_is_what_clears_a_moved_sentinel(
    specs: SourceSpecs,
    good_answers: Answers,
    read_fixture: Callable[[str], bytes],
    out: Path,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # Every intelligence index drops by a fifth, as in the sentinel tests.
    doc = json.loads(read_fixture("openrouter.json"))
    for model in doc["data"]:
        indices = (model.get("benchmarks") or {}).get("artificial_analysis")
        if indices:
            indices["intelligence_index"] *= 0.8
    good_answers[fetch.OPENROUTER_URL] = [json.dumps(doc).encode()]

    # The same data twice. With the old pins nothing is written.
    assert cli.main(RUN, deps=_deps(specs, good_answers)) == 3
    assert not out.exists()
    assert "sentinels: 2 of 4 FAILED\n" in capsys.readouterr().out
    # With the pins edited the run is clean and the file names the new version.
    deps = _deps(specs, good_answers, contracts_path=_recalibrated(tmp_path))
    assert cli.main(RUN, deps=deps) == 0
    assert "sentinels: 4 of 4 hold\n" in capsys.readouterr().out
    assert _written(out)["contracts_version"] == "fixture-2"
```

- [ ] **Step 2: Run them and see six fail**

Run: `uv run --directory modeltiers pytest tests/test_calibration.py -q`
Expected: FAIL with `6 failed, 1 passed`. In each of the six the first `cli.main` call that is given `aliases_calibrated.toml` returns 1, and the captured stderr reads `modeltiers: aliases: cannot read` followed by the path of `aliases_calibrated.toml` and `No such file or directory`. Four tests fail on the exit code (`assert 1 == 0`, or `assert 1 == 3` in the sentinel test; in `test_the_first_calibrated_run_reports_what_changed_and_replaces_the_file` it is the second call; the first is given the uncalibrated fixture file and passes). The two that do not assert the exit code, `test_an_ignored_row_is_written_unrated_and_not_applicable` and `test_a_declared_key_becomes_a_candidate_with_both_gates_skipped`, fail one line later with `FileNotFoundError` on `tiers.toml`, which was never written.

- [ ] **Step 3: Write the calibrated fixture**

It is `tests/fixtures/aliases.toml` with three entries added: a key declared for the Arena record the fixtures leave unresolved, an alias for a pair the fixture inventory does not have, and an ignore for the fixture's unmapped row.

Write `modeltiers/tests/fixtures/aliases_calibrated.toml`:

```toml
# tests/fixtures/aliases.toml after one calibration pass. Read only by
# tests/test_calibration.py.

[[alias]]
canonical = "deepseek/deepseek-v4-pro-0813"
note = "ollama :cloud and :0813-cloud shared digest 6ed7420bc3a4 on 2026-10-12"
inventory = [{ provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }]
arena = ["DeepSeek V4 Pro (0813)"]
epoch = ["DeepSeek V4 Pro 0813"]

# A key the fixture models.dev does not have, declared for a benchmark row.
[[alias]]
canonical = "alibaba/qwen3.8-max"
note = "fixture: Arena scores it and models.dev has no key for it"
arena = ["Qwen3.8 Max"]

# A pair that is not in the fixture inventory: the model was removed.
[[alias]]
canonical = "alibaba/qwen3.8-27b"
note = "fixture: Hugging Face base_model is Qwen/Qwen3.8-27B"
inventory = [{ provider_id = "mtplx", model_name = "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality" }]

[[ignore]]
inventory = { provider_id = "ollama", model_name = "nomic-embed-text:latest" }
reason = "embedding model"

[[ignore]]
inventory = { provider_id = "ollama", model_name = "medgemma:27b" }
reason = "fixture: a fine-tune no source scores"

[[ignore]]
source = "epoch"
id = "GPT-5"
reason = "fixture: stands for a name no key will ever match"
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_calibration.py -q`
Expected: PASS, `7 passed`.

- [ ] **Step 5: Credit the fixture**

In `modeltiers/tests/fixtures/README.md`, replace:

```markdown
| `aliases.toml` | Hand-written for the tests | This repository. The same shape as `src/modeltiers/data/aliases.toml`; its entries are chosen for the tests and are not calibration. |
```

with:

```markdown
| `aliases.toml` | Hand-written for the tests | This repository. The same shape as `src/modeltiers/data/aliases.toml`; its entries are chosen for the tests and are not calibration. |
| `aliases_calibrated.toml` | Hand-written for the tests | This repository. `aliases.toml` after one calibration pass: one more ignore and two more aliases. Read only by `tests/test_calibration.py`. |
```

In `modeltiers/tests/fixtures/README.md`, replace:

```markdown
`docs/contracts/tiers.sample.toml`, at the repo root, is what `modeltiers run` writes from the golden fixtures here with `aliases.toml` and `contracts.toml`. A change to any of them changes that file; `tests/test_output.py` and `tests/test_cli.py` compare against it apart from timestamps.
```

with:

```markdown
`docs/contracts/tiers.sample.toml`, at the repo root, is what `modeltiers run` writes from the golden fixtures here with `aliases.toml` and `contracts.toml` (not with `aliases_calibrated.toml`). A change to any of them changes that file; `tests/test_output.py` and `tests/test_cli.py` compare against it apart from timestamps.
```

- [ ] **Step 6: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS, with `All checks passed!` and `Success: no issues found in 25 source files`.

- [ ] **Step 7: Commit**

```bash
git add modeltiers/tests/test_calibration.py modeltiers/tests/fixtures/aliases_calibrated.toml modeltiers/tests/fixtures/README.md
git commit -m "test(modeltiers): pin what an alias, an ignore, a declared key and a re-pinned sentinel do to a run"
```

---

### Task 2: The calibrated `aliases.toml`

**Files:**
- Modify: `modeltiers/tests/test_data_files.py` (`test_the_committed_aliases_file_loads_and_validates`)
- Replace: `modeltiers/src/modeltiers/data/aliases.toml`

**Interfaces:**
- Consumes: `aliases.load_aliases(path: Path | None = None) -> Aliases` (`None` is the committed file); `Aliases.aliases: tuple[Alias, ...]`; `Alias.canonical: str`; `Alias.note: str`.
- Produces: the committed identity answers. No name.

`modeltiers/src/modeltiers/data/contracts.toml` is not edited: see "Floors and sentinels: confirmed" in the Calibration Record.

- [ ] **Step 1: Require a note on every committed alias**

In `modeltiers/tests/test_data_files.py`, replace:

```python
    assert all("/" in alias.canonical for alias in loaded.aliases)
```

with:

```python
    assert all("/" in alias.canonical for alias in loaded.aliases)
    # Calibration's rule: an alias states the evidence it rests on.
    assert [alias.canonical for alias in loaded.aliases if not alias.note.strip()] == []
```

- [ ] **Step 2: Run it**

Run: `uv run --directory modeltiers pytest tests/test_data_files.py -q`
Expected: PASS, `2 passed`. It passes before the file changes, because step 3's one alias has a note. The assertion is a guard for the edit that follows and for every later one, not a failing test for this task; the tests that fail first are Task 1's.

- [ ] **Step 3: Replace the committed file with the calibrated one**

Eleven aliases and five ignores. The first alias and the ignores for `nomic-embed-text:latest` and `Mistral Large` are the spec's, the last with its reason corrected. Every other entry is a row of the Calibration Record. Epoch's `Gemini 3.1 Pro`, `Muse Spark` and `Grok 4.20` have no entry, on purpose.

Replace the whole of `modeltiers/src/modeltiers/data/aliases.toml` with:

```toml
# Hand-kept identity answers, tried before the host row and before name
# matching. Read by modeltiers.aliases; validated on every run.
#
# [[alias]]   canonical = the key, as models.dev spells canonical_model_id
#             inventory = [{ provider_id = "...", model_name = "..." }, ...]
#             openrouter / arena / epoch = ["the source's own id", ...]
#             note      = why the entry exists
# [[ignore]]  inventory = { provider_id = "...", model_name = "..." }
#             or source = "openrouter" | "arena" | "epoch" with id = "..."
#             reason    = required
#
# An Arena id is written without its effort parenthesis: "GLM 5.3", not
# "GLM 5.3 (Max)". Every other id is exactly as the source spells it.
#
# Calibrated against the live sources and the owner's inventory on
# 2026-10-12. Every note states evidence that identifies the model, read
# that day: an id the publisher or the source gives, never a name, a date or
# an architecture alone. A row or a record with no such evidence has no
# entry here and stays listed by `run`. How to re-check an entry:
# docs/guides/12-model-tiers.md, step 4.

[[alias]]
canonical = "deepseek/deepseek-v4-pro-0813"
note = "ollama :cloud and :0813-cloud shared digest 6ed7420bc3a4 on 2026-10-12; the registry config for :cloud names remote_model deepseek-v4-pro:0813; Epoch's runs of DeepSeek V4 Pro 0813 are all under model_version deepseek-v4-pro-0813; Arena publishes no model id, so its row is matched on the snapshot date in Arena's own label, (0813), which this key alone carries among the three deepseek-v4-pro keys"
inventory = [{ provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }]
arena = ["DeepSeek V4 Pro (0813)"]
epoch = ["DeepSeek V4 Pro 0813"]

[[alias]]
canonical = "google/gemma-4-31b-it"
note = "the ollama registry config for gemma4:cloud names remote_model gemma4:31b, and its manifest is byte for byte :31b-cloud's (sha256 ef09f235533c), read 2026-10-12"
inventory = [{ provider_id = "ollama", model_name = "gemma4:cloud" }]

[[alias]]
canonical = "mistral/mistral-large-2512"
note = "the ollama registry config names remote_model mistral-large-3:675b, read 2026-10-12; models.dev's Bedrock row mistral.mistral-large-3-675b-instruct, the same model and size, carries this key"
inventory = [{ provider_id = "ollama", model_name = "mistral-large-3:675b-cloud" }]

[[alias]]
canonical = "nvidia/nemotron-3.5-lightning"
note = "ollama :30b-mlx and :30b-a3b-mlx shared digest 8b1474be6e54 on 2026-10-12; models.dev has one Nemotron 3.5 Lightning key, this one"
inventory = [{ provider_id = "ollama", model_name = "nemotron-3.5-lightning:30b-mlx" }]

[[alias]]
canonical = "nvidia/nemotron-3-ultra-550b-a55b"
note = "Epoch's runs of Nemotron 3 Ultra are all under model_version nemotron-3-ultra, read 2026-10-12; every models.dev row with that model id carries this key, and no other key is a Nemotron 3 Ultra"
epoch = ["Nemotron 3 Ultra"]

[[alias]]
canonical = "deepreinforce/ornith-1.5-35b-a3b"
note = "the ollama library ornith-1.5 has three tags, 397b, 35b and 9b, and its readme names three models, Ornith-1.5-397B, Ornith-1.5-35B-A3B and Ornith-1.5-9B, read 2026-10-12; this is models.dev's only Ornith 1.5 key"
inventory = [{ provider_id = "ollama", model_name = "ornith-1.5:35b" }]

[[alias]]
canonical = "alibaba/qwen3.6-35b-a3b"
note = "ollama :35b-mlx and :35b-a3b-nvfp4 shared digest e92a3e94bbca on 2026-10-12. The MTPLX repo's mtplx_runtime.json names base_trunk mlx-community/Qwen3.6-35B-A3B-6bit and a forge_recipe that is a quantization (body_bits 6, source_format mlx_affine); that repo's Hugging Face base_model is Qwen/Qwen3.6-35B-A3B, relation quantized"
inventory = [
    { provider_id = "ollama", model_name = "qwen3.6:35b-mlx" },
    { provider_id = "mtplx", model_name = "Youssofal/Qwen3.6-35B-A3B-MTPLX-Optimized-Balance" },
]

[[alias]]
canonical = "alibaba/qwen3.8-27b"
note = "Hugging Face base_model is Qwen/Qwen3.8-27B with base_model_relation quantized, read 2026-10-12"
inventory = [{ provider_id = "mtplx", model_name = "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality" }]

[[alias]]
canonical = "google/gemini-3-pro-preview"
note = "Epoch's runs of Gemini 3 Pro are all under model_version gemini-3-pro-preview, read 2026-10-12: this key's own id"
epoch = ["Gemini 3 Pro"]

[[alias]]
canonical = "google/gemini-3-flash-preview"
note = "Epoch's runs of Gemini 3 Flash are all under model_version gemini-3-flash-preview, read 2026-10-12: this key's own id"
epoch = ["Gemini 3 Flash"]

[[alias]]
canonical = "google/gemini-4-argon"
note = "declared here: Arena lists the model under organization google and models.dev had no key for it on 2026-10-12. Replace with models.dev's key once it has one"
arena = ["Gemini 4 Argon"]

[[ignore]]
inventory = { provider_id = "ollama", model_name = "nomic-embed-text:latest" }
reason = "embedding model"

[[ignore]]
inventory = { provider_id = "ollama", model_name = "medgemma:27b" }
reason = "medical fine-tune of Gemma 3 (ollama arch gemma3, 27.4B); models.dev has no key for it and no source scores it (checked 2026-10-12)"

[[ignore]]
inventory = { provider_id = "ollama", model_name = "ornith-1.5:9b" }
reason = "models.dev has an Ornith 1.5 key for the 35B only and no source scores the 9B (checked 2026-10-12)"

[[ignore]]
inventory = { provider_id = "openrouter", model_name = "nex-agi/nex-n2.5-pro:free" }
reason = "models.dev's OpenRouter row has no canonical id, no key exists for Nex N2.5 and no source scores it (checked 2026-10-12)"

[[ignore]]
source = "epoch"
id = "Mistral Large"
reason = "Epoch's runs are under model_version mistral-large-2402 (2024-02-26); models.dev has no key for that version, and the bare name must never match a later Mistral Large (checked 2026-10-12)"
```

- [ ] **Step 4: The committed files still load, and the suite passes**

Run: `uv run --directory modeltiers pytest tests/test_data_files.py -q`
Expected: PASS, `2 passed`.

Run: `make -C modeltiers test`
Expected: PASS, `494 passed`, coverage 100%.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/tests/test_data_files.py modeltiers/src/modeltiers/data/aliases.toml
git commit -m "feat(modeltiers): calibrate aliases.toml against the live sources of 2026-10-12"
```

---

### Task 3: Re-verify live, and decide what has changed since

**Files:**
- Modify, only if a rule below says so: `modeltiers/src/modeltiers/data/aliases.toml`, `modeltiers/src/modeltiers/data/contracts.toml`

**Interfaces:**
- Consumes: the `modeltiers run` command; `wt model list --json`.
- Produces: nothing code reads.

This task needs the network and the owner's machine. It is done by the owner, or by an agent the owner has told to do it. Nothing here is a test and nothing here runs in CI. The command blocks in this task are marked `sh`: they are run by hand, from the repo root.

**Every block stands alone, with two named exceptions.** No block uses a variable that an earlier block set: the scratch directory is the literal path `/tmp/modeltiers-calibration` on every line, each `modeltiers` command line carries its own `MODELTIERS_OUT` and `MODELTIERS_CACHE`, and Step 2's block defines its helpers at its own top. A line copied out of a block into a fresh shell therefore cannot write the real `tiers.toml`. The exceptions are two shell functions, `ladder` and `tierdump`. "Moving a floor" in Step 3 calls them and does not define them: they are pasted first, in the same shell, from the one block of guide step 5 that defines both. `ladder` reads the cache that its own command line names with `MODELTIERS_CACHE`, `tierdump` reads the file it is given, and neither writes anything; a line that calls one without the paste stops at `command not found`. The one bare `modeltiers run` is in Step 6, which is the owner's.

The sources move. Task 2's file was right on 2026-10-12; this task finds what is different on the day the plan is executed and says what to do about each difference.

- [ ] **Step 1: One live dry run into scratch paths**

```sh
mkdir -p /tmp/modeltiers-calibration
wt model list --json > /tmp/modeltiers-calibration/inventory.json
MODELTIERS_OUT=/tmp/modeltiers-calibration/tiers.toml MODELTIERS_CACHE=/tmp/modeltiers-calibration/cache uv run --directory modeltiers modeltiers run --dry-run --inventory /tmp/modeltiers-calibration/inventory.json > /tmp/modeltiers-calibration/report.txt 2> /tmp/modeltiers-calibration/warnings.txt; echo "exit $?"
cat /tmp/modeltiers-calibration/warnings.txt
grep -E '^(identity|unmapped|review|unresolved|sentinels):' /tmp/modeltiers-calibration/report.txt
sed -n '/^unresolved:/,/^sentinels:/p' /tmp/modeltiers-calibration/report.txt
```

Expected when nothing has moved, with the counts of 2026-10-12 (yours will differ wherever your inventory or a source has changed; only the conditions in bold are the result):

```text
exit 0
warning: provider mtplx probe status is partial; models it would have discovered may be absent from this run
identity: 46 rows: 42 resolved, 4 ignored, 0 unmapped
unmapped: 0 (each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)
review: 12 matched by name alone (check each)
unresolved: 3 at or above a standard floor (each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)
sentinels: 8 of 8 hold
unmapped: 0 (unrated until aliased or ignored)
unresolved: 3 at or above a standard floor (each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)
  epoch  Gemini 3.1 Pro  154.77
  epoch  Muse Spark  152.04
  epoch  Grok 4.20  151.98
  openrouter: 52 of 284 records unresolved, 0 ignored, 166 keys resolved
  arena: 1 of 52 records unresolved, 0 ignored, 49 keys resolved
  epoch: 158 of 274 records unresolved, 1 ignored, 115 keys resolved
sentinels: 8 of 8 hold
```

The second `unmapped: 0` is the table's own footer. **`unmapped: 0`; `sentinels: 8 of 8 hold`; and the records under `unresolved:` are `Gemini 3.1 Pro`, `Muse Spark` and `Grok 4.20`, the three the Calibration Record leaves open, and no other.** If all three hold, there is no `warning: alias for` line and the review list has no row that is missing from the Calibration Record, go to Step 5. Otherwise apply Step 3's rules.

- [ ] **Step 2: Re-read the evidence behind each entry**

One block, helpers first. They are the ones `docs/guides/12-model-tiers.md` step 4 prints, with the scratch cache written in, plus `epoch_row`. `hf_base` prints a repo's `base_model` and its relation to the repo; only `relation quantized`, or a publisher's explicit statement of a conversion, carries the base model's key across, and a `finetune`, a `merge` or an `adapter` does not. Each line's comment is the answer of 2026-10-12; where a line runs two commands the two answers are separated by three spaces.

```sh
CACHE=/tmp/modeltiers-calibration/cache
newest() { ls "$CACHE/$1"/*."$2" | grep -v '\.bad\.' | tail -1; }
ollama_manifest() { curl -fsS -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' "https://registry.ollama.ai/v2/library/$1/manifests/$2"; }
ollama_digest() { ollama_manifest "$1" "$2" | shasum -a 256 | cut -c1-12; }
ollama_config() { curl -fsSL "https://registry.ollama.ai/v2/library/$1/blobs/$(ollama_manifest "$1" "$2" | jq -r .config.digest)"; }
hf_base() { curl -fsS "https://huggingface.co/api/models/$1" | jq -r '"\([.cardData.base_model // "(none)"] | flatten | join(" ")), relation \((.cardData.base_model_relation // ([.tags[]? | select(startswith("base_model:")) | split(":") | select(length == 3) | .[1]] | unique | join(" "))) | if . == "" then "(not stated)" else . end)"'; }
keys() { jq -r --arg re "$1" '[.[] | .models[]? | select(.canonical_model_id != null) | select((.canonical_model_id + " " + (.name // "")) | test($re; "i")) | .canonical_model_id] | unique | join(" ")' "$(newest modelsdev json)"; }
idkeys() { jq -r --arg id "$1" '[.[] | .models[]? | select((.id | ascii_downcase | split("/") | last) == ($id | ascii_downcase | split("/") | last)) | .canonical_model_id // "(no key)"] | group_by(.) | map("\(.[0]) x\(length)") | join(", ")' "$(newest modelsdev json)"; }
epoch_row() { unzip -p "$(newest epoch zip)" epoch_capabilities_index/eci_scores.csv | grep "^$1," | cut -d, -f1-3,6; }
epoch_ids() { unzip -p "$(newest epoch zip)" epoch_capabilities_index/processed_data_for_eci.csv | python3 -c 'import collections, csv, sys; runs = collections.Counter(row["model_version"] for row in csv.DictReader(sys.stdin) if row["Model"] == sys.argv[1]); print(", ".join(f"{version} x{count}" for version, count in sorted(runs.items())))' "$1"; }

# 1. ollama cloud tags: the model the tag serves, then its key
ollama_config deepseek-v4-pro cloud | jq -r .remote_model        # deepseek-v4-pro:0813
idkeys 'deepseek-v4-pro:0813'                                    # deepseek/deepseek-v4-pro-0813 x1
ollama_digest deepseek-v4-pro cloud; ollama_digest deepseek-v4-pro 0813-cloud   # 47d1ad2bea02, twice (the manifest's sha256; for a cloud tag it is not the id ollama.com shows)
curl -fsSL https://ollama.com/library/deepseek-v4-pro/tags | grep -oE 'deepseek-v4-pro:[a-z0-9.-]+"|>[0-9a-f]{12}<' | uniq | tr -d '"<>' | paste -sd' ' -   # deepseek-v4-pro:cloud 6ed7420bc3a4 deepseek-v4-pro:0813-cloud 6ed7420bc3a4
ollama_config gemma4 cloud | jq -r .remote_model                 # gemma4:31b
ollama_digest gemma4 cloud; ollama_digest gemma4 31b-cloud       # ef09f235533c, twice
keys 'gemma.4.31b'                                               # google/gemma-4-31b-it
ollama_config mistral-large-3 675b-cloud | jq -r .remote_model   # mistral-large-3:675b
idkeys 'mistral-large-3:675b'                                    # (no key) x1
idkeys mistral.mistral-large-3-675b-instruct                     # mistral/mistral-large-2512 x1

# 2. ollama local tags: two tags, one digest, then the key
ollama_digest nemotron-3.5-lightning 30b-mlx; ollama_digest nemotron-3.5-lightning 30b-a3b-mlx   # 8b1474be6e54, twice
keys 'nemotron.3\.5.lightning'                                   # nvidia/nemotron-3.5-lightning
ollama_digest qwen3.6 35b-mlx; ollama_digest qwen3.6 35b-a3b-nvfp4   # e92a3e94bbca, twice
keys 'qwen3\.6.35b'                                              # alibaba/qwen3.6-35b-a3b
curl -fsSL https://ollama.com/library/ornith-1.5/tags | grep -o 'href="/library/ornith-1.5:[^"]*"' | sort -u   # three tags: :35b, :397b, :9b
curl -fsSL https://ollama.com/library/ornith-1.5:35b | grep -o '<h2>Ornith[^<]*'   # <h2>Ornith -1.5-397B, <h2>Ornith -1.5-35B-A3B, <h2>Ornith-1.5-9B
keys ornith                                                      # deepreinforce/ornith-1.0-35b deepreinforce/ornith-1.5-35b-a3b

# 3. Hugging Face repos: the publisher's statement of the origin and of the relation, then the key. Only a quantization inherits the key
hf_base Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality            # Qwen/Qwen3.8-27B, relation quantized (the card's own base_model_relation)
idkeys Qwen/Qwen3.8-27B                                          # (no key) x2, alibaba/qwen3.8-27b x40
hf_base Youssofal/Qwen3.6-35B-A3B-MTPLX-Optimized-Balance        # (none), relation (not stated)
curl -fsS https://huggingface.co/Youssofal/Qwen3.6-35B-A3B-MTPLX-Optimized-Balance/raw/main/mtplx_runtime.json | jq -r '"\(.base_trunk), body_bits \(.forge_provenance.forge_recipe.body_bits), source_format \(.forge_provenance.source_format)"'   # mlx-community/Qwen3.6-35B-A3B-6bit, body_bits 6, source_format mlx_affine (the publisher's conversion statement: that repo's weights, 6-bit)
curl -fsS https://huggingface.co/Youssofal/Qwen3.6-35B-A3B-MTPLX-Optimized-Balance/raw/main/README.md | grep -o 'a 6-bit MLX body paired with'   # a 6-bit MLX body paired with (the card says the same in words; nothing in the repo names a fine-tune)
hf_base mlx-community/Qwen3.6-35B-A3B-6bit                       # Qwen/Qwen3.6-35B-A3B, relation quantized (from the base_model:quantized: tag; the card gives no base_model_relation)
idkeys Qwen/Qwen3.6-35B-A3B                                      # (no key) x3, alibaba/qwen3.6-35b-a3b x26

# 4. Epoch records: column 1 (Model), display name, score and date; the model ids Epoch ran; the key of each id
epoch_row 'DeepSeek V4 Pro 0813'; epoch_ids 'DeepSeek V4 Pro 0813'   # DeepSeek V4 Pro 0813,DeepSeek V4 Pro 0813,155.31,2026-08-13   deepseek-v4-pro-0813_high x1, deepseek-v4-pro-0813_low x1, deepseek-v4-pro-0813_max x9, deepseek-v4-pro-0813_unknown x4
idkeys deepseek-v4-pro-0813                                      # deepseek/deepseek-v4-pro-0813 x41
epoch_row 'Gemini 3 Pro'; epoch_ids 'Gemini 3 Pro'               # Gemini 3 Pro,Gemini 3 Pro,152.92,2025-11-18   gemini-3-pro-preview x21, gemini-3-pro-preview_low x1
idkeys gemini-3-pro-preview                                      # (no key) x4, google/gemini-3-pro-preview x2
epoch_row 'Gemini 3 Flash'; epoch_ids 'Gemini 3 Flash'           # Gemini 3 Flash,Gemini 3 Flash,151.8,2025-12-17   gemini-3-flash-preview x13, gemini-3-flash-preview_high x6, gemini-3-flash-preview_low x1, gemini-3-flash-preview_minimal x1
idkeys gemini-3-flash-preview                                    # (no key) x8, google/gemini-3-flash-preview x18
epoch_row 'Nemotron 3 Ultra'; epoch_ids 'Nemotron 3 Ultra'       # Nemotron 3 Ultra,Nemotron 3 Ultra,146.17,2026-06-04   nemotron-3-ultra x9
idkeys nemotron-3-ultra; keys 'nemotron.3.ultra'                 # nvidia/nemotron-3-ultra-550b-a55b x2   nvidia/nemotron-3-ultra-550b-a55b

# 5. the declared key: still no models.dev key?
jq -r '.pages[] | fromjson | .rows[].row | select(.model_name | test("Argon")) | [.model_name, .organization] | @tsv' "$(newest arena json)"   # Gemini 4 Argon (High)	google
keys 'gemini.4|argon'                                            # (an empty line: the declared key is still needed)

# 6. the ignores: still nothing to resolve to?
keys medgemma                                                    # (an empty line)
jq -r '[.data[] | select(.id | test("medgemma|ornith"; "i"))] | length' "$(newest openrouter json)"   # 0
jq -r '[.pages[] | fromjson | .rows[].row.model_name | select(test("medgemma|ornith|nex.n2"; "i"))] | length' "$(newest arena json)"   # 0
unzip -p "$(newest epoch zip)" epoch_capabilities_index/eci_scores.csv | grep -icE 'medgemma|ornith|nex.n2'   # 0
keys 'nex.n2'                                                    # nex-agi/nex-n2-pro
idkeys nex-n2.5-pro                                              # (no key) x2
jq -r '.data[] | select(.id == "nex-agi/nex-n2.5-pro") | .benchmarks.artificial_analysis.intelligence_index // "no score"' "$(newest openrouter json)"   # no score
epoch_row 'Mistral Large'; epoch_ids 'Mistral Large'             # Mistral Large,Mistral Large,122.01,2024-02-26   mistral-large-2402 x5
idkeys mistral-large-2402; keys 'mistral.large'                  # (no key) x1   mistral/mistral-large-2411 mistral/mistral-large-2512 mistral/mistral-large-4 mistral/mistral-large-latest

# 7. the three records left open: has the evidence changed?
epoch_row 'Gemini 3.1 Pro'; epoch_ids 'Gemini 3.1 Pro'           # Gemini 3.1 Pro,Gemini 3.1 Pro,154.77,2026-02-19   gemini-3.1-pro-preview x19, gemini-3.1-pro-preview-customtools x1, gemini-3.1-pro-preview_high x9, gemini-3.1-pro-preview_medium x1
idkeys gemini-3.1-pro-preview; idkeys gemini-3.1-pro-preview-customtools   # (no key) x5, google/gemini-3.1-pro-preview x24   (no key) x2, google/gemini-3.1-pro-preview-customtools x8
epoch_row 'Muse Spark'; epoch_ids 'Muse Spark'                   # Muse Spark,Muse Spark,152.04,2026-04-08   muse-spark x6
idkeys muse-spark; keys 'muse.spark'                             # (an empty line)   meta/muse-spark-1.1 meta/muse-spark-1.2 meta/muse-spark-1.3
epoch_row 'Grok 4.20'; epoch_ids 'Grok 4.20'                     # Grok 4.20,Grok 4.20,151.98,2026-02-17   grok-4-20 x8, grok-4.20-0309-reasoning x7
idkeys grok-4-20; idkeys grok-4.20-0309-reasoning                # (no key) x1   (no key) x1, xai/grok-4.20-0309-reasoning x5

# 8. the review list: each name match, checked
ollama_config gpt-oss 120b-cloud | jq -r .remote_model; idkeys 'gpt-oss:120b'   # gpt-oss:120b   (no key) x1, openai/gpt-oss-120b x1
ollama_config gpt-oss 20b-cloud | jq -r .remote_model; keys 'gpt.oss.20b'       # gpt-oss:20b   openai/gpt-oss-20b
ollama_config kimi-k2.6 cloud | jq -r .remote_model; idkeys kimi-k2.6           # kimi-k2.6   (no key) x17, moonshotai/kimi-k2.6 x52
ollama_config minimax-m2.7 cloud | jq -r .remote_model; idkeys minimax-m2.7     # minimax-m2.7   (no key) x12, minimax/MiniMax-M2.7 x31
ollama_config minimax-m3 cloud | jq -r .remote_model; idkeys minimax-m3         # minimax-m3   (no key) x8, minimax/MiniMax-M3 x46
ollama_digest qwen3.6 27b-mlx; ollama_digest qwen3.6 27b-nvfp4; keys 'qwen3\.6.27b'   # d49fd9e0da45, twice   alibaba/qwen3.6-27b
ollama_digest qwen3.8 27b-mlx; ollama_digest qwen3.8 27b-nvfp4; keys 'qwen3\.8.27b'   # 5642e97495e1, twice   alibaba/qwen3.8-27b
ollama_digest gemma4 31b-mlx; ollama_digest gemma4 31b-nvfp4                    # a22a363052da, twice
ollama_digest muse-glimmer 30b-mlx; ollama_digest muse-glimmer 30b-nvfp4-dflash; keys 'muse.glimmer'   # 015fa21845be, twice   meta/muse-glimmer-30b
hf_base mlx-community/Qwen3.8-27B-4bit                           # Qwen/Qwen3.8-27B, relation quantized (from the tag)
```

- [ ] **Step 3: Apply the decision rules to every difference**

**Guide commands, during calibration.** `docs/guides/12-model-tiers.md` prints its commands bare, as a user runs them against the real paths, and several rules below send you to a guide step. During calibration every guide command is run with the two variables inline, and the guide's helpers read the scratch cache. These are the prefixed lines; each pointer below names the ones it needs.

```sh
# P1. Guide step 4, "Set these up once per shell": use the first ten lines of Step 2's block instead. They are the guide's helpers, and their first line replaces the guide's CACHE= line:
CACHE=/tmp/modeltiers-calibration/cache
# P2. Guide step 4, "Then check and commit", and the `modeltiers run` that closes guide step 5: the check run. Never the bare run; that one is Step 6.
MODELTIERS_OUT=/tmp/modeltiers-calibration/tiers.toml MODELTIERS_CACHE=/tmp/modeltiers-calibration/cache uv run --directory modeltiers modeltiers run --dry-run --inventory /tmp/modeltiers-calibration/inventory.json; echo "exit $?"
# P3. Guide step 5, point 2: the ladder, after pasting the guide's `ladder` and `tierdump` block into this shell.
MODELTIERS_OUT=/tmp/modeltiers-calibration/tiers.toml MODELTIERS_CACHE=/tmp/modeltiers-calibration/cache ladder <signal> <low> <high> /tmp/modeltiers-calibration/inventory.json
# P4. Guide step 5, point 4: the new rule's table, written to a scratch file beside the baseline, then the comparison (bash or zsh).
MODELTIERS_OUT=/tmp/modeltiers-calibration/after.toml MODELTIERS_CACHE=/tmp/modeltiers-calibration/cache uv run --directory modeltiers modeltiers run --offline --inventory /tmp/modeltiers-calibration/inventory.json > /tmp/modeltiers-calibration/after.txt; echo "exit $?"
diff <(tierdump /tmp/modeltiers-calibration/tiers.toml) <(tierdump /tmp/modeltiers-calibration/after.toml)
```

| What differs | Rule |
|---|---|
| A row under `unmapped:` | A model was added to the inventory, or a host row lost its canonical id. Follow `docs/guides/12-model-tiers.md` step 4, cases (a) to (d), with every guide command prefixed (helpers: P1; the check run: P2): an `[[alias]]` with a note when evidence identifies the model; an `[[ignore]]` with a reason ending `(checked <date>)` when models.dev has no key for it and no source scores it; nothing when neither holds. A row left unmapped is added to this plan's "Left open" table in the PR description, with what was found and the entry that would close it |
| A record under `unresolved:` other than `Gemini 3.1 Pro`, `Muse Spark` and `Grok 4.20` | A source added a model. Guide step 4 (e), with every guide command prefixed (helpers: P1; the check run: P2): alias it when every model id the source ran belongs to one key; declare a key when no key or row of models.dev could be the model; otherwise leave it and record it as open |
| `Gemini 3.1 Pro`, `Muse Spark` or `Grok 4.20` is no longer under `unresolved:` | A source or models.dev changed. Run section 7 of Step 2's block. If the record now resolves by itself (a new key, a renamed row), check the match like a review-list row |
| An `epoch_ids` answer differs for an aliased record | Epoch re-ran the model. All ids still belong to the alias's key: update the note. Before checking, remove a trailing `_<effort>` or `_unknown` from each id (`_max`, `_high`, `_medium`, `_low`, `_minimal`): Epoch appends its run setting there and models.dev never has it. An id of another key has appeared, or an id no models.dev row has, in however few runs: remove the source id from the alias (the whole entry, when that id was all it held), leave the record unresolved and record it as open |
| `epoch_ids 'Gemini 3.1 Pro'` no longer prints `gemini-3.1-pro-preview-customtools` | Epoch dropped or re-labelled the one run. When every id it prints now belongs to `google/gemini-3.1-pro-preview` (check each with `idkeys`, after removing the same trailing `_<effort>`), add the entry from "Left open, for the owner" with a note that names the ids and the date in place of the owner's confirmation |
| `warning: alias for <key> names <source> id '<id>', which is not in this run's <source> data` | The model left that leaderboard. Leave the entry: a warning is the designed outcome. Exception: if the id was renamed (the same model is on the `unresolved:` list under a new spelling), add the new id to the same entry. For the declared key `google/gemini-4-argon`, when its one Arena id is gone and nothing replaces it, delete the entry |
| `keys 'gemini.4\|argon'` now prints a key | models.dev has caught up. Replace `google/gemini-4-argon` in `aliases.toml` with that key only when a models.dev row with that key has Google as its provider and a model id that names Argon; otherwise leave the declared key and record what was found |
| A `remote_model` answer differs (`deepseek-v4-pro:cloud`, `gemma4:cloud`, `mistral-large-3:675b-cloud`) | The tag now serves another model. Find the new tag's key with `idkeys '<new remote_model>'`, then `keys`. One key: re-point the alias and rewrite its note. No key and no source scores it: remove the pair from the alias and add an `[[ignore]]` naming the new `remote_model`. Otherwise remove the pair and leave the row unmapped |
| Two digests that were equal now differ | A tag was rebuilt, and the short tag is no longer shown to be the fully spelled one. Read the library page for a statement of what the short tag is. None: remove the pair from the alias and leave the row unmapped |
| An `hf_base` or `base_trunk` answer differs | The repo was changed under the same name. If it now names another origin and the relation is still a quantization (`relation quantized`, or the publisher's conversion statement), follow it with `hf_base` and `idkeys` and re-point the alias. If it names none, or the relation is now `finetune`, `merge` or `adapter`, remove the pair and leave the row unmapped: such a repo does not inherit the key |
| An `idkeys` answer names a second key for an id an alias rests on | models.dev split or merged a model. Read both keys' rows (`keys`, then the host rows) and keep the alias only if the evidence still leads to one key; otherwise remove the entry and record the row as open |
| `keys medgemma`, `keys 'nex.n2'` or `keys ornith` prints a key for an ignored model, or one of the three counts in section 6 is no longer 0 | The ignore has expired: there is now something to resolve to. Delete it and run again; the row will resolve by host row or by name, or need evidence like any other |
| A row on `review:` that is not in the Calibration Record | A new name match. Check it with guide step 4 (a) to (d), with the helpers of P1. Right: nothing to do. Wrong: add an `[[alias]]` to the right key |
| `sentinels: F of 8 FAILED`, exit 3 | Re-calibrate with guide step 5, with every guide command prefixed (the ladder: P3; the acceptance run and comparison: P4; the closing run: P2). One sentinel on one signal: change its pinned vote. A signal that `voted nothing`: drop it from that sentinel's `votes`. Several on one signal in one direction: move that signal's floors with the procedure under this table. Change `version` to the day's date |
| A source line says `stale` and the exit is 2 | A download or a parse failed and there is a good snapshot. Not a calibration finding. Run Step 1 again later |
| Exit 1 naming a source | A download failed and the scratch cache has no snapshot of it. Run Step 1 again later |
| The counts differ from 2026-10-12 and nothing above applies | Nothing to do. The counts are a record of one day |

**Moving a floor** (the procedure of guide step 5; its commands are the guide's with the two variables inline, lines P3 and P4 above). For each floor of the failing signal that a `differs:` line crosses:

1. From the `scores:` lines of the failed report (`/tmp/modeltiers-calibration/report.txt`), write down the signal's new score for each sentinel that pins a vote on it. `<low>` is the highest score among the sentinels pinned below the floor's tier, `<high>` the lowest among those pinned at or above it; `-inf` or `inf` where no sentinel is pinned on that side. If `<low>` is not under `<high>` the sentinels changed order: re-pin the differing votes instead and stop.
2. Paste the block of guide step 5 that defines `ladder` and `tierdump` into the shell. Then list the signal's resolved scores in the window, sorted, with the gap under each and its midpoint (line P3). Pass the bounds a little wider than printed, because the report rounds to four places:

   ```sh
   MODELTIERS_OUT=/tmp/modeltiers-calibration/tiers.toml MODELTIERS_CACHE=/tmp/modeltiers-calibration/cache ladder <signal> <low> <high> /tmp/modeltiers-calibration/inventory.json
   ```

   On 2026-10-12, `ladder intelligence_index 38.2 49.6` printed 25 lines, from `49.6000  gap below  1.5000  midpoint   48.8500  anthropic/claude-fable-5` down to `38.2000  anthropic/claude-sonnet-5`; its widest gap was that first one, between 49.6 and 48.1.
3. Take the midpoint of the widest gap (of two equal gaps, the one nearer the old floor). Round it to the precision `contracts.toml` uses for the signal: one decimal for `intelligence_index` and `eci`, three for `arena_agent` and `marathon_ci_lower`. The rounded value must still be above the gap's lower score and not above its upper score. Write it as the floor.
4. Accept the edit only when the checks below hold. The run that failed its sentinels exited 3 and wrote nothing, so the table to compare with cannot come from it. The baseline is `/tmp/modeltiers-calibration/tiers.toml`, the file Step 5 of an earlier, passing calibration wrote from the downloads of that day. The new rule's table goes to a second scratch file, and the two are compared key by key, inventory models and candidates alike (lines P4):

   ```sh
   MODELTIERS_OUT=/tmp/modeltiers-calibration/after.toml MODELTIERS_CACHE=/tmp/modeltiers-calibration/cache uv run --directory modeltiers modeltiers run --offline --inventory /tmp/modeltiers-calibration/inventory.json > /tmp/modeltiers-calibration/after.txt; echo "exit $?"
   diff <(tierdump /tmp/modeltiers-calibration/tiers.toml) <(tierdump /tmp/modeltiers-calibration/after.toml)
   tierdump /tmp/modeltiers-calibration/after.toml | cut -d' ' -f1,3 | sort | uniq -c
   make -C modeltiers test
   ```

   - the run prints `exit 0` and `/tmp/modeltiers-calibration/after.txt` holds `sentinels: 8 of 8 hold`. `exit 3` means a sentinel still differs: `after.toml` was not written and there is nothing to compare yet;
   - it does not print `exit 1` with a `modeltiers: contracts.toml:` line (floors that no longer rise with the tier);
   - every line `diff` prints has been read. A `<` line is a key's tier in the baseline and a `>` line its tier under the new rule. Each must be one that can be accounted for: a model its source re-scored since the baseline, a model that joined or left a source or the inventory, or a model whose score lies next to the floor that moved. Many keys of one tier moving together means the floor is in the wrong gap. Keys leaving a tier whose floor was not touched mean the scale moved under that floor too, without carrying a sentinel across it: move that floor the same way;
   - when there is no baseline file (the first calibration on this machine, with Step 1 already at exit 3), `tierdump` stops at the missing file with `FileNotFoundError` and `diff` shows every key as new. The comparison is then the third line's counts against those of 2026-10-12, below, together with Step 5's list of the keys at `marathon` and `deep`. A count that moved by more than the models the `differs:` lines name is a floor in the wrong gap;
   - the suite passes.

   The third line printed this for the table of 2026-10-12:

   ```text
      8 candidate deep
      3 candidate marathon
     84 candidate quick
     32 candidate standard
     40 candidate unrated
     21 model quick
     15 model standard
     10 model unrated
   ```
After any edit, run Step 1's `uv run` line again, and `make -C modeltiers test`.

- [ ] **Step 4: Commit, only if Step 3 changed a file**

```sh
git add modeltiers/src/modeltiers/data/aliases.toml modeltiers/src/modeltiers/data/contracts.toml
git commit -m "chore(modeltiers): re-calibrate against the live sources of <date>"
```

Say in the PR description which rule applied to which entry, and list every row left open.

- [ ] **Step 5: Look at the candidates once**

```sh
MODELTIERS_OUT=/tmp/modeltiers-calibration/tiers.toml MODELTIERS_CACHE=/tmp/modeltiers-calibration/cache uv run --directory modeltiers modeltiers run --offline --inventory /tmp/modeltiers-calibration/inventory.json > /dev/null; echo "exit $?"
MODELTIERS_OUT=/tmp/modeltiers-calibration/tiers.toml MODELTIERS_CACHE=/tmp/modeltiers-calibration/cache uv run --directory modeltiers modeltiers show --candidates | awk '$2 == "marathon" || $2 == "deep"'
```

The first line writes `/tmp/modeltiers-calibration/tiers.toml` from the snapshots Step 1 saved (every source then says `stale`, which is what `--offline` means) and prints `exit 0`, after any provider warning Step 1 showed, which goes to stderr again. Keep the file: it is the baseline that "Moving a floor" compares with at the next calibration. macOS clears `/tmp`, so copy it somewhere lasting if that calibration may be weeks away; once the owner has made the first real run, the real `tiers.toml` is the baseline and this copy no longer matters.

Expected on 2026-10-12: three `marathon` lines and eight `deep` lines, the ones named in the Calibration Record:

```text
M  marathon  high    anthropic/claude-fable-5-1
M  marathon  high    anthropic/claude-opus-5-5
M  marathon  high    openai/gpt-6-astra
D  deep      high    anthropic/claude-fable-5
D  deep      high    anthropic/claude-opus-5
D  deep      high    anthropic/claude-sonnet-5-5
D  deep      low     google/gemini-4-argon
D  deep      low     openai/gpt-5.5-pro
D  deep      medium  openai/gpt-5.6-sol
D  deep      medium  openai/gpt-6-sol
D  deep      high    openai/gpt-6.1-sol
```

A different list is not an error; it is what the sources say that day, and the sentinels have already vouched for the floors.

- [ ] **Step 6 (owner only): the first real run**

This writes `~/.config/local-ai/tiers.toml` and creates `~/.cache/local-ai/modeltiers/`. It is the one step of this plan that touches the real config home and the real cache, and the one place a bare `modeltiers run` appears. It is the owner's to run, by hand, in a shell where the two variables are not set. An agent executing this plan stops before this step and reports.

```sh
env -u MODELTIERS_OUT -u MODELTIERS_CACHE uv run --directory modeltiers modeltiers run; echo "exit $?"
env -u MODELTIERS_OUT -u MODELTIERS_CACHE uv run --directory modeltiers modeltiers show
```

Expected: `unmapped: 0`, `sentinels: 8 of 8 hold`, under `unresolved:` only the records recorded as open, a last line `wrote /Users/<you>/.config/local-ai/tiers.toml` and `exit 0`. That is Delivery item 4's "complete by itself".

---

### Task 4: The user guide, the config map and the README index

**Files:**
- Create: `modeltiers/tests/test_guide_samples.py`
- Create: `docs/guides/12-model-tiers.md`
- Modify: `docs/guides/00-config-map.md` (line 17, the TL;DR table; line 58, before "Left on disk, read by nothing"; line 233 and line 243)
- Modify: `README.md` (lines 3, 10, 31, 80, 84 and 85)

**Interfaces:**
- Consumes: the commands, exit codes and messages of steps 1 to 3; Task 2's file; from `tests/conftest.py`: `FIXTURES`, `SAMPLE`, `T0`, and the fixtures `specs`, `good_answers`, `read_fixture`; from `tests/fakes.py`: `FakeFetcher`, `Sleeps`.
- Produces: nothing code reads.

The repo's rule is that a guide never embeds live model state. Here that means three things, and each has a check:

- every command names its model as `<provider>/<model>` or `<canonical key>` (the third test below, and Step 7);
- every sample block is what `modeltiers` prints for the package's test fixtures, word for word, and is labelled "Illustrative example from the test fixtures" (the first two tests below);
- no count, row or tier of the live run appears (Step 7).

- [ ] **Step 1: Write the failing tests**

Write `modeltiers/tests/test_guide_samples.py`:

````python
"""The sample blocks in the user guide and the config map are output of this
package over the golden fixtures, word for word. Guides must not embed a real
machine's models, so a sample is either a placeholder or a slice of what
these tests can reproduce."""

import json
import re
from collections.abc import Callable

import pytest
from conftest import FIXTURES, SAMPLE, T0
from fakes import FakeFetcher, Sleeps

from modeltiers import cli, fetch
from modeltiers.loader import SourceSpecs

GUIDES = SAMPLE.parent.parent / "guides"
GUIDE = GUIDES / "12-model-tiers.md"
CONFIG_MAP = GUIDES / "00-config-map.md"
RUN = ["run", "--inventory", str(FIXTURES / "inventory.json")]
Answers = dict[str, list[bytes | Exception]]


def _blocks(text: str, language: str) -> list[str]:
    """The body of every fenced block of one language, each ending in a newline."""
    return re.findall(rf"^```{language}\n(.*?^)```$", text, re.M | re.S)


def _deps(specs: SourceSpecs, answers: Answers) -> cli.Deps:
    return cli.Deps(
        fetcher=FakeFetcher(answers),
        sleep=Sleeps(),
        now=lambda: T0,
        specs=specs,
        aliases_path=FIXTURES / "aliases.toml",
        contracts_path=FIXTURES / "contracts.toml",
    )


@pytest.fixture
def printed(
    specs: SourceSpecs,
    good_answers: Answers,
    read_fixture: Callable[[str], bytes],
    capsys: pytest.CaptureFixture[str],
) -> str:
    """Everything the fixtures make the three commands print: a run, `explain`
    of one row, and a run whose intelligence indices dropped by a fifth."""
    assert cli.main(RUN, deps=_deps(specs, dict(good_answers))) == 0
    assert cli.main(["explain", "ollama/minimax-m3:cloud"], deps=_deps(specs, {})) == 0
    doc = json.loads(read_fixture("openrouter.json"))
    for model in doc["data"]:
        indices = (model.get("benchmarks") or {}).get("artificial_analysis")
        if indices:
            indices["intelligence_index"] *= 0.8
    drifted = {**good_answers, fetch.OPENROUTER_URL: [json.dumps(doc).encode()]}
    assert cli.main([*RUN, "--dry-run"], deps=_deps(specs, drifted)) == 3
    return capsys.readouterr().out


def test_every_output_sample_in_the_guide_is_fixture_output(printed: str) -> None:
    samples = _blocks(GUIDE.read_text(), "text")
    # The report's head, the table, one explain, one failed sentinel table.
    assert len(samples) == 4
    for sample in samples:
        assert sample in printed


def test_the_config_map_example_is_the_committed_sample() -> None:
    section = CONFIG_MAP.read_text().split("### `~/.config/local-ai/tiers.toml`\n")[1]
    (example,) = _blocks(section.split("\n### ")[0], "toml")
    head, entry = example.split("\n\n", 1)
    sample = SAMPLE.read_text()
    assert sample.startswith(head + "\n")
    assert "\n" + entry in sample
    assert entry.startswith("[[models]]\n")


def test_no_command_in_the_guide_names_a_fixture_model() -> None:
    names = re.compile(r"glm-|deepseek|qwen|medgemma|minimax|gemma|claude-|nomic", re.I)
    for block in _blocks(GUIDE.read_text(), "bash"):
        assert not names.search(block), block
````

- [ ] **Step 2: Run them and see them fail**

Run: `uv run --directory modeltiers pytest tests/test_guide_samples.py -q`
Expected: FAIL with `3 failed`. Two stop at `FileNotFoundError` on `12-model-tiers.md`, which does not exist yet; `test_the_config_map_example_is_the_committed_sample` stops at `IndexError`, because the config map has no `tiers.toml` section yet.

- [ ] **Step 3: Write the guide**

Write `docs/guides/12-model-tiers.md`:

````markdown
# Model tiers — `modeltiers`

> Use this to: see which of your models suit which kind of work. `modeltiers`
> reads the wt inventory and four public sources, gives every model an
> advisory capability tier with the evidence behind it, and writes
> `~/.config/local-ai/tiers.toml`. Nothing routes on a tier yet.
>
> Verified against: modeltiers 0.1.0 on 2026-10-12 (one live run against all four sources).

Design rationale: `docs/superpowers/specs/2026-10-12-model-tiers-design.md`.

## Prerequisites

- `wt` on PATH. `modeltiers` runs one wt command, `wt model list --json`, which only reads.
- `uv`, and the package installed: `make install` from the repo root, or `make -C modeltiers install`.
- Network access to `models.dev`, `openrouter.ai`, `datasets-server.huggingface.co` and `epoch.ai`. No API key is needed.
- For the evidence commands in steps 4 and 5: `curl`, `jq`, `shasum`, `unzip` and `python3`.

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
uv run --directory modeltiers modeltiers run --dry-run   # fetch, resolve, assign, report; no tiers.toml written
uv run --directory modeltiers modeltiers run             # the same, then write tiers.toml
uv run --directory modeltiers modeltiers show            # the tier table, from the file
uv run --directory modeltiers modeltiers explain <provider_id/model_name>
```

`modeltiers` never writes `registry.toml`, `config.yaml` or any wt state. It
writes `tiers.toml` and its own snapshot cache, and nothing else
([00-config-map](00-config-map.md)).

## What a tier means

A tier is a capability band, set from public benchmark scores. It is a label
to read. No launch, rotation or route looks at it.

| Code | Tier | Intended work |
|---|---|---|
| `M` | `marathon` | Long autonomous sessions |
| `D` | `deep` | Planning, architecture, hard debugging |
| `S` | `standard` | Everyday coding |
| `Q` | `quick` | Lookups, renames, background jobs |
| `-` | `unrated` | No tier; the entry says why |

- **Three signals vote.** The Artificial Analysis intelligence index (through OpenRouter), Arena's agent score and Epoch AI's capabilities index each vote a tier from a score floor, or abstain when they have no record of the model. The tier is the lower median of the votes: with two votes, the lower one.
- **`marathon` has one route.** A model whose votes give `deep` becomes `marathon` when the lower bound of its Arena interval clears the marathon floor.
- **Two gates can only lower a tier.** A model with no tool calling is `unrated`. A model whose context is under its tier's minimum drops to the highest tier it meets.
- **Confidence** says how far the votes agree: `high` (two or more, all equal), `medium` (one tier apart), `low` (one vote, or two tiers apart), `none` (no votes).
- **A local build inherits.** Every public score describes a hosted serving of a model. A local row carries the tier of the model it resolves to, marked `basis = "inherited"`: the quantized build on your machine was never measured.
- **`unrated` always has a reason:** `no_votes` (no source scores it), `below_floor` (the votes are under the quick floor), `gate_tool_call`, `gate_context`, or `not_applicable` (you listed it under `[[ignore]]`).
- **Candidates** are models a source scores that are not in your inventory. They are tiered the same way, which makes the candidate list the shortlist of models worth adding.

The floors, the context minimums and the pinned sentinel models are data, in
`modeltiers/src/modeltiers/data/contracts.toml`. Read the values there; this
guide does not repeat them.

## Steps

### 1. Run it

```bash
uv run --directory modeltiers modeltiers run --dry-run
```

`--dry-run` does everything except write `tiers.toml`, and returns the exit
code the real run would return. The one exception is a redirected registry
(see Gotchas): the real run is refused with exit 1, and the dry run reports
and warns. It still saves each download as a snapshot under
`~/.cache/local-ai/modeltiers/`. When the report is what you expect, run it
again without the flag.

The report, in order: the inventory line; one line per source; how every
inventory row was identified; the `unmapped:` list; the `review:` list of rows
matched by name alone; the `unresolved:` benchmark records; the sentinel
check; what changed since the last file (or the whole table when there is no
file yet); the attribution lines; `wrote <path>` or `dry run: <path> not
written`.

Illustrative example from the test fixtures (`modeltiers/tests/fixtures/`),
not from any machine: the first lines of the report, exactly as the tool
prints them for those files. Your models, counts and scores will differ.

```text
inventory: 8 models (1 native rows dropped), registry /home/owner/.config/local-ai/registry.toml
modelsdev: ok, 12 records, fetched 2026-10-12T15:58:00Z
openrouter: ok, 6 records, fetched 2026-10-12T15:58:00Z
arena: ok, 6 records, fetched 2026-10-12T15:58:00Z
epoch: ok, 6 records, fetched 2026-10-12T15:58:00Z
identity: 8 rows: 6 resolved, 1 ignored, 1 unmapped
  ollama/glm-5.3:cloud            host        zhipuai/glm-5.3
  ollama/deepseek-v4-pro:cloud    alias       deepseek/deepseek-v4-pro-0813
  openrouter/z-ai/glm-5.3         host        zhipuai/glm-5.3
  ollama/qwen3.8:27b-mlx          normalized  alibaba/qwen3.8-27b  [inherited]
  ollama/medgemma:27b             unmapped
  ollama/minimax-m3:cloud         normalized  minimax/MiniMax-M3  [host row has no canonical id; matched by name]
  ollama/nomic-embed-text:latest  ignored     embedding model
  ollama/gemma4:31b-mlx           normalized  google/gemma-4-31b-it  [inherited]
unmapped: 1 (each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)
  ollama/medgemma:27b
review: 3 matched by name alone (check each)
  ollama/qwen3.8:27b-mlx -> alibaba/qwen3.8-27b
  ollama/minimax-m3:cloud -> minimax/MiniMax-M3
  ollama/gemma4:31b-mlx -> google/gemma-4-31b-it
unresolved: 1 at or above a standard floor (each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)
  arena  Qwen3.8 Max  0.0235
  openrouter: 1 of 6 records unresolved, 0 ignored, 4 keys resolved
  arena: 1 of 6 records unresolved, 0 ignored, 5 keys resolved
  epoch: 0 of 6 records unresolved, 1 ignored, 5 keys resolved
sentinels: 4 of 4 hold
```

After a `changes:` line the same fixture run prints the table, which is also
what `show` prints (illustrative example from the test fixtures):

```text
T  tier      conf    model                           canonical
S  standard  high    ollama/glm-5.3:cloud            zhipuai/glm-5.3
S  standard  high    ollama/deepseek-v4-pro:cloud    deepseek/deepseek-v4-pro-0813
S  standard  high    openrouter/z-ai/glm-5.3         zhipuai/glm-5.3
S  standard  low     ollama/qwen3.8:27b-mlx          alibaba/qwen3.8-27b
Q  quick     high    ollama/minimax-m3:cloud         minimax/MiniMax-M3
-  unrated   none    ollama/nomic-embed-text:latest  -  (not_applicable)
-  unrated   none    ollama/gemma4:31b-mlx           google/gemma-4-31b-it  (no_votes)
unmapped: 1 (unrated until aliased or ignored)
  ollama/medgemma:27b
candidates: 2 (list them with --candidates)
```

| Exit | Meaning | What to do |
|---|---|---|
| 0 | Written (or would be, under `--dry-run`) | Read the lists; see step 3 |
| 1 | Not written: an invalid `aliases.toml` or `contracts.toml`, wt missing or failing, a redirected registry (a run without `--dry-run`; see Gotchas), or a source with no usable data and no good snapshot | The `modeltiers:` line on stderr names the entry, the file or the source |
| 2 | Written, with a source that fell back to its last good snapshot | The source's line says `stale`. Run again later; the tiers from that source are as old as its `fetched` time |
| 3 | Not written: a sentinel model's votes or tier moved | Step 5 |

Two more flags:

- `--offline` fetches nothing and reads each source's newest good snapshot. Every source is then `stale` and the exit code is still 0.
- `--inventory FILE` reads saved `wt model list --json` output and does not run wt.

### 2. Read the result

```bash
uv run --directory modeltiers modeltiers show                             # inventory models
uv run --directory modeltiers modeltiers show --candidates                # and the candidates
uv run --directory modeltiers modeltiers explain <provider>/<model>       # one row, by provider_id/model_name
uv run --directory modeltiers modeltiers explain <canonical key>          # every row of a canonical key, or the candidate
```

Both read `tiers.toml` and nothing else: no fetch, no wt. `show` starts with
the file's age and warns on stderr once it is more than 14 days old. Both end
with the attribution lines of the sources whose data they print.

`<provider>/<model>` is a row as `show` prints it in its `model` column;
`<canonical key>` is the value in its `canonical` column.

Illustrative example from the test fixtures: what `explain` prints for one
fixture row, before its attribution lines.

```text
ollama/minimax-m3:cloud
  identity:    minimax/MiniMax-M3, resolved by normalized
  tier:        quick (Q)
  confidence:  high, basis measured
  votes:       arena_agent quick, eci quick
  scores:      arena_agent -0.0695, arena_agent_ci_lower -0.0786, eci 146.95
  efforts:     none
  tool_call:   true
  context:     512000
  note:        host row has no canonical id; matched by name
```

- `identity` is the canonical key (models.dev's spelling) and how the row reached it: `alias` (you said so in `aliases.toml`), `host` (models.dev lists the host's own id beside the key) or `normalized` (the name alone matched exactly one key).
- `efforts` names the reasoning effort of the Arena row that was used. A tier describes the best-scoring effort row, which may not be what a default launch gives you.
- `coding_index` and `agentic_index` appear under `scores` as evidence. They do not vote.

To read the file directly, its format is
[`docs/contracts/tiers.sample.toml`](../contracts/tiers.sample.toml). A model
absent from the file, or listed under `[[unmapped]]`, is `unrated`.

### 3. Decide what the report asks of you

| The report shows | It means | Do |
|---|---|---|
| `unmapped: N` with N above 0 | N inventory rows reached no canonical key and have no tier | Step 4, once per row |
| `unresolved: N at or above a standard floor` with N above 0 | N benchmark records that would vote `standard` or higher matched no key, so a model is missing a vote or a candidate is missing | Step 4, once per record |
| A row on the `review:` list you have not checked before | Its name alone picked the key. That can be wrong without failing | Check it with step 4's evidence commands. Right: nothing to do. Wrong: add an `[[alias]]` to the right key, or an `[[ignore]]` when there is none |
| `warning: alias for <key> names <source> id '<id>', which is not in this run's <source> data` | A model you aliased has left that leaderboard | Nothing, unless the id was only renamed: then add the new id to the same entry |
| `warning: provider <name> probe status is ...` | wt could not fully probe a provider, so its discovered models may be missing from this run | Fix the provider and run again, or accept the smaller inventory |
| A source line that says `stale` and exit 2 | That source's download or parse failed and its last good snapshot was used | Run again later. A file under the cache named `<stamp>.bad.<ext>` is the download that failed to parse |
| `sentinels: F of N FAILED` and exit 3 | A pinned model's score crossed a floor | Step 5 |

A run with `unmapped: 0`, `unresolved: 0`, every sentinel holding and exit 0
needs nothing from you. A row or a record that stays listed because step 4
found no evidence for it is not a fault: it is `unrated`, or a missing vote,
and the list says so on every run until someone finds the evidence.

### 4. Add an alias or an ignore

The file is `modeltiers/src/modeltiers/data/aliases.toml`. It is committed, so
a change is a commit.

```toml
# An inventory row, or a source's own id, that means this key.
[[alias]]
canonical = "<vendor/model as models.dev spells canonical_model_id>"
note = "<the evidence, and the date you read it>"
inventory = [{ provider_id = "<provider_id>", model_name = "<model_name>" }]
openrouter = ["<OpenRouter id>"]
arena = ["<Arena name without its effort parenthesis>"]
epoch = ["<Epoch display name>"]

# A row or a record that has nothing to resolve to (a finding; see (f)).
[[ignore]]
inventory = { provider_id = "<provider_id>", model_name = "<model_name>" }
reason = "<why, and what you checked>"

[[ignore]]
source = "<openrouter | arena | epoch>"
id = "<the source's own id>"
reason = "<why>"
```

An `[[alias]]` needs `canonical` and at least one of the four lists. The
`inventory` pair is exactly the `provider_id` and `model_name` that
`wt model list --json` prints; ids are exactly as the source spells them,
except that an Arena name is written without `(High)`, `(Max)` and the like.

**The rule: an alias needs evidence that identifies the model, and its `note`
says what the evidence is and the date you read it.** Evidence that identifies
is an id that the model's publisher, its distributor or the benchmark source
itself gives, and that leads to exactly one key. These identify nothing,
alone or together, because a fine-tune, a sibling and a successor share them:

- a name that looks like the key (name matching is what the tool already does, and its matches are on the `review:` list because a name can be wrong);
- a release date that equals the key's;
- an architecture, a layer count, a vocabulary size or a parameter count, from a registry config or a `config.json`.

When you find no identifying evidence, write nothing. The row stays under
`unmapped:` and the record under `unresolved:`, and the model is `unrated` or
short of a vote. That is the honest state. A guess is worse: it gives a model
another model's tier and nothing fails. Do not write an `[[ignore]]` to empty
a list either; an ignore is also a claim (see (f)).

Set these up once per shell. They read the snapshots the last run saved:

```bash
CACHE="${MODELTIERS_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/local-ai/modeltiers}"
newest() { ls "$CACHE/$1"/*."$2" | grep -v '\.bad\.' | tail -1; }   # newest good snapshot: newest modelsdev json
ollama_manifest() { curl -fsS -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' "https://registry.ollama.ai/v2/library/$1/manifests/$2"; }
ollama_digest() { ollama_manifest "$1" "$2" | shasum -a 256 | cut -c1-12; }
ollama_config() { curl -fsSL "https://registry.ollama.ai/v2/library/$1/blobs/$(ollama_manifest "$1" "$2" | jq -r .config.digest)"; }
# a Hugging Face repo's base_model and how the repo relates to it (the card's base_model_relation, else the relation in the tag Hugging Face derives)
hf_base() { curl -fsS "https://huggingface.co/api/models/$1" | jq -r '"\([.cardData.base_model // "(none)"] | flatten | join(" ")), relation \((.cardData.base_model_relation // ([.tags[]? | select(startswith("base_model:")) | split(":") | select(length == 3) | .[1]] | unique | join(" "))) | if . == "" then "(not stated)" else . end)"'; }
# every key whose id or display name matches a pattern
keys() { jq -r --arg re "$1" '[.[] | .models[]? | select(.canonical_model_id != null) | select((.canonical_model_id + " " + (.name // "")) | test($re; "i")) | .canonical_model_id] | unique | join(" ")' "$(newest modelsdev json)"; }
# the keys that models.dev rows with exactly this model id carry (a vendor prefix before the last "/" is not compared)
idkeys() { jq -r --arg id "$1" '[.[] | .models[]? | select((.id | ascii_downcase | split("/") | last) == ($id | ascii_downcase | split("/") | last)) | .canonical_model_id // "(no key)"] | group_by(.) | map("\(.[0]) x\(length)") | join(", ")' "$(newest modelsdev json)"; }
# the model ids Epoch ran under one display name, with the number of benchmark runs under each
epoch_ids() { unzip -p "$(newest epoch zip)" epoch_capabilities_index/processed_data_for_eci.csv | python3 -c 'import collections, csv, sys; runs = collections.Counter(row["model_version"] for row in csv.DictReader(sys.stdin) if row["Model"] == sys.argv[1]); print(", ".join(f"{version} x{count}" for version, count in sorted(runs.items())))' "$1"; }
```

Then pick the case. `<name>` and `<tag>` are the two halves of an ollama
model name, `<name>:<tag>`.

**(a) An ollama cloud tag.** The registry says which model the tag serves:

```bash
ollama_config <name> <tag> | jq -r .remote_model
```

The answer is a size-specific or dated tag: the distributor's own statement
of the model. Find its key with (d).

**(b) An ollama local tag with a short name** (a tag that leaves out part of
the model's name). Two tags of one library with one digest are one build, so
the short tag is the fully spelled one:

```bash
ollama_digest <name> <tag>
ollama_digest <name> <the fully spelled tag you think it equals>
```

Equal digests: find the fully spelled tag's key with (d). Different digests,
or no fully spelled tag: read the library's own page
(`https://ollama.com/library/<name>`) for a statement of which model each tag
is. A `model_family` or a parameter count in the registry config is not such
a statement.

**(c) A Hugging Face repo** (an mtplx or omlx row named `<owner>/<repo>`):

```bash
hf_base <owner>/<repo>
```

It prints two things: the card's `base_model`, which is the publisher saying
what the repo was made from, and the relation between the two, which is the
card's `base_model_relation` or, when the card gives none, the relation in the
`base_model:<relation>:<repo>` tag Hugging Face derives. The relation is
`quantized`, `finetune`, `merge` or `adapter`.

**Only a quantization inherits the base model's key.** A quantized copy is the
same model in another encoding, so `relation quantized` sends you to (d) with
the `base_model` it printed. A fine-tune, a merge and an adapter are other
models that share a base: they do not take its key, however close the name.
With `relation (not stated)` the repo inherits only when one of the
publisher's own files says in so many words that it is a conversion of the
base (a quantization recipe, a "converted from" line); with no such statement
it does not.

When it prints `(none)`, look for the same two statements in the publisher's
other files: list them and read any manifest or runtime file for the repo it
was built from and for how it was built, then ask that repo. Every link of
the chain has to be a quantization or a stated conversion.

```bash
curl -fsS "https://huggingface.co/api/models/<owner>/<repo>" | jq -r '.siblings[].rfilename'
curl -fsS "https://huggingface.co/<owner>/<repo>/raw/main/<a manifest or runtime json>" | jq .
hf_base <the repo that file names>
```

If nothing in the repo names where it came from, or a link of the chain is a
fine-tune, a merge or an adapter, the base model's key is not this model's:
leave the row unmapped, or go to (f). The repo's name, and a `config.json`
that matches another model's, do not count.

**(d) Find the key in models.dev.** Start from the id the evidence gave you,
not from a name:

```bash
idkeys '<the id: an upstream repo, a dated tag, a source's model id>'
keys '<regex for the model's name>'
```

`idkeys` prints the keys that rows with that model id carry. One key (beside
`(no key)`, the hosts that give none): that is the `canonical`. `keys` then
shows whether another key could be meant; if one could, say in the note why
it is a different product, or go to (f). `idkeys` printing nothing, or only
`(no key)`: use `keys`, and accept its answer only when it prints exactly one
key for the full designation the evidence gave (name and size).

**(e) A benchmark record** from the `unresolved:` list. Ask the source which
model id it ran:

```bash
# Epoch: the versioned model ids behind a display name, then the key each belongs to
epoch_ids '<display name>'
idkeys '<a model id it printed, without its _high / _low effort suffix>'
# Arena: name, organization, score, lower bound (Arena publishes no model id)
jq -r '.pages[] | fromjson | .rows[].row | select(.model_name | test("<name>"))
  | [.model_name, .organization, .score, .score_ci_lower] | @tsv' "$(newest arena json)"
# OpenRouter: id, the upstream repo it names, and the intelligence index
jq -r '.data[] | select(.id | test("<id>"))
  | [.id, (.hugging_face_id // ""), (.benchmarks.artificial_analysis.intelligence_index // "no score")] | @tsv' "$(newest openrouter json)"
```

Alias the record when every id the source ran belongs to one key, and say in
the note what the ids were. When the source ran the name under ids of two
keys, or under an id no models.dev row has, the record does not identify one
key: go to (f).

**(f) Nothing fits.** Four outcomes, in this order:

- **The model is not in models.dev at all and no source scores it:** `[[ignore]]`, with a reason that says what you checked and ends `(checked <date>)`. This is a finding (there is nothing to resolve to), not a way to shorten the list. An embedding model or another model that takes no chat work is ignored the same way.
- **A source scores the model and no models.dev key or row could be it:** declare a key. Write an `[[alias]]` whose `canonical` is a new `vendor/model` and which lists the source id. The model becomes a candidate; both gates are skipped, with a note, because models.dev knows nothing about it. Replace the entry with models.dev's key once it has one.
- **The evidence leaves two readings** (two keys the id could mean, a source that ran one name under two models, a repo that names no origin): write nothing. Leave the row or the record listed, and say in the commit or the PR what you found, so the next person starts from it.
- **The owner knows.** The person who installed or built the model may simply know what it is. That is evidence: write the alias with the note `owner-confirmed <date>` and what they confirmed.

Then check and commit:

```bash
uv run --directory modeltiers modeltiers run --dry-run   # the row has left unmapped: or unresolved:
make -C modeltiers test                                  # the committed file still loads, and every alias has a note
git add modeltiers/src/modeltiers/data/aliases.toml
git commit -m "chore(modeltiers): alias <model> to <key>"
```

`aliases.toml` is checked before wt is run and before any fetch. Two entries
that claim one inventory pair or one source id, an `[[ignore]]` without a
reason, and an unknown key in the file each stop the run with exit 1 and a
message that names the entry.

### 5. Re-calibrate after a sentinel failure

`contracts.toml` pins a handful of sentinel models to the votes and the tier
they had when the floors were set. When one differs, `run` prints every
sentinel, writes nothing and exits 3. The existing `tiers.toml` stays as it
was. There is no flag that overrides this: the fix is an edit to
`modeltiers/src/modeltiers/data/contracts.toml`.

Illustrative example from the test fixtures: the sentinel table of a fixture
run in which every intelligence index was lowered by a fifth.

```text
sentinels: 2 of 4 FAILED
  FAIL  anthropic/claude-opus-5-5
        scores:  intelligence_index 46.08, arena_agent 0.1433, arena_agent_ci_lower 0.1215, eci 167.33
        votes:   intelligence_index standard, arena_agent deep, eci deep; tier before the gates marathon
        pinned:  intelligence_index deep, arena_agent deep, eci deep; tier marathon
        differs: intelligence_index voted standard, pinned deep
  FAIL  anthropic/claude-fable-5
        scores:  intelligence_index 39.68, coding_index 76.5, agentic_index 50.7, arena_agent 0.0894, arena_agent_ci_lower 0.0767, eci 162.06
        votes:   intelligence_index standard, arena_agent deep, eci deep; tier before the gates deep
        pinned:  intelligence_index deep, arena_agent deep, eci deep; tier deep
        differs: intelligence_index voted standard, pinned deep
  ok    zhipuai/glm-5.3
        scores:  intelligence_index 35.84, coding_index 74.8, agentic_index 53.1, arena_agent 0.0215, arena_agent_ci_lower 0.0143, eci 155.61
        votes:   intelligence_index standard, arena_agent standard, eci standard; tier before the gates standard
        pinned:  intelligence_index standard, arena_agent standard, eci standard; tier standard
  ok    minimax/MiniMax-M3
        scores:  arena_agent -0.0695, arena_agent_ci_lower -0.0786, eci 146.95
        votes:   arena_agent quick, eci quick; tier before the gates quick
        pinned:  arena_agent quick, eci quick; tier quick
```

Read the `differs:` lines, then decide which of three things happened:

| What you see | What happened | The edit |
|---|---|---|
| One sentinel differs, on one signal | That model was re-scored by that source | Change that sentinel's pinned vote (and its `tier`, if the tier before the gates moved too) to what the run printed |
| Several sentinels differ on the same signal, all in the same direction | The source's scale moved. Arena's score is relative to its pool and drifts when the pool changes | Move that signal's floors, one floor at a time, with the procedure below |
| `differs: <signal> voted nothing, pinned <tier>` | The model left that source | Remove that signal from the sentinel's `votes`, or replace the sentinel with a model all three sources score |

**Moving a floor.** A floor is the lowest score that votes its tier. Do this
for each floor of the signal that a `differs:` line crosses (the floor between
the vote printed and the vote pinned). `marathon_ci_lower` is a floor on the
Arena lower bound and is moved the same way, as signal `arena_agent_ci_lower`.

Set up two more helpers for it. `ladder` prints a signal's resolved scores
between two bounds, highest first: the best record of every key, which is what
votes. It reads the newest snapshots and fetches nothing. `tierdump` prints
one line per inventory model and per candidate of a `tiers.toml`, with its
tier, sorted, so that two files can be compared with `diff`.

```bash
ladder() { uv run --directory modeltiers python - "$@" <<'PY'
import sys
from datetime import UTC, datetime
from pathlib import Path

from modeltiers import aliases, cli, contracts, inventory, loader, paths, pipeline

signal, low, high, saved = sys.argv[1], float(sys.argv[2]), float(sys.argv[3]), sys.argv[4:]
name = signal.removesuffix("_ci_lower")
field = "score" if name == signal else "ci_lower"
deps = cli.Deps()
sources = loader.load_all(
    deps.specs, cache=paths.cache_dir(), fetcher=deps.fetcher, sleep=deps.sleep,
    now=datetime.now(UTC), offline=True, warn=lambda line: None,
)
rows = inventory.load_inventory(path=Path(saved[0]) if saved else None, run_wt=deps.run_wt)
found = pipeline.resolve_all(rows, sources, aliases.load_aliases())
best = getattr(found, contracts.SIGNAL_SOURCE[name]).best.values()
scored = sorted(((getattr(m.record, field), m.canonical) for m in best), reverse=True)
window = [(score, key) for score, key in scored if score is not None and low <= score <= high]
for (score, key), (below, _) in zip(window, window[1:]):
    print(f"{score:9.4f}  gap below {score - below:7.4f}  midpoint {(score + below) / 2:9.4f}  {key}")
print(f"{window[-1][0]:9.4f}  {window[-1][1]}")
PY
}
tierdump() { uv run --directory modeltiers python -c 'import sys, tomllib
doc = tomllib.load(open(sys.argv[1], "rb"))
for model in doc.get("models", []):
    print("model", model["provider_id"] + "/" + model["model_name"], model["tier"])
for row in doc.get("unmapped", []):
    print("model", row["provider_id"] + "/" + row["model_name"], "unmapped")
for candidate in doc.get("candidates", []):
    print("candidate", candidate["canonical"], candidate["tier"])' "$1" | sort; }
```

1. **Find the window the sentinels allow.** From the `scores:` lines of the failed report, take the signal's new score for every sentinel that pins a vote on it. `<low>` is the highest score among the sentinels pinned *below* the floor's tier; `<high>` is the lowest score among those pinned *at or above* it. Any floor above `<low>` and at or under `<high>` restores every pinned vote across this floor. When no sentinel is pinned on one side, use `-inf` or `inf` for that side. When `<low>` is not under `<high>`, the sentinels changed order: this is not a scale move, so re-pin the votes that differ instead (the first row of the table).
2. **List the resolved scores in the window, with the gaps between them.**

   ```bash
   ladder <signal> <low> <high>                    # reads the newest snapshots; runs wt model list --json
   ladder <signal> <low> <high> <inventory file>   # the same, from a saved inventory (an absolute path)
   ```

   Each line is a score, the gap to the next score down, and that gap's midpoint. The first line is the sentinel at `<high>` and the last is the one at `<low>`. The report rounds scores to four places, so pass `<low>` a little lower and `<high>` a little higher than printed.
3. **Choose the midpoint of the widest gap.** Two gaps of one width: take the one nearer the old floor. Round the midpoint to the precision the file already uses for that signal, and check that the rounded value is still above the lower score of its gap and not above the upper one. Write it as the floor.
4. **Accept the edit only when all of these hold,** on the same downloads. The run that failed wrote nothing, so the table to compare with cannot come from it: the baseline is the `tiers.toml` the last passing run wrote. Write the new rule's table to a scratch file and compare the two, key by key, inventory models and candidates alike (the change report names only the inventory models that moved and gives the candidates as a count):

   ```bash
   BEFORE="${MODELTIERS_OUT:-${XDG_CONFIG_HOME:-$HOME/.config}/local-ai/tiers.toml}"   # the file the last passing run wrote
   AFTER="$(mktemp -d)/tiers.toml"
   MODELTIERS_OUT="$AFTER" uv run --directory modeltiers modeltiers run --offline; echo "exit $?"   # the same downloads, the new rule, a scratch file
   diff <(tierdump "$BEFORE") <(tierdump "$AFTER")
   ```

   - the run prints `sentinels: N of N hold` and `exit 0` (so every pinned vote is back, on every signal). `exit 3` means a sentinel still differs; nothing was written and there is nothing to compare yet;
   - it does not print `exit 1` with `contracts.toml:` on stderr (the floors still rise with the tier);
   - you have read every line `diff` prints. A `<` line is a key's tier in the last passing file and a `>` line is its tier under the new rule; a key with only one of the two joined or left. Each line has to be one you can account for: a model its source re-scored, a model that joined or left a source or your inventory, or a model whose score lies next to the floor you moved. Many keys of one tier moving together means the floor is in the wrong gap. Keys leaving a tier whose floor you did not touch mean the scale moved under that floor too, without carrying a sentinel across it: move that floor the same way;
   - with no file at `$BEFORE` (nothing has passed on this machine yet) there is no baseline: read the whole of `tierdump "$AFTER"` instead;
   - `make -C modeltiers test` passes.

Change `version` at the top of `contracts.toml` to the day's date:
`tiers.toml` records it as `contracts_version`, so a reader can tell which
rule set made a file. Then `modeltiers run`, and commit `contracts.toml`.

The file is validated on load. Floors that do not rise with the tier, a
sentinel that names an unknown tier or signal, or `marathon_ci_lower` on a
number of signals other than one stops the run with exit 1. A sentinel whose
key is in neither models.dev nor `aliases.toml` is also exit 1, not a
sentinel failure.

### 6. Try something without touching the real file

Point both paths somewhere else. With `MODELTIERS_OUT` set, an inventory from
any registry is accepted:

```bash
SCRATCH="$(mktemp -d)"
MODELTIERS_OUT="$SCRATCH/tiers.toml" MODELTIERS_CACHE="$SCRATCH/cache" \
  uv run --directory modeltiers modeltiers run
MODELTIERS_OUT="$SCRATCH/tiers.toml" uv run --directory modeltiers modeltiers show
```

## Gotchas

- **Nothing reads the file yet.** The wt model selector does not show tiers. That is the next phase; this guide fixes only what the file says.
- **A tier is a capability band, not a price or a speed.** Cost is a column in the wt selector already. No keyless source gives measured speed.
- **Two rows of one model can differ.** The context gate uses the context the row's own host serves. Two hosts of one key can therefore end in different tiers.
- **A local row's context is unknown,** so the context gate is skipped for it and the entry says so in a note.
- **Low floors are rough.** Sources often disagree by one tier near a floor, which is what `medium` confidence shows. A two-vote model takes the lower vote, so thin evidence cannot promote a model.
- **A redirected registry stops the write.** wt's inventory follows `WT_REGISTRY`; the output path does not. When the registry wt read is not `registry.toml` beside the output file and `MODELTIERS_OUT` is unset, `run` exits 1 before any fetch, so a scratch inventory can never replace the real `tiers.toml`. `run --dry-run` writes nothing, so it goes on: it prints a `warning:` that the same run without the flag is refused, then its whole report, and its exit code is the one the report gives (0, 2 or 3) where the real run's is 1. This is the only case in which `--dry-run` does not return the real run's code. Set `MODELTIERS_OUT` (step 6).
- **`--dry-run` still writes snapshots.** It skips `tiers.toml` only. Use `MODELTIERS_CACHE` to keep a trial run out of the real cache.
- **`--directory modeltiers` moves the working directory.** A relative `--inventory` or `MODELTIERS_OUT` path resolves from `modeltiers/`. Use absolute paths.
- **An ignore is never re-examined.** `[[ignore]]` is checked before any other step, so an ignored row stays `unrated` after models.dev gains a key for it. When you re-calibrate, re-check each entry whose reason ends `(checked <date>)` with step 4 (d).
- **A row left listed is not forgotten.** A row under `unmapped:` or a record under `unresolved:` that step 4 found no evidence for is printed by every run. It needs nothing until evidence turns up; do not alias or ignore it to make the list empty.
- **An alias for a model you removed is silent.** An `inventory` pair that is no longer in the inventory is not reported. Delete the pair when you notice it; it does no harm.
- **Runs are manual.** The sources' terms for automated fetching have not been read. Read them before scheduling this.
- **Credit the sources** when you quote a score or a tier outside this machine. `show` and `explain` print the lines.

## Going deeper

- Design spec: `docs/superpowers/specs/2026-10-12-model-tiers-design.md`
- Module map, test seams and the data files: `modeltiers/CLAUDE.md`
- The output contract: [`docs/contracts/tiers.sample.toml`](../contracts/tiers.sample.toml)
- Who owns `tiers.toml` and the snapshot cache: [00-config-map](00-config-map.md)
- Source: `modeltiers/src/modeltiers/`
````

- [ ] **Step 4: Add `tiers.toml` and the snapshot directory to the config map**

Four replacements: two rows after line 17, the two sections around line 58 in one block, and the cross-references at lines 233 and 243. The text replaced is on main today. The example is the head and the first `[[models]]` entry of `docs/contracts/tiers.sample.toml`, copied without a change.

In `docs/guides/00-config-map.md`, replace:

```markdown
| `~/.config/local-ai/benchmarks/latest.toml` | `llmbench` | `llmbench` | Latest-run pointers behind `--latest`; the run directories sit beside it |
```

with:

```markdown
| `~/.config/local-ai/benchmarks/latest.toml` | `llmbench` | `llmbench` | Latest-run pointers behind `--latest`; the run directories sit beside it |
| `~/.config/local-ai/tiers.toml` | `modeltiers` (`modeltiers run`) | `modeltiers show` and `explain`; nothing else yet | Advisory capability tier per model, with the evidence ([12-model-tiers](12-model-tiers.md)) |
| `~/.cache/local-ai/modeltiers/` | `modeltiers` (`modeltiers run`) | `modeltiers run` (fallback, and `--offline`) | Snapshots of the four public sources' downloads; safe to delete |
```

In `docs/guides/00-config-map.md`, replace:

````markdown
```

### Left on disk, read by nothing
````

with:

````markdown
```

### `~/.config/local-ai/tiers.toml`

- **Owner:** `modeltiers`. `modeltiers run` replaces the whole file in one step (a temporary file beside it, then a rename); `--dry-run` leaves it alone. Never edit it by hand: the next run overwrites it. What a run decides is changed in the two committed data files, `modeltiers/src/modeltiers/data/aliases.toml` and `contracts.toml` ([12-model-tiers](12-model-tiers.md) steps 4 and 5).
- **Consumers:** `modeltiers show` and `modeltiers explain`. Nothing routes on it: wt does not read it yet.
- **Purpose:** one advisory tier (`marathon`, `deep`, `standard`, `quick` or `unrated`) for every model `wt model list --json` reports apart from the native agent rows, with the votes and scores behind it, plus the candidates: models a source scores that are not in the inventory. See yours with `uv run --directory modeltiers modeltiers show`.
- **Contract for readers:** a model absent from the file, or under `[[unmapped]]`, is `unrated`; a file that cannot be parsed is treated as absent. The format is [`docs/contracts/tiers.sample.toml`](../contracts/tiers.sample.toml).
- **Env override:** `MODELTIERS_OUT` (a file path), else `$XDG_CONFIG_HOME/local-ai/tiers.toml`. It does **not** follow `WT_REGISTRY`: when the registry wt read is not `registry.toml` beside the output file and `MODELTIERS_OUT` is unset, `modeltiers run` refuses and exits 1, so an inventory from a scratch registry cannot replace this file (`run --dry-run` writes nothing, so it warns and goes on).
- `modeltiers` reads the inventory only through `wt model list --json`. It never opens `registry.toml` and never writes it, `config.yaml` or any wt state.

Illustrative example from the test fixtures, not from any machine: the first two lines and the first `[[models]]` entry of [`docs/contracts/tiers.sample.toml`](../contracts/tiers.sample.toml), which the test suite regenerates from `modeltiers/tests/fixtures/`. Your models will differ.

```toml
generated_at = "2026-10-12T15:58:00Z"
contracts_version = "fixture-1"

[[models]]
provider_id = "ollama"
model_name = "glm-5.3:cloud"
registered = true
canonical = "zhipuai/glm-5.3"
resolved_by = "host"
tier = "standard"
code = "S"
confidence = "high"
basis = "measured"
tool_call = true
context = 1048576
notes = []

[models.votes]
intelligence_index = "standard"
arena_agent = "standard"
eci = "standard"

[models.efforts]
arena_agent = "max"

[models.scores]
intelligence_index = 44.8
coding_index = 74.8
agentic_index = 53.1
arena_agent = 0.02147446416824893
arena_agent_ci_lower = 0.014306504009895325
eci = 155.61
```

### `~/.cache/local-ai/modeltiers/`

- **Owner:** `modeltiers run`. Each source has a directory (`modelsdev/`, `openrouter/`, `arena/`, `epoch/`) holding its downloads as `<UTC stamp>.json` or `.zip`, saved before they are parsed. A download that fails to parse is renamed `<UTC stamp>.bad.<ext>` and is never read again. After a successful write each source keeps its four newest good snapshots and its two newest bad ones.
- **Consumers:** `modeltiers run`: the newest good snapshot stands in when a source's download or parse fails (the source is marked `stale`, exit 2), and `--offline` reads nothing else.
- **Purpose:** a cache. Deleting it loses only the fallback; the next run downloads everything again.
- **Env override:** `MODELTIERS_CACHE`, else `$XDG_CACHE_HOME/local-ai/modeltiers/`.

### Left on disk, read by nothing
````

In `docs/guides/00-config-map.md`, replace:

```markdown
- **Files appear on first run of their owner:** `themes.toml` only after the first `wt config theme set <name>`, `rotation*.state` / `usage.jsonl` after the first `wt` launch, `~/.config/local-ai/benchmarks/` after `llmbench run`. Don't create them by hand.
```

with:

```markdown
- **Files appear on first run of their owner:** `themes.toml` only after the first `wt config theme set <name>`, `rotation*.state` / `usage.jsonl` after the first `wt` launch, `~/.config/local-ai/benchmarks/` after `llmbench run`, `tiers.toml` and `~/.cache/local-ai/modeltiers/` after `modeltiers run`. Don't create them by hand.
```

In `docs/guides/00-config-map.md`, replace:

```markdown
- Provider artifacts + llama.cpp re-enable procedure: `/Users/keith/github/ohanaverse/local-ai-setup/docs/reference/provider-artifacts.md`
```

with:

```markdown
- Provider artifacts + llama.cpp re-enable procedure: `/Users/keith/github/ohanaverse/local-ai-setup/docs/reference/provider-artifacts.md`
- Model tiers (`tiers.toml`, its two data files): [12-model-tiers](12-model-tiers.md)
```

- [ ] **Step 5: Run the tests and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_guide_samples.py -q`
Expected: PASS, `3 passed`.

- [ ] **Step 6: Index the guide and the package in the README**

Five replacements for the six places listed under Files: the last block does two of them, a line inserted before line 84 and line 85 changed, because they are neighbours. The text replaced is on main today; steps 1 to 3 did not touch this file.

In `README.md`, replace:

```markdown
Entry point for running local and routed LLMs on macOS. One monorepo, three
```

with:

```markdown
Entry point for running local and routed LLMs on macOS. One monorepo, four
```

In `README.md`, replace:

```markdown
| `llmbench/` | benchmarks (throughput, agent, eval) and the provider isolation they need (`llmbench` CLI) |
```

with:

```markdown
| `llmbench/` | benchmarks (throughput, agent, eval) and the provider isolation they need (`llmbench` CLI) |
| `modeltiers/` | advisory capability tiers for the models wt lists, from public benchmark sources (`modeltiers` CLI) |
```

In `README.md`, replace:

```markdown
| [`11-capability-eval-benchmark.md`](docs/guides/11-capability-eval-benchmark.md) | cross-category capability benchmark (`llmbench eval`) |
```

with:

```markdown
| [`11-capability-eval-benchmark.md`](docs/guides/11-capability-eval-benchmark.md) | cross-category capability benchmark (`llmbench eval`) |
| [`12-model-tiers.md`](docs/guides/12-model-tiers.md) | which models suit which work: advisory tiers (`modeltiers`), aliases, re-calibration |
```

In `README.md`, replace:

```markdown
│   ├── contracts/      # cross-language config-format fixtures (read by wt Go and llmbench Python tests)
```

with:

```markdown
│   ├── contracts/      # cross-language config-format fixtures (read by wt Go and llmbench Python tests) + modeltiers' output sample
```

In `README.md`, replace:

```markdown
├── wt/                 # worktree agent launcher (Go) — has its own CLAUDE.md
├── .github/workflows/ # CI: shell-ci, wt-ci, llmbench-ci
```

with:

```markdown
├── modeltiers/         # advisory capability tiers from public sources (Python/uv) — has its own CLAUDE.md
├── wt/                 # worktree agent launcher (Go) — has its own CLAUDE.md
├── .github/workflows/ # CI: shell-ci, wt-ci, llmbench-ci, modeltiers-ci
```

- [ ] **Step 7: Check the links, and that the guides hold no live state**

`bin/check-links` reads the files git tracks, so the new guide is added to the index first.

Run: `git add docs/guides/12-model-tiers.md && make check-links`
Expected: PASS, ending `ALL LINKS OK`.

Nowhere in either file: a count of the live run, or the name of a model that is in the owner's inventory or the live candidate list and not in the test fixtures.

Run: `! grep -nE '8,?499|(^|[^.0-9])(284|274|167|192|46|42)([^.0-9]|$)|12 matched|ornith|nemotron|nex-agi|mistral-large|gemini|muse-|kimi|gpt-oss|grok|qwen3\.6|Youssofal' docs/guides/12-model-tiers.md docs/guides/00-config-map.md`
Expected: PASS with no output.

Outside the sample blocks of the guide: no fixture model either, so every command and every sentence uses a placeholder.

Run: `! awk 'BEGIN { fence = sprintf("%c%c%c", 96, 96, 96) } index($0, fence) { inside = !inside; next } !inside' docs/guides/12-model-tiers.md | grep -inE 'glm-|deepseek|qwen|medgemma|minimax|gemma|claude-|nomic'`
Expected: PASS with no output.

The same for the lines this task adds to the config map (the file's older sections are not this plan's):

Run: `! git diff -U0 -- docs/guides/00-config-map.md | grep '^+' | awk 'BEGIN { fence = sprintf("%c%c%c", 96, 96, 96) } index($0, fence) { inside = !inside; next } !inside' | grep -inE 'glm-|deepseek|qwen|medgemma|minimax|gemma|claude-|nomic'`
Expected: PASS with no output.

- [ ] **Step 8: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS, with `All checks passed!` and `Success: no issues found in 25 source files`.

- [ ] **Step 9: Commit**

```bash
git add modeltiers/tests/test_guide_samples.py docs/guides/12-model-tiers.md docs/guides/00-config-map.md README.md
git commit -m "docs: guide 12, model tiers, with tiers.toml and its cache in the config map"
```

---

### Task 5: `CLAUDE.md`, root and package

**Files:**
- Modify: `CLAUDE.md` (line 4, the guides list; line 19, the `modeltiers run` Commands entry; line 35, the `modeltiers/` Architecture bullet; a new bullet before line 41, "LaunchAgent plists")
- Replace: `modeltiers/CLAUDE.md`

**Interfaces:**
- Consumes: Tasks 1 to 4; the Calibration Record.
- Produces: nothing code reads.

- [ ] **Step 1: Update the root `CLAUDE.md`**

Four replacements. Each replaced text is part of one long line; what stands before and after it stays. Line 4 is on main today. Lines 19 and 35 were added by step 1 and last changed by step 3. The last replacement adds the spec's "`tiers.toml` note under the config paths" as its own bullet, after the LiteLLM config bullet.

In `CLAUDE.md`, replace:

```markdown
agent benchmarks, MLX quantization, capability eval). Read
```

with:

```markdown
agent benchmarks, MLX quantization, capability eval, model tiers). Read
```

In `CLAUDE.md`, replace:

```markdown
(`MODELTIERS_CACHE` overrides). Nothing routes on a tier yet
```

with:

```markdown
(`MODELTIERS_CACHE` overrides). Nothing routes on a tier yet; see `docs/guides/12-model-tiers.md`
```

In `CLAUDE.md`, replace:

```markdown
are committed in `modeltiers/src/modeltiers/data/aliases.toml`; the tier rule (score floors per signal, context minimums, pinned sentinel models) in `data/contracts.toml` beside it. Writes one file,
```

with:

```markdown
are committed in `modeltiers/src/modeltiers/data/aliases.toml`, each with the evidence it rests on; the tier rule (score floors per signal, context minimums, pinned sentinel models) in `data/contracts.toml` beside it. Both were calibrated against the live sources on 2026-10-12, and editing them is the only way to change what a run decides; an alias needs evidence that identifies the model, and a row with none stays listed. Writes one file,
```

In `CLAUDE.md`, replace:

```markdown
- LaunchAgent plists: `~/Library/LaunchAgents/local.litellm.proxy.plist` (LiteLLM)
```

with:

```markdown
- Tiers file: `~/.config/local-ai/tiers.toml` — written only by `modeltiers run` (`MODELTIERS_OUT` overrides the path), read by `modeltiers show|explain` and by nothing else yet; advisory, and a reader treats a missing or unparseable file as every model `unrated`. The path does not follow `WT_REGISTRY`, so `modeltiers run` refuses (exit 1) an inventory from a redirected registry while `MODELTIERS_OUT` is unset (`--dry-run`, which writes nothing, reports and warns, and returns the code its report gives) — **any ad-hoc `modeltiers run` against a scratch registry, or one that must not touch the real file or `~/.cache/local-ai/modeltiers/`, must set `MODELTIERS_OUT` and `MODELTIERS_CACHE`**
- LaunchAgent plists: `~/Library/LaunchAgents/local.litellm.proxy.plist` (LiteLLM)
```

- [ ] **Step 2: Bring the package's `CLAUDE.md` to its final form**

Changes from step 3's file: the overview says the package is complete and calibrated, names the guide and points at the Calibration Record; a new section, "The two data files", between "Assignment" and "Output file", with the evidence rule; the command table's `--dry-run` sentence is step 3's, unchanged; the "Re-calibrating" bullet points at it; three Testing bullets (the calibration tests, the guide-sample tests, and what the committed-file test asserts) and `tests/test_calibration.py` in the list of files a fixture row moves.

Replace the whole of `modeltiers/CLAUDE.md` with:

`````markdown
# CLAUDE.md

Guidance for Claude Code when working in `modeltiers/`.

## Project overview

`modeltiers` is a Python 3.13 CLI (argparse) that writes an advisory capability tier (`marathon`, `deep`, `standard`, `quick` or `unrated`) for every model wt lists, from four public sources, into `tiers.toml`. Nothing routes on a tier yet. Design: `../docs/superpowers/specs/2026-10-12-model-tiers-design.md`. User guide: `../docs/guides/12-model-tiers.md`. All four steps of the spec's Delivery section are built. The committed `aliases.toml` and `contracts.toml` were calibrated against the live sources and the owner's inventory on 2026-10-12: every sentinel held, and each alias rests on evidence that identifies its model. What that run could not identify was left listed, not guessed; the record is the Calibration Record in `../docs/superpowers/plans/2026-10-12-model-tiers-step4-calibration-docs.md`.

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
| `snapshots.py` | `<cache>/<source>/<UTC stamp>.<ext>`; `mark_bad` renames to `<stamp>.bad.<ext>`; `good_snapshots` newest first; `prune` keeps 4 good and 2 bad; `run` calls it for each source after a successful write, never under `--dry-run` or `--offline` and never after a failed sentinel check. |
| `sources/*.py` | Pure parsers, bytes to records. An unrecognised shape or a count under `min_rows` raises `SourceParseError`. `min_rows` defaults to the spec's floor (models.dev 1,000, OpenRouter 100, Arena 20, Epoch 100). |
| `loader.py` | `load_source`: fetch, save, parse; on a failure fall back to the newest good snapshot (`status = "stale"`, `fallback = True`); with none, `SourceUnavailable`. `load_all` loads the four and names every unavailable source in one error. |
| `tokens.py` | `tokens(name)`: the token function, the same for a name and for a canonical key. `is_floating(key)`: a key ending in `-latest`. |
| `aliases.py` | `load_aliases` / `parse_aliases`: `aliases.toml` to an `Aliases` with four lookups. Refuses, before any fetch, an unknown key, an `[[ignore]]` without a reason and a pair or source id claimed twice. `check_anchored` (needs models.dev) refuses an alias whose canonical is not a models.dev key and lists no source id. `stale_ids` gives a warning per alias source id absent from this run. |
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
- **models.dev** keeps `canonical_model_id` in its own spelling (`minimax/MiniMax-M3`); a host row without one is kept with `canonical = None`.

## Identity

- **Canonical key.** `vendor/model` exactly as models.dev spells `canonical_model_id` (`minimax/MiniMax-M3`). Stored in that spelling, compared in lower case: `Index.keys`, `Index.rows` and `SourceIdentity.best` are keyed by the lower-case key and hold the stored spelling as the value. An alias written in another case reaches the same key.
- **Host rows.** Inventory `provider_id` `ollama` (cloud rows only) is looked up in models.dev's `ollama-cloud` provider with `:cloud` or `-cloud` removed; `openrouter` rows and OpenRouter records in the `openrouter` provider with `:free` or `:batch` removed. A host row with no canonical id does not answer, but stays on `RowIdentity.host_row`: the next step needs its context.
- **Normalization** answers only when exactly one key has the name's token sequence. A `-latest` key is never in that index. A row resolved this way is on the review list; when it also passed a host row with no canonical id it carries the note `host row has no canonical id; matched by name`.
- **Aliases** match the inventory pair or the source's own id exactly. Arena ids are compared with the effort parenthesis removed, in the file and in the records, so one alias covers every effort row.
- **Three states for an inventory row:** resolved (`canonical`, `resolved_by`), ignored (`ignore_reason`), unmapped (neither). A local row has `basis == "inherited"`.
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
- **Re-calibrating** is editing `src/modeltiers/data/contracts.toml`: a floor, a context minimum or a sentinel's pinned values. See "The two data files" below.

## The two data files

Both are package data under `src/modeltiers/data/`, loaded with `importlib.resources` and validated on every run, before wt is run and before any fetch. Editing one is the only way to change what a run decides: no flag and no environment variable overrides either. The procedure, with the commands that gather the evidence, is `../docs/guides/12-model-tiers.md` steps 4 and 5.

**`aliases.toml`** answers identity by hand.

- **`[[alias]]`** sends an inventory pair, or a source's own id, to a canonical key. **`[[ignore]]`** records a finding that a row or a record has nothing to resolve to. A row in `[[unmapped]]` or a record in `[[unresolved]]` is one nobody has found identifying evidence for yet; it is `unrated`, or a missing vote, and it is listed on every run.
- **Every `[[alias]]` has a `note` that states its evidence and the date it was read.** `tests/test_data_files.py` fails on an alias without one. Evidence identifies the model: an id that the publisher, the distributor or the benchmark source itself gives, leading to exactly one key. The kinds in the file today: the `remote_model` in an ollama cloud tag's registry config; two ollama tags with one manifest digest; the ollama library's own readme; a Hugging Face `base_model` whose relation to the repo is `quantized`, or the origin a publisher's manifest names with a statement that the repo is a conversion of it (a fine-tune, a merge and an adapter do not inherit the base model's key); the versioned model ids Epoch ran a display name under (`model_version` in its `processed_data_for_eci.csv`). A matching name, a matching release date and a matching architecture or parameter count are not evidence, alone or together.
- **No evidence, no entry.** A row or a record that cannot be supported that way gets no alias, and no ignore either: it stays listed. An `[[ignore]]` is for a finding (models.dev has no key for the model and no source scores it; an embedding model), with a `reason` that says what was checked and when. The owner's own knowledge of a model is evidence; such a note reads `owner-confirmed <date>`. Never alias on the name alone: normalization already does that, and its matches are on the review list because a name can be wrong.
- **A declared key.** An alias whose `canonical` is not a models.dev key must list a source id (`check_anchored`). The model becomes a candidate with both gates skipped. It is for a model a source scores and models.dev does not have yet; replace it with models.dev's key when one appears.
- **What goes stale without a message.** An alias source id absent from a run is a warning. An inventory pair that has left the inventory is not reported. An `[[ignore]]` is checked before every other step, so an ignored row stays `unrated` after models.dev gains a key for it: the entries whose reason ends `(checked <date>)` are the ones to look at again.
- **The review list is not emptied.** Rows resolved by normalization are printed on every run. Checking one changes nothing in the file unless the match is wrong, in which case it gets an `[[alias]]`.

**`contracts.toml`** is the tier rule: a code and a context minimum per tier, three floors per signal, the one `marathon_ci_lower`, an attribution line per source and the sentinels.

- **A failed sentinel is fixed here and nowhere else.** `run` prints each sentinel's scores, votes and pinned values and exits 3. One sentinel off on one signal: the model was re-scored, so change its pinned vote. Several off on the same signal in the same direction: the source's scale moved (Arena's score is relative to its pool), so move that signal's floors, each to the midpoint of the widest gap among the resolved scores between the sentinels on either side (guide step 5, "Moving a floor", has the helper and the acceptance checks). A signal that `voted nothing`: the model left that source, so drop the signal from the sentinel's `votes` or pick another sentinel.
- **Change `version` with every edit.** `tiers.toml` records it as `contracts_version`.
- **Check an edit against the same downloads:** `modeltiers run --dry-run --offline` re-reads the newest snapshots with the new rule. Its change report names every inventory model whose tier differs from the existing file and only counts the candidates; a run that fails its sentinels writes nothing, so the file to compare with is the one the last passing run wrote. The full comparison, per key, models and candidates, is guide step 5's `tierdump` of both files through `diff`.

## Output file

`docs/contracts/tiers.sample.toml` is the contract: it is what `run` writes from the test fixtures, and two tests compare against it apart from timestamps. Regenerate it after a deliberate format change with `MODELTIERS_OUT` and `MODELTIERS_CACHE` pointed at scratch paths; never hand-edit it.

- Top level: `generated_at`, `contracts_version`, `[inventory]`, `[sources.<name>]`, then `[[models]]`, `[[candidates]]`, `[[unmapped]]`, `[[unresolved]]`. An array with no entries is absent.
- Left out when unknown: `reason` (rated entries), `tool_call` and `open_weights` (a key with no models.dev rows), `context`, any score a source did not give, `canonical` and `resolved_by` (an ignored row), `unresolved` under `[sources.modelsdev]`.
- Scores are written as the source gave them; `show` and `explain` round to four places for display only.
- **Contract for readers:** a model absent from the file, or in `[[unmapped]]`, is `unrated`; a file that cannot be parsed is treated as absent. `run` applies the same rule to the existing file when it builds the change report.

## Testing patterns

- **Nothing live.** The autouse `_hermetic` fixture in `tests/conftest.py` makes `urllib.request.urlopen` and `subprocess.run` raise and points `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `MODELTIERS_OUT` and `MODELTIERS_CACHE` under `tmp_path`. `tests/test_conftest_guards.py` fails if that stops holding. CI's `bin/check-config-dirs-untouched` covers the config home only; nothing but this fixture protects the real cache.
- **Seams are parameters.** The network is a `Fetcher`, the wait is a `Sleep`, the clock is a `now`, wt is a `RunWt`, the two data files are `Deps.aliases_path` and `Deps.contracts_path`. Tests pass `fakes.FakeFetcher` and `fakes.Sleeps` (`tests/fakes.py`), or a whole `cli.Deps`.
- **Small fixtures.** `tests/fixtures/` holds a few rows per source, so tests pass small minimums: the `specs` fixture is `loader.build_specs(modelsdev_min=12, openrouter_min=6, arena_min=6, epoch_min=6)`. The Epoch fixtures are CSV text; the `epoch_zip` fixture zips one in memory. The Arena fixtures are single page bodies; `arena_doc` wraps them as `fetch_arena` would.
- **Fixture-local data files.** Tests pass `tests/fixtures/aliases.toml` and `tests/fixtures/contracts.toml` (as `Deps.aliases_path` / `Deps.contracts_path`, or through the `fixture_aliases` and `rule` fixtures), or build an `Aliases` with `aliases.build`. Only `tests/test_data_files.py` reads the committed files under `src/modeltiers/data/`. It asserts that they load, that every alias has a note and nothing about which entries or values they hold, so calibration changes no test.
- **Shared pipeline fixtures** in `tests/conftest.py`: `inventory`, `sources`, `fixture_aliases`, `rule`, `assignment` (the golden fixtures resolved and assigned) and `document` (`tiers.toml` for them, as a dict, generated at `T0`). `SAMPLE` and `without_timestamps` are there for the two tests that compare against `docs/contracts/tiers.sample.toml`.
- **The calibration moves are pinned over the fixtures** by `tests/test_calibration.py`, which runs the CLI with `tests/fixtures/aliases_calibrated.toml` (the fixture `aliases.toml` plus an ignore for the unmapped row, a declared key for the unresolved record and an alias for a pair the inventory does not have), with a copy of `aliases.toml` that aliases the unmapped row instead (the change report then says `newly resolved: <pair> <tier>`), and with a re-pinned copy of `tests/fixtures/contracts.toml`. No other test reads that aliases file.
- **The guide's samples are pinned too.** `tests/test_guide_samples.py` fails when a ```` ```text ```` block in `../docs/guides/12-model-tiers.md` is not a word-for-word slice of what the fixtures make `run` and `explain` print, when the `tiers.toml` example in `../docs/guides/00-config-map.md` is not a slice of `docs/contracts/tiers.sample.toml`, or when a command block in the guide names a fixture model. A change to a golden fixture, or to a line `run` prints, therefore needs the guide's sample updated in the same commit. Both guides are among `modeltiers-ci`'s path filters, so an edit to either one alone runs this suite too.
- **Adding a row to a golden fixture** moves the counts pinned in `tests/test_source_*.py`, `tests/test_loader.py`, `tests/test_pipeline.py`, `tests/test_cli.py`, `tests/test_calibration.py`, the `specs` minimums, `docs/contracts/tiers.sample.toml`, and the samples in the two guides that `tests/test_guide_samples.py` checks.
- **Identity tests build their own models.dev.** `tests/test_identity.py` and `tests/test_identity_sources.py` use hand-built `ModelsDev` rows; `tests/test_pipeline.py` and `tests/test_cli.py` use the golden fixtures.
- `tests/fixtures/README.md` credits each source. Keep it current when a fixture is added.
`````


The spec is not edited in this task or anywhere in this plan. The owner corrects it on the docs branch; it already carries the corrected "run over the 2026-10-12 downloads" list, which the Calibration Record sets out line by line.

- [ ] **Step 3: Check the links**

Run: `make check-links`
Expected: PASS, ending `ALL LINKS OK`.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md modeltiers/CLAUDE.md
git commit -m "docs(modeltiers): the calibrated data files, the guide and the tiers file in both CLAUDE.md"
```

---

### Task 6: Full verification

**Files:** none.

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS, with this output:

```
uv run ruff check src/ tests/
All checks passed!
uv run ruff format --check src/ tests/
54 files already formatted
uv run mypy src/
Success: no issues found in 25 source files
```

- [ ] **Step 2: The suite, with the coverage floor**

Run: `make -C modeltiers test`
Expected: PASS. The last lines are:

```
TOTAL                                   1644      0   100%
Required test coverage of 90.0% reached. Total coverage: 100.00%
============================= 497 passed in 0.95s ==============================
```

The time varies; the counts do not. `TOTAL` is step 3's: no source line was added. The ten tests added here are the seven of `tests/test_calibration.py` and the three of `tests/test_guide_samples.py`.

- [ ] **Step 3: The repo's lint**

Run: `make lint`
Expected: PASS, ending `ALL LINKS OK`.

- [ ] **Step 4: The work left the tree clean**

Run: `git status --short`
Expected: PASS with no output. If Task 3 Step 6 has not been run, `~/.config/local-ai/tiers.toml` and `~/.cache/local-ai/modeltiers` do not exist either; no automated step of this plan creates them.

**Done when**

- On the owner's machine a live `modeltiers run` reports `unmapped: 0`, every sentinel holds, the records under `unresolved:` are exactly the ones the Calibration Record lists as open, and the run exits 0 and writes `~/.config/local-ai/tiers.toml` (Delivery item 4; success criterion 1; Task 3 Steps 1 and 6).
- Every `[[alias]]` in the committed `aliases.toml` has a note stating evidence that identifies the model and was read; no alias rests on a name, a date or an architecture; every `[[ignore]]` states a finding; and every row or record with neither is listed as open with the entry that would close it (spec, Testing, "Live"; Task 2, the Calibration Record, `test_the_committed_aliases_file_loads_and_validates`).
- Every piece of evidence in the Calibration Record has a command in Task 3 Step 2 with its answer of 2026-10-12.
- Every row on the review list has been checked and the check is recorded (spec, Testing, "Live"; the Calibration Record).
- The sentinel table and the spec's "run over the 2026-10-12 downloads" list are checked against the final run, line by line. The spec already carries the corrected list, and no step of this plan edits the spec (spec, Testing, "Live"; the Calibration Record).
- An ignored row is `unrated` with reason `not_applicable` and absent from `[[unmapped]]`; an aliased row leaves `[[unmapped]]` with its key's tier and the change report says so (spec, Identity, `aliases.toml`; `tests/test_calibration.py`).
- A sentinel that moved blocks the write until `contracts.toml` is edited, and the edit is the whole fix (success criterion 5; spec, Sentinels; `test_editing_the_pinned_votes_is_what_clears_a_moved_sentinel`). Moving a floor has a written procedure with an acceptance check whose baseline is the last passing file, never the failing run, and which compares every key, candidates included (guide step 5; Task 3 Step 3).
- No test touches the network, the real config home or the real cache (success criterion 7), and the tool wrote nothing but snapshots and the output file (success criterion 8).
- The spec's Docs list is complete: `modeltiers/CLAUDE.md` (commands, pipeline, the two data files, the test seams); `modeltiers/tests/fixtures/README.md`; the root `CLAUDE.md` Commands line, Architecture bullet, package-level context list and `tiers.toml` note; `docs/guides/12-model-tiers.md` (what a tier means, how to run and read it, how to add an alias or an ignore, how to re-calibrate after a sentinel failure, samples that are fixture output and say so, placeholders in every command, no live model state); `docs/guides/00-config-map.md` (`tiers.toml` and the snapshot directory, each with its owner).
- `make -C modeltiers check`, `make -C modeltiers test` and `make lint` pass.

**After this plan**

- **The next phase is the wt model selector reading `tiers.toml`** (spec, Out of scope). The contract it reads is `docs/contracts/tiers.sample.toml`: a model absent from the file or under `[[unmapped]]` is `unrated`, and a file that cannot be parsed is treated as absent.
- **Three records are open** and wait for the owner's word or for a source to change: Epoch's `Gemini 3.1 Pro`, `Muse Spark` and `Grok 4.20` (the Calibration Record, "Left open, for the owner").
- **Re-calibration is routine.** Whenever a run stops at exit 3, or lists a new row under `unmapped:` or `unresolved:`, the procedure is `docs/guides/12-model-tiers.md` steps 4 and 5, and Task 3 Step 3's table is the same rules with this calibration's entries named.
- **The entries most likely to need attention first:** the declared key `google/gemini-4-argon` (until models.dev has a key), the ignores whose reason ends `(checked 2026-10-12)`, and the three sentinels with thin margins named in the Calibration Record.
- **Before any scheduled run:** read the four sources' terms for automated fetching (spec, Risks). Runs are manual until then.
