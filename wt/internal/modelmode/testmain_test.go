package modelmode

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestMain points the config home at a scratch directory for the whole test
// binary and arms the registry write guard, so no test touches the real one.
func TestMain(m *testing.M) {
	_, cleanup := config.IsolateConfigHomeForTest()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
