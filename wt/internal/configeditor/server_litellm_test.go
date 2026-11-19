package configeditor

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
)

const (
	tlPlist  = "/Users/dev/Library/LaunchAgents/local.litellm.proxy.plist"
	tlBackup = tlPlist + ".bak-20261118-101500"
)

// litellmLocalState is a local machine a real BuildPlan accepts for every
// LiteLLM action: wt-model installed, a managed LaunchAgent, and the LiteLLM
// plist as given. No key is in any field: State never holds one.
func litellmLocalState(p modelsetup.LiteLLMPlistState) modelsetup.State {
	return modelsetup.State{
		Platform: modelsetup.Platform{OK: true},
		State: modelmode.State{
			Resolution: modelclient.Resolution{Mode: modelclient.ModeLocal, Source: modelclient.SourceStored},
			Config:     modelsetup.ConfigState{Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true, Table: config.ModelServerState{Mode: "local"}},
			Token:      modelsetup.TokenState{Path: "/Users/dev/.config/wt-model/server.token", Exists: true},
			Host:       modelmode.HostState{Home: "/Users/dev", ConfigHome: "/Users/dev/.config"},
		},
		Tools:        modelsetup.ToolsState{Binary: "/Users/dev/.local/bin/wt-model", Path: "/usr/bin", UID: 501},
		Agent:        modelsetup.AgentState{PlistPath: "/Users/dev/Library/LaunchAgents/local.wt-model.serve.plist"},
		Listener:     modelsetup.ListenerState{Port: 8787},
		LiteLLMPlist: p,
	}
}

func xmlPlist() modelsetup.LiteLLMPlistState {
	return modelsetup.LiteLLMPlistState{Path: tlPlist, Present: true, Port: 4000, Mode: 0o644, HasMasterKey: true, HasSaltKey: true, HasDatabaseURL: true}
}

// installable is a state with no LiteLLM plist and LiteLLM installed: a create is possible.
func installable() modelsetup.State {
	st := litellmLocalState(modelsetup.LiteLLMPlistState{Path: tlPlist})
	st.LiteLLMInstall = modelsetup.LiteLLMInstallState{Binary: "/Users/dev/.local/bin/litellm", ConfigPath: "/Users/dev/.config/litellm/config.yaml",
		ConfigExists: true, Port: 4000, ListenerChecked: true}
	return st
}

var (
	optUse    = "use the plist's key"
	optRotate = "generate a new key"
	optCreate = "create the LiteLLM agent"
)

// TestServerTabMasterKeyMenuOffersOnlyWhatTheStateAllows pins the menu's
// rules: create only with no plist and LiteLLM installed, rotate only for a
// readable XML plist with a key that is not the salt-refusal case, use only
// when the plist has a key; each option the state rules out is not offered,
// and why (naming guide 14 where it helps) is shown in a dim line instead.
func TestServerTabMasterKeyMenuOffersOnlyWhatTheStateAllows(t *testing.T) {
	noBinary := installable()
	noBinary.LiteLLMInstall.Binary = ""
	noConfig := installable()
	noConfig.LiteLLMInstall.ConfigExists = false
	portBusy := installable()
	portBusy.LiteLLMInstall.ListenerPID = 777
	noSalt := xmlPlist()
	noSalt.HasSaltKey = false
	binary := modelsetup.LiteLLMPlistState{Path: tlPlist, Present: true, Binary: true, Mode: 0o600}
	unreadable := modelsetup.LiteLLMPlistState{Path: tlPlist, Present: true, Err: "cannot be parsed", Mode: 0o600}

	cases := []struct {
		name    string
		st      modelsetup.State
		offered []string
		not     []string
		reasons []string
	}{
		{"absent and installed", installable(), []string{optCreate}, []string{optUse, optRotate}, nil},
		{"absent, no binary", noBinary, nil, []string{optCreate, optUse, optRotate}, []string{"litellm binary", "14-litellm"}},
		{"absent, no config.yaml", noConfig, nil, []string{optCreate}, []string{"config.yaml", "14-litellm"}},
		{"absent, port taken", portBusy, nil, []string{optCreate}, []string{"port 4000", "pid 777", "14-litellm"}},
		{"present with key and salt", litellmLocalState(xmlPlist()), []string{optUse, optRotate}, []string{optCreate}, nil},
		{"database without salt", litellmLocalState(noSalt), []string{optUse}, []string{optRotate, optCreate}, []string{"LITELLM_SALT_KEY", "14-litellm"}},
		{"binary plist", litellmLocalState(binary), nil, []string{optUse, optRotate, optCreate}, []string{"plutil", "14-litellm"}},
		{"unreadable plist", litellmLocalState(unreadable), nil, []string{optUse, optRotate, optCreate}, []string{"cannot be read", "14-litellm"}},
	}
	for _, c := range cases {
		sm := &serverMachine{states: []modelsetup.State{c.st}}
		m := keys(t, serverEditor(t, sm, 120, 50), "k")
		if m.server.phase != serverMenu {
			t.Errorf("%s: k did not open the menu (phase %d, status %q)", c.name, m.server.phase, m.server.status)
			continue
		}
		view := flat(plain(m))
		for _, o := range c.offered {
			if !strings.Contains(view, flat(o)) {
				t.Errorf("%s: %q is not offered:\n%s", c.name, o, plain(m))
			}
		}
		for _, o := range c.not {
			if strings.Contains(view, flat(o)) {
				t.Errorf("%s: %q is offered, the state does not allow it:\n%s", c.name, o, plain(m))
			}
		}
		for _, r := range c.reasons {
			if !strings.Contains(view, flat(r)) {
				t.Errorf("%s: the reason lacks %q:\n%s", c.name, r, plain(m))
			}
		}
		if sm.plans != 0 || sm.applies != 0 {
			t.Errorf("%s: opening the menu planned or applied", c.name)
		}
	}
}

