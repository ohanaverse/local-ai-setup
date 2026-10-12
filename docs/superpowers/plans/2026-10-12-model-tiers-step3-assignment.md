# modeltiers Step 3: Assignment, Output and Commands Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `modeltiers run` gives every resolved model a tier from the three voting signals, checks the sentinels, prints what changed and writes `tiers.toml` atomically; `modeltiers show` and `modeltiers explain <model>` read that file back.

**Architecture:** `contracts.py` loads the tier rule (`contracts.toml`: floors, context minimums, attribution lines, sentinels). `assign.py` is pure functions over one key: `weigh` (votes, lower-median base tier, marathon promotion), `check_sentinel`, `gather` (the context table) and `decide` (the two gates). `pipeline.assign_all` runs them over step 2's `Identity` and returns one `Assignment`; `output.py` turns it into the document and writes it; `changes.py` and `show.py` render from documents alone, so `show` and `explain` need nothing but the file; `cli.py` orders the stages and owns the exit codes.

**Tech Stack:** Python `==3.13.*`, uv, hatchling, src layout. Runtime dependency `tomli-w` (declared in step 1, first imported here; `uv.lock` does not change). Standard library otherwise: `tomllib`, `tempfile`, `argparse`, `dataclasses`, `importlib.resources`. Dev: pytest, pytest-cov, ruff, mypy (`strict = true` on `src/`).

**Spec:** docs/superpowers/specs/2026-10-12-model-tiers-design.md

**Verified against:** main at 51887c0, and a working prototype of this step. Every code block below was extracted from this file into a clone that holds the seventeen commits of steps 1 and 2 and run in task order: each "Expected: FAIL" failed with the quoted message and each "Expected: PASS" passed, and the resulting tree equals the prototype file for file. The prototype was also run over the 2026-10-12 downloads and the owner's saved `wt model list --json` output with the committed `contracts.toml` and `aliases.toml`: all eight sentinels held; the three `marathon` keys were Claude Opus 5.5, Claude Fable 5.1 and GPT-6 Astra; the inventory split 14 `standard`, 17 `quick`, 5 `unrated`, 10 unmapped, none `deep` or `marathon`; the tool-calling gate moved two candidates from `quick` to `unrated` and the context gate moved nobody; no model's votes spanned two tiers. All of that is what the spec reports. One difference from the spec's first list: seven keys came out `deep` where that list named four (the run adds GPT-6 Sol, GPT-5.6 Sol and GPT-5.5 Pro). The spec has since been corrected and names all seven, plus Gemini 4 Argon, which is `deep` only once step 4 declares its key; "For step 4", at the end of this plan, has the detail.

