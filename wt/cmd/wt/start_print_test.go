package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// failedStartMessage is a failed start as the engine words it for mtplx: one
// line, then the server's log tail on lines of its own.
const failedStartMessage = "failed to start mtplx/m: serve process exited during model load: exit status 1; log tail: loading weights\nfatal: out of memory"

// stubFailedStart makes the start driver fail as startForLaunch does, with
// failedStartMessage.
func stubFailedStart(t *testing.T) {
	t.Helper()
	old := startModel
	startModel = func(*config.Config, catalog.Row, bool) error {
		return startFailure("mtplx/m", errors.New(strings.TrimPrefix(failedStartMessage, "failed to start mtplx/m: ")))
	}
	t.Cleanup(func() { startModel = old })
}

// failingStartServer points the config home at a fake model server whose
// one model, omlx/q, is local and stopped, and whose start jobs fail with a
// multi-line message (a server's log tail), polled without sleeping.
func failingStartServer(t *testing.T) *fakeModelServer {
	t.Helper()
	f := newFakeModelServer(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	f.Outcome = func(int, modelapi.StartRequest) *modelapi.Problem {
		return &modelapi.Problem{Code: modelapi.CodeFailed, Message: "serve process exited during model load"}
	}
	pointAtFake(t, f, "")
	old := newModelClient
	newModelClient = fastModelClient
	t.Cleanup(func() { newModelClient = old })
	captureStderr(t) // the start's progress lines
	return f
}

// TestStartCmdLeavesAFailedStartToMainsOnePrint pins #344 for `wt start`, now
// on the model server: main prints every error once, after "wt:", and cobra
// printed it too, as "Error: …", so a failed start came out twice. Through
// the real command tree a failed start job is one "wt:" line, no cobra
// "Error:" copy and no usage block, exit 2 (a started thing that did not
// finish). `--replace` is the same command and the same return.
func TestStartCmdLeavesAFailedStartToMainsOnePrint(t *testing.T) {
	for name, args := range map[string][]string{
		"wt start <id>":           {"start", "omlx/q"},
		"wt start <id> --replace": {"start", "omlx/q", "--replace"},
	} {
		t.Run(name, func(t *testing.T) {
			failingStartServer(t)
			out, code := runRoot(t, args...)
			if code != 2 || len(wtLines(out)) != 1 || !strings.Contains(out, "serve process exited during model load") {
				t.Errorf("exit %d, output %q; want 2 and one wt: line carrying the job's message", code, out)
			}
			if strings.Contains(out, "Error:") || strings.Contains(out, "Usage:") {
				t.Errorf("cobra printed its own copy or usage:\n%s", out)
			}
		})
	}
}

// TestSmokeCmdLeavesAFailedStartToMainsOnePrint is the same for `wt smoke`,
// which starts an idle model before its rows: the failure is returned, and
// cobra prints no "Error:" copy of it — and no usage block either, which with
// the "Error:" line gone would stand alone above main's line, for a failure
// that is not a usage mistake.
func TestSmokeCmdLeavesAFailedStartToMainsOnePrint(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	stubFailedStart(t)
	oldRel, oldPick, oldTTY := releaseSession, runStopPicker, stdinTTY
	t.Cleanup(func() { releaseSession, runStopPicker, stdinTTY = oldRel, oldPick, oldTTY })
	releaseSession = func() {}
	runStopPicker = func(*config.Config) {}
	stdinTTY = func() bool { return true }

	out, errOut, err := runSmokeDirectCmd(&app{hostSet: true, hostCfg: cfg}, "ollama/not-eligible:x")
	if err == nil || err.Error() != failedStartMessage {
		t.Fatalf("err = %v, want the failed start handed back for main to print", err)
	}
	if strings.Contains(out+errOut, "failed to start") {
		t.Errorf("the command printed the error itself (stdout %q, stderr %q); main prints it once", out, errOut)
	}
	if strings.Contains(out+errOut, "Usage:") {
		t.Errorf("the command printed its usage after a failed start (stdout %q, stderr %q)", out, errOut)
	}
}

// TestLaunchLeavesAFailedStartToMainsOnePrint is the same for a launch whose
// -M pin has to be started, through the real command tree on the model
// server: a failed start job and an unknown pin are each one "wt:" line,
// with no cobra "Error:" copy and no usage block (the root command silences
// usage once its flags have parsed), exit 2 for the job that did not finish
// and 1 for the pin that never started anything.
func TestLaunchLeavesAFailedStartToMainsOnePrint(t *testing.T) {
	for name, tc := range map[string]struct {
		pin  string
		code int
		has  string
	}{
		"the start job's failure":   {"omlx/q", 2, "serve process exited during model load"},
		"an error that is no start": {"omlx/nope", 1, "omlx/nope"},
	} {
		t.Run(name, func(t *testing.T) {
			failingStartServer(t)
			fakeAgentOnPath(t, "pi")
			t.Chdir(t.TempDir())
			stubStdinTTY(t, false)
			out, code := runRoot(t, "--cwd", "-A", "pi", "-M", tc.pin)
			if code != tc.code || len(wtLines(out)) != 1 || !strings.Contains(out, tc.has) {
				t.Errorf("exit %d, output %q; want %d and one wt: line naming %q", code, out, tc.code, tc.has)
			}
			if strings.Contains(out, "Error:") || strings.Contains(out, "Usage:") {
				t.Errorf("cobra printed its own copy or usage:\n%s", out)
			}
		})
	}
}

// TestPrintOnceSilencesCobraForAStartErrorOnly runs the mark through cobra
// itself, on a command set up as the root command is (no SilenceUsage): for
// an error printOnce recognises Execute prints nothing — not the error, and
// not the usage block, which would otherwise stand alone between the start's
// progress and main's line — and any other error is still printed as
// "Error: …" with the usage after it. It is what ties the fields the launch
// test reads to what the user sees.
func TestPrintOnceSilencesCobraForAStartErrorOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		silent bool
	}{
		"a start error":   {asStartError(errors.New("boom")), true},
		"any other error": {errors.New("boom"), false},
	} {
		t.Run(name, func(t *testing.T) {
			c := &cobra.Command{Use: "x"}
			c.RunE = func(cmd *cobra.Command, _ []string) error { return printOnce(cmd, tc.err) }
			var out, errOut bytes.Buffer
			c.SetOut(&out)
			c.SetErr(&errOut)
			c.SetArgs(nil)
			if err := c.Execute(); err == nil || err.Error() != "boom" {
				t.Fatalf("err = %v, want boom", err)
			}
			printed := errOut.String() + out.String()
			if tc.silent && printed != "" {
				t.Errorf("cobra printed %q, want nothing", printed)
			}
			if !tc.silent && (!strings.HasPrefix(printed, "Error: boom\n") || !strings.Contains(printed, "Usage:")) {
				t.Errorf("cobra printed %q, want the error and the usage, as before", printed)
			}
		})
	}
}