// pickOption opens the menu and picks the option at index i of those
// offered, by down and enter.
func pickOption(t *testing.T, m *model, i int) *model {
	t.Helper()
	m = keys(t, m, "k")
	for range i {
		m = keys(t, m, "down")
	}
	return keys(t, m, "enter")
}

// TestServerTabRotateShowsThePlanAndNeverTheKey pins that choosing a new key
// reads the machine again, plans with Desired.LiteLLM = rotate, and shows the
// real plan's text (the backup, the LITELLM_MASTER_KEY edit, the pair, the
// LiteLLM and wt-model restarts, what else may hold the old key) on the
// confirm screen; a master key typed into the form earlier and
// any fake key never appear, and no key reaches Plan.
func TestServerTabRotateShowsThePlanAndNeverTheKey(t *testing.T) {
	sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true}
	m := pickOption(t, serverEditor(t, sm, 120, 50), 1)
	if m.server.phase != serverConfirm || m.server.prompt != promptApply {
		t.Fatalf("phase %d prompt %d status %q, want the plan's confirm", m.server.phase, m.server.prompt, m.server.status)
	}
	if sm.observes != 2 || len(sm.desired) != 1 || sm.desired[0].LiteLLM != modelsetup.LiteLLMRotateKey {
		t.Fatalf("observes %d desired %v, want a fresh read and a rotate plan", sm.observes, sm.desired)
	}
	view := flat(plain(m))
	for _, w := range []string{"backup", "LITELLM_MASTER_KEY", "oldkey", "kickstart-k", "Apply this plan? [y/N]"} {
		if !strings.Contains(view, flat(w)) {
			t.Errorf("the confirm screen lacks %q:\n%s", w, plain(m))
		}
	}

	// A key typed into the master field of a keyless machine (where a create
	// is possible) is dropped from the plan and never drawn.
	st := installable()
	sm = &serverMachine{states: []modelsetup.State{st}, build: true}
	m = serverEditor(t, sm, 120, 50)
	m = keys(t, m, "down", "down", "down", "sk-FAKE-SECRET", "esc")
	m = pickOption(t, m, 0)
	if m.server.phase != serverConfirm || len(sm.desired) != 1 {
		t.Fatalf("create: phase %d status %q desired %d", m.server.phase, m.server.status, len(sm.desired))
	}
	if sm.desired[0].LiteLLM != modelsetup.LiteLLMCreate || sm.desired[0].MasterKey != "" {
		t.Errorf("create: Desired = LiteLLM %q, key set %v; want create and no key", sm.desired[0].LiteLLM, sm.desired[0].MasterKey != "")
	}
	if strings.Contains(plain(m), "FAKE-SECRET") {
		t.Errorf("the typed key is on the create confirm screen:\n%s", plain(m))
	}
	if !strings.Contains(flat(plain(m)), flat("DATABASE_URL")) {
		t.Errorf("the create plan does not say what the minimal plist omits:\n%s", plain(m))
	}
}

// TestServerTabRotateIsDefaultNo pins P1 on the LiteLLM confirm: only y or
// Y applies; any other key leaves the question up and applies nothing; n and
// esc cancel saying nothing was changed; ctrl+c quits.
func TestServerTabRotateIsDefaultNo(t *testing.T) {
	open := func() (*serverMachine, *model) {
		sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true}
		return sm, pickOption(t, serverEditor(t, sm, 80, 24), 1)
	}
	sm, m := open()
	for _, k := range []string{"x", "enter", " ", "q", "tab", "k", "a", "Y "} {
		m = keys(t, m, k)
		if m.server.phase != serverConfirm || sm.applies != 0 {
			t.Fatalf("after %q: phase %d applies %d, want the question to stay", k, m.server.phase, sm.applies)
		}
	}
	for _, k := range []string{"n", "N", "esc"} {
		sm, m = open()
		m = keys(t, m, k)
		if m.server.phase != serverView || sm.applies != 0 || !strings.Contains(m.server.status, "nothing was changed") {
			t.Errorf("%q: phase %d applies %d status %q, want a cancel", k, m.server.phase, sm.applies, m.server.status)
		}
	}
	sm, m = open()
	if _, cmd := m.Update(keyMsg("ctrl+c")); cmd == nil || sm.applies != 0 {
		t.Errorf("ctrl+c did not quit")
	}
	for _, k := range []string{"y", "Y"} {
		sm, m = open()
		m = keys(t, m, k)
		if sm.applies != 1 || !hasStep(sm.applied[0], modelsetup.StepMasterKey) {
			t.Errorf("%q: applies %d, want the rotation applied", k, sm.applies)
		}
	}
}

