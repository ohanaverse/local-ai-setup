# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`modelman` is a small Python 3.13 Textual TUI and CLI for managing local LLM models across multiple providers (Ollama, llama.cpp, oMLX) and exposing them through LiteLLM. The TUI lets you browse models, queue changes (download/delete/expose), and apply them on exit. CLI subcommands: `download` (TUI at a family), `migrate` (one-time import of legacy config), `sync` (reconcile state against providers), `expose`/`unexpose` (LiteLLM model_list).

## Monorepo context

- `wt/` (Go sibling) reads `registry.toml` and `modelman.toml` (exposure flags) read-only. The cross-language schema is pinned by the `docs/contracts/` fixtures, loaded by `tests/contracts/` here and `wt/internal/config` there — change a fixture without updating both sides and both CI jobs fail.
- `modelman benchmark` shells out to `bin/llm-isolate-provider` / `bin/llm-restore-providers` **by PATH** (`src/modelman/benchmark/isolation.py`) — the monorepo root's `bin/` must be on PATH for `modelman benchmark` to isolate providers.

## Common development commands

The project uses `uv` for packaging and dependency management. Python 3.13 is required (`requires-python = "==3.13.*"`).

- **Install dependencies:** `make install` (runs `uv sync`)
- **Run the CLI during development:** `uv run modelman` (TUI) or `uv run modelman download <family>`
- **Other subcommands:** `uv run modelman migrate`, `uv run modelman sync`, `uv run modelman expose <model_id>`, `uv run modelman unexpose <model_id>`
- **Run all tests:** `make test`
- **Run a single test:** `uv run pytest tests/path/to/test.py::test_name`
- **Lint / format / typecheck:** `make lint`, `make format`, `make typecheck` (or `make check` to run lint+typecheck together)
- **Run everything:** `make all` (format + test + check)
- **Clean caches:** `make clean`
- **Build a wheel:** `uv build`

The Makefile wraps the standard dev commands. Run `make help` to list targets.

## Architecture

### Entry point

- `src/modelman/main.py` defines the Typer `app`. A single `@app.callback(invoke_without_command=True)` opens the TUI when no subcommand is given; subcommands: `download <family>` (pushes `ModelScreen`), `migrate` (one-shot legacy import), `sync` (reconcile state against providers), `expose <model_id>` / `unexpose <model_id>` (LiteLLM model_list). Two sub-Typer apps are also mounted: `app.add_typer(benchmark_app, name="benchmark")` and `app.add_typer(usage_app, name="usage")`.

### Textual TUI

