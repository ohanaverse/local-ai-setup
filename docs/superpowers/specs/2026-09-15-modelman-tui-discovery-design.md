# modelman TUI: surface discovered-but-unregistered local models

Date: 2026-09-15
Status: approved, pending implementation

## Problem

`modelman models` (the TUI's `ModelScreen`) shows only what's in
`registry.toml`. A model downloaded straight through a provider's own
tooling — `mtplx pull`, a manual `omlx` model directory, etc. — is
invisible there even though it's fully present on disk, until someone
manually adds a `[[models]]` entry.

This gap already has a partial fix: `modelman start` (CLI) discovers
on-disk-but-unregistered artifacts live and can auto-register one at a
time, interactively, when the user types its native name (see
`2026-09-13-modelman-start-provider-discovery-design.md`). That design
explicitly scoped the TUI out ("no TUI changes ... no batch
register-everything command"). This spec is that follow-up: bring the
same discovery capability into the `modelman models` screen so a user
browsing the TUI can see and register what's already on disk, without
switching to the CLI.

Concrete example that prompted this: `mtplx models` lists 6 models on
disk; the TUI showed only 1 (the one previously auto-registered via
`modelman start`). `ls -l ~/.omlx` shows 3 model directories; the
registry has 2 entries.

## Scope

- Providers: every local provider whose `Provider.supports_discovery`
  is `True` — today that's `ollama`, `omlx`, `omlx-6bit`, `mtplx`.
  `mlx_lm_server` (target+draft pairing) and retired `llamacpp` opt out
  via that same flag, unchanged.
- Out of scope: batch "register everything" action, a standalone
  `modelman discover` CLI command, any change to `modelman sync` or
  `reconcile_model_state`. Registration stays one-model-at-a-time,
  user-initiated — same philosophy the CLI design already established.

## Discovery data flow

`local_control.py` already has the enumeration and name-matching logic
the CLI's `start` (no-arg) inventory uses: `_provider_local_models()`
(asks each local provider's `list_local()` and returns
`(provider_id, variant_id) -> LocalModel`) and `_discovered_models()`
(filters that map down to entries with no matching `registry.toml` row,
via `_registered_under_name()`'s lenient `_name_matches()` — needed
because omlx's `list_local()` reports a directory basename while
`ModelEntry.model_name` holds the full HF repo id).

Add one new public function reusing both, so the TUI needs no
provider-scanning logic of its own:

```python
def discover_unregistered_models(registry: Registry) -> list[DiscoveredModel]:
    """Every on-disk artifact from an in-scope local provider with no
    matching registry.toml entry."""
    local_map, _unqueryable = _provider_local_models(registry)
    return _discovered_models(registry, local_map)
```

`ModelScreen._run_reconcile` (the existing background-thread worker
that runs once on mount) gets one more step: after its existing
`reconcile_model_state()` call, it calls `discover_unregistered_models`
and stores the result on `self.discovered: list[DiscoveredModel]`, then
reloads the table on the main thread. One worker, not two — both calls
are read-only provider queries with no reason to run concurrently.

Unqueryable providers (a provider whose `list_local()` raised) produce
a soft `self.app.notify(...)` warning; discovery for the others still
renders. This should be rare here specifically — omlx/mtplx discovery
is a plain filesystem scan (`~/.omlx/models`, `~/.mtplx/models`), not a
live server probe like the ready-state reconcile is.

## Rendering

Discovered entries aren't `ModelEntry` rows, so they render as
synthetic `DataTable` rows appended after the sorted curated rows
(sorted `(provider_id, variant_id)`). A side dict,
`self._discovered_by_key: dict[RowKey, DiscoveredModel]`, tracks which
row keys are synthetic — correctness doesn't depend on any string-
prefix convention for the key itself, just dict membership.

Columns: FAMILY `—`, PROVIDER/MODEL from the discovered entry, LOC `▤`
(local only — discovery only covers local providers), STATUS a new
glyph distinct from the existing `✗ ↓ ↑ → ✓ ○` set (e.g. `[cyan]+[/cyan]`),
EXPOSED/RUNNING/COST `–`, SIZE formatted from `size_bytes` when known.

`action_edit_model` (bound to both Enter and `e`) gets one new branch:
if the cursor is on a discovered row, open the registration form
instead of the normal edit dialog. Every other action (`d` delete, `x`
expose, `s` start/stop, `r` ready) is untouched — they already no-op
gracefully on a row with no matching `ModelEntry`, via
`_current_entry()` returning `None`.

## Registration

Reuses `ModelForm` (the existing add/edit dialog) rather than building
a new one, with one necessary behavioral difference from a normal add:

**Provider and Model are locked (disabled), not editable.** The reason
is a genuine validation bug, not just a UX preference: `list_local()`
for omlx reports the bare directory basename (e.g.
`Qwen3.8-27B-4bit`), not an `org/repo` id, but `ModelForm.parse_model()`
rejects any single-segment omlx input ("model must be 'org/repo'").
Pre-filling that field editable and expecting the user to just hit Save
would fail validation on exactly the case this feature exists to make
easy — the user would have to type a meaningless fake org prefix to get
past it. (mtplx's discovered names already include `org/repo` via its
own directory-name reconstruction, so this is omlx-specific in
practice, but the form handles it uniformly rather than special-casing
one provider.)

Concretely: `ModelForm` gains a `discovered: DiscoveredModel | None`
constructor parameter. When set:
- Provider Select is disabled, value pinned to `discovered.provider_id`.
- Model Input is disabled, pre-filled with `discovered.variant_id` for
  display only.
- `_submit()` skips `parse_model()` entirely for this path and builds
  `repo`/`name` directly from `discovered.variant_id` — mirroring what
  the CLI's `_register_discovered_model` already does at the data
  layer (`ModelEntry(model_name=match.variant_id, fetch=Fetch(repo=match.variant_id), ...)`).
- Family (with "+ New family…"), quantization, and pricing stay fully
  editable, same as a normal add.

**The model is already on disk — registration must not queue a
download.** A normal add always sets `queued_ready[id] = True`, which
at Apply time calls `provider.download(variant)`. For a freshly
registered omlx discovery, that would call
`snapshot_download(repo_id="Qwen3.8-27B-4bit")` — HF rejects that
outright, since it isn't a valid `org/repo`. Instead, registration
writes `state.ready/disk_path/size_bytes` directly from the already-
known `DiscoveredModel` (same mechanism `reconcile_model_state`
already uses for existing entries) — no `queued_ready` entry, nothing
downloads. The new `ModelEntry` gets `source="discovered"` (not the
`"curated"` every other add produces) for provenance, matching the
CLI's existing convention.

Everything else — the id-collision guard, `save_registry()`, table
reload — reuses `_on_add_model`'s existing pipeline; the "write state
directly instead of queuing ready" is the one branch specific to this
path.

## Staleness after registration

On successful save, the registered item is removed from
`self.discovered`/`self._discovered_by_key` in memory and the table
reloads. No re-scan is triggered — discovery runs once, on mount,
matching the existing reconcile worker's cadence (no manual refresh
binding, no live re-scan on every screen event).

## Testing

- A thin test for the new `discover_unregistered_models()` wrapper
  (the underlying `_provider_local_models`/`_discovered_models` are
  already covered).
- A `ModelScreen` test: mount with a stubbed discovery result, assert
  a synthetic row renders with the new STATUS glyph; press Enter on
  it, assert `ModelForm` opens with Provider/Model disabled and pre-
  filled; submit, assert the registry gains a `source="discovered"`
  entry, `state.ready=True`/`disk_path`/`size_bytes` are set directly,
  and — the regression case that would otherwise break omlx at Apply
  time — no `queued_ready` entry was added.

## Rejected alternatives

- **Re-scan `~/.omlx`/`~/.mtplx` directly in `screens/models.py`**:
  would duplicate `_name_matches`/`_registered_under_name`'s spelling-
  mismatch handling, which already exists and is tested in
  `local_control.py`. Rejected in favor of one shared discovery
  function both the CLI and TUI call.
- **Keep Provider/Model editable in the registration form**: would
  require the user to type a fabricated org prefix to get an omlx
  discovery past `parse_model()`'s validation, for no real benefit —
  the artifact's identity is already verified by the provider's own
  filesystem scan.
- **Auto-register everything on screen load**: rejected for the same
  reason the CLI design rejected a batch `discover` command — no
  human-in-the-loop family assignment, and a silent registry.toml
  write the user didn't ask for.
