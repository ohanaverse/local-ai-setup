package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testEnv is an env with tiny poll intervals and timeouts so polling tests run
// in milliseconds.
func testEnv() *env {
	e := defaultEnv()
	e.pollInterval = 5 * time.Millisecond
	e.warmupTimeout = 300 * time.Millisecond
	e.loadTimeout = 300 * time.Millisecond
	e.stopTimeout = 200 * time.Millisecond
	e.portUpTimeout = 300 * time.Millisecond
	e.prebindTimeout = 200 * time.Millisecond
	return e
}

// TestProbeClassifiesResponses verifies probe's three outcomes: any HTTP
// response (even 404) is "responded"; a stalled listener is "timedOut"; a
// closed port is neither. The distinction matters: "still holds the port" must
// count a stalled listener but not a refused connection.
func TestProbeClassifiesResponses(t *testing.T) {
	e := testEnv()
	ctx := context.Background()

	notFound := httptest.NewServer(http.NotFoundHandler())
	if responded, timedOut := e.probe(ctx, notFound.URL, time.Second); !responded || timedOut {
		t.Errorf("404 server: responded=%v timedOut=%v, want true,false", responded, timedOut)
	}
	notFound.Close()
	if responded, timedOut := e.probe(ctx, notFound.URL, time.Second); responded || timedOut {
		t.Errorf("closed server: responded=%v timedOut=%v, want false,false", responded, timedOut)
	}

	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer stall.Close()
	if responded, timedOut := e.probe(ctx, stall.URL, 30*time.Millisecond); responded || !timedOut {
		t.Errorf("stalled server: responded=%v timedOut=%v, want false,true", responded, timedOut)
	}
}

// TestWaitPortOpenAndClosed verifies the two waits: waitPortOpen reports true
// for an answering server and false (no error) after its timeout for a dead
// one; portClosedWithin is the mirror image, and a stalled listener does NOT
// count as closed.
func TestWaitPortOpenAndClosed(t *testing.T) {
	e := testEnv()
	ctx := context.Background()
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()

	if ok, err := e.waitPortOpen(ctx, up.URL, 100*time.Millisecond); !ok || err != nil {
		t.Errorf("waitPortOpen(up) = %v, %v", ok, err)
	}
	if closed, err := e.portClosedWithin(ctx, up.URL, 60*time.Millisecond); closed || err != nil {
		t.Errorf("portClosedWithin(up) = %v, %v; want false, nil", closed, err)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if ok, err := e.waitPortOpen(ctx, dead.URL, 60*time.Millisecond); ok || err != nil {
		t.Errorf("waitPortOpen(dead) = %v, %v; want false, nil", ok, err)
	}
	if closed, err := e.portClosedWithin(ctx, dead.URL, 100*time.Millisecond); !closed || err != nil {
		t.Errorf("portClosedWithin(dead) = %v, %v; want true, nil", closed, err)
	}
}

// TestWaitsAbortOnCancel verifies a cancelled context ends both waits promptly
// with the context's error — a user pressing cancel during a long start must
// not wait out the 300s model-load budget.
func TestWaitsAbortOnCancel(t *testing.T) {
	e := testEnv()
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := e.portClosedWithin(ctx, up.URL, 5*time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("portClosedWithin err = %v, want context.Canceled", err)
	}
	if _, err := e.waitPortOpen(ctx, "http://127.0.0.1:1/", 5*time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("waitPortOpen err = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("waits took %v after cancel, want prompt return", time.Since(start))
	}
}

type fakeProc struct {
	done bool
	err  error
}

func (f *fakeProc) exited() (bool, error) { return f.done, f.err }

// TestWaitForModel verifies waitForModel returns once /v1/models lists the
// model (lenient prefix match), times out with a clear error when it never
// does, and fails fast with the process's exit error when the serve process
// dies during load instead of waiting out the whole budget.
func TestWaitForModel(t *testing.T) {
	e := testEnv()
	ctx := context.Background()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"org/Qwen3.8"}]}`))
	}))
	defer srv.Close()
	if err := e.waitForModel(ctx, srv.URL, "Qwen3.8", nil, time.Second); err != nil {
		t.Fatalf("waitForModel: %v", err)
	}

	never := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":[]}`)) }))
	defer never.Close()
	if err := e.waitForModel(ctx, never.URL, "x", nil, 60*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("never-listed err = %v, want timed out", err)
	}

	dead := &fakeProc{done: true, err: errors.New("exit status 3")}
	start := time.Now()
	if err := e.waitForModel(ctx, never.URL, "x", dead, 5*time.Second); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Errorf("dead-proc err = %v, want exited", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("dead process should fail fast, took %v", time.Since(start))
	}
}

// TestWarmupSendsOneTokenChatAndWaitsForCompletion verifies warmup polls the
// health URL, POSTs a 1-token chat completion naming the model, and only
// succeeds when the reply carries the chat.completion marker (an early error
// body is retried). A model is "loaded" only when a real completion came back.
func TestWarmupSendsOneTokenChatAndWaitsForCompletion(t *testing.T) {
	e := testEnv()
	var posts int32
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &lastBody)
		if atomic.AddInt32(&posts, 1) < 2 {
			http.Error(w, "loading", 503)
			return
		}
		_, _ = w.Write([]byte(`{"object": "chat.completion","choices":[]}`))
	}))
	defer srv.Close()

	if err := e.warmup(context.Background(), srv.URL+"/v1/chat/completions", "m1", srv.URL+"/health", time.Second); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if lastBody["model"] != "m1" || lastBody["max_tokens"] != float64(1) || lastBody["stream"] != false {
		t.Errorf("request body = %v, want model m1, max_tokens 1, stream false", lastBody)
	}
	if atomic.LoadInt32(&posts) != 2 {
		t.Errorf("posts = %d, want 2 (first attempt errored)", posts)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"error":"no"}`)) }))
	defer bad.Close()
	if err := e.warmup(context.Background(), bad.URL, "m1", bad.URL, 80*time.Millisecond); err == nil || !strings.Contains(err.Error(), "warm") {
		t.Errorf("never-completes err = %v, want warm-up failure", err)
	}
}

// TestWarmupRejectsNon2xx verifies a non-2xx answer is not accepted as a
// successful warmup even when its body carries the chat.completion marker.
// Accepting it reports a model as resident when it is not, so the caller's
// "ready to use" assumption is wrong on the very next request.
func TestWarmupRejectsNon2xx(t *testing.T) {
	e := testEnv()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	if err := e.warmup(context.Background(), srv.URL, "m", srv.URL, e.warmupTimeout); err == nil {
		t.Error("warmup must fail on a non-2xx response carrying a chat.completion body")
	}
}
