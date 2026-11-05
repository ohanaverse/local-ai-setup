package cleanup

import (
	"errors"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// TestMain keeps git, in this package's tests, to the repositories they make:
// the fixture builds origins, clones and worktrees in temp directories.
// gitenv.IsolateForTest carries the full rationale. It also makes the gh seam
// fail, so a test that forgets to stub it errors instead of asking the
// developer's GitHub account anything.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	ghPRList = func(dir, branch string) ([]byte, error) {
		return nil, errors.New("test forgot to stub ghPRList")
	}
	os.Exit(m.Run())
}
