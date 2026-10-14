package litellm

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

const testProxyURL = "http://proxy.test:4000"

// fakeBounce records the order of a Bounce's steps and what each was given.
type fakeBounce struct {
	listening bool
	waitErr   error
	restartW  []string
	steps     []string
}

func (f *fakeBounce) bounce() Bounce {
	return Bounce{
		Restart: func(context.Context) []string {
			f.steps = append(f.steps, "restart")
			return f.restartW
		},
		Listening: func(_ context.Context, url string, d time.Duration) bool {
			f.steps = append(f.steps, "probe "+url+" "+d.String())
			return f.listening
		},
		WaitReady: func(_ context.Context, url string, d time.Duration) error {
			f.steps = append(f.steps, "wait "+url+" "+d.String())
			return f.waitErr
		},
	}
}

// stubBounce replaces the default restart (bounce) with fb for one test.
func stubBounce(t *testing.T, fb *fakeBounce) {
	t.Helper()
	old := bounce
	bounce = fb.bounce()
	t.Cleanup(func() { bounce = old })
}

// proxyConfig is testConfig with a [litellm] url, the proxy the default
// restart probes and waits for.
func proxyConfig(url string) *config.Config {
	cfg := testConfig()
	cfg.SetLitellmForTest(config.LitellmState{URL: url})
	return cfg
}

var (
	probeStep = "probe " + testProxyURL + " " + ProxyAliveTimeout.String()
	waitStep  = "wait " + testProxyURL + " " + ProxyReadyTimeout.String()
)

// TestSyncWaitsForAListeningProxy pins #379: `wt litellm sync` (and every
// command that runs it after a registry write) restarts the proxy and used to
// return as soon as the restart command did, while the proxy refused
// connections for seconds more — an agent launched right after met a proxy
// that was not there. Sync's default restart now probes the proxy once before
// the restart and, since it was listening, waits for it to answer again, for
// the same 30s bound the lifecycle path uses, before Sync returns.
func TestSyncWaitsForAListeningProxy(t *testing.T) {
	fb := &fakeBounce{listening: true}
	stubBounce(t, fb)
	o, _, _ := opts(t, "model_list: []\n")
	o.Restart = nil

	res, err := Sync(proxyConfig(testProxyURL+"/"), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("the sync changed nothing, so it made no restart to wait for")
	}
	if want := []string{probeStep, "restart", waitStep}; !slices.Equal(fb.steps, want) {
		t.Fatalf("steps = %q, want %q", fb.steps, want)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %q, want none for a proxy that answered", res.Warnings)
	}
}

// TestSyncSkipsTheWaitWithNothingListening pins the other half of the
// lifecycle path's rule: a proxy URL with nothing behind it (the proxy is not
// running, or routing is off and nobody started it) is not waited for, or
// every sync on such a machine would cost the whole 30s bound.
func TestSyncSkipsTheWaitWithNothingListening(t *testing.T) {
	fb := &fakeBounce{listening: false}
	stubBounce(t, fb)
	o, _, _ := opts(t, "model_list: []\n")
	o.Restart = nil

	res, err := Sync(proxyConfig(testProxyURL), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{probeStep, "restart"}; !slices.Equal(fb.steps, want) {
		t.Fatalf("steps = %q, want %q", fb.steps, want)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %q, want none", res.Warnings)
	}
}

// TestSyncWithNoProxyURLNeitherProbesNorWaits pins that a config with no
// [litellm] url, which names no proxy to look at, only restarts.
func TestSyncWithNoProxyURLNeitherProbesNorWaits(t *testing.T) {
	fb := &fakeBounce{listening: true}
	stubBounce(t, fb)
	o, _, _ := opts(t, "model_list: []\n")
	o.Restart = nil

	if _, err := Sync(proxyConfig(""), nil, o); err != nil {
		t.Fatal(err)
	}
	if want := []string{"restart"}; !slices.Equal(fb.steps, want) {
		t.Fatalf("steps = %q, want %q", fb.steps, want)
	}
}

// TestSyncReportsAFailedWaitAsAWarning pins that a proxy that does not answer
// within the bound never fails the sync: the routes were written, which is
// what the command is for, so the timeout is one more warning after the
// restart's own, printed the way they are.
func TestSyncReportsAFailedWaitAsAWarning(t *testing.T) {
	waitErr := errors.New("LiteLLM proxy not ready at " + testProxyURL + "/health/liveliness: context deadline exceeded")
	fb := &fakeBounce{listening: true, waitErr: waitErr, restartW: []string{"failed to restart LiteLLM proxy (boom)"}}
	stubBounce(t, fb)
	o, _, _ := opts(t, "model_list: []\n")
	o.Restart = nil

	res, err := Sync(proxyConfig(testProxyURL), nil, o)
	if err != nil {
		t.Fatalf("a failed wait failed the sync: %v", err)
	}
	if !res.Changed {
		t.Fatal("want the routes written")
	}
	if want := []string{"failed to restart LiteLLM proxy (boom)", waitErr.Error()}; !slices.Equal(res.Warnings, want) {
		t.Fatalf("warnings = %q, want %q", res.Warnings, want)
	}
}

// TestApplyChangeDefaultRestartWaitsToo pins that the wait belongs to the
// default restart, not to Sync alone: an ApplyChange that restarts (one that
// does not pass NoRestart) gets it as well.
func TestApplyChangeDefaultRestartWaitsToo(t *testing.T) {
	fb := &fakeBounce{listening: true}
	stubBounce(t, fb)
	o, _, _ := opts(t, "model_list: []\n")
	o.Restart = nil
	cfg := proxyConfig(testProxyURL)

	if _, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o); err != nil {
		t.Fatal(err)
	}
	if want := []string{probeStep, "restart", waitStep}; !slices.Equal(fb.steps, want) {
		t.Fatalf("steps = %q, want %q", fb.steps, want)
	}
}

