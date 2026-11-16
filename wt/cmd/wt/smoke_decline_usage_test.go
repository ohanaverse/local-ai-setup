package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
)

// TestSmokeCwdDeclineShowsNoUsage: a "no" at `wt smoke --cwd`'s launch
// question is a declined prompt (exit 3, one line), not a usage mistake, so
// cobra's usage block must not follow it.
func TestSmokeCwdDeclineShowsNoUsage(t *testing.T) {
	r := smokeRig(t, func(string, string, string, *func() error) smoke.ExecOutcome { return smoke.StubOutcome("ok", 0) })
	stubStdinTTY(t, true)
	t.Chdir(sharedRootRepo(t))
	captureNotes(t)

	cmd, _, _ := smokeRootCmd(t, r, strings.NewReader("n\n"), "--cwd", "ollama/does-not-exist")
	cmd.SilenceErrors = true
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.Execute()
	if err == nil || exitcode.Of(err) != 3 {
		t.Fatalf("err = %v (exit %d), want a decline with exit 3", err, exitcode.Of(err))
	}
	if strings.Contains(buf.String(), "Usage:") {
		t.Errorf("usage printed after a decline:\n%s", buf.String())
	}
}
