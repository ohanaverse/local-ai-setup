package sessioneditor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeview"
)

// TestTabBarShowsAgentsAndMode pins the tab bar's two names: `wt config`
// draws Agents and Mode, the current one in brackets.
func TestTabBarShowsAgentsAndMode(t *testing.T) {
	got := tabBar(testTheme(), TabMode)
	if !strings.Contains(got, "Agents") || !strings.Contains(got, "[Mode]") {
		t.Errorf("tab bar = %q, want Agents and [Mode]", got)
	}
}

// TestNextTabCycles pins the one tab order that every tab key uses:
// Agents, Mode, back to Agents.
func TestNextTabCycles(t *testing.T) {
	for from, want := range map[Tab]Tab{TabAgents: TabMode, TabMode: TabAgents} {
		if got := nextTab(from); got != want {
			t.Errorf("nextTab(%d) = %d, want %d", from, got, want)
		}
	}
}

// TestStartTabOpensTheModeTab checks that Options.StartTab can open the Mode
// tab (the first-run gate opens it there), that its view starts with the
// tab bar, and that tab leaves it for Agents.
func TestStartTabOpensTheModeTab(t *testing.T) {
	m := newModel(testTheme(), nil, nil)
	m.opts, m.tab = Options{StartTab: TabMode, Mode: (&modeMachine{}).deps()}, TabMode
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = send(t, m, loadedMsg{cfg: nil})
	if !strings.Contains(m.View(), "[Mode]") {
		t.Fatalf("want the Mode tab open, view:\n%s", m.View())
	}
	m = keys(t, m, "tab")
	if m.tab != TabAgents {
		t.Errorf("tab on Mode = %d, want Agents", m.tab)
	}
}

// modeMachine is a throwaway machine for the Mode tab: a canned State per
// observation (the last one repeats), a canned plan and apply result, and a
// record of every dep call, so a test can tell what the tab asked for and in
// which order.
type modeMachine struct {
	states   []modelmode.State
	observes int
	opts     []modelmode.ObserveOptions
	plans    int
	applies  int
	// seq is every dep call in order: observe, plan, apply.
	seq []string
	// desired and planned are what each Plan call was given.
	desired []modelmode.Desired
	planned []modelmode.State
	// applied are the plans Apply ran.
	applied []modelmode.Plan
	// plan and planErr are what Plan returns (plan.Want is the Desired it
	// was given when plan has none); result and progress what Apply returns
	// and reports.
	plan    modelmode.Plan
	planErr error
	// build makes Plan the real modelmode.BuildPlan instead of the canned
	// plan, for a test about what a real plan's text holds.
	build    bool
	result   modelmode.Result
	progress []string
}

