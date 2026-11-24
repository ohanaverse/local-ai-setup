package lifecycle

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// The two notices the default restart can give while it lets a request in
// flight finish (#377), as package litellm words them for the 3-minute bound.
const (
	drainLine  = "the LiteLLM proxy is finishing a request in flight; it restarts when the request ends (up to 3m0s)"
	cutOffLine = "the LiteLLM proxy was still serving a request after 3m0s; it was restarted anyway and that request was cut off"
)

var (
	drainNotice  = litellm.Notice{Msg: drainLine, InProgress: true}
	cutOffNotice = litellm.Notice{Msg: cutOffLine}
)

// drainThenCutOff stands in for a restart that waited out the bound: both
// notices, and no warning, since the restart was made.
func drainThenCutOff(ctx context.Context) []string {
	litellm.RestartNotice(ctx, drainNotice)
	litellm.RestartNotice(ctx, cutOffNotice)
	return nil
}

// TestAsyncRestartNoticeFollowsTheOperation pins where the "finishing a
// request in flight" line goes: to the writer of the operation that caused
// the restart, like its warnings, and to stderr for an operation handed none.
// It is printed from the restart goroutine, which can run for minutes; under
// the TUI's alt screen a line written straight to stderr is never seen.
func TestAsyncRestartNoticeFollowsTheOperation(t *testing.T) {
	_, stderr := stubRoutes(t, wrote("ollama/a:1"), nil)
	restartProxy = func(ctx context.Context) []string {
		litellm.RestartNotice(ctx, drainNotice)
		return nil
	}
	cfg := routesCfg()
	const want = "wt: " + drainLine + "\n"
	write := func(ctx context.Context) {
		applyAndReport(ctx, cfg, litellm.Change{Add: []config.Model{{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal}}}, restartIfChanged)
		WaitPendingRoutes()
	}

	var captured bytes.Buffer
	write(withRouteOutput(context.Background(), &captured))
	if got := captured.String(); got != want {
		t.Fatalf("the operation's writer got %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr got %q, want nothing", stderr.String())
	}

	write(context.Background())
	if got := stderr.String(); got != want {
		t.Fatalf("stderr after an operation with no writer = %q, want %q", got, want)
	}
}

// noticeWriter is a route writer that takes the restart's notices itself
// (RestartNoticer), as the TUI's does.
type noticeWriter struct {
	bytes.Buffer
	notices []litellm.Notice
}

func (w *noticeWriter) RestartNotice(n litellm.Notice) { w.notices = append(w.notices, n) }

// TestRestartNoticerTakesTheNotices pins the hook a live screen uses: a writer
// that is a RestartNoticer gets each notice whole, InProgress included, and
// none of them is printed into it. The TUI shows the in-progress line while
// it waits; printed into its buffer, the line would be read only after the
// restart, when it is no longer true.
func TestRestartNoticerTakesTheNotices(t *testing.T) {
	_, stderr := stubRoutes(t, litellm.Result{}, nil)
	restartProxy = drainThenCutOff
	waitProxy = func(context.Context, string, time.Duration) error { return nil }

	var w noticeWriter
	BounceProxyTo(context.Background(), &w, routesCfg())
	WaitPendingRoutes()
	if len(w.notices) != 2 || w.notices[0] != drainNotice || w.notices[1] != cutOffNotice {
		t.Fatalf("notices = %+v, want the drain notice, then the cut-off", w.notices)
	}
	if w.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("printed %q to the writer and %q to stderr, want nothing", w.String(), stderr.String())
	}
}

// TestCutOffIsNotAFailedRestart pins what the drain bound's cut-off means to
// the asynchronous restart: it is printed, and the readiness wait still runs,
// because the proxy WAS restarted.
func TestCutOffIsNotAFailedRestart(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, nil)
	restartProxy = func(ctx context.Context) []string {
		litellm.RestartNotice(ctx, cutOffNotice)
		return nil
	}
	var waited bool
	waitProxy = func(_ context.Context, _ string, d time.Duration) error {
		if d == proxyReadyTimeout {
			waited = true
		}
		return nil
	}
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: proxyURL, APIKey: "sk-test-1234"})

	bounceRoutes(context.Background(), cfg)
	WaitPendingRoutes()

	if got, want := warn.String(), "wt: "+cutOffLine+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if !waited {
		t.Fatal("the readiness wait did not run after a forced restart")
	}
}

// TestSettleRoutesToPrintsTheDrainLines pins where a settling restart prints
// for a caller that cannot show stderr (the picker, back on its alt screen):
// the notice and the cut-off go to the writer it handed in, although the
// restart runs detached, and to stderr for a caller that handed in none.
func TestSettleRoutesToPrintsTheDrainLines(t *testing.T) {
	_, stderr := stubRoutes(t, litellm.Result{}, nil)
	restartProxy = drainThenCutOff
	cfg := routesCfg()
	const want = "wt: " + drainLine + "\nwt: " + cutOffLine + "\n"

	var out bytes.Buffer
	SettleRoutesTo(context.Background(), &out, cfg)
	WaitPendingRoutes()
	if got := out.String(); got != want {
		t.Fatalf("the caller's writer got %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr got %q, want nothing", stderr.String())
	}

	SettleRoutes(context.Background(), cfg)
	WaitPendingRoutes()
	if got := stderr.String(); got != want {
		t.Fatalf("stderr after a settle with no writer = %q, want %q", got, want)
	}
}
