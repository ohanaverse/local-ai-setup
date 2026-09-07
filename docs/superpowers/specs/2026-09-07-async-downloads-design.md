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

**On success:** set `state.ready=True`, `disk_path`, `size_bytes`; save state;
run post-download actions (deferred exposes).

**On cancel:** clean up partial files (see "Partial-download cleanup"), mark
`cancelled`.

**On failure:** record the error, mark `failed`; the model stays not-ready.

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

## Partial-download cleanup

- **oMLX** (`local_dir`): on cancel, `shutil.rmtree` the partial target
  directory.
- **llamacpp** (HF cache): on cancel, remove `.incomplete` files for the repo.
- **Ollama**: `ollama pull` is resumable; killing the process leaves ollama's
  own state, which it reconciles on the next pull — no extra cleanup.

## Parallel HF downloads (required fix)

`ProgressTqdm` uses **class-level** slots for `on_progress`/`should_cancel`,
which break under parallel HF downloads (two downloads clobber each other's
context). Fix: switch to `contextvars.ContextVar` (thread-local, propagates to
child threads) so each download's callbacks are isolated.

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
  isolation, post-download actions).
- Screen tests for `DownloadScreen`, locking, and the quit guard.
- Provider cleanup tests (partial dir removal, `.incomplete` removal).
- Contract: no change to `registry.toml` / `modelman.toml` schema.
