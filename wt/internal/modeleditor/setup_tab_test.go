package modeleditor

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
)

// TestNextTabCycles pins the one tab order that every tab key uses:
// Models, Setup, back to Models.
func TestNextTabCycles(t *testing.T) {
	for from, want := range map[Tab]Tab{TabModels: TabSetup, TabSetup: TabModels} {
		if got := nextTab(from); got != want {
			t.Errorf("nextTab(%d) = %d, want %d", from, got, want)
		}
	}
}

// TestStartTabOpensTheSetupTab checks that Options.StartTab can open the
// Setup tab, that its view starts with the tab bar, and that tab leaves it
// for the Models tab, whose first visit probes the providers.
func TestStartTabOpensTheSetupTab(t *testing.T) {
	m := newModel(testTheme(), nil, nil)
	probes := 0
	models := quietModels()
	probe := models.Probe
	models.Probe = func(c *config.Config) localmodels.Snapshot { probes++; return probe(c) }
	m.opts, m.tab = Options{StartTab: TabSetup, Setup: (&setupMachine{}).deps(), Models: models}, TabSetup
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = runCmd(t, m, m.Init())
	if !strings.Contains(m.View(), "[Setup]") || probes != 0 {
		t.Fatalf("want the Setup tab open and no probe (probes = %d), view:\n%s", probes, m.View())
	}
	m = keys(t, m, "tab")
	if m.tab != TabModels || probes != 1 {
		t.Errorf("tab on Setup = %d probes = %d, want Models probed once", m.tab, probes)
	}
}

// quietModels are Models tab deps that read an empty config and probe
// nothing, for a test about the Setup tab that may pass through Models.
func quietModels() ModelsDeps {
	return ModelsDeps{
		Load:  func() (*config.Config, error) { return &config.Config{}, nil },
		Probe: func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} },
	}
}

// setupMachine is a throwaway machine for the Setup tab: a canned
// State per observation (the last one repeats), a canned plan, apply result
// and control error, and a record of every dep call, so a test can tell what
// the tab asked for and in which order.
type setupMachine struct {
	states   []modelsetup.State
	observes int
	opts     []modelsetup.ObserveOptions
	plans    int
	applies  int
	controls int
	// seq is every dep call in order: observe, plan, apply, control.
	seq []string
	// desired and planned are what each Plan call was given.
	desired []modelsetup.Desired
	planned []modelsetup.State
	// applied are the plans Apply ran; actions what Control was asked.
	applied []modelsetup.Plan
	actions []modelsetup.ControlAction
	// plan and planErr are what Plan returns (plan.Want is the Desired it
	// was given when plan has none); result and progress what Apply returns
	// and reports; controlErr what Control returns.
	plan    modelsetup.Plan
	planErr error
	// build makes Plan the real modelsetup.BuildPlan instead of the canned
	// plan, for a test about what a real plan's text holds.
	build      bool
	result     modelsetup.Result
	progress   []string
	controlErr error
}

func (sm *setupMachine) deps() SetupDeps {
	return SetupDeps{
		Observe: func(_ context.Context, o modelsetup.ObserveOptions) modelsetup.State {
			sm.observes++
			sm.seq = append(sm.seq, "observe")
			sm.opts = append(sm.opts, o)
			if len(sm.states) == 0 {
				return modelsetup.State{}
			}
			return sm.states[min(sm.observes, len(sm.states))-1]
		},
		Plan: func(st modelsetup.State, want modelsetup.Desired) (modelsetup.Plan, error) {
			sm.plans++
			sm.seq = append(sm.seq, "plan")
			sm.desired = append(sm.desired, want)
			sm.planned = append(sm.planned, st)
			if sm.build {
				return modelsetup.BuildPlan(st, want)
			}
			if sm.planErr != nil {
				return modelsetup.Plan{}, sm.planErr
			}
			p := sm.plan
			if p.Want.Mode == "" {
				p.Want = want.Desired
				p.Admin = want.AdminWant
			}
			return p, nil
		},
		Apply: func(_ context.Context, p modelsetup.Plan, progress func(string)) modelsetup.Result {
			sm.applies++
			sm.seq = append(sm.seq, "apply")
			sm.applied = append(sm.applied, p)
			for _, line := range sm.progress {
				progress(line)
			}
			return sm.result
		},
		Control: func(_ context.Context, a modelsetup.ControlAction) error {
			sm.controls++
			sm.seq = append(sm.seq, "control")
			sm.actions = append(sm.actions, a)
			return sm.controlErr
		},
	}
}

// scratchConfigHome points XDG_CONFIG_HOME at a temporary directory of this
// test (restored afterwards), so config.Path() names a file of the test's.
func scratchConfigHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// setupEditor is the editor in a terminal of the given size, opened on the
// Setup tab with Init run, so its first observation is delivered.
func setupEditor(t *testing.T, sm *setupMachine, width, height int) *model {
	t.Helper()
	m := newModel(testTheme(), &config.Config{}, nil)
	m.home = testUserHome
	scratchConfigHome(t)
	m.opts = Options{StartTab: TabSetup, Setup: sm.deps(), Models: quietModels()}
	m.tab = TabSetup
	m = send(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	return runCmd(t, m, m.Init())
}

// plain is the view with its colour taken out (L14).
func plain(m *model) string { return ansi.Strip(m.View()) }

// fullState is a local-mode machine with every part filled: the worst case
// for the tab's height.
func fullState() modelsetup.State {
	return modelsetup.State{
		Platform: modelsetup.Platform{OK: true},
		State: modelmode.State{
			Resolution:  modelmode.Resolution{Mode: modelmode.ModeLocal, Source: modelmode.SourceStored},
			EnvOverride: "WT_MODEL_URL, WT_MODEL_TOKEN_FILE",
			Config: modelsetup.ConfigState{
				Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true,
				Table:    config.ModelServerState{Mode: "local", URL: "http://127.0.0.1:8787", TokenFile: "~/.config/wt-model/server.token"},
				Problems: []string{"token under [model-server] is set; move it into a 0600 token_file"},
			},
			Token: modelsetup.TokenState{Path: "/Users/dev/.config/wt-model/server.token", Exists: true},
			Host:  modelmode.HostState{Home: "/Users/dev"},
			Server: modelsetup.ServerState{URL: "http://127.0.0.1:8787", Checked: true, Kind: modelsetup.KindUnauthorized,
				Message: "model server at 127.0.0.1:8787 refused the token (unauthorized) — check token_file under [model-server] in config.toml"},
		},
		Pair: modelsetup.PairState{URL: "http://127.0.0.1:4000", KeySet: true},
		Agent: modelsetup.AgentState{
			PlistPath: "/Users/dev/Library/LaunchAgents/local.wt-model.serve.plist", Present: true, Managed: true,
			Job: modelsetup.LaunchdJob{Loaded: true, Running: true, PID: 4123},
		},
		LiteLLM: modelsetup.LiteLLMState{
			Checked:      true,
			Reachable:    modelsetup.Check{Status: modelsetup.CheckOK},
			Database:     modelsetup.Check{Status: modelsetup.CheckWarn, Detail: "readiness says the database is not connected"},
			KeyAccepted:  modelsetup.Check{Status: modelsetup.CheckFail, Detail: "the proxy refused the pair's key (401)"},
			RoutesInSync: modelsetup.Check{Status: modelsetup.CheckOK, Detail: "12 routes in sync"},
			RestartOwed:  modelsetup.Check{Status: modelsetup.CheckSkipped, Detail: "proxy not running under launchd"},
			PortMatches:  modelsetup.Check{Status: modelsetup.CheckOK},
			PlistMode:    modelsetup.Check{Status: modelsetup.CheckWarn, Detail: "plist is mode 0644 and holds secrets (chmod 600)"},
		},
	}
}

// parseErrState is a machine whose config.toml does not parse.
func parseErrState() modelsetup.State {
	return modelsetup.State{
		Platform: modelsetup.Platform{OK: true},
		State: modelmode.State{
			Config: modelsetup.ConfigState{Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true,
				ParseErr: "toml: line 3 (last key \"model-server.mode\"): expected value but found \"=\" instead"},
		},
	}
}

// offMacState is a Linux machine: local cannot be set up there.
func offMacState() modelsetup.State {
	return modelsetup.State{Platform: modelsetup.Platform{Reason: "the local model server runs as a macOS LaunchAgent; this is linux"}}
}

// TestSetupTabObservesOnFirstVisitOnly pins that the tab reads the machine
// once when it is first shown (by tab or by StartTab) and not on later
// visits: a slow ping must not run on every pass through the tabs.
func TestSetupTabObservesOnFirstVisitOnly(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{fullState()}}
	m := newModel(testTheme(), &config.Config{}, nil)
	scratchConfigHome(t)
	m.opts = Options{Setup: sm.deps(), Models: quietModels()}
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = runCmd(t, m, m.Init())
	if sm.observes != 0 {
		t.Fatalf("observes on the Models tab = %d, want 0", sm.observes)
	}
	m = keys(t, m, "tab")
	if m.tab != TabSetup || sm.observes != 1 {
		t.Fatalf("tab = %d observes = %d, want the Setup tab observed once", m.tab, sm.observes)
	}
	m = keys(t, m, "tab", "tab")
	if m.tab != TabSetup || sm.observes != 1 {
		t.Errorf("tab = %d observes = %d after a second visit, want still 1", m.tab, sm.observes)
	}

	sm = &setupMachine{states: []modelsetup.State{fullState()}}
	m = setupEditor(t, sm, 80, 24)
	if sm.observes != 1 {
		t.Fatalf("observes with StartTab = TabSetup = %d, want 1 from Init", sm.observes)
	}
	m = keys(t, m, "tab", "tab")
	if sm.observes != 1 {
		t.Errorf("observes after coming back = %d, want 1", sm.observes)
	}
	m = keys(t, m, "c")
	if sm.observes != 2 {
		t.Errorf("observes after c = %d, want 2", sm.observes)
	}
	m = keys(t, m, "r")
	if sm.observes != 3 {
		t.Errorf("observes after r = %d, want 3", sm.observes)
	}
	if sm.plans+sm.applies+sm.controls != 0 {
		t.Errorf("the tab planned, applied or controlled: %d %d %d", sm.plans, sm.applies, sm.controls)
	}
}