- `src/modelman/app.py` — `ModelmanApp(App[None])`. `on_mount` pushes `FamilyScreen`.
- `src/modelman/screens/__init__.py` — `reload_preserving_cursor(table, repopulate)` helper: snapshots the row key under the cursor before `DataTable.clear()` (which resets to row 0) and restores the cursor onto that key after repopulate. Both `FamilyScreen.reload()` and `ModelScreen._load_models()` route through it.
- `src/modelman/screens/families.py` — `FamilyScreen`: DataTable of families (family · display · variants · downloaded · size). Actions: `action_add_family` (`a`), `action_edit_family` (`e`, renames display_name only — the slug is read-only to avoid orphaning cross-references), `action_delete_family` (`d`, blocked if any downloaded models), `action_open_family` (Enter / `DataTable.RowSelected`), `action_reconcile` (`r`), `action_quit` (`q`). While `_reconciling` is true the table is disabled, a "Refreshing sizes…" indicator is shown, and every action above (plus `RowSelected`) no-ops so the user can't click a row that's about to mutate; `q` (quit) stays live.
- `src/modelman/screens/models.py` — `ModelScreen`: single DataTable (family · provider · model · loc · status · exposed · cost · tier · size) plus a details panel Static below the table showing the row's on-disk path (`path: —` when unknown). LOC is an icon (↗ cloud / ▤ local) and EXPOSED renders `Y`/`–`; there is no PATH column. STATUS renders queued ops as glyphs with priority `✗ delete > ↓ download > → move > ✓ ready > ○`. Holds `queued_ready` / `queued_deletes` / `queued_moves` / `queued_exposes` dicts. Actions: `a` add model (family selectable), `e` edit (id/provider fixed; picking a different family queues a move), `d` queue delete (works on any model — the old not-ready gate is gone; apply skips the on-disk removal if `provider.is_downloaded()` reports False), `x` toggle ready, `l` toggle LiteLLM expose, `r` reconcile, `escape` back/apply. `_provider_list()` returns `sorted(...)` so the Add dialog's provider dropdown is alphabetical.
- Both `FamilyScreen` and `ModelScreen` run a background worker (`_run_reconcile`, `thread=True`) on mount, on `on_screen_resume`, and via the `r` binding: it asks each provider whether a variant is actually on disk and overlays fresh size/downloaded values into an in-memory cache, independent of `modelman.toml` and the `sync` CLI command. This is why the SIZE/DOWNLOADED columns can differ from `state.py`'s stored values until a reconcile runs. `FamilyScreen` wraps the worker in try/finally + `app.call_from_thread(self._reconcile_done)` so the lock always clears, even on worker exception.
- `src/modelman/screens/status.py` — `StatusScreen`: live progress log of the apply-queue run. Opened from ModelScreen when the user confirms applying pending changes.
- `src/modelman/screens/forms.py` — modal screens: `AddFamilyModal`, `EditFamilyModal` (rename a family's display name only; the slug itself is read-only to avoid orphaning cross-references), `ConfirmModal` (y/n with keybindings), `ModelForm` (add/edit with provider Select, family Select, model input, location Select, a cost section — kind Select (free / per_token / subscription) plus conditional price fields — and an ollama-only usage-tier Select; `parse_cost_fields()` parses the cost fields alongside `parse_model()`; see "ModelForm parsing rules" below), `ConfirmExitDialog` (shows pending set, applies on confirm), `CancelApplyDialog` (Escape during an in-progress apply on `StatusScreen`; offers cancel-or-wait). All inherit from a shared `ModelmanModal` base that enforces the dialog conventions: buttons composed left-to-right (cancel/default rightmost, primary left of it), priority-bound `escape` action that cancels even from inside an `Input`, and a `_focus_button(id)` helper for the safe-default focus on destructive prompts.

### Pending changes queue

- `src/modelman/queue.py` — `PendingChanges(registry, state, family, registry_path, state_path, providers, downloads, deletes, moves, exposes, litellm_path, failures, cancelled)`. `apply()` runs deletes first (so downloads free up disk), then moves (pure registry metadata: `ModelEntry.family = new_family`; a move for a model deleted in the same apply is dropped), then downloads (each calls `provider.download(variant)` → `state.set(id, replace(state.get(id), ready=True, disk_path=local_path))`), then exposes (each writes a LiteLLM `model_list` entry), then a single `save_registry()` + `save_state()`. The delete step checks `provider.is_downloaded(variant)` before artifact removal: if the artifact is absent, the provider's `delete()` is skipped but the lifecycle events, registry/state cleanup, and cascade-unexpose still run; if `is_downloaded()` raises, the artifact delete is attempted conservatively and real failures surface. Failures are captured per-step, processing continues.

### Registry and state

- `src/modelman/registry.py` — `Registry` dataclass: providers + models + families. `ModelEntry` now carries `cost` (`Cost` dataclass: `free`/`per_token`/`subscription`), optional `usage_tier`, and an optional per-model `location` that overrides the provider's location for the LOC icon. Loaded from/saved to `registry.toml` (path precedence `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config`, matching wt's `config.RegistryPath`). See `README.md` for the exact TOML schema.
- `src/modelman/state.py` — `StateStore`: which models are downloaded, exposed, etc. (`get`/`set`/`forget_family`). Loaded from `modelman.toml` (`MODELMAN_STATE`); display-name resolution lives in `registry.family_display_name`/`known_families`.
- `src/modelman/migrate.py` — one-shot import of legacy `~/.config/local-ai/config.yaml` + `families/*.yaml` (and optionally `wt` config) into the registry/state pair. Run once via `uv run modelman migrate`.
- `src/modelman/sync.py` — reconciles `state` against each provider's actual filesystem (`ollama list`, HF cache scan, omlx model dir). Writes back to state.
- `src/modelman/litellm.py` — `expose_model`/`unexpose_model` add/remove entries in the LiteLLM config's `model_list` (one load/save per CLI call; `unexpose_model` is a no-op for ids missing from the registry). `PendingChanges.apply()` batches its queued exposes through `apply_expose_queue` instead — one config load/save for the whole queue. Every writer runs `ensure_litellm_settings()` before save (value-enforces `litellm_settings.drop_params: true`; adds `additional_drop_params: ["reasoning_effort"]` to every `ollama_chat/*` row missing it — the BerriAI/litellm#37452 codex workaround) and saves/restarts only when the parsed document or an exposed flag actually changed. Provider prefix/api_key/cloud rules live in `PROVIDER_POLICIES` (`is_cloud()` is the TUI's gate). After a config write that actually changed the model list, `restart_litellm_proxy()` runs the `MODELMAN_LITELLM_RESTART_CMD` command (30s timeout) to reconcile the running proxy; it returns warning strings (command unset or failed) rather than printing to stderr, so the CLI surfaces them and the TUI routes them through the apply event channel (`expose:warning|…`). `_set_exposed_flag` returns whether the flag changed, so a no-op unexpose of an already-removed model does not bounce the proxy.
- `src/modelman/_toml_io.py` — minimal `tomllib`/`tomli_w` helpers (read/write dict-shaped TOML without external config libs).

