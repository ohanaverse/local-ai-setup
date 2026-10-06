# omlx as a multi-model pool — design

Date: 2026-10-06
Issue: #213 (items 1, 2, 5, 6; item 3 landed in #255; item 4 is out of scope)

## Problem

omlx is an engine pool: it can hold several models loaded at once, bounded by a memory ceiling, and it evicts by least recent use when a load does not fit. wt and modelman handle it as a server that holds one model.

- **A start replaces.** `omlxBackend.singleModel()` is `true`, so starting a second omlx model runs `omlx stop` first and unloads the model that was running.
- **A stop halts everything.** Stopping one omlx model stops the service and every other loaded model (`withFamilyCollateral`).
- **Routes disagree.** `wt litellm sync` routes every loaded omlx model, while the start hook clears the family's other routes (`Change.RemoveFamilies`). With two models loaded, the two undo each other.
- **Aliased and hidden models are mismatched.** When every pool model is loaded, wt reads the loaded set from `/v1/models`, which lists an aliased model under its alias. wt matches artifacts by directory name, so that model does not read as running.
- **modelman misses a model mid-load.** `_omlx_loaded` returns "nothing loaded" when `/health` reports `loaded_count == 0`, which is also what a pool with one model still loading reports.

None of this shows on a machine with one omlx model. All of it shows with two.

## Goal

wt and modelman treat omlx as a pool. Starting a second omlx model leaves the first loaded unless memory requires otherwise, and stopping one model leaves the service and the other models up.

**Success criteria**

1. With two omlx models that fit together, `wt start` on the second loads it beside the first. Both are routed. No prompt appears.
2. With a model that does not fit, the prompt names the models expected to be unloaded, each with its live session count. Declining changes nothing.
3. After any load, `wt litellm list` matches what omlx has loaded. Any eviction wt did not announce is printed.
4. `wt stop <omlx model>` leaves the other loaded omlx models loaded and routed.
5. `modelman start` and `modelman stop` on an omlx model produce the same pool state and the same `running` flags as the wt commands.
6. mtplx and ollama behave exactly as before.

## Decisions

These were settled during design and are not open.

| Decision | Choice |
|---|---|
| Scope | Full pool support in wt and modelman |
| A model that does not fit | Predict, name the likely victims, ask; `--replace` skips the ask |
| Who evicts | omlx. wt reconciles from omlx's status afterwards |
| modelman | Delegates normal omlx start and stop to wt |
| omlx routes | Keep following loaded state, per model |
| `wt stop <model>` on omlx | Unloads that model; the service stays up, even when it was the last one |
| `wt stop omlx` | Halts the service, as today |
| Benchmark isolation | Unchanged: restarts omlx so exactly one model is loaded |

## Out of scope