// TestSetupTabShowsEachLiteLLMCheck pins one line per LiteLLM check, each
// with its status as a word (V2: colour only repeats it).
func TestSetupTabShowsEachLiteLLMCheck(t *testing.T) {
	v := flat(plain(setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, 120, 50)))
	for _, want := range []string{
		"reachableok", "databasewarning:readinesssays", "keyproblem:theproxyrefused", "routesok:12routes",
		"restartskipped:proxynotrunning", "portok", "plistmodewarning:plistismode0644",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
}

// TestSetupTabShowsAParseErrorWithThePath pins the parse-error state: the
// file, the error and the way out (fix it, press r) are on screen, and r
// reads the machine again.
func TestSetupTabShowsAParseErrorWithThePath(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{parseErrState(), fullState()}}
	m := setupEditor(t, sm, 80, 24)
	v := flat(plain(m))
	for _, want := range []string{"~/.config/agent-wt/config.toml", "line3", "fixthefile,thenpressr"} {
		if !strings.Contains(v, flat(want)) {
			t.Errorf("view lacks %q:\n%s", want, plain(m))
		}
	}
	// At every size: the status block gives way before the path and the way
	// out do.
	for _, size := range tabSizes {
		v := flat(plain(setupEditor(t, &setupMachine{states: []modelsetup.State{parseErrState()}}, size[0], size[1])))
		if !strings.Contains(v, "~/.config/agent-wt/config.toml") || !strings.Contains(v, "fixthefile,thenpressr") {
			t.Errorf("at %dx%d the parse error lacks the path or the way out: %s", size[0], size[1], v)
		}
	}
	m = keys(t, m, "r")
	if sm.observes != 2 || strings.Contains(plain(m), "then press r") {
		t.Errorf("r: observes = %d, want 2 and the error gone:\n%s", sm.observes, plain(m))
	}
}

// TestSetupTabDropsAStaleObservation pins that an observation older than
// the latest one asked for is dropped, in both orders of arrival: delivered
// after the newer one it does not overwrite it; delivered first it changes
// nothing and the tab stays busy until the newer one lands. A slow ping must
// not overwrite a later answer, nor end the wait for it.
func TestSetupTabDropsAStaleObservation(t *testing.T) {
	newerReason := offMacState().Platform.Reason

	// staleMsgs opens the tab on fullState and dispatches two observations:
	// the older reads parseErrState, the newer offMacState.
	staleMsgs := func() (*model, *setupMachine, tea.Msg, tea.Msg) {
		sm := &setupMachine{states: []modelsetup.State{fullState()}}
		m := setupEditor(t, sm, 80, 24)
		older := m.observeSetupCmd()
		newer := m.observeSetupCmd()
		sm.states = []modelsetup.State{fullState(), parseErrState(), offMacState()}
		return m, sm, older(), newer()
	}

	// Newer first, then the stale one.
	m, _, oldMsg, newMsg := staleMsgs()
	m = send(t, m, newMsg)
	m = send(t, m, oldMsg)
	if !m.setup.loaded || m.setup.state.Platform.Reason != newerReason || m.setup.state.Config.ParseErr != "" {
		t.Errorf("newer then older: loaded = %v state = %+v %+v, want the newer (off-macOS) observation",
			m.setup.loaded, m.setup.state.Platform, m.setup.state.Config)
	}
	if m.setup.busy {
		t.Errorf("newer then older: busy after the latest observation arrived")
	}

	// The stale one first: nothing changes and the tab still waits.
	m, _, oldMsg, newMsg = staleMsgs()
	m = send(t, m, oldMsg)
	if !m.setup.busy {
		t.Errorf("older first: busy = false, want true while the latest observation is out")
	}
	if st := m.setup.state; !st.Platform.OK || st.Resolution.Mode != modelmode.ModeLocal || st.Config.ParseErr != "" {
		t.Errorf("older first: state = %+v %+v, want fullState unchanged", st.Platform, st.Config)
	}
	m = send(t, m, newMsg)
	if m.setup.busy || !m.setup.loaded || m.setup.state.Platform.Reason != newerReason {
		t.Errorf("then the newer: busy = %v loaded = %v platform = %+v, want the newer observation applied",
			m.setup.busy, m.setup.loaded, m.setup.state.Platform)
	}
}

// TestSetupTabLocalDisabledOffMacOS pins that local is shown as unavailable
// with the reason off macOS, and that the mode keys pass over it: native is
// then the one choice either key reaches.
func TestSetupTabLocalDisabledOffMacOS(t *testing.T) {
	m := setupEditor(t, &setupMachine{states: []modelsetup.State{offMacState()}}, 80, 24)
	v := plain(m)
	if !strings.Contains(v, "local needs launchd (macOS)") || !strings.Contains(v, "(-) local") {
		t.Errorf("view:\n%s", v)
	}
	m = keys(t, m, "right")
	if m.setup.want.Mode != modelmode.ModeNative {
		t.Fatalf("right from unset = %q, want native (local skipped)", m.setup.want.Mode)
	}
	for _, k := range []string{"right", "left"} {
		m = keys(t, m, k)
		if m.setup.want.Mode != modelmode.ModeNative {
			t.Errorf("%s from native off macOS = %q, want native (local skipped)", k, m.setup.want.Mode)
		}
	}
}

// TestSetupTabFitsTheTerminal measures every state of the tab at every
// supported size (L13): nothing is taller or wider than the terminal, and
// the hint line is the last line.
func TestSetupTabFitsTheTerminal(t *testing.T) {
	long := fullState()
	long.Config.Problems = append(long.Config.Problems, strings.Repeat("a long problem that wraps ", 12))
	long.Token.Path = "/Users/dev/" + strings.Repeat("deeply-nested-directory/", 6) + "server.token"
	states := map[string]modelsetup.State{
		"empty": {}, "full": fullState(), "long": long, "parse error": parseErrState(), "off macOS": offMacState(),
	}
	for name, st := range states {
		for _, size := range tabSizes {
			m := setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, size[0], size[1])
			assertFits(t, name, m.View(), size[0], size[1])
			lines := strings.Split(plain(m), "\n")
			if last := lines[len(lines)-1]; !strings.Contains(last, "q quit") {
				t.Errorf("%s at %dx%d: the last line is %q, want the hints", name, size[0], size[1], last)
			}
		}
	}
	// While the first observation is out: the tab bar and a line, no more.
	m := newModel(testTheme(), &config.Config{}, nil)
	m.opts, m.tab = Options{Setup: (&setupMachine{}).deps(), Models: quietModels()}, TabSetup
	if v := m.setupTabView(); v != tabBar(testTheme(), TabSetup) {
		t.Errorf("before the first WindowSizeMsg the view is %q, want only the tab bar", v)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	m = send(t, m, loadedMsg{})
	assertFits(t, "loading", m.View(), 40, 12)
}

