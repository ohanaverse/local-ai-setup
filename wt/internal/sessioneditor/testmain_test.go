package sessioneditor

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
)

// testUserHome is the home directory the tests' models abbreviate paths
// against (model.home): the tests' paths are under this one, not the
// developer's.
const testUserHome = "/Users/dev"

// TestMain keeps this package's tests off the developer's machine. The
// Agents tab writes config.toml, so the package needs the throwaway config
// home (which also arms the registry write guard: this editor reads and
// writes no registry, and the guard proves a mistake would fail closed). Git
// is kept to the repositories a test makes (gitenv.IsolateForTest).
func TestMain(m *testing.M) {
	_, cleanup := config.IsolateConfigHomeForTest()
	gitenv.IsolateForTest()
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