func hasStep(p modelsetup.Plan, k modelsetup.StepKind) bool {
	for _, s := range p.Steps {
		if s.Kind == k {
			return true
		}
	}
	return false
}

// TestServerTabLooseLiteLLMPlistShowsAWarning pins that a plist looser than
// 0600 shows a warning line with its mode and "tighten to 0600", that t is
// offered (in the hints) only then, and that t plans a chmod-only change
// through the same confirm; at 0600 there is no warning, no t hint, and t
// says nothing was changed and plans nothing.
func TestServerTabLooseLiteLLMPlistShowsAWarning(t *testing.T) {
	sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true}
	m := serverEditor(t, sm, 200, 50)
	view := plain(m)
	if !strings.Contains(view, "warning") || !strings.Contains(view, "0644") || !strings.Contains(view, "tighten to 0600") {
		t.Errorf("no warning with the mode and the fix:\n%s", view)
	}
	if h := hintLine(m); !strings.Contains(h, "t tighten") {
		t.Errorf("hints %q do not offer t", h)
	}
	m = keys(t, m, "t")
	if m.server.phase != serverConfirm || len(sm.applied) != 0 {
		t.Fatalf("t: phase %d status %q, want the confirm", m.server.phase, m.server.status)
	}
	if sm.desired[0].LiteLLM != modelsetup.LiteLLMTighten {
		t.Errorf("t planned %q", sm.desired[0].LiteLLM)
	}
	if !strings.Contains(flat(plain(m)), flat("mode 0600")) {
		t.Errorf("the confirm does not say the chmod:\n%s", plain(m))
	}
	m = keys(t, m, "y")
	if sm.applies != 1 || len(sm.applied[0].Steps) != 1 || sm.applied[0].Steps[0].Kind != modelsetup.StepChmod {
		t.Errorf("applied %v, want one chmod step", sm.applied)
	}

	tight := xmlPlist()
	tight.Mode = 0o600
	sm = &serverMachine{states: []modelsetup.State{litellmLocalState(tight)}, build: true}
	m = serverEditor(t, sm, 120, 50)
	if strings.Contains(plain(m), "tighten to 0600") || strings.Contains(hintLine(m), "t tighten") {
		t.Errorf("a 0600 plist shows the warning or the key:\n%s", plain(m))
	}
	m = keys(t, m, "t")
	if m.server.phase != serverView || sm.plans != 0 || !strings.Contains(m.server.status, "nothing was changed") {
		t.Errorf("t at 0600: phase %d plans %d status %q", m.server.phase, sm.plans, m.server.status)
	}
}

// TestServerTabLiteLLMKeysAreInertWhenTheyCannotAct pins that k and t do
// nothing, with a status, outside local mode, while the tab is busy, and
// when config.toml does not parse (naming the path and saying nothing was
// changed); none of them calls Plan or Apply.
func TestServerTabLiteLLMKeysAreInertWhenTheyCannotAct(t *testing.T) {
	loose := litellmLocalState(xmlPlist())
	native := loose
	native.Resolution.Mode = modelclient.ModeNative
	for name, c := range map[string]struct {
		st   modelsetup.State
		want string
	}{
		"native": {native, "local"},
		"parse":  {parseErrState(), "/Users/dev/.config/agent-wt/config.toml"},
	} {
		for _, k := range []string{"k", "t"} {
			sm := &serverMachine{states: []modelsetup.State{c.st}, build: true}
			m := keys(t, serverEditor(t, sm, 120, 50), k)
			if m.server.phase != serverView || sm.plans != 0 || sm.applies != 0 || !strings.Contains(m.server.status, c.want) ||
				!strings.Contains(m.server.status, "nothing was changed") {
				t.Errorf("%s %s: phase %d plans %d status %q", name, k, m.server.phase, sm.plans, m.server.status)
			}
		}
	}
	// Busy: a was pressed and its read is still out.
	sm := &serverMachine{states: []modelsetup.State{loose}, build: true}
	m := serverEditor(t, sm, 120, 50)
	next, _ := m.Update(keyMsg("a"))
	m = next.(*model)
	for _, k := range []string{"k", "t"} {
		next, cmd := m.Update(keyMsg(k))
		m = next.(*model)
		if cmd != nil || m.server.phase != serverView || !strings.Contains(m.server.status, "wait") {
			t.Errorf("busy %s: phase %d cmd %v status %q", k, m.server.phase, cmd != nil, m.server.status)
		}
	}
	// The menu's keys do nothing outside it: escape closes without a plan.
	sm = &serverMachine{states: []modelsetup.State{loose}, build: true}
	m = keys(t, serverEditor(t, sm, 80, 24), "k", "esc")
	if m.server.phase != serverView || sm.plans != 0 {
		t.Errorf("esc in the menu: phase %d plans %d", m.server.phase, sm.plans)
	}
}

