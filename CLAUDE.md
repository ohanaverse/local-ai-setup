# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`modelman` is a small Python 3.13 Textual TUI and CLI for managing local LLM models across multiple providers (Ollama, llama.cpp, oMLX) and exposing them through LiteLLM. The TUI lets you browse models, queue changes (download/delete/expose), and apply them on exit. CLI subcommands: `download` (TUI at a family), `migrate` (one-time import of legacy config), `sync` (reconcile state against providers), `expose`/`unexpose` (LiteLLM model_list).

## Common development commands

The project uses `uv` for packaging and dependency management. Python 3.13 is required (`requires-python = "==3.13.*"`).

- **Install dependencies:** `make install` (runs `uv sync`)
- **Run the CLI during development:** `uv run modelman` (TUI) or `uv run modelman download <family>`
- **Other subcommands:** `uv run modelman migrate`, `uv run modelman sync`, `uv run modelman expose <model_id>`, `uv run modelman unexpose <model_id>`
- **Run all tests:** `make test`
- **Run a single test:** `uv run pytest tests/path/to/test.py::test_name`
- **Lint / format / typecheck:** `make lint`, `make format`, `make typecheck` (or `make check` to run lint+typecheck together)
- **Build a wheel:** `uv build`

The Makefile wraps the standard dev commands. Run `make help` to list targets.

## Architecture

### Entry point

- `src/modelman/main.py` defines the Typer `app`. A single `@app.callback(invoke_without_command=True)` opens the TUI when no subcommand is given; subcommands: `download <family>` (pushes `ModelScreen`), `migrate` (one-shot legacy import), `sync` (reconcile state against providers), `expose <model_id>` / `unexpose <model_id>` (LiteLLM model_list). Two sub-Typer apps are also mounted: `app.add_typer(benchmark_app, name="benchmark")` and `app.add_typer(usage_app, name="usage")`.

### Textual TUI

