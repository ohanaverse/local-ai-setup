# Mode-Conflict Guard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Gate and warn where `wt-model setup` and `wt config`'s Mode tab would
cross: confirm before an apply replaces `remote`'s stored url/token or an
unused local server starts, and name the next step that resolves the conflict
on both screens.

**Architecture:** A new leaf helper `modelmode.ConfigMode` reports the mode
config.toml holds (no env), and both gates and all three notes key off it. The
Setup tab's existing pre-plan prompt queue carries the replace-remote gate; a
new confirm prompt kind carries the start/restart gate. The Mode tab's
leaving-local note is strengthened in `modelmode` where it is planned.

**Tech Stack:** Go (bubbletea TUI; `wt/internal/modelmode`,
`wt/internal/modeleditor`, `wt/internal/sessioneditor`), Go tests only.

**Spec:** `docs/superpowers/specs/2026-11-21-mode-conflict-guard-design.md`

## Global Constraints

- Every gate message (G1, G2) and every note that names a fix (NB, L1) ends
  with the `next:` clause the spec fixes, naming what resolves the conflict
  after the plan is applied. NA is the standing form of G1 and carries no
  `next:`: its gate (later) does. Copy is verbatim from "Task copy" below.
- Gates and notes key off `modelmode.ConfigMode` (config.toml only).
  `WT_MODEL_MODE`/`WT_MODEL_URL` never count: they are per-process and already
  flagged by the `env` row.
- Every prompt is default-No: only `y`/`Y` confirms (`n`/`N`/`esc` cancel).
- Planners write exactly what they wrote before: the gate sits before the
  plan, never inside it. The Mode tab never touches wt-model's LaunchAgent.
  `x` (stop) is never gated. No prompt on non-crossing paths: `t` (tighten),
  `k`-menu actions that write no config step, and any apply where the stored
  mode is not `remote`.
- `modelmode` and `modeview` stay session-safe leaves (no `modelsetup`
  import). New view strings are ASCII (`TestSetupTabViewIsASCII`) and carry no
  secrets (`TestSetupTabNeverShowsASecret`).

### Task copy (verbatim; `<mode>`/`<action>` are the named values)

| id | string |
|----|--------|
| G1 | `config.toml says remote: applying <mode> replaces its url and token. next: to use that server again, set remote on ` + "`wt config`" + `'s Mode tab.` |
| G1Q | `Continue? [y/N]` |
| G1H | `y continue · n/esc cancel · ctrl+c quit` / `y · n/esc · ctrl+c quit` |
| G1N | `not applied; nothing was changed` |
| G2 | `the mode is <mode>: no session uses the local server. next: stop it with x when done, or make the mode local on ` + "`wt config`" + `'s Mode tab.` |
| G2Q | `Start it anyway? [y/N]` for ControlStart, `Restart it anyway? [y/N]` for ControlRestart |
| G2H | `y run · n/esc cancel · ctrl+c quit` / `y · n/esc · ctrl+c quit` |
| G2N | `not started; nothing was changed` / `not restarted; nothing was changed` (per action) |
| NA | `config.toml says remote: applying <mode> replaces its url and token` |
| NB | `the local server runs but <mode> is in effect: nothing uses it. next: press x to stop it (choose local first), or apply native here to remove it` |
| L1 | `wt-model's LaunchAgent is untouched; the local server answers at <url> and will be unused. next: stop it with x on ` + "`wt-model setup`" + `, or apply native there to remove the LaunchAgent` |

G1's `<mode>` is `st.planWant.Mode` (the `a` snapshot), G2's `<mode>` is
`modelmode.ConfigMode(st.state)`, NB's `<mode>` likewise; L1's `<url>` is
`st.Server.URL` (the pinged local listen address; always credential-free).

## Review Focus

- `t` (tighten) while stored remote writes no config step and must NOT gate;
  a `k`-menu action that plans the table must gate like `a`. (Test:
  `TestSetupTabGateOnlyForPlansThatReplaceRemote`, Task 3.)
- `n` at the G1 gate aborts the whole flow: no plan, no remove-agent
  question after it, status G1N. (Test: same, Task 3.)
- `x` stays ungated and working on a remote-stored machine — the notes tell
  the user to press it. (Test: `TestSetupTabStartGateOnlyForStartAndRestart`,
  Task 4.)
