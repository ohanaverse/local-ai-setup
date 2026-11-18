package tui

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// nativeModelFor is a picker model with no model client at the point a
// model-driven agent is chosen (wt-session resolved native mode, so it opened
// the picker without a client).
func nativeModelFor() model {
	m := newRunModel(false, false, "", "claude", "", "", "", []string{"--x"}, themes.Default, ".", nil, &config.Config{}, nil)
	m.selectedPath = "/work/tree"
	m.status = ""
	return m
}

// TestPickerLaunchesAChosenAgentNativelyWithNoServer pins #615 in the picker:
// with no client and the native flag, choosing a model-driven agent skips the
// model phase and commits a launch of the native model (a zero Gateway, the
// worktree as its directory, the passthrough args), leaves the one native note
// to the launch gate (said once it passes), and leaves no status. Without it the picker stopped on
// "no model server is configured" for every agent. Since stage 3 wt-session
// passes no client only in native mode (an unset mode refuses before the
// picker opens), so a nil client still means "launch natively".
func TestPickerLaunchesAChosenAgentNativelyWithNoServer(t *testing.T) {
	m := nativeModelFor()
	var got struct {
		agent string
		m     config.Model
		dir   string
		gw    agents.Gateway
		args  []string
	}
	m.hooks.gateway = func(agent string, mdl config.Model, path string, _ bool, gw agents.Gateway, _ []config.Model, extra []string) (*exec.Cmd, agents.LaunchInfo, error) {
		got.agent, got.m, got.dir, got.gw, got.args = agent, mdl, path, gw, extra
		cmd := exec.Command("true")
		cmd.Dir = path
		return cmd, agents.LaunchInfo{}, nil
	}
	pendingRouteNotes = ""
	t.Cleanup(func() { pendingRouteNotes = "" })

	m.agent = "claude"
	next, cmd := m.enterModelPhaseForAgent()
	if cmd == nil || !next.committed || next.status != "" {
		t.Fatalf("cmd nil %v, committed %v, status %q; want a committed launch and no status", cmd == nil, next.committed, next.status)
	}
	if got.agent != "claude" || !got.m.Native || got.m.ID != "" || got.gw != (agents.Gateway{}) || got.dir != "/work/tree" || len(got.args) != 1 {
		t.Errorf("built %+v, want the native claude launch in the worktree with the passthrough args", got)
	}
	if pendingRouteNotes != "" {
		t.Errorf("pending notes %q, want none: the native note is said after the launch gate", pendingRouteNotes)
	}
}

// TestNativeNoteIsSaidOnlyAfterTheGateLetsTheLaunchGo pins that a declined
// launch gate never printed "launching X natively": the note came out before
// the gate when it rode pendingRouteNotes, so a user who answered no had
// already been told a launch was under way.
func TestNativeNoteIsSaidOnlyAfterTheGateLetsTheLaunchGo(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
		want string
	}{{"declined", false, ""}, {"accepted", true, "launching claude natively\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			ok, err := sayAfterGate(func() (bool, error) { return tc.ok, nil }, &b, "launching claude natively")()
			if ok != tc.ok || err != nil || b.String() != tc.want {
				t.Errorf("ok %v err %v wrote %q; want ok %v and %q", ok, err, b.String(), tc.ok, tc.want)
			}
		})
	}
}

// TestPickerEnterOnAnAgentLaunchesNatively drives the real entry point: Enter
// on a model-driven agent in the agent picker, through Update, with no client
// and the native flag, commits a native launch and leaves no pending note (the
// note is the gate wrapper's, said after the gate).
func TestPickerEnterOnAnAgentLaunchesNatively(t *testing.T) {
	m := nativeModelFor()
	built := 0
	m.hooks.gateway = func(agent string, mdl config.Model, path string, _ bool, gw agents.Gateway, _ []config.Model, _ []string) (*exec.Cmd, agents.LaunchInfo, error) {
		built++
		if !mdl.Native || gw != (agents.Gateway{}) {
			t.Errorf("built %+v with gateway %+v, want the native model and a zero Gateway", mdl, gw)
		}
		return exec.Command("true"), agents.LaunchInfo{}, nil
	}
	pendingRouteNotes = ""
	t.Cleanup(func() { pendingRouteNotes = "" })

	m.agent = ""
	m.phase = phaseAgent
	m.agentList = list.New([]list.Item{agentItem{name: "claude"}}, themes.ListDelegate(m.theme), 80, 20)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(model)
	if cmd == nil || !nm.committed || built != 1 || pendingRouteNotes != "" {
		t.Errorf("cmd nil %v, committed %v, built %d, pending %q; want a committed native launch, one build, no pending note", cmd == nil, nm.committed, built, pendingRouteNotes)
	}
}

// TestPickerPinnedAgentLaunchesNatively is the same through the pinned-agent
// entry (proceedWithPath with an initial agent).
func TestPickerPinnedAgentLaunchesNatively(t *testing.T) {
	m := nativeModelFor()
	built := 0
	m.hooks.gateway = func(string, config.Model, string, bool, agents.Gateway, []config.Model, []string) (*exec.Cmd, agents.LaunchInfo, error) {
		built++
		return exec.Command("true"), agents.LaunchInfo{}, nil
	}
	defer func(p func(string) bool) { installed = p }(installed)
	installed = func(string) bool { return true }
	m.initialAgent = "claude"
	next, cmd := m.proceedWithPath()
	if cmd == nil || !next.committed || built != 1 {
		t.Errorf("cmd nil %v, committed %v, built %d; want a committed native launch", cmd == nil, next.committed, built)
	}
}
