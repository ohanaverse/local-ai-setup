package worktree

import (
	"errors"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// TestMain keeps git, in this package's tests, to the repositories they make:
// its tests build repositories, remotes and worktrees in temp directories.
// gitenv.IsolateForTest carries the full rationale. It also stubs the
// picker's `gh pr list` (listPRs) to fail, so no test asks the developer's
// GitHub account anything; tests of PRs install their own answer.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	listPRs = func(string) ([]byte, error) {
		return nil, errors.New("listPRs not stubbed in this test")
	}
	os.Exit(m.Run())
}
