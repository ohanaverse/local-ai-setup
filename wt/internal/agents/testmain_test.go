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
// wants a window swaps loadTiers.
func TestMain(m *testing.M) {
	gitenv.IsolateForTest()
	tiers.IsolateForTest()
	os.Exit(m.Run())
}
