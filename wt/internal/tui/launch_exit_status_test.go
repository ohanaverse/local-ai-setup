package tui

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// keepPendingPostExit saves and restores the post-exit state a launch
// command writes, so a test's launch leaves nothing for the next one.
func keepPendingPostExit(t *testing.T) {
	t.Helper()
	summary, survey, applier := pendingSummary, pendingSurveyState, profileApplier
	t.Cleanup(func() { pendingSummary, pendingSurveyState, profileApplier = summary, survey, applier })
}

// exitStatusOf is the status an *exec.ExitError in err carries, or -2 when
// err holds none.
func exitStatusOf(err error) int {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return -2
	}
	return ee.ExitCode()
}

// TestPickerLaunchReturnsTheAgentsExitStatus pins #412 for a single-model
// launch: an agent that exits 3 makes Run's result carry status 3 (cmd/wt
// exits with it), and the status line still says how the agent exited; an
// agent that exits 0 leaves the result nil. Before #412 the launchDoneMsg
// handler dropped the error and wt exited 0 after a failed session.
func TestPickerLaunchReturnsTheAgentsExitStatus(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   int
	}{
		{"exit 3", "exit 3", 3},
		{"exit 0", "exit 0", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keepPendingPostExit(t)
			profileApplier = nil
			msg := runAndWaitCmd(exec.Command("sh", "-c", c.script), "shell", config.Model{})()
			m, _ := updateMsg(model{cfg: testConfig()}, msg)
			err := runResult(m, nil)
			if c.want == 0 {
				if err != nil || m.status != "" {
					t.Fatalf("result = %v, status = %q, want nil and no status", err, m.status)
				}
				return
			}
			if got := exitStatusOf(err); got != c.want {
				t.Fatalf("result = %v (status %d), want the agent's exit status %d", err, got, c.want)
			}
			if m.status != "agent exited: exit status 3" {
				t.Errorf("status = %q, want the agent's exit shown as before", m.status)
			}
		})
	}
}

// TestPickerProfileErrorStillReturnsNil pins what #412 left alone: a
// profile that fails to apply before the agent starts is printed where it
// happens and Run returns nil, as before; only an agent that ran passes its
// status on.
func TestPickerProfileErrorStillReturnsNil(t *testing.T) {
	keepPendingPostExit(t)
	profileApplier = func(*exec.Cmd, string, config.Model) (func() error, error) {
		return nil, errors.New("wrapper missing")
	}
	msg := runAndWaitCmd(exec.Command("sh", "-c", "exit 3"), "shell", config.Model{})()
	m, _ := updateMsg(model{cfg: testConfig()}, msg)
	if err := runResult(m, nil); err != nil {
		t.Fatalf("result = %v, want nil for a launch whose agent never ran", err)
	}
}

// TestSlotProfileLaunchReturnsTheAgentsExitStatus pins #412 for a
// slot-profile launch from the picker: its run command shares the
// launchDoneMsg, so an agent that exits 3 makes Run's result carry status 3.
func TestSlotProfileLaunchReturnsTheAgentsExitStatus(t *testing.T) {
	m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) { return nil, nil })
	row := catalog.Row{Model: config.Model{ID: "profile/cloud"}, Profile: &catalog.ProfileRow{}}
	msg := runSlotAndWaitCmd(exec.Command("sh", "-c", "exit 3"), "claude", row)()
	m, _ = updateMsg(m, msg)
	if got := exitStatusOf(runResult(m, nil)); got != 3 {
		t.Fatalf("result status = %d, want the agent's exit status 3", got)
	}
}
