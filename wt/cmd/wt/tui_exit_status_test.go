package main

import (
	"bytes"
	"os/exec"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
)

// agentRunError runs script under sh and returns the error cmd.Run gives,
// the same error tui.Run hands back for an agent that ended that way.
func agentRunError(t *testing.T, script string) error {
	t.Helper()
	return exec.Command("sh", "-c", script).Run()
}

// runPickerLaunch launches through the model picker with tuiRun stubbed to
// return tuiErr, as tui.Run does once the agent it launched has exited and
// the post-exit flow has run.
func runPickerLaunch(t *testing.T, tuiErr error) (string, error) {
	t.Helper()
	dir := initTestRepo(t)
	t.Chdir(dir)
	writeConfiguredAgent(t, t.TempDir(), "pi", "ollama")
	old := tuiRun
	tuiRun = func(bool, bool, string, string, string, string, []string, themes.Theme, string, *config.Config, tui.ProfileApplier, tui.SlotProfiles) error {
		return tuiErr
	}
	t.Cleanup(func() { tuiRun = old })
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	return runWT(t, "-W", "my-feature", "-A", "pi")
}

// TestPickerLaunchExitsWithTheAgentsStatus pins #412 at the process exit: a
// session launched from the picker whose agent exited 3 makes wt exit 3,
// silently (no cobra Error: line or usage, no main "wt:" line), as
// runAgentCmd's os.Exit does for a launch without the picker;
// a signal-killed agent exits 255, which is what os.Exit(-1) gives there.
// Without it a script or a shell `&&` reads a failed agent session as a
// success.
func TestPickerLaunchExitsWithTheAgentsStatus(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   int
	}{
		{"exit 3", "exit 3", 3},
		{"killed by a signal", "kill -9 $$", 255},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runPickerLaunch(t, agentRunError(t, c.script))
			if err == nil {
				t.Fatal("launch returned nil, want the agent's exit status")
			}
			if out != "" {
				t.Errorf("cobra printed %q, want nothing: no Error: line or usage block after an agent's own failure", out)
			}
			var stderr bytes.Buffer
			if got := reportExit(err, &stderr); got != c.want {
				t.Errorf("exit status = %d, want %d", got, c.want)
			}
			if exitCodeOf(err) != c.want {
				t.Errorf("exitCodeOf = %d, want %d", exitCodeOf(err), c.want)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing: the agent said why it failed, and runAgentCmd adds no line either", stderr.String())
			}
		})
	}
}

// TestPickerLaunchThatSucceededStaysZero pins the other half of #412: an
// agent that exited 0 leaves tui.Run's nil untouched, so wt exits 0.
func TestPickerLaunchThatSucceededStaysZero(t *testing.T) {
	if _, err := runPickerLaunch(t, nil); err != nil {
		t.Fatalf("launch = %v, want nil", err)
	}
}

// TestReportExitPrintsOtherErrors pins that the quiet agent status is the
// only error main does not print: every other error still gets its "wt:"
// line and its exitCodeOf status, so no command lost its message.
func TestReportExitPrintsOtherErrors(t *testing.T) {
	var stderr bytes.Buffer
	if got := reportExit(&exitCodeError{code: 4, err: errNoTTYForTest}, &stderr); got != 4 {
		t.Errorf("exit status = %d, want 4", got)
	}
	if stderr.String() != "wt: no terminal\n" {
		t.Errorf("stderr = %q, want the error's wt: line", stderr.String())
	}
}

var errNoTTYForTest = errString("no terminal")

type errString string

func (e errString) Error() string { return string(e) }
