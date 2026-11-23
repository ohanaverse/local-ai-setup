package litellm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestMain replaces the shell runner with a hard failure so no test in this
// package can ever run the real restart command (launchctl kickstart would
// bounce the developer's live LiteLLM proxy and kill in-flight agent
// requests). Tests that exercise restart set runShell themselves.
//
// The process seam of the default restart is replaced the same way: unstubbed,
// it finds no pid, refuses to signal and reports nothing alive, so no test can
// look up or SIGTERM the developer's live proxy. Tests of the default restart
// set proxyProc themselves (stubDefaultRestart), and the readiness half of
// the default restart is replaced too (see below).
func TestMain(m *testing.M) {
	// The alias half re-reads the registry (config.Load) and the proxy pair
	// comes from the wt-model home: both resolve inside a throwaway config
	// home, with the registry write guard armed.
	_, cleanup := config.IsolateConfigHomeForTest()
	runShell = func(context.Context, string) error {
		return errors.New("runShell not stubbed in this test")
	}
	proxyProc = proxyProcess{
		pid:   func(context.Context) (int, bool) { return 0, false },
		term:  func(int) error { return errors.New("proxyProc.term not stubbed in this test") },
		kill:  func(int) error { return errors.New("proxyProc.kill not stubbed in this test") },
		alive: func(int) bool { return false },
	}
	// The readiness half of the default restart (bounce): unstubbed, a write
	// under a config with a proxy URL would probe and wait on whatever answers
	// there. It sees nothing listening, so it never waits. Tests of the wait
	// set bounce themselves (stubBounce).
	bounce.Listening = func(context.Context, string, time.Duration) bool { return false }
	bounce.WaitReady = func(context.Context, string, time.Duration) error {
		return errors.New("bounce.WaitReady not stubbed in this test")
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestRegistryWriteGuardIsArmedHere verifies this test binary cannot read or
// write the developer's registry: the alias half re-reads the registry
// (Options.loadRegistry's default, config.Load), so without the throwaway
// config home a test that forgot to redirect it would read the real one.
func TestRegistryWriteGuardIsArmedHere(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("the registry write guard is not armed: TestMain must call config.IsolateConfigHomeForTest")
	}
}
