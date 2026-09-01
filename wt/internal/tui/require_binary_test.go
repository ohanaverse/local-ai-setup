package tui

import (
	"os/exec"
	"testing"
)

// requireBinary skips the test if the named agent binary is absent from PATH.
// TUI launch-path tests call agents.BuildLaunchCmd, which resolves the real
// binary via exec.LookPath (not the stubbable "installed" seam), so they can
// only exercise the launch wiring on a machine that actually has the agent
// installed. The contract tests and pure-TUI phase tests remain headless-safe.
func requireBinary(t *testing.T, bins ...string) {
	t.Helper()
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed on PATH; skipping launcher test", bin)
		}
	}
}
