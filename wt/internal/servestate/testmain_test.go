package servestate

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestMain isolates the config home before any test runs: no servestate test
// may reach the developer's wt-model home (its serve.json included), so
// Default() resolves under a scratch directory removed after the run.
func TestMain(m *testing.M) {
	_, rmConfigHome := config.IsolateConfigHomeForTest()
	code := m.Run()
	rmConfigHome()
	os.Exit(code)
}
