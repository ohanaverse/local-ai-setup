# Async Background Downloads

**Date:** 2026-09-07
**Status:** Approved (design)

## Problem

Today, model downloads are synchronous and apply-on-exit. When a model needs
downloading (toggle-ready, add-model, or expose-cascade), the download is
queued in memory and only runs when the user leaves the model screen and
confirms "Apply". The user is then stuck on `StatusScreen` until the download
finishes (or is cancelled). There is no way to keep using modelman while a
download runs, and quitting mid-download is unguarded.

## Goal

Make model downloads a background operation:

1. When a model needs downloading, the download starts immediately in the
   background; the user can continue using modelman.
2. While a model is downloading, it is locked against modification.
3. Attempting to quit while downloads are in progress notifies the user and
   routes them to a download screen to cancel first.
4. A dedicated download screen shows in-progress downloads.

## Non-goals

- Deletes, ready-off clears, moves, and exposes remain apply-on-exit (they are
  not made async).
- No change to the `registry.toml` / `modelman.toml` schemas.
- No change to the provider download *mechanisms* (Ollama `pull`, oMLX/llamacpp
  `snapshot_download`) beyond the cancellation/cleanup fixes below.

## Decisions (from brainstorming)

- **A + C:** downloads are async; deletes/moves/exposes stay apply-on-exit; an
  expose of a not-yet-downloaded model queues and auto-applies when the
  download completes.
- **Locking:** while downloading, `d` (delete), `e` (edit), and move are
  blocked; `r` cancels the download; `x` (expose) is allowed and queues+waits.
- **Parallel:** multiple models download simultaneously.
- **Expose deferral:** at apply time, an expose whose model is still
  downloading is deferred and applied automatically on download completion
  (option (a)).
- **Add-model immediacy:** `a` saves the registry entry immediately (like
  `_on_edit_model` already does) and starts the download immediately.
- **Discard-cancels-cascade:** an `x`-cascaded download is cancelled when its
  expose is discarded (preserves `_ready_cascade_for_expose` semantics).

## Architecture

### New component: `DownloadManager`

New module `modelman/src/modelman/downloads.py`. Owned by `ModelmanApp`
(created once, survives screen navigation). Screens reach it via
`self.app.downloads`.

Responsibilities and API:

- `start(model_id, variant, provider_config, on_complete)` — spawn a background
  thread that runs `provider.download()`, streams progress, and invokes
  `on_complete(local_path)` on success.
- `cancel(model_id)` — cancel one download (calls that provider's
  `cancel_current`).
- `is_downloading(model_id) -> bool` — for locking.
- `has_active() -> bool` — for the quit guard.
- `states() -> list[DownloadState]` — for the download screen.
- `register_post_download(model_id, action)` — for deferred exposes.

`DownloadState` is a dataclass: `model_id`, `variant`, `provider`, `status`
(`downloading` / `done` / `failed` / `cancelled`), `progress` (latest line),
`error`.

**Parallel isolation:** each download gets a *fresh provider instance* via
`ProviderRegistry.get(name, config)`, so per-download cancellation state
(`_current_proc` on Ollama, `_cancel_requested` on oMLX/llamacpp) is isolated.
Each download runs in its own daemon `threading.Thread`; UI updates marshal
through `app.call_from_thread`.

**On success:** set `state.ready=True`, `disk_path`, `size_bytes`; save state
(via the locked merge-on-save path below); run post-download actions
(deferred exposes).

**On cancel:** clean up partial files (see "Partial-download cleanup"), mark
`cancelled`. Any post-download action registered for this `model_id` is
discarded, not run.

**On failure:** record the error, mark `failed`; the model stays not-ready.
Any post-download action registered for this `model_id` is discarded, not
run — the failure is already surfaced via the download's own `failed`
status, so no separate notification is needed for the dropped action.

### Queue changes

`PendingChanges.apply()` drops the download step. It now runs: deletes → moves
→ ready-off clears → exposes → save.

`ModelScreen.queued_ready` splits:

- **ready-on** is no longer queued — it immediately calls
  `downloads.start(...)`.
- **ready-off** stays queued (apply-on-exit clear).

The `r` key becomes three-way:

- not ready → start download (immediate)
- ready → queue clear (apply-on-exit)
- downloading → cancel the download

Models that do not actually download (flag-only providers — native/unmapped —
and cloud-located models) keep their existing immediate flag-flip behavior:
"ready on" just sets `state.ready=True` with no background thread.

### Locking

While `downloads.is_downloading(mid)`:

- `d` (delete) → blocked with a notification.
- `e` (edit) → blocked with a notification.
- move (via edit) → blocked.
- `r` → cancels the download.
- `x` (expose) → allowed; queues and waits (deferred).

The STATUS column shows a distinct "downloading" glyph (⏳) for in-flight
models.

### Download screen

New `DownloadScreen` (`screens/downloads.py`), opened with `g` from
`FamilyScreen` and `ModelScreen`. A `DataTable` with rows: model · provider ·
status (`downloading`/`done`/`failed`/`cancelled`) · progress
(bytes/percent/speed). Per-row cancel via a keypress. Non-blocking — the user
can leave and keep working. A footer/header indicator ("⏳ N downloading") is
shown while downloads are active.

### Quit guard

Guard all exit paths (`q` on `FamilyScreen`, `ctrl+q` App default). If
`downloads.has_active()`, show a dialog — "Downloads must be cancelled before
quitting" — with a **Review Downloads** button that pushes `DownloadScreen`.
No direct quit while downloads are active. This also fixes the pre-existing
quirk where `ctrl+q` quits immediately from any screen, bypassing the
apply-on-exit confirm.

