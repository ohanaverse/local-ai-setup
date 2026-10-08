# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`modelman` is a Python 3.13 Textual TUI and CLI for managing LLM models across providers (Ollama, oMLX, MTPLX, mlx_lm_server, OpenRouter, native agents; llama.cpp is retired but its provider code is kept — see `../docs/reference/provider-artifacts.md`) and routing them through LiteLLM. **LiteLLM management is wt-owned (since 2026-09-21):** modelman never edits LiteLLM's `config.yaml` or restarts the proxy — it delegates to `wt litellm ...` and **requires `wt` on PATH** (`make install` from the repo root; write paths raise a clear error before changing any state when it is missing). User-facing behaviour (TUI keys, column formats, TOML schemas) is documented in `README.md`; this file covers internals and gotchas.

CLI (`src/modelman/main.py`, Typer). **The TUI is disabled**: bare `modelman` prints `TUI_DISABLED_MESSAGE` (where to go in wt) and exits 1, from `@app.callback(invoke_without_command=True)`. `run_tui()`, `app.py` and `screens/` are still in the tree and still tested, but nothing reaches them from the command line; the TUI sections below describe that code, not a command a user can run.

| Command | Purpose |
|---|---|
| `migrate` | One-time import of legacy `config.yaml` + `families/*.yaml` |
| `sync` | Reconcile state against providers, then one `wt litellm sync` |
| `litellm status\|on\|off\|set` | Passthroughs to `wt litellm ...` (wt owns routing state) |
| `start [model_id]` / `stop <id>\|--all` | Local-model lifecycle (`local_control.py`); bare `stop` is a usage error. No-arg `start` prints a live inventory (see "Local-model lifecycle") |
| `ollama-catalog sync [--dry-run] [--yes --approve-removals DIGEST] [--force]` | Mirror ollama.com/pricing into the registry and ollama (prices, added/removed cloud models), then one `wt litellm sync`. Flags, confirmation and exit codes 1–5: the `ollama-catalog` skill (`.claude/skills/ollama-catalog/SKILL.md`) |
| `refresh-prices` | Refresh OpenRouter-priced models' per-token prices from OpenRouter (`pricing.py`); stamps `price_refresh_last_run` only when it updated at least one model |
| `delete-family <name>` | Remove an empty family's lingering `[[families]]` entry (queue.py keeps families sticky); refuses if the family still has models |
| `provider isolate\|stop\|stop-all\|restore\|list` | llmbench's `provider_app`, mounted here until modelman is retired; prefer `llmbench provider ...` |
| `benchmark ...` | llmbench's `benchmark_app`, mounted here until modelman is retired; prefer `llmbench ...` |
| `usage report` | `usage/cli.py` — wt launch history × LiteLLM spend |

Sub-Typer apps mounted in `main.py`: `usage`, `litellm`, and from llmbench `benchmark` and `provider`.

## Monorepo context

