package launchwarn

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// TestMain keeps git, in this package's tests, to the repositories they make
// (gitenv.IsolateForTest carries the rationale). The package runs git from
// Task 203 on; the call is here from the start.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	os.Exit(m.Run())
}
