package tui

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// nativeModelFor is a picker model with no model client at the point a
// model-driven agent is chosen: native says wt-session found no model server
// configured at all.
func nativeModelFor(native bool) model {
	m := newRunModel(false, false, "", "claude", "", "", "", []string{"--x"}, themes.Default, ".", nil, &config.Config{}, nil)
	m.native = native
	m.selectedPath = "/work/tree"
	m.status = ""
	return m
}

// TestPickerLaunchesAChosenAgentNativelyWithNoServer pins #615 in the picker:
// with no client and the native flag, choosing a model-driven agent skips the
// model phase and commits a launch of the native model (a zero Gateway, the
// worktree as its directory, the passthrough args), queues the one native note
// for the terminal, and leaves no status. Without it the picker stopped on
// "no model server is configured" for every agent.
func TestPickerLaunchesAChosenAgentNativelyWithNoServer(t *testing.T) {
	var got struct {
		agent string
		m     config.Model
		dir   string
		gw    agents.Gateway
		args  []string
	}
	old := buildGatewayLaunch
	buildGatewayLaunch = func(agent string, mdl config.Model, path string, _ bool, gw agents.Gateway, _ []config.Model, extra []string) (*exec.Cmd, agents.LaunchInfo, error) {
		got.agent, got.m, got.dir, got.gw, got.args = agent, mdl, path, gw, extra
		cmd := exec.Command("true")
		cmd.Dir = path
		return cmd, agents.LaunchInfo{}, nil
	}
	t.Cleanup(func() { buildGatewayLaunch = old })
	pendingRouteNotes = ""
	t.Cleanup(func() { pendingRouteNotes = "" })

	m := nativeModelFor(true)
	m.agent = "claude"
	next, cmd := m.enterModelPhaseForAgent()
	if cmd == nil || !next.committed || next.status != "" {
		t.Fatalf("cmd nil %v, committed %v, status %q; want a committed launch and no status", cmd == nil, next.committed, next.status)
	}
	if got.agent != "claude" || !got.m.Native || got.m.ID != "" || got.gw != (agents.Gateway{}) || got.dir != "/work/tree" || len(got.args) != 1 {
		t.Errorf("built %+v, want the native claude launch in the worktree with the passthrough args", got)
	}
	if n := strings.Count(pendingRouteNotes, "launching claude natively"); n != 1 {
		t.Errorf("pending notes %q, want the native note once", pendingRouteNotes)
	}
}

// TestPickerRefusesAModelAgentWhenNotNative pins the other side: with no
// client and no native flag (a half-configured server or a -M/-T/-F launch
// never gets the flag) choosing an agent keeps the old status and launches
// nothing, so the fallback cannot leak into a refusal.
func TestPickerRefusesAModelAgentWhenNotNative(t *testing.T) {
	old := buildGatewayLaunch
	buildGatewayLaunch = func(string, config.Model, string, bool, agents.Gateway, []config.Model, []string) (*exec.Cmd, agents.LaunchInfo, error) {
		t.Error("an agent was built with no server and no native flag")
		return nil, agents.LaunchInfo{}, nil
	}
	t.Cleanup(func() { buildGatewayLaunch = old })
	m := nativeModelFor(false)
	m.agent = "claude"
	next, cmd := m.enterModelPhaseForAgent()
	if cmd != nil || next.committed || !strings.Contains(next.status, "no model server is configured") {
		t.Errorf("cmd nil %v, committed %v, status %q; want the no-server status and no launch", cmd == nil, next.committed, next.status)
	}
}