// TestSyncOptionsRestartReplacesTheWait pins that a caller's own
// Options.Restart replaces the whole default, readiness included: the
// lifecycle path writes with NoRestart and runs Bounce on its own goroutine,
// and must never have a synchronous wait slipped in under it.
func TestSyncOptionsRestartReplacesTheWait(t *testing.T) {
	fb := &fakeBounce{listening: true}
	stubBounce(t, fb)
	o, restarts, _ := opts(t, "model_list: []\n")

	if _, err := Sync(proxyConfig(testProxyURL), nil, o); err != nil {
		t.Fatal(err)
	}
	if *restarts != 1 || len(fb.steps) != 0 {
		t.Fatalf("restarts = %d, default steps = %q; want the caller's restart alone", *restarts, fb.steps)
	}
}

// TestSyncDefaultRestartStillGivesTheDrainNotice pins that waiting for the
// proxy did not cost `wt litellm sync` the line #381 added: with the default
// restart (no override), a proxy still finishing a request in flight is
// reported on stderr while the sync waits for it, and the readiness wait
// follows the restart.
func TestSyncDefaultRestartStillGivesTheDrainNotice(t *testing.T) {
	timing := drainTiming{bound: 5 * time.Second, notice: 20 * time.Millisecond, poll: 2 * time.Millisecond, command: time.Second}
	fp := &fakeProxy{pid: 7, found: true, exitAfter: 200 * time.Millisecond}
	stubDefaultRestart(t, fp, timing, nil)
	fb := &fakeBounce{listening: true}
	stubBounce(t, fb)
	bounce.Restart = RestartContext
	o, _, _ := opts(t, "model_list: []\n")
	o.Restart = nil

	stderr := captureStderr(t)
	res, err := Sync(proxyConfig(testProxyURL), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	got := stderr()
	if want := "wt: " + drainNotice(timing.bound).Msg + "\n"; !strings.Contains(got, want) {
		t.Fatalf("stderr = %q, want the drain notice %q", got, want)
	}
	if len(res.Warnings) != 0 || len(fp.terms) != 1 {
		t.Fatalf("warnings = %q, SIGTERMs = %v; want none and one", res.Warnings, fp.terms)
	}
	if want := []string{probeStep, waitStep}; !slices.Equal(fb.steps, want) {
		t.Fatalf("readiness steps = %q, want %q", fb.steps, want)
	}
}

// captureStderr points os.Stderr at a pipe until the returned function is
// called, which restores it and returns what was written.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	restored := false
	restore := func() {
		if !restored {
			restored = true
			os.Stderr = old
			w.Close()
		}
	}
	t.Cleanup(restore)
	return func() string {
		restore()
		return <-done
	}
}

// TestBounceRunOrder pins Bounce.Run itself, the sequence both restart paths
// share: one probe before the restart, the wait after it only when the probe
// found a proxy, the restart's warnings and the wait's error kept apart (the
// lifecycle path remembers them in that order of precedence).
func TestBounceRunOrder(t *testing.T) {
	waitErr := errors.New("not ready")
	for _, tc := range []struct {
		name      string
		url       string
		listening bool
		steps     []string
		waitErr   error
	}{
		{"listening", testProxyURL, true, []string{probeStep, "restart", waitStep}, waitErr},
		{"not listening", testProxyURL, false, []string{probeStep, "restart"}, nil},
		{"no url", "", true, []string{"restart"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb := &fakeBounce{listening: tc.listening, waitErr: waitErr, restartW: []string{"w"}}
			warnings, err := fb.bounce().Run(context.Background(), tc.url)
			if !slices.Equal(fb.steps, tc.steps) {
				t.Fatalf("steps = %q, want %q", fb.steps, tc.steps)
			}
			if !slices.Equal(warnings, []string{"w"}) || !errors.Is(err, tc.waitErr) {
				t.Fatalf("warnings = %q, err = %v; want [w] and %v", warnings, err, tc.waitErr)
			}
		})
	}
}
