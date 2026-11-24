package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// watchHarness runs watchSubscription with a scripted check and records what
// it did; the returned stop waits for the watcher to return.
type watchHarness struct {
	mu       sync.Mutex
	checks   int
	disabled int
	out      bytes.Buffer
	done     chan struct{}
	cancel   context.CancelFunc
}

func startWatch(t *testing.T, script func(n int) ([]string, error)) *watchHarness {
	t.Helper()
	h := &watchHarness{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	var on atomic.Bool
	on.Store(true)
	go func() {
		defer close(h.done)
		watchSubscription(ctx, 2*time.Millisecond,
			func(context.Context) ([]string, error) {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.checks++
				return script(h.checks)
			},
			func() bool {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.disabled++
				return on.CompareAndSwap(true, false)
			},
			&watchWriter{h: h})
	}()
	return h
}

type watchWriter struct{ h *watchHarness }

func (w *watchWriter) Write(p []byte) (int, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	return w.h.out.Write(p)
}

func (h *watchHarness) wait(t *testing.T, what string) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: the watcher did not return", what)
	}
}

// TestWatchSubscriptionTurnsTheModeOffOnAHazardAndStops pins #697: the start
// gate looked once, so a hazard that came later (a foreign keyless anthropic/
// deployment, an ANTHROPIC_BASE_URL in the proxy's environment after a
// restart) left header mode on until serve restarted. The watcher re-runs the
// check, turns the mode off on the first hazard with one line naming it and
// the way back, and then stops: the mode stays off for the run.
func TestWatchSubscriptionTurnsTheModeOffOnAHazardAndStops(t *testing.T) {
	h := startWatch(t, func(n int) ([]string, error) {
		if n < 3 {
			return nil, nil // clean
		}
		return []string{"a keyless anthropic/ deployment is not wt's"}, nil
	})
	h.wait(t, "hazard")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.checks != 3 || h.disabled != 1 {
		t.Errorf("checks = %d, disabled = %d, want the third check to turn it off once and stop", h.checks, h.disabled)
	}
	line := h.out.String()
	if strings.Count(line, "\n") != 1 || !strings.Contains(line, "subscription passthrough turned off while serving") ||
		!strings.Contains(line, "a keyless anthropic/ deployment is not wt's") || !strings.Contains(line, "restart wt-model serve") {
		t.Errorf("line = %q, want one line naming the hazard and the restart", line)
	}
}

// TestWatchSubscriptionKeepsTheModeWhenTheProxyCannotBeRead pins the start
// gate's rule at run time: a check that errors without a hazard (the proxy
// answering slowly, a restart in progress) changes nothing and says nothing,
// and the watcher goes on until its context ends.
func TestWatchSubscriptionKeepsTheModeWhenTheProxyCannotBeRead(t *testing.T) {
	h := startWatch(t, func(int) ([]string, error) { return nil, errors.New("503") })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		n := h.checks
		h.mu.Unlock()
		if n >= 4 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.cancel()
	h.wait(t, "cancel")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.checks < 4 {
		t.Fatalf("only %d checks ran", h.checks)
	}
	if h.disabled != 0 || h.out.Len() != 0 {
		t.Errorf("disabled = %d, output %q after an unreadable proxy, want nothing", h.disabled, h.out.String())
	}
}

// TestWatchSubscriptionStopsWithItsContext pins that the watcher ends with
// serve: a cancelled context returns it at once, before any check.
func TestWatchSubscriptionStopsWithItsContext(t *testing.T) {
	var checks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchSubscription(ctx, time.Hour,
			func(context.Context) ([]string, error) { checks.Add(1); return nil, nil },
			func() bool { return true }, &bytes.Buffer{})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher ignored a cancelled context")
	}
	if checks.Load() != 0 {
		t.Errorf("%d checks ran after cancel", checks.Load())
	}
}

// TestServeTurnsTheModeOffWhenAHazardAppearsAfterTheStart pins #697 through
// the whole command: serve starts with the mode on (the proxy not answering
// keeps it on), the proxy then lists a foreign anthropic/ deployment, and
// within a recheck the live ping stops advertising the mode and serve prints
// one warning naming the deployment, never its api_base.
func TestServeTurnsTheModeOffWhenAHazardAppearsAfterTheStart(t *testing.T) {
	asWTModel(t)
	old := subscriptionRecheckEvery
	subscriptionRecheckEvery = 5 * time.Millisecond
	t.Cleanup(func() { subscriptionRecheckEvery = old })
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	enablePassthrough(t)
	var hazard atomic.Bool
	h := testHost()
	h.ModelInfo = func(context.Context) ([]litellm.DeploymentInfo, error) {
		if hazard.Load() {
			return []litellm.DeploymentInfo{{Provider: "anthropic", ModelName: "hand-claude", Model: "anthropic/claude-x", APIBase: "https://relay.example"}}, nil
		}
		return nil, context.DeadlineExceeded
	}
	read, _, stop := serveUntilListeningOn(t, h, testRuntime(t), writeToken(t, 0o600))
	addr := listeningAddr(t, read())
	if !pingServe(t, addr).SubscriptionPassthrough {
		t.Fatal("the mode is not on at the start")
	}
	hazard.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	for pingServe(t, addr).SubscriptionPassthrough {
		if time.Now().After(deadline) {
			t.Fatal("the mode was still on 10 s after a hazard appeared")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	var turnedOff []string
	for _, l := range warningLines(read()) {
		if strings.Contains(l, "turned off while serving") {
			turnedOff = append(turnedOff, l)
		}
	}
	if len(turnedOff) != 1 || !strings.Contains(turnedOff[0], "hand-claude") || strings.Contains(turnedOff[0], "relay.example") {
		t.Errorf("turned-off warnings = %q, want one naming the deployment and no api_base", turnedOff)
	}
}

// TestServeWithTheModeOffStartsNoWatcher pins that a serve whose mode is off
// (the default) never reads the proxy again after the start: the recheck is
// for the mode alone, as the start gate is.
func TestServeWithTheModeOffStartsNoWatcher(t *testing.T) {
	asWTModel(t)
	old := subscriptionRecheckEvery
	subscriptionRecheckEvery = 2 * time.Millisecond
	t.Cleanup(func() { subscriptionRecheckEvery = old })
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	var calls atomic.Int32
	h := testHost()
	h.ProxyEnv = func() litellm.ProxyEnv { calls.Add(1); return litellm.ProxyEnv{} }
	h.ModelInfo = func(context.Context) ([]litellm.DeploymentInfo, error) { calls.Add(1); return nil, nil }
	read, _, stop := serveUntilListeningOn(t, h, testRuntime(t), writeToken(t, 0o600))
	listeningAddr(t, read())
	time.Sleep(60 * time.Millisecond) // thirty rechecks, if there were a watcher
	if err := stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the proxy was read %d times with the mode off", n)
	}
}
