package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/launchwarn"
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

func TestLaunchQuestionDeclinedExits3(t *testing.T) {
	// A "no" at the launch warning is a decline like any other.
	tg := launchwarn.Target{Dir: "/r", IsRoot: true}
	err := confirmLaunch(tg, nil, strings.NewReader("n\n"), &bytes.Buffer{}, true)
	wantApproval(t, err, "approval missing: declined; nothing was launched")
}