// TestServerTabHintsNameKAndTOnlyWhenTheyAct pins T5 for the new keys: k only
// in local mode with a write allowed, t only for a loose plist as well.
func TestServerTabHintsNameKAndTOnlyWhenTheyAct(t *testing.T) {
	tight := xmlPlist()
	tight.Mode = 0o600
	native := litellmLocalState(xmlPlist())
	native.Resolution.Mode = modelclient.ModeNative
	for _, c := range []struct {
		name     string
		st       modelsetup.State
		k, tight bool
	}{
		{"local loose", litellmLocalState(xmlPlist()), true, true},
		{"local tight", litellmLocalState(tight), true, false},
		{"native", native, false, false},
		{"parse error", parseErrState(), false, false},
	} {
		h := hintLine(serverEditor(t, &serverMachine{states: []modelsetup.State{c.st}}, 200, 24))
		if got := strings.Contains(h, "k key"); got != c.k {
			t.Errorf("%s: hints %q, k named = %v, want %v", c.name, h, got, c.k)
		}
		if got := strings.Contains(h, "t tighten"); got != c.tight {
			t.Errorf("%s: hints %q, t named = %v, want %v", c.name, h, got, c.tight)
		}
	}
}

// failedRotate is the result of a rotation that stopped at the pair step:
// what Apply returns, with its backup note and the recovery commands.
func failedRotate() modelsetup.Result {
	step := modelsetup.Step{Kind: modelsetup.StepPair}
	return modelsetup.Result{
		Done:   []modelsetup.Step{{Kind: modelsetup.StepBackup}, {Kind: modelsetup.StepMasterKey}},
		Failed: &step, Err: errors.New("pair: disk full"),
		Notes: []string{"the old LiteLLM plist is kept at " + tlBackup + " (it holds the old master key)"},
		Recovery: "the plist " + tlPlist + " may hold a new master key while wt-model's pair and the running LiteLLM still use the old one: " +
			"put the old plist back with `cp -p " + tlBackup + " " + tlPlist + " && chmod 600 " + tlPlist + "` (nothing else was changed), then apply again",
	}
}

// TestServerTabShowsTheNotesAndRecoveryAfterAFailedRotate pins that a failed
// rotation shows its backup note and its recovery commands, whole and
// scrubbed, at every size including 40x12, where they scroll with up and
// down rather than being clipped: each command is on screen at some scroll
// position and nothing is cut mid-line; esc returns to the tab.
func TestServerTabShowsTheNotesAndRecoveryAfterAFailedRotate(t *testing.T) {
	for _, size := range tabSizes {
		sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true, result: failedRotate()}
		m := keys(t, pickOption(t, serverEditor(t, sm, size[0], size[1]), 1), "y")
		if m.server.result == nil {
			t.Fatalf("%dx%d: no result screen (status %q)", size[0], size[1], m.server.status)
		}
		// Collect the lines seen while scrolling to the end.
		seen := plain(m)
		assertFits(t, "recovery", m.View(), size[0], size[1])
		for range 40 {
			m = keys(t, m, "down")
			assertFits(t, "recovery", m.View(), size[0], size[1])
			seen += "\n" + plain(m)
		}
		flatSeen := flat(seen)
		for _, w := range []string{"cp-p" + tlBackup + tlPlist + "&&chmod600" + tlPlist, "thenapplyagain", tlBackup, "disk full"} {
			if !strings.Contains(flatSeen, flat(w)) {
				t.Errorf("%dx%d: %q never on screen while scrolling:\n%s", size[0], size[1], w, seen)
			}
		}
		if size[0] >= 120 && size[1] >= 24 && !strings.Contains(flat(plain(keys(t, m, "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up"))), flat("cp -p "+tlBackup)) {
			t.Errorf("%dx%d: the command is not on the first screen of a roomy terminal", size[0], size[1])
		}
		m = keys(t, m, "esc")
		if m.server.result != nil || m.server.phase != serverView {
			t.Errorf("%dx%d: esc did not leave the result screen", size[0], size[1])
		}
	}
}

// TestServerTabRecoveryIsOnTheFirstScreenWhenItFits pins that at 40x12 a
// short recovery command is on the first screen, before any scrolling.
func TestServerTabRecoveryIsOnTheFirstScreenWhenItFits(t *testing.T) {
	res := failedRotate()
	res.Recovery = "run `launchctl kickstart -k gui/501/local.wt-model.serve`"
	sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true, result: res}
	m := keys(t, pickOption(t, serverEditor(t, sm, 40, 12), 1), "y")
	if !strings.Contains(flat(plain(m)), flat("launchctl kickstart -k gui/501/local.wt-model.serve")) {
		t.Errorf("the recovery command is not on the first screen:\n%s", plain(m))
	}
	assertFits(t, "recovery", m.View(), 40, 12)
}

