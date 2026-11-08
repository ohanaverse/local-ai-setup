package dirtsnap

import (
	"fmt"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// TestMain keeps git, in this package's tests, to the repositories they make
// (gitenv.IsolateForTest carries the rationale) and keeps the developer's
// global git config out, since a hooksPath or template would change what the
// fixtures do.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	home, err := os.MkdirTemp("", "dirtsnap-home-")
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
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
