package litellm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRestartCommandPrecedence pins env precedence (WT_ over legacy
// MODELMAN_, then the launchctl fallback) so an existing modelman user's
// exported restart command keeps working after the move.
func TestRestartCommandPrecedence(t *testing.T) {
	t.Setenv("WT_LITELLM_RESTART_CMD", "")
	t.Setenv("MODELMAN_LITELLM_RESTART_CMD", "")
	if got := RestartCommand(); !strings.Contains(got, "launchctl kickstart -k") {
		t.Fatalf("fallback = %q", got)
	}
	t.Setenv("MODELMAN_LITELLM_RESTART_CMD", "legacy")
	if RestartCommand() != "legacy" {
		t.Fatal("legacy env ignored")
	}
	t.Setenv("WT_LITELLM_RESTART_CMD", "new")
	if RestartCommand() != "new" {
		t.Fatal("WT_ env must win")
	}
}

// TestRestartNeverFails pins that a failing restart command yields a warning
// (naming the manual fix) instead of an error: the config write is the
// source of truth and a stale proxy must not fail wt start/stop.
func TestRestartNeverFails(t *testing.T) {
	t.Setenv("WT_LITELLM_RESTART_CMD", "mycmd")
	orig := runShell
	t.Cleanup(func() { runShell = orig })
	var ran string
	runShell = func(_ context.Context, cmd string) error { ran = cmd; return nil }
	if w := Restart(); len(w) != 0 || ran != "mycmd" {
		t.Fatalf("ok restart: warnings=%v ran=%q", w, ran)
	}
	runShell = func(context.Context, string) error { return context.DeadlineExceeded }
	w := Restart()
	if len(w) != 1 || !strings.Contains(w[0], "restart it manually") {
		t.Fatalf("failed restart warnings = %v", w)
	}
}

// TestWaitReady pins the readiness poll: it retries until the proxy answers
// /health/liveliness with 200, and gives up with an error at the timeout, so
// an agent launched right after a restart does not hit connection-refused.
func TestWaitReady(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/liveliness" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	if err := WaitReady(context.Background(), srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) < 3 {
		t.Fatalf("hits = %d, want >=3 (must retry through 503)", hits)
	}
	if err := WaitReady(context.Background(), "http://127.0.0.1:1", 300*time.Millisecond); err == nil {
		t.Fatal("WaitReady on a dead port = nil, want timeout error")
	}
}

// TestListeningHitsLivenessPath pins the shared health request: it dials
// /health/liveliness (once, even with a trailing slash on the base URL), and
// honors a cancelled caller ctx by reporting nothing listening.
func TestListeningHitsLivenessPath(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/liveliness" {
			t.Errorf("path = %s", r.URL.Path)
		}
	}))
	defer up.Close()
	if !Listening(context.Background(), up.URL+"/", 2*time.Second) {
		t.Error("Listening on a healthy proxy = false")
	}
}

// TestRestartContextHonorsCallerCancel pins that the proxy restart runs on the
// CALLER's context: a user who pressed Ctrl+C during a start/stop must not
// keep paying for the restart command's full run time.
func TestRestartContextHonorsCallerCancel(t *testing.T) {
	t.Setenv("WT_LITELLM_RESTART_CMD", "sleep 3")
	orig := runShell
	t.Cleanup(func() { runShell = orig })
	runShell = realRunShell // safe here: the command above is a sleep, not launchctl

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	began := time.Now()
	w := RestartContext(ctx)
	el := time.Since(began)
	if el > time.Second {
		t.Fatalf("a cancelled restart took %v, want it to return at once", el)
	}
	if len(w) != 1 {
		t.Fatalf("warnings = %v, want the cancelled restart reported once", w)
	}
}

// TestListeningOnlyFalseWhenNothingIsThere pins the gate for the post-restart
// wait: a closed port is "not listening" (skip the wait, there is no proxy to
// come back), but a proxy that answers 503 or is too slow to answer within the
// probe timeout still counts as listening. Treating a slow proxy as absent
// used to skip the wait and launch the agent into a proxy mid-restart.
func TestListeningOnlyFalseWhenNothingIsThere(t *testing.T) {
	ctx := context.Background()

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "starting", http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()
	if !Listening(ctx, unhealthy.URL, time.Second) {
		t.Error("a 503 answer must count as listening")
	}

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer slow.Close()
	defer close(release)
	if !Listening(ctx, slow.URL, 50*time.Millisecond) {
		t.Error("a probe timeout must count as listening (busy proxy)")
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	if Listening(ctx, url, time.Second) {
		t.Error("a refused connection must not count as listening")
	}
}