// TestServerTabShowsTheBackupNoteAfterASuccessfulRotate pins that a rotation
// that succeeded still shows where the backup is, at every size.
func TestServerTabShowsTheBackupNoteAfterASuccessfulRotate(t *testing.T) {
	ok := failedRotate()
	ok.Failed, ok.Err, ok.Recovery = nil, nil, ""
	for _, size := range tabSizes {
		sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true, result: ok}
		m := keys(t, pickOption(t, serverEditor(t, sm, size[0], size[1]), 1), "y")
		seen := plain(m)
		for range 20 {
			m = keys(t, m, "down")
			seen += "\n" + plain(m)
		}
		if !strings.Contains(flat(seen), flat(tlBackup)) || !strings.Contains(flat(seen), "applied") {
			t.Errorf("%dx%d: the backup note or the result is not shown:\n%s", size[0], size[1], seen)
		}
	}
}

// TestServerTabScrubsTheNotesAndRecovery pins that a url's user:password in
// a result's notes or recovery is never drawn, and that a fake key a careless
// dep put in a warning never reaches the rendered result screen either.
func TestServerTabScrubsTheNotesAndRecovery(t *testing.T) {
	res := failedRotate()
	res.Notes = []string{"see https://user:hunter2@host:4000/x"}
	res.Recovery = "run `curl https://user:hunter2@host:4000/health`"
	res.Warnings = []string{"LiteLLM said the key sk-warnkey0123456789abcdef0123456789abcdef was refused"}
	sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true, result: res}
	m := keys(t, pickOption(t, serverEditor(t, sm, 120, 50), 1), "y")
	if strings.Contains(plain(m), "hunter2") || !strings.Contains(plain(m), "host:4000") {
		t.Errorf("the result screen leaks or loses the url:\n%s", plain(m))
	}
	if strings.Contains(plain(m), "warnkey") {
		t.Errorf("a key in a warning reaches the result screen:\n%s", plain(m))
	}
}

// TestServerTabHoldsAQuitDuringALiteLLMApply pins that a quit asked for while
// a LiteLLM apply is out is held until its result is in, like every write.
func TestServerTabHoldsAQuitDuringALiteLLMApply(t *testing.T) {
	sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true, result: modelsetup.Result{}}
	m := pickOption(t, serverEditor(t, sm, 80, 24), 1)
	next, apply := m.Update(keyMsg("y"))
	m = next.(*model)
	if apply == nil || sm.applies != 0 || !m.server.writing {
		t.Fatalf("y: the apply was not handed to a command (writing %v)", m.server.writing)
	}
	next, cmd := m.Update(keyMsg("q"))
	m = next.(*model)
	if cmd != nil || !m.quitPending {
		t.Fatalf("q during the apply: cmd %v quitPending %v, want it held", cmd != nil, m.quitPending)
	}
	next, cmd = m.Update(apply())
	if cmd == nil {
		t.Fatal("the result: no command, want the held quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("the held quit was not issued")
	}
}

// TestServerTabFitsTheTerminalWithTheMenu pins that the menu (with all
// options, and with reasons only), and the result screens fit every size,
// keep the hints on the last line, and that the cursor option stays visible.
func TestServerTabFitsTheTerminalWithTheMenu(t *testing.T) {
	noBinary := installable()
	noBinary.LiteLLMInstall.Binary = ""
	phases := map[string]func(*testing.T, int, int) *model{
		"menu with options": func(t *testing.T, w, h int) *model {
			return keys(t, serverEditor(t, &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}}, w, h), "k")
		},
		"menu with a reason": func(t *testing.T, w, h int) *model {
			noSalt := xmlPlist()
			noSalt.HasSaltKey = false
			return keys(t, serverEditor(t, &serverMachine{states: []modelsetup.State{litellmLocalState(noSalt)}}, w, h), "k")
		},
		"menu with only a reason": func(t *testing.T, w, h int) *model {
			return keys(t, serverEditor(t, &serverMachine{states: []modelsetup.State{noBinary}}, w, h), "k")
		},
		"rotate confirm": func(t *testing.T, w, h int) *model {
			return pickOption(t, serverEditor(t, &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true}, w, h), 1)
		},
		"failed rotate": func(t *testing.T, w, h int) *model {
			sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true, result: failedRotate()}
			return keys(t, pickOption(t, serverEditor(t, sm, w, h), 1), "y")
		},
		"loose warning": func(t *testing.T, w, h int) *model {
			return serverEditor(t, &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}}, w, h)
		},
	}
	for name, open := range phases {
		for _, size := range tabSizes {
			m := open(t, size[0], size[1])
			assertFits(t, name, m.View(), size[0], size[1])
			if last := hintLine(m); !strings.Contains(last, "quit") {
				t.Errorf("%s at %dx%d: the last line is %q, want the hints", name, size[0], size[1], last)
			}
		}
	}
	// The options themselves survive the smallest terminal.
	m := phases["menu with options"](t, 40, 12)
	for _, o := range []string{optUse, optRotate} {
		if !strings.Contains(flat(plain(m)), flat(o)) {
			t.Errorf("40x12: %q is not on the menu:\n%s", o, plain(m))
		}
	}
}