**Depends on:** docs/superpowers/plans/2026-10-12-model-tiers-step2-identity.md, merged (and step 1's plan before it).

## Global Constraints

- Python is `==3.13.*`, uv-managed, src layout, console script `modeltiers = "modeltiers.cli:main"`.
- The only runtime dependency is `tomli-w`. The CLI uses `argparse`. Dev dependencies are pytest, pytest-cov, ruff and mypy. No dependency is added in this step.
- The tool never writes `registry.toml`, `config.yaml` or any wt state, and never reads `registry.toml`. It writes two things: snapshot files under the cache directory, and the one output file.
- wt is run only as `wt model list --json`. `show` and `explain` read only the output file: they never fetch and never run wt.
- The join key for an inventory row is the pair (`provider_id`, `model_name`), never the registry `id`. The change report is keyed on that pair.
- No test touches the network, the real config home or the real cache. Step 1's autouse `_hermetic` fixture stays as it is.
- **Output path:** `MODELTIERS_OUT`, else `$XDG_CONFIG_HOME/local-ai/tiers.toml`, else `~/.config/local-ai/tiers.toml`. Written atomically: a temporary file in the same directory, then a rename.
- **Redirected registry:** when the registry path `wt model list --json` reports is not `registry.toml` in the output file's directory and `MODELTIERS_OUT` is unset, `run` refuses to write and exits 1, before any fetch. `run --dry-run` writes nothing and is not refused: it warns that the same run without the flag would be, and goes on to print its report.
- **Tiers and codes:** `marathon` `M` (rank 4), `deep` `D` (3), `standard` `S` (2), `quick` `Q` (1), `unrated` `-` (0).
- **Assignment order,** for every resolved key: (1) votes; (2) base tier, the element at index `(n-1)//2` of the votes sorted lowest first; (3) marathon promotion, base tier `deep` and the Arena record's `score_ci_lower` at or above the marathon floor, no other route; (4) sentinel check on the votes and the pre-gate tier; (5) tool-calling gate; (6) context gate; (7) final tier.
- **Signals:** `intelligence_index` (OpenRouter), `arena_agent` (Arena), `eci` (Epoch), each with `max_vote = "deep"`. `coding_index` and `agentic_index` are written to `scores` and do not vote. A score below a signal's quick floor is a vote of `unrated`; a signal with no record abstains.
- **Starting floors** (quick / standard / deep): `intelligence_index` 10.6 / 33.0 / 49.0; `arena_agent` -0.090 / -0.029 / 0.056, marathon 0.097 on `score_ci_lower`; `eci` 137.1 / 150.7 / 160.6.
- **Context minimums:** `quick` 32,000; `standard` 128,000; `deep` 200,000; `marathon` 400,000.
- **`unrated` reasons:** `no_votes`, `below_floor`, `gate_tool_call`, `gate_context`, `not_applicable`. An `unrated` entry always carries one; a rated entry never does.
- **Confidence,** from the votes alone, unchanged by promotion or gates: `high` two or more votes all equal; `medium` two or more whose ranks span one; `low` one vote, or ranks spanning two or more; `none` no votes.
- **Sentinels:** a sentinel fails when any pinned vote differs or is missing, or when its pre-gate tier differs. On failure the run prints every sentinel's scores, votes and pinned values, writes nothing and exits 3. There is no override. A sentinel whose `canonical` is not a known key is a configuration error (exit 1).
- **The eight committed sentinels:** `anthropic/claude-fable-5-1` and `anthropic/claude-opus-5-5` (deep, deep, deep; `marathon`); `anthropic/claude-fable-5` and `anthropic/claude-opus-5` (deep ×3; `deep`); `anthropic/claude-sonnet-5` and `zhipuai/glm-5.2` (standard ×3; `standard`); `minimax/MiniMax-M3` (quick ×3; `quick`); `google/gemma-4-31b-it` (`intelligence_index` quick, `eci` quick, Arena not pinned; `quick`).
- **Attribution strings:** OpenRouter's is `Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter (openrouter.ai/rankings).`; Epoch's is `Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0`; Arena's is `Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0`. `run`, `show` and `explain` print the lines for the sources whose data they display.
- **Exit codes:** 0 written, every source `ok` or `stale` because `--offline` was given; 1 not written, configuration, inventory or source error; 2 written, with a source that fell back to a snapshot; 3 not written, blocked by sentinels. 1 takes precedence over 3 and 3 over 2. `--dry-run` writes no output file and returns the code the run would have returned; the one exception is a redirected registry, where the run without the flag is refused (1) and the dry run returns the code its report gives (0, 2 or 3).
- **Pruning:** after a successful write each source keeps its four newest good snapshots and its two newest bad ones. `--offline` and `--dry-run` prune nothing.
- **`show`:** prints the file's age in days, with a warning line past 14 days, and the attribution lines. **`explain`:** the argument is tried as an exact `provider_id/model_name` first, then as a canonical key without regard to case; no match prints the file's generated date and exits 1.
- Tests use `tests/fixtures/contracts.toml` and `tests/fixtures/aliases.toml`. One test file, `tests/test_data_files.py`, loads the committed data files.
- Quality bar: `make check` (ruff lint, `ruff format --check`, mypy) and `make test` (pytest, coverage floor 90).
- Guides never embed live model state. This step adds no guide. The committed `contracts.toml` is calibration data; `docs/contracts/tiers.sample.toml` is generated from the test fixtures and says nothing about the owner's inventory.
- **Not in this step:** the live calibration run and the aliases, ignores, floor or sentinel corrections it calls for; `docs/guides/12-model-tiers.md`; the `tiers.toml` and snapshot-directory rows of `docs/guides/00-config-map.md` (all step 4). The wt selector does not read the file (out of scope for the spec).
- **Chosen where the spec is silent** (each is pinned by a test in the task named):
  - **`contracts.toml` is strict** (Task 1). An unknown key anywhere is an error. A signal's `source` must be the one this plan fixes for it. `max_vote` is `quick`, `standard` or `deep`. Floors must rise strictly. `context_min` must not fall as the tier rises. A code is one character, not `-`, and not shared. A sentinel's `tier` is one of the four rated tiers; a pinned vote is `unrated`, `quick`, `standard` or `deep`; one key listed twice, in any case, is an error. A file with no `[[sentinels]]` is valid.
  - **models.dev's attribution line** is `models.dev (https://models.dev), MIT` (Task 1). The spec gives the other three.
  - **A vote of `unrated`** is written in `votes` as `"unrated"` and counts, at rank 0, in the median and in the confidence (Task 2).
  - **Only the marathon signal's lower bound is written** to `scores`, as `arena_agent_ci_lower`. Epoch's interval is read and not written, as in the spec's example (Task 2).
  - **`no_votes` and `below_floor` entries** skip both gates, and still carry `tool_call`, `context` and the note that says where the context came from (Task 3).
  - **The context table's rows can overlap.** A key with no models.dev rows skips both gates even when the inventory row has a host row. A host row with no `limit.context` counts as no host row. When no row of the key gives a context, the gate is skipped with a note (Task 3).
  - **`tool_call` and `open_weights`** are true when any models.dev row of the key says so (Tasks 3 and 4).
  - **A candidate's `resolved_by`** is the earliest of `alias`, `host`, `normalized` among its records in the three sources (Task 4).
  - **`[[unresolved]]`** lists one entry per source id, its best-scoring record. `[sources.<name>].unresolved` counts every unresolved record of that source, listed or not; `[sources.modelsdev]` has no such key (Tasks 4 and 5).
  - **An ignored row** is written without `canonical` and `resolved_by`, with the note `ignored: <reason>` (Tasks 4 and 5).
  - **Scores are written as the source gave them,** unrounded. `show`, `explain` and the report round to four places for display (Tasks 5 and 6).
  - **Layout.** Every table inside an entry is a sub-table (`[models.votes]`), where the spec's example writes two of them inline; the TOML is the same. An array with no entries is absent from the file. `tomli_w` formats the values; `output.render` lays out the blocks, because `tomli_w` writes a short table in an array inline (Task 5).
  - **The output file's mode** is what `tempfile.mkstemp` gives, `0600` (Task 5).
  - **The existing file is advisory to `run` too.** One that is not TOML, or not UTF-8, gives a warning, is treated as absent and is replaced. One written by another version is compared by the fields it has, with `?` for those it lacks (Tasks 7 and 9).
  - **The registry check** runs right after the inventory is read, before any fetch. A plain `run` stops there. `--dry-run` "does everything except write", and with nothing to write there is nothing to refuse: it gets one `warning:` on stderr at that point, naming both registries and saying that the run without `--dry-run` is refused and exits 1, then goes on; its change report compares against the default output file, and its exit code is not changed by the warning. Directories are compared resolved; the registry file itself is not, because it may be a symlink (Tasks 8 and 9).
  - **What the two list headings say.** `unmapped: N` and `unresolved: N at or above a standard floor` each end `(each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)`. The spec's "Live" paragraph leaves a row with no identifying evidence listed and recorded as open, so the tool does not tell its reader that every listed row needs an entry. Step 2 printed the `unmapped:` line without the last clause; Task 9 adds it (`test_an_empty_inventory_still_prints_every_section`, `test_unresolved_records_at_a_floor_are_named_and_the_rest_counted`, `test_run_writes_the_contract_sample_and_reports`).
  - **After a failed sentinel check** the change report is not printed and nothing is pruned (Task 9).
  - **`show`'s past-14-days warning** goes to stderr as `warning: …`, like every other warning; the age itself is on stdout. A file with no readable `generated_at` has an unknown age and no warning (Tasks 6 and 10).
  - **`explain` on an unmapped row** exits 0. The exact-pair lookup is case-sensitive; only the key lookup ignores case. The no-match message goes to stderr (Tasks 6 and 10).
  - **The golden fixtures gain one model,** Claude Fable 5, in all four sources, because the spec's sentinel test names it, **and one OpenRouter record,** `qwen/qwen3.8-27b` (intelligence index 33.7, a `standard` vote), which resolves by name to `alibaba/qwen3.8-27b`, the key the local inventory row `ollama/qwen3.8:27b-mlx` already resolves to. That row is then rated through inheritance, in the pipeline tests, a CLI test and the contract sample (Task 4).
  - **A row that left `[[unmapped]]` because an `[[ignore]]` now covers it** is reported as `now ignored: <pair> (<reason>)`. It was not resolved, so it is not `newly resolved`. The reason is read from the entry's `ignored: <reason>` note, since the change report sees only the two documents (Task 7).
  - **The timestamp format,** `STAMP`, lives in `contracts.py` beside the other names `tiers.toml` is written with, so that `show.py` and `changes.py` import nothing of the package but constants (Tasks 1 and 6).
- Commits use the repo's conventional style (`feat(modeltiers): …`, lowercase after the type, no trailing period), with the attribution trailer your session is told to add, if any.
- Work on one feature branch off `main`, for example `feat/modeltiers-step3-assignment`. **Pushing the branch and opening the pull request need the owner's OK.**
- **Never, in any step of this plan:** read or print `~/.config/local-ai/registry.toml` (it holds a secret); write under `~/.config` or `~/.cache`; run a wt command other than `wt model list --json`. From this step on a bare `modeltiers run` writes the real `tiers.toml`: the one optional live command (Task 12) sets `MODELTIERS_OUT` and `MODELTIERS_CACHE` to scratch paths.
- Commands are run from the repo root unless a step says otherwise.

## Review Focus

1. **A source that drifted under a sentinel whose tier did not move.** Every intelligence index scaled by 0.8 takes `anthropic/claude-fable-5` from 49.6 to 39.68: its votes become standard, deep, deep and the lower median is still `deep`. Expected: the run prints every sentinel's scores, votes and pinned values, leaves an existing `tiers.toml` byte for byte as it was, prunes nothing and exits 3; `--dry-run` returns 3; with a source that also fell back it is still 3; with a configuration error in the same run, or a source that has no data at all, it is 1. Pinned in Task 2 (`test_a_drifted_vote_fails_the_sentinel_though_the_tier_holds`), Task 4 (`test_scaled_intelligence_indices_fail_a_sentinel_whose_tier_holds`) and Task 9 (`test_a_failed_sentinel_writes_nothing_and_exits_3`, `test_dry_run_returns_3_for_a_failed_sentinel`, `test_3_takes_precedence_over_2`, `test_1_takes_precedence_over_3`, `test_a_source_with_no_data_is_1_whatever_the_sentinels_would_say`).
2. **An existing `tiers.toml` this version did not write:** one from an older version (no `generated_at`, no `[sources]`, entries without `tier` or `confidence`), or one cut short so it is not TOML. Expected: the older file is compared by the fields it has, with `?` for the rest, and is replaced; the broken file gives one `warning:`, is treated as absent (the full table is printed) and is replaced; `show` and `explain` on the broken file exit 1 naming `modeltiers run`. Pinned in Task 5 (`test_a_file_that_is_not_toml_reads_as_absent_for_the_change_report`), Task 6 (`test_an_entry_from_an_older_file_prints_question_marks`), Task 7 (`test_a_file_from_an_older_version_is_compared_by_the_fields_it_has`), Task 9 (`test_an_existing_file_from_an_older_version_is_compared_and_replaced`, `test_an_existing_file_that_is_not_toml_is_treated_as_absent_and_replaced`) and Task 10 (`test_show_and_explain_with_a_file_that_is_not_toml_exit_1`, one case for each command).
3. **An inventory that came from another registry** (`WT_REGISTRY` pointed at a scratch file, or a saved `--inventory` from another machine) while `MODELTIERS_OUT` is unset. Expected: `run` exits 1 before any download, with nothing printed on stdout and nothing created in the default directory; `run --dry-run` is not refused: it prints the whole report, a `warning:` on stderr that the same run without `--dry-run` is refused, creates nothing and exits with the code the report gives; a config directory that is a symlink still counts as the same place; with `MODELTIERS_OUT` set the run goes ahead. Pinned in Task 8 (`test_the_registry_beside_the_output_file_is_recognised`, `test_a_symlinked_config_directory_still_matches`, `test_out_is_overridden_only_by_a_non_empty_variable`) and Task 9 (`test_a_redirected_registry_is_refused_before_any_fetch`, `test_dry_run_with_a_redirected_registry_reports_and_warns`, `test_the_registry_beside_the_default_output_file_is_accepted`).
4. **A canonical key in mixed case.** models.dev spells `minimax/MiniMax-M3`; the fixture `contracts.toml` pins the sentinel as `minimax/minimax-m3`; a person types `modeltiers explain ZHIPUAI/GLM-5.3`. Expected: one key each time; the sentinel is known and holds, and its table line shows models.dev's spelling; `explain` finds every model that resolved to the key, and a candidate by its key in any case; the same key in two cases is one sentinel "listed twice" and one candidate in the change report. Pinned in Task 1 (the `listed twice` row of `test_an_invalid_file_is_a_config_error_naming_the_entry`), Task 4 (`test_every_fixture_sentinel_holds`), Task 6 (`test_explain_by_canonical_key_prints_every_model_that_resolved_to_it`, `test_explain_a_candidate_by_its_key_in_any_case`), Task 7 (`test_candidate_changes_are_counts`) and Task 10 (`test_explain_a_model_by_pair_and_by_key`).
5. **An entry models.dev says little or nothing about:** a key with no models.dev rows (declared by an alias), a local build, a cloud row whose host serves less context than the key's largest row, an `[[ignore]]`d row, a row with `limit.context` missing, and a score exactly on a floor. Expected: a gate with nothing to read is skipped and says so in a note; `tool_call`, `open_weights` and `context` are absent from the file, never `false` or `0`; the cloud row's own host context is the one used, and it can end two inventory rows of one key in two tiers; a key none of whose rows calls tools is `unrated` / `gate_tool_call`, and a host under the quick minimum `unrated` / `gate_context`, through `assign_all` and not only in `decide`; the ignored row is in `[[models]]` as `unrated` / `not_applicable` and not in `[[unmapped]]`, and when the existing file listed it as unmapped the change report says `now ignored`, not `newly resolved`; a score equal to a floor reaches it, and a context equal to a minimum meets it. Pinned in Task 2 (`test_a_score_votes_the_highest_tier_whose_floor_it_reaches`), Task 3 (`test_a_key_with_no_models_dev_rows_skips_both_gates`, `test_a_local_rows_context_is_unknown`, `test_a_cloud_row_uses_its_host_rows_context`, `test_rows_that_give_no_context_leave_it_unknown`, `test_the_context_gate_drops_to_the_highest_tier_the_context_meets`), Task 4 (`test_context_comes_from_the_rows_own_host`, `test_a_cloud_rows_own_host_context_decides_its_final_tier`, `test_a_key_no_row_says_calls_tools_is_unrated_gate_tool_call`, `test_a_host_under_the_quick_minimum_is_unrated_gate_context`, `test_an_ignored_row_is_unrated_not_applicable`, `test_a_sentinel_may_name_an_alias_declared_key`), Task 5 (`test_optional_fields_are_absent_when_unknown`, `test_a_key_with_no_models_dev_rows_has_no_tool_call_or_open_weights`, `test_an_ignored_row_is_in_models_as_not_applicable`) and Task 7 (`test_a_row_an_ignore_now_covers_is_now_ignored_not_newly_resolved`).
6. **A local build of a key a source scored.** The inventory row `ollama/qwen3.8:27b-mlx` is local; nothing scores it under that name, and OpenRouter scores `qwen/qwen3.8-27b`, which reaches the same key. Expected: the row is rated, with the key's votes, scores, tier and tool-calling flag and `basis = "inherited"`; its evidence equals what the key gets as a candidate when the row is absent; its context is unknown, so the context gate is skipped with a note; when no row of the key calls tools the local row is `unrated` / `gate_tool_call` too; `docs/contracts/tiers.sample.toml` holds the entry, and every other inherited entry there is `unrated`. Pinned in Task 4 (`test_a_local_row_is_rated_through_inheritance`, `test_a_key_no_row_says_calls_tools_is_unrated_gate_tool_call`), Task 5 (`test_a_local_build_is_written_with_its_keys_tier_votes_and_tool_call`), Task 6 (`test_explain_a_local_build_rated_through_inheritance`) and Task 10 (`test_explain_a_local_build_shows_the_tier_it_inherited`).

## File Structure

Created:

| File | Responsibility |
|---|---|
| `modeltiers/src/modeltiers/contracts.py` | `Tier`, `Signal`, `Sentinel`, `Contracts`; `parse_contracts`, `load_contracts`; the tier, signal and source name constants, and `STAMP` |
| `modeltiers/src/modeltiers/data/contracts.toml` | The committed tier rule: the spec's starting floors, context minimums, attribution lines and eight sentinels. Package data |
| `modeltiers/src/modeltiers/assign.py` | `vote`, `base_tier`, `confidence`, `weigh`; `check_sentinel`; `gather`, `decide` |
| `modeltiers/src/modeltiers/output.py` | `build_document`, `render`, `write_document`, `read_document`, `read_previous` |
| `modeltiers/src/modeltiers/show.py` | What `show` and `explain` print, from a document: `table_lines`, `age_line`, `stale_warning`, `attribution_lines`, `explain_lines` |
| `modeltiers/src/modeltiers/changes.py` | `change_lines`: the change report, from two documents |
| `docs/contracts/tiers.sample.toml` | The output contract: what `run` writes from the golden fixtures |
| `modeltiers/tests/fixtures/contracts.toml` | The fixture-local tier rule: the same floors, four sentinels the fixtures satisfy |
| `modeltiers/tests/test_contracts.py` | Loading and every validation error |
| `modeltiers/tests/test_assign.py` | Floors, lower median, promotion, confidence, sentinels, the context table, both gates, each reason |
| `modeltiers/tests/test_output.py` | The document against the sample, optional fields, the atomic write, reading back |
| `modeltiers/tests/test_show.py` | The table, the age, attribution, `explain` lookups |
| `modeltiers/tests/test_changes.py` | The change report, including an older file and a row that is now ignored |

Modified:

| File | Change |
|---|---|
| `modeltiers/src/modeltiers/pipeline.py` | `ModelResult`, `Candidate`, `Assignment`, `assign_all` added below `resolve_all` |
| `modeltiers/src/modeltiers/errors.py` | `OutputFileError` |
| `modeltiers/src/modeltiers/paths.py` | `out_is_overridden`, `registry_is_beside_output` |
| `modeltiers/src/modeltiers/report.py` | `identity_lines` loses its `unresolved:` section and its `unmapped:` line says a row may be left open; `unresolved_lines` and `sentinel_lines` added |
| `modeltiers/src/modeltiers/cli.py` | `Deps.contracts_path`; `run` assigns, checks, reports, writes and prunes; `show` and `explain`; `EXIT_SENTINEL` |
| `modeltiers/tests/conftest.py` | `rule`, `inventory`, `sources`, `fixture_aliases`, `assignment`, `document`; `T0`, `SAMPLE`, `without_timestamps`; the `specs` minimums |
| `modeltiers/tests/fixtures/modelsdev.json`, `openrouter.json`, `arena_page1.json`, `arena_page2.json`, `eci_scores.csv` | One more model, Claude Fable 5; `openrouter.json` also gains `qwen/qwen3.8-27b` |
| `modeltiers/tests/test_source_modelsdev.py`, `test_source_openrouter.py`, `test_source_arena.py`, `test_source_epoch.py`, `test_loader.py` | The counts the grown fixtures pin |
| `modeltiers/tests/test_pipeline.py` | Its three fixtures move to `conftest.py`; sixteen `assign_all` tests |
| `modeltiers/tests/test_data_files.py` | The committed `contracts.toml` loads and validates |
| `modeltiers/tests/test_paths.py` | The registry check |
| `modeltiers/tests/test_report.py` | The two new sections |
| `modeltiers/tests/test_cli.py` | Rewritten: plain `run` writes; sentinels, precedence, the registry refusal and the dry run it does not stop, the change report, pruning, `show`, `explain` |
| `modeltiers/tests/fixtures/README.md` | A row for `contracts.toml`; the sample file; "eleven" becomes "twelve"; the Arena row count |
| `modeltiers/CLAUDE.md` | The package is at step 3 |
| `CLAUDE.md` (lines 19 and 34, added by steps 1 and 2; line 37, the `docs/` bullet) | What `run` does now and its four exit codes; `show` and `explain`; `contracts.toml`, `tiers.toml` and the sample |

Not modified: `.github/workflows/modeltiers-ci.yml` (step 1 already lists `docs/contracts/tiers.sample.toml` among its paths), `modeltiers/pyproject.toml`, `modeltiers/uv.lock`, the root `Makefile`.

---

### Task 1: `contracts.toml`, loaded and validated

**Files:**
- Create: `modeltiers/src/modeltiers/contracts.py`, `modeltiers/src/modeltiers/data/contracts.toml`
- Modify: `modeltiers/tests/conftest.py`, `modeltiers/tests/test_data_files.py`
- Test: `modeltiers/tests/test_contracts.py`, `modeltiers/tests/fixtures/contracts.toml`

**Interfaces:**
- Consumes: `modeltiers.errors.ConfigError` (step 1, Task 1).
- Produces, in `modeltiers.contracts`:
  - `UNRATED = "unrated"`, `UNRATED_CODE = "-"`, `TIERS = ("quick", "standard", "deep", "marathon")`, `RANK: Mapping[str, int]` (`unrated` 0 to `marathon` 4), `FLOOR_TIERS = ("quick", "standard", "deep")`, `SIGNALS = ("intelligence_index", "arena_agent", "eci")`, `SIGNAL_SOURCE: Mapping[str, str]`, `SOURCES = ("modelsdev", "openrouter", "arena", "epoch")`, `STAMP = "%Y-%m-%dT%H:%M:%SZ"` (timestamps as written to `tiers.toml` and as printed; first used in Task 5).
  - `Tier(name: str, code: str, context_min: int)`, frozen.
  - `Signal(name: str, source: str, max_vote: str, floors: Mapping[str, float], marathon_ci_lower: float | None = None)`, frozen. `floors` has the keys `quick`, `standard`, `deep`.
  - `Sentinel(canonical: str, tier: str, votes: Mapping[str, str])`, frozen. `canonical` is as written in the file.
  - `Contracts(version: str, tiers: Mapping[str, Tier], signals: Mapping[str, Signal], attribution: Mapping[str, str], sentinels: tuple[Sentinel, ...], origin: str = "contracts.toml")`, frozen, with `code(tier: str) -> str` (`-` for `unrated`) and the property `marathon -> Signal` (the one signal that carries `marathon_ci_lower`). `tiers` is in `TIERS` order, `signals` in `SIGNALS` order, `attribution` in `SOURCES` order.
  - `parse_contracts(text: str, *, origin: str = "contracts.toml") -> Contracts`. Raises `ConfigError`.
  - `load_contracts(path: Path | None = None) -> Contracts`. `None` reads the committed `data/contracts.toml` through `importlib.resources`. Raises `ConfigError`.
  - The `rule` fixture in `tests/conftest.py`: the fixture-local `Contracts`.

- [ ] **Step 1: Write the fixture file and the failing tests**

The fixture file has the spec's starting floors and context minimums, so the fixture scores land where the real ones did, and four sentinels the golden fixtures satisfy. `anthropic/claude-fable-5` gets its fixture rows in Task 4. The fourth sentinel is written `minimax/minimax-m3` on purpose: models.dev spells that key `minimax/MiniMax-M3`.

Write `modeltiers/tests/fixtures/contracts.toml`:

```toml
version = "fixture-1"

[tiers.quick]
code = "Q"
context_min = 32000

[tiers.standard]
code = "S"
context_min = 128000

[tiers.deep]
code = "D"
context_min = 200000

[tiers.marathon]
code = "M"
context_min = 400000

[signals.intelligence_index]
source = "openrouter"
max_vote = "deep"
floors = { quick = 10.6, standard = 33.0, deep = 49.0 }

[signals.arena_agent]
source = "arena"
max_vote = "deep"
floors = { quick = -0.090, standard = -0.029, deep = 0.056 }
marathon_ci_lower = 0.097

[signals.eci]
source = "epoch"
max_vote = "deep"
floors = { quick = 137.1, standard = 150.7, deep = 160.6 }

[sources.modelsdev]
attribution = "models.dev (https://models.dev), MIT"

[sources.openrouter]
attribution = "Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter (openrouter.ai/rankings)."

[sources.arena]
attribution = "Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0"

[sources.epoch]
attribution = "Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0"

[[sentinels]]
canonical = "anthropic/claude-opus-5-5"
tier = "marathon"
votes = { intelligence_index = "deep", arena_agent = "deep", eci = "deep" }

[[sentinels]]
canonical = "anthropic/claude-fable-5"
tier = "deep"
votes = { intelligence_index = "deep", arena_agent = "deep", eci = "deep" }

[[sentinels]]
canonical = "zhipuai/glm-5.3"
tier = "standard"
votes = { intelligence_index = "standard", arena_agent = "standard", eci = "standard" }

[[sentinels]]
canonical = "minimax/minimax-m3"
tier = "quick"
votes = { arena_agent = "quick", eci = "quick" }
```

Each row of the parametrised test replaces one exact piece of that file and names the message the loader must give.

Write `modeltiers/tests/test_contracts.py`:

```python
from pathlib import Path

import pytest

from modeltiers import contracts
from modeltiers.errors import ConfigError

FIXTURE = Path(__file__).parent / "fixtures" / "contracts.toml"
TEXT = FIXTURE.read_text()


def _broken(old: str, new: str) -> str:
    """The fixture text with one exact piece replaced."""
    assert TEXT.count(old) == 1, old
    return TEXT.replace(old, new)


def test_the_fixture_loads() -> None:
    loaded = contracts.load_contracts(FIXTURE)
    assert loaded.version == "fixture-1"
    assert loaded.origin == "contracts.toml"
    assert list(loaded.tiers) == ["quick", "standard", "deep", "marathon"]
    assert [(t.code, t.context_min) for t in loaded.tiers.values()] == [
        ("Q", 32000),
        ("S", 128000),
        ("D", 200000),
        ("M", 400000),
    ]
    assert list(loaded.signals) == ["intelligence_index", "arena_agent", "eci"]
    arena = loaded.signals["arena_agent"]
    assert (arena.source, arena.max_vote, arena.marathon_ci_lower) == ("arena", "deep", 0.097)
    assert arena.floors == {"quick": -0.090, "standard": -0.029, "deep": 0.056}
    assert loaded.signals["eci"].marathon_ci_lower is None
    assert loaded.marathon is arena
    assert list(loaded.attribution) == ["modelsdev", "openrouter", "arena", "epoch"]
    assert loaded.attribution["arena"] == (
        "Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0"
    )
    assert [(s.canonical, s.tier) for s in loaded.sentinels] == [
        ("anthropic/claude-opus-5-5", "marathon"),
        ("anthropic/claude-fable-5", "deep"),
        ("zhipuai/glm-5.3", "standard"),
        ("minimax/minimax-m3", "quick"),
    ]
    # A signal a sentinel leaves out is not pinned.
    assert loaded.sentinels[3].votes == {"arena_agent": "quick", "eci": "quick"}


def test_codes_and_the_unrated_dash() -> None:
    loaded = contracts.load_contracts(FIXTURE)
    assert [loaded.code(t) for t in ("marathon", "deep", "standard", "quick", "unrated")] == [
        "M",
        "D",
        "S",
        "Q",
        "-",
    ]
    assert contracts.RANK["unrated"] < contracts.RANK["quick"] < contracts.RANK["marathon"]


def test_integer_floors_are_read_as_numbers() -> None:
    text = _broken(
        "floors = { quick = 137.1, standard = 150.7, deep = 160.6 }",
        "floors = { quick = 137, standard = 150.7, deep = 161 }",
    )
    assert contracts.parse_contracts(text).signals["eci"].floors == {
        "quick": 137.0,
        "standard": 150.7,
        "deep": 161.0,
    }


def test_no_sentinels_is_valid() -> None:
    text = TEXT[: TEXT.index("[[sentinels]]")]
    assert contracts.parse_contracts(text).sentinels == ()


@pytest.mark.parametrize(
    ("old", "new", "message"),
    [
        # Floors must rise with the tier.
        (
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0 }",
            "floors = { quick = 10.6, standard = 49.0, deep = 33.0 }",
            r"contracts.toml: \[signals.intelligence_index\]: floors must rise with the tier",
        ),
        (
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0 }",
            "floors = { quick = 10.6, standard = 33.0 }",
            r"\[signals.intelligence_index\]: floors.deep is missing or not a number",
        ),
        (
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0 }",
            "floors = { quick = true, standard = 33.0, deep = 49.0 }",
            r"\[signals.intelligence_index\]: floors.quick is missing or not a number",
        ),
        (
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0 }",
            "floors = { quick = 10.6, standard = 33.0, deep = 49.0, marathon = 60.0 }",
            r"\[signals.intelligence_index\] floors: unknown key 'marathon'",
        ),
        # Exactly one signal carries marathon_ci_lower.
        (
            "marathon_ci_lower = 0.097\n",
            "",
            "exactly one signal must carry marathon_ci_lower; found none",
        ),
        (
            'source = "epoch"\n',
            'source = "epoch"\nmarathon_ci_lower = 165.0\n',
            "exactly one signal must carry marathon_ci_lower; found arena_agent, eci",
        ),
        (
            "marathon_ci_lower = 0.097",
            'marathon_ci_lower = "0.097"',
            r"\[signals.arena_agent\]: marathon_ci_lower is missing or not a number",
        ),
        (
            'source = "arena"',
            'source = "epoch"',
            r"\[signals.arena_agent\]: source must be 'arena', not 'epoch'",
        ),
        (
            'source = "arena"\nmax_vote = "deep"',
            'source = "arena"\nmax_vote = "marathon"',
            r"\[signals.arena_agent\]: max_vote 'marathon' is not one of quick, standard, deep",
        ),
        ("[signals.eci]", "[signals.ecii]", r"contracts.toml: no \[signals.eci\] table"),
        (
            "[signals.eci]",
            '[signals.speed]\nsource = "x"\n\n[signals.eci]',
            "contracts.toml: unknown signals entry 'speed'",
        ),
        (
            'source = "arena"\n',
            'source = "arena"\nweight = 2\n',
            r"\[signals.arena_agent\]: unknown key 'weight'",
        ),
        # Tiers.
        ("[tiers.deep]", "[tiers.deeper]", r"contracts.toml: no \[tiers.deep\] table"),
        (
            'code = "D"',
            'code = "S"',
            r"\[tiers.deep\]: code 'S' is used by another tier",
        ),
        (
            'code = "D"',
            'code = "-"',
            r"\[tiers.deep\]: code must be one character other than '-'",
        ),
        (
            "context_min = 200000",
            "context_min = 100000",
            r"\[tiers.deep\]: context_min must not fall as the tier rises",
        ),
        (
            "context_min = 200000",
            'context_min = "200k"',
            r"\[tiers.deep\]: context_min is missing or not a whole number",
        ),
        # Sources.
        (
            "[sources.epoch]",
            "[sources.epoc]",
            r"contracts.toml: no \[sources.epoch\] table",
        ),
        (
            'attribution = "models.dev (https://models.dev), MIT"',
            'attribution = ""',
            r"\[sources.modelsdev\]: attribution is missing, empty or not a string",
        ),
        # Sentinels name a known tier and known signals.
        (
            'canonical = "anthropic/claude-fable-5"\ntier = "deep"',
            'canonical = "anthropic/claude-fable-5"\ntier = "deeper"',
            r"\[\[sentinels\]\] #2 \(anthropic/claude-fable-5\): tier 'deeper' is not one of",
        ),
        (
            'votes = { arena_agent = "quick", eci = "quick" }',
            'votes = { arena = "quick", eci = "quick" }',
            r"\[\[sentinels\]\] #4 \(minimax/minimax-m3\): votes names an unknown signal 'arena'",
        ),
        (
            'votes = { arena_agent = "quick", eci = "quick" }',
            'votes = { arena_agent = "marathon", eci = "quick" }',
            r"#4 \(minimax/minimax-m3\): votes.arena_agent 'marathon' is not one of unrated, quick",
        ),
        (
            'canonical = "minimax/minimax-m3"',
            'canonical = "Anthropic/Claude-Opus-5-5"',
            r"\[\[sentinels\]\] #4 \(Anthropic/Claude-Opus-5-5\): listed twice",
        ),
        (
            'canonical = "minimax/minimax-m3"\n',
            "",
            r"\[\[sentinels\]\] #4: canonical is missing, empty or not a string",
        ),
        (
            'votes = { arena_agent = "quick", eci = "quick" }',
            "",
            r"\[\[sentinels\]\] #4 \(minimax/minimax-m3\) votes: missing or not a table",
        ),
        # The file as a whole.
        ('version = "fixture-1"', "", "contracts.toml: version is missing, empty or not a string"),
        ('version = "fixture-1"', 'version = "fixture-1"\nfloors = 1', "unknown key 'floors'"),
        ('version = "fixture-1"', "version = ", "contracts.toml: not TOML"),
    ],
)
def test_an_invalid_file_is_a_config_error_naming_the_entry(
    old: str, new: str, message: str
) -> None:
    with pytest.raises(ConfigError, match=message):
        contracts.parse_contracts(_broken(old, new))


def test_a_single_sentinels_table_is_refused() -> None:
    text = TEXT[: TEXT.index("[[sentinels]]")] + '[sentinels]\ncanonical = "a/b"\n'
    with pytest.raises(ConfigError, match=r"sentinels is not an array of tables; write \[\["):
        contracts.parse_contracts(text)


def test_a_file_with_no_tiers_table_is_refused() -> None:
    with pytest.raises(ConfigError, match=r"other.toml: \[tiers\]: missing or not a table"):
        contracts.parse_contracts('version = "1"\n', origin="other.toml")


def test_a_missing_file_is_a_config_error(tmp_path: Path) -> None:
    with pytest.raises(ConfigError, match="contracts: cannot read .*absent.toml"):
        contracts.load_contracts(tmp_path / "absent.toml")
```

The `rule` fixture gives every later test the fixture-local contracts.

In `modeltiers/tests/conftest.py`, replace:

```python
from modeltiers import fetch, loader
```

with:

```python
from modeltiers import contracts as contracts_mod
from modeltiers import fetch, loader
from modeltiers.contracts import Contracts
```

Append to the end of `modeltiers/tests/conftest.py`, after two blank lines:

```python
@pytest.fixture
def rule() -> Contracts:
    """The fixture-local contracts.toml, never the committed one."""
    return contracts_mod.load_contracts(FIXTURES / "contracts.toml")
```

The committed file gets its one test where step 2 put the test for `aliases.toml`. It asserts nothing about floors or about which sentinels the file holds, so step 4 can re-calibrate without touching it.

In `modeltiers/tests/test_data_files.py`, replace:

```python
from modeltiers import aliases
```

with:

```python
from modeltiers import aliases, contracts
```

Append to the end of `modeltiers/tests/test_data_files.py`, after two blank lines:

```python
def test_the_committed_contracts_file_loads_and_validates() -> None:
    loaded = contracts.load_contracts()
    assert loaded.origin == "contracts.toml"
    assert loaded.version
    assert set(loaded.tiers) == set(contracts.TIERS)
    assert set(loaded.signals) == set(contracts.SIGNALS)
    assert set(loaded.attribution) == set(contracts.SOURCES)
    assert loaded.sentinels
    assert all("/" in sentinel.canonical for sentinel in loaded.sentinels)
```

- [ ] **Step 2: Run the tests and see them fail**

Run: `uv run --directory modeltiers pytest tests/test_contracts.py tests/test_data_files.py -q`
Expected: FAIL while loading `tests/conftest.py`, with `ImportError: cannot import name 'contracts' from 'modeltiers'`.

- [ ] **Step 3: Write the loader**

Every message starts with the file's name and names the table: `contracts.toml: [signals.eci]: floors must rise with the tier: quick < standard < deep`, `contracts.toml: [[sentinels]] #2 (anthropic/claude-fable-5): tier 'deeper' is not one of …`. Whether a sentinel's key exists needs models.dev, so that check is in Task 4.

Write `modeltiers/src/modeltiers/contracts.py`:

```python
"""`contracts.toml`: the tier rule as data.

The floors each signal votes by, the context each tier needs, one attribution
line per source, and the sentinel models whose votes and tier are pinned.
"""

import tomllib
from collections.abc import Mapping
from dataclasses import dataclass
from importlib import resources
from pathlib import Path
from typing import Any

from modeltiers.errors import ConfigError

UNRATED = "unrated"
UNRATED_CODE = "-"
TIERS = ("quick", "standard", "deep", "marathon")  # the rated tiers, lowest first
RANK: Mapping[str, int] = {UNRATED: 0, "quick": 1, "standard": 2, "deep": 3, "marathon": 4}
FLOOR_TIERS = ("quick", "standard", "deep")  # the tiers a signal can vote
SIGNALS = ("intelligence_index", "arena_agent", "eci")
SIGNAL_SOURCE: Mapping[str, str] = {
    "intelligence_index": "openrouter",
    "arena_agent": "arena",
    "eci": "epoch",
}
SOURCES = ("modelsdev", "openrouter", "arena", "epoch")
STAMP = "%Y-%m-%dT%H:%M:%SZ"  # timestamps as written to tiers.toml and as printed

_TOP_KEYS = frozenset({"version", "tiers", "signals", "sources", "sentinels"})
_TIER_KEYS = frozenset({"code", "context_min"})
_SIGNAL_KEYS = frozenset({"source", "max_vote", "floors", "marathon_ci_lower"})
_SOURCE_KEYS = frozenset({"attribution"})
_SENTINEL_KEYS = frozenset({"canonical", "tier", "votes"})


@dataclass(frozen=True)
class Tier:
    name: str
    code: str  # one character, for the wt selector
    context_min: int


@dataclass(frozen=True)
class Signal:
    name: str
    source: str  # "openrouter" | "arena" | "epoch"
    max_vote: str  # no vote of this signal is higher
    floors: Mapping[str, float]  # quick, standard, deep
    marathon_ci_lower: float | None = None  # set on exactly one signal


@dataclass(frozen=True)
class Sentinel:
    canonical: str  # as written in the file
    tier: str  # the pinned pre-gate tier
    votes: Mapping[str, str]  # signal -> pinned vote; a signal left out is not pinned


@dataclass(frozen=True)
class Contracts:
    version: str
    tiers: Mapping[str, Tier]  # the four rated tiers, lowest first
    signals: Mapping[str, Signal]  # in the order of SIGNALS
    attribution: Mapping[str, str]  # source -> its line, in the order of SOURCES
    sentinels: tuple[Sentinel, ...]
    origin: str = "contracts.toml"  # the file's name, for messages

    def code(self, tier: str) -> str:
        """The one-character code of a tier; `-` for `unrated`."""
        return UNRATED_CODE if tier == UNRATED else self.tiers[tier].code

    @property
    def marathon(self) -> Signal:
        """The one signal whose record's lower bound can promote to marathon."""
        return next(s for s in self.signals.values() if s.marathon_ci_lower is not None)


def _table(where: str, value: Any, allowed: frozenset[str] | None = None) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ConfigError(f"{where}: missing or not a table")
    if allowed is not None:
        unknown = sorted(set(value) - allowed)
        if unknown:
            raise ConfigError(f"{where}: unknown key {unknown[0]!r}")
    return value


def _exactly(where: str, table: Mapping[str, Any], names: tuple[str, ...], kind: str) -> None:
    """The table has one entry for each of `names` and nothing else."""
    for name in names:
        if name not in table:
            raise ConfigError(f"{where}: no [{kind}.{name}] table")
    unknown = sorted(set(table) - set(names))
    if unknown:
        raise ConfigError(f"{where}: unknown {kind} entry {unknown[0]!r}")


def _text(where: str, entry: Mapping[str, Any], key: str) -> str:
    value = entry.get(key)
    if not isinstance(value, str) or not value.strip():
        raise ConfigError(f"{where}: {key} is missing, empty or not a string")
    return value


def _number(where: str, value: Any) -> float:
    if isinstance(value, bool) or not isinstance(value, int | float):
        raise ConfigError(f"{where} is missing or not a number")
    return float(value)


def _tiers(origin: str, doc: Mapping[str, Any]) -> dict[str, Tier]:
    table = _table(f"{origin}: [tiers]", doc.get("tiers"))
    _exactly(origin, table, TIERS, "tiers")
    tiers: dict[str, Tier] = {}
    for name in TIERS:
        where = f"{origin}: [tiers.{name}]"
        entry = _table(where, table[name], _TIER_KEYS)
        code = _text(where, entry, "code")
        context_min = entry.get("context_min")
        if len(code) != 1 or code == UNRATED_CODE:
            raise ConfigError(f"{where}: code must be one character other than {UNRATED_CODE!r}")
        if code in {t.code for t in tiers.values()}:
            raise ConfigError(f"{where}: code {code!r} is used by another tier")
        if isinstance(context_min, bool) or not isinstance(context_min, int) or context_min < 0:
            raise ConfigError(f"{where}: context_min is missing or not a whole number")
        if tiers and context_min < max(t.context_min for t in tiers.values()):
            raise ConfigError(f"{where}: context_min must not fall as the tier rises")
        tiers[name] = Tier(name=name, code=code, context_min=context_min)
    return tiers


def _signal(where: str, name: str, value: Any) -> Signal:
    entry = _table(where, value, _SIGNAL_KEYS)
    source = _text(where, entry, "source")
    if source != SIGNAL_SOURCE[name]:
        raise ConfigError(f"{where}: source must be {SIGNAL_SOURCE[name]!r}, not {source!r}")
    max_vote = _text(where, entry, "max_vote")
    if max_vote not in FLOOR_TIERS:
        raise ConfigError(f"{where}: max_vote {max_vote!r} is not one of {', '.join(FLOOR_TIERS)}")
    table = _table(f"{where} floors", entry.get("floors"), frozenset(FLOOR_TIERS))
    floors = {tier: _number(f"{where}: floors.{tier}", table.get(tier)) for tier in FLOOR_TIERS}
    if not floors["quick"] < floors["standard"] < floors["deep"]:
        raise ConfigError(f"{where}: floors must rise with the tier: quick < standard < deep")
    marathon = entry.get("marathon_ci_lower")
    return Signal(
        name=name,
        source=source,
        max_vote=max_vote,
        floors=floors,
        marathon_ci_lower=(
            None if marathon is None else _number(f"{where}: marathon_ci_lower", marathon)
        ),
    )


def _signals(origin: str, doc: Mapping[str, Any]) -> dict[str, Signal]:
    table = _table(f"{origin}: [signals]", doc.get("signals"))
    _exactly(origin, table, SIGNALS, "signals")
    signals = {name: _signal(f"{origin}: [signals.{name}]", name, table[name]) for name in SIGNALS}
    carriers = [s.name for s in signals.values() if s.marathon_ci_lower is not None]
    if len(carriers) != 1:
        found = ", ".join(carriers) or "none"
        raise ConfigError(
            f"{origin}: exactly one signal must carry marathon_ci_lower; found {found}"
        )
    return signals


def _attribution(origin: str, doc: Mapping[str, Any]) -> dict[str, str]:
    table = _table(f"{origin}: [sources]", doc.get("sources"))
    _exactly(origin, table, SOURCES, "sources")
    lines: dict[str, str] = {}
    for name in SOURCES:
        where = f"{origin}: [sources.{name}]"
        lines[name] = _text(where, _table(where, table[name], _SOURCE_KEYS), "attribution")
    return lines


def _sentinel(where: str, value: dict[str, Any]) -> Sentinel:
    entry = _table(where, value, _SENTINEL_KEYS)
    canonical = _text(where, entry, "canonical")
    where = f"{where} ({canonical})"
    tier = _text(where, entry, "tier")
    if tier not in TIERS:
        raise ConfigError(f"{where}: tier {tier!r} is not one of {', '.join(TIERS)}")
    votes = _table(f"{where} votes", entry.get("votes"))
    for signal, vote in votes.items():
        if signal not in SIGNALS:
            raise ConfigError(f"{where}: votes names an unknown signal {signal!r}")
        if vote != UNRATED and vote not in FLOOR_TIERS:
            raise ConfigError(
                f"{where}: votes.{signal} {vote!r} is not one of "
                f"{', '.join((UNRATED, *FLOOR_TIERS))}"
            )
    return Sentinel(canonical=canonical, tier=tier, votes=dict(votes))


def _sentinels(origin: str, doc: Mapping[str, Any]) -> tuple[Sentinel, ...]:
    value = doc.get("sentinels", [])
    if not isinstance(value, list) or not all(isinstance(v, dict) for v in value):
        raise ConfigError(f"{origin}: sentinels is not an array of tables; write [[sentinels]]")
    sentinels: list[Sentinel] = []
    seen: set[str] = set()
    for index, entry in enumerate(value, start=1):
        sentinel = _sentinel(f"{origin}: [[sentinels]] #{index}", entry)
        if sentinel.canonical.lower() in seen:
            raise ConfigError(
                f"{origin}: [[sentinels]] #{index} ({sentinel.canonical}): listed twice"
            )
        seen.add(sentinel.canonical.lower())
        sentinels.append(sentinel)
    return tuple(sentinels)


def parse_contracts(text: str, *, origin: str = "contracts.toml") -> Contracts:
    """Read and validate the file's text. `origin` names the file in every
    message. Whether each sentinel names a known key needs models.dev and is
    checked later, by `pipeline.assign_all`."""
    try:
        doc = tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        raise ConfigError(f"{origin}: not TOML: {exc}") from exc
    unknown = sorted(set(doc) - _TOP_KEYS)
    if unknown:
        raise ConfigError(f"{origin}: unknown key {unknown[0]!r}")
    return Contracts(
        version=_text(origin, doc, "version"),
        tiers=_tiers(origin, doc),
        signals=_signals(origin, doc),
        attribution=_attribution(origin, doc),
        sentinels=_sentinels(origin, doc),
        origin=origin,
    )


def load_contracts(path: Path | None = None) -> Contracts:
    """Read `path`, or the committed `data/contracts.toml` when it is None."""
    if path is None:
        text = resources.files("modeltiers").joinpath("data/contracts.toml").read_text("utf-8")
        return parse_contracts(text, origin="contracts.toml")
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ConfigError(f"contracts: cannot read {path}: {exc.strerror}") from exc
    return parse_contracts(text, origin=path.name)
```

- [ ] **Step 4: Run the loader's tests and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_contracts.py -q`
Expected: PASS, `35 passed`.

- [ ] **Step 5: Write the committed data file**

The spec's starting floors, context minimums, attribution strings and all eight sentinels. hatchling puts every file under `src/modeltiers/` in the wheel, so `pyproject.toml` does not change.

Write `modeltiers/src/modeltiers/data/contracts.toml`:

```toml
# The tier rule as data. Read by modeltiers.contracts; validated on every run.
# Floors and sentinels were set from the 2026-10-12 snapshots. Editing this
# file is the re-calibration: there is no flag that overrides a sentinel.
#
# [tiers.<tier>]     code = one character; context_min = the context the tier needs
# [signals.<signal>] floors = the lowest score that votes each tier; a score under
#                    floors.quick votes unrated; no vote is above max_vote
#                    marathon_ci_lower = on one signal only: a deep model whose
#                    record's lower bound reaches it becomes marathon
# [sources.<source>] attribution = the line printed with that source's data
# [[sentinels]]      canonical, tier (before the gates), votes = the pinned ones

version = "2026-10-12"

[tiers.quick]
code = "Q"
context_min = 32000

[tiers.standard]
code = "S"
context_min = 128000

[tiers.deep]
code = "D"
context_min = 200000

[tiers.marathon]
code = "M"
context_min = 400000

[signals.intelligence_index]
source = "openrouter"
max_vote = "deep"
floors = { quick = 10.6, standard = 33.0, deep = 49.0 }

[signals.arena_agent]
source = "arena"
max_vote = "deep"
floors = { quick = -0.090, standard = -0.029, deep = 0.056 }
marathon_ci_lower = 0.097

[signals.eci]
source = "epoch"
max_vote = "deep"
floors = { quick = 137.1, standard = 150.7, deep = 160.6 }

[sources.modelsdev]
attribution = "models.dev (https://models.dev), MIT"

[sources.openrouter]
attribution = "Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter (openrouter.ai/rankings)."

[sources.arena]
attribution = "Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0"

[sources.epoch]
attribution = "Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0"

[[sentinels]]
canonical = "anthropic/claude-fable-5-1"
tier = "marathon"
votes = { intelligence_index = "deep", arena_agent = "deep", eci = "deep" }

[[sentinels]]
canonical = "anthropic/claude-opus-5-5"
tier = "marathon"
votes = { intelligence_index = "deep", arena_agent = "deep", eci = "deep" }

[[sentinels]]
canonical = "anthropic/claude-fable-5"
tier = "deep"
votes = { intelligence_index = "deep", arena_agent = "deep", eci = "deep" }

[[sentinels]]
canonical = "anthropic/claude-opus-5"
tier = "deep"
votes = { intelligence_index = "deep", arena_agent = "deep", eci = "deep" }

[[sentinels]]
canonical = "anthropic/claude-sonnet-5"
tier = "standard"
votes = { intelligence_index = "standard", arena_agent = "standard", eci = "standard" }

[[sentinels]]
canonical = "zhipuai/glm-5.2"
tier = "standard"
votes = { intelligence_index = "standard", arena_agent = "standard", eci = "standard" }

[[sentinels]]
canonical = "minimax/MiniMax-M3"
tier = "quick"
votes = { intelligence_index = "quick", arena_agent = "quick", eci = "quick" }

[[sentinels]]
canonical = "google/gemma-4-31b-it"
tier = "quick"
votes = { intelligence_index = "quick", eci = "quick" }
```

- [ ] **Step 6: Run both test files and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_contracts.py tests/test_data_files.py -q`
Expected: PASS, `37 passed`.

- [ ] **Step 7: Commit**

```bash
git add modeltiers/src/modeltiers/contracts.py modeltiers/src/modeltiers/data/contracts.toml modeltiers/tests/test_contracts.py modeltiers/tests/test_data_files.py modeltiers/tests/conftest.py modeltiers/tests/fixtures/contracts.toml
git commit -m "feat(modeltiers): load contracts.toml and refuse floors, signals and sentinels that do not fit"
```

---

### Task 2: Votes, base tier, marathon promotion, confidence and the sentinel check

**Files:**
- Create: `modeltiers/src/modeltiers/assign.py`
- Test: `modeltiers/tests/test_assign.py`

**Interfaces:**
- Consumes: `modeltiers.contracts.FLOOR_TIERS`, `RANK`, `UNRATED`, `Contracts`, `Sentinel`, `Signal` (Task 1); `modeltiers.records.ScoreRecord` (step 1, Task 2); the `rule` fixture (Task 1).
- Produces, in `modeltiers.assign`:
  - `NO_VOTES = "no_votes"`, `BELOW_FLOOR = "below_floor"`, `GATE_TOOL_CALL = "gate_tool_call"`, `GATE_CONTEXT = "gate_context"`, `NOT_APPLICABLE = "not_applicable"`, and the four note texts `NOTE_LARGEST`, `NOTE_LOCAL`, `NOTE_NO_ROWS`, `NOTE_NO_CONTEXT` (used from Task 3 on).
  - `vote(signal: Signal, score: float) -> str`.
  - `base_tier(votes: Iterable[str]) -> str`. `unrated` for no votes.
  - `confidence(votes: Iterable[str]) -> str`. `high`, `medium`, `low` or `none`.
  - `Evidence(votes: Mapping[str, str], scores: Mapping[str, float], efforts: Mapping[str, str], base: str, pre_gate: str, confidence: str)`, frozen. `votes` holds only the signals that had a record, in `SIGNALS` order. `scores` is in the order it is written: `intelligence_index`, `coding_index`, `agentic_index`, `arena_agent`, `arena_agent_ci_lower`, `eci`, each only when the source gave it.
  - `weigh(records: Mapping[str, ScoreRecord], contracts: Contracts) -> Evidence`. `records` maps a signal name to the key's best record in that signal's source.
  - `SentinelResult(sentinel: Sentinel, canonical: str, evidence: Evidence, problems: tuple[str, ...])`, frozen, with the property `ok -> bool`.
  - `check_sentinel(sentinel: Sentinel, canonical: str, evidence: Evidence) -> SentinelResult`.

- [ ] **Step 1: Write the failing test**

Hand-built records, one case per floor boundary, per vote count and per confidence value. The scores in the promotion tests are the real ones from 2026-10-12: Opus 5.5's Arena lower bound was 0.1215 and Sonnet 5.5's 0.0868, either side of the 0.097 floor.

Write `modeltiers/tests/test_assign.py`:

```python
from dataclasses import replace

import pytest

from modeltiers import assign
from modeltiers.assign import Evidence
from modeltiers.contracts import Contracts, Sentinel
from modeltiers.records import ScoreRecord


def _records(
    ii: float | None = None,
    arena: float | None = None,
    eci: float | None = None,
    *,
    arena_lower: float | None = None,
    effort: str | None = None,
) -> dict[str, ScoreRecord]:
    """Best records by signal, from bare scores."""
    found: dict[str, ScoreRecord] = {}
    if ii is not None:
        found["intelligence_index"] = ScoreRecord("openrouter", "v/m", ii)
    if arena is not None:
        found["arena_agent"] = ScoreRecord("arena", "M", arena, ci_lower=arena_lower, effort=effort)
    if eci is not None:
        found["eci"] = ScoreRecord("epoch", "M", eci, ci_lower=eci - 2.0)
    return found


# --- step 1: votes -----------------------------------------------------------


@pytest.mark.parametrize(
    ("signal", "score", "expected"),
    [
        ("intelligence_index", 10.59, "unrated"),
        ("intelligence_index", 10.6, "quick"),
        ("intelligence_index", 32.99, "quick"),
        ("intelligence_index", 33.0, "standard"),
        ("intelligence_index", 48.99, "standard"),
        ("intelligence_index", 49.0, "deep"),
        ("intelligence_index", 99.0, "deep"),
        ("arena_agent", -0.0901, "unrated"),
        ("arena_agent", -0.090, "quick"),
        ("arena_agent", -0.0291, "quick"),
        ("arena_agent", -0.029, "standard"),
        ("arena_agent", 0.0559, "standard"),
        ("arena_agent", 0.056, "deep"),
        ("eci", 137.09, "unrated"),
        ("eci", 137.1, "quick"),
        ("eci", 150.69, "quick"),
        ("eci", 150.7, "standard"),
        ("eci", 160.59, "standard"),
        ("eci", 160.6, "deep"),
    ],
)
def test_a_score_votes_the_highest_tier_whose_floor_it_reaches(
    rule: Contracts, signal: str, score: float, expected: str
) -> None:
    assert assign.vote(rule.signals[signal], score) == expected


def test_no_vote_is_above_max_vote(rule: Contracts) -> None:
    capped = replace(rule.signals["eci"], max_vote="standard")
    assert assign.vote(capped, 170.0) == "standard"
    assert assign.vote(capped, 140.0) == "quick"
    assert assign.vote(capped, 100.0) == "unrated"


# --- step 2: base tier -------------------------------------------------------


@pytest.mark.parametrize(
    ("votes", "expected"),
    [
        ([], "unrated"),
        (["deep"], "deep"),
        (["deep", "standard"], "standard"),  # two votes: the lower
        (["standard", "deep"], "standard"),
        (["deep", "quick", "standard"], "standard"),  # three: the middle
        (["deep", "deep", "quick"], "deep"),
        (["quick", "quick", "deep"], "quick"),
        (["unrated", "standard"], "unrated"),
        (["unrated", "standard", "standard"], "standard"),
    ],
)
def test_the_base_tier_is_the_lower_median(votes: list[str], expected: str) -> None:
    assert assign.base_tier(votes) == expected


@pytest.mark.parametrize(
    ("votes", "expected"),
    [
        ([], "none"),
        (["deep"], "low"),
        (["deep", "deep"], "high"),
        (["deep", "deep", "deep"], "high"),
        (["deep", "standard"], "medium"),
        (["deep", "standard", "standard"], "medium"),
        (["deep", "quick"], "low"),
        (["deep", "standard", "quick"], "low"),
        (["unrated", "quick"], "medium"),
    ],
)
def test_confidence_comes_from_the_votes_alone(votes: list[str], expected: str) -> None:
    assert assign.confidence(votes) == expected


# --- steps 1 to 3 together ---------------------------------------------------


def test_weigh_records_votes_scores_and_efforts_in_file_order(rule: Contracts) -> None:
    records = _records(44.8, 0.0215, 155.61, arena_lower=0.0143, effort="max")
    records["intelligence_index"] = replace(
        records["intelligence_index"], coding_index=74.8, agentic_index=53.1
    )
    evidence = assign.weigh(records, rule)
    assert evidence.votes == {
        "intelligence_index": "standard",
        "arena_agent": "standard",
        "eci": "standard",
    }
    # The evidence-only indices are recorded and do not vote. The Epoch
    # interval is not written: only the marathon signal's lower bound is used.
    assert list(evidence.scores.items()) == [
        ("intelligence_index", 44.8),
        ("coding_index", 74.8),
        ("agentic_index", 53.1),
        ("arena_agent", 0.0215),
        ("arena_agent_ci_lower", 0.0143),
        ("eci", 155.61),
    ]
    assert evidence.efforts == {"arena_agent": "max"}
    assert (evidence.base, evidence.pre_gate, evidence.confidence) == (
        "standard",
        "standard",
        "high",
    )


def test_a_signal_with_no_record_abstains(rule: Contracts) -> None:
    evidence = assign.weigh(_records(eci=162.0), rule)
    assert evidence.votes == {"eci": "deep"}
    assert evidence.scores == {"eci": 162.0}
    assert evidence.efforts == {}
    assert (evidence.base, evidence.confidence) == ("deep", "low")


def test_no_records_is_unrated_with_confidence_none(rule: Contracts) -> None:
    evidence = assign.weigh({}, rule)
    assert evidence == Evidence({}, {}, {}, "unrated", "unrated", "none")


def test_a_deep_model_is_promoted_when_its_arena_lower_bound_reaches_the_floor(
    rule: Contracts,
) -> None:
    at_floor = assign.weigh(_records(57.6, 0.1433, 167.33, arena_lower=0.097), rule)
    assert (at_floor.base, at_floor.pre_gate) == ("deep", "marathon")
    # The point score does not promote: Sonnet 5.5 sat at 0.1195 with a lower
    # bound of 0.0868.
    under = assign.weigh(_records(57.6, 0.1195, 167.33, arena_lower=0.0868), rule)
    assert (under.base, under.pre_gate) == ("deep", "deep")
    # Promotion does not touch the votes or the confidence.
    assert set(at_floor.votes.values()) == {"deep"}
    assert at_floor.confidence == "high"


def test_promotion_is_refused_when_the_base_tier_is_standard(rule: Contracts) -> None:
    # Arena alone says marathon-grade; the other two votes hold the base down.
    evidence = assign.weigh(_records(44.8, 0.15, 155.0, arena_lower=0.12), rule)
    assert evidence.votes["arena_agent"] == "deep"
    assert (evidence.base, evidence.pre_gate) == ("standard", "standard")


def test_no_arena_record_means_no_route_to_marathon(rule: Contracts) -> None:
    evidence = assign.weigh(_records(ii=60.0, eci=170.0), rule)
    assert (evidence.base, evidence.pre_gate) == ("deep", "deep")
    missing_interval = assign.weigh(_records(60.0, 0.2, 170.0, arena_lower=None), rule)
    assert missing_interval.pre_gate == "deep"


# --- step 4: sentinels -------------------------------------------------------


def test_a_sentinel_holds_when_its_pinned_votes_and_tier_match(rule: Contracts) -> None:
    evidence = assign.weigh(_records(57.6, 0.1433, 167.33, arena_lower=0.1215), rule)
    result = assign.check_sentinel(rule.sentinels[0], "anthropic/claude-opus-5-5", evidence)
    assert result.ok
    assert result.problems == ()


def test_a_drifted_vote_fails_the_sentinel_though_the_tier_holds(rule: Contracts) -> None:
    # 49.6 * 0.8 = 39.68: the intelligence index now votes standard. The
    # lower median of standard, deep, deep is still deep.
    evidence = assign.weigh(_records(39.68, 0.0894, 162.06, arena_lower=0.0767), rule)
    assert evidence.pre_gate == "deep"
    result = assign.check_sentinel(rule.sentinels[1], "anthropic/claude-fable-5", evidence)
    assert not result.ok
    assert result.problems == ("intelligence_index voted standard, pinned deep",)


def test_a_missing_pinned_vote_and_a_moved_tier_both_fail(rule: Contracts) -> None:
    evidence = assign.weigh(_records(arena=0.0894, arena_lower=0.0767), rule)
    pinned = Sentinel("v/m", "marathon", {"eci": "deep", "arena_agent": "deep"})
    result = assign.check_sentinel(pinned, "v/m", evidence)
    assert result.problems == (
        "eci voted nothing, pinned deep",
        "tier before the gates is deep, pinned marathon",
    )


def test_a_signal_the_sentinel_does_not_pin_is_not_checked(rule: Contracts) -> None:
    # The fixture's MiniMax sentinel pins Arena and Epoch only.
    evidence = assign.weigh(_records(60.0, -0.0695, 146.95), rule)
    assert evidence.votes["intelligence_index"] == "deep"
    assert assign.check_sentinel(rule.sentinels[3], "minimax/MiniMax-M3", evidence).ok
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_assign.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'assign' from 'modeltiers'`.

- [ ] **Step 3: Write steps 1 to 4 of the assignment**

Write `modeltiers/src/modeltiers/assign.py`:

```python
"""Assignment: from one key's best records to its tier.

The spec's order: (1) votes, (2) base tier, (3) marathon promotion, which
gives the pre-gate tier, (4) the sentinel check on the votes and the pre-gate
tier, (5) the tool-calling gate, (6) the context gate, (7) the final tier.
`weigh` is steps 1 to 3, `check_sentinel` is step 4, `decide` is steps 5 to 7.
"""

from collections.abc import Iterable, Mapping
from dataclasses import dataclass

from modeltiers.contracts import FLOOR_TIERS, RANK, UNRATED, Contracts, Sentinel, Signal
from modeltiers.records import ScoreRecord

NO_VOTES = "no_votes"
BELOW_FLOOR = "below_floor"
GATE_TOOL_CALL = "gate_tool_call"
GATE_CONTEXT = "gate_context"
NOT_APPLICABLE = "not_applicable"

NOTE_LARGEST = "context is the largest among the key's models.dev rows"
NOTE_LOCAL = "local build: served context unknown; context gate skipped"
NOTE_NO_ROWS = "key has no models.dev rows; tool-calling and context gates skipped"
NOTE_NO_CONTEXT = "no models.dev row gives a context; context gate skipped"


def vote(signal: Signal, score: float) -> str:
    """The tier one score votes for: the highest tier whose floor it reaches,
    never above the signal's `max_vote`, and `unrated` under the quick floor."""
    cast = UNRATED
    for tier in FLOOR_TIERS:
        if score >= signal.floors[tier]:
            cast = tier
    return cast if RANK[cast] <= RANK[signal.max_vote] else signal.max_vote


def base_tier(votes: Iterable[str]) -> str:
    """The lower median: the votes sorted by rank, lowest first, and the one
    at index (n-1)//2. With two votes it is the lower; with none, `unrated`."""
    ranked = sorted(votes, key=lambda tier: RANK[tier])
    return ranked[(len(ranked) - 1) // 2] if ranked else UNRATED


def confidence(votes: Iterable[str]) -> str:
    """How far the votes agree. Promotion and gates do not change it."""
    ranks = [RANK[tier] for tier in votes]
    if not ranks:
        return "none"
    spread = max(ranks) - min(ranks)
    if len(ranks) == 1 or spread >= 2:
        return "low"
    return "high" if spread == 0 else "medium"


@dataclass(frozen=True)
class Evidence:
    """Steps 1 to 3 for one key. Each mapping is in the order it is written."""

    votes: Mapping[str, str]  # signal -> the tier it voted; only signals with a record
    scores: Mapping[str, float]  # the voting scores and the evidence-only ones
    efforts: Mapping[str, str]  # signal -> the effort its record named
    base: str  # the lower median of the votes
    pre_gate: str  # after marathon promotion; what a sentinel pins
    confidence: str  # high | medium | low | none


def weigh(records: Mapping[str, ScoreRecord], contracts: Contracts) -> Evidence:
    """Votes, base tier and marathon promotion. `records` maps a signal name
    to the key's best record in that signal's source; a signal with no record
    abstains."""
    votes: dict[str, str] = {}
    scores: dict[str, float] = {}
    efforts: dict[str, str] = {}
    for name, signal in contracts.signals.items():
        record = records.get(name)
        if record is None:
            continue
        votes[name] = vote(signal, record.score)
        scores[name] = record.score
        if record.coding_index is not None:
            scores["coding_index"] = record.coding_index
        if record.agentic_index is not None:
            scores["agentic_index"] = record.agentic_index
        if signal.marathon_ci_lower is not None and record.ci_lower is not None:
            scores[f"{name}_ci_lower"] = record.ci_lower
        if record.effort is not None:
            efforts[name] = record.effort
    base = base_tier(votes.values())
    marathon = contracts.marathon
    lower = scores.get(f"{marathon.name}_ci_lower")
    promoted = (
        base == "deep"
        and lower is not None
        and marathon.marathon_ci_lower is not None
        and lower >= marathon.marathon_ci_lower
    )
    return Evidence(
        votes=votes,
        scores=scores,
        efforts=efforts,
        base=base,
        pre_gate="marathon" if promoted else base,
        confidence=confidence(votes.values()),
    )


@dataclass(frozen=True)
class SentinelResult:
    sentinel: Sentinel
    canonical: str  # in the stored spelling
    evidence: Evidence
    problems: tuple[str, ...]  # empty: the sentinel holds

    @property
    def ok(self) -> bool:
        return not self.problems


def check_sentinel(sentinel: Sentinel, canonical: str, evidence: Evidence) -> SentinelResult:
    """Step 4. A sentinel fails when a pinned vote differs or is missing, or
    when the pre-gate tier differs. A signal the sentinel does not pin is not
    looked at."""
    problems = [
        f"{signal} voted {evidence.votes.get(signal, 'nothing')}, pinned {pinned}"
        for signal, pinned in sentinel.votes.items()
        if evidence.votes.get(signal) != pinned
    ]
    if evidence.pre_gate != sentinel.tier:
        problems.append(f"tier before the gates is {evidence.pre_gate}, pinned {sentinel.tier}")
    return SentinelResult(
        sentinel=sentinel, canonical=canonical, evidence=evidence, problems=tuple(problems)
    )
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_assign.py -q`
Expected: PASS, `48 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/assign.py modeltiers/tests/test_assign.py
git commit -m "feat(modeltiers): votes, the lower-median base tier, marathon promotion and the sentinel check"
```

---

### Task 3: The context table and the two gates

**Files:**
- Modify: `modeltiers/src/modeltiers/assign.py` (two import lines; append `Facts`, `gather`, `Verdict`, `decide`)
- Test: `modeltiers/tests/test_assign.py` (two import lines; append)

**Interfaces:**
- Consumes: `Evidence`, `weigh`, the reason and note constants (Task 2); `modeltiers.contracts.TIERS`, `RANK`, `Contracts` (Task 1); `modeltiers.records.ModelsDevRow` (step 1, Task 2).
- Produces, in `modeltiers.assign`:
  - `Facts(tool_call: bool | None, context: int | None, notes: tuple[str, ...] = ())`, frozen. `tool_call` is `None` for a key with no models.dev rows; `context` is `None` when unknown.
  - `gather(rows: tuple[ModelsDevRow, ...], *, location: str | None, host_row: ModelsDevRow | None) -> Facts`. `rows` are the key's models.dev rows; `location` is the inventory row's, or `None` for a candidate; `host_row` is `RowIdentity.host_row`.
  - `Verdict(tier: str, reason: str | None, notes: tuple[str, ...])`, frozen. `reason` is set exactly when `tier` is `unrated`.
  - `decide(evidence: Evidence, facts: Facts, contracts: Contracts) -> Verdict`.
  - The note a lowered entry gets: `gate_context: <tier> needs <minimum>, context is <context>; lowered to <tier>`.

- [ ] **Step 1: Add the failing tests**

One test per row of the spec's context table, then the gates and each `unrated` reason.

In `modeltiers/tests/test_assign.py`, replace:

```python
from modeltiers.assign import Evidence
from modeltiers.contracts import Contracts, Sentinel
from modeltiers.records import ScoreRecord
```

with:

```python
from modeltiers.assign import Evidence, Facts
from modeltiers.contracts import Contracts, Sentinel
from modeltiers.records import ModelsDevRow, ScoreRecord
```

Append to the end of `modeltiers/tests/test_assign.py`, after two blank lines:

```python
# --- the context table -------------------------------------------------------


def _row(*, tool_call: bool = True, context: int | None = 1_000_000) -> ModelsDevRow:
    return ModelsDevRow("vendor", "m", "vendor/m", tool_call, True, context)


def test_a_cloud_row_uses_its_host_rows_context() -> None:
    host = ModelsDevRow("ollama-cloud", "m", None, False, True, 512_000)
    facts = assign.gather((_row(context=1_048_576),), location="cloud", host_row=host)
    # The host row need not have a canonical id; tool_call comes from the
    # key's rows, never from the host row.
    assert facts == Facts(tool_call=True, context=512_000, notes=())


def test_a_cloud_row_with_no_host_row_and_a_candidate_use_the_largest_context() -> None:
    rows = (_row(context=131_072), _row(context=1_048_576), _row(context=None))
    expected = Facts(tool_call=True, context=1_048_576, notes=(assign.NOTE_LARGEST,))
    assert assign.gather(rows, location="cloud", host_row=None) == expected
    assert assign.gather(rows, location=None, host_row=None) == expected
    # A host row that gives no context is no better than none.
    blank = ModelsDevRow("openrouter", "v/m", "vendor/m", True, True, None)
    assert assign.gather(rows, location="cloud", host_row=blank) == expected


def test_a_local_rows_context_is_unknown() -> None:
    facts = assign.gather((_row(),), location="local", host_row=None)
    assert facts == Facts(tool_call=True, context=None, notes=(assign.NOTE_LOCAL,))


def test_a_key_with_no_models_dev_rows_skips_both_gates() -> None:
    host = ModelsDevRow("ollama-cloud", "m", None, True, True, 512_000)
    for location, host_row in (("cloud", host), ("local", None), (None, None)):
        facts = assign.gather((), location=location, host_row=host_row)
        assert facts == Facts(tool_call=None, context=None, notes=(assign.NOTE_NO_ROWS,))


def test_rows_that_give_no_context_leave_it_unknown() -> None:
    facts = assign.gather((_row(context=None),), location=None, host_row=None)
    assert facts == Facts(tool_call=True, context=None, notes=(assign.NOTE_NO_CONTEXT,))


def test_tool_call_is_true_when_any_row_of_the_key_reports_it() -> None:
    rows = (_row(tool_call=False), _row(tool_call=True))
    assert assign.gather(rows, location=None, host_row=None).tool_call is True
    assert assign.gather(rows[:1], location=None, host_row=None).tool_call is False


# --- steps 5 to 7: gates and the unrated reasons -----------------------------


def _evidence(rule: Contracts, pre_gate: str) -> Evidence:
    scores = {"marathon": 0.15, "deep": 0.06, "standard": 0.0, "quick": -0.05, "unrated": -0.2}
    evidence = assign.weigh(_records(arena=scores[pre_gate], arena_lower=0.0), rule)
    return replace(evidence, pre_gate=pre_gate)


def test_no_votes_is_unrated_and_no_gate_is_applied(rule: Contracts) -> None:
    facts = Facts(tool_call=False, context=10)
    verdict = assign.decide(assign.weigh({}, rule), facts, rule)
    assert (verdict.tier, verdict.reason) == ("unrated", "no_votes")


def test_votes_under_the_quick_floor_are_below_floor(rule: Contracts) -> None:
    evidence = assign.weigh(_records(ii=10.0), rule)  # gpt-oss:20b
    assert evidence.votes == {"intelligence_index": "unrated"}
    verdict = assign.decide(evidence, Facts(tool_call=False, context=10), rule)
    assert (verdict.tier, verdict.reason) == ("unrated", "below_floor")


def test_no_tool_calling_is_unrated(rule: Contracts) -> None:
    verdict = assign.decide(_evidence(rule, "deep"), Facts(False, 1_000_000), rule)
    assert (verdict.tier, verdict.reason, verdict.notes) == ("unrated", "gate_tool_call", ())


def test_the_tool_calling_gate_is_skipped_for_a_key_with_no_rows(rule: Contracts) -> None:
    facts = Facts(tool_call=None, context=None, notes=(assign.NOTE_NO_ROWS,))
    verdict = assign.decide(_evidence(rule, "deep"), facts, rule)
    assert (verdict.tier, verdict.reason, verdict.notes) == ("deep", None, (assign.NOTE_NO_ROWS,))


@pytest.mark.parametrize(
    ("pre_gate", "context", "tier", "reason"),
    [
        ("marathon", 400_000, "marathon", None),  # at the minimum: kept
        ("marathon", 399_999, "deep", None),
        ("marathon", 150_000, "standard", None),  # drops past deep
        ("deep", 200_000, "deep", None),
        ("deep", 199_999, "standard", None),
        ("standard", 127_999, "quick", None),
        ("standard", 31_999, "unrated", "gate_context"),
        ("quick", 32_000, "quick", None),
        ("quick", 31_999, "unrated", "gate_context"),
    ],
)
def test_the_context_gate_drops_to_the_highest_tier_the_context_meets(
    rule: Contracts, pre_gate: str, context: int, tier: str, reason: str | None
) -> None:
    verdict = assign.decide(_evidence(rule, pre_gate), Facts(True, context), rule)
    assert (verdict.tier, verdict.reason) == (tier, reason)
    if tier == pre_gate:
        assert verdict.notes == ()
    else:
        need = rule.tiers[pre_gate].context_min
        assert verdict.notes == (
            f"gate_context: {pre_gate} needs {need}, context is {context}; lowered to {tier}",
        )


def test_an_unknown_context_skips_the_context_gate(rule: Contracts) -> None:
    facts = Facts(tool_call=True, context=None, notes=(assign.NOTE_LOCAL,))
    verdict = assign.decide(_evidence(rule, "marathon"), facts, rule)
    assert (verdict.tier, verdict.reason, verdict.notes) == ("marathon", None, (assign.NOTE_LOCAL,))


def test_a_gate_does_not_change_the_evidence(rule: Contracts) -> None:
    evidence = _evidence(rule, "deep")
    assign.decide(evidence, Facts(True, 50_000), rule)
    assert evidence.pre_gate == "deep"
    assert evidence.confidence == "low"
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_assign.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'Facts' from 'modeltiers.assign'`.

- [ ] **Step 3: Add the context table and the gates**

In `modeltiers/src/modeltiers/assign.py`, replace:

```python
from modeltiers.contracts import FLOOR_TIERS, RANK, UNRATED, Contracts, Sentinel, Signal
from modeltiers.records import ScoreRecord
```

with:

```python
from modeltiers.contracts import FLOOR_TIERS, RANK, TIERS, UNRATED, Contracts, Sentinel, Signal
from modeltiers.records import ModelsDevRow, ScoreRecord
```

Append to the end of `modeltiers/src/modeltiers/assign.py`, after two blank lines:

```python
@dataclass(frozen=True)
class Facts:
    """What models.dev says about one entry, for the two gates."""

    tool_call: bool | None  # None: the key has no models.dev rows
    context: int | None  # None: unknown, and the context gate is skipped
    notes: tuple[str, ...] = ()


def gather(
    rows: tuple[ModelsDevRow, ...], *, location: str | None, host_row: ModelsDevRow | None
) -> Facts:
    """The spec's context table. `rows` are the key's models.dev rows;
    `location` is the inventory row's, or None for a candidate; `host_row` is
    a cloud inventory row's own host row, with or without a canonical id."""
    if not rows:
        return Facts(tool_call=None, context=None, notes=(NOTE_NO_ROWS,))
    tool_call = any(row.tool_call for row in rows)
    if location == "local":
        return Facts(tool_call=tool_call, context=None, notes=(NOTE_LOCAL,))
    if host_row is not None and host_row.context is not None:
        return Facts(tool_call=tool_call, context=host_row.context)
    contexts = [row.context for row in rows if row.context is not None]
    if not contexts:
        return Facts(tool_call=tool_call, context=None, notes=(NOTE_NO_CONTEXT,))
    return Facts(tool_call=tool_call, context=max(contexts), notes=(NOTE_LARGEST,))


@dataclass(frozen=True)
class Verdict:
    tier: str  # the final tier
    reason: str | None  # set exactly when the tier is unrated
    notes: tuple[str, ...]


def decide(evidence: Evidence, facts: Facts, contracts: Contracts) -> Verdict:
    """Steps 5 to 7: the tool-calling gate, the context gate, the final tier.
    With no votes, or votes under the quick floor, neither gate is applied."""
    if not evidence.votes:
        return Verdict(UNRATED, NO_VOTES, facts.notes)
    if evidence.pre_gate == UNRATED:
        return Verdict(UNRATED, BELOW_FLOOR, facts.notes)
    if facts.tool_call is False:
        return Verdict(UNRATED, GATE_TOOL_CALL, facts.notes)
    tier = evidence.pre_gate
    if facts.context is None or facts.context >= contracts.tiers[tier].context_min:
        return Verdict(tier, None, facts.notes)
    met = [
        name
        for name in TIERS
        if RANK[name] < RANK[tier] and facts.context >= contracts.tiers[name].context_min
    ]
    lowered = met[-1] if met else UNRATED
    note = (
        f"{GATE_CONTEXT}: {tier} needs {contracts.tiers[tier].context_min}, "
        f"context is {facts.context}; lowered to {lowered}"
    )
    return Verdict(lowered, None if met else GATE_CONTEXT, (*facts.notes, note))
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_assign.py -q`
Expected: PASS, `69 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/assign.py modeltiers/tests/test_assign.py
git commit -m "feat(modeltiers): the context table, the tool-calling gate and the context gate"
```

---

### Task 4: `assign_all`, over the golden fixtures

**Files:**
- Modify: `modeltiers/src/modeltiers/pipeline.py`, `modeltiers/tests/conftest.py`, `modeltiers/tests/test_pipeline.py`, `modeltiers/tests/fixtures/modelsdev.json`, `modeltiers/tests/fixtures/openrouter.json`, `modeltiers/tests/fixtures/arena_page1.json`, `modeltiers/tests/fixtures/arena_page2.json`, `modeltiers/tests/fixtures/eci_scores.csv`, `modeltiers/tests/test_source_modelsdev.py`, `modeltiers/tests/test_source_openrouter.py`, `modeltiers/tests/test_source_arena.py`, `modeltiers/tests/test_source_epoch.py`, `modeltiers/tests/test_loader.py`, `modeltiers/tests/test_cli.py`
- Test: `modeltiers/tests/test_pipeline.py`

**Interfaces:**
- Consumes: `modeltiers.assign.weigh`, `gather`, `decide`, `check_sentinel`, `Evidence`, `Facts`, `Verdict`, `SentinelResult`, `NOT_APPLICABLE` (Tasks 2 and 3); `modeltiers.contracts.Contracts`, `RANK`, `UNRATED` (Task 1); `modeltiers.pipeline.Identity`, `resolve_all` (step 2, Task 6); `modeltiers.identity.Match`, `RowIdentity`, `Index.spelling`, `Index.rows_for`, `ALIAS`, `HOST`, `NORMALIZED` (step 2, Tasks 4 and 5); `fakes.FakeFetcher`, `fakes.Sleeps`, the `specs` and `good_answers` fixtures (step 1).
- Produces, in `modeltiers.pipeline`:
  - `ModelResult(identity: RowIdentity, evidence: Evidence, facts: Facts, verdict: Verdict)`, frozen.
  - `Candidate(canonical: str, resolved_by: str, open_weights: bool | None, evidence: Evidence, facts: Facts, verdict: Verdict)`, frozen.
  - `Assignment(identity: Identity, contracts: Contracts, models: tuple[ModelResult, ...], candidates: tuple[Candidate, ...], unresolved: tuple[ScoreRecord, ...], sentinels: tuple[SentinelResult, ...])`, frozen, with the property `sentinels_ok -> bool`. `models` holds the resolved and the ignored rows in wt's order; the unmapped rows stay in `identity.unmapped`. `candidates` is ordered by rank, highest first, then by key. `unresolved` holds the best record of each unresolved source id at or above its signal's standard floor, in source order (openrouter, arena, epoch) and then by score, highest first.
  - `assign_all(result: Identity, contracts: Contracts) -> Assignment`. Raises `ConfigError` for a sentinel whose `canonical` is not a known key.
  - In `tests/conftest.py`: `T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)` and the fixtures `inventory -> Inventory`, `sources -> Sources`, `fixture_aliases -> Aliases`, `assignment -> Assignment`.
  - The golden fixtures now hold 12 models.dev rows, 8 OpenRouter models (6 records), 6 Arena rows and 6 Epoch rows; the `specs` fixture is `build_specs(modelsdev_min=12, openrouter_min=6, arena_min=6, epoch_min=6)`.

- [ ] **Step 1: Add Claude Fable 5 to the four golden fixtures, and a score for a local row's key**

The spec's sentinel test names `anthropic/claude-fable-5`, so it needs a record in each source. The rows are trimmed from the 2026-10-12 downloads like the others: intelligence index 49.6, Arena 0.0894 with a lower bound of 0.0767, Epoch 162.06. Each row is added at the end of its list, so no existing row changes position; the Arena row goes on page 2 and both pages now say `num_rows_total` 6.

`openrouter.json` gains a second row after it, `qwen/qwen3.8-27b`, also trimmed from the 2026-10-12 download: intelligence index 33.7, coding 68.1, agentic 45.8. models.dev's fixture has no `openrouter` host row for it, so the record reaches `alibaba/qwen3.8-27b` by name, and that is the key the local inventory row `ollama/qwen3.8:27b-mlx` resolves to. Until now no local row of the fixtures had a score behind it, so nothing showed a tier being inherited. `modelsdev.json` gets no new row for it.

Replace the whole of `modeltiers/tests/fixtures/modelsdev.json` with:

```json
{
 "ollama-cloud": {
  "id": "ollama-cloud",
  "name": "Ollama Cloud",
  "models": {
   "glm-5.3": {"id": "glm-5.3", "name": "GLM-5.3", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 131072}, "canonical_model_id": "zhipuai/glm-5.3"},
   "deepseek-v4-pro": {"id": "deepseek-v4-pro", "name": "deepseek-v4-pro", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 1048576}},
   "deepseek-v4-pro:0813": {"id": "deepseek-v4-pro:0813", "name": "DeepSeek V4 Pro 0813", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 1048576}, "canonical_model_id": "deepseek/deepseek-v4-pro-0813"},
   "minimax-m3": {"id": "minimax-m3", "name": "minimax-m3", "tool_call": true, "open_weights": true, "limit": {"context": 512000, "output": 131072}}
  }
 },
 "openrouter": {
  "id": "openrouter",
  "name": "OpenRouter",
  "models": {
   "z-ai/glm-5.3": {"id": "z-ai/glm-5.3", "name": "GLM-5.3", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 943718}, "canonical_model_id": "zhipuai/glm-5.3"},
   "anthropic/claude-opus-5.5": {"id": "anthropic/claude-opus-5.5", "name": "Claude Opus 5.5", "tool_call": true, "open_weights": false, "limit": {"context": 1000000, "output": 128000}, "canonical_model_id": "anthropic/claude-opus-5-5"},
   "anthropic/claude-fable-5": {"id": "anthropic/claude-fable-5", "name": "Claude Fable 5", "tool_call": true, "open_weights": false, "limit": {"context": 1000000, "output": 128000}, "canonical_model_id": "anthropic/claude-fable-5"},
   "minimax/minimax-m3": {"id": "minimax/minimax-m3", "name": "MiniMax-M3", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 512000}, "canonical_model_id": "minimax/MiniMax-M3"}
  }
 },
 "zhipuai": {
  "id": "zhipuai",
  "name": "Zhipu AI",
  "models": {
   "glm-5.3": {"id": "glm-5.3", "name": "GLM-5.3", "tool_call": true, "open_weights": true, "limit": {"context": 1000000, "output": 131072}, "canonical_model_id": "zhipuai/glm-5.3"}
  }
 },
 "deepinfra": {
  "id": "deepinfra",
  "name": "Deep Infra",
  "models": {
   "google/gemma-4-31B-it": {"id": "google/gemma-4-31B-it", "name": "Gemma 4 31B IT", "tool_call": true, "open_weights": true, "limit": {"context": 262144, "output": 32768}, "canonical_model_id": "google/gemma-4-31b-it"},
   "Qwen/Qwen3.8-27B": {"id": "Qwen/Qwen3.8-27B", "name": "Qwen3.8 27B", "tool_call": true, "open_weights": true, "limit": {"context": 262144, "output": 32768}, "canonical_model_id": "alibaba/qwen3.8-27b"},
   "deepseek-ai/DeepSeek-V4-Pro": {"id": "deepseek-ai/DeepSeek-V4-Pro", "name": "DeepSeek V4 Pro", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 16384}, "canonical_model_id": "deepseek/deepseek-v4-pro"}
  }
 }
}
```

Replace the whole of `modeltiers/tests/fixtures/openrouter.json` with:

```json
{
 "data": [
  {"id": "z-ai/glm-5.3", "name": "Z.ai: GLM 5.3", "context_length": 1048576, "benchmarks": {"artificial_analysis": {"intelligence_index": 44.8, "coding_index": 74.8, "agentic_index": 53.1}}},
  {"id": "z-ai/glm-5.3:batch", "name": "Z.ai: GLM 5.3 (batch)", "context_length": 1048576, "benchmarks": {"artificial_analysis": {"intelligence_index": 44.8, "coding_index": 74.8, "agentic_index": 53.1}}},
  {"id": "anthropic/claude-opus-5.5", "name": "Anthropic: Claude Opus 5.5", "context_length": 1000000, "benchmarks": {"artificial_analysis": {"intelligence_index": 57.6, "coding_index": null, "agentic_index": null}}},
  {"id": "liquid/lfm-2.5-2.6b:free", "name": "LiquidAI: LFM2.5-2.6B (free)", "context_length": 65536, "benchmarks": {"artificial_analysis": {"intelligence_index": 8.4, "coding_index": 7.7, "agentic_index": null}}},
  {"id": "z-ai/glm-5.3-prime", "name": "Z.ai: GLM 5.3 Prime", "context_length": 1000000},
  {"id": "mistralai/mistral-large-4-0", "name": "Mistral: Mistral Large 4", "context_length": 1048576, "benchmarks": {"artificial_analysis": null}},
  {"id": "anthropic/claude-fable-5", "name": "Anthropic: Claude Fable 5", "context_length": 1000000, "benchmarks": {"artificial_analysis": {"intelligence_index": 49.6, "coding_index": 76.5, "agentic_index": 50.7}}},
  {"id": "qwen/qwen3.8-27b", "name": "Qwen: Qwen3.8 27B", "context_length": 1000000, "benchmarks": {"artificial_analysis": {"intelligence_index": 33.7, "coding_index": 68.1, "agentic_index": 45.8}}}
 ]
}
```

Replace the whole of `modeltiers/tests/fixtures/arena_page1.json` with:

```json
{
 "rows": [
  {"row_idx": 0, "row": {"model_name": "Claude Opus 5.5 (High)", "organization": "anthropic", "score": 0.14331280058415052, "score_ci_lower": 0.12150663912969831, "score_ci_upper": 0.16511896203860274, "rank": 1, "category": "overall"}, "truncated_cells": []},
  {"row_idx": 1, "row": {"model_name": "GLM 5.3 (Max)", "organization": "zai", "score": 0.02147446416824893, "score_ci_lower": 0.014306504009895325, "score_ci_upper": 0.028642424326602536, "rank": 24, "category": "overall"}, "truncated_cells": []},
  {"row_idx": 2, "row": {"model_name": "Qwen3.8 Max", "organization": "alibaba", "score": 0.023467932612357068, "score_ci_lower": 0.017532412700631165, "score_ci_upper": 0.02940345252408297, "rank": 23, "category": "overall"}, "truncated_cells": []}
 ],
 "num_rows_total": 6,
 "num_rows_per_page": 100,
 "partial": false
}
```

Replace the whole of `modeltiers/tests/fixtures/arena_page2.json` with:

```json
{
 "rows": [
  {"row_idx": 3, "row": {"model_name": "DeepSeek V4 Pro (High) (0813)", "organization": "deepseek", "score": 0.013425874696272922, "score_ci_lower": -0.0031749129284354416, "score_ci_upper": 0.030026662320981286, "rank": 26, "category": "overall"}, "truncated_cells": []},
  {"row_idx": 4, "row": {"model_name": "Minimax M3", "organization": "minimax", "score": -0.06954648731246331, "score_ci_lower": -0.07855233102193303, "score_ci_upper": -0.060540643602993584, "rank": 44, "category": "overall"}, "truncated_cells": []},
  {"row_idx": 5, "row": {"model_name": "Claude Fable 5 (High)", "organization": "anthropic", "score": 0.08937936055727591, "score_ci_lower": 0.0766906485538394, "score_ci_upper": 0.10206807256071242, "rank": 8, "category": "overall"}, "truncated_cells": []}
 ],
 "num_rows_total": 6,
 "num_rows_per_page": 100,
 "partial": false
}
```

Replace the whole of `modeltiers/tests/fixtures/eci_scores.csv` with:

```text
Model,Display name,eci,eci_ci_low,eci_ci_high,date,Organization,Country (of organization),Model accessibility,Accessibility group,model_versions
Claude Opus 5.5,Claude Opus 5.5,167.33,164.06,171.68,2026-09-22,Anthropic,United States of America,API access,Closed weights,
GLM-5.3,GLM-5.3,155.61,153.6,157.92,2026-08-14,Z.ai (Zhipu AI),China,API access,Closed weights,
DeepSeek V4 Pro 0813,DeepSeek V4 Pro 0813,155.31,153.48,157.45,2026-08-13,DeepSeek,China,Open weights (unrestricted),Open weights,
MiniMax-M3,MiniMax-M3,146.95,142.59,149.44,2026-06-01,MiniMax,China,Open weights (restricted use),Open weights,
GPT-5,GPT-5,150.0,,,2025-08-07,OpenAI,United States of America,API access,Closed weights,
Claude Fable 5,Claude Fable 5,162.06,159.28,166.18,2026-06-09,Anthropic,United States of America,API access,Closed weights,
```

- [ ] **Step 2: Update the counts the earlier tests pin**

In `modeltiers/tests/conftest.py`, replace:

```python
    return loader.build_specs(modelsdev_min=11, openrouter_min=4, arena_min=5, epoch_min=5)
