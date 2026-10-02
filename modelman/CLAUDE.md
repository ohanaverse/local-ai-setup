# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`modelman` is a Python 3.13 Textual TUI and CLI for managing LLM models across providers (Ollama, oMLX, MTPLX, mlx_lm_server, OpenRouter, native agents; llama.cpp is retired but its provider code is kept — see `../docs/reference/provider-artifacts.md`) and exposing them through LiteLLM. **LiteLLM management is wt-owned (since 2026-09-21):** modelman never edits LiteLLM's `config.yaml` or restarts the proxy — it delegates to `wt litellm ...` and **requires `wt` on PATH** (`make install` from the repo root; write paths raise a clear error before changing any state when it is missing). User-facing behaviour (TUI keys, column formats, TOML schemas) is documented in `README.md`; this file covers internals and gotchas.

CLI (`src/modelman/main.py`, Typer; bare `modelman` opens the TUI via `@app.callback(invoke_without_command=True)`):

| Command | Purpose |
|---|---|
| `migrate` | One-time import of legacy `config.yaml` + `families/*.yaml` |
| `sync` | Reconcile state against providers |
| `expose <id>` / `unexpose <id>` | Apply modelman's gates, then delegate to `wt litellm expose\|unexpose` |
| `litellm status\|on\|off\|set` | Passthroughs to `wt litellm ...` (wt owns routing state) |
| `start [model_id]` / `stop <id>\|--all` | Local-model lifecycle (`local_control.py`); bare `stop` is a usage error. No-arg `start` prints a live inventory (see "Local-model lifecycle") |
| `ollama-catalog sync [--dry-run] [--html F] [--yes] [--force]` | Mirror ollama.com/pricing: update ollama cloud prices (incl. off-peak `time_prices`), add/remove registry entries, `ollama pull` missing cloud stubs and `ollama rm` unlisted ones (removal unexposes), and (re)expose every page model so its LiteLLM row carries current prices. One confirmation, or `--yes`. Exit 1 a pull/rm/save failed / 2 fetch or `ollama list` failed / 3 page-shape change (HTML saved) / 4 mass removal (> half the cloud entries) without `--force`; 2–4 change nothing. Driven by the `ollama-catalog` skill |
| `refresh-prices` | Refresh cloud models' per-token prices from OpenRouter (`pricing.py`) |
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
- Focused subsets: `uv run pytest tests/test_expose.py tests/test_queue.py -q`, `uv run pytest -k "not screen" -q` (skips the slow Textual screen tests)

The suite (~1500 tests; screen tests in `tests/screens/` dominate) runs in ~1.5 min. `tests/conftest.py` autouse fixtures stop it from restarting the live LiteLLM proxy or calling the live `ollama` daemon, so it is safe to run while agents use the proxy.

`/code-review` findings must be verified manually — it misreads indirect usage like `monkeypatch.setitem()` and in-function imports.

## Architecture

### Entry point and TUI hand-off

- `main.py::run_tui()` runs the app; the app returns `QueuedOps | None`, and `run_queued_ops()` applies it against **freshly loaded** on-disk state after `run()` returns.
- `main.py::litellm_app` — thin passthroughs to `wt litellm status|on|off|set [--url --api-key]` via `wt_bridge`. The TUI's LiteLLM status line and `l` toggle use the same bridge ("LiteLLM: unavailable" when wt is unreachable). The mount-time status/route reads use a 5s timeout (`wt_bridge.STATUS_TIMEOUT`); other bridge calls keep the 120s default.

### Textual TUI

