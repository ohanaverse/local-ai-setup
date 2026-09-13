# modelman downloads-on-exit — design

Follows PR #84 (single model-tui screen). Simplifies the download/apply path now that ModelScreen is the app's only screen.

## Summary

Replace modelman's background concurrent download machinery with a queue-and-apply-on-exit model. Downloads become ordinary queued operations; on exit-Apply the TUI shuts down and the queued operations (downloads + deletes + moves + exposes) run afterwards in the terminal, printing provider progress directly to stdout. This deletes the entire background-download + in-TUI-apply subsystem: `DownloadManager`, `DownloadScreen`, `StatusScreen`, the quit guard, and the deferred-expose indirection.

## Behavior

### Exit flow

All operations — including ready-on (downloads) against downloadable providers — are queued in `ModelScreen`. Nothing downloads during the session; there is no `DownloadScreen`, no progress poll, and no quit guard because nothing runs in the background.

Escape/ctrl+q with a pending queue opens the existing ConfirmExitDialog (its summary now also lists queued downloads as "ready" rows):

- **Apply** — carry the queue out; the TUI exits; queued operations run post-exit in the terminal (sequential downloads + deletes + moves + exposes), provider progress forwarded verbatim to stdout, one thin lifecycle line per op, error summary at the end.
- **Discard** — restore in-memory snapshot, undo any immediately-persisted add/edit (re-save registry), drop the queue, exit without running.
- **Cancel** — stay in the TUI, keep the queue (leads to a later Escape → Apply).

Escape/ctrl+q with an empty queue exits immediately (no dialog). Apply and Discard both now *exit the app* — unlike today, where they stay in the TUI.

### Command line

