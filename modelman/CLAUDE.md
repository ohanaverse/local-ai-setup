# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`modelman` is a small Python 3.13 Textual TUI and CLI for managing local LLM models across multiple providers (Ollama, oMLX — llama.cpp is retired but its provider code is kept; see `docs/reference/provider-artifacts.md`) and exposing them through LiteLLM. The TUI lets you browse models, queue changes (ready/delete/move/expose), and apply them on exit. CLI subcommands: `migrate` (one-time import of legacy config), `sync` (reconcile state against providers), `expose`/`unexpose` (LiteLLM model_list), `litellm status|on|off|set` (the LiteLLM routing on/off switch wt reads), `start [model_id]`/`stop` (issue #65 — a local model wt's picker may offer alongside any other currently-running local model; delegates to `src/modelman/providers/lifecycle/orchestrate.py` in-process (issue #79 — no more bash isolation helper); `start` with no `model_id` prints a live three-way inventory — registered+on-disk, registered-but-missing, and discovered-but-unregistered — asked live of the providers rather than trusting only cached state (the registered buckets via each provider's own `resolve_local()`/`is_downloaded()`/`size_of()`, the discovered bucket via `list_local()`; a provider that can't be asked is named in a caveat line instead of being silently read as "nothing there"); `start <name>` accepts a registry id, an existing model's native provider-side name, or the native name of a discovered artifact, auto-registering+exposing the last case after an interactive family prompt — see `../docs/superpowers/specs/2026-09-13-modelman-start-provider-discovery-design.md`, monorepo-root docs).

## Monorepo context

