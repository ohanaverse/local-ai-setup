package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// TestMain stubs the live seams a cmd/wt-session test would otherwise hit: the
// local-model inventory probe of the host commands (no test may dial the
// developer's real ollama/omlx/mtplx servers) and the host `wt stop`'s engine
// (no test may stop a real model). Every test app's model-server client is
// loopback-only because hooks.client guards it in a test binary.
// Tests that need a probe result call stubProbeInventory.
func TestMain(m *testing.M) {
	// No test may read or write the developer's real config directory —
	// config.IsolateConfigHomeForTest carries the full rationale. A test that
	// sets its own XDG_CONFIG_HOME still wins.
	_, rmConfigHome := config.IsolateConfigHomeForTest()
	// No test may find a git repository it did not make, whether it builds
	// one in a temp directory or changes into a bare one to be outside any —
	// gitenv.IsolateForTest carries the full rationale.
	gitenv.IsolateForTest()
	// No test may read the developer's global or system git config: a global
	// core.hooksPath would send a test's hook into the real hooks directory, a
	// global commit.gpgsign or excludes file changes what the fixtures do.
	// HOME stays (config.IsolateConfigHomeForTest isolates XDG instead).
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1"} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	// No model table may read the developer's tiers.toml (or the one a
	// MODELTIERS_OUT in their shell names): every TIER cell is "-" unless a
	// test names a file of its own (tiers.IsolateForTest).
	tiers.IsolateForTest()
	probeInventory = localmodels.OnDiskSnapshotForTest
	// The model-server client path fails closed: no WT_MODEL_* from the
	// developer's shell reaches a test.
	for _, k := range []string{"WT_MODEL_URL", "WT_MODEL_TOKEN_FILE", "WT_MODEL_TOKEN"} {
		os.Unsetenv(k)
	}
	// wt remove's live-process finder runs lsof over the whole machine: no
	// test scans it. Tests that need processes stub a finder of their own.
	// The real default is kept for TestRemoveDefaultProcFinderIsLsof.
	defaultRemoveProcs = removeProcs
	removeProcs = func() removal.ProcFinder { return nil }
	// A pinned agent's binary-presence check (issue #147) defaults to
	// "installed" so existing tests that pin an agent are unaffected; tests
	// that need to exercise the not-installed path stub this explicitly.
	installed = func(string) bool { return true }
	// `wt stop` seams: no test may probe live servers or stop a real model.
	stopState = func(*config.Config) survey.StopState { return survey.StopState{} }
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		return errors.New("stopEntries not stubbed in this test")
	}
	stopProvider = func(context.Context, *config.Config, string) error {
		return errors.New("stopProvider not stubbed in this test")
	}
	// No test may read the developer's mtplx pidfile or signal one of their
	// processes: the stop of a loading server is refused here, and the
	// engine's own process seams are closed beneath it for a test that
	// swaps the real stop state back in (liveStopState). Tests call
	// stubStopLoading.
	stopLoading = func(_ context.Context, _ *config.Config, id string, pid int) error {
		return fmt.Errorf("stopLoading not stubbed in this test (%s, pid %d)", id, pid)
	}
	lifecycle.IsolateProcessesForTest()
	confirmStop = func(string) (bool, error) { return false, nil }
	// `wt stats` seam: the width is pinned to "not a terminal" so a table
	// does not depend on where `go test` was started.
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
	// The readiness half of the proxy restart a route sync makes (#379): the
	// restart itself is the test's WT_LITELLM_RESTART_CMD, but the probe
	// before it and the wait after it would reach whatever answers at the
	// test config's [litellm] url, the developer's proxy included. Unstubbed,
	// a test sees nothing listening, so no sync waits. Tests of the wait call
	// litellm.StubProxyReadinessForTest themselves.
	litellm.StubProxyReadinessForTest(
		func(context.Context, string, time.Duration) bool { return false },
		func(_ context.Context, url string, _ time.Duration) error {
			return errors.New("proxy readiness wait not stubbed in this test: " + url)
		},
	)
	code := m.Run()
	rmConfigHome()
	os.Exit(code)
}

// stubProbeInventory makes the host commands see snap for the duration of a
// test; the seam is restored on cleanup.
func stubProbeInventory(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := probeInventory
	probeInventory = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { probeInventory = old })
}

// TestConfigHomeIsNotTheDevelopersOwn pins TestMain's throwaway config home:
// everything wt keeps under its config directory — rotation.state, usage.jsonl,
// profile backups and locks — resolves into a temp directory
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

// TestGitConfigIsNotTheDevelopersOwn checks TestMain keeps git off the
// developer's global and system config: GIT_CONFIG_GLOBAL is the null device,
// GIT_CONFIG_NOSYSTEM is set, and `git config --global --list` lists nothing.
// Why: a global core.hooksPath once let a test write a hook into the real
// global hooks directory, and a global gpgsign or excludes file changes what
// the fixtures do.
func TestGitConfigIsNotTheDevelopersOwn(t *testing.T) {
	if got := os.Getenv("GIT_CONFIG_GLOBAL"); got != os.DevNull {
		t.Errorf("GIT_CONFIG_GLOBAL = %q, want %q", got, os.DevNull)
	}
	if os.Getenv("GIT_CONFIG_NOSYSTEM") == "" {
		t.Error("GIT_CONFIG_NOSYSTEM is not set")
	}
	out, _ := exec.Command("git", "config", "--global", "--list").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("git config --global --list = %q, want nothing", out)
	}
}
