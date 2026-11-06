package removal

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// TestMain keeps git, in this package's tests, to the repositories they make:
// the fixture builds origins, clones and worktrees in temp directories, and
// removal runs `git worktree remove` and `update-ref -d`, so a leaked GIT_DIR
// would aim them at the developer's repository. gitenv.IsolateForTest carries
// the full rationale. isolateGitConfig keeps the developer's global and system
// git config out too. It also makes the gh seam fail, so a test that forgets
// to stub it errors instead of asking the developer's GitHub account anything.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	home, err := os.MkdirTemp("", "removal-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := isolateGitConfig(home); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cleanup.StubPRListForTest(func(dir, branch string) ([]byte, error) {
		return nil, errors.New("test forgot to stub the gh pr list seam")
	})
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// isolateGitConfig points HOME at home (an empty directory) and makes git read
// no global or system config: a developer's core.hooksPath, init.templateDir,
// push.default or merge driver would otherwise change what the fixture's
// commits, pushes and merges do (a failing hook fails every fixture; a merge
// driver changes what the classifier proves).
func isolateGitConfig(home string) error {
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     home,
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
	} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}
