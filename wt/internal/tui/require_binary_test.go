package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireBinary skips the test if the named agent binary is absent from PATH.
// TUI launch-path tests call agents.BuildLaunchCmd, which resolves the real
// binary via exec.LookPath (not the stubbable "installed" seam), so exercising
// the launch wiring needs a binary on PATH. Prefer stubFakeBinary below when
// the test only needs the resolution to *succeed* — that runs everywhere,
// where requireBinary silently drops the coverage on a machine without the
// agent. The contract tests and pure-TUI phase tests remain headless-safe.
func requireBinary(t *testing.T, bins ...string) {
	t.Helper()
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed on PATH; skipping launcher test", bin)
		}
	}
}

// stubFakeBinary puts an executable named bin on PATH for the duration of the
// test. The launcher only needs exec.LookPath to succeed; the shim is never
// executed, so a /bin/sh script is enough and this works on any host — unlike
// requireBinary, which skips. Use it when the assertion is about wt's own
// wiring (which command gets built, what the status line says) rather than
// about a particular agent's behaviour.
func stubFakeBinary(t *testing.T, bins ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, bin := range bins {
		if err := os.WriteFile(filepath.Join(dir, bin), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", bin, err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
