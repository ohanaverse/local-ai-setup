# `modelman start` provider-backed discovery

**Status:** Draft
**Date:** 2026-09-13
**Scope:** `modelman/` package only (`local_control.py`, `main.py`, tests)

## Motivation

`modelman start` (no `model_id`) lists local models by reading
`registry.toml` + `modelman.toml` state only (`list_local_exposed_models()`
in `local_control.py`). It never asks a provider what's actually on disk,
so:

- The listing can drift from reality — `state.ready` is a cached flag
  written by `sync`/TUI reconcile, not a live check. A model deleted
  outside modelman, or a fresh omlx/mtplx artifact modelman hasn't
  reconciled yet, isn't reflected until the next reconcile.
- It shows no size information, even though every in-scope provider's
  `Provider.list_local()` already returns `size_bytes`.
- A model downloaded straight through a provider's own tooling
  (`ollama pull`, `mtplx`'s own cache, a folder dropped into
  `~/.omlx/models`) is invisible to modelman until someone manually adds
  a `[[models]]` entry via the TUI or hand-edits `registry.toml` — there
  is no path from "provider has it" to "modelman can start it."

`ModelEntry` already carries an unused `source: "curated" | "discovered"`
field (`registry.py:157`), which suggests this was anticipated but never
wired up. This design wires it up.

## Goals

1. `modelman start` (no args) shows a live, three-way view of local
   models: registered-and-downloaded, registered-but-not-downloaded, and
   discovered (on disk, unregistered).
2. `modelman start <name>` accepts either a registered model id (today's
   behavior, unchanged) or a provider-native name for a discovered
   artifact — in the latter case it auto-registers the model
   (`source="discovered"`), exposes it, and starts it in one step.

## Non-goals

- `mlx_lm_server` is excluded. It's a target+draft pairing chosen at
  start time, not a single downloaded artifact — "discover one model and
  register it" doesn't map onto that shape. It keeps working exactly as
  today (manual `registry.toml` pairing).
- No new TUI screens or `FamilyScreen`/`ModelScreen` changes. Once a
  discovered model is written to `registry.toml`, the existing TUI reads
  it like any other model — no code path there needs to change.
- No changes to `sync`, `reconcile_model_state`, or the download queue.
- No batch "register everything found" command (rejected alternative —
  see below).

## Current behavior (for reference)

- `list_local_exposed_models(registry, state)` (`local_control.py:155`):
  filters `registry.models` to those with `model_has_local_artifact()`
  true and `is_effectively_exposed()` true, sorted by id. Only the model
  matching `state.local.running_model` gets a live probe
  (`_probe_running`, via `ollama ps` or `GET /v1/models`).
- `start_local_model(registry, model_id, ...)` (`local_control.py:184`):
  looks up `registry.model(model_id)` (raises `LocalControlError` if
  unknown), resolves isolate args, stops whatever's running, starts the
  requested model, writes the `[local].running_model` marker.
- `Provider.list_local() -> list[LocalModel]` (`{variant_id, path,
  size_bytes}`) already exists and is implemented for the three in-scope
  providers:
  - `ollama.py::list_local` — one `ollama list` call; `variant_id` is the
    tag as ollama reports it (e.g. `llama3.1:8b`), `size_bytes` populated.
  - `omlx.py::list_local` — scans `~/.omlx/models`; `variant_id` is the
    directory basename (the repo's last path segment — the org prefix is
    not recoverable from disk), `size_bytes` always `None`.
  - `mtplx.py::list_local` — scans `~/.mtplx/models`; `variant_id` is the
    full `org/repo` (directory names are `org--repo`, reversed by
    `_repo_id()`), `size_bytes` always `None`.

## Approaches considered

- **(A, chosen) Extend `modelman start` in place.** One command surface;
  the no-arg listing gains a live query and a third bucket, `start
  <name>` gains a discovery fallback. Reuses the existing (unused)
  `source` field.
- **(B) Separate `modelman discover` command** that finds and registers
  everything at once, leaving `start` untouched. Rejected: a second
  command to remember, and doesn't give "start it directly by
  provider-native name" in one step.
- **(C) Lazy match inside `start <name>` only**, no listing changes.
  Rejected: drops the staleness/size half of the motivation.

## Design

### 1. Classification helper

New function in `local_control.py`, e.g.:

```python
@dataclass
class LocalModelInventory:
    downloaded: list[LocalModelStatus]        # registered + on disk
    not_downloaded: list[str]                  # registered model ids, not on disk
    discovered: list[DiscoveredModel]          # on disk, unregistered

@dataclass
class DiscoveredModel:
    provider_id: str
    variant_id: str
    size_bytes: int | None

def inventory_local_models(registry: Registry, state: StateStore) -> LocalModelInventory: ...
```

For each in-scope provider present in `registry.providers`
(`ollama`, `omlx`, `omlx-6bit`, `mtplx`), call `provider.list_local()`
once. Build a `{(provider_id, variant_id): LocalModel}` map. Then:

