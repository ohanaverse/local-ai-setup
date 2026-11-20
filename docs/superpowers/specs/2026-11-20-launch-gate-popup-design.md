# Launch gate and profile confirmations as TUI popups

Date: 2026-11-20. Size: a project (one new TUI phase, two seams, the commit
flow, tests across `internal/tui` and `cmd/wt-session`, three docs).
Resolves the picker-path half of [issue #625](https://github.com/ohanaverse/local-ai-setup/issues/625).

## Intent

wt's picker flow ends with two y/n confirmations that drop out of the TUI onto
the raw terminal, and the drop is jarring: the alt screen is released, `wt:`
lines and a bare `[Y/n]` appear over whatever the shell left there, the answer
is typed blind into a prompt the picker had nothing to do with visually, and
only then does the agent start. The two questions:

1. **The launch gate** (`internal/tui/launchgate.go` → `launchwarn.Check`):
   the warnings of the resolved directory (a chosen root, root drift, a
   session holding the checkout, a gone upstream, dirt) — plus, for a pick
   that owes a worktree, the creation line — and one default-yes
   `continue?` question.
2. **The profile confirmation** (`cmd/wt-session/launch.go` →
   `confirmProfile`/`promptProfile`): `wt: local-model profile available for
   this launch (…) — apply? [Y/n]`, read from `/dev/tty` inside the released
   phase, before the gate.

Both should be bubble tea popups inside the running picker, answered before
the terminal is released for the agent — issue #625, ring 1 (the scope the
user picked): the flag paths (`--root`, `-W`, `--cwd`) and `wt smoke` keep
their raw prompt, because they ask before any TUI exists.

The TUI already asks its start-time questions as popup screens (the replace
confirmation and the stop-others choice, `replaceState` over
`choiceFrame`); the gate and profile popups join that idiom rather than
invent a new mechanism.

## Goals

- On a picker launch with warnings (on a tty), the warnings and the creation
  line appear as one popup screen with two choices — launch anyway / go back
  — answered inside the TUI, before the terminal is released.
- On any picker launch committing a model-driven agent where a local-model
  profile would apply, a popup asks apply / don't apply — including when the
  gate had no warnings to ask about (today's raw `promptProfile` fires there
  too, and must keep firing, only nicer).
