# TUI for modelman — Family & Model Management

**Date:** 2026-08-26
**Status:** Approved design

## Summary

Turn `modelman` into a Textual TUI for managing local LLM model families and the models within them. The app opens on a **family screen**, lets the user drill into a family's **model screen**, and queues all model changes (adds, edits, deletes, downloads) until the user exits that screen — at which point deletes run first, then downloads. The existing `modelman download <family>` command becomes a shortcut that launches the TUI directly at that family's model screen.

This effort only **stores** LiteLLM capability fields on variants; generating a LiteLLM `config.yaml` (which also includes cloud models) is a separate, later effort.

## Context

- Families are YAML manifests at `~/.config/local-ai/families/<family>.yaml` (`MODELMAN_FAMILY_DIR` overrides the dir for tests). A manifest holds `family`, `display_name`, `variants`, `downloaded` (variant_id → `{downloaded_at, local_path}`).
- A **model** is a `VariantSpec` (`id`, `provider`, `name`, `repo`, `files`, `quantizations`).
- Providers live behind a registry; each provider implements `is_downloaded`, `download` (accepting an optional `on_progress` callback for streaming), `list_local`, and accepts an optional `runner` for test injection.
- Config lives in `~/.config/local-ai/config.yaml` (`MODELMAN_CONFIG` overrides the path).

## Goals / non-goals

**Goals**
- A full-screen TUI with family and model management.
- All model changes queued in memory and applied on exit (deletes before downloads).
- Freeform LiteLLM capability (`model_info`) fields on each variant, auto-detected where reliable.

**Non-goals**
- LiteLLM config export (later).
- Adding non-local / cloud providers (later).

## Architecture

A Textual `App` with a screen per view and a small change-queue module independent of the UI.

```
src/modelman/
  app.py                  # Textual App + screen routing + keybindings
  screens/families.py     # FamilyScreen
  screens/models.py       # ModelScreen
  screens/status.py       # StatusScreen (live apply progress)
  screens/forms.py        # AddFamilyModal, ModelForm, ConfirmExitDialog, CancelApplyDialog
  queue.py                # PendingChanges: in-memory change queue + apply()
  commands/download.py    # reworked: launches TUI at a family's model screen
```

### Entry points

- `modelman` (no args) → launch TUI at `FamilyScreen`.
- `modelman download <family>` → launch TUI at that family's `ModelScreen`. The old
  questionary-based selection flow is removed. The `--all`/`-y` flags are dropped for now
  (non-interactive download can be revisited later if needed).

### Screens

**FamilyScreen** (default)
- `DataTable` of all configured families, columns: **family · display · variants · downloaded · size**.
  - `size` is the total downloaded bytes across variants, human-readable; `None`/unknown shown as `—`.
- Footer keys: `a` add family · `d` delete family · `Enter` open family · `q` quit.
- **Add family** modal: `family` name (required, becomes id + filename) and optional `display_name`. Creates an empty manifest (no variants).
- **Delete family**: only allowed when the family has **no downloaded models**; otherwise refuse and notify (no cascade delete).

**ModelScreen** (a selected family)
- Two panes. **Left**: provider list, each row showing the provider name and per-provider model count. **Right**: `DataTable` of the selected provider's models, columns: **name · status · size · path**.
  - `path` comes from `downloaded[vid].local_path` (shown for downloaded models only).
  - `status` is a four-state glyph reflecting disk reality and pending queue:
    - `✓` green — downloaded (on disk)
    - `○` dim — not downloaded
    - `↓` yellow — queued for download on exit
    - `✗` red — queued for delete on exit
