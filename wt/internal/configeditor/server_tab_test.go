package configeditor

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
)

// TestTabBarShowsThreeTabs pins the tab bar's three names in order, so the
// Model server tab is drawn after Models and the old two-tab prefixes stay.
func TestTabBarShowsThreeTabs(t *testing.T) {
	got := tabBar(testTheme(), TabServer)
	if !strings.Contains(got, "Agents") || !strings.Contains(got, "Models") || !strings.Contains(got, "[Model server]") {
		t.Errorf("tab bar = %q, want Agents, Models and [Model server]", got)
	}
	if strings.Index(got, "Models") > strings.Index(got, "Model server") {
		t.Errorf("tab bar = %q, want Models before Model server", got)
	}
}

// TestNextTabCycles pins the one tab order that every tab key uses:
// Agents, Models, Model server, back to Agents.
func TestNextTabCycles(t *testing.T) {
	for from, want := range map[Tab]Tab{TabAgents: TabModels, TabModels: TabServer, TabServer: TabAgents} {
		if got := nextTab(from); got != want {
			t.Errorf("nextTab(%d) = %d, want %d", from, got, want)
		}
	}
}

// TestStartTabOpensTheModelServerTab checks that Options.StartTab can open the
// new tab, that its view starts with the tab bar, and that tab leaves it.
func TestStartTabOpensTheModelServerTab(t *testing.T) {
	m := newModel(testTheme(), nil, nil)
	m.opts, m.tab = Options{StartTab: TabServer, Server: (&serverMachine{}).deps()}, TabServer
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = send(t, m, loadedMsg{cfg: nil})
	if !strings.Contains(m.View(), "[Model server]") {
		t.Fatalf("want the Model server tab open, view:\n%s", m.View())
	}
	m = keys(t, m, "tab")
	if m.tab != TabAgents {
		t.Errorf("tab on Model server = %d, want Agents", m.tab)
	}
}

// serverMachine is a throwaway machine for the Model server tab: a canned
// State per observation (the last one repeats) and a counter per dep, so a
// test can tell what the tab asked for.
type serverMachine struct {
	states   []modelsetup.State
	observes int
	opts     []modelsetup.ObserveOptions
	plans    int
	applies  int
	controls int
}

func (sm *serverMachine) deps() ServerDeps {
	return ServerDeps{
		Observe: func(_ context.Context, o modelsetup.ObserveOptions) modelsetup.State {
			sm.observes++
			sm.opts = append(sm.opts, o)
			if len(sm.states) == 0 {
				return modelsetup.State{}
			}
			return sm.states[min(sm.observes, len(sm.states))-1]
		},
		Plan: func(modelsetup.State, modelsetup.Desired) (modelsetup.Plan, error) {
			sm.plans++
			return modelsetup.Plan{}, nil
		},
		Apply: func(context.Context, modelsetup.Plan, func(string)) modelsetup.Result {
			sm.applies++
			return modelsetup.Result{}
		},
		Control: func(context.Context, modelsetup.ControlAction) error {
			sm.controls++
			return nil
		},
	}
}

// runCmd runs a command the way the Bubble Tea runtime would, a batch's
// commands each in turn, and delivers what they produce.
func runCmd(t *testing.T, m *model, cmd tea.Cmd) *model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch out := cmd().(type) {
	case nil, tea.QuitMsg:
		return m
	case tea.BatchMsg:
		for _, c := range out {
			m = runCmd(t, m, c)
		}
		return m
	default:
		return send(t, m, out)
	}
}

