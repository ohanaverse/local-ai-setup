package gittest

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// TestMain keeps git, in this package's tests, to the repositories they make
// (gitenv.IsolateForTest carries the rationale), keeps the developer's global
// git config out (a hooksPath, template or merge driver would change what the
// fixtures do), and makes the gh seam fail so a test that forgets to stub it
// errors instead of asking a GitHub account anything. CleanupTemplates runs
// just before os.Exit: the seeded templates are process-scoped and no test
// owns their lifetime.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	home, err := os.MkdirTemp("", "gittest-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": home,
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	cleanup.StubPRListForTest(func(dir, branch string) ([]byte, error) {
		return nil, errors.New("test forgot to stub the gh pr list seam")
	})
	code := m.Run()
	CleanupTemplates()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