- **A command or screen to set the omlx API key** (#213 item 4). `auth.secret_ref` on the registry's omlx provider stays a hand edit.
- **Loads wt does not perform.** An agent that dials omlx directly and names an unloaded model makes omlx load it, and possibly evict, with no prompt. `wt litellm sync` corrects the routes, and the launch-time check adds the launched model's own route when it is missing; a start or a model stop does not, since neither clears the family (only `wt stop omlx` does). This is documented, not prevented.
- **Models pinned in omlx's own settings.** wt reads the `pinned` flag to predict evictions and never sets it.
- **The `mlx_lm_server` pairing identity fix** (#194 #19a/b). Separate, bounded work.

## What omlx provides

Read from the omlx 0.7.0 source (`server.py`, `engine_pool.py`).

| Endpoint | Auth | Behavior |
|---|---|---|
| `GET /health` | none | `engine_pool.model_count` and `loaded_count`. Answers 503 with the same body while pinned models preload |
| `GET /v1/models` | inference | Every pool model minus hidden ones, loaded or not, under its alias when it has one |
| `GET /v1/models/status` | management | Pool: `final_ceiling`, `current_model_memory`. Per model, by on-disk id: `loaded`, `is_loading`, `pinned`, `last_access`, `resident_estimated_size`, `estimated_size` |
| `POST /v1/models/{id}/load` | management | Blocks until loaded. 200 when already loaded. 404 unknown id. 507 too large or nothing evictable. 409 busy or already loading |
| `POST /v1/models/{id}/unload` | management | Unloads one model. 400 when not loaded. 404 unknown id |

Management auth accepts a keyless request only when the server has no API key and binds loopback.

**Keyless-inference mode.** An omlx with an API key and `allow_unauthenticated_inference = true` relaxes the inference column only: a keyless `GET /v1/models` and `POST /v1/chat/completions` work, and `/health` answers, while the three management endpoints answer 401 without the key. wt sends a key only when the registry's omlx provider names one (`auth.secret_ref`), and a registry without one is the common local setup. There wt has no status reading and can neither load nor unload through the management endpoints; "Start on a pool" and "Stop" say what it does instead. Setting `auth.secret_ref` gives full pool behavior. (Found by a check against a real omlx 0.7.0.)

**Eviction.** A load that does not fit evicts loaded models in order of `last_access`, oldest first. It skips pinned models and models with a request in flight. A model whose agent session is idle between turns is evictable.

**The limit on prediction.** omlx starts evicting at a soft watermark below `final_ceiling`: `final_ceiling * soft_threshold`, where `soft_threshold` defaults to 0.85. The threshold is not exposed. A load that fits under the ceiling can therefore still evict. wt cannot rule that out through the public API, which is why every load is followed by a reconcile (below).

## Design

### Tenancy

`backend.singleModel() bool` in `wt/internal/lifecycle` becomes `backend.tenancy() Tenancy`.

| Tenancy | Family | Start | Stop one model | Routes |
|---|---|---|---|---|
| `Exclusive` | mtplx | replaces the occupant | stops the process | whole family removed |
| `Shared` | ollama | loads beside | unloads that model | follow the pulled artifact |
| `Pool` | omlx | loads beside; may evict | unloads that model | follow loaded state, per model |

The exported `lifecycle.SingleModel(providerID)` is replaced by `lifecycle.TenancyOf(providerID)`. Every caller switches on it: `routes.go` (`StartRouteChange`, `routeRemove`), `survey/stop.go` (session counts, the stop-once-per-family shortcut) and `cmd/wt/model_cmds.go` (`withFamilyCollateral`, `stopImpact`). The `Exclusive` and `Shared` arms keep today's code.

### The pool reading

A new `localmodels.OmlxPool(cfg, client)` returns one reading of the pool:

```go
type Pool struct {
    Models    []PoolModel
    Ceiling   int64 // final_ceiling; 0 when omlx's memory guard is off
    InUse     int64 // current_model_memory
    SizesKnown bool  // false when the reading came from the fallback
}

type PoolModel struct {
    ID         string // on-disk id
    Loaded     bool
    Loading    bool
    Pinned     bool
    LastAccess float64
    Size       int64 // resident_estimated_size, else estimated_size
}
```

- **Status first.** The reading comes from `/v1/models/status`, sent with `FamilyAPIKey(cfg, "omlx")` when the registry names a key. Status reports on-disk ids, includes hidden models, and reports `is_loading`, so the alias, hidden-model and mid-load cases need no special handling.
- **Fallback.** When status refuses or does not answer, the existing `/health` plus `/v1/models` logic in `omlxLoaded` supplies the loaded ids, with `SizesKnown` false. Its error cases are unchanged: mixed counts that status will not explain remain an error, never a guess.
- **One answer.** `ServedIDs(cfg, client, "omlx")` is derived from the reading (models that are loaded or loading). The inventory snapshot carries the reading (`Snapshot.OmlxPool`), so the picker, the occupancy check and the eviction plan read the same data.

### The eviction plan

`lifecycle.Evictions(t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, known bool)` replaces `lifecycle.Occupant`.

- **`Exclusive`:** today's rule. The one running model of the family other than the target, if any.
- **`Shared`:** always none.
- **`Pool`, sizes known and ceiling above zero:** the target fits when `InUse + size(target) <= Ceiling - margin`. `poolAdmissionMarginPct` is one named constant, 15% of the ceiling: it matches omlx's default `soft_threshold` of 0.85, which omlx does not report. (It was first 10%, which predicted "fits" for a load landing between 85% and 90% of the ceiling that then evicted.) When the target does not fit, walk the other loaded, unpinned models by `LastAccess`, oldest first, subtracting each size until the projection fits. Those models are the victims. If the walk ends without fitting, every unpinned loaded model is a victim; omlx gives the final answer at load time.
- **`Pool`, sizes unknown or ceiling zero:** wt cannot tell whether the target fits, so every other loaded model is a victim. The loaded set itself is known, so `known` stays true and the prompt names real models.
- **Server refused the connection** (`Snapshot.Down`): nothing is serving, so no victims and `known` is true. A cold start plans as one that fits. The start engine still re-probes such a server before acting, in case a model came up after the snapshot.
- **Any tenancy, probe not trusted otherwise:** no victims and `known` is false, as `Occupant` reports today. Only this case leads to `*OccupancyUnknownError`.

A target missing from the pool reading (a model added to disk after omlx scanned) has size zero for the plan; the load itself reports the outcome.

### Start on a pool

`omlxBackend.start`:

1. If the service does not answer, run `omlx start` and wait for the port, as today.
2. Read the pool. If the target is loaded, return. If it is loading, poll the pool until it is loaded or the warmup timeout passes.
3. Compute the plan. With victims and no `AllowReplace`, return `*OccupiedError`, touching nothing. With `known` false and no `AllowReplace`, return `*OccupancyUnknownError`, as today.
4. `POST /v1/models/{id}/load`. The id is the pool model that name-matches the target (`localmodels.NameMatches`), else the target's directory basename. The request carries the registry key when there is one and is bounded by `warmupTimeout`. A 409 for a model already loading is handled as in step 2. When omlx refuses the load (401, 403) and no key was sent, the start falls back to the keyless chat-completion warmup (`omlxWarm`), on which omlx loads the model; this is keyless-inference mode, where `main` starts a model the same way. A server that refuses inference too answers the warmup with `*KeyRefusedError`. A key that was sent and refused gets no fallback.
5. Read the pool again and reconcile (below).

This replaces the chat-completion warmup on the start path, except as the fallback in step 4. `omlxWarm`, `lifecycle.Warm` and `wt warm` stay as they are for modelman's benchmark isolation.

In keyless-inference mode the pool reading is the fallback (no sizes), so the plan names every other loaded model and wt asks before a second load; a partly loaded pool cannot be read at all and the start gets `*OccupancyUnknownError`. The reconcile does nothing without a status reading, so an eviction there is corrected by `wt litellm sync`.

**`*OccupiedError` carries a list.** `Occupant localmodels.Entry` becomes `Occupants []localmodels.Entry`. `Exclusive` always fills exactly one. The CLI prompt (`cmd/wt/start.go`) renders the list with each entry's live session count from `refcount`. The TUI replace screen (`internal/tui/start_flow.go`) lists the ids; the picker's in-use column already shows the counts.

**Errors.**

| omlx answer | wt error |
|---|---|
| 507 | `*NoRoomError`, carrying omlx's `detail`, which names what holds the memory |
| 401, 403, a key was sent | `*KeyRefusedError`, as the warmup returns today |
| 401, 403, no key was sent | fall back to the keyless chat warmup; its own 401 or 403 is `*KeyRefusedError` |
| 404 | the existing start failure, naming the model and the pool directory |
| timeout, transport failure | the existing start failure |

### Reconcile after a load

After every `/load`, whether it succeeded or failed, wt reads the pool and compares the loaded set with the one from step 2.

- **Evicted** is every model loaded before and not loaded now.
- **One route change** adds the target (on success) and removes each evicted model's route. An evicted model's route ids are the registry model id for that family and name, when a registry entry matches, and its discovered id.
- **One line per evicted model** goes to the route output. An eviction the plan did not name is marked as unexpected.
- **Failure still reconciles.** omlx can evict before a load fails. The removal is written without a restart and sets `restartOwed`, so `Start`'s single settling bounce applies it. A failed start costs one proxy restart, like a failed replace today.

The start reports what it evicted to its caller through a new optional callback, `Options.OnUnloaded func(localmodels.Entry)`, called once per evicted model, on a failed start too. It sits beside `Options.Progress`, so `Start`'s signature and its existing callers and test seams are unchanged.

### Stop

- **`wt stop <omlx model>`** calls `omlxBackend.stopModel`: `POST /v1/models/{id}/unload`, then a pool read to confirm. A 400 "not loaded" answer, or a pool read showing the model unloaded, is success. A 401 or 403 with no key sent (keyless-inference mode) is an error that wraps `*KeyRefusedError` and names both ways out: set `auth.secret_ref` so wt can unload one model, or run `wt stop omlx`. wt does not fall back to stopping the service, because a model stop must never take siblings down. The route change removes that model's ids only (`Change.Remove`).
- **`wt stop omlx`** calls `omlxBackend.stop`: `omlx stop`, wait for the port to close, remove the family's routes. Unchanged.
- **`routeRemove`** takes the stopped model as well as the provider. `Exclusive` removes the family, `Shared` writes nothing, `Pool` removes the model's ids.
- **The stop picker and `confirmStop`** count sessions per omlx model. The "stopping it also stops X" line and the stop-once-per-family shortcut apply to `Exclusive` only.

### Routes

- The start hook for `Pool` adds one model and removes evicted ids. It sets no `RemoveFamilies`.
- `wt litellm sync` already routes every loaded omlx model and needs no change. With the start hook no longer clearing the family, the two agree.
- `EnsureModelRoute` (the launch-time check) is unchanged.

### wt command surface

| Command | Behavior |
|---|---|
| `wt start <id> --plan --json` | Dry run. Prints the plan and changes nothing |
| `wt start <id> --replace --json` | Starts without asking and prints the result |
| `wt stop <id> --yes` | Exists today; unloads one omlx model under `Pool` |

`--json` never prompts. Without `--replace`, `wt start <id> --json` on a start that would evict exits 1 with the plan on stdout.

```json
{"id": "omlx/B", "status": "would_unload",
 "would_unload": [{"id": "omlx/A", "sessions": 1}]}
```

`status` is `running`, `fits`, `would_unload` or `unknown` (the pool could not be read). `would_unload` is empty for every status but `would_unload`.

```json
{"id": "omlx/B", "status": "started", "unloaded": ["omlx/A"]}
```

`status` is `started` or `already_running`. A start that fails is exit 1 with the message on stderr and nothing on stdout.

Both shapes are pinned by a new fixture, `docs/contracts/wt-start-cli.sample.json`, loaded by a Go test in `cmd/wt` and a modelman contract test in `tests/contracts/`.

### modelman

modelman's normal omlx start and stop delegate to wt. `omlx` and `omlx-6bit` are both the omlx family here.

**`wt_bridge.py`** gains three calls, following `warm` and `served_ids`:

- `start_plan(model_id) -> StartPlan | None`: runs `wt start <id> --plan --json`. A read, so every failure is `None`.
- `start(model_id, replace) -> StartOutcome`: runs `wt start <id> --json` with `--replace` when asked. Raises `WtBridgeError` with wt's message on failure. Its timeout sits above wt's warmup budget, as `WARM_TIMEOUT` does.
- `stop(target) -> None`: runs `wt stop <target> --yes`.

**`local_control.py`:**

- `start_local_model`, omlx family: the occupant lookup, `stop_provider()` and `isolate_provider(solo=True)` are replaced by `wt_bridge.start(resolved_id, replace=True)`. On success it sets the target's `running` flag and clears the flag of each id in `unloaded`. `StartResult` gains `unloaded`, and the CLI prints it. The already-running check, the stale-flag clear and the trailing `sync_routes` stay.
- `stop_local_model`, omlx family: `wt_bridge.stop(model_id)`, then the flag clear and sync, as today.
- `stop_all_local_models`: `wt_bridge.stop("omlx")` once for the family, which halts the service.
- `same_provider_occupant` returns `None` for the omlx family. omlx no longer has a single occupant.
- `_omlx_loaded`: ask `/v1/models/status` keyless first, then `wt_bridge.served_ids("omlx")`. The `/health` count arithmetic is removed. An omlx that predates status still falls back to the `/v1/models` list.

**`screens/models.py`:** the `s` key's confirm dialog, for an omlx model, takes its text from `wt_bridge.start_plan(id)` fetched in a worker. `fits` shows a plain start confirmation; `would_unload` names the models; `unknown` or `None` says wt could not tell what would be unloaded.

**Unchanged:** benchmark isolation and `modelman provider isolate|stop|restore` keep the Python omlx backend; mtplx, `mlx_lm_server` and ollama keep their paths.

**Ids.** modelman's resolved id and wt's id are the same string for registered and discovered omlx models (`<registry id>`, or `omlx/<directory>`), so ids pass in both directions unchanged.

## Error handling summary

| Situation | Result |
|---|---|
| Would evict, no `--replace`, TTY | Prompt naming the victims; "no" leaves everything as it was |
| Would evict, no `--replace`, no TTY | `*OccupiedError` message naming the victims and `--replace` |
| Sizes unknown, other models loaded | Same prompt, naming every other loaded model |
| Pool could not be read at all | `*OccupancyUnknownError`, as today |
| omlx evicts a model the plan did not name | Route removed, line printed and marked unexpected |
| Load fails after an eviction | Evicted model's route removed, one proxy restart, the load error returned |
| Unload of a model already gone | Success |
| Model stop refused for want of a key (keyless-inference mode) | Error naming `auth.secret_ref` and `wt stop omlx`; nothing is unloaded and the service stays up |
| Load refused for want of a key, no key in the registry | The start falls back to the keyless chat warmup |
| `wt` missing when modelman starts an omlx model | modelman's existing `WtNotFoundError` |

## Testing

**wt**

- A fake omlx (`httptest`) serving the five endpoints, driven through `testEnv()`.
- Plan: fits; does not fit, with victims oldest first and pinned skipped; sizes unknown; ceiling zero; target not in the pool reading.
- Start: second model loads beside the first with no prompt; `*OccupiedError` lists victims and sends no load; 507 becomes `*NoRoomError`; a failed load after an eviction removes the evicted route; an unnamed eviction is reported; a target already loading is waited for.
- Probe: aliased model matched with every model loaded; hidden model; model mid-load in an empty pool; status refused falls back.
- Stop: one model unloaded with siblings and their routes intact; `wt stop omlx` clears the family.
- Routes: the id the start hook writes equals the id sync desires (the existing pin), now with two loaded models.
- `--plan --json` and `--replace --json` against the contract fixture.
- Existing mtplx and ollama tests pass without edits.

**modelman**

- Bridge parsing for `start`, `start_plan`, `stop`, including malformed output and non-zero exits.
- `start_local_model` sets the target flag and clears `unloaded` flags from a stubbed outcome; a bridge failure leaves flags as they were.
- `_omlx_loaded` with status answering, status refused, and an omlx without status.
- A pilot test for the confirm dialog with a would-unload plan.
- Contract test on the shared fixture.
- `conftest.py` autouse stubs for the new bridge calls, so no test runs the real `wt`.

**Live**

- Needs a second omlx model on disk. A small one is enough for the fits path.
- Criteria 1, 3 and 4 are checked against a scratch registry with `WT_LITELLM_CONFIG` set.
- Criterion 2 needs a ceiling two models exceed. If omlx's memory limit setting can be lowered for a test run, it is checked live; otherwise the fake-server tests are its only coverage and the PR says so.
- wt's replace screen is captured through the pty driver. modelman's TUI dialog needs a check by eye.

## Docs

- `wt/docs/internals/local-models.md`: the omlx paragraph, lifecycle, route hook and start/stop notes.
- `wt/docs/wt-start-stop.md`: pool behavior, `--plan`, `--json`.
- `wt/CLAUDE.md`: the lifecycle and local-model summaries, and the verification command list.
- `modelman/docs/internals/local-model-lifecycle.md` and `modelman/CLAUDE.md`: delegation, the probe.
- `docs/guides/02-providers-and-models.md` and `08-maintenance-and-troubleshooting.md`, where they describe omlx start and stop. No live model state is embedded.
- Root `CLAUDE.md`: the "Stop mechanisms per backend" gotcha and the per-provider limit note.
- Changelogs.

## Delivery

1. **PR 1, wt.** Tenancy, the pool reading, the plan, start, stop, routes, `--plan` and `--json`, the contract fixture, wt docs. Complete by itself: until PR 2, `modelman start` on omlx keeps today's halt-and-replace behavior.
2. **PR 2, modelman.** Delegation, the probe change, the TUI dialog, modelman docs. Closes #213.