// serverEditor is the editor in a terminal of the given size, opened on the
// Model server tab with Init run, so its first observation is delivered.
func serverEditor(t *testing.T, sm *serverMachine, width, height int) *model {
	t.Helper()
	m := newModel(testTheme(), &config.Config{}, nil)
	m.opts = Options{StartTab: TabServer, Server: sm.deps(), Models: newTabMachine(t, "").deps()}
	m.tab = TabServer
	m = send(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	return runCmd(t, m, m.Init())
}

// plain is the view with its colour taken out (L14).
func plain(m *model) string { return ansi.Strip(m.View()) }

// fullState is a local-mode machine with every part filled: the worst case
// for the tab's height.
func fullState() modelsetup.State {
	return modelsetup.State{
		Platform:    modelsetup.Platform{OK: true},
		Resolution:  modelclient.Resolution{Mode: modelclient.ModeLocal, Source: modelclient.SourceStored},
		EnvOverride: "WT_MODEL_URL, WT_MODEL_TOKEN_FILE",
		Config: modelsetup.ConfigState{
			Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true,
			Table:    config.ModelServerState{Mode: "local", URL: "http://127.0.0.1:8787", TokenFile: "~/.config/wt-model/server.token"},
			Problems: []string{"token under [model-server] is set; move it into a 0600 token_file"},
		},
		Token: modelsetup.TokenState{Path: "/Users/dev/.config/wt-model/server.token", Exists: true},
		Host:  modelsetup.HostState{Home: "/Users/dev"},
		Pair:  modelsetup.PairState{URL: "http://127.0.0.1:4000", KeySet: true},
		Agent: modelsetup.AgentState{
			PlistPath: "/Users/dev/Library/LaunchAgents/local.wt-model.serve.plist", Present: true, Managed: true,
			Job: modelsetup.LaunchdJob{Loaded: true, Running: true, PID: 4123},
		},
		Server: modelsetup.ServerState{URL: "http://127.0.0.1:8787", Checked: true, Kind: modelsetup.KindUnauthorized,
			Message: "model server at 127.0.0.1:8787 refused the token (unauthorized) — check token_file under [model-server] in config.toml"},
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
		Config: modelsetup.ConfigState{Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true,
			ParseErr: "toml: line 3 (last key \"model-server.mode\"): expected value but found \"=\" instead"},
	}
}

// offMacState is a Linux machine: local cannot be set up there.
func offMacState() modelsetup.State {
	return modelsetup.State{Platform: modelsetup.Platform{Reason: "the local model server runs as a macOS LaunchAgent; this is linux"}}
}

// TestServerTabObservesOnFirstVisitOnly pins that the tab reads the machine
// once when it is first shown (by tab or by StartTab) and not on later
// visits: a slow ping must not run on every pass through the tabs.
func TestServerTabObservesOnFirstVisitOnly(t *testing.T) {
	sm := &serverMachine{states: []modelsetup.State{fullState()}}
	m := newModel(testTheme(), &config.Config{}, nil)
	m.opts = Options{Server: sm.deps(), Models: newTabMachine(t, "").deps()}
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = runCmd(t, m, m.Init())
	if sm.observes != 0 {
		t.Fatalf("observes on the Agents tab = %d, want 0", sm.observes)
	}
	m = keys(t, m, "tab", "tab")
	if m.tab != TabServer || sm.observes != 1 {
		t.Fatalf("tab = %d observes = %d, want the Model server tab observed once", m.tab, sm.observes)
	}
	m = keys(t, m, "tab", "tab", "tab")
	if m.tab != TabServer || sm.observes != 1 {
		t.Errorf("tab = %d observes = %d after a second visit, want still 1", m.tab, sm.observes)
	}

	sm = &serverMachine{states: []modelsetup.State{fullState()}}
	m = serverEditor(t, sm, 80, 24)
	if sm.observes != 1 {
		t.Fatalf("observes with StartTab = TabServer = %d, want 1 from Init", sm.observes)
	}
	m = keys(t, m, "tab", "tab", "tab")
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

// TestServerTabShowsTheEffectiveModeAndEnvOverride pins that the tab says
// which mode is in force and where it came from, marks it in the choice, and
// names the environment variables that override config.toml.
func TestServerTabShowsTheEffectiveModeAndEnvOverride(t *testing.T) {
	st := fullState()
	st.Resolution = modelclient.Resolution{Mode: modelclient.ModeRemote, Source: modelclient.SourceEnvMode}
	st.EnvOverride = "WT_MODEL_MODE, WT_MODEL_URL"
	m := serverEditor(t, &serverMachine{states: []modelsetup.State{st}}, 120, 50)
	v := plain(m)
	for _, want := range []string{"remote (from WT_MODEL_MODE)", "( ) native  ( ) local  (x) remote", "WT_MODEL_MODE, WT_MODEL_URL", "override config.toml"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	// The choice moves with left and right; nothing is written.
	m = keys(t, m, "right")
	if m.server.want.Mode != modelclient.ModeNative || !strings.Contains(plain(m), "(x) native  ( ) local  ( ) remote") {
		t.Errorf("right from remote: want.Mode = %q:\n%s", m.server.want.Mode, plain(m))
	}
	m = keys(t, m, "left", "left")
	if m.server.want.Mode != modelclient.ModeLocal {
		t.Errorf("left twice from native: want.Mode = %q, want local", m.server.want.Mode)
	}
	m = keys(t, m, " ")
	if m.server.want.Mode != modelclient.ModeRemote {
		t.Errorf("space from local: want.Mode = %q, want remote", m.server.want.Mode)
	}

	st.Resolution = modelclient.Resolution{Mode: modelclient.ModeLocal, Source: modelclient.SourceStored}
	st.EnvOverride = ""
	v = plain(serverEditor(t, &serverMachine{states: []modelsetup.State{st}}, 120, 50))
	if !strings.Contains(v, "local (from config.toml)") || strings.Contains(v, "override") {
		t.Errorf("stored local, no env:\n%s", v)
	}
	// The token file is shown the way the config writes it, with its state.
	if !strings.Contains(v, "~/.config/wt-model/server.token ok") {
		t.Errorf("token line:\n%s", v)
	}
	if !strings.Contains(v, "running (pid 4123)") || !strings.Contains(v, "token refused") {
		t.Errorf("agent or server line:\n%s", v)
	}
}

// TestServerTabShowsEachLiteLLMCheck pins one line per LiteLLM check, each
// with its status as a word (V2: colour only repeats it).
func TestServerTabShowsEachLiteLLMCheck(t *testing.T) {
	v := flat(plain(serverEditor(t, &serverMachine{states: []modelsetup.State{fullState()}}, 120, 50)))
	for _, want := range []string{
		"reachableok", "databasewarning:readinesssays", "keyproblem:theproxyrefused", "routesok:12routes",
		"restartskipped:proxynotrunning", "portok", "plistmodewarning:plistismode0644",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
}

// TestServerTabShowsAParseErrorWithThePath pins the parse-error state: the
// file, the error and the way out (fix it, press r) are on screen, and r
// reads the machine again.
func TestServerTabShowsAParseErrorWithThePath(t *testing.T) {
	sm := &serverMachine{states: []modelsetup.State{parseErrState(), fullState()}}
	m := serverEditor(t, sm, 80, 24)
	v := flat(plain(m))
	for _, want := range []string{"/Users/dev/.config/agent-wt/config.toml", "line3", "fixthefile,thenpressr"} {
		if !strings.Contains(v, flat(want)) {
			t.Errorf("view lacks %q:\n%s", want, plain(m))
		}
	}
	// At every size: the status block gives way before the path and the way
	// out do.
	for _, size := range tabSizes {
		v := flat(plain(serverEditor(t, &serverMachine{states: []modelsetup.State{parseErrState()}}, size[0], size[1])))
		if !strings.Contains(v, "/Users/dev/.config/agent-wt/config.toml") || !strings.Contains(v, "fixthefile,thenpressr") {
			t.Errorf("at %dx%d the parse error lacks the path or the way out: %s", size[0], size[1], v)
		}
	}
	m = keys(t, m, "r")
	if sm.observes != 2 || strings.Contains(plain(m), "then press r") {
		t.Errorf("r: observes = %d, want 2 and the error gone:\n%s", sm.observes, plain(m))
	}
}

// TestServerTabDropsAStaleObservation pins that an observation older than
// the latest one asked for is dropped, in both orders of arrival: delivered
// after the newer one it does not overwrite it; delivered first it changes
// nothing and the tab stays busy until the newer one lands. A slow ping must
// not overwrite a later answer, nor end the wait for it.
func TestServerTabDropsAStaleObservation(t *testing.T) {
	newerReason := offMacState().Platform.Reason

	// staleMsgs opens the tab on fullState and dispatches two observations:
	// the older reads parseErrState, the newer offMacState.
	staleMsgs := func() (*model, *serverMachine, tea.Msg, tea.Msg) {
		sm := &serverMachine{states: []modelsetup.State{fullState()}}
		m := serverEditor(t, sm, 80, 24)
		older := m.observeServerCmd()
		newer := m.observeServerCmd()
		sm.states = []modelsetup.State{fullState(), parseErrState(), offMacState()}
		return m, sm, older(), newer()
	}

	// Newer first, then the stale one.
	m, _, oldMsg, newMsg := staleMsgs()
	m = send(t, m, newMsg)
	m = send(t, m, oldMsg)
	if !m.server.loaded || m.server.state.Platform.Reason != newerReason || m.server.state.Config.ParseErr != "" {
		t.Errorf("newer then older: loaded = %v state = %+v %+v, want the newer (off-macOS) observation",
			m.server.loaded, m.server.state.Platform, m.server.state.Config)
	}
	if m.server.busy {
		t.Errorf("newer then older: busy after the latest observation arrived")
	}

	// The stale one first: nothing changes and the tab still waits.
	m, _, oldMsg, newMsg = staleMsgs()
	m = send(t, m, oldMsg)
	if !m.server.busy {
		t.Errorf("older first: busy = false, want true while the latest observation is out")
	}
	if st := m.server.state; !st.Platform.OK || st.Resolution.Mode != modelclient.ModeLocal || st.Config.ParseErr != "" {
		t.Errorf("older first: state = %+v %+v, want fullState unchanged", st.Platform, st.Config)
	}
	m = send(t, m, newMsg)
	if m.server.busy || !m.server.loaded || m.server.state.Platform.Reason != newerReason {
		t.Errorf("then the newer: busy = %v loaded = %v platform = %+v, want the newer observation applied",
			m.server.busy, m.server.loaded, m.server.state.Platform)
	}
}

// TestServerTabLocalDisabledOffMacOS pins that local is shown as unavailable
// with the reason off macOS, and that the mode keys pass over it.
func TestServerTabLocalDisabledOffMacOS(t *testing.T) {
	m := serverEditor(t, &serverMachine{states: []modelsetup.State{offMacState()}}, 80, 24)
	v := plain(m)
	if !strings.Contains(v, "local needs launchd (macOS)") || !strings.Contains(v, "(-) local") {
		t.Errorf("view:\n%s", v)
	}
	m = keys(t, m, "right")
	if m.server.want.Mode != modelclient.ModeNative {
		t.Fatalf("right from unset = %q, want native", m.server.want.Mode)
	}
	m = keys(t, m, "right")
	if m.server.want.Mode != modelclient.ModeRemote {
		t.Errorf("right from native off macOS = %q, want remote (local skipped)", m.server.want.Mode)
	}
	m = keys(t, m, "left")
	if m.server.want.Mode != modelclient.ModeNative {
		t.Errorf("left from remote off macOS = %q, want native", m.server.want.Mode)
	}
}

// TestServerTabFitsTheTerminal measures every state of the tab at every
// supported size (L13): nothing is taller or wider than the terminal, and
// the hint line is the last line.
func TestServerTabFitsTheTerminal(t *testing.T) {
	long := fullState()
	long.Config.Problems = append(long.Config.Problems, strings.Repeat("a long problem that wraps ", 12))
	long.Token.Path = "/Users/dev/" + strings.Repeat("deeply-nested-directory/", 6) + "server.token"
	states := map[string]modelsetup.State{
		"empty": {}, "full": fullState(), "long": long, "parse error": parseErrState(), "off macOS": offMacState(),
	}
	for name, st := range states {
		for _, size := range tabSizes {
			m := serverEditor(t, &serverMachine{states: []modelsetup.State{st}}, size[0], size[1])
			assertFits(t, name, m.View(), size[0], size[1])
			lines := strings.Split(plain(m), "\n")
			if last := lines[len(lines)-1]; !strings.Contains(last, "q quit") {
				t.Errorf("%s at %dx%d: the last line is %q, want the hints", name, size[0], size[1], last)
			}
		}
	}
	// While the first observation is out: the tab bar and a line, no more.
	m := newModel(testTheme(), &config.Config{}, nil)
	m.opts, m.tab = Options{Server: (&serverMachine{}).deps()}, TabServer
	if v := m.serverTabView(); v != tabBar(testTheme(), TabServer) {
		t.Errorf("before the first WindowSizeMsg the view is %q, want only the tab bar", v)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	m = send(t, m, loadedMsg{})
	assertFits(t, "loading", m.View(), 40, 12)
}

// TestCtrlSOnTheServerTabDoesNothing pins that ctrl+s, the Agents tab's
// save, neither saves nor observes nor plans on this tab.
func TestCtrlSOnTheServerTabDoesNothing(t *testing.T) {
	sm := &serverMachine{states: []modelsetup.State{fullState()}}
	m := serverEditor(t, sm, 80, 24)
	m.dirty = true
	next, cmd := m.Update(keyMsg("ctrl+s"))
	m = next.(*model)
	if cmd != nil || m.saving || !m.dirty || sm.observes != 1 || sm.plans != 0 {
		t.Errorf("ctrl+s: cmd = %v saving = %v dirty = %v observes = %d plans = %d", cmd != nil, m.saving, m.dirty, sm.observes, sm.plans)
	}
}

// TestServerTabQuitKeysUseTheExistingQuit pins that q and ctrl+c leave
// through the editor's quit: straight out when nothing is pending, by the
// unsaved-changes prompt for agent edits, and held (on the Models tab) while
// a registry write is in flight.
func TestServerTabQuitKeysUseTheExistingQuit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := serverEditor(t, &serverMachine{states: []modelsetup.State{fullState()}}, 80, 24)
		_, cmd := m.Update(keyMsg(k))
		if cmd == nil {
			t.Fatalf("%s: no command, want tea.Quit", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s: command is not tea.Quit", k)
		}

		m = serverEditor(t, &serverMachine{states: []modelsetup.State{fullState()}}, 80, 24)
		m.dirty = true
		next, _ := m.Update(keyMsg(k))
		if next.(*model).phase != phaseQuit {
			t.Errorf("%s with agent edits: phase = %d, want the quit prompt", k, next.(*model).phase)
		}

		m = serverEditor(t, &serverMachine{states: []modelsetup.State{fullState()}}, 80, 24)
		m.models.writing = true
		next, cmd = m.Update(keyMsg(k))
		m = next.(*model)
		if cmd != nil || !m.quitPending || m.tab != TabModels {
			t.Errorf("%s during a registry write: cmd = %v quitPending = %v tab = %d, want the quit held on Models", k, cmd != nil, m.quitPending, m.tab)
		}
	}
}

// TestServerDepsWithDefaultsFillsOnlyNilFuncs pins that Run keeps a dep the
// caller injected and fills only the missing ones.
func TestServerDepsWithDefaultsFillsOnlyNilFuncs(t *testing.T) {
	sm := &serverMachine{}
	d := ServerDeps{Observe: sm.deps().Observe}.withDefaults()
	if d.Plan == nil || d.Apply == nil || d.Control == nil {
		t.Fatalf("withDefaults left a nil func: %+v", d)
	}
	d.Observe(context.Background(), modelsetup.ObserveOptions{})
	if sm.observes != 1 {
		t.Errorf("the injected Observe was replaced")
	}
}

// TestServerTabViewIsASCII pins V5: state is drawn in ASCII ((x), ( ), ok,
// warning:), never a check mark or a bullet; only the prose glyphs the style
// guide allows (· — …) may appear, and only in hints and prose.
func TestServerTabViewIsASCII(t *testing.T) {
	for _, st := range []modelsetup.State{fullState(), parseErrState(), offMacState(), {}} {
		v := plain(serverEditor(t, &serverMachine{states: []modelsetup.State{st}}, 120, 50))
		for _, r := range v {
			if r > 127 && !strings.ContainsRune("·—…", r) {
				t.Errorf("non-ASCII %q in the view:\n%s", r, v)
				break
			}
		}
	}
}

// TestServerTabNeverShowsASecret pins that no token, key, pasted secret or
// url password reaches the screen, even when a State or Desired a dep hands
// back carries one.
func TestServerTabNeverShowsASecret(t *testing.T) {
	st := fullState()
	st.Config.Table.Token = "tok-INLINE-SECRET"
	st.Config.Table.URL = "https://me:pw-URL-SECRET@gpu.example:8787/"
	st.Server.Message = "model server at https://me:pw-MSG-SECRET@gpu.example:8787 is not reachable"
	for _, size := range [][2]int{{120, 50}, {40, 12}} {
		m := serverEditor(t, &serverMachine{states: []modelsetup.State{st}}, size[0], size[1])
		m.server.want.PastedToken = "tok-PASTED-SECRET"
		m.server.want.MasterKey = "sk-MASTER-SECRET"
		v := plain(m)
		for _, secret := range []string{"INLINE-SECRET", "URL-SECRET", "MSG-SECRET", "PASTED-SECRET", "MASTER-SECRET", "me:pw"} {
			if strings.Contains(v, secret) {
				t.Errorf("at %dx%d the view shows %q:\n%s", size[0], size[1], secret, v)
			}
		}
	}
}