- `app.py` — `ModelmanApp(App[QueuedOps | None])`; `on_mount` pushes one registry-backed `ModelScreen` as the only screen (FamilyScreen is gone). Also runs a daily-gated OpenRouter price-refresh worker.
- `screens/__init__.py` — `reload_preserving_cursor(table, repopulate)` restores the cursor onto the same row key after `DataTable.clear()` (falls back to row 0 if the key vanished). `reconcile_model_state(models, registry, state, local_map)` is the reconcile write-path: per provider it tries `resolve_local()` (batch; ollama uses one `ollama list`) and falls back to per-model `is_downloaded()`/`size_of()`; a misaligned/failed batch result degrades to the per-model path. No hard-coded provider ids — batch support is a provider capability. For local-artifact models (`registry.model_has_local_artifact`) it writes `ready`/`disk_path`/`size_bytes`; for cloud models `ready` is left alone. A known `disk_path` is kept unless a fresh one is observed.
- `screens/models.py` — `ModelScreen`. Holds `queued_ready`/`queued_deletes`/`queued_moves`/`queued_exposes`. Invariants worth knowing before touching it:
  - **EXPOSED** is `Y` if `locally_routed or flag_exposed` (models.py ~546). `flag_exposed` = `is_effectively_exposed()` with the queued expose and the *projected* ready (`_projected_ready`) as overrides — the same gate `litellm.py::_validate_locally` applies at apply time, so the column previews post-apply state. `locally_routed` (local models only) reads wt's live routes from `_routed_ids_cache`, filled by `wt_bridge.routed_ids()` on mount (`_prefetch_litellm_state`) and after each `s` start/stop; wt unreachable ⇒ cache `None` ⇒ falls back to the flag alone. Consequence: a routed local model shows `Y` even with a queued unexpose.
  - **Cloud rows are exempt from the ready gate** via `litellm.py::is_cloud_effective` (openrouter policy, or `location = "cloud"`), used by both the column and `_validate_locally`. **Native rows** always render `Y` (`is_effectively_exposed` short-circuits on `model.native`), but this is display-only: `_validate_locally` still rejects them ("no LiteLLM mapping") by design.
  - **RUNNING** renders `●` for a local model whose `state.running` flag is set. The on-mount reconcile worker (`_run_reconcile`) also calls `running_model_ids()`, which probes and clears stale flags (and re-reads `exposed` from disk for them), so after mount the column shows verified state.
  - **Expose depends on ready**, enforced in one place: `_enforce_expose_ready_rule`, run at every queue mutation. A queued `expose=True` is dropped (with a notification) when projected ready is False; `x` on a model queued not-ready refuses. `x` on a not-ready model cascades a `ready=True` queue, tracked in `_ready_cascade_for_expose` so cancelling the expose cancels the cascaded download. The screen never queues unexpose cascades — `apply()` re-derives them.
  - `r` is a true file-presence toggle: ready-off queues artifact removal (provider `delete()`, or `state.disk_path` for flag-only providers); pressing it again cancels the queued flip.
  - `s` start/stop — see "Local-model lifecycle".
  - Discovered-but-unregistered artifacts (`discover_unregistered_models()`, run alongside the on-mount reconcile) render as synthetic `[cyan]+[/cyan]` rows outside the STATUS glyph priority chain (they have no id to queue against); `enter`/`e` on one opens a pre-filled `ModelForm` with Provider/Model locked.
  - `_provider_list()` derives from `registry.providers` (sorted).