`modelman download <family>` is removed. The `family` scroll-to-startup argument (`download`'s only caller) is removed as dead code.

## Architecture

### Deleted

- `modelman/downloads.py` (`DownloadManager`) and its daemon threads/locks
- `modelman/screens/downloads.py` (`DownloadScreen`, `g` binding)
- `modelman/screens/status.py` (`StatusScreen`) and the StatusScreen handoff in `forms.py`
- `QuitBlockedModal` (quit guard) and the download-active check in `app.request_quit()`; `request_quit()` delegates to the top ModelScreen's `action_back()` (which gives the dialog)
- `_poll_downloads` timer, `_projected_ready`'s downloading branch, `_ready_cascade_for_expose` cancel-on-discard cascade, `_register_deferred_expose` + `register_post_download`
- `main.py` `download` command

### Changed

- `queue.py apply()` — ready-on downloads run inline via `provider.download(variant, on_progress=...)` (deletes → moves → downloads → exposes → save). The old `assert ... must go through DownloadManager` is **reversed**: apply() owns ready-on downloads again, sequentially. Failures go into `self.failures` and the run continues (existing behavior). The `manages_own_cache` carve-out stays (MTPLX is a genuine flag-only case).
- `ModelmanApp` — exits with the queue as the app result (`self.exit(QueuedOps)` on Apply, `self.exit(None)` on Discard/empty). `App.run()` returns it.
- `main.py run_tui()` — runs the post-exit runner when the app returns a non-None queue: builds a fresh `PendingChanges` from disk (`load_registry`/`load_state`) + the returned queue, calls `apply(on_event=print_event, on_progress=print)`, prints the error summary, exits non-zero on failures.
- `ConfirmExitDialog` — unchanged structurally; its pending list just includes queued downloads.
- Kept: `PendingChanges` structure, add/edit immediate-persist + discard-undo, provider `download()`/progress signature.

## Data flow

1. **Queue payload.** On exit, `ModelScreen` packages pending state into a plain dataclass:
   ```
   QueuedOps(ready: {id: target}, deletes: {id: VariantSpec},
             moves: {id: new_family}, exposes: {id: target})
   ```
   No registry/state objects cross the TUI/terminal boundary — just data.
2. **Exit.** Apply → `self.exit(QueuedOps)`; Discard/empty → `self.exit(None)`. `ModelmanApp.run()` returns the value.
3. **Post-exit runner** in `run_tui()`:
   ```python
   queued = ModelmanApp(family).run()
   if queued is None:
       return                       # Discard / empty
   registry = load_registry(); state = load_state()
   providers = {...}                # Provider classes for providers touched by ready/deletes
   pending = PendingChanges(registry, state, ..., ready=…, deletes=…, moves=…, exposes=…,
                            litellm_path=default_litellm_config_path())
   pending.apply(on_event=print_event, on_progress=print)   # print_event: thin formatter for lifecycle tags
   print_error_summary(pending.failures)   # exit non-zero if any
   ```
   Mirrors today's `_run_apply` construction against fresh disk state. Safe because adds/edits already persist immediately and queued (unapplied) moves/deletes/ready live only in the queue.
4. **Ordering.** Downloads run inside apply before exposes, so a queued expose against a just-downloaded model "just works" (the ready gate passes a step earlier). The deferred-expose indirection is deleted.

## Error handling, cancellation, summary

- **Continuation on failure.** `apply()` collects per-step failures into `self.failures` and continues to the next step (unchanged). A failed download aborts only its own step.
- **Error summary.** After apply, if `self.failures`, `main.py` prints:
  ```
  Completed with errors:
    - download <model>: connection refused
    - expose <model>: config not writable
  2 of 5 operations failed.
  ```
  and exits non-zero. Clean runs print nothing extra.
- **Cancellation (Ctrl+C).** The runner wraps `apply(...)` in `try/except KeyboardInterrupt`: on interrupt, call `pending.cancel()` (sets the existing `cancelled` flag → `aborted()` gates between every step; the current step finishes/terminates, remaining never start, in-memory state is not saved), print `Cancelled: N steps completed, M remaining skipped.`, exit non-zero. Reuses the `cancelled`/`aborted()` machinery; provider subprocesses get killed by the interrupt signal delivery as today.
- **No per-model cancel prompt.** Post-TUI there is no UI to cancel one queued item; Ctrl+C is all-or-nothing between steps. Acceptable: downloads are sequential and Cancel on the dialog already stops short.

## Testing

Unit (pytest, mocked providers/state/LiteLLM per conftest):
- `queue.py`: ready-on against a mapped provider downloads — new test that `apply()` calls `provider.download(variant, on_progress=...)`; sequential order (download A completes before B starts); per-line progress forwarded to `on_progress`; a download failure records into `self.failures` and does not stop deletes/moves/exposes.
- cancellation: setting `cancelled` mid-queue skips remaining steps after the current one; no save.

CLI integration (post-exit runner directly):
- given `QueuedOps`, builds `PendingChanges` from disk and applies; `None` leaves registry/state unchanged.
- error-summary output and non-zero exit on failures; `KeyboardInterrupt` → cancelled path, remaining skipped, non-zero exit.

Screen tests (updated):
- ready-on against a downloadable provider appears in the confirm dialog's ready list and pushes the app to exit-with-queue (not into a StatusScreen).
- Discard restores snapshot + re-saves registry + exits with `None`.
- Escape with empty vs. pending queue: empty exits; pending opens dialog.
- Remove tests for deleted components: download-screen tests, `StatusScreen`, `QuitBlockedModal`, download-quit-guard, deferred-expose. Adjust `test_app_navigation`/`test_reconcile_model_state` families rather than deleting blindly.

Manual:
- two-model sequential download with one artificially failing provider shows both progress bars and the error summary.
- Ctrl+C midway shows the cancelled message and skips the rest.

## Sequencing / risk

This touches the shared `queue.py` apply path and the navigation tests in parallel. Sequence edits: `queue.py` first, then `app.py`/`main.py`, then screens, then test rewrites. Lean on `make lint` + `modelman make check/test` to catch regressions. The one risk is broad test churn from deleting the concurrent-download subsystem; mitigate by keeping the queue payload and apply signature additive-compatible.