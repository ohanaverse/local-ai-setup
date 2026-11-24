package modelsetup

import (
	"errors"
	"strings"
	"testing"
)

// TestStepLabelNamesWhatAWaitWaitsFor pins #710: the rotate and create plans
// wait for LiteLLM, and their progress line and error must not say they wait
// for wt-model, which the plan's later wait does.
func TestStepLabelNamesWhatAWaitWaitsFor(t *testing.T) {
	litellm := Step{Kind: StepWait, Action: ActionWaitLiteLLM}
	agent := Step{Kind: StepWait}
	if got := stepLabel(litellm); got != "wait for LiteLLM" {
		t.Errorf("a LiteLLM wait is labelled %q", got)
	}
	if got := stepLabel(agent); got != "wait for wt-model" {
		t.Errorf("a wt-model wait is labelled %q", got)
	}
	if got := stepLabel(Step{Kind: StepKickstart}); got != "restart the LaunchAgent" {
		t.Errorf("another step's label changed: %q", got)
	}
	r := failed(Result{}, litellm, errors.New("timed out"))
	if r.Err == nil || !strings.HasPrefix(r.Err.Error(), "wait for LiteLLM: ") {
		t.Errorf("the failure of a LiteLLM wait reads %v", r.Err)
	}
}