// TestServerTabUseThePlistsKeyRePairsOverASetPair pins that choosing "use the
// plist's key" while wt-model's pair is already set reaches a confirm screen
// (the real plan) that says the pair is re-set from the plist, never showing
// a key.
func TestServerTabUseThePlistsKeyRePairsOverASetPair(t *testing.T) {
	st := litellmLocalState(xmlPlist())
	st.Pair = modelsetup.PairState{URL: "http://localhost:4000", KeySet: true}
	sm := &serverMachine{states: []modelsetup.State{st}, build: true}
	m := pickOption(t, serverEditor(t, sm, 120, 50), 0)
	if m.server.phase != serverConfirm || sm.desired[0].LiteLLM != modelsetup.LiteLLMUseExistingKey {
		t.Fatalf("phase %d status %q", m.server.phase, m.server.status)
	}
	view := flat(plain(m))
	if !strings.Contains(view, flat("key LITELLM_MASTER_KEY read from "+tlPlist)) || strings.Contains(view, flat("keep wt-model's LiteLLM pair")) {
		t.Errorf("the confirm does not name re-pairing from the plist:\n%s", plain(m))
	}
}

// TestServerTabHoldsAQuitUntilTheNotesAreRead pins that a quit held during a
// successful apply whose result carries notes is called off when the result
// lands, so the backup path can be read, and says how to quit.
func TestServerTabHoldsAQuitUntilTheNotesAreRead(t *testing.T) {
	ok := failedRotate()
	ok.Failed, ok.Err, ok.Recovery = nil, nil, ""
	sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true, result: ok}
	m := pickOption(t, serverEditor(t, sm, 80, 24), 1)
	next, apply := m.Update(keyMsg("y"))
	m = next.(*model)
	next, _ = m.Update(keyMsg("q"))
	m = next.(*model)
	if !m.quitPending {
		t.Fatal("the quit was not held")
	}
	next, cmd := m.Update(apply())
	m = next.(*model)
	if m.quitPending || m.tab != TabServer || m.server.result == nil {
		t.Errorf("quitPending %v tab %d result %v, want the quit called off with the notes showing", m.quitPending, m.tab, m.server.result != nil)
	}
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Errorf("the held quit was issued")
		}
	}
	if !strings.Contains(m.server.status, "press q to quit") {
		t.Errorf("status %q does not say how to quit", m.server.status)
	}
}

// TestServerTabMenuAgreesWithBuildPlan pins the menu against the real plan
// for every state of the menu test: an action the menu offers plans without
// a refusal, an action it hides is refused. Create is the one exception:
// the menu does not look at the plist path or at a port other than 4000
// (BuildPlan refuses those), so those states are not in the table.
func TestServerTabMenuAgreesWithBuildPlan(t *testing.T) {
	noBinary := installable()
	noBinary.LiteLLMInstall.Binary = ""
	noConfig := installable()
	noConfig.LiteLLMInstall.ConfigExists = false
	portBusy := installable()
	portBusy.LiteLLMInstall.ListenerPID = 777
	unchecked := installable()
	unchecked.LiteLLMInstall.ListenerChecked = false
	noSalt := xmlPlist()
	noSalt.HasSaltKey = false
	noKey := xmlPlist()
	noKey.HasMasterKey = false
	binary := modelsetup.LiteLLMPlistState{Path: tlPlist, Present: true, Binary: true, Mode: 0o600}
	unreadable := modelsetup.LiteLLMPlistState{Path: tlPlist, Present: true, Err: "cannot be parsed", Mode: 0o600}
	for name, st := range map[string]modelsetup.State{
		"installable": installable(), "no binary": noBinary, "no config": noConfig, "port busy": portBusy, "port unchecked": unchecked,
		"present": litellmLocalState(xmlPlist()), "no salt": litellmLocalState(noSalt), "no key": litellmLocalState(noKey),
		"binary": litellmLocalState(binary), "unreadable": litellmLocalState(unreadable),
	} {
		opts, _ := serverLiteLLMMenu(st, false)
		offered := map[modelsetup.LiteLLMAction]bool{}
		for _, o := range opts {
			offered[o.action] = true
		}
		for _, a := range []modelsetup.LiteLLMAction{modelsetup.LiteLLMUseExistingKey, modelsetup.LiteLLMRotateKey, modelsetup.LiteLLMCreate} {
			_, err := modelsetup.BuildPlan(st, modelsetup.Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: modelsetup.AdminWant{LiteLLM: a}})
			if a == modelsetup.LiteLLMUseExistingKey {
				// Use is a no-op plan, not a refusal, when the plist has no key
				// and the pair is set; it is offered only with a key.
				continue
			}
			if offered[a] && err != nil {
				t.Errorf("%s: %s is offered but the plan refuses: %v", name, a, err)
			}
			if !offered[a] && err == nil {
				t.Errorf("%s: %s is hidden but the plan accepts it", name, a)
			}
		}
	}
}