func (mm *modeMachine) deps() ModeDeps {
	return ModeDeps{
		Observe: func(_ context.Context, o modelmode.ObserveOptions) modelmode.State {
			mm.observes++
			mm.seq = append(mm.seq, "observe")
			mm.opts = append(mm.opts, o)
			if len(mm.states) == 0 {
				return modelmode.State{}
			}
			return mm.states[min(mm.observes, len(mm.states))-1]
		},
		Plan: func(st modelmode.State, want modelmode.Desired) (modelmode.Plan, error) {
			mm.plans++
			mm.seq = append(mm.seq, "plan")
			mm.desired = append(mm.desired, want)
			mm.planned = append(mm.planned, st)
			if mm.build {
				return modelmode.BuildPlan(st, want)
			}
			if mm.planErr != nil {
				return modelmode.Plan{}, mm.planErr
			}
			p := mm.plan
			if p.Want.Mode == "" {
				p.Want = want
			}
			return p, nil
		},
		Apply: func(_ context.Context, p modelmode.Plan, progress func(string)) modelmode.Result {
			mm.applies++
			mm.seq = append(mm.seq, "apply")
			mm.applied = append(mm.applied, p)
			for _, line := range mm.progress {
				progress(line)
			}
			return mm.result
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

// noWtModel is a LookPath that finds nothing, so no test offers to run
// `wt-model setup` unless it says so.
func noWtModel(string) (string, error) { return "", errors.New("not found") }

// modeEditor is the editor in a terminal of the given size, opened on the
// Mode tab with Init run, so its first observation is delivered.
func modeEditor(t *testing.T, mm *modeMachine, width, height int) *model {
	t.Helper()
	return modeEditorWith(t, mm, noWtModel, width, height)
}

// modeEditorWith is modeEditor with the LookPath given.
func modeEditorWith(t *testing.T, mm *modeMachine, look func(string) (string, error), width, height int) *model {
	t.Helper()
	m := newModel(testTheme(), &config.Config{}, nil)
	m.home = testUserHome
	scratchConfigHome(t)
	m.opts = Options{StartTab: TabMode, Mode: mm.deps(), LookPath: look}
	m.tab = TabMode
	m = send(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	return runCmd(t, m, m.Init())
}

// plain is the view with its colour taken out (L14).
func plain(m *model) string { return ansi.Strip(m.View()) }

// hintLine is the view's last line: the key hints.
func hintLine(m *model) string {
	lines := strings.Split(plain(m), "\n")
	return lines[len(lines)-1]
}

// fullState is a local-mode session with every row filled: the worst case
// for the tab's height.
func fullState() modelmode.State {
	return modelmode.State{
		Resolution:  modelmode.Resolution{Mode: modelmode.ModeLocal, Source: modelmode.SourceStored},
		EnvOverride: "WT_MODEL_URL, WT_MODEL_TOKEN_FILE",
		Config: modelmode.ConfigState{
			Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true,
			Table:    config.ModelServerState{Mode: "local", URL: "http://127.0.0.1:8787", TokenFile: "~/.config/wt-model/server.token"},
			Problems: []string{"token under [model-server] is set; move it into a 0600 token_file"},
		},
		Token: modelmode.TokenState{Path: "/Users/dev/.config/wt-model/server.token", Exists: true},
		Host:  modelmode.HostState{Home: "/Users/dev", ConfigHome: "/Users/dev/.config"},
		Server: modelmode.ServerState{URL: "http://127.0.0.1:8787", Checked: true, Kind: modelmode.KindUnauthorized,
			Message: "model server at 127.0.0.1:8787 refused the token (unauthorized) — check token_file under [model-server] in config.toml"},
	}
}

// parseErrState is a session whose config.toml does not parse.
func parseErrState() modelmode.State {
	return modelmode.State{
		Config: modelmode.ConfigState{Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true,
			ParseErr: "toml: line 3 (last key \"model-server.mode\"): expected value but found \"=\" instead"},
	}
}

// remoteState is a session stored in remote mode with no url or token yet:
// where a user setting up a remote server starts.
func remoteState() modelmode.State {
	return modelmode.State{
		Resolution: modelmode.Resolution{Mode: modelmode.ModeRemote, Source: modelmode.SourceStored},
		Config: modelmode.ConfigState{Path: "/Users/dev/.config/agent-wt/config.toml", Exists: true,
			Table: config.ModelServerState{Mode: "remote"}},
		Host: modelmode.HostState{Home: "/Users/dev", ConfigHome: "/Users/dev/.config"},
	}
}

// failedRemoteState is remoteState with a stored url (which fills the url
// field) whose check failed.
func failedRemoteState() modelmode.State {
	st := remoteState()
	st.Config.Table.URL = "https://gpu.example:8787"
	st.Server = modelmode.ServerState{URL: "https://gpu.example:8787", Checked: true, Kind: modelmode.KindUnreachable,
		Message: "model server at https://gpu.example:8787 is not reachable"}
	return st
}

// okPlan is a two-step plan for the confirm screen.
func okPlan() modelmode.Plan {
	return modelmode.Plan{Steps: []modelmode.Step{
		{Kind: modelmode.StepToken, Detail: "create the token file /Users/dev/.config/agent-wt/remote.token"},
		{Kind: modelmode.StepConfig, Detail: "write [model-server] in /Users/dev/.config/agent-wt/config.toml: mode = \"remote\""},
	}}
}

// longPlan is a plan whose text is taller than the smaller terminals.
func longPlan() modelmode.Plan {
	long := modelmode.Plan{Notes: []string{"the environment sets WT_MODEL_URL, which overrides what is stored here"}}
	for i := 0; i < 9; i++ {
		long.Steps = append(long.Steps, modelmode.Step{Kind: modelmode.StepConfig,
			Detail: "write [model-server] in /Users/dev/.config/agent-wt/some-long-directory/config.toml: mode = \"remote\", url = \"https://gpu.example:8787\""})
	}
	return long
}

// TestModeTabObservesOnFirstVisitOnly pins that the tab reads the machine
// once when it is first shown (by tab or by StartTab) and not on later
// visits: a slow ping must not run on every pass through the tabs.
func TestModeTabObservesOnFirstVisitOnly(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{fullState()}}
	m := newModel(testTheme(), &config.Config{}, nil)
	scratchConfigHome(t)
	m.opts = Options{Mode: mm.deps(), LookPath: noWtModel}
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = runCmd(t, m, m.Init())
	if mm.observes != 0 {
		t.Fatalf("observes on the Agents tab = %d, want 0", mm.observes)
	}
	m = keys(t, m, "tab")
	if m.tab != TabMode || mm.observes != 1 {
		t.Fatalf("tab = %d observes = %d, want the Mode tab observed once", m.tab, mm.observes)
	}
	m = keys(t, m, "tab", "tab")
	if m.tab != TabMode || mm.observes != 1 {
		t.Errorf("tab = %d observes = %d after a second visit, want still 1", m.tab, mm.observes)
	}

	mm = &modeMachine{states: []modelmode.State{fullState()}}
	m = modeEditor(t, mm, 80, 24)
	if mm.observes != 1 {
		t.Fatalf("observes with StartTab = TabMode = %d, want 1 from Init", mm.observes)
	}
	m = keys(t, m, "tab", "tab")
	if mm.observes != 1 {
		t.Errorf("observes after coming back = %d, want 1", mm.observes)
	}
	m = keys(t, m, "c")
	if mm.observes != 2 {
		t.Errorf("observes after c = %d, want 2", mm.observes)
	}
	keys(t, m, "r")
	if mm.observes != 3 {
		t.Errorf("observes after r = %d, want 3", mm.observes)
	}
	if mm.plans+mm.applies != 0 {
		t.Errorf("the tab planned or applied: %d %d", mm.plans, mm.applies)
	}
}

// TestModeTabShowsTheEffectiveModeAndEnvOverride pins that the tab says
// which mode is in force and where it came from, marks it in the choice, and
// names the environment variables that override config.toml.
func TestModeTabShowsTheEffectiveModeAndEnvOverride(t *testing.T) {
	st := fullState()
	st.Resolution = modelmode.Resolution{Mode: modelmode.ModeRemote, Source: modelmode.SourceEnvMode}
	st.EnvOverride = "WT_MODEL_MODE, WT_MODEL_URL"
	m := modeEditor(t, &modeMachine{states: []modelmode.State{st}}, 120, 50)
	v := plain(m)
	for _, want := range []string{"remote (from WT_MODEL_MODE)", "( ) native  ( ) local  (x) remote", "WT_MODEL_MODE, WT_MODEL_URL", "override config.toml"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	// The choice moves with left and right; nothing is written.
	m = keys(t, m, "right")
	if m.mode.want.Mode != modelmode.ModeNative || !strings.Contains(plain(m), "(x) native  ( ) local  ( ) remote") {
		t.Errorf("right from remote: want.Mode = %q:\n%s", m.mode.want.Mode, plain(m))
	}
	m = keys(t, m, "left", "left")
	if m.mode.want.Mode != modelmode.ModeLocal {
		t.Errorf("left twice from native: want.Mode = %q, want local", m.mode.want.Mode)
	}
	m = keys(t, m, " ")
	if m.mode.want.Mode != modelmode.ModeRemote {
		t.Errorf("space from local: want.Mode = %q, want remote", m.mode.want.Mode)
	}

	st.Resolution = modelmode.Resolution{Mode: modelmode.ModeLocal, Source: modelmode.SourceStored}
	st.EnvOverride = ""
	v = plain(modeEditor(t, &modeMachine{states: []modelmode.State{st}}, 120, 50))
	if !strings.Contains(v, "local (from config.toml)") || strings.Contains(v, "override") {
		t.Errorf("stored local, no env:\n%s", v)
	}
	// The token file is shown the way the config writes it, with its state.
	if !strings.Contains(v, "~/.config/wt-model/server.token ok") {
		t.Errorf("token line:\n%s", v)
	}
	if !strings.Contains(v, "token refused") {
		t.Errorf("server line:\n%s", v)
	}
}

// TestModeTabShowsAParseErrorWithThePath pins the parse-error state: the
// file, the error and the way out (fix it, press r) are on screen, and r
// reads the machine again.
func TestModeTabShowsAParseErrorWithThePath(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{parseErrState(), fullState()}}
	m := modeEditor(t, mm, 80, 24)
	v := flat(plain(m))
	for _, want := range []string{"~/.config/agent-wt/config.toml", "line3", "fixthefile,thenpressr"} {
		if !strings.Contains(v, flat(want)) {
			t.Errorf("view lacks %q:\n%s", want, plain(m))
		}
	}
	// At every size: the status block gives way before the path and the way
	// out do.
	for _, size := range tabSizes {
		v := flat(plain(modeEditor(t, &modeMachine{states: []modelmode.State{parseErrState()}}, size[0], size[1])))
		if !strings.Contains(v, "~/.config/agent-wt/config.toml") || !strings.Contains(v, "fixthefile,thenpressr") {
			t.Errorf("at %dx%d the parse error lacks the path or the way out: %s", size[0], size[1], v)
		}
	}
	m = keys(t, m, "r")
	if mm.observes != 2 || strings.Contains(plain(m), "then press r") {
		t.Errorf("r: observes = %d, want 2 and the error gone:\n%s", mm.observes, plain(m))
	}
}

// TestModeTabParseErrorSaysTheErrorOnce pins that a config.toml that does
// not parse is explained once, in the banner: the mode row says it is
// unknown, not repeating the error.
func TestModeTabParseErrorSaysTheErrorOnce(t *testing.T) {
	v := plain(modeEditor(t, &modeMachine{states: []modelmode.State{parseErrState()}}, 120, 50))
	if n := strings.Count(flat(v), flat(`expected value but found "=" instead`)); n != 1 {
		t.Errorf("the toml error appears %d times, want 1:\n%s", n, v)
	}
	if !strings.Contains(v, "unknown: config.toml is not readable") || strings.Contains(v, "unset: choose one below") {
		t.Errorf("the mode row does not say unknown:\n%s", v)
	}
}

// TestModeTabParseErrorPathIsAbbreviatedAndBreaksAtSlashes pins that the
// banner writes the config path with ~ and, when it is wider than the
// terminal, breaks it only after a slash: no segment is split when it fits.
func TestModeTabParseErrorPathIsAbbreviatedAndBreaksAtSlashes(t *testing.T) {
	st := parseErrState()
	st.Host.Home = "/Users/dev"
	st.Config.Path = "/Users/dev/.config/agent-wt/some-long-directory-name/another-long-directory/config.toml"
	for _, size := range [][2]int{{40, 50}, {80, 50}, {120, 50}} {
		m := modeEditor(t, &modeMachine{states: []modelmode.State{st}}, size[0], size[1])
		assertFits(t, "parse error", m.View(), size[0], size[1])
		v := plain(m)
		if strings.Contains(v, "/Users/dev") {
			t.Errorf("width %d: the home is not abbreviated:\n%s", size[0], v)
		}
		lines := strings.Split(v, "\n")
		for _, seg := range strings.Split(strings.TrimPrefix(editorkit.HomePath(st.Config.Path, "/Users/dev", ""), "~/"), "/") {
			found := false
			for _, l := range lines {
				if strings.Contains(l, seg) {
					found = true
				}
			}
			if len(seg)+1 <= size[0] && !found {
				t.Errorf("width %d: the segment %q is split across lines:\n%s", size[0], seg, v)
			}
		}
	}
}

// TestModeTabDropsAStaleObservation pins that an observation older than the
// latest one asked for is dropped, in both orders of arrival: delivered
// after the newer one it does not overwrite it; delivered first it changes
// nothing and the tab stays busy until the newer one lands.
func TestModeTabDropsAStaleObservation(t *testing.T) {
	// staleMsgs opens the tab on fullState and dispatches two observations:
	// the older reads parseErrState, the newer remoteState.
	staleMsgs := func() (*model, tea.Msg, tea.Msg) {
		mm := &modeMachine{states: []modelmode.State{fullState()}}
		m := modeEditor(t, mm, 80, 24)
		older := m.observeModeCmd()
		newer := m.observeModeCmd()
		mm.states = []modelmode.State{fullState(), parseErrState(), remoteState()}
		return m, older(), newer()
	}
	isNewer := func(st modelmode.State) bool {
		return st.Resolution.Mode == modelmode.ModeRemote && st.Config.ParseErr == ""
	}

	m, oldMsg, newMsg := staleMsgs()
	m = send(t, m, newMsg)
	m = send(t, m, oldMsg)
	if !m.mode.loaded || !isNewer(m.mode.state) || m.mode.busy {
		t.Errorf("newer then older: loaded = %v busy = %v state = %+v, want the newer observation", m.mode.loaded, m.mode.busy, m.mode.state.Config)
	}

	m, oldMsg, newMsg = staleMsgs()
	m = send(t, m, oldMsg)
	if !m.mode.busy {
		t.Errorf("older first: busy = false, want true while the latest observation is out")
	}
	if st := m.mode.state; st.Resolution.Mode != modelmode.ModeLocal || st.Config.ParseErr != "" {
		t.Errorf("older first: state = %+v, want fullState unchanged", st.Config)
	}
	m = send(t, m, newMsg)
	if m.mode.busy || !m.mode.loaded || !isNewer(m.mode.state) {
		t.Errorf("then the newer: busy = %v loaded = %v, want the newer observation applied", m.mode.busy, m.mode.loaded)
	}
}

// TestModeTabOffersAllThreeModesEverywhere pins that local is a choice on
// every platform here (it is a pointer, not a LaunchAgent): with nothing
// known of the machine the radios are native, local, remote, and right
// cycles through all three.
func TestModeTabOffersAllThreeModesEverywhere(t *testing.T) {
	m := modeEditor(t, &modeMachine{states: []modelmode.State{{}}}, 120, 50)
	if v := plain(m); !strings.Contains(v, "( ) native  ( ) local  ( ) remote") || strings.Contains(v, "(-)") {
		t.Fatalf("radios:\n%s", v)
	}
	for _, want := range []modelmode.Mode{modelmode.ModeNative, modelmode.ModeLocal, modelmode.ModeRemote, modelmode.ModeNative} {
		m = keys(t, m, "right")
		if m.mode.want.Mode != want {
			t.Errorf("right: want.Mode = %q, want %q", m.mode.want.Mode, want)
		}
	}
}

// TestModeTabShowsNoAdminRows pins that the session tab shows none of
// `wt-model setup`'s rows: no pair, agent, LiteLLM or plist row and no
// listen or master-key field, in any mode.
func TestModeTabShowsNoAdminRows(t *testing.T) {
	for _, st := range []modelmode.State{fullState(), remoteState(), {}} {
		m := modeEditor(t, &modeMachine{states: []modelmode.State{st}}, 120, 50)
		for _, mode := range []string{"", "right", "right"} {
			if mode != "" {
				m = keys(t, m, mode)
			}
			// Below the tab bar (which names the Agents tab), and without the
			// agent-wt config directory a path may name.
			_, body, _ := strings.Cut(plain(m), "\n")
			v := strings.ToLower(strings.ReplaceAll(body, "agent-wt", ""))
			for _, word := range []string{"pair", "agent", "litellm", "plist", "listen", "master"} {
				if strings.Contains(v, word) {
					t.Errorf("mode %q: the view names %q:\n%s", m.mode.want.Mode, word, plain(m))
				}
			}
		}
	}
}

// TestModeTabAdminKeysAreInert pins that the keys of `wt-model setup`'s tab
// (k, t, s, x, R) do nothing here: no dep is called and the tab stays on
// its view.
func TestModeTabAdminKeysAreInert(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{fullState()}}
	m := modeEditor(t, mm, 120, 50)
	before := strings.Join(mm.seq, ",")
	for _, k := range []string{"k", "t", "s", "x", "R"} {
		m = keys(t, m, k)
		if m.mode.phase != modeView {
			t.Errorf("%s: phase = %d, want the view", k, m.mode.phase)
		}
	}
	if got := strings.Join(mm.seq, ","); got != before {
		t.Errorf("dep calls = %s, want %s unchanged", got, before)
	}
	for _, k := range []string{"k key", "t tighten", "s/x/R"} {
		if strings.Contains(hintLine(m), k) {
			t.Errorf("hints %q name %q", hintLine(m), k)
		}
	}
}

// TestModeTabFitsTheTerminal measures every state of the tab at every
// supported size (L13): nothing is taller or wider than the terminal, and
// the hint line is the last line.
func TestModeTabFitsTheTerminal(t *testing.T) {
	long := fullState()
	long.Config.Problems = append(long.Config.Problems, strings.Repeat("a long problem that wraps ", 12))
	long.Token.Path = "/Users/dev/" + strings.Repeat("deeply-nested-directory/", 6) + "server.token"
	states := map[string]modelmode.State{
		"empty": {}, "full": fullState(), "long": long, "parse error": parseErrState(), "remote": failedRemoteState(),
	}
	for name, st := range states {
		for _, size := range tabSizes {
			m := modeEditor(t, &modeMachine{states: []modelmode.State{st}}, size[0], size[1])
			assertFits(t, name, m.View(), size[0], size[1])
			if last := hintLine(m); !strings.Contains(last, "q quit") {
				t.Errorf("%s at %dx%d: the last line is %q, want the hints", name, size[0], size[1], last)
			}
		}
	}
	// While the first observation is out: the tab bar and a line, no more.
	m := newModel(testTheme(), &config.Config{}, nil)
	m.opts, m.tab = Options{Mode: (&modeMachine{}).deps()}, TabMode
	if v := m.modeTabView(); v != tabBar(testTheme(), TabMode) {
		t.Errorf("before the first WindowSizeMsg the view is %q, want only the tab bar", v)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	m = send(t, m, loadedMsg{})
	assertFits(t, "loading", m.View(), 40, 12)
}

// TestCtrlSOnTheModeTabDoesNothing pins that ctrl+s, the Agents tab's save,
// neither saves nor observes nor plans on this tab.
func TestCtrlSOnTheModeTabDoesNothing(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{fullState()}}
	m := modeEditor(t, mm, 80, 24)
	m.dirty = true
	next, cmd := m.Update(keyMsg("ctrl+s"))
	m = next.(*model)
	if cmd != nil || m.saving || !m.dirty || mm.observes != 1 || mm.plans != 0 {
		t.Errorf("ctrl+s: cmd = %v saving = %v dirty = %v observes = %d plans = %d", cmd != nil, m.saving, m.dirty, mm.observes, mm.plans)
	}
}

// TestModeTabQuitKeysUseTheExistingQuit pins that q and ctrl+c leave
// through the editor's quit: straight out when nothing is pending, and by
// the unsaved-changes prompt for agent edits.
func TestModeTabQuitKeysUseTheExistingQuit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}}, 80, 24)
		_, cmd := m.Update(keyMsg(k))
		if !isQuit(cmd) {
			t.Errorf("%s: the command is not tea.Quit", k)
		}
		m = modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}}, 80, 24)
		m.dirty = true
		next, _ := m.Update(keyMsg(k))
		if next.(*model).phase != phaseQuit {
			t.Errorf("%s with agent edits: phase = %d, want the quit prompt", k, next.(*model).phase)
		}
	}
}

