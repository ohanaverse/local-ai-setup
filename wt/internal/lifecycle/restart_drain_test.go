package lifecycle

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// The two lines the default restart can produce while it lets a request in
// flight finish (#377), as package litellm words them for the 3-minute bound.
const (
	drainLine  = "the LiteLLM proxy is finishing a request in flight; it restarts when the request ends (up to 3m0s)"
	cutOffLine = "the LiteLLM proxy was still serving a request after 3m0s; it was restarted anyway and that request was cut off"
)

// TestAsyncRestartNoticeFollowsTheOperation pins where the "finishing a
// request in flight" line goes: to the writer of the operation that caused
// the restart, like its warnings, and to stderr for an operation handed none.
// It is printed from the restart goroutine, which can run for minutes; under
// the TUI's alt screen a line written straight to stderr is never seen.
func TestAsyncRestartNoticeFollowsTheOperation(t *testing.T) {
	_, stderr := stubRoutes(t, wrote("ollama/a:1"), nil)
	restartProxy = func(ctx context.Context) []string {
		litellm.RestartNotice(ctx, drainLine)
		return nil
	}
	mdl := config.Model{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal}
	const want = "wt: LiteLLM route for ollama/a:1 updated\nwt: " + drainLine + "\n"

	var captured bytes.Buffer
	if !EnsureModelRouteTo(&captured, routesCfg(), mdl) {
		t.Fatal("changed = false, want true")
	}
	WaitPendingRoutes()
	if got := captured.String(); got != want {
		t.Fatalf("the check's writer got %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr got %q, want nothing", stderr.String())
	}

	EnsureModelRoute(routesCfg(), mdl)
	WaitPendingRoutes()
	if got := stderr.String(); got != want {
		t.Fatalf("stderr after a check with no writer = %q, want %q", got, want)
	}
}

// TestCutOffWarningIsNotAFailedRestart pins what the drain bound's warning
// means to the asynchronous restart: it is printed, the readiness wait still
// runs, and the restart is remembered as one that worked. The proxy WAS
// restarted, so recording a failure would refuse the next slot-profile
// launch (ProbeProxy) over routes that are live.
func TestCutOffWarningIsNotAFailedRestart(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, nil)
	restartProxy = func(context.Context) []string { return []string{cutOffLine} }
	var waited bool
	waitProxy = func(_ context.Context, _ string, d time.Duration) error {
		if d == proxyReadyTimeout {
			waited = true
		}
		return nil
	}
	cfg := profileCfg()

	bounceRoutes(context.Background(), cfg)
	WaitPendingRoutes()

	if got, want := warn.String(), "wt: "+cutOffLine+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if !waited {
		t.Fatal("the readiness wait did not run after a forced restart")
	}
	if err := ProbeProxy(context.Background(), cfg); err != nil {
		t.Fatalf("ProbeProxy = %v, want nil: the proxy was restarted", err)
	}

	// A real failure beside it still fails the probe, and names only itself.
	restartProxy = func(context.Context) []string { return []string{cutOffLine, "restart command failed"} }
	bounceRoutes(context.Background(), cfg)
	WaitPendingRoutes()
	if err := ProbeProxy(context.Background(), cfg); err == nil || err.Error() != restartFailed("restart command failed") {
		t.Fatalf("ProbeProxy = %v, want %q", err, restartFailed("restart command failed"))
	}
}

// TestRestartProxyToPrintsTheDrainLines pins the synchronous restart of a
// slot-profile launch: the notice and the cut-off warning go to the writer it
// was handed (stderr for none), and a restart forced at the bound is a
// restart that worked — nil, the readiness wait made, the next probe clean.
func TestRestartProxyToPrintsTheDrainLines(t *testing.T) {
	_, stderr := stubRoutes(t, litellm.Result{}, nil)
	restartProxy = func(ctx context.Context) []string {
		litellm.RestartNotice(ctx, drainLine)
		return []string{cutOffLine}
	}
	var waits int
	waitProxy = func(_ context.Context, _ string, d time.Duration) error {
		if d == proxyReadyTimeout {
			waits++
		}
		return nil
	}
	cfg := profileCfg()
	const want = "wt: " + drainLine + "\nwt: " + cutOffLine + "\n"

	var out bytes.Buffer
	if err := RestartProxyTo(context.Background(), &out, cfg); err != nil {
		t.Fatalf("RestartProxyTo = %v, want nil", err)
	}
	if got := out.String(); got != want {
		t.Fatalf("the caller's writer got %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr got %q, want nothing", stderr.String())
	}
	if waits != 1 {
		t.Fatalf("readiness waits = %d, want 1", waits)
	}
	if err := ProbeProxy(context.Background(), cfg); err != nil {
		t.Fatalf("ProbeProxy = %v, want nil", err)
	}

	if err := RestartProxy(context.Background(), cfg); err != nil {
		t.Fatalf("RestartProxy = %v, want nil", err)
	}
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}

	// A failed restart is still the error, and is not also printed.
	restartProxy = func(context.Context) []string { return []string{"restart command failed"} }
	out.Reset()
	err := RestartProxyTo(context.Background(), &out, cfg)
	if err == nil || !strings.Contains(err.Error(), "restart command failed") {
		t.Fatalf("RestartProxyTo = %v, want the failure", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want nothing: the failure is the returned error", out.String())
	}
}
