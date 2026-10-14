package litellm

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestMain replaces the shell runner with a hard failure so no test in this
// package can ever run the real restart command (launchctl kickstart would
// bounce the developer's live LiteLLM proxy and kill in-flight agent
// requests). Tests that exercise restart set runShell themselves.
//
// The process seam of the default restart is replaced the same way: unstubbed,
// it finds no pid, refuses to signal and reports nothing alive, so no test can
// look up or SIGTERM the developer's live proxy. Tests of the default restart
// set proxyProc themselves (stubDefaultRestart).
func TestMain(m *testing.M) {
	runShell = func(context.Context, string) error {
		return errors.New("runShell not stubbed in this test")
	}
	proxyProc = proxyProcess{
		pid:   func(context.Context) (int, bool) { return 0, false },
		term:  func(int) error { return errors.New("proxyProc.term not stubbed in this test") },
		alive: func(int) bool { return false },
	}
	os.Exit(m.Run())
}