// TestServerTabResultScreenEatsCAndR pins that c and r do nothing while the
// result screen shows: a check or re-read would replace the status and the
// screen with it, and the Recovery commands and the backup note of a failed
// apply could not be brought back. The screen and its text stay, no read is
// asked for, and the hint line names esc/enter to close it and neither key;
// after esc, c reads again as usual.
func TestServerTabResultScreenEatsCAndR(t *testing.T) {
	sm := &serverMachine{states: []modelsetup.State{litellmLocalState(xmlPlist())}, build: true, result: failedRotate()}
	m := keys(t, pickOption(t, serverEditor(t, sm, 120, 50), 1), "y")
	if m.server.result == nil {
		t.Fatalf("no result screen (status %q)", m.server.status)
	}
	before := sm.observes
	for _, k := range []string{"c", "r"} {
		m = keys(t, m, k)
		if m.server.result == nil || !strings.Contains(m.server.result.Recovery, "cp -p "+tlBackup) {
			t.Fatalf("%s closed the result screen or lost its Recovery (status %q)", k, m.server.status)
		}
		if !strings.Contains(flat(plain(m)), flat("cp -p "+tlBackup)) || !strings.Contains(flat(plain(m)), flat(tlBackup+" (it holds")) {
			t.Errorf("%s: the Recovery or the backup note is no longer drawn:\n%s", k, plain(m))
		}
		if sm.observes != before {
			t.Errorf("%s: observes %d -> %d, want no read while the result shows", k, before, sm.observes)
		}
	}
	h := hintLine(m)
	if !strings.Contains(h, "esc/enter") || strings.Contains(h, "c check") || strings.Contains(h, "r re-read") {
		t.Errorf("result hints = %q, want esc/enter to close and no c or r", h)
	}
	m = keys(t, m, "esc", "c")
	if m.server.result != nil || sm.observes != before+1 {
		t.Errorf("esc then c: result %v, observes %d, want the screen closed and one read", m.server.result != nil, sm.observes-before)
	}
}

// TestServerTabReadsTheLiteLLMProbeWhenLocalIsChosen pins that choosing
// local with the mode keys, after an observation that skipped the LiteLLM
// probe (the first read of an unset or remote config does), reads the
// machine again with the probe, exactly as r would, so the k menu can offer
// create without the user first pressing r. Moving between native and remote
// asks for no read, and coming back to local after a probed read asks for
// none either. The fake answers like Observe: the litellm binary and the
// port check only when the probe is not skipped.
func TestServerTabReadsTheLiteLLMProbeWhenLocalIsChosen(t *testing.T) {
	base := installable()
	base.Resolution = modelclient.Resolution{}
	base.Config.Table.Mode = ""
	probed := 0
	sm := &serverMachine{}
	deps := sm.deps()
	observe := deps.Observe
	deps.Observe = func(ctx context.Context, o modelsetup.ObserveOptions) modelsetup.State {
		observe(ctx, o)
		st := base
		if o.SkipLiteLLMProbe {
			st.LiteLLMInstall = modelsetup.LiteLLMInstallState{ConfigPath: base.LiteLLMInstall.ConfigPath, ConfigExists: true}
		} else {
			probed++
		}
		return st
	}
	m := newModel(testTheme(), &config.Config{}, nil)
	scratchConfigHome(t)
	m.opts = Options{StartTab: TabServer, Server: deps}
	m.tab = TabServer
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})
	m = runCmd(t, m, m.Init())
	if sm.observes != 1 || probed != 0 {
		t.Fatalf("first read: observes %d probed %d, want one read without the probe", sm.observes, probed)
	}

	// Among native and remote: no read.
	for _, c := range []struct {
		key  string
		mode modelclient.Mode
	}{{"right", modelclient.ModeNative}, {"left", modelclient.ModeRemote}, {" ", modelclient.ModeNative}} {
		m = keys(t, m, c.key)
		if m.server.want.Mode != c.mode || sm.observes != 1 {
			t.Fatalf("%q: mode %q observes %d, want %q and no read", c.key, m.server.want.Mode, sm.observes, c.mode)
		}
	}
	next, cmd := m.Update(keyMsg("right")) // local
	m = next.(*model)
	if m.server.want.Mode != modelclient.ModeLocal || cmd == nil || !m.server.busy ||
		!strings.Contains(m.server.status, "re-reading the model server's state") {
		t.Fatalf("local: mode %q cmd %v busy %v status %q, want a re-read as r starts", m.server.want.Mode, cmd != nil, m.server.busy, m.server.status)
	}
	m = runCmd(t, m, cmd)
	if sm.observes != 2 || probed != 1 || sm.opts[1].SkipLiteLLMProbe {
		t.Fatalf("local: observes %d probed %d, want a second read with the probe", sm.observes, probed)
	}
	if v := flat(plain(keys(t, m, "k"))); !strings.Contains(v, flat(optCreate)) {
		t.Errorf("k after choosing local does not offer create:\n%s", plain(keys(t, m, "k")))
	}

	m = keys(t, m, "right", "left") // remote, then local again after a probed read
	if m.server.want.Mode != modelclient.ModeLocal || sm.observes != 2 {
		t.Errorf("remote and back: mode %q observes %d, want local and no new read", m.server.want.Mode, sm.observes)
	}
}