- `wt/` (Go sibling) reads `registry.toml` and `modelman.toml` (exposure flags) read-only. The cross-language schema is pinned by the `docs/contracts/` fixtures, loaded by `tests/contracts/` here and `wt/internal/config` there — change a fixture without updating both sides and both CI jobs fail.
- `modelman benchmark` isolates providers in-process through `src/modelman/providers/lifecycle/orchestrate.py` (`src/modelman/benchmark/isolation.py` calls it directly — no subprocess, no PATH lookup). The old `bin/llm-isolate-provider`/`bin/llm-restore-providers` bash helpers were retired (issue #79); nothing in modelman shells out to `bin/` any more.

## Common development commands

The project uses `uv` for packaging and dependency management. Python 3.13 is required (`requires-python = "==3.13.*"`).

- **Install dependencies:** `make install` (runs `uv sync`)
- **Run the CLI during development:** `uv run modelman` (TUI)
- **Other subcommands:** `uv run modelman migrate`, `uv run modelman sync`, `uv run modelman expose <model_id>`, `uv run modelman unexpose <model_id>`, `uv run modelman litellm status|on|off|set`, `uv run modelman start <model_id>`, `uv run modelman stop <model_id>` / `uv run modelman stop --all` (bare `stop` is a usage error — see `local_control.py` below), `uv run modelman provider isolate/stop/stop-all/restore/list` (the lower-level per-provider lifecycle CLI issue #79 ported from bash — see the "Provider lifecycle" bullet below)
- **Run all tests:** `make test`
- **Run a single test:** `uv run pytest tests/path/to/test.py::test_name`
- **Lint / format / typecheck:** `make lint`, `make format`, `make typecheck` (or `make check` to run lint+typecheck together)
- **Run everything:** `make all` (format + test + check)
- **Clean caches:** `make clean`
- **Build a wheel:** `uv build`

The Makefile wraps the standard dev commands. Run `make help` to list targets.

### Code review

- `/code-review` — runs background review on the current diff; **always verify findings manually** before accepting (the review can misanalyze indirect usage like `monkeypatch.setitem()` or local import patterns)

### Running tests inside the pi agent

**Fixed:** The modelman test suite now has autouse fixtures in `tests/conftest.py` that prevent it from restarting the live LiteLLM proxy or shelling out to the live `ollama` daemon, so the full suite can be run safely while agents (pi, Claude) are using the proxy. The historical connection drops were caused by expose-queue tests calling `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` against the live launchd service during the run.

For day-to-day development you can still run focused subsets:

- `uv run pytest tests/test_litellm.py -q` — fast unit tests
- `uv run pytest tests/test_expose.py -q` — expose logic
- `uv run pytest tests/test_queue.py -q` — queue apply logic
- `uv run pytest -k "not screen" -q` — skip the slow Textual screen tests

The screen tests (`tests/screens/*.py`, ~1.5 min) use Textual's `App.run_test()`; the full modelman suite (~1272 tests) runs in ~1.5 min on this host. Numbers drift — re-measure before trusting them.

## Architecture

### Entry point

- `src/modelman/main.py` defines the Typer `app`. A single `@app.callback(invoke_without_command=True)` opens the TUI when no subcommand is given; subcommands: `migrate` (one-shot legacy import), `sync` (reconcile state against providers), `expose <model_id>` / `unexpose <model_id>` (LiteLLM model_list), `delete-family <name>` (removes an empty family's lingering `[[families]]` entry left behind by queue.py's stickiness — the only remaining way to do so now that FamilyScreen is gone; refuses if the family still has models), `start <model_id>` / `stop` (issue #65 local-model lifecycle — see `local_control.py` below). Three sub-Typer apps are also mounted: `app.add_typer(benchmark_app, name="benchmark")`, `app.add_typer(usage_app, name="usage")`, and `app.add_typer(litellm_app, name="litellm")`.
- `main.py::litellm_app` (mounted as `modelman litellm`) — the LiteLLM routing on/off switch wt consumes: `status` / `on` / `off` / `set --url --api-key`. Routing-policy only: mutates just modelman.toml's `[litellm]` table through `locked_state()` and never starts, stops, or restarts the proxy (proxy lifecycle stays with `restart_litellm_proxy`/`MODELMAN_LITELLM_RESTART_CMD` after config writes). `status` redacts the key to `***<last4>`. wt reads the table read-only (`finalizeCfg`/`loadModelmanState` in `wt/internal/config`).

### Textual TUI

- `src/modelman/app.py` — `ModelmanApp(App[QueuedOps | None])`. `on_mount` builds one registry-backed `ModelScreen` and pushes it as the app's only screen (FamilyScreen was removed — family list/rename/delete UI no longer exists).
- `src/modelman/screens/__init__.py` — `reload_preserving_cursor(table, repopulate)` helper: snapshots the row key under the cursor before `DataTable.clear()` (which resets to row 0) and restores the cursor onto that key after repopulate. `ModelScreen._load_models()` routes through it. `reconcile_model_state(models, registry, state)` is the reconcile write-path `ModelScreen`'s background worker delegates to (see below) — per provider it tries `resolve_local()` (the batch presence/path/size check; ollama implements it with one `ollama list` for the whole batch) and falls back to the per-model path (`is_downloaded()`/`size_of()` per model, `list_local()` at most once per provider and only when at least one model in the batch is ready). A misaligned/failed batch result degrades to the per-model path rather than dropping variants. No hard-coded provider-id checks — batch support is a provider capability.
- `src/modelman/screens/models.py` — `ModelScreen`: the app's single root screen. One DataTable (family · provider · model · loc · status · exposed · cost · size) — COST renders per-token input/cache/output prices as three space-delimited values, each a leading-space-padded 2-digit integer part and 4 decimal places (e.g. ` 2.0000`, `12.5000`) so prices over $10/million stay aligned (a missing individual price shows as `-------`; no pricing at all shows a single `-`); there is no SUB (subscription) column, though subscription pricing can still be set via `ModelForm` — sorted `(family, location, provider, model name)`, local before cloud within each family/provider group — plus a details panel Static below the table showing the row's on-disk path (`path: —` when unknown), a pending-changes bar, and a LiteLLM on/off status line (`l` toggles `[litellm].enabled`; routing policy only, never touches the proxy process). LOC is an icon (↗ cloud / ▤ local / `—` when unknown) and EXPOSED renders `Y`/`–`; there is no PATH column. **EXPOSED is the AND of the (queued-or-persisted) exposure flag and the *projected* ready value** — the same gate `_validated_entry` applies at apply time, so the column shows the post-apply state: a flagged but not-ready model renders `–`, but the user's `x` press on it cascades a `ready=True` queue (see below), so the column flips to `Y` at queue time, before apply runs. **Cloud models are exempt from the readiness gate** — via `is_cloud_effective(model)` in `litellm.py` (`openrouter` provider policy, or any model with `location = "cloud"`) used by both the TUI column and `_validated_entry` — so a flagged cloud row always renders `Y`. **Native-provider rows are exempt from the entire predicate** — `is_effectively_exposed` returns `Y` unconditionally for `model.native` (provider `auth.type = "native"`; see the exposure predicate under state.py) — but unlike the cloud exemption this is display-only: `_validated_entry` still rejects native rows at apply time ("no LiteLLM mapping"), by design. STATUS renders queued ops as glyphs with priority `✗ delete > ↓ download > → move > ✓ ready > ○`. Holds `queued_ready` / `queued_deletes` / `queued_moves` / `queued_exposes` dicts. Actions: `a` add model (family Select offers every known family plus a "+ New family…" sentinel that reveals a text input — see `forms.py` below), `e` edit (id/provider/location/family all immutable — issue #52; family re-homing isn't exposed from this dialog), `d` queue delete (works on any model — the old not-ready gate is gone; apply skips the on-disk removal if `provider.is_downloaded()` reports False), `r` toggle ready — a true file-presence toggle: ready-off queues the artifact removal (provider `delete()`, or the `state.disk_path` file for flag-only providers) and apply() re-derives the unexpose from the persisted exposure flag; ready-on queues a download/pull or flag flip, `x` toggle exposed (cascades a ready=True queue first if the model isn't ready yet — the cascaded queue is what flips the EXPOSED column to `Y`, since the column gates on `_projected_ready`, not the persisted flag), `escape` shows the apply/discard/cancel dialog if anything is queued, otherwise quits the app (this is the root screen — there's nothing to pop back to). The expose-depends-on-ready invariant is enforced in one place — `_enforce_expose_ready_rule`, run at every queue mutation: a queued `expose=True` is dropped (with a notification) whenever the *projected* ready value (`queued_ready` target, else persisted) is False, and `x` on a model queued to be made not-ready refuses rather than overwrites. Cloud rows are exempt (matching `_validated_entry`). Only the expose→ready cascade direction carries a provenance marker (`_ready_cascade_for_expose`): an `x`-cascaded download is cancelled when its expose is cancelled. The screen never queues unexpose cascades itself — `apply()` re-derives them. `_provider_list()` derives from `registry.providers` (sorted) so the Add dialog's provider dropdown is alphabetical and needs no value threaded down from elsewhere.
- `ModelScreen` runs a background worker (`_run_reconcile`, `thread=True`) on mount over every model in the registry — there is no manual reconcile binding; it runs automatically, and there is no resume-triggered reconcile (queued changes are only ever applied after the TUI has exited — see "Downloads (queued, applied on exit)" below — so there is no covering screen whose pop would need to trigger one). It delegates its actual state-write logic to the shared `reconcile_model_state()` (`screens/__init__.py`, see above) rather than duplicating it. For a local-artifact model (per `registry.model_has_local_artifact` — driven by `ModelEntry.location`/`ProviderEntry.location`, not a hard-coded provider list), it writes `state.ready`/`disk_path`/`size_bytes` directly from what the provider reports; for a cloud-located or cloud-provider model, `ready` is left alone (only `disk_path`/`size_bytes` are opportunistically updated) since reconcile cannot verify a remote model against a local filesystem. A known `state.disk_path` is preserved unless reconcile observes a fresh one.
- `src/modelman/screens/forms.py` — modal screens: `ConfirmModal` (y/n with keybindings), `ModelForm` (add/edit with provider Select, family Select, model input, location Select, and a cost section with two independent Checkbox controls — per-token pricing (input/cache/output price per million tokens) and subscription pricing (price + `month`/`year` period) — so either, both, or no pricing can be set; `parse_cost_fields()` and `parse_subscription_fields()` parse the cost fields alongside `parse_model()`; see "ModelForm parsing rules" below), `ConfirmExitDialog` (shows the pending queue; `Apply`/`Discard` both exit the app, `Cancel` stays in the TUI). The family Select is disabled/display-only in edit mode (issue #52); in add mode it's enabled and carries a `NEW_FAMILY_VALUE` sentinel option (label `"+ New family…"`) that reveals a `#new-family-input` text field — `_resolve_family()` resolves the sentinel to that typed name (blank shows an error) so a brand-new family needs no separate creation step. All modals inherit from a shared `ModelmanModal` base that enforces the dialog conventions: buttons composed left-to-right (cancel/default rightmost, primary left of it), priority-bound `escape` action that cancels even from inside an `Input`, and a `_focus_button(id)` helper for the safe-default focus on destructive prompts.

### Pending changes queue

- `src/modelman/queue.py` — `PendingChanges(registry, state, registry_path, state_path, providers, downloads, deletes, moves, exposes, litellm_path, failures, cancelled)`. `apply()` runs deletes first (so downloads free up disk), then moves (pure registry metadata: `ModelEntry.family = new_family`; a move for a model deleted in the same apply is dropped), then downloads (each calls `provider.download(variant)` → `state.set(id, replace(state.get(id), ready=True, disk_path=local_path))`), then exposes (each writes a LiteLLM `model_list` entry), then a single `save_registry()` + `save_state()`. The delete step checks `provider.is_downloaded(variant)` before artifact removal: if the artifact is absent, the provider's `delete()` is skipped but the lifecycle events, registry/state cleanup, and cascade-unexpose still run; if `is_downloaded()` raises, the artifact delete is attempted conservatively and real failures surface. The deletes loop and the ready-off loop both check `registry.find_shared_artifact_owner()` before artifact removal: when another registry entry's `path_of()` resolves to the same on-disk target (omlx dirs are keyed on the repo basename, so colliding repos share one), the file is kept and the reason surfaced, while registry/state cleanup still runs. The ready-off loop also guards removal with `provider.is_downloaded()` like the deletes loop, skips any id queued for deletion (succeeded or failed), and flag-only providers remove the artifact recorded in `state.disk_path` on ready-off via `_remove_local_artifact`. Failures are captured per-step, processing continues.

### Downloads (queued, applied on exit)

- Every TUI action — including a real download/pull against a
  reconcilable provider (ollama/omlx/llamacpp) — just populates
  `ModelScreen`'s `queued_ready`/`queued_deletes`/`queued_moves`/
  `queued_exposes` dicts. Nothing runs during the TUI session; there is
  no background download manager, no live progress screen, and no quit
  guard, because nothing is ever mid-flight while the TUI is open.
- `Escape`/`ctrl+q` with a pending queue shows `ConfirmExitDialog`;
  `Apply` exits the app carrying a `queue.py::QueuedOps` (the app's
  `App[QueuedOps | None]` return value), `Discard` restores the
  pre-session snapshot and exits with `None`, `Cancel` stays in the TUI.
  `main.py::run_tui()` runs `run_queued_ops()` against fresh on-disk
  state after the TUI process's `run()` call returns — see "Entry point"
  above.
- `PendingChanges.apply()` (`queue.py`) owns real downloads again: a
  ready-on against a mapped, non-`manages_own_cache` provider calls
  `provider.download(variant, on_progress=...)` directly, sequentially,
  the same way it did before an async `DownloadManager` briefly existed.
  `Ctrl+C` during `run_queued_ops()` raises `KeyboardInterrupt` in the
  foreground process; the runner catches it, calls `pending.cancel()`
  (reusing the existing `cancelled`/`aborted()` gate), and reports how
  many steps completed vs. were skipped. `PendingChanges.apply()`'s
  exception safety net persists any step (a delete, a move, a completed
  download) that fully finished before the interrupt landed — only the
  interrupted step itself, and anything not yet started, is lost.

### Adding a new TUI screen

See the `adding-a-tui-screen` skill.

### Thread-safety pattern for worker threads

When a screen's worker runs on a background thread (e.g., `ModelScreen._run_reconcile`):

```python
on_mount: self._app_ref = self.app  # Capture on main thread
worker: use self._app_ref, never self.app  # self.app raises NoActiveAppError when popped
```

This pattern is required because `Screen.app` is only valid while the screen is active and accessed from the main thread.

### Registry and state

- `src/modelman/registry.py` — `Registry` dataclass: providers + models + families. `ModelEntry` now carries `cost` (`Cost` dataclass with flat fields: `input_price_per_million`, `cache_price_per_million`, `output_price_per_million`, `subscription_price`, `subscription_period`; the legacy `kind`/`price_per_million_tokens`/`price_per_period` schema is migrated on load), and an optional per-model `location` that overrides the provider's location for the LOC icon. `usage_tier` has been removed. `ProviderEntry` carries `protocols` (wire protocols the provider serves — e.g. ollama `["anthropic", "openai-chat"]`, omlx `["openai-chat"]`; defaults to `["openai-chat"]` when absent). wt intersects these with each agent's declared wire protocols when resolving routes. Loaded from/saved to `registry.toml` (path precedence `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config`, matching wt's `config.RegistryPath`). See `README.md` for the exact TOML schema.
- `src/modelman/state.py` — `StateStore`: which models are downloaded, exposed, etc. (`get`/`set`/`forget_family`). Loaded from `modelman.toml` (`MODELMAN_STATE`); family visibility is `registry.known_families()` — display-name resolution (`registry.family_display_name`) was removed as dead code once FamilyScreen (its only caller) was deleted; `FamilyEntry`/`FamilyState.display_name` fields themselves are still round-tripped (queue.py's family-stickiness logic, migrate.py's legacy import) even though nothing renders them anymore. Also owns the `[litellm]` routing table (`enabled`/`url`/`api_key`) — the LiteLLM on/off switch + proxy endpoint; wt reads it read-only (see the `litellm_app` bullet above). Also owns `ModelState.running` — the per-model `running: bool` flag in each `[model_state."<id>"]` block (default `false`; the singular `[local].running_model` marker / `LocalState` of issue #65 is retired, with no migration step — an absent field reads as `false`, which is the correct post-upgrade state). The flag is a HINT recording modelman's own start/stop intent, never ground truth: every reader (`local_control.running_model_ids`, the TUI's RUNNING column, wt's picker) confirms it with a live probe and treats a probe-failing `true` as not running. Also owns `locked_state()`, the process-locked whole-file read-modify-write used by `local_control.py`'s short flag transactions and the merge-based persist in `main.py` (`sync`/`expose`/`unexpose` re-apply only their own keys onto a freshly-loaded file — a whole-file write from a stale snapshot could revert a concurrently-written `running` flag).
- `src/modelman/local_control.py` — `start_local_model(registry, model_id)` / `stop_local_model(model_id)` / `stop_all_local_models()` / `running_model_ids(registry, state)` (the first three called directly by `main.py`'s `start`/`stop`, NOT wrapped in a `locked_state` — the subprocesses must run outside the lock; only the short per-model flag writes take it). Start flow: resolve or auto-register the model (`_resolve_or_register`) → reject cloud models and providers outside `SUPPORTED_PROVIDER_IDS` → resolve the provider's launch arguments (mlx_lm_server pairing, mtplx model name, per-provider env var) fail-fast, before any teardown → re-read state and collect `other_running` for the CLI's advisory warning (never blocks — cross-provider concurrency is unrestricted, and NOTHING stop-alls) → if this model's own `running` flag is already true, PROBE it: serving ⇒ no-op "already running"; dead ⇒ clear the stale flag and do a full start. Then, per provider: ollama is flag-only (no process call at all — it lazy-loads on first request), everything else looks up the SAME-provider occupant (`_same_provider_occupant`, over every registry id on that provider; `omlx`/`omlx-6bit` are ONE occupancy domain — `_OMLX_PROVIDER_IDS` — since they share port 8000) and, when one exists, clears its flag once its teardown is CONFIRMED, not before: for the omlx ids, `stop_provider()` is called first and the occupant's flag is cleared only after that succeeds (a failure raises before either flag is touched); for mtplx and mlx_lm_server, whose prior occupant is torn down INSIDE the later `isolate_provider(..., solo=True)` call rather than by a separate precheck, the occupant's flag is cleared only after that call succeeds — clearing it earlier would mark the occupant stopped on the unconfirmed assumption that a teardown attempted later would work → `isolate_provider()` (failure clears THIS model's flag and raises, occupant's flag untouched either way) → success ⇒ locked write of `running=True` for this model alone. Models on other providers are never touched. Stop flow: `stop_local_model` no-ops when the flag is false; otherwise ollama → `ollama stop <name>` (daemon stays up), mtplx → `providers/lifecycle.stop("mtplx")`, everything else → `stop_provider()`, then a locked clear of just that model's flag; `stop_all_local_models()` is the only stop-everything path (`stop_all_local_providers()` + clear every flag), reached from `modelman stop --all`. Probing (`_probe_running`) is name-checked per provider — omlx/omlx-6bit via `/v1/models` ids matched lenient-prefix/strict-variant-tail (4-bit and 6-bit share port 8000), mtplx the same way, mlx_lm_server via a merely non-empty `/v1/models` (one target+draft pairing per process, whose reported spelling need not match the registry's) — with **ollama EXEMPT and always read as running**: its start is deliberately flag-only, so `ollama ps` (currently-LOADED models only) would permanently self-clear a flag that was never wrong. `running_model_ids()` is the shared flag+probe read path (TUI RUNNING column, `modelman start`'s inventory), opportunistically clearing flags whose probe fails. `main.py`'s sync/expose/unexpose persist merge-style (above) so they can never revert these flags.
  **Exposure predicate:** a model is effectively exposed for LiteLLM routing iff `exposed = true` (legacy `litellm_exposed` still read as a fallback on load) AND (`ready = true` OR `location = "cloud"`). Native models (provider `auth.type = "native"`) are always exposed — they bypass LiteLLM entirely. This rule is shared with wt (both read `modelman.toml` and apply the same predicate).
- `src/modelman/migrate.py` — one-shot import of legacy `~/.config/local-ai/config.yaml` + `families/*.yaml` (and optionally `wt` config) into the registry/state pair. Run once via `uv run modelman migrate`. Also runs `migrate_wt_gateway_to_litellm`: imports wt's legacy `[gateway]` block into modelman.toml's `[litellm]` exactly once (read-only on wt's file) and never clobbers already-set `[litellm]` values — a user's later `modelman litellm set` wins.
- `src/modelman/pricing.py` — OpenRouter price refresh: `fetch_openrouter_pricing` / `apply_prices` / `refresh_prices` / `should_run_price_refresh`. `_is_cloud_model` excludes native providers (`auth.type="native"`) even though they're `location="cloud"`; the per-token merge (`_merge_api_cost`) preserves manually-set cache/subscription pricing. Driven by the `modelman refresh-prices` CLI and a daily-gated startup worker in `app.py`.
- `src/modelman/sync.py` — reconciles `state` against each provider's actual filesystem (`ollama list`, HF cache scan, omlx model dir). Writes back to state. `backfill_provider_defaults` fills a missing `auth.base_url`/`protocols` on an existing provider entry from its default template (never overwrites user-set values; templateless providers like openrouter are untouched) — the omlx template's `base_url` is `http://localhost:8000`, so pre-upgrade omlx entries become routable in direct mode.
- `src/modelman/litellm.py` — `expose_model`/`unexpose_model` add/remove entries in the LiteLLM config's `model_list` (one load/save per CLI call; `unexpose_model` is a no-op for ids missing from the registry). `PendingChanges.apply()` batches its queued exposes through `apply_expose_queue` instead — one config load/save for the whole queue. Every writer runs `ensure_litellm_settings()` before save (value-enforces `litellm_settings.drop_params: true`; adds `additional_drop_params: ["reasoning_effort"]` to every `ollama_chat/*` row missing it — the BerriAI/litellm#37452 codex workaround) and saves/restarts only when the parsed document or an exposed flag actually changed. Provider prefix/api_key/cloud rules live in `PROVIDER_POLICIES` (`is_cloud()` is the TUI's gate). After a config write that actually changed the model list, `restart_litellm_proxy()` runs the `MODELMAN_LITELLM_RESTART_CMD` command (30s timeout) to reconcile the running proxy, falling back to the canonical `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` when the var is unset (the var is exported only from interactive shells like ~/.zshrc — an unset env used to silently skip the restart and leave the proxy stale); it returns warning strings (command failed) rather than printing to stderr, so the CLI surfaces them and the TUI routes them through the apply event channel (`expose:warning|…`). `_set_exposed_flag` returns whether the flag changed, so a no-op unexpose of an already-removed model does not bounce the proxy.
- `src/modelman/_toml_io.py` — shared atomic-write helpers: `atomic_write()` (temp file + rename; `binary`/`preserve_mode` options — litellm.py's YAML writer passes `preserve_mode=True` so config permission bits survive rewrites), `atomic_write_toml()` (registry/state TOML writes), plus `tomllib`/`tomli_w` shims (`drop_none`, `unknown_keys` for round-trip preservation).

### Provider plugin system

- `src/modelman/providers/base.py` defines the `Provider` abstract base class and the `VariantSpec` / `LocalModel` TypedDicts. `VariantSpec` is `total=False` with a freeform `model_info: dict[str, Any] | None` for LiteLLM-style capability keys. `Provider` requires `name`, `is_downloaded`, `download`, `list_local`, and `size_of(variant) -> int | None` (default returns `None`). Optional: `path_of(variant)` (default `None`) and `resolve_local(variants)` (batch presence/path/size, default `None` = unsupported — callers fall back to the per-variant methods; ollama implements it with one `ollama list`). Two capability class-attributes gate behavior elsewhere without hardcoding provider ids: `manages_own_cache` (default `False`; `True` means the provider's own CLI populates its cache and `download()` always raises — `queue.py`'s `PendingChanges.apply()` reads this via `ProviderRegistry.get_class(provider_id).manages_own_cache` to route a ready-on to a flag-only flip instead of a real download) and `supports_discovery` (default `True`; `False` for `MLXLMServerProvider` — a target+draft pairing, not a single discoverable artifact — and the retired `LlamaCppProvider` — `local_control.py`'s `_provider_local_models()` reads this instead of a hardcoded provider-id set).
- `src/modelman/providers/registry.py` — `ProviderRegistry.register(cls)` / `.get(name, config)`.
- Each provider module (`ollama.py`, `llamacpp.py`, `omlx.py`, `mtplx.py`, `mlx_lm_server.py`) calls `ProviderRegistry.register(ItsProvider)` at import time.
- `src/modelman/providers/__init__.py` imports every provider module solely to trigger registration. Code that needs providers should import from `modelman.providers` rather than a single submodule.
- `src/modelman/providers/_progress.py` — shared progress-callback helpers (`llamacpp.py`/`omlx.py`/`ollama.py`/`mlx_lm_server.py` all use it) plus `DownloadCancelled`, raised by the HF `ProgressTqdm` bar when its `should_cancel` callable returns True. `OMLXProvider`, `LlamaCppProvider`, and `MLXLMServerProvider`'s `download()` methods all wire `should_cancel` to their own `_cancel_requested` flag, flipped by `cancel_current()` (called from `PendingChanges.cancel()`, typically from another thread while this download's context is still active) and also, best-effort, by their own `download()` when a `BaseException` — a real Ctrl+C included — unwinds through the `snapshot_download` call. That second flip mostly does NOT extend `should_cancel`'s real reach, though: `download()`'s `finally` clears the class-level active context on its way out, so a straggling worker thread only picks up the flip if its own `display()` call lands in the brief window before that clear runs — the mechanism that actually protects an in-flight download is a concurrent `cancel_current()` call arriving while the context is still set. `PendingChanges.apply()` (`queue.py`) catches `DownloadCancelled` around the download step and persists any already-completed work before returning.
- `src/modelman/providers/mtplx.py` — MTPLX is discovery-only: it finds models MTPLX has already cached under `~/.mtplx/models` (dir names `<org>--<model>`, mapped back to the registry's `org/model` repo id by `_dir_name`/`_repo_id`) and its `download()` always raises `NotImplementedError` — MTPLX manages its own cache via the `mtplx` CLI, modelman never drives a download for it. The live server's start/stop/warmup lives separately, not in the provider class: `mtplx serve` runs as a plain backgrounded subprocess (never a LaunchAgent, one model per process like `mlx_lm_server`) tracked by a pidfile at `/tmp/local-ai-setup-mtplx.pid`, serving on port 8003 — driven by the `MtplxBackend` class in `src/modelman/providers/lifecycle/backends/mtplx.py` (see "Provider lifecycle" below).

### Provider lifecycle (`src/modelman/providers/lifecycle/`)

Local-provider isolate/stop/stop-all/restore logic, ported from the bash
helpers `bin/llm-isolate-provider`/`bin/llm-restore-providers` (issue
#79, now deleted). `orchestrate.py` holds the isolate/stop/stop_all/restore
implementations; `backends/` holds one `Backend` subclass per provider
(`ollama.py`, `omlx.py`, `mlx_lm_server.py`, `mtplx.py`, `llamacpp.py`,
registered in `backends/__init__.py`'s `BACKENDS` dict, with
`SUPPORTED_PROVIDER_IDS` marking which are fully supported vs.
retired-only like `llamacpp`) — mtplx and mlx_lm_server subclass
`backends/base.py`'s `PidfileTrackedBackend` instead of `Backend`
directly, since both are single-process, pidfile-tracked backends that
otherwise duplicated the same `self._proc` field; `probe.py`/`launchd.py`/
`pidproc.py`/`binaries.py` hold primitives the backends share
(`pidproc.py`'s `PidfileProcess` generalizes the pidfile-tracked-
background-process pattern itself — spawn/stop/log-tail — while
`PidfileTrackedBackend` only factors out the `Backend`-side `_proc`
bookkeeping). `cli.py` exposes it all as `modelman provider
isolate/stop/stop-all/restore/list` — everything runs in-process now;
nothing shells out to a script in `bin/`. `modelman benchmark` and
`local_control.py`'s `start`/`stop` both call `orchestrate.py` directly
rather than going through the CLI. `stop_all()`'s `keep` argument takes a
PROVIDER ID, matching `isolate()`/`stop()` — it resolves internally to the
right `occupancy_key` (so `--keep omlx-6bit` keeps both omlx variants) and
rejects an unknown id with `ok=False` before any teardown runs; `cli.py`'s
`stop-all --keep` just forwards the raw string.

**Testing pattern:** backend tests (`tests/providers/lifecycle/backends/test_*.py`,
e.g. `test_mtplx.py`) use `unittest.mock.patch` on the module-attribute
*as imported into the backend module's own namespace* — e.g.
`patch("modelman.providers.lifecycle.backends.mtplx.subprocess.run")`,
`patch("modelman.providers.lifecycle.backends.mtplx.probe.wait_for_port_closed")`,
`patch("modelman.providers.lifecycle.backends.mtplx._PROC")` — rather than
patching the origin module (`probe.py`, `pidproc.py`) directly. This is
the standard "patch where it's used, not where it's defined" convention,
applied consistently across the whole `lifecycle/` package; it sits
alongside (but is distinct from) the `Provider`-class `runner=` injection
seam noted below.

### Ollama capability detection

- `src/modelman/ollama_caps.py` — `parse_ollama_show(stdout)` translates `Capabilities` section entries into `model_info` (e.g. `tools` → `supports_function_calling: True`). `auto_detect_model_info(name, runner)` runs `ollama show <name>` and returns the parsed dict (or `{}` on failure). `ModelForm` calls this on add for ollama variants.

### Benchmark subsystem

- `src/modelman/benchmark/cli.py` — `benchmark_app` (mounted as `modelman benchmark`): `list-workloads`, `run` (executes a workload against a model, optionally isolated), `show-results` (reads persisted run output).
- `src/modelman/benchmark/isolation.py` — process/resource isolation for a benchmark run so results aren't skewed by concurrent load.
- `src/modelman/benchmark/runner.py` — drives a workload against a target model and times/scores it.
- `src/modelman/benchmark/results.py` — persists and loads benchmark run results.
- `src/modelman/benchmark/workloads/` — workload definitions `run` selects between.
- `src/modelman/benchmark/agent/` — the agentic coding benchmark (`modelman benchmark agent`): `suite.py` (TOML parsing + row expansion + preflight), `task.py` (task bundle loading), `workspace.py` (scratch git repo per row), `pidriver.py` (route resolution, pi process driver, speed metrics), `gates.py` (nine-gate deterministic taxonomy + composite cap), `judge.py` (blind LLM rubric judge), `report.py` (artifact writes + summary tables), `runner.py` (phase orchestration + isolation loop), `cli.py` (`run`/`list-tasks`/`list-suites`/`show`/`judge`).
  - `gates.toml`'s `tests_dir` must name a real subdirectory, never `"."` or `""` — `task.py::load_task` rejects those at bundle-load time, because `gates.py`'s gate 6/7 prefix check compares `Path` `.parts` tuples and an empty tuple (what `"."`/`""` produce) is a prefix of every path.
- Tests: `tests/benchmark/agent/` (one file per module above, plus `fixtures/fake_agent.py` and `fixtures/tasks/mini-drift/`).
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

See the `adding-a-provider` skill.

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
- **Focused test timeout:** `tests/test_expose.py` + `tests/test_queue.py` together run in well under a minute now; default timeouts are fine, but re-measure if a focused run feels slow.
- **Pyenv `VIRTUAL_ENV` warning:** `uv run` ignores an active pyenv `VIRTUAL_ENV` and uses the project's `.venv`; the emitted warning is expected and can be disregarded.
- **Local imports in tests:** `test_queue.py` and other test files use `from X import Y` inside test functions (not module-level) as a consistent pattern — this is intentional, not inconsistency
- **MagicMock provider stubs:** optional `Provider` capabilities (`resolve_local`, `path_of`) auto-exist as truthy mocks — set `stub.resolve_local.return_value = None` (or a well-formed, length-aligned list) when testing code that branches on them; reconcile treats a non-list/misaligned batch result as "no batch support" and falls back to the per-model path.
- **Ruff bugbear B905 is enforced:** `zip()` needs an explicit `strict=` (usually `strict=True`); a bare `zip()` passes some focused checks but fails `make lint`.

## Important implementation notes

- The `OllamaProvider` methods accept an optional `runner` argument so tests can substitute a mock. The default runner just calls `subprocess.run`.
- New `Provider` methods that shell out must accept an optional `runner` arg (like `is_downloaded`/`size_of`/`resolve_local` do): the autouse `_never_call_real_ollama` fixture only patches the module-level default runners, so a non-injectable subprocess call breaks suite hermeticity.
- `StateStore.set` is the single state-write path (downloads do `state.set(id, replace(state.get(id), ready=True, disk_path=local_path))`; the ISO timestamp + local path shape was the legacy `FamilyManifest.mark_downloaded`, now gone).
- `Registry`/`StateStore` save helpers rewrite the whole TOML file (`tomli_w`) — preserve unknown keys on round-trip so user-edited fields survive.
- `llamacpp` checks the Hugging Face cache (`HF_HOME/hub`) for the requested files; `omlx` downloads into `model_dir/<repo-basename>`.
- `size_of` is implemented per provider: Ollama parses the `ollama list` SIZE column (two-token `<number> <UNIT>`), llamacpp stats the primary file in the HF cache snapshot dir, omlx sums files in the model directory.
- `OllamaProvider.is_downloaded` returns `False` only when `ollama show` stderr contains "not found"; any other non-zero exit raises (transient daemon-down = unknown, not absent) so the delete step attempts removal instead of orphaning the on-disk artifact.
- `PendingChanges.apply()` is the single integration point between the TUI queue and the providers. It deliberately reorders to deletes-before-downloads so a queued delete frees disk before a queued download fills it, and runs exposes last so any queued download is visible to the LiteLLM writer. The delete step now also checks `provider.is_downloaded()` first: when the artifact is already gone, the provider's `delete()` is skipped but registry/state cleanup and lifecycle events still run — so the TUI's `d` action can queue a delete for any model, ready or not.
- Deleting an exposed model auto-queues its unexpose inside `apply()` (keyed on the state's `exposed` flag), so config.yaml never keeps a route to a deleted file.
- `reload_preserving_cursor(table, repopulate)` is the DataTable-clear helper `ModelScreen` uses: it snapshots the row key under the cursor before `repopulate()` (which calls `clear()` and resets the cursor to row 0) and restores the cursor onto that key after. If the key vanished, falls back to row 0; empty-table cases are no-ops.
- `save_litellm_config` preserves the existing config.yaml's permission bits on rewrite (mkstemp's 0600 would otherwise silently tighten them); malformed or non-dict `model_list` content is preserved or refused, never crashed on. Config writes use ruamel round-trip (`_rt_yaml()`: `typ="rt"`, `preserve_quotes=True`, `width=4096`) so hand-written comments and untouched sections survive byte-identically; changed-detection is `copy.deepcopy` before the mutation + ensure, `!=` after — save iff changed, proxy restart iff changed or a flag flipped.
- The TUI's `action_back` (`escape` from `ModelScreen`) shows the exit confirmation only if the queue is non-empty; otherwise it quits the app (`ModelScreen` is the root screen — there's nothing to pop back to).

## Configuration for end users

See `README.md` for the exact TOML schemas for `registry.toml` and `modelman.toml`, the LiteLLM `model_list` format, and the provider config field reference. Legacy `config.yaml`/`families/*.yaml` schemas are still documented for users on the pre-migration layout — run `uv run modelman migrate` to upgrade.

### Location semantics

`ModelEntry.location` (and the matching `VariantSpec` key) accepts `local` or `cloud`. The TUI treats only `local` (and legacy `None`/empty values) as on-disk:

- **ModelScreen** `LOC` renders `↗` for `cloud`, `▤` for `local`, and `—` when unset; the same rule sorts local models before cloud within each family/provider group.

Shared helpers for this live in `registry.py`: `LOCATION_LOCAL`, `LOCATION_CLOUD`, and `is_local_location()`.

### Local-model lifecycle (multi-model design, 2026-09-14)

`modelman start <model_id>` / `modelman stop <model_id>` / `modelman stop
--all`, and the modelman TUI's `s` keybinding, are the sanctioned ways to
start or stop a local model for normal (non-benchmark) usage — see
`src/modelman/local_control.py` and
`../docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md`
(monorepo-root docs; supersedes the retired single-marker design in
`../docs/superpowers/specs/2026-09-10-one-local-model-at-a-time-design.md`).
Multiple local models may run concurrently, subject to real per-provider
process limits: ollama is multi-tenant (many models may be flagged
running at once); omlx, mtplx, and mlx_lm_server are single-model-per-
process, so starting a different model on one of them replaces whatever
it was already running. Each model's `running` flag lives in
`modelman.toml`'s per-model `[model_state."<id>"]` block (`src/modelman/
state.py`'s `ModelState.running`) — a HINT, never trusted by itself: every
reader (the TUI's RUNNING column, `modelman start`'s other-running
warning, wt's picker) verifies it with a live probe first. wt reads the
flags read-only (`wt/internal/config/modelman.go`,
`wt/internal/localgate`) to filter its model picker to cloud models plus
every verified-running local model — see `wt/CLAUDE.md`'s "Local-model
gate" section.

`modelman start`'s no-arg listing and its discovered-model auto-register
path (`_provider_local_models` in `local_control.py`, gated on each
provider's `Provider.supports_discovery` class attribute — `False` for
mlx_lm_server's target+draft pairing and retired llamacpp) ask the local
providers live rather than trusting only `modelman.toml`'s cached `ready`
flag. Two different questions, two different mechanisms: "is this
*registered* model on disk?" goes through the provider's own
`resolve_local()`/`is_downloaded()`/`size_of()` (`_registered_presence`),
which derive the artifact path the same way `download()` does, while
"what's on disk that ISN'T registered?" goes through `list_local()`
(`_provider_local_models`). Never join the two on an exact
`variant_id == model_name`: omlx's `list_local()` reports the model
directory's basename while its `ModelEntry.model_name` holds the full HF
repo id — `_name_matches` (`_registered_under_name`, and the native-name
match in `_resolve_or_register`) is what bridges the two spellings, and an
exact comparison there both hid every registered omlx model and
duplicate-registered it. See
`../docs/superpowers/specs/2026-09-13-modelman-start-provider-discovery-design.md`
(monorepo-root docs).

## Foreign agent configs

`~/.codex/` and `~/.gemini/` exist on this machine. To import user-level instructions, MCP servers, slash commands, subagents, or skills from those configs, run `/import` in Claude Code (or `claude import` from a terminal) to scan what's available, then `/import --yes=<digest>` to apply the items you want.