- L1 must be byte-identical to today's note when the local server does not
  answer (existing sessioneditor tests pin it) and name the url only when
  `ServerState.OK`. (Tests: `TestLeavingLocalNamesTheAnsweringServer`, Task 2.)
- G1 must name `st.planWant.Mode` (the `a` snapshot), never the live choice,
  so the copy cannot drift from what `y` will plan. (Assert the gate names
  `native` when native was chosen before `a`, in
  `TestSetupTabGateBeforeReplacingRemote`, Task 3.)

---

### Before Task 1: commit the pending work

The mode-hint change, its test, `wt/docs/wt-model-server.md` and the two
`docs/superpowers/` documents are uncommitted. Commit them first so each task
below is its own commit.

- [ ] **Step 1: Commit**

```bash
git add wt/internal/modeleditor/setup_view.go wt/internal/modeleditor/setup_tab_test.go wt/docs/wt-model-server.md docs/superpowers/specs/2026-11-21-mode-conflict-guard-design.md docs/superpowers/plans/2026-11-21-mode-conflict-guard.md
git commit -m "feat(wt): point the Setup tab at wt config for remote; design and plan for mode-conflict guard"
```

---

### Task 1: `modelmode.ConfigMode`

**Files:**
- Modify: `wt/internal/modelmode/mode.go`
- Test: `wt/internal/modelmode/mode_test.go`

**Interfaces:**
- Consumes: `ResolveMode(storedMode, storedURL, storedTokenFile string, env func(string) string) Resolution`, `State.Config.Table config.ModelServerState{Mode, URL, TokenFile}`.
- Produces: `func ConfigMode(st State) Mode` — the mode config.toml holds,
  with `ResolveMode`'s inference (a stored url or token file means remote) and
  no env sources. Task 3 and Task 4 call it as `modelmode.ConfigMode(st.state)`.

- [ ] **Step 1: Write the failing test**

```go
// TestConfigMode pins ConfigMode: config.toml's mode, with the resolution's
// inference and no env — what either editor screen's apply would replace.
func TestConfigMode(t *testing.T) {
	cases := []struct {
		name string
		st   State
		want Mode
	}{
		{"stored remote", State{Config: ConfigState{Table: config.ModelServerState{Mode: "remote", URL: "https://gpu.example:8787"}}}, ModeRemote},
		{"stored local wins over a url", State{Config: ConfigState{Table: config.ModelServerState{Mode: "local", URL: "http://127.0.0.1:8787"}}}, ModeLocal},
		{"stored native", State{Config: ConfigState{Table: config.ModelServerState{Mode: "native"}}}, ModeNative},
		{"inferred remote from a url", State{Config: ConfigState{Table: config.ModelServerState{URL: "https://gpu.example:8787"}}}, ModeRemote},
		{"inferred remote from a token file", State{Config: ConfigState{Table: config.ModelServerState{TokenFile: "/tmp/t"}}}, ModeRemote},
		{"unset", State{}, ""},
	}
	for _, c := range cases {
		if got := ConfigMode(c.st); got != c.want {
			t.Errorf("%s: ConfigMode = %q, want %q", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/modelmode/ -run TestConfigMode -v`
Expected: FAIL with `undefined: ConfigMode`.

- [ ] **Step 3: Implement `ConfigMode(st State) Mode` in `wt/internal/modelmode/mode.go`**

Body delegates, so the inference lives in one place:

```go
return ResolveMode(st.Config.Table.Mode, st.Config.Table.URL, st.Config.Table.TokenFile, func(string) string { return "" }).Mode
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd wt && go test ./internal/modelmode/ -run TestConfigMode -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/modelmode/mode.go wt/internal/modelmode/mode_test.go
git commit -m "feat(wt): modelmode.ConfigMode reports the stored mode without env"
```

---

### Task 2: The leaving-local note names the answering server

**Files:**
- Modify: `wt/internal/modelmode/plan.go` (`leavingLocal`)
- Modify: `wt/docs/wt-config.md` (the Mode tab paragraph that quotes this note)
- Test: `wt/internal/modelmode/plan_test.go`

**Interfaces:**
- Consumes: `State.Server ServerState{URL string; Checked, OK bool}`; fixtures `freshState()`, `localState()`.
- Produces: nothing new — the note string in `Plan.Notes`.

- [ ] **Step 1: Write the failing test**

