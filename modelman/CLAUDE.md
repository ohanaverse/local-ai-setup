# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`modelman` is a Python 3.13 Textual TUI and CLI for managing LLM models across providers (Ollama, oMLX, MTPLX, mlx_lm_server, OpenRouter, native agents; llama.cpp is retired but its provider code is kept — see `../docs/reference/provider-artifacts.md`) and routing them through LiteLLM. **LiteLLM management is wt-owned (since 2026-09-21):** modelman never edits LiteLLM's `config.yaml` or restarts the proxy — it delegates to `wt litellm ...` and **requires `wt` on PATH** (`make install` from the repo root; write paths raise a clear error before changing any state when it is missing). User-facing behaviour (TUI keys, column formats, TOML schemas) is documented in `README.md`; this file covers internals and gotchas.

CLI (`src/modelman/main.py`, Typer; bare `modelman` opens the TUI via `@app.callback(invoke_without_command=True)`):

| Command | Purpose |
|---|---|
| `migrate` | One-time import of legacy `config.yaml` + `families/*.yaml` |
| `sync` | Reconcile state against providers, then one `wt litellm sync` |
| `litellm status\|on\|off\|set` | Passthroughs to `wt litellm ...` (wt owns routing state) |
| `start [model_id]` / `stop <id>\|--all` | Local-model lifecycle (`local_control.py`); bare `stop` is a usage error. No-arg `start` prints a live inventory (see "Local-model lifecycle") |
| `ollama-catalog sync [--dry-run] [--yes --approve-removals DIGEST] [--force]` | Mirror ollama.com/pricing into the registry and ollama (prices, added/removed cloud models), then one `wt litellm sync`. Flags, confirmation and exit codes 1–5: the `ollama-catalog` skill (`.claude/skills/ollama-catalog/SKILL.md`) |
| `refresh-prices` | Refresh OpenRouter-priced models' per-token prices from OpenRouter (`pricing.py`); stamps `price_refresh_last_run` only when it updated at least one model |
| `delete-family <name>` | Remove an empty family's lingering `[[families]]` entry (queue.py keeps families sticky); refuses if the family still has models |
| `provider isolate\|stop\|stop-all\|restore\|list` | Low-level per-provider lifecycle (`providers/lifecycle/cli.py`) |
| `benchmark ...` | `benchmark/cli.py`, plus `benchmark agent` / `benchmark eval` sub-apps |
| `usage report` | `usage/cli.py` — wt launch history × LiteLLM spend |

Sub-Typer apps mounted in `main.py`: `benchmark`, `usage`, `provider`, `litellm`.

## Monorepo context

