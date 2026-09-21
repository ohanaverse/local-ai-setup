# wt model subcommands: `start`, `stop`, `smoke`

Date: 2026-09-21 · Branch: `wt-model-subcommands`

## Goal

Give `wt` three subcommands for working with local models directly, without launching an agent:

- `wt start [model]`
- `wt stop [model|provider]`
- `wt smoke [model]` (exists today; reworked)

Two selection screens back them:

1. **Full model selection** (screen 1): the table used by `wt`'s model step, `wt start` and `wt smoke`. Lists every configured and detected local model, running or not.
2. **Local model-stopping selection** (screen 2): the stop picker shown when a `wt` session ends, and now by `wt stop` and after `wt smoke`.

## Decisions (from brainstorming)

- `wt smoke` starts a non-running local model before smoking it (reverses today's "smoke never starts models" rule).
- `wt stop` lists in-use models (marked with session count) and confirms before stopping one; the exit flows (`wt`, `wt smoke`) keep hiding in-use models.
- Screen 1 reuses `tui.PickModel`; screen 2 keeps the existing line-typed `survey` picker. No new Bubble Tea screen.
- Arguments: model ids are registry ids `<provider>/<name>` (same scheme as `-M`, discovered models included). `wt stop` also accepts a bare provider id.

## Behavior

### `wt start [model]`

- **With model:** look up the row with `catalog.Find`.
  - Running: print `<id> is already running`, exit 0.
  - Start row: run `startForLaunch` (stderr progress, Ctrl+C cancel, replace confirmation). `--replace` skips the confirmation.
  - Blocked row: error with the row's `BlockReason`.
  - Cloud model or unknown id: error (`unknown model` / `not a local model`).
- **No model:** open screen 1 over all local rows. Running row selected: no-op. Start row selected: start it. Blocked rows are unselectable. Cancel exits non-zero with `model selection canceled`. Without a TTY: error naming the direct form.

### `wt stop [model|provider]`

- **With argument:** a registry id → `lifecycle.StopModel`; a provider id in {ollama, omlx, omlx-6bit, mtplx} (those with `lifecycle.CanStop`) → `lifecycle.Stop`. Anything else: error listing valid providers. A model that is not running: error. For provider stops, every running model of the provider is affected, so the in-use check covers all of them.
- **In-use guard:** if any target has live sessions (`refcount`), print the count and ask y/N; `--yes` skips. Non-TTY without `--yes` on an in-use target: error.
- **No argument:** open screen 2 with `IncludeInUse` (in-use rows marked `N sessions`). Nothing running: print `no running local models`, exit 0. Without a TTY: error.
- Ctrl+C handling matches the existing stop picker (`stopSignalCtx`).

### `wt smoke [model]`

- Resolve the model over all local rows plus cloud rows (screen 1 if omitted; needs a TTY).
- Start row: start it first (same driver as `wt start`, `--replace` supported). Blocked row: error with reason.
- Run the existing per-agent smoke loop and reports unchanged (`--only`, `--prompt`, `--timeout`, `--json`).
- On exit, always (pass, fail, or cancelled): release the session refcount, then run screen 2 with `IncludeInUse=false`. Skipped for `--json` and non-TTY stdin. The stop flow runs before the process exit code is applied so a smoke FAIL still exits 1.

### Help

`wt --help` lists `start`, `stop`, `smoke` (plus `config`, `stats`; `rotate` stays hidden) and the root `Example` block gains one example per new subcommand. Each subcommand has `Short`, `Long`, and flags documented.

## Structure

- `cmd/wt/model_cmds.go` (new): `startCmd`, `stopCmd`, argument resolution helpers. Registered next to `smokeCmd` in `main.go`.
- `cmd/wt/smoke.go`: `resolveSmokeModel` switched to all-rows resolution + start step + exit stop flow.
- `internal/smoke.Eligibility`: gains a variant that keeps start rows (the "never starts" contract moves to `cmd/wt`; the function itself still starts nothing).
- `internal/tui.PickModel`: builds rows from `catalog.Build` over all local models so screen 1 is identical across `wt`, `wt start`, `wt smoke`.
- `internal/survey/stop.go`: `Picker` takes `Options{IncludeInUse bool}`; `stoppable()` stays the single candidate source and reports in-use counts; in-use rows are rendered with the count.
- Docs: `wt/CLAUDE.md`, `docs/wt-smoke.md`, new short `docs/wt-start-stop.md`, root `docs/guides/` model guide cross-reference.

## Error handling

All argument errors return before any side effect, exit non-zero via cobra, and name the offending value plus the valid forms. Start failures use `lifecycle.StartErrorMessage`. Stop failures are reported per model; the command exits non-zero if any requested stop failed.

## Testing

Follow the repo seams (`startModel`, `pickModelTUI`, `stopSignalCtx`, `probeInventory`, `stdinTTY`, `smokeExit`); each `Test*` carries a what/why comment.

- start: running is a no-op; blocked errors with reason; unknown/cloud id errors; picker no-op on a running row.
- stop: model id, provider id, invalid arg error, not-running error, in-use confirm/decline/`--yes`, picker includes in-use rows.
- smoke: non-running model is started before smoking; blocked errors; stop flow runs after PASS and after FAIL and does not change exit code; skipped for `--json`.
- help: root `--help` output contains all subcommands.
- survey: `Options{IncludeInUse}` variants of `stoppable`.

## Out of scope

Rebuilding screen 2 as a Bubble Tea table; changing `modelman start/stop`; bare-name (no provider prefix) matching; starting cloud models.