// TestCtrlSOnTheSetupTabDoesNothing pins that ctrl+s, a save key on other
// screens, neither observes nor plans nor applies on this tab: nothing is
// written until a plan is confirmed.
func TestCtrlSOnTheSetupTabDoesNothing(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{fullState()}}
	m := setupEditor(t, sm, 80, 24)
	next, cmd := m.Update(keyMsg("ctrl+s"))
	m = next.(*model)
	if cmd != nil || m.setup.busy || sm.observes != 1 || sm.plans != 0 || sm.applies != 0 {
		t.Errorf("ctrl+s: cmd = %v busy = %v observes = %d plans = %d applies = %d", cmd != nil, m.setup.busy, sm.observes, sm.plans, sm.applies)
	}
}

// TestSetupTabQuitKeysUseTheExistingQuit pins that q and ctrl+c leave
// through the editor's quit: straight out when nothing is pending (the
// screen has no unsaved edits to ask about); a held quit is
// TestSetupTabHoldsAQuitForAModelsWriteToo's.
func TestSetupTabQuitKeysUseTheExistingQuit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, 80, 24)
		_, cmd := m.Update(keyMsg(k))
		if cmd == nil {
			t.Fatalf("%s: no command, want tea.Quit", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s: command is not tea.Quit", k)
		}
	}
}

// TestSetupDepsWithDefaultsFillsOnlyNilFuncs pins that Run keeps a dep the
// caller injected and fills only the missing ones.
func TestSetupDepsWithDefaultsFillsOnlyNilFuncs(t *testing.T) {
	sm := &setupMachine{}
	d := SetupDeps{Observe: sm.deps().Observe}.withDefaults()
	if d.Plan == nil || d.Apply == nil || d.Control == nil {
		t.Fatalf("withDefaults left a nil func: %+v", d)
	}
	d.Observe(context.Background(), modelsetup.ObserveOptions{})
	if sm.observes != 1 {
		t.Errorf("the injected Observe was replaced")
	}
}

// TestSetupTabViewIsASCII pins V5: state is drawn in ASCII ((x), ( ), ok,
// warning:), never a check mark or a bullet; only the prose glyphs the style
// guide allows (· — …) may appear, and only in hints and prose.
func TestSetupTabViewIsASCII(t *testing.T) {
	for i, st := range []modelsetup.State{fullState(), parseErrState(), offMacState(), {}} {
		v := plain(setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 120, 50))
		// An anchor: the view is drawn (an empty one would pass the scan).
		if anchor := []string{"(x) local", "config.toml does not parse", "(-) local", "( ) native"}[i]; !strings.Contains(v, anchor) {
			t.Errorf("state %d: the view lacks %q, so the scan below proves nothing:\n%s", i, anchor, v)
		}
		for _, r := range v {
			if r > 127 && !strings.ContainsRune("·—…", r) {
				t.Errorf("non-ASCII %q in the view:\n%s", r, v)
				break
			}
		}
	}
}

// TestSetupTabNeverShowsASecret pins that no token, key, pasted secret or
// url password reaches the screen, even when a State or Desired a dep hands
// back carries one.
func TestSetupTabNeverShowsASecret(t *testing.T) {
	st := fullState()
	st.Config.Table.Token = "tok-INLINE-SECRET"
	st.Config.Table.URL = "https://me:pw-URL-SECRET@gpu.example:8787/"
	st.Server.Message = "model server at https://me:pw-MSG-SECRET@gpu.example:8787 is not reachable"
	for _, size := range [][2]int{{120, 50}, {40, 12}} {
		m := setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, size[0], size[1])
		m.setup.want.PastedToken = "tok-PASTED-SECRET"
		m.setup.want.MasterKey = "sk-MASTER-SECRET"
		v := plain(m)
		// Anchors: the url (without its password) and the mode choice are
		// drawn, so the absence of a secret below means something.
		if size[0] == 120 && (!strings.Contains(v, "(x) local") || !strings.Contains(v, "gpu.example:8787")) {
			t.Errorf("at %dx%d the view lacks the mode choice or the url:\n%s", size[0], size[1], v)
		}
		for _, secret := range []string{"INLINE-SECRET", "URL-SECRET", "MSG-SECRET", "PASTED-SECRET", "MASTER-SECRET", "me:pw"} {
			if strings.Contains(v, secret) {
				t.Errorf("at %dx%d the view shows %q:\n%s", size[0], size[1], secret, v)
			}
		}
	}
}

// remoteState is a machine stored in remote mode with no url or token yet
// and no LaunchAgent: where a user setting up a remote server starts.
func remoteState() modelsetup.State {
	return modelsetup.State{
		Platform: modelsetup.Platform{OK: true},
		State: modelmode.State{
			Resolution: modelmode.Resolution{Mode: modelmode.ModeRemote, Source: modelmode.SourceStored},
			Config: modelsetup.ConfigState{Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true,
				Table: config.ModelServerState{Mode: "remote"}},
			Host: modelmode.HostState{Home: "/Users/dev", ConfigHome: "/Users/dev/.config"},
		},
	}
}

// noKeyState is fullState with no LiteLLM master key anywhere: neither
// wt-model's pair nor the LiteLLM plist has one.
func noKeyState() modelsetup.State {
	st := fullState()
	st.Pair.KeySet = false
	st.LiteLLMPlist.HasMasterKey = false
	return st
}

// okPlan is a two-step plan for the confirm screen.
func okPlan() modelsetup.Plan {
	return modelsetup.Plan{Plan: modelmode.Plan{Steps: []modelsetup.Step{
		{Kind: modelsetup.StepToken, Detail: "create the token file /Users/dev/.config/wt-model/server.token"},
		{Kind: modelsetup.StepConfig, Detail: "write [model-server] in /Users/dev/.config/agent-wt/config.toml: mode = \"local\""},
	}}}
}

// hintLine is the view's last line: the key hints.
func hintLine(m *model) string {
	lines := strings.Split(plain(m), "\n")
	return lines[len(lines)-1]
}

// TestSetupTabAlwaysReObservesBeforePlanning pins that a reads the machine
// again, with the form's values, before it plans: after the listen port is
// edited, the observation looks at the new port, and Plan is given that
// fresh observation and the edited address. A plan from a stale read could
// bind a port that has since been taken.
func TestSetupTabAlwaysReObservesBeforePlanning(t *testing.T) {
	fresh := fullState()
	fresh.Listener.Port = 9999
	sm := &setupMachine{states: []modelsetup.State{fullState(), fresh}, plan: okPlan()}
	m := setupEditor(t, sm, 80, 24)
	m = keys(t, m, "down", "ctrl+u", "127.0.0.1:9999", "esc")
	m = keys(t, m, "a")
	if got := strings.Join(sm.seq, ","); got != "observe,observe,plan" {
		t.Fatalf("dep calls = %s, want observe,observe,plan", got)
	}
	if o := sm.opts[1]; o.ListenPort != 9999 {
		t.Errorf("the observation before planning looked at port %d, want 9999", o.ListenPort)
	}
	if sm.desired[0].Listen != "127.0.0.1:9999" || sm.planned[0].Listener.Port != 9999 {
		t.Errorf("Plan got Listen %q and a state with port %d, want the edited address and the fresh observation",
			sm.desired[0].Listen, sm.planned[0].Listener.Port)
	}
	if m.setup.phase != setupConfirm {
		t.Errorf("phase = %d, want the confirm screen", m.setup.phase)
	}
	// Every a reads again, even straight after the last one.
	m = keys(t, m, "n", "a")
	if got := strings.Join(sm.seq, ","); got != "observe,observe,plan,observe,plan" {
		t.Errorf("dep calls after a second a = %s", got)
	}
}

