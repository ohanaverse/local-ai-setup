package smoke

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// TestMain keeps git, in this package's tests, to the repositories they make:
// NewRowDir makes each row's temp directory a repository.
// gitenv.IsolateForTest carries the full rationale.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	os.Exit(m.Run())
}
