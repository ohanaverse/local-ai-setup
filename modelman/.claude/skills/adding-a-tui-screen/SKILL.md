---
name: adding-a-tui-screen
description: Steps to add a new Textual TUI screen to modelman. Use when asked to add a new screen to modelman's TUI.
---

## Adding a new TUI screen

1. Create `src/modelman/screens/<name>.py` extending `Screen[None]`
2. Add `action_back()` binding (escape key) with queue-check if applicable
3. Register in `app.py` if pushed from multiple screens, or push directly from caller
4. Follow the `reload_preserving_cursor` pattern if using DataTable with background refresh
5. A `@work(thread=True)` worker may call `self.app` directly while the screen is mounted — no captured app reference is needed, and `self._app_ref` (deleted in #85) must not come back. Only a worker that *outlives* its screen is a problem: `self.app` raises `NoActiveAppError` once the widget is unmounted, so have the worker carry the paths/ids it needs instead of reaching back through the widget