- `src/modelman/app.py` — `ModelmanApp(App[None])`. `on_mount` pushes `FamilyScreen`.
- `src/modelman/screens/families.py` — `FamilyScreen`: DataTable of families (family · display · variants · downloaded · size). Actions: `action_add_family` (`a`), `action_edit_family` (`e`, renames display_name only — the slug is read-only to avoid orphaning cross-references), `action_delete_family` (`d`, blocked if any downloaded models), `action_open_family` (Enter / `DataTable.RowSelected`), `action_reconcile` (`r`), `action_quit` (`q`).
- `src/modelman/screens/models.py` — `ModelScreen`: single DataTable (provider · model · location · status · exposed · size · path). STATUS renders queued ops as glyphs with priority `✗ delete > ↓ download > → move > ✓ ready > ○`. Holds `queued_ready` / `queued_deletes` / `queued_moves` / `queued_exposes` dicts. Actions: `a` add model (family selectable), `e` edit (id/provider fixed; picking a different family queues a move), `d` queue delete, `x` toggle ready, `l` toggle LiteLLM expose, `r` reconcile, `escape` back/apply.
- Both `FamilyScreen` and `ModelScreen` run a background worker (`_run_reconcile`, `thread=True`) on mount, on `on_screen_resume`, and via the `r` binding: it asks each provider whether a variant is actually on disk and overlays fresh size/downloaded values into an in-memory cache, independent of `modelman.toml` and the `sync` CLI command. This is why the SIZE/DOWNLOADED columns can differ from `state.py`'s stored values until a reconcile runs.
- `src/modelman/screens/status.py` — `StatusScreen`: live progress log of the apply-queue run. Opened from ModelScreen when the user confirms applying pending changes.
- `src/modelman/screens/forms.py` — modal screens: `AddFamilyModal`, `EditFamilyModal` (rename a family's display name only; the slug itself is read-only to avoid orphaning cross-references), `ConfirmModal` (y/n with keybindings), `ModelForm` (add/edit with provider Select, family Select, model input, and location Select; see "ModelForm parsing rules" below), `ConfirmExitDialog` (shows pending set, applies on confirm), `CancelApplyDialog` (Escape during an in-progress apply on `StatusScreen`; offers cancel-or-wait).

### Pending changes queue

- `src/modelman/queue.py` — `PendingChanges(registry, state, family, registry_path, state_path, providers, downloads, deletes, moves, exposes, litellm_path, failures, cancelled)`. `apply()` runs deletes first (so downloads free up disk), then moves (pure registry metadata: `ModelEntry.family = new_family`; a move for a model deleted in the same apply is dropped), then downloads (each calls `provider.download(variant)` → `state.mark_downloaded(...)`), then exposes (each writes a LiteLLM `model_list` entry), then a single `save_registry()` + `save_state()`. Failures are captured per-step, processing continues.

### Registry and state

- `src/modelman/registry.py` — `Registry` dataclass: providers + models. Loaded from/saved to `registry.toml` (path precedence `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config`, matching agent-worktree's `config.RegistryPath`).
- `src/modelman/state.py` — `StateStore`: which models are downloaded, exposed, etc. Loaded from `modelman.toml` (`MODELMAN_STATE`).
- `src/modelman/migrate.py` — one-shot import of legacy `~/.config/local-ai/config.yaml` + `families/*.yaml` (and optionally `agent-worktree` config) into the registry/state pair. Run once via `uv run modelman migrate`.
- `src/modelman/sync.py` — reconciles `state` against each provider's actual filesystem (`ollama list`, HF cache scan, omlx model dir). Writes back to state.
- `src/modelman/litellm.py` — `expose_model`/`unexpose_model` add/remove entries in the LiteLLM config's `model_list` (one load/save per CLI call; `unexpose_model` is a no-op for ids missing from the registry). `PendingChanges.apply()` batches its queued exposes through `apply_expose_queue` instead — one config load/save for the whole queue. Provider prefix/api_key/cloud rules live in `PROVIDER_POLICIES` (`is_cloud()` is the TUI's gate).
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
- `src/modelman/migrate.py` is the one-time path: imports legacy `~/.config/local-ai/config.yaml` + `families/*.yaml` (and optionally `agent-worktree` config) into the registry/state pair. The TUI/CLI expect the new layout after migration.
- `src/modelman/manifest.py` still exists for the legacy path — `FamilyManifest.variant_by_id(vid)` looks up a variant; `downloaded` is a dict keyed by variant id; `mark_downloaded` stores an ISO timestamp and local path; `save_manifest` rewrites the whole YAML file with `sort_keys=False`. Not used by the new TUI screens.
- `src/modelman/config.py` — loads legacy `~/.config/local-ai/config.yaml`; used **only** by the `migrate` path (see its module docstring) — do not add new callers, add to `registry.toml` instead.
- `src/modelman/settings.py` — persists TUI user preferences (currently just theme) to `~/.config/local-ai/settings.yaml` (`MODELMAN_SETTINGS` override); missing file = defaults, corrupted file raises rather than silently falling back.

### Adding a new provider

1. Create `src/modelman/providers/<name>.py` with a class extending `Provider`.
2. Call `ProviderRegistry.register(TheProvider)` at the bottom of the module.
3. Add the provider to `~/.config/local-ai/config.yaml` under `providers:`.
4. Use `provider: <name>` in family manifests.
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

## Testing patterns

- TUI tests use `pytest-asyncio` (`asyncio_mode = "auto"` in pyproject) and `ModelmanApp.run_test()` with a `pilot`. Drive interactions with `await pilot.press("a")` etc. and assert on `app.screen` / `query_one(DataTable)`. Modal text-input flows explicitly focus the target `Input` before pressing characters (Tab cycling is unreliable in tests).
- Provider unit tests inject a `runner` callable (commonly the `mock_runner` fixture in `tests/conftest.py`) so subprocess behavior can be mocked without shelling out.
- CLI entry-point tests use `typer.testing.CliRunner` and `unittest.mock.patch("modelman.main.run_tui")` to assert the TUI is invoked with the right argument. Every subcommand has two layers: the orchestration helpers are tested directly with `tmp_path`-based registry/state fixtures (e.g. `tests/test_expose.py`, `tests/test_sync.py`), and the command wiring (load → run → save → report) is covered by `tests/commands/test_*.py` driving the CLI runner against env-var-redirected paths.
- Tests redirect config/registry/state paths via `MODELMAN_REGISTRY`, `MODELMAN_STATE` (new) and the legacy `MODELMAN_CONFIG` / `MODELMAN_FAMILY_DIR` (still honored by `migrate` tests). LiteLLM config paths redirect via `MODELMAN_LITELLM_CONFIG`; the LiteLLM DSN via `MODELMAN_LITELLM_DATABASE_URL`.

## Important implementation notes

- The `OllamaProvider` methods accept an optional `runner` argument so tests can substitute a mock. The default runner just calls `subprocess.run`.
- `StateStore.mark_downloaded` stores an ISO timestamp and local path under the registry model id (replaces the legacy `FamilyManifest.mark_downloaded`).
- `Registry`/`StateStore` save helpers rewrite the whole TOML file (`tomli_w`) — preserve unknown keys on round-trip so user-edited fields survive.
- `llamacpp` checks the Hugging Face cache (`HF_HOME/hub`) for the requested files; `omlx` downloads into `model_dir/<repo-basename>`.
- `size_of` is implemented per provider: Ollama parses the `ollama list` SIZE column (two-token `<number> <UNIT>`), llamacpp stats the primary file in the HF cache snapshot dir, omlx sums files in the model directory.
- `PendingChanges.apply()` is the single integration point between the TUI queue and the providers. It deliberately reorders to deletes-before-downloads so a queued delete frees disk before a queued download fills it, and runs exposes last so any queued download is visible to the LiteLLM writer.
- Deleting an exposed model auto-queues its unexpose inside `apply()` (keyed on the state's `litellm_exposed` flag), so config.yaml never keeps a route to a deleted file.
- `save_litellm_config` preserves the existing config.yaml's permission bits on rewrite (mkstemp's 0600 would otherwise silently tighten them); malformed or non-dict `model_list` content is preserved or refused, never crashed on.
- The TUI's `action_back` (`escape` from ModelScreen) shows the exit confirmation only if the queue is non-empty; otherwise it pops immediately.

## Configuration for end users

See `README.md` for the exact TOML schemas for `registry.toml` and `modelman.toml`, the LiteLLM `model_list` format, and the provider config field reference. Legacy `config.yaml`/`families/*.yaml` schemas are still documented for users on the pre-migration layout — run `uv run modelman migrate` to upgrade.

## Foreign agent configs

`~/.codex/` and `~/.gemini/` exist on this machine. To import user-level instructions, MCP servers, slash commands, subagents, or skills from those configs, run `/import` in Claude Code (or `claude import` from a terminal) to scan what's available, then `/import --yes=<digest>` to apply the items you want.