- For every `ModelEntry` with `model_has_local_artifact(model, provider)`
  true: if `(model.provider_id, model.model_name)` is in the map →
  **downloaded** (carry `size_bytes` from the map); else →
  **not_downloaded**. (Unlike today's listing, this is **not** filtered
  by `is_effectively_exposed` — it's a full local-artifact inventory.
  The running marker's `*`/`(running)` annotation is preserved via the
  same `_probe_running` call as today, only for the entry matching
  `state.local.running_model`.)
- Any map entry whose `(provider_id, variant_id)` matches no
  `ModelEntry.model_name` under that `provider_id` → **discovered**.

`list_local_exposed_models` stays as-is (still correct, still tested) —
`inventory_local_models` is additive, not a replacement, since nothing
else consumes the old function's narrower semantics and there's no
reason to disturb its tests.

### 2. `start` (no args) output

```
Registered, on disk:
* ollama/llama3.1-8b        4.9 GB (running)
  omlx/qwen3-8b-mlx-4bit    5.1 GB

Registered, not downloaded:
  mtplx/foo-org--bar-model

Discovered (not in registry.toml — `modelman start <name>` to add):
  ollama:llama3.2:3b        2.0 GB
  omlx:some-other-model     —
```

Empty sections are omitted. Discovered rows are printed as
`<provider_id>:<variant_id>` (colon, not slash — it is explicitly *not*
a usable id yet, to avoid it being mistaken for one).

### 3. `start <name>` auto-register fallback

In `start_local_model`, when `registry.model(model_id)` raises
`KeyError`:

1. Call `inventory_local_models`-equivalent discovery (or reuse a shared
   internal helper) restricted to in-scope providers; look for exactly
   one `(provider_id, variant_id)` where `variant_id == model_id`.
   - Zero matches → today's `unknown model: {model_id}` error, unchanged.
   - More than one match (different providers, same literal string) →
     `LocalControlError` naming both providers and asking the user to
     qualify which one (not expected in practice: ollama tags use `:`,
     omlx/mtplx use `org/repo`).
2. One match → **prompt interactively** for `family`
   (`typer.prompt("Family", ...)`), showing `known_families(registry,
   state)` as a hint list. Free text accepted; no pre-existing
   `FamilyEntry` required (`derived_families()` unions from
   `ModelEntry.family` directly, so an unseen name is valid).
3. Build:
   - `id = f"{provider_id}/{variant_id.replace('/', '--')}"` (same
     scheme `forms.py`'s `parse_model()` uses for TUI-added models).
   - `ModelEntry(id=id, family=family, provider_id=provider_id,
     model_name=variant_id, location="local", source="discovered")`.
4. Write the registry entry under `locked_registry()`.
5. Write state under `locked_state()`: `ready=True`,
   `disk_path`/`size_bytes` from the discovery record.
6. Call `expose_model(registry, state, id, default_litellm_config_path())`
   — this both sets `exposed=True` (via `set_exposed`) and writes the
   LiteLLM `model_list` entry, restarting the proxy if changed. It must
   run *after* step 5, since `expose_model` → `_validated_entry` →
   `passes_ready_gate` requires `state.ready` already true.
7. Continue into the existing start flow using the new `id` exactly as
   if the user had typed it.

Steps 3–6 happen once, at first discovery. A subsequent `modelman start
<same-name>` resolves the now-registered id on the first
`registry.model()` lookup and never re-prompts.

### 4. Error handling

- A discovery-provider `list_local()` call failing (e.g. `ollama`
  daemon down) must not fail the whole listing/start — catch and treat
  that provider's contribution as empty, same tolerance
  `_probe_running` already has for its own failures.
- The family prompt has no default; an empty answer re-prompts once,
  then errors with a clear message (no silent empty-string family).

### 5. Testing

- `tests/test_local_control.py`: unit tests for `inventory_local_models`
  with mocked `list_local()` per provider (downloaded / not_downloaded /
  discovered classification, provider-call-failure tolerance).
- `tests/commands/test_local_control.py`: CLI-level tests for the new
  three-section `start` output, and for `start <discovered-name>`
  driving the family prompt via `CliRunner(input=...)` and asserting the
  written registry entry (`source="discovered"`), state
  (`ready`/`exposed`), and LiteLLM `model_list` entry.
- A repeat `start <same-name>` after auto-registration does not re-prompt
  and does not create a duplicate entry.

## Open risk

`omlx`'s `list_local()` only reports a directory basename, never the
full `org/repo`. A discovered omlx entry's `model_name` will be that
basename — sufficient to start it (the running provider process is
keyed the same way) but not sufficient to `download()`/re-fetch it later
if the artifact is ever deleted. This is an existing limitation of
`OMLXProvider.list_local()`, not something this design introduces or
needs to fix.
