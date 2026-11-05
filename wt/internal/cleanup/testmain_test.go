package cleanup

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// TestMain keeps git, in this package's tests, to the repositories they make:
// the fixture builds origins, clones and worktrees in temp directories.
// gitenv.IsolateForTest carries the full rationale.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	os.Exit(m.Run())
}