```

with:

```python
    return loader.build_specs(modelsdev_min=12, openrouter_min=6, arena_min=6, epoch_min=6)
```

In `modeltiers/tests/test_source_modelsdev.py`, replace:

```python
    parsed = modelsdev.parse(read_fixture("modelsdev.json"), min_rows=11)
    assert len(parsed.rows) == 11
```

with:

```python
    parsed = modelsdev.parse(read_fixture("modelsdev.json"), min_rows=12)
    assert len(parsed.rows) == 12
```

In `modeltiers/tests/test_source_openrouter.py`, replace:

```python
    # Six models; the one with no benchmarks and the one whose
```

with:

```python
    # Eight models; the one with no benchmarks and the one whose
```

In `modeltiers/tests/test_source_openrouter.py`, replace:

```python
        "liquid/lfm-2.5-2.6b:free",
```

with:

```python
        "liquid/lfm-2.5-2.6b:free",
        "anthropic/claude-fable-5",
        "qwen/qwen3.8-27b",
```

In `modeltiers/tests/test_source_arena.py`, replace:

```python
        ("Minimax M3", None),
```

with:

```python
        ("Minimax M3", None),
        ("Claude Fable 5", "high"),
```

In `modeltiers/tests/test_source_arena.py`, replace:

```python
    # Page 1 alone: 3 rows, and it says the dataset has 5.
    with pytest.raises(SourceParseError, match="read 3 rows of num_rows_total 5"):
```

with:

```python
    # Page 1 alone: 3 rows, and it says the dataset has 6.
    with pytest.raises(SourceParseError, match="read 3 rows of num_rows_total 6"):
```

In `modeltiers/tests/test_source_epoch.py`, replace:

```python
        ("GPT-5", 150.0),
```

with:

```python
        ("GPT-5", 150.0),
        ("Claude Fable 5", 162.06),
```

In `modeltiers/tests/test_loader.py`, replace:

```python
    assert result.fetched_at == T0
    assert result.records == 4