// TestServerTabModeKeysPassThroughLocalDuringItsProbeRead pins that going
// from native to remote, which passes over local, is not stopped halfway by
// the probe read the stop on local starts: the next mode key is let through
// while that read is out, and only that read.
func TestServerTabModeKeysPassThroughLocalDuringItsProbeRead(t *testing.T) {
	base := installable()
	base.Resolution = modelclient.Resolution{}
	base.Config.Table.Mode = ""
	sm := &serverMachine{}
	deps := sm.deps()
	observe := deps.Observe
	deps.Observe = func(ctx context.Context, o modelsetup.ObserveOptions) modelsetup.State {
		observe(ctx, o)
		return base
	}
	m := newModel(testTheme(), &config.Config{}, nil)
	scratchConfigHome(t)
	m.opts = Options{StartTab: TabServer, Server: deps}
	m.tab = TabServer
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})
	m = runCmd(t, m, m.Init())
	m = keys(t, m, "right")                // native
	next, cmd := m.Update(keyMsg("right")) // local: the probe read starts
	m = next.(*model)
	if !m.server.busy || cmd == nil {
		t.Fatalf("local: busy %v cmd %v, want a read out", m.server.busy, cmd != nil)
	}
	next, _ = m.Update(keyMsg("right")) // remote, while that read is out
	m = next.(*model)
	if m.server.want.Mode != modelclient.ModeRemote {
		t.Fatalf("mode %q, want remote: the key was held back (%q)", m.server.want.Mode, m.server.status)
	}
	next, _ = m.Update(keyMsg("enter")) // other keys still wait
	m = next.(*model)
	if !strings.Contains(m.server.status, "wait") {
		t.Errorf("enter during the probe read: status %q, want the wait line", m.server.status)
	}
}

// TestServerTabProbesOnFirstOpenOfAStoredLocalConfig pins that opening the
// tab on a config that already stores mode = "local" runs the LiteLLM probe
// in its one first read: that read is made before the mode is known, and no
// mode key moves the choice to local because it already is local, so a read
// that skipped the probe left the k menu saying no litellm binary was found
// when nobody had looked. The first read asks Observe to probe if the mode it
// resolves is local (the fake answers by the same rule), the tab still reads
// once, and k offers create. An unset or remote config is still not probed.
func TestServerTabProbesOnFirstOpenOfAStoredLocalConfig(t *testing.T) {
	for _, c := range []struct {
		name  string
		mode  modelclient.Mode
		probe bool
	}{{"local", modelclient.ModeLocal, true}, {"remote", modelclient.ModeRemote, false}, {"unset", "", false}} {
		base := installable() // no LiteLLM plist, litellm installed, port free
		base.Resolution.Mode = c.mode
		base.Config.Table.Mode = string(c.mode)
		probed := 0
		sm := &serverMachine{}
		deps := sm.deps()
		observe := deps.Observe
		deps.Observe = func(ctx context.Context, o modelsetup.ObserveOptions) modelsetup.State {
			observe(ctx, o)
			st := base
			if o.LiteLLMProbeSkipped(base.Resolution.Mode) {
				st.LiteLLMInstall = modelsetup.LiteLLMInstallState{ConfigPath: base.LiteLLMInstall.ConfigPath, ConfigExists: true}
			} else {
				probed++
			}
			return st
		}
		m := newModel(testTheme(), &config.Config{}, nil)
		scratchConfigHome(t)
		m.opts = Options{StartTab: TabServer, Server: deps}
		m.tab = TabServer
		m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})
		m = runCmd(t, m, m.Init())
		if m.server.want.Mode != c.mode || sm.observes != 1 || (probed == 1) != c.probe || m.server.probeSkipped == c.probe {
			t.Fatalf("%s: mode %q observes %d probed %d probeSkipped %v, want one read, probed %v",
				c.name, m.server.want.Mode, sm.observes, probed, m.server.probeSkipped, c.probe)
		}
		if !c.probe {
			continue
		}
		if v := flat(plain(keys(t, m, "k"))); !strings.Contains(v, flat(optCreate)) {
			t.Errorf("k on a stored local config does not offer create:\n%s", plain(keys(t, m, "k")))
		}
	}
}

// TestServerTabMenuSaysTheProbeWasNotRun pins the k menu's reason when the
// observation shown skipped the LiteLLM probe: the binary and the port were
// never looked at, so it says the probe was not run and names r, and never
// claims no litellm binary was found.
func TestServerTabMenuSaysTheProbeWasNotRun(t *testing.T) {
	st := installable()
	st.LiteLLMInstall = modelsetup.LiteLLMInstallState{ConfigPath: st.LiteLLMInstall.ConfigPath, ConfigExists: true}
	opts, reasons := serverLiteLLMMenu(st, true)
	for _, o := range opts {
		if o.action == modelsetup.LiteLLMCreate {
			t.Fatalf("create offered from a read that skipped the probe")
		}
	}
	all := strings.Join(reasons, "\n")
	if strings.Contains(all, "no litellm binary was found") || !strings.Contains(all, "the LiteLLM probe was not run; press r, then k again") {
		t.Errorf("reasons %q, want the probe named as not run, not a missing binary", all)
	}
	// A probed read with no binary still says so.
	st.LiteLLMInstall.ListenerChecked = true
	if _, reasons := serverLiteLLMMenu(st, false); !strings.Contains(strings.Join(reasons, "\n"), "no litellm binary was found") {
		t.Errorf("probed read without a binary: reasons %q, want the missing binary named", reasons)
	}
}
