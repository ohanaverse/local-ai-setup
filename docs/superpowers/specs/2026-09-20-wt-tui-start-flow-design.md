# wt: TUI start-on-select flow (sub-project 4b)

Date: 2026-09-20
Status: approved, pending implementation plan

## Context

Sub-project 4b of the "make wt's model selector look like modelman's" effort.
Merged so far: #110 per-agent stats, #111 `localmodels.Inventory`, #112 selector
table, #113 `internal/lifecycle` (4a: `Start`/`Occupant`/`Stop`, typed errors,
progress stages).

| # | Sub-project | Depends on |
|---|---|---|
| 1-3 | Stats, inventory, selector table (done) | |
| 4a | Lifecycle engine (done, #113) | 2 |
| 4b | TUI start-on-select flow (this spec) | 4a |
| 4c | Non-TUI launch, `-M` pin check and `wt smoke` move from `localgate` to live Inventory, with start-on-select and a warn/confirm policy without a TUI; retires `localgate`'s flag dependence | 4a |
| 5 | Launch-time smoke gate (exit non-zero on failure) | 4b, 4c |

State scope (decided earlier): live truth everywhere; wt writes nothing to
modelman-owned state and never restarts the LiteLLM proxy.

## Problem

Selecting a non-running local row in the picker only shows a status hint ("start
it with modelman start"). Wt now has an engine that can start models
(`lifecycle.Start`), but no UI drives it: no progress, no cancel, no
confirmation before replacing a running single-model server, and no handling of
the engine's typed errors. A pulled ollama row is a special launch exception
(`launchable()`), left over from before the engine existed.

## Design

### Approach

Two new phases inside the existing Bubble Tea state machine, with the start
running as a `tea.Cmd` that returns messages (the repo's existing pattern:
`ensureBranchWorktreeCmd`, `launchDoneMsg`); confirmations reuse the `list`
choice-screen pattern (`phaseOllamaWarn`). Rejected: calling `lifecycle.Start`
synchronously in the update loop (freezes the UI for up to minutes, no cancel);
suspending the TUI and printing progress like the launched agent does (no
in-TUI cancel, jarring redraw).

### What a row can do

`tableRow` gets `action()`, returning what Enter does, replacing `launchable()`
and the ad-hoc `blocked` string:

- **launch** — cloud rows and running local rows.
- **start** — a non-running local row whose model is on disk (status `ok` or
  `new`) or whose presence is unknown (`ArtifactKnown` false).
- **block(reason)** — with a reason and message: `absent` rows ("not on disk —
  pull or download it first": the engine would wait out its 600s warmup on a
  model that is not there), a discovered model under LiteLLM routing ("not in
  LiteLLM"), and any other row that cannot proceed.

Pulled ollama models stop being a launch exception: they go through **start**
(the engine warms the model), so a dead daemon fails early with a clear message
and RUNNING reflects reality. The single-row auto-launch shortcut applies only
to a row whose action is **launch**.

### The start flow

Enter on a **start** row runs `lifecycle.Start` as a `tea.Cmd` with
`AllowReplace: false` first. This reuses the engine's own occupant check rather
than duplicating it in the UI, and stays correct if state changed since the
table was built.

- **`phaseStarting`** shows `Starting <model> — <stage> (12s)   [esc] cancel`.
  Stages arrive through a channel that a re-arming command turns into messages.
  Cancel calls the stored `context.CancelFunc`; the engine tears down anything
  it spawned.
- **`phaseReplaceConfirm`** appears when `Start` returns `*OccupiedError` (names
  the model that will be stopped) or `*OccupancyUnknownError` ("cannot tell
  whether <provider> is already serving"). Choices: "Replace and start" and
  "Cancel"; **Cancel is the default**. Confirming re-issues `Start` with
  `AllowReplace: true`.
- **Keys during a start:** esc, q and ctrl+c all cancel on the **first** press
  (the stored `context.CancelFunc` is called; the engine tears down anything it
  spawned). Once cancellation is draining, esc and q are **ignored** and only
  ctrl+c quits. Quitting mid-teardown can orphan a half-started mtplx server —
  it runs in its own session and keeps the port — and esc/q are exactly the keys
  a user mashes when a start looks stuck; but a teardown that never returns must
  not trap the user on the progress screen, so ctrl+c stays a deliberate escape
  hatch. Otherwise the further press to quit is available from the picker.
  *(Amended 2026-09-20 during code review: the original text said all three keys
  cancel and "never quit the program", which contradicted the implementation and
  left a hung cancel with no way out.)*

### Results and errors

| Result | UX |
|---|---|
| success | launch immediately through the existing `proceedToLaunch` (resume prompt and ollama check unchanged) |
| `*DaemonDownError` | back to the picker: "<provider> is not answering at <origin> — start it first" |
| `*BinaryMissingError`, `*PortBusyError` | back to the picker with the engine's own message |
| cancelled | "cancelled", back to the picker |
| any other error | "failed to start <model>: <error>" |

After any start attempt that returns to the picker, the table is rebuilt from a
fresh `Inventory` with the cursor kept: a replace can stop the occupant and then
fail, and the old table would be lying. This requires extracting the table build
from `enterModelPhase` into a reusable method.

Test seam: `var startModel = lifecycle.Start`.

### Out of scope

The non-TUI, `-M` pin and `wt smoke` paths (4c); the smoke gate (5); any change
to the engine; stopping models when wt exits; a user-facing stop command.

## Testing

Per the repo pattern (assert on unexported functions; stub through a
`var x = realX` seam; every `Test*` has a what/why comment):

- `action()` for every row kind (launch / start / each block reason), including
  a pulled ollama row now being **start**.
- Full flow: Enter on a start row enters `phaseStarting`; stage messages update
  the view; success proceeds to launch.
- Occupied and unknown-occupancy each show the confirm dialog with Cancel
  selected by default; confirming re-issues `Start` with `AllowReplace: true`;
  Cancel returns to the picker without starting.
- Esc, q and ctrl+c during a start cancel the context; while cancelling, esc and
  q are ignored and ctrl+c quits wt (see "Keys during a start" above). The
  engine's done message returns to the picker with status "cancelled".
- Each typed error produces its message; cancellation reads "cancelled".
- The table refreshes after a failed start (a stale "run" row disappears).
- The single-row shortcut does not fire for a start row.
- View-based check that the progress screen renders the stage and elapsed time.

Update `wt/CLAUDE.md` (TUI section, launch rules, test seams) when implemented.
