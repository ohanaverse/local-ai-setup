package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// TestMain stubs the live seams a cmd/wt test would otherwise hit: the
// local-model inventory probe (no test may dial the developer's real
// ollama/omlx/mtplx servers) and the non-TUI start driver plus the engine
// behind it (no test may start a real model process or let the engine's route
// hook rewrite the real config.yaml). Tests that need a probe result call
// stubProbeInventory; tests that exercise a start call stubStartDriver.
func TestMain(m *testing.M) {
	// No test may read or write the developer's real config directory. A test
	// that launches, records usage, takes a profile lock or loads the config
	// without pointing XDG_CONFIG_HOME somewhere of its own used to land in
	// ~/.config/agent-wt: every run of this package rewrote the real
	// rotation.state (so the picker's last-launched marker pointed at a test
	// model) and appended test launches to the real usage.jsonl. The whole
	// package gets a throwaway config home; a test that sets its own still
	// wins. MODELMAN_REGISTRY is cleared for the same reason: it would send
	// config.Load to the developer's registry whatever XDG says.
	cfgHome, err := os.MkdirTemp("", "wt-test-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", cfgHome)
	os.Unsetenv("MODELMAN_REGISTRY")
	probeInventory = localmodels.OnDiskSnapshotForTest
	// A pinned agent's binary-presence check (issue #147) defaults to
	// "installed" so existing tests that pin an agent are unaffected; tests
	// that need to exercise the not-installed path stub this explicitly.
	installed = func(string) bool { return true }
	startModel = func(*config.Config, catalog.Row, bool) error {
		return errors.New("startModel not stubbed in this test")
	}
	// The engine behind the driver, stubbed for the same reason one level
	// down: an unstubbed test reaching lifecycle.Start would start a real
	// model AND let its route hook rewrite the developer's real config.yaml.
	lifecycleStart = func(context.Context, *config.Config, lifecycle.Target, lifecycle.Options) error {
		return errors.New("lifecycleStart not stubbed in this test")
	}
	// The launch-time route check (#192): an unstubbed test that launches a
	// running local model through LiteLLM would rewrite the developer's real
	// config.yaml and restart their proxy. Tests that assert on it call
	// stubEnsureRoute.
	ensureModelRoute = func(*config.Config, config.Model) bool { return false }
	// Post-exit seams: no test may rewrite the real refcount file or offer to
	// stop the developer's running models.
	releaseSession = func() {}
	runStopPicker = func(*config.Config) {}
	// `wt stop` seams: no test may probe live servers or stop a real model.
	stopCandidates = func(*config.Config) []survey.Candidate { return nil }
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		return errors.New("stopEntries not stubbed in this test")
	}
	stopPickerAll = func(*config.Config) bool { return false }
	confirmStop = func(string) (bool, error) { return false, nil }
	code := m.Run()
	os.RemoveAll(cfgHome)
	os.Exit(code)
}

// startRequest records what a stubbed start driver was asked to do.
type startRequest struct {
	called  bool
	row     catalog.Row
	replace bool
}

// stubProbeInventory makes the non-TUI paths see snap for the duration of a
// test; the seam is restored on cleanup.
func stubProbeInventory(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := probeInventory
	probeInventory = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { probeInventory = old })
}

// stubStartDriver replaces the driver with a recorder returning err, so a
// test can assert which row was started and with which replace permission
// without invoking the lifecycle engine.
func stubStartDriver(t *testing.T, err error) *startRequest {
	t.Helper()
	req := &startRequest{}
	old := startModel
	startModel = func(_ *config.Config, row catalog.Row, replace bool) error {
		req.called, req.row, req.replace = true, row, replace
		return err
	}
	t.Cleanup(func() { startModel = old })
	return req
}

// stubEnsureRoute records, in order, each launch-time route check as
// "ensure:<model id>" and each wait for the proxy as "wait". All three seams
// are restored on cleanup.
//
// osStderr is captured too, though nothing asserts on it: a check that reports
// "changed" sends the flow through waitForProxyRestart, whose progress line
// would otherwise be sprayed over the test log by every launch test here. A
// test that wants to see that line stubs osStderr itself.
func stubEnsureRoute(t *testing.T) *[]string {
	t.Helper()
	var events []string
	oldEnsure, oldWait, oldOut := ensureModelRoute, waitPendingRoutes, osStderr
	ensureModelRoute = func(_ *config.Config, m config.Model) bool {
		events = append(events, "ensure:"+m.ID)
		return true
	}
	waitPendingRoutes = func() { events = append(events, "wait") }
	osStderr = io.Discard
	t.Cleanup(func() { ensureModelRoute, waitPendingRoutes, osStderr = oldEnsure, oldWait, oldOut })
	return &events
}

// TestConfigHomeIsNotTheDevelopersOwn pins TestMain's throwaway config home:
// everything wt keeps under its config directory — rotation.state, usage.jsonl,
// refcount.jsonl, profile backups and locks — resolves into a temp directory
// for this package's tests, never ~/.config. Without it a test that launches a
// stub agent rewrites the developer's real rotation state on every run.
func TestConfigHomeIsNotTheDevelopersOwn(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	for name, p := range map[string]string{"config.Dir()": config.Dir(), "config.RegistryPath()": config.RegistryPath()} {
		if strings.HasPrefix(p, filepath.Join(home, ".config")) || !strings.Contains(p, "wt-test-config-") {
			t.Errorf("%s = %s, want a path under this package's throwaway config home", name, p)
		}
	}
}
