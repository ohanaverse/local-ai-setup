// wt/internal/agents/run.go
package agents

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// RunAndCleanup runs cmd, then unconditionally calls cleanup (success or
// failure) before returning cmd.Run()'s own error and the elapsed
// duration. It is the one piece of the apply-profile -> run -> cleanup
// sequence that was hand-written identically in both cmd/wt's runAgentCmd
// and internal/tui's runAndWaitCmd; everything before it (applying the
// profile, wiring stdio) and after it (summary, survey, stop picker) still
// differs enough between the TUI and non-TUI launch paths — the TUI's
// capture-then-emit pattern for the alt-screen buffer — to stay separate.
// A cleanup failure is reported to stderr as a warning, never promoted to
// the returned error: cmd.Run()'s own result always wins, matching both
// callers' pre-existing behavior. cleanup must never be nil — pass
// func() error { return nil } when no profile layer applied.
func RunAndCleanup(cmd *exec.Cmd, cleanup func() error) (time.Duration, error) {
	start := time.Now()
	err := cmd.Run()
	if cerr := cleanup(); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: profile cleanup failed: %v\n", cerr)
	}
	return time.Since(start), err
}