// TestModeDepsWithDefaultsFillsOnlyNilFuncs pins that Run keeps a dep the
// caller injected and fills only the missing ones.
func TestModeDepsWithDefaultsFillsOnlyNilFuncs(t *testing.T) {
	mm := &modeMachine{}
	d := ModeDeps{Observe: mm.deps().Observe}.withDefaults()
	if d.Plan == nil || d.Apply == nil {
		t.Fatalf("withDefaults left a nil func")
	}
	d.Observe(context.Background(), modelmode.ObserveOptions{})
	if mm.observes != 1 {
		t.Errorf("the injected Observe was replaced")
	}
}

// TestModeTabViewIsASCII pins V5: state is drawn in ASCII ((x), ( ), ok,
// problem:), never a check mark or a bullet; only the prose glyphs the style
// guide allows (· — …) may appear.
func TestModeTabViewIsASCII(t *testing.T) {
	for i, st := range []modelmode.State{fullState(), parseErrState(), remoteState(), {}} {
		v := plain(modeEditor(t, &modeMachine{states: []modelmode.State{st}}, 120, 50))
		if anchor := []string{"(x) local", "config.toml does not parse", "(x) remote", "( ) native"}[i]; !strings.Contains(v, anchor) {
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

// TestModeTabNeverShowsASecret pins that no token, pasted secret or url
// password reaches the screen, even when a State or Desired a dep hands back
// carries one.
func TestModeTabNeverShowsASecret(t *testing.T) {
	st := fullState()
	st.Config.Table.Token = "tok-INLINE-SECRET"
	st.Config.Table.URL = "https://me:pw-URL-SECRET@gpu.example:8787/"
	st.Server.Message = "model server at https://me:pw-MSG-SECRET@gpu.example:8787 is not reachable"
	for _, size := range [][2]int{{120, 50}, {40, 12}} {
		m := modeEditor(t, &modeMachine{states: []modelmode.State{st}}, size[0], size[1])
		m.mode.want.PastedToken = "tok-PASTED-SECRET"
		v := plain(m)
		if size[0] == 120 && (!strings.Contains(v, "(x) local") || !strings.Contains(v, "gpu.example:8787")) {
			t.Errorf("at %dx%d the view lacks the mode choice or the url:\n%s", size[0], size[1], v)
		}
		for _, secret := range []string{"INLINE-SECRET", "URL-SECRET", "MSG-SECRET", "PASTED-SECRET", "me:pw"} {
			if strings.Contains(v, secret) {
				t.Errorf("at %dx%d the view shows %q:\n%s", size[0], size[1], secret, v)
			}
		}
	}
}

// TestModeTabAlwaysReObservesBeforePlanning pins that a reads the machine
// again, with the form's values, before it plans: after the url is edited,
// the observation checks the new url, and Plan is given that fresh
// observation and the edited url. A plan from a stale read could store a url
// whose check was of another one.
func TestModeTabAlwaysReObservesBeforePlanning(t *testing.T) {
	fresh := failedRemoteState()
	fresh.Server = modelmode.ServerState{URL: "https://other.example:9999", Checked: true, OK: true}
	mm := &modeMachine{states: []modelmode.State{failedRemoteState(), fresh}, plan: okPlan()}
	m := modeEditor(t, mm, 80, 24)
	m = keys(t, m, "down", "ctrl+u", "https://other.example:9999", "esc")
	m = keys(t, m, "a")
	if got := strings.Join(mm.seq, ","); got != "observe,observe,plan" {
		t.Fatalf("dep calls = %s, want observe,observe,plan", got)
	}
	if o := mm.opts[1]; o.CheckURL != "https://other.example:9999" {
		t.Errorf("the observation before planning checked %q, want the edited url", o.CheckURL)
	}
	if mm.desired[0].URL != "https://other.example:9999" || mm.planned[0].Server.URL != "https://other.example:9999" {
		t.Errorf("Plan got url %q and a state checked at %q, want the edited url and the fresh observation",
			mm.desired[0].URL, mm.planned[0].Server.URL)
	}
	if m.mode.phase != modeConfirm {
		t.Errorf("phase = %d, want the confirm screen", m.mode.phase)
	}
	// Every a reads again, even straight after the last one.
	keys(t, m, "n", "a")
	if got := strings.Join(mm.seq, ","); got != "observe,observe,plan,observe,plan" {
		t.Errorf("dep calls after a second a = %s", got)
	}
}

// TestModeTabRemotePingsTheEnteredURLBeforePlanning pins that for remote the
// observation before planning pings the url and token the user ENTERED, so
// the plan's check gate is about the server the user typed and not the one
// stored before.
func TestModeTabRemotePingsTheEnteredURLBeforePlanning(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{remoteState()}}
	m := modeEditor(t, mm, 120, 50)
	keys(t, m, "down", "https://gpu.example:8787", "down", "~/remote.token", "down", "tok-PASTED", "esc", "a")
	if got := strings.Join(mm.seq, ","); got != "observe,observe,plan" {
		t.Fatalf("dep calls = %s, want observe,observe,plan", got)
	}
	o := mm.opts[1]
	if o.CheckURL != "https://gpu.example:8787" || o.CheckToken.Reveal() != "tok-PASTED" || o.CheckTokenFile != "~/remote.token" {
		t.Errorf("the observation before planning checked %q with token %q / file %q, want the entered ones",
			o.CheckURL, o.CheckToken.Reveal(), o.CheckTokenFile)
	}
	if len(o.TokenPaths) != 1 || o.TokenPaths[0] != "~/remote.token" {
		t.Errorf("TokenPaths = %q, want the typed token file described", o.TokenPaths)
	}
	d := mm.desired[0]
	if d.Mode != modelmode.ModeRemote || d.URL != "https://gpu.example:8787" || d.TokenFile != "~/remote.token" || d.PastedToken.Reveal() != "tok-PASTED" {
		t.Errorf("Plan got mode %q url %q token file %q, want the entered values", d.Mode, d.URL, d.TokenFile)
	}
}

// TestModeTabDisablesWritesOnAParseError pins that a config.toml that does
// not parse disables the write: a only says so, naming the file and that
// nothing was changed, and Plan and Apply are never called. A parse error
// the observation before planning finds stops a too.
func TestModeTabDisablesWritesOnAParseError(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{parseErrState()}}
	m := modeEditor(t, mm, 80, 24)
	m = keys(t, m, "a")
	if s := m.mode.status; !strings.Contains(s, "/Users/dev/.config/agent-wt/config.toml") || !strings.Contains(s, "nothing was changed") {
		t.Errorf("a: status = %q, want the path and nothing was changed", s)
	}
	if !strings.Contains(flat(plain(m)), "nothingwaschanged") {
		t.Errorf("a: the view does not say nothing was changed:\n%s", plain(m))
	}
	if mm.plans+mm.applies != 0 || mm.observes != 1 {
		t.Errorf("plans %d applies %d observes %d, want no write path and no new read", mm.plans, mm.applies, mm.observes)
	}

	mm = &modeMachine{states: []modelmode.State{fullState(), parseErrState()}}
	m = modeEditor(t, mm, 80, 24)
	m = keys(t, m, "a")
	if mm.plans != 0 || m.mode.phase != modeView || !strings.Contains(m.mode.status, "config.toml") || !strings.Contains(m.mode.status, "nothing was changed") {
		t.Errorf("a parse error found before planning: plans = %d phase = %d status = %q", mm.plans, m.mode.phase, m.mode.status)
	}
}

// TestModeTabConfirmIsDefaultNo pins P1 on the plan's confirm screen: only y
// or Y applies; n, N and esc cancel; every other key, enter and q among
// them, is ignored and the prompt stays. ctrl+c quits.
func TestModeTabConfirmIsDefaultNo(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{fullState()}, plan: okPlan()}
	m := modeEditor(t, mm, 120, 50)
	m = keys(t, m, "a")
	v := plain(m)
	if m.mode.phase != modeConfirm || !strings.Contains(v, "Apply this plan? [y/N]") || !strings.Contains(v, "create the token file") {
		t.Fatalf("after a: phase = %d, want the plan and the question:\n%s", m.mode.phase, v)
	}
	for _, k := range []string{"enter", " ", "q", "a", "s", "x", "R", "r", "tab", "left", "down", "esc!", "j", "1"} {
		m = keys(t, m, k)
		if m.mode.phase != modeConfirm || m.tab != TabMode || mm.applies != 0 {
			t.Fatalf("%q left the prompt: phase = %d tab = %d applies = %d", k, m.mode.phase, m.tab, mm.applies)
		}
	}
	for _, k := range []string{"n", "N", "esc"} {
		m = keys(t, m, k)
		if m.mode.phase != modeView || mm.applies != 0 || !strings.Contains(m.mode.status, "nothing was changed") {
			t.Errorf("%s: phase = %d applies = %d status = %q, want cancelled", k, m.mode.phase, mm.applies, m.mode.status)
		}
		m = keys(t, m, "a")
	}
	_, cmd := m.Update(keyMsg("ctrl+c"))
	if !isQuit(cmd) || mm.applies != 0 {
		t.Errorf("ctrl+c on the confirm screen did not quit, or applied")
	}
}

