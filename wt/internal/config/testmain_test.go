package config

import (
	"os"
	"testing"
)

// TestMain gives this package's tests a throwaway config home and clears both
// registry variables (IsolateConfigHomeForTest). Most tests here set their own
// XDG_CONFIG_HOME or MODELMAN_REGISTRY and still win; what this stops is a
// WT_REGISTRY exported in the developer's shell, which outranks both and would
// otherwise send every one of those tests to the developer's real registry.
func TestMain(m *testing.M) {
	_, cleanup := IsolateConfigHomeForTest()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
