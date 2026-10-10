package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// TestMain stubs the live seams a cmd/wt test would otherwise hit: the
// local-model inventory probe (no test may dial the developer's real
// ollama/omlx/mtplx servers) and the non-TUI start driver plus the engine
// behind it (no test may start a real model process or let the engine's route
// hook rewrite the real config.yaml). Tests that need a probe result call
// stubProbeInventory; tests that exercise a start call stubStartDriver.
func TestMain(m *testing.M) {
	// No test may read or write the developer's real config directory —
	// config.IsolateConfigHomeForTest carries the full rationale. A test that
	// sets its own XDG_CONFIG_HOME still wins.
	_, rmConfigHome := config.IsolateConfigHomeForTest()
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
	// The check that follows a start (#343) probes the provider's server and
	// may remove a route: stubbed for the same reason. Tests that assert on
	// it swap it themselves.
	confirmStarted = func(context.Context, *config.Config, lifecycle.Target) error { return nil }
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
	stopState = func(*config.Config) survey.StopState { return survey.StopState{} }
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		return errors.New("stopEntries not stubbed in this test")
	}
	stopProvider = func(context.Context, *config.Config, string) error {
		return errors.New("stopProvider not stubbed in this test")
	}
	stopPickerAll = func(*config.Config) (bool, []string, map[string]lifecycle.Loading) { return false, nil, nil }
	// No test may read the developer's mtplx pidfile or signal one of their
	// processes: the stop of a loading server is refused here, and the
	// engine's own process seams are closed beneath it for a test that
	// swaps the real stop state back in (liveStopState). Tests call
	// stubStopLoading.
	stopLoading = func(_ context.Context, _ *config.Config, id string, pid int) error {
		return fmt.Errorf("stopLoading not stubbed in this test (%s, pid %d)", id, pid)
	}
	lifecycle.IsolateProcessesForTest()
	// No test may read the developer's refcount file for session counts.
	sessionCounts = func([]string) map[string]int { return map[string]int{} }
	confirmStop = func(string) (bool, error) { return false, nil }
	// `wt stats` seams: no test may resolve the developer's LiteLLM database
	// or run psql against it. Unstubbed, a stats test sees "spend
	// unavailable" (the degraded report) and never a query. Tests that
	// want spend rows call stubSpend. The width is pinned to "not a
	// terminal" so a table does not depend on where `go test` was started.
	querySpend = func(context.Context, time.Time, time.Time) (spend.Result, error) {
		return spend.Result{}, errors.New("querySpend not stubbed in this test")
	}
	stdoutWidth = func() int { return 0 }
	// Registry-write seams: no test may read the developer's PATH to decide
	// what to seed, and none may go on from a registry write to the real
	// route sync (which probes providers and can restart the proxy). Tests of
	// `wt model init` call stubSeedEnv, and realRouteSync for the sync itself.
	// `wt model add` seams: no test may run the developer's ollama or wait on
	// their terminal. Tests call stubOllamaCaps and stubConfirmRemove.
	ollamaCaps = func(*config.Config, string) (map[string]any, error) {
		return nil, errors.New("ollamaCaps not stubbed in this test")
	}
	confirmRemove = func(string) (bool, error) { return false, errors.New("confirmRemove not stubbed in this test") }
	seedEnv = func() config.SeedEnv { return config.SeedEnv{} }
	syncRoutesAfterWrite = func(io.Writer, io.Writer) string {
		return "syncRoutesAfterWrite not stubbed in this test"
	}
	// `wt cloud-sync` seams: no test may fetch a public page, run the
	// developer's ollama (a pull or an rm there changes their machine), or
	// open the terminal to ask. Tests use stubCloudFetch and stubConfirm
	// (cloudsync_test.go) and stubOllama (cloudsync_ollama_test.go).
	cloudFetch = func(_ context.Context, url string) ([]byte, error) {
		return nil, errors.New("cloudFetch not stubbed in this test: " + url)
	}
	ollamaCLI = func(_ context.Context, _ string, args ...string) (string, string, error) {
		return "", "", errors.New("ollamaCLI not stubbed in this test: ollama " + strings.Join(args, " "))
	}
	confirmCloudSync = func(string) (bool, error) {
		return false, errors.New("confirmCloudSync not stubbed in this test")
	}
	// The pre-launch ollama check (#317), reached by a direct launch of an
	// ollama model here and through the TUI: unstubbed it would run whatever
	// `ollama` is on the developer's PATH. Tests that reach it call
	// stubOllamaList (launch_test.go).
	ollamacheck.StubListForTest(func(origin string) ([]string, bool, error) {
		return nil, true, errors.New("ollamacheck list not stubbed in this test (ollama list at " + origin + ")")
	})
	code := m.Run()
	rmConfigHome()
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
	for _, pair := range [][2]string{{"config.Dir()", config.Dir()}, {"config.RegistryPath()", config.RegistryPath()}} {
		if strings.HasPrefix(pair[1], filepath.Join(home, ".config")) || !strings.Contains(pair[1], "wt-test-config-") {
			t.Errorf("%s = %s, want a path under this package's throwaway config home", pair[0], pair[1])
		}
	}
}