// TestModeTabAppliesAfterY pins that y runs the confirmed plan through
// Apply, then says what ran (the progress lines and the outcome) and reads
// the machine again; and that a failure names the step and its error.
func TestModeTabAppliesAfterY(t *testing.T) {
	for _, yes := range []string{"y", "Y"} {
		mm := &modeMachine{states: []modelmode.State{remoteState()}, plan: okPlan(),
			progress: []string{"step 1/2: token", "step 2/2: config"},
			result:   modelmode.Result{Done: okPlan().Steps, Warnings: []string{"the environment overrides it"}}}
		m := modeEditor(t, mm, 120, 50)
		m = keys(t, m, "a", yes)
		if mm.applies != 1 || len(mm.applied[0].Steps) != 2 || m.mode.phase != modeView || m.mode.busy || m.mode.writing {
			t.Fatalf("%s: applies = %d phase = %d busy = %v writing = %v", yes, mm.applies, m.mode.phase, m.mode.busy, m.mode.writing)
		}
		if got := strings.Join(mm.seq, ","); got != "observe,observe,plan,apply,observe" {
			t.Errorf("%s: dep calls = %s, want a re-read after the apply", yes, got)
		}
		v := flat(plain(m))
		for _, want := range []string{"step2/2:config", "applied", "theenvironmentoverridesit"} {
			if !strings.Contains(v, want) {
				t.Errorf("%s: the view lacks %q:\n%s", yes, want, plain(m))
			}
		}
	}

	cfg := modelmode.Step{Kind: modelmode.StepConfig}
	mm := &modeMachine{states: []modelmode.State{remoteState()}, plan: okPlan(),
		result: modelmode.Result{Done: okPlan().Steps[:1], Failed: &cfg, Err: errors.New("permission denied")}}
	m := modeEditor(t, mm, 120, 50)
	m = keys(t, m, "a", "y")
	if s := m.mode.status; !strings.Contains(s, "config step") || !strings.Contains(s, "permission denied") || !strings.Contains(s, "done before it: token") || strings.Contains(s, "applied:") {
		t.Errorf("a failed step: status = %q", s)
	}
}

// TestModeTabRefusalIsShownVerbatim pins that a plan Plan refuses is shown
// as its own words, every reason, and that nothing is applied.
func TestModeTabRefusalIsShownVerbatim(t *testing.T) {
	ref := &modelmode.RefusalError{Reasons: []string{
		"the url is not http or https; type a url like https://host:8787; nothing was changed",
		"token file /x is readable by others; run chmod 600 /x; nothing was changed",
	}}
	mm := &modeMachine{states: []modelmode.State{fullState()}, planErr: ref}
	m := modeEditor(t, mm, 120, 50)
	m = keys(t, m, "a")
	if m.mode.status != ref.Error() {
		t.Errorf("status = %q, want the refusal verbatim %q", m.mode.status, ref.Error())
	}
	if m.mode.phase != modeView || mm.applies != 0 {
		t.Errorf("phase = %d applies = %d, want the tab and nothing applied", m.mode.phase, mm.applies)
	}
	v := flat(plain(m))
	for _, r := range ref.Reasons {
		if !strings.Contains(v, flat(r)) {
			t.Errorf("the view lacks %q:\n%s", r, plain(m))
		}
	}
}

// TestModeTabOffersSaveAnywayDefaultNoAfterAFailedRemoteCheck pins the
// remote check's way past a server that is off: after a failed check the tab
// asks "Save anyway? [y/N]", and only y sets SaveUnchecked. Other keys leave
// the question up, esc plans nothing, and a check that passed asks nothing.
func TestModeTabOffersSaveAnywayDefaultNoAfterAFailedRemoteCheck(t *testing.T) {
	for answer, want := range map[string]bool{"n": false, "N": false, "y": true, "Y": true} {
		mm := &modeMachine{states: []modelmode.State{failedRemoteState()}}
		m := modeEditor(t, mm, 120, 50)
		m = keys(t, m, "a")
		v := plain(m)
		if !strings.Contains(v, "Save anyway? [y/N]") || !strings.Contains(v, "not reachable") || mm.plans != 0 {
			t.Fatalf("%s: want the question with the failure, before any plan (plans = %d):\n%s", answer, mm.plans, v)
		}
		m = keys(t, m, "enter", "q", "z", "a")
		if mm.plans != 0 || !strings.Contains(plain(m), "Save anyway? [y/N]") {
			t.Fatalf("%s: another key answered the question", answer)
		}
		keys(t, m, answer)
		if mm.plans != 1 || mm.desired[0].SaveUnchecked != want {
			t.Errorf("%s: plans = %d SaveUnchecked = %v, want %v", answer, mm.plans, mm.desired, want)
		}
	}

	mm := &modeMachine{states: []modelmode.State{failedRemoteState()}}
	m := modeEditor(t, mm, 120, 50)
	m = keys(t, m, "a", "esc")
	if mm.plans != 0 || m.mode.phase != modeView || !strings.Contains(m.mode.status, "nothing was changed") {
		t.Errorf("esc: plans = %d phase = %d status = %q", mm.plans, m.mode.phase, m.mode.status)
	}

	ok := remoteState()
	ok.Config.Table.URL = "https://gpu.example:8787"
	ok.Server = modelmode.ServerState{URL: "https://gpu.example:8787", Checked: true, OK: true}
	mm = &modeMachine{states: []modelmode.State{ok}}
	m = modeEditor(t, mm, 120, 50)
	m = keys(t, m, "a")
	if mm.plans != 1 || mm.desired[0].SaveUnchecked || strings.Contains(plain(m), "Save anyway") {
		t.Errorf("a passed check: plans = %d desired = %+v", mm.plans, mm.desired)
	}
}