### Expose deferral

At apply time, each queued expose whose model is still downloading is
registered as a post-download action on the `DownloadManager` and skipped in
this apply. On download completion, the manager runs the action (write the
LiteLLM entry + set `litellm_exposed=True` + save).

### State synchronization

`StateStore`'s save (`save_state()`) is a whole-file overwrite from whatever
in-memory `StateStore` it's given — there is no merge, and today that's safe
because only one thing ever mutates `modelman.toml` at a time (a synchronous
apply). This design breaks that assumption: `DownloadManager` writes
`modelman.toml` from background threads on completion, while the user can
independently run a `PendingChanges.apply()` for an unrelated delete/move/
expose at the same time (the locking rules above require this to be
possible — `x` and unrelated-model applies are allowed during a download).
Two concrete races follow directly from "multiple models download
simultaneously" plus "apply-on-exit stays available for other ops":

- Two parallel downloads finishing close together each do a naive
  load-mutate-save; the second can stomp the first's `ready=True` write.
- A download completes *during* an `apply()` run, which holds an in-memory
  `StateStore` snapshot taken before the run started; `apply()`'s final
  `save_state()` overwrites the whole file, silently reverting the
  download's just-written `ready=True`.

Fix, scoped to `state.py` and `queue.py` (does not touch `registry.toml` —
downloads never mutate the `Registry` object, only `StateStore`):

1. Add a `state.py` helper that does load-merge-save for one or more
   `model_id` deltas, under a module-level lock. `DownloadManager` uses it
   for every completion/failure/cancel write instead of holding and saving
   a whole `StateStore` snapshot.
2. `PendingChanges.apply()`'s final save reloads state fresh from disk and
   merges in only the `model_id`s it actually touched this run (deletes,
   ready-loop entries, exposes — apply() already knows exactly which ids
   these are) onto that fresh copy, instead of saving its own
   possibly-stale snapshot wholesale.
3. The same lock serializes both write paths so they can't interleave at
   the byte level either.

## Partial-download cleanup

- **oMLX** (`local_dir`): on cancel, `shutil.rmtree` the partial target
  directory.
- **llamacpp** (HF cache): on cancel, remove `.incomplete` files for the repo.
- **Ollama**: `ollama pull` is resumable; killing the process leaves ollama's
  own state, which it reconciles on the next pull — no extra cleanup.

## Parallel HF downloads (required fix)

`ProgressTqdm` uses **class-level** slots for `on_progress`/`should_cancel`,
which break under parallel HF downloads (two downloads clobber each other's
context).

The obvious-looking fix — `contextvars.ContextVar` — is wrong and was
verified (against `huggingface_hub` 1.28.0, the version pinned here) to make
things worse, not better: `contextvars.ContextVar` values do not propagate to
new threads (confirmed empirically: a value set in a parent thread reads back
as the default in any `threading.Thread` or `ThreadPoolExecutor` worker
spawned from it). `snapshot_download()` internally runs an 8-worker
`ThreadPoolExecutor` (`hf_thread_map`) for per-file downloads — even for a
*single* model — and the per-file bar's `.update()`/`.display()` calls
execute inside those worker threads, not the thread that called
`snapshot_download()`. The current class-level slot works today specifically
*because* a plain class attribute is visible identically from any thread —
it survives that thread-boundary crossing. A `ContextVar` would not: `display()`
running on an HF worker thread would always see the default (`None`),
silently breaking progress lines **and cancellation** (`should_cancel` goes
through the same mechanism) for every oMLX/llamacpp download, not just
concurrent ones.

**Fix:** keep the class-level shared slot (it has to stay — it's what makes
the thread-crossing work), and add a `threading.Lock` held for the full
`set_active_context()` → `snapshot_download()` → `clear_active_context()`
critical section, so only one HF-backed `snapshot_download()` call ever owns
the slot at a time. Two oMLX downloads starting together still both show
`downloading` immediately in the UI (DownloadManager threads are both alive
and their state is independent), but their actual byte transfers serialize
against each other while the lock is held. Ollama is unaffected — it uses
its own dedicated Popen + reader-thread plumbing with an explicitly-passed
callback, no shared state, so it runs fully in parallel with anything.

## Error handling and edge cases

- Download fails → status `failed`, error surfaced on the download screen and
  via notification; the model stays not-ready.
- Cancel during download → cleanup partial files; the model stays not-ready.
- Discard (exit without apply) while a download is in flight → the download is
  independent of the queue and continues; only queued ops are discarded. The
  exception is an `x`-cascaded download, which is cancelled when its expose is
  discarded (preserves `_ready_cascade_for_expose`).
- App quit with downloads active → blocked by the quit guard until cancelled.

## Testing

- Unit tests for `DownloadManager` (start/cancel/complete/fail, parallel
  isolation, post-download actions, post-download actions dropped on
  cancel/fail).
- Screen tests for `DownloadScreen`, locking, and the quit guard.
- Provider cleanup tests (partial dir removal, `.incomplete` removal).
- State-sync test: a queued `apply()` (unrelated model) run concurrently
  with a `DownloadManager` completion write must not lose either write —
  both `model_id`s reflect their correct final state in `modelman.toml`
  after both finish.
- Contract: no change to `registry.toml` / `modelman.toml` schema.
