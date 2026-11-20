package tui

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// forceTTY makes the popup path's terminal probe say yes for one test.
func forceTTY(t *testing.T) {
	t.Helper()
	old := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = old })
}

// TestCommitAskGatePopupAndDecline: with warnings on a (stubbed) terminal the
// commit enters phaseAsk; the decline returns to the picker phase, releases
// committed, clears the gate fields, runs the supplied hold drop and never
// builds a run command. It matters because a go-back must leave the picker
// able to commit again and must not strand the launch's client-session hold.
func TestCommitAskGatePopupAndDecline(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	m := model{agent: "claude", selectedPath: root, phase: phaseModel}
	var built, dropped bool
	bind := func(mm model) (model, tea.Cmd) { built = true; return mm, nil }
	drop := func() tea.Msg { dropped = true; return nil }
	next, cmd := m.commitAsk(config.Model{}, bind, drop)
	if next.phase != phaseAsk || next.ask == nil || cmd != nil || !next.committed {
		t.Fatalf("commitAsk = phase %v committed %v; want a gate popup holding the guard", next.phase, next.committed)
	}
	if !strings.Contains(strings.Join(next.ask.body, "\n"), "repository root") {
		t.Errorf("popup body = %v; want the root warning", next.ask.body)
	}
	after, cmd := updateMsg(next, askKey("esc"))
	if built {
		t.Error("decline built a run command")
	}
	if after.phase != phaseModel || after.committed || after.gateAnswer != nil || after.gateTarget != nil || after.ask != nil {
		t.Errorf("decline = phase %v committed %v; want back on the picker, uncommitted, fields cleared", after.phase, after.committed)
	}
	if cmd == nil {
		t.Fatal("decline returned no drop command")
	}
	cmd()
	if !dropped {
		t.Error("decline did not run the hold drop")
	}
	if claimExists(root) {
		t.Error("a declined gate claimed the root")
	}
}

// TestCommitAskGatePopupYesBindsWithGateTarget: yes binds with the gate
// pre-answered against the popup's own Target, and queues the launching line
// for after the release. It matters because the released-phase gate must not
// ask the question again or re-read the Target.
func TestCommitAskGatePopupYesBindsWithGateTarget(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	m := model{agent: "claude", selectedPath: root, phase: phaseModel}
	var got model
	bind := func(mm model) (model, tea.Cmd) {
		got = mm
		return mm.commitLaunch(func() tea.Msg { return nil })
	}
	next, _ := m.commitAsk(config.Model{}, bind)
	after, cmd := updateMsg(next, askKey("enter"))
	if got.gateAnswer == nil || !*got.gateAnswer || got.gateTarget == nil {
		t.Fatalf("bound model gate fields = (%v, %v); want answered with the popup's Target", got.gateAnswer, got.gateTarget)
	}
	if cmd == nil || !after.committed {
		t.Errorf("yes left cmd %v committed %v; want the run command bound and the guard taken", cmd, after.committed)
	}
	if !strings.Contains(pendingRouteNotes, "wt: launching in ") {
		t.Errorf("route notes = %q; want the launching line queued", pendingRouteNotes)
	}
	if strings.Contains(pendingRouteNotes, "repository root") {
		t.Errorf("route notes = %q; the warnings live in the popup alone", pendingRouteNotes)
	}
}

// TestCommitAskNoPopupPassesThrough: a preset path, a non-terminal stdin and a
// launch with no warnings all bind straight away, with the gate fields unset
// so the released-phase gate behaves exactly as before. It matters because
// the non-popup paths are byte-identical to today's.
func TestCommitAskNoPopupPassesThrough(t *testing.T) {
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	for name, tc := range map[string]struct {
		tty bool
		m   model
	}{
		"prePath":  {true, model{agent: "claude", prePath: root, selectedPath: root}},
		"no tty":   {false, model{agent: "claude", selectedPath: root}},
		"no warns": {true, model{agent: "claude", selectedPath: linked}},
	} {
		old := stdinTTY
		stdinTTY = func() bool { return tc.tty }
		bound := false
		next, _ := tc.m.commitAsk(config.Model{}, func(mm model) (model, tea.Cmd) {
			bound = true
			if mm.gateAnswer != nil || mm.gateTarget != nil {
				t.Errorf("%s: gate fields set on a no-popup commit", name)
			}
			return mm.commitLaunch(func() tea.Msg { return nil })
		})
		stdinTTY = old
		if !bound || next.phase == phaseAsk || !next.committed {
			t.Errorf("%s: bound %v phase %v committed %v; want a straight bind", name, bound, next.phase, next.committed)
		}
	}
}

// TestCommitAskQuitDeclines: q at the popup runs the decline's release and
// then quits, so a quit never strands the hold. It matters because the
// prep's client session would otherwise never end.
func TestCommitAskQuitDeclines(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	dropped := false
	m := model{agent: "claude", selectedPath: root, phase: phaseModel}
	next, _ := m.commitAsk(config.Model{}, func(mm model) (model, tea.Cmd) { return mm, nil }, func() tea.Msg { dropped = true; return nil })
	_, cmd := updateMsg(next, askKey("ctrl+c"))
	if cmd == nil {
		t.Fatal("quit returned no command")
	}
	// tea.Sequence yields an unexported slice of commands the runtime runs in
	// order; run them the way it would.
	seq := reflect.ValueOf(cmd())
	if seq.Kind() != reflect.Slice || seq.Len() != 2 {
		t.Fatalf("quit produced %v; want a sequence of the decline's drop then quit", seq)
	}
	for i := 0; i < seq.Len(); i++ {
		if c, ok := seq.Index(i).Interface().(tea.Cmd); ok && c != nil {
			c()
		}
	}
	if !dropped {
		t.Error("quitting from the popup did not run the hold drop")
	}
}

// TestLaunchCommandPopupDeclineBuildsNothing: through the real command site, a
// declined gate returns to the picker with no run bound, and a retry can
// commit again. It matters because commitLaunch's guard must release on a
// popup go-back.
func TestLaunchCommandPopupDeclineBuildsNothing(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	m := model{agent: "shell", selectedPath: root, phase: phaseAgent}
	m.hooks.gateway = func(string, config.Model, string, bool, agents.Gateway, []config.Model, []string) (*exec.Cmd, agents.LaunchInfo, error) {
		return exec.Command("true"), agents.LaunchInfo{}, nil
	}
	next, cmd := m.launchCommand("shell")
	if next.phase != phaseAsk || cmd != nil {
		t.Fatalf("launchCommand = phase %v cmd %v; want the popup", next.phase, cmd)
	}
	after, _ := updateMsg(next, askKey("n"))
	if after.phase != phaseAgent || after.committed {
		t.Fatalf("decline = phase %v committed %v; want back at the agent picker", after.phase, after.committed)
	}
	again, _ := after.launchCommand("shell")
	if again.phase != phaseAsk {
		t.Errorf("retry after a go-back did not commit again (phase %v)", again.phase)
	}
}
