# Family & Model Screen Redesign

Status: merged (PR #25, 2026-08-30)
Date: 2026-08-29

## Summary

Cleans up `FamilyScreen`'s delete behavior, replaces `ModelScreen`'s two-pane
(provider list + model list) layout with a single sorted table scoped to one
family, and generalizes "downloaded" into a provider-agnostic **ready**
concept that also covers cloud/native providers that have nothing to
download. Introduces **native providers** — providers whose name matches an
agent-worktree agent (`claude`, `codex`, `copilot`, ...) — so modelman can
register model configs (`claude/native`, `claude/opus`, ...) for agents
launched directly by `wt`, without any download mechanics.

## Motivation

- Today's model screen requires drilling into a provider pane before seeing
  any models; the user wants one flat, sorted view.
- "Downloaded" as the sole readiness signal doesn't fit providers with
  nothing to download (`openrouter`, and the new native-agent providers).
  It also has a live bug: reconcile determines readiness from
  `provider.size_of()`, which returns `None` for pulled ollama `:cloud`
  models (they have no local footprint), so those models can never show as
  ready even after a successful `ollama pull ...:cloud`.
- Family delete today allows deleting a family with undownloaded models
  after a confirm dialog, silently discarding their definitions. The user
  wants this blocked outright.
- `agent-worktree` just unified its own "native model" detection onto a
  `Model.Native` field derived from `auth.type == "native"` (commit
  `a5d1586`, 2026-08-29). `modelman`'s `AuthConfig.type` already documents
  `"native"` as a valid value, anticipating this. This design is the
  modelman-side half of that convention: registry.toml becomes the shared
  source both tools agree on.

## Non-goals

- No changes to `llamacpp`/`omlx` download mechanics.
- No new `OpenRouterProvider` Python class — openrouter stays a flag-only
  provider (per the user: "not sure if open router requires anything ...
  just store a flag").
- No change to how `agent-worktree` itself launches native models — this is
  registry/state plumbing on the modelman side only.

## 1. Data model changes

### `ModelState.downloaded` → `ModelState.ready`

`state.py`'s `ModelState.downloaded: bool` is renamed to `ready: bool`.
`load_state` accepts either TOML key — `ready` if present, else falls back
to the legacy `downloaded` key — so existing `modelman.toml` files keep
working with no migration step. `save_state` always writes `ready` going
forward; the legacy key is never written again.

`disk_path` / `size_bytes` are unchanged in shape and meaning; they're
still populated only for reconcilable providers and stay `None` for
flag-only ones.

### Reconcile switches from `size_of()` to `is_downloaded()`

Both `FamilyScreen._run_reconcile` (`screens/families.py`) and
`ModelScreen._run_reconcile` (`screens/models.py`) currently compute
"downloaded" as `provider.size_of(spec) is not None`. Both switch their
readiness signal to `provider.is_downloaded(spec)` (the abstract method
every registered `Provider` already implements). `size_of()` is still
called, but only to populate the SIZE column — a `None` size no longer
implies "not ready".

This directly fixes the ollama `:cloud` gap: `OllamaProvider.is_downloaded`
runs `ollama show <name>`, which succeeds for pulled cloud references even
though `size_of` (parsed from `ollama list`'s SIZE column) returns `None`
for them.

Because reconcile's ready signal is now correct for cloud-located ollama
models, `ModelScreen.action_delete_model`'s existing
`entry.location != "cloud"` special-case (added in #24 to let cloud models
bypass the "not downloaded" refusal) is no longer needed and is removed —
`_is_ready()` (renamed from `_is_downloaded()`) already returns `True` for
a pulled cloud model.

### Flag-only providers

A provider is **flag-only** when `ProviderRegistry.get(provider_id, ...)`
raises (no Python class registered for it) — this covers `openrouter`
today and every native-agent provider (no `Provider` subclass will ever
exist for `claude`, `codex`, etc.; there is nothing to reconcile against).
Both reconcile loops already skip providers that fail this lookup
(`except Exception: continue`); no new code is needed there. For these
providers, `ready` is *purely* the stored `ModelState.ready` flag — never
overwritten by reconcile, only ever changed by the ready toggle (§3).

### Native providers

`registry.py` gains `sync_agent_providers(registry: Registry) -> None`,
mirroring `sync.py`'s existing loop over `DEFAULT_PROVIDER_IDS`
(`sync.py:136`): read agent names from `~/.config/agent-wt/config.toml`'s
`[[agents]]` table (`MODELMAN_WT_DIR` override, matching the usage
subsystem's existing convention), and for every agent name missing from
`registry.providers`, append:

```python
ProviderEntry(
    id=agent_name,
    name=agent_name.title(),
    location="cloud",
    auth=AuthConfig(type="native"),
)
```

Called from `FamilyScreen._load_from_disk`/`_refresh_from_disk` (so the
provider dropdown always includes native agents, even before any model
references one) and from `sync.py`'s existing repair pass. A missing or
unreadable agent-wt config is not fatal — `sync_agent_providers` is a
no-op in that case, same tolerance `migrate.py` already has for a missing
agent-worktree config.

### LiteLLM exposure — no change needed

`provider_policy("claude")` (and any other native/unmapped provider) is
already `None` in `PROVIDER_POLICIES`, so `expose_model` /
`apply_expose_queue` already raise `ExposeError("... has no LiteLLM
mapping")` for these. The TUI adds a pre-flight check so this surfaces as
a friendly notify instead of an apply-time failure (§3).

## 2. Family screen changes

`FamilyScreen.action_delete_family` drops its two-tier logic. If
`self.registry.models_by_family(family_name)` is non-empty — regardless of
ready state — show an informational `ConfirmModal`-style message:

> "Family 'X' has N model(s). Remove or move them before deleting this
> family."

routed to `_on_blocked_confirm` (informational only, already exists).
Delete proceeds without any confirmation only when the family has zero
models (today's empty-family branch, unchanged). `_on_delete_family_with_
variants` and its "delete anyway, loses definitions" `ConfirmModal` are
deleted as dead code.

The family table's `DOWNLOADED` column is renamed `READY` (still counting
models whose `ready` is `True`, sourced from the same reconcile-overlay-or-
state fallback logic already in `FamilyScreen.reload()`).

Everything else — families listed regardless of model count, SIZE column,
reconcile worker — is unchanged.

## 3. Model screen: single-pane layout + ready toggle

### Layout

`ModelScreen.compose()` drops the `Horizontal(provider-pane, model-pane)`
split for a single `DataTable`. The screen still opens scoped to one
family (`ModelScreen(family=...)`, unchanged constructor shape) — per
explicit decision, this is *not* becoming a global cross-family view.

Columns: `FAMILY` (constant per row — `self.family` — shown for column-
spec consistency even though every row in this view shares it), `PROVIDER`,
`MODEL`, `LOCATION`, `STATUS`, `EXPOSED`, `SIZE`, `PATH`.

Rows sorted by `(provider_id, model_name)`, fixed order (no interactive
re-sort). `selected_provider`, the provider `DataTable`, and
`on_data_table_row_highlighted`'s provider-switch handler are deleted.
`on_data_table_row_selected` / `action_select_row` simplify to "Enter
always edits the row under the cursor" (the provider-vs-model-table focus
branching goes away with the second table).

`_provider_list()` (used by the Add dialog's provider options) no longer
has a "currently selected provider" to default from; it defaults to the
provider of the last-added-or-edited model this session, else the first
available provider.

### STATUS glyphs

Priority order is unchanged in spirit, meaning shifts:

- `✗` (red) — queued full delete (model definition being removed).
- `↓`/`↑` (yellow) — queued ready-on (download/pull queued) / queued
  ready-off (clear queued).
- `→` (magenta) — queued move.
- `✓` (green) — ready.
- `○` (dim) — not ready.

### Ready toggle (`x`, `action_toggle_ready`, replaces `action_toggle_download`)

`ModelScreen.queued_downloads: dict[str, VariantSpec]` is replaced by
`queued_ready: dict[str, bool]` — the toggle's *target* state, following
the same toggle-a-dict-entry pattern `queued_downloads`/`queued_exposes`
already use.

Pressing `x` flips the target ready state for the row under the cursor:

- target `True`, reconcilable provider, not currently ready → identical to
  today's download-toggle-on (queues a download).
- target `True`, flag-only provider → queues a flag-only ready-on (no
  provider call at apply time — just `state.ready = True`).
- target `False`, reconcilable provider, currently ready → queues a
  **clear**: at apply time, calls the provider's delete/rm path (reusing
  `PendingChanges._delete`'s existing logic) but does **not** remove the
  `ModelEntry` from the registry.
- target `False`, flag-only provider → queues a flag-only ready-off (just
  `state.ready = False` at apply time).
- Either off-case (reconcilable or flag-only) cascades a forced unexpose if
  the model is currently exposed — same rule the existing delete step
  already applies (`queue.py:177-180`), just triggered by `queued_ready[id]
  is False` instead of `model_id in deletes`.

Same UI-level guard as today's download toggle: pressing ready-on when the
model is already ready (or ready-off when it's already not ready) is a
no-op — nothing is queued. This mirrors the existing early-return in
`action_toggle_download` (`if self._is_downloaded(mid): return`).

### Delete model (`d`, `action_delete_model`) — unchanged in kind, narrowed in scope

Stays the *stronger* action: removes the `ModelEntry` from the registry
entirely (as today), plus the same on-disk-clear + auto-unexpose cascade.
The distinction from ready-off: ready-off keeps the model's definition
around (so the user can flip it ready again later without re-adding it via
the form); delete removes the definition too. The existing
`entry.location != "cloud"` bypass in the "nothing to delete" refusal is
removed (see §1 — `_is_ready()` now correctly reports ready for cloud
ollama models, so the general "not ready — nothing to delete" refusal
already covers this case without a special-case).

### Expose gate (`l`, `action_toggle_expose`)

Two changes:

1. Drop the `is_cloud(entry.provider_id)` special case in the "must be
   downloaded" gate — `ready` already unifies "downloaded" (reconcilable)
   and "flagged available" (flag-only/cloud), so the gate becomes simply
   `if not self._is_ready(mid): refuse`.
2. Add a pre-flight check: if `provider_policy(entry.provider_id) is None`
   (native providers, any other unmapped provider), refuse with "Provider
   has no LiteLLM mapping — cannot expose" instead of letting the toggle
   queue and fail deep inside `apply()`.

## 4. Add/Edit form (`ModelForm`, `forms.py`)

### Provider

Becomes a real `Select` in **Add** mode. Options = `self._provider_list()`
(already merged with live native-agent providers via
`sync_agent_providers`, §1). In **Edit** mode the provider is shown
disabled/read-only, same as today — id stays stable, no
replace-on-provider-change complexity.

### Family

Unchanged — already a `Select`.

### Model name

Plain text `Input`, parsed per provider-kind in `parse_model()`:

- `ollama` — unchanged: bare tag, rejects `/`.
- `llamacpp` / `omlx` — unchanged: HF `org/repo[/path/to/file]` parsing.
- **native** (`auth.type == "native"`) — new branch: blank input → sentinel
  `model_name = "native"`; non-blank input is used verbatim as
  `model_name` (no slash-splitting). `id` is always
  `f"{provider}/{model_name}"` (`claude/native`, `claude/opus`,
  `codex/native`, ...), matching agent-worktree's `Model.Native`
  convention exactly (sentinel name `"native"`, not `"default"`).
- **openrouter** (and any other provider that is neither ollama, an HF
  provider, nor native) — new branch: the raw string is stored whole as
  `model_name` (e.g. `anthropic/claude-opus`), no repo/files split —
  openrouter has no separate fetch mechanics to represent.

### Location

New `Select`, options `cloud` / `local`:

- `llamacpp` / `omlx` → forced `local`, disabled.
- `openrouter` / native → forced `cloud`, disabled.
- `ollama` → editable (the one provider where the same model name can
  genuinely be pulled either as a local model or a `:cloud` reference).

### Ready status untouched by add/edit

Already true by construction and requires no new code: `_on_add_model`
always queues a fresh ready-on for a brand-new model (nothing to
preserve); `_on_edit_model` never touches `queued_ready`/state unless the
id was already in `queued_ready`, in which case it just refreshes the
pending spec (same pattern `queued_downloads` refresh has today).

## 5. Queue / apply changes (`queue.py`)

`PendingChanges.downloads: list[(model_id, VariantSpec)]` is replaced by
`ready: list[(model_id, VariantSpec, bool)]` — `(model_id, spec, target)`
triples carrying the ready toggle's target state (mirrors how `moves`
carries `(model_id, new_family)`).

`deletes` is unchanged in meaning: full model removal, still runs first,
still removes the `ModelEntry`, still cascades unexpose.

Apply order stays `deletes → moves → ready → exposes → save` (clears
still free disk before any ready-on downloads, matching the existing
"deletes before downloads" rationale — a clear is disk-freeing exactly
like a delete is).

For each `(model_id, spec, target)` in `ready`:

- `target=True`, reconcilable, not currently ready → `provider.download()`
  (identical to today's download step; same `download:start/done/fail`
  event tags).
- `target=True`, flag-only → `state.ready = True`; emits
  `ready:start|done` (new tags — nothing to download, but StatusScreen
  still gets a line to show).
- `target=False`, reconcilable, currently ready → provider delete/rm,
  reusing `PendingChanges._delete`'s existing fallback-unlink logic,
  **without** touching `self.registry.models`; emits `delete:start/done/
  fail` tags (same tags as full delete — StatusScreen doesn't need to
  distinguish the two, the registry-removal difference is invisible to
  the progress log).
- `target=False`, flag-only → `state.ready = False`; emits
  `ready:start|done`.
- Either off-case: if `self.state.get(model_id).litellm_exposed`, queue a
  forced unexpose (append `(model_id, False)` to `self.exposes`, dropping
  any conflicting queued expose for the same id) — identical cascade to
  the existing full-delete step, just triggered here too.

`ModelScreen`'s `queued_downloads` attribute, `_refresh_pending_bar`
text, and `ConfirmExitDialog`'s pending-summary all rename to
`queued_ready` / "ready N" accordingly. `ConfirmExitDialog`'s listing
splits queued-ready entries into "ready-on" (`↓`) vs "ready-off" (`↑`)
lines (using the target bool) alongside the existing delete/move/expose
sections, so the user can see what's about to run before confirming.

## 6. Testing plan

- **State/registry**: legacy `downloaded` key still loads into `.ready`
  (round-trip test); `sync_agent_providers` appends missing agent
  providers idempotently (running it twice doesn't duplicate entries) and
  no-ops tolerantly on a missing/unreadable agent-wt config.
- **Reconcile** (`FamilyScreen`, `ModelScreen`): asserts against
  `is_downloaded()` mocks instead of `size_of()`; add an ollama `:cloud`
  case that previously could never show ready and now does.
- **`FamilyScreen`**: delete-with-models (downloaded or not) is always
  blocked with no confirm-anyway path; delete-empty still works; column
  header/value renamed to READY.
- **`ModelScreen`**: single-table rendering and fixed sort order; ready-
  toggle on/off for a reconcilable provider (queues download vs. queues a
  clear that does not remove the registry entry); ready-toggle for a
  flag-only provider (pure state flip, no provider call, no subprocess);
  delete-model still removes the registry entry; expose gate keyed on
  `ready` with the `is_cloud` special-case removed; new "no LiteLLM
  mapping" refusal for native providers.
- **`ModelForm`**: native provider model-name parsing (blank → `native`
  sentinel, named → verbatim, id = `provider/model_name`); openrouter
  plain-string parsing (no repo/files split); location `Select` lock
  rules per provider kind; provider `Select` disabled in edit mode,
  enabled in add mode.
- **`queue.py`**: `ready` list's four target/provider-kind combinations
  (download, flag-only-on, clear-without-registry-removal, flag-only-off);
  cascade-unexpose on either ready-off path; apply ordering (deletes →
  moves → ready → exposes → save) still holds.
