package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/launchwarn"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// reportLine is what main prints for err, and the status it exits with.
func reportLine(err error) string {
	var buf bytes.Buffer
	reportExit(err, &buf)
	return buf.String()
}

// wantApproval checks the W4 contract for one refusal or decline: exit 3 and
// exactly one line on stderr, containing want.
func wantApproval(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want exit 3 containing %q", want)
	}
	if got := exitcode.Of(err); got != 3 {
		t.Errorf("exit = %d (%v), want 3", got, err)
	}
	line := reportLine(err)
	if strings.Count(line, "\n") != 1 || !strings.HasPrefix(line, "wt: ") || !strings.Contains(line, want) {
		t.Errorf("stderr = %q, want one \"wt: ...\" line containing %q", line, want)
	}
}

func TestApprovalMissingWording(t *testing.T) {
	err := approvalMissing("--yes", "nothing was stopped")
	if got, want := reportLine(err), "wt: approval missing: pass --yes (there is no terminal to ask); nothing was stopped\n"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	if got := exitcode.Of(err); got != 3 {
		t.Errorf("exit = %d, want 3", got)
	}
}

func TestStartWithoutATerminalRefusesWithExit3(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, false)
	occ := &lifecycle.OccupiedError{Occupants: []localmodels.Entry{localmodelsEntry("omlx/other", "other")}}
	calls := stubLifecycleStart(t, []error{occ})

	err := startForLaunch(&config.Config{}, startTestRow(), false)
	wantApproval(t, err, "approval missing: pass --replace (there is no terminal to ask); nothing was started")
	if len(*calls) != 1 {
		t.Errorf("engine calls = %v, want 1 (nothing changed)", *calls)
	}
}

func TestStartDeclinedExits3(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, true)
	old := confirmReplace
	confirmReplace = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { confirmReplace = old })
	occ := &lifecycle.OccupiedError{Occupants: []localmodels.Entry{localmodelsEntry("omlx/other", "other")}}
	stubLifecycleStart(t, []error{occ})
	wantApproval(t, startForLaunch(&config.Config{}, startTestRow(), false), "approval missing: declined; omlx/other still running")
}

func TestStartUnknownOccupancyDeclinedExits3(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, true)
	old := confirmReplace
	confirmReplace = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { confirmReplace = old })
	unk := &lifecycle.OccupancyUnknownError{ProviderID: "mtplx", Origin: "http://localhost:8003"}
	stubLifecycleStart(t, []error{unk})
	wantApproval(t, startForLaunch(&config.Config{}, startTestRow(), false), "approval missing: declined; mtplx may still be serving a model")
}

func TestStartOthersDeclinedExits3(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, true)
	stubOthersMode(t, "")
	stubSessionCounts(t, nil)
	stubChooseOthers(t, othersCancel, nil)
	calls := stubOthersEngine(t, othersFixture())
	wantApproval(t, startForLaunch(&config.Config{}, startTestRow(), false), "still loaded")
	if len(*calls) != 1 {
		t.Errorf("engine calls = %d, want 1 (nothing started)", len(*calls))
	}
}

func TestStartOthersWithoutATerminalKeepsAndGoesOn(t *testing.T) {
	// The one P4 exception: the safe answer is to do nothing, so no refusal.
	stubSignals(t)
	stubStdinTTY(t, false)
	stubOthersMode(t, "")
	stubSessionCounts(t, nil)
	stubOthersEngine(t, othersFixture())
	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("err = %v, want nil (kept, warned once)", err)
	}
}

func TestLaunchQuestionDeclinedExits3(t *testing.T) {
	// A "no" at the launch warning is a decline like any other.
	tg := launchwarn.Target{Dir: "/r", IsRoot: true}
	err := confirmLaunch(tg, nil, strings.NewReader("n\n"), &bytes.Buffer{}, true)
	wantApproval(t, err, "approval missing: declined; nothing was launched")
}