// TestModeTabQuitWaitsForAnApplyInFlight pins that a quit asked for while an
// apply is out is held until its result is in, so the result is never lost;
// that it then quits with the result kept; and that a failed apply calls the
// quit off on this tab, where the failure is drawn.
func TestModeTabQuitWaitsForAnApplyInFlight(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{remoteState()}, plan: okPlan(), result: modelmode.Result{Done: okPlan().Steps}}
	m := modeEditor(t, mm, 80, 24)
	m = keys(t, m, "a")
	next, apply := m.Update(keyMsg("y"))
	m = next.(*model)
	if apply == nil || mm.applies != 0 || !m.mode.writing {
		t.Fatalf("y: cmd = %v applies = %d writing = %v, want the apply handed to a command", apply != nil, mm.applies, m.mode.writing)
	}
	m = keys(t, m, "tab")
	next, cmd := m.Update(keyMsg("q"))
	m = next.(*model)
	if cmd != nil || !m.quitPending || m.tab != TabMode {
		t.Fatalf("q during the apply: cmd = %v quitPending = %v tab = %d, want the quit held on the Mode tab", cmd != nil, m.quitPending, m.tab)
	}
	if !strings.Contains(flat(plain(m)), "quit") || !strings.Contains(flat(plain(m)), "apply") {
		t.Errorf("the tab does not say the quit is waiting for the apply:\n%s", plain(m))
	}
	next, cmd = m.Update(apply())
	m = next.(*model)
	if !isQuit(cmd) {
		t.Errorf("the held quit was not issued")
	}
	if !strings.Contains(m.mode.status, "applied") {
		t.Errorf("status = %q, want the result kept", m.mode.status)
	}

	// A failed apply: the quit is called off and the failure is shown here.
	cfg := modelmode.Step{Kind: modelmode.StepConfig}
	mm = &modeMachine{states: []modelmode.State{remoteState()}, plan: okPlan(), result: modelmode.Result{Failed: &cfg, Err: errors.New("disk full")}}
	m = modeEditor(t, mm, 80, 24)
	m = keys(t, m, "a")
	next, apply = m.Update(keyMsg("y"))
	m = next.(*model)
	m = keys(t, m, "tab")
	next, _ = m.Update(keyMsg("q"))
	m = next.(*model)
	m = send(t, m, apply())
	if m.quitPending || m.tab != TabMode || !strings.Contains(m.mode.status, "disk full") {
		t.Errorf("a failed apply: quitPending = %v tab = %d status = %q", m.quitPending, m.tab, m.mode.status)
	}
}

// TestADiscardHeldForAModeApplySurvivesItsFailure pins discardHeld: "discard
// and quit" answered while an apply is out clears dirty while the quit
// waits, and a failed apply calls the quit off and puts the edits back, so
// they are not lost without being asked about again.
func TestADiscardHeldForAModeApplySurvivesItsFailure(t *testing.T) {
	cfg := modelmode.Step{Kind: modelmode.StepConfig}
	mm := &modeMachine{states: []modelmode.State{remoteState()}, plan: okPlan(), result: modelmode.Result{Failed: &cfg, Err: errors.New("disk full")}}
	m := modeEditor(t, mm, 80, 24)
	m = keys(t, m, "a")
	next, apply := m.Update(keyMsg("y"))
	m = next.(*model)
	m.dirty = true
	next, _ = m.Update(keyMsg("q"))
	m = next.(*model)
	if m.phase != phaseQuit {
		t.Fatalf("q with agent edits: phase = %d, want the quit prompt", m.phase)
	}
	next, cmd := m.Update(keyMsg("n"))
	m = next.(*model)
	if cmd != nil || !m.quitPending || m.dirty || !m.discardHeld {
		t.Fatalf("n while the apply is out: quitPending = %v dirty = %v discardHeld = %v", m.quitPending, m.dirty, m.discardHeld)
	}
	m = send(t, m, apply())
	if m.quitPending || !m.dirty || m.discardHeld {
		t.Errorf("after the failed apply: quitPending = %v dirty = %v discardHeld = %v, want the edits back", m.quitPending, m.dirty, m.discardHeld)
	}
}

// TestModeTabMasksThePastedTokenAndTheKey pins that the pasted token is
// drawn masked while typed, never appears on the confirm screen or in a
// status, reaches Apply through the plan, and is cleared from its input once
// an apply succeeded. (The master key is `wt-model setup`'s.)
func TestModeTabMasksThePastedTokenAndTheKey(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{remoteState()}}
	m := modeEditor(t, mm, 120, 50)
	m = keys(t, m, "down", "https://gpu.example:8787", "down", "down", "tok-PASTED-SECRET")
	if v := plain(m); strings.Contains(v, "PASTED-SECRET") || !strings.Contains(v, "*****") {
		t.Errorf("while typed the pasted token shows or is not masked:\n%s", v)
	}
	m = keys(t, m, "esc", "a")
	if v := plain(m); m.mode.phase != modeConfirm || strings.Contains(v, "PASTED-SECRET") {
		t.Errorf("confirm screen (phase %d) shows the token:\n%s", m.mode.phase, v)
	}
	m = keys(t, m, "y")
	if mm.applies != 1 || mm.applied[0].Want.PastedToken.Reveal() != "tok-PASTED-SECRET" {
		t.Errorf("the pasted token did not reach Apply")
	}
	if v := plain(m); strings.Contains(v, "PASTED-SECRET") || strings.Contains(m.mode.status, "PASTED-SECRET") {
		t.Errorf("after the apply the token shows:\n%s", v)
	}
	if got := m.mode.fields[mfPaste].Value(); got != "" {
		t.Errorf("the pasted-token input still holds %d characters after a successful apply", len(got))
	}
}

// TestModeTabFitsTheTerminalInEveryPhase measures every phase of the tab
// (the status view of each mode, the remote fields, the confirm screen with
// a long plan, both questions, an apply in flight, a long result and a parse
// error) at every supported size: nothing taller or wider than the
// terminal, and the hints on the last line.
func TestModeTabFitsTheTerminalInEveryPhase(t *testing.T) {
	failed := modelmode.Step{Kind: modelmode.StepConfig}
	result := modelmode.Result{Failed: &failed, Warnings: []string{strings.Repeat("a long warning ", 8), "another"},
		Err: errors.New(strings.Repeat("a long error ", 10))}
	progress := []string{"step 1/2: token", "step 2/2: config"}
	native := fullState()
	native.Resolution.Mode = modelmode.ModeNative

	phases := map[string]func(*testing.T, int, int) *model{
		"native": func(t *testing.T, w, h int) *model {
			return modeEditor(t, &modeMachine{states: []modelmode.State{native}}, w, h)
		},
		"local": func(t *testing.T, w, h int) *model {
			return modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}}, w, h)
		},
		"remote": func(t *testing.T, w, h int) *model {
			return modeEditor(t, &modeMachine{states: []modelmode.State{failedRemoteState()}}, w, h)
		},
		"remote fields": func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{remoteState()}}, w, h), "down", "https://gpu.example:8787")
		},
		"confirm": func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}, plan: longPlan()}, w, h), "a")
		},
		"save anyway": func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{failedRemoteState()}}, w, h), "a")
		},
		"run setup": func(t *testing.T, w, h int) *model {
			mm := &modeMachine{states: []modelmode.State{remoteState()}, plan: longPlan(), progress: progress, result: modelmode.Result{Done: okPlan().Steps}}
			return keys(t, modeEditorWith(t, mm, onPath, w, h), "left", "a", "y")
		},
		"applying": func(t *testing.T, w, h int) *model {
			m := keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}, plan: longPlan()}, w, h), "a")
			next, _ := m.Update(keyMsg("y"))
			next, _ = next.(*model).Update(keyMsg("q"))
			return next.(*model)
		},
		"result": func(t *testing.T, w, h int) *model {
			mm := &modeMachine{states: []modelmode.State{fullState()}, plan: longPlan(), result: result, progress: progress}
			return keys(t, modeEditor(t, mm, w, h), "a", "y")
		},
		"parse error": func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{parseErrState()}}, w, h), "a")
		},
	}
	// reached says the phase was really reached, so a fit is not measured on
	// the wrong screen.
	reached := map[string]func(*model) bool{
		"native":        func(m *model) bool { return m.mode.phase == modeView && m.mode.want.Mode == modelmode.ModeNative },
		"local":         func(m *model) bool { return m.mode.phase == modeView && m.mode.want.Mode == modelmode.ModeLocal },
		"remote":        func(m *model) bool { return m.mode.phase == modeView && m.mode.want.Mode == modelmode.ModeRemote },
		"remote fields": func(m *model) bool { return m.mode.phase == modeEdit && m.mode.field == mfURL },
		"confirm":       func(m *model) bool { return m.mode.phase == modeConfirm && m.mode.prompt == promptApply },
		"save anyway":   func(m *model) bool { return m.mode.phase == modeConfirm && m.mode.prompt == promptSaveAnyway },
		"run setup":     func(m *model) bool { return m.mode.phase == modeConfirm && m.mode.prompt == promptRunSetup },
		"applying":      func(m *model) bool { return m.mode.phase == modeApplying && m.mode.writing && m.quitPending },
		"result":        func(m *model) bool { return strings.Contains(m.mode.status, "a long error") },
		"parse error": func(m *model) bool {
			return m.mode.state.Config.ParseErr != "" && strings.Contains(m.mode.status, "nothing was changed")
		},
	}
	for name, open := range phases {
		for _, size := range tabSizes {
			m := open(t, size[0], size[1])
			if !reached[name](m) {
				t.Errorf("%s at %dx%d: the phase was not reached (phase %d)", name, size[0], size[1], m.mode.phase)
			}
			assertFits(t, name, m.View(), size[0], size[1])
			if last := hintLine(m); !strings.Contains(last, "quit") {
				t.Errorf("%s at %dx%d: the last line is %q, want the hints", name, size[0], size[1], last)
			}
		}
	}
}

// hiddenURLText is urlrule's placeholder for a url holding an '@'.
const hiddenURLText = "(hidden: the url contains an @)"

// credURLState is a session stored in remote mode whose url held an '@',
// which Observe shows only as the placeholder (URLHadCredentials), with no
// check. No Problems line: the tab must not need its wording.
func credURLState() modelmode.State {
	st := remoteState()
	st.Config.Table.URL = hiddenURLText
	st.Config.URLHadCredentials = true
	return st
}

// retypeURL are the keys that replace the prefilled url with a plain one,
// then paste tok into the token row and ask for the plan.
func retypeURL(tok string) []string {
	return []string{"down", "ctrl+u", "https://gpu.example:8787/", "down", "down", tok, "esc", "a"}
}

