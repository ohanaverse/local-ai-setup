package agents

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// TestMain keeps git, in this package's tests, to the repositories they make:
// its tests build repositories and linked worktrees in temp directories
// (relargs_test.go).
// gitenv.IsolateForTest carries the full rationale. A claude launch reads
// tiers.toml for the model's context window (claudeWindowEnv), so the path is
// pointed at a file that does not exist (tiers.IsolateForTest); a test that
// wants a window sets the model's HostContext (windowModels). The
// environment's own CLAUDE_CODE_MAX_CONTEXT_TOKENS is unset for the same
// reason: declaredWindow (claude_window.go) always yields to the user's
// value, so an ambient one from the developer's shell would silently drop the
// declaration the settings goldens assert; a test that wants it present sets
// it with t.Setenv.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	tiers.IsolateForTest()
	os.Unsetenv(claudeContextVar)
	os.Exit(m.Run())
}