```

with:

```python
    assert result.fetched_at == T0
    assert result.records == 6
```

In `modeltiers/tests/test_loader.py`, replace:

```python
    assert result.records == 4
```

with:

```python
    assert result.records == 6
```

In `modeltiers/tests/test_loader.py`, replace:

```python
    assert "1 rows, under the minimum of 4" in warnings.lines[0]
```

with:

```python
    assert "1 rows, under the minimum of 6" in warnings.lines[0]
```

In `modeltiers/tests/test_loader.py`, replace:

```python
        ("modelsdev", "ok", 11),
        ("openrouter", "ok", 4),
        ("arena", "ok", 5),
        ("epoch", "ok", 5),
```

with:

```python
        ("modelsdev", "ok", 12),
        ("openrouter", "ok", 6),
        ("arena", "ok", 6),
        ("epoch", "ok", 6),
```

In `modeltiers/tests/test_pipeline.py`, replace:

```python
        (4, 2, 1, 0),  # glm-5.3 and its :batch share a key; lfm has no key
        (5, 4, 1, 0),  # Qwen3.8 Max has no key in the fixture
        (5, 4, 0, 1),  # GPT-5 is ignored by the fixture aliases
```

with:

```python
        (6, 4, 1, 0),  # glm-5.3 and its :batch share a key; lfm has no key
        (6, 5, 1, 0),  # Qwen3.8 Max has no key in the fixture
        (6, 5, 0, 1),  # GPT-5 is ignored by the fixture aliases
```

In `modeltiers/tests/test_cli.py`, replace:

```python
modelsdev: ok, 11 records, fetched 2026-10-12T15:58:00Z
openrouter: ok, 4 records, fetched 2026-10-12T15:58:00Z
arena: ok, 5 records, fetched 2026-10-12T15:58:00Z
epoch: ok, 5 records, fetched 2026-10-12T15:58:00Z
```

with:

```python
modelsdev: ok, 12 records, fetched 2026-10-12T15:58:00Z
openrouter: ok, 6 records, fetched 2026-10-12T15:58:00Z
arena: ok, 6 records, fetched 2026-10-12T15:58:00Z
epoch: ok, 6 records, fetched 2026-10-12T15:58:00Z
```

In `modeltiers/tests/test_cli.py`, replace:

```python
  openrouter: 1 of 4 records, 0 ignored, 2 keys resolved
  arena: 1 of 5 records, 0 ignored, 4 keys resolved
  epoch: 0 of 5 records, 1 ignored, 4 keys resolved
```

with:

```python
  openrouter: 1 of 6 records, 0 ignored, 4 keys resolved
  arena: 1 of 6 records, 0 ignored, 5 keys resolved
  epoch: 0 of 6 records, 1 ignored, 5 keys resolved
```

In `modeltiers/tests/test_cli.py`, replace:

```python
    assert "arena: stale, 5 records, fetched 2026-10-12T15:58:00Z\n" in captured.out
    assert "openrouter: ok, 4 records, fetched 2026-10-19T15:58:00Z\n" in captured.out
```

with:

```python
    assert "arena: stale, 6 records, fetched 2026-10-12T15:58:00Z\n" in captured.out
    assert "openrouter: ok, 6 records, fetched 2026-10-19T15:58:00Z\n" in captured.out
```

In `modeltiers/tests/test_cli.py`, replace:

```python
    assert "  epoch: 1 of 5 records, 0 ignored, 4 keys resolved\n" in out
```

with:

```python
    assert "  epoch: 1 of 6 records, 0 ignored, 5 keys resolved\n" in out
```

- [ ] **Step 3: Run the whole suite and see it still pass**

Run: `uv run --directory modeltiers pytest -q`
Expected: PASS, `397 passed`.

- [ ] **Step 4: Move the pipeline fixtures to `conftest.py` and write the failing tests**

`inventory`, `sources` and `fixture_aliases` were local to `tests/test_pipeline.py`. The output, report and CLI tests need them too, so they move, and `assignment` joins them.

In `modeltiers/tests/conftest.py`, replace:

```python
from collections.abc import Callable
```

with:

```python
from collections.abc import Callable
from datetime import UTC, datetime
```

In `modeltiers/tests/conftest.py`, replace:

```python
import pytest

from modeltiers import contracts as contracts_mod
from modeltiers import fetch, loader
from modeltiers.contracts import Contracts
from modeltiers.loader import SourceSpecs
```

with:

```python
import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import aliases as aliases_mod
from modeltiers import contracts as contracts_mod
from modeltiers import fetch, loader, pipeline
from modeltiers import inventory as inventory_mod
from modeltiers.aliases import Aliases
from modeltiers.contracts import Contracts
from modeltiers.loader import Sources, SourceSpecs
from modeltiers.pipeline import Assignment
from modeltiers.records import Inventory
```

In `modeltiers/tests/conftest.py`, replace:

```python
EPOCH_MEMBER = "epoch_capabilities_index/eci_scores.csv"
```

with:

```python
EPOCH_MEMBER = "epoch_capabilities_index/eci_scores.csv"
T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
```

Append to the end of `modeltiers/tests/conftest.py`, after two blank lines:

```python
@pytest.fixture
def inventory() -> Inventory:
    """The golden inventory fixture, parsed."""
    return inventory_mod.parse_inventory((FIXTURES / "inventory.json").read_text())


@pytest.fixture
def sources(
    specs: SourceSpecs, good_answers: dict[str, list[bytes | Exception]], tmp_path: Path
) -> Sources:
    """The four golden fixtures through the loader, fetched at T0."""
    return loader.load_all(
        specs,
        cache=tmp_path / "cache",
        fetcher=FakeFetcher(good_answers),
        sleep=Sleeps(),
        now=T0,
        offline=False,
        warn=lambda message: None,
    )


@pytest.fixture
def fixture_aliases() -> Aliases:
    return aliases_mod.load_aliases(FIXTURES / "aliases.toml")