### Provider plugin system

- `src/modelman/providers/base.py` defines the `Provider` abstract base class and the `VariantSpec` / `LocalModel` TypedDicts. `VariantSpec` is `total=False` with a freeform `model_info: dict[str, Any] | None` for LiteLLM-style capability keys. `Provider` requires `name`, `is_downloaded`, `download`, `list_local`, and `size_of(variant) -> int | None` (default returns `None`).
- `src/modelman/providers/registry.py` — `ProviderRegistry.register(cls)` / `.get(name, config)`.
- Each provider module (`ollama.py`, `llamacpp.py`, `omlx.py`) calls `ProviderRegistry.register(ItsProvider)` at import time.
- `src/modelman/providers/__init__.py` imports every provider module solely to trigger registration. Code that needs providers should import from `modelman.providers` rather than a single submodule.

### Ollama capability detection

- `src/modelman/ollama_caps.py` — `parse_ollama_show(stdout)` translates `Capabilities` section entries into `model_info` (e.g. `tools` → `supports_function_calling: True`). `auto_detect_model_info(name, runner)` runs `ollama show <name>` and returns the parsed dict (or `{}` on failure). `ModelForm` calls this on add for ollama variants.

### Benchmark subsystem

- `src/modelman/benchmark/cli.py` — `benchmark_app` (mounted as `modelman benchmark`): `list-workloads`, `run` (executes a workload against a model, optionally isolated), `show-results` (reads persisted run output).
- `src/modelman/benchmark/isolation.py` — process/resource isolation for a benchmark run so results aren't skewed by concurrent load.
- `src/modelman/benchmark/runner.py` — drives a workload against a target model and times/scores it.
- `src/modelman/benchmark/results.py` — persists and loads benchmark run results.
- `src/modelman/benchmark/workloads/` — workload definitions `run` selects between.
- Tests: `tests/benchmark/` (cli, errors, isolation, results, runner, state_pointer, workload_registry, workloads — one file per module above).

### Usage/spend tracking

`modelman usage report` (mounted as `modelman usage`) joins `wt`'s local launch history with LiteLLM's Postgres spend logs into a Markdown report — read-only on both sources. See `docs/ROADMAP.md` Phase 5 for the design rationale.

- `src/modelman/usage/cli.py` — `usage_app`; `report` command reads `wt`'s `usage.jsonl` + `rotation.state` (`MODELMAN_WT_DIR`, default `~/.config/agent-wt`), the registry, and LiteLLM's Postgres spend table, then prints `format_report()`'s output.
- `src/modelman/usage/wt_state.py` — `read_usage_counts()` parses `usage.jsonl` into 1d/7d/30d launch counts per model id (malformed lines are skipped, not fatal); `read_last_launched()` reads `rotation.state`.
- `src/modelman/usage/db.py` — `SpendStore` protocol; `PostgresSpendStore` queries LiteLLM's `LiteLLM_SpendLogs` table via `psycopg2` (connection is explicitly closed — psycopg2's `with conn:` only manages the transaction, not the connection lifetime); `InMemorySpendStore` is the test fake. `_reverse_model_index()` maps `litellm_params.model` → `model_name` for spend rows with a NULL `model_name` (first `model_list` entry wins on a duplicate `litellm_params.model`). `database_url()` precedence: `MODELMAN_LITELLM_DATABASE_URL` env var → `general_settings.database_url` in config.yaml; the env var lets `usage report` run without a config file.
- `src/modelman/usage/reconcile.py` — `reconcile()` joins wt counts + spend by registry model id into `matched` / `wt_only` / `litellm_only`; `--model`/`--family` filters apply to every observed id, not just registry-known ones.
- `src/modelman/usage/report.py` — `format_report()` renders the `ReconcileResult` as Markdown.
- `src/modelman/usage/errors.py` — `UsageError` (subclasses `RuntimeError`), caught by `report_cmd` in cli.py for a clean CLI error instead of a traceback.
- Tests: `tests/usage/` (cli, db, errors, reconcile, report, wt_state).

### Config and manifests