- `screens/forms.py` — `ConfirmModal`, `ModelForm`, `ConfirmExitDialog`, `ConfirmForceQuitDialog`, all on a shared `ModelmanModal` base (buttons left-to-right with cancel rightmost, priority-bound `escape` that cancels even inside an `Input`, `_focus_button(id)` for safe-default focus on destructive prompts). Family Select is display-only in edit mode (issue #52; id/provider/location/family immutable); in add mode its `NEW_FAMILY_VALUE` sentinel ("+ New family…") reveals `#new-family-input`, resolved by `_resolve_family()`.

### Thread-safety pattern for worker threads

A screen worker on a background thread (e.g. `ModelScreen._run_reconcile`, `thread=True`) must capture the app on the main thread:

```python
on_mount: self._app_ref = self.app  # Capture on main thread
worker: use self._app_ref, never self.app  # self.app raises NoActiveAppError when popped
```

### Adding a new TUI screen

See the `adding-a-tui-screen` skill (`.claude/skills/adding-a-tui-screen/SKILL.md`).

### Pending changes queue

- `queue.py` — `PendingChanges(registry, state, registry_path, state_path, providers, ready, deletes, exposes, moves, litellm_path, failures, cancelled)`; `ready` is `list[tuple[model_id, VariantSpec, target_ready]]`. `apply()` order: **deletes** (free disk first) → **moves** (pure registry metadata; a move for a same-apply delete is dropped) → **ready** (downloads / clears / flag flips) → **exposes** (registry saved first because wt reads it from disk; then one `wt litellm expose|unexpose --json --skip-ready-gate` call per direction, so a mixed queue can cost two proxy restarts) → `_persist()`.
- `_persist()` saves the registry and merges only this run's touched model rows onto a freshly-loaded `modelman.toml` under `locked_state` — never a whole-file write from the stale snapshot. A deleted id is popped from the fresh store (writing `state.get(mid)` would resurrect it as a default row).
- Deletes and ready-off both: check `provider.is_downloaded()` first (absent ⇒ skip provider `delete()` but still clean registry/state, emit lifecycle events, cascade-unexpose; a raising `is_downloaded()` ⇒ attempt the delete conservatively); check `registry.find_shared_artifact_owner()` (omlx dirs are keyed on repo basename, so colliding repos share one directory — the file is kept, reason surfaced). Ready-off skips ids queued for deletion. Deleting or clearing ready on an exposed model auto-appends its unexpose, so config.yaml never routes to a missing file.
- Ready-on routing: provider absent from `providers` (native/unmapped/openrouter), or `manages_own_cache` (mtplx) ⇒ flag flip only; otherwise `provider.download(variant, on_progress=...)`, sequentially. Flag-only ready-off removes the artifact in `state.disk_path` via `_remove_local_artifact`.
- Failures are captured per step and processing continues. `main.py::run_queued_ops` turns a provider that fails to instantiate into a per-item ready failure rather than silently flag-flipping it.

### Downloads (queued, applied on exit)

- Nothing runs while the TUI is open: every action (including a real download against ollama/omlx/mlx_lm_server) only fills `ModelScreen`'s queued dicts, so there is no download manager, progress screen or download quit-guard. Add/edit are the exception — `registry.toml` is written immediately.
- `Escape`/`ctrl+q` with a pending queue shows `ConfirmExitDialog`: `Apply` exits with a `QueuedOps`, `Discard` restores the pre-session snapshot and exits with `None`, `Cancel` stays.
- **Ctrl+C during `run_queued_ops()`**: the `KeyboardInterrupt` unwinds through the download step's `except BaseException` (which removes the partial artifact via `_cleanup_partial_download` so the next reconcile can't promote a truncated download), then through `apply()`'s outer `except BaseException` safety net, which calls `_persist()` — so every step that fully finished (deletes, moves, completed downloads/flag flips) is saved. The runner then calls `pending.cancel()`, prints `Cancelled: N steps completed, M remaining skipped.`, and exits non-zero. Distinct path: the `aborted()`/`self.cancelled` early return (and `DownloadCancelled`) is a plain return, not an exception — see the cancellation tests in `tests/test_queue.py` and `../docs/superpowers/specs/2026-09-13-downloads-on-exit-design.md` before changing either.
- `providers/_progress.py` — shared progress helpers plus `DownloadCancelled`, raised by the HF `ProgressTqdm` bar when `should_cancel` returns True. omlx, mlx_lm_server and llamacpp wire `should_cancel` to their `_cancel_requested` flag, flipped by `cancel_current()` (from `PendingChanges.cancel()`, usually another thread). Only a concurrent `cancel_current()` while the download context is still set reliably stops an in-flight download: `download()`'s own flip on `BaseException` races the `finally` that clears the class-level context.

### Registry and state