// TestModeTabConfirmSaysTheURLsCredentialsAreNotKept pins, through the real
// modelmode.BuildPlan, that when the stored url had credentials
// (ConfigState.URLHadCredentials) and the plan writes the url
// (Plan.WritesURL), the confirm screen says the credentials will not be
// kept. Neither fact is read from text. No note when the url had none.
func TestModeTabConfirmSaysTheURLsCredentialsAreNotKept(t *testing.T) {
	const note = "credentialsarenotkept"
	mm := &modeMachine{states: []modelmode.State{credURLState()}, build: true}
	m := keys(t, modeEditor(t, mm, 120, 50), retypeURL("tok-NEW")...)
	if m.mode.phase != modeConfirm || !strings.Contains(flat(plain(m)), note) {
		t.Errorf("phase %d status %q: a url write over a url with credentials, no note:\n%s", m.mode.phase, m.mode.status, plain(m))
	}
	clean := credURLState()
	clean.Config.URLHadCredentials = false
	mm = &modeMachine{states: []modelmode.State{clean}, build: true}
	m = keys(t, modeEditor(t, mm, 120, 50), retypeURL("tok-NEW")...)
	if m.mode.phase != modeConfirm || strings.Contains(flat(plain(m)), note) {
		t.Errorf("phase %d: no credentials, yet the note:\n%s", m.mode.phase, plain(m))
	}
}

// TestModeTabRealPlanNeverShowsTheSecrets builds the plan with the real
// modelmode.BuildPlan from a Desired that carries a pasted token, and pins
// that neither the plan's Format nor the confirm screen holds the value,
// while the plan's Want still carries it for Apply.
func TestModeTabRealPlanNeverShowsTheSecrets(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{credURLState()}, build: true}
	m := keys(t, modeEditor(t, mm, 120, 50), retypeURL("tok-FAKE-SECRET")...)
	p, err := modelmode.BuildPlan(mm.planned[0], mm.desired[0])
	if err != nil || p.Want.PastedToken.Reveal() != "tok-FAKE-SECRET" {
		t.Fatalf("real remote plan: err %v, or the token did not reach Want", err)
	}
	if m.mode.phase != modeConfirm || strings.Contains(plain(m), "FAKE-SECRET") || strings.Contains(p.Format(), "FAKE-SECRET") {
		t.Errorf("phase %d: the pasted token is on the confirm screen or in Format:\n%s", m.mode.phase, plain(m))
	}
}

// TestModeTabFormIsInertWhileAPlanIsPrepared pins that nothing on the form
// changes while the read and the plan a started are out: the mode keys and
// the key into the fields only say why they wait, so typed text can neither
// change what is planned nor land on a question that appears meanwhile (a y
// typed into a url must never confirm).
func TestModeTabFormIsInertWhileAPlanIsPrepared(t *testing.T) {
	st := remoteState()
	st.Config.Table.URL = "https://gpu.example:8787"
	mm := &modeMachine{states: []modelmode.State{st}}
	m := modeEditor(t, mm, 120, 50)
	next, observe := m.Update(keyMsg("a"))
	m = next.(*model)
	for _, k := range []string{"down", "xy", "y", "left", "right", " ", "enter"} {
		next, _ = m.Update(keyMsg(k))
		m = next.(*model)
	}
	if m.mode.phase != modeView || m.mode.want.Mode != modelmode.ModeRemote || m.mode.fields[mfURL].Value() != "https://gpu.example:8787" {
		t.Fatalf("while the plan was prepared: phase %d mode %q url %q, want the form unchanged",
			m.mode.phase, m.mode.want.Mode, m.mode.fields[mfURL].Value())
	}
	if !strings.Contains(m.mode.status, "wait") {
		t.Errorf("status = %q, want it to say the form waits", m.mode.status)
	}
	if h := hintLine(m); strings.Contains(h, "left/right") || strings.Contains(h, "down") {
		t.Errorf("hints %q name keys that do nothing while busy", h)
	}
	send(t, m, observe())
	if mm.applies != 0 || mm.plans != 1 || mm.desired[0].URL != "https://gpu.example:8787" {
		t.Errorf("applies %d plans %d, want one plan of the url as it was and no apply", mm.applies, mm.plans)
	}
}

// TestModeTabPlansTheSnapshotTakenAtA pins that the read a starts and the
// plan after it use one snapshot of the form, taken when a was pressed: a
// field changed between the read and the plan does not reach Plan, and the
// save-anyway answer given about the url that was checked goes with that
// url.
func TestModeTabPlansTheSnapshotTakenAtA(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{failedRemoteState()}}
	m := modeEditor(t, mm, 120, 50)
	next, observe := m.Update(keyMsg("a"))
	m = next.(*model)
	m.mode.fields[mfURL].SetValue("https://other.example:9999")
	m = send(t, m, observe())
	if !strings.Contains(plain(m), "Save anyway? [y/N]") {
		t.Fatalf("want the save-anyway question:\n%s", plain(m))
	}
	m.mode.fields[mfURL].SetValue("https://third.example:7777")
	keys(t, m, "y")
	if mm.opts[1].CheckURL != "https://gpu.example:8787" {
		t.Errorf("checked %q, want the url at a", mm.opts[1].CheckURL)
	}
	if mm.plans != 1 || mm.desired[0].URL != "https://gpu.example:8787" || !mm.desired[0].SaveUnchecked {
		t.Errorf("Plan got url %q SaveUnchecked %v, want the snapshot's url with the answer", mm.desired[0].URL, mm.desired[0].SaveUnchecked)
	}
}

// TestModeTabScrubsTheResolutionProblem pins that the mode resolution's
// problem text is scrubbed like every other free text the tab draws.
func TestModeTabScrubsTheResolutionProblem(t *testing.T) {
	st := fullState()
	st.Resolution = modelmode.Resolution{Problem: "WT_MODEL_URL https://me:pw-PROB-SECRET@gpu.example:8787 has no token"}
	v := plain(modeEditor(t, &modeMachine{states: []modelmode.State{st}}, 120, 50))
	if strings.Contains(v, "PROB-SECRET") || !strings.Contains(v, "gpu.example:8787") {
		t.Errorf("the resolution problem is not scrubbed, or not drawn:\n%s", v)
	}
}

// TestModeTabHintsNameOnlyKeysThatAct pins T5: a key is in the hints only
// when pressing it acts. Before the first observation the mode keys do
// nothing; with a parse error a refuses; the fields' key only for remote;
// the admin keys never.
func TestModeTabHintsNameOnlyKeysThatAct(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.opts, m.tab = Options{Mode: (&modeMachine{}).deps()}, TabMode
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = send(t, m, loadedMsg{})
	if h := hintLine(m); strings.Contains(h, "left/right") || strings.Contains(h, "a apply") || !strings.Contains(h, "r re-read") {
		t.Errorf("before the first observation the hints are %q", h)
	}
	native := fullState()
	native.Resolution.Mode = modelmode.ModeNative
	cases := []struct {
		name     string
		st       modelmode.State
		has, not []string
	}{
		{"local", fullState(), []string{"left/right", "a apply", "c check"}, []string{"down", "s/x/R", "k key"}},
		{"native", native, []string{"left/right", "a apply"}, []string{"down", "s/x/R"}},
		{"remote", remoteState(), []string{"a apply", "down"}, []string{"s/x/R", "t tighten"}},
		{"parse error", parseErrState(), []string{"r re-read"}, []string{"a apply"}},
	}
	for _, c := range cases {
		h := hintLine(modeEditor(t, &modeMachine{states: []modelmode.State{c.st}}, 120, 24))
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
}

// TestModeTabCallsApplyAndControlOnlyFromCommands pins that Update hands
// Plan and Apply to commands that captured the deps when they were made;
// nothing is called until the command runs, and a dep swapped afterwards is
// not the one called.
func TestModeTabCallsApplyAndControlOnlyFromCommands(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{remoteState()}, plan: okPlan()}
	m := modeEditor(t, mm, 80, 24)
	m = keys(t, m, "a")
	next, apply := m.Update(keyMsg("y"))
	m = next.(*model)
	if apply == nil || mm.applies != 0 {
		t.Fatalf("Update called a dep: applies = %d", mm.applies)
	}
	other := &modeMachine{}
	m.opts.Mode = other.deps()
	apply()
	if mm.applies != 1 || other.applies != 0 {
		t.Errorf("the apply command did not use the deps captured with it: %d / %d", mm.applies, other.applies)
	}
}

// TestModeTabSourceNeverFormatsAPlanWithV pins that the tab's code never
// formats a value with %v, %+v or %#v, so a Plan or a Desired (which carry
// secrets, redacted or not) is never printed whole.
func TestModeTabSourceNeverFormatsAPlanWithV(t *testing.T) {
	verb := regexp.MustCompile(`%[+#]?v`)
	files, err := filepath.Glob("mode_*.go")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		if loc := verb.FindIndex(src); loc != nil {
			t.Errorf("%s formats with %q", f, src[loc[0]:loc[1]])
		}
	}
	if scanned < 2 {
		t.Errorf("scanned %d mode_*.go files, want mode_tab.go and mode_view.go at least", scanned)
	}
}

