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