- `wt/` (Go sibling) reads `modelman.toml` read-only and both reads and writes `registry.toml` (`wt model init`, `wt model add|edit|rm` and the Models tab of `wt config` — models are managed there now). The cross-language schema is pinned by the `docs/contracts/` fixtures, loaded by `tests/contracts/` here and `wt/internal/config` there — change a fixture without updating both sides and both CI jobs fail.
- **`../llmbench/` owns the benchmarks and the provider lifecycle** (carved out 2026-10; modelman has an editable path dependency on it, `[tool.uv.sources]` in `pyproject.toml`). `local_control.py` calls `llmbench.benchmark.isolation` and `llmbench.providers.lifecycle` in-process (issue #79); `local_process.ProcessResult` and `wt_bridge`'s `WtBridgeError`/`WtNotFoundError`/`WtBridgeTimeoutError` are re-exports of llmbench's classes; `tests/test_registry_path_parity.py` keeps the two registry readers on one file. Change that code in `../llmbench/` (see `../llmbench/CLAUDE.md`), then run both suites. Nothing in modelman shells out to `bin/`.

## Common development commands

`uv` for packaging; Python `==3.13.*`. Run `make help` for targets.

- `make install` (`uv sync`), `uv run modelman <subcommand>` (bare `uv run modelman` prints the TUI-disabled notice and exits 1)
- `make test`; single test: `uv run pytest tests/path/to/test.py::test_name`
- `make check` = lint + `ruff format --check` + mypy; `make all` = format + test + check
- Focused subsets: `uv run pytest tests/test_litellm.py tests/test_queue.py -q`, `uv run pytest -k "not screen" -q` (skips the slow Textual screen tests)

The suite (screen tests in `tests/screens/` dominate) runs in ~1.5 min. `tests/conftest.py` autouse fixtures stop it from restarting the live LiteLLM proxy or calling the live `ollama` daemon, so it is safe to run while agents use the proxy.

`/code-review` findings must be verified manually — it misreads indirect usage like `monkeypatch.setitem()` and in-function imports.

## Architecture

### Entry point and TUI hand-off

- `main.py::run_tui()` runs the app; the app returns `QueuedOps | None`, and `run_queued_ops()` applies it against **freshly loaded** on-disk state after `run()` returns.
- `main.py::litellm_app` — thin passthroughs to `wt litellm status|on|off|set [--url --api-key]` via `wt_bridge`. The TUI's LiteLLM status line and `l` toggle use the same bridge ("LiteLLM: unavailable" when wt is unreachable). The mount-time status read uses a 5s timeout (`wt_bridge.STATUS_TIMEOUT`) so an unreachable wt can't hang the first paint; other bridge calls keep the 120s default.

### Textual TUI

`app.py` (`ModelmanApp`) mounts one registry-backed `ModelScreen` (`screens/models.py`) as the only screen; `screens/forms.py` holds the modals on a shared `ModelmanModal` base; `screens/__init__.py` holds `reload_preserving_cursor` and the reconcile write-path `reconcile_model_state`.

- **Only a missing `registry.toml` opens as an empty registry** (`RegistryNotFoundError`); any other load failure exits 1 (#240). A caller that will save treats an unreadable registry as an error.
- **Routing is invisible here**: no column reports it (`wt litellm list` is the answer); `_sync_routes_and_warn()` runs one `wt litellm sync` after each state change.
- **Any new modal that can be topmost during quit needs its own branch in `ModelmanApp.request_quit()`** — `ctrl+q` is a priority `App` binding.
- Provider behaviour comes from provider capabilities, never hard-coded provider ids.

Read `docs/internals/tui.md` before changing `ModelScreen` (columns, `r`/`s` keys, discovered `+` rows), the forms, `parse_model()`, or a thread worker.

### Thread safety in worker threads

`self.app` is safe from a `@work(thread=True)` worker **while the widget is mounted**; it raises `NoActiveAppError` only on an unmounted widget. A worker that can outlive its screen carries paths/ids and re-loads state instead of reaching back through the widget. Full reasoning: `docs/internals/tui.md`.

### Adding a new TUI screen

Don't: the TUI is disabled and modelman is frozen until it is retired — new model-management UI goes in wt. The `adding-a-tui-screen` skill (`.claude/skills/adding-a-tui-screen/SKILL.md`) describes the screen code that is still in the tree.

### Pending changes queue

`queue.py::PendingChanges.apply()` runs **deletes → moves → ready → `_persist()`** and touches no route; every caller runs `_sync_routes_and_warn()` afterwards.

- `_persist()` merges only this run's touched rows onto a freshly-loaded `modelman.toml` under `locked_state`.
- Deletes and ready-off check `registry.find_shared_artifact_owner()`: the guard compares `Provider.removable_paths()` against every other entry's `artifact_paths()` with `providers/_paths.py::paths_overlap` (#241).
- Failures are captured per step and processing continues.

Read `docs/internals/queue-and-downloads.md` before changing `apply()` order, `_persist()`, the delete guard, ready-on routing, or cancellation.

### Downloads (queued, applied on exit)

Nothing runs while the TUI is open: actions fill `ModelScreen`'s queued dicts, and `run_queued_ops()` applies them after exit against freshly loaded state. Add/edit are the exception — `registry.toml` is written immediately. Ctrl+C during apply persists every finished step and removes the partial download. Exit dialog, `route_sync_owed` and the cancellation paths: `docs/internals/queue-and-downloads.md`.

### Registry and state

- `registry.py` — stale-snapshot guard: `load_registry` records the file's `(mtime_ns, size, inode)` per process (or that there was no file), `_write_registry` raises `RegistryError("registry.toml changed on disk; reload")` when the file no longer matches or when the `Registry` being saved was loaded before another program's change was seen (`Registry._loaded_at`; a `Registry()` built in memory counts as older than any such change), and a successful write records the new file. wt writes the registry too; this is what stops a modelman command saving its old snapshot over a wt edit. The TUI is disabled, so the writers left are the non-interactive commands, and a refused one is run again. (In the TUI code, which no command reaches now, the screens report a refused save as `Registry not saved: …`, a session cannot reload a registry, and a queued Apply whose final save is refused (`save:fail`) has already run its deletes and downloads, with neither `registry.toml` nor `modelman.toml` recording them.) `modelman migrate` and `modelman ollama-catalog sync` do not catch the refusal and end in a traceback.
- `registry.py` — `Registry` (providers + models + families); path precedence `WT_REGISTRY` > `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` (matches wt's `config.RegistryPath` and llmbench's `registry_path`).
- `state.py` — `StateStore` over `modelman.toml` (`MODELMAN_STATE`). **`ModelState.running` is a hint**, confirmed by a live probe on every read. **Writers are merge-style through `locked_state()`**: update the fields you observed on the row as it is on disk.
- **No exposure flag (#179)**: "is this model routed?" is `wt litellm list`.
- `litellm.py` writes nothing; `sync_routes()` is modelman's one route-write path and calls `wt_bridge.sync()`.
- `wt_bridge.py` — subprocess wrapper for `wt litellm ...`; **error messages leave argv out** (it may carry `--api-key`).
- `_toml_io.py` — atomic writes; registry/state saves preserve unknown keys on round-trip.
- `pricing.py::_is_openrouter_priced` and wt's `agents.HasOpenRouterPricedModel` mirror each other — change both together. A provider's `openrouter_priced` key overrides both, in both directions.
- `manifest.py` and `config.py` are migrate-only; new config goes in `registry.toml`.

Read `docs/internals/registry-and-state.md` before changing any of these, `sync.py`, `migrate.py`, `settings.py`, `time_pricing.py`, or `ollama_catalog.py`.

### Provider plugin system

`providers/base.py` — `Provider` ABC; capability class attributes (`manages_own_cache`, `supports_discovery`, `pooled`) replace hard-coded provider ids. `providers/registry.py` — `ProviderRegistry`; each provider module registers at import time, so import from `modelman.providers`.

- **Every `Provider` method that shells out must accept an optional `runner` arg** — the autouse `_never_call_real_ollama` fixture patches only module-level default runners.
- `omlx-6bit` resolves to omlx's class (`_ALIASES`): two rows that resolve to the same class are one server.
- An ollama failure other than "not found" raises (daemon down = unknown, not absent).

Read `docs/internals/providers.md` before changing a provider class, the ollama not-found rules, or `ollama_caps.py`. Adding a provider: the `adding-a-provider` skill (`.claude/skills/adding-a-provider/SKILL.md`).

### Provider lifecycle

Moved to llmbench (`../llmbench/src/llmbench/providers/lifecycle/`): see `../llmbench/CLAUDE.md`, "Provider lifecycle". modelman's tests patch it under its new name (`patch("llmbench.providers.lifecycle.stop")`), and `tests/conftest.py`'s autouse guards patch `llmbench.providers.lifecycle.*`.

### Local-model lifecycle

`modelman start <id>` / `stop <id>` / `stop --all` are the sanctioned non-benchmark start/stop paths (`local_control.py`); the TUI's `s` key is the same path, in code no command reaches now. **Per-provider process limits:** multiple local models may run concurrently across providers (advisory only). ollama is multi-tenant; omlx is a pool that can hold several loaded models, and its start and stop go through `wt` (`wt start --json`, `wt stop`; modelman needs a `wt` that has them), so starting an omlx model leaves the others loaded unless omlx evicts and `modelman stop <omlx model>` unloads just that one — asked of wt even for a model modelman has no running flag for; mtplx and mlx_lm_server serve one model per process, so starting a different model replaces the occupant. `omlx`/`omlx-6bit` are ONE server (shared port 8000). `modelman stop --all` and `modelman provider isolate|stop|restore` (benchmarks) still halt or restart the omlx service through the Python backend.

- Every start and stop closes with one `wt litellm sync`, after its state writes and outside the lock; sync warnings never fail the operation.
- An occupant's flag is cleared only after its teardown is confirmed. An omlx start sets its target's flag and clears the flags of the ids wt reports `unloaded`; a failed start changes no flag, and an omlx stop wt refuses clears one only when the pool wt reads does not hold the model ("cannot say" never clears one — only a refused connection at the registry's omlx origin does). The omlx probe likewise answers "cannot say" when nothing answered; only a refused connection reads as "nothing loaded".
- **For ollama the probe asks "still pulled?"** via `/api/tags` at the provider row's origin (#242); an unreachable daemon is unknown and stays running.
- **`modelman sync` writes only what it observed** (#231): use `dataclasses.replace` on the existing row; `running` comes from a fresh probe.
- A discovered artifact's id is `<family>/<artifact>` (`_discovered_id`), matching wt's `config.DiscoveredModelID`.
- When adding an alias or an id-keyed gate, grep for `MODELDIR_PROVIDER_IDS`, `RECONCILABLE_PROVIDERS`, `DEFAULT_PROVIDER_IDS`, `SUPPORTED_PROVIDER_IDS` and `provider_id ==`/`in` tests.

Read `docs/internals/local-model-lifecycle.md` before changing start/stop, name resolution, probing, discovery, the shared-artifact guard, provider-row creation, the TUI `s` key, or quit-with-workers.

### Benchmark subsystem

Moved to llmbench (`../llmbench/src/llmbench/benchmark/`): see `../llmbench/CLAUDE.md`, "Benchmark subsystem". `modelman benchmark ...` still runs it, mounted from llmbench in `main.py`; its tests live in `../llmbench/tests/benchmark/`.

### Usage/spend tracking

User guide: `../docs/guides/07-usage-and-spend.md`. `usage/` joins wt's `usage.jsonl` + `rotation.state` (`MODELMAN_WT_DIR`, default `~/.config/agent-wt`) with LiteLLM's `LiteLLM_SpendLogs`, read-only on both. Gotchas: `db.py::PostgresSpendStore` closes its connection explicitly (psycopg2's `with conn:` manages only the transaction); `database_url()` precedence is `MODELMAN_LITELLM_DATABASE_URL` → config.yaml `general_settings.database_url`, so the env var lets `usage report` run without a config file; `--model`/`--family` filters apply to every observed id, not just registry-known ones; malformed `usage.jsonl` lines are skipped.

## ModelForm parsing rules

`screens/forms.py::parse_model()` interprets the single `model` input per provider; the model id is `provider/name` with `/` → `--` (native and mtplx keep the name as it is). Per-provider rules: `docs/internals/tui.md`.

## Testing patterns

- TUI tests: `pytest-asyncio` (`asyncio_mode = "auto"`) with `ModelmanApp.run_test()` and `pilot.press(...)`. Focus the target `Input` explicitly before typing — Tab cycling is unreliable in tests.
- Provider tests inject `runner` (the `mock_runner` fixture in `tests/conftest.py`).
- CLI tests use `typer.testing.CliRunner` (`patch("modelman.main.run_tui")` for the TUI). Two layers per subcommand: orchestration helpers with `tmp_path` fixtures (`tests/test_litellm.py`, `tests/test_sync.py`), and wiring in `tests/commands/test_*.py` against env-redirected paths.
- Path redirects: `WT_REGISTRY` > `MODELMAN_REGISTRY` (an autouse conftest fixture clears both, so one inherited from the shell cannot outrank a test's own), `MODELMAN_STATE`, legacy `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` (migrate tests), `WT_LITELLM_CONFIG` > legacy `MODELMAN_LITELLM_CONFIG` (wt's precedence; `default_litellm_config_path()` — the usage reader, and `sync_routes` skips wt when that file is missing), `MODELMAN_LITELLM_DATABASE_URL`. Tests stub the wt bridge (conftest autouse guards) instead of writing config.yaml; the start/stop calls go through `wt_bridge._run_wt`, which the autouse stub makes fail the test unless it stubs `wt_bridge.start`/`start_plan`/`stop` (or `_run_wt`); `wt_bridge.served_ids` is stubbed to `None` (it shells out past both seams — its own tests keep a reference to the real function), and `local_control._connection_refused` is stubbed to "not refused". Outside pytest nothing stubs it: a snippet or `CliRunner` run that sets `WT_REGISTRY` or `MODELMAN_REGISTRY` must set `WT_LITELLM_CONFIG` as well — wt refuses the route sync otherwise (`sync_routes` then returns the refusal as its warning), where it used to sync the scratch registry onto the real config.yaml.
- **Run focused tests per change**, plus `make check`; run the full suite once when reviewing the whole change set.
- **Run `make check` before calling any task done**, not just the last: mypy flags things pytest can't (e.g. reusing a local name for two types across mutually exclusive early-return branches of one Typer command).
- **MagicMock provider stubs:** optional capabilities (`resolve_local`, `path_of`) auto-exist as truthy mocks — set `stub.resolve_local.return_value = None` (or a length-aligned list) when the code branches on them.
- **Ruff B905 is enforced:** `zip()` needs explicit `strict=`.
- **Formatting:** CI runs `ruff format --check` on `src/` and `tests/`; run `make format`. Scope any bare `ruff format` to `src/ tests/`. If it rewrites `except (A, B):` to `except A, B:` (known ruff bug), exclude the file via `[tool.ruff.format].extend-exclude`.
- In-function `from X import Y` in tests (e.g. `test_queue.py`) is intentional.
- `uv run`'s warning about an active pyenv `VIRTUAL_ENV` is expected; it uses the project `.venv`.

## Configuration for end users

See `README.md` for the `registry.toml`/`modelman.toml` schemas, TUI keys and columns, and the legacy pre-migration layout (`uv run modelman migrate` upgrades it). Config-file ownership across the monorepo: `../docs/guides/00-config-map.md`.
