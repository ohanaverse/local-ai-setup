package main

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// TestMain stubs the live seams a cmd/wt-session test would otherwise hit
// (no test may stop a real model: the host stop moved to wt-model). This binary
// has no local-model inventory probe left to stub: litellm sync, its last
// reader, moved to wt-model. Every test app's model-server client
// is loopback-only because hooks.client guards it in a test binary.
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
	// The model-server client path fails closed: no WT_MODEL_* from the
	// developer's shell reaches a test.
	for _, k := range []string{"WT_MODEL_URL", "WT_MODEL_TOKEN_FILE", "WT_MODEL_TOKEN", "WT_MODEL_MODE"} {
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
	// No test may read the developer's process pidfiles or signal one of
	// their servers (5a removes the last lifecycle use from this binary).
	lifecycle.IsolateProcessesForTest()
	// `wt stats` and `wt model list --server` seam: the width is pinned to
	// "not a terminal" so a table does not depend on where `go test` was
	// started.
	stdoutWidth = func() int { return 0 }
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

// TestMainIsolatesEveryProcessPidfile checks TestMain's
// lifecycle.IsolateProcessesForTest() redirects every process family's
// pidfile env var to a scratch path that is not the real
// /tmp/local-ai-setup-* default: a cmd/wt-session test must never read or signal a
// developer's real server, and sushi's stop reads its pidfile to find the pid
// it signals.
func TestMainIsolatesEveryProcessPidfile(t *testing.T) {
	for _, family := range provider.ProcessFamilies() {
		spec, ok := provider.ProcessFor(family)
		if !ok || spec.PidfileEnv == "" {
			t.Errorf("family %q has no pidfile env var", family)
			continue
		}
		got := os.Getenv(spec.PidfileEnv)
		if got == "" || got == spec.PidfileDefault || !strings.Contains(got, "wt-test-no-"+family+"-") {
			t.Errorf("%s = %q, want a wt-test-no-%s- scratch path, not %q", spec.PidfileEnv, got, family, spec.PidfileDefault)
		}
	}
	if os.Getenv("WT_SUSHI_PIDFILE") == "" {
		t.Error("WT_SUSHI_PIDFILE is unset: TestMain does not isolate the sushi pidfile")
	}
}