- `src/modelman/registry.py` loads/saves `registry.toml` (providers + model definitions) into a `Registry` dataclass. Path overridable with `MODELMAN_REGISTRY`.
- `src/modelman/state.py` loads/saves `modelman.toml` (per-model state: downloaded, exposed, paths) into a `StateStore`. Path overridable with `MODELMAN_STATE`.
- `src/modelman/migrate.py` is the one-time path: imports legacy `~/.config/local-ai/config.yaml` + `families/*.yaml` (and optionally `wt` config) into the registry/state pair. The TUI/CLI expect the new layout after migration.
- `src/modelman/manifest.py` — legacy `families/*.yaml` read/write, used **only** by the migrate path (its `save_manifest` is the migrate test fixture-writer). No TUI code touches it; do not add new callers (see its module docstring).
- `src/modelman/config.py` — one helper, `default_config_path()` (`MODELMAN_CONFIG` override), pointing at the legacy `~/.config/local-ai/config.yaml`; `migrate.py` parses the YAML itself. Read-only legacy path — add new config to `registry.toml` instead.
- `src/modelman/settings.py` — persists TUI user preferences (currently just theme) to `~/.config/local-ai/settings.yaml` (`MODELMAN_SETTINGS` override); missing file = defaults, corrupted file raises rather than silently falling back.

### Adding a new provider

1. Create `src/modelman/providers/<name>.py` with a class extending `Provider`.
2. Call `ProviderRegistry.register(TheProvider)` at the bottom of the module.
3. Add a `[[providers]]` entry to `registry.toml` with `id = "<name>"`.
4. Reference it from models via `provider_id = "<name>"`.
5. (Optional) Override `size_of` so the size column is populated for downloaded variants.
6. Add a `ProviderPolicy` entry to `PROVIDER_POLICIES` in `src/modelman/litellm.py` (prefix, api_key, cloud flag). This table is the single source of truth for LiteLLM exposure — both the config writer and the TUI's expose gate read it, and an unmapped provider cannot be exposed.

No changes to `main.py` are required unless a new CLI subcommand is also added.

## ModelForm parsing rules

`src/modelman/screens/forms.py::parse_model()` decides how the single `model` input is interpreted per provider:

- **ollama**: tag verbatim (e.g. `ornith-1.5:35b`). Slashes are rejected.
- **llamacpp / omlx**: HuggingFace-style `org/repo` or `org/repo/file`. Single-segment input is rejected.
- **native providers** (`provider_kinds[provider] == "native"`): model name is used verbatim; blank defaults to `native`.
- **cloud-only providers** (e.g. openrouter): model string is stored whole, no repo/files split.

`ModelForm` derives the model id as `provider/name` with `/` replaced by `--` (except native providers, which keep the name as-is). On save, `ModelScreen._variant_to_model_entry()` turns the resulting `VariantSpec` into a `ModelEntry`.

## Worktree-aware file paths

When operating inside a linked worktree (`.worktrees/<branch>/`), verify
absolute paths point to the worktree and not the main checkout before
reading or editing source files. The main checkout's paths are easy to
reach by accident and can lead to analyzing or mutating the wrong branch.

## Testing patterns

- TUI tests use `pytest-asyncio` (`asyncio_mode = "auto"` in pyproject) and `ModelmanApp.run_test()` with a `pilot`. Drive interactions with `await pilot.press("a")` etc. and assert on `app.screen` / `query_one(DataTable)`. Modal text-input flows explicitly focus the target `Input` before pressing characters (Tab cycling is unreliable in tests).
- Provider unit tests inject a `runner` callable (commonly the `mock_runner` fixture in `tests/conftest.py`) so subprocess behavior can be mocked without shelling out.
- CLI entry-point tests use `typer.testing.CliRunner` and `unittest.mock.patch("modelman.main.run_tui")` to assert the TUI is invoked with the right argument. Every subcommand has two layers: the orchestration helpers are tested directly with `tmp_path`-based registry/state fixtures (e.g. `tests/test_expose.py`, `tests/test_sync.py`), and the command wiring (load → run → save → report) is covered by `tests/commands/test_*.py` driving the CLI runner against env-var-redirected paths.
- Tests redirect config/registry/state paths via `MODELMAN_REGISTRY`, `MODELMAN_STATE` (new) and the legacy `MODELMAN_CONFIG` / `MODELMAN_FAMILY_DIR` (still honored by `migrate` tests). LiteLLM config paths redirect via `MODELMAN_LITELLM_CONFIG`; the LiteLLM DSN via `MODELMAN_LITELLM_DATABASE_URL`.
- **Run focused tests per change, not the full suite.** When working on a change, run only the test files that exercise the code you touched (plus `make check` for lint/typecheck) — the full suite is slow. Run the entire suite once at the end, when reviewing the whole set of changes (e.g. the final task of a multi-task plan runs `make all`).
- **Focused test timeout:** `tests/test_expose.py` + `tests/test_queue.py` together take roughly 2.5 minutes; use a longer timeout or run them in the background and poll `TaskOutput`.
- **Pyenv `VIRTUAL_ENV` warning:** `uv run` ignores an active pyenv `VIRTUAL_ENV` and uses the project's `.venv`; the emitted warning is expected and can be disregarded.

