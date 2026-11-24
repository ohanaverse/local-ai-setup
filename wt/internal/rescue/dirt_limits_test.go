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

// TestRecordRefPrefixMatchesRescue pins that cleanup's RecordRefPrefix (the ref
// namespace its ListRecords reads) equals rescue's (the namespace rescue
// writes). Why: both name one namespace but cleanup may not import rescue, so
// a drift would make every record read as absent. It lives here because rescue
// already imports cleanup. Note rootmove's RoundTrip of
// `for-each-ref --format=%(refname) <prefix>` pins only the pattern git
// accepts, not ListRecords' full --format string.
func TestRecordRefPrefixMatchesRescue(t *testing.T) {
	if cleanup.RecordRefPrefix != RecordRefPrefix {
		t.Errorf("cleanup.RecordRefPrefix = %q, rescue RecordRefPrefix = %q", cleanup.RecordRefPrefix, RecordRefPrefix)
	}
}