@pytest.fixture
def assignment(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> Assignment:
    """The golden fixtures resolved and assigned: what `run` writes."""
    return pipeline.assign_all(pipeline.resolve_all(inventory, sources, fixture_aliases), rule)
```

In `tests/test_pipeline.py` the three fixtures and their imports go, and sixteen tests are added at the end. Four of them are the pipeline-level cases the unit tests of Task 3 cannot give: `test_a_local_row_is_rated_through_inheritance`, and three that pass `assign_all` the golden sources with one models.dev row changed in the test (`_with_rows`): a key no row of which calls tools (`gate_tool_call`, for a cloud row and for the local row that inherits the flag), a host that serves less than the quick minimum (`gate_context`), and a host whose context puts one inventory row of a key a tier below another row of the same key.

In `modeltiers/tests/test_pipeline.py`, replace:

```python
from datetime import UTC, datetime
from pathlib import Path

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import aliases as aliases_mod
from modeltiers import inventory as inventory_mod
from modeltiers import loader, pipeline
from modeltiers.aliases import Alias, Aliases
from modeltiers.errors import ConfigError
from modeltiers.loader import Sources, SourceSpecs
from modeltiers.records import Inventory

FIXTURES = Path(__file__).parent / "fixtures"
T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
Answers = dict[str, list[bytes | Exception]]


@pytest.fixture
def inventory() -> Inventory:
    return inventory_mod.parse_inventory((FIXTURES / "inventory.json").read_text())


@pytest.fixture
def sources(specs: SourceSpecs, good_answers: Answers, tmp_path: Path) -> Sources:
    return loader.load_all(
        specs,
        cache=tmp_path / "cache",
        fetcher=FakeFetcher(good_answers),
        sleep=Sleeps(),
        now=T0,
        offline=False,
        warn=lambda message: None,
    )


@pytest.fixture
def fixture_aliases() -> Aliases:
    return aliases_mod.load_aliases(FIXTURES / "aliases.toml")
```

with:

```python
from collections.abc import Callable
from dataclasses import replace

import pytest

from modeltiers import aliases as aliases_mod
from modeltiers import pipeline
from modeltiers.aliases import Alias, Aliases
from modeltiers.contracts import Contracts, Sentinel
from modeltiers.errors import ConfigError
from modeltiers.loader import Sources
from modeltiers.pipeline import Assignment
from modeltiers.records import Inventory, ModelsDevRow, ScoreRecord
```

Append to the end of `modeltiers/tests/test_pipeline.py`, after two blank lines:

```python
# --- assign_all ---------------------------------------------------------------


def test_every_resolved_or_ignored_row_gets_a_tier_in_wt_order(assignment: Assignment) -> None:
    assert [
        (m.identity.row.model_name, m.verdict.tier, m.verdict.reason, m.evidence.confidence)
        for m in assignment.models
    ] == [
        ("glm-5.3:cloud", "standard", None, "high"),
        ("deepseek-v4-pro:cloud", "standard", None, "high"),
        ("z-ai/glm-5.3", "standard", None, "high"),
        ("qwen3.8:27b-mlx", "standard", None, "low"),
        ("minimax-m3:cloud", "quick", None, "high"),
        ("nomic-embed-text:latest", "unrated", "not_applicable", "none"),
        ("gemma4:31b-mlx", "unrated", "no_votes", "none"),
    ]
    # models plus unmapped is the inventory without its native rows.
    assert len(assignment.models) + len(assignment.identity.unmapped) == 8


def test_a_model_carries_its_votes_scores_and_the_best_records_effort(
    assignment: Assignment,
) -> None:
    glm = assignment.models[0]
    assert glm.evidence.votes == {
        "intelligence_index": "standard",
        "arena_agent": "standard",
        "eci": "standard",
    }
    assert glm.evidence.scores["coding_index"] == 74.8
    assert glm.evidence.efforts == {"arena_agent": "max"}
    # DeepSeek has no OpenRouter record in the fixture: two votes.
    deepseek = assignment.models[1]
    assert list(deepseek.evidence.votes) == ["arena_agent", "eci"]
    assert deepseek.evidence.efforts == {"arena_agent": "high"}


def test_context_comes_from_the_rows_own_host(assignment: Assignment) -> None:
    by_name = {m.identity.row.model_name: m for m in assignment.models}
    # The ollama-cloud row for minimax-m3 has no canonical id and serves
    # 512000; the key's only models.dev row (OpenRouter's) says 1048576.
    assert by_name["minimax-m3:cloud"].facts.context == 512000
    assert by_name["minimax-m3:cloud"].facts.notes == ()
    local = by_name["qwen3.8:27b-mlx"]
    assert (local.facts.tool_call, local.facts.context) == (True, None)
    assert local.verdict.notes == ("local build: served context unknown; context gate skipped",)


def test_a_local_row_is_rated_through_inheritance(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    """A local build takes its key's votes, tier and tool-calling flag: the
    row is `inherited` and rated, on a score no source gave under its name."""
    identity = pipeline.resolve_all(inventory, sources, fixture_aliases)
    by_name = {m.identity.row.model_name: m for m in pipeline.assign_all(identity, rule).models}
    local = by_name["qwen3.8:27b-mlx"]
    assert (local.identity.row.location, local.identity.basis) == ("local", "inherited")
    assert local.identity.canonical == "alibaba/qwen3.8-27b"
    # The key's one record is OpenRouter's `qwen/qwen3.8-27b`, joined by name.
    assert identity.openrouter.best["alibaba/qwen3.8-27b"].resolved_by == "normalized"
    assert local.evidence.votes == {"intelligence_index": "standard"}
    assert local.evidence.scores == {
        "intelligence_index": 33.7,
        "coding_index": 68.1,
        "agentic_index": 45.8,
    }
    assert (local.verdict.tier, local.verdict.reason) == ("standard", None)
    assert local.evidence.confidence == "low"  # one vote
    assert local.facts.tool_call is True  # the key's deepinfra row says so
    # With no inventory row the key is a candidate, with the same evidence.
    alone = pipeline.resolve_all(replace(inventory, rows=()), sources, fixture_aliases)
    (candidate,) = [
        c
        for c in pipeline.assign_all(alone, rule).candidates
        if c.canonical == local.identity.canonical
    ]
    assert candidate.evidence == local.evidence
    assert candidate.verdict.tier == local.verdict.tier
    # A local row whose key nothing scored inherits that too.
    gemma = by_name["gemma4:31b-mlx"]
    assert (gemma.identity.basis, gemma.verdict.tier, gemma.verdict.reason) == (
        "inherited",
        "unrated",
        "no_votes",
    )


def _with_rows(sources: Sources, change: Callable[[ModelsDevRow], ModelsDevRow]) -> Sources:
    """The golden sources with every models.dev row passed through `change`."""
    modelsdev = sources.modelsdev
    rows = tuple(change(row) for row in modelsdev.data.rows)
    return replace(sources, modelsdev=replace(modelsdev, data=replace(modelsdev.data, rows=rows)))


def _tiers(result: Assignment) -> dict[str, tuple[str, str | None]]:
    return {m.identity.row.model_name: (m.verdict.tier, m.verdict.reason) for m in result.models}


def test_a_key_no_row_says_calls_tools_is_unrated_gate_tool_call(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    keys = {"minimax/MiniMax-M3", "alibaba/qwen3.8-27b"}
    mute = _with_rows(
        sources, lambda row: replace(row, tool_call=False) if row.canonical in keys else row
    )
    result = pipeline.assign_all(pipeline.resolve_all(inventory, mute, fixture_aliases), rule)
    by_name = {m.identity.row.model_name: m for m in result.models}
    cloud = by_name["minimax-m3:cloud"]
    assert (cloud.verdict.tier, cloud.verdict.reason) == ("unrated", "gate_tool_call")
    # The votes, the confidence and the tier before the gates are untouched.
    assert (cloud.evidence.pre_gate, cloud.evidence.confidence) == ("quick", "high")
    # A local build inherits the flag, and the gate with it.
    local = by_name["qwen3.8:27b-mlx"]
    assert local.facts.tool_call is False
    assert (local.verdict.tier, local.verdict.reason) == ("unrated", "gate_tool_call")
    assert local.evidence.pre_gate == "standard"
    # The sentinel check reads the pre-gate tier: MiniMax-M3 still holds.
    assert result.sentinels_ok
    assert _tiers(result)["glm-5.3:cloud"] == ("standard", None)


def test_a_host_under_the_quick_minimum_is_unrated_gate_context(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    def shrink(row: ModelsDevRow) -> ModelsDevRow:
        host = (row.provider, row.model_id) == ("ollama-cloud", "minimax-m3")
        return replace(row, context=16000) if host else row

    result = pipeline.assign_all(
        pipeline.resolve_all(inventory, _with_rows(sources, shrink), fixture_aliases), rule
    )
    cloud = next(m for m in result.models if m.identity.row.model_name == "minimax-m3:cloud")
    assert (cloud.verdict.tier, cloud.verdict.reason) == ("unrated", "gate_context")
    assert cloud.verdict.notes == (
        "gate_context: quick needs 32000, context is 16000; lowered to unrated",
    )
    assert cloud.evidence.pre_gate == "quick"
    assert result.sentinels_ok


def test_a_cloud_rows_own_host_context_decides_its_final_tier(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    """Two inventory rows of one key end in two tiers: Ollama's cloud serves
    GLM-5.3 with 64000 tokens here, OpenRouter with 1048576. The key's
    largest context, 1048576, would have left both at standard."""

    def shrink(row: ModelsDevRow) -> ModelsDevRow:
        host = (row.provider, row.model_id) == ("ollama-cloud", "glm-5.3")
        return replace(row, context=64000) if host else row

    identity = pipeline.resolve_all(inventory, _with_rows(sources, shrink), fixture_aliases)
    result = pipeline.assign_all(identity, rule)
    by_name = {m.identity.row.model_name: m for m in result.models}
    ollama, openrouter = by_name["glm-5.3:cloud"], by_name["z-ai/glm-5.3"]
    assert ollama.identity.canonical == openrouter.identity.canonical == "zhipuai/glm-5.3"
    assert ollama.evidence == openrouter.evidence
    assert max(row.context or 0 for row in identity.index.rows_for("zhipuai/glm-5.3")) == 1048576
    assert (ollama.facts.context, openrouter.facts.context) == (64000, 1048576)
    assert (ollama.verdict.tier, ollama.verdict.reason) == ("quick", None)
    assert ollama.verdict.notes == (
        "gate_context: standard needs 128000, context is 64000; lowered to quick",
    )
    assert (openrouter.verdict.tier, openrouter.verdict.notes) == ("standard", ())


def test_an_ignored_row_is_unrated_not_applicable(assignment: Assignment) -> None:
    ignored = assignment.models[5]
    assert ignored.identity.canonical is None
    assert (ignored.verdict.tier, ignored.verdict.reason) == ("unrated", "not_applicable")
    assert ignored.verdict.notes == ("ignored: embedding model",)
    assert (ignored.facts.tool_call, ignored.facts.context) == (None, None)
    assert "nomic-embed-text:latest" not in [r.row.model_name for r in assignment.identity.unmapped]


def test_candidates_are_keys_with_a_vote_and_no_inventory_row(assignment: Assignment) -> None:
    assert [
        (c.canonical, c.verdict.tier, c.resolved_by, c.open_weights) for c in assignment.candidates
    ] == [
        ("anthropic/claude-opus-5-5", "marathon", "host", False),
        ("anthropic/claude-fable-5", "deep", "host", False),
    ]
    opus, fable = assignment.candidates
    # Opus 5.5's Arena lower bound is 0.1215; Fable 5's is 0.0767.
    assert (opus.evidence.base, opus.evidence.pre_gate) == ("deep", "marathon")
    assert (fable.evidence.base, fable.evidence.pre_gate) == ("deep", "deep")
    assert opus.facts.context == 1000000
    assert opus.verdict.notes == ("context is the largest among the key's models.dev rows",)


def test_candidates_are_ordered_by_rank_then_key(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    # No inventory: every scored key is a candidate.
    empty = replace(inventory, rows=())
    result = pipeline.assign_all(pipeline.resolve_all(empty, sources, fixture_aliases), rule)
    assert [(c.verdict.tier, c.canonical) for c in result.candidates] == [
        ("marathon", "anthropic/claude-opus-5-5"),
        ("deep", "anthropic/claude-fable-5"),
        ("standard", "alibaba/qwen3.8-27b"),
        ("standard", "deepseek/deepseek-v4-pro-0813"),
        ("standard", "zhipuai/glm-5.3"),
        ("quick", "minimax/MiniMax-M3"),
    ]
    # The earliest resolution step among a key's records is the one written.
    assert [c.resolved_by for c in result.candidates] == [
        "host",
        "host",
        "normalized",
        "alias",
        "host",
        "normalized",
    ]


def test_unresolved_lists_only_records_at_or_above_a_standard_floor(
    assignment: Assignment,
) -> None:
    # Arena's "Qwen3.8 Max" (0.0235) is above -0.029; OpenRouter's lfm (8.4)
    # is under 33.0 and is only counted.
    assert [(r.source, r.source_id) for r in assignment.unresolved] == [("arena", "Qwen3.8 Max")]
    assert len(assignment.identity.openrouter.unresolved) == 1


def test_unresolved_keeps_one_record_per_id_in_source_then_score_order(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    def grown(name: str, *extra: ScoreRecord) -> Sources:
        result = getattr(sources, name)
        return replace(sources, **{name: replace(result, data=(*result.data, *extra))})

    sources = grown("openrouter", ScoreRecord("openrouter", "x/unknown", 40.0))
    sources = grown(
        "arena",
        ScoreRecord("arena", "Mystery", -0.01, effort="low"),
        ScoreRecord("arena", "Mystery", 0.04, effort="high"),  # the better effort row
        ScoreRecord("arena", "Too Low", -0.03),
    )
    sources = grown(
        "epoch", ScoreRecord("epoch", "Unknown A", 151.0), ScoreRecord("epoch", "Unknown B", 158.0)
    )
    result = pipeline.assign_all(pipeline.resolve_all(inventory, sources, fixture_aliases), rule)
    assert [(r.source, r.source_id, r.score) for r in result.unresolved] == [
        ("openrouter", "x/unknown", 40.0),
        ("arena", "Mystery", 0.04),
        ("arena", "Qwen3.8 Max", 0.023467932612357068),
        ("epoch", "Unknown B", 158.0),
        ("epoch", "Unknown A", 151.0),
    ]
    # Every unresolved record is still counted, listed or not.
    assert len(result.identity.arena.unresolved) == 4


def test_every_fixture_sentinel_holds(assignment: Assignment) -> None:
    assert assignment.sentinels_ok
    assert [(s.canonical, s.evidence.pre_gate) for s in assignment.sentinels] == [
        ("anthropic/claude-opus-5-5", "marathon"),
        ("anthropic/claude-fable-5", "deep"),
        ("zhipuai/glm-5.3", "standard"),
        ("minimax/MiniMax-M3", "quick"),  # written minimax/minimax-m3 in the file
    ]


def test_scaled_intelligence_indices_fail_a_sentinel_whose_tier_holds(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    scaled = tuple(replace(r, score=r.score * 0.8) for r in sources.openrouter.data)
    drifted = replace(sources, openrouter=replace(sources.openrouter, data=scaled))
    result = pipeline.assign_all(pipeline.resolve_all(inventory, drifted, fixture_aliases), rule)
    assert not result.sentinels_ok
    fable = result.sentinels[1]
    assert fable.evidence.votes["intelligence_index"] == "standard"  # 49.6 * 0.8 = 39.68
    assert fable.evidence.pre_gate == "deep"  # standard, deep, deep: the median holds
    assert fable.problems == ("intelligence_index voted standard, pinned deep",)
    # GLM-5.3 at 35.84 still votes standard, and MiniMax has no index pinned.
    assert [s.ok for s in result.sentinels] == [False, False, True, True]


def test_a_sentinel_naming_an_unknown_key_is_a_config_error(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    unknown = Sentinel("anthropic/claude-fable-9", "deep", {})
    broken = replace(rule, sentinels=(*rule.sentinels, unknown))
    identity = pipeline.resolve_all(inventory, sources, fixture_aliases)
    with pytest.raises(
        ConfigError,
        match=r"contracts.toml: \[\[sentinels\]\] #5 \(anthropic/claude-fable-9\): not a canonical",
    ):
        pipeline.assign_all(identity, broken)


def test_a_sentinel_may_name_an_alias_declared_key(
    inventory: Inventory, sources: Sources, rule: Contracts
) -> None:
    declared = aliases_mod.build([Alias("alibaba/qwen3.8-max", arena=("Qwen3.8 Max",))], [])
    pinned = Sentinel("alibaba/qwen3.8-max", "standard", {"arena_agent": "standard"})
    result = pipeline.assign_all(
        pipeline.resolve_all(inventory, sources, declared), replace(rule, sentinels=(pinned,))
    )
    assert result.sentinels_ok
    (candidate,) = [c for c in result.candidates if c.canonical == "alibaba/qwen3.8-max"]
    # No models.dev rows: both gates skipped, tool_call and open_weights unknown.
    assert (candidate.facts.tool_call, candidate.open_weights) == (None, None)
    assert candidate.verdict.tier == "standard"
    assert candidate.verdict.notes == (
        "key has no models.dev rows; tool-calling and context gates skipped",
    )
```

- [ ] **Step 5: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_pipeline.py -q`
Expected: FAIL while loading `tests/conftest.py`, with `ImportError: cannot import name 'Assignment' from 'modeltiers.pipeline'`.

- [ ] **Step 6: Write `assign_all`**

The whole file again. `Identity` and `resolve_all` are as step 2 left them; the module docstring, the imports and everything below `resolve_all` are new.

Replace the whole of `modeltiers/src/modeltiers/pipeline.py` with:

```python
"""The stages after loading, run in order over one inventory and one set of
sources: `resolve_all` (identity), then `assign_all` (tiers)."""

from dataclasses import dataclass

from modeltiers import aliases as aliases_mod
from modeltiers import assign, identity
from modeltiers.aliases import Aliases
from modeltiers.assign import Evidence, Facts, SentinelResult, Verdict
from modeltiers.contracts import RANK, UNRATED, Contracts
from modeltiers.errors import ConfigError
from modeltiers.identity import Index, Match, RowIdentity, SourceIdentity
from modeltiers.loader import Sources
from modeltiers.records import Inventory, ScoreRecord


@dataclass(frozen=True)
class Identity:
    """Every inventory row and every source record, resolved or not."""

    index: Index
    rows: tuple[RowIdentity, ...]  # one per inventory row, in wt's order
    openrouter: SourceIdentity
    arena: SourceIdentity
    epoch: SourceIdentity
    warnings: tuple[str, ...]  # alias source ids absent from this run's data

    def sources(self) -> tuple[SourceIdentity, ...]:
        """The three benchmark sources, in the order the report lists them."""
        return (self.openrouter, self.arena, self.epoch)

    @property
    def unmapped(self) -> tuple[RowIdentity, ...]:
        """Rows no step resolved and no `[[ignore]]` lists."""
        return tuple(r for r in self.rows if r.unmapped)

    @property
    def review(self) -> tuple[RowIdentity, ...]:
        """Rows matched by name alone, for a person to check."""
        return tuple(r for r in self.rows if r.resolved_by == identity.NORMALIZED)


def resolve_all(inventory: Inventory, sources: Sources, aliases: Aliases) -> Identity:
    """Resolve the inventory and the three benchmark sources against one
    models.dev snapshot. Raises ConfigError for an alias whose canonical is
    neither a models.dev key nor anchored by a source id."""
    modelsdev = sources.modelsdev.data
    aliases_mod.check_anchored(aliases, (r.canonical for r in modelsdev.rows if r.canonical))
    index = identity.build_index(modelsdev, aliases)
    data = {
        "openrouter": sources.openrouter.data,
        "arena": sources.arena.data,
        "epoch": sources.epoch.data,
    }
    present = {name: frozenset(r.source_id for r in records) for name, records in data.items()}
    return Identity(
        index=index,
        rows=tuple(identity.resolve_row(row, index, aliases) for row in inventory.rows),
        openrouter=identity.resolve_source("openrouter", data["openrouter"], index, aliases),
        arena=identity.resolve_source("arena", data["arena"], index, aliases),
        epoch=identity.resolve_source("epoch", data["epoch"], index, aliases),
        warnings=tuple(aliases_mod.stale_ids(aliases, present)),
    )


@dataclass(frozen=True)
class ModelResult:
    """One resolved or ignored inventory row, with its tier."""

    identity: RowIdentity
    evidence: Evidence
    facts: Facts
    verdict: Verdict


@dataclass(frozen=True)
class Candidate:
    """A key with at least one vote and no inventory row."""

    canonical: str  # in the stored spelling
    resolved_by: str  # the earliest of alias, host, normalized among its records
    open_weights: bool | None  # None: the key has no models.dev rows
    evidence: Evidence
    facts: Facts
    verdict: Verdict


@dataclass(frozen=True)
class Assignment:
    """Everything `tiers.toml` is built from."""

    identity: Identity
    contracts: Contracts
    models: tuple[ModelResult, ...]  # resolved and ignored rows, in wt's order
    candidates: tuple[Candidate, ...]  # rank descending, then key
    unresolved: tuple[ScoreRecord, ...]  # at or above a standard floor; source, then score
    sentinels: tuple[SentinelResult, ...]

    @property
    def sentinels_ok(self) -> bool:
        return all(result.ok for result in self.sentinels)


_NO_EVIDENCE = Evidence(
    votes={}, scores={}, efforts={}, base=UNRATED, pre_gate=UNRATED, confidence="none"
)
_RESOLUTION_ORDER = (identity.ALIAS, identity.HOST, identity.NORMALIZED)


def _matches(result: Identity, key: str, contracts: Contracts) -> dict[str, Match]:
    """The key's best record per signal, for the signals whose source has one."""
    by_source = {source.source: source for source in result.sources()}
    found: dict[str, Match] = {}
    for name, signal in contracts.signals.items():
        match = by_source[signal.source].best.get(key.lower())
        if match is not None:
            found[name] = match
    return found


def _evidence(result: Identity, key: str, contracts: Contracts) -> Evidence:
    matches = _matches(result, key, contracts)
    return assign.weigh({name: match.record for name, match in matches.items()}, contracts)


def _model(entry: RowIdentity, result: Identity, contracts: Contracts) -> ModelResult:
    if entry.canonical is None:
        # Listed under [[ignore]]: unrated, and no gate has anything to read.
        return ModelResult(
            identity=entry,
            evidence=_NO_EVIDENCE,
            facts=Facts(tool_call=None, context=None),
            verdict=Verdict(UNRATED, assign.NOT_APPLICABLE, (f"ignored: {entry.ignore_reason}",)),
        )
    evidence = _evidence(result, entry.canonical, contracts)
    facts = assign.gather(
        result.index.rows_for(entry.canonical),
        location=entry.row.location,
        host_row=entry.host_row,
    )
    return ModelResult(
        identity=entry,
        evidence=evidence,
        facts=facts,
        verdict=assign.decide(evidence, facts, contracts),
    )


def _candidates(result: Identity, contracts: Contracts) -> tuple[Candidate, ...]:
    held = {entry.canonical.lower() for entry in result.rows if entry.canonical is not None}
    keys: dict[str, str] = {}
    for source in result.sources():
        for lower, match in source.best.items():
            if lower not in held:
                keys.setdefault(lower, match.canonical)
    found: list[Candidate] = []
    for key in keys.values():
        matches = _matches(result, key, contracts)
        steps = {match.resolved_by for match in matches.values()}
        evidence = assign.weigh({name: m.record for name, m in matches.items()}, contracts)
        rows = result.index.rows_for(key)
        facts = assign.gather(rows, location=None, host_row=None)
        found.append(
            Candidate(
                canonical=key,
                resolved_by=next(step for step in _RESOLUTION_ORDER if step in steps),
                open_weights=any(row.open_weights for row in rows) if rows else None,
                evidence=evidence,
                facts=facts,
                verdict=assign.decide(evidence, facts, contracts),
            )
        )
    found.sort(key=lambda c: (-RANK[c.verdict.tier], c.canonical.lower()))
    return tuple(found)


def _unresolved(result: Identity, contracts: Contracts) -> tuple[ScoreRecord, ...]:
    """Unresolved records at or above their signal's standard floor: the best
    record of each id, in source order and then by score, highest first."""
    floors = {signal.source: signal.floors["standard"] for signal in contracts.signals.values()}
    listed: list[ScoreRecord] = []
    for source in result.sources():
        best: dict[str, ScoreRecord] = {}
        for record in source.unresolved:
            if record.score < floors[source.source]:
                continue
            if record.source_id not in best or record.score > best[record.source_id].score:
                best[record.source_id] = record
        listed.extend(sorted(best.values(), key=lambda record: -record.score))
    return tuple(listed)


def _sentinels(result: Identity, contracts: Contracts) -> tuple[SentinelResult, ...]:
    checked: list[SentinelResult] = []
    for number, sentinel in enumerate(contracts.sentinels, start=1):
        key = result.index.spelling(sentinel.canonical)
        if key is None:
            raise ConfigError(
                f"{contracts.origin}: [[sentinels]] #{number} ({sentinel.canonical}): "
                "not a canonical key in this run's models.dev data or in aliases.toml"
            )
        checked.append(assign.check_sentinel(sentinel, key, _evidence(result, key, contracts)))
    return tuple(checked)


def assign_all(result: Identity, contracts: Contracts) -> Assignment:
    """Give every resolved key its tier. Raises ConfigError for a sentinel
    whose canonical is not a known key. A failed sentinel is not an error
    here: it is in `Assignment.sentinels`, and the caller writes nothing."""
    sentinels = _sentinels(result, contracts)
    return Assignment(
        identity=result,
        contracts=contracts,
        models=tuple(
            _model(entry, result, contracts) for entry in result.rows if not entry.unmapped
        ),
        candidates=_candidates(result, contracts),
        unresolved=_unresolved(result, contracts),
        sentinels=sentinels,
    )
```

- [ ] **Step 7: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_pipeline.py -q`
Expected: PASS, `21 passed`.

- [ ] **Step 8: Commit**

```bash
git add modeltiers/src/modeltiers/pipeline.py modeltiers/tests/conftest.py modeltiers/tests/test_pipeline.py modeltiers/tests/fixtures/modelsdev.json modeltiers/tests/fixtures/openrouter.json modeltiers/tests/fixtures/arena_page1.json modeltiers/tests/fixtures/arena_page2.json modeltiers/tests/fixtures/eci_scores.csv modeltiers/tests/test_source_modelsdev.py modeltiers/tests/test_source_openrouter.py modeltiers/tests/test_source_arena.py modeltiers/tests/test_source_epoch.py modeltiers/tests/test_loader.py modeltiers/tests/test_cli.py
git commit -m "feat(modeltiers): assign a tier to every resolved row and every scored key"
```

---

### Task 5: The output document, written atomically

**Files:**
- Create: `modeltiers/src/modeltiers/output.py`, `docs/contracts/tiers.sample.toml`
- Modify: `modeltiers/src/modeltiers/errors.py`, `modeltiers/tests/conftest.py`
- Test: `modeltiers/tests/test_output.py`

**Interfaces:**
- Consumes: `modeltiers.pipeline.Assignment`, `ModelResult`, `Candidate` (Task 4); `modeltiers.assign.Evidence`, `Facts`, `Verdict` (Tasks 2 and 3); `modeltiers.contracts.Contracts`, `STAMP` (Task 1); `modeltiers.loader.Sources` (step 1, Task 7); `modeltiers.records.Inventory` (step 1, Task 2); `tomli_w.dumps`.
- Produces:
  - `modeltiers.errors.OutputFileError(ModelTiersError)`.
  - In `modeltiers.output`: `ARRAYS = ("models", "candidates", "unmapped", "unresolved")`, `Document = dict[str, Any]`. Timestamps are formatted with `contracts.STAMP`.
  - `build_document(assignment: Assignment, inventory: Inventory, sources: Sources, now: datetime) -> Document`.
  - `render(doc: Document) -> str`. `tomllib.loads(render(doc)) == doc`.
  - `write_document(path: Path, doc: Document) -> None`. Creates the directory when it is missing.
  - `read_document(path: Path) -> Document`. Raises `OutputFileError` for a missing file and for one that is not TOML.
  - `read_previous(path: Path) -> Document | None`. `None` for a missing file and for one that is not TOML.
  - In `tests/conftest.py`: `SAMPLE: Path` (`docs/contracts/tiers.sample.toml`), `without_timestamps(text: str) -> str`, and the fixture `document -> dict[str, Any]` (`build_document(assignment, inventory, sources, T0)`).
  - The document, in the order written: `generated_at`, `contracts_version`, `[inventory]` (`registry`, `providers`), `[sources.<name>]` (`status`, `fetched_at`, `records`, `unresolved` except under `modelsdev`, `attribution`), `[[models]]`, `[[candidates]]`, `[[unmapped]]`, `[[unresolved]]`.

- [ ] **Step 1: Write the contract sample**

This is the file `run` must produce from the golden fixtures with the fixture `aliases.toml` and `contracts.toml`, generated at `T0`. It shows a rated cloud row, a two-vote row, a local row rated through inheritance (`basis = "inherited"` beside `tier = "standard"`, one vote, `tool_call` from the key, no `context`), a local row with no votes, a row matched by name past a host row with no canonical id, an ignored row, a discovered row (`registered = false`), a `marathon` and a `deep` candidate, one unmapped row and one unresolved record. Like `docs/contracts/registry.written.sample.toml` it holds no comments, because it is exactly what the writer gives.

Write `docs/contracts/tiers.sample.toml`:

```toml
generated_at = "2026-10-12T15:58:00Z"
contracts_version = "fixture-1"

[inventory]
registry = "/home/owner/.config/local-ai/registry.toml"

[inventory.providers]
mtplx = "partial"
ollama = "ok"
omlx = "ok"

[sources.modelsdev]
status = "ok"
fetched_at = "2026-10-12T15:58:00Z"
records = 12
attribution = "models.dev (https://models.dev), MIT"

[sources.openrouter]
status = "ok"
fetched_at = "2026-10-12T15:58:00Z"
records = 6
unresolved = 1
attribution = "Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter (openrouter.ai/rankings)."

[sources.arena]
status = "ok"
fetched_at = "2026-10-12T15:58:00Z"
records = 6
unresolved = 1
attribution = "Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0"

[sources.epoch]
status = "ok"
fetched_at = "2026-10-12T15:58:00Z"
records = 6
unresolved = 0
attribution = "Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0"

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

[[models]]
provider_id = "ollama"
model_name = "deepseek-v4-pro:cloud"
registered = true
canonical = "deepseek/deepseek-v4-pro-0813"
resolved_by = "alias"
tier = "standard"
code = "S"
confidence = "high"
basis = "measured"
tool_call = true
context = 1048576
notes = []

[models.votes]
arena_agent = "standard"
eci = "standard"

[models.efforts]
arena_agent = "high"

[models.scores]
arena_agent = 0.013425874696272922
arena_agent_ci_lower = -0.0031749129284354416
eci = 155.31

[[models]]
provider_id = "openrouter"
model_name = "z-ai/glm-5.3"
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

[[models]]
provider_id = "ollama"
model_name = "qwen3.8:27b-mlx"
registered = true
canonical = "alibaba/qwen3.8-27b"
resolved_by = "normalized"
tier = "standard"
code = "S"
confidence = "low"
basis = "inherited"
tool_call = true
notes = [
    "local build: served context unknown; context gate skipped",
]

[models.votes]
intelligence_index = "standard"

[models.efforts]

[models.scores]
intelligence_index = 33.7
coding_index = 68.1
agentic_index = 45.8

[[models]]
provider_id = "ollama"
model_name = "minimax-m3:cloud"
registered = true
canonical = "minimax/MiniMax-M3"
resolved_by = "normalized"
tier = "quick"
code = "Q"
confidence = "high"
basis = "measured"
tool_call = true
context = 512000
notes = [
    "host row has no canonical id; matched by name",
]

[models.votes]
arena_agent = "quick"
eci = "quick"

[models.efforts]

[models.scores]
arena_agent = -0.06954648731246331
arena_agent_ci_lower = -0.07855233102193303
eci = 146.95

[[models]]
provider_id = "ollama"
model_name = "nomic-embed-text:latest"
registered = true
tier = "unrated"
code = "-"
reason = "not_applicable"
confidence = "none"
basis = "inherited"
notes = [
    "ignored: embedding model",
]

[models.votes]

[models.efforts]

[models.scores]

[[models]]
provider_id = "ollama"
model_name = "gemma4:31b-mlx"
registered = false
canonical = "google/gemma-4-31b-it"
resolved_by = "normalized"
tier = "unrated"
code = "-"
reason = "no_votes"
confidence = "none"
basis = "inherited"
tool_call = true
notes = [
    "local build: served context unknown; context gate skipped",
]

[models.votes]

[models.efforts]

[models.scores]

[[candidates]]
canonical = "anthropic/claude-opus-5-5"
resolved_by = "host"
tier = "marathon"
code = "M"
confidence = "high"
open_weights = false
tool_call = true
context = 1000000
notes = [
    "context is the largest among the key's models.dev rows",
]

[candidates.votes]
intelligence_index = "deep"
arena_agent = "deep"
eci = "deep"

[candidates.efforts]
arena_agent = "high"

[candidates.scores]
intelligence_index = 57.6
arena_agent = 0.14331280058415052
arena_agent_ci_lower = 0.12150663912969831
eci = 167.33

[[candidates]]
canonical = "anthropic/claude-fable-5"
resolved_by = "host"
tier = "deep"
code = "D"
confidence = "high"
open_weights = false
tool_call = true
context = 1000000
notes = [
    "context is the largest among the key's models.dev rows",
]

[candidates.votes]
intelligence_index = "deep"
arena_agent = "deep"
eci = "deep"

[candidates.efforts]
arena_agent = "high"

[candidates.scores]
intelligence_index = 49.6
coding_index = 76.5
agentic_index = 50.7
arena_agent = 0.08937936055727591
arena_agent_ci_lower = 0.0766906485538394
eci = 162.06

[[unmapped]]
provider_id = "ollama"
model_name = "medgemma:27b"

[[unresolved]]
source = "arena"
id = "Qwen3.8 Max"
score = 0.023467932612357068
```

- [ ] **Step 2: Write the failing tests**

`SAMPLE`, `without_timestamps` and the `document` fixture go in `tests/conftest.py`; Task 9's end-to-end test uses them too.

In `modeltiers/tests/conftest.py`, replace:

```python
import io
```

with:

```python
import io
import re
```

In `modeltiers/tests/conftest.py`, replace:

```python
from modeltiers import fetch, loader, pipeline
```

with:

```python
from modeltiers import fetch, loader, output, pipeline
```

In `modeltiers/tests/conftest.py`, replace:

```python
T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
```

with:

```python
T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
# The output contract: what `run` writes for the golden fixtures.
SAMPLE = Path(__file__).parents[2] / "docs" / "contracts" / "tiers.sample.toml"
_STAMPS = re.compile(r'^(generated_at|fetched_at) = "[^"]*"$', re.MULTILINE)


def without_timestamps(text: str) -> str:
    """`tiers.toml` text with every timestamp replaced by one placeholder."""
    return _STAMPS.sub(r'\1 = "<time>"', text)
```

Append to the end of `modeltiers/tests/conftest.py`, after two blank lines:

```python
@pytest.fixture
def document(assignment: Assignment, inventory: Inventory, sources: Sources) -> dict[str, Any]:
    """`tiers.toml` for the golden fixtures, generated at T0, as a dict."""
    return output.build_document(assignment, inventory, sources, T0)
```

Write `modeltiers/tests/test_output.py`:

```python
import re
import tomllib
from dataclasses import replace
from datetime import timedelta
from pathlib import Path
from typing import Any

import pytest
from conftest import SAMPLE, T0, without_timestamps

from modeltiers import aliases as aliases_mod
from modeltiers import output, pipeline
from modeltiers.aliases import Alias
from modeltiers.contracts import Contracts
from modeltiers.errors import OutputFileError
from modeltiers.loader import Sources
from modeltiers.pipeline import Assignment
from modeltiers.records import Inventory


def test_the_document_is_the_contract_sample_apart_from_timestamps(
    document: dict[str, Any],
) -> None:
    assert without_timestamps(output.render(document)) == without_timestamps(SAMPLE.read_text())


def test_the_rendered_text_reads_back_as_the_same_document(document: dict[str, Any]) -> None:
    assert tomllib.loads(output.render(document)) == document


def test_tables_are_written_in_the_specs_order(document: dict[str, Any]) -> None:
    assert list(document) == [
        "generated_at",
        "contracts_version",
        "inventory",
        "sources",
        "models",
        "candidates",
        "unmapped",
        "unresolved",
    ]
    headers = re.findall(r"^\[+[a-z]+", output.render(document), re.MULTILINE)
    assert list(dict.fromkeys(headers)) == [
        "[inventory",
        "[sources",
        "[[models",
        "[models",
        "[[candidates",
        "[candidates",
        "[[unmapped",
        "[[unresolved",
    ]
    assert list(document["sources"]) == ["modelsdev", "openrouter", "arena", "epoch"]


def test_the_head_of_the_file(document: dict[str, Any]) -> None:
    assert document["generated_at"] == "2026-10-12T15:58:00Z"
    assert document["contracts_version"] == "fixture-1"
    assert document["inventory"] == {
        "registry": "/home/owner/.config/local-ai/registry.toml",
        "providers": {"mtplx": "partial", "ollama": "ok", "omlx": "ok"},
    }
    assert document["sources"]["arena"] == {
        "status": "ok",
        "fetched_at": "2026-10-12T15:58:00Z",
        "records": 6,
        "unresolved": 1,
        "attribution": "Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0",
    }
    # models.dev is the identity spine: nothing of it is "unresolved".
    assert "unresolved" not in document["sources"]["modelsdev"]


def test_a_rated_model_entry_has_every_field(document: dict[str, Any]) -> None:
    assert document["models"][0] == {
        "provider_id": "ollama",
        "model_name": "glm-5.3:cloud",
        "registered": True,
        "canonical": "zhipuai/glm-5.3",
        "resolved_by": "host",
        "tier": "standard",
        "code": "S",
        "confidence": "high",
        "basis": "measured",
        "tool_call": True,
        "context": 1048576,
        "votes": {
            "intelligence_index": "standard",
            "arena_agent": "standard",
            "eci": "standard",
        },
        "efforts": {"arena_agent": "max"},
        "notes": [],
        "scores": {
            "intelligence_index": 44.8,
            "coding_index": 74.8,
            "agentic_index": 53.1,
            "arena_agent": 0.02147446416824893,
            "arena_agent_ci_lower": 0.014306504009895325,
            "eci": 155.61,
        },
    }
    assert list(document["models"][0]) == [
        "provider_id",
        "model_name",
        "registered",
        "canonical",
        "resolved_by",
        "tier",
        "code",
        "confidence",
        "basis",
        "tool_call",
        "context",
        "votes",
        "efforts",
        "notes",
        "scores",
    ]


def test_optional_fields_are_absent_when_unknown(document: dict[str, Any]) -> None:
    by_name = {m["model_name"]: m for m in document["models"]}
    # A score the source did not give, an effort the record did not name.
    assert set(by_name["deepseek-v4-pro:cloud"]["scores"]) == {
        "arena_agent",
        "arena_agent_ci_lower",
        "eci",
    }
    assert by_name["minimax-m3:cloud"]["efforts"] == {}
    # reason only on an unrated entry; context absent for a local build.
    assert "reason" not in by_name["glm-5.3:cloud"]
    local = by_name["gemma4:31b-mlx"]
    assert (local["tier"], local["code"], local["reason"]) == ("unrated", "-", "no_votes")
    assert (local["basis"], local["tool_call"]) == ("inherited", True)
    assert "context" not in local
    # The identity note comes before the gate's.
    assert by_name["minimax-m3:cloud"]["notes"] == ["host row has no canonical id; matched by name"]
    assert by_name["gemma4:31b-mlx"]["registered"] is False


def test_a_local_build_is_written_with_its_keys_tier_votes_and_tool_call(
    document: dict[str, Any],
) -> None:
    """A local row is rated through inheritance: `basis = "inherited"` beside
    a rated tier, with the key's votes, scores and tool-calling flag."""
    local = next(m for m in document["models"] if m["model_name"] == "qwen3.8:27b-mlx")
    assert local == {
        "provider_id": "ollama",
        "model_name": "qwen3.8:27b-mlx",
        "registered": True,
        "canonical": "alibaba/qwen3.8-27b",
        "resolved_by": "normalized",
        "tier": "standard",
        "code": "S",
        "confidence": "low",
        "basis": "inherited",
        "tool_call": True,
        "votes": {"intelligence_index": "standard"},
        "efforts": {},
        "notes": ["local build: served context unknown; context gate skipped"],
        "scores": {"intelligence_index": 33.7, "coding_index": 68.1, "agentic_index": 45.8},
    }
    # The contract sample wt reads holds the same entry.
    sample = tomllib.loads(SAMPLE.read_text())
    assert next(m for m in sample["models"] if m["model_name"] == "qwen3.8:27b-mlx") == local
    # Every other inherited entry of the fixtures is unrated.
    assert [
        (m["model_name"], m["tier"]) for m in sample["models"] if m["basis"] == "inherited"
    ] == [
        ("qwen3.8:27b-mlx", "standard"),
        ("nomic-embed-text:latest", "unrated"),
        ("gemma4:31b-mlx", "unrated"),
    ]


def test_an_ignored_row_is_in_models_as_not_applicable(document: dict[str, Any]) -> None:
    ignored = next(m for m in document["models"] if m["model_name"] == "nomic-embed-text:latest")
    assert ignored == {
        "provider_id": "ollama",
        "model_name": "nomic-embed-text:latest",
        "registered": True,
        "tier": "unrated",
        "code": "-",
        "reason": "not_applicable",
        "confidence": "none",
        "basis": "inherited",
        "votes": {},
        "efforts": {},
        "notes": ["ignored: embedding model"],
        "scores": {},
    }
    assert document["unmapped"] == [{"provider_id": "ollama", "model_name": "medgemma:27b"}]


def test_a_candidate_has_open_weights_and_no_inventory_fields(document: dict[str, Any]) -> None:
    opus = document["candidates"][0]
    assert list(opus) == [
        "canonical",
        "resolved_by",
        "tier",
        "code",
        "confidence",
        "open_weights",
        "tool_call",
        "context",
        "votes",
        "efforts",
        "notes",
        "scores",
    ]
    assert (opus["canonical"], opus["tier"], opus["code"]) == (
        "anthropic/claude-opus-5-5",
        "marathon",
        "M",
    )
    assert (opus["open_weights"], opus["context"]) == (False, 1000000)
    assert [c["canonical"] for c in document["candidates"]] == [
        "anthropic/claude-opus-5-5",
        "anthropic/claude-fable-5",
    ]
    assert document["unresolved"] == [
        {"source": "arena", "id": "Qwen3.8 Max", "score": 0.023467932612357068}
    ]


def test_a_key_with_no_models_dev_rows_has_no_tool_call_or_open_weights(
    inventory: Inventory, sources: Sources, rule: Contracts
) -> None:
    declared = aliases_mod.build([Alias("alibaba/qwen3.8-max", arena=("Qwen3.8 Max",))], [])
    assignment = pipeline.assign_all(pipeline.resolve_all(inventory, sources, declared), rule)
    document = output.build_document(assignment, inventory, sources, T0)
    (entry,) = [c for c in document["candidates"] if c["canonical"] == "alibaba/qwen3.8-max"]
    assert not {"tool_call", "open_weights", "context"} & set(entry)
    # Nothing unresolved is left at or above a floor: the array is absent.
    assert "unresolved" not in document
    assert "[[unresolved]]" not in output.render(document)


def test_a_stale_source_keeps_its_snapshots_own_time(
    assignment: Assignment, inventory: Inventory, sources: Sources
) -> None:
    stale = replace(sources, arena=replace(sources.arena, status="stale", fallback=True))
    document = output.build_document(assignment, inventory, stale, T0 + timedelta(days=7))
    assert document["generated_at"] == "2026-10-19T15:58:00Z"
    assert document["sources"]["arena"]["status"] == "stale"
    assert document["sources"]["arena"]["fetched_at"] == "2026-10-12T15:58:00Z"


def test_write_then_read(document: dict[str, Any], tmp_path: Path) -> None:
    path = tmp_path / "new" / "dir" / "tiers.toml"
    output.write_document(path, document)
    assert output.read_document(path) == document
    assert [p.name for p in path.parent.iterdir()] == ["tiers.toml"]  # no temporary file left


def test_a_failed_write_leaves_the_old_file_and_no_temporary_file(
    document: dict[str, Any], tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "tiers.toml"
    path.write_text('generated_at = "old"\n')

    def broken(doc: dict[str, Any]) -> str:
        raise RuntimeError("disk full")

    monkeypatch.setattr(output, "render", broken)
    with pytest.raises(RuntimeError, match="disk full"):
        output.write_document(path, document)
    assert path.read_text() == 'generated_at = "old"\n'
    assert [p.name for p in tmp_path.iterdir() if p.name.startswith("tiers.toml")] == ["tiers.toml"]


def test_reading_a_missing_file_names_the_run_command(tmp_path: Path) -> None:
    with pytest.raises(OutputFileError, match="cannot read .*tiers.toml.*run `modeltiers run`"):
        output.read_document(tmp_path / "tiers.toml")
    assert output.read_previous(tmp_path / "tiers.toml") is None


def test_a_file_that_is_not_toml_reads_as_absent_for_the_change_report(tmp_path: Path) -> None:
    path = tmp_path / "tiers.toml"
    path.write_text("generated_at = \n")
    with pytest.raises(OutputFileError, match="is not TOML"):
        output.read_document(path)
    assert output.read_previous(path) is None
    # Bytes that are not UTF-8 are the same case, not a UnicodeDecodeError.
    path.write_bytes(b'generated_at = "\xff\xfe"\n')
    with pytest.raises(OutputFileError, match="is not TOML"):
        output.read_document(path)
    assert output.read_previous(path) is None
```

- [ ] **Step 3: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_output.py -q`
Expected: FAIL while loading `tests/conftest.py`, with `ImportError: cannot import name 'output' from 'modeltiers'`.

- [ ] **Step 4: Add the error and write the module**

Append to the end of `modeltiers/src/modeltiers/errors.py`, after two blank lines:

```python
class OutputFileError(ModelTiersError):
    """`tiers.toml` is missing or unreadable when `show` or `explain` needs it."""
```

Write `modeltiers/src/modeltiers/output.py`:

```python
"""`tiers.toml`: build the document, write it atomically, read it back.

The document is a plain dict in the order it is written. Values are formatted
by `tomli_w`; `render` lays the file out itself so that every entry of the
four arrays is its own `[[models]]`-style block, in the spec's table order,
whatever its length.
"""

import os
import tempfile
import tomllib
from datetime import datetime
from pathlib import Path
from typing import Any

import tomli_w

from modeltiers.assign import Evidence, Facts, Verdict
from modeltiers.contracts import STAMP, Contracts
from modeltiers.errors import OutputFileError
from modeltiers.loader import Sources
from modeltiers.pipeline import Assignment, Candidate, ModelResult
from modeltiers.records import Inventory

ARRAYS = ("models", "candidates", "unmapped", "unresolved")  # in the order written

Document = dict[str, Any]


def _rating(
    evidence: Evidence, facts: Facts, verdict: Verdict, contracts: Contracts, **extra: Any
) -> Document:
    """The fields a `[[models]]` and a `[[candidates]]` entry share, from
    `tier` on. `extra` (basis, open_weights) goes after `confidence`. A field
    whose value is unknown is left out: `reason` on a rated entry, `tool_call`
    for a key with no models.dev rows, `context` when no row gives one."""
    entry: Document = {"tier": verdict.tier, "code": contracts.code(verdict.tier)}
    if verdict.reason is not None:
        entry["reason"] = verdict.reason
    entry["confidence"] = evidence.confidence
    entry.update({key: value for key, value in extra.items() if value is not None})
    if facts.tool_call is not None:
        entry["tool_call"] = facts.tool_call
    if facts.context is not None:
        entry["context"] = facts.context
    entry["votes"] = dict(evidence.votes)
    entry["efforts"] = dict(evidence.efforts)
    entry["notes"] = list(verdict.notes)
    entry["scores"] = dict(evidence.scores)
    return entry


def _model(model: ModelResult, contracts: Contracts) -> Document:
    found = model.identity
    entry: Document = {
        "provider_id": found.row.provider_id,
        "model_name": found.row.model_name,
        "registered": found.row.registered,
    }
    if found.canonical is not None:  # an ignored row has neither
        entry["canonical"] = found.canonical
        entry["resolved_by"] = found.resolved_by
    notes = (*found.notes, *model.verdict.notes)
    rating = _rating(model.evidence, model.facts, model.verdict, contracts, basis=found.basis)
    return {**entry, **rating, "notes": list(notes)}


def _candidate(candidate: Candidate, contracts: Contracts) -> Document:
    return {
        "canonical": candidate.canonical,
        "resolved_by": candidate.resolved_by,
        **_rating(
            candidate.evidence,
            candidate.facts,
            candidate.verdict,
            contracts,
            open_weights=candidate.open_weights,
        ),
    }


def _sources(assignment: Assignment, sources: Sources) -> Document:
    unresolved = {s.source: len(s.unresolved) for s in assignment.identity.sources()}
    tables: Document = {}
    for result in sources.results():
        table: Document = {
            "status": result.status,
            "fetched_at": result.fetched_at.strftime(STAMP),
            "records": result.records,
        }
        if result.name in unresolved:  # models.dev is not resolved against anything
            table["unresolved"] = unresolved[result.name]
        table["attribution"] = assignment.contracts.attribution[result.name]
        tables[result.name] = table
    return tables


def build_document(
    assignment: Assignment, inventory: Inventory, sources: Sources, now: datetime
) -> Document:
    """The whole of `tiers.toml`. An array with no entries is left out: an
    absent `[[unmapped]]` is an empty one."""
    contracts = assignment.contracts
    doc: Document = {
        "generated_at": now.strftime(STAMP),
        "contracts_version": contracts.version,
        "inventory": {"registry": inventory.registry, "providers": dict(inventory.providers)},
        "sources": _sources(assignment, sources),
    }
    arrays: Document = {
        "models": [_model(model, contracts) for model in assignment.models],
        "candidates": [_candidate(c, contracts) for c in assignment.candidates],
        "unmapped": [
            {"provider_id": entry.row.provider_id, "model_name": entry.row.model_name}
            for entry in assignment.identity.unmapped
        ],
        "unresolved": [
            {"source": record.source, "id": record.source_id, "score": record.score}
            for record in assignment.unresolved
        ],
    }
    doc.update({name: entries for name, entries in arrays.items() if entries})
    return doc


def render(doc: Document) -> str:
    """The document as TOML text: the plain keys, `[inventory]` and
    `[sources.*]` first, then one block per entry of each array in ARRAYS. A
    table inside an entry (`votes`, `efforts`, `scores`) follows the entry's
    plain values as `[models.votes]`; an empty one is a bare header."""
    parts = [tomli_w.dumps({key: value for key, value in doc.items() if key not in ARRAYS})]
    for name in ARRAYS:
        for entry in doc.get(name, []):
            plain = {key: value for key, value in entry.items() if not isinstance(value, dict)}
            parts.append(f"[[{name}]]\n{tomli_w.dumps(plain)}")
            parts.extend(
                f"[{name}.{key}]\n{tomli_w.dumps(value)}"
                for key, value in entry.items()
                if isinstance(value, dict)
            )
    return "\n".join(parts)


def write_document(path: Path, doc: Document) -> None:
    """Write the document atomically: a temporary file in the same directory,
    then a rename over `path`. A reader sees the old file or the new one."""
    path.parent.mkdir(parents=True, exist_ok=True)
    handle, name = tempfile.mkstemp(dir=path.parent, prefix=path.name + ".", suffix=".tmp")
    temporary = Path(name)
    try:
        with os.fdopen(handle, "w", encoding="utf-8") as stream:
            stream.write(render(doc))
        temporary.replace(path)
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


def read_document(path: Path) -> Document:
    """Read `tiers.toml` for `show` and `explain`. Raises OutputFileError when
    it is missing or is not TOML."""
    try:
        data = path.read_bytes()
    except OSError as exc:
        raise OutputFileError(
            f"cannot read {path}: {exc.strerror}; run `modeltiers run` to write it"
        ) from exc
    try:
        # Decoded here, not by read_text: bytes that are not UTF-8 are a file
        # that is not TOML, and must not reach `run` as a UnicodeDecodeError.
        return tomllib.loads(data.decode("utf-8"))
    except (UnicodeDecodeError, tomllib.TOMLDecodeError) as exc:
        raise OutputFileError(
            f"{path} is not TOML ({exc}); run `modeltiers run` to write it again"
        ) from exc


def read_previous(path: Path) -> Document | None:
    """The existing file, for the change report. The file is advisory: one
    that is absent or cannot be parsed reads as absent."""
    try:
        return read_document(path)
    except OutputFileError:
        return None
```

- [ ] **Step 5: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_output.py -q`
Expected: PASS, `15 passed`.

- [ ] **Step 6: Commit**

```bash
git add modeltiers/src/modeltiers/output.py modeltiers/src/modeltiers/errors.py modeltiers/tests/test_output.py modeltiers/tests/conftest.py docs/contracts/tiers.sample.toml
git commit -m "feat(modeltiers): build tiers.toml, write it atomically and pin its format with a sample"
```

---

### Task 6: What `show` and `explain` print

**Files:**
- Create: `modeltiers/src/modeltiers/show.py`
- Test: `modeltiers/tests/test_show.py`

**Interfaces:**
- Consumes: `modeltiers.contracts.SOURCES`, `STAMP` (Task 1); the `document` fixture and `T0` (Tasks 4 and 5).
- Produces, in `modeltiers.show`, every function taking the document as a `Mapping[str, Any]`:
  - `STALE_AFTER_DAYS = 14`.
  - `model_name(entry: Mapping[str, Any]) -> str`: `provider_id/model_name`.
  - `age_days(doc, now: datetime) -> int | None`; `age_line(doc, now: datetime) -> str`; `stale_warning(doc, now: datetime) -> str | None`.
  - `table_lines(doc, *, candidates: bool = False) -> list[str]`.
  - `attribution_lines(doc, sources: Iterable[str] = SOURCES) -> list[str]`.
  - `short(value: Any) -> str`: a float to at most four decimal places.
  - `explain_lines(doc, name: str) -> list[str] | None`. `None` when nothing matches.

- [ ] **Step 1: Write the failing test**

Write `modeltiers/tests/test_show.py`:

```python
import ast
import copy
from datetime import timedelta
from pathlib import Path
from typing import Any

from conftest import T0

from modeltiers import show

ATTRIBUTION = [
    "modelsdev: models.dev (https://models.dev), MIT",
    "openrouter: Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter "
    "(openrouter.ai/rankings).",
    "arena: Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0",
    "epoch: Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0",
]

TABLE = [
    "T  tier      conf    model                           canonical",
    "S  standard  high    ollama/glm-5.3:cloud            zhipuai/glm-5.3",
    "S  standard  high    ollama/deepseek-v4-pro:cloud    deepseek/deepseek-v4-pro-0813",
    "S  standard  high    openrouter/z-ai/glm-5.3         zhipuai/glm-5.3",
    "S  standard  low     ollama/qwen3.8:27b-mlx          alibaba/qwen3.8-27b",
    "Q  quick     high    ollama/minimax-m3:cloud         minimax/MiniMax-M3",
    "-  unrated   none    ollama/nomic-embed-text:latest  -  (not_applicable)",
    "-  unrated   none    ollama/gemma4:31b-mlx           google/gemma-4-31b-it  (no_votes)",
    "unmapped: 1 (unrated until aliased or ignored)",
    "  ollama/medgemma:27b",
]


def test_the_table_lists_inventory_models_and_counts_the_candidates(
    document: dict[str, Any],
) -> None:
    assert show.table_lines(document) == [
        *TABLE,
        "candidates: 2 (list them with --candidates)",
    ]


def test_the_table_lists_candidates_when_asked(document: dict[str, Any]) -> None:
    assert show.table_lines(document, candidates=True) == [
        *TABLE,
        "candidates: 2 (scored by a source, not in the inventory)",
        "M  marathon  high    anthropic/claude-opus-5-5",
        "D  deep      high    anthropic/claude-fable-5",
    ]


def test_an_empty_file_still_prints_a_table() -> None:
    assert show.table_lines({}, candidates=True) == [
        "T  tier      conf    model  canonical",
        "unmapped: 0 (unrated until aliased or ignored)",
        "candidates: 0 (scored by a source, not in the inventory)",
    ]


def test_age_in_whole_days_and_the_warning_past_fourteen(document: dict[str, Any]) -> None:
    assert show.age_line(document, T0) == (
        "tiers.toml generated 2026-10-12T15:58:00Z (0 days old), contracts fixture-1"
    )
    fourteen = T0 + timedelta(days=14, hours=23)
    assert show.age_days(document, fourteen) == 14
    assert show.stale_warning(document, fourteen) is None
    fifteen = T0 + timedelta(days=15)
    assert "(15 days old)" in show.age_line(document, fifteen)
    assert show.stale_warning(document, fifteen) == (
        "tiers.toml is 15 days old; run `modeltiers run` to refresh it"
    )
    # A clock behind the file is not a negative age.
    assert show.age_days(document, T0 - timedelta(days=2)) == 0


def test_a_file_with_no_readable_date_has_no_age() -> None:
    for doc in ({}, {"generated_at": "last tuesday"}, {"generated_at": 20261012}):
        assert show.age_days(doc, T0) is None
        assert show.stale_warning(doc, T0) is None
    assert show.age_line({}, T0) == "tiers.toml generated ? (of unknown age), contracts ?"


def test_attribution_lines_for_every_source_or_for_some(document: dict[str, Any]) -> None:
    assert show.attribution_lines(document) == ATTRIBUTION
    assert show.attribution_lines(document, {"epoch", "modelsdev"}) == [
        ATTRIBUTION[0],
        ATTRIBUTION[3],
    ]
    # An older file without the strings prints none rather than failing.
    assert show.attribution_lines({"sources": {"arena": {"status": "ok"}}}) == []
    assert show.attribution_lines({}) == []


GLM_CLOUD = [
    "ollama/glm-5.3:cloud",
    "  identity:    zhipuai/glm-5.3, resolved by host",
    "  tier:        standard (S)",
    "  confidence:  high, basis measured",
    "  votes:       intelligence_index standard, arena_agent standard, eci standard",
    "  scores:      intelligence_index 44.8, coding_index 74.8, agentic_index 53.1, "
    "arena_agent 0.0215, arena_agent_ci_lower 0.0143, eci 155.61",
    "  efforts:     arena_agent max",
    "  tool_call:   true",
    "  context:     1048576",
]


def test_explain_by_exact_provider_and_model_name(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/glm-5.3:cloud") == [*GLM_CLOUD, *ATTRIBUTION]
    # The pair is exact: another case is not this row.
    assert show.explain_lines(document, "Ollama/GLM-5.3:cloud") is None


def test_explain_by_canonical_key_prints_every_model_that_resolved_to_it(
    document: dict[str, Any],
) -> None:
    lines = show.explain_lines(document, "ZhipuAI/GLM-5.3")
    assert lines is not None
    assert lines[:9] == GLM_CLOUD
    assert lines[9] == "openrouter/z-ai/glm-5.3"
    assert lines[-4:] == ATTRIBUTION
    assert len(lines) == 9 + 9 + 4


def test_a_pair_that_looks_like_a_key_is_tried_as_a_pair_first(document: dict[str, Any]) -> None:
    doc = copy.deepcopy(document)
    # An inventory row whose provider_id/model_name spells another row's key.
    doc["models"][4].update(provider_id="zhipuai", model_name="glm-5.3")
    lines = show.explain_lines(doc, "zhipuai/glm-5.3")
    assert lines is not None
    assert [line for line in lines if not line.startswith("  ")][0] == "zhipuai/glm-5.3"
    assert "  identity:    minimax/MiniMax-M3, resolved by normalized" in lines


def test_explain_a_candidate_by_its_key_in_any_case(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "anthropic/Claude-Opus-5-5") == [
        "candidate anthropic/claude-opus-5-5",
        "  identity:    anthropic/claude-opus-5-5, resolved by host",
        "  tier:        marathon (M)",
        "  confidence:  high",
        "  votes:       intelligence_index deep, arena_agent deep, eci deep",
        "  scores:      intelligence_index 57.6, arena_agent 0.1433, "
        "arena_agent_ci_lower 0.1215, eci 167.33",
        "  efforts:     arena_agent high",
        "  tool_call:   true",
        "  context:     1000000",
        "  open_weights: false",
        "  note:        context is the largest among the key's models.dev rows",
        *ATTRIBUTION,
    ]


def test_explain_shows_the_reason_the_notes_and_only_the_sources_used(
    document: dict[str, Any],
) -> None:
    # No votes: models.dev identified it and nothing scored it.
    assert show.explain_lines(document, "ollama/gemma4:31b-mlx") == [
        "ollama/gemma4:31b-mlx",
        "  identity:    google/gemma-4-31b-it, resolved by normalized",
        "  tier:        unrated (-), reason no_votes",
        "  confidence:  none, basis inherited",
        "  votes:       none",
        "  scores:      none",
        "  efforts:     none",
        "  tool_call:   true",
        "  context:     unknown",
        "  note:        local build: served context unknown; context gate skipped",
        ATTRIBUTION[0],
    ]
    # Two votes, neither from OpenRouter.
    deepseek = show.explain_lines(document, "ollama/deepseek-v4-pro:cloud")
    assert deepseek is not None
    assert deepseek[-3:] == [ATTRIBUTION[0], ATTRIBUTION[2], ATTRIBUTION[3]]


def test_explain_a_local_build_rated_through_inheritance(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/qwen3.8:27b-mlx") == [
        "ollama/qwen3.8:27b-mlx",
        "  identity:    alibaba/qwen3.8-27b, resolved by normalized",
        "  tier:        standard (S)",
        "  confidence:  low, basis inherited",
        "  votes:       intelligence_index standard",
        "  scores:      intelligence_index 33.7, coding_index 68.1, agentic_index 45.8",
        "  efforts:     none",
        "  tool_call:   true",
        "  context:     unknown",
        "  note:        local build: served context unknown; context gate skipped",
        ATTRIBUTION[0],
        ATTRIBUTION[1],
    ]


def test_explain_an_ignored_row(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/nomic-embed-text:latest") == [
        "ollama/nomic-embed-text:latest",
        "  identity:    not resolved (listed under [[ignore]])",
        "  tier:        unrated (-), reason not_applicable",
        "  confidence:  none, basis inherited",
        "  votes:       none",
        "  scores:      none",
        "  efforts:     none",
        "  tool_call:   unknown",
        "  context:     unknown",
        "  note:        ignored: embedding model",
    ]


def test_explain_an_unmapped_row_says_it_needs_an_alias(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/medgemma:27b") == [
        "ollama/medgemma:27b is unmapped: no alias, host row or name match gave it a "
        "canonical key, so it is unrated.",
        "Add an [[alias]] or an [[ignore]] for it in aliases.toml and run `modeltiers run`.",
    ]


def test_explain_with_no_match_is_none(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/absent:cloud") is None
    assert show.explain_lines(document, "") is None
    assert show.explain_lines({}, "zhipuai/glm-5.3") is None


def test_an_entry_from_an_older_file_prints_question_marks() -> None:
    old = {"models": [{"provider_id": "ollama", "model_name": "m", "canonical": "v/m"}]}
    assert show.table_lines(old)[1] == "?  ?         ?       ollama/m  v/m"
    assert show.explain_lines(old, "v/M") == [
        "ollama/m",
        "  identity:    v/m, resolved by ?",
        "  tier:        ? (?)",
        "  confidence:  ?",
        "  votes:       none",
        "  scores:      none",
        "  efforts:     none",
        "  tool_call:   unknown",
        "  context:     unknown",
    ]


def test_show_imports_nothing_of_the_package_but_the_constants() -> None:
    """`show` and `explain` work from the file alone: the module that prints
    them takes two constants from `contracts` and nothing from the pipeline,
    the sources or the writer."""
    assert show.__file__ is not None
    tree = ast.parse(Path(show.__file__).read_text())
    imported = {
        node.module
        for node in ast.walk(tree)
        if isinstance(node, ast.ImportFrom) and node.module and node.module.startswith("modeltiers")
    }
    assert imported == {"modeltiers.contracts"}
    assert not [
        alias.name
        for node in ast.walk(tree)
        if isinstance(node, ast.Import)
        for alias in node.names
        if alias.name.startswith("modeltiers")
    ]


def test_short_scores() -> None:
    assert [show.short(v) for v in (44.8, 0.02147446, -0.0695464, 150.0, 7, "x")] == [
        "44.8",
        "0.0215",
        "-0.0695",
        "150",
        "7",
        "x",
    ]
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_show.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'show' from 'modeltiers'`.

- [ ] **Step 3: Write the module**

Of the package this module imports two constants from `modeltiers.contracts` (`SOURCES`, `STAMP`) and nothing else: not the pipeline, the sources, the loader or the writer. The document is the only input, which is what lets `show` and `explain` work from the file alone; `test_show_imports_nothing_of_the_package_but_the_constants` reads the module's import statements and pins it. `_has_key` exists because an ignored row has no `canonical`: without the check an empty argument would match it.

Write `modeltiers/src/modeltiers/show.py`:

```python
"""What `show` and `explain` print. Everything here reads the output document
alone: a dict as `output.read_document` returns it. No source, no inventory,
no contracts file. A field an older file lacks prints as `?`."""

from collections.abc import Iterable, Mapping
from datetime import UTC, datetime
from typing import Any

from modeltiers.contracts import SOURCES, STAMP

STALE_AFTER_DAYS = 14

Document = Mapping[str, Any]

# Which source each score in an entry came from, for the attribution lines.
_SCORE_SOURCE: Mapping[str, str] = {
    "intelligence_index": "openrouter",
    "coding_index": "openrouter",
    "agentic_index": "openrouter",
    "arena_agent": "arena",
    "arena_agent_ci_lower": "arena",
    "eci": "epoch",
}


def model_name(entry: Mapping[str, Any]) -> str:
    """`provider_id/model_name`, the name a `[[models]]` or `[[unmapped]]`
    entry is listed and looked up by."""
    return f"{entry.get('provider_id', '?')}/{entry.get('model_name', '?')}"


def age_days(doc: Document, now: datetime) -> int | None:
    """Whole days since the file was generated; None when it does not say."""
    try:
        generated = datetime.strptime(str(doc.get("generated_at")), STAMP).replace(tzinfo=UTC)
    except ValueError:
        return None
    return max((now - generated).days, 0)


def age_line(doc: Document, now: datetime) -> str:
    days = age_days(doc, now)
    age = "of unknown age" if days is None else f"{days} days old"
    return (
        f"tiers.toml generated {doc.get('generated_at', '?')} ({age}), "
        f"contracts {doc.get('contracts_version', '?')}"
    )


def stale_warning(doc: Document, now: datetime) -> str | None:
    """The warning for a file past STALE_AFTER_DAYS, else None."""
    days = age_days(doc, now)
    if days is None or days <= STALE_AFTER_DAYS:
        return None
    return f"tiers.toml is {days} days old; run `modeltiers run` to refresh it"


def _row(entry: Mapping[str, Any], name: str, width: int, canonical: str | None) -> str:
    reason = f"  ({entry['reason']})" if "reason" in entry else ""
    tail = "" if canonical is None else f"  {canonical}"
    return (
        f"{entry.get('code', '?')}  {entry.get('tier', '?'):<8}  "
        f"{entry.get('confidence', '?'):<6}  {name:<{width}}{tail}{reason}".rstrip()
    )


def table_lines(doc: Document, *, candidates: bool = False) -> list[str]:
    """The tier table: one line per `[[models]]` entry in file order, the
    unmapped rows, and with `candidates` one line per `[[candidates]]` entry."""
    models = doc.get("models", [])
    unmapped = doc.get("unmapped", [])
    width = max([len(model_name(m)) for m in models] + [len("model")])
    lines = [f"T  {'tier':<8}  {'conf':<6}  {'model':<{width}}  canonical"]
    lines.extend(_row(m, model_name(m), width, m.get("canonical", "-")) for m in models)
    lines.append(f"unmapped: {len(unmapped)} (unrated until aliased or ignored)")
    lines.extend(f"  {model_name(entry)}" for entry in unmapped)
    found = doc.get("candidates", [])
    if candidates:
        lines.append(f"candidates: {len(found)} (scored by a source, not in the inventory)")
        lines.extend(_row(c, str(c.get("canonical", "?")), 0, None) for c in found)
    else:
        lines.append(f"candidates: {len(found)} (list them with --candidates)")
    return lines


def attribution_lines(doc: Document, sources: Iterable[str] = SOURCES) -> list[str]:
    """The attribution line of each named source the file has one for."""
    tables = doc.get("sources", {})
    wanted = set(sources)
    return [
        f"{name}: {tables[name]['attribution']}"
        for name in SOURCES
        if name in wanted and "attribution" in tables.get(name, {})
    ]


def short(value: Any) -> str:
    """A score for display: at most four decimal places, no trailing zeros."""
    if isinstance(value, float):
        return f"{value:.4f}".rstrip("0").rstrip(".")
    return str(value)


def _pairs(table: Mapping[str, Any]) -> str:
    return ", ".join(f"{key} {short(value)}" for key, value in table.items()) or "none"


def _entry_lines(title: str, entry: Mapping[str, Any]) -> list[str]:
    tier = f"{entry.get('tier', '?')} ({entry.get('code', '?')})"
    if "reason" in entry:
        tier += f", reason {entry['reason']}"
    basis = f", basis {entry['basis']}" if "basis" in entry else ""
    identity = "not resolved (listed under [[ignore]])"
    if "canonical" in entry:
        identity = f"{entry['canonical']}, resolved by {entry.get('resolved_by', '?')}"
    lines = [
        title,
        f"  identity:    {identity}",
        f"  tier:        {tier}",
        f"  confidence:  {entry.get('confidence', '?')}{basis}",
        f"  votes:       {_pairs(entry.get('votes', {}))}",
        f"  scores:      {_pairs(entry.get('scores', {}))}",
        f"  efforts:     {_pairs(entry.get('efforts', {}))}",
        f"  tool_call:   {str(entry['tool_call']).lower() if 'tool_call' in entry else 'unknown'}",
        f"  context:     {entry.get('context', 'unknown')}",
    ]
    if "open_weights" in entry:
        lines.append(f"  open_weights: {str(entry['open_weights']).lower()}")
    lines.extend(f"  note:        {note}" for note in entry.get("notes", []))
    return lines


def _used_sources(entries: Iterable[Mapping[str, Any]]) -> set[str]:
    used: set[str] = set()
    for entry in entries:
        if "canonical" in entry:
            used.add("modelsdev")
        used.update(
            _SCORE_SOURCE[name] for name in entry.get("scores", {}) if name in _SCORE_SOURCE
        )
    return used


def _has_key(entry: Mapping[str, Any], key: str) -> bool:
    """True when the entry resolved to `key`, compared without regard to
    case. An ignored row has no canonical and matches no key."""
    return "canonical" in entry and str(entry["canonical"]).lower() == key.lower()


def explain_lines(doc: Document, name: str) -> list[str] | None:
    """Everything the file says about one model, or None when nothing
    matches. `name` is tried as an exact `provider_id/model_name` first, then
    as a canonical key without regard to case: every `[[models]]` entry that
    resolved to it, or else the candidate."""
    models = doc.get("models", [])
    found = [(model_name(m), m) for m in models if model_name(m) == name]
    if not found:
        for entry in doc.get("unmapped", []):
            if model_name(entry) == name:
                return [
                    f"{name} is unmapped: no alias, host row or name match gave it a "
                    "canonical key, so it is unrated.",
                    "Add an [[alias]] or an [[ignore]] for it in aliases.toml and run "
                    "`modeltiers run`.",
                ]
        found = [(model_name(m), m) for m in models if _has_key(m, name)]
    if not found:
        found = [
            (f"candidate {c['canonical']}", c)
            for c in doc.get("candidates", [])
            if _has_key(c, name)
        ]
    if not found:
        return None
    lines: list[str] = []
    for title, entry in found:
        lines.extend(_entry_lines(title, entry))
    lines.extend(attribution_lines(doc, _used_sources(entry for _, entry in found)))
    return lines
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_show.py -q`
Expected: PASS, `18 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/show.py modeltiers/tests/test_show.py
git commit -m "feat(modeltiers): the tier table and the explain lines, from the output file alone"
```

---

### Task 7: The change report

**Files:**
- Create: `modeltiers/src/modeltiers/changes.py`
- Test: `modeltiers/tests/test_changes.py`

**Interfaces:**
- Consumes: `modeltiers.show.model_name`, `table_lines` (Task 6); `modeltiers.contracts.SOURCES` (Task 1); the `document` fixture (Task 5).
- Produces: `modeltiers.changes.change_lines(old: Mapping[str, Any] | None, new: Mapping[str, Any], path: Path) -> list[str]`. With `old` as `None`: one line saying there is no existing file at `path`, then the full table. Otherwise `changes since <generated_at>:`, then one line per model that changed tier (`tier: <name> <old> -> <new>`), was added, was removed, became unmapped, became resolved (`newly resolved: <name> <tier>`) or left the unmapped list because an `[[ignore]]` now covers it (`now ignored: <name> (<reason>)`, the reason taken from the entry's `ignored: <reason>` note); then `confidence only: N`, `candidates: A added, R removed, C changed tier` and `sources: …` with each source's status and, when it moved, what it was.

- [ ] **Step 1: Write the failing test**

Write `modeltiers/tests/test_changes.py`:

```python
import copy
from pathlib import Path
from typing import Any

from modeltiers import changes

PATH = Path("/config/local-ai/tiers.toml")
STEADY = [
    "  confidence only: 0",
    "  candidates: 0 added, 0 removed, 0 changed tier",
    "  sources: modelsdev ok, openrouter ok, arena ok, epoch ok",
]


def test_with_no_existing_file_it_says_so_and_prints_the_full_table(
    document: dict[str, Any],
) -> None:
    lines = changes.change_lines(None, document, PATH)
    assert (
        lines[0]
        == "changes: no existing file at /config/local-ai/tiers.toml; the full table follows"
    )
    assert lines[1] == "T  tier      conf    model                           canonical"
    assert lines[2] == "S  standard  high    ollama/glm-5.3:cloud            zhipuai/glm-5.3"
    assert lines[-1] == "candidates: 2 (list them with --candidates)"
    assert len(lines) == 1 + 11


def test_the_same_document_twice_reports_nothing(document: dict[str, Any]) -> None:
    assert changes.change_lines(document, document, PATH) == [
        "changes since 2026-10-12T15:58:00Z:",
        "  no model changed tier, was added or was removed",
        *STEADY,
    ]


def test_tier_changes_added_removed_newly_unmapped_and_newly_resolved(
    document: dict[str, Any],
) -> None:
    old = copy.deepcopy(document)
    models = {m["model_name"]: m for m in old["models"]}
    models["glm-5.3:cloud"]["tier"] = "deep"  # was deep, is standard
    models["minimax-m3:cloud"]["confidence"] = "medium"  # the tier is the same
    models["z-ai/glm-5.3"]["confidence"] = "low"
    # deepseek was unmapped and is now resolved; medgemma was a model and is now unmapped.
    old["models"].remove(models["deepseek-v4-pro:cloud"])
    old["models"].remove(models["gemma4:31b-mlx"])  # not in the old file: added
    old["models"].append(
        {
            "provider_id": "ollama",
            "model_name": "medgemma:27b",
            "tier": "quick",
            "confidence": "low",
        }
    )
    old["models"].append(
        {"provider_id": "ollama", "model_name": "gone:cloud", "tier": "deep", "confidence": "high"}
    )
    old["unmapped"] = [
        {"provider_id": "ollama", "model_name": "deepseek-v4-pro:cloud"},
        {"provider_id": "ollama", "model_name": "left-unmapped:1b"},
    ]
    assert changes.change_lines(old, document, PATH) == [
        "changes since 2026-10-12T15:58:00Z:",
        "  tier: ollama/glm-5.3:cloud deep -> standard",
        "  newly resolved: ollama/deepseek-v4-pro:cloud standard",
        "  added: ollama/gemma4:31b-mlx unrated",
        "  newly unmapped: ollama/medgemma:27b (was quick)",
        "  removed: ollama/gone:cloud (was deep)",
        "  removed: ollama/left-unmapped:1b (was unmapped)",
        "  confidence only: 2",
        "  candidates: 0 added, 0 removed, 0 changed tier",
        "  sources: modelsdev ok, openrouter ok, arena ok, epoch ok",
    ]


def test_a_row_an_ignore_now_covers_is_now_ignored_not_newly_resolved(
    document: dict[str, Any],
) -> None:
    old = copy.deepcopy(document)
    ignored = next(m for m in old["models"] if m["model_name"] == "nomic-embed-text:latest")
    old["models"].remove(ignored)
    old["unmapped"].append({"provider_id": "ollama", "model_name": "nomic-embed-text:latest"})
    assert changes.change_lines(old, document, PATH) == [
        "changes since 2026-10-12T15:58:00Z:",
        "  now ignored: ollama/nomic-embed-text:latest (embedding model)",
        *STEADY,
    ]
    # A row that was already ignored is no change at all.
    assert changes.change_lines(document, document, PATH)[1].startswith("  no model changed")


def test_a_row_added_as_unmapped_says_so(document: dict[str, Any]) -> None:
    old = copy.deepcopy(document)
    del old["unmapped"]
    assert changes.change_lines(old, document, PATH)[1] == "  added: ollama/medgemma:27b unmapped"


def test_candidate_changes_are_counts(document: dict[str, Any]) -> None:
    old = copy.deepcopy(document)
    old["candidates"][0]["tier"] = "deep"  # Opus 5.5 was deep, is marathon
    old["candidates"][1]["canonical"] = "Anthropic/Claude-Fable-5"  # the same key
    old["candidates"].append({"canonical": "openai/gpt-6-astra", "tier": "marathon"})
    new = copy.deepcopy(document)
    new["candidates"].append({"canonical": "xiaomi/mimo-v2.6-pro", "tier": "standard"})
    assert changes.change_lines(old, new, PATH)[-2] == (
        "  candidates: 1 added, 1 removed, 1 changed tier"
    )


def test_a_source_status_that_moved_shows_what_it_was(document: dict[str, Any]) -> None:
    new = copy.deepcopy(document)
    new["sources"]["arena"]["status"] = "stale"
    assert changes.change_lines(document, new, PATH)[-1] == (
        "  sources: modelsdev ok, openrouter ok, arena stale (was ok), epoch ok"
    )


def test_a_file_from_an_older_version_is_compared_by_the_fields_it_has(
    document: dict[str, Any],
) -> None:
    # No generated_at, no sources, no confidence, no candidates, a row with no tier.
    old = {
        "models": [
            {"provider_id": "ollama", "model_name": "glm-5.3:cloud", "tier": "standard"},
            {"provider_id": "ollama", "model_name": "minimax-m3:cloud"},
        ]
    }
    lines = changes.change_lines(old, document, PATH)
    assert lines[0] == "changes since an undated file:"
    assert "  tier: ollama/minimax-m3:cloud ? -> quick" in lines
    assert "  added: openrouter/z-ai/glm-5.3 standard" in lines
    assert lines[-3:] == [
        "  confidence only: 1",  # glm-5.3:cloud: the old file gave no confidence
        "  candidates: 2 added, 0 removed, 0 changed tier",
        "  sources: modelsdev ok (was ?), openrouter ok (was ?), arena ok (was ?), "
        "epoch ok (was ?)",
    ]
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_changes.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'changes' from 'modeltiers'`.

- [ ] **Step 3: Write the module**

`IGNORED_NOTE` repeats the prefix `pipeline.assign_all` writes, because this module reads documents and imports nothing from the pipeline. `test_a_row_an_ignore_now_covers_is_now_ignored_not_newly_resolved` runs on the `document` fixture, which the pipeline built, so the two cannot drift apart unseen.

Write `modeltiers/src/modeltiers/changes.py`:

```python
"""The change report: the new document against the existing output file,
keyed on (provider_id, model_name). Both sides are plain documents, so a file
written by an older version is compared by whatever fields it has."""

from collections.abc import Mapping
from pathlib import Path
from typing import Any

from modeltiers.contracts import SOURCES
from modeltiers.show import model_name, table_lines

Document = Mapping[str, Any]
UNMAPPED = "unmapped"
# How an [[ignore]]d row's reason is written in its `notes`.
IGNORED_NOTE = "ignored: "


def _states(doc: Document) -> dict[str, tuple[str, str]]:
    """name -> (tier or "unmapped", confidence), in file order."""
    states = {
        model_name(m): (str(m.get("tier", "?")), str(m.get("confidence", "?")))
        for m in doc.get("models", [])
    }
    states.update({model_name(entry): (UNMAPPED, "") for entry in doc.get("unmapped", [])})
    return states


def _ignored(doc: Document) -> dict[str, str]:
    """name -> the reason, for every `[[models]]` entry an [[ignore]] covers."""
    reasons = {}
    for model in doc.get("models", []):
        for note in model.get("notes", []):
            if str(note).startswith(IGNORED_NOTE):
                reasons[model_name(model)] = str(note)[len(IGNORED_NOTE) :]
    return reasons


def _candidates(doc: Document) -> dict[str, str]:
    return {
        str(c.get("canonical", "?")).lower(): str(c.get("tier", "?"))
        for c in doc.get("candidates", [])
    }


def _source_statuses(old: Document, new: Document) -> str:
    parts = []
    for name in SOURCES:
        now = new.get("sources", {}).get(name, {}).get("status", "?")
        was = old.get("sources", {}).get(name, {}).get("status", "?")
        parts.append(f"{name} {now}" if now == was else f"{name} {now} (was {was})")
    return ", ".join(parts)


def change_lines(old: Document | None, new: Document, path: Path) -> list[str]:
    """What `run` prints before it writes. With no existing file it says so
    and prints the full table."""
    if old is None:
        return [f"changes: no existing file at {path}; the full table follows", *table_lines(new)]
    before, after = _states(old), _states(new)
    ignored = _ignored(new)
    lines = [f"changes since {old.get('generated_at', 'an undated file')}:"]
    confidence_only = 0
    for name, (tier, confidence) in after.items():
        if name not in before:
            lines.append(f"  added: {name} {tier}")
            continue
        was, was_confidence = before[name]
        if tier == was:
            confidence_only += confidence != was_confidence
        elif tier == UNMAPPED:
            lines.append(f"  newly unmapped: {name} (was {was})")
        elif was == UNMAPPED and name in ignored:
            # It left the unmapped list without being resolved to anything.
            lines.append(f"  now ignored: {name} ({ignored[name]})")
        elif was == UNMAPPED:
            lines.append(f"  newly resolved: {name} {tier}")
        else:
            lines.append(f"  tier: {name} {was} -> {tier}")
    lines.extend(
        f"  removed: {name} (was {was})" for name, (was, _) in before.items() if name not in after
    )
    if len(lines) == 1:
        lines.append("  no model changed tier, was added or was removed")
    old_candidates, new_candidates = _candidates(old), _candidates(new)
    added = sum(1 for key in new_candidates if key not in old_candidates)
    removed = sum(1 for key in old_candidates if key not in new_candidates)
    moved = sum(1 for key, tier in new_candidates.items() if old_candidates.get(key, tier) != tier)
    lines.append(f"  confidence only: {confidence_only}")
    lines.append(f"  candidates: {added} added, {removed} removed, {moved} changed tier")
    lines.append(f"  sources: {_source_statuses(old, new)}")
    return lines
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_changes.py -q`
Expected: PASS, `8 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/changes.py modeltiers/tests/test_changes.py
git commit -m "feat(modeltiers): the change report between the existing tiers.toml and the new one"
```

---

### Task 8: The redirected-registry check

**Files:**
- Modify: `modeltiers/src/modeltiers/paths.py` (append two functions)
- Test: `modeltiers/tests/test_paths.py` (append)

**Interfaces:**
- Consumes: `modeltiers.paths.output_path`, `OUT_ENV` (step 1, Task 1).
- Produces, in `modeltiers.paths`:
  - `out_is_overridden(env: Mapping[str, str] | None = None) -> bool`. True when `MODELTIERS_OUT` is set and not empty.
  - `registry_is_beside_output(registry: str, env: Mapping[str, str] | None = None) -> bool`. True when `registry` is a file named `registry.toml` in `output_path(env).parent`. A leading `~` is expanded; the two directories are compared after `Path.resolve()`.

- [ ] **Step 1: Add the failing tests**

Append to the end of `modeltiers/tests/test_paths.py`, after two blank lines:

```python
def test_out_is_overridden_only_by_a_non_empty_variable() -> None:
    assert paths.out_is_overridden({"MODELTIERS_OUT": "/o/t.toml"})
    assert not paths.out_is_overridden({"MODELTIERS_OUT": ""})
    assert not paths.out_is_overridden({"HOME": "/h"})
    assert paths.out_is_overridden()  # conftest sets it


def test_the_registry_beside_the_output_file_is_recognised() -> None:
    env = {"HOME": "/h"}
    assert paths.registry_is_beside_output("/h/.config/local-ai/registry.toml", env)
    assert not paths.registry_is_beside_output("/tmp/scratch/registry.toml", env)
    # The right directory and another file name is still a redirect.
    assert not paths.registry_is_beside_output("/h/.config/local-ai/scratch.toml", env)
    xdg = {"HOME": "/h", "XDG_CONFIG_HOME": "/x"}
    assert paths.registry_is_beside_output("/x/local-ai/registry.toml", xdg)
    assert not paths.registry_is_beside_output("/h/.config/local-ai/registry.toml", xdg)


def test_a_symlinked_config_directory_still_matches(tmp_path: Path) -> None:
    real = tmp_path / "dotfiles" / "local-ai"
    real.mkdir(parents=True)
    config = tmp_path / "home" / ".config"
    config.mkdir()
    (config / "local-ai").symlink_to(real)
    env = {"HOME": str(tmp_path / "home")}
    assert paths.registry_is_beside_output(str(real / "registry.toml"), env)
    assert paths.registry_is_beside_output(str(config / "local-ai" / "registry.toml"), env)


def test_a_registry_path_with_a_tilde_is_expanded(tmp_path: Path) -> None:
    # conftest's HOME is tmp_path/home; expanduser reads the process environment.
    env = {"HOME": str(tmp_path / "home")}
    assert paths.registry_is_beside_output("~/.config/local-ai/registry.toml", env)
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_paths.py -q`
Expected: FAIL, `4 failed, 10 passed`: one test with `AttributeError: module 'modeltiers.paths' has no attribute 'out_is_overridden'` and three with `AttributeError: module 'modeltiers.paths' has no attribute 'registry_is_beside_output'`.

- [ ] **Step 3: Add the two functions**

Append to the end of `modeltiers/src/modeltiers/paths.py`, after two blank lines:

```python
def out_is_overridden(env: Mapping[str, str] | None = None) -> bool:
    """True when MODELTIERS_OUT names the output file."""
    env = os.environ if env is None else env
    return bool(env.get(OUT_ENV, ""))


def registry_is_beside_output(registry: str, env: Mapping[str, str] | None = None) -> bool:
    """True when `registry`, the path wt says it read, is `registry.toml` in
    the output file's directory. Directories are compared resolved, so a
    symlinked config directory still matches; the file itself is not
    resolved, because wt's registry.toml may be a symlink into another tree."""
    found = Path(registry).expanduser()
    expected = output_path(env).parent
    return found.name == "registry.toml" and found.parent.resolve() == expected.resolve()
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_paths.py -q`
Expected: PASS, `14 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/paths.py modeltiers/tests/test_paths.py
git commit -m "feat(modeltiers): tell an inventory from the registry beside tiers.toml from a redirected one"
```

---

### Task 9: `run` assigns, reports and writes

**Files:**
- Modify: `modeltiers/src/modeltiers/report.py`, `modeltiers/src/modeltiers/cli.py`
- Test: `modeltiers/tests/test_report.py`, `modeltiers/tests/test_cli.py` (both rewritten)

**Interfaces:**
- Consumes: `modeltiers.contracts.load_contracts` (Task 1); `modeltiers.pipeline.assign_all`, `Assignment` (Task 4); `modeltiers.output.build_document`, `write_document`, `read_previous` (Task 5); `modeltiers.contracts.STAMP` (Task 1); `modeltiers.show.attribution_lines`, `short` (Task 6); `modeltiers.changes.change_lines` (Task 7); `modeltiers.paths.out_is_overridden`, `registry_is_beside_output`, `output_path`, `cache_dir`, `OUT_ENV` (Task 8 and step 1); `modeltiers.snapshots.prune` (step 1, Task 4); `SAMPLE`, `without_timestamps`, `T0`, `FIXTURES` from `tests/conftest.py`.
- Produces:
  - `modeltiers.report.identity_lines(result: Identity) -> list[str]`, now ending after the review list.
  - `modeltiers.report.unresolved_lines(assignment: Assignment) -> list[str]`.
  - `modeltiers.report.sentinel_lines(assignment: Assignment) -> list[str]`. One line, `sentinels: N of N hold`, or `sentinels: F of N FAILED` and five or six lines per sentinel.
  - `modeltiers.cli.EXIT_SENTINEL = 3`; `cli.STAMP` is `contracts.STAMP`; `cli.Deps` gains `contracts_path: Path | None = None` as its last field.
  - `_run` order: load `aliases.toml`, load `contracts.toml`, read the inventory, refuse a redirected registry (under `--dry-run`, warn and go on), print provider warnings, `load_all`, `resolve_all`, print alias warnings, `assign_all`; then stdout: the `inventory:` line, the four source lines, the identity lines, the unresolved lines, the sentinel lines. A failed sentinel check stops there: one `modeltiers: …` line on stderr, return 3. Otherwise the change report, the four attribution lines, then either `dry run: <path> not written`, or the write, the pruning (skipped under `--offline`) and `wrote <path>`. Return 2 when any source fell back, else 0.
  - In `tests/test_cli.py`: `RUN`, `DRY`, `HEAD`, `TABLE`, `ATTRIBUTION`, `NO_CHANGES`, `MTPLX_WARNING`, `first_report(out: Path, last: str) -> str`, `_deps(specs, answers, *, now=T0, run_wt=…, aliases_path=ALIASES, contracts_path=CONTRACTS)`, `_scaled(body: bytes, factor: float) -> bytes`, `_seed`, `_arena`, and the fixtures `out` and `drifted`.

- [ ] **Step 1: Rewrite the report tests**

Step 2's two tests stay, without the `unresolved:` lines `identity_lines` no longer prints and with the `unmapped:` line's new last clause; four tests are new.

Replace the whole of `modeltiers/tests/test_report.py` with:

```python
from dataclasses import replace

from modeltiers import assign, identity, pipeline, report
from modeltiers.aliases import Aliases
from modeltiers.contracts import Contracts, Sentinel
from modeltiers.identity import RowIdentity, SourceIdentity
from modeltiers.loader import Sources
from modeltiers.pipeline import Assignment, Identity
from modeltiers.records import Inventory, InventoryRow, ModelsDev


def _identity(*rows: RowIdentity) -> Identity:
    def empty(source: str) -> SourceIdentity:
        return SourceIdentity(source=source, records=0, best={}, unresolved=(), ignored=0)

    return Identity(
        index=identity.build_index(ModelsDev(rows=()), Aliases()),
        rows=rows,
        openrouter=empty("openrouter"),
        arena=empty("arena"),
        epoch=empty("epoch"),
        warnings=(),
    )


def test_an_empty_inventory_still_prints_every_section() -> None:
    assert report.identity_lines(_identity()) == [
        "identity: 0 rows: 0 resolved, 0 ignored, 0 unmapped",
        "unmapped: 0 (each needs an [[alias]] or an [[ignore]] in aliases.toml, "
        "or is left open until evidence identifies it)",
        "review: 0 matched by name alone (check each)",
    ]


def test_the_name_column_is_as_wide_as_the_longest_name() -> None:
    short = RowIdentity(InventoryRow("omlx", "m", "local", True))
    long = RowIdentity(
        InventoryRow("mtplx", "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", "local", True),
        canonical="alibaba/qwen3.8-27b",
        resolved_by="alias",
    )
    lines = report.identity_lines(_identity(short, long))
    assert lines[1] == "  omlx/m" + " " * 45 + "  unmapped"
    assert lines[2] == (
        "  mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality  alias       "
        "alibaba/qwen3.8-27b  [inherited]"
    )
    # An alias-resolved row is not on the review list; only name matches are.
    assert lines[5] == "review: 0 matched by name alone (check each)"


def test_unresolved_records_at_a_floor_are_named_and_the_rest_counted(
    assignment: Assignment,
) -> None:
    assert report.unresolved_lines(assignment) == [
        "unresolved: 1 at or above a standard floor "
        "(each needs an [[alias]] or an [[ignore]] in aliases.toml, "
        "or is left open until evidence identifies it)",
        "  arena  Qwen3.8 Max  0.0235",
        "  openrouter: 1 of 6 records unresolved, 0 ignored, 4 keys resolved",
        "  arena: 1 of 6 records unresolved, 0 ignored, 5 keys resolved",
        "  epoch: 0 of 6 records unresolved, 1 ignored, 5 keys resolved",
    ]


def test_sentinels_that_hold_are_one_line(assignment: Assignment) -> None:
    assert report.sentinel_lines(assignment) == ["sentinels: 4 of 4 hold"]


def test_a_failed_check_prints_every_sentinel_with_scores_votes_and_pins(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    scaled = tuple(replace(r, score=r.score * 0.8) for r in sources.openrouter.data)
    drifted = replace(sources, openrouter=replace(sources.openrouter, data=scaled))
    result = pipeline.assign_all(pipeline.resolve_all(inventory, drifted, fixture_aliases), rule)
    lines = report.sentinel_lines(result)
    assert lines[0] == "sentinels: 2 of 4 FAILED"
    # Fable 5: the vote moved, the tier did not.
    assert lines[6:11] == [
        "  FAIL  anthropic/claude-fable-5",
        "        scores:  intelligence_index 39.68, coding_index 76.5, agentic_index 50.7, "
        "arena_agent 0.0894, arena_agent_ci_lower 0.0767, eci 162.06",
        "        votes:   intelligence_index standard, arena_agent deep, eci deep; "
        "tier before the gates deep",
        "        pinned:  intelligence_index deep, arena_agent deep, eci deep; tier deep",
        "        differs: intelligence_index voted standard, pinned deep",
    ]
    # A sentinel that holds is printed too, without a `differs` line.
    assert lines[-4:] == [
        "  ok    minimax/MiniMax-M3",
        "        scores:  arena_agent -0.0695, arena_agent_ci_lower -0.0786, eci 146.95",
        "        votes:   arena_agent quick, eci quick; tier before the gates quick",
        "        pinned:  arena_agent quick, eci quick; tier quick",
    ]


def test_a_sentinel_with_no_record_prints_none(assignment: Assignment, rule: Contracts) -> None:
    absent = assign.check_sentinel(
        Sentinel("alibaba/qwen3.8-27b", "quick", {}), "alibaba/qwen3.8-27b", assign.weigh({}, rule)
    )
    lines = report.sentinel_lines(replace(assignment, sentinels=(absent,)))
    assert lines == [
        "sentinels: 1 of 1 FAILED",
        "  FAIL  alibaba/qwen3.8-27b",
        "        scores:  none",
        "        votes:   none; tier before the gates unrated",
        "        pinned:  none; tier quick",
        "        differs: tier before the gates is unrated, pinned quick",
    ]
```

- [ ] **Step 2: Rewrite the CLI tests**

Step 1's `test_plain_run_does_what_dry_run_does` becomes `test_run_writes_the_contract_sample_and_reports`, the spec's end-to-end test: `run --inventory` over the fixtures with a fake `Fetcher` writes a file equal to `docs/contracts/tiers.sample.toml` apart from timestamps. The earlier tests are kept with the new report text; the rest are new.

Replace the whole of `modeltiers/tests/test_cli.py` with:

```python
import json
from collections.abc import Callable
from datetime import datetime, timedelta
from pathlib import Path

import pytest
from conftest import FIXTURES, SAMPLE, T0, without_timestamps
from fakes import FakeFetcher, Sleeps

from modeltiers import cli, fetch, snapshots
from modeltiers.errors import FetchError, InventoryError
from modeltiers.identity import HOST_NOTE
from modeltiers.loader import SourceSpecs

T1 = T0 + timedelta(days=7)
INVENTORY = FIXTURES / "inventory.json"
ALIASES = FIXTURES / "aliases.toml"
CONTRACTS = FIXTURES / "contracts.toml"
RUN = ["run", "--inventory", str(INVENTORY)]
DRY = [*RUN, "--dry-run"]
Answers = dict[str, list[bytes | Exception]]

HEAD = f"""\
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
  ollama/minimax-m3:cloud         normalized  minimax/MiniMax-M3  [{HOST_NOTE}]
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
"""

TABLE = """\
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
"""

ATTRIBUTION = """\
modelsdev: models.dev (https://models.dev), MIT
openrouter: Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter (openrouter.ai/rankings).
arena: Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0
epoch: Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0
"""

NO_CHANGES = """\
  no model changed tier, was added or was removed
  confidence only: 0
  candidates: 0 added, 0 removed, 0 changed tier
  sources: modelsdev ok, openrouter ok, arena ok, epoch ok
"""

MTPLX_WARNING = (
    "warning: provider mtplx probe status is partial; "
    "models it would have discovered may be absent from this run\n"
)


def first_report(out: Path, last: str) -> str:
    """What `run` prints over the golden fixtures when no file exists yet."""
    return (
        HEAD
        + "sentinels: 4 of 4 hold\n"
        + f"changes: no existing file at {out}; the full table follows\n"
        + TABLE
        + "candidates: 2 (list them with --candidates)\n"
        + ATTRIBUTION
        + last
    )


def _never_run_wt() -> str:
    raise AssertionError("wt must not be run when --inventory is given")


def _deps(
    specs: SourceSpecs,
    answers: Answers,
    *,
    now: datetime = T0,
    run_wt: Callable[[], str] = _never_run_wt,
    aliases_path: Path = ALIASES,
    contracts_path: Path = CONTRACTS,
) -> tuple[cli.Deps, FakeFetcher]:
    fetcher = FakeFetcher(answers)
    deps = cli.Deps(
        fetcher=fetcher,
        sleep=Sleeps(),
        now=lambda: now,
        run_wt=run_wt,
        specs=specs,
        aliases_path=aliases_path,
        contracts_path=contracts_path,
    )
    return deps, fetcher


def _scaled(body: bytes, factor: float) -> bytes:
    """An OpenRouter body with every intelligence index multiplied."""
    doc = json.loads(body)
    for model in doc["data"]:
        indices = (model.get("benchmarks") or {}).get("artificial_analysis")
        if indices:
            indices["intelligence_index"] *= factor
    return json.dumps(doc).encode()


@pytest.fixture
def out(tmp_path: Path) -> Path:
    """Where conftest's MODELTIERS_OUT points."""
    return tmp_path / "out" / "tiers.toml"


# --- run: the clean path ------------------------------------------------------


def test_run_writes_the_contract_sample_and_reports(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    captured = capsys.readouterr()
    assert captured.out == first_report(out, f"wrote {out}\n")
    # The fixture's mtplx probe is "partial".
    assert captured.err == MTPLX_WARNING
    assert without_timestamps(out.read_text()) == without_timestamps(SAMPLE.read_text())
    assert [p.name for p in out.parent.iterdir()] == ["tiers.toml"]


def test_dry_run_prints_the_same_report_and_writes_nothing(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(DRY, deps=_deps(specs, good_answers)[0]) == 0
    assert capsys.readouterr().out == first_report(out, f"dry run: {out} not written\n")
    assert not out.parent.exists()


def test_snapshots_go_under_modeltiers_cache(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path
) -> None:
    cli.main(DRY, deps=_deps(specs, good_answers)[0])
    assert sorted(p.name for p in (tmp_path / "cache").iterdir()) == [
        "arena",
        "epoch",
        "modelsdev",
        "openrouter",
    ]


def test_without_inventory_flag_wt_is_asked(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    deps, _ = _deps(specs, good_answers, run_wt=INVENTORY.read_text)
    assert cli.main(["run", "--dry-run"], deps=deps) == 0
    assert capsys.readouterr().out.startswith("inventory: 8 models")


# --- run: the change report ---------------------------------------------------


def test_a_second_run_reports_against_the_existing_file(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    cli.main(RUN, deps=_deps(specs, good_answers)[0])
    capsys.readouterr()
    assert cli.main(RUN, deps=_deps(specs, good_answers, now=T1)[0]) == 0
    report = capsys.readouterr().out
    assert report.endswith(
        "sentinels: 4 of 4 hold\n"
        "changes since 2026-10-12T15:58:00Z:\n" + NO_CHANGES + ATTRIBUTION + f"wrote {out}\n"
    )
    assert 'generated_at = "2026-10-19T15:58:00Z"' in out.read_text()


def test_a_tier_that_moved_is_reported_old_to_new(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    cli.main(RUN, deps=_deps(specs, good_answers)[0])
    out.write_text(out.read_text().replace('tier = "quick"', 'tier = "standard"'))
    capsys.readouterr()
    cli.main(DRY, deps=_deps(specs, good_answers, now=T1)[0])
    report = capsys.readouterr().out
    assert "  tier: ollama/minimax-m3:cloud standard -> quick\n" in report
    assert "no model changed tier" not in report
    # --dry-run left the edited file alone.
    assert 'tier = "standard"\ncode = "Q"' in out.read_text()


def test_an_existing_file_from_an_older_version_is_compared_and_replaced(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    out.parent.mkdir()
    out.write_text(
        "[[models]]\n"
        'provider_id = "ollama"\n'
        'model_name = "glm-5.3:cloud"\n'
        'tier = "deep"\n'
        "\n"
        "[[models]]\n"
        'provider_id = "ollama"\n'
        'model_name = "retired:cloud"\n'
    )
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    report = capsys.readouterr().out
    assert "changes since an undated file:\n" in report
    assert "  tier: ollama/glm-5.3:cloud deep -> standard\n" in report
    assert "  removed: ollama/retired:cloud (was ?)\n" in report
    assert without_timestamps(out.read_text()) == without_timestamps(SAMPLE.read_text())


def test_an_existing_file_that_is_not_toml_is_treated_as_absent_and_replaced(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    out.parent.mkdir()
    out.write_text("generated_at = \n")
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    captured = capsys.readouterr()
    assert captured.out == first_report(out, f"wrote {out}\n")
    assert captured.err == (
        MTPLX_WARNING + f"warning: {out} cannot be read as TOML; it is treated as absent\n"
    )
    assert without_timestamps(out.read_text()) == without_timestamps(SAMPLE.read_text())


# --- run: sources -------------------------------------------------------------


def test_a_source_that_falls_back_is_stale_and_exits_2(
    specs: SourceSpecs,
    good_answers: Answers,
    epoch_zip: Callable[..., bytes],
    read_fixture: Callable[[str], bytes],
    out: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    capsys.readouterr()
    second: Answers = {
        fetch.MODELSDEV_URL: [read_fixture("modelsdev.json")],
        fetch.OPENROUTER_URL: [read_fixture("openrouter.json")],
        # Arena: the rows API says its index is loading, twice.
        fetch.arena_page_url(0): [read_fixture("arena_error.json")] * 2,
        fetch.EPOCH_URL: [epoch_zip()],
    }
    assert cli.main(RUN, deps=_deps(specs, second, now=T1)[0]) == 2
    captured = capsys.readouterr()
    assert "arena: stale, 6 records, fetched 2026-10-12T15:58:00Z\n" in captured.out
    assert "openrouter: ok, 6 records, fetched 2026-10-19T15:58:00Z\n" in captured.out
    assert (
        "  sources: modelsdev ok, openrouter ok, arena stale (was ok), epoch ok\n" in captured.out
    )
    assert captured.out.endswith(f"wrote {out}\n")
    assert (
        "warning: arena: download failed: arena: the rows API answered with an error"
        in captured.err
    )
    # Written, with the stale source and its snapshot's own time.
    written = out.read_text()
    assert '[sources.arena]\nstatus = "stale"\nfetched_at = "2026-10-12T15:58:00Z"' in written
    assert '[sources.epoch]\nstatus = "ok"\nfetched_at = "2026-10-19T15:58:00Z"' in written


def test_dry_run_with_a_fallback_returns_2_and_writes_nothing(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    cli.main(DRY, deps=_deps(specs, good_answers)[0])
    good_answers[fetch.EPOCH_URL] = [FetchError("HTTP 404")]
    assert cli.main(DRY, deps=_deps(specs, good_answers, now=T1)[0]) == 2
    assert not out.exists()


def test_a_source_with_no_good_snapshot_exits_1_and_names_it(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    good_answers[fetch.EPOCH_URL] = [FetchError("HTTP 404")]
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.endswith("modeltiers: no usable data and no good snapshot for: epoch\n")
    assert not out.exists()


def test_offline_reads_snapshots_fetches_nothing_and_exits_0(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, out: Path
) -> None:
    cli.main(DRY, deps=_deps(specs, good_answers)[0])
    deps, fetcher = _deps(specs, {}, now=T1)
    assert cli.main([*RUN, "--offline"], deps=deps) == 0
    assert fetcher.calls == []
    # Stale because --offline was given: written, and exit 0.
    written = out.read_text()
    assert written.count('status = "stale"') == 4
    assert 'generated_at = "2026-10-19T15:58:00Z"' in written
    # Nothing saved by the offline run: still one snapshot per source.
    assert len(snapshots.list_snapshots(tmp_path / "cache", "arena")) == 1


def test_offline_with_an_empty_cache_exits_1(
    specs: SourceSpecs, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main([*DRY, "--offline"], deps=_deps(specs, {})[0]) == 1
    assert capsys.readouterr().err.endswith(
        "modeltiers: no usable data and no good snapshot for: modelsdev, openrouter, arena, epoch\n"
    )


# --- run: pruning -------------------------------------------------------------


def _seed(cache: Path, count: int) -> None:
    """`count` older good Arena snapshots and three bad ones."""
    for day in range(count):
        snapshots.save(cache, "arena", "json", b"{}", T0 - timedelta(days=day + 1))
    for day in range(3):
        bad = snapshots.save(cache, "arena", "json", b"{}", T0 - timedelta(days=day + 20))
        snapshots.mark_bad(bad)


def _arena(cache: Path) -> tuple[int, int]:
    found = snapshots.list_snapshots(cache, "arena")
    return sum(1 for s in found if not s.bad), sum(1 for s in found if s.bad)


def test_a_successful_write_prunes_to_four_good_and_two_bad(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path
) -> None:
    _seed(tmp_path / "cache", 6)
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    assert _arena(tmp_path / "cache") == (4, 2)
    # The newest good one is this run's.
    assert snapshots.good_snapshots(tmp_path / "cache", "arena")[0].fetched_at == T0


def test_dry_run_and_offline_prune_nothing(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path
) -> None:
    cache = tmp_path / "cache"
    cli.main(DRY, deps=_deps(specs, good_answers)[0])  # one good snapshot per source
    _seed(cache, 6)
    assert cli.main(DRY, deps=_deps(specs, good_answers, now=T1)[0]) == 0
    assert _arena(cache) == (8, 3)
    # Every seeded Arena snapshot is `{}`; offline must read the two real ones.
    for snapshot in snapshots.good_snapshots(cache, "arena"):
        if snapshot.path.read_bytes() == b"{}":
            snapshot.path.unlink()
    assert cli.main([*RUN, "--offline"], deps=_deps(specs, {}, now=T1)[0]) == 0
    assert _arena(cache) == (2, 3)


# --- run: configuration and inventory errors ----------------------------------


def test_an_inventory_error_exits_1_before_any_fetch(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    def no_wt() -> str:
        raise InventoryError("wt is not on PATH; install it or pass --inventory FILE")

    deps, fetcher = _deps(specs, good_answers, run_wt=no_wt)
    assert cli.main(["run", "--dry-run"], deps=deps) == 1
    assert capsys.readouterr().err == (
        "modeltiers: wt is not on PATH; install it or pass --inventory FILE\n"
    )
    assert fetcher.calls == []


def test_a_missing_inventory_file_exits_1(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["run", "--inventory", str(tmp_path / "absent.json")]
    assert cli.main(argv, deps=_deps(specs, good_answers)[0]) == 1
    assert "cannot read" in capsys.readouterr().err


def test_no_command_is_a_usage_error() -> None:
    with pytest.raises(SystemExit) as caught:
        cli.main([])
    assert caught.value.code == 2


def test_default_deps_are_the_real_seams() -> None:
    deps = cli.Deps()
    assert deps.fetcher is fetch.http_fetcher
    assert deps.specs.arena.name == "arena"
    assert deps.now().tzinfo is not None
    assert deps.aliases_path is None  # the committed data/aliases.toml
    assert deps.contracts_path is None  # the committed data/contracts.toml


def test_an_invalid_aliases_file_exits_1_before_wt_and_before_any_fetch(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    bad = tmp_path / "aliases.toml"
    bad.write_text(
        "[[ignore]]\n"
        'inventory = { provider_id = "ollama", model_name = "nomic-embed-text:latest" }\n'
    )
    # No --inventory: _never_run_wt fails the test if wt is asked.
    deps, fetcher = _deps(specs, good_answers, aliases_path=bad)
    assert cli.main(["run", "--dry-run"], deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == "modeltiers: aliases.toml: [[ignore]] #1: an ignore needs a reason\n"
    assert fetcher.calls == []


def test_an_invalid_contracts_file_exits_1_before_wt_and_before_any_fetch(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    bad = tmp_path / "contracts.toml"
    bad.write_text(CONTRACTS.read_text().replace("marathon_ci_lower = 0.097\n", ""))
    deps, fetcher = _deps(specs, good_answers, contracts_path=bad)
    assert cli.main(["run"], deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == (
        "modeltiers: contracts.toml: exactly one signal must carry marathon_ci_lower; found none\n"
    )
    assert fetcher.calls == []


def test_an_unanchored_alias_exits_1_once_models_dev_is_parsed(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    unanchored = tmp_path / "aliases.toml"
    unanchored.write_text(
        "[[alias]]\n"
        'canonical = "google/gemini-4-argon"\n'
        'inventory = [{ provider_id = "ollama", model_name = "medgemma:27b" }]\n'
    )
    deps, fetcher = _deps(specs, good_answers, aliases_path=unanchored)
    assert cli.main(DRY, deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.endswith(
        "modeltiers: aliases.toml: [[alias]] #1 (google/gemini-4-argon): canonical is not a "
        "models.dev key and the entry lists no openrouter, arena or epoch id\n"
    )
    assert fetch.MODELSDEV_URL in fetcher.calls


def test_a_stale_alias_id_is_a_warning_and_the_run_goes_on(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    stale = tmp_path / "aliases.toml"
    stale.write_text(
        ALIASES.read_text() + '\n[[alias]]\ncanonical = "zhipuai/glm-5.3"\narena = ["GLM 5.2"]\n'
    )
    deps, _ = _deps(specs, good_answers, aliases_path=stale)
    assert cli.main(DRY, deps=deps) == 0
    captured = capsys.readouterr()
    assert captured.out.startswith(HEAD)
    assert captured.err.endswith(
        "warning: alias for zhipuai/glm-5.3 names arena id 'GLM 5.2', "
        "which is not in this run's arena data\n"
    )


def test_with_no_aliases_every_row_falls_to_the_rules(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    empty = tmp_path / "aliases.toml"
    empty.write_text("")
    deps, _ = _deps(specs, good_answers, aliases_path=empty)
    assert cli.main(DRY, deps=deps) == 0
    out = capsys.readouterr().out
    assert "identity: 8 rows: 6 resolved, 0 ignored, 2 unmapped\n" in out
    # Joined by name to the April key, and listed for review with its note.
    assert (
        "  ollama/deepseek-v4-pro:cloud    normalized  deepseek/deepseek-v4-pro  "
        f"[{HOST_NOTE}]\n" in out
    )
    assert "  ollama/deepseek-v4-pro:cloud -> deepseek/deepseek-v4-pro\n" in out
    assert "unmapped: 2 (each needs" in out
    assert "  ollama/nomic-embed-text:latest\n" in out
    assert "  epoch: 1 of 6 records unresolved, 0 ignored, 5 keys resolved\n" in out
    # The April key has no record: the row is rated on nothing.
    assert "-  unrated   none    ollama/deepseek-v4-pro:cloud" in out


# --- run: the redirected registry ---------------------------------------------


def test_a_redirected_registry_is_refused_before_any_fetch(
    specs: SourceSpecs,
    good_answers: Answers,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # MODELTIERS_OUT unset: the output file is the default one, under
    # conftest's XDG_CONFIG_HOME. The fixture inventory names another registry.
    monkeypatch.delenv("MODELTIERS_OUT")
    default = tmp_path / "xdg-config" / "local-ai"
    deps, fetcher = _deps(specs, good_answers)
    assert cli.main(RUN, deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == (
        "modeltiers: the inventory came from /home/owner/.config/local-ai/registry.toml, "
        f"which is not {default / 'registry.toml'}; refusing to replace "
        f"{default / 'tiers.toml'} from it. Set MODELTIERS_OUT to write somewhere else\n"
    )
    assert fetcher.calls == []
    assert not default.exists()


def test_dry_run_with_a_redirected_registry_reports_and_warns(
    specs: SourceSpecs,
    good_answers: Answers,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # --dry-run writes nothing, so there is nothing to refuse: the whole
    # report, and a warning that the same run without the flag stops.
    monkeypatch.delenv("MODELTIERS_OUT")
    default = tmp_path / "xdg-config" / "local-ai"
    assert cli.main(DRY, deps=_deps(specs, good_answers)[0]) == 0
    captured = capsys.readouterr()
    out = default / "tiers.toml"
    assert captured.out == first_report(out, f"dry run: {out} not written\n")
    assert captured.err == (
        "warning: the inventory came from /home/owner/.config/local-ai/registry.toml, "
        f"which is not {default / 'registry.toml'}; without --dry-run this run is refused "
        "and exits 1. Set MODELTIERS_OUT to write somewhere else\n" + MTPLX_WARNING
    )
    assert not default.exists()


def test_the_registry_beside_the_default_output_file_is_accepted(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.delenv("MODELTIERS_OUT")
    default = tmp_path / "xdg-config" / "local-ai"
    saved = tmp_path / "inventory.json"
    saved.write_text(
        INVENTORY.read_text().replace(
            "/home/owner/.config/local-ai/registry.toml", str(default / "registry.toml")
        )
    )
    assert cli.main(["run", "--inventory", str(saved)], deps=_deps(specs, good_answers)[0]) == 0
    assert (default / "tiers.toml").exists()


# --- run: sentinels and exit-code precedence ----------------------------------


@pytest.fixture
def drifted(good_answers: Answers, read_fixture: Callable[[str], bytes]) -> Answers:
    """Clean answers, with every intelligence index scaled by 0.8."""
    good_answers[fetch.OPENROUTER_URL] = [_scaled(read_fixture("openrouter.json"), 0.8)]
    return good_answers


def test_a_failed_sentinel_writes_nothing_and_exits_3(
    specs: SourceSpecs,
    good_answers: Answers,
    read_fixture: Callable[[str], bytes],
    epoch_zip: Callable[..., bytes],
    out: Path,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # A good file from last week.
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    before = out.read_bytes()
    _seed(tmp_path / "cache", 6)
    capsys.readouterr()
    second: Answers = {
        fetch.MODELSDEV_URL: [read_fixture("modelsdev.json")],
        fetch.OPENROUTER_URL: [_scaled(read_fixture("openrouter.json"), 0.8)],
        fetch.arena_page_url(0): [read_fixture("arena_page1.json")],
        fetch.arena_page_url(3): [read_fixture("arena_page2.json")],
        fetch.EPOCH_URL: [epoch_zip()],
    }
    assert cli.main(RUN, deps=_deps(specs, second, now=T1)[0]) == 3
    captured = capsys.readouterr()
    assert out.read_bytes() == before  # the existing file is untouched
    assert "sentinels: 2 of 4 FAILED\n" in captured.out
    # Fable 5's vote moved from deep to standard; its tier is still deep.
    assert (
        "  FAIL  anthropic/claude-fable-5\n"
        "        scores:  intelligence_index 39.68, coding_index 76.5, agentic_index 50.7, "
        "arena_agent 0.0894, arena_agent_ci_lower 0.0767, eci 162.06\n"
        "        votes:   intelligence_index standard, arena_agent deep, eci deep; "
        "tier before the gates deep\n"
        "        pinned:  intelligence_index deep, arena_agent deep, eci deep; tier deep\n"
        "        differs: intelligence_index voted standard, pinned deep\n"
    ) in captured.out
    assert "  ok    zhipuai/glm-5.3\n" in captured.out
    # No change report and no "wrote" line after a failed check.
    assert "changes" not in captured.out
    assert "wrote" not in captured.out
    assert captured.err.endswith(
        "modeltiers: a sentinel moved; nothing written. If the move is real, edit contracts.toml\n"
    )
    assert _arena(tmp_path / "cache") == (8, 3)  # and nothing pruned


def test_dry_run_returns_3_for_a_failed_sentinel(
    specs: SourceSpecs, drifted: Answers, out: Path
) -> None:
    assert cli.main(DRY, deps=_deps(specs, drifted)[0]) == 3
    assert not out.exists()


def test_3_takes_precedence_over_2(
    specs: SourceSpecs, good_answers: Answers, drifted: Answers, out: Path
) -> None:
    cli.main(DRY, deps=_deps(specs, good_answers)[0])  # a good snapshot of every source
    drifted[fetch.EPOCH_URL] = [FetchError("HTTP 404")]  # Epoch falls back: exit 2 alone
    assert cli.main(RUN, deps=_deps(specs, drifted, now=T1)[0]) == 3
    assert not out.exists()


def test_1_takes_precedence_over_3(
    specs: SourceSpecs, drifted: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    # These downloads fail two sentinels: on their own the run exits 3.
    assert cli.main(RUN, deps=_deps(specs, drifted)[0]) == 3
    assert "sentinels: 2 of 4 FAILED\n" in capsys.readouterr().out
    # The same downloads and the same four sentinels, plus a fifth that names
    # an unknown key: a configuration error in the run whose sentinels fail.
    unknown = tmp_path / "contracts.toml"
    unknown.write_text(
        CONTRACTS.read_text()
        + '\n[[sentinels]]\ncanonical = "anthropic/claude-fable-9"\ntier = "deep"\nvotes = {}\n'
    )
    deps, fetcher = _deps(specs, drifted, contracts_path=unknown)
    assert cli.main(RUN, deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.endswith(
        "modeltiers: contracts.toml: [[sentinels]] #5 (anthropic/claude-fable-9): "
        "not a canonical key in this run's models.dev data or in aliases.toml\n"
    )
    # Every source was downloaded: the error came with the drifted data in hand.
    assert fetch.OPENROUTER_URL in fetcher.calls
    assert fetch.EPOCH_URL in fetcher.calls


def test_a_source_with_no_data_is_1_whatever_the_sentinels_would_say(
    specs: SourceSpecs, drifted: Answers, out: Path
) -> None:
    # The drifted OpenRouter data would fail two sentinels, and Epoch has no
    # download and no snapshot: the source error ends the run first.
    drifted[fetch.EPOCH_URL] = [FetchError("HTTP 404")]
    assert cli.main(RUN, deps=_deps(specs, drifted)[0]) == 1
    assert not out.exists()
```

- [ ] **Step 3: Run the tests and see them fail**

Run: `uv run --directory modeltiers pytest tests/test_report.py -q`
Expected: FAIL, `5 failed, 1 passed`: three tests with `AttributeError: module 'modeltiers.report' has no attribute 'sentinel_lines'`, one with `AttributeError: module 'modeltiers.report' has no attribute 'unresolved_lines'`, and one with an `AssertionError`, because step 2's `identity_lines` still ends with its unresolved section and still prints the `unmapped:` line without the words "or is left open until evidence identifies it".

Run: `uv run --directory modeltiers pytest tests/test_cli.py -q`
Expected: FAIL, `31 failed, 1 passed`: thirty tests with `TypeError: Deps.__init__() got an unexpected keyword argument 'contracts_path'` and one, `test_default_deps_are_the_real_seams`, with `AttributeError: 'Deps' object has no attribute 'contracts_path'`. The test that passes is the usage-error one, which builds no `Deps`.

- [ ] **Step 4: Rewrite the report**

Replace the whole of `modeltiers/src/modeltiers/report.py` with:

```python
"""What `run` prints from the pipeline's own results: how every inventory row
resolved, the unmapped list, the review list, the unresolved records and the
sentinel check. The change report is `changes.py`; the table is `show.py`."""

from modeltiers.identity import INHERITED, RowIdentity
from modeltiers.pipeline import Assignment, Identity
from modeltiers.show import short


def _name(entry: RowIdentity) -> str:
    return f"{entry.row.provider_id}/{entry.row.model_name}"


def _outcome(entry: RowIdentity) -> str:
    if entry.ignore_reason is not None:
        return f"{'ignored':<10}  {entry.ignore_reason}"
    if entry.canonical is None:
        return "unmapped"
    extras = [INHERITED] if entry.basis == INHERITED else []
    extras.extend(entry.notes)
    tail = f"  [{'; '.join(extras)}]" if extras else ""
    return f"{entry.resolved_by:<10}  {entry.canonical}{tail}"


def identity_lines(result: Identity) -> list[str]:
    """The identity section of the report, one string per line: a row per
    inventory model in wt's order, then the unmapped list and the review list
    (rows matched by name alone)."""
    rows = result.rows
    resolved = sum(1 for r in rows if r.canonical is not None)
    ignored = sum(1 for r in rows if r.ignore_reason is not None)
    width = max((len(_name(r)) for r in rows), default=0)
    lines = [
        f"identity: {len(rows)} rows: {resolved} resolved, {ignored} ignored, "
        f"{len(result.unmapped)} unmapped"
    ]
    lines.extend(f"  {_name(r):<{width}}  {_outcome(r)}" for r in rows)
    lines.append(
        f"unmapped: {len(result.unmapped)} (each needs an [[alias]] or an [[ignore]] "
        "in aliases.toml, or is left open until evidence identifies it)"
    )
    lines.extend(f"  {_name(r)}" for r in result.unmapped)
    lines.append(f"review: {len(result.review)} matched by name alone (check each)")
    lines.extend(f"  {_name(r)} -> {r.canonical}" for r in result.review)
    return lines


def unresolved_lines(assignment: Assignment) -> list[str]:
    """The source records no step resolved: those at or above their signal's
    standard floor by name, then one count line per benchmark source."""
    listed = assignment.unresolved
    lines = [
        f"unresolved: {len(listed)} at or above a standard floor "
        "(each needs an [[alias]] or an [[ignore]] in aliases.toml, "
        "or is left open until evidence identifies it)"
    ]
    lines.extend(f"  {r.source}  {r.source_id}  {short(r.score)}" for r in listed)
    lines.extend(
        f"  {s.source}: {len(s.unresolved)} of {s.records} records unresolved, "
        f"{s.ignored} ignored, {len(s.best)} keys resolved"
        for s in assignment.identity.sources()
    )
    return lines


def sentinel_lines(assignment: Assignment) -> list[str]:
    """One line when every sentinel holds. Otherwise every sentinel: its
    scores, its votes, its pinned values and what differs."""
    results = assignment.sentinels
    failed = [r for r in results if not r.ok]
    if not failed:
        return [f"sentinels: {len(results)} of {len(results)} hold"]
    lines = [f"sentinels: {len(failed)} of {len(results)} FAILED"]
    for result in results:
        evidence, pinned = result.evidence, result.sentinel
        scores = ", ".join(f"{k} {short(v)}" for k, v in evidence.scores.items()) or "none"
        votes = ", ".join(f"{k} {v}" for k, v in evidence.votes.items()) or "none"
        pins = ", ".join(f"{k} {v}" for k, v in pinned.votes.items()) or "none"
        lines.append(f"  {'ok  ' if result.ok else 'FAIL'}  {result.canonical}")
        lines.append(f"        scores:  {scores}")
        lines.append(f"        votes:   {votes}; tier before the gates {evidence.pre_gate}")
        lines.append(f"        pinned:  {pins}; tier {pinned.tier}")
        lines.extend(f"        differs: {problem}" for problem in result.problems)
    return lines
```

- [ ] **Step 5: Rewrite `run`**

The whole file again. From step 2's: the imports; `EXIT_SENTINEL` and `STAMP`; `Deps.contracts_path`; the help texts; `_check_registry`, which raises for a plain run and warns for a dry one; and `_run` from the `contracts` line on. The placeholder last line is gone.

Replace the whole of `modeltiers/src/modeltiers/cli.py` with:

```python
"""The `modeltiers` command line.

`main` builds the real dependencies; a test passes its own `Deps`, so no test
reaches the network, the clock or wt.
"""

import argparse
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path

from modeltiers import aliases as aliases_mod
from modeltiers import changes, loader, output, paths, pipeline, report, show, snapshots
from modeltiers import contracts as contracts_mod
from modeltiers import inventory as inventory_mod
from modeltiers.errors import ConfigError, ModelTiersError
from modeltiers.fetch import Fetcher, Sleep, http_fetcher
from modeltiers.inventory import RunWt
from modeltiers.loader import SourceSpecs
from modeltiers.records import Inventory

EXIT_OK = 0
EXIT_ERROR = 1
EXIT_STALE = 2
EXIT_SENTINEL = 3
STAMP = contracts_mod.STAMP


def _utc_now() -> datetime:
    return datetime.now(UTC)


@dataclass(frozen=True)
class Deps:
    """Everything `run` takes from outside the process."""

    fetcher: Fetcher = http_fetcher
    sleep: Sleep = time.sleep
    now: Callable[[], datetime] = _utc_now
    run_wt: RunWt = inventory_mod.run_wt
    specs: SourceSpecs = field(default_factory=loader.build_specs)
    aliases_path: Path | None = None  # None: the committed data/aliases.toml
    contracts_path: Path | None = None  # None: the committed data/contracts.toml


def _warn(message: str) -> None:
    print(f"warning: {message}", file=sys.stderr)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="modeltiers",
        description="Advisory capability tiers for the models wt knows.",
    )
    commands = parser.add_subparsers(dest="command", required=True)
    run = commands.add_parser(
        "run", help="fetch, resolve, assign, print the change report, write tiers.toml"
    )
    run.add_argument(
        "--dry-run",
        action="store_true",
        help=(
            "do everything except write the output file; the exit code is the real run's, "
            "except that a redirected registry, which the real run refuses with 1, only warns"
        ),
    )
    run.add_argument(
        "--offline",
        action="store_true",
        help="fetch nothing; read each source's newest good snapshot",
    )
    run.add_argument(
        "--inventory",
        metavar="FILE",
        type=Path,
        help="read saved `wt model list --json` output instead of running wt",
    )
    return parser


def _check_registry(inventory: Inventory, *, dry_run: bool) -> None:
    """Refuse a run whose inventory came from a redirected registry: wt
    follows WT_REGISTRY and the output path does not, so a scratch inventory
    would replace the real tiers.toml. `--dry-run` writes nothing, so it goes
    on and says that the same run without it would stop here."""
    if paths.out_is_overridden() or paths.registry_is_beside_output(inventory.registry):
        return
    out = paths.output_path()
    found = (
        f"the inventory came from {inventory.registry}, which is not {out.parent / 'registry.toml'}"
    )
    remedy = f"Set {paths.OUT_ENV} to write somewhere else"
    if not dry_run:
        raise ConfigError(f"{found}; refusing to replace {out} from it. {remedy}")
    _warn(f"{found}; without --dry-run this run is refused and exits 1. {remedy}")


def _run(args: argparse.Namespace, deps: Deps) -> int:
    # Before the inventory is read and before any fetch: a bad data file
    # costs no download.
    aliases = aliases_mod.load_aliases(deps.aliases_path)
    contracts = contracts_mod.load_contracts(deps.contracts_path)
    inventory = inventory_mod.load_inventory(path=args.inventory, run_wt=deps.run_wt)
    _check_registry(inventory, dry_run=args.dry_run)
    for line in inventory_mod.provider_warnings(inventory):
        _warn(line)
    now = deps.now()
    cache = paths.cache_dir()
    sources = loader.load_all(
        deps.specs,
        cache=cache,
        fetcher=deps.fetcher,
        sleep=deps.sleep,
        now=now,
        offline=args.offline,
        warn=_warn,
    )
    identity = pipeline.resolve_all(inventory, sources, aliases)
    for line in identity.warnings:
        _warn(line)
    assignment = pipeline.assign_all(identity, contracts)
    print(
        f"inventory: {len(inventory.rows)} models "
        f"({inventory.native_dropped} native rows dropped), registry {inventory.registry}"
    )
    for result in sources.results():
        print(
            f"{result.name}: {result.status}, {result.records} records, "
            f"fetched {result.fetched_at.strftime(STAMP)}"
        )
    for line in (
        *report.identity_lines(identity),
        *report.unresolved_lines(assignment),
        *report.sentinel_lines(assignment),
    ):
        print(line)
    if not assignment.sentinels_ok:
        print(
            f"modeltiers: a sentinel moved; nothing written. If the move is real, "
            f"edit {contracts.origin}",
            file=sys.stderr,
        )
        return EXIT_SENTINEL
    out = paths.output_path()
    document = output.build_document(assignment, inventory, sources, now)
    previous = output.read_previous(out)
    if previous is None and out.exists():
        _warn(f"{out} cannot be read as TOML; it is treated as absent")
    for line in (
        *changes.change_lines(previous, document, out),
        *show.attribution_lines(document),
    ):
        print(line)
    if args.dry_run:
        print(f"dry run: {out} not written")
    else:
        output.write_document(out, document)
        if not args.offline:
            for result in sources.results():
                snapshots.prune(cache, result.name)
        print(f"wrote {out}")
    return EXIT_STALE if any(r.fallback for r in sources.results()) else EXIT_OK


def main(argv: list[str] | None = None, *, deps: Deps | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        return _run(args, deps or Deps())
    except ModelTiersError as exc:
        print(f"modeltiers: {exc}", file=sys.stderr)
        return EXIT_ERROR
```

- [ ] **Step 6: Run the tests and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_report.py tests/test_cli.py -q`
Expected: PASS, `38 passed`.

- [ ] **Step 7: Commit**

```bash
git add modeltiers/src/modeltiers/report.py modeltiers/src/modeltiers/cli.py modeltiers/tests/test_report.py modeltiers/tests/test_cli.py
git commit -m "feat(modeltiers): run assigns tiers, checks the sentinels, reports the changes and writes tiers.toml"
```

---

### Task 10: The `show` and `explain` commands

**Files:**
- Modify: `modeltiers/src/modeltiers/cli.py`
- Test: `modeltiers/tests/test_cli.py` (append)

**Interfaces:**
- Consumes: `modeltiers.output.read_document` (Task 5); `modeltiers.show.age_line`, `stale_warning`, `table_lines`, `attribution_lines`, `explain_lines` (Task 6); `cli.Deps`, `_warn`, `_run` (Task 9).
- Produces:
  - `modeltiers show [--candidates]`: stdout is the age line, the table and the attribution lines; past 14 days one `warning:` line on stderr. Exit 0, or 1 when the file is missing or is not TOML.
  - `modeltiers explain <model>`: stdout is `explain_lines`. Exit 0; 1 with `modeltiers: no model or canonical key '<model>' in the file generated <generated_at>` on stderr when nothing matches, and 1 when the file is missing or is not TOML.
  - `main` dispatches on the subcommand through `_COMMANDS`.
  - In `tests/test_cli.py`: `_reader_deps(now: datetime) -> cli.Deps`, whose fetcher and `run_wt` fail the test when called, and the fixture `written`.

- [ ] **Step 1: Add the failing tests**

Append to the end of `modeltiers/tests/test_cli.py`, after two blank lines:

```python
# --- show ---------------------------------------------------------------------


def _reader_deps(now: datetime) -> cli.Deps:
    """Deps for `show` and `explain`: any fetch or wt call fails the test."""

    def no_fetch(url: str) -> bytes:
        raise AssertionError(f"show and explain never fetch: {url}")

    return cli.Deps(fetcher=no_fetch, sleep=Sleeps(), now=lambda: now, run_wt=_never_run_wt)


@pytest.fixture
def written(specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]) -> None:
    """A `tiers.toml` generated at T0 from the golden fixtures."""
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    capsys.readouterr()


@pytest.mark.usefixtures("written")
def test_show_prints_the_age_the_table_and_the_attribution(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["show"], deps=_reader_deps(T1)) == 0
    captured = capsys.readouterr()
    assert captured.out == (
        "tiers.toml generated 2026-10-12T15:58:00Z (7 days old), contracts fixture-1\n"
        + TABLE
        + "candidates: 2 (list them with --candidates)\n"
        + ATTRIBUTION
    )
    assert captured.err == ""


@pytest.mark.usefixtures("written")
def test_show_candidates_lists_them_and_warns_past_fourteen_days(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["show", "--candidates"], deps=_reader_deps(T0 + timedelta(days=15))) == 0
    captured = capsys.readouterr()
    assert captured.out == (
        "tiers.toml generated 2026-10-12T15:58:00Z (15 days old), contracts fixture-1\n"
        + TABLE
        + "candidates: 2 (scored by a source, not in the inventory)\n"
        + "M  marathon  high    anthropic/claude-opus-5-5\n"
        + "D  deep      high    anthropic/claude-fable-5\n"
        + ATTRIBUTION
    )
    assert captured.err == (
        "warning: tiers.toml is 15 days old; run `modeltiers run` to refresh it\n"
    )


@pytest.mark.parametrize("argv", [["show"], ["explain", "zhipuai/glm-5.3"]])
def test_show_and_explain_without_a_file_name_the_run_command(
    argv: list[str], out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(argv, deps=_reader_deps(T0)) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == (
        f"modeltiers: cannot read {out}: No such file or directory; "
        "run `modeltiers run` to write it\n"
    )


@pytest.mark.parametrize("argv", [["show"], ["explain", "zhipuai/glm-5.3"]])
def test_show_and_explain_with_a_file_that_is_not_toml_exit_1(
    argv: list[str], out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    out.parent.mkdir()
    out.write_text("generated_at = \n")
    assert cli.main(argv, deps=_reader_deps(T0)) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.startswith(f"modeltiers: {out} is not TOML")
    assert captured.err.endswith("run `modeltiers run` to write it again\n")


# --- explain ------------------------------------------------------------------


@pytest.mark.usefixtures("written")
def test_explain_a_model_by_pair_and_by_key(capsys: pytest.CaptureFixture[str]) -> None:
    assert cli.main(["explain", "ollama/minimax-m3:cloud"], deps=_reader_deps(T0)) == 0
    assert capsys.readouterr().out == (
        "ollama/minimax-m3:cloud\n"
        "  identity:    minimax/MiniMax-M3, resolved by normalized\n"
        "  tier:        quick (Q)\n"
        "  confidence:  high, basis measured\n"
        "  votes:       arena_agent quick, eci quick\n"
        "  scores:      arena_agent -0.0695, arena_agent_ci_lower -0.0786, eci 146.95\n"
        "  efforts:     none\n"
        "  tool_call:   true\n"
        "  context:     512000\n"
        "  note:        host row has no canonical id; matched by name\n"
        "modelsdev: models.dev (https://models.dev), MIT\n"
        "arena: Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0\n"
        "epoch: Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0\n"
    )
    # A key, in another case: both inventory rows that resolved to it.
    assert cli.main(["explain", "ZHIPUAI/GLM-5.3"], deps=_reader_deps(T0)) == 0
    by_key = capsys.readouterr().out
    assert by_key.startswith("ollama/glm-5.3:cloud\n")
    assert "\nopenrouter/z-ai/glm-5.3\n" in by_key
    # A key with no inventory row: the candidate.
    assert cli.main(["explain", "anthropic/claude-fable-5"], deps=_reader_deps(T0)) == 0
    assert capsys.readouterr().out.startswith("candidate anthropic/claude-fable-5\n")


@pytest.mark.usefixtures("written")
def test_explain_a_local_build_shows_the_tier_it_inherited(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["explain", "ollama/qwen3.8:27b-mlx"], deps=_reader_deps(T0)) == 0
    assert capsys.readouterr().out == (
        "ollama/qwen3.8:27b-mlx\n"
        "  identity:    alibaba/qwen3.8-27b, resolved by normalized\n"
        "  tier:        standard (S)\n"
        "  confidence:  low, basis inherited\n"
        "  votes:       intelligence_index standard\n"
        "  scores:      intelligence_index 33.7, coding_index 68.1, agentic_index 45.8\n"
        "  efforts:     none\n"
        "  tool_call:   true\n"
        "  context:     unknown\n"
        "  note:        local build: served context unknown; context gate skipped\n"
        "modelsdev: models.dev (https://models.dev), MIT\n"
        "openrouter: Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter "
        "(openrouter.ai/rankings).\n"
    )


@pytest.mark.usefixtures("written")
def test_explain_an_unmapped_row_and_a_name_that_matches_nothing(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["explain", "ollama/medgemma:27b"], deps=_reader_deps(T0)) == 0
    assert capsys.readouterr().out.startswith("ollama/medgemma:27b is unmapped: ")
    assert cli.main(["explain", "ollama/absent:cloud"], deps=_reader_deps(T0)) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == (
        "modeltiers: no model or canonical key 'ollama/absent:cloud' in the file generated "
        "2026-10-12T15:58:00Z\n"
    )
```

- [ ] **Step 2: Run the tests and see them fail**

Run: `uv run --directory modeltiers pytest tests/test_cli.py -q`
Expected: FAIL, `9 failed, 32 passed`: argparse refuses the subcommand and exits 2 (`SystemExit: 2`), with `invalid choice: 'show'` in four tests and `invalid choice: 'explain'` in five.

- [ ] **Step 3: Add the two commands**

In `modeltiers/src/modeltiers/cli.py`, replace:

```python
    """Everything `run` takes from outside the process."""
```

with:

```python
    """Everything a command takes from outside the process."""
```

In `modeltiers/src/modeltiers/cli.py`, replace:

```python
    )
    return parser
```

with:

```python
    )
    shown = commands.add_parser("show", help="print the tier table from tiers.toml")
    shown.add_argument("--candidates", action="store_true", help="list the candidates too")
    explain = commands.add_parser(
        "explain", help="print how one model was identified and why it has its tier"
    )
    explain.add_argument("model", help="provider_id/model_name, or a canonical key")
    return parser
```

In `modeltiers/src/modeltiers/cli.py`, replace:

```python
    return EXIT_STALE if any(r.fallback for r in sources.results()) else EXIT_OK


def main(argv: list[str] | None = None, *, deps: Deps | None = None) -> int:
```

with:

```python
    return EXIT_STALE if any(r.fallback for r in sources.results()) else EXIT_OK


def _show(args: argparse.Namespace, deps: Deps) -> int:
    document = output.read_document(paths.output_path())
    now = deps.now()
    warning = show.stale_warning(document, now)
    if warning is not None:
        _warn(warning)
    for line in (
        show.age_line(document, now),
        *show.table_lines(document, candidates=args.candidates),
        *show.attribution_lines(document),
    ):
        print(line)
    return EXIT_OK


def _explain(args: argparse.Namespace, deps: Deps) -> int:
    document = output.read_document(paths.output_path())
    lines = show.explain_lines(document, args.model)
    if lines is None:
        print(
            f"modeltiers: no model or canonical key {args.model!r} in the file generated "
            f"{document.get('generated_at', 'at an unknown time')}",
            file=sys.stderr,
        )
        return EXIT_ERROR
    for line in lines:
        print(line)
    return EXIT_OK


_COMMANDS: dict[str, Callable[[argparse.Namespace, Deps], int]] = {
    "run": _run,
    "show": _show,
    "explain": _explain,
}


def main(argv: list[str] | None = None, *, deps: Deps | None = None) -> int:
```

In `modeltiers/src/modeltiers/cli.py`, replace:

```python
        return _run(args, deps or Deps())
```

with:

```python
        return _COMMANDS[args.command](args, deps or Deps())
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_cli.py -q`
Expected: PASS, `41 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/cli.py modeltiers/tests/test_cli.py
git commit -m "feat(modeltiers): show the tier table and explain one model from tiers.toml"
```

---

### Task 11: Docs

**Files:**
- Modify: `modeltiers/CLAUDE.md`, `modeltiers/tests/fixtures/README.md`, `CLAUDE.md` (lines 19, 34 and 37)

**Interfaces:**
- Consumes: the behaviour of Tasks 1 to 10.
- Produces: nothing code reads.

- [ ] **Step 1: Bring the package's `CLAUDE.md` to step 3**

Changes from step 2's file: the overview; the command table (three commands), what `run` prints, the exit-code table and the redirected-registry note in place of the old exit-code line; a Contracts row and a caution in the files table; eight pipeline rows (`contracts.py`, `assign.py`, `pipeline.py`, `output.py`, `report.py`, `changes.py`, `show.py`, `cli.py`); the unresolved bullet; new Assignment and Output file sections; four testing patterns.

Replace the whole of `modeltiers/CLAUDE.md` with:

```markdown
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
- **Re-calibrating** is editing `src/modeltiers/data/contracts.toml`: a floor, a context minimum or a sentinel's pinned values.

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
- **Fixture-local data files.** Tests pass `tests/fixtures/aliases.toml` and `tests/fixtures/contracts.toml` (as `Deps.aliases_path` / `Deps.contracts_path`, or through the `fixture_aliases` and `rule` fixtures), or build an `Aliases` with `aliases.build`. Only `tests/test_data_files.py` reads the committed files under `src/modeltiers/data/`, and it asserts nothing about which entries or values they hold, so calibration changes no test.
- **Shared pipeline fixtures** in `tests/conftest.py`: `inventory`, `sources`, `fixture_aliases`, `rule`, `assignment` (the golden fixtures resolved and assigned) and `document` (`tiers.toml` for them, as a dict, generated at `T0`). `SAMPLE` and `without_timestamps` are there for the two tests that compare against `docs/contracts/tiers.sample.toml`.
- **Adding a row to a golden fixture** moves the counts pinned in `tests/test_source_*.py`, `tests/test_loader.py`, `tests/test_pipeline.py`, `tests/test_cli.py`, the `specs` minimums, and `docs/contracts/tiers.sample.toml`.
- **Identity tests build their own models.dev.** `tests/test_identity.py` and `tests/test_identity_sources.py` use hand-built `ModelsDev` rows; `tests/test_pipeline.py` and `tests/test_cli.py` use the golden fixtures.
- `tests/fixtures/README.md` credits each source. Keep it current when a fixture is added.
```

- [ ] **Step 2: Credit the new fixture and name the sample**

In `modeltiers/tests/fixtures/README.md`, replace:

```markdown
| `aliases.toml` | Hand-written for the tests | This repository. The same shape as `src/modeltiers/data/aliases.toml`; its entries are chosen for the tests and are not calibration. |
```

with:

```markdown
| `aliases.toml` | Hand-written for the tests | This repository. The same shape as `src/modeltiers/data/aliases.toml`; its entries are chosen for the tests and are not calibration. |
| `contracts.toml` | Hand-written for the tests | This repository. The same shape as `src/modeltiers/data/contracts.toml`, with four sentinels that the golden fixtures satisfy. Its floors are not kept in step with the committed file's. |
```

In `modeltiers/tests/fixtures/README.md`, replace:

```markdown
- **Rows removed.** Each file keeps between one and eleven rows.
- **Fields removed.** models.dev rows keep `id`, `name`, `tool_call`, `open_weights`, `limit` and `canonical_model_id`. OpenRouter rows keep `id`, `name`, `context_length` and `benchmarks.artificial_analysis`. Arena rows keep `model_name`, `organization`, `score`, `score_ci_lower`, `score_ci_upper`, `rank` and `category`, and the page's `features` list is dropped. The Epoch CSV keeps every column.
- **Page counts rewritten.** `num_rows_total` in the Arena pages is the fixture's own row count (5, or 1 in `arena_too_few.json`), and `row_idx` is renumbered.
```

with:

```markdown
- **Rows removed.** Each file keeps between one and twelve rows.
- **Fields removed.** models.dev rows keep `id`, `name`, `tool_call`, `open_weights`, `limit` and `canonical_model_id`. OpenRouter rows keep `id`, `name`, `context_length` and `benchmarks.artificial_analysis`. Arena rows keep `model_name`, `organization`, `score`, `score_ci_lower`, `score_ci_upper`, `rank` and `category`, and the page's `features` list is dropped. The Epoch CSV keeps every column.
- **Page counts rewritten.** `num_rows_total` in the Arena pages is the fixture's own row count (6, or 1 in `arena_too_few.json`), and `row_idx` is renumbered.
```

In `modeltiers/tests/fixtures/README.md`, replace:

```markdown
The Epoch fixtures are CSV text so they can be read and diffed; the `epoch_zip` fixture in `tests/conftest.py` puts one in a ZIP archive in memory.
```

with:

```markdown
The Epoch fixtures are CSV text so they can be read and diffed; the `epoch_zip` fixture in `tests/conftest.py` puts one in a ZIP archive in memory.

`docs/contracts/tiers.sample.toml`, at the repo root, is what `modeltiers run` writes from the golden fixtures here with `aliases.toml` and `contracts.toml`. A change to any of them changes that file; `tests/test_output.py` and `tests/test_cli.py` compare against it apart from timestamps.
```

- [ ] **Step 3: Update the root `CLAUDE.md`**

Three replacements. Each is a part of one long line; the text before and after it stays. Lines 19 and 34 were added by step 1 and last changed by step 2.

Line 19, the Commands entry for `modeltiers run`, which also gains a line for `show` and `explain` below it:

In `CLAUDE.md`, replace:

```markdown
and print each source's status and record count, then how every inventory model resolved to a canonical key (alias, host row or name match), the unmapped list and the list of name matches to review. It writes no output file yet: tier assignment and `tiers.toml` are later steps of `docs/superpowers/specs/2026-10-12-model-tiers-design.md`. Exit 0 clean, 1 on an invalid `aliases.toml`, an inventory error or a source with no usable data, 2 when a source fell back to its last good snapshot. Snapshots go under `~/.cache/local-ai/modeltiers/` (`MODELTIERS_CACHE` overrides)
```

with:

```markdown
give every model an advisory capability tier (`marathon`, `deep`, `standard`, `quick` or `unrated`), print how each resolved and what changed since the last run, and write `~/.config/local-ai/tiers.toml` (`MODELTIERS_OUT` overrides); `--dry-run` does everything except write and returns the code the real run would, except under a redirected registry, where the real run is refused (1) and the dry run reports and warns. Exit 0 written, 1 not written (an invalid `aliases.toml` or `contracts.toml`, an inventory error, a source with no usable data, or an inventory from a redirected registry while `MODELTIERS_OUT` is unset), 2 written with a source that fell back to its last good snapshot, 3 not written because a sentinel model's votes or tier moved (re-calibrate in `contracts.toml`). Snapshots go under `~/.cache/local-ai/modeltiers/` (`MODELTIERS_CACHE` overrides). Nothing routes on a tier yet
- `uv run --directory modeltiers modeltiers show [--candidates]` / `explain <provider_id/model_name | canonical key>` — read `tiers.toml` only (no fetch, no wt): the tier table with the file's age, or one model's identity, votes, scores, gates and notes
```

Line 34, the Architecture bullet for `modeltiers/`:

In `CLAUDE.md`, replace:

```markdown
Hand-kept identity answers (which key a model name means, which rows to ignore) are committed in `modeltiers/src/modeltiers/data/aliases.toml`. Design:
```

with:

```markdown
Hand-kept identity answers (which key a model name means, which rows to ignore) are committed in `modeltiers/src/modeltiers/data/aliases.toml`; the tier rule (score floors per signal, context minimums, pinned sentinel models) in `data/contracts.toml` beside it. Writes one file, `~/.config/local-ai/tiers.toml`, atomically; its format is `docs/contracts/tiers.sample.toml`. A reader treats a model absent from the file, or a file it cannot parse, as `unrated`. Design:
```

Line 37, the `docs/` bullet. The text replaced is in `CLAUDE.md` on main today (line 35 there, before step 1's two added lines):

In `CLAUDE.md`, replace:

```markdown
it holds no comments, so what it is for is recorded in the tests that read it), archive/ (dated docs), superpowers/ (plans+specs)
```

with:

```markdown
it holds no comments, so what it is for is recorded in the tests that read it; `tiers.sample.toml` is what modeltiers writes from its test fixtures, compared by modeltiers' own tests), archive/ (dated docs), superpowers/ (plans+specs)
```

- [ ] **Step 4: Check the links**

Run: `make check-links`
Expected: PASS, ending `ALL LINKS OK`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/CLAUDE.md modeltiers/tests/fixtures/README.md CLAUDE.md
git commit -m "docs(modeltiers): describe assignment, contracts.toml, tiers.toml and the three commands"
```

---

### Task 12: Full verification

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
52 files already formatted
uv run mypy src/
Success: no issues found in 25 source files
```

- [ ] **Step 2: The suite, with the coverage floor**

Run: `make -C modeltiers test`
Expected: PASS. The last lines are:

```
TOTAL                                   1644      0   100%
Required test coverage of 90.0% reached. Total coverage: 100.00%
============================= 487 passed in 0.89s ==============================
```

The time varies; the counts do not.

- [ ] **Step 3: The repo's lint**

Run: `make lint`
Expected: PASS, ending `ALL LINKS OK`.

- [ ] **Step 4: The tests left the real directories alone**

Run: `git status --short`
Expected: PASS with no output: nothing untracked, nothing modified. No step of this plan created `~/.config/local-ai/tiers.toml` or `~/.cache/local-ai/modeltiers`.

- [ ] **Step 5 (optional, needs the network and wt): one live run into scratch paths, by hand**

It makes four kinds of request and runs `wt model list --json`. Both variables are set, so neither the real `tiers.toml` nor `~/.cache` is touched; with `MODELTIERS_OUT` set the registry check accepts the real registry's inventory.

Run (live, by hand): `SCRATCH="$(mktemp -d)" && MODELTIERS_CACHE="$SCRATCH/cache" MODELTIERS_OUT="$SCRATCH/tiers.toml" uv run --directory modeltiers modeltiers run; echo "exit $?"`

Expected: exit 0 and a last line `wrote <scratch>/tiers.toml`, or exit 3 with `sentinels: F of 8 FAILED` and the sentinel table when a source has moved since 2026-10-12. Exit 3 is not a defect in this step: it is the check doing its job, and step 4 re-calibrates. On exit 0 the report shows `sentinels: 8 of 8 hold`, `changes: no existing file at …; the full table follows`, a table with one line per inventory model, a non-empty `unmapped:` list and a non-empty `unresolved:` list (both are step 4's work) and the four attribution lines. `MODELTIERS_OUT="$SCRATCH/tiers.toml" uv run --directory modeltiers modeltiers show --candidates` then prints the same table with the candidates, and `… modeltiers explain ollama/deepseek-v4-pro:cloud` prints that row's identity (`resolved by alias`), votes and scores. The numbers depend on the day's inventory and sources and are not pinned here.

**Done when**

- `modeltiers run` writes the output file, and every model `wt model list --json` reports apart from the native rows is in `[[models]]` or in `[[unmapped]]` (success criterion 1; `test_every_resolved_or_ignored_row_gets_a_tier_in_wt_order`, `test_run_writes_the_contract_sample_and_reports`). On the owner's machine the default path is `~/.config/local-ai/tiers.toml`; this plan never writes it.
- Every `[[models]]` and `[[candidates]]` entry carries a tier, a confidence, the votes and the raw scores (success criterion 2; `test_a_rated_model_entry_has_every_field`, `test_a_candidate_has_open_weights_and_no_inventory_fields`).
- `modeltiers explain <model>` shows, from the output file alone, how the model was identified and why it has its tier (success criterion 3; `tests/test_show.py`, `test_explain_a_model_by_pair_and_by_key`, with `_reader_deps` failing on any fetch or wt call).
- A local build is rated through its key: `basis = "inherited"` beside a rated tier, with the key's votes and tool-calling flag (the Decisions row "local builds inherit the tier"; `test_a_local_row_is_rated_through_inheritance`, `test_a_local_build_is_written_with_its_keys_tier_votes_and_tool_call`, `test_explain_a_local_build_shows_the_tier_it_inherited`).
- Models in a benchmark source and not in the inventory are listed as candidates with a tier (success criterion 4; `test_candidates_are_keys_with_a_vote_and_no_inventory_row`).
- A run in which a sentinel's votes or tier differ from the pinned values leaves the existing output file untouched and exits 3 (success criterion 5; `test_a_failed_sentinel_writes_nothing_and_exits_3`).
- A run with one source failing uses its last good snapshot, writes the file with that source `stale` and exits 2 (success criterion 6; `test_a_source_that_falls_back_is_stale_and_exits_2`).
- No test touches the network, the real config home or the real cache (success criterion 7), and the tool writes only snapshots and the output file (success criterion 8).
- The committed `contracts.toml` carries the spec's starting floors, context minimums, attribution strings and eight sentinels, and loads (spec Delivery item 3; `test_the_committed_contracts_file_loads_and_validates`).
- The spec's Testing bullets for Assignment, Sentinels, End to end and Fixture-local data each have their tests: `tests/test_assign.py`, `tests/test_pipeline.py`, `tests/test_cli.py`, `tests/test_data_files.py`. Pruning keeps four good and two bad after a successful write and is skipped under `--dry-run` and `--offline` (`test_a_successful_write_prunes_to_four_good_and_two_bad`, `test_dry_run_and_offline_prune_nothing`).
- `docs/contracts/tiers.sample.toml` exists and two tests compare the writer's output against it apart from timestamps.
- `make -C modeltiers check`, `make -C modeltiers test` and `make lint` pass.

**For step 4 (calibration and docs)**

- **Start with `--dry-run` and scratch paths.** A bare `modeltiers run` now writes `~/.config/local-ai/tiers.toml` and prunes `~/.cache/local-ai/modeltiers/`. The first write to the real paths is the owner's call.
- **What is left on the 2026-10-12 data:** ten inventory rows unmapped (step 2's plan gives the count and not the rows; `modeltiers run --dry-run` prints them under its unmapped heading, and step 4's Calibration Record names each with the evidence for it), and six unresolved records at or above a standard floor: Arena `Gemini 4 Argon`; Epoch `Gemini 3.1 Pro`, `Gemini 3 Pro`, `Muse Spark`, `Grok 4.20`, `Gemini 3 Flash`. Each needs an `[[alias]]` or an `[[ignore]]` in `src/modeltiers/data/aliases.toml`, or is left open until evidence identifies it, which is what `run` now prints beside both counts. Twelve rows are on the review list.
- **The `deep` list differs from the spec's.** Seven keys were `deep`: the spec's four, plus `openai/gpt-6-sol` and `openai/gpt-5.6-sol` (votes standard, deep, deep) and `openai/gpt-5.5-pro` (one Epoch vote, confidence `low`). The spec's list has since been corrected on the docs branch and names all of them, with Gemini 4 Argon, which step 4's declared key adds.
- **Re-calibration is an edit to `src/modeltiers/data/contracts.toml`** and needs no test change: only `tests/test_data_files.py` reads it, and it asserts no value.
- **The docs still owed:** `docs/guides/12-model-tiers.md`, and the `tiers.toml` and snapshot-directory rows of `docs/guides/00-config-map.md`. The root `CLAUDE.md` already names `tiers.toml` in the Commands and Architecture lines; the spec's "note under the config paths" may still want its own bullet there.
- **A fixture change moves the sample.** Adding a row to a golden fixture, or an entry to `tests/fixtures/aliases.toml` or `tests/fixtures/contracts.toml`, changes `docs/contracts/tiers.sample.toml` and the counts listed in `modeltiers/CLAUDE.md`.
