package main

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// failingStartServer returns the hooks for the app it builds, and points the
// config home at a fake model server whose
// one model, omlx/q, is local and stopped, and whose start jobs fail with a
// multi-line message (a server's log tail), polled without sleeping.
func failingStartServer(t *testing.T) hooks {
	t.Helper()
	f := newFakeModelServer(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	f.Outcome = func(int, modelapi.StartRequest) *modelapi.Problem {
		return &modelapi.Problem{Code: modelapi.CodeFailed, Message: "serve process exited during model load"}
	}
	pointAtFake(t, f, "")
	captureStderr(t) // the start's progress lines
	h := testHooks()
	h.newClient = fastModelClient
	return h
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
			h := failingStartServer(t)
			out, code := runRootWith(t, h, args...)
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
// which starts an idle model before its rows, on the model server: through
// the real command tree a failed start job is one "wt:" line, with no cobra
// "Error:" copy of it and no usage block, which with the "Error:" line gone
// would stand alone above main's line for a failure that is not a usage
// mistake.
func TestSmokeCmdLeavesAFailedStartToMainsOnePrint(t *testing.T) {
	h := failingStartServer(t)
	out, code := runRootWith(t, h, "smoke", "omlx/q", "--only", "claude")
	if code == 0 || len(wtLines(out)) != 1 || !strings.Contains(out, "serve process exited during model load") {
		t.Errorf("exit %d, output %q; want a failure and one wt: line carrying the job's message", code, out)
	}
	if strings.Contains(out, "Error:") || strings.Contains(out, "Usage:") {
		t.Errorf("cobra printed its own copy or usage:\n%s", out)
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
			h := failingStartServer(t)
			fakeAgentOnPath(t, "pi")
			t.Chdir(t.TempDir())
			stubStdinTTY(t, false)
			out, code := runRootWith(t, h, "--cwd", "-A", "pi", "-M", tc.pin)
			if code != tc.code || len(wtLines(out)) != 1 || !strings.Contains(out, tc.has) {
				t.Errorf("exit %d, output %q; want %d and one wt: line naming %q", code, out, tc.code, tc.has)
			}
			if strings.Contains(out, "Error:") || strings.Contains(out, "Usage:") {
				t.Errorf("cobra printed its own copy or usage:\n%s", out)
			}
		})
	}
}
