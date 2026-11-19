package main

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// TestMain isolates the process from the developer's machine before any test
// runs: no config home, git repository, tiers file or model process may be
// reached from a wt-model test.
func TestMain(m *testing.M) {
	_, rmConfigHome := config.IsolateConfigHomeForTest()
	gitenv.IsolateForTest()
	tiers.IsolateForTest()
	lifecycle.IsolateProcessesForTest()
	probeInventory = localmodels.OnDiskSnapshotForTest
	// Registry-write seams: no test may read the developer's PATH to decide
	// what to seed, and none may go on from a registry write to the real
	// route sync (which probes providers and can restart the proxy). Tests
	// call stubSeedEnv, and realRouteSync for the sync itself.
	// `wt model add` seams: no test may run the developer's ollama. Tests
	// call stubOllamaCaps.
	ollamaCaps = func(*config.Config, string) (map[string]any, error) {
		return nil, errors.New("ollamaCaps not stubbed in this test")
	}
	seedEnv = func() config.SeedEnv { return config.SeedEnv{} }
	syncRoutesAfterWrite = func(io.Writer, io.Writer) string {
		return "syncRoutesAfterWrite not stubbed in this test"
	}
	code := m.Run()
	rmConfigHome()
	os.Exit(code)
}