- `registry.py` — `Registry` (providers + models + families). `ModelEntry.cost` is a flat `Cost` (legacy `kind`/`price_per_*` schema migrated on load); optional per-model `location` overrides the provider's. `ProviderEntry.protocols` (wire protocols, default `["openai-chat"]`) is intersected by wt with each agent's protocols. Path precedence `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` (matches wt's `config.RegistryPath`). `LOCATION_LOCAL`/`LOCATION_CLOUD`/`is_local_location()` live here; legacy `None`/empty location counts as local. `DEFAULT_PROVIDER_IDS` drives default templates and sync's model-dir set.
- `state.py` — `StateStore` over `modelman.toml` (`MODELMAN_STATE`); `StateStore.set` is the single write path. `FamilyEntry`/`FamilyState.display_name` are still round-tripped (queue.py family stickiness, migrate.py) though nothing renders them.
  - The `[litellm]` table is a **legacy read-only fallback** for wt; modelman round-trips it verbatim and never alters it.
  - `ModelState.running` is a **hint** recording modelman's start/stop intent, never ground truth; an absent field reads `false`. Every reader confirms it with a live probe.
  - `locked_state()` is the process-locked read-modify-write. `local_control.py`'s flag writes and `main.py`'s `sync`/`expose`/`unexpose` persist merge-style through it — a whole-file write from a stale snapshot could revert a concurrently-written `running` flag. Keep new writers merge-style too.
  - **Exposure predicate:** effectively exposed iff `exposed = true` (legacy `litellm_exposed` read as fallback) AND (`ready` OR `location = "cloud"`); native models always exposed (they bypass LiteLLM). wt shares this predicate for cloud/native only; for LOCAL models wt ignores `exposed`/`ready` and decides from its own live probes (see `../wt/CLAUDE.md`, "Local-model resolution").
- `litellm.py` — writes nothing. Holds: read helpers for `modelman usage` (`load_litellm_config`, `_database_url_from_config`, `_reverse_model_index` — maps `litellm_params.model` → `model_name` for spend rows with a NULL `model_name`, first `model_list` entry wins); display predicates (`is_effectively_exposed`, `passes_ready_gate`, `is_cloud`, `is_cloud_effective`) backed by `wt litellm providers` via `wt_bridge.provider_cloud_flags()` (wt missing ⇒ degrade to "unmapped/non-cloud" with a one-time stderr warning); and `expose_model`/`unexpose_model`/`apply_expose_queue`/`apply_unexpose_queue`, which run `_validate_locally` against IN-MEMORY state, call wt, and flip `exposed` only after wt reports success, returning wt's warnings.
- `wt_bridge.py` — subprocess wrapper for `wt litellm ...` (`expose`, `unexpose`, `routed_ids`, `provider_cloud_flags`, `litellm_status[_text]`, `litellm_set_enabled`, `litellm_set`); parses `--json` (contract fixture `docs/contracts/litellm-cli.sample.json`); raises `WtNotFoundError`/`WtBridgeError`; **never puts argv into an error message** (it may carry `--api-key`). Sets `WT_LITELLM_CONFIG` for the child when given a config path.
- `sync.py` — reconciles state against ollama (`ollama list`) and the model-dir providers (`MODELDIR_PROVIDER_IDS` = `DEFAULT_PROVIDER_IDS` minus ollama, plus retired llamacpp). `backfill_provider_defaults` fills a missing `auth.base_url`/`protocols` from the default template, never overwriting user values.
- `pricing.py` — OpenRouter price refresh. `_is_cloud_model` excludes native providers even though they are `location="cloud"`; `_merge_api_cost` preserves manually-set cache/subscription pricing.
- `migrate.py` — one-shot legacy import, plus `migrate_wt_gateway_to_litellm` (never clobbers set `[litellm]` values). `manifest.py` (legacy `families/*.yaml`) and `config.py` (`default_config_path()`, `MODELMAN_CONFIG`) are migrate-only — add no new callers; new config goes in `registry.toml`.
- `settings.py` — TUI preferences (theme) in `~/.config/local-ai/settings.yaml` (`MODELMAN_SETTINGS`); a corrupted file raises rather than falling back.
- `_toml_io.py` — `atomic_write()` (temp + rename; `preserve_mode=True` keeps permission bits), `atomic_write_toml()`, `drop_none`, `unknown_keys`. Registry/state saves rewrite the whole file — preserve unknown keys on round-trip so hand-edited fields survive.
- `time_pricing.py` — `Window`/`TimePrice` (`Cost.time_prices`, `[[models.cost.time_prices]]`): time-windowed overrides of the flat per-token prices, first-match-wins per field, reference resolver `price_at()`. Stored only (no TUI/wt reader yet); `_merge_api_cost` and the TUI edit (`screens/models.py::_carry_over_unedited`) must carry them through.
- `ollama_catalog.py` + `ollama_catalog_cli.py` — `modelman ollama-catalog sync`. `parse_pricing` is the only code that knows ollama.com/pricing's HTML (header-keyed; raises `CatalogParseError` rather than writing partial data); `plan_sync` is pure (page name `X` → tag `X:cloud`/`X-cloud`, matched entries get `catalog_name` in model extra). The CLI saves price updates/additions under `locked_registry`, then hands pulls (`ready=True`), removals (`deletes`) and every page model's route (`exposes=True` — wt bakes prices into `config.yaml` only at expose time, so re-exposing is the price refresh) to `main.run_queued_ops` — queue.py's `is_downloaded` check skips `ollama rm` for never-pulled removals and cascades the unexpose. Tests use `tests/fixtures/ollama_pricing.html`; conftest guards its HTTP/ollama default runners.

### Provider plugin system

- `providers/base.py` — `Provider` ABC and `VariantSpec`/`LocalModel` TypedDicts (`VariantSpec` is `total=False`, with freeform `model_info`). Required: `name`, `is_downloaded`, `download`, `list_local`; `size_of` defaults to `None`. Optional: `path_of`, `resolve_local(variants)` (batch; `None` = unsupported). Capability class attributes replace hard-coded provider ids: `manages_own_cache` (`True` ⇒ `download()` always raises and `apply()` flag-flips ready-on; mtplx) and `supports_discovery` (`False` for `MLXLMServerProvider` — a target+draft pairing — and retired `LlamaCppProvider`; read by `local_control._provider_local_models()`).
- `providers/registry.py` — `ProviderRegistry.register(cls)` / `.get(name, config)` / `.get_class(name)`. Each provider module (`ollama.py`, `omlx.py`, `mtplx.py`, `mlx_lm_server.py`, `llamacpp.py`) registers at import time; `providers/__init__.py` imports them all, so import from `modelman.providers`, not a submodule.
- `mtplx.py` is discovery-only over `~/.mtplx/models` (dir `<org>--<model>` ↔ repo `org/model` via `_dir_name`/`_repo_id`); the `mtplx` CLI manages its cache. Its server lifecycle lives in `lifecycle/backends/mtplx.py`, not the provider class.
- `OllamaProvider.is_downloaded` returns False only when `ollama show` stderr contains "not found"; any other failure raises (daemon down = unknown, not absent) so a delete still attempts removal instead of orphaning the artifact.
- **Every `Provider` method that shells out must accept an optional `runner` arg** (like `is_downloaded`/`size_of`/`resolve_local`): the autouse `_never_call_real_ollama` fixture patches only module-level default runners, so a non-injectable subprocess call breaks suite hermeticity.
- `ollama_caps.py` — `auto_detect_model_info()` runs `ollama show` and maps `Capabilities` (e.g. `tools` → `supports_function_calling`); `ModelForm` calls it on add.
- Adding a provider: see the `adding-a-provider` skill (`.claude/skills/adding-a-provider/SKILL.md`).

### Provider lifecycle (`src/modelman/providers/lifecycle/`)

Isolate/stop/stop-all/restore for local providers (ported from bash, issue #79). `orchestrate.py` holds the operations; `backends/` has one backend per provider, registered in `backends/__init__.py`'s `BACKENDS`, with `SUPPORTED_PROVIDER_IDS` (`ollama`, `omlx`, `omlx-6bit`, `mlx_lm_server`, `mtplx`) excluding the retired-only `llamacpp`. mtplx and mlx_lm_server subclass `PidfileTrackedBackend` (`backends/base.py`); `pidproc.py`'s `PidfileProcess` is the spawn/stop/log-tail primitive; `probe.py`/`launchd.py`/`binaries.py` are shared primitives; `cli.py` is `modelman provider ...`. `src/modelman/local_process.py` is the neutral home for process/probe types shared with `benchmark/isolation.py` (living under either package would make the other's import backwards).

- `stop_all()`'s `keep` takes a **provider id** (like `isolate()`/`stop()`), resolved internally to its `occupancy_key` — `--keep omlx-6bit` keeps both omlx variants — and an unknown id returns `ok=False` before any teardown.
- **Testing pattern:** backend tests patch the name *as imported into the backend module* (`patch("modelman.providers.lifecycle.backends.mtplx.subprocess.run")`, `...backends.mtplx.probe.wait_for_port_closed`, `...backends.mtplx._PROC`), never the origin module — "patch where it's used" across the whole `lifecycle/` package. Distinct from the `Provider`-class `runner=` seam.

### Local-model lifecycle

`modelman start <id>` / `stop <id>` / `stop --all` and the TUI's `s` key are the sanctioned non-benchmark start/stop paths (`local_control.py`; design: `../docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md`). **Per-provider process limits:** multiple local models may run concurrently across providers (advisory only — nothing stop-alls on start). ollama is multi-tenant; omlx, mtplx and mlx_lm_server serve one model per process, so starting a different model on one of them replaces its current occupant. `omlx`/`omlx-6bit` are ONE occupancy domain (`_OMLX_PROVIDER_IDS`, shared port 8000). wt ignores the `running` flag and probes providers itself.

- **Start** (`start_local_model`): resolve the name — registry id, an existing model's native provider-side name, or a discovered artifact's native name, which is auto-registered + exposed after an interactive family prompt (`_resolve_or_register` raises `DiscoveredModelNeedsFamily` before writing anything; the CLI prompts and retries) → reject cloud models and providers outside `SUPPORTED_PROVIDER_IDS` → resolve launch args (mlx_lm_server pairing, mtplx model name, env vars) fail-fast *before any teardown* → if already flagged running, probe: serving ⇒ no-op, dead ⇒ clear and start. ollama is flag-only (lazy-loads on first request). Others find the same-provider occupant (`_same_provider_occupant`) and **clear its flag only after teardown is confirmed**: omlx calls `stop_provider()` first; mtplx/mlx_lm_server tear the occupant down inside `isolate_provider(..., solo=True)`, so the occupant's flag is cleared only after that call succeeds. `isolate_provider()` failure clears this model's flag and raises. Every start (including already-running) also exposes the model via wt; an expose failure degrades to a warning.
- **Stop**: no-op when the flag is false; ollama → `ollama stop <name>`, mtplx → `lifecycle.stop("mtplx")`, others → `stop_provider()`; then best-effort unexpose outside the lock and a locked flag clear. `exposed` is not sticky across stop/start (a stopped model must not stay routable to a dead backend). `stop_all_local_models()` is the only stop-everything path, with one batched unexpose. Failed unexposes surface on `StopResult.warnings`/`StopAllResult.warnings`, never block.
- Subprocess calls run outside `locked_state`; only the short per-model flag writes take the lock.
- **Probing** (`_probe_running`): omlx/omlx-6bit and mtplx match `/v1/models` ids lenient-prefix/strict-variant-tail; mlx_lm_server needs only a non-empty `/v1/models` (its reported spelling need not match the registry). **ollama is exempt and always reads as running** — `ollama ps` lists only currently-LOADED models, so probing would self-clear a flag that was never wrong. `running_model_ids()` is the shared flag+probe read path; its `_clear_stale_running_flag()` also best-effort unexposes.
- **Discovery** (`modelman start` no-arg inventory, auto-register, TUI `+` rows): "is this *registered* model on disk?" uses the provider's `resolve_local()`/`is_downloaded()`/`size_of()` (`_registered_presence`); "what's on disk that isn't registered?" uses `list_local()` (`_provider_local_models`, gated on `supports_discovery`). A provider that can't be asked is named in a caveat line, never read as empty. **Never join the two on `variant_id == model_name`:** omlx's `list_local()` reports the directory basename while `ModelEntry.model_name` holds the full HF repo id — `_name_matches` bridges the spellings; an exact comparison hid every registered omlx model and duplicate-registered it. Spec: `../docs/superpowers/specs/2026-09-13-modelman-start-provider-discovery-design.md`.
- **TUI `s`** always confirms (the stop dialog names the unexpose). `_lifecycle_worker_busy()` rejects a second `s` while a `model-start`/`model-stop` thread worker runs. Textual cannot cancel a thread worker, so a warmup can hold it for minutes.
- **Quit with workers running:** `ModelScreen.action_back()` (Escape and Ctrl+Q) checks `self.workers` for anything `is_running` (start/stop, reconcile, price refresh) and shows `ConfirmForceQuitDialog`. "Force quit" → `_force_quit()` restores the terminal then `os._exit(0)` — the only way out, since a normal exit hangs in `asyncio.run()`'s executor join and then `concurrent.futures.thread`'s untimed `atexit` join. Safe because `running` is a hint the next probe self-heals.
- **`ctrl+q` routing:** `ModelmanApp.request_quit()` (`app.py`) `isinstance`-checks the top screen (`ModelScreen` → `action_back()`, `ConfirmForceQuitDialog` → `dismiss(True)`, else `self.exit()`). `ctrl+q` is a priority `App` binding resolved before any modal's bindings, so **any new modal that can be topmost during quit needs its own branch in `request_quit()`**, or a second `ctrl+q` silently bypasses it.

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

`screens/forms.py::parse_model()` interprets the single `model` input per provider:

- **ollama**: tag verbatim (e.g. `ornith-1.5:35b`); slashes rejected.
- **llamacpp / omlx**: `org/repo` or `org/repo/file`; single-segment rejected.
- **native** (`provider_kinds[provider] == "native"`): verbatim; blank defaults to `native`.
- **cloud-only** (e.g. openrouter): stored whole, no repo/files split.

The model id is `provider/name` with `/` → `--` (native keeps the name). `ModelScreen._variant_to_model_entry()` builds the `ModelEntry`. `parse_cost_fields()`/`parse_subscription_fields()` parse the two independent cost sections.

## Testing patterns

- TUI tests: `pytest-asyncio` (`asyncio_mode = "auto"`) with `ModelmanApp.run_test()` and `pilot.press(...)`. Focus the target `Input` explicitly before typing — Tab cycling is unreliable in tests.
- Provider tests inject `runner` (the `mock_runner` fixture in `tests/conftest.py`).
- CLI tests use `typer.testing.CliRunner` (`patch("modelman.main.run_tui")` for the TUI). Two layers per subcommand: orchestration helpers with `tmp_path` fixtures (`tests/test_expose.py`, `tests/test_sync.py`), and wiring in `tests/commands/test_*.py` against env-redirected paths.
- Path redirects: `MODELMAN_REGISTRY`, `MODELMAN_STATE`, legacy `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` (migrate tests), `MODELMAN_LITELLM_CONFIG` (usage reader; also a legacy alias for wt's `WT_LITELLM_CONFIG`), `MODELMAN_LITELLM_DATABASE_URL`. Tests stub the wt bridge (conftest autouse guards) instead of writing config.yaml.
- **Run focused tests per change**, plus `make check`; run the full suite once when reviewing the whole change set.
- **Run `make check` before calling any task done**, not just the last: mypy flags things pytest can't (e.g. reusing a local name for two types across mutually exclusive early-return branches of one Typer command).
- **MagicMock provider stubs:** optional capabilities (`resolve_local`, `path_of`) auto-exist as truthy mocks — set `stub.resolve_local.return_value = None` (or a length-aligned list) when the code branches on them.
- **Ruff B905 is enforced:** `zip()` needs explicit `strict=`.
- **Formatting:** CI runs `ruff format --check` on `src/` and `tests/`; run `make format`. Scope any bare `ruff format` to `src/ tests/`. If it rewrites `except (A, B):` to `except A, B:` (known ruff bug), exclude the file via `[tool.ruff.format].extend-exclude`.
- In-function `from X import Y` in tests (e.g. `test_queue.py`) is intentional.
- `uv run`'s warning about an active pyenv `VIRTUAL_ENV` is expected; it uses the project `.venv`.

## Configuration for end users

See `README.md` for the `registry.toml`/`modelman.toml` schemas, TUI keys and columns, and the legacy pre-migration layout (`uv run modelman migrate` upgrades it). Config-file ownership across the monorepo: `../docs/guides/00-config-map.md`.
