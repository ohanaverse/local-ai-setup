package configeditor

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestMain keeps this package's tests off the developer's machine. The Models
// tab writes registry.toml through config.UpdateRegistry, so the package
// needs the throwaway config home and the registry write guard it arms: a
// test that forgot to redirect the registry would otherwise edit the real
// one.
func TestMain(m *testing.M) {
	_, cleanup := config.IsolateConfigHomeForTest()
	// The tab abbreviates a path under the home directory; the tests' paths
	// are under this one, not the developer's.
	userHome = func() (string, error) { return "/Users/dev", nil }
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestRegistryWriteGuardIsArmedHere verifies this test binary cannot write
// the developer's registry. The guard is off unless a TestMain arms it, so a
// package that reaches the writer and forgot would fail open.
func TestRegistryWriteGuardIsArmedHere(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("the registry write guard is not armed: TestMain must call config.IsolateConfigHomeForTest")
	}
}
