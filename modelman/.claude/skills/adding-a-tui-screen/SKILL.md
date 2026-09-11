---
name: adding-a-tui-screen
description: Steps to add a new Textual TUI screen to modelman. Use when asked to add a new screen to modelman's TUI.
---

## Adding a new TUI screen

1. Create `src/modelman/screens/<name>.py` extending `Screen[None]`
2. Add `action_back()` binding (escape key) with queue-check if applicable
3. Register in `app.py` if pushed from multiple screens, or push directly from caller
4. Follow the `reload_preserving_cursor` pattern if using DataTable with background refresh
5. If the screen spawns workers that outlive the screen, capture `self._app_ref = self.app` in `on_mount()` for thread-safe access