- `wt/` (Go sibling) reads `registry.toml` and `modelman.toml` read-only. The cross-language schema is pinned by the `docs/contracts/` fixtures, loaded by `tests/contracts/` here and `wt/internal/config` there — change a fixture without updating both sides and both CI jobs fail.
- `modelman benchmark` and `local_control.py` call `providers/lifecycle/orchestrate.py` in-process (issue #79). Nothing in modelman shells out to `bin/`.

## Common development commands

`uv` for packaging; Python `==3.13.*`. Run `make help` for targets.

- `make install` (`uv sync`), `uv run modelman` (TUI), `uv run modelman <subcommand>`
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

See the `adding-a-tui-screen` skill (`.claude/skills/adding-a-tui-screen/SKILL.md`).

### Pending changes queue

`queue.py::PendingChanges.apply()` runs **deletes → moves → ready → `_persist()`** and touches no route; every caller runs `_sync_routes_and_warn()` afterwards.

- `_persist()` merges only this run's touched rows onto a freshly-loaded `modelman.toml` under `locked_state`.
- Deletes and ready-off check `registry.find_shared_artifact_owner()`: the guard compares `Provider.removable_paths()` against every other entry's `artifact_paths()` with `providers/_paths.py::paths_overlap` (#241).
- Failures are captured per step and processing continues.

Read `docs/internals/queue-and-downloads.md` before changing `apply()` order, `_persist()`, the delete guard, ready-on routing, or cancellation.

### Downloads (queued, applied on exit)

Nothing runs while the TUI is open: actions fill `ModelScreen`'s queued dicts, and `run_queued_ops()` applies them after exit against freshly loaded state. Add/edit are the exception — `registry.toml` is written immediately. Ctrl+C during apply persists every finished step and removes the partial download. Exit dialog, `route_sync_owed` and the cancellation paths: `docs/internals/queue-and-downloads.md`.

### Registry and state

- `registry.py` — `Registry` (providers + models + families); path precedence `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` (matches wt's `config.RegistryPath`).
- `state.py` — `StateStore` over `modelman.toml` (`MODELMAN_STATE`). **`ModelState.running` is a hint**, confirmed by a live probe on every read. **Writers are merge-style through `locked_state()`**: update the fields you observed on the row as it is on disk.
- **No exposure flag (#179)**: "is this model routed?" is `wt litellm list`.
- `litellm.py` writes nothing; `sync_routes()` is modelman's one route-write path and calls `wt_bridge.sync()`.
- `wt_bridge.py` — subprocess wrapper for `wt litellm ...`; **error messages leave argv out** (it may carry `--api-key`).
- `_toml_io.py` — atomic writes; registry/state saves preserve unknown keys on round-trip.
- `pricing.py::_is_openrouter_priced` and wt's `agents.HasOpenRouterPricedModel` mirror each other — change both together.
- `manifest.py` and `config.py` are migrate-only; new config goes in `registry.toml`.

Read `docs/internals/registry-and-state.md` before changing any of these, `sync.py`, `migrate.py`, `settings.py`, `time_pricing.py`, or `ollama_catalog.py`.

### Provider plugin system

`providers/base.py` — `Provider` ABC; capability class attributes (`manages_own_cache`, `supports_discovery`) replace hard-coded provider ids. `providers/registry.py` — `ProviderRegistry`; each provider module registers at import time, so import from `modelman.providers`.

- **Every `Provider` method that shells out must accept an optional `runner` arg** — the autouse `_never_call_real_ollama` fixture patches only module-level default runners.
- `omlx-6bit` resolves to omlx's class (`_ALIASES`): two rows that resolve to the same class are one server.
- An ollama failure other than "not found" raises (daemon down = unknown, not absent).

Read `docs/internals/providers.md` before changing a provider class, the ollama not-found rules, or `ollama_caps.py`. Adding a provider: the `adding-a-provider` skill (`.claude/skills/adding-a-provider/SKILL.md`).

### Provider lifecycle (`src/modelman/providers/lifecycle/`)

Isolate/stop/stop-all/restore for local providers. `orchestrate.py` holds the operations; `backends/` has one backend per provider (`BACKENDS`), with `SUPPORTED_PROVIDER_IDS` excluding retired `llamacpp`; `cli.py` is `modelman provider ...`.

- **Backend tests patch the name as imported into the backend module** (`patch("modelman.providers.lifecycle.backends.mtplx.subprocess.run")`).
- `stop_all()`'s `keep` takes a provider id, resolved to its `occupancy_key`.

Module map and primitives: `docs/internals/providers.md`.

### Local-model lifecycle

`modelman start <id>` / `stop <id>` / `stop --all` and the TUI's `s` key are the sanctioned non-benchmark start/stop paths (`local_control.py`). **Per-provider process limits:** multiple local models may run concurrently across providers (advisory only). ollama is multi-tenant; omlx is a pool that can hold several loaded models, and its start and stop go through `wt` (`wt start --json`, `wt stop`; modelman needs a `wt` that has them), so starting an omlx model leaves the others loaded unless omlx evicts and `modelman stop <omlx model>` unloads just that one; mtplx and mlx_lm_server serve one model per process, so starting a different model replaces the occupant. `omlx`/`omlx-6bit` are ONE server (shared port 8000). `modelman stop --all` and `modelman provider isolate|stop|restore` (benchmarks) still halt or restart the omlx service through the Python backend.

- Every start and stop closes with one `wt litellm sync`, after its state writes and outside the lock; sync warnings never fail the operation.
- An occupant's flag is cleared only after its teardown is confirmed. An omlx start sets its target's flag and clears the flags of the ids wt reports `unloaded`; a failed start changes no flag, and an omlx stop that wt cannot confirm ("cannot say") never clears one — only a refused connection at the registry's omlx origin does.
- **For ollama the probe asks "still pulled?"** via `/api/tags` at the provider row's origin (#242); an unreachable daemon is unknown and stays running.
- **`modelman sync` writes only what it observed** (#231): use `dataclasses.replace` on the existing row; `running` comes from a fresh probe.
- A discovered artifact's id is `<family>/<artifact>` (`_discovered_id`), matching wt's `config.DiscoveredModelID`.
- When adding an alias or an id-keyed gate, grep for `MODELDIR_PROVIDER_IDS`, `RECONCILABLE_PROVIDERS`, `DEFAULT_PROVIDER_IDS`, `SUPPORTED_PROVIDER_IDS` and `provider_id ==`/`in` tests.

Read `docs/internals/local-model-lifecycle.md` before changing start/stop, name resolution, probing, discovery, the shared-artifact guard, provider-row creation, the TUI `s` key, or quit-with-workers.

### Benchmark subsystem

User guides: `../docs/guides/09-agent-benchmarks.md` (agent) and `../docs/guides/11-capability-eval-benchmark.md` (eval). Module map:

- `benchmark/` — `cli.py` (`benchmark_app`: `list-workloads`, `run`, `show-results`), `isolation.py`, `runner.py`, `results.py`, `workloads/`; shared helpers `runmeta.py` (`git_sha()`), `errors.py` (`RunSavedButRestoreFailed`), `judge_core.py` (strict-JSON rubric scoring, retry-on-malformed, multi-sample median), and `_routes.py` — the **single source** for OpenRouter key resolution (env, then the LiteLLM LaunchAgent plist) and LiteLLM apiKey/baseUrl from `~/.pi/agent/models.json`; fix a credential-format change there, once.
- `benchmark/agent/` — `suite.py`, `task.py`, `workspace.py`, `pidriver.py`, `gates.py` (nine-gate taxonomy), `judge.py`, `report.py`, `runner.py`, `cli.py`.
  - `gates.toml`'s `tests_dir` must name a real subdirectory, never `"."`/`""` — `task.py::load_task` rejects them because gates 6/7 compare `Path.parts` tuples and an empty tuple is a prefix of every path.
- `benchmark/eval/` — `category.py`, `suite.py` (deliberately parallel to `agent/suite.py`, not shared), `judged_runner.py`, `evalplus_runner.py` (reports EvalPlus's "+"/hardened pass@1, not base), `runner.py` (incl. `rejudge_run` from persisted `response.txt`), `report.py`, `cli.py`.
- Tests mirror modules: `tests/benchmark/` (incl. `test_routes.py`, `test_judge_core.py`), `tests/benchmark/agent/` (plus `fixtures/`), `tests/benchmark/eval/` (plus `test_rejudge.py`).

### Usage/spend tracking

User guide: `../docs/guides/07-usage-and-spend.md`. `usage/` joins wt's `usage.jsonl` + `rotation.state` (`MODELMAN_WT_DIR`, default `~/.config/agent-wt`) with LiteLLM's `LiteLLM_SpendLogs`, read-only on both. Gotchas: `db.py::PostgresSpendStore` closes its connection explicitly (psycopg2's `with conn:` manages only the transaction); `database_url()` precedence is `MODELMAN_LITELLM_DATABASE_URL` → config.yaml `general_settings.database_url`, so the env var lets `usage report` run without a config file; `--model`/`--family` filters apply to every observed id, not just registry-known ones; malformed `usage.jsonl` lines are skipped.

## ModelForm parsing rules

`screens/forms.py::parse_model()` interprets the single `model` input per provider; the model id is `provider/name` with `/` → `--` (native and mtplx keep the name as it is). Per-provider rules: `docs/internals/tui.md`.

## Testing patterns

- TUI tests: `pytest-asyncio` (`asyncio_mode = "auto"`) with `ModelmanApp.run_test()` and `pilot.press(...)`. Focus the target `Input` explicitly before typing — Tab cycling is unreliable in tests.
- Provider tests inject `runner` (the `mock_runner` fixture in `tests/conftest.py`).
- CLI tests use `typer.testing.CliRunner` (`patch("modelman.main.run_tui")` for the TUI). Two layers per subcommand: orchestration helpers with `tmp_path` fixtures (`tests/test_litellm.py`, `tests/test_sync.py`), and wiring in `tests/commands/test_*.py` against env-redirected paths.
- Path redirects: `MODELMAN_REGISTRY`, `MODELMAN_STATE`, legacy `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` (migrate tests), `WT_LITELLM_CONFIG` > legacy `MODELMAN_LITELLM_CONFIG` (wt's precedence; `default_litellm_config_path()` — the usage reader, and `sync_routes` skips wt when that file is missing), `MODELMAN_LITELLM_DATABASE_URL`. Tests stub the wt bridge (conftest autouse guards) instead of writing config.yaml; the start/stop calls go through `wt_bridge._run_wt`, which the autouse stub makes fail the test unless it stubs `wt_bridge.start`/`start_plan`/`stop` (or `_run_wt`), and `local_control._connection_refused` is stubbed to "not refused". Outside pytest nothing stubs it: a snippet or `CliRunner` run that sets `MODELMAN_REGISTRY` must set `WT_LITELLM_CONFIG` as well — wt refuses the route sync otherwise (`sync_routes` then returns the refusal as its warning), where it used to sync the scratch registry onto the real config.yaml.
- **Run focused tests per change**, plus `make check`; run the full suite once when reviewing the whole change set.
- **Run `make check` before calling any task done**, not just the last: mypy flags things pytest can't (e.g. reusing a local name for two types across mutually exclusive early-return branches of one Typer command).
- **MagicMock provider stubs:** optional capabilities (`resolve_local`, `path_of`) auto-exist as truthy mocks — set `stub.resolve_local.return_value = None` (or a length-aligned list) when the code branches on them.
- **Ruff B905 is enforced:** `zip()` needs explicit `strict=`.
- **Formatting:** CI runs `ruff format --check` on `src/` and `tests/`; run `make format`. Scope any bare `ruff format` to `src/ tests/`. If it rewrites `except (A, B):` to `except A, B:` (known ruff bug), exclude the file via `[tool.ruff.format].extend-exclude`.
- In-function `from X import Y` in tests (e.g. `test_queue.py`) is intentional.
- `uv run`'s warning about an active pyenv `VIRTUAL_ENV` is expected; it uses the project `.venv`.

## Configuration for end users

See `README.md` for the `registry.toml`/`modelman.toml` schemas, TUI keys and columns, and the legacy pre-migration layout (`uv run modelman migrate` upgrades it). Config-file ownership across the monorepo: `../docs/guides/00-config-map.md`.
