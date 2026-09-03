"""StatusScreen — live progress of the apply-queue run."""

from __future__ import annotations

from collections.abc import Callable
from typing import TYPE_CHECKING

from textual.app import ComposeResult
from textual.screen import Screen
from textual.widgets import Footer, Header, RichLog
from textual.worker import Worker

if TYPE_CHECKING:
    from ..queue import PendingChanges


# The apply runner closure receives:
#   - on_event:    sink to forward lifecycle tags into the log
#   - on_progress: sink to forward per-line progress from providers
#   - register:    callback the runner uses to expose the PendingChanges it
#                  creates, so the screen can call .cancel() on it
RunApply = Callable[
    [Callable[[str], None], Callable[[str], None], Callable[["PendingChanges"], None]],
    None,
]


class StatusScreen(Screen[None]):
    """Show apply-queue progress as a RichLog of events.

    The ModelScreen passes a `run_apply` closure that constructs the
    PendingChanges and invokes `apply(on_event=...)`. StatusScreen runs
    it on a worker thread and translates each event tag into a log line.

    While the worker is running, Escape opens a CancelApplyDialog that
    either cancels the apply (kills the running subprocess if possible,
    skips remaining steps, no save) or keeps waiting.

    Once the worker signals `apply:done` or `apply:cancelled`, the
    footer binding switches to "Back" and the user can pop to
    FamilyScreen.
    """

    BINDINGS = [
        ("escape", "back", "Back"),
    ]

    def __init__(self, family: str, run_apply: RunApply) -> None:
        super().__init__()
        self.family = family
        self._run_apply = run_apply
        self.done = False
        self.cancelled = False
        self.pending: PendingChanges | None = None
        self._worker: Worker[None] | None = None
        self._failure_count = 0
        # Presentation-only copy of pending.failures, kept in the order
        # the failure events arrive. Distinct from PendingChanges.failures
        # (queue.py) so the screen can show labels and reasons in the
        # rendered format without leaking exception objects into the
        # presentation layer. Always appended in lockstep with
        # `_failure_count` so the summary count and list never desync.
        self._displayed_failures: list[str] = []

    def compose(self) -> ComposeResult:
        yield Header()
        yield RichLog(id="status-log", wrap=False, highlight=False, markup=False)
        yield Footer()

    def on_mount(self) -> None:
        self.title = f"Applying changes \u2014 {self.family}"
        self._worker = self.run_worker(self._run, exclusive=True, thread=True)

    def _run(self) -> None:
        """Worker entrypoint: drive apply() and forward events to the log.

        Runs on a background thread; calls back into the UI via
        `app.call_from_thread` to update the RichLog.
        """
        self._emit(f"Applying changes for family '{self.family}'\n")
        try:
            self._run_apply(self._emit_threaded, self._emit_threaded, self._register_pending)
        except Exception as exc:  # noqa: BLE001
            # apply() normally captures per-step failures and emits its own
            # failure events, but unexpected errors in the runner closure
            # (e.g. a provider that fails to instantiate with a real error)
            # must not crash the worker silently. Emit a synthetic apply:done
            # so the standard handler in _handle_event runs — it writes the
            # failure summary block and flips `done` itself, in the same
            # order as the happy path. (Earlier this path set `done` directly
            # without emitting apply:done, which made the user-visible
            # "Done with errors" summary never render and let Escape pop the
            # screen before traceback lines were processed.)
            import traceback
            self._emit("[red]Unexpected error during apply:[/red]")
            self._emit(f"[red]  {exc.__class__.__name__}: {exc}[/red]")
            # Include traceback for debugging
            tb_lines = traceback.format_exc().splitlines()
            for line in tb_lines[-10:]:  # Last 10 lines to avoid flooding
                self._emit(f"[dim]{line}[/dim]")
            self._emit_threaded("apply:done")
            return
        # The closure calls _emit_threaded, which marshals to _emit on the UI
        # thread. After it returns, the apply:done (or apply:cancelled)
        # event has been processed and `done` is True.

    def _register_pending(self, pending: PendingChanges) -> None:
        """Closure hook: store the PendingChanges so we can cancel it.

        Called from the worker thread; the PendingChanges itself is
        thread-safe to mutate (.cancel() sets a flag).
        """
        self.pending = pending

    def _emit_threaded(self, tag: str) -> None:
        """Event sink used by the worker thread.

        Schedules the UI update on the main thread, then sets the `done`
        flag when the apply finishes.
        """
        self.app.call_from_thread(self._handle_event, tag)

    def _handle_event(self, tag: str) -> None:
        """Translate an event tag into a log line; flip `done` at the end."""
        log = self.query_one("#status-log", RichLog)
        # Progress lines from providers arrive without the verb:status
        # prefix. They are written as indented, dim lines so the lifecycle
        # markers remain the visual spine of the log.
        if (
            not tag.startswith("delete:")
            and not tag.startswith("download:")
            and not tag.startswith("ready:")
            and not tag.startswith("move:")
            and not tag.startswith("save:")
            and not tag.startswith("apply:")
        ):
            log.write(f"    [dim]{tag}[/dim]")
            return
        # Per-item tags are pipe-delimited:
        #   "verb:status|vid|label"        normal lifecycle,
        #   "verb:status|vid|label|detail" failure reason, download size,
        #                                   or move target family — meaning
        #                                   depends on verb, see below,
        # Global tags:
        #   "verb:status"                  normal lifecycle,
        #   "save:fail|reason"             failure with reason.
        parts = tag.split("|", 3)
        verb = parts[0]
        if verb in ("delete:fail", "download:fail") and len(parts) == 4:
            # 4th field: failure reason.
            label = parts[2]
            detail = parts[3]
        elif verb == "save:fail" and len(parts) == 2:
            label = ""
            detail = parts[1]
        elif verb == "download:done" and len(parts) == 4:
            # 4th field: human-readable size (e.g. "21.7 GB"), when known.
            # Surfaced in the success marker so the user can verify the
            # download landed at the expected size (0 B messages look
            # suspicious; concrete sizes don't).
            label = parts[2]
            detail = parts[3]
        elif verb in ("move:start", "move:done") and len(parts) == 4:
            # 4th field: target family.
            label = parts[2]
            detail = parts[3]
        elif ((verb == "move:fail") or verb in ("expose:fail", "unexpose:fail")) and len(parts) == 4:
            # 4th field: failure reason.
            label = parts[2]
            detail = parts[3]
        elif verb == "expose:warning" and len(parts) == 2:
            # 2nd field: non-fatal proxy-restart notice.
            label = ""
            detail = parts[1]
        else:
            # Normal lifecycle events.
            label = parts[2] if len(parts) >= 3 else ""
            detail = ""
        if verb == "delete:start":
            log.write(f"· Deleting {label}...")
        elif verb == "delete:done":
            log.write(f"  [green]✓[/green] Deleted {label}")
        elif verb == "delete:fail":
            log.write(f"  [red]✗[/red] Failed to delete {label}")
            if detail:
                # Defense-in-depth cap: even if _reason didn't truncate,
                # a malformed/long detail line should not overflow the
                # wrap=False RichLog.
                if len(detail) > 200:
                    detail = detail[:197] + "…"
                log.write(f"    [red]{detail}[/red]")
            self._displayed_failures.append(
                f"Delete failed for {label}: {detail or '(no reason provided)'}"
            )
            self._failure_count += 1
        elif verb == "download:start":
            log.write(f"· Downloading {label}...")
        elif verb == "download:done":
            # detail (if present) is the file's actual on-disk size,
            # e.g. "21.7 GB". Including it here so the user has
            # concrete confirmation the download landed at the
            # expected size, not zero bytes.
            suffix = f" ({detail})" if detail else ""
            log.write(f"  [green]✓[/green] Downloaded {label}{suffix}")
        elif verb == "download:fail":
            log.write(f"  [red]✗[/red] Failed to download {label}")
            if detail:
                if len(detail) > 200:
                    detail = detail[:197] + "…"
                log.write(f"    [red]{detail}[/red]")
            self._displayed_failures.append(
                f"Download failed for {label}: {detail or '(no reason provided)'}"
            )
            self._failure_count += 1
        elif verb == "download:cancelled":
            log.write(f"  [yellow]![/yellow] Cancelled {label}")
        elif verb == "ready:start":
            log.write(f"· Marking {label} ready...")
        elif verb == "ready:done":
            log.write(f"  [green]✓[/green] Ready: {label}")
        elif verb == "move:start":
            log.write(f"· Moving {label} → {detail}...")
        elif verb == "move:done":
            log.write(f"  [green]✓[/green] Moved {label} → {detail}")
        elif verb in ("expose:fail", "unexpose:fail"):
            action = verb.split(":")[0]
            log.write(f"  [red]✗[/red] Failed to {action} {label}")
            if detail:
                if len(detail) > 200:
                    detail = detail[:197] + "…"
                log.write(f"    [red]{detail}[/red]")
            self._displayed_failures.append(
                f"{action.title()} failed for {label}: {detail or '(no reason provided)'}"
            )
            self._failure_count += 1
        elif verb == "move:fail":
            log.write(f"  [red]✗[/red] Failed to move {label}")
            if detail:
                if len(detail) > 200:
                    detail = detail[:197] + "…"
                log.write(f"    [red]{detail}[/red]")
            self._displayed_failures.append(
                f"Move failed for {label}: {detail or '(no reason provided)'}"
            )
            self._failure_count += 1
        elif verb == "expose:warning":
            # Non-fatal proxy-restart notice (command unset or failed).
            log.write(f"  [yellow]![/yellow] {detail}")
        elif verb == "save:start":
            log.write("· Saving manifest...")
        elif verb == "save:done":
            log.write("  [green]✓[/green] Saved manifest")
        elif verb == "save:fail":
            log.write("  [red]✗[/red] Failed to save manifest")
            if detail:
                if len(detail) > 200:
                    detail = detail[:197] + "…"
                log.write(f"    [red]{detail}[/red]")
            self._displayed_failures.append(
                f"Save failed: {detail or '(no reason provided)'}"
            )
            self._failure_count += 1
        elif verb == "apply:cancelled":
            self.cancelled = True
            self.done = True
            self._refresh_bindings()
            log.write("\n[bold yellow]Cancelled.[/bold yellow] Press Escape to return.")
        elif verb == "apply:done":
            self.done = True
            self._refresh_bindings()
            # Show failure summary if any failures occurred
            if self._failure_count > 0:
                log.write("\n" + "=" * 60)
                log.write(f"[bold red]{self._failure_count} operation(s) failed:[/bold red]")
                for i, failure in enumerate(self._displayed_failures, 1):
                    log.write(f"[red]  {i}. {failure}[/red]")
                log.write("\n[dim]Review the errors above. Fix the issue and retry.[/dim]")
                log.write("\n[bold]Done with errors.[/bold] Press Escape to return.")
            else:
                log.write("\n[bold green]All operations completed successfully.[/bold green] Press Escape to return.")

    def _emit(self, line: str) -> None:
        """Helper for the worker to log a plain line (used before the loop)."""
        self.app.call_from_thread(self.query_one("#status-log", RichLog).write, line)

    def _refresh_bindings(self) -> None:
        # Toggle the footer's binding caption by rebuilding it; the binding
        # itself stays the same (Escape pops).
        self.refresh_bindings()

    def action_back(self) -> None:
        # Only allow going back once the apply has finished or been cancelled.
        if self.done:
            self.app.pop_screen()
            return
        # Still running: ask whether to cancel or wait.
        from .forms import CancelApplyDialog

        self.app.push_screen(CancelApplyDialog(), self._on_cancel_choice)

    def _on_cancel_choice(self, choice: str | None) -> None:
        if choice == "cancel" and self.pending is not None:
            # Immediate user-visible feedback: the worker may still be busy
            # in proc.wait() for a few hundred ms, and the user should see
            # something happened right away.
            log = self.query_one("#status-log", RichLog)
            log.write("\n[bold yellow]Cancelling…[/bold yellow]")
            self.pending.cancel()
            return
        # "wait" or dismissed: stay on the status screen.
        return
