# Configured is exposed; local models are discovered — design

Date: 2026-10-02
Status: approved design, pending implementation plan
Issues: #179 (this work), #180 (folded into Phase A's contract fixtures)

## Goal

Remove the `exposed` concept from the whole stack (#179):

- **Phase A — cloud:** a cloud model is in the catalog, and routed through
  LiteLLM, if and only if it has a `registry.toml` entry. Configured means
  exposed; there is no per-model switch.
- **Phase B — local:** local models stop being configuration. What exists is
  what `localmodels.Inventory` discovers on disk (or running). A registry entry
  for a local model becomes an optional **overlay** of metadata (family, tags,
  fetch/draft/quantization, model_info, cost) attached to a discovered model.

Long-term direction (not part of this work): everything moves into wt. So all
new logic here lives in wt; modelman only deletes code and calls
`wt litellm sync`.

## Non-goals

- Keeping a cloud model configured but hidden. To hide one, delete it.
- Rewriting existing registry ids, or migrating usage/survey/rotation/profile
  history.
- A modelman view of discovered models that have no overlay entry (wt's
  picker and `wt litellm list` already show them).
- Moving modelman's download manager into wt.

## Decisions (from the brainstorm)

| Question | Decision |
|---|---|
| Local registry metadata | Optional overlay, matched to a discovered model |
| Overlay entries not on disk | Hidden from wt's picker; still listed (downloadable) in modelman |
| `modelman benchmark` default | No default: errors unless models or `-F` are named |
| Discovered models without an overlay | Routed through LiteLLM like any other running local model |
| Delivery | One spec, two phases, cloud first |

## Phase A — configured is exposed (cloud)

### A1. Catalog membership and flags

- **Rule:** native → in catalog; local → in catalog (Phase B narrows this to
  "discovered"); cloud → in catalog. wt's `IsExposed` becomes `InCatalog`; the
  cloud branch `exposed AND (ready OR location = cloud)` is deleted, and wt
  stops calling `ExposedFlag` and `ReadyFlag` for cloud models.
- **`modelman.toml`:**
  - `exposed` and legacy `litellm_exposed`: deleted. modelman's reader
    ignores them and its writer stops emitting them, so they leave the file on
    the next write. wt stops reading them.
  - `ready`: kept for local models only (it means "downloaded"). Cloud models
    stop reading or writing it.
  - `running`: unchanged (wt already ignores it).
- **Day-one effect:** none. Every configured cloud model on the reference host
  is exposed and ready today, so each agent's eligible-model list is identical
  before and after Phase A. This is acceptance check A-1.

### A2. Route ownership and reconciliation (wt)

- **Ownership marker:** every row wt writes to LiteLLM's `config.yaml` gets
  `model_info.wt_managed: true` (set in `litellm.BuildEntry`). **wt touches a
  row only when it owns it: the row carries the marker, or its `model_name`
  equals a managed registry id** — for removal that is any cloud id with a
  LiteLLM mapping or any local id wt can route, running or not; adoption below
  is narrower, into the desired set (cloud, or a running local). The name clause
  covers rows config.yaml accumulated before the marker existed, which would
  otherwise strand a stale route forever.
  Unmarked rows with any other name are hand-written and are never touched —
  not removed, not rewritten.
- **Adoption (one-time migration):** on the first `wt litellm sync` after
  upgrade, an unmarked row whose `model_name` equals an id wt should route (a
  registry cloud id, or a running local id) is rewritten with the marker. Every
  other unmarked row stays hand-written. `wt litellm list` gains a
  managed/hand-written indicator so leftover hand-written rows are easy to
  spot and delete.
- **`wt litellm sync` reconciles everything.** Desired set:
  - every registry cloud model whose provider is non-native and has a LiteLLM
    mapping (`PolicyFor`), plus
  - every running local model (Phase A: registry ones; Phase B: discovered
    ones too).

  Sync adds missing rows, rewrites rows whose content differs (so updated
  prices reach `config.yaml`), and removes owned rows outside the set. wt
  enforces three `litellm_params` on every sync — `model`, `api_base`,
  `api_key`, whose values come from the registry and provider policy — and
  carries over every other key the old row's `litellm_params` set that the new
  row lacks, so a hand-written timeout, rate limit or header survives adoption
  and every later sync (the presence-based `additional_drop_params` /
  `use_chat_completions_api` fall out of the same rule). Registry-level
  customization belongs in the registry entry (`model_info`, which
  `BuildEntry` already merges into the row it builds); anything else wt has
  already marked is overwritten by the next sync.
  Existing rules stay: a family whose probe is `partial` is left alone; a
  refused connection (`Snapshot.Down`) still clears that family's routes; a
  provider with no mapping is reported per id without blocking the rest; one
  proxy restart, only when the file changed.
- **`wt litellm sync --dry-run`:** new; prints the planned adds, rewrites,
  removals and adoptions, plus the probe warnings the real sync prints
  (skipped families, refused daemons). It writes nothing and never re-checks
  the removal set against a fresh probe — that recheck is the real sync's
  under-the-lock step, so a dry run reports the plan the caller's probe built.
- **Removed:** `wt litellm expose|unexpose` (and their `--skip-ready-gate` /
  `--dry-run` flags). `wt start`/`wt stop` keep their targeted route writes,
  which now stamp the marker.
- **Verification gate:** before any marked row is written on a real host,
  confirm the LiteLLM proxy starts with a `wt_managed` model_info key and
  `/model/info` still answers. Fallback if it does not: a YAML comment marker
  on the row, which the comment-preserving editor already carries.

### A3. modelman

- **One helper replaces the expose machinery:** `routes.sync()` in
  `litellm.py` runs `wt litellm sync` once. Every path that changes the
  registry or local-model state calls it once at the end. A wt failure is a
  warning, never a failed command: the registry is the source of truth, and
  the next sync converges.
- **Removed:**
  - CLI `modelman expose` / `modelman unexpose`;
  - TUI: the `x` key, the EXPOSED column, queued exposes, the exposes list in
    the exit-confirm dialog, the merge-from-disk of `exposed`;
  - `litellm.py`: `expose_model`, `unexpose_model`, `apply_expose_queue`, the
    stop-all batch unexpose, `_set_exposed_flag`, `is_effectively_exposed`,
    the expose checks in `_validate_locally`, `passes_ready_gate`,
    `is_cloud_effective`;
  - `wt_bridge.py`: `expose` / `unexpose` (adds `sync`, with `--dry-run`;
    keeps `list`, `providers`, `status`);
  - `state.py`: the `exposed` field.
- **Changed call sites:**

  | Path | Today | After |
  |---|---|---|
  | Queue apply (add / edit / delete / ready on-off) | per-model expose/unexpose | one `sync()` after the apply |
  | `modelman start` / `stop` / stop-all | expose on start, unexpose on stop, writes `exposed=True` | one `sync()`; no flag writes |
  | `_register_discovered_model` | registers, then exposes | registers only (removed entirely in Phase B) |
  | `ollama-catalog sync` | "Not yet exposed" plan section; re-exposes every page model | section removed; one `sync()` at the end (also refreshes prices in config.yaml) |
  | `refresh-prices` | registry only | `sync()` at the end |
  | `modelman benchmark`, no models named | exposed local models | error: name models or pass `-F <family>` |
  | `sync.py` reconcile | keeps `exposed` | ignores it |

### A4. Contract fixtures, tests, docs

- **`docs/contracts/modelman.sample.toml`:** drop the exposure rule and its
  entries; keep one legacy row that still carries `exposed = true`, to prove
  both readers ignore it.
- **New `docs/contracts/catalog-predicates.sample.toml` +
  `catalog-predicates.expected.json`** (closes #180; a separate file rather
  than new rows in `registry.sample.toml`, whose provider/model counts both
  existing fixture tests assert): rows covering each catalog and pricing
  branch — openrouter model, native cloud provider, ollama cloud model on
  the local ollama provider, non-native cloud provider without a LiteLLM
  mapping, a model whose `provider_id` has no provider entry, and a local
  model. The expected id lists live once, in the JSON; both sides assert
  them:
  - catalog members (wt `InCatalog`);
  - OpenRouter-priced ids (modelman `_is_openrouter_priced`, wt
    `HasOpenRouterPricedModel`);
  - routable cloud ids (wt sync's desired cloud set).
- **`docs/contracts/litellm-cli.sample.json`:** drop the `exposed`/
  `unexposed` actions and the native "cannot be exposed" error; add sync's
  result and `--dry-run` plan shapes and `list`'s managed indicator.
- **Tests (written first):**
  - wt `internal/litellm` (real `Apply` on a temp config.yaml): sync adds,
    rewrites on price change, removes marked rows of deleted models; never
    touches an unmarked row, even one named like a registry id that is not in
    the registry; adoption marks matching rows once; `--dry-run` writes
    nothing; an unchanged sync does not restart.
  - wt `internal/config`: `InCatalog` includes every cloud model even with the
    legacy flag keys present in modelman.toml.
  - modelman: each changed path makes exactly one bridge `sync` call; a failed
    sync warns and the command succeeds; `benchmark` with no models errors with
    the hint; saving state never writes `exposed`. Tests asserting expose/
    unexpose calls (`test_expose.py`, queue, local_control, the autouse
    `wt litellm list` fake in `conftest.py`) are rewritten to assert sync.
- **Docs:** root `CLAUDE.md` (drop both `exposed` caveats, replace the
  `grep -c "exposed = true"` example with `wt litellm list`, fix the
  `test_expose.py` example); `modelman/CLAUDE.md`, `wt/CLAUDE.md` (drop the
  exposure-predicate sections; document the marker and reconciliation);
  guides 00, 01, 02, 04, 05, 06, 08, 10 (02 and 04 are rewritten around
  sync, not edited); `README.md`, `modelman/README.md`.

### Phase A acceptance (reference host)

1. **Catalog unchanged:** each agent's eligible-model list, captured by a
   script over wt's catalog, is identical before and after.
2. **Hand-written rows safe:** back up `config.yaml`; `wt litellm sync
   --dry-run` proposes no deletion of an unmarked row; the user reviews it;
   the real sync marks only wt-written rows.
3. **Delete round trip:** deleting a throwaway cloud model in modelman removes
   its route via sync.
4. **Marker accepted:** the proxy starts with marked rows and `/model/info`
   answers (the A2 verification gate).
5. `make test-all` passes.

### Phase A PRs

1. wt: marker, adoption, reconciling sync with `--dry-run`, `InCatalog`,
   fixtures including #180's rows. Compatible with the current modelman.
2. modelman: switch to `sync()`; delete the expose machinery; explicit
   benchmark models.
3. wt: remove `litellm expose|unexpose`, the flag readers and the picker's
   EXPOSED column (nothing left to show once the flag is gone).
4. Docs and guides.

Each PR leaves `make test-all` green. wt's additions land first, so modelman
never calls a command that does not exist yet.

## Phase B — local models are discovered (overlay)

Starts after Phase A merges.

### B1. Which local models wt shows, and their ids

- **Rows come only from `localmodels.Inventory`:** models on disk, plus
  running ones (mlx_lm_server: running only, as today). Registry entries no
  longer create rows. An overlay entry whose artifact is not on disk is not
  listed; the `absent` status and its "pull or download it first" block are
  removed.
- **Overlay matching:** a discovered model takes a registry entry when the
  provider family and `model_name` match (the matching inventory already does,
  in `inventory.go`). A matched row gets the entry's family, tags, profile and
  cost; an unmatched row is `Source=discovered`, with no family or tags.
- **Ids:**
  - matched row → its registry id (existing usage, survey, rotation, profile
    keys stay valid; nothing migrates);
  - unmatched row → `config.DiscoveredModelID` (`provider/<name>`, as today).

  Accepted cost: registry ids for local models are not uniform on the
  reference host (ollama ids equal `provider/model_name`; one omlx and one
  mtplx id spell `/` as `--`). Adding or removing an overlay for such a model
  starts a new stats key. Docs state the convention for new overlay entries:
  id = `provider/model_name`.
- **Filters:** `-T`/`-F` match overlay family/tags only, so untagged
  discovered rows are hidden whenever a filter is set (today's
  `HideDiscovered`, now the stated rule). With no filter, every on-disk model
  is listed.
- **Sort and rotation:** unchanged. Rotation never auto-selects a discovered
  row; a user can still pick one.

### B2. Routing discovered models

- **Route name = the row's id:** the registry id for a matched row,
  `DiscoveredModelID` otherwise; the same id `-M`, usage and the picker use.
- **Entry for a discovered model:** wt builds a minimal
  `config.Model{ID, ProviderID, ModelName, Location: local}`; `PolicyFor`
  supplies `api_base`, the model prefix and the key; pricing is the explicit
  $0 local models already get; the row carries the marker.
- **Code changes:**
  - `routeAfterStart` routes discovered models (today it warns "is not in the
    registry; no LiteLLM route added").
  - Sync's local desired set = every running inventory row, registered or
    discovered; untrusted families skipped.
  - Single-model providers (omlx, mtplx): a stop or replace removes every
    **marked** route of that family, replacing the registry-derived
    `familyModelIDs` list, which would miss discovered siblings.
  - Removed: the "not in LiteLLM" refusal for discovered rows
    (`Row.RefusedByRoute`'s discovered branch, `pickerBlockedReason`), and the
    `ReadyFlag` gate before writing a route.
- **Known gap, to verify:** discovered models get no overlay `model_info`
  capability flags (`supports_function_calling`, `supports_vision`). Expected
  to be harmless; checked by acceptance B-2. If an agent needs a flag, add
  per-provider defaults in `PolicyFor`; don't make overlays mandatory.
- **Two-slash route names** (e.g. `omlx/mlx-community/Qwen3.8-27B-4bit`):
  expected to work (one registry mtplx id already has this shape); checked for
  every agent by acceptance B-2.

### B3. modelman

- **Unchanged:** the TUI's registry-centered local view (overlay entries in
  every state, download/delete/ready queue, fetch/draft/quantization editing);
  `ready` as download state reconciled by `modelman sync`; mlx_lm_server
  pairings from overlay `fetch.local_path` + `draft`; `modelman benchmark`
  resolving registry ids (benchmarking a discovered artifact means adding an
  overlay first).
- **Changed:**
  - `modelman start <artifact>` no longer auto-registers
    (`_register_discovered_model` removed). The start runs, and its single
    `sync()` routes the model under its discovered id. Existing
    `source = "discovered"` entries stay as valid overlays.
  - Deleting a local overlay entry does not change routing: a running model
    keeps its route under its discovered id after the next sync. Only a stop
    removes a local route.

### B4. Fixtures, tests, acceptance, PRs

- **Fixtures:** `registry.sample.toml` gains a local overlay entry absent
  from disk (excluded from the catalog, still parsed by both sides) and one
  `--`-style plus one `provider/model_name`-style local id, pinning overlay
  matching to `model_name`. `litellm-cli.sample.json`'s sync plan gains a
  discovered local route under its discovered id, marked.
- **Tests (written first):**
  - wt `catalog`: overlay not on disk → no row; discovered + matching overlay
    → overlay id/family/tags; unmatched → `DiscoveredModelID`,
    `Source=discovered`; `-T`/`-F` hide untagged discovered rows; rotation
    never lands on a discovered row.
  - wt `lifecycle`/`litellm`: starting a discovered model writes a marked
    route under its discovered id; stopping a single-model provider removes
    every marked route of that family, discovered siblings included; sync
    routes running discovered models and skips untrusted families; unmarked
    rows are never touched.
  - wt `cmd/wt`: a non-TUI launch of a discovered model by a LiteLLM-forced
    agent (codex) launches instead of being refused.
  - modelman: `start <artifact>` writes no registry entry and makes one
    `sync()` call.
- **Acceptance (reference host):**
  1. `wt` lists exactly what is on disk; the not-downloaded ollama entries
     are gone from the picker but still in modelman's TUI.
  2. `wt smoke` against a discovered ollama model and a two-slash mtplx/omlx
     id passes for every agent, codex included (settles the B2 capability and
     naming questions).
  3. Stopping an mtplx model removes its marked route; a hand-written row
     survives.
  4. `wt stats` still shows history under existing ids.
  5. `make test-all` passes.
- **PRs:**
  1. wt: discovery-driven catalog and overlay matching (hidden absent rows).
  2. wt: discovered routing (start hook, sync, marker-based family removal);
     remove the "not in LiteLLM" refusal and the ready gate.
  3. modelman: stop auto-registering on start.
  4. Docs: guides 02, 03, 06, 08; both package `CLAUDE.md` files.

## Risks

| Risk | Mitigation |
|---|---|
| Sync deletes a hand-written route | Removal ownership is marker OR managed-registry-name (so pre-marker wt rows cannot strand); any other unmarked row is never touched; adoption carries the row's hand-written params, not just its name; dry-run reviewed on the real host before the first write |
| LiteLLM rejects the unknown `model_info` key | Verified before the first marked write; comment-marker fallback |
| A one-sided change to a shared rule | Contract fixtures asserted by both CI jobs (#180) |
| Discovered models lack capability flags an agent needs | Acceptance B-2 with every agent; per-provider defaults in `PolicyFor` if needed |
| Stats split when an overlay is added for a `--`-style id | Accepted and documented; id convention for new entries |