## Important implementation notes

- The `OllamaProvider` methods accept an optional `runner` argument so tests can substitute a mock. The default runner just calls `subprocess.run`.
- `StateStore.set` is the single state-write path (downloads do `state.set(id, replace(state.get(id), ready=True, disk_path=local_path))`; the ISO timestamp + local path shape was the legacy `FamilyManifest.mark_downloaded`, now gone).
- `Registry`/`StateStore` save helpers rewrite the whole TOML file (`tomli_w`) — preserve unknown keys on round-trip so user-edited fields survive.
- `llamacpp` checks the Hugging Face cache (`HF_HOME/hub`) for the requested files; `omlx` downloads into `model_dir/<repo-basename>`.
- `size_of` is implemented per provider: Ollama parses the `ollama list` SIZE column (two-token `<number> <UNIT>`), llamacpp stats the primary file in the HF cache snapshot dir, omlx sums files in the model directory.
- `OllamaProvider.is_downloaded` returns `False` only when `ollama show` stderr contains "not found"; any other non-zero exit raises (transient daemon-down = unknown, not absent) so the delete step attempts removal instead of orphaning the on-disk artifact.
- `PendingChanges.apply()` is the single integration point between the TUI queue and the providers. It deliberately reorders to deletes-before-downloads so a queued delete frees disk before a queued download fills it, and runs exposes last so any queued download is visible to the LiteLLM writer. The delete step now also checks `provider.is_downloaded()` first: when the artifact is already gone, the provider's `delete()` is skipped but registry/state cleanup and lifecycle events still run — so the TUI's `d` action can queue a delete for any model, ready or not.
- Deleting an exposed model auto-queues its unexpose inside `apply()` (keyed on the state's `litellm_exposed` flag), so config.yaml never keeps a route to a deleted file.
- `FamilyScreen._reconciling` (bool) gates every action while the background size refresh runs. The flag is set when `_start_reconcile_worker` is called (mount, resume, `r` binding), and cleared on the main thread via `app.call_from_thread(self._reconcile_done)` inside a `try/finally` so a worker exception can never leave the UI locked. The table is `disabled` and a `#refresh-indicator` Static is shown for the duration. `on_screen_resume` also fires on initial mount, so two workers start on mount; `exclusive=True` cancels the first but its `finally` still runs — `_reconcile_generation` (a monotonic counter) ensures only the latest worker clears the flag.
- `reload_preserving_cursor(table, repopulate)` is the single DataTable-clear helper: it snapshots the row key under the cursor before `repopulate()` (which calls `clear()` and resets the cursor to row 0) and restores the cursor onto that key after. If the key vanished, falls back to row 0; empty-table cases are no-ops. Both list screens route through it.
- `save_litellm_config` preserves the existing config.yaml's permission bits on rewrite (mkstemp's 0600 would otherwise silently tighten them); malformed or non-dict `model_list` content is preserved or refused, never crashed on. Config writes use ruamel round-trip (`_rt_yaml()`: `typ="rt"`, `preserve_quotes=True`, `width=4096`) so hand-written comments and untouched sections survive byte-identically; changed-detection is `copy.deepcopy` before the mutation + ensure, `!=` after — save iff changed, proxy restart iff changed or a flag flipped.
- The TUI's `action_back` (`escape` from ModelScreen) shows the exit confirmation only if the queue is non-empty; otherwise it pops immediately.

## Configuration for end users

See `README.md` for the exact TOML schemas for `registry.toml` and `modelman.toml`, the LiteLLM `model_list` format, and the provider config field reference. Legacy `config.yaml`/`families/*.yaml` schemas are still documented for users on the pre-migration layout — run `uv run modelman migrate` to upgrade.

## Foreign agent configs

`~/.codex/` and `~/.gemini/` exist on this machine. To import user-level instructions, MCP servers, slash commands, subagents, or skills from those configs, run `/import` in Claude Code (or `claude import` from a terminal) to scan what's available, then `/import --yes=<digest>` to apply the items you want.