- **Bottom**: a live "Pending" bar summarizing queued downloads/deletes.
- Footer keys: `a` add model · `d` delete model · `e` edit model · `x` toggle download · `Esc` back (triggers apply flow).
- **Queue a download** for an existing not-downloaded model: select it and press `x` to toggle "download on exit". Pressing `x` on a downloaded model is a no-op. This is what makes `modelman download <family>` useful: it opens here and you toggle the models you want pulled.
- **Queue a delete** for a downloaded model: select it and press `d`. Pressing `d` on a not-downloaded model is a no-op (there's nothing to delete).

### Add / edit / delete models

- **Add model** modal form:
  - Pick `provider` from configured providers.
  - Enter a stable `id`.
  - Provider-specific fields:
    - `ollama`: `name` (e.g. `ornith-1.5:35b`)
    - `llamacpp`: `repo` + `files`
    - `omlx`: `repo` + `quantizations`
  - Optional `model_info` key/value editor.
  - **Ollama auto-detect:** on add, run `ollama show <name>` and prefill known `model_info`
    capabilities (e.g. a `tools` capability → `supports_function_calling: true`). HF providers
    get no auto-detection; fields are left for the user.
- **Edit model** modal: same form, prefilled. `id` and `provider` are **immutable**; all other
  fields, including `model_info`, are editable.
- **Delete model**: queues removal of the variant **and** its local files.

### Queued operations on exit

`PendingChanges` holds all edits in memory while working:
- `downloads: dict[variant_id, VariantSpec]` (newly added variants plus any existing not-downloaded
  variant toggled with `x`)
- `deletes: dict[variant_id, VariantSpec]`
- metadata edits applied to the in-memory manifest.

Leaving the model screen shows a **confirm dialog** listing the pending downloads and deletes.
On confirm, switch to a **StatusScreen** that runs the apply pipeline on a worker thread and
streams live progress into a `RichLog`. Apply in this order:
1. **Deletes** — remove local files via the stored `downloaded[vid].local_path`, then remove the variant and its `downloaded` entry. (Run first to free disk space before downloads.)
2. **Downloads** — run `provider.download()` for each pending variant, then `mark_downloaded`. Providers stream progress via an `on_progress` callback: Ollama's subprocess stdout is stripped of ANSI escapes and forwarded line-by-line; huggingface_hub snapshot_download is wired to a `ProgressTqdm` that emits per-file byte/rate lines. Each line is appended to the log as it arrives.
3. **`save_manifest()`** once.

The StatusScreen has no destructive bindings while the worker is running. Pressing
`Escape` mid-run opens a **CancelApplyDialog**: `Cancel` (sets the cancellation flag on
`PendingChanges`, kills any active subprocess via `Provider.cancel_current()`,
skips remaining steps, does not save the manifest, already-completed steps remain
applied) or `Wait` (close the dialog, keep waiting). For llamacpp/oMLX, the
in-flight Python-level download is not safely interruptible; the worker thread
is abandoned and finishes in the background, but no more variants are started.
Once the worker reports `apply:done` (or `apply:cancelled`), `Escape` pops back to
FamilyScreen and the footer caption switches to "Back".

Per-item failures are collected and reported after the run; the manifest reflects only what
actually succeeded. Nothing is written to disk before the confirm. A Ctrl-C mid-session discards
the queue.

### Data model changes

- `VariantSpec` gains optional `model_info: dict | None` (freeform LiteLLM keys). `_coerce_variant`
  and `save_manifest` pass it through; not a breaking change.
- Add `size_of(variant) -> int | None` to `Provider` (default `None`) to feed the size columns:
  - `ollama`: parse the SIZE column of `ollama list`.
  - `llamacpp`: stat the primary file path from `downloaded[vid].local_path`.
  - `omlx`: sum file sizes in the local model dir.

## Error handling

- Config/manifest errors surface as an in-app notification/modal, not an unhandled crash.
- Download/delete failures are collected and summarized after the apply run; the manifest only
  reflects successful operations.

## Testing

- Unit tests for `PendingChanges.apply()` (deletes before downloads) using mocked providers.
- Manifest round-trip tests for `model_info` (preserved through load/save).
- TUI tests using Textual `App.run_test()` (Pilot) for navigation (family → model screen),
  add/edit/delete flows, and the exit confirm + queue application.
- Reuse the existing `mock_runner` fixture pattern for provider subprocess calls.
- `size_of` unit tests per provider.