// TestModeTabNeverRendersAPieceOfAnAtURL pins the tab's side of the rule "a
// url with an '@' is never echoed": with a State whose stored url and check
// are the placeholder and fixed sentence modelmode gives such a url, and an
// Apply whose failure is the fixed refusal, no screen (the view, the
// prefilled remote form's save-anyway question and refusal, the applied
// result, native and an entered or env url) shows any piece of the hostile
// url.
func TestModeTabNeverRendersAPieceOfAnAtURL(t *testing.T) {
	const checkHiddenText = "the check failed; the url contains an @ so the error text is hidden"
	refusal := "write [model-server]: the url contains an @ (read as a user:password); wt does not store credentials in config.toml; use a token file; config.toml was not written"
	st := credURLState()
	st.Server = modelmode.ServerState{URL: hiddenURLText, Checked: true, Kind: modelmode.KindUnreachable, Message: checkHiddenText}
	mm := &modeMachine{states: []modelmode.State{st}, build: true}
	m := modeEditor(t, mm, 120, 50)
	screens := map[string]string{"view": plain(m)}
	m = keys(t, m, "a")
	screens["save anyway"] = plain(m)
	m = keys(t, m, "y")
	screens["prefilled plan"] = plain(m) + "\n" + m.mode.status
	if !strings.Contains(m.mode.status, "user:password") || m.mode.phase == modeConfirm {
		t.Errorf("the prefilled placeholder was not refused: phase %d status %q", m.mode.phase, m.mode.status)
	}
	// An entered url with an '@': the field and the save-anyway screen.
	mm = &modeMachine{states: []modelmode.State{remoteState()}, build: true}
	m = keys(t, modeEditor(t, mm, 120, 50), "down", "https://USERX:PASSX@gpu.example:8787", "esc")
	screens["entered"] = plain(m)
	m = keys(t, m, "right")
	screens["entered, native"] = plain(m)
	// An env url: the mode row's problem and the server row.
	env := fullState()
	env.Resolution = modelmode.Resolution{Mode: modelmode.ModeRemote, Source: modelmode.SourceEnvEndpoint}
	env.EnvOverride = "WT_MODEL_URL"
	env.Server = modelmode.ServerState{URL: hiddenURLText, Message: modelmode.NotCheckedAtURL}
	screens["env"] = plain(modeEditor(t, &modeMachine{states: []modelmode.State{env}}, 120, 50))

	failed := modelmode.Step{Kind: modelmode.StepConfig}
	mm2 := &modeMachine{states: []modelmode.State{remoteState()}, plan: okPlan(),
		result: modelmode.Result{Failed: &failed, Err: errors.New(refusal),
			Warnings: []string{"https://USERX:PASSX@gpu.example:8787", "dial http://USERX@gpu.example:8787/x: refused"}}}
	m = keys(t, modeEditor(t, mm2, 120, 50), "a", "y")
	screens["applied"] = plain(m)
	for name, v := range screens {
		for _, piece := range []string{"USERX", "PASSX"} {
			if strings.Contains(v, piece) {
				t.Errorf("%s shows %q:\n%s", name, piece, v)
			}
		}
	}
}

// TestModeTabLocalPlansThePointer pins that choosing local plans the pointer:
// Plan is given the mode alone (no url, token file or pasted token from the
// form), and the confirm screen holds exactly the real BuildPlan's text for
// that Desired and State.
func TestModeTabLocalPlansThePointer(t *testing.T) {
	st := remoteState()
	st.Resolution = modelmode.Resolution{}
	st.Config.Table = config.ModelServerState{}
	mm := &modeMachine{states: []modelmode.State{st}, build: true}
	m := modeEditor(t, mm, 120, 50)
	m = keys(t, m, "right", "right")
	if m.mode.want.Mode != modelmode.ModeLocal {
		t.Fatalf("want.Mode = %q, want local", m.mode.want.Mode)
	}
	m = keys(t, m, "a")
	if mm.plans != 1 {
		t.Fatalf("plans = %d status %q", mm.plans, m.mode.status)
	}
	if d := mm.desired[0]; d != (modelmode.Desired{Mode: modelmode.ModeLocal}) {
		t.Errorf("Plan got %+v, want local alone", d)
	}
	p, err := modelmode.BuildPlan(st, modelmode.Desired{Mode: modelmode.ModeLocal})
	if err != nil {
		t.Fatal(err)
	}
	if m.mode.phase != modeConfirm || !strings.Contains(flat(plain(m)), flat(p.Format())) {
		t.Errorf("the confirm screen does not hold the pointer plan %q:\n%s", p.Format(), plain(m))
	}
}

// TestModeTabRemoteToLocalNeverKeepsTheRemoteURL pins that switching from a
// stored (and an entered) remote url to local carries neither over: local
// shows no fields, Plan gets no url or token file, and the plan written is
// the local default url and token file.
func TestModeTabRemoteToLocalNeverKeepsTheRemoteURL(t *testing.T) {
	st := remoteState()
	st.Config.Table = config.ModelServerState{Mode: "remote", URL: "https://gpu.example:8787", TokenFile: "/y/t"}
	mm := &modeMachine{states: []modelmode.State{st}, build: true}
	m := modeEditor(t, mm, 120, 50)
	m = keys(t, m, "down", "ctrl+u", "https://other.example:1", "esc", "left")
	if m.mode.want.Mode != modelmode.ModeLocal {
		t.Fatalf("want.Mode = %q, want local", m.mode.want.Mode)
	}
	if f := m.mode.visibleFields(); len(f) != 0 {
		t.Errorf("local shows fields %v", f)
	}
	for _, l := range m.modeFieldLines() {
		if strings.Contains(l, "gpu.example") || strings.Contains(l, "other.example") {
			t.Errorf("the form area shows %q", l)
		}
	}
	m = keys(t, m, "a")
	if d := mm.desired[0]; d.Mode != modelmode.ModeLocal || d.URL != "" || d.TokenFile != "" {
		t.Errorf("Plan got %+v, want local with no url or token file", d)
	}
	v := flat(plain(m))
	for _, want := range []string{`url = "http://` + modelmode.DefaultListen + `"`, modelmode.DefaultLocalTokenPath(st.Host.ConfigHome)} {
		if !strings.Contains(v, flat(want)) {
			t.Errorf("the confirm screen lacks %q:\n%s", want, plain(m))
		}
	}
	for _, not := range []string{"gpu.example", "/y/t", "other.example"} {
		if strings.Contains(v, not) {
			t.Errorf("the confirm screen holds %q:\n%s", not, plain(m))
		}
	}
	keys(t, m, "y")
	if mm.applies != 1 || mm.applied[0].Want.URL != "http://"+modelmode.DefaultListen {
		t.Errorf("applied %d plans, want the local default url", mm.applies)
	}
}

// onPath is a LookPath that finds wt-model.
func onPath(name string) (string, error) {
	if name == "wt-model" {
		return "/Users/user_name/.local/bin/wt-model", nil
	}
	return "", errors.New("not found")
}

// applyLocal opens the tab on a stored remote session, chooses local and
// applies the (canned) plan.
func applyLocal(t *testing.T, look func(string) (string, error)) (*model, *modeMachine) {
	t.Helper()
	mm := &modeMachine{states: []modelmode.State{remoteState()}, plan: okPlan(), result: modelmode.Result{Done: okPlan().Steps}}
	m := keys(t, modeEditorWith(t, mm, look, 120, 50), "left", "a", "y")
	if mm.applies != 1 || mm.applied[0].Want.Mode != modelmode.ModeLocal {
		t.Fatalf("applies = %d, want local applied once", mm.applies)
	}
	return m, mm
}

// TestModeTabOffersToRunWtModelSetupWhenOnPath pins the offer after local
// was applied: with wt-model on PATH the tab asks whether to run
// `wt-model setup`, and y quits with a Result asking the caller to run it
// with that path.
func TestModeTabOffersToRunWtModelSetupWhenOnPath(t *testing.T) {
	m, _ := applyLocal(t, onPath)
	if v := plain(m); !strings.Contains(v, runSetupQuestion) {
		t.Fatalf("the view lacks the question:\n%s", v)
	}
	next, cmd := m.Update(keyMsg("y"))
	m = next.(*model)
	if !isQuit(cmd) {
		t.Errorf("y: the command is not tea.Quit")
	}
	if want := (Result{RunModelSetup: true, ModelSetup: "/Users/user_name/.local/bin/wt-model"}); m.result != want {
		t.Errorf("result = %+v, want %+v", m.result, want)
	}
	// Agent edits still get their prompt first.
	m, _ = applyLocal(t, onPath)
	m.dirty = true
	next, cmd = m.Update(keyMsg("y"))
	if cmd != nil || next.(*model).phase != phaseQuit {
		t.Errorf("y with agent edits: phase = %d, want the unsaved-changes prompt", next.(*model).phase)
	}
}

// TestModeTabRunSetupIsDefaultNo pins P1 on the run-setup question: n, esc
// and enter decline (no quit, no result) and leave the pointer as the
// status.
func TestModeTabRunSetupIsDefaultNo(t *testing.T) {
	for _, k := range []string{"n", "esc", "enter"} {
		m, _ := applyLocal(t, onPath)
		next, cmd := m.Update(keyMsg(k))
		m = next.(*model)
		if isQuit(cmd) || m.result != (Result{}) || m.mode.phase != modeView {
			t.Errorf("%s: quit = %v result = %+v phase = %d", k, isQuit(cmd), m.result, m.mode.phase)
		}
		if m.mode.status != localPointerNote {
			t.Errorf("%s: status = %q, want %q", k, m.mode.status, localPointerNote)
		}
	}
}

// TestModeTabWithoutWtModelOnPathOnlyPoints pins that without wt-model on
// PATH a local apply asks nothing and only points at `wt-model setup`.
func TestModeTabWithoutWtModelOnPathOnlyPoints(t *testing.T) {
	m, _ := applyLocal(t, noWtModel)
	if m.mode.phase != modeView || strings.Contains(plain(m), runSetupQuestion) {
		t.Errorf("phase = %d, want no question:\n%s", m.mode.phase, plain(m))
	}
	if m.mode.status != localPointerNote {
		t.Errorf("status = %q, want the pointer note", m.mode.status)
	}
}

// TestModeTabLeavingLocalNotesTheAgentIsUntouched pins that leaving local
// from the session tab never asks about the LaunchAgent: the real plan
// notes it is untouched and names `wt-model setup`.
func TestModeTabLeavingLocalNotesTheAgentIsUntouched(t *testing.T) {
	mm := &modeMachine{states: []modelmode.State{fullState()}, build: true}
	m := keys(t, modeEditor(t, mm, 120, 50), "left", "a")
	if mm.plans != 1 || m.mode.phase != modeConfirm || m.mode.prompt != promptApply {
		t.Fatalf("plans = %d phase = %d prompt = %d status %q, want the plan's confirm with no question first", mm.plans, m.mode.phase, m.mode.prompt, m.mode.status)
	}
	if v := flat(plain(m)); !strings.Contains(v, flat("wt-model's LaunchAgent is untouched; remove it with `wt-model setup`")) || strings.Contains(v, "RemovetheLaunchAgent") {
		t.Errorf("the plan does not note the agent is untouched:\n%s", plain(m))
	}
}