// TestSetupTabDisablesWritesOnAParseError pins that a config.toml that does
// not parse disables every write: a, s, x and R only say so, naming the file
// and that nothing was changed, and Plan, Apply and Control are never
// called. A parse error the observation before planning finds stops a too.
func TestSetupTabDisablesWritesOnAParseError(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{parseErrState()}}
	m := setupEditor(t, sm, 80, 24)
	for _, k := range []string{"a", "s", "x", "R"} {
		m = keys(t, m, k)
		if s := m.setup.status; !strings.Contains(s, "/Users/dev/.config/agent-wt/config.toml") || !strings.Contains(s, "nothing was changed") {
			t.Errorf("%s: status = %q, want the path and nothing was changed", k, s)
		}
		if !strings.Contains(flat(plain(m)), "nothingwaschanged") {
			t.Errorf("%s: the view does not say nothing was changed:\n%s", k, plain(m))
		}
	}
	if sm.plans+sm.applies+sm.controls != 0 || sm.observes != 1 {
		t.Errorf("plans %d applies %d controls %d observes %d, want no write path and no new read", sm.plans, sm.applies, sm.controls, sm.observes)
	}

	sm = &setupMachine{states: []modelsetup.State{fullState(), parseErrState()}}
	m = setupEditor(t, sm, 80, 24)
	m = keys(t, m, "a")
	if sm.plans != 0 || m.setup.phase != setupView || !strings.Contains(m.setup.status, "config.toml") || !strings.Contains(m.setup.status, "nothing was changed") {
		t.Errorf("a parse error found before planning: plans = %d phase = %d status = %q", sm.plans, m.setup.phase, m.setup.status)
	}
}

// TestSetupTabConfirmIsDefaultNo pins P1 on the plan's confirm screen: only
// y or Y applies; n, N and esc cancel; every other key, enter and q among
// them, is ignored and the prompt stays. ctrl+c quits.
func TestSetupTabConfirmIsDefaultNo(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan()}
	m := setupEditor(t, sm, 120, 50)
	m = keys(t, m, "a")
	v := plain(m)
	if m.setup.phase != setupConfirm || !strings.Contains(v, "Apply this plan? [y/N]") || !strings.Contains(v, "create the token file") {
		t.Fatalf("after a: phase = %d, want the plan and the question:\n%s", m.setup.phase, v)
	}
	for _, k := range []string{"enter", " ", "q", "a", "s", "x", "R", "r", "tab", "left", "down", "esc!", "j", "1"} {
		m = keys(t, m, k)
		if m.setup.phase != setupConfirm || m.tab != TabSetup || sm.applies != 0 {
			t.Fatalf("%q left the prompt: phase = %d tab = %d applies = %d", k, m.setup.phase, m.tab, sm.applies)
		}
	}
	for _, k := range []string{"n", "N", "esc"} {
		m = keys(t, m, k)
		if m.setup.phase != setupView || sm.applies != 0 || !strings.Contains(m.setup.status, "nothing was changed") {
			t.Errorf("%s: phase = %d applies = %d status = %q, want cancelled", k, m.setup.phase, sm.applies, m.setup.status)
		}
		m = keys(t, m, "a")
	}
	_, cmd := m.Update(keyMsg("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c on the confirm screen: no command, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok || sm.applies != 0 {
		t.Errorf("ctrl+c on the confirm screen did not quit, or applied")
	}
}

// TestSetupTabAppliesAfterY pins that y runs the confirmed plan through
// Apply, then says what ran (the progress lines and the outcome) and reads
// the machine again; and that a failure names the step and its error, and a
// wait that ran out names the url, how the ping failed and the log.
func TestSetupTabAppliesAfterY(t *testing.T) {
	for _, yes := range []string{"y", "Y"} {
		sm := &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan(),
			progress: []string{"step 1/2: token", "step 2/2: config"},
			result:   modelsetup.Result{Done: okPlan().Steps, Warnings: []string{"LiteLLM restart owed"}}}
		m := setupEditor(t, sm, 120, 50)
		m = keys(t, m, "a", yes)
		if sm.applies != 1 || len(sm.applied[0].Steps) != 2 || m.setup.phase != setupView || m.setup.busy || m.setup.writing {
			t.Fatalf("%s: applies = %d phase = %d busy = %v writing = %v", yes, sm.applies, m.setup.phase, m.setup.busy, m.setup.writing)
		}
		if got := strings.Join(sm.seq, ","); got != "observe,observe,plan,apply,observe" {
			t.Errorf("%s: dep calls = %s, want a re-read after the apply", yes, got)
		}
		v := flat(plain(m))
		for _, want := range []string{"step2/2:config", "applied", "LiteLLMrestartowed"} {
			if !strings.Contains(v, want) {
				t.Errorf("%s: the view lacks %q:\n%s", yes, want, plain(m))
			}
		}
	}

	wait := modelsetup.Step{Kind: modelsetup.StepWait}
	sm := &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan(), result: modelsetup.Result{
		Done: okPlan().Steps[:1], Failed: &wait,
		Err: &modelsetup.WaitError{URL: "http://127.0.0.1:8787", Kind: modelsetup.KindUnauthorized, Message: "401", Log: "/Users/dev/.wt-model.err.log"}}}
	m := setupEditor(t, sm, 120, 50)
	m = keys(t, m, "a", "y")
	for _, want := range []string{"http://127.0.0.1:8787", "token refused", "/Users/dev/.wt-model.err.log", "token"} {
		if !strings.Contains(m.setup.status, want) {
			t.Errorf("a wait that ran out: status lacks %q: %q", want, m.setup.status)
		}
	}

	plist := modelsetup.Step{Kind: modelsetup.StepPlist}
	sm = &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan(),
		result: modelsetup.Result{Failed: &plist, Err: errors.New("permission denied")}}
	m = setupEditor(t, sm, 120, 50)
	m = keys(t, m, "a", "y")
	if s := m.setup.status; !strings.Contains(s, "plist") || !strings.Contains(s, "permission denied") || strings.Contains(s, "applied:") {
		t.Errorf("a failed step: status = %q", s)
	}
}

// TestSetupTabRefusalIsShownVerbatim pins that a plan Plan refuses is shown
// as its own words, every reason, and that nothing is applied.
func TestSetupTabRefusalIsShownVerbatim(t *testing.T) {
	ref := &modelsetup.RefusalError{Reasons: []string{
		"port 8787 is in use by pid 77, which is not wt's LaunchAgent; stop that process or choose another port; nothing was changed",
		"token file /x is readable by others; run chmod 600 /x; nothing was changed",
	}}
	sm := &setupMachine{states: []modelsetup.State{fullState()}, planErr: ref}
	m := setupEditor(t, sm, 120, 50)
	m = keys(t, m, "a")
	if m.setup.status != ref.Error() {
		t.Errorf("status = %q, want the refusal verbatim %q", m.setup.status, ref.Error())
	}
	if m.setup.phase != setupView || sm.applies != 0 {
		t.Errorf("phase = %d applies = %d, want the tab and nothing applied", m.setup.phase, sm.applies)
	}
	v := flat(plain(m))
	for _, r := range ref.Reasons {
		if !strings.Contains(v, flat(r)) {
			t.Errorf("the view lacks %q:\n%s", r, plain(m))
		}
	}
}

// TestSetupTabAsksForTheMasterKeyWhenNeitherPairNorPlistHasOne pins that the
// masked master-key input is offered in local mode only when neither
// wt-model's pair nor the LiteLLM plist holds a key, and that what is typed
// there reaches Plan as Desired.MasterKey.
func TestSetupTabAsksForTheMasterKeyWhenNeitherPairNorPlistHasOne(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{noKeyState()}}
	m := setupEditor(t, sm, 120, 50)
	if !strings.Contains(plain(m), "master") {
		t.Fatalf("no master-key input with no key anywhere:\n%s", plain(m))
	}
	m = keys(t, m, "down", "down", "down", "sk-MASTER-1", "esc", "a")
	if sm.plans != 1 || sm.desired[0].MasterKey.Reveal() != "sk-MASTER-1" {
		t.Errorf("plans = %d, want the typed master key in Desired", sm.plans)
	}

	plistKey := noKeyState()
	plistKey.LiteLLMPlist.HasMasterKey = true
	pairKey := noKeyState()
	pairKey.Pair.KeySet = true
	native := noKeyState()
	native.Resolution.Mode = modelmode.ModeNative
	for name, st := range map[string]modelsetup.State{"plist key": plistKey, "pair key": pairKey, "native": native} {
		m := setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 120, 50)
		if strings.Contains(plain(m), "master") {
			t.Errorf("%s: the master-key input is offered:\n%s", name, plain(m))
		}
	}
	// A key typed while it was asked for is not sent once it is not.
	sm = &setupMachine{states: []modelsetup.State{noKeyState(), pairKey}}
	m = setupEditor(t, sm, 120, 50)
	m = keys(t, m, "down", "down", "down", "sk-MASTER-1", "esc", "a")
	if sm.plans != 1 || sm.desired[0].MasterKey != "" {
		t.Errorf("the pair has a key by now: Desired.MasterKey set = %v", sm.desired[0].MasterKey != "")
	}
}

