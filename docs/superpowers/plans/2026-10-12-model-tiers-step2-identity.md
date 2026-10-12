# modeltiers Step 2: Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `modeltiers run --dry-run` prints how every inventory row resolved to a canonical key (by alias, by its host's models.dev row, or by name), followed by the unmapped list, the review list of name matches and each benchmark source's unresolved count.

**Architecture:** Four new modules on step 1's records: `tokens.py` (the token function), `aliases.py` (`aliases.toml` to an `Aliases` with four lookups, and its two validation passes), `identity.py` (an `Index` over one models.dev snapshot, then `resolve_row` and `resolve_source`, each trying alias, host row, normalization in that order) and `pipeline.py` (`resolve_all`, one `Identity` result for the inventory and the three benchmark sources). `report.py` turns that result into lines and `cli.py` prints them. Nothing assigns a tier or writes `tiers.toml`; step 3 builds on `Identity`.

**Tech Stack:** Python `==3.13.*`, uv, hatchling, src layout. Standard library only in this step: `tomllib`, `importlib.resources`, `re`, `dataclasses`. No dependency is added; `uv.lock` does not change. Dev: pytest, pytest-cov, ruff, mypy (`strict = true` on `src/`).

**Spec:** docs/superpowers/specs/2026-10-12-model-tiers-design.md

