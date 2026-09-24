// wt/internal/agents/run_test.go
package agents

import (
	"errors"
	"os/exec"
	"testing"
)

// TestRunAndCleanupCallsCleanupAfterRunRegardlessOfOutcome verifies
// RunAndCleanup always invokes cleanup exactly once after cmd.Run()
// returns, whether the command succeeds or fails — the shared
// apply->run->cleanup sequence duplicated between cmd/wt's runAgentCmd and
// internal/tui's runAndWaitCmd depends on cleanup running unconditionally
// so a profile's config_content file is always restored.
func TestRunAndCleanupCallsCleanupAfterRunRegardlessOfOutcome(t *testing.T) {
	calls := 0
	cleanup := func() error { calls++; return nil }

	if _, err := RunAndCleanup(exec.Command("true"), cleanup); err != nil {
		t.Fatalf("RunAndCleanup() error = %v, want nil for a successful command", err)
	}
	if calls != 1 {
		t.Errorf("cleanup called %d times after success, want 1", calls)
	}

	if _, err := RunAndCleanup(exec.Command("false"), cleanup); err == nil {
		t.Fatal("RunAndCleanup() error = nil, want the command's own exit error")
	}
	if calls != 2 {
		t.Errorf("cleanup called %d times total, want 2 (once per Run, including the failing one)", calls)
	}
}

// TestRunAndCleanupNeverPromotesCleanupErrorOverCommandError verifies a
// cleanup failure is reported as a warning, never returned as
// RunAndCleanup's own error — cmd.Run()'s result always wins, matching
// both existing call sites' behavior before this helper was extracted.
func TestRunAndCleanupNeverPromotesCleanupErrorOverCommandError(t *testing.T) {
	cleanupErr := errors.New("cleanup boom")
	_, err := RunAndCleanup(exec.Command("true"), func() error { return cleanupErr })
	if err != nil {
		t.Errorf("RunAndCleanup() error = %v, want nil (a successful command's result, not the cleanup error)", err)
	}
}
