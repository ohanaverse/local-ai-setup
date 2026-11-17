package hoststop

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// TestMain closes the process servers' pidfiles and the process seams
// (lifecycle.IsolateProcessesForTest): a test that reaches the production
// stop state reads no real pidfile and signals nothing.
func TestMain(m *testing.M) {
	lifecycle.IsolateProcessesForTest()
	os.Exit(m.Run())
}
