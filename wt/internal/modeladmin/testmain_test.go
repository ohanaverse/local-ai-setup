package modeladmin

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestMain keeps this package's tests off the developer's machine: the
// writes it tests go through config.UpdateRegistry, and without the throwaway
// config home (and the write guard it arms) a test that forgot to redirect
// the registry would edit the real one.
func TestMain(m *testing.M) {
	_, cleanup := config.IsolateConfigHomeForTest()
	// No test may run the developer's ollama; stubOllamaShow supplies output.
	runOllamaShow = func(context.Context, string, string) (string, error) {
		return "", errors.New("runOllamaShow not stubbed in this test")
	}
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
