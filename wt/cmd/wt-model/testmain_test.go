package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// TestMain isolates the process from the developer's machine before any test
// runs: no config home, git repository, tiers file or model process may be
// reached from a wt-model test. The command seams are the host's (testHost
// isolates them per test); the LiteLLM proxy readiness probe is litellm's
// own package seam, so it is stubbed once here.
func TestMain(m *testing.M) {
	_, rmConfigHome := config.IsolateConfigHomeForTest()
	gitenv.IsolateForTest()
	tiers.IsolateForTest()
	lifecycle.IsolateProcessesForTest()
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