// TestSetupTabLeavingLocalAsksAboutRemovingTheAgentDefaultNo pins the
// question asked when the choice leaves local with wt's LaunchAgent
// installed: "Remove the LaunchAgent? [y/N]", where only y sets RemoveAgent,
// n keeps it, esc plans nothing and other keys are ignored. Without a
// managed agent nothing is asked.
func TestSetupTabLeavingLocalAsksAboutRemovingTheAgentDefaultNo(t *testing.T) {
	for answer, want := range map[string]bool{"n": false, "N": false, "y": true, "Y": true} {
		sm := &setupMachine{states: []modelsetup.State{fullState()}}
		m := setupEditor(t, sm, 120, 50)
		m = keys(t, m, "left", "a")
		if !strings.Contains(plain(m), "Remove the LaunchAgent? [y/N]") || sm.plans != 0 {
			t.Fatalf("%s: want the question before any plan (plans = %d):\n%s", answer, sm.plans, plain(m))
		}
		m = keys(t, m, "enter", "q", "x", "a")
		if sm.plans != 0 || !strings.Contains(plain(m), "Remove the LaunchAgent? [y/N]") {
			t.Fatalf("%s: another key answered the question", answer)
		}
		m = keys(t, m, answer)
		if sm.plans != 1 || sm.desired[0].Mode != modelmode.ModeNative || sm.desired[0].RemoveAgent != want {
			t.Errorf("%s: plans = %d desired = %+v, want native with RemoveAgent %v", answer, sm.plans, sm.desired, want)
		}
	}

	sm := &setupMachine{states: []modelsetup.State{fullState()}}
	m := setupEditor(t, sm, 120, 50)
	m = keys(t, m, "left", "a", "esc")
	if sm.plans != 0 || !strings.Contains(m.setup.status, "nothing was changed") {
		t.Errorf("esc: plans = %d status = %q", sm.plans, m.setup.status)
	}

	noAgent := fullState()
	noAgent.Agent = modelsetup.AgentState{}
	unmanaged := fullState()
	unmanaged.Agent.Managed = false
	for name, st := range map[string]modelsetup.State{"no agent": noAgent, "not wt's": unmanaged} {
		sm := &setupMachine{states: []modelsetup.State{st}}
		m := setupEditor(t, sm, 120, 50)
		m = keys(t, m, "left", "a")
		if sm.plans != 1 || strings.Contains(plain(m), "Remove the LaunchAgent") {
			t.Errorf("%s: plans = %d, want a plan without the question", name, sm.plans)
		}
	}
	// Staying local asks nothing.
	sm = &setupMachine{states: []modelsetup.State{fullState()}}
	m = setupEditor(t, sm, 120, 50)
	m = keys(t, m, "a")
	if sm.plans != 1 {
		t.Errorf("staying local: plans = %d, want 1 without a question", sm.plans)
	}
}

// TestSetupTabQuitWaitsForAnApplyInFlight pins that a quit asked for while
// an apply (or a start/stop) is out is held until its result is in, so the
// result is never lost; that it then quits with the result kept; and that a
// failed apply calls the quit off on this tab, where the failure is drawn.
// Mirrors TestQuitWaitsForARegistryWriteInFlight.
func TestSetupTabQuitWaitsForAnApplyInFlight(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan(), result: modelsetup.Result{Done: okPlan().Steps}}
	m := setupEditor(t, sm, 80, 24)
	m = keys(t, m, "a")
	next, apply := m.Update(keyMsg("y"))
	m = next.(*model)
	if apply == nil || sm.applies != 0 || !m.setup.writing {
		t.Fatalf("y: cmd = %v applies = %d writing = %v, want the apply handed to a command", apply != nil, sm.applies, m.setup.writing)
	}
	m = keys(t, m, "tab")
	next, cmd := m.Update(keyMsg("q"))
	m = next.(*model)
	if cmd != nil || !m.quitPending || m.tab != TabSetup {
		t.Fatalf("q during the apply: cmd = %v quitPending = %v tab = %d, want the quit held on the Setup tab", cmd != nil, m.quitPending, m.tab)
	}
	if !strings.Contains(flat(plain(m)), "quit") || !strings.Contains(flat(plain(m)), "apply") {
		t.Errorf("the tab does not say the quit is waiting for the apply:\n%s", plain(m))
	}
	next, cmd = m.Update(apply())
	m = next.(*model)
	if cmd == nil {
		t.Fatal("the apply's result: no command, want the held quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("the held quit was not issued")
	}
	if !strings.Contains(m.setup.status, "applied") {
		t.Errorf("status = %q, want the result kept", m.setup.status)
	}

	// A failed apply: the quit is called off and the failure is shown here.
	plist := modelsetup.Step{Kind: modelsetup.StepPlist}
	sm = &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan(), result: modelsetup.Result{Failed: &plist, Err: errors.New("disk full")}}
	m = setupEditor(t, sm, 80, 24)
	m = keys(t, m, "a")
	next, apply = m.Update(keyMsg("y"))
	m = next.(*model)
	m = keys(t, m, "tab")
	next, _ = m.Update(keyMsg("q"))
	m = next.(*model)
	m = send(t, m, apply())
	if m.quitPending || m.tab != TabSetup || !strings.Contains(m.setup.status, "disk full") {
		t.Errorf("a failed apply: quitPending = %v tab = %d status = %q", m.quitPending, m.tab, m.setup.status)
	}

	// A start in flight holds the quit the same way.
	sm = &setupMachine{states: []modelsetup.State{fullState()}}
	m = setupEditor(t, sm, 80, 24)
	next, start := m.Update(keyMsg("s"))
	m = next.(*model)
	next, cmd = m.Update(keyMsg("ctrl+c"))
	m = next.(*model)
	if start == nil || cmd != nil || !m.quitPending {
		t.Fatalf("ctrl+c during a start: quitPending = %v, want held", m.quitPending)
	}
	_, cmd = m.Update(start())
	if cmd == nil {
		t.Fatal("the start's result: no command, want the held quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("the held quit was not issued after the start")
	}
}

// TestSetupTabMasksThePastedTokenAndTheKey pins that the master key is
// drawn masked while typed, never appears on the confirm screen or in a
// status, reaches Apply through the plan, and is cleared from its input once
// an apply succeeded. (The pasted token is remote's, set in `wt config`.)
func TestSetupTabMasksThePastedTokenAndTheKey(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{noKeyState()}}
	m := setupEditor(t, sm, 120, 50)
	m = keys(t, m, "down", "down", "down", "sk-MASTER-SECRET")
	if v := plain(m); strings.Contains(v, "MASTER-SECRET") || !strings.Contains(v, "*****") {
		t.Errorf("while typed the master key shows or is not masked:\n%s", v)
	}
	m = keys(t, m, "esc", "a")
	if strings.Contains(plain(m), "MASTER-SECRET") {
		t.Errorf("the confirm screen shows the master key")
	}
	m = keys(t, m, "y")
	if sm.applied[0].Admin.MasterKey.Reveal() != "sk-MASTER-SECRET" {
		t.Errorf("the master key did not reach Apply")
	}
	if m.setup.fields[sfMaster].Value() != "" || strings.Contains(plain(m), "MASTER-SECRET") {
		t.Errorf("the master key is kept or shown after a successful apply")
	}
}

