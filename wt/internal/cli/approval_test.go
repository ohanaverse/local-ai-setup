package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
)

// TestApprovalMissingWording pins the exit-3 refusal words both binaries
// print: a missing approval names its flag and what did not happen, a
// declined prompt says so, and only a no-terminal error becomes a refusal.
func TestApprovalMissingWording(t *testing.T) {
	err := ApprovalMissing("--yes", "nothing was stopped")
	if got, want := err.Error(), "approval missing: pass --yes (there is no terminal to ask); nothing was stopped"; got != want {
		t.Errorf("ApprovalMissing = %q, want %q", got, want)
	}
	if ExitApprovalMissing != 3 {
		t.Errorf("ExitApprovalMissing = %d, want 3", ExitApprovalMissing)
	}
	if got := exitcode.Of(err); got != 3 {
		t.Errorf("ApprovalMissing exit = %d, want 3", got)
	}

	err = Declined("nothing was removed")
	if got, want := err.Error(), "approval missing: declined; nothing was removed"; got != want {
		t.Errorf("Declined = %q, want %q", got, want)
	}
	if got := exitcode.Of(err); got != 3 {
		t.Errorf("Declined exit = %d, want 3", got)
	}

	err = NoTerminalRefusal(fmt.Errorf("x: %w", ErrNoTerminal), "--yes", "s")
	if got, want := err.Error(), ApprovalMissing("--yes", "s").Error(); got != want {
		t.Errorf("NoTerminalRefusal(no terminal) = %q, want %q", got, want)
	}
	if got := exitcode.Of(err); got != 3 {
		t.Errorf("NoTerminalRefusal(no terminal) exit = %d, want 3", got)
	}

	if got := NoTerminalRefusal(errors.New("boom"), "--yes", "s").Error(); got != "boom" {
		t.Errorf("NoTerminalRefusal(other) = %q, want %q", got, "boom")
	}
}