// phasesForFit builds every non-prompt and prompt phase of the Mode tab at a
// size, for the clipping test.
func phasesForFit() map[string]func(*testing.T, int, int) *model {
	return map[string]func(*testing.T, int, int) *model{
		"view": func(t *testing.T, w, h int) *model {
			return modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}}, w, h)
		},
		"edit": func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{failedRemoteState()}}, w, h), "down", "down")
		},
		"confirm": func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}, plan: longPlan()}, w, h), "a")
		},
		"save anyway": func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{failedRemoteState()}}, w, h), "a")
		},
		"applying": func(t *testing.T, w, h int) *model {
			m := keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}, plan: longPlan()}, w, h), "a")
			next, _ := m.Update(keyMsg("y"))
			next, _ = next.(*model).Update(keyMsg("q"))
			return next.(*model)
		},
		"parse error": func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{parseErrState()}}, w, h), "a")
		},
	}
}

// TestModeTabClippedViewKeepsRadiosHintsAndWholeRows pins the clipped view
// of every phase at 40x12 and 80x24: the mode choice stays whole (all three
// options) outside the prompts, the hints are the last line, no row is left
// as an orphan continuation under a marker or a blank, and nothing is wider
// or taller than the terminal.
func TestModeTabClippedViewKeepsRadiosHintsAndWholeRows(t *testing.T) {
	radio := regexp.MustCompile(`\([x ]\) native.*\([x ]\) local.*\([x ]\) remote`)
	for name, open := range phasesForFit() {
		for _, size := range [][2]int{{40, 12}, {80, 24}} {
			m := open(t, size[0], size[1])
			assertFits(t, name, m.View(), size[0], size[1])
			if last := hintLine(m); !strings.Contains(last, "quit") {
				t.Errorf("%s at %dx%d: the last line is %q, want the hints", name, size[0], size[1], last)
			}
			if m.mode.phase == modeConfirm {
				continue
			}
			v := plain(m)
			if !radio.MatchString(strings.Join(strings.Fields(strings.ReplaceAll(v, "\n", " ")), " ")) {
				t.Errorf("%s at %dx%d: the mode choice is not whole:\n%s", name, size[0], size[1], v)
			}
			lines := strings.Split(v, "\n")
			for i, l := range lines {
				if strings.HasPrefix(l, strings.Repeat(" ", editorkit.LabelW)) && strings.TrimSpace(l) != "" && i > 0 {
					if prev := strings.TrimSpace(lines[i-1]); prev == "" || strings.Contains(prev, "not shown") {
						t.Errorf("%s at %dx%d: orphan continuation line %q after %q:\n%s", name, size[0], size[1], l, lines[i-1], v)
					}
				}
			}
		}
	}
}

// TestModeTabDropsLowestPriorityRowsFirstAndMarksTheEnd pins the order in
// which a short terminal gives up status rows (env, token, url, problems,
// server, mode last), that the marker names how many rows went and stands
// after the last row kept.
func TestModeTabDropsLowestPriorityRowsFirstAndMarksTheEnd(t *testing.T) {
	order := modeDropOrder
	marker := regexp.MustCompile(`\.\.\. (\d+) status rows? not shown \.\.\.`)
	sawDrop := false
	for h := 6; h <= 30; h++ {
		m := modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}}, 80, h)
		assertFits(t, "view", m.View(), 80, h)
		lines := strings.Split(plain(m), "\n")
		present := map[string]int{}
		markerAt, markerN := -1, 0
		for i, l := range lines {
			if strings.HasPrefix(l, "choose") {
				break
			}
			for _, k := range order {
				if strings.HasPrefix(l, k+" ") {
					present[k] = i
				}
			}
			if g := marker.FindStringSubmatch(l); g != nil {
				markerAt = i
				markerN = int(g[1][0] - '0')
			}
		}
		missing, keepSeen := 0, false
		for _, k := range order {
			if _, ok := present[k]; !ok {
				missing++
				if keepSeen {
					t.Errorf("height %d: %q is dropped though a lower-priority row is kept:\n%s", h, k, strings.Join(lines, "\n"))
				}
			} else {
				keepSeen = true
				if markerAt >= 0 && present[k] > markerAt {
					t.Errorf("height %d: row %q is after the marker", h, k)
				}
			}
		}
		if missing > 0 {
			sawDrop = true
		}
		if missing != markerN {
			t.Errorf("height %d: %d rows dropped, marker says %d:\n%s", h, missing, markerN, strings.Join(lines, "\n"))
		}
	}
	if !sawDrop {
		t.Error("no height dropped a row; the test measures nothing")
	}
}

// TestModeTabCancelsAPlanWhenCOrRReadsAgain pins that c or r while the read
// before a plan is out says planning was cancelled and clears the stored
// snapshot, the pasted token included, at once.
func TestModeTabCancelsAPlanWhenCOrRReadsAgain(t *testing.T) {
	for _, k := range []string{"c", "r"} {
		mm := &modeMachine{states: []modelmode.State{remoteState()}}
		m := modeEditor(t, mm, 120, 50)
		m = keys(t, m, "down", "https://gpu.example:8787", "down", "down", "tok-secret", "esc")
		next, _ := m.Update(keyMsg("a"))
		m = next.(*model)
		if m.mode.planWant.PastedToken == "" {
			t.Fatalf("%s: no snapshot taken at a", k)
		}
		next, _ = m.Update(keyMsg(k))
		m = next.(*model)
		if !strings.Contains(m.mode.status, "planning cancelled") {
			t.Errorf("%s: status = %q, want planning cancelled", k, m.mode.status)
		}
		if m.mode.planWant != (modelmode.Desired{}) || m.mode.planGen != 0 {
			t.Errorf("%s: the plan snapshot was not cleared", k)
		}
	}
}

// TestModeTabCancelsAPlanInFlightWhenCOrRReadsAgain pins the later twin:
// once the read before a plan landed and the plan itself is out, c or r
// still says planning was cancelled, and the dropped plan never reaches the
// confirm screen.
func TestModeTabCancelsAPlanInFlightWhenCOrRReadsAgain(t *testing.T) {
	for _, k := range []string{"c", "r"} {
		mm := &modeMachine{states: []modelmode.State{fullState()}, plan: okPlan()}
		m := modeEditor(t, mm, 120, 50)
		next, read := m.Update(keyMsg("a"))
		m = next.(*model)
		next, plan := m.Update(read())
		m = next.(*model)
		if plan == nil {
			t.Fatalf("%s: the read before the plan did not hand the plan to a command", k)
		}
		next, _ = m.Update(keyMsg(k))
		m = next.(*model)
		if !strings.Contains(m.mode.status, "planning cancelled") {
			t.Errorf("%s: status = %q, want planning cancelled", k, m.mode.status)
		}
		m = send(t, m, plan())
		if m.mode.phase == modeConfirm {
			t.Errorf("%s: the cancelled plan reached the confirm screen", k)
		}
	}
}

// TestModeTabNamesWhatACheckIsMissing pins that a server that was not
// checked says which of the url and the token is missing.
func TestModeTabNamesWhatACheckIsMissing(t *testing.T) {
	url := remoteState()
	url.Config.Table.URL = "https://gpu.example:8787"
	tok := remoteState()
	tok.Token = modelmode.TokenState{Path: "/Users/dev/t", Exists: true}
	none := remoteState()
	for _, c := range []struct {
		name string
		st   modelmode.State
		want string
	}{
		{"url only", url, "not checked: no token"},
		{"token only", tok, "not checked: no url"},
		{"neither", none, "not checked: no url and no token"},
	} {
		if got := modeview.ServerCheckLine(c.st).Text; got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if v := plain(modeEditor(t, &modeMachine{states: []modelmode.State{c.st}}, 120, 50)); !strings.Contains(v, c.want) {
			t.Errorf("%s: the view lacks %q:\n%s", c.name, c.want, v)
		}
	}
}

// TestModeTabPromptsStayVisibleAtEverySize pins that each of the three
// default-No questions is on screen at every size, with nothing but the hint
// after it: a clipped question would leave the user answering blind.
func TestModeTabPromptsStayVisibleAtEverySize(t *testing.T) {
	cases := []struct {
		prompt string
		open   func(*testing.T, int, int) *model
	}{
		{"Apply this plan? [y/N]", func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{fullState()}, plan: longPlan()}, w, h), "a")
		}},
		{"Save anyway? [y/N]", func(t *testing.T, w, h int) *model {
			return keys(t, modeEditor(t, &modeMachine{states: []modelmode.State{failedRemoteState()}}, w, h), "a")
		}},
		{"now? [y/N]", func(t *testing.T, w, h int) *model {
			mm := &modeMachine{states: []modelmode.State{remoteState()}, plan: longPlan(), result: modelmode.Result{Done: longPlan().Steps},
				progress: []string{"step 1/9: config", "step 2/9: config", "step 3/9: config"}}
			return keys(t, modeEditorWith(t, mm, onPath, w, h), "left", "a", "y")
		}},
	}
	for _, c := range cases {
		for _, size := range tabSizes {
			m := c.open(t, size[0], size[1])
			assertFits(t, c.prompt, m.View(), size[0], size[1])
			lines := strings.Split(plain(m), "\n")
			at := -1
			for i, l := range lines {
				if strings.Contains(l, c.prompt) {
					at = i
				}
			}
			if at < 0 {
				t.Errorf("%q at %dx%d: the question is not shown:\n%s", c.prompt, size[0], size[1], plain(m))
				continue
			}
			for _, l := range lines[at+1 : len(lines)-1] {
				if strings.TrimSpace(l) != "" {
					t.Errorf("%q at %dx%d: %q follows the question", c.prompt, size[0], size[1], l)
				}
			}
		}
	}
}