// TestSetupTabStartStopRestartCallControl pins s, x and R: each runs
// Control with its action in a command and reads the machine again, a
// failure is said, and outside local mode they only say why they do
// nothing.
func TestSetupTabStartStopRestartCallControl(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{fullState()}}
	m := setupEditor(t, sm, 120, 50)
	for _, k := range []string{"s", "x", "R"} {
		m = keys(t, m, k)
		if m.setup.status == "" || m.setup.busy || m.setup.writing {
			t.Errorf("%s: status = %q busy = %v writing = %v", k, m.setup.status, m.setup.busy, m.setup.writing)
		}
	}
	want := []modelsetup.ControlAction{modelsetup.ControlStart, modelsetup.ControlStop, modelsetup.ControlRestart}
	if len(sm.actions) != 3 || sm.actions[0] != want[0] || sm.actions[1] != want[1] || sm.actions[2] != want[2] {
		t.Errorf("actions = %v, want start, stop, restart", sm.actions)
	}
	if got := strings.Join(sm.seq, ","); got != "observe,control,observe,control,observe,control,observe" {
		t.Errorf("dep calls = %s, want a re-read after each", got)
	}
	sm.controlErr = errors.New("launchctl bootstrap: exit status 5")
	m = keys(t, m, "s")
	if !strings.Contains(m.setup.status, "exit status 5") {
		t.Errorf("a failed start: status = %q", m.setup.status)
	}

	native := fullState()
	native.Resolution.Mode = modelmode.ModeNative
	for name, st := range map[string]modelsetup.State{"native": native, "off macOS": func() modelsetup.State {
		st := offMacState()
		st.Resolution = modelmode.Resolution{Mode: modelmode.ModeLocal, Source: modelmode.SourceStored}
		return st
	}()} {
		sm := &setupMachine{states: []modelsetup.State{st}}
		m := setupEditor(t, sm, 120, 50)
		for _, k := range []string{"s", "x", "R"} {
			m = keys(t, m, k)
			if !strings.Contains(m.setup.status, "local") {
				t.Errorf("%s %s: status = %q, want why it does nothing", name, k, m.setup.status)
			}
		}
		if sm.controls != 0 {
			t.Errorf("%s: controls = %d, want none", name, sm.controls)
		}
	}
}

// TestSetupTabFitsTheTerminalInEveryPhase measures every phase of the tab
// (the status view, a remote machine's view, local's fields, the confirm
// screen with a long plan, the question, an apply in flight and a long
// result) at every
// supported size: nothing taller or wider than the terminal, and the hints
// on the last line.
func TestSetupTabFitsTheTerminalInEveryPhase(t *testing.T) {
	long := modelsetup.Plan{}
	for i := 0; i < 9; i++ {
		long.Steps = append(long.Steps, modelsetup.Step{Kind: modelsetup.StepPlist,
			Detail: "write wt's LaunchAgent plist /Users/dev/Library/LaunchAgents/local.wt-model.serve.plist: /Users/dev/.local/bin/wt-model serve --listen 127.0.0.1:8787"})
	}
	long.Notes = []string{"the environment sets WT_MODEL_URL, which overrides what is stored here"}
	failed := modelsetup.Step{Kind: modelsetup.StepWait}
	result := modelsetup.Result{Failed: &failed, Warnings: []string{strings.Repeat("a long warning ", 8), "another"},
		Err: &modelsetup.WaitError{URL: "http://127.0.0.1:8787", Kind: modelsetup.KindUnreachable, Log: "/Users/dev/.wt-model.err.log"}}
	progress := []string{"step 1/5: token", "step 2/5: pair", "step 3/5: plist", "step 4/5: bootstrap", "step 5/5: wait"}

	phases := map[string]func(*testing.T, int, int) *model{
		"view": func(t *testing.T, w, h int) *model {
			return setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, w, h)
		},
		"local fields": func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{noKeyState()}}, w, h), "down", "down", "down", "sk-x")
		},
		"remote machine": func(t *testing.T, w, h int) *model {
			return setupEditor(t, &setupMachine{states: []modelsetup.State{remoteState()}}, w, h)
		},
		"confirm": func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}, plan: long}, w, h), "a")
		},
		"remove agent": func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, w, h), "left", "a")
		},
		"applying": func(t *testing.T, w, h int) *model {
			m := keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}, plan: long}, w, h), "a")
			next, _ := m.Update(keyMsg("y"))
			next, _ = next.(*model).Update(keyMsg("q"))
			return next.(*model)
		},
		"result": func(t *testing.T, w, h int) *model {
			sm := &setupMachine{states: []modelsetup.State{fullState()}, plan: long, result: result, progress: progress}
			return keys(t, setupEditor(t, sm, w, h), "a", "y")
		},
		"parse error": func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{parseErrState()}}, w, h), "a")
		},
		"off macOS local": func(t *testing.T, w, h int) *model {
			st := offMacState()
			st.Resolution = modelmode.Resolution{Mode: modelmode.ModeLocal, Source: modelmode.SourceStored}
			return setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, w, h)
		},
	}
	// reached says the phase was really reached, so a fit is not measured on
	// the wrong screen.
	reached := map[string]func(*model) bool{
		"view":         func(m *model) bool { return m.setup.phase == setupView && m.setup.loaded },
		"local fields": func(m *model) bool { return m.setup.phase == setupEdit && m.setup.field == sfMaster },
		"remote machine": func(m *model) bool {
			return m.setup.phase == setupView && m.setup.loaded && m.setup.want.Mode == modelmode.ModeLocal
		},
		"confirm":      func(m *model) bool { return m.setup.phase == setupConfirm && m.setup.prompt == promptApply },
		"remove agent": func(m *model) bool { return m.setup.phase == setupConfirm && m.setup.prompt == promptRemoveAgent },
		"applying":     func(m *model) bool { return m.setup.phase == setupApplying && m.setup.writing && m.quitPending },
		"result":       func(m *model) bool { return strings.Contains(m.setup.status, "did not answer") },
		"parse error": func(m *model) bool {
			return m.setup.state.Config.ParseErr != "" && strings.Contains(m.setup.status, "nothing was changed")
		},
		"off macOS local": func(m *model) bool {
			return !m.setup.state.Platform.OK && m.setup.want.Mode == modelmode.ModeLocal
		},
	}
	for name, open := range phases {
		for _, size := range tabSizes {
			m := open(t, size[0], size[1])
			if !reached[name](m) {
				t.Errorf("%s at %dx%d: the phase was not reached (phase %d)", name, size[0], size[1], m.setup.phase)
			}
			assertFits(t, name, m.View(), size[0], size[1])
			if last := hintLine(m); !strings.Contains(last, "quit") {
				t.Errorf("%s at %dx%d: the last line is %q, want the hints", name, size[0], size[1], last)
			}
		}
	}
}

// hiddenURLText is modelsetup's placeholder for a url holding an '@'.
const hiddenURLText = "(hidden: the url contains an @)"

// buildableLocal is a macOS host stored in local mode that the real
// modelsetup.BuildPlan plans for once a master key is entered: wt-model is
// installed, the LiteLLM plist holds no key and the pair is unset.
func buildableLocal() modelsetup.State {
	return modelsetup.State{
		Platform: modelsetup.Platform{OK: true},
		State: modelmode.State{
			Resolution: modelmode.Resolution{Mode: modelmode.ModeLocal, Source: modelmode.SourceStored},
			Config:     modelsetup.ConfigState{Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true, Table: config.ModelServerState{Mode: "local"}},
			Token:      modelsetup.TokenState{Path: "/Users/dev/.config/wt-model/server.token"},
			Host:       modelmode.HostState{Home: "/Users/dev", ConfigHome: "/Users/dev/.config"},
		},
		Tools:        modelsetup.ToolsState{Binary: "/Users/dev/.local/bin/wt-model", Path: "/usr/bin", UID: 501},
		Agent:        modelsetup.AgentState{PlistPath: "/Users/dev/Library/LaunchAgents/local.wt-model.serve.plist"},
		Listener:     modelsetup.ListenerState{Port: 8787},
		LiteLLMPlist: modelsetup.LiteLLMPlistState{Path: "/Users/dev/Library/LaunchAgents/local.litellm.proxy.plist", Present: true},
	}
}

