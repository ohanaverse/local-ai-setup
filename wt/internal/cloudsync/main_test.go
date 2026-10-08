package cloudsync

import (
	"os"
	"strconv"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestMain keeps this package's tests off the developer's machine. The Apply
// tests go through config.UpdateRegistry, so the config home is a throwaway
// directory and the registry write guard is armed: a test that forgot to
// name its own registry fails instead of rewriting the real one.
func TestMain(m *testing.M) {
	_, cleanup := config.IsolateConfigHomeForTest()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestRegistryWriteGuardIsArmed pins the TestMain above: the guard is off in
// any test binary that did not arm it, and a package that reaches
// config.UpdateRegistry without it can rewrite the developer's registry.
func TestRegistryWriteGuardIsArmed(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("the registry write guard is not armed in this test binary")
	}
}

func f(v float64) *float64 { return &v }

func s(v string) *string { return &v }

// num prints a price for a failure message.
func num(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}
