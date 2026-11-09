package rescue

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestDirtLimitsMatchRescue pins that cleanup's duplicated dirt caps equal the
// caps rescue enforces. They are copies because cleanup may not import
// rescue; if they drifted, a state rescue accepted could read as not covered
// (safe) or a state it refused could read as covered (unsafe).
func TestDirtLimitsMatchRescue(t *testing.T) {
	if cleanup.DirtMaxFiles != MaxUntrackedFiles {
		t.Errorf("DirtMaxFiles = %d, rescue MaxUntrackedFiles = %d", cleanup.DirtMaxFiles, MaxUntrackedFiles)
	}
	if cleanup.DirtMaxBytes != MaxUntrackedBytes {
		t.Errorf("DirtMaxBytes = %d, rescue MaxUntrackedBytes = %d", cleanup.DirtMaxBytes, MaxUntrackedBytes)
	}
}
