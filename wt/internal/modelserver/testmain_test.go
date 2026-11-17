package modelserver

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// TestMain isolates the developer's config home, git, tiers file and local
// processes from every test in the package, the same four calls cmd/wt-session makes.
func TestMain(m *testing.M) {
	_, rmConfigHome := config.IsolateConfigHomeForTest()
	gitenv.IsolateForTest()
	tiers.IsolateForTest()
	lifecycle.IsolateProcessesForTest()
	code := m.Run()
	rmConfigHome()
	os.Exit(code)
}