**Verified against:** main at 51887c0, and a working prototype of this step. Every code block below was extracted from this file into a clone that holds step 1's nine commits and run in task order: each "Expected: FAIL" failed with the quoted message and each "Expected: PASS" passed. The resolver was also run over the 2026-10-12 downloads and the owner's `wt model list --json` output: 446 canonical keys, 14 of them `-latest`, 432 distinct token sequences and no two keys sharing one; with no aliases 35 of the 46 inventory rows resolved (the spec's number) and `ollama/deepseek-v4-pro:cloud` joined the April key by name with its note; with the committed `aliases.toml` it reached `deepseek/deepseek-v4-pro-0813`, every id in the file was present in that day's data, and 10 rows were left unmapped for step 4.

**Depends on:** docs/superpowers/plans/2026-10-12-model-tiers-step1-sources.md, merged.

## Global Constraints

- Python is `==3.13.*`, uv-managed, src layout, console script `modeltiers = "modeltiers.cli:main"`.
- The only runtime dependency is `tomli-w`, still unused in this step. `aliases.toml` is read with the standard library's `tomllib`. Dev dependencies are pytest, pytest-cov, ruff and mypy.
- The tool never writes `registry.toml`, `config.yaml` or any wt state, and never reads `registry.toml`. The inventory comes only from `wt model list --json` or from `--inventory FILE`.
- wt is run only as `wt model list --json`. No other wt command appears in code, tests or steps.
- The join key for an inventory row is the pair (`provider_id`, `model_name`), never the registry `id`.
- No test touches the network, the real config home or the real cache. Step 1's autouse `_hermetic` fixture stays as it is.
- **Canonical key:** `vendor/model[-snapshot]` exactly as models.dev spells `canonical_model_id`. Keys are stored in that spelling and compared without regard to case. The valid keys are every `canonical_model_id` in the models.dev snapshot, plus any key an alias declares.
- **Resolution order,** the same for an inventory row and a source record; the first that answers wins and is recorded in `resolved_by`: `alias`, `host`, `normalized`.
- **Host rows:** an ollama cloud row is looked up in models.dev's `ollama-cloud` provider by its tag with the `:cloud` or `-cloud` suffix removed; an OpenRouter row or record in the `openrouter` provider by its id with `:free` or `:batch` removed. A host row with no `canonical_model_id` does not answer.
- **The token function,** in order: (1) lowercase, discard everything up to and including the first `/`; (2) split on spaces, `-`, `_`, `/`, `:`, `(` and `)`; (3) drop any piece in `cloud`, `free`, `batch`, `latest`, `it`, `mlx`, `4bit`, `6bit`, `8bit`, `nvfp4`, `fp4`, `fp8`, `fp16`, `bf16`; (4) split each remaining piece where a letter is followed by a digit, and at every `.` between digits. A digit followed by letters stays one piece.
- **Normalization** answers only when the name's token sequence equals exactly one key's. A key that ends in `-latest` is left out of the normalization index.
- The note on a cloud inventory row that passed a host row with no canonical id and resolved by name is exactly `host row has no canonical id; matched by name`.
- An inventory row with `location = "local"` resolves like any other and is marked `inherited`.
- **Records that share a key:** the best-scoring record is used, and its effort and its confidence interval come from that same record.
- **`aliases.toml`:** `[[alias]]` source keys are `inventory`, `openrouter`, `arena` and `epoch`. Validation before any fetch: two entries that claim the same inventory pair or the same source id are an error, and so is an `[[ignore]]` without a reason. Validation once models.dev is parsed: an alias `canonical` that is not a models.dev key must list at least one source id. An alias that names a source id absent from this run's snapshot is a warning.
- An invalid `aliases.toml` names the entry, writes nothing and exits 1.
- Tests use `tests/fixtures/aliases.toml` or build an `Aliases` in the test. One test, `tests/test_data_files.py`, loads the committed `src/modeltiers/data/aliases.toml`.
- Quality bar: `make check` (ruff lint, `ruff format --check`, mypy) and `make test` (pytest, coverage floor 90).
- Guides never embed live model state. This step adds no guide. The committed `aliases.toml` is calibration data, not a guide; every model name in `modeltiers/CLAUDE.md` is one from the test fixtures.
- **Not in this step:** `contracts.toml`, `assign.py`, `output.py`, `show`, `explain`, `tiers.toml`, `docs/contracts/tiers.sample.toml`, the `[[unresolved]]` list (it holds only records at or above a signal's standard floor, and the floors arrive with `contracts.toml` in step 3; this step keeps every unresolved record and prints counts), writing an ignored row as `unrated` with reason `not_applicable` (this step marks it ignored), the calibration aliases (step 4).
- **Chosen where the spec is silent** (each is pinned by a test named in the task):
  - An `[[alias]]` and an `[[ignore]]` share one set of claims: a pair or a source id in both is "claimed twice". So is an id listed twice inside one entry. A source id is the pair (source, id), so one string under `arena` and under `epoch` is two ids.
  - An unknown table or an unknown key in an entry is an error (`openroutr = [...]` would otherwise be silently ignored). `note` is optional. `[alias]` in place of `[[alias]]` is an error that says to write `[[alias]]`.
  - An Arena id in `aliases.toml` has its effort parenthesis removed when the file is loaded, so `"GLM 5.3 (Max)"` and `"GLM 5.3"` are the same id there, as they are in the records.
  - An `[[ignore]]` is either `inventory = {…}` or `source` with `id`, never both and never neither; `source` is one of `openrouter`, `arena`, `epoch`.
  - A source-id alias is exact: an alias on OpenRouter id `x/y` does not cover `x/y:free`, which goes on to its host row.
  - The stale-id warning covers `[[alias]]` source ids only. An `[[ignore]]` whose record has left, and an inventory pair this machine does not have, are silent: inventories differ between machines.
  - The unanchored-canonical check stops at the first offending entry.
  - The host step applies to inventory rows whose `provider_id` is `ollama` (and whose `location` is `cloud`) or `openrouter`, and to OpenRouter records. A local ollama tag is never looked up among the cloud host rows. Arena and Epoch records have no host step.
  - A cloud row's host row is kept on its result (`RowIdentity.host_row`) whichever step answered, and when none did. Step 3 reads its `limit.context`.
  - An `[[ignore]]` is checked before any resolution step, for a row and for a record.
  - An alias-declared key joins the normalization index like a models.dev key. When an alias spells a models.dev key in another case, models.dev's spelling is the one stored.
  - Two records for one key with equal scores: the earlier one in the source's order stays, which keeps a base model ahead of its `:batch` copy.
  - A name with no tokens left (`mlx-4bit:latest`) matches nothing.
  - Unresolved counts are records, not distinct ids: two effort rows of one unresolved Arena model count twice.
  - Unmapped rows, unresolved records and stale-id warnings do not change the exit code.
  - Plain `run` still does what `--dry-run` does; the last line of the report becomes `nothing written: this version stops after resolving names`. Step 3 removes it.
- Commits use the repo's conventional style (`feat(modeltiers): …`, lowercase after the type, no trailing period), with the attribution trailer your session is told to add, if any.
- Work on one feature branch off `main`, for example `feat/modeltiers-step2-identity`. **Pushing the branch and opening the pull request need the owner's OK.**
- **Never, in any step of this plan:** read or print `~/.config/local-ai/registry.toml` (it holds a secret); write under `~/.config` or `~/.cache`; run a wt command other than `wt model list --json`. The one optional live command (Task 9) sets `MODELTIERS_CACHE` to a scratch directory.
- Commands are run from the repo root unless a step says otherwise.

## Review Focus

1. **A cloud row whose models.dev host row exists and has no `canonical_model_id`**, as `ollama/deepseek-v4-pro:cloud` has today. Its name matches exactly one key, the April `deepseek/deepseek-v4-pro`, which is the wrong model, and nothing fails. Expected: without an alias the row resolves with `resolved_by = "normalized"`, carries the note `host row has no canonical id; matched by name` and is on the review list `run` prints; with the alias it resolves to `deepseek/deepseek-v4-pro-0813` with `resolved_by = "alias"` and leaves the review list. Pinned in Task 4 (`test_deepseek_cloud_reaches_the_april_key_without_its_alias`, `test_deepseek_cloud_reaches_the_0813_key_through_its_alias`, `test_a_host_row_without_a_canonical_id_falls_through_to_the_name`) and Task 7 (`test_with_no_aliases_every_row_falls_to_the_rules`), where the new `CLEAN_REPORT` also pins the aliased row for step 1's `test_dry_run_prints_each_source_and_the_inventory_count`.
2. **A canonical key in mixed case.** models.dev spells `minimax/MiniMax-M3`; a person writing an alias types `minimax/minimax-m3`. Expected: both are one key; the stored spelling is models.dev's whichever step answered; the alias is not reported as unanchored; lookups by key work in any case. Pinned in Task 3 (`test_the_key_is_compared_without_regard_to_case`), Task 4 (`test_keys_keep_models_dev_spelling_and_are_found_in_any_case`, `test_the_stored_key_is_models_dev_spelling_whatever_answered`, `test_an_alias_declares_a_key_and_models_dev_spelling_wins`) and Task 5 (`test_best_is_keyed_in_lower_case_and_holds_the_stored_spelling`).
3. **Names that look alike and are not the same model:** `qwen3-8b` beside `qwen3.8`; `Qwen3.8 Max` beside the effort in `GLM 5.3 (Max)`; `Mistral Large` beside the floating key `mistral/mistral-large-latest`; a name whose token sequence two keys share. Expected: none of them joins. The row stays unmapped or the record unresolved; a wrong join is never preferred to no join. Pinned in Task 1 (`test_qwen3_8_never_matches_qwen3_8b`, `test_max_is_removed_as_an_effort_and_kept_as_a_name`) and Task 4 (`test_a_latest_key_is_valid_and_left_out_of_the_normalization_index`, `test_a_name_that_matches_two_keys_does_not_answer`, `test_an_ambiguous_name_stays_unmapped`).
4. **An `aliases.toml` with a slip in it:** an `[[ignore]]` with no reason or an empty one; one inventory pair under an `[[alias]]` and an `[[ignore]]`; one Arena id written once bare and once with `(High)`; a misspelt key (`openroutr`); `[alias]` for `[[alias]]`. Expected: exit 1 with a message that names the file and the entry, before wt is run and before any download. Pinned in Task 2 (`test_an_ignore_without_a_reason_is_an_error`, `test_an_alias_and_an_ignore_for_one_pair_is_an_error`, `test_a_duplicate_source_id_is_an_error`, `test_a_file_in_another_shape_is_a_config_error`) and Task 7 (`test_an_invalid_aliases_file_exits_1_before_wt_and_before_any_fetch`).
5. **An `aliases.toml` that has aged.** A model left Arena, so an alias names an id the snapshot no longer holds; or an alias points at a key models.dev does not have and lists only an inventory pair. Expected: the first is one `warning:` line on stderr and the run goes on with exit 0; the second is exit 1 once models.dev is parsed, naming the entry, because it would create a key no source can score. An alias to a key models.dev lacks is accepted when it lists at least one source id. Pinned in Task 3 (`test_a_source_id_absent_from_this_run_is_a_warning`, `test_an_unanchored_canonical_is_an_error`, `test_one_source_id_anchors_a_declared_key`), Task 6 (`test_an_alias_id_absent_from_this_run_is_a_warning_not_an_error`, `test_an_unanchored_alias_is_a_config_error`) and Task 7 (`test_a_stale_alias_id_is_a_warning_and_the_run_goes_on`, `test_an_unanchored_alias_exits_1_once_models_dev_is_parsed`).

## File Structure

Created:

| File | Responsibility |
|---|---|
| `modeltiers/src/modeltiers/tokens.py` | `DROP`, `tokens`, `is_floating` |
| `modeltiers/src/modeltiers/aliases.py` | `Alias`, `Ignore`, `Aliases`; `parse_aliases`, `load_aliases`, `build`; `check_anchored`, `stale_ids` |
| `modeltiers/src/modeltiers/data/aliases.toml` | The committed aliases and ignores: the spec's three entries. Package data |
| `modeltiers/src/modeltiers/identity.py` | `Index`, `build_index`, `host_row`; `RowIdentity`, `resolve_row`; `Match`, `SourceIdentity`, `resolve_record`, `resolve_source` |
| `modeltiers/src/modeltiers/pipeline.py` | `Identity`, `resolve_all` |
| `modeltiers/src/modeltiers/report.py` | `identity_lines` |
| `modeltiers/tests/test_tokens.py` | The token table and the spec's named cases |
| `modeltiers/tests/test_aliases.py` | Loading, shape errors, duplicate claims, the missing reason |
| `modeltiers/tests/test_aliases_checks.py` | The unanchored canonical and the stale source id |
| `modeltiers/tests/test_data_files.py` | The committed `aliases.toml` loads and validates |
| `modeltiers/tests/test_identity.py` | The index, the host lookup, inventory rows |
| `modeltiers/tests/test_identity_sources.py` | Source records, records that share a key, ignores |
| `modeltiers/tests/test_pipeline.py` | `resolve_all` over the golden fixtures |
| `modeltiers/tests/test_report.py` | The identity lines for an empty inventory; column width |
| `modeltiers/tests/fixtures/aliases.toml` | The fixture-local aliases: one alias, two ignores |

Modified:

| File | Change |
|---|---|
| `modeltiers/src/modeltiers/cli.py` | `Deps.aliases_path`; `_run` loads the aliases first, resolves, prints the identity section; a new last line |
| `modeltiers/tests/test_cli.py` | `CLEAN_REPORT` gains the identity section; `_deps` passes the fixture aliases; four new tests |
| `modeltiers/tests/fixtures/modelsdev.json` | Three `deepinfra` rows, for the keys `google/gemma-4-31b-it`, `alibaba/qwen3.8-27b` and `deepseek/deepseek-v4-pro` (8 rows become 11) |
| `modeltiers/tests/fixtures/inventory.json` | Two rows: `ollama/minimax-m3:cloud` and `ollama/nomic-embed-text:latest` (6 non-native rows become 8) |
| `modeltiers/tests/conftest.py`, `test_source_modelsdev.py`, `test_loader.py`, `test_inventory.py` | The row counts those two fixtures pin |
| `modeltiers/tests/fixtures/README.md` | A row for `aliases.toml`; "eight" becomes "eleven" |
| `modeltiers/CLAUDE.md` | The package is at step 2: the command's output, the aliases file, five pipeline rows, an Identity section, two testing patterns |
| `CLAUDE.md` (lines 19 and 34, both added by step 1) | The Commands line says what `run` now prints and that an invalid `aliases.toml` exits 1; the Architecture bullet names `aliases.toml` |

---

### Task 1: The token function

**Files:**
- Create: `modeltiers/src/modeltiers/tokens.py`
- Test: `modeltiers/tests/test_tokens.py`

**Interfaces:**
- Consumes: `modeltiers.sources.arena.split_effort(name: str) -> tuple[str, str | None]` (step 1, Task 6), in the test only.
- Produces:
  - `modeltiers.tokens.DROP: frozenset[str]`, the spec's fourteen pieces.
  - `tokens(name: str) -> tuple[str, ...]`. The one function used for a model name and for a canonical key.
  - `is_floating(key: str) -> bool`. True for a key that ends in `-latest`, in any case.

- [ ] **Step 1: Write the failing test**

The table covers every case the spec's Testing section lists for the token function. The `-latest` case is `is_floating` here; Task 4 tests that the index leaves such a key out.

Write `modeltiers/tests/test_tokens.py`:

```python
import pytest

from modeltiers.sources.arena import split_effort
from modeltiers.tokens import DROP, is_floating, tokens


@pytest.mark.parametrize(
    ("name", "expected"),
    [
        # The spec's worked examples: three spellings of one model.
        ("Qwen3.8-27B-4bit", ("qwen", "3", "8", "27b")),
        ("qwen3.8:27b-mlx", ("qwen", "3", "8", "27b")),
        ("alibaba/qwen3.8-27b", ("qwen", "3", "8", "27b")),
        ("Claude Opus 5.5", ("claude", "opus", "5", "5")),
        ("anthropic/claude-opus-5-5", ("claude", "opus", "5", "5")),
        # A digit followed by letters stays one piece.
        ("qwen3-8b", ("qwen", "3", "8b")),
        ("gpt-oss:120b-cloud", ("gpt", "oss", "120b")),
        # Everything up to the first slash goes: vendor, uploader, or nothing.
        ("z-ai/glm-5.3", ("glm", "5", "3")),
        ("zhipuai/glm-5.3", ("glm", "5", "3")),
        ("mlx-community/Qwen3.8-27B-4bit", ("qwen", "3", "8", "27b")),
        ("GLM-5.3", ("glm", "5", "3")),
        # Only the first slash is a prefix; a later one is a separator.
        ("a/b/c", ("b", "c")),
        # A parenthesis is a separator, so `(0813)` stays as a piece.
        ("DeepSeek V4 Pro (0813)", ("deepseek", "v", "4", "pro", "0813")),
        ("deepseek/deepseek-v4-pro-0813", ("deepseek", "v", "4", "pro", "0813")),
        # `max` is not in the drop list: `Qwen3.8 Max` is a model.
        ("Qwen3.8 Max", ("qwen", "3", "8", "max")),
        # `it` is dropped from the name and from the key.
        ("Gemma 4 31B IT", ("gemma", "4", "31b")),
        ("google/gemma-4-31b-it", ("gemma", "4", "31b")),
        ("gemma4:31b-mlx", ("gemma", "4", "31b")),
        # Dropped whole, before the letter-digit split could cut them up.
        ("Qwen3.8-27B-nvfp4", ("qwen", "3", "8", "27b")),
        ("qwen3.8-27b-bf16", ("qwen", "3", "8", "27b")),
        ("model_fp8", ("model",)),
        # Mixed case and underscores.
        ("minimax/MiniMax-M3", ("minimax", "m", "3")),
        ("Minimax M3", ("minimax", "m", "3")),
        ("Qwen-1_8B", ("qwen", "1", "8b")),
        # A dot is cut only between digits.
        ("v1.x", ("v", "1.x")),
        ("lfm-2.5-2.6b", ("lfm", "2", "5", "2", "6b")),
        # Nothing left.
        ("", ()),
        ("vendor/", ()),
        ("mlx-4bit:latest", ()),
    ],
)
def test_tokens(name: str, expected: tuple[str, ...]) -> None:
    assert tokens(name) == expected


def test_qwen3_8_never_matches_qwen3_8b() -> None:
    assert tokens("qwen3.8") == ("qwen", "3", "8")
    assert tokens("qwen3-8b") != tokens("qwen3.8")


def test_an_arena_name_matches_its_key_once_the_effort_is_removed() -> None:
    name, effort = split_effort("Claude Opus 5.5 (High)")
    assert effort == "high"
    assert tokens(name) == tokens("anthropic/claude-opus-5-5")


def test_max_is_removed_as_an_effort_and_kept_as_a_name() -> None:
    assert tokens(split_effort("GLM 5.3 (Max)")[0]) == tokens("zhipuai/glm-5.3")
    assert tokens(split_effort("Qwen3.8 Max")[0]) == ("qwen", "3", "8", "max")


def test_the_drop_list_is_the_specs() -> None:
    assert sorted(DROP) == sorted(
        ["cloud", "free", "batch", "latest", "it", "mlx", "4bit", "6bit", "8bit"]
        + ["nvfp4", "fp4", "fp8", "fp16", "bf16"]
    )


def test_a_latest_key_is_floating() -> None:
    assert is_floating("mistral/mistral-large-latest")
    assert is_floating("Mistral/Mistral-Large-LATEST")
    assert not is_floating("mistral/mistral-large-4")
    assert not is_floating("vendor/latest-model")
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_tokens.py -q`
Expected: FAIL at collection with `ModuleNotFoundError: No module named 'modeltiers.tokens'`.

- [ ] **Step 3: Write the token function**

`re.split` on a pattern that can match the empty string cuts at each such position (Python 3.7 and later), which is how a piece is cut between a letter and a digit without consuming either.

Write `modeltiers/src/modeltiers/tokens.py`:

```python
"""The token function: a model name or a canonical key reduced to the pieces
two spellings of one model share."""

import re

# Pieces that say where or how a model is served, never which model it is.
DROP = frozenset(
    {
        "cloud",
        "free",
        "batch",
        "latest",
        "it",
        "mlx",
        "4bit",
        "6bit",
        "8bit",
        "nvfp4",
        "fp4",
        "fp8",
        "fp16",
        "bf16",
    }
)
_SEPARATORS = re.compile(r"[ \-_/:()]+")
# Between a letter and a digit, and at a `.` that has a digit on each side.
_INNER = re.compile(r"(?<=[a-z])(?=[0-9])|(?<=[0-9])\.(?=[0-9])")


def tokens(name: str) -> tuple[str, ...]:
    """The token sequence of a name or a canonical key.

    1. Lowercase; discard everything up to and including the first `/`.
    2. Split on spaces, `-`, `_`, `/`, `:`, `(` and `)`.
    3. Drop every piece in DROP.
    4. Split each piece left where a letter is followed by a digit and at
       every `.` between digits. A digit followed by letters stays whole.

    `Qwen3.8-27B-4bit`, `qwen3.8:27b-mlx` and `alibaba/qwen3.8-27b` all give
    `("qwen", "3", "8", "27b")`; `qwen3-8b` gives `("qwen", "3", "8b")`.
    """
    lowered = name.lower()
    _, slash, rest = lowered.partition("/")
    body = rest if slash else lowered
    out: list[str] = []
    for piece in _SEPARATORS.split(body):
        if not piece or piece in DROP:
            continue
        out.extend(part for part in _INNER.split(piece) if part)
    return tuple(out)


def is_floating(key: str) -> bool:
    """A canonical key that ends in `-latest` names whatever is newest, not
    one model, and is left out of the normalization index."""
    return key.lower().endswith("-latest")
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_tokens.py -q`
Expected: PASS, `34 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/tokens.py modeltiers/tests/test_tokens.py
git commit -m "feat(modeltiers): the token function that names and canonical keys share"
```

---

### Task 2: `aliases.toml`, loaded and checked before any fetch

**Files:**
- Create: `modeltiers/src/modeltiers/aliases.py`, `modeltiers/src/modeltiers/data/aliases.toml`
- Test: `modeltiers/tests/test_aliases.py`, `modeltiers/tests/test_data_files.py`, `modeltiers/tests/fixtures/aliases.toml`

**Interfaces:**
- Consumes: `modeltiers.errors.ConfigError` (step 1, Task 1); `modeltiers.sources.arena.split_effort` (step 1, Task 6).
- Produces, in `modeltiers.aliases`:
  - `SOURCES = ("openrouter", "arena", "epoch")`; `Pair = tuple[str, str]` (`(provider_id, model_name)`); `SourceId = tuple[str, str]` (`(source, id)`).
  - `Alias(canonical: str, note: str = "", inventory: tuple[Pair, ...] = (), openrouter: tuple[str, ...] = (), arena: tuple[str, ...] = (), epoch: tuple[str, ...] = ())`, frozen, with `source_ids() -> tuple[SourceId, ...]`.
  - `Ignore(reason: str, inventory: Pair | None = None, source: str | None = None, id: str | None = None)`, frozen.
  - `Aliases`, frozen, with fields `aliases: tuple[Alias, ...]`, `ignores: tuple[Ignore, ...]`, `inventory_alias: Mapping[Pair, str]`, `source_alias: Mapping[SourceId, str]`, `inventory_ignore: Mapping[Pair, str]`, `source_ignore: Mapping[SourceId, str]` and `origin: str = "aliases.toml"`. Every field has an empty default, so `Aliases()` is "no aliases". The two alias maps give the canonical as written in the file; the two ignore maps give the reason.
  - `build(aliases: Iterable[Alias], ignores: Iterable[Ignore], *, origin: str = "aliases.toml") -> Aliases`. Raises `ConfigError` for a pair or a source id claimed twice.
  - `parse_aliases(text: str, *, origin: str = "aliases.toml") -> Aliases`. Raises `ConfigError`.
  - `load_aliases(path: Path | None = None) -> Aliases`. `None` reads the committed `data/aliases.toml` through `importlib.resources`. Raises `ConfigError`.

- [ ] **Step 1: Write the fixture and the failing tests**

The fixture file is the spec's example with one change: the ignored Epoch id is `GPT-5`, a row the Epoch fixture holds and no models.dev fixture key matches.

Write `modeltiers/tests/fixtures/aliases.toml`:

```toml
[[alias]]
canonical = "deepseek/deepseek-v4-pro-0813"
note = "ollama :cloud and :0813-cloud shared digest 6ed7420bc3a4 on 2026-10-12"
inventory = [{ provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }]
arena = ["DeepSeek V4 Pro (0813)"]
epoch = ["DeepSeek V4 Pro 0813"]

[[ignore]]
inventory = { provider_id = "ollama", model_name = "nomic-embed-text:latest" }
reason = "embedding model"

[[ignore]]
source = "epoch"
id = "GPT-5"
reason = "fixture: stands for a name no key will ever match"
```

Write `modeltiers/tests/test_aliases.py`:

```python
from pathlib import Path

import pytest

from modeltiers import aliases
from modeltiers.aliases import Alias, Aliases, Ignore
from modeltiers.errors import ConfigError

FIXTURE = Path(__file__).parent / "fixtures" / "aliases.toml"

ALIAS = """
[[alias]]
canonical = "deepseek/deepseek-v4-pro-0813"
inventory = [{ provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }]
arena = ["DeepSeek V4 Pro (0813)"]
"""


def test_the_fixture_file_loads() -> None:
    loaded = aliases.load_aliases(FIXTURE)
    assert loaded.origin == "aliases.toml"
    assert loaded.aliases == (
        Alias(
            canonical="deepseek/deepseek-v4-pro-0813",
            note="ollama :cloud and :0813-cloud shared digest 6ed7420bc3a4 on 2026-10-12",
            inventory=(("ollama", "deepseek-v4-pro:cloud"),),
            arena=("DeepSeek V4 Pro (0813)",),
            epoch=("DeepSeek V4 Pro 0813",),
        ),
    )
    assert loaded.ignores == (
        Ignore(reason="embedding model", inventory=("ollama", "nomic-embed-text:latest")),
        Ignore(
            reason="fixture: stands for a name no key will ever match", source="epoch", id="GPT-5"
        ),
    )
    assert loaded.inventory_alias == {
        ("ollama", "deepseek-v4-pro:cloud"): "deepseek/deepseek-v4-pro-0813"
    }
    assert loaded.source_alias == {
        ("arena", "DeepSeek V4 Pro (0813)"): "deepseek/deepseek-v4-pro-0813",
        ("epoch", "DeepSeek V4 Pro 0813"): "deepseek/deepseek-v4-pro-0813",
    }
    assert loaded.inventory_ignore == {("ollama", "nomic-embed-text:latest"): "embedding model"}
    assert loaded.source_ignore == {
        ("epoch", "GPT-5"): "fixture: stands for a name no key will ever match"
    }


def test_an_empty_file_is_no_aliases() -> None:
    assert aliases.parse_aliases("") == Aliases()


def test_an_arena_id_loses_its_effort_parenthesis() -> None:
    parsed = aliases.parse_aliases(
        """
[[alias]]
canonical = "deepseek/deepseek-v4-pro-0813"
arena = ["DeepSeek V4 Pro (High) (0813)"]

[[ignore]]
source = "arena"
id = "Gemini 4 Argon (Max)"
reason = "no models.dev key yet"

[[ignore]]
source = "epoch"
id = "Gemini 2.5 Pro (Max)"
reason = "only Arena ids carry an effort"
"""
    )
    assert ("arena", "DeepSeek V4 Pro (0813)") in parsed.source_alias
    assert ("arena", "Gemini 4 Argon") in parsed.source_ignore
    assert ("epoch", "Gemini 2.5 Pro (Max)") in parsed.source_ignore


def test_a_duplicate_inventory_pair_is_an_error() -> None:
    text = (
        ALIAS
        + """
[[alias]]
canonical = "deepseek/deepseek-v4-pro"
inventory = [{ provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }]
"""
    )
    with pytest.raises(ConfigError) as caught:
        aliases.parse_aliases(text)
    assert str(caught.value) == (
        "aliases.toml: inventory pair ollama/deepseek-v4-pro:cloud is claimed twice: "
        "by [[alias]] #1 (deepseek/deepseek-v4-pro-0813) "
        "and by [[alias]] #2 (deepseek/deepseek-v4-pro)"
    )


def test_an_alias_and_an_ignore_for_one_pair_is_an_error() -> None:
    text = (
        ALIAS
        + """
[[ignore]]
inventory = { provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }
reason = "listed twice"
"""
    )
    with pytest.raises(
        ConfigError, match=r"claimed twice: by \[\[alias\]\] #1 .* \[\[ignore\]\] #1"
    ):
        aliases.parse_aliases(text)


def test_a_duplicate_source_id_is_an_error() -> None:
    # The second claim spells the Arena id with its effort; it is the same id.
    text = (
        ALIAS
        + """
[[alias]]
canonical = "deepseek/deepseek-v4-pro"
arena = ["DeepSeek V4 Pro (High) (0813)"]
"""
    )
    with pytest.raises(ConfigError) as caught:
        aliases.parse_aliases(text)
    assert str(caught.value) == (
        "aliases.toml: arena id 'DeepSeek V4 Pro (0813)' is claimed twice: "
        "by [[alias]] #1 (deepseek/deepseek-v4-pro-0813) "
        "and by [[alias]] #2 (deepseek/deepseek-v4-pro)"
    )


def test_one_id_under_two_sources_is_two_ids() -> None:
    parsed = aliases.parse_aliases(
        """
[[alias]]
canonical = "zhipuai/glm-5.3"
arena = ["GLM-5.3"]
epoch = ["GLM-5.3"]
"""
    )
    assert len(parsed.source_alias) == 2


def test_one_entry_that_lists_an_id_twice_is_an_error() -> None:
    text = '[[alias]]\ncanonical = "zhipuai/glm-5.3"\nepoch = ["GLM-5.3", "GLM-5.3"]\n'
    with pytest.raises(ConfigError, match="epoch id 'GLM-5.3' is claimed twice"):
        aliases.parse_aliases(text)


@pytest.mark.parametrize(
    "entry",
    [
        'inventory = { provider_id = "ollama", model_name = "nomic-embed-text:latest" }',
        'inventory = { provider_id = "ollama", model_name = "x" }\nreason = ""',
        'inventory = { provider_id = "ollama", model_name = "x" }\nreason = "  "',
        'source = "epoch"\nid = "Mistral Large"\nreason = 7',
    ],
)
def test_an_ignore_without_a_reason_is_an_error(entry: str) -> None:
    with pytest.raises(ConfigError) as caught:
        aliases.parse_aliases(f"[[ignore]]\n{entry}\n")
    assert str(caught.value) == "aliases.toml: [[ignore]] #1: an ignore needs a reason"


@pytest.mark.parametrize(
    ("text", "message"),
    [
        ("[[alias]", "aliases.toml: not TOML"),
        ("[alias]\ncanonical = 'a/b'\n", r"alias is not an array of tables; write \[\[alias\]\]"),
        ("[[aliases]]\ncanonical = 'a/b'\n", "unknown table 'aliases'"),
        ("[[alias]]\nnote = 'x'\n", r"\[\[alias\]\] #1: canonical is missing, empty"),
        ("[[alias]]\ncanonical = 'a/b'\nopenroutr = ['x']\n", r"#1: unknown key 'openroutr'"),
        ("[[alias]]\ncanonical = 'a/b'\nnote = 7\n", r"#1 \(a/b\): note is not a string"),
        ("[[alias]]\ncanonical = 'a/b'\narena = 'x'\n", "arena is not a list of non-empty"),
        ("[[alias]]\ncanonical = 'a/b'\nepoch = ['']\n", "epoch is not a list of non-empty"),
        (
            "[[alias]]\ncanonical = 'a/b'\ninventory = { provider_id = 'p', model_name = 'm' }\n",
            "inventory is not a list of pairs",
        ),
        (
            "[[alias]]\ncanonical = 'a/b'\ninventory = ['ollama/x']\n",
            r"inventory\[0\]: expected \{ provider_id = ..., model_name = ... \}",
        ),
        (
            "[[alias]]\ncanonical = 'a/b'\ninventory = [{ provider_id = 'p' }]\n",
            r"inventory\[0\]: model_name is missing",
        ),
        (
            "[[alias]]\ncanonical = 'a/b'\n"
            "inventory = [{ provider_id = 'p', model_name = 'm', id = 'p/m' }]\n",
            r"inventory\[0\]: unknown key 'id'",
        ),
        (
            "[[ignore]]\nreason = 'r'\n",
            r"\[\[ignore\]\] #1: give either inventory, or source and id",
        ),
        (
            "[[ignore]]\nreason = 'r'\nsource = 'epoch'\nid = 'x'\n"
            "inventory = { provider_id = 'p', model_name = 'm' }\n",
            "give either inventory, or source and id",
        ),
        ("[[ignore]]\nreason = 'r'\nsource = 'epoch'\n", "#1: id is missing"),
        ("[[ignore]]\nreason = 'r'\nid = 'x'\n", "#1: source is missing"),
        (
            "[[ignore]]\nreason = 'r'\nsource = 'modelsdev'\nid = 'x'\n",
            "source 'modelsdev' is not one of openrouter, arena, epoch",
        ),
        (
            "[[ignore]]\nreason = 'r'\nsource = 'epoch'\nid = 'x'\nnote = 'n'\n",
            "unknown key 'note'",
        ),
    ],
)
def test_a_file_in_another_shape_is_a_config_error(text: str, message: str) -> None:
    with pytest.raises(ConfigError, match=message):
        aliases.parse_aliases(text)


def test_the_message_names_the_file_it_was_given(tmp_path: Path) -> None:
    path = tmp_path / "mine.toml"
    path.write_text("[[ignore]]\nsource = 'epoch'\nid = 'x'\n")
    with pytest.raises(
        ConfigError, match=r"^mine\.toml: \[\[ignore\]\] #1: an ignore needs a reason$"
    ):
        aliases.load_aliases(path)


def test_a_missing_file_is_a_config_error(tmp_path: Path) -> None:
    with pytest.raises(ConfigError, match="aliases: cannot read .*absent.toml"):
        aliases.load_aliases(tmp_path / "absent.toml")
```

`tests/test_data_files.py` is the one test that reads a committed data file. It asserts nothing about which entries the file holds, so step 4 can add aliases without touching it. Step 3 adds the `contracts.toml` test here.

Write `modeltiers/tests/test_data_files.py`:

```python
"""The one test that reads the committed data files. Every other test uses
the copies under tests/fixtures/, so calibration can change the committed
values without changing a test."""

from modeltiers import aliases


def test_the_committed_aliases_file_loads_and_validates() -> None:
    loaded = aliases.load_aliases()
    assert loaded.origin == "aliases.toml"
    assert loaded.aliases or loaded.ignores
    assert all("/" in alias.canonical for alias in loaded.aliases)
```

- [ ] **Step 2: Run the tests and see them fail**

Run: `uv run --directory modeltiers pytest tests/test_aliases.py tests/test_data_files.py -q`
Expected: FAIL at collection of both files with `ImportError: cannot import name 'aliases' from 'modeltiers'`.

- [ ] **Step 3: Write the loader**

Every message starts with the file's name and names the entry by its table and its position, counted from 1: `aliases.toml: [[ignore]] #2: an ignore needs a reason`.

Write `modeltiers/src/modeltiers/aliases.py`:

```python
"""`aliases.toml`: the hand-kept answers the resolver tries first.

`[[alias]]` pins inventory pairs and source ids to a canonical key.
`[[ignore]]` acknowledges a row or a record that will never resolve.
"""

import tomllib
from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field
from importlib import resources
from pathlib import Path
from typing import Any

from modeltiers.errors import ConfigError
from modeltiers.sources.arena import split_effort

SOURCES = ("openrouter", "arena", "epoch")
Pair = tuple[str, str]  # (provider_id, model_name)
SourceId = tuple[str, str]  # (source, the source's own id)

_ALIAS_KEYS = frozenset({"canonical", "note", "inventory", *SOURCES})
_IGNORE_KEYS = frozenset({"reason", "inventory", "source", "id"})
_PAIR_KEYS = frozenset({"provider_id", "model_name"})


@dataclass(frozen=True)
class Alias:
    """One `[[alias]]` entry. Arena ids have their effort parenthesis removed."""

    canonical: str
    note: str = ""
    inventory: tuple[Pair, ...] = ()
    openrouter: tuple[str, ...] = ()
    arena: tuple[str, ...] = ()
    epoch: tuple[str, ...] = ()

    def source_ids(self) -> tuple[SourceId, ...]:
        """Every (source, id) the entry lists, in the order of SOURCES."""
        return (
            *(("openrouter", sid) for sid in self.openrouter),
            *(("arena", sid) for sid in self.arena),
            *(("epoch", sid) for sid in self.epoch),
        )


@dataclass(frozen=True)
class Ignore:
    """One `[[ignore]]` entry: an inventory pair, or a source and its id."""

    reason: str
    inventory: Pair | None = None
    source: str | None = None
    id: str | None = None


@dataclass(frozen=True)
class Aliases:
    """The file, with the four lookups the resolver needs. Build one with
    `parse_aliases` or `build`, which refuse a pair or an id claimed twice."""

    aliases: tuple[Alias, ...] = ()
    ignores: tuple[Ignore, ...] = ()
    inventory_alias: Mapping[Pair, str] = field(default_factory=dict)  # pair -> canonical
    source_alias: Mapping[SourceId, str] = field(default_factory=dict)  # id -> canonical
    inventory_ignore: Mapping[Pair, str] = field(default_factory=dict)  # pair -> reason
    source_ignore: Mapping[SourceId, str] = field(default_factory=dict)  # id -> reason
    origin: str = "aliases.toml"  # the file's name, for messages


def _alias_label(index: int, alias: Alias) -> str:
    return f"[[alias]] #{index} ({alias.canonical})"


def build(
    aliases: Iterable[Alias], ignores: Iterable[Ignore], *, origin: str = "aliases.toml"
) -> Aliases:
    """Index the entries. Two claims on one inventory pair or one source id,
    in two entries or in one, are a ConfigError that names both claimants."""
    kept_aliases = tuple(aliases)
    kept_ignores = tuple(ignores)
    pair_owner: dict[Pair, str] = {}
    id_owner: dict[SourceId, str] = {}

    def claim_pair(pair: Pair, label: str) -> None:
        if pair in pair_owner:
            raise ConfigError(
                f"{origin}: inventory pair {pair[0]}/{pair[1]} is claimed twice: "
                f"by {pair_owner[pair]} and by {label}"
            )
        pair_owner[pair] = label

    def claim_id(source_id: SourceId, label: str) -> None:
        if source_id in id_owner:
            raise ConfigError(
                f"{origin}: {source_id[0]} id {source_id[1]!r} is claimed twice: "
                f"by {id_owner[source_id]} and by {label}"
            )
        id_owner[source_id] = label

    inventory_alias: dict[Pair, str] = {}
    source_alias: dict[SourceId, str] = {}
    inventory_ignore: dict[Pair, str] = {}
    source_ignore: dict[SourceId, str] = {}
    for index, alias in enumerate(kept_aliases, start=1):
        label = _alias_label(index, alias)
        for pair in alias.inventory:
            claim_pair(pair, label)
            inventory_alias[pair] = alias.canonical
        for source_id in alias.source_ids():
            claim_id(source_id, label)
            source_alias[source_id] = alias.canonical
    for index, ignore in enumerate(kept_ignores, start=1):
        label = f"[[ignore]] #{index}"
        if ignore.inventory is not None:
            claim_pair(ignore.inventory, label)
            inventory_ignore[ignore.inventory] = ignore.reason
        if ignore.source is not None and ignore.id is not None:
            claim_id((ignore.source, ignore.id), label)
            source_ignore[(ignore.source, ignore.id)] = ignore.reason
    return Aliases(
        aliases=kept_aliases,
        ignores=kept_ignores,
        inventory_alias=inventory_alias,
        source_alias=source_alias,
        inventory_ignore=inventory_ignore,
        source_ignore=source_ignore,
        origin=origin,
    )


def _text(where: str, entry: Mapping[str, Any], key: str) -> str:
    value = entry.get(key)
    if not isinstance(value, str) or not value.strip():
        raise ConfigError(f"{where}: {key} is missing, empty or not a string")
    return value


def _known_keys(where: str, entry: Mapping[str, Any], allowed: frozenset[str]) -> None:
    unknown = sorted(set(entry) - allowed)
    if unknown:
        raise ConfigError(f"{where}: unknown key {unknown[0]!r}")


def _pair(where: str, value: Any) -> Pair:
    if not isinstance(value, dict):
        raise ConfigError(f"{where}: expected {{ provider_id = ..., model_name = ... }}")
    _known_keys(where, value, _PAIR_KEYS)
    return (
        _text(where, value, "provider_id"),
        _text(where, value, "model_name"),
    )


def _id_list(where: str, entry: Mapping[str, Any], source: str) -> tuple[str, ...]:
    value = entry.get(source, [])
    if not isinstance(value, list) or not all(isinstance(v, str) and v.strip() for v in value):
        raise ConfigError(f"{where}: {source} is not a list of non-empty strings")
    if source == "arena":
        return tuple(split_effort(v)[0] for v in value)
    return tuple(value)


def _tables(origin: str, doc: Mapping[str, Any], key: str) -> list[dict[str, Any]]:
    value = doc.get(key, [])
    if not isinstance(value, list) or not all(isinstance(v, dict) for v in value):
        raise ConfigError(f"{origin}: {key} is not an array of tables; write [[{key}]]")
    return value


def _alias(where: str, entry: Mapping[str, Any]) -> Alias:
    _known_keys(where, entry, _ALIAS_KEYS)
    canonical = _text(where, entry, "canonical")
    where = f"{where} ({canonical})"
    note = entry.get("note", "")
    pairs = entry.get("inventory", [])
    if not isinstance(note, str):
        raise ConfigError(f"{where}: note is not a string")
    if not isinstance(pairs, list):
        raise ConfigError(f"{where}: inventory is not a list of pairs")
    return Alias(
        canonical=canonical,
        note=note,
        inventory=tuple(
            _pair(f"{where} inventory[{position}]", pair) for position, pair in enumerate(pairs)
        ),
        openrouter=_id_list(where, entry, "openrouter"),
        arena=_id_list(where, entry, "arena"),
        epoch=_id_list(where, entry, "epoch"),
    )


def _ignore(where: str, entry: Mapping[str, Any]) -> Ignore:
    _known_keys(where, entry, _IGNORE_KEYS)
    reason = entry.get("reason")
    if not isinstance(reason, str) or not reason.strip():
        raise ConfigError(f"{where}: an ignore needs a reason")
    has_pair = "inventory" in entry
    has_source = "source" in entry or "id" in entry
    if has_pair == has_source:
        raise ConfigError(f"{where}: give either inventory, or source and id")
    if has_pair:
        return Ignore(reason=reason, inventory=_pair(f"{where} inventory", entry["inventory"]))
    source = _text(where, entry, "source")
    if source not in SOURCES:
        raise ConfigError(f"{where}: source {source!r} is not one of {', '.join(SOURCES)}")
    source_id = _text(where, entry, "id")
    if source == "arena":
        source_id = split_effort(source_id)[0]
    return Ignore(reason=reason, source=source, id=source_id)


def parse_aliases(text: str, *, origin: str = "aliases.toml") -> Aliases:
    """Read and validate the file's text. This is the validation that runs
    before any fetch: shape, an `[[ignore]]` without a reason, and a pair or
    an id claimed twice. `origin` names the file in every message."""
    try:
        doc = tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        raise ConfigError(f"{origin}: not TOML: {exc}") from exc
    unknown = sorted(set(doc) - {"alias", "ignore"})
    if unknown:
        raise ConfigError(f"{origin}: unknown table {unknown[0]!r}")
    aliases = [
        _alias(f"{origin}: [[alias]] #{index}", entry)
        for index, entry in enumerate(_tables(origin, doc, "alias"), start=1)
    ]
    ignores = [
        _ignore(f"{origin}: [[ignore]] #{index}", entry)
        for index, entry in enumerate(_tables(origin, doc, "ignore"), start=1)
    ]
    return build(aliases, ignores, origin=origin)


def load_aliases(path: Path | None = None) -> Aliases:
    """Read `path`, or the committed `data/aliases.toml` when it is None."""
    if path is None:
        text = resources.files("modeltiers").joinpath("data/aliases.toml").read_text("utf-8")
        return parse_aliases(text, origin="aliases.toml")
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ConfigError(f"aliases: cannot read {path}: {exc.strerror}") from exc
    return parse_aliases(text, origin=path.name)
```

- [ ] **Step 4: Write the committed data file**

The spec's three entries. Each id was checked against the 2026-10-12 downloads: `deepseek/deepseek-v4-pro-0813` is a models.dev key, `DeepSeek V4 Pro (High) (0813)` is an Arena row (the id below is that name with its effort removed), `DeepSeek V4 Pro 0813` and `Mistral Large` are Epoch display names. hatchling puts every file under `src/modeltiers/` in the wheel, so `pyproject.toml` does not change.

Write `modeltiers/src/modeltiers/data/aliases.toml`:

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

[[alias]]
canonical = "deepseek/deepseek-v4-pro-0813"
note = "ollama :cloud and :0813-cloud shared digest 6ed7420bc3a4 on 2026-10-12"
inventory = [{ provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }]
arena = ["DeepSeek V4 Pro (0813)"]
epoch = ["DeepSeek V4 Pro 0813"]

[[ignore]]
inventory = { provider_id = "ollama", model_name = "nomic-embed-text:latest" }
reason = "embedding model"

[[ignore]]
source = "epoch"
id = "Mistral Large"
reason = "floating name; no fixed model"
```

- [ ] **Step 5: Run the tests and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_aliases.py tests/test_data_files.py -q`
Expected: PASS, `33 passed`.

- [ ] **Step 6: Commit**

```bash
git add modeltiers/src/modeltiers/aliases.py modeltiers/src/modeltiers/data/aliases.toml modeltiers/tests/test_aliases.py modeltiers/tests/test_data_files.py modeltiers/tests/fixtures/aliases.toml
git commit -m "feat(modeltiers): load aliases.toml and refuse duplicate claims and reasonless ignores"
```

---

### Task 3: The alias checks that need the sources

**Files:**
- Modify: `modeltiers/src/modeltiers/aliases.py` (append two functions)
- Test: `modeltiers/tests/test_aliases_checks.py`

**Interfaces:**
- Consumes: `Alias`, `Aliases`, `build`, `_alias_label` (Task 2).
- Produces, in `modeltiers.aliases`:
  - `check_anchored(aliases: Aliases, modelsdev_keys: Iterable[str]) -> None`. Raises `ConfigError` for the first alias whose `canonical` is not among `modelsdev_keys` (compared in lower case) and which lists no `openrouter`, `arena` or `epoch` id.
  - `stale_ids(aliases: Aliases, present: Mapping[str, frozenset[str]]) -> list[str]`. `present` maps a source name to the ids its records hold this run; a source missing from the mapping holds none. One warning line per alias source id not present, in file order.

- [ ] **Step 1: Write the failing test**

Write `modeltiers/tests/test_aliases_checks.py`:

```python
import pytest

from modeltiers import aliases
from modeltiers.aliases import Alias
from modeltiers.errors import ConfigError

KEYS = ["zhipuai/glm-5.3", "minimax/MiniMax-M3"]


def test_an_alias_to_a_models_dev_key_needs_no_source_id() -> None:
    entries = aliases.build([Alias("zhipuai/glm-5.3", inventory=(("ollama", "glm:cloud"),))], [])
    aliases.check_anchored(entries, KEYS)


def test_the_key_is_compared_without_regard_to_case() -> None:
    entries = aliases.build([Alias("MiniMax/minimax-m3", inventory=(("ollama", "m3"),))], [])
    aliases.check_anchored(entries, KEYS)


def test_an_unanchored_canonical_is_an_error() -> None:
    entries = aliases.build(
        [
            Alias("zhipuai/glm-5.3", inventory=(("ollama", "glm:cloud"),)),
            Alias("google/gemini-4-argon", inventory=(("openrouter", "google/gemini-4-argon"),)),
        ],
        [],
    )
    with pytest.raises(ConfigError) as caught:
        aliases.check_anchored(entries, KEYS)
    assert str(caught.value) == (
        "aliases.toml: [[alias]] #2 (google/gemini-4-argon): canonical is not a models.dev key "
        "and the entry lists no openrouter, arena or epoch id"
    )


@pytest.mark.parametrize("source", ["openrouter", "arena", "epoch"])
def test_one_source_id_anchors_a_declared_key(source: str) -> None:
    alias = Alias("google/gemini-4-argon", **{source: ("Gemini 4 Argon",)})
    aliases.check_anchored(aliases.build([alias], []), KEYS)


def test_a_source_id_absent_from_this_run_is_a_warning() -> None:
    entries = aliases.build(
        [
            Alias(
                "deepseek/deepseek-v4-pro-0813",
                openrouter=("deepseek/deepseek-v4-pro-0813",),
                arena=("DeepSeek V4 Pro (0813)",),
                epoch=("DeepSeek V4 Pro 0813",),
            )
        ],
        [],
    )
    present = {
        "openrouter": frozenset({"deepseek/deepseek-v4-pro-0813"}),
        "arena": frozenset({"GLM 5.3"}),
        # no "epoch" entry at all: every Epoch id is absent
    }
    assert aliases.stale_ids(entries, present) == [
        "alias for deepseek/deepseek-v4-pro-0813 names arena id 'DeepSeek V4 Pro (0813)', "
        "which is not in this run's arena data",
        "alias for deepseek/deepseek-v4-pro-0813 names epoch id 'DeepSeek V4 Pro 0813', "
        "which is not in this run's epoch data",
    ]


def test_ids_that_are_present_give_no_warning() -> None:
    entries = aliases.build([Alias("zhipuai/glm-5.3", epoch=("GLM-5.3",))], [])
    assert aliases.stale_ids(entries, {"epoch": frozenset({"GLM-5.3"})}) == []
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_aliases_checks.py -q`
Expected: FAIL, `8 failed`: six with `AttributeError: module 'modeltiers.aliases' has no attribute 'check_anchored'` and the two `stale_ids` tests with the same error naming `stale_ids`.

- [ ] **Step 3: Add the two checks**

Append to the end of `modeltiers/src/modeltiers/aliases.py`, after two blank lines:

```python
def check_anchored(aliases: Aliases, modelsdev_keys: Iterable[str]) -> None:
    """The validation that needs models.dev: an alias whose canonical is not
    a models.dev key (compared without regard to case) must list at least one
    source id. One that lists only inventory pairs would create a key no
    source can ever score."""
    known = {key.lower() for key in modelsdev_keys}
    for index, alias in enumerate(aliases.aliases, start=1):
        if alias.canonical.lower() not in known and not alias.source_ids():
            raise ConfigError(
                f"{aliases.origin}: {_alias_label(index, alias)}: canonical is not a models.dev key "
                "and the entry lists no openrouter, arena or epoch id"
            )


def stale_ids(aliases: Aliases, present: Mapping[str, frozenset[str]]) -> list[str]:
    """One warning per alias source id that this run's snapshot of the source
    does not hold. Models leave leaderboards; the alias may be out of date."""
    return [
        f"alias for {alias.canonical} names {source} id {source_id!r}, "
        f"which is not in this run's {source} data"
        for alias in aliases.aliases
        for source, source_id in alias.source_ids()
        if source_id not in present.get(source, frozenset())
    ]
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_aliases_checks.py tests/test_aliases.py -q`
Expected: PASS, `40 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/aliases.py modeltiers/tests/test_aliases_checks.py
git commit -m "feat(modeltiers): refuse an unanchored alias and warn about a stale source id"
```

---

### Task 4: The key index and inventory-row resolution

**Files:**
- Create: `modeltiers/src/modeltiers/identity.py`
- Test: `modeltiers/tests/test_identity.py`

**Interfaces:**
- Consumes: `modeltiers.tokens.tokens`, `is_floating` (Task 1); `modeltiers.aliases.Aliases`, `Alias`, `Ignore`, `build` (Task 2); `modeltiers.records.InventoryRow`, `ModelsDev`, `ModelsDevRow` (step 1, Task 2).
- Produces, in `modeltiers.identity`:
  - `ALIAS = "alias"`, `HOST = "host"`, `NORMALIZED = "normalized"`, `MEASURED = "measured"`, `INHERITED = "inherited"`, `HOST_NOTE = "host row has no canonical id; matched by name"`.
  - `Index(keys: Mapping[str, str], rows: Mapping[str, tuple[ModelsDevRow, ...]], hosts: Mapping[tuple[str, str], ModelsDevRow], by_tokens: Mapping[tuple[str, ...], tuple[str, ...]])`, frozen. `keys` and `rows` are keyed by the lower-case key; `keys` gives the stored spelling. Methods: `spelling(key: str) -> str | None`, `rows_for(key: str) -> tuple[ModelsDevRow, ...]`, `normalized(name: str) -> str | None`.
  - `build_index(modelsdev: ModelsDev, aliases: Aliases) -> Index`.
  - `host_row(index: Index, host: str, model_id: str) -> ModelsDevRow | None`. `host` is an inventory `provider_id` or a source name.
  - `RowIdentity(row: InventoryRow, canonical: str | None = None, resolved_by: str | None = None, ignore_reason: str | None = None, host_row: ModelsDevRow | None = None, notes: tuple[str, ...] = ())`, frozen, with properties `basis -> str` (`"inherited"` for a local row, else `"measured"`) and `unmapped -> bool`.
  - `resolve_row(row: InventoryRow, index: Index, aliases: Aliases) -> RowIdentity`.

- [ ] **Step 1: Write the failing test**

The tests build their own models.dev rows, so each case shows the rows it depends on. `vendor-a/twin-1` and `vendor-b/twin-1` are two keys with one token sequence; the 2026-10-12 snapshot has no such pair, and the test stands for the day it does.

Write `modeltiers/tests/test_identity.py`:

```python
import pytest

from modeltiers import aliases as aliases_mod
from modeltiers import identity
from modeltiers.aliases import Alias, Aliases, Ignore
from modeltiers.identity import RowIdentity
from modeltiers.records import InventoryRow, ModelsDev, ModelsDevRow


def _md(
    provider: str, model_id: str, canonical: str | None, context: int = 1000000
) -> ModelsDevRow:
    return ModelsDevRow(provider, model_id, canonical, True, True, context)


GLM_HOST = _md("ollama-cloud", "glm-5.3", "zhipuai/glm-5.3", 1048576)
DEEPSEEK_HOST = _md("ollama-cloud", "deepseek-v4-pro", None, 1048576)
MODELSDEV = ModelsDev(
    rows=(
        GLM_HOST,
        DEEPSEEK_HOST,
        _md("ollama-cloud", "deepseek-v4-pro:0813", "deepseek/deepseek-v4-pro-0813"),
        _md("ollama-cloud", "gpt-oss:120b", None),
        _md("openrouter", "z-ai/glm-5.3", "zhipuai/glm-5.3"),
        _md("openrouter", "minimax/minimax-m3", "minimax/MiniMax-M3"),
        _md("openrouter", "nex-agi/nex-n2.5-pro", None),
        _md("zhipuai", "glm-5.3", "zhipuai/glm-5.3"),
        _md("deepinfra", "deepseek-ai/DeepSeek-V4-Pro", "deepseek/deepseek-v4-pro"),
        _md("deepinfra", "Qwen/Qwen3.8-27B", "alibaba/qwen3.8-27b"),
        _md("deepinfra", "openai/gpt-oss-120b", "openai/gpt-oss-120b"),
        _md("mistral", "mistral-large-latest", "mistral/mistral-large-latest"),
        # Two keys with one token sequence: `a/` and `b/` are both discarded.
        _md("vendor-a", "twin-1", "vendor-a/twin-1"),
        _md("vendor-b", "twin-1", "vendor-b/twin-1"),
    )
)
DEEPSEEK_ALIAS = Alias(
    "deepseek/deepseek-v4-pro-0813", inventory=(("ollama", "deepseek-v4-pro:cloud"),)
)
NO_ALIASES = Aliases()
INDEX = identity.build_index(MODELSDEV, NO_ALIASES)


def _cloud(provider_id: str, model_name: str) -> InventoryRow:
    return InventoryRow(provider_id, model_name, "cloud", True)


def _local(provider_id: str, model_name: str) -> InventoryRow:
    return InventoryRow(provider_id, model_name, "local", True)


# --- the index ---------------------------------------------------------------


def test_keys_keep_models_dev_spelling_and_are_found_in_any_case() -> None:
    assert INDEX.spelling("minimax/minimax-m3") == "minimax/MiniMax-M3"
    assert INDEX.spelling("MINIMAX/MINIMAX-M3") == "minimax/MiniMax-M3"
    assert INDEX.spelling("minimax/minimax-m4") is None


def test_rows_for_a_key_are_every_models_dev_row_that_names_it() -> None:
    assert [r.provider for r in INDEX.rows_for("ZhipuAI/GLM-5.3")] == [
        "ollama-cloud",
        "openrouter",
        "zhipuai",
    ]
    assert INDEX.rows_for("nobody/nothing") == ()


def test_a_latest_key_is_valid_and_left_out_of_the_normalization_index() -> None:
    assert INDEX.spelling("mistral/mistral-large-latest") == "mistral/mistral-large-latest"
    # `latest` is in the drop list, so without the exclusion this would match.
    assert INDEX.normalized("Mistral Large") is None
    assert all("latest" not in key for keys in INDEX.by_tokens.values() for key in keys)


def test_a_name_that_matches_two_keys_does_not_answer() -> None:
    assert INDEX.by_tokens[("twin", "1")] == ("vendor-a/twin-1", "vendor-b/twin-1")
    assert INDEX.normalized("Twin 1") is None


def test_a_name_with_no_tokens_does_not_answer() -> None:
    assert INDEX.normalized("mlx-4bit:latest") is None


def test_an_alias_declares_a_key_and_models_dev_spelling_wins() -> None:
    declared = aliases_mod.build(
        [
            Alias("google/gemini-4-argon", arena=("Gemini 4 Argon",)),
            Alias("MINIMAX/minimax-m3", arena=("Minimax M3",)),
        ],
        [],
    )
    index = identity.build_index(MODELSDEV, declared)
    assert index.spelling("google/gemini-4-argon") == "google/gemini-4-argon"
    assert index.rows_for("google/gemini-4-argon") == ()
    assert index.normalized("Gemini 4 Argon") == "google/gemini-4-argon"
    assert index.spelling("minimax/minimax-m3") == "minimax/MiniMax-M3"


# --- the host lookup ---------------------------------------------------------


@pytest.mark.parametrize(
    ("host", "model_id", "found"),
    [
        ("ollama", "glm-5.3:cloud", ("ollama-cloud", "glm-5.3")),
        ("ollama", "gpt-oss:120b-cloud", ("ollama-cloud", "gpt-oss:120b")),
        ("ollama", "deepseek-v4-pro:0813-cloud", ("ollama-cloud", "deepseek-v4-pro:0813")),
        ("ollama", "glm-5.3", ("ollama-cloud", "glm-5.3")),
        ("openrouter", "z-ai/glm-5.3", ("openrouter", "z-ai/glm-5.3")),
        ("openrouter", "z-ai/glm-5.3:free", ("openrouter", "z-ai/glm-5.3")),
        ("openrouter", "z-ai/glm-5.3:batch", ("openrouter", "z-ai/glm-5.3")),
        ("openrouter", "z-ai/glm-5.3:cloud", None),  # not an OpenRouter suffix
        ("openrouter", "glm-5.3", None),  # an ollama id, not an OpenRouter one
        ("omlx", "glm-5.3", None),  # models.dev lists no such host
        ("arena", "glm-5.3", None),
    ],
)
def test_host_row(host: str, model_id: str, found: tuple[str, str] | None) -> None:
    row = identity.host_row(INDEX, host, model_id)
    assert (None if row is None else (row.provider, row.model_id)) == found


# --- inventory rows ----------------------------------------------------------


def test_a_cloud_row_resolves_through_its_host_row() -> None:
    row = _cloud("ollama", "glm-5.3:cloud")
    assert identity.resolve_row(row, INDEX, NO_ALIASES) == RowIdentity(
        row=row, canonical="zhipuai/glm-5.3", resolved_by="host", host_row=GLM_HOST
    )


def test_the_stored_key_is_models_dev_spelling_whatever_answered() -> None:
    by_host = identity.resolve_row(_cloud("openrouter", "minimax/minimax-m3"), INDEX, NO_ALIASES)
    assert (by_host.canonical, by_host.resolved_by) == ("minimax/MiniMax-M3", "host")
    by_name = identity.resolve_row(_cloud("ollama", "minimax-m3:cloud"), INDEX, NO_ALIASES)
    assert (by_name.canonical, by_name.resolved_by) == ("minimax/MiniMax-M3", "normalized")
    lower = aliases_mod.build([Alias("minimax/minimax-m3", inventory=(("omlx", "M3-4bit"),))], [])
    by_alias = identity.resolve_row(_local("omlx", "M3-4bit"), INDEX, lower)
    assert (by_alias.canonical, by_alias.resolved_by) == ("minimax/MiniMax-M3", "alias")


def test_deepseek_cloud_reaches_the_april_key_without_its_alias() -> None:
    # models.dev's ollama-cloud row for the bare tag has no canonical id, so
    # the host step does not answer and the name matches the April key.
    row = _cloud("ollama", "deepseek-v4-pro:cloud")
    assert identity.resolve_row(row, INDEX, NO_ALIASES) == RowIdentity(
        row=row,
        canonical="deepseek/deepseek-v4-pro",
        resolved_by="normalized",
        host_row=DEEPSEEK_HOST,
        notes=("host row has no canonical id; matched by name",),
    )


def test_deepseek_cloud_reaches_the_0813_key_through_its_alias() -> None:
    row = _cloud("ollama", "deepseek-v4-pro:cloud")
    resolved = identity.resolve_row(row, INDEX, aliases_mod.build([DEEPSEEK_ALIAS], []))
    assert resolved == RowIdentity(
        row=row,
        canonical="deepseek/deepseek-v4-pro-0813",
        resolved_by="alias",
        # The host row is still found: it carries the context the host serves.
        host_row=DEEPSEEK_HOST,
    )
    assert resolved.basis == "measured"


def test_an_alias_wins_over_a_host_row_that_has_a_canonical_id() -> None:
    pinned = aliases_mod.build(
        [Alias("deepseek/deepseek-v4-pro", inventory=(("ollama", "glm-5.3:cloud"),))], []
    )
    resolved = identity.resolve_row(_cloud("ollama", "glm-5.3:cloud"), INDEX, pinned)
    assert (resolved.canonical, resolved.resolved_by) == ("deepseek/deepseek-v4-pro", "alias")


def test_a_local_build_resolves_by_name_and_is_inherited() -> None:
    row = _local("ollama", "qwen3.8:27b-mlx")
    resolved = identity.resolve_row(row, INDEX, NO_ALIASES)
    assert resolved == RowIdentity(
        row=row, canonical="alibaba/qwen3.8-27b", resolved_by="normalized"
    )
    assert resolved.basis == "inherited"
    assert resolved.notes == ()
    for name in ("mlx-community/Qwen3.8-27B-4bit", "Qwen3.8-27B-nvfp4"):
        assert identity.resolve_row(_local("omlx", name), INDEX, NO_ALIASES).canonical == (
            "alibaba/qwen3.8-27b"
        )


def test_a_local_ollama_row_never_takes_the_cloud_host_row() -> None:
    # `glm-5.3` is an ollama-cloud id; a local tag of that name is another
    # thing, so it resolves by name and has no host row.
    resolved = identity.resolve_row(_local("ollama", "glm-5.3"), INDEX, NO_ALIASES)
    assert (resolved.resolved_by, resolved.host_row) == ("normalized", None)


def test_an_ambiguous_name_stays_unmapped() -> None:
    resolved = identity.resolve_row(_local("ollama", "twin:1"), INDEX, NO_ALIASES)
    assert resolved == RowIdentity(row=_local("ollama", "twin:1"))
    assert resolved.unmapped


def test_a_name_no_key_matches_stays_unmapped() -> None:
    assert identity.resolve_row(_local("ollama", "medgemma:27b"), INDEX, NO_ALIASES).unmapped
    # A size the key does not have is another model.
    assert identity.resolve_row(_local("ollama", "qwen3.8:9b"), INDEX, NO_ALIASES).unmapped


def test_a_host_row_without_a_canonical_id_falls_through_to_the_name() -> None:
    resolved = identity.resolve_row(_cloud("ollama", "gpt-oss:120b-cloud"), INDEX, NO_ALIASES)
    assert resolved.canonical == "openai/gpt-oss-120b"
    assert resolved.resolved_by == "normalized"
    assert resolved.notes == (identity.HOST_NOTE,)


def test_an_unmapped_cloud_row_keeps_its_host_row() -> None:
    resolved = identity.resolve_row(
        _cloud("openrouter", "nex-agi/nex-n2.5-pro:free"), INDEX, NO_ALIASES
    )
    assert resolved.unmapped
    assert resolved.host_row is not None
    assert resolved.host_row.model_id == "nex-agi/nex-n2.5-pro"
    assert resolved.notes == ()


def test_an_ignored_row_is_neither_resolved_nor_unmapped() -> None:
    row = _local("ollama", "nomic-embed-text:latest")
    ignoring = aliases_mod.build(
        [], [Ignore("embedding model", inventory=(row.provider_id, row.model_name))]
    )
    resolved = identity.resolve_row(row, INDEX, ignoring)
    assert resolved == RowIdentity(row=row, ignore_reason="embedding model")
    assert not resolved.unmapped


def test_an_ignore_wins_over_a_name_that_would_resolve() -> None:
    row = _local("ollama", "qwen3.8:27b-mlx")
    ignoring = aliases_mod.build(
        [], [Ignore("not for coding", inventory=(row.provider_id, row.model_name))]
    )
    assert identity.resolve_row(row, INDEX, ignoring).canonical is None
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_identity.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'identity' from 'modeltiers'`.

- [ ] **Step 3: Write the index and the row resolver**

Write `modeltiers/src/modeltiers/identity.py`:

```python
"""Identity: inventory rows and source records to canonical keys.

Three steps, the first that answers wins: an alias, the host's own row in
models.dev, then normalization (the token function matching exactly one key).
"""

from collections.abc import Mapping
from dataclasses import dataclass

from modeltiers.aliases import Aliases
from modeltiers.records import InventoryRow, ModelsDev, ModelsDevRow
from modeltiers.tokens import is_floating, tokens

ALIAS = "alias"
HOST = "host"
NORMALIZED = "normalized"
MEASURED = "measured"
INHERITED = "inherited"
HOST_NOTE = "host row has no canonical id; matched by name"

# inventory provider_id or source name -> (models.dev provider, suffixes the
# host adds to a model id that models.dev does not list)
_HOSTS: Mapping[str, tuple[str, tuple[str, ...]]] = {
    "ollama": ("ollama-cloud", (":cloud", "-cloud")),
    "openrouter": ("openrouter", (":free", ":batch")),
}


@dataclass(frozen=True)
class Index:
    """What one models.dev snapshot and the aliases say about keys. Every
    mapping keyed by a canonical key uses the key in lower case."""

    keys: Mapping[str, str]  # lower-case key -> the spelling to store
    rows: Mapping[str, tuple[ModelsDevRow, ...]]  # lower-case key -> its models.dev rows
    hosts: Mapping[tuple[str, str], ModelsDevRow]  # (models.dev provider, model id) -> row
    by_tokens: Mapping[tuple[str, ...], tuple[str, ...]]  # token sequence -> stored keys

    def spelling(self, key: str) -> str | None:
        """The stored spelling of a key written in any case, or None."""
        return self.keys.get(key.lower())

    def rows_for(self, key: str) -> tuple[ModelsDevRow, ...]:
        """The models.dev rows of a key; empty for an alias-declared key."""
        return self.rows.get(key.lower(), ())

    def normalized(self, name: str) -> str | None:
        """The one key whose token sequence equals the name's, or None when
        no key or more than one key matches."""
        matches = self.by_tokens.get(tokens(name), ())
        return matches[0] if len(matches) == 1 else None


def build_index(modelsdev: ModelsDev, aliases: Aliases) -> Index:
    """Index a models.dev snapshot. The valid keys are every
    `canonical_model_id`, in models.dev's spelling, plus any key an alias
    declares. A key ending in `-latest` is valid and is left out of the
    normalization index."""
    keys: dict[str, str] = {}
    rows: dict[str, list[ModelsDevRow]] = {}
    hosts: dict[tuple[str, str], ModelsDevRow] = {}
    host_providers = {provider for provider, _ in _HOSTS.values()}
    for row in modelsdev.rows:
        if row.provider in host_providers:
            hosts[(row.provider, row.model_id)] = row
        if row.canonical is not None:
            keys.setdefault(row.canonical.lower(), row.canonical)
            rows.setdefault(row.canonical.lower(), []).append(row)
    for alias in aliases.aliases:
        keys.setdefault(alias.canonical.lower(), alias.canonical)
    by_tokens: dict[tuple[str, ...], list[str]] = {}
    for key in keys.values():
        sequence = tokens(key)
        if sequence and not is_floating(key):
            by_tokens.setdefault(sequence, []).append(key)
    return Index(
        keys=keys,
        rows={key: tuple(found) for key, found in rows.items()},
        hosts=hosts,
        by_tokens={sequence: tuple(found) for sequence, found in by_tokens.items()},
    )


def host_row(index: Index, host: str, model_id: str) -> ModelsDevRow | None:
    """The models.dev row a host lists for one of its own model ids. `host` is
    an inventory provider_id or a source name; one models.dev does not carry
    as a host has no rows. The host's serving suffix is removed first:
    `glm-5.3:cloud` is looked up as `glm-5.3`, `z-ai/glm-5.3:free` as
    `z-ai/glm-5.3`."""
    if host not in _HOSTS:
        return None
    provider, suffixes = _HOSTS[host]
    for suffix in suffixes:
        if model_id.endswith(suffix):
            model_id = model_id.removesuffix(suffix)
            break
    return index.hosts.get((provider, model_id))


@dataclass(frozen=True)
class RowIdentity:
    """How one inventory row resolved. Exactly one of three states: resolved
    (`canonical` and `resolved_by` set), ignored (`ignore_reason` set), or
    unmapped (neither)."""

    row: InventoryRow
    canonical: str | None = None  # in the stored spelling
    resolved_by: str | None = None  # ALIAS | HOST | NORMALIZED
    ignore_reason: str | None = None
    host_row: ModelsDevRow | None = None  # a cloud row's models.dev host row, canonical or not
    notes: tuple[str, ...] = ()

    @property
    def basis(self) -> str:
        """A local build inherits its tier: every public score describes a
        hosted serving of the model."""
        return INHERITED if self.row.location == "local" else MEASURED

    @property
    def unmapped(self) -> bool:
        return self.canonical is None and self.ignore_reason is None


def _stored(index: Index, key: str) -> str:
    return index.spelling(key) or key


def resolve_row(row: InventoryRow, index: Index, aliases: Aliases) -> RowIdentity:
    """Resolve one inventory row: ignore, alias, host row, normalization."""
    pair = (row.provider_id, row.model_name)
    if pair in aliases.inventory_ignore:
        return RowIdentity(row=row, ignore_reason=aliases.inventory_ignore[pair])
    host = host_row(index, row.provider_id, row.model_name) if row.location == "cloud" else None
    if pair in aliases.inventory_alias:
        return RowIdentity(
            row=row,
            canonical=_stored(index, aliases.inventory_alias[pair]),
            resolved_by=ALIAS,
            host_row=host,
        )
    if host is not None and host.canonical is not None:
        return RowIdentity(
            row=row, canonical=_stored(index, host.canonical), resolved_by=HOST, host_row=host
        )
    key = index.normalized(row.model_name)
    if key is None:
        return RowIdentity(row=row, host_row=host)
    return RowIdentity(
        row=row,
        canonical=key,
        resolved_by=NORMALIZED,
        host_row=host,
        notes=(HOST_NOTE,) if host is not None else (),
    )
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_identity.py -q`
Expected: PASS, `30 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/identity.py modeltiers/tests/test_identity.py
git commit -m "feat(modeltiers): resolve inventory rows by alias, host row, then name"
```

---

### Task 5: Source-record resolution and records that share a key

**Files:**
- Modify: `modeltiers/src/modeltiers/identity.py` (two import lines; append two dataclasses and two functions)
- Test: `modeltiers/tests/test_identity_sources.py`

**Interfaces:**
- Consumes: `Index`, `build_index`, `host_row`, `_stored`, `ALIAS`, `HOST`, `NORMALIZED` (Task 4); `modeltiers.records.ScoreRecord` (step 1, Task 2).
- Produces, in `modeltiers.identity`:
  - `Match(canonical: str, resolved_by: str, record: ScoreRecord)`, frozen. `canonical` is in the stored spelling.
  - `SourceIdentity(source: str, records: int, best: Mapping[str, Match], unresolved: tuple[ScoreRecord, ...], ignored: int)`, frozen. `best` is keyed by the lower-case key, in the order keys were first seen. `unresolved` keeps every record no step answered, in the source's order, duplicates of one id included.
  - `resolve_record(record: ScoreRecord, index: Index, aliases: Aliases) -> Match | None`. It does not look at `[[ignore]]`.
  - `resolve_source(source: str, records: Iterable[ScoreRecord], index: Index, aliases: Aliases) -> SourceIdentity`.

- [ ] **Step 1: Write the failing test**

Write `modeltiers/tests/test_identity_sources.py`:

```python
from modeltiers import aliases as aliases_mod
from modeltiers import identity
from modeltiers.aliases import Alias, Aliases, Ignore
from modeltiers.identity import Match
from modeltiers.records import ModelsDev, ModelsDevRow, ScoreRecord


def _md(provider: str, model_id: str, canonical: str | None) -> ModelsDevRow:
    return ModelsDevRow(provider, model_id, canonical, True, True, 1000000)


MODELSDEV = ModelsDev(
    rows=(
        _md("openrouter", "z-ai/glm-5.3", "zhipuai/glm-5.3"),
        _md("openrouter", "minimax/minimax-m3", "minimax/MiniMax-M3"),
        _md("openrouter", "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"),
        _md("openrouter", "nex-agi/nex-n2.5-pro", None),
        _md("deepinfra", "deepseek-ai/DeepSeek-V4-Pro-0813", "deepseek/deepseek-v4-pro-0813"),
        _md("anthropic", "claude-opus-5-5", "anthropic/claude-opus-5-5"),
        _md("ollama-cloud", "cogito-3", "deepcogito/cogito-3"),
    )
)
NO_ALIASES = Aliases()
INDEX = identity.build_index(MODELSDEV, NO_ALIASES)


def _or(source_id: str, score: float, coding: float | None = None) -> ScoreRecord:
    return ScoreRecord("openrouter", source_id, score, coding_index=coding)


def _arena(source_id: str, score: float, ci_lower: float, effort: str | None) -> ScoreRecord:
    return ScoreRecord("arena", source_id, score, ci_lower=ci_lower, effort=effort)


def test_an_openrouter_record_resolves_through_its_host_row() -> None:
    record = _or("z-ai/glm-5.3", 44.8)
    assert identity.resolve_record(record, INDEX, NO_ALIASES) == Match(
        "zhipuai/glm-5.3", "host", record
    )


def test_an_arena_or_epoch_record_resolves_by_name() -> None:
    arena = _arena("Claude Opus 5.5", 0.1433, 0.1215, "high")
    assert identity.resolve_record(arena, INDEX, NO_ALIASES) == Match(
        "anthropic/claude-opus-5-5", "normalized", arena
    )
    epoch = ScoreRecord("epoch", "MiniMax-M3", 146.95, ci_lower=142.59)
    assert identity.resolve_record(epoch, INDEX, NO_ALIASES) == Match(
        "minimax/MiniMax-M3", "normalized", epoch
    )


def test_a_source_other_than_openrouter_has_no_host_step() -> None:
    # `cogito-3` is an ollama-cloud host id. Arena is not that host, so the
    # record can only resolve by name, and the name alone matches the key.
    record = _arena("cogito-3", 0.01, 0.0, None)
    assert identity.resolve_record(record, INDEX, NO_ALIASES) == Match(
        "deepcogito/cogito-3", "normalized", record
    )


def test_an_alias_answers_before_the_host_row() -> None:
    pinned = aliases_mod.build(
        [Alias("deepseek/deepseek-v4-pro-0813", openrouter=("deepseek/deepseek-v4-pro",))], []
    )
    record = _or("deepseek/deepseek-v4-pro", 41.0)
    assert identity.resolve_record(record, INDEX, pinned) == Match(
        "deepseek/deepseek-v4-pro-0813", "alias", record
    )
    # The alias is on the exact id; the `:free` variant still goes to its host row.
    free = _or("deepseek/deepseek-v4-pro:free", 41.0)
    assert identity.resolve_record(free, INDEX, pinned) == Match(
        "deepseek/deepseek-v4-pro", "host", free
    )


def test_an_arena_alias_covers_every_effort_row() -> None:
    pinned = aliases_mod.build(
        [Alias("deepseek/deepseek-v4-pro-0813", arena=("DeepSeek V4 Pro (High) (0813)",))], []
    )
    # The parser has already removed the effort from the record's id.
    rows = (
        _arena("DeepSeek V4 Pro (0813)", 0.0134, -0.0032, "high"),
        _arena("DeepSeek V4 Pro (0813)", 0.0101, -0.0040, "max"),
    )
    resolved = identity.resolve_source("arena", rows, INDEX, pinned)
    assert resolved.unresolved == ()
    assert list(resolved.best) == ["deepseek/deepseek-v4-pro-0813"]


def test_a_record_no_step_answers_is_unresolved() -> None:
    no_canonical = _or("nex-agi/nex-n2.5-pro:free", 30.0)  # host row without a canonical id
    no_row = _or("liquid/lfm-2.5-2.6b:free", 8.4)
    resolved = identity.resolve_source("openrouter", (no_canonical, no_row), INDEX, NO_ALIASES)
    assert resolved.unresolved == (no_canonical, no_row)
    assert (resolved.records, len(resolved.best), resolved.ignored) == (2, 0, 0)


def test_free_and_batch_collapse_to_the_base_and_the_first_stays_on_a_tie() -> None:
    base = _or("z-ai/glm-5.3", 44.8, coding=74.8)
    batch = _or("z-ai/glm-5.3:batch", 44.8, coding=74.8)
    free = _or("z-ai/glm-5.3:free", 44.8)
    resolved = identity.resolve_source("openrouter", (base, batch, free), INDEX, NO_ALIASES)
    assert resolved.records == 3
    assert resolved.best == {"zhipuai/glm-5.3": Match("zhipuai/glm-5.3", "host", base)}


def test_two_records_for_one_key_keep_the_better_score_with_its_own_interval_and_effort() -> None:
    high = _arena("Claude Opus 5.5", 0.1433, 0.1215, "high")
    maximum = _arena("Claude Opus 5.5", 0.1391, 0.1300, "max")
    for rows in ((high, maximum), (maximum, high)):
        best = identity.resolve_source("arena", rows, INDEX, NO_ALIASES).best
        kept = best["anthropic/claude-opus-5-5"].record
        # The lower score has the higher lower bound; it is not borrowed.
        assert (kept.score, kept.ci_lower, kept.effort) == (0.1433, 0.1215, "high")


def test_a_dated_and_an_undated_name_can_share_a_key() -> None:
    pinned = aliases_mod.build(
        [Alias("anthropic/claude-opus-5-5", epoch=("Claude Opus 5.5 (Sep 2026)",))], []
    )
    dated = ScoreRecord("epoch", "Claude Opus 5.5 (Sep 2026)", 167.33, ci_lower=164.06)
    undated = ScoreRecord("epoch", "Claude Opus 5.5", 165.0, ci_lower=None)
    resolved = identity.resolve_source("epoch", (undated, dated), INDEX, pinned)
    assert resolved.best == {
        "anthropic/claude-opus-5-5": Match("anthropic/claude-opus-5-5", "alias", dated)
    }


def test_best_is_keyed_in_lower_case_and_holds_the_stored_spelling() -> None:
    record = _or("minimax/minimax-m3", 20.1)
    best = identity.resolve_source("openrouter", (record,), INDEX, NO_ALIASES).best
    assert best == {"minimax/minimax-m3": Match("minimax/MiniMax-M3", "host", record)}


def test_an_ignored_record_is_counted_and_is_not_unresolved() -> None:
    ignoring = aliases_mod.build(
        [], [Ignore("floating name; no fixed model", source="epoch", id="Mistral Large")]
    )
    rows = (
        ScoreRecord("epoch", "Mistral Large", 140.2),
        ScoreRecord("epoch", "Mistral Medium 3.5", 151.0),
    )
    resolved = identity.resolve_source("epoch", rows, INDEX, ignoring)
    assert resolved.ignored == 1
    assert [r.source_id for r in resolved.unresolved] == ["Mistral Medium 3.5"]
    assert resolved.records == 2


def test_an_ignore_for_one_source_does_not_hide_the_id_in_another() -> None:
    ignoring = aliases_mod.build([], [Ignore("r", source="epoch", id="Mistral Large")])
    resolved = identity.resolve_source(
        "arena", (_arena("Mistral Large", 0.0, -0.01, None),), INDEX, ignoring
    )
    assert (resolved.ignored, len(resolved.unresolved)) == (0, 1)
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_identity_sources.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'Match' from 'modeltiers.identity'`.

- [ ] **Step 3: Add the record resolver**

In `modeltiers/src/modeltiers/identity.py`, replace:

```python
from collections.abc import Mapping
```

with:

```python
from collections.abc import Iterable, Mapping
```

In `modeltiers/src/modeltiers/identity.py`, replace:

```python
from modeltiers.records import InventoryRow, ModelsDev, ModelsDevRow
```

with:

```python
from modeltiers.records import InventoryRow, ModelsDev, ModelsDevRow, ScoreRecord
```

Append to the end of `modeltiers/src/modeltiers/identity.py`, after two blank lines:

```python
@dataclass(frozen=True)
class Match:
    """The record a source contributes to one key."""

    canonical: str  # in the stored spelling
    resolved_by: str  # ALIAS | HOST | NORMALIZED
    record: ScoreRecord


@dataclass(frozen=True)
class SourceIdentity:
    """How one source's records resolved."""

    source: str
    records: int  # every record the source gave
    best: Mapping[str, Match]  # lower-case key -> its best-scoring record
    unresolved: tuple[ScoreRecord, ...]  # no step answered; source order
    ignored: int  # records listed under [[ignore]]


def resolve_record(record: ScoreRecord, index: Index, aliases: Aliases) -> Match | None:
    """Resolve one source record: alias, host row (OpenRouter only, since
    models.dev lists no Arena or Epoch ids), normalization."""
    source_id = (record.source, record.source_id)
    if source_id in aliases.source_alias:
        return Match(_stored(index, aliases.source_alias[source_id]), ALIAS, record)
    host = host_row(index, record.source, record.source_id)
    if host is not None and host.canonical is not None:
        return Match(_stored(index, host.canonical), HOST, record)
    key = index.normalized(record.source_id)
    return None if key is None else Match(key, NORMALIZED, record)


def resolve_source(
    source: str, records: Iterable[ScoreRecord], index: Index, aliases: Aliases
) -> SourceIdentity:
    """Resolve every record of one source. When several records reach one
    key, the best-scoring one is kept whole, so its effort and its interval
    are its own; on a tie the earlier record stays."""
    best: dict[str, Match] = {}
    unresolved: list[ScoreRecord] = []
    ignored = 0
    count = 0
    for record in records:
        count += 1
        if (record.source, record.source_id) in aliases.source_ignore:
            ignored += 1
            continue
        match = resolve_record(record, index, aliases)
        if match is None:
            unresolved.append(record)
            continue
        key = match.canonical.lower()
        if key not in best or record.score > best[key].record.score:
            best[key] = match
    return SourceIdentity(
        source=source, records=count, best=best, unresolved=tuple(unresolved), ignored=ignored
    )
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_identity_sources.py tests/test_identity.py -q`
Expected: PASS, `42 passed`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/src/modeltiers/identity.py modeltiers/tests/test_identity_sources.py
git commit -m "feat(modeltiers): resolve source records and keep the best record per key"
```

---

### Task 6: `resolve_all`, over the golden fixtures

**Files:**
- Create: `modeltiers/src/modeltiers/pipeline.py`
- Modify: `modeltiers/tests/fixtures/modelsdev.json`, `modeltiers/tests/fixtures/inventory.json`, `modeltiers/tests/conftest.py`, `modeltiers/tests/test_source_modelsdev.py`, `modeltiers/tests/test_loader.py`, `modeltiers/tests/test_inventory.py`, `modeltiers/tests/test_cli.py`
- Test: `modeltiers/tests/test_pipeline.py`

**Interfaces:**
- Consumes: `modeltiers.aliases.check_anchored`, `stale_ids`, `Aliases` (Tasks 2 and 3); `modeltiers.identity.build_index`, `resolve_row`, `resolve_source`, `Index`, `RowIdentity`, `SourceIdentity`, `NORMALIZED` (Tasks 4 and 5); `modeltiers.loader.Sources` (step 1, Task 7); `modeltiers.records.Inventory` (step 1, Task 2); the `specs` and `good_answers` fixtures and `fakes.FakeFetcher`, `fakes.Sleeps` (step 1).
- Produces, in `modeltiers.pipeline`:
  - `Identity(index: Index, rows: tuple[RowIdentity, ...], openrouter: SourceIdentity, arena: SourceIdentity, epoch: SourceIdentity, warnings: tuple[str, ...])`, frozen. `rows` has one entry per inventory row in wt's order. Method `sources() -> tuple[SourceIdentity, ...]` (openrouter, arena, epoch). Properties `unmapped -> tuple[RowIdentity, ...]` and `review -> tuple[RowIdentity, ...]` (rows with `resolved_by == "normalized"`).
  - `resolve_all(inventory: Inventory, sources: Sources, aliases: Aliases) -> Identity`. Raises `ConfigError` for an unanchored alias.
  - The golden fixtures now hold 11 models.dev rows and 8 non-native inventory rows; the `specs` fixture is `build_specs(modelsdev_min=11, openrouter_min=4, arena_min=5, epoch_min=5)`.

- [ ] **Step 1: Grow the two golden fixtures**

The models.dev fixture gains a `deepinfra` provider with the three keys the inventory fixture's local rows and the deepseek row need: `google/gemma-4-31b-it`, `alibaba/qwen3.8-27b` and the April `deepseek/deepseek-v4-pro`. The rows are trimmed from the 2026-10-12 download like the others. The inventory fixture gains a cloud row whose host row has no canonical id (`minimax-m3:cloud`) and the embedding model the fixture aliases ignore.

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

Replace the whole of `modeltiers/tests/fixtures/inventory.json` with:

```json
{
 "registry": "/home/owner/.config/local-ai/registry.toml",
 "models": [
  {"id": "claude/native", "family": "claude", "provider_id": "claude", "model_name": "native", "location": "cloud", "tags": ["code", "design"], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/glm-5.3:cloud", "family": "glm-5.3", "provider_id": "ollama", "model_name": "glm-5.3:cloud", "location": "cloud", "tags": [], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/deepseek-v4-pro:cloud", "family": "deepseek-v4", "provider_id": "ollama", "model_name": "deepseek-v4-pro:cloud", "location": "cloud", "tags": [], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "openrouter/z-ai--glm-5.3", "family": "glm-5.3", "provider_id": "openrouter", "model_name": "z-ai/glm-5.3", "location": "cloud", "tags": [], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/qwen3.8:27b-mlx", "family": "qwen3.8", "provider_id": "ollama", "model_name": "qwen3.8:27b-mlx", "location": "local", "tags": [], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/medgemma:27b", "family": "medgemma", "provider_id": "ollama", "model_name": "medgemma:27b", "location": "local", "tags": [], "registered": true, "status": "missing", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/minimax-m3:cloud", "family": "minimax-m3", "provider_id": "ollama", "model_name": "minimax-m3:cloud", "location": "cloud", "tags": [], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/nomic-embed-text:latest", "family": "nomic-embed-text", "provider_id": "ollama", "model_name": "nomic-embed-text:latest", "location": "local", "tags": [], "registered": true, "status": "missing", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/gemma4:31b-mlx", "family": "", "provider_id": "ollama", "model_name": "gemma4:31b-mlx", "location": "local", "tags": [], "registered": false, "status": "new", "running": "", "size_bytes": null, "path": null, "malformed": []}
 ],
 "providers": {"mtplx": "partial", "ollama": "ok", "omlx": "ok"}
}
```

- [ ] **Step 2: Update the counts step 1's tests pin**

In `modeltiers/tests/conftest.py`, replace:

```python
    return loader.build_specs(modelsdev_min=8, openrouter_min=4, arena_min=5, epoch_min=5)
```

with:

```python
    return loader.build_specs(modelsdev_min=11, openrouter_min=4, arena_min=5, epoch_min=5)
```

In `modeltiers/tests/test_source_modelsdev.py`, replace:

```python
    parsed = modelsdev.parse(read_fixture("modelsdev.json"), min_rows=8)
    assert len(parsed.rows) == 8
```

with:

```python
    parsed = modelsdev.parse(read_fixture("modelsdev.json"), min_rows=11)
    assert len(parsed.rows) == 11
```

In `modeltiers/tests/test_loader.py`, replace:

```python
        ("modelsdev", "ok", 8),
```

with:

```python
        ("modelsdev", "ok", 11),
```

In `modeltiers/tests/test_inventory.py`, replace:

```python
        ("ollama", "medgemma:27b"),
        ("ollama", "gemma4:31b-mlx"),
```

with:

```python
        ("ollama", "medgemma:27b"),
        ("ollama", "minimax-m3:cloud"),
        ("ollama", "nomic-embed-text:latest"),
        ("ollama", "gemma4:31b-mlx"),
```

In `modeltiers/tests/test_inventory.py`, replace:

```python
def test_inventory_file_is_read_and_wt_is_not_run() -> None:
    inv = inventory.load_inventory(path=FIXTURE, run_wt=_never_run_wt)
    assert len(inv.rows) == 6
```

with:

```python
def test_inventory_file_is_read_and_wt_is_not_run() -> None:
    inv = inventory.load_inventory(path=FIXTURE, run_wt=_never_run_wt)
    assert len(inv.rows) == 8
```

In `modeltiers/tests/test_inventory.py`, replace:

```python
    inv = inventory.load_inventory(path=None, run_wt=FIXTURE.read_text)
    assert len(inv.rows) == 6
```

with:

```python
    inv = inventory.load_inventory(path=None, run_wt=FIXTURE.read_text)
    assert len(inv.rows) == 8
```

In `modeltiers/tests/test_cli.py`, replace:

```python
inventory: 6 models (1 native rows dropped), registry /home/owner/.config/local-ai/registry.toml
modelsdev: ok, 8 records, fetched 2026-10-12T15:58:00Z
```

with:

```python
inventory: 8 models (1 native rows dropped), registry /home/owner/.config/local-ai/registry.toml
modelsdev: ok, 11 records, fetched 2026-10-12T15:58:00Z
```

In `modeltiers/tests/test_cli.py`, replace:

```python
    assert capsys.readouterr().out.startswith("inventory: 6 models")
```

with:

```python
    assert capsys.readouterr().out.startswith("inventory: 8 models")
```

- [ ] **Step 3: Run the whole suite and see it still pass**

Run: `uv run --directory modeltiers pytest -q`
Expected: PASS, `278 passed`.

- [ ] **Step 4: Write the failing test**

The `sources` fixture runs step 1's loader over the golden fixtures with a fake fetcher, so the test resolves exactly what `run` will.

Write `modeltiers/tests/test_pipeline.py`:

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


def _outcomes(result: pipeline.Identity) -> list[tuple[str, str | None, str | None]]:
    return [(r.row.model_name, r.resolved_by, r.canonical) for r in result.rows]


def test_every_inventory_row_gets_one_outcome_in_wt_order(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases
) -> None:
    result = pipeline.resolve_all(inventory, sources, fixture_aliases)
    assert _outcomes(result) == [
        ("glm-5.3:cloud", "host", "zhipuai/glm-5.3"),
        ("deepseek-v4-pro:cloud", "alias", "deepseek/deepseek-v4-pro-0813"),
        ("z-ai/glm-5.3", "host", "zhipuai/glm-5.3"),
        ("qwen3.8:27b-mlx", "normalized", "alibaba/qwen3.8-27b"),
        ("medgemma:27b", None, None),
        ("minimax-m3:cloud", "normalized", "minimax/MiniMax-M3"),
        ("nomic-embed-text:latest", None, None),
        ("gemma4:31b-mlx", "normalized", "google/gemma-4-31b-it"),
    ]
    assert [r.row.model_name for r in result.unmapped] == ["medgemma:27b"]
    assert [r.row.model_name for r in result.review] == [
        "qwen3.8:27b-mlx",
        "minimax-m3:cloud",
        "gemma4:31b-mlx",
    ]
    by_name = {r.row.model_name: r for r in result.rows}
    assert by_name["nomic-embed-text:latest"].ignore_reason == "embedding model"
    assert by_name["qwen3.8:27b-mlx"].basis == "inherited"
    assert by_name["minimax-m3:cloud"].notes == ("host row has no canonical id; matched by name",)
    # The host row found on the way, canonical or not: step 3 reads its context.
    assert by_name["minimax-m3:cloud"].host_row is not None
    assert by_name["minimax-m3:cloud"].host_row.context == 512000
    assert result.warnings == ()


def test_the_three_sources_resolve_against_the_same_index(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases
) -> None:
    result = pipeline.resolve_all(inventory, sources, fixture_aliases)
    assert [s.source for s in result.sources()] == ["openrouter", "arena", "epoch"]
    assert [(s.records, len(s.best), len(s.unresolved), s.ignored) for s in result.sources()] == [
        (4, 2, 1, 0),  # glm-5.3 and its :batch share a key; lfm has no key
        (5, 4, 1, 0),  # Qwen3.8 Max has no key in the fixture
        (5, 4, 0, 1),  # GPT-5 is ignored by the fixture aliases
    ]
    assert result.arena.best["zhipuai/glm-5.3"].record.effort == "max"
    assert result.arena.best["deepseek/deepseek-v4-pro-0813"].resolved_by == "alias"
    assert result.epoch.best["minimax/minimax-m3"].canonical == "minimax/MiniMax-M3"
    assert [r.source_id for r in result.openrouter.unresolved] == ["liquid/lfm-2.5-2.6b:free"]
    assert result.index.spelling("ZHIPUAI/GLM-5.3") == "zhipuai/glm-5.3"


def test_without_the_alias_deepseek_cloud_reaches_the_april_key(
    inventory: Inventory, sources: Sources
) -> None:
    result = pipeline.resolve_all(inventory, sources, Aliases())
    by_name = {r.row.model_name: r for r in result.rows}
    assert by_name["deepseek-v4-pro:cloud"].canonical == "deepseek/deepseek-v4-pro"
    assert by_name["deepseek-v4-pro:cloud"].resolved_by == "normalized"
    assert by_name["nomic-embed-text:latest"].unmapped
    assert result.epoch.ignored == 0


def test_an_unanchored_alias_is_a_config_error(inventory: Inventory, sources: Sources) -> None:
    unanchored = aliases_mod.build(
        [Alias("google/gemini-4-argon", inventory=(("ollama", "medgemma:27b"),))], []
    )
    with pytest.raises(ConfigError, match=r"\(google/gemini-4-argon\): canonical is not a models"):
        pipeline.resolve_all(inventory, sources, unanchored)


def test_an_alias_id_absent_from_this_run_is_a_warning_not_an_error(
    inventory: Inventory, sources: Sources
) -> None:
    stale = aliases_mod.build(
        [Alias("zhipuai/glm-5.3", arena=("GLM 5.3",), epoch=("GLM-5.3", "GLM 5.3 (retired)"))], []
    )
    result = pipeline.resolve_all(inventory, sources, stale)
    assert result.warnings == (
        "alias for zhipuai/glm-5.3 names epoch id 'GLM 5.3 (retired)', "
        "which is not in this run's epoch data",
    )
    assert result.epoch.best["zhipuai/glm-5.3"].resolved_by == "alias"
```

- [ ] **Step 5: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_pipeline.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'pipeline' from 'modeltiers'`.

- [ ] **Step 6: Write `resolve_all`**

Write `modeltiers/src/modeltiers/pipeline.py`:

```python
"""The stages after loading, run in order over one inventory and one set of
sources. Step 2 of the design stops at identity."""

from dataclasses import dataclass

from modeltiers import aliases as aliases_mod
from modeltiers import identity
from modeltiers.aliases import Aliases
from modeltiers.identity import Index, RowIdentity, SourceIdentity
from modeltiers.loader import Sources
from modeltiers.records import Inventory


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
```

- [ ] **Step 7: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_pipeline.py -q`
Expected: PASS, `5 passed`.

- [ ] **Step 8: Commit**

```bash
git add modeltiers/src/modeltiers/pipeline.py modeltiers/tests/test_pipeline.py modeltiers/tests/fixtures/modelsdev.json modeltiers/tests/fixtures/inventory.json modeltiers/tests/conftest.py modeltiers/tests/test_source_modelsdev.py modeltiers/tests/test_loader.py modeltiers/tests/test_inventory.py modeltiers/tests/test_cli.py
git commit -m "feat(modeltiers): resolve the inventory and the three sources in one pass"
```

---

### Task 7: `run` prints how every row resolved

**Files:**
- Create: `modeltiers/src/modeltiers/report.py`
- Modify: `modeltiers/src/modeltiers/cli.py`, `modeltiers/tests/test_cli.py`
- Test: `modeltiers/tests/test_report.py`, `modeltiers/tests/test_cli.py`

**Interfaces:**
- Consumes: `modeltiers.pipeline.Identity`, `resolve_all` (Task 6); `modeltiers.aliases.load_aliases` (Task 2); `modeltiers.identity.RowIdentity`, `SourceIdentity`, `INHERITED`, `HOST_NOTE`, `build_index` (Tasks 4 and 5); `modeltiers.cli.Deps`, `_run`, `_warn` (step 1, Task 8).
- Produces:
  - `modeltiers.report.identity_lines(result: Identity) -> list[str]`.
  - `modeltiers.cli.Deps` gains `aliases_path: Path | None = None` as its last field. `None` is the committed file.
  - `_run` order: load and validate the aliases, load the inventory, print provider warnings, `load_all`, `resolve_all`, print alias warnings, then stdout: the `inventory:` line, the four source lines, the identity lines, `nothing written: this version stops after resolving names`.
  - The identity section, as `tests/test_cli.py`'s `CLEAN_REPORT` shows it: one `identity:` count line; one line per inventory row (`provider_id/model_name`, padded to the longest; then `alias`, `host` or `normalized` with the key and, in brackets, `inherited` and any note; or `unmapped`; or `ignored` with the reason); `unmapped: N (…)` and its rows; `review: N matched by name alone (check each)` and its rows as `name -> key`; `unresolved: …` and one count line per benchmark source.

- [ ] **Step 1: Write the failing report test**

Write `modeltiers/tests/test_report.py`:

```python
from modeltiers import identity, report
from modeltiers.aliases import Aliases
from modeltiers.identity import RowIdentity, SourceIdentity
from modeltiers.pipeline import Identity
from modeltiers.records import InventoryRow, ModelsDev


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
        "unmapped: 0 (each needs an [[alias]] or an [[ignore]] in aliases.toml)",
        "review: 0 matched by name alone (check each)",
        "unresolved: source records no step resolved (counts only; "
        "listing those at or above a standard floor needs contracts.toml)",
        "  openrouter: 0 of 0 records, 0 ignored, 0 keys resolved",
        "  arena: 0 of 0 records, 0 ignored, 0 keys resolved",
        "  epoch: 0 of 0 records, 0 ignored, 0 keys resolved",
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
```

- [ ] **Step 2: Change the CLI tests**

Six replacements in `modeltiers/tests/test_cli.py`, then four tests added at its end.

In `modeltiers/tests/test_cli.py`, replace:

```python
from modeltiers.errors import FetchError, InventoryError
from modeltiers.loader import SourceSpecs
```

with:

```python
from modeltiers.errors import FetchError, InventoryError
from modeltiers.identity import HOST_NOTE
from modeltiers.loader import SourceSpecs
```

In `modeltiers/tests/test_cli.py`, replace:

```python
INVENTORY = Path(__file__).parent / "fixtures" / "inventory.json"
Answers = dict[str, list[bytes | Exception]]
```

with:

```python
INVENTORY = Path(__file__).parent / "fixtures" / "inventory.json"
ALIASES = Path(__file__).parent / "fixtures" / "aliases.toml"
Answers = dict[str, list[bytes | Exception]]
```

In `modeltiers/tests/test_cli.py`, replace:

```python
CLEAN_REPORT = """\
inventory: 8 models (1 native rows dropped), registry /home/owner/.config/local-ai/registry.toml
modelsdev: ok, 11 records, fetched 2026-10-12T15:58:00Z
openrouter: ok, 4 records, fetched 2026-10-12T15:58:00Z
arena: ok, 5 records, fetched 2026-10-12T15:58:00Z
epoch: ok, 5 records, fetched 2026-10-12T15:58:00Z
nothing written: this version stops after parsing the sources
"""
```

with:

```python
CLEAN_REPORT = f"""\
inventory: 8 models (1 native rows dropped), registry /home/owner/.config/local-ai/registry.toml
modelsdev: ok, 11 records, fetched 2026-10-12T15:58:00Z
openrouter: ok, 4 records, fetched 2026-10-12T15:58:00Z
arena: ok, 5 records, fetched 2026-10-12T15:58:00Z
epoch: ok, 5 records, fetched 2026-10-12T15:58:00Z
identity: 8 rows: 6 resolved, 1 ignored, 1 unmapped
  ollama/glm-5.3:cloud            host        zhipuai/glm-5.3
  ollama/deepseek-v4-pro:cloud    alias       deepseek/deepseek-v4-pro-0813
  openrouter/z-ai/glm-5.3         host        zhipuai/glm-5.3
  ollama/qwen3.8:27b-mlx          normalized  alibaba/qwen3.8-27b  [inherited]
  ollama/medgemma:27b             unmapped
  ollama/minimax-m3:cloud         normalized  minimax/MiniMax-M3  [{HOST_NOTE}]
  ollama/nomic-embed-text:latest  ignored     embedding model
  ollama/gemma4:31b-mlx           normalized  google/gemma-4-31b-it  [inherited]
unmapped: 1 (each needs an [[alias]] or an [[ignore]] in aliases.toml)
  ollama/medgemma:27b
review: 3 matched by name alone (check each)
  ollama/qwen3.8:27b-mlx -> alibaba/qwen3.8-27b
  ollama/minimax-m3:cloud -> minimax/MiniMax-M3
  ollama/gemma4:31b-mlx -> google/gemma-4-31b-it
unresolved: source records no step resolved (counts only; listing those at or above a standard floor needs contracts.toml)
  openrouter: 1 of 4 records, 0 ignored, 2 keys resolved
  arena: 1 of 5 records, 0 ignored, 4 keys resolved
  epoch: 0 of 5 records, 1 ignored, 4 keys resolved
nothing written: this version stops after resolving names
"""
```

In `modeltiers/tests/test_cli.py`, replace:

```python
    run_wt: Callable[[], str] = _never_run_wt,
) -> tuple[cli.Deps, FakeFetcher]:
    fetcher = FakeFetcher(answers)
    deps = cli.Deps(fetcher=fetcher, sleep=Sleeps(), now=lambda: now, run_wt=run_wt, specs=specs)
    return deps, fetcher
```

with:

```python
    run_wt: Callable[[], str] = _never_run_wt,
    aliases_path: Path = ALIASES,
) -> tuple[cli.Deps, FakeFetcher]:
    fetcher = FakeFetcher(answers)
    deps = cli.Deps(
        fetcher=fetcher,
        sleep=Sleeps(),
        now=lambda: now,
        run_wt=run_wt,
        specs=specs,
        aliases_path=aliases_path,
    )
    return deps, fetcher
```

In `modeltiers/tests/test_cli.py`, replace:

```python
    assert deps.now().tzinfo is UTC
```

with:

```python
    assert deps.now().tzinfo is UTC
    assert deps.aliases_path is None  # the committed data/aliases.toml
```

Append to the end of `modeltiers/tests/test_cli.py`, after two blank lines:

```python
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
    assert cli.main(["run", "--dry-run", "--inventory", str(INVENTORY)], deps=deps) == 1
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
    assert cli.main(["run", "--dry-run", "--inventory", str(INVENTORY)], deps=deps) == 0
    captured = capsys.readouterr()
    assert captured.out == CLEAN_REPORT
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
    assert cli.main(["run", "--dry-run", "--inventory", str(INVENTORY)], deps=deps) == 0
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
    assert "  epoch: 1 of 5 records, 0 ignored, 4 keys resolved\n" in out
```

- [ ] **Step 3: Run the tests and see them fail**

Run: `uv run --directory modeltiers pytest tests/test_report.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'report' from 'modeltiers'`.

Run: `uv run --directory modeltiers pytest tests/test_cli.py -q`
Expected: FAIL, `15 failed, 1 passed`: fourteen with `TypeError: Deps.__init__() got an unexpected keyword argument 'aliases_path'` and `test_default_deps_are_the_real_seams` with `AttributeError: 'Deps' object has no attribute 'aliases_path'`.

- [ ] **Step 4: Write the report**

Write `modeltiers/src/modeltiers/report.py`:

```python
"""What `run` prints about identity: how every inventory row resolved, the
unmapped list, the review list and each source's unresolved count."""

from modeltiers.identity import INHERITED, RowIdentity
from modeltiers.pipeline import Identity


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
    inventory model in wt's order, then the unmapped list, the review list
    (rows matched by name alone) and one count line per benchmark source."""
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
        "in aliases.toml)"
    )
    lines.extend(f"  {_name(r)}" for r in result.unmapped)
    lines.append(f"review: {len(result.review)} matched by name alone (check each)")
    lines.extend(f"  {_name(r)} -> {r.canonical}" for r in result.review)
    lines.append(
        "unresolved: source records no step resolved (counts only; "
        "listing those at or above a standard floor needs contracts.toml)"
    )
    lines.extend(
        f"  {s.source}: {len(s.unresolved)} of {s.records} records, "
        f"{s.ignored} ignored, {len(s.best)} keys resolved"
        for s in result.sources()
    )
    return lines
```

- [ ] **Step 5: Wire it into `run`**

The whole file again, with five changes from step 1: the two import lines for `aliases`, `pipeline` and `report`; `Deps.aliases_path`; the `run` help text; and in `_run` the aliases loaded first, `resolve_all` after `load_all`, the identity lines printed, and the new last line.

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
from modeltiers import inventory as inventory_mod
from modeltiers import loader, paths, pipeline, report
from modeltiers.errors import ModelTiersError
from modeltiers.fetch import Fetcher, Sleep, http_fetcher
from modeltiers.inventory import RunWt
from modeltiers.loader import SourceSpecs

EXIT_OK = 0
EXIT_ERROR = 1
EXIT_STALE = 2
STAMP = "%Y-%m-%dT%H:%M:%SZ"


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


def _warn(message: str) -> None:
    print(f"warning: {message}", file=sys.stderr)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="modeltiers",
        description="Advisory capability tiers for the models wt knows.",
    )
    commands = parser.add_subparsers(dest="command", required=True)
    run = commands.add_parser(
        "run", help="read the inventory and the four sources and report how each model resolved"
    )
    run.add_argument("--dry-run", action="store_true", help="write no output file")
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


def _run(args: argparse.Namespace, deps: Deps) -> int:
    # Before the inventory is read and before any fetch: a bad aliases.toml
    # costs no download.
    aliases = aliases_mod.load_aliases(deps.aliases_path)
    inventory = inventory_mod.load_inventory(path=args.inventory, run_wt=deps.run_wt)
    for line in inventory_mod.provider_warnings(inventory):
        _warn(line)
    sources = loader.load_all(
        deps.specs,
        cache=paths.cache_dir(),
        fetcher=deps.fetcher,
        sleep=deps.sleep,
        now=deps.now(),
        offline=args.offline,
        warn=_warn,
    )
    identity = pipeline.resolve_all(inventory, sources, aliases)
    for line in identity.warnings:
        _warn(line)
    print(
        f"inventory: {len(inventory.rows)} models "
        f"({inventory.native_dropped} native rows dropped), registry {inventory.registry}"
    )
    for result in sources.results():
        print(
            f"{result.name}: {result.status}, {result.records} records, "
            f"fetched {result.fetched_at.strftime(STAMP)}"
        )
    for line in report.identity_lines(identity):
        print(line)
    print("nothing written: this version stops after resolving names")
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
Expected: PASS, `18 passed`.

- [ ] **Step 7: Commit**

```bash
git add modeltiers/src/modeltiers/report.py modeltiers/src/modeltiers/cli.py modeltiers/tests/test_report.py modeltiers/tests/test_cli.py
git commit -m "feat(modeltiers): run prints how every inventory row resolved"
```

---

### Task 8: Docs

**Files:**
- Modify: `modeltiers/CLAUDE.md`, `modeltiers/tests/fixtures/README.md`, `CLAUDE.md` (lines 19 and 34)

**Interfaces:**
- Consumes: the behaviour of Tasks 1 to 7.
- Produces: nothing code reads.

- [ ] **Step 1: Bring the package's `CLAUDE.md` to step 2**

Changes from step 1's file: the overview paragraph, the `run` row and the exit-code line, an Aliases row in the files table, five rows in the pipeline table (`tokens.py`, `aliases.py`, `identity.py`, `pipeline.py`, `report.py`), a new Identity section, two testing patterns, and the `specs` minimum (8 becomes 11).

Replace the whole of `modeltiers/CLAUDE.md` with:

```markdown
# CLAUDE.md

Guidance for Claude Code when working in `modeltiers/`.

## Project overview

`modeltiers` is a Python 3.13 CLI (argparse) that will write an advisory capability tier for every model wt lists, from four public sources. Design: `../docs/superpowers/specs/2026-10-12-model-tiers-design.md`. The spec's Delivery section lists four steps; this package is at step 2: it reads the inventory, downloads and parses the sources, resolves every inventory row and every source record to a canonical key, and reports on that. It assigns no tiers and writes no output file yet.

It never writes `registry.toml`, `config.yaml` or any wt state, and it never reads `registry.toml`: the inventory comes only from `wt model list --json`.

## Commands

Run from the repo root as `uv run --directory modeltiers modeltiers <command>`, or from this directory as `uv run modeltiers <command>`. `--directory` makes `modeltiers/` the working directory, so a relative `--inventory` path resolves from here.

| Command | Effect |
|---|---|
| `modeltiers run [--dry-run] [--offline] [--inventory FILE]` | Validate `aliases.toml`, read the inventory, load the four sources, resolve names. Prints one line for the inventory, one per source (status, record count, download time), then how every inventory row resolved, the unmapped list, the review list (rows matched by name alone) and each benchmark source's unresolved count. `--offline` fetches nothing and reads each source's newest good snapshot. `--inventory FILE` reads saved `wt model list --json` output and does not run wt. At this step `run` writes no output file with or without `--dry-run`, and says so on its last line. |

Exit codes: 0 every source `ok` (or `stale` because of `--offline`); 1 `aliases.toml` is invalid, the inventory could not be read, or a source failed and has no good snapshot; 2 a source failed and its last good snapshot was used. Unmapped rows and unresolved records do not change the exit code.

Development: `make install` (`uv sync`), `make check` (ruff lint, `ruff format --check`, mypy strict on `src/`), `make test` (pytest with coverage; fails under the floor in `pyproject.toml`), `make all`. One test: `uv run pytest tests/test_loader.py::test_a_clean_fetch_is_ok_and_saved`.

## Files and environment

| Thing | Default | Override |
|---|---|---|
| Output file (not written yet) | `$XDG_CONFIG_HOME/local-ai/tiers.toml`, else `~/.config/local-ai/tiers.toml` | `MODELTIERS_OUT` |
| Snapshots | `$XDG_CACHE_HOME/local-ai/modeltiers/`, else `~/.cache/local-ai/modeltiers/` | `MODELTIERS_CACHE` |
| Aliases | `src/modeltiers/data/aliases.toml` (committed, package data) | none |

## Pipeline

One module per stage, plain frozen dataclasses (`records.py`) in and out.

| Module | Job |
|---|---|
| `inventory.py` | `wt model list --json` or `--inventory FILE` to an `Inventory`. Drops rows whose `model_name` is `native`; keeps discovered rows and rows with missing weights. The join key downstream is (`provider_id`, `model_name`), never the registry `id`. `provider_warnings` names each provider whose probe status is not `ok`. |
| `fetch.py` | One function per source behind `Fetcher` (`Callable[[str], bytes]`). `http_fetcher` has a 30-second timeout; `fetch_with_retry` tries once more after 2 seconds on a `FetchError` with `retryable` set (timeout, 5xx, Arena `error` body). `fetch_arena` pages the Hugging Face rows API to `num_rows_total` and returns one wrapper document, `{"pages": [...]}`, each page body unparsed. |
| `snapshots.py` | `<cache>/<source>/<UTC stamp>.<ext>`; `mark_bad` renames to `<stamp>.bad.<ext>`; `good_snapshots` newest first; `prune` keeps 4 good and 2 bad. `prune` is not called by any command yet. |
| `sources/*.py` | Pure parsers, bytes to records. An unrecognised shape or a count under `min_rows` raises `SourceParseError`. `min_rows` defaults to the spec's floor (models.dev 1,000, OpenRouter 100, Arena 20, Epoch 100). |
| `loader.py` | `load_source`: fetch, save, parse; on a failure fall back to the newest good snapshot (`status = "stale"`, `fallback = True`); with none, `SourceUnavailable`. `load_all` loads the four and names every unavailable source in one error. |
| `tokens.py` | `tokens(name)`: the token function, the same for a name and for a canonical key. `is_floating(key)`: a key ending in `-latest`. |
| `aliases.py` | `load_aliases` / `parse_aliases`: `aliases.toml` to an `Aliases` with four lookups. Refuses, before any fetch, an unknown key, an `[[ignore]]` without a reason and a pair or source id claimed twice. `check_anchored` (needs models.dev) refuses an alias whose canonical is not a models.dev key and lists no source id. `stale_ids` gives a warning per alias source id absent from this run. |
| `identity.py` | `build_index` (keys, models.dev rows per key, host rows, token sequences), `host_row`, `resolve_row` (inventory), `resolve_record` and `resolve_source` (benchmark records). Order: alias, host row, normalization; `resolved_by` records which. |
| `pipeline.py` | `resolve_all(inventory, sources, aliases)` to one `Identity`: a `RowIdentity` per inventory row, a `SourceIdentity` per benchmark source, the warnings. |
| `report.py` | `identity_lines`: the identity section `run` prints. |
| `cli.py` | `main(argv, *, deps=None)`. `Deps` carries every seam, `aliases_path` included. |

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
- **Unresolved records** are kept in full on `SourceIdentity.unresolved` and only counted in the report. Listing those at or above a standard floor needs `contracts.toml`, which the next step adds.

## Testing patterns

- **Nothing live.** The autouse `_hermetic` fixture in `tests/conftest.py` makes `urllib.request.urlopen` and `subprocess.run` raise and points `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `MODELTIERS_OUT` and `MODELTIERS_CACHE` under `tmp_path`. `tests/test_conftest_guards.py` fails if that stops holding. CI's `bin/check-config-dirs-untouched` covers the config home only; nothing but this fixture protects the real cache.
- **Seams are parameters.** The network is a `Fetcher`, the wait is a `Sleep`, the clock is a `now`, wt is a `RunWt`. Tests pass `fakes.FakeFetcher` and `fakes.Sleeps` (`tests/fakes.py`), or a whole `cli.Deps`.
- **Small fixtures.** `tests/fixtures/` holds a few rows per source, so tests pass small minimums: the `specs` fixture is `loader.build_specs(modelsdev_min=11, openrouter_min=4, arena_min=5, epoch_min=5)`. The Epoch fixtures are CSV text; the `epoch_zip` fixture zips one in memory. The Arena fixtures are single page bodies; `arena_doc` wraps them as `fetch_arena` would.
- **Fixture-local aliases.** Tests pass `tests/fixtures/aliases.toml` as `Deps.aliases_path` or build an `Aliases` with `aliases.build`. Only `tests/test_data_files.py` reads the committed `src/modeltiers/data/aliases.toml`, and it asserts nothing about which entries the file holds.
- **Identity tests build their own models.dev.** `tests/test_identity.py` and `tests/test_identity_sources.py` use hand-built `ModelsDev` rows; `tests/test_pipeline.py` and `tests/test_cli.py` use the golden fixtures.
- `tests/fixtures/README.md` credits each source. Keep it current when a fixture is added.
```

- [ ] **Step 2: Credit the new fixture**

In `modeltiers/tests/fixtures/README.md`, replace:

```markdown
| `inventory.json` | `wt model list --json` on the owner's machine | This repository. The registry path is made up; `size_bytes` and `path` are nulled. |
```

with:

```markdown
| `inventory.json` | `wt model list --json` on the owner's machine | This repository. The registry path is made up; `size_bytes` and `path` are nulled. |
| `aliases.toml` | Hand-written for the tests | This repository. The same shape as `src/modeltiers/data/aliases.toml`; its entries are chosen for the tests and are not calibration. |
```

In `modeltiers/tests/fixtures/README.md`, replace:

```markdown
- **Rows removed.** Each file keeps between one and eight rows.
```

with:

```markdown
- **Rows removed.** Each file keeps between one and eleven rows.
```

- [ ] **Step 3: Update the root `CLAUDE.md`**

Both lines were added by step 1. Each replacement below is a part of one long line; the text before and after it stays.

Line 19, the Commands entry for `modeltiers run`:

In `CLAUDE.md`, replace:

```markdown
and print each source's status and record count. It writes no output file yet: tier assignment and `tiers.toml` are later steps of `docs/superpowers/specs/2026-10-12-model-tiers-design.md`. Exit 0 clean, 1 on an inventory error or a source with no usable data,
```

with:

```markdown
and print each source's status and record count, then how every inventory model resolved to a canonical key (alias, host row or name match), the unmapped list and the list of name matches to review. It writes no output file yet: tier assignment and `tiers.toml` are later steps of `docs/superpowers/specs/2026-10-12-model-tiers-design.md`. Exit 0 clean, 1 on an invalid `aliases.toml`, an inventory error or a source with no usable data,
```

Line 34, the Architecture bullet for `modeltiers/`:

In `CLAUDE.md`, replace:

```markdown
and falls back to the newest good one when a download or a parse fails. Design:
```

with:

```markdown
and falls back to the newest good one when a download or a parse fails. Hand-kept identity answers (which key a model name means, which rows to ignore) are committed in `modeltiers/src/modeltiers/data/aliases.toml`. Design:
```

- [ ] **Step 4: Check the links**

Run: `make check-links`
Expected: PASS, ending `ALL LINKS OK`.

- [ ] **Step 5: Commit**

```bash
git add modeltiers/CLAUDE.md modeltiers/tests/fixtures/README.md CLAUDE.md
git commit -m "docs(modeltiers): describe identity, aliases.toml and the resolution report"
```

---

### Task 9: Full verification

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
42 files already formatted
uv run mypy src/
Success: no issues found in 20 source files
```

- [ ] **Step 2: The suite, with the coverage floor**

Run: `make -C modeltiers test`
Expected: PASS. The last lines are:

```
TOTAL                                    944      0   100%
Required test coverage of 90.0% reached. Total coverage: 100.00%
============================= 289 passed in 0.36s ==============================
```

The time varies; the counts do not.

- [ ] **Step 3: The repo's lint**

Run: `make lint`
Expected: PASS, ending `ALL LINKS OK`.

- [ ] **Step 4: The tests left the real directories alone**

Run: `git status --short`
Expected: PASS with no output: nothing untracked, nothing modified. `~/.cache/local-ai/modeltiers` was not created by any step of this plan.

- [ ] **Step 5 (optional, needs the network and wt): one live run, by hand**

It makes four kinds of request and runs `wt model list --json`. The snapshots go to a scratch directory, not to `~/.cache`.

Run (live, by hand): `MODELTIERS_CACHE="$(mktemp -d)" uv run --directory modeltiers modeltiers run --dry-run`

Expected: exit 0. After the `inventory:` line and the four source lines comes `identity: N rows: R resolved, 1 ignored, U unmapped`, with one line per inventory model. `ollama/deepseek-v4-pro:cloud` reads `alias       deepseek/deepseek-v4-pro-0813` and `ollama/nomic-embed-text:latest` reads `ignored     embedding model`. The unmapped list is not empty and the review list names every row matched by name; both are step 4's calibration work and neither changes the exit code. A `warning: alias for … names … id …` line means a source no longer lists an id the committed `aliases.toml` names. The last line is `nothing written: this version stops after resolving names`. The numbers depend on the day's inventory and sources and are not pinned here.

**Done when**

- `modeltiers run --dry-run` prints how every inventory row resolved: its key and which step answered, or `unmapped`, or `ignored` with the reason (spec Delivery item 2). It also prints the unmapped list, the review list of rows with `resolved_by = "normalized"`, and per-source unresolved counts.
- Every inventory row apart from the native rows has exactly one outcome, so step 3 can put each in `[[models]]` or `[[unmapped]]` (success criterion 1, the identity half; `test_every_inventory_row_gets_one_outcome_in_wt_order`).
- The token function passes the spec's table (spec Testing, Token function; `tests/test_tokens.py`, and `test_a_latest_key_is_valid_and_left_out_of_the_normalization_index`).
- `deepseek-v4-pro:cloud` reaches the 0813 key through its alias and the April key without it; `qwen3.8:27b-mlx` reaches the 27B key and is marked inherited; an ambiguous name stays unmapped; a host row without a canonical id falls through and carries the note; `:free` and `:batch` collapse to the base; two records for one key keep the better score with its own interval and effort (spec Testing, Resolution; `tests/test_identity.py`, `tests/test_identity_sources.py`).
- A duplicate inventory pair, a duplicate source id, an unanchored canonical key and an `[[ignore]]` without a reason are each exit 1 naming the entry; a stale source id is a warning; an ignored row is neither resolved nor unmapped (spec Testing, Alias validation, as far as it applies before `[[models]]` exists).
- An invalid `aliases.toml` stops the run before wt is run and before any download (spec Error handling, first row; `test_an_invalid_aliases_file_exits_1_before_wt_and_before_any_fetch`).
- Every test uses the `aliases.toml` under `tests/fixtures/` or builds its own; one test loads the committed file (spec Testing, Fixture-local data).
- No test touches the network, the real config home or the real cache (success criterion 7), and the tool still writes nothing but snapshot files (success criterion 8).
- `make -C modeltiers check`, `make -C modeltiers test` and `make lint` pass.

**For step 3 (assignment, output and commands)**

- `pipeline.Identity` is the input. Extend `pipeline.py` with the assignment stage; do not rename `resolve_all` or the `Identity` fields.
- **Context for the gate:** a cloud row's host row is `RowIdentity.host_row` (present whether or not it has a canonical id, and whichever step answered). `Index.rows_for(key)` gives a key's models.dev rows for the tool-calling gate, the largest context and `open_weights`; it is empty for an alias-declared key.
- **Votes:** `SourceIdentity.best` maps the lower-case key to a `Match`, whose `record` carries the score, `ci_lower`, `effort`, `coding_index` and `agentic_index`. Look up with `key.lower()`; write `Match.canonical` or `RowIdentity.canonical`, which are in the stored spelling.
- **Candidates** are the keys in any source's `best` that no `RowIdentity.canonical` equals (compare in lower case). `Match.resolved_by` is per source; a candidate seen in several sources needs one value chosen for its `resolved_by` field.
- **`[[unresolved]]`:** `SourceIdentity.unresolved` holds every unresolved record, with one entry per effort row. Filter by the signal's standard floor and decide whether to list an id once.
- **Ignored rows** arrive as `RowIdentity` with `ignore_reason` set and no `canonical`; write them as `unrated` with reason `not_applicable`.
- `cli.Deps.aliases_path` is the pattern for a `contracts_path` seam; `tests/test_data_files.py` is where the committed `contracts.toml` gets its one test.
- The report's last line and step 1's `test_plain_run_does_what_dry_run_does` still stand in for the write; step 3 replaces both.
- Adding a row to a golden fixture changes the counts pinned in `tests/test_source_*.py`, `tests/test_loader.py`, `tests/test_inventory.py`, `tests/test_pipeline.py`, `tests/test_cli.py` (`CLEAN_REPORT`) and the `specs` fixture's minimums.