// TestSetupTabRealPlanNeverShowsTheSecrets builds the plan with the real
// modelsetup.BuildPlan from a Desired that carries a master key (local), and
// pins that neither the plan's Format nor the confirm screen holds the
// value, while the plan's admin half still carries it for Apply.
func TestSetupTabRealPlanNeverShowsTheSecrets(t *testing.T) {
	local := buildableLocal()
	sm := &setupMachine{states: []modelsetup.State{local}, build: true}
	m := keys(t, setupEditor(t, sm, 120, 50), "down", "down", "down", "sk-FAKE-SECRET", "esc", "a")
	p, err := modelsetup.BuildPlan(sm.planned[0], sm.desired[0])
	if err != nil || p.Admin.MasterKey.Reveal() != "sk-FAKE-SECRET" {
		t.Fatalf("real local plan: err %v, or the key did not reach Want", err)
	}
	if m.setup.phase != setupConfirm || strings.Contains(plain(m), "FAKE-SECRET") || strings.Contains(p.Format(), "FAKE-SECRET") {
		t.Errorf("phase %d status %q: the master key is on the confirm screen or in Format:\n%s", m.setup.phase, m.setup.status, plain(m))
	}
	if !strings.Contains(flat(plain(m)), "keyasentered(notshown)") {
		t.Errorf("the confirm screen does not show the pair step that uses the key:\n%s", plain(m))
	}
}

// TestSetupTabFormIsInertWhileAPlanIsPrepared pins that nothing on the form
// changes while the read and the plan a started are out: the mode keys and
// the key into the fields only say why they wait, so typed text can neither
// change what is planned nor land on a question that appears meanwhile (a y
// typed into a field must never confirm). The plan then comes from the form
// as it was when a was pressed, and nothing is applied.
func TestSetupTabFormIsInertWhileAPlanIsPrepared(t *testing.T) {
	st := fullState()
	st.Agent.Listen = "127.0.0.1:8787"
	sm := &setupMachine{states: []modelsetup.State{st}}
	m := setupEditor(t, sm, 120, 50)
	next, observe := m.Update(keyMsg("a"))
	m = next.(*model)
	for _, k := range []string{"down", "xy", "y", "left", "right", " ", "enter"} {
		next, _ = m.Update(keyMsg(k))
		m = next.(*model)
	}
	if m.setup.phase != setupView || m.setup.want.Mode != modelmode.ModeLocal || m.setup.fields[sfListen].Value() != "127.0.0.1:8787" {
		t.Fatalf("while the plan was prepared: phase %d mode %q listen %q, want the form unchanged",
			m.setup.phase, m.setup.want.Mode, m.setup.fields[sfListen].Value())
	}
	if !strings.Contains(m.setup.status, "wait") {
		t.Errorf("status = %q, want it to say the form waits", m.setup.status)
	}
	if h := hintLine(m); strings.Contains(h, "left/right") || strings.Contains(h, "down") {
		t.Errorf("hints %q name keys that do nothing while busy", h)
	}
	m = send(t, m, observe())
	if sm.applies != 0 || sm.plans != 1 || sm.desired[0].Listen != "127.0.0.1:8787" {
		t.Errorf("applies %d plans %d, want one plan of the address as it was and no apply", sm.applies, sm.plans)
	}
}

// TestSetupTabPlansTheSnapshotTakenAtA pins that the read a starts and the
// plan after it use one snapshot of the form, taken when a was pressed: a
// field changed between the read and the plan (set on the model here, since
// the keys cannot) does not reach Plan, and the read looked at the port of
// the address that is planned.
func TestSetupTabPlansTheSnapshotTakenAtA(t *testing.T) {
	st := fullState()
	st.Agent.Listen = "127.0.0.1:8787"
	sm := &setupMachine{states: []modelsetup.State{st}}
	m := setupEditor(t, sm, 120, 50)
	next, observe := m.Update(keyMsg("a"))
	m = next.(*model)
	m.setup.fields[sfListen].SetValue("127.0.0.1:9999")
	m = send(t, m, observe())
	if sm.opts[1].ListenPort != 8787 {
		t.Errorf("the read looked at port %d, want the address at a", sm.opts[1].ListenPort)
	}
	if sm.plans != 1 || sm.desired[0].Listen != "127.0.0.1:8787" {
		t.Errorf("Plan got listen %q, want the snapshot's address", sm.desired[0].Listen)
	}
}

// TestSetupTabScrubsTheResolutionProblem pins carry-forward 5: the mode
// resolution's problem text is scrubbed like every other free text the tab
// draws.
func TestSetupTabScrubsTheResolutionProblem(t *testing.T) {
	st := fullState()
	st.Resolution = modelmode.Resolution{Problem: "WT_MODEL_URL https://me:pw-PROB-SECRET@gpu.example:8787 has no token"}
	v := plain(setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 120, 50))
	if strings.Contains(v, "PROB-SECRET") || !strings.Contains(v, "gpu.example:8787") {
		t.Errorf("the resolution problem is not scrubbed, or not drawn:\n%s", v)
	}
}

// TestSetupTabHintsNameOnlyKeysThatAct pins carry-forward 6 and T5: a key
// is in the hints only when pressing it acts. Before the first observation
// the mode keys do nothing; with a parse error a and s/x/R refuse; s/x/R act
// only for local; the fields' key only where a mode has fields (local).
func TestSetupTabHintsNameOnlyKeysThatAct(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.opts, m.tab = Options{Setup: (&setupMachine{}).deps(), Models: quietModels()}, TabSetup
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = send(t, m, loadedMsg{})
	if h := hintLine(m); strings.Contains(h, "left/right") || strings.Contains(h, "a apply") || !strings.Contains(h, "r re-read") {
		t.Errorf("before the first observation the hints are %q", h)
	}
	cases := []struct {
		name     string
		st       modelsetup.State
		has, not []string
	}{
		{"local", fullState(), []string{"left/right", "a apply", "s/x/R", "down"}, nil},
		// A remote machine's choice starts on local, whose keys act.
		{"remote", remoteState(), []string{"a apply", "down", "s/x/R"}, nil},
		{"parse error", parseErrState(), []string{"r re-read"}, []string{"a apply", "s/x/R"}},
	}
	for _, c := range cases {
		h := hintLine(setupEditor(t, &setupMachine{states: []modelsetup.State{c.st}}, 120, 24))
		for _, k := range c.has {
			if !strings.Contains(h, k) {
				t.Errorf("%s: hints %q lack %q", c.name, h, k)
			}
		}
		for _, k := range c.not {
			if strings.Contains(h, k) {
				t.Errorf("%s: hints %q name %q, which does nothing here", c.name, h, k)
			}
		}
	}
	native := fullState()
	native.Resolution.Mode = modelmode.ModeNative
	if h := hintLine(setupEditor(t, &setupMachine{states: []modelsetup.State{native}}, 120, 24)); strings.Contains(h, "down") {
		t.Errorf("native has no fields, yet the hints are %q", h)
	}
}

// TestSetupTabSaysWhenStoredLocalCannotRunHere pins carry-forward 7: off
// macOS with local in force, no option is marked (x), and the tab says why
// rather than leaving the choice blank.
func TestSetupTabSaysWhenStoredLocalCannotRunHere(t *testing.T) {
	st := offMacState()
	st.Resolution = modelmode.Resolution{Mode: modelmode.ModeLocal, Source: modelmode.SourceStored}
	v := plain(setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 120, 50))
	if strings.Contains(v, "(x)") || !strings.Contains(v, "local is set but cannot run here") {
		t.Errorf("stored local off macOS:\n%s", v)
	}
}

// TestSetupTabPointsAtWtConfigForRemote pins that the mode choice carries
// the mirror of the Mode tab's local pointer note: remote is not one of the
// modes set up here, so the note names where it is chosen.
func TestSetupTabPointsAtWtConfigForRemote(t *testing.T) {
	for _, st := range []modelsetup.State{fullState(), remoteState(), offMacState()} {
		v := plain(setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 120, 50))
		if !strings.Contains(v, remotePointerNote) {
			t.Errorf("resolution %+v: the view does not name `wt config` for remote:\n%s", st.Resolution, v)
		}
	}
}

