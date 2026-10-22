# Modelman LiteLLM Proxy Reconcile — Design

**Status:** Proposed (2026-08-31). Not yet planned or implemented.

**Scope:** modelman side only. Closes the gap where `expose`/`unexpose` write
`config.yaml` but the running LiteLLM proxy keeps serving a stale model list
until it is restarted.

## Problem

`modelman expose`/`unexpose` (and the TUI's `l` key) write/remove `model_list`
entries in `~/.config/litellm/config.yaml` and flip the `litellm_exposed` flag
in `modelman.toml`. But the running LiteLLM proxy loads `config.yaml` **only at
startup**. After an expose change, the proxy keeps serving the old model list
until it is restarted, so a newly exposed model returns:

```
400 Invalid model name passed in model=ollama/glm-5.3-flash:cloud.
Call `/v1/models` to view available models for your key.
```

This affects every consumer that routes through the proxy (claude, codex,
copilot, opencode, pi), not just one launcher. The current design
(`2026-08-28-modelman-litellm-exposure-design.md`) never reconciles the running
proxy, so the drift is silent until a launch fails.

## Goal

After a successful `config.yaml` write that changes the `model_list`, modelman
reconciles the running proxy so it serves the new model list. The reconcile is
**best-effort and non-fatal**: the config write is the source of truth, and a
failed reconcile must not roll back or error out the expose operation.

## Decisions

1. **modelman owns proxy reconciliation.** modelman is the component that
   mutates `config.yaml` and the only one that knows when it changed, so it is
   the natural owner of "make the running proxy reflect the new config." `wt`
   (a launcher) does not detect or restart the proxy; it is out of scope here.

2. **Reconcile via a configurable restart command, with a canonical fallback.**
   modelman is a general tool; `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`
   is this machine's answer. The restart command is read from the
   `MODELMAN_LITELLM_RESTART_CMD` env var (matching the existing
   `MODELMAN_LITELLM_CONFIG` pattern); when unset, reconcile falls back to that
   canonical kickstart rather than silently skipping. On platforms where the
   fallback command fails or is absent, the failure degrades to the same
   non-fatal warning, so other environments are not broken.

   *Follow-up (2026-09-02):* the env var was exported only from interactive
   shells (`~/.zshrc`), so non-interactive launches (scripts, agents, worktree
   launchers) never inherited it — exposes from that context wrote
   `config.yaml` but never restarted the proxy, leaving clients 400ing on the
   new rows until a manual kickstart. The fallback closes that gap; the env
   var remains honored when set (overrides the fallback).

3. **Prefer a non-disruptive reload when available.** LiteLLM 1.98.0 (the
   version on this machine) exposes no HTTP config-reload endpoint (`/reload`
   returns 404; `--reload` is uvicorn dev-only), so a process restart is the
   reliable mechanism today. The design keeps the reconcile behind a single
   function so a future non-disruptive reload (e.g. a `/reload` endpoint or a
   `SIGHUP` handler) can replace the restart without touching callers.

4. **Reconcile once per apply cycle, only when something changed.**
   `apply_expose_queue` already batches the whole queue into one config write;
   reconcile runs once after that write, and only if at least one queue item
   was applied (i.e. validated and written). A queue item that fails validation
   is not "applied"; an idempotent expose of an already-exposed model is still
   "applied" and will bounce the proxy. The empty queue short-circuits before
   the save and therefore never restarts.

5. **Reconcile failure is a warning, not an error.** The config write already
   succeeded; a failed restart just means the proxy is still stale. Surface a
   clear warning so the user can restart manually, but do not fail the expose
   operation or roll back the write.

## Architecture

**`src/modelman/litellm.py`** (already the single owner of LiteLLM knowledge)
gains:

- `default_litellm_restart_cmd() -> str | None` — reads
  `MODELMAN_LITELLM_RESTART_CMD` (lazily, so env overrides work in tests).
- `restart_litellm_proxy() -> list[str]` — runs the configured command via
  `subprocess` (bounded by a 30-second timeout); when unset, falls back to the
  canonical `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`. Returns a
  list of warning strings (empty on success) rather than printing to stderr,
  so callers can surface them on the UI thread — a direct stderr write from a
  TUI worker thread would interleave with and garble Textual's rendering.
  Never raises.

**Call sites** (all in `litellm.py`):

- `expose_model` / `unexpose_model` — after `save_litellm_config` + flag flip,
  call `restart_litellm_proxy()` **only when the flag actually changed**
  (`_set_exposed_flag` returns whether it did). A no-op unexpose of a model
  already removed from the registry does not bounce the shared proxy. Both
  return the warning list so the CLI can surface it.
- `apply_expose_queue` — after the batch save succeeds, call
  `restart_litellm_proxy()` **once** if `succeeded` is non-empty. Returns
  `(outcomes, warnings)`; `PendingChanges.apply()` emits each warning through
  the event channel so the TUI renders it on the UI thread.

## Data Flow

**CLI (`modelman expose <id>`):**
1. Validate + build entry (unchanged).
2. Read `config.yaml`, add/remove the row, atomic-write (unchanged).
3. Flip `litellm_exposed` (unchanged).
4. `restart_litellm_proxy()` — best-effort; returns warnings, which the CLI
   prints to stderr.

**TUI apply (`PendingChanges.apply()`):**
1. `apply_expose_queue(...)` writes the whole queue in one atomic save.
2. If any entry applied, `restart_litellm_proxy()` runs once.
3. Restart warnings are emitted as `expose:warning|…` events and rendered in
   the StatusScreen log.
4. `save:start` / `save:done` (unchanged).

## Configuration

- `MODELMAN_LITELLM_RESTART_CMD` — shell command to restart the proxy.
  Defaults to the canonical `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`
  when unset (the pre-2026-09-02 behavior of no-op-with-warning silently
  skipped restarts in non-interactive launch contexts).

## Error Handling

- **Restart command unset** — fall back to the canonical launchctl kickstart.
- **Restart command fails** (non-zero exit, command not found, or a 30-second
  timeout) — warn, do not raise. The expose operation already succeeded.
- **Config write fails** — unchanged: the expose operation errors out before
  any reconcile is attempted.

## Testing

- **`tests/test_litellm.py`** — `default_litellm_restart_cmd()` reads the env
  var (set/unset); `restart_litellm_proxy()` runs the command (monkeypatched
  `subprocess`) and falls back to the canonical kickstart when unset; failure
  is non-fatal.
- **`tests/test_expose.py`** — `expose_model`/`unexpose_model` call
  `restart_litellm_proxy()` after a successful write (monkeypatched to record
  the call); a failing restart does not raise.
- **`tests/test_queue.py`** — `apply_expose_queue` calls `restart_litellm_proxy()`
  once when entries applied, and not at all when the queue is empty / all no-op.
- **`tests/commands/test_expose.py`** — CLI wiring: restart runs after a
  successful expose; a failing restart still exits 0 with a warning.

## Explicitly Out of Scope

- `wt`-side staleness detection or proxy restart (a launcher should not bounce
  a shared service). `wt` docs are updated to note that modelman owns
  reconciliation.
- Any change to LiteLLM's `general_settings` or the proxy's own config reload
  mechanism.
- A non-disruptive reload endpoint (future work if LiteLLM exposes one).
