package removal

import (
	"errors"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// TestMain keeps git, in this package's tests, to the repositories they make:
// the fixture builds origins, clones and worktrees in temp directories, and
// removal runs `git worktree remove` and `update-ref -d`, so a leaked GIT_DIR
// would aim them at the developer's repository. gitenv.IsolateForTest carries
// the full rationale. It also makes the gh seam fail, so a test that forgets
// to stub it errors instead of asking the developer's GitHub account anything.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	cleanup.StubPRListForTest(func(dir, branch string) ([]byte, error) {
		return nil, errors.New("test forgot to stub the gh pr list seam")
	})
	os.Exit(m.Run())
}
