package main

import (
	"bytes"
	"strings"
	"syscall"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

// closedPipe is a stdout whose reader went away (`wt remove ... | true`):
// every write fails with EPIPE, as it does once SIGPIPE is ignored.
type closedPipe struct{}

func (closedPipe) Write([]byte) (int, error) { return 0, syscall.EPIPE }

// deletedOutcome is a branch item whose branch this run deleted.
func deletedOutcome() removal.Outcome {
	o := removal.Outcome{Item: removal.Item{Kind: cleanup.KindBranch, Name: "topic", Ref: "refs/heads/topic", Saw: cleanup.StatusProven},
		Worktree: removal.WtNotRequested, Branch: removal.BrDeleted, Reason: "deleted refs/heads/topic"}
	o.DeletedTip = strings.Repeat("a", 40)
	return o
}

// TestRemoveResultSurvivesAClosedStdout: after a real removal the result is
// the only record of each deleted branch's commit. With stdout a closed pipe
// the result was lost (SIGPIPE killed wt; with it ignored, the write error
// was returned bare): the human text goes to stderr instead, restore ids
// included, and the exit is 2. JSON and text mode alike.
func TestRemoveResultSurvivesAClosedStdout(t *testing.T) {
	for _, asJSON := range []bool{true, false} {
		var errOut bytes.Buffer
		err := writeRemoveResult(closedPipe{}, &errOut, asJSON, removal.Meta{Repo: "/r"}, []removal.Outcome{deletedOutcome()})
		if exitOf(err) != 2 {
			t.Errorf("json=%v: exit %d, want 2", asJSON, exitOf(err))
		}
		if !strings.Contains(errOut.String(), strings.Repeat("a", 40)) {
			t.Errorf("json=%v: stderr %q lacks the deleted branch's commit", asJSON, errOut.String())
		}
	}
}

// TestRemoveDryRunSaysWouldNotBeRemoved: a dry run changes nothing, so its
// failure line must not say items "were not removed" (read as a run that
// tried and failed); it says they would not be.
func TestRemoveDryRunSaysWouldNotBeRemoved(t *testing.T) {
	refused := removal.Outcome{Item: removal.Item{Kind: cleanup.KindBranch, Name: "topic", Saw: cleanup.StatusProven},
		Worktree: removal.WtRefused, Branch: removal.BrNotAttempted, Reason: "in use"}
	for _, asJSON := range []bool{true, false} {
		var out bytes.Buffer
		err := writeRemoveResult(&out, &out, asJSON, removal.Meta{Repo: "/r", DryRun: true}, []removal.Outcome{refused})
		if exitOf(err) != 2 || err == nil || !strings.Contains(err.Error(), "would not be removed") {
			t.Errorf("json=%v: exit %d err %v, want 2 saying some items would not be removed", asJSON, exitOf(err), err)
		}
	}
}
