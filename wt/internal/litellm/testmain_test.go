package litellm

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestMain replaces the shell runner with a hard failure so no test in this
// package can ever run the real restart command (launchctl kickstart would
// bounce the developer's live LiteLLM proxy and kill in-flight agent
// requests). Tests that exercise restart set runShell themselves.
func TestMain(m *testing.M) {
	runShell = func(context.Context, string) error {
		return errors.New("runShell not stubbed in this test")
	}
	os.Exit(m.Run())
}