- A decline at either popup releases whatever the launch took (the prep's
  client-session hold, a slot's holds), releases the commit guard, and
  returns to where the launch was picked from — nothing is created, claimed,
  recorded or summarised, and the model server's idle grace reaps any model
  the launch started. Quitting wt from a popup (ctrl+c) runs the same
  release: a quit must not strand the prep's hold with no session end.
- Without warnings (or without a tty, or on a preset `prePath` — gate only),
  the commit behaves byte-for-byte as today: no popup, the one
  `launching in <dir>` informational line (or the creation line) prints
  post-release, and a preset path's gate speaks nothing twice.

## Non-goals and anti-goals

- No change to the flag paths (`--root`, `-W`, `--cwd`) or `wt smoke`:
  `confirmLaunch`/`launchwarn.Check` keep printing their warnings and asking
  their raw `[Y/n]` on a terminal, and their declined-gate contract (exit 3,
  one line, nothing created) is untouched.
- No change to `launchwarn`'s wording. `Collect` is the single source of the
  warning lines; the popup renders the same strings `Check` would print. The
  creation line gets a shared helper (below) so popup and `Check` cannot
  drift, and the informational line needs none: `Collect`'s `info` return
  already is that line. Ring 2 (a standalone popup for the flag
  paths) is explicitly out, but the popup's body rendering should be a
  function a future `launchwarn` caller could reuse — pure text in, lines
  drawn.
- No change to what the agent sees: the released phase still wires the
  agent's stdio to the raw terminal; popups strictly precede the release.
- The post-run prompts (survey is switched off; `wt stop`'s numbered picker)
  are outside this design.
- Anti-goal: a second raw y/n anywhere in a picker launch, and a popup that
  prints the warnings a second time in the scrollback without saying why
  (see "Calls" for which lines repeat post-release).

## Constraints

- `internal/tui` may not import the registry or build removal engines:
  cross-package pieces still arrive as closures (the established
  `profileApplier` / `SlotProfiles` / `RemoveFuncs` idiom in `tui.Run`'s
  signature).
- The `committed` guard (app.go) must keep its meaning: exactly one run
  command per selection outcome; it releases on a retry path (today only
  `slotAbortMsg`) and otherwise holds to process exit. A popup go-back is a
  retry path and releases it (a decline returning to the picker must be
  able to pick and commit again).
- Declines stay approval-missing-shaped (C9) where a decline ends a run:
  the flag paths. The popup declines are not run-ends and take the replace
  dialog's path instead (`replaceCancelChoice` → drop the holds → picker).
- One Target read per ask. The popup's read is the ask: the released-phase
  gate must consume the popup-evaluated Target, never re-read (a session
  appearing while the popup is open would otherwise be confirmed against
  questions that no longer match the Target the gate acts on).
- Test seams must survive: `gateFor`'s `stdinTTY` probe is called at the
  ask; tests drive popups by seeding state directly and pressing keys, as
  `others_dialog_test.go` and the `cleanup_confirm_*` tests do.

## Approaches

- **A. Two dedicated popup phases** — a gate phase and a profile phase, each
  with its own state, Update branch and View branch, mirroring
  `phaseReplaceConfirm` one-for-one. Most explicit; the popup mechanics
  (choices list, frame, keys, cursor) get built twice and drift apart.
- **B. One ask-popup component, two instances** — chosen. Both questions are
  the same shape: a title, body lines, and yes/no. One component holds the
  title, the body lines and a choices list; a small purpose switch (gate /
  profile) dispatches each answer to its own handler. One place for keys,
  look and tests; if ring 2 lands later, its standalone popup reuses the
  same rendering.

## Design (approach B)

### The ask popup component

`internal/tui` gains one phase (`phaseAsk`) and one state type
`askState` (file: a new `ask_popup.go`, beside `start_flow.go`):

- Fields: `title string`, `body []string` (the rendered lines), `purpose`
  (an enum: `askGate`, `askProfile`), `choices list.Model` of
  `choiceItem` (the existing type), and **`bind func(model) (model,
  tea.Cmd)`** — the commit site's continuation, which on a yes reads the
  model then (`m.gateAnswer` already set) and binds the run command. The
  continuation is built at commit-evaluate time but reads current state at
  bind time, because `gateFor()` captures `m` by value: a run command built
  before the answer would carry a gate closure that can never see the
  answer.
- `Update`: Enter reads `choices.SelectedItem()`; `y`/`Enter` = yes,
  `n`/`esc` = no (both run the decline path below). ctrl+c quits wt through
  the decline path's releases first, then quits. The list plumbing
  (filtering off, delegate, keys) copies `newReplaceChoices`
  (start_flow.go) so the look matches the replace dialog.
- `View`: `drawnFrame(&m.ask.choices, …)` with the title and body lines
  rendered the way `replaceFrames` composes its own (`choiceFrame`,
  layout.go). Body lines are already plain text; warnings draw as bullets
  (the `•` prefix is rendering, not part of the stored string — `Collect`'s
  strings and `launchwarn`'s frozen wording stay byte-identical).

### Launchwarn line helper

`internal/launchwarn` gains one small exported helper used by `Check` and
the popup alike, so the frozen wording lives in one place:
`CreationLine(c *Creation) string` — the `creating branch b tracking
origin/b and worktree …` line's branches, lifted from `check.go`.
`Check` switches to it; its tests stay byte-green. The informational line
is unchanged: `Collect` returns it (`launching in <dir>`) and the popup
path queues that return verbatim.

### The commit point

At each of the four commit sites (`launchCommand`, `launchNative`,
`handlePrepared`, the slot launch), a new `commitAsk(purposeless so far —
signature sketch: `func (m model) commitAsk(gateOn bool, askProfile bool,
bind func(model) (model, tea.Cmd)) (model, tea.Cmd)`) replaces the direct
`commitLaunch(run…Cmd(…))` call, keeping `runRecordAndWaitWith`'s arguments
inside the `bind` closure the site supplies:

1. **The gate read happens here** (`m.launchTarget()` — read at the ask, the
   rule today's `gateFor` states). The read's `(target, readErr)` is kept on
   the model (`m.gateTarget *launchwarn.Target`) for the released-phase gate
   to consume exactly, so this is the one read per ask.
2. Decide the gate popup: `m.prePath != ""`, or non-tty (`stdinTTY`,
   unchanged probe), or no warnings → no popup; proceed to the profile
   question (below), then bind. Warnings on a tty → seed
   `askState{purpose: askGate, title "launch here?", body:`
   `CreationLine` (when a creation is owed) `+ warnings}`, `bind` as built,
   `phase = phaseAsk`, and return. (A read failure queues its
   `wt: note: …` line to `pendingRouteNotes` instead of printing raw:
   stderr is not visible under the alt screen — the note reaches the user
   post-release above the agent's output; the gate closure skips its own
   note when the popup already reported it.)
3. **On gate yes**, the profile question runs whenever `askProfile` (a
   model-driven, non-command, non-native, non-slot launch — the sites know
   which they are; a command's zero `Model.ID` and a native model match no
   profile anyway): call `ProfileLayer.Resolve(agent, launchModel)`. A match
   seeds the popup (`askProfile`, body: the sources line in
   `promptProfile`'s wording minus the `[Y/n]`), else bind. This question is
   independent of the gate popup — a launch with no warnings but a matching
   profile still gets it. The resolved profile and answer are kept on the
   model (`m.profileResolved`, `m.profileAnswer *bool`) for the applier.
4. **On bind** (gate pass-through or popup yes; profile answered or absent):
   `m.gateAnswer = yes` (unless `prePath` spoke already), then `bind(m)` →
   the site's `commitLaunch(run…Cmd(…))` with gates pre-answered below.
5. **On no at either popup** (and on quit through the popup): the decline
   handler per site:
   - `handlePrepared`'s launch: drop the prep's hold
     (`dropAllCmd(m.hold, [m.launchModel.ID])`, the `staleDropCmd`
     treatment), `m.phase = phaseModel`, release `committed`.
   - slot launch: `m.dropRemoteSlot()` (the replace-cancel treatment), back
     to the model picker, release `committed`.
   - command launch / native launch: nothing is held; release `committed`
     and return to the phase the pick came from (`phaseList` for a command
     picked off a worktree row, `phaseModel` otherwise).
   - A decline populates nothing: no summary, no survey state, no records.
     Any model the launch already started is reaped by the server's idle
     grace, as today.

The gate popup's yes does not create anything: creation (`ensure`), claims,
records and the run all stay inside the released-phase run command, so a
creation that fails after the confirm still returns as a `GateError`
through `launchDoneMsg` and main prints it once — today's contract, one
step later.

### The pre-answered gate

`launchgate.go`'s gate closure stays the single place that prints, ensures
and claims — but on the popup path it consumes `m.gateTarget` instead of
re-reading, and skips `Check`:

- `m.gateAnswer != nil` (popup answered yes) → no `Check` call; take the
  `Target` from `m.gateTarget`, `ensure`/claim as today, proceed.
- otherwise → today's behavior unchanged: read, note, `Check`, hint,
  ensure, claim (the `prePath` skip stays first, untouched).

`gateFor` builds its closure as the current shape (it sees `m.gateAnswer`
because the closure captures the model state read at bind time — the
`askState.bind` continuation rebuilt it — and the popup's Target rides the
model). The `errLaunchDeclined` path becomes picker-path-dead (declines
return to the picker) but stays: the flag paths and `wt smoke` share
`launchwarn.Check`, and the exit-3 contract stays theirs.

The hint line (`t.Hint`) stays where it is: `launchGate` prints it after
`Check` on the released terminal, popup or not — it must not also be queued
(route notes and the raw print would double it).

### The profile layer

`tui.Run`'s signature: the `profileApplier` closure-parameter shape becomes
a pair mirroring the `SlotProfiles{Checks, Apply}` idiom:

```go
type ProfileLayer struct {
    Resolve func(agent string, m config.Model) (profiles.ResolvedProfile, bool, error)
    Apply   func(cmd *exec.Cmd, agent string, m config.Model,
                 rp *profiles.ResolvedProfile, apply bool) (func() error, error)
}
```

- `main.go` builds both from the state `applyProfileForLaunch` already uses
  (the precomputed store, load and validate errors, the session config).
  `Resolve` is the pure match; its `error` (load, validate, resolve
  failures) degrades to unprofiled with one `wt:` note post-release — the
  existing failure posture, no popup when it errors.
- `Apply` uses the popup's `rp`/`apply` when `rp` is non-nil, else resolves
  itself and defaults to apply-without-prompt — exactly today's
  no-popup-TUI behavior (`promptProfile` defaults yes with no tty — and a
  commit that skipped the popup because the tty probe failed is that case).
  The TUI applier path never reaches `confirmProfile`/`promptProfile`; those
  survive untouched for the non-TUI paths.
- `runRecordAndWaitWith` calls the applier with the model's carried
  answer. No changes to what applying means (env/args → config_content →
  wrapper; cleanup after the run).

### What prints after the release

- No-popup paths: unchanged, byte for byte (`launchwarn.Check`'s info line
  when warnings were empty; nothing when there were warnings and no tty).
- Gate-popup path: on confirm, `InfoLine` and, for a creation,
  `CreationLine` are queued to `pendingRouteNotes` (flushed post-release
  above the agent's output, the established capture-then-emit channel). The
  warnings themselves print nowhere after the popup: the popup is the
  record of having seen them, and re-printing four `wt:` lines the agent's
  output then scrolls under buys nothing. The hint line stays with
  `launchGate`'s raw print (above).
- The slot-launch tail, the native note (via `sayAfterGate`, unchanged: it
  fires once the released-phase gate proceeds) and the route notes keep
  their order, all post-release.

### Order and naming

Gate before profile, when both occur. Today's raw order (profile prompt
first, then gate) is an artifact of the released-phase call order, not a
design; asking "apply the profile?" before "launch here at all?" wastes an
answer a gate decline discards. The flow reads: pick model → [gate popup,
when warned] → [profile popup, when a profile matches] → agent. Popup titles
("launch here?", "apply local-model profile?") and the choices' labels are
the builder's to set to taste, subject to the wording rules (`wt:`
prefixing applies to stderr lines, not TUI frames; `docs/style-guide.md`).

## What this leaves open (builder's calls)

- Exact popup keys and visuals beyond the component's shape (title strings,
  whether the body shows the directory, whether `esc` and `n` are equal).
- How tests seed `askState`, and whether the tty probe gets a seam var or
  the tests seed the phase directly (the file's existing idiom is direct).
- The precise docs edits below, beyond the named files.

## Tests and docs

- New: the component's keys and rendering; the two helpers byte-match what
  `Check` printed (shared-source test); gate popup on warnings (yes binds
  and runs, go-back drops the hold and returns, decline creates/claims/
  records nothing, `committed` released — a retry can commit); quit from the
  popup drops the hold; popup skipped on no warnings / non-tty / `prePath`;
  profile popup independently for a matching profile with and without gate
  warnings, its no skipping application, its yes applying without the raw
  prompt; resolve-error posture (no popup, unprofiled, one post-release
  note); the route-notes lines post-release (info + creation + the readErr
  note; hint not among them); slot and command/native sites decline
  correctly; the released-phase gate consumes `m.gateTarget` (no second
  read).
- Existing to keep green: `launchgate_test.go` (nil answer → behavior
  unchanged), all `cleanup_confirm_*`/`others_dialog_test.go` (unrelated
  popups), `cmd/wt-session`'s launch tests (the flag paths, unchanged), the
  exit-status tests (`launch_exit_status_test.go`,
  `tui_exit_status_test.go`).
- Docs: `docs/internals/launch-flow.md` (the picker-gate bullet: where the
  question is asked now, the decline's new meaning, what repeats
  post-release), `docs/wt-agents/profiles.md` "Confirmation" (popup on the
  picker path, raw elsewhere), `wt/docs/internals/tui.md` (the new phase in
  the phase list), `wt/CHANGELOG.md`. `make pre-pr` before the PR.

## Calls made for you to check while reading

1. **Popup decline returns to the picker** (exit 0 when wt is quit there),
   not today's exit-3-and-end — the replace dialog's precedent; wt stays
   alive to pick another model. The flag paths' exit 3 is untouched.
2. **Gate asked before profile**, and the profile question fires whenever a
   profile matches, gate popup or not.
3. **No checkboxes** (agreed): warnings render as bullet lines; the launch
   is all-or-nothing.
4. **Post-release, only factual lines repeat** (`launching in …`, creating
   …, the readErr note): the warnings live in the popup alone; the hint
   continues to print raw from the released-phase gate.
5. **`ensure` and the claim stay in the released phase** (a pre-answered
   gate), not at popup-confirm time — a git failure then still exits 1 via
   `GateError` after the confirm, as today's contract says.