With `localState()` and a remote Desired (mirror the fixture shape of the
existing remote-plan tests; `PastedToken` is the pasted token's value):

```go
// TestLeavingLocalNamesTheAnsweringServer pins the strengthened note L1 when
// the local server answers, and the unchanged short note when it does not.
func TestLeavingLocalNamesTheAnsweringServer(t *testing.T) {
	st := localState()
	st.Server = ServerState{URL: "http://127.0.0.1:8787", Checked: true, OK: true}
	p, err := BuildPlan(st, Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(p.Notes, "\n")
	for _, want := range []string{
		"wt-model's LaunchAgent is untouched",
		"the local server answers at http://127.0.0.1:8787 and will be unused",
		"next: stop it with x on `wt-model setup`",
		"apply native there to remove the LaunchAgent",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("answering server: notes %q lack %q", text, want)
		}
	}

	st.Server = ServerState{URL: "http://127.0.0.1:8787", Checked: true, OK: false}
	p, err = BuildPlan(st, Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	text = strings.Join(p.Notes, "\n")
	if text != "wt-model's LaunchAgent is untouched; remove it with `wt-model setup`" {
		t.Errorf("silent server: notes = %q, want the short note unchanged", text)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/modelmode/ -run TestLeavingLocalNamesTheAnsweringServer -v`
Expected: FAIL: the notes hold the short note in the first case.

- [ ] **Step 3: Implement in `wt/internal/modelmode/plan.go`'s `leavingLocal`**

In the no-hook branch, when `st.Resolution.Mode == ModeLocal`: if
`st.Server.Checked && st.Server.OK`, append L1 (Task copy) instead of the
short note; otherwise the short note stays byte-for-byte. L1's `<url>` is
`st.Server.URL`. Update the function comment.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd wt && go test ./internal/modelmode/ -run TestLeavingLocalNamesTheAnsweringServer -v`
Expected: PASS.

- [ ] **Step 5: Update `wt/docs/wt-config.md`**

In the Mode tab paragraph ("Leaving local (for native or remote) from this tab
never touches wt-model's LaunchAgent…"), say that the plan's note names the
answering local server and the next step (stop it with `x`, or apply `native`
on `wt-model setup`), quoting L1 as the illustrative wording.

- [ ] **Step 6: Verify no other test pinned the old copy**

Run: `cd wt && go test ./internal/modelmode/ ./internal/sessioneditor/`
Expected: PASS (the sessioneditor pin uses a silent-server fixture; if it
fails, the fixture needs `Server` left unchecked, not the copy changed).

- [ ] **Step 7: Commit**

```bash
git add wt/internal/modelmode/plan.go wt/internal/modelmode/plan_test.go wt/docs/wt-config.md
git commit -m "feat(wt): leaving-local note names the answering server and the next step"
```

---

### Task 3: Setup tab gate 1 (replace remote) + standing note NA

**Files:**
- Modify: `wt/internal/modeleditor/setup_tab.go` (prompt kind, queueing, `updateSetupConfirm`, hints)
- Modify: `wt/internal/modeleditor/setup_view.go` (`setupPromptView`, `modeChoice` notes)
- Modify: `wt/docs/wt-model-server.md` (the `wt-model setup` section)
- Test: `wt/internal/modeleditor/setup_tab_test.go`

**Interfaces:**
- Consumes: `modelmode.ConfigMode` (Task 1); fixtures `remoteState()`,
  `fullState()`, `setupEditor`, `keys`, `flat`, `plain`, `setupMachine{states,
  plan: okPlan(), seq, applies}`.
- Produces: `promptReplaceRemote setupPrompt` (file-local; Task 4 adds a
  sibling kind). Queue order when both fire: `promptReplaceRemote` then the
  existing `promptRemoveAgent`.

- [ ] **Step 1: Write the failing tests**

```go
// TestSetupTabGateBeforeReplacingRemote pins G1: an apply that would replace
// remote's stored url and token asks first (default No), names the snapshot's
// mode and the next step; n aborts the whole flow with nothing planned, and y
// continues into the plan and its confirm. The mode named is the a snapshot's,
// even after the choice moves.
func TestSetupTabGateBeforeReplacingRemote(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{remoteState()}, plan: okPlan()}
	m := setupEditor(t, sm, 120, 50)
	m = keys(t, m, "a")
	if m.setup.phase != setupConfirm || m.setup.prompt != promptReplaceRemote {
		t.Fatalf("after a: phase = %d prompt = %d, want the replace-remote gate", m.setup.phase, m.setup.prompt)
	}
	v := flat(plain(m))
	for _, want := range []string{"configtoml says remote: applying local replaces its url and token", "next: to use that server again, set remote on"} {
		if !strings.Contains(v, want) {
			t.Errorf("gate copy lacks %q:\n%s", want, plain(m))
		}
	}
	m = keys(t, m, "y", "y") // gate, then Apply this plan?
	if sm.applies != 1 {
		t.Errorf("y then y: applies = %d, want 1", sm.applies)
	}

	// The gate names the snapshot's mode: native chosen before a.
	sm = &setupMachine{states: []modelsetup.State{remoteState()}, plan: okPlan()}
	m = setupEditor(t, sm, 120, 50)
	m = keys(t, m, "right", "a")
	if v := flat(plain(m)); !strings.Contains(v, "applying native replaces its url and token") {
		t.Errorf("native snapshot:\n%s", plain(m))
	}

	sm = &setupMachine{states: []modelsetup.State{remoteState()}, plan: okPlan()}
	m = setupEditor(t, sm, 120, 50)
	m = keys(t, m, "a", "n")
	if m.setup.phase != setupView || m.setup.status != "not applied; nothing was changed" {
		t.Errorf("n: phase = %d status = %q", m.setup.phase, m.setup.status)
	}
	if strings.Contains(strings.Join(sm.seq, ","), "plan") || sm.applies != 0 {
		t.Errorf("n: dep calls = %v, want no plan and no apply", sm.seq)
	}
}

