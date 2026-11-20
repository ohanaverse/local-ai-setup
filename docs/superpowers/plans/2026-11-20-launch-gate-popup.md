# Launch gate & profile popups (issue #625, ring 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** wt's picker launches ask their launch-gate (warnings + creation) and local-model-profile (`apply?`) confirmations as bubble tea popup screens, answered before the terminal is released for the agent.

**Architecture:** One generic ask-popup component (`phaseAsk` + `askState`, instantiated for gate and profile) intercepts `commitLaunch` at the commit point: `commitAsk` reads the gate Target at the ask, shows popups when they apply, and binds the run command only once answered, with a pre-answered gate (`m.gateAnswer`/`m.gateTarget`) and a carried profile answer (`m.profileCarry`). The profile layer arrives as a `Resolve`/`Apply` pair mirroring the `SlotProfiles` idiom; non-TUI paths and non-popup paths are byte-identical to today.

**Tech Stack:** Go, bubbletea/bubbles (v1.3.10), charmbracelet/lipgloss.

**Spec:** `docs/superpowers/specs/2026-11-20-launch-gate-popup-design.md`

## Global Constraints

- No raw y/n anywhere in a picker launch: every question in `phaseAsk`. Flag paths (`--root`, `-W`, `--cwd`) and `wt smoke` keep `confirmLaunch`/`launchwarn.Check` untouched, including declined-gate exit 3.
- `internal/launchwarn`'s warning strings stay byte-identical; only `creationLine` is extracted to an exported `CreationLine`.
- One Target read per ask when a popup shows: the released-phase gate consumes `m.gateTarget`, never re-reads (`launchTarget` again).
- Non-popup paths (no warnings / non-tty / `prePath`) behave byte-for-byte as today: `Check` runs inside the released-phase gate, prints, and — non-tty — never reads stdin.
- Declines at a popup release everything the launch took (prep's client-session hold, slot holds), release `m.committed`, return to the phase the commit was made from, and populate nothing (no records, summary, or survey state). No run command is ever built-and-executed around a decline.
- `committed` stays true from `commitAsk` entry until `bind` or decline; only declines (and `slotAbortMsg`) release it.
- `profiles`' failure posture: a resolve error → no popup, unprofiled launch, one `wt: profiles.toml: …` note at the released phase (the existing warning), agent still starts.
- Wording rules: queued post-release lines carry the `wt: ` prefix (`wt: note: …` for a read failure) because they print to stderr, not to a popup frame.

## Review Focus

- **A session appears while the gate popup is open** — the confirm must bind against the popup-evaluated Target (`m.gateTarget`), never a second read. Test: Task 2 step "answered gate consumes gateTarget" + Task 4 (popup path sets both fields together).
- **Quit/give-up from a popup while a hold exists** (prep-held model during `phaseAsk`, slot models) — the hold must drop; no stranded client session. Test: Task 4's "decline/quit drops the prep hold" and slot-decline steps.
- **Non-tty picker run with warnings** — warnings print raw and the launch proceeds, stdin never read, no popup. Test: Task 4 keeps the existing `TestCheckNoTTY…`-parity launcher test green and adds a `commitAsk` non-tty pass-through test.
- **Flag paths and `wt smoke`** — identical raw behavior, decline = exit 3, nothing created. Test: the existing `cmd/wt-session` launcher tests stay untouched and green after every task.
- **Profile resolve error mid-commit** — no popup, unprofiled launch, agent still starts, one post-release note. Test: Task 5's resolve-error test.

---

### Task 1: `launchwarn.CreationLine`

**Files:**
- Modify: `wt/internal/launchwarn/check.go`
- Test: `wt/internal/launchwarn/check_test.go` (create if absent; the package's tests live in per-file `_test.go`s)

**Interfaces:**
- Produces: `func CreationLine(c *Creation) string` — the exact strings `creationLine` builds today (`creating branch %s tracking %s and worktree %s` / `creating branch %s and worktree %s` / `creating worktree %s for branch %s tracking %s` / `creating worktree %s for branch %s`). Task 4's popup and post-release queue call it; `Check` keeps its output byte-identical (delta from spec: no `InfoLine` helper — `Collect`'s `info` return already is the informational line).

- [ ] **Step 1: Write the failing test**

```go
func TestCreationLineMatchesCheck(t *testing.T) {
	for _, c := range []*Creation{
		{Branch: "b", Tracks: "origin/b", NewBranch: true, Path: "/p"},
		{Branch: "b", NewBranch: true, Path: "/p"},
		{Branch: "b", Tracks: "origin/b", Path: "/p"},
		{Branch: "b", Path: "/p"},
	} {
		var buf bytes.Buffer
		Check(strings.NewReader("\n"), &buf, true, nil, "", c)
		want := strings.TrimPrefix(strings.TrimRight(buf.String(), "\n"), "wt: ")
		if got := CreationLine(c); got != want {
			t.Errorf("CreationLine(%+v) = %q, Check printed %q", c, got, want)
		}
	}
}
```

(With warnings nil and `isTTY` true, `Check` outputs only the creation line for a non-nil creation — see `TestCheckCreationOnlyDoesNotPrompt`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/launchwarn/ -run TestCreationLineMatchesCheck -v`
Expected: FAIL — `undefined: CreationLine`.

- [ ] **Step 3: Implement** — rename `creationLine` to `CreationLine` (exported) in `check.go`; `Check` calls it unchanged.

- [ ] **Step 4: Run test to verify it passes; run the package's other tests**

Run: `cd wt && go test ./internal/launchwarn/`
Expected: PASS (all of `check_test`, `launchwarn_test`).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/launchwarn/ && git commit -m "issue #625: launchwarn.CreationLine exported for the ask popup"
```

---

### Task 2: The pre-answered gate (`gateAnswer`/`gateTarget`)

**Files:**
- Modify: `wt/internal/tui/launchgate.go` (gateFor, launchGate), `wt/internal/tui/app.go` (model fields: `gateTarget`, `gateAnswer`)
- Test: `wt/internal/tui/launchgate_test.go`

**Interfaces:**
- Produces: model fields `gateTarget *launchwarn.Target` (the popup's read; nil = no popup path) and `gateAnswer *bool` (set before bind on a popup yes). `launchGate` keeps its exact signature `(m model) launchGate(t *launchwarn.Target, isTTY bool) (ok bool, err error)` — Task 4 calls it through `gateFor()` unchanged. The closure consults the fields at run time; `m` captured by value inside `gateFor`'s closure is why Task 4's bind closures rebuild `gateFor()` at bind time — do not "simplify" this away.

- [ ] **Step 1: Write the failing tests** (append to `launchgate_test.go`; reuse `rootRepo`, `linkedWorktree`, `releaseRootClaim`, `answerOnStdio`, `claimExists` there)

```go
// TestLaunchGateAnsweredSkipsCheckAndClaims: a pre-answered gate (gateAnswer
// non-nil) asks nothing raw — no "continue?" line is printed or read — and
// still claims, when pointed at the popup's captured target.
func TestLaunchGateAnsweredSkipsCheckAndClaims(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	stderr := answerOnStdio(t, "")
	m := model{agent: "claude", selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	m.gateTarget, m.gateAnswer = &tg, &yes
	if ok, gerr := m.launchGate(m.gateTarget, true); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); the answered gate proceeds", ok, gerr)
	}
	if got := stderr(); strings.Contains(got, "continue?") || strings.Contains(got, "repository root") {
		t.Errorf("answered gate printed %q; want nothing (the popup asked)", got)
	}
	if !claimExists(root) {
		t.Error("the answered gate claimed nothing")
	}
}

// TestGateForConsumesGateTarget: gateFor's closure uses gateTarget and never
// re-reads — a changed worktree state after the popup must not surface here.
func TestGateForConsumesGateTarget(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	stderr := answerOnStdio(t, "")
	m := model{agent: "claude", selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	m.gateTarget, m.gateAnswer = &tg, &yes
	cmd := m.gateFor()
	if ok, gerr := cmd(); !ok || gerr != nil {
		t.Fatalf("gateFor = (%v, %v)", ok, gerr)
	}
	if got := stderr(); got != "" {
		t.Errorf("gate printed %q; the popup path asks nothing", got)
	}
	if !claimExists(root) {
		t.Error("gateFor claimed nothing on the popup path")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/tui/ -run 'TestLaunchGateAnswered|TestGateForConsumesGateTarget' -v`
Expected: FAIL (compile error on `gateTarget`/`gateAnswer`).

- [ ] **Step 3: Implement**

`launchGate` short-circuits the raw ask only; the claim/ensure body is untouched:

```go
	if m.gateAnswer == nil {
		if !launchwarn.Check(os.Stdin, os.Stderr, isTTY, warnings, info, t.Creation) {
			return false, nil
		}
	}
```

`gateFor` consumes the captured target when the popup answered, else today's re-read:

```go
func (m model) gateFor() func() (bool, error) {
	return func() (bool, error) {
		if m.gateAnswer != nil && m.gateTarget != nil {
			return m.launchGate(m.gateTarget, stdinTTY())
		}
		t, readErr := m.launchTarget()
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "wt: note: %v\n", readErr)
		}
		return m.launchGate(&t, stdinTTY())
	}
}
```

Add both fields to the `model` struct in `app.go` beside `typedPlan`/`pickedRow`, with a comment each ("Rebound by every selection" does not apply — `commitAsk` (Task 4) resets them at every commit; a decline clears them).

- [ ] **Step 4: Run tests; also the package's launch tests**

Run: `cd wt && go test ./internal/tui/ -run 'TestLaunchGate|TestGateFor' && go build ./...`
Expected: PASS. (`TestLaunchGateRootNoOthersWarnsAndClaims` passes unchanged: fields nil → today's behavior.)

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/ && git commit -m "issue #625: pre-answered launch gate (gateAnswer/gateTarget)"
```

---

### Task 3: The ask popup component (`phaseAsk`) — component only

**Files:**
- Create: `wt/internal/tui/ask_popup.go`
- Modify: `wt/internal/tui/app.go` (phase const, model field `ask`, Update dispatch, View, fitLists)
- Test: `wt/internal/tui/ask_popup_test.go`

**Interfaces:**
- Consumes: `choiceItem` (launch.go), `newReplaceChoices`' idiom, `drawnFrame`/`fitTo`/`clip`/`tuilayout.WrapText` (layout.go), `themes.ListDelegate`.
- Produces (Task 4 consumes): `askPurpose` enum with `askGate` and `askProfile`;
  `type askState struct { purpose askPurpose; title string; body []string; backPhase phase; choices list.Model; bind, decline func(model) (model, tea.Cmd) }`;
  `newAskState(theme themes.Theme, width, height int, title string, body []string, backPhase phase, bind, decline func(model) (model, tea.Cmd)) *askState` — two `choiceItem`s, titles "launch anyway"/"go back" for `askGate` and "apply profile"/"don't apply" for `askProfile`, cursor at 0 (the yes item — the raw prompts' default-yes parity);
  `(m *model) seedAsk(s *askState) (model, tea.Cmd)` — sets `m.ask`, `m.phase = phaseAsk`;
  `(m model) updateAsk(msg tea.Msg) (model, tea.Cmd)`.

- [ ] **Step 1: Write the failing tests**

```go
// TestAskKeysRoute: enter/y answer yes (cursor item 0), esc/n answer no, and
// q/ctrl+c quit through the decline. up/down move the cursor.
func TestAskKeysRoute(t *testing.T) {
	for tt, tc := range map[string]struct{ key, want string }{
		"enter": {"enter", "yes"}, "y": {"y", "yes"},
		"esc": {"esc", "no"}, "n": {"n", "no"}, "q": {"q", "quit"},
		"down+enter": {"down+enter", "no"},
	} {
		var answered string
		bind := func(mm model) (model, tea.Cmd) { answered = "yes"; return mm, nil }
		decline := func(mm model) (model, tea.Cmd) {
			answered = map[string]string{"no": "no", "quit": "quit"}[tc.want]
			if tc.want == "quit" {
				return mm, tea.Quit
			}
			return mm, nil
		}
		m := model{}
		s := newAskState(themes.Theme{}, 80, 24, "launch here?", []string{"• root is on feat"}, phaseModel, bind, decline)
		next, _ := m.seedAsk(s)
		switch tc.key {
		case "down+enter":
			next, _ = updateMsg(next, tea.KeyMsg{Type: tea.KeyDown})
			next, _ = updateMsg(next, tea.KeyMsg{Type: tea.KeyEnter})
		default:
			next, _ = updateMsg(next, tea.KeyMsg{Type: tea.KeyEnter, Runes: []rune(tc.key)})
		}
		if answered != tc.want {
			t.Errorf("%s: answered %q, want %q", tt, answered, tc.want)
		}
		if next.phase == phaseAsk {
			t.Errorf("%s: still in phaseAsk", tt)
		}
	}
}

// TestAskViewShowsTitleBodyAndChoices: the header renders the title and body
// (wrapped, never clipped mid-word — reusing the replaceFrames wrap loop), and
// the choices list shows both items' titles.
func TestAskViewShowsTitleBodyAndChoices(t *testing.T) {
	m := model{theme: themes.Theme{}, width: 80, height: 24}
	next, _ := m.seedAsk(newAskState(m.theme, 80, 24, "launch here?", []string{"• root is on feat, not main"}, phaseModel, nil, nil))
	v := next.View()
	for _, want := range []string{"launch here?", "root is on feat, not main", "launch anyway", "go back"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}
```

If `updateMsg` (used by `others_dialog_test.go`) lives in another test file it is package-visible; reuse it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/tui/ -run TestAsk -v`
Expected: FAIL — undefined `askPurpose`/`newAskState`/`seedAsk`.

- [ ] **Step 3: Implement `ask_popup.go`** plus the four app.go hooks:

- `app.go`: add `phaseAsk` to the phase consts (comment: "the launch gate / profile popups (ask_popup.go)"); field `ask *askState` on `model` beside `replace`; intercept in `Update`'s KeyMsg block beside the `phaseStarting`/`phaseLaunching` intercepts: `if m.phase == phaseAsk && m.ask != nil { return m.updateAsk(msg) }` (this placement, before the generic `q`/ctrl+c/esc switch, is what makes q/ctrl+c route through the decline path — Task 4 wires the quit case); View: `if m.phase == phaseAsk && m.ask != nil { return drawnFrame(&m.ask.choices, m.height, m.askFrames()...) }` guarded on `m.width <= 0 || m.height <= 0` like `phaseReplaceConfirm` (return `"confirm (waiting for window size)"`); `fitLists`: `case phaseAsk: m.fitTo(&m.ask.choices, m.askFrames()...)`.
- `ask_popup.go`: `askPurpose`, `askState`, `newAskState` (builds the `list.Model` via the `newReplaceChoices` helper), `seedAsk` (clears `m.status` before entering, as every phase transition does), `updateAsk`:

```go
func (m model) updateAsk(msg tea.Msg) (model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "y", "enter":
			return m.ask.bind(m)
		case "esc", "n":
			return m.ask.decline(m)
		case "q", "ctrl+c":
			next, c := m.ask.decline(m)
			return next, tea.Sequence(c, tea.Quit)
		}
	}
	// Non-key messages (resize) and the cursor keys fall through to the list.
	nextList, cmd := m.ask.choices.Update(msg)
	m.ask.choices = nextList
	return m, cmd
}
```

(The down+enter test path exercises the cursor fall-through and the yes/no items' order; the cursor's position picks nothing beyond the yes/no order, which the keys already cover. Keep `choiceItem`'s `choice` field set anyway, mirroring `replaceState`'s idiom.)
- `askFrames()` in `ask_popup.go`: header = title + body lines wrapped with the same `WrapText`-per-line loop `replaceFrames` uses (indent two spaces), then `m.choiceFrame()` hints; a second frame without hints for short terminals; no stop-choice machinery.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/tui/ -run TestAsk && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/ && git commit -m "issue #625: phaseAsk ask-popup component (gate/profile)"
```

---

### Task 4: `commitAsk` — the four commit sites, gate popup, declines

**Files:**
- Modify: `wt/internal/tui/launchgate.go` (stdinTTY var seam), `wt/internal/tui/launch.go` (commitAsk, site helpers), `wt/internal/tui/app.go` (launchCommand, launchNative sites), `wt/internal/tui/remote_start.go` (`handlePrepared` site), `wt/internal/tui/slot_flow.go` (slot site)
- Test: `wt/internal/tui/ask_popup_test.go` (or a new `commit_ask_test.go`)

**Interfaces:**
- Consumes: Task 2's fields, Task 3's component, `launchwarn.Collect`, `CreationLine`, `launchwarn.FromRow`.
- Produces:

```go
func (m model) commitAsk(mdl config.Model, bind func(model) (model, tea.Cmd)) (model, tea.Cmd)
```
— the commit point for all four sites. Takes the `committed` guard, resets `gateTarget`/`gateAnswer` (profileCarry lands in Task 5), reads the Target once (`m.launchTarget()`), and:
  - popup, when `m.prePath == ""` && `stdinTTY()` && warnings non-empty: read failure queues `wt: note: …` to `pendingRouteNotes` via `m.recordRouteNotes`; routes the popup body = `CreationLine` (when `t.Creation != nil`) + warning texts (plain, no `wt: ` prefix); backPhase = the phase the commit was entered from (`m.phase` at entry); `bind` = the site's binder; `decline` = the site's decliner; returns `phaseAsk`, no cmd.
  - no popup: falls straight through to `bind(m)`.
- Site binders (each the old tail, called with the answered model): `mm.commitLaunch(runAndWaitCmd(cmd, name, config.Model{}, mm.gateFor()))` etc. — the four sites keep their run-command construction exactly as today, moved inside `bind`.
- Site decliners: `handlePrepared` — `dropAllCmd(m.hold, []string{m.launchModel.ID})`, `phase = phaseModel`; slot — `m.dropRemoteSlot()`, back to `phaseModel`; command/native — no drop; all set `phase = s.backPhase`, `m.committed = false`, `m.ask = nil`, clear `gateTarget`/`gateAnswer`.
- Gate-yes continuation `afterGate(bind, mdl, t *launchwarn.Target) (model, tea.Cmd)`: sets `gateTarget`+`gateAnswer` together, queues `"wt: "+info+"\n"` (from `Collect`) and, when a creation is owed, `"wt: "+CreationLine+"\n"` to `pendingRouteNotes`, then the profile step (Task 5 slot: resolver consulted only when `profileResolver != nil`; for this task the resolver seam does not exist yet, so bind directly).
- `var stdinTTY = func() bool { return term.IsTerminal(os.Stdin.Fd()) }` — `stdinTTY` becomes a package var (same body, called as before), so tests can force the popup path.

- [ ] **Step 1: Write the failing tests** (these need `stdinTTY` stubbed true; restore with `t.Cleanup`. Reuse `rootRepo`, `liveOther`, `releaseRootClaim`, `claimExists`, `readClaimFields` from `launchgate_test.go`; the sites need hooks stubbed — see `launch_test.go`'s hook seams for `buildLaunch`)

```go
// TestCommitAskGatePopupAndDecline: with warnings on a (stubbed) terminal the
// commit enters phaseAsk; the decline returns to the picker phase, releases
// committed, clears the fields, drops the prep hold, and never builds a run
// command.
func TestCommitAskGatePopupAndDecline(t *testing.T) {
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	m := model{agent: "claude", selectedPath: root, phase: phaseModel}
	var built bool
	bind := func(mm model) (model, tea.Cmd) { built = true; return mm, nil }
	next, _ := m.commitAsk(config.Model{}, bind)
	if next.phase != phaseAsk || next.ask == nil {
		t.Fatalf("commitAsk = phase %v; want a gate popup", next.phase)
	}
	if !strings.Contains(strings.Join(next.ask.body, "\n"), "repository root") {
		t.Errorf("popup body = %v; want the root warning", next.ask.body)
	}
	after, cmd := updateMsg(next, tea.KeyMsg{Type: tea.KeyEsc})
	if built {
		t.Error("decline built a run command")
	}
	if cmd != nil || after.phase != phaseModel || after.committed || after.gateAnswer != nil || after.gateTarget != nil {
		t.Errorf("decline = (phase %v, committed %v); want back on the picker, uncommitted, fields cleared", after.phase, after.committed)
	}
}
```

Plus the y-path (`enter` → `built` true, `gateAnswer`/`gateTarget` non-nil, route notes queued: check `pendingRouteNotes` contains `"wt: launching in "`), a slot-decline test asserting `m.rslot == nil` after `esc`, and a `prePath` model that binds with no popup.

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/tui/ -run TestCommitAsk -v`
Expected: FAIL — undefined `commitAsk`.

- [ ] **Step 3: Implement** — `commitAsk` in `launch.go` per the Interfaces block; convert the four sites:

- `app.go` `launchCommand`: `return m.commitAsk(config.Model{}, func(mm model) (model, tea.Cmd) { return mm.commitLaunch(runAndWaitCmd(cmd, name, config.Model{}, mm.gateFor())) })` (the queueLaunchNotes/build-error lines stay put).
- `app.go` `launchNative`: same shape, binder `mm.commitLaunch(runAndWaitCmd(cmd, agent, mdl, sayAfterGate(mm.gateFor(), os.Stderr, agents.NativeNote(agent))))`.
- `remote_start.go` `handlePrepared`: binder `mm.commitLaunch(runRecordAndWaitWith(msg.cmd, agent, m.launchModel, record, mm.gateFor(), func() { h.end(os.Stderr) }))`; decliner as in Interfaces (prep hold drop).
- `slot_flow.go`: binder `mm.commitLaunch(runSlotAndWaitCmd(cmd, m.agent, row, mm.gateFor()))`; decliner `mm.dropRemoteSlot()` + back-phase.
- `commitLaunch` itself is unchanged; `commitAsk` sets `m.committed = true` before the popup decision.
- Route-note queueing on gate-yes: `afterGate` queues `info`/creation; the read-failure note is queued at popup-open only on the popup path (`wt: note: <err>`); a later popup-path decline leaves it queued (it prints when wt exits — acceptable: it's informational).

- [ ] **Step 4: Run to verify they pass; then the whole package and the flag-path package**

Run: `cd wt && go test ./internal/tui/ && go test ./cmd/wt-session/ -run 'TestLaunch|TestConfirm|Root|Cwd' -count=1`
Expected: PASS — the non-popup/non-tty parity tests (e.g. `TestLaunchGateRootNoOthersWarnsAndClaims` via `launchGate` direct calls, all `cmd/wt-session` launcher tests) still green.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/ && git commit -m "issue #625: commitAsk gate popup at all four commit sites"
```

---

### Task 5: The profile layer — Resolve/Apply, carry, popup

**Files:**
- Modify: `wt/internal/tui/launch.go` (`ProfileLayer`, seams `profileResolver`/`profileApplier`, `profileCarry`, `runRecordAndWaitWith` signature, `afterGate`'s profile step), `wt/internal/tui/app.go` (`Run` signature, `withProfileLayer`), `wt/internal/tui/remote_start.go` (build passes `mm.profileCarry`), `cmd/wt-session/launch.go` (`resolveLaunchProfile`, `applyProfileForLaunchCarried`), `cmd/wt-session/main.go` (builds and passes the layer)
- Test: `wt/internal/tui/ask_popup_test.go` (or `profile_popup_test.go`), `wt/internal/tui/launch_test.go` + `launch_exit_status_test.go` (mechanical stub updates), `wt/cmd/wt-session/flip_test.go` + `helpers_test.go` (stub signatures), `wt/cmd/wt-session/launch_test.go` (new resolve/apply tests)

**Interfaces:**

```go
type ProfileLayer struct {
	Resolve func(agent string, m config.Model) (profiles.ResolvedProfile, bool, error)
	Apply   func(cmd *exec.Cmd, agent string, m config.Model, rp *profiles.ResolvedProfile, apply bool) (func() error, error)
}
type profileCarry struct{ rp *profiles.ResolvedProfile; apply bool }
```
- `runRecordAndWaitWith(cmd, agent, m, record, gate, release, carry *profileCarry)` — calls the applier as `profileApplier(cmd, agent, m, c.rp, c.apply)` (c nil → `(nil, false)`); `profileApplier` and the new `profileResolver` are package vars set by `withProfileLayer(m model, p ProfileLayer) model` from `Run`'s new `profileLayer ProfileLayer` param (replacing `applyProfile ProfileApplier`), or by tests directly — the zero value must keep the "no profile layer" no-op.
- `afterGate` resolves when `profileResolver != nil`: a match and the tty-still-true probe? (no extra probe — the popup is only shown on the poppable path; a no-popup commit that matched (resolve ok) with no tty: apply defaults yes via the legacy path — same as today) seeds a second `askState{purpose: askProfile, title: "apply local-model profile?", body: sources line}` with the same backPhase/bind/decline, and its yes sets `profileCarry` then binds; its no runs the decline. A resolve error or no-match binds directly (no popup) and leaves `profileCarry` nil (Apply's `(nil, false)` → legacy self-resolve + `confirmProfile`, which on a no-tty commit is today's auto-yes).
- `cmd/wt-session`:

```go
func resolveLaunchProfile(agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles) (profiles.ResolvedProfile, bool, error)
func applyProfileForLaunchCarried(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles, rp *profiles.ResolvedProfile, apply bool) (func() error, error)
```
— `resolveLaunchProfile` is applyProfileForLaunch's middle (guards `m.ID == "" || m.Native || cfg == nil` → no match; load/validate error → error; `!store.Enabled` → no match; `Resolve` → `Empty()` → no match); `applyProfileForLaunch` is refactored to call it and keeps its exact stderr warnings; the carried variant: `selfHealAgentConfigContentTarget` first, then the same guards, then `rp != nil ? (apply ? applyResolvedProfile(cmd, agent, *rp, nil) : noop) : applyProfileForLaunch(…)` — the `rp != nil` branch never touches `confirmProfile`. `main.go` builds `tui.ProfileLayer{Resolve: …, Apply: …}` from `scfg`/`a.profileState()` and passes it to `tui.Run`; the test stubs' `tui.ProfileApplier` parameter type becomes `tui.ProfileLayer`.

- [ ] **Step 1: Write the failing tests**

In `internal/tui` (stdinTTY stubbed true; `profileResolver`/`profileApplier` stubbed, restored with `t.Cleanup`; `m.launchModel` set on the model):

```go
// TestCommitAskProfilePopup: a matching profile after a gate yes (no warnings)
// opens the profile popup; "don't apply" declines (no run command, holds
// dropped); "apply profile" binds with profileCarry set, and the carried answer
// travels to the applier.
func TestCommitAskProfilePopup(t *testing.T) {
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	var appliedRP *profiles.ResolvedProfile
	var appliedApply bool
	oldApply := profileApplier
	profileApplier = func(_ *exec.Cmd, _ string, _ config.Model, rp *profiles.ResolvedProfile, apply bool) (func() error, error) {
		appliedRP, appliedApply = rp, apply
		return func() error { return nil }, nil
	}
	t.Cleanup(func() { profileApplier = oldApply })
	oldResolve := profileResolver
	rp := profiles.ResolvedProfile{Sources: []string{"omlx/qwen3.8"}}
	profileResolver = func(string, config.Model) (profiles.ResolvedProfile, bool, error) { return rp, true, nil }
	t.Cleanup(func() { profileResolver = oldResolve })

	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	m := model{agent: "claude", selectedPath: linked, launchModel: config.Model{ID: "ollama/x"}, phase: phaseModel}
	next, _ := m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) { return mm, nil })
	if next.phase != phaseAsk {
		t.Fatalf("no profile popup (phase %v)", next.phase)
	}
	after, _ := updateMsg(next, tea.KeyMsg{Type: tea.KeyEnter}) // cursor 0 = apply
	if appliedRP == nil || !appliedApply {
		t.Errorf("applier got (%v, %v); want the popup's (rp, true)", appliedRP, appliedApply)
	}
}
```

(a "don't apply" sibling test asserts `applied` untouched and the decline treatment; a resolve-error test asserts no popup and the legacy path runs.) In `cmd/wt-session/launch_test.go`:

```go
// TestResolveLaunchProfileGuards: command/native/nil-cfg are no-match; a store
// load error is the error; a matching store resolves; the carried apply skips
// confirmProfile (a stubbed openTTY that would block proves it).
func TestResolveLaunchProfileGuards(t *testing.T) { ... }
```

(Reuse that file's profile fixtures — see `TestApplyProfileForLaunch*` there — and loadProfileStore stub.)

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/tui/ -run TestCommitAskProfile -v && go test ./cmd/wt-session/ -run TestResolveLaunchProfile -v`
Expected: FAIL — undefined `ProfileLayer`/`resolveLaunchProfile`.

- [ ] **Step 3: Implement** per Interfaces. `runRecordAndWaitWith`'s call site: `profileCleanup, perr = profileApplier(cmd, agent, m, c.rp, c.apply)`. The applier's nil-layer no-op check becomes `profileApplier == nil`. Existing direct `profileApplier` test stubs (`launch_test.go:163`, `launch_exit_status_test.go:104`) get the two extra params (ignored unless they want them). `main.go`'s `tui.Run(...)` gains the layer where `applyProfile` sat; `flip_test.go:72` and `helpers_test.go:39` update the stub's parameter list to `tui.ProfileLayer`.

- [ ] **Step 4: Run to verify they pass; then both packages**

Run: `cd wt && go test ./internal/tui/ ./cmd/wt-session/`
Expected: PASS (flag paths' profile prompt behavior unchanged — `confirmProfile`/`promptProfile` untouched; `smoke` unaffected).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/ wt/cmd/wt-session/ && git commit -m "issue #625: profile confirmation as an ask popup (ProfileLayer Resolve/Apply)"
```

---

### Task 6: Docs and changelog

**Files:**
- Modify: `wt/docs/internals/launch-flow.md` (the *Picker* sub-bullet under "Session claims": where the gate's question is asked now — the ask popup at the commit point; the decline's new meaning (return to picker, holds dropped, wt alive; exit 3 stays the flag paths'); what repeats post-release (info/creation/hint via route notes as above); the one-read-per-ask rule)
- Modify: `wt/docs/wt-agents/profiles.md` "Confirmation" line — popup on the picker path, raw `/dev/tty` elsewhere.
- Modify: `wt/docs/internals/tui.md` — add `phaseAsk` to the phase list (one line, the file's own style).
- Modify: `wt/CHANGELOG.md` — one entry, issue #625.
- Do NOT touch: `docs/guides/*` (live model state ban is unrelated, but no guide describes this prompt), `docs/style-guide.md` (no new rule introduced).

**Interfaces:** none.

- [ ] **Step 1: Edit the three docs + changelog** — wording follows the spec's non-goals and "What prints after the release"; label nothing as a captured example.
- [ ] **Step 2: Run `cd wt && make check 2>/dev/null || (go test ./... && go vet ./...)`**

Run: `cd wt && go test ./... && go vet ./...`
Expected: PASS.
- [ ] **Step 3: Root-level checks the repo runs before every PR (full `make pre-pr` is the executor's final step)**

Run: `cd /Users/keith/github/ohanaverse/local-ai-setup && ./bin/check-links`
Expected: exit 0.
- [ ] **Step 4: Commit**

```bash
git add wt/docs wt/CHANGELOG.md && git commit -m "issue #625: docs for the ask popups"
```

**Executor's final step (not a task):** `make pre-pr` in a background shell (~12.5 min; read the exit from the log), then `git push` and open the PR referencing #625 with `Refs #625` (never a `close`/`fixes` keyword — the issue also wants the flag-paths half later).
