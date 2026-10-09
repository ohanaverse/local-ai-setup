package config

import (
	"os"
	"testing"
)

// TestMain gives this package's tests a throwaway config home and clears both
// registry variables (IsolateConfigHomeForTest). Most tests here set their own
// XDG_CONFIG_HOME or WT_REGISTRY and still win; what this stops is a
// WT_REGISTRY or MODELMAN_REGISTRY exported in the developer's shell, which
// outranks XDG_CONFIG_HOME and would otherwise send every test that redirects
// through XDG alone to the developer's real registry. It is also why those
// tests need not clear either name themselves. Two clears remain on purpose:
// litellmStateEnv clears both names, and TestModelDecodesFetchAndDraft clears
// WT_REGISTRY to undo its own set.
func TestMain(m *testing.M) {
	_, cleanup := IsolateConfigHomeForTest()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