// TestSetupTabGateOnlyForPlansThatReplaceRemote pins the non-goals: no gate
// where the stored mode is not remote, and none for t (tighten), which writes
// no config step.
func TestSetupTabGateOnlyForPlansThatReplaceRemote(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan()}
	m := setupEditor(t, sm, 120, 50)
	m = keys(t, m, "a")
	if m.setup.prompt != promptApply {
		t.Errorf("stored local: prompt = %d, want the plan's confirm directly", m.setup.prompt)
	}
	sm = &setupMachine{states: []modelsetup.State{remoteState()}, plan: okPlan()}
	m = setupEditor(t, sm, 120, 50)
	m = keys(t, m, "t")
	if m.setup.prompt != promptApply {
		t.Errorf("tighten: prompt = %d, want the plan's confirm directly", m.setup.prompt)
	}
}

// TestSetupTabNotesRemoteReplacement pins NA: while config.toml says remote,
// the choice names what applying it replaces.
func TestSetupTabNotesRemoteReplacement(t *testing.T) {
	m := setupEditor(t, &setupMachine{states: []modelsetup.State{remoteState()}}, 120, 50)
	if v := flat(plain(m)); !strings.Contains(v, "configtoml says remote: applying local replaces its url and token") {
		t.Errorf("local chosen:\n%s", plain(m))
	}
	m = keys(t, m, "right")
	if v := flat(plain(m)); !strings.Contains(v, "applying native replaces its url and token") {
		t.Errorf("native chosen:\n%s", plain(m))
	}
	m = setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, 120, 50)
	if v := flat(plain(m)); strings.Contains(v, "replaces its url and token") {
		t.Errorf("stored local must not note a replacement:\n%s", plain(m))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/modeleditor/ -run 'TestSetupTabGate|TestSetupTabNotesRemote' -v`
Expected: FAIL with `undefined: promptReplaceRemote` (and the note assertions).

- [ ] **Step 3: Implement in `wt/internal/modeleditor/setup_tab.go`**

- New kind `promptReplaceRemote` after `promptRemoveAgent` in the `setupPrompt` const block.
- In `applySetupObserved`, queue it before the remove-agent question:
  `if want.LiteLLM != modelsetup.LiteLLMTighten && modelmode.ConfigMode(s) == modelmode.ModeRemote { st.queue = append(st.queue, promptReplaceRemote) }`
  (the `queue` mechanism already asks before planning).
- In `updateSetupConfirm`, a `case promptReplaceRemote:` — `y` falls through
  to `nextSetupStep()` like `promptRemoveAgent`; `n`/`N` returns to the form
  with `st.queue, st.planWant = nil, modelsetup.Desired{}`, status G1N.
- In `setupHints`'s `setupConfirm` case, G1H for `promptReplaceRemote`.
- Guard: G1's `<mode>` is `st.planWant.Mode` at render time.

- [ ] **Step 4: Implement in `wt/internal/modeleditor/setup_view.go`**

- `setupPromptView`: a `case promptReplaceRemote:` building `text` = G1 with
  `<mode>` = `st.planWant.Mode`, `question` = G1Q, through `editorkit.PromptView` like `promptRemoveAgent`.
- `modeChoice`: append NA (with `<mode>` = `st.want.Mode`) as a note when
  `modelmode.ConfigMode(st.state) == modelmode.ModeRemote && st.want.Mode != ""`,
  after the existing `remotePointerNote`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd wt && go test ./internal/modeleditor/ -run 'TestSetupTabGate|TestSetupTabNotesRemote' -v`
Expected: PASS.

- [ ] **Step 6: Run the package**

Run: `cd wt && go test ./internal/modeleditor/`
Expected: PASS (hints, ASCII and secret pins included).

- [ ] **Step 7: Update `wt/docs/wt-model-server.md`**

In the `wt-model setup` section's mode bullet: an apply while the stored mode
is `remote` asks G1 first (quote G1/G1Q as illustrative wording) and NA shows
under the choice; `t` and applies that replace nothing extra ask only the
plan's confirm.

- [ ] **Step 8: Commit**

```bash
git add wt/internal/modeleditor/setup_tab.go wt/internal/modeleditor/setup_view.go wt/internal/modeleditor/setup_tab_test.go wt/docs/wt-model-server.md
git commit -m "feat(wt): confirm before a setup apply replaces remote's stored url and token"
```

---

### Task 4: Setup tab gate 2 (`s`/`R`) + standing note NB

**Files:**
- Modify: `wt/internal/modeleditor/setup_tab.go` (prompt kind, `setupTab.control`, the `s`/`R` handlers, `updateSetupConfirm`, hints)
- Modify: `wt/internal/modeleditor/setup_view.go` (`setupPromptView`, `modeChoice` notes)
- Modify: `wt/docs/wt-model-server.md` (the `wt-model setup` section's keys paragraph)
- Test: `wt/internal/modeleditor/setup_tab_test.go`

**Interfaces:**
- Consumes: `modelmode.ConfigMode` (Task 1), `promptReplaceRemote` sibling
  plumbing from Task 3, `modelsetup.ControlAction` (`ControlStart`,
  `ControlStop`, `ControlRestart`), `setupControlCmd`.
- Produces: `promptRunUnused setupPrompt`; field `control modelsetup.ControlAction`
  on `setupTab`, set when the gate opens and read when `y` lands.

- [ ] **Step 1: Write the failing tests**

Give `remoteState()` an agent for NB (`st.Agent = modelsetup.AgentState{Present: true, Managed: true, Job: modelsetup.LaunchdJob{Loaded: true, Running: true, PID: 1066}}`) where the test needs one.

```go
// TestSetupTabStartGateOnlyForStartAndRestart pins G2: s and R ask first
// (default No) when the stored mode is not local, y runs the control and n
// cancels in its own words; x is never gated.
func TestSetupTabStartGateOnlyForStartAndRestart(t *testing.T) {
	for action, keysIn := range map[string][]string{"start": {"s"}, "restart": {"R"}} {
		sm := &setupMachine{states: []modelsetup.State{remoteState()}}
		m := setupEditor(t, sm, 120, 50)
		m = keys(t, m, keysIn...)
		if m.setup.phase != setupConfirm || m.setup.prompt != promptRunUnused {
			t.Fatalf("%s: phase = %d prompt = %d, want the unused-server gate", action, m.setup.phase, m.setup.prompt)
		}
		v := flat(plain(m))
		for _, want := range []string{"the mode is remote: no session uses the local server", "next: stop it with x when done, or make the mode local on"} {
			if !strings.Contains(v, want) {
				t.Errorf("%s: gate copy lacks %q:\n%s", action, want, plain(m))
			}
		}
		sm = &setupMachine{states: []modelsetup.State{remoteState()}}
		m = setupEditor(t, sm, 120, 50)
		m = keys(t, m, append(keysIn, "n")...)
		if sm.controls != 0 || !strings.Contains(m.setup.status, "nothing was changed") {
			t.Errorf("%s n: controls = %d status = %q", action, sm.controls, m.setup.status)
		}
		sm = &setupMachine{states: []modelsetup.State{remoteState()}}
		m = setupEditor(t, sm, 120, 50)
		m = keys(t, m, append(keysIn, "y")...)
		if sm.controls != 1 {
			t.Errorf("%s y: controls = %d, want 1", action, sm.controls)
		}
	}

	sm := &setupMachine{states: []modelsetup.State{remoteState()}}
	m := setupEditor(t, sm, 120, 50)
	m = keys(t, m, "x")
	if sm.controls != 1 || m.setup.phase == setupConfirm {
		t.Errorf("x: controls = %d phase = %d, want stop ungated", sm.controls, m.setup.phase)
	}
	sm = &setupMachine{states: []modelsetup.State{fullState()}}
	m = setupEditor(t, sm, 120, 50)
	m = keys(t, m, "s")
	if sm.controls != 1 || m.setup.phase == setupConfirm {
		t.Errorf("stored local: s must not gate (controls = %d phase = %d)", sm.controls, m.setup.phase)
	}
}

// TestSetupTabNotesUnusedServer pins NB: a running local server is named as
// unused while the stored mode is remote or native, with its next step.
func TestSetupTabNotesUnusedServer(t *testing.T) {
	agent := modelsetup.AgentState{Present: true, Managed: true, Job: modelsetup.LaunchdJob{Loaded: true, Running: true, PID: 1066}}
	st := remoteState()
	st.Agent = agent
	m := setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 120, 50)
	if v := flat(plain(m)); !strings.Contains(v, "the local server runs but remote is in effect: nothing uses it") ||
		!strings.Contains(v, "next: press x to stop it") {
		t.Errorf("remote:\n%s", plain(m))
	}
	st = fullState() // stored local: the server is in use
	st.Agent = agent
	m = setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 120, 50)
	if v := flat(plain(m)); strings.Contains(v, "nothing uses it") {
		t.Errorf("stored local must not note an unused server:\n%s", plain(m))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/modeleditor/ -run 'TestSetupTabStartGate|TestSetupTabNotesUnused' -v`
Expected: FAIL with `undefined: promptRunUnused` (and the note assertions).

- [ ] **Step 3: Implement in `wt/internal/modeleditor/setup_tab.go`**

- New kind `promptRunUnused`; new field `control modelsetup.ControlAction` on `setupTab`.
- The `s`/`R` cases of `updateSetup`: run `setupControlBlocked()` first (its
  message and behavior unchanged); when clear and
  `modelmode.ConfigMode(st.state)` is `ModeRemote` or `ModeNative` (unset does
  not gate), set `st.control`, `st.prompt, st.phase = promptRunUnused, setupConfirm`
  and return; otherwise `setupControlCmd` as today. `x` is untouched.
- `updateSetupConfirm`, `case promptRunUnused:` — `y` runs
  `m.setupControlCmd(st.control)`; `n`/`N` returns to the form with status G2N
  for the action (`not started; nothing was changed` /
  `not restarted; nothing was changed`).
- `setupHints`'s `setupConfirm` case: G2H for `promptRunUnused`.

- [ ] **Step 4: Implement in `wt/internal/modeleditor/setup_view.go`**

- `setupPromptView`, `case promptRunUnused:` — text = G2 (`<mode>` =
  `modelmode.ConfigMode(st.state)`), question = G2Q per `st.control`, through
  `editorkit.PromptView`.
- `modeChoice`: append NB (`<mode>` = `modelmode.ConfigMode(st.state)`) when
  the stored mode is `ModeRemote` or `ModeNative` and
  `st.state.Agent.Job.Running || st.state.Agent.Job.Loaded`, after NA.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd wt && go test ./internal/modeleditor/ -run 'TestSetupTabStartGate|TestSetupTabNotesUnused' -v`
Expected: PASS.

- [ ] **Step 6: Run the whole module and the format/lint check**

Run: `cd wt && go test ./... && make -C wt check`
Expected: PASS (from `local-ai-setup/`, `make -C wt check`).

- [ ] **Step 7: Update `wt/docs/wt-model-server.md`**

In the keys paragraph: `s`/`R` ask G2 first while the stored mode is not
local (quote G2/G2Q as illustrative wording), `x` never asks; NB shows under
the choice while the local server runs unused.

- [ ] **Step 8: Commit**

```bash
git add wt/internal/modeleditor/setup_tab.go wt/internal/modeleditor/setup_view.go wt/internal/modeleditor/setup_tab_test.go wt/docs/wt-model-server.md
git commit -m "feat(wt): confirm before starting the local model server nothing uses"
```