// TestSetupTabCallsApplyAndControlOnlyFromCommands pins carry-forward 8:
// Update hands Plan, Apply and Control to commands that captured the deps
// when they were made; nothing is called until the command runs, and a dep
// swapped afterwards is not the one called.
func TestSetupTabCallsApplyAndControlOnlyFromCommands(t *testing.T) {
	sm := &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan()}
	m := setupEditor(t, sm, 80, 24)
	m = keys(t, m, "a")
	next, apply := m.Update(keyMsg("y"))
	m = next.(*model)
	next, control := m.Update(keyMsg("s"))
	m = next.(*model)
	if apply == nil || sm.applies != 0 || sm.controls != 0 {
		t.Fatalf("Update called a dep: applies = %d controls = %d", sm.applies, sm.controls)
	}
	other := &setupMachine{}
	m.opts.Setup = other.deps()
	apply()
	if sm.applies != 1 || other.applies != 0 {
		t.Errorf("the apply command did not use the deps captured with it: %d / %d", sm.applies, other.applies)
	}
	if control != nil {
		t.Errorf("s while an apply is out: a control command, want it refused")
	}

	sm = &setupMachine{states: []modelsetup.State{fullState()}}
	m = setupEditor(t, sm, 80, 24)
	next, control = m.Update(keyMsg("x"))
	m = next.(*model)
	if control == nil || sm.controls != 0 {
		t.Fatalf("x: cmd = %v controls = %d", control != nil, sm.controls)
	}
	m.opts.Setup = other.deps()
	control()
	if sm.controls != 1 || other.controls != 0 {
		t.Errorf("the control command did not use the deps captured with it")
	}
}

// TestSetupTabSourceNeverFormatsAPlanWithV pins the other half of
// carry-forward 8: the tab's code never formats a value with %v, %+v or %#v,
// so a Plan or a Desired (which carry secrets, redacted or not) is never
// printed whole.
func TestSetupTabSourceNeverFormatsAPlanWithV(t *testing.T) {
	verb := regexp.MustCompile(`%[+#]?v`)
	for _, f := range []string{"setup_tab.go", "setup_view.go", "setup_litellm.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := verb.FindIndex(src); loc != nil {
			t.Errorf("%s formats with %q", f, src[loc[0]:loc[1]])
		}
	}
}

// TestSetupTabNeverRendersAPieceOfAnAtURL pins the tab's side of the rule
// "a url with an '@' is never echoed": with a State whose stored (remote) url
// and check are the placeholder and fixed sentence modelsetup gives such a
// url, the view and the real local plan that replaces it (its Format, and
// the note that the stored url's credentials are not kept, read from
// ConfigState.URLHadCredentials and Plan.WritesURL, never from wording), and
// an Apply whose failure is the fixed refusal, no screen shows any piece of
// the hostile url. Free-text Warnings carrying a hostile url raw with a plain
// user:password are hidden by editorkit.Scrub too, which is defence in depth
// only: it keys on "://" and the first '/', so "ftp://u:p//@h",
// "http://u:p@PASS/x@h" and a schemeless "user:pa://ss@host" keep a piece
// (modelsetup never hands the tab such text; editorkit.Scrub was not
// widened, since a greedy '@' rule would also eat the placeholder's '@').
func TestSetupTabNeverRendersAPieceOfAnAtURL(t *testing.T) {
	const checkHiddenText = "the check failed; the url contains an @ so the error text is hidden"
	refusal := "write [model-server]: the url contains an @ (read as a user:password); wt does not store credentials in config.toml; use a token file; config.toml was not written"
	st := buildableLocal()
	st.LiteLLMPlist.HasMasterKey = true
	st.Resolution = modelmode.Resolution{Mode: modelmode.ModeRemote, Source: modelmode.SourceStored}
	st.Config.Table = config.ModelServerState{Mode: "remote", URL: hiddenURLText}
	st.Config.URLHadCredentials = true
	st.Server = modelsetup.ServerState{URL: hiddenURLText, Checked: true, Kind: modelsetup.KindUnreachable, Message: checkHiddenText}
	sm := &setupMachine{states: []modelsetup.State{st}, build: true}
	m := setupEditor(t, sm, 120, 50)
	screens := map[string]string{"view": plain(m)}
	m = keys(t, m, "a")
	screens["local plan"] = plain(m) + "\n" + m.setup.status
	if m.setup.phase != setupConfirm || !strings.Contains(flat(plain(m)), "credentialsarenotkept") {
		t.Errorf("phase %d status %q: the local plan over a url with credentials lacks the note:\n%s", m.setup.phase, m.setup.status, plain(m))
	}
	failed := modelsetup.Step{Kind: modelsetup.StepConfig}
	sm2 := &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan(),
		result: modelsetup.Result{Failed: &failed, Err: errors.New(refusal),
			Warnings: []string{"https://USERX:PASSX@gpu.example:8787", "dial http://USERX@gpu.example:8787/x: refused"}}}
	m = keys(t, setupEditor(t, sm2, 120, 50), "a", "y")
	screens["applied"] = plain(m)
	for name, v := range screens {
		for _, piece := range []string{"USERX", "PASSX"} {
			if strings.Contains(v, piece) {
				t.Errorf("%s shows %q:\n%s", name, piece, v)
			}
		}
	}
}

// TestSetupTabOffersLocalAndNativeOnly pins that the Setup tab's mode choice
// is local and native, in that order: remote is set in `wt config` (the
// session editor's Mode tab), so this admin screen never offers it, and the
// mode keys cycle between the two.
func TestSetupTabOffersLocalAndNativeOnly(t *testing.T) {
	m := setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, 100, 30)
	radios, _ := m.modeChoice()
	joined := strings.Join(radios, " ")
	li, ni := strings.Index(joined, "local"), strings.Index(joined, "native")
	if li < 0 || ni < 0 || li > ni || strings.Contains(joined, "remote") {
		t.Fatalf("radios = %q, want local then native and no remote", radios)
	}
	for i, want := range []modelmode.Mode{modelmode.ModeNative, modelmode.ModeLocal, modelmode.ModeNative} {
		m = keys(t, m, "right")
		if m.setup.want.Mode != want {
			t.Fatalf("right #%d: mode %q, want %q", i+1, m.setup.want.Mode, want)
		}
	}
	m = keys(t, m, "left")
	if m.setup.want.Mode != modelmode.ModeLocal {
		t.Errorf("left from native: mode %q, want local", m.setup.want.Mode)
	}
}

// TestSetupTabSaysRemoteIsSetInWtConfig pins what the Setup tab does with a
// machine whose mode is remote: it shows the mode (where it came from), says
// in a line under it that remote is set in `wt config`, and starts the
// choice on local, the setup this screen is for.
func TestSetupTabSaysRemoteIsSetInWtConfig(t *testing.T) {
	st := fullState()
	st.Resolution = modelmode.Resolution{Mode: modelmode.ModeRemote, Source: modelmode.SourceStored}
	m := setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 100, 30)
	v := plain(m)
	if !strings.Contains(v, "remote mode is set in `wt config` (its Mode tab), not here") || !strings.Contains(v, "remote (from config.toml)") {
		t.Fatal(v)
	}
	if m.setup.want.Mode != modelmode.ModeLocal {
		t.Fatalf("choice = %q, want local", m.setup.want.Mode)
	}
	if strings.Contains(plain(setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, 100, 30)), "remote mode is set") {
		t.Errorf("a local machine shows the remote line")
	}
}

// TestSetupTabHoldsAQuitForAModelsWriteToo pins the shell's held quit over
// both tabs: a quit while a registry write is out waits on the Models tab,
// and a quit while a setup apply or start/stop is out waits on the Setup
// tab, whichever tab it was pressed on, so neither write's result is lost.
func TestSetupTabHoldsAQuitForAModelsWriteToo(t *testing.T) {
	m := setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, 80, 24)
	m.models.writing = true
	next, cmd := m.Update(keyMsg("q"))
	m = next.(*model)
	if cmd != nil || !m.quitPending || m.tab != TabModels {
		t.Errorf("q during a registry write: cmd %v quitPending %v tab %d, want held on Models", cmd != nil, m.quitPending, m.tab)
	}

	sm := &setupMachine{states: []modelsetup.State{fullState()}}
	m = setupEditor(t, sm, 80, 24)
	next, start := m.Update(keyMsg("s"))
	m = next.(*model)
	m = keys(t, m, "tab")
	if m.tab != TabModels || !m.setup.writing {
		t.Fatalf("tab during a start: tab %d writing %v", m.tab, m.setup.writing)
	}
	next, cmd = m.Update(keyMsg("q"))
	m = next.(*model)
	if cmd != nil || !m.quitPending || m.tab != TabSetup {
		t.Fatalf("q on Models during a start: cmd %v quitPending %v tab %d, want held on Setup", cmd != nil, m.quitPending, m.tab)
	}
	if _, cmd = m.Update(start()); !isQuit(cmd) {
		t.Errorf("the start's result did not issue the held quit")
	}
}
