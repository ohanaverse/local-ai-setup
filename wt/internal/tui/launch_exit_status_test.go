package tui

import (
	"errors"
	"fmt"
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

// TestGateFailureIsNeverReadAsTheAgentsExit checks the one discrimination the
// exit routing depends on (#412's edge): a gate/creation failure can wrap
// git's own *exec.ExitError (gitError's empty-stderr branch and startPhrase's
// rev-parse failure both %w one), and cmd/wt's agentExitStatus reads any
// error carrying an *exec.ExitError as the agent's own quiet exit — printing
// nothing and exiting with git's status instead of the gate failure's one
// line. The gate's error must travel out of the run command with no
// *exec.ExitError in its chain, which is exactly what exitStatusOf's -2
// reports here and agentExitStatus's errors.As sees there.
func TestGateFailureIsNeverReadAsTheAgentsExit(t *testing.T) {
	keepPendingPostExit(t)
	profileApplier = nil
	// gitError's empty-stderr branch wraps git's exit status exactly so.
	synthetic := fmt.Errorf("git worktree add: %w", exec.Command("false").Run())
	msg := runAndWaitCmd(exec.Command("true"), "claude", config.Model{}, func() (bool, error) { return false, synthetic })()
	done, ok := msg.(launchDoneMsg)
	if !ok {
		t.Fatalf("msg = %#v, want launchDoneMsg", msg)
	}
	if done.err == nil {
		t.Fatal("the gate's failure did not travel out as the run's error")
	}
	if got := exitStatusOf(done.err); got != -2 {
		t.Errorf("exitStatusOf = %d, want -2: agentExitStatus would exit quietly with an agent status instead of printing the gate's failure once", got)
	}
	if done.err.Error() != synthetic.Error() {
		t.Errorf("err = %q, want the failure's own message %q", done.err.Error(), synthetic.Error())
	}
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
			msg := runAndWaitCmd(exec.Command("sh", "-c", c.script), "shell", config.Model{}, nil)()
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

// TestPickerProfileErrorIsReturned pins #412's last case: a profile that
// fails to apply before the agent starts makes Run return that error, so wt
// exits 1 as a launch without the picker does (runAgentCmd returns it).
func TestPickerProfileErrorIsReturned(t *testing.T) {
	keepPendingPostExit(t)
	profileApplier = func(*exec.Cmd, string, config.Model) (func() error, error) {
		return nil, errors.New("wrapper missing")
	}
	msg := runAndWaitCmd(exec.Command("sh", "-c", "exit 3"), "shell", config.Model{}, nil)()
	m, _ := updateMsg(model{cfg: testConfig()}, msg)
	err := runResult(m, nil)
	if err == nil || err.Error() != "wrapper missing" {
		t.Fatalf("result = %v, want the profile error", err)
	}
}

// TestSlotProfileLaunchReturnsTheAgentsExitStatus pins #412 for a
// slot-profile launch from the picker: its run command shares the
// launchDoneMsg, so an agent that exits 3 makes Run's result carry status 3.
func TestSlotProfileLaunchReturnsTheAgentsExitStatus(t *testing.T) {
	m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) { return nil, nil })
	row := catalog.Row{Model: config.Model{ID: "profile/cloud"}, Profile: &catalog.ProfileRow{}}
	msg := runSlotAndWaitCmd(exec.Command("sh", "-c", "exit 3"), "claude", row, nil)()
	m, _ = updateMsg(m, msg)
	if got := exitStatusOf(runResult(m, nil)); got != 3 {
		t.Fatalf("result status = %d, want the agent's exit status 3", got)
	}